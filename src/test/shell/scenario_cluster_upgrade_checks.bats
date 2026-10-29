#!/usr/bin/env bats
# SPDX-License-Identifier: Apache-2.0
#
# cluster-upgrade-drill grades a rolling replacement of the worker nodes under
# load. Each check is run against the cluster before any roll, part-way, when
# solved, and in the states that look finished without the property holding.

load 'helpers/stub'

setup() {
  stub_setup
  ROOT="$(project_root)"
  CHECKS="$ROOT/scenarios/cluster-upgrade-drill/checks"
  export WORKLOAD_NAME=cart WORKLOAD_NAMESPACE=shop
  export PROMETHEUS_URL=http://prometheus.lab.test
  unset TARGET_K3S_VERSION AVAILABILITY_SLO UPGRADE_MIN_RPS
  NOW_S="$(date -u +%s)"
  # The cluster as activation leaves it: the two recorded workers are still
  # the live ones, every node is Ready, and the app runs on both.
  export CHECKPOINT_worker_uids="uid-a uid-b" CHECKPOINT_workers="worker-0 worker-1"
  export LIVE_UIDS="uid-server uid-a uid-b"
  export NODE_VERSIONS="$(printf 'server-0 v1.31.4+k3s1\nworker-0 v1.31.4+k3s1\nworker-1 v1.31.4+k3s1')"
  export READINESS="$(printf 'server-0 True\nworker-0 True\nworker-1 True')"
  export PODS="$(printf 'cart-1 Running worker-0\ncart-2 Running worker-1')"
  stub_script kubectl <<'STUB'
case "$*" in
  *"get configmap upgrade-drill-baseline"*)
    args="$*"
    key="${args##*jsonpath=\{.data.}"
    key="${key%%\}*}"
    var="CHECKPOINT_$(printf '%s' "$key" | tr '-' '_')"
    printf '%s' "${!var-}"
    ;;
  *"get nodes"*"{.metadata.uid}"*) [ -z "${LIVE_UIDS-}" ] || printf '%s\n' $LIVE_UIDS ;;
  *"get nodes -l !node-role.kubernetes.io/control-plane"*"kubeletVersion"*)
    [ -z "${NODE_VERSIONS-}" ] || printf '%s\n' "$NODE_VERSIONS" | grep -v '^server-' ;;
  *"get nodes -l node-role.kubernetes.io/control-plane"*"kubeletVersion"*)
    printf '%s\n' "${NODE_VERSIONS-}" | awk '/^server-/ {printf "%s", $2; exit}' ;;
  *"get nodes"*'type=="Ready"'*) [ -z "${READINESS-}" ] || printf '%s\n' "$READINESS" ;;
  *"get nodes"*"unschedulable"*) [ -z "${CORDONED-}" ] || printf '%s\n' "$CORDONED" ;;
  *"get pods -l app=cart"*"waiting.reason"*) [ -z "${WAITING-}" ] || printf '%s\n' "$WAITING" ;;
  *"get pods -l app=cart"*"{.status.phase}"*) [ -z "${PODS-}" ] || printf '%s\n' "$PODS" ;;
esac
exit 0
STUB
  # Prometheus: retention, success ratio and request rate answer separately.
  stub_script curl <<'STUB'
case "$*" in
  *prometheus_tsdb_lowest_timestamp_seconds*) v="${RETENTION_MIN-600}" ;;
  *'5..'*) v="${RATIO-}" ;;
  *) v="${RPS-}" ;;
esac
[ -z "$v" ] || printf '{"status":"success","data":{"result":[{"value":[1700000000,"%s"]}]}}' "$v"
STUB
}

teardown() {
  stub_teardown
}

check() {
  run bash "$CHECKS/$1.sh"
}

# rolled sets a finished roll: both workers replaced on a newer version ten
# minutes ago, with steady load served within the SLO throughout. The control
# plane is not part of the drill and keeps the version it started on.
rolled() {
  export LIVE_UIDS="uid-server uid-c uid-d"
  export NODE_VERSIONS="$(printf 'server-0 v1.31.4+k3s1\nworker-0 v1.32.1+k3s1\nworker-1 v1.32.1+k3s1')"
  export CHECKPOINT_roll_started_at="$((NOW_S - 600))"
  export RPS=19.8 RATIO=0.998
}

@test "the roll-graded checks are pending before any node is rolled" {
  for c in check-node-versions check-availability check-workload-healthy; do
    check "$c"
    [ "$status" -ne 0 ] || { echo "$c passed with no work done: $output"; return 1; }
    [[ "$output" == *"PENDING"* ]] || { echo "$c: $output"; return 1; }
  done
}

@test "every check passes after both workers are replaced under load" {
  rolled
  for c in check-node-versions check-availability check-workload-healthy check-nodes-ready; do
    check "$c"
    [ "$status" -eq 0 ] || { echo "$c: $output"; return 1; }
  done
}

@test "the roll-graded checks cannot grade without the activation checkpoint" {
  rolled
  export CHECKPOINT_worker_uids="" CHECKPOINT_workers=""
  for c in check-node-versions check-availability check-workload-healthy; do
    check "$c"
    [ "$status" -ne 0 ] || { echo "$c passed with no checkpoint: $output"; return 1; }
    [[ "$output" == *"no checkpoint from activation"* ]] || { echo "$c: $output"; return 1; }
  done
}

# ── node versions ─────────────────────────────────────────────────────────────

@test "node-versions names only the workers that have not been rolled" {
  LIVE_UIDS="uid-server uid-c uid-b" check check-node-versions
  [ "$status" -ne 0 ]
  [[ "$output" == *"worker-1"* ]] || false
  [[ "$output" != *"worker-0"* ]] || false
}

@test "node-versions fails workers rolled onto different versions" {
  rolled
  NODE_VERSIONS="$(printf 'server-0 v1.31.4+k3s1\nworker-0 v1.32.1+k3s1\nworker-1 v1.31.9+k3s1')" check check-node-versions
  [ "$status" -ne 0 ]
  [[ "$output" == *"mixed kubelet versions"* ]] || false
  [[ "$output" == *"worker-1 v1.31.9+k3s1"* ]] || false
}

@test "node-versions does not hold the workers to the control plane's version" {
  rolled
  check check-node-versions
  [ "$status" -eq 0 ]
  [[ "$output" == *"v1.32.1+k3s1"* ]] || false
  [[ "$output" == *"The control plane stays on v1.31.4+k3s1"* ]] || false
}

@test "node-versions holds the cluster to TARGET_K3S_VERSION when it is set" {
  rolled
  TARGET_K3S_VERSION=v1.32.1-k3s1 check check-node-versions
  [ "$status" -eq 0 ]
  TARGET_K3S_VERSION=v1.33.0-k3s1 check check-node-versions
  [ "$status" -ne 0 ]
  [[ "$output" == *"asks for v1.33.0+k3s1"* ]] || false
}

@test "node-versions fails when the versions cannot be read" {
  rolled
  NODE_VERSIONS="" check check-node-versions
  [ "$status" -ne 0 ]
  [[ "$output" == *"could not read node versions"* ]] || false
}

# ── availability ──────────────────────────────────────────────────────────────

@test "availability grades the window since the roll began" {
  rolled
  check check-availability
  [ "$status" -eq 0 ]
  [[ "$output" == *"across the 10m since the"* ]] || false
}

@test "availability narrows the window to what Prometheus still holds, and caps it at thirty minutes" {
  rolled
  RETENTION_MIN=4 check check-availability
  [[ "$output" == *"across the 4m since the"* ]] || false
  CHECKPOINT_roll_started_at="$((NOW_S - 7200))" check check-availability
  [[ "$output" == *"across the 30m since the"* ]] || false
}

@test "availability fails an upgrade nobody was sending traffic through" {
  rolled
  for rps in "" 0 0.2; do
    RPS="$rps" check check-availability
    [ "$status" -ne 0 ] || { echo "rps '$rps' passed"; return 1; }
    [[ "$output" == *"proves nothing about availability"* ]] || false
  done
}

@test "availability fails a roll that dropped below the SLO" {
  rolled
  RATIO=0.93 check check-availability
  [ "$status" -ne 0 ]
  [[ "$output" == *"served 0.93 of requests"* ]] || false
  [[ "$output" == *"below the 0.99 SLO"* ]] || false
}

@test "availability fails when Prometheus has no figure" {
  rolled
  RATIO="" check check-availability
  [ "$status" -ne 0 ]
  [[ "$output" == *"no availability figure"* ]] || false
}

@test "availability stamps the start of the roll the first time it sees one" {
  rolled
  CHECKPOINT_roll_started_at="" check check-availability
  assert_called kubectl "patch configmap upgrade-drill-baseline"
}

# ── workload healthy ──────────────────────────────────────────────────────────

@test "workload-healthy fails each way the app can come back wrong" {
  rolled
  while IFS='|' read -r var value want; do
    env "$var=$(printf '%b' "$value")" bash "$CHECKS/check-workload-healthy.sh" >"$STUB_DIR/out" 2>&1 && {
      echo "$var=$value passed"
      return 1
    }
    grep -q "$want" "$STUB_DIR/out" || { cat "$STUB_DIR/out"; return 1; }
  done <<'CASES'
PODS||no cart pods found
PODS|cart-1 Running worker-0\ncart-2 Pending |cart-2 (Pending)
PODS|cart-1 Running worker-0\ncart-2 Running worker-0|on a single node
WAITING|cart-2 ImagePullBackOff|cannot pull their image
CASES
}

# ── nodes ready ───────────────────────────────────────────────────────────────

@test "nodes-ready fails a node that is NotReady or still cordoned" {
  READINESS="$(printf 'server-0 True\nworker-0 False\nworker-1 True')" check check-nodes-ready
  [ "$status" -ne 0 ]
  [[ "$output" == *"worker-0"* ]] || false

  CORDONED=worker-1 check check-nodes-ready
  [ "$status" -ne 0 ]
  [[ "$output" == *"still cordoned"* ]] || false
}

@test "nodes-ready does not pass a cluster it could not query" {
  READINESS="" check check-nodes-ready
  [ "$status" -ne 0 ]
  [[ "$output" == *"could not list the nodes"* ]] || false
}

