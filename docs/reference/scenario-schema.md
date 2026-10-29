# Scenario schema

The complete `scenario.yaml` reference. A scenario is a directory under
`scenarios/` containing this file; the engine auto-discovers any directory with
a valid one. Schema validation runs in CI, so a malformed scenario cannot merge.

For a guided walkthrough instead of a reference, read
[your first scenario](../authoring/first-scenario.md).

## Skeleton

```yaml
apiVersion: scenario.snowops.net/v2   # optional; defaults to v2
name: my-scenario                     # must match the directory name
displayName: "My Scenario"            # shown in the UI and CLI
description: "What this scenario teaches"
category: observability               # grouping label

prerequisites:
  platform: [ingress, monitoring/metrics]
  apps: [go-api]
  capabilities: [prometheus-metrics]   # what the bound app must promise

runtimes: [k3d, kind]                 # optional; compatible runtimes

objectives:                           # human-readable goals
  - "Aggregate application logs in Loki"
  - "Keep p99 latency under 300ms"

stages:                               # ordered groups of components
  - name: baseline
    description: Install the baseline stack
    components: [...]

checks: [...]                         # machine-verifiable assertions

explore:
  urls:
    - label: "My Dashboard"
      url: "http://my-app.{{.IngressURLSuffix}}"
  commands:
    - label: "Check status"
      command: "kubectl get pods -n my-ns"
  tips:
    - "Generate traffic first, or the dashboards stay empty"
```

`prerequisites.platform` lists everything the scenario's own parts need, so
`scenario up --deploy-prereqs` works on a bare cluster: `ingress` for a
`promql` or `http` check (both read through the ingress), `monitoring/metrics`
for a `promql` check, and a `monitoring/...` component for a
`grafana-dashboard` component (its ConfigMap goes in the monitoring
namespace). `TestRepoScenarios_DeclareWhatTheyUse` enforces it.

They install in the order listed. `monitoring/metrics` comes before any
component whose values turn on a `serviceMonitor`: the chart creates it only
when the Prometheus CRDs already exist
(`TestRepoScenarios_MetricsBeforeWhatTheyScrape`).

## Components

A component is one installable thing. Use **either** a top-level `components:`
list **or** `stages:`, never both. Stages are preferred: they group components
into phases you can reason about (`baseline`, `inject-failure`) and install in
declaration order.

```yaml
components:
  - name: loki
    type: helm                        # helm | manifest | grafana-dashboard | script
    chart: grafana-community/loki
    repo: https://grafana-community.github.io/helm-charts
    version: "18.12.0"                # always pinned - see config/versions.env
    namespace: "{{.MonitoringNamespace}}"
    platformValues: logging/loki      # base values, owned by the platform
    valuesFile: values/loki.yaml      # overlay, applied on top
    adopt: true                       # reuse an existing release, do not upgrade
    set:
      key: value                      # --set overrides
```

| Field | Applies to | Meaning |
|---|---|---|
| `name` | all | Unique, non-empty within the scenario |
| `type` | all | `helm`, `manifest`, `grafana-dashboard` or `script` |
| `chart` / `repo` / `version` | `helm` | Chart, repo URL, and the **required** pinned version |
| `namespace` | all | Target namespace; template-resolved |
| `platformValues` | `helm` | Platform component whose values are the base - see below |
| `valuesFile` | `helm` | Values file relative to the scenario directory, layered on top |
| `set` | `helm` | Map of `--set` key/value overrides |
| `adopt` | `helm` | Reuse an already-installed release instead of upgrading it |
| `path` | `manifest`, `grafana-dashboard` | YAML file, or a directory of dashboard JSON |
| `script` | `script` | Shell script relative to the scenario directory |
| `uninstallScript` | `script` | Reverses that script on `scenario down` |

**Component types**

| Type | What it does |
|---|---|
| `helm` | Adds the repo and installs the chart at the pinned version with the resolved values |
| `manifest` | `kubectl apply` of the YAML at `path` |
| `grafana-dashboard` | Creates a ConfigMap from the dashboard JSON at `path`, which the Grafana sidecar picks up |
| `script` | Runs the shell script at `script` |

### Dashboards: an App selector, and legends a reader can follow

A dashboard that shows the workload declares an `app` variable, plus a
`namespace` variable when its queries need one, and its queries use `$app` and
`$namespace` rather than `{{.WorkloadName}}`. The variables default to the
activation's app, and the reader can switch to any other:

```json
{
  "name": "app", "type": "query",
  "query": {"query": "query_result(count by (app) ({{.WorkloadMetric}}_count) or label_replace(vector(1), \"app\", \"{{.WorkloadName}}\", \"\", \"\"))"},
  "regex": "/app=\"([^\"]+)\"/",
  "current": {"text": "{{.WorkloadName}}", "value": "{{.WorkloadName}}"}
},
{
  "name": "namespace", "type": "query",
  "query": {"query": "label_values(kube_deployment_spec_replicas{deployment=\"$app\"}, namespace)"},
  "current": {"text": "{{.WorkloadNamespace}}", "value": "{{.WorkloadNamespace}}"}
}
```

The `or label_replace(vector(1), …)` keeps the activation's app in the list even
when it exposes no request metric. The dashboard `uid` and `title` stay fixed.

`labctl validate` lints every dashboard's legends:

- A query grouped `by (label)` must name one of those labels in its legend,
  e.g. `{{namespace}}: available`. Otherwise every line carries the same name and
  nobody can tell the environments apart. A label pinned with `label="value"`
  yields one series and needs no legend label.
- A legend may only name labels the query returns. `{{code}}` on a query grouped
  by `http_response_status_code` renders empty.

Queries using `without`, `label_replace` or `label_join` are not judged, and
neither are tables.

### `platformValues`: one values file per component

A component's Helm values live **once**, at
`platform/<category>/<component>/values.yaml`. A scenario names that file with
`platformValues:` and supplies only its differences in `valuesFile:`. It never
keeps a second full copy.

```yaml
platformValues: logging/loki                        # the whole component's values
platformValues: logging/loki/promtail-values.yaml   # or one specific file
valuesFile: values/loki.yaml                        # overlay
```

Duplicated values drift, and Helm reports the drift as a forbidden update to an
immutable StatefulSet field. See
[ADR-0010](../adr/0010-platform-values-single-source.md).

### `adopt`: do not clobber a platform release

`adopt: true` reuses an already-installed release rather than upgrading it, so a
scenario never overwrites something the platform owns. A scenario that adopts a
platform release must not uninstall it on teardown.

### `uninstallScript`: undo a script component

Without it, a script's side effects (an env var set on a Deployment, say)
outlive the scenario that created them.

```yaml
- name: enable-tracing
  type: script
  script: scripts/enable-tracing.sh
  uninstallScript: scripts/disable-tracing.sh
```

## Checks

Checks are the grading primitive. Write them as "what must be true when this
scenario is healthy". They run in declaration order, and a failure does not stop
the rest.

```yaml
checks:
  - name: loki-ready
    type: kubectl                     # http | kubectl | promql | script
    resource: statefulset/loki        # type/name, or a bare type for existence
    namespace: "{{.MonitoringNamespace}}"
    jsonpath: "{.status.readyReplicas}"
    operator: ">="                    # == != < <= > >= contains
    value: "1"

  - name: grafana-reachable
    type: http
    url: "http://grafana.{{.IngressURLSuffix}}"
    expectStatus: 200                 # default 200
    bodyContains: "Grafana"           # optional

  - name: latency-ok
    type: promql                      # queries $PROMETHEUS_URL, default
    query: 'histogram_quantile(...)'  # http://prometheus.<DOMAIN_SUFFIX>
    operator: "<"
    value: "0.3"

  - name: custom
    type: script                      # exit 0 passes; runs with DOMAIN_SUFFIX,
    script: checks/custom.sh          # MONITORING_NAMESPACE and PROJECT_ROOT set
    timeoutSeconds: 60                # any check may override the 30s default
    remediation: "run the fix: …"     # shown under a failing check
    pending: true                     # render PENDING rather than FAIL
```

Each check type accepts only its own fields - an `http` check carrying a `query`
is rejected, not ignored.

A `kubectl` check whose field is unset, such as `.status.readyReplicas` before
any replica is ready, observes an empty value. Against a number that counts as
`0` for every operator, so `>= 1` and `!= 0` both fail on it.

A `promql` check fails when the query has nothing to measure: no samples, or a
result of `NaN` or `Inf`, which is what a ratio answers when its denominator is
zero. Neither can satisfy a threshold, whichever operator the check uses.

### `remediation` and `pending`

These make `verify` teach instead of alarm.

- A failing check with a `remediation` prints it under **Next step(s)**. The
  generic "a pod may still be starting" hint appears only for a failure with no
  remediation of its own.
- A failing `pending` check renders **PENDING** and reads as an incomplete drill
  step, so "you have not run the backup yet" never looks like "the scenario is
  broken". `verify` still exits non-zero.

Assert on the observability pipeline too. A check that the metric *exists* means
an empty dashboard fails `verify` instead of quietly confusing a learner - see
[R13](../runbooks/R13-observability-pipeline.md).

> When asserting on a running image, compare `.spec.containers[].image`, not
> `.status.containerStatuses[].image`. The latter reports whichever tag the
> kubelet resolved, so it names `:latest` for a pod that requested `:v1.1.0`
> when both tags share a digest.

### Testing a check script

A `script` check ships with a behaviour test in `src/test/shell/`, one file per
scenario (`scenario_<name>_checks.bats`). The test runs the real script against
a scripted `kubectl` and `curl` (`stub_script` in `helpers/stub.bash`), so it
needs no cluster. `content_tests_complete.bats` fails for a check script no
test names.

Cover one lab state per test:

| State | The check must |
|---|---|
| The lab as activation leaves it | fail (PENDING) if it grades learner work; pass if it grades lab health |
| Solved | pass |
| Each shortcut that produces the result without the work: the change made with `kubectl` instead of Git, the pod redeployed, the workload scaled to zero, the file created empty | fail, saying why |
| The cluster or a tool cannot answer | fail with a reason, never pass |
| Its activation checkpoint is missing | fail with the command that re-creates it |

Two rules these tests enforce, because both have shipped as bugs:

- **An empty answer is not a good answer.** `kubectl ... || true` followed by
  "nothing found, so OK" passes on a cluster that is down. Fail when the query
  itself failed.
- **A refusal needs something to refuse.** A probe that cannot connect proves
  enforcement only while the target is running.

Declared `kubectl`, `promql` and `http` checks are graded against recorded
lab states instead: `test/fixtures/untouched.json` and `solved.json`, written
by the cluster suite's record mode and replayed by `go test`. A `pending`
check that passes in the untouched state, or any check that fails in the
solved one, fails the build. Change a check's query, jsonpath or URL and the
replay asks for a re-record.

**`test/solve.sh`** does the scenario's work the way its review guide does, so
the nightly cluster suite can run `up`, a failing `verify`, the solve script, a
passing `verify --watch` and `down` on a real cluster
([TESTING](../TESTING.md#real-cluster)). Write it with the helpers in
`src/test/cluster/solve-lib.sh`, give it a test in
`src/test/shell/cluster_solves.bats`, and try it on your own lab with
`make test-cluster ITEM=scenario/<name>`; with `RECORD=1` the same run writes
the fixtures.

## Parameters

Tunable knobs exposed at activation time and substituted into the scenario's
manifests as `{{.Name}}`. No parameters, or no overrides, means unchanged
behaviour.

```yaml
parameters:
  - name: MaxReplicas
    displayName: "Maximum replicas"
    description: "Ceiling the autoscaler will not exceed."
    default: "6"                      # required
    type: int                         # int | string (default string)
    min: 1                            # inclusive bounds, int only
    max: 10
  - name: MinReplicas
    default: "1"
    type: int
    min: 1
    max: 10
    notGreaterThan: MaxReplicas       # enforced against the effective values
```

Override at activation:

```bash
labctl scenario up autoscaling-under-load --set MaxReplicas=4 --set Threshold=15
```

An `int` parameter is bounds-checked and parsed before substitution, for both
the default and any override.

## Prerequisites

| Field | Meaning |
|---|---|
| `platform` | Platform components that must be installed |
| `apps` | Apps whose `apps/<name>/app.env` must exist |
| `capabilities` | What the **bound workload** must declare |

### `apps` is for a pinned app, not for the one you are bound to

Write `apps: ["{{.WorkloadName}}"]` and the entry means "the bound workload must
exist", not "this scenario requires go-api". Every shipped scenario is written
this way, and the UI treats the two differently: a templated entry is shown as
**Runs against**, with a picker offering every app in the lab; a literal one is
shown as a requirement the user has to satisfy, and it disables the picker.

Prefer `capabilities` over a literal app. Pinning one is a restriction that
cannot be lifted at activation time.

### `capabilities`: stating what you need, not who provides it

A scenario that names an app can only ever run against that app. One that states
what it needs of an app can run against any app that provides it, including a
user's own. See [ADR-0014](../adr/0014-workload-binding-and-app-contract.md).

```yaml
prerequisites:
  capabilities:
    - prometheus-metrics
    - otlp-tracing
```

The vocabulary is closed - `labctl app capabilities` lists it, and an unknown
name fails `labctl validate` rather than silently meaning "never satisfied":

| Capability | The app… |
|---|---|
| `prometheus-metrics` | serves the metrics path in Prometheus format, including the request-duration histogram |
| `otlp-tracing` | honours `OTEL_EXPORTER_OTLP_ENDPOINT` and exports spans |
| `readiness-toggle` | can have its readiness flipped to failing on demand |

Require only what the scenario genuinely uses. A requirement is a restriction on
which apps can run it, so an unnecessary one narrows the scenario for nothing.

Preflight refuses to activate a scenario the bound app cannot satisfy, before
anything installs. `labctl scenario info` shows the requirement graded against
the app currently bound:

```
Prerequisites (workload capabilities), bound to "echo-server":
  - prometheus-metrics     ok
  - otlp-tracing           missing
  - readiness-toggle       missing
```

## Requirements

`requirements` tells labctl what activating the scenario needs from the lab
beyond what it already declares. Everything is optional.

```yaml
requirements:
  memory: 300Mi     # memory for this scenario's own workloads
  cpus: 4           # fewest Docker CPUs it runs well on
  agents: 2         # agent nodes it needs
  exclusive: true   # nothing else may be active alongside it
```

| Field | Meaning |
|---|---|
| `memory` | A Kubernetes quantity (`300Mi`, `1.5Gi`) for the workloads that belong to this scenario alone. Leave out the Helm releases it installs, the platform components it lists and its apps: those have measured footprints in `config/footprints.yaml` and are counted once however many active scenarios share them. |
| `cpus` | Fewer Docker CPUs only warns; the scenario runs slowly. |
| `agents` | k3d adds the missing agent nodes when the scenario starts, if memory allows, and removes them when it goes down and nothing active needs them. kind cannot add nodes to a running cluster, so the activation stops and asks for `AGENTS=<n>` and `labctl reset`. |
| `exclusive` | For a drill that changes the cluster itself, such as replacing its nodes. Nothing else may be active while it is, and it cannot start while anything else is. |

Before a scenario (or a fault, which takes the same `requirements` in
`fault.yaml`) starts, labctl estimates the lab's memory two ways and uses the
larger: the baseline plus the footprints of everything active and the new
scenario, and the lab's current usage plus whatever the new scenario would
start that is not already running. If that exceeds 85% of Docker's memory the
activation is blocked. The message lists which active scenarios to bring down
to make room and how large to make Docker instead, with the resize command for
colima, Docker Desktop, WSL or native Linux. Too few CPUs, and two active
scenarios bound to the same app, only warn.

`scripts/measure-footprints.sh` measures what each scenario adds on a real lab;
use it to set `memory` for a new scenario and to update
`config/footprints.yaml`.

## Template variables

URLs, commands, namespaces, snippets and manifests are Go templates.

| Variable | Example | Meaning |
|---|---|---|
| `{{.DomainSuffix}}` | `snowops.localhost` | Ingress domain suffix from the active runtime. Use it for **hostnames** (an Ingress `host:`), never in a URL. |
| `{{.IngressURLSuffix}}` | `snowops.localhost` or `snowops.localhost:8080` | The domain suffix plus the ingress port when it is not 80 (the runtime falls back when 80 is busy). Build every **URL** from it: `http://grafana.{{.IngressURLSuffix}}`. `labctl validate` rejects a URL built from `{{.DomainSuffix}}`. |
| `{{.MonitoringNamespace}}` | `monitoring` | Where the monitoring stack lives |
| `{{.ProjectRoot}}` | `/home/me/snowopslabs` | Absolute path to the learner's clone for execution. Learner-facing commands shorten it to a clone-relative path at the clone root, or a `~` path elsewhere. |
| `{{.StateDir}}` | `/home/me/.snowops/state/snowops` | The lab state directory, for files that belong to the running lab rather than the clone (e.g. backup archives). Scripts get it as `LAB_STATE_DIR`; learner-facing text shortens home paths to `~`. |
| `{{.LokiRetentionPeriod}}` | `72h` | Loki's configured retention |
| `{{.IngressClass}}` | `traefik` | Ingress class for scenario Ingress manifests |
| `{{.WorkloadName}}` | `go-api` | The bound app's name, and its Deployment name |
| `{{.WorkloadNamespace}}` | `go-api` | Where the bound app is deployed |
| `{{.WorkloadService}}` | `go-api.go-api.svc.cluster.local` | Its in-cluster DNS name |
| `{{.WorkloadPort}}` | `8080` | The port it serves HTTP on |
| `{{.WorkloadMetric}}` | `http_server_request_duration_seconds` | The request-duration histogram it exposes |
| `{{.SinceActivation}}` | `95m` | Time since the scenario was activated (or the incident injected), as a PromQL range; `1m` when nothing is active |

An unknown variable is a validation error, not an empty string.

### Grading something that already happened

A check for an event (an alert fired, a backlog built up) looks back over a
range. Use `{{.SinceActivation}}` for it, not a fixed window:

```yaml
query: 'max_over_time((count(ALERTS{scenario="my-scenario",alertstate="firing"}) or vector(0))[{{.SinceActivation}}:1m])'
```

A fixed `[15m:1m]` fails a learner who did the drill early and verified later.
An unbounded one counts the previous run's alerts. The range opens when
`scenario up` finishes, and re-activating opens a new one.

### The workload variables

A scenario names the app it acts on through `{{.Workload*}}` rather than a
literal, so the same scenario runs against a built-in app or one the user brings
(see [ADR-0014](../adr/0014-workload-binding-and-app-contract.md)):

```yaml
checks:
  - name: app-scaled-up
    type: kubectl
    resource: deployment/{{.WorkloadName}}
    namespace: "{{.WorkloadNamespace}}"
    jsonpath: "{.status.readyReplicas}"
    operator: ">="
    value: "3"

  - name: latency-within-slo
    type: promql
    query: 'histogram_quantile(0.99, sum(rate({{.WorkloadMetric}}_bucket[5m])) by (le))'
    operator: "<"
    value: "1.5"
```

`{{.WorkloadMetric}}` names the histogram rather than assuming it, because each
language's instrumentation library picks its own. Derive a request rate from its
`_count` series (`rate({{.WorkloadMetric}}_count[5m])`) - the OpenTelemetry
semantic conventions the app contract mandates define no separate counter.

The names are flat (`{{.WorkloadName}}`, not `{{.Workload.Name}}`). The expander
matches a single dotted identifier on purpose, so it never rewrites the Helm,
Prometheus and Grafana templating that shares these files; a nested reference
would pass through unresolved and reach kubectl verbatim.

### Templates in content, environment variables in scripts

The engine resolves `{{.Workload*}}` in everything it reads itself: `scenario.yaml`,
the manifests it applies, dashboard JSON, snippets and prose. It does **not**
resolve a script: a script is executed as a file, so a `{{.WorkloadName}}` inside
one reaches `kubectl` verbatim.

Scripts get the binding as flags or from the environment. Every component
script, check script and fault script run by the engine is given:

| Variable | Example |
|---|---|
| `WORKLOAD_NAME` | `go-api` |
| `WORKLOAD_NAMESPACE` | `go-api` |
| `WORKLOAD_PORT` | `8080` |
| `WORKLOAD_METRIC` | `http_server_request_duration_seconds` |
| `PROJECT_ROOT` | `/home/me/snowopslabs` (the clone) |
| `LAB_STATE_DIR` | `/home/me/.snowops/state/snowops` |

Check scripts also get the lab's ingress: `DOMAIN_SUFFIX`, `INGRESS_URL_SUFFIX`,
`HTTP_PORT`, `HTTPS_PORT`, and `PROMETHEUS_URL`, `GRAFANA_URL` and
`ALERTMANAGER_URL`. curl and browsers resolve a `*.localhost` host to this
machine themselves; other tools (openssl, dig, a language runtime) ask the
system resolver, which often cannot. Such a script connects to `127.0.0.1` and
names the host separately, as `openssl s_client -servername` does.

A script that prints a command for the learner names repo files through
`$PROJECT_ROOT`, and one that keeps files for the running lab puts them under
`$LAB_STATE_DIR`. By hand neither is set, so fall back to the script's own
location and to labctl's default:
`${LAB_STATE_DIR:-${SNOWOPS_HOME:-$HOME/.snowops}/state/${CLUSTER_NAME:-snowops}}`.

A learner also runs scripts by hand, from a terminal that has none of these. So
a scenario script sources the shared helper first, which takes the binding from
`--app` and `--namespace` flags and removes them from `"$@"`:

```sh
#!/usr/bin/env bash
set -euo pipefail
. "$(dirname "$0")/../../_lib/workload.sh"

kubectl -n "$WORKLOAD_NAMESPACE" get deploy "$WORKLOAD_NAME"
```

Flags win over the environment, and `--app` without `--namespace` means a
namespace of the same name. With neither, the script stops with a usage error
rather than guessing an app. Never write a `:-go-api` fallback: a script run by
hand for echo-server would quietly act on go-api.

Every command that calls such a script passes both flags, so the copied text is
already filled in for the activation's app:

```yaml
explore:
  commands:
    - label: "Build the new version"
      command: "bash scenarios/env-promotion/scripts/build-image.sh v1.1.0 --app {{.WorkloadName}} --namespace {{.WorkloadNamespace}}"
```

`labctl validate` fails an explore command, check remediation, tip or snippet
`apply` that calls a script sourcing the helper without `--app`.

A manifest the learner applies by hand goes through `labctl scenario render`,
never `kubectl apply -f` on the file itself, which would send the literal
`{{.WorkloadName}}` to the cluster. `labctl validate` fails the second form when
the file contains a template variable:

```yaml
      command: "labctl scenario render node-drain-drill manifests/baseline.yaml --app {{.WorkloadName}} | kubectl apply -f -"
```
 Apply the same
rule to traffic: write `labctl traffic start --app {{.WorkloadName}}`.

> Incident scripts additionally receive `TARGET_NAMESPACE` and `TARGET_WORKLOAD`
> from the fault's own `target:` block, which is what a fault pinned to one
> application uses. See [incidents/README.md](../../incidents/README.md).

### What not to template

- **File paths.** `path: manifests/{{.WorkloadName}}-tls.yaml` looks for a file
  that does not exist. Name the file for its role instead.
- **Check names.** They are recorded against scores, so a templated name makes a
  result history incomparable. Name the check for what it asserts -
  `workload-healthy`, not `go-api-healthy`.
- **Dashboard UIDs**, for the same reason: they are stable identities.

### Writing a check that does not grade the language

A threshold calibrated against one application grades that application's runtime,
not the engineer's work. A p99 bound a Go service clears easily is one a JVM
fails on warmup alone, and a user's own application has no calibration at all,
so it would fail a check it has no way to satisfy.

Per-application baselines do not fix this: a conforming app still arrives without
one, and a *declared* baseline can simply be set generously enough to pass. Write
the assertion so it needs no calibration instead.

| Instead of | Assert | Why it travels |
|---|---|---|
| `p99 < 1.5` | `p99 / p50 < N` | Tail amplification is the same question for a 2ms service and a 200ms one - it measures degradation, not speed |
| `readyReplicas >= 3` | `readyReplicas > {{.MinReplicas}}` | "It scaled" is the lesson; a fixed count encodes one runtime's throughput per replica |
| a latency bound | error ratio `< 0.01` | Saturation shows up as errors in every runtime |

Most checks need no thought here: `readyReplicas >= 1`, `deployment exists`, and
`the metric is being scraped` already grade configuration rather than speed.

**When an absolute number really is the lesson**: an availability SLO in a
drill - make it a parameter so it reads as a deliberate choice, and say in its
description what a failure means:

```yaml
parameters:
  - name: AvailabilitySLO
    displayName: "Availability SLO across the drain"
    description: "Lower it if your workload is slower to become Ready than the
      drill allows - a slow-starting runtime failing this is a real finding about
      replica count and readiness gating, not a defect in the application."
    default: "0.995"
    type: string

checks:
  - name: availability-held-during-drain
    type: promql
    query: '...'
    operator: ">="
    value: "{{.AvailabilitySLO}}"
```

A check is graded against the values the scenario was **activated** with, so
`--set AvailabilitySLO=0.99` changes what `verify` requires. Parameters declared
by the scenario are legal template variables anywhere in it, and `labctl validate`
rejects a reference to one that is not declared.

Everything `labctl scenario info` and the UI display is resolved before it is
shown, component namespaces and charts included, using the scenario's
parameter **defaults** where a `{{.Param}}` appears. A learner therefore reads
`ns=monitoring` and `minReplicaCount: 1`, never the raw placeholder, and both
surfaces render the same content identically.

Templates belonging to another system are left alone: Prometheus rule
annotations (`{{ $value }}`, `{{ $labels.pod }}`), Loki `line_format`, and
`kubectl -o go-template` expressions print literally, because resolving them
would corrupt the very thing the snippet is teaching.

## References and snippets

Two optional blocks that turn a scenario into a jumping-off point. Both are
shown by `labctl scenario info` and the UI's Implementation tab, template-resolved
for the app the scenario runs against, so they display with its real names,
namespaces and domains.

```yaml
references:
  - label: "KEDA - ScaledObject specification"
    url: "https://keda.sh/docs/latest/reference/scaledobject-spec/"
    note: "Optional one-liner on why this link is relevant."

snippets:
  - label: "KEDA ScaledObject for go-api"
    description: "Optional context shown above the manifest."
    path: manifests/scaledobject.yaml
  - label: "A quick inline manifest"
    yaml: |
      apiVersion: v1
      kind: ConfigMap
      metadata:
        name: demo
        namespace: "{{.MonitoringNamespace}}"
  - label: "The autoscaler you write"
    path: manifests/scaledobject.yaml
    exercise: true
  - label: "Wiring the app to the collector"
    path: scripts/enable-tracing.sh
    exercise: true
    apply: "bash scenarios/observability-sre/scripts/enable-tracing.sh --app {{.WorkloadName}} --namespace {{.WorkloadNamespace}}"
```

- A reference needs a `label` and an `http(s)` `url`; `note` is optional.
- A snippet needs a `label` and **exactly one** of `yaml` (inline text) or
  `path` (a file in the scenario directory). `labctl validate` fails on a `path`
  that does not resolve, naming the file and the snippet.
- `exercise: true` marks a snippet nothing installs, because applying it is the
  learner's task. The UI badges it **You apply this** and offers a ready-to-run
  command. Say what it does in `description`, not "apply this yourself".
- `apply` is the complete command for an exercise. Leave it empty for a manifest:
  the command becomes the manifest piped into `kubectl apply -f -`. Set it for
  anything else, such as a script.
- `description` is shown above the body, and is where the explanation belongs.
  The body is trimmed for reading: the file's leading comment banner and every
  block of two or more comment lines are dropped, while single-line comments,
  comments after code, heredoc bodies and YAML block scalars stay.

## `verified`

```yaml
verified: true    # confirmed end-to-end on a fresh cluster
```

Curation metadata, not a user-facing badge. It records which content has been
confirmed to activate, pass its checks and tear down cleanly on a fresh cluster,
so the nightly e2e job knows what to guard. It is deliberately not surfaced in
the CLI or UI: shipped content is expected to work. Absent or `false` means "not
yet confirmed".

## Validation rules

Enforced at load time - an invalid scenario refuses to load, and CI fails on it.

- `components` or `stages`, never both.
- Stage, component and check names are unique and non-empty.
- Every `helm` component has a pinned `version`.
- Each check type accepts only its own fields.
- Asset paths stay inside the scenario directory; absolute paths and `..`
  traversal are rejected.
- Template variables must be known.
- A URL is built from `{{.IngressURLSuffix}}`, never `{{.DomainSuffix}}`.
- `requirements.memory` is a quantity such as `300Mi`; `cpus` and `agents`
  are not negative.
- A `path` on a snippet or component must resolve.
- A command that calls a workload-bound script passes `--app`.
- A command never runs `kubectl apply|create|replace|delete -f` on a templated
  scenario file; it pipes `labctl scenario render` instead.
- A dashboard legend names the labels its query groups by, and only labels the
  query returns.

```bash
labctl validate            # everything: scenarios, incidents, paths, challenges
labctl validate --json
```

## Sharing scenarios

A scenario is just a directory, so sharing one is git and nothing else, no pack
format, registry or publish step ([ADR-0008](../adr/0008-content-extensibility-seam.md)).

```bash
git clone https://github.com/org/our-scenarios ~/our-scenarios
export SNOWOPS_CONTENT_PATH=~/our-scenarios
labctl scenario list        # yours appear, badged as external
```

`SNOWOPS_CONTENT_PATH` accepts several roots separated by the OS path
separator.

- Each root is scanned for directories containing a `scenario.yaml`, and every
  one is schema-validated. An invalid scenario is reported by name and skipped;
  it never hides the rest.
- External scenarios work with `up`, `down`, `verify` and `info` exactly like
  in-repo ones, and show their root in the SOURCE column of `scenario list`.
- **In-repo scenarios win name collisions.** A colliding external scenario is
  skipped, with the conflict named.

> **Security.** A scenario's components run scripts and apply manifests on your
> cluster with your credentials. Only point `SNOWOPS_CONTENT_PATH` at sources
> you trust, and read them first. This is the same trust level as running any
> script from that repository.

## Creating one

```bash
labctl scenario new my-scenario       # scaffolds a valid, verify-green scenario
labctl validate                       # check it
labctl scenario up my-scenario
labctl scenario verify my-scenario
```

Add supporting files under `values/`, `manifests/`, `dashboards/` and `checks/`
as needed.
