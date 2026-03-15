package app

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	ProdEnv = "prod"
	DevEnv  = "dev"
)

// Config holds the main application configuration.
type Config struct {
	Env            string                  `yaml:"env"`            // Application environment: dev | prod
	LogLevel       string                  `yaml:"logLevel"`       // Log level: debug | info | warning | error
	LogDestination string                  `yaml:"logDestination"` // Log output: stdout | stderr | /path/to/logfile
	Webserver      WebserverConfig         `yaml:"webserver"`      // Webserver configuration
	ModbusServer   ModbusServerConfig      `yaml:"modbusServer"`   // Optional Modbus TCP gateway server
	Devices        map[string]DeviceConfig `yaml:"devices"`        // Modbus devices addressable through the API
}

// WebserverConfig holds HTTPS server settings.
type WebserverConfig struct {
	ListenHost string   `yaml:"listenHost"` // Host address for web server
	ListenPort int      `yaml:"listenPort"` // Port for web server
	ApiKey     string   `yaml:"apiKey"`     // API key for requests
	JwtSecret  string   `yaml:"jwtSecret"`  // Secret for JWT tokens
	JwtID      string   `yaml:"jwtID"`      // Unique JWT ID
	KeyFile    string   `yaml:"keyFile"`    // SSL private key file
	CertFile   string   `yaml:"certFile"`   // SSL certificate file
	BlockedIPs []string `yaml:"blockedIPs"` // Forbidden IP addresses or networks
	AllowedIPs []string `yaml:"allowedIPs"` // Allowed IP addresses or networks
}

// ModbusServerConfig holds optional Modbus TCP gateway server settings.
type ModbusServerConfig struct {
	Enabled    bool   `yaml:"enabled"`
	ListenHost string `yaml:"listenHost"`
	ListenPort int    `yaml:"listenPort"`
}

// DeviceConfig describes one named Modbus endpoint exposed by the API.
type DeviceConfig struct {
	Description     string        `yaml:"description"`
	Transport       string        `yaml:"transport"`
	DeviceID        uint8         `yaml:"deviceId"`
	GatewayDeviceID uint8         `yaml:"gatewayDeviceId,omitempty"`
	Timeout         time.Duration `yaml:"timeout"`
	TCP             *TCPConfig    `yaml:"tcp,omitempty"`
	Serial          *SerialConfig `yaml:"serial,omitempty"`
}

// TCPConfig contains network settings for Modbus TCP devices.
type TCPConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

// SerialConfig contains line settings for Modbus RTU or ASCII devices.
type SerialConfig struct {
	Port     string `yaml:"port"`
	BaudRate int    `yaml:"baudRate"`
	DataBits int    `yaml:"dataBits"`
	Parity   string `yaml:"parity"`
	StopBits int    `yaml:"stopBits"`
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
		ModbusServer: ModbusServerConfig{
			ListenHost: "0.0.0.0",
			ListenPort: 1502,
		},
		Devices: map[string]DeviceConfig{},
	}
}

// LoadConfig loads configuration from a YAML file and expands environment variables.
func LoadConfig(fileName string) (*Config, error) {
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

	// Replace environment variables in the YAML
	replaced := os.ExpandEnv(string(content))

	// Unmarshal YAML into the config struct
	if err = yaml.Unmarshal([]byte(replaced), cfg); err != nil {
		return cfg, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return cfg, nil
}

// IsDevEnv returns true if the environment is development.
func (c *Config) IsDevEnv() bool {
	return c.Env == DevEnv
}

// Validate checks the Config for invalid or missing values.
func (c *Config) Validate() error {

	if c.Env != ProdEnv && c.Env != DevEnv {
		return fmt.Errorf("invalid environment: %s, must be %s or %s", c.Env, ProdEnv, DevEnv)
	}

	if c.Webserver.ApiKey == "" {
		return errors.New("ApiKey is not configured")
	}

	validLogLevels := []string{"debug", "info", "warning", "warn", "error"}
	if !slices.Contains(validLogLevels, c.LogLevel) {
		return fmt.Errorf("invalid log level: %s, must be one of %v", c.LogLevel, validLogLevels)
	}

	if c.Webserver.ListenPort < 1 || c.Webserver.ListenPort > 65535 {
		return fmt.Errorf("invalid port: %d", c.Webserver.ListenPort)
	}
	if c.ModbusServer.Enabled && (c.ModbusServer.ListenPort < 1 || c.ModbusServer.ListenPort > 65535) {
		return fmt.Errorf("invalid modbusServer.listenPort: %d", c.ModbusServer.ListenPort)
	}
	if c.ModbusServer.Enabled && c.ModbusServer.ListenHost == "" {
		return errors.New("modbusServer.listenHost must not be empty when modbusServer is enabled")
	}

	gatewayIDs := make(map[uint8]string)
	exposedCount := 0
	for name, device := range c.Devices {
		if err := validateDeviceConfig(name, device); err != nil {
			return err
		}
		if c.ModbusServer.Enabled && device.GatewayDeviceID > 0 {
			if other, exists := gatewayIDs[device.GatewayDeviceID]; exists {
				return fmt.Errorf("devices %q and %q use the same gatewayDeviceId %d", other, name, device.GatewayDeviceID)
			}
			gatewayIDs[device.GatewayDeviceID] = name
			exposedCount++
		}
	}
	if c.ModbusServer.Enabled && exposedCount == 0 {
		return errors.New("modbusServer is enabled, but no device has a gatewayDeviceId configured")
	}

	return nil
}

// validateDeviceConfig checks one named device entry for transport-specific consistency.
func validateDeviceConfig(name string, device DeviceConfig) error {
	if name == "" {
		return errors.New("device name must not be empty")
	}

	transport := strings.ToLower(device.Transport)
	if !slices.Contains([]string{"tcp", "rtu"}, transport) {
		return fmt.Errorf("device %q: invalid transport %q, must be tcp or rtu", name, device.Transport)
	}

	if device.Timeout <= 0 {
		return fmt.Errorf("device %q: timeout must be > 0", name)
	}

	switch transport {
	case "tcp":
		if device.TCP == nil {
			return fmt.Errorf("device %q: tcp config is required for transport=tcp", name)
		}
		if device.Serial != nil {
			return fmt.Errorf("device %q: serial config must not be set for transport=tcp", name)
		}
		if device.TCP.Host == "" {
			return fmt.Errorf("device %q: tcp.host must not be empty", name)
		}
		if device.TCP.Port < 1 || device.TCP.Port > 65535 {
			return fmt.Errorf("device %q: invalid tcp.port %d", name, device.TCP.Port)
		}
	case "rtu":
		if device.Serial == nil {
			return fmt.Errorf("device %q: serial config is required for transport=%s", name, transport)
		}
		if device.TCP != nil {
			return fmt.Errorf("device %q: tcp config must not be set for transport=%s", name, transport)
		}
		if device.Serial.Port == "" {
			return fmt.Errorf("device %q: serial.port must not be empty", name)
		}
		if device.Serial.BaudRate <= 0 {
			return fmt.Errorf("device %q: serial.baudRate must be > 0", name)
		}
		if !slices.Contains([]int{5, 6, 7, 8}, device.Serial.DataBits) {
			return fmt.Errorf("device %q: serial.dataBits must be one of 5, 6, 7, 8", name)
		}
		if !slices.Contains([]string{"N", "E", "O"}, strings.ToUpper(device.Serial.Parity)) {
			return fmt.Errorf("device %q: serial.parity must be N, E, or O", name)
		}
		if !slices.Contains([]int{1, 2}, device.Serial.StopBits) {
			return fmt.Errorf("device %q: serial.stopBits must be 1 or 2", name)
		}
	}

	return nil
}
