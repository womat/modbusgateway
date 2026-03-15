# Copilot instructions for ModbusGateway

## Build and test commands

- Full build check: `go build ./...`
- Build the main binary explicitly: `go build -o bin/mbgw ./cmd/mbgw.go`
- Run locally with the sample config in this repo: `go run ./cmd/mbgw.go -config ./config/config.yaml`
- Cross-build examples already used by the repo live in `build/make.bat`:
  - Linux ARM: `GOOS=linux GOARCH=arm go build -o bin/mbgw ./cmd/mbgw.go`
  - Windows 386: `GOOS=windows GOARCH=386 go build -o bin/mbgw.exe ./cmd/mbgw.go`
- Repo-wide test/smoke check: `go test ./...`
- There are currently no `*_test.go` files in the repository, so there is no existing single-test command to reference yet.
- At the moment, `go build ./...` and `go test ./...` fail in a fresh checkout because `go.sum` is missing some non-`/go.mod` checksum entries for `github.com/knadh/koanf` and `github.com/goburrow/modbus`. If you need to run commands that resolve dependencies, refresh module sums first with `go mod tidy`.

## High-level architecture

- `cmd/mbgw.go` is the only entrypoint. `main()` calls `pkg/config.Init()` and then blocks forever with `select {}`. There is no separate service runner or shutdown orchestration.
- `pkg/config` is the bootstrap layer. It parses CLI flags, loads YAML config through `koanf`, stores the result in `global.Options`, optionally enables the HTTP layer and debug mode, then creates and starts the shared Modbus client.
- `global` is the shared runtime state package. It owns the process-wide config (`global.Options`), the singleton Modbus client (`global.Clientd`), the version string, and the `IsError` helper that respects the `IsQuiet` flag.
- `pkg/mbclient` is the core Modbus execution engine. `NewClient()` creates a client with `Get` and `Stop` channels, and `Start()` launches a single goroutine (`Clientd`) that serializes requests. Each request carries its own response channel.
- `pkg/webservice` exposes the HTTP API on the default `net/http` mux. Handlers enqueue requests onto `global.Clientd.Get` and wait on the per-request channel for the Modbus result.
- The only HTTP endpoints currently implemented are `/version` and `/readholdingregisters`, and they are registered only when the matching booleans under `webservices:` are enabled in config.

## Key conventions

- Local development should usually pass `-config ./config/config.yaml`. The compiled binary defaults to `/opt/womat/config.yaml`, which is the production-oriented default baked into `pkg/config/loadConfig`.
- Configuration shape matters more than directory structure here. The important keys are `webserver`, `webservices`, `debug`, `IsQuiet`, and `modbusclient`, matching the sample YAML in `config/config.yaml`.
- The codebase uses package-level mutable singletons instead of dependency injection. Most runtime behavior depends on `global.Options` and `global.Clientd`, so changes to startup order or package initialization can have repo-wide effects.
- Modbus connections are passed around as strings, not structured config objects. Preserve the existing formats:
  - `TCP 192.168.65.197:502 DeviceId:2 Timeout:2000`
  - `RTU com4,9600,8,N,1 DeviceId:1 Timeout:1500`
  - `ASCII /dev/ttyS0,9600,8,N,1 DeviceId:1 Timeout:1000`
- `pkg/mbclient` reparses the connection string on every request and infers transport type, endpoint/serial settings, `DeviceId`, and `Timeout` from those tokens. If you change connection handling, keep it compatible with both the YAML sample and the HTTP override flow.
- The `/readholdingregisters` handler allows a `Connection` query parameter override only when it matches one of the regexes in `modbusclient.permitted`. That allowlist is a key safety boundary for remote requests.
- The handler supports both `Address` and `Register` query params, but they are not equivalent: `Register` is treated as one-based and decremented before the Modbus call, while `Address` is used as-is.
- Error handling is intentionally simple and mostly log-based. The HTTP handlers often log and return early without writing structured error payloads; match that style unless you are deliberately changing API behavior.
