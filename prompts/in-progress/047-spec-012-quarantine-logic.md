---
status: approved
spec: [012-quarantine-on-conflict]
created: "2026-06-02T20:00:00Z"
queued: "2026-06-02T20:36:39Z"
branch: dark-factory/quarantine-on-conflict
---

<summary>
- When the conflict resolver fails on a single file mid-merge, the puller no longer aborts the entire merge — it moves just that file aside and keeps going
- The failing file is preserved under a new `_conflicts/` directory inside the repo, with a unix-timestamp suffix on its filename so an operator can inspect or replay it later
- Every other conflicted file in the same merge still goes through the resolver normally; the merge produces one commit that includes the quarantine move
- The commit message now uses a fixed, greppable format `merge: resolved=[...] quarantined=[...]` so operators can find quarantine activity with `git log --grep`
- A new WARN log line names each quarantined path with the resolver error and the destination path
- A single bad markdown file no longer wedges the pod for hours (the 2026-06-02 incident)
- The pod only wedges if every conflicted file in a merge fails BOTH resolve and quarantine, or if `_conflicts/` already exists as a regular file (catastrophic config error)
- A new integration test exercises the 1-corrupt-of-11 happy path and the all-fail-quarantine pathological case; an unsafe-path test asserts the `unsafe_path` counter increments
- The `ConflictResolver` interface and its counterfeiter mock are not touched; the new code wraps the existing `Resolve` call from `resolveConflictMerge`
- Prompt 1 must already be complete and `make precommit` green — this prompt uses the `IncQuarantinedFiles()` method prompt 1 added
</summary>

<objective>
Modify `resolveConflictMerge` in `pkg/git/git.go` to replace the single-shot resolver-failure abort path with a per-file retry loop. On per-file resolver failure, attempt to quarantine the file via `git mv` into `_conflicts/<path>.<unix-ts>.md` (or `.<ts>.quarantined` for non-`.md` files). The merge commits with the fixed format `merge: resolved=[<sorted-paths>] quarantined=[<sorted-paths>]` if at least one path was resolved OR quarantined; otherwise the existing abort path runs. Add an integration test in `pkg/git/git_test.go` covering the 1-corrupt-of-11 happy path, the all-fail-quarantine pathological case, and the unsafe-path rejection. The `ConflictResolver` interface, `MarkerResolver`, `YAMLMergeResolver`, and `mocks/conflict_resolver.go` MUST stay byte-identical. Prompt 1 (`1-spec-012-quarantine-metrics.md`) must be complete and `make precommit` passing before this prompt begins.
</objective>

<context>
Read `CLAUDE.md` at the repo root for project conventions.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `errors.Wrap` / `errors.Wrapf` from `github.com/bborbe/errors`; sentinel errors; never `fmt.Errorf`, never bare `return err`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega; external test packages (`package git_test`); `BeforeEach` fixture setup; `captureSlogLogs` helper
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-test-types-guide.md` — Integration tests use in-process real git via `os/exec`, real temp working tree, no network
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md` — `slog.WarnContext` for conditional warnings; the existing `git.go` uses `log/slog`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` — Interface → Constructor → Struct → Method; pointer receivers; counterfeiter directives

Files to read in full before editing:

- `pkg/git/git.go` (794 lines) — `resolveConflictMerge` at lines 498-538 is the function you are rewriting. Read the existing `runCmdRaw` (lines 224-242), `runCmd` (lines 176-196), `validatePath` (lines 145-173) for the path-validation and git-exec helpers you will reuse
- `pkg/git/conflict_resolver.go` (53 lines) — `ConflictResolver` interface and `MarkerResolver` — FROZEN, do not edit
- `pkg/git/yaml_merge_resolver.go` (315 lines) — `YAMLMergeResolver` and `safePath` helper at lines 173-190 — FROZEN, do not edit. Note: `safePath` already exists in `YAMLMergeResolver`; the new quarantine code uses its own `unsafePath` check (the YAML resolver's `safePath` is unexported and belongs to the YAML resolver). The `unsafe_path` category constant is already pre-initialised by prompt 1.
- `pkg/git/git_test.go` (1155 lines) — `setupPullFixture` (lines 615-664) is the existing pattern for integration-test fixtures. `captureSlogLogs` (lines 888-895) is the helper to capture slog output. `noopMetrics` (lines 36-50) already has `IncQuarantinedFiles` stub from prompt 1. `Pull state machine` Describe (lines 686-886) is the parent Describe for pull-related tests; add new Context blocks there or in a sibling Describe.
- `pkg/git/yaml_merge_resolver_test.go` (421 lines) — `setupYAMLConflictRepo` (lines 27-78) is the closest existing pattern for "set up a repo with 1 conflicted file". It produces a single-file conflict; the new test needs an 11-file variant.
- `mocks/metrics.go` (counterfeiter-generated) — already includes `IncQuarantinedFiles` (added by prompt 1)
- `CHANGELOG.md` — `## Unreleased` already has two bullets from prompt 1. Append this prompt's bullets to that same section (do NOT create a new `## Unreleased` heading).

**Cross-prompt note:** Prompt 1 ships the metric and the interface method. This prompt USES them. Do not re-add `IncQuarantinedFiles` or `QuarantinedFilesTotal` — they are already in the codebase from prompt 1.

**FROZEN (do not edit):**
- `pkg/git/conflict_resolver.go` — `ConflictResolver` interface unchanged
- `pkg/git/yaml_merge_resolver.go` — internal implementation unchanged
- `mocks/conflict_resolver.go` — counterfeiter output unchanged
- `pkg/git/conflict_resolver_test.go` and `pkg/git/yaml_merge_resolver_test.go` — existing tests must pass unedited

**Verified from the spec:**
- `ErrConflictResolutionFailed` is the existing sentinel (line 48 of `git.go`) — reused for the abort path
- `IncResolverFailure(category string)` already exists (added by spec 011) — call it with `"unsafe_path"` and `"git_mv_failed"`
- `IncQuarantinedFiles()` is the new method (added by prompt 1)
- `IncMergeOutcome("resolved")` (line 530 of `git.go`) fires on successful commit; `IncMergeOutcome("aborted")` (line 517) fires on abort — the new code keeps both call sites
- `IncConflictPaths(n)` (line 531) — call with `len(conflictPaths)` (the original semantic: total count of paths the merge touched, including ones that failed both resolve and quarantine)
- `g.currentDateTimeGetter` (line 139) is `libtime.CurrentDateTimeGetter` — use `g.currentDateTimeGetter.Now().Unix()` for the timestamp
- `_conflicts/` is the quarantine directory name; it must be at the repo root (NOT under any subdir)
- The spec is explicit that `_conflicts/` MUST NOT collide with any existing top-level directory in any served repo; pre-existence as a file is a catastrophic config error

**Quarantine destination path rule (spec, verbatim):**
- For `a.md` → `_conflicts/a.md.<ts>.md` (timestamp inserted before final `.md`)
- For `dir/b.md` → `_conflicts/dir/b.md.<ts>.md` (directory tree mirrors original path)
- For `foo.bin` (no `.md`) → `_conflicts/foo.bin.<ts>.quarantined`
- For `dir/foo.bin` → `_conflicts/dir/foo.bin.<ts>.quarantined`
- Use `g.currentDateTimeGetter.Now().Unix()` to get the integer unix-seconds
</context>

<requirements>

## 1. Define the failure-category constants used by the new quarantine code

In `pkg/git/git.go`, add a small const block near the top of the file (just below the existing sentinel-var block at lines 33-48):

```go
// Failure category label values emitted on git_rest_resolver_failures_total{category=...}
// from the quarantine path in resolveConflictMerge.
const (
	quarantineFailureUnsafePath = "unsafe_path"
	quarantineFailureGitMv      = "git_mv_failed"
)
```

These names are local to `git.go`; they are NOT exported. The `unsafe_path` value is already pre-initialised by the existing `init()` in `pkg/metrics/metrics.go` (added by spec 011) and re-confirmed by prompt 1. The `git_mv_failed` value was added by prompt 1. The constant names are intentionally different from the ones in `yaml_merge_resolver.go` (which uses `resolverFailureUnsafePath`, `resolverFailureGitAdd`, etc.) because the quarantine code is in a different file and uses its own vocabulary.

## 2. Add the quarantine helpers

Add the following helpers in `pkg/git/git.go`, placed just below the existing `runCmdRaw` helper (around line 242). These are private (lowercase) and used only by `resolveConflictMerge`.

```go
// quarantineDestPath builds the destination path for a quarantined file.
// For paths ending in ".md", the timestamp is inserted before the final ".md"
// and the directory tree under "_conflicts/" mirrors the original path
// (e.g. "dir/b.md" -> "_conflicts/dir/b.md.<ts>.md"). For non-".md" paths
// the timestamp is appended with a ".quarantined" suffix
// (e.g. "foo.bin" -> "_conflicts/foo.bin.<ts>.quarantined"). The repoPath is
// not included; the caller is expected to join it with the repo root.
func quarantineDestPath(path string, unixSeconds int64) string {
	ts := strconv.FormatInt(unixSeconds, 10)
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	if dir == "." {
		dir = ""
	}
	if strings.HasSuffix(base, ".md") {
		stripped := strings.TrimSuffix(base, ".md")
		base = stripped + "." + ts + ".md"
	} else {
		base = base + "." + ts + ".quarantined"
	}
	if dir == "" {
		return filepath.Join("_conflicts", base)
	}
	return filepath.Join("_conflicts", dir, base)
}

// unsafeConflictPath returns true and a non-empty reason if path escapes the
// repo root after joining. Mirrors the safePath pattern in yaml_merge_resolver.go
// but lives here because the quarantine code needs an independent check (the
// YAML resolver's safePath is unexported and only callable from inside the
// yamlMergeResolver). The check rejects absolute paths, ".." components, and
// any joined result that does not stay under repoPath.
func unsafeConflictPath(repoPath, rel string) (bool, string) {
	if rel == "" {
		return true, "empty path"
	}
	if filepath.IsAbs(rel) {
		return true, "absolute path"
	}
	joined := filepath.Join(repoPath, rel)
	cleanRoot := filepath.Clean(repoPath)
	if !strings.HasPrefix(joined+string(filepath.Separator), cleanRoot+string(filepath.Separator)) &&
		joined != cleanRoot {
		return true, "path escapes repo root"
	}
	return false, ""
}
```

Add the `strconv` import to the existing import block at the top of the file (`strconv` is not currently imported in `git.go` — verify with `grep -n strconv pkg/git/git.go`).

## 3. Rewrite `resolveConflictMerge` to use the per-file retry loop

Replace the body of `resolveConflictMerge` (lines 498-538) with the new per-file loop. The new implementation:

```go
// resolveConflictMerge handles the conflict path of pullMergeAndPush: delegates to g.resolver,
// commits the resolved merge, then pushes. On per-file resolver failure, the failing file is
// quarantined via `git mv` into _conflicts/<path>.<ts>.md (or .<ts>.quarantined for non-md).
// The merge commits with format `merge: resolved=[...] quarantined=[...]` if at least one path
// was resolved or quarantined; otherwise the existing abort path runs.
//
// The per-file loop is in resolveConflictPaths (unexported) so the unsafe-path and quarantine
// branches can be unit-tested in package git (internal test file) without going through Pull.
func (g *git) resolveConflictMerge(
	ctx context.Context,
	upstream string,
	mergeOut []byte,
	mergeErr error,
) error {
	conflictPaths := parseMergeConflictPaths(string(mergeOut))
	if len(conflictPaths) == 0 {
		g.metrics.IncGitOperationError("merge")
		return errors.Wrapf(
			ctx,
			mergeErr,
			"merge %s: %s",
			upstream,
			strings.TrimSpace(string(mergeOut)),
		)
	}
	return g.resolveConflictPaths(ctx, conflictPaths)
}

// resolveConflictPaths runs the per-file retry loop on a pre-parsed conflict path list.
// Extracted from resolveConflictMerge so the unsafe-path and quarantine branches can be
// exercised directly from internal tests (package git) without fabricating a real git merge
// output. Returns nil on successful commit+push; returns wrapped ErrConflictResolutionFailed
// on any abort path.
func (g *git) resolveConflictPaths(
	ctx context.Context,
	conflictPaths []string,
) error {
	resolved := make([]string, 0, len(conflictPaths))
	quarantined := make([]string, 0, len(conflictPaths))
	ts := g.currentDateTimeGetter.Now().Unix()

	for _, path := range conflictPaths {
		// Reject paths that escape the repo root before any I/O. Such paths are
		// neither resolved nor quarantined; they count toward the abort.
		if unsafe, reason := unsafeConflictPath(g.repoPath, path); unsafe {
			g.metrics.IncResolverFailure(quarantineFailureUnsafePath)
			slog.WarnContext(
				ctx,
				"git-rest: unsafe conflicted path rejected; aborting merge",
				"path",
				path,
				"reason",
				reason,
			)
			_, _ = g.runCmdRaw(ctx, g.repoPath, "merge", "--abort")
			g.metrics.IncMergeOutcome("aborted")
			return errors.Wrap(
				ctx,
				ErrConflictResolutionFailed,
				"unsafe conflict path",
			)
		}

		// 1) Try the resolver on this single path.
		resolveErr := g.resolver.Resolve(ctx, []string{path})
		if resolveErr == nil {
			resolved = append(resolved, path)
			continue
		}
		slog.WarnContext(
			ctx,
			"git-rest: resolver failed on path; attempting quarantine",
			"path",
			path,
			"err",
			resolveErr.Error(),
		)

		// 2) Quarantine: ensure _conflicts/ exists, then `git mv` the file there.
		conflictsDirRel := "_conflicts"
		conflictsDirAbs := filepath.Join(g.repoPath, conflictsDirRel)
		if info, statErr := os.Stat(conflictsDirAbs); statErr == nil {
			if !info.IsDir() {
				// Catastrophic config error: _conflicts/ exists as a regular file.
				slog.ErrorContext(
					ctx,
					"git-rest: _conflicts/ exists as a regular file; aborting merge",
					"path",
					conflictsDirRel,
				)
				_, _ = g.runCmdRaw(ctx, g.repoPath, "merge", "--abort")
				g.metrics.IncMergeOutcome("aborted")
				return errors.Wrap(
					ctx,
					ErrConflictResolutionFailed,
					"_conflicts/ is a regular file",
				)
			}
		} else if os.IsNotExist(statErr) {
			if mkErr := os.MkdirAll(conflictsDirAbs, 0o750); mkErr != nil {
				g.metrics.IncResolverFailure(quarantineFailureGitMv)
				slog.ErrorContext(
					ctx,
					"git-rest: failed to create _conflicts/; aborting merge",
					"path",
					path,
					"err",
					mkErr.Error(),
				)
				_, _ = g.runCmdRaw(ctx, g.repoPath, "merge", "--abort")
				g.metrics.IncMergeOutcome("aborted")
				return errors.Wrap(ctx, ErrConflictResolutionFailed, "mkdir _conflicts")
			}
		} else {
			g.metrics.IncResolverFailure(quarantineFailureGitMv)
			slog.ErrorContext(
				ctx,
				"git-rest: stat _conflicts/ failed; aborting merge",
				"path",
				conflictsDirRel,
				"err",
				statErr.Error(),
			)
			_, _ = g.runCmdRaw(ctx, g.repoPath, "merge", "--abort")
			g.metrics.IncMergeOutcome("aborted")
			return errors.Wrap(ctx, ErrConflictResolutionFailed, "stat _conflicts")
		}

		destRel := quarantineDestPath(path, ts)
		if mvErr := g.runCmd(ctx, g.repoPath, "mv", "--", path, destRel); mvErr != nil {
			g.metrics.IncResolverFailure(quarantineFailureGitMv)
			slog.ErrorContext(
				ctx,
				"git-rest: git mv quarantine failed",
				"path",
				path,
				"dest",
				destRel,
				"err",
				mvErr.Error(),
			)
			continue
		}

		// Quarantine succeeded.
		g.metrics.IncQuarantinedFiles()
		slog.WarnContext(
			ctx,
			"git-rest: quarantined conflicted file",
			"path",
			path,
			"dest",
			destRel,
		)
		quarantined = append(quarantined, path)
	}

	// Pathological case: every conflicted path failed BOTH resolve and quarantine.
	if len(resolved) == 0 && len(quarantined) == 0 {
		_, _ = g.runCmdRaw(ctx, g.repoPath, "merge", "--abort")
		g.metrics.IncMergeOutcome("aborted")
		return errors.Wrap(
			ctx,
			ErrConflictResolutionFailed,
			"all conflicted paths failed resolve and quarantine",
		)
	}

	sort.Strings(resolved)
	sort.Strings(quarantined)
	commitMsg := "merge: resolved=[" + strings.Join(resolved, ",") +
		"] quarantined=[" + strings.Join(quarantined, ",") + "]"

	if commitErr := g.runCmd(ctx, g.repoPath, "commit", "-m", commitMsg); commitErr != nil {
		_, _ = g.runCmdRaw(ctx, g.repoPath, "merge", "--abort")
		g.metrics.IncGitOperationError("merge")
		return errors.Wrap(ctx, commitErr, "commit resolved merge")
	}
	g.metrics.IncMergeOutcome("resolved")
	// Count all conflicted paths the merge touched, including ones that failed both
	// resolve and quarantine (which contributed to the all-fail abort when this
	// commit succeeded but the per-file loop still saw them). Matches the original
	// semantics of `IncConflictPaths(len(conflictPaths))` for the clean case.
	g.metrics.IncConflictPaths(len(conflictPaths))
	slog.InfoContext(
		ctx,
		"git-rest: merge committed with per-file quarantine",
		"resolved",
		resolved,
		"quarantined",
		quarantined,
	)
	if pushErr := g.runCmd(ctx, g.repoPath, "push"); pushErr != nil {
		g.metrics.IncGitOperationError("push")
		return errors.Wrap(ctx, pushErr, "push after resolved merge failed")
	}
	return nil
}
```

Notes for the implementer:

- The new function still calls `g.resolver.Resolve(ctx, []string{path})` per path, NOT `g.resolver.Resolve(ctx, conflictPaths)` for the whole batch. This is the core behavior change.
- The `ts` is captured ONCE at the start of the loop (all quarantines in the same merge get the same timestamp suffix). This is fine because the pod is single-threaded per pull; the spec calls out timestamp-collision recovery under the "two quarantines of the same path in the same second" failure mode, which falls back to the existing `git_mv_failed` row in the failure-mode table.
- `runCmd` (line 176) already wraps git failures with `errors.Wrapf(ctx, err, "git %v: %s", args, buf.String())` — the `mvErr.Error()` surfaced in the WARN log includes the `git mv` stderr.
- The existing `validatePath` (line 145) is NOT used here because the quarantine code only needs the containment check; the deeper validation (e.g. `.git` directory component rejection) is unnecessary for conflict paths, which come from `git merge` output.
- The `_conflicts/` directory check uses `os.Stat` once per merge (per-merge cost is negligible; per-path cost would be a hot-path regression).
- The `unsafe_path` abort path uses `runCmdRaw` for the merge --abort (matching the existing abort path at line 516); the new abort paths likewise use `runCmdRaw` so an abort that itself fails does not mask the original error.
- The commit message uses the literal format from the spec; `strings.Join(..., ",")` is correct (no space — spec is explicit: "comma-separated with no spaces"). Empty slices render as `[]` (e.g. all-quarantined case: `merge: resolved=[] quarantined=[a.md,b.md]`).
- `g.metrics.IncConflictPaths` is called with `len(conflictPaths)` (the total number of conflicted paths the merge touched, including any that failed both resolve and quarantine). This matches the original semantics: every conflicted path in the merge is counted once, regardless of per-file outcome.
- The function still uses `ctx` everywhere (passed to `runCmd`, `runCmdRaw`, `slog.WarnContext`, `errors.Wrap`). No `context.Background()` is introduced.

## 4. Add the integration test in `pkg/git/git_test.go`

Add a new `Context` block inside the existing `Pull state machine` Describe (around line 686) for the 1-corrupt-of-11 happy path, plus separate `Context` blocks for the pathological case and the unsafe-path rejection. NO new test file — the spec says "Same `*_test.go` file as the existing `git_test.go` resides in."

Before adding the new tests, add this fixture helper to `pkg/git/git_test.go` (place it just below the existing `setupPullFixture` helper at line 615):

```go
// setupQuarantineFixture creates a working repo backed by a local bare remote
// pre-seeded with fileCount markdown files. Returns workDir, two closures
// (localEdit commits a divergent version of one file locally; externalPush
// pushes a divergent version of one file from a temp clone), the seed file
// list, and a cleanup func.
//
// Use pattern in tests:
//
//	workDir, localEdit, externalPush, seedFiles, cleanup := setupQuarantineFixture(11)
//	defer cleanup()
//	for i, f := range seedFiles {
//	    externalPush(f, fmt.Sprintf("---\nshared: remote-%d\n---\nremote body %d\n", i, i)) // diverge remote
//	}
//	for i, f := range seedFiles {
//	    localEdit(f, fmt.Sprintf("---\nshared: local-%d\n---\nlocal body %d\n", i, i))       // diverge local differently
//	}
//	// Now g.Pull(ctx) produces fileCount conflicts.
func setupQuarantineFixture(fileCount int) (
	workDir string,
	localEdit func(file, content string),
	externalPush func(file, content string),
	seedFiles []string,
	cleanup func(),
) {
	remoteDir, err := os.MkdirTemp("", "git-remote-quarantine-*")
	Expect(err).NotTo(HaveOccurred())
	workDir, err = os.MkdirTemp("", "git-work-quarantine-*")
	Expect(err).NotTo(HaveOccurred())

	rg := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, e := cmd.CombinedOutput()
		Expect(e).NotTo(HaveOccurred(), "%s %v: %s", "git", args, string(out))
	}

	rg(remoteDir, "init", "--bare")
	rg(workDir, "init", "-b", "main")
	rg(workDir, "config", "user.email", "test@example.com")
	rg(workDir, "config", "user.name", "Test")

	// Seed fileCount markdown files on main and push to origin.
	seedFiles = make([]string, 0, fileCount)
	for i := 0; i < fileCount; i++ {
		name := fmt.Sprintf("note-%02d.md", i)
		seedFiles = append(seedFiles, name)
		abs := filepath.Join(workDir, name)
		fm := fmt.Sprintf("key%d: 1\nshared: base\n", i)
		body := fmt.Sprintf("body %d base\n", i)
		content := "---\n" + fm + "---\n" + body
		Expect(os.WriteFile(abs, []byte(content), 0o600)).To(Succeed())
		rg(workDir, "add", "--", name)
	}
	rg(workDir, "commit", "-q", "-m", "seed")
	rg(workDir, "push", "-u", "origin", "main")

	// localEdit: write a divergent version of one file locally and commit
	// (does not push). Pair with externalPush for the same file to create
	// a real merge conflict.
	localEdit = func(file, content string) {
		abs := filepath.Join(workDir, file)
		Expect(os.WriteFile(abs, []byte(content), 0o600)).To(Succeed())
		rg(workDir, "add", "--", file)
		rg(workDir, "commit", "-q", "-m", "local: "+file)
	}

	// externalPush: clone the bare remote, change one file, commit, push.
	externalPush = func(file, content string) {
		extDir, err := os.MkdirTemp("", "git-ext-quarantine-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = os.RemoveAll(extDir) }()
		rg(extDir, "clone", remoteDir, ".")
		rg(extDir, "config", "user.email", "ext@example.com")
		rg(extDir, "config", "user.name", "External")
		abs := filepath.Join(extDir, file)
		Expect(os.WriteFile(abs, []byte(content), 0o600)).To(Succeed())
		rg(extDir, "add", "--", file)
		rg(extDir, "commit", "-q", "-m", "external: "+file)
		rg(extDir, "push", "origin", "main")
	}

	cleanup = func() {
		_ = os.RemoveAll(workDir)
		_ = os.RemoveAll(remoteDir)
	}
	return workDir, localEdit, externalPush, seedFiles, cleanup
}
```

The fixture contract: after seeding and the test's per-file `externalPush` + `localEdit` calls, the working tree has `fileCount` conflicted files when `g.Pull(ctx)` runs. For the 1-corrupt-of-11 happy path: seed 11 files, `externalPush` a divergent valid version of each, `localEdit` a different divergent version of each (with the corrupt one having invalid YAML).

Add this small test helper just below the existing `setupQuarantineFixture` definition (private to the test package, alongside `gitOutputStr` and `captureSlogLogs`):

```go
// gatherQuarantinedFiles returns the current value of the process-global
// git_rest_quarantined_files_total counter. Returns 0 if the counter is not yet
// registered. Used by the per-file quarantine tests to capture a baseline before
// Pull and assert on the delta afterwards (so the test is order-independent with
// other tests in the suite that may also touch the counter).
func gatherQuarantinedFiles() float64 {
	mfs, err := prometheus.DefaultGatherer.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, mf := range mfs {
		if mf.GetName() != "git_rest_quarantined_files_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			return m.GetCounter().GetValue()
		}
	}
	return 0
}
```

Add these `Context` blocks to the existing `Pull state machine` Describe (just before the closing `})` of the Describe, around line 886):

```go
	Context("diverged, 1 of 11 conflicted files has invalid frontmatter (per-file quarantine)", func() {
		var (
			corruptPath = "note-00.md"
			workDir     string
			seedFiles   []string
			pgLocal     git.Git
		)
		BeforeEach(func() {
			var localEdit, externalPush func(file, content string)
			var qCleanup func()
			workDir, localEdit, externalPush, seedFiles, qCleanup = setupQuarantineFixture(11)
			DeferCleanup(qCleanup)

			// Push a divergent valid version of every file to the remote.
			for i, f := range seedFiles {
				externalPush(
					f,
					fmt.Sprintf("---\nshared: remote-%d\n---\nremote body %d\n", i, i),
				)
			}
			// Local divergence: invalid YAML on the corrupt file, valid divergence on the 10 clean ones.
			for i, f := range seedFiles {
				if f == corruptPath {
					localEdit(f, "---\nbad: : : colon: storm\n---\nlocal corrupt body\n")
				} else {
					localEdit(
						f,
						fmt.Sprintf("---\nshared: local-%d\n---\nlocal body %d\n", i, i),
					)
				}
			}

			pgLocal = git.New(
				workDir,
				fakeMetrics,
				libtime.NewCurrentDateTime(),
				"",
				git.NewYAMLMergeResolver(workDir, fakeMetrics),
			)
		})

		It("AC1-AC6 + AC12: Pull returns nil; 1 file quarantined; 10 resolved; commit message in fixed format", func() {
			logs, restore := captureSlogLogs()
			defer restore()

			// AC4 setup: capture the pre-Pull baseline of the process-global counter
			// so the post-Pull assertion is a delta, not an absolute value.
			beforeQuarantined := gatherQuarantinedFiles()
			Expect(pgLocal.Pull(ctx)).To(BeNil())
			afterQuarantined := gatherQuarantinedFiles()
			Expect(afterQuarantined - beforeQuarantined).To(Equal(1.0),
				"git_rest_quarantined_files_total must increment by exactly 1 in this test")

			// AC2: corrupt file is in _conflicts/ tree; no longer at the original path.
			// Spec evidence: filepath.Glob finds exactly one matching quarantine file;
			// os.Stat of the original path returns IsNotExist.
			qGlob, _ := filepath.Glob(filepath.Join(workDir, "_conflicts", corruptPath+".*.md"))
			Expect(qGlob).To(HaveLen(1), "exactly one _conflicts/<corrupt-path>.<ts>.md must exist; got: %v", qGlob)
			_, statErr := os.Stat(filepath.Join(workDir, corruptPath))
			Expect(os.IsNotExist(statErr)).To(BeTrue(),
				"corrupt file must not exist at original path after quarantine")

			// AC3: 10 clean files at original paths with merged content (local body preserved).
			for i := 1; i < 11; i++ {
				name := fmt.Sprintf("note-%02d.md", i)
				abs := filepath.Join(workDir, name)
				data, readErr := os.ReadFile(abs)
				Expect(readErr).NotTo(HaveOccurred())
				Expect(string(data)).To(ContainSubstring(fmt.Sprintf("local body %d", i)))
			}

			// AC5: exactly one merge commit on top of pre-test HEAD; clean working tree.
			preTestHead := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "HEAD@{1}"))
			revCount := strings.TrimSpace(
				gitOutputStr(workDir, "rev-list", "--count", "HEAD", "^"+preTestHead),
			)
			Expect(revCount).To(Equal("1"), "expected exactly one merge commit, got: %s", revCount)
			Expect(strings.TrimSpace(gitOutputStr(workDir, "status", "--porcelain"))).
				To(BeEmpty(), "working tree must be clean after commit")

			// AC6: WARN log line naming the corrupt path and the substring "quarantined".
			logStr := logs.String()
			Expect(logStr).To(ContainSubstring("quarantined conflicted file"))
			Expect(logStr).To(ContainSubstring(corruptPath))

			// AC12: commit message matches the fixed format and lists the corrupt path
			// in quarantined plus all 10 clean paths in resolved (sorted alphabetically).
			commitMsg := strings.TrimSpace(gitOutputStr(workDir, "log", "-1", "--format=%s"))
			Expect(commitMsg).To(MatchRegexp(
				`^merge: resolved=\[[^\]]*\] quarantined=\[[^\]]*\]$`,
			))
			Expect(commitMsg).To(ContainSubstring("quarantined=[" + corruptPath + "]"))
			for i := 1; i < 11; i++ {
				name := fmt.Sprintf("note-%02d.md", i)
				Expect(commitMsg).To(ContainSubstring(name),
					"clean path %s must be in commit message", name)
			}
		})
	})

	Context("diverged, _conflicts/ already exists as a regular file (pathological case)", func() {
		It("AC8: Pull returns wrapped ErrConflictResolutionFailed; working tree is clean; ERROR log is emitted", func() {
			workDir, localEdit, externalPush, seedFiles, qCleanup := setupQuarantineFixture(1)
			defer qCleanup()
			pPath := seedFiles[0]

			// Capture slog output to assert the spec's failure-modes detection
			// requirement: ERROR log line is emitted on the `_conflicts/ is a
			// regular file` abort path (mirrors the WARN assertion in the
			// unsafe-path internal test for symmetry).
			logs, restoreLogs := captureSlogLogs()
			defer restoreLogs()

			// Force a single-file conflict with invalid frontmatter so the resolver fails.
			externalPush(pPath, "---\nshared: remote\n---\nremote body\n")
			localEdit(pPath, "---\nbad: : : colon: storm\n---\nlocal body\n")

			// Pre-create _conflicts/ as a regular file to trip the pre-flight stat check.
			conflictPath := filepath.Join(workDir, "_conflicts")
			Expect(os.WriteFile(conflictPath, []byte("not a directory"), 0o600)).To(Succeed())

			pgPath := git.New(
				workDir,
				fakeMetrics,
				libtime.NewCurrentDateTime(),
				"",
				git.NewYAMLMergeResolver(workDir, fakeMetrics),
			)

			err := pgPath.Pull(ctx)
			Expect(err).To(HaveOccurred())
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue(),
				"expected wrapped ErrConflictResolutionFailed, got: %v", err)

			// Working tree clean: merge was aborted.
			Expect(strings.TrimSpace(gitOutputStr(workDir, "status", "--porcelain"))).
				To(BeEmpty(), "working tree must be clean after abort")

			// ERROR log line names the catastrophic config error (spec failure-modes row).
			Expect(logs.String()).To(ContainSubstring("_conflicts/ exists as a regular file"))
			Expect(logs.String()).To(ContainSubstring("level=ERROR"))
		})
	})

	// AC11 (unsafe path) is tested in pkg/git/resolve_conflict_merge_test.go
	// (a separate internal test file in `package git`) because it requires injecting a
	// crafted conflictPaths list (containing "../escape.md") that a real git merge cannot
	// produce. The internal test file calls the unexported resolveConflictPaths helper
	// directly. See requirement 4b below for the file contents.
```

Create a new internal test file `pkg/git/resolve_conflict_merge_test.go` in `package git` (NOT `package git_test`) — internal tests have access to unexported symbols and can call `resolveConflictPaths` directly with a crafted conflictPaths list. This is the only way to exercise the unsafe-path branch end-to-end without going through a real `git merge` (which cannot produce `../escape.md` from honest history). The file MUST live next to the existing `conflict_resolver_test.go` (which is `package git_test`); Go allows both internal and external test files in the same directory.

```go
// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"context"
	stderrors "errors"
	"os"
	"os/exec"
	"path/filepath"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/mocks"
)

// fakeResolver is a noop ConflictResolver used to drive the per-file loop into the
// quarantine and abort branches without depending on MarkerResolver or YAMLMergeResolver.
type fakeResolver struct{}

func (fakeResolver) Resolve(_ context.Context, _ []string) error { return nil }

var _ = Describe("resolveConflictPaths (internal)", func() {
	var (
		ctx    context.Context
		workDir string
		repo   *git
		metrics *mocks.FakeMetrics
	)

	BeforeEach(func() {
		ctx = context.Background()
		workDir, err := os.MkdirTemp("", "git-resolve-conflict-paths-*")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = os.RemoveAll(workDir) })
		// Initialise a real git repo so the production `git merge --abort` call in
		// the unsafe-path abort path runs against a valid working tree (even though
		// there is no merge in progress — the call is best-effort and its output is
		// discarded).
		run := func(args ...string) {
			cmd := exec.Command("git", args...)
			cmd.Dir = workDir
			out, e := cmd.CombinedOutput()
			Expect(e).NotTo(HaveOccurred(), "%s %v: %s", "git", args, string(out))
		}
		run("init", "-q", "-b", "main")
		run("config", "user.email", "test@example.com")
		run("config", "user.name", "Test")
		run("commit", "--allow-empty", "-q", "-m", "init")
		metrics = &mocks.FakeMetrics{}
		// Use the package constructor (not a struct literal) so any future
		// fields added to `git` (validation, default-init, helpers) are picked
		// up automatically. The fakeResolver is unused in the unsafe-path
		// branch (path validation rejects before resolver.Resolve fires) but
		// the constructor still requires a non-nil value.
		repo = New(workDir, metrics, libtime.NewCurrentDateTime(), "", fakeResolver{}).(*git)
	})

	It("AC11: unsafe path is rejected, unsafe_path counter increments, abort path runs", func() {
		err := repo.resolveConflictPaths(ctx, []string{"../escape.md"})
		Expect(err).To(HaveOccurred())
		Expect(stderrors.Is(err, ErrConflictResolutionFailed)).To(BeTrue(),
			"expected wrapped ErrConflictResolutionFailed, got: %v", err)

		// unsafe_path counter incremented exactly once.
		var unsafeCount int
		for i := 0; i < metrics.IncResolverFailureCallCount(); i++ {
			if metrics.IncResolverFailureArgsForCall(i) == "unsafe_path" {
				unsafeCount++
			}
		}
		Expect(unsafeCount).To(Equal(1), "unsafe_path counter must increment exactly once")

		// abort outcome recorded.
		var abortedCount int
		for i := 0; i < metrics.IncMergeOutcomeCallCount(); i++ {
			if metrics.IncMergeOutcomeArgsForCall(i) == "aborted" {
				abortedCount++
			}
		}
		Expect(abortedCount).To(Equal(1), "aborted outcome must be recorded exactly once")

		// The escape path must not exist on disk (we never tried to write it).
		externalPath := filepath.Clean(filepath.Join(workDir, "..", "escape.md"))
		_, statErr := os.Stat(externalPath)
		Expect(os.IsNotExist(statErr)).To(BeTrue(),
			"escape path must not exist on disk outside the repo root")

		// No _conflicts/ entries mentioning the escape attempt.
		matches, _ := filepath.Glob(filepath.Join(workDir, "_conflicts", "**", "*escape*"))
		Expect(matches).To(BeEmpty(),
			"no _conflicts/** entry may mention the escape attempt; got: %v", matches)
	})
})
```

Notes for the implementer:

- The `*git` literal in `BeforeEach` works because this file is `package git` (internal); the unexported `git` struct (defined in `git.go` at line 135) is directly accessible. External test files (`package git_test`) cannot construct `*git` directly.
- `fakeResolver` is a local type defined in this test file; it is not exported and not added to `mocks/conflict_resolver.go` (which is the counterfeiter-generated mock and is frozen).
- The `git` struct literal sets only the fields the test needs (`repoPath`, `metrics`). `currentDateTimeGetter` and `resolver` are left nil — the unsafe-path branch returns BEFORE calling either, so the test never dereferences them.
- The `*mocks.FakeMetrics` type is already exported by counterfeiter (regenerated by prompt 1). It exposes `IncResolverFailureCallCount()`, `IncResolverFailureArgsForCall(i)`, `IncMergeOutcomeCallCount()`, `IncMergeOutcomeArgsForCall(i)`. The unsafe-path test asserts on the call counts, not the package-level `prometheus.DefaultGatherer`, because the FakeMetrics doesn't write to the default registry — the assertions stay inside the fake.
- The test file does NOT import `github.com/bborbe/git-rest/pkg/git` because it lives IN `package git`. Symbols like `ErrConflictResolutionFailed`, `resolveConflictPaths`, and the unexported `git` struct are all directly accessible without qualification.

Notes for the implementer:

- The 1-corrupt-of-11 happy path test is the spec's load-bearing integration test. The `externalPush` + `localEdit` per-file loop creates a real `git merge` with 11 simultaneous conflicts; the new per-file retry loop in `resolveConflictMerge` runs the `YAMLMergeResolver` once per path. The corrupt file fails the YAML parse; the 10 clean files succeed. The `IncQuarantinedFiles` counter is asserted at exactly 1.0.
- The pathological case AC (#8) is simpler: pre-create `_conflicts/` as a regular file, then trigger any conflict. The pre-flight `os.Stat` check in step 2 of the per-file loop catches it on the first path and runs the abort path with `ErrConflictResolutionFailed`. The new code runs the stat check ONCE at the start of the per-file loop, so even a single-path conflict trips it.
- The unsafe-path AC (#11) is exercised by the internal test file `pkg/git/resolve_conflict_merge_test.go` (added in requirement 4 above), which calls `repo.resolveConflictPaths(ctx, []string{"../escape.md"})` directly. This bypasses the `parseMergeConflictPaths` step that would otherwise restrict conflict paths to whatever `git merge` produces (which can never be `../escape.md` from honest history). The test asserts the `unsafe_path` counter increment, the `aborted` merge outcome, no file written outside the repo root, and no `_conflicts/**` entry mentioning the escape attempt.
- The `prometheus` import is already used in `pkg/git/yaml_merge_resolver_test.go` (line 17); add it to `pkg/git/git_test.go` if not already present (`grep -n prometheus pkg/git/git_test.go` to verify).
- The `fmt` import is already used in `pkg/git/git_test.go`; verify with `grep -n '^import\|\"fmt\"' pkg/git/git_test.go`.
- The `stderrors` alias is `stderrors "errors"` at line 10. Use `stderrors.Is(...)` for the `errors.Is` calls in the new test blocks.
- The `pg` variable in the existing `Pull state machine` Describe is re-assigned by the existing AC3 test (line 852); the new tests do NOT need to touch `pg` because they create their own `pgLocal` / `pgPath` local Git variables.

After all edits, the existing `git diff` of frozen files MUST be empty:

```bash
git diff --stat pkg/git/conflict_resolver.go pkg/git/yaml_merge_resolver.go mocks/conflict_resolver.go pkg/git/conflict_resolver_test.go pkg/git/yaml_merge_resolver_test.go
```

MUST show zero changes.

## 5. CHANGELOG: append this prompt's bullets to the existing `## Unreleased` section

Append (do NOT replace) the following bullets to the `## Unreleased` section that prompt 1 created:

```markdown
- feat: Per-file quarantine in `resolveConflictMerge` — when the conflict resolver fails on a single file, the puller moves the file to `_conflicts/<path>.<unix-ts>.md` (or `.<ts>.quarantined` for non-`.md` files) via `git mv` and continues the merge, instead of aborting and wedging the pod. The merge commit message uses the fixed format `merge: resolved=[<sorted-paths>] quarantined=[<sorted-paths>]`. The pod only wedges if every conflicted path fails BOTH resolve and quarantine, or if `_conflicts/` already exists as a regular file in the repo. WARN log line names each quarantined path with the resolver error and the destination. Fixes the 2026-06-02 vault-obsidian-openclaw-0 incident (3.5h pod-wedge from a single corrupt-frontmatter file).
```

The existing two `feat:` bullets from prompt 1 stay above. The header block (lines 1-9) stays untouched.

## 6. Final verification

Run from the repo root:

```bash
make precommit
```

Must exit 0.

Targeted tests:

```bash
go test ./pkg/git/... -v -run "Pull state machine"
go test ./pkg/git/... -v -run "MarkerResolver"
go test ./pkg/git/... -v -run "YAMLMergeResolver"
```

All pass.

Frozen-file sanity:

```bash
git diff --stat pkg/git/conflict_resolver.go pkg/git/yaml_merge_resolver.go mocks/conflict_resolver.go pkg/git/conflict_resolver_test.go pkg/git/yaml_merge_resolver_test.go
```

MUST show zero changes.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT edit `pkg/git/conflict_resolver.go`, `pkg/git/yaml_merge_resolver.go`, `mocks/conflict_resolver.go`, `pkg/git/conflict_resolver_test.go`, or `pkg/git/yaml_merge_resolver_test.go` — they are frozen by the spec.
- Do NOT change the `ConflictResolver` interface signature — it must remain `Resolve(ctx context.Context, conflictedPaths []string) error` exactly. The new code calls the existing interface once per path, but does not modify it.
- Do NOT add a "disable quarantine" flag, per-repo override, or any tunable threshold on the quarantine behavior — spec Non-goals.
- Do NOT add a `repo` label to the new counter — that was prompt 1's job and is already in place.
- Do NOT add a quarantine-size cap, retention policy, or auto-repair — spec Non-goals.
- Do NOT add Prometheus alerts on the new counter — sibling task covers alerting.
- `ErrConflictResolutionFailed` is the ONLY error the puller can return from the new abort paths. It MUST be wrapped via `errors.Wrap(ctx, ErrConflictResolutionFailed, "...")` so `errors.Is(err, ErrConflictResolutionFailed)` traverses the chain.
- The common per-file resolver failure does NOT surface as an error to the puller — it is handled by quarantine. Only the all-fail-quarantine pathological case, the `_conflicts/ is a file` pre-flight rejection, and the unsafe-path rejection return `ErrConflictResolutionFailed`.
- Errors MUST be wrapped with `errors.Wrap` / `errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf`, never bare `return err`.
- Logging via `log/slog` (matches the `slog.InfoContext` / `slog.WarnContext` calls already in `git.go`).
- The new quarantine code uses `slog.WarnContext` for the per-quarantine "resolver failed" and "quarantined" log lines; `slog.ErrorContext` for the catastrophic `_conflicts/ is a file` and `git mv failed` cases. The existing `git.go` already imports `log/slog` (line 12).
- `g.metrics.IncQuarantinedFiles()` (added by prompt 1) MUST be called exactly once per successful quarantine.
- `g.metrics.IncResolverFailure("git_mv_failed")` MUST be called for each `git mv` failure; `g.metrics.IncResolverFailure("unsafe_path")` for each unsafe-path rejection.
- The new `_conflicts/` directory MUST be created at the repo root (not under any subdir) when it does not already exist; pre-existing as a file triggers the abort path.
- The commit message format is FIXED: `merge: resolved=[<sorted-comma-paths>] quarantined=[<sorted-comma-paths>]` — paths sorted alphabetically, comma-separated, no spaces, empty list rendered as `[]`. The format MUST be greppable by operators via `git log --grep`.
- The new code MUST honor `ctx` for the `runCmd` / `runCmdRaw` calls and the `slog` calls — never `context.Background()`.
- `g.currentDateTimeGetter.Now().Unix()` MUST be called ONCE at the start of the per-file loop (NOT per path) so all quarantines in the same merge get the same timestamp suffix.
- The quarantine destination path rule is FIXED:
  - `*.md` → `_conflicts/<dir-tree-mirror>/<name-without-md>.<ts>.md`
  - non-`*.md` → `_conflicts/<dir-tree-mirror>/<name>.<ts>.quarantined`
- Existing tests must still pass: the only edit to `pkg/git/git_test.go` allowed beyond the new test cases is the existing one-line `noopMetrics.IncQuarantinedFiles` stub added by prompt 1.
- The 1-corrupt-of-11 happy-path test and the pathological case test live in the existing `pkg/git/git_test.go` (external test package, `package git_test`). The unsafe-path test lives in a NEW internal test file `pkg/git/resolve_conflict_merge_test.go` (internal test package, `package git`) because the test needs to construct `*git` directly and call the unexported `resolveConflictPaths` helper. Go allows both internal and external test files in the same directory. Both files use Ginkgo v2 + Gomega; no `t.Run` tables. No build tags.
- `make precommit` from the repo root MUST exit 0.
- The CHANGELOG entry MUST live under `## Unreleased` (the section prompt 1 created), NOT under a freshly-invented version heading.
- The `## Unreleased` heading already exists; APPEND to it. Do NOT create a second `## Unreleased` heading.
</constraints>

<verification>
Run from the repo root:

```bash
make precommit
```

Must exit 0.

Targeted test runs:

```bash
go test ./pkg/git/... -v -run "Pull state machine" -ginkgo.v
go test ./pkg/git/... -v -run "MarkerResolver"
go test ./pkg/git/... -v -run "YAMLMergeResolver"
go test ./pkg/metrics/... -v
```

All pass.

Frozen-file sanity (matches the spec's Verification block):

```bash
git diff pkg/git/conflict_resolver.go mocks/conflict_resolver.go pkg/git/conflict_resolver_test.go pkg/git/yaml_merge_resolver_test.go
```

MUST report zero changes.

CHANGELOG entries:

```bash
grep -n '^## Unreleased' CHANGELOG.md                                # exactly 1 match
grep -n 'quarantine' CHANGELOG.md                                    # ≥1 match in the ## Unreleased section
grep -n 'merge: resolved=' CHANGELOG.md                               # ≥1 match in the ## Unreleased section
grep -n 'git_rest_quarantined_files_total' CHANGELOG.md              # ≥1 match (added by prompt 1, still present)
```

Commit-message-format integration assertion is part of the integration test in requirement 4 — covered by the test's `MatchRegexp` assertion on `git log -1 --format=%s`.

Final spec-AC sweep (matches the spec's Verification block):

```bash
make precommit
make test
grep -n 'git_rest_quarantined_files_total' pkg/metrics/metrics.go
grep -n 'quarantine' CHANGELOG.md
git diff pkg/git/conflict_resolver.go mocks/conflict_resolver.go pkg/git/conflict_resolver_test.go pkg/git/yaml_merge_resolver_test.go
```

All commands exit 0; the `grep` lines return ≥1 match; the `git diff` shows zero changes for the listed files.
</verification>

<changelog>
Append to the existing `## Unreleased` section in `CHANGELOG.md` (the section prompt 1 created — do NOT create a second `## Unreleased` heading):

```markdown
- feat: Per-file quarantine in `resolveConflictMerge` — when the conflict resolver fails on a single file, the puller moves the file to `_conflicts/<path>.<unix-ts>.md` (or `.<ts>.quarantined` for non-`.md` files) via `git mv` and continues the merge, instead of aborting and wedging the pod. The merge commit message uses the fixed format `merge: resolved=[<sorted-paths>] quarantined=[<sorted-paths>]`. The pod only wedges if every conflicted path fails BOTH resolve and quarantine, or if `_conflicts/` already exists as a regular file in the repo. WARN log line names each quarantined path with the resolver error and the destination. Fixes the 2026-06-02 vault-obsidian-openclaw-0 incident (3.5h pod-wedge from a single corrupt-frontmatter file).
```

Prefix: `feat:` (new feature → minor bump).
</changelog>

<self-review>
Standard Go self-review checklist applies (composition, factory, patterns, context cancellation, concurrency, HTTP handlers, testing, imports-grep-verified).

Spec-specific gates for this prompt:

- [ ] `git diff --stat pkg/git/conflict_resolver.go pkg/git/yaml_merge_resolver.go mocks/conflict_resolver.go pkg/git/conflict_resolver_test.go pkg/git/yaml_merge_resolver_test.go` is empty
- [ ] `git diff pkg/git/git_test.go | grep -E 'noopMetrics'` shows ONLY the one-line `IncQuarantinedFiles` stub added by prompt 1, plus the new test cases
- [ ] `resolveConflictMerge` calls `g.resolver.Resolve(ctx, []string{path})` per path (NOT `g.resolver.Resolve(ctx, conflictPaths)` for the whole batch)
- [ ] `_conflicts/` is created via `os.MkdirAll` with mode `0o750` (matches existing `os.MkdirAll` calls in `git.go` line 263)
- [ ] The commit message uses the EXACT format `merge: resolved=[<sorted-paths>] quarantined=[<sorted-paths>]` — no spaces in the comma-separated lists; empty lists render as `[]`
- [ ] `g.metrics.IncQuarantinedFiles()` is called EXACTLY once per successful quarantine (not per merge, not per path attempted)
- [ ] `g.metrics.IncResolverFailure("git_mv_failed")` is called for each `git mv` failure
- [ ] `g.metrics.IncResolverFailure("unsafe_path")` is called for each unsafe-path rejection
- [ ] `g.metrics.IncMergeOutcome("aborted")` fires only on the three abort paths: `_conflicts/` is a file, all-fail-quarantine, and unsafe-path
- [ ] `g.metrics.IncMergeOutcome("resolved")` fires only after a successful commit
- [ ] `g.currentDateTimeGetter.Now().Unix()` is called ONCE at the start of the per-file loop
- [ ] `CHANGELOG.md` has exactly one `## Unreleased` heading (added by prompt 1; this prompt appends to it, does not create a new one)
- [ ] No deliberation prose in this prompt body (no "Wait —", "Decision:", etc.)
- [ ] No invented helpers: `quarantineDestPath`, `unsafeConflictPath`, and the two failure-category constants are the only new symbols; all are private to `git.go`
- [ ] String values of `quarantineFailureUnsafePath` (`"unsafe_path"`) and `quarantineFailureGitMv` (`"git_mv_failed"`) EXACTLY match the pre-init label values in `pkg/metrics/metrics.go init()` (added by prompt 1). A typo or drift orphans the pre-init and the live counter starts unregistered. Verify: `grep -n '"unsafe_path"\|"git_mv_failed"' pkg/metrics/metrics.go pkg/git/git.go` shows the SAME string literals in both files.
- [ ] `make precommit` exits 0 from the repo root
- [ ] No `context.Background()` introduced; all `ctx` propagation preserved
- [ ] No `fmt.Errorf`; all error wrapping uses `github.com/bborbe/errors`
- [ ] No `t.Run` tables; all new tests are Ginkgo `It` blocks
- [ ] The 1-corrupt-of-11 happy-path test and pathological case test live in the existing `pkg/git/git_test.go` (no new test file for those); the unsafe-path test lives in the new internal test file `pkg/git/resolve_conflict_merge_test.go` (in `package git`, NOT `package git_test`)
</self-review>
