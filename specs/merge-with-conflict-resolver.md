---
status: draft
tags:
    - dark-factory
    - spec
    - feature
---

## Summary

- The puller currently uses `git rebase` for diverged history and leaves the repo permanently broken on any content conflict (see spec 009 for the recovery half).
- Replace rebase with `git merge`. Git's three-way merge auto-resolves non-overlapping changes — the most common case in a vault where multiple writers edit different files or different lines.
- For changes git cannot auto-resolve (true content conflicts on the same lines), delegate to a new `ConflictResolver` interface. This spec ships one implementation: `MarkerResolver` — keeps git's standard `<<<<<<<` / `=======` / `>>>>>>>` markers in the file, stages them as-is, and commits the merge. Both versions persist in the file; whoever next edits the file resolves the markers like any normal edit.
- No env var, no policy enum. The resolver is wired at `git.New(...)` construction. A future `GeminiResolver` (LLM-based three-way merge) plugs into the same interface via a separate spec — no `pkg/git/` change required for that.
- Both versions of conflicted content are preserved in the committed file content; nothing is silently dropped, no quarantine branches, no operator-toil recovery procedure.

## Problem

`pullRebaseAndPush` (`pkg/git/git.go:492`) calls `git rebase` on diverged history. Two failure shapes follow:

1. **Conflicts wedge the repo permanently.** On any content conflict, rebase exits with the working tree in a conflicted state and `pullRebaseAndPush` returns `*RebaseConflictError`. The documented intent (`pkg/git/git.go:440-447`) is "operator inspects and resolves," but in practice no operator inspects — they reset and move on, silently dropping the local commits. Spec 009 fixes the *recovery* half; this spec fixes the *resolution* half.
2. **Real conflicts hide in metrics.** Conflicts are indistinguishable from network errors or auth failures in the puller's outcome signal. There's no way to alert on "the puller is dropping data" vs. "the puller can't reach the remote."

Same-file concurrent writes are not a misuse; they're the load-bearing use case. The fix needs to handle them without losing data and without operator action.

## Goal

The puller resolves diverged history via `git merge`. Non-overlapping changes auto-resolve into a single clean merge commit. True conflicts (same-line edits) are delegated to a pluggable `ConflictResolver`. The default `MarkerResolver` preserves both versions in the file via standard git conflict markers and commits the merge. The puller never wedges on a conflict; data is never silently dropped; future resolver implementations (e.g. LLM-based merge) plug in via the same interface.

## Desired Behavior

1. The puller's diverged-history path calls `git merge origin/<branch>` instead of `git rebase`. Non-overlapping changes are auto-merged by git's three-way merge; a standard merge commit is produced; the merge commit is pushed; the operation logs `slog.InfoContext("git-rest: merge resolved cleanly", ...)`.

2. When `git merge` produces conflicts (working tree contains marker-annotated files), the puller invokes a `ConflictResolver.Resolve(ctx, conflictedPaths)` method. The interface is:

    ```go
    // ConflictResolver receives the list of paths git left in a conflicted state
    // after a merge. The resolver must produce final content for each path and
    // stage it (git add). After Resolve returns nil, the puller commits the
    // merge with a single merge commit.
    type ConflictResolver interface {
        Resolve(ctx context.Context, conflictedPaths []string) error
    }
    ```

3. This spec ships exactly one implementation: `MarkerResolver`. Its `Resolve` accepts the working tree as git left it (with `<<<<<<<` / `=======` / `>>>>>>>` markers), runs `git add` on each conflicted path, and returns nil. Both versions of conflicted content persist in the committed file. Implementation is ~10 lines plus tests.

4. The puller's `New` constructor takes a `ConflictResolver` parameter. `main.go` wires `&MarkerResolver{}` as the default. Future resolvers (e.g. `GeminiResolver`) are wired by changing `main.go` only — `pkg/git/git.go` does not change.

5. After a resolver returns successfully, the puller runs `git commit` with a structured merge-commit message that names the conflicted paths, e.g. `git-rest: merge with marker-preserved conflicts in tasks/foo.md, tasks/bar.md`. The message is greppable from logs and `git log` so operators can find conflicted commits later.

6. After a resolver returns an error (or fails to stage all conflicted paths), the puller runs `git merge --abort` to leave the repo in a clean state and returns a typed `ErrConflictResolutionFailed` (wrapping the resolver error). The pod remains healthy (per spec 005), but the sync state records the failure.

7. Two new Prometheus counters:
    - `git_rest_merge_outcome_total{result="clean|resolved|aborted"}` — `clean` when git auto-merged, `resolved` when the resolver wrote markers/output and the merge committed, `aborted` when the resolver errored and the merge was aborted.
    - `git_rest_conflict_paths_total` — count of files passed to `Resolve` across the lifetime of the pod (so operators can graph conflict frequency without scraping `git log`).

8. Unit tests cover: clean merge path, marker-resolver path (fixture with same-line conflict, assert committed file contains markers + correct merge commit message), resolver-error path (assert `git merge --abort` ran, repo clean, typed error returned).

## Assumptions

- The remote default branch is resolvable via `origin/HEAD` (same as spec 009). The merge uses the upstream tracking ref derived from the current branch, never hardcoded.
- `git merge` (not `git pull`) is used so the puller controls fetch and merge separately. Existing `pullFetchSHAs` (`pkg/git/git.go:399`) already performs the fetch; only the merge step changes.
- Git's standard conflict markers (`<<<<<<<`, `=======`, `>>>>>>>`) are acceptable content in committed files. For markdown vault files specifically, markers render as plain text in Obsidian — visible, ugly, but legible. The next human/agent edit resolves them naturally.
- `MarkerResolver` does not parse or modify marker syntax — it accepts whatever git's three-way merge produced. If git left a stray index entry (resolved-but-unstaged), the resolver's `git add` covers it.
- Concurrent puller ticks are serialized by `g.mu` (no change). A merge in progress with markers in the working tree is held under the mutex until the resolver returns.
- The interface is intentionally minimal — one method, one argument. Future resolvers needing more context (e.g. base/ours/theirs blobs from the git index) can read them inside `Resolve` via `git show :1:<path>` / `:2:<path>` / `:3:<path>` without changing the signature.

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

## Why this is a bug *and* a feature

**Bug:** the current code documents one policy ("operator inspects conflicted rebase") and implements it incorrectly — the readiness probe flips 503 (spec 005 made readiness fast, but still flips on stale state), so operators page-then-reset rather than inspect. The current code never delivers on its documented contract.

**Feature:** the `ConflictResolver` interface is new surface. It enables LLM-based merge (`GeminiResolver`), domain-specific resolution (e.g. JSON-aware merge), and the marker-preservation default that no current code provides.

## Root Cause (triaged)

Line numbers pinned to `v0.19.6`; function names stable.

- `pullRebaseAndPush` (`pkg/git/git.go:492`) runs `git rebase origin/<branch>` and bails on conflict. No conflict-resolution surface exists.
- The puller has no abstraction for "what to do with a conflicted file." Conflict handling is hardcoded as "fail."
- `git.New` (`pkg/git/git.go`, constructor) has no parameter for conflict behavior; everything is implicit.

## Constraints

- MUST NOT change the public HTTP contract (`/api/v1/files/*`, `/readiness`, `/healthz`, `/metrics`).
- MUST NOT change the meaning of `*RebaseConflictError` — it remains tied to the legacy rebase path if anything still uses it, but the new code path never returns it. The new `ErrConflictResolutionFailed` is a separate typed error for resolver failures.
- MUST preserve the entry-state recovery from spec 009 — `recoverRepoState` still runs at `Pull` entry, before the new merge path. A repo in a leftover merge state (`.git/MERGE_HEAD` present) is treated by `recoverRepoState` analogously to leftover rebase state: `git merge --abort` is called.
- MUST be compatible with the cached readiness state from spec 005. `MarkerResolver` success counts as a successful pull (cache refreshed). Resolver error counts as a failed pull (cache stale).
- MUST follow project coding conventions (`docs/dod.md`): structured logging, `errors.Wrap`, context propagated, no `fmt.Errorf`, no `context.Background()` in `pkg/`.
- The `ConflictResolver` interface lives in `pkg/git/conflict_resolver.go` (new file). `MarkerResolver` implementation lives in the same file or in `pkg/git/conflict_resolver_marker.go` (implementer's choice; both reasonable).
- `git.New` signature changes to accept a `ConflictResolver`. All call sites (production `main.go` + tests) update accordingly. Tests that don't exercise the merge path may pass `nil` and the code panics on first use — explicit, not silent. (Alternative: `nil` defaults to `MarkerResolver{}` — preferred for backward-compat in tests.)
- MUST NOT regress existing tests (`make precommit`).
- Merge commit message format is frozen by this spec: `git-rest: merge with marker-preserved conflicts in <path1>, <path2>, ...` (paths sorted; truncated at 200 chars with `...` if too many). Frozen so operators can grep `git log` reliably.

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

- [ ] **AC1**: Given a fixture with non-overlapping changes on the same branch (local: edit `local.md`, remote: edit `remote.md`), calling `Pull` produces one merge commit, both files present, `git_rest_merge_outcome_total{result="clean"}` increments by 1, no resolver invocation observed.

- [ ] **AC2**: Given a fixture with a same-line conflict on `tasks/foo.md` and `MarkerResolver` wired, calling `Pull` produces one merge commit whose tree contains `tasks/foo.md` with `<<<<<<<` / `=======` / `>>>>>>>` markers and both versions visible. The merge commit message starts with `git-rest: merge with marker-preserved conflicts in tasks/foo.md`. `git_rest_merge_outcome_total{result="resolved"}` increments by 1, `git_rest_conflict_paths_total` increments by 1.

- [ ] **AC3**: Given a fixture with a same-line conflict and a stub resolver that returns an error, calling `Pull` results in: the merge is aborted (`git status` reports clean working tree, on branch, with upstream), `Pull` returns a non-nil error such that `errors.Is(err, ErrConflictResolutionFailed)` is true, `git_rest_merge_outcome_total{result="aborted"}` increments by 1.

- [ ] **AC4**: `MarkerResolver` exists in `pkg/git/` and implements `ConflictResolver`. Its `Resolve` runs `git add` for each provided path. Unit test asserts the behavior end-to-end against a fixture with a conflicted file.

- [ ] **AC5**: `git.New` accepts a `ConflictResolver` parameter. `main.go` wires `&MarkerResolver{}` as the default. A second-implementation-in-test (any throwaway type satisfying the interface) verifies the wiring is genuinely pluggable — proof the future `GeminiResolver` slot exists.

- [ ] **AC6**: `make precommit` passes; existing tests unchanged where they don't exercise the conflict path; existing tests that pass a non-nil resolver are updated (or a `nil`-safe default is implemented, but explicit is preferred). Spec 005 and spec 009 contracts preserved (verify by running the spec 009 test suite against the new code).

- [ ] **AC7 (verification)**: Replay the real-world scenario on a dev pod with the new binary deployed. Induce a same-file conflict between two writers (one via `POST /api/v1/files/`, one via direct `git push` to the dev remote). Within `3 × PullInterval` (default 90s) observe: pod stays 1/1 Running, dev remote receives a merge commit whose message matches `git-rest: merge with marker-preserved conflicts in <path>`, the file in the merge commit contains both versions under marker fences, no operator action taken. Capture: `kubectlquant get pod` output (Running), `git log -1` of dev remote (merge commit message), file content via `git show` (markers + both versions).

## Verification

```
make precommit
```

Plus the runtime replay called out in AC7: deploy to a dev git-rest instance with the patched binary, induce a same-file conflict between two writers, observe automatic merge-with-markers resolution without operator action.

Note: line-number references in §Root Cause are pinned to `v0.19.6`. Prompts implementing this spec MUST re-verify line numbers against the current HEAD of `git-rest` before editing; function names (`Pull`, `pullRebaseAndPush`, `pullFetchSHAs`, `recoverRepoState`) are stable and authoritative.
