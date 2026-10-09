package modbusmanager

import (
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	simonmodbus "github.com/simonvetter/modbus"
)

// fakeClient is a bus with register memory per unit ID. It counts the transactions, can delay
// or hold them, and records the largest number of transactions that ran at the same time.
type fakeClient struct {
	mu        sync.Mutex
	unitId    uint8
	registers map[uint8]map[uint16]uint16
	coils     map[uint8]map[uint16]bool
	log       []string // "<unit>:<op>" in execution order

	delay     time.Duration
	openDelay time.Duration // how long Open takes
	gate      chan struct{} // when set, every transaction waits for a receive
	failNext  error         // returned once by the next transaction
	exception error         // returned by every transaction

	calls     atomic.Int32
	opens     atomic.Int32
	active    atomic.Int32
	maxActive atomic.Int32
}

func newFakeClient() *fakeClient {
	return &fakeClient{registers: map[uint8]map[uint16]uint16{}, coils: map[uint8]map[uint16]bool{}}
}

func (f *fakeClient) Open() error {
	time.Sleep(f.openDelay)
	f.opens.Add(1)
	return nil
}

func (f *fakeClient) Close() error { return nil }

func (f *fakeClient) SetUnitId(id uint8) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unitId = id
	return nil
}

// transaction simulates the bus time of one request.
func (f *fakeClient) transaction(op string) error {
	f.calls.Add(1)
	if n := f.active.Add(1); n > f.maxActive.Load() {
		f.maxActive.Store(n)
	}
	defer f.active.Add(-1)

	if f.gate != nil {
		<-f.gate
	}
	time.Sleep(f.delay)

	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, string(rune('0'+f.unitId))+":"+op)
	if err := f.failNext; err != nil {
		f.failNext = nil
		return err
	}
	return f.exception
}

func (f *fakeClient) regs() map[uint16]uint16 {
	if f.registers[f.unitId] == nil {
		f.registers[f.unitId] = map[uint16]uint16{}
	}
	return f.registers[f.unitId]
}

func (f *fakeClient) bits() map[uint16]bool {
	if f.coils[f.unitId] == nil {
		f.coils[f.unitId] = map[uint16]bool{}
	}
	return f.coils[f.unitId]
}

func (f *fakeClient) ReadCoils(addr, quantity uint16) ([]bool, error) {
	if err := f.transaction("read-coils"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	values := make([]bool, quantity)
	for i := range values {
		values[i] = f.bits()[addr+uint16(i)]
	}
	return values, nil
}

func (f *fakeClient) ReadDiscreteInputs(addr, quantity uint16) ([]bool, error) {
	return f.ReadCoils(addr, quantity)
}

func (f *fakeClient) ReadRawBytes(addr, quantity uint16, _ simonmodbus.RegType) ([]byte, error) {
	if err := f.transaction("read"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	data := make([]byte, quantity)
	for i := 0; i < int(quantity)/2; i++ {
		binary.BigEndian.PutUint16(data[i*2:], f.regs()[addr+uint16(i)])
	}
	return data, nil
}

func (f *fakeClient) WriteCoil(addr uint16, value bool) error {
	return f.WriteCoils(addr, []bool{value})
}

func (f *fakeClient) WriteCoils(addr uint16, values []bool) error {
	if err := f.transaction("write-coils"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, v := range values {
		f.bits()[addr+uint16(i)] = v
	}
	return nil
}

func (f *fakeClient) WriteRegister(addr, value uint16) error {
	return f.WriteRegisters(addr, []uint16{value})
}

func (f *fakeClient) WriteRegisters(addr uint16, values []uint16) error {
	if err := f.transaction("write"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, v := range values {
		f.regs()[addr+uint16(i)] = v
	}
	return nil
}

func (f *fakeClient) executed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.log)
}

// newTestManager returns a manager with one bus on fake and the given devices.
func newTestManager(t *testing.T, fake *fakeClient, bus BusConfig, devices ...DeviceConfig) *Manager {
	t.Helper()
	if bus.Name == "" {
		bus.Name = "bus"
	}
	if bus.Type == "" {
		bus.Type = "rtu"
	}
	m := New()
	if err := m.addBus(bus, func() (busClient, error) { return fake, nil }); err != nil {
		t.Fatal(err)
	}
	for _, d := range devices {
		if d.Bus == "" {
			d.Bus = bus.Name
		}
		if err := m.Register(d); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

var allFunctions = []uint8{1, 2, 3, 4, 5, 6, 15, 16}

// waitQueued waits until n requests wait on the only bus of m.
func waitQueued(t *testing.T, m *Manager, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		status := m.ListStatus()
		if len(status) > 0 && status[0].QueueLen >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue did not reach %d requests", n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestIdenticalReadsShareOneTransaction(t *testing.T) {
	fake := newFakeClient()
	fake.delay = 50 * time.Millisecond
	m := newTestManager(t, fake, BusConfig{}, DeviceConfig{Name: "meter", UnitId: 2})

	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := m.ReadHoldingRegisters("meter", 4096, 59); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	if n := fake.calls.Load(); n != 1 {
		t.Errorf("20 identical reads made %d bus transactions, want 1", n)
	}
}

func TestCacheTTL(t *testing.T) {
	fake := newFakeClient()
	m := newTestManager(t, fake, BusConfig{}, DeviceConfig{Name: "meter", UnitId: 2, CacheTTL: 100 * time.Millisecond})

	first, err := m.ReadHoldingRegisters("meter", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.ReadHoldingRegisters("meter", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if first.Cached || !second.Cached || fake.calls.Load() != 1 {
		t.Errorf("cached = %v/%v with %d transactions, want false/true with 1", first.Cached, second.Cached, fake.calls.Load())
	}

	time.Sleep(120 * time.Millisecond)
	third, err := m.ReadHoldingRegisters("meter", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if third.Cached || fake.calls.Load() != 2 {
		t.Errorf("after the TTL: cached = %v with %d transactions, want a new transaction", third.Cached, fake.calls.Load())
	}

	status, _ := m.Status("meter")
	if status.CacheHits != 1 || status.CacheMisses != 2 {
		t.Errorf("cache hits/misses = %d/%d, want 1/2", status.CacheHits, status.CacheMisses)
	}
}

func TestCacheServesPartOfABlock(t *testing.T) {
	fake := newFakeClient()
	fake.registers[2] = map[uint16]uint16{10: 0x1111, 11: 0x2222, 12: 0x3333}
	m := newTestManager(t, fake, BusConfig{}, DeviceConfig{Name: "meter", UnitId: 2, CacheTTL: time.Hour})

	if _, err := m.ReadHoldingRegisters("meter", 8, 10); err != nil {
		t.Fatal(err)
	}
	part, err := m.ReadHoldingRegisters("meter", 11, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !part.Cached || !slices.Equal(part.Values, []uint16{0x2222, 0x3333}) {
		t.Errorf("part of the cached block = %#v (cached %v), want [0x2222 0x3333] from the cache", part.Values, part.Cached)
	}
	if fake.calls.Load() != 1 {
		t.Errorf("%d transactions, want 1", fake.calls.Load())
	}

	// Another table of the same unit is not in the cache.
	if r, err := m.ReadInputRegisters("meter", 11, 2); err != nil || r.Cached {
		t.Errorf("input registers: cached = %v, err = %v, want a bus transaction", r.Cached, err)
	}
}

func TestWriteInvalidatesTheCache(t *testing.T) {
	fake := newFakeClient()
	m := newTestManager(t, fake, BusConfig{},
		DeviceConfig{Name: "heatpump", UnitId: 3, CacheTTL: time.Hour, Functions: allFunctions})

	if _, err := m.ReadHoldingRegisters("heatpump", 0, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := m.WriteSingleRegister("heatpump", 5, 7); err != nil {
		t.Fatal(err)
	}
	after, err := m.ReadHoldingRegisters("heatpump", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if after.Cached || after.Values[5] != 7 {
		t.Errorf("read after write: cached = %v, register 5 = %d, want a fresh read with 7", after.Cached, after.Values[5])
	}

	// A write elsewhere keeps the entry.
	if _, err = m.WriteSingleRegister("heatpump", 100, 1); err != nil {
		t.Fatal(err)
	}
	if again, _ := m.ReadHoldingRegisters("heatpump", 0, 10); !again.Cached {
		t.Error("a write outside the cached range dropped the entry")
	}
}

func TestWritesBeforeReadsAndInOrder(t *testing.T) {
	fake := newFakeClient()
	fake.gate = make(chan struct{})
	m := newTestManager(t, fake, BusConfig{}, DeviceConfig{Name: "heatpump", UnitId: 3, Functions: allFunctions})

	var wg sync.WaitGroup
	// The first read occupies the worker until the gate opens.
	wg.Go(func() { _, _ = m.ReadHoldingRegisters("heatpump", 0, 1) })
	waitQueued(t, m, 0)
	time.Sleep(20 * time.Millisecond)

	wg.Go(func() { _, _ = m.ReadInputRegisters("heatpump", 0, 1) })
	waitQueued(t, m, 1)
	for i := range 5 {
		wg.Go(func() { _, _ = m.WriteSingleRegister("heatpump", uint16(i), uint16(i)) })
		waitQueued(t, m, 2+i)
	}

	close(fake.gate)
	wg.Wait()

	want := []string{"3:read", "3:write", "3:write", "3:write", "3:write", "3:write", "3:read"}
	if got := fake.executed(); !slices.Equal(got, want) {
		t.Fatalf("executed %v, want the queued writes before the waiting read", got)
	}
	for i := range 5 {
		if fake.registers[3][uint16(i)] != uint16(i) {
			t.Errorf("register %d = %d, want %d", i, fake.registers[3][uint16(i)], i)
		}
	}
}

func TestOneTransactionAtATime(t *testing.T) {
	fake := newFakeClient()
	fake.delay = time.Millisecond
	m := newTestManager(t, fake, BusConfig{},
		DeviceConfig{Name: "a", UnitId: 1, Functions: allFunctions},
		DeviceConfig{Name: "b", UnitId: 2, Functions: allFunctions})

	var wg sync.WaitGroup
	for i := range 30 {
		wg.Go(func() {
			name := []string{"a", "b"}[i%2]
			if i%3 == 0 {
				_, _ = m.WriteSingleCoil(name, uint16(i), true)
			} else {
				_, _ = m.ReadHoldingRegisters(name, uint16(i), 1)
			}
		})
	}
	wg.Wait()

	if n := fake.maxActive.Load(); n != 1 {
		t.Errorf("%d transactions ran at the same time on one bus, want 1", n)
	}
	for _, entry := range fake.executed() {
		unit := entry[0]
		if unit != '1' && unit != '2' {
			t.Errorf("transaction %q ran with a wrong unit id", entry)
		}
	}
}

func TestFullQueueIsBusy(t *testing.T) {
	fake := newFakeClient()
	fake.gate = make(chan struct{})
	m := newTestManager(t, fake, BusConfig{QueueSize: 1}, DeviceConfig{Name: "meter", UnitId: 2})

	var wg sync.WaitGroup
	wg.Go(func() { _, _ = m.ReadHoldingRegisters("meter", 0, 1) }) // on the bus
	time.Sleep(20 * time.Millisecond)
	wg.Go(func() { _, _ = m.ReadHoldingRegisters("meter", 1, 1) }) // waits
	waitQueued(t, m, 1)

	_, err := m.ReadHoldingRegisters("meter", 2, 1)
	if !errors.Is(err, ErrBusBusy) {
		t.Errorf("read with a full queue: err = %v, want ErrBusBusy", err)
	}
	close(fake.gate)
	wg.Wait()
}

func TestExpiredRequestDoesNotReachTheBus(t *testing.T) {
	fake := newFakeClient()
	fake.delay = 100 * time.Millisecond
	m := newTestManager(t, fake, BusConfig{MaxWait: 30 * time.Millisecond}, DeviceConfig{Name: "meter", UnitId: 2})

	var wg sync.WaitGroup
	wg.Go(func() { _, _ = m.ReadHoldingRegisters("meter", 0, 1) })
	time.Sleep(10 * time.Millisecond)
	_, err := m.ReadHoldingRegisters("meter", 1, 1)
	wg.Wait()

	if !errors.Is(err, ErrQueueTimeout) {
		t.Errorf("err = %v, want ErrQueueTimeout", err)
	}
	if n := fake.calls.Load(); n != 1 {
		t.Errorf("%d transactions, want 1: the expired request must not reach the bus", n)
	}
}

func TestFunctionNotAllowed(t *testing.T) {
	fake := newFakeClient()
	m := newTestManager(t, fake, BusConfig{}, DeviceConfig{Name: "meter", UnitId: 2, Functions: []uint8{3}})

	if _, err := m.ReadCoils("meter", 0, 1); !errors.Is(err, ErrFunctionNotAllowed) {
		t.Errorf("FC1: err = %v, want ErrFunctionNotAllowed", err)
	}
	if _, err := m.WriteSingleRegister("meter", 0, 1); !errors.Is(err, ErrFunctionNotAllowed) {
		t.Errorf("FC6: err = %v, want ErrFunctionNotAllowed", err)
	}
	if n := fake.calls.Load(); n != 0 {
		t.Errorf("%d transactions for refused function codes, want 0", n)
	}

	// Without functions a device may read only.
	m2 := newTestManager(t, newFakeClient(), BusConfig{}, DeviceConfig{Name: "meter", UnitId: 2})
	if _, err := m2.WriteSingleCoil("meter", 0, true); !errors.Is(err, ErrFunctionNotAllowed) {
		t.Errorf("default functions: FC5 err = %v, want ErrFunctionNotAllowed", err)
	}
}

func TestDevicesShareABus(t *testing.T) {
	fake := newFakeClient()
	m := newTestManager(t, fake, BusConfig{},
		DeviceConfig{Name: "meter", UnitId: 1},
		DeviceConfig{Name: "heatpump", UnitId: 2})

	_, _ = m.ReadHoldingRegisters("meter", 0, 1)
	_, _ = m.ReadHoldingRegisters("heatpump", 0, 1)
	if got := fake.executed(); !slices.Equal(got, []string{"1:read", "2:read"}) {
		t.Errorf("executed %v, want one read per unit id on the shared bus", got)
	}
	if fake.opens.Load() != 1 {
		t.Errorf("bus opened %d times, want once for both devices", fake.opens.Load())
	}

	if err := m.Register(DeviceConfig{Name: "other", Bus: "bus", UnitId: 2}); !errors.Is(err, ErrUnitIdInUse) {
		t.Errorf("second device with unit id 2: err = %v, want ErrUnitIdInUse", err)
	}
	if err := m.Register(DeviceConfig{Name: "lost", Bus: "nowhere", UnitId: 5}); !errors.Is(err, ErrBusNotConfigured) {
		t.Errorf("device on an unknown bus: err = %v, want ErrBusNotConfigured", err)
	}
}

func TestExceptionKeepsTheConnection(t *testing.T) {
	fake := newFakeClient()
	fake.exception = simonmodbus.ErrIllegalDataAddress
	m := newTestManager(t, fake, BusConfig{}, DeviceConfig{Name: "meter", UnitId: 2})

	_, err := m.ReadHoldingRegisters("meter", 60000, 1)
	if !errors.Is(err, simonmodbus.ErrIllegalDataAddress) {
		t.Errorf("err = %v, want the exception of the device", err)
	}
	if fake.calls.Load() != 1 || fake.opens.Load() != 1 {
		t.Errorf("%d transactions, %d opens; want 1 and 1: an exception is no reason to reconnect", fake.calls.Load(), fake.opens.Load())
	}
}

func TestTransportErrorReconnectsOnce(t *testing.T) {
	fake := newFakeClient()
	fake.failNext = io.EOF
	m := newTestManager(t, fake, BusConfig{}, DeviceConfig{Name: "meter", UnitId: 2})

	if _, err := m.ReadHoldingRegisters("meter", 0, 1); err != nil {
		t.Fatalf("read after a dropped connection: %v", err)
	}
	if fake.calls.Load() != 2 || fake.opens.Load() != 2 {
		t.Errorf("%d transactions, %d opens; want 2 and 2", fake.calls.Load(), fake.opens.Load())
	}
}

func TestCloseFailsWaitingRequests(t *testing.T) {
	fake := newFakeClient()
	fake.gate = make(chan struct{})
	m := newTestManager(t, fake, BusConfig{}, DeviceConfig{Name: "meter", UnitId: 2})

	errs := make(chan error, 2)
	go func() { _, err := m.ReadHoldingRegisters("meter", 0, 1); errs <- err }()
	time.Sleep(20 * time.Millisecond)
	go func() { _, err := m.ReadHoldingRegisters("meter", 1, 1); errs <- err }()
	waitQueued(t, m, 1)

	go func() { time.Sleep(20 * time.Millisecond); close(fake.gate) }()
	_ = m.Close()
	for range 2 {
		select {
		case <-errs:
		case <-time.After(2 * time.Second):
			t.Fatal("a request still waits after Close")
		}
	}
}

// Connect opens all buses at the same time: slow or unreachable devices delay the start by one
// timeout, not by the sum of them.
func TestConnectOpensBusesInParallel(t *testing.T) {
	m := New()
	t.Cleanup(func() { _ = m.Close() })
	for _, name := range []string{"a", "b", "c"} {
		fake := newFakeClient()
		fake.openDelay = 100 * time.Millisecond
		if err := m.addBus(BusConfig{Name: name, Type: "rtu"}, func() (busClient, error) { return fake, nil }); err != nil {
			t.Fatal(err)
		}
	}

	start := time.Now()
	if failed := m.Connect(); len(failed) != 0 {
		t.Fatalf("Connect failed for %v", failed)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Errorf("Connect took %v for three buses of 100ms each, want them opened in parallel", elapsed)
	}
}
