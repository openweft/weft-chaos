// cluster_test.go — pin Load + ConfirmDestructive against
// representative cluster.hcl shapes. The production guard is the
// safety-critical part : every chaos run's blast radius depends
// on it firing when it should + standing aside when it shouldn't.

package cluster

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeHCL(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster.hcl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_MinimalClusterParses(t *testing.T) {
	path := writeHCL(t, `
name = "sandbox-eu-west"
production = false
portal_url = "https://sandbox.example.com"
`)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "sandbox-eu-west" {
		t.Errorf("Name = %q, want sandbox-eu-west", c.Name)
	}
	if c.Production {
		t.Errorf("Production = true, want false")
	}
	if c.PortalURL != "https://sandbox.example.com" {
		t.Errorf("PortalURL = %q", c.PortalURL)
	}
}

func TestLoad_EmptyFileIsZeroCluster(t *testing.T) {
	path := writeHCL(t, "")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Production || c.Name != "" || c.PortalURL != "" {
		t.Errorf("empty file = %+v, want zero Cluster", c)
	}
}

func TestLoad_MissingFileErrs(t *testing.T) {
	_, err := Load("/no/such/cluster.hcl")
	if err == nil {
		t.Fatal("Load on missing file = nil, want err")
	}
	if !strings.Contains(err.Error(), "cluster") {
		t.Errorf("err = %q, want mention of 'cluster'", err)
	}
}

func TestLoad_MalformedHCLErrs(t *testing.T) {
	path := writeHCL(t, "production = ")
	_, err := Load(path)
	if err == nil {
		t.Fatal("malformed HCL = nil, want err")
	}
}

func TestConfirmDestructive_NonProductionPasses(t *testing.T) {
	c := &Cluster{Production: false}
	if err := c.ConfirmDestructive(false); err != nil {
		t.Errorf("non-prod without override = %v, want nil", err)
	}
	if err := c.ConfirmDestructive(true); err != nil {
		t.Errorf("non-prod with override = %v, want nil", err)
	}
}

func TestConfirmDestructive_ProductionRefusedWithoutOverride(t *testing.T) {
	c := &Cluster{Name: "prod-1", Production: true}
	err := c.ConfirmDestructive(false)
	if err == nil {
		t.Fatal("prod without --i-know-what-im-doing = nil, want refusal")
	}
	if !strings.Contains(err.Error(), "production") {
		t.Errorf("err = %q, want mention of 'production'", err)
	}
	if !strings.Contains(err.Error(), "--i-know-what-im-doing") {
		t.Errorf("err = %q, want mention of the override flag", err)
	}
}

func TestConfirmDestructive_ProductionAllowedWithOverride(t *testing.T) {
	c := &Cluster{Name: "prod-1", Production: true}
	if err := c.ConfirmDestructive(true); err != nil {
		t.Errorf("prod with override = %v, want nil", err)
	}
}

func TestLoad_ExtraBlocksTolerated(t *testing.T) {
	// Real cluster.hcl carries dozens of blocks chaos doesn't
	// care about (dc, hypervisor, driver…) ; Load must not
	// reject them via the `Remain` catch-all.
	path := writeHCL(t, `
name = "sandbox"
production = false

dc "dc1" {
  region = "eu-west-1"
}

hypervisor "qemu" {
  endpoint = "tcp://10.0.0.5:7770"
}
`)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load with extra blocks = %v, want nil", err)
	}
	if c.Name != "sandbox" {
		t.Errorf("Name = %q, want sandbox", c.Name)
	}
}
