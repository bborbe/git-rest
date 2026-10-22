---
status: completed
spec: [021-flush-timeout-isolation]
summary: Bounded the pre-pull flush to one third of the remaining pull deadline, added the per-cycle budget log line and the cut-off log, documented it, and covered it with TestFlush* tests.
execution_id: git-rest-coalesce-exec-080-spec-021-flush-deadline
dark-factory-version: v0.198.7
created: "2026-10-22T14:31:10Z"
queued: "2026-10-22T14:43:29Z"
started: "2026-10-22T14:43:31Z"
completed: "2026-10-22T14:48:54Z"
---

<summary>
- A pull cycle can no longer spend its whole time budget pushing pending writes.
- The flush that runs before a pull is capped at one third of the pull's remaining deadline.
- When the pull context carries no deadline, the flush behaves exactly as before.
- After a cut-off flush the pull still merges, still runs the dirty-tree rescue, and still pushes the rescue branch.
- A flush cut short by its own deadline is logged with a distinguishable message.
- Every pull cycle logs one budget line with the start time, the pull deadline and the flush deadline.
- A healthy remote is unchanged: the flush still lands before the merge, exactly once.
- Turning write coalescing off leaves the pull path untouched.
- The deployment docs explain the bounded flush and the new log line.
</summary>

<objective>
Bound the pre-pull write flush to at most one third of the remaining pull budget so a contended push can never consume the whole deadline and starve the merge and the dirty-tree rescue. Make the budget observable through one log line per cycle and a distinct log line when the flush is cut short. Leave behaviour unchanged when the context has no deadline and when write coalescing is disabled.
</objective>

<context>
Read the approved spec at `specs/in-progress/021-flush-timeout-isolation.md` — especially § Problem, § Assumptions, § Desired Behavior (DB1–DB4), § Constraints and § Failure Modes.

Read these files fully before editing:
- `pkg/git/git.go` — `Pull` (the `flushPendingLocked(ctx)` call near the end of the method), `flushPendingLocked`, `pushWithRetry`, `retryPush`, `syncWithUpstream`, `fastForwardOrRescue`, `rescueDirtyTree`, `restorePendingIndexLocked`, and the `runCmd*` helpers. `Pull` already holds `g.mu` for its whole body and already has `start := g.currentDateTimeGetter.Now()` for the metrics observation; do not change that.
- `pkg/puller/puller.go` — shows where the periodic `--pull-timeout` context deadline comes from (`context.WithTimeout` around `git.Pull`).
- `main.go` — `syncOnStartupTimeout` (60 s) and `syncOnStartup`; this is the other caller whose context carries a deadline. `createShutdownFlusher` logs the existing `git-rest: flush deadline exceeded` string — that string is a PREFIX of the new one, so it must not be reused or shortened.
- `pkg/git/coalesce_test.go` — `newCoalesceFixture`, `newCoalesceFixtureWithMetrics`, `originGitRestSubjects`, `waitFor`.
- `pkg/git/git_test.go` — `captureSlogLogs` (buffer-backed text handler at `slog.LevelInfo`).
- `pkg/git/claim_refs_test.go` — `mustGit`, `configureClaimIdentity`.
- `docs/deployment.md` — § *Write coalescing* (the paragraph mentioning the SIGTERM 10 s deadline is where the new rule goes).
- `CHANGELOG.md` — existing `## Unreleased` section, if present.

Coding docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors`, never `fmt.Errorf`, never `context.Background()` in `pkg/`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — plain `testing` tests in `package git_test` (Ginkgo's single `TestSuite` entry point cannot be selected with `-run`).
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` — coverage and verification rules.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — entry format and prefix rules.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md` — `log/slog` conventions.

Project rule that matters here: this repo's tests use plain `testing` for `pkg/git` (see `coalesce_test.go`), so write the new tests as `func TestFlush…(t *testing.T)` and give every new test a `TestFlush` prefix — the verification pattern `-run 'TestFlush'` must select all of them.
</context>

<requirements>
## 1. Derive the flush deadline and log the budget (`pkg/git/git.go`, `Pull`)

1.1 In `Pull`, replace the body of the existing flush block (the `if err := g.flushPendingLocked(ctx); err != nil { … }` block with the comment "Flush pending writes before resolving upstream or merging") with the following behaviour, **leaving the block where it is**. It already sits after `g.mu.Lock()` / `defer g.mu.Unlock()` / `defer g.refreshQuarantinedBacklog(ctx)` and before the `if !g.hasRemote(ctx)` early return — do not move it.

1.2 Compute the budget from the context's own deadline — never from a flag. The rule keys on `ctx.Deadline()` because both callers (`puller` with `--pull-timeout`, and `syncOnStartup` with the hardcoded 60 s ceiling) install a deadline on the context `Pull` receives:

```go
	// Bound the flush to a third of the pull budget so a contended push cannot
	// consume the whole deadline and starve the merge and the dirty-tree rescue,
	// which run later in this Pull on the parent context.
	pullStart := time.Now()
	pullDeadline, hasPullDeadline := ctx.Deadline()
	pullDeadlineText := "none"
	flushDeadlineText := "none"
	flushCtx := ctx
	if hasPullDeadline {
		pullDeadlineText = pullDeadline.Format(time.RFC3339Nano)
		flushDeadline := pullStart.Add(pullDeadline.Sub(pullStart) / 3)
		flushDeadlineText = flushDeadline.Format(time.RFC3339Nano)
		var cancel context.CancelFunc
		flushCtx, cancel = context.WithDeadline(ctx, flushDeadline)
		defer cancel()
	} else {
		slog.DebugContext(ctx, "git-rest: flush deadline skipped: no pull deadline")
	}
```

Use `time.Now()` for `pullStart`, not `g.currentDateTimeGetter.Now()`: the budget is arithmetic against `ctx.Deadline()`, which comes from `time.Now()`, so both values must be read from the same clock.

1.3 Log exactly one budget line per `Pull` call, unconditionally (including when there is no deadline and including when there is no remote), with this pre-formatted message — the three `key=value` pairs are parsed by tests and by operators, so they must be part of the message string, not separate slog attributes:

```go
	slog.InfoContext(
		ctx,
		"git-rest: pull budget pull_start="+pullStart.Format(time.RFC3339Nano)+
			" pull_deadline="+pullDeadlineText+
			" flush_deadline="+flushDeadlineText,
	)
```

With no deadline on `ctx`, the line must carry `pull_deadline=none flush_deadline=none`.

1.4 Run the flush on `flushCtx`, and log the cut-off case separately from the existing failure log. Keep the existing `git-rest: flush pending writes before pull failed` warn line — a flush failure must still never abort the pull:

```go
	if err := g.flushPendingLocked(flushCtx); err != nil {
		// Only the FLUSH's own deadline counts as a cut-off. flushCtx inherits the
		// parent's DeadlineExceeded, so testing flushCtx.Err() alone would also
		// mislabel a pull-budget expiry as a flush cut-off. ctx.Err() == nil proves
		// the parent was still alive when the child expired.
		if hasPullDeadline && ctx.Err() == nil && errors.Is(flushCtx.Err(), context.DeadlineExceeded) {
			slog.WarnContext(ctx, "git-rest: flush deadline exceeded before pull", "error", err)
		}
		slog.WarnContext(ctx, "git-rest: flush pending writes before pull failed", "error", err)
	}
```

`errors` here is the already-imported `github.com/bborbe/errors` package; it exposes `Is`. The trailing `before pull` in the new string is load-bearing: `main.go:470` already logs `git-rest: flush deadline exceeded` on SIGTERM, and that is a prefix of this string.

1.5 Everything after the flush — `hasRemote`, stale-lock cleanup, `recoverRepoState`, `@{u}` resolution, `syncWithUpstream`, the merge and `rescueDirtyTree` — must continue to receive the PARENT `ctx`, never `flushCtx`. This is what leaves the merge and rescue the remaining two thirds.

1.6 Do NOT change `flushPendingLocked`, `pushWithRetry` or `pushMaxAttempts`. This bounds the whole flush, not the individual push attempts. Do NOT add a `--flush-timeout` flag or any other knob.

## 2. Tests (`pkg/git/flush_deadline_test.go`, new file, `package git_test`)

Start the file with the repo's license header (copy the three-line comment header from `pkg/git/coalesce_test.go`). Reuse the existing helpers; do not duplicate them.

2.1 Add a shared hook helper that both this prompt and prompt 2 use:

```go
// installBlockingPreReceive installs a pre-receive hook on the bare origin that
// sleeps past a 10 s pull context and rejects updates to refs/heads/main, while
// allowing any other ref (notably refs/heads/rescue/*) immediately.
func installBlockingPreReceive(t *testing.T, bare string)
```

The hook script (write it to `filepath.Join(bare, "hooks", "pre-receive")` with mode `0750`; create the directory with `os.MkdirAll(..., 0750)`) must read stdin first, then branch:

```sh
#!/bin/sh
input=$(cat)
case "$input" in
  *refs/heads/main*)
    sleep 15
    echo "remote rejected: refs/heads/main is locked" >&2
    exit 1
    ;;
esac
exit 0
```

The 15 s sleep must outlast the test's whole 10 s context (there is no flush deadline on the unfixed tree, so a sleep sized against a third would prove nothing). Rescue refs must not sleep — the rescue push has to land inside the remaining budget.

2.2 Add a debug-level log capture helper local to this file (do NOT change `captureSlogLogs`, which is pinned at `slog.LevelInfo`):

```go
// captureSlogLogsAtDebug is captureSlogLogs with a handler that emits Debug.
func captureSlogLogsAtDebug() (*bytes.Buffer, func())
```

2.3 `TestFlushBudgetLine` (DB1, DB4, AC4). With a coalescing-enabled fixture and no pending writes:
- Call `Pull` under `context.WithTimeout(context.Background(), 9*time.Second)` while capturing logs.
- Assert exactly one budget line per cycle: `strings.Count(buf.String(), "git-rest: pull budget") == 1`.
- Find the `git-rest: pull budget …` line with a regexp equivalent to `git-rest: pull budget pull_start=(\S+) pull_deadline=(\S+) flush_deadline=(\S+)`.
- Parse all three values with `time.Parse(time.RFC3339Nano, …)`.
- Assert `flushDeadline.Sub(pullStart) <= pullDeadline.Sub(pullStart)/3 + time.Millisecond`.
- Assert `flushDeadline.Before(pullDeadline)` (a build that sets `flush_deadline = pull_deadline - 1ns` must fail the third-budget assertion above).
- Then call `Pull(context.Background())` with the debug-level capture and assert the budget line contains `flush_deadline=none` and the log contains `git-rest: flush deadline skipped: no pull deadline`.

2.4 `TestFlushDeadlineCut` (DB1, DB4, AC2 — a supplementary AC2 check; the AC1-fixture AC2 assertion lands with the later rescue-regression prompt). With a coalescing-enabled fixture (`Delay: time.Hour`, `MaxDelay: time.Hour` so only the pull boundary can flush):
- `WriteFile(ctx, "tasks/x.md", []byte("L"))` so the pending set is non-empty.
- Install the blocking hook on the bare origin.
- Capture logs, then call `Pull` under a 10 s `context.WithTimeout`, measuring elapsed time.
- Assert the returned error is nil (after the cut flush, local and remote are still equal, so the cycle is a no-op).
- Assert the batch survived: `mustGit(t, work, "status", "--porcelain")` still contains `tasks/x.md` (the cut flush reset HEAD but left the path staged), so the next cycle can push it.
- Assert the log contains `git-rest: flush deadline exceeded before pull`.
- Assert `elapsed < 6*time.Second`. On the unfixed tree the push runs until the full 10 s context is gone; a flush cut at one third returns in roughly 3.3 s. The 6 s bound leaves margin for local git work without accepting the unfixed timing.

2.5 `TestFlushHealthyRemoteUnchanged` (DB3, AC3). With a coalescing-enabled fixture and a path that already exists (`tasks/x.md`):
- Capture logs, `WriteFile(ctx, "tasks/x.md", []byte("H"))`, then `Pull(context.Background())`.
- Assert `originGitRestSubjects(t, bare, "main", "tasks/x.md")` has exactly one entry and that it matches `^git-rest: (create|update) tasks/x\.md$` (use a `regexp`; a `POST` to an existing path yields `update`, but the alternation is what AC3 specifies).
- Assert the log contains zero occurrences of `git-rest: flush deadline exceeded before pull` and at least one `git-rest: flushed pending writes`.

2.6 `TestFlushCoalescingOff` (AC5). With `git.CoalesceConfig{Enabled: false}`:
- Capture logs, `WriteFile` `tasks/x.md` twice with different content (`"A"` then `"B"`), then `Pull(context.Background())`.
- Assert the log contains zero occurrences of `git-rest: flushed pending writes` and zero occurrences of `git-rest: flush deadline exceeded before pull`.
- Assert `originGitRestSubjects(t, bare, "main", "tasks/x.md")` has exactly 2 entries.

2.7 Before finishing, re-run the verification block and walk DB1–DB4 and AC2–AC5 against the change.

## 3. Documentation (`docs/deployment.md`)

3.1 Extend § *Write coalescing* (the paragraph that currently starts "On SIGTERM the process flushes…"). Add, beside the existing SIGTERM deadline sentence, the bounded-flush rule: a pull cycle bounds its flush to one third of the remaining pull budget (`--pull-timeout`, or the 60 s startup ceiling); a flush cut short logs `git-rest: flush deadline exceeded before pull`, leaves the paths staged for the next cycle, and the cycle still merges and runs the dirty-tree rescue on the remaining budget. Mention that each cycle logs `git-rest: pull budget pull_start=… pull_deadline=… flush_deadline=…`, with `flush_deadline=none` when the context carries no deadline.

3.2 Do not document a `--flush-timeout` flag — it does not exist and must not be added.

## 4. Changelog (`CHANGELOG.md`)

4.1 Add one bullet under `## Unreleased` (create the section at the top if absent, following the file's existing style): `- fix: Bound the pre-pull write flush to one third of the pull budget so a contended push cannot starve the dirty-tree rescue`.
</requirements>

<constraints>
- `flushPendingLocked` must still run BEFORE the merge: merging into a tree this process dirtied is what the rescue exists to avoid.
- The write lock is still held across the flush. This change alters how long the flush may run, not whether it holds the lock.
- `pushWithRetry`'s three attempts stay. This bounds the whole flush, not the individual attempts.
- Claim-ref operations remain synchronous and outside this path — do not touch claim-ref code.
- The rule keys on `ctx.Deadline()`, never on the `--pull-timeout` flag, so both the periodic pull and the startup pull are covered.
- Do NOT add a `--flush-timeout` flag, a new metric, a config field, or any other knob. The spec's Non-goals forbid them.
- Do NOT raise `--pull-timeout` anywhere.
- Preserve repo invariants: errors wrapped with `github.com/bborbe/errors` (never `fmt.Errorf`), no `context.Background()` in `pkg/`, `log/slog` for logging.
- Do NOT create or edit `pkg/git/flush_rescue_test.go` — it does not exist yet; a later prompt in this batch owns it. Do not change `rescueDirtyTree`.
- The spec's Failure Modes row *Flush killed after the remote accepted the ref* is OUT OF SCOPE here: `flushPendingLocked` keeps the pending set on error and resets HEAD, so the next cycle re-commits and re-pushes rather than logging a no-op. Requirement 1.6 forbids changing that bookkeeping — do not add code for this row.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass. New code needs ≥ 80% statement coverage; changed code needs its changed paths tested.
- The container has no view of `origin/master` and no operator tooling: do not add `docker`, `kubectl`, `dark-factory`, `scripts/*` or bare `git` commands to the verification block.
</constraints>

<verification>
Run from the repo root. Iterate with `make test`; run `make precommit` once at the end.

```
go test -mod=mod ./pkg/git -run 'TestFlush' -count=1 -v
grep -c 'flush deadline exceeded before pull' docs/deployment.md
grep -c 'pull budget' docs/deployment.md
make test
make precommit
```

The two `grep -c` commands must print a number ≥ 1. `make precommit` must exit 0.
</verification>
