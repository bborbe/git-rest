---
status: approved
spec: [024-bug-body-conflict-hunks-quarantine]
created: "2026-11-02T18:50:00Z"
queued: "2026-11-02T18:46:44Z"
branch: dark-factory/bug-body-conflict-hunks-quarantine
---

# Add the body-merge counter and its positive and negative assertions

<summary>
- A new `git_rest_body_merged_total` counter is exposed on `/metrics` at zero before the first event.
- Each path resolved by the body rule increments it exactly once, only after both the write and `git add` succeed.
- A body resolution increments neither the scalar-rule counter nor any resolver-failure category.
- The deep-merge path and the scalar-frontmatter rule both succeed without touching the new counter.
- A rejected or failed body path never increments the counter.
- The counter is a separate series, not a label on the existing autoresolved counter, so the existing unlabelled series keeps its shape.
- The generated metrics mock exposes the usual stub, call-count and calls accessors.
</summary>

<objective>
Make body-rule resolutions observable and prove the counter is not over-counted: add a pre-initialised `git_rest_body_merged_total` counter, expose `IncBodyMerged` on the metrics interface, increment it once per body-resolved path after the write and `git add` both succeed, and add positive and negative FakeMetrics assertions.
</objective>

<context>
There is no repo-root `CLAUDE.md` in this worktree — do not look for one. The project conventions are restated in `<constraints>` below.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` — counter registration and pre-initialisation
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, `prometheus.DefaultGatherer` assertions
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — linter limits

Files to read in full before editing (anchor by symbol name, not line number):

- `pkg/metrics/metrics.go` — every counter, the `Metrics` interface, `prometheusMetrics`, and the `init()` block with its explicit `.Add(0)` pre-initialisations (notably `AutoresolvedTotal.Add(0)`)
- `pkg/metrics/metrics_test.go` — `gatherCounter`, `gatherCounterPresent`, `gatherCounterVecLabelValue`
- `pkg/git/yaml_merge_resolver.go` — `applyBodyRule` (added by prompt 1) and `applyScalarFrontmatterRule`; the success point is after `runGitAdd` returns nil
- `pkg/git/yaml_merge_resolver_test.go` — the `Describe("Body-only conflict regions: extended", …)` block added by prompt 1, the top-level `BeforeEach`, and the helpers `setupYAMLConflictRepo`, `setupHandWrittenConflict`
- `pkg/git/git_test.go` — `noopMetrics`, a hand-written `metrics.Metrics` implementation
- `pkg/git/resolve_conflict_merge_test.go` — `unsafeTestMetrics`, the second hand-written `metrics.Metrics` implementation

**Verified facts — do not re-derive, do not contradict:**

- `pkg/metrics/metrics.go` registers every series in a package `init()` and calls `.Add(0)` explicitly on each unlabeled counter, so `/metrics` exposes the series before the first event. The new counter follows that pattern.
- The counterfeiter directive lives on the `Metrics` interface (`//counterfeiter:generate -o ../../mocks/metrics.go --fake-name FakeMetrics . Metrics`). `make generate` regenerates `mocks/`, and `make precommit` runs `generate` before `test`.
- Adding a method to `metrics.Metrics` breaks every hand-written implementation. There are exactly two besides the generated mock and the Prometheus implementation: `noopMetrics` in `pkg/git/git_test.go` and `unsafeTestMetrics` in `pkg/git/resolve_conflict_merge_test.go`.
- The generated mock exposes `IncBodyMergedStub`, `IncBodyMergedCallCount()` and `IncBodyMergedCalls(stub func())` after regeneration, matching the existing `IncAutoresolvedStub` / `IncAutoresolvedCallCount()` / `IncAutoresolvedCalls(...)` shape. There is no `ArgsForCall` accessor for a method with no parameters.
- `applyBodyRule` returns `(true, nil)` only after both `os.WriteFile` and `r.runGitAdd` succeed. That is the single point where a path has been body-resolved.
- `applyScalarFrontmatterRule` returns `(true, nil)` after incrementing `IncAutoresolved`; the body counter must stay 0 on that path.
- The deep-merge path (`splitConflictSides` → `splitFrontmatter` → `parseYAML` → `deepMergeTheirsWins` → write → `git add`) never calls `applyBodyRule`.
- `setupYAMLConflictRepo("a: 1\n", "X", "b: 2\n", "Y")` exercises the deep-merge path (both sides carry frontmatter).
- A scalar-frontmatter conflict fixture for `setupHandWrittenConflict("note.md", …)` is `---\n<<<<<<< HEAD\ntrigger_count: 3\n=======\ntrigger_count: 0\n>>>>>>> theirs\ntitle: probe\n---\nbody\n`; it resolves through the scalar rule.
- `setupHandWrittenConflict` returns `workDir`; an untracked second markdown file written into `workDir` is readable by the resolver, and `runGitAdd` stages it. No pre-staging is needed.
</context>

<requirements>

## 1. Add the counter

In `pkg/metrics/metrics.go`, add beside `AutoresolvedTotal`:

```go
// BodyMergedTotal counts conflicted paths resolved by the body-only rule (a
// conflict confined to the markdown body below the closing frontmatter
// delimiter) during pull.
var BodyMergedTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "git_rest_body_merged_total",
	Help: "Total conflicted paths auto-resolved by the body-only rule during pull.",
})
```

- Register it in the existing `prometheus.MustRegister(...)` call in `init()`.
- Add an explicit `BodyMergedTotal.Add(0)` in `init()` with a comment mirroring `AutoresolvedTotal.Add(0)`: the series must be present on `/metrics` at zero before the first resolution.
- Do not add a label. This is a separate unlabelled counter, not a `rule` label on `git_rest_autoresolved_total` — that counter's help text scopes it to the scalar rule and a label would break the existing unlabelled series.

## 2. Expose it on the metrics interface

Add to the `Metrics` interface, next to `IncAutoresolved`:

```go
// IncBodyMerged records one conflicted path auto-resolved by the body-only
// rule. Called once per path, after the file is written and staged.
IncBodyMerged()
```

Implement it on `prometheusMetrics`:

```go
func (p *prometheusMetrics) IncBodyMerged() {
	BodyMergedTotal.Inc()
}
```

Run `make generate` to regenerate `mocks/metrics.go` from the counterfeiter directive. Do not hand-edit the generated mock; confirm the regenerated file contains `IncBodyMergedStub`, `IncBodyMergedCallCount()` and `IncBodyMergedCalls(stub func())`.

## 3. Update the two hand-written metrics implementations

- In `pkg/git/git_test.go`, add `func (n *noopMetrics) IncBodyMerged() {}` beside `IncAutoresolved`.
- In `pkg/git/resolve_conflict_merge_test.go`, add `func (u *unsafeTestMetrics) IncBodyMerged() {}` beside `IncAutoresolved`.

Do not change any other behaviour in those files.

## 4. Increment once per body-resolved path

In `pkg/git/yaml_merge_resolver.go`, in `applyBodyRule`, call `r.metrics.IncBodyMerged()` exactly once, after both `os.WriteFile` and `r.runGitAdd(ctx, path)` have succeeded, immediately before `return true, nil`.

It must NOT fire on:

- the not-applicable path (`return false, nil`),
- the cancellation path,
- the `write_failed` path,
- the `git_add_failed` path.

Do not increment any resolver-failure category on the success path, and do not touch `IncAutoresolved`.

## 5. Tests

### 5a. Metrics unit tests

In `pkg/metrics/metrics_test.go`, add a `Describe("BodyMergedTotal", …)` block following the existing `PullRescuesTotal` / `AutoresolvedTotal` blocks:

```go
It("is registered with value 0 in the default registry at init() time", func() {
    value, found := gatherCounterPresent("git_rest_body_merged_total")
    Expect(found).To(BeTrue(), "the counter series must exist at init() time")
    Expect(value).To(Equal(0.0))
})

It("increments when IncBodyMerged is called via the interface", func() {
    before := gatherCounter("git_rest_body_merged_total")
    m := metrics.NewMetrics()
    m.IncBodyMerged()
    after := gatherCounter("git_rest_body_merged_total")
    Expect(after - before).To(Equal(1.0))
})
```

### 5b. Ginkgo assertions in the resolver suite

Add a new `Describe("Body merge metric", …)` block inside the top-level `Describe("YAMLMergeResolver", …)` in `pkg/git/yaml_merge_resolver_test.go`, so the shared `BeforeEach` applies. The committed `Describe("Resolve body-only conflict regions")` block and prompt 1's block must stay byte-identical; only add.

1. **Positive: one increment per body-resolved path (spec AC 9).** `setupHandWrittenConflict("note.md", conflicted)` with an empty-ours body region (e.g. `---\npage_type: note\n---\n\n# Heading\n\nintro\n<<<<<<< HEAD\n=======\ntheirs line\n>>>>>>> theirs\n`). After `Resolve` succeeds assert `fakeMetrics.IncBodyMergedCallCount() == 1`, `fakeMetrics.IncAutoresolvedCallCount() == 0`, and `fakeMetrics.IncResolverFailureCallCount() == 0`.
2. **Positive: once per path across two paths.** Use `workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)` for the first body conflict, write a second body-conflict file `note2.md` into the same `workDir` with `os.WriteFile(filepath.Join(workDir, "note2.md"), …)`, then call `Resolve(ctx, []string{rel, "note2.md"})` and assert `IncBodyMergedCallCount() == 2`. Do not pre-stage `note2.md`; it is untracked, and the resolver's own `git add` stages it.
3. **Negative: deep-merge path leaves the counter at 0 (spec AC 11).** `setupYAMLConflictRepo("a: 1\n", "X", "b: 2\n", "Y")` resolves successfully through the deep-merge path; assert `IncBodyMergedCallCount() == 0`.
4. **Negative: scalar-frontmatter path leaves the counter at 0 (spec AC 11).** `setupHandWrittenConflict("note.md", conflicted)` with the scalar counter fixture above; `Resolve` succeeds, `IncAutoresolvedCallCount() == 1`, and `IncBodyMergedCallCount() == 0`.
5. **Negative: a rejected body shape never increments.** A boundary-crossing or non-markdown fixture that returns `ErrConflictResolutionFailed`; assert `IncBodyMergedCallCount() == 0` and `IncResolverFailureArgsForCall(0) == "no_frontmatter"`.

## 6. Self-check before finishing

Walk each requirement: the counter is on `/metrics` at zero before any event; it increments exactly once per body-resolved path and never on a rejection, a write failure, a `git add` failure, the deep-merge path, or the scalar rule; the two hand-written metrics implementations compile; the generated mock exposes the accessors.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT change the `ConflictResolver` interface (`pkg/git/conflict_resolver.go`) or `mocks/conflict_resolver.go`.
- Do NOT change the body rule's planning, splice, or eligibility logic landed by prompt 1 — this prompt only adds the increment call.
- Do NOT change the scalar-frontmatter rule, the deep-merge path, `MarkerResolver`, the quarantine path, `pkg/git/git.go`, or the pull loop.
- Do NOT add a second metric, a label, or a gauge. Only `git_rest_body_merged_total` is added.
- Do NOT change the existing `git_rest_resolver_failures_total` categories or their pre-initialisation, and do NOT change `git_rest_autoresolved_total`.
- Do NOT change the `merge: resolved=[…] quarantined=[…]` commit-message format or any existing resolver log line.
- Do NOT edit `CHANGELOG.md`, `docs/`, or `docs/deployment.md` in this prompt; they land in prompt 3.
- Do NOT edit `specs/`.
- Do NOT modify any `pkg/git/*_test.go` file other than permitted ADDITIONS to `pkg/git/yaml_merge_resolver_test.go` (and the two one-line hand-written metrics methods in `pkg/git/git_test.go` and `pkg/git/resolve_conflict_merge_test.go`, which the interface change requires).
- The committed `Describe("Resolve body-only conflict regions")` block must stay byte-identical.
- Errors via `github.com/bborbe/errors` — no `fmt.Errorf`, no bare `return err`.
- Keep `glog` in `pkg/git/yaml_merge_resolver.go`; do not introduce `slog`.
- No new dependency.
- `mocks/metrics.go` is generated — regenerate it with `make generate`; never hand-edit it.
- Do NOT add a scenario file under `scenarios/`.
- Do NOT run `go mod vendor`, and do NOT write `-mod=vendor` in any command — this repo does not commit `vendor/`.
- Interface → Constructor → Struct → Method; factory functions stay pure composition.
- Tests use Ginkgo v2 / Gomega and Counterfeiter mocks; `make precommit` is the gate.
</constraints>

<verification>
Run from the repo root. The daemon runs with `hideGit=true`, which masks `.git` inside the container — bare `git` commands die with `fatal: not a git repository`, so none appear in this block. `ROOTDIR=/workspace` is the dark-factory convention for that case; this repo's `Makefile` does not itself reference `ROOTDIR`, so the prefix is inert here and harmless.

```bash
ROOTDIR=/workspace make precommit
```

Must exit 0.

Focused runs while implementing (a `0 of 0` result means the focus string matched nothing — fix the focus, do not accept it):

```bash
go test ./pkg/metrics/... -mod=mod -ginkgo.focus='BodyMergedTotal'
go test ./pkg/git/... -mod=mod -ginkgo.focus='Body merge metric'
go test ./pkg/git/... -mod=mod -ginkgo.focus='body-only conflict regions'
```

The counter, the interface method, the regenerated mock and the increment are present (each `grep -n` must print at least one match):

```bash
grep -n 'git_rest_body_merged_total' pkg/metrics/metrics.go
grep -n 'BodyMergedTotal.Add(0)' pkg/metrics/metrics.go
grep -n 'IncBodyMerged' pkg/metrics/metrics.go
grep -n 'IncBodyMergedStub' mocks/metrics.go
grep -n 'func (fake \*FakeMetrics) IncBodyMergedCallCount' mocks/metrics.go
grep -n 'r.metrics.IncBodyMerged()' pkg/git/yaml_merge_resolver.go
grep -n 'func (n \*noopMetrics) IncBodyMerged' pkg/git/git_test.go
grep -n 'func (u \*unsafeTestMetrics) IncBodyMerged' pkg/git/resolve_conflict_merge_test.go
grep -n 'IncBodyMergedCallCount' pkg/git/yaml_merge_resolver_test.go
grep -n 'BodyMergedTotal' pkg/metrics/metrics_test.go
```

Boundary checks (each `! grep -q` must exit 0):

```bash
! grep -q 'git_rest_autoresolved_total.*label' pkg/metrics/metrics.go
! grep -q '^## Unreleased' CHANGELOG.md
```

Absence is written as `! grep -q`, never as `grep -c` — `grep -c` prints `0` and exits non-zero, so an absence assertion written that way fails the step it was meant to pass.

Self-check before finishing: re-run `ROOTDIR=/workspace make precommit`, then walk each requirement against the change.
</verification>
