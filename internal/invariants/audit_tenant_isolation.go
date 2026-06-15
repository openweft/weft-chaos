// Package invariants is the watcher side of a chaos run. Each
// Invariant evaluates a continuously-checked rule by polling the
// cluster's /metrics + /api/audit-log endpoints + comparing the
// observed state against the rule's contract. Breaches accumulate
// in a slice the report writer drains at exit.
//
// Conventions :
//
//   - Invariants are READ-ONLY. They never call mutating endpoints
//     even on a violation — only the report writer is allowed to
//     decide what the human does about it.
//   - Each invariant runs in its own goroutine on a configurable
//     poll cadence (Window from the scenario block). Default 10s.
//   - Window also bounds the lookback when evaluating, so a slow
//     incident isn't flagged for events outside the operator's
//     declared interest.
//
// This file ships the canonical sample : audit_tenant_isolation —
// the most important multi-tenant invariant. Other invariants
// (vm_count_consistent, scheduling_compliant_within_60s,
// zombies_zero, bus_drops_zero) land as follow-up commits.

package invariants

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/openweft/weft-chaos/internal/scenario"
)

// Breach is one logged invariant violation, surfaced in the report.
type Breach struct {
	Invariant string    `json:"invariant"`
	Kind      string    `json:"kind,omitempty"`
	At        time.Time `json:"at"`
	Detail    string    `json:"detail"`
	// TenantsInvolved is the set of tenant names the breach touches.
	// Populated when the rule is tenant-aware (most are).
	TenantsInvolved []string `json:"tenants_involved,omitempty"`
}

// Invariant is the runtime interface. Run blocks until ctx is
// cancelled ; breaches are appended via Recorder.
type Invariant interface {
	Name() string
	Run(ctx context.Context, rec *Recorder) error
}

// Recorder is the concurrent-safe sink the agents + invariants
// share. The report writer reads from it at exit. An optional
// prometheus CounterVec mirrors every Record into a
// weft_chaos_breach_total{invariant,kind} cell so a Grafana panel
// can see breaches accumulating in real time alongside the cluster-
// side metrics the harness watches.
type Recorder struct {
	mu       sync.Mutex
	breaches []Breach
	counter  *prometheus.CounterVec
}

func NewRecorder() *Recorder { return &Recorder{} }

// NewRecorderWithCounter mirrors every Record into the given
// CounterVec. Pass nil to opt out.
func NewRecorderWithCounter(c *prometheus.CounterVec) *Recorder {
	return &Recorder{counter: c}
}

func (r *Recorder) Record(b Breach) {
	r.mu.Lock()
	r.breaches = append(r.breaches, b)
	r.mu.Unlock()
	if r.counter != nil {
		r.counter.WithLabelValues(b.Invariant, b.Kind).Inc()
	}
}

func (r *Recorder) Snapshot() []Breach {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Breach, len(r.breaches))
	copy(out, r.breaches)
	return out
}

// AuditTenantIsolation watches /api/audit-log on the tenant portal
// and confirms NO event tagged with a tenant other than the
// caller's leaks through. The whole point of the tenant portal is
// to never see another tenant's audit trail (cf. webui commit
// 3d1de0b) ; this invariant tests that property under load.
type AuditTenantIsolation struct {
	Spec   scenario.Invariant
	Logger *slog.Logger
	// TODO(weft-chaos) : wclient handle for the tenant-portal API.
	// One per tenant in the scenario ; the invariant fans the watch
	// out across them.
}

func (a *AuditTenantIsolation) Name() string { return a.Spec.Name }

// Run polls each configured tenant's /api/audit-log every
// scrapeInterval, asserts that every returned event's tenant
// matches the caller. Any cross-tenant event is a Breach.
func (a *AuditTenantIsolation) Run(ctx context.Context, rec *Recorder) error {
	scrape, _ := a.Spec.WindowDuration()
	if scrape <= 0 {
		scrape = 10 * time.Second
	}
	a.Logger.Info("invariant up", "name", a.Spec.Name, "window", scrape)
	t := time.NewTicker(scrape)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			a.evaluate(ctx, rec)
		}
	}
}

// evaluate runs one round. Stub today : the real call hits each
// tenant's /api/audit-log, walks the response, records a breach
// when the event.tenant doesn't match the caller's tenant claim.
func (a *AuditTenantIsolation) evaluate(ctx context.Context, rec *Recorder) {
	// TODO(weft-chaos) : for each tenant in the scenario :
	//   - wclient.AsTenant(name).TailAuditLog(ctx, since=lastWindow)
	//   - for ev in result : if ev.tenant != name : rec.Record(...)
	// The webui guarantees this property under correctly-set scope ;
	// this invariant catches a regression where a future endpoint
	// is exposed on the tenant portal without filtering.
	_ = fmt.Sprintf // keep fmt live for future stringification of breaches
}
