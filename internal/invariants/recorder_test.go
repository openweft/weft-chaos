// recorder_test.go — pin Recorder behaviour : Record is concurrent-
// safe, Snapshot returns an isolated copy, and the optional counter
// bumps exactly once per Record (with the right labels).

package invariants

import (
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestRecorder_RecordAndSnapshot(t *testing.T) {
	r := NewRecorder()
	r.Record(Breach{Invariant: "a", Kind: "healthy_endpoint", At: time.Now()})
	r.Record(Breach{Invariant: "b", Kind: "zombies_zero", At: time.Now()})
	snap := r.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(snap))
	}
	if snap[0].Invariant != "a" || snap[1].Invariant != "b" {
		t.Errorf("snapshot order broken : %+v", snap)
	}
	// Mutating the snapshot must not affect the recorder.
	snap[0].Invariant = "mutated"
	if r.Snapshot()[0].Invariant == "mutated" {
		t.Error("Snapshot returned the live slice, not a copy")
	}
}

func TestRecorder_CounterBumpsOncePerRecord(t *testing.T) {
	vec := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "test_breach_total",
		Help: "test",
	}, []string{"invariant", "kind"})
	r := NewRecorderWithCounter(vec)
	r.Record(Breach{Invariant: "alpha", Kind: "healthy_endpoint"})
	r.Record(Breach{Invariant: "alpha", Kind: "healthy_endpoint"})
	r.Record(Breach{Invariant: "beta", Kind: "zombies_zero"})

	if got := readVec(t, vec, "alpha", "healthy_endpoint"); got != 2 {
		t.Errorf("alpha breaches = %v, want 2", got)
	}
	if got := readVec(t, vec, "beta", "zombies_zero"); got != 1 {
		t.Errorf("beta breaches = %v, want 1", got)
	}
}

func TestRecorder_NilCounterIsBenign(t *testing.T) {
	// Recorder built without a counter must still record breaches
	// + never panic when Record is called.
	r := NewRecorderWithCounter(nil)
	r.Record(Breach{Invariant: "x", Kind: "k"})
	if got := len(r.Snapshot()); got != 1 {
		t.Errorf("snapshot = %d, want 1", got)
	}
}

func TestRecorder_ConcurrentRecordSafe(t *testing.T) {
	vec := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "test_breach_concurrent_total",
		Help: "test",
	}, []string{"invariant", "kind"})
	r := NewRecorderWithCounter(vec)
	const goroutines = 8
	const each = 50
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				r.Record(Breach{Invariant: "race", Kind: "healthy_endpoint"})
			}
		}()
	}
	wg.Wait()
	if got := len(r.Snapshot()); got != goroutines*each {
		t.Errorf("snapshot len under race = %d, want %d", got, goroutines*each)
	}
	if got := readVec(t, vec, "race", "healthy_endpoint"); got != float64(goroutines*each) {
		t.Errorf("counter under race = %v, want %d", got, goroutines*each)
	}
}

func readVec(t *testing.T, vec *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	c, err := vec.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues: %v", err)
	}
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("counter.Write: %v", err)
	}
	return m.GetCounter().GetValue()
}
