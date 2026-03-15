package app

import (
	"net/http"

	"github.com/womat/golib/web"
)

// HandleVersion returns the application name and version.
//
//	@Summary		Get application version and name
//	@Description	Returns the current application name and version. No authentication required.
//	@Tags			info
//	@Produce		json
//	@Success		200	{object}	object{app=string,appVersion=string}	"Application version and name"
//	@Router			/version [get]
func (app *App) HandleVersion() http.Handler {
	// Response defines the JSON structure returned by /version
	type HandleVersionResponse struct {
		App        string `json:"app"`        // Application name (MODULE)
		AppVersion string `json:"appVersion"` // Application version (VERSION)
	}

	return http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			web.Encode(w, http.StatusOK, HandleVersionResponse{App: MODULE, AppVersion: VERSION})
		})
}
