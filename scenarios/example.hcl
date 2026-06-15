# scenarios/example.hcl — a representative 30-minute chaos run :
# steady-state CRUD across three tenants, a DC2 cordon at the
# 10-minute mark, three watchers that keep the cluster honest the
# whole time.
#
# Run it with :
#   weft-chaos run --cluster ./cluster.hcl --scenario scenarios/example.hcl
#
# The chaos binary refuses to touch a cluster whose cluster.hcl
# carries `production = true` unless --i-know-what-im-doing is
# also set. Keep this scenario pointed at a sandbox cluster.

# ---- Workloads : per-tenant CRUD pressure ----------------------

workload "acme-mix" {
  tenant      = "acme"
  steady_rps  = 5
  burst_rps   = 50
  burst_every = "5m"
  burst_for   = "30s"
  resources   = ["microvm", "volume", "network", "security-group"]
}

workload "globex-mix" {
  tenant      = "globex"
  steady_rps  = 3
  burst_rps   = 25
  burst_every = "7m"
  burst_for   = "20s"
  resources   = ["microvm", "volume", "dns-zone"]
}

workload "initech-readonly" {
  tenant     = "initech"
  steady_rps = 2
  resources  = ["microvm"]
}

# ---- Injectors : controlled failure on a schedule --------------

injector "dc2-cordon" {
  kind       = "host_cordon"
  selector   = "az=dc2"
  at_offset  = "10m"
  recover_at = "15m"
}

# ---- Invariants : continuous correctness checks ----------------

invariant "no_cross_tenant_audit_leak" {
  kind   = "audit_tenant_isolation"
  window = "30s"
}
