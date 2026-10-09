# modbusgateway

**modbusgateway** puts Modbus devices on the network: an HTTPS REST API and Modbus listeners for the
devices on serial (RTU) and TCP buses, with one queue and a read cache per bus.

---

## Usage

```text
modbusgateway [--config FILE] [--debug] [--version] [--about] [--help]
```

| Flag        | Default                              | Description                                       |
|-------------|--------------------------------------|---------------------------------------------------|
| `--config`  | `/opt/modbusgateway/etc/config.yaml` | Path to the configuration file                    |
| `--debug`   | `false`                              | Enable debug logging to stdout (overrides config) |
| `--version` | `false`                              | Print the application version and exit            |
| `--about`   | `false`                              | Print application details and exit                |
| `--help`    | `false`                              | Print this help message and exit                  |

The config file path can also be set via the environment variable `CONFIG_FILE`; `--config` wins over it.

---

## API

| Method | Path                                              | Auth    | Description                      |
|--------|---------------------------------------------------|---------|----------------------------------|
| GET    | `/version`                                        | –       | App name and version             |
| GET    | `/health`                                         | API Key | Runtime health metrics           |
| GET    | `/devices`, `/devices/{device}/status`            | API Key | Device, bus, queue and cache     |
| GET    | `/devices/{device}/{table}/{address}?quantity=N`  | API Key | Read (FC1-FC4)                   |
| PUT    | `/devices/{device}/{table}/{address}`             | API Key | Write (FC5, FC6, FC15, FC16)     |

`{table}` is `coils`, `discrete-inputs`, `holding-registers` or `input-registers`; writes go to
`coils` or `holding-registers`. A write body `{"value": x}` uses FC5/FC6, `{"values": [x, ...]}`
FC15/FC16. Authentication via
the `X-API-Key` header. A device allows the function codes in its `functions` only.

Modbus listeners: `listen.tcp` offers the devices on serial buses to Modbus TCP clients (default port
1502), `listen.rtu` offers the devices on TCP buses on a serial port of its own.

---

## Signals

| Signal             | Effect                                                     |
|--------------------|------------------------------------------------------------|
| `SIGHUP`           | Reload the config; a broken file is refused, a failed start falls back to the previous config |
| `SIGTERM`/`SIGINT` | Graceful stop                                              |

---

Configuration, installation, TLS and build: see `README.md` in the repository,
https://github.com/womat/modbusgateway
