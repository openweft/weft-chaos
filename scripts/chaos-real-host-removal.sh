#!/usr/bin/env bash
# chaos-real-host-removal.sh — drain + remove a REAL host from the
# live 3-DC × 2-rack cluster, then re-register it. Validates the
# cordon → set-state down → rm → re-register cycle against an actual
# weft-agent on 192.168.105.0/24.
#
# Target : dc3-r2-h1 (the newest add) — removing it doesn't touch
# anything else since no VMs are placed on r2 yet.

set -uo pipefail

DC=dc1-r1-h1
SOCK=/home/admin/.weft/weft.sock
WEFT="weft --socket=$SOCK"
LOGDIR=/tmp/chaos-real-host-removal-$(date +%s)
mkdir -p "$LOGDIR"
echo "log dir : $LOGDIR"

TARGET_HOST=dc3-r2-h1
TARGET_IP=192.168.105.23

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
echo "=== Phase 0 : baseline ==="
BASELINE=$(count_hosts)
echo "  host count (with header) : $BASELINE"
TARGET_UUID=$(probe "host ls" | grep $TARGET_HOST | awk '{print $1}')
echo "  target $TARGET_HOST uuid : $TARGET_UUID"
assert "target host is present at baseline" "[ -n \"$TARGET_UUID\" ]"

echo ""
echo "=== Phase 1 : cordon (block new schedules) ==="
probe "host cordon $TARGET_UUID" | tee "$LOGDIR/cordon.txt"
sleep 1

echo ""
echo "=== Phase 2 : set-state down (required gate before rm) ==="
probe "host set-state $TARGET_UUID down" | tee "$LOGDIR/set-state-down.txt"
sleep 1

echo ""
echo "=== Phase 3 : remove ==="
probe "host rm $TARGET_UUID" | tee "$LOGDIR/rm.txt"
sleep 1
AFTER_RM=$(count_hosts)
echo "  host count after rm : $AFTER_RM"
assert "list shrank by 1" "[ \"$AFTER_RM\" = \"$((BASELINE-1))\" ]"

echo ""
echo "=== Phase 4 : kill stale agent on target (its old self-registration record is gone, agent is now an orphan publishing to a removed UUID) ==="
ssh -o ConnectTimeout=3 admin@$TARGET_IP 'pkill -9 weft 2>&1 ; echo "agent killed"' || true
sleep 2

echo ""
echo "=== Phase 5 : restart agent → it self-registers FRESH with a new UUID ==="
ssh -o ConnectTimeout=3 admin@$TARGET_IP 'rm -f /home/admin/.weft/*.sock ; nohup weft agent &> /tmp/weft-agent.log < /dev/null & disown ; sleep 2 ; ps -ef | grep -v grep | grep "weft agent" | wc -l' | tail -1 | xargs -I{} echo "  weft processes : {}"
sleep 3
AFTER_REJOIN=$(count_hosts)
echo "  host count after restart : $AFTER_REJOIN"
assert "host re-appears in inventory (self-registered)" "[ \"$AFTER_REJOIN\" = \"$BASELINE\" ]"

NEW_UUID=$(probe "host ls" | grep $TARGET_HOST | awk '{print $1}')
echo "  new uuid : $NEW_UUID"
# The agent persists its UUID locally so identity is STABLE across
# restart — the re-registration re-uses the same UUID. Validates
# the right design : an operator who restarts an agent doesn't
# rotate the host identity, which would orphan VM records pinned
# to that uuid. Asserting equality.
assert "uuid is stable across restart" "[ \"$NEW_UUID\" = \"$TARGET_UUID\" ]"

echo ""
echo "=== Phase 6 : refresh AZ/rack via re-register by UUID ==="
probe "host register --uuid=$NEW_UUID --hostname=$TARGET_HOST --az=dc3 --rack=r2 --hypervisor=qemu --architecture=arm64" | tee "$LOGDIR/refresh.txt"
SHOW=$(probe "host show $NEW_UUID")
assert "AZ field set" "echo \"$SHOW\" | grep -q dc3"
assert "rack field set" "echo \"$SHOW\" | grep -q r2"

echo ""
echo "=== All phases passed ✓ ==="
echo "Real host removal + automatic re-join via self-registration validated."
echo "Log dir : $LOGDIR"
