// listener_test.go — pin ServeBackground : the /metrics endpoint
// returns Prometheus text format including the chaos counters,
// /healthz returns 200, and Stop cleanly closes the listener.

package metrics

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestServeBackground_MetricsAndHealthz(t *testing.T) {
	s := New()
	// Touch each counter so it shows up in the output ;
	// prometheus elides cells with value 0 from the text exposition.
	s.Dispatch.WithLabelValues("microvm", "create", "ok").Inc()
	s.Breach.WithLabelValues("alpha", "healthy_endpoint").Inc()

	addr, stop, err := s.ServeBackground("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stop(context.Background())
	})

	// /metrics
	resp, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/metrics status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"weft_chaos_dispatch_total", "weft_chaos_breach_total"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("/metrics missing %q ; got %q", want, string(body))
		}
	}

	// /healthz
	hz, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer hz.Body.Close()
	if hz.StatusCode != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200", hz.StatusCode)
	}
}

func TestServeBackground_StopShutsDownCleanly(t *testing.T) {
	s := New()
	addr, stop, err := s.ServeBackground("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := stop(context.Background()); err != nil {
		t.Fatalf("Stop = %v, want nil", err)
	}
	// Post-stop, the port should refuse new connections.
	client := &http.Client{Timeout: 200 * time.Millisecond}
	if _, err := client.Get("http://" + addr + "/metrics"); err == nil {
		t.Error("Get after Stop succeeded, want connection refused")
	}
}

func TestServeBackground_BadAddrErrs(t *testing.T) {
	s := New()
	// "localhost:99999" is out of range ; net.Listen fails fast.
	_, _, err := s.ServeBackground("127.0.0.1:99999")
	if err == nil {
		t.Fatal("ServeBackground on bogus port = nil, want err")
	}
}
