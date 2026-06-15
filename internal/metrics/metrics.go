// Package metrics centralises every Prometheus surface the chaos
// harness publishes about itself. Agents bump counters when they
// dispatch a CRUD round ; invariants bump counters when they record
// a breach. An operator running multiple chaos jobs in parallel
// can scrape /metrics on the harness itself + chart effort + outcome
// alongside the cluster-side metrics the harness watches.
//
// The harness doesn't expose /metrics from the CLI yet — the
// counter handles are reachable from the orchestrator + an embedded
// `--metrics-listen :7771` flag lands in a follow-up.

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// New builds a fresh registry + the well-known counter set. Each
// chaos process owns one registry — re-creating counters between
// runs avoids the "duplicate metrics collector" panic prometheus
// fires when the same counter is registered twice on the default
// registry.
func New() *Set {
	reg := prometheus.NewRegistry()
	s := &Set{
		Registry: reg,
		Dispatch: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "weft_chaos_dispatch_total",
			Help: "Total CRUD rounds an agent attempted, labelled by resource + verb + outcome.",
		}, []string{"resource", "verb", "result"}),
		Breach: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "weft_chaos_breach_total",
			Help: "Total invariant breaches recorded, labelled by invariant name + kind.",
		}, []string{"invariant", "kind"}),
	}
	reg.MustRegister(s.Dispatch, s.Breach)
	return s
}

// Set bundles the registry + every counter the harness emits.
// Passed to agents + invariants by the orchestrator so they don't
// need a global.
type Set struct {
	Registry *prometheus.Registry
	Dispatch *prometheus.CounterVec
	Breach   *prometheus.CounterVec
}
