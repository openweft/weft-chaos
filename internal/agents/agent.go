// Package agents drives the workload side of a chaos run. One Agent
// per scenario.Workload entry ; agents issue mutations against the
// cluster's /api surface at the configured RPS, alternating between
// steady-state and burst windows.
//
// Each agent runs as a single goroutine. The runner cancels its
// ctx at scenario deadline or on SIGINT ; agents drain in-flight
// requests, emit their final counters, and exit.
//
// What's wired today : microvm CREATE / DELETE rounds through
// wclient.CreateMicroVM/DeleteMicroVM. With an empty PortalURL the
// wclient calls short-circuit to nil, so chaos still exercises its
// own dispatch loop + bumps its own counters without a real cluster.
// Other resources (volume / network / SG / DNS / etc.) land as
// follow-up commits, one resource per turn so each driver gets
// focused review.

package agents

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/openweft/weft-chaos/internal/scenario"
	"github.com/openweft/weft-chaos/internal/wclient"
)

// Agent is a single tenant-scoped workload driver. The Run method
// blocks until ctx is cancelled.
type Agent struct {
	W      scenario.Workload
	Logger *slog.Logger
	// Client touches the cluster. Mandatory ; pass a no-op client
	// (PortalURL == "") in tests + dry-run smokes.
	Client *wclient.Client
	// Dispatch counter (labels : resource, verb, result). Optional ;
	// when nil the agent still drives but no metrics are emitted.
	Dispatch *prometheus.CounterVec

	// resourceIdx is the round-robin cursor over W.Resources. Each
	// dispatchOne advances it ; this keeps the resource mix
	// deterministic + auditable from the log stream.
	resourceIdx int
	// dispatched is the monotonic tick count used to derive a name
	// suffix per CREATE call + alternate verb pick.
	dispatched int
}

// Run drives the workload at SteadyRPS, switching to BurstRPS every
// BurstEvery for BurstFor. Cancellation drains gracefully.
func (a *Agent) Run(ctx context.Context) error {
	steady := time.Second / time.Duration(max1(a.W.SteadyRPS))
	burst := time.Duration(0)
	if a.W.BurstRPS > 0 {
		burst = time.Second / time.Duration(a.W.BurstRPS)
	}
	burstEvery, _ := a.W.BurstEveryDuration()
	burstFor, _ := a.W.BurstForDuration()

	// State : are we currently in a burst window ?
	bursting := false
	burstStart := time.Now()
	lastBurst := time.Now()

	tick := time.NewTimer(steady)
	defer tick.Stop()

	a.Logger.Info("agent up",
		"workload", a.W.Name, "tenant", a.W.Tenant,
		"steady_rps", a.W.SteadyRPS, "burst_rps", a.W.BurstRPS,
		"resources", a.W.Resources)

	for {
		select {
		case <-ctx.Done():
			a.Logger.Info("agent drained", "workload", a.W.Name)
			return nil
		case <-tick.C:
			a.dispatchOne(ctx)
			now := time.Now()
			if burstEvery > 0 && !bursting && now.Sub(lastBurst) >= burstEvery {
				bursting = true
				burstStart = now
			}
			if bursting && now.Sub(burstStart) >= burstFor {
				bursting = false
				lastBurst = now
			}
			next := steady
			if bursting && burst > 0 {
				next = burst
			}
			tick.Reset(next)
		}
	}
}

// dispatchOne picks the next resource (round-robin) + fires one
// CRUD round. Verb alternates CREATE → DELETE → CREATE → … so a
// run leaves the cluster at a similar resource count to where it
// started.
func (a *Agent) dispatchOne(ctx context.Context) {
	if len(a.W.Resources) == 0 {
		return
	}
	resource := a.W.Resources[a.resourceIdx%len(a.W.Resources)]
	a.resourceIdx++

	verb := "create"
	if a.dispatched%2 == 1 {
		verb = "delete"
	}
	a.dispatched++

	name := fmt.Sprintf("chaos-%s-%d", a.W.Name, a.dispatched)

	result := a.dispatch(ctx, resource, verb, name)
	if a.Dispatch != nil {
		a.Dispatch.WithLabelValues(resource, verb, result).Inc()
	}
}

// dispatch routes one (resource, verb) tuple to the right wclient
// method + returns a Prometheus-friendly result label.
func (a *Agent) dispatch(ctx context.Context, resource, verb, name string) string {
	var err error
	switch resource {
	case "microvm":
		switch verb {
		case "create":
			err = a.Client.CreateMicroVM(ctx, a.W.Tenant, name)
		case "delete":
			err = a.Client.DeleteMicroVM(ctx, a.W.Tenant, name)
		}
	case "volume":
		switch verb {
		case "create":
			err = a.Client.CreateVolume(ctx, a.W.Tenant, name)
		case "delete":
			err = a.Client.DeleteVolume(ctx, a.W.Tenant, name)
		}
	case "network":
		switch verb {
		case "create":
			err = a.Client.CreateNetwork(ctx, a.W.Tenant, name)
		case "delete":
			err = a.Client.DeleteNetwork(ctx, a.W.Tenant, name)
		}
	case "security-group":
		switch verb {
		case "create":
			err = a.Client.CreateSecurityGroup(ctx, a.W.Tenant, name)
		case "delete":
			err = a.Client.DeleteSecurityGroup(ctx, a.W.Tenant, name)
		}
	case "dns-zone":
		switch verb {
		case "create":
			err = a.Client.CreateDNSZone(ctx, a.W.Tenant, name)
		case "delete":
			err = a.Client.DeleteDNSZone(ctx, a.W.Tenant, name)
		}
	default:
		// Driver not yet implemented for this resource kind. The
		// counter records "unsupported" so an operator can see how
		// many rounds the scenario intended vs. how many actually
		// touched the cluster.
		return "unsupported"
	}
	if err != nil {
		a.Logger.Debug("dispatch failed",
			"workload", a.W.Name, "resource", resource,
			"verb", verb, "err", err.Error())
		return "error"
	}
	return "ok"
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
