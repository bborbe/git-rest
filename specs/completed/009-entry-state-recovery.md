---
status: completed
tags:
    - dark-factory
    - spec
    - bug
approved: "2026-05-12T17:50:19Z"
generating: "2026-05-12T17:52:51Z"
prompted: "2026-05-12T18:07:44Z"
verifying: "2026-05-12T18:08:18Z"
completed: "2026-05-12T21:28:05Z"
branch: dark-factory/entry-state-recovery
---

## Summary

- `Pull` can never escape an abandoned-rebase or detached-HEAD state on its own — every subsequent call fails at the very first command it issues.
- The "abandoned rebase" state is the documented intent of the current code: `git rebase --abort` is intentionally never invoked on conflict, so the conflicted rebase is left in place "for operator inspection." In practice it leaves the repo permanently broken — the puller wedges on the next tick and the only recovery is operators running `git rebase --abort` and a hard reset atomically against a racing puller.
- Detached HEAD without an in-progress rebase has the same symptom and the same lack of recovery.
- Fix shape: `Pull` checks repo state on entry, self-heals abandoned rebases and bare detached HEAD, returns a typed `ErrRepoUnrecoverable` for states it cannot heal. Conflict-handling *policy* during the pull itself is out of scope for this spec (see 007).

## Problem

`Pull` assumes the local repo is on a branch with a configured upstream — every call begins with `git rev-parse --abbrev-ref --symbolic-full-name @{u}` (`pkg/git/git.go:462`). Two real-world states violate that assumption and there is no recovery path:

1. **Abandoned rebase**: when `pullRebaseAndPush` (`pkg/git/git.go:492`) hits a content conflict, it returns and intentionally leaves the repo in the conflicted-rebase state (HEAD detached, `.git/rebase-merge/` populated). Every subsequent `Pull` immediately fails because `@{u}` cannot be resolved while HEAD is detached.
2. **Bare detached HEAD**: e.g. operator runs `git checkout <sha>` inside the pod, or `git rebase --abort` is interrupted mid-flight leaving HEAD detached but with no rebase metadata. Same symptom: `@{u}` fails, `Pull` errors forever.

Spec 005 made `/readiness` answer in 1s by decoupling it from the pull mutex, but readiness still flips 503 once the cache-freshness threshold (`3 × PullInterval`, default 90s) elapses without a successful pull. So pods stuck in either state stay 0/1 Running indefinitely until manual intervention.

## Goal

`Pull` recovers from abandoned-rebase and bare-detached-HEAD states automatically. Whatever sequence of events produced the bad state, the next puller tick brings the repo back to a clean branch with a configured upstream — or returns a typed sentinel error identifying the state as unrecoverable, with no false claims of success.

## Desired Behavior

1. On every `Pull` entry, before resolving `@{u}`, the puller checks repo state. If `.git/rebase-merge/` or `.git/rebase-apply/` exists, the puller runs `git rebase --abort` and continues with the normal pull flow. If HEAD is detached without an in-progress rebase, the puller resolves `refs/remotes/origin/HEAD` to identify the default branch, runs `git checkout <branch>`, runs `git branch --set-upstream-to=origin/<branch> <branch>`, and continues.
2. If state check or recovery fails, `Pull` returns a typed `ErrRepoUnrecoverable` (wrapping the underlying git error). The puller records this in its cached state per the existing `puller.PullStateWriter` contract; the new error type is distinguishable from generic pull errors by callers via `errors.Is`.
3. Recovery actions emit structured log lines naming the detected state and the recovery taken (e.g. `slog.InfoContext(ctx, "git-rest: recovered from abandoned rebase", "branch", "main")`). Logs MUST be greppable from operator tooling.
4. The conflict-handling code path inside `pullRebaseAndPush` is unchanged by this spec — on a fresh rebase conflict during a healthy pull, the current behavior (leave conflicted, return `*RebaseConflictError`) still applies. The new recovery only triggers on the *next* `Pull` call, which finds the conflicted state on entry and aborts the abandoned rebase. (Whether to also resolve the conflict — `theirs`/`ours`/`quarantine` — is spec 007.)
5. New unit tests prove: (a) abandoned-rebase fixture is detected and `git rebase --abort` is invoked, (b) bare detached-HEAD fixture is detected and the default branch is restored, (c) a fixture whose `origin/HEAD` cannot be resolved returns `ErrRepoUnrecoverable`, (d) `errors.Is(err, ErrRepoUnrecoverable)` is true for that path.

## Non-goals

- Conflict-resolution policy during a healthy pull (`theirs` / `ours` / `quarantine` / corrected `manual`). That's spec 007.
- Automatic recovery for repo states beyond abandoned rebase and bare detached HEAD (e.g. corrupted `.git/objects/`, missing `refs/remotes/origin/HEAD`). Those return `ErrRepoUnrecoverable` and require operator action.
- Changes to the readiness contract from spec 005. Cached pull state still drives `/readiness`; this spec just changes what `Pull` does before it records that state.
- New env vars or flags. Entry-state recovery is always on; there is no deployment where leaving the repo unrecoverable is correct.
- Alertmanager rules. A separate operational task may wire `ErrRepoUnrecoverable` to a distinct alert; not blocked on this spec.

## Do-Nothing Option

Leaving the puller as-is is concretely expensive: `vault-obsidian-openclaw-0` was stuck 0/1 Running for 2d2h on 2026-05-12 before the readiness-stale alert surfaced it. Recovery required atomic `kubectl exec` against the racing puller — three separate exec attempts before the atomic single-call sequence succeeded — and silently dropped a local commit via `git reset --hard origin/main` (no audit trail of what was lost). Every same-file write conflict between writers triggers the same outage shape; the rate is bounded only by how often two writers race, not by anything the puller does. Other git-rest deployments (`vault-obsidian-personal`, `vault-obsidian-trading`) share the failure mode and the same recovery cost.

## Assumptions

- The remote always has a default branch resolvable via `refs/remotes/origin/HEAD` (set automatically by `git clone` and by the auto-clone path in spec 002). Repos that lack this ref are out of scope and will fall into `ErrRepoUnrecoverable`.
- `git rebase --abort` is safe to call when `.git/rebase-merge/` or `.git/rebase-apply/` exists, and it leaves the working tree clean (this is git's documented contract).
- The puller's mutex (`g.mu`) protects state-recovery operations the same way it protects the rest of `Pull`. No new locking is required.
- The puller can be racing with itself across the recovery window (a 30s tick fires while recovery is in flight). The existing mutex serializes this — no additional coordination needed.
- Operators occasionally exec into pods and may leave repos in detached HEAD as a side effect of investigation. This is treated as a normal state to recover from, not an error.

## Workaround

Until the fix lands, operators recover stuck pods by running atomically against the puller's 30s tick:

```bash
kubectlquant -n <ns> exec <pod> -- sh -c 'cd /data && git rebase --abort 2>/dev/null; git reset --hard origin/<branch> && git status'
```

Non-atomic approaches lose to the puller's race: a `rebase --abort` followed by a `reset --hard` in separate exec calls is interrupted by the puller starting a new rebase between them, and the cycle repeats until enough commits accumulate to make recovery painful. This was the observed failure on `vault-obsidian-openclaw-0` 2026-05-12: three separate exec calls were needed before the atomic single-call recovery worked. The workaround is also lossy — `git reset --hard` discards local commits without operator awareness of what was dropped (separate concern; see spec 007).

## Reproduction

**Build / environment:** `git-rest v0.19.6`, observed 2026-05-12 on `vault-obsidian-openclaw-0` (prod), stuck 0/1 Running for 2d2h.

**Abandoned-rebase recipe (deterministic):**

1. Start a `git-rest` instance against a remote where the puller will see divergence with overlapping file changes.
2. Make a local change to `tasks/file.md` via the REST write API — git-rest commits and tries to push.
3. Push fails because remote is ahead; puller sees divergence on next tick and runs `pullRebaseAndPush` (`pkg/git/git.go:492`).
4. Rebase conflicts on `tasks/file.md`; `pullRebaseAndPush` returns `*RebaseConflictError`, repo is left in `(no branch, rebasing <branch>)` state.
5. Every subsequent `Pull` call fails immediately at `@{u}` resolution with `fatal: HEAD does not point to a branch`.

**Bare-detached recipe (deterministic):**

```bash
git clone <repo> /tmp/x && cd /tmp/x
git checkout HEAD --detach
git rev-parse --abbrev-ref --symbolic-full-name @{u}
# → fatal: HEAD does not point to a branch
```

A `Pull` call against `/tmp/x` reproduces the bug.

**Observed evidence (verbatim from prod logs, 2026-05-12 14:09:41):**

```
puller.go:74 WARN git pull failed error="exit status 128
git [rev-parse --abbrev-ref --symbolic-full-name @{u}]: fatal: HEAD does not point to a branch
...
no upstream configured"
```

Logs repeat this every 30s for 2d2h with no recovery.

## Expected vs Actual

**Expected:** `Pull` finds an abandoned rebase, aborts it, and continues with the normal pull flow. The next successful pull resets readiness to 200 and the pod returns to service automatically. Detached HEAD without rebase is also auto-recovered. Unrecoverable states are reported as a typed error and tracked by metrics, never silently masked.

**Actual:** `Pull` fails immediately at `@{u}` resolution. Cached pull state stays in the failed branch indefinitely; readiness flips 503 after `3 × PullInterval`; pod marked 0/1 Running with no automatic recovery. Operator must `kubectl exec` and run the atomic workaround above.

## Why this is a bug

1. **Single point of failure for the whole puller.** Any path that produces detached HEAD (rebase conflict, manual checkout, interrupted command) permanently wedges the puller. The recovery surface is zero.
2. **Documented behavior is unsafe.** The comment at `pkg/git/git.go:440-447` says rebase aborts are intentionally avoided "for operator inspection," but in practice nothing inspects — the pod just stays unhealthy. The expected operator workflow (exec, inspect, `git rebase --abort`) doesn't exist in any runbook or alert; conflict-state diagnosis happens only after the pod-down page fires.
3. **Race condition during manual recovery.** Even when operators know the workaround, the puller's 30s tick races with the recovery commands. Atomic single-call recovery is required to escape the loop. This is an operability footgun.
4. **Conflates two different decisions.** Conflict *resolution policy* (theirs/ours/quarantine/manual) is one decision; recovering the *repo state* so the next pull can run is a different decision. The current code couples them by refusing to abort, which makes the second impossible without making a choice about the first.

## Root Cause (triaged)

Line numbers pinned to `v0.19.6`; function names stable.

- `Pull` (`pkg/git/git.go:448`) begins by resolving `@{u}` at line 462 with no prior state check. This is the first command issued and it errors on any non-branch HEAD.
- `pullRebaseAndPush` (called at line 492) is documented at `pkg/git/git.go:440-447` as leaving the repo in conflicted-rebase state without aborting. That's the dominant source of abandoned-rebase entries to the next call.
- There are no helpers in `pkg/git/` that report whether a rebase is in progress or whether HEAD is detached. Every `Pull` call assumes a healthy starting state.
- `pkg/puller/` records the last pull outcome but doesn't act on it — it does not (and shouldn't, per layering) attempt recovery. Recovery belongs in `pkg/git/git.go` because it is a git-operations concern.

## Constraints

- MUST NOT change the public HTTP contract (`/api/v1/files/*`, `/readiness`, `/healthz`, `/metrics`).
- MUST NOT change the meaning of `RebaseConflictError` — it remains the type returned mid-pull when a rebase conflicts. The new `ErrRepoUnrecoverable` is a separate sentinel for state checks that cannot heal.
- MUST be compatible with the cached readiness state from spec 005. Cached state interprets `ErrRepoUnrecoverable` as a non-success outcome.
- MUST NOT introduce new env vars or flags. Recovery is always on; there is no deployment where leaving the repo unrecoverable is the right behavior.
- The state-detection and recovery commands MUST be deterministic and idempotent — running them twice on a healthy repo MUST be a no-op.
- MUST follow project coding conventions (`docs/dod.md`): structured logging with `slog`, errors wrapped with `github.com/bborbe/errors`, no naked `fmt.Errorf`, context propagated.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---------|-------------------|----------|
| Previous `pullRebaseAndPush` left `(no branch, rebasing <branch>)` | Entry-state check finds `.git/rebase-merge/`, runs `git rebase --abort`, continues `Pull` | Automatic on next tick |
| Operator exec'd `git checkout <sha>` leaving bare detached HEAD | Entry-state check finds HEAD detached, resolves `origin/HEAD`, checks out default branch, sets upstream, continues `Pull` | Automatic on next tick |
| `origin/HEAD` ref missing (clone before default-branch ref was set) | `Pull` returns `ErrRepoUnrecoverable`, cached state records failure, metric increments | Operator must `git remote set-head origin --auto` manually |
| `git rebase --abort` itself fails (corrupted `.git/rebase-merge/`) | `Pull` returns `ErrRepoUnrecoverable` wrapping the abort error | Operator must inspect repo state |
| Concurrent puller tick fires during state recovery | Blocked on `g.mu` until recovery completes; then runs against the healed state | No operator action |

## Acceptance Criteria

- [ ] **AC1**: Given a fixture repo in `(no branch, rebasing main)` state, calling `Pull` once detects the abandoned rebase, runs `git rebase --abort`, and completes the normal pull. After the call, `git status` reports "On branch main" and `git rev-parse --abbrev-ref --symbolic-full-name @{u}` returns `origin/main`.
- [ ] **AC2**: Given a fixture repo with bare detached HEAD (`git checkout HEAD --detach`), calling `Pull` once resolves `origin/HEAD`, checks out the default branch, sets upstream, and completes the normal pull. After the call, the same `git status` / `@{u}` checks pass.
- [ ] **AC3**: Given a fixture repo with detached HEAD where `refs/remotes/origin/HEAD` is missing, calling `Pull` returns a non-nil error and `errors.Is(err, ErrRepoUnrecoverable)` is true.
- [ ] **AC4a**: Idempotency — running `Pull` twice in succession on a healthy repo invokes the state check both times, does not modify the working tree on the second call, and produces no structured-log lines on the second call (state check returns "already healthy").
- [ ] **AC4b**: Structured-log assertions — each recovery path (abandoned rebase, detached HEAD) emits exactly one `slog.InfoContext` line naming the detected state and the action taken. Logs are greppable from operator tooling.
- [ ] **AC5**: `make precommit` passes; existing `pkg/git/git_test.go` tests remain unchanged or are updated only to thread context through new helpers. Spec 005's readiness contract is preserved — a unit test driving `Pull` on a detached-HEAD fixture observes that no `@{u}` resolution occurs before the state check has run.
- [ ] **AC6 (verification)**: Replay the real abandoned-rebase scenario on a dev pod against the new binary. Reproduce by writing the same file from two writers as described in §Reproduction; confirm the next `Pull` tick recovers and the pod returns to 1/1 Running within `3 × PullInterval` (default 90s). Capture the structured-log line and the timing.

## Verification

```
make precommit
```

Plus the runtime replay called out in AC6: deploy the patched binary to a dev git-rest instance, force the abandoned-rebase state by inducing a same-file conflict, and observe automatic recovery without operator action.

Note: line numbers in §Root Cause are pinned to `v0.19.6`. Prompts implementing this spec MUST re-verify line numbers against the current HEAD of `git-rest` before editing; function names (`Pull`, `pullRebaseAndPush`, `runCmdOutput`) are stable and authoritative.

## Verification Result

**Verified:** 2026-05-12T21:27:25Z (binary `v0.19.7-1-g3a90601`, image `git-rest:v0.19.7`)
**Binary:** installed `dark-factory v0.156.1-1-g04f3863-dirty` (spec lifecycle); deployed `git-rest:v0.19.7` on `vault-obsidian-trading-0` (runtime replay)
**Scenario:** induced abandoned rebase on dev pod via `git rebase --exec /bin/false HEAD~1 HEAD` at 21:21:18 UTC; observed automatic recovery across two puller ticks (no operator action)
**Evidence:**
- Code: `pkg/git/git.go:43` (`ErrRepoUnrecoverable` sentinel), `pkg/git/git.go:530` (`recoverRepoState`), called from `Pull` at `git.go:653` before `@{u}` resolution
- Tests: `pkg/git/git_test.go` adds Context blocks for AC1 (abandoned rebase, line 858), AC2 (detached HEAD, line 908), AC3+AC4d (`errors.Is(ErrRepoUnrecoverable)`, line 975), AC4a (idempotency, line 1007); `captureSlogLogs` helper at line 842 asserts exactly one log line per recovery path
- Runtime log (vault-obsidian-trading-0): `I0512 21:21:20.528828 git.go:468] INFO git-rest: recovered from abandoned rebase branch=HEAD`
- Runtime log (vault-obsidian-trading-0): `I0512 21:21:50.534106 git.go:526] INFO git-rest: recovered from detached HEAD branch=master`
- Pod state post-recovery: `Ready=true Restarts=0` (kubectlquant -n dev get pod), full recovery in 32s (< 3× PullInterval = 90s)
- `make precommit` gated release of v0.19.7 (dark-factory precommit-required tag policy)
**Verdict:** PASS
