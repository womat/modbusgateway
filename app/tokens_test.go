package app

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/womat/golib/web"
)

// writeTokenSetup writes a config naming tokenFile and, unless tokens is empty, the token file
// next to it. It returns the path of the config.
func writeTokenSetup(t *testing.T, tokenFile, tokens string) string {
	t.Helper()
	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(config, []byte("webserver:\n  tokenFile: "+tokenFile+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if tokens != "" {
		if err := os.WriteFile(filepath.Join(dir, "tokens"), []byte(tokens), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return config
}

func tokenLine(name, token string) string {
	sum := sha256.Sum256([]byte(token))
	return name + " " + hex.EncodeToString(sum[:]) + "\n"
}

// A relative tokenFile is relative to the config file, not to the working directory, so the
// service and "modbusgateway token" agree on the file.
func TestLoadConfigReadsTokenFileNextToConfig(t *testing.T) {
	config := writeTokenSetup(t, "tokens", "# callers\n"+tokenLine("nodered", "secret"))

	c, err := LoadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(filepath.Dir(config), "tokens"); c.Webserver.TokenFile != want {
		t.Errorf("TokenFile = %q, want %q", c.Webserver.TokenFile, want)
	}
	if len(c.Webserver.Tokens) != 1 || c.Webserver.Tokens[0].Name != "nodered" {
		t.Errorf("Tokens = %+v", c.Webserver.Tokens)
	}
}

// A missing or broken token file stops LoadConfig - at start and on a SIGHUP reload, where the
// running service then keeps its tokens.
func TestLoadConfigRefusesMissingOrBrokenTokenFile(t *testing.T) {
	for name, tokens := range map[string]string{
		"missing": "",
		"broken":  "nodered not-a-hash\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadConfig(writeTokenSetup(t, "tokens", tokens)); err == nil {
				t.Error("LoadConfig accepted it")
			}
		})
	}
}

// "modbusgateway token add" asks for the token file before it exists.
func TestTokenFileNeedsNoTokenFile(t *testing.T) {
	config := writeTokenSetup(t, "tokens", "")

	got, err := TokenFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(filepath.Dir(config), "tokens"); got != want {
		t.Errorf("TokenFile = %q, want %q", got, want)
	}
}

func TestValidateNeedsTokenFile(t *testing.T) {
	c, err := load(t, "")
	if err != nil {
		t.Fatal(err)
	}
	c.Webserver.TokenFile = ""
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "modbusgateway token add") {
		t.Errorf("Validate without tokenFile: %v", err)
	}
}

// An old config with apiKey fails with a message that names the replacement, not just
// "unknown key".
func TestValidateRejectsApiKey(t *testing.T) {
	c, err := load(t, "")
	if err != nil {
		t.Fatal(err)
	}
	c.Webserver.ApiKey = "old"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "tokenFile") {
		t.Errorf("Validate with apiKey: %v", err)
	}
}

// The protected routes take a token from the token file as a bearer token, nothing else.
func TestWebConfigTakesBearerTokens(t *testing.T) {
	c := NewConfig()
	c.Webserver.Tokens = []web.Token{{Name: "nodered", Hash: web.HashToken("the-token")}}
	h := web.WithAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), (&App{config: c}).webConfig())

	for _, tt := range []struct {
		name   string
		header map[string]string
		denied bool
	}{
		{"token", map[string]string{"Authorization": "Bearer the-token"}, false},
		{"token in X-API-Key", map[string]string{"X-API-Key": "the-token"}, true},
		{"wrong token", map[string]string{"Authorization": "Bearer other"}, true},
		{"nothing", nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			for k, v := range tt.header {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if denied := rec.Code == http.StatusUnauthorized; denied != tt.denied {
				t.Errorf("status %d, denied %v, want %v", rec.Code, denied, tt.denied)
			}
		})
	}
}

// Every route except the web page and /version sits behind the authentication.
func TestProtectedRoutesRejectWithoutToken(t *testing.T) {
	_, h := newTestApp(t)

	for _, path := range []string{"/health", "/devices", "/devices/meter/status", "/activity", "/devices/meter/holding-registers/0"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer wrong")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s = %d, want 401", path, rec.Code)
		}
	}
}
