package modbusmanager

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	goburrowmodbus "github.com/goburrow/modbus"
)

var (
	ErrLengthMustBeGreaterThanZero = errors.New("length must be greater than 0")
	ErrLengthExceedsBitLimit       = errors.New("length exceeds bit read limit")
	ErrLengthExceedsRegisterLimit  = errors.New("length exceeds register read limit")
)

// ReadBitsResult contains the normalized raw result for FC1 or FC2 reads.
type ReadBitsResult struct {
	Device   DeviceConfig
	Register uint16
	Length   uint16
	Data     []byte
	Values   []bool
}

// ReadRegistersResult contains the normalized raw result for FC3 or FC4 reads.
type ReadRegistersResult struct {
	Device   DeviceConfig
	Register uint16
	Length   uint16
	Data     []byte
	Values   []uint16
}

// ReadCoils executes a FC1 read against a managed device connection.
func (m *Manager) ReadCoils(deviceName string, register, length uint16) (ReadBitsResult, error) {
	data, device, err := m.readBits(deviceName, register, length, 2000, func(client goburrowmodbus.Client) ([]byte, error) {
		return client.ReadCoils(register, length)
	})
	if err != nil {
		return ReadBitsResult{}, err
	}

	values, err := decodeBitValues(data, length)
	if err != nil {
		return ReadBitsResult{}, err
	}

	return ReadBitsResult{
		Device:   device.config,
		Register: register,
		Length:   length,
		Data:     data,
		Values:   values,
	}, nil
}

// ReadDiscreteInputs executes a FC2 read against a managed device connection.
func (m *Manager) ReadDiscreteInputs(deviceName string, register, length uint16) (ReadBitsResult, error) {
	data, device, err := m.readBits(deviceName, register, length, 2000, func(client goburrowmodbus.Client) ([]byte, error) {
		return client.ReadDiscreteInputs(register, length)
	})
	if err != nil {
		return ReadBitsResult{}, err
	}

	values, err := decodeBitValues(data, length)
	if err != nil {
		return ReadBitsResult{}, err
	}

	return ReadBitsResult{
		Device:   device.config,
		Register: register,
		Length:   length,
		Data:     data,
		Values:   values,
	}, nil
}

// ReadHoldingRegisters executes a FC3 read against a managed device connection.
func (m *Manager) ReadHoldingRegisters(deviceName string, register, length uint16) (ReadRegistersResult, error) {
	data, device, err := m.readRegisters(deviceName, register, length, 125, func(client goburrowmodbus.Client) ([]byte, error) {
		return client.ReadHoldingRegisters(register, length)
	})
	if err != nil {
		return ReadRegistersResult{}, err
	}

	values, err := decodeRegisterValues(data)
	if err != nil {
		return ReadRegistersResult{}, err
	}

	return ReadRegistersResult{
		Device:   device.config,
		Register: register,
		Length:   length,
		Data:     data,
		Values:   values,
	}, nil
}

// ReadInputRegisters executes a FC4 read against a managed device connection.
func (m *Manager) ReadInputRegisters(deviceName string, register, length uint16) (ReadRegistersResult, error) {
	data, device, err := m.readRegisters(deviceName, register, length, 125, func(client goburrowmodbus.Client) ([]byte, error) {
		return client.ReadInputRegisters(register, length)
	})
	if err != nil {
		return ReadRegistersResult{}, err
	}

	values, err := decodeRegisterValues(data)
	if err != nil {
		return ReadRegistersResult{}, err
	}

	return ReadRegistersResult{
		Device:   device.config,
		Register: register,
		Length:   length,
		Data:     data,
		Values:   values,
	}, nil
}

func (m *Manager) readBits(deviceName string, register, length, maxLength uint16, op func(goburrowmodbus.Client) ([]byte, error)) ([]byte, *managedDevice, error) {
	if length == 0 {
		return nil, nil, ErrLengthMustBeGreaterThanZero
	}
	if length > maxLength {
		return nil, nil, fmt.Errorf("%w: max=%d", ErrLengthExceedsBitLimit, maxLength)
	}

	device, err := m.getDevice(deviceName)
	if err != nil {
		return nil, nil, err
	}

	data, err := device.executeRead(op)
	if err != nil {
		return nil, nil, err
	}

	return data, device, nil
}

func (m *Manager) readRegisters(deviceName string, register, length, maxLength uint16, op func(goburrowmodbus.Client) ([]byte, error)) ([]byte, *managedDevice, error) {
	if length == 0 {
		return nil, nil, ErrLengthMustBeGreaterThanZero
	}
	if length > maxLength {
		return nil, nil, fmt.Errorf("%w: max=%d", ErrLengthExceedsRegisterLimit, maxLength)
	}

	device, err := m.getDevice(deviceName)
	if err != nil {
		return nil, nil, err
	}

	data, err := device.executeRead(op)
	if err != nil {
		return nil, nil, err
	}

	return data, device, nil
}

func (d *managedDevice) executeRead(op func(goburrowmodbus.Client) ([]byte, error)) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.status.LastRequestAt = time.Now().UTC()

	if err := d.ensureConnectedLocked(); err != nil {
		return nil, fmt.Errorf("connect %q: %w", d.config.Name, err)
	}

	data, err := op(d.client)
	if err == nil {
		d.status.LastSuccessAt = time.Now().UTC()
		d.status.LastError = ""
		return data, nil
	}
	d.status.LastError = err.Error()
	d.status.Connected = false

	_ = d.closeLocked()
	if reconnectErr := d.ensureConnectedLocked(); reconnectErr != nil {
		d.status.LastError = fmt.Sprintf("%v; reconnect failed: %v", err, reconnectErr)
		return nil, fmt.Errorf("read failed: %w; reconnect failed: %v", err, reconnectErr)
	}

	data, retryErr := op(d.client)
	if retryErr != nil {
		d.status.LastError = retryErr.Error()
		d.status.Connected = false
		return nil, fmt.Errorf("read failed after reconnect: %w", retryErr)
	}

	d.status.LastSuccessAt = time.Now().UTC()
	d.status.LastError = ""
	return data, nil
}

func decodeRegisterValues(data []byte) ([]uint16, error) {
	if len(data)%2 != 0 {
		return nil, fmt.Errorf("invalid Modbus register payload length %d", len(data))
	}

	values := make([]uint16, 0, len(data)/2)
	for i := 0; i < len(data); i += 2 {
		values = append(values, binary.BigEndian.Uint16(data[i:i+2]))
	}

	return values, nil
}

func decodeBitValues(data []byte, quantity uint16) ([]bool, error) {
	values := make([]bool, 0, quantity)
	for i := uint16(0); i < quantity; i++ {
		byteIndex := i / 8
		if int(byteIndex) >= len(data) {
			return nil, fmt.Errorf("invalid Modbus bit payload length %d for quantity %d", len(data), quantity)
		}
		bitIndex := i % 8
		values = append(values, data[byteIndex]&(1<<bitIndex) != 0)
	}

	return values, nil
}
