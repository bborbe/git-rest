---
status: approved
spec: [021-flush-timeout-isolation]
created: "2026-10-22T14:31:10Z"
queued: "2026-10-22T14:43:29Z"
---

<summary>
- A pull cycle whose flush cannot finish still pushes a rescue branch to the remote.
- The regression test reproduces the production failure: a pending write, a diverged remote on the same path, and a remote that stalls pushes to main.
- The test asserts the rescue ref exists on the remote, not merely that the pull returned without error.
- The test also proves the flush was cut by its own deadline rather than by exhausting its retries.
- The test is shown to fail when the bounded-flush call is temporarily reverted, so it genuinely locks the regression.
</summary>

<objective>
Add a regression test in `pkg/git` that reproduces the production starvation — a flush that cannot complete, a diverged remote touching the same path, and a stalled push — and asserts that the dirty-tree rescue still lands a `rescue/*` ref on the remote. Demonstrate that the same test fails against the pre-fix behaviour, proving it locks the regression rather than passing vacuously.
</objective>

<context>
Read the approved spec at `specs/in-progress/021-flush-timeout-isolation.md` — especially § Reproduction, § Acceptance Criteria AC1 and AC6, and § Verification.

Read these files fully before editing:
- `pkg/git/flush_deadline_test.go` — shipped by prompt 1. It defines `installBlockingPreReceive(t, bare)` (a pre-receive hook that sleeps 15 s and rejects `refs/heads/main`, allowing every other ref) and `captureSlogLogsAtDebug`. Reuse `installBlockingPreReceive`; do not duplicate it.
- `pkg/git/coalesce_test.go` — `newCoalesceFixture(t, config)` returns `(git.Git, workPath, bareOriginPath)`; `originGitRestSubjects`; `waitFor`.
- `pkg/git/claim_refs_test.go` — `mustGit(t, dir, args...)` runs git and fails the test on a non-zero exit; `configureClaimIdentity(t, dir)`.
- `pkg/git/git_test.go` — `captureSlogLogs` (buffer-backed text handler at `slog.LevelInfo`).
- `pkg/git/git.go` — `Pull`, `syncWithUpstream`, `fastForwardOrRescue`, `rescueDirtyTree`, `createRescueCommit`, `flushPendingLocked`, `restorePendingIndexLocked`. Read them to understand why the fixture lands in the fast-forward-with-rescue branch.

Coding docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — plain `testing` tests in `package git_test` (Ginkgo's single `TestSuite` entry point cannot be selected with `-run`).
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` — verification and coverage rules.
</context>

<requirements>
## 1. The regression test (`pkg/git/flush_rescue_test.go`, new file, `package git_test`)

Start the file with the repo's license header (copy the three-line copyright header from `pkg/git/coalesce_test.go`).

1.1 Add one test, named exactly `TestFlushRescueLandsAfterFlushDeadline` so the `-run 'TestFlush'` pattern selects it. Build the fixture with all four AC1 preconditions, in this order:

```go
client, _, bare := newCoalesceFixture(t, git.CoalesceConfig{
    Enabled:  true,
    Delay:    time.Hour,
    MaxDelay: time.Hour,
})
```

- **Precondition 1 — a non-empty pending set.** Call `client.WriteFile(context.Background(), "tasks/x.md", []byte("local\n"))`. This goes through the write path, so it enqueues the pending write AND stages the change, leaving the working tree dirty on that path. Do not dirty the tree by writing directly to disk: `flushPendingLocked` returns immediately when the pending set is empty and would consume no budget.
- **Precondition 2 — remote divergence on the same path.** Clone the bare origin into a fresh directory — declare `remote := filepath.Join(t.TempDir(), "remote")` first, then `mustGit(t, filepath.Dir(remote), "clone", bare, remote)` and `configureClaimIdentity(t, remote)`, overwrite `tasks/x.md` with `"remote\n"`, commit it, and push `main` to `origin`. The remote must carry a commit the local branch does not.
- **Precondition 3 — an overlapping dirty path.** The remote commit must touch `tasks/x.md`, the same path the pending write staged, so the merge conflicts and the rescue is attempted. Do not pick a different file.
- **Precondition 4 — a stall that outlasts the whole pull budget.** Install the hook only AFTER the remote-divergence push: `installBlockingPreReceive(t, bare)`. It sleeps 15 s and rejects `refs/heads/main`, which outlasts the test's 10 s context (the unfixed tree has no flush deadline, so a sleep sized against a third of the budget would prove nothing). It allows `refs/heads/rescue/*` immediately.

1.2 Drive `Pull` in-process with an explicit 10 s context — do not boot the binary (`syncOnStartup` uses a hardcoded 60 s ceiling and would resolve the fixture state before the 10 s pull runs):

```go
buf, restore := captureSlogLogs()
defer restore()
pullCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
pullErr := client.Pull(pullCtx)
```

1.3 Assert in this order. The rescue-ref assertion MUST come first and MUST be a `t.Fatalf`, so the failure reported against the pre-fix tree is the missing rescue and not a downstream log assertion:

1. `count := strings.TrimSpace(mustGit(t, bare, "rev-list", "--count", "--branches=rescue/*"))`. If `count == "0"`, fail with a message that includes the count, `pullErr`, and `buf.String()`. The intent is AC1's numeric ref count ≥ 1. Use `--branches=rescue/*`, not a bare `refs/heads/rescue/*` revision argument: `git rev-list` rejects a bare glob ref with a usage error (exit 129), while `--branches=PATTERN` (e.g. `--branches=rescue/*`) returns `0` for no match and the count for matches. (This is a correction to the literal command in the spec's AC1; flag it in your report's `## Improvements`.)
2. The captured log contains `rescue branch pushed`.
3. The captured log contains no `git [add -A]` substring — on a correct build the rescue's `git add -A` runs to completion and is never logged inside an error. This is the negative log match from AC1.
4. `strings.Count(buf.String(), "git-rest: flush deadline exceeded before pull") >= 1` (AC2). The trailing `before pull` is required: `main.go` logs `git-rest: flush deadline exceeded` on SIGTERM, and a prefix grep would match that unrelated path.

5. **Fixture self-guard:** the captured log contains `git-rest: flush pending writes before pull failed`. Without it a silently-empty pending set would make the fixture prove nothing — the flush would return immediately, no budget would be consumed, and assertion 1 could pass on a tree that never exercised the defect. Add this as a separate assertion after assertion 1 so it cannot mask the rescue failure.

Do not assert that `Pull` returned nil as a success criterion — the assertion is the rescue ref on the remote. `pullErr` may be included in failure messages for diagnosis.

1.4 The test must not depend on the `pull budget` log line for its failure. If the budget line were deleted, the test must still fail on the missing rescue ref against the pre-fix behaviour.

## 2. Prove the test locks the regression

The fix from prompt 1 is already in the tree, so a passing run alone does not prove the test catches the bug. Demonstrate both directions and record the evidence:

1. Run `go test -mod=mod ./pkg/git -run 'TestFlushRescueLandsAfterFlushDeadline' -count=1 -v` on the fixed tree. It must PASS.
2. Temporarily revert ONLY the bounded-flush call in `Pull` (`pkg/git/git.go`): change `g.flushPendingLocked(flushCtx)` to `g.flushPendingLocked(ctx)` and add `_ = flushCtx` immediately after the `if hasPullDeadline { … }` block — removing the only read of `flushCtx` otherwise leaves it "declared and not used" and the package will not compile. Leave the budget log line and everything else in place; remove the `_ = flushCtx` line when you restore in step 4.
3. Re-run the same test. It must FAIL, and the failure must be assertion 1 — the message reporting that no rescue ref was pushed to origin. If it fails on a log assertion instead, the fixture is wrong: fix the fixture until the missing rescue is the reported failure.
4. Restore `g.flushPendingLocked(flushCtx)`, re-run, and confirm it PASSES again. The tree must end in the fixed state — the revert is a temporary diagnostic, not part of the change.
5. Record the test name and the captured pre-fix failure output in the completion report's `## Verification` section, as AC6 requires.

If step 3 fails for an unrelated reason (for example the hook does not block the push, or the remote commit does not touch the same path), fix the fixture rather than weakening the assertion.

## 3. Self-check

Before finishing, re-run the verification block, walk AC1 and AC6 against the change, and confirm no other test changed behaviour.
</requirements>

<constraints>
- The fixture must satisfy all four preconditions. Any one missing means the unfixed tree also satisfies the acceptance criterion, so the test proves nothing.
- Assert the rescue ref on the REMOTE (the bare origin), not a local `refs/heads/rescue/*` ref.
- The test must not assert merely that `Pull` returned no error.
- Do NOT change `pkg/git/git.go` in the final state of this prompt. The only edit there is the temporary revert in step 2, which must be restored before finishing.
- Do NOT duplicate `installBlockingPreReceive` or modify `pkg/git/flush_deadline_test.go`.
- Do NOT weaken the hook's sleep below the test's 10 s context, and do not let it sleep on `rescue/*` pushes — the rescue must land inside the remaining budget.
- Preserve repo invariants: errors wrapped with `github.com/bborbe/errors` (never `fmt.Errorf`), no `context.Background()` in `pkg/` production code, `log/slog` for logging.
- Do NOT add a flag, metric, config field or any other knob.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass. New code needs ≥ 80% statement coverage.
- The container has no view of `origin/master` and no operator tooling: do not add `docker`, `kubectl`, `dark-factory`, `scripts/*` or bare `git` commands to the verification block.
</constraints>

<verification>
Run from the repo root. Iterate with `make test`; run `make precommit` once at the end.

```
go test -mod=mod ./pkg/git -run 'TestFlushRescueLandsAfterFlushDeadline' -count=1 -v
go test -mod=mod ./pkg/git -run 'TestFlush' -count=1 -v
make test
make precommit
```

All must exit 0 in the final state. The pre-fix failure is captured during requirement step 2 and reported in `## Verification`; it is not part of the final verification block.
</verification>
