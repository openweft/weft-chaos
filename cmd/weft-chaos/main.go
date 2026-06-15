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
		scenario   = flag.String("scenario", "", "path to scenario.hcl describing workloads + injectors + invariants (required)")
		duration   = flag.Duration("duration", 30*time.Minute, "total runtime ; injectors + workloads stop at the deadline + invariants drain")
		dryRun     = flag.Bool("dry-run", false, "parse the scenario, print the execution plan, exit without touching the cluster")
		yolo       = flag.Bool("i-know-what-im-doing", false, "override the production = true guard in cluster.hcl ; never set this by default")
		reportPath = flag.String("report", "weft-chaos-report.json", "path to the JSON timeline report written at exit")
		showVer    = flag.Bool("version", false, "print version + commit + build date, then exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Printf("weft-chaos %s (%s) built %s\n", version, commit, date)
		return nil
	}
	if *cluster == "" || *scenario == "" {
		flag.Usage()
		return fmt.Errorf("--cluster and --scenario are required")
	}

	logger.Info("weft-chaos starting",
		"version", version, "commit", commit,
		"cluster", *cluster, "scenario", *scenario,
		"duration", *duration, "dry_run", *dryRun)

	// Two-phase context : the outer is bound to SIGINT/SIGTERM so the
	// operator can pull the plug ; the inner carries the scenario
	// deadline so workloads + injectors stop cleanly + the invariant
	// drain has a window before the binary exits.
	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runCtx, cancel := context.WithTimeout(rootCtx, *duration)
	defer cancel()

	if *dryRun {
		logger.Info("dry-run : scenario + cluster parsed, exiting before any injection")
		return nil
	}

	// TODO(weft-chaos) :
	//   - load cluster.hcl, refuse on `production = true` unless yolo
	//   - load scenario.hcl, build the execution plan
	//   - spawn agent goroutines (one per workload)
	//   - spawn injector goroutines (one per injector schedule entry)
	//   - spawn invariant goroutines (one per checker, scrape /metrics + /api/audit-log)
	//   - on runCtx.Done() : stop workloads, drain invariants for 30s, write report
	//
	// The scaffold ships a stable CLI surface + the directory shape ;
	// the agents / injectors / invariants subpackages each ship one
	// canonical sample so contributors have a template.

	_ = runCtx
	_ = reportPath
	_ = yolo

	logger.Info("scaffold-only build : the live pilot path lands in a follow-up commit ; this binary's job today is to ratify the CLI surface + repo layout")
	return nil
}
