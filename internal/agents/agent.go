// Package agents drives the workload side of a chaos run. One Agent
// per scenario.Workload entry ; agents issue mutations against the
// cluster's /api surface at the configured RPS, alternating between
// steady-state and burst windows.
//
// Each agent runs as a single goroutine. The runner cancels its
// ctx at scenario deadline or on SIGINT ; agents drain in-flight
// requests, emit their final counters, and exit.
//
// Scaffold-only : the cluster client lives in internal/wclient ; this
// package's job is the cadence + the resource-mix dispatch. The full
// driver per resource (microvm / volume / network / SG / DNS / etc.)
// lands as follow-up commits, one resource per turn so each driver
// gets focused review.

package agents

import (
	"context"
	"log/slog"
	"time"

	"github.com/openweft/weft-chaos/internal/scenario"
)

// Agent is a single tenant-scoped workload driver. The Run method
// blocks until ctx is cancelled.
type Agent struct {
	W      scenario.Workload
	Logger *slog.Logger
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
			a.dispatchOne()
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

// dispatchOne picks one of the workload's configured resources +
// fires a single CRUD round. The actual cluster-touching call lives
// in internal/wclient ; this scaffold spins in place to surface the
// agent shape without driving traffic.
func (a *Agent) dispatchOne() {
	if len(a.W.Resources) == 0 {
		return
	}
	// TODO(weft-chaos) : pick resource via round-robin or weighted,
	// dispatch into wclient.Do(resourceKind, tenant) which encapsulates
	// the CRUD round (create → maybe-update → delete) per resource.
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
