// Package scenario parses scenario.hcl — the declarative description
// of a chaos run : what workloads to drive, what failures to inject,
// what invariants to watch.
//
// Schema (see scenarios/example.hcl for a concrete file) :
//
//	workload "vm-churn" {
//	  tenant       = "acme"
//	  steady_rps   = 5
//	  burst_rps    = 50
//	  burst_every  = "5m"
//	  burst_for    = "30s"
//	  resources    = ["microvm", "volume", "network"]
//	}
//
//	injector "dc2-down" {
//	  kind       = "host_cordon"
//	  selector   = "az=dc2"
//	  at_offset  = "10m"
//	  recover_at = "15m"
//	}
//
//	invariant "no_cross_tenant_audit_leak" {
//	  kind   = "audit_tenant_isolation"
//	  window = "1m"
//	}
//
// The HCL dialect mirrors cluster.hcl + plugin manifests so the
// operator UX is consistent across the stack.
package scenario

import (
	"fmt"
	"os"
	"time"

	"github.com/hashicorp/hcl/v2/hclsimple"
)

// Load parses the scenario.hcl at path into a typed Scenario.
// Errors surface the HCL diagnostic verbatim — the operator wants
// to see "line 17: undefined block kind" not a generic wrapper.
//
// Load is syntax-only ; semantic validation lives in Validate so a
// `plan` subcommand can still parse an incomplete draft.
func Load(path string) (*Scenario, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("scenario: %w", err)
	}
	var s Scenario
	if err := hclsimple.DecodeFile(path, nil, &s); err != nil {
		return nil, fmt.Errorf("scenario: parse %s: %w", path, err)
	}
	return &s, nil
}

// Validate enforces the rules a syntactically-clean scenario must
// also satisfy to be runnable. The biggest footgun the chaos
// harness can produce is a "successful" run that quietly passed
// because no invariant was ever checked — Validate refuses that
// shape explicitly.
//
// `weft-chaos run` calls Validate before touching the cluster ;
// `weft-chaos plan` does NOT, so an operator can lint a draft
// scenario without invariants while authoring.
func (s *Scenario) Validate() error {
	if len(s.Invariants) == 0 {
		return fmt.Errorf("scenario : 0 invariants — a chaos run with no checks " +
			"would always report success ; add at least one invariant block")
	}
	return nil
}

// Scenario is the parsed top-level document.
type Scenario struct {
	Workloads  []Workload  `hcl:"workload,block"`
	Injectors  []Injector  `hcl:"injector,block"`
	Invariants []Invariant `hcl:"invariant,block"`
}

// Workload is one tenant-scoped driver that issues mutations against
// the cluster at the configured rate.
type Workload struct {
	Name        string   `hcl:",label"`
	Tenant      string   `hcl:"tenant"`
	SteadyRPS   int      `hcl:"steady_rps"`
	BurstRPS    int      `hcl:"burst_rps,optional"`
	BurstEvery  string   `hcl:"burst_every,optional"` // duration, parsed via time.ParseDuration
	BurstFor    string   `hcl:"burst_for,optional"`
	Resources   []string `hcl:"resources"`
}

// BurstEveryDuration / BurstForDuration parse the string knobs into
// real Durations. Returns 0 + nil when unset — workloads with no
// burst run pure steady-state.
func (w Workload) BurstEveryDuration() (time.Duration, error) {
	if w.BurstEvery == "" {
		return 0, nil
	}
	return time.ParseDuration(w.BurstEvery)
}

func (w Workload) BurstForDuration() (time.Duration, error) {
	if w.BurstFor == "" {
		return 0, nil
	}
	return time.ParseDuration(w.BurstFor)
}

// Injector schedules a failure-injection action.
type Injector struct {
	Name      string `hcl:",label"`
	Kind      string `hcl:"kind"`               // host_cordon | network_partition | disk_pressure | kill_pid | etcd_evict
	Selector  string `hcl:"selector,optional"`  // resource selector (az=dc2, host_uuid=…)
	AtOffset  string `hcl:"at_offset"`          // duration from scenario start
	RecoverAt string `hcl:"recover_at,optional"` // duration from scenario start ; empty = permanent
	Params    map[string]string `hcl:"params,optional"`
}

func (i Injector) AtOffsetDuration() (time.Duration, error)  { return time.ParseDuration(i.AtOffset) }
func (i Injector) RecoverAtDuration() (time.Duration, error) {
	if i.RecoverAt == "" {
		return 0, nil
	}
	return time.ParseDuration(i.RecoverAt)
}

// Invariant declares one continuously-checked rule. Window is how
// far back the checker looks when a sample fires.
type Invariant struct {
	Name   string `hcl:",label"`
	Kind   string `hcl:"kind"`              // vm_count_consistent | audit_tenant_isolation | scheduling_compliant_within | zombies_zero
	Window string `hcl:"window,optional"`
	Params map[string]string `hcl:"params,optional"`
}

func (v Invariant) WindowDuration() (time.Duration, error) {
	if v.Window == "" {
		return 0, nil
	}
	return time.ParseDuration(v.Window)
}
