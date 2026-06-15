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
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
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
//
// Scenario block :
//
//	invariant "no_cross_tenant_audit_leak" {
//	  kind   = "audit_tenant_isolation"
//	  window = "30s"
//	  params = {
//	    tenants      = "acme,globex,initech"
//	    url_template = "https://weft.example.com/api/v1/audit-log?tenant={tenant}"
//	  }
//	}
//
// For each tenant T in the CSV list, GETs the URL with {tenant}
// substituted, parses the response as a JSON array of audit events,
// and records a Breach for every event whose `tenant` field does
// not equal T. A scrape error is itself recorded as a Breach.
type AuditTenantIsolation struct {
	Spec   scenario.Invariant
	Logger *slog.Logger
	Client interface {
		FetchJSON(ctx context.Context, url string) ([]byte, error)
	}
}

// auditEvent is the minimal shape the invariant cares about. The
// real openweft audit-log carries far more fields ; we decode only
// what the rule names.
type auditEvent struct {
	ID     string `json:"id,omitempty"`
	Tenant string `json:"tenant,omitempty"`
	Action string `json:"action,omitempty"`
}

func (a *AuditTenantIsolation) Name() string { return a.Spec.Name }

// Run polls each configured tenant's /api/audit-log every Window,
// asserts that every returned event's tenant matches the caller.
// Cross-tenant events + scrape errors are Breaches.
func (a *AuditTenantIsolation) Run(ctx context.Context, rec *Recorder) error {
	tenants := parseCSV(a.Spec.Params["tenants"])
	if len(tenants) == 0 {
		return fmt.Errorf("audit_tenant_isolation %q : params.tenants is empty", a.Spec.Name)
	}
	tmpl := a.Spec.Params["url_template"]
	if tmpl == "" {
		return fmt.Errorf("audit_tenant_isolation %q : params.url_template is empty", a.Spec.Name)
	}
	if !strings.Contains(tmpl, "{tenant}") {
		return fmt.Errorf("audit_tenant_isolation %q : url_template %q : must contain {tenant}", a.Spec.Name, tmpl)
	}
	scrape, _ := a.Spec.WindowDuration()
	if scrape <= 0 {
		scrape = 30 * time.Second
	}
	a.Logger.Info("invariant up",
		"name", a.Spec.Name, "kind", a.Spec.Kind,
		"tenants", tenants, "window", scrape)

	t := time.NewTicker(scrape)
	defer t.Stop()
	a.evaluate(ctx, rec, tenants, tmpl, scrape)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			a.evaluate(ctx, rec, tenants, tmpl, scrape)
		}
	}
}

// evaluate fans across tenants : one GET per tenant, walk the
// returned events, record a Breach for every cross-tenant leak.
func (a *AuditTenantIsolation) evaluate(ctx context.Context, rec *Recorder, tenants []string, tmpl string, window time.Duration) {
	for _, tenant := range tenants {
		probeCtx, cancel := context.WithTimeout(ctx, window/2)
		url := strings.ReplaceAll(tmpl, "{tenant}", tenant)
		body, err := a.Client.FetchJSON(probeCtx, url)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			rec.Record(Breach{
				Invariant: a.Spec.Name,
				Kind:      a.Spec.Kind,
				At:        time.Now().UTC(),
				Detail:    fmt.Sprintf("fetch %s: %s", url, err),
				TenantsInvolved: []string{tenant},
			})
			continue
		}
		var events []auditEvent
		if err := json.Unmarshal(body, &events); err != nil {
			rec.Record(Breach{
				Invariant: a.Spec.Name,
				Kind:      a.Spec.Kind,
				At:        time.Now().UTC(),
				Detail:    fmt.Sprintf("decode %s: %s", url, err),
				TenantsInvolved: []string{tenant},
			})
			continue
		}
		for _, ev := range events {
			if ev.Tenant == "" || ev.Tenant == tenant {
				continue
			}
			rec.Record(Breach{
				Invariant: a.Spec.Name,
				Kind:      a.Spec.Kind,
				At:        time.Now().UTC(),
				Detail: fmt.Sprintf("tenant %q saw event id=%q from tenant %q (action=%q)",
					tenant, ev.ID, ev.Tenant, ev.Action),
				TenantsInvolved: []string{tenant, ev.Tenant},
			})
		}
	}
}

// parseCSV is the same shape as parseURLs in healthy_endpoint.go but
// kept local so the two invariants stay independent.
func parseCSV(s string) []string {
	if s == "" {
		return nil
	}
	out := make([]string, 0, 4)
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
