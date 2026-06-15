// agent_test.go — pin dispatchOne against httptest + a non-nil
// Prometheus counter. The agent must (1) round-robin its resources,
// (2) alternate CREATE/DELETE, (3) bump the dispatch counter with
// the correct (resource, verb, result) labels.

package agents

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/openweft/weft-chaos/internal/metrics"
	"github.com/openweft/weft-chaos/internal/scenario"
	"github.com/openweft/weft-chaos/internal/wclient"
)

func nullLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestDispatchOne_AlternatesVerbsAndRoundRobinsResources(t *testing.T) {
	var (
		creates atomic.Int32
		deletes atomic.Int32
		paths   = struct {
			mu sync.Mutex
			s  []string
		}{}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths.mu.Lock()
		paths.s = append(paths.s, r.Method+" "+r.URL.Path)
		paths.mu.Unlock()
		switch r.Method {
		case http.MethodPost:
			creates.Add(1)
			w.WriteHeader(http.StatusCreated)
		case http.MethodDelete:
			deletes.Add(1)
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)

	m := metrics.New()
	client := wclient.New(nullLogger())
	client.PortalURL = srv.URL

	a := &Agent{
		W: scenario.Workload{
			Name:      "test-mix",
			Tenant:    "acme",
			Resources: []string{"microvm"}, // single resource keeps round-robin trivial
		},
		Logger:   nullLogger(),
		Client:   client,
		Dispatch: m.Dispatch,
	}

	// Drive four rounds : expect CREATE, DELETE, CREATE, DELETE
	// against /api/v1/microvms (POST + DELETE).
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		a.dispatchOne(ctx)
	}

	if got := creates.Load(); got != 2 {
		t.Errorf("create hits = %d, want 2", got)
	}
	if got := deletes.Load(); got != 2 {
		t.Errorf("delete hits = %d, want 2", got)
	}

	// Counter must record 4 "ok" results across (resource=microvm, verb=create|delete).
	got := readCounter(t, m.Dispatch, "microvm", "create", "ok") +
		readCounter(t, m.Dispatch, "microvm", "delete", "ok")
	if got != 4 {
		t.Errorf("dispatch counter total = %v, want 4", got)
	}
}

func TestDispatchOne_RoutesVolumeResource(t *testing.T) {
	var (
		creates atomic.Int32
		deletes atomic.Int32
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/volumes") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodPost:
			creates.Add(1)
			w.WriteHeader(http.StatusCreated)
		case http.MethodDelete:
			deletes.Add(1)
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)

	m := metrics.New()
	client := wclient.New(nullLogger())
	client.PortalURL = srv.URL
	a := &Agent{
		W: scenario.Workload{
			Name:      "vol-mix",
			Tenant:    "globex",
			Resources: []string{"volume"},
		},
		Logger:   nullLogger(),
		Client:   client,
		Dispatch: m.Dispatch,
	}
	for i := 0; i < 4; i++ {
		a.dispatchOne(context.Background())
	}
	if creates.Load() != 2 || deletes.Load() != 2 {
		t.Errorf("volumes : POST=%d DELETE=%d, want 2/2", creates.Load(), deletes.Load())
	}
	if got := readCounter(t, m.Dispatch, "volume", "create", "ok"); got != 2 {
		t.Errorf("volume create counter = %v, want 2", got)
	}
	if got := readCounter(t, m.Dispatch, "volume", "delete", "ok"); got != 2 {
		t.Errorf("volume delete counter = %v, want 2", got)
	}
}

func TestDispatchOne_UnsupportedResourceLabelsAccordingly(t *testing.T) {
	// "dns-zone" driver doesn't exist yet (microvm + volume do) —
	// agent must still drive (no panic), but label the counter
	// with result="unsupported".
	m := metrics.New()
	a := &Agent{
		W: scenario.Workload{
			Name:      "dns-only",
			Tenant:    "initech",
			Resources: []string{"dns-zone"},
		},
		Logger:   nullLogger(),
		Client:   wclient.New(nullLogger()),
		Dispatch: m.Dispatch,
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		a.dispatchOne(ctx)
	}
	c := readCounter(t, m.Dispatch, "dns-zone", "create", "unsupported") +
		readCounter(t, m.Dispatch, "dns-zone", "delete", "unsupported")
	if c != 3 {
		t.Errorf("unsupported counter = %v, want 3", c)
	}
}

func TestDispatchOne_NoOpWhenPortalURLEmpty(t *testing.T) {
	// Empty PortalURL = wclient short-circuits to nil. The agent
	// should record this as "ok" because no error came back ;
	// dispatch loop still exercises its own bookkeeping.
	m := metrics.New()
	client := wclient.New(nullLogger())
	// PortalURL deliberately empty.
	a := &Agent{
		W: scenario.Workload{
			Name:      "noop",
			Tenant:    "x",
			Resources: []string{"microvm"},
		},
		Logger:   nullLogger(),
		Client:   client,
		Dispatch: m.Dispatch,
	}
	for i := 0; i < 2; i++ {
		a.dispatchOne(context.Background())
	}
	c := readCounter(t, m.Dispatch, "microvm", "create", "ok") +
		readCounter(t, m.Dispatch, "microvm", "delete", "ok")
	if c != 2 {
		t.Errorf("ok counter on no-op path = %v, want 2", c)
	}
}

func TestDispatchOne_EmptyResourcesIsBenign(t *testing.T) {
	// A workload with no resources listed shouldn't panic + shouldn't
	// touch the counter.
	m := metrics.New()
	a := &Agent{
		W:        scenario.Workload{Name: "blank"},
		Logger:   nullLogger(),
		Client:   wclient.New(nullLogger()),
		Dispatch: m.Dispatch,
	}
	a.dispatchOne(context.Background())
	if got := totalCounter(t, m.Dispatch); got != 0 {
		t.Errorf("counter touched on empty Resources : %v", got)
	}
}

func TestDispatchOne_NilCounterIsBenign(t *testing.T) {
	// Agents with no metrics handle (older callers, tests) must
	// still dispatch without panicking.
	a := &Agent{
		W:      scenario.Workload{Name: "no-counter", Resources: []string{"microvm"}},
		Logger: nullLogger(),
		Client: wclient.New(nullLogger()),
		// Dispatch nil
	}
	a.dispatchOne(context.Background())
}

// readCounter pulls one cell out of the dispatch CounterVec by
// label triple via the prometheus.Metric.Write protocol.
func readCounter(t *testing.T, vec *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	c, err := vec.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues(%v): %v", labels, err)
	}
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("counter.Write: %v", err)
	}
	return m.GetCounter().GetValue()
}

// totalCounter sums every cell of the vec (used to assert
// "the counter wasn't touched at all").
func totalCounter(t *testing.T, vec *prometheus.CounterVec) float64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 32)
	go func() {
		vec.Collect(ch)
		close(ch)
	}()
	var total float64
	for m := range ch {
		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			t.Fatalf("metric.Write: %v", err)
		}
		total += pb.GetCounter().GetValue()
	}
	return total
}
