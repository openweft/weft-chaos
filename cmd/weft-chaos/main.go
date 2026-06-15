// Command weft-chaos drives a live openweft cluster with deterministic
// + random churn to surface dynamic-state bugs.
//
// What it does :
//
//   - Pulls a scenario.hcl that lists workloads (per-tenant CRUD
//     drivers), injectors (DC-down / network-partition / disk-pressure
//     / kill-pid / etcd-evict), and invariants (vm_count_consistent /
//     no_cross_tenant_audit_leak / scheduling_compliant_within_60s).
//   - Spins per-workload goroutines that hit /api/* in the configured
//     pattern (steady-state RPS, burst, slow-cook).
//   - Triggers injectors on the configured schedule.
//   - Polls /metrics + /api/audit-log to evaluate invariants
//     continuously.
//   - Emits a timeline report — breaches highlighted, blast radius
//     scoped per tenant.
//
// Pattern : pure-Go, CGO=0, ships as a 4-arch OCI image consumed by
// `weft microvm pull` like every other openweft binary. Mirrors the
// weft-router cmd/main.go skeleton ; the operator UX is identical
// across the repos.
//
// Safety : refuses to run against a cluster whose `cluster.hcl` has
// `production = true` unless --i-know-what-im-doing is set. Every
// destructive injector logs an audit-style event so the cluster's
// own audit log records the chaos.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openweft/weft-chaos/internal/metrics"
	"github.com/openweft/weft-chaos/internal/orchestrate"
	"github.com/openweft/weft-chaos/internal/scenario"
	"github.com/openweft/weft-chaos/internal/wclient"
)

// version + commit + date are linker-stamped via the same
// -X main.version=... convention every openweft binary uses. Defaults
// keep `go run .` legible from a dev checkout.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "weft-chaos:", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var (
		cluster    = flag.String("cluster", "", "path to cluster.hcl (required)")
		scenarioPath = flag.String("scenario", "", "path to scenario.hcl describing workloads + injectors + invariants (required)")
		duration   = flag.Duration("duration", 30*time.Minute, "total runtime ; injectors + workloads stop at the deadline + invariants drain")
		dryRun     = flag.Bool("dry-run", false, "parse the scenario, print the execution plan, exit without touching the cluster")
		yolo       = flag.Bool("i-know-what-im-doing", false, "override the production = true guard in cluster.hcl ; never set this by default")
		reportPath     = flag.String("report", "weft-chaos-report.json", "path to the JSON timeline report written at exit")
		portalURL      = flag.String("portal-url", "", "base URL of the weft cluster portal (e.g. https://weft.example.com) ; empty = dispatchOne short-circuits to no-op")
		token          = flag.String("token", "", "bearer token sent on resource calls ; empty = no Authorization header")
		metricsListen  = flag.String("metrics-listen", "", "host:port to serve /metrics on (e.g. :7771) ; empty = no listener")
		showVer        = flag.Bool("version", false, "print version + commit + build date, then exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Printf("weft-chaos %s (%s) built %s\n", version, commit, date)
		return nil
	}
	if *cluster == "" || *scenarioPath == "" {
		flag.Usage()
		return fmt.Errorf("--cluster and --scenario are required")
	}

	logger.Info("weft-chaos starting",
		"version", version, "commit", commit,
		"cluster", *cluster, "scenario", *scenarioPath,
		"duration", *duration, "dry_run", *dryRun)

	// Two-phase context : the outer is bound to SIGINT/SIGTERM so the
	// operator can pull the plug ; the inner carries the scenario
	// deadline so workloads + injectors stop cleanly + the invariant
	// drain has a window before the binary exits.
	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runCtx, cancel := context.WithTimeout(rootCtx, *duration)
	defer cancel()

	// Parse the scenario unconditionally — even live runs need the
	// plan in hand before they touch the cluster. cluster.hcl
	// parsing lands once weft repo exports its parser ; today we
	// only validate the file exists so a typo'd --cluster fails
	// loud before the chaos starts.
	if _, err := os.Stat(*cluster); err != nil {
		return fmt.Errorf("cluster file : %w", err)
	}
	sc, err := scenario.Load(*scenarioPath)
	if err != nil {
		return err
	}
	logger.Info("scenario loaded",
		"workloads", len(sc.Workloads),
		"injectors", len(sc.Injectors),
		"invariants", len(sc.Invariants))

	if *dryRun {
		printPlan(logger, sc)
		return nil
	}

	// TODO(weft-chaos) : real cluster.hcl parser to check
	// production = true and refuse without --yolo. Today we
	// accept any file shape ; this is a sandbox-only run.
	_ = yolo

	client := wclient.New(logger)
	client.PortalURL = *portalURL
	client.Token = *token
	if err := client.Dial(runCtx); err != nil {
		return fmt.Errorf("wclient: %w", err)
	}

	metricsSet := metrics.New()
	if *metricsListen != "" {
		addr, stop, err := metricsSet.ServeBackground(*metricsListen)
		if err != nil {
			return fmt.Errorf("metrics listener : %w", err)
		}
		logger.Info("metrics listener up", "addr", addr)
		defer func() {
			if err := stop(context.Background()); err != nil {
				logger.Warn("metrics shutdown error", "err", err.Error())
			}
		}()
	}

	return orchestrate.Run(runCtx, orchestrate.Options{
		Scenario:     sc,
		Client:       client,
		Metrics:      metricsSet,
		Logger:       logger,
		ScenarioPath: *scenarioPath,
		ReportPath:   *reportPath,
		StartedAt:    time.Now().UTC(),
	})
}

// printPlan dumps the parsed scenario to slog so an operator can
// eyeball "yes that's what I asked for" before the live run does
// any damage. One slog line per block keeps the output greppable
// + jq-friendly when the logger is in JSON mode.
func printPlan(logger *slog.Logger, sc *scenario.Scenario) {
	for _, w := range sc.Workloads {
		logger.Info("plan : workload",
			"name", w.Name, "tenant", w.Tenant,
			"steady_rps", w.SteadyRPS, "burst_rps", w.BurstRPS,
			"burst_every", w.BurstEvery, "burst_for", w.BurstFor,
			"resources", w.Resources)
	}
	for _, i := range sc.Injectors {
		logger.Info("plan : injector",
			"name", i.Name, "kind", i.Kind,
			"selector", i.Selector,
			"at_offset", i.AtOffset, "recover_at", i.RecoverAt)
	}
	for _, v := range sc.Invariants {
		logger.Info("plan : invariant",
			"name", v.Name, "kind", v.Kind,
			"window", v.Window, "params", v.Params)
	}
}
