// Package cluster owns the small set of cluster.hcl fields chaos
// cares about. Today : the production guard, the portal URL, and
// the cluster name (used in slog tags + the report header). The
// real openweft cluster.hcl carries dozens more blocks (DCs,
// hypervisors, drivers, federation peers) — chaos only needs the
// safety-critical subset, so we don't import weft repo's heavier
// parser here.
//
// Why a guard : the chaos run can SIGKILL agents, partition AZs,
// cordon hosts. Pointing it at a production cluster by mistake is
// the kind of disaster a tooling line should make hard. The guard
// is `production = true` → refuse unless --i-know-what-im-doing
// is set ; matches the prompt-for-confirmation pattern weft uses
// in its own destructive CLIs.

package cluster

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsimple"
)

// Cluster is the parsed cluster.hcl. Only the safety-critical
// fields ; the rest is captured via Remain (hcl.Body) so unknown
// blocks like dc/hypervisor/driver pass through without erroring
// — chaos doesn't have to chase upstream schema changes in
// lockstep.
type Cluster struct {
	Name       string   `hcl:"name,optional"`
	Production bool     `hcl:"production,optional"`
	PortalURL  string   `hcl:"portal_url,optional"`
	Remain     hcl.Body `hcl:",remain"`
}

// Load reads cluster.hcl from disk. Missing file or malformed HCL
// surfaces as an error ; a well-formed file with no fields at all
// returns a zero Cluster (Production=false, PortalURL="").
func Load(path string) (*Cluster, error) {
	var c Cluster
	if err := hclsimple.DecodeFile(path, nil, &c); err != nil {
		return nil, fmt.Errorf("cluster %s: %w", path, err)
	}
	return &c, nil
}

// ConfirmDestructive enforces the production guard. Returns nil if
// the cluster is non-production OR the operator passed --i-know-
// what-im-doing. Returns a refusal error otherwise — the chaos
// binary aborts before touching the cluster.
func (c *Cluster) ConfirmDestructive(iKnowWhatImDoing bool) error {
	if !c.Production {
		return nil
	}
	if iKnowWhatImDoing {
		return nil
	}
	return fmt.Errorf("cluster %q is marked production = true ; pass --i-know-what-im-doing to override", c.Name)
}
