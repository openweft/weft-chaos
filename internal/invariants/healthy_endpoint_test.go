// healthy_endpoint_test.go — pin the HealthyEndpoint invariant
// against an httptest server : OK responses produce no breaches,
// 503 produces one breach per round.

package invariants

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openweft/weft-chaos/internal/scenario"
	"github.com/openweft/weft-chaos/internal/wclient"
)

func nullLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHealthyEndpoint_NoBreachWhen200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	inv := &HealthyEndpoint{
		Spec: scenario.Invariant{
			Name:   "ok-endpoint",
			Kind:   "healthy_endpoint",
			Window: "100ms",
			Params: map[string]string{"urls": srv.URL + "/api/healthz"},
		},
		Logger: nullLogger(),
		Client: wclient.New(nullLogger()),
	}
	rec := NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_ = inv.Run(ctx, rec)
	if got := len(rec.Snapshot()); got != 0 {
		t.Errorf("breach count on 200-only = %d, want 0", got)
	}
}

func TestHealthyEndpoint_BreachWhen503(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	inv := &HealthyEndpoint{
		Spec: scenario.Invariant{
			Name:   "down-endpoint",
			Kind:   "healthy_endpoint",
			Window: "100ms",
			Params: map[string]string{"urls": srv.URL + "/api/healthz"},
		},
		Logger: nullLogger(),
		Client: wclient.New(nullLogger()),
	}
	rec := NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_ = inv.Run(ctx, rec)
	got := len(rec.Snapshot())
	if got == 0 {
		t.Fatalf("breach count on 503 = 0, want at least 1")
	}
	for _, b := range rec.Snapshot() {
		if b.Invariant != "down-endpoint" {
			t.Errorf("breach.Invariant = %q, want down-endpoint", b.Invariant)
		}
	}
}

func TestHealthyEndpoint_EmptyURLsRejected(t *testing.T) {
	inv := &HealthyEndpoint{
		Spec: scenario.Invariant{
			Name:   "no-urls",
			Kind:   "healthy_endpoint",
			Params: map[string]string{}, // no urls
		},
		Logger: nullLogger(),
		Client: wclient.New(nullLogger()),
	}
	rec := NewRecorder()
	err := inv.Run(context.Background(), rec)
	if err == nil {
		t.Fatal("Run with empty urls = nil err, want config error")
	}
}

func TestParseURLs_TrimsAndDropsEmpty(t *testing.T) {
	got := parseURLs("  https://a , , https://b,,https://c  ")
	want := []string{"https://a", "https://b", "https://c"}
	if len(got) != len(want) {
		t.Fatalf("len=%d, want %d : %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%q, want %q", i, got[i], want[i])
		}
	}
}
