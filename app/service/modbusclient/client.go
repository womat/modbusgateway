// Package modbusclient contains the application-facing outbound Modbus client service.
package modbusclient

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/womat/modbusgateway/pkg/modbusmanager"
)

// ReadBitsRequest contains the validated parameters for a FC1 or FC2 read.
type ReadBitsRequest struct {
	Device   string
	Register uint16
	Length   uint16
}

// ReadRegistersRequest contains the validated parameters for a FC3 or FC4 read.
type ReadRegistersRequest struct {
	Device   string
	Register uint16
	Length   uint16
}

// WriteSingleCoilRequest contains the validated parameters for a FC5 write.
type WriteSingleCoilRequest struct {
	Device   string
	Register uint16
	Value    bool
}

// WriteSingleRegisterRequest contains the validated parameters for a FC6 write.
type WriteSingleRegisterRequest struct {
	Device   string
	Register uint16
	Value    uint16
}

// WriteMultipleCoilsRequest contains the validated parameters for a FC15 write.
type WriteMultipleCoilsRequest struct {
	Device   string
	Register uint16
	Values   []bool
}

// WriteMultipleRegistersRequest contains the validated parameters for a FC16 write.
type WriteMultipleRegistersRequest struct {
	Device   string
	Register uint16
	Values   []uint16
}

// ReadBitsResponse contains the normalized API response for a FC1 or FC2 read.
type ReadBitsResponse struct {
	Device       string `json:"device"`
	Transport    string `json:"transport"`
	DeviceID     uint8  `json:"deviceId"`
	FunctionCode uint8  `json:"functionCode"`
	Register     uint16 `json:"register"`
	Length       uint16 `json:"length"`
	DataHex      string `json:"dataHex"`
	Values       []bool `json:"values"`
}

// ReadRegistersResponse contains the normalized API response for a FC3 or FC4 read.
type ReadRegistersResponse struct {
	Device       string   `json:"device"`
	Transport    string   `json:"transport"`
	DeviceID     uint8    `json:"deviceId"`
	FunctionCode uint8    `json:"functionCode"`
	Register     uint16   `json:"register"`
	Length       uint16   `json:"length"`
	DataHex      string   `json:"dataHex"`
	Values       []uint16 `json:"values"`
}

// WriteResponse contains the normalized API response for write operations.
type WriteResponse struct {
	Device       string `json:"device"`
	Transport    string `json:"transport"`
	DeviceID     uint8  `json:"deviceId"`
	FunctionCode uint8  `json:"functionCode"`
	Register     uint16 `json:"register"`
	Length       uint16 `json:"length"`
	DataHex      string `json:"dataHex"`
}

// DeviceStatusResponse contains the normalized API response for one device status.
type DeviceStatusResponse struct {
	Device        string `json:"device"`
	Description   string `json:"description,omitempty"`
	Transport     string `json:"transport"`
	DeviceID      uint8  `json:"deviceId"`
	Connected     bool   `json:"connected"`
	LastError     string `json:"lastError,omitempty"`
	LastConnectAt string `json:"lastConnectAt,omitempty"`
	LastSuccessAt string `json:"lastSuccessAt,omitempty"`
	LastRequestAt string `json:"lastRequestAt,omitempty"`
}

// Service provides Modbus operations for the application layer.
type Service struct {
	manager *modbusmanager.Manager
}

// New returns a new Modbus service instance.
func New(manager *modbusmanager.Manager) *Service {
	return &Service{manager: manager}
}

// ReadCoils validates the request shape and executes a Modbus FC1 read via the manager.
func (s *Service) ReadCoils(req ReadBitsRequest) (ReadBitsResponse, error) {
	if err := validateReadBitsRequest(req); err != nil {
		return ReadBitsResponse{}, err
	}

	result, err := s.manager.ReadCoils(req.Device, req.Register, req.Length)
	if err != nil {
		return ReadBitsResponse{}, err
	}

	return mapReadBitsResponse(result.Device, 1, result.Register, result.Length, result.Data, result.Values), nil
}

// ReadDiscreteInputs validates the request shape and executes a Modbus FC2 read via the manager.
func (s *Service) ReadDiscreteInputs(req ReadBitsRequest) (ReadBitsResponse, error) {
	if err := validateReadBitsRequest(req); err != nil {
		return ReadBitsResponse{}, err
	}

	result, err := s.manager.ReadDiscreteInputs(req.Device, req.Register, req.Length)
	if err != nil {
		return ReadBitsResponse{}, err
	}

	return mapReadBitsResponse(result.Device, 2, result.Register, result.Length, result.Data, result.Values), nil
}

// ReadHoldingRegisters validates the request shape and executes a Modbus FC3 read via the manager.
func (s *Service) ReadHoldingRegisters(req ReadRegistersRequest) (ReadRegistersResponse, error) {
	if err := validateReadRegistersRequest(req); err != nil {
		return ReadRegistersResponse{}, err
	}

	result, err := s.manager.ReadHoldingRegisters(req.Device, req.Register, req.Length)
	if err != nil {
		return ReadRegistersResponse{}, err
	}

	return mapReadRegistersResponse(result.Device, 3, result.Register, result.Length, result.Data, result.Values), nil
}

// ReadInputRegisters validates the request shape and executes a Modbus FC4 read via the manager.
func (s *Service) ReadInputRegisters(req ReadRegistersRequest) (ReadRegistersResponse, error) {
	if err := validateReadRegistersRequest(req); err != nil {
		return ReadRegistersResponse{}, err
	}

	result, err := s.manager.ReadInputRegisters(req.Device, req.Register, req.Length)
	if err != nil {
		return ReadRegistersResponse{}, err
	}

	return mapReadRegistersResponse(result.Device, 4, result.Register, result.Length, result.Data, result.Values), nil
}

// WriteSingleCoil validates the request shape and executes a Modbus FC5 write via the manager.
func (s *Service) WriteSingleCoil(req WriteSingleCoilRequest) (WriteResponse, error) {
	if err := validateWriteSingleCoilRequest(req); err != nil {
		return WriteResponse{}, err
	}

	result, err := s.manager.WriteSingleCoil(req.Device, req.Register, req.Value)
	if err != nil {
		return WriteResponse{}, err
	}

	return mapWriteResponse(result.Device, 5, result.Register, result.Length, result.Data), nil
}

// WriteSingleRegister validates the request shape and executes a Modbus FC6 write via the manager.
func (s *Service) WriteSingleRegister(req WriteSingleRegisterRequest) (WriteResponse, error) {
	if err := validateWriteSingleRegisterRequest(req); err != nil {
		return WriteResponse{}, err
	}

	result, err := s.manager.WriteSingleRegister(req.Device, req.Register, req.Value)
	if err != nil {
		return WriteResponse{}, err
	}

	return mapWriteResponse(result.Device, 6, result.Register, result.Length, result.Data), nil
}

// WriteMultipleCoils validates the request shape and executes a Modbus FC15 write via the manager.
func (s *Service) WriteMultipleCoils(req WriteMultipleCoilsRequest) (WriteResponse, error) {
	if err := validateWriteMultipleCoilsRequest(req); err != nil {
		return WriteResponse{}, err
	}

	result, err := s.manager.WriteMultipleCoils(req.Device, req.Register, req.Values)
	if err != nil {
		return WriteResponse{}, err
	}

	return mapWriteResponse(result.Device, 15, result.Register, result.Length, result.Data), nil
}

// WriteMultipleRegisters validates the request shape and executes a Modbus FC16 write via the manager.
func (s *Service) WriteMultipleRegisters(req WriteMultipleRegistersRequest) (WriteResponse, error) {
	if err := validateWriteMultipleRegistersRequest(req); err != nil {
		return WriteResponse{}, err
	}

	result, err := s.manager.WriteMultipleRegisters(req.Device, req.Register, req.Values)
	if err != nil {
		return WriteResponse{}, err
	}

	return mapWriteResponse(result.Device, 16, result.Register, result.Length, result.Data), nil
}

// GetDeviceStatus returns the current status for one configured device.
func (s *Service) GetDeviceStatus(name string) (DeviceStatusResponse, error) {
	if s.manager == nil {
		return DeviceStatusResponse{}, modbusmanager.ErrManagerNotInitialized
	}
	if name == "" {
		return DeviceStatusResponse{}, modbusmanager.ErrDeviceNameEmpty
	}

	status, err := s.manager.Status(name)
	if err != nil {
		return DeviceStatusResponse{}, err
	}

	return mapStatus(status), nil
}

// ListDeviceStatus returns the current status for all configured devices.
func (s *Service) ListDeviceStatus() ([]DeviceStatusResponse, error) {
	if s.manager == nil {
		return nil, modbusmanager.ErrManagerNotInitialized
	}

	statuses := s.manager.ListStatus()
	resp := make([]DeviceStatusResponse, 0, len(statuses))
	for _, status := range statuses {
		resp = append(resp, mapStatus(status))
	}

	return resp, nil
}

func validateReadBitsRequest(req ReadBitsRequest) error {
	if req.Device == "" {
		return modbusmanager.ErrDeviceNameEmpty
	}
	if req.Length == 0 {
		return modbusmanager.ErrLengthMustBeGreaterThanZero
	}
	if req.Length > 2000 {
		return fmt.Errorf("%w: max=%d", modbusmanager.ErrLengthExceedsBitLimit, 2000)
	}

	return nil
}

func validateReadRegistersRequest(req ReadRegistersRequest) error {
	if req.Device == "" {
		return modbusmanager.ErrDeviceNameEmpty
	}
	if req.Length == 0 {
		return modbusmanager.ErrLengthMustBeGreaterThanZero
	}
	if req.Length > 125 {
		return fmt.Errorf("%w: max=%d", modbusmanager.ErrLengthExceedsRegisterLimit, 125)
	}

	return nil
}

func validateWriteSingleCoilRequest(req WriteSingleCoilRequest) error {
	if req.Device == "" {
		return modbusmanager.ErrDeviceNameEmpty
	}

	return nil
}

func validateWriteSingleRegisterRequest(req WriteSingleRegisterRequest) error {
	if req.Device == "" {
		return modbusmanager.ErrDeviceNameEmpty
	}

	return nil
}

func validateWriteMultipleCoilsRequest(req WriteMultipleCoilsRequest) error {
	if req.Device == "" {
		return modbusmanager.ErrDeviceNameEmpty
	}
	if len(req.Values) == 0 {
		return modbusmanager.ErrValuesEmpty
	}
	if len(req.Values) > 1968 {
		return fmt.Errorf("%w: max=%d", modbusmanager.ErrValuesExceedCoilLimit, 1968)
	}

	return nil
}

func validateWriteMultipleRegistersRequest(req WriteMultipleRegistersRequest) error {
	if req.Device == "" {
		return modbusmanager.ErrDeviceNameEmpty
	}
	if len(req.Values) == 0 {
		return modbusmanager.ErrValuesEmpty
	}
	if len(req.Values) > 123 {
		return fmt.Errorf("%w: max=%d", modbusmanager.ErrValuesExceedRegisterLimit, 123)
	}

	return nil
}

func mapReadBitsResponse(device modbusmanager.DeviceStatus, functionCode uint8, register, length uint16, data []byte, values []bool) ReadBitsResponse {
	return ReadBitsResponse{
		Device:       device.Name,
		Transport:    strings.ToLower(device.Transport),
		DeviceID:     device.DeviceID,
		FunctionCode: functionCode,
		Register:     register,
		Length:       length,
		DataHex:      strings.ToUpper(hex.EncodeToString(data)),
		Values:       values,
	}
}

func mapReadRegistersResponse(device modbusmanager.DeviceStatus, functionCode uint8, register, length uint16, data []byte, values []uint16) ReadRegistersResponse {
	return ReadRegistersResponse{
		Device:       device.Name,
		Transport:    strings.ToLower(device.Transport),
		DeviceID:     device.DeviceID,
		FunctionCode: functionCode,
		Register:     register,
		Length:       length,
		DataHex:      strings.ToUpper(hex.EncodeToString(data)),
		Values:       values,
	}
}

func mapWriteResponse(device modbusmanager.DeviceStatus, functionCode uint8, register, length uint16, data []byte) WriteResponse {
	return WriteResponse{
		Device:       device.Name,
		Transport:    strings.ToLower(device.Transport),
		DeviceID:     device.DeviceID,
		FunctionCode: functionCode,
		Register:     register,
		Length:       length,
		DataHex:      strings.ToUpper(hex.EncodeToString(data)),
	}
}

func mapStatus(status modbusmanager.DeviceStatus) DeviceStatusResponse {
	return DeviceStatusResponse{
		Device:        status.Name,
		Description:   status.Description,
		Transport:     strings.ToLower(status.Transport),
		DeviceID:      status.DeviceID,
		Connected:     status.Connected,
		LastError:     status.LastError,
		LastConnectAt: formatTime(status.LastConnectAt),
		LastSuccessAt: formatTime(status.LastSuccessAt),
		LastRequestAt: formatTime(status.LastRequestAt),
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}

	return t.UTC().Format(time.RFC3339)
}
