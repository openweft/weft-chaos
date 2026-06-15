// network_partition.go — injector `kind = "network_partition"` :
// installs a netfilter rule that drops (or rejects) traffic
// between a target host and the rest of the WireGuard mesh, then
// removes the rule on Revert.
//
// What this lets a chaos run probe : federation peer recovery
// (peers should flip to `stale` then `unreachable`, recover when
// the partition heals), HA cluster behaviour (etcd quorum loss
// + reform), VM respawn during cross-DC mesh failure.
//
// Scenario block :
//
//	injector "dc2-isolated" {
//	  kind       = "network_partition"
//	  selector   = "az=dc2"
//	  at_offset  = "10m"
//	  recover_at = "12m"
//	  params = {
//	    mode = "drop"        # drop | reject
//	  }
//	}
//
// Scaffold-only : Apply / Revert log + return nil ; the live
// netfilter work goes through wclient.PartitionAZ(ctx, az, mode)
// — TBD when the wclient ↔ weft-agent gRPC channel lands.

package injectors

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/openweft/weft-chaos/internal/scenario"
)

// NetworkPartition implements `kind = "network_partition"`.
type NetworkPartition struct {
	Spec   scenario.Injector
	Logger *slog.Logger
}

func (n *NetworkPartition) Name() string { return n.Spec.Name }

func (n *NetworkPartition) Apply(ctx context.Context) error {
	az, ok := parseSelector(n.Spec.Selector, "az")
	if !ok {
		return fmt.Errorf("network_partition : selector %q : expected az=<name>", n.Spec.Selector)
	}
	mode := n.Spec.Params["mode"]
	if mode == "" {
		mode = "drop"
	}
	if mode != "drop" && mode != "reject" {
		return fmt.Errorf("network_partition : mode %q : expected drop|reject", mode)
	}
	n.Logger.Info("applying network_partition",
		"name", n.Spec.Name, "az", az, "mode", mode)
	// TODO(weft-chaos) : wclient.PartitionAZ(ctx, az, mode).
	// Until the gRPC channel to weft-agent is wired this logs +
	// returns nil ; the scenario still records the timeline event.
	return nil
}

func (n *NetworkPartition) Revert(ctx context.Context) error {
	if n.Spec.RecoverAt == "" {
		return nil
	}
	az, _ := parseSelector(n.Spec.Selector, "az")
	n.Logger.Info("reverting network_partition",
		"name", n.Spec.Name, "az", az)
	// TODO(weft-chaos) : wclient.HealPartition(ctx, az).
	return nil
}
