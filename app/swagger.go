//go:build swagger

package app

import (
	"net/http"

	_ "github.com/womat/modbusgateway/docs"

	httpSwagger "github.com/swaggo/http-swagger"
)

const PathSwagger = "/swagger/"

func (app *App) registerSwaggerRoute(mux *http.ServeMux) {
	mux.Handle("GET "+PathSwagger, httpSwagger.Handler(
		httpSwagger.PersistAuthorization(true),
	))
}
