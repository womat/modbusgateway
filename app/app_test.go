package app

import (
	"errors"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// lifecycleConfig is a config with the HTTPS API on webPort and a Modbus TCP listener on
// modbusPort, for a device on a serial port that does not exist: opening it fails, which is only
// logged.
func lifecycleConfig(t *testing.T, webPort, modbusPort int) *Config {
	t.Helper()
	cfg, err := LoadConfig(writeConfig(t, `
webserver:
  apiKey: 0123456789abcdef
buses:
  serial:
    type: rtu
    timeout: 100ms
    rtu: { port: /dev/does-not-exist }
devices:
  meter: { bus: serial, unitId: 1, gateway: { unitId: 11 } }
listen:
  tcp: { host: 127.0.0.1, port: `+strconv.Itoa(modbusPort)+` }
`))
	if err == nil {
		err = cfg.Validate()
	}
	if err != nil {
		t.Fatal(err)
	}
	cfg.Webserver.ListenHost = "127.0.0.1"
	cfg.Webserver.ListenPort = webPort
	cfg.Webserver.CertFile = "/does-not-exist/cert.pem" // env dev: embedded certificate
	return cfg
}

func TestSIGHUPWithBrokenConfigKeepsRunning(t *testing.T) {
	signals := make(chan os.Signal, 1)
	var broken atomic.Bool
	broken.Store(true)
	checkReload := func() error {
		if broken.Load() {
			return errors.New("broken config")
		}
		return nil
	}

	a, err := New(lifecycleConfig(t, freePort(t), freePort(t)), signals, checkReload).Run()
	if err != nil {
		t.Fatal(err)
	}

	signals <- syscall.SIGHUP
	select {
	case <-a.Restart():
		t.Fatal("restart with a broken config file")
	case <-time.After(200 * time.Millisecond):
	}

	broken.Store(false)
	signals <- syscall.SIGHUP
	select {
	case <-a.Restart():
	case <-time.After(5 * time.Second):
		t.Fatal("no restart after SIGHUP with a valid config file")
	}
}

// A Run that fails must release what it opened, so the previous configuration can start on the
// same ports again.
func TestFailedRunReleasesItsPorts(t *testing.T) {
	webPort, modbusPort := freePort(t), freePort(t)
	busy, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(webPort))
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()

	// The Modbus listener starts, then the web server fails on the busy port.
	if _, err = New(lifecycleConfig(t, webPort, modbusPort), make(chan os.Signal, 1), nil).Run(); err == nil {
		t.Fatal("Run succeeded on a busy web port")
	}

	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(modbusPort))
	if err != nil {
		t.Fatalf("the Modbus port is still in use after the failed Run: %v", err)
	}
	_ = l.Close()
}

func TestSIGTERMStops(t *testing.T) {
	signals := make(chan os.Signal, 1)
	a, err := New(lifecycleConfig(t, freePort(t), freePort(t)), signals, nil).Run()
	if err != nil {
		t.Fatal(err)
	}

	signals <- syscall.SIGTERM
	select {
	case <-a.Shutdown():
	case <-time.After(5 * time.Second):
		t.Fatal("no shutdown after SIGTERM")
	}
}
