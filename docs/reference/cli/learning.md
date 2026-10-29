# Learning & challenges

## `labctl learn`: guided paths

Learning paths combine cluster setup, app deployment, scenarios and incidents
into structured modules with machine-verifiable completion checks. Progress
lives in `~/.snowops/state/<cluster>/learn/` and survives CLI restarts.

```bash
labctl learn list                                    # paths with your progress
labctl learn start kubernetes-foundations            # start or restart a path
labctl learn next kubernetes-foundations             # verify the current module and advance
labctl learn next kubernetes-foundations --show-only # print the intro without checking
labctl learn progress                                # all paths
labctl learn progress kubernetes-foundations
```

`next` shows the next incomplete module's intro and objective, then runs its
completion check. Run it again once you have done the task to verify and
advance. If only one path exists, the path argument can be omitted.

The web UI's **Check** button does the same through
`POST /api/v2/learn/paths/{name}/complete` with `{"moduleIdx": n}`. The server
runs the module's check against the lab and records the module only when it
passes; the answer is the path's progress with the check's result in `check`.
Like the CLI, it grades against the default app.

REST: `GET /api/v2/learn/paths`, `GET /api/v2/learn/paths/{name}`,
`GET /api/v2/learn/paths/{name}/progress`, `POST /api/v2/learn/paths/{name}/start`,
`POST /api/v2/learn/paths/{name}/reset`, `POST /api/v2/learn/paths/{name}/complete`.

## `labctl challenge`: timed assessments

A challenge injects a real fault (or activates a scenario), races you against a
par time, and auto-grades on submit. Each hint costs score.

```bash
labctl challenge list
labctl challenge info restore-broken-deploy
labctl challenge start restore-broken-deploy    # sets up and starts the timer
labctl challenge status                         # elapsed time and hints used
labctl challenge hint                           # next hint (−5% score each)
labctl challenge submit                         # run checks, score, record the result
labctl challenge abort                          # undo setup, score as aborted
labctl challenge history                        # past runs with MTTR, score, hints
```

`start` flags:

| Flag | Default | Meaning |
|---|---|---|
| `--deploy-prereqs` | off | install the platform components and build and deploy the app the challenge needs |
| `--force` | off | override an already-active challenge |

**Score formula:** `100 − (hints × penalty) − time_over_par_penalty`, scaled by
the fraction of checks that pass. Details in
[challenges/README.md](../../../challenges/README.md).

**Grading runs the check the way its own engine does.** With
`grading.useDetectionCheck: true`, `submit` resolves the fault's detection check
exactly as `labctl incident status` does - template variables expanded, and the
check's `script` resolved against the *fault's* directory rather than the
project root, and the fault's target (`TARGET_NAMESPACE`, `TARGET_WORKLOAD`) in
its environment. A challenge that sets up several faults (`setup.refs`) is
graded by every fault's detection check, each run that way, so fixing one of two
scores half. A challenge's own `grading.checks` are template-resolved too, with
their scripts relative to the project root. Without this a templated URL reaches
the HTTP client verbatim and a detection script is looked up in the wrong place,
so a correct fix scores zero.

**Several faults, and silent challenges.** `start` injects a `setup.refs`
challenge's faults in order; `abort`, or a passing `submit`, resolves every one.
`hint` takes the faults' hint ladders in turn, one rung of each. A challenge with
`silent: true` keeps its faults' names out of `info` and the API, which give
only how many there are. A failed `submit` also reports each check as `fault N of M`
with `not fixed yet`; after every fault passes, the result names the checks again.

REST: `GET /api/v2/challenges`, `GET /api/v2/challenges/{name}`,
`GET /api/v2/challenges/status`, `GET /api/v2/challenges/history`,
`POST /api/v2/challenges/complete`. A run's `elapsedSeconds` is whole seconds.
`GET /api/v2/challenges/{name}` carries `silent`, and for a silent challenge its
`setup` is `{"type", "count"}` instead of the names.

`complete` is `labctl challenge submit` over HTTP. It takes no body: the server
runs the challenge's checks against the lab and answers the scored run with
`passed` and the result of each check in `checks`. For a failed silent challenge,
the check names and messages are replaced with `fault N of M` and `not fixed yet`.
A pass is recorded for the signed-in user, ends the run and undoes the setup;
`cleanupWarning` is set if the setup could not be undone. A fail scores the
attempt and leaves the run going. It answers `409 no_active_challenge` when no
challenge is running and `409 no_checks` when the challenge's grading resolves
to no checks.
