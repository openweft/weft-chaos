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
	"math"
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

// CreateMicroVM posts a minimal microVM spec to /api/v1/microvms.
// Body : {"name", "tenant"} ; server defaults resolve the rest
// (image, kernel, memory_mb, disk_gb…) so chaos doesn't have to
// track upstream schema churn. Empty PortalURL = no-op.
func (c *Client) CreateMicroVM(ctx context.Context, tenant, name string) error {
	return c.createResource(ctx, "microvms", tenant, name)
}

// DeleteMicroVM removes a microVM by tenant+name. 404 is idempotent
// so chaos may issue duplicate deletes harmlessly.
func (c *Client) DeleteMicroVM(ctx context.Context, tenant, name string) error {
	return c.deleteResource(ctx, "microvms", tenant, name)
}

// CreateVolume posts a minimal volume spec to /api/v1/volumes.
func (c *Client) CreateVolume(ctx context.Context, tenant, name string) error {
	return c.createResource(ctx, "volumes", tenant, name)
}

// DeleteVolume removes a volume.
func (c *Client) DeleteVolume(ctx context.Context, tenant, name string) error {
	return c.deleteResource(ctx, "volumes", tenant, name)
}

// CreateNetwork posts a minimal subnet spec to /api/v1/networks.
func (c *Client) CreateNetwork(ctx context.Context, tenant, name string) error {
	return c.createResource(ctx, "networks", tenant, name)
}

// DeleteNetwork removes a subnet.
func (c *Client) DeleteNetwork(ctx context.Context, tenant, name string) error {
	return c.deleteResource(ctx, "networks", tenant, name)
}

// CreateSecurityGroup posts a minimal SG spec to /api/v1/security-groups.
func (c *Client) CreateSecurityGroup(ctx context.Context, tenant, name string) error {
	return c.createResource(ctx, "security-groups", tenant, name)
}

// DeleteSecurityGroup removes a security group.
func (c *Client) DeleteSecurityGroup(ctx context.Context, tenant, name string) error {
	return c.deleteResource(ctx, "security-groups", tenant, name)
}

// CreateDNSZone posts a minimal DNS-zone spec to /api/v1/dns-zones.
func (c *Client) CreateDNSZone(ctx context.Context, tenant, name string) error {
	return c.createResource(ctx, "dns-zones", tenant, name)
}

// DeleteDNSZone removes a DNS zone.
func (c *Client) DeleteDNSZone(ctx context.Context, tenant, name string) error {
	return c.deleteResource(ctx, "dns-zones", tenant, name)
}

// CreateDNSRecord posts a DNS record spec to /api/v1/dns-records.
func (c *Client) CreateDNSRecord(ctx context.Context, tenant, name string) error {
	return c.createResource(ctx, "dns-records", tenant, name)
}

// DeleteDNSRecord removes a DNS record.
func (c *Client) DeleteDNSRecord(ctx context.Context, tenant, name string) error {
	return c.deleteResource(ctx, "dns-records", tenant, name)
}

// CreateLoadBalancer posts an L7 load-balancer spec to
// /api/v1/loadbalancers. Caddy-backed in openweft per the router/LB
// Go-native memory.
func (c *Client) CreateLoadBalancer(ctx context.Context, tenant, name string) error {
	return c.createResource(ctx, "loadbalancers", tenant, name)
}

// DeleteLoadBalancer removes a load balancer.
func (c *Client) DeleteLoadBalancer(ctx context.Context, tenant, name string) error {
	return c.deleteResource(ctx, "loadbalancers", tenant, name)
}

// CreateBucket posts an S3-compatible bucket spec to /api/v1/buckets.
// Backed by versitygw or CubeFS objectnode in openweft (no MinIO
// per the no-minio policy).
func (c *Client) CreateBucket(ctx context.Context, tenant, name string) error {
	return c.createResource(ctx, "buckets", tenant, name)
}

// DeleteBucket removes an S3 bucket.
func (c *Client) DeleteBucket(ctx context.Context, tenant, name string) error {
	return c.deleteResource(ctx, "buckets", tenant, name)
}

// CreateShare posts a POSIX/CIFS share spec to /api/v1/shares.
// CubeFS-backed in openweft.
func (c *Client) CreateShare(ctx context.Context, tenant, name string) error {
	return c.createResource(ctx, "shares", tenant, name)
}

// DeleteShare removes a share.
func (c *Client) DeleteShare(ctx context.Context, tenant, name string) error {
	return c.deleteResource(ctx, "shares", tenant, name)
}

// CreateSSHKey posts a public-key entry to the catalogue /api/v1/sshkeys.
// Tenant-scoped : the SSH key is bound to one project + injectable
// into VM cloud-init via the sshkeys field of a workload spec.
func (c *Client) CreateSSHKey(ctx context.Context, tenant, name string) error {
	return c.createResource(ctx, "sshkeys", tenant, name)
}

// DeleteSSHKey removes an SSH key from the catalogue.
func (c *Client) DeleteSSHKey(ctx context.Context, tenant, name string) error {
	return c.deleteResource(ctx, "sshkeys", tenant, name)
}

// createResource is the shared POST path : marshals the identity
// JSON, attaches auth, returns wrapped errors. `plural` is the
// URL segment ("microvms", "volumes", "networks", …) ; the call
// sites pass the canonical pluralisation.
func (c *Client) createResource(ctx context.Context, plural, tenant, name string) error {
	if c.PortalURL == "" {
		return nil
	}
	body, _ := json.Marshal(map[string]string{"name": name, "tenant": tenant})
	url := fmt.Sprintf("%s/api/v1/%s", c.PortalURL, plural)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create %s: new request: %w", plural, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("create %s %s/%s: %w", plural, tenant, name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("create %s %s/%s: status %d", plural, tenant, name, resp.StatusCode)
	}
	return nil
}

// deleteResource is the shared DELETE path. 404 is treated as
// success (idempotence — chaos may double-delete).
func (c *Client) deleteResource(ctx context.Context, plural, tenant, name string) error {
	if c.PortalURL == "" {
		return nil
	}
	url := fmt.Sprintf("%s/api/v1/%s/%s/%s", c.PortalURL, plural, tenant, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("delete %s: new request: %w", plural, err)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete %s %s/%s: %w", plural, tenant, name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("delete %s %s/%s: status %d", plural, tenant, name, resp.StatusCode)
	}
	return nil
}

// FetchJSON GETs the given URL + returns the raw body bytes. Used
// by invariants that need to walk a JSON response shape (e.g.
// audit_tenant_isolation parsing /api/audit-log). Honours the
// Authorization header when c.Token is set.
//
// Returns the raw body so the caller owns the unmarshal target —
// chaos doesn't want to pin its understanding of every API shape
// in wclient ; each invariant decodes what it needs.
func (c *Client) FetchJSON(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("fetch %s: status %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// Histogram is the minimal Prometheus histogram readout the
// chaos invariants need : bucket bound → cumulative count,
// plus the total sample count. Sum is intentionally NOT exposed
// — invariants checking quantile ratios don't need it, and
// adding fields invites callers to compute means that are
// statistically meaningless without weights.
type Histogram struct {
	Buckets map[float64]float64 // upper-bound (le) → cumulative count
	Count   float64             // total samples ( = bucket{le=+Inf} )
}

// ScrapeHistogram fetches a Prometheus /metrics page + extracts
// one histogram by name. Returns ErrMetricNotFound when the page
// has no `<metric>_count` line — distinguishable from a network
// error so the caller can tell "endpoint reachable but no samples
// yet" from "endpoint unreachable".
//
// Bucket lines look like : `<metric>_bucket{le="5"} 12`. Labels
// other than le are tolerated but ignored — chaos doesn't slice
// histograms by label today.
func (c *Client) ScrapeHistogram(ctx context.Context, url, metric string) (Histogram, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Histogram{}, fmt.Errorf("new request: %w", err)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return Histogram{}, fmt.Errorf("scrape %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Histogram{}, fmt.Errorf("scrape %s: status %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Histogram{}, fmt.Errorf("scrape %s: read: %w", url, err)
	}
	return parseHistogram(body, metric)
}

// parseHistogram walks the text-format body for the three pieces
// that define a histogram : every <metric>_bucket{le="X"} line +
// the <metric>_count line. Order in the page doesn't matter ; the
// parser is line-oriented.
func parseHistogram(body []byte, metric string) (Histogram, error) {
	h := Histogram{Buckets: map[float64]float64{}}
	bucketPrefix := metric + "_bucket"
	countPrefix := metric + "_count"
	var sawCount bool
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		switch {
		case strings.HasPrefix(line, bucketPrefix):
			// Need to ensure the next char terminates the metric name
			// (avoids matching `<metric>_bucketwise`).
			rest := line[len(bucketPrefix):]
			if rest == "" || (rest[0] != '{' && rest[0] != ' ' && rest[0] != '\t') {
				continue
			}
			le, value, ok := parseBucketLine(rest)
			if !ok {
				continue
			}
			h.Buckets[le] = value
		case strings.HasPrefix(line, countPrefix):
			rest := line[len(countPrefix):]
			if rest == "" || (rest[0] != ' ' && rest[0] != '\t' && rest[0] != '{') {
				continue
			}
			// `_count` may carry labels (rare for histograms but valid).
			if strings.HasPrefix(rest, "{") {
				end := strings.IndexByte(rest, '}')
				if end < 0 {
					continue
				}
				rest = rest[end+1:]
			}
			fields := strings.Fields(rest)
			if len(fields) == 0 {
				continue
			}
			v, err := strconv.ParseFloat(fields[0], 64)
			if err != nil {
				continue
			}
			h.Count = v
			sawCount = true
		}
	}
	if !sawCount {
		return Histogram{}, ErrMetricNotFound
	}
	return h, nil
}

// parseBucketLine extracts le + value from a `_bucket{le="X",...} V`
// line. Returns (le, value, true) on success. `+Inf` is treated as
// math.Inf(1) so callers can sort buckets monotonically.
func parseBucketLine(rest string) (float64, float64, bool) {
	if !strings.HasPrefix(rest, "{") {
		return 0, 0, false
	}
	end := strings.IndexByte(rest, '}')
	if end < 0 {
		return 0, 0, false
	}
	labels := rest[1:end]
	rest = rest[end+1:]
	// Find le="…" anywhere in the labels block.
	le, ok := extractLabel(labels, "le")
	if !ok {
		return 0, 0, false
	}
	var leVal float64
	if le == "+Inf" {
		leVal = math.Inf(1)
	} else {
		v, err := strconv.ParseFloat(le, 64)
		if err != nil {
			return 0, 0, false
		}
		leVal = v
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return 0, 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, 0, false
	}
	return leVal, v, true
}

// extractLabel pulls the value of `key="…"` out of a Prometheus
// labels block. Returns ("", false) when the key is absent or the
// quoting doesn't line up.
func extractLabel(labels, key string) (string, bool) {
	// Look for `key="`.
	needle := key + `="`
	idx := strings.Index(labels, needle)
	if idx < 0 {
		return "", false
	}
	start := idx + len(needle)
	end := strings.IndexByte(labels[start:], '"')
	if end < 0 {
		return "", false
	}
	return labels[start : start+end], true
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
