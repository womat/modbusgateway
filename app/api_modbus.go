package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/womat/golib/web"
	"github.com/womat/modbusgateway/app/service/modbusclient"
	"github.com/womat/modbusgateway/pkg/modbusmanager"
)

var (
	ErrMissingRequestBody   = errors.New("missing request body")
	ErrRequestBodyNotSingle = errors.New("request body must contain a single JSON object")
	ErrValueXorValues       = errors.New(`request body needs either "value" (single write) or "values" (multiple write)`)
)

type ModbusErrorResponse struct {
	Error string `json:"error"`
}

// ModbusWriteCoilsRequest is the body of a coil write: "value" writes one coil with FC5,
// "values" writes one or more coils with FC15. Exactly one of them must be set.
type ModbusWriteCoilsRequest struct {
	Value  *bool  `json:"value,omitempty" example:"true"`
	Values []bool `json:"values,omitempty"`
}

// ModbusWriteRegistersRequest is the body of a holding register write: "value" writes one
// register with FC6, "values" writes one or more registers with FC16. Exactly one of them must
// be set.
type ModbusWriteRegistersRequest struct {
	Value  *uint16  `json:"value,omitempty" example:"7"`
	Values []uint16 `json:"values,omitempty"`
}

// HandleModbusListDeviceStatus returns the current manager status for all configured devices.
//
//	@Summary		List configured device status
//	@Description	Returns the current connection and health status for all configured Modbus devices.
//	@Tags			modbus
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Success		200	{array}		modbusclient.DeviceStatusResponse	"Device status list"
//	@Failure		401	{string}	string								"Unauthorized"
//	@Failure		503	{object}	ModbusErrorResponse					"Modbus service unavailable"
//	@Router			/devices [get]
func (app *App) HandleModbusListDeviceStatus() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if app.modbusClient == nil {
			web.Encode(w, http.StatusServiceUnavailable, ModbusErrorResponse{Error: "modbus service is not initialized"})
			return
		}

		resp, err := app.modbusClient.ListDeviceStatus()
		if err != nil {
			web.Encode(w, http.StatusServiceUnavailable, ModbusErrorResponse{Error: err.Error()})
			return
		}

		web.Encode(w, http.StatusOK, resp)
	})
}

// HandleModbusGetDeviceStatus returns the current manager status for one configured device.
//
//	@Summary		Get device status
//	@Description	Returns the current connection and health status for one configured Modbus device.
//	@Tags			modbus
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			device	path		string								true	"Configured device name"
//	@Success		200		{object}	modbusclient.DeviceStatusResponse	"Device status"
//	@Failure		401		{string}	string								"Unauthorized"
//	@Failure		404		{object}	ModbusErrorResponse					"Device not found"
//	@Failure		503		{object}	ModbusErrorResponse					"Modbus service unavailable"
//	@Router			/devices/{device}/status [get]
func (app *App) HandleModbusGetDeviceStatus() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if app.modbusClient == nil {
			web.Encode(w, http.StatusServiceUnavailable, ModbusErrorResponse{Error: "modbus service is not initialized"})
			return
		}

		device := r.PathValue("device")
		if device == "" {
			web.Encode(w, http.StatusBadRequest, ModbusErrorResponse{Error: "missing device path parameter"})
			return
		}

		resp, err := app.modbusClient.GetDeviceStatus(device)
		if err != nil {
			web.Encode(w, http.StatusNotFound, ModbusErrorResponse{Error: err.Error()})
			return
		}

		web.Encode(w, http.StatusOK, resp)
	})
}

// HandleModbusReadCoils reads coils from a configured device.
//
//	@Summary		FC 1: Read coils
//	@Description	Reads one or more coils from a configured Modbus device.
//	@Tags			modbus
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			device		path		string							true	"Configured device name"
//	@Param			address		path		string							true	"Start address (decimal or 0x-prefixed hex)"
//	@Param			quantity	query		int								false	"Number of values to read"	default(1)
//	@Success		200			{object}	modbusclient.ReadBitsResponse	"Coils successfully read"
//	@Failure		400			{object}	ModbusErrorResponse				"Invalid request"
//	@Failure		502			{object}	ModbusErrorResponse				"Modbus read failed"
//	@Failure		403			{object}	ModbusErrorResponse				"Function code not allowed for the device"
//	@Failure		404			{object}	ModbusErrorResponse				"Device not found"
//	@Failure		503			{object}	ModbusErrorResponse				"Bus queue full"
//	@Failure		504			{object}	ModbusErrorResponse				"Request waited too long in the bus queue"
//	@Failure		401			{string}	string							"Unauthorized"
//	@Router			/devices/{device}/coils/{address} [get]
func (app *App) HandleModbusReadCoils() http.Handler {
	return app.handleReadBits(func(req modbusclient.ReadBitsRequest) (modbusclient.ReadBitsResponse, error) {
		return app.modbusClient.ReadCoils(req)
	})
}

// HandleModbusReadDiscreteInputs reads discrete inputs from a configured device.
//
//	@Summary		FC 2: Read discrete inputs
//	@Description	Reads one or more discrete inputs from a configured Modbus device.
//	@Tags			modbus
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			device		path		string							true	"Configured device name"
//	@Param			address		path		string							true	"Start address (decimal or 0x-prefixed hex)"
//	@Param			quantity	query		int								false	"Number of values to read"	default(1)
//	@Success		200			{object}	modbusclient.ReadBitsResponse	"Discrete inputs successfully read"
//	@Failure		400			{object}	ModbusErrorResponse				"Invalid request"
//	@Failure		502			{object}	ModbusErrorResponse				"Modbus read failed"
//	@Failure		403			{object}	ModbusErrorResponse				"Function code not allowed for the device"
//	@Failure		404			{object}	ModbusErrorResponse				"Device not found"
//	@Failure		503			{object}	ModbusErrorResponse				"Bus queue full"
//	@Failure		504			{object}	ModbusErrorResponse				"Request waited too long in the bus queue"
//	@Failure		401			{string}	string							"Unauthorized"
//	@Router			/devices/{device}/discrete-inputs/{address} [get]
func (app *App) HandleModbusReadDiscreteInputs() http.Handler {
	return app.handleReadBits(func(req modbusclient.ReadBitsRequest) (modbusclient.ReadBitsResponse, error) {
		return app.modbusClient.ReadDiscreteInputs(req)
	})
}

// HandleModbusReadHoldingRegisters reads holding registers from a configured device.
//
//	@Summary		FC 3: Read holding registers
//	@Description	Reads one or more holding registers from a configured Modbus device.
//	@Tags			modbus
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			device		path		string								true	"Configured device name"
//	@Param			address		path		string								true	"Start address (decimal or 0x-prefixed hex)"
//	@Param			quantity	query		int									false	"Number of values to read"	default(1)
//	@Success		200			{object}	modbusclient.ReadRegistersResponse	"Holding registers successfully read"
//	@Failure		400			{object}	ModbusErrorResponse					"Invalid request"
//	@Failure		502			{object}	ModbusErrorResponse					"Modbus read failed"
//	@Failure		403			{object}	ModbusErrorResponse					"Function code not allowed for the device"
//	@Failure		404			{object}	ModbusErrorResponse					"Device not found"
//	@Failure		503			{object}	ModbusErrorResponse					"Bus queue full"
//	@Failure		504			{object}	ModbusErrorResponse					"Request waited too long in the bus queue"
//	@Failure		401			{string}	string								"Unauthorized"
//	@Router			/devices/{device}/holding-registers/{address} [get]
func (app *App) HandleModbusReadHoldingRegisters() http.Handler {
	return app.handleReadRegisters(func(req modbusclient.ReadRegistersRequest) (modbusclient.ReadRegistersResponse, error) {
		return app.modbusClient.ReadHoldingRegisters(req)
	})
}

// HandleModbusReadInputRegisters reads input registers from a configured device.
//
//	@Summary		FC 4: Read input registers
//	@Description	Reads one or more input registers from a configured Modbus device.
//	@Tags			modbus
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			device		path		string								true	"Configured device name"
//	@Param			address		path		string								true	"Start address (decimal or 0x-prefixed hex)"
//	@Param			quantity	query		int									false	"Number of values to read"	default(1)
//	@Failure		400			{object}	ModbusErrorResponse					"Invalid request"
//	@Success		200			{object}	modbusclient.ReadRegistersResponse	"Input registers successfully read"
//	@Failure		502			{object}	ModbusErrorResponse					"Modbus read failed"
//	@Failure		403			{object}	ModbusErrorResponse					"Function code not allowed for the device"
//	@Failure		404			{object}	ModbusErrorResponse					"Device not found"
//	@Failure		503			{object}	ModbusErrorResponse					"Bus queue full"
//	@Failure		504			{object}	ModbusErrorResponse					"Request waited too long in the bus queue"
//	@Failure		401			{string}	string								"Unauthorized"
//	@Router			/devices/{device}/input-registers/{address} [get]
func (app *App) HandleModbusReadInputRegisters() http.Handler {
	return app.handleReadRegisters(func(req modbusclient.ReadRegistersRequest) (modbusclient.ReadRegistersResponse, error) {
		return app.modbusClient.ReadInputRegisters(req)
	})
}

// HandleModbusWriteCoils writes coils on a configured device.
//
//	@Summary		FC 5 / FC 15: Write coils
//	@Description	Writes coils starting at the address. {"value": true} writes one coil with FC 5, {"values": [true, false]} writes one or more coils with FC 15 — also for a single value, for devices that only accept FC 15.
//	@Tags			modbus
//	@Accept			json
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			device	path		string						true	"Configured device name"
//	@Param			address	path		string						true	"Start address (decimal or 0x-prefixed hex)"
//	@Param			request	body		ModbusWriteCoilsRequest		true	"Either value (FC 5) or values (FC 15)"
//	@Success		200		{object}	modbusclient.WriteResponse	"Coils successfully written"
//	@Failure		400		{object}	ModbusErrorResponse			"Invalid request"
//	@Failure		502		{object}	ModbusErrorResponse			"Modbus write failed"
//	@Failure		403		{object}	ModbusErrorResponse			"Function code not allowed for the device"
//	@Failure		404		{object}	ModbusErrorResponse			"Device not found"
//	@Failure		503		{object}	ModbusErrorResponse			"Bus queue full"
//	@Failure		504		{object}	ModbusErrorResponse			"Request waited too long in the bus queue"
//	@Failure		401		{string}	string						"Unauthorized"
//	@Router			/devices/{device}/coils/{address} [put]
func (app *App) HandleModbusWriteCoils() http.Handler {
	return app.handleWrite(func(device string, register uint16, r *http.Request) (modbusclient.WriteResponse, error) {
		var body ModbusWriteCoilsRequest
		if err := decodeWriteBody(r, &body, func() (bool, bool) { return body.Value != nil, body.Values != nil }); err != nil {
			return modbusclient.WriteResponse{}, err
		}
		if body.Value != nil {
			return app.modbusClient.WriteSingleCoil(modbusclient.WriteSingleCoilRequest{Device: device, Register: register, Value: *body.Value})
		}
		return app.modbusClient.WriteMultipleCoils(modbusclient.WriteMultipleCoilsRequest{Device: device, Register: register, Values: body.Values})
	})
}

// HandleModbusWriteHoldingRegisters writes holding registers on a configured device.
//
//	@Summary		FC 6 / FC 16: Write holding registers
//	@Description	Writes holding registers starting at the address. {"value": 7} writes one register with FC 6, {"values": [7, 8]} writes one or more registers with FC 16 — also for a single value, for devices that only accept FC 16.
//	@Tags			modbus
//	@Accept			json
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			device	path		string						true	"Configured device name"
//	@Param			address	path		string						true	"Start address (decimal or 0x-prefixed hex)"
//	@Param			request	body		ModbusWriteRegistersRequest	true	"Either value (FC 6) or values (FC 16)"
//	@Success		200		{object}	modbusclient.WriteResponse	"Registers successfully written"
//	@Failure		400		{object}	ModbusErrorResponse			"Invalid request"
//	@Failure		502		{object}	ModbusErrorResponse			"Modbus write failed"
//	@Failure		403		{object}	ModbusErrorResponse			"Function code not allowed for the device"
//	@Failure		404		{object}	ModbusErrorResponse			"Device not found"
//	@Failure		503		{object}	ModbusErrorResponse			"Bus queue full"
//	@Failure		504		{object}	ModbusErrorResponse			"Request waited too long in the bus queue"
//	@Failure		401		{string}	string						"Unauthorized"
//	@Router			/devices/{device}/holding-registers/{address} [put]
func (app *App) HandleModbusWriteHoldingRegisters() http.Handler {
	return app.handleWrite(func(device string, register uint16, r *http.Request) (modbusclient.WriteResponse, error) {
		var body ModbusWriteRegistersRequest
		if err := decodeWriteBody(r, &body, func() (bool, bool) { return body.Value != nil, body.Values != nil }); err != nil {
			return modbusclient.WriteResponse{}, err
		}
		if body.Value != nil {
			return app.modbusClient.WriteSingleRegister(modbusclient.WriteSingleRegisterRequest{Device: device, Register: register, Value: *body.Value})
		}
		return app.modbusClient.WriteMultipleRegisters(modbusclient.WriteMultipleRegistersRequest{Device: device, Register: register, Values: body.Values})
	})
}

func parseUint16(value string, field string) (uint16, error) {
	parsed, err := parseModbusUint16(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: must be an unsigned 16-bit integer in decimal or 0x-prefixed hex", field)
	}

	return parsed, nil
}

func parseOptionalUint16(value string, defaultValue uint16, field string) (uint16, error) {
	if value == "" {
		return defaultValue, nil
	}

	return parseUint16(value, field)
}

func parseModbusUint16(value string) (uint16, error) {
	if value == "" {
		return 0, errors.New("empty value")
	}

	base := 10
	if strings.HasPrefix(value, "0x") || strings.HasPrefix(value, "0X") {
		base = 16
		value = value[2:]
	}

	parsed, err := strconv.ParseUint(value, base, 16)
	if err != nil {
		return 0, err
	}

	return uint16(parsed), nil
}

func (app *App) handleReadBits(read func(modbusclient.ReadBitsRequest) (modbusclient.ReadBitsResponse, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if app.modbusClient == nil {
			web.Encode(w, http.StatusServiceUnavailable, ModbusErrorResponse{Error: "modbus service is not initialized"})
			return
		}

		device, register, ok := parseDeviceRegisterPath(w, r)
		if !ok {
			return
		}

		quantityParam := r.URL.Query().Get("quantity")
		length, err := parseOptionalUint16(quantityParam, 1, "quantity")
		if err != nil {
			web.Encode(w, http.StatusBadRequest, ModbusErrorResponse{Error: err.Error()})
			return
		}

		resp, err := read(modbusclient.ReadBitsRequest{
			Device:   device,
			Register: register,
			Length:   length,
		})
		if err != nil {
			web.Encode(w, statusForModbusError(err), ModbusErrorResponse{Error: err.Error()})
			return
		}

		web.Encode(w, http.StatusOK, resp)
	})
}

func (app *App) handleReadRegisters(read func(modbusclient.ReadRegistersRequest) (modbusclient.ReadRegistersResponse, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if app.modbusClient == nil {
			web.Encode(w, http.StatusServiceUnavailable, ModbusErrorResponse{Error: "modbus service is not initialized"})
			return
		}

		device, register, ok := parseDeviceRegisterPath(w, r)
		if !ok {
			return
		}

		quantityParam := r.URL.Query().Get("quantity")
		length, err := parseOptionalUint16(quantityParam, 1, "quantity")
		if err != nil {
			web.Encode(w, http.StatusBadRequest, ModbusErrorResponse{Error: err.Error()})
			return
		}

		resp, err := read(modbusclient.ReadRegistersRequest{
			Device:   device,
			Register: register,
			Length:   length,
		})
		if err != nil {
			web.Encode(w, statusForModbusError(err), ModbusErrorResponse{Error: err.Error()})
			return
		}

		web.Encode(w, http.StatusOK, resp)
	})
}

// requestError is a problem with the request itself; the handler answers 400.
type requestError struct{ error }

// decodeWriteBody decodes a write body into dst; set reports afterwards whether value and values
// are present, and exactly one of them must be.
func decodeWriteBody(r *http.Request, dst any, set func() (value, values bool)) error {
	if err := decodeJSONBody(r, dst); err != nil {
		return requestError{err}
	}
	if value, values := set(); value == values {
		return requestError{ErrValueXorValues}
	}
	return nil
}

// handleWrite parses device and address from the path and answers with the result of write.
func (app *App) handleWrite(write func(device string, register uint16, r *http.Request) (modbusclient.WriteResponse, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if app.modbusClient == nil {
			web.Encode(w, http.StatusServiceUnavailable, ModbusErrorResponse{Error: "modbus service is not initialized"})
			return
		}

		device, register, ok := parseDeviceRegisterPath(w, r)
		if !ok {
			return
		}

		resp, err := write(device, register, r)
		if err != nil {
			status := statusForModbusError(err)
			if errors.As(err, new(requestError)) {
				status = http.StatusBadRequest
			}
			web.Encode(w, status, ModbusErrorResponse{Error: err.Error()})
			return
		}

		web.Encode(w, http.StatusOK, resp)
	})
}

func parseDeviceRegisterPath(w http.ResponseWriter, r *http.Request) (string, uint16, bool) {
	device := r.PathValue("device")
	if device == "" {
		web.Encode(w, http.StatusBadRequest, ModbusErrorResponse{Error: "missing device path parameter"})
		return "", 0, false
	}

	addressValue := r.PathValue("address")
	register, err := parseUint16(addressValue, "address")
	if err != nil {
		web.Encode(w, http.StatusBadRequest, ModbusErrorResponse{Error: err.Error()})
		return "", 0, false
	}

	return device, register, true
}

func decodeJSONBody(r *http.Request, dst any) error {
	if r.Body == nil {
		return ErrMissingRequestBody
	}

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return ErrMissingRequestBody
		}
		return fmt.Errorf("invalid request body: %w", err)
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return ErrRequestBodyNotSingle
		}
		return fmt.Errorf("invalid request body: %w", err)
	}

	return nil
}

func isValidationError(err error) bool {
	return errors.Is(err, modbusmanager.ErrLengthMustBeGreaterThanZero) ||
		errors.Is(err, modbusmanager.ErrLengthExceedsBitLimit) ||
		errors.Is(err, modbusmanager.ErrLengthExceedsRegisterLimit) ||
		errors.Is(err, modbusmanager.ErrValuesEmpty) ||
		errors.Is(err, modbusmanager.ErrValuesExceedCoilLimit) ||
		errors.Is(err, modbusmanager.ErrValuesExceedRegisterLimit)
}

// statusForModbusError maps a manager error to the HTTP status of the response.
func statusForModbusError(err error) int {
	switch {
	case isValidationError(err):
		return http.StatusBadRequest
	case errors.Is(err, modbusmanager.ErrDeviceNotConfigured):
		return http.StatusNotFound
	case errors.Is(err, modbusmanager.ErrFunctionNotAllowed):
		return http.StatusForbidden
	case errors.Is(err, modbusmanager.ErrBusBusy):
		return http.StatusServiceUnavailable
	case errors.Is(err, modbusmanager.ErrQueueTimeout):
		return http.StatusGatewayTimeout
	default:
		return http.StatusBadGateway
	}
}
