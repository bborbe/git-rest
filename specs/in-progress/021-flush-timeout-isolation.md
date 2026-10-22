---
status: prompted
approved: "2026-10-22T14:28:37Z"
generating: "2026-10-22T14:29:24Z"
prompted: "2026-10-22T14:37:42Z"
branch: dark-factory/flush-timeout-isolation
---

## Summary

- `020-commit-coalescing` put the pending-write flush at the top of `Pull`, so every pull cycle now pushes over the network before it does anything else.
- That flush runs under the same context as the rest of the pull. When the push is rejected as a lost race (`(fetch first)`), `pushWithRetry` makes three attempts, and against the contended vault remote each attempt costs seconds of git network work — the dev log shows 13 s and 17 s between consecutive attempts inside one cycle.
- Those attempts consume the 60 s pull deadline, so the dirty-tree rescue that runs later in the same `Pull` receives an expired context and its `git add -A` is killed. The rescue fails, the tree stays dirty, and the next pull fails identically.
- The flush gets a deadline bounded to a third of the pull budget, leaving two thirds for the merge and the rescue — the split the rollback evidence actually supports.

## Problem

Deployed `v0.34.0` to nuke dev on 2026-10-22T15:57+02:00 and observed the writer make no progress at all.

From 14:03:52 the same cycle repeated for ten minutes:

```
WARN git-rest: flush pending writes before pull failed
  error="... ! [remote rejected] main -> main (cannot lock ref 'refs/heads/main':
  is at 8747ee45... but expected 886064c1...)"

WARN git-rest: flush pending writes failed
  error="... ! [rejected] main -> main (fetch first)"

WARN git-rest: retry merge failed upstream=origin/main
  error="CONFLICT (content): Merge conflict in tasks/PR Review ..."

WARN git pull failed error="signal: killed\ngit [add -A]: ..."
  git.go:2282 createRescueCommit -> git.go:2333 rescueDirtyTree
  -> git.go:1259 mergeWithDirtyTreeRescue -> git.go:1210 pullMergeAndPush
```

Over that window `grep -c 'flushed pending writes'` returned **0** and `grep -c 'flush pending writes ... failed'` returned **8** — the pod pushed nothing it staged. Writes returned 502 while the write lock was held across the retrying push.

The rollback to `v0.33.2` is the control. On the **same** PVC, with the **same** dirty tree, the old build logged within 40 s of starting:

```
INFO rescue branch pushed branch=rescue/20261022T141250Z files=10 paths="scratch/...,tasks/Dark Factory ..."
INFO git-rest: merged diverged history upstream=origin/main
INFO startup git pull succeeded
```

So the rescue logic is sound; only its time budget changed. `v0.33.2` has no flush in that path, so `git add -A` runs with the whole budget and completes. Under `v0.34.0` the flush spends it first.

**Where the time goes.** `pushWithRetry` logs `push retry attempt=N` at 14:03:52, 14:04:39, 14:04:52, 14:05:09 and 14:05:39. `sleepPushJitter` is only 100–500 ms (`git.go:447`), so the time is the git operation itself — a push against the contended remote, plus the fetch and merge the retry path performs before trying again. The 13 s and 17 s gaps are intra-cycle; the 47 s gap spans cycles and is not evidence about a single attempt.

## Reproduction

Smallest reproduction, in-process against a real bare remote:

1. Create a bare remote; install a `pre-receive` that sleeps longer than the test's pull context and rejects `refs/heads/main`, allowing `refs/heads/rescue/*`.
2. `POST` a file through the write path so the pending set is non-empty, and leave the working tree dirty on that same path.
3. Advance the remote with a commit touching **that same path**, so the local branch is diverged and the merge will conflict.
4. Call `Pull` with a 10 s context.

**Expected:** the flush is cut at ~3.3 s, the merge conflicts, the rescue runs on the remaining ~6.7 s, and `refs/heads/rescue/*` exists on the remote.

**Actual on `v0.34.0`:** the flush consumes the whole 10 s on retries; the rescue's `git add -A` is killed; `refs/heads/rescue/*` does not exist and `Pull` returns a merge error. On nuke dev this repeated for ten minutes with 0 successful flushes.

## Goal

A pull cycle in which the flush cannot complete still performs the dirty-tree rescue and pushes the rescue branch, within the pull's own deadline. The flush is bounded; the merge and rescue are not.

## Assumptions

- The pull context carries a deadline in production. It does: `pkg/puller/puller.go:63` installs `--pull-timeout` (default 60 s) around `Pull`, and the startup pull is separately bounded by `syncOnStartupTimeout = 60 * time.Second` (`main.go:209`). Both are deadlines on the context `Pull` receives.
- A rejected flush is recoverable: the staged work survives on disk, so a flush cut short by its own deadline loses nothing that a later flush cannot push.
- **The rescue needs roughly two thirds of a 60 s budget.** Measured on the rollback control: `v0.33.2` completed rescue, merge and push within ~40 s of a 60 s startup budget. That is why the flush is capped at a **third** and not at half — a half-budget cap would leave the rescue 30 s for work measured at 40 s, and the reserve would be smallest exactly when the remote is contended and the rescue push is slowest.

## Non-goals

- Removing the write lock from the flush, or making the flush asynchronous with respect to `Pull`.
- Raising `--pull-timeout` in the deployment. That hides the starvation instead of fixing it.
- A separate `--flush-timeout` flag. `--pull-timeout` is the operator's knob; a second one adds config, docs and test surface for no consumer.
- The push-rejection rate itself, which is the parent task's subject.

## Acceptance Criteria

- [ ] **AC1 — a flush that cannot finish still lets the rescue land.** The fixture needs **all four** preconditions; any one missing means the unfixed tree satisfies the AC, so the fixture proves nothing:
  1. **A non-empty pending set.** `flushPendingLocked` returns immediately when `len(g.pending) == 0` (`git.go:890-892`), so a tree dirtied by a direct disk write consumes no budget. The fixture must `POST` through the write path.
  2. **Remote divergence on the same path.** `syncWithUpstream` routes `remoteSHA == baseSHA` (local ahead) straight to `pushWithRetry` with **no rescue** (`git.go:2210-2215`). The remote must carry a commit that the local branch does not.
  3. **An overlapping dirty path.** A merge over a dirty tree succeeds when the remote commit touches a different file. The remote-side commit must touch the same path as the pending write, so the merge conflicts and the rescue is attempted.
  4. **A sleep that outlasts the pull budget, not the flush deadline.** On the unfixed tree there is no flush deadline at all, so a sleep sized against it proves nothing. The `pre-receive` must sleep past the test's whole 10 s context (not the production `--pull-timeout` of 60 s, which the in-process test does not use), and it must reject `refs/heads/main` while **allowing** `refs/heads/rescue/*` (`git.go:2343-2349`).

  Drive `Pull` in-process with an explicit 10 s context rather than booting the binary — `syncOnStartup` runs with a hardcoded 60 s ceiling (`main.go:208-236`) and would resolve the fixture state before the 10 s pull ever runs. Assertions: the captured log carries `rescue branch pushed`; `git -C "$BARE" rev-list --count refs/heads/rescue/*` returns ≥ 1; and the captured log carries **no** `git [add -A]` line inside a `signal: killed` error. Evidence shape: log-line match + numeric ref count + negative log match.
- [ ] **AC2 — the flush is cut off by its own deadline, not by exhausting retries.** In the AC1 run, `grep -c 'git-rest: flush deadline exceeded before pull' run.log` returns ≥ 1. ⚠️ The trailing `before pull` is load-bearing: the existing shutdown path logs `git-rest: flush deadline exceeded` (`main.go:470`), a prefix of the new string, so a prefix grep matches the SIGTERM path and passes vacuously. Evidence shape: numeric stdout.
- [ ] **AC3 — a healthy remote is unchanged.** With a normal remote and a path that **already exists**, `POST` new content then run a pull: `git -C "$BASE/work" log origin/main --format='%s' -- tasks/x.md | grep -cE '^git-rest: (create|update) tasks/x\.md$'` returns `1` (the flush landed before the merge, once), and `grep -c 'flush deadline exceeded before pull' run.log` returns `0`. The alternation is required: a `POST` to a path that does not exist produces `git-rest: create <path>` (message built at `git.go:806-808`), so a `^…update…$`-only assertion fails a correct build. Evidence shape: numeric stdout + negative log match.
- [ ] **AC4 — the reserve is numeric and parsed from the log.** `Pull` logs one line per cycle carrying all three values: `git-rest: pull budget pull_start=<RFC3339Nano> pull_deadline=<RFC3339Nano> flush_deadline=<RFC3339Nano>`. Parse them and assert `flush_deadline - pull_start <= (pull_deadline - pull_start) / 3 + 1ms`. A build that sets `flush_deadline = pull_deadline - 1ns` satisfies "strictly less" and still starves the rescue; only the third-budget assertion rejects it. Evidence shape: numeric stdout.
- [ ] **AC5 — coalescing off is untouched.** With `--write-coalesce=false`: `grep -c 'git-rest: flushed pending writes' run.log` returns `0`, `grep -c 'flush deadline exceeded before pull' run.log` returns `0`, and two `POST`s to one path give `git -C "$BASE/work" log --format='%s' -- tasks/x.md | grep -c '^git-rest:'` = 2. Evidence shape: numeric stdout.
- [ ] **AC6 — the regression is locked by a test that fails on the current tree.** A test in `pkg/git` installs **exactly** the AC1 fixture — all four preconditions — and asserts the rescue ref exists on the remote (`rev-list --count refs/heads/rescue/*` ≥ 1). It must not assert merely that `Pull` returned no error, and it must not rely on the `pull budget` log line for its pre-fix failure: the failure must come from the missing rescue, which is what the fix actually changes. Record the test name and its captured failure output against the pre-fix tree in `## Verification`. Evidence shape: test name + captured failure.
- [ ] **Post-Deploy (Rung-2):** AC7 — on nuke dev, over ten minutes of ordinary operation the writer both pushes and never starves its rescue: `kubectlnukedev -n dev logs vault-obsidian-openclaw-0 --since=10m | grep -c 'git-rest: flushed pending writes'` returns ≥ 1; and `kubectlnukedev -n dev logs vault-obsidian-openclaw-0 --since=10m | grep -c 'git \[add -A\]'` returns `0`. The negative is scoped to the **rescue**, matching AC1, because the Goal bounds the flush and not the post-merge push — a push killed at the parent deadline logs `signal: killed\ngit [push]: …`, which a correct build may still produce. ⚠️ The positive clause needs traffic: an idle vault writes nothing and yields 0 flushes on a correct build, so the ten-minute window must contain active writes (the parent task's agent fleet supplies them; if the window is quiet, extend it rather than reading 0 as a failure). Baseline to beat, measured on the failed deploy: 0 flushes and 8 failures over ten minutes.
  - `deploy_check:` `kubectlnukedev -n dev get pod vault-obsidian-openclaw-0 -o jsonpath='{.spec.containers[*].image}' | sed -E 's#.*:##'`
  - `deploy_target:` `$(git describe --tags --abbrev=0 origin/master)`

  The Post-Deploy marker is the first token of this bullet by requirement — `spec-verifier` extracts it with a literal grep for the marker string. The `deploy_check` reads the image tag from the pod spec rather than scraping `/metrics`, because that form is verified to work on this deployment and needs no `wget` inside the container.

## Verification

- **Container-executable:** `make precommit`, then the AC1–AC6 harness. AC1–AC5 are **not** covered by `go test ./pkg/git -run TestSuite` — `TestSuite` produces Ginkgo output and no booted-binary `run.log`. Define AC1–AC5 as in-process `pkg/git` tests that capture their log to a file, and AC6 as the named regression test. Exact commands: `go test ./pkg/git -run 'TestFlush' -count=1 -v`, where every new test is named `TestFlush…` so the one pattern covers AC1–AC6. The `docs/deployment.md` grep in the bullet below runs in the same container-executable rung.
- **Operator-executable:** AC7 on nuke dev only, after the image is published and `BRANCH=dev make apply` runs. Prod is not touched by this spec.
- **Docs:** `docs/deployment.md` § *Write coalescing* must gain the bounded-flush rule and the `git-rest: flush deadline exceeded before pull` string beside the existing SIGTERM deadline paragraph. `grep -c 'flush deadline exceeded before pull' docs/deployment.md` returns ≥ 1.

## Desired Behavior

- **DB1 — the flush deadline is at most a third of the remaining pull budget.** On entering `Pull`, if the context has a deadline, the flush runs under a child context expiring no later than `pull_start + (pull_deadline - pull_start)/3`. If the context has no deadline, no child deadline is installed, the `pull budget` line records `flush_deadline=none`, and behaviour is unchanged.
- **DB2 — the flush cannot starve the rest of the pull.** Whatever the flush does — succeed, fail, or time out — `Pull` continues to `syncWithUpstream` and the dirty-tree rescue on the parent context, which still has its remaining two thirds.
- **DB3 — a healthy flush is unchanged.** A flush that completes inside the window still runs before the merge, still pushes the pending batch, and adds no log line beyond DB4's budget line.
- **DB4 — the budget is observable.** `Pull` logs one line per cycle carrying `pull_start`, `pull_deadline` and `flush_deadline`, and a flush cut short by its own deadline logs `git-rest: flush deadline exceeded before pull`.

## Constraints

- `flushPendingLocked` must still run **before** the merge: merging into a tree this process dirtied is what the rescue exists to avoid.
- The write lock is still held across the flush. This spec changes how long the flush may run, not whether it holds the lock.
- `pushWithRetry`'s three attempts stay. This bounds the whole flush, not the individual attempts.
- Claim-ref operations remain synchronous and outside this path.
- The startup pull (`syncOnStartupTimeout`, 60 s) and the periodic pull (`--pull-timeout`, default 60 s) must both be covered, which is why the rule keys on `ctx.Deadline()` rather than on the flag.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| `--pull-timeout=0` | No deadline on the context; no derived flush deadline; pre-fix behaviour | Debug log line `flush deadline skipped: no pull deadline`; nothing to recover |
| Flush killed after the remote accepted the ref | The pending set is **retained** and HEAD is reset (`flushPendingLocked` clears `g.pending` only on success, `git.go:920-922`; on push error it calls `restorePendingIndexLocked`, `git.go:916`), so the next cycle re-commits and re-pushes the same content — landing a second commit with an identical tree if the first push had in fact landed | `git -C /data log origin/main --oneline -2` shows two commits with the same `git-rest:` subject; harmless, no action. This window widens under the third-budget cap, so it is expected, not a regression |
| Remote very slow but not rejecting | Flush cut at one third; the batch stays staged and is retried next cycle | `grep -c 'flush deadline exceeded before pull'` rises while `git-rest: flushed pending writes` still rises over time; if only the former rises for an hour, raise `--pull-timeout` |
| Rescue push rejected | `rescueDirtyTree` logs `rescue push failed, leaving repo for inspection` | `git -C /data for-each-ref refs/heads/rescue` lists the ref; operator inspects and pushes by hand |
| Flush deadline fires every cycle, nothing lands | No write ever reaches the remote | AC7 fails (`flushed pending writes` = 0 over ten minutes); treat as a regression, do not promote |

## Security / Abuse Cases

No new input surface. The change bounds an existing internal operation; it adds no endpoint, flag, or credential path.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Deadline derivation, the `pull budget` log line, the `flush deadline exceeded before pull` string, and the `docs/deployment.md` § *Write coalescing* rule | DB1, DB3, DB4 | AC2, AC3, AC4, AC5 | — |
| 2 | Preserve the merge and rescue budget: `Pull` continues on the parent context after any flush outcome | DB2 | AC1, AC6 | 1 |
| 3 | Regression test with the four-precondition fixture and the rescue-ref assertion | DB2 | AC1, AC6 | 2 |

## Do-Nothing Option

Doing nothing leaves `v0.34.0` undeployable: on dev it pushed nothing for ten minutes and returned 502s while holding the write lock. The parent task's goal (cut the vault commit rate) cannot be reached without a working coalescing build, so the alternative to this fix is abandoning `020-commit-coalescing` entirely and returning to the synchronous write path — which restores the 12,490-commits-per-day rate the parent task exists to cut.
