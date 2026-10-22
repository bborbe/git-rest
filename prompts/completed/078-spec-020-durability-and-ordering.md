---
status: completed
spec: [020-commit-coalescing]
summary: Added SIGTERM flush with a 10s deadline, boot recovery of index-staged paths, flush-first DeleteFile ordering, and durability regression tests.
execution_id: git-rest-coalesce-exec-078-spec-020-durability-and-ordering
dark-factory-version: v0.198.7
created: "2026-10-22T10:38:09Z"
queued: "2026-10-22T10:56:37Z"
started: "2026-10-22T11:04:06Z"
completed: "2026-10-22T11:08:01Z"
---

<summary>
- A pending (staged, uncommitted) write survives a graceful restart: SIGTERM flushes and pushes before the process exits, bounded by a 10-second deadline.
- When that deadline cuts the flush short, the staged paths stay in the index and one INFO line beginning "git-rest: flush deadline exceeded" records it.
- A pending write survives a crash: on boot the staged paths are committed and pushed before the HTTP server starts listening, and one INFO line beginning "git-rest: recovered pending writes" names the path count.
- Deleting a file first flushes any pending writes, so the deletion commit always lands above the write commit and the path is not re-created.
- Claim refs stay synchronous: a PUT returns 200 only after the ref is on the origin, even while a file write is pending, and it never flushes the buffer.
- A failed flush push keeps the batch intact and the next tick retries it.
</summary>

<objective>
Add the durability and ordering guarantees on top of the coalescing core from prompt 1: a SIGTERM flush with a 10-second deadline, boot recovery of index-staged paths, flush-first ordering for `DeleteFile`, and regression tests proving claim refs remain synchronous and a failed push never drops the batch.
</objective>

<context>
Read the approved spec at `specs/in-progress/020-commit-coalescing.md` — § Desired Behavior 5 and 6, § Failure Modes, and the acceptance criteria named below.

Prompt 1 must already be merged into the worktree: it added the pending set, `FlushPending`, `FlushPendingIfDue`, `flushPendingLocked`, `enqueuePendingLocked`, the three metrics and the flusher package. Read the current state of:
- `pkg/git/git.go` — `Git` interface (line ~144), `git` struct (line ~193), `DeleteFile` (line ~772), `flushPendingLocked`, `pushWithRetry` (line ~327), `hasRemote` (line ~2200).
- `main.go` — `Run` (line ~61), `createGitClient` (line ~360), `syncOnStartup` (line ~199), `service.Run(...)` call.
- `export_test.go` — package `main` test exports; `main_test.go` — package `main_test` Ginkgo suite, uses `mocks.FakeGit` and `gexec`.
- `pkg/git/coalesce_test.go` — the `newCoalesceFixture` helper added by prompt 1 (reuse it; do not redefine it). It returns the client, the work-clone path and the bare-origin path. Because requirement 5.1 needs the fake metrics, add one variant in your new file — `newCoalesceFixtureWithMetrics(t *testing.T, config git.CoalesceConfig) (git.Git, string, string, *mocks.FakeMetrics)` — that builds the same origin/seed/work clone and returns the `*mocks.FakeMetrics` it passed to `git.New`. Leave prompt 1's helper and its call sites untouched.
- `pkg/git/claim_refs.go` — `PutClaimRef` (line ~335) and `pushClaimRef` (line ~396). Claim refs deliberately do not take `g.mu` and push their own ref; do not change them.
- `pkg/git/claim_refs_test.go` — `newClaimFixture`, `mustGit` helpers.

Coding docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors`, `errors.Is`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` — `run.Func`, caller-owned context, no raw goroutines.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` — deriving a fresh deadline context for shutdown work.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` and `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md` — `log/slog`, INFO for lifecycle events.

Also read the project's own `docs/dod.md` (the daemon's validation prompt). **Two of its criteria are intentionally deviated from here**: the Testing criterion uses plain `testing` for the `pkg/git` tests because Ginkgo's single `TestSuite` entry point (`pkg/git/git_suite_test.go:18`) cannot be selected by `go test -run` (the `main_test.go` spec in 5.5 stays Ginkgo), and the `## Unreleased` CHANGELOG entry it asks for is delivered by prompt 3 in this batch — do not edit `CHANGELOG.md` here and do not report either as a blocker.
</context>

<requirements>
## 1. Boot recovery of staged paths (DB5, crash case)

1.1 Add one method to the `Git` interface (GoDoc comment required):

```go
// RecoverPendingWrites commits and pushes paths left staged in the index by a
// crash, before the HTTP server starts serving. A no-op when nothing is staged.
RecoverPendingWrites(ctx context.Context) error
```

Regenerate `mocks/git.go` with `go generate ./...`.

1.2 Implement it in `pkg/git/git.go`:

```go
// RecoverPendingWrites commits and pushes paths left staged in the index by a
// crash. It runs before the HTTP server starts, so it is the only writer.
func (g *git) RecoverPendingWrites(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	out, err := g.runCmdOutput(ctx, g.repoPath, "diff", "--cached", "--name-only")
	if err != nil {
		return errors.Wrap(ctx, err, "list staged paths for recovery")
	}
	paths := make([]string, 0, 8)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			paths = append(paths, line)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	sort.Strings(paths)

	message := "git-rest: update " + strconv.Itoa(len(paths)) + " files"
	if len(paths) == 1 {
		status, statusErr := g.runCmdOutput(
			ctx, g.repoPath, "diff", "--cached", "--name-status", "--", paths[0],
		)
		if statusErr != nil {
			return errors.Wrapf(ctx, statusErr, "read staged status for %s", paths[0])
		}
		if strings.HasPrefix(strings.TrimSpace(string(status)), "A") {
			message = "git-rest: create " + paths[0]
		} else {
			message = "git-rest: update " + paths[0]
		}
	}

	if err := g.runCmd(ctx, g.repoPath, "commit", "-m", message); err != nil {
		g.metrics.IncWriteFlush("error")
		return errors.Wrap(ctx, err, "git commit recovered pending writes")
	}

	pushOutcome := "skipped"
	if g.hasRemote(ctx) {
		if err := g.pushWithRetry(ctx, ""); err != nil {
			g.metrics.IncWriteFlush("error")
			return errors.Wrap(ctx, err, "git push recovered pending writes")
		}
		pushOutcome = "ok"
	}

	g.pending = make(map[string]pendingWrite)
	g.metrics.SetWritePending(0)
	g.metrics.IncWriteFlush("ok")
	slog.InfoContext(
		ctx,
		"git-rest: recovered pending writes",
		"paths", len(paths),
		"push", pushOutcome,
	)
	return nil
}
```

The commit-message rule is DB3's: one recovered path uses the single-path form (create vs update from `git diff --cached --name-status`), more than one uses `git-rest: update <n> files`. The exact log prefix `git-rest: recovered pending writes` is load-bearing — the operator acceptance criterion greps it.

1.3 `main.go` — call recovery before the startup pull and before the HTTP server starts. In `application.Run`, between `gitClient, err := a.createGitClient(ctx)` and `syncOnStartup(ctx, gitClient)`, add:

```go
if err := gitClient.RecoverPendingWrites(ctx); err != nil {
	slog.WarnContext(ctx, "boot recovery of pending writes failed", "error", err)
}
```

Recovery is best-effort: a failure is warn-logged and must not abort startup. **A failed PUSH leaves the commit local and the startup pull's local-ahead push path pushes it. A failed COMMIT leaves the paths staged with an empty pending set, so no in-process flush retries them** — they stay in the index until the next successful flush (which commits the whole index) or until the next process start re-runs recovery. Do not add a retry loop for this; the next boot is the recovery mechanism.

It must run BEFORE `syncOnStartup` — `Pull` flushes pending writes first (prompt 1, DB4), which would otherwise consume the staged paths and log the flush line instead of the recovery line.

When the index is clean — a crash after the commit but before the push — recovery is a **no-op by design**: the commit is already local and the startup pull's local-ahead push path pushes it (spec Failure Modes, "Pod killed mid-push"). Do not make recovery push when nothing is staged.

## 2. SIGTERM flush with a 10-second deadline (DB5)

2.1 In `main.go` add:

```go
// shutdownFlushDeadline bounds the SIGTERM flush. When it expires the process
// exits anyway; the staged paths stay on disk for boot recovery.
const shutdownFlushDeadline = 10 * time.Second
```

2.2 Add:

```go
// createShutdownFlusher returns a run.Func that flushes pending writes when the
// service context is cancelled. The flush runs on a context detached from
// cancellation so the 10 s deadline, not the already-cancelled service
// context, bounds it.
func (a *application) createShutdownFlusher(gitClient git.Git) run.Func {
	return func(ctx context.Context) error {
		<-ctx.Done()
		flushCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			shutdownFlushDeadline,
		)
		defer cancel()
		if err := gitClient.FlushPending(flushCtx); err != nil {
			if flushCtx.Err() != nil {
				slog.InfoContext(
					ctx,
					"git-rest: flush deadline exceeded",
					"deadline", shutdownFlushDeadline.String(),
				)
				return nil
			}
			slog.WarnContext(ctx, "git-rest: shutdown flush failed", "error", err)
		}
		return nil
	}
}
```

Decide the deadline log line from `flushCtx.Err() != nil`, not from `errors.Is(err, context.DeadlineExceeded)`: `exec.CommandContext` reports a killed subprocess as a signal error, so `errors.Is` against `context.DeadlineExceeded` does not match.

2.3 Add `a.createShutdownFlusher(gitClient)` to the `service.Run(...)` argument list, after `a.createWriteFlusher(gitClient)`. `service.Run` uses `run.CancelOnFirstFinishWait`, so it waits for this function to finish before the process exits.

The failed-flush index restore from prompt 1 (`restorePendingIndexLocked`) is what keeps `git diff --cached --name-only` listing the path after the deadline; do not change it.

## 3. Delete flushes pending writes first (DB6)

In `DeleteFile`, after `g.mu.Lock()` / `defer g.mu.Unlock()` and before the `os.Stat` / `git rm` work, add:

```go
// Flush pending writes first: a delete that jumped the queue would leave the
// path re-created by the later flush.
if err := g.flushPendingLocked(ctx); err != nil {
	g.metrics.IncGitOperationError("delete_file")
	return errors.Wrap(ctx, err, "flush pending writes before delete")
}
```

A flush failure aborts the delete (the pending content is not lost; the next tick retries). When nothing is pending this is a no-op and the delete keeps today's synchronous commit + push.

## 4. Claim refs stay synchronous (DB6) — no production change

`PutClaimRef` / `DeleteClaimRef` already push their own ref and never touch the index or the pending set. Do not route them through the buffer and do not call any flush from them. Prove the property with the test in 5.4.

## 5. Tests

Reuse `newCoalesceFixture` from `pkg/git/coalesce_test.go`. Put the new tests in `pkg/git/coalesce_durability_test.go` (plain `testing`, package `git_test`), so the spec's `go test ./pkg/git/... -run '<Name>'` commands match.

5.1 `TestFlushPushFailureKeepsPending` — install a failing hook in the bare origin:

```go
hook := filepath.Join(bare, "hooks", "pre-receive")
os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755)
```

- write `tasks/x.md`, call `FlushPending` → non-nil error;
- the origin's log for `tasks/x.md` has 0 `^git-rest:` commits;
- `git diff --cached --name-only` in the work clone lists `tasks/x.md` (the index was restored);
- `FakeMetrics.IncWriteFlushArgsForCall(last)` is `"error"`; the last `SetWritePending` argument is greater than 0;
- remove the hook, call `FlushPending` again → nil; the origin now has exactly one `git-rest:` commit for `tasks/x.md` with the written content; the last `IncWriteFlush` argument is `"ok"` and the last `SetWritePending` argument is 0.

5.2 `TestDeleteFlushesPendingFirst` — write `tasks/x.md` with content `I` (do not flush), then call `DeleteFile("tasks/x.md")`:
- `git log -2 --format='%s'` in the work clone lists `git-rest: delete tasks/x.md` above a `git-rest: update tasks/x.md` commit;
- the same two subjects appear on the origin;
- `git status --porcelain` in the work clone returns no lines.

5.3 `TestRecoverPendingWrites` — simulate a crash after staging: write `tasks/x.md` with content `SURVIVOR` directly and run `git add tasks/x.md` in the work clone (use the `mustGit`-style helper), without committing. Then call `RecoverPendingWrites(ctx)`:
- the origin's branch has a commit whose `tasks/x.md` content is `SURVIVOR`;
- the local index is clean (`git diff --cached --name-only` empty) and `git status --porcelain` is empty;
- the captured `slog` output contains `git-rest: recovered pending writes` (use a `slog.NewTextHandler` on a `bytes.Buffer` with `slog.SetDefault`, restoring the previous logger with `defer`);
- the last `SetWritePending` argument is 0.
- Add a second scenario staging two paths: the commit subject is `git-rest: update 2 files`.
- Add a third scenario staging one **new** path (`tasks/new.md`): the commit subject is `git-rest: create tasks/new.md`. **Without it the `A`-prefix branch of `RecoverPendingWrites` has no test anywhere in the batch** — prompt 1's 8.1 covers the *flush* create branch, not this one.

5.4 `TestClaimRefsStaySynchronousWhileWritePending` — with coalescing enabled and a pending write on `tasks/x.md`:
- call `PutClaimRef(ctx, "claims/probe", git.ClaimRefRequest{Cluster: "dev", TaskID: "probe"})` (empty `ExpectedSHA` means create);
- immediately `git ls-remote origin 'refs/claims/*'` lists `refs/claims/probe`;
- the origin's log for `tasks/x.md` still has 0 `^git-rest:` commits (the PUT did not flush the buffer);
- then `FlushPending` and assert the file commit appears.

5.5 `main_test.go` — add a Ginkgo spec for the shutdown hook. First expose it in `export_test.go` (package `main`):

```go
// CreateShutdownFlusher exposes the shutdown flush hook for main_test.
func CreateShutdownFlusher(g git.Git) run.Func {
	a := &application{}
	return a.createShutdownFlusher(g)
}
```

Then in `main_test.go` (package `main_test`), with `fakeGit := &mocks.FakeGit{}`:
- create a context, call `cancel()`, then run `main.CreateShutdownFlusher(fakeGit)(ctx)`;
- assert it returns nil and `fakeGit.FlushPendingCallCount() == 1`;
- assert the context passed to `FlushPending` has a deadline (`_, ok := fakeGit.FlushPendingArgsForCall(0).Deadline(); Expect(ok).To(BeTrue())`) and that it is within 1 s of `10 * time.Second` from now (`shutdownFlushDeadline` is unexported in package `main` and `main_test` cannot reference it — hardcode the constant here, or export it from `export_test.go`);
- **add a second spec for the deadline branch.** `git-rest: flush deadline exceeded` is a contract with the operator's grep (spec DB5 and the Failure Modes row "Flush exceeds its 10 s deadline on SIGTERM"), and nothing else asserts it: set `fakeGit.FlushPendingStub = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }`, capture `slog` into a `bytes.Buffer` via `slog.NewTextHandler` (`slog.SetDefault`, restore with `defer`), run `main.CreateShutdownFlusher(fakeGit)` on an already-cancelled context, and assert the func returns nil and the buffer contains `git-rest: flush deadline exceeded`. This spec takes ~10 s because the deadline is a constant — that is expected, not a hang. **Do NOT make the deadline configurable to speed it up.**

## 6. Reference — acceptance criteria this prompt implements (operator-executed Rung 1, not run in the container)

Copied from the spec for behavioural precision. Do not run them here and do not put them in the verification block.

- **AC6** — a pending write survives SIGTERM: recipe (e) posts `SURVIVOR`, waits 2 s, records `T0`, sends SIGTERM, waits for exit; then the origin's log for `tasks/x.md` has ≥ 1 `^git-rest:` commit and the origin's `tasks/x.md` content is `SURVIVOR`. The deadline is not asserted here.
- **AC7** — a pending write survives SIGKILL and is recovered on boot: recipe (f) posts `SURVIVOR`, waits 2 s, `kill -9`s the process, asserts the origin has no `SURVIVOR` commit AND `git diff --cached --name-only` lists `tasks/x.md`, then reboots the same binary against the same repo and waits two pull intervals. The origin's `tasks/x.md` content is `SURVIVOR` and the log contains `git-rest: recovered pending writes` ≥ 1. The precondition is load-bearing.
- **AC8** — claim refs stay synchronous while a file write is pending: recipe (g) posts a file, immediately `PUT /api/v1/refs/claims/{key}` with `{"cluster":"dev","task_id":"probe","expected_sha":""}`, and asserts the ref is listed on the origin at the moment the PUT returned 200 while the origin's log for `tasks/x.md` still has 0 `^git-rest:` commits.
- **AC11** — `DeleteFile` flushes pending writes before its own commit: recipe (i) posts `tasks/x.md`, deletes it 1 s later, then flushes; `git log -2 --format='%s'` lists `git-rest: delete tasks/x.md` above a `git-rest:` write commit and `git status --porcelain` returns 0 lines.
- **AC14** — the shutdown deadline is enforced: recipe (e2) installs a `pre-receive` that sleeps 60 s, posts, waits 2 s, then runs SIGTERM-and-wait as one script block and prints `ELAPSED` in milliseconds. `ELAPSED` ≤ 11000; the process exits; the origin has 0 `^git-rest:` commits for `tasks/x.md`; and `git diff --cached --name-only` still lists `tasks/x.md` (the work is staged for boot recovery).

Before finishing, re-run the verification block and confirm every command passes; then walk AC6, AC7, AC8, AC11 and AC14 against the change and name, per AC, the requirement that implements it. The ACs above are operator-executed on the host and are deliberately out of scope here — do not run them.
</requirements>

<constraints>
- The buffer is scoped to the file-write path only. Claim-ref PUT/DELETE must never enter it, and a deferred claim push would break exactly-once claim scheduling. Do not change `pkg/git/claim_refs.go`.
- A failed push must never drop the batch: the pending set is cleared only after the push returns nil, and the index is restored so the paths stay staged.
- `DeleteFile` flushes pending writes first, then commits and pushes the deletion synchronously.
- Boot recovery runs before the HTTP server starts and before the startup pull. It is best-effort: a failure is warn-logged and never aborts startup.
- The shutdown flush is bounded by a 10-second deadline; when it is cut short, log exactly one INFO line beginning `git-rest: flush deadline exceeded` and exit anyway. Staged paths remain on disk for boot recovery.
- Do NOT add a shutdown flag, a configurable deadline, or any knob the spec did not ask for. The 10 s deadline is a constant.
- Preserve repo invariants: `github.com/bborbe/errors` (never `fmt.Errorf`), no `context.Background()` in `pkg/`, `log/slog`, metrics pre-initialised in `init()`, interface → constructor → struct → method.
- Do NOT edit `docs/api.md`, `docs/deployment.md` or `CHANGELOG.md` — prompt 3 owns those.
- Do NOT touch the `VERSION` pin or anything outside this repository. No release, image-publish or cluster commands.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass. New code needs ≥ 80% statement coverage; changed code needs its changed paths tested.
- The container has no view of `origin/master` and no operator tooling: do not add `git`, `docker`, `kubectl`, `dark-factory` or `scripts/*` commands to the verification block.
</constraints>

<verification>
Run these from the repo root. Iterate with `make test`; run `make precommit` once at the end.

```
go generate ./...
go test -mod=mod ./pkg/git/... -run 'FlushPushFailureKeepsPending|DeleteFlushesPendingFirst|RecoverPendingWrites|ClaimRefsStaySynchronousWhileWritePending' -v
go test -mod=mod . -v
make test
make precommit
```

`go generate ./...` must regenerate `mocks/git.go` with the new `RecoverPendingWrites` method. `make precommit` must exit 0 (format, generate, test, lint, license).
</verification>
