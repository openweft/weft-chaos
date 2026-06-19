#!/usr/bin/env bash
# chaos-host-lifecycle.sh — chaos test : add a transient host to a
# running cluster, then cordon + remove ; verify the inventory + the
# scheduler react cleanly.
#
# Targets the live 3-DC cluster (dc1-r1-h1 / dc2-r1-h1 / dc3-r1-h1).
# The "new" host is synthetic — we register a fake UUID + hostname +
# AZ via the explicit `weft host register` path (no SSH push to a
# real new machine).
#
# Lifecycle :
#   1. Pre-clean any leftover fake host
#   2. Baseline list-count
#   3. Register → count grows
#   4. show → fields match
#   5. Idempotent re-register → count unchanged
#   6. Cordon (drain) — `host rm` refuses "active" hosts
#   7. Remove → count returns to baseline

set -uo pipefail

DC=dc1-r1-h1
SOCK=/home/admin/.weft/weft.sock
WEFT="weft --socket=$SOCK"
LOGDIR=/tmp/chaos-host-lifecycle-$(date +%s)
mkdir -p "$LOGDIR"
echo "log dir : $LOGDIR"

FAKE_UUID=00000000-aaaa-bbbb-cccc-444444444444
FAKE_HOST=chaos-h4
FAKE_AZ=dc4
FAKE_HYP=qemu-kvm
FAKE_ARCH=arm64

probe() {
  ssh -o ConnectTimeout=3 admin@$DC "$WEFT $1" 2>&1
}

count_hosts() {
  probe "host ls" | wc -l | tr -d ' '
}

assert() {
  if ! eval "$2"; then
    echo "  ✗ $1"
    exit 1
  fi
  echo "  ✓ $1"
}

echo ""
echo "=== Phase 0 : pre-clean any leftover fake host ==="
# set-state down → rm. Errors here are non-fatal (host may not exist).
probe "host set-state $FAKE_UUID down" 2>&1 >/dev/null || true
probe "host rm $FAKE_UUID" 2>&1 >/dev/null || true
sleep 1

echo ""
echo "=== Phase 1 : baseline ==="
BASELINE=$(count_hosts)
echo "  host count (with header) : $BASELINE"

echo ""
echo "=== Phase 2 : register fake host ==="
probe "host register --uuid=$FAKE_UUID --hostname=$FAKE_HOST --az=$FAKE_AZ --hypervisor=$FAKE_HYP --architecture=$FAKE_ARCH" | tee "$LOGDIR/register.txt"

echo ""
echo "=== Phase 3 : verify post-register ==="
AFTER_REG=$(count_hosts)
echo "  host count : $AFTER_REG"
assert "list grew by 1 after register" "[ \"$AFTER_REG\" = \"$((BASELINE+1))\" ]"
SHOW=$(probe "host show $FAKE_UUID")
echo "$SHOW" > "$LOGDIR/show.txt"
assert "show by uuid succeeds" "echo \"$SHOW\" | grep -q $FAKE_HOST"
assert "AZ field matches" "echo \"$SHOW\" | grep -q $FAKE_AZ"

echo ""
echo "=== Phase 4 : idempotent re-register ==="
probe "host register --uuid=$FAKE_UUID --hostname=$FAKE_HOST --az=$FAKE_AZ --hypervisor=$FAKE_HYP --architecture=$FAKE_ARCH" | tee "$LOGDIR/re-register.txt"
AFTER_REREG=$(count_hosts)
assert "re-register is idempotent (count unchanged)" "[ \"$AFTER_REREG\" = \"$AFTER_REG\" ]"

echo ""
echo "=== Phase 5 : cordon (block new schedules) ==="
probe "host cordon $FAKE_UUID" | tee "$LOGDIR/cordon.txt"
sleep 1
SHOW_CORDONED=$(probe "host show $FAKE_UUID")
echo "$SHOW_CORDONED" > "$LOGDIR/show-cordoned.txt"

echo ""
echo "=== Phase 6 : set-state down (required before rm — host rm refuses 'active' hosts) ==="
probe "host set-state $FAKE_UUID down" | tee "$LOGDIR/set-state-down.txt"
sleep 1

echo ""
echo "=== Phase 7 : remove ==="
RM_OUT=$(probe "host rm $FAKE_UUID")
echo "$RM_OUT" | tee "$LOGDIR/rm.txt"
sleep 1
AFTER_RM=$(count_hosts)
echo "  host count after rm : $AFTER_RM"
assert "list shrank to baseline after rm" "[ \"$AFTER_RM\" = \"$BASELINE\" ]"

echo ""
echo "=== All phases passed ✓ ==="
echo "log dir : $LOGDIR"
