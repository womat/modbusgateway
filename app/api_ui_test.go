package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebPage(t *testing.T) {
	_, h := newTestApp(t)

	// Without a token: the page holds no data.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>modbusgateway</title>") {
		t.Fatalf("GET / = %d, want the web page without a token", rec.Code)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self'") {
		t.Errorf("Content-Security-Policy = %q, want requests limited to this server", csp)
	}

	// The page must not catch unknown paths. No "OPTIONS /" route (WithCORS answers preflights),
	// so an unknown path is a 404, not 405.
	if code, _ := request(t, h, http.MethodGet, "/nonexistent", ""); code != http.StatusNotFound {
		t.Errorf("GET /nonexistent = %d, want 404", code)
	}
}

// A preflight needs no token and advertises the methods the API serves only.
func TestCORSAdvertisesUsedMethodsOnly(t *testing.T) {
	_, h := newTestApp(t)

	// A preflight is an OPTIONS request with Access-Control-Request-Method; WithCORS answers it
	// before auth and the mux.
	req := httptest.NewRequest(http.MethodOptions, "/devices/pump/holding-registers/0", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", http.MethodPut)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight OPTIONS /devices/pump/holding-registers/0 = %d, want 204 without a token", rec.Code)
	}
	if got, want := rec.Header().Get("Access-Control-Allow-Methods"), "GET, PUT, OPTIONS"; got != want {
		t.Errorf("Access-Control-Allow-Methods = %q, want %q", got, want)
	}
}

// The page hides the banner and other elements with the hidden attribute; a class that sets
// display would override it without this rule.
func TestWebPageHidesHiddenElements(t *testing.T) {
	if !strings.Contains(string(uiPage), "[hidden] { display: none !important; }") {
		t.Error("app/ui/index.html lacks the [hidden] rule: an empty banner would show")
	}
}
