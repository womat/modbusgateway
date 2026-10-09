package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/womat/mbserver"
)

const testApiKey = "0123456789abcdef"

// newTestApp starts a Modbus TCP device and returns an App with the devices "meter" (unit 1,
// reading only, values cached) and "pump" (unit 2, writes with FC5, FC6 and FC15 but not FC16)
// on it, and its HTTP handler.
func newTestApp(t *testing.T) (*mbserver.Server, http.Handler) {
	t.Helper()
	device := mbserver.NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := device.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(device.Close)
	if err := device.NewUnit(2); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	if err = device.ListenTCP(context.Background(), "127.0.0.1:"+strconv.Itoa(port)); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(writeConfig(t, `
webserver:
  apiKey: `+testApiKey+`
buses:
  lan:
    type: tcp
    timeout: 1s
    tcp: { host: 127.0.0.1, port: `+strconv.Itoa(port)+` }
devices:
  meter: { bus: lan, unitId: 1, cacheTTL: 1m }
  pump: { bus: lan, unitId: 2, cacheTTL: 1m, functions: [FC1, FC3, FC5, FC6, FC15] }
`))
	if err != nil {
		t.Fatal(err)
	}
	if err = cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	a := New(cfg, nil, nil)
	if err = a.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Cleanup() })
	return device, a.web.Handler
}

func request(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Api-Key", testApiKey)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

func TestRESTReadIsCached(t *testing.T) {
	device, h := newTestApp(t)
	_ = device.SetHoldingRegisters(1, 4096, []uint16{0x0003, 0x8A40})

	code, first := request(t, h, http.MethodGet, "/devices/meter/holding-registers/4096?quantity=2", "")
	if code != http.StatusOK || first["dataHex"] != "00038A40" || first["cached"] != false {
		t.Fatalf("first read: %d %v, want 200 with dataHex 00038A40 from the bus", code, first)
	}
	if values, _ := first["values"].([]any); !slices.Equal(values, []any{float64(3), float64(0x8A40)}) {
		t.Errorf("first read: values = %v, want [3 35392]", first["values"])
	}
	_, second := request(t, h, http.MethodGet, "/devices/meter/holding-registers/4096?quantity=2", "")
	if second["cached"] != true {
		t.Errorf("second read: %v, want it from the cache", second)
	}
}

func TestRESTStatusCodes(t *testing.T) {
	_, h := newTestApp(t)

	if code, resp := request(t, h, http.MethodPut, "/devices/meter/holding-registers/0", `{"value":1}`); code != http.StatusForbidden {
		t.Errorf("write to a read-only device: %d %v, want 403", code, resp)
	}
	if code, resp := request(t, h, http.MethodGet, "/devices/nope/holding-registers/0", ""); code != http.StatusNotFound {
		t.Errorf("unknown device: %d %v, want 404", code, resp)
	}
}

func TestRESTStatus(t *testing.T) {
	_, h := newTestApp(t)
	request(t, h, http.MethodGet, "/devices/meter/holding-registers/0", "")

	code, status := request(t, h, http.MethodGet, "/devices/meter/status", "")
	if code != http.StatusOK || status["bus"] != "lan" || status["unitId"] != float64(1) || status["cacheMisses"] != float64(1) {
		t.Fatalf("status: %d %v", code, status)
	}
	var functions []string
	for _, fc := range status["functions"].([]any) {
		functions = append(functions, fc.(string))
	}
	if !slices.Equal(functions, []string{"FC1", "FC2", "FC3", "FC4"}) {
		t.Errorf("functions = %v, want the read-only default", functions)
	}
}

// TestRESTWriteSelectsFunctionCode: "value" writes with FC5/FC6, "values" with FC15/FC16 — the
// pump allows FC6 but not FC16, so the same register write succeeds or is refused by body shape.
func TestRESTWriteSelectsFunctionCode(t *testing.T) {
	device, h := newTestApp(t)

	if code, resp := request(t, h, http.MethodPut, "/devices/pump/holding-registers/0x10", `{"value":7}`); code != http.StatusOK || resp["functionCode"] != float64(6) {
		t.Fatalf("single register write: %d %v, want 200 with FC6", code, resp)
	}
	if got, _ := device.HoldingRegisters(2, 16, 1); got[0] != 7 {
		t.Errorf("register 16 = %v after the write, want 7", got)
	}
	if code, resp := request(t, h, http.MethodPut, "/devices/pump/holding-registers/16", `{"values":[7]}`); code != http.StatusForbidden {
		t.Errorf("values on a device without FC16: %d %v, want 403", code, resp)
	}

	// Coils: read once to fill the cache; the write must invalidate it.
	request(t, h, http.MethodGet, "/devices/pump/coils/0?quantity=3", "")
	if code, resp := request(t, h, http.MethodPut, "/devices/pump/coils/0", `{"values":[true,false,true]}`); code != http.StatusOK || resp["functionCode"] != float64(15) || resp["quantity"] != float64(3) {
		t.Fatalf("multiple coil write: %d %v, want 200 with FC15 and quantity 3", code, resp)
	}
	if code, resp := request(t, h, http.MethodPut, "/devices/pump/coils/1", `{"value":true}`); code != http.StatusOK || resp["functionCode"] != float64(5) {
		t.Fatalf("single coil write: %d %v, want 200 with FC5", code, resp)
	}
	code, read := request(t, h, http.MethodGet, "/devices/pump/coils/0?quantity=3", "")
	if values, _ := read["values"].([]any); code != http.StatusOK || !slices.Equal(values, []any{true, true, true}) || read["cached"] != false {
		t.Errorf("coils after the writes: %d %v, want [true true true] from the bus", code, read)
	}
}

func TestRESTWriteBody(t *testing.T) {
	_, h := newTestApp(t)

	for _, body := range []string{``, `{}`, `{"value":1,"values":[1]}`, `{"values":[]}`, `{"value":70000}`, `{"value":1,"unknown":2}`, `{"value":1}{"value":2}`} {
		if code, resp := request(t, h, http.MethodPut, "/devices/pump/holding-registers/0", body); code != http.StatusBadRequest {
			t.Errorf("body %q: %d %v, want 400", body, code, resp)
		}
	}
	if code, _ := request(t, h, http.MethodPost, "/devices/pump/holding-registers/0", `{"value":1}`); code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d, want 405", code)
	}
}
