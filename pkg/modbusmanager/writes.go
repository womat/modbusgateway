package modbusmanager

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

var (
	ErrValuesEmpty               = errors.New("values must not be empty")
	ErrValuesExceedCoilLimit     = errors.New("values exceed coil write limit")
	ErrValuesExceedRegisterLimit = errors.New("values exceed register write limit")
)

const (
	maxWriteCoils     = 1968
	maxWriteRegisters = 123
)

// WriteResult contains the normalized raw result for FC5, FC6, FC15, and FC16 writes.
type WriteResult struct {
	Device   DeviceStatus
	Register uint16
	Length   uint16
	Data     []byte // the response PDU data: address and value (FC5/6) or address and quantity (FC15/16)
	Duration time.Duration
}

// WriteSingleCoil executes a FC5 write.
func (m *Manager) WriteSingleCoil(deviceName string, register uint16, value bool) (WriteResult, error) {
	duration, d, err := m.write(deviceName, 5, coils, register, 1, func(c busClient) error {
		return c.WriteCoil(register, value)
	})
	if err != nil {
		return WriteResult{}, err
	}

	writeValue := uint16(0x0000)
	if value {
		writeValue = 0xFF00
	}
	return WriteResult{Device: d.status(), Register: register, Length: 1, Data: encodeAck(register, writeValue), Duration: duration}, nil
}

// WriteSingleRegister executes a FC6 write.
func (m *Manager) WriteSingleRegister(deviceName string, register, value uint16) (WriteResult, error) {
	duration, d, err := m.write(deviceName, 6, holdingRegisters, register, 1, func(c busClient) error {
		return c.WriteRegister(register, value)
	})
	if err != nil {
		return WriteResult{}, err
	}
	return WriteResult{Device: d.status(), Register: register, Length: 1, Data: encodeAck(register, value), Duration: duration}, nil
}

// WriteMultipleCoils executes a FC15 write.
func (m *Manager) WriteMultipleCoils(deviceName string, register uint16, values []bool) (WriteResult, error) {
	if len(values) == 0 {
		return WriteResult{}, ErrValuesEmpty
	}
	if len(values) > maxWriteCoils {
		return WriteResult{}, fmt.Errorf("%w: max=%d", ErrValuesExceedCoilLimit, maxWriteCoils)
	}

	length := uint16(len(values))
	duration, d, err := m.write(deviceName, 15, coils, register, length, func(c busClient) error {
		return c.WriteCoils(register, values)
	})
	if err != nil {
		return WriteResult{}, err
	}
	return WriteResult{Device: d.status(), Register: register, Length: length, Data: encodeAck(register, length), Duration: duration}, nil
}

// WriteMultipleRegisters executes a FC16 write.
func (m *Manager) WriteMultipleRegisters(deviceName string, register uint16, values []uint16) (WriteResult, error) {
	if len(values) == 0 {
		return WriteResult{}, ErrValuesEmpty
	}
	if len(values) > maxWriteRegisters {
		return WriteResult{}, fmt.Errorf("%w: max=%d", ErrValuesExceedRegisterLimit, maxWriteRegisters)
	}

	length := uint16(len(values))
	duration, d, err := m.write(deviceName, 16, holdingRegisters, register, length, func(c busClient) error {
		return c.WriteRegisters(register, values)
	})
	if err != nil {
		return WriteResult{}, err
	}
	return WriteResult{Device: d.status(), Register: register, Length: length, Data: encodeAck(register, length), Duration: duration}, nil
}

// write runs a write of the device through its bus and records the outcome.
func (m *Manager) write(deviceName string, fc uint8, kind regType, register, length uint16, op func(busClient) error) (time.Duration, *device, error) {
	d, err := m.device(deviceName)
	if err != nil {
		return 0, nil, err
	}
	if err = d.allow(fc); err != nil {
		return 0, nil, err
	}

	start := time.Now()
	written := span{unitId: d.cfg.UnitId, kind: kind, addr: register, qty: length}
	err = d.bus.write(d.cfg.UnitId, written, op)
	d.record(err, false, false)
	if err != nil {
		return 0, d, fmt.Errorf("write device %q: %w", deviceName, err)
	}
	return time.Since(start), d, nil
}

// encodeAck is the data of a write response: address and value, or address and quantity.
func encodeAck(register, value uint16) []byte {
	data := make([]byte, 4)
	binary.BigEndian.PutUint16(data[0:2], register)
	binary.BigEndian.PutUint16(data[2:4], value)
	return data
}
