---
status: completed
spec: [014-bug-pull-cannot-recover-from-dirty-working-tree]
summary: 'Taught Pull to rescue a dirty working tree: capture the whole tree as an object-only commit on a rescue/<timestamp> branch, push it, and only then reset --hard + clean -fd, with 5 new specs and spec 006''s ban narrowed.'
execution_id: git-rest-exec-054-spec-014-dirty-tree-rescue
dark-factory-version: v0.196.0
created: "2026-09-25T10:39:18Z"
queued: "2026-09-25T11:34:36Z"
started: "2026-09-25T11:34:38Z"
completed: "2026-09-25T11:38:49Z"
branch: dark-factory/bug-pull-cannot-recover-from-dirty-working-tree
---

# Rescue a dirty working tree before a fast-forward pull

<summary>
- A pod whose working tree holds uncommitted changes recovers from a blocked fast-forward pull on its own
- The uncommitted changes are preserved on a `rescue/<timestamp>` branch pushed to the vault's own remote
- The working tree is returned to the remote state only after that push has succeeded, so nothing is ever discarded
- A rejected rescue push resets nothing: HEAD stays put, the tree stays dirty, and the local rescue branch remains for a manual retry
- Every shape of dirt is captured: unstaged edits, staged changes, deletions and untracked files
- Ignored files (secrets, keys) are excluded from the rescue commit by construction
- The pull completes and readiness returns to ready within one pull interval, without operator action
- A clean fast-forward, a push, a no-op and the existing committed-divergence merge behave exactly as before
- Spec 006's ban on automatic `git reset --hard` is narrowed to this one rescue path, where it is safe
</summary>

<objective>
Teach `Pull` to recover when `git merge --ff-only` fails because the working tree holds uncommitted changes. Capture the whole working-tree state as an object-only commit on a `rescue/<timestamp>` branch, push it to the upstream remote, and only then `git reset --hard` + `git clean -fd` back to the upstream state. This closes the last unrecovered git shape — a modified tracked file — which wedged `vault-obsidian-agent-0` on 2026-09-24 and stalled the agent-task-executor reconcile loop behind it.
</objective>

<context>
Read `CLAUDE.md` at the repo root for project conventions.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `errors.Wrap` / `errors.Wrapf` from `github.com/bborbe/errors`; never `fmt.Errorf`, never a bare `return err`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md` — `log/slog` with `slog.InfoContext` / `slog.WarnContext` / `slog.ErrorContext`; the `pkg/puller` and `pkg/git` call sites already follow this
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, external test packages, real `git` via `os/exec` against a temp working tree
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md` — timestamps come from the injected `libtime` getter, never `time.Now()`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` heading; `fix:` prefix rules

Files to read in full before editing (line numbers are hints; anchor by function name):

- `pkg/git/git.go` — `Pull` (~1168, the 4-state machine; the `localSHA == baseSHA` case at ~1211 is the branch being changed), `pullFetchSHAs` (~641), `runCmd` / `runCmdOutput` / `runCmdRaw` (~195-261, the exec helpers the new env-aware helpers sit beside), `recoverRepoState` (~1072), `commitAndPushMerge` (~858, the precedent for commit + push under the mutex), `parseMergeConflictPaths` (~94)
- `pkg/git/git_test.go` — `runGit` (~29), `setupPullFixture` (~689, the fixture shape to model the new one on), `writeLocalCommit` (~741), `gitOutputStr` (~750), `captureSlogLogs` (~1418), the `Pull state machine` Describe (~1006)
- `pkg/metrics/metrics.go` — the `Metrics` interface (~123) and `prometheusMetrics` (~149); this prompt adds no method, but read it so prompt 2's addition lands on the right shape
- `specs/completed/006-bug-pull-cannot-recover-from-divergence.md` — its `## Constraints` bullet beginning "MUST NOT auto-clobber local commits" is the constraint this prompt amends
- `specs/in-progress/014-bug-pull-cannot-recover-from-dirty-working-tree.md` — `## Desired Behavior`, `## Constraints`, `## Failure Modes`, `## Security / Abuse Cases`
- `CHANGELOG.md` — no `## Unreleased` section exists yet (the top heading is `## v0.26.0`); create one under the SemVer preamble
- `docs/dod.md` — the project's definition of done

**Verified facts about the current code:**

- `Pull(ctx context.Context) error` takes `g.mu.Lock()` with `defer g.mu.Unlock()` and registers `defer g.refreshQuarantinedBacklog(ctx)` after it, then returns early for `!g.hasRemote(ctx)`, `recoverRepoState` failure, and `@{u}` resolution failure.
- `Pull` resolves `upstream` with `git rev-parse --abbrev-ref --symbolic-full-name @{u}`; the value is of the form `origin/master`.
- The `localSHA == baseSHA` case is a single `if err := g.runCmd(ctx, g.repoPath, "merge", "--ff-only", upstream); err != nil { ... }` returning `errors.Wrap(ctx, err, "fast-forward merge failed")`.
- `runCmd` / `runCmdOutput` / `runCmdRaw` build `cmd.Env` ONLY when `g.sshKeyPath != ""`; otherwise `cmd.Env` is nil and the child inherits the parent environment.
- `pkg/git/git.go` already imports `bytes`, `context`, `stderrors "errors"`, `fmt`, `io/fs`, `log/slog`, `os`, `os/exec`, `path/filepath`, `sort`, `strconv`, `strings`, `sync`, `time`, `github.com/bborbe/errors`, `libtime "github.com/bborbe/time"`, `github.com/bborbe/git-rest/pkg/metrics`.
- `libtime.DateTime` has `Format(layout string) string`, `UTC() DateTime` and `Unix() int64` (verified in `github.com/bborbe/time@v1.27.14`, `time_date-time.go`). `g.currentDateTimeGetter.Now()` returns a `libtime.DateTime`.
- `pkg/git/git_test.go` is `package git_test` and already imports `bytes`, `context`, `errors`, `fmt`, `log/slog`, `os`, `os/exec`, `path/filepath`, `strings`, `libtime "github.com/bborbe/time"`, Ginkgo, Gomega, `prometheus`, `github.com/bborbe/git-rest/mocks`, `github.com/bborbe/git-rest/pkg/git`, `github.com/bborbe/git-rest/pkg/metrics`.
- `setupPullFixture() (workDir string, externalPush func(file, content string), cleanup func())` creates a temp repo backed by a local bare remote whose initial commit is EMPTY, with upstream set — so it has no tracked files to modify. The new fixture below seeds tracked files instead.
- `gitOutputStr(dir string, args ...string) string` runs git and PANICS on a non-zero exit, returning combined output.
- `captureSlogLogs() (*bytes.Buffer, func())` swaps `slog.Default()` for a buffer-backed text handler at `slog.LevelInfo`.
- `git ls-remote --heads origin 'refs/heads/rescue/*'` and `git for-each-ref --format=%(refname) refs/heads/rescue/` both exit 0 with empty output when no ref matches — verified live.

**Verified git mechanics (replayed live on this machine before writing this prompt):**

- With `GIT_INDEX_FILE` pointing at a NON-EXISTENT path, `git add -A` succeeds, honours `.gitignore`, and populates the temp index; `git write-tree` then prints the tree SHA of the whole working tree. A zero-byte index file fails with `index file smaller than expected`, so the path must not pre-exist.
- `git commit-tree <tree> -p HEAD -m "<msg>"` prints the new commit SHA and moves nothing: HEAD, the real index and the working tree are untouched.
- `git diff --name-only HEAD <commit>` lists exactly the captured paths (modified + added + deleted). For the four-shape dirt below it prints `tasks/doomed.md`, `tasks/scratch.md`, `tasks/x.md` in that order.
- `git diff --name-status <commit>^ <commit>` prints `D<TAB>tasks/doomed.md`, `A<TAB>tasks/scratch.md`, `M<TAB>tasks/x.md`; `git show <commit>:tasks/doomed.md` fails because the path is ABSENT from the rescue tree; `git show <commit>:secrets.env` fails because ignored files are excluded.
- `git merge --ff-only origin/<branch>` on a dirty colliding tree aborts with `error: Your local changes to the following files would be overwritten by merge` and leaves HEAD, the index and the tree exactly as they were.
- `git reset --hard origin/<branch>` followed by `git clean -fd` yields an empty `git status --porcelain`, leaves `.gitignore`d files on disk, and removes untracked files.
- With `.git/index.lock` present, `git status --porcelain` exits 0 with empty output (so the tree reads as clean) while `git merge --ff-only` fails with `Unable to create '.../index.lock': File exists.` — this is the deterministic construction for a non-dirty fast-forward failure.
- `git stash create` is NOT usable here: it captures tracked changes only and returns an empty SHA for an untracked-only tree. Do NOT use it.
- In zsh, `"$VAR:suffix"` is mangled by the `:t`/`:r` modifiers; use `${VAR}` braces in any shell snippet.

**Interaction with quarantine (factual, no behavior change required):** the rescue commit is built from a temp index fed by `git add -A`, so any untracked or staged-but-uncommitted `_conflicts/` residue is captured on the rescue branch and then removed from the working tree by `git clean -fd`. The content stays recoverable from the rescue branch and the INFO log names those paths. Spec 012/013's quarantine code, the resolver interface and the `_conflicts/` layout are untouched.

**Sibling prompts (do not do their work):**

- Prompt 2 adds the `git_rest_pull_rescues_total` counter, the `Metrics` interface method, the regenerated `mocks/metrics.go`, and the counter call site inside `rescueDirtyTree`. Do NOT add any metric in this prompt.
- Prompt 3 adds the untouched-path regression tests and the three undocumented `CLAUDE.md` invariants.
- Prompt 4 rewrites the verification docs. Do NOT touch `docs/verifying-specs.md` or `docs/deployment.md` here.

**Out of scope (do not attempt):** the diverged-state dirty tree (`localSHA != baseSHA`, which goes through `pullMergeAndPush` → `git merge --no-edit`) is a known remaining gap of the same class, deliberately excluded by the spec. Do not touch `pullMergeAndPush` or `resolveConflictMerge`. The boot path (`recoverUntracked`, `syncOnStartup` in `main.go`) is unchanged — it may consume a dirty tree before the puller's first cycle, which is fine.
</context>

<requirements>

## 1. Narrow spec 006's `git reset --hard` ban

Edit `specs/completed/006-bug-pull-cannot-recover-from-divergence.md`. In its `## Constraints` section, replace the bullet that currently reads:

```
- MUST NOT auto-clobber local commits. Rebase preserves local work; on conflict the repo is left for human inspection. `git reset --hard` and `git rebase --abort` are NEVER invoked automatically.
```

with:

```
- MUST NOT auto-clobber local commits. Rebase preserves local work; on conflict the repo is left for human inspection. `git rebase --abort` is NEVER invoked automatically.
- **Amended by spec 014 (`014-bug-pull-cannot-recover-from-dirty-working-tree.md`):** `git reset --hard`, paired with `git clean -fd`, IS invoked automatically on exactly one path — the dirty-working-tree rescue — and only AFTER the whole working-tree state has been pushed to a `rescue/<timestamp>` branch on the remote, so no commit and no uncommitted change can be lost. Everywhere else the ban stands unchanged.
```

No other part of 006 changes. This amendment is what legalises requirement 7 below.

## 2. Add three environment-aware git exec helpers to `pkg/git/git.go`

Place them immediately after `runCmdRaw`. Do NOT modify `runCmd`, `runCmdOutput` or `runCmdRaw` — their existing behaviour must stay byte-identical.

```go
// childEnv returns the child-process environment for git: the inherited
// environment, plus GIT_SSH_COMMAND when an SSH key is configured, plus any
// extra entries supplied by the caller.
func (g *git) childEnv(extraEnv ...string) []string {
	env := os.Environ()
	if g.sshKeyPath != "" {
		env = append(
			env,
			"GIT_SSH_COMMAND=ssh -i "+string(g.sshKeyPath)+
				" -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no",
		)
	}
	return append(env, extraEnv...)
}

// runCmdEnv executes a git subcommand in dir with extra environment entries,
// combining stdout+stderr into any error message.
func (g *git) runCmdEnv(
	ctx context.Context,
	dir string,
	extraEnv []string,
	args ...string,
) error {
	// #nosec G204 -- binary is hardcoded to "git"; args are internal subcommands, not user input
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = g.childEnv(extraEnv...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return errors.Wrapf(ctx, err, "git %v: %s", args, buf.String())
	}
	return nil
}

// runCmdOutputEnv executes a git subcommand in dir with extra environment
// entries and returns its stdout.
func (g *git) runCmdOutputEnv(
	ctx context.Context,
	dir string,
	extraEnv []string,
	args ...string,
) ([]byte, error) {
	// #nosec G204 -- binary is hardcoded to "git"; args are internal subcommands, not user input
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = g.childEnv(extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, errors.Wrapf(ctx, err, "git %v: %s", args, stderr.String())
	}
	return stdout.Bytes(), nil
}
```

## 3. Add the dirty-tree detector

Place it directly above `rescueDirtyTree` (requirement 5). It is a pure read; it never returns an error.

```go
// workingTreeDirty reports whether `git status --porcelain` lists at least one
// path — a staged change, an unstaged change to a tracked file, a deletion, or an
// untracked file. A FAILING `git status` reports false, so the caller still
// attempts the merge and surfaces the merge's own error.
func (g *git) workingTreeDirty(ctx context.Context) bool {
	out, err := g.runCmdOutput(ctx, g.repoPath, "status", "--porcelain")
	if err != nil {
		slog.WarnContext(
			ctx,
			"git-rest: git status failed before fast-forward; treating tree as clean",
			"err",
			err.Error(),
		)
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}
```

The detection MUST be a working-tree inspection and MUST NOT match git's abort text — the wording is git-version-dependent.

## 4. Add the object-only rescue commit builder

```go
// createRescueCommit writes a commit object capturing the whole working-tree
// state — staged, unstaged, deletions and untracked, excluding .gitignore'd
// paths — against HEAD as parent, WITHOUT moving HEAD and WITHOUT touching the
// real index. Returns the commit SHA and the captured paths (repo-relative).
func (g *git) createRescueCommit(ctx context.Context) (string, []string, error) {
	indexDir, err := os.MkdirTemp("", "git-rest-rescue-index-*")
	if err != nil {
		return "", nil, errors.Wrap(ctx, err, "create temporary index dir")
	}
	defer func() { _ = os.RemoveAll(indexDir) }()
	// The temporary index path must NOT exist yet: git rejects a zero-byte index
	// with "index file smaller than expected".
	indexFile := filepath.Join(indexDir, "index")
	env := []string{"GIT_INDEX_FILE=" + indexFile}

	if err := g.runCmdEnv(ctx, g.repoPath, env, "add", "-A"); err != nil {
		return "", nil, errors.Wrap(ctx, err, "stage working tree into temporary index")
	}
	treeOut, err := g.runCmdOutputEnv(ctx, g.repoPath, env, "write-tree")
	if err != nil {
		return "", nil, errors.Wrap(ctx, err, "write rescue tree")
	}
	tree := strings.TrimSpace(string(treeOut))

	commitOut, err := g.runCmdOutput(
		ctx,
		g.repoPath,
		"commit-tree",
		tree,
		"-p",
		"HEAD",
		"-m",
		"git-rest: rescue dirty working tree",
	)
	if err != nil {
		return "", nil, errors.Wrap(ctx, err, "create rescue commit object")
	}
	commit := strings.TrimSpace(string(commitOut))

	pathsOut, err := g.runCmdOutput(ctx, g.repoPath, "diff", "--name-only", "HEAD", commit)
	if err != nil {
		return "", nil, errors.Wrap(ctx, err, "list rescued paths")
	}
	paths := make([]string, 0, 8)
	for _, line := range strings.Split(strings.TrimSpace(string(pathsOut)), "\n") {
		if line != "" {
			paths = append(paths, line)
		}
	}
	return commit, paths, nil
}
```

Three properties are load-bearing and must not be changed:

- The index path comes from `os.MkdirTemp` + `filepath.Join(dir, "index")` so it does NOT pre-exist. `$(mktemp)`-style zero-byte creation is wrong.
- The temp index is fed by `git add -A`, which honours `.gitignore` — that is what excludes `secrets.env` / `*.pem` by construction.
- `git stash create` MUST NOT be used: it captures tracked changes only and returns an empty SHA for an untracked-only tree.

`commit-tree` takes no `GIT_INDEX_FILE` — the tree is passed explicitly — so it uses the existing `runCmdOutput`.

## 5. Add the rescue function

```go
// rescueDirtyTree captures the whole working-tree state on a
// rescue/<timestamp> branch and pushes it to the upstream remote. Only after that
// push succeeds does it return the working tree to the upstream state
// (reset --hard + clean -fd). It never discards a change: a failed push leaves
// HEAD unmoved, the working tree dirty, and the local rescue ref in place so the
// push can be retried by hand.
func (g *git) rescueDirtyTree(ctx context.Context, upstream string) error {
	branch := "rescue/" + g.currentDateTimeGetter.Now().UTC().Format("20060102T150405Z")

	commit, paths, err := g.createRescueCommit(ctx)
	if err != nil {
		return err
	}

	if err := g.runCmd(ctx, g.repoPath, "update-ref", "refs/heads/"+branch, commit); err != nil {
		return errors.Wrapf(ctx, err, "create local rescue ref %s", branch)
	}

	remote := strings.SplitN(upstream, "/", 2)[0]
	if err := g.runCmd(
		ctx,
		g.repoPath,
		"push",
		remote,
		"refs/heads/"+branch+":refs/heads/"+branch,
	); err != nil {
		slog.ErrorContext(
			ctx,
			"rescue push failed, leaving repo for inspection",
			"branch",
			branch,
			"err",
			err.Error(),
		)
		return errors.Wrapf(ctx, err, "rescue push failed, leaving repo for inspection")
	}

	slog.InfoContext(
		ctx,
		"rescue branch pushed",
		"branch",
		branch,
		"files",
		len(paths),
		"paths",
		strings.Join(paths, ","),
	)

	// Only now is the content safe on the remote, so only now may the local
	// working tree be returned to the upstream state.
	if err := g.runCmd(ctx, g.repoPath, "reset", "--hard", upstream); err != nil {
		return errors.Wrapf(ctx, err, "reset --hard %s after rescue", upstream)
	}
	if err := g.runCmd(ctx, g.repoPath, "clean", "-fd"); err != nil {
		return errors.Wrap(ctx, err, "git clean -fd after rescue")
	}
	return nil
}
```

Properties that must hold exactly:

- The timestamp layout is ISO-8601 BASIC UTC: `20060102T150405Z`. The extended form (`2006-01-02T15:04:05Z`) is NOT a legal git ref name — refs may not contain `:`. Build it with `g.currentDateTimeGetter.Now().UTC().Format(...)`; never `time.Now()`.
- The local ref is created BEFORE the push. It is what lets an operator re-push a failed rescue by hand; it is not cosmetic.
- `git reset --hard` and `git clean -fd` are reachable ONLY after the push returned success. On a push failure the function returns without touching HEAD, the index or the working tree.
- `reset --hard` targets the `upstream` value derived from `@{u}` — never a hardcoded `master`/`main`.
- The remote name is derived from `upstream` (`strings.SplitN(upstream, "/", 2)[0]`), never hardcoded to `origin`.
- The refspec is explicit (`refs/heads/<branch>:refs/heads/<branch>`) so the push does not depend on `push.default`.
- The INFO line begins `rescue branch pushed` and names the captured PATHS, not only a count. No truncation threshold — the captured set is bounded by the working tree.
- The rescue branch is never force-pushed and never deleted.
- Prompt 2 adds `g.metrics.IncPullRescue()` immediately after this INFO line. Do NOT add any metric call here.

## 6. Extract the state machine from `Pull` into `syncWithUpstream`

`Pull`'s body currently holds the 4-state switch. Move the state machine into an unexported method so the rescue path can re-evaluate it without re-entering `Pull`.

`Pull` keeps its signature, its `g.mu.Lock()`/`defer g.mu.Unlock()`, its `defer g.refreshQuarantinedBacklog(ctx)` registered after the unlock defer, its `ObserveGitOperation("pull", ...)` defer, the `!g.hasRemote(ctx)` early return, the `recoverRepoState` call and the `@{u}` resolution — then ends with:

```go
	return g.syncWithUpstream(ctx, upstream)
```

Add:

```go
// syncWithUpstream runs the deterministic 4-state sync against the resolved
// upstream tracking ref:
//   - local == remote        → no-op
//   - local clean, remote new → fast-forward (git merge --ff-only), with a
//     dirty-working-tree rescue when the merge refuses to clobber the tree
//   - local ahead, remote same → push
//   - diverged (both ahead)  → merge + push via pullMergeAndPush
//
// It recurses ONCE after a successful dirty-tree rescue, to re-evaluate the
// state machine. It must never call g.Pull(ctx): Pull holds g.mu for its whole
// body and sync.Mutex is not reentrant. Recursion depth is bounded to one — the
// rescue resets and cleans the working tree, so the re-evaluated pass can never
// take the dirty-tree branch again.
func (g *git) syncWithUpstream(ctx context.Context, upstream string) error {
	localSHA, remoteSHA, baseSHA, err := g.pullFetchSHAs(ctx, upstream)
	if err != nil {
		return err
	}

	switch {
	case localSHA == remoteSHA:
		return nil
	case localSHA == baseSHA:
		rescued, err := g.fastForwardOrRescue(ctx, upstream)
		if err != nil {
			return err
		}
		if rescued {
			return g.syncWithUpstream(ctx, upstream)
		}
		return nil
	case remoteSHA == baseSHA:
		if err := g.runCmd(ctx, g.repoPath, "push"); err != nil {
			g.metrics.IncGitOperationError("push")
			return errors.Wrap(ctx, err, "push failed")
		}
		return nil
	default:
		return g.pullMergeAndPush(ctx, upstream)
	}
}
```

The `pullFetchSHAs` call, the `IncGitOperationError("push")` on the push path, the `errors.Wrap(ctx, err, "push failed")` message and the `pullMergeAndPush` delegation must be preserved verbatim from today's `Pull`. Update `Pull`'s doc comment to describe the guards plus the delegation, and keep the 4-state description on `syncWithUpstream`.

## 7. Add the fast-forward-or-rescue branch

```go
// fastForwardOrRescue handles the localSHA == baseSHA case. Returns
// (rescued, err): rescued is true only when a dirty-working-tree rescue
// succeeded, which tells the caller to re-evaluate the state machine.
//
// The working tree is inspected BEFORE the merge is attempted, so the decision
// never depends on matching git's abort text. A failing git status reports a
// clean tree, so the merge is still attempted and its own error surfaces.
func (g *git) fastForwardOrRescue(ctx context.Context, upstream string) (bool, error) {
	dirty := g.workingTreeDirty(ctx)

	mergeErr := g.runCmd(ctx, g.repoPath, "merge", "--ff-only", upstream)
	if mergeErr == nil {
		return false, nil
	}
	if !dirty {
		g.metrics.IncGitOperationError("pull")
		return false, errors.Wrap(ctx, mergeErr, "fast-forward merge failed")
	}
	if err := g.rescueDirtyTree(ctx, upstream); err != nil {
		// The pull did NOT succeed: the rescue push failed and the repo is left
		// dirty and not-ready, so the existing git-operation-error signal — and the
		// alert on it at helm/templates/alerts.yaml:16 — must still fire.
		g.metrics.IncGitOperationError("pull")
		return false, err
	}
	return true, nil
}
```

- The `workingTreeDirty` call MUST come before the `merge --ff-only` call.
- When the tree is not dirty (including when `git status` itself failed), the merge is still attempted and the returned error is the merge's own, wrapped as `fast-forward merge failed` with `IncGitOperationError("pull")` recorded exactly as today.
- A **successful** rescue MUST NOT record `IncGitOperationError("pull")` — the pull succeeded. A **failed** rescue MUST record it: the push failed, the repo is left dirty and not-ready, and the existing `operation="pull"` alert must still fire on that path.
- The re-evaluation MUST be `g.syncWithUpstream(ctx, upstream)`. A recursive `g.Pull(ctx)` deadlocks: `Pull` holds `g.mu` for its whole body and `sync.Mutex` is not reentrant.

## 8. Add the test fixture `setupRescueFixture`

Add it to `pkg/git/git_test.go` next to `setupPullFixture`. It seeds the tracked files the spec's reproduction needs, because `setupPullFixture`'s initial commit is empty.

```go
// setupRescueFixture creates a working repo backed by a local bare remote whose
// initial commit tracks tasks/x.md, tasks/doomed.md and .gitignore. Returns
// workDir, a function that advances the remote with a commit touching
// tasks/x.md, a function that dirties the working tree in all four shapes, the
// bare remote path (for hook installation), and a cleanup func.
func setupRescueFixture() (
	workDir string,
	advanceRemote func(),
	dirtyTree func(),
	remoteDir string,
	cleanup func(),
) {
	remoteDir, err := os.MkdirTemp("", "git-remote-rescue-*")
	Expect(err).NotTo(HaveOccurred())
	workDir, err = os.MkdirTemp("", "git-work-rescue-*")
	Expect(err).NotTo(HaveOccurred())

	rg := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, e := cmd.CombinedOutput()
		Expect(e).NotTo(HaveOccurred(), "git %v: %s", args, string(out))
	}

	rg(remoteDir, "init", "--bare")
	rg(workDir, "init")
	rg(workDir, "config", "user.email", "test@example.com")
	rg(workDir, "config", "user.name", "Test User")
	rg(workDir, "remote", "add", "origin", remoteDir)

	Expect(os.MkdirAll(filepath.Join(workDir, "tasks"), 0o750)).To(Succeed())
	Expect(os.WriteFile(
		filepath.Join(workDir, "tasks", "x.md"), []byte("line one\n"), 0o600,
	)).To(Succeed())
	Expect(os.WriteFile(
		filepath.Join(workDir, "tasks", "doomed.md"), []byte("gone\n"), 0o600,
	)).To(Succeed())
	Expect(os.WriteFile(
		filepath.Join(workDir, ".gitignore"), []byte("secrets.env\n"), 0o600,
	)).To(Succeed())
	rg(workDir, "add", "-A")
	rg(workDir, "commit", "-m", "init")
	rg(workDir, "push", "-u", "origin", "HEAD")

	advanceRemote = func() {
		extDir, e := os.MkdirTemp("", "git-ext-rescue-*")
		Expect(e).NotTo(HaveOccurred())
		defer func() { _ = os.RemoveAll(extDir) }()
		rg(extDir, "clone", remoteDir, ".")
		rg(extDir, "config", "user.email", "ext@example.com")
		rg(extDir, "config", "user.name", "External")
		Expect(os.WriteFile(
			filepath.Join(extDir, "tasks", "x.md"), []byte("line one\nline two\n"), 0o600,
		)).To(Succeed())
		rg(extDir, "add", "-A")
		rg(extDir, "commit", "-m", "external: touches x")
		rg(extDir, "push", "origin")
	}

	dirtyTree = func() {
		Expect(os.WriteFile(
			filepath.Join(workDir, "tasks", "x.md"),
			[]byte("line one\nLOCAL UNCOMMITTED EDIT\n"),
			0o600,
		)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(workDir, "tasks", "scratch.md"), []byte("scratch\n"), 0o600,
		)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(workDir, "secrets.env"), []byte("secret\n"), 0o600,
		)).To(Succeed())
		rg(workDir, "rm", "-q", "tasks/doomed.md")
	}

	cleanup = func() {
		_ = os.RemoveAll(workDir)
		_ = os.RemoveAll(remoteDir)
	}
	return
}
```

## 9. Add the rescue specs to `pkg/git/git_test.go`

Add one new top-level `Describe` at the end of the file. The specs drive the real `Pull` path with the real `metrics.NewMetrics()` so the process-global registry is the one under test. Use `MatchRegexp` for the ref-name assertion (no new `regexp` import) and `captureSlogLogs` for the log assertions.

```go
var _ = Describe("Dirty working tree rescue", func() {
	var (
		workDir       string
		advanceRemote func()
		dirtyTree     func()
		remoteDir     string
		rescueCleanup func()
		pg            git.Git
		ctx           context.Context
	)

	// rescueRefs returns the local rescue refs in workDir, one per line, or "".
	rescueRefs := func() string {
		return strings.TrimSpace(gitOutputStr(
			workDir, "for-each-ref", "--format=%(refname)", "refs/heads/rescue/",
		))
	}
	// remoteRescueRefs returns the rescue refs visible on the remote, or "".
	remoteRescueRefs := func() string {
		return strings.TrimSpace(gitOutputStr(
			workDir, "ls-remote", "--heads", "origin", "refs/heads/rescue/*",
		))
	}

	BeforeEach(func() {
		ctx = context.Background()
		workDir, advanceRemote, dirtyTree, remoteDir, rescueCleanup = setupRescueFixture()
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

	It("AC1: completes the pull, cleans the tree and fast-forwards to the remote", func() {
		advanceRemote()
		dirtyTree()

		Expect(pg.Pull(ctx)).To(Succeed())

		upstream := strings.TrimSpace(gitOutputStr(
			workDir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}",
		))
		Expect(strings.TrimSpace(gitOutputStr(workDir, "status", "--porcelain"))).
			To(BeEmpty(), "the working tree must return to the remote state")
		Expect(gitOutputStr(workDir, "rev-parse", "HEAD")).
			To(Equal(gitOutputStr(workDir, "rev-parse", upstream)))
	})

	It("AC2: preserves every dirty shape on one rescue branch and excludes ignored files", func() {
		advanceRemote()
		dirtyTree()

		Expect(pg.Pull(ctx)).To(Succeed())

		refs := strings.Fields(rescueRefs())
		Expect(refs).To(HaveLen(1), "exactly one rescue branch per rescue event")
		Expect(refs[0]).To(MatchRegexp(`^refs/heads/rescue/[0-9]{8}T[0-9]{6}Z$`))

		// The same single ref is the one published to the remote.
		Expect(remoteRescueRefs()).To(ContainSubstring(refs[0]))

		commit := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", refs[0]))
		Expect(gitOutputStr(workDir, "show", commit+":tasks/x.md")).
			To(ContainSubstring("LOCAL UNCOMMITTED EDIT"))
		Expect(gitOutputStr(workDir, "show", commit+":tasks/scratch.md")).
			To(ContainSubstring("scratch"))
		// The staged deletion is recorded as a tree-vs-parent diff; the path is
		// ABSENT from the rescue tree.
		Expect(gitOutputStr(workDir, "diff", "--name-status", commit+"^", commit)).
			To(ContainSubstring("D\ttasks/doomed.md"))
		Expect(gitOutputStr(workDir, "ls-tree", "-r", "--name-only", commit)).
			NotTo(ContainSubstring("tasks/doomed.md"))
		Expect(gitOutputStr(workDir, "ls-tree", "-r", "--name-only", commit)).
			NotTo(ContainSubstring("secrets.env"))
	})

	It("AC2: rescues an untracked-only collision (git stash create would lose it)", func() {
		// Advance the remote so the incoming commit ADDS tasks/scratch.md, then
		// leave only an untracked file at that path.
		extDir, err := os.MkdirTemp("", "git-ext-untracked-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = os.RemoveAll(extDir) }()
		runGit(extDir, "clone", remoteDir, ".")
		runGit(extDir, "config", "user.email", "ext@example.com")
		runGit(extDir, "config", "user.name", "External")
		Expect(os.WriteFile(
			filepath.Join(extDir, "tasks", "scratch.md"), []byte("remote scratch\n"), 0o600,
		)).To(Succeed())
		runGit(extDir, "add", "-A")
		runGit(extDir, "commit", "-m", "external: adds scratch")
		runGit(extDir, "push", "origin")

		Expect(os.WriteFile(
			filepath.Join(workDir, "tasks", "scratch.md"), []byte("local untracked\n"), 0o600,
		)).To(Succeed())
		Expect(strings.TrimSpace(gitOutputStr(workDir, "status", "--porcelain"))).
			To(Equal("?? tasks/scratch.md"))

		Expect(pg.Pull(ctx)).To(Succeed())

		refs := strings.Fields(rescueRefs())
		Expect(refs).To(HaveLen(1))
		commit := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", refs[0]))
		Expect(gitOutputStr(workDir, "show", commit+":tasks/scratch.md")).
			To(ContainSubstring("local untracked"))
		Expect(strings.TrimSpace(gitOutputStr(workDir, "status", "--porcelain"))).To(BeEmpty())
	})

	It("logs one INFO line naming the rescue branch and the captured paths", func() {
		advanceRemote()
		dirtyTree()

		logs, restore := captureSlogLogs()
		defer restore()

		Expect(pg.Pull(ctx)).To(Succeed())

		logStr := logs.String()
		Expect(strings.Count(logStr, "rescue branch pushed")).To(Equal(1))
		Expect(logStr).To(ContainSubstring("files=3"))
		Expect(logStr).To(ContainSubstring("paths=tasks/doomed.md,tasks/scratch.md,tasks/x.md"))
	})

	It("AC5: a rejected rescue push resets nothing and leaves the rescue ref for retry", func() {
		advanceRemote()
		dirtyTree()
		headBefore := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "HEAD"))

		// A pre-receive hook in the bare remote rejects rescue/* refs. The bare
		// repo must point at its own hooks directory: a global core.hooksPath (as
		// set on some machines) makes git ignore the per-repo hooks/ directory,
		// the hook silently no-ops, and this spec would false-fail.
		hooksDir := filepath.Join(remoteDir, "hooks")
		Expect(os.MkdirAll(hooksDir, 0o750)).To(Succeed())
		hook := "#!/bin/sh\n" +
			"while read old new ref; do\n" +
			"  case \"$ref\" in refs/heads/rescue/*) echo \"rejected by test hook\" >&2; exit 1;; esac\n" +
			"done\n" +
			"exit 0\n"
		Expect(os.WriteFile(filepath.Join(hooksDir, "pre-receive"), []byte(hook), 0o755)).
			To(Succeed())
		runGit(remoteDir, "config", "core.hooksPath", "hooks")

		logs, restore := captureSlogLogs()
		defer restore()

		err := pg.Pull(ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("rescue push failed"))
		Expect(logs.String()).To(ContainSubstring("rescue push failed, leaving repo for inspection"))

		// Nothing was reset and nothing was cleaned: HEAD unmoved, the edit still
		// in the working tree, and the REAL index still holds the staged deletion
		// (proving the temporary index never leaked into it).
		Expect(strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "HEAD"))).To(Equal(headBefore))
		Expect(gitOutputStr(workDir, "status", "--porcelain")).To(ContainSubstring(" M tasks/x.md"))
		Expect(gitOutputStr(workDir, "diff", "--cached", "--name-only", "HEAD")).
			To(ContainSubstring("tasks/doomed.md"))

		// The local rescue ref exists so the push can be retried by hand; the
		// remote has no rescue branch.
		Expect(rescueRefs()).NotTo(BeEmpty())
		Expect(remoteRescueRefs()).To(BeEmpty())
	})
})
```

## 10. CHANGELOG

Create the `## Unreleased` section directly under the SemVer preamble and above the existing `## v0.26.0` heading:

```markdown
## Unreleased

- fix: Recover automatically from a dirty working tree that blocks a fast-forward pull. When `git merge --ff-only` fails because uncommitted changes (staged, unstaged, deletions or untracked files) would be overwritten, the puller now captures the whole working-tree state as an object-only commit on a `rescue/<timestamp>` branch, pushes it to the upstream remote, and only after that push succeeds returns the working tree to the upstream state (`git reset --hard` + `git clean -fd`). A rejected rescue push resets nothing — HEAD stays put, the tree stays dirty, and the local rescue branch remains for a manual retry — so no change is ever discarded. `.gitignore`d paths are excluded by construction. This narrows spec 006's `git reset --hard` ban to this one path, where the content is already safe on the remote. Fixes the 2026-09-24 `vault-obsidian-agent-0` incident, where one wedged puller stalled the agent-task-executor reconcile loop and no Seibert-Data PR got a bot review.
```

## 11. Final verification

Run every command in `<verification>` from the repo root and confirm each result before finishing. Then walk each requirement above against the change: the amendment is in 006, the temp index path does not pre-exist, `git stash create` appears nowhere, reset/clean are unreachable without a successful push, the re-evaluation is `syncWithUpstream` (not `Pull`), and the timestamp layout is the basic form.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT add any Prometheus metric or any `Metrics` interface method in this prompt — prompt 2 owns `git_rest_pull_rescues_total`, its interface method, the regenerated `mocks/metrics.go`, and the `g.metrics.IncPullRescue()` call site inside `rescueDirtyTree`.
- Do NOT modify `runCmd`, `runCmdOutput` or `runCmdRaw`. The new `childEnv`, `runCmdEnv` and `runCmdOutputEnv` sit beside them.
- Do NOT use `git stash create` — it captures tracked changes only and returns an empty SHA for an untracked-only tree.
- Do NOT create the temporary index with a path that already exists (`mktemp` creates a zero-byte file and git rejects it with `index file smaller than expected`). Use `os.MkdirTemp` + `filepath.Join(dir, "index")`, or a non-existent path.
- Do NOT call `g.Pull(ctx)` for the re-evaluation — `Pull` holds `g.mu` for its whole body and `sync.Mutex` is not reentrant, so a recursive call deadlocks. Re-evaluate via `g.syncWithUpstream(ctx, upstream)`.
- `git reset --hard` and `git clean -fd` MUST be unreachable unless the rescue push returned success in the same call path. This is the amendment's safety condition.
- `git reset --hard` MUST target the upstream tracking ref derived from `@{u}` (the `upstream` value already resolved in `Pull`), never a hardcoded `master`/`main`. The remote name for the push MUST be derived from `upstream`, never hardcoded to `origin`.
- The rescue branch name MUST be `rescue/<ISO-8601-basic-UTC>` built as `g.currentDateTimeGetter.Now().UTC().Format("20060102T150405Z")`. Never `time.Now()`. The extended ISO-8601 form is not a legal git ref name.
- The rescue branch MUST NOT be force-pushed and MUST NOT be deleted. One branch per rescue event.
- The rescue commit MUST NOT move HEAD and MUST NOT touch the real index. The real index and the working tree are byte-for-byte unchanged until the push has succeeded.
- `git rebase --abort` remains banned everywhere, exactly as spec 006 states.
- Do NOT change spec 012/013's conflict path: `resolveConflictMerge`, `resolveConflictPaths`, `quarantineOne`, `validateConflictPathsSafe`, `validateConflictPathsNotNested`, `ensureConflictsDir`, the `ConflictResolver` interface, `pkg/git/conflict_resolver.go`, `pkg/git/yaml_merge_resolver.go` and `mocks/conflict_resolver.go` are frozen.
- Do NOT touch `pullMergeAndPush` / `resolveConflictMerge`: the diverged-state dirty tree (`localSHA != baseSHA`) is a known remaining gap deliberately excluded by the spec.
- Do NOT change the public HTTP contract in `docs/api.md` — no new endpoint, no changed status code, no changed response shape, no change to readiness semantics.
- Do NOT touch `main.go`'s boot path (`recoverUntracked`, `syncOnStartup`, `cleanupStaleLocks`) — the spec relies on it being unchanged.
- Do NOT touch `docs/verifying-specs.md`, `docs/deployment.md` or `CLAUDE.md` — prompts 3 and 4 own those.
- Errors MUST be wrapped with `errors.Wrap` / `errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf`, never a bare `return err`.
- Logging MUST use `log/slog` (`slog.InfoContext`, `slog.WarnContext`, `slog.ErrorContext`). Never `context.Background()` in `pkg/`.
- The INFO line MUST begin `rescue branch pushed` and MUST name the captured paths, not only a count. The ERROR line MUST be exactly `rescue push failed, leaving repo for inspection`.
- `newly added` code MUST reach >=80% statement coverage; the new specs in requirement 9 plus the untouched-path regressions in prompt 3 are the coverage vehicle for this prompt's code.
- `make precommit` from the repo root MUST exit 0.
- Linter limits that apply to the new code: `funlen` 80 lines / 50 statements, `nestif` complexity 4, `gocognit` 20, `golines` max-len 100. Keep `Pull` small by delegating to `syncWithUpstream`, `fastForwardOrRescue` and `rescueDirtyTree`; keep the nesting shallow (no `if` nested inside `if` nested inside `if` inside a `switch` case).
- Existing tests must still pass, including every spec in the `Pull state machine`, `Entry-state recovery`, `Pull nested quarantine guard` and `Quarantine backlog gauge` Describe blocks. The only edits to `pkg/git/git_test.go` are the new `setupRescueFixture`, the new `Dirty working tree rescue` Describe, and any imports those need.
</constraints>

<verification>
Run from the repo root.

```bash
make precommit
```

Must exit 0.

Iterative runs while implementing (use these after each meaningful change, before the final `make precommit`):

```bash
make test
go test ./pkg/git/... -ginkgo.focus='Dirty working tree rescue'
go test ./pkg/git/... -ginkgo.focus='Pull state machine'
go test ./pkg/git/... -ginkgo.focus='Entry-state recovery'
```

All pass.

Coverage for the changed package:

```bash
go test -coverprofile=/tmp/cover.out ./pkg/git/... && go tool cover -func=/tmp/cover.out
```

The new functions (`workingTreeDirty`, `createRescueCommit`, `rescueDirtyTree`, `syncWithUpstream`, `fastForwardOrRescue`, `childEnv`, `runCmdEnv`, `runCmdOutputEnv`) must each report a non-zero coverage percentage, and the package total must not fall below its pre-change value.

Amendment check — the amended bullet is present and the old blanket ban is gone:

```bash
grep -n 'Amended by spec 014' specs/completed/006-bug-pull-cannot-recover-from-dirty-working-tree.md
grep -n 'git rebase --abort` is NEVER invoked automatically' specs/completed/006-bug-pull-cannot-recover-from-divergence.md 2>/dev/null || true
grep -c 'NEVER invoked automatically' specs/completed/006-bug-pull-cannot-recover-from-divergence.md
```

The first prints a match; the third must print `1` (the surviving `git rebase --abort` ban only — the old combined sentence is replaced by two bullets, exactly one of which keeps the phrase).

Mechanism checks (each must print at least one match):

```bash
grep -n 'workingTreeDirty' pkg/git/git.go
grep -n 'createRescueCommit' pkg/git/git.go
grep -n 'rescueDirtyTree' pkg/git/git.go
grep -n 'syncWithUpstream' pkg/git/git.go
grep -n 'fastForwardOrRescue' pkg/git/git.go
grep -n 'GIT_INDEX_FILE' pkg/git/git.go
grep -n '20060102T150405Z' pkg/git/git.go
grep -n 'rescue branch pushed' pkg/git/git.go
grep -n 'rescue push failed, leaving repo for inspection' pkg/git/git.go
grep -n 'childEnv\|runCmdEnv\|runCmdOutputEnv' pkg/git/git.go
```

Banned-construction checks — each must print NOTHING (absence assertions, so `! grep -q`):

```bash
! grep -q 'stash create' pkg/git/git.go
! grep -q 'mktemp' pkg/git/git.go
! grep -vE '^\s*//' pkg/git/git.go | grep -q 'g.Pull(ctx)'
```

After stripping comment lines, `g.Pull(ctx)` must not appear as a call anywhere in `pkg/git/git.go`: the only re-evaluation is `g.syncWithUpstream(ctx, upstream)`. Comment lines are excluded on purpose — the `syncWithUpstream` doc comment is required to carry the deadlock warning, which names `g.Pull(ctx)`.

Ordering check — `reset --hard` and `clean` appear only inside `rescueDirtyTree`, after the push:

```bash
grep -vE '^\s*//' pkg/git/git.go | grep -n 'reset --hard\|"clean", "-fd"'
```

After stripping comment lines, every match must be a call site inside `rescueDirtyTree`, after the `slog.InfoContext(... "rescue branch pushed" ...)` call. Comment lines are excluded on purpose — the function's own doc comment names both commands.

CHANGELOG checks:

```bash
grep -c '^## Unreleased' CHANGELOG.md
grep -n 'rescue/' CHANGELOG.md
```

The first must print `1`; the second must print at least one match inside `## Unreleased`.

Self-check before finishing: re-run `make precommit`, then walk each requirement above against the change — the detection is a `git status --porcelain` read taken BEFORE the merge, a failing `git status` still lets the merge run, the temp index path does not pre-exist, `git stash create` is absent, reset/clean are gated on the push result, the re-evaluation is not a recursive `Pull`, and the ref name uses the basic ISO-8601 layout.
</verification>
