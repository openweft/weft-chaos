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

# WireGuard mesh drop : every packet from dc2 to dc1+dc3 is silently
# dropped between t=20m and t=22m. Probes the federation peer state
# machine + the etcd quorum behaviour during a real partition.
injector "dc2-partition" {
  kind       = "network_partition"
  selector   = "az=dc2"
  at_offset  = "20m"
  recover_at = "22m"
  params = {
    mode = "drop"
  }
}

# ---- Invariants : continuous correctness checks ----------------

invariant "no_cross_tenant_audit_leak" {
  kind   = "audit_tenant_isolation"
  window = "30s"
}

invariant "endpoints_alive" {
  kind   = "healthy_endpoint"
  window = "10s"
  params = {
    urls = "https://weft.example.com/api/healthz,https://infra.weft.example.com/api/healthz"
  }
}

# Zombie VMs : weft v0.4.12+ publishes `weft_vm_zombies` from the
# ZombieGC reconciler. Any non-zero reading during a chaos run is
# already informative ; >0 between rounds is an unambiguous bug.
invariant "no_zombie_vms" {
  kind   = "zombies_zero"
  window = "30s"
  params = {
    url       = "https://weft.example.com/metrics"
    threshold = "0"
    metric    = "weft_vm_zombies"
  }
}

# Event-bus drops : weft v0.1.7+ publishes `weft_bus_dropped_total`
# per subscriber. Any growth between rounds means a watcher fell
# behind ; reconcile loops + registry consumers are at risk.
invariant "no_bus_drops" {
  kind   = "bus_drops_zero"
  window = "30s"
  params = {
    url       = "https://weft.example.com/metrics"
    threshold = "0"
    metric    = "weft_bus_dropped_total"
  }
}
