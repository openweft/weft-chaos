// main_test.go — pin the cobra wire-up : the subcommands are
// registered, required flags fire, plan/version don't touch the
// cluster. Live `run` smoke happens via the orchestrate package's
// end-to-end tests + the scenarios/*.hcl smokes.

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoot_HasExpectedSubcommands(t *testing.T) {
	root := newRootCmd()
	want := map[string]bool{"run": false, "plan": false, "version": false}
	for _, c := range root.Commands() {
		want[c.Name()] = true
	}
	for name, found := range want {
		if !found {
			t.Errorf("subcommand %q missing", name)
		}
	}
}

func TestPlan_AliasIsValidate(t *testing.T) {
	root := newRootCmd()
	for _, c := range root.Commands() {
		if c.Name() != "plan" {
			continue
		}
		hasValidate := false
		for _, a := range c.Aliases {
			if a == "validate" {
				hasValidate = true
			}
		}
		if !hasValidate {
			t.Errorf("plan aliases = %v, want to include 'validate'", c.Aliases)
		}
		return
	}
	t.Fatal("plan subcommand not found")
}

func TestRun_RequiresClusterAndScenario(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"run"})
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	err := root.Execute()
	if err == nil {
		t.Fatal("run without --cluster/--scenario = nil err, want required-flag error")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("err = %v, want mention of 'required'", err)
	}
}

func TestPlan_RequiresScenario(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"plan"})
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	err := root.Execute()
	if err == nil {
		t.Fatal("plan without --scenario = nil err, want required-flag error")
	}
}

func TestPlan_ParsesValidScenario(t *testing.T) {
	dir := t.TempDir()
	scn := filepath.Join(dir, "s.hcl")
	if err := os.WriteFile(scn, []byte(`workload "w" {
  tenant = "x"
  steady_rps = 1
  resources = ["microvm"]
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	root := newRootCmd()
	root.SetArgs([]string{"plan", "--scenario", scn})
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	if err := root.Execute(); err != nil {
		t.Fatalf("plan = %v, want nil", err)
	}
}

func TestPlan_RejectsMissingScenarioFile(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"plan", "--scenario", "/no/such/scenario.hcl"})
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	if err := root.Execute(); err == nil {
		t.Fatal("plan on missing scenario file = nil err, want err")
	}
}

func TestVersion_PrintsLine(t *testing.T) {
	// Capture stdout : the version subcommand uses fmt.Printf,
	// which goes to os.Stdout not the cobra cmd.Out().
	r, w, _ := os.Pipe()
	orig := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = orig })

	root := newRootCmd()
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), "weft-chaos") {
		t.Errorf("version output = %q, want to mention 'weft-chaos'", string(out))
	}
}
