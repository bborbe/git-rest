---
status: approved
approved: "2026-10-22T17:48:46Z"
branch: dark-factory/bug-orphaned-pipe-outlives-deadline
---

## Summary

- Every git command the service runs is started with a context, so a deadline is supposed to bound it.
- The deadline kills the command, but the service then waits for that command's output to finish draining — and a child process left behind keeps the output open.
- So a pull carrying a 60 s deadline blocked for **4 m 13 s** on nuke dev, breaching the parent task's 120 s staleness ceiling.
- Bounding the drain makes a cancelled command return within a fixed grace instead of whenever the last orphan exits.
- One consequence must be handled deliberately: a command that **succeeded** before the grace expires has to still be reported as a success, or a push that already landed gets retried.

## Problem

`v0.34.1` on nuke dev, first ten minutes of operation. A coalescing flush lost the ref race and exhausted its three retries at **17:27:06**. The tree is then dirty, so the pull's dirty-tree rescue fires: branch `rescue/20261022T172710Z` is created at **17:27:10**. Its push blocks until **17:31:19**:

```
17:26:55  INFO  git-rest: push retry attempt=2 max=3
17:27:01  INFO  git-rest: push retry attempt=3 max=3
17:27:06  WARN  git-rest: flush pending writes before pull failed
                error="... ! [rejected] main -> main (fetch first)"
17:31:19  ERROR rescue push failed, leaving repo for inspection branch=rescue/20261022T172710Z
17:31:19  WARN  git pull failed error="signal: killed\ngit [push origin refs/heads/rescue/...]"
```

17:27:06 → 17:31:19 is **4 m 13 s**. Between those two lines the log carries **no line except memory warnings**: nothing flushes and nothing pulls. The next flush lands at 17:31:49, so a write staged around 17:26:5x stayed invisible to the other clusters for about five minutes.

`signal: killed` proves the context fired and the process was killed. It does not prove `Wait()` returned — and it did not.

## Reproduction

Deterministic, in-process, no network:

1. Write an executable script that spawns a background child inheriting stdout and then blocks: `sh -c 'sleep 300 & exec sleep 300'`.
2. `t.Setenv("GIT_SSH_COMMAND", <script>)`. With no SSH key configured `cmd.Env` is nil, so the git child inherits the parent environment verbatim — the same variable the production path sets explicitly at `git.go:292-300`. Injecting it through `sshKeyPath` is not possible: that path builds `ssh -i <key> …` itself.
3. In a temp repo **with at least one commit**, add the remote `ssh://example.invalid/repo.git` (the host never has to resolve — the fake command hangs before any connection), then call `runCmd(ctx, dir, "push", "origin", "HEAD")` with a **2 s** context. A repo with no commit fails inside `git push` before the transport is ever invoked, which would make the measurement meaningless.
4. Measure wall time from call to return.

The test must live in `package git` (not `git_test`): `runCmd` is an unexported method on an unexported type. That also means it cannot reuse the `git_test` fixture helpers below.

**Expected:** the call returns within the context plus a small grace, because the process was killed and the pipes were closed.

**Actual on the current tree:** the call returns only when the `sleep 300` child exits — minutes, not seconds — even though the context fired at 2 s.

## Expected vs Actual

| | |
|---|---|
| **Expected** | A command whose context is cancelled returns within a bounded interval after cancellation. `--pull-timeout` and `syncOnStartupTimeout` are documented as deadlines on the work they wrap (`pkg/puller/puller.go:63`, `main.go:209`). |
| **Actual** | The process is killed at the deadline, but `Wait()` blocks on the output-copy goroutine until every inherited pipe closes, so a pull bounded at 60 s ran for 4 m 13 s. |

## Why this is a bug

The deadline is the only mechanism keeping a pull cycle from starving the flusher, and the parent task promises that **no vault write stays invisible to another cluster longer than 120 s** (max-delay ceiling). A bound the code cannot enforce is not a bound, and the ceiling is measured against the whole cycle, not just the flush.

The same trap is already recorded in the parent task for a test fixture: killing `git push` orphans the server-side process and holds the stderr pipe, which is why a `sleep 15` fixture could not produce an elapsed time below 6 s. There the harness worked around it with an ancestor poll. The production paths carry no such guard.

## Goal

A git command whose context is cancelled — or whose process has exited — returns within a fixed, small grace period regardless of what children still hold its output pipe, so a pull cycle bounded at 60 s cannot run for minutes, and a command that completed successfully is still reported as a success.

## Assumptions

- The orphaned-child behaviour is the mechanism. Verified: `os/exec` starts a copy goroutine whenever `Stdout`/`Stderr` is not an `*os.File`, and `Cmd.Wait` waits for those copies; `Cmd.WaitDelay` exists to bound that wait once the process exits or the context is cancelled.
- `go.mod` declares `go 1.27.3`, so `Cmd.WaitDelay` (Go 1.20+) is available. `grep -rn WaitDelay --include='*.go'` currently returns nothing.
- **`Wait` returns `exec.ErrWaitDelay` when the grace expires with output still open, not the process's exit status.** Verified with a local probe: a command exiting 0 with a lingering child returns `err=exec: WaitDelay expired before I/O complete` under a non-zero `WaitDelay`, and `err=nil` under today's zero value. This is why DB3 exists: without handling it, a `git push` that actually landed becomes an error and `pushWithRetry` retries a push that already succeeded.

## Alternatives considered

- **`cmd.WaitDelay` (chosen).** One assignment per call site, no change to signal semantics, error text keeps `signal: killed` plus git's stderr. Smallest change that bounds wall time.
- **Process-group kill (`Setpgid` + `kill(-pid)`).** Also bounds the wait, and would additionally reap the reparented orphans that `008-bug-pid1-no-init-leaks-grandchild-zombies` documents as accumulating as zombies. Rejected here because it changes what a cancellation means for every child of every git invocation — a wider blast radius than this bug needs, and a change worth its own spec if the zombie count is worth fixing.
- **`*os.File` for stdout/stderr instead of `bytes.Buffer`.** Removes the copy goroutine entirely, so there is nothing to block on. Rejected because the error paths read the buffer inline (`git %v: %s`) and would need a read-after-close step; it also loses output on the kill path unless the file is drained first.
- **Do nothing** — see the Do-Nothing Option.

## Non-goals

- The memory ceiling. The dev writer sits at ~93% of its 512 Mi limit, and `git pack-objects` during a flush is the pressure that makes the rescue push slow. That is a deployment change with its own risk and its own spec; this one bounds wall time whether or not the memory is raised.
- The flush budget split. `021-flush-timeout-isolation` landed the third-of-budget rule in the tree; this spec does not revisit it.
- Reaping orphaned grandchildren (spec 008). Named in Alternatives; not fixed here.
- The push-rejection rate itself, which is the parent task's subject.

## Acceptance Criteria

- [ ] **AC1 — a cancelled command returns within the grace period, and that test is the regression lock.** The reproduction above, as a test in `package git`: call `runCmd` with a 2 s context against the pipe-holding `GIT_SSH_COMMAND`, assert elapsed **≤ 6 s**. A build that only kills the process still blocks for the child's lifetime, so the AC cannot be satisfied without a real bound. The same test is the regression lock and is named **`TestWaitDelayCancelledCommandReturns`**. Its pre-fix failure output goes in the **PR body**, not into this spec: an AC that asks the implementer to edit the contract it is being judged against has no evidence shape of its own. Evidence shape: numeric elapsed time from the test, plus the PR body quoting the same test failing on the pre-fix tree.
- [ ] **AC2 — the bound is applied at every production call site, never to the zero value, and a test enforces it.** `sites=$(grep -rn 'exec\.CommandContext' --include='*.go' --exclude='*_test.go' . | grep -vcE '^[^:]+:[0-9]+:[[:space:]]*//')` and `waits=$(grep -rn 'cmd\.WaitDelay = ' --include='*.go' --exclude='*_test.go' . | wc -l | tr -d ' ')`; assert `[ "$sites" = "$waits" ]`, `[ "$sites" -ge 11 ]`, and `grep -rcE 'cmd\.WaitDelay\s*=\s*0\b' --include='*.go' . | awk -F: '{s+=$NF} END {print s+0}'` returns 0. **Plus** a Go test named **`TestWaitDelayEveryExecSite`** that makes the same comparison in-process over the source files, so the invariant fails `make precommit` instead of only failing when this command is run by hand. ⚠️ Every detail was measured on the current tree: `--exclude='*_test.go'` scopes to production code, matching DB2 — without it a test file using `exec.CommandContext` false-fails a correct fix; the anchored filter `^[^:]+:[0-9]+:[[:space:]]*//` drops exactly one line, the comment at `git.go:1748`, and nothing else, where a bare `grep -v '//'` would also drop a site carrying a trailing `//nolint`; the result is **11** sites (`git.go` 6, `yaml_merge_resolver.go` 2, `conflict_resolver.go` 1, `claim_refs.go` 1, `main.go` 1). Two near-misses to avoid: bare `exec.CommandContext` counts **12** and fails a correct build, and `exec.CommandContext(ctx` counts **10** because `main.go`'s call is split across lines. `grep -rc … | awk` rather than `grep -c`, because `grep -c` over several files prints `file:count`. The zero-value clause exists because `cmd.WaitDelay = 0` means "no delay" — exactly today's bug — and it satisfies a naive count. Evidence shape: numeric stdout plus a passing test name.
- [ ] **AC3 — a successful command stays a success, on every wrapper and specifically on the push path.** (a) With a normal remote and no hang, `runCmdOutput(ctx, dir, "log", "-1", "--format=%s")` returns the same subject as a plain `git log -1 --format=%s` run outside the process, and `make precommit` exits 0 — the grace must not truncate healthy output or change exit-code handling. (b) A command whose **process exits 0** while a child still holds the pipe returns **nil**, not `exec.ErrWaitDelay` — asserted **through `pushWithRetry`**, not through `runCmdOutput`. This is the half a lazy fix gets wrong, and the reason is in the call graph: `pushWithRetry` (`git.go:380`) calls **`runCmdRaw`** (`git.go:388`), which returns the raw error from `cmd.Run()` (`git.go:352-353`); `pushWithRetry` returns nil only on `err == nil` (`git.go:389`) and otherwise wraps the error (`git.go:395-396`), because a landed push's output contains no `[rejected]`. A normalisation applied only to `runCmdOutput` therefore leaves the flush turning a landed push into a failure — retaining the pending set and re-pushing the same content next cycle, which is the duplicate-commit regression DB3 exists to prevent. The normalisation must sit where **all six wrappers** inherit it (`runCmd`, `runCmdOutput`, `runCmdRaw`, `runCmdEnv`, `runCmdOutputEnv`, `main.runGitCmd`), and the test must observe it on `pushWithRetry` against a script that writes output, exits 0 and leaves a child behind. Named **`TestWaitDelayLandedPushIsNotAnError`**. Evidence shape: `make precommit` exit 0, string equality, and a nil error from `pushWithRetry`.
- [ ] **AC4 — the pull cycle is bounded, which is the symptom.** Reuse the four-precondition fixture from `021`'s AC1 — non-empty pending set, remote divergence on the same path, overlapping dirty path, blocking `pre-receive` that allows `refs/heads/rescue/*` — but **install a new `pre-receive` that sleeps 120 s and carries no ancestor poll**, after the existing setup helper returns. ⚠️ `installBlockingPreReceive` (`pkg/git/flush_deadline_test.go:24`) bounds itself at 15 s and **exits as soon as the pushing client is gone**; reused verbatim the orphan dies ~0.2 s after the kill, `Wait` returns, and this AC passes on the unfixed tree. Do not edit that helper — `TestFlushRescueLandsAfterFlushDeadline` depends on its 15 s bound. Use a **10 s** pull context and assert `Pull` returns in **≤ 25 s** and that the log carries `signal: killed` (the kill is expected; the point is that it returns). On the current tree this returns at ~120 s. Evidence shape: numeric elapsed time plus a log-line match.
- [ ] **Post-Deploy (Rung-2):** AC5 — on nuke dev, consecutive pull cycles are no longer minutes apart. Extract every `pull_start=` timestamp from a 30-minute window and assert **every consecutive delta is ≤ 130 s**:

  ```
  kubectlnukedev -n dev logs vault-obsidian-openclaw-0 --since=30m \
    | grep -oE 'pull_start=[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}' \
    | cut -d= -f2 \
    | python3 -c 'import sys,datetime as d
  ts=[d.datetime.strptime(l.strip(),"%Y-%m-%dT%H:%M:%S") for l in sys.stdin]
  g=[(b-a).total_seconds() for a,b in zip(ts,ts[1:])]
  m=max(g) if g else 0
  print("cycles=%d max_gap=%.1fs" % (len(ts), m))
  raise SystemExit(0 if len(ts)>=10 and m<=130 else 1)'
  ```

  The command exits non-zero on failure, so the verifier reads an exit code rather than a printed number it has to judge.

  The observed failure is a 4 m 13 s gap, so 130 s — the 60 s budget plus the 60 s slack the task's ceiling allows — rejects it directly, and the measurement needs no hang to be present in the window. ⚠️ Fewer than ten `pull_start` lines is too quiet to judge: extend the window rather than reading a pass. ⚠️ A pod restart inside the window (a rollout, or an OOM kill at the ~93% memory headroom) breaks the timestamp sequence; exclude the boundary pair spanning the restart and say so beside the result.
  - `deploy_check:` `kubectlnukedev -n dev get pod vault-obsidian-openclaw-0 -o jsonpath='{.spec.containers[*].image}' | sed -E 's#.*:##'`
  - `deploy_target:` `$(git describe --tags --abbrev=0 origin/master)`

  ⚠️ `deploy_target` compares an image tag against the newest tag on master, so it false-passes on a stale deploy when the fix merges **without** a release: both sides then read the old tag. `.dark-factory.yaml` sets `autoRelease: false` here, so **the release is a prerequisite, not a step to remember** — cut the tag before this AC runs, record it in the AC's evidence, and treat the AC as unmet if the pod's image tag equals the tag the pre-fix build already ran, whatever the equality says.
- [ ] **AC6 — the rule is written down where an operator will find it.** `docs/deployment.md` § *Write coalescing* carries the literal sentence `WaitDelay bounds every git subprocess once its context is cancelled`, and `grep -c 'WaitDelay bounds every git subprocess once its context is cancelled' docs/deployment.md` returns 1. The literal is pinned because a paraphrase fails the grep, and a bare `WaitDelay` match is satisfied by a sentence saying it is *not* used. Evidence shape: numeric stdout.

## Verification

**Container-executable** (runs inside the YOLO container at prompt time):

- `make precommit` — build, lint, and the full suite.
- `go test ./pkg/git -list 'TestWaitDelay' | grep -c '^TestWaitDelay'` returns **≥ 4**, then `go test ./pkg/git -run 'TestWaitDelay' -count=1 -v` — AC1, AC2 (the in-process invariant), AC3(b) and AC4. ⚠️ The `-list` guard is load-bearing: `go test -run` with a pattern matching nothing exits **0** with `no tests to run`, which reads as a pass. The four names are `TestWaitDelayCancelledCommandReturns`, `TestWaitDelayEveryExecSite`, `TestWaitDelayLandedPushIsNotAnError` and `TestWaitDelayPullBounded`; there is no `TestPull*` in `pkg/git`.
- `sites=$(grep -rn 'exec\.CommandContext' --include='*.go' --exclude='*_test.go' . | grep -vcE '^[^:]+:[0-9]+:[[:space:]]*//'); waits=$(grep -rn 'cmd\.WaitDelay = ' --include='*.go' --exclude='*_test.go' . | wc -l | tr -d ' '); echo "sites=$sites waits=$waits"` — AC2; both equal and ≥ 11. Plus `grep -rcE 'cmd\.WaitDelay\s*=\s*0\b' --include='*.go' . | awk -F: '{s+=$NF} END {print s+0}'` returning 0.
- `grep -c 'WaitDelay bounds every git subprocess once its context is cancelled' docs/deployment.md` returns 1 — AC6.

**Operator-executable** (runs on the host after PR merge and deploy):

- AC5 on nuke dev only, after the image is published and `BRANCH=dev make apply` runs. Prod is not touched by this spec.

## Desired Behavior

- **DB1 — a cancelled command returns within the grace period.** When the context is cancelled, or the process exits while a child still holds the output pipe, `Wait` returns within the configured delay instead of waiting for the child.
- **DB2 — the bound is uniform.** Every `exec.CommandContext` in `pkg/git` and `main.go` carries it, so no future caller has to remember. That includes the `lsof` probe and the startup `runGitCmd`.
- **DB3 — a successful command stays a success, in every wrapper.** If the process exited with status 0 and the only error is `exec.ErrWaitDelay`, the command's error is nil and its captured output is returned — for `runCmd`, `runCmdOutput`, `runCmdRaw`, `runCmdEnv`, `runCmdOutputEnv` and `main.runGitCmd` alike, so `pushWithRetry`, which reads `runCmdRaw`'s raw error, sees a landed push as landed. A non-zero exit is returned as today. This is the half that keeps a landed push from being retried, and the half a single-wrapper fix misses.
- **DB4 — the pull cycle observes its own budget.** A cycle whose rescue push hangs returns near its deadline, so consecutive `pull budget` lines are never minutes apart.
- **DB5 — the rule is written down, verbatim.** `docs/deployment.md` § *Write coalescing* carries the literal sentence `WaitDelay bounds every git subprocess once its context is cancelled`, beside the existing bounded-flush paragraph. The literal is pinned so the AC's grep and the required sentence cannot drift apart.

## Constraints

- The write lock, the pending-set retention on a failed flush, and `pushWithRetry`'s three attempts are all unchanged. This bounds wall time, not policy.
- The grace is a package constant fixed at **2 s**, not a flag. It has no operator consumer, and a second knob would need config, docs and tests for nothing. Two seconds is derived from the two bounds that actually constrain it, not picked: AC1 requires a 2 s context to return within 6 s (so at most ~4 s), and `021`'s `TestFlushDeadlineCut` requires the whole 10 s pull to finish under 6 s after a 3.3 s flush deadline — which leaves **~2.6 s** and is the tighter of the two. Measured: 3 s made that test fail at 6.65 s, 2 s passes. It is far below the 60 s pull budget, so it never becomes the constraint.
- A killed command's error text stays `signal: killed` plus the git stderr, because operators and alerts already match it.
- `installBlockingPreReceive` keeps its 15 s bound and its ancestor poll; AC4 adds a new hook rather than changing it.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Grace expires while a child still holds the pipe, and the process **succeeded** | DB3: nil error, output intact | Nothing — the work landed; only trailing output was dropped |
| Grace expires while the process was **killed** | `signal: killed` plus git's stderr, as today | Nothing; no state is written by an unconfirmed push |
| Grace too short for a large `git log` | Output truncated; the wrapped error carries a partial buffer | Not observed at 3 s for git's output sizes. A raise is not free — AC1's `2 s context + grace ≤ 6 s` caps the grace at ~3 s, so raising it must be re-justified against that AC, not applied alone |
| **A killed push had already landed on the remote** | The pending set is retained (`flushPendingLocked` clears `g.pending` only on success), so the next cycle re-commits and re-pushes the same content — landing a second commit with an identical tree | `git -C /data log origin/main --oneline -2` shows two commits with the same `git-rest:` subject; harmless, no action. Expected to widen slightly under this change, since more pushes are cut |
| A future caller adds `exec.CommandContext` without the assignment | `TestWaitDelayEveryExecSite` fails `make precommit` | Add the assignment; the invariant is enforced mechanically rather than by a grep someone has to remember |
| Killed orphans linger | Reparented to PID 1, which is `/main` — Go installs no `SIGCHLD` reaper, so they accumulate as `<defunct>` (`008-bug-pid1-no-init-leaks-grandchild-zombies`). **Unchanged by this spec** — today's behaviour too | Count with `ps -eo stat | grep -c Z`; reaping them is spec 008's subject, not this one |
| Context has no deadline | Unchanged: the command runs to completion, and the grace only bounds post-exit draining | Nothing |

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | The grace constant, its assignment at all 11 sites, the `ErrWaitDelay`-on-success handling, `TestWaitDelayEveryExecSite`, and the `docs/deployment.md` sentence | DB1, DB2, DB3, DB5 | AC2, AC3(a), AC6 | — |
| 2 | The pipe-holding fixture, the elapsed-time test and the regression lock, in `package git` | DB1, DB3 | AC1, AC3(b) | 1 |
| 3 | The bounded pull-cycle test on a new 120 s no-poll `pre-receive` | DB4 | AC4 | 1 |

AC5 is operator-run after deploy and has no prompt.

## Do-Nothing Option

The parent task cannot close. Its `SC2` clause promises a ≤ 120 s staleness ceiling, and the measured behaviour is ~5 minutes whenever a flush loses the ref race — which, at 13,397 `git-rest:` commits per 24 h, happens several times an hour. Every such occurrence also leaves a `rescue/*` branch unpushed and the tree dirty until the next cycle, so the vault is both stale and less recoverable for minutes at a time.
