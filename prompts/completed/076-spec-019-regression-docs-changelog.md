---
status: completed
spec: [019-bug-dirty-tree-rescue-all-pull-paths]
execution_id: git-rest-dirty-tree-rescue-exec-076-spec-019-regression-docs-changelog
dark-factory-version: v0.198.7
created: "2026-10-22T07:10:42Z"
queued: "2026-10-22T07:32:06Z"
started: "2026-10-22T07:43:17Z"
completed: "2026-10-22T08:37:51Z"
branch: dark-factory/bug-dirty-tree-rescue-all-pull-paths
---

# Lock in the push-retry rescue, amend spec 014, update docs and changelog

<summary>
- The push-retry merge path, which no reproduction recipe can force deterministically, is covered by a unit test that proves a dirty tree is rescued there
- A clean tree on the push-retry path still merges without creating a rescue branch, so the new check does not disturb the normal path
- A rejected rescue push on the diverged path still resets nothing: HEAD stays put, the tree stays dirty, and the local rescue ref remains for a manual retry
- Spec 014's "dirty tree in the diverged state" failure-mode row is closed and now points at this spec, with the outdated readiness claims removed
- The deployment documentation explains that the rescue runs on all four sync paths and that the reset target is path-dependent
- The deployment documentation explains the per-cycle stale-lock cleanup, its liveness probe and its log line
- The changelog records both fixes under an Unreleased heading
- The four-path rescue reachability is covered collectively: fast-forward by the existing spec 014 suite, diverged and startup by prompt 1, push-retry by this prompt
- No operator-only verification is part of this prompt; the post-deploy check stays on the spec
</summary>

<objective>
Close the loop on spec 019: add the regression tests that lock in the push-retry rescue and the safety gate on the diverged path, amend spec 014's now-stale failure-mode row, and document the four-path rescue and the per-cycle lock cleanup in `docs/deployment.md` with a changelog entry. This prompt depends on prompts 1 and 2 having landed.
</objective>

<context>
Read `README.md` at the repo root for project conventions, and `docs/dod.md` (the daemon's `validationPrompt`) for the definition of done.

Read these coding-plugin guides before implementing (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, external test packages, real `git` against a temp working tree
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` heading, `fix:` prefix, one bullet per logical change
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` — coverage and verification rules

Files to read in full before editing (anchor by function name):

- `pkg/git/git.go` — `retryPush`, `mergeWithDirtyTreeRescue`, `rescueDirtyTree` (all changed by prompt 1; read the landed versions before writing tests)
- `pkg/git/export_test.go` — the test-only export pattern (`NewGitWithClaimOrigin`, `NewYAMLMergeResolverWithSideTimes`)
- `pkg/git/git_test.go` — `setupRescueFixture`, `runGit`, `gitOutputStr`, `gatherPullRescues`, `captureSlogLogs`, and the existing `Dirty working tree rescue` and `Dirty tree rescue on the diverged path` Describes
- `docs/deployment.md` — the `### Dirty working tree rescue` section and the `git_rest_pull_rescues_total` metric row
- `specs/in-progress/014-bug-pull-cannot-recover-from-dirty-working-tree.md` — its `## Failure Modes` table
- `CHANGELOG.md` — the top heading is `## v0.33.1`; no `## Unreleased` section exists yet
- `specs/in-progress/019-bug-dirty-tree-rescue-all-pull-paths.md` — `## Desired Behavior` items 3 and 5, `## Constraints`, `## Acceptance Criteria` AC 8

**State after prompts 1 and 2 (verify by reading, do not assume):**

- `rescueDirtyTree(ctx context.Context, upstream, resetRef string) error`; the fast-forward path passes `upstream`, the diverged and push-retry paths pass `"HEAD"`.
- `mergeWithDirtyTreeRescue(ctx, upstream) ([]byte, error, error)` exists and is used by `pullMergeAndPush` and `retryPush`.
- `retryPush(ctx, upstream string, nextAttempt int) error` is still unexported.
- `Pull` calls `cleanupStaleLocksForCycle(ctx)` after the no-remote early return.
- `syncOnStartup(ctx, g git.Git)` is void and delegates to `Pull`.

**Sibling prompts (already done — do not redo):**

- Prompt 1 wired the rescue into `pullMergeAndPush`, `retryPush` and the startup sync, and added the diverged-path test plus the startup-rescue test.
- Prompt 2 added the per-cycle stale-lock cleanup and its tests.

**Four-path coverage map (do not duplicate these suites):**

- Fast-forward: the existing `Dirty working tree rescue` Describe (spec 014).
- Diverged: the `Dirty tree rescue on the diverged path` Describe (prompt 1).
- Startup: the `SyncOnStartup` Describe's dirty-and-diverged Context (prompt 1).
- Push retry: added by this prompt.

<!-- Requirement 4 (the spec 014 row amendment) applies ONLY when spec 014 no longer carries `status: verifying`. If it still does, SKIP requirement 4, leave every other requirement intact, and report requirement 4 as the single unmet item — the row amendment is re-issued by the spec owner once 014's verification completes. Do not wait, and do not edit a spec mid-verification. The file is located by grep rather than a hardcoded path so it works whether 014 is still in specs/in-progress/ or has moved to specs/completed/. -->
</context>

<requirements>

## 1. Export a test-only entry point for `retryPush`

Add to `pkg/git/export_test.go`:

```go
// RetryPushForTest exposes retryPush so external tests can drive the
// push-retry merge path directly. Forcing a real non-fast-forward rejection
// deterministically is a race, so the path is exercised here instead.
func RetryPushForTest(ctx context.Context, g Git, upstream string, nextAttempt int) error {
	impl, ok := g.(*git)
	if !ok {
		panic("RetryPushForTest: Git is not a *git")
	}
	return impl.retryPush(ctx, upstream, nextAttempt)
}
```

`context` is already imported in that file. Do not export `retryPush` on the `Git` interface and do not regenerate mocks.

## 2. Add the push-retry rescue tests

Append a new top-level Describe to `pkg/git/git_test.go`:

```go
var _ = Describe("Dirty tree rescue on the push-retry path", func() {
	var (
		workDir       string
		advanceRemote func()
		rescueCleanup func()
		pg            git.Git
		ctx           context.Context
	)

	upstreamRef := func() string {
		return strings.TrimSpace(gitOutputStr(
			workDir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}",
		))
	}
	rescueRefs := func() string {
		return strings.TrimSpace(gitOutputStr(
			workDir, "for-each-ref", "--format=%(refname)", "refs/heads/rescue/",
		))
	}
	commitLocalWork := func() {
		runGit(workDir, "config", "user.email", "test@example.com")
		runGit(workDir, "config", "user.name", "Test User")
		Expect(os.WriteFile(
			filepath.Join(workDir, "tasks", "z.md"), []byte("local only\n"), 0o600,
		)).To(Succeed())
		runGit(workDir, "add", "-A")
		runGit(workDir, "commit", "-m", "local work")
	}

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

	It("rescues the dirty tree, then merges, keeping the local commit", func() {
		commitLocalWork()
		advanceRemote()
		Expect(os.WriteFile(
			filepath.Join(workDir, "tasks", "x.md"),
			[]byte("line one\nLOCAL UNCOMMITTED EDIT\n"),
			0o600,
		)).To(Succeed())

		before := gatherPullRescues()
		Expect(git.RetryPushForTest(pg, ctx, upstreamRef(), 2)).To(Succeed())

		Expect(strings.TrimSpace(gitOutputStr(workDir, "status", "--porcelain"))).To(BeEmpty())
		Expect(gitOutputStr(workDir, "log", "--oneline")).To(ContainSubstring("local work"))
		Expect(gatherPullRescues() - before).To(Equal(1.0))

		refs := strings.Fields(rescueRefs())
		Expect(refs).To(HaveLen(1))
		commit := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", refs[0]))
		Expect(gitOutputStr(workDir, "show", commit+":tasks/x.md")).
			To(ContainSubstring("LOCAL UNCOMMITTED EDIT"))
	})

	It("AC7: a clean tree on the retry path merges without a rescue", func() {
		commitLocalWork()
		advanceRemote()

		before := gatherPullRescues()
		Expect(git.RetryPushForTest(pg, ctx, upstreamRef(), 2)).To(Succeed())

		Expect(strings.TrimSpace(gitOutputStr(workDir, "status", "--porcelain"))).To(BeEmpty())
		Expect(gitOutputStr(workDir, "log", "--oneline")).To(ContainSubstring("local work"))
		Expect(rescueRefs()).To(BeEmpty())
		Expect(gatherPullRescues() - before).To(Equal(0.0))
	})
})
```

`remoteDir` is needed only for the rejected-push test below. Prompt 1's `Dirty tree rescue on the diverged path` Describe deliberately binds the fixture's fourth return to `_` (it does not read it), so **do not** add this test there — add it in its own Describe that owns the variable.

## 3. Add the diverged-path safety-gate test

Add a **new** Describe (not a new `It` inside prompt 1's) so it owns `remoteDir`:

```go
var _ = Describe("Dirty tree rescue on the diverged path — rejected rescue push", func() {
	var (
		workDir       string
		advanceRemote func()
		remoteDir     string
		rescueCleanup func()
		pg            git.Git
		ctx           context.Context
	)

	BeforeEach(func() {
		ctx = context.Background()
		workDir, advanceRemote, _, remoteDir, rescueCleanup = setupRescueFixture()
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

	It("a rejected rescue push resets nothing on the diverged path", func() {
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

	headBefore := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "HEAD"))

	// A pre-receive hook in the bare remote rejects rescue/* refs. Point the bare
	// repo at its own hooks directory: a global core.hooksPath would make git
	// ignore the per-repo hooks/ directory and this test would false-pass.
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

	err := pg.Pull(ctx)
	Expect(err).To(HaveOccurred())
	Expect(err.Error()).To(ContainSubstring("rescue push failed"))

	// Nothing was reset and nothing was cleaned: HEAD unmoved, the edit still in
	// the working tree, and the local rescue ref present for a manual retry.
	Expect(strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "HEAD"))).To(Equal(headBefore))
	Expect(gitOutputStr(workDir, "status", "--porcelain")).To(ContainSubstring(" M tasks/x.md"))
	Expect(gitOutputStr(workDir, "log", "--oneline")).To(ContainSubstring("local work"))
	Expect(gitOutputStr(
		workDir, "ls-remote", "--heads", "origin", "refs/heads/rescue/*",
	)).To(BeEmpty())
	// The local rescue ref is the manual-retry path the docs promise.
	Expect(strings.Fields(strings.TrimSpace(gitOutputStr(
		workDir, "for-each-ref", "--format=%(refname)", "refs/heads/rescue/",
	)))).To(HaveLen(1))
	})
})
```

This Describe owns `remoteDir` (the hook lives in the bare origin) and reads it, so the variable is used. Prompt 1's Describe binds the same fourth return to `_` — do not merge the two, or one of them gets an unused variable, which is a compile error in Go.

## 4. Amend spec 014's failure-mode row

Locate the file (do not assume it is still in `specs/in-progress/`):

```bash
grep -rln 'Dirty tree in the \*\*diverged\*\* state' specs/
```

Expected: `specs/in-progress/014-bug-pull-cannot-recover-from-dirty-working-tree.md` (or `specs/completed/...` if 014 has since been archived). Edit only the row whose first cell begins `Dirty tree in the **diverged** state`. Replace the whole row:

```
| Dirty tree in the **diverged** state (`localSHA != baseSHA`) | **Not covered by this spec — a known remaining gap of the same class.** `pullMergeAndPush` → `git merge --no-edit` (`git.go:696`) aborts on the same dirty-tree message, `parseMergeConflictPaths` finds no conflict, and readiness stays not-ready | Operator clears the tree by hand; extending the rescue to this path is a follow-up spec | Existing `git pull failed` warn; readiness stays not-ready | Reversible |
```

with:

```
| Dirty tree in the **diverged** state (`localSHA != baseSHA`) | **Closed by spec 019 (`019-bug-dirty-tree-rescue-all-pull-paths.md`).** `pullMergeAndPush` now inspects the working tree before `git merge --no-edit`, rescues it to `rescue/<ts>`, resets to `HEAD` so the local commits survive, and retries the merge inline | None needed — self-healed | `rescue branch pushed` INFO; `git_rest_pull_rescues_total` increments | Reversible — content on `rescue/<ts>` |
```

Do not change any other row of 014's Failure Modes table, and do not change any other section of 014.

## 5. Update `docs/deployment.md`

Two named places, plus one stale factual claim inside the same section.

**(a) The `### Dirty working tree rescue` Trigger paragraph.** Replace:

```
**Trigger.** The served repo's working tree holds uncommitted changes to tracked files while the remote has moved. `git merge --ff-only` refuses rather than clobbering the working tree, so the pull aborts. Before this rescue existed, that wedged the pod indefinitely — readiness reported not-ready on every cycle, and every consumer of the vault (including the agent-task-executor reconcile loop) stalled behind it. It now self-heals within one pull interval.
```

with:

```
**Trigger.** The served repo's working tree holds uncommitted changes to tracked files while the remote has moved. Whichever sync path runs — fast-forward, the diverged merge, the push retry, or the startup sync — git refuses to clobber the working tree, so the sync aborts. Before this rescue existed, that wedged the pod indefinitely: the repository stopped advancing and consumers saw stale content until an operator restarted the pod. Readiness is decoupled from sync outcome, so the pod kept serving while its content went stale. It now self-heals within one pull interval.
```

**(b) The "What the puller does" paragraph.** Replace:

```
**What the puller does.** On that failure it captures the whole working-tree state — staged, unstaged, deletions and untracked alike — as a commit on a `rescue/<timestamp>` branch (`rescue/20060102T150405Z`, ISO-8601 basic UTC), pushes that branch to the vault's own remote, and only after the push succeeds returns the working tree to the remote state (`git reset --hard` on the upstream tracking ref, then `git clean -fd`). `.gitignore`d paths are excluded by construction, so secrets and keys never leave the pod. The rescue branch is never force-pushed and never deleted.
```

with:

```
**What the puller does.** On that failure it captures the whole working-tree state — staged, unstaged, deletions and untracked alike — as a commit on a `rescue/<timestamp>` branch (`rescue/20060102T150405Z`, ISO-8601 basic UTC), pushes that branch to the vault's own remote, and only after the push succeeds returns the working tree to a clean state. `git reset --hard` targets the upstream tracking ref on the fast-forward path, where HEAD holds no unique commit; on the diverged, push-retry and startup paths it targets `HEAD`, so the local commits those paths are about to merge survive. `git clean -fd` then removes untracked residue. `.gitignore`d paths are excluded by construction, so secrets and keys never leave the pod. The rescue branch is never force-pushed and never deleted.
```

**(c) Add a stale-lock paragraph** at the end of the same section, immediately after the "A rejected rescue push" paragraph and before "Observing the rescue":

```
**Stale locks.** Before each pull cycle the puller removes stale `*.lock` files under `.git/` — every lock file, not only `index.lock` — but only when no live process holds an open file descriptor on it. The probe uses `lsof` on macOS and `/proc/<pid>/fd` on Linux; if neither can decide, the lock is left in place and a warning is logged. A removal logs `stale lock removed during pull cycle` with the path. The boot-time cleanup is unchanged and remains unconditional.
```

Leave the metric table and the `git_rest_pull_rescues_total` row unchanged: the counter's contract did not change.

## 6. Add the changelog entry

Create `## Unreleased` directly under the SemVer preamble and above `## v0.33.1`:

```markdown
## Unreleased

- fix: Rescue a dirty working tree on every pull path, not only fast-forward. A dirty tree that blocks the diverged merge, the push-retry merge or the startup sync is now captured on a `rescue/<timestamp>` branch and pushed before the merge is retried, exactly as on the fast-forward path. The reset target is path-dependent: the upstream tracking ref on fast-forward, `HEAD` on the other three paths, so the local commits those paths are about to merge survive.
- fix: Clear stale `.git/*.lock` files before every pull cycle, not only at boot. A lock is removed only when no live process holds an open file descriptor on it (`lsof` on macOS, `/proc/<pid>/fd` on Linux; fail closed when neither can decide), so a concurrent git process is never corrupted. Removal logs `stale lock removed during pull cycle` with the path. Fixes the 2026-10-22 dev incident where a post-boot `index.lock` defeated the rescue and wedged the pod for ~35 minutes.
```

## 7. Self-check

Before finishing, re-run `<verification>` and walk each requirement:

- The push-retry rescue test proves a rescue happened on `retryPush`'s merge path (rescue ref, counter delta 1, local commit kept).
- The clean-retry test proves no rescue branch and no counter increment on the normal path.
- The rejected-push test proves HEAD and the working tree are untouched and no remote rescue ref exists.
- The 014 row is closed, points at 019, and no longer claims readiness stays not-ready.
- `docs/deployment.md` names all four paths and both reset targets; the metric table is unchanged.
- `CHANGELOG.md` has exactly one `## Unreleased` heading with two `fix:` bullets.
- No operator-only command (kubectl, docker, dark-factory, gh) appears in this prompt's verification or in the tests.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT add a scenario file. No scenario exists for this behaviour and the four-condition scenario test is not met: the behaviour is reachable by unit/integration tests against real git.
- Do NOT implement AC 8 (the Post-Deploy rung-2 check). It is operator verification during spec verification, not a prompt deliverable. Do NOT add kubectl, docker, dark-factory, or `gh` commands anywhere.
- Do NOT change the rescue's capture contract, `rescueDirtyTree`'s push/reset ordering, the counter contract, or the `Metrics` interface.
- Do NOT change the public HTTP contract in `docs/api.md` or readiness semantics.
- Do NOT add duplicate fast-forward, diverged or startup rescue suites. The four-path coverage map in `<context>` is authoritative; this prompt adds only the push-retry and safety-gate tests.
- Do NOT change any other row of spec 014's Failure Modes table or any other section of 014.
- Do NOT change the metric table in `docs/deployment.md`; `git_rest_pull_rescues_total` is unchanged.
- Errors MUST be wrapped with `errors.Wrap` / `errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf`.
- Logging MUST use `log/slog`; never `context.Background()` in production code under `pkg/`. Test files build a root context with `context.Background()` exactly as the existing suites in `pkg/git/git_test.go` do.
- Tests MUST use real `git` through `os/exec` against temp repositories, as the existing suite does; never mock git.
- New test code MUST reach >=80% statement coverage of the code it exercises; `retryPush`'s rescue branch is the specific gap this prompt closes.
- Existing tests must still pass, including `Dirty working tree rescue`, `Dirty tree rescue on the diverged path`, `Per-cycle stale lock cleanup`, and `SyncOnStartup`.
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
go test ./pkg/git/... -ginkgo.focus='Dirty tree rescue on the push-retry path'
go test ./pkg/git/... -ginkgo.focus='Dirty tree rescue on the diverged path'
go test ./pkg/git/... -ginkgo.focus='Per-cycle stale lock cleanup'
```

All pass.

Coverage for the changed package:

```bash
go test -coverprofile=/tmp/cover.out ./pkg/git/... && go tool cover -func=/tmp/cover.out
```

`retryPush` and `mergeWithDirtyTreeRescue` must each report a non-zero coverage percentage, and the package total must not fall below its pre-change value.

Documentation checks:

```bash
grep -c '^## Unreleased' CHANGELOG.md
grep -n 'diverged, push-retry and startup paths' docs/deployment.md
grep -n 'stale lock removed during pull cycle' docs/deployment.md
grep -rn 'Closed by spec 019' specs/
```

The first must print `1`; the others must each print at least one match.

Negative checks — each must print NOTHING:

```bash
! grep -n 'Not covered by this spec' "$(grep -rl 'Dirty tree in the \*\*diverged\*\* state' specs/)"
```

The second applies only while 014 is still at that path; if 014 has moved to `specs/completed/`, run the same check against the moved file.

Self-check before finishing: re-run `make precommit`, then walk requirement 7 against the change.
</verification>
