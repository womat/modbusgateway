// Package modbusserver exposes configured devices via an optional Modbus TCP gateway server.
package modbusserver

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	simonmodbus "github.com/simonvetter/modbus"
	"github.com/womat/modbusgateway/pkg/modbusmanager"
)

var (
	ErrNoGatewayDevicesConfigured = errors.New("no gateway devices configured")
	ErrServerAlreadyRunning       = errors.New("modbus TCP server is already running")
)

// Server exposes configured devices via Modbus TCP.
type Server struct {
	addr    string
	manager *modbusmanager.Manager
	devices map[uint8]string

	mu     sync.Mutex
	server *simonmodbus.ModbusServer
}

// New creates a new Modbus TCP gateway server instance.
func New(listenHost string, listenPort int, manager *modbusmanager.Manager, devices map[uint8]string) *Server {
	return &Server{
		addr:    net.JoinHostPort(listenHost, strconv.Itoa(listenPort)),
		manager: manager,
		devices: devices,
	}
}

// Start begins accepting Modbus TCP connections.
func (s *Server) Start() error {
	if s.manager == nil {
		return modbusmanager.ErrManagerNotInitialized
	}
	if len(s.devices) == 0 {
		return ErrNoGatewayDevicesConfigured
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.server != nil {
		return ErrServerAlreadyRunning
	}

	server, err := simonmodbus.NewServer(&simonmodbus.ServerConfiguration{
		URL:        "tcp://" + s.addr,
		Timeout:    120 * time.Second,
		MaxClients: 10,
	}, &requestHandler{
		manager: s.manager,
		devices: s.devices,
	})
	if err != nil {
		return fmt.Errorf("create modbus TCP server on %s: %w", s.addr, err)
	}

	if err := server.Start(); err != nil {
		return fmt.Errorf("start modbus TCP server on %s: %w", s.addr, err)
	}

	s.server = server
	slog.Info("Modbus TCP server started", "addr", s.addr, "devices", len(s.devices))
	return nil
}

// Close stops the server.
func (s *Server) Close() error {
	s.mu.Lock()
	server := s.server
	s.server = nil
	s.mu.Unlock()

	if server == nil {
		return nil
	}

	if err := server.Stop(); err != nil {
		return fmt.Errorf("stop modbus TCP server on %s: %w", s.addr, err)
	}

	slog.Info("Modbus TCP server stopped", "addr", s.addr)
	return nil
}

type requestHandler struct {
	manager *modbusmanager.Manager
	devices map[uint8]string
}

func (h *requestHandler) HandleCoils(req *simonmodbus.CoilsRequest) ([]bool, error) {
	deviceName, err := h.deviceName(req.UnitId)
	if err != nil {
		return nil, err
	}
	if req.IsWrite {
		return nil, simonmodbus.ErrIllegalFunction
	}

	result, err := h.manager.ReadCoils(deviceName, req.Addr, req.Quantity)
	if err != nil {
		return nil, mapManagerError(err)
	}

	return result.Values, nil
}

func (h *requestHandler) HandleDiscreteInputs(req *simonmodbus.DiscreteInputsRequest) ([]bool, error) {
	deviceName, err := h.deviceName(req.UnitId)
	if err != nil {
		return nil, err
	}

	result, err := h.manager.ReadDiscreteInputs(deviceName, req.Addr, req.Quantity)
	if err != nil {
		return nil, mapManagerError(err)
	}

	return result.Values, nil
}

func (h *requestHandler) HandleHoldingRegisters(req *simonmodbus.HoldingRegistersRequest) ([]uint16, error) {
	deviceName, err := h.deviceName(req.UnitId)
	if err != nil {
		return nil, err
	}
	if req.IsWrite {
		return nil, simonmodbus.ErrIllegalFunction
	}

	result, err := h.manager.ReadHoldingRegisters(deviceName, req.Addr, req.Quantity)
	if err != nil {
		return nil, mapManagerError(err)
	}

	return result.Values, nil
}

func (h *requestHandler) HandleInputRegisters(req *simonmodbus.InputRegistersRequest) ([]uint16, error) {
	deviceName, err := h.deviceName(req.UnitId)
	if err != nil {
		return nil, err
	}

	result, err := h.manager.ReadInputRegisters(deviceName, req.Addr, req.Quantity)
	if err != nil {
		return nil, mapManagerError(err)
	}

	return result.Values, nil
}

func (h *requestHandler) deviceName(unitID uint8) (string, error) {
	deviceName, ok := h.devices[unitID]
	if !ok {
		return "", simonmodbus.ErrGWPathUnavailable
	}

	return deviceName, nil
}

func mapManagerError(err error) error {
	if err == nil {
		return simonmodbus.ErrGWTargetFailedToRespond
	}

	switch {
	case errors.Is(err, modbusmanager.ErrLengthMustBeGreaterThanZero),
		errors.Is(err, modbusmanager.ErrLengthExceedsBitLimit),
		errors.Is(err, modbusmanager.ErrLengthExceedsRegisterLimit):
		return simonmodbus.ErrIllegalDataValue
	case errors.Is(err, modbusmanager.ErrDeviceNotConfigured):
		return simonmodbus.ErrGWPathUnavailable
	default:
		return simonmodbus.ErrGWTargetFailedToRespond
	}
}
