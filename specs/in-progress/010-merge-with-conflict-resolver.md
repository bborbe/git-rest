---
status: generating
tags:
    - dark-factory
    - spec
    - bug
approved: "2026-05-12T20:51:43Z"
generating: "2026-05-12T20:51:44Z"
branch: dark-factory/merge-with-conflict-resolver
---

## Summary

- The puller uses `git rebase` for diverged history and leaves the repo permanently broken on any content conflict. Spec 009 fixes the recovery half (puller no longer wedges on bad state); this spec fixes the resolution half (real resolution instead of "fail").
- Replace rebase with `git merge`. Git's three-way merge auto-resolves non-overlapping changes — the most common case in a vault where multiple writers edit different files or different lines.
- For changes git cannot auto-resolve, the puller delegates to a pluggable resolver seam. The default behavior preserves both versions in the file via git's standard `<<<<<<<` / `=======` / `>>>>>>>` markers and commits the merge. Whoever next edits the file resolves the markers like any normal edit.
- The seam is intentionally minimal so future resolvers (e.g. LLM-based three-way merge) plug in without changing the puller's code path.
- Net effect: no operator action, no silently dropped commits, no side branches. Conflicts are visible in the file content itself.

## Problem

`pullRebaseAndPush` (`pkg/git/git.go:492`) calls `git rebase` on diverged history. Two failure shapes follow:

1. **Conflicts wedge the repo permanently.** On any content conflict, rebase exits with the working tree in a conflicted state and `pullRebaseAndPush` returns `*RebaseConflictError`. The documented intent (`pkg/git/git.go:440-447`) is "operator inspects and resolves," but in practice no operator inspects — they reset and move on, silently dropping the local commits. Spec 009 fixes the *recovery* half; this spec fixes the *resolution* half.
2. **Real conflicts hide in metrics.** Conflicts are indistinguishable from network errors or auth failures in the puller's outcome signal. There's no way to alert on "the puller is dropping data" vs. "the puller can't reach the remote."

Same-file concurrent writes are not a misuse; they're the load-bearing use case. The fix needs to handle them without losing data and without operator action.

## Goal

The puller resolves diverged history via `git merge`. Non-overlapping changes auto-resolve into a single clean merge commit. True conflicts (same-line edits) are delegated to a pluggable `ConflictResolver`. The default `MarkerResolver` preserves both versions in the file via standard git conflict markers and commits the merge. The puller never wedges on a conflict; data is never silently dropped; future resolver implementations (e.g. LLM-based merge) plug in via the same interface.

## Desired Behavior

1. The puller's diverged-history path uses `git merge origin/<branch>` instead of `git rebase`. Non-overlapping changes are auto-merged by git's three-way merge; a standard merge commit is produced; the merge commit is pushed; the operation emits one structured log line summarising the clean-merge outcome.

2. When `git merge` produces conflicts (working tree contains marker-annotated files), the puller delegates to a pluggable resolver seam. The resolver receives the list of conflicted paths, must produce final content for each, and stages them. After the resolver returns success, the puller commits the merge with a structured message naming the conflicted paths so operators can find them via `git log`.

3. The default resolver preserves both versions in the file via git's standard conflict markers. Specifically: the resolver accepts the working tree as git left it (with `<<<<<<<` / `=======` / `>>>>>>>`) and stages the marker-annotated files as-is. Both versions persist in the committed file.

4. The seam is implemented so a future resolver (e.g. LLM-based three-way merge) plugs in by wiring a different implementation at process startup, with no change to the merge code path inside the puller. This spec ships only the marker-preserving default; richer resolvers are out of scope (see Non-goals).

5. On resolver failure (resolver returns an error or fails to stage all conflicted paths), the puller aborts the merge so the repo is left in a clean state on the branch with upstream configured, and surfaces a typed sentinel error so callers and Prometheus alerts can distinguish resolver failure from generic merge / push errors. The pod remains healthy (per spec 005); the sync state records the failure.

6. Two new Prometheus counters:
    - `git_rest_merge_outcome_total{result="clean|resolved|aborted"}` — `clean` when git auto-merged, `resolved` when the resolver succeeded and the merge committed, `aborted` when the resolver errored and the merge was aborted.
    - `git_rest_conflict_paths_total` — total count of conflicted paths passed to the resolver across the lifetime of the pod (so operators can graph conflict frequency without scraping `git log`).

7. Merge commit message format is frozen for greppability: `git-rest: merge with marker-preserved conflicts in <path1>, <path2>, ...` (paths sorted; the message is truncated at the boundary git accepts without rejection if the list is very long). Frozen so operators can grep `git log` reliably across pods and time.

## Assumptions

- The remote default branch is resolvable via `origin/HEAD` (same as spec 009). The merge uses the upstream tracking ref derived from the current branch, never hardcoded.
- Fetch and merge are separate steps in the puller (the existing fetch path is unchanged). Only the merge step changes.
- Git's standard conflict markers (`<<<<<<<`, `=======`, `>>>>>>>`) are acceptable content in committed files. For markdown vault files specifically, markers render as plain text in Obsidian — visible, ugly, but legible. The next human/agent edit resolves them naturally.
- The default marker-preserving resolver does not parse or modify marker syntax — it accepts whatever git's three-way merge produced and stages it. If git left a stray index entry (resolved-but-unstaged), staging covers it.
- Concurrent puller ticks are serialized by the existing puller mutex. A merge in progress with markers in the working tree is held under the mutex until the resolver returns.
- The resolver seam is intentionally minimal — one operation, one input (list of conflicted paths). Future resolvers needing more context (e.g. base/ours/theirs blobs from the git index) can read them via `git show :1:<path>` / `:2:<path>` / `:3:<path>` without expanding the seam.

## Workaround

Without this fix, operators recover from conflicts by `kubectl exec` + manual `git rebase --abort && git reset --hard origin/<branch>` (silently drops local commits — same as spec 009's workaround). This is what happened on `vault-obsidian-openclaw-0` 2026-05-12: the local commit for `tasks/analyse-trades-2026-05-11.md` was discarded by the `reset --hard` recovery, no audit trail of the dropped work. The workaround is lossy by design; there is no way to preserve both versions without committing the markers.

## Reproduction

**Build / environment:** `git-rest v0.19.6` (or v0.19.7 with spec 009 entry-state recovery). Observed 2026-05-12 on `vault-obsidian-openclaw-0` (prod).

**Recipe (deterministic):**

1. Start a git-rest instance against a remote with default `PullInterval=30s`.
2. Writer A: `POST /api/v1/files/tasks/foo.md` with content "version A".
3. Writer B (direct push to remote, simulating another git-rest instance or a human): commit "version B" to `tasks/foo.md` on the same branch and push.
4. Puller detects divergence on next tick → `git rebase origin/<branch>` conflicts on `tasks/foo.md`.
5. Repo enters `(no branch, rebasing main)` state; puller wedges (see spec 009 for symptom).

**Expected with this spec:** Step 4 becomes `git merge origin/<branch>`. The merge fails to auto-resolve `tasks/foo.md`. The puller calls `MarkerResolver.Resolve`, which `git add`s the file (markers intact). The puller commits the merge with message `git-rest: merge with marker-preserved conflicts in tasks/foo.md`. Both Writer A's and Writer B's content live in the file under marker fences. Push succeeds (it's a fast-forward of the local merge commit). No operator action.

**Observed evidence (real prod scenario, 2026-05-12 17:00 UTC):**

The 52h outage of `vault-obsidian-openclaw-0` involved exactly this pattern on `tasks/analyse-trades-2026-05-11.md`. Local and remote both committed updates with identical commit titles (`git-rest: update tasks/analyse-trades-2026-05-11.md`) and similar-but-non-identical content. The current code rebased, conflicted, and stayed stuck. With this spec, the puller would have merged, called the resolver, and committed a file containing both versions under conflict markers.

## Non-goals

- `GeminiResolver` (LLM-based three-way merge). Defined as a future spec, plugs into the same `ConflictResolver` interface. The interface is intentionally designed to accept it without further change.
- Configurable per-deployment resolver selection (env var, flag, etc.). Resolver choice is a wire-up decision in `main.go`. If a deployment needs a different resolver, it builds a different binary or branches `main.go`. Per-deployment policy enums are explicitly out of scope.
- TTL-based cleanup of marker-conflicted files. If markers persist in a file because nobody edits it, they stay. Operators may grep for `<<<<<<<` in vault files as a scheduled cleanup; that's a separate operational concern.
- Changes to the conflict semantics during a fresh-merge attempt — the spec only changes what happens *after* git's three-way merge runs. The merge strategy itself remains git's default recursive merge.
- Changes to the `/readiness` contract from spec 005, the entry-state recovery from spec 009, or the API surface (`/api/v1/files/*`).
- Alertmanager rules. A future operational task may wire `git_rest_merge_outcome_total{result="aborted"}` to a dedicated alert; not blocked on this spec.

## Do-Nothing Option

Every conflict in production today produces a multi-hour vault-sync outage. The `vault-obsidian-openclaw-0` 2d2h outage on 2026-05-12 cost ~52h of vault desync, an on-call page, manual atomic kubectl recovery, and a silently dropped local commit (no audit trail). With spec 009 alone (entry-state recovery), the pod won't wedge — but the puller will still hit the same conflict on every tick and never resolve it, so the vault stays desynced.

Doing nothing means:
- Conflicts continue to cost on-call attention.
- Local commits continue to be silently dropped during ad-hoc `reset --hard` recovery.
- The puller's design remains a hidden tax that scales with vault write volume.
- The future `GeminiResolver` (which makes LLM-based merge possible) has no place to plug in — every deployment needs custom branching in `pullRebaseAndPush`.

## Why this is a bug

The current code documents one policy ("operator inspects the conflicted rebase") and fails to deliver on it. The readiness probe flips 503 once the sync state goes stale (spec 005 made it fast but didn't change the contract), so the actual operator workflow is "page → reset → silently drop local commits," not "inspect → manually resolve." No runbook exists for the documented inspection workflow because nobody has ever used it. Conflicts are a normal load-bearing event in a multi-writer vault, and the current code treats them as fatal — that's the bug.

The pluggable resolver seam introduced as part of the fix is an implementation detail, not a new feature. It exists so the fix is testable (stub resolvers in unit tests) and forward-compatible (richer resolvers can replace the default later without touching the merge code path). It is not the primary purpose of this spec.

## Root Cause (triaged)

Line numbers pinned to `v0.19.6`; function names stable.

- `pullRebaseAndPush` (`pkg/git/git.go:492`) runs `git rebase origin/<branch>` and bails on conflict. No conflict-resolution surface exists.
- The puller has no abstraction for "what to do with a conflicted file." Conflict handling is hardcoded as "fail."
- The puller's constructor in `pkg/git/git.go` has no parameter for conflict behavior; everything is implicit.

## Constraints

- MUST NOT change the public HTTP contract (`/api/v1/files/*`, `/readiness`, `/healthz`, `/metrics`).
- MUST NOT change the meaning of `*RebaseConflictError` if anything still references it. The new typed sentinel for resolver failures is distinct.
- MUST preserve the entry-state recovery from spec 009. A repo left in an in-progress merge state (analogous to abandoned rebase) is handled by entry-state recovery via `git merge --abort` on entry — so leftover merge state from a killed process self-heals on the next tick.
- MUST be compatible with the cached readiness state from spec 005. A successful merge (clean or resolved) counts as a successful pull. A resolver error or an aborted merge counts as a failed pull.
- MUST follow project coding conventions (`docs/dod.md`): structured logging, `errors.Wrap`, context propagated, no `fmt.Errorf`, no `context.Background()` in `pkg/`.
- The puller's constructor signature changes to accept a resolver. The default at startup is the marker-preserving resolver; tests that don't exercise the merge path receive the same default to keep test wiring simple.
- MUST NOT regress existing tests (`make precommit`).
- Merge commit message format is frozen by this spec: `git-rest: merge with marker-preserved conflicts in <path1>, <path2>, ...` (paths sorted). Frozen so operators can grep `git log` reliably across pods and time.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---------|-------------------|----------|
| Clean three-way merge (non-overlapping changes) | `git merge` produces a merge commit automatically; `git_rest_merge_outcome_total{result="clean"}` increments; push succeeds | None needed |
| Same-line conflict, `MarkerResolver` active | Markers left in file by git, resolver `git add`s, puller commits merge with structured message, `result="resolved"` counter increments | None needed; operator/agent resolves markers on next file edit |
| Same-line conflict, resolver returns error | `git merge --abort` runs, repo clean, `ErrConflictResolutionFailed` returned, `result="aborted"` counter increments | Operator inspects (typically only a custom resolver would error; `MarkerResolver` cannot meaningfully fail) |
| Leftover `.git/MERGE_HEAD` from previous tick (e.g. process killed mid-merge) | `recoverRepoState` (spec 009) detects and runs `git merge --abort` on entry, then normal merge runs | Automatic |
| Push of merge commit fails (remote rejected, race with another push) | Puller logs, next tick re-fetches and re-merges — possibly into a new conflict that the resolver handles again | Automatic |
| Concurrent puller tick during merge | Serialized via `g.mu` (no change) | Normal |

## Acceptance Criteria

- [ ] **AC1**: Given a fixture with non-overlapping changes on the same branch (local: edit `local.md`, remote: edit `remote.md`), one pull cycle produces one merge commit, both files present in the merge tree, `git_rest_merge_outcome_total{result="clean"}` increments by 1, the resolver is not invoked.

- [ ] **AC2**: Given a fixture with a same-line conflict on `tasks/foo.md` and the default marker-preserving resolver wired, one pull cycle produces one merge commit whose tree contains `tasks/foo.md` with `<<<<<<<` / `=======` / `>>>>>>>` markers and both versions visible. The merge commit message starts with `git-rest: merge with marker-preserved conflicts in tasks/foo.md`. `git_rest_merge_outcome_total{result="resolved"}` increments by 1, `git_rest_conflict_paths_total` increments by 1.

- [ ] **AC3**: Given a fixture with a same-line conflict and a stub resolver that returns an error, one pull cycle results in: the merge aborted (working tree clean, on branch, upstream configured), pull returns a non-nil error matching the typed sentinel via `errors.Is`, `git_rest_merge_outcome_total{result="aborted"}` increments by 1.

- [ ] **AC4**: The marker-preserving resolver exists, satisfies the resolver seam contract, and is unit-tested end-to-end against a fixture with a conflicted file. Test asserts the file is staged after `Resolve` returns and the working tree no longer reports the path as conflicted.

- [ ] **AC5**: The puller's constructor accepts a resolver. Production wiring uses the marker-preserving resolver by default. A second, throwaway implementation used only in tests satisfies the same seam and is exercised in AC3 — this validates the seam is genuinely pluggable for future resolvers.

- [ ] **AC6**: `make precommit` exits 0. Spec 005 (readiness contract) and spec 009 (entry-state recovery) test suites pass unchanged against the new code.

## Verification

```
make precommit
```

Plus the runtime replay: deploy the patched binary to a dev git-rest instance, induce a same-file conflict between two writers (one via the REST API, one via direct `git push` to the dev remote). Within `3 × PullInterval` (default 90s) observe:

1. Pod stays 1/1 Running through the conflict cycle.
2. Dev remote receives a merge commit whose message matches `git-rest: merge with marker-preserved conflicts in <path>`.
3. The file in that merge commit contains both versions under `<<<<<<<` / `=======` / `>>>>>>>` markers.
4. No operator action taken; recovery is automatic.

Capture: `kubectlquant get pod` (Running), `git log -1` from dev remote (merge commit message), file content via `git show` (markers + both versions), pod logs for the structured merge log lines.

Note: function-name references in §Root Cause are stable (`Pull`, `pullRebaseAndPush`, `pullFetchSHAs`, `recoverRepoState`); line numbers are pinned to `v0.19.6` and may drift. Prompts implementing this spec MUST re-verify line numbers against the current HEAD of `git-rest` before editing.
