---
status: completed
spec: [014-bug-pull-cannot-recover-from-dirty-working-tree]
summary: Added the git_rest_pull_rescues_total counter with one Metrics interface method, regenerated the counterfeiter mock, stubbed both hand-written fakes, and incremented it once per rescue after the INFO log in rescueDirtyTree
execution_id: git-rest-exec-055-spec-014-rescue-counter
dark-factory-version: v0.196.0
created: "2026-09-25T10:39:18Z"
queued: "2026-09-25T11:34:36Z"
started: "2026-09-25T11:38:50Z"
completed: "2026-09-25T11:42:23Z"
branch: dark-factory/bug-pull-cannot-recover-from-dirty-working-tree
---

# Count dirty-tree rescues on `/metrics`

<summary>
- Operators can see how often a pod had to rescue a dirty working tree
- The count is a monotonic counter that is visible on `/metrics` before the first rescue ever happens
- The rescue log line already names the captured paths; this prompt adds the number that makes the event aggregatable
- The metric surface gains exactly one method, and the counterfeiter mock is regenerated to match
- Existing metrics, their names, their labels and their registration keep their current meaning
- Nothing about the rescue mechanism itself changes
</summary>

<objective>
Add the `git_rest_pull_rescues_total` counter, one method on the `Metrics` interface, its `*prometheusMetrics` implementation, the regenerated `mocks/metrics.go`, the pre-initialisation in `init()`, and the single call site inside the rescue path shipped by prompt 1. Prompt 1 already logs the rescue; the counter is what makes "how often is this happening across the fleet" expressible.
</objective>

<context>
Read `CLAUDE.md` at the repo root for project conventions.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` — Counter vs Gauge rule; pre-initialisation in `init()`; the composed-metrics-interface rule; gathering from `prometheus.DefaultGatherer` for tests
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega; external test packages
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` heading; `feat:` prefix rules

Files to read in full before editing (line numbers are hints; anchor by symbol name):

- `pkg/metrics/metrics.go` — `QuarantinedFilesTotal` (~43, the model for an unlabeled `prometheus.NewCounter`), `QuarantinedBacklog` (~49, the most recent declaration, after which the new counter goes), the `prometheus.MustRegister(...)` call and the `QuarantinedFilesTotal.Add(0)` comment in `init()` (~67), the `Metrics` interface (~123) and the `*prometheusMetrics` implementation (~149)
- `pkg/metrics/metrics_test.go` — `gatherCounter` (~18), the `QuarantinedFilesTotal` Describe (~52, the two-spec shape to mirror)
- `mocks/metrics.go` — the counterfeiter output; it gains the new method's stub field, mutex, `argsForCall` struct, implementation, `CallCount`, `Calls` and `ArgsForCall`. Compare the existing `IncQuarantinedFiles` surface (~33, ~191-212)
- `pkg/git/git.go` — `rescueDirtyTree` (added by prompt 1), specifically the `slog.InfoContext(ctx, "rescue branch pushed", ...)` call — the counter increments immediately after it
- `pkg/git/git_test.go` — the `noopMetrics` block (~39) needs one more stub; the `Dirty working tree rescue` Describe added by prompt 1 is where the increment spec goes
- `pkg/git/resolve_conflict_merge_test.go` — the `unsafeTestMetrics` block (~49) needs one more stub
- `CHANGELOG.md` — prompt 1 created `## Unreleased`; append to it, do not create a second heading

**Preconditions from prompt 1 (already on this branch):**

- `pkg/git/git.go` has `rescueDirtyTree(ctx context.Context, upstream string) error`, which creates a `rescue/<timestamp>` commit, pushes it, logs exactly one INFO line beginning `rescue branch pushed` with `branch`, `files` and `paths` attributes, and only then runs `git reset --hard` + `git clean -fd`.
- `pkg/git/git.go` has `workingTreeDirty`, `createRescueCommit`, `syncWithUpstream`, `fastForwardOrRescue`, `childEnv`, `runCmdEnv`, `runCmdOutputEnv`.
- `pkg/git/git_test.go` has `setupRescueFixture() (workDir string, advanceRemote func(), dirtyTree func(), remoteDir string, cleanup func())` and the `Dirty working tree rescue` Describe.
- `CHANGELOG.md` has exactly one `## Unreleased` section containing one `fix:` bullet.

**Verified facts about the current code:**

- `prometheusMetrics` is a stateless struct; every method delegates to a package-level metric variable, so `metrics.NewMetrics()` instances share the process-global registry.
- `init()` in `pkg/metrics/metrics.go` calls `prometheus.MustRegister(...)` with every metric variable, then pre-initialises the labelled series; the unlabeled `QuarantinedFilesTotal` and `QuarantinedBacklog` each carry an explicit `.Add(0)` with a comment explaining why the zero is visible.
- `pkg/metrics/metrics_test.go` is `package metrics_test` and already imports `prometheus`, Ginkgo and Gomega, and `github.com/bborbe/git-rest/pkg/metrics`. It has `gatherCounter(name string) float64` (reads `GetCounter().GetValue()` from the default registry, returns 0 if absent).
- `pkg/metrics/metrics_test.go` runs in its own test binary (one process per package), so a pre-init assertion there is not perturbed by the `pkg/git` specs.
- `pkg/git/git_test.go` is `package git_test` and already imports `prometheus`, `github.com/bborbe/git-rest/pkg/metrics` and `libtime "github.com/bborbe/time"`.
- The rescue counter is process-global and shared by every spec in the `pkg/git` test binary, so the `pkg/git` assertions MUST be deltas, not absolute values. The absolute-value assertion belongs in `pkg/metrics/metrics_test.go`, whose binary never drives a rescue.
- The spec's AC for the counter reads it as `1` against a freshly started process in the operator recipe; the in-process equivalent is "incremented by exactly 1 across one rescue".

**Sibling prompts (do not do their work):**

- Prompt 1 shipped the whole rescue mechanism, the two log lines, the 006 constraint amendment and the `fix:` CHANGELOG bullet. Do NOT change the rescue's control flow, the log messages, the ref naming, the temp-index construction or the reset/clean gating.
- Prompt 3 adds the untouched-path regression tests and the three undocumented `CLAUDE.md` invariants. Do NOT touch `CLAUDE.md`.
- Prompt 4 rewrites the verification docs. Do NOT touch `docs/verifying-specs.md` or `docs/deployment.md`.
</context>

<requirements>

## 1. Declare the counter in `pkg/metrics/metrics.go`

Place it directly after the `QuarantinedBacklog` gauge declaration:

```go
// PullRescuesTotal counts dirty-working-tree rescues performed during pull. One
// increment per rescue event: the working-tree state was captured on a
// rescue/<timestamp> branch and pushed, then the working tree was returned to the
// upstream state.
var PullRescuesTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "git_rest_pull_rescues_total",
	Help: "Total number of dirty-working-tree rescues performed during pull (working-tree state captured on a rescue/<timestamp> branch and pushed, then the working tree returned to the upstream state).",
})
```

An unlabeled `prometheus.NewCounter` — NOT a `CounterVec`, NOT a `Gauge`. The value can only go up, and it is a lifetime event count. No `repo` label: one pod serves one repo, so the label would have cardinality 1 and the pod identity already carries the repo via the `app` label at scrape time.

## 2. Register and pre-initialise it in `init()`

Append `PullRescuesTotal` to the `prometheus.MustRegister(...)` argument list, after `QuarantinedBacklog`.

Immediately after the existing `QuarantinedBacklog.Add(0)` line, add:

```go
	// Explicit .Add(0) on the unlabeled rescue counter, mirroring
	// QuarantinedFilesTotal: the series is registered at init() time so /metrics
	// exposes it as 0 before the first rescue has ever happened.
	PullRescuesTotal.Add(0)
```

The series MUST be present with value 0 on `/metrics` before any rescue. A name grep over `metrics.go` proves neither the declaration nor the pre-registration — only a gather assertion does.

## 3. Add exactly one method to the `Metrics` interface

Append it after `SetQuarantinedBacklog`, as the last method of the interface:

```go
	// IncPullRescue records one dirty-working-tree rescue: the working-tree state
	// was captured on a rescue/<timestamp> branch and pushed to the upstream
	// remote, and the working tree was then returned to the upstream state.
	IncPullRescue()
```

Do not reorder, rename or re-signature any existing method — every other line of the interface stays byte-identical. This is the only method the interface gains.

Implement it on `*prometheusMetrics`, appended after `SetQuarantinedBacklog`:

```go
func (p *prometheusMetrics) IncPullRescue() {
	PullRescuesTotal.Inc()
}
```

## 4. Add the single call site in `pkg/git/git.go`

In `rescueDirtyTree`, insert `g.metrics.IncPullRescue()` immediately AFTER the `slog.InfoContext(ctx, "rescue branch pushed", ...)` call and BEFORE the `git reset --hard` call.

Placement rules, all load-bearing:

- It MUST be after the successful push. A failed push returns before this line, so a rejected rescue is never counted.
- It MUST be after the INFO log, so the log and the counter describe the same event in the same order.
- It MUST be exactly once per rescue. There is no other call site anywhere in the repo.
- It MUST NOT be called on the `!dirty` branch of `fastForwardOrRescue` — that path records `IncGitOperationError("pull")` and returns the merge's error, and it is not a rescue.

Do not add a metric call anywhere else. Do not change the log messages, the control flow, the ref naming, the temp-index construction or the reset/clean gating.

## 5. Regenerate the counterfeiter mock

Run from the repo root:

```bash
make generate
```

Run this BEFORE any `make test` run: until the mock is regenerated and the two hand-written fakes gain the stub, every test package that uses `metrics.Metrics` fails to compile, which is expected for an interface change.

`mocks/metrics.go` must gain `IncPullRescue` (stub field, mutex, `argsForCall` struct, the `IncPullRescue` method, `IncPullRescueCallCount`, `IncPullRescueCalls`, `IncPullRescueArgsForCall`). Model the shape on the existing `IncQuarantinedFiles` surface.

The other mocks must come back byte-identical. `make generate` deletes and regenerates the whole `mocks/` directory; only `mocks/metrics.go` may differ. If any other file differs, STOP and investigate rather than keeping the change.

## 6. Add the stub to both hand-written fakes

The `Metrics` interface gained a method, so every hand-written implementation must satisfy it:

- `pkg/git/git_test.go`, in the `noopMetrics` block: `func (n *noopMetrics) IncPullRescue() {}`
- `pkg/git/resolve_conflict_merge_test.go`, in the `unsafeTestMetrics` block: `func (u *unsafeTestMetrics) IncPullRescue() {}`

These two stub lines are the only edits allowed in those blocks.

## 7. Add the pre-init and increment specs in `pkg/metrics/metrics_test.go`

Append one presence-returning helper and one Describe. `gatherCounter` alone cannot prove pre-registration — its own doc comment says it "returns 0 if the counter is not registered or has not been incremented", so asserting `Equal(0.0)` passes vacuously for a counter that `init()` never registered. Mirror the existing `gatherGauge` presence helper, exactly as `hasResolverFailureSeries` in `pkg/git/git_test.go` does for the resolver counter.

```go
// gatherCounterPresent returns the value of an unlabeled counter from the
// prometheus default registry, and whether the series was present. Mirrors the
// existing gatherGauge helper: gatherCounter returns 0 both for an absent series
// and for one present with value 0, so it cannot prove pre-registration.
func gatherCounterPresent(name string) (float64, bool) {
	mfs, err := prometheus.DefaultGatherer.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			return m.GetCounter().GetValue(), true
		}
	}
	return 0, false
}

var _ = Describe("PullRescuesTotal", func() {
	It("is registered with value 0 in the default registry at init() time", func() {
		value, found := gatherCounterPresent("git_rest_pull_rescues_total")
		Expect(found).To(BeTrue(), "the counter series must exist at init() time")
		Expect(value).To(Equal(0.0))
	})

	It("increments when IncPullRescue is called via the interface", func() {
		// Use a fresh metrics implementation to avoid bleeding counter state into
		// other test cases that share the default registry. (The default registry
		// is process-global; the unlabeled counter at init() is the baseline.)
		before := gatherCounter("git_rest_pull_rescues_total")
		m := metrics.NewMetrics()
		m.IncPullRescue()
		after := gatherCounter("git_rest_pull_rescues_total")
		Expect(after - before).To(Equal(1.0))
	})
})
```

The two specs live in the same `Describe` container, so they run in declaration order and the zero assertion is not perturbed by the increment assertion. This package's test binary never drives a rescue, so the absolute-zero assertion is sound here.

## 8. Add the counter specs in `pkg/git/git_test.go`

Add a gather helper next to the `Dirty working tree rescue` Describe's other helpers:

```go
// gatherPullRescues returns the current value of the process-global
// git_rest_pull_rescues_total counter. Returns 0 if the counter is not registered.
func gatherPullRescues() float64 {
	mfs, err := prometheus.DefaultGatherer.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, mf := range mfs {
		if mf.GetName() != "git_rest_pull_rescues_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			return m.GetCounter().GetValue()
		}
	}
	return 0
}
```

The counter is process-global and shared by every spec in this test binary, so assertions here MUST be deltas.

Add one spec inside the existing `Dirty working tree rescue` Describe. It drives the real `Pull` path with the real `metrics.NewMetrics()`, because the counter is only ever incremented by the production code path and `mocks.FakeMetrics` cannot back a gather assertion.

```go
	It("AC3: increments git_rest_pull_rescues_total by exactly one per rescue", func() {
		advanceRemote()
		dirtyTree()

		before := gatherPullRescues()
		Expect(pg.Pull(ctx)).To(Succeed())
		Expect(gatherPullRescues() - before).To(Equal(1.0))
	})
```

The gated-increment proof — a non-dirty fast-forward failure increments nothing — belongs to **prompt 3's `AC4b`**, which builds the same `.git/index.lock` construction and asserts the counter delta of 0 plus a `rescue branch pushed` log assertion. Do not duplicate it here: two specs named `AC4b` in the same test binary would make the `-ginkgo.focus='AC4b'` check below ambiguous.

`DeferCleanup` is available because the file dot-imports Ginkgo v2. `os`, `path/filepath`, `strings`, `prometheus` and `metrics` are already imported by `pkg/git/git_test.go`.

## 9. CHANGELOG

Append one bullet to the existing `## Unreleased` section (do not create a second heading):

```markdown
- feat: Add `git_rest_pull_rescues_total` counter — one increment per dirty-working-tree rescue. Registered and pre-initialised to zero at process start, so the series is visible on `/metrics` before any rescue has happened and `rate(git_rest_pull_rescues_total[5m])` is meaningful on a freshly started pod.
```

Prefix `feat:` (a new metric surface is a feature for operators). The `fix:` bullet prompt 1 wrote stays exactly as it is.

## 10. Final verification

Run every command in `<verification>` from the repo root and confirm each result before finishing. Then walk each requirement above against the change: the counter is an unlabeled `prometheus.NewCounter`, pre-initialised with an explicit `.Add(0)` in `init()`, the interface gained exactly one method, the mock was regenerated, both hand-written fakes gained the stub, and there is exactly one call site — inside `rescueDirtyTree`, after the INFO log.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT change the rescue's control flow, log messages, ref naming, temporary-index construction, or the gating of `git reset --hard` / `git clean -fd` behind a successful push. Prompt 1 shipped those.
- Do NOT add a second call site for `IncPullRescue`. Exactly one, inside `rescueDirtyTree`, after the `rescue branch pushed` INFO line and before `git reset --hard`.
- Do NOT call `IncPullRescue` on the `!dirty` path of `fastForwardOrRescue` — that path records `IncGitOperationError("pull")` and is not a rescue.
- Do NOT modify any existing `Metrics` method signature, name or order. The interface gains exactly one method, appended last.
- Do NOT add a `repo` label to the counter — one pod serves one repo, so the label would have cardinality 1.
- Do NOT add an alert, an alert rule, a Helm template, or any `helm/` change. The spec's Constraints state explicitly that alerting for the agent vault belongs in `seibert-data/agent/alerts/` and is a separate spec there. This spec ships the counter that makes such an alert expressible and nothing more.
- Do NOT add a tunable threshold, a per-repo override, a "disable rescue" flag, or a second metric (no gauge, no histogram, no duration metric).
- The counter MUST be a `prometheus.NewCounter` (not a `CounterVec`, not a `Gauge`) and MUST be pre-initialised with an explicit `.Add(0)` in `init()`.
- The counter name MUST be exactly `git_rest_pull_rescues_total` (the `git_rest_` prefix is a documented repo invariant).
- The `Metrics` interface method MUST be named `IncPullRescue()` and MUST be implemented on `*prometheusMetrics` by incrementing the package-level `PullRescuesTotal`.
- `make generate` deletes and regenerates the whole `mocks/` directory. The only intended change is `mocks/metrics.go`. `pkg/git/conflict_resolver.go`, `pkg/git/yaml_merge_resolver.go` and `mocks/conflict_resolver.go` are frozen by spec 012/013 — their generated output must come back byte-identical.
- Errors MUST be wrapped with `errors.Wrap` / `errors.Wrapf` from `github.com/bborbe/errors` — but this prompt introduces no new error path.
- Logging MUST use `log/slog`. Never `context.Background()` in `pkg/`. This prompt adds no log line.
- The `pkg/git` counter assertions MUST be deltas against a `before` read, because the counter is process-global and shared by every spec in that test binary. The absolute-zero assertion belongs in `pkg/metrics/metrics_test.go`, whose binary never drives a rescue.
- The new specs MUST drive the counter through the real `Pull` path with the real `metrics.NewMetrics()` — `mocks.FakeMetrics` cannot back a `prometheus.DefaultGatherer` assertion.
- `pkg/metrics/metrics_test.go` MUST stay `package metrics_test` (external test package), matching the existing file.
- Do NOT touch `CLAUDE.md` (prompt 3), `docs/verifying-specs.md` or `docs/deployment.md` (prompt 4), or `docs/api.md` (frozen public HTTP contract).
- New code MUST reach >=80% statement coverage; the counter is covered by the pre-init spec, the interface-increment spec and the two `pkg/git` specs above.
- `make precommit` from the repo root MUST exit 0.
- Existing tests must still pass. The only edits to `pkg/git/git_test.go` are the `noopMetrics.IncPullRescue` stub, the `gatherPullRescues` helper, and the one new spec inside the `Dirty working tree rescue` Describe.
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
go test ./pkg/metrics/... -ginkgo.focus='PullRescuesTotal'
go test ./pkg/git/... -ginkgo.focus='Dirty working tree rescue'
```

All pass.

Mock regeneration checks:

```bash
grep -n 'IncPullRescue' mocks/metrics.go
grep -n 'IncPullRescue' pkg/git/git_test.go
grep -n 'IncPullRescue' pkg/git/resolve_conflict_merge_test.go
grep -n 'IncPullRescue' pkg/git/git.go
```

The first prints at least 6 matches (stub field, mutex, `argsForCall` struct, the method, `CallCount`, `Calls`, `ArgsForCall`); the second and third print at least one each (the two hand-written fakes); the fourth prints exactly one — the single call site.

Call-site placement check — the increment sits after the INFO log and before the reset:

```bash
grep -n 'rescue branch pushed\|IncPullRescue\|"reset", "--hard"' pkg/git/git.go
```

The line numbers must appear in that order: `rescue branch pushed` < `IncPullRescue` < `reset --hard`.

Exactly one call site:

```bash
grep -c 'IncPullRescue()' pkg/git/git.go
```

Must print `1`.

Metric surface checks (each must print at least one match):

```bash
grep -n 'git_rest_pull_rescues_total' pkg/metrics/metrics.go
grep -n 'PullRescuesTotal' pkg/metrics/metrics.go
grep -n 'PullRescuesTotal.Add(0)' pkg/metrics/metrics.go
grep -n 'git_rest_pull_rescues_total' pkg/metrics/metrics_test.go
```

`PullRescuesTotal` in `pkg/metrics/metrics.go` must match at least 4 times: the declaration, the `MustRegister` entry, the `.Add(0)`, the interface doc reference is optional but the `IncPullRescue` implementation must reference it.

Interface-method check:

```bash
grep -n 'IncPullRescue' pkg/metrics/metrics.go
```

Must print at least 3 matches: the interface doc comment, the interface method, and the `*prometheusMetrics` implementation.

No-metric-elsewhere check:

```bash
grep -rn 'IncPullRescue' --include='*.go' . | grep -v '^./mocks/' | grep -v '^./pkg/metrics/metrics.go'
```

Must print only the two hand-written fakes in `pkg/git/` and the single call site in `pkg/git/git.go`.

Pre-init and increment specs pass:

```bash
go test ./pkg/metrics/... -v -ginkgo.focus='PullRescuesTotal'
```

Both specs pass, and the increment spec asserts a delta of exactly 1.

Counter-is-gated-on-the-rescue spec:

```bash
go test ./pkg/git/... -ginkgo.focus='AC3' -v
```

It passes: it asserts a delta of 1 across one rescue. The delta-of-0 case is asserted by prompt 3's `AC4b`, not here.

CHANGELOG checks:

```bash
grep -c '^## Unreleased' CHANGELOG.md
grep -n 'git_rest_pull_rescues_total' CHANGELOG.md
grep -n 'rescue/' CHANGELOG.md
```

The first must print `1`; the second and third must each print at least one match inside `## Unreleased` (the third is prompt 1's `fix:` bullet, which must still be present).

Frozen-file check:

```bash
grep -c 'func (fake \*FakeConflictResolver)' mocks/conflict_resolver.go
```

Must print `8` — the `ConflictResolver` interface is unchanged, so its regenerated mock is byte-identical.

Self-check before finishing: re-run `make precommit`, then walk each requirement above against the change — the counter is an unlabeled `prometheus.NewCounter` pre-initialised to 0 in `init()`, the interface gained exactly one method, `mocks/metrics.go` was regenerated, both hand-written fakes gained the stub, and there is exactly one call site inside `rescueDirtyTree` after the INFO log and before the reset.
</verification>
