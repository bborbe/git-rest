---
status: completed
spec: [024-bug-one-sided-frontmatter-adds-quarantine]
summary: Added a per-file emitted-key set so a frontmatter key produced by two conflict regions declines the file via the existing fail-closed path, and added regression coverage for both the one-sided and shared-key arms.
execution_id: git-rest-frontmatter-union-exec-094-spec-024-decline-cross-region-duplicate-key
dark-factory-version: v0.198.7
created: "2026-11-02T20:04:44Z"
queued: "2026-11-02T20:17:42Z"
started: "2026-11-02T21:10:08Z"
completed: "2026-11-02T21:14:52Z"
---

# Decline a frontmatter file whose regions would emit the same key twice

<summary>
- A file whose conflict regions would together write the same frontmatter key more than once is now quarantined instead of being merged into invalid YAML.
- The duplicate-key check spans every conflict region of one file, not just the keys inside a single region.
- A declined file takes the existing fail-closed path: the same sentinel error, the same failure category, and the file left byte-identical with no partial write.
- Files whose regions carry distinct keys keep resolving exactly as they do today, including the union case and the counter and newer-side rules.
- No new exported symbol, configuration knob, metric, or log category is introduced.
- The hand-written "one-sided key" quarantine fixture is corrected so its shared key resolves without timestamps, which makes the one-sided key the deciding input again instead of an unreached branch.
- A new regression case proves the cross-region duplicate shape declines and that the fix is not over-broad.
</summary>

<objective>
Close the correctness regression the union rule introduced: a key that one side adds in one conflict region and the other side adds in a separate region was emitted twice, producing frontmatter that the next YAML unmarshal rejects. The resolver must decline such a file through its existing fail-closed path, so a shape that used to quarantine never silently becomes invalid output.
</objective>

<context>
Read `docs/dod.md` and `.dark-factory.yaml` for project conventions, then read these files before changing anything:

- `pkg/git/yaml_merge_resolver.go` — the whole file. The code this prompt changes is `resolveScalarRegion` and `applyScalarFrontmatterRule`. Read `oneSidedKeyIsAdd`, `baseKeySet`, `parseScalarOurs`, `parseScalarTheirs`, `resolveScalarEntry`, `spliceScalarRegions` and `resolveOne` so the decline path is unambiguous.
- `pkg/git/yaml_merge_resolver_test.go` — helpers `setupHandWrittenConflict` (markers written by hand, no `MERGE_HEAD`, no index stages), `scalarFixture`, and the `Describe("Scalar frontmatter rule")` block. In particular the `DescribeTable("AC4: rejection shapes keep quarantining", …)` entries and the two-region spec `It("resolves a union region and a counter region in the same file", …)`.
- `pkg/git/export_test.go` — `NewYAMLMergeResolverWithBase(repoPath, metrics, base, baseOK)` stubs the merge-base content and makes the merge-parent timestamps unavailable; use it for the new regression case so the fixture exercises the union rule without a real index stage 1.
- `specs/in-progress/024-bug-one-sided-frontmatter-adds-quarantine.md` — `## Desired Behavior` items 4 and 5, `## Constraints`, `## Failure Modes`.

Coding guides (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors` only, never `fmt.Errorf`, never `context.Background()` in non-test `pkg/` code.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — this file logs with `github.com/golang/glog`, not `log/slog`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 / Gomega conventions, external test package `git_test`, `DescribeTable` for shape matrices.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` — small unexported helpers, one decision per function.
</context>

<requirements>
## 1. Track emitted keys across every region of one file (`pkg/git/yaml_merge_resolver.go`)

1.1 Add an unexported key-set type and its recording method immediately above `resolveScalarRegion`:

```go
// emittedKeys records the frontmatter keys already written by the resolved
// regions of one file. A key emitted by two regions would leave a duplicate
// mapping key that the next yaml.Unmarshal of the frontmatter rejects, so the
// second occurrence declines the whole file instead of writing invalid YAML.
type emittedKeys map[string]struct{}

// record returns false when key was already emitted.
func (e emittedKeys) record(key string) bool {
	if _, duplicate := e[key]; duplicate {
		return false
	}
	e[key] = struct{}{}
	return true
}
```

1.2 Change `resolveScalarRegion` to take the shared set as a fourth parameter and to record every key it emits. The set is per file, never per region, so it must not be re-created inside this function:

```go
func resolveScalarRegion(
	region scalarRegion,
	newerTheirs func() (theirsNewer bool, ok bool),
	loadBase func() (map[string]struct{}, bool),
	emitted emittedKeys,
) ([]string, bool) {
```

Behaviour, keeping the existing order and the existing decline semantics:

- Shared key: call `resolveScalarEntry` as today, then decline when either it reports not-ok or `emitted.record(key)` reports a duplicate.
- Ours-only key: decline when `oneSidedKeyIsAdd` is false or `emitted.record(key)` reports a duplicate.
- Theirs-only key: the same pair of checks, after the existing `oursKeys` skip.
- A decline returns `nil, false` and writes nothing. Entries already recorded in the set are irrelevant because the whole file declines.

The three call sites read:

```go
		if theirsEntry, shared := theirsByKey[oursEntry.key]; shared {
			line, ok := resolveScalarEntry(
				oursEntry.key,
				oursEntry.entry,
				theirsEntry,
				newerTheirs,
			)
			if !ok || !emitted.record(oursEntry.key) {
				return nil, false
			}
			resolved = append(resolved, line)
			continue
		}
		if !oneSidedKeyIsAdd(loadBase, oursEntry.key) || !emitted.record(oursEntry.key) {
			return nil, false
		}
		resolved = append(resolved, oursEntry.entry.line)
```

and, in the theirs loop:

```go
		if !oneSidedKeyIsAdd(loadBase, theirsEntry.key) ||
			!emitted.record(theirsEntry.key) {
			return nil, false
		}
		resolved = append(resolved, theirsEntry.entry.line)
```

1.3 Create the set once per file in `applyScalarFrontmatterRule` and pass it to every region:

```go
	emitted := make(emittedKeys, len(regions))
	replacements := make([][]string, 0, len(regions))
	for _, region := range regions {
		resolved, resolvedOK := resolveScalarRegion(region, newerTheirs, loadBase, emitted)
		if !resolvedOK {
			return false, nil
		}
		replacements = append(replacements, resolved)
	}
```

The decline outcome stays exactly what it is today: `applyScalarFrontmatterRule` returns `false, nil`, `resolveOne` records `no_frontmatter` and returns `ErrConflictResolutionFailed`, and no byte of the file is written or staged.

1.4 Update the doc comments. `resolveScalarRegion`'s comment must state that it records every emitted key in the caller's set and declines when a key would be emitted twice. `applyScalarFrontmatterRule`'s comment must state that a duplicate key across regions is one of the decline shapes.

1.5 Keep `applyScalarFrontmatterRule` and `resolveScalarRegion` inside the lint budgets (funlen 80 lines / 50 statements, gocognit 20, nestif 4, golines 100). The added lines are one declaration plus one condition per emit site; do not restructure the functions to compensate.

1.6 Out of scope, do not implement: duplicate detection between a region and the unchanged lines outside the regions. A key duplicated between a region and an untouched line cannot occur in valid input, because that input would already carry a duplicate key.

## 2. Make the existing one-sided fixture decide on the one-sided key (`pkg/git/yaml_merge_resolver_test.go`)

2.1 Replace the `Entry("one-sided key", …)` fixture inside `DescribeTable("AC4: rejection shapes keep quarantining", …)`. Its current text is:

```go
			Entry("one-sided key",
				"---\n<<<<<<< HEAD\na: 1\nb: 2\n=======\na: 1\n"+
					">>>>>>> theirs\ntitle: probe\n---\nbody\n"),
```

Replace it with:

```go
			// The shared key is a counter so the region resolves it without
			// merge-parent timestamps; that makes the one-sided key `b` the input
			// that decides the outcome. With a shared non-counter key the region
			// declined before `b` was ever examined, and a regression in the
			// one-sided base guard would have gone unnoticed.
			Entry("one-sided key",
				"---\n<<<<<<< HEAD\ntrigger_count: 3\nb: 2\n=======\ntrigger_count: 0\n"+
					">>>>>>> theirs\ntitle: probe\n---\nbody\n"),
```

2.2 Leave the entry's expectation untouched: it still asserts `ErrConflictResolutionFailed`, `IncResolverFailureArgsForCall(0) == "no_frontmatter"`, `IncAutoresolvedCallCount() == 0`, and the file unchanged. `setupHandWrittenConflict` leaves no index stage 1, so `oneSidedKeyIsAdd(loadBase, "b")` is false and the region declines at `b` — the documented "unreadable base" reason. Do not delete the entry, do not rename it, do not move it to another table.

2.3 Do not touch the `duplicate key`, `list item`, `block scalar`, `nested mapping`, `flow collection` or `outside frontmatter` entries.

## 3. Add the cross-region regression case (`pkg/git/yaml_merge_resolver_test.go`)

3.1 Add this spec inside the existing `Describe("Scalar frontmatter rule")` block, immediately after `It("resolves a union region and a counter region in the same file", …)`. Both regions carry keys the base lacks, so the union rule confirms every one of them as an add; `k` is an ours-only add in the first region and a theirs-only add in the second, and an unchanged line separates the two regions:

```go
		// The union rule decides each region on its own, so a key one side adds
		// in one region and the other side adds in a separate region must still
		// be caught: writing both would leave a duplicate mapping key that the
		// next yaml.Unmarshal of the frontmatter rejects.
		It("AC: declines the file when one key would be emitted in two regions", func() {
			conflicted := "---\n" +
				"<<<<<<< HEAD\nk: 1\n=======\nn: 1\n>>>>>>> theirs\n" +
				"title: probe\n" +
				"<<<<<<< HEAD\nm: 1\n=======\nk: 2\n>>>>>>> theirs\n" +
				"---\nbody\n"
			workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
			defer cleanup()

			abs := filepath.Join(workDir, rel)
			before, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())

			r := git.NewYAMLMergeResolverWithBase(
				workDir,
				fakeMetrics,
				"---\ntitle: probe\n---\nbody\n",
				true,
			)
			err = r.Resolve(ctx, []string{rel})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(1))
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))
			Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(0))

			after, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before))
		})
```

The stubbed base is load-bearing: with a readable base that lacks `k`, `n` and `m`, the code before this fix resolves both regions and writes `k` twice, so this spec fails. With `setupHandWrittenConflict`'s default resolver or `assertScalarQuarantine`, the file would decline for the wrong reason (unreadable base) and the spec would pass without the fix. Do not switch to either of those.

3.2 Also cover the **shared-key** arm of the guard, which the case above does not reach. Add one sibling `It` whose two regions both carry the same key on both sides — `<<<<<<< HEAD\nk: 1\n=======\nk: 2\n>>>>>>> theirs` followed by an unchanged line and the same region again — using the same `NewYAMLMergeResolverWithBase` stub with a base that lacks `k`. Expect `ErrConflictResolutionFailed`, `no_frontmatter`, `IncAutoresolvedCallCount() == 0`, and the file byte-identical. Without this row the shared-branch decline in `resolveScalarRegion` is never exercised.

3.3 Do not add a second positive case for distinct keys across regions: `It("resolves a union region and a counter region in the same file", …)` already proves the guard is not over-broad. Leave it unchanged and confirm it still passes.

3.4 All conflict-marker text stays inside quoted Go strings with escaped `\n`, never as raw lines at column 0 — the repo's global pre-commit hook rejects a staged file containing a marker at column 0.

## 4. Self-check before finishing

4.1 Run the targeted suite and confirm it is green: `GOFLAGS=-buildvcs=false go test ./pkg/git/ -count=1`.

4.2 Confirm by reading the code that the `emitted` set is created once in `applyScalarFrontmatterRule`, passed to every region, and that all three emit sites in `resolveScalarRegion` consult it.

4.3 Walk `## Desired Behavior` items 4 and 5 of the spec against the change: every key still appears exactly once, and a file the rule cannot decide returns `ErrConflictResolutionFailed` with no partial write.
</requirements>

<constraints>
- `pkg/git/yaml_merge_resolver.go` keeps `github.com/golang/glog`; never introduce `log/slog` in that file.
- Errors are wrapped with `github.com/bborbe/errors`; never `fmt.Errorf`.
- No new `context.Background()` in non-test `pkg/` code; timestamps keep coming from the injected `sideTimes` getter, never `time.Now()`.
- The resolver stays fail-closed: a file whose regions would emit a duplicate key returns `ErrConflictResolutionFailed` through the existing `false, nil` decline, with the file byte-identical and nothing staged.
- The existing counter rule (`trigger_count`, `retry_count`, integer `metrics_*` → max) and the newer-side rule must not regress.
- The union rule for one-sided adds must not regress: `It("AC: resolves two one-sided additions to the union of both sides", …)`, `It("AC: orders ours-side keys first, then the theirs-only keys", …)`, `It("AC: counters and newer-side rules survive a one-sided add in the same region", …)` and `It("AC: the newer side wins a shared key while both one-sided keys are kept", …)` must all still pass unchanged.
- The `Entry("one-sided key", …)` case keeps expecting `ErrConflictResolutionFailed`; do not delete or weaken it. Only its fixture's shared key changes, from a non-counter to a counter.
- No new exported symbol, config field, CLI flag, metric or failure category. The decline reuses `resolverFailureNoFront` and the existing `ErrConflictResolutionFailed` sentinel. The new `emittedKeys` type and its `record` method stay unexported.
- Non-goals that must stay out of scope: body conflicts, one-sided deletes (never resurrect a key the base carries and one side removed), conflict regions outside the frontmatter, non-markdown files, draining or rewriting existing `_conflicts/` entries, and any change to `bborbe/vault-api`.
- Precommit budgets: funlen 80, gocognit 20, nestif 4, golines 100.
- `.dark-factory.yaml` sets `workflow: direct`, `pr: false`, `autoRelease: false`: commit to the branch directly; no PR or auto-release is expected.
- Do NOT commit — dark-factory handles git.
- Do not touch `CHANGELOG.md`, `docs/deployment.md`, or `specs/in-progress/016-yaml-scalar-conflict-autoresolve.md`. The existing `## Unreleased` bullet and the `docs/deployment.md` wording already cover this behaviour (duplicate keys keep quarantining), so the project DoD's CHANGELOG line is satisfied by that entry — do not add a second bullet.
- Existing tests must still pass.
</constraints>

<verification>
Run inside the container:

```
GOFLAGS=-buildvcs=false go build ./...
GOFLAGS=-buildvcs=false go test ./pkg/git/ -count=1
make test
make precommit
```

All four must exit 0.

Then confirm the observable outcomes (each line must print `ok`):

```
test "$(grep -c 'type emittedKeys map\[string\]struct{}' pkg/git/yaml_merge_resolver.go)" = "1" && echo ok
test "$(grep -c 'func (e emittedKeys) record(key string) bool {' pkg/git/yaml_merge_resolver.go)" = "1" && echo ok
test "$(grep -cF 'emitted.record(' pkg/git/yaml_merge_resolver.go)" -ge 3 && echo ok
test "$(grep -c 'emitted emittedKeys' pkg/git/yaml_merge_resolver.go)" = "1" && echo ok
test "$(grep -c 'emitted := make(emittedKeys, len(regions))' pkg/git/yaml_merge_resolver.go)" = "1" && echo ok
test "$(grep -cF 'trigger_count: 3\nb: 2' pkg/git/yaml_merge_resolver_test.go)" = "1" && echo ok
! grep -qF '<<<<<<< HEAD\na: 1\nb: 2' pkg/git/yaml_merge_resolver_test.go && echo ok
test "$(grep -c 'AC: declines the file when one key would be emitted in two regions' pkg/git/yaml_merge_resolver_test.go)" = "1" && echo ok
test "$(grep -c 'Entry("one-sided key"' pkg/git/yaml_merge_resolver_test.go)" = "1" && echo ok
! grep -q 'log/slog' pkg/git/yaml_merge_resolver.go && echo ok
! grep -q 'fmt.Errorf' pkg/git/yaml_merge_resolver.go && echo ok
```

`make precommit` is the authoritative gate: it runs format, generate, the full test suite, and the linter. If it fails, fix the failure and re-run only the failing target until it passes, then run `make precommit` once more.
</verification>
