# weft-chaos

Stress + chaos harness for openweft clusters. Drives one or more
tenants with declared workloads, injects controlled failures (DC
cordon, network partition, disk pressure, etcd evict, kill-pid),
and continuously checks invariants (audit isolation, scheduling
compliance, zombie count, event-bus drops). Emits a timeline JSON
report flagging every breach.

**Status : scaffold.** The CLI surface + the repo layout + one
sample injector + one sample invariant are in. Per-resource agent
drivers + the remaining injectors / invariants land as follow-up
turns, one per commit, so each gets focused review.

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

## Status — what's NOT in the scaffold

- The `wclient` package is a stub ; no gRPC or REST calls are
  wired yet. The CLI runs to completion + prints a `scaffold-only`
  log line.
- Only `host_cordon` is sketched in `injectors/` ; the other four
  follow-up commits add `network_partition`, `disk_pressure`,
  `kill_pid`, `etcd_evict`.
- Only `audit_tenant_isolation` is sketched in `invariants/` ;
  follow-ups : `vm_count_consistent`, `scheduling_compliant_within`,
  `zombies_zero`, `bus_drops_zero`.
- Per-resource agent drivers (microvm / volume / network / SG /
  DNS / scheduling-rule) are TODO inside `internal/agents/` ; one
  commit per resource so reviewers can sign each off.

The scaffold ratifies the CLI + the repo shape ; lighting it up
against a live cluster is the next batch of work.
