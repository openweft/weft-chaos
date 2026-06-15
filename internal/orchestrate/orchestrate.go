// Package orchestrate is the glue between scenario blocks and the
// runtime goroutines that drive them. main.go does flag parsing +
// signal handling ; orchestrate.Run owns the lifecycle :
//
//   1. Build agents / injectors / invariants from sc.
//   2. Spawn each in its own goroutine on the run context.
//   3. Wait for the run context to end (deadline or SIGTERM).
//   4. Drain : give each invariant a final round to record
//      breaches, then write the timeline report.
//
// Pure-Go ; the cluster-touching seam is internal/wclient. A
// caller in unit tests can pass a fake-wclient.Client + a tiny
// scenario.Scenario + assert the report shape.

package orchestrate

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/openweft/weft-chaos/internal/agents"
	"github.com/openweft/weft-chaos/internal/injectors"
	"github.com/openweft/weft-chaos/internal/invariants"
	"github.com/openweft/weft-chaos/internal/metrics"
	"github.com/openweft/weft-chaos/internal/report"
	"github.com/openweft/weft-chaos/internal/scenario"
	"github.com/openweft/weft-chaos/internal/wclient"
)

// drainTimeout is how long invariants get after the run context
// expires before the report writer drains. 30 s is enough for one
// final 10 s tick + slack.
const drainTimeout = 30 * time.Second

// Options bundles every dependency Run needs. Constructor in main.go
// fills these from flags + the loaded Scenario.
type Options struct {
	Scenario     *scenario.Scenario
	Client       *wclient.Client
	Metrics      *metrics.Set
	Logger       *slog.Logger
	ScenarioPath string
	ReportPath   string
	StartedAt    time.Time
}

// Run owns the lifecycle. Blocks until ctx ends + the drain
// completes ; returns the first error from any goroutine OR
// from the report writer.
func Run(ctx context.Context, opts Options) error {
	var rec *invariants.Recorder
	if opts.Metrics != nil {
		rec = invariants.NewRecorderWithCounter(opts.Metrics.Breach)
	} else {
		rec = invariants.NewRecorder()
	}

	// Build the runtime objects. A construction error (e.g. an
	// invariant with bad params) is fatal — we want to fail loud
	// before any churn rather than silently skip.
	agentList, err := buildAgents(opts.Scenario, opts.Client, opts.Metrics, opts.Logger)
	if err != nil {
		return fmt.Errorf("build agents: %w", err)
	}
	injectorList, err := buildInjectors(opts.Scenario, opts.Client, opts.Logger)
	if err != nil {
		return fmt.Errorf("build injectors: %w", err)
	}
	invariantList, err := buildInvariants(opts.Scenario, opts.Client, opts.Logger)
	if err != nil {
		return fmt.Errorf("build invariants: %w", err)
	}

	opts.Logger.Info("orchestrator armed",
		"agents", len(agentList),
		"injectors", len(injectorList),
		"invariants", len(invariantList))

	// Spawn every goroutine on the run context. Each one drains
	// cleanly when ctx fires : agents stop their tickers, injectors
	// call their Revert() handler (if AtOffset already fired),
	// invariants exit their polling loop.
	var wg sync.WaitGroup
	for _, a := range agentList {
		wg.Add(1)
		go func(a *agents.Agent) {
			defer wg.Done()
			_ = a.Run(ctx)
		}(a)
	}
	for _, inj := range injectorList {
		wg.Add(1)
		go func(inj scheduledInjector) {
			defer wg.Done()
			inj.run(ctx, opts.Logger)
		}(inj)
	}
	for _, inv := range invariantList {
		wg.Add(1)
		go func(inv invariants.Invariant) {
			defer wg.Done()
			if err := inv.Run(ctx, rec); err != nil {
				opts.Logger.Error("invariant exited with error", "name", inv.Name(), "err", err.Error())
			}
		}(inv)
	}

	// Wait for either the scenario deadline (ctx) OR a goroutine
	// crash. wg.Wait blocks for both.
	wg.Wait()

	// Drain window : invariants ran in lockstep with ctx, so
	// they've already exited. The drain is a courtesy buffer for
	// any in-flight HTTP probes to land in the recorder before
	// we snapshot.
	time.Sleep(min(drainTimeout, 100*time.Millisecond))

	// Write the timeline.
	r := &report.Report{
		StartedAt:    opts.StartedAt,
		EndedAt:      time.Now().UTC(),
		ScenarioPath: opts.ScenarioPath,
		Invariants:   collectInvariantTimelines(opts.Scenario, rec),
	}
	if err := report.Write(r, opts.ReportPath); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	opts.Logger.Info("report written",
		"path", opts.ReportPath,
		"breaches", totalBreaches(r))
	return nil
}

// buildAgents instantiates one Agent per workload block. Cheap +
// pure ; no IO.
func buildAgents(sc *scenario.Scenario, client *wclient.Client, m *metrics.Set, logger *slog.Logger) ([]*agents.Agent, error) {
	out := make([]*agents.Agent, 0, len(sc.Workloads))
	for _, w := range sc.Workloads {
		// Validate the burst knobs at build time so a typo
		// surfaces before the run starts. Empty values are fine —
		// they mean "no burst window".
		if _, err := w.BurstEveryDuration(); err != nil {
			return nil, fmt.Errorf("workload %q: burst_every %q: %w", w.Name, w.BurstEvery, err)
		}
		if _, err := w.BurstForDuration(); err != nil {
			return nil, fmt.Errorf("workload %q: burst_for %q: %w", w.Name, w.BurstFor, err)
		}
		a := &agents.Agent{W: w, Logger: logger, Client: client}
		if m != nil {
			a.Dispatch = m.Dispatch
		}
		out = append(out, a)
	}
	return out, nil
}

// scheduledInjector pairs an Injector with its AtOffset / RecoverAt
// schedule. The orchestrator builds these so the goroutine knows
// when to Apply + when to Revert without re-parsing the block.
type scheduledInjector struct {
	inj       injectors.Injector
	atOffset  time.Duration
	recoverAt time.Duration
	hasRecover bool
}

func (s scheduledInjector) run(ctx context.Context, logger *slog.Logger) {
	// Sleep until AtOffset (cancellable). After that, Apply ;
	// if RecoverAt is set, sleep again until that offset + Revert.
	applyT := time.NewTimer(s.atOffset)
	defer applyT.Stop()
	select {
	case <-ctx.Done():
		return
	case <-applyT.C:
	}
	if err := s.inj.Apply(ctx); err != nil {
		logger.Error("injector Apply failed", "name", s.inj.Name(), "err", err.Error())
	}
	if !s.hasRecover {
		return
	}
	gap := s.recoverAt - s.atOffset
	if gap <= 0 {
		return
	}
	revertT := time.NewTimer(gap)
	defer revertT.Stop()
	select {
	case <-ctx.Done():
		// ctx cancelled mid-window : revert anyway so the
		// cluster doesn't stay in the chaos state past the run.
		if err := s.inj.Revert(context.Background()); err != nil {
			logger.Error("injector Revert (on cancel) failed",
				"name", s.inj.Name(), "err", err.Error())
		}
		return
	case <-revertT.C:
	}
	if err := s.inj.Revert(ctx); err != nil {
		logger.Error("injector Revert failed", "name", s.inj.Name(), "err", err.Error())
	}
}

// buildInjectors fans the scenario.Injector blocks out into typed
// runtime objects. New injector kinds register here.
func buildInjectors(sc *scenario.Scenario, client *wclient.Client, logger *slog.Logger) ([]scheduledInjector, error) {
	out := make([]scheduledInjector, 0, len(sc.Injectors))
	_ = client // wired into per-kind constructors as they need it
	for _, i := range sc.Injectors {
		at, err := i.AtOffsetDuration()
		if err != nil {
			return nil, fmt.Errorf("injector %q: at_offset %q: %w", i.Name, i.AtOffset, err)
		}
		recover, err := i.RecoverAtDuration()
		if err != nil {
			return nil, fmt.Errorf("injector %q: recover_at %q: %w", i.Name, i.RecoverAt, err)
		}
		var typed injectors.Injector
		switch i.Kind {
		case "host_cordon":
			typed = &injectors.HostCordon{Spec: i, Logger: logger}
		case "network_partition":
			typed = &injectors.NetworkPartition{Spec: i, Logger: logger}
		case "process_kill":
			typed = &injectors.ProcessKill{Spec: i, Logger: logger}
		default:
			return nil, fmt.Errorf("injector %q: unknown kind %q (known: host_cordon, network_partition, process_kill)", i.Name, i.Kind)
		}
		out = append(out, scheduledInjector{
			inj: typed, atOffset: at, recoverAt: recover, hasRecover: i.RecoverAt != "",
		})
	}
	return out, nil
}

// buildInvariants instantiates one Invariant per block. Each
// kind registers its constructor here ; the orchestrator stays
// agnostic of how the rule is checked.
func buildInvariants(sc *scenario.Scenario, client *wclient.Client, logger *slog.Logger) ([]invariants.Invariant, error) {
	out := make([]invariants.Invariant, 0, len(sc.Invariants))
	for _, v := range sc.Invariants {
		if _, err := v.WindowDuration(); err != nil {
			return nil, fmt.Errorf("invariant %q: window %q: %w", v.Name, v.Window, err)
		}
		var typed invariants.Invariant
		switch v.Kind {
		case "healthy_endpoint":
			typed = &invariants.HealthyEndpoint{Spec: v, Logger: logger, Client: client}
		case "audit_tenant_isolation":
			typed = &invariants.AuditTenantIsolation{Spec: v, Logger: logger}
		case "zombies_zero":
			typed = &invariants.ZombiesZero{Spec: v, Logger: logger, Client: client}
		case "bus_drops_zero":
			typed = &invariants.BusDropsZero{Spec: v, Logger: logger, Client: client}
		default:
			return nil, fmt.Errorf("invariant %q: unknown kind %q (known: healthy_endpoint, audit_tenant_isolation, zombies_zero, bus_drops_zero)", v.Name, v.Kind)
		}
		out = append(out, typed)
	}
	return out, nil
}

// collectInvariantTimelines walks the recorder's snapshot + groups
// breaches by invariant name. Used by the report writer.
func collectInvariantTimelines(sc *scenario.Scenario, rec *invariants.Recorder) []report.InvariantTimeline {
	all := rec.Snapshot()
	byName := make(map[string][]invariants.Breach, len(sc.Invariants))
	for _, b := range all {
		byName[b.Invariant] = append(byName[b.Invariant], b)
	}
	out := make([]report.InvariantTimeline, 0, len(sc.Invariants))
	for _, v := range sc.Invariants {
		out = append(out, report.InvariantTimeline{
			Name:     v.Name,
			Kind:     v.Kind,
			Breaches: byName[v.Name],
		})
	}
	return out
}

func totalBreaches(r *report.Report) int {
	n := 0
	for _, it := range r.Invariants {
		n += len(it.Breaches)
	}
	return n
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
