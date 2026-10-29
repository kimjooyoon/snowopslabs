# Platform

Platform components are the infrastructure the lab runs on: ingress,
monitoring, logging, tracing, mesh, data, secrets, security, autoscaling, cost,
gitops, chaos and the dashboard. Each lives in
`platform/<category>/<component>/` with a single `values.yaml` and a chart
version pinned in `config/versions.env`.

A **target** is either a category (whose provider comes from an env var, or is
the only one) or an explicit `category/provider` for additive categories such
as `data`.

```bash
labctl platform up                          # the full stack
MESH_PROVIDER=istio labctl platform up mesh # a category - provider from the env var
labctl platform up data/kafka               # a specific provider (additive category)
labctl platform up data/postgres
```

```bash
labctl platform down                        # uninstall everything
MESH_PROVIDER=istio labctl platform down mesh
labctl platform down data/kafka
```

`platform down` with no target tries every component even when one fails, then
exits non-zero and lists what it could not remove. Uninstalls are safe to
repeat.

```bash
labctl platform status                      # what the cluster holds, with labctl's last run
labctl platform status --live               # run each provider's status.sh
labctl platform status mesh
labctl platform status data/postgres
```

`status` lists every component the cluster holds, however it got there:
`platform up`, `scenario up --deploy-prereqs` or a manual `helm install`. A
monitoring, logging or tracing provider counts when its Helm release is
deployed, since they share a namespace; any other provider counts when its
namespace exists. LAST RUN is labctl's last install or uninstall of the
component. When that run failed, the state is `error`, even if the cluster
still holds what the run left behind. When the cluster does not answer, the
table comes from the run history and the command says so.

`status --live` needs the cluster: when it does not answer, the command says so
instead of printing an empty table.

Which components install is driven by the provider variables in `.env`:
`INGRESS_PROVIDER`, `METRICS_PROVIDER`, `LOGGING_PROVIDER`, `TRACING_PROVIDER`,
`MESH_PROVIDER` and so on. When a category has several providers and none is
selected, the command lists the choices and names the variable to set. An
environment variable of the same name wins over `.env`
([which value wins](index.md#which-value-wins)).

## `labctl platform teardown`

Removes exactly the components the inventory records as installed, in reverse
order, and reports anything it could not remove instead of exiting 0 and hoping.
Each uninstall is a recorded, cancellable run. The inventory holds what
`platform up` and `--deploy-prereqs` installed; components installed outside
labctl are not in it and are left alone.

```bash
labctl platform teardown
```

REST: `GET /api/v2/platform`, `POST /api/v2/platform/{up,down}`,
`GET /api/v2/platform/component/{category}/{name}`.

Adding a component, including the CRD and StatefulSet traps that will bite you,
 is [R05](../../runbooks/R05-platform-components.md).
