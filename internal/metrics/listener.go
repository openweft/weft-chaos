// listener.go — minimal HTTP server that serves /metrics on the
// chaos harness's own registry. Bound to a separate listener so a
// nosy operator (or a Grafana scrape job) can watch
// weft_chaos_dispatch_total + weft_chaos_breach_total live without
// waiting for report.json to land at run-exit.
//
// Shape : promhttp.HandlerFor(s.Registry), wrapped in a single
// http.Server. ServeBackground spins the listener in a goroutine
// + returns a Stop function the orchestrator calls at exit. A
// closed listener flushes in-flight requests before returning.

package metrics

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ServeBackground binds to `addr` (e.g. ":7771"), spawns the
// listener goroutine, + returns a Stop function that gracefully
// shuts the server down with a 2 s drain. Returns the resolved
// Addr (handy when addr was ":0" in tests).
func (s *Set) ServeBackground(addr string) (resolvedAddr string, stop func(context.Context) error, err error) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(s.Registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", nil, fmt.Errorf("metrics listen %s: %w", addr, err)
	}
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		// Serve returns http.ErrServerClosed on graceful Shutdown ;
		// any other error is fatal for this listener. We swallow
		// here ; the caller's Stop captures shutdown errors.
		_ = srv.Serve(ln)
	}()
	stop = func(ctx context.Context) error {
		shutdownCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
	return ln.Addr().String(), stop, nil
}
