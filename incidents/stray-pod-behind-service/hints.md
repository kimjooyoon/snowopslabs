# Hints: stray-pod-behind-service

## Hint 1
Start from the dashboard. On *Application Request Metrics*, the load
generator's failed-request rate is a steady fraction while the app's own 5xx
rate is flat. Some requests fail somewhere the app's metrics cannot see. What
else is the Service sending traffic to? Compare its backends with the
Deployment's replicas.

## Hint 2
`kubectl get endpoints {{.WorkloadName}} -n {{.WorkloadNamespace}}` should have one
address for each Deployment replica. If it has one more, list the pods with
their labels and ages. One may not look like a pod the Deployment created.

## Hint 3
The extra pod has the Service's labels but no `pod-template-hash`. Describe it
and look for `Controlled By`, then read its logs. Its `/ready` answers 200 even
though its upstream fails, so readiness keeps it in the Service while `/`
returns 5xx. What stops the Service sending it traffic, without touching the
Deployment?
