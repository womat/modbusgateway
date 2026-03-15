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

	goburrowmodbus "github.com/goburrow/modbus"
)

var (
	ErrManagerNotInitialized   = errors.New("modbus manager is not initialized")
	ErrDeviceNameEmpty         = errors.New("device name must not be empty")
	ErrManagerClosed           = errors.New("modbus manager is closed")
	ErrDeviceAlreadyRegistered = errors.New("device is already registered")
	ErrDeviceNotRegistered     = errors.New("device is not registered")
	ErrDeviceNotConfigured     = errors.New("device is not configured")
	ErrDeviceDisabled          = errors.New("device is disabled")
	ErrMissingTCPSettings      = errors.New("missing tcp settings")
	ErrMissingSerialSettings   = errors.New("missing serial settings")
	ErrUnsupportedTransport    = errors.New("unsupported transport")
)

// DeviceConfig describes one named Modbus endpoint managed by the connection manager.
type DeviceConfig struct {
	Name        string
	Enabled     bool
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

// SerialConfig contains line settings for Modbus RTU or ASCII devices.
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
	Enabled       bool
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
	mu      sync.Mutex
	config  DeviceConfig
	handler clientHandler
	client  goburrowmodbus.Client
	status  DeviceStatus
}

type clientHandler interface {
	goburrowmodbus.ClientHandler
	Connect() error
	Close() error
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

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return ErrManagerClosed
	}

	if _, ok := m.devices[cfg.Name]; ok {
		return fmt.Errorf("%w: %s", ErrDeviceAlreadyRegistered, cfg.Name)
	}

	m.devices[cfg.Name] = &managedDevice{
		config: cfg,
		status: DeviceStatus{
			Name:        cfg.Name,
			Enabled:     cfg.Enabled,
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

// IsEnabled reports whether the device should be connected and served.
func (d DeviceConfig) IsEnabled() bool {
	return d.Enabled
}

func (m *Manager) getDevice(name string) (*managedDevice, error) {
	device, err := m.getDeviceAny(name)
	if err != nil {
		return nil, err
	}
	if !device.config.IsEnabled() {
		return nil, fmt.Errorf("%w: %s", ErrDeviceDisabled, name)
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
	if d.client != nil && d.handler != nil {
		return nil
	}

	handler, err := newClientHandler(d.config)
	if err != nil {
		d.status.Connected = false
		d.status.LastError = err.Error()
		return err
	}
	if err = handler.Connect(); err != nil {
		d.status.Connected = false
		d.status.LastError = err.Error()
		return err
	}

	d.handler = handler
	d.client = goburrowmodbus.NewClient(handler)
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
	if d.handler == nil {
		d.client = nil
		d.status.Connected = false
		return nil
	}

	err := d.handler.Close()
	d.handler = nil
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
	status.Name = d.config.Name
	status.Enabled = d.config.Enabled
	status.Description = d.config.Description
	status.Transport = strings.ToLower(d.config.Transport)
	status.DeviceID = d.config.DeviceID
	status.Connected = d.client != nil && d.handler != nil && d.status.Connected
	return status
}

func newClientHandler(device DeviceConfig) (clientHandler, error) {
	switch strings.ToLower(device.Transport) {
	case "tcp":
		if device.TCP == nil {
			return nil, fmt.Errorf("%w for device %q", ErrMissingTCPSettings, device.Name)
		}
		address := net.JoinHostPort(device.TCP.Host, strconv.Itoa(device.TCP.Port))
		handler := goburrowmodbus.NewTCPClientHandler(address)
		handler.Timeout = device.timeout()
		handler.SlaveId = device.DeviceID
		return handler, nil
	case "rtu":
		if device.Serial == nil {
			return nil, fmt.Errorf("%w for device %q", ErrMissingSerialSettings, device.Name)
		}
		handler := goburrowmodbus.NewRTUClientHandler(device.Serial.Port)
		handler.Timeout = device.timeout()
		handler.SlaveId = device.DeviceID
		handler.BaudRate = device.Serial.BaudRate
		handler.DataBits = device.Serial.DataBits
		handler.Parity = strings.ToUpper(device.Serial.Parity)
		handler.StopBits = device.Serial.StopBits
		return handler, nil
	case "ascii":
		if device.Serial == nil {
			return nil, fmt.Errorf("%w for device %q", ErrMissingSerialSettings, device.Name)
		}
		handler := goburrowmodbus.NewASCIIClientHandler(device.Serial.Port)
		handler.Timeout = device.timeout()
		handler.SlaveId = device.DeviceID
		handler.BaudRate = device.Serial.BaudRate
		handler.DataBits = device.Serial.DataBits
		handler.Parity = strings.ToUpper(device.Serial.Parity)
		handler.StopBits = device.Serial.StopBits
		return handler, nil
	default:
		return nil, fmt.Errorf("%w %q for device %q", ErrUnsupportedTransport, device.Transport, device.Name)
	}
}

func (d DeviceConfig) timeout() time.Duration {
	if d.Timeout <= 0 {
		return time.Second
	}

	return d.Timeout
}
