package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/womat/golib/v2/web"
	"github.com/womat/modbusgateway/pkg/modbusmanager"
	"gopkg.in/yaml.v3"
)

const (
	ProdEnv = "prod"
	DevEnv  = "dev"

	// defaultListenTCPPort is the Modbus TCP port of the gateway; not 502, so it does not
	// collide with a device on the same host and needs no privileges.
	defaultListenTCPPort = 1502
	// defaultRTUCacheTTL is how long a read of a serial device stays in the cache; TCP devices
	// are not cached unless configured.
	defaultRTUCacheTTL = time.Second
)

// Config holds the main application configuration.
type Config struct {
	Env            string                  `yaml:"env"`            // Application environment: dev | prod
	LogLevel       string                  `yaml:"logLevel"`       // Log level: debug | info | warning | error
	LogDestination string                  `yaml:"logDestination"` // Log output: stdout | stderr | /path/to/logfile
	Webserver      WebserverConfig         `yaml:"webserver"`      // Webserver configuration
	Buses          map[string]BusConfig    `yaml:"buses"`          // Physical connections, keyed by name
	Devices        map[string]DeviceConfig `yaml:"devices"`        // Modbus devices, keyed by the name used in the API
	Listen         ListenConfig            `yaml:"listen"`         // Modbus listeners of the gateway
}

// WebserverConfig holds HTTPS server settings.
type WebserverConfig struct {
	ListenHost string      `yaml:"listenHost"` // Host address for web server
	ListenPort int         `yaml:"listenPort"` // Port for web server
	ApiKey     string      `yaml:"apiKey"`     // Removed in v3; only read to name the replacement
	TokenFile  string      `yaml:"tokenFile"`  // File with the accepted bearer tokens as SHA-256 hashes
	Tokens     []web.Token `yaml:"-"`          // Read from TokenFile by LoadConfig
	KeyFile    string      `yaml:"keyFile"`    // SSL private key file
	CertFile   string      `yaml:"certFile"`   // SSL certificate file
	BlockedIPs []string    `yaml:"blockedIPs"` // Forbidden IP addresses or networks
	AllowedIPs []string    `yaml:"allowedIPs"` // Allowed IP addresses or networks
}

// BusConfig describes one physical connection: a serial port or a TCP endpoint. All devices
// on a bus share one queue.
type BusConfig struct {
	Type      string        `yaml:"type"`      // tcp | rtu
	Timeout   time.Duration `yaml:"timeout"`   // response timeout of one request
	QueueSize int           `yaml:"queueSize"` // requests that may wait (default 64)
	TCP       *TCPConfig    `yaml:"tcp"`       // with type tcp
	RTU       *SerialConfig `yaml:"rtu"`       // with type rtu
}

// TCPConfig contains network settings for a Modbus TCP bus.
type TCPConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"` // default 502
}

// SerialConfig contains line settings for a Modbus RTU bus or listener.
type SerialConfig struct {
	Port     string `yaml:"port"`     // Device, e.g. /dev/ttyUSB0
	BaudRate int    `yaml:"baudRate"` // default 9600
	DataBits int    `yaml:"dataBits"` // 5-8, default 8
	Parity   string `yaml:"parity"`   // N | E | O, default N
	StopBits int    `yaml:"stopBits"` // 1 | 2, default 1
}

// DeviceConfig describes one Modbus device on a bus.
type DeviceConfig struct {
	Description string         `yaml:"description"`
	Bus         string         `yaml:"bus"`    // name of an entry in buses
	UnitId      uint8          `yaml:"unitId"` // address of the device on its bus, unique per bus
	Functions   []FunctionCode `yaml:"functions"`
	Gateway     *GatewayConfig `yaml:"gateway"` // offer the device on the Modbus listener

	// CacheTTL is how long a read stays valid in the cache. nil = 1s on a serial bus and no
	// cache on TCP; 0 = no cache.
	CacheTTL *time.Duration `yaml:"cacheTTL"`
}

// GatewayConfig offers a device on the Modbus listener of the other transport: a device on a
// serial bus on the TCP listener, a device on TCP on the RTU listener.
type GatewayConfig struct {
	UnitId uint8 `yaml:"unitId"` // unit ID on the listener, unique per listener
}

// ListenConfig configures the Modbus listeners. A listener is active when its block is
// present (nil = off).
type ListenConfig struct {
	TCP *ListenTCPConfig `yaml:"tcp"` // offers the devices on serial buses
	RTU *ListenRTUConfig `yaml:"rtu"` // offers the devices on TCP buses
}

// ListenTCPConfig configures the Modbus TCP listener.
type ListenTCPConfig struct {
	Host string `yaml:"host"` // default 0.0.0.0
	Port int    `yaml:"port"` // default 1502
}

// ListenRTUConfig configures the Modbus RTU listener on a serial port.
type ListenRTUConfig struct {
	SerialConfig `yaml:",inline"`

	// InterFrameDelay is the silence that ends a request; 0 = t3.5 of the Modbus specification
	// (about 4 ms at 9600 baud). Raise it (e.g. 20-40ms) for USB adapters that split requests.
	InterFrameDelay time.Duration `yaml:"interFrameDelay"`
}

// FunctionCode is a Modbus function code, written FC1 ... FC16 in the configuration.
type FunctionCode uint8

// supportedFunctionCodes are the function codes a device can allow.
var supportedFunctionCodes = []FunctionCode{1, 2, 3, 4, 5, 6, 15, 16}

// defaultFunctionCodes apply to a device without functions: reading only.
var defaultFunctionCodes = []FunctionCode{1, 2, 3, 4}

// String returns e.g. "FC3".
func (f FunctionCode) String() string {
	return "FC" + strconv.Itoa(int(f))
}

// UnmarshalYAML accepts FC1 ... FC16, in any case, for the supported function codes.
func (f *FunctionCode) UnmarshalYAML(node *yaml.Node) error {
	text := strings.ToUpper(strings.TrimSpace(node.Value))
	if node.Kind == yaml.ScalarNode && strings.HasPrefix(text, "FC") {
		if n, err := strconv.Atoi(text[2:]); err == nil && slices.Contains(supportedFunctionCodes, FunctionCode(n)) {
			*f = FunctionCode(n)
			return nil
		}
	}
	return fmt.Errorf("line %d: invalid function code %q, must be one of %v", node.Line, node.Value, supportedFunctionCodes)
}

// NewConfig returns a Config with sane defaults
func NewConfig() *Config {
	return &Config{
		Env:            DevEnv,
		LogLevel:       "info",
		LogDestination: "stdout",
		Webserver: WebserverConfig{
			ListenHost: "0.0.0.0",
			ListenPort: 8443,
			BlockedIPs: []string{},
			AllowedIPs: []string{},
		},
		Buses:   map[string]BusConfig{},
		Devices: map[string]DeviceConfig{},
	}
}

// envBraces matches ${VAR} references; see expandEnvBraces.
var envBraces = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnvBraces replaces ${VAR} with the value of the environment variable VAR, or with an
// empty string when it is unset. Unlike os.ExpandEnv it leaves every other "$" alone, so a
// password containing "$" is not silently cut short.
func expandEnvBraces(s string) string {
	return envBraces.ReplaceAllStringFunc(s, func(ref string) string {
		return os.Getenv(envBraces.FindStringSubmatch(ref)[1])
	})
}

// LoadConfig loads configuration from a YAML file and expands ${VAR} environment references,
// then reads the bearer tokens from webserver.tokenFile.
//
// Unknown keys are an error rather than ignored, so a misspelled or renamed key cannot silently
// leave its setting at the default. A token file that is missing or broken is an error too: the
// service would otherwise start and reject every request with 401. Since a SIGHUP reload goes
// through here, a token added with "modbusgateway token add" applies on reload.
func LoadConfig(fileName string) (*Config, error) {
	cfg, err := readConfig(fileName)
	if err != nil {
		return cfg, err
	}

	if cfg.Webserver.TokenFile != "" {
		if cfg.Webserver.Tokens, err = web.ReadTokenFile(cfg.Webserver.TokenFile); err != nil {
			return cfg, fmt.Errorf("token file: %w", err)
		}
	}

	return cfg, nil
}

// TokenFile returns webserver.tokenFile of the config file, without reading the token file
// itself - "modbusgateway token add" has to work before that file exists.
func TokenFile(fileName string) (string, error) {
	cfg, err := readConfig(fileName)
	if err != nil {
		return "", err
	}
	return cfg.Webserver.TokenFile, nil
}

// readConfig loads the config file alone. A relative tokenFile is resolved against the
// directory of the config file, so the service and "modbusgateway token" find the same file
// wherever they are started from.
func readConfig(fileName string) (*Config, error) {
	cfg := NewConfig()

	fileInfo, err := os.Stat(fileName)
	if err != nil {
		return cfg, err
	}
	if fileInfo.IsDir() {
		return cfg, errors.New("config path is a directory, not a file")
	}

	content, err := os.ReadFile(fileName)
	if err != nil {
		return cfg, err
	}

	dec := yaml.NewDecoder(bytes.NewReader([]byte(expandEnvBraces(string(content)))))
	dec.KnownFields(true)
	if err = dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return cfg, fmt.Errorf("failed to unmarshal config: %w%s", err, renamedKeyHint(err))
	}

	if f := cfg.Webserver.TokenFile; f != "" && !filepath.IsAbs(f) {
		cfg.Webserver.TokenFile = filepath.Join(filepath.Dir(fileName), f)
	}

	return cfg, nil
}

// renamedKeys explains keys of earlier releases, keyed by the type they are reported in.
var renamedKeys = map[string]string{
	"Config.modbusServer":          "renamed to listen, with a tcp and an rtu block; a listener is active when its block is present",
	"DeviceConfig.deviceId":        "renamed to unitId",
	"DeviceConfig.gatewayDeviceId": "replaced by gateway: { unitId: N }",
	"DeviceConfig.transport":       "the connection moved to buses: define it there and reference it with bus",
	"DeviceConfig.timeout":         "moved to buses.<name>.timeout",
	"DeviceConfig.tcp":             "moved to buses.<name>.tcp, referenced with bus",
	"DeviceConfig.serial":          "moved to buses.<name>.rtu, referenced with bus",
	"BusConfig.transport":          "renamed to type",
	"BusConfig.serial":             "renamed to rtu",
	"ListenTCPConfig.enabled":      "removed: a listener is active when its block is present; delete or comment out the block to turn it off",
	"ListenRTUConfig.enabled":      "removed: a listener is active when its block is present; delete or comment out the block to turn it off",
	"WebserverConfig.jwtSecret":    "removed: authentication is by bearer tokens from webserver.tokenFile only",
	"WebserverConfig.jwtID":        "removed: authentication is by bearer tokens from webserver.tokenFile only",
}

// unknownField matches the key and type in yaml.v3's "field X not found in type pkg.Y".
var unknownField = regexp.MustCompile(`field (\w+) not found in type \w+\.(\w+)`)

// renamedKeyHint returns "; <key>: <hint>" for every renamed key that err complains about.
func renamedKeyHint(err error) string {
	var hint strings.Builder
	seen := map[string]bool{}
	for _, m := range unknownField.FindAllStringSubmatch(err.Error(), -1) {
		key := m[2] + "." + m[1]
		if text, ok := renamedKeys[key]; ok && !seen[key] {
			seen[key] = true
			fmt.Fprintf(&hint, "; %s: %s", m[1], text)
		}
	}
	return hint.String()
}

// Validate checks the Config for invalid or missing values and applies the defaults of the
// buses, devices and listeners.
func (c *Config) Validate() error {

	if c.Env != ProdEnv && c.Env != DevEnv {
		return fmt.Errorf("invalid environment: %s, must be %s or %s", c.Env, ProdEnv, DevEnv)
	}

	if c.Webserver.ApiKey != "" {
		return errors.New(`webserver.apiKey is no longer supported; create a token with ` +
			`"modbusgateway token add <name>" and set webserver.tokenFile`)
	}
	if c.Webserver.TokenFile == "" {
		return errors.New(`webserver.tokenFile is not configured; create a token with "modbusgateway token add <name>"`)
	}

	validLogLevels := []string{"debug", "info", "warning", "warn", "error"}
	if !slices.Contains(validLogLevels, c.LogLevel) {
		return fmt.Errorf("invalid log level: %s, must be one of %v", c.LogLevel, validLogLevels)
	}

	if c.Webserver.ListenPort < 1 || c.Webserver.ListenPort > 65535 {
		return fmt.Errorf("invalid port: %d", c.Webserver.ListenPort)
	}

	if err := c.validateBuses(); err != nil {
		return err
	}
	if err := c.validateDevices(); err != nil {
		return err
	}
	return c.validateListen()
}

func (c *Config) validateBuses() error {
	endpoints := map[string]string{} // serial port or host:port -> bus
	for _, name := range sortedKeys(c.Buses) {
		bus := c.Buses[name]
		bus.Type = strings.ToLower(bus.Type)
		if bus.QueueSize == 0 {
			bus.QueueSize = modbusmanager.DefaultQueueSize
		}

		var endpoint string
		switch bus.Type {
		case "tcp":
			if bus.TCP == nil || bus.RTU != nil {
				return fmt.Errorf("bus %q: type tcp needs a tcp block and no rtu block", name)
			}
			if bus.TCP.Port == 0 {
				bus.TCP.Port = 502
			}
			if bus.TCP.Host == "" {
				return fmt.Errorf("bus %q: tcp.host must not be empty", name)
			}
			if bus.TCP.Port < 1 || bus.TCP.Port > 65535 {
				return fmt.Errorf("bus %q: invalid tcp.port %d", name, bus.TCP.Port)
			}
			endpoint = net.JoinHostPort(bus.TCP.Host, strconv.Itoa(bus.TCP.Port))
		case "rtu":
			if bus.RTU == nil || bus.TCP != nil {
				return fmt.Errorf("bus %q: type rtu needs an rtu block and no tcp block", name)
			}
			bus.RTU.applyDefaults()
			if err := bus.RTU.validate(); err != nil {
				return fmt.Errorf("bus %q: rtu.%w", name, err)
			}
			endpoint = bus.RTU.Port
		default:
			return fmt.Errorf("bus %q: invalid type %q, must be tcp or rtu", name, bus.Type)
		}

		if bus.Timeout <= 0 {
			return fmt.Errorf("bus %q: timeout must be > 0", name)
		}
		if bus.QueueSize < 1 {
			return fmt.Errorf("bus %q: queueSize must be > 0", name)
		}
		if other, used := endpoints[endpoint]; used {
			return fmt.Errorf("buses %q and %q use the same connection %s; put both devices on one bus", other, name, endpoint)
		}
		endpoints[endpoint] = name
		c.Buses[name] = bus
	}
	return nil
}

func (c *Config) validateDevices() error {
	unitIds := map[string]map[uint8]string{} // bus -> unit ID -> device
	for _, name := range sortedKeys(c.Devices) {
		device := c.Devices[name]
		bus, ok := c.Buses[device.Bus]
		switch {
		case !ok:
			return fmt.Errorf("device %q: bus %q is not configured", name, device.Bus)
		case device.UnitId < 1 || device.UnitId > 247:
			return fmt.Errorf("device %q: unitId must be 1-247, got %d", name, device.UnitId)
		case device.CacheTTL != nil && *device.CacheTTL < 0:
			return fmt.Errorf("device %q: cacheTTL must not be negative", name)
		}

		if unitIds[device.Bus] == nil {
			unitIds[device.Bus] = map[uint8]string{}
		}
		if other, used := unitIds[device.Bus][device.UnitId]; used {
			return fmt.Errorf("devices %q and %q on bus %q use the same unitId %d", other, name, device.Bus, device.UnitId)
		}
		unitIds[device.Bus][device.UnitId] = name

		if device.Functions == nil {
			device.Functions = slices.Clone(defaultFunctionCodes)
		}
		if len(device.Functions) == 0 {
			return fmt.Errorf("device %q: functions must not be empty; leave it out for %v", name, defaultFunctionCodes)
		}
		seen := map[FunctionCode]bool{}
		for _, fc := range device.Functions {
			if seen[fc] {
				return fmt.Errorf("device %q: function %s is listed twice", name, fc)
			}
			seen[fc] = true
		}

		if device.CacheTTL == nil {
			ttl := time.Duration(0)
			if bus.Type == "rtu" {
				ttl = defaultRTUCacheTTL
			}
			device.CacheTTL = &ttl
		}

		if device.Gateway != nil && (device.Gateway.UnitId < 1 || device.Gateway.UnitId > 247) {
			return fmt.Errorf("device %q: gateway.unitId must be 1-247, got %d", name, device.Gateway.UnitId)
		}
		c.Devices[name] = device
	}
	return nil
}

func (c *Config) validateListen() error {
	if l := c.Listen.TCP; l != nil {
		if l.Host == "" {
			l.Host = "0.0.0.0"
		}
		if l.Port == 0 {
			l.Port = defaultListenTCPPort
		}
		if l.Port < 1 || l.Port > 65535 {
			return fmt.Errorf("invalid listen.tcp.port: %d", l.Port)
		}
	}
	if l := c.Listen.RTU; l != nil {
		l.applyDefaults()
		if err := l.validate(); err != nil {
			return fmt.Errorf("invalid listen.rtu: %w", err)
		}
		for _, name := range sortedKeys(c.Buses) {
			if bus := c.Buses[name]; bus.RTU != nil && bus.RTU.Port == l.Port {
				return fmt.Errorf("listen.rtu.port %s is the port of bus %q; the listener needs a serial port of its own", l.Port, name)
			}
		}
	}

	for _, listener := range []string{"tcp", "rtu"} {
		if !c.listenerActive(listener) {
			continue
		}
		devices, err := c.GatewayDevices(listener)
		if err != nil {
			return err
		}
		if len(devices) == 0 {
			return fmt.Errorf("listen.%s is configured, but no device on a %s bus has a gateway block", listener, otherTransport(listener))
		}
	}
	return nil
}

func (c *Config) listenerActive(listener string) bool {
	if listener == "tcp" {
		return c.Listen.TCP != nil
	}
	return c.Listen.RTU != nil
}

// otherTransport is the bus type a listener offers: the TCP listener offers serial devices,
// the RTU listener TCP devices.
func otherTransport(listener string) string {
	if listener == "tcp" {
		return "rtu"
	}
	return "tcp"
}

// GatewayDevices returns the devices offered on a listener ("tcp" or "rtu"), keyed by their
// gateway unit ID. It fails when two devices claim the same unit ID on that listener.
func (c *Config) GatewayDevices(listener string) (map[uint8]string, error) {
	devices := map[uint8]string{}
	for _, name := range sortedKeys(c.Devices) {
		device := c.Devices[name]
		if device.Gateway == nil || c.Buses[device.Bus].Type != otherTransport(listener) {
			continue
		}
		if other, used := devices[device.Gateway.UnitId]; used {
			return nil, fmt.Errorf("devices %q and %q use the same gateway.unitId %d on listen.%s", other, name, device.Gateway.UnitId, listener)
		}
		devices[device.Gateway.UnitId] = name
	}
	return devices, nil
}

// Warnings returns findings that do not stop the service but should be fixed. It never includes
// secret values.
func (c *Config) Warnings() []string {
	var warnings []string

	for _, name := range sortedKeys(c.Devices) {
		device := c.Devices[name]
		if device.Gateway == nil {
			continue
		}
		// The pairing is symmetric: the listener of a bus type is its other transport.
		listener := otherTransport(c.Buses[device.Bus].Type)
		if !c.listenerActive(listener) {
			warnings = append(warnings, fmt.Sprintf("device %q has a gateway block, but listen.%s is not configured; it is offered on the REST API only", name, listener))
		}
	}
	return warnings
}

func (s *SerialConfig) applyDefaults() {
	if s.BaudRate == 0 {
		s.BaudRate = 9600
	}
	if s.DataBits == 0 {
		s.DataBits = 8
	}
	if s.Parity == "" {
		s.Parity = "N"
	}
	if s.StopBits == 0 {
		s.StopBits = 1
	}
}

func (s *SerialConfig) validate() error {
	switch {
	case s.Port == "":
		return errors.New("port must not be empty")
	case s.BaudRate <= 0:
		return fmt.Errorf("baudRate must be > 0, got %d", s.BaudRate)
	case s.DataBits < 5 || s.DataBits > 8:
		return fmt.Errorf("dataBits must be one of 5, 6, 7, 8, got %d", s.DataBits)
	case !slices.Contains([]string{"N", "E", "O"}, strings.ToUpper(s.Parity)):
		return fmt.Errorf("parity must be N, E or O, got %q", s.Parity)
	case s.StopBits != 1 && s.StopBits != 2:
		return fmt.Errorf("stopBits must be 1 or 2, got %d", s.StopBits)
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
