#!/usr/bin/env bats
# SPDX-License-Identifier: Apache-2.0

load 'helpers/stub'

setup() {
  stub_setup
  START="$(project_root)/src/services/traffic/start.sh"
  export TRAFFIC_METRICS=off
  export TRAFFIC_NAMESPACE=traffic-test
  export TRAFFIC_STATE="$STUB_DIR/state"
  mkdir -p "$TRAFFIC_STATE"
  stub_script kubectl <<'EOF'
pause_at() {
  [ "${CANCEL_AT:-}" = "$1" ] || return 0
  echo "$$" >"$TRAFFIC_STATE/child.pid"
  : >"$TRAFFIC_STATE/reached"
  trap ': >"$TRAFFIC_STATE/child-stopped"; exit 143' TERM HUP INT
  while [ ! -f "$TRAFFIC_STATE/release" ]; do sleep 0.02; done
}
fail_at() {
  [ "${FAIL_AT:-}" != "$1" ] || exit 23
}
case "$*" in
  'get namespace '*)
    pause_at before
    fail_at probe
    [ ! -f "$TRAFFIC_STATE/namespace" ] || echo namespace/traffic-test
    ;;
  'get configmap traffic-profile '*)
    [ ! -f "$TRAFFIC_STATE/configmap" ] || echo configmap/traffic-profile
    ;;
  'create namespace '*)
    fail_at namespace-manifest
    printf 'kind: Namespace\n'
    ;;
  'create configmap traffic-profile '*)
    fail_at configmap-manifest
    printf 'kind: ConfigMap\n'
    ;;
  'apply -f -')
    manifest=$(cat)
    case "$manifest" in
      *'kind: Namespace'*) resource=namespace ;;
      *'kind: ConfigMap'*) resource=configmap ;;
      *'kind: Job'*) resource=job; printf '%s\n' "$manifest" >"$TRAFFIC_STATE/job.yaml" ;;
      *) exit 24 ;;
    esac
    fail_at "apply-$resource"
    : >"$TRAFFIC_STATE/$resource"
    pause_at "$resource"
    ;;
  'delete job traffic-k6 '*)
    fail_at delete-job
    rm -f "$TRAFFIC_STATE/job"
    ;;
  'delete configmap traffic-profile '*)
    fail_at delete-configmap
    rm -f "$TRAFFIC_STATE/configmap"
    ;;
  'delete namespace '*)
    fail_at delete-namespace
    rm -f "$TRAFFIC_STATE/namespace"
    ;;
  'wait '*)
    pause_at ready
    fail_at ready
    ;;
esac
EOF
}

teardown() {
  if [ -f "$TRAFFIC_STATE/child.pid" ]; then
    kill "$(cat "$TRAFFIC_STATE/child.pid")" 2>/dev/null || true
  fi
  if [ -n "${start_pid:-}" ]; then
    kill "$start_pid" 2>/dev/null || true
    wait "$start_pid" 2>/dev/null || true
  fi
  stub_teardown
}

cancel_start() {
  "$START" >"$STUB_DIR/start.output" 2>&1 &
  start_pid=$!
  attempts=0
  while [ ! -f "$TRAFFIC_STATE/reached" ]; do
    attempts=$((attempts + 1))
    if [ "$attempts" -gt 150 ]; then
      cat "$STUB_DIR/start.output" >&2
      return 1
    fi
    sleep 0.02
  done
  kill -TERM "$start_pid"
  sleep 0.1
  : >"$TRAFFIC_STATE/release"
  if wait "$start_pid"; then
    status=0
  else
    status=$?
  fi
  start_pid=
  output=$(cat "$STUB_DIR/start.output")
}

@test "cancelling each start phase removes only resources created so far" {
  for phase in before namespace configmap job ready; do
    export CANCEL_AT="$phase"
    rm -f "$TRAFFIC_STATE"/*
    reset_calls
    cancel_start
    [ "$status" -eq 143 ]
    [ ! -f "$TRAFFIC_STATE/namespace" ]
    [ ! -f "$TRAFFIC_STATE/configmap" ]
    [ ! -f "$TRAFFIC_STATE/job" ]
    case "$phase" in
      before) refute_called kubectl "delete" ;;
      namespace) assert_called kubectl "delete namespace traffic-test" ;;
      configmap) assert_called kubectl "delete configmap traffic-profile" ;;
      job | ready) assert_called kubectl "delete job traffic-k6" ;;
    esac
  done
}

@test "cancellation preserves existing namespace and configmap but removes a replacement job" {
  for existing in namespace both; do
    rm -f "$TRAFFIC_STATE"/*
    : >"$TRAFFIC_STATE/namespace"
    : >"$TRAFFIC_STATE/job"
    [ "$existing" != both ] || : >"$TRAFFIC_STATE/configmap"
    reset_calls
    export CANCEL_AT=ready
    cancel_start
    [ "$status" -eq 143 ]
    [ -f "$TRAFFIC_STATE/namespace" ]
    [ ! -f "$TRAFFIC_STATE/job" ]
    refute_called kubectl "delete namespace"
    if [ "$existing" = both ]; then
      [ -f "$TRAFFIC_STATE/configmap" ]
      refute_called kubectl "delete configmap"
    else
      [ ! -f "$TRAFFIC_STATE/configmap" ]
      assert_called kubectl "delete configmap traffic-profile"
    fi
    [ -f "$TRAFFIC_STATE/child-stopped" ]
    assert_called kubectl "--request-timeout=2s"
    assert_called kubectl "--wait=false"
  done
}

@test "successful starts retain the workload and preserve the job settings" {
  export TRAFFIC_PROFILE=spike TRAFFIC_TARGET=http://echo-server/ TRAFFIC_RPS=25
  export TRAFFIC_DURATION=2m TRAFFIC_METHOD=POST K6_IMAGE=k6:test
  export K6_PROMETHEUS_RW_SERVER_URL=http://prometheus/write
  run "$START"
  [ "$status" -eq 0 ]
  [ -f "$TRAFFIC_STATE/namespace" ]
  [ -f "$TRAFFIC_STATE/configmap" ]
  [ -f "$TRAFFIC_STATE/job" ]
  grep -F 'traffic-profile: spike' "$TRAFFIC_STATE/job.yaml"
  grep -F 'image: k6:test' "$TRAFFIC_STATE/job.yaml"
  grep -F 'experimental-prometheus-rw' "$TRAFFIC_STATE/job.yaml"
  for value in http://echo-server/ 25 2m POST http://prometheus/write; do
    grep -F "value: \"$value\"" "$TRAFFIC_STATE/job.yaml"
  done
  reset_calls
  run "$START"
  [ "$status" -eq 0 ]
  assert_call_count kubectl 1 "delete job traffic-k6"
  refute_called kubectl "delete configmap"
  refute_called kubectl "delete namespace"
}

@test "tool failures propagate and roll back newly created resources" {
  for phase in probe namespace-manifest apply-namespace configmap-manifest apply-configmap delete-job apply-job; do
    rm -f "$TRAFFIC_STATE"/*
    reset_calls
    export FAIL_AT="$phase"
    run "$START"
    [ "$status" -ne 0 ]
    [ ! -f "$TRAFFIC_STATE/namespace" ]
    [ ! -f "$TRAFFIC_STATE/configmap" ]
    [ ! -f "$TRAFFIC_STATE/job" ]
  done
}

@test "a readiness failure keeps the existing diagnostic success behavior" {
  export FAIL_AT=ready
  run "$START"
  [ "$status" -eq 0 ]
  [[ "$output" == *"did NOT become Ready"* ]] || false
  [ -f "$TRAFFIC_STATE/job" ]
  refute_called kubectl "delete configmap"
  refute_called kubectl "delete namespace"
}

@test "cleanup failures remain visible without replacing cancellation status" {
  export CANCEL_AT=ready FAIL_AT=delete-configmap
  cancel_start
  [ "$status" -eq 143 ]
  [[ "$output" == *"Could not remove traffic configmap"* ]] || false
  assert_called kubectl "delete namespace traffic-test"
}
