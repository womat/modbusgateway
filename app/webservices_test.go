package app

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// A client that offers at most TLS 1.2 is refused.
func TestTLSConfigRequires13(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.pem")
	cert, err := loadTLSCert(missing, missing, DevEnv)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.TLS = newTLSConfig(cert)
	srv.StartTLS()
	defer srv.Close()

	for _, tt := range []struct {
		max  uint16
		fail bool
	}{
		{tls.VersionTLS12, true},
		{tls.VersionTLS13, false},
	} {
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // self-signed test certificate
			MaxVersion:         tt.max,
		}}}
		resp, err := client.Get(srv.URL)
		if err == nil {
			_ = resp.Body.Close()
		}
		if (err != nil) != tt.fail {
			t.Errorf("max version %x: err = %v, want failure %v", tt.max, err, tt.fail)
		}
	}
}
