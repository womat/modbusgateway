package modbusmanager

import (
	"errors"
	"fmt"
	"testing"
	"time"

	simonmodbus "github.com/simonvetter/modbus"
)

// TestBusStatusCountsBusTransactions: only requests that reach the bus count; a cache hit and a
// refused function code do not. Exceptions and timeouts count as errors.
func TestBusStatusCountsBusTransactions(t *testing.T) {
	fake := newFakeClient()
	m := newTestManager(t, fake, BusConfig{Timeout: time.Second, Serial: &SerialConfig{Port: "/dev/ttyX", BaudRate: 9600, DataBits: 8, Parity: "n", StopBits: 1}},
		DeviceConfig{Name: "meter", UnitId: 1, CacheTTL: time.Minute, Functions: []uint8{3}})

	if _, err := m.ReadHoldingRegisters("meter", 0, 2); err != nil { // bus
		t.Fatal(err)
	}
	if r, _ := m.ReadHoldingRegisters("meter", 0, 2); !r.Cached { // cache
		t.Fatal("second read not from the cache")
	}
	if _, err := m.WriteSingleRegister("meter", 0, 1); !errors.Is(err, ErrFunctionNotAllowed) { // refused
		t.Fatalf("write err = %v, want ErrFunctionNotAllowed", err)
	}
	fake.exception = simonmodbus.ErrIllegalDataAddress
	_, _ = m.ReadHoldingRegisters("meter", 100, 1) // bus, exception
	fake.exception = simonmodbus.ErrRequestTimedOut
	_, _ = m.ReadHoldingRegisters("meter", 200, 1) // bus, timeout (retried once on a new connection)

	buses := m.BusStatus()
	if len(buses) != 1 {
		t.Fatalf("BusStatus() = %+v, want one bus", buses)
	}
	b := buses[0]
	if b.Transactions != 3 || b.Errors != 2 || b.Timeouts != 1 {
		t.Errorf("transactions/errors/timeouts = %d/%d/%d, want 3/2/1", b.Transactions, b.Errors, b.Timeouts)
	}
	if b.Address != "/dev/ttyX 9600 8N1" || b.Type != "rtu" || b.QueueSize != DefaultQueueSize || b.BusyTime <= 0 {
		t.Errorf("BusStatus = %+v", b)
	}
}

// TestLastErrorStays: a success after an error keeps the error and its time, so a page can show
// "last error 2 min ago" next to "ok 1 s ago".
func TestLastErrorStays(t *testing.T) {
	fake := newFakeClient()
	m := newTestManager(t, fake, BusConfig{Timeout: time.Second}, DeviceConfig{Name: "meter", UnitId: 1, CacheTTL: 2 * time.Second})

	fake.exception = simonmodbus.ErrIllegalDataAddress
	_, _ = m.ReadHoldingRegisters("meter", 0, 1)
	fake.exception = nil
	if _, err := m.ReadHoldingRegisters("meter", 0, 1); err != nil {
		t.Fatal(err)
	}

	s, _ := m.Status("meter")
	if s.LastError == "" || s.LastErrorAt.IsZero() || s.LastSuccessAt.Before(s.LastErrorAt) {
		t.Errorf("status = %+v, want the error kept and a later success", s)
	}
	if s.CacheTTL != 2*time.Second {
		t.Errorf("CacheTTL = %v, want 2s", s.CacheTTL)
	}
}

func TestOutcome(t *testing.T) {
	for _, tc := range []struct {
		err    error
		cached bool
		result string
		class  Class
	}{
		{nil, false, "ok", ClassOK},
		{nil, true, "cache", ClassCache},
		{fmt.Errorf("x: %w", ErrFunctionNotAllowed), false, "forbidden", ClassRejected},
		{ErrLengthExceedsRegisterLimit, false, "invalid", ClassRejected},
		{ErrInvalidRequest, false, "invalid", ClassRejected},
		{ErrBusBusy, false, "busy", ClassRejected},
		{ErrQueueTimeout, false, "queue timeout", ClassRejected},
		{fmt.Errorf("after reconnect: %w", simonmodbus.ErrRequestTimedOut), false, "timeout", ClassTimeout},
		{simonmodbus.ErrIllegalDataAddress, false, "IllegalDataAddress", ClassException},
		{simonmodbus.ErrGWTargetFailedToRespond, false, "GatewayTargetDeviceFailedToRespond", ClassException},
		{errors.New("connection refused"), false, "error", ClassError},
	} {
		result, class := Outcome(tc.err, tc.cached)
		if result != tc.result || class != tc.class {
			t.Errorf("Outcome(%v, %v) = %q, %q; want %q, %q", tc.err, tc.cached, result, class, tc.result, tc.class)
		}
	}
	if !ClassTimeout.OnBus() || ClassCache.OnBus() || ClassRejected.OnBus() {
		t.Error("OnBus: want timeout on the bus, cache and rejected not")
	}
}
