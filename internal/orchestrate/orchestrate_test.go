// orchestrate_test.go — pin the factory funcs + the end-to-end
// Run path against a tiny in-process scenario. The orchestrator
// is the glue ; misbuilding it is the single highest-impact
// regression so the unit tests are heavier here than for the
// individual injectors/invariants.

package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openweft/weft-chaos/internal/scenario"
	"github.com/openweft/weft-chaos/internal/wclient"
)

func nullLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestBuildAgents_ValidatesBurstDuration(t *testing.T) {
	// A scenario with a bogus burst_every should fail the build,
	// not silently skip the workload.
	sc := &scenario.Scenario{
		Workloads: []scenario.Workload{
			{Name: "bad", Tenant: "x", SteadyRPS: 1, BurstEvery: "not-a-duration"},
		},
	}
	_, err := buildAgents(sc, wclient.New(nullLogger()), nil, nullLogger())
	if err == nil {
		t.Fatal("buildAgents on bad burst_every = nil, want err")
	}
}

func TestBuildInjectors_UnknownKindRejected(t *testing.T) {
	sc := &scenario.Scenario{
		Injectors: []scenario.Injector{
			{Name: "alien", Kind: "make_the_sun_explode", AtOffset: "1s"},
		},
	}
	_, err := buildInjectors(sc, wclient.New(nullLogger()), nullLogger())
	if err == nil {
		t.Fatal("buildInjectors on unknown kind = nil, want err")
	}
}

func TestBuildInvariants_UnknownKindRejected(t *testing.T) {
	sc := &scenario.Scenario{
		Invariants: []scenario.Invariant{
			{Name: "alien", Kind: "tea_temperature"},
		},
	}
	_, err := buildInvariants(sc, wclient.New(nullLogger()), nullLogger())
	if err == nil {
		t.Fatal("buildInvariants on unknown kind = nil, want err")
	}
}

func TestBuildInjectors_ProcessKillWired(t *testing.T) {
	sc := &scenario.Scenario{
		Injectors: []scenario.Injector{
			{Name: "kill-agent", Kind: "process_kill",
				Selector: "az=dc2", AtOffset: "1s", RecoverAt: "2s"},
		},
	}
	list, err := buildInjectors(sc, wclient.New(nullLogger()), nullLogger())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].inj.Name() != "kill-agent" {
		t.Errorf("buildInjectors process_kill mis-wired : %+v", list)
	}
}

func TestBuildInjectors_NetworkPartitionWired(t *testing.T) {
	sc := &scenario.Scenario{
		Injectors: []scenario.Injector{
			{Name: "dc2-isolated", Kind: "network_partition",
				Selector: "az=dc2", AtOffset: "1s", RecoverAt: "2s"},
		},
	}
	list, err := buildInjectors(sc, wclient.New(nullLogger()), nullLogger())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].inj.Name() != "dc2-isolated" {
		t.Errorf("buildInjectors network_partition mis-wired : %+v", list)
	}
}

func TestBuildInvariants_BusDropsZeroWired(t *testing.T) {
	sc := &scenario.Scenario{
		Invariants: []scenario.Invariant{
			{Name: "drops", Kind: "bus_drops_zero", Window: "1s",
				Params: map[string]string{"url": "http://localhost:7770/metrics"}},
		},
	}
	list, err := buildInvariants(sc, wclient.New(nullLogger()), nullLogger())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name() != "drops" {
		t.Errorf("buildInvariants bus_drops_zero mis-wired : %+v", list)
	}
}

func TestBuildInvariants_ZombiesZeroWired(t *testing.T) {
	sc := &scenario.Scenario{
		Invariants: []scenario.Invariant{
			{Name: "zombies", Kind: "zombies_zero", Window: "1s",
				Params: map[string]string{"url": "http://localhost:7770/metrics"}},
		},
	}
	list, err := buildInvariants(sc, wclient.New(nullLogger()), nullLogger())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name() != "zombies" {
		t.Errorf("buildInvariants zombies_zero mis-wired : %+v", list)
	}
}

func TestBuildInvariants_HealthyEndpointWired(t *testing.T) {
	sc := &scenario.Scenario{
		Invariants: []scenario.Invariant{
			{Name: "alive", Kind: "healthy_endpoint", Window: "1s",
				Params: map[string]string{"urls": "http://localhost:80"}},
		},
	}
	list, err := buildInvariants(sc, wclient.New(nullLogger()), nullLogger())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name() != "alive" {
		t.Errorf("buildInvariants returned %+v, want one entry named alive", list)
	}
}

// TestRun_EndToEnd_ReportsBreaches drives the full orchestrator
// against a fake cluster URL that 503s. Report MUST list at
// least one breach for the healthy_endpoint invariant.
func TestRun_EndToEnd_ReportsBreaches(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(down.Close)

	sc := &scenario.Scenario{
		Invariants: []scenario.Invariant{
			{Name: "alive", Kind: "healthy_endpoint", Window: "200ms",
				Params: map[string]string{"urls": down.URL}},
		},
	}
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err := Run(ctx, Options{
		Scenario:     sc,
		Client:       wclient.New(nullLogger()),
		Logger:       nullLogger(),
		ScenarioPath: "scenarios/fake.hcl",
		ReportPath:   reportPath,
		StartedAt:    time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}

	b, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Invariants []struct {
			Name     string
			Breaches []struct{ Detail string }
		}
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Invariants) != 1 {
		t.Fatalf("report.invariants = %d, want 1", len(got.Invariants))
	}
	if len(got.Invariants[0].Breaches) == 0 {
		t.Errorf("report has no breaches ; expected at least 1 from a 503 endpoint")
	}
}

// TestRun_EndToEnd_HealthyEndpointNoBreach : the negative side
// of the matrix above — a 200-returning server produces an empty
// breach list.
func TestRun_EndToEnd_HealthyEndpointNoBreach(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ok.Close)

	sc := &scenario.Scenario{
		Invariants: []scenario.Invariant{
			{Name: "alive", Kind: "healthy_endpoint", Window: "200ms",
				Params: map[string]string{"urls": ok.URL}},
		},
	}
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := Run(ctx, Options{
		Scenario: sc, Client: wclient.New(nullLogger()), Logger: nullLogger(),
		ScenarioPath: "scenarios/fake.hcl", ReportPath: reportPath, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(reportPath)
	var got struct {
		Invariants []struct {
			Breaches []struct{}
		}
	}
	_ = json.Unmarshal(b, &got)
	if len(got.Invariants) != 1 {
		t.Fatalf("invariants = %d, want 1", len(got.Invariants))
	}
	if n := len(got.Invariants[0].Breaches); n != 0 {
		t.Errorf("breaches on a 200 endpoint = %d, want 0", n)
	}
}

// TestScheduledInjector_AppliesAtOffset confirms the timer math
// of the scheduledInjector — Apply fires at AtOffset, Revert at
// RecoverAt. Uses a tiny fake injector that just bumps counters.
func TestScheduledInjector_AppliesAtOffset(t *testing.T) {
	fake := &countInjector{}
	si := scheduledInjector{
		inj:        fake,
		atOffset:   50 * time.Millisecond,
		recoverAt:  150 * time.Millisecond,
		hasRecover: true,
		event:      &injectorEvent{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	si.run(ctx, nullLogger())
	if fake.applied != 1 {
		t.Errorf("Apply count = %d, want 1", fake.applied)
	}
	if fake.reverted != 1 {
		t.Errorf("Revert count = %d, want 1", fake.reverted)
	}
	applied, reverted, _ := si.event.snapshot()
	if applied.IsZero() {
		t.Error("event.appliedAt not stamped")
	}
	if reverted.IsZero() {
		t.Error("event.revertedAt not stamped")
	}
	if !reverted.After(applied) {
		t.Errorf("reverted (%v) not after applied (%v)", reverted, applied)
	}
}

// TestScheduledInjector_RevertOnEarlyCancel : if ctx cancels
// between Apply and Revert, the orchestrator MUST still run
// Revert (so the cluster doesn't stay in the chaos state).
func TestScheduledInjector_RevertOnEarlyCancel(t *testing.T) {
	fake := &countInjector{}
	si := scheduledInjector{
		inj:        fake,
		atOffset:   50 * time.Millisecond,
		recoverAt:  10 * time.Second, // far past the cancel
		hasRecover: true,
		event:      &injectorEvent{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	si.run(ctx, nullLogger())
	if fake.applied != 1 {
		t.Errorf("Apply = %d, want 1", fake.applied)
	}
	if fake.reverted != 1 {
		t.Errorf("Revert-on-cancel = %d, want 1", fake.reverted)
	}
	applied, reverted, _ := si.event.snapshot()
	if applied.IsZero() || reverted.IsZero() {
		t.Errorf("revert-on-cancel : applied=%v reverted=%v, both should be stamped", applied, reverted)
	}
}

// TestScheduledInjector_ApplyErrorRecordedInDetail : a failing
// Apply must surface in event.detail so the report explains why.
func TestScheduledInjector_ApplyErrorRecordedInDetail(t *testing.T) {
	fake := &countInjector{applyErr: fmt.Errorf("boom")}
	si := scheduledInjector{
		inj:        fake,
		atOffset:   20 * time.Millisecond,
		hasRecover: false,
		event:      &injectorEvent{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	si.run(ctx, nullLogger())
	_, _, detail := si.event.snapshot()
	if !strings.Contains(detail, "boom") {
		t.Errorf("detail = %q, want to mention the Apply error", detail)
	}
	if !strings.Contains(detail, "Apply") {
		t.Errorf("detail = %q, want 'Apply:' prefix", detail)
	}
}

// countInjector implements injectors.Injector for tests : bumps
// counters so we can assert order without touching a cluster.
// applyErr / revertErr let a test simulate a failing call.
type countInjector struct {
	applied, reverted int
	applyErr          error
	revertErr         error
}

func (c *countInjector) Name() string { return "count" }
func (c *countInjector) Apply(_ context.Context) error {
	c.applied++
	return c.applyErr
}
func (c *countInjector) Revert(_ context.Context) error {
	c.reverted++
	return c.revertErr
}
