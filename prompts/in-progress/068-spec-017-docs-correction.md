---
status: approved
spec: [017-bug-readiness-decoupled-from-sync-outcome]
created: "2026-10-21T15:45:28Z"
queued: "2026-10-21T15:52:46Z"
branch: dark-factory/bug-readiness-decoupled-from-sync-outcome
---

# Correct the readiness contract in the docs and the rebase-conflict alert

<summary>
- The API reference describes readiness as "can this pod serve", not "did the last sync win".
- The API reference's positive contract block now states the repo-path readability rule.
- The README's one-line readiness claim matches the new contract.
- The deployment guide says sync failures do not remove the pod from the Service.
- The deployment guide's conflict-handling note no longer claims readiness turns 503 or names the conflict path.
- The rescue note no longer claims readiness recovers only after a successful push.
- The rebase-conflict alert points the operator at the pod log and drops the readiness claim.
- A changelog entry records the behaviour change.
- Verification-doc diagnostics that lean on readiness are known collateral and are deliberately left out of this fix.
</summary>

<objective>
Rewrite every artifact that still asserts the old readiness semantics — `/readiness` as a function of pull success, pending pushes, working-tree cleanliness or conflict state — so the API reference, the README, the deployment guide, the rebase-conflict alert and the changelog all describe the repo-path readability contract.
</objective>

<context>
There is no repo-root `CLAUDE.md` in this worktree — do not look for one. The project conventions are restated in `<constraints>` below.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` heading, `fix:` prefix, one bullet per logical change
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — durable-doc expectations

Files to read in full before editing:

- `docs/api.md` — the endpoint table row for `/readiness` and the whole `## Readiness` section
- `README.md` — the feature bullet list
- `docs/deployment.md` — the `### Health & readiness` bullet, the `**Conflict handling**` bullet under `## Operational notes`, and the `**A rejected rescue push.**` paragraph
- `helm/templates/alerts.yaml` — the `VaultObsidian*RebaseConflict` alert description
- `CHANGELOG.md` — the top of the file; there is no `## Unreleased` section today and the newest release heading is `## v0.30.1`

**Verified facts — do not re-derive, do not contradict:**

- The new contract is: `/readiness` returns `200 OK` with body `ok` when the HTTP server is listening and the served repository path is readable; it returns `503 Service Unavailable` with body `repo path unreadable` when that path is missing or unreadable. Sync outcomes — rejected push, failed fetch, merge conflict — do not affect it.
- The sync signal is unchanged: the puller logs `git pull failed`, and `git_rest_git_operation_errors_total{operation=...}` is still incremented. `helm/templates/alerts.yaml` already alerts on both the pull-failure and rebase-conflict series; no alert expression changes.
- The exact stale lines that must be rewritten are (baseline grep output, one line each):
  - `docs/api.md:20` — table row: `Readiness probe (checks git status + pending pushes)`
  - `docs/api.md:114-116` — the three-bullet positive contract (`git status` succeeds / Working tree is clean / No local commits pending push)
  - `docs/api.md:118` — `a pod flips unready while a write is in-flight or if a push is stuck`
  - `README.md:33` — `Health and readiness endpoints (readiness reflects git status)`
  - `docs/deployment.md:134` — `removes the pod from the Service while git operations are in-flight or pushes are pending`
  - `docs/deployment.md:184` — `readiness reports 503 with the conflict path (...), and require human inspection`
  - `docs/deployment.md:196` — `Readiness returns to ready within one pull interval after the push succeeds.`
  - `helm/templates/alerts.yaml:31` — `readiness 503, requires human resolution. Exec into pod, check /readiness for conflict path, ...`
- `docs/api.md` has exactly one `## Readiness` heading today; keep exactly one.
- The acceptance grep is case-insensitive and covers the four files. None of the replacement text below matches any banned phrase; do not reintroduce one while editing.

</context>

<requirements>

## 1. `docs/api.md` — endpoint table row

Replace:

```
| `GET` | `/readiness` | Readiness probe (checks git status + pending pushes) |
```

with:

```
| `GET` | `/readiness` | Readiness probe (serves while the repository path is readable) |
```

## 2. `docs/api.md` — the `## Readiness` section

Replace from the `## Readiness` heading up to (not including) the next `## ` heading with (the outer four-backtick fence is only for this prompt — the file itself uses ordinary Markdown):

````markdown
## Readiness

```bash
curl http://localhost:9090/readiness
```

Returns `200 OK` with body `ok` when the process can serve its API: the HTTP
server is listening and the served repository path is readable.

Returns `503 Service Unavailable` with body `repo path unreadable` when the
served repository path is missing or unreadable. Used for Kubernetes readiness
probes.

Readiness does not depend on the outcome of the most recent sync. A rejected
push, a failed fetch, or a merge conflict leaves the probe at `200`; those
failures are reported through the pod log (`git pull failed`) and through
`git_rest_git_operation_errors_total{operation=...}`.
````

Requirements:

- Keep the heading exactly `## Readiness` (one occurrence in the file).
- The section must contain the word `readable` (the acceptance check greps for it inside this section).
- Do not claim readiness depends on git status, working-tree cleanliness, pending pushes, in-flight operations or conflict state.
- The section contains exactly one bash code fence; the outer four-backtick fence above is prompt formatting only and must not be copied into the file.

## 3. `README.md` — feature bullet

Replace:

```
- Health and readiness endpoints (readiness reflects git status)
```

with:

```
- Health and readiness endpoints (readiness reflects whether the served repository path is readable)
```

## 4. `docs/deployment.md` — `### Health & readiness` bullet

Replace:

```
- `readinessProbe` → `/readiness`: removes the pod from the Service while git operations are in-flight or pushes are pending.
```

with:

```
- `readinessProbe` → `/readiness`: removes the pod from the Service only when the served repository path is missing or unreadable. Sync failures — a rejected push, a failed fetch, a merge conflict — do not affect readiness; they stay visible in the pod log and in `git_rest_git_operation_errors_total`.
```

## 5. `docs/deployment.md` — `**Conflict handling**` bullet

Replace the sentence fragment `readiness reports 503 with the conflict path (`last pull failed: rebase conflict at <path>`), and require human inspection (`kubectl exec` + manual resolve, or PVC reset for recoverable churn).` so the bullet reads:

```
- **Conflict handling**: git-rest auto-recovers from divergence (local ahead AND remote ahead, no content conflict) by rebase + push within one PullInterval. Real content conflicts during rebase leave the repo in conflicted state and require human inspection (`kubectl exec` + manual resolve, or PVC reset for recoverable churn); `/readiness` stays `200` because the pod can still serve, and the conflict is visible in the pod log and through the `VaultObsidian<Realm>RebaseConflict` alert.
```

Do not claim the conflict path appears in a readiness body.

## 6. `docs/deployment.md` — `**A rejected rescue push.**` paragraph

Replace the final sentence:

```
Readiness returns to ready within one pull interval after the push succeeds.
```

with:

```
The pod stays ready throughout — readiness tracks whether the served repository path is readable, not whether a push has succeeded — and the rescue branch appears on the remote once the push succeeds.
```

Do not keep the old sentence in any form.

## 7. `helm/templates/alerts.yaml` — rebase-conflict alert description

Replace the `description` of the `VaultObsidian*RebaseConflict` alert (the one whose expression selects `operation="rebase",conflict="true"`) with:

```yaml
    description: 'git-rest in {{ $name }} hit a rebase content conflict — requires human resolution. Exec into pod, inspect the pod log for the conflict path, resolve manually or reset PVC.'
```

- Remove both the `readiness 503` claim and the `check /readiness for conflict path` instruction.
- Do not change the alert `name`, `expression`, `for`, `labels` or any other alert in the file.

## 8. `CHANGELOG.md` — unreleased entry

There is no `## Unreleased` section today. Add one immediately after the preamble (after the SemVer bullet list) and above `## v0.30.1`, containing exactly one bullet:

```
- fix: `/readiness` now reports whether the pod can serve — the HTTP server is listening and the served repository path is readable — instead of whether the most recent sync succeeded. A rejected push, a failed fetch or a merge conflict no longer removes the pod from its Service; those failures remain visible in the pod log and in `git_rest_git_operation_errors_total`.
```

Do not touch any released section and do not add a second bullet.

## 9. Self-check before finishing

Run every command in `<verification>` from the repo root. Confirm the negative grep prints `0` for each of the four files, the `## Readiness` heading still occurs exactly once and its section contains `readable`, and the changelog's first version heading is `## Unreleased`.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT change any `.go` file, `Makefile`, `go.mod`, `go.sum` or `helm/templates/statefulset.yaml` in this prompt.
- Do NOT change the `/readiness` URL, its `200`/`503` status-code contract, or the `/healthz` contract.
- Do NOT introduce a new metric, alert, route, flag or configuration field. The four documents describe the existing signal only.
- Do NOT edit `docs/verifying-specs.md`. Its `/readiness`-based diagnostics (`modify/delete` and drain recipes) are known collateral that the spec deliberately excludes from this fix; leave the file untouched and do not "fix" it here.
- Do NOT edit `specs/`.
- Do NOT add a scenario file under `scenarios/`.
- Do NOT touch `docs/dod.md` or any other file outside the five named in the requirements (`docs/api.md`, `README.md`, `docs/deployment.md`, `helm/templates/alerts.yaml`, `CHANGELOG.md`).
- Keep the Markdown table, heading levels and alert YAML structure intact — the acceptance checks parse them by heading and by grep.
- No trailing whitespace; keep lines reasonably wrapped like the surrounding prose.
- `/readiness` is unauthenticated by design — the documented 503 body names the failure (`repo path unreadable`) and never a host path.
</constraints>

<verification>
Run from the repo root. No git commands are needed.

Negative check — every file must print `0` (the command exits non-zero when all counts are zero; that is expected, read the printed counts):

```bash
grep -Eic 'pending pushes|push is stuck|readiness reflects git status|pushes are pending|check /readiness for conflict path|git status. succeeds|Working tree is clean|local commits pending push|readiness reports 503 with the conflict path|readiness 503, requires human resolution|returns to ready within one pull interval|flips unready|git operations are in-flight' docs/api.md README.md docs/deployment.md helm/templates/alerts.yaml
```

Expected: a `0` count for every file. grep emits files in filesystem order, so ignore the order and check only the four counts:

```
docs/api.md:0
README.md:0
docs/deployment.md:0
helm/templates/alerts.yaml:0
```

Positive check — the `## Readiness` block survives and now states the readability contract:

```bash
grep -c '^## Readiness' docs/api.md
sed -n '/^## Readiness/,/^## /p' docs/api.md | grep -Eic 'readable'
```

The first must print `1`; the second must print a value `≥1`.

The corrected contract text is present (each `grep -n` must print at least one match):

```bash
grep -n 'repo path unreadable' docs/api.md
grep -n 'does not depend on the outcome of the most recent sync' docs/api.md
grep -n 'repository path is readable' README.md
grep -n 'missing or unreadable' docs/deployment.md
grep -n 'stays `200`' docs/deployment.md
grep -n 'tracks whether the served repository path is readable' docs/deployment.md
grep -n 'inspect the pod log for the conflict path' helm/templates/alerts.yaml
```

The changelog section is new and is the topmost version heading:

```bash
grep -n '^## Unreleased' CHANGELOG.md
awk '/^## /{print; exit}' CHANGELOG.md
awk '/^## /{sec=$0} sec=="## Unreleased" && /^- fix:/{n++} END{print n+0}' CHANGELOG.md
```

The first must print one match; the second must print `## Unreleased`; the third must print `1` — an empty `## Unreleased` section passes the first two checks while still failing `docs/dod.md`'s "entry under `## Unreleased`" criterion.

The excluded file was not touched by this prompt. A `/readiness` grep only proves the string still exists, not that the file is unedited, so assert the file's digest instead:

```bash
sha256sum docs/verifying-specs.md | cut -d' ' -f1
```

This must print `8bb19b1a6e80faa9b4de89ec774c394d9a5592e2ce0c2e5cc62deab2c58c79cb` — the file's digest at prompt-writing time. If `sha256sum` is unavailable, `shasum -a 256 docs/verifying-specs.md | cut -d' ' -f1` prints the same value.
</verification>
