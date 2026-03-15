// Package app provides the main application.
//
// It initializes S0 meters, handles MQTT publishing, periodic backups,
// web server startup, and OS signal handling for graceful shutdowns
// or restarts.
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
	"os/signal"
	"strconv"
	"sync"
	"syscall"

	modbusclientservice "github.com/womat/modbusgateway/app/service/modbusclient"
	modbusserverservice "github.com/womat/modbusgateway/app/service/modbusserver"
	"github.com/womat/modbusgateway/pkg/modbusmanager"
)

// VERSION holds the version information with the following logic in mind
//
//	4 ... fixed
//	0 ... year 2020, 1->year 2021, etc.
//	7 ... month of year (7=July)
//	the date format after the + is always the first of the month
//
// VERSION differs from semantic versioning as described in https://semver.org/
// but we keep the correct syntax.
// TODO: increase version number
const (
	VERSION = "1.6.2+20260228"
	MODULE  = "modbusgateway"

	ModeStop    = 0
	ModeRestart = 1
)

// App is the main application struct.
// App is where the application is wired up.
type App struct {
	wg           sync.WaitGroup // wait group to track running webserver
	baseDir      string         // working directory
	config       *Config        // app configuration
	web          *http.Server   // HTTP server
	modbusClient *modbusclientservice.Service
	modbusMgr    *modbusmanager.Manager
	modbusServer *modbusserverservice.Server
	restart      chan struct{} // signals application restart
	shutdown     chan struct{} // signals application shutdown
	ctx          context.Context
	cancelFunc   context.CancelFunc

	// add your additional handler here
}

// New initializes the App struct but does not start services.
func New(config *Config, baseDir string) *App {
	ctx, cancel := context.WithCancel(context.Background())

	return &App{
		baseDir: baseDir,
		config:  config,
		web: &http.Server{
			Addr: net.JoinHostPort(config.Webserver.ListenHost, strconv.Itoa(config.Webserver.ListenPort)),
		},

		restart:    make(chan struct{}),
		shutdown:   make(chan struct{}),
		ctx:        ctx,
		cancelFunc: cancel,
	}
}

// Run initializes the application, starts runtime services,
// and sets up OS signal handling.
func (app *App) Run() (*App, error) {
	slog.Info("Initializing application")

	if err := app.Init(); err != nil {
		return app, err
	}

	// handle the OS signals
	app.HandleOSSignals()

	slog.Info("Starting application")
	if err := app.Start(); err != nil {
		slog.Error("Application failed to start", "error", err)
		return app, err
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

	// initialize managed Modbus connections before serving requests
	app.modbusMgr = modbusmanager.New()
	deviceMap := make(map[uint8]string)
	for name, device := range app.config.Devices {
		cfg := mapManagedDeviceConfig(name, device)
		if err := app.modbusMgr.Register(cfg); err != nil {
			_ = app.modbusMgr.Close()
			return fmt.Errorf("register modbus device %q: %w", cfg.Name, err)
		}
		if device.Enabled && device.GatewayDeviceID != 0 {
			deviceMap[device.GatewayDeviceID] = name
		}
	}
	app.modbusClient = modbusclientservice.New(app.modbusMgr)
	if app.config.ModbusServer.Enabled {
		app.modbusServer = modbusserverservice.New(
			app.config.ModbusServer.ListenHost,
			app.config.ModbusServer.ListenPort,
			app.modbusMgr,
			deviceMap,
		)
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

	for name, device := range app.config.Devices {
		if !device.Enabled {
			continue
		}

		if err := app.modbusMgr.Connect(name); err != nil {
			slog.Warn("Initial Modbus connect failed; will retry on first request",
				"device", name,
				"error", err,
			)
		}
	}

	if app.modbusServer != nil {
		if err := app.modbusServer.Start(); err != nil {
			return fmt.Errorf("start modbus TCP server: %w", err)
		}
	}

	slog.Info("Starting web server", "url", app.web.Addr)
	if err := app.StartWebServer(); err != nil {
		if app.modbusServer != nil {
			_ = app.modbusServer.Close()
		}
		return fmt.Errorf("start web server: %w", err)
	}

	return nil
}

// Restart returns a read-only channel for restart signals.
func (app *App) Restart() <-chan struct{} {
	return app.restart
}

// Shutdown returns a read-only channel for shutdown signals.
func (app *App) Shutdown() <-chan struct{} {
	return app.shutdown
}

// HandleOSSignals listens for SIGHUP, SIGTERM, and SIGINT signals.
func (app *App) HandleOSSignals() {

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
		defer signal.Stop(sig) // Cleanup: rollback signal.Notify

		slog.Debug("Starting signal handler")

		// Use select instead of a plain channel receive so the goroutine has
		// two exit paths and always terminates cleanly:
		//   - a signal is received and handled, or
		//   - the context is cancelled externally (e.g. from a concurrent shutdown).
		// Without this, the goroutine would block forever after signal.Reset()
		// on a SIGHUP restart, leaking one goroutine per reload cycle.
		select {
		case receivedSignal := <-sig:
			slog.Info("Received OS signal", "signal", receivedSignal)
			switch receivedSignal {
			case syscall.SIGHUP:
				slog.Info("SIGHUP received, initiating restart")
				app.shutdownProcedure(ModeRestart)
			case syscall.SIGTERM, syscall.SIGINT:
				slog.Info("SIGTERM/SIGINT received, stopping")
				app.shutdownProcedure(ModeStop)
			}
		case <-app.ctx.Done():
			// Context was cancelled externally – exit without triggering
			// a second shutdown procedure.
			slog.Debug("Signal handler: context cancelled, exiting goroutine")
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

	if app.modbusServer != nil {
		if err := app.modbusServer.Close(); err != nil {
			errs = errors.Join(errs, fmt.Errorf("close modbus TCP server: %w", err))
		}
	}
	if app.modbusMgr != nil {
		if err := app.modbusMgr.Close(); err != nil {
			errs = errors.Join(errs, fmt.Errorf("close modbus manager: %w", err))
		}
	}

	return errs
}
func mapManagedDeviceConfig(name string, device DeviceConfig) modbusmanager.DeviceConfig {
	cfg := modbusmanager.DeviceConfig{
		Name:        name,
		Enabled:     device.Enabled,
		Description: device.Description,
		Transport:   device.Transport,
		DeviceID:    device.DeviceID,
		Timeout:     device.Timeout,
	}

	if device.TCP != nil {
		cfg.TCP = &modbusmanager.TCPConfig{
			Host: device.TCP.Host,
			Port: device.TCP.Port,
		}
	}

	if device.Serial != nil {
		cfg.Serial = &modbusmanager.SerialConfig{
			Port:     device.Serial.Port,
			BaudRate: device.Serial.BaudRate,
			DataBits: device.Serial.DataBits,
			Parity:   device.Serial.Parity,
			StopBits: device.Serial.StopBits,
		}
	}

	return cfg
}
