# modbusgateway

**modbusgateway** provides an HTTPS API for reading from and writing to configured Modbus devices. It can also expose
selected downstream devices through an optional Modbus TCP gateway for read access (`FC1`-`FC4`).

---

## Features

- Optional Modbus TCP server for read access (`FC1`-`FC4`)
- Supports Modbus TCP and RTU downstream devices
- Exposes a secured **HTTPS REST API** (API key authentication)
- **IP allowlist / blocklist** support
- **Hot-reload** of configuration via `SIGHUP`
- Embedded self-signed TLS certificate for development (no setup required)
- Optional **Swagger UI** (build tag `swagger`, dev only)

---

## Where to start

- Runtime, API, build, deploy, and Swagger usage: [`cmd/README.md`](cmd/README.md)
- Example configuration: [`config/config.yaml`](config/config.yaml)
- Swagger generation script: [`docs/generate.sh`](docs/generate.sh)

---

## API Endpoints

| Method | Path                                                      | Auth    | Description                               |
|--------|-----------------------------------------------------------|---------|-------------------------------------------|
| GET    | `/version`                                                | —       | Application name and version              |
| GET    | `/health`                                                 | API key | Runtime health information                |
| GET    | `/devices`                                                | API key | List status of all configured devices     |
| GET    | `/devices/{device}/status`                                | API key | Status of one configured device           |
| GET    | `/devices/{device}/coils/{register}?length=N`             | API key | Read coils (`FC1`)                        |
| GET    | `/devices/{device}/discrete-inputs/{register}?length=N`   | API key | Read discrete inputs (`FC2`)              |
| GET    | `/devices/{device}/holding-registers/{register}?length=N` | API key | Read holding registers (`FC3`)            |
| GET    | `/devices/{device}/input-registers/{register}?length=N`   | API key | Read input registers (`FC4`)              |
| POST   | `/devices/{device}/coils/{register}`                      | API key | Write single coil (`FC5`)                 |
| POST   | `/devices/{device}/holding-registers/{register}`          | API key | Write single holding register (`FC6`)     |
| POST   | `/devices/{device}/coils`                                 | API key | Write multiple coils (`FC15`)             |
| POST   | `/devices/{device}/holding-registers`                     | API key | Write multiple holding registers (`FC16`) |

Authentication via the `X-API-Key` header.

### Examples

```sh
# List configured devices
curl -k -H "X-Api-Key: your-api-key" https://localhost:8443/devices

# Status of one configured device
curl -k -H "X-Api-Key: your-api-key" \
  https://localhost:8443/devices/smartfox/status

# Read two input registers starting at 30001
curl -k -H "X-Api-Key: your-api-key" \
  "https://localhost:8443/devices/smartfox/input-registers/30001?length=2"

# Read eight coils starting at 0
curl -k -H "X-Api-Key: your-api-key" \
  "https://localhost:8443/devices/smartfox/coils/0?length=8"

# Write a single coil
curl -k -X POST -H "X-Api-Key: your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"value":true}' \
  https://localhost:8443/devices/smartfox/coils/5

# Write a single holding register
curl -k -X POST -H "X-Api-Key: your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"value":1234}' \
  https://localhost:8443/devices/smartfox/holding-registers/10

# Write multiple coils
curl -k -X POST -H "X-Api-Key: your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"register":20,"values":[true,false,true,true]}' \
  https://localhost:8443/devices/smartfox/coils

# Write multiple holding registers
curl -k -X POST -H "X-Api-Key: your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"register":100,"values":[10,20,30]}' \
  https://localhost:8443/devices/smartfox/holding-registers

# Application version (no auth required)
curl -k https://localhost:8443/version

# Health check
curl -k -H "X-Api-Key: your-api-key" https://localhost:8443/health
```

---

## Command-line flags

| Flag        | Default                              | Description                     |
|-------------|--------------------------------------|---------------------------------|
| `--config`  | `/opt/modbusgateway/etc/config.yaml` | Path to the config file         |
| `--debug`   | `false`                              | Force `debug` logging to stdout |
| `--version` | `false`                              | Print version and exit          |
| `--about`   | `false`                              | Print app metadata and exit     |
| `--help`    | `false`                              | Print embedded help and exit    |

The config file path can also be set via the environment variable `CONFIG_FILE`.

```sh
modbusgateway --config /etc/modbusgateway/config.yaml
modbusgateway --debug
modbusgateway --version
CONFIG_FILE=/etc/modbusgateway/config.yaml modbusgateway
```

---

## Configuration

Default config path: `/opt/modbusgateway/etc/config.yaml`

Environment variables are expanded inside the YAML file, for example:

```yaml
# logLevel defines the minimum log level.
# Messages with at least this level are logged.
# Allowed values: debug | info | warn | error
logLevel: info

# logDestination defines where logs are written to.
# Supported values: stdout | stderr | /path/to/logfile
logDestination: stdout

# environment: dev | prod
env: dev

# =============================================================================
# Webserver configuration (HTTPS)
# =============================================================================
webserver:
  # Host address the HTTPS server listens on (0.0.0.0 = all interfaces)
  listenHost: 0.0.0.0

  # Port the HTTPS server listens on
  listenPort: 8443

  # Global API key for protected endpoints
  apiKey: changeme!

  # TLS private key file
  keyFile: /opt/modbusgateway/etc/key.pem

  # TLS certificate file
  certFile: /opt/modbusgateway/etc/cert.pem

  # Blocked IP addresses or networks (empty = none blocked)
  # Examples: 192.168.0.1, 192.168.0.0/16, 10.0.0.0/8
  blockedIPs: [ ]
  #  - 192.168.0.1
  #  - 192.168.0.0/16

  # Allowed IP addresses or networks (empty = all allowed)
  # Note: ::1 is the IPv6 loopback address
  # Examples: 127.0.0.1, ::1, 192.168.0.0/16
  allowedIPs: [ ]
  #  - 127.0.0.1
  #  - ::1
  #  - 192.168.0.0/16

# =============================================================================
# Modbus devices exposed through the API
# =============================================================================
# Each top-level key under devices is the public device name used by the API,
# e.g. GET /devices/fronius-smartmeter/input-registers/30001?length=2
#
# Common fields:
# - description: free-text label for humans
# - transport: tcp | rtu
# - deviceId: Modbus slave/unit identifier on that bus
# - gatewayDeviceId: optional external Modbus unit ID of this gateway
#   Clients use this ID against the gateway server. It is independent from
#   deviceId, which remains the slave/unit ID of the real downstream device.
# - timeout: request timeout as Go duration, e.g. 1500ms, 2s
#
# Transport-specific blocks:
# - tcp: required when transport=tcp
# - serial: required when transport=rtu
devices:
  smartfox:
    description: PV controller
    transport: tcp
    deviceId: 2
    gatewayDeviceId: 12
    timeout: 2s
    tcp:
      host: 192.168.65.197
      port: 502

  fronius-smartmeter:
    description: Main power meter
    transport: rtu
    deviceId: 1
    gatewayDeviceId: 11
    timeout: 1s
    serial:
      port: /dev/ttyUSB0
      baudRate: 9600
      dataBits: 8
      parity: N
      stopBits: 1
```

### Device fields

| Field             | Description                                            |
|-------------------|--------------------------------------------------------|
| `description`     | Free-text label                                        |
| `transport`       | `tcp` or `rtu`                                         |
| `deviceId`        | Real downstream Modbus slave/unit ID                   |
| `gatewayDeviceId` | Optional external unit ID exposed by this gateway      |
| `timeout`         | Go duration such as `1500ms`, `2s`, `5s`               |

`gatewayDeviceId` is only used by clients that connect to this gateway as a Modbus TCP server. It is intentionally
separate from the downstream `deviceId`.

### Optional Modbus TCP gateway

When `modbusServer.enabled: true`, the application listens as a Modbus TCP server and forwards read requests to
configured downstream devices.

- exposed functions: `FC1`, `FC2`, `FC3`, `FC4`
- writes stay on the REST API
- every exposed device needs a unique `gatewayDeviceId`

Both the Modbus TCP server and the outbound client stack use `github.com/simonvetter/modbus`.

---

## TLS Certificate

For development the application falls back to an embedded self-signed certificate automatically.
For production, generate your own:

```sh
openssl req -x509 -nodes -newkey rsa:2048 \
  -keyout /opt/modbusgateway/etc/key.pem \
  -out    /opt/modbusgateway/etc/cert.pem \
  -days 825 \
  -subj "/C=AT/ST=Vienna/L=Vienna/O=MyOrg/CN=localhost"
```

**Subject fields:**

| Field           | Example             | Description                                  |
|-----------------|---------------------|----------------------------------------------|
| `/C`            | `AT`                | Country code (2 letters)                     |
| `/ST`           | `Vienna`            | State or province (optional)                 |
| `/L`            | `Vienna`            | City (optional)                              |
| `/O`            | `MyCompany`         | Organization (optional)                      |
| `/OU`           | `DEV`               | Organizational unit (optional)               |
| `/CN`           | `localhost`         | **Common Name — your domain or `localhost`** |
| `/emailAddress` | `admin@example.com` | E-mail address (optional)                    |

> **Note:** Browsers enforce a maximum certificate validity of 825 days. Use `-days 365` for production-like setups.

---

## Installation

### 1. Create system user and directories

```sh
sudo groupadd -f modbusgateway
sudo useradd -r -s /usr/sbin/nologin -g modbusgateway modbusgateway
sudo usermod -aG gpio modbusgateway
sudo mkdir -p /opt/modbusgateway/{bin,etc,data}
sudo chown -R modbusgateway:modbusgateway /opt/modbusgateway
```

### 2. Copy files

```sh
sudo cp modbusgateway /opt/modbusgateway/bin/
sudo cp config.yaml /opt/modbusgateway/etc/
sudo cp cert.pem key.pem /opt/modbusgateway/etc/
sudo chown -R modbusgateway:modbusgateway /opt/modbusgateway
```

### 3. Create systemd service

```sh
sudo tee /etc/systemd/system/modbusgateway.service > /dev/null <<'EOF'
[Unit]
Description=modbusgateway - HTTPS Modbus gateway
After=network.target

[Service]
User=modbusgateway
Group=modbusgateway
Type=simple
ExecStart=/opt/modbusgateway/bin/modbusgateway --config /opt/modbusgateway/etc/config.yaml
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable modbusgateway
sudo systemctl start modbusgateway
sudo systemctl status modbusgateway
```

### 4. View logs

```sh
journalctl -u modbusgateway -n 50 -f
```

---

## Build

```sh
# Raspberry Pi 4/5 (64-bit OS)
make build_arm64

# Raspberry Pi 2/3/4 (32-bit OS)
make build_arm7

# Raspberry Pi 1 / Zero (32-bit OS)
make build_arm6

# Build with Swagger UI (dev only)
make build_arm64_dev

# Build and deploy to Raspberry Pi via SCP
make deploy
```

---

## Hot-Reload

Send `SIGHUP` to reload the configuration without restarting the process:

```sh
sudo systemctl reload modbusgateway
# or
kill -HUP $(pidof modbusgateway)
```

---

## Firewall

```sh
# Allow the configured port (default 8443)
sudo ufw allow 8443/tcp
sudo ufw status
```

---

## Backup & Restore

```sh
# Backup
sudo tar czf /tmp/modbusgateway-backup.tar.gz /opt/modbusgateway

# Restore
sudo tar xzf /tmp/modbusgateway-backup.tar.gz -C /
sudo chown -R modbusgateway:modbusgateway /opt/modbusgateway
sudo systemctl restart modbusgateway
```

---

# License

MIT
