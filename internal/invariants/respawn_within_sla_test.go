// respawn_within_sla_test.go — pin RespawnWithinSLA against canned
// histogram pages. The chaos run's "did recovery happen on time"
// signal flows through this invariant.

package invariants

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openweft/weft-chaos/internal/scenario"
	"github.com/openweft/weft-chaos/internal/wclient"
)

const goodRespawnBody = `# TYPE weft_respawn_seconds histogram
weft_respawn_seconds_bucket{le="5"} 30
weft_respawn_seconds_bucket{le="30"} 95
weft_respawn_seconds_bucket{le="60"} 99
weft_respawn_seconds_bucket{le="+Inf"} 100
weft_respawn_seconds_sum 1500
weft_respawn_seconds_count 100
`

const badRespawnBody = `# TYPE weft_respawn_seconds histogram
weft_respawn_seconds_bucket{le="5"} 5
weft_respawn_seconds_bucket{le="30"} 30
weft_respawn_seconds_bucket{le="60"} 50
weft_respawn_seconds_bucket{le="+Inf"} 100
weft_respawn_seconds_sum 9000
weft_respawn_seconds_count 100
`

func newRespawn(srvURL string, params map[string]string) *RespawnWithinSLA {
	if params == nil {
		params = map[string]string{}
	}
	params["url"] = srvURL + "/metrics"
	return &RespawnWithinSLA{
		Spec: scenario.Invariant{
			Name:   "respawn",
			Kind:   "respawn_within_sla",
			Window: "100ms",
			Params: params,
		},
		Logger: nullLogger(),
		Client: wclient.New(nullLogger()),
	}
}

func TestRespawnWithinSLA_NoBreachOnGoodHistogram(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(goodRespawnBody))
	}))
	t.Cleanup(srv.Close)
	inv := newRespawn(srv.URL, nil) // defaults sla=60 min_ratio=0.95
	rec := NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_ = inv.Run(ctx, rec)
	for _, b := range rec.Snapshot() {
		if strings.Contains(b.Detail, "respawned") {
			t.Errorf("good histogram produced a ratio breach : %s", b.Detail)
		}
	}
}

func TestRespawnWithinSLA_BreachWhenRatioBelowMin(t *testing.T) {
	// bad histogram : only 50/100 respawn under 60s = 50% < 95%
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(badRespawnBody))
	}))
	t.Cleanup(srv.Close)
	inv := newRespawn(srv.URL, nil)
	rec := NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_ = inv.Run(ctx, rec)
	snap := rec.Snapshot()
	if len(snap) == 0 {
		t.Fatal("bad histogram should produce >=1 breach")
	}
	found := false
	for _, b := range snap {
		if strings.Contains(b.Detail, "50.0%") && strings.Contains(b.Detail, "≤ 60") {
			found = true
		}
	}
	if !found {
		t.Errorf("breach detail missing 50.0%% / ≤ 60s phrase : %+v", snap)
	}
}

func TestRespawnWithinSLA_TooFewSamplesNoBreach(t *testing.T) {
	// 2 samples, min_samples=5 → invariant abstains.
	body := `# TYPE weft_respawn_seconds histogram
weft_respawn_seconds_bucket{le="60"} 1
weft_respawn_seconds_bucket{le="+Inf"} 2
weft_respawn_seconds_count 2
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	inv := newRespawn(srv.URL, map[string]string{"min_samples": "5"})
	rec := NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_ = inv.Run(ctx, rec)
	for _, b := range rec.Snapshot() {
		if strings.Contains(b.Detail, "respawned") {
			t.Errorf("abstain-on-low-n violated : %s", b.Detail)
		}
	}
}

func TestRespawnWithinSLA_MissingBucketIsConfigBreach(t *testing.T) {
	// SLA=60 but histogram has no le=60 bucket → fail closed.
	body := `# TYPE weft_respawn_seconds histogram
weft_respawn_seconds_bucket{le="30"} 80
weft_respawn_seconds_bucket{le="+Inf"} 100
weft_respawn_seconds_count 100
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	inv := newRespawn(srv.URL, nil) // sla=60 default
	rec := NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_ = inv.Run(ctx, rec)
	snap := rec.Snapshot()
	if len(snap) == 0 {
		t.Fatal("missing-bucket case should breach with a config message")
	}
	for _, b := range snap {
		if !strings.Contains(b.Detail, "no bucket") && !strings.Contains(b.Detail, "scrape") {
			t.Errorf("unexpected breach detail : %q", b.Detail)
		}
	}
}

func TestRespawnWithinSLA_ScrapeErrorIsBreach(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	inv := newRespawn(srv.URL, nil)
	rec := NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_ = inv.Run(ctx, rec)
	if len(rec.Snapshot()) == 0 {
		t.Fatal("503 metrics endpoint should breach")
	}
}

func TestRespawnWithinSLA_EmptyURLRejected(t *testing.T) {
	inv := &RespawnWithinSLA{
		Spec: scenario.Invariant{Name: "no-url", Kind: "respawn_within_sla",
			Params: map[string]string{}},
		Logger: nullLogger(),
		Client: wclient.New(nullLogger()),
	}
	if err := inv.Run(context.Background(), NewRecorder()); err == nil {
		t.Fatal("empty url = nil, want config err")
	}
}

func TestRespawnWithinSLA_BadRatioRejected(t *testing.T) {
	inv := newRespawn("http://x", map[string]string{"min_ratio": "1.5"})
	if err := inv.Run(context.Background(), NewRecorder()); err == nil {
		t.Fatal("min_ratio > 1 should reject")
	}
}
