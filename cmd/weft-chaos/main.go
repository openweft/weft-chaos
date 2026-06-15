// Command weft-chaos drives a live openweft cluster with deterministic
// + random churn to surface dynamic-state bugs.
//
// Verbs (cobra) :
//
//	weft-chaos run     drive the cluster + write the timeline report
//	weft-chaos plan    parse the scenario, print the plan, exit clean
//	weft-chaos version print version + commit + build date
//
// Pattern : pure-Go, CGO=0, ships as a 4-arch OCI image. Mirrors the
// weft-router cmd/main.go skeleton ; UX is identical across openweft
// CLIs (cobra subcommands + Aliases on ls).
//
// Safety : refuses to run against a cluster whose cluster.hcl carries
// `production = true` unless --i-know-what-im-doing is set. Every
// destructive injector logs an audit-style event so the cluster's own
// audit log records the chaos.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/openweft/weft-chaos/internal/agents"
	"github.com/openweft/weft-chaos/internal/cluster"
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
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

// newRootCmd builds the `weft-chaos` cobra root + attaches every
// subcommand. Factored so tests can inspect the wire-up without
// invoking the real main().
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "weft-chaos",
		Short: "Drive an openweft cluster with churn + watch invariants",
		Long: "weft-chaos drives a live openweft cluster with deterministic + random " +
			"churn (creation/destruction across resources, multi-tenant, AZ partitions) " +
			"to surface dynamic-state bugs.",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newRunCmd())
	root.AddCommand(newPlanCmd())
	root.AddCommand(newVersionCmd())
	return root
}

// runFlags holds the bag of options shared by `run` ; defaults are
// applied via cobra's StringVarP / DurationVarP.
type runFlags struct {
	clusterPath   string
	scenarioPath  string
	duration      time.Duration
	yolo          bool
	reportPath    string
	portalURL     string
	token         string
	metricsListen string
}

func newRunCmd() *cobra.Command {
	rf := &runFlags{}
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Drive the cluster + write the timeline report at exit",
		RunE: func(_ *cobra.Command, _ []string) error {
			return doRun(rf)
		},
	}
	cmd.Flags().StringVar(&rf.clusterPath, "cluster", "", "path to cluster.hcl (required)")
	cmd.Flags().StringVar(&rf.scenarioPath, "scenario", "", "path to scenario.hcl describing workloads + injectors + invariants (required)")
	cmd.Flags().DurationVar(&rf.duration, "duration", 30*time.Minute, "total runtime ; injectors + workloads stop at the deadline + invariants drain")
	cmd.Flags().BoolVar(&rf.yolo, "i-know-what-im-doing", false, "override the production = true guard in cluster.hcl ; never set this by default")
	cmd.Flags().StringVar(&rf.reportPath, "report", "weft-chaos-report.json", "path to the JSON timeline report written at exit")
	cmd.Flags().StringVar(&rf.portalURL, "portal-url", "", "base URL of the weft cluster portal ; empty = use cluster.hcl portal_url")
	cmd.Flags().StringVar(&rf.token, "token", "", "bearer token sent on resource calls ; empty = no Authorization header")
	cmd.Flags().StringVar(&rf.metricsListen, "metrics-listen", "", "host:port to serve /metrics on (e.g. :7771) ; empty = no listener")
	_ = cmd.MarkFlagRequired("cluster")
	_ = cmd.MarkFlagRequired("scenario")
	return cmd
}

func newPlanCmd() *cobra.Command {
	var scenarioPath string
	var clusterPath string
	cmd := &cobra.Command{
		Use:     "plan",
		Aliases: []string{"validate"},
		Short:   "Parse the scenario + cluster, print the plan, exit clean",
		RunE: func(_ *cobra.Command, _ []string) error {
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
			if clusterPath != "" {
				cc, err := cluster.Load(clusterPath)
				if err != nil {
					return err
				}
				logger.Info("cluster loaded",
					"name", cc.Name, "production", cc.Production,
					"portal_url", cc.PortalURL)
			}
			sc, err := scenario.Load(scenarioPath)
			if err != nil {
				return err
			}
			logger.Info("scenario loaded",
				"workloads", len(sc.Workloads),
				"injectors", len(sc.Injectors),
				"invariants", len(sc.Invariants))
			printPlan(logger, sc)
			return nil
		},
	}
	cmd.Flags().StringVar(&scenarioPath, "scenario", "", "path to scenario.hcl (required)")
	cmd.Flags().StringVar(&clusterPath, "cluster", "", "path to cluster.hcl (optional ; parsed + summarised when provided)")
	_ = cmd.MarkFlagRequired("scenario")
	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version + commit + build date",
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Printf("weft-chaos %s (%s) built %s\n", version, commit, date)
		},
	}
}

// doRun is the live-cluster path : load cluster + scenario, enforce
// the production guard, spin the orchestrator. Pure function of
// runFlags so tests can pass an in-memory bag without parsing argv.
func doRun(rf *runFlags) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger.Info("weft-chaos starting",
		"version", version, "commit", commit,
		"cluster", rf.clusterPath, "scenario", rf.scenarioPath,
		"duration", rf.duration)

	// Two-phase context : the outer is bound to SIGINT/SIGTERM so the
	// operator can pull the plug ; the inner carries the scenario
	// deadline so workloads + injectors stop cleanly + the invariant
	// drain has a window before the binary exits.
	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runCtx, cancel := context.WithTimeout(rootCtx, rf.duration)
	defer cancel()

	clusterCfg, err := cluster.Load(rf.clusterPath)
	if err != nil {
		return err
	}
	logger.Info("cluster loaded",
		"name", clusterCfg.Name,
		"production", clusterCfg.Production,
		"portal_url", clusterCfg.PortalURL)
	sc, err := scenario.Load(rf.scenarioPath)
	if err != nil {
		return err
	}
	if err := sc.Validate(); err != nil {
		return err
	}
	logger.Info("scenario loaded",
		"workloads", len(sc.Workloads),
		"injectors", len(sc.Injectors),
		"invariants", len(sc.Invariants))
	warnUnsupportedResources(logger, sc)

	// Production guard : refuse to run against a cluster marked
	// `production = true` in cluster.hcl unless the operator
	// explicitly passed --i-know-what-im-doing.
	if err := clusterCfg.ConfirmDestructive(rf.yolo); err != nil {
		return err
	}

	// cluster.hcl's portal_url wins over the empty default. The
	// CLI flag still trumps the file so an operator can point a
	// shared cluster.hcl at an alternate portal for testing.
	resolvedPortalURL := clusterCfg.PortalURL
	if rf.portalURL != "" {
		resolvedPortalURL = rf.portalURL
	}

	client := wclient.New(logger)
	client.PortalURL = resolvedPortalURL
	client.Token = rf.token
	if err := client.Dial(runCtx); err != nil {
		return fmt.Errorf("wclient: %w", err)
	}

	metricsSet := metrics.New()
	if rf.metricsListen != "" {
		addr, stopFn, err := metricsSet.ServeBackground(rf.metricsListen)
		if err != nil {
			return fmt.Errorf("metrics listener : %w", err)
		}
		logger.Info("metrics listener up", "addr", addr)
		defer func() {
			if err := stopFn(context.Background()); err != nil {
				logger.Warn("metrics shutdown error", "err", err.Error())
			}
		}()
	}

	return orchestrate.Run(runCtx, orchestrate.Options{
		Scenario:     sc,
		Client:       client,
		Metrics:      metricsSet,
		Logger:       logger,
		ScenarioPath: rf.scenarioPath,
		ClusterName:  clusterCfg.Name,
		ReportPath:   rf.reportPath,
		StartedAt:    time.Now().UTC(),
	})
}

// warnUnsupportedResources walks every workload + logs a Warn for
// each resource kind that no dispatch case handles. Dispatch will
// still mark these "unsupported" + bump the counter, but a typo
// (e.g. `"microvms"` vs `"microvm"`) deserves an upfront warning
// rather than silent counter growth that an operator has to grep
// out of /metrics to find.
func warnUnsupportedResources(logger *slog.Logger, sc *scenario.Scenario) {
	supported := map[string]bool{}
	for _, k := range agents.SupportedResources() {
		supported[k] = true
	}
	for _, w := range sc.Workloads {
		for _, r := range w.Resources {
			if !supported[r] {
				logger.Warn("workload references unsupported resource",
					"workload", w.Name, "resource", r,
					"supported", agents.SupportedResources())
			}
		}
	}
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
