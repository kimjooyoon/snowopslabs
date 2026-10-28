#!/usr/bin/env bash
# Passes only when the Service selects Deployment-owned pods and `/` succeeds
# through ingress. Endpoint membership alone is not enough because a stray pod
# can be healthy on probes while returning errors to real requests.
set -euo pipefail

NS="${TARGET_NAMESPACE:-go-api}"
SVC="${TARGET_WORKLOAD:-go-api}"
DEPLOY="${TARGET_WORKLOAD:-go-api}"
SUFFIX="${INGRESS_URL_SUFFIX:-${DOMAIN_SUFFIX:-${CLUSTER_NAME:-snowops}.localhost}}"
LIB="$(cd "$(dirname "$0")/../../_lib" && pwd)"
# shellcheck source=/dev/null
. "$LIB/assert.sh"

assert_workload_healthy "$NS" "$DEPLOY"

SELECTOR="$(kubectl -n "$NS" get svc "$SVC" \
  -o 'go-template={{range $k, $v := .spec.selector}}{{$k}}={{$v}},{{end}}' 2>/dev/null |
  sed 's/,$//' || true)"
if [ -z "$SELECTOR" ]; then
  echo "FAIL: Service $NS/$SVC has no selector, so it cannot provide the expected endpoints." >&2
  exit 1
fi

PODS="$(kubectl -n "$NS" get pods -l "$SELECTOR" \
  --field-selector=status.phase=Running --no-headers \
  -o 'custom-columns=POD:.metadata.name,HASH:.metadata.labels.pod-template-hash,OWNER_KIND:.metadata.ownerReferences[0].kind,OWNER_NAME:.metadata.ownerReferences[0].name' 2>/dev/null || true)"
if [ -z "$PODS" ]; then
  echo "FAIL: Service $NS/$SVC selects no Running pods." >&2
  exit 1
fi

BAD=""
while read -r pod hash owner_kind owner_name _; do
  [ -n "${pod:-}" ] || continue
  case "$owner_kind:$hash" in
    ReplicaSet:*)
      rs_owner="$(kubectl -n "$NS" get rs "$owner_name" \
        -o 'go-template={{range .metadata.ownerReferences}}{{if eq .kind "Deployment"}}{{.name}}{{end}}{{end}}' \
        2>/dev/null || true)"
      [ "$rs_owner" = "$DEPLOY" ] || BAD="$BAD $pod"
      ;;
    *) BAD="$BAD $pod" ;;
  esac
done <<EOF
$PODS
EOF

if [ -n "$BAD" ]; then
  echo "FAIL: Service $NS/$SVC selects pod(s) not owned by deployment $DEPLOY:$BAD" >&2
  echo "  Check endpoints, pod-template-hash labels and owner references." >&2
  exit 1
fi

CODE="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "http://${SVC}.${SUFFIX}/" || true)"
if [ "$CODE" != "200" ]; then
  echo "FAIL: http://${SVC}.${SUFFIX}/ returned ${CODE}." >&2
  exit 1
fi

echo "OK: every Service-selected pod belongs to deployment $DEPLOY, and / answers 200 through ingress."
