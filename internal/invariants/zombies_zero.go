// zombies_zero.go — invariant `kind = "zombies_zero"` :
// scrapes a Prometheus /metrics endpoint, reads the
// `weft_vm_zombies` gauge (published by weft v0.4.12+ via the
// ZombieGC reconciler), and records a Breach when the count
// exceeds `params.threshold` (default 0).
//
// What counts as a zombie : per weft/zombiegc.md, a VM whose
// agent-side state has diverged from the registry — orphan
// projects, ci_cross past CIGracePeriod, ha_cross stranded, or
// local file artefacts with no registry row. A non-zero count
// during a chaos run is interesting : the cluster reconciler
// hasn't kept up with the churn. A non-zero count between
// chaos runs is a real bug.
//
// Scenario block :
//
//	invariant "zombies_zero" {
//	  kind   = "zombies_zero"
//	  window = "30s"
//	  params = {
//	    url       = "http://weft.weft.internal:7770/metrics"
//	    threshold = "0"
//	    metric    = "weft_vm_zombies"
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

// ZombiesZero scrapes a single metric + breaches when its value
// exceeds the configured threshold.
type ZombiesZero struct {
	Spec   scenario.Invariant
	Logger *slog.Logger
	Client *wclient.Client
}

func (z *ZombiesZero) Name() string { return z.Spec.Name }

// Run polls every Window seconds. Each scrape error or
// over-threshold reading produces one Breach.
func (z *ZombiesZero) Run(ctx context.Context, rec *Recorder) error {
	url := z.Spec.Params["url"]
	if url == "" {
		return fmt.Errorf("zombies_zero %q : params.url is empty", z.Spec.Name)
	}
	metric := z.Spec.Params["metric"]
	if metric == "" {
		metric = "weft_vm_zombies"
	}
	thresholdStr := z.Spec.Params["threshold"]
	threshold := 0.0
	if thresholdStr != "" {
		v, err := strconv.ParseFloat(thresholdStr, 64)
		if err != nil {
			return fmt.Errorf("zombies_zero %q : threshold %q : %w", z.Spec.Name, thresholdStr, err)
		}
		threshold = v
	}
	window, _ := z.Spec.WindowDuration()
	if window <= 0 {
		window = 30 * time.Second
	}
	z.Logger.Info("invariant up",
		"name", z.Spec.Name, "kind", z.Spec.Kind,
		"url", url, "metric", metric,
		"threshold", threshold, "window", window)
	t := time.NewTicker(window)
	defer t.Stop()
	z.evaluate(ctx, rec, url, metric, threshold, window)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			z.evaluate(ctx, rec, url, metric, threshold, window)
		}
	}
}

func (z *ZombiesZero) evaluate(ctx context.Context, rec *Recorder, url, metric string, threshold float64, window time.Duration) {
	probeCtx, cancel := context.WithTimeout(ctx, window/2)
	defer cancel()
	value, err := z.Client.ScrapeMetric(probeCtx, url, metric)
	if err != nil {
		// MetricNotFound during a chaos run is interesting — the
		// scraped target restarted or the gauge wasn't published.
		// Either way, log as a breach so the timeline records it.
		detail := err.Error()
		if errors.Is(err, wclient.ErrMetricNotFound) {
			detail = fmt.Sprintf("%s : metric %q not found on %s", z.Spec.Name, metric, url)
		}
		rec.Record(Breach{
			Invariant: z.Spec.Name,
			At:        time.Now().UTC(),
			Detail:    detail,
		})
		return
	}
	if value > threshold {
		rec.Record(Breach{
			Invariant: z.Spec.Name,
			At:        time.Now().UTC(),
			Detail: fmt.Sprintf("%s = %.1f > threshold %.1f", metric, value, threshold),
		})
	}
}
