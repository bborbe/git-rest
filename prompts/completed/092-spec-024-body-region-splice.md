---
status: completed
spec: [024-bug-body-conflict-hunks-quarantine]
summary: Added in-place resolution of markdown body-only conflict regions via planBodyRegions/spliceConflictRegions/applyBodyRule, wired scalar-then-body fallbacks, and added extended specs.
execution_id: git-rest-body-merge-exec-092-spec-024-body-region-splice
dark-factory-version: v0.198.7
created: "2026-11-02T18:50:00Z"
queued: "2026-11-02T18:46:44Z"
started: "2026-11-02T18:46:49Z"
completed: "2026-11-02T18:54:02Z"
branch: dark-factory/bug-body-conflict-hunks-quarantine
---

# Resolve body-only conflict regions per region, in place

<summary>
- A merge conflict whose regions lie entirely below a markdown file's closing `---` is resolved in place instead of quarantined.
- Each conflict region is enumerated from the original file's own line range and resolved independently; regions are never concatenated into one block.
- An empty side is a valid state for this rule: empty ours takes theirs verbatim, empty theirs keeps ours, equal sides keep ours once, two differing non-empty sides become ours, a blank line, then theirs.
- Every line outside a conflict region — the whole frontmatter block and both `---` delimiters included — stays byte-identical to the original.
- A task-shaped path whose frontmatter carries the documented `phase: in_progress` alias is normalised to `phase: execution` by the wrapper every write path already uses.
- A non-markdown path, a markdown file with no frontmatter block, a region crossing the frontmatter/body boundary, or any mixed eligible/ineligible region set keeps the existing quarantine path and leaves the file unmodified.
- A resolved path is staged with the same `git add -- <path>` the resolver already runs, so a genuine conflicted merge can complete.
- The four committed `Describe("Resolve body-only conflict regions")` specs turn green without any edit to their assertions.
</summary>

<objective>
Make `NewYAMLMergeResolver` resolve a markdown merge conflict whose every conflict region lies entirely below the frontmatter's closing `---`: enumerate the regions from the original file, replace each with the existing `combineBodies` result, splice the result into the original lines, write it, and stage it. Every other conflict shape keeps today's quarantine path unchanged. This is the behavioural fix; the counter, docs and changelog land in later prompts.
</objective>

<context>
There is no repo-root `CLAUDE.md` in this worktree — do not look for one. The project conventions are restated in `<constraints>` below.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors`; never `fmt.Errorf`, never a bare `return err`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, external `git_test` package, real `git` via `os/exec` against a temp working tree
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — this file's logger is `github.com/golang/glog`; do not introduce `slog` here and do not mix loggers
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — linter limits (funlen 80 lines / 50 statements, gocognit 20, nestif 4)

Files to read in full before editing (anchor by symbol name, not line number):

- `pkg/git/yaml_merge_resolver.go` — `resolveOne`, `splitConflictSides`, `splitFrontmatter`, `combineBodies`, `conflictRegion`, `enumerateConflictRegions`, `frontmatterBounds`, `planScalarRegions`, `regionInsideFrontmatter`, `scalarRegion`, `spliceScalarRegions`, `applyScalarFrontmatterRule`, `runGitAdd`, `safePath`, `normalizeTaskPhase` (called on every write path), and the failure-category constants
- `pkg/git/yaml_merge_resolver_test.go` — `setupYAMLConflictRepo`, `setupHandWrittenConflict`, `parseFrontmatterOf`, `bodyOf`, the top-level `BeforeEach` that sets `ctx` and `fakeMetrics`, and the committed `Describe("Resolve body-only conflict regions")` block whose four `It` names are `AC-bodyonly-ours-empty`, `AC-bodyonly-theirs-empty`, `AC-bodyonly-both-differ`, `AC-bodyonly-nonmarkdown`
- `pkg/git/task_phase.go` — `normalizeTaskPhase` (task directories are `25 Tasks` and `tasks`, decided by `isTaskFilePath` in `git.go`)
- `pkg/git/git.go` — `ErrConflictResolutionFailed` (defined at package scope) and `isTaskFilePath`
- `pkg/git/conflict_resolver.go` — the `ConflictResolver` interface and its counterfeiter directive; both stay byte-identical

**Verified facts — do not re-derive, do not contradict:**

- `splitConflictSides(content string) (oursStr, theirsStr string, ok bool)` returns `ok=false` when either side has zero lines, and it concatenates every region into one block. It must NOT be reused for body extraction.
- `enumerateConflictRegions(lines []string) ([]conflictRegion, bool)` already scans the original lines in file order, records `startLine`, `oursLines`, `theirsLines`, `endLine` per region, and returns `ok=false` on unbalanced, nested or absent markers.
- `frontmatterBounds(lines []string) (closeIndex int, ok bool)` returns the index of the first line equal to `---` after line 0; it requires `lines[0] == "---"`.
- `combineBodies(ours, theirs string) string` implements exactly: both empty → `""`; theirs empty → ours; ours empty → theirs; equal → ours; otherwise `ours + "\n\n" + theirs`. Reuse it as-is.
- `spliceScalarRegions` walks `lines` by index, appends untouched lines before each region, appends that region's replacement lines, then continues at `region.endLine + 1`, and joins with `"\n"`. It is not referenced by any test.
- `normalizeTaskPhase(path string, content []byte) []byte` rewrites a top-level `phase: in_progress` line to `phase: execution` for `25 Tasks/...` and `tasks/...` paths, and returns the input unchanged otherwise.
- `applyScalarFrontmatterRule(ctx, path, abs, raw)` returns `(false, nil)` when the file is not `.md`, when `planScalarRegions` rejects the file, or when a key cannot be resolved; it returns `(true, err)` once it has taken responsibility.
- The four committed specs are the acceptance tests for this prompt. They must pass without editing a single line of their assertions. The committed `AC-bodyonly-nonmarkdown` already passes on master by design.
- The conflict-failure sentinel `ErrConflictResolutionFailed` is the only failure error; no new exported error types.
- `git_rest_body_merged_total` does NOT exist yet. Do not add it in this prompt; prompt 2 does.
</context>

<requirements>

## 1. Add a body-region planner

In `pkg/git/yaml_merge_resolver.go`, add beside the scalar-region types:

```go
// bodyRegion is one eligible body-only conflict region with both sides extracted.
type bodyRegion struct {
	conflictRegion
	oursBody   string
	theirsBody string
}

// planBodyRegions decides whether the whole file is eligible for the body rule.
// Every conflict region must lie entirely below the closing frontmatter delimiter,
// and regions must not overlap. It inspects shape only: no I/O.
func planBodyRegions(lines []string) ([]bodyRegion, bool) {
	closeIndex, ok := frontmatterBounds(lines)
	if !ok {
		return nil, false
	}
	conflicts, ok := enumerateConflictRegions(lines)
	if !ok {
		return nil, false
	}

	regions := make([]bodyRegion, 0, len(conflicts))
	previousEnd := 0
	for _, region := range conflicts {
		if !regionInsideBody(region, closeIndex, previousEnd) {
			return nil, false
		}
		regions = append(regions, bodyRegion{
			conflictRegion: region,
			oursBody:       strings.Join(region.oursLines, "\n"),
			theirsBody:     strings.Join(region.theirsLines, "\n"),
		})
		previousEnd = region.endLine
	}

	if len(regions) == 0 {
		return nil, false
	}
	return regions, true
}

// regionInsideBody reports whether region lies strictly below the closing
// frontmatter delimiter and does not overlap the previous region.
func regionInsideBody(region conflictRegion, closeIndex, previousEnd int) bool {
	return region.startLine > closeIndex && region.startLine > previousEnd
}
```

Rules:

- `frontmatterBounds` must succeed; a markdown file with no frontmatter block is not eligible.
- `enumerateConflictRegions` must succeed; unbalanced or nested markers are not eligible.
- EVERY region must satisfy `regionInsideBody`; one ineligible region rejects the whole file (all-or-nothing).
- `oursBody`/`theirsBody` are the region's lines joined with `"\n"`. An empty side is `""`, which is a valid state — never treat it as a parse failure.
- `previousEnd` starts at `0`; each accepted region advances it to `region.endLine`, so two regions can never overlap.
- Do not call `splitConflictSides` and do not add a second concatenation helper beside `combineBodies`.

## 2. Add a generic region splice helper

`spliceScalarRegions` already contains the exact splice loop this rule needs. Add one generic helper and leave `spliceScalarRegions` untouched:

```go
// spliceConflictRegions returns the original file with each conflict region
// replaced by its resolved lines. Every line outside a region is copied
// verbatim, so the frontmatter and both delimiters stay byte-identical.
func spliceConflictRegions(
	lines []string,
	regions []conflictRegion,
	replacements [][]string,
) string {
	out := make([]string, 0, len(lines))
	cursor := 0
	for i, region := range regions {
		out = append(out, lines[cursor:region.startLine]...)
		out = append(out, replacements[i]...)
		cursor = region.endLine + 1
	}
	out = append(out, lines[cursor:]...)
	return strings.Join(out, "\n")
}
```

Do NOT modify `spliceScalarRegions`; it stays byte-identical.

## 3. Add the body rule

Add:

```go
// resolvedBodyLines splits a combined body into lines. An empty body yields no
// lines, so the region disappears rather than leaving a blank line.
func resolvedBodyLines(combined string) []string {
	if combined == "" {
		return nil
	}
	return strings.Split(combined, "\n")
}

// applyBodyRule attempts the body-only rule on the raw conflicted file. applied
// reports whether the rule took responsibility for the file; when it did, the
// file has been written and staged and err is the outcome.
func (r *yamlMergeResolver) applyBodyRule(
	ctx context.Context, path, abs, raw string,
) (applied bool, err error) {
	if !strings.EqualFold(filepath.Ext(path), ".md") {
		return false, nil
	}

	lines := strings.Split(raw, "\n")
	regions, ok := planBodyRegions(lines)
	if !ok {
		return false, nil
	}

	replacements := make([][]string, 0, len(regions))
	conflictRegions := make([]conflictRegion, 0, len(regions))
	for _, region := range regions {
		combined := combineBodies(region.oursBody, region.theirsBody)
		replacements = append(replacements, resolvedBodyLines(combined))
		conflictRegions = append(conflictRegions, region.conflictRegion)
	}

	final := spliceConflictRegions(lines, conflictRegions, replacements)

	select {
	case <-ctx.Done():
		return true, errors.Wrap(ctx, ctx.Err(), "body resolve cancelled")
	default:
	}

	// #nosec G703 -- path validated by safePath
	if writeErr := os.WriteFile(abs, normalizeTaskPhase(path, []byte(final)), 0o600); writeErr != nil {
		r.metrics.IncResolverFailure(resolverFailureWrite)
		glog.Warningf("yaml-merge-resolver: write body-resolved %q: %v", path, writeErr)
		return true, errors.Wrap(ctx, ErrConflictResolutionFailed, "write body-resolved file")
	}

	if err := r.runGitAdd(ctx, path); err != nil {
		return true, err
	}
	return true, nil
}
```

Sequence rules:

1. Non-`.md` path → `(false, nil)`; the caller keeps the quarantine path.
2. `planBodyRegions` rejects the file → `(false, nil)`.
3. Build every replacement before any write; no partial write on an ineligible file.
4. Cancellation check before the write, mirroring `applyScalarFrontmatterRule`.
5. Write failure → `resolverFailureWrite`, one `glog.Warningf` naming the path, return `(true, errors.Wrap(ctx, ErrConflictResolutionFailed, "write body-resolved file"))`. The file must be left as the puller found it on a failed write where the OS did not modify it.
6. `git add` failure → `runGitAdd` already increments `resolverFailureGitAdd` and logs; return `(true, err)`.
7. Success → `(true, nil)`. Do NOT increment any counter here; prompt 2 adds the body counter.

## 4. Hook the rule into `resolveOne`

The scalar rule stays first and unchanged in BOTH failure branches. Restructure only the two failure branches so each tries the scalar rule, then the body rule, then falls through to `no_frontmatter` exactly once.

In the `splitConflictSides` failure branch:

```go
ours, theirs, ok := splitConflictSides(string(raw))
if !ok {
	if applied, ruleErr := r.applyScalarFrontmatterRule(ctx, path, abs, string(raw)); applied {
		return ruleErr
	}
	if applied, ruleErr := r.applyBodyRule(ctx, path, abs, string(raw)); applied {
		return ruleErr
	}
	r.metrics.IncResolverFailure(resolverFailureNoFront)
	glog.Warningf("yaml-merge-resolver: %q has no parseable conflict region", path)
	return errors.Wrap(ctx, ErrConflictResolutionFailed, "no conflict region")
}
```

In this branch `applyScalarFrontmatterRule` returns `(false, nil)`: `splitConflictSides` returns not-ok only when one side's union of lines is empty, and `planScalarRegions` rejects any region with an empty side. Calling it first keeps the scalar rule's precedence literal and costs one shape check.

In the `splitFrontmatter` failure branch:

```go
if !oursOK || !theirsOK {
	applied, ruleErr := r.applyScalarFrontmatterRule(ctx, path, abs, string(raw))
	if applied {
		return ruleErr
	}
	if applied, ruleErr := r.applyBodyRule(ctx, path, abs, string(raw)); applied {
		return ruleErr
	}
	r.metrics.IncResolverFailure(resolverFailureNoFront)
	glog.Warningf(
		"yaml-merge-resolver: %q has no frontmatter delimiter pair on one side and the scalar-frontmatter and body rules do not apply",
		path,
	)
	return errors.Wrap(ctx, ErrConflictResolutionFailed, "no frontmatter delimiter pair")
}
```

The deep-merge path (`parseYAML` → `deepMergeTheirsWins` → `yaml.Marshal` → `combineBodies` → write → `runGitAdd`) stays byte-identical. `no_frontmatter` is incremented exactly once per failed path, never twice.

## 5. Tests

All new specs live in `pkg/git/yaml_merge_resolver_test.go` (`package git_test`, Ginkgo v2 / Gomega). The committed `Describe("Resolve body-only conflict regions")` block must stay byte-identical — do not insert into it. Add a new sibling block `Describe("Body-only conflict regions: extended", …)` immediately after it, inside the same top-level `Describe("YAMLMergeResolver", …)`, so the shared `BeforeEach` (`ctx`, `fakeMetrics`) applies.

**One existing spec must change.** `DescribeTable("AC4: rejection shapes keep quarantining")` holds `Entry("outside frontmatter", "---\ntitle: probe\n---\n<<<<<<< HEAD\nbody a\n=======\nbody b\n>>>>>>> theirs\n")`. That fixture is exactly the body-only shape this prompt now resolves, so it fails once the rule lands. Delete that one `Entry` and nothing else — spec 024 supersedes spec 016's body-conflict non-goal for this shape, so the expectation is obsolete rather than a regression. Every other `Entry` in that table (list item, block scalar, nested mapping, flow collection, one-sided key, duplicate key) still quarantines and must stay byte-identical.

Every new **resolving** `It` must be distinguishable before vs after this change; the quarantine `It`s are fail-closed guards that must stay green and must not be weakened. Use the existing helpers; do not add new repo helpers.

1. **Failure counter is zero on the three resolving shapes (spec ACs 1-3).** Three `It` blocks mirroring the committed fixtures: empty ours (98-line theirs), empty theirs, both-differ. Each calls `git.NewYAMLMergeResolver(workDir, fakeMetrics).Resolve(ctx, []string{rel})`, expects success, and asserts `fakeMetrics.IncResolverFailureCallCount() == 0`. Do not assert `IncBodyMergedCallCount` — that accessor does not exist yet.
2. **Two body regions resolve independently (spec AC 4).** `setupHandWrittenConflict("note.md", conflicted)` with:

   ```
   ---
   page_type: note
   ---
   <<<<<<< HEAD
   first ours
   =======
   first theirs
   >>>>>>> theirs
   SEPARATOR LINE
   <<<<<<< HEAD
   second ours
   =======
   second theirs
   >>>>>>> theirs
   ```

   After `Resolve` succeeds, assert the whole written file equals the original with region one replaced by `first ours\n\nfirst theirs` and region two by `second ours\n\nsecond theirs`, the `SEPARATOR LINE` preserved byte-identically, and no `<<<<<<<` / `=======` / `>>>>>>>` line remaining. A concatenating implementation deletes the separator and cannot pass.
3. **Task-shaped path normalises the phase alias (spec AC 5).** `setupHandWrittenConflict("25 Tasks/note.md", conflicted)` with:

   ```
   ---
   page_type: task
   phase: in_progress
   title: probe
   ---
   <<<<<<< HEAD
   ours body
   =======
   theirs body
   >>>>>>> theirs
   ```

   After success, assert the whole file equals the same text with `phase: in_progress` rewritten to `phase: execution` and the region replaced by `ours body\n\ntheirs body`. Every other frontmatter line and both `---` delimiters are unchanged. Do not use a fixture that already carries `phase: execution` — it would not discriminate.
4. **Two quarantine shapes (spec AC 6).** Two `It` blocks, each asserting `stderrors.Is(err, git.ErrConflictResolutionFailed)` (the test file aliases the stdlib package as `stderrors`; it does not import `github.com/bborbe/errors`), `fakeMetrics.IncResolverFailureCallCount() == 1`, `fakeMetrics.IncResolverFailureArgsForCall(0) == "no_frontmatter"`, and the file bytes equal the pre-call bytes:
   - `setupHandWrittenConflict("note.txt", conflicted)` where `conflicted` has a frontmatter block and a body region.
   - `setupHandWrittenConflict("note.md", conflicted)` where `conflicted` has no frontmatter block and a body region.
5. **All-or-nothing with a boundary-crossing region (spec AC 7).** `setupHandWrittenConflict("note.md", conflicted)` with a region that crosses the closing `---` FIRST, then a body region:

   ```
   ---
   page_type: note
   <<<<<<< HEAD
   body ours
   =======
   tags: [b]
   ---
   body theirs
   >>>>>>> theirs
   <<<<<<< HEAD
   tail ours
   =======
   tail theirs
   >>>>>>> theirs
   ```

   The first region's `ours` side must NOT carry a `---` line. If both sides do, `splitConflictSides` extracts two complete frontmatter blocks, `splitFrontmatter` succeeds on both, and `resolveOne` takes the deep-merge path — `Resolve` returns nil and the test fails without ever reaching `planBodyRegions`. With the fixture above, `splitFrontmatter` fails on the ours side, so the resolver reaches `planBodyRegions`, where `frontmatterBounds` returns the `---` that sits inside the first region, that region starts above the returned close index, and the whole file is rejected. Assert `stderrors.Is(err, git.ErrConflictResolutionFailed)`, one `no_frontmatter`, and the file left unmodified. Order matters: a body region placed before the boundary-crossing region would make both regions body-only and the file would resolve, which does not exercise the all-or-nothing gate.
6. **Staging on a genuine conflicted merge (spec AC 8).** `setupYAMLConflictRepo("a: 1\n", "X", "a: 1\n", "Y")` — identical frontmatter on both sides, differing bodies, so git leaves a real body-only conflict and a real unmerged index entry. After `Resolve` succeeds:
   - `git diff --name-only --diff-filter=U` in `workDir` prints empty.
   - `git status --porcelain` prints no line beginning with `UU`.
   - The written body is `X\n\nY` (use `bodyOf`).
   The hand-written helpers cannot carry this check: `setupHandWrittenConflict` runs its own `git add` on the conflicted bytes and leaves no merge in progress, so both clauses already pass before `Resolve`.
7. **Write and `git add` failures (spec Failure Modes row 4).** Two `It` blocks over a body-only fixture from `setupHandWrittenConflict("note.md", conflicted)`. For the write case, `chmod 0400` the conflicted file after setup and assert `stderrors.Is(err, git.ErrConflictResolutionFailed)` plus `fakeMetrics.IncResolverFailureArgsForCall(0) == "write_failed"` (skip under root). For the `git add` case, `Expect(os.RemoveAll(filepath.Join(workDir, ".git"))).To(Succeed())` after setup and assert the sentinel plus `"git_add_failed"`. Mirror the existing scalar-rule cases at `pkg/git/yaml_merge_resolver_test.go:851` and `:1090`.

## 6. Self-check before finishing

Walk each requirement: the four committed specs pass unedited; each resolving shape asserts zero resolver failures; two regions resolve independently with the separator intact; a task path normalises only the alias line; the two quarantine shapes and the mixed-region file return the sentinel and leave the file untouched; the genuine-merge fixture is staged; the deep-merge and scalar paths still pass their existing specs.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Extend `pkg/git/yaml_merge_resolver.go`. Do NOT change the `ConflictResolver` interface (`pkg/git/conflict_resolver.go`) or its counterfeiter mock (`mocks/conflict_resolver.go`); both stay byte-identical.
- Reuse `combineBodies` as-is. A second concatenation helper beside it is the defect this spec exists to prevent.
- Do NOT reuse `splitConflictSides` for extraction — it concatenates every region into one block. Reuse `enumerateConflictRegions` and `frontmatterBounds`, then follow the `planScalarRegions` / `spliceScalarRegions` precedent for the splice. A duplicate region enumerator is the same defect this spec forbids for concatenation.
- Do NOT modify `spliceScalarRegions`; add the generic `spliceConflictRegions` helper alongside it.
- Do NOT change the scalar-frontmatter rule (spec 016), the deep-merge path, `MarkerResolver`, the quarantine path, `pkg/git/git.go`, or the pull loop.
- Do NOT change the public `NewYAMLMergeResolver(repoPath string, m metrics.Metrics) ConflictResolver` signature.
- Do NOT edit any `pkg/git/*_test.go` file other than `pkg/git/yaml_merge_resolver_test.go`. In that file the committed `Describe("Resolve body-only conflict regions")` block must stay byte-identical and you may only add specs — with the single exception of deleting the `Entry("outside frontmatter", …)` from `DescribeTable("AC4: rejection shapes keep quarantining")` (requirement 5), whose fixture this prompt now resolves.
- Do NOT add a metric in this prompt. `pkg/metrics/metrics.go` and `mocks/metrics.go` stay byte-identical; `git_rest_body_merged_total` lands in prompt 2.
- Do NOT edit `CHANGELOG.md`, `docs/`, or `docs/deployment.md` in this prompt; they land in prompt 3.
- The conflict-failure sentinel `ErrConflictResolutionFailed` remains the only failure error; no new exported error types.
- Errors via `github.com/bborbe/errors` — no `fmt.Errorf`, no bare `return err`.
- Keep `glog` in `pkg/git/yaml_merge_resolver.go`; do not introduce `slog` there and do not mix loggers.
- No new dependency.
- Do NOT add per-repository, per-path, or per-key configuration.
- Do NOT deduplicate a shared prefix or suffix between the two sides. The 3-vs-4 append case deliberately resolves to ours, a blank line, then theirs, which repeats shared lines. That is the specified behaviour.
- Do NOT auto-resolve a region that crosses the frontmatter/body boundary, a non-markdown path, or a markdown file with no frontmatter block.
- Do NOT auto-drain, delete, or rewrite existing `_conflicts/` entries.
- Do NOT add a repeat-guard for two replicas resolving the same divergence (see the spec's concurrency failure-mode row).
- Do NOT add a scenario file under `scenarios/`.
- Do NOT run `go mod vendor`, and do NOT write `-mod=vendor` in any command — this repo does not commit `vendor/`.
- `prealloc` is enabled in `.golangci.yml`: preallocate `regions`, `replacements` and `conflictRegions` with a capacity instead of append-only nil slices.
- Interface → Constructor → Struct → Method; factory functions stay pure composition.
- Tests use Ginkgo v2 / Gomega and Counterfeiter mocks; `make precommit` is the gate.
</constraints>

<verification>
Run from the repo root. If the daemon runs with `hideGit=true`, `.git` is masked inside the container and bare `git` commands die with `fatal: not a git repository`; this block uses none either way. `ROOTDIR=/workspace` is the dark-factory convention for that case; this repo's `Makefile` does not itself reference `ROOTDIR`, so the prefix is inert here and harmless.

```bash
ROOTDIR=/workspace make precommit
```

Must exit 0.

Focused runs while implementing (a `0 of 0` result means the focus string matched nothing — fix the focus, do not accept it):

```bash
go test ./pkg/git/... -mod=mod -ginkgo.focus='body-only conflict regions'
go test ./pkg/git/... -mod=mod -ginkgo.focus='Body-only conflict regions: extended'
```

The committed block reports 4 passed, 0 failed on the first command. The new symbols are present (each `grep -n` must print at least one match):

```bash
grep -n 'func planBodyRegions' pkg/git/yaml_merge_resolver.go
grep -n 'func regionInsideBody' pkg/git/yaml_merge_resolver.go
grep -n 'func spliceConflictRegions' pkg/git/yaml_merge_resolver.go
grep -n 'func (r \*yamlMergeResolver) applyBodyRule' pkg/git/yaml_merge_resolver.go
grep -n 'func resolvedBodyLines' pkg/git/yaml_merge_resolver.go
grep -n 'combineBodies(region.oursBody, region.theirsBody)' pkg/git/yaml_merge_resolver.go
grep -n 'Body-only conflict regions: extended' pkg/git/yaml_merge_resolver_test.go
```

Frozen surfaces and boundary checks (each `grep -n` must print at least one match; each `! grep -q` must exit 0):

```bash
grep -n 'func spliceScalarRegions' pkg/git/yaml_merge_resolver.go
grep -n 'func combineBodies' pkg/git/yaml_merge_resolver.go
grep -n 'ConflictResolver interface' pkg/git/conflict_resolver.go
ls mocks/conflict_resolver.go
! grep -q 'git_rest_body_merged_total' pkg/metrics/metrics.go
! grep -q '^## Unreleased' CHANGELOG.md
```

Absence is written as `! grep -q`, never as `grep -c` — `grep -c` prints `0` and exits non-zero, so an absence assertion written that way fails the step it was meant to pass.

Self-check before finishing: re-run `ROOTDIR=/workspace make precommit`, then walk each requirement against the change.
</verification>
