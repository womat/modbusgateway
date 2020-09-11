package global

import (
	"log"
	"sync"

	"modbusgateway/pkg/mbclient"
)

// VERSION holds the version information with the following logic in mind
//  1 ... fixed
//  0 ... year 2020, 1->year 2021, etc.
//  7 ... month of year (7=July)
//  the date format after the + is always the first of the month
//
// VERSION differs from semantic versioning as described in https://semver.org/
// but we keep the correct syntax.
const VERSION = "1.0.10+20200911"

type ModbusClient struct {
	Connection string
	Permitted  []string
}

type ConfigOptions struct {
	sync.Mutex
	Version   bool
	Webserver struct {
		Active bool
		Port   string
	}
	Webservices struct {
		Version              bool
		ReadHoldingRegisters bool
	}
	Debug struct {
		Active bool
		Path   string
	}

	ModbusClient ModbusClient

	// ModeOfOperation allows more detailed customization of how the collector should behave
	// e.g. -quiet -> do not print any error messages
	IsQuiet bool `json:"IsQuiet,omitempty" yaml:"IsQuiet,omitempty"`
}

// Options holds all command line options.
var Options ConfigOptions

// Clientd is the Modbus Client handler and controls requests (get, stop)
var Clientd *mbclient.Client

// IsError checks if e is an error and prints it.
// If X.IsQuiet is set -> the error is not printed. This is useful in daemon mode.
var IsError = func(e error) bool {
	if e != nil {
		var b bool
		Options.Lock()
		b = Options.IsQuiet
		Options.Unlock()
		if b {
			return true
		}
		log.Println(e)
		return true
	}
	return false
}
