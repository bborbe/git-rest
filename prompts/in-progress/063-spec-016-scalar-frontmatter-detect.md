---
status: committing
spec: [016-yaml-scalar-conflict-autoresolve]
summary: Added scalar-frontmatter conflict detection with per-region counter max resolution, splicing resolved lines in place and wiring the fallback, with full Ginkgo and internal table tests.
execution_id: git-rest-scalar-autoresolve-exec-063-spec-016-scalar-frontmatter-detect
dark-factory-version: v0.198.7
created: "2026-10-19T20:50:37Z"
queued: "2026-10-19T21:23:08Z"
started: "2026-10-19T21:33:58Z"
completed: "2026-10-19T21:37:45Z"
---

# Resolve scalar frontmatter conflicts: per-region detection and counter merge

<summary>
- A merge conflict confined to single-line scalar keys inside a markdown file's YAML frontmatter is resolved in place instead of quarantined.
- Only conflicts whose every line is a single-line `key: value` scalar qualify; anything else keeps quarantining exactly as today.
- Both sides of every conflicting key must carry the same key set, with no duplicates, or the whole file is rejected.
- Allowlisted integer counters take the greater of the two sides.
- The resolved file keeps every non-conflicting frontmatter line, the closing delimiter and the body byte-for-byte.
- Multiple conflict regions in one file are handled per region from their own line ranges, never concatenated.
- A file with one qualifying region and one non-qualifying region quarantines as a whole.
- Conflicts outside the frontmatter, list items, block scalars, nested mappings and flow collections all still quarantine.
- Non-markdown files are untouched by the new rule.
- The existing deep-merge behaviour for whole-block conflicts is unchanged.
</summary>

<objective>
Make the YAML merge resolver recognise a conflict that lies entirely inside a markdown file's frontmatter and whose every conflicting line is a single-line scalar mapping entry, and resolve the integer-counter keys in place so the vault keeps one canonical file and no `_conflicts/` entry is created. This prompt lands the detection layer plus the counter rule; every non-counter key still makes the whole file quarantine until the follow-up prompt adds the newer-side rule.
</objective>

<context>
There is no repo-root `CLAUDE.md` in this worktree — do not look for one. The project conventions are restated in `<constraints>` below.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors`; never `fmt.Errorf`, never a bare `return err`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, external `git_test` package, real `git` via `os/exec` against a temp working tree
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — this file's logger is `github.com/golang/glog`; do not introduce `slog` here and do not mix loggers
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — linter limits (funlen 80 lines / 50 statements, gocognit 20, nestif 4, golines 100)
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` — read-only context: the new counter lands in a later prompt, not this one

Files to read in full before editing (anchor by symbol name, not line number):

- `pkg/git/yaml_merge_resolver.go` — `resolveOne`, `splitConflictSides`, `splitFrontmatter`, `parseYAML`, `deepMergeTheirsWins`, `combineBodies`, `runGitAdd`, `safePath`, the failure-category constants
- `pkg/git/yaml_merge_resolver_test.go` — `setupYAMLConflictRepo`, `parseFrontmatterOf`, `bodyOf`, the existing `Describe` blocks
- `pkg/git/git.go` — `resolveEachPath` (calls `g.resolver.Resolve(ctx, []string{path})` once per path) and `commitAndPushMerge` (commits the staged tree with `merge: resolved=[…] quarantined=[…]`)
- `pkg/git/conflict_resolver.go` — the `ConflictResolver` interface and its counterfeiter directive; both stay byte-identical
- `docs/deployment.md` — the `**Vault-write mode**` bullet under `## Operational notes`, for the behaviour this rule changes (you do not edit it in this prompt)

**The shape this prompt targets, verified against git.** Base file `---\ntrigger_count: 9\ntitle: probe\n---\nbody\n`; ours commits `trigger_count: 3`, theirs commits `trigger_count: 0`; `git merge --no-edit theirs` leaves:

```
---
<<<<<<<
trigger_count: 3
=======
trigger_count: 0
>>>>>>>
title: probe
---
body
```

`splitConflictSides` extracts ours `---\ntrigger_count: 3\n` and theirs `---\ntrigger_count: 0\n` (it prepends `---\n` when absent and concatenates every region). `splitFrontmatter` then fails on both sides because neither extracted side carries a closing `---`, so today the file is counted as `no_frontmatter` and quarantined. That is the defect this prompt closes.

**Verified facts about the existing helpers — do not re-derive, do not contradict:**

- `splitConflictSides` returns `ok=false` when either side has zero lines. That branch keeps its current behaviour: an empty side never reaches the new rule.
- `splitConflictSides` concatenates every conflict region into one ours block and one theirs block. The new rule must NOT reuse it; regions are enumerated from the original file so each region is resolved from its own line range.
- `splitFrontmatter` requires the input to start with a line that is exactly `---` (after trimming `\r`) and to contain a later line that is exactly `---`; it returns the frontmatter and the body separately.
- `resolveOne` reads the conflicted file with `os.ReadFile`, and the resolver is called with one path at a time in production.
- `yaml.v3` is already a direct dependency (`gopkg.in/yaml.v3 v3.0.1` in `go.mod`). No new dependency.
- `yaml.Node` constants verified against the pinned version: mapping `Kind == yaml.MappingNode` (4), sequence `Kind == yaml.SequenceNode` (2), scalar `Kind == yaml.ScalarNode` (8); block scalars carry `Style == yaml.LiteralStyle` (8) or `yaml.FoldedStyle` (16); a flow collection parses as a sequence/mapping node; a quoted scalar carries `Style == yaml.DoubleQuotedStyle` (2); an anchor populates `node.Anchor`.
</context>

<requirements>

## 1. Enumerate conflict regions from the original file

Add to `pkg/git/yaml_merge_resolver.go`:

```go
// conflictRegion is one conflict region located in the original file's lines.
type conflictRegion struct {
	startLine   int      // index of the "<<<<<<<" line
	oursLines   []string // lines between "<<<<<<<" and "=======", in order
	theirsLines []string // lines between "=======" and ">>>>>>>", in order
	endLine     int      // index of the ">>>>>>>" line
}

// enumerateConflictRegions returns every conflict region in lines, in file
// order. ok is false when the markers are unbalanced, a region is nested, or
// the file holds no region at all.
func enumerateConflictRegions(lines []string) ([]conflictRegion, bool)
```

Rules:

- A line opens a region when `strings.HasPrefix(line, "<<<<<<<")`; the separator is a line with prefix `=======`; the closer is a line with prefix `>>>>>>>`. These are the same marker prefixes `splitConflictSides` recognises.
- Return `ok=false` for: a second `<<<<<<<` while a region is already open; a `=======` or `>>>>>>>` outside a region; a region closed without a separator; a region still open at end of file; zero regions.
- Do not call `splitConflictSides`.

## 2. Locate the frontmatter block in the original file

```go
// frontmatterBounds returns the index of the closing "---" line in the original
// file. The opening delimiter is always line 0 when ok is true.
func frontmatterBounds(lines []string) (close int, ok bool)
```

- Trim a trailing `\r` from each candidate line before comparing, exactly as `splitFrontmatter` does.
- `lines[0]` must equal `---`; otherwise `ok=false`.
- `close` is the index of the first later line equal to `---`. No such line → `ok=false`.

## 3. Parse one single-line scalar mapping entry

```go
// scalarEntry is one parsed frontmatter line: the raw line plus its parsed YAML
// value node.
type scalarEntry struct {
	line  string
	value *yaml.Node
}

// parseScalarEntry reports whether line is a single-line "key: value" mapping
// entry whose value is a YAML scalar, returning the key.
func parseScalarEntry(line string) (key string, entry scalarEntry, ok bool)
```

Acceptance rules — every one is load-bearing. Requirement 8's fixtures exercise the rejection shapes (rules 2, 3 and 7, plus the key-set/duplicate guards); add direct `parseScalarEntry` and `enumerateConflictRegions` table tests in a `package git` test file (precedent: `pkg/git/resolve_conflict_merge_test.go`) — the first covering all ten parse rules, including an empty line, a `#` comment, a parse error (`a: b: c`), an anchored key (`&x a: 1`), a null value (`key:`), a trailing comment (`a: 1 # note`) and a hex value (`k: 0x10`); the second covering the four `ok=false` branches (nested `<<<<<<<`, a `=======`/`>>>>>>>` outside a region, a region without a separator, and a region left open at EOF).

1. Trim a trailing `\r`. Reject an empty line.
2. Reject a line whose first byte is a space or tab (an indented line is a nested value, never a top-level entry).
3. Reject a line starting with `- ` or a bare `-` (a sequence item), and a line starting with `#` (a comment).
4. Parse the single line with `yaml.Unmarshal([]byte(line), &node)` where `node` is a `yaml.Node`. A parse error rejects (`a: b: c` is a parse error; `key: *alias` is a parse error when the anchor is not defined).
5. The document must hold exactly one content node (`len(node.Content) == 1`), and that node must be a `yaml.MappingNode` with exactly two content entries. This rejects a top-level sequence (`- a`).
6. The key node must be `yaml.ScalarNode`, tag `!!str`, non-empty `Value`, empty `Anchor`.
7. The value node must be `yaml.ScalarNode` — this rejects a flow sequence (`tags: [a, b]`) and a flow mapping (`meta: {a: b}`) — with empty `Anchor` (rejects `key: &x 1`) and with `Style` neither `yaml.LiteralStyle` nor `yaml.FoldedStyle` (rejects a block scalar `desc: |` / `desc: >`).
8. `!!null` is accepted: `key:` with no value parses to a null scalar, and null is a scalar for this rule.
9. A trailing comment is accepted: `a: 1 # note` parses to the integer scalar `1`, and `entry.line` keeps the raw line including the comment so the later splice re-emits it verbatim.
10. `entry.line` is the line as it appeared in the original file (after the `\r` trim), not a re-serialisation.

## 4. Decide whole-file eligibility

```go
// scalarKeyEntry pairs a key with its parsed entry on the ours side.
type scalarKeyEntry struct {
	key   string
	entry scalarEntry
}

// scalarRegion is an eligible conflict region with both sides parsed.
type scalarRegion struct {
	conflictRegion
	ours   []scalarKeyEntry         // ours-side order
	theirs map[string]scalarEntry
}

// planScalarRegions decides whether the whole file is eligible for the
// scalar-frontmatter rule. It inspects shape only: no timestamps, no I/O.
func planScalarRegions(lines []string) ([]scalarRegion, bool)
```

Return `ok=false` unless ALL of the following hold:

1. `frontmatterBounds(lines)` succeeds.
2. `enumerateConflictRegions(lines)` succeeds.
3. Every region satisfies `startLine > 0` and `endLine < close` — the region lies entirely inside the frontmatter block, after the opening delimiter and before the closing one.
4. Regions do not overlap: each region's `startLine` is greater than the previous region's `endLine`.
5. Every line in `oursLines` and `theirsLines` parses via `parseScalarEntry`.
6. Each side carries at least one key.
7. No duplicate key within either side.
8. The two key sets are identical: same length, and every ours key exists in `theirs`. A key present on only one side rejects the whole file.
9. The key set is non-empty.

The markdown-extension check does not belong here — `planScalarRegions` sees only lines. It is done by the caller in requirement 7.

## 5. Counter rule

```go
// isCounterKey reports whether key takes the max-of-both-sides rule.
func isCounterKey(key string) bool
```

- True for `trigger_count`, `retry_count`, and any key with prefix `metrics_`.

```go
// integerValue returns the integer a parsed scalar node carries, and whether it
// carries one at all.
func integerValue(node *yaml.Node) (int64, bool)
```

- True only when `node.Tag == "!!int"` and `strconv.ParseInt(node.Value, 10, 64)` succeeds. A quoted value (`"3"`) carries tag `!!str` and is not an integer. A hex value (`0x10`) carries tag `!!int` but does not parse base 10, so it is not an integer for this rule.

```go
// resolveScalarEntry returns the replacement line for one key in one region.
// ok is false when the rule cannot decide this key, which makes the whole file
// ineligible.
func resolveScalarEntry(key string, ours, theirs scalarEntry) (line string, ok bool)
```

In this prompt: when `isCounterKey(key)` and both `integerValue` calls succeed, return `key + ": " + strconv.FormatInt(max, 10)` with `ok=true`, where `max` is the greater of the two integers. Every other key, and a counter whose value is not an integer on both sides, returns `ok=false`. The newer-side rule for those keys is the next prompt; do not implement it here.

## 6. Splice the resolved lines into the original file

```go
// spliceScalarRegions returns the original file with each conflict region
// replaced by its resolved lines. Every line outside a region is copied
// verbatim, so the closing "---" and the body are byte-identical.
func spliceScalarRegions(
	lines []string,
	regions []scalarRegion,
	replacements [][]string,
) string
```

- Walk `lines` by index. Before each region append the untouched lines; then append that region's replacement lines; then continue at `region.endLine + 1`.
- Join the result with `"\n"`. `strings.Split` / `strings.Join` round-trips a trailing newline because the final empty element survives. A `\r` trimmed by `parseScalarEntry` is not re-added, so a CRLF file's resolved region line is LF-terminated while untouched lines keep their `\r`; this rule targets LF vault files and that is accepted.
- `regions` is `[]scalarRegion` (not `[]conflictRegion`) so the embedded fields are reachable without a slice conversion.

## 7. Apply the rule and wire the fallback

```go
// applyScalarFrontmatterRule attempts the scalar-frontmatter rule on the raw
// conflicted file. applied reports whether the rule took responsibility for the
// file; when it did, the file has been written and staged and err is the outcome.
func (r *yamlMergeResolver) applyScalarFrontmatterRule(
	ctx context.Context, path, abs, raw string,
) (applied bool, err error)
```

Sequence:

1. If `!strings.EqualFold(filepath.Ext(path), ".md")`, return `(false, nil)`.
2. `lines := strings.Split(raw, "\n")`.
3. `regions, ok := planScalarRegions(lines)`; `!ok` → return `(false, nil)`.
4. Build `replacements`: for each region, iterate `region.ours` in order; for each key call `resolveScalarEntry(key, oursEntry, region.theirs[key])`. Any `ok=false` → return `(false, nil)` before any write.
5. `final := spliceScalarRegions(lines, regions, replacements)`.
6. Check `ctx.Done()` first, mirroring `quarantineOne`'s guard; on cancellation return `(true, errors.Wrap(ctx, ctx.Err(), "scalar resolve cancelled"))`. Then `os.WriteFile(abs, []byte(final), 0o600)`. On failure: `r.metrics.IncResolverFailure(resolverFailureWrite)`, one `glog.Warningf` naming the path, return `(true, errors.Wrap(ctx, ErrConflictResolutionFailed, "write scalar-resolved file"))`.
7. `r.runGitAdd(ctx, path)` — it already increments `resolverFailureGitAdd` and logs on failure. Return `(true, err)`.
8. Return `(true, nil)`.

Then restructure `resolveOne`. Today it returns `no_frontmatter` at two separate `splitFrontmatter` failure points. Replace those two branches with one combined branch, and keep the `splitConflictSides(string(raw))` call and its failure branch exactly as they are:

```go
oursFM, oursBody, oursOK := splitFrontmatter(ours)
theirsFM, theirsBody, theirsOK := splitFrontmatter(theirs)
if !oursOK || !theirsOK {
	applied, ruleErr := r.applyScalarFrontmatterRule(ctx, path, abs, string(raw))
	if applied {
		return ruleErr
	}
	r.metrics.IncResolverFailure(resolverFailureNoFront)
	glog.Warningf(
		"yaml-merge-resolver: %q has no frontmatter delimiter pair on one side and the scalar-frontmatter rule does not apply",
		path,
	)
	return errors.Wrap(ctx, ErrConflictResolutionFailed, "no frontmatter delimiter pair")
}
```

The rest of `resolveOne` — `parseYAML`, `deepMergeTheirsWins`, `yaml.Marshal`, `combineBodies`, the final write and `runGitAdd` — stays unchanged. `no_frontmatter` must be incremented exactly once on the fallback path, never twice.

## 8. Tests

All new specs live in a new `Describe("Scalar frontmatter rule", …)` block nested inside the existing top-level `Describe("YAMLMergeResolver", …)` in `pkg/git/yaml_merge_resolver_test.go` (`package git_test`, Ginkgo v2 / Gomega), so the `BeforeEach` that sets `ctx` and `fakeMetrics` applies. Existing specs stay byte-identical and must still pass.

Add two helpers next to `setupYAMLConflictRepo`:

```go
// setupScalarMergeRepo builds a real repo whose note.md is left in a genuine
// conflicted merge. base, ours and theirs are full file contents. oursDate and
// theirsDate set GIT_AUTHOR_DATE and GIT_COMMITTER_DATE (RFC3339) on the
// respective commits; an empty string leaves the commit date at the current
// time. Returns workDir, the repo-relative path, and a cleanup func.
func setupScalarMergeRepo(
	base, ours, theirs, oursDate, theirsDate string,
) (workDir, relPath string, cleanup func())

// setupHandWrittenConflict builds a repo with the given repo-relative path
// committed as a base file and then overwritten with a hand-written conflicted
// body (staged, not committed), so the resolver sees exactly the bytes given.
func setupHandWrittenConflict(
	path, conflicted string,
) (workDir, relPath string, cleanup func())
```

`setupScalarMergeRepo` mirrors `setupYAMLConflictRepo` (init, config, base commit, branch `theirs`, theirs commit, back to `main`, ours commit, `git merge --no-edit theirs` expected to fail) but takes full file contents and applies the date overrides through `cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)` when the date is non-empty. This prompt passes `""` for both dates.

Cases (each resolution `It` must be distinguishable before vs after this change; the rejection guards in cases 3, 6, 7 and 8 assert that a pre-existing quarantine outcome is preserved and are not expected to change):

1. **Counter conflict resolves in place (spec AC 1).** `setupScalarMergeRepo` with base `---\ntrigger_count: 9\ntitle: probe\n---\nbody\n`, ours `---\ntrigger_count: 3\ntitle: probe\n---\nbody\n`, theirs `---\ntrigger_count: 0\ntitle: probe\n---\nbody\n` (all three arguments are full file contents, per the helper docstring). `git.NewYAMLMergeResolver(workDir, fakeMetrics).Resolve(ctx, []string{rel})` succeeds. Read the file and assert: exactly one line equals `trigger_count: 3`; zero lines start with `<<<<<<<`; the lines `title: probe`, `---` (closing) and `body` are present; `git diff --name-only --diff-filter=U` in `workDir` prints nothing; `fakeMetrics.IncResolverFailureCallCount() == 0`.
2. **Counter table (spec AC 2, counter rows).** Use `DescribeTable` / `Entry`. Through `Resolve` on `setupHandWrittenConflict("note.md", …)` with one region and the counter rows: `retry_count: 1` vs `5` → `retry_count: 5`; `metrics_retries: 2` vs `7` → `metrics_retries: 7`; `trigger_count: 3` vs `0` → `trigger_count: 3`. Assert the exact replacement line is present and no marker line remains.
3. **Rejection table (spec AC 4).** Use `DescribeTable` / `Entry`. Each fixture is hand-written with a closing `---` after the region, so both extracted sides carry no closing `---` and the existing path fails first; all fixtures except the outside-frontmatter one place the region inside the frontmatter. For each: `errors.Is(err, git.ErrConflictResolutionFailed)` is true, `fakeMetrics.IncResolverFailureCallCount() == 1`, `fakeMetrics.IncResolverFailureArgsForCall(0) == "no_frontmatter"`, and the file's bytes equal the pre-call bytes. Fixtures:
   - list item: ours `- a` vs theirs `- b`
   - block scalar: ours `desc: |` then `  line one` vs theirs `desc: plain`
   - nested mapping: ours `owner:` then `  name: a` vs theirs `owner:` then `  name: b`
   - flow collection: ours `tags: [a, b]` vs theirs `tags: [c]`
   - outside frontmatter: `---\ntitle: probe\n---\n` then a region with ours `body a` vs theirs `body b`
   - one-sided key: ours `a: 1` then `b: 2` vs theirs `a: 1`
   - duplicate key: ours `a: 1` then `a: 2` vs theirs `a: 3`
4. **All-or-nothing (spec AC 5).** One file with two regions — the first a counter conflict (`trigger_count: 3` vs `0`), the second a list conflict (`- a` vs `- b`). Assert `ErrConflictResolutionFailed` and that the file bytes are unchanged.
4b. **Multiple regions resolve per region (spec Desired Behavior 1).** One file with two counter regions — `trigger_count: 3` vs `0` and `retry_count: 1` vs `5`, both inside the frontmatter, separated by a non-conflicting `title: probe` line. Assert both resolved lines are present, no marker line remains, and the separating line is byte-identical.
5. **Byte preservation (spec AC 6).** Fixture exactly:

   ```
   ---
   <<<<<<< HEAD
   trigger_count: 3
   =======
   trigger_count: 0
   >>>>>>> theirs
   title: probe
   tags:
     - x
   ---
   body line one
   body line two
   ```

   Expected written file exactly:

   ```
   ---
   trigger_count: 3
   title: probe
   tags:
     - x
   ---
   body line one
   body line two
   ```

   with a trailing newline. Assert `Expect(string(written)).To(Equal(expected))` — the whole file, not a substring. The `tags:` list sits outside the conflict region, so it must be copied verbatim even though a list line would be rejected inside a region.
6. **Non-markdown is untouched.** `setupHandWrittenConflict("note.txt", …)` with the same counter shape. Assert `ErrConflictResolutionFailed`, one `no_frontmatter`, and unchanged bytes.
7. **A conflict whose extracted side is empty keeps the pre-existing path.** A hand-written region with an empty ours side returns `ErrConflictResolutionFailed` with `no_frontmatter`; this is the `splitConflictSides` branch, not the new rule, and it must not be routed through `planScalarRegions`.
8. **A region that crosses the frontmatter/body boundary rejects.** A fixture whose `<<<<<<<` starts inside the frontmatter and whose `>>>>>>>` is after the closing `---` returns `ErrConflictResolutionFailed` with `no_frontmatter` and leaves the file untouched.
9. **`git add` failure inside the new rule reports `git_add_failed`, not `no_frontmatter`.** A counter-only scalar-frontmatter conflict in a repo whose `.git` is removed after the conflict is produced returns the sentinel, increments `git_add_failed` once, and does not increment `no_frontmatter`.
10. **Write failure inside the new rule reports `write_failed`.** Make the file read-only after the conflict is produced; assert the sentinel and `write_failed` with the file unchanged.

## 9. Self-check before finishing

Run every command in `<verification>` from the repo root and confirm each result. Then walk each requirement: a single-line scalar counter conflict resolves in place with no markers, every rejection shape returns the sentinel with `no_frontmatter` and leaves the file untouched, a two-region file with one bad region quarantines as a whole, the golden file is byte-identical outside the region, and the existing deep-merge specs still pass unedited.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Extend `pkg/git/yaml_merge_resolver.go`. Do NOT change the `ConflictResolver` interface (`pkg/git/conflict_resolver.go`) or its counterfeiter mock (`mocks/conflict_resolver.go`); both stay byte-identical.
- Do NOT change `MarkerResolver`, the quarantine path, `pkg/git/git.go`, or the pull loop.
- The new rule must NOT reuse `splitConflictSides`; regions are enumerated from the original file so they are never concatenated.
- The existing deep-merge path (`splitConflictSides` → `splitFrontmatter` → `parseYAML` → `deepMergeTheirsWins` → `combineBodies`) and its tests must not regress.
- The conflict-failure sentinel (`ErrConflictResolutionFailed`) remains the only failure error; no new exported error types.
- Errors via `github.com/bborbe/errors` — no `fmt.Errorf`, no bare `return err`.
- Keep `glog` in `pkg/git/yaml_merge_resolver.go`; do not introduce `slog` there and do not mix loggers.
- No new dependency; use the already-direct `gopkg.in/yaml.v3`.
- Do NOT add a metric in this prompt. `pkg/metrics/metrics.go` and `mocks/metrics.go` stay byte-identical here; the `git_rest_autoresolved_total` counter lands in a later prompt.
- Do NOT edit `CHANGELOG.md` or `docs/` in this prompt. The repo's `docs/dod.md` validation prompt asks for a `## Unreleased` CHANGELOG entry; that entry is deliberately deferred to prompt 3 of this spec (`3-spec-016-metric-and-docs.md`) — do not add it here and do not treat that DoD line as a blocker.
- `prealloc` is enabled in `.golangci.yml`: preallocate `regions`, `ours` and `replacements` (`make([]T, 0, len(lines))` / `len(regions)`) instead of append-only nil slices.
- Do NOT add per-repository or per-key configuration for the rule.
- Do NOT auto-resolve body conflicts, list items, block scalars, nested mappings, one-sided add/delete, or duplicate keys — those keep quarantining.
- The rule applies to markdown files only; non-markdown conflicts keep the current path.
- Timestamps are not used in this prompt; the newer-side rule is the follow-up prompt. Do not call `time.Now()`.
- Do NOT add a scenario file under `scenarios/`.
- `NewYAMLMergeResolver`'s signature does not change in this prompt; `main.go` and `main_resolver_selection_test.go` stay untouched.
- Do NOT run `go mod vendor`, and do NOT write `-mod=vendor` in any command — this repo does not commit `vendor/`.
- Every new `.go` file needs the repo's copyright header (`// Copyright (c) 2026 Benjamin Borbe All rights reserved.` / BSD-style licence lines), because `make precommit` runs `addlicense`.
- Interface → Constructor → Struct → Method; factory functions stay pure composition.
- Tests use Ginkgo v2 / Gomega and Counterfeiter mocks; the direct `parseScalarEntry`/`enumerateConflictRegions` table tests use `testing.T` in a `package git` file, following `pkg/git/resolve_conflict_merge_test.go`; `make precommit` is the gate.
</constraints>

<verification>
Run from the repo root. The daemon may run with `hideGit=true`, which masks `.git` inside the container — under that setting bare `git` commands die with `fatal: not a git repository`, so do not put one in this block. `ROOTDIR=/workspace` is the dark-factory convention for that case; this repo's `Makefile` does not itself reference `ROOTDIR`, so the prefix is inert here and harmless.

```bash
ROOTDIR=/workspace make precommit
```

Must exit 0.

Focused run while implementing (a `0 of 0` result means the focus string matched nothing — fix the focus, do not accept it):

```bash
go test ./pkg/git/... -mod=mod -ginkgo.focus='Scalar frontmatter rule'
```

The new helpers and the rule are present (each `grep -n` must print at least one match):

```bash
grep -n 'func enumerateConflictRegions' pkg/git/yaml_merge_resolver.go
grep -n 'func frontmatterBounds' pkg/git/yaml_merge_resolver.go
grep -n 'func parseScalarEntry' pkg/git/yaml_merge_resolver.go
grep -n 'func planScalarRegions' pkg/git/yaml_merge_resolver.go
grep -n 'func isCounterKey' pkg/git/yaml_merge_resolver.go
grep -n 'func resolveScalarEntry' pkg/git/yaml_merge_resolver.go
grep -n 'func spliceScalarRegions' pkg/git/yaml_merge_resolver.go
grep -n 'func (r \*yamlMergeResolver) applyScalarFrontmatterRule' pkg/git/yaml_merge_resolver.go
grep -n 'Scalar frontmatter rule' pkg/git/yaml_merge_resolver_test.go
```

Frozen surfaces and absence checks (each `! grep -q` must exit 0):

```bash
grep -n 'deepMergeTheirsWins' pkg/git/yaml_merge_resolver.go
grep -n 'splitConflictSides(string(raw))' pkg/git/yaml_merge_resolver.go
grep -n 'ConflictResolver interface' pkg/git/conflict_resolver.go
ls mocks/conflict_resolver.go
! grep -q 'autoresolved' pkg/git/yaml_merge_resolver.go
! grep -q 'git_rest_autoresolved_total' pkg/metrics/metrics.go
! grep -q '^## Unreleased' CHANGELOG.md
```

Absence is written as `! grep -q`, never as `grep -c` — `grep -c` prints `0` and exits non-zero, so an absence assertion written that way fails the step it was meant to pass.

Self-check before finishing: re-run `ROOTDIR=/workspace make precommit`, then walk each requirement above against the change — the counter conflict resolves in place, every rejection shape quarantines with `no_frontmatter` and an untouched file, the two-region file is all-or-nothing, the golden file is byte-identical outside the region, and no existing spec was edited.
</verification>
