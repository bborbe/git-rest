---
status: completed
summary: Resolved mixed frontmatter/body conflict regions by classifying each region on its own position via planConflictPartitions, unifying scalar and body rules into applyPartitionedRules with a single write/stage and per-half metrics.
execution_id: git-rest-exec-099-mixed-frontmatter-and-body-conflict-regions
dark-factory-version: v0.198.17
created: "2026-11-05T12:40:00Z"
queued: "2026-11-05T11:39:03Z"
started: "2026-11-05T11:46:22Z"
completed: "2026-11-05T11:50:06Z"
---

# Resolve a file whose conflict regions split across the frontmatter and the body

<summary>
- A note whose conflict regions fall partly in the frontmatter and partly in the body is now resolved instead of quarantined.
- Each conflict region is classified on its own position; the file is no longer rejected because one rule cannot claim every region.
- The frontmatter half keeps its existing rules — counter keys take the maximum, other keys follow the newer-side rule, one-sided keys need the merge base.
- The body half keeps its existing rule — both sides are kept, separated by a blank line.
- A region that genuinely straddles the closing `---` still quarantines the file, because it mixes a frontmatter key with body text and no rule can split it.
- A decline on either half still writes nothing, so a partial file is never produced.
- The whole file is written and staged exactly once per resolve, whichever halves it carries.
- The resolver-failure category reported for a straddling or declined file is unchanged.
</summary>

<objective>
Resolve a Markdown note that carries one conflict region inside its frontmatter and another inside its body, so the vault stops quarantining files that the resolver can in fact merge — the two existing rules each demand that *every* region lie in their own half, so a file that satisfies both halves satisfies neither and is written to `_conflicts/`.
</objective>

<context>
Read `docs/dod.md` for the project's Definition of Done, then read these before changing anything.

- `pkg/git/yaml_merge_resolver.go` — the whole fallback path. The relevant anchors are:
  - `resolveOne` — calls `applyFallbackRules` when the YAML deep-merge path cannot handle the file.
  - `conflictRegion` — `startLine` is the index of the `<<<<<<<` line, `endLine` the index of the `>>>>>>>` line, both inclusive and both indices into the *original* file.
  - `enumerateConflictRegions` — returns every region in file order; `ok` is false on unbalanced or nested markers, or when the file holds no region.
  - `frontmatterBounds` — returns the index of the closing `---`. The opening delimiter is line 0.
  - `planScalarRegions` / `regionInsideFrontmatter` and `planBodyRegions` / `regionInsideBody` — the two per-file vetoes this change replaces. Note the shape of the veto: each iterates **all** regions and returns false the moment one region fails its own half's test.
  - `applyScalarFrontmatterRule` — the scalar machinery: `parseScalarSide`, `seedEmittedOutsideRegions`, `resolveScalarRegion`, `spliceScalarRegions`, then one write and one `runGitAdd`, then `IncAutoresolved`.
  - `applyBodyRule` — `combineBodies`, `resolvedBodyLines`, `spliceConflictRegions`, one write and one `runGitAdd`, then `IncBodyMerged`.
  - `applyFallbackRules` — tries the scalar rule, then the body rule.
  - `resolveScalarEntry` / `isCounterKey` — a shared counter key (`trigger_count`, `retry_count`, `metrics_*`) resolves to the larger integer without consulting the merge base or the merge-parent timestamps.
  - `combineBodies` — ours and theirs both non-empty and different yields `ours + "\n\n" + theirs`.
- `pkg/git/yaml_merge_resolver_test.go` — read `setupHandWrittenConflict` and the `Describe("Scalar frontmatter rule")` and `Describe("Body merge metric")` blocks. These are the patterns the new tests follow. Three existing specs must keep passing unchanged and are the reason the straddling case stays a decline:
  - `It("rejects a region that crosses the frontmatter/body boundary")` in `Describe("Scalar frontmatter rule")`.
  - `It("rejects the whole file when a region crosses the boundary")` in `Describe("Body-only conflict regions: extended")`.
  - `It("does not increment when the body shape is rejected")` in `Describe("Body merge metric")` — its first region starts above the closing delimiter and ends below it.
- `CHANGELOG.md` — there is no `## Unreleased` section right now; the top section is `## v0.40.3`.

Coding guides (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors` only, never `fmt.Errorf`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 / Gomega, external test package `git_test`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — this file logs with `github.com/golang/glog`; do not migrate it to `log/slog`.
</context>

<requirements>

## 1. Classify per region instead of per file (`pkg/git/yaml_merge_resolver.go`)

1.1 Add:

```go
func planConflictPartitions(lines []string) (frontmatter, body []conflictRegion, ok bool)
```

Contract:

- `frontmatterBounds` must succeed and `enumerateConflictRegions` must succeed; otherwise `ok` is false.
- A region belongs to `frontmatter` when `region.endLine < closeIndex`.
- A region belongs to `body` when `region.startLine > closeIndex`.
- Any other region straddles the closing delimiter and makes the whole call return `ok == false`. A straddling region is genuinely unresolvable — its ours-side mixes a frontmatter key with body text — and it is a different shape from two disjoint regions.
- Both slices keep file order. At least one region must be returned; `ok` is false for a file with no region.
- Pure shape inspection, no I/O, no timestamps.

1.2 Extract the scalar resolve loop out of `applyScalarFrontmatterRule` into:

```go
func (r *yamlMergeResolver) resolveScalarRegions(
	ctx context.Context, path string, lines []string, regions []conflictRegion,
) ([][]string, bool)
```

- Returns one replacement `[]string` per input region, in the same order, and `ok == true`.
- `ok == false` on any decline, with nothing written. Every decline `applyScalarFrontmatterRule` produces today stays a decline: an unparseable side, a side with no key, a duplicate key within a side, an undecidable merge-parent timestamp, an unreadable merge base for a one-sided key, a key emitted twice, or a key emitted that an untouched line already carries.
- It owns the `newerTheirs` and `loadBase` lazily-initialised closures exactly as `applyScalarFrontmatterRule` does today, so a file whose regions all resolve without needing the base never reads it.
- It parses each region with `parseScalarSide` and builds the `scalarRegion` values itself; `seedEmittedOutsideRegions` is called once with the frontmatter set.
- `ctx` cancellation is checked once by the caller, not per region.

1.3 Replace `applyFallbackRules` with:

```go
func (r *yamlMergeResolver) applyPartitionedRules(
	ctx context.Context, path, abs, raw string,
) (applied bool, err error)
```

Sequence:

1. Return `false, nil` for a non-`.md` path, as both current rules do.
2. `planConflictPartitions`. If `ok == false`, return `false, nil` — the caller records `no_frontmatter` and quarantines, unchanged.
3. If `frontmatter` is non-empty, call `resolveScalarRegions`. On `ok == false`, return `false, nil` — a frontmatter decline aborts the whole file and nothing is written.
4. Build the body replacements with `combineBodies` and `resolvedBodyLines`, one per body region.
5. Concatenate: the `conflictRegion` values are the frontmatter set followed by the body set (file order holds — every frontmatter region ends above `closeIndex`, every body region starts below it), and the replacements are concatenated in the same order.
6. Splice once with `spliceConflictRegions`.
7. One `ctx.Done()` check, one `os.WriteFile` with `normalizeTaskPhase`, one `r.runGitAdd`. A write failure records `resolverFailureWrite` and returns `errors.Wrap(ctx, ErrConflictResolutionFailed, …)` with `applied == true`, matching the current rules.
8. Metrics, each at most once per resolved file: `IncAutoresolved()` when the frontmatter set was non-empty, `IncBodyMerged()` when the body set was non-empty. A file carrying both halves increments both.

1.4 `resolveOne` must call `applyPartitionedRules` at **both** sites where it calls `applyFallbackRules` today: after `splitConflictSides` fails, and after `splitFrontmatter` fails on either side. Its quarantine branch, its failure categories and its log lines do not change.

1.5 Delete what the change makes unreachable: `applyScalarFrontmatterRule`, `applyBodyRule`, `applyFallbackRules`, `planScalarRegions`, `planBodyRegions`, `regionInsideFrontmatter`, `regionInsideBody`, `spliceScalarRegions`, and the `bodyRegion` type. Keep `scalarRegion` — `resolveScalarRegion` and `seedEmittedOutsideRegions` still take it. Deleting `spliceScalarRegions` also removes the duplicate of `spliceConflictRegions`; do not introduce a third copy of that loop. Update the comment above the `It("AC: declines the file when a region re-emits a key already outside it")` spec in `yaml_merge_resolver_test.go`, which names `spliceScalarRegions`, to name `spliceConflictRegions` instead.

1.6 Do not change: `Resolve`, `resolveOne`'s deep-merge path, `splitConflictSides`, `splitFrontmatter`, `deepMergeTheirsWins`, `parseScalarEntry`, `resolveScalarEntry`, `isCounterKey`, `combineBodies`, `spliceConflictRegions`, `seedEmittedOutsideRegions`, `resolveScalarRegion`, `baseKeySet`, `oneSidedKeyIsAdd`, `normalizeTaskPhase`, or any quarantine code in `pkg/git/git.go`.

## 2. Tests (`pkg/git/yaml_merge_resolver_test.go`)

Add a new `Describe("Mixed frontmatter and body conflict regions", …)` block after `Describe("Body merge metric")`, using `setupHandWrittenConflict` and the `ctx` and `fakeMetrics` declared in the outer `Describe("YAMLMergeResolver")` block, as the sibling blocks do.

2.1 Resolves the real shape. This is the shape that produced the quarantine: a counter region in the frontmatter and a prose region in the body. Bind the fixture with `workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)` and `defer cleanup()`.

```go
conflicted := "---\n" +
	"title: probe\n" +
	"<<<<<<< HEAD\n" +
	"trigger_count: 8\n" +
	"=======\n" +
	"trigger_count: 1\n" +
	">>>>>>> theirs\n" +
	"status: in_progress\n" +
	"---\n" +
	"\n" +
	"# Heading\n" +
	"\n" +
	"<<<<<<< HEAD\n" +
	"ours body\n" +
	"=======\n" +
	"theirs body\n" +
	">>>>>>> theirs\n" +
	"\n" +
	"tail\n"
```

Assert `Resolve` succeeds, and the written file equals:

```go
"---\ntitle: probe\ntrigger_count: 8\nstatus: in_progress\n---\n\n# Heading\n\n" +
	"ours body\n\ntheirs body\n\ntail\n"
```

The counter resolves to the maximum because `isCounterKey("trigger_count")` is true, so the file resolves with no merge parent present — `setupHandWrittenConflict` writes no `MERGE_HEAD`.

Then assert the file is staged, not merely written — `runGitAdd` is a subprocess boundary and this path is new. Do **not** copy the `--diff-filter=U` form from `It("stages a body-only region in a genuine conflicted merge")`: that spec uses `setupYAMLConflictRepo`, which leaves a real unmerged entry, while `setupHandWrittenConflict` stages the conflicted file itself and is never unmerged — `--diff-filter=U` is empty before the resolver runs and stays empty even if `runGitAdd` is never called. Compare the worktree against the index instead:

```go
diffCmd := exec.Command("git", "diff", "--name-only")
diffCmd.Dir = workDir
diffOut, err := diffCmd.CombinedOutput()
Expect(err).NotTo(HaveOccurred())
Expect(strings.TrimSpace(string(diffOut))).To(BeEmpty())
```

`setupHandWrittenConflict` stages the conflicted content, so a resolve that skips `runGitAdd` leaves worktree ≠ index and this line prints `note.md`. `exec` and `strings` are already imported in the test file.

2.2 Increments both counters once for that file, and neither failure counter.

2.3 The frontmatter half declining aborts the whole file. Use a shared **non-counter** key, which reaches `resolveScalarEntry`'s `newerTheirs()` and declines because `setupHandWrittenConflict` writes no `MERGE_HEAD`, so `sideTimes` cannot resolve the merge-parent timestamps:

```go
conflicted := "---\n" +
	"title: probe\n" +
	"<<<<<<< HEAD\n" +
	"assignee: alice\n" +
	"=======\n" +
	"assignee: bob\n" +
	">>>>>>> theirs\n" +
	"status: in_progress\n" +
	"---\n" +
	"\n" +
	"<<<<<<< HEAD\n" +
	"ours body\n" +
	"=======\n" +
	"theirs body\n" +
	">>>>>>> theirs\n"
```

Assert `Resolve` returns an error matching `git.ErrConflictResolutionFailed`, `fakeMetrics.IncResolverFailureArgsForCall(0)` is `"no_frontmatter"`, `fakeMetrics.IncBodyMergedCallCount()` is `0`, and the file bytes are unchanged — the body region must not have been written. Capture the bytes before the call and compare after, as the existing quarantine specs do.

2.4 Add no new straddling spec — three existing ones already pin that shape and must keep passing unchanged: `It("rejects a region that crosses the frontmatter/body boundary")` in `Describe("Scalar frontmatter rule")`, `It("rejects the whole file when a region crosses the boundary")` in `Describe("Body-only conflict regions: extended")`, and `It("does not increment when the body shape is rejected")` in `Describe("Body merge metric")`. Do not weaken any of them; they are the guard that the per-region rule never starts accepting a straddler.

Do not modify any existing spec's assertions or fixtures; the only permitted test-file edit outside the new block is the comment rename in 1.5.

## 3. Changelog (`CHANGELOG.md`)

3.1 Insert a `## Unreleased` section directly above `## v0.40.3`, after the SemVer preamble. Add exactly one bullet:

```
- fix: Resolve a Markdown note whose conflict regions split across the frontmatter and the body instead of quarantining it, by classifying each region on its own position rather than requiring every region to lie in one half
```

Do not add any other bullet and do not touch a released section.

## 4. Self-check before finishing

4.1 Re-run `<verification>` and confirm every line passes.

4.2 Walk each `<summary>` bullet against the change. In particular confirm that a declined frontmatter half leaves the file byte-identical — the "nothing is written" property is the one a careless refactor loses.
</requirements>

<constraints>
- The deep-merge path (`Resolve` → `splitConflictSides` → `splitFrontmatter` → `parseYAML` → `deepMergeTheirsWins` → `combineBodies`) must not change behaviour.
- The quarantine path, its `no_frontmatter` failure category and its log lines must not change.
- A file that resolves today must produce byte-identical output after the change.
- A file that quarantines today must still quarantine, with the same failure category.
- No new sentinel, configuration field, flag or metric label. `git_rest_resolver_failures_total{category}` and `git_rest_resolved_total`-style counters keep their meaning.
- Errors are wrapped with `github.com/bborbe/errors`, never `fmt.Errorf`.
- Non-test `pkg/git` code must not call `context.Background()`; contexts are passed in.
- `pkg/git/yaml_merge_resolver.go` uses `github.com/golang/glog` for logging. Keep it — do not migrate this file to `slog`.
- Tests are Ginkgo v2 / Gomega, external test package `git_test`.
- Precommit budgets: funlen 80/50, gocognit 20, nestif 4, maintidx 20, dupl, golines 100. `applyPartitionedRules` and `resolveScalarRegions` must each stay under these; if a function exceeds one, extract a named helper rather than nesting.
- `.dark-factory.yaml` sets `workflow: direct`, `pr: false`, `autoRelease: false`: commit to the branch directly; no PR or auto-release is expected.
- Do NOT commit — dark-factory handles git.
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

Then confirm the observable outcomes — every line must print `ok`:

```
test "$(grep -cF 'func planConflictPartitions(' pkg/git/yaml_merge_resolver.go)" = "1" && echo ok
test "$(grep -cF 'func (r *yamlMergeResolver) applyPartitionedRules(' pkg/git/yaml_merge_resolver.go)" = "1" && echo ok
test "$(grep -cF 'func (r *yamlMergeResolver) resolveScalarRegions(' pkg/git/yaml_merge_resolver.go)" = "1" && echo ok
! grep -qF 'func (r *yamlMergeResolver) applyScalarFrontmatterRule(' pkg/git/yaml_merge_resolver.go && echo ok
! grep -qF 'func (r *yamlMergeResolver) applyBodyRule(' pkg/git/yaml_merge_resolver.go && echo ok
! grep -qF 'func (r *yamlMergeResolver) applyFallbackRules(' pkg/git/yaml_merge_resolver.go && echo ok
! grep -qF 'func planScalarRegions(' pkg/git/yaml_merge_resolver.go && echo ok
! grep -qF 'func planBodyRegions(' pkg/git/yaml_merge_resolver.go && echo ok
! grep -qF 'func spliceScalarRegions(' pkg/git/yaml_merge_resolver.go && echo ok
test "$(grep -cF 'Describe("Mixed frontmatter and body conflict regions"' pkg/git/yaml_merge_resolver_test.go)" = "1" && echo ok
test "$(grep -cF 'rejects a region that crosses the frontmatter/body boundary' pkg/git/yaml_merge_resolver_test.go)" = "1" && echo ok
test "$(grep -cF 'rejects the whole file when a region crosses the boundary' pkg/git/yaml_merge_resolver_test.go)" = "1" && echo ok
test "$(grep -cF 'does not increment when the body shape is rejected' pkg/git/yaml_merge_resolver_test.go)" = "1" && echo ok
test "$(grep -c '^## Unreleased' CHANGELOG.md)" = "1" && echo ok
! grep -qF 'fmt.Errorf' pkg/git/yaml_merge_resolver.go && echo ok
! grep -qF 'slog' pkg/git/yaml_merge_resolver.go && echo ok
```
</verification>
