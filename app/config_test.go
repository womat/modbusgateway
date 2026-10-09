package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// writeConfig writes content to a temporary config file and returns its path.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

const validBase = `
webserver:
  apiKey: 0123456789abcdef
buses:
  rs485:
    type: rtu
    timeout: 1s
    rtu: { port: /dev/ttyUSB0 }
  lan:
    type: tcp
    timeout: 2s
    tcp: { host: 192.168.1.10 }
`

// load reads base plus content and validates it.
func load(t *testing.T, content string) (*Config, error) {
	t.Helper()
	cfg, err := LoadConfig(writeConfig(t, validBase+content))
	if err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

func TestLoadConfigExample(t *testing.T) {
	cfg, err := LoadConfig("../config/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err = cfg.Validate(); err != nil {
		t.Fatalf("the shipped example config does not validate: %v", err)
	}
	if len(cfg.Devices) == 0 || len(cfg.Buses) == 0 || cfg.Listen.TCP == nil {
		t.Errorf("the example config should show buses, devices and the tcp listener: %+v", cfg)
	}
	warnings := strings.Join(cfg.Warnings(), "\n")
	if !strings.Contains(warnings, "smartfox") || !strings.Contains(warnings, "apiKey is still the example value") {
		t.Errorf("warnings = %v, want one for smartfox, whose listener is commented out, and one for the example apiKey", warnings)
	}
}

func TestDefaults(t *testing.T) {
	cfg, err := load(t, `
devices:
  meter: { bus: rs485, unitId: 1 }
  pv:    { bus: lan, unitId: 1 }
  fresh: { bus: rs485, unitId: 2, cacheTTL: 0s }
`)
	if err != nil {
		t.Fatal(err)
	}

	if rtu := cfg.Buses["rs485"].RTU; rtu.BaudRate != 9600 || rtu.DataBits != 8 || rtu.Parity != "N" || rtu.StopBits != 1 {
		t.Errorf("rtu defaults = %+v, want 9600 8N1", rtu)
	}
	if cfg.Buses["lan"].TCP.Port != 502 || cfg.Buses["lan"].QueueSize != defaultQueueSize {
		t.Errorf("tcp bus = %+v, want port 502 and queue size %d", cfg.Buses["lan"], defaultQueueSize)
	}
	if !slices.Equal(cfg.Devices["meter"].Functions, defaultFunctionCodes) {
		t.Errorf("functions = %v, want the default %v", cfg.Devices["meter"].Functions, defaultFunctionCodes)
	}
	for name, want := range map[string]time.Duration{"meter": time.Second, "pv": 0, "fresh": 0} {
		if got := *cfg.Devices[name].CacheTTL; got != want {
			t.Errorf("%s: cacheTTL = %v, want %v", name, got, want)
		}
	}
	if cfg.Listen.TCP != nil || cfg.Listen.RTU != nil {
		t.Errorf("listen = %+v without listen blocks, want no listener", cfg.Listen)
	}
}

func TestFunctionCodes(t *testing.T) {
	cfg, err := load(t, "devices:\n  hp: { bus: rs485, unitId: 1, functions: [FC3, fc6, FC16] }\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Devices["hp"].Functions; !slices.Equal(got, []FunctionCode{3, 6, 16}) {
		t.Errorf("functions = %v, want [FC3 FC6 FC16]", got)
	}

	for value, want := range map[string]string{
		"[3]":        "invalid function code",
		"[FC7]":      "invalid function code",
		"[read]":     "invalid function code",
		"[FC3, FC3]": "listed twice",
		"[]":         "must not be empty",
	} {
		_, err := load(t, "devices:\n  hp: { bus: rs485, unitId: 1, functions: "+value+" }\n")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("functions %s: err = %v, want %q", value, err, want)
		}
	}
}

func TestValidationRules(t *testing.T) {
	for name, tc := range map[string]struct{ content, want string }{
		"unitId twice on one bus": {
			"devices:\n  a: { bus: rs485, unitId: 1 }\n  b: { bus: rs485, unitId: 1 }\n", "same unitId 1"},
		"unknown bus": {
			"devices:\n  a: { bus: nowhere, unitId: 1 }\n", `bus "nowhere" is not configured`},
		"unitId 0": {
			"devices:\n  a: { bus: rs485, unitId: 0 }\n", "unitId must be 1-247"},
		"gateway unitId twice": {
			"devices:\n  a: { bus: rs485, unitId: 1, gateway: { unitId: 11 } }\n  b: { bus: rs485, unitId: 2, gateway: { unitId: 11 } }\nlisten:\n  tcp: {}\n", "same gateway.unitId 11"},
		"listener without devices": {
			"devices:\n  a: { bus: rs485, unitId: 1 }\nlisten:\n  tcp: {}\n", "no device on a rtu bus has a gateway block"},
		"rtu listener on a bus port": {
			"devices:\n  pv: { bus: lan, unitId: 1, gateway: { unitId: 31 } }\nlisten:\n  rtu: { port: /dev/ttyUSB0 }\n", "needs a serial port of its own"},
		"rtu listener without port": {
			"devices:\n  pv: { bus: lan, unitId: 1, gateway: { unitId: 31 } }\nlisten:\n  rtu: { baudRate: 19200 }\n", "port must not be empty"},
		"two buses on one port": { // extends the buses of validBase
			"  other:\n    type: rtu\n    timeout: 1s\n    rtu: { port: /dev/ttyUSB0 }\n", "use the same connection"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := load(t, tc.content); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSameUnitIdOnTwoBuses(t *testing.T) {
	if _, err := load(t, "devices:\n  a: { bus: rs485, unitId: 1 }\n  b: { bus: lan, unitId: 1 }\n"); err != nil {
		t.Errorf("unitId 1 on two buses: %v", err)
	}
}

func TestGatewayDevicesPerListener(t *testing.T) {
	cfg, err := load(t, `
devices:
  meter: { bus: rs485, unitId: 1, gateway: { unitId: 11 } }
  pv:    { bus: lan, unitId: 1, gateway: { unitId: 11 } }
  quiet: { bus: rs485, unitId: 2 }
listen:
  tcp: {}
  rtu: { port: /dev/ttyUSB1 }
`)
	if err != nil {
		t.Fatal(err)
	}
	tcp, _ := cfg.GatewayDevices("tcp")
	rtu, _ := cfg.GatewayDevices("rtu")
	if len(tcp) != 1 || tcp[11] != "meter" || len(rtu) != 1 || rtu[11] != "pv" {
		t.Errorf("tcp listener %v, rtu listener %v; want the rtu device on tcp and the tcp device on rtu", tcp, rtu)
	}
	if cfg.Listen.TCP.Host != "0.0.0.0" || cfg.Listen.TCP.Port != defaultListenTCPPort {
		t.Errorf("listen.tcp = %+v, want 0.0.0.0:%d", cfg.Listen.TCP, defaultListenTCPPort)
	}
	if len(cfg.Warnings()) != 0 {
		t.Errorf("warnings = %v, want none", cfg.Warnings())
	}
}

func TestWarningForGatewayWithoutListener(t *testing.T) {
	cfg, err := load(t, "devices:\n  meter: { bus: rs485, unitId: 1, gateway: { unitId: 11 } }\n")
	if err != nil {
		t.Fatal(err)
	}
	if w := cfg.Warnings(); len(w) != 1 || !strings.Contains(w[0], "listen.tcp is not configured") {
		t.Errorf("warnings = %v, want one about listen.tcp", w)
	}
}

func TestLoadConfigExplainsOldKeys(t *testing.T) {
	for name, tc := range map[string]struct{ content, want string }{
		"deviceId":         {"devices:\n  a:\n    deviceId: 1\n", "deviceId: renamed to unitId"},
		"gatewayDeviceId":  {"devices:\n  a:\n    gatewayDeviceId: 11\n", "gatewayDeviceId: replaced by gateway"},
		"device transport": {"devices:\n  a:\n    transport: rtu\n", "transport: the connection moved to buses"},
		"device serial":    {"devices:\n  a:\n    serial: { port: /dev/ttyS0 }\n", "serial: moved to buses"},
		"modbusServer":     {"modbusServer:\n  enabled: true\n", "modbusServer: renamed to listen"},
		"listen enabled":   {"listen:\n  tcp:\n    enabled: true\n", "enabled: removed"},
		"bus transport":    {"buses:\n  b:\n    transport: rtu\n", "transport: renamed to type"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadConfig(writeConfig(t, tc.content))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// Only ${VAR} is expanded, so an API key containing "$" stays as it is.
func TestLoadConfigExpandsBracedVariablesOnly(t *testing.T) {
	t.Setenv("MODBUSGATEWAY_TEST_KEY", "from-env")
	cfg, err := LoadConfig(writeConfig(t, "webserver:\n  apiKey: ${MODBUSGATEWAY_TEST_KEY}\n  keyFile: /tmp/pa$word\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Webserver.ApiKey != "from-env" || cfg.Webserver.KeyFile != "/tmp/pa$word" {
		t.Errorf("apiKey = %q, keyFile = %q; want the environment value and an untouched $", cfg.Webserver.ApiKey, cfg.Webserver.KeyFile)
	}
}

func TestWarningsWeakApiKey(t *testing.T) {
	for key, want := range map[string]bool{"changeme!": true, "short": true, "0123456789abcdef": false} {
		cfg := NewConfig()
		cfg.Webserver.ApiKey = key
		if got := len(cfg.Warnings()) == 1; got != want {
			t.Errorf("apiKey %q: warning = %v, want %v", key, got, want)
		}
	}
}

// The embedded development certificate ships in every release: with env prod a missing
// certFile is an error, not a fallback.
func TestLoadTLSCertProdRefusesEmbeddedCert(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "cert.pem")
	if _, err := loadTLSCert(missing, missing, ProdEnv); err == nil || !strings.Contains(err.Error(), "not used with env: prod") {
		t.Errorf("prod without certFile: err = %v, want a refusal", err)
	}
	if _, err := loadTLSCert(missing, missing, DevEnv); err != nil {
		t.Errorf("dev without certFile: %v, want the embedded fallback", err)
	}
}
