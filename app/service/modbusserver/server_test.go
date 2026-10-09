package modbusserver

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"testing"
	"time"

	simonmodbus "github.com/simonvetter/modbus"
	"github.com/womat/mbserver"
	"github.com/womat/modbusgateway/pkg/modbusmanager"
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

// downstream starts a Modbus TCP device with register memory for unit ID 1.
func downstream(t *testing.T) (*mbserver.Server, int) {
	t.Helper()
	device := mbserver.NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := device.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(device.Close)
	port := freePort(t)
	if err := device.ListenTCP(context.Background(), "127.0.0.1:"+strconv.Itoa(port)); err != nil {
		t.Fatal(err)
	}
	return device, port
}

// gateway starts the manager with one TCP bus to devicePort and a TCP listener that offers
// "rw" (all function codes) as unit 11 and "ro" (FC3 only) as unit 12.
func gateway(t *testing.T, devicePort int) *simonmodbus.ModbusClient {
	t.Helper()
	m := modbusmanager.New()
	t.Cleanup(func() { _ = m.Close() })
	// Two buses to the same device: on one bus a unit id belongs to one device, and the test
	// needs a read-write and a read-only view of it.
	for _, bus := range []string{"rw-bus", "ro-bus"} {
		if err := m.RegisterBus(modbusmanager.BusConfig{
			Name: bus, Type: "tcp", Timeout: 500 * time.Millisecond,
			TCP: &modbusmanager.TCPConfig{Host: "127.0.0.1", Port: devicePort},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []modbusmanager.DeviceConfig{
		{Name: "rw", Bus: "rw-bus", UnitId: 1, Functions: modbusmanager.SupportedFunctions},
		{Name: "ro", Bus: "ro-bus", UnitId: 1, Functions: []uint8{3}},
	} {
		if err := m.Register(d); err != nil {
			t.Fatal(err)
		}
	}

	port := freePort(t)
	server := New(Listener{TCP: &TCPListener{Host: "127.0.0.1", Port: port}}, m, map[uint8]string{11: "rw", 12: "ro"})
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	client, err := simonmodbus.NewClient(&simonmodbus.ClientConfiguration{
		URL: "tcp://127.0.0.1:" + strconv.Itoa(port), Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestGatewayReads(t *testing.T) {
	device, port := downstream(t)
	if err := device.SetHoldingRegisters(1, 4096, []uint16{0x0003, 0x8A40, 0x1234}); err != nil {
		t.Fatal(err)
	}
	client := gateway(t, port)
	_ = client.SetUnitId(11)

	values, err := client.ReadRegisters(4096, 3, simonmodbus.HOLDING_REGISTER)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(values, []uint16{0x0003, 0x8A40, 0x1234}) {
		t.Errorf("FC3 through the gateway = %#v, want the registers of the device", values)
	}

	if _, err = client.ReadRegisters(0, 2, simonmodbus.INPUT_REGISTER); err != nil {
		t.Errorf("FC4: %v", err)
	}
	if _, err = client.ReadCoils(0, 10); err != nil {
		t.Errorf("FC1: %v", err)
	}
	if _, err = client.ReadDiscreteInputs(0, 10); err != nil {
		t.Errorf("FC2: %v", err)
	}
}

func TestGatewayWrites(t *testing.T) {
	device, port := downstream(t)
	client := gateway(t, port)
	_ = client.SetUnitId(11)

	if err := client.WriteRegister(10, 4711); err != nil {
		t.Fatalf("FC6: %v", err)
	}
	if err := client.WriteRegisters(20, []uint16{1, 2, 3}); err != nil {
		t.Fatalf("FC16: %v", err)
	}
	registers, err := device.HoldingRegisters(1, 10, 13)
	if err != nil {
		t.Fatal(err)
	}
	if registers[0] != 4711 || !slices.Equal(registers[10:13], []uint16{1, 2, 3}) {
		t.Errorf("device registers 10 and 20-22 = %d, %v; want 4711 and [1 2 3]", registers[0], registers[10:13])
	}

	if err = client.WriteCoil(3, true); err != nil {
		t.Fatalf("FC5: %v", err)
	}
	if err = client.WriteCoils(8, []bool{true, false, true, true, false, false, false, false, true}); err != nil {
		t.Fatalf("FC15: %v", err)
	}
	coils, err := client.ReadCoils(0, 17)
	if err != nil {
		t.Fatal(err)
	}
	want := []bool{false, false, false, true, false, false, false, false, true, false, true, true, false, false, false, false, true}
	if !slices.Equal(coils, want) {
		t.Errorf("coils after FC5/FC15 = %v, want %v", coils, want)
	}
}

func TestGatewayRefusesFunctionsOfTheDevice(t *testing.T) {
	device, port := downstream(t)
	client := gateway(t, port)
	_ = client.SetUnitId(12)

	if _, err := client.ReadRegisters(0, 1, simonmodbus.HOLDING_REGISTER); err != nil {
		t.Errorf("FC3 on the read-only device: %v", err)
	}
	if err := client.WriteRegister(0, 1); !errors.Is(err, simonmodbus.ErrIllegalFunction) {
		t.Errorf("FC6 on the read-only device: err = %v, want ErrIllegalFunction", err)
	}
	if _, err := client.ReadCoils(0, 1); !errors.Is(err, simonmodbus.ErrIllegalFunction) {
		t.Errorf("FC1 on the read-only device: err = %v, want ErrIllegalFunction", err)
	}
	if values, _ := device.HoldingRegisters(1, 0, 1); values[0] != 0 {
		t.Error("the refused write reached the device")
	}
}

func TestGatewayPassesExceptionsOfTheDevice(t *testing.T) {
	device, port := downstream(t)
	// The device knows registers 0-999 only.
	device.RegisterFunctionHandler(3, func(s *mbserver.Server, frame mbserver.Framer) ([]byte, mbserver.Exception) {
		if binary.BigEndian.Uint16(frame.GetData()) >= 1000 {
			return nil, mbserver.IllegalDataAddress
		}
		return mbserver.ReadHoldingRegisters(s, frame)
	})
	client := gateway(t, port)
	_ = client.SetUnitId(11)

	_, err := client.ReadRegisters(5000, 2, simonmodbus.HOLDING_REGISTER)
	if !errors.Is(err, simonmodbus.ErrIllegalDataAddress) {
		t.Errorf("read beyond the registers of the device: err = %v, want ErrIllegalDataAddress", err)
	}
}

func TestGatewayUnknownUnitAndBroadcast(t *testing.T) {
	_, port := downstream(t)
	client := gateway(t, port)

	_ = client.SetUnitId(99)
	if _, err := client.ReadRegisters(0, 1, simonmodbus.HOLDING_REGISTER); !errors.Is(err, simonmodbus.ErrGWTargetFailedToRespond) {
		t.Errorf("unknown unit id: err = %v, want ErrGWTargetFailedToRespond", err)
	}

	// Unit ID 0 must not be forwarded to every device: the gateway drops it.
	_ = client.SetUnitId(0)
	if err := client.WriteRegister(0, 1); !errors.Is(err, simonmodbus.ErrGWTargetFailedToRespond) {
		t.Errorf("broadcast write: err = %v, want ErrGWTargetFailedToRespond", err)
	}
}

func TestGatewayDeviceOffline(t *testing.T) {
	client := gateway(t, freePort(t)) // nothing listens there
	_ = client.SetUnitId(11)

	if _, err := client.ReadRegisters(0, 1, simonmodbus.HOLDING_REGISTER); !errors.Is(err, simonmodbus.ErrGWTargetFailedToRespond) {
		t.Errorf("device offline: err = %v, want ErrGWTargetFailedToRespond", err)
	}
}

func TestMalformedRequestData(t *testing.T) {
	h := &handler{devices: map[uint8]string{1: "x"}}
	for name, tc := range map[string]struct {
		fc   uint8
		data []byte
		call func(*mbserver.Server, mbserver.Framer) ([]byte, mbserver.Exception)
	}{
		"FC3 short":              {3, []byte{0, 1}, h.readHoldingRegisters},
		"FC5 bad value":          {5, []byte{0, 1, 0x12, 0x34}, h.writeSingleCoil},
		"FC15 byte count":        {15, []byte{0, 0, 0, 9, 1, 0xFF}, h.writeMultipleCoils},
		"FC16 payload too short": {16, []byte{0, 0, 0, 2, 4, 0, 1}, h.writeMultipleRegisters},
		"FC16 quantity 0":        {16, []byte{0, 0, 0, 0, 0}, h.writeMultipleRegisters},
	} {
		t.Run(name, func(t *testing.T) {
			frame := &mbserver.TCPFrame{UnitId: 1, Function: tc.fc}
			frame.SetData(tc.data)
			if _, ex := tc.call(nil, frame); ex != mbserver.IllegalDataValue {
				t.Errorf("exception = %v, want IllegalDataValue", ex)
			}
		})
	}
}

func TestAddressAndQuantity(t *testing.T) {
	frame := &mbserver.TCPFrame{UnitId: 1, Function: 3}
	data := make([]byte, 4)
	binary.BigEndian.PutUint16(data, 4096)
	binary.BigEndian.PutUint16(data[2:], 59)
	frame.SetData(data)
	if addr, qty, ok := addressAndQuantity(frame); !ok || addr != 4096 || qty != 59 {
		t.Errorf("addressAndQuantity = %d, %d, %v; want 4096, 59, true", addr, qty, ok)
	}
}
