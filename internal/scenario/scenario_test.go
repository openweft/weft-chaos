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
	if len(sc.Injectors) != 2 {
		t.Errorf("injectors = %d, want 2 (dc2-cordon + dc2-partition)", len(sc.Injectors))
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
