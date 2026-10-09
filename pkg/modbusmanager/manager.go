// Package modbusmanager serializes the access to Modbus buses.
//
// Every physical bus - a serial port or a TCP endpoint - has exactly one worker goroutine that
// owns the only client of that bus, so two requests never meet on the wire. Writes are queued
// and executed in order, before waiting reads. Reads are answered from a short-lived cache when
// possible, and identical reads that wait at the same time share one bus transaction.
//
// Devices are registered on a bus with their unit ID; several devices can share one bus.
package modbusmanager

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrManagerNotInitialized   = errors.New("modbus manager is not initialized")
	ErrManagerClosed           = errors.New("modbus manager is closed")
	ErrDeviceNameEmpty         = errors.New("device name must not be empty")
	ErrDeviceAlreadyRegistered = errors.New("device is already registered")
	ErrDeviceNotConfigured     = errors.New("device is not configured")
	ErrBusNameEmpty            = errors.New("bus name must not be empty")
	ErrBusAlreadyRegistered    = errors.New("bus is already registered")
	ErrBusNotConfigured        = errors.New("bus is not configured")
	ErrUnitIdOutOfRange        = errors.New("unit id must be 1-247")
	ErrUnitIdInUse             = errors.New("unit id is already used on this bus")
	ErrFunctionNotAllowed      = errors.New("function code is not allowed for this device")
	ErrUnsupportedFunction     = errors.New("unsupported function code")
)

// DefaultFunctions are the function codes a device allows when its configuration names none:
// reading only.
var DefaultFunctions = []uint8{1, 2, 3, 4}

// SupportedFunctions are the function codes the manager can execute.
var SupportedFunctions = []uint8{1, 2, 3, 4, 5, 6, 15, 16}

// DeviceConfig describes one Modbus device on a registered bus.
type DeviceConfig struct {
	Name        string
	Description string
	Bus         string        // name of a bus registered with RegisterBus
	UnitId      uint8         // address of the device on its bus, 1-247
	CacheTTL    time.Duration // how long a read stays valid in the cache; 0 = no cache
	Functions   []uint8       // allowed function codes; empty = DefaultFunctions
}

// DeviceStatus reports the current manager view of one configured device.
type DeviceStatus struct {
	Name          string
	Description   string
	Bus           string
	Transport     string // type of the bus: tcp | rtu
	UnitId        uint8
	Functions     []uint8
	Connected     bool // the bus connection is open
	LastError     string
	LastConnectAt time.Time // of the bus
	LastSuccessAt time.Time
	LastRequestAt time.Time
	QueueLen      int // requests waiting on the bus
	CacheHits     uint64
	CacheMisses   uint64
}

// Manager owns the buses and the devices on them.
type Manager struct {
	mu      sync.RWMutex
	buses   map[string]*bus
	devices map[string]*device
	closed  bool
}

type device struct {
	cfg       DeviceConfig
	bus       *bus
	functions [256]bool

	mu            sync.Mutex
	lastError     string
	lastSuccessAt time.Time
	lastRequestAt time.Time

	cacheHits   atomic.Uint64
	cacheMisses atomic.Uint64
}

// New creates a new empty manager.
func New() *Manager {
	return &Manager{
		buses:   make(map[string]*bus),
		devices: make(map[string]*device),
	}
}

// RegisterBus adds a bus and starts its worker. The connection is opened by Connect or by the
// first request.
func (m *Manager) RegisterBus(cfg BusConfig) error {
	if cfg.Name == "" {
		return ErrBusNameEmpty
	}
	newClient, err := clientFactory(cfg)
	if err != nil {
		return err
	}
	return m.addBus(cfg, newClient)
}

// addBus registers a bus with the given client factory; tests pass a fake.
func (m *Manager) addBus(cfg BusConfig, newClient func() (busClient, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return ErrManagerClosed
	}
	if _, ok := m.buses[cfg.Name]; ok {
		return fmt.Errorf("%w: %s", ErrBusAlreadyRegistered, cfg.Name)
	}
	m.buses[cfg.Name] = newBus(cfg, newClient)
	return nil
}

// Register adds a device on a registered bus. The unit ID must be unique on that bus.
func (m *Manager) Register(cfg DeviceConfig) error {
	if cfg.Name == "" {
		return ErrDeviceNameEmpty
	}
	if cfg.UnitId < 1 || cfg.UnitId > 247 {
		return fmt.Errorf("%w for device %q: %d", ErrUnitIdOutOfRange, cfg.Name, cfg.UnitId)
	}
	if len(cfg.Functions) == 0 {
		cfg.Functions = DefaultFunctions
	}
	cfg.Functions = slices.Clone(cfg.Functions)
	slices.Sort(cfg.Functions)

	d := &device{cfg: cfg}
	for _, fc := range cfg.Functions {
		if !slices.Contains(SupportedFunctions, fc) {
			return fmt.Errorf("%w for device %q: %d", ErrUnsupportedFunction, cfg.Name, fc)
		}
		d.functions[fc] = true
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return ErrManagerClosed
	}
	if _, ok := m.devices[cfg.Name]; ok {
		return fmt.Errorf("%w: %s", ErrDeviceAlreadyRegistered, cfg.Name)
	}
	b, ok := m.buses[cfg.Bus]
	if !ok {
		return fmt.Errorf("%w for device %q: %q", ErrBusNotConfigured, cfg.Name, cfg.Bus)
	}
	for _, other := range m.devices {
		if other.bus == b && other.cfg.UnitId == cfg.UnitId {
			return fmt.Errorf("%w: devices %q and %q on bus %q use unit id %d",
				ErrUnitIdInUse, other.cfg.Name, cfg.Name, cfg.Bus, cfg.UnitId)
		}
	}

	d.bus = b
	m.devices[cfg.Name] = d
	return nil
}

// Connect opens the connection of every bus that is not open yet, all buses at the same time,
// so an unreachable device delays the start by one timeout at most. It returns the errors of
// the buses that could not be opened; those are retried on their next request.
func (m *Manager) Connect() map[string]error {
	m.mu.RLock()
	buses := make([]*bus, 0, len(m.buses))
	for _, b := range m.buses {
		buses = append(buses, b)
	}
	m.mu.RUnlock()

	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		failed = make(map[string]error)
	)
	for _, b := range buses {
		wg.Go(func() {
			if err := b.connect(); err != nil {
				mu.Lock()
				failed[b.cfg.Name] = err
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return failed
}

// Status returns the current status for one registered device.
func (m *Manager) Status(name string) (DeviceStatus, error) {
	d, err := m.device(name)
	if err != nil {
		return DeviceStatus{}, err
	}
	return d.status(), nil
}

// ListStatus returns the current status for all registered devices, sorted by name.
func (m *Manager) ListStatus() []DeviceStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.devices))
	for name := range m.devices {
		names = append(names, name)
	}
	sort.Strings(names)

	statuses := make([]DeviceStatus, 0, len(names))
	for _, name := range names {
		statuses = append(statuses, m.devices[name].status())
	}
	return statuses
}

// Close stops the workers and closes all bus connections. Requests still waiting fail with
// ErrManagerClosed.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	buses := make([]*bus, 0, len(m.buses))
	for _, b := range m.buses {
		buses = append(buses, b)
	}
	m.mu.Unlock()

	var errs error
	for _, b := range buses {
		errs = errors.Join(errs, b.close())
	}
	return errs
}

func (m *Manager) device(name string) (*device, error) {
	if name == "" {
		return nil, ErrDeviceNameEmpty
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, ErrManagerClosed
	}
	d, ok := m.devices[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrDeviceNotConfigured, name)
	}
	return d, nil
}

// allow returns ErrFunctionNotAllowed unless the device allows function code fc.
func (d *device) allow(fc uint8) error {
	if !d.functions[fc] {
		return fmt.Errorf("%w: FC%d on device %q", ErrFunctionNotAllowed, fc, d.cfg.Name)
	}
	return nil
}

// record notes the outcome of one request for the status.
func (d *device) record(err error, read, cached bool) {
	if read {
		if cached {
			d.cacheHits.Add(1)
		} else {
			d.cacheMisses.Add(1)
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now().UTC()
	d.lastRequestAt = now
	if err != nil {
		d.lastError = err.Error()
		return
	}
	d.lastError = ""
	d.lastSuccessAt = now
}

func (d *device) status() DeviceStatus {
	connected, lastConnectAt := d.bus.connection()

	d.mu.Lock()
	defer d.mu.Unlock()
	return DeviceStatus{
		Name:          d.cfg.Name,
		Description:   d.cfg.Description,
		Bus:           d.cfg.Bus,
		Transport:     d.bus.cfg.Type,
		UnitId:        d.cfg.UnitId,
		Functions:     slices.Clone(d.cfg.Functions),
		Connected:     connected,
		LastError:     d.lastError,
		LastConnectAt: lastConnectAt,
		LastSuccessAt: d.lastSuccessAt,
		LastRequestAt: d.lastRequestAt,
		QueueLen:      d.bus.queueLen(),
		CacheHits:     d.cacheHits.Load(),
		CacheMisses:   d.cacheMisses.Load(),
	}
}
