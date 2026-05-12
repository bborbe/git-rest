---
status: committing
spec: [009-entry-state-recovery]
summary: Added ErrRepoUnrecoverable sentinel and recoverRepoState method to Pull() in pkg/git/git.go with four new Entry-state recovery Ginkgo tests covering abandoned-rebase, detached-HEAD, missing-origin/HEAD, and healthy-repo paths.
container: git-rest-041-spec-009-entry-state-recovery
dark-factory-version: v0.156.1-1-g04f3863-dirty
created: "2026-05-12T18:00:00Z"
queued: "2026-05-12T18:58:50Z"
started: "2026-05-12T18:58:52Z"
---

<summary>
- `Pull` no longer gets permanently stuck on "HEAD does not point to a branch" after a rebase conflict or operator detach
- Abandoned-rebase state (`.git/rebase-merge/` or `.git/rebase-apply/` present) is detected on every `Pull` entry and automatically cleared via `git rebase --abort` before the pull proceeds
- Bare-detached-HEAD state (no in-progress rebase) is detected and healed: the default branch is resolved from `refs/remotes/origin/HEAD`, checked out, and the upstream tracking ref is set
- A new `ErrRepoUnrecoverable` sentinel error is returned when recovery is impossible (e.g., `git rebase --abort` itself fails, or `refs/remotes/origin/HEAD` is missing) — callers can distinguish this from transient pull errors via `errors.Is`
- Every recovery action (abandoned-rebase abort, detached-HEAD restore) emits a single greppable `slog.InfoContext` line naming the detected state and the branch recovered to
- Healthy repos (HEAD on a branch, no rebase in progress) are unaffected — the entry-state check is a no-op and emits no log lines
- Unit tests cover all four paths: abandoned-rebase detected → pull succeeds, detached-HEAD recovery → pull succeeds, missing-origin/HEAD returns `ErrRepoUnrecoverable`, and `errors.Is(err, ErrRepoUnrecoverable)` is true
- The `pullRebaseAndPush` conflict path is unchanged — this spec only adds pre-pull recovery, not conflict-resolution policy
- `make precommit` passes; CHANGELOG updated
</summary>

<objective>
Add entry-state recovery to `Pull()` in `pkg/git/git.go`. Before resolving `@{u}`, `Pull` must check whether the repo is in an abandoned-rebase or bare-detached-HEAD state and self-heal. If recovery fails, `Pull` returns a typed `ErrRepoUnrecoverable` sentinel. This eliminates the current failure mode where any path that leaves HEAD detached permanently wedges the puller with "fatal: HEAD does not point to a branch" on every subsequent tick (prod incident 2026-05-12, `vault-obsidian-openclaw-0`, 2d2h downtime).
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read these coding guides before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-patterns.md` — interface→struct→constructor; private methods on structs
- `go-error-wrapping-guide.md` — `errors.Wrap`/`errors.Wrapf` from `github.com/bborbe/errors`; sentinel errors with `stderrors` alias; never `fmt.Errorf`; never bare `return err`
- `go-logging-guide.md` — `slog` structured logging, `slog.InfoContext`, key/value args
- `go-testing-guide.md` — Ginkgo/Gomega patterns, external test packages (`package git_test`)
- `test-pyramid-triggers.md` — which test types to write for each code change

Files to read in full before implementing (line numbers approximate — re-verify at current HEAD):

- `pkg/git/git.go` (full, ~573 lines): note the import block, existing sentinel errors (`ErrNotFound`, `ErrInvalidPath`), `Pull` (~line 448), `recoverRepoState` does NOT exist yet — you will add it.
  Key facts about the file:
  - `g.repoPath` is the working-tree root; rebase dirs are at `filepath.Join(g.repoPath, ".git", "rebase-merge")` and `filepath.Join(g.repoPath, ".git", "rebase-apply")`
  - `g.mu.Lock()` is held for the entire `Pull` body; `recoverRepoState` MUST be called inside that lock — no additional locking needed
  - `runCmdOutput(ctx, dir, args...)` → `([]byte, error)` — use for detecting HEAD state and resolving `origin/HEAD`
  - `runCmdRaw(ctx, dir, args...)` → `([]byte, error)` — use for `git rebase --abort` so output is available on failure
  - The import block already has: `os`, `path/filepath`, `strings`, `log/slog`, `github.com/bborbe/errors`, `stderrors "errors"` — do NOT add duplicate imports

- `pkg/git/git_test.go` (full, ~819 lines): note the imports (no `"fmt"`), existing helpers `runGit` (line ~24), `setupPullFixture` (line ~568), `writeLocalCommit` (line ~620), `gitOutputStr` (line ~629), and the `Describe("Pull state machine", ...)` block (line ~639). You will add a new `Describe("Entry-state recovery", ...)` block after the `Pull state machine` block.

- `CHANGELOG.md` — add `## Unreleased` after implementing
</context>

<requirements>

## 1. Add `ErrRepoUnrecoverable` sentinel in `pkg/git/git.go`

After the existing `ErrInvalidPath` declaration, add:

```go
// ErrRepoUnrecoverable is returned by Pull when the repository is in a state
// that cannot be automatically healed (e.g., git rebase --abort fails, or
// refs/remotes/origin/HEAD is missing for detached-HEAD recovery).
// Callers detect this via errors.Is(err, ErrRepoUnrecoverable).
var ErrRepoUnrecoverable = stderrors.New("repo state unrecoverable")
```

`stderrors` is already imported as `stderrors "errors"`. Do NOT add a duplicate import.

## 2. Add `recoverRepoState` method in `pkg/git/git.go`

Add this unexported method immediately before the `Pull` function. It is called inside `Pull` after `g.mu.Lock()` — no additional locking required.

```go
// recoverRepoState checks whether the repository is in an abandoned-rebase or
// bare-detached-HEAD state and heals it before the normal pull flow runs.
// Returns nil for a healthy repo (no-op, emits no log lines).
// Returns errors.Wrap(ErrRepoUnrecoverable, ...) for states that cannot be healed.
func (g *git) recoverRepoState(ctx context.Context) error {
	rebaseMergeDir := filepath.Join(g.repoPath, ".git", "rebase-merge")
	rebaseApplyDir := filepath.Join(g.repoPath, ".git", "rebase-apply")

	_, errMerge := os.Stat(rebaseMergeDir)
	_, errApply := os.Stat(rebaseApplyDir)

	if errMerge == nil || errApply == nil {
		// Abandoned rebase: abort it so Pull can run the normal fetch→state-machine flow.
		out, err := g.runCmdRaw(ctx, g.repoPath, "rebase", "--abort")
		if err != nil {
			return errors.Wrapf(ctx, ErrRepoUnrecoverable, "git rebase --abort failed: %s", strings.TrimSpace(string(out)))
		}
		headOut, _ := g.runCmdOutput(ctx, g.repoPath, "rev-parse", "--abbrev-ref", "HEAD")
		slog.InfoContext(ctx, "git-rest: recovered from abandoned rebase", "branch", strings.TrimSpace(string(headOut)))
		return nil
	}

	// Check if HEAD is detached (no rebase in progress).
	headOut, err := g.runCmdOutput(ctx, g.repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		// Cannot determine HEAD state — let Pull's @{u} resolution surface the error.
		return nil
	}
	if strings.TrimSpace(string(headOut)) != "HEAD" {
		// HEAD is on a branch — healthy state, no-op.
		return nil
	}

	// Bare detached HEAD: resolve the default branch from refs/remotes/origin/HEAD.
	symrefOut, err := g.runCmdOutput(ctx, g.repoPath, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err != nil {
		return errors.Wrap(ctx, ErrRepoUnrecoverable, "cannot determine default branch: refs/remotes/origin/HEAD not set")
	}
	// symrefOut = "refs/remotes/origin/main\n" — strip the known prefix.
	const remotePrefix = "refs/remotes/origin/"
	trimmed := strings.TrimSpace(string(symrefOut))
	if !strings.HasPrefix(trimmed, remotePrefix) {
		return errors.Wrapf(ctx, ErrRepoUnrecoverable, "unexpected symbolic-ref format: %s", trimmed)
	}
	branch := strings.TrimPrefix(trimmed, remotePrefix)

	if err := g.runCmd(ctx, g.repoPath, "checkout", branch); err != nil {
		return errors.Wrapf(ctx, ErrRepoUnrecoverable, "git checkout %s failed during detached-HEAD recovery", branch)
	}
	if err := g.runCmd(ctx, g.repoPath, "branch", "--set-upstream-to=origin/"+branch, branch); err != nil {
		return errors.Wrapf(ctx, ErrRepoUnrecoverable, "git branch --set-upstream-to=origin/%s failed", branch)
	}
	slog.InfoContext(ctx, "git-rest: recovered from detached HEAD", "branch", branch)
	return nil
}
```

## 3. Call `recoverRepoState` in `Pull()` in `pkg/git/git.go`

Add the `recoverRepoState` call AFTER the `hasRemote` check and BEFORE the `@{u}` resolution. This ordering is mandatory — calling it after `@{u}` would be too late (the `@{u}` call itself fails on a detached HEAD).

Find the current `Pull` body and locate the block that starts with `if !g.hasRemote(ctx)`. Insert the recovery call immediately after it:

```go
	if !g.hasRemote(ctx) {
		slog.DebugContext(ctx, "git pull skipped: no remote configured")
		return nil
	}

	// Entry-state recovery: abort any abandoned rebase or restore a detached HEAD
	// before @{u} resolution, which fails on any non-branch HEAD.
	if err := g.recoverRepoState(ctx); err != nil {
		g.metrics.IncGitOperationError("pull")
		return err
	}

	upstreamOut, err := g.runCmdOutput(
		ctx, g.repoPath, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}",
	)
```

The rest of `Pull` is **UNCHANGED**. Do NOT modify `pullRebaseAndPush` — conflict-handling policy is spec 007.

## 4. Add `Describe("Entry-state recovery", ...)` tests in `pkg/git/git_test.go`

Add the following block after the closing `})` of the `Describe("Pull state machine", ...)` block (currently ending around line 769 — verify the exact line before editing).

The test file already imports: `"context"`, `"errors"`, `"os"`, `"os/exec"`, `"path/filepath"`, `"strings"`, `libtime`, `git`, `mocks`. **Add these new imports** for log capture: `"bytes"`, `"log/slog"`. Do NOT add `"fmt"` — it is not imported and not needed.

The block uses existing package-level helpers `runGit`, `setupPullFixture`, `writeLocalCommit`, and `gitOutputStr` — all already defined in the file.

```go
// captureSlogLogs swaps slog.Default() to a buffer-backed text handler for the
// duration of a test. Returns the buffer and a restore func (use with defer).
// Required for AC4a + AC4b log-assertion coverage.
func captureSlogLogs() (*bytes.Buffer, func()) {
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return buf, func() { slog.SetDefault(prev) }
}

var _ = Describe("Entry-state recovery", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	Context("AC1: abandoned rebase (.git/rebase-merge/ present)", func() {
		It("aborts the abandoned rebase and completes pull successfully", func() {
			workDir, externalPush, cleanup := setupPullFixture()
			defer cleanup()

			// Non-conflicting divergence: remote pushes remote.txt, local commits local.txt.
			externalPush("remote.txt", "from remote\n")
			writeLocalCommit(workDir, "local.txt", "local only\n")
			runGit(workDir, "fetch")

			// Derive the upstream tracking ref without hardcoding "main".
			upstream := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"))

			// Force abandoned-rebase state via --exec false: git pauses the rebase after
			// the first commit (exec failure), leaving .git/rebase-merge/ populated.
			// Since the files don't conflict, after git rebase --abort the re-attempted
			// rebase in Pull will succeed cleanly.
			forceCmd := exec.Command("git", "rebase", "--exec", "false", upstream)
			forceCmd.Dir = workDir
			_ = forceCmd.Run() // error expected — we only want the side-effect

			_, statErr := os.Stat(filepath.Join(workDir, ".git", "rebase-merge"))
			Expect(statErr).NotTo(HaveOccurred(), ".git/rebase-merge should exist after --exec false")

			logs, restore := captureSlogLogs()
			defer restore()

			pg := git.New(workDir, &noopMetrics{}, libtime.NewCurrentDateTime(), "")
			err := pg.Pull(ctx)

			Expect(err).NotTo(HaveOccurred(), "Pull should succeed after aborting abandoned rebase")
			head := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "--abbrev-ref", "HEAD"))
			Expect(head).NotTo(Equal("HEAD"), "HEAD must be on a branch after recovery")
			u := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"))
			Expect(u).To(ContainSubstring("origin/"), "@{u} must resolve after recovery")

			// AC4b: exactly one structured-log line for the recovery.
			logStr := logs.String()
			Expect(strings.Count(logStr, "recovered from abandoned rebase")).To(Equal(1),
				"expected exactly one 'recovered from abandoned rebase' log line, got: %s", logStr)
			Expect(logStr).To(ContainSubstring("branch="), "log line must include branch key")
		})
	})

	Context("AC2: bare detached HEAD (no rebase in progress, origin/HEAD set)", func() {
		It("checks out the default branch, sets upstream, and completes pull", func() {
			// Use git clone so refs/remotes/origin/HEAD is set automatically.
			remoteDir, err := os.MkdirTemp("", "git-remote-*")
			Expect(err).NotTo(HaveOccurred())

			// Seed the bare remote via a temp clone.
			seedDir, seedErr := os.MkdirTemp("", "git-seed-*")
			Expect(seedErr).NotTo(HaveOccurred())
			defer func() { _ = os.RemoveAll(seedDir) }()

			rg := func(dir string, args ...string) {
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				out, e := cmd.CombinedOutput()
				if e != nil {
					panic(string(out))
				}
			}
			rg(remoteDir, "init", "--bare")
			rg(seedDir, "init")
			rg(seedDir, "config", "user.email", "test@example.com")
			rg(seedDir, "config", "user.name", "Test")
			rg(seedDir, "remote", "add", "origin", remoteDir)
			rg(seedDir, "commit", "--allow-empty", "-m", "init")
			rg(seedDir, "push", "-u", "origin", "HEAD")

			// Clone so origin/HEAD is set automatically (git init+push does NOT set it).
			workDir, wdErr := os.MkdirTemp("", "git-work-*")
			Expect(wdErr).NotTo(HaveOccurred())
			defer func() {
				_ = os.RemoveAll(workDir)
				_ = os.RemoveAll(remoteDir)
			}()
			rg(workDir, "clone", remoteDir, ".")
			rg(workDir, "config", "user.email", "test@example.com")
			rg(workDir, "config", "user.name", "Test")

			// Detach HEAD (simulates operator running `git checkout HEAD --detach`).
			runGit(workDir, "checkout", "--detach", "HEAD")
			Expect(strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "--abbrev-ref", "HEAD"))).
				To(Equal("HEAD"), "fixture sanity: HEAD should be detached before Pull")

			logs, restore := captureSlogLogs()
			defer restore()

			pg := git.New(workDir, &noopMetrics{}, libtime.NewCurrentDateTime(), "")
			pullErr := pg.Pull(ctx)

			Expect(pullErr).NotTo(HaveOccurred())
			head := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "--abbrev-ref", "HEAD"))
			Expect(head).NotTo(Equal("HEAD"), "HEAD should be on a branch after recovery")
			u := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"))
			Expect(u).To(ContainSubstring("origin/"), "@{u} must resolve after detached-HEAD recovery")

			// AC4b: exactly one structured-log line for the recovery.
			logStr := logs.String()
			Expect(strings.Count(logStr, "recovered from detached HEAD")).To(Equal(1),
				"expected exactly one 'recovered from detached HEAD' log line, got: %s", logStr)
			Expect(logStr).To(ContainSubstring("branch="), "log line must include branch key")
		})
	})

	Context("AC3 + AC4d: detached HEAD, refs/remotes/origin/HEAD absent", func() {
		It("returns ErrRepoUnrecoverable and errors.Is matches", func() {
			// setupPullFixture uses git-init+push, NOT git-clone, so origin/HEAD is not set.
			workDir, _, cleanup := setupPullFixture()
			defer cleanup()

			// Detach HEAD; explicitly delete origin/HEAD to guarantee it is absent.
			runGit(workDir, "checkout", "--detach", "HEAD")
			// git remote set-head --delete is idempotent (ok if ref didn't exist).
			delCmd := exec.Command("git", "remote", "set-head", "origin", "--delete")
			delCmd.Dir = workDir
			_ = delCmd.Run()

			pg := git.New(workDir, &noopMetrics{}, libtime.NewCurrentDateTime(), "")
			pullErr := pg.Pull(ctx)

			Expect(pullErr).To(HaveOccurred())
			Expect(errors.Is(pullErr, git.ErrRepoUnrecoverable)).To(BeTrue(),
				"expected ErrRepoUnrecoverable, got: %v", pullErr)
		})
	})

	Context("AC4a: healthy repo — state check is a no-op", func() {
		It("Pull succeeds twice; HEAD stays on a branch, no side effects, no recovery logs", func() {
			workDir, _, cleanup := setupPullFixture()
			defer cleanup()

			pg := git.New(workDir, &noopMetrics{}, libtime.NewCurrentDateTime(), "")
			headBefore := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "HEAD"))

			logs, restore := captureSlogLogs()
			defer restore()

			Expect(pg.Pull(ctx)).NotTo(HaveOccurred())
			Expect(pg.Pull(ctx)).NotTo(HaveOccurred())

			headAfter := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "HEAD"))
			Expect(headAfter).To(Equal(headBefore), "HEAD SHA must not change on no-op pulls")
			head := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "--abbrev-ref", "HEAD"))
			Expect(head).NotTo(Equal("HEAD"), "HEAD must remain on a branch")

			// AC4a: healthy repo emits ZERO recovery log lines.
			logStr := logs.String()
			Expect(logStr).NotTo(ContainSubstring("recovered from"),
				"healthy repo must not emit recovery log lines, got: %s", logStr)
		})
	})
})
```

## 5. Add CHANGELOG entry

In `CHANGELOG.md`, add `## Unreleased` immediately before the first versioned heading. If `## Unreleased` already exists, append to it:

```markdown
## Unreleased

- fix: `Pull` now auto-recovers from abandoned-rebase (`.git/rebase-merge/` or `.git/rebase-apply/` present) and bare-detached-HEAD states on entry. Previously any path leaving HEAD detached permanently wedged the puller with "fatal: HEAD does not point to a branch" until manual `kubectl exec` recovery. New `ErrRepoUnrecoverable` sentinel returned for unrecoverable states (missing `refs/remotes/origin/HEAD`, failed `git rebase --abort`); callers use `errors.Is`. Recovery actions log at INFO with `"branch"` field. Fixes `vault-obsidian-openclaw-0` stuck 0/1 Running for 2d2h (prod incident 2026-05-12).
```

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- `context.Background()` MUST NOT appear in `pkg/` — only in `main.go` and test files; the `BeforeEach` scope in tests is fine
- Errors MUST be wrapped with `errors.Wrap`/`errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf`, never bare `return err`
- `ErrRepoUnrecoverable` MUST be the second argument to `errors.Wrap`/`errors.Wrapf` (as the wrapped sentinel) so `errors.Is` traverses the chain and finds it
- `recoverRepoState` MUST be called after `g.mu.Lock()` and after `hasRemote`, and BEFORE `rev-parse @{u}` — this ordering is non-negotiable
- `recoverRepoState` MUST check BOTH `.git/rebase-merge` AND `.git/rebase-apply` with `os.Stat` — git uses either depending on rebase type
- The abandoned-rebase recovery MUST call `git rebase --abort` via `runCmdRaw` (not `runCmd`) so failure output is available for wrapping
- The detached-HEAD recovery MUST call `git checkout <branch>` then `git branch --set-upstream-to=origin/<branch> <branch>` — both steps are required for `@{u}` to resolve
- The structured-log lines MUST use `slog.InfoContext(ctx, ..., "branch", branch)` — not `slog.With`, not `slog.Info`
- A HEALTHY repo MUST emit ZERO `slog.InfoContext` recovery lines — the early-return for `head != "HEAD"` ensures this
- Do NOT modify `pullRebaseAndPush` — conflict handling during the pull itself is spec 007
- Do NOT change the public HTTP contract (`/api/v1/files/*`, `/readiness`, `/healthz`, `/metrics`)
- Do NOT introduce new env vars or flags — recovery is always on
- Existing tests must still pass
- Test file imports: do NOT add `"fmt"` — the test block above does not require it; use `panic(string(out))` in `rg` helpers, not `Fail(fmt.Sprintf(...))`
</constraints>

<verification>
Run tests (fast, iterative — repeat after each meaningful change):
```bash
cd /workspace && make test
```

Spot-check only the new entry-state recovery tests:
```bash
cd /workspace && go test ./pkg/git/... -v -run "Entry-state recovery"
```
Expected: all four `It` blocks pass.

Verify `ErrRepoUnrecoverable` is declared and used:
```bash
grep -n "ErrRepoUnrecoverable" /workspace/pkg/git/git.go
```
Expected: one `var` declaration plus usages in `recoverRepoState`.

Verify `recoverRepoState` is called BEFORE `@{u}` in `Pull` (line number must be lower):
```bash
grep -n "recoverRepoState\|@{u}" /workspace/pkg/git/git.go
```
Expected: `recoverRepoState` line < `@{u}` line.

Verify `pullRebaseAndPush` is UNCHANGED:
```bash
grep -n -A 15 "func (g \*git) pullRebaseAndPush" /workspace/pkg/git/git.go
```
Expected: function body matches the pre-change version exactly.

Verify recovery log lines are greppable by operators:
```bash
grep -n "git-rest: recovered" /workspace/pkg/git/git.go
```
Expected: exactly two matches (abandoned-rebase and detached-HEAD paths).

Final validation:
```bash
cd /workspace && make precommit
```
Must pass with exit code 0.
</verification>
