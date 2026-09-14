---
status: approved
spec: [013-quarantine-nesting-and-drain]
created: "2026-09-13T21:20:00Z"
queued: "2026-09-14T06:08:34Z"
branch: dark-factory/quarantine-nesting-and-drain
---

# Expose the live `_conflicts/` backlog as a gauge

<summary>
- Operators can see how many files are currently sitting in `_conflicts/` on every pod
- The number is a gauge, so removing a quarantined file counts the backlog down instead of only ever going up
- The count is refreshed once per pull cycle, so a deletion is reflected on the next tick even when the pull itself is a no-op
- Files left nested by an older binary are counted too, so legacy residue cannot hide
- A repository that has never quarantined anything still exposes the series as zero on `/metrics`
- A missing `_conflicts/` directory reads as zero; an unreadable one reads as zero and logs a warning, and neither can fail a pull
- The metric surface gains exactly one method, and it is a setter rather than an increment
- The existing lifetime quarantine counter keeps its current meaning and is not reused for backlog
- The repository directory walk lives with the git code, so the metrics package stays free of filesystem access
</summary>

<objective>
Add a `git_rest_quarantined_backlog` gauge that reports the number of regular files currently under `_conflicts/` in the served repo, refreshed once per pull cycle from `pkg/git`. Quarantine is a fail-safe with no drain step today — the only related series is a monotonic counter over lifetime events, which cannot express backlog. Making the live size visible is what bounds accumulation: an operator learns a file was set aside without going to look for it.
</objective>

<context>
This repo has no `CLAUDE.md`; project conventions live in `docs/dod.md` (definition of done), `docs/deployment.md`, and the coding-plugin guides below.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` — the Counter-vs-Gauge rule ("can the value decrease?" → Gauge); pre-initialisation in `init()`; the composed-metrics-interface rule
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega; external test packages; gathering from `prometheus.DefaultGatherer`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `errors.Wrap` / `errors.Wrapf` from `github.com/bborbe/errors`; never `fmt.Errorf`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` heading; `feat:` prefix rules

Files to read in full before editing (line numbers are hints; anchor by function name):

- `pkg/metrics/metrics.go` — `QuarantinedFilesTotal` (~43, the sibling declaration to model the gauge on), `init()` (~61, the register call and the pre-init block with the `QuarantinedFilesTotal.Add(0)` comment), the `Metrics` interface (~108) and its `*prometheusMetrics` implementation (~133)
- `pkg/metrics/metrics_test.go` — `gatherCounter` (~18), `gatherCounterVecLabelValue` (~34), the existing Describe blocks
- `pkg/git/git.go` — `Pull` (~1057, the pull cycle the refresh hooks into), `quarantineDestPath` (~261) and the `conflictsDirName` constant above it, `resolveConflictPaths` (~633), `ensureConflictsDir` (~781, the existing `_conflicts` stat/mkdir precedent)
- `pkg/git/git_test.go` — `initRepo` (~57, the fixture the gauge specs use), `gatherQuarantinedFiles` (~847), `captureSlogLogs` (~1276), `noopMetrics` (~38)
- `pkg/git/resolve_conflict_merge_test.go` — `unsafeTestMetrics` (~49)
- `CHANGELOG.md` — `## Unreleased` exists after prompt 1; append to it, do not create a second heading

**Preconditions from prompt 1 (already on this branch):**

- `conflictsDirName = "_conflicts"` is a package-level constant in `pkg/git/git.go`. Use it; do not redefine it and do not reintroduce the string literal in new code.
- `quarantineDestPath` keeps exactly one `_conflicts/` level for any input.
- `nested_source` is pre-initialised in `pkg/metrics/metrics.go` and the guard rejects a re-quarantine before any disk I/O.
- The `## Unreleased` CHANGELOG section exists with one `fix:` bullet.

**Verified facts about the current code:**

- `prometheusMetrics` is a stateless struct; every method delegates to a package-level metric variable, so `metrics.NewMetrics()` instances share the process-global registry.
- `init()` in `pkg/metrics/metrics.go` registers every metric with `prometheus.MustRegister(...)` and pre-initialises the labelled series; `QuarantinedFilesTotal.Add(0)` carries a comment explaining why the explicit zero is visible.
- `Pull(ctx context.Context) error` in `pkg/git/git.go` takes `g.mu.Lock()` with `defer g.mu.Unlock()` and has several early returns (no remote, no upstream, local == remote, fast-forward, push).
- `pkg/git/git.go` imports `stderrors "errors"`, `os`, `path/filepath`, `strings`, `log/slog`; it does NOT import `io/fs` yet.
- `pkg/git/git_test.go` is `package git_test` and already imports `os`, `path/filepath`, `strings`, `log/slog`, `github.com/prometheus/client_golang/prometheus`, `github.com/bborbe/git-rest/pkg/git`, `github.com/bborbe/git-rest/pkg/metrics`, and `libtime "github.com/bborbe/time"`.
- `initRepo() (workDir string, cleanup func())` creates a temp repo backed by a local bare remote with the branch pushed and upstream set, so `Pull` reaches its "nothing to do" path and returns nil.
- `pkg/metrics/metrics_test.go` runs in its own test binary (one process per package), so a pre-init assertion there is not perturbed by the `pkg/git` specs.

**Environment fact:** the container's `.git` is masked (`hideGit`), so branch-relative `git log` / `git diff` checks are not runnable here. Do not run `git diff`, `git log`, or `git status` against the project repo, and do not attempt to commit.

**Sibling prompts (do not do their work):** prompt 1 shipped the nesting guard and the `nested_source` category; prompt 3 adds the per-vault alert that consumes this gauge; prompt 4 refreshes the docs. Do NOT touch `helm/`, `docs/deployment.md`, or `docs/verifying-specs.md` in this prompt.
</context>

<requirements>

## 1. Add the gauge to `pkg/metrics/metrics.go`

Declare the gauge directly after `QuarantinedFilesTotal`:

```go
// QuarantinedBacklog reports the number of regular files currently under _conflicts/.
var QuarantinedBacklog = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "git_rest_quarantined_backlog",
	Help: "Number of regular files currently under _conflicts/ in the served repo (counted recursively).",
})
```

An unlabeled `prometheus.NewGauge` — NOT a `CounterVec`, NOT a `Counter` — because the value must be able to decrease when a quarantined file is removed. No `repo` label: one pod serves one repo, so the label would have cardinality 1 and the pod identity already carries the repo via the `app` label at scrape time.

## 2. Register and pre-initialise the gauge in `init()`

Append `QuarantinedBacklog` to the `prometheus.MustRegister(...)` argument list, after `QuarantinedFilesTotal`.

Immediately after the existing `QuarantinedFilesTotal.Add(0)` line, add:

```go
	// Explicit .Add(0) on the unlabeled gauge, mirroring QuarantinedFilesTotal:
	// the series is registered at init() time so /metrics exposes it as 0 before
	// the first pull cycle refreshes it.
	QuarantinedBacklog.Add(0)
```

The series must be present with value 0 on `/metrics` before any pull has ever run.

## 3. Add the setter to the `Metrics` interface

Append exactly one method to the `Metrics` interface, after `IncQuarantinedFiles`:

```go
	// SetQuarantinedBacklog records the number of regular files currently under
	// _conflicts/ in the served repo. It is a setter rather than an increment so a
	// deletion reduces the reported backlog.
	SetQuarantinedBacklog(count int)
```

Do not reorder, rename, or re-signature any existing method — every other line of the interface stays byte-identical. This is the only method the interface gains.

Implement it on `*prometheusMetrics`, appended after `IncQuarantinedFiles`:

```go
func (p *prometheusMetrics) SetQuarantinedBacklog(count int) {
	QuarantinedBacklog.Set(float64(count))
}
```

## 4. Add the directory walk and the refresh to `pkg/git/git.go`

Add `io/fs` to the import block (it is not imported yet).

Place these two functions next to `quarantineDestPath`:

```go
// countQuarantinedFiles counts the regular files under root, recursively. It does
// not follow symlinks: a symlinked directory is not descended into and a symlink
// entry is not counted as a regular file, so the walk cannot escape the repo root.
// Returns the walk error when root is missing (fs.ErrNotExist) or unreadable.
func countQuarantinedFiles(root string) (int, error) {
	count := 0
	if err := filepath.WalkDir(
		root,
		func(_ string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type().IsRegular() {
				count++
			}
			return nil
		},
	); err != nil {
		return 0, err
	}
	return count, nil
}

// refreshQuarantinedBacklog sets the git_rest_quarantined_backlog gauge to the
// number of regular files currently under _conflicts/. A missing directory reports
// 0 without a log line; an unreadable directory reports 0 and logs one WARN. It
// never returns an error — a backlog read must not fail a pull.
func (g *git) refreshQuarantinedBacklog(ctx context.Context) {
	root := filepath.Join(g.repoPath, conflictsDirName)
	count, err := countQuarantinedFiles(root)
	if err != nil {
		if !stderrors.Is(err, fs.ErrNotExist) {
			slog.WarnContext(
				ctx,
				"git-rest: reading _conflicts/ backlog failed; reporting 0",
				"path",
				conflictsDirName,
				"err",
				err.Error(),
			)
		}
		g.metrics.SetQuarantinedBacklog(0)
		return
	}
	g.metrics.SetQuarantinedBacklog(count)
}
```

`countQuarantinedFiles` is a pure filesystem read: it opens no file, writes nothing, and follows no symlink out of the tree. It takes no `ctx` — the walk is bounded by the size of one directory tree inside the repo root and a partial count would be worse than a slightly longer walk.

## 5. Refresh the gauge once per pull cycle

In `Pull`, register the refresh as a deferred call immediately after the mutex is taken:

```go
	g.mu.Lock()
	defer g.mu.Unlock()
	// Refresh the backlog gauge once per pull cycle, after the merge attempt has
	// resolved. Registering this defer after the unlock defer keeps the walk under
	// the mutex and runs it on every exit path, so a no-op pull still counts a
	// deleted quarantine file down.
	defer g.refreshQuarantinedBacklog(ctx)
```

Because the refresh is deferred, it runs on every exit path of `Pull` — including the early returns for a repo with no remote, a repo with no upstream, and a pull where local and remote already match. That is required: on a healthy vault every pull is a no-op, and a deletion must still be counted down on the next cycle.

Do NOT call the refresh from the puller package, from `main.go`, or from a new goroutine. Do NOT add a ticker, a config knob, or a refresh interval — the pull cycle is the only schedule.

## 6. Regenerate the mock and update the two hand-written fakes

Run from the repo root:

```bash
make generate
```

Run this step before any `make test` run: until the mock is regenerated and the two fakes gain the stub, every test package that uses `metrics.Metrics` fails to compile, which is expected for an interface change.

`mocks/metrics.go` must gain `SetQuarantinedBacklog` (stub field, mutex, argsForCall struct, `SetQuarantinedBacklog`, `SetQuarantinedBacklogCallCount`, `SetQuarantinedBacklogCalls`, `SetQuarantinedBacklogArgsForCall`). `mocks/conflict_resolver.go` is frozen — it must be regenerated byte-identical because the `ConflictResolver` interface is unchanged; if it differs, STOP and investigate rather than keeping the change.

Add the stub to both hand-written `metrics.Metrics` implementations so the packages still compile:

- `pkg/git/git_test.go`, in the `noopMetrics` block: `func (n *noopMetrics) SetQuarantinedBacklog(_ int) {}`
- `pkg/git/resolve_conflict_merge_test.go`, in the `unsafeTestMetrics` block: `func (u *unsafeTestMetrics) SetQuarantinedBacklog(_ int) {}`

These two stub lines are the only edits allowed in those blocks.

## 7. Add the pre-init spec in `pkg/metrics/metrics_test.go`

Add a gauge gather helper and one spec. Do NOT add a spec here that calls `SetQuarantinedBacklog` — the gauge is process-global, and a setter spec in this package would make the pre-init assertion depend on spec order.

```go
// gatherGauge returns the value of an unlabeled gauge from the prometheus default
// registry, and whether the series was present.
func gatherGauge(name string) (float64, bool) {
	mfs, err := prometheus.DefaultGatherer.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			return m.GetGauge().GetValue(), true
		}
	}
	return 0, false
}

var _ = Describe("QuarantinedBacklog", func() {
	It("is pre-initialised to 0 and exposed before any pull refreshes it", func() {
		value, found := gatherGauge("git_rest_quarantined_backlog")
		Expect(found).To(BeTrue(), "the gauge series must exist at init() time")
		Expect(value).To(Equal(0.0))
	})
})
```

## 8. Add the gauge specs in `pkg/git/git_test.go`

Add a gather helper next to `gatherQuarantinedFiles`:

```go
// gatherQuarantinedBacklog returns the current value of the process-global
// git_rest_quarantined_backlog gauge. Returns 0 if the gauge is not registered.
func gatherQuarantinedBacklog() float64 {
	mfs, err := prometheus.DefaultGatherer.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, mf := range mfs {
		if mf.GetName() != "git_rest_quarantined_backlog" {
			continue
		}
		for _, m := range mf.GetMetric() {
			return m.GetGauge().GetValue()
		}
	}
	return 0
}
```

Add a new top-level `Describe` at the end of the file. The specs drive the refresh through the real `Pull` path with the real `metrics.NewMetrics()` — the gauge is only ever set by the production code path, and `mocks.FakeMetrics` cannot back a gather assertion.

```go
var _ = Describe("Quarantine backlog gauge", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("AC8: reports the live _conflicts/ file count and counts down after a deletion", func() {
		workDir, cleanup := initRepo()
		defer cleanup()

		Expect(os.MkdirAll(filepath.Join(workDir, "_conflicts", "dir"), 0o750)).To(Succeed())
		for _, rel := range []string{"a.md", "dir/b.md", "dir/c.md"} {
			Expect(os.WriteFile(filepath.Join(workDir, "_conflicts", rel), []byte("x"), 0o600)).
				To(Succeed())
		}

		logs, restore := captureSlogLogs()
		defer restore()

		pg := git.New(
			workDir,
			metrics.NewMetrics(),
			libtime.NewCurrentDateTime(),
			"",
			git.NewMarkerResolver(workDir),
		)

		Expect(pg.Pull(ctx)).To(Succeed())
		Expect(gatherQuarantinedBacklog()).To(Equal(3.0), "three quarantined files must be reported")

		Expect(os.Remove(filepath.Join(workDir, "_conflicts", "dir", "c.md"))).To(Succeed())
		Expect(pg.Pull(ctx)).To(Succeed())
		Expect(gatherQuarantinedBacklog()).To(Equal(2.0),
			"a deleted quarantine file must be counted down — this is the gauge-vs-counter assertion")

		// A removed directory takes the ErrNotExist branch: report 0, and do NOT warn
		// (the directory is absent on a healthy vault, so warning would be noise).
		Expect(os.RemoveAll(filepath.Join(workDir, "_conflicts"))).To(Succeed())
		Expect(pg.Pull(ctx)).To(Succeed())
		Expect(gatherQuarantinedBacklog()).To(Equal(0.0),
			"a missing _conflicts/ directory must report 0")
		Expect(strings.Count(logs.String(), "level=WARN")).To(Equal(0),
			"an absent _conflicts/ must not emit a WARN — only the unreadable case warns")
	})

	It("AC9: counts nested files so legacy residue is not invisible", func() {
		workDir, cleanup := initRepo()
		defer cleanup()

		Expect(os.MkdirAll(filepath.Join(workDir, "_conflicts", "_conflicts"), 0o750)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(workDir, "_conflicts", "a.md"), []byte("x"), 0o600)).
			To(Succeed())
		Expect(os.WriteFile(filepath.Join(workDir, "_conflicts", "_conflicts", "b.md"), []byte("x"), 0o600)).
			To(Succeed())

		pg := git.New(
			workDir,
			metrics.NewMetrics(),
			libtime.NewCurrentDateTime(),
			"",
			git.NewMarkerResolver(workDir),
		)

		Expect(pg.Pull(ctx)).To(Succeed())
		Expect(gatherQuarantinedBacklog()).To(Equal(2.0),
			"a nested _conflicts/ level must be counted, not hidden")
	})

	It("AC11: an unreadable _conflicts/ degrades to 0 and logs a WARN without failing the pull", func() {
		if os.Geteuid() == 0 {
			Skip("running as root: a 0000 directory stays readable, so the case would pass vacuously")
		}

		workDir, cleanup := initRepo()
		defer cleanup()

		conflictsDir := filepath.Join(workDir, "_conflicts")
		Expect(os.MkdirAll(conflictsDir, 0o750)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(conflictsDir, "a.md"), []byte("x"), 0o600)).To(Succeed())
		Expect(os.Chmod(conflictsDir, 0o000)).To(Succeed())
		DeferCleanup(func() { _ = os.Chmod(conflictsDir, 0o750) })

		logs, restore := captureSlogLogs()
		defer restore()

		pg := git.New(
			workDir,
			metrics.NewMetrics(),
			libtime.NewCurrentDateTime(),
			"",
			git.NewMarkerResolver(workDir),
		)

		Expect(pg.Pull(ctx)).To(Succeed(), "an unreadable _conflicts/ must never fail the pull")
		Expect(gatherQuarantinedBacklog()).To(Equal(0.0))

		logStr := logs.String()
		Expect(logStr).To(ContainSubstring("level=WARN"))
		Expect(logStr).To(ContainSubstring("_conflicts"))
	})
})
```

The `Skip` guard is required: under uid 0 a `0000` directory is still readable, so the spec would pass without exercising the branch.

## 9. CHANGELOG

Append to the existing `## Unreleased` section (do not create a second heading):

```markdown
- feat: Add `git_rest_quarantined_backlog` gauge — the number of regular files currently under `_conflicts/`, counted recursively so nested residue left by an older binary is visible. The gauge is refreshed once per pull cycle and is a setter, so removing a quarantined file counts the backlog down instead of only ever up. A missing `_conflicts/` directory reports 0; an unreadable one reports 0 and logs one WARN without failing the pull. The existing `git_rest_quarantined_files_total` counter keeps its lifetime-events meaning and cannot express backlog.
```

## 10. Final verification

Run every command in `<verification>` from the repo root and confirm each result before finishing.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT modify `pkg/git/conflict_resolver.go`, `mocks/conflict_resolver.go`, or `pkg/git/yaml_merge_resolver.go` — they are frozen by the spec. `make generate` regenerates all mocks; `mocks/conflict_resolver.go` must come back byte-identical.
- Do NOT change any existing `Metrics` method signature, name, or order. The interface gains exactly one method.
- Do NOT add a `repo` label to the gauge — one pod serves one repo, so the label would have cardinality 1.
- Do NOT add a refresh-interval knob, a ticker, a per-repo override, or a "disable backlog" flag. The pull cycle is the only schedule, and the walk runs at most once per cycle.
- Do NOT add a quarantine-size cap or retention policy — visibility bounds accumulation; deliberate truncation does not.
- Do NOT reuse or repurpose `git_rest_quarantined_files_total`: it stays a monotonic counter over lifetime quarantine events.
- Do NOT add filesystem access to `pkg/metrics`. The directory walk lives in `pkg/git`, which is the only caller; the metrics package receives a number.
- The gauge MUST be a `prometheus.NewGauge` (not a Counter, not a GaugeVec) and MUST be pre-initialised with an explicit `.Add(0)` in `init()`.
- The refresh MUST be a deferred call inside `Pull`, registered after the mutex is taken, so it runs on every exit path including the no-op pulls. It MUST NOT return an error and MUST NOT fail the pull.
- The refresh MUST use the `conflictsDirName` constant from prompt 1 — do not reintroduce the `"_conflicts"` literal in new code.
- Errors MUST be wrapped with `errors.Wrap` / `errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf`, never a bare `return err`. The refresh path introduces no new error return.
- Logging MUST use `log/slog` (`slog.WarnContext`), matching the existing quarantine call sites. Never `context.Background()` in `pkg/`.
- The walk MUST NOT follow symlinks and MUST NOT open or write any file.
- `countQuarantinedFiles` and `refreshQuarantinedBacklog` are unexported. The gauge specs live in `pkg/git/git_test.go` (Ginkgo v2 + Gomega, `package git_test`) and MUST drive the refresh through the real `Pull` path with the real `metrics.NewMetrics()`.
- `pkg/git/conflict_resolver_test.go`, `pkg/git/yaml_merge_resolver_test.go`, and every existing spec in `pkg/git/git_test.go` stay intact; the only edits to `pkg/git/git_test.go` are the `noopMetrics` stub, the gather helper, and the new Describe block.
- `make precommit` from the repo root MUST exit 0.
- The container's `.git` is masked: do not run `git diff`, `git log`, or `git status` against the project repo as verification.
- `helm/`, `docs/deployment.md`, and `docs/verifying-specs.md` are prompt 3's and prompt 4's scope — do not touch them here.
</constraints>

<verification>
Run from the repo root.

```bash
make precommit
```
Must exit 0.

Targeted test runs:

```bash
go test ./pkg/git/... -ginkgo.focus='Quarantine backlog gauge'
go test ./pkg/metrics/... -ginkgo.focus='QuarantinedBacklog'
go test ./pkg/git/... -ginkgo.focus='(?i)quarantine'
```
All pass. (The `AC11` spec skips itself when the test process runs as root; that is expected, not a failure.)

Metric surface checks (each `grep` must print at least one match):

```bash
grep -n 'git_rest_quarantined_backlog' pkg/metrics/metrics.go        # the GaugeOpts Name
grep -n 'QuarantinedBacklog' pkg/metrics/metrics.go                  # ≥5: declaration, register, .Add(0), interface doc + method, implementation
grep -n 'SetQuarantinedBacklog' pkg/metrics/metrics.go               # ≥3: interface method, doc comment, implementation
grep -n 'SetQuarantinedBacklog' mocks/metrics.go                     # ≥6: regenerated counterfeiter surface
grep -n 'SetQuarantinedBacklog' pkg/git/git_test.go                  # ≥1: the noopMetrics stub
grep -n 'SetQuarantinedBacklog' pkg/git/resolve_conflict_merge_test.go  # ≥1: the unsafeTestMetrics stub
grep -n 'SetQuarantinedBacklog' pkg/git/git.go                       # ≥1: the refresh call site
```

Wiring checks:

```bash
grep -n 'refreshQuarantinedBacklog' pkg/git/git.go                   # ≥2: definition + deferred call in Pull
grep -n 'defer g.refreshQuarantinedBacklog(ctx)' pkg/git/git.go      # exactly 1
grep -n 'io/fs' pkg/git/git.go                                       # the new import
grep -n 'conflictsDirName' pkg/git/git.go                            # the walk root uses the constant
grep -c 'func (fake \*FakeConflictResolver)' mocks/conflict_resolver.go   # must still print 8
```

Deletion-tracking evidence — the gauge must fall, which is what distinguishes it from the counter:

```bash
go test ./pkg/git/... -ginkgo.focus='AC8' -v
```
The focused run passes; the spec asserts 3 then 2 across two pulls.

CHANGELOG checks:

```bash
grep -c '^## Unreleased' CHANGELOG.md                     # must print 1
grep -n 'git_rest_quarantined_backlog' CHANGELOG.md       # ≥1 match inside ## Unreleased
grep -n 'nested_source' CHANGELOG.md                      # the prompt-1 bullet is still present
```

Self-check before finishing: re-run `make precommit`, then walk each requirement above against the change — the gauge is a `Gauge` pre-initialised to 0, the interface gained exactly one setter, the refresh is deferred inside `Pull` and runs on the no-op path, the walk counts nested files and follows no symlink, the unreadable directory degrades to 0 with one WARN, and the existing counter keeps its meaning.
</verification>
