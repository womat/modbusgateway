# modbusgateway

**modbusgateway** puts Modbus devices on the network: an HTTPS REST API and Modbus listeners for the
devices on serial (RTU) and TCP buses, with one queue and a read cache per bus.

---

## Usage

```text
modbusgateway [--config FILE] [--debug] [--version] [--about] [--help]
modbusgateway token add <name> [--config FILE] [--dry-run]
modbusgateway token remove <name> [--config FILE] [--dry-run]
modbusgateway token list [--config FILE]
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
| GET    | `/`                                               | –       | Web page (asks for a token)      |
| GET    | `/version`                                        | –       | App name and version             |
| GET    | `/health`                                         | Token   | Runtime health metrics           |
| GET    | `/devices`, `/devices/{device}/status`            | Token   | Device, bus, queue and cache     |
| GET    | `/activity`                                       | Token   | Clients, buses, last transactions|
| GET    | `/devices/{device}/{table}/{address}?quantity=N`  | Token   | Read (FC1-FC4)                   |
| PUT    | `/devices/{device}/{table}/{address}`             | Token   | Write (FC5, FC6, FC15, FC16)     |

`{table}` is `coils`, `discrete-inputs`, `holding-registers` or `input-registers`; writes go to
`coils` or `holding-registers`. A write body `{"value": x}` uses FC5/FC6, `{"values": [x, ...]}`
FC15/FC16. Authentication with
`Authorization: Bearer <token>`, HTTPS is TLS 1.3 only. A device allows the function codes in its `functions` only.

Modbus listeners: `listen.tcp` offers the devices on serial buses to Modbus TCP clients (default port
1502), `listen.rtu` offers the devices on TCP buses on a serial port of its own.

---

## Signals

| Signal             | Effect                                                     |
|--------------------|------------------------------------------------------------|
| `SIGHUP`           | Reload the config and the token file; a broken file is refused, a failed start falls back to the previous config |
| `SIGTERM`/`SIGINT` | Graceful stop                                              |

A config that does not load or validate at start ends modbusgateway with exit code 78.

---

## Tokens

The REST API except `/` and `/version` needs `Authorization: Bearer <token>`. modbusgateway keeps
only the SHA-256 hash of each token, in the file named by `webserver.tokenFile`, one
`<name> <hash>` per line.

- `modbusgateway token add <name>` writes the hash and prints the token once, on stdout alone.
- `modbusgateway token remove <name>` deletes it, `modbusgateway token list` prints the names.
- `--dry-run` prints what would be done and writes nothing.
- `systemctl reload modbusgateway` applies a change.

Run them with `sudo`: the token file belongs to root.

---

Configuration, installation, TLS and build: see `README.md` in the repository,
https://github.com/womat/modbusgateway
