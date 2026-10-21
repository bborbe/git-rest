---
status: completed
summary: Retry-merge failures now log git's trimmed output as a structured warning and return an error leading with the underlying cause instead of git's stdout; added a real-repo regression test and a CHANGELOG entry.
execution_id: git-rest-merge-err-exec-073-merge-error-text
dark-factory-version: v0.198.7
created: "2026-10-21T21:11:35Z"
queued: "2026-10-21T21:11:35Z"
started: "2026-10-21T21:16:37Z"
completed: "2026-10-21T21:18:08Z"
---

# Report a failed retry-merge by its cause, not by git's stdout

<summary>
- A retry merge that fails now reports why it failed
- The subprocess output moves to a structured log line instead of the error message
- A merge killed by the operating system no longer reads as a successful merge
- Existing merge-error assertions keep working
- The merge is still aborted when it fails, so no half-finished merge is left behind
</summary>

<objective>
Make a failed retry merge state its cause. Today the error message leads with git's stdout, so a merge whose process was killed reports "merge origin/main: Merge made by the 'ort' strategy." and the real cause trails after it — a message that reads like success while the request has already failed.
</objective>

<context>
Read `pkg/git/git.go`, the `retryPush` method. Its merge step is the last statement before the function returns:

```go
mergeOut, mergeErr := g.runCmdRaw(ctx, g.repoPath, "merge", "--no-edit", upstream)
if mergeErr != nil {
    _, _ = g.runCmdRaw(ctx, g.repoPath, "merge", "--abort")
    return errors.Wrapf(ctx, mergeErr, "merge %s: %s", upstream, mergeOut)
}
```

`runCmdRaw` returns git's combined output alongside the error. When the process is killed, the output can still contain git's progress or success text, so formatting the output into the message produces the misleading string observed in production:

`write file: git push: merge origin/main: Merge made by the 'ort' strategy.: signal: killed`

`pkg/git/git.go` already logs structured warnings with `slog.WarnContext` elsewhere in the file; follow that style, including the `"err"` key the existing failure paths use.
</context>

<requirements>
1. In `retryPush`, change the failed-merge branch to log the subprocess output and return an error that leads with the cause:
   - log `slog.WarnContext(ctx, "git-rest: retry merge failed", "upstream", upstream, "err", mergeErr.Error(), "output", <trimmed combined output>)`
   - return `errors.Wrapf(ctx, mergeErr, "merge %s failed", upstream)`
   The resulting message for the production case must read `merge origin/main failed: signal: killed` — the cause, with no success text ahead of it.

2. Keep the `git merge --abort` call before returning, in the same place it is today.

3. Trim the output before logging it (`strings.TrimSpace`), so multi-line git output does not produce a log value with a trailing newline.

4. Add a test in `pkg/git` that a failed retry merge produces an error whose message does not contain the merge's stdout and does end with the underlying cause, and that the output was logged (`captureSlogLogs` in `pkg/git/git_test.go`) as `git-rest: retry merge failed`.

   Drive it through the same real-repo seam the other retry specs use: `installRemoteAdvanceOnFetch(workDir, true, "conflict.txt")` in `pkg/git/git_test.go` (see the "aborts the merge and surfaces the error when the retry merge conflicts" test) produces a real failing `git merge` with conflict text in its combined stdout+stderr output. Assert the returned message does not contain the conflict text (e.g. `CONFLICT (content): Merge conflict in conflict.txt`) and ends with the cause (`exit status 1`). `installRemoteAdvanceOnFetch` only wraps `git-uploadpack`, so it cannot inject a fake merge command — do not build a fake-`git`-on-PATH harness for this.

5. Add a `CHANGELOG.md` entry under `## Unreleased`. That section does not exist today (the newest heading is `## v0.32.0`) — create it above the newest `## vX.Y.Z` heading.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Errors are wrapped with `github.com/bborbe/errors` — never `fmt.Errorf`, never a bare `return err`
- Logging uses `log/slog` only — no `fmt.Print*`, no `glog` in this file
- `context.Background()` must not appear in production code under `pkg/` — take `ctx` from the caller. Test files may use it, as the existing `pkg/git` tests do.
- No `go mod vendor`, no `-mod=vendor` in any command
- Scope: change only `retryPush`. `resolveConflictMerge` (func at ~line 1115, format string at ~line 1143) formats a failed pull-merge with the same `merge %s: %s` pattern; leave it unchanged — that path needs its own follow-up prompt.
</constraints>

<verification>
Run `make precommit` — must pass.

Then confirm the misleading format is gone from the retry path (exit 0 means absent): `! awk '/^func \(g \*git\) retryPush\(/,/^}/' pkg/git/git.go | grep -q 'merge %s: %s'`. (`resolveConflictMerge`, func at ~line 1115 with the format string at ~line 1143, still uses the same format on the pull-merge path — out of scope for this prompt.)
</verification>
