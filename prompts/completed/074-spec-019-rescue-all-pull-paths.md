---
status: completed
spec: [019-bug-dirty-tree-rescue-all-pull-paths]
summary: Extended the dirty-working-tree rescue to the diverged, push-retry and startup sync paths with a path-dependent reset target, routed startup through Pull, and added rescue/regression tests plus a CHANGELOG entry.
execution_id: git-rest-dirty-tree-rescue-exec-074-spec-019-rescue-all-pull-paths
dark-factory-version: v0.198.7
created: "2026-10-22T07:10:42Z"
queued: "2026-10-22T07:32:05Z"
started: "2026-10-22T07:32:07Z"
completed: "2026-10-22T07:37:57Z"
branch: dark-factory/bug-dirty-tree-rescue-all-pull-paths
---

# Rescue a dirty working tree on the diverged, push-retry and startup paths

<summary>
- A dirty working tree is rescued on the diverged merge path, not only on the fast-forward path
- A dirty working tree is rescued on the push-retry merge path, which previously had the same missing rescue
- A dirty working tree present at boot is rescued by the startup sync, because the startup sync now uses the same four-state pull as every later cycle
- The reset target is path-dependent: the upstream tracking ref on fast-forward, HEAD on the other three paths, so the local commits those paths are about to merge are never discarded
- A successful rescue re-runs the failed merge inline, without recursing into `Pull` (which holds the repo mutex for its whole body)
- A failed rescue aborts any half-finished merge before returning, so `MERGE_HEAD` is never left behind
- Every rescue still increments the existing rescue counter exactly once and logs the existing `rescue branch pushed` line, unchanged
- The rescue capture contract from spec 014 is reused verbatim — no second rescue implementation
- Clean fast-forward, clean diverged merge, conflict resolution and the no-op path behave exactly as before
- The startup sync stays best-effort: a failure is warn-logged and never aborts boot
</summary>

<objective>
Extend the spec 014 dirty-working-tree rescue to every sync path that can fail on a dirty tree: `pullMergeAndPush` (the diverged state), `retryPush`'s merge, and `syncOnStartup`. The reset target differs by path — upstream on fast-forward, `HEAD` on the diverged, push-retry and startup paths — so the local commits those paths carry survive the rescue and the subsequent merge. The rescue itself is not reimplemented: spec 014's capture contract is called from the new sites with a path-dependent reset target.
</objective>

<context>
Read `README.md` and `docs/dod.md` at the repo root for project conventions and the definition of done (this repo has no `CLAUDE.md`).

Read these coding-plugin guides before implementing (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `errors.Wrap` / `errors.Wrapf` from `github.com/bborbe/errors`; never `fmt.Errorf`, never a bare `return err`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md` — `log/slog` with `slog.InfoContext` / `slog.WarnContext` / `slog.ErrorContext`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, external test packages, real `git` via `os/exec` against a temp working tree
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md` — the rescue branch timestamp comes from the injected `libtime` getter, never `time.Now()`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` — coverage and verification rules

Files to read in full before editing (anchor by function name, not line number):

- `pkg/git/git.go` — `Pull`, `syncWithUpstream`, `fastForwardOrRescue`, `workingTreeDirty`, `createRescueCommit`, `rescueDirtyTree`, `pullMergeAndPush`, `resolveConflictMerge`, `retryPush`, `pushWithRetry`, `runCmdRaw`, `runCmd`, `hasRemote`
- `main.go` — `Run`, `bootstrap`, `syncOnStartup`, `syncOnStartupTimeout`, `recoverUntracked`, `runGitCmd`
- `main_test.go` — the `SyncOnStartup` Describe and its `initRepoWithLocalRemote` / `remoteLogCount` helpers
- `pkg/git/git_test.go` — `setupRescueFixture`, `gatherPullRescues`, `captureSlogLogs`, `gitOutputStr`, `runGit`, and the existing `Dirty working tree rescue` Describe
- `specs/in-progress/019-bug-dirty-tree-rescue-all-pull-paths.md` — `## Desired Behavior`, `## Constraints`, `## Failure Modes`
- `specs/in-progress/014-bug-pull-cannot-recover-from-dirty-working-tree.md` — the capture contract this prompt must not change

**Verified facts about the current code (read these, do not re-derive):**

- `Pull` takes `g.mu.Lock()` with `defer g.mu.Unlock()`, registers `defer g.refreshQuarantinedBacklog(ctx)`, early-returns for `!g.hasRemote(ctx)`, runs `recoverRepoState`, resolves `upstream` from `@{u}`, and ends with `return g.syncWithUpstream(ctx, upstream)`.
- `syncWithUpstream` switches on `localSHA == remoteSHA` / `localSHA == baseSHA` / `remoteSHA == baseSHA` / default. The `localSHA == baseSHA` case calls `fastForwardOrRescue` and re-evaluates by calling `g.syncWithUpstream(ctx, upstream)` once; the default case calls `g.pullMergeAndPush(ctx, upstream)`.
- `fastForwardOrRescue(ctx, upstream) (bool, error)` inspects `workingTreeDirty` before `git merge --ff-only`, and on a dirty-tree merge failure calls `g.rescueDirtyTree(ctx, upstream)`.
- `rescueDirtyTree(ctx context.Context, upstream string) error` builds the `rescue/<20060102T150405Z>` branch, pushes it, logs `rescue branch pushed` with `branch`, `files`, `paths`, calls `g.metrics.IncPullRescue()`, then runs `git reset --hard <upstream>` and `git clean -fd`. The reset and clean are reachable ONLY after the push succeeded.
- `pullMergeAndPush(ctx, upstream) error` runs `g.runCmdRaw(ctx, g.repoPath, "merge", "--no-edit", upstream)`. On success it records `IncMergeOutcome("clean")`, logs `git-rest: merged diverged history`, and calls `g.pushWithRetry`. On failure it delegates to `g.resolveConflictMerge(ctx, upstream, out, mergeErr)`. It does not inspect the working tree today.
- `retryPush(ctx context.Context, upstream string, nextAttempt int) error` sleeps jitter, increments `IncPushRetry`, resolves `@{u}` when `upstream == ""`, runs `git fetch`, then `g.runCmdRaw(..., "merge", "--no-edit", upstream)`. On merge failure it runs `git merge --abort`, warn-logs `git-rest: retry merge failed` with `upstream`, `err`, `output`, and returns `errors.Wrapf(ctx, mergeErr, "merge %s failed", upstream)`.
- `hasRemote` returns `true` when `git remote` errors (so `Pull` on a directory without `.git` proceeds and fails at `@{u}` resolution).
- `main.go` `Run` calls `a.bootstrap(ctx)` first and only then `a.createGitClient(ctx)`; `bootstrap` calls `cleanupStaleLocks`, `initIfNeeded`, `cloneIfNeeded`, `configureUserIfSet`, `recoverUntracked`, and `syncOnStartup(ctx, a.Repo, sshCommand)`.
- `syncOnStartup(parentCtx, repoDir, sshCommand)` checks `.git`, applies a 60 s `syncOnStartupTimeout`, checks `git remote`, runs raw `git pull`, warn-logs `startup git pull failed (puller will retry)` on failure, then runs raw `git push`.
- `main_test.go` calls `main.SyncOnStartup(ctx, <dir>, "")` in 7 places through the `SyncOnStartup` var in `export_test.go`.
- `pkg/git/git_test.go` already has `setupRescueFixture() (workDir string, advanceRemote func(), dirtyTree func(), remoteDir string, cleanup func())`, `gatherPullRescues() float64`, `captureSlogLogs() (*bytes.Buffer, func())`, and `gitOutputStr(dir string, args ...string) string` (panics on a non-zero git exit).
- `metrics.Metrics` already has `IncPullRescue()`; no metric change is needed.

**Sibling prompts (do not do their work):**

- Prompt 2 adds per-cycle stale-lock cleanup in `pkg/git/git.go` and its tests. Do NOT add lock cleanup, `lsof`, or `/proc` probing here.
- Prompt 3 adds the `retryPush` regression test, the spec 014 Failure-Modes amendment, `docs/deployment.md`, and `CHANGELOG.md`. Do NOT touch those here.

<!-- OPEN QUESTION for the reviewer: prompts 1 and 2 both edit pkg/git/git.go and pkg/git/git_test.go. They are independent in behaviour, but if dark-factory executes them concurrently on one branch the two edits must merge cleanly. This prompt touches only pullMergeAndPush / retryPush / rescueDirtyTree / syncOnStartup / main.go; prompt 2 touches Pull, new lock helpers and new test blocks. Approve them in sequence if the runner does not serialise. -->

<!-- OPEN QUESTION for the reviewer: routing the startup sync through the shared Git client means the startup path now authenticates exactly like the puller (GIT_SSH_KEY-derived command). The old startup path used the resolved --git-ssh-command override; createGitClient never passes that override to pkg/git. This matches the puller's existing behaviour but narrows startup's auth to the same key. Flag if a deployment relies on --git-ssh-command for the boot sync only. -->
</context>

<requirements>

## 1. Make the rescue reset target path-dependent

Change `rescueDirtyTree`'s signature in `pkg/git/git.go` to:

```go
func (g *git) rescueDirtyTree(ctx context.Context, upstream, resetRef string) error
```

Inside it, replace the hardcoded reset call:

```go
if err := g.runCmd(ctx, g.repoPath, "reset", "--hard", upstream); err != nil {
	return errors.Wrapf(ctx, err, "reset --hard %s after rescue", upstream)
}
```

with:

```go
if err := g.runCmd(ctx, g.repoPath, "reset", "--hard", resetRef); err != nil {
	return errors.Wrapf(ctx, err, "reset --hard %s after rescue", resetRef)
}
```

Nothing else in the function changes: the branch name, `createRescueCommit`, the local ref creation before the push, the push refspec, the `rescue branch pushed` INFO line, `g.metrics.IncPullRescue()`, and the `git clean -fd` after the reset all stay byte-identical. Update the doc comment to state the reset-target rule:

```go
// resetRef is path-dependent and load-bearing: the fast-forward path passes the
// upstream tracking ref (its HEAD holds no unique commit), while the diverged,
// push-retry and startup paths pass "HEAD" so the local commits they are about
// to merge survive. It is never a hardcoded branch name.
```

Update the one existing call site in `fastForwardOrRescue` to `g.rescueDirtyTree(ctx, upstream, upstream)`.

## 2. Add the shared merge-with-rescue helper

Add this unexported method next to `pullMergeAndPush`:

```go
// mergeWithDirtyTreeRescue runs `git merge --no-edit upstream`, rescuing a dirty
// working tree when (and only when) the merge fails, then retrying the merge
// once inline. It returns the merge output and error, plus a non-nil rescueErr
// when the rescue itself failed — in which case the merge was aborted and the
// caller must return rescueErr without entering the conflict path.
//
// The working tree is inspected BEFORE the merge is attempted, so the decision
// never matches git's abort text. The reset target is HEAD, never upstream:
// this path's HEAD carries local commits that must survive the merge.
func (g *git) mergeWithDirtyTreeRescue(
	ctx context.Context,
	upstream string,
) ([]byte, error, error) {
	dirty := g.workingTreeDirty(ctx)
	out, mergeErr := g.runCmdRaw(ctx, g.repoPath, "merge", "--no-edit", upstream)
	if mergeErr == nil || !dirty {
		return out, mergeErr, nil
	}
	if rescueErr := g.rescueDirtyTree(ctx, upstream, "HEAD"); rescueErr != nil {
		// A merge abort must never leave MERGE_HEAD behind.
		_, _ = g.runCmdRaw(ctx, g.repoPath, "merge", "--abort")
		return out, mergeErr, rescueErr
	}
	out, mergeErr = g.runCmdRaw(ctx, g.repoPath, "merge", "--no-edit", upstream)
	return out, mergeErr, nil
}
```

Properties that must hold exactly:

- The `workingTreeDirty` call comes BEFORE the merge. A failing `git status` reports a clean tree, so the merge is still attempted and its own error surfaces.
- A clean merge never runs a rescue.
- A rescue runs only when the merge failed AND the tree was dirty before the merge.
- The merge is retried at most once, inline. Never call `g.Pull(ctx)` or `g.syncWithUpstream(ctx, ...)` from here — `Pull` holds `g.mu` for its whole body and `sync.Mutex` is not reentrant.
- A rescue failure aborts any in-progress merge before returning.
- The reset target passed to `rescueDirtyTree` is the literal `"HEAD"`.

## 3. Wire the rescue into `pullMergeAndPush`

Replace the body of `pullMergeAndPush` with:

```go
func (g *git) pullMergeAndPush(ctx context.Context, upstream string) error {
	out, mergeErr, rescueErr := g.mergeWithDirtyTreeRescue(ctx, upstream)
	if rescueErr != nil {
		g.metrics.IncGitOperationError("merge")
		g.metrics.IncMergeOutcome("aborted")
		return errors.Wrap(ctx, rescueErr, "merge after dirty-tree rescue failed")
	}
	if mergeErr == nil {
		// Clean merge: git auto-produced a merge commit.
		g.metrics.IncMergeOutcome("clean")
		slog.InfoContext(ctx, "git-rest: merged diverged history", "upstream", upstream)
		if pushErr := g.pushWithRetry(ctx, upstream); pushErr != nil {
			g.metrics.IncGitOperationError("push")
			return errors.Wrap(ctx, pushErr, "push after clean merge failed")
		}
		return nil
	}
	return g.resolveConflictMerge(ctx, upstream, out, mergeErr)
}
```

The clean-merge branch (metrics, log line, `pushWithRetry`, error wrapping) and the conflict delegation must stay behaviourally identical. Only the merge call and the rescue-failure branch are new.

## 4. Wire the rescue into `retryPush`

Replace the merge block in `retryPush` with:

```go
	mergeOut, mergeErr, rescueErr := g.mergeWithDirtyTreeRescue(ctx, upstream)
	if rescueErr != nil {
		return errors.Wrap(ctx, rescueErr, "merge after dirty-tree rescue failed")
	}
	if mergeErr != nil {
		_, _ = g.runCmdRaw(ctx, g.repoPath, "merge", "--abort")
		slog.WarnContext(
			ctx,
			"git-rest: retry merge failed",
			"upstream",
			upstream,
			"err",
			mergeErr.Error(),
			"output",
			strings.TrimSpace(string(mergeOut)),
		)
		return errors.Wrapf(ctx, mergeErr, "merge %s failed", upstream)
	}
	return nil
```

Everything before the merge (jitter, `IncPushRetry`, upstream resolution, `git fetch`) and everything after a successful merge stays unchanged. The existing `merge --abort` + warn + `merge %s failed` behaviour is preserved for a non-dirty merge failure and for a post-rescue merge failure.

## 5. Route the startup sync through `Pull`

In `main.go`:

1. Remove the `syncOnStartup` call (and its `errors.Wrap(..., "sync on startup")`) from `bootstrap`. Keep `cleanupStaleLocks`, `recoverUntracked` and the `sshCommand` variable — `sshCommand` is still used by `recoverUntracked`.
2. In `Run`, after `gitClient, err := a.createGitClient(ctx)` succeeds and before `service.Run(...)`, call `syncOnStartup(ctx, gitClient)`.
3. Replace `syncOnStartup` with a void function that delegates to the shared four-state pull:

```go
// syncOnStartup runs one best-effort sync against the upstream tracking ref
// before the HTTP server starts serving. It delegates to Pull so the startup
// path uses the same four-state machine — and therefore the same
// dirty-working-tree rescue — as every later cycle.
//
// A failure is warn-logged and never aborts startup: the puller retries on its
// next interval. It must run after createGitClient, because the client is what
// exposes Pull.
func syncOnStartup(ctx context.Context, g git.Git) {
	ctx, cancel := context.WithTimeout(ctx, syncOnStartupTimeout)
	defer cancel()

	if err := g.Pull(ctx); err != nil {
		slog.WarnContext(ctx, "startup git pull failed (puller will retry)", "error", err)
		return
	}
	slog.InfoContext(ctx, "startup git pull succeeded")
}
```

- The function returns nothing. Do not keep an always-nil `error` return (the `unparam` linter rejects it).
- The raw `git pull` and `git push` calls and the `.git` / `git remote` pre-checks are removed: `Pull` performs the no-remote early return, the state machine and the push. `syncOnStartupTimeout` stays.
- `runGitCmd` stays: `recoverUntracked` still uses it.
- `Run` must not fail when `syncOnStartup` logs a failure; the boot sync remains best-effort exactly as today.

## 6. Update `export_test.go` and `main_test.go`

`export_test.go`: the `var SyncOnStartup = syncOnStartup` line stays; the function value now has type `func(context.Context, git.Git)`.

`main_test.go`:

- Add a helper inside the `SyncOnStartup` Describe:

```go
newStartupGit := func(repoDir string) git.Git {
	return git.New(
		repoDir,
		metrics.NewMetrics(),
		libtime.NewCurrentDateTime(),
		"",
		git.NewMarkerResolver(repoDir),
	)
}
```

- Replace every `Expect(main.SyncOnStartup(ctx, <dir>, "")).To(Succeed())` with a bare `main.SyncOnStartup(ctx, newStartupGit(<dir>))` — the function is now void, so there is no error to assert. Do **not** re-add a `Pull` success assertion: the "no remote" case does return nil from `Pull` (the `hasRemote` early return), but the "no `.git`" case does **not** — `hasRemote` returns true when `git remote` errors, so `Pull` fails at `@{u}` resolution and `syncOnStartup` warn-logs it. Rename that `It` from "returns nil" to "does not panic".
- Add the import `"github.com/bborbe/git-rest/pkg/metrics"` to `main_test.go`.
- Update the stale comment **and** the `It` description in the "both local and remote have new commits (disjoint files)" Context: it no longer "warns and fails the push" — `Pull` now merges and pushes. That Context currently has no assertion beyond the success check, so **add** one: after the call, `Expect(strings.TrimSpace(gitOutputStr(localDir, "status", "--porcelain"))).To(BeEmpty())` and `Expect(localLogCount(localDir)).To(Equal(before + 1))`.

## 7. Add a startup rescue test

Add a new Context inside the `SyncOnStartup` Describe:

```go
Context("when the working tree is dirty and the branch is diverged at boot", func() {
	It("rescues the dirty tree, merges and pushes", func() {
		remoteDir, localDir := initRepoWithLocalRemote()
		runLocal := func(args ...string) {
			full := append([]string{"-C", localDir}, args...)
			cmd := exec.Command("git", full...)
			out, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), string(out))
		}
		Expect(os.WriteFile(
			filepath.Join(localDir, "local-file.md"), []byte("local"), 0o644,
		)).To(Succeed())
		runLocal("add", "local-file.md")
		runLocal("commit", "-m", "local commit")

		secondDir := filepath.Join(GinkgoT().TempDir(), "second")
		Expect(exec.Command("git", "clone", remoteDir, secondDir).Run()).To(Succeed())
		runSecond := func(args ...string) {
			full := append([]string{"-C", secondDir}, args...)
			cmd := exec.Command("git", full...)
			out, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), string(out))
		}
		runSecond("config", "user.email", "test@example.com")
		runSecond("config", "user.name", "Test")
		Expect(os.WriteFile(
			filepath.Join(secondDir, ".gitkeep"), []byte("remote\n"), 0o644,
		)).To(Succeed())
		runSecond("add", ".gitkeep")
		runSecond("commit", "-m", "remote commit")
		runSecond("push", "origin", "main")

		// Dirty the file the remote commit touches, so the diverged merge aborts.
		Expect(os.WriteFile(
			filepath.Join(localDir, ".gitkeep"), []byte("LOCAL UNCOMMITTED\n"), 0o644,
		)).To(Succeed())

		main.SyncOnStartup(ctx, newStartupGit(localDir))

		Expect(strings.TrimSpace(gitOutput(localDir, "status", "--porcelain"))).To(BeEmpty())
		Expect(gitOutput(localDir, "log", "--oneline")).To(ContainSubstring("local commit"))
		Expect(gitOutput(localDir, "for-each-ref", "--format=%(refname)", "refs/heads/rescue/")).
			NotTo(BeEmpty())
		Expect(gitOutput(remoteDir, "for-each-ref", "--format=%(refname)", "refs/heads/rescue/")).
			NotTo(BeEmpty())
		content, readErr := os.ReadFile(filepath.Join(localDir, ".gitkeep"))
		Expect(readErr).NotTo(HaveOccurred())
		Expect(content).To(Equal([]byte("remote\n")))
	})
})
```

Define a tiny helper in `main_test.go` (there is no `gitOutputStr` there) — either inline `exec.Command("git", "-C", dir, args...).Output()` wrapped in a Gomega assertion, or add:

```go
gitOutput := func(dir string, args ...string) string {
	full := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", full...).Output()
	Expect(err).NotTo(HaveOccurred())
	return string(out)
}
```

Place it inside the new Context (or the Describe) so it does not collide with other helpers.

## 8. Add a diverged-path rescue test in `pkg/git/git_test.go`

Append a new top-level Describe (do not modify the existing `Dirty working tree rescue` Describe):

```go
var _ = Describe("Dirty tree rescue on the diverged path", func() {
	var (
		workDir       string
		advanceRemote func()
		rescueCleanup func()
		pg            git.Git
		ctx           context.Context
	)

	BeforeEach(func() {
		ctx = context.Background()
		workDir, advanceRemote, _, _, rescueCleanup = setupRescueFixture()
		pg = git.New(
			workDir,
			metrics.NewMetrics(),
			libtime.NewCurrentDateTime(),
			"",
			git.NewMarkerResolver(workDir),
		)
	})

	AfterEach(func() {
		rescueCleanup()
	})

	It("AC1/AC2/AC7: rescues, merges, pushes and keeps the local commit", func() {
		// A local commit on its own file makes the branch diverged; the remote
		// commit touches the file the working tree will dirty.
		runGit(workDir, "config", "user.email", "test@example.com")
		runGit(workDir, "config", "user.name", "Test User")
		Expect(os.WriteFile(
			filepath.Join(workDir, "tasks", "z.md"), []byte("local only\n"), 0o600,
		)).To(Succeed())
		runGit(workDir, "add", "-A")
		runGit(workDir, "commit", "-m", "local work")
		advanceRemote()
		Expect(os.WriteFile(
			filepath.Join(workDir, "tasks", "x.md"),
			[]byte("line one\nLOCAL UNCOMMITTED EDIT\n"),
			0o600,
		)).To(Succeed())

		before := gatherPullRescues()
		Expect(pg.Pull(ctx)).To(Succeed())

		Expect(strings.TrimSpace(gitOutputStr(workDir, "status", "--porcelain"))).To(BeEmpty())
		logs := gitOutputStr(workDir, "log", "--oneline")
		Expect(strings.Count(logs, "local work")).To(Equal(1))
		Expect(strings.Count(logs, "external: touches x")).To(Equal(1))
		Expect(gatherPullRescues() - before).To(Equal(1.0))

		refs := strings.Fields(strings.TrimSpace(gitOutputStr(
			workDir, "for-each-ref", "--format=%(refname)", "refs/heads/rescue/",
		)))
		Expect(refs).To(HaveLen(1))
		commit := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", refs[0]))
		Expect(gitOutputStr(workDir, "show", commit+":tasks/x.md")).
			To(ContainSubstring("LOCAL UNCOMMITTED EDIT"))
		Expect(strings.TrimSpace(gitOutputStr(
			workDir, "ls-remote", "--heads", "origin", "refs/heads/rescue/*",
		))).To(ContainSubstring(refs[0]))
	})
})
```

The `remoteDir` element of `setupRescueFixture`'s return is not needed here — bind it to `_` in the `BeforeEach` tuple, as the code above already does. Do **not** declare an unused `remoteDir` var: an assigned-but-never-read local is a compile error in Go, not a lint warning. `runGit` panics on failure and is already defined in the file.

## 9. Self-check

Before finishing, re-run the `<verification>` commands. Walk each requirement against the diff:

- `rescueDirtyTree` takes `resetRef`; `fastForwardOrRescue` passes `upstream`; the shared merge helper passes `"HEAD"`.
- No second rescue implementation exists; `createRescueCommit` and the push/ref/reset ordering are untouched.
- No call to `g.Pull` or `g.syncWithUpstream` was added inside `pkg/git/git.go`.
- Every failure branch after a merge attempt runs `git merge --abort`.
- `syncOnStartup` is void, delegates to `Pull`, and is called from `Run` after `createGitClient`; `bootstrap` no longer calls it.
- No metric was added and `metrics.Metrics` is unchanged.
- `docs/deployment.md`, `CHANGELOG.md`, `specs/in-progress/014-*.md` and any lock-cleanup code are untouched.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT reimplement the rescue. Spec 014's capture contract is reused verbatim: object-only commit on a temporary non-existent index path, real index untouched, HEAD unmoved by the capture, `git stash create` forbidden, no reset until the push succeeded, `rescue/<timestamp>` branch, never force-pushed and never deleted.
- The reset target is path-dependent and MUST be exactly: `upstream` on the fast-forward path; the literal `"HEAD"` on the diverged, push-retry and startup paths. Never hardcode a branch name.
- A successful rescue on the diverged/push-retry path MUST re-run the merge inline, at most once. NEVER call `g.Pull(ctx)` — `Pull` holds `g.mu` for its whole body and `sync.Mutex` is not reentrant. Do not call `g.syncWithUpstream` from inside `pullMergeAndPush` or `retryPush` either.
- Every failure branch that ran a merge MUST run `git merge --abort` before returning (`retryPush` and `resolveConflictMerge` already do this; the new rescue-failure branches must preserve it).
- Do NOT change `parseMergeConflictPaths`, `resolveConflictMerge`, `resolveConflictPaths`, the quarantine code, the `ConflictResolver` interface, `pkg/git/conflict_resolver.go`, `pkg/git/yaml_merge_resolver.go`, or `mocks/conflict_resolver.go`.
- Do NOT add a Prometheus metric and do NOT change the `Metrics` interface or `mocks/metrics.go`. The rescue counter `git_rest_pull_rescues_total` already exists and is incremented inside `rescueDirtyTree`; every path must increment it exactly once per rescue event by reusing that function.
- Do NOT add stale-lock cleanup, `lsof`, `/proc` probing, or any change to `main.go`'s `cleanupStaleLocks` — prompt 2 owns that.
- Do NOT touch `docs/deployment.md`, `CHANGELOG.md`, `specs/in-progress/014-*.md`, or `docs/verifying-specs.md` — prompt 3 owns those.
- Do NOT change the public HTTP contract in `docs/api.md` or readiness semantics.
- Errors MUST be wrapped with `errors.Wrap` / `errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf`, never a bare `return err`.
- Logging MUST use `log/slog`; never `context.Background()` in `pkg/`. The rescue branch timestamp MUST come from `g.currentDateTimeGetter.Now()` — never `time.Now()`.
- The startup sync MUST remain best-effort: a failed `Pull` is warn-logged and never aborts startup.
- `syncOnStartup` MUST NOT return an always-nil `error` (the `unparam` linter rejects it).
- Existing tests must still pass, including the whole `SyncOnStartup` Describe after its call-shape update, the `Pull state machine` Describe, and the existing `Dirty working tree rescue` Describe.
- New/changed code MUST reach >=80% statement coverage. Requirement 7 covers the startup path and requirement 8 the diverged happy path; the `rescueErr` branches in `mergeWithDirtyTreeRescue` and `pullMergeAndPush` are **not** covered by either and MUST get their own test: reuse the pre-receive hook from `pkg/git/git_test.go`'s "AC5: a rejected rescue push…" (reject `refs/heads/rescue/*`) on the diverged fixture, then assert `Pull` returns an error containing `rescue push failed`, `git rev-parse HEAD` is unchanged, `status --porcelain` still shows the dirty file, and no `MERGE_HEAD` exists in `.git/`. The `retryPush` rescue is covered by the regression test in prompt 3, which depends on this prompt — do not merge prompt 1 without prompt 3.
- Linter limits: `funlen` 80 lines / 50 statements, `nestif` complexity 4, `gocognit` 20, `golines` max-len 100.
- `make precommit` from the repo root MUST exit 0.
</constraints>

<verification>
Run from the repo root.

```bash
make precommit
```

Must exit 0.

Iterative runs while implementing:

```bash
make test
go test ./pkg/git/... -ginkgo.focus='Dirty tree rescue on the diverged path'
go test ./pkg/git/... -ginkgo.focus='Dirty working tree rescue'
go test . -ginkgo.focus='SyncOnStartup'
```

All pass.

Coverage for the changed packages:

```bash
go test -coverprofile=/tmp/cover.out ./pkg/git/... . && go tool cover -func=/tmp/cover.out
```

The new/changed functions (`rescueDirtyTree`, `mergeWithDirtyTreeRescue`, `pullMergeAndPush`, `retryPush`, `syncOnStartup`) must each report a non-zero coverage percentage, and neither package total may fall below its pre-change value.

Mechanism checks — each must print at least one match:

```bash
grep -n 'mergeWithDirtyTreeRescue' pkg/git/git.go
grep -n 'resetRef' pkg/git/git.go
grep -n 'rescueDirtyTree(ctx, upstream, "HEAD")' pkg/git/git.go
grep -n 'rescueDirtyTree(ctx, upstream, upstream)' pkg/git/git.go
grep -n 'func syncOnStartup(ctx context.Context, g git.Git)' main.go
grep -n 'syncOnStartup(ctx, gitClient)' main.go
```

Banned-construction checks — each must print NOTHING:

```bash
! grep -vE '^\s*//' pkg/git/git.go | grep -q 'g\.Pull(ctx)'
! grep -n 'syncOnStartup(ctx, a.Repo, sshCommand)' main.go
! grep -n 'stash create' pkg/git/git.go
! grep -n 'lsof\|/proc' pkg/git/git.go
```

Comment lines are excluded from the first check on purpose: the `syncWithUpstream` doc comment names `g.Pull(ctx)` to warn against calling it.

Ordering check — `reset --hard` must still appear only inside `rescueDirtyTree`, after the push:

```bash
grep -vE '^\s*//' pkg/git/git.go | grep -n 'reset --hard\|"clean", "-fd"'
```

After stripping comment lines, every match must be inside `rescueDirtyTree` and after the `slog.InfoContext(... "rescue branch pushed" ...)` call.

Self-check before finishing: re-run `make precommit`, then walk requirement 9 against the change.
</verification>
