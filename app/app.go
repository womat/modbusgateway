// Package app provides the main application.
//
// It registers the Modbus buses and devices, starts the Modbus listeners and the HTTPS API,
// and handles OS signals for graceful shutdowns or restarts.
//
// Usage:
//
//	config := LoadConfig()
//	app := app.New(config, "/opt/modbusgateway")
//	app.Run()
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"syscall"

	modbusclientservice "github.com/womat/modbusgateway/app/service/modbusclient"
	modbusserverservice "github.com/womat/modbusgateway/app/service/modbusserver"
	"github.com/womat/modbusgateway/pkg/modbusmanager"
)

// VERSION is the application version, following semantic versioning
// as described in https://semver.org/.
//
// It is not maintained in source: the Git tag is the single source of truth and
// the value is injected at build time via -ldflags (see Makefile and
// .goreleaser.yaml). The "dev" default applies to builds made without them.
var VERSION = "dev"

const (
	MODULE = "modbusgateway"

	ModeStop    = 0
	ModeRestart = 1
)

// App is the main application struct.
// App is where the application is wired up.
type App struct {
	wg            sync.WaitGroup // wait group to track running webserver
	config        *Config        // app configuration
	web           *http.Server   // HTTP server
	modbusClient  *modbusclientservice.Service
	modbusMgr     *modbusmanager.Manager
	modbusServers []*modbusserverservice.Server
	signals       <-chan os.Signal // OS signals, subscribed once by the caller for all lifecycles
	checkReload   func() error     // loads and validates the config file before a SIGHUP restart
	serverErr     chan error       // reports a web server that stopped on its own
	restart       chan struct{}    // signals application restart
	shutdown      chan struct{}    // signals application shutdown
	ctx           context.Context
	cancelFunc    context.CancelFunc

	// add your additional handler here
}

// New initializes the App struct but does not start services.
//
// signals must already be subscribed (signal.Notify) to SIGHUP, SIGTERM and SIGINT, and stay
// subscribed across restarts, so a signal between two lifecycles waits for the next App instead
// of ending the process. checkReload is called on SIGHUP before anything is torn down; if it
// reports an error, the App keeps running with its configuration. It may be nil.
func New(config *Config, signals <-chan os.Signal, checkReload func() error) *App {
	ctx, cancel := context.WithCancel(context.Background())

	return &App{
		config: config,
		web: &http.Server{
			Addr: net.JoinHostPort(config.Webserver.ListenHost, strconv.Itoa(config.Webserver.ListenPort)),
		},

		signals:     signals,
		checkReload: checkReload,
		serverErr:   make(chan error, 1),
		restart:     make(chan struct{}),
		shutdown:    make(chan struct{}),
		ctx:         ctx,
		cancelFunc:  cancel,
	}
}

// Run initializes the application, starts runtime services,
// and sets up OS signal handling.
func (app *App) Run() (*App, error) {
	slog.Info("Initializing application")

	if err := app.Init(); err != nil {
		return app, app.abort(err)
	}

	// handle the OS signals
	app.HandleOSSignals()

	slog.Info("Starting application")
	if err := app.Start(); err != nil {
		slog.Error("Application failed to start", "error", err)
		return app, app.abort(err)
	}

	slog.Info("Module started successfully",
		"module", MODULE,
		"version", VERSION,
		"pid", os.Getpid(),
	)
	return app, nil
}

// Init prepares the application:
// - initializes services and runtime objects
// - initializes API routes
func (app *App) Init() (err error) {

	// Register the buses first, then the devices on them, before serving requests.
	app.modbusMgr = modbusmanager.New()
	for _, name := range sortedKeys(app.config.Buses) {
		if err := app.modbusMgr.RegisterBus(mapBusConfig(name, app.config.Buses[name])); err != nil {
			_ = app.modbusMgr.Close()
			return fmt.Errorf("register modbus bus %q: %w", name, err)
		}
	}
	for _, name := range sortedKeys(app.config.Devices) {
		if err := app.modbusMgr.Register(mapDeviceConfig(name, app.config.Devices[name])); err != nil {
			_ = app.modbusMgr.Close()
			return fmt.Errorf("register modbus device %q: %w", name, err)
		}
	}
	app.modbusClient = modbusclientservice.New(app.modbusMgr)

	for _, listener := range []string{"tcp", "rtu"} {
		if !app.config.listenerActive(listener) {
			continue
		}
		devices, err := app.config.GatewayDevices(listener)
		if err != nil {
			_ = app.modbusMgr.Close()
			return err
		}
		app.modbusServers = append(app.modbusServers,
			modbusserverservice.New(mapListener(listener, app.config.Listen), app.modbusMgr, devices))
	}

	// initRoutes should always be called at the end
	slog.Debug("Initializing API routes")
	app.SetupRoutes()

	return nil
}

// Start starts runtime services after Init completed successfully.
func (app *App) Start() error {
	if app.modbusMgr == nil {
		return modbusmanager.ErrManagerNotInitialized
	}

	for bus, err := range app.modbusMgr.Connect() {
		slog.Warn("Initial Modbus connect failed; will retry on the next request", "bus", bus, "error", err)
	}

	for _, server := range app.modbusServers {
		if err := server.Start(app.ctx); err != nil {
			app.closeModbusServers()
			return fmt.Errorf("start modbus server: %w", err)
		}
	}

	slog.Info("Starting web server", "url", app.web.Addr)
	if err := app.StartWebServer(); err != nil {
		app.closeModbusServers()
		return fmt.Errorf("start web server: %w", err)
	}

	return nil
}

// closeModbusServers stops the Modbus listeners and returns their errors.
func (app *App) closeModbusServers() error {
	var errs error
	for _, server := range app.modbusServers {
		if err := server.Close(); err != nil {
			errs = errors.Join(errs, fmt.Errorf("close modbus server: %w", err))
		}
	}
	return errs
}

// Restart returns a read-only channel for restart signals.
func (app *App) Restart() <-chan struct{} {
	return app.restart
}

// Shutdown returns a read-only channel for shutdown signals.
func (app *App) Shutdown() <-chan struct{} {
	return app.shutdown
}

// abort undoes a failed Run: it stops the signal handler and the web server and releases the
// bus connections, serial ports and listen ports, so the caller can start another App - e.g.
// with the previous configuration - on the same resources.
func (app *App) abort(err error) error {
	app.cancelFunc()
	app.wg.Wait()
	if cleanupErr := app.Cleanup(); cleanupErr != nil {
		slog.Error("Cleanup after a failed start failed", "error", cleanupErr)
	}
	return err
}

// HandleOSSignals handles SIGHUP (restart), SIGTERM and SIGINT (stop) from app.signals, and a
// web server that stopped on its own (restart).
func (app *App) HandleOSSignals() {

	go func() {
		slog.Debug("Starting signal handler")

		// Use select instead of a plain channel receive so the goroutine has
		// two exit paths and always terminates cleanly:
		//   - a signal or a server error is received and handled, or
		//   - the context is cancelled externally (e.g. from a failed start).
		// Without the second path the goroutine would outlive its App and take
		// the next signal away from the App that replaced it. The loop only
		// continues after a SIGHUP whose config was rejected.
		for {
			select {
			case receivedSignal := <-app.signals:
				slog.Info("Received OS signal", "signal", receivedSignal)
				switch receivedSignal {
				case syscall.SIGHUP:
					if app.checkReload != nil {
						if err := app.checkReload(); err != nil {
							slog.Error("Config reload rejected, keeping the running configuration", "error", err)
							continue
						}
					}
					slog.Info("SIGHUP received, initiating restart")
					app.shutdownProcedure(ModeRestart)
				case syscall.SIGTERM, syscall.SIGINT:
					slog.Info("SIGTERM/SIGINT received, stopping")
					app.shutdownProcedure(ModeStop)
				}
				return
			case err := <-app.serverErr:
				slog.Error("Web server stopped unexpectedly, initiating restart", "error", err)
				app.shutdownProcedure(ModeRestart)
				return
			case <-app.ctx.Done():
				// Context was cancelled externally – exit without triggering
				// a second shutdown procedure.
				slog.Debug("Signal handler: context cancelled, exiting goroutine")
				return
			}
		}
	}()
}

// shutdownProcedure gracefully stops or restarts the app based on mode.
//   - ModeStop: graceful shutdown the web server, Cleanup app resources and exit the application.
//   - ModeRestart: graceful shutdown the web server and Cleanup app resources and restart the application.
func (app *App) shutdownProcedure(mode int) {
	slog.Info("Initiating shutdown", "mode", mode)

	// cancel the application context to stop all running goroutines
	app.cancelFunc()
	app.wg.Wait() //wait for the web server to shutdown before cleaning up resources

	if err := app.Cleanup(); err != nil {
		slog.Error("Cleanup failed", "error", err)
	}

	switch mode {
	case ModeRestart:
		slog.Info("Shutdown complete, restarting")
		app.restart <- struct{}{}
		// Channels are intentionally left open: cmd/main.go receives the restart
		// signal and calls New(), which creates fresh channels for the next lifecycle.
	case ModeStop:
		slog.Info("Module stopped", "module", MODULE, "version", VERSION, "pid", os.Getpid())
		app.shutdown <- struct{}{}
		close(app.shutdown)
	}

}

// Cleanup releases application resources.
// It's called when the application is shutdown or restarted.
// Should be used to free up resources.
func (app *App) Cleanup() error {
	var errs error

	errs = errors.Join(errs, app.closeModbusServers())
	if app.modbusMgr != nil {
		if err := app.modbusMgr.Close(); err != nil {
			errs = errors.Join(errs, fmt.Errorf("close modbus manager: %w", err))
		}
	}

	return errs
}
func mapBusConfig(name string, bus BusConfig) modbusmanager.BusConfig {
	cfg := modbusmanager.BusConfig{
		Name:      name,
		Type:      bus.Type,
		Timeout:   bus.Timeout,
		QueueSize: bus.QueueSize,
	}
	if bus.TCP != nil {
		cfg.TCP = &modbusmanager.TCPConfig{Host: bus.TCP.Host, Port: bus.TCP.Port}
	}
	if bus.RTU != nil {
		cfg.Serial = &modbusmanager.SerialConfig{
			Port:     bus.RTU.Port,
			BaudRate: bus.RTU.BaudRate,
			DataBits: bus.RTU.DataBits,
			Parity:   bus.RTU.Parity,
			StopBits: bus.RTU.StopBits,
		}
	}
	return cfg
}

func mapDeviceConfig(name string, device DeviceConfig) modbusmanager.DeviceConfig {
	cfg := modbusmanager.DeviceConfig{
		Name:        name,
		Description: device.Description,
		Bus:         device.Bus,
		UnitId:      device.UnitId,
	}
	if device.CacheTTL != nil {
		cfg.CacheTTL = *device.CacheTTL
	}
	for _, fc := range device.Functions {
		cfg.Functions = append(cfg.Functions, uint8(fc))
	}
	return cfg
}

func mapListener(listener string, listen ListenConfig) modbusserverservice.Listener {
	if listener == "tcp" {
		return modbusserverservice.Listener{TCP: &modbusserverservice.TCPListener{Host: listen.TCP.Host, Port: listen.TCP.Port}}
	}
	rtu := listen.RTU
	return modbusserverservice.Listener{RTU: &modbusserverservice.RTUListener{
		Port:            rtu.Port,
		BaudRate:        rtu.BaudRate,
		DataBits:        rtu.DataBits,
		Parity:          rtu.Parity,
		StopBits:        rtu.StopBits,
		InterFrameDelay: rtu.InterFrameDelay,
	}}
}
