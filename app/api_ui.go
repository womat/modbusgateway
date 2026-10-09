package app

// The web page at GET /: a single HTML file embedded in the binary. It is public because it
// contains no data; its script asks for the API key and reads /health, /devices and /activity.

import (
	_ "embed"
	"net/http"
)

//go:embed ui/index.html
var uiPage []byte

// uiCSP keeps the page to its own inline script and style, its inline favicon and requests back
// to this server.
const uiCSP = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; " +
	"connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// HandleUI serves the web page.
func (app *App) HandleUI() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		h.Set("Content-Security-Policy", uiCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(uiPage)
	})
}
