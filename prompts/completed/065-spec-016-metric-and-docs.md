---
status: completed
spec: [016-yaml-scalar-conflict-autoresolve]
summary: Added pre-initialised git_rest_autoresolved_total counter, incremented it once per scalar-frontmatter auto-resolved path, regenerated the metrics mock, added tests for success/rejection/failure paths, and documented the rule in CHANGELOG and the deployment guide
execution_id: git-rest-scalar-autoresolve-exec-065-spec-016-metric-and-docs
dark-factory-version: v0.198.7
created: "2026-10-19T20:50:37Z"
queued: "2026-10-19T21:23:09Z"
started: "2026-10-19T21:45:10Z"
completed: "2026-10-19T21:47:00Z"
---

# Resolve scalar frontmatter conflicts: autoresolve counter, changelog and docs

<summary>
- Each path resolved by the scalar-frontmatter rule increments a dedicated counter.
- The counter is exposed on `/metrics` at zero before the first resolution ever happens.
- An operator can tell an auto-resolution from a quarantine without reading the pod log.
- The counter increments once per resolved path, only after the file is written and staged.
- A rejected or failed path never increments the counter.
- The changelog carries an unreleased entry naming the rule.
- The deployment guide's vault-write bullet documents which conflicts now resolve themselves and which still quarantine.
- No existing metric, label, log line or commit-message format changes.
</summary>

<objective>
Make scalar-frontmatter auto-resolution observable and documented: add a pre-initialised `git_rest_autoresolved_total` counter that increments once per resolved path after the write and `git add` both succeed, record the change in the changelog, and extend the deployment guide's vault-write bullet so operators know which conflict shapes now resolve themselves and which still quarantine.
</objective>

<context>
There is no repo-root `CLAUDE.md` in this worktree — do not look for one. The project conventions are restated in `<constraints>` below.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` — counter registration and pre-initialisation
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, `prometheus.DefaultGatherer` assertions
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` heading and `feat:` / `fix:` prefix rules
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — durable-doc expectations
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors`; never `fmt.Errorf`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — linter limits (funlen 80 lines / 50 statements, nestif 4; this repo's `.golangci.yml` enforces gofmt/goimports only, so the guide's golines 100 does not apply here)

Files to read in full before editing (anchor by symbol name, not line number):

- `pkg/metrics/metrics.go` — every counter, the `Metrics` interface, `prometheusMetrics`, and the `init()` block with its explicit `.Add(0)` pre-initialisations
- `pkg/metrics/metrics_test.go` — `gatherCounter`, `gatherGauge`, `gatherCounterPresent`, `gatherCounterVecLabelValue`
- `pkg/git/yaml_merge_resolver.go` — `applyScalarFrontmatterRule` (the write → `runGitAdd` sequence added by the previous prompts)
- `pkg/git/yaml_merge_resolver_test.go` — the `Describe("Scalar frontmatter rule", …)` block and its helpers
- `pkg/git/git_test.go` — `noopMetrics`, a hand-written `metrics.Metrics` implementation
- `pkg/git/resolve_conflict_merge_test.go` — `unsafeTestMetrics`, the second hand-written `metrics.Metrics` implementation
- `docs/deployment.md` — the `**Vault-write mode**` bullet under `## Operational notes` (the durable home for the rule)
- `CHANGELOG.md` — the newest heading today is `## v0.29.6`; there is no `## Unreleased` section, and no line in the file contains the word `scalar`

**Verified facts — do not re-derive, do not contradict:**

- `pkg/metrics/metrics.go` registers every series in a package `init()` and calls `.Add(0)` on each unlabeled counter explicitly, so `/metrics` exposes the series before the first event. The new counter must follow that pattern.
- The counterfeiter directive lives on the `Metrics` interface (`//counterfeiter:generate -o ../../mocks/metrics.go --fake-name FakeMetrics . Metrics`). `make generate` wipes `mocks/` and regenerates it from the `go:generate` directives, and `make precommit` runs `generate` before `test`.
- Adding a method to `metrics.Metrics` breaks every hand-written implementation. There are exactly two besides the generated mock and the Prometheus implementation: `noopMetrics` in `pkg/git/git_test.go` and `unsafeTestMetrics` in `pkg/git/resolve_conflict_merge_test.go`.
- `applyScalarFrontmatterRule` returns `(true, nil)` only after both `os.WriteFile` and `r.runGitAdd` succeed. That is the single point where a path has been auto-resolved.
- The generated mock exposes `IncAutoresolvedStub`, `IncAutoresolvedCallCount()` and `IncAutoresolvedCalls(stub func())` once regenerated, matching the shape of the existing `IncPullRescueStub` / `IncPullRescueCallCount()` / `IncPullRescueCalls(...)` accessors. Counterfeiter emits no `ArgsForCall` accessor for a method with no parameters.
- The existing `**Vault-write mode**` bullet does not contain the phrase `human inspection`; the `**Conflict handling**` bullet above it does. The spec requires the phrase `scalar frontmatter` and the word `auto-resolve` in the deployment guide, and requires that `scalar frontmatter` is never followed by `human inspection` on the same line.
- The commit-message format `merge: resolved=[…] quarantined=[…]` and the existing resolver log lines are fixed by earlier specs and asserted by existing tests.
</context>

<requirements>

## 1. Add the counter

In `pkg/metrics/metrics.go`, add next to the other counters:

```go
// AutoresolvedTotal counts conflicted paths resolved by the scalar-frontmatter
// rule (a conflict confined to single-line scalar frontmatter keys) during pull.
var AutoresolvedTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "git_rest_autoresolved_total",
	Help: "Total conflicted paths auto-resolved by the scalar-frontmatter rule during pull.",
})
```

- Register it in the existing `prometheus.MustRegister(...)` call in `init()`.
- Add an explicit `AutoresolvedTotal.Add(0)` in `init()` with a comment mirroring the neighbouring `QuarantinedFilesTotal.Add(0)` / `PullRescuesTotal.Add(0)` comments: the series must be present on `/metrics` at zero before the first resolution.
- Do not add a label. This is an unlabeled counter.

## 2. Expose it on the metrics interface

Add to the `Metrics` interface, next to `IncPullRescue`:

```go
// IncAutoresolved records one conflicted path auto-resolved by the
// scalar-frontmatter rule. Called once per path, after the file is written and
// staged.
IncAutoresolved()
```

Implement it on `prometheusMetrics`:

```go
func (p *prometheusMetrics) IncAutoresolved() {
	AutoresolvedTotal.Inc()
}
```

Then run `make generate` to regenerate `mocks/metrics.go` from the counterfeiter directive. Do not hand-edit the generated mock; confirm the regenerated file contains `IncAutoresolvedStub`, `IncAutoresolvedCallCount()` and `IncAutoresolvedCalls(stub func())` — there is no `IncAutoresolvedArgsForCall` accessor because the method takes no arguments.

## 3. Update the two hand-written metrics implementations

- In `pkg/git/git_test.go`, add `func (n *noopMetrics) IncAutoresolved() {}` beside `IncPullRescue`.
- In `pkg/git/resolve_conflict_merge_test.go`, add `func (u *unsafeTestMetrics) IncAutoresolved() {}` beside `IncPullRescue`.

Do not change any other behavior in those files.

## 4. Increment once per resolved path

In `pkg/git/yaml_merge_resolver.go`, in `applyScalarFrontmatterRule`, call `r.metrics.IncAutoresolved()` exactly once, after both `os.WriteFile` and `r.runGitAdd(ctx, path)` have succeeded, immediately before `return true, nil`.

It must NOT fire on:

- the not-applicable path (`return false, nil`),
- the `write_failed` path,
- the `git_add_failed` path.

Do not increment any resolver-failure category on the success path.

## 5. Tests

1. **Pre-initialisation and increment** in `pkg/metrics/metrics_test.go`, following the `PullRescuesTotal` block:

   ```go
   var _ = Describe("AutoresolvedTotal", func() {
       It("is registered with value 0 in the default registry at init() time", func() {
           value, found := gatherCounterPresent("git_rest_autoresolved_total")
           Expect(found).To(BeTrue(), "the counter series must exist at init() time")
           Expect(value).To(Equal(0.0))
       })

       It("increments when IncAutoresolved is called via the interface", func() {
           before := gatherCounter("git_rest_autoresolved_total")
           m := metrics.NewMetrics()
           m.IncAutoresolved()
           after := gatherCounter("git_rest_autoresolved_total")
           Expect(after - before).To(Equal(1.0))
       })
   })
   ```

2. **Success path** in `pkg/git/yaml_merge_resolver_test.go`, inside `Describe("Scalar frontmatter rule", …)`: after a successful `Resolve` of a scalar counter conflict, `fakeMetrics.IncAutoresolvedCallCount() == 1` and `fakeMetrics.IncResolverFailureCallCount() == 0`.
3. **Once per path**: write a second conflicted file in the same temp repo (`note2.md`, same shape), call `Resolve(ctx, []string{rel, "note2.md"})`, and assert `fakeMetrics.IncAutoresolvedCallCount() == 2`. `git add` works for the untracked second file, so no extra fixture step is needed.
4. **Failure and rejection paths**: for a rejection fixture (e.g. a list-item conflict) assert `fakeMetrics.IncAutoresolvedCallCount() == 0` and `fakeMetrics.IncResolverFailureArgsForCall(0) == "no_frontmatter"`. For a `git_add_failed` fixture, build a **counter-only** scalar conflict (`trigger_count: 3` vs `0`, so the lazy merge-parent timestamp getter added by the previous prompt is never called) and then remove `.git` after building the repo, as the existing `AC-git-add-failed` spec does. Do not reuse that spec's fixture shape (`a: 1` vs `b: 2`): its key sets differ, so the scalar rule rejects it, and a non-counter fixture with matching keys fails the timestamp lookup after `.git` is removed — both report `no_frontmatter` without ever reaching `git add`. Assert `IncAutoresolvedCallCount() == 0` and `IncResolverFailureArgsForCall(0) == "git_add_failed"`. For a `write_failed` fixture (chmod the conflicted file to `0o400` after building the counter-only conflict) assert `IncAutoresolvedCallCount() == 0` and `IncResolverFailureArgsForCall(0) == "write_failed"`; skip this fixture with `Skip("requires a non-root user")` when `os.Geteuid() == 0`, because root bypasses the permission check and would resolve successfully.

## 6. Changelog

`CHANGELOG.md` has no `## Unreleased` section today. Add one at the top of the version list (immediately after the preamble, above `## v0.29.6`) containing exactly one bullet:

```
- feat: Auto-resolve merge conflicts confined to single-line scalar frontmatter keys. A conflict whose hunks lie entirely inside a markdown file's YAML frontmatter and whose lines are all `key: value` scalars now resolves in place instead of quarantining: allowlisted counters (`trigger_count`, `retry_count`, `metrics_*`) take the greater integer, every other scalar takes the newer merge parent's value, and each resolved path increments `git_rest_autoresolved_total`. Non-scalar hunks, hunks outside the frontmatter, one-sided keys and duplicate keys keep quarantining.
```

Do not touch any released section, and do not add a second bullet. The bullet must contain the word `scalar` (the acceptance check greps for it).

## 7. Deployment guide

In `docs/deployment.md`, extend the `**Vault-write mode**` bullet under `## Operational notes` — do not create a new heading and do not touch the `**Conflict handling**` bullet above it. Insert one sentence after the existing deep-merge sentence (`... the resolver deep-merges frontmatter keys (theirs wins on overlap) and combines bodies, producing a syntactically valid file.`):

> A **scalar frontmatter** conflict — one whose hunks lie entirely inside the frontmatter and whose lines are all single-line `key: value` scalars — is auto-resolved in place instead of quarantined: allowlisted counters (`trigger_count`, `retry_count`, `metrics_*`) take the greater integer, every other scalar takes the newer merge parent's value (a tie goes to the local side), and `git_rest_autoresolved_total` increments once per resolved path. Every other shape — non-scalar hunks, hunks outside the frontmatter, one-sided keys, duplicate keys — keeps the quarantine path described below.

Requirements on the text:

- It must contain the exact phrase `scalar frontmatter` and the word `auto-resolve`.
- The phrase `scalar frontmatter` must never be followed on the same line by `human inspection` — a human-inspection sentence belongs to the rebase conflict bullet, not to this rule.
- Do not describe the deep-merge path as changed; the spec deliberately leaves `deepMergeTheirsWins` (theirs-wins) untouched.

## 8. Self-check before finishing

Run every command in `<verification>` from the repo root and confirm each result. Then walk each requirement: the counter is on `/metrics` at zero before any event, it increments exactly once per auto-resolved path and never on a rejection or failure, the changelog has one unreleased bullet naming the rule, the deployment guide documents which shapes resolve and which quarantine, and no existing metric, label, log line or commit-message format changed.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT change the `ConflictResolver` interface (`pkg/git/conflict_resolver.go`) or `mocks/conflict_resolver.go`; both stay byte-identical.
- Do NOT change the resolver's per-key rule, the eligibility guard, the splice, or the timestamp getter landed by the previous prompts — this prompt only adds the increment call.
- Do NOT change `MarkerResolver`, the quarantine path, `pkg/git/git.go`, or the pull loop.
- Do NOT add a second metric, a label, or a gauge. Only `git_rest_autoresolved_total` is added.
- Do NOT change the existing `git_rest_resolver_failures_total` categories or their pre-initialisation.
- Do NOT change the `merge: resolved=[…] quarantined=[…]` commit-message format or any existing resolver log line.
- Do NOT change the public HTTP contract in `docs/api.md`.
- Do NOT edit `specs/`.
- The changelog gets exactly one new `## Unreleased` bullet; released sections stay untouched.
- The deployment-guide edit is confined to the `**Vault-write mode**` bullet; do not create a heading and do not touch the `**Conflict handling**` bullet.
- Errors via `github.com/bborbe/errors` — no `fmt.Errorf`, no bare `return err`.
- Keep `glog` in `pkg/git/yaml_merge_resolver.go`; do not introduce `slog` there and do not mix loggers.
- No new dependency; no per-repository or per-key configuration.
- Do NOT add a scenario file under `scenarios/`.
- Do NOT run `go mod vendor`, and do NOT write `-mod=vendor` in any command — this repo does not commit `vendor/`.
- `mocks/metrics.go` is generated — regenerate it with `make generate`; never hand-edit it.
- Interface → Constructor → Struct → Method; factory functions stay pure composition.
- Tests use Ginkgo v2 / Gomega and Counterfeiter mocks; `make precommit` is the gate.
</constraints>

<verification>
Run from the repo root. The daemon may run with `hideGit=true`, which masks `.git` inside the container — under that setting bare `git` commands die with `fatal: not a git repository`, so do not put one in this block. `ROOTDIR=/workspace` is the dark-factory convention for that case; this repo's `Makefile` does not itself reference `ROOTDIR`, so the prefix is inert here and harmless.

```bash
ROOTDIR=/workspace make precommit
```

Must exit 0.

Focused run while implementing (a `0 of 0` result means the focus string matched nothing — fix the focus, do not accept it):

```bash
go test ./pkg/metrics/... -mod=mod -ginkgo.focus='AutoresolvedTotal'
go test ./pkg/git/... -mod=mod -ginkgo.focus='Scalar frontmatter rule'
```

The counter, the interface method and the regenerated mock are present (each `grep -n` must print at least one match):

```bash
grep -n 'git_rest_autoresolved_total' pkg/metrics/metrics.go
grep -n 'IncAutoresolved' pkg/metrics/metrics.go
grep -n 'IncAutoresolvedStub' mocks/metrics.go
grep -n 'func (fake \*FakeMetrics) IncAutoresolvedCallCount' mocks/metrics.go
grep -n 'IncAutoresolved' pkg/git/yaml_merge_resolver.go
grep -n 'func (n \*noopMetrics) IncAutoresolved' pkg/git/git_test.go
grep -n 'func (u \*unsafeTestMetrics) IncAutoresolved' pkg/git/resolve_conflict_merge_test.go
grep -n 'IncAutoresolvedCallCount' pkg/git/yaml_merge_resolver_test.go
grep -n 'AutoresolvedTotal' pkg/metrics/metrics_test.go
```

The changelog check — the section is new and is the topmost version heading:

```bash
grep -n '^## Unreleased' CHANGELOG.md
awk '/^## /{sec=$0} /^## Unreleased/{print "first section: " sec; exit}' CHANGELOG.md
awk '/^## /{s=$0} /scalar/{print s}' CHANGELOG.md
```

The first must print one match; the second must print `first section: ## Unreleased`; the third must print `## Unreleased` (and only that, because no released section mentions `scalar`).

The deployment guide documents the rule (each `grep -n` must print at least one match):

```bash
grep -n 'scalar frontmatter' docs/deployment.md
grep -n 'auto-resolve' docs/deployment.md
grep -n 'git_rest_autoresolved_total' docs/deployment.md
```

Absence and frozen-surface checks (each `! grep -q` must exit 0; each `grep -n` must print at least one match):

```bash
! grep -q 'scalar frontmatter.*human inspection' docs/deployment.md
grep -n 'deepMergeTheirsWins(oursMap, theirsMap)' pkg/git/yaml_merge_resolver.go
grep -n 'ConflictResolver interface' pkg/git/conflict_resolver.go
ls mocks/conflict_resolver.go
```

Absence is written as `! grep -q`, never as `grep -c` — `grep -c` prints `0` and exits non-zero, so an absence assertion written that way fails the step it was meant to pass.

Self-check before finishing: re-run `ROOTDIR=/workspace make precommit`, then walk each requirement above against the change — the counter is pre-initialised at zero, increments once per resolved path, never increments on a rejection or a `git add` failure, the changelog has one unreleased bullet, and the deployment guide documents the rule without moving the human-inspection sentence.
</verification>
