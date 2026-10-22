---
status: approved
spec: [020-commit-coalescing]
created: "2026-10-22T10:38:09Z"
queued: "2026-10-22T10:56:37Z"
---

<summary>
- The API reference no longer promises that a 200 means "written + committed"; it states that a write is written and staged and that a coalesced commit follows.
- The two stale sentences that describe a synchronous commit and push are removed, so the contract does not contradict itself.
- The deployment runbook documents the three write-coalescing flags, the three metrics, boot recovery, and the 10-second shutdown deadline.
- The runbook records the 2026-10-21 commit-rate baseline so the regression stays checkable after this spec is archived.
- The README's headline and flag table stop promising a commit and push on every write, and list the three new flags.
- The changelog carries a new Unreleased entry describing the change.
</summary>

<objective>
Update the write contract and the deployment runbook in the same change as the coalescing implementation, and add the changelog entry. The API contract must stop claiming a synchronous commit and push, and must state that 200 means written and staged.
</objective>

<context>
Read the approved spec at `specs/in-progress/020-commit-coalescing.md` — § Constraints (the `docs/api.md` / `docs/deployment.md` bullet and the CHANGELOG bullet) and AC12.

Read these files fully before editing:
- `docs/api.md` — the endpoint table (line ~16), the Write File section (line ~60), the Write File status table (line ~73), and the Delete File section (line ~82).
- `docs/deployment.md` — the remote-backed mode flag list (line ~24), Upgrades (line ~150), Monitoring (line ~155), the claim-ref and quarantine metric tables, and Operational notes (line ~190).
- `CHANGELOG.md` — the intro paragraph and the top released section (`## v0.33.2`).
- `README.md` — the intro sentence, the feature bullet list, and the Configuration flag table.

The behaviour being documented (already implemented by prompts 1 and 2):
- `POST /api/v1/files/{path}` writes the bytes to disk and stages them; no commit or push happens inside the request.
- A background flusher commits and pushes when the pending set has been quiet for `--write-coalesce-delay` (default `30s`) or when the oldest pending write reaches `--write-coalesce-max-delay` (default `120s`), whichever comes first.
- A pull cycle flushes pending writes before it resolves upstream.
- A write whose bytes match the file on disk stages nothing and produces no commit.
- `DELETE /api/v1/files/{path}` stays synchronous and flushes pending writes first.
- Claim refs stay synchronous; their 200 still means the ref is on the origin.
- On SIGTERM the process flushes with a 10-second deadline; a flush cut short logs `git-rest: flush deadline exceeded` and leaves the paths staged.
- On boot, index-staged paths are committed and pushed before the HTTP server starts, logging `git-rest: recovered pending writes`.
- Three metrics: `git_rest_write_flushes_total{outcome}` (`ok` / `error`), `git_rest_write_coalesced_total`, `git_rest_write_pending`.
- Three flags: `--write-coalesce` (bool, default `true`, env `WRITE_COALESCE`), `--write-coalesce-delay` (default `30s`, env `WRITE_COALESCE_DELAY`), `--write-coalesce-max-delay` (default `120s`, env `WRITE_COALESCE_MAX_DELAY`).
- Commit messages: one path keeps `git-rest: create <path>` / `git-rest: update <path>`; a batch of more than one path uses `git-rest: update <n> files`.
- Baseline: 12,490 `git-rest:` commits in the trailing 24 hours on `bborbe/obsidian-openclaw`, measured 2026-10-21.

Coding docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — entry format and the `## Unreleased` rule.
</context>

<requirements>
## 1. `docs/api.md` — the write contract

1.1 Endpoint table row. Replace:

```
| `POST` | `/api/v1/files/{path}` | Create or overwrite a file (auto-commits + pushes) |
```

with:

```
| `POST` | `/api/v1/files/{path}` | Create or overwrite a file (written and staged; coalesced commit + push) |
```

1.2 Write File section. Replace the sentence:

```
Create or overwrite a file. On success git-rest stages the change, commits, and pushes (if `--git-remote-url` is set).
```

with:

```
Create or overwrite a file. On success git-rest writes the content and stages it; a background flusher commits and pushes the accumulated writes within the coalescing window (`--write-coalesce-delay`, default `30s`, or `--write-coalesce-max-delay`, default `120s`). Set `--write-coalesce=false` to restore the previous synchronous write-commit-push behaviour. In local-only mode (no `--git-remote-url`) the commit stays on disk and no push is attempted. A write whose bytes match the file already on disk stages nothing and produces no commit.
```

1.3 Status table row. Replace:

```
| `200` | File written + committed |
```

with:

```
| `200` | File written and staged |
```

1.4 Commit message paragraph. Keep the existing single-path format sentence and append:

```
A coalesced batch spanning more than one path uses `git-rest: update <n> files`.
```

1.5 Delete File section. Replace:

```
Remove a file. Auto-commits + pushes.
```

with:

```
Remove a file synchronously: pending coalesced writes are flushed first, then the deletion is committed and pushed.
```

Do not change the status codes, the body limit, the claim-ref section, or the error shapes.

## 2. `docs/deployment.md` — the runbook

2.0 Remote-backed mode intro. Replace:

```
Pass a remote URL + SSH key. git-rest clones on startup and pushes on every write.
```

with:

```
Pass a remote URL + SSH key. git-rest clones on startup and pushes coalesced write bursts (see `### Write coalescing` below), or immediately on a delete.
```

2.1 Add a new `### Write coalescing` subsection immediately after the remote-backed mode flag block, containing a short paragraph plus this table:

| Flag | Env | Default | Meaning |
|------|-----|---------|---------|
| `--write-coalesce` | `WRITE_COALESCE` | `true` | Coalesce file writes into one commit and one push per window. `false` restores the synchronous write-commit-push path on every request. |
| `--write-coalesce-delay` | `WRITE_COALESCE_DELAY` | `30s` | Flush after the pending set has been quiet for this long. |
| `--write-coalesce-max-delay` | `WRITE_COALESCE_MAX_DELAY` | `120s` | Flush when the oldest pending write is this old, even under a continuous write stream. |

State in the paragraph that a write returns once its content is on disk and staged, that a pull cycle flushes pending writes before merging, and that `--write-coalesce=false` is the rollback lever.

2.2 Add a boot-recovery and shutdown paragraph to the same subsection:

```
On SIGTERM the process flushes and pushes pending writes before exiting, bounded by a 10 s deadline; a flush cut short logs `git-rest: flush deadline exceeded` and leaves the paths staged. On boot, paths left staged by a crash are committed and pushed before the HTTP server starts listening, logging `git-rest: recovered pending writes` with the path count.
```

2.3 Extend the Monitoring section. It currently ends with a single `Key metrics:` sentence and contains no table — add a new metrics table for these three series immediately after that sentence, in the same shape as the per-feature tables under `### Claim refs`, `### Quarantine backlog` and `### Dirty working tree rescue`:

| Series | Meaning |
|--------|---------|
| `git_rest_write_flushes_total{outcome}` | Counter over coalesced flush attempts, `outcome` = `ok` or `error`. Pre-initialised to zero at process start. |
| `git_rest_write_coalesced_total` | Counter over writes absorbed into a coalesced batch. A three-write burst that lands as one commit increments it by 3. |
| `git_rest_write_pending` | Gauge: paths currently staged and waiting for a coalesced commit. Returns to 0 after a successful flush. |

2.4 Under Monitoring, record the baseline:

```
Baseline: 12,490 `git-rest:` commits in the trailing 24 hours on `bborbe/obsidian-openclaw`, measured 2026-10-21. Coalescing is expected to reduce this by roughly the burst ratio; a return toward the baseline means the flusher is not firing.
```

The literal string `12,490` must appear.

2.5 Operational notes. The bullet beginning `- **Auto-commit noise**: every write produces a commit.` is now false. Replace its body with an accurate statement: with coalescing enabled a burst of writes produces one commit and one push per window; with `--write-coalesce=false` every write produces a commit as before.

## 3. `CHANGELOG.md`

3.1 `CHANGELOG.md` has no `## Unreleased` heading. Insert one immediately after the intro paragraphs (after the semver bullet list and its blank line) and before `## v0.33.2`.

3.2 Add one `feat:` bullet under `## Unreleased`. It must contain the lowercase substring `coalesc` (the acceptance criterion greps case-sensitively) and must describe the implemented behaviour, not the verification. Use wording along these lines:

```
- feat: Coalesce file writes into one commit and one push per window. A POST now writes and stages its content and returns without a commit or push; a background flusher commits and pushes when the pending set has been quiet for `--write-coalesce-delay` (default 30s) or when the oldest pending write reaches `--write-coalesce-max-delay` (default 120s). Unchanged content still stages nothing, a pull flushes pending writes before merging, SIGTERM flushes with a 10 s deadline, and boot recovery commits paths left staged by a crash. Adds `git_rest_write_flushes_total{outcome}`, `git_rest_write_coalesced_total` and `git_rest_write_pending`; `--write-coalesce=false` restores the synchronous write-commit-push path.
```

Do not add a version heading and do not bump any version.

## 4. `README.md` — the flag reference and the headline contract

`README.md` is the repo's other canonical flag table and states the old synchronous contract twice. Both statements must be corrected and the three new flags added, or the README advertises a contract the service no longer honours.

4.1 Intro sentence. Replace:

```
Lightweight REST API server for reading and writing files in a git repository. Auto-commits and pushes on writes, periodic pull keeps the local clone in sync.
```

with:

```
Lightweight REST API server for reading and writing files in a git repository. A write is written and staged; a background flusher commits and pushes a burst of writes as one commit, and a periodic pull keeps the local clone in sync.
```

4.2 Feature bullet. Replace:

```
- Auto `git add + commit + push` on every write/delete
```

with:

```
- Coalesced writes — `git add + commit + push` runs in a background flusher once per window (default 30s quiet, 120s ceiling), not inside the request; deletes stay synchronous
```

4.3 Configuration table. Immediately after the `--pull-interval` row, add these three rows:

```
| `--write-coalesce` | `WRITE_COALESCE` | no | `true` | Coalesce file writes into one commit and one push per window |
| `--write-coalesce-delay` | `WRITE_COALESCE_DELAY` | no | `30s` | Flush after the pending set has been quiet this long |
| `--write-coalesce-max-delay` | `WRITE_COALESCE_MAX_DELAY` | no | `120s` | Flush when the oldest pending write is this old |
```

## 5. Self-check

Before finishing, re-run every command in the verification block at the end of this prompt and confirm each passes, then walk AC12 (section 6) against the change: the `written and staged` positive assertion, both removed strings, the flags and the `12,490` baseline in `docs/deployment.md`, the three flags and both corrected sentences in `README.md`, and the CHANGELOG entry sitting under `## Unreleased`.

## 6. Reference — acceptance criterion this prompt implements (operator-executed, not run in the container)

Copied from the spec. Do not put it in the verification block and do not attempt to run it.

**AC12 — the contract and the runbook are updated in the same change.** `grep -c 'coalesc' docs/api.md` returns ≥ 1; `grep -c 'written and staged' docs/api.md` returns ≥ 1; `grep -c 'File written + committed' docs/api.md` returns 0; `grep -c 'stages the change, commits, and pushes' docs/api.md` returns 0; `grep -c 'write-coalesce' docs/deployment.md` returns ≥ 1; `grep -c '12,490' docs/deployment.md` returns ≥ 1; `grep -c 'coalesc' CHANGELOG.md` returns ≥ 1; `awk '/^## /{s=$0} /coalesc/{print s; exit}' CHANGELOG.md` prints `## Unreleased`; `git diff origin/master --name-only` lists all three files. The positive `written and staged` assertion is load-bearing alongside the two negatives. **This prompt extends AC12 with a fourth file, `README.md`** — the spec's AC12 enumerates only `docs/api.md`, `docs/deployment.md` and `CHANGELOG.md`, but `README.md` carries the same stale contract and the repo's other canonical flag table, so leaving it untouched would ship a README advertising a contract the service no longer honours.
</requirements>

<constraints>
- Documentation only: do not change any Go source, test, mock, metric or flag. `README.md`, `docs/api.md`, `docs/deployment.md` and `CHANGELOG.md` are the only files in scope.
- `docs/api.md` must state the new contract positively ("written and staged") AND remove both stale synchronous-push statements. Adding a coalescing sentence beside the old lines leaves the contract contradicting itself and fails AC12.
- `CHANGELOG.md` gains `## Unreleased`; the entry must land under that heading, not under a released section, and must contain the lowercase substring `coalesc`.
- `docs/deployment.md` must contain the literal `write-coalesce` and the literal `12,490`.
- Do NOT mention or perform release, image-publish, cluster-apply or `VERSION`-pin steps anywhere. Those are operator-only and out of scope.
- Do NOT commit — dark-factory handles git.
- The container has no view of `origin/master` and no operator tooling: do not add `git`, `docker`, `kubectl`, `dark-factory` or `scripts/*` commands to the verification block.
</constraints>

<verification>
Run these from the repo root. All must pass.

```
grep -q 'coalesc' docs/api.md
grep -q 'written and staged' docs/api.md
! grep -qF 'File written + committed' docs/api.md
! grep -qF 'stages the change, commits, and pushes' docs/api.md
grep -q 'write-coalesce' docs/deployment.md
grep -q '12,490' docs/deployment.md
grep -q 'write-coalesce' README.md
! grep -qF 'Auto-commits and pushes on writes' README.md
! grep -qF 'Auto `git add + commit + push` on every write/delete' README.md
grep -q 'coalesc' CHANGELOG.md
awk '/^## /{s=$0} /coalesc/{print s; exit}' CHANGELOG.md | grep -qx '## Unreleased'
make precommit
```

`make precommit` must exit 0 (format, generate, test, lint, license).
</verification>
