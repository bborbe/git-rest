---
status: completed
spec: [022-bug-coalesced-flush-discards-the-commit]
summary: Coalesced flushes now retain their commit and clear the pending set on a failed push, delivering it via the existing pull state machine, with rewritten tests, docs, spec-021 supersession and changelog.
execution_id: git-rest-exec-087-spec-022-retain-coalesced-flush-commit
dark-factory-version: v0.198.7
created: "2026-10-24T20:48:38Z"
queued: "2026-10-24T21:10:43Z"
started: "2026-10-24T21:10:46Z"
completed: "2026-10-24T21:24:52Z"
branch: dark-factory/bug-coalesced-flush-discards-the-commit
---

<summary>
- A coalesced write whose push fails keeps the commit it just made instead of resetting the branch below it.
- The failed flush clears the pending set and its gauge, so the same paths are never re-committed on top of their own commit.
- The next pull delivers the retained commit through the existing state machine — it pushes it when the remote has not moved, or merges and resolves it when it has.
- A modify/modify divergence therefore reaches the pull path's conflict machinery, so the vault-api merge resolver fires under the default coalescing configuration.
- A flush cut by its own deadline follows the same rule; the dirty-tree rescue is no longer its recovery path.
- The failure is visible as one WARN record naming the retained commit's SHA and path count, with no new Prometheus series.
- The two tests introduced for the bounded flush are rewritten to the new contract, and the failed-push durability test is inverted under its original name.
- The superseded "paths stay staged" claim is named in the changelog and rewritten in the deployment doc, and the earlier spec records the supersession.
- The non-coalesced write path, the HTTP contract, the coalescing default and the flush budget split are unchanged.
</summary>

<objective>
Make a failed coalesced flush retain its commit and clear the pending set, so a write is never unmade by a delivery failure. This restores the pull path's conflict machinery — and with it the vault-api merge resolver — for every write-enabled vault, which runs coalescing by default.
</objective>

<context>
Read the approved spec `specs/in-progress/022-bug-coalesced-flush-discards-the-commit.md` in full — especially § Goal, § Desired Behavior, § Constraints, § Failure Modes, § Acceptance Criteria and § Verification.

Read these files fully before editing:
- `pkg/git/git.go` — `flushPendingLocked`, `restorePendingIndexLocked`, `pushWithRetry`, `retryPush`, `Pull` (the flush call and its cut-off record), `syncWithUpstream`, `writeFileLocked`, `enqueuePendingLocked`, `RecoverPendingWrites`. `g.mu` is held for the whole of `flushPendingLocked`, `Pull` and `DeleteFile`.
- `pkg/git/coalesce_durability_test.go` — contains `TestFlushPushFailureKeepsPending`, the test to invert, plus the `TestDeleteFlushesPendingFirst` / `TestRecoverPendingWrites*` tests that must keep passing.
- `pkg/git/flush_deadline_test.go` — contains `installBlockingPreReceive`, `captureSlogLogsAtDebug`, `TestFlushDeadlineCut` (to rewrite), and the tests that must keep passing.
- `pkg/git/flush_rescue_test.go` — contains `setupFlushRescueFixture` and `TestFlushRescueLandsAfterFlushDeadline` (to rewrite).
- `pkg/git/coalesce_test.go` — `newCoalesceFixtureWithMetrics(t, config)` returns `(git.Git, *mocks.FakeMetrics, workPath, bareOriginPath)`; `newCoalesceFixture` is its three-value form; `originGitRestSubjects(t, bare, branch, path)` filters to `git-rest:` subjects.
- `pkg/git/claim_refs_test.go` — `mustGit(t, dir, args ...string) string` (fails the test on a non-zero exit) and `configureClaimIdentity(t, dir)`.
- `pkg/git/git_test.go` — `captureSlogLogs()` (buffer-backed text handler at `slog.LevelInfo`, so WARN is captured), `shellQuote(path string) string`.
- `pkg/metrics/metrics.go` — the `Metrics` interface: `IncWriteFlush(outcome string)`, `SetWritePending(count int)`, `IncMergeOutcome(result string)`, `IncConflictPaths(n int)`, `IncPullRescue()`.
- `docs/deployment.md` — § Write coalescing and the write-coalescing rows of the metrics table.
- `CHANGELOG.md` — the `## Unreleased` section and the released `v0.34.1` entry (released history; do not edit it).
- `specs/in-progress/021-flush-timeout-isolation.md` — § Goal, § Assumptions, § Failure Modes.

Coding docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — plain `testing` tests in `package git_test` (the Ginkgo `TestSuite` entry point cannot be selected with `-run`).
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors`, never `fmt.Errorf`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md` — `log/slog` for new logging.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — bullet format and `## Unreleased` rules.
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` — coverage and verification rules.

<!-- Review note for the human auditor: the spec's § Assumptions says the rewritten `TestFlushDeadlineCut` keeps "a Pull that returns without error". That only holds pre-fix (where the reset makes local == remote). Post-fix the retained commit makes the branch ahead, so the pull's own push runs and the existing all-push-rejecting hook would make `Pull` return an error. Requirement 3 resolves this with a first-push-only rejection hook, so the flush is still cut at its deadline and the pull's delivery push succeeds. The spec text is not changed by this prompt; flag the discrepancy at audit time. -->
</context>

<requirements>
## 1. Retain the commit in `flushPendingLocked` (`pkg/git/git.go`)

1.1 In `func (g *git) flushPendingLocked(ctx context.Context) error`, delete the pre-flush HEAD capture. Remove these two statements (they exist only to feed the reset):

```go
	preFlushHEAD, headErr := g.runCmdOutput(ctx, g.repoPath, "rev-parse", "HEAD")
	preFlushSHA := strings.TrimSpace(string(preFlushHEAD))
```

1.2 Replace the push-failure branch. Old:

```go
	pushOutcome := "skipped"
	if g.hasRemote(ctx) {
		if err := g.pushWithRetry(ctx, ""); err != nil {
			g.restorePendingIndexLocked(ctx, preFlushSHA, headErr)
			g.metrics.IncWriteFlush("error")
			return errors.Wrap(ctx, err, "git push")
		}
		pushOutcome = "ok"
	}
```

New:

```go
	pushOutcome := "skipped"
	if g.hasRemote(ctx) {
		if pushErr := g.pushWithRetry(ctx, ""); pushErr != nil {
			g.retainCommitAfterFailedPushLocked(ctx, paths, pushErr)
			g.metrics.IncWriteFlush("error")
			return errors.Wrap(ctx, pushErr, "git push")
		}
		pushOutcome = "ok"
	}
```

The `paths` slice is the already-sorted batch path list built earlier in the method. Keep the successful-flush tail (`g.pending = make(...)`, `SetWritePending(0)`, `IncWriteFlush("ok")`, the `git-rest: flushed pending writes` INFO record) exactly as it is.

1.3 Add this new method immediately after `flushPendingLocked`. Keep the comment verbatim — it states the contract the spec's § Why this is a bug and § Desired Behavior 6 depend on:

```go
// retainCommitAfterFailedPushLocked keeps the commit a failed flush push left at
// HEAD and clears the pending set, so a delivery failure never unmakes the
// write: the commit stays in local history for the next pull to push or merge.
// The HEAD read runs without cancellation because a flush cut by its own
// deadline still holds a committed HEAD, and rev-parse is a fast local
// operation.
func (g *git) retainCommitAfterFailedPushLocked(
	ctx context.Context,
	paths []string,
	pushErr error,
) {
	headOut, headErr := g.runCmdOutput(
		context.WithoutCancel(ctx), g.repoPath, "rev-parse", "HEAD",
	)
	g.pending = make(map[string]pendingWrite)
	g.metrics.SetWritePending(0)
	if headErr != nil {
		slog.WarnContext(
			ctx,
			"git-rest: flush push failed, commit retained",
			"paths", len(paths),
			"error", pushErr.Error(),
			"head_error", headErr.Error(),
		)
		return
	}
	slog.WarnContext(
		ctx,
		"git-rest: flush push failed, commit retained",
		"commit", strings.TrimSpace(string(headOut)),
		"paths", len(paths),
		"error", pushErr.Error(),
	)
}
```

The record is emitted exactly once per failed flush. Its `commit` field is the SHA HEAD holds at failure time — the flush commit, or a merge commit when `retryPush`'s merge succeeded and the following push failed. `paths` is `len(paths)`.

1.4 Delete `restorePendingIndexLocked` entirely, including its doc comment. It has exactly one caller (the branch replaced in 1.2); verify with `! grep -rn 'restorePendingIndexLocked' --include='*.go' .` (exit 0 means no match, i.e. success) that no other call site remains in `pkg/` or `main.go`.

1.5 Do NOT change the commit-step failure branch. When `git commit` itself fails (index lock, disk full), nothing is committed: keep `IncWriteFlush("error")` and the wrapped error, leave `g.pending` and the gauge untouched, and emit no retained-commit record.

1.6 Do NOT change the `hasRemote(ctx) == false` path: it skips the push, clears the pending set, and records `outcome=ok` exactly as today. `RecoverPendingWrites` already satisfies the retained-commit contract — it commits, and a failed push returns with the commit at HEAD and the pending set untouched — so it is not modified either.

1.7 Update the `flushPendingLocked` doc comment from "commits and pushes the pending write set" to also state the retained-commit rule: a push failure or a flush cut at its own deadline leaves the commit at HEAD, clears the pending set, and returns the push error. Do not change the `Git` interface comment on `FlushPending` — it stays accurate.

## 2. Correct the push-retry doc comment (`retryPush`, `pkg/git/git.go`)

2.1 Replace the last sentence of `retryPush`'s doc comment. Old:

```go
// failure aborts the half-finished merge so the pull path's conflict machinery
// is never entered from here.
```

New (behavior is unchanged — only the claim is corrected):

```go
// failure aborts the half-finished merge so the repository is left clean and the
// error surfaces; the caller retains its commit (or the merge commit, when this
// merge succeeded and the following push failed), and the next pull's
// diverged-history path is where that conflict reaches the conflict machinery.
```

2.2 Do not start resolving conflicts inside `retryPush`, and keep the `merge --abort` call and the `git-rest: retry merge failed` WARN.

## 3. Rewrite `TestFlushDeadlineCut` (`pkg/git/flush_deadline_test.go`)

3.1 Add a helper next to `installBlockingPreReceive`, in the same file. Do NOT modify `installBlockingPreReceive` — the rewritten rescue test in requirement 4 still uses it. The new helper rejects only the FIRST update to `refs/heads/main`, after stalling past the flush deadline, and accepts every later push:

```go
// installFirstPushRejection installs a pre-receive hook that rejects the FIRST
// push to refs/heads/main after stalling past the flush deadline and accepts
// every later push. The flush's push is the one that is cut; the pull's own
// delivery push in the same cycle succeeds, so Pull returns without error on a
// correct build. The sentinel is created by the hook itself.
func installFirstPushRejection(t *testing.T, bare string) {
	t.Helper()
	hooksDir := filepath.Join(bare, "hooks")
	if err := os.MkdirAll(hooksDir, 0o750); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	sentinel := shellQuote(filepath.Join(bare, "first-push-rejected"))
	script := "#!/bin/sh\n" +
		"input=$(cat)\n" +
		"case \"$input\" in\n" +
		"  *refs/heads/main*)\n" +
		"    if [ -f " + sentinel + " ]; then exit 0; fi\n" +
		"    : > " + sentinel + "\n" +
		"    gp=$(ps -o ppid= -p \"$PPID\" 2>/dev/null | tr -d ' ')\n" +
		"    gpp=$(ps -o ppid= -p \"$gp\" 2>/dev/null | tr -d ' ')\n" +
		"    i=0\n" +
		"    while [ \"$i\" -lt 75 ]; do\n" +
		"      if [ -n \"$gpp\" ] && ! ps -p \"$gpp\" >/dev/null 2>&1; then\n" +
		"        exit 1\n" +
		"      fi\n" +
		"      sleep 0.2\n" +
		"      i=$((i+1))\n" +
		"    done\n" +
		"    echo \"remote rejected: first push\" >&2\n" +
		"    exit 1\n" +
		"    ;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-receive"), []byte(script), 0o750); err != nil {
		t.Fatalf("write pre-receive hook: %v", err)
	}
	mustGit(t, bare, "config", "core.hooksPath", "hooks")
}
```

The ancestor poll is load-bearing: without it the killed `git push` waits on `gitWaitDelay` (2 s) and the elapsed-time bound in 3.3 becomes unreliable.

3.2 Rewrite the test body. Use the metrics-aware fixture so the pending gauge is assertable:

```go
client, fake, work, bare := newCoalesceFixtureWithMetrics(t, git.CoalesceConfig{
	Enabled:  true,
	Delay:    time.Hour,
	MaxDelay: time.Hour,
})
```

Keep `client.WriteFile(ctx, "tasks/x.md", []byte("L"))` (a NEW path in this fixture, so the commit subject is `create`), then `installFirstPushRejection(t, bare)`, then `captureSlogLogs()`, then drive `client.Pull(pullCtx)` with a 10 s context and measure the elapsed time as today.

3.3 Assert, in this order:
1. `Pull` returns nil — a non-nil error is a failure (`t.Fatalf` including the error and the captured log).
2. The cut flush's commit is retained at HEAD: `strings.TrimSpace(mustGit(t, work, "log", "-1", "--format=%s"))` equals `git-rest: create tasks/x.md`.
3. The tree is clean: `strings.TrimSpace(mustGit(t, work, "status", "--porcelain"))` is empty.
4. The retained commit was delivered in the same cycle: `strings.TrimSpace(mustGit(t, work, "rev-list", "--count", "origin/main..HEAD"))` equals `0`, and `originGitRestSubjects(t, bare, "main", "tasks/x.md")` has length 1 with subject `git-rest: create tasks/x.md`.
5. The pending gauge returned to zero: the last `fake.SetWritePendingArgsForCall(...)` argument is `0`.
6. The flush recorded an error outcome: the last `fake.IncWriteFlushArgsForCall(...)` argument is `"error"`.
7. The captured log contains `git-rest: flush deadline exceeded before pull`.
8. `elapsed < 6*time.Second`.

Do not keep the old "want tasks/x.md still staged" assertion or its message; the string `still staged` must no longer appear in this file.

## 4. Rewrite `TestFlushRescueLandsAfterFlushDeadline` (`pkg/git/flush_rescue_test.go`)

4.1 Keep `setupFlushRescueFixture`'s setup, `rescueFixtureBase` and `installBlockingPreReceive` as they are. The fixture's four preconditions are unchanged: a pending write through the write path, a remote commit on the same path, an overlapping edit, and a hook that stalls past the 10 s pull context while allowing `rescue/*`. The ONE change to the fixture is its return: it currently returns `(git.Git, string)`; add the work-clone path so the body can read the served repo — `func setupFlushRescueFixture(t *testing.T) (git.Git, string, string)` returning `(client, work, bare)`, with the body destructuring `client, work, bare := setupFlushRescueFixture(t)`. Change nothing else inside it.

4.2 Rewrite the test body's assertions. The old contract (a `rescue/*` ref on the remote) is superseded. Assert, in this order:
1. No rescue ref reaches the origin: `strings.TrimSpace(mustGit(t, bare, "rev-list", "--count", "--branches=rescue/*"))` equals `"0"`. This assertion comes first and is fatal, so a pre-fix run fails on the rescue rather than downstream; include `pullErr` and `buf.String()` in the message.
2. The cut flush's commit survives into the pull's merge: `strings.TrimSpace(mustGit(t, work, "rev-list", "--count", "origin/main..HEAD"))` equals `"2"` (the flush commit and the clean merge commit, neither pushed because the origin stalls).
3. `mustGit(t, work, "log", "--format=%s")` contains the line `git-rest: update tasks/x.md`.
4. The tree is clean: `strings.TrimSpace(mustGit(t, work, "status", "--porcelain"))` is empty.
5. The captured log contains `git-rest: flush deadline exceeded before pull`.
6. The captured log contains `git-rest: flush pending writes before pull failed` (the fixture self-guard).
7. The captured log does NOT contain `rescue branch pushed`.

4.3 Do not assert that `Pull` returned nil. `pullErr` is non-nil here: the clean merge's push is stalled by the hook and killed at the pull deadline. Include it only in failure messages.

4.4 Update the test's doc comment: it now proves a cut flush retains its commit, the next pull merges it, and the dirty-tree rescue is no longer the recovery path.

## 5. Invert `TestFlushPushFailureKeepsPending` (`pkg/git/coalesce_durability_test.go`)

5.1 Keep the function name exactly `TestFlushPushFailureKeepsPending` — the inversion is auditable against the pre-change tree only under its original name. Keep the fixture (`newCoalesceFixtureWithMetrics`), the `notes/x.md` write of `"P"`, the `exit 1` pre-receive hook, and the `mustGit(t, bare, "config", "core.hooksPath", "hooks")` guard. Add `buf, restore := captureSlogLogs()` with `defer restore()` immediately before the first `FlushPending`, and remove the hook afterwards as today.

5.2 Replace the doc comment with one stating what the name now covers, e.g.: "TestFlushPushFailureKeepsPending proves a failed coalesced push retains its commit and clears the pending set, so the same batch is never re-committed on top of its own commit, and the next pull delivers the retained commit once the remote accepts pushes again."

5.3 After `client.FlushPending(ctx)` returns an error, assert:
1. `strings.TrimSpace(mustGit(t, work, "log", "-1", "--format=%s"))` equals `git-rest: update notes/x.md`.
2. `strings.TrimSpace(mustGit(t, work, "status", "--porcelain"))` is empty.
3. `strings.TrimSpace(mustGit(t, work, "rev-list", "--count", "origin/main..HEAD"))` equals `"1"`.
4. `originGitRestSubjects(t, bare, "main", "notes/x.md")` has length 0 (the retained commit is local only).
5. The last `fake.SetWritePendingArgsForCall(...)` argument is `0` (replaces the old `> 0` assertion).
6. The last `fake.IncWriteFlushArgsForCall(...)` argument is `"error"`.
7. `buf.String()` contains `git-rest: flush push failed, commit retained`, the substring `paths=1`, and a `commit=([0-9a-f]{40})` match whose value equals `strings.TrimSpace(mustGit(t, work, "rev-parse", "HEAD"))`. Use `regexp.MustCompile` and fail with the captured log when there is no match.

5.4 After removing the hook and calling `client.Pull(ctx)` (require nil), assert the delivery: `originGitRestSubjects(t, bare, "main", "notes/x.md")` is exactly one subject `git-rest: update notes/x.md`; `mustGit(t, bare, "show", "main:notes/x.md")` is `P`; the ahead count is `"0"`; the work tree is clean. Do not assert a final `ok` flush outcome — the pull's flush is a no-op with an empty pending set.

## 6. Add the regression test (`pkg/git/flush_retained_commit_test.go`, new file, `package git_test`)

Start the file with the repo's three-line license header (copy it from `pkg/git/coalesce_test.go`).

6.1 Add one test named `TestFlushRetainsCommitOnRejectedPush`. Build it as follows:
1. `newCoalesceFixtureWithMetrics` with `git.CoalesceConfig{Enabled: true, Delay: time.Hour, MaxDelay: time.Hour}`.
2. `client.WriteFile(ctx, "notes/x.md", []byte("LOCAL\n"))` — the fixture tracks `notes/x.md`, so this is an update and stages the path.
3. Advance the remote on the SAME path: `remote := filepath.Join(t.TempDir(), "remote")`; `mustGit(t, filepath.Dir(remote), "clone", bare, remote)`; `configureClaimIdentity(t, remote)`; overwrite `filepath.Join(remote, "notes", "x.md")` with `"REMOTE\n"`; `mustGit(t, remote, "add", "-A")`; `mustGit(t, remote, "commit", "-m", "remote change")`; `mustGit(t, remote, "push", "origin", "main")`. Do this AFTER the write, so the local branch and the remote both moved on the same path.
4. `buf, restore := captureSlogLogs()` with `defer restore()`, then require `client.FlushPending(ctx)` to return an error (the push is rejected as non-fast-forward, the retry merge conflicts on `notes/x.md`, and the retry aborts).

6.2 After the failed flush, assert: HEAD subject `git-rest: update notes/x.md`; clean work tree; ahead count `"1"`; the last `fake.SetWritePendingArgsForCall(...)` argument is `0`; `buf.String()` contains `git-rest: flush push failed, commit retained`.

6.3 Then `client.Pull(ctx)` must return nil, and:
1. `strings.TrimSpace(mustGit(t, bare, "rev-list", "--count", "--branches=rescue/*"))` equals `"0"`.
2. HEAD subject matches `^merge: resolved=\[notes/x\.md\] quarantined=\[\]$` (`regexp.MustCompile`).
3. The ahead count is `"0"` and the work tree is clean — the merge commit was pushed.
4. The last `fake.IncMergeOutcomeArgsForCall(...)` argument is `"resolved"`, the last `fake.IncConflictPathsArgsForCall(...)` argument is `1`, and `fake.IncPullRescueCallCount()` is `0`.

6.4 This test must fail on the pre-change tree by assertion, not by compile error: it may only use helpers that already exist (`newCoalesceFixtureWithMetrics`, `mustGit`, `configureClaimIdentity`, `captureSlogLogs`) and the public `git` package API.

## 7. Amend `specs/in-progress/021-flush-timeout-isolation.md`

7.1 Insert this blockquote immediately after the closing `---` of the frontmatter, before `## Summary`:

```markdown
> **Superseded in part by `022-bug-coalesced-flush-discards-the-commit`.** A flush that committed now keeps its commit when its push fails or is cut at its own deadline, and clears the pending set: the paths are no longer left staged, and the dirty-tree rescue is no longer a cut flush's recovery path. What this spec established and 022 keeps is the budget rule — the pre-pull flush stays within one third of the pull budget — and the exact `git-rest: flush deadline exceeded before pull` record. `TestFlushDeadlineCut` and `TestFlushRescueLandsAfterFlushDeadline` were rewritten to the retained-commit contract.
```

7.2 Annotate the superseded claims in place (do not rewrite their history):
- § Goal, after "The flush is bounded; the merge and rescue are not.", append: `*(Superseded by 022: a cut flush retains its commit and the rescue is no longer its recovery path; the budget rule stands.)*`
- § Assumptions, the bullet beginning "A rejected flush is recoverable", append: `*(Superseded by 022: a flush that committed retains its commit and clears the pending set, so recovery rests on the retained commit rather than on staged work surviving on disk.)*`
- § Failure Modes, the row whose trigger is "Flush killed after the remote accepted the ref": replace that row's Expected-behavior cell with `**Superseded by 022:** the commit is retained, the pending set is cleared, and the next pull pushes or merges it — no re-commit.` Keep the Recovery cell.
- § Failure Modes, the row whose trigger is "Remote very slow but not rejecting": replace that row's Expected-behavior cell with `Flush cut at one third; **superseded by 022:** the commit is retained and the pending set cleared, and the next pull delivers it.` Keep the Recovery cell.

7.3 Do not change 021's frontmatter, its Acceptance Criteria, or its Verification section.

## 8. Documentation (`docs/deployment.md`)

8.1 In § Write coalescing, replace the whole paragraph that begins "On SIGTERM the process flushes and pushes pending writes before exiting" with:

```markdown
On SIGTERM the process flushes and pushes pending writes before exiting, bounded by a 10 s deadline; a flush cut short logs `git-rest: flush deadline exceeded`. A pull cycle bounds its pre-merge flush to one third of the remaining pull budget (`--pull-timeout`, or the 60 s startup ceiling), so a contended push cannot consume the whole deadline. A flush cut short by that bound logs `git-rest: flush deadline exceeded before pull`, and the cycle still merges and runs the dirty-tree rescue on the remaining budget. A flush that committed keeps its commit when the push fails — whether the remote rejected it or the flush was cut at its own deadline: HEAD is never moved below the commit, the working tree and index stay clean, the pending set is cleared, and one WARN `git-rest: flush push failed, commit retained` names the retained commit's SHA and its path count. The next pull delivers that retained commit through its ordinary state machine — pushing it when the remote has not moved, or merging and resolving it when it has — so a failed push never discards a write and a modify/modify divergence reaches the conflict machinery. Every pull cycle logs `git-rest: pull budget pull_start=… pull_deadline=… flush_deadline=…`; `flush_deadline=none` when the context carries no deadline. On boot, paths left staged by a crash are committed and pushed before the HTTP server starts listening, logging `git-rest: recovered pending writes` with the path count.
```

Both "leaves the paths staged" clauses must be gone from this file.

8.2 In the write-coalescing metrics table, replace the `git_rest_write_pending` row's meaning with: `Gauge: paths currently staged and waiting for a coalesced commit. Returns to 0 once a flush commits the batch, including when its push fails and the commit is retained.`

## 9. Changelog (`CHANGELOG.md`)

9.1 Append one bullet to the existing `## Unreleased` section (do not replace the existing bullet, do not touch any released section, including `v0.34.1`):

```markdown
- fix: Keep the commit a coalesced flush made when its push fails instead of resetting HEAD below it and leaving the paths staged. A rejected push, or a flush cut at its own deadline, now leaves the write as a real local commit, clears the pending set and `git_rest_write_pending`, and logs `git-rest: flush push failed, commit retained` with the commit SHA and path count; the next pull pushes that retained commit or merges it, so a modify/modify divergence reaches the pull path's conflict machinery and the vault-api resolver fires under the default coalescing configuration. Supersedes the v0.34.1 rule that a cut flush leaves its paths staged for the next cycle, and the rescue-after-cut recovery that depended on it.
```

## 10. Self-check before finishing

10.1 Re-run the `<verification>` block and confirm every command exits 0.
10.2 Walk § Desired Behavior 1-8 and the Failure Modes table of `specs/in-progress/022-bug-coalesced-flush-discards-the-commit.md` against the change: a rejected push, a retry-merge-then-push failure, a deadline cut, a transient network failure, a throttled push, a pod kill between commit and clear, a failing commit step, and a resolver failure all end in the state the table names.
10.3 Confirm no new Prometheus series, flag, config field or chart value was added, and that `WRITE_COALESCE`'s default is untouched.
</requirements>

<constraints>
- **Do not change `WRITE_COALESCE`'s default.** It stays `true`, and no flag, environment variable, or chart value is added to make the fix opt-in.
- **Spec 021's "leaves the paths staged" guarantee is superseded**, together with the rescue-after-cut claim that depends on it. What 021 established and this change keeps is the budget rule — the pre-pull flush stays at most a third of the pull budget — and the exact `git-rest: flush deadline exceeded before pull` record text.
- **The budget split stays as spec 021 left it.** Do not widen the budget, do not move the delivery push back into the flush, and do not add a separate push budget.
- **Do not touch the vault-api.** Its merge endpoint, request and response shapes, `apply: false` contract, gateway-secret header, and caller header are unchanged.
- **The push-retry helper must not hide the conflict either.** `retryPush` still aborts its own half-finished merge; only its doc comment is corrected. Do not start resolving conflicts inside the retry.
- **Errors are wrapped with `github.com/bborbe/errors`** — never `fmt.Errorf`.
- **New logging uses `log/slog`** (`slog.WarnContext`), never `fmt.Print*`. `pkg/git/yaml_merge_resolver.go` keeps `glog` and is not touched; never mix the two loggers in one file.
- **Timestamps come from the injected `libtime` getter**, never `time.Now()` inside `pkg/`.
- **No `context.Background()` in `pkg/`.** `context.WithoutCancel(ctx)` is the only detaching call allowed, and only for the fast local `rev-parse HEAD` read in the new helper.
- **`git_rest_write_flushes_total{outcome="error"}` keeps its meaning**: it still increments once per failed coalesced flush, a cut included. The WARN record is additional, not a replacement, and no new Prometheus series is introduced.
- **No change to the public HTTP contract** in `docs/api.md`: a coalesced POST still returns 200 before any push is attempted, and the push failure stays asynchronous and invisible to that request. No new endpoint, status code, or response field.
- **The commit-subject format is unchanged** — `git-rest: create <path>`, `git-rest: update <path>`, `git-rest: update <n> files`.
- **The retained commit is delivered by the existing pull state machine.** No new retry loop, timer, or goroutine pushes it.
- **Single-writer assumption unchanged** (`replicas: 1`). No cross-pod coordination is added.
- **The durability test keeps its original function name** `TestFlushPushFailureKeepsPending`; a comment on the function states what the name now covers.
- **The CHANGELOG bullet and the `docs/deployment.md` § Write coalescing update are part of this change**, not follow-up work. The v0.34.1 CHANGELOG entry is released history and is not edited.
- **The rejected alternatives stay rejected.** Do not add a startup warning about an unreachable resolver, and do not keep the cut staged.
- **The non-coalesced path (`writeFileLocked`) is unchanged**: it commits, surfaces the push error to the caller, keeps its commit, and touches neither the pending set nor the flush counters.
- **AC 8(c)'s pre-fix replay** (`git worktree add /tmp/git-rest-prefix cac512e`, copy the new test, `go vet` / `go test`) is assigned by the spec's Verification section to its operator rung. Do not attempt it in the container; the inverted durability test and the new regression test are the container-side evidence.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass. New and changed code needs ≥ 80% statement coverage.
- The container has no view of `origin/master` and no operator tooling: do not add `docker`, `kubectl`, `dark-factory`, `scripts/*` or bare `git` commands to the verification block.
</constraints>

<verification>
Run from the repo root. Iterate with `make test`; run `make precommit` once at the end.

```
make test
make precommit
go test -mod=mod ./pkg/git/ -count=1
go test -mod=mod ./pkg/git/ -run 'TestFlush' -count=1 -v
! grep -q 'still staged' pkg/git/flush_deadline_test.go
test "$(grep -rl 'func TestFlushPushFailureKeepsPending' pkg/git/ | wc -l)" -eq 1
test -f pkg/git/flush_retained_commit_test.go
grep -q 'func TestFlushRetainsCommitOnRejectedPush' pkg/git/flush_retained_commit_test.go
grep -q 'func installFirstPushRejection' pkg/git/flush_deadline_test.go
! grep -q 'leaves the paths staged' docs/deployment.md
grep -c 'retained commit' docs/deployment.md
grep -c 'retained commit' CHANGELOG.md
awk '/^## /{sec=$0} /retained commit/{print sec}' CHANGELOG.md | grep -q '^## Unreleased$'
awk '/^## /{sec=$0} /v0\.34\.1/{print sec}' CHANGELOG.md | grep -q '^## Unreleased$'
grep -q 'Superseded in part by' specs/in-progress/021-flush-timeout-isolation.md
```

All must exit 0. The two negative checks use `! grep -q` rather than `grep -c` so a correct absence exits 0 rather than the `1` that `grep -c` returns when it prints `0`.
</verification>
