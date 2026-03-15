// Package modbusmanager manages long-lived Modbus connections per configured device.
package modbusmanager

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	simonmodbus "github.com/simonvetter/modbus"
)

var (
	ErrManagerNotInitialized    = errors.New("modbus manager is not initialized")
	ErrDeviceNameEmpty          = errors.New("device name must not be empty")
	ErrManagerClosed            = errors.New("modbus manager is closed")
	ErrDeviceAlreadyRegistered  = errors.New("device is already registered")
	ErrDeviceNotRegistered      = errors.New("device is not registered")
	ErrDeviceNotConfigured      = errors.New("device is not configured")
	ErrTimeoutMustBePositive    = errors.New("timeout must be greater than zero")
	ErrMissingTCPSettings       = errors.New("missing tcp settings")
	ErrUnexpectedTCPSettings    = errors.New("unexpected tcp settings")
	ErrTCPHostEmpty             = errors.New("tcp host must not be empty")
	ErrTCPPortOutOfRange        = errors.New("tcp port is out of range")
	ErrMissingSerialSettings    = errors.New("missing serial settings")
	ErrUnexpectedSerialSettings = errors.New("unexpected serial settings")
	ErrSerialPortEmpty          = errors.New("serial port must not be empty")
	ErrSerialBaudRateInvalid    = errors.New("serial baud rate must be greater than zero")
	ErrSerialDataBitsInvalid    = errors.New("serial data bits are invalid")
	ErrUnsupportedSerialParity  = errors.New("unsupported serial parity")
	ErrSerialStopBitsInvalid    = errors.New("serial stop bits are invalid")
	ErrUnsupportedTransport     = errors.New("unsupported transport")
)

// DeviceConfig describes one named Modbus endpoint managed by the connection manager.
type DeviceConfig struct {
	Name        string
	Description string
	Transport   string
	DeviceID    uint8
	Timeout     time.Duration
	TCP         *TCPConfig
	Serial      *SerialConfig
}

// TCPConfig contains network settings for Modbus TCP devices.
type TCPConfig struct {
	Host string
	Port int
}

// SerialConfig contains line settings for Modbus RTU devices.
type SerialConfig struct {
	Port     string
	BaudRate int
	DataBits int
	Parity   string
	StopBits int
}

// DeviceStatus reports the current manager view of one configured device.
type DeviceStatus struct {
	Name          string
	Description   string
	Transport     string
	DeviceID      uint8
	Connected     bool
	LastError     string
	LastConnectAt time.Time
	LastSuccessAt time.Time
	LastRequestAt time.Time
}

// Manager owns one reusable Modbus client per configured device.
type Manager struct {
	mu      sync.RWMutex
	devices map[string]*managedDevice
	closed  bool
}

type managedDevice struct {
	mu           sync.Mutex
	clientConfig *simonmodbus.ClientConfiguration
	client       *simonmodbus.ModbusClient
	status       DeviceStatus
}

// New creates a new empty manager.
func New() *Manager {
	return &Manager{devices: make(map[string]*managedDevice)}
}

// Register adds one managed device configuration and fails if the name already exists.
func (m *Manager) Register(cfg DeviceConfig) error {
	if cfg.Name == "" {
		return ErrDeviceNameEmpty
	}
	clientConfig, err := buildClientConfiguration(cfg)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return ErrManagerClosed
	}

	if _, ok := m.devices[cfg.Name]; ok {
		return fmt.Errorf("%w: %s", ErrDeviceAlreadyRegistered, cfg.Name)
	}

	m.devices[cfg.Name] = &managedDevice{
		clientConfig: clientConfig,
		status: DeviceStatus{
			Name:        cfg.Name,
			Description: cfg.Description,
			Transport:   strings.ToLower(cfg.Transport),
			DeviceID:    cfg.DeviceID,
		},
	}
	return nil
}

// Remove closes and unregisters one managed device.
func (m *Manager) Remove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return ErrManagerClosed
	}

	device, ok := m.devices[name]
	if !ok {
		return fmt.Errorf("%w: %s", ErrDeviceNotRegistered, name)
	}

	if err := device.close(); err != nil {
		return fmt.Errorf("remove device %q: %w", name, err)
	}

	delete(m.devices, name)
	return nil
}

// Connect establishes or refreshes the connection for one registered device.
func (m *Manager) Connect(name string) error {
	device, err := m.getDevice(name)
	if err != nil {
		return err
	}

	if err := device.ensureConnected(); err != nil {
		return fmt.Errorf("connect device %q: %w", name, err)
	}

	return nil
}

// Status returns the current status for one registered device.
func (m *Manager) Status(name string) (DeviceStatus, error) {
	device, err := m.getDeviceAny(name)
	if err != nil {
		return DeviceStatus{}, err
	}

	return device.snapshotStatus(), nil
}

// ListStatus returns the current status for all registered devices.
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
		statuses = append(statuses, m.devices[name].snapshotStatus())
	}

	return statuses
}

// Close closes all managed device connections.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil
	}

	var firstErr error
	for _, device := range m.devices {
		if err := device.close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	m.closed = true
	return firstErr
}

func (m *Manager) getDevice(name string) (*managedDevice, error) {
	device, err := m.getDeviceAny(name)
	if err != nil {
		return nil, err
	}

	return device, nil
}

func (m *Manager) getDeviceAny(name string) (*managedDevice, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, ErrManagerClosed
	}

	device, ok := m.devices[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrDeviceNotConfigured, name)
	}

	return device, nil
}

func (d *managedDevice) ensureConnected() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ensureConnectedLocked()
}

func (d *managedDevice) ensureConnectedLocked() error {
	if d.client != nil {
		return nil
	}

	client, err := newModbusClient(d.status, d.clientConfig)
	if err != nil {
		d.status.Connected = false
		d.status.LastError = err.Error()
		return err
	}
	if err = client.Open(); err != nil {
		d.status.Connected = false
		d.status.LastError = err.Error()
		return err
	}

	d.client = client
	d.status.Connected = true
	d.status.LastError = ""
	d.status.LastConnectAt = time.Now().UTC()
	return nil
}

func (d *managedDevice) close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closeLocked()
}

func (d *managedDevice) closeLocked() error {
	if d.client == nil {
		d.status.Connected = false
		return nil
	}

	err := d.client.Close()
	d.client = nil
	d.status.Connected = false
	if err != nil {
		d.status.LastError = err.Error()
	}
	return err
}

func (d *managedDevice) snapshotStatus() DeviceStatus {
	d.mu.Lock()
	defer d.mu.Unlock()

	status := d.status
	status.Connected = d.client != nil && d.status.Connected
	return status
}

func newModbusClient(device DeviceStatus, conf *simonmodbus.ClientConfiguration) (*simonmodbus.ModbusClient, error) {
	client, err := simonmodbus.NewClient(conf)
	if err != nil {
		return nil, fmt.Errorf("configure client for device %q: %w", device.Name, err)
	}
	if err = client.SetUnitId(device.DeviceID); err != nil {
		return nil, fmt.Errorf("set unit id for device %q: %w", device.Name, err)
	}

	return client, nil
}

func buildClientConfiguration(device DeviceConfig) (*simonmodbus.ClientConfiguration, error) {
	if device.Timeout <= 0 {
		return nil, fmt.Errorf("%w for device %q", ErrTimeoutMustBePositive, device.Name)
	}

	conf := &simonmodbus.ClientConfiguration{Timeout: device.Timeout}

	switch strings.ToLower(device.Transport) {
	case "tcp":
		if device.TCP == nil {
			return nil, fmt.Errorf("%w for device %q", ErrMissingTCPSettings, device.Name)
		}
		if device.Serial != nil {
			return nil, fmt.Errorf("%w for device %q", ErrUnexpectedSerialSettings, device.Name)
		}
		if device.TCP.Host == "" {
			return nil, fmt.Errorf("%w for device %q", ErrTCPHostEmpty, device.Name)
		}
		if device.TCP.Port < 1 || device.TCP.Port > 65535 {
			return nil, fmt.Errorf("%w for device %q: %d", ErrTCPPortOutOfRange, device.Name, device.TCP.Port)
		}
		conf.URL = "tcp://" + net.JoinHostPort(device.TCP.Host, strconv.Itoa(device.TCP.Port))
	case "rtu":
		if device.Serial == nil {
			return nil, fmt.Errorf("%w for device %q", ErrMissingSerialSettings, device.Name)
		}
		if device.TCP != nil {
			return nil, fmt.Errorf("%w for device %q", ErrUnexpectedTCPSettings, device.Name)
		}
		if device.Serial.Port == "" {
			return nil, fmt.Errorf("%w for device %q", ErrSerialPortEmpty, device.Name)
		}
		if device.Serial.BaudRate <= 0 {
			return nil, fmt.Errorf("%w for device %q", ErrSerialBaudRateInvalid, device.Name)
		}
		if device.Serial.DataBits < 5 || device.Serial.DataBits > 8 {
			return nil, fmt.Errorf("%w for device %q: %d", ErrSerialDataBitsInvalid, device.Name, device.Serial.DataBits)
		}
		if device.Serial.StopBits != 1 && device.Serial.StopBits != 2 {
			return nil, fmt.Errorf("%w for device %q: %d", ErrSerialStopBitsInvalid, device.Name, device.Serial.StopBits)
		}
		parity, err := mapParity(device.Serial.Parity)
		if err != nil {
			return nil, fmt.Errorf("%w for device %q", err, device.Name)
		}
		conf.URL = "rtu://" + device.Serial.Port
		conf.Speed = uint(device.Serial.BaudRate)
		conf.DataBits = uint(device.Serial.DataBits)
		conf.Parity = parity
		conf.StopBits = uint(device.Serial.StopBits)
	default:
		return nil, fmt.Errorf("%w %q for device %q", ErrUnsupportedTransport, device.Transport, device.Name)
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
