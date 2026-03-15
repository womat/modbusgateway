# modbusgateway

**modbusgateway** provides an HTTPS API for reading from and writing to configured Modbus devices. It can also expose
selected downstream devices through an optional Modbus TCP gateway for read access (`FC1`-`FC4`).

---

## Usage

```text
modbusgateway [--config FILE] [--debug] [--version] [--about] [--help]
```

---

## Command-Line Flags

| Flag        | Default                              | Description                               |
|-------------|--------------------------------------|-------------------------------------------|
| `--config`  | `/opt/modbusgateway/etc/config.yaml` | Path to the config file                   |
| `--debug`   | `false`                              | Force `debug` logging to stdout           |
| `--version` | `false`                              | Print the application version and exit    |
| `--about`   | `false`                              | Print build and runtime metadata and exit |
| `--help`    | `false`                              | Print this help text and exit             |

The config file path can also be set via the environment variable `CONFIG_FILE`.

```sh
modbusgateway --config /etc/modbusgateway/config.yaml
modbusgateway --debug
modbusgateway --version
CONFIG_FILE=/etc/modbusgateway/config.yaml modbusgateway
```

---

## Configuration

The configuration file is a YAML file. By default it is loaded from `/opt/modbusgateway/etc/config.yaml`.
Environment variables are expanded inside the file, e.g. `apiKey: ${MODBUSGATEWAY_API_KEY}`.
