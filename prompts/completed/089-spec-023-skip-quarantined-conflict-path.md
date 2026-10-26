---
status: completed
spec: [023-bug-quarantined-path-aborts-whole-merge]
summary: Replaced the nesting pre-flight abort with a per-path skip that keeps the local side of an already-quarantined conflicted path, inverting the three live specs and adding the AC4/AC5 fixtures, docs and CHANGELOG entry.
execution_id: git-rest-exec-089-spec-023-skip-quarantined-conflict-path
dark-factory-version: v0.198.7
created: "2026-10-26T11:10:20Z"
queued: "2026-10-26T12:09:15Z"
started: "2026-10-26T12:09:17Z"
completed: "2026-10-26T12:15:03Z"
branch: dark-factory/bug-quarantined-path-aborts-whole-merge
---

# Resolve a conflicted `_conflicts/` path in place instead of aborting the merge

<summary>
- A conflicted path that already lives under `_conflicts/` is now resolved in place by keeping the local version, instead of aborting the whole merge.
- The no-deeper-nesting guarantee survives: the skip happens before any quarantine attempt, so `_conflicts/_conflicts/` can still never be created.
- A pull that hits this shape completes: the merge commits, pushes, and names the path in the `resolved=[…]` half of the commit message.
- `nested_source` still counts one event per skipped path, so the metric keeps meaning "an already-quarantined path was encountered".
- The aborted-merge counter no longer moves for this shape — the failure it used to record is gone.
- Modify/delete conflicts under `_conflicts/` keep their existing behaviour; the `ours`-deleted arm, which the old guard used to pre-empt, now actually runs.
- First-time conflicts are unchanged: the resolver runs first and a resolver failure still quarantines to exactly one `_conflicts/` level.
- The three live specs that asserted the abort are inverted in place, and two new fixtures cover first-time quarantine and the `ours`-deleted arm.
- The operator-facing documentation and both superseded specs now describe the fixed behaviour, so a verifier does not read the old abort as the contract.
</summary>

<objective>
Turn the nesting pre-flight that aborts a merge on an already-quarantined conflicted path into a per-path skip that keeps the local side, so one quarantined file can never again stop a vault replica from pulling, while the one-level `_conflicts/` contract stays enforced at the quarantine write.
</objective>

<context>
Read `CLAUDE.md` for project conventions, then read these files before changing anything:

- `pkg/git/git.go` — the whole quarantine/conflict region: `quarantineDestPath`, `nestedConflictPath`, `isUnderConflictsDir`, `dropAcceptedDrains`, `validateConflictPathsNotNested`, `classifyConflicts`, `conflictKind` and its three constants, `stageUpstreamVersion`, `acceptUpstreamDeletion`, `resolveModifyDelete`, `resolveConflictMerge`, `resolveConflictPaths`, `validateConflictPathsSafe`, `resolveEachPath`, `commitAndPushMerge`, `ensureConflictsDir`, `quarantineOne`. Note the existing idiom for a two-step git resolution in `stageUpstreamVersion`: `g.runCmd(ctx, g.repoPath, "checkout", "--theirs", "--", path)` followed by `g.runCmd(ctx, g.repoPath, "add", "--", path)`. The new skip is the `--ours` twin of that function.
- `pkg/git/git_test.go` — the three specs to invert: `Describe("Pull nested quarantine guard")` at `:2085` (spec `:2096` `"AC2: Pull fails with ErrConflictResolutionFailed and leaves the file one level deep"`, and spec `:2188` `"AC3: the whole merge aborts, the worktree is restored and no commit is created"` inside `Context("a merge contains a nested path and an ordinary resolvable path")` at `:2182`), and `Context("a genuine re-quarantine")` at `:2916` with spec `:2917` `"AC8: is still refused by the nesting guard"` inside `Describe("Upstream drain of a quarantined path (spec 015)")` at `:2810`. Also read the helpers they use: `setupQuarantineFixturePaths` (`:1107`), `setupQuarantinePathFixture` (`:2722`), `gitOutputStr` (`:921`), `gatherQuarantinedFiles` (`:1025`), `hasResolverFailureSeries` (`:1061`), `gatherResolverFailure` (`:1083`), `captureSlogLogs` (`:1755`), `countFilesUnder` (`:2619`).
- `pkg/git/resolve_conflict_merge_test.go` — `TestNestedConflictPath` (`:497-527`), the only caller of `nestedConflictPath`.
- `pkg/metrics/metrics.go` — `MergeOutcomeTotal` (`git_rest_merge_outcome_total`, label `result`, pre-initialised for `clean`/`resolved`/`aborted` in `init()`), `ResolverFailuresTotal` (`git_rest_resolver_failures_total`, label `category`, pre-initialised including `nested_source`), `IncMergeOutcome`, `IncResolverFailure`.
- `docs/deployment.md` — the single bullet at line 215 that begins `- **Vault-write mode**`. The sentence to rewrite is `The merge aborts only when every conflicted path fails both resolve and quarantine, or when a pre-flight guard rejects the merge — an unsafe path, a conflicted path that already lives under \`_conflicts/\`, or \`_conflicts/\` existing as a regular file.`
- `docs/verifying-specs.md` — the `Fixture B` assertions ending at `:298` (keep untouched, including the `delta 0` line) and the `Fixture C — the shape that must stay refused:` block at `:301-313`.
- `CHANGELOG.md` — the newest heading is `## v0.36.2` at line 11; there is no `## Unreleased` section.
- `specs/in-progress/013-quarantine-nesting-and-drain.md` line 10 and `specs/in-progress/015-bug-pull-wedges-on-modify-delete-conflict.md` line 10 — both already carry a `> **Partly superseded** by ...` banner. Confirm they are there; do not write a second banner.
- `specs/in-progress/023-bug-quarantined-path-aborts-whole-merge.md` — this prompt's spec, especially `## Desired Behavior`, `## Non-goals`, `## Acceptance Criteria`, `## Failure Modes`.

Coding guides (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors` only, never `fmt.Errorf`, never `context.Background()` in non-test `pkg/` code.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 / Gomega suite conventions, external test package `git_test`, delta assertions against the process-global registry.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` — interface → constructor → struct, unexported helper conventions.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` — the loop already has a non-blocking `select`; keep it.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` placement and the required `- <prefix>: <what>` shape.
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — prose style for the two doc rewrites.
</context>

<requirements>
## 1. Replace the nesting pre-flight with a per-path skip (`pkg/git/git.go`)

1.1 Delete `validateConflictPathsNotNested` entirely (the function whose doc comment begins `// validateConflictPathsNotNested pre-flights the conflict path list before any disk`). Its `slog.WarnContext` message contains `aborting merge` and its `errors.Wrap(ctx, ErrConflictResolutionFailed, "nested conflict path already quarantined")` is the error the Rung-3 probe and the AC greps require to be gone. Keep the `quarantineFailureNested` constant at the top of the file — it is still used.

1.2 Delete `dropAcceptedDrains` (its only caller was `validateConflictPathsNotNested`) and delete `nestedConflictPath` (its only production caller was `validateConflictPathsNotNested`). **Keep `isUnderConflictsDir`** — it is still used by `resolveModifyDelete` and by the new skip.

1.3 Remove the call site in `resolveConflictPaths`:

```go
	// Pre-flight: a conflicted path that already lives under _conflicts/ aborts the
	// merge before any disk I/O, so a re-quarantine can never deepen the tree.
	if err := g.validateConflictPathsNotNested(ctx, conflictPaths, kinds); err != nil {
		return err
	}
```

Delete those four lines (comment included). Rewrite the surrounding comments so they no longer describe two pre-flights:

- The `resolveConflictPaths` doc comment currently says `both pre-flights — validateConflictPathsSafe and validateConflictPathsNotNested — MUST run BEFORE ensureConflictsDir`. Change it to state that `validateConflictPathsSafe` MUST run BEFORE `ensureConflictsDir`, so the unsafe-path abort never creates `_conflicts/` as a side effect, and that a path already under `_conflicts/` is handled per path inside `resolveEachPath`, not by a pre-flight.
- The comment above the `kinds := g.classifyConflicts(ctx)` call currently says the classification must run "before the nesting guard, because the guard needs the classification to tell an accepted operator drain ... from a genuine re-quarantine". Change it to: the classification must run after `validateConflictPathsSafe` and before `resolveEachPath`, because `resolveEachPath` dispatches modify/delete conflicts from it; it writes nothing, so the ordering invariant above is preserved.

1.4 Add a new unexported method immediately after `resolveModifyDelete` and before the `resolveConflictMerge` doc comment:

```go
// skipQuarantinedPath resolves a conflicted path that already lives under
// _conflicts/ by keeping the local side. `git checkout --ours -- <path>` writes
// HEAD's version into the working tree and `git add -- <path>` clears the
// unmerged index entry, so the merge can commit with the tree byte-identical to
// HEAD and no conflict markers staged. The path is never passed to
// quarantineOne, so the quarantine tree cannot gain a second "_conflicts/" level.
//
// The configured resolver is deliberately not tried: _conflicts/ is
// operator-owned, and a resolver that rewrote a quarantined file would mutate
// the artefact the operator is meant to inspect, silently, on every pull. The
// upstream edit stays reachable in the merge's second parent.
//
// Records the nested_source category exactly once per skipped path and does NOT
// record an aborted merge outcome. Returns false when either git command fails,
// leaving the index entry unmerged so a later commit refuses rather than
// silently dropping the conflict.
func (g *git) skipQuarantinedPath(ctx context.Context, path string) bool {
	if err := g.runCmd(ctx, g.repoPath, "checkout", "--ours", "--", path); err != nil {
		slog.WarnContext(
			ctx,
			"git-rest: keeping the local version of a conflicted path under _conflicts/ failed",
			"path",
			path,
			"err",
			err.Error(),
		)
		return false
	}
	if err := g.runCmd(ctx, g.repoPath, "add", "--", path); err != nil {
		slog.WarnContext(
			ctx,
			"git-rest: staging the kept local version of a conflicted path under _conflicts/ failed",
			"path",
			path,
			"err",
			err.Error(),
		)
		return false
	}
	g.metrics.IncResolverFailure(quarantineFailureNested)
	slog.WarnContext(
		ctx,
		"git-rest: nested conflicted path already under _conflicts/ skipped; keeping the local version",
		"path",
		path,
		"reason",
		"path already under _conflicts/; keeping the local version keeps the tree one level deep",
	)
	return true
}
```

Constraints on this helper: the WARN message must not contain the substring `aborting merge` (the Rung-3 probe greps for the absence of `already under _conflicts/ rejected`, and the old message is the one that must disappear); do not call `g.metrics.IncMergeOutcome` anywhere in it; do not increment `quarantineFailureNested` on either failure branch.

1.5 In `resolveEachPath`, insert the skip **after** the existing `resolveModifyDelete` dispatch and **before** the `g.resolver.Resolve` call:

```go
		if g.resolveModifyDelete(ctx, path, kinds[path]) {
			resolved = append(resolved, path)
			continue
		}
		// A path that already lives under _conflicts/ is operator-owned: keep the
		// local side instead of re-quarantining it (which would nest the tree) or
		// handing it to the configured resolver (which would silently rewrite the
		// artefact the operator inspects). The modify/delete dispatch above must run
		// first: an ours-deleted quarantined path has no stage 2, so `checkout
		// --ours` would fail on it, and resolveModifyDelete resolves it by taking
		// the upstream version instead.
		if isUnderConflictsDir(path) {
			if g.skipQuarantinedPath(ctx, path) {
				resolved = append(resolved, path)
			}
			continue
		}
		resolveErr := g.resolver.Resolve(ctx, []string{path})
```

Update the `resolveEachPath` doc comment to add: a path already under `_conflicts/` is skipped and kept local after the modify/delete dispatch, so it is never quarantined a second time and never reaches the configured resolver.

1.6 Do not touch `quarantineOne`, `quarantineDestPath`, `ensureConflictsDir`, `validateConflictPathsSafe`, `commitAndPushMerge`, `classifyConflicts`, `stageUpstreamVersion`, `acceptUpstreamDeletion`, or `resolveModifyDelete`. The one-level `quarantineDestPath` contract and its doc comment stay exactly as they are.

<!-- Deviation from spec 023 Failure Modes row 5, accepted by the prompt author: a failed skip adds the path to neither list, so when it is the *only* conflicted path the pre-existing "all conflicted paths failed resolve and quarantine" branch returns `ErrConflictResolutionFailed` and records the aborted outcome, instead of the row's wrapped commit error. With any sibling path resolving, `commitAndPushMerge` refuses the commit exactly as the row describes. The single-path arm is unreachable in practice — git reports no conflict for a path absent from the index. The spec owner should amend row 5 (expected error class and Detection metric) to match; the prompt keeps the simpler design rather than fabricating a resolved entry. -->

## 2. Rewrite the internal pure-function test (`pkg/git/resolve_conflict_merge_test.go`)

2.1 `nestedConflictPath` no longer exists, so `TestNestedConflictPath` (`:497-527`) must not survive. Replace it with a table test for the surviving predicate, preserving every boundary case:

```go
// TestIsUnderConflictsDir covers the prefix rule that decides whether a conflicted
// path is already quarantined. The "_conflicts-archive/a.md" case pins the
// boundary: only the exact "_conflicts" directory and its children count.
func TestIsUnderConflictsDir(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"ordinary path", "notes/a.md", false},
		{"sibling dir with the same prefix", "_conflicts-archive/a.md", false},
		{"nested path at the root", "_conflicts/a.md", true},
		{"nested path in a subdir", "_conflicts/25 Tasks/Prev A.md", true},
		{"bare _conflicts", "_conflicts", true},
	}
	for _, tc := range cases {
		if got := isUnderConflictsDir(tc.path); got != tc.want {
			t.Errorf("%s: isUnderConflictsDir(%q) = %v, want %v", tc.name, tc.path, got, tc.want)
		}
	}
}
```

2.2 Leave every other test in that file unchanged, including `TestQuarantineDestPath` and `TestQuarantineDestPathSingleConflictsSegment` (the one-level destination contract is still in force) and the unsafe-path / all-fail-abort specs, which exercise abort paths this change keeps.

2.3 Add a failure-path test for the new helper, next to `TestIsUnderConflictsDir`, mirroring `TestResolveConflictPathsUnsafePath`'s construction (`New(workDir, &unsafeTestMetrics{}, libtime.NewCurrentDateTime(), "", fakeResolver{}).(*git)` on a temp repo with no matching path). Add a `nestedCount()` helper beside `unsafePathCount()` that counts `incResolverFailureCalls == "nested_source"`, then assert:
- `repo.skipQuarantinedPath(ctx, "missing.md")` returns `false` (the `checkout --ours` arm fails);
- `metrics.nestedCount()` is `0` (a failed skip records no `nested_source` event);
- `len(metrics.incMergeOutcomeCalls)` is `0` (a failed skip records no merge outcome).

## 3. Invert the three live Ginkgo specs in place (`pkg/git/git_test.go`)

All three must be edited in place. Adding new specs that assert the new behaviour while any old one still asserts the abort leaves the suite self-contradictory and `make precommit` cannot pass.

3.1 Rename the `Describe` at `:2085` from `"Pull nested quarantine guard"` to `"Pull of an already-quarantined conflicted path (spec 023)"`. Rename the inner `Context("a conflicted path already under _conflicts/ is the only conflict")` only if you keep it as-is otherwise — its name is accurate and may stay.

3.2 Replace the spec at `:2096` (body `:2098-2178`) with the inverted assertion set. Title: `"AC1: Pull succeeds, keeps the local version and leaves the file one level deep"`. Required content, in this order:

- `workDir, localEdit, externalPush, cleanup := setupQuarantineFixturePaths([]string{nestedPath})` with `const nestedPath = "_conflicts/25 Tasks/Prev A.md"`, `defer cleanup()`.
- `externalPush(nestedPath, "---\ntitle: [unclosed\n---\nREMOTE CHANGE\n")` then `localEdit(nestedPath, "---\ntitle: a\n---\nLOCAL CHANGE\n")`.
- Capture `contentBefore` via `os.ReadFile(absNested)` and `statBefore` via `os.Stat(absNested)` immediately before the pull (keep the existing comment explaining that an earlier baseline compares against the seed bytes).
- `pg := git.New(workDir, metrics.NewMetrics(), libtime.NewCurrentDateTime(), "", git.NewYAMLMergeResolver(workDir, metrics.NewMetrics()))`.
- `logs, restore := captureSlogLogs()` with `defer restore()`.
- `Expect(hasResolverFailureSeries("nested_source")).To(BeTrue(), ...)` — the category must still be pre-initialised in `init()`.
- Read the three baselines: `beforeNested := gatherResolverFailure("nested_source")`, `beforeQuarantined := gatherQuarantinedFiles()`, `beforeAborted := gatherMergeOutcome("aborted")`.
- `Expect(pg.Pull(ctx)).To(Succeed())`.
- `Expect(gatherResolverFailure("nested_source")-beforeNested).To(Equal(1.0), "nested_source must increment by exactly 1 for a skipped path")`.
- `Expect(gatherMergeOutcome("aborted")-beforeAborted).To(Equal(0.0), "a skipped quarantined path must not record an aborted merge")`.
- `Expect(gatherQuarantinedFiles()-beforeQuarantined).To(Equal(0.0), "a skipped quarantined path must not record a quarantine event")`.
- `os.Stat(filepath.Join(workDir, "_conflicts", "_conflicts"))` satisfies `os.IsNotExist`.
- `os.Stat(filepath.Join(workDir, ".git", "MERGE_HEAD"))` satisfies `os.IsNotExist`.
- `strings.TrimSpace(gitOutputStr(workDir, "status", "--porcelain"))` is empty.
- `contentAfter` read again equals `contentBefore`; `statAfter.Mode()` equals `statBefore.Mode()`.
- `strings.TrimSpace(gitOutputStr(workDir, "log", "-1", "--format=%s"))` has prefix `merge: resolved=[_conflicts/25 Tasks/Prev A.md]`.
- Exactly one WARN names the path: `Expect(strings.Count(logs.String(), "already under _conflicts/ skipped")).To(Equal(1))`, `Expect(logs.String()).To(ContainSubstring(nestedPath))`, and `Expect(logs.String()).NotTo(ContainSubstring("aborting merge"))`.

3.3 Replace the spec at `:2188` (body `:2190-2225`) with title `"AC2: the merge completes, the ordinary path is quarantined and the nested path is kept local"`. Required content:

- Same fixture with both paths: `setupQuarantineFixturePaths([]string{nestedPath, ordinaryPath})` where `ordinaryPath = "notes/plain.md"`.
- `externalPush(nestedPath, "---\ntitle: [unclosed\n---\nREMOTE CHANGE\n")`.
- `externalPush(ordinaryPath, "---\nshared: [unclosed\n---\nremote body\n")` — **invalid** YAML on the remote side is what drives the ordinary path into quarantine; the previous fixture used valid frontmatter, which the YAML resolver deep-merges, so it never reached `_conflicts/`. Add a one-line comment saying so.
- `localEdit(nestedPath, "---\ntitle: a\n---\nLOCAL CHANGE\n")` and `localEdit(ordinaryPath, "---\nshared: local\n---\nlocal body\n")`.
- Capture the three baselines before the pull: `beforeNested := gatherResolverFailure("nested_source")`, `beforeQuarantined := gatherQuarantinedFiles()`, and `logs, restore := captureSlogLogs()` with `defer restore()`.
- `Expect(pg.Pull(ctx)).To(Succeed())`; then `Expect(gatherQuarantinedFiles()-beforeQuarantined).To(Equal(1.0), ...)` — exactly the ordinary path is quarantined — and `Expect(gatherResolverFailure("nested_source")-beforeNested).To(Equal(1.0), ...)` — exactly one skip in the same merge.
- `gitOutputStr(workDir, "status", "--porcelain")` trimmed is empty; `.git/MERGE_HEAD` satisfies `os.IsNotExist`.
- `Expect(strings.Count(logs.String(), "already under _conflicts/ skipped")).To(Equal(1))` and `Expect(logs.String()).To(ContainSubstring(nestedPath))` — one WARN naming the skipped path, while the ordinary path is quarantined.
- The ordinary path's quarantine file exists at exactly one `_conflicts/` level: `qGlob, _ := filepath.Glob(filepath.Join(workDir, "_conflicts", "notes", "plain.*.md"))`, `Expect(qGlob).To(HaveLen(1))`, and `Expect(strings.Count(filepath.ToSlash(qGlob[0]), "_conflicts/")).To(Equal(1))`.
- `os.Stat(filepath.Join(workDir, "_conflicts", "_conflicts"))` satisfies `os.IsNotExist`.
- `gitOutputStr(workDir, "log", "-1", "--format=%s")` trimmed equals `merge: resolved=[_conflicts/25 Tasks/Prev A.md] quarantined=[notes/plain.md]`.

3.4 Replace the spec at `:2917` (body `:2918-2972`) with title `"AC8: is resolved in place and keeps the local version"` and rename its `Context("a genuine re-quarantine")` to `Context("a content conflict under _conflicts/ with no upstream deletion")`. Keep the fixture `drainedPath = "_conflicts/25 Tasks/Prev A.1791388434.md"` and `setupQuarantinePathFixture(drainedPath)`; keep `remoteModify("---\ntitle: [unclosed\n---\nREMOTE CHANGE\n")` and `localEdit("---\ntitle: a\n---\nLOCAL CHANGE\n")`. Required content:

- `pg := git.New(workDir, metrics.NewMetrics(), libtime.NewCurrentDateTime(), "", git.NewYAMLMergeResolver(workDir, metrics.NewMetrics()))`.
- `logs, restore := captureSlogLogs()` with `defer restore()`; `Expect(hasResolverFailureSeries("nested_source")).To(BeTrue(), ...)`.
- `before := gatherResolverFailure("nested_source")`; capture `contentBefore`/`statBefore` for `filepath.Join(workDir, drainedPath)` before the pull.
- `Expect(pg.Pull(ctx)).To(Succeed())`.
- `Expect(gatherResolverFailure("nested_source")-before).To(Equal(1.0), ...)`.
- `_conflicts/_conflicts` satisfies `os.IsNotExist`; `.git/MERGE_HEAD` satisfies `os.IsNotExist`; `status --porcelain` trimmed is empty.
- `contentAfter` equals `contentBefore`; `statAfter.Mode()` equals `statBefore.Mode()`.
- `gitOutputStr(workDir, "log", "-1", "--format=%s")` trimmed has prefix `merge: resolved=[_conflicts/25 Tasks/Prev A.1791388434.md]`.
- `Expect(strings.Count(logs.String(), "already under _conflicts/ skipped")).To(Equal(1))` and the log contains `drainedPath`.

3.5 Do **not** touch the sibling spec `"AC8: does not trip the nesting guard"` at `:2892` — the drain's `nested_source` delta of 0 stays in force. Do not touch the `ErrConflictResolutionFailed` specs for `_conflicts/` existing as a regular file or for unsafe paths: those aborts remain.

3.6 In `setupQuarantineFixturePaths`, the seed-mode comment still says `merge --abort leaves it there — so a 0o600 seed makes the post-abort mode assertion in the AC2 spec fail`. Update that clause to say the mode assertion is now about `git checkout --ours`, which writes the index mode (100644) back into the working tree, so a 0o600 seed would still fail the assertion. Keep the `0o644` seed.

## 4. Add the two companion fixtures (AC4 and AC5)

Add a new `Describe` block immediately after the inverted `Describe("Pull of an already-quarantined conflicted path (spec 023)")` block (which ends before `Describe("Quarantine backlog gauge")`), with its own `var ctx context.Context` and `BeforeEach(func() { ctx = context.Background() })`, and the two specs below.

4.1 First-time quarantine still at depth 1 (this is the discriminator against an implementation that keeps the local version for *every* conflict):

```go
	It("AC4: a first-time conflicting path is still quarantined at depth 1", func() {
		const plainPath = "notes/plain.md"
		workDir, localEdit, externalPush, cleanup := setupQuarantineFixturePaths(
			[]string{plainPath},
		)
		defer cleanup()

		externalPush(plainPath, "---\nshared: [unclosed\n---\nremote body\n")
		localEdit(plainPath, "---\nshared: local\n---\nlocal body\n")

		pg := git.New(
			workDir,
			metrics.NewMetrics(),
			libtime.NewCurrentDateTime(),
			"",
			git.NewYAMLMergeResolver(workDir, metrics.NewMetrics()),
		)

		before := gatherQuarantinedFiles()
		Expect(pg.Pull(ctx)).To(Succeed())
		Expect(gatherQuarantinedFiles()-before).To(Equal(1.0),
			"a first-time conflicting path must still be quarantined")

		qGlob, _ := filepath.Glob(filepath.Join(workDir, "_conflicts", "notes", "plain.*.md"))
		Expect(qGlob).To(HaveLen(1),
			"the path must be quarantined at _conflicts/notes/plain.<ts>.md; got: %v", qGlob)
		Expect(strings.Count(filepath.ToSlash(qGlob[0]), "_conflicts/")).To(Equal(1),
			"the quarantine destination must carry exactly one _conflicts/ segment")

		_, statErr := os.Stat(filepath.Join(workDir, "_conflicts", "_conflicts"))
		Expect(os.IsNotExist(statErr)).To(BeTrue(),
			"_conflicts/_conflicts must not exist after a first-time quarantine")

		Expect(strings.TrimSpace(gitOutputStr(workDir, "log", "-1", "--format=%s"))).
			To(Equal("merge: resolved=[] quarantined=[notes/plain.md]"),
				"the first-time conflict must be quarantined, not kept local")
	})
```

4.2 The `ours`-deleted arm of a quarantined path. This needs a fixture with a local deletion, so add this helper next to `setupQuarantinePathFixture` (it mirrors that helper's seeding and `cloneRemote` shape, replacing `localEdit` with a `git rm` + commit):

```go
// setupQuarantineDeleteFixture seeds the given _conflicts/ path at the merge base
// on a local bare remote, then returns closures that advance the remote by
// modifying the path and delete it locally with `git rm` plus a commit — the
// ours-deleted modify/delete shape for an already-quarantined path.
func setupQuarantineDeleteFixture(
	path string,
) (
	workDir string,
	remoteModify func(content string),
	localDelete func(),
	cleanup func(),
) {
	remoteDir, err := os.MkdirTemp("", "git-remote-quardelete-*")
	Expect(err).NotTo(HaveOccurred())
	workDir, err = os.MkdirTemp("", "git-work-quardelete-*")
	Expect(err).NotTo(HaveOccurred())

	rg := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, e := cmd.CombinedOutput()
		Expect(e).NotTo(HaveOccurred(), "%s %v: %s", "git", args, string(out))
	}

	rg(remoteDir, "init", "--bare", "-b", "main")
	rg(workDir, "init", "-b", "main")
	rg(workDir, "config", "user.email", "test@example.com")
	rg(workDir, "config", "user.name", "Test")
	rg(workDir, "remote", "add", "origin", remoteDir)

	abs := filepath.Join(workDir, path)
	Expect(os.MkdirAll(filepath.Dir(abs), 0o750)).To(Succeed())
	Expect(os.WriteFile(abs, []byte("---\ntitle: a\n---\nbody\n"), 0o644)).To(Succeed())
	rg(workDir, "add", "--", path)
	rg(workDir, "commit", "-q", "-m", "seed")
	rg(workDir, "push", "-u", "origin", "main")

	remoteModify = func(content string) {
		extDir, extErr := os.MkdirTemp("", "git-ext-quardelete-*")
		Expect(extErr).NotTo(HaveOccurred())
		defer func() { _ = os.RemoveAll(extDir) }()
		rg(extDir, "clone", remoteDir, ".")
		rg(extDir, "config", "user.email", "ext@example.com")
		rg(extDir, "config", "user.name", "External")
		Expect(os.WriteFile(filepath.Join(extDir, path), []byte(content), 0o644)).To(Succeed())
		rg(extDir, "add", "--", path)
		rg(extDir, "commit", "-q", "-m", "external: "+path)
		rg(extDir, "push", "origin", "main")
	}

	localDelete = func() {
		rg(workDir, "rm", "-q", "--", path)
		rg(workDir, "commit", "-q", "-m", "local deletes "+path)
	}

	cleanup = func() {
		_ = os.RemoveAll(workDir)
		_ = os.RemoveAll(remoteDir)
	}
	return workDir, remoteModify, localDelete, cleanup
}
```

Then the spec:

```go
	It("AC5: an ours-deleted _conflicts/ path stages the upstream version", func() {
		const deletedPath = "_conflicts/25 Tasks/Prev A.md"
		workDir, remoteModify, localDelete, cleanup := setupQuarantineDeleteFixture(deletedPath)
		defer cleanup()

		remoteModify("---\ntitle: a\n---\nREMOTE CHANGE\n")
		localDelete()

		pg := git.New(
			workDir,
			metrics.NewMetrics(),
			libtime.NewCurrentDateTime(),
			"",
			git.NewMarkerResolver(workDir),
		)

		before := gatherResolverFailure("nested_source")
		Expect(pg.Pull(ctx)).To(Succeed())
		Expect(gatherResolverFailure("nested_source")-before).To(Equal(0.0),
			"an ours-deleted quarantined path must be dispatched to resolveModifyDelete "+
				"before the skip, so it never counts as nested_source")

		Expect(strings.TrimSpace(gitOutputStr(workDir, "status", "--porcelain"))).
			To(BeEmpty(), "the working tree must be clean after the resolved merge")
		Expect(gitOutputStr(workDir, "show", "HEAD:"+deletedPath)).
			To(ContainSubstring("REMOTE CHANGE"),
				"the upstream version must be staged and committed")
		Expect(strings.TrimSpace(gitOutputStr(workDir, "log", "-1", "--format=%s"))).
			To(Equal("merge: resolved=[_conflicts/25 Tasks/Prev A.md] quarantined=[]"))

		_, statErr := os.Stat(filepath.Join(workDir, "_conflicts", "_conflicts"))
		Expect(os.IsNotExist(statErr)).To(BeTrue(),
			"_conflicts/_conflicts must not exist after the ours-deleted arm resolves")
	})
```

## 5. Add the process-global merge-outcome gatherer (`pkg/git/git_test.go`)

Place it immediately after `gatherResolverFailure` (`:1083-1099`), mirroring that helper's shape exactly:

```go
// gatherMergeOutcome returns the current value of
// git_rest_merge_outcome_total{result=<result>} from the process-global registry.
// Returns 0 when the series is absent. Used to assert that a pull recorded no
// aborted outcome, as a delta around a single Pull, because sibling specs in the
// same binary also increment the counter.
func gatherMergeOutcome(result string) float64 {
	mfs, err := prometheus.DefaultGatherer.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, mf := range mfs {
		if mf.GetName() != "git_rest_merge_outcome_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "result" && l.GetValue() == result {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}
```

`prometheus` is already imported in that file.

## 6. Rewrite the abort sentence in `docs/deployment.md`

6.1 In the `- **Vault-write mode**` bullet at line 215, replace exactly this sentence:

```
The merge aborts only when every conflicted path fails both resolve and quarantine, or when a pre-flight guard rejects the merge — an unsafe path, a conflicted path that already lives under `_conflicts/`, or `_conflicts/` existing as a regular file.
```

with:

```
The merge aborts only when every conflicted path fails both resolve and quarantine, or when a pre-flight guard rejects the merge — an unsafe path, or `_conflicts/` existing as a regular file. A conflicted path that already lives under `_conflicts/` is skipped and kept local: the local version stays in the tree, `git_rest_resolver_failures_total{category="nested_source"}` increments once, and the merge commits, so one already-quarantined file can never wedge a replica.
```

6.2 Change nothing else in that bullet — in particular the `nested_source` entry in the metric-category list stays, and the quarantine description above it stays.

## 7. Invert Fixture C in `docs/verifying-specs.md`

Replace the whole block from `Fixture C — the shape that must stay refused:` through the closing `This is the shape the nesting guard exists for. ... second `_conflicts/` level.` paragraph (lines 301-313) with:

```markdown
Fixture C — the shape spec 023 fixed (an already-quarantined path is kept local):

Same fixture as B, except step 4 **modifies** the `_conflicts/` path on the remote with **invalid YAML** frontmatter (`---\ntitle: [unclosed\n---\nREMOTE CHANGE\n`) and step 5 modifies it locally with valid frontmatter (`---\ntitle: a\n---\nLOCAL CHANGE\n`) — a content conflict in which both sides changed the path and the resolver cannot merge the remote side. Run the puller with `-vault-write=true`, the deployed configuration for the personal and openclaw vaults (`values-dev.yaml` sets `writeMode: true`), which selects the YAML merge resolver.

Assertions:

- `/readiness` is `200`.
- `git_rest_resolver_failures_total{category="nested_source"}` increases by exactly 1 across the one pull: read it before and after in the same process lifetime — the counter is per-process. The path is skipped once, not rejected once per interval.
- `git_rest_merge_outcome_total{result="aborted"}` is unchanged across the pull.
- No path exists at `_conflicts/_conflicts`.
- `git -C "$BASE/work" status --porcelain` prints 0 lines — the merge committed, so the worktree is clean because the local version was staged into the merge, not because an abort restored the tree.
- The log carries a WARN naming the nested path, and no WARN containing `aborting merge`.

A recipe run that makes fixture C abort again — readiness non-200, `nested_source` climbing per interval, no merge commit — is a regression.
```

7.1 Do not touch Fixture B or its `delta 0` assertion, and do not touch any other part of the document (the Rung 2 recipe, the teardown note, the rung ladder).

## 8. Confirm the two supersession banners (`specs/in-progress/013-…`, `specs/in-progress/015-…`)

8.1 Both files already carry a `> **Partly superseded** by `specs/in-progress/023-bug-quarantined-path-aborts-whole-merge.md`` banner on line 10, and that path already resolves in both — this step is a no-op confirmation, not an edit. Do not add a second banner and do not rewrite its substance. Nothing else in those spec files changes — no status edits, no AC edits, no frontmatter edits.

## 9. Add the CHANGELOG entry

9.1 `CHANGELOG.md` has no `## Unreleased` section. Insert one immediately above `## v0.36.2` (currently line 11), with exactly one bullet:

```markdown
## Unreleased

- fix: Resolve a conflicted path that already lives under `_conflicts/` by keeping the local version instead of aborting the whole merge. The path is never re-quarantined, so `_conflicts/_conflicts/` still cannot be created; `git_rest_resolver_failures_total{category="nested_source"}` still increments once per skipped path, and `git_rest_merge_outcome_total{result="aborted"}` is no longer incremented for this shape. Supersedes spec 013's abort rule and spec 015's "a genuine re-quarantine still aborts" clause.
```

9.2 Do not edit any released section, and do not bump a version heading.

## 10. Self-check before finishing

10.1 `go build ./...` compiles (the removed helpers have no remaining callers).

10.2 Run the targeted suite and confirm it is green: `GOFLAGS=-buildvcs=false go test ./pkg/git/ -count=1`.

10.3 Confirm the three old spec titles are gone and the new ones exist, that the removed production strings are gone, and that the doc rewrites landed — see `<verification>` for the exact counts.
</requirements>

<constraints>
- Do NOT change the `ConflictResolver` interface, `MarkerResolver`, or `YAMLMergeResolver` internals.
- Do NOT change the `merge: resolved=[…] quarantined=[…]` commit-message format.
- Do NOT change `quarantineDestPath`'s one-level contract, or spec 013's backlog gauge and alerts.
- Do NOT auto-delete, auto-restore, or auto-repair quarantined content. Skipping a path resolves the merge; it does not clean the quarantine.
- Do NOT drain the existing `_conflicts/` residue in any vault from code. That stays a one-time operator action.
- Do NOT add a retry, backoff, or cap around the pull loop.
- Do NOT add a new Prometheus series, config field, CLI flag, or tunable. `nested_source` keeps its existing name and meaning; only its consequence changes.
- Do NOT hand a `_conflicts/` path to the configured resolver: `_conflicts/` is operator-owned and a resolver would silently rewrite the artefact.
- Do NOT move the skip ahead of the `resolveModifyDelete` dispatch in `resolveEachPath`. An `ours`-deleted quarantined path has no stage 2, so `checkout --ours` fails on it; the modify/delete dispatch is what resolves it.
- The guard and its helpers are unexported; pure-function cases live in the internal `package git` test file `pkg/git/resolve_conflict_merge_test.go`, and the merge-level specs live in `pkg/git/git_test.go`.
- All three existing specs must be edited in place. Do not leave an old spec asserting the abort while adding a new one that asserts the skip.
- The `nested_source` and `aborted`-delta assertions read the process-global registry, so the merge-level specs construct `metrics.NewMetrics()` rather than `mocks.FakeMetrics` — the precedent specs 012 and 013 set.
- `github.com/bborbe/errors` only, no `fmt.Errorf`; `slog` in new code; an injected `libtime` getter, never `time.Now()`; no `context.Background()` in non-test `pkg/` code (the Ginkgo suites legitimately use it for `ctx`).
- Precommit budgets: funlen 80, gocognit 20, nestif 4, golines 100. The added branch in `resolveEachPath` is four lines plus the helper call — do not deepen the loop further.
- `.dark-factory.yaml` sets `workflow: direct`, `pr: false`, `autoRelease: false`: commit to the branch directly; no PR or auto-release is expected.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.

<!-- Host-only and cluster rungs (the RED check, the frozen-interface `git log`, the Post-Deploy Rung-3 kubectl probes) live in spec 023's `## Verification` § Operator-executable and are out of scope for this container. -->
</constraints>

<verification>
Run inside the container. No `git` command may be used — the daemon masks `.git`.

```
GOFLAGS=-buildvcs=false go build ./...
GOFLAGS=-buildvcs=false go test ./pkg/git/ -count=1
make test
make precommit
```

All four must exit 0.

Then confirm the observable outcomes (each line must print `ok`):

```
test "$(grep -c 'nested conflict path already quarantined' pkg/git/git.go)" = "0" && echo ok   # abort error text gone
test "$(grep -c 'already under _conflicts/ rejected' pkg/git/git.go)" = "0" && echo ok          # abort WARN gone
test "$(grep -c 'already under _conflicts/ skipped' pkg/git/git.go)" = "1" && echo ok           # skip WARN present once
test "$(grep -c 'validateConflictPathsNotNested' pkg/git/git.go)" = "0" && echo ok              # pre-flight deleted
test "$(grep -c 'nestedConflictPath' pkg/git/git.go)" = "0" && echo ok                          # helper deleted
test "$(grep -c 'dropAcceptedDrains' pkg/git/git.go)" = "0" && echo ok                          # helper deleted
test "$(grep -c 'AC2: Pull fails with ErrConflictResolutionFailed' pkg/git/git_test.go)" = "0" && echo ok
test "$(grep -c 'the whole merge aborts' pkg/git/git_test.go)" = "0" && echo ok
test "$(grep -c 'is still refused by the nesting guard' pkg/git/git_test.go)" = "0" && echo ok
test "$(grep -c '^## Unreleased' CHANGELOG.md)" = "1" && echo ok
test "$(awk '/^## Unreleased/{f=1;next} /^## /{f=0} f && /^- /' CHANGELOG.md | grep -c .)" -ge 1 && echo ok
test "$(grep -c 'an unsafe path, a conflicted path that already lives under' docs/deployment.md)" = "0" && echo ok
test "$(grep -c 'skipped and kept local' docs/deployment.md)" -ge 1 && echo ok
test "$(grep -c 'regression, not a pass' docs/verifying-specs.md)" = "0" && echo ok
test "$(grep -c 'must stay refused' docs/verifying-specs.md)" = "0" && echo ok
test "$(grep -c 'delta 0' docs/verifying-specs.md)" -ge 1 && echo ok
test "$(grep -c 'specs/in-progress/023-bug-quarantined-path-aborts-whole-merge.md' specs/in-progress/013-quarantine-nesting-and-drain.md)" = "1" && echo ok
test "$(grep -c 'specs/in-progress/023-bug-quarantined-path-aborts-whole-merge.md' specs/in-progress/015-bug-pull-wedges-on-modify-delete-conflict.md)" = "1" && echo ok
```

`make precommit` is the authoritative gate: it runs format, generate, the full test suite, and the linter. If it fails, fix the failure and re-run only the failing target until it passes, then run `make precommit` once more.
</verification>
