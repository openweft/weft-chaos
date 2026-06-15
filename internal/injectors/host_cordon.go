// Package injectors lists the failure modes the chaos run can apply
// to a live cluster. Each injector takes a *scenario.Injector spec
// + drives the cluster's mutation API (cordon hosts, drop NICs,
// fill volumes, etc.) on the scheduled offset, then optionally
// reverses the action when RecoverAt fires.
//
// Conventions :
//
//   - Injectors NEVER touch the cluster directly ; they go through
//     internal/wclient so every action is traced + auditable from
//     the cluster's own /api/audit-log.
//   - The Apply / Revert pair is idempotent : a re-run with the
//     same selector that's already in the target state is a no-op.
//   - Every injector publishes a Prometheus counter so a Grafana
//     panel can plot "what we did to the cluster" alongside the
//     dashboard's own breach metrics.
//
// This file ships the canonical sample : host_cordon — the
// equivalent of "kill the DC2 hosts as far as the scheduler is
// concerned". Other injectors (network_partition, disk_pressure,
// kill_pid, etcd_evict) land as follow-up commits, one per turn.

package injectors

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/openweft/weft-chaos/internal/scenario"
)

// Injector is the runtime interface. Apply is called at the
// AtOffset boundary, Revert at RecoverAt (if set ; nil-Revert when
// the spec is meant to be permanent for the run's duration).
type Injector interface {
	Name() string
	Apply(ctx context.Context) error
	Revert(ctx context.Context) error
}

// HostCordon implements `kind = "host_cordon"`. Marks every host
// matching the selector as draining ; the scheduler stops picking
// it for new placements, but already-running VMs stay put. Revert
// uncordons.
type HostCordon struct {
	Spec   scenario.Injector
	Logger *slog.Logger
	// TODO(weft-chaos) : wclient.Client handle for the real cluster.
	// Inject via a constructor so unit tests can pass a fake.
}

// Name returns the operator-facing injector name from the scenario.
func (h *HostCordon) Name() string { return h.Spec.Name }

// Apply cordons every host matching Spec.Selector.
func (h *HostCordon) Apply(ctx context.Context) error {
	az, ok := parseSelector(h.Spec.Selector, "az")
	if !ok {
		return fmt.Errorf("host_cordon : selector %q : expected az=<name>", h.Spec.Selector)
	}
	h.Logger.Info("applying host_cordon", "name", h.Spec.Name, "az", az)
	// TODO(weft-chaos) : iterate hosts where az == az, call
	// wclient.CordonHost(ctx, uuid). Log per-host outcome ; if
	// any individual cordon errs, log + continue ; the test we
	// want to run is "scheduler reacts to MOST hosts gone", not
	// "every API call succeeded".
	return nil
}

// Revert uncordons the same set.
func (h *HostCordon) Revert(ctx context.Context) error {
	if h.Spec.RecoverAt == "" {
		return nil
	}
	az, _ := parseSelector(h.Spec.Selector, "az")
	h.Logger.Info("reverting host_cordon", "name", h.Spec.Name, "az", az)
	// TODO(weft-chaos) : iterate + wclient.UncordonHost(ctx, uuid).
	return nil
}

// parseSelector pulls a single `key=value` out of a comma-separated
// selector string. Returns ("", false) when key is absent. Enough
// for the v0 injectors ; richer selectors (multi-key AND / OR) land
// once a scenario actually needs them.
func parseSelector(sel, key string) (string, bool) {
	for _, kv := range strings.Split(sel, ",") {
		kv = strings.TrimSpace(kv)
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		if strings.TrimSpace(kv[:eq]) == key {
			return strings.TrimSpace(kv[eq+1:]), true
		}
	}
	return "", false
}
