package webservice

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"modbusgateway/global"
	"modbusgateway/pkg/mbclient"
)

type data struct {
	Address, Quantity uint16
	Data              string
}

type result struct {
	Time       time.Time
	Duration   int
	Connection string
	Data       data
}

func InitWebService() (err error) {
	if active := global.Options.Webserver.Active; !active {
		return nil
	}

	port := ":" + global.Options.Webserver.Port

	if version := global.Options.Webservices.Version; version {
		http.HandleFunc("/version", httpGetVersion)
	}
	if version := global.Options.Webservices.PresetMultipleRegisters; version {
		http.HandleFunc("/PresetMultipleRegisters", httpReadHoldingRegisters)
	}
	go http.ListenAndServe(port, nil)
	return
}

// httpGetVersion print the SW Version
func httpGetVersion(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write([]byte(global.VERSION)); err != nil {
		log.Println("Error: ", err)
		return
	}
}

// httpReadHoldingRegisters supplies the data of the specified Modbus register
// e.g.: http://localhost:8080/PresetMultipleRegisters?Register=4096&Quantity=1&Connection=TCP%20192.168.65.197:502%20DeviceId:2%20Timeout:50
func httpReadHoldingRegisters(w http.ResponseWriter, r *http.Request) {
	var ok bool
	var address, quantity int
	var connection string
	//	var b []byte
	var j []byte
	var err error
	//	start := time.Now()

	getUrlQuery := func(s string) (i int, err error) {
		var urlParam []string

		if urlParam, ok = r.URL.Query()[s]; !ok || len(urlParam[0]) < 1 {
			err = fmt.Errorf("url param '%v' is missing", s)
			return
		}

		if i, err = strconv.Atoi(urlParam[0]); err != nil {
			err = fmt.Errorf("Value does'nt look like a number (%q).\n", urlParam[0])
			return
		}
		return
	}

	if address, err = getUrlQuery("Address"); err != nil {
		if address, err = getUrlQuery("Register"); err != nil {
			log.Printf("Url Param Register resp. Address is missing or wrong: %v\n", err)
			return
		}
		address -= 1
	}
	if quantity, err = getUrlQuery("Quantity"); err != nil {
		log.Printf("Url Param Quantity is missing or wrong: %v\n", err)
		return
	}
	if urlParam, ok := r.URL.Query()["Connection"]; ok {
		connection = urlParam[0]
	} else {
		connection = global.Options.ModbusClient.Connection
	}

	isValidPattern := func(pattern []string, test string) (b bool) {
		for _, s := range global.Options.ModbusClient.Permitted {
			if regexp.MustCompile(s).MatchString(connection) {
				return true
			}
		}
		return false
	}

	if global.Options.ModbusClient.Connection != connection && !isValidPattern(global.Options.ModbusClient.Permitted, connection) {
		log.Printf("Connection is denied: Connection '%v' doesn't match with permitted pattern", connection)
		return
	}

	log.Printf("ReadHoldingRegisters from %v, quantity %v (%v)", address, quantity, connection)

	var d mbclient.ClientData

	request := mbclient.Request{
		Connection: connection,
		Address:    uint16(address),
		Quantity:   uint16(quantity),
		Data:       make(chan mbclient.ClientData, 0),
	}

	getTimeOut := func(s string) (d time.Duration) {
		d = time.Second
		for _, field := range strings.Fields(s) {
			parts := strings.Split(field, ":")

			if parts[0] == "Timeout" && len(parts) == 2 {
				if i, err := strconv.Atoi(parts[1]); err == nil {
					d = time.Duration(i) * time.Millisecond
					return
				}
			}
		}
		return
	}

	//so that the timeout doesn't precede from the timeout of the request, 100ms are added to the timeout
	timeOut := time.NewTimer(getTimeOut(connection) + time.Second)
	defer timeOut.Stop()

	global.Clientd.Get <- request

	// wait for Modbus Data and check Timeout
	select {
	case d = <-request.Data:
		if d.Error != nil {
			log.Printf("Error to receive Modbus Client Data: %v", d.Error)
			return
		}
	case <-timeOut.C:
		log.Printf("Error to receive Modbus Client Data: Timeout")
		return
	}

	result := result{
		Time:       d.Timestamp,
		Duration:   int(d.Runtime / time.Millisecond),
		Connection: connection,
		Data: data{
			Address:  uint16(address),
			Quantity: uint16(quantity),
			Data:     fmt.Sprintf("%X", d.Data),
		},
	}

	if j, err = json.Marshal(result); err != nil {
		log.Println("Error: ", err)
		return
	}
	log.Printf("Data %v", result.Data.Data)

	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(j); err != nil {
		log.Println("Error: ", err)
		return
	}
}
