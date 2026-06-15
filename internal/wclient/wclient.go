// Package wclient is the cluster-touching seam : the chaos harness
// + the injectors + the invariants all go through this package to
// talk to weft-agent + weft-network + the weft-webui infra portal.
//
// Why a separate package : centralising the calls makes mocking
// trivial in unit tests, and chaos-internal retry / backoff /
// rate-limit policy lives in one place instead of leaking into
// every agent.
//
// Scaffold-only today : the constructor + the resource-CRUD
// interface land here, the per-resource implementations land in
// follow-up commits along with the matching agent / injector code.

package wclient

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Client is the chaos-side handle to a live cluster. Holds the
// auth material + the per-portal sockets (user / tenant / infra)
// so an agent can pick the right surface.
type Client struct {
	Logger *slog.Logger
	// HTTPClient is overridable from tests ; defaults to a
	// 5 s-timeout http.Client at New() time so a hung endpoint
	// doesn't stall an invariant's polling loop.
	HTTPClient *http.Client
	// TODO(weft-chaos) : OIDC token source, gRPC conns to weft-
	// agent + weft-network, REST client to /api/audit-log with the
	// session header set, AsTenant fork that swaps the bearer.
}

// New returns a not-yet-connected client. Call Dial(ctx) before
// any operation. Logger is mandatory ; pass slog.NewTextHandler-
// wrapped Logger or a discard logger for tests.
func New(logger *slog.Logger) *Client {
	return &Client{
		Logger:     logger,
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
	}
}

// Dial is the connect-time barrier — runs cheap reachability
// checks against the cluster + caches what worked. The chaos
// harness aborts early on Dial err so a misconfigured run doesn't
// false-positive every invariant by "everything looks broken".
//
// Today : no-op (the per-portal endpoints land with the OIDC
// follow-up). The signature is in place so callers route through
// it ; lighting up the gRPC + bearer-token wiring is a follow-up
// commit.
func (c *Client) Dial(ctx context.Context) error {
	_ = ctx
	return nil
}

// Healthz GETs the given URL + reports whether the status is 200.
// Used by the healthy_endpoint invariant + by the harness's own
// pre-flight check. Wraps every HTTP error into a wrappable Go
// error so callers can log + classify uniformly.
//
// Honours the caller's context so a slow endpoint can be aborted
// when the invariant's polling round expires.
func (c *Client) Healthz(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("healthz %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz %s: status %d", url, resp.StatusCode)
	}
	return nil
}

// AsTenant returns a sub-client bound to one tenant's portal — the
// way the invariants test isolation. The returned client only ever
// hits the tenant portal even if the caller has cluster-admin
// rights, so an isolation breach reports the property an end
// tenant would actually experience.
func (c *Client) AsTenant(name string) *Client {
	// TODO(weft-chaos) : clone + swap the portal endpoint to the
	// tenant URL, swap the OIDC token for the tenant's SA token.
	_ = name
	return c
}
