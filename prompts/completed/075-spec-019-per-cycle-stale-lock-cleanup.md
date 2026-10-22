---
status: completed
spec: [019-bug-dirty-tree-rescue-all-pull-paths]
summary: Added per-pull-cycle stale lock cleanup with fd-liveness probes (lsof + /proc) that fails closed, plus tests and an export seam
execution_id: git-rest-dirty-tree-rescue-exec-075-spec-019-per-cycle-stale-lock-cleanup
dark-factory-version: v0.198.7
created: "2026-10-22T07:10:42Z"
queued: "2026-10-22T07:32:06Z"
started: "2026-10-22T07:37:59Z"
completed: "2026-10-22T07:43:15Z"
branch: dark-factory/bug-dirty-tree-rescue-all-pull-paths
---

# Remove stale git locks before every pull cycle, with a liveness check

<summary>
- A stale `.git/*.lock` file created after boot no longer wedges the puller until the next restart
- Stale locks are cleared before every pull cycle, not only once at process start
- The cleanup walks every `*.lock` under `.git/`, matching the boot-time cleanup's scope — not only `index.lock`
- A lock held by a live process is left in place, so a concurrent git process is never corrupted
- Liveness is decided by an open file descriptor on the lock path — `lsof` on macOS, `/proc/<pid>/fd` on Linux
- When neither probe can decide, the lock is left in place and a warning is logged; there is no unconditional `rm` fallback
- Removal logs one distinguishable INFO line beginning `stale lock removed during pull cycle`, naming the path
- A clean tree at lock-removal time does not count as a rescue, so the rescue counter stays unchanged
- The boot-time cleanup is untouched and remains unconditional, because a lock present at boot can only belong to a process that no longer exists
- No new metric and no `Metrics` interface change
</summary>

<objective>
Run stale-lock cleanup before every pull/rescue cycle, not only during bootstrap, and decide staleness by process liveness rather than assumption. A lock is removed only when no live process holds an open descriptor on it; when liveness cannot be determined the lock is left alone. This closes the 2026-10-22 dev incident where a post-boot `index.lock` defeated the rescue's reset step and wedged the pod for ~35 minutes.
</objective>

<context>
Read `README.md` and `.golangci.yml` at the repo root for project conventions (this repo has no `CLAUDE.md`).

Read these coding-plugin guides before implementing (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `errors.Wrap` / `errors.Wrapf` from `github.com/bborbe/errors`; never `fmt.Errorf`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md` — `log/slog` with `slog.InfoContext` / `slog.WarnContext`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, external test packages, real `git` against a temp working tree
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-security-linting.md` — `#nosec` needs a reason; file permissions `0600` / `0750`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` — coverage and verification rules

Files to read in full before editing (anchor by function name):

- `pkg/git/git.go` — `Pull` (the cycle entry point), `hasRemote`, `recoverRepoState`, `runCmdOutput`; the existing import block (it already imports `io/fs`, `log/slog`, `os`, `os/exec`, `path/filepath`, `strconv`, `strings`, `stderrors "errors"`)
- `main.go` — `cleanupStaleLocks` (the boot-time walk this cycle-time walk must mirror in scope; do NOT change it)
- `pkg/git/git_test.go` — `setupPullFixture`, `externalPush`, `gitOutputStr`, `captureSlogLogs`
- `specs/in-progress/019-bug-dirty-tree-rescue-all-pull-paths.md` — `## Desired Behavior` item 4, `## Constraints`, `## Failure Modes`

**Verified facts about the current code:**

- `Pull` acquires `g.mu` with `defer g.mu.Unlock()`, registers `defer g.refreshQuarantinedBacklog(ctx)`, early-returns when `!g.hasRemote(ctx)`, then calls `g.recoverRepoState(ctx)` and resolves `upstream` from `@{u}`.
- `main.go`'s `cleanupStaleLocks(ctx, repoDir)` walks `filepath.Join(repoDir, ".git")` with `filepath.WalkDir`, skips directories, matches `strings.HasSuffix(path, ".lock")`, and removes unconditionally (boot-time only, `#nosec G122`).
- `git status --porcelain` exits 0 with empty output while `.git/index.lock` exists (verified in spec 014), so a live lock does not make the tree read as dirty; the merge is what fails.
- `lsof -t <path>` prints one pid per holder and exits 1 when no process holds the file. `lsof` exists on macOS and is absent from the `alpine:3.23` image. `/proc/<pid>/fd/<n>` exists on Linux and not on macOS.
- Git creates `*.lock` files with `O_CREAT|O_EXCL` and never takes an OS lock (`flock`/`fcntl`) on them, so an OS lock probe cannot see a live git process.
- `pkg/git/git.go` already imports `stderrors "errors"`; use it for `stderrors.As`.

**Sibling prompts (do not do their work):**

- Prompt 1 adds the dirty-tree rescue to `pullMergeAndPush`, `retryPush` and the startup sync, and changes `rescueDirtyTree`'s signature. Do NOT touch those functions, `main.go`, or `docs/`.
- Prompt 3 adds regression tests for the rescue paths, the spec 014 amendment, `docs/deployment.md`, and `CHANGELOG.md`. Do NOT touch those here.

<!-- OPEN QUESTION for the reviewer: prompts 1 and 2 both edit pkg/git/git.go and pkg/git/git_test.go. This prompt touches Pull (one insertion), new lock helpers, and a new test Describe; prompt 1 touches pullMergeAndPush / retryPush / rescueDirtyTree / main.go. Approve them in sequence if the runner does not serialise prompt execution. -->
</context>

<requirements>

## 1. Add the per-cycle cleanup to `Pull`

In `pkg/git/git.go`, in `Pull`, immediately after the `!g.hasRemote(ctx)` early return and before `g.recoverRepoState(ctx)`, insert:

```go
	// Clear locks left by a process that no longer exists before any git command
	// needs the index. A lock held by a live process is left alone.
	if err := g.cleanupStaleLocksForCycle(ctx); err != nil {
		slog.WarnContext(ctx, "stale lock cleanup failed", "error", err)
	}
```

Do NOT change `Pull`'s mutex, its `refreshQuarantinedBacklog` defer, its `ObserveGitOperation` defer, its `hasRemote` early return, its `recoverRepoState` call, its `@{u}` resolution, or its `syncWithUpstream` delegation. Do NOT change `main.go`'s `cleanupStaleLocks`.

## 2. Add the cycle-time walk

Add to `pkg/git/git.go`:

```go
// cleanupStaleLocksForCycle removes stale *.lock files under .git before a pull
// cycle. A lock is removed only when no live process holds an open file
// descriptor on it. When liveness cannot be determined the lock is left in place
// and a warning is logged — never fall back to an unconditional remove, which
// could delete the lock of a concurrent git process.
//
// Scope matches the boot-time cleanupStaleLocks in main.go: every *.lock under
// .git, not only index.lock. The boot cleanup stays unconditional because a lock
// present at boot can only belong to a process that no longer exists.
func (g *git) cleanupStaleLocksForCycle(ctx context.Context) error {
	gitDir := filepath.Join(g.repoPath, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return errors.Wrapf(ctx, err, "stat %s", gitDir)
	}
	return filepath.WalkDir(gitDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			slog.WarnContext(
				ctx, "walk error during per-cycle lock cleanup", "path", path, "error", walkErr,
			)
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(path, ".lock") {
			return nil
		}
		held, determined := lockHeldByProcess(ctx, path)
		if !determined {
			slog.WarnContext(
				ctx, "stale lock liveness undetermined; leaving lock in place", "path", path,
			)
			return nil
		}
		if held {
			return nil
		}
		if rmErr := os.Remove(path); rmErr != nil { // #nosec G122 -- path is under the repo's own .git and the liveness probe already cleared it
			if !os.IsNotExist(rmErr) {
				slog.WarnContext(
					ctx, "failed to remove stale lock during pull cycle", "path", path, "error", rmErr,
				)
			}
			return nil
		}
		slog.InfoContext(ctx, "stale lock removed during pull cycle", "path", path)
		return nil
	})
}
```

Properties that must hold exactly:

- The walk is best-effort: an individual walk or remove error is logged and the walk continues. Only a failure to stat `.git/` returns an error, which `Pull` warn-logs and ignores.
- A missing `.git/` is a no-op (pre-init / pre-clone).
- Only non-directory entries ending in `.lock` are considered.
- The removal INFO message begins exactly `stale lock removed during pull cycle` and carries `path`. This prefix is deliberately distinct from the boot line `removed stale lock`.
- Never call `os.Remove` without a `determined && !held` result.
- The `os.Remove` inside `filepath.WalkDir` carries `// #nosec G122` with a stated reason, exactly as `main.go:132` does — gosec G122 fires on remove-inside-walk, and `make precommit` fails without it.
- **The fail-closed branch must be tested.** Split the walk from the probe so a test can drive the undetermined case without package-level mutable state: `func (g *git) cleanupStaleLocksForCycle(ctx context.Context) error { return g.cleanupStaleLocks(ctx, lockHeldByProcess) }` and `func (g *git) cleanupStaleLocks(ctx context.Context, probe func(context.Context, string) (bool, bool)) error`. Add an `export_test.go` seam (that file already uses this pattern) and a test that injects `probe = func(...) (bool, bool) { return false, false }`, then asserts the lock file is still present, zero `stale lock removed during pull cycle` lines, and one `liveness undetermined` WARN.

## 3. Add the liveness probe

Add to `pkg/git/git.go`:

```go
// lockHeldByProcess reports whether any live process holds an open file
// descriptor on path, and whether that could be determined. The probe is
// deliberately a file-descriptor check: git creates *.lock files with
// O_CREAT|O_EXCL and never takes an OS lock on them, so flock/fcntl cannot see a
// live git process. darwin uses lsof; linux reads /proc/<pid>/fd and compares
// file identity with os.SameFile. If neither probe is available the result is
// (false, false) — the caller must leave the lock alone.
func lockHeldByProcess(ctx context.Context, path string) (bool, bool) {
	if held, determined := lockHeldByLsof(ctx, path); determined {
		return held, true
	}
	return lockHeldByProc(ctx, path)
}

// lockHeldByLsof probes with lsof, which exists on macOS and not in the alpine
// image. lsof exits 1 when no process has the file open.
func lockHeldByLsof(ctx context.Context, path string) (bool, bool) {
	if _, err := exec.LookPath("lsof"); err != nil {
		return false, false
	}
	// #nosec G204 -- binary is hardcoded to "lsof"; path is an internal repo path
	out, err := exec.CommandContext(ctx, "lsof", "-t", path).Output()
	if err == nil {
		return strings.TrimSpace(string(out)) != "", true
	}
	var exitErr *exec.ExitError
	if stderrors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, true
	}
	return false, false
}

// lockHeldByProc probes /proc/<pid>/fd, which exists on linux and not on macOS.
// An fd directory that cannot be read is skipped; the pod runs a single uid, so
// same-uid holders are visible. readable==0 means /proc could not be inspected
// and the result is undetermined.
func lockHeldByProc(ctx context.Context, path string) (bool, bool) {
	lockInfo, err := os.Stat(path)
	if err != nil {
		// The lock vanished; there is nothing to protect.
		return false, os.IsNotExist(err)
	}
	procEntries, err := os.ReadDir("/proc")
	if err != nil {
		return false, false
	}
	readable := 0
	for _, entry := range procEntries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		fdDir := filepath.Join("/proc", entry.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		readable++
		for _, fd := range fds {
			fdInfo, err := os.Stat(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			if os.SameFile(lockInfo, fdInfo) {
				return true, true
			}
		}
		if ctx.Err() != nil {
			return false, false
		}
	}
	if readable == 0 {
		return false, false
	}
	return false, true
}
```

Properties that must hold exactly:

- `flock` / `fcntl` MUST NOT be used. They are the wrong signal: git never takes an OS lock on `*.lock`.
- `lsof` is tried first so macOS works; `/proc` is the fallback for the alpine pod.
- An unavailable or erroring probe yields `determined == false`, and the caller leaves the lock in place. There is no unconditional `rm` fallback anywhere.
- A cancelled context during the `/proc` walk yields undetermined (leave the lock).
- `os.SameFile` compares device + inode, so a deleted-and-recreated file at the same path is not a false match.

## 4. Add the per-cycle cleanup tests

Append a new top-level Describe to `pkg/git/git_test.go` (do not modify existing Describes):

```go
var _ = Describe("Per-cycle stale lock cleanup", func() {
	var (
		workDir      string
		externalPush func(file, content string)
		pullCleanup  func()
		pg           git.Git
		ctx          context.Context
	)

	BeforeEach(func() {
		ctx = context.Background()
		workDir, externalPush, pullCleanup = setupPullFixture()
		pg = git.New(
			workDir,
			metrics.NewMetrics(),
			libtime.NewCurrentDateTime(),
			"",
			git.NewMarkerResolver(workDir),
		)
	})

	AfterEach(func() {
		pullCleanup()
	})

	It("AC4: removes a lock created after boot and completes the fast-forward", func() {
		externalPush("remote.txt", "remote\n")
		Expect(os.WriteFile(
			filepath.Join(workDir, ".git", "index.lock"), nil, 0o600,
		)).To(Succeed())
		logs, restore := captureSlogLogs()
		defer restore()

		Expect(pg.Pull(ctx)).To(Succeed())

		Expect(filepath.Join(workDir, ".git", "index.lock")).NotTo(BeAnExistingFile())
		Expect(strings.TrimSpace(gitOutputStr(workDir, "status", "--porcelain"))).To(BeEmpty())
		Expect(strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "HEAD"))).
			To(Equal(strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "@{u}"))))
		Expect(strings.Count(logs.String(), "stale lock removed during pull cycle")).To(Equal(1))
	})

	It("AC5: leaves a lock held by a live process in place", func() {
		externalPush("remote.txt", "remote\n")
		// A pure file-descriptor holder: this test process holds the file open.
		// Do NOT use flock/fcntl, and do NOT use a git command as the holder —
		// git exits immediately when the lock already exists.
		holder, err := os.OpenFile(
			filepath.Join(workDir, ".git", "index.lock"), os.O_CREATE|os.O_RDWR, 0o600,
		)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = holder.Close() }()

		logs, restore := captureSlogLogs()
		defer restore()

		// The held lock blocks the cycle; the assertion is that it survives.
		Expect(pg.Pull(ctx)).To(HaveOccurred())

		Expect(filepath.Join(workDir, ".git", "index.lock")).To(BeAnExistingFile())
		Expect(logs.String()).NotTo(ContainSubstring("stale lock removed during pull cycle"))
	})

	It("AC6: covers every *.lock under .git and touches no non-lock file", func() {
		externalPush("remote.txt", "remote\n")
		Expect(os.WriteFile(
			filepath.Join(workDir, ".git", "HEAD.lock"), nil, 0o600,
		)).To(Succeed())

		Expect(pg.Pull(ctx)).To(Succeed())

		Expect(filepath.Join(workDir, ".git", "HEAD.lock")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(workDir, ".git", "index")).To(BeAnExistingFile())
		Expect(strings.TrimSpace(gitOutputStr(workDir, "status", "--porcelain"))).To(BeEmpty())
	})
})
```

Notes for the test author:

- `git_test.go` is `package git_test` and already imports `os`, `path/filepath`, `strings`, `libtime`, `metrics`, and `git`.
- `setupPullFixture()` returns `(workDir, externalPush, cleanup)`; its initial commit is empty, so `externalPush` creates the fast-forward work.
- The live-holder test holds an fd in the test process itself. Do not replace it with a sleeping shell process or a git command.

## 5. Self-check

Before finishing, re-run the `<verification>` commands and walk each requirement:

- `cleanupStaleLocksForCycle` is called once per `Pull`, after the no-remote early return and before `recoverRepoState`.
- The walk scope is every `*.lock` under `.git/`, directories skipped.
- No `os.Remove` runs when `determined` is false or `held` is true.
- Neither `flock` nor `fcntl` appears.
- The INFO message begins `stale lock removed during pull cycle`; the boot line `removed stale lock` in `main.go` is untouched.
- No metric was added; `metrics.Metrics` and `mocks/metrics.go` are unchanged.
- `main.go` and `docs/` are untouched.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT use `flock` or `fcntl` as the liveness signal. Git creates `*.lock` with `O_CREAT|O_EXCL` and never takes an OS lock on it, so those probes cannot see a live git process.
- Do NOT delete a lock unconditionally, and do NOT add an unconditional-`rm` fallback when the probe fails. When liveness cannot be determined, log and leave the lock in place (fail closed).
- Implement BOTH probes: `lsof` (darwin) and `/proc/<pid>/fd` inode matching (linux). The Rung-1 host is macOS and the Rung-2 pod is `alpine:3.23`; neither tool exists on both.
- The removal INFO line MUST begin exactly `stale lock removed during pull cycle` and MUST name the path. It must be distinguishable from the boot-time `removed stale lock` line.
- The walk scope MUST be every `*.lock` under `.git/`, matching `main.go`'s `cleanupStaleLocks` — not only `index.lock`.
- Do NOT change `main.go`'s `cleanupStaleLocks` or its unconditional boot-time semantics.
- Do NOT add a Prometheus metric and do NOT change the `Metrics` interface or `mocks/metrics.go`.
- Do NOT touch `pullMergeAndPush`, `retryPush`, `rescueDirtyTree`, `syncOnStartup`, `main.go`, `docs/`, `CHANGELOG.md`, or `specs/in-progress/014-*.md` — prompts 1 and 3 own those.
- Do NOT change the public HTTP contract in `docs/api.md` or readiness semantics.
- Errors MUST be wrapped with `errors.Wrap` / `errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf`.
- Logging MUST use `log/slog`; never `context.Background()` in `pkg/`.
- Any `exec.CommandContext` call needs a `#nosec G204` comment with the stated reason.
- New code MUST reach >=80% statement coverage; requirement 4 is the coverage vehicle.
- Existing tests must still pass, including the `Pull state machine` and `Dirty working tree rescue` Describes.
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
go test ./pkg/git/... -ginkgo.focus='Per-cycle stale lock cleanup'
go test ./pkg/git/... -ginkgo.focus='Pull state machine'
```

All pass.

Coverage for the changed package:

```bash
go test -coverprofile=/tmp/cover.out ./pkg/git/... && go tool cover -func=/tmp/cover.out
```

`cleanupStaleLocksForCycle`, `lockHeldByProcess`, `lockHeldByLsof` and `lockHeldByProc` must each report a non-zero coverage percentage, and the package total must not fall below its pre-change value.

Mechanism checks — each must print at least one match:

```bash
grep -n 'cleanupStaleLocksForCycle' pkg/git/git.go
grep -n 'stale lock removed during pull cycle' pkg/git/git.go
grep -n 'lsof' pkg/git/git.go
grep -n '/proc' pkg/git/git.go
grep -n 'os.SameFile' pkg/git/git.go
```

Banned-construction checks — each must print NOTHING:

```bash
! grep -vE '^\s*//' pkg/git/git.go | grep -qE 'flock|fcntl'
! grep -n '"syscall"' pkg/git/git.go
```

Self-check before finishing: re-run `make precommit`, then walk requirement 5 against the change.
</verification>
