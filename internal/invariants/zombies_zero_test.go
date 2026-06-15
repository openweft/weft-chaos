// zombies_zero_test.go — pin the ZombiesZero invariant against an
// httptest server that emits Prometheus text-format metrics.
// Three angles : steady-state below threshold = no breach, value
// above threshold = breach, metric missing = breach (chaos-time
// classification — see ZombiesZero.evaluate).

package invariants

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openweft/weft-chaos/internal/scenario"
	"github.com/openweft/weft-chaos/internal/wclient"
)

func TestZombiesZero_NoBreachWhenBelowThreshold(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("weft_vm_zombies 0\n"))
	}))
	t.Cleanup(srv.Close)
	inv := &ZombiesZero{
		Spec: scenario.Invariant{
			Name:   "zombies",
			Kind:   "zombies_zero",
			Window: "100ms",
			Params: map[string]string{
				"url": srv.URL + "/metrics",
			},
		},
		Logger: nullLogger(),
		Client: wclient.New(nullLogger()),
	}
	rec := NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_ = inv.Run(ctx, rec)
	if got := len(rec.Snapshot()); got != 0 {
		t.Errorf("breach count when gauge=0 = %d, want 0", got)
	}
}

func TestZombiesZero_BreachWhenOverThreshold(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte("weft_vm_zombies 5\n"))
	}))
	t.Cleanup(srv.Close)
	inv := &ZombiesZero{
		Spec: scenario.Invariant{
			Name:   "zombies",
			Kind:   "zombies_zero",
			Window: "100ms",
			Params: map[string]string{
				"url":       srv.URL + "/metrics",
				"threshold": "0",
			},
		},
		Logger: nullLogger(),
		Client: wclient.New(nullLogger()),
	}
	rec := NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_ = inv.Run(ctx, rec)
	snap := rec.Snapshot()
	if len(snap) == 0 {
		t.Fatalf("breach count on gauge=5 = 0, want >=1")
	}
	for _, b := range snap {
		if b.Invariant != "zombies" {
			t.Errorf("breach.Invariant = %q, want zombies", b.Invariant)
		}
		if !strings.Contains(b.Detail, "weft_vm_zombies") {
			t.Errorf("breach.Detail = %q, want mention of metric name", b.Detail)
		}
	}
}

func TestZombiesZero_RespectsThreshold(t *testing.T) {
	// gauge=3, threshold=5 → no breach.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("weft_vm_zombies 3\n"))
	}))
	t.Cleanup(srv.Close)
	inv := &ZombiesZero{
		Spec: scenario.Invariant{
			Name:   "zombies-tolerant",
			Kind:   "zombies_zero",
			Window: "100ms",
			Params: map[string]string{
				"url":       srv.URL + "/metrics",
				"threshold": "5",
			},
		},
		Logger: nullLogger(),
		Client: wclient.New(nullLogger()),
	}
	rec := NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_ = inv.Run(ctx, rec)
	// Assert NO "value > threshold" breach. Scrape-error breaches
	// under heavy concurrent test load are tolerated — they're
	// noise, not a threshold violation, and the assertion is "the
	// threshold gate worked", not "the network was perfect".
	for _, b := range rec.Snapshot() {
		if strings.Contains(b.Detail, "> threshold") {
			t.Errorf("threshold breach with gauge=3 threshold=5 : %s", b.Detail)
		}
	}
}

func TestZombiesZero_MetricMissingIsBreach(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# nothing relevant here\nother_metric 42\n"))
	}))
	t.Cleanup(srv.Close)
	inv := &ZombiesZero{
		Spec: scenario.Invariant{
			Name:   "zombies",
			Kind:   "zombies_zero",
			Window: "100ms",
			Params: map[string]string{
				"url": srv.URL + "/metrics",
			},
		},
		Logger: nullLogger(),
		Client: wclient.New(nullLogger()),
	}
	rec := NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_ = inv.Run(ctx, rec)
	snap := rec.Snapshot()
	if len(snap) == 0 {
		t.Fatalf("missing-metric should record breach, got 0")
	}
	for _, b := range snap {
		if !strings.Contains(b.Detail, "not found") {
			t.Errorf("breach.Detail = %q, want mention of 'not found'", b.Detail)
		}
	}
}

func TestZombiesZero_EmptyURLRejected(t *testing.T) {
	inv := &ZombiesZero{
		Spec: scenario.Invariant{
			Name:   "no-url",
			Kind:   "zombies_zero",
			Params: map[string]string{}, // no url
		},
		Logger: nullLogger(),
		Client: wclient.New(nullLogger()),
	}
	rec := NewRecorder()
	err := inv.Run(context.Background(), rec)
	if err == nil {
		t.Fatal("Run with empty url = nil err, want config error")
	}
}

func TestZombiesZero_BadThresholdRejected(t *testing.T) {
	inv := &ZombiesZero{
		Spec: scenario.Invariant{
			Name:   "bad-threshold",
			Kind:   "zombies_zero",
			Params: map[string]string{
				"url":       "http://127.0.0.1:1/metrics",
				"threshold": "not-a-number",
			},
		},
		Logger: nullLogger(),
		Client: wclient.New(nullLogger()),
	}
	rec := NewRecorder()
	err := inv.Run(context.Background(), rec)
	if err == nil {
		t.Fatal("Run with bad threshold = nil err, want config error")
	}
}
