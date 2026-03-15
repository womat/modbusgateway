package modbusmanager

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	goburrowmodbus "github.com/goburrow/modbus"
)

var (
	ErrValuesEmpty               = errors.New("values must not be empty")
	ErrValuesExceedCoilLimit     = errors.New("values exceed coil write limit")
	ErrValuesExceedRegisterLimit = errors.New("values exceed register write limit")
)

// WriteResult contains the normalized raw result for FC5, FC6, FC15, and FC16 writes.
type WriteResult struct {
	Device   DeviceConfig
	Register uint16
	Length   uint16
	Data     []byte
}

// WriteSingleCoil executes a FC5 write against a managed device connection.
func (m *Manager) WriteSingleCoil(deviceName string, register uint16, value bool) (WriteResult, error) {
	writeValue := uint16(0x0000)
	if value {
		writeValue = 0xFF00
	}

	data, device, err := m.write(deviceName, func(client goburrowmodbus.Client) ([]byte, error) {
		return client.WriteSingleCoil(register, writeValue)
	})
	if err != nil {
		return WriteResult{}, err
	}

	return WriteResult{
		Device:   device.config,
		Register: register,
		Length:   1,
		Data:     data,
	}, nil
}

// WriteSingleRegister executes a FC6 write against a managed device connection.
func (m *Manager) WriteSingleRegister(deviceName string, register, value uint16) (WriteResult, error) {
	data, device, err := m.write(deviceName, func(client goburrowmodbus.Client) ([]byte, error) {
		return client.WriteSingleRegister(register, value)
	})
	if err != nil {
		return WriteResult{}, err
	}

	return WriteResult{
		Device:   device.config,
		Register: register,
		Length:   1,
		Data:     data,
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

	packed := packCoilValues(values)
	length := uint16(len(values))

	data, device, err := m.write(deviceName, func(client goburrowmodbus.Client) ([]byte, error) {
		return client.WriteMultipleCoils(register, length, packed)
	})
	if err != nil {
		return WriteResult{}, err
	}

	return WriteResult{
		Device:   device.config,
		Register: register,
		Length:   length,
		Data:     data,
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

	payload := make([]byte, 2*len(values))
	for i, value := range values {
		binary.BigEndian.PutUint16(payload[i*2:], value)
	}
	length := uint16(len(values))

	data, device, err := m.write(deviceName, func(client goburrowmodbus.Client) ([]byte, error) {
		return client.WriteMultipleRegisters(register, length, payload)
	})
	if err != nil {
		return WriteResult{}, err
	}

	return WriteResult{
		Device:   device.config,
		Register: register,
		Length:   length,
		Data:     data,
	}, nil
}

func (m *Manager) write(deviceName string, op func(goburrowmodbus.Client) ([]byte, error)) ([]byte, *managedDevice, error) {
	device, err := m.getDevice(deviceName)
	if err != nil {
		return nil, nil, err
	}

	data, err := device.executeWrite(op)
	if err != nil {
		return nil, nil, err
	}

	return data, device, nil
}

func (d *managedDevice) executeWrite(op func(goburrowmodbus.Client) ([]byte, error)) ([]byte, error) {
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
		return nil, fmt.Errorf("write failed: %w; reconnect failed: %v", err, reconnectErr)
	}

	data, retryErr := op(d.client)
	if retryErr != nil {
		d.status.LastError = retryErr.Error()
		d.status.Connected = false
		return nil, fmt.Errorf("write failed after reconnect: %w", retryErr)
	}

	d.status.LastSuccessAt = time.Now().UTC()
	d.status.LastError = ""
	return data, nil
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
