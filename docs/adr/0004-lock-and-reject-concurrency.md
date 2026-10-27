# ADR 0004: Exclusive lock keys; reject conflicting runs

**Status:** Accepted
**Date:** 2026-07-26
**Wave:** W1

## Context

v1 had no concurrency control. Two `platform up` invocations, from the CLI and
from an impatient double-click in the UI, would run simultaneously against the
same cluster, racing on Helm releases and namespaces. Helm's own locking
produces confusing mid-operation errors rather than a clear refusal.

## Decision

Every run declares a **lock key**, typically `lab:<name>` or
`lab:<name>/ns:<namespace>`. The engine holds an exclusive lock per key.

A submission whose lock key is held is **rejected immediately** (HTTP 409, or a
non-zero CLI exit) with an error naming the run that holds the lock, what it is
doing, and how long it has been running. Runs with disjoint keys proceed
concurrently, bounded by a worker pool.

## Alternatives considered

| Alternative | Why rejected |
|---|---|
| Queue conflicting runs | A user who fires `platform up` twice wants to be told, not to silently wait five minutes for a duplicate install |
| Global mutex (one run at a time) | Blocks legitimately independent work - watching traffic while inspecting a different namespace |
| Optimistic, let Helm arbitrate | Produces opaque failures deep in a run, after partial mutation. Worse than refusing up front |

## Consequences

- **Easier:** the failure mode is a clear, immediate message instead of a
  corrupted half-install. Trivially testable.
- **Harder:** callers must handle 409. The CLI prints the holder and suggests
  `labctl runs logs <id>`; the UI surfaces it as a non-blocking notice with a
  link to the running job.
- Lock keys must be chosen carefully. Too coarse serialises unnecessarily, too
  fine allows real races. The key for each run kind is declared in one table in
  `internal/run` and reviewed as part of any new run kind.
- Queued and running rows reserve keys through the store's atomic insertion.
  Starting a second engine preserves work whose owner file is locked. A dead
  owner's queued work is cancelled before execution; interrupted running work
  keeps its key and requires an explicit operator release.

## Owner-aware recovery

Each engine holds a fresh random owner's nonblocking local file lock until all
workers return and persist their results. A shutdown timeout retains both that
lock and the store. Worker start and completion require the same owner and no
recovery override. Children do not inherit the descriptor; acquiring a dead
owner's lock does not establish that its children stopped.

`labctl runs recover <id> --force` holds the recorded owner's lock through a
transaction that releases the key and records the former owner, local operator,
time and reason. Failed audit writes retain the key. Busy or failed lock probes
refuse recovery. Repeated releases preserve the original audit. Referenced owner
files are never removed or recreated during a probe.

The scope is local storage and one canonical database path on macOS or Linux,
including WSL2 in its Linux home. Store-lifetime shared upgrade locks and
nonblocking exclusive migration locks require an offline upgrade. Old binaries
without those locks must be stopped by the operator. See the
[accepted recovery design](../rfcs/0002-owned-runs-and-cancellation.md) for the
surviving-child limitation and the separately planned remote-cancellation fix.
