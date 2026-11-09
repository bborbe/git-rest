---
status: completed
summary: Made git-rest file writes roll back the working tree and index when staging or committing fails, so a failed write leaves no uncommitted file and a retried create succeeds.
execution_id: git-rest-ghost-file-exec-103-rollback-uncommitted-write-on-write-failure
dark-factory-version: v0.198.17
created: "2026-11-08T20:47:32Z"
queued: "2026-11-08T20:47:32Z"
started: "2026-11-09T01:07:55Z"
completed: "2026-11-09T01:12:17Z"
---

<summary>
- A write that fails while staging its own change leaves no trace in the working tree
- Callers stop seeing a file that was never committed as if it were already present
- A failed create can be retried instead of being answered "already exists"
- A retry of the same path and content after a failed write succeeds
- A write that fails on a path it created leaves no empty parent directory behind
- Writes that fail after the commit exists are unchanged: their commit is pushed by the next sync
- Write-if-absent keeps its existing guarantee that an occupied path is never overwritten
</summary>

<objective>
Make `git-rest`'s file write atomic with respect to the repository state: when staging or committing fails, the working tree and the index must be left exactly as they were before the call. Today the content reaches the disk before staging, and a staging failure returns without removing it, so a caller that sees an error still finds the file on disk. A caller that checks the path before retrying — vault-api's create, which stats it — then reports the resource as already existing and never writes it again, losing the write permanently while logging only a benign "already exists".
</objective>

<context>
There is no repo-root `CLAUDE.md` in this worktree — `.gitignore` lists `/CLAUDE.md`, and a gitignored file does not propagate to a linked worktree. Do not look for one. Project conventions are restated in `<constraints>` below; the definition of done is `docs/dod.md`.

Read `docs/api.md` § Write File — the documented write contract this change makes true.

Read `pkg/git/git.go` — `WriteFile`, `WriteFileIfAbsent` and `writeFileLocked`. The sequence in `writeFileLocked` is: `os.WriteFile(fullPath, …)`, then `g.runCmd(ctx, g.repoPath, "add", path)`, then either the coalescing enqueue or `git commit` followed by `git push`. Every one of those steps returns an error today without undoing the write.

Read `pkg/git/git_test.go` — the `Context("WriteFile", …)` block for the existing style of these tests. For failure injection, follow the pattern already used for locks in this file (`setupPullFixture`'s `AC5: leaves a lock held by a live process in place`): hold `.git/index.lock` open with `os.OpenFile` from the test process, which makes the next `git add` fail deterministically. Do not use `flock`, and do not use a git command as the holder — git exits immediately when the lock already exists.
</context>

<requirements>
1. In `pkg/git/git.go`, give `writeFileLocked` a way to restore the working tree when it returns an error after it has already written the file. Capture the previous state before the write, so the rollback does not depend on re-reading anything after a failure.

2. The rollback distinguishes its two cases from the state captured before the write (requirement 1), not from the `fileExists` boolean alone — `WriteFile` derives that boolean from `os.Stat` (`statErr == nil`), so a stat error other than "does not exist" reports `false` for a path that does exist:
   - the path was definitively absent (`os.IsNotExist`): remove the file;
   - the path existed and its content was captured: restore that content;
   - the pre-write state could not be determined: do nothing at all — no removal and no write. Writing an empty or nil captured buffer over a real file would be the data loss this change exists to prevent.

3. Apply the rollback on every error return that occurs after the write: the `git add` failure, the `git commit` failure, and any other post-write failure branch. The rollback must leave the whole tree clean, not only the path: after a `git commit` failure the path is already staged, so unstage that path too (`git restore --staged -- <path>`, or `git reset -q -- <path>`) before restoring the working tree. Otherwise `RecoverPendingWrites` (`pkg/git/git.go`, called from `main.go` before the server starts) commits and pushes the staged ghost on the next boot, and the next successful write's plain `git commit` — which commits the whole index — carries it as well. A failure before the write — path validation, the missing-frontmatter refusal, `MkdirAll`, the `os.WriteFile` failure itself — must not attempt a rollback. The "content unchanged" short-circuit is not a failure and must keep returning `nil` without writing.

4. Do **not** roll back a failed `git push`. The commit exists locally at that point and the next successful push carries it; removing the file would discard already-committed work.

5. When the rollback removes a file the call created, also remove any parent directories the call created for it, when they are empty. Leave a directory holding any other entry alone. Put this in a small named helper — `writeFileLocked` is already near the funlen budget (roughly 64 of 80).

6. The rollback is best-effort and must not mask the original error: if the restore itself fails, log it with `slog.ErrorContext` naming the path and keep returning the original error. Never return the rollback's error in place of the cause. The restore-failure branch is not reachable from a test — the write, the `git add` failure and the rollback all run inside one `WriteFile` call, so a test cannot make the restore fail without an injection seam, and a test-only package-level setter is forbidden by the coding guidelines. Record that in a comment above the rollback helper: the branch keeps returning the original error and logs the rollback failure at error level, and it is deliberately uncovered.

7. `WriteFileIfAbsent` shares `writeFileLocked` and inherits the fix. Its occupied-path refusal must be unchanged: still `ErrFileExists`, file byte-identical, repository untouched.

8. Tests in `pkg/git/git_test.go`, Ginkgo v2 / Gomega:
   - `WriteFile` of a new path whose `git add` fails (index.lock held) leaves no file at that path, and a second `WriteFile` of the same path and content after the lock is released succeeds.
   - `WriteFile` of an existing path whose `git add` fails leaves the file with its original content.
   - `WriteFile` of a nested new path whose `git add` fails leaves no file and no empty parent directory.
   - `WriteFile` whose `git add` succeeds but whose `git commit` fails leaves the working tree **and the index** exactly as they were: `git status --porcelain` is empty afterwards; then release the held lock, `WriteFile` a different path, and assert the resulting commit carries only that path (e.g. `git show --name-only --format= HEAD` lists just the second path). Inject the commit failure the way the lock tests above do: hold the branch ref lock (`.git/refs/heads/<branch>.lock`) open from the test process, which lets `git add` succeed and makes `git commit` fail.
   - The existing `ErrFileExists` / byte-identical test for `WriteFileIfAbsent` stays green (do not duplicate it — it already exists in the file).
   - In each failure case above, assert `git status --porcelain` is empty, not only that the path is absent.

9. Add a `CHANGELOG.md` entry under `## Unreleased` in the existing `- fix: …` style, describing the user-visible effect: a write that fails while staging no longer leaves an uncommitted file behind, so a retried create is no longer answered as already existing.

10. `DeleteFile` has the same `git rm` + plain `git commit` shape with no rollback. It is deliberately **out of scope** — do not widen this change to it.

11. Before finishing, re-run `<verification>` and confirm every line passes, then walk each `<summary>` bullet against the change.
</requirements>

<constraints>
- The public `Git` interface must not change. No new exported method, no new sentinel error, no new metric label.
- The success path must not change: same commit message, same staged content, same push behaviour.
- The coalescing path must keep working — when coalescing is enabled the function returns after enqueueing on success, so no rollback applies there.
- Errors are wrapped with `github.com/bborbe/errors`, never `fmt.Errorf`.
- Non-test `pkg/git` code must not call `context.Background()`; contexts are passed in.
- `pkg/git/git.go` logs with `log/slog`. Do not introduce `github.com/golang/glog` there.
- Timestamps come from the injected `currentDateTimeGetter`, never `time.Now()`.
- Precommit budgets: funlen 80/50, gocognit 20, nestif 4, maintidx 20, dupl, golines 100. If `writeFileLocked` grows past them, extract a named helper instead of nesting.
- `.dark-factory.yaml` sets `workflow: direct`, `pr: false`, `autoRelease: false`: commit to the branch directly; no PR or auto-release is expected.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run inside the container, from the repository root:

```
GOFLAGS=-buildvcs=false go build ./...
GOFLAGS=-buildvcs=false go test ./pkg/git/ -count=1
make test
make precommit
```

All must exit 0. `make precommit` is the authoritative gate; if it fails, fix the failure and re-run only the failing target until it passes, then run `make precommit` once more.

Then confirm the observable outcomes — every line must print `ok`. The first two and the three `! grep` lines are regression guards (already true before the change); the CHANGELOG count is the one assertion this change must move:

```
test "$(grep -cF 'func (g *git) writeFileLocked(' pkg/git/git.go)" = "1" && echo ok
test "$(grep -cF 'os.WriteFile(fullPath, content' pkg/git/git.go)" = "1" && echo ok
! grep -qF 'fmt.Errorf' pkg/git/git.go && echo ok
! grep -qF 'context.Background()' pkg/git/git.go && echo ok
! grep -qF 'github.com/golang/glog' pkg/git/git.go && echo ok
# one `- fix:` bullet exists under ## Unreleased before the change; the new entry must make it two
test "$(awk '/^## Unreleased/{u=1;next} /^## /{u=0} u && /^- fix: /{n++} END{print n+0}' CHANGELOG.md)" -ge 2 && echo ok
```
</verification>
