//go:build linux

package modbusserver

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	simonmodbus "github.com/simonvetter/modbus"
	"github.com/womat/modbusgateway/pkg/modbusmanager"
)

// TestRTUListener: a client on a serial bus reaches a TCP device through the gateway. The bus
// is a pair of virtual serial ports created by socat; the test is skipped without socat.
func TestRTUListener(t *testing.T) {
	if _, err := exec.LookPath("socat"); err != nil {
		t.Skip("socat not installed")
	}
	dir := t.TempDir()
	gatewayPort, clientPort := filepath.Join(dir, "ttyGW"), filepath.Join(dir, "ttyCLIENT")
	cmd := exec.Command("socat", "pty,raw,echo=0,link="+gatewayPort, "pty,raw,echo=0,link="+clientPort)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, errGW := os.Stat(gatewayPort)
		_, errClient := os.Stat(clientPort)
		if errGW == nil && errClient == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socat did not create the virtual serial ports")
		}
		time.Sleep(10 * time.Millisecond)
	}

	device, devicePort := downstream(t)
	_ = device.SetHoldingRegisters(1, 100, []uint16{42, 43})

	m := modbusmanager.New()
	t.Cleanup(func() { _ = m.Close() })
	if err := m.RegisterBus(modbusmanager.BusConfig{
		Name: "lan", Type: "tcp", Timeout: 500 * time.Millisecond,
		TCP: &modbusmanager.TCPConfig{Host: "127.0.0.1", Port: devicePort},
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.Register(modbusmanager.DeviceConfig{Name: "pv", Bus: "lan", UnitId: 1, Functions: modbusmanager.SupportedFunctions}); err != nil {
		t.Fatal(err)
	}

	server := New(Listener{RTU: &RTUListener{Port: gatewayPort, BaudRate: 115200, DataBits: 8, Parity: "N", StopBits: 1}},
		m, map[uint8]string{31: "pv"})
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	client, err := simonmodbus.NewClient(&simonmodbus.ClientConfiguration{
		URL: "rtu://" + clientPort, Speed: 115200, DataBits: 8, Parity: simonmodbus.PARITY_NONE, StopBits: 1,
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	_ = client.SetUnitId(31)
	values, err := client.ReadRegisters(100, 2, simonmodbus.HOLDING_REGISTER)
	if err != nil || !slices.Equal(values, []uint16{42, 43}) {
		t.Fatalf("FC3 over RTU = %v, %v; want [42 43]", values, err)
	}
	if err = client.WriteRegister(100, 7); err != nil {
		t.Fatalf("FC6 over RTU: %v", err)
	}
	if got, _ := device.HoldingRegisters(1, 100, 1); got[0] != 7 {
		t.Errorf("device register 100 = %d after the write, want 7", got[0])
	}

	// Another server on the bus may own an unknown unit ID: the gateway stays silent.
	_ = client.SetUnitId(9)
	if _, err = client.ReadRegisters(100, 1, simonmodbus.HOLDING_REGISTER); !errors.Is(err, simonmodbus.ErrRequestTimedOut) {
		t.Errorf("unknown unit id over RTU: err = %v, want a timeout (no answer)", err)
	}
}
