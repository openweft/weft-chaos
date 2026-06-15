// audit_tenant_isolation_test.go — pin the cross-tenant leak
// detector against canned audit-log responses. The chaos run's
// "did the multi-tenant boundary hold" signal flows entirely
// through this invariant ; getting the parsing wrong silently
// hides leaks.

package invariants

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openweft/weft-chaos/internal/scenario"
	"github.com/openweft/weft-chaos/internal/wclient"
)

func TestAuditTenantIsolation_NoBreachWhenAllOwn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always return events whose tenant matches the query.
		tenant := r.URL.Query().Get("tenant")
		fmt.Fprintf(w, `[{"id":"e1","tenant":%q,"action":"vm.create"},{"id":"e2","tenant":%q,"action":"vm.delete"}]`, tenant, tenant)
	}))
	t.Cleanup(srv.Close)

	inv := &AuditTenantIsolation{
		Spec: scenario.Invariant{
			Name:   "audit",
			Kind:   "audit_tenant_isolation",
			Window: "100ms",
			Params: map[string]string{
				"tenants":      "acme,globex",
				"url_template": srv.URL + "/api/v1/audit-log?tenant={tenant}",
			},
		},
		Logger: nullLogger(),
		Client: wclient.New(nullLogger()),
	}
	rec := NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_ = inv.Run(ctx, rec)
	if got := len(rec.Snapshot()); got != 0 {
		t.Errorf("no-leak run breaches = %d, want 0 : %+v", got, rec.Snapshot())
	}
}

func TestAuditTenantIsolation_BreachOnCrossTenantLeak(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenant := r.URL.Query().Get("tenant")
		// Inject ONE cross-tenant event whenever acme queries.
		// globex sees only its own.
		if tenant == "acme" {
			fmt.Fprint(w, `[{"id":"e1","tenant":"acme","action":"vm.create"},{"id":"e2","tenant":"globex","action":"vm.create"}]`)
			return
		}
		fmt.Fprintf(w, `[{"id":"e3","tenant":%q,"action":"vm.create"}]`, tenant)
	}))
	t.Cleanup(srv.Close)

	inv := &AuditTenantIsolation{
		Spec: scenario.Invariant{
			Name:   "audit",
			Kind:   "audit_tenant_isolation",
			Window: "100ms",
			Params: map[string]string{
				"tenants":      "acme,globex",
				"url_template": srv.URL + "/api/v1/audit-log?tenant={tenant}",
			},
		},
		Logger: nullLogger(),
		Client: wclient.New(nullLogger()),
	}
	rec := NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Millisecond)
	defer cancel()
	_ = inv.Run(ctx, rec)
	snap := rec.Snapshot()
	if len(snap) == 0 {
		t.Fatal("cross-tenant leak should produce >=1 breach, got 0")
	}
	// At least one breach must mention the leaking tenant pair.
	found := false
	for _, b := range snap {
		if strings.Contains(b.Detail, `tenant "acme" saw event`) &&
			strings.Contains(b.Detail, `from tenant "globex"`) {
			found = true
			if len(b.TenantsInvolved) != 2 {
				t.Errorf("TenantsInvolved = %v, want both tenants", b.TenantsInvolved)
			}
		}
	}
	if !found {
		t.Errorf("breaches missing the acme/globex leak detail : %+v", snap)
	}
}

func TestAuditTenantIsolation_ScrapeErrorIsBreach(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	inv := &AuditTenantIsolation{
		Spec: scenario.Invariant{
			Name:   "audit",
			Kind:   "audit_tenant_isolation",
			Window: "100ms",
			Params: map[string]string{
				"tenants":      "acme",
				"url_template": srv.URL + "/api/v1/audit-log?tenant={tenant}",
			},
		},
		Logger: nullLogger(),
		Client: wclient.New(nullLogger()),
	}
	rec := NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Millisecond)
	defer cancel()
	_ = inv.Run(ctx, rec)
	if len(rec.Snapshot()) == 0 {
		t.Fatal("503 audit endpoint should breach, got 0")
	}
}

func TestAuditTenantIsolation_MissingTenantsRejected(t *testing.T) {
	inv := &AuditTenantIsolation{
		Spec: scenario.Invariant{
			Name:   "audit",
			Kind:   "audit_tenant_isolation",
			Params: map[string]string{"url_template": "https://x/audit?t={tenant}"},
		},
		Logger: nullLogger(),
		Client: wclient.New(nullLogger()),
	}
	err := inv.Run(context.Background(), NewRecorder())
	if err == nil {
		t.Fatal("Run with empty tenants = nil, want config error")
	}
}

func TestAuditTenantIsolation_MissingURLTemplateRejected(t *testing.T) {
	inv := &AuditTenantIsolation{
		Spec: scenario.Invariant{
			Name:   "audit",
			Kind:   "audit_tenant_isolation",
			Params: map[string]string{"tenants": "acme"},
		},
		Logger: nullLogger(),
		Client: wclient.New(nullLogger()),
	}
	err := inv.Run(context.Background(), NewRecorder())
	if err == nil {
		t.Fatal("Run with empty url_template = nil, want config error")
	}
}

func TestAuditTenantIsolation_URLTemplateMustHavePlaceholder(t *testing.T) {
	inv := &AuditTenantIsolation{
		Spec: scenario.Invariant{
			Name: "audit",
			Kind: "audit_tenant_isolation",
			Params: map[string]string{
				"tenants":      "acme",
				"url_template": "https://x/audit",
			},
		},
		Logger: nullLogger(),
		Client: wclient.New(nullLogger()),
	}
	err := inv.Run(context.Background(), NewRecorder())
	if err == nil {
		t.Fatal("url_template without {tenant} should reject")
	}
}

func TestParseCSV_TrimsAndDropsEmpty(t *testing.T) {
	got := parseCSV(" acme , , globex , initech,")
	want := []string{"acme", "globex", "initech"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%q, want %q", i, got[i], want[i])
		}
	}
}
