---
status: completed
spec: [024-bug-one-sided-frontmatter-adds-quarantine]
summary: Seeded the scalar-frontmatter duplicate guard with keys outside every conflict region and added a real-merge regression case that fails without the fix.
execution_id: git-rest-frontmatter-union-exec-096-spec-024-seed-outside-region-keys
dark-factory-version: v0.198.7
created: "2026-11-02T21:41:29Z"
queued: "2026-11-02T22:04:14Z"
started: "2026-11-02T22:30:19Z"
completed: "2026-11-02T22:34:02Z"
---

# Decline a frontmatter file whose region re-emits a key already outside it

<summary>
- A file whose frontmatter gains the same key twice — once in an untouched line and once from a resolved conflict region — is now quarantined instead of being written as invalid YAML.
- The duplicate-key guard now sees keys that already sit outside every conflict region, not only keys emitted by regions.
- A declined file keeps the existing fail-closed path: the same sentinel error, the same failure category, no partial write, nothing staged.
- Files whose regions carry keys that are not already present outside them keep resolving exactly as today, including the union case and the counter and newer-side rules.
- No new exported symbol, configuration knob, metric, log category or failure category is introduced.
- The merge-base key reader's doc comment now names the third case in which it reports an unreadable base, so the advertised contract matches the code.
- A regression case reproduces the exact shape from the second PR review through a real git merge and proves the fix by failing without it.
</summary>

<objective>
Close the duplicate-key gap the region-spanning guard left open: a key that one side adds in a clean, non-conflicting hunk outside every conflict region and the other side adds inside a region was emitted twice, producing frontmatter that the next YAML unmarshal rejects. The resolver must decline such a file through its existing fail-closed path, so a shape that cannot be decided never becomes silently invalid output.
</objective>

<context>
Read `docs/dod.md` and `.dark-factory.yaml` for project conventions, then read these files before changing anything:

- `pkg/git/yaml_merge_resolver.go` — the whole file. The code this prompt changes is `applyScalarFrontmatterRule`, plus one new unexported helper next to `emittedKeys` / `emittedKeys.record`. Read `planScalarRegions`, `frontmatterBounds`, `parseScalarEntry`, `resolveScalarRegion`, `spliceScalarRegions`, `baseKeySet`, `applyBodyRule` and `resolveOne` so the decline path is unambiguous.
- `pkg/git/yaml_merge_resolver_test.go` — helpers `setupScalarMergeRepo(base, ours, theirs, oursDate, theirsDate)` (real repo, real merge, real index stage 1, commit dates settable) and `setupHandWrittenConflict` (markers written by hand, no `MERGE_HEAD`, no index stages), plus the `Describe("Scalar frontmatter rule")` block. Read the existing duplicate cases `It("AC: declines the file when one key would be emitted in two regions", …)` and `It("AC: declines the file when a shared key appears in two regions", …)`.
- `pkg/git/export_test.go` — `NewYAMLMergeResolverWithBase(repoPath, metrics, base, baseOK)` stubs the merge-base content and deliberately makes merge-parent timestamps unavailable. You do **not** use it for this prompt's regression case: the fixture needs both a readable base and readable timestamps, which a real merge provides.
- `prompts/completed/094-spec-024-decline-cross-region-duplicate-key.md` — the prompt that added the region-spanning `emittedKeys` guard this prompt extends. Its requirement 1.6 declared "duplicate detection between a region and the unchanged lines outside the regions" out of scope on the assumption that such input "cannot occur in valid input". The second PR review on spec 024 disproved that assumption: ours can add a key in a clean hunk while theirs adds the same key inside a region. Requirement 1.6 is **superseded** by this prompt; do not follow it.
- `specs/in-progress/024-bug-one-sided-frontmatter-adds-quarantine.md` — `## Desired Behavior` item 2 ("A key that would be emitted in two different conflict regions quarantines the whole file, because a repeated mapping key makes the frontmatter unreadable"), `## Constraints`, `## Failure Modes`.

Coding guides (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors` only, never `fmt.Errorf`, never `context.Background()` in non-test `pkg/` code.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — this file logs with `github.com/golang/glog`, not `log/slog`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 / Gomega conventions, external test package `git_test`, `It` titles that state the observable outcome.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` — small unexported helpers, one decision per function, doc comments on every non-trivial helper.
</context>

<requirements>
## 1. Seed the emitted set with the keys outside every region (`pkg/git/yaml_merge_resolver.go`)

The defect: `applyScalarFrontmatterRule` builds `emitted` empty, and `spliceScalarRegions` copies every line outside a region verbatim. A key that already sits outside the regions is therefore invisible to the duplicate guard, so a region that re-emits it writes a duplicate mapping key.

1.1 Add one unexported helper immediately after the `emittedKeys.record` method. Its name and shape are fixed so the verification below can assert it:

```go
// seedEmittedOutsideRegions records the frontmatter keys that sit outside every
// conflict region. spliceScalarRegions copies those lines verbatim, so a region
// that emits one of them would leave a duplicate mapping key; seeding the set
// makes the existing duplicate guard see them.
func seedEmittedOutsideRegions(lines []string, regions []scalarRegion) emittedKeys {
	closeIndex, ok := frontmatterBounds(lines)
	if !ok {
		return emittedKeys{}
	}
	insideRegion := make([]bool, len(lines))
	for _, region := range regions {
		for i := region.startLine; i <= region.endLine; i++ {
			insideRegion[i] = true
		}
	}
	emitted := make(emittedKeys, closeIndex)
	for i := 1; i < closeIndex; i++ {
		if insideRegion[i] {
			continue
		}
		if key, ok := topLevelKey(lines[i]); ok {
			emitted[key] = struct{}{}
		}
	}
	return emitted
}

// topLevelKey returns the key of a top-level `key: value` frontmatter line, for
// any value shape — plain scalar, flow collection, block scalar or anchored
// value. It is deliberately looser than parseScalarEntry: a key that a region
// emits as a plain scalar still collides with an untouched `tags: [a]` or
// `desc: |` line, and a collision there produces a duplicate mapping key just
// as surely.
func topLevelKey(line string) (string, bool) {
	trimmed := strings.TrimRight(line, "\r")
	if trimmed == "" || trimmed[0] == ' ' || trimmed[0] == '\t' {
		return "", false
	}
	if trimmed == "-" || strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "#") {
		return "", false
	}
	key, _, found := strings.Cut(trimmed, ":")
	if !found {
		return "", false
	}
	key = strings.TrimSpace(key)
	key = strings.Trim(key, `"'`)
	if key == "" {
		return "", false
	}
	return key, true
}
```

`topLevelKey` is intentionally broader than `parseScalarEntry`: a region may emit a plain-scalar key that collides with an untouched `tags: [a]` or `desc: |` line, and `parseScalarEntry` rejects both, so seeding from it alone would miss that collision.

Notes that are load-bearing, not stylistic:

- `i := 1` skips line 0 (the opening `---`); `i < closeIndex` excludes the closing `---`. Only lines strictly between the delimiters count.
- Lines covered by a region are skipped entirely, so a region's own key is recorded by `resolveScalarRegion`, not by the seed.
- Blank lines, comments, indented continuation lines and list items are skipped — they carry no mapping key. A top-level line with **any** value shape (plain scalar, flow collection, block scalar, anchored value) contributes its key, because a region can emit that same key as a plain scalar and the collision is real: `tags: [a]` outside a region plus `tags: b` inside one would otherwise write `tags` twice.
- `planScalarRegions` already guarantees every region lies strictly inside the frontmatter (`regionInsideFrontmatter` requires `startLine > 0` and `endLine < closeIndex`), so the index arithmetic cannot go out of range.

1.2 Replace the empty-set construction in `applyScalarFrontmatterRule`. Its current text is:

```go
	emitted := make(emittedKeys, len(regions))
```

Replace it with:

```go
	emitted := seedEmittedOutsideRegions(lines, regions)
```

Keep the `false, nil` decline: it is the existing fail-closed signal, and `resolveOne` turns it into `no_frontmatter` plus `ErrConflictResolutionFailed` after `applyBodyRule` also declines. Do not return an error from this branch and do not write or stage anything.

1.3 Update the `emittedKeys` doc comment so it describes what the type now holds. Its current text is:

```go
// emittedKeys records the frontmatter keys already written by the resolved
// regions of one file. A key emitted by two regions would leave a duplicate
// mapping key that the next yaml.Unmarshal of the frontmatter rejects, so the
// second occurrence declines the whole file instead of writing invalid YAML.
```

Replace it with:

```go
// emittedKeys records the frontmatter keys already present in one file: the keys
// that live outside every conflict region, seeded before any region resolves,
// plus the keys the resolved regions emit. A key emitted twice — by two regions,
// or by a region when an untouched line already carries it — would leave a
// duplicate mapping key that the next yaml.Unmarshal of the frontmatter rejects,
// so the second occurrence declines the whole file instead of writing invalid
// YAML.
```

1.4 Update the `applyScalarFrontmatterRule` doc comment. Replace the sentence

```
// quarantines. A key that would be emitted by two regions is one of the decline
// shapes, because writing both would produce a duplicate mapping key. A decline
// is never a partial write: nothing is written or staged and the caller records
// no_frontmatter.
```

with

```
// quarantines. The keys already present outside every region are seeded into the
// same set before any region resolves. A key that would be emitted by two
// regions, or emitted by a region when an untouched line already carries it, is
// one of the decline shapes, because writing both would produce a duplicate
// mapping key. A decline is never a partial write: nothing is written or staged
// and the caller records no_frontmatter.
```

1.5 Keep the change inside `applyScalarFrontmatterRule` and the new helper. Do not change `planScalarRegions`'s signature or return values, do not change `resolveScalarRegion`, do not add a parameter to `spliceScalarRegions`, and do not add an exported symbol, config field, CLI flag, metric or failure category.

1.6 Stay inside the precommit budgets: funlen 80 lines / 50 statements, gocognit 20, nestif 4, golines 100. The new helper carries the loop **and** the `frontmatterBounds` check; `applyScalarFrontmatterRule` gains a single-line call and nothing else — it is at 78 of the 80 funlen lines, so the call site must not grow. If gofmt/golines keeps `emitted := seedEmittedOutsideRegions(lines, regions)` on one line, leave it on one line — the verification asserts that exact text.

## 2. Name the third unreadable-base case in `baseKeySet`'s doc comment (`pkg/git/yaml_merge_resolver.go`)

The code returns `ok=false` in three places: `baseContent` fails, `splitFrontmatter` fails, and the YAML unmarshal fails. The doc comment currently names only two of them, so the advertised contract understates what the callers must decline on.

2.1 Replace the first three lines of the `baseKeySet` doc comment. Its current text is:

```go
// baseKeySet returns the top-level frontmatter keys of the merge-base version of
// path. ok is false when the base cannot be read at all (no index stage 1, for
// example an add/add conflict) or when its frontmatter is not parseable YAML.
// Callers that need the base to decide something MUST decline when ok is false:
```

Replace it with:

```go
// baseKeySet returns the top-level frontmatter keys of the merge-base version of
// path. ok is false when the base cannot be read at all (no index stage 1, for
// example an add/add conflict), when the base carries no frontmatter delimiter
// pair, or when its frontmatter is not parseable YAML. Callers that need the base
// to decide something MUST decline when ok is false:
```

Leave the remaining sentences of that comment (`an unreadable base is indistinguishable…` and the `full YAML unmarshal` paragraph) unchanged.

2.2 Change no behaviour here. This is a doc-comment correction only.

## 3. Add the regression case (`pkg/git/yaml_merge_resolver_test.go`)

3.1 Add this `It` inside the existing `Describe("Scalar frontmatter rule")` block, immediately after `It("AC: declines the file when a shared key appears in two regions", …)`. Use a real merge so the base stage and the merge-parent timestamps are both readable:

```go
		// A key added by ours in a clean hunk outside every region and added
		// again by theirs inside a region is the shape the region-spanning guard
		// alone cannot see: spliceScalarRegions copies the outside line verbatim,
		// so seeding the emitted set with the outside keys is what catches it.
		It("AC: declines the file when a region re-emits a key already outside it", func() {
			base := "---\ncreated_date: \"2026-11-02\"\nphase: planning\n" +
				"title: probe\nassignee: base\n---\nbody\n"
			ours := "---\npr_state: open\ncreated_date: \"2026-11-02\"\nphase: planning\n" +
				"title: probe\nassignee: alice\n---\nbody\n"
			theirs := "---\ncreated_date: \"2026-11-02\"\nphase: planning\n" +
				"title: probe\nassignee: bob\npr_state: closed\n---\nbody\n"
			workDir, rel, cleanup := setupScalarMergeRepo(
				base, ours, theirs,
				"2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z",
			)
			defer cleanup()

			abs := filepath.Join(workDir, rel)
			before, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(before)).To(ContainSubstring("pr_state: open\ncreated_date:"))
			Expect(string(before)).To(ContainSubstring("pr_state: closed\n>>>>>>> theirs"))

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
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

3.2 The two `ContainSubstring` assertions are guards on the fixture, not decoration. They pin the shape the bug needs: `pr_state: open` sits above the `<<<<<<<` marker (a clean hunk outside every region) and `pr_state: closed` sits inside the region directly above `>>>>>>> theirs`. Git's default hunk grouping produces exactly this for the fixture above — the ours-side insert is separated from the conflicting `assignee` lines by three unchanged lines — so if a future git version groups them differently, this case fails loudly instead of passing vacuously. Do not delete these two assertions.

3.3 Why this fixture exercises the fix and no other: the shared `assignee` key is not a counter, so the region reaches the newer-side rule; a real merge makes `sideTimes` readable, so it resolves instead of declining on unreadable timestamps; the base is readable and lacks `pr_state`, so `oneSidedKeyIsAdd` confirms the theirs-side `pr_state` as an add and the region reaches `emitted.record("pr_state")`. With the seed in place the record is a duplicate and the file declines. Without it, the resolver writes `pr_state` twice and `Resolve` returns nil, so every assertion from `stderrors.Is` onward fails.

3.4 Do not switch this case to `setupHandWrittenConflict`, `NewYAMLMergeResolverWithBase` or `assertScalarQuarantine`. All three have no readable index stage 1, so the region would decline on the one-sided base guard and the case would pass without the fix.

3.5 Negative control — required, and it must leave the tree fixed:

1. Temporarily replace the seeding line in `applyScalarFrontmatterRule` with `emitted := make(emittedKeys, len(regions))`.
2. Run `GOFLAGS=-buildvcs=false go test ./pkg/git/ -count=1 -ginkgo.focus='re-emits a key already outside it'` — it must FAIL.
3. Restore the seeding line exactly as written in 1.2.
4. Re-run the same focused command — it must PASS.
5. Re-run `GOFLAGS=-buildvcs=false go test ./pkg/git/ -count=1` — the whole package must pass.

3.6 Do not weaken or reorder any existing case. In particular `It("AC: resolves two one-sided additions to the union of both sides", …)`, `It("AC: orders ours-side keys first, then the theirs-only keys", …)`, `It("AC: counters and newer-side rules survive a one-sided add in the same region", …)`, `It("AC: the newer side wins a shared key while both one-sided keys are kept", …)`, `It("AC: declines the file when one key would be emitted in two regions", …)`, `It("AC: declines the file when a shared key appears in two regions", …)` and `Entry("one-sided key", …)` must all still pass unchanged. In each of them the region's keys do not appear outside the region, so the seed changes no outcome.

3.7 All conflict-marker text stays inside quoted Go strings with escaped `\n`, never as raw lines at column 0 — the repo's global pre-commit hook rejects a staged file containing a marker at column 0.

## 4. Self-check before finishing

4.1 Walk `## Desired Behavior` item 2 of the spec against the change: a repeated mapping key anywhere in the frontmatter — across two regions, or between a region and an untouched line — declines the whole file, and the decline returns `ErrConflictResolutionFailed` with the file byte-identical and nothing staged.

4.2 Read `applyScalarFrontmatterRule` end to end and confirm the seed is built once per file from `lines` + `regions` + `closeIndex`, that it is passed to every region through the existing `emitted` argument, and that the decline path still returns `false, nil`.

4.3 Confirm the union path still counts as an auto-resolution: `IncAutoresolved()` is reached only after the write and `runGitAdd` succeed, and the new decline returns before both.

4.4 Re-run `<verification>` and confirm every command passes.
</requirements>

<constraints>
- `pkg/git/yaml_merge_resolver.go` keeps `github.com/golang/glog`; never introduce `log/slog` in that file.
- Errors are wrapped with `github.com/bborbe/errors`; never `fmt.Errorf`.
- No new `context.Background()` in non-test `pkg/` code; timestamps keep coming from the injected `sideTimes` getter, never `time.Now()`.
- The resolver stays fail-closed: a file whose frontmatter would carry a duplicate key returns `ErrConflictResolutionFailed` through the existing `false, nil` decline, with the file byte-identical and nothing written or staged.
- The existing counter rule (`trigger_count`, `retry_count`, integer `metrics_*` → max) and the newer-side rule must not regress.
- The union rule for one-sided adds must not regress; the existing union and duplicate cases listed in 3.6 must all still pass unchanged.
- No new exported symbol, config field, CLI flag, metric, log category or failure category. The decline reuses `resolverFailureNoFront` and the existing `ErrConflictResolutionFailed` sentinel. `seedEmittedOutsideRegions` stays unexported.
- No change to `bborbe/vault-api`. Body conflicts, one-sided deletes (never resurrect a key the base carries and one side removed), conflict regions outside the frontmatter, non-markdown files, and draining or rewriting existing `_conflicts/` entries all stay out of scope.
- Precommit budgets: funlen 80, gocognit 20, nestif 4, golines 100.
- `.dark-factory.yaml` sets `workflow: direct`, `pr: false`, `autoRelease: false`: commit to the branch directly; no PR or auto-release is expected.
- Do NOT commit — dark-factory handles git.
- Do not touch `CHANGELOG.md`, `docs/deployment.md`, or any file under `specs/` — those are being edited separately. The existing `## Unreleased` bullet already describes the duplicate-key decline this prompt hardens, so the project DoD's CHANGELOG line is satisfied by that entry; do not add a second bullet.
- Existing tests must still pass.
</constraints>

<verification>
Run inside the container; all must exit 0:

```
GOFLAGS=-buildvcs=false go build ./...
GOFLAGS=-buildvcs=false go test ./pkg/git/ -count=1
make test
make precommit
```

Then confirm the observable outcomes. Each line must print `ok`:

```
test "$(grep -c 'func seedEmittedOutsideRegions(' pkg/git/yaml_merge_resolver.go)" = "1" && echo ok
test "$(grep -cF 'emitted := seedEmittedOutsideRegions(lines, regions)' pkg/git/yaml_merge_resolver.go)" = "1" && echo ok
grep -qF 'when the base carries no frontmatter delimiter' pkg/git/yaml_merge_resolver.go && echo ok
grep -qF 'or by a region when an untouched line already carries it' pkg/git/yaml_merge_resolver.go && echo ok
grep -qF 'declines the file when a region re-emits a key already outside it' pkg/git/yaml_merge_resolver_test.go && echo ok
```

Regression guards — each must print `ok`, i.e. the forbidden pattern is absent:

```
! grep -q 'log/slog' pkg/git/yaml_merge_resolver.go && echo ok
! grep -q 'fmt\.Errorf' pkg/git/yaml_merge_resolver.go && echo ok
! grep -q 'time\.Now()' pkg/git/yaml_merge_resolver.go && echo ok
```

Absence is asserted with the negated `grep -q` form rather than `grep -c`, because `grep -c` exits 1 when the count is zero and `! grep -q` exits 0 when the pattern is absent.
</verification>
