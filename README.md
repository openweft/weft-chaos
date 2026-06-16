# weft-chaos

Stress + chaos harness for openweft clusters. Drives one or more
tenants with declared workloads, injects controlled failures (DC
cordon, network partition, disk pressure, etcd evict, kill-pid),
and continuously checks invariants (audit isolation, scheduling
compliance, zombie count, event-bus drops). Emits a timeline JSON
report flagging every breach.

**Status : functional.** The CLI surface, repo layout, agent
drivers for 10 resource kinds, 3 injectors and 5 invariants are
all wired. `wclient` is a stub-with-mock-seam ; the live-cluster
gRPC/REST plumbing lands in the next batch.

## What it does (eventually)

Reads a `scenario.hcl` :

```hcl
workload "vm-churn" {
  tenant       = "acme"
  steady_rps   = 5
  burst_rps    = 50
  burst_every  = "5m"
  burst_for    = "30s"
  resources    = ["microvm", "volume", "network"]
}

injector "dc2-cordon" {
  kind       = "host_cordon"
  selector   = "az=dc2"
  at_offset  = "10m"
  recover_at = "15m"
}

invariant "no_cross_tenant_audit_leak" {
  kind   = "audit_tenant_isolation"
  window = "30s"
}
```

…and drives that against a live (sandbox) cluster :

```sh
weft-chaos --cluster ./cluster.hcl \
           --scenario scenarios/example.hcl \
           --duration 30m \
           --report run-2026-06-15.json
```

## Safety

`weft-chaos` REFUSES to touch a cluster whose `cluster.hcl`
carries `production = true` unless `--i-know-what-im-doing` is
also set. Even then it logs an audit event into the cluster's own
`/api/audit-log` for every destructive injector so the operator
trail is honest.

## Layout

| path                            | purpose                                          |
| ------------------------------- | ------------------------------------------------ |
| `cmd/weft-chaos/`               | CLI entry point — flags, signal handling, orchestrator |
| `internal/scenario/`            | HCL parse → typed Scenario / Workload / Injector / Invariant |
| `internal/agents/`              | per-workload goroutines (CRUD drivers per tenant) |
| `internal/injectors/`           | failure-injection actions (host_cordon, …)        |
| `internal/invariants/`          | continuously-checked rules                       |
| `internal/wclient/`             | cluster-touching seam (gRPC + REST), mockable    |
| `internal/report/`              | timeline JSON writer                             |
| `scenarios/`                    | shipped example scenarios                        |

## Deploy

Same shape as every other openweft binary : 4-arch OCI image
published by tag-gated CI to
`ghcr.io/openweft/weft-chaos:vX.Y.Z`, consumed via
`weft microvm pull` and run as a one-shot microVM beside the
target cluster's control plane. Operators don't run it on the
host — that's an anti-pattern for the stack.

## Status — what landed

- **Injectors (3)** : `host_cordon`, `network_partition`,
  `process_kill`. `disk_pressure` + `etcd_evict` remain to be
  added (both need ssh-into-host primitives that aren't in
  `wclient` yet).
- **Invariants (5)** : `audit_tenant_isolation`, `bus_drops_zero`,
  `healthy_endpoint`, `respawn_within_sla` (histogram-based
  recovery-time SLA), `zombies_zero`. The
  `scheduling_compliant_within` invariant is captured by the
  `respawn_within_sla` histogram + the cluster's own
  `scheduling-rule.compliant` stream, so it's not a separate
  invariant.
- **Agent drivers (10 resource kinds)** : `microvm`, `volume`,
  `network`, `security-group`, `dns-zone`, `dns-record`,
  `loadbalancer`, `bucket`, `share`, `sshkey`. Each does
  create/delete via the `wclient` seam ; unsupported resources
  declared in a scenario log a startup warning rather than
  silently dropping.
- **Scenario validation** : `Scenario.Validate()` refuses
  scenarios with zero invariants at run time so a chaos pass
  always has at least one breach detector.

## Status — what's NOT done yet

- `wclient` is a stub with a mockable seam ; the live-cluster
  gRPC + REST plumbing lands in the next batch (paired with the
  4-arch OCI release).
- `disk_pressure` + `etcd_evict` injectors need ssh-into-host
  primitives.
- `.github/workflows/ci.yml` + `release.yml` are drafted under
  `~/.weft-loom/build/weft-chaos-workflows-pending/` but the
  current GitHub PAT lacks `workflow` scope ; merge them once
  the token is refreshed.
