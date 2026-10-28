#!/bin/sh
# Delete the endpoint that the Service selects but the Deployment does not own.
set -eu
kubectl -n "$WORKLOAD_NAMESPACE" delete pod "${WORKLOAD_NAME}-debug" --ignore-not-found
