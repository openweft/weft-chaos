// Package report writes the timeline JSON that summarises a chaos
// run : the scenario that ran, the durations, every invariant
// breach with timestamps, every injector apply/revert event. The
// operator + a future Grafana panel both consume this file.
//
// Pure-Go ; the JSON shape is intentionally flat enough to feed
// to jq + ingest into a TSDB :
//
//	{
//	  "started_at":  "2026-06-15T12:00:00Z",
//	  "ended_at":    "2026-06-15T12:30:00Z",
//	  "scenario":    "scenarios/example.hcl",
//	  "workloads":   [{name, tenant, ops, errors, …}],
//	  "injectors":   [{name, kind, applied_at, reverted_at}],
//	  "invariants":  [{name, kind, breaches: [{at, detail}]}]
//	}

package report

import (
	"encoding/json"
	"os"
	"time"

	"github.com/openweft/weft-chaos/internal/invariants"
)

// Report is the top-level document written at scenario exit.
type Report struct {
	StartedAt    time.Time           `json:"started_at"`
	EndedAt      time.Time           `json:"ended_at"`
	ScenarioPath string              `json:"scenario"`
	ClusterName  string              `json:"cluster,omitempty"`
	Summary      Summary             `json:"summary"`
	Workloads    []WorkloadResult    `json:"workloads"`
	Injectors    []InjectorTimeline  `json:"injectors"`
	Invariants   []InvariantTimeline `json:"invariants"`
}

// Summary is the operator-grade "did this run pass" cell. Reading
// it first answers the question 95 % of the time ; the timelines
// below explain WHICH invariant blew + when. Designed for jq +
// alert pipelines : `jq '.summary.total_breaches > 0'` is the
// canonical gate.
type Summary struct {
	TotalBreaches  int            `json:"total_breaches"`
	BreachesByKind map[string]int `json:"breaches_by_kind,omitempty"`
	BreachesByName map[string]int `json:"breaches_by_name,omitempty"`
	DurationS      float64        `json:"duration_s"`
	Workloads      int            `json:"workloads"`
	Injectors      int            `json:"injectors"`
	Invariants     int            `json:"invariants"`
}

// Summarize computes the Summary in one pass over the report's
// child collections. Called by orchestrate.Run after the timeline
// is assembled ; keeping it pure here means a downstream tool
// reading just the JSON can also reconstruct + audit the totals.
func (r *Report) Summarize() {
	s := Summary{
		Workloads:      len(r.Workloads),
		Injectors:      len(r.Injectors),
		Invariants:     len(r.Invariants),
		BreachesByKind: map[string]int{},
		BreachesByName: map[string]int{},
	}
	if !r.EndedAt.IsZero() && !r.StartedAt.IsZero() {
		s.DurationS = r.EndedAt.Sub(r.StartedAt).Seconds()
	}
	for _, it := range r.Invariants {
		n := len(it.Breaches)
		if n == 0 {
			continue
		}
		s.TotalBreaches += n
		s.BreachesByKind[it.Kind] += n
		s.BreachesByName[it.Name] += n
	}
	r.Summary = s
}

// WorkloadResult aggregates one workload's counters at exit.
type WorkloadResult struct {
	Name     string `json:"name"`
	Tenant   string `json:"tenant"`
	Ops      int    `json:"ops"`
	Errors   int    `json:"errors"`
	BurstHit int    `json:"burst_windows_hit"`
}

// InjectorTimeline records when each injector ran + reverted.
type InjectorTimeline struct {
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	AppliedAt  time.Time `json:"applied_at,omitzero"`
	RevertedAt time.Time `json:"reverted_at,omitzero"`
	Detail     string    `json:"detail,omitempty"`
}

// InvariantTimeline collects every breach for one invariant.
type InvariantTimeline struct {
	Name     string              `json:"name"`
	Kind     string              `json:"kind"`
	Breaches []invariants.Breach `json:"breaches"`
}

// Write serialises r to path as indented JSON. Atomic via
// tmp + rename so a SIGKILL mid-write never leaves a torn report.
func Write(r *Report, path string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
