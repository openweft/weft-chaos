// scenario_test.go — pin Load against the shipped example +
// against malformed inputs. The chaos harness's safety story
// depends on a misconfigured scenario.hcl failing loud before
// any cluster is touched.

package scenario

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_ExampleScenarioParses(t *testing.T) {
	// scenarios/example.hcl ships with the repo as the
	// canonical reference. It MUST parse cleanly — operators
	// new to the harness start from this file.
	wd, _ := os.Getwd() // .../internal/scenario
	path := filepath.Join(wd, "..", "..", "scenarios", "example.hcl")
	sc, err := Load(path)
	if err != nil {
		t.Fatalf("Load(example.hcl) = %v, want nil", err)
	}
	if len(sc.Workloads) != 3 {
		t.Errorf("workloads = %d, want 3 (acme + globex + initech)", len(sc.Workloads))
	}
	if len(sc.Injectors) != 3 {
		t.Errorf("injectors = %d, want 3 (cordon + partition + kill-agent)", len(sc.Injectors))
	}
	if len(sc.Invariants) != 4 {
		t.Errorf("invariants = %d, want 4 (audit + endpoints + zombies + bus drops)", len(sc.Invariants))
	}

	// Spot-check the dc2-cordon block — Selector + AtOffset
	// MUST round-trip exactly or the injector's `az=dc2`
	// pattern matcher will fail at runtime.
	var cordon *Injector
	for i := range sc.Injectors {
		if sc.Injectors[i].Name == "dc2-cordon" {
			cordon = &sc.Injectors[i]
			break
		}
	}
	if cordon == nil {
		t.Fatal("dc2-cordon block missing from example.hcl")
	}
	if cordon.Selector != "az=dc2" || cordon.AtOffset != "10m" {
		t.Errorf("dc2-cordon spec = %+v, schema regressed", cordon)
	}
}

func TestLoad_MissingFileErrs(t *testing.T) {
	_, err := Load("/no/such/scenario.hcl")
	if err == nil {
		t.Fatal("Load on missing file = nil, want err")
	}
	if !strings.Contains(err.Error(), "scenario") {
		t.Errorf("err = %q, want prefix \"scenario\"", err)
	}
}

func TestLoad_MalformedHCLErrs(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.hcl")
	if err := os.WriteFile(bad, []byte("workload {\n  name = unterminated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(bad)
	if err == nil {
		t.Fatal("Load on malformed HCL = nil, want err")
	}
}

func TestValidate_AcceptsNonEmptyInvariants(t *testing.T) {
	s := &Scenario{Invariants: []Invariant{
		{Name: "alive", Kind: "healthy_endpoint"},
	}}
	if err := s.Validate(); err != nil {
		t.Errorf("Validate with 1 invariant = %v, want nil", err)
	}
}

func TestValidate_RejectsZeroInvariants(t *testing.T) {
	// A scenario with workloads + injectors but no invariants
	// is the canonical footgun : the run would drive churn against
	// the cluster + always report "success" because there's no
	// rule to break. Validate must refuse.
	s := &Scenario{
		Workloads: []Workload{{Name: "w", Tenant: "t", SteadyRPS: 1, Resources: []string{"microvm"}}},
		Injectors: []Injector{{Name: "i", Kind: "host_cordon", Selector: "az=dc2", AtOffset: "1m"}},
	}
	err := s.Validate()
	if err == nil {
		t.Fatal("Validate with 0 invariants = nil, want refusal")
	}
	if !strings.Contains(err.Error(), "0 invariants") {
		t.Errorf("err = %q, want mention of '0 invariants'", err)
	}
}

func TestValidate_RejectsEmptyScenario(t *testing.T) {
	s := &Scenario{}
	if err := s.Validate(); err == nil {
		t.Fatal("Validate on empty scenario = nil, want refusal")
	}
}

func TestLoad_DoesNotValidate(t *testing.T) {
	// Load must accept a syntactically-clean scenario even if it
	// has no invariants — the plan subcommand needs to lint drafts.
	dir := t.TempDir()
	path := filepath.Join(dir, "draft.hcl")
	if err := os.WriteFile(path, []byte(`workload "w" {
  tenant = "t"
  steady_rps = 1
  resources = ["microvm"]
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sc, err := Load(path)
	if err != nil {
		t.Fatalf("Load(draft) = %v, want nil (syntax-only)", err)
	}
	if err := sc.Validate(); err == nil {
		t.Error("Validate(draft) = nil, want refusal at run-time")
	}
}

func TestWorkload_BurstEveryDurationParses(t *testing.T) {
	w := Workload{BurstEvery: "5m"}
	d, err := w.BurstEveryDuration()
	if err != nil {
		t.Fatal(err)
	}
	if d.Minutes() != 5 {
		t.Errorf("5m parsed as %v", d)
	}
}

func TestWorkload_EmptyBurstEveryIsZeroNoError(t *testing.T) {
	w := Workload{}
	d, err := w.BurstEveryDuration()
	if err != nil {
		t.Fatal(err)
	}
	if d != 0 {
		t.Errorf("empty burst_every = %v, want 0", d)
	}
}
