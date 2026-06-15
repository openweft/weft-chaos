// bus_drops_zero.go — invariant `kind = "bus_drops_zero"` :
// watches the `weft_bus_dropped_total` counter (added in weft
// v0.1.7 bus hardening — see project_v017_bus_hardening memory).
// A non-zero growth between rounds means a subscriber fell behind
// + the event bus dropped messages : registry watchers stale,
// reconcile loops missing events, status updates lost.
//
// Counter, not gauge : we compare deltas across rounds rather
// than the absolute value, so a long-running cluster with a
// pre-existing drop count doesn't false-positive every round.
//
// Scenario block :
//
//	invariant "no_bus_drops" {
//	  kind   = "bus_drops_zero"
//	  window = "30s"
//	  params = {
//	    url       = "http://weft.weft.internal:7770/metrics"
//	    threshold = "0"          # per-round delta tolerance
//	    metric    = "weft_bus_dropped_total"
//	  }
//	}

package invariants

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/openweft/weft-chaos/internal/scenario"
	"github.com/openweft/weft-chaos/internal/wclient"
)

// BusDropsZero watches a Prometheus counter and breaches when it
// grows by more than `params.threshold` between two consecutive
// scrapes. ScrapeMetric sums across label permutations so
// per-component drops (subscriber=X / subscriber=Y) all roll up
// into one number.
type BusDropsZero struct {
	Spec   scenario.Invariant
	Logger *slog.Logger
	Client *wclient.Client
}

func (b *BusDropsZero) Name() string { return b.Spec.Name }

// Run polls every Window seconds. The first scrape sets the
// baseline ; subsequent scrapes whose delta exceeds threshold
// produce a Breach. A scrape error or ErrMetricNotFound is itself
// a Breach (chaos-time classification, same as ZombiesZero).
func (b *BusDropsZero) Run(ctx context.Context, rec *Recorder) error {
	url := b.Spec.Params["url"]
	if url == "" {
		return fmt.Errorf("bus_drops_zero %q : params.url is empty", b.Spec.Name)
	}
	metric := b.Spec.Params["metric"]
	if metric == "" {
		metric = "weft_bus_dropped_total"
	}
	thresholdStr := b.Spec.Params["threshold"]
	threshold := 0.0
	if thresholdStr != "" {
		v, err := strconv.ParseFloat(thresholdStr, 64)
		if err != nil {
			return fmt.Errorf("bus_drops_zero %q : threshold %q : %w", b.Spec.Name, thresholdStr, err)
		}
		threshold = v
	}
	window, _ := b.Spec.WindowDuration()
	if window <= 0 {
		window = 30 * time.Second
	}
	b.Logger.Info("invariant up",
		"name", b.Spec.Name, "kind", b.Spec.Kind,
		"url", url, "metric", metric,
		"threshold", threshold, "window", window)

	// Baseline lives in a local var so two BusDropsZero invariants
	// against different endpoints don't share state. NaN sentinel
	// means "not yet sampled".
	var baseline float64 = -1
	t := time.NewTicker(window)
	defer t.Stop()
	baseline = b.evaluate(ctx, rec, url, metric, threshold, window, baseline)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			baseline = b.evaluate(ctx, rec, url, metric, threshold, window, baseline)
		}
	}
}

// evaluate scrapes once + returns the new baseline. The first
// successful sample becomes the baseline (no breach emitted ; we
// have nothing to compare to yet). Subsequent samples compare
// delta against threshold.
func (b *BusDropsZero) evaluate(ctx context.Context, rec *Recorder, url, metric string, threshold float64, window time.Duration, prev float64) float64 {
	probeCtx, cancel := context.WithTimeout(ctx, window/2)
	defer cancel()
	value, err := b.Client.ScrapeMetric(probeCtx, url, metric)
	if err != nil {
		// A cancelled outer ctx means the run is shutting down ;
		// don't pollute the timeline with a "scrape … context
		// canceled" breach the operator didn't cause.
		if ctx.Err() != nil {
			return prev
		}
		detail := err.Error()
		if errors.Is(err, wclient.ErrMetricNotFound) {
			detail = fmt.Sprintf("%s : metric %q not found on %s", b.Spec.Name, metric, url)
		}
		rec.Record(Breach{
			Invariant: b.Spec.Name,
			Kind:      b.Spec.Kind,
			At:        time.Now().UTC(),
			Detail:    detail,
		})
		// Don't disturb the baseline on transient error — next
		// successful scrape will compare against the last good one.
		return prev
	}
	if prev < 0 {
		// First successful scrape becomes the baseline.
		return value
	}
	delta := value - prev
	if delta > threshold {
		rec.Record(Breach{
			Invariant: b.Spec.Name,
			Kind:      b.Spec.Kind,
			At:        time.Now().UTC(),
			Detail: fmt.Sprintf("%s grew by %.1f in %s (threshold %.1f)",
				metric, delta, window, threshold),
		})
	}
	return value
}
