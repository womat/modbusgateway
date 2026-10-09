package modbusmanager

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	simonmodbus "github.com/simonvetter/modbus"
)

var (
	ErrLengthMustBeGreaterThanZero = errors.New("length must be greater than 0")
	ErrLengthExceedsBitLimit       = errors.New("length exceeds bit read limit")
	ErrLengthExceedsRegisterLimit  = errors.New("length exceeds register read limit")
)

const (
	maxReadBits      = 2000
	maxReadRegisters = 125
)

// ReadBitsResult contains the normalized raw result for FC1 or FC2 reads.
type ReadBitsResult struct {
	Device   DeviceStatus
	Register uint16
	Length   uint16
	Data     []byte // packed, eight values per byte, first value in the lowest bit
	Values   []bool
	Duration time.Duration
	Cached   bool // answered from the cache, without a bus transaction
}

// ReadRegistersResult contains the normalized raw result for FC3 or FC4 reads.
type ReadRegistersResult struct {
	Device   DeviceStatus
	Register uint16
	Length   uint16
	Data     []byte // two bytes per register, big endian
	Values   []uint16
	Duration time.Duration
	Cached   bool // answered from the cache, without a bus transaction
}

// ReadCoils executes a FC1 read.
func (m *Manager) ReadCoils(deviceName string, register, length uint16) (ReadBitsResult, error) {
	return m.readBits(deviceName, 1, coils, register, length, func(c busClient) ([]bool, error) {
		return c.ReadCoils(register, length)
	})
}

// ReadDiscreteInputs executes a FC2 read.
func (m *Manager) ReadDiscreteInputs(deviceName string, register, length uint16) (ReadBitsResult, error) {
	return m.readBits(deviceName, 2, discreteInputs, register, length, func(c busClient) ([]bool, error) {
		return c.ReadDiscreteInputs(register, length)
	})
}

// ReadHoldingRegisters executes a FC3 read.
func (m *Manager) ReadHoldingRegisters(deviceName string, register, length uint16) (ReadRegistersResult, error) {
	return m.readRegisters(deviceName, 3, holdingRegisters, register, length, func(c busClient) ([]byte, error) {
		return c.ReadRawBytes(register, length*2, simonmodbus.HOLDING_REGISTER)
	})
}

// ReadInputRegisters executes a FC4 read.
func (m *Manager) ReadInputRegisters(deviceName string, register, length uint16) (ReadRegistersResult, error) {
	return m.readRegisters(deviceName, 4, inputRegisters, register, length, func(c busClient) ([]byte, error) {
		return c.ReadRawBytes(register, length*2, simonmodbus.INPUT_REGISTER)
	})
}

func (m *Manager) readBits(deviceName string, fc uint8, kind regType, register, length uint16, op func(busClient) ([]bool, error)) (ReadBitsResult, error) {
	if length == 0 {
		return ReadBitsResult{}, ErrLengthMustBeGreaterThanZero
	}
	if length > maxReadBits {
		return ReadBitsResult{}, fmt.Errorf("%w: max=%d", ErrLengthExceedsBitLimit, maxReadBits)
	}

	values, cached, duration, d, err := read(m, deviceName, fc, kind, register, length, func(c busClient) (any, error) {
		return op(c)
	})
	if err != nil {
		return ReadBitsResult{}, err
	}

	bits := values.([]bool)
	return ReadBitsResult{
		Device:   d.status(),
		Register: register,
		Length:   length,
		Data:     packCoilValues(bits),
		Values:   bits,
		Duration: duration,
		Cached:   cached,
	}, nil
}

func (m *Manager) readRegisters(deviceName string, fc uint8, kind regType, register, length uint16, op func(busClient) ([]byte, error)) (ReadRegistersResult, error) {
	if length == 0 {
		return ReadRegistersResult{}, ErrLengthMustBeGreaterThanZero
	}
	if length > maxReadRegisters {
		return ReadRegistersResult{}, fmt.Errorf("%w: max=%d", ErrLengthExceedsRegisterLimit, maxReadRegisters)
	}

	values, cached, duration, d, err := read(m, deviceName, fc, kind, register, length, func(c busClient) (any, error) {
		return op(c)
	})
	if err != nil {
		return ReadRegistersResult{}, err
	}

	data := values.([]byte)
	return ReadRegistersResult{
		Device:   d.status(),
		Register: register,
		Length:   length,
		Data:     data,
		Values:   decodeRegisterValues(data),
		Duration: duration,
		Cached:   cached,
	}, nil
}

// read runs a read of the device through its bus and records the outcome.
func read(m *Manager, deviceName string, fc uint8, kind regType, register, length uint16, op func(busClient) (any, error)) (any, bool, time.Duration, *device, error) {
	d, err := m.device(deviceName)
	if err != nil {
		return nil, false, 0, nil, err
	}
	if err = d.allow(fc); err != nil {
		return nil, false, 0, nil, err
	}

	start := time.Now()
	key := span{unitId: d.cfg.UnitId, kind: kind, addr: register, qty: length}
	value, cached, err := d.bus.read(key, d.cfg.CacheTTL, op)
	d.record(err, true, cached)
	if err != nil {
		return nil, false, 0, d, fmt.Errorf("read device %q: %w", deviceName, err)
	}
	return value, cached, time.Since(start), d, nil
}

func decodeRegisterValues(data []byte) []uint16 {
	values := make([]uint16, len(data)/2)
	for i := range values {
		values[i] = binary.BigEndian.Uint16(data[i*2:])
	}
	return values
}

func packCoilValues(values []bool) []byte {
	packed := make([]byte, (len(values)+7)/8)
	for i, value := range values {
		if value {
			packed[i/8] |= 1 << (uint(i) % 8)
		}
	}
	return packed
}
