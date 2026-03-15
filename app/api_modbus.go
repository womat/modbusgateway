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
)

type ModbusErrorResponse struct {
	Error string `json:"error"`
}

type ModbusUint16 uint16

type ModbusWriteSingleCoilRequest struct {
	Value *bool `json:"value"`
}

type ModbusWriteSingleRegisterRequest struct {
	Value *uint16 `json:"value"`
}

type ModbusWriteMultipleCoilsRequest struct {
	Address *ModbusUint16 `json:"address"`
	Values  []bool        `json:"values"`
}

type ModbusWriteMultipleRegistersRequest struct {
	Address *ModbusUint16 `json:"address"`
	Values  []uint16      `json:"values"`
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
//	@Failure		401			{string}	string								"Unauthorized"
//	@Router			/devices/{device}/input-registers/{address} [get]
func (app *App) HandleModbusReadInputRegisters() http.Handler {
	return app.handleReadRegisters(func(req modbusclient.ReadRegistersRequest) (modbusclient.ReadRegistersResponse, error) {
		return app.modbusClient.ReadInputRegisters(req)
	})
}

// HandleModbusWriteSingleCoil writes a single coil on a configured device.
//
//	@Summary		FC 5: Write single coil
//	@Description	Writes a single coil on a configured Modbus device.
//	@Tags			modbus
//	@Accept			json
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			device	path		string							true	"Configured device name"
//	@Param			address	path		string							true	"Target address (decimal or 0x-prefixed hex)"
//	@Param			request	body		ModbusWriteSingleCoilRequest	true	"Single coil write request"
//	@Success		200		{object}	modbusclient.WriteResponse		"Coil successfully written"
//	@Failure		400		{object}	ModbusErrorResponse				"Invalid request"
//	@Failure		502		{object}	ModbusErrorResponse				"Modbus write failed"
//	@Failure		401		{string}	string							"Unauthorized"
//	@Router			/devices/{device}/coils/{address} [post]
func (app *App) HandleModbusWriteSingleCoil() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if app.modbusClient == nil {
			web.Encode(w, http.StatusServiceUnavailable, ModbusErrorResponse{Error: "modbus service is not initialized"})
			return
		}

		device, register, ok := parseDeviceRegisterPath(w, r)
		if !ok {
			return
		}

		var body ModbusWriteSingleCoilRequest
		if err := decodeJSONBody(r, &body); err != nil {
			web.Encode(w, http.StatusBadRequest, ModbusErrorResponse{Error: err.Error()})
			return
		}
		if body.Value == nil {
			web.Encode(w, http.StatusBadRequest, ModbusErrorResponse{Error: "missing value field"})
			return
		}

		resp, err := app.modbusClient.WriteSingleCoil(modbusclient.WriteSingleCoilRequest{
			Device:   device,
			Register: register,
			Value:    *body.Value,
		})
		if err != nil {
			web.Encode(w, statusForModbusError(err), ModbusErrorResponse{Error: err.Error()})
			return
		}

		web.Encode(w, http.StatusOK, resp)
	})
}

// HandleModbusWriteSingleRegister writes a single holding register on a configured device.
//
//	@Summary		FC 6: Write single holding register
//	@Description	Writes a single holding register on a configured Modbus device.
//	@Tags			modbus
//	@Accept			json
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			device	path		string								true	"Configured device name"
//	@Param			address	path		string								true	"Target address (decimal or 0x-prefixed hex)"
//	@Param			request	body		ModbusWriteSingleRegisterRequest	true	"Single register write request"
//	@Success		200		{object}	modbusclient.WriteResponse			"Register successfully written"
//	@Failure		400		{object}	ModbusErrorResponse					"Invalid request"
//	@Failure		502		{object}	ModbusErrorResponse					"Modbus write failed"
//	@Failure		401		{string}	string								"Unauthorized"
//	@Router			/devices/{device}/holding-registers/{address} [post]
func (app *App) HandleModbusWriteSingleRegister() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if app.modbusClient == nil {
			web.Encode(w, http.StatusServiceUnavailable, ModbusErrorResponse{Error: "modbus service is not initialized"})
			return
		}

		device, register, ok := parseDeviceRegisterPath(w, r)
		if !ok {
			return
		}

		var body ModbusWriteSingleRegisterRequest
		if err := decodeJSONBody(r, &body); err != nil {
			web.Encode(w, http.StatusBadRequest, ModbusErrorResponse{Error: err.Error()})
			return
		}
		if body.Value == nil {
			web.Encode(w, http.StatusBadRequest, ModbusErrorResponse{Error: "missing value field"})
			return
		}

		resp, err := app.modbusClient.WriteSingleRegister(modbusclient.WriteSingleRegisterRequest{
			Device:   device,
			Register: register,
			Value:    *body.Value,
		})
		if err != nil {
			web.Encode(w, statusForModbusError(err), ModbusErrorResponse{Error: err.Error()})
			return
		}

		web.Encode(w, http.StatusOK, resp)
	})
}

// HandleModbusWriteMultipleCoils writes multiple coils on a configured device.
//
//	@Summary		FC 15: Write multiple coils
//	@Description	Writes multiple coils on a configured Modbus device.
//	@Tags			modbus
//	@Accept			json
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			device	path		string							true	"Configured device name"
//	@Param			request	body		ModbusWriteMultipleCoilsRequest	true	"Multiple coils write request"
//	@Success		200		{object}	modbusclient.WriteResponse		"Coils successfully written"
//	@Failure		400		{object}	ModbusErrorResponse				"Invalid request"
//	@Failure		502		{object}	ModbusErrorResponse				"Modbus write failed"
//	@Failure		401		{string}	string							"Unauthorized"
//	@Router			/devices/{device}/coils [post]
func (app *App) HandleModbusWriteMultipleCoils() http.Handler {
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

		var body ModbusWriteMultipleCoilsRequest
		if err := decodeJSONBody(r, &body); err != nil {
			web.Encode(w, http.StatusBadRequest, ModbusErrorResponse{Error: err.Error()})
			return
		}
		if body.Address == nil {
			web.Encode(w, http.StatusBadRequest, ModbusErrorResponse{Error: "missing address field"})
			return
		}

		resp, err := app.modbusClient.WriteMultipleCoils(modbusclient.WriteMultipleCoilsRequest{
			Device:   device,
			Register: uint16(*body.Address),
			Values:   body.Values,
		})
		if err != nil {
			web.Encode(w, statusForModbusError(err), ModbusErrorResponse{Error: err.Error()})
			return
		}

		web.Encode(w, http.StatusOK, resp)
	})
}

// HandleModbusWriteMultipleRegisters writes multiple holding registers on a configured device.
//
//	@Summary		FC 16: Write multiple holding registers
//	@Description	Writes multiple holding registers on a configured Modbus device.
//	@Tags			modbus
//	@Accept			json
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			device	path		string								true	"Configured device name"
//	@Param			request	body		ModbusWriteMultipleRegistersRequest	true	"Multiple registers write request"
//	@Success		200		{object}	modbusclient.WriteResponse			"Registers successfully written"
//	@Failure		400		{object}	ModbusErrorResponse					"Invalid request"
//	@Failure		502		{object}	ModbusErrorResponse					"Modbus write failed"
//	@Failure		401		{string}	string								"Unauthorized"
//	@Router			/devices/{device}/holding-registers [post]
func (app *App) HandleModbusWriteMultipleRegisters() http.Handler {
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

		var body ModbusWriteMultipleRegistersRequest
		if err := decodeJSONBody(r, &body); err != nil {
			web.Encode(w, http.StatusBadRequest, ModbusErrorResponse{Error: err.Error()})
			return
		}
		if body.Address == nil {
			web.Encode(w, http.StatusBadRequest, ModbusErrorResponse{Error: "missing address field"})
			return
		}

		resp, err := app.modbusClient.WriteMultipleRegisters(modbusclient.WriteMultipleRegistersRequest{
			Device:   device,
			Register: uint16(*body.Address),
			Values:   body.Values,
		})
		if err != nil {
			web.Encode(w, statusForModbusError(err), ModbusErrorResponse{Error: err.Error()})
			return
		}

		web.Encode(w, http.StatusOK, resp)
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

func (v *ModbusUint16) UnmarshalJSON(data []byte) error {
	var number uint16
	if err := json.Unmarshal(data, &number); err == nil {
		*v = ModbusUint16(number)
		return nil
	}

	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("address must be a number or string: %w", err)
	}

	parsed, err := parseModbusUint16(text)
	if err != nil {
		return fmt.Errorf("invalid address: %w", err)
	}

	*v = ModbusUint16(parsed)
	return nil
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

func statusForModbusError(err error) int {
	if isValidationError(err) {
		return http.StatusBadRequest
	}

	return http.StatusBadGateway
}
