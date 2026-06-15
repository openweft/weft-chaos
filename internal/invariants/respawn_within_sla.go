// respawn_within_sla.go — invariant `kind = "respawn_within_sla"` :
// watches the `weft_respawn_seconds` histogram (published by the
// weft v0.4.x respawn reconciler) and breaches when the fraction
// of respawns completed within the SLA falls below the configured
// minimum ratio.
//
// What this lets a chaos run probe : after process_kill or
// host_cordon fires, VMs are supposed to respawn within ~60 s
// (per the respawn-V0.1.6 election + claim pipeline). A drift in
// the time-to-recover surfaces here long before zombies_zero
// (which only fires when ZombieGC notices a stranded record).
//
// Scenario block :
//
//	invariant "respawn_under_60s" {
//	  kind   = "respawn_within_sla"
//	  window = "30s"
//	  params = {
//	    url          = "http://weft.weft.internal:7770/metrics"
//	    metric       = "weft_respawn_seconds"
//	    sla_seconds  = "60"
//	    min_ratio    = "0.95"
//	    min_samples  = "5"          # avoid breaching on a tiny n
//	  }
//	}
//
// Limitations of V1 :
//
//   - The check is on the CUMULATIVE histogram. A single slow
//     respawn early in cluster history can keep the ratio low for
//     subsequent runs ; the recommended setup is to point this at
//     a fresh-state cluster or run delta-aware tooling.
//   - The histogram must declare a bucket at exactly the
//     `sla_seconds` boundary (e.g. le="60"). If the bucket isn't
//     present, the invariant fails CLOSED — records a breach
//     explaining the misconfiguration so the operator notices.

package invariants

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/openweft/weft-chaos/internal/scenario"
	"github.com/openweft/weft-chaos/internal/wclient"
)

// RespawnWithinSLA scrapes a Prometheus histogram + breaches when
// the fraction of samples completing under the SLA falls below
// min_ratio.
type RespawnWithinSLA struct {
	Spec   scenario.Invariant
	Logger *slog.Logger
	Client *wclient.Client
}

func (r *RespawnWithinSLA) Name() string { return r.Spec.Name }

// Run polls every Window seconds. Each scrape error or below-ratio
// reading produces one Breach.
func (r *RespawnWithinSLA) Run(ctx context.Context, rec *Recorder) error {
	url := r.Spec.Params["url"]
	if url == "" {
		return fmt.Errorf("respawn_within_sla %q : params.url is empty", r.Spec.Name)
	}
	metric := r.Spec.Params["metric"]
	if metric == "" {
		metric = "weft_respawn_seconds"
	}
	slaStr := r.Spec.Params["sla_seconds"]
	sla := 60.0
	if slaStr != "" {
		v, err := strconv.ParseFloat(slaStr, 64)
		if err != nil {
			return fmt.Errorf("respawn_within_sla %q : sla_seconds %q : %w", r.Spec.Name, slaStr, err)
		}
		sla = v
	}
	ratioStr := r.Spec.Params["min_ratio"]
	minRatio := 0.95
	if ratioStr != "" {
		v, err := strconv.ParseFloat(ratioStr, 64)
		if err != nil {
			return fmt.Errorf("respawn_within_sla %q : min_ratio %q : %w", r.Spec.Name, ratioStr, err)
		}
		if v < 0 || v > 1 {
			return fmt.Errorf("respawn_within_sla %q : min_ratio %v out of [0,1]", r.Spec.Name, v)
		}
		minRatio = v
	}
	minSamplesStr := r.Spec.Params["min_samples"]
	minSamples := 1.0
	if minSamplesStr != "" {
		v, err := strconv.ParseFloat(minSamplesStr, 64)
		if err != nil {
			return fmt.Errorf("respawn_within_sla %q : min_samples %q : %w", r.Spec.Name, minSamplesStr, err)
		}
		minSamples = v
	}
	window, _ := r.Spec.WindowDuration()
	if window <= 0 {
		window = 30 * time.Second
	}
	r.Logger.Info("invariant up",
		"name", r.Spec.Name, "kind", r.Spec.Kind,
		"url", url, "metric", metric,
		"sla_seconds", sla, "min_ratio", minRatio,
		"min_samples", minSamples, "window", window)

	t := time.NewTicker(window)
	defer t.Stop()
	r.evaluate(ctx, rec, url, metric, sla, minRatio, minSamples, window)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			r.evaluate(ctx, rec, url, metric, sla, minRatio, minSamples, window)
		}
	}
}

func (r *RespawnWithinSLA) evaluate(ctx context.Context, rec *Recorder, url, metric string, sla, minRatio, minSamples float64, window time.Duration) {
	probeCtx, cancel := context.WithTimeout(ctx, window/2)
	defer cancel()
	h, err := r.Client.ScrapeHistogram(probeCtx, url, metric)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		rec.Record(Breach{
			Invariant: r.Spec.Name,
			Kind:      r.Spec.Kind,
			At:        time.Now().UTC(),
			Detail:    fmt.Sprintf("scrape %s : %s", url, err),
		})
		return
	}
	if h.Count < minSamples {
		// Too few samples to judge ; not a breach, just not enough
		// data. An operator who wants this case flagged sets
		// min_samples = 0.
		return
	}
	within, ok := h.Buckets[sla]
	if !ok {
		rec.Record(Breach{
			Invariant: r.Spec.Name,
			Kind:      r.Spec.Kind,
			At:        time.Now().UTC(),
			Detail: fmt.Sprintf("%s : no bucket at le=%v ; configure the histogram with a matching bucket boundary",
				metric, sla),
		})
		return
	}
	ratio := within / h.Count
	if ratio < minRatio {
		rec.Record(Breach{
			Invariant: r.Spec.Name,
			Kind:      r.Spec.Kind,
			At:        time.Now().UTC(),
			Detail: fmt.Sprintf("%s : only %.1f%% of %v samples respawned ≤ %vs (min %.1f%%)",
				metric, ratio*100, h.Count, sla, minRatio*100),
		})
	}
}
