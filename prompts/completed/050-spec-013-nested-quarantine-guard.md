---
status: completed
spec: [013-quarantine-nesting-and-drain]
summary: Added a nested-path pre-flight guard that aborts a merge whose conflict list contains an already-quarantined path, made quarantineDestPath always produce exactly one _conflicts/ level, and added the nested_source resolver-failure category with tests and a CHANGELOG entry.
execution_id: git-rest-quarantine-drain-exec-050-spec-013-nested-quarantine-guard
dark-factory-version: dev
created: "2026-09-13T21:20:00Z"
queued: "2026-09-14T06:08:34Z"
started: "2026-09-14T06:08:36Z"
completed: "2026-09-14T06:12:41Z"
branch: dark-factory/quarantine-nesting-and-drain
---

# Reject nested re-quarantine in the conflict-merge path

<summary>
- A file that already lives under `_conflicts/` can no longer be quarantined a second time
- The quarantine destination builder always produces exactly one `_conflicts/` level, whatever the source path looks like
- A merge whose conflict list contains an already-quarantined path is rejected before anything is written to disk
- The rejected merge aborts, the pull reports a conflict-resolution failure, and the worktree returns to its pre-merge state
- The rejected file is neither moved nor staged — it stays byte-identical where it was
- Operators see one WARN log line that names the nested path and says why it was rejected
- A new failure category makes the rejection countable, so an operator can see how often the guard fires
- A merge that resolves cleanly and contains no nested path behaves exactly as before
- The conflict-resolver interface, its generated mock, and the merge commit-message format are untouched
</summary>

<objective>
Stop the quarantine flow from nesting `_conflicts/` one level deeper on every retry. `quarantineDestPath` must map any source — including a source that already starts with `_conflicts/` — to a destination with exactly one `_conflicts/` level, and a new pre-flight guard must reject a merge whose conflict list contains an already-quarantined path: one `nested_source` counter increment, one WARN naming the path, `git merge --abort`, and a wrapped `ErrConflictResolutionFailed`, so the file stays untouched and the pod surfaces the conflict instead of silently deepening the tree.
</objective>

<context>
This repo has no `CLAUDE.md`; project conventions live in `docs/dod.md` (definition of done), `docs/deployment.md`, and the coding-plugin guides below.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `errors.Wrap` / `errors.Wrapf` from `github.com/bborbe/errors`; sentinel errors; never `fmt.Errorf`, never bare `return err`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega; external test package (`package git_test`); internal test package for unexported helpers; table tests
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-test-types-guide.md` — integration tests use in-process real `git` via `os/exec` against a temp working tree, no network
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md` — `slog.WarnContext` for conditional warnings
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` — pre-initialisation requirement for labelled metrics; label values are registered once in `init()`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` heading; `feat:` / `fix:` prefix rules

Files to read in full before editing (line numbers are hints; anchor by function name):

- `pkg/git/git.go` — `quarantineDestPath` (~261), `unsafeConflictPath` (~287), the category const block (~56-61), `resolveConflictMerge` (~604), `resolveConflictPaths` (~633, note its ordering-invariant doc comment), `validateConflictPathsSafe` (~673, the structural model for the new guard), `resolveEachPath` (~706), `ensureConflictsDir` (~781), `quarantineOne` (~835), `Pull` (~1057)
- `pkg/git/resolve_conflict_merge_test.go` — `unsafeTestMetrics` (~49), `TestQuarantineDestPath` (~227, the table test the new cases extend)
- `pkg/git/git_test.go` — `noopMetrics` (~38), `initRepo` (~57), `setupPullFixture` (~687), `setupQuarantineFixture` (~775, and its two existing callers at ~1102 and ~1220), `gatherQuarantinedFiles` (~852), `captureSlogLogs` (~1278), and the existing spec-012 quarantine Context blocks (~1028-1267) that the new specs sit beside
- `pkg/metrics/metrics.go` — `ResolverFailuresTotal` (~48), `init()` (~61), the `Metrics` interface (~108)
- `pkg/metrics/metrics_test.go` — `gatherCounterVecLabelValue` (~34) and the `quarantine_io_failed` pre-init spec (~70)
- `CHANGELOG.md` — the top of the file currently starts with `## v0.25.4`; there is no `## Unreleased` section yet
- `docs/dod.md` — the daemon's validation prompt (definition of done); its `## Documentation` bullet is why the `## Unreleased` entry is mandatory

**Verified facts about the current code (do not re-derive, but re-read before editing):**

- `quarantineDestPath(path string, unixSeconds int64) string` joins `filepath.Join("_conflicts", dir, base)` with no guard for a source that already starts with `_conflicts/` — that is the nesting defect.
- `validateConflictPathsSafe(ctx context.Context, conflictPaths []string) error` is the pre-flight model: it loops the paths, on a rejection increments a `git_rest_resolver_failures_total` category via `g.metrics.IncResolverFailure(...)`, emits one `slog.WarnContext` with `path` and `reason` attributes, runs `_, _ = g.runCmdRaw(ctx, g.repoPath, "merge", "--abort")`, records `g.metrics.IncMergeOutcome("aborted")`, and returns `errors.Wrap(ctx, ErrConflictResolutionFailed, "...")`.
- `resolveConflictPaths` runs `validateConflictPathsSafe` FIRST and `ensureConflictsDir` second; the doc comment states the ordering invariant explicitly.
- The category constants in `pkg/git/git.go` are `quarantineFailureUnsafePath = "unsafe_path"` and `quarantineFailureIO = "quarantine_io_failed"`.
- `stderrors "errors"` is already the alias for the standard library in `pkg/git/git.go`; `strings`, `os`, `path/filepath`, `log/slog` are already imported.
- `pkg/git/resolve_conflict_merge_test.go` is `package git` (internal) and does NOT import `strings` yet.
- `pkg/git/git_test.go` is `package git_test` and already imports `errors`, `os`, `path/filepath`, `strings`, `log/slog`, `github.com/prometheus/client_golang/prometheus`, `github.com/bborbe/git-rest/mocks`, `github.com/bborbe/git-rest/pkg/git`, `github.com/bborbe/git-rest/pkg/metrics`, and `libtime "github.com/bborbe/time"`.

**Environment fact:** the container's `.git` is masked (`hideGit`), so branch-relative `git log` / `git diff` checks are not runnable here. Keep the frozen files unchanged by inspection and by the content assertions in `<verification>`; the branch-level frozen-surface check belongs to the spec's verification ladder.

**Sibling prompts (do not do their work):** prompt 2 adds the `git_rest_quarantined_backlog` gauge and the per-pull refresh; prompt 3 adds the per-vault alert; prompt 4 refreshes the docs. This prompt only fixes the tree shape, adds the guard, and adds the `nested_source` category.
</context>

<requirements>

## 1. Define the quarantine directory name once, and make `quarantineDestPath` total

In `pkg/git/git.go`, add a package-level constant just above `quarantineDestPath`:

```go
// conflictsDirName is the repo-root directory quarantined files are moved into.
// Quarantine mirrors the source path one level under it and never nests deeper.
const conflictsDirName = "_conflicts"
```

Replace `quarantineDestPath` with the version below. The only behavioural change is the prefix strip at the top: every leading `_conflicts/` segment (and a bare `_conflicts`) is removed before the destination is built, so the result always carries exactly one `_conflicts/` level. The `.md` timestamp insertion and the `.quarantined` suffix rule are unchanged.

```go
// quarantineDestPath builds the destination path for a quarantined file.
// For paths ending in ".md", the timestamp is inserted before the final ".md"
// and the directory tree under "_conflicts/" mirrors the original path
// (e.g. "dir/b.md" -> "_conflicts/dir/b.md.<ts>.md"). For non-".md" paths
// the timestamp is appended with a ".quarantined" suffix
// (e.g. "foo.bin" -> "_conflicts/foo.bin.<ts>.quarantined"). A source that
// already lives under "_conflicts/" (a re-quarantine, or residue left by an
// older binary) keeps exactly one "_conflicts/" level: the leading segment is
// stripped before the destination is built, so the destination is never nested
// deeper than one level (e.g. "_conflicts/dir/b.md" ->
// "_conflicts/dir/b.md.<ts>.md"). The repoPath is not included; the caller is
// expected to join it with the repo root.
func quarantineDestPath(path string, unixSeconds int64) string {
	ts := strconv.FormatInt(unixSeconds, 10)
	rel := path
	for rel == conflictsDirName || strings.HasPrefix(rel, conflictsDirName+"/") {
		rel = strings.TrimPrefix(strings.TrimPrefix(rel, conflictsDirName), "/")
	}
	dir := filepath.Dir(rel)
	base := filepath.Base(rel)
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
		return filepath.Join(conflictsDirName, base)
	}
	return filepath.Join(conflictsDirName, dir, base)
}
```

The loop must terminate: for `rel == "_conflicts"` the two `TrimPrefix` calls leave `""`, which fails both loop conditions. Do not replace it with a single `strings.TrimPrefix` — one strip is not enough for a doubly nested input.

Replace the local `conflictsDirRel := "_conflicts"` inside `ensureConflictsDir` with `conflictsDirName` so the directory name is defined once. Behaviour of `ensureConflictsDir` must not change.

## 2. Add the nested-path lookup and the rejecting guard

Add these two functions to `pkg/git/git.go`, directly below `unsafeConflictPath`:

```go
// nestedConflictPath returns the first path in paths that already lives under the
// _conflicts/ quarantine directory, or "" when no path does. Quarantining such a
// path again would nest the tree one level deeper on every retry.
func nestedConflictPath(paths []string) string {
	prefix := conflictsDirName + "/"
	for _, path := range paths {
		if path == conflictsDirName || strings.HasPrefix(path, prefix) {
			return path
		}
	}
	return ""
}

// validateConflictPathsNotNested pre-flights the conflict path list before any disk
// I/O. A conflicted path that already lives under _conflicts/ is rejected: the file
// stays exactly one level deep, the nested_source counter records the rejection, and
// the merge is aborted. Returns wrapped ErrConflictResolutionFailed when a nested path
// is present, nil otherwise. Pure read of the path list; no file writes.
func (g *git) validateConflictPathsNotNested(
	ctx context.Context,
	conflictPaths []string,
) error {
	path := nestedConflictPath(conflictPaths)
	if path == "" {
		return nil
	}
	g.metrics.IncResolverFailure(quarantineFailureNested)
	slog.WarnContext(
		ctx,
		"git-rest: nested conflicted path already under _conflicts/ rejected; aborting merge",
		"path",
		path,
		"reason",
		"path already under _conflicts/; re-quarantining would nest the tree one level deeper",
	)
	_, _ = g.runCmdRaw(ctx, g.repoPath, "merge", "--abort")
	g.metrics.IncMergeOutcome("aborted")
	return errors.Wrap(
		ctx,
		ErrConflictResolutionFailed,
		"nested conflict path already quarantined",
	)
}
```

The WARN message must contain the substring `nested` and the log record must carry the nested path in the `path` attribute and the explanation in the `reason` attribute. The counter increment, the WARN, and the abort each happen exactly once per rejected merge, on the FIRST nested path found.

## 3. Add the `nested_source` category constant

Extend the existing const block at the top of `pkg/git/git.go` (the one holding `quarantineFailureUnsafePath` and `quarantineFailureIO`):

```go
	quarantineFailureNested     = "nested_source"
```

Keep the two existing constants byte-identical.

## 4. Wire the guard into `resolveConflictPaths`

Call the new guard in the same ordering slot as `validateConflictPathsSafe`, before `ensureConflictsDir` touches disk:

```go
	// Pre-flight: validate ALL paths are safe BEFORE any disk I/O.
	// An unsafe path aborts immediately; ensureConflictsDir must NOT
	// create _conflicts/ on the abort path (caught by code review 2026-06-02).
	if err := g.validateConflictPathsSafe(ctx, conflictPaths); err != nil {
		return err
	}
	// Pre-flight: a conflicted path that already lives under _conflicts/ aborts the
	// merge before any disk I/O, so a re-quarantine can never deepen the tree.
	if err := g.validateConflictPathsNotNested(ctx, conflictPaths); err != nil {
		return err
	}
	if err := g.ensureConflictsDir(ctx); err != nil {
		return err
	}
```

Extend the ordering-invariant doc comment on `resolveConflictPaths` to name both pre-flights and to state that both MUST run before `ensureConflictsDir`.

Do NOT add a nested-path check inside `quarantineOne` or inside the per-file loop — the guard is a whole-list pre-flight; `quarantineDestPath` is correct on its own for the nested input.

## 5. Document and pre-initialise the `nested_source` category in `pkg/metrics/metrics.go`

- Add `"nested_source"` to the `for _, category := range []string{...}` pre-init slice in `init()` (the slice currently ends with `"quarantine_io_failed"`). Do not remove or reorder the existing values.
- Extend the `ResolverFailuresTotal` doc comment so its category list reads: `yaml_parse_failed, no_frontmatter, write_failed, git_add_failed, quarantine_io_failed, unsafe_path, nested_source`.
- Extend the `Help` string on `ResolverFailuresTotal` so the quarantine-failure clause reads: `Quarantine failures: quarantine_io_failed (any I/O step of the quarantine flow), unsafe_path (path-traversal rejection), nested_source (a conflicted path already under _conflicts/ was rejected by the nesting guard).`
- Extend the `IncResolverFailure` doc comment on the `Metrics` interface so its allowed-category list also names `nested_source`. Do not change any method signature in this prompt.

## 6. Extend the pure-function tests in `pkg/git/resolve_conflict_merge_test.go`

Add the `strings` import to this file (it is not imported yet).

(a) Extend the existing `TestQuarantineDestPath` table with these cases (same `const ts int64 = 1700000000`):

```go
		{
			"md already under _conflicts at repo root",
			"_conflicts/note.md",
			filepath.Join("_conflicts", "note.1700000000.md"),
		},
		{
			"md already under _conflicts in nested dir",
			"_conflicts/tasks/build/note.md",
			filepath.Join("_conflicts", "tasks/build", "note.1700000000.md"),
		},
		{
			"doubly nested md collapses to one level",
			"_conflicts/_conflicts/note.md",
			filepath.Join("_conflicts", "note.1700000000.md"),
		},
		{
			"non-md already under _conflicts",
			"_conflicts/foo.bin",
			filepath.Join("_conflicts", "foo.bin.1700000000.quarantined"),
		},
```

(b) Add a test asserting the one-level property for both inputs named in the spec:

```go
// TestQuarantineDestPathSingleConflictsSegment asserts the spec's destination
// contract: whatever the input, the destination carries exactly one "_conflicts/"
// segment. Both a nested input and an ordinary input are checked, so the
// normalization is total rather than special-cased.
func TestQuarantineDestPathSingleConflictsSegment(t *testing.T) {
	const ts int64 = 1700000000
	for _, path := range []string{"_conflicts/dir/b.md", "dir/b.md", "_conflicts/_conflicts/b.md"} {
		got := quarantineDestPath(path, ts)
		if n := strings.Count(got, "_conflicts/"); n != 1 {
			t.Errorf(
				"quarantineDestPath(%q) = %q carries %d \"_conflicts/\" segments, want 1",
				path, got, n,
			)
		}
	}
}
```

(c) Add a table test for the lookup helper:

```go
// TestNestedConflictPath covers the prefix rule that decides whether a conflicted
// path is already quarantined. The "_conflicts-archive/a.md" case pins the
// boundary: only the exact "_conflicts" directory and its children count.
func TestNestedConflictPath(t *testing.T) {
	cases := []struct {
		name  string
		paths []string
		want  string
	}{
		{"empty list", nil, ""},
		{"ordinary path", []string{"notes/a.md"}, ""},
		{"sibling dir with the same prefix", []string{"_conflicts-archive/a.md"}, ""},
		{"nested path at the root", []string{"_conflicts/a.md"}, "_conflicts/a.md"},
		{"nested path in a subdir", []string{"_conflicts/25 Tasks/Prev A.md"}, "_conflicts/25 Tasks/Prev A.md"},
		{"bare _conflicts", []string{"_conflicts"}, "_conflicts"},
		{
			"first nested path wins",
			[]string{"notes/a.md", "_conflicts/b.md", "_conflicts/c.md"},
			"_conflicts/b.md",
		},
	}
	for _, tc := range cases {
		if got := nestedConflictPath(tc.paths); got != tc.want {
			t.Errorf("%s: nestedConflictPath(%v) = %q, want %q", tc.name, tc.paths, got, tc.want)
		}
	}
}
```

## 7. Add the merge-level specs in `pkg/git/git_test.go`

These specs need the process-global registry, so they construct the real `metrics.NewMetrics()` instead of `mocks.FakeMetrics` — a gather assertion cannot be backed by the counterfeiter fake.

(a) Add a gather helper next to `gatherQuarantinedFiles`:

```go
// hasResolverFailureSeries reports whether
// git_rest_resolver_failures_total{category=<category>} is present in the
// process-global registry at all. gatherResolverFailure returns 0 both for a
// series that is absent and for one that is present with value 0, so the
// pre-initialisation assertion in AC4 needs this separate presence check —
// without it, an implementation that forgets the init() slice entry passes.
func hasResolverFailureSeries(category string) bool {
	mfs, err := prometheus.DefaultGatherer.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, mf := range mfs {
		if mf.GetName() != "git_rest_resolver_failures_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "category" && l.GetValue() == category {
					return true
				}
			}
		}
	}
	return false
}

// gatherResolverFailure returns the current value of
// git_rest_resolver_failures_total{category=<category>} from the process-global
// registry. Returns 0 when the series is absent. Used to assert a delta around a
// single Pull, because sibling specs in the same binary also increment the counter.
func gatherResolverFailure(category string) float64 {
	mfs, err := prometheus.DefaultGatherer.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, mf := range mfs {
		if mf.GetName() != "git_rest_resolver_failures_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "category" && l.GetValue() == category {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}
```

(b) Add a fixture helper that seeds explicit repo-relative paths. `setupQuarantineFixture(fileCount int)` only seeds root-level `note-NN.md` names, so it cannot produce a conflict on a path under `_conflicts/`. Leave `setupQuarantineFixture` and its two existing callers unchanged.

```go
// setupQuarantineFixturePaths is the explicit-seed sibling of
// setupQuarantineFixture: it seeds the given repo-relative paths (creating parent
// directories) on a fresh repo backed by a local bare remote, then returns the same
// closure pair — localEdit commits a divergent version of one path locally,
// externalPush pushes a divergent version of one path from a temp clone. Use both
// closures on the same path to create a real merge conflict.
func setupQuarantineFixturePaths(seedPaths []string) (
	workDir string,
	localEdit func(file, content string),
	externalPush func(file, content string),
	cleanup func(),
) {
	remoteDir, err := os.MkdirTemp("", "git-remote-nested-*")
	Expect(err).NotTo(HaveOccurred())
	workDir, err = os.MkdirTemp("", "git-work-nested-*")
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

	for _, p := range seedPaths {
		abs := filepath.Join(workDir, p)
		Expect(os.MkdirAll(filepath.Dir(abs), 0o750)).To(Succeed())
		// 0o644, not 0o600: git rewrites a conflicted file with the index mode
		// (100644) during the merge, and `merge --abort` leaves it there — so a
		// 0o600 seed makes the post-abort mode assertion in the AC2 spec fail at
		// the container's umask 022 (it would pass at 077, where both sides land
		// on 0600, which is exactly why the seed must not depend on the umask).
		Expect(os.WriteFile(abs, []byte("---\nshared: base\n---\nbase body\n"), 0o644)).To(Succeed())
		rg(workDir, "add", "--", p)
	}
	rg(workDir, "commit", "-q", "-m", "seed")
	rg(workDir, "push", "-u", "origin", "main")

	localEdit = func(file, content string) {
		abs := filepath.Join(workDir, file)
		// Mode is ignored on an existing file; 0o644 keeps the fixture uniform so
		// no reader mistakes 0o600 for something the mode assertion depends on.
		Expect(os.WriteFile(abs, []byte(content), 0o644)).To(Succeed())
		rg(workDir, "add", "--", file)
		rg(workDir, "commit", "-q", "-m", "local: "+file)
	}

	externalPush = func(file, content string) {
		extDir, err := os.MkdirTemp("", "git-ext-nested-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = os.RemoveAll(extDir) }()
		rg(extDir, "clone", remoteDir, ".")
		rg(extDir, "config", "user.email", "ext@example.com")
		rg(extDir, "config", "user.name", "External")
		abs := filepath.Join(extDir, file)
		Expect(os.WriteFile(abs, []byte(content), 0o644)).To(Succeed())
		rg(extDir, "add", "--", file)
		rg(extDir, "commit", "-q", "-m", "external: "+file)
		rg(extDir, "push", "origin", "main")
	}

	cleanup = func() {
		_ = os.RemoveAll(workDir)
		_ = os.RemoveAll(remoteDir)
	}
	return workDir, localEdit, externalPush, cleanup
}
```

(c) Add a new top-level `Describe` at the end of `pkg/git/git_test.go` (do not nest it inside the existing `Pull state machine` Describe, whose `BeforeEach` builds an unrelated fixture):

```go
var _ = Describe("Pull nested quarantine guard", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	Context("a conflicted path already under _conflicts/ is the only conflict", func() {
		const nestedPath = "_conflicts/25 Tasks/Prev A.md"

		It("AC2: Pull fails with ErrConflictResolutionFailed and leaves the file one level deep", func() {
			workDir, localEdit, externalPush, cleanup := setupQuarantineFixturePaths(
				[]string{nestedPath},
			)
			defer cleanup()

			absNested := filepath.Join(workDir, nestedPath)

			// Diverge both sides of the quarantined path: the remote side carries
			// invalid YAML frontmatter (the trigger the defect was observed with),
			// the local side a valid change.
			externalPush(nestedPath, "---\ntitle: [unclosed\n---\nREMOTE CHANGE\n")
			localEdit(nestedPath, "---\ntitle: a\n---\nLOCAL CHANGE\n")

			// Baseline immediately before the pull: localEdit has already moved the
			// source off the seed content, and `git merge --abort` restores the tree
			// to HEAD (the local side). A baseline read any earlier compares against
			// the seed bytes and fails on a correct implementation.
			contentBefore, readErr := os.ReadFile(absNested)
			Expect(readErr).NotTo(HaveOccurred())
			statBefore, statErr := os.Stat(absNested)
			Expect(statErr).NotTo(HaveOccurred())

			pg := git.New(
				workDir,
				metrics.NewMetrics(),
				libtime.NewCurrentDateTime(),
				"",
				git.NewYAMLMergeResolver(workDir, metrics.NewMetrics()),
			)

			logs, restore := captureSlogLogs()
			defer restore()

			// AC4, presence half: the category must be registered at init() with value
			// 0, not merely have a value of 0. An implementation that adds the const
			// and the doc text but forgets the pre-init slice entry is caught here and
			// nowhere else.
			Expect(hasResolverFailureSeries("nested_source")).To(BeTrue(),
				"nested_source must be pre-initialised in init() so the series exists before any rejection")

			beforeNested := gatherResolverFailure("nested_source")
			beforeQuarantined := gatherQuarantinedFiles()

			err := pg.Pull(ctx)
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue(),
				"expected wrapped ErrConflictResolutionFailed, got: %v", err)

			// AC4: the rejection is counted exactly once. Delta, not absolute: the
			// counter lives in the process-global registry and sibling specs in this
			// binary increment it too.
			afterNested := gatherResolverFailure("nested_source")
			Expect(afterNested - beforeNested).To(Equal(1.0),
				"nested_source must increment by exactly 1")

			// AC6: a rejected re-quarantine records no quarantine event.
			afterQuarantined := gatherQuarantinedFiles()
			Expect(afterQuarantined - beforeQuarantined).To(Equal(0.0),
				"a rejected re-quarantine must not increment git_rest_quarantined_files_total")

			// Negative evidence: no second _conflicts/ level was created.
			_, statErr = os.Stat(filepath.Join(workDir, "_conflicts", "_conflicts"))
			Expect(os.IsNotExist(statErr)).To(BeTrue(),
				"_conflicts/_conflicts must not exist after a rejected re-quarantine")

			// The source is untouched: same content, same mode, not staged.
			contentAfter, readErr := os.ReadFile(absNested)
			Expect(readErr).NotTo(HaveOccurred())
			Expect(contentAfter).To(Equal(contentBefore))
			statAfter, statErr := os.Stat(absNested)
			Expect(statErr).NotTo(HaveOccurred())
			Expect(statAfter.Mode()).To(Equal(statBefore.Mode()))
			Expect(gitOutputStr(workDir, "diff", "--cached", "--name-only")).
				NotTo(ContainSubstring(nestedPath))

			// AC5: one WARN naming the nested source path.
			logStr := logs.String()
			Expect(logStr).To(ContainSubstring("level=WARN"))
			Expect(logStr).To(ContainSubstring("nested"))
			Expect(logStr).To(ContainSubstring(nestedPath))
		})
	})

	Context("a merge contains a nested path and an ordinary resolvable path", func() {
		const (
			nestedPath   = "_conflicts/25 Tasks/Prev A.md"
			ordinaryPath = "notes/plain.md"
		)

		It("AC3: the whole merge aborts, the worktree is restored and no commit is created", func() {
			workDir, localEdit, externalPush, cleanup := setupQuarantineFixturePaths(
				[]string{nestedPath, ordinaryPath},
			)
			defer cleanup()

			externalPush(nestedPath, "---\ntitle: [unclosed\n---\nREMOTE CHANGE\n")
			externalPush(ordinaryPath, "---\nshared: remote\n---\nremote body\n")
			localEdit(nestedPath, "---\ntitle: a\n---\nLOCAL CHANGE\n")
			localEdit(ordinaryPath, "---\nshared: local\n---\nlocal body\n")

			// Baseline immediately before the pull: the two localEdit calls above each
			// create a commit, and `git merge --abort` restores HEAD to the local tip.
			// A baseline captured earlier compares against the seed commit and fails on
			// a correct implementation.
			headBefore := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "HEAD"))

			pg := git.New(
				workDir,
				metrics.NewMetrics(),
				libtime.NewCurrentDateTime(),
				"",
				git.NewYAMLMergeResolver(workDir, metrics.NewMetrics()),
			)

			err := pg.Pull(ctx)
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue(),
				"expected wrapped ErrConflictResolutionFailed, got: %v", err)

			Expect(strings.TrimSpace(gitOutputStr(workDir, "status", "--porcelain"))).
				To(BeEmpty(), "the abort must restore the worktree")
			Expect(strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "HEAD"))).
				To(Equal(headBefore), "the aborted merge must not create a commit")
		})
	})
})
```

## 8. Add the `nested_source` pre-init spec in `pkg/metrics/metrics_test.go`

Mirror the existing `quarantine_io_failed` spec:

```go
var _ = Describe("ResolverFailuresTotal nested_source label", func() {
	It("is pre-initialised to 0 alongside the existing label values", func() {
		Expect(gatherCounterVecLabelValue(
			"git_rest_resolver_failures_total", "category", "nested_source",
		)).To(Equal(0.0))
	})
})
```

## 9. CHANGELOG

Add a `## Unreleased` section directly under the SemVer preamble (the bullet list that ends above `## v0.25.4`) and above the existing `## v0.25.4` heading. If a `## Unreleased` section already exists, append to it instead — there must be exactly one such heading. The header block at the top of the file stays untouched.

```markdown
- fix: Reject a re-quarantine of a conflicted path that already lives under `_conflicts/`. The quarantine destination builder now strips every leading `_conflicts/` segment, so it always produces exactly one `_conflicts/` level, and a new pre-flight guard rejects a merge whose conflict list contains an already-quarantined path — the file is left untouched, the merge aborts with `ErrConflictResolutionFailed`, a WARN names the nested path, and `git_rest_resolver_failures_total{category="nested_source"}` records the rejection. Fixes the 2026-09-13 Personal vault chain where an 11:08 run quarantined a file that had already been quarantined at 11:07, producing `_conflicts/_conflicts/...` with its conflict markers still embedded.
```

## 10. Final verification

Run every command in `<verification>` from the repo root and confirm each result before finishing.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT modify `pkg/git/conflict_resolver.go`, `mocks/conflict_resolver.go`, or `pkg/git/yaml_merge_resolver.go` — they are frozen by the spec. Do not change the `ConflictResolver` interface, `MarkerResolver`, or `YAMLMergeResolver` internals.
- Do NOT change the `merge: resolved=[…] quarantined=[…]` commit-message format established by spec 012.
- Do NOT add a per-repo label to any metric — one pod serves one repo, so the label would have cardinality 1.
- Do NOT add a quarantine-size cap, retention policy, auto-repair, auto-restore, or auto-delete of quarantined content.
- Do NOT suppress the abort for an all-rejected merge: a guard-rejected nested file must not be called "handled" so the merge could commit.
- Do NOT add a "disable quarantine" flag, per-repo override, or tunable threshold.
- Do NOT add a new Prometheus metric in this prompt — the backlog gauge belongs to prompt 2. This prompt adds only the `nested_source` label value.
- Do NOT add the nested-path check inside `quarantineOne`, `resolveEachPath`, or the per-file loop. The guard is a whole-list pre-flight; `quarantineDestPath` is correct on its own.
- `ErrConflictResolutionFailed` is the ONLY error the new guard returns; it MUST be wrapped via `errors.Wrap(ctx, ErrConflictResolutionFailed, "...")` so `errors.Is` traverses the chain.
- Errors MUST be wrapped with `errors.Wrap` / `errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf`, never a bare `return err`.
- Logging MUST use `log/slog` (`slog.WarnContext`), matching the existing quarantine call sites. Never `context.Background()` in `pkg/`.
- The guard and the helper are unexported. Pure-function cases live in the internal `package git` test file (`pkg/git/resolve_conflict_merge_test.go`) and call the helpers directly, matching the existing `TestQuarantineDestPath`. The merge-level specs live in `pkg/git/git_test.go` (Ginkgo v2 + Gomega, `package git_test`) and extend the existing quarantine coverage rather than replacing it.
- The merge-level specs MUST construct the real `metrics.NewMetrics()` — `mocks.FakeMetrics` cannot back a `prometheus.DefaultGatherer` assertion.
- `pkg/git/conflict_resolver_test.go` and `pkg/git/yaml_merge_resolver_test.go` stay byte-identical. `pkg/git/git_test.go` is extended; its existing specs must keep passing and `noopMetrics` needs no change in this prompt.
- The merge-level specs use real `git` via `os/exec` against a temp working tree with no network.
- Existing tests must still pass; `make precommit` from the repo root must exit 0.
- The container's `.git` is masked: do not run `git diff`, `git log`, or `git status` against the project repo as verification, and do not attempt to commit.
- The `## Unreleased` CHANGELOG section MUST be the only one; append to it if it already exists instead of creating a second heading.
- This prompt is the first of four: prompt 2 adds the backlog gauge, prompt 3 the alert, prompt 4 the docs. Do not touch `helm/`, `docs/deployment.md`, or `docs/verifying-specs.md` in this prompt, and do not add or change any *method* on the `Metrics` interface — the only permitted interface edit here is the `IncResolverFailure` doc-comment addition in requirement 5 (the backlog setter belongs to prompt 2).
</constraints>

<verification>
Run from the repo root.

```bash
make precommit
```
Must exit 0.

Targeted test runs:

```bash
go test ./pkg/git/... -run 'TestQuarantineDestPath|TestQuarantineDestPathSingleConflictsSegment|TestNestedConflictPath' -v
go test ./pkg/git/... -ginkgo.focus='nested quarantine guard'
go test ./pkg/metrics/... -ginkgo.focus='nested_source'
```
All pass.

Structural checks (each `grep` must print at least one match):

```bash
grep -n 'nested_source' pkg/metrics/metrics.go                     # pre-init slice + doc comments
grep -n 'nested_source' pkg/git/git.go                             # the category constant
grep -n 'conflictsDirName' pkg/git/git.go                          # ≥3 matches: const, quarantineDestPath, nestedConflictPath, ensureConflictsDir
grep -n 'validateConflictPathsNotNested' pkg/git/git.go            # ≥2 matches: definition + call site in resolveConflictPaths
grep -n 'nestedConflictPath' pkg/git/git.go                        # ≥2 matches: definition + use in the guard
grep -n 'nested' pkg/git/git.go                                    # the WARN message and the guard names
```

Guard ordering check — the nested guard call must sit between `validateConflictPathsSafe` and `ensureConflictsDir` in `resolveConflictPaths`:

```bash
grep -n 'validateConflictPathsSafe\|validateConflictPathsNotNested\|ensureConflictsDir' pkg/git/git.go
```
Inside `resolveConflictPaths` the call sites appear in this order: `validateConflictPathsSafe`, then `validateConflictPathsNotNested`, then `ensureConflictsDir`. Definition occurrences elsewhere in the file are expected and fine — the new guard's definition sits below `unsafeConflictPath` (so it is printed before the first call site), and `ensureConflictsDir`'s definition is further down.

Frozen-surface check — the two frozen files must still carry their original interface surface:

```bash
grep -c 'Resolve(ctx context.Context, conflictedPaths \[\]string) error' pkg/git/conflict_resolver.go
grep -c 'counterfeiter:generate -o ../../mocks/conflict_resolver.go' pkg/git/conflict_resolver.go
grep -c 'func (fake \*FakeConflictResolver)' mocks/conflict_resolver.go
```
The first prints `2` (the interface declaration plus the `markerResolver` method), the second prints `1`, the third prints `8` (Resolve, ResolveCallCount, ResolveCalls, ResolveArgsForCall, ResolveReturns, ResolveReturnsOnCall, Invocations, recordInvocation). Confirm these counts are unchanged after your edits.

CHANGELOG checks:

```bash
grep -c '^## Unreleased' CHANGELOG.md          # must print 1
grep -n 'nested_source' CHANGELOG.md           # ≥1 match
grep -n '^## v0.25.4' CHANGELOG.md             # the existing release heading is still present, below ## Unreleased
```

Self-check before finishing: re-run `make precommit`, then walk each requirement above against the change — the strip loop terminates for every input, the guard runs before `ensureConflictsDir`, the counter increments exactly once per rejected merge, the WARN contains `nested` and the path, the file is untouched, and no existing spec changed behaviour.
</verification>
