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

// newTestApp starts a Modbus TCP device and returns an App with the device "meter" on it
// (reading only, values cached) and its HTTP handler.
func newTestApp(t *testing.T) (*mbserver.Server, http.Handler) {
	t.Helper()
	device := mbserver.NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := device.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(device.Close)
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
	_, second := request(t, h, http.MethodGet, "/devices/meter/holding-registers/4096?quantity=2", "")
	if second["cached"] != true {
		t.Errorf("second read: %v, want it from the cache", second)
	}
}

func TestRESTStatusCodes(t *testing.T) {
	_, h := newTestApp(t)

	if code, resp := request(t, h, http.MethodPost, "/devices/meter/holding-registers/0", `{"value":1}`); code != http.StatusForbidden {
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
