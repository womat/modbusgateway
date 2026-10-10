# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

`modbusgateway` is a Go daemon that puts Modbus devices on the network. Devices sit on **buses** — a serial port (Modbus RTU) or a TCP endpoint — and are offered through a TLS REST API with API-key auth and through **Modbus listeners**: `listen.tcp` offers the devices on rtu buses to Modbus TCP clients, `listen.rtu` offers the devices on tcp buses to a client on a serial line of its own. Every bus has one worker with a queue and a read cache, so no two requests meet on the wire. Single binary, cross-compiled for the Pi. It shares its skeleton (lifecycle, config, TLS, Makefile, CI, release) with the sibling projects `relayhat`, `s0meter`, `tadl` and `smartmeter` — when changing one of those parts, check whether the others need the same change. The Modbus listeners use the own module `github.com/womat/mbserver`.

The production predecessor was `mbgw` (release `v1.0.10`, `/readholdingregisters` over plain HTTP); 2.0.0 is a rewrite with a different API and configuration.

## Build / develop

Pure Go, no cgo: `go build ./...`, `go vet ./...` and `go test -race ./...` work on macOS too.

```sh
make build_arm6        # every Pi in 32-bit mode, Pi 1 / Zero — the default deployment target
make build_arm7        # Pi 2/3/4/5, 32-bit OS
make build_arm64       # Pi 3/4/5/400/Zero2, 64-bit OS
make build_arm6_dev    # + Swagger UI (-tags swagger); _dev variants exist per arch
make test              # go test -race ./...
make lint              # gofmt, go vet, golangci-lint, govulncheck - for $(PI_ARCH), with and without -tags swagger
make deploy            # build for $(PI_ARCH) then scp to $(PI_USER)@$(PI_HOST)
make clean
```

`ensure_dev_certs` (a prerequisite of every build target, `make test` and `make lint`) generates `app/certs/dev_{cert,key}.pem` if missing; these are `//go:embed`-ed and gitignored, so a fresh clone must build via `make`, not bare `go build`. `VERSION` (`app/app.go`), `buildDate` and `buildCommit` (`cmd/main.go`) are `var`s injected via `-ldflags` — never edit them in source. The Makefile derives `VERSION` from `git describe --tags`; GoReleaser uses the tag itself.

`make lint` installs the pinned golangci-lint and govulncheck into `bin/tools` for this machine and runs them for the deployment target (`GOOS=linux`): only then is the Linux-only RTU listener test linted, and `go run` under a target `GOOS`/`GOARCH` would build a binary the host cannot execute. `.golangci.yml` keeps the default linters; every exclusion there is a decision with a comment saying why (as in golib) — fix a finding rather than adding one, and when excluding, keep it narrow (an exact function under errcheck's `exclude-functions`, or a path + linter rule).

Tests need no hardware: Modbus devices are emulated with an mbserver on a free local port, the bus worker is tested against a fake `busClient`. `TestRTUListener` (`app/service/modbusserver/rtu_linux_test.go`) runs the RTU listener over a socat pty pair; it is Linux-only and skipped without socat — on macOS: `docker run --rm -v "$PWD":/src -w /src golang:1.27 sh -c 'apt-get update -qq && apt-get install -y -qq socat && make test'`.

### Releases

**There is one branch, `main`: work is committed to it and a release is a tag on it.** `make release TAG=vX.Y.Z` refuses to run from any other branch, with a dirty tree, or when `main` and `origin/main` differ; `.github/workflows/release.yml` re-checks that the tagged commit is on `main`, so a hand-made `git tag` cannot bypass it. Use a short-lived feature branch for work that must not land on `main` yet.

Versioning is SemVer and the Git tag is the single source of truth. `.github/workflows/release.yml` runs `goreleaser release --clean`, which builds linux arm64/armv7/armv6 and publishes a GitHub release with checksums and a grouped changelog. `.goreleaser.yaml`'s `before` hook must keep running `make ensure_dev_certs` (GoReleaser calls `go build` directly), and archives must keep shipping `README.md` (third-party license overview) and `LICENSE` (MIT). When adding a dependency, update the license table in `README.md`.

`.github/workflows/ci.yml` runs on every push/PR against `main`: a `test` job (native, with socat, `make test`) and a `build` matrix over armv6/armv7/arm64 that vets, builds (also `-tags swagger`), runs golangci-lint (with and without `-tags swagger`) and govulncheck. All actions are pinned to a commit SHA with the release in a comment, `govulncheck` and `golangci-lint` to a version (in the workflows and the Makefile); `.github/dependabot.yml` updates actions and Go modules weekly, but not the `go install` pins.

`PI_USER`/`PI_HOST`/`PI_PATH` default to placeholders; the actual device comes from environment variables or, project-specific, from `Makefile.local` (gitignored, pulled in via `-include`). Real host names, addresses and the production configuration stay out of this public repository. **`PI_ARCH` defaults to `arm6`**: it runs on every Pi in 32-bit mode, and a Pi Zero (1st gen) is ARMv6 only. `make deploy` is the development loop (binary reports a `-dirty` version), `make deploy_release TAG=vX.Y.Z` downloads, verifies and copies a published release.

### Swagger

Swagger UI is behind the `swagger` build tag (`app/swagger.go` vs `app/swagger_stub.go`). Regenerate `docs/` from the annotations after changing API handlers:

```sh
docs/generate.sh   # from the project root; needs swaggo/swag v1.16.6
```

### Screenshots

The README screenshots and the social preview are rendered from the real `app/ui/index.html` with a mocked `/health`, `/devices` and `/activity` in headless Chromium. Re-run after visible UI changes; the social preview is uploaded by hand in the repository settings:

```sh
docker run --rm -v "$PWD":/src -w /src mcr.microsoft.com/playwright/python:v1.52.0-noble \
  sh -c 'pip install -q --break-system-packages playwright==1.52.0 && python3 docs/screenshots/capture.py'
```

## Architecture

Layering is strict: `cmd` → `app` → `app/service/*` → `pkg/*`. Lower layers never import upward.

- **`cmd/main.go`** — flags, config load/validate, logger (`xlog`), config warnings, and the **restart loop**, the same as in relayhat/smartmeter. `run()` subscribes to SIGHUP/SIGTERM/SIGINT **once** and hands that channel to every `App` — never `signal.Stop`/`Reset` it inside `app`. `checkReload` (load + validate the file) is called by the SIGHUP handler **before** tearing anything down, so a broken file is refused and the running `App` keeps going. A restart that passes the check but fails in `Run` (TLS, a port or serial line busy) falls back to `lastGood`; only a failing first start exits. The new logger replaces the old one before it is closed; a log destination that cannot be opened on a reload keeps the current logger. `cmd/README.md` is `//go:embed`-ed as `--help` output.
- **`app/app.go`** — wiring and lifecycle. `Init` registers the buses, then the devices (`mapBusConfig`, `mapDeviceConfig`), and creates one `modbusserver.Server` per configured listener; `Start` opens the bus connections in parallel (a failure is only logged, the next request retries), starts the listeners and the HTTPS server; `Cleanup` closes listeners, then the manager. A failure in `Run` goes through `abort` (cancel, `wg.Wait`, `Cleanup`), which releases ports and serial lines for the next `App`. The signal goroutine is the only caller of `shutdownProcedure`: SIGHUP → `ModeRestart` (after `checkReload`), SIGTERM/SIGINT → `ModeStop`, and a web server that stops on its own reports on `app.serverErr` → `ModeRestart`.
- **`app/config.go`** — YAML decoded with `KnownFields(true)`; only `${VAR}` is expanded. Keys of 1.x fail with a hint (`renamedKeys`, matched on yaml.v3's "field X not found in type Y"). `Validate` applies the defaults and checks buses (type with matching block, one bus per serial port or host:port), devices (bus exists, `unitId` 1-247 unique per bus, `functions` as `FunctionCode` — `FC1`…`FC16` in YAML, `cacheTTL` pointer: nil = 1s on rtu / 0 on tcp) and listeners (block present = active, `listen.rtu` not on a bus port, at least one device per active listener, `gateway.unitId` unique per listener). A listener offers the devices of the **other** transport only (`GatewayDevices`). `Warnings` reports a weak `apiKey` and a `gateway` block without its listener.
- **`pkg/modbusmanager`** — the core.
  - `bus.go`: one worker goroutine per bus owns the only simonvetter client. Two bounded queues, `writes` before `reads`, FIFO each. A job past its deadline (`MaxWait`, 10 s) is dropped without touching the bus (`ErrQueueTimeout`); a full queue is `ErrBusBusy`. Before each job `SetUnitId`. A Modbus **exception** keeps the connection (`isModbusException`); a transport error closes it, reopens and retries once.
  - `cache.go`: per bus, guarded by `bus.mu`. Entries keyed by `span` (unit, table, addr, qty); a covering entry answers a sub-range; a write invalidates every overlapping entry of the same unit and table. Identical in-flight reads are coalesced via `bus.inflight` (singleflight).
  - `manager.go`: buses and devices; `Register` enforces unique unit IDs per bus; the allowed function codes are checked here (`ErrFunctionNotAllowed`), so REST and listeners behave alike. `reads.go`/`writes.go` keep the public `Read*`/`Write*` signatures; results carry `Cached`. `DeviceStatus` keeps `LastError`/`LastErrorAt` until the next error; `BusStatus` counts the transactions that reached the bus (errors, timeouts, busy time).
  - `outcome.go`: `Outcome(err, cached)` names the result of a request ("ok", "cache", the exception, "timeout", "forbidden", ...) and its `Class`; `IsValidationError` and `IsTimeout` are shared by REST, the listeners and the activity.
- **`pkg/activity`** — `Recorder`: a ring of the last 100 transactions and the clients (source + IP; the RTU line has no address) with requests, errors, per-minute rate and targets. A client is dropped 10 min after its last request, at most 32 are kept. No values. Fed by the REST handlers (`recordREST`, Modbus endpoints only, unknown devices left out) and by the listener handlers (client IP from mbserver's `TCPFrame.RemoteAddr`).
- **`app/service/modbusserver`** — one mbserver instance per listener (`SetBroadcast(false)`, unit 1 removed, `NewUnit` per gateway unit ID) with handlers for FC1-6, 15, 16 that decode the PDU strictly, forward to the manager and record the request in the activity; `Connections` reports the open TCP connections (mbserver's `Clients`). `exception` maps manager errors: validation → IllegalDataValue, not allowed → IllegalFunction, busy → ServerDeviceBusy, device exceptions passed through, everything else → GatewayTargetDeviceFailedToRespond.
- **`app/service/modbusclient`** — the REST-facing service: request validation and JSON mapping (`values`, `dataHex`, `cached`, device status with `functions` as `["FC3"]`).
- **`app/routes.go` / `api_*.go`** — `http.ServeMux` with method patterns: `GET` reads, `PUT` writes with the start address in the path, the body picks the function code (`value` → FC5/FC6, `values` → FC15/FC16, so a single-element `values` reaches devices that accept only FC15/FC16); `web.WithAuth` (`X-API-Key`) per route, middleware `WithLogging` → `WithIPFilter` → `WithCORS`. `statusForModbusError` maps 400/403/404/502/503/504.
- **`app/api_activity.go`** — `GET /activity`: listeners, clients (merged with the open TCP connections), buses and the recent transactions, for the web page.
- **`app/ui/index.html` / `api_ui.go`** — the web page at `GET /{$}`, embedded, public (no data), CSP limited to inline script/style and this server; same header, footer, login, logo style and favicon rule as the sibling pages. It polls `/health`, `/devices` and `/activity` every 2 s and never shows register values. Screenshots in `docs/screenshots/` and `docs/social-preview.png`, rendered with sample data (see Screenshots).
- **`app/webservices.go`** — HTTPS only; the embedded dev cert is used only with `env: dev`, because its key ships in every release.

**External dependencies:** `github.com/simonvetter/modbus` (client side, TCP and RTU), `github.com/womat/mbserver` (listeners, TCP and RTU server), `github.com/womat/golib` (`web`, `xlog`). Read them in `$(go env GOMODCACHE)` when behavior is unclear.

## Conventions

- Logging is `log/slog` with key/value pairs throughout; `slog.SetDefault` is set once per lifecycle in `cmd`.
- Doc comments: every package and exported symbol is documented, in English; Swagger annotations live on the handlers.
- No "master"/"slave" wording anywhere — use client/server. The Modbus address is `unitId` (Go `UnitId`), never `deviceId`.
- Optional config blocks are switched on by their presence; there are no `enabled` flags.
- Config field docs live in `README.md` (key table and example) and `config/config.yaml` — update both when adding a config key. `cmd/README.md` is the short `--help` text.
- Commit subjects follow Conventional Commits, `type(scope): description` with an optional scope (`fix: default port 8443`, `feat(ui): …`), types `feat`, `fix`, `docu`, `chore`, `refactor`. The release changelog groups on them (`.goreleaser.yaml`).
