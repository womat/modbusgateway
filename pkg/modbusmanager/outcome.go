package modbusmanager

import (
	"errors"

	simonmodbus "github.com/simonvetter/modbus"
)

// Class groups the outcomes of a request.
type Class string

const (
	ClassOK        Class = "ok"        // answered over the bus
	ClassCache     Class = "cache"     // answered from the cache, without a bus transaction
	ClassException Class = "exception" // the device answered with an exception
	ClassTimeout   Class = "timeout"   // the device did not answer in time
	ClassError     Class = "error"     // the bus failed, e.g. the connection could not be opened
	ClassRejected  Class = "rejected"  // the gateway refused the request before the bus
)

// OnBus reports whether a request of this class was executed on the bus.
func (c Class) OnBus() bool {
	return c == ClassOK || c == ClassException || c == ClassTimeout || c == ClassError
}

// exceptionNames are the device exceptions, named as in the Modbus specification.
var exceptionNames = []struct {
	err  error
	name string
}{
	{simonmodbus.ErrIllegalFunction, "IllegalFunction"},
	{simonmodbus.ErrIllegalDataAddress, "IllegalDataAddress"},
	{simonmodbus.ErrIllegalDataValue, "IllegalDataValue"},
	{simonmodbus.ErrServerDeviceFailure, "ServerDeviceFailure"},
	{simonmodbus.ErrAcknowledge, "Acknowledge"},
	{simonmodbus.ErrServerDeviceBusy, "ServerDeviceBusy"},
	{simonmodbus.ErrMemoryParityError, "MemoryParityError"},
	{simonmodbus.ErrGWPathUnavailable, "GatewayPathUnavailable"},
	{simonmodbus.ErrGWTargetFailedToRespond, "GatewayTargetDeviceFailedToRespond"},
}

// IsTimeout reports whether err means the device did not answer in time.
func IsTimeout(err error) bool {
	return errors.Is(err, simonmodbus.ErrRequestTimedOut)
}

// IsValidationError reports whether err rejects the request itself: a malformed request, or a
// quantity or a number of values outside the Modbus limits.
func IsValidationError(err error) bool {
	return errors.Is(err, ErrInvalidRequest) ||
		errors.Is(err, ErrLengthMustBeGreaterThanZero) ||
		errors.Is(err, ErrLengthExceedsBitLimit) ||
		errors.Is(err, ErrLengthExceedsRegisterLimit) ||
		errors.Is(err, ErrValuesEmpty) ||
		errors.Is(err, ErrValuesExceedCoilLimit) ||
		errors.Is(err, ErrValuesExceedRegisterLimit)
}

// Outcome names the result of a request for a log or a protocol - "ok", "cache", the exception
// of the device, "timeout", "forbidden", ... - and its class.
func Outcome(err error, cached bool) (string, Class) {
	switch {
	case err == nil && cached:
		return "cache", ClassCache
	case err == nil:
		return "ok", ClassOK
	case errors.Is(err, ErrFunctionNotAllowed):
		return "forbidden", ClassRejected
	case IsValidationError(err):
		return "invalid", ClassRejected
	case errors.Is(err, ErrDeviceNotConfigured):
		return "unknown device", ClassRejected
	case errors.Is(err, ErrBusBusy):
		return "busy", ClassRejected
	case errors.Is(err, ErrQueueTimeout):
		return "queue timeout", ClassRejected
	case errors.Is(err, ErrManagerClosed):
		return "closed", ClassRejected
	case IsTimeout(err):
		return "timeout", ClassTimeout
	}
	for _, e := range exceptionNames {
		if errors.Is(err, e.err) {
			return e.name, ClassException
		}
	}
	return "error", ClassError
}
