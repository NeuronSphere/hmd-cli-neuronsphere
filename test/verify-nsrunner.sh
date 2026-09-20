#!/usr/bin/env bash
# Standalone verification of the DAG runner: no NeuronSphere platform needed,
# only Docker. Builds the image, starts the service against a throwaway
# HMD_HOME, and checks the claims Phases 4 and 5 rest on -- node execution and
# failure reporting across the container boundary, then that the scheduler
# respects the DAG's edges and is actually faster for obeying only those.
set -euo pipefail

REPO="${REPO:-/Users/aburg/hmdtr1/projects/hmd-cli-neuronsphere}"
IMAGE="${IMAGE:-hmd-img-nsrunner:$(cat "$REPO/meta-data/VERSION")}"
PORT="${PORT:-18098}"
# Stands in for hmd-img-projectbuilder. Any image with bash will do.
NODE_IMAGE="${NODE_IMAGE:-python:3.11-slim}"
NAME=nsrunner-verify

pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; FAILED=1; }
FAILED=0

TMP="$(mktemp -d)"; HOME_DIR="$TMP/home"; REPO_HOME="$TMP/repos"
cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; rm -rf "$TMP"; }
trap cleanup EXIT

mkdir -p "$HOME_DIR/.cache/neuronsphere" "$REPO_HOME/hmd-inf-thing"
echo "marker-from-host" > "$REPO_HOME/hmd-inf-thing/MARKER.txt"
cat > "$HOME_DIR/.cache/neuronsphere/environments.json" <<JSON
{"version":1,"default_env":"local",
 "control_plane":{"network":"bridge","bootstrapped":true,"compose_project":"ns-verify"},
 "environments":{"local":{"name":"local","slug":"local","account_id":"000000000001",
   "deployment_id":"local","state_dir":"$HOME_DIR/.cache/environments/local",
   "k3s_cluster":"ns-verify","port_slot":0}}}
JSON

echo "==> building $IMAGE"
make -C "$REPO" image NSRUNNER_IMAGE="$IMAGE" >/dev/null

echo "==> starting the runner"
docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" -p "$PORT:8080" \
  -e HMD_HOME="$HOME_DIR" -e HMD_REPO_HOME="$REPO_HOME" \
  -v "$HOME_DIR:$HOME_DIR" -v "$REPO_HOME:$REPO_HOME" \
  -v /var/run/docker.sock:/var/run/docker.sock "$IMAGE" >/dev/null
for _ in $(seq 30); do curl -sf "http://localhost:$PORT/healthz" >/dev/null && break; sleep 1; done

API="http://localhost:$PORT/api/v1/workflows/local"
submit() { curl -s -X POST "$API" -H 'Content-Type: application/json' -d "$1"; }
phase()  { curl -s "$API?environment=local" | python3 -c "
import sys,json
w=[x for x in json.load(sys.stdin)['items'] if x['csd_nid']=='$1']
print(json.dumps(w[0]) if w else '{}')"; }
# node <instance> <script-json> [dependencies-json]
node() { printf '{"instance_name":"%s","repo_class_name":"hmd-inf-thing","version":"0.1","rid_nid":"","script":%s,"dependencies":%s}' "$1" "$2" "${3:-[]}"; }
# wf <csd> <nodes-json> [parallelism]  -- 0 means the runner's default of 4
wf()   { printf '{"namespace":"local","workflow":{"csd_nid":"%s","environment":"local","nodes":[%s],"parallelism":%s,"config":{"image":"%s","network":"bridge","repo_home":"%s"}}}' "$1" "$2" "${3:-0}" "$NODE_IMAGE" "$REPO_HOME"; }
settle() { for _ in $(seq 120); do p=$(phase "$1" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d.get("phase",""))'); case "$p" in Succeeded|Failed) echo "$p"; return;; esac; sleep 1; done; echo Timeout; }
# Each node appends its own start and finish to one file on the shared
# workspace, which is what makes the schedule observable from outside.
trace() { printf '"echo %s-start >> /workspace/ORDER.txt; sleep 2; echo %s-done >> /workspace/ORDER.txt"' "$1" "$1"; }

echo "==> 1. a node runs in a sibling container with the host working tree mounted"
submit "$(wf ws "$(node w '"test -f /workspace/MARKER.txt && grep -q marker-from-host /workspace/MARKER.txt"')")" >/dev/null
[ "$(settle ws)" = Succeeded ] && pass "the developer's tree reached the deploy node" \
                               || fail "workspace mount broken"

echo "==> 2. a failing node carries its detail back"
submit "$(wf boom "$(node b '"echo about-to-fail >&2; exit 3"')")" >/dev/null
settle boom >/dev/null
phase boom | python3 -c '
import sys,json; w=json.load(sys.stdin); f=w.get("failure") or {}
ok = w["phase"]=="Failed" and f.get("instance_name")=="b" and "about-to-fail" in (f.get("stderr") or "")
sys.exit(0 if ok else 1)' && pass "stderr and the failing instance crossed the boundary" \
                          || fail "failure detail lost"

echo "==> 3. the deployment survives its client going away, and refuses a racer"
submit "$(wf slow "$(node s '"echo start; sleep 20; echo done"')")" >/dev/null
sleep 3
curl -sN --max-time 2 "$API/$(phase slow | python3 -c 'import sys,json;print(json.load(sys.stdin)["name"])')/log?follow=true" >/dev/null 2>&1 || true
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API" -H 'Content-Type: application/json' \
       -d '{"namespace":"local","workflow":{"csd_nid":"race","environment":"local","nodes":[]}}')
[ "$code" = 409 ] && pass "a second submission was refused 409" || fail "concurrent submit returned $code, want 409"
[ "$(settle slow)" = Succeeded ] && pass "it finished with no client attached" \
                                 || fail "the deployment died with its client"

echo "==> 4. the scheduler respects the edges and overlaps what has none"
ORDER="$REPO_HOME/hmd-inf-thing/ORDER.txt"; rm -f "$ORDER"
cat > "$TMP/order_check.py" <<'ORDERCHECK'
import sys
ev = [l.strip() for l in open(sys.argv[1]) if l.strip()]
at = {name: i for i, name in enumerate(ev)}
try:
    ok = (at["top-done"] < at["left-start"] and at["top-done"] < at["right-start"]
          and at["left-start"] < at["right-done"] and at["right-start"] < at["left-done"]
          and at["left-done"] < at["bottom-start"] and at["right-done"] < at["bottom-start"])
except KeyError as missing:
    ok, ev = False, ev + ["(never ran: %s)" % missing]
if not ok:
    print("    order was: " + " ".join(ev))
sys.exit(0 if ok else 1)
ORDERCHECK
DIAMOND="$(node top "$(trace top)"),$(node left "$(trace left)" '["top"]')"
DIAMOND="$DIAMOND,$(node right "$(trace right)" '["top"]'),$(node bottom "$(trace bottom)" '["left","right"]')"
submit "$(wf diamond "$DIAMOND")" >/dev/null
settle diamond >/dev/null
python3 "$TMP/order_check.py" "$ORDER" \
  && pass "top gated the middle, the middle overlapped, bottom waited for both" \
  || fail "the DAG order was not respected"

echo "==> 5. the same DAG is measurably faster in parallel than in sequence"
# Six nodes with no edges between them -- the shape a cold bootstrap's
# foundation services actually have. The number is measured rather than
# asserted: what matters is that concurrency buys something real, not that it
# buys exactly four times.
WIDE=""
for n in n1 n2 n3 n4 n5 n6; do WIDE="${WIDE:+$WIDE,}$(node $n '"sleep 3"')"; done
elapsed() { local start=$SECONDS; submit "$(wf "$1" "$WIDE" "$2")" >/dev/null; local r; r=$(settle "$1"); echo "$((SECONDS - start)) $r"; }
read -r SEQ SEQ_PHASE <<<"$(elapsed seq 1)"
read -r PAR PAR_PHASE <<<"$(elapsed par 0)"
echo "    sequential: ${SEQ}s ($SEQ_PHASE)   parallel: ${PAR}s ($PAR_PHASE)"
if [ "$SEQ_PHASE" != Succeeded ] || [ "$PAR_PHASE" != Succeeded ]; then
  fail "the two runs did not both succeed, so their times say nothing"
elif [ "$PAR" -lt "$((SEQ * 7 / 10))" ]; then
  pass "parallel took ${PAR}s against ${SEQ}s sequential, with identical outcomes"
else
  fail "parallel took ${PAR}s against ${SEQ}s sequential -- no measurable gain"
fi

echo
[ "$FAILED" = 0 ] && echo "All checks passed." || { echo "Some checks FAILED."; exit 1; }
