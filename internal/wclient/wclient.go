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
	"log/slog"
)

// Client is the chaos-side handle to a live cluster. Holds the
// auth material + the per-portal sockets (user / tenant / infra)
// so an agent can pick the right surface.
type Client struct {
	Logger *slog.Logger
	// TODO(weft-chaos) : OIDC token source, gRPC conns to weft-
	// agent + weft-network, REST client to /api/audit-log for the
	// invariants.
}

// New returns a not-yet-connected client. Call Dial(ctx) before
// any operation.
func New(logger *slog.Logger) *Client {
	return &Client{Logger: logger}
}

// Dial opens the underlying gRPC + HTTP connections. Returns the
// first error ; partial connect (some sockets up, some down) is
// surfaced via the wclient's Status method (TBD).
func (c *Client) Dial(ctx context.Context) error {
	// TODO(weft-chaos) : load OIDC token (env or service-account),
	// dial weft-agent's gRPC socket, ping /api/healthz on the
	// webui infra portal, log connection state.
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
	return c
}
