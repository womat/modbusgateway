//go:build !swagger

package app

import "net/http"

func (app *App) registerSwaggerRoute(mux *http.ServeMux) {}
