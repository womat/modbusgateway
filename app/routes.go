// Package app sets up HTTP routes and middleware for the application.
// It supports authentication, Swagger documentation (dev only), and monitoring endpoints.
// Routes:
// - Public routes without authentication (e.g., version)
// - Protected routes requiring API key or JWT
// - Swagger documentation (only in development) at /swagger/
// - Health, Live, Ready, Monitoring, and S0 data endpoints
//
// Middleware applied:
// - CORS
// - IP filtering (allowed/blocked IPs)
//
// This must be called during app startup before starting the HTTP server.
package app

import (
	"log/slog"
	"net/http"

	"github.com/womat/golib/web"
)

// SetupRoutes configures all HTTP routes and global middleware for the application.
func (app *App) SetupRoutes() {
	webCfg := web.Config{
		ApiKey:    app.config.Webserver.ApiKey,
		JwtSecret: app.config.Webserver.JwtSecret,
		JwtID:     app.config.Webserver.JwtID,
		AppName:   MODULE,
	}

	mux := http.NewServeMux()

	// Preflight CORS requests
	mux.Handle("OPTIONS /", web.HandlePreflight())

	// Dev-only Swagger documentation (only registered with -tags swagger)
	app.registerSwaggerRoute(mux)

	// Public routes
	mux.Handle("GET /version", app.HandleVersion())

	// Protected routes
	mux.Handle("GET /health", web.WithAuth(app.HandleHealth(), webCfg))
	mux.Handle("GET /devices", web.WithAuth(app.HandleModbusListDeviceStatus(), webCfg))
	mux.Handle("GET /devices/{device}/status", web.WithAuth(app.HandleModbusGetDeviceStatus(), webCfg))

	// Modbus read routes
	mux.Handle("GET /devices/{device}/coils/{address}", web.WithAuth(app.HandleModbusReadCoils(), webCfg))                        // Function code 1: Read Coils
	mux.Handle("GET /devices/{device}/discrete-inputs/{address}", web.WithAuth(app.HandleModbusReadDiscreteInputs(), webCfg))     // Function code 2: Read Discrete Inputs
	mux.Handle("GET /devices/{device}/holding-registers/{address}", web.WithAuth(app.HandleModbusReadHoldingRegisters(), webCfg)) // Function code 3: Read Holding Registers
	mux.Handle("GET /devices/{device}/input-registers/{address}", web.WithAuth(app.HandleModbusReadInputRegisters(), webCfg))     // Function code 4: Read Input Registers

	// Modbus write routes
	mux.Handle("POST /devices/{device}/coils/{address}", web.WithAuth(app.HandleModbusWriteSingleCoil(), webCfg))                 // Function code 5: Write Single Coil
	mux.Handle("POST /devices/{device}/holding-registers/{address}", web.WithAuth(app.HandleModbusWriteSingleRegister(), webCfg)) // Function code 6: Write Single Register
	mux.Handle("POST /devices/{device}/coils", web.WithAuth(app.HandleModbusWriteMultipleCoils(), webCfg))                        // Function code 15: Write Multiple Coils
	mux.Handle("POST /devices/{device}/holding-registers", web.WithAuth(app.HandleModbusWriteMultipleRegisters(), webCfg))        // Function code 16: Write Multiple Registers

	// Apply global middleware: CORS + IP filter
	handler := web.WithCORS(mux)
	handler = web.WithIPFilter(handler, app.config.Webserver.AllowedIPs, app.config.Webserver.BlockedIPs)
	handler = WithLogging(handler)
	app.web.Handler = handler
}

func WithLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Debug("Incoming web request",
			"method", r.Method,
			"path", r.URL.Path,
			"client_ip", r.RemoteAddr)
		next.ServeHTTP(w, r)
	})
}
