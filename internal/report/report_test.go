// report_test.go — pin Summarize against representative timelines.
// The summary is what alert pipelines hit ; getting the totals wrong
// silently turns a real bug into a no-op alarm.

package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openweft/weft-chaos/internal/invariants"
)

func TestSummarize_CountsAndBreakdown(t *testing.T) {
	r := &Report{
		StartedAt: time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC),
		EndedAt:   time.Date(2026, 6, 15, 12, 30, 0, 0, time.UTC),
		Workloads: []WorkloadResult{
			{Name: "acme", Tenant: "acme"}, {Name: "globex", Tenant: "globex"},
		},
		Injectors: []InjectorTimeline{
			{Name: "dc2-cordon", Kind: "host_cordon"},
		},
		Invariants: []InvariantTimeline{
			{Name: "alive", Kind: "healthy_endpoint",
				Breaches: []invariants.Breach{
					{Invariant: "alive", Kind: "healthy_endpoint"},
					{Invariant: "alive", Kind: "healthy_endpoint"},
				}},
			{Name: "zombies", Kind: "zombies_zero",
				Breaches: []invariants.Breach{{Invariant: "zombies", Kind: "zombies_zero"}}},
			{Name: "clean", Kind: "bus_drops_zero"}, // no breaches
		},
	}
	r.Summarize()
	if r.Summary.TotalBreaches != 3 {
		t.Errorf("total = %d, want 3", r.Summary.TotalBreaches)
	}
	if r.Summary.BreachesByKind["healthy_endpoint"] != 2 {
		t.Errorf("healthy_endpoint count = %d, want 2", r.Summary.BreachesByKind["healthy_endpoint"])
	}
	if r.Summary.BreachesByKind["zombies_zero"] != 1 {
		t.Errorf("zombies_zero count = %d, want 1", r.Summary.BreachesByKind["zombies_zero"])
	}
	if _, ok := r.Summary.BreachesByKind["bus_drops_zero"]; ok {
		t.Errorf("bus_drops_zero shouldn't appear (0 breaches) ; got %v", r.Summary.BreachesByKind)
	}
	if r.Summary.BreachesByName["alive"] != 2 || r.Summary.BreachesByName["zombies"] != 1 {
		t.Errorf("by_name = %v, want alive:2 zombies:1", r.Summary.BreachesByName)
	}
	if r.Summary.Workloads != 2 || r.Summary.Injectors != 1 || r.Summary.Invariants != 3 {
		t.Errorf("counts = w%d i%d v%d, want 2/1/3",
			r.Summary.Workloads, r.Summary.Injectors, r.Summary.Invariants)
	}
	if r.Summary.DurationS != 1800 {
		t.Errorf("duration = %v, want 1800", r.Summary.DurationS)
	}
}

func TestSummarize_EmptyReport(t *testing.T) {
	r := &Report{}
	r.Summarize()
	if r.Summary.TotalBreaches != 0 {
		t.Errorf("empty total = %d, want 0", r.Summary.TotalBreaches)
	}
	if r.Summary.DurationS != 0 {
		t.Errorf("empty duration = %v, want 0", r.Summary.DurationS)
	}
}

func TestSummarize_PassRunHasZeroTotals(t *testing.T) {
	// A clean run : 3 invariants, every list empty. Summary must
	// say TotalBreaches=0 with no by_kind / by_name entries.
	r := &Report{
		StartedAt: time.Now(),
		EndedAt:   time.Now().Add(time.Second),
		Invariants: []InvariantTimeline{
			{Name: "a", Kind: "healthy_endpoint"},
			{Name: "b", Kind: "zombies_zero"},
			{Name: "c", Kind: "bus_drops_zero"},
		},
	}
	r.Summarize()
	if r.Summary.TotalBreaches != 0 {
		t.Errorf("clean run total = %d, want 0", r.Summary.TotalBreaches)
	}
	if len(r.Summary.BreachesByKind) != 0 {
		t.Errorf("clean run by_kind = %v, want empty", r.Summary.BreachesByKind)
	}
}

func TestWrite_RoundTripsThroughJSON(t *testing.T) {
	r := &Report{
		StartedAt:    time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
		EndedAt:      time.Date(2026, 6, 15, 0, 1, 0, 0, time.UTC),
		ScenarioPath: "scenarios/example.hcl",
		ClusterName:  "sandbox-lab",
		Invariants: []InvariantTimeline{
			{Name: "alive", Kind: "healthy_endpoint",
				Breaches: []invariants.Breach{{Invariant: "alive", Kind: "healthy_endpoint", Detail: "503"}}},
		},
	}
	r.Summarize()
	dir := t.TempDir()
	path := filepath.Join(dir, "r.json")
	if err := Write(r, path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Report
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.ClusterName != "sandbox-lab" {
		t.Errorf("ClusterName = %q, want sandbox-lab", got.ClusterName)
	}
	if got.Summary.TotalBreaches != 1 {
		t.Errorf("round-trip total = %d, want 1", got.Summary.TotalBreaches)
	}
	if got.Summary.BreachesByKind["healthy_endpoint"] != 1 {
		t.Errorf("round-trip by_kind = %v", got.Summary.BreachesByKind)
	}
}
