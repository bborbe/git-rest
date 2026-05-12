---
status: draft
tags:
    - dark-factory
    - spec
    - feature
---

## Summary

- Same-file concurrent writes between the local pod and the remote (e.g. multiple writers pushing to the same GitHub repo) are an expected use case for git-rest, not a misuse — but the puller has no way to resolve them automatically.
- The current code documents one policy ("manual: leave the conflicted rebase for operator inspection") and implements it incorrectly: nothing inspects, the pod just wedges (see spec 006 for the recovery half of the story).
- This spec adds an explicit `CONFLICT_RESOLUTION` env var with four values: `manual` (default, abort + log), `theirs` (remote wins), `ours` (local wins), `quarantine` (tag local commits to a branch, then take remote). Each policy is well-defined, observable, and chosen per deployment.
- For automation-dominant vaults (`vault-obsidian-openclaw`, `-personal`, `-trading`), the recommended policy is `quarantine` — nothing is silently lost, operators get a branch to inspect, and the puller never stays stuck on a same-file conflict.

## Problem

When the puller detects diverged history, `pullRebaseAndPush` (`pkg/git/git.go:492`) runs `git rebase origin/<branch>`. On a content conflict, the rebase exits with the working tree in a conflicted state and `Pull` returns `*RebaseConflictError`. Per the design comment at `pkg/git/git.go:440-447`, this is intentional: "git rebase --abort is NEVER invoked." The implicit expectation is that operators inspect the conflict and resolve it by hand.

In practice none of this happens:

- The conflicted state cascades into the abandoned-rebase failure mode (spec 006), so even *recovery* requires manual intervention before any inspection is possible.
- The puller has no logging that names the conflict, no metric that counts conflicts vs. other failures, and no Alertmanager rule that fires specifically on conflict (it's lumped under generic "sync stale").
- The "inspection" workflow is undocumented. Operators have no runbook that distinguishes conflict cases (which need a human decision) from operational cases (which just need a reset).
- For the dominant use case — vaults where every writer is automation and equivalence between commits is normal — the right answer is "drop the older or take the newer," not "wait for a human." Manual review of every conflict on a high-traffic vault is operationally infeasible.

The puller's design coupled *recovery* and *resolution* into a single non-choice. Spec 006 separates them (recovery becomes automatic). This spec makes resolution an explicit, configurable choice.

## Goal

Each git-rest deployment selects its conflict resolution policy via a single env var, with semantics that are well-defined, observable, and testable. The default (`manual`) preserves the current intent — abort the conflict, log it, wait for human action — but does so correctly (the repo is left clean and the pod stays serving). Other policies (`theirs`, `ours`, `quarantine`) provide automated resolutions with explicit data-loss vs. preservation tradeoffs. Operators choosing a policy understand exactly which data may be dropped or preserved.

## Desired Behavior

1. The git-rest binary reads a new env var `CONFLICT_RESOLUTION` at startup. Valid values: `manual` (default), `theirs`, `ours`, `quarantine`. Invalid values cause startup to fail with a clear error message naming the valid values. Empty/unset behaves identically to `manual`.

2. **`manual` (default)**: when `pullRebaseAndPush` would hit a content conflict, the puller runs `git rebase --abort` immediately, returns `*RebaseConflictError` to the caller, increments `git_rest_pull_outcome_total{result="conflict"}` and logs a structured warning naming the conflicted files. The repo is left in a clean state on the branch with upstream configured. The next `Pull` tick will hit the same conflict and produce the same outcome until either the remote or the local state changes. This is the *correct implementation* of the policy the current code claims to provide.

3. **`theirs`**: the puller runs `git rebase -X theirs origin/<branch>`. Conflicts on overlapping changes are auto-resolved in favor of the remote version. Local edits to conflicting files are silently dropped. The puller logs a structured info line naming the files whose local changes were dropped. `git_rest_pull_outcome_total{result="success"}` increments. Pull completes normally.

4. **`ours`**: the puller runs `git rebase -X ours origin/<branch>`. Conflicts auto-resolve in favor of the local version. Remote edits to conflicting files are silently dropped from the local working tree (they remain on the remote in their pre-rebase commits; the push afterwards overwrites only the conflict-resolved commits). Logging and metrics mirror `theirs`.

5. **`quarantine`**: before attempting the rebase, the puller captures the list of local commits ahead of upstream (`git log origin/<branch>..HEAD`). It creates a quarantine branch `refs/heads/quarantine/<ISO8601-timestamp>` pointing at the current HEAD (so the commits are preserved). It pushes that branch to the remote (`git push origin quarantine/<timestamp>:quarantine/<timestamp>`). Then it runs `git reset --hard origin/<branch>` to align local with remote, dropping the local commits from the main line. The puller logs a structured info line naming the quarantine branch and the commit shas it contains. `git_rest_pull_outcome_total{result="success"}` increments, plus a new `git_rest_quarantine_total` counter increments. No data is lost — operators can inspect, cherry-pick, or delete quarantine branches at their leisure.

6. All four policies preserve the entry-state recovery from spec 006 — recovery happens *before* the policy dispatches, so a conflicted repo from a previous tick is healed first, then the new tick attempts the rebase under the configured policy.

7. New unit tests cover each policy against a fixture conflict: assert the expected git state after `Pull` returns, assert the structured-log line, assert the counter increment.

## Non-goals

- Entry-state recovery (abandoned rebase, bare detached HEAD). That's spec 006, which this spec depends on — recovery runs *before* policy dispatch.
- Per-request policy override. `CONFLICT_RESOLUTION` is a deployment-wide invariant; there is no API to choose policy on a write-by-write basis.
- Automatic TTL-based cleanup of quarantine branches. Operators run `git branch -D quarantine/<ts>` manually when done. A future spec may add a sweeper; not blocked on this one.
- Recovering data already silently dropped by `theirs` / `ours` in the past. Once a commit is dropped without the quarantine policy active, it's gone (except reflog, which is best-effort and outside the puller's contract).
- Changes to the readiness contract from spec 005, or to the API surface (`/api/v1/files/*`).
- Alertmanager rules. A future operational task may wire `git_rest_pull_outcome_total{result="conflict"}` and `git_rest_quarantine_total` to dedicated alerts; not blocked on this spec.

## Do-Nothing Option

Each conflict in production today produces a multi-hour outage of the affected vault-sync pod. `vault-obsidian-openclaw-0` was stuck 0/1 Running for 2d2h on 2026-05-12 because no automated resolution exists. The cost compounds:

- Every same-file write race triggers an outage. The rate scales with vault write volume — not with bug frequency.
- Recovery requires operator pages, atomic kubectl scripting against the racing puller, and ad-hoc `git reset --hard` that silently drops local commits with no audit trail.
- Three production vaults (`openclaw`, `personal`, `trading`) share the failure mode; each conflict on each vault is a separate page.
- The "manual inspection" workflow documented in the current code does not exist in any runbook. Operators have never inspected a conflict — they just reset and move on.

Doing nothing keeps the puller's conflict behavior as a hidden tax on the on-call rotation, scaling with vault traffic, with no audit trail of dropped work.

## Assumptions

- The remote default branch is resolvable via `origin/HEAD` (same assumption as spec 006). Quarantine branches are pushed to the same remote; the remote MUST accept pushes to `quarantine/*` refs (no protected-branch rule blocks them). For GitHub this is the default.
- `git rebase -X theirs` and `git rebase -X ours` behave as documented by git: conflict-only file changes are resolved per strategy; non-conflicting changes are merged normally.
- Quarantine branches are not garbage-collected automatically. Operators run `git branch -D quarantine/<ts>` when they're done inspecting. (A separate operational concern: future spec may add a TTL-based cleanup; out of scope here.)
- The `CONFLICT_RESOLUTION` env var is a property of the *deployment*, not of the request — it cannot be overridden per-call. This is intentional: conflict policy is a vault-wide invariant.
- A misconfigured policy (e.g. `ours` on a vault with human writers) is recoverable by changing the env var and redeploying; data already dropped is not recovered, but the quarantine branches from `quarantine` policy provide an audit trail if that mode was used.
- The puller's mutex (`g.mu`) protects policy execution. No new locking.

## Workaround

Operators currently work around conflict cases by `kubectl exec`-ing into the pod and running policy-equivalent commands by hand:

- `manual` equivalent: `git rebase --abort` then wait for the conflict to clear naturally (i.e. another writer commits a resolution).
- `theirs` equivalent: `git rebase --abort && git reset --hard origin/<branch>` (drops local work; equivalent to `theirs` only when the dropped local commits would have lost the conflict anyway).
- `quarantine` equivalent: `git branch quarantine-manual-<date>; git reset --hard origin/<branch>`.

The workarounds suffer the same race-with-the-puller problem as spec 006's recovery workarounds, and they require operators to know which policy is appropriate for the vault.

## Reproduction

**Build / environment:** `git-rest v0.19.6`, observed 2026-05-12 on `vault-obsidian-openclaw-0`. The puller stayed stuck on the same conflict from approximately 2026-05-10 12:00 UTC through 2026-05-12 17:00 UTC (52h), with no automated resolution and no operator inspection (the conflict was discovered only when the readiness-stale alert fired).

**Recipe (deterministic):**

1. Start two writers against the same git-rest backend or two writers against the same GitHub repo (one via git-rest's REST API, one via direct `git push`).
2. Writer A writes `path/to/file.md` with content "A's version" and commits via REST. Writer B writes the same file with content "B's version" and commits directly to the remote.
3. The puller pulls, sees divergence on `path/to/file.md`, runs `git rebase`, hits a content conflict.
4. The repo is left in `(no branch, rebasing <branch>)` state. The conflict is on the same file. The puller errors and stays stuck (until spec 006 lands).

**Observed evidence (real prod scenario, 2026-05-12 17:00 UTC):**

The vault-obsidian-openclaw-0 conflict involved `tasks/analyse-trades-2026-05-11.md`. Both local and remote committed an update to that file with identical commit titles (`git-rest: update tasks/analyse-trades-2026-05-11.md`). Direct inspection showed both commits had similar but non-identical content. The repo was stuck for 2d2h. Recovery required:

```bash
kubectlquant -n prod exec vault-obsidian-openclaw-0 -- sh -c \
  'cd /data && git rebase --abort 2>/dev/null; git reset --hard origin/main'
```

— i.e. an ad-hoc `theirs`-equivalent recovery that silently dropped the local commit. Had `CONFLICT_RESOLUTION=quarantine` been set, the local commit would have been preserved on a quarantine branch and the puller would have continued without operator intervention.

## Expected vs Actual

**Expected:** Each deployment selects a conflict policy at deploy time. Conflicts auto-resolve per policy. Operators have observability (logs, metrics) and an audit trail (quarantine branches) sufficient to understand what was dropped or kept. The puller never wedges on the policy itself.

**Actual:** There is no policy choice — the implicit policy is "wedge until human intervenes," and the human-intervention workflow is undocumented. Conflicts indistinguishable from other sync failures in logs/metrics. Operators discover conflicts via stale-sync alerts and must reconstruct what happened from `git log`. No audit trail of dropped commits.

## Why this is a bug *and* a feature

This spec is partly a bug fix and partly a new feature:

- **Bug fix part**: `manual` policy currently leaves the repo in a conflicted-rebase state. That violates the documented intent (operator inspection) because the readiness probe flips 503 and the pod becomes unhealthy *before* the operator gets a chance to inspect. The correct `manual` implementation aborts the rebase, keeps the pod healthy, and surfaces the conflict via logs/metrics — not via pod failure.
- **Feature part**: `theirs`, `ours`, `quarantine` are new behaviors that don't exist today. They turn the puller from "stuck on conflict" into "explicit data-loss tradeoff per deployment."

## Root Cause (triaged)

Line numbers pinned to `v0.19.6`; function names stable.

- `pullRebaseAndPush` (`pkg/git/git.go:492`) runs `git rebase origin/<branch>` with no strategy option. On conflict it returns `*RebaseConflictError` without invoking `git rebase --abort`.
- The decision to leave the repo conflicted is documented at `pkg/git/git.go:440-447` but the operator workflow it assumes does not exist (no runbook, no alert, no inspection tooling).
- No env var or flag exists to select a different policy. The code path is hard-coded.
- The puller doesn't distinguish conflict failures from other pull failures in metrics or logs — both surface as generic `WARN git pull failed`.

## Constraints

- MUST NOT change the meaning of `*RebaseConflictError` — it remains the error returned when policy is `manual` and a conflict occurs. The four other outcomes (`theirs` / `ours` / `quarantine` success, or unrecoverable error from spec 006) use existing or new error types as appropriate.
- MUST NOT introduce new HTTP endpoints or change the API contract.
- MUST validate `CONFLICT_RESOLUTION` at startup — invalid values fail-fast with a message naming the four valid values. No silent fallback to default for typos.
- MUST emit one new Prometheus counter: `git_rest_quarantine_total` (incremented when `quarantine` policy creates a branch). Existing `git_rest_pull_outcome_total{result="conflict"}` counter from spec 005's outcomes set covers the `manual` case.
- Quarantine-branch creation MUST be atomic with the reset: if the push of the quarantine branch fails, the puller MUST NOT proceed with the reset — local commits stay where they are, and the puller falls back to `manual` behavior (abort, log, wait for next tick). This prevents silent data loss when the remote rejects the quarantine push.
- Quarantine branches follow the naming pattern `quarantine/<timestamp>` with timestamp in UTC and `:` replaced by `-` for git ref-name validity (e.g. `quarantine/2026-05-12T17-03-22Z`). Pattern frozen so operators can sort and clean up. Precision: seconds (no fractional seconds — branch creation rate is bounded by puller tick interval, so seconds resolution is unique enough).
- MUST follow project coding conventions (`docs/dod.md`): structured logging, errors wrapped, context propagated.
- MUST integrate cleanly with spec 006's entry-state recovery — recovery runs first, then policy dispatches.
- MUST NOT regress existing tests.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---------|-------------------|----------|
| `CONFLICT_RESOLUTION=manual`, content conflict | `git rebase --abort`, `*RebaseConflictError`, metric+log, repo clean | Operator inspects, manually resolves, restarts pod or waits for remote change |
| `CONFLICT_RESOLUTION=theirs`, content conflict | `rebase -X theirs` succeeds, structured log names dropped local edits, success metric | None needed; if dropped data mattered, audit logs |
| `CONFLICT_RESOLUTION=ours`, content conflict | `rebase -X ours` succeeds, log names dropped remote edits, success metric | Same |
| `CONFLICT_RESOLUTION=quarantine`, content conflict | Quarantine branch created and pushed, `reset --hard`, log+metric | Operator inspects branch when convenient, cherry-picks or deletes |
| `CONFLICT_RESOLUTION=quarantine`, quarantine push fails (remote rejects) | Falls back to `manual` behavior — abort, log explicitly naming the push failure, repo clean | Operator fixes the remote-side issue; next tick re-attempts |
| `CONFLICT_RESOLUTION=invalid_value` at startup | Process fails to start with a clear error message | Fix the env var, redeploy |
| Concurrent pull tick during quarantine | Blocked on `g.mu` until quarantine completes | Normal |

## Acceptance Criteria

- [ ] **AC1**: Startup with `CONFLICT_RESOLUTION` unset or `manual` runs the corrected `manual` policy: a fixture conflict produces an aborted rebase (working tree clean, on branch, with upstream), returns `*RebaseConflictError`, increments `git_rest_pull_outcome_total{result="conflict"}`, and logs a structured warning naming the conflicted files.
- [ ] **AC2**: Startup with `CONFLICT_RESOLUTION=theirs` runs `git rebase -X theirs origin/<branch>` against a fixture conflict; pull succeeds, working tree contains the remote version of the conflicted file, structured info log names the file whose local change was dropped, success metric increments.
- [ ] **AC3**: Startup with `CONFLICT_RESOLUTION=ours` runs `git rebase -X ours origin/<branch>` against the same fixture; pull succeeds, working tree contains the local version, log names dropped remote edits, success metric increments.
- [ ] **AC4**: Startup with `CONFLICT_RESOLUTION=quarantine` against a fixture conflict creates `refs/heads/quarantine/<ts>` pointing at the pre-reset HEAD, pushes it to the remote, runs `reset --hard origin/<branch>`, increments `git_rest_quarantine_total`, logs a structured info line naming the quarantine branch and the contained shas.
- [ ] **AC5**: Startup with `CONFLICT_RESOLUTION=garbage_value` fails immediately with an error message listing the valid values.
- [ ] **AC6**: Quarantine-push failure path: with a fixture whose remote rejects pushes to `quarantine/*`, `quarantine` policy falls back to `manual` semantics (rebase aborted, error returned, no `reset --hard` performed), preserving the local commits in place.
- [ ] **AC7**: New unit tests cover AC1-AC6. `make test` passes; existing pull/push tests remain unchanged except for the renamed/refactored conflict path.
## Verification

```
make precommit
```

Plus the runtime replay: deploy the patched binary to dev with `CONFLICT_RESOLUTION=quarantine`, force a same-file conflict between two writers, observe:

1. Automatic quarantine-branch creation visible via `git branch -a --remotes` against the dev remote.
2. Puller continues without operator action — no `kubectl exec` required.
3. Prometheus metrics show `git_rest_quarantine_total` increment and `git_rest_pull_outcome_total{result="success"}` increment.
4. `kubectlquant get pod` stays 1/1 Running through the conflict cycle.

Note: function-name references in §Root Cause (`pullRebaseAndPush`, `Pull`) are stable; line numbers may drift. Prompts implementing this spec MUST re-verify line numbers against the current HEAD of `git-rest` before editing.

## Note on spec scope

This spec deliberately mixes a bug fix (correct `manual` semantics — abort instead of leaving conflicted) with three new policies. Splitting would force an artificial sequencing where the bug fix lands first and the new policies follow, but they share the same code site (`pullRebaseAndPush`), the same Do-Nothing argument, the same observability surface (metrics, logs), and the same constraints. Implementing them together keeps the policy-dispatch refactor atomic and avoids a two-step intermediate state where the puller has a half-implemented dispatch with only one policy populated. The spec stays prompt-scoped because all four policies share the dispatch skeleton; only the per-policy branches differ.
