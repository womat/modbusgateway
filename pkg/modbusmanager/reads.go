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

// ReadBitsResult contains the normalized raw result for FC1 or FC2 reads.
type ReadBitsResult struct {
	Device   DeviceStatus
	Register uint16
	Length   uint16
	Data     []byte
	Values   []bool
	Duration time.Duration
}

// ReadRegistersResult contains the normalized raw result for FC3 or FC4 reads.
type ReadRegistersResult struct {
	Device   DeviceStatus
	Register uint16
	Length   uint16
	Data     []byte
	Values   []uint16
	Duration time.Duration
}

// ReadCoils executes a FC1 read against a managed device connection.
func (m *Manager) ReadCoils(deviceName string, register, length uint16) (ReadBitsResult, error) {
	values, device, duration, err := m.readBits(deviceName, length, 2000, func(client *simonmodbus.ModbusClient) ([]bool, error) {
		return client.ReadCoils(register, length)
	})
	if err != nil {
		return ReadBitsResult{}, err
	}

	return ReadBitsResult{
		Device:   device.snapshotStatus(),
		Register: register,
		Length:   length,
		Data:     packCoilValues(values),
		Values:   values,
		Duration: duration,
	}, nil
}

// ReadDiscreteInputs executes a FC2 read against a managed device connection.
func (m *Manager) ReadDiscreteInputs(deviceName string, register, length uint16) (ReadBitsResult, error) {
	values, device, duration, err := m.readBits(deviceName, length, 2000, func(client *simonmodbus.ModbusClient) ([]bool, error) {
		return client.ReadDiscreteInputs(register, length)
	})
	if err != nil {
		return ReadBitsResult{}, err
	}

	return ReadBitsResult{
		Device:   device.snapshotStatus(),
		Register: register,
		Length:   length,
		Data:     packCoilValues(values),
		Values:   values,
		Duration: duration,
	}, nil
}

// ReadHoldingRegisters executes a FC3 read against a managed device connection.
func (m *Manager) ReadHoldingRegisters(deviceName string, register, length uint16) (ReadRegistersResult, error) {
	data, device, duration, err := m.readRegisterBytes(deviceName, length, 125, func(client *simonmodbus.ModbusClient) ([]byte, error) {
		return client.ReadRawBytes(register, length*2, simonmodbus.HOLDING_REGISTER)
	})
	if err != nil {
		return ReadRegistersResult{}, err
	}

	return ReadRegistersResult{
		Device:   device.snapshotStatus(),
		Register: register,
		Length:   length,
		Data:     data,
		Values:   decodeRegisterValues(data),
		Duration: duration,
	}, nil
}

// ReadInputRegisters executes a FC4 read against a managed device connection.
func (m *Manager) ReadInputRegisters(deviceName string, register, length uint16) (ReadRegistersResult, error) {
	data, device, duration, err := m.readRegisterBytes(deviceName, length, 125, func(client *simonmodbus.ModbusClient) ([]byte, error) {
		return client.ReadRawBytes(register, length*2, simonmodbus.INPUT_REGISTER)
	})
	if err != nil {
		return ReadRegistersResult{}, err
	}

	return ReadRegistersResult{
		Device:   device.snapshotStatus(),
		Register: register,
		Length:   length,
		Data:     data,
		Values:   decodeRegisterValues(data),
		Duration: duration,
	}, nil
}

func (m *Manager) readBits(deviceName string, length, maxLength uint16, op func(*simonmodbus.ModbusClient) ([]bool, error)) ([]bool, *managedDevice, time.Duration, error) {
	if length == 0 {
		return nil, nil, 0, ErrLengthMustBeGreaterThanZero
	}
	if length > maxLength {
		return nil, nil, 0, fmt.Errorf("%w: max=%d", ErrLengthExceedsBitLimit, maxLength)
	}

	device, err := m.getDevice(deviceName)
	if err != nil {
		return nil, nil, 0, err
	}

	values, duration, err := device.executeReadBits(op)
	if err != nil {
		return nil, nil, 0, err
	}

	return values, device, duration, nil
}

func (m *Manager) readRegisterBytes(deviceName string, length, maxLength uint16, op func(*simonmodbus.ModbusClient) ([]byte, error)) ([]byte, *managedDevice, time.Duration, error) {
	if length == 0 {
		return nil, nil, 0, ErrLengthMustBeGreaterThanZero
	}
	if length > maxLength {
		return nil, nil, 0, fmt.Errorf("%w: max=%d", ErrLengthExceedsRegisterLimit, maxLength)
	}

	device, err := m.getDevice(deviceName)
	if err != nil {
		return nil, nil, 0, err
	}

	data, duration, err := device.executeReadRegisters(op)
	if err != nil {
		return nil, nil, 0, err
	}

	return data, device, duration, nil
}

func (d *managedDevice) executeReadBits(op func(*simonmodbus.ModbusClient) ([]bool, error)) ([]bool, time.Duration, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	start := time.Now()

	d.status.LastRequestAt = time.Now().UTC()

	if err := d.ensureConnectedLocked(); err != nil {
		return nil, 0, fmt.Errorf("connect %q: %w", d.status.Name, err)
	}

	values, err := op(d.client)
	if err == nil {
		d.status.LastSuccessAt = time.Now().UTC()
		d.status.LastError = ""
		return values, time.Since(start), nil
	}

	d.status.LastError = err.Error()
	d.status.Connected = false
	_ = d.closeLocked()
	if reconnectErr := d.ensureConnectedLocked(); reconnectErr != nil {
		d.status.LastError = fmt.Sprintf("%v; reconnect failed: %v", err, reconnectErr)
		return nil, 0, fmt.Errorf("read failed: %w; reconnect failed: %v", err, reconnectErr)
	}

	values, retryErr := op(d.client)
	if retryErr != nil {
		d.status.LastError = retryErr.Error()
		d.status.Connected = false
		return nil, 0, fmt.Errorf("read failed after reconnect: %w", retryErr)
	}

	d.status.LastSuccessAt = time.Now().UTC()
	d.status.LastError = ""
	return values, time.Since(start), nil
}

func (d *managedDevice) executeReadRegisters(op func(*simonmodbus.ModbusClient) ([]byte, error)) ([]byte, time.Duration, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	start := time.Now()

	d.status.LastRequestAt = time.Now().UTC()

	if err := d.ensureConnectedLocked(); err != nil {
		return nil, 0, fmt.Errorf("connect %q: %w", d.status.Name, err)
	}

	data, err := op(d.client)
	if err == nil {
		d.status.LastSuccessAt = time.Now().UTC()
		d.status.LastError = ""
		return data, time.Since(start), nil
	}

	d.status.LastError = err.Error()
	d.status.Connected = false
	_ = d.closeLocked()
	if reconnectErr := d.ensureConnectedLocked(); reconnectErr != nil {
		d.status.LastError = fmt.Sprintf("%v; reconnect failed: %v", err, reconnectErr)
		return nil, 0, fmt.Errorf("read failed: %w; reconnect failed: %v", err, reconnectErr)
	}

	data, retryErr := op(d.client)
	if retryErr != nil {
		d.status.LastError = retryErr.Error()
		d.status.Connected = false
		return nil, 0, fmt.Errorf("read failed after reconnect: %w", retryErr)
	}

	d.status.LastSuccessAt = time.Now().UTC()
	d.status.LastError = ""
	return data, time.Since(start), nil
}

func decodeRegisterValues(data []byte) []uint16 {
	values := make([]uint16, len(data)/2)
	for i := range values {
		values[i] = binary.BigEndian.Uint16(data[i*2:])
	}
	return values
}
