// Package app sets up HTTP routes and middleware for the application.
// It supports authentication, Swagger documentation (dev only), and monitoring endpoints.
// Routes:
// - Public routes without authentication (e.g., version)
// - Protected routes requiring API key or JWT
// - Swagger documentation (only in development) at /swagger/
// - The web page at / (public, it holds no data)
// - Health, device status, activity, and the Modbus read (GET) and write (PUT) endpoints
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

	// Public routes: the web page holds no data, its script calls the protected routes
	mux.Handle("GET /{$}", app.HandleUI())
	mux.Handle("GET /version", app.HandleVersion())

	// Protected routes
	mux.Handle("GET /health", web.WithAuth(app.HandleHealth(), webCfg))
	mux.Handle("GET /devices", web.WithAuth(app.HandleModbusListDeviceStatus(), webCfg))
	mux.Handle("GET /devices/{device}/status", web.WithAuth(app.HandleModbusGetDeviceStatus(), webCfg))
	mux.Handle("GET /activity", web.WithAuth(app.HandleActivity(), webCfg))

	// Modbus read routes
	mux.Handle("GET /devices/{device}/coils/{address}", web.WithAuth(app.HandleModbusReadCoils(), webCfg))                        // Function code 1: Read Coils
	mux.Handle("GET /devices/{device}/discrete-inputs/{address}", web.WithAuth(app.HandleModbusReadDiscreteInputs(), webCfg))     // Function code 2: Read Discrete Inputs
	mux.Handle("GET /devices/{device}/holding-registers/{address}", web.WithAuth(app.HandleModbusReadHoldingRegisters(), webCfg)) // Function code 3: Read Holding Registers
	mux.Handle("GET /devices/{device}/input-registers/{address}", web.WithAuth(app.HandleModbusReadInputRegisters(), webCfg))     // Function code 4: Read Input Registers

	// Modbus write routes: the address is the start address; the body selects the function code,
	// "value" for a single write (FC5/FC6), "values" for a multiple write (FC15/FC16).
	mux.Handle("PUT /devices/{device}/coils/{address}", web.WithAuth(app.HandleModbusWriteCoils(), webCfg))                        // Function code 5 or 15: Write Coils
	mux.Handle("PUT /devices/{device}/holding-registers/{address}", web.WithAuth(app.HandleModbusWriteHoldingRegisters(), webCfg)) // Function code 6 or 16: Write Holding Registers

	// Apply global middleware: CORS + IP filter
	// CORS advertises only the methods the API serves: GET for reads, PUT for writes and the
	// preflight OPTIONS.
	handler := web.WithCORS(mux, web.WithAllowedMethods(http.MethodGet, http.MethodPut, http.MethodOptions))
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
