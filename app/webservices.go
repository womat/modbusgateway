package app

import (
	"context"
	"crypto/tls"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"
)

const (
	defaultReadTimeout  = 10 * time.Second
	defaultWriteTimeout = 15 * time.Second
	defaultIdleTimeout  = 120 * time.Second
)

// to generate new self-signed certs for development, run:
// openssl req -x509 -newkey rsa:4096 -keyout dev_key.pem -out dev_cert.pem -days 365 -nodes -subj "/CN=localhost"
// mkdir -p certs
// cd certs
//
// # 1. generate Private Key
// openssl genrsa -out dev_key.pem 2048
//
// # 2. generate Self-signed Certificate
// openssl req -new -x509 -key dev_key.pem -out dev_cert.pem -days 365 -subj "/C=AT/ST=Vienna/L=Vienna/O=Dev/OU=Dev/CN=localhost"

//go:embed certs/dev_cert.pem
var embeddedCertFile []byte

//go:embed certs/dev_key.pem
var embeddedKeyFile []byte

// StartWebServer initializes and starts the HTTPS server asynchronously.
// - Production: uses file-based cert/key from config
// - Development fallback: embedded self-signed cert
// - Non-blocking: runs in goroutine
// - Graceful shutdown on app.ctx cancellation
func (app *App) StartWebServer() error {
	// Set default timeouts
	app.web.ReadTimeout = defaultReadTimeout
	app.web.WriteTimeout = defaultWriteTimeout
	app.web.IdleTimeout = defaultIdleTimeout

	// Load TLS certificate
	cert, err := loadTLSCert(app.config.Webserver.CertFile, app.config.Webserver.KeyFile)
	if err != nil {
		return fmt.Errorf("failed to load TLS certificate: %w", err)
	}

	// Assign TLS config
	app.web.TLSConfig = &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12}

	// Create listener
	listener, err := net.Listen("tcp", app.web.Addr)
	if err != nil {
		return fmt.Errorf("failed to create listener: %w", err)
	}

	// Channel to report server runtime errors
	serverErrCh := make(chan error, 1)

	go func() {
		// ServeTLS blocks until Shutdown is called
		err := app.web.ServeTLS(listener, "", "")
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrCh <- err
		}

		// Close the listener when ServeTLS exits
		if err := listener.Close(); err != nil {
			slog.Error("Failed to close listener", "error", err)
		}
	}()

	// Goroutine to monitor runtime errors and handle shutdown
	app.wg.Add(1) // before shutdown
	go func() {
		defer app.wg.Done() // after shutdown

		select {
		case err := <-serverErrCh:
			slog.Error("Webserver runtime error", "error", err)
			// Optional: trigger restart or shutdown here
			// app.shutdownProcedure(ModeRestart)
		case <-app.ctx.Done():
			ctxShutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := app.web.Shutdown(ctxShutdown); err != nil {
				slog.Error("Failed to shutdown webserver gracefully", "error", err)
			} else {
				slog.Info("Webserver stopped gracefully")
			}
		}
	}()

	return nil
}

// loadTLSCert tries to load a file-based cert, falls back to embedded certs if missing
func loadTLSCert(certFile, keyFile string) (tls.Certificate, error) {
	if _, err := os.Stat(certFile); err == nil {
		// Production cert
		return tls.LoadX509KeyPair(certFile, keyFile)
	} else if errors.Is(err, os.ErrNotExist) {
		// Dev fallback
		slog.Warn("TLS cert file not found, using embedded fallback")
		return tls.X509KeyPair(embeddedCertFile, embeddedKeyFile)
	} else {
		return tls.Certificate{}, fmt.Errorf("failed to read cert file: %w", err)
	}
}
