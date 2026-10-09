// Package modbusserver offers configured devices on a Modbus listener: TCP clients reach the
// devices on serial buses, a client on a serial bus reaches the devices on TCP.
//
// Every request is forwarded to the modbusmanager, so it shares the bus queue and the cache
// with the REST API. The unit ID of a request selects the device.
package modbusserver

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	simonmodbus "github.com/simonvetter/modbus"
	"github.com/womat/mbserver"
	"github.com/womat/modbusgateway/pkg/modbusmanager"
)

var (
	ErrNoGatewayDevicesConfigured = errors.New("no gateway devices configured")
	ErrServerAlreadyRunning       = errors.New("modbus server is already running")
	ErrNoListener                 = errors.New("exactly one of tcp and rtu must be set")
)

// Listener is where the server accepts requests: TCP or a serial port, exactly one of them.
type Listener struct {
	TCP *TCPListener
	RTU *RTUListener
}

// TCPListener is a Modbus TCP listen address.
type TCPListener struct {
	Host string
	Port int
}

// RTUListener is a serial port on which the server answers as a Modbus RTU server.
type RTUListener struct {
	Port            string
	BaudRate        int
	DataBits        int
	Parity          string // N | E | O
	StopBits        int    // 1 | 2
	InterFrameDelay time.Duration
}

// String names the listener for logs and errors.
func (l Listener) String() string {
	switch {
	case l.TCP != nil:
		return "tcp " + net.JoinHostPort(l.TCP.Host, strconv.Itoa(l.TCP.Port))
	case l.RTU != nil:
		return "rtu " + l.RTU.Port
	default:
		return "none"
	}
}

// Server offers devices on one listener.
type Server struct {
	listener Listener
	manager  *modbusmanager.Manager
	devices  map[uint8]string // unit ID on this listener -> device name

	mu     sync.Mutex
	server *mbserver.Server
	cancel context.CancelFunc
}

// New creates a server; devices maps the unit IDs of this listener to device names.
func New(listener Listener, manager *modbusmanager.Manager, devices map[uint8]string) *Server {
	return &Server{listener: listener, manager: manager, devices: devices}
}

// Start begins accepting requests.
func (s *Server) Start(ctx context.Context) error {
	if s.manager == nil {
		return modbusmanager.ErrManagerNotInitialized
	}
	if len(s.devices) == 0 {
		return ErrNoGatewayDevicesConfigured
	}
	if (s.listener.TCP == nil) == (s.listener.RTU == nil) {
		return ErrNoListener
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil {
		return ErrServerAlreadyRunning
	}

	server := mbserver.NewServer(slog.Default().With("component", "mbserver", "listener", s.listener.String()))
	// A write to unit ID 0 must not reach every device behind the gateway.
	server.SetBroadcast(false)
	// Unit ID 1 exists from the start; keep only the configured ones.
	_ = server.RemoveUnit(1)
	for id := range s.devices {
		if err := server.NewUnit(id); err != nil {
			return fmt.Errorf("unit id %d: %w", id, err)
		}
	}
	h := &handler{manager: s.manager, devices: s.devices}
	server.RegisterFunctionHandler(1, h.readCoils)
	server.RegisterFunctionHandler(2, h.readDiscreteInputs)
	server.RegisterFunctionHandler(3, h.readHoldingRegisters)
	server.RegisterFunctionHandler(4, h.readInputRegisters)
	server.RegisterFunctionHandler(5, h.writeSingleCoil)
	server.RegisterFunctionHandler(6, h.writeSingleRegister)
	server.RegisterFunctionHandler(15, h.writeMultipleCoils)
	server.RegisterFunctionHandler(16, h.writeMultipleRegisters)

	ctx, cancel := context.WithCancel(ctx)
	if err := server.Start(ctx); err != nil {
		cancel()
		return fmt.Errorf("start modbus server on %s: %w", s.listener, err)
	}
	if err := s.listen(ctx, server); err != nil {
		cancel()
		server.Close()
		return err
	}

	s.server, s.cancel = server, cancel
	slog.Info("Modbus server started", "listener", s.listener.String(), "devices", len(s.devices))
	return nil
}

func (s *Server) listen(ctx context.Context, server *mbserver.Server) error {
	if tcp := s.listener.TCP; tcp != nil {
		address := net.JoinHostPort(tcp.Host, strconv.Itoa(tcp.Port))
		if err := server.ListenTCP(ctx, address); err != nil {
			return fmt.Errorf("listen tcp %s: %w", address, err)
		}
		return nil
	}

	config, err := serialConfig(*s.listener.RTU)
	if err != nil {
		return err
	}
	// mbserver detects the end of a request by the inter-frame delay and reopens the port
	// after a failure, e.g. an unplugged USB adapter.
	if err = server.ListenRTU(ctx, s.listener.RTU.Port, config); err != nil {
		return fmt.Errorf("listen rtu %s: %w", s.listener.RTU.Port, err)
	}
	return nil
}

// Close stops the server and disconnects its clients.
func (s *Server) Close() error {
	s.mu.Lock()
	server, cancel := s.server, s.cancel
	s.server, s.cancel = nil, nil
	s.mu.Unlock()

	if server == nil {
		return nil
	}
	cancel()
	server.Close()
	slog.Info("Modbus server stopped", "listener", s.listener.String())
	return nil
}

func serialConfig(l RTUListener) (mbserver.SerialConfig, error) {
	config := mbserver.SerialConfig{BaudRate: l.BaudRate, DataBits: l.DataBits, InterFrameDelay: l.InterFrameDelay}

	switch strings.ToUpper(l.Parity) {
	case "N":
		config.Parity = mbserver.NoParity
	case "E":
		config.Parity = mbserver.EvenParity
	case "O":
		config.Parity = mbserver.OddParity
	default:
		return config, fmt.Errorf("unsupported parity %q", l.Parity)
	}

	switch l.StopBits {
	case 1:
		config.StopBits = mbserver.OneStopBit
	case 2:
		config.StopBits = mbserver.TwoStopBits
	default:
		return config, fmt.Errorf("unsupported stop bits %d", l.StopBits)
	}
	return config, nil
}

// handler forwards the requests of one listener to the manager.
type handler struct {
	manager *modbusmanager.Manager
	devices map[uint8]string
}

// device returns the device of the request; mbserver only passes configured unit IDs on.
func (h *handler) device(frame mbserver.Framer) (string, bool) {
	name, ok := h.devices[frame.GetUnitId()]
	return name, ok
}

// addressAndQuantity decodes the data of FC1-6: address and quantity or value, exactly 4 bytes.
func addressAndQuantity(frame mbserver.Framer) (uint16, uint16, bool) {
	data := frame.GetData()
	if len(data) != 4 {
		return 0, 0, false
	}
	return binary.BigEndian.Uint16(data[0:2]), binary.BigEndian.Uint16(data[2:4]), true
}

// multipleWrite decodes the data of FC15/16: address, quantity, byte count and the values. The
// byte count must match the payload; wantBytes computes it from the quantity.
func multipleWrite(frame mbserver.Framer, wantBytes func(qty uint16) int) (uint16, uint16, []byte, bool) {
	data := frame.GetData()
	if len(data) < 5 {
		return 0, 0, nil, false
	}
	addr := binary.BigEndian.Uint16(data[0:2])
	qty := binary.BigEndian.Uint16(data[2:4])
	count := int(data[4])
	if qty == 0 || count != wantBytes(qty) || len(data) != 5+count {
		return 0, 0, nil, false
	}
	return addr, qty, data[5:], true
}

func (h *handler) readCoils(_ *mbserver.Server, frame mbserver.Framer) ([]byte, mbserver.Exception) {
	return h.readBits(frame, h.manager.ReadCoils)
}

func (h *handler) readDiscreteInputs(_ *mbserver.Server, frame mbserver.Framer) ([]byte, mbserver.Exception) {
	return h.readBits(frame, h.manager.ReadDiscreteInputs)
}

func (h *handler) readBits(frame mbserver.Framer, read func(string, uint16, uint16) (modbusmanager.ReadBitsResult, error)) ([]byte, mbserver.Exception) {
	name, ok := h.device(frame)
	if !ok {
		return nil, mbserver.GatewayPathUnavailable
	}
	addr, qty, ok := addressAndQuantity(frame)
	if !ok {
		return nil, mbserver.IllegalDataValue
	}
	result, err := read(name, addr, qty)
	if err != nil {
		return nil, exception(err)
	}
	return append([]byte{byte(len(result.Data))}, result.Data...), mbserver.Success
}

func (h *handler) readHoldingRegisters(_ *mbserver.Server, frame mbserver.Framer) ([]byte, mbserver.Exception) {
	return h.readRegisters(frame, h.manager.ReadHoldingRegisters)
}

func (h *handler) readInputRegisters(_ *mbserver.Server, frame mbserver.Framer) ([]byte, mbserver.Exception) {
	return h.readRegisters(frame, h.manager.ReadInputRegisters)
}

func (h *handler) readRegisters(frame mbserver.Framer, read func(string, uint16, uint16) (modbusmanager.ReadRegistersResult, error)) ([]byte, mbserver.Exception) {
	name, ok := h.device(frame)
	if !ok {
		return nil, mbserver.GatewayPathUnavailable
	}
	addr, qty, ok := addressAndQuantity(frame)
	if !ok {
		return nil, mbserver.IllegalDataValue
	}
	result, err := read(name, addr, qty)
	if err != nil {
		return nil, exception(err)
	}
	return append([]byte{byte(len(result.Data))}, result.Data...), mbserver.Success
}

func (h *handler) writeSingleCoil(_ *mbserver.Server, frame mbserver.Framer) ([]byte, mbserver.Exception) {
	name, ok := h.device(frame)
	if !ok {
		return nil, mbserver.GatewayPathUnavailable
	}
	addr, value, ok := addressAndQuantity(frame)
	if !ok || (value != 0xFF00 && value != 0x0000) {
		return nil, mbserver.IllegalDataValue
	}
	result, err := h.manager.WriteSingleCoil(name, addr, value == 0xFF00)
	if err != nil {
		return nil, exception(err)
	}
	return result.Data, mbserver.Success
}

func (h *handler) writeSingleRegister(_ *mbserver.Server, frame mbserver.Framer) ([]byte, mbserver.Exception) {
	name, ok := h.device(frame)
	if !ok {
		return nil, mbserver.GatewayPathUnavailable
	}
	addr, value, ok := addressAndQuantity(frame)
	if !ok {
		return nil, mbserver.IllegalDataValue
	}
	result, err := h.manager.WriteSingleRegister(name, addr, value)
	if err != nil {
		return nil, exception(err)
	}
	return result.Data, mbserver.Success
}

func (h *handler) writeMultipleCoils(_ *mbserver.Server, frame mbserver.Framer) ([]byte, mbserver.Exception) {
	name, ok := h.device(frame)
	if !ok {
		return nil, mbserver.GatewayPathUnavailable
	}
	addr, qty, payload, ok := multipleWrite(frame, func(qty uint16) int { return (int(qty) + 7) / 8 })
	if !ok {
		return nil, mbserver.IllegalDataValue
	}
	values := make([]bool, qty)
	for i := range values {
		values[i] = payload[i/8]&(1<<(uint(i)%8)) != 0
	}
	result, err := h.manager.WriteMultipleCoils(name, addr, values)
	if err != nil {
		return nil, exception(err)
	}
	return result.Data, mbserver.Success
}

func (h *handler) writeMultipleRegisters(_ *mbserver.Server, frame mbserver.Framer) ([]byte, mbserver.Exception) {
	name, ok := h.device(frame)
	if !ok {
		return nil, mbserver.GatewayPathUnavailable
	}
	addr, qty, payload, ok := multipleWrite(frame, func(qty uint16) int { return int(qty) * 2 })
	if !ok {
		return nil, mbserver.IllegalDataValue
	}
	values := make([]uint16, qty)
	for i := range values {
		values[i] = binary.BigEndian.Uint16(payload[i*2:])
	}
	result, err := h.manager.WriteMultipleRegisters(name, addr, values)
	if err != nil {
		return nil, exception(err)
	}
	return result.Data, mbserver.Success
}

// deviceExceptions passes an exception of the device on unchanged.
var deviceExceptions = []struct {
	err       error
	exception mbserver.Exception
}{
	{simonmodbus.ErrIllegalFunction, mbserver.IllegalFunction},
	{simonmodbus.ErrIllegalDataAddress, mbserver.IllegalDataAddress},
	{simonmodbus.ErrIllegalDataValue, mbserver.IllegalDataValue},
	{simonmodbus.ErrServerDeviceFailure, mbserver.ServerDeviceFailure},
	{simonmodbus.ErrAcknowledge, mbserver.Acknowledge},
	{simonmodbus.ErrServerDeviceBusy, mbserver.ServerDeviceBusy},
	{simonmodbus.ErrMemoryParityError, mbserver.MemoryParityError},
	{simonmodbus.ErrGWPathUnavailable, mbserver.GatewayPathUnavailable},
	{simonmodbus.ErrGWTargetFailedToRespond, mbserver.GatewayTargetDeviceFailedToRespond},
}

// exception maps a manager error to the exception the client gets.
func exception(err error) mbserver.Exception {
	switch {
	case errors.Is(err, modbusmanager.ErrFunctionNotAllowed):
		return mbserver.IllegalFunction
	case errors.Is(err, modbusmanager.ErrLengthMustBeGreaterThanZero),
		errors.Is(err, modbusmanager.ErrLengthExceedsBitLimit),
		errors.Is(err, modbusmanager.ErrLengthExceedsRegisterLimit),
		errors.Is(err, modbusmanager.ErrValuesEmpty),
		errors.Is(err, modbusmanager.ErrValuesExceedCoilLimit),
		errors.Is(err, modbusmanager.ErrValuesExceedRegisterLimit):
		return mbserver.IllegalDataValue
	case errors.Is(err, modbusmanager.ErrDeviceNotConfigured):
		return mbserver.GatewayPathUnavailable
	case errors.Is(err, modbusmanager.ErrBusBusy):
		return mbserver.ServerDeviceBusy
	}
	for _, e := range deviceExceptions {
		if errors.Is(err, e.err) {
			return e.exception
		}
	}
	// Timeouts, a full queue wait and connection failures: the device did not answer.
	return mbserver.GatewayTargetDeviceFailedToRespond
}
