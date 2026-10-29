---
name: release-acceptance
description: "Accept a SnowOps Labs branch for release: walk the live lab as a learner with the regression journey (scripts/release-acceptance.sh), derive and run checks for every user-visible change on the branch, fix or record what fails, and report a go/no-go. Use before merging to main for a release, or when asked to test the application as a user, check a branch for regressions, or confirm new features work end to end."
user-invocable: true
disable-model-invocation: true
---

# Release acceptance

The process is [docs/release-acceptance.md](../../../docs/release-acceptance.md).
Read it first. It defines the steps and what counts as a pass, and this file
is only the operating procedure. When the two disagree, the document wins.

**Input:** a branch (default: the current one) and optionally a scenario to
cycle. **Output:** `report.md` with the journey table, a change-walk section,
findings ranked by severity, and a go/no-go verdict.

## 1. Preconditions

```bash
git status --short                         # a clean tree: you test what ships
git fetch origin main && git log --oneline origin/main..HEAD
kubectl config current-context             # a live lab; none means stop and say so
./bin/labctl incident status               # "No incident is active"
```

With no live lab, say so and stop. Do not build one unless asked: `labctl init`
on a fresh machine takes 10+ minutes and belongs to R14 onboarding.

## 2. Plan the change walk

Read the branch, not just its titles:

```bash
git log --format='%h %s%n%b' origin/main..HEAD
git diff --stat origin/main...HEAD
```

Map every user-visible change to a check with the table in the doc's "change
walk" section. Write the plan into the report before running anything: one
line per check, naming the command or page and the expected result. A commit
that says it fixes something gets a check that reproduces the original
symptom where it can (the Grafana repair was proven by removing
`GF_PLUGINS_PREINSTALL_DISABLED` from the live Deployment and running
`labctl init`).

## 3. Run the regression journey

```bash
make cli-install                           # learners run the labctl on PATH
command -v labctl                          # must be this clone's build
scripts/release-acceptance.sh              # add RA_SCENARIO=<name> to cycle one
```

Run it in the background; it takes about six minutes with the gates. Read every
FAIL's `<step>.log`. Also skim the PASS logs of `init` and `incident`: a fast
`init` should show "Platform already installed", and the incident log should
say "detection check fires".

## 4. Walk the changes

Run each planned check. Prefer the learner's surface: the CLI as documented,
the UI in the browser pane (`labctl ui`, then read the page and the console),
Grafana through its lab URL. Log in with the lab's default `admin` / `admin`
only; never use real credentials.

Anything a check changes (a fault, a drifted Deployment, a scenario), put back
before moving on, and confirm with `labctl status`.

## 5. Fix or record

- A regression, or a new feature that does not work: fix it on the branch with
  its test and docs (CLAUDE.md rules apply), then re-run the failing step with
  `--only` and the change-walk check.
- Anything out of scope: a GitHub issue with the reproduction (`gh issue create`,
  labelled by area). The issue describes the project only: version, commands,
  what happened and what was expected, never the outside work that surfaced it
  ([WRITING](../../../docs/WRITING.md#write-about-the-project-not-the-occasion)).
- Never weaken a check to make it pass.

## 6. Report

Append to `report.md`:

```markdown
## Change walk
| Change (commit) | Check | Result |

## Findings
1. <severity> - <symptom>, <evidence>, <fixed in commit | issue number>

## Verdict
GO / NO-GO - <one sentence>
```

Give the user the report path, the verdict, and each finding with its evidence.
GO needs every journey step PASS or an explained SKIP, every change-walk check
passing, and no open finding above low severity.

## Checklist

- [ ] Clean tree, live lab, no active incident
- [ ] Change-walk plan written before running
- [ ] Journey run on this branch's `labctl`, every FAIL log read
- [ ] Every planned change check run on the live lab
- [ ] The lab left as found (`labctl status`, `labctl incident status`)
- [ ] Fixes carry tests and docs; `make test lint docs-check` green after them
- [ ] Report with findings and a verdict
