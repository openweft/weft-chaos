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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
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
	// PortalURL is the base URL agents POST to (e.g.
	// "https://weft.example.com"). Empty = the resource methods
	// are no-ops, which lets unit tests + dry-run smokes exercise
	// the dispatch loop without a live server.
	PortalURL string
	// Token is the bearer credential sent on resource calls. Empty
	// = no Authorization header. Set via env or --token at the CLI.
	Token string
	// TODO(weft-chaos) : OIDC token source, gRPC conns to weft-
	// agent + weft-network, AsTenant fork that swaps the bearer.
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

// ScrapeMetric pulls a Prometheus text-format /metrics page from
// the given URL + returns the value of the named metric. Used by
// invariants that watch gauges or counters from weft / weft-webui
// (zombies_zero, bus_drops_zero, auth_throttle_locked, …).
//
// Bare-bones parser : matches the metric name as a line prefix,
// strips optional `{labels}`, returns the first matching sample's
// value. Doesn't aggregate across labels — most weft gauges are
// label-free or the caller passes the metric+labels (e.g.
// `weft_webui_auth_throttle_locked`) as one identifier.
//
// Returns ErrMetricNotFound when the metric isn't in the page —
// distinguishable from a network error so a scraper can tell
// "endpoint reachable but metric absent" from "endpoint down".
func (c *Client) ScrapeMetric(ctx context.Context, url, metric string) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("new request: %w", err)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("scrape %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("scrape %s: status %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("scrape %s: read: %w", url, err)
	}
	return parseMetric(body, metric)
}

// ErrMetricNotFound is returned by ScrapeMetric when the page
// parsed cleanly but doesn't contain the named metric. Lets the
// caller distinguish absence (which may be normal) from a
// connection error (which is always alarming).
var ErrMetricNotFound = fmt.Errorf("metric not found in /metrics page")

// parseMetric walks the Prometheus text-format body line by line
// looking for one whose first whitespace-delimited token is the
// metric name (with optional {labels} suffix) and returns its
// value. Sums across all matching label-permutations so a
// label-instrumented counter like `events_total{action=…}` works
// without the caller spelling each combination.
func parseMetric(body []byte, metric string) (float64, error) {
	var total float64
	var found bool
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		// Match either `metric value` or `metric{labels} value`.
		if !strings.HasPrefix(line, metric) {
			continue
		}
		rest := line[len(metric):]
		// Reject `metric_x` when we wanted `metric` — the next char
		// must be space, brace, or end-of-line.
		if rest != "" && rest[0] != ' ' && rest[0] != '{' && rest[0] != '\t' {
			continue
		}
		// Strip the optional {label,…} block.
		if strings.HasPrefix(rest, "{") {
			end := strings.IndexByte(rest, '}')
			if end < 0 {
				continue
			}
			rest = rest[end+1:]
		}
		// rest now starts with whitespace + the value + optionally
		// a timestamp. ParseFloat tolerates the trailing junk
		// when we hand it just the value field.
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			continue
		}
		total += v
		found = true
	}
	if !found {
		return 0, ErrMetricNotFound
	}
	return total, nil
}

// CreateMicroVM posts a minimal microVM spec to the cluster's
// /api/v1/microvms endpoint. The tenant header gates which project
// the new microVM lands under. With an empty PortalURL the call
// short-circuits to nil — unit tests + dry-run smokes use this to
// exercise the dispatch loop without needing a server.
//
// Body shape : {"name": "<name>", "tenant": "<tenant>"}. The real
// weft-webui POST surface accepts a much richer block (image, kernel,
// memory_mb, disk_gb…) ; chaos sends only the identity fields + lets
// the cluster's defaults resolve the rest, so the harness stays
// resilient to schema churn upstream.
func (c *Client) CreateMicroVM(ctx context.Context, tenant, name string) error {
	if c.PortalURL == "" {
		return nil
	}
	body, _ := json.Marshal(map[string]string{"name": name, "tenant": tenant})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.PortalURL+"/api/v1/microvms", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create microvm: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("create microvm %s/%s: %w", tenant, name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("create microvm %s/%s: status %d", tenant, name, resp.StatusCode)
	}
	return nil
}

// DeleteMicroVM symmetrically removes a microVM by tenant+name.
// Empty PortalURL = no-op, same rationale as CreateMicroVM.
func (c *Client) DeleteMicroVM(ctx context.Context, tenant, name string) error {
	if c.PortalURL == "" {
		return nil
	}
	url := fmt.Sprintf("%s/api/v1/microvms/%s/%s", c.PortalURL, tenant, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("delete microvm: new request: %w", err)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete microvm %s/%s: %w", tenant, name, err)
	}
	defer resp.Body.Close()
	// 404 is fine — chaos may try to delete the same name twice ;
	// idempotence is the kindness the operator deserves.
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("delete microvm %s/%s: status %d", tenant, name, resp.StatusCode)
	}
	return nil
}

// CreateVolume posts a minimal volume spec to the cluster's
// /api/v1/volumes endpoint. Same shape contract as CreateMicroVM :
// {"name", "tenant"}, server fills defaults (size_gb, backend, …).
// Empty PortalURL = no-op.
func (c *Client) CreateVolume(ctx context.Context, tenant, name string) error {
	if c.PortalURL == "" {
		return nil
	}
	body, _ := json.Marshal(map[string]string{"name": name, "tenant": tenant})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.PortalURL+"/api/v1/volumes", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create volume: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("create volume %s/%s: %w", tenant, name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("create volume %s/%s: status %d", tenant, name, resp.StatusCode)
	}
	return nil
}

// DeleteVolume symmetrically removes a volume. 404 is idempotent.
func (c *Client) DeleteVolume(ctx context.Context, tenant, name string) error {
	if c.PortalURL == "" {
		return nil
	}
	url := fmt.Sprintf("%s/api/v1/volumes/%s/%s", c.PortalURL, tenant, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("delete volume: new request: %w", err)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete volume %s/%s: %w", tenant, name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("delete volume %s/%s: status %d", tenant, name, resp.StatusCode)
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
