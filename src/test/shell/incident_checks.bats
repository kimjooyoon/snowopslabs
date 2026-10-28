#!/usr/bin/env bats
# SPDX-License-Identifier: Apache-2.0
#
# Detection checks grade a learner's fix. Each one is run against the state of
# a solved lab, the state the fault leaves behind, and the wrong fixes that
# silence the symptom without repairing the cause. A check that passes any
# state but the first tells a learner they are done when they are not.

load 'helpers/stub'

setup() {
  stub_setup
  ROOT="$(project_root)"
  export TARGET_NAMESPACE=shop TARGET_WORKLOAD=cart
  export DOMAIN_SUFFIX=lab.test INGRESS_URL_SUFFIX=lab.test:8081
  stub_script curl <<'STUB'
if [ "${HTTP_CODE:-200}" = "000" ]; then printf '000'; exit 7; fi
printf '%s' "${HTTP_CODE:-200}"
STUB
}

teardown() {
  stub_teardown
}

check() {
  run bash "$ROOT/incidents/$1/checks/resolved.sh"
}

# ── A workload that must be serving: crashloop-bad-config, bad-deploy-rollout ──

# REPLICAS, READY and ROLLED_OUT describe the Deployment the check asks about.
workload_stub() {
  stub_script kubectl <<'STUB'
case "$*" in
  *"rollout status"*) [ "${ROLLED_OUT:-yes}" = yes ] || exit 1 ;;
  *"get deploy"*"jsonpath={.spec.replicas}"*)
    [ "${DEPLOY_EXISTS:-yes}" = yes ] || exit 1
    echo "${REPLICAS:-2}"
    ;;
  *"get deploy"*"jsonpath={.status.readyReplicas}"*) echo "${READY-2}" ;;
  *"get pods"*) echo "cart-7d9f-abcde   false   CrashLoopBackOff   7" ;;
esac
STUB
}

serving_checks() {
  echo crashloop-bad-config
  echo bad-deploy-rollout
}

@test "serving checks pass when every replica is ready after a completed rollout" {
  workload_stub
  for fault in $(serving_checks); do
    check "$fault"
    [ "$status" -eq 0 ] || { echo "$fault: $output"; return 1; }
  done
}

@test "serving checks fail while the fault is in: the rollout has not completed" {
  workload_stub
  for fault in $(serving_checks); do
    ROLLED_OUT=no READY="" check "$fault"
    [ "$status" -ne 0 ] || { echo "$fault passed a stalled rollout"; return 1; }
    [[ "$output" == *"rollout of shop/cart has not completed"* ]] || false
    [[ "$output" == *"CrashLoopBackOff"* ]] || false
  done
}

@test "serving checks fail the wrong fix: scaled to zero" {
  workload_stub
  for fault in $(serving_checks); do
    REPLICAS=0 READY="" check "$fault"
    [ "$status" -ne 0 ] || { echo "$fault passed a workload scaled to zero"; return 1; }
    [[ "$output" == *"scaled to 0 replicas"* ]] || false
  done
}

@test "serving checks fail when fewer replicas are ready than asked for" {
  workload_stub
  for fault in $(serving_checks); do
    REPLICAS=3 READY=1 check "$fault"
    [ "$status" -ne 0 ]
    [[ "$output" == *"1/3 replicas ready"* ]] || false
  done
}

@test "serving checks fail when the workload is gone" {
  workload_stub
  for fault in $(serving_checks); do
    DEPLOY_EXISTS=no check "$fault"
    [ "$status" -ne 0 ]
    [[ "$output" == *"deployment shop/cart not found"* ]] || false
  done
}

# ── oom-kill ──────────────────────────────────────────────────────────────────

# RS lists "<owner>=<pod-template-hash>" for each ReplicaSet scaled above zero;
# TERMINATED is the last-terminated reason of the pods in the serving one.
oom_stub() {
  stub_script kubectl <<'STUB'
case "$*" in
  *"rollout status"*) [ "${ROLLED_OUT:-yes}" = yes ] || exit 1 ;;
  *"get deploy"*"jsonpath={.spec.replicas}"*) echo "${REPLICAS-2}" ;;
  *"get deploy"*"jsonpath={.status.readyReplicas}"*) echo "${READY-2}" ;;
  *"get rs"*) [ -z "${RS-cart=abc123}" ] || printf '%s\n' "${RS-cart=abc123}" ;;
  *"get pods"*"pod-template-hash=abc123"*) printf '%s' "${TERMINATED:-}" ;;
  *"get pods"*) echo "cart-abc123-xyz   false   OOMKilled   4" ;;
esac
STUB
}

@test "oom-kill passes when the serving pods have not been OOMKilled" {
  oom_stub
  check oom-kill
  [ "$status" -eq 0 ]
}

@test "oom-kill fails while the fault is in: the serving pods are OOMKilled" {
  oom_stub
  TERMINATED="OOMKilled OOMKilled" check oom-kill
  [ "$status" -ne 0 ]
  [[ "$output" == *"OOMKilled"* ]] || false
}

@test "oom-kill fails while the rollout is stalled, and shows the pods" {
  oom_stub
  ROLLED_OUT=no check oom-kill
  [ "$status" -ne 0 ]
  [[ "$output" == *"rollout of shop/cart has not completed"* ]] || false
  [[ "$output" == *"cart-abc123-xyz"* ]] || false
}

@test "oom-kill ignores the history of pods outside the serving ReplicaSet" {
  oom_stub
  RS="$(printf 'other=zzz999\ncart=abc123')" check oom-kill
  [ "$status" -eq 0 ]
}

@test "oom-kill fails the wrong fix: scaled to zero, and says so" {
  oom_stub
  REPLICAS=0 READY="" RS="" check oom-kill
  [ "$status" -ne 0 ]
  [[ "$output" == *"scaled to 0 replicas"* ]] || false
}

@test "oom-kill fails with a reason when the serving ReplicaSet cannot be found" {
  oom_stub
  RS="" check oom-kill
  [ "$status" -ne 0 ]
  [[ "$output" == *"cannot find the ReplicaSet serving shop/cart"* ]] || false
}

@test "oom-kill fails when the workload is gone" {
  oom_stub
  REPLICAS="" check oom-kill
  [ "$status" -ne 0 ]
  [[ "$output" == *"deployment shop/cart not found"* ]] || false
}

# ── service-selector-broken ───────────────────────────────────────────────────

selector_stub() {
  stub_script kubectl <<'STUB'
healthy_selector='{"app.kubernetes.io/name":"cart"}'
healthy_labels='{"app.kubernetes.io/name":"cart","tier":"web"}'
case "$*" in
  *"get svc"*"jsonpath={.spec.selector}"*) printf '%s' "${SELECTOR-$healthy_selector}" ;;
  *"get deploy"*"jsonpath={.spec.template.metadata.labels}"*) printf '%s' "${LABELS-$healthy_labels}" ;;
esac
STUB
}

@test "service-selector-broken passes when the Service selects the pod template's labels" {
  selector_stub
  check service-selector-broken
  [ "$status" -eq 0 ]
  [[ "$output" == *"answers 200"* ]] || false
}

@test "service-selector-broken fails while the fault is in, naming the label that differs" {
  selector_stub
  SELECTOR='{"app.kubernetes.io/name":"cart-v2"}' check service-selector-broken
  [ "$status" -ne 0 ]
  [[ "$output" == *'Service wants "cart-v2", pods are labelled "cart"'* ]] || false
}

@test "service-selector-broken fails the wrong fix: a pod relabelled to match, so the URL answers" {
  selector_stub
  SELECTOR='{"app.kubernetes.io/name":"cart-v2"}' HTTP_CODE=200 check service-selector-broken
  [ "$status" -ne 0 ]
}

@test "service-selector-broken fails when one key of several is wrong" {
  selector_stub
  SELECTOR='{"app.kubernetes.io/name":"cart","tier":"api"}' check service-selector-broken
  [ "$status" -ne 0 ]
  [[ "$output" == *'tier: Service wants "api", pods are labelled "web"'* ]] || false
}

@test "service-selector-broken fails the wrong fix: the selector deleted outright" {
  selector_stub
  SELECTOR='{}' check service-selector-broken
  [ "$status" -ne 0 ]
  [[ "$output" == *"no selector at all"* ]] || false
}

@test "service-selector-broken fails when the selector matches but nothing answers" {
  selector_stub
  HTTP_CODE=503 check service-selector-broken
  [ "$status" -ne 0 ]
  [[ "$output" == *"returned 503"* ]] || false
}

# ── stray-pod-behind-service ─────────────────────────────────────────────────

stray_pod_stub() {
  stub_script kubectl <<'STUB'
case "$*" in
  *"rollout status"*) exit 0 ;;
  *"get deploy"*"jsonpath={.spec.replicas}"*) printf '%s' "${REPLICAS-2}" ;;
  *"get deploy"*"jsonpath={.status.readyReplicas}"*) printf '%s' "${READY-2}" ;;
  *"get svc"*"go-template={{range"*) printf '%s' "${SELECTOR-app.kubernetes.io/name=cart}" ;;
  *"get pods"*"custom-columns=POD"*) printf '%s\n' "${PODS-cart-abc123-xyz abc123 ReplicaSet cart-abc123}" ;;
  *"get rs"*) printf '%s' "${RS_OWNER-cart}" ;;
esac
STUB
}

@test "stray-pod-behind-service passes when selected pods belong to the Deployment and / answers" {
  stray_pod_stub
  check stray-pod-behind-service
  [ "$status" -eq 0 ]
  [[ "$output" == *"every Service-selected pod belongs"* ]] || false
}

@test "stray-pod-behind-service fails while an extra selected pod is not Deployment-owned" {
  stray_pod_stub
  PODS='cart-abc123-xyz abc123 ReplicaSet cart-abc123
cart-debug - None -' check stray-pod-behind-service
  [ "$status" -ne 0 ]
  [[ "$output" == *"not owned by deployment cart"* ]] || false
}

@test "stray-pod-behind-service fails the wrong fix: ingress answers but the stray pod remains selected" {
  stray_pod_stub
  PODS='cart-abc123-xyz abc123 ReplicaSet cart-abc123
cart-debug - None -' HTTP_CODE=200 check stray-pod-behind-service
  [ "$status" -ne 0 ]
}

@test "stray-pod-behind-service passes after the stray pod is relabelled away" {
  stray_pod_stub
  PODS='cart-abc123-xyz abc123 ReplicaSet cart-abc123' check stray-pod-behind-service
  [ "$status" -eq 0 ]
}

@test "stray-pod-behind-service fails when its Deployment is scaled to zero" {
  stray_pod_stub
  REPLICAS=0 READY= PODS='' check stray-pod-behind-service
  [ "$status" -ne 0 ]
  [[ "$output" == *"scaled to 0 replicas"* ]] || false
}

# ── network-blackhole ─────────────────────────────────────────────────────────

# POLICIES lists "<name>|<policyTypes>|<ingress rules>" for each NetworkPolicy
# in the workload's namespace.
policy_stub() {
  stub_script kubectl <<'STUB'
case "$*" in
  *"get networkpolicy"*)
    [ "${CLUSTER:-up}" = up ] || exit 1
    [ -z "${POLICIES:-}" ] || printf '%s\n' "$POLICIES"
    ;;
esac
STUB
}

@test "network-blackhole passes when no policy denies ingress and the app answers" {
  policy_stub
  check network-blackhole
  [ "$status" -eq 0 ]
}

@test "network-blackhole fails while the fault is in, naming the policy" {
  policy_stub
  POLICIES='default-deny-ingress|["Ingress"]|' HTTP_CODE=000 check network-blackhole
  [ "$status" -ne 0 ]
  [[ "$output" == *"default-deny-ingress"* ]] || false
}

@test "network-blackhole fails a deny-all policy even while old connections still answer" {
  policy_stub
  POLICIES='default-deny-ingress|["Ingress"]|[]' HTTP_CODE=200 check network-blackhole
  [ "$status" -ne 0 ]
}

@test "network-blackhole passes a policy the learner opened with an allow rule" {
  policy_stub
  POLICIES='default-deny-ingress|["Ingress"]|[{"from":[{"namespaceSelector":{}}]}]' check network-blackhole
  [ "$status" -eq 0 ]
}

@test "network-blackhole leaves another deny baseline with its own allows alone" {
  policy_stub
  POLICIES="$(printf 'default-deny-all|["Ingress","Egress"]|\nallow-ingress-traffic|["Ingress"]|[{"from":[]}]')" \
    check network-blackhole
  [ "$status" -eq 0 ]
}

@test "network-blackhole fails when the policy is gone but the app does not answer" {
  policy_stub
  HTTP_CODE=502 check network-blackhole
  [ "$status" -ne 0 ]
  [[ "$output" == *"returned 502"* ]] || false
}

@test "network-blackhole fails with a reason when the policies cannot be listed" {
  policy_stub
  CLUSTER=down check network-blackhole
  [ "$status" -ne 0 ]
  [[ "$output" == *"could not list the NetworkPolicies in shop"* ]] || false
}

# ── noisy-neighbor ────────────────────────────────────────────────────────────

# LIMITS is one CPU limit per running pod (an empty line for a pod with none).
neighbor_stub() {
  export TARGET_NAMESPACE=labfault-batch TARGET_WORKLOAD=report-batch
  stub_script kubectl <<'STUB'
case "$*" in
  "get deployment"*) [ "${DEPLOY_EXISTS:-yes}" = yes ] || exit 1 ;;
  *"get deploy"*"jsonpath={.spec.replicas}"*) echo "${REPLICAS:-2}" ;;
  *"get rs"*) echo "2026-01-01T00:00:00Z ${REPLICAS:-2} abc123" ;;
  *"get pods"*"resources.limits.cpu"*) printf '%s\n' "${LIMITS-}" ;;
  *"get pods"*"nodeName"*) printf 'node-1' ;;
  *"get node"*) printf '%s' "${NODE_CPU:-4}" ;;
esac
STUB
}

@test "noisy-neighbor passes when the batch workload is gone or scaled to zero" {
  neighbor_stub
  DEPLOY_EXISTS=no check noisy-neighbor
  [ "$status" -eq 0 ]
  REPLICAS=0 check noisy-neighbor
  [ "$status" -eq 0 ]
}

@test "noisy-neighbor fails while the fault is in: the pods carry no CPU limit" {
  neighbor_stub
  LIMITS="" check noisy-neighbor
  [ "$status" -ne 0 ]
  [[ "$output" == *"only 0 of 2 pod(s)"* ]] || false
}

@test "noisy-neighbor fails when only some pods are capped" {
  neighbor_stub
  LIMITS="$(printf '500m\n\n')" check noisy-neighbor
  [ "$status" -ne 0 ]
  [[ "$output" == *"only 1 of 2 pod(s)"* ]] || false
}

@test "noisy-neighbor fails the wrong fix: a limit too large to bound anything" {
  neighbor_stub
  LIMITS="$(printf '4\n4')" check noisy-neighbor
  [ "$status" -ne 0 ]
  [[ "$output" == *"too big to be one"* ]] || false
}

@test "noisy-neighbor passes when every pod is capped within half the node" {
  neighbor_stub
  LIMITS="$(printf '500m\n500m')" check noisy-neighbor
  [ "$status" -eq 0 ]
  [[ "$output" == *"capped at 500m, 1000m in total on a 4000m node"* ]] || false
}

# ── pending-unschedulable ─────────────────────────────────────────────────────

# PENDING lists the workload's Pending pods; PODS=fail makes listing them fail.
pending_stub() {
  stub_script kubectl <<'STUB'
case "$*" in
  *"go-template"*) printf '%s' "${SELECTOR-app.kubernetes.io/name=cart,tier=web,}" ;;
  *"get pods -l app.kubernetes.io/name=cart,tier=web --field-selector=status.phase=Pending,spec.nodeName= "*)
    [ "${PODS:-ok}" = ok ] || exit 1
    [ -z "${PENDING:-}" ] || printf '%s\n' $PENDING
    ;;
  *"rollout status"*) [ "${ROLLED_OUT:-yes}" = yes ] || exit 1 ;;
  *"get deploy"*"jsonpath={.spec.replicas}"*)
    [ "${DEPLOY_EXISTS:-yes}" = yes ] || exit 1
    echo "${REPLICAS:-4}"
    ;;
  *"get deploy"*"jsonpath={.status.readyReplicas}"*) echo "${READY-4}" ;;
  *"get pods"*) echo "cart-7d9f-abcde   true   <none>   0" ;;
esac
STUB
}

@test "pending-unschedulable passes when every replica is scheduled and ready" {
  pending_stub
  check pending-unschedulable
  [ "$status" -eq 0 ]
  [[ "$output" == *"every replica of shop/cart is scheduled and ready"* ]] || false
}

@test "pending-unschedulable fails while the fault is in: replicas are Pending" {
  pending_stub
  PENDING="pod/cart-new-1 pod/cart-new-2 pod/cart-new-3" ROLLED_OUT=no READY=2 check pending-unschedulable
  [ "$status" -ne 0 ]
  [[ "$output" == *"3 pod(s) of shop/cart are Pending"* ]] || false
}

@test "pending-unschedulable fails the wrong fix: scaled back to the old count, one new pod still Pending" {
  pending_stub
  PENDING="pod/cart-new-1" REPLICAS=2 READY=2 ROLLED_OUT=no check pending-unschedulable
  [ "$status" -ne 0 ]
  [[ "$output" == *"1 pod(s) of shop/cart are Pending"* ]] || false
}

@test "pending-unschedulable fails while recreated pods have not finished rolling out" {
  pending_stub
  ROLLED_OUT=no READY=2 check pending-unschedulable
  [ "$status" -ne 0 ]
  [[ "$output" == *"rollout of shop/cart has not completed"* ]] || false
}

@test "pending-unschedulable fails the wrong fix: scaled to zero" {
  pending_stub
  REPLICAS=0 check pending-unschedulable
  [ "$status" -ne 0 ]
  [[ "$output" == *"scaled to 0 replicas"* ]] || false
}

@test "pending-unschedulable fails with a reason when the pods cannot be listed" {
  pending_stub
  PODS=fail check pending-unschedulable
  [ "$status" -ne 0 ]
  [[ "$output" == *"could not list the pods of shop/cart"* ]] || false
}

@test "pending-unschedulable fails with a reason when the selector cannot be read" {
  pending_stub
  SELECTOR="" check pending-unschedulable
  [ "$status" -ne 0 ]
  [[ "$output" == *"cannot read the pod selector of shop/cart"* ]] || false
}

@test "pending-unschedulable fails when the workload is gone" {
  pending_stub
  DEPLOY_EXISTS=no check pending-unschedulable
  [ "$status" -ne 0 ]
  [[ "$output" == *"deployment shop/cart not found"* ]] || false
}

# ── liveness-probe-kill ───────────────────────────────────────────────────────

# PROBE is the container's liveness probe as JSON; PROBE_PATH, PORT and SCHEME
# are its httpGet fields; GET=fail makes the pod answer the path with an error.
liveness_stub() {
  stub_script kubectl <<'STUB'
default_probe='{"httpGet":{}}'
case "$*" in
  *"livenessProbe.httpGet.path}"*) printf '%s' "${PROBE_PATH-/health}" ;;
  *"livenessProbe.httpGet.port}"*) printf '%s' "${PORT-http}" ;;
  *"livenessProbe.httpGet.scheme}"*) printf '%s' "${SCHEME-HTTP}" ;;
  *"livenessProbe}"*) printf '%s' "${PROBE-$default_probe}" ;;
  *'ports[?(@.name=="http")].containerPort}'*) printf '8080' ;;
  *"go-template"*) printf 'app.kubernetes.io/name=cart,' ;;
  *"get pods"*"status.phase=Running"*) printf '%s' "${RUNNING-cart-abc}" ;;
  *"get --raw /api/v1/namespaces/shop/pods/"*) [ "${GET:-ok}" = ok ] || exit 1 ;;
  *"rollout status"*) [ "${ROLLED_OUT:-yes}" = yes ] || exit 1 ;;
  *"get deploy"*"jsonpath={.spec.replicas}"*)
    [ "${DEPLOY_EXISTS:-yes}" = yes ] || exit 1
    echo "${REPLICAS:-1}"
    ;;
  *"get deploy"*"jsonpath={.status.readyReplicas}"*) echo "${READY-1}" ;;
  *"get pods"*) echo "cart-abc   false   <none>   4" ;;
esac
STUB
}

@test "liveness-probe-kill passes when the probe's path answers and every replica is ready" {
  liveness_stub
  check liveness-probe-kill
  [ "$status" -eq 0 ]
  assert_called kubectl "get --raw /api/v1/namespaces/shop/pods/cart-abc:8080/proxy/health"
}

@test "liveness-probe-kill fails while the fault is in: the probe's path does not answer" {
  liveness_stub
  PROBE_PATH=/healthz GET=fail check liveness-probe-kill
  [ "$status" -ne 0 ]
  [[ "$output" == *"GET /healthz on port 8080) does not succeed on cart-abc"* ]] || false
}

@test "liveness-probe-kill fails the wrong fix: a longer delay on a path that still 404s" {
  liveness_stub
  PROBE='{"httpGet":{"path":"/healthz"},"initialDelaySeconds":600}' PROBE_PATH=/healthz GET=fail \
    check liveness-probe-kill
  [ "$status" -ne 0 ]
}

@test "liveness-probe-kill fails the wrong fix: the probe deleted" {
  liveness_stub
  PROBE="" PROBE_PATH="" check liveness-probe-kill
  [ "$status" -ne 0 ]
  [[ "$output" == *"has no liveness probe"* ]] || false
}

@test "liveness-probe-kill passes a probe that is not HTTP" {
  liveness_stub
  PROBE='{"tcpSocket":{"port":8080}}' PROBE_PATH="" check liveness-probe-kill
  [ "$status" -eq 0 ]
  refute_called kubectl "get --raw"
}

@test "liveness-probe-kill asks a numbered port and an HTTPS probe the way the kubelet does" {
  liveness_stub
  PORT=9443 SCHEME=HTTPS check liveness-probe-kill
  [ "$status" -eq 0 ]
  assert_called kubectl "get --raw /api/v1/namespaces/shop/pods/https:cart-abc:9443/proxy/health"
}

@test "liveness-probe-kill fails with a reason when the probe names an undeclared port" {
  liveness_stub
  PORT=metrics check liveness-probe-kill
  [ "$status" -ne 0 ]
  [[ "$output" == *"names a port the container does not declare"* ]] || false
}

@test "liveness-probe-kill fails with a reason when no pod is running to try the probe on" {
  liveness_stub
  RUNNING="" check liveness-probe-kill
  [ "$status" -ne 0 ]
  [[ "$output" == *"no pod of shop/cart is running"* ]] || false
}

@test "liveness-probe-kill fails while the rollout has not completed" {
  liveness_stub
  ROLLED_OUT=no READY=0 check liveness-probe-kill
  [ "$status" -ne 0 ]
  [[ "$output" == *"rollout of shop/cart has not completed"* ]] || false
}

@test "liveness-probe-kill fails the wrong fix: scaled to zero" {
  liveness_stub
  REPLICAS=0 check liveness-probe-kill
  [ "$status" -ne 0 ]
  [[ "$output" == *"scaled to 0 replicas"* ]] || false
}

@test "liveness-probe-kill fails when the workload is gone" {
  liveness_stub
  DEPLOY_EXISTS=no check liveness-probe-kill
  [ "$status" -ne 0 ]
  [[ "$output" == *"deployment shop/cart not found"* ]] || false
}

# ── missing-secret-key ────────────────────────────────────────────────────────

# REFS is "<env>|<secret>|<key>" per variable, KEYS the Secret's keys, and
# RECORDED the variable the fault's record names.
secret_stub() {
  stub_script kubectl <<'STUB'
default_refs="$(printf 'PORT||\nAPI_TOKEN|cart-secret|api-token')"
case "$*" in
  *"get configmap labfault-missing-secret-key"*"data.env}"*) printf '%s' "${RECORDED-}" ;;
  *"secretKeyRef.key}"*) [ "${ENV_READ:-ok}" = ok ] || exit 1; printf '%s\n' "${REFS-$default_refs}" ;;
  *"get secret cart-secret"*"go-template"*) printf '%s\n' "${KEYS-api-token}" ;;
  *"rollout status"*) [ "${ROLLED_OUT:-yes}" = yes ] || exit 1 ;;
  *"get deploy"*"jsonpath={.spec.replicas}"*)
    [ "${DEPLOY_EXISTS:-yes}" = yes ] || exit 1
    echo "${REPLICAS:-1}"
    ;;
  *"get deploy"*"jsonpath={.status.readyReplicas}"*) echo "${READY-1}" ;;
  *"get pods"*) echo "cart-abc   false   CreateContainerConfigError   0" ;;
esac
STUB
}

@test "missing-secret-key passes when every Secret key it reads exists and the replicas are ready" {
  secret_stub
  RECORDED=API_TOKEN check missing-secret-key
  [ "$status" -eq 0 ]
}

@test "missing-secret-key fails while the fault is in, showing only what get pods shows" {
  secret_stub
  RECORDED=API_TOKEN KEYS=API_TOKEN ROLLED_OUT=no READY=0 check missing-secret-key
  [ "$status" -ne 0 ]
  [[ "$output" == *"rollout of shop/cart has not completed"* ]] || false
  [[ "$output" == *"CreateContainerConfigError"* ]] || false
  [[ "$output" != *"api-token"* ]] || false
}

@test "missing-secret-key passes when the reference names the key the Secret has now" {
  secret_stub
  RECORDED=API_TOKEN REFS="API_TOKEN|cart-secret|API_TOKEN" KEYS=API_TOKEN check missing-secret-key
  [ "$status" -eq 0 ]
}

@test "missing-secret-key fails the wrong fix: the reference marked optional, pods up without the key" {
  secret_stub
  RECORDED=API_TOKEN KEYS=API_TOKEN check missing-secret-key
  [ "$status" -ne 0 ]
  [[ "$output" == *'API_TOKEN reads key "api-token" of Secret cart-secret, which does not have it'* ]] || false
}

@test "missing-secret-key fails the wrong fix: the variable removed" {
  secret_stub
  RECORDED=API_TOKEN REFS="PORT||" KEYS=API_TOKEN check missing-secret-key
  [ "$status" -ne 0 ]
  [[ "$output" == *"API_TOKEN no longer comes from a Secret"* ]] || false
}

@test "missing-secret-key fails the wrong fix: the value hardcoded in the pod template" {
  secret_stub
  RECORDED=API_TOKEN REFS="$(printf 'PORT||\nAPI_TOKEN||')" KEYS=API_TOKEN check missing-secret-key
  [ "$status" -ne 0 ]
  [[ "$output" == *"API_TOKEN no longer comes from a Secret"* ]] || false
}

@test "missing-secret-key passes a healthy lab with no record" {
  secret_stub
  check missing-secret-key
  [ "$status" -eq 0 ]
}

@test "missing-secret-key fails with a reason when the environment cannot be read" {
  secret_stub
  ENV_READ=fail check missing-secret-key
  [ "$status" -ne 0 ]
  [[ "$output" == *"could not read the environment of shop/cart"* ]] || false
}

@test "missing-secret-key fails the wrong fix: scaled to zero" {
  secret_stub
  REPLICAS=0 check missing-secret-key
  [ "$status" -ne 0 ]
  [[ "$output" == *"scaled to 0 replicas"* ]] || false
}

@test "missing-secret-key fails when the workload is gone" {
  secret_stub
  DEPLOY_EXISTS=no check missing-secret-key
  [ "$status" -ne 0 ]
  [[ "$output" == *"deployment shop/cart not found"* ]] || false
}

# ── cpu-throttling ────────────────────────────────────────────────────────────

# LIMIT is the container's CPU limit; DEMAND and ORIGINAL come from the record.
throttle_stub() {
  stub_script kubectl <<'STUB'
case "$*" in
  *"get configmap labfault-cpu-throttling"*"demand-millicores}"*) printf '%s' "${DEMAND-40}" ;;
  *"get configmap labfault-cpu-throttling"*"original-limit}"*) printf '%s' "${ORIGINAL-200m}" ;;
  *"resources.limits.cpu}"*) printf '%s' "${LIMIT-200m}" ;;
  *"rollout status"*) [ "${ROLLED_OUT:-yes}" = yes ] || exit 1 ;;
  *"get deploy"*"jsonpath={.spec.replicas}"*)
    [ "${DEPLOY_EXISTS:-yes}" = yes ] || exit 1
    echo "${REPLICAS:-1}"
    ;;
  *"get deploy"*"jsonpath={.status.readyReplicas}"*) echo "${READY-${REPLICAS:-1}}" ;;
  *"get pods"*) echo "cart-abc   false   <none>   0" ;;
esac
STUB
}

@test "cpu-throttling passes when the limit has headroom over the measured demand" {
  throttle_stub
  check cpu-throttling
  [ "$status" -eq 0 ]
  LIMIT=80m check cpu-throttling
  [ "$status" -eq 0 ]
}

@test "cpu-throttling fails while the fault is in: the limit is a third of the demand" {
  throttle_stub
  LIMIT=13m check cpu-throttling
  [ "$status" -ne 0 ]
  [[ "$output" == *"not sized for the load it serves"* ]] || false
}

@test "cpu-throttling fails a half fix: a limit below twice the demand" {
  throttle_stub
  LIMIT=79m check cpu-throttling
  [ "$status" -ne 0 ]
}

@test "cpu-throttling fails the wrong fix: scaled out under the same low limit" {
  throttle_stub
  LIMIT=13m REPLICAS=4 check cpu-throttling
  [ "$status" -ne 0 ]
}

@test "cpu-throttling passes with the limit removed" {
  throttle_stub
  LIMIT="" check cpu-throttling
  [ "$status" -eq 0 ]
}

@test "cpu-throttling passes a rollback to an original limit below the usual headroom" {
  throttle_stub
  DEMAND=40 ORIGINAL=60m LIMIT=60m check cpu-throttling
  [ "$status" -eq 0 ]
}

@test "cpu-throttling passes a healthy lab with no record" {
  throttle_stub
  DEMAND="" ORIGINAL="" LIMIT=13m check cpu-throttling
  [ "$status" -eq 0 ]
}

@test "cpu-throttling fails while the rollout has not completed" {
  throttle_stub
  ROLLED_OUT=no READY=0 check cpu-throttling
  [ "$status" -ne 0 ]
  [[ "$output" == *"rollout of shop/cart has not completed"* ]] || false
}

@test "cpu-throttling fails the wrong fix: scaled to zero" {
  throttle_stub
  REPLICAS=0 check cpu-throttling
  [ "$status" -ne 0 ]
  [[ "$output" == *"scaled to 0 replicas"* ]] || false
}

@test "cpu-throttling fails when the workload is gone" {
  throttle_stub
  DEPLOY_EXISTS=no check cpu-throttling
  [ "$status" -ne 0 ]
  [[ "$output" == *"deployment shop/cart not found"* ]] || false
}

# ── dns-egress-blocked ────────────────────────────────────────────────────────

# POLICIES is "<name>|<policyTypes>|<egress rules>" per policy; UPSTREAM the
# app's UPSTREAM_URL; RECORDED the upstream the fault's record names.
egress_stub() {
  stub_script kubectl <<'STUB'
url=http://echo-server.echo-server.svc.cluster.local:8080/
baseline='default-deny-egress|["Egress"]|[{"ports":[{"port":8080,"protocol":"TCP"}]},{"ports":[{"port":53,"protocol":"UDP"}]}]'
case "$*" in
  *"get configmap labfault-dns-egress-blocked"*"data.upstream}"*) printf '%s' "${RECORDED-$url}" ;;
  *'env[?(@.name=="UPSTREAM_URL")].value}'*) printf '%s' "${UPSTREAM-$url}" ;;
  *"get networkpolicy"*)
    [ "${CLUSTER:-up}" = up ] || exit 1
    [ -z "${POLICIES-$baseline}" ] || printf '%s\n' "${POLICIES-$baseline}"
    ;;
  *"go-template"*) printf 'app.kubernetes.io/name=cart,' ;;
  *"get pods"*"status.phase=Running"*) printf '%s' "${RUNNING-cart-abc}" ;;
  *"containers[0].ports[0].containerPort}"*) printf '8080' ;;
  *"get --raw /api/v1/namespaces/shop/pods/cart-abc:8080/proxy/"*)
    [ "${GET:-ok}" = ok ] || exit 1
    printf '%s' "${BODY-{\"service\":\"cart\",\"upstreamStatus\":200\}}"
    ;;
  *"rollout status"*) [ "${ROLLED_OUT:-yes}" = yes ] || exit 1 ;;
  *"get deploy"*"jsonpath={.spec.replicas}"*)
    [ "${DEPLOY_EXISTS:-yes}" = yes ] || exit 1
    echo "${REPLICAS:-1}"
    ;;
  *"get deploy"*"jsonpath={.status.readyReplicas}"*) echo "${READY-1}" ;;
  *"get pods"*) echo "cart-abc   true   <none>   0" ;;
esac
STUB
}

# An image older than UPSTREAM_URL ignores it and answers 200 without calling
# anything, which is a lab where the fault never happened, not a fix.
@test "dns-egress-blocked fails an app that answers without calling its upstream" {
  egress_stub
  BODY='{"service":"cart"}' check dns-egress-blocked
  [ "$status" -ne 0 ]
  [[ "$output" == *"answers without calling its upstream"* ]] || false
  [[ "$output" == *"labctl app build cart"* ]] || false
}

@test "dns-egress-blocked passes when the app reaches its upstream by name behind an egress baseline" {
  egress_stub
  check dns-egress-blocked
  [ "$status" -eq 0 ]
}

@test "dns-egress-blocked fails while the fault is in: a request that calls the upstream fails" {
  egress_stub
  GET=fail check dns-egress-blocked
  [ "$status" -ne 0 ]
  [[ "$output" == *"calls its upstream still fails on cart-abc"* ]] || false
}

@test "dns-egress-blocked passes a DNS rule in a policy of its own" {
  egress_stub
  POLICIES="$(printf 'default-deny-egress|["Egress"]|[{"ports":[{"port":8080}]}]\nallow-dns|["Egress"]|[{"ports":[{"port":53,"protocol":"UDP"}],"to":[{"namespaceSelector":{}}]}]')" \
    check dns-egress-blocked
  [ "$status" -eq 0 ]
}

@test "dns-egress-blocked fails the wrong fix: the egress policy deleted" {
  egress_stub
  POLICIES="" check dns-egress-blocked
  [ "$status" -ne 0 ]
  [[ "$output" == *"nothing restricts egress from shop any more"* ]] || false
}

@test "dns-egress-blocked fails the wrong fix: a rule that allows all egress" {
  egress_stub
  POLICIES='default-deny-egress|["Egress"]|[{"ports":[{"port":8080}]},{}]' check dns-egress-blocked
  [ "$status" -ne 0 ]
  POLICIES="$(printf 'default-deny-egress|["Egress"]|[{"ports":[{"port":8080}]}]\nallow-all|["Egress"]|[{}]')" check dns-egress-blocked
  [ "$status" -ne 0 ]
}

@test "dns-egress-blocked fails the wrong fix: the upstream addressed by IP" {
  egress_stub
  UPSTREAM=http://10.43.207.135:8080/ check dns-egress-blocked
  [ "$status" -ne 0 ]
  [[ "$output" == *"no longer calls its upstream at http://echo-server"* ]] || false
}

@test "dns-egress-blocked fails the wrong fix: the upstream call switched off" {
  egress_stub
  UPSTREAM="" check dns-egress-blocked
  [ "$status" -ne 0 ]
  [[ "$output" == *'UPSTREAM_URL is ""'* ]] || false
}

@test "dns-egress-blocked passes a healthy lab with no record and no policy" {
  egress_stub
  RECORDED="" UPSTREAM="" POLICIES="" check dns-egress-blocked
  [ "$status" -eq 0 ]
}

@test "dns-egress-blocked fails with a reason when the policies cannot be listed" {
  egress_stub
  CLUSTER=down check dns-egress-blocked
  [ "$status" -ne 0 ]
  [[ "$output" == *"could not list the NetworkPolicies in shop"* ]] || false
}

@test "dns-egress-blocked fails with a reason when no pod is running" {
  egress_stub
  RUNNING="" check dns-egress-blocked
  [ "$status" -ne 0 ]
  [[ "$output" == *"no pod of shop/cart is running"* ]] || false
}

@test "dns-egress-blocked fails while the rollout has not completed, or scaled to zero, or gone" {
  egress_stub
  ROLLED_OUT=no READY=0 check dns-egress-blocked
  [[ "$output" == *"rollout of shop/cart has not completed"* ]] || false
  REPLICAS=0 check dns-egress-blocked
  [[ "$output" == *"scaled to 0 replicas"* ]] || false
  DEPLOY_EXISTS=no check dns-egress-blocked
  [ "$status" -ne 0 ]
  [[ "$output" == *"deployment shop/cart not found"* ]] || false
}
