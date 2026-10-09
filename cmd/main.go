// Package main provides the entry point for the modbusgateway application.
//
// This program initializes logging, loads configuration, handles command-line flags,
// and starts the main application loop. It supports hot reloads of the config
// and provides about/version/help output.
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/womat/golib/xlog"
	"github.com/womat/modbusgateway/app"
	"gopkg.in/yaml.v3"
)

// Readme embeds the README.md file for the --help flag
//
//go:embed README.md
var Readme string

// buildDate and buildCommit are injected at build time via -ldflags
var (
	buildDate   = "dev"
	buildCommit = "none"
)

// @securityDefinitions.apikey	ApiKeyAuth
// @in							header
// @name						X-API-Key
func main() {
	// Parse command line flags.
	flags := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	flags.SetOutput(os.Stdout)

	about := flags.Bool("about", false, "Print app details and exit")
	help := flags.Bool("help", false, "Print a help message and exit")
	version := flags.Bool("version", false, "Print the app version and exit")
	debug := flags.Bool("debug", false, "Enable debug logging to stdout (overrides log settings from the config file)")
	configFile := flags.String("config", filepath.Join("/opt", app.MODULE, "etc", "config.yaml"), "Specify the path to the config file")

	if envCfg := os.Getenv("CONFIG_FILE"); envCfg != "" {
		*configFile = envCfg
	}

	if err := flags.Parse(os.Args[1:]); err != nil {
		fmt.Println("Error parsing flags:", err)
		flags.Usage()
		os.Exit(1)
	}

	switch {
	case *about:
		fmt.Println(About())
		os.Exit(0)
	case *version:
		fmt.Println(app.VERSION)
		os.Exit(0)
	case *help:
		fmt.Println(Readme)
		os.Exit(0)
	}

	// Run main application loop
	os.Exit(run(*configFile, *debug))
}

// run initializes configuration, logging, and the application loop.
// It supports hot-reloading of the configuration and handles graceful shutdown.
func run(configFile string, debug bool) int {

	// logger is the current logger; it is replaced, and the previous one closed, on a reload.
	var logger *xlog.LoggerWrapper
	defer func() {
		if logger != nil {
			_ = logger.Close()
		}
	}()

	fmt.Printf("Starting %s %s\n", app.MODULE, app.VERSION)
	fmt.Printf("Loading configuration from: %s\n", configFile)

	// Subscribe once for the whole process, not per App: between two lifecycles no App is
	// listening, and without a subscription a SIGTERM or a second SIGHUP in that gap would end
	// the process with the default action. Here the signal waits in the buffer and the next App
	// handles it.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)

	config, err := loadConfig(configFile, debug)
	if err != nil {
		fmt.Printf("Failed to load config file %s: %s\n", configFile, err.Error())
		return 1
	}

	// lastGood is the configuration the last App ran with. A restart whose new configuration
	// passes the check but still fails to start (TLS certificate missing, a port or a serial
	// line busy) falls back to it, so a reload never leaves the devices unreachable.
	var lastGood *app.Config

	for {
		// Switch to the new logger before closing the previous one, so no line written in
		// between goes to a file that is already closed. A log destination that cannot be
		// opened on a reload keeps the current logger.
		newLogger, err := xlog.Init(config.LogDestination, config.LogLevel)
		switch {
		case err != nil && lastGood == nil:
			fmt.Printf("Failed to initialize logger: %s\n", err.Error())
			return 1
		case err != nil:
			slog.Error("Failed to initialize logger, keeping the current one", "error", err)
		default:
			slog.SetDefault(newLogger.Logger)
			if logger != nil {
				_ = logger.Close()
			}
			logger = newLogger
			slog.Info("Logging initialized/reloaded", "logLevel", config.LogLevel)
		}

		for _, warning := range config.Warnings() {
			slog.Warn("Configuration warning", "warning", warning)
		}

		// A SIGHUP restart only goes ahead when the config file still loads and validates;
		// otherwise the running App keeps going with its current configuration.
		checkReload := func() error {
			_, err := loadConfig(configFile, debug)
			return err
		}

		// Create and run the application. A failed Run has released its ports and buses.
		a, err := app.New(config, signals, checkReload).Run()
		if err != nil {
			if lastGood == nil || config == lastGood {
				slog.Error("Critical error occurred, shutting down", "error", err)
				return 1
			}
			slog.Error("Start with the new configuration failed, continuing with the previous one", "error", err)
			config = lastGood
			continue
		}
		lastGood = config

		// Wait for restart or shutdown signals
		select {
		case <-a.Restart():
			slog.Info("Reloading configuration", "configFile", configFile)
			time.Sleep(time.Second) // prevent tight restart loops
			// The file was checked before the restart, but it may have changed since.
			if config, err = loadConfig(configFile, debug); err != nil {
				slog.Error("Config reload failed, continuing with the previous configuration", "error", err)
				config = lastGood
			}
		case <-a.Shutdown():
			slog.Info("Shutdown requested")
			return 0
		}
	}
}

func About() string {
	info := map[string]string{
		"Author":   "Wolfgang Mathe",
		"Binary":   filepath.Join("/opt", app.MODULE, "bin", app.MODULE),
		"Date":     buildDate,
		"Commit":   buildCommit,
		"Desc":     "Modbus devices on the network: HTTPS API and Modbus listeners",
		"Help":     filepath.Join("/opt", app.MODULE, "bin", app.MODULE) + " --help",
		"Main":     filepath.Join("/opt/src", app.MODULE, "cmd", app.MODULE, "main.go"),
		"ProgLang": runtime.Version(),
		"Repo":     "https://github.com/womat/ModbusGateway",
		"Version":  app.VERSION,
	}

	b, err := yaml.Marshal(info)
	if err != nil {
		return fmt.Sprintf("Failed to marshal About info: %v", err)
	}
	return string(b)
}

// loadConfig loads the configuration from a YAML file, applies overrides and validates the configuration values.
//
// If debug mode is enabled, log level is forced to "debug" and logs are written to stdout.
func loadConfig(configFile string, debug bool) (*app.Config, error) {

	config, err := app.LoadConfig(configFile)
	if err != nil {
		return nil, err
	}

	if debug {
		config.LogLevel = "debug"
		config.LogDestination = "stdout"
	}

	if err = config.Validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return config, nil
}
