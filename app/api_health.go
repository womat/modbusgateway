// HTTP handler for /health: runtime metrics. There is no readiness route; the state of the
// devices is in /devices.

package app

import (
	"net/http"

	"github.com/womat/golib/v2/web"
	"github.com/womat/modbusgateway/app/service/health"
)

// HandleHealth returns the current health data of the application.
//
//	@Summary		Get health data
//	@Description	Retrieves memory usage, goroutine count, version, hostname, Go runtime version, and OS.
//	@Tags			info
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	health.Model	"Health data successfully retrieved"
//	@Failure		401	{string}	string			"Unauthorized"
//	@Router			/health [get]
func (app *App) HandleHealth() http.Handler {
	return http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			resp := health.GetCurrentHealth(MODULE, VERSION)
			web.Encode(w, http.StatusOK, resp)
		},
	)
}
