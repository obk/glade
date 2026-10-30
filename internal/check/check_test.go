package check

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/obk/glade/internal/config"
)

func TestHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			io.WriteString(w, "hello canopy")
		case "/slow":
			time.Sleep(50 * time.Millisecond)
		case "/teapot":
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()

	for _, tc := range []struct {
		check config.Check
		state string
		note  string
	}{
		{config.Check{URL: srv.URL + "/ok", Contains: "canopy"}, Up, ""},
		{config.Check{URL: srv.URL + "/ok", Contains: "forest"}, Down, "doesn't contain the expected text"},
		{config.Check{URL: srv.URL + "/teapot"}, Down, "418 I'm a teapot"},
		{config.Check{URL: srv.URL + "/slow", Slow: 10 * time.Millisecond}, Degraded, "slow"},
		{config.Check{URL: srv.URL + "/slow", Timeout: 10 * time.Millisecond}, Down, "no answer within 10ms"},
	} {
		if tc.check.Timeout == 0 {
			tc.check.Timeout = 5 * time.Second
		}
		r := Run(tc.check, srv.Client(), 0)
		if r.State != tc.state || !strings.Contains(r.Note, tc.note) {
			t.Errorf("%s: got %s %q, want %s %q", tc.check.URL, r.State, r.Note, tc.state, tc.note)
		}
	}
}

func TestRetry(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	ch := config.Check{URL: srv.URL, Timeout: 5 * time.Second}
	if r := Run(ch, srv.Client(), 0); r.State != Up {
		t.Errorf("one failure then success: %s %q, want up", r.State, r.Note)
	}
}

func TestCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	ch := config.Check{URL: srv.URL, Timeout: 5 * time.Second}
	if r := Run(ch, srv.Client(), 0); r.State != Up {
		t.Errorf("got %s %q, want up", r.State, r.Note)
	}
	defer func(w time.Duration) { CertWarning = w }(CertWarning)
	CertWarning = 200 * 365 * 24 * time.Hour // every certificate expires sooner
	if r := Run(ch, srv.Client(), 0); r.State != Degraded || !strings.Contains(r.Note, "certificate expires in") {
		t.Errorf("got %s %q, want degraded by the certificate", r.State, r.Note)
	}
}

func TestTCP(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ch := config.Check{TCP: l.Addr().String(), Timeout: time.Second}
	if r := Run(ch, nil, 0); r.State != Up {
		t.Errorf("open port: %s %q", r.State, r.Note)
	}
	l.Close()
	if r := Run(ch, nil, 0); r.State != Down || r.Note != "connection refused" {
		t.Errorf("closed port: %s %q, want down, connection refused", r.State, r.Note)
	}
}

// The note is shown to everyone, so it must not repeat the URL, which can
// hold a token, or a host's name or address; the log gets those.
func TestNoSecrets(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer tls.Close()

	for _, tc := range []struct {
		check config.Check
		note  string // "" for any
	}{
		{config.Check{URL: "http://" + addr + "/health?token=SECRET"}, "connection refused"},
		{config.Check{TCP: addr}, "connection refused"},
		{config.Check{URL: tls.URL + "/health?token=SECRET"}, "the certificate isn't from a trusted authority"},
		{config.Check{URL: "http://secret-host.invalid/?token=SECRET"}, ""},
	} {
		tc.check.Timeout = 5 * time.Second
		r := Run(tc.check, &http.Client{}, 0) // trusts only real authorities
		if r.State != Down || (tc.note != "" && r.Note != tc.note) {
			t.Errorf("%s%s: %s %q, want down %q", tc.check.URL, tc.check.TCP, r.State, r.Note, tc.note)
		}
		for _, secret := range []string{"SECRET", "127.0.0.1", "secret-host"} {
			if strings.Contains(r.Note, secret) {
				t.Errorf("%s%s: the note %q gives away %s", tc.check.URL, tc.check.TCP, r.Note, secret)
			}
		}
		if r.Detail == "" {
			t.Errorf("%s%s: no detail for the log", tc.check.URL, tc.check.TCP)
		}
	}
}

func TestStatusText(t *testing.T) {
	// A server's own reason phrase could say anything; the page shows the
	// standard one.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buf, _ := w.(http.Hijacker).Hijack()
		buf.WriteString("HTTP/1.1 503 Visit evil.example\r\nContent-Length: 0\r\n\r\n")
		buf.Flush()
		conn.Close()
	}))
	defer srv.Close()
	r := Run(config.Check{URL: srv.URL, Timeout: 5 * time.Second}, srv.Client(), 0)
	if r.Note != "503 Service Unavailable" {
		t.Errorf("note %q, want 503 Service Unavailable", r.Note)
	}
}
