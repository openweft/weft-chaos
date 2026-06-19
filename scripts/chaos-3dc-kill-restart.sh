#!/usr/bin/env bash
# chaos-weft.sh — operational chaos for the live 3-DC weft cluster.

set -uo pipefail

LOGDIR=/tmp/chaos-weft-$(date +%s)
mkdir -p "$LOGDIR"
echo "log dir : $LOGDIR"

probe_one() {
  local host="$1"
  ssh -o ConnectTimeout=3 admin@"$host" \
    'timeout 5 weft --socket=/home/admin/.weft/weft.sock host ls >/dev/null 2>&1'
  echo $?
}

# phase_label : drive parallel workload + tally per-host success
phase_label() {
  local name="$1"
  local duration="$2"
  echo ""
  echo "=== Phase : $name ($duration sec) ==="
  local stop=$(($(date +%s) + duration))
  local ok1=0 ok2=0 ok3=0 fail1=0 fail2=0 fail3=0
  while [ "$(date +%s)" -lt "$stop" ]; do
    rc=$(probe_one dc1-r1-h1) ; if [ "$rc" = "0" ]; then ok1=$((ok1+1)); else fail1=$((fail1+1)); fi
    rc=$(probe_one dc2-r1-h1) ; if [ "$rc" = "0" ]; then ok2=$((ok2+1)); else fail2=$((fail2+1)); fi
    rc=$(probe_one dc3-r1-h1) ; if [ "$rc" = "0" ]; then ok3=$((ok3+1)); else fail3=$((fail3+1)); fi
    sleep 0.4
  done
  echo "  dc1 : $ok1 ok / $fail1 fail"
  echo "  dc2 : $ok2 ok / $fail2 fail"
  echo "  dc3 : $ok3 ok / $fail3 fail"
  echo "$name dc1 $ok1 $fail1" >> "$LOGDIR/results.txt"
  echo "$name dc2 $ok2 $fail2" >> "$LOGDIR/results.txt"
  echo "$name dc3 $ok3 $fail3" >> "$LOGDIR/results.txt"
}

# Phase 1 : baseline
phase_label "baseline" 12

# Phase 2 : kill dc2 agent
echo ""
echo "=== Inject : SIGKILL weft-agent on dc2 ==="
ssh admin@dc2-r1-h1 'pkill -9 weft' 2>&1 || true
sleep 1
echo "  dc2 weft processes : $(ssh admin@dc2-r1-h1 'ps -ef | grep -v grep | grep "weft agent" | wc -l')"
phase_label "during-outage" 10

# Phase 3 : restart dc2 agent
echo ""
echo "=== Recover : restart weft-agent on dc2 ==="
ssh admin@dc2-r1-h1 'rm -f /home/admin/.weft/*.sock ; nohup weft agent &> /tmp/weft-agent.log < /dev/null & disown ; sleep 2 ; ps -ef | grep -v grep | grep "weft agent" | wc -l' | tail -1 | xargs -I{} echo "  dc2 weft processes post-restart : {}"
sleep 3
phase_label "post-recover" 10

echo ""
echo "=== Final tally ==="
cat "$LOGDIR/results.txt"
