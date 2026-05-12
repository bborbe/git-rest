---
status: completed
spec: [009-configurable-conflict-resolution]
summary: 'WriteFile and DeleteFile made idempotent: same-content writes return 200 (no-op) and absent-file deletes return 200 instead of 404/500.'
container: git-rest-039-spec-007-idempotent-write-delete
dark-factory-version: v0.148.4-3-gc45254a
created: "2026-05-06T15:30:00Z"
queued: "2026-05-06T15:28:00Z"
started: "2026-05-06T15:28:36Z"
completed: "2026-05-06T15:32:19Z"
branch: dark-factory/bug-write-file-no-content-change-returns-500
---

<summary>
- `POST /api/v1/files/<path>` with the same body now returns 200 instead of 500 when the file is byte-identical to HEAD
- `DELETE /api/v1/files/<path>` now returns 200 when the file is already absent, instead of 404
- "Nothing to commit" is logged at INFO and treated as a silent no-op — operators can distinguish idempotent writes from real writes
- Real `git commit` failures (pre-commit hook rejection, out-of-disk) continue to return 500 with the original error message
- At-least-once delivery clients (CQRS task controllers, Kafka consumers) no longer receive spurious 500s on idempotent retry
- Unit tests cover same-content double-write, different-content write after same-content write, and double-delete
- `make precommit` passes; CHANGELOG updated
</summary>

<objective>
Fix `WriteFile` in `pkg/git/git.go` so that `git commit` returning "nothing to commit" is treated as a no-op success (200) rather than a real error (500). Fix `DeleteFile` so that deleting an already-absent file is also a no-op success (200) rather than 404. Both fixes make the HTTP API idempotent for at-least-once delivery clients.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read these coding guides before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-error-wrapping-guide.md` — `errors.Wrapf` from `github.com/bborbe/errors`; never `fmt.Errorf`; never bare `return err`
- `go-patterns.md` — interface→struct→constructor; error wrapping conventions
- `go-testing-guide.md` — Ginkgo/Gomega patterns, external test packages (`package git_test`)

Files to read in full before implementing:
- `pkg/git/git.go` — `WriteFile` (~line 208), `DeleteFile` (~line 262), `runCmd`, `runCmdOutput`, `runCmdRaw` (already present, added in an earlier prompt); you will modify `WriteFile` and `DeleteFile` only
- `pkg/git/git_test.go` — existing `WriteFile` tests, `DeleteFile` tests; you will add new `It` blocks and update one existing `DeleteFile` test
- `CHANGELOG.md` — prepend `## Unreleased` section
</context>

<requirements>

## 1. Fix `WriteFile` in `pkg/git/git.go` — treat "nothing to commit" as no-op success

`runCmdRaw` already exists in this file. Use it for the `git commit` call so that combined stdout+stderr is available even on failure.

Replace the existing `git commit` block in `WriteFile` (currently ~line 247):

```go
// Before:
if err := g.runCmd(ctx, g.repoPath, "commit", "-m", commitMsg); err != nil {
    g.metrics.IncGitOperationError("write_file")
    return errors.Wrap(ctx, err, "git commit")
}
```

With:

```go
// After:
commitOut, err := g.runCmdRaw(ctx, g.repoPath, "commit", "-m", commitMsg)
if err != nil {
    if strings.Contains(string(commitOut), "nothing to commit") {
        slog.InfoContext(ctx, "write file: no changes to commit (content unchanged)", "path", path)
        return nil
    }
    g.metrics.IncGitOperationError("write_file")
    return errors.Wrapf(ctx, err, "git commit: %s", strings.TrimSpace(string(commitOut)))
}
```

The early `return nil` inside the `if err != nil` block exits `WriteFile` before the subsequent `git push` step — correct, because there is nothing to push.

## 2. Fix `DeleteFile` in `pkg/git/git.go` — treat already-absent file as no-op success

`DeleteFile` currently returns `ErrNotFound` when `os.Stat` shows the file does not exist on disk. This causes the HTTP handler to return 404 — but idempotent retry semantics require 200: the desired state (file absent) is already achieved.

Replace the existing `os.Stat` check in `DeleteFile` (~line 278):

```go
// Before:
if _, err := os.Stat(fullPath); os.IsNotExist(err) {
    return ErrNotFound
}
```

With:

```go
// After:
if _, err := os.Stat(fullPath); os.IsNotExist(err) {
    slog.InfoContext(ctx, "delete file: no changes to commit (file already absent)", "path", path)
    return nil
}
```

This makes `DeleteFile` idempotent: if the file is already absent (whether deleted by a prior call or never created), the desired state is satisfied and the call succeeds silently.

## 3. Update the existing `ErrNotFound` test for `DeleteFile` in `pkg/git/git_test.go`

The existing test:

```go
It("returns ErrNotFound for non-existent file", func() {
    err := g.DeleteFile(ctx, "doesnotexist.txt")
    Expect(err).To(MatchError(git.ErrNotFound))
})
```

This test documents the OLD behavior. Since we are intentionally making `DeleteFile` idempotent, this test must be updated to match the new behavior:

```go
It("returns nil for a non-existent file (idempotent delete)", func() {
    err := g.DeleteFile(ctx, "doesnotexist.txt")
    Expect(err).To(BeNil())
})
```

## 4. Add `WriteFile` idempotency tests to `pkg/git/git_test.go`

Add these `It` blocks inside the existing `Context("round-trip with ReadFile", ...)` block in `Context("WriteFile", ...)`:

```go
It("returns nil on second write with identical body (no-op)", func() {
    Expect(g.WriteFile(ctx, "idempotent.txt", []byte("hello"))).To(Succeed())
    Expect(g.WriteFile(ctx, "idempotent.txt", []byte("hello"))).To(Succeed())
})

It("does not create a second commit when content is unchanged", func() {
    Expect(g.WriteFile(ctx, "same.txt", []byte("content"))).To(Succeed())
    Expect(g.WriteFile(ctx, "same.txt", []byte("content"))).To(Succeed())

    // Only one commit beyond the initial repo commit
    out, err := exec.Command("git", "-C", workDir, "log", "--oneline", "--", "same.txt").Output()
    Expect(err).NotTo(HaveOccurred())
    lines := strings.Split(strings.TrimSpace(string(out)), "\n")
    // Filter empty lines
    var nonEmpty []string
    for _, l := range lines {
        if l != "" {
            nonEmpty = append(nonEmpty, l)
        }
    }
    Expect(nonEmpty).To(HaveLen(1), "expected exactly one commit for same.txt, got: %s", string(out))
})

It("creates a new commit when content changes after a same-content write", func() {
    Expect(g.WriteFile(ctx, "evolving.txt", []byte("v1"))).To(Succeed())
    Expect(g.WriteFile(ctx, "evolving.txt", []byte("v1"))).To(Succeed()) // no-op
    Expect(g.WriteFile(ctx, "evolving.txt", []byte("v2"))).To(Succeed()) // real update

    out, err := exec.Command("git", "-C", workDir, "log", "--oneline", "--", "evolving.txt").Output()
    Expect(err).NotTo(HaveOccurred())
    lines := strings.Split(strings.TrimSpace(string(out)), "\n")
    var nonEmpty []string
    for _, l := range lines {
        if l != "" {
            nonEmpty = append(nonEmpty, l)
        }
    }
    Expect(nonEmpty).To(HaveLen(2), "expected create + update commits, got: %s", string(out))
})
```

## 5. Add `DeleteFile` idempotency test to `pkg/git/git_test.go`

Add this `It` block inside `Context("DeleteFile", ...)`, after the existing "deletes an existing file" test:

```go
It("returns nil on second delete of the same file (idempotent delete)", func() {
    Expect(g.WriteFile(ctx, "gone.txt", []byte("bye"))).To(Succeed())
    Expect(g.DeleteFile(ctx, "gone.txt")).To(Succeed())
    Expect(g.DeleteFile(ctx, "gone.txt")).To(Succeed())
})
```

## 6. Add CHANGELOG entry

In `CHANGELOG.md`, add `## Unreleased` immediately after the preamble block (before `## v0.19.3`):

```markdown
## Unreleased

- fix: `WriteFile` and `DeleteFile` are now idempotent. Re-writing a file with identical content returns 200 and logs "no changes to commit" at INFO instead of returning 500 "nothing to commit, working tree clean". Re-deleting an already-absent file returns 200 instead of 404. Fixes CQRS retry loops in `bborbe/agent` task-controller that read the 500/404 as failure and retried up to 5 times (prod incident 2026-05-06, `vault-obsidian-openclaw-0`).
```

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- `context.Background()` must NOT appear in `pkg/` — only in `main.go` and test files
- Errors must be wrapped with `errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf`, never bare `return err`
- The "nothing to commit" check MUST use `strings.Contains(string(commitOut), "nothing to commit")` — this substring is stable across git versions and appears in both "working tree clean" and "nothing added to commit" variants
- Real `git commit` failures (non "nothing to commit" in output) MUST still return 500 — do not swallow all commit errors
- The INFO log for `WriteFile` MUST include the `path` field: `slog.InfoContext(ctx, "...", "path", path)`
- The INFO log for `DeleteFile` MUST include the `path` field: `slog.InfoContext(ctx, "...", "path", path)`
- `runCmdRaw` already exists in `pkg/git/git.go` — do NOT add a duplicate definition
- After the `WriteFile` no-op case returns `nil`, the `git push` step MUST be skipped (early return achieves this — verify the push block is after the commit block, not inside it)
- The `strings` package is already imported in `pkg/git/git.go` — no new imports needed for `strings.Contains`
- Test file is `package git_test` (external) — imports `"os/exec"` and `"strings"` (both already present)
- Existing `WriteFile` tests (round-trip, create message, update message, nested directories) MUST still pass
- The updated `DeleteFile` test must use `BeNil()` not `MatchError(git.ErrNotFound)` — the behavior change is intentional
</constraints>

<verification>
Run tests (fast, iterative):
```bash
cd /workspace && make test
```
Must pass — all existing and new tests green.

Spot-check idempotency tests:
```bash
cd /workspace && go test ./pkg/git/... -v -run "idempotent"
```
Expected: 4 It blocks pass (no-op write, no second commit, update after same-content, double delete).

Verify the no-op commit detection substring:
```bash
grep -n "nothing to commit" /workspace/pkg/git/git.go
```
Expected: exactly one match inside `WriteFile`.

Verify `runCmdRaw` is used in `WriteFile` (not the old `runCmd`):
```bash
grep -n "runCmdRaw\|runCmd" /workspace/pkg/git/git.go | grep -A1 -B1 "commit"
```
Expected: `runCmdRaw` appears near the commit message lines; `runCmd` does not appear for the commit call.

Verify `DeleteFile` no longer returns `ErrNotFound` for missing files:
```bash
grep -n "ErrNotFound" /workspace/pkg/git/git.go
```
Expected: no match inside `DeleteFile` function body (`ErrNotFound` may still appear in declarations at the top of the file).

Final validation:
```bash
cd /workspace && make precommit
```
Must pass with exit code 0.
</verification>
