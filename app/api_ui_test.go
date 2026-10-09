package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebPage(t *testing.T) {
	_, h := newTestApp(t)

	// Without an API key: the page holds no data.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>modbusgateway</title>") {
		t.Fatalf("GET / = %d, want the web page without an API key", rec.Code)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self'") {
		t.Errorf("Content-Security-Policy = %q, want requests limited to this server", csp)
	}

	// The page must not catch unknown paths.
	if code, _ := request(t, h, http.MethodGet, "/nonexistent", ""); code == http.StatusOK {
		t.Errorf("GET /nonexistent = %d, want an error", code)
	}
}

// The page hides the banner and other elements with the hidden attribute; a class that sets
// display would override it without this rule.
func TestWebPageHidesHiddenElements(t *testing.T) {
	if !strings.Contains(string(uiPage), "[hidden] { display: none !important; }") {
		t.Error("app/ui/index.html lacks the [hidden] rule: an empty banner would show")
	}
}
