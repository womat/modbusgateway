package modbusmanager

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	simonmodbus "github.com/simonvetter/modbus"
)

var (
	ErrBusBusy                  = errors.New("bus queue is full")
	ErrQueueTimeout             = errors.New("request waited too long in the bus queue")
	ErrTimeoutMustBePositive    = errors.New("timeout must be greater than zero")
	ErrUnsupportedTransport     = errors.New("unsupported transport")
	ErrMissingTCPSettings       = errors.New("missing tcp settings")
	ErrTCPHostEmpty             = errors.New("tcp host must not be empty")
	ErrTCPPortOutOfRange        = errors.New("tcp port is out of range")
	ErrMissingSerialSettings    = errors.New("missing serial settings")
	ErrSerialPortEmpty          = errors.New("serial port must not be empty")
	ErrSerialBaudRateInvalid    = errors.New("serial baud rate must be greater than zero")
	ErrSerialDataBitsInvalid    = errors.New("serial data bits are invalid")
	ErrUnsupportedSerialParity  = errors.New("unsupported serial parity")
	ErrSerialStopBitsInvalid    = errors.New("serial stop bits are invalid")
	ErrUnexpectedTCPSettings    = errors.New("unexpected tcp settings")
	ErrUnexpectedSerialSettings = errors.New("unexpected serial settings")
)

const (
	// DefaultQueueSize is the number of requests that may wait on a bus.
	DefaultQueueSize = 64
	// DefaultMaxWait is how long a request may wait in the queue before it is dropped
	// without reaching the bus.
	DefaultMaxWait = 10 * time.Second
)

// BusConfig describes one physical bus: a serial port or a TCP endpoint.
type BusConfig struct {
	Name      string
	Type      string        // tcp | rtu
	Timeout   time.Duration // response timeout of one request
	QueueSize int           // requests that may wait; 0 = DefaultQueueSize
	MaxWait   time.Duration // how long a request may wait in the queue; 0 = DefaultMaxWait
	TCP       *TCPConfig
	Serial    *SerialConfig
}

// TCPConfig contains network settings for Modbus TCP buses.
type TCPConfig struct {
	Host string
	Port int
}

// SerialConfig contains line settings for Modbus RTU buses.
type SerialConfig struct {
	Port     string
	BaudRate int
	DataBits int
	Parity   string
	StopBits int
}

// busClient is the part of the simonvetter client a bus uses; tests replace it.
type busClient interface {
	Open() error
	Close() error
	SetUnitId(id uint8) error
	ReadCoils(addr, quantity uint16) ([]bool, error)
	ReadDiscreteInputs(addr, quantity uint16) ([]bool, error)
	ReadRawBytes(addr, quantity uint16, regType simonmodbus.RegType) ([]byte, error)
	WriteCoil(addr uint16, value bool) error
	WriteCoils(addr uint16, values []bool) error
	WriteRegister(addr, value uint16) error
	WriteRegisters(addr uint16, values []uint16) error
}

// busStats counts the transactions that reached the bus; guarded by bus.mu.
type busStats struct {
	transactions uint64        // requests executed on the bus, failed ones included
	errors       uint64        // of them: exceptions, timeouts and connection failures
	timeouts     uint64        // of them: the device did not answer in time
	busy         time.Duration // time the bus spent on them
}

// job is one request on a bus. A nil op only opens the connection.
type job struct {
	unitId   uint8
	deadline time.Time
	op       func(busClient) (any, error)
	done     chan jobResult

	read       *span // reads: the cache key; identical reads wait on waiters
	ttl        time.Duration
	waiters    []chan jobResult // guarded by bus.mu
	invalidate *span            // writes: the cache entries the write makes stale
}

type jobResult struct {
	value  any
	err    error
	cached bool
}

// bus runs one worker goroutine that owns the only client of the bus.
type bus struct {
	cfg       BusConfig
	newClient func() (busClient, error)

	writes chan *job
	reads  chan *job
	stop   chan struct{}
	wg     sync.WaitGroup

	mu            sync.Mutex // guards inflight, cache, the connection state and stats
	inflight      map[span]*job
	cache         cache
	connected     bool
	lastConnectAt time.Time
	stats         busStats

	client busClient // used by the worker only
}

func newBus(cfg BusConfig, newClient func() (busClient, error)) *bus {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = DefaultQueueSize
	}
	if cfg.MaxWait <= 0 {
		cfg.MaxWait = DefaultMaxWait
	}
	cfg.Type = strings.ToLower(cfg.Type)

	b := &bus{
		cfg:       cfg,
		newClient: newClient,
		writes:    make(chan *job, cfg.QueueSize),
		reads:     make(chan *job, cfg.QueueSize),
		stop:      make(chan struct{}),
		inflight:  make(map[span]*job),
	}
	b.wg.Go(b.run)
	return b
}

// read returns the value of a read: from the cache when an entry for key is younger than ttl,
// else from the bus. Identical reads that wait at the same time share one bus transaction.
func (b *bus) read(key span, ttl time.Duration, op func(busClient) (any, error)) (any, bool, error) {
	b.mu.Lock()
	if ttl > 0 {
		if value, ok := b.cache.get(key, time.Now()); ok {
			b.mu.Unlock()
			return value, true, nil
		}
	}
	if pending, ok := b.inflight[key]; ok {
		waiter := make(chan jobResult, 1)
		pending.waiters = append(pending.waiters, waiter)
		b.mu.Unlock()
		return b.wait(waiter)
	}

	j := &job{
		unitId:   key.unitId,
		deadline: time.Now().Add(b.cfg.MaxWait),
		op:       op,
		done:     make(chan jobResult, 1),
		read:     &key,
		ttl:      ttl,
	}
	b.inflight[key] = j
	b.mu.Unlock()

	if err := b.submit(j, b.reads); err != nil {
		b.mu.Lock()
		delete(b.inflight, key)
		waiters := j.waiters
		b.mu.Unlock()
		for _, waiter := range waiters {
			waiter <- jobResult{err: err}
		}
		return nil, false, err
	}
	return b.wait(j.done)
}

// write executes op in queue order, before any waiting read. The cache entries that overlap
// written are dropped, whether the write succeeded or not.
func (b *bus) write(unitId uint8, written span, op func(busClient) error) error {
	j := &job{
		unitId:     unitId,
		deadline:   time.Now().Add(b.cfg.MaxWait),
		op:         func(c busClient) (any, error) { return nil, op(c) },
		done:       make(chan jobResult, 1),
		invalidate: &written,
	}
	if err := b.submit(j, b.writes); err != nil {
		return err
	}
	_, _, err := b.wait(j.done)
	return err
}

// connect opens the connection unless it is open already.
func (b *bus) connect() error {
	j := &job{deadline: time.Now().Add(b.cfg.MaxWait), done: make(chan jobResult, 1)}
	if err := b.submit(j, b.writes); err != nil {
		return err
	}
	_, _, err := b.wait(j.done)
	return err
}

func (b *bus) submit(j *job, queue chan *job) error {
	select {
	case <-b.stop:
		return ErrManagerClosed
	default:
	}
	select {
	case queue <- j:
		return nil
	default:
		return fmt.Errorf("%w: bus %q", ErrBusBusy, b.cfg.Name)
	}
}

func (b *bus) wait(result chan jobResult) (any, bool, error) {
	select {
	case r := <-result:
		return r.value, r.cached, r.err
	case <-b.stop:
		return nil, false, ErrManagerClosed
	}
}

// run executes the jobs one after the other, writes first.
func (b *bus) run() {
	defer b.closeClient()
	for {
		var j *job
		select {
		case j = <-b.writes:
		default:
			select {
			case j = <-b.writes:
			case j = <-b.reads:
			case <-b.stop:
				return
			}
		}
		start := time.Now()
		result := b.execute(j)
		b.count(j, result, time.Since(start))
		b.finish(j, result)
	}
}

// count adds a request that reached the bus to the stats; connect-only jobs and requests
// dropped from the queue do not count.
func (b *bus) count(j *job, result jobResult, took time.Duration) {
	if j.op == nil || errors.Is(result.err, ErrQueueTimeout) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stats.transactions++
	b.stats.busy += took
	if result.err != nil {
		b.stats.errors++
		if IsTimeout(result.err) {
			b.stats.timeouts++
		}
	}
}

func (b *bus) execute(j *job) jobResult {
	if time.Now().After(j.deadline) {
		return jobResult{err: fmt.Errorf("%w: bus %q", ErrQueueTimeout, b.cfg.Name)}
	}
	if err := b.open(); err != nil {
		return jobResult{err: fmt.Errorf("connect bus %q: %w", b.cfg.Name, err)}
	}
	if j.op == nil {
		return jobResult{}
	}

	value, err := b.do(j)
	if err == nil || isModbusException(err) {
		// An exception is an answer of the device: the connection is fine.
		return jobResult{value: value, err: err}
	}

	// A transport error: open the connection again and retry once.
	b.closeClient()
	if reopenErr := b.open(); reopenErr != nil {
		return jobResult{err: fmt.Errorf("%w; reconnect bus %q failed: %v", err, b.cfg.Name, reopenErr)}
	}
	value, err = b.do(j)
	if err != nil && !isModbusException(err) {
		b.closeClient()
		return jobResult{err: fmt.Errorf("after reconnect: %w", err)}
	}
	return jobResult{value: value, err: err}
}

func (b *bus) do(j *job) (any, error) {
	if err := b.client.SetUnitId(j.unitId); err != nil {
		return nil, err
	}
	return j.op(b.client)
}

// finish updates the cache and hands the result to the caller and to every coalesced reader.
func (b *bus) finish(j *job, result jobResult) {
	b.mu.Lock()
	var waiters []chan jobResult
	if j.read != nil {
		delete(b.inflight, *j.read)
		waiters = j.waiters
		if result.err == nil && j.ttl > 0 {
			b.cache.put(*j.read, result.value, time.Now().Add(j.ttl))
		}
	}
	if j.invalidate != nil {
		b.cache.invalidate(*j.invalidate)
	}
	b.mu.Unlock()

	j.done <- result
	for _, waiter := range waiters {
		waiter <- result
	}
}

func (b *bus) open() error {
	if b.client != nil {
		return nil
	}
	client, err := b.newClient()
	if err == nil {
		err = client.Open()
	}
	if err != nil {
		return err
	}

	b.client = client
	b.mu.Lock()
	b.connected = true
	b.lastConnectAt = time.Now().UTC()
	b.mu.Unlock()
	return nil
}

// closeClient closes the connection; the next job opens it again.
func (b *bus) closeClient() {
	if b.client == nil {
		return
	}
	_ = b.client.Close()
	b.client = nil
	b.mu.Lock()
	b.connected = false
	b.mu.Unlock()
}

func (b *bus) close() error {
	select {
	case <-b.stop:
		return nil
	default:
	}
	close(b.stop)
	b.wg.Wait()
	return nil
}

func (b *bus) connection() (connected bool, lastConnectAt time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.connected, b.lastConnectAt
}

func (b *bus) queueLen() int {
	return len(b.writes) + len(b.reads)
}

func (b *bus) status() BusStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := BusStatus{
		Name:          b.cfg.Name,
		Type:          b.cfg.Type,
		Address:       b.address(),
		Connected:     b.connected,
		LastConnectAt: b.lastConnectAt,
		QueueLen:      len(b.writes) + len(b.reads),
		QueueSize:     b.cfg.QueueSize,
		Transactions:  b.stats.transactions,
		Errors:        b.stats.errors,
		Timeouts:      b.stats.timeouts,
		BusyTime:      b.stats.busy,
	}
	return s
}

// address describes where the bus is: host:port, or the serial port with its line settings.
func (b *bus) address() string {
	switch {
	case b.cfg.TCP != nil:
		return net.JoinHostPort(b.cfg.TCP.Host, strconv.Itoa(b.cfg.TCP.Port))
	case b.cfg.Serial != nil:
		s := b.cfg.Serial
		return fmt.Sprintf("%s %d %d%s%d", s.Port, s.BaudRate, s.DataBits, strings.ToUpper(s.Parity), s.StopBits)
	}
	return ""
}

// modbusExceptions are the errors simonvetter returns for an exception response.
var modbusExceptions = []error{
	simonmodbus.ErrIllegalFunction,
	simonmodbus.ErrIllegalDataAddress,
	simonmodbus.ErrIllegalDataValue,
	simonmodbus.ErrServerDeviceFailure,
	simonmodbus.ErrAcknowledge,
	simonmodbus.ErrServerDeviceBusy,
	simonmodbus.ErrMemoryParityError,
	simonmodbus.ErrGWPathUnavailable,
	simonmodbus.ErrGWTargetFailedToRespond,
}

// isModbusException reports whether err is an exception response of the device rather than a
// transport error.
func isModbusException(err error) bool {
	for _, exception := range modbusExceptions {
		if errors.Is(err, exception) {
			return true
		}
	}
	return false
}

// clientFactory validates cfg and returns a function that creates a simonvetter client for it.
func clientFactory(cfg BusConfig) (func() (busClient, error), error) {
	conf, err := clientConfiguration(cfg)
	if err != nil {
		return nil, err
	}
	return func() (busClient, error) {
		client, err := simonmodbus.NewClient(conf)
		if err != nil {
			return nil, fmt.Errorf("configure client for bus %q: %w", cfg.Name, err)
		}
		return client, nil
	}, nil
}

func clientConfiguration(cfg BusConfig) (*simonmodbus.ClientConfiguration, error) {
	if cfg.Timeout <= 0 {
		return nil, fmt.Errorf("%w for bus %q", ErrTimeoutMustBePositive, cfg.Name)
	}
	conf := &simonmodbus.ClientConfiguration{Timeout: cfg.Timeout}

	switch strings.ToLower(cfg.Type) {
	case "tcp":
		switch {
		case cfg.TCP == nil:
			return nil, fmt.Errorf("%w for bus %q", ErrMissingTCPSettings, cfg.Name)
		case cfg.Serial != nil:
			return nil, fmt.Errorf("%w for bus %q", ErrUnexpectedSerialSettings, cfg.Name)
		case cfg.TCP.Host == "":
			return nil, fmt.Errorf("%w for bus %q", ErrTCPHostEmpty, cfg.Name)
		case cfg.TCP.Port < 1 || cfg.TCP.Port > 65535:
			return nil, fmt.Errorf("%w for bus %q: %d", ErrTCPPortOutOfRange, cfg.Name, cfg.TCP.Port)
		}
		conf.URL = "tcp://" + net.JoinHostPort(cfg.TCP.Host, strconv.Itoa(cfg.TCP.Port))
	case "rtu":
		switch s := cfg.Serial; {
		case s == nil:
			return nil, fmt.Errorf("%w for bus %q", ErrMissingSerialSettings, cfg.Name)
		case cfg.TCP != nil:
			return nil, fmt.Errorf("%w for bus %q", ErrUnexpectedTCPSettings, cfg.Name)
		case s.Port == "":
			return nil, fmt.Errorf("%w for bus %q", ErrSerialPortEmpty, cfg.Name)
		case s.BaudRate <= 0:
			return nil, fmt.Errorf("%w for bus %q", ErrSerialBaudRateInvalid, cfg.Name)
		case s.DataBits < 5 || s.DataBits > 8:
			return nil, fmt.Errorf("%w for bus %q: %d", ErrSerialDataBitsInvalid, cfg.Name, s.DataBits)
		case s.StopBits != 1 && s.StopBits != 2:
			return nil, fmt.Errorf("%w for bus %q: %d", ErrSerialStopBitsInvalid, cfg.Name, s.StopBits)
		}
		parity, err := mapParity(cfg.Serial.Parity)
		if err != nil {
			return nil, fmt.Errorf("%w for bus %q", err, cfg.Name)
		}
		conf.URL = "rtu://" + cfg.Serial.Port
		conf.Speed = uint(cfg.Serial.BaudRate)
		conf.DataBits = uint(cfg.Serial.DataBits)
		conf.Parity = parity
		conf.StopBits = uint(cfg.Serial.StopBits)
	default:
		return nil, fmt.Errorf("%w %q for bus %q", ErrUnsupportedTransport, cfg.Type, cfg.Name)
	}

	return conf, nil
}

func mapParity(parity string) (uint, error) {
	switch strings.ToUpper(parity) {
	case "N":
		return simonmodbus.PARITY_NONE, nil
	case "E":
		return simonmodbus.PARITY_EVEN, nil
	case "O":
		return simonmodbus.PARITY_ODD, nil
	default:
		return 0, ErrUnsupportedSerialParity
	}
}
