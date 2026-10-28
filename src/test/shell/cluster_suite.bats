#!/usr/bin/env bats
# SPDX-License-Identifier: Apache-2.0
#
# The cluster-suite runner (src/test/cluster/run.sh) and the solve scripts it
# runs. labctl and kubectl are stand-ins that keep the lab's state in files,
# so each test can make one step misbehave and check the runner stops there.

load 'helpers/stub'

setup() {
  stub_setup
  ROOT="$(project_root)"
  RUN="$ROOT/src/test/cluster/run.sh"
  export LAB="$BATS_TEST_TMPDIR/lab" POLL_SECONDS=0 WAIT_SECONDS=0
  mkdir -p "$LAB"
  export DETECTION_PASSES_BROKEN=no RESOLVE_LEAVES_ACTIVE=no LEAK_NS="" RECORD_LEFT=no VERIFY_ALWAYS=no

  stub_script labctl <<'STUB'
case "$*" in
  "incident inject"*)
    touch "$LAB/active"
    rm -f "$LAB/fixed"
    case "$*" in *--deploy-prereqs*) touch "$LAB/prereqs" ;; esac
    ;;
  "incident status")
    if [ -f "$LAB/active" ] && { [ -f "$LAB/fixed" ] || [ "$DETECTION_PASSES_BROKEN" = yes ]; }; then
      echo 'RESOLVED - detection check "c" passes. Nice work.'
      rm -f "$LAB/active"
    elif [ -f "$LAB/active" ]; then
      echo 'NOT RESOLVED - detection check "c" fails'
    else
      echo 'No incident is active.'
    fi
    ;;
  "incident resolve") [ "$RESOLVE_LEAVES_ACTIVE" = yes ] || rm -f "$LAB/active" ;;
  "scenario up"*)
    touch "$LAB/up"
    case "$*" in *--deploy-prereqs*) touch "$LAB/prereqs" ;; esac
    ;;
  "scenario verify"*) [ -f "$LAB/solved" ] || [ "$VERIFY_ALWAYS" = yes ] ;;
  "scenario down"*)
    # The first down ends the runner's warm-up; only the drill's own down leaks.
    rm -f "$LAB/up"
    [ ! -f "$LAB/warmed" ] || [ -z "$LEAK_NS" ] || touch "$LAB/leaked"
    touch "$LAB/warmed"
    ;;
  "traffic start"*) touch "$LAB/traffic" ;;
  "traffic stop") rm -f "$LAB/traffic" ;;
esac
STUB
  stub_script kubectl <<'STUB'
case "$*" in
  "get namespaces -o name")
    printf 'namespace/default\nnamespace/go-api\n'
    [ ! -f "$LAB/leaked" ] || printf 'namespace/%s\n' "$LEAK_NS"
    # Prerequisites (the app, the ingress) stay installed by design.
    [ ! -f "$LAB/prereqs" ] || printf 'namespace/traefik\n'
    [ ! -f "$LAB/traffic" ] || printf 'namespace/traffic\n'
    ;;
  *"get configmap labfault-"*) [ "$RECORD_LEFT" = yes ]; exit $? ;;
  "api-resources"*) printf 'pods\nconfigmaps\nscaledobjects.keda.sh\npolicyreports.wgpolicyk8s.io\n' ;;
  "-n go-api get configmaps -o name") echo configmap/kube-root-ca.crt ;;
  "-n go-api get scaledobjects.keda.sh -o name") [ ! -f "$LAB/scaledobject" ] || echo scaledobject.keda.sh/go-api ;;
  "-n go-api get pods -o name") echo "pod/go-api-$RANDOM" ;;
  *"delete networkpolicy"*) touch "$LAB/fixed" ;;
esac
exit 0
STUB
}

@test "an incident that breaks, restores and can be fixed by hand passes" {
  run "$RUN" incident network-blackhole
  [ "$status" -eq 0 ]
  [[ "$output" == *"PASS  the detection check fails on the broken lab"* ]] || false
  [[ "$output" == *"PASS  resolve restores the lab"* ]] || false
  [[ "$output" == *"PASS  the detection check passes on the fixed lab"* ]] || false
  assert_called kubectl "-n go-api delete networkpolicy default-deny-ingress"
  [ "$(calls_for labctl | tail -1)" = "incident resolve network-blackhole" ]
  [[ "$output" == *"PASS  nothing left behind after a hand fix and resolve"* ]] || false
}

@test "the warm-up installs prerequisites before the namespaces are counted" {
  run "$RUN" incident network-blackhole
  [ "$status" -eq 0 ]
  in_calls=$(calls_for labctl | grep -n "incident inject" | cut -d: -f2-)
  [ "$(echo "$in_calls" | head -1)" = "incident inject network-blackhole --app go-api --deploy-prereqs" ]
  [ "$(echo "$in_calls" | sed -n 2p)" = "incident inject network-blackhole --app go-api" ]
  [[ "$output" == *"PASS  prerequisites in place"* ]] || false
}

@test "an incident whose check passes on the broken lab fails" {
  DETECTION_PASSES_BROKEN=yes run "$RUN" incident network-blackhole
  [ "$status" -ne 0 ]
  [[ "$output" == *"the detection check passes on the broken lab"* ]] || false
}

@test "an incident that resolve does not clear fails" {
  RESOLVE_LEAVES_ACTIVE=yes run "$RUN" incident network-blackhole
  [ "$status" -ne 0 ]
  [[ "$output" == *"still active after resolve"* ]] || false
}

@test "an incident that leaves its fault record behind fails" {
  RECORD_LEFT=yes run "$RUN" incident network-blackhole
  [ "$status" -ne 0 ]
  [[ "$output" == *"configmap/labfault-network-blackhole"* ]] || false
}

@test "an incident whose hand fix never passes fails at the deadline" {
  stub_script kubectl <<'STUB'
case "$*" in
  "get namespaces -o name") printf 'namespace/default\n' ;;
  *"get configmap labfault-"*) exit 1 ;;
esac
exit 0
STUB
  run "$RUN" incident network-blackhole
  [ "$status" -ne 0 ]
  [[ "$output" == *"does not pass on the fixed lab"* ]] || false
}

# A scenario item lives in a scratch checkout, so the test controls its solve.
scratch_scenario() {
  export ITEMS_ROOT="$BATS_TEST_TMPDIR/items"
  mkdir -p "$ITEMS_ROOT/scenarios/drill/test"
  printf '#!/bin/sh\ntouch "$LAB/solved"\n' >"$ITEMS_ROOT/scenarios/drill/test/solve.sh"
  chmod +x "$ITEMS_ROOT/scenarios/drill/test/solve.sh"
}

@test "a scenario that fails untouched, passes solved and tears down cleanly passes" {
  scratch_scenario
  run "$RUN" scenario drill
  [ "$status" -eq 0 ]
  [[ "$output" == *"PASS  verify fails on the untouched lab"* ]] || false
  [[ "$output" == *"PASS  verify passes on the solved lab"* ]] || false
  [[ "$output" == *"PASS  nothing left behind after scenario down"* ]] || false
}

@test "record mode writes the untouched and solved fixtures into the scenario" {
  scratch_scenario
  RECORD=1 run "$RUN" scenario drill
  [ "$status" -eq 0 ]
  assert_called labctl "scenario verify drill --record $ITEMS_ROOT/scenarios/drill/test/fixtures/untouched.json"
  assert_called labctl "scenario verify drill --watch --interval 10s --timeout 0s --record $ITEMS_ROOT/scenarios/drill/test/fixtures/solved.json"
  [ "$(call_count labctl "scenario verify drill")" -eq 2 ]
  [[ "$output" == *"PASS  recorded the untouched and solved check fixtures"* ]] || false
}

@test "without record mode no fixture is written" {
  scratch_scenario
  run "$RUN" scenario drill
  [ "$status" -eq 0 ]
  refute_called labctl "--record"
}

@test "a scenario whose checks pass on the untouched lab fails" {
  scratch_scenario
  VERIFY_ALWAYS=yes run "$RUN" scenario drill
  [ "$status" -ne 0 ]
  [[ "$output" == *"the checks grade nothing"* ]] || false
}

@test "a scenario that leaves an object in the workload's namespace fails, naming it" {
  scratch_scenario
  printf '#!/bin/sh\ntouch "$LAB/scaledobject" "$LAB/solved"\n' >"$ITEMS_ROOT/scenarios/drill/test/solve.sh"
  run "$RUN" scenario drill
  [ "$status" -ne 0 ]
  [[ "$output" == *"left behind in namespace go-api after scenario down: scaledobject.keda.sh/go-api"* ]] || false
}

@test "pods that churn and objects that were there before are not counted as left behind" {
  scratch_scenario
  run "$RUN" scenario drill
  [ "$status" -eq 0 ]
  refute_called kubectl "get pods -o name"
  refute_called kubectl "get policyreports.wgpolicyk8s.io"
}

@test "a scenario that leaves a namespace behind fails" {
  scratch_scenario
  LEAK_NS=drill-ns run "$RUN" scenario drill
  [ "$status" -ne 0 ]
  [[ "$output" == *"namespace/drill-ns"* ]] || false
}

@test "traffic a solve starts is stopped before the scenario comes down" {
  scratch_scenario
  printf '#!/bin/sh\n"$LABCTL" traffic start --app "$WORKLOAD_NAME"\ntouch "$LAB/solved"\n' \
    >"$ITEMS_ROOT/scenarios/drill/test/solve.sh"
  LABCTL="$STUB_BIN/labctl" run "$RUN" scenario drill
  [ "$status" -eq 0 ]
  assert_called labctl "traffic start --app go-api"
  assert_called labctl "traffic stop"
  [[ "$output" == *"PASS  nothing left behind after scenario down"* ]] || false
}

@test "an item with no solve script fails and says so" {
  export ITEMS_ROOT="$BATS_TEST_TMPDIR/empty"
  mkdir -p "$ITEMS_ROOT/scenarios/drill"
  run "$RUN" scenario drill
  [ "$status" -ne 0 ]
  [[ "$output" == *"has no executable test/solve.sh"* ]] || false
}

@test "an unknown kind is refused" {
  run "$RUN" chart drill
  [ "$status" -ne 0 ]
  [[ "$output" == *"want scenario or incident"* ]] || false
}

# Each fault's solve script makes the fix its solution.md describes.
@test "every fault's solve script applies its documented fix" {
  export WORKLOAD_NAME=go-api WORKLOAD_NAMESPACE=go-api WORKLOAD_PORT=8080 PROJECT_ROOT="$ROOT"
  stub_script kubectl <<'STUB'
case "$*" in
  *"get configmap labfault-oom-kill"*"original-limit"*) printf '%s' 128Mi ;;
  *"get configmap labfault-oom-kill"*"original-request"*) printf '%s' 32Mi ;;
esac
exit 0
STUB
  while IFS='|' read -r fault call; do
    run "$ROOT/incidents/$fault/test/solve.sh"
    [ "$status" -eq 0 ] || { echo "$fault: exit $status: $output"; false; }
    assert_called kubectl "$call"
  done <<'TABLE'
service-selector-broken|-n go-api patch svc go-api
crashloop-bad-config|-n go-api set env deploy/go-api PORT=8080
bad-deploy-rollout|-n go-api rollout undo deploy/go-api
network-blackhole|-n go-api delete networkpolicy default-deny-ingress
oom-kill|-n go-api set resources deploy/go-api --limits=memory=128Mi --requests=memory=32Mi
noisy-neighbor|-n labfault-batch set resources deploy/report-batch --requests=cpu=100m --limits=cpu=200m
pending-unschedulable|-n go-api set resources deploy/go-api --requests=cpu=100m --limits=cpu=500m
liveness-probe-kill|-n go-api patch deploy go-api --type=json
missing-secret-key|-n go-api patch deploy go-api --type=strategic
cpu-throttling|-n go-api set resources deploy/go-api --requests=cpu=50m --limits=cpu=200m
dns-egress-blocked|-n go-api patch networkpolicy default-deny-egress --type=json
stray-pod-behind-service|-n go-api delete pod go-api-debug --ignore-not-found
TABLE
}

@test "every verified fault has a solve script" {
  for dir in "$ROOT"/incidents/*/; do
    [ -f "$dir/fault.yaml" ] || continue
    grep -q '^verified: true' "$dir/fault.yaml" || continue
    [ -x "$dir/test/solve.sh" ] || { echo "$(basename "$dir") has no test/solve.sh"; false; }
  done
}

@test "env-promotion's solve promotes v1.1.0 through every env and rolls prod back" {
  export WORKLOAD_NAME=go-api WORKLOAD_NAMESPACE=go-api PROJECT_ROOT="$BATS_TEST_TMPDIR/root"
  mkdir -p "$PROJECT_ROOT/scenarios/env-promotion/scripts"
  printf '#!/bin/sh\necho "built $*"\n' >"$PROJECT_ROOT/scenarios/env-promotion/scripts/build-image.sh"
  stub_script kubectl <<'STUB'
case "$*" in
  *"get rs"*) printf 'go-api:v1.1.0 go-api:v9.9.9' ;;
esac
exit 0
STUB
  run "$ROOT/scenarios/env-promotion/test/solve.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"built v1.1.0 --app go-api --namespace go-api"* ]] || false
  for env_name in dev staging prod; do
    assert_called kubectl "-n env-$env_name set image deployment/go-api go-api=go-api:v1.1.0"
  done
  assert_called kubectl "-n env-prod set image deployment/go-api go-api=go-api:v9.9.9"
  assert_called kubectl "-n env-prod rollout undo deployment/go-api"
}

@test "secrets-management's solve rotates the key in Vault and touches nothing in Kubernetes" {
  export WORKLOAD_NAME=go-api WORKLOAD_NAMESPACE=go-api PROJECT_ROOT="$ROOT"
  run "$ROOT/scenarios/secrets-management/test/solve.sh"
  [ "$status" -eq 0 ]
  assert_called kubectl "exec -n vault vault-0 -- sh -c"
  assert_called kubectl "vault kv patch secret/go-api api-key=rotated-"
  refute_called kubectl "rollout"
  refute_called kubectl "set "
  refute_called kubectl "delete"
}

# cost-right-sizing's solve reads Prometheus through curl: the load rate, then
# the CPU (cores) and memory (bytes) peaks.
stub_prometheus() {
  export PROM_RATE="$1" PROM_CPU="$2" PROM_MEM="$3"
  stub_script curl <<'STUB'
case "$*" in
  *_count*) v="$PROM_RATE" ;;
  *container_cpu_usage_seconds_total*) v="$PROM_CPU" ;;
  *container_memory_working_set_bytes*) v="$PROM_MEM" ;;
esac
[ -n "$v" ] && printf '{"data":{"result":[{"value":[1700000000,"%s"]}]}}' "$v"
exit 0
STUB
  stub_script sleep <<'STUB'
exit 0
STUB
}

@test "cost-right-sizing's solve sizes requests above the peak under load" {
  export WORKLOAD_NAME=go-api WORKLOAD_NAMESPACE=go-api PROJECT_ROOT="$ROOT"
  stub_prometheus 6.2 0.004 20971520 # 4m of CPU, 20Mi of memory
  run "$ROOT/scenarios/cost-right-sizing/test/solve.sh"
  [ "$status" -eq 0 ]
  assert_called labctl "traffic start --app go-api --profile steady --rps 25"
  assert_called kubectl "-n go-api set resources deployment/go-api --requests=cpu=24m,memory=40Mi"
  assert_called kubectl "-n go-api rollout status deployment/go-api"
}

@test "cost-right-sizing's solve never sizes over the scenario's ceilings" {
  export WORKLOAD_NAME=go-api WORKLOAD_NAMESPACE=go-api PROJECT_ROOT="$ROOT"
  stub_prometheus 30 0.09 209715200 # 90m of CPU, 200Mi of memory
  run "$ROOT/scenarios/cost-right-sizing/test/solve.sh"
  [ "$status" -eq 0 ]
  assert_called kubectl "--requests=cpu=100m,memory=256Mi"
}

@test "cost-right-sizing's solve stops when the load never arrives" {
  export WORKLOAD_NAME=go-api WORKLOAD_NAMESPACE=go-api PROJECT_ROOT="$ROOT"
  stub_prometheus 0 0.004 20971520
  run "$ROOT/scenarios/cost-right-sizing/test/solve.sh"
  [ "$status" -ne 0 ]
  [[ "$output" == *"never reached 5 requests a second"* ]] || false
  refute_called kubectl "set resources"
}

@test "cost-right-sizing's solve stops when Prometheus has no usage" {
  export WORKLOAD_NAME=go-api WORKLOAD_NAMESPACE=go-api PROJECT_ROOT="$ROOT"
  stub_prometheus 6 "" ""
  run "$ROOT/scenarios/cost-right-sizing/test/solve.sh"
  [ "$status" -ne 0 ]
  [[ "$output" == *"Prometheus returned no usage"* ]] || false
  refute_called kubectl "set resources"
}

@test "the matrix lists each item with a solve script, on the runtime and workers it needs" {
  items="$BATS_TEST_TMPDIR/items-root"
  for s in plain k3d-only two-workers no-solve; do mkdir -p "$items/scenarios/$s/test"; done
  printf 'name: plain\nruntimes:\n  - k3d\n  - kind\nobjectives: []\n' >"$items/scenarios/plain/scenario.yaml"
  printf 'name: k3d-only\nrequirements:\n  agents: 2\n  exclusive: true\nruntimes:\n  - k3d\n' >"$items/scenarios/k3d-only/scenario.yaml"
  printf 'name: two-workers\nrequirements:\n  agents: 2\n' >"$items/scenarios/two-workers/scenario.yaml"
  printf 'name: no-solve\n' >"$items/scenarios/no-solve/scenario.yaml"
  mkdir -p "$items/incidents/a-fault/test"
  for f in scenarios/plain scenarios/k3d-only scenarios/two-workers incidents/a-fault; do
    printf '#!/bin/sh\n' >"$items/$f/test/solve.sh"
    chmod +x "$items/$f/test/solve.sh"
  done

  run "$ROOT/src/test/cluster/items.sh" "$items"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 4 ]
  [[ "$output" == *'{"kind":"scenario","name":"plain","profile":"kind","agents":1}'* ]] || false
  [[ "$output" == *'{"kind":"scenario","name":"k3d-only","profile":"k3d","agents":2}'* ]] || false
  [[ "$output" == *'{"kind":"scenario","name":"two-workers","profile":"kind","agents":2}'* ]] || false
  [[ "$output" == *'{"kind":"incident","name":"a-fault","profile":"kind","agents":1}'* ]] || false
  [[ "$output" != *no-solve* ]] || false
}

# A checkout with two scenarios and a fault, and the results of a nightly run.
nightly_checkout() {
  CO="$BATS_TEST_TMPDIR/checkout"
  RES="$BATS_TEST_TMPDIR/results"
  mkdir -p "$CO/scenarios/good" "$CO/scenarios/flaky" "$CO/incidents/a-fault" "$CO/src/internal/scenario/testdata"
  printf 'apiVersion: v2\nname: good\nverified: false\ndisplayName: Good\n' >"$CO/scenarios/good/scenario.yaml"
  printf 'name: flaky\nverified: true\n' >"$CO/scenarios/flaky/scenario.yaml"
  printf 'name: a-fault\ncategory: x\n' >"$CO/incidents/a-fault/fault.yaml"
  printf '# header\nflaky\ngood\n' >"$CO/src/internal/scenario/testdata/unrecorded-scenarios.txt"
  mkdir -p "$RES/scenario-good/fixtures" "$RES/scenario-flaky/fixtures" "$RES/incident-a-fault"
  echo pass >"$RES/scenario-good/result"
  echo '{"results":{}}' >"$RES/scenario-good/fixtures/untouched.json"
  echo '{"results":{"x":true}}' >"$RES/scenario-good/fixtures/solved.json"
  echo fail >"$RES/scenario-flaky/result"
  echo '{}' >"$RES/scenario-flaky/fixtures/untouched.json"
  echo '{}' >"$RES/scenario-flaky/fixtures/solved.json"
  echo pass >"$RES/incident-a-fault/result"
}

@test "nightly results set each item's verified flag and land the passing scenarios' fixtures" {
  nightly_checkout
  run "$ROOT/src/test/cluster/apply-results.sh" "$RES" "$CO"
  [ "$status" -eq 0 ]
  grep -qx 'verified: true' "$CO/scenarios/good/scenario.yaml"
  grep -qx 'verified: false' "$CO/scenarios/flaky/scenario.yaml"
  [ "$(sed -n 3p "$CO/incidents/a-fault/fault.yaml")" = "category: x" ]
  [ "$(sed -n 2p "$CO/incidents/a-fault/fault.yaml")" = "verified: true" ]
  [ "$(cat "$CO/scenarios/good/test/fixtures/solved.json")" = '{"results":{"x":true}}' ]
  [ ! -d "$CO/scenarios/flaky/test/fixtures" ]
  [ "$(cat "$CO/src/internal/scenario/testdata/unrecorded-scenarios.txt")" = "$(printf '# header\nflaky')" ]
}

@test "a result for an item the checkout does not have is skipped, not invented" {
  nightly_checkout
  mkdir -p "$RES/scenario-gone" "$RES/oddity"
  echo pass >"$RES/scenario-gone/result"
  run "$ROOT/src/test/cluster/apply-results.sh" "$RES" "$CO"
  [ "$status" -eq 0 ]
  [ ! -d "$CO/scenarios/gone" ]
  [[ "$output" == *"skipping scenario-gone"* ]] || false
  [[ "$output" == *"skipping oddity"* ]] || false
}

@test "a missing result counts as a failure" {
  nightly_checkout
  rm "$RES/scenario-good/result"
  run "$ROOT/src/test/cluster/apply-results.sh" "$RES" "$CO"
  [ "$status" -eq 0 ]
  grep -qx 'verified: false' "$CO/scenarios/good/scenario.yaml"
  [ ! -d "$CO/scenarios/good/test/fixtures" ]
}
