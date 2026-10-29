#!/usr/bin/env bash
# Shared helpers for reading a workload's shape in fault scripts and checks.
#
# Sourced, not executed:
#   . "$(cd "$(dirname "$0")/../_lib" && pwd)/workload.sh"

# workload_selector <namespace> <deployment>: print the Deployment's pod
# selector as "key=value,key=value", the form `kubectl get pods -l` takes.
# The Deployment's own selector finds its pods whatever labels the app's chart
# uses; a guessed label misses a bound workload's pods.
workload_selector() {
  kubectl -n "$1" get deploy "$2" \
    -o 'go-template={{range $k, $v := .spec.selector.matchLabels}}{{$k}}={{$v}},{{end}}' 2>/dev/null |
    sed 's/,$//' || true
}

# cpu_millicores <quantity>: print a Kubernetes CPU quantity ("2", "1.5",
# "250m") in millicores, or nothing when it is empty or unreadable.
cpu_millicores() {
  awk -v q="${1:-}" 'BEGIN {
    if (q ~ /^[0-9]+m$/)                { sub(/m$/, "", q); printf "%d", q }
    else if (q ~ /^[0-9]+(\.[0-9]+)?$/) { printf "%d", q * 1000 }
  }'
}

# container_port <namespace> <deployment> <port> - print the number behind a
# probe's port, which is either a number or the name of a container port.
container_port() {
  case "$3" in
    '' | *[!0-9]*)
      kubectl -n "$1" get deploy "$2" \
        -o "jsonpath={.spec.template.spec.containers[0].ports[?(@.name==\"$3\")].containerPort}" 2>/dev/null || true
      ;;
    *) printf '%s' "$3" ;;
  esac
}

# running_pod <namespace> <selector> - print the name of one Running pod that
# is not being deleted. A terminating pod stays Running until it exits, so just
# after a rollout the first Running pod can be an old one on its way out.
running_pod() {
  kubectl -n "$1" get pods -l "$2" --field-selector=status.phase=Running \
    -o 'jsonpath={range .items[*]}{.metadata.name} {.metadata.deletionTimestamp}{"\n"}{end}' 2>/dev/null |
    awk 'NF == 1 { print $1; exit }' || true
}

# pod_get <namespace> <pod> <port> <path> [scheme] - print the body of an HTTP
# GET on <path>, and fail unless the pod answers 2xx. It is asked through the
# API server the way the kubelet asks it directly: on the pod's own IP.
pod_get() {
  target="$2:$3"
  [ "${5:-HTTP}" != HTTPS ] || target="https:$target"
  kubectl get --raw "/api/v1/namespaces/$1/pods/$target/proxy$4" 2>/dev/null
}

# pod_get_ok <namespace> <pod> <port> <path> [scheme] - succeed when the pod
# answers an HTTP GET on <path> with a 2xx.
pod_get_ok() {
  pod_get "$@" >/dev/null
}
