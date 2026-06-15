// wclient_test.go — pin Healthz against an httptest server. Real
// cluster smoke happens through scenarios ; this file owns the
// unit invariants of the seam itself.

package wclient

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func nullLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHealthz_200IsOk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	c := New(nullLogger())
	if err := c.Healthz(context.Background(), srv.URL+"/api/healthz"); err != nil {
		t.Errorf("Healthz on 200 = %v, want nil", err)
	}
}

func TestHealthz_503IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	c := New(nullLogger())
	err := c.Healthz(context.Background(), srv.URL+"/api/healthz")
	if err == nil {
		t.Fatal("Healthz on 503 = nil, want err")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("err = %v, want it to mention the status code", err)
	}
}

func TestHealthz_UnreachableServerIsError(t *testing.T) {
	c := New(nullLogger())
	// Port 1 is the canonical "nothing listens here" : on macOS
	// + Linux both refuse the connect immediately.
	err := c.Healthz(context.Background(), "http://127.0.0.1:1/api/healthz")
	if err == nil {
		t.Errorf("Healthz to unreachable host = nil, want err")
	}
}

func TestScrapeMetric_ReturnsValue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# HELP weft_vm_zombies number of zombie VMs\n# TYPE weft_vm_zombies gauge\nweft_vm_zombies 3\n"))
	}))
	t.Cleanup(srv.Close)
	c := New(nullLogger())
	v, err := c.ScrapeMetric(context.Background(), srv.URL+"/metrics", "weft_vm_zombies")
	if err != nil {
		t.Fatal(err)
	}
	if v != 3 {
		t.Errorf("ScrapeMetric = %v, want 3", v)
	}
}

func TestScrapeMetric_SumsAcrossLabels(t *testing.T) {
	// A label-instrumented counter is the common shape in
	// weft-webui (audit_events_total{action,result}). The parser
	// sums across all permutations so the caller doesn't have to
	// enumerate them.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`weft_webui_audit_events_total{action="az",result="ok"} 4
weft_webui_audit_events_total{action="auth",result="error"} 2
weft_webui_audit_events_total{action="rack",result="ok"} 1
`))
	}))
	t.Cleanup(srv.Close)
	c := New(nullLogger())
	v, err := c.ScrapeMetric(context.Background(), srv.URL+"/metrics", "weft_webui_audit_events_total")
	if err != nil {
		t.Fatal(err)
	}
	if v != 7 {
		t.Errorf("ScrapeMetric sum = %v, want 7", v)
	}
}

func TestScrapeMetric_MissingMetricErr(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# nothing useful here\nsome_other_metric 0\n"))
	}))
	t.Cleanup(srv.Close)
	c := New(nullLogger())
	_, err := c.ScrapeMetric(context.Background(), srv.URL+"/metrics", "weft_vm_zombies")
	if err == nil {
		t.Fatal("ScrapeMetric on missing metric = nil err, want ErrMetricNotFound")
	}
	if err != ErrMetricNotFound && !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v, want ErrMetricNotFound", err)
	}
}

func TestScrapeMetric_IgnoresPrefixCollision(t *testing.T) {
	// `weft_vm_zombies_total` must NOT be picked up when we ask
	// for `weft_vm_zombies` (different metric, prefix collision).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("weft_vm_zombies_total 99\n"))
	}))
	t.Cleanup(srv.Close)
	c := New(nullLogger())
	_, err := c.ScrapeMetric(context.Background(), srv.URL+"/metrics", "weft_vm_zombies")
	if err != ErrMetricNotFound {
		t.Errorf("got %v, want ErrMetricNotFound on prefix-collision-only", err)
	}
}

func TestCreateMicroVM_PostsExpectedBody(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotAuth   string
		gotBody   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)
	c := New(nullLogger())
	c.PortalURL = srv.URL
	c.Token = "abc123"
	if err := c.CreateMicroVM(context.Background(), "acme", "vm1"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/microvms" {
		t.Errorf("path = %q, want /api/v1/microvms", gotPath)
	}
	if gotAuth != "Bearer abc123" {
		t.Errorf("auth header = %q, want Bearer abc123", gotAuth)
	}
	if !strings.Contains(gotBody, `"name":"vm1"`) || !strings.Contains(gotBody, `"tenant":"acme"`) {
		t.Errorf("body = %q, want both name + tenant", gotBody)
	}
}

func TestCreateMicroVM_EmptyPortalURLNoOps(t *testing.T) {
	c := New(nullLogger())
	// PortalURL deliberately empty.
	if err := c.CreateMicroVM(context.Background(), "acme", "vm1"); err != nil {
		t.Errorf("empty portal CreateMicroVM = %v, want nil", err)
	}
}

func TestCreateMicroVM_Non2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	c := New(nullLogger())
	c.PortalURL = srv.URL
	err := c.CreateMicroVM(context.Background(), "acme", "vm1")
	if err == nil {
		t.Fatal("500 = nil err, want error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %v, want mention of status code", err)
	}
}

func TestDeleteMicroVM_IdempotentOn404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	c := New(nullLogger())
	c.PortalURL = srv.URL
	if err := c.DeleteMicroVM(context.Background(), "acme", "vm1"); err != nil {
		t.Errorf("404 DELETE = %v, want nil (idempotent)", err)
	}
}

func TestDeleteMicroVM_PathIncludesTenantAndName(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	c := New(nullLogger())
	c.PortalURL = srv.URL
	if err := c.DeleteMicroVM(context.Background(), "acme", "vm1"); err != nil {
		t.Fatal(err)
	}
	want := "/api/v1/microvms/acme/vm1"
	if gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
}

func TestHealthz_ContextCancelAborts(t *testing.T) {
	// A server that never responds — Healthz must respect the
	// caller's cancelled context rather than hang on the
	// HTTPClient default timeout.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	c := New(nullLogger())
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already-cancelled context
	err := c.Healthz(ctx, srv.URL+"/api/healthz")
	if err == nil {
		t.Errorf("Healthz with cancelled ctx = nil, want err")
	}
}
