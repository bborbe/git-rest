---
status: completed
spec: [024-bug-one-sided-frontmatter-adds-quarantine]
summary: One-sided frontmatter conflict keys now resolve to the union of both sides when the merge base is readable and lacks the key, while base-confirmed deletes, unreadable bases, and other ineligible shapes still quarantine.
execution_id: git-rest-frontmatter-union-exec-093-spec-024-one-sided-union
dark-factory-version: v0.198.7
created: "2026-11-02T19:40:00Z"
queued: "2026-11-02T19:26:42Z"
started: "2026-11-02T19:30:20Z"
completed: "2026-11-02T19:34:07Z"
branch: dark-factory/bug-one-sided-frontmatter-adds-quarantine
---

# Resolve one-sided frontmatter additions to the union of both sides

<summary>
- A frontmatter conflict region in which each side adds a different scalar key now resolves to both keys, with no conflict markers and no quarantine.
- A one-sided key is kept only when the merge base is readable and does not already carry it; the key's presence in the base is what separates an addition from a deletion.
- When the base carries the key, or the base cannot be read at all, the region quarantines exactly as today — a deleted key is never resurrected.
- The union lists the local side's keys in their original order and then the remote side's extra keys in their order, each key appearing once.
- Keys both sides carry keep their existing rules: allowlisted counters take the larger integer, every other scalar takes the newer merge parent's value.
- A union resolution counts as an auto-resolution, not a failure; an undecidable region still fails the file and increments the existing failure category.
- The existing quarantine expectations stay intact, including the hand-written one-sided fixture that has no merge base to consult.
- The operator-facing documentation, the changelog, and the superseded spec now describe the base-confirmed union.
</summary>

<objective>
Turn the merge base into the deciding input for one-sided frontmatter keys so two writers who each add a different key both keep their data, while a delete that raced an edit, a base-less conflict, an unparsable region and a non-markdown file all still quarantine.
</objective>

<context>
Read `docs/dod.md` and `.dark-factory.yaml` for project conventions, then read these files before changing anything:

- `pkg/git/yaml_merge_resolver.go` — the whole file. The code this prompt changes is `scalarRegion`, `planScalarRegions`, `parseScalarSide`, `parseScalarOurs`, `parseScalarTheirs`, `resolveScalarEntry`, `applyScalarFrontmatterRule`. The base reader `baseKeySet` and the `baseContent` field land in the previous prompt and are already wired into both constructors.
- `pkg/git/export_test.go` — `NewYAMLMergeResolverWithSideTimes` and `YAMLBaseKeySetForTest`; you add one more test-only constructor here.
- `pkg/git/yaml_merge_resolver_test.go` — helpers `setupScalarMergeRepo` (real conflicted merge, index stage 1 present, commit dates controllable), `setupHandWrittenConflict` (markers written by hand, no index stages), `scalarFixture` (inside `Describe("Scalar frontmatter rule")`), and the `AC4: rejection shapes keep quarantining` table including the `Entry("one-sided key", …)` case that must keep passing unmodified.
- `docs/deployment.md` — the `- **Vault-write mode**` bullet. The sentence to rewrite contains `non-scalar hunks, hunks outside the frontmatter, one-sided keys, duplicate keys`.
- `CHANGELOG.md` — the newest heading is `## v0.39.0`; there is no `## Unreleased` section.
- `specs/in-progress/016-yaml-scalar-conflict-autoresolve.md` — `## Summary` line 15, `## Non-goals` item 2 and the acceptance criterion naming a one-sided key. Read them so the note you add is accurate; do not rewrite them.
- `specs/in-progress/024-bug-one-sided-frontmatter-adds-quarantine.md` — `## Desired Behavior`, `## Non-goals`, `## Failure Modes`, `## Acceptance Criteria`.

Coding guides (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors` only, never `fmt.Errorf`, never `context.Background()` in non-test `pkg/` code.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — this file logs with `github.com/golang/glog`, not `log/slog`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 / Gomega conventions, external test package `git_test`, `DescribeTable` for shape matrices.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` — small unexported helpers, one decision per function, constructor → struct wiring.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` placement and the required `- <prefix>: <what>` shape.
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — prose style for the `docs/deployment.md` rewrite.
</context>

<requirements>
## 1. Accept disjoint key sets in the region parser (`pkg/git/yaml_merge_resolver.go`)

1.1 Change `scalarRegion` so both sides keep their original order. `theirs` becomes an ordered slice; the lookup map is built at resolution time because the region is small:

```go
// scalarRegion is an eligible conflict region with both sides parsed.
type scalarRegion struct {
	conflictRegion
	ours   []scalarKeyEntry // ours-side order
	theirs []scalarKeyEntry // theirs-side order
}
```

1.2 Change `parseScalarTheirs` to return an ordered slice:

```go
func parseScalarTheirs(lines []string) ([]scalarKeyEntry, bool) {
```

It keeps the duplicate-key guard and the "every line must parse as a single-line scalar entry" guard, and appends in input order.

1.3 Change `parseScalarSide` to drop the identical-key-set guard and the equal-length guard, and to return the ordered theirs slice:

```go
// parseScalarSide parses both sides of one region and enforces the shape
// guards: at least one key per side and no duplicate key within a side. The
// key sets may be disjoint here — whether a one-sided key is an addition or a
// deletion is decided against the merge base in resolveScalarRegion, which is
// the only place that may consult the base.
func parseScalarSide(
	oursLines, theirsLines []string,
) ([]scalarKeyEntry, []scalarKeyEntry, bool) {
```

Delete the `len(ours) != len(theirs)` check and the loop that requires every ours key to exist in theirs. Keep `len(ours) == 0 || len(theirs) == 0` as a rejection: a fully empty side is pre-existing behaviour and stays on the quarantine path.

1.4 Update the `scalarRegion.theirs` construction in `planScalarRegions` to match the new tuple. `planScalarRegions` keeps its "shape only, no I/O" contract — it must not read the base.

## 2. Add the union rule to the resolution path

2.1 Add two helpers immediately before `applyScalarFrontmatterRule`:

```go
// oneSidedKeyIsAdd reports whether key, present on only one side of a region,
// is an addition relative to the merge base. It is false when the base cannot
// be read at all, and false when the base already carries the key — that shape
// is a delete on one side that raced an edit on the other, and the deleted key
// must never be resurrected.
func oneSidedKeyIsAdd(
	loadBase func() (map[string]struct{}, bool),
	key string,
) bool {
	keys, ok := loadBase()
	if !ok {
		return false
	}
	_, inBase := keys[key]
	return !inBase
}

// resolveScalarRegion returns the replacement lines for one region. Keys the
// two sides share go through resolveScalarEntry unchanged; a key only one side
// carries is kept verbatim when the merge base lacks it. Ordering is the
// ours-side keys in their original order, then the theirs-only keys in theirs.
// ok is false when the region needs a human.
func resolveScalarRegion(
	region scalarRegion,
	newerTheirs func() (theirsNewer bool, ok bool),
	loadBase func() (map[string]struct{}, bool),
) ([]string, bool) {
```

Behaviour, in this order:

- Build `theirsByKey map[string]scalarEntry` from `region.theirs`.
- Build `oursKeys map[string]struct{}`.
- For each entry of `region.ours` in order: record the key in `oursKeys`; if `theirsByKey` has it, call `resolveScalarEntry(key, oursEntry.entry, theirsEntry, newerTheirs)` and decline (`return nil, false`) when it reports not-ok; otherwise call `oneSidedKeyIsAdd(loadBase, key)` and decline when it is false, else append `oursEntry.entry.line` verbatim.
- Then for each entry of `region.theirs` in order: skip keys already in `oursKeys`; otherwise apply the same `oneSidedKeyIsAdd` check and append `theirsEntry.entry.line` verbatim.
- Never emit a key twice.

2.2 In `applyScalarFrontmatterRule`, keep the existing lazy `newerTheirs` closure, add a lazy base closure beside it, and replace the per-region loop body with a call to `resolveScalarRegion`:

```go
	var (
		baseLoaded bool
		baseOK     bool
		baseKeys   map[string]struct{}
	)
	loadBase := func() (map[string]struct{}, bool) {
		if !baseLoaded {
			baseLoaded = true
			baseKeys, baseOK = r.baseKeySet(ctx, path)
		}
		return baseKeys, baseOK
	}
```

- A region that `resolveScalarRegion` declines makes `applyScalarFrontmatterRule` return `false, nil` before any write, so `resolveOne` records `no_frontmatter` and quarantines exactly as it does for every other ineligible shape. Do not write the file, do not stage it, and do not increment the auto-resolved counter on that path.
- The base is read at most once per file, and only when a region actually carries a one-sided key. A file whose regions all have identical key sets must not spawn a `git show` subprocess.
- Keep the counter and newer-side rules untouched: `resolveScalarEntry`, `isCounterKey`, `integerValue`, `newerTheirs` and `sideTimes` do not change.
- Keep `applyScalarFrontmatterRule` under the funlen budget (80 lines) by delegating the loop body to `resolveScalarRegion`; do not inline it.

2.3 Update the `applyScalarFrontmatterRule` doc comment to state the union rule and that a decline is a quarantine, not a partial write.

## 3. Keep the test-only constructors usable (`pkg/git/export_test.go`)

3.1 Add a constructor whose merge base is stubbed, so the base-carries and base-unreadable arms are deterministic:

```go
// NewYAMLMergeResolverWithBase exposes a resolver whose merge-base content is
// stubbed, so external tests can drive the union rule without a real index
// stage 1. baseOK=false makes the base read fail. The merge-parent timestamps
// are stubbed to fail as well, so a test that accidentally needs them declines
// instead of silently picking a side. Test builds only.
func NewYAMLMergeResolverWithBase(
	repoPath string,
	m metrics.Metrics,
	base string,
	baseOK bool,
) ConflictResolver {
	baseContent := func(context.Context, string) (string, error) {
		if !baseOK {
			return "", stderrors.New("stubbed merge base unavailable")
		}
		return base, nil
	}
	return &yamlMergeResolver{
		repoPath:  repoPath,
		metrics:   m,
		baseContent: baseContent,
		sideTimes: func(context.Context) (int64, int64, error) {
			return 0, 0, stderrors.New("stubbed merge-parent timestamps unavailable")
		},
	}
}
```

Add the standard-library errors import as `stderrors "errors"` to `pkg/git/export_test.go` (it does not import it yet); never `github.com/bborbe/errors`. Gofmt the literal.

3.2 Leave `NewYAMLMergeResolverWithSideTimes` and `YAMLBaseKeySetForTest` unchanged.

## 4. Regression cases (`pkg/git/yaml_merge_resolver_test.go`)

Add all of these inside the existing `Describe("Scalar frontmatter rule")` block. Do not edit or delete the `Entry("one-sided key", …)` case or any other existing spec.

4.1 The incident shape, through a real merge (this is the AC that proves the real `git show :1:<path>` boundary and the ordering):

```go
It("AC: resolves two one-sided additions to the union of both sides", func() {
	base := "---\ncreated_date: \"2026-11-02\"\nphase: planning\n---\n\nbody line\n"
	ours := "---\ncreated_date: \"2026-11-02\"\nphase: planning\npr_state: open\n---\n\nbody line\n"
	theirs := "---\ncreated_date: \"2026-11-02\"\nphase: planning\n" +
		"previous_assignee: someone\n---\n\nbody line\n"
	workDir, rel, cleanup := setupScalarMergeRepo(base, ours, theirs, "", "")
	defer cleanup()

	r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
	Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

	written, err := os.ReadFile(filepath.Join(workDir, rel))
	Expect(err).NotTo(HaveOccurred())
	Expect(string(written)).To(Equal(
		"---\ncreated_date: \"2026-11-02\"\nphase: planning\npr_state: open\n" +
			"previous_assignee: someone\n---\n\nbody line\n",
	))
	Expect(string(written)).NotTo(ContainSubstring("<<<<<<<"))
	Expect(string(written)).NotTo(ContainSubstring(">>>>>>>"))
	Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(1))
	Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(0))
})
```

4.2 Ordering, with a stubbed base that carries none of the region keys:

```go
It("AC: orders ours-side keys first, then the theirs-only keys", func() {
	conflicted := "---\n<<<<<<< HEAD\nzeta: 1\nalpha: 2\n=======\n" +
		"omega: 3\nbeta: 4\n>>>>>>> theirs\ntitle: probe\n---\nbody\n"
	workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
	defer cleanup()

	r := git.NewYAMLMergeResolverWithBase(workDir, fakeMetrics, "---\ntitle: probe\n---\nbody\n", true)
	Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

	written, err := os.ReadFile(filepath.Join(workDir, rel))
	Expect(err).NotTo(HaveOccurred())
	Expect(string(written)).To(Equal(
		"---\nzeta: 1\nalpha: 2\nomega: 3\nbeta: 4\ntitle: probe\n---\nbody\n",
	))
})
```

4.3 A one-sided key the base already carries is a delete raced by an edit, so the region must quarantine. The shared key is a counter so the region never needs merge-parent timestamps:

```go
It("AC: a one-sided key the base carries still quarantines", func() {
	conflicted := "---\n<<<<<<< HEAD\ntrigger_count: 3\n=======\n" +
		"pr_state: closed\ntrigger_count: 0\n>>>>>>> theirs\n---\nbody\n"
	workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
	defer cleanup()

	abs := filepath.Join(workDir, rel)
	before, err := os.ReadFile(abs)
	Expect(err).NotTo(HaveOccurred())

	base := "---\npr_state: open\ntrigger_count: 9\n---\nbody\n"
	r := git.NewYAMLMergeResolverWithBase(workDir, fakeMetrics, base, true)
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

4.4 An unreadable base keeps the one-sided shape on the quarantine path:

```go
It("AC: an unreadable merge base keeps the one-sided shape quarantined", func() {
	conflicted := "---\n<<<<<<< HEAD\npr_state: open\n=======\n" +
		"previous_assignee: someone\n>>>>>>> theirs\ntitle: probe\n---\nbody\n"
	workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
	defer cleanup()

	abs := filepath.Join(workDir, rel)
	before, err := os.ReadFile(abs)
	Expect(err).NotTo(HaveOccurred())

	r := git.NewYAMLMergeResolverWithBase(workDir, fakeMetrics, "", false)
	err = r.Resolve(ctx, []string{rel})
	Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
	Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))
	Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(0))

	after, err := os.ReadFile(abs)
	Expect(err).NotTo(HaveOccurred())
	Expect(after).To(Equal(before))
})
```

4.5 The delete-raced-by-edit shape through a real merge, so the base that carries the key is read from a real stage 1:

```go
It("AC: a real merge whose base carries the one-sided key quarantines", func() {
	base := "---\npr_state: open\nphase: planning\n---\nbody\n"
	ours := "---\nphase: done\n---\nbody\n"
	theirs := "---\npr_state: closed\nphase: planning\n---\nbody\n"
	workDir, rel, cleanup := setupScalarMergeRepo(base, ours, theirs, "", "")
	defer cleanup()

	abs := filepath.Join(workDir, rel)
	before, err := os.ReadFile(abs)
	Expect(err).NotTo(HaveOccurred())

	r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
	err = r.Resolve(ctx, []string{rel})
	Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
	Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(0))

	after, err := os.ReadFile(abs)
	Expect(err).NotTo(HaveOccurred())
	Expect(after).To(Equal(before))
})
```

Add a one-line comment above this fixture: git may fold the deletion and the adjacent edit into one region or split them; either way the region must quarantine, and the assertion is on the outcome, not on the region count.

4.6 Add/add conflict with no stage 1, through a real merge. Add a helper beside `setupScalarMergeRepo`:

```go
// setupScalarAddAddRepo builds a repo where both branches create note.md with
// different content, so the merge is an add/add conflict with no index stage 1
// for note.md. Returns workDir, the repo-relative path, and a cleanup func.
func setupScalarAddAddRepo(ours, theirs string) (workDir, relPath string, cleanup func()) {
```

It initialises the repo on `main`, commits an unrelated `README.md` as the base, branches `theirs` and commits `note.md` with the theirs content, returns to `main` and commits `note.md` with the ours content, then runs `git merge --no-edit theirs` (non-zero exit expected) and asserts the file contains `<<<<<<<`. Then the spec:

```go
It("AC: an add/add conflict with no merge base quarantines", func() {
	ours := "---\npr_state: open\n---\nbody\n"
	theirs := "---\nprevious_assignee: someone\n---\nbody\n"
	workDir, rel, cleanup := setupScalarAddAddRepo(ours, theirs)
	defer cleanup()

	abs := filepath.Join(workDir, rel)
	before, err := os.ReadFile(abs)
	Expect(err).NotTo(HaveOccurred())

	r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
	err = r.Resolve(ctx, []string{rel})
	Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
	Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))
	Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(0))

	after, err := os.ReadFile(abs)
	Expect(err).NotTo(HaveOccurred())
	Expect(after).To(Equal(before))
})
```

4.7 Overlapping keys keep their rules while the same region carries a one-sided add, through a real merge:

```go
It("AC: counters and newer-side rules survive a one-sided add in the same region", func() {
	base := "---\ntrigger_count: 9\ntitle: probe\n---\nbody\n"
	ours := "---\ntrigger_count: 3\npr_state: open\ntitle: probe\n---\nbody\n"
	theirs := "---\ntrigger_count: 0\nprevious_assignee: someone\ntitle: probe\n---\nbody\n"
	workDir, rel, cleanup := setupScalarMergeRepo(base, ours, theirs, "", "")
	defer cleanup()

	r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
	Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

	written, err := os.ReadFile(filepath.Join(workDir, rel))
	Expect(err).NotTo(HaveOccurred())
	Expect(string(written)).To(ContainSubstring("trigger_count: 3"))
	Expect(string(written)).To(ContainSubstring("pr_state: open"))
	Expect(string(written)).To(ContainSubstring("previous_assignee: someone"))
	Expect(strings.Count(string(written), "trigger_count: 3")).To(Equal(1))
	Expect(string(written)).NotTo(ContainSubstring("<<<<<<<"))
	Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(1))
	Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(0))
})
```

4.8 The newer-side rule plus a one-sided add, through a real merge with controlled commit dates:

```go
It("AC: the newer side wins a shared key while both one-sided keys are kept", func() {
	base := "---\nassignee: base\ntitle: probe\n---\nbody\n"
	ours := "---\nassignee: alice\npr_state: open\ntitle: probe\n---\nbody\n"
	theirs := "---\nassignee: bob\nprevious_assignee: someone\ntitle: probe\n---\nbody\n"
	workDir, rel, cleanup := setupScalarMergeRepo(
		base, ours, theirs,
		"2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z",
	)
	defer cleanup()

	r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
	Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

	written, err := os.ReadFile(filepath.Join(workDir, rel))
	Expect(err).NotTo(HaveOccurred())
	Expect(string(written)).To(ContainSubstring("assignee: bob"))
	Expect(string(written)).To(ContainSubstring("pr_state: open"))
	Expect(string(written)).To(ContainSubstring("previous_assignee: someone"))
	Expect(string(written)).NotTo(ContainSubstring("assignee: alice"))
})
```

4.9 Multiple regions, one a union and one a counter, resolve in one pass (stubbed base, so no timestamps are needed):

```go
It("resolves a union region and a counter region in the same file", func() {
	conflicted := "---\n" +
		"<<<<<<< HEAD\npr_state: open\n=======\nprevious_assignee: someone\n>>>>>>> theirs\n" +
		"<<<<<<< HEAD\ntrigger_count: 3\n=======\ntrigger_count: 0\n>>>>>>> theirs\n" +
		"---\nbody\n"
	workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
	defer cleanup()

	r := git.NewYAMLMergeResolverWithBase(workDir, fakeMetrics, "---\ntitle: probe\n---\nbody\n", true)
	Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

	written, err := os.ReadFile(filepath.Join(workDir, rel))
	Expect(err).NotTo(HaveOccurred())
	Expect(string(written)).To(Equal(
		"---\npr_state: open\nprevious_assignee: someone\ntrigger_count: 3\n---\nbody\n",
	))
	Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(1))
})
```

4.10 Confirm the pre-existing guards still hold. Do not add duplicates of them: the existing `AC4: rejection shapes keep quarantining` table (list item, block scalar, nested mapping, flow collection, outside frontmatter, one-sided key, duplicate key), `keeps the pre-existing path when an extracted side is empty`, `rejects a region that crosses the frontmatter/body boundary`, `counter-only file resolves without a merge parent`, and `AC5: an undecidable timestamp quarantines the whole file` must all pass unchanged.

## 5. Rewrite the operator documentation (`docs/deployment.md`)

5.1 In the `- **Vault-write mode**` bullet, replace exactly this sentence:

```
Every other shape — non-scalar hunks, hunks outside the frontmatter, one-sided keys, duplicate keys — keeps the quarantine path described below.
```

with:

```
Every other shape — non-scalar hunks, hunks outside the frontmatter, duplicate keys — keeps the quarantine path described below. A **one-sided key** — a key only one side of the region carries — is resolved to the union of both sides when the merge base is readable and does not carry it, so two writers who each add a different key both keep their data; the local side's keys keep their order and the remote side's extra keys follow. When the merge base already carries the key (a delete that raced an edit) or the base stage cannot be read, the region still quarantines and a deleted key is never resurrected.
```

5.2 Change nothing else in that bullet: the counter and newer-side sentences stay, the quarantine description stays, and the `no_frontmatter` entry in the metric-category list stays.

## 6. Add the changelog entry (`CHANGELOG.md`)

6.1 There is no `## Unreleased` section. Insert one immediately above `## v0.39.0` with exactly one bullet:

```markdown
## Unreleased

- fix: Resolve a frontmatter conflict region whose sides add different scalar keys to the union of both sides, when the merge base is readable and carries neither key. A one-sided key is now kept only as a base-confirmed add: when the base carries it (a delete that raced an edit) or the base stage cannot be read, the region still quarantines with `git_rest_resolver_failures_total{category="no_frontmatter"}` and the deleted key is never resurrected. Counters still take the greater value and every other shared scalar still takes the newer merge parent's value. Supersedes spec 016's one-sided-key exclusion for the base-confirmed add case only.
```

6.2 Do not edit any released section and do not bump a version heading.

## 7. Record the supersession in spec 016 (`specs/in-progress/016-yaml-scalar-conflict-autoresolve.md`)

7.1 Insert one blockquote line immediately after the frontmatter's closing `---` and before `## Summary`:

```markdown
> **Partly superseded** by `specs/in-progress/024-bug-one-sided-frontmatter-adds-quarantine.md` for the one-sided-add case only: a one-sided key is now kept when the merge base is readable and lacks it. One-sided deletes, non-scalar hunks, hunks outside the frontmatter and duplicate keys keep quarantining exactly as this spec describes.
```

7.2 Do not touch the summary line, the non-goals, the acceptance criteria, the status, or any other line of that file. This is a note, not a rewrite of its evidence.

## 8. Self-check before finishing

8.1 Run the targeted suite and confirm it is green: `GOFLAGS=-buildvcs=false go test ./pkg/git/ -count=1`.

8.2 Walk each acceptance criterion in `specs/in-progress/024-bug-one-sided-frontmatter-adds-quarantine.md` that is container-executable (the disjoint-union case, the base-confirmed rule, the overlapping-key rules, the metrics counts, the docs grep) against the change and confirm each is covered by a test or a grep in `<verification>`.
</requirements>

<constraints>
- `pkg/git/yaml_merge_resolver.go` keeps `github.com/golang/glog`; never introduce `log/slog` in that file.
- Errors are wrapped with `github.com/bborbe/errors`; never `fmt.Errorf`.
- No new `context.Background()` in non-test `pkg/` code; merge-parent timestamps keep coming from the injected `sideTimes` getter, never `time.Now()`.
- The existing counter rule (`trigger_count`, `retry_count`, integer `metrics_*` → max) and the newer-side rule must not regress.
- The resolver remains fail-closed: a region it cannot decide aborts resolution with `ErrConflictResolutionFailed`. A declined union returns `false, nil` from `applyScalarFrontmatterRule` so `resolveOne` records `no_frontmatter` and quarantines; it must never leave a partial write.
- The `Entry("one-sided key", …)` case in `pkg/git/yaml_merge_resolver_test.go` keeps expecting `ErrConflictResolutionFailed` and must not be deleted or weakened. Its fixture (`a: 1`/`b: 2` vs `a: 1`) is hand-written with no `MERGE_HEAD` and no index stages: the region declines on the shared non-counter key `a` because the merge-parent timestamps are unavailable, and `b` would decline on the unreadable base. Either way it quarantines — do not delete or weaken it.
- Non-goals that must stay out of scope: body conflicts, one-sided deletes (never resurrect a key the base carries and one side removed), conflict regions outside the frontmatter, non-markdown files, draining or rewriting existing `_conflicts/` entries, and any change to `bborbe/vault-api` including its `ReasonBodyConflict` rule.
- The deep-merge path in `resolveOne` (both sides carrying their own closing `---`) is untouched. It already unions keys and is not part of this spec.
- Do not add a new metric, config field, CLI flag, or tunable. The union path uses the existing `IncAutoresolved` and the existing `no_frontmatter` category.
- Precommit budgets: funlen 80, gocognit 20, nestif 4, golines 100. Extract `resolveScalarRegion` rather than growing `applyScalarFrontmatterRule` past the budget.
- `.dark-factory.yaml` sets `workflow: direct`, `pr: false`, `autoRelease: false`: commit to the branch directly; no PR or auto-release is expected.
- Do NOT commit — dark-factory handles git.
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
test "$(grep -c 'func resolveScalarRegion' pkg/git/yaml_merge_resolver.go)" = "1" && echo ok
test "$(grep -c 'func oneSidedKeyIsAdd' pkg/git/yaml_merge_resolver.go)" = "1" && echo ok
test "$(grep -c 'r.baseKeySet(ctx, path)' pkg/git/yaml_merge_resolver.go)" = "1" && echo ok
test "$(grep -c 'func NewYAMLMergeResolverWithBase' pkg/git/export_test.go)" = "1" && echo ok
test "$(grep -c 'pr_state' pkg/git/yaml_merge_resolver_test.go)" -ge 4 && echo ok
test "$(grep -c 'previous_assignee' pkg/git/yaml_merge_resolver_test.go)" -ge 4 && echo ok
test "$(grep -c 'Entry("one-sided key"' pkg/git/yaml_merge_resolver_test.go)" = "1" && echo ok
test "$(grep -c 'func setupScalarAddAddRepo' pkg/git/yaml_merge_resolver_test.go)" = "1" && echo ok
test "$(grep -c 'one-sided keys, duplicate keys' docs/deployment.md)" = "0" && echo ok
test "$(grep -c 'one-sided' docs/deployment.md)" -ge 1 && echo ok
test "$(grep -c 'base-confirmed add' CHANGELOG.md)" = "1" && echo ok
test "$(grep -c '^## Unreleased' CHANGELOG.md)" = "1" && echo ok
test "$(grep -c 'specs/in-progress/024-bug-one-sided-frontmatter-adds-quarantine.md' specs/in-progress/016-yaml-scalar-conflict-autoresolve.md)" = "1" && echo ok
! grep -q 'identical key sets' pkg/git/yaml_merge_resolver.go && echo ok
! grep -q 'log/slog' pkg/git/yaml_merge_resolver.go && echo ok
! grep -q 'fmt.Errorf' pkg/git/yaml_merge_resolver.go && echo ok
```

`make precommit` is the authoritative gate: it runs format, generate, the full test suite, and the linter. If it fails, fix the failure and re-run only the failing target until it passes, then run `make precommit` once more.
</verification>
