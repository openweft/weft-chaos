// wclient_test.go — pin Healthz against an httptest server. Real
// cluster smoke happens through scenarios ; this file owns the
// unit invariants of the seam itself.

package wclient

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func nullLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHealthz_200IsOk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	c := New(nullLogger())
	if err := c.Healthz(context.Background(), srv.URL+"/api/healthz"); err != nil {
		t.Errorf("Healthz on 200 = %v, want nil", err)
	}
}

func TestHealthz_503IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	c := New(nullLogger())
	err := c.Healthz(context.Background(), srv.URL+"/api/healthz")
	if err == nil {
		t.Fatal("Healthz on 503 = nil, want err")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("err = %v, want it to mention the status code", err)
	}
}

func TestHealthz_UnreachableServerIsError(t *testing.T) {
	c := New(nullLogger())
	// Port 1 is the canonical "nothing listens here" : on macOS
	// + Linux both refuse the connect immediately.
	err := c.Healthz(context.Background(), "http://127.0.0.1:1/api/healthz")
	if err == nil {
		t.Errorf("Healthz to unreachable host = nil, want err")
	}
}

func TestHealthz_ContextCancelAborts(t *testing.T) {
	// A server that never responds — Healthz must respect the
	// caller's cancelled context rather than hang on the
	// HTTPClient default timeout.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	c := New(nullLogger())
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already-cancelled context
	err := c.Healthz(ctx, srv.URL+"/api/healthz")
	if err == nil {
		t.Errorf("Healthz with cancelled ctx = nil, want err")
	}
}
