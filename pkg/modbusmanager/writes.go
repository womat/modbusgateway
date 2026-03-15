package modbusmanager

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	simonmodbus "github.com/simonvetter/modbus"
)

var (
	ErrValuesEmpty               = errors.New("values must not be empty")
	ErrValuesExceedCoilLimit     = errors.New("values exceed coil write limit")
	ErrValuesExceedRegisterLimit = errors.New("values exceed register write limit")
)

// WriteResult contains the normalized raw result for FC5, FC6, FC15, and FC16 writes.
type WriteResult struct {
	Device   DeviceStatus
	Register uint16
	Length   uint16
	Data     []byte
	Duration time.Duration
}

// WriteSingleCoil executes a FC5 write against a managed device connection.
func (m *Manager) WriteSingleCoil(deviceName string, register uint16, value bool) (WriteResult, error) {
	device, duration, err := m.write(deviceName, func(client *simonmodbus.ModbusClient) error {
		return client.WriteCoil(register, value)
	})
	if err != nil {
		return WriteResult{}, err
	}

	writeValue := uint16(0x0000)
	if value {
		writeValue = 0xFF00
	}

	return WriteResult{
		Device:   device.snapshotStatus(),
		Register: register,
		Length:   1,
		Data:     encodeWriteSingleAck(register, writeValue),
		Duration: duration,
	}, nil
}

// WriteSingleRegister executes a FC6 write against a managed device connection.
func (m *Manager) WriteSingleRegister(deviceName string, register, value uint16) (WriteResult, error) {
	device, duration, err := m.write(deviceName, func(client *simonmodbus.ModbusClient) error {
		return client.WriteRegister(register, value)
	})
	if err != nil {
		return WriteResult{}, err
	}

	return WriteResult{
		Device:   device.snapshotStatus(),
		Register: register,
		Length:   1,
		Data:     encodeWriteSingleAck(register, value),
		Duration: duration,
	}, nil
}

// WriteMultipleCoils executes a FC15 write against a managed device connection.
func (m *Manager) WriteMultipleCoils(deviceName string, register uint16, values []bool) (WriteResult, error) {
	if len(values) == 0 {
		return WriteResult{}, ErrValuesEmpty
	}
	if len(values) > 1968 {
		return WriteResult{}, fmt.Errorf("%w: max=%d", ErrValuesExceedCoilLimit, 1968)
	}

	device, duration, err := m.write(deviceName, func(client *simonmodbus.ModbusClient) error {
		return client.WriteCoils(register, values)
	})
	if err != nil {
		return WriteResult{}, err
	}

	length := uint16(len(values))
	return WriteResult{
		Device:   device.snapshotStatus(),
		Register: register,
		Length:   length,
		Data:     encodeWriteMultipleAck(register, length),
		Duration: duration,
	}, nil
}

// WriteMultipleRegisters executes a FC16 write against a managed device connection.
func (m *Manager) WriteMultipleRegisters(deviceName string, register uint16, values []uint16) (WriteResult, error) {
	if len(values) == 0 {
		return WriteResult{}, ErrValuesEmpty
	}
	if len(values) > 123 {
		return WriteResult{}, fmt.Errorf("%w: max=%d", ErrValuesExceedRegisterLimit, 123)
	}

	device, duration, err := m.write(deviceName, func(client *simonmodbus.ModbusClient) error {
		return client.WriteRegisters(register, values)
	})
	if err != nil {
		return WriteResult{}, err
	}

	length := uint16(len(values))
	return WriteResult{
		Device:   device.snapshotStatus(),
		Register: register,
		Length:   length,
		Data:     encodeWriteMultipleAck(register, length),
		Duration: duration,
	}, nil
}

func (m *Manager) write(deviceName string, op func(*simonmodbus.ModbusClient) error) (*managedDevice, time.Duration, error) {
	device, err := m.getDevice(deviceName)
	if err != nil {
		return nil, 0, err
	}

	duration, err := device.executeWrite(op)
	if err != nil {
		return nil, 0, err
	}

	return device, duration, nil
}

func (d *managedDevice) executeWrite(op func(*simonmodbus.ModbusClient) error) (time.Duration, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	start := time.Now()

	d.status.LastRequestAt = time.Now().UTC()

	if err := d.ensureConnectedLocked(); err != nil {
		return 0, fmt.Errorf("connect %q: %w", d.status.Name, err)
	}

	if err := op(d.client); err == nil {
		d.status.LastSuccessAt = time.Now().UTC()
		d.status.LastError = ""
		return time.Since(start), nil
	} else {
		d.status.LastError = err.Error()
		d.status.Connected = false
		_ = d.closeLocked()
		if reconnectErr := d.ensureConnectedLocked(); reconnectErr != nil {
			d.status.LastError = fmt.Sprintf("%v; reconnect failed: %v", err, reconnectErr)
			return 0, fmt.Errorf("write failed: %w; reconnect failed: %v", err, reconnectErr)
		}
		if retryErr := op(d.client); retryErr != nil {
			d.status.LastError = retryErr.Error()
			d.status.Connected = false
			return 0, fmt.Errorf("write failed after reconnect: %w", retryErr)
		}
	}

	d.status.LastSuccessAt = time.Now().UTC()
	d.status.LastError = ""
	return time.Since(start), nil
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

func encodeWriteSingleAck(register, value uint16) []byte {
	data := make([]byte, 4)
	binary.BigEndian.PutUint16(data[0:2], register)
	binary.BigEndian.PutUint16(data[2:4], value)
	return data
}

func encodeWriteMultipleAck(register, length uint16) []byte {
	data := make([]byte, 4)
	binary.BigEndian.PutUint16(data[0:2], register)
	binary.BigEndian.PutUint16(data[2:4], length)
	return data
}
