# Solution: stray-pod-behind-service

## What happened

Someone started a bare pod while debugging {{.WorkloadName}} and copied the
Deployment's pod labels onto it. The Service selects labels, not controller
ownership, so the extra pod became an endpoint. Its `UPSTREAM_URL` points at a
host that does not exist. The pod is Running and Ready because `/health` and
`/ready` answer, but every `/` request routed to it returns 502. With the
steady traffic profile, the error budget burns at roughly one request in
`replicas + 1`.

This is different from `service-selector-broken`: that incident has **zero**
endpoints because the Service selector matches no pods. Here the endpoints list
is populated and has **one too many** addresses. It is also different from
`dns-egress-blocked`: that fault makes every request fail because every replica
shares the blocked dependency. Here only the stray endpoint fails. A bad pod
during a rollout, or one node with a broken network path, can produce a similar
fraction, so confirm the pod's labels, age, owner and logs before deleting it.

## Diagnosis path

```bash
kubectl get endpoints {{.WorkloadName}} -n {{.WorkloadNamespace}}
kubectl get deploy {{.WorkloadName}} -n {{.WorkloadNamespace}}
kubectl get pods -n {{.WorkloadNamespace}} --show-labels
kubectl describe pod -n {{.WorkloadNamespace}} <extra-pod>
kubectl logs -n {{.WorkloadNamespace}} <extra-pod>
```

The extra endpoint is a pod without the Deployment's `pod-template-hash` and
without `Controlled By`. Its logs contain the upstream connection error.

On *Application Request Metrics*, the load generator's failed-request rate is a
steady fraction while the app's own 5xx rate stays flat. Prometheus scrapes
pods that carry the `prometheus.io/scrape` annotation, which the Deployment's
pod template sets and a hand-run pod does not, so the failing pod's requests
never reach the app's metrics. Client-side and app-side numbers that disagree
mean some traffic goes somewhere the app is not measuring. The page is
`LabFaultClientErrorRate`.

## Fix

Delete the pod that is selected by the Service but is not part of the
Deployment:

```bash
kubectl -n {{.WorkloadNamespace}} delete pod <extra-pod>
```

Relabelling it so the Service no longer selects it is also a valid outcome:

```bash
kubectl -n {{.WorkloadNamespace}} label pod <extra-pod> app.kubernetes.io/name=debug-only --overwrite
```

Labelling it with a `pod-template-hash` that one of the Deployment's
ReplicaSets selects also works, indirectly: that ReplicaSet adopts the pod and,
being over its replica count, deletes it. Deleting it yourself is clearer.

The grader checks the outcome, not which of those commands you used: every
Service-selected pod must be owned through the Deployment's ReplicaSet, and a
request to `/` through the ingress must return 200.

For a fresh lab, the same fault can be reproduced with plain kubectl:

```bash
kubectl -n {{.WorkloadNamespace}} run {{.WorkloadName}}-debug \
  --image="$(kubectl -n {{.WorkloadNamespace}} get deploy {{.WorkloadName}} -o jsonpath='{.spec.template.spec.containers[0].image}')" \
  --image-pull-policy=IfNotPresent --restart=Never \
  --labels="$(kubectl -n {{.WorkloadNamespace}} get deploy {{.WorkloadName}} -o 'go-template={{range $k, $v := .spec.template.metadata.labels}}{{$k}}={{$v}},{{end}}' | sed 's/,$//')" \
  --env=UPSTREAM_URL=http://no-such-upstream.invalid:8080 \
  --override-type=strategic \
  --overrides='{"spec":{"containers":[{"name":"{{.WorkloadName}}-debug","ports":[{"name":"http","containerPort":8080}]}]}}'
```

The port needs the name the Service's `targetPort` uses (`http` here): an
unnamed port leaves the pod out of the Service's endpoints, and it gets no
traffic at all.

## Real-world parallel

Services do not know which controller created a labelled pod. A hand-run
debugger pod, a stale ReplicaSet, or a copied manifest can therefore receive
production traffic. The safe check is the relationship between endpoint count,
Deployment ownership and request outcomes, not the Ready column alone. A
rollout restart replaces Deployment pods but leaves this bare pod untouched,
so it is a tempting wrong fix.
