// bus_drops_zero_test.go — pin the BusDropsZero invariant against
// an httptest server. Three angles : the FIRST sample is never a
// breach (baseline), a flat counter produces no breach, a counter
// growing above threshold produces a breach.

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

// counterAt holds the current emitted value ; the test mutates it
// between rounds to simulate counter growth.
type counterAt struct {
	v atomic.Int64
}

func (c *counterAt) handler(metric string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(metric + " " + fmtInt(c.v.Load()) + "\n"))
	}
}

func fmtInt(n int64) string {
	if n == 0 {
		return "0"
	}
	const digits = "0123456789"
	// minimal positive-int formatter ; tests only need 0..999
	if n < 0 {
		return "-" + fmtInt(-n)
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	return string(buf[i:])
}

func TestBusDropsZero_FirstSampleIsBaseline(t *testing.T) {
	// Server reports a non-zero count from the start. The FIRST
	// scrape must not record a breach (it's the baseline).
	c := &counterAt{}
	c.v.Store(100)
	srv := httptest.NewServer(c.handler("weft_bus_dropped_total"))
	t.Cleanup(srv.Close)
	inv := &BusDropsZero{
		Spec: scenario.Invariant{
			Name:   "drops",
			Kind:   "bus_drops_zero",
			Window: "100ms",
			Params: map[string]string{"url": srv.URL + "/metrics"},
		},
		Logger: nullLogger(),
		Client: wclient.New(nullLogger()),
	}
	rec := NewRecorder()
	// Single round : ctx cancels before the second tick. No deltas
	// can be measured, so no breaches.
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_ = inv.Run(ctx, rec)
	if got := len(rec.Snapshot()); got != 0 {
		t.Errorf("first-sample breaches = %d, want 0 (baseline)", got)
	}
}

func TestBusDropsZero_BreachWhenGrowing(t *testing.T) {
	c := &counterAt{}
	c.v.Store(0)
	srv := httptest.NewServer(c.handler("weft_bus_dropped_total"))
	t.Cleanup(srv.Close)
	inv := &BusDropsZero{
		Spec: scenario.Invariant{
			Name:   "drops",
			Kind:   "bus_drops_zero",
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

	// Step the counter forward in a goroutine while Run loops.
	go func() {
		for i := 0; i < 5; i++ {
			time.Sleep(90 * time.Millisecond)
			c.v.Add(10)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	_ = inv.Run(ctx, rec)

	snap := rec.Snapshot()
	if len(snap) == 0 {
		t.Fatal("growing counter produced 0 breaches, want >=1")
	}
	for _, b := range snap {
		if !strings.Contains(b.Detail, "grew by") && !strings.Contains(b.Detail, "not found") {
			t.Errorf("breach.Detail = %q, want either 'grew by' or 'not found'", b.Detail)
		}
	}
}

func TestBusDropsZero_NoBreachWhenFlat(t *testing.T) {
	// Counter never moves : no breach should be recorded even
	// across many rounds.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("weft_bus_dropped_total 42\n"))
	}))
	t.Cleanup(srv.Close)
	inv := &BusDropsZero{
		Spec: scenario.Invariant{
			Name:   "drops",
			Kind:   "bus_drops_zero",
			Window: "50ms",
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
	if got := len(rec.Snapshot()); got != 0 {
		t.Errorf("flat counter breaches = %d, want 0", got)
	}
}

func TestBusDropsZero_RespectsThreshold(t *testing.T) {
	// Threshold=100 ; counter only grows by ~5 per round → no breach.
	c := &counterAt{}
	c.v.Store(0)
	srv := httptest.NewServer(c.handler("weft_bus_dropped_total"))
	t.Cleanup(srv.Close)
	inv := &BusDropsZero{
		Spec: scenario.Invariant{
			Name:   "tolerant",
			Kind:   "bus_drops_zero",
			Window: "100ms",
			Params: map[string]string{
				"url":       srv.URL + "/metrics",
				"threshold": "100",
			},
		},
		Logger: nullLogger(),
		Client: wclient.New(nullLogger()),
	}
	rec := NewRecorder()
	go func() {
		for i := 0; i < 3; i++ {
			time.Sleep(90 * time.Millisecond)
			c.v.Add(5)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	_ = inv.Run(ctx, rec)
	for _, b := range rec.Snapshot() {
		// network errors are allowed (ctx race) but never the "grew by" path
		if strings.Contains(b.Detail, "grew by") {
			t.Errorf("threshold=100 with delta~5 should NOT breach ; got %v", b.Detail)
		}
	}
}

func TestBusDropsZero_EmptyURLRejected(t *testing.T) {
	inv := &BusDropsZero{
		Spec: scenario.Invariant{
			Name:   "no-url",
			Kind:   "bus_drops_zero",
			Params: map[string]string{},
		},
		Logger: nullLogger(),
		Client: wclient.New(nullLogger()),
	}
	err := inv.Run(context.Background(), NewRecorder())
	if err == nil {
		t.Fatal("Run with empty url = nil err, want config error")
	}
}

func TestBusDropsZero_BadThresholdRejected(t *testing.T) {
	inv := &BusDropsZero{
		Spec: scenario.Invariant{
			Name: "bad-threshold",
			Kind: "bus_drops_zero",
			Params: map[string]string{
				"url":       "http://127.0.0.1:1/metrics",
				"threshold": "fast",
			},
		},
		Logger: nullLogger(),
		Client: wclient.New(nullLogger()),
	}
	err := inv.Run(context.Background(), NewRecorder())
	if err == nil {
		t.Fatal("Run with bad threshold = nil err, want config error")
	}
}
