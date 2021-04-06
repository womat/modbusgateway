package mbclient

import (
	"fmt"
	"io"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goburrow/modbus"
)

type ClientData struct {
	Timestamp time.Time
	Runtime   time.Duration
	Error     error
	Data      []byte
}

type Request struct {
	Connection        string
	Address, Quantity uint16
	Data              chan ClientData
}

type Client struct {
	debug     io.Writer
	debugLock sync.Mutex
	Stop      chan bool
	Get       chan Request
}

func NewClient() (c *Client) {
	c = &Client{
		Stop: make(chan bool, 1),
		Get:  make(chan Request, 1),
	}

	return
}

func (c *Client) Start() (err error) {
	go c.Clientd()
	return
}

func (c *Client) Clientd() {
	var request Request

	for {
		select {
		case <-c.Stop:
			return
		case request = <-c.Get:
		}

		start := time.Now()

		var data []byte
		var err error
		var client modbus.Client

		param := make(map[string]interface{})
		param["DeviceId"] = byte(1)
		param["Timeout"] = time.Second

		fields := strings.Fields(request.Connection)
		for _, field := range fields {
			// check the connection string and break it down into fields
			// TCP 192.168.65.197:502  DeviceId:1 Timeout:500
			// RTU com4,9600,8,N,1 DeviceId:1 Timeout:1000
			// ASCII /dev/ttyS0,9600,8,N,1 DeviceId:1 Timeout:1000
			switch {
			case field == "RTU", field == "TCP", field == "ASCII":
				param["Type"] = field
				// RTU & ASCII
			case regexp.MustCompile(`^[0-9A-Za-z:./-]*,[0-9]{1,5},[5678],[NEO],[12]$`).MatchString(field):
				param["connection"] = field
				// TCP
			case regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}:[0-9]{1,5}$`).MatchString(field):
				param["connection"] = field
			default:
				// break fields into a map, eg DeviceId:1 >> m[DeviceId]=1
				var value string

				parts := strings.Split(field, ":")
				if len(parts) == 2 {
					value = parts[1]
				}

				switch key := parts[0]; key {
				case "DeviceId":
					if i, err := strconv.Atoi(value); err == nil {
						param[key] = byte(i)
					}
				case "Timeout":
					if i, err := strconv.Atoi(value); err == nil {
						if i > 10000 {
							// max. Timeout: 10 seconds
							i = 10000
						}
						param[key] = time.Duration(i) * time.Millisecond
					}
				default:
					param[key] = value
				}
			}
		}

		if _, ok := param["connection"]; !ok {
			request.Data <- ClientData{
				Timestamp: time.Now(),
				Runtime:   time.Since(start),
				Error:     fmt.Errorf("invalid connection string parameter: '%v'", request.Connection),
			}

			close(request.Data)
			continue
		}

		// a function ensures that all channels and timers are closed after the ending
		// important: defer are only obtained at the end of a function and not after the end of a loop!
		func() {
			// check Timeouts
			ch := make(chan bool, 1)
			defer close(ch)

			// so that the timeout doesn't precede from the timeout of the client handler, 100ms are added to the timeout
			timeOut := time.NewTimer(param["Timeout"].(time.Duration) + time.Millisecond*100)
			defer timeOut.Stop()

			go func() {
				// fills the "data" variable with client data
				// or variable "err" with error information
				defer func() {
					// ensures that data is sent to the channel when the function is terminated
					defer func() {
						// recover from panic caused by writing to a closed channel
						if r := recover(); r != nil {
							log.Printf("Error write to closed channel: %v", r)
							return
						}
					}()

					ch <- true
				}()

				switch param["Type"].(string) {
				case "RTU":
					serial := strings.Split(param["connection"].(string), ",")
					clientHandler := modbus.NewRTUClientHandler(serial[0])
					clientHandler.Timeout = param["Timeout"].(time.Duration)
					clientHandler.SlaveId = param["DeviceId"].(byte)
					clientHandler.BaudRate, _ = strconv.Atoi(serial[1])
					clientHandler.DataBits, _ = strconv.Atoi(serial[2])
					clientHandler.Parity = serial[3]
					clientHandler.StopBits, _ = strconv.Atoi(serial[4])

					if err = clientHandler.Connect(); err != nil {
						return
					}

					defer clientHandler.Close()
					client = modbus.NewClient(clientHandler)

				case "TCP":
					clientHandler := modbus.NewTCPClientHandler(param["connection"].(string))
					clientHandler.Timeout = param["Timeout"].(time.Duration)
					clientHandler.SlaveId = param["DeviceId"].(byte)

					if err = clientHandler.Connect(); err != nil {
						return
					}

					defer clientHandler.Close()
					client = modbus.NewClient(clientHandler)

				case "ASCII":
					serial := strings.Split(param["connection"].(string), ",")
					clientHandler := modbus.NewASCIIClientHandler(serial[0])
					clientHandler.Timeout = param["Timeout"].(time.Duration)
					clientHandler.SlaveId = param["DeviceId"].(byte)
					clientHandler.BaudRate, _ = strconv.Atoi(serial[1])
					clientHandler.DataBits, _ = strconv.Atoi(serial[2])
					clientHandler.Parity = serial[3]
					clientHandler.StopBits, _ = strconv.Atoi(serial[4])

					if err = clientHandler.Connect(); err != nil {
						return
					}

					defer clientHandler.Close()
					client = modbus.NewClient(clientHandler)

				default:
					err = fmt.Errorf("modbus type '%v' ist not supported", param["Type"].(string))
					return
				}

				data, err = client.ReadHoldingRegisters(request.Address, request.Quantity)
			}()

			// wait for Modbus Data
			select {
			case <-ch:
			case <-timeOut.C:
				err = fmt.Errorf("timeout")
			}
		}()

		// TODO repeat on error
		select {
		case request.Data <- ClientData{
			Timestamp: time.Now(),
			Runtime:   time.Since(start),
			Error:     err,
			Data:      data,
		}:

		default:
			log.Printf("Error write to closed channel: request.Data <- ClientData")
		}

		close(request.Data)
	}
}

func (c *Client) Close() (err error) {
	c.Stop <- true

	close(c.Stop)
	return
}

func (c *Client) SetDebug(w io.Writer) {
	c.debugLock.Lock()
	defer c.debugLock.Unlock()

	c.debug = w
}
