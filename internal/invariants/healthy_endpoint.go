// healthy_endpoint.go — invariant `kind = "healthy_endpoint"` :
// hits a comma-separated list of URLs every Window seconds and
// records a Breach for each one that returns non-200 (or fails
// to connect).
//
// The simplest fully-wired invariant — no OIDC, no audit-log
// parsing, just an HTTP probe. Doubles as the canonical
// invariant template : new invariants follow the same
// constructor + Run + evaluate split.
//
// Scenario block :
//
//	invariant "endpoints_alive" {
//	  kind   = "healthy_endpoint"
//	  window = "10s"
//	  params = {
//	    urls = "https://weft.example.com/api/healthz,https://infra.weft.example.com/api/healthz"
//	  }
//	}

package invariants

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/openweft/weft-chaos/internal/scenario"
	"github.com/openweft/weft-chaos/internal/wclient"
)

// HealthyEndpoint scrapes one or more URLs + records a breach
// every time one comes back non-200. No state across rounds —
// each tick is independent.
type HealthyEndpoint struct {
	Spec   scenario.Invariant
	Logger *slog.Logger
	Client *wclient.Client
}

func (h *HealthyEndpoint) Name() string { return h.Spec.Name }

// Run polls the configured URLs every Window seconds until ctx
// cancels. Each probe failure → one Breach in rec.
func (h *HealthyEndpoint) Run(ctx context.Context, rec *Recorder) error {
	urls := parseURLs(h.Spec.Params["urls"])
	if len(urls) == 0 {
		return fmt.Errorf("healthy_endpoint %q : params.urls is empty", h.Spec.Name)
	}
	window, _ := h.Spec.WindowDuration()
	if window <= 0 {
		window = 10 * time.Second
	}
	h.Logger.Info("invariant up",
		"name", h.Spec.Name, "kind", h.Spec.Kind,
		"urls", urls, "window", window)
	t := time.NewTicker(window)
	defer t.Stop()
	// Evaluate once immediately so a short-lived run still
	// reports at least one round.
	h.evaluate(ctx, rec, urls)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			h.evaluate(ctx, rec, urls)
		}
	}
}

func (h *HealthyEndpoint) evaluate(ctx context.Context, rec *Recorder, urls []string) {
	// Bound each probe to half the window so a slow endpoint
	// doesn't push the next tick.
	window, _ := h.Spec.WindowDuration()
	if window <= 0 {
		window = 10 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, window/2)
	defer cancel()
	for _, url := range urls {
		if err := h.Client.Healthz(probeCtx, url); err != nil {
			rec.Record(Breach{
				Invariant: h.Spec.Name,
				Kind:      h.Spec.Kind,
				At:        time.Now().UTC(),
				Detail:    err.Error(),
			})
		}
	}
}

// parseURLs splits "a,b,c" → ["a", "b", "c"], trims, drops empties.
// Tolerant : extra commas / surrounding whitespace don't matter so
// an operator hand-editing the scenario.hcl can be sloppy.
func parseURLs(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
