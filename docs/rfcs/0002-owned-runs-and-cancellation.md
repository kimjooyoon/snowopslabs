# RFC 0002: Owned runs and cancellation requests

**Status:** Draft, not implemented or accepted
**Author:** @kimjooyoon
**Date:** 2026-10-25

## Summary

**Planned:** give each engine an OS-held owner lock, preserve another live
engine's runs, and separate cancellation requests from execution completion.
Keep uncertain interrupted work reserved until machine-verifiable evidence
establishes that execution has stopped. This proposal does not supply an
execution witness for running work after its owner is killed.

## Motivation

The [traffic operation migration](https://github.com/sagar2395/snowopslabs/issues/13)
needs multiple CLI invocations to share one durable store safely. Two independent
[reproducers and their raw results](https://gist.github.com/kimjooyoon/209dba6b3ca78c1bfc44aa3f5222c677/0d6986f1dd05df1d26be24dc666cefca92321c97)
show that adding a service lock is insufficient:

- Starting an unlocked status engine marks another live engine's start as
  cancelled. Startup recovery currently terminalizes every active row.
- The actual CLI cancel command marks a run cancelled before its execution
  returns. A same-key operation then executes during the first operation's
  deliberately held cleanup. Without cancellation, the control rejects it.

The second test uses an in-process operation and controlled cleanup channels.
It establishes the engine/CLI completion gap, not Kubernetes process cleanup.
Both tests use temporary SQLite and a fake toolchain. Neither uses a cluster.

## Design

### Scope and ownership

Support cooperating engines on one host, using local storage and one canonical
SQLite path and owner directory. Remote filesystems, hard-link aliases of the
database, external replacement/deletion of referenced owner files, and mixed
old/new writers are outside this protocol. A lock at a replaced pathname cannot
establish anything about the original inode. Unsupported or
uncertain owner probes must preserve reservations, not guess that an owner died.

Each engine creates a fresh random owner ID and an exclusive owner file beside
the canonical database. Acquire a nonblocking exclusive `flock` before accepting
work. Hold the descriptor until every worker has returned and persisted its
completion. A shutdown timeout must not release it or close the store beneath
workers that are still finishing. Owner IDs are never reused.

The descriptor is close-on-exec and is not passed to children. Referenced owner
files are never replaced or unlinked. Their directory is writable only by the
local operator. Probe an existing owner file without creating a missing one.
A busy lock is evidence of an active protocol participant. Acquiring the lock
permits owner-scoped recovery; it is not evidence that its subprocesses stopped.

These are advisory cooperation locks, not authentication. The
[macOS manual](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/flock.2.html)
and [Linux manual](https://man7.org/linux/man-pages/man2/flock.2.html) describe
nonblocking locks and shared references through duplicated descriptors. Keep the
adapter cgo-free and test it on macOS and Linux, including WSL2 in its Linux home.

### Durable fields and writes

Add these internal store fields:

| Field | Meaning |
|---|---|
| `owner_id` | Immutable engine identity; nullable only for migrated legacy rows |
| `cancel_requested_at` | Nullable cancellation intent, not completion |
| `recovery_state` | `none` or `required`, default `none` |
| `recovery_reason` | Stable reason for a required recovery, empty otherwise |

Keep the existing six lifecycle status values. With `recovery_state=required`,
`queued` or `running` records the last observed phase, not current liveness.
The existing atomic guarded `CreateRun` insertion and queued/running lock
predicate remain the reservation mechanism. Do not replace them with a
process-local mutex or a separate check followed by an unguarded insertion.

New submissions require the submitting owner, bound to the engine rather than
accepted from a client request. Ordinary start and completion
writes must match that owner and `recovery_state=none`. Start additionally
requires no pending cancellation. Owner-scoped recovery uses separate guarded
writes while holding the probed owner's exclusive file lock. Recheck owner,
phase and recovery state in the same transaction that changes the record.
Only established execution completion releases a running reservation.

### Startup recovery

Inspect owners of active records without cancelling unrelated work. Hold a
successfully acquired owner lock through its recovery transaction.

| Owner evidence | Last phase | Result |
|---|---|---|
| Owner lock busy | queued or running | Preserve record, reservation and logs |
| Existing owner lock acquired | queued | Cancel without executing; release reservation |
| Existing owner lock acquired | running | Set recovery required; retain reservation and partial logs |
| Legacy ownership during the offline upgrade | queued or running | Set recovery required; retain reservation and logs |
| Missing, inaccessible or uncertain owner probe | queued or running | Preserve phase, recovery state and owner-write eligibility; report the probe error |
| Terminal record | any | Leave unchanged |

A queued-to-running transition must precede every script or in-process action.
That is what makes dead-owner queued recovery safe. A crash after that
transition but before launching is conservatively treated as unverified running
work. Repeated recovery is idempotent. An uncertain owner probe preserves its
reservations and may still belong to a live engine. It cannot revoke that
owner's ability to finish normally. Report the failed probe without changing
its recovery state. Unrelated keys and unlocked probes continue to run.

An exited owner does not establish that its children exited. The current runner
keeps process-group identity and its TERM/KILL goroutine only in memory. This
proposal therefore provides no automatic release of interrupted running work.
Do not signal a saved PID or PGID based on age or identifier equality, and do not
release the reservation on a human assertion that cleanup happened. An accepted
supervisor or OS execution-witness protocol is required before such a release
operation exists. Missing evidence remains visible and keeps the reservation.

### Cancellation and completion

All CLI, API and in-engine cancellation entry points persist idempotent intent.
For a running operation, a request keeps its status, owner and reservation. The
executing owner observes the request, cancels its context and lets the runner
finish its TERM/KILL and cleanup path. Only after execution returns does that
owner record terminal completion. A local owner can signal its context at once
as well as persisting intent; a remote requester cannot finish its record.

Queued cancellation must prevent execution through the guarded start transition.
The owner acknowledges a queued request without launching work. A cancelling
request must not cause an elapsed-time lease to expire or release the key.

The CLI says `Cancellation requested`, rather than `cancelled`, until completion
is recorded. Existing terminal-run errors remain errors. Requests against work
requiring recovery report that termination is unverified and retain its key.

### CLI and API compatibility

Expose optional `recoveryState` and `recoveryReason` fields in run summaries,
details and log responses. The CLI and UI must display `Recovery required:
execution termination unverified`, rather than an advancing running duration.
Disable ordinary cancellation and active streaming cues for that state. A CLI
follower drains saved logs and returns a recovery-required error; the log API's
existing terminal `done` meaning remains unchanged. Recovery state stops a
follower separately from terminal completion.

Derived platform/lab state must not interpret a recovery-required reservation as
an active install. Keep the existing status filter vocabulary. No public Go SDK
run type changes are needed, but JSON clients that ignore the new fields can
still display stale activity. Structural compatibility is not full behavioral
compatibility.

## Alternatives considered

| Alternative | Reason to reject or defer |
|---|---|
| Recover all active rows at every startup | Cancels a live owner and clears its reservation |
| One global engine lock | Prevents independent or unlocked operations from coexisting |
| PID alone or an expiring heartbeat | Reuse or a paused live owner can produce a false death decision |
| Terminalize a cancellation request | Releases the key while cleanup can still be running |
| New `recovery_required` lifecycle status | Changes SQL checks, API status filters and the closed client status vocabulary |
| Human acknowledgment of cleanup | Does not establish execution quiescence |
| Local supervisor for every run | Can improve crash cleanup, but needs a separately accepted execution-witness design and its own failure handling |

## Impact

An offline schema upgrade must stop older engine processes and prevent older
writers from reopening the migrated database. Active legacy rows retain their
reservations because they have no owner witness. Do not claim that a nullable
owner field alone fences an already-open old writer. Establishing this upgrade
boundary is an implementation acceptance condition.

Owner files add one small file per engine. Existing retention can prune an owner
file only after acquiring its exclusive lock and establishing that no run row
references it. Never reuse a retired ID or delete a busy or referenced file.
No heartbeat service, daemon, remote backend or paid infrastructure is proposed.

After acceptance, update the architecture's cancellation and recovery contract,
the lock ADR, run CLI/API reference and the cancellation runbook. In particular,
replace the runbook's unconditional interrupted-lock-release assertion with
phase-aware expectations. The traffic wrapper remains a separate implementation;
this draft does not complete its cross-terminal acceptance.

Required implementation tests, with real local subprocesses where relevant:

1. The two-engine status reproducer preserves the live start and its key.
2. The remote-cancel reproducer refuses a same-key operation until cleanup returns.
3. Simultaneous same-key submissions through separate connections have one holder;
   disjoint keys and unlocked probes proceed.
4. A paused live owner remains reserved; failed probes do not imply owner death.
5. A dead owner's queued work recovers without execution. Interrupted running work
   with a surviving child retains its reservation and is shown as recovery required.
6. Cancellation arriving at worker pickup prevents launch. Concurrent cancellation
   and natural exit produce one terminal result, with the key held until return.
7. Shutdown timeout retains owner/store lifetimes until workers finish. Test that
   descriptors are not inherited and protocol operations never replace or unlink
   busy or referenced owner files; pruning must preserve those witnesses.
8. Legacy rows, an already-open old writer and repeated recovery cannot release an
   uncertain reservation. Client/follower tests exercise the additive recovery fields.

## Open questions

- Accept additive recovery metadata, or require a new lifecycle status despite
  its wider client and schema impact?
- How will the offline upgrade gate mechanically exclude already-open old writers?
- Which supervisor or OS witness can establish termination of ownerless execution?
  Until that protocol is accepted and tested, running-orphan release stays unsupported.
- Should queued requests be acknowledged by an owner-wide poll or at worker pickup?
