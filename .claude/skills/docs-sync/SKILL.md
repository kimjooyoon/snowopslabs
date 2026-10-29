---
name: docs-sync
description: "Bring the documentation back in step with the code after any change, and keep the snowopslabs-web site building. Use at the END of every task that touched src/, scenarios/, incidents/, learn/, challenges/, platform/ or config/, and whenever a doc is added, renamed, split or deleted."
user-invocable: true
---

# Keeping the docs true

Documentation is the source of truth, and the website publishes these files
directly. A change is not finished until the docs match it.

## What to update, by what you changed

| You changed | Update |
|---|---|
| A CLI command, flag or its help text | the matching page in `docs/reference/cli/` |
| A `scenario.yaml` field or check type | `docs/reference/scenario-schema.md` |
| A new or removed scenario | `docs/scenarios.md` (the catalog) |
| A new or removed fault | the table in `incidents/README.md` |
| A learning path or challenge | the table in `learn/README.md` or `challenges/README.md` |
| A platform component or chart pin | `docs/runbooks/R05-platform-components.md` |
| Anything metrics, logs or traces | `docs/runbooks/R13-observability-pipeline.md` |
| An API route or convention | the REST section of the relevant `docs/reference/cli/` page |
| `install.sh`, `labctl init`, `doctor`, `bootstrap/` or a runtime under `runtimes/` | `README.md` (What you need, Install, Your first lab), `docs/getting-started/` for each OS it affects |
| An error or warning a user can see | its row in `docs/troubleshooting.md`, or a new row |
| A scenario's or fault's memory needs, or the capacity check | `docs/resources.md` |
| A key in `config/.env.example` | its comment there, and README Configuration if users set it |
| The app contract or `apps/<name>/app.env` | `apps/README.md` |
| A web UI screen shown in the README | re-capture it with `docs/assets/gen/capture-ui.mjs` |
| The release process or the `stable` branch | `RELEASING.md` |
| What the product is for, or what comes next | `docs/PRODUCT.md`, `docs/ROADMAP.md` |
| A structural or notable decision | a new ADR under `docs/adr/`, listed in `docs/adr/README.md` |

If the change makes an existing sentence false, fix that sentence. Stale docs
are worse than missing ones, because agents are told to trust them.

## Run the gates

```bash
make docs-check     # links resolve, published paths exist, no em dashes or listed phrases
```

`docs-links` also flags a documentation path cited in backticks that no longer
exists - prose goes stale that way silently. ADRs are exempt, since they record
files a decision deleted.

## When the website picks it up

labs.snowops.net publishes these docs and the catalog from the `stable`
branch. The website checks `stable` once a day and rebuilds when it has moved,
so a release reaches the site by the next day, or straight away if the
maintainer runs the website's sync by hand. A doc fix
merged to `main` reaches the site with the next release, not before.

## If you added, renamed, split or deleted a doc

The website's manifest names each published file by path. A rename with no
matching manifest edit drops the page from the site with no error.

1. Edit `scripts/docs-manifest.mjs` in the **snowopslabs-web** repository:
   `src` (path in this repo), `slug` (URL under `/docs`), `title`, `group`,
   `desc`.
2. Regenerate and verify there:
   ```bash
   npm run sync -- --local
   npm run lint
   npm run build
   ```
3. Commit the generated `content/` - it is committed, and the deployed site
   keeps the old data until you do.
4. Grep the site's own source for hardcoded links to the old slug:
   `src/config/site.ts` and `src/app/` both carry some.

`make docs-check` catches a missing manifest path from this side, but it cannot
know about a hardcoded link in the site's components. Check both.

## Writing style

Follow [docs/WRITING.md](../../../docs/WRITING.md). These pages are read by
users of the product, and the website publishes them unedited. The short
version:

- Lead with what the reader does, then the detail.
- Plain words, concrete facts, no hype or filler; no em dashes.
- One idea per sentence; no wave, task or PR numbers anywhere.
- Describe the project itself. Never mention outside work that led to a change
  or that a change is for.
- Every command shown must run as written.
- Mark anything not yet built as **Planned**. Never document an intention as if
  it ships, especially a security property.

Read your diff once as a user would. If a sentence would sound odd said out
loud to a colleague, rewrite it.

## Before you finish

- [ ] Every doc the change affects is updated in the same change.
- [ ] The text follows docs/WRITING.md.
- [ ] `make docs-check` passes.
- [ ] Manifest updated and the site rebuilt, if a doc moved.
- [ ] No task or ticket numbers introduced.
- [ ] Nothing mentions outside work: only the project, its problem and its fix.
