---
status: approved
spec: [025-bug-empty-clone-never-recloned]
created: "2026-11-03T15:46:47Z"
queued: "2026-11-03T16:10:56Z"
branch: dark-factory/bug-empty-clone-never-recloned
---

# Surface an unresolvable HEAD from Pull as ErrRepoUnrecoverable

<summary>
- A pull against a repository whose HEAD cannot be resolved now returns an error that callers can identify as an unrecoverable repository state.
- The misleading "no upstream configured" error is no longer produced for that case; it stays in place for every other upstream-resolution failure.
- The existing behaviour of a healthy repository, a detached HEAD that recovery can heal, and every pull state-machine branch is unchanged.
- The change is confined to the pull entry point; no clone, rescue, quarantine or conflict-resolution code is touched.
- A regression test removes the repository's HEAD file and asserts the returned error matches the existing unrecoverable-repository sentinel.
- The changelog records the corrected error surface.
</summary>

<objective>
Make `Pull` return an error wrapping `ErrRepoUnrecoverable` when the repository's HEAD cannot be resolved, so the one place that can explain a wedged repository names the real cause instead of surfacing an untyped upstream error that points at the wrong code path.
</objective>

<context>
Read `docs/dod.md` for the project's Definition of Done, then read these files before changing anything:

- `pkg/git/git.go` — the whole `Pull` method and the `recoverRepoState` method directly below it. `Pull` calls `recoverRepoState`, then resolves upstream with `git rev-parse --abbrev-ref --symbolic-full-name @{u}` and wraps that failure as `"no upstream configured"`. `ErrRepoUnrecoverable` is declared near the top of the file.
- `pkg/git/git_test.go` — `Describe("Entry-state recovery")`, especially the `Context("AC3 + AC4d: detached HEAD, refs/remotes/origin/HEAD absent")` block, which is the pattern for asserting `errors.Is(pullErr, git.ErrRepoUnrecoverable)`. Also read `setupPullFixture` and `captureSlogLogs`.
- `specs/in-progress/025-bug-empty-clone-never-recloned.md` — `## Desired Behavior` items 6 and 7, `## Acceptance Criteria` item 4, `## Assumptions` (no caller branches on `ErrRepoUnrecoverable` today), and `## Constraints` (DB6 intentionally changes what `Pull` returns for one class of failure).

Coding guides (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors` only, never `fmt.Errorf`; `errors.Wrap(ctx, err, message)`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 / Gomega conventions, external test package `git_test`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` — not applicable here, but contexts are passed in, never created with `context.Background()` in non-test `pkg/` code.
</context>

<requirements>

## 1. Add the HEAD check to `Pull` (`pkg/git/git.go`)

1.1 In `func (g *git) Pull(ctx context.Context) error`, insert the check between the `recoverRepoState` block and the upstream lookup. The existing code is:

```go
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

Insert this block immediately after the `recoverRepoState` block and before `upstreamOut`:

```go
	// An unresolvable HEAD means there is no usable repository. Without this
	// check the @{u} lookup below reports "HEAD does not point to a branch",
	// which reads as a repository-state problem rather than the missing-clone
	// problem it is. Surface it as unrecoverable instead.
	if _, err := g.runCmdOutput(
		ctx, g.repoPath, "rev-parse", "--verify", "--quiet", "HEAD",
	); err != nil {
		g.metrics.IncGitOperationError("pull")
		return errors.Wrap(ctx, ErrRepoUnrecoverable, "HEAD cannot be resolved")
	}
```

1.2 Do not change the `@{u}` lookup or its `"no upstream configured"` wrap. It must keep reporting every other upstream-resolution failure exactly as it does today.

1.3 Do not change `recoverRepoState`'s behaviour, `syncWithUpstream`, the flush, the stale-lock cleanup, the `hasRemote` early return, or any other line of `Pull`. **One comment-only edit is expected:** inside `recoverRepoState`, the comment `// Cannot determine HEAD state — let Pull's @{u} resolution surface the error.` is now inaccurate — the new HEAD check in `Pull` surfaces it, not the `@{u}` lookup. Replace that comment with `// Cannot determine HEAD state — Pull's HEAD probe reports it as ErrRepoUnrecoverable.` Change no code in that function. A detached HEAD that resolves to a commit passes the new check, so the detached-HEAD recovery path is unaffected. A repository with no remote still returns nil before the check runs.

1.4 Do not add a new sentinel, a new metric label, or a new exported function. `ErrRepoUnrecoverable` already exists and `errors.Wrap(ctx, ErrRepoUnrecoverable, ...)` is the idiom used by `recoverRepoState`.

## 2. Regression test (`pkg/git/git_test.go`)

2.1 Add a new `Context` inside the existing `Describe("Entry-state recovery")` block, after the `Context("AC3 + AC4d: detached HEAD, refs/remotes/origin/HEAD absent")` block. It reuses the block's `ctx` variable set by `BeforeEach`:

```go
Context("unresolvable HEAD", func() {
	It("returns ErrRepoUnrecoverable instead of an untyped upstream error", func() {
		workDir, _, cleanup := setupPullFixture()
		defer cleanup()

		// A repository whose HEAD cannot be resolved has no usable HEAD ref.
		Expect(os.Remove(filepath.Join(workDir, ".git", "HEAD"))).To(Succeed())

		pg := git.New(
			workDir,
			&noopMetrics{},
			libtime.NewCurrentDateTime(),
			"",
			git.NewMarkerResolver(workDir),
		)
		pullErr := pg.Pull(ctx)

		Expect(pullErr).To(HaveOccurred())
		Expect(errors.Is(pullErr, git.ErrRepoUnrecoverable)).To(BeTrue(),
			"expected ErrRepoUnrecoverable, got: %v", pullErr)
		Expect(pullErr.Error()).NotTo(ContainSubstring("no upstream configured"))
	})
})
```

The file already imports `os`, `path/filepath`, `errors`, `libtime`, `git` and defines `noopMetrics`; do not add imports.

2.2 Do not modify any existing spec in that file. The detached-HEAD recovery cases and the `AC3 + AC4d` `ErrRepoUnrecoverable` case must keep passing unchanged.

## 3. Changelog (`CHANGELOG.md`)

3.1 Ensure a `## Unreleased` section exists directly after the SemVer preamble and before `## v0.40.2`. If a sibling prompt already created it, append to that section; never create a second `## Unreleased`. Add exactly this bullet:

```
- fix: Return `ErrRepoUnrecoverable` from `Pull` when the repository HEAD cannot be resolved, instead of a misleading `no upstream configured` error
```

Do not add any other changelog bullet.

## 4. Self-check before finishing

4.1 Confirm the new return site is the only place that mentions `"HEAD cannot be resolved"` and that the existing `"no upstream configured"` wrap still exists — see `<verification>`.

4.2 Walk AC4 in the spec against the change: the test removes `.git/HEAD`, calls `Pull`, asserts `err != nil` and `errors.Is(err, ErrRepoUnrecoverable)`.
</requirements>

<constraints>
- The pull state machine (no-op / fast-forward / push / merge), the dirty-tree rescue, the quarantine paths and the conflict resolver must not change behaviour.
- `Pull` must never return an untyped upstream error on a path where the repository's HEAD could not be resolved; it returns an error wrapping `ErrRepoUnrecoverable`.
- No new sentinel, configuration field, flag or metric label.
- Errors are wrapped with `github.com/bborbe/errors`, never `fmt.Errorf`.
- Non-test `pkg/git` code must not call `context.Background()`; contexts are passed in.
- Tests are Ginkgo v2 / Gomega, external test package `git_test`.
- Precommit budgets: funlen 80, gocognit 20, nestif 4, golines 100.
- `.dark-factory.yaml` sets `workflow: direct`, `pr: false`, `autoRelease: false`: commit to the branch directly; no PR or auto-release is expected.
- Do NOT commit — dark-factory handles git.
- Do not modify `main.go` or `export_test.go` in this prompt; the startup re-clone is a separate prompt.
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

Then confirm the observable outcomes (each line must print `ok`):

```
test "$(grep -c 'ErrRepoUnrecoverable, "HEAD cannot be resolved"' pkg/git/git.go)" = "1" && echo ok
test "$(grep -c 'no upstream configured' pkg/git/git.go)" = "1" && echo ok
test "$(grep -c 'ErrRepoUnrecoverable instead of an untyped upstream error' pkg/git/git_test.go)" = "1" && echo ok
test "$(grep -c '^## Unreleased' CHANGELOG.md)" = "1" && echo ok
! grep -q 'fmt.Errorf' pkg/git/git.go && echo ok
```
</verification>
