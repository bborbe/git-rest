---
status: completed
spec: [016-yaml-scalar-conflict-autoresolve]
summary: Added a git-backed merge-parent timestamp getter and extended the scalar frontmatter rule so counters take the max while every other scalar takes the newer parent's raw line, with stubbed-constructor tests, a real-git end-to-end test, a linked-worktree variant, and quarantine coverage for missing/multi-valued/unreadable merge heads.
execution_id: git-rest-scalar-autoresolve-exec-064-spec-016-newer-side-rule
dark-factory-version: v0.198.7
created: "2026-10-19T20:50:37Z"
queued: "2026-10-19T21:23:08Z"
started: "2026-10-19T21:43:20Z"
completed: "2026-10-19T21:45:08Z"
---

# Resolve scalar frontmatter conflicts: newer-side rule and timestamp getter

<summary>
- Every non-counter scalar key in a qualifying frontmatter conflict now takes the value from the newer merge parent.
- A counter whose value is not an integer on both sides falls back to the newer side instead of blocking resolution.
- A tie between the two merge parents takes the local side.
- The newer side is read from git's own committer timestamps, never from the wall clock.
- A missing, multi-valued or unreadable merge parent makes the whole file quarantine rather than guess.
- The winning non-counter line is re-emitted verbatim, so quoting and trailing comments survive.
- Counter-only conflicts still resolve without any git timestamp lookup.
- A conflict whose two regions mix resolvable and unresolvable content still quarantines as a whole.
</summary>

<objective>
Complete the per-key rule for scalar frontmatter conflicts: counters still take the greater integer, and every other scalar now takes the value from the merge parent with the newer committer timestamp (tie goes to the local side). The timestamp comes from git's own objects, so the result is deterministic and testable, and a file whose merge parent is missing, multi-valued or unreadable keeps quarantining instead of resolving on a guess.
</objective>

<context>
There is no repo-root `CLAUDE.md` in this worktree — do not look for one. The project conventions are restated in `<constraints>` below.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors`; never `fmt.Errorf`, never a bare `return err`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, external `git_test` package, real `git` via `os/exec`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — this file's logger is `github.com/golang/glog`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — linter limits (funlen 80 lines / 50 statements, gocognit 20, nestif 4, golines 100)

Files to read in full before editing (anchor by symbol name, not line number):

- `pkg/git/yaml_merge_resolver.go` — the prompt-1 additions `conflictRegion`, `enumerateConflictRegions`, `frontmatterBounds`, `scalarEntry`, `parseScalarEntry`, `scalarRegion`, `planScalarRegions`, `isCounterKey`, `integerValue`, `resolveScalarEntry`, `spliceScalarRegions`, `applyScalarFrontmatterRule`; plus `resolveOne`, `runGitAdd`, `NewYAMLMergeResolver`
- `pkg/git/yaml_merge_resolver_test.go` — `setupScalarMergeRepo`, `setupHandWrittenConflict`, the `Describe("Scalar frontmatter rule", …)` block (all three are prompt-1 outputs; prompt 1 must have completed before this prompt runs)
- `pkg/git/git.go` — `pullMergeAndPush` runs `git merge --no-edit <upstream>` and only then `resolveConflictMerge`; `resolveEachPath` calls `g.resolver.Resolve(ctx, []string{path})` once per path
- `pkg/git/resolve_conflict_merge_test.go` — precedent for a `package git` test that runs real git against a temp repo
- `main_resolver_selection_test.go` — asserts the resolver type by reflection; it must stay untouched

**Verified facts — do not re-derive, do not contradict:**

- The pull path is a merge, not a rebase: `pullMergeAndPush` runs `git merge --no-edit`, so `.git/MERGE_HEAD` exists while the resolver runs. `HEAD` is the local (ours) side and `MERGE_HEAD` is the upstream (theirs) side.
- `git rev-parse HEAD` prints one SHA.
- **`git rev-parse MERGE_HEAD` prints only the FIRST SHA even when `.git/MERGE_HEAD` holds several lines** — verified against a repo whose `MERGE_HEAD` was written with two SHAs. It therefore cannot detect a multi-valued merge parent on its own. The getter must locate the file with `git rev-parse --git-path MERGE_HEAD` and read it to count its lines.
- `git rev-parse --git-path MERGE_HEAD` prints the path of the merge-head file (`.git/MERGE_HEAD` for an ordinary repo; an absolute path for a linked worktree). Resolve a relative result against `repoPath` before reading.
- `git show -s --format=%ct <sha>` prints the committer timestamp as a decimal Unix integer.
- A hand-written conflicted file in a repo that is not mid-merge has no `.git/MERGE_HEAD`; the rule must not consult timestamps for a counter-only file, so prompt 1's counter specs keep passing there.
- `runGitAdd` in `pkg/git/yaml_merge_resolver.go` is the existing subprocess pattern: `exec.CommandContext(ctx, "git", …)`, `cmd.Dir = r.repoPath`, combined output captured, failure logged and wrapped.
- The prompt-1 `applyScalarFrontmatterRule` returns `(false, nil)` when the rule does not apply; the caller then increments `no_frontmatter` and returns the sentinel. A timestamp failure must take that same path, so the file quarantines and is never partially written.
- `yaml.v3` node facts: a quoted scalar keeps `Style == yaml.DoubleQuotedStyle`; the raw line is what gets re-emitted, so quoting and trailing comments survive untouched.
</context>

<requirements>

## 1. Add a merge-parent timestamp getter to the resolver

In `pkg/git/yaml_merge_resolver.go`, add a field to `yamlMergeResolver`:

```go
type yamlMergeResolver struct {
	repoPath  string
	metrics   metrics.Metrics
	sideTimes func(ctx context.Context) (ours int64, theirs int64, err error)
}
```

`NewYAMLMergeResolver` keeps its exact signature and populates the field with the git-backed getter:

```go
func NewYAMLMergeResolver(repoPath string, m metrics.Metrics) ConflictResolver {
	return &yamlMergeResolver{
		repoPath:  repoPath,
		metrics:   m,
		sideTimes: gitMergeSideTimes(repoPath),
	}
}
```

Implement the getter in the same file:

```go
// gitMergeSideTimes returns the committer timestamps of the merge's two
// parents: HEAD (ours) and MERGE_HEAD (theirs). It reads git objects only.
func gitMergeSideTimes(repoPath string) func(
	ctx context.Context,
) (ours int64, theirs int64, err error)
```

Behavior:

1. Resolve `HEAD` with `git rev-parse HEAD` in `repoPath`; trim whitespace; require exactly one non-empty line, otherwise return an error.
2. Resolve the merge-head file path with `git rev-parse --git-path MERGE_HEAD`; trim. If the result is relative, join it with `repoPath`.
3. Read that file. A missing or unreadable file returns an error. Split its content on `\n`, trim `\r` and surrounding whitespace per line, drop empty lines, and require **exactly one** non-empty line. Zero lines (no merge parent) or two or more lines (multi-valued merge parent) return an error. Do not use `git rev-parse MERGE_HEAD` for this step — it prints only the first SHA and would silently accept a multi-valued file.
4. Read each timestamp with `git show -s --format=%ct <sha>`; parse with `strconv.ParseInt(strings.TrimSpace(out), 10, 64)`. A parse failure returns an error.
5. Use `exec.CommandContext(ctx, "git", …)` with `cmd.Dir = repoPath`, capturing combined output, mirroring `runGitAdd` — including its `// #nosec G204 -- git binary is hardcoded; args are internal` comment, which gosec requires on every `exec.CommandContext` call. Reading the merge-head file uses `os.ReadFile` on the resolved path with `// #nosec G304 -- path comes from git rev-parse --git-path, not user input`, matching `resolveOne`'s annotation, because gosec G304 flags a variable path.
6. Wrap every failure with `github.com/bborbe/errors` (`errors.Wrap` / `errors.Wrapf`), naming which lookup failed. Never `fmt.Errorf`, never a bare `return err`.
7. Do not call `time.Now()` anywhere. Timestamps come from git.

## 2. Extend the per-key rule with the newer side

Change the signature of `resolveScalarEntry` (added in prompt 1; it has exactly one caller):

```go
// resolveScalarEntry returns the replacement line for one key in one region.
// newerTheirs is consulted only when the counter rule does not apply; it reports
// whether the theirs side is strictly newer and whether that could be decided.
func resolveScalarEntry(
	key string,
	ours, theirs scalarEntry,
	newerTheirs func() (theirsNewer bool, ok bool),
) (line string, ok bool)
```

Behavior, in order:

1. If `isCounterKey(key)` and both `integerValue` calls succeed → return `key + ": " + strconv.FormatInt(max, 10)`, `ok=true`. This branch must NOT call `newerTheirs`.
2. Otherwise call `newerTheirs()`.
   - `ok=false` → return `"", false` (the whole file becomes ineligible).
   - `theirsNewer=true` → return `theirs.line`, `true` (the raw theirs line, verbatim — quoting and trailing comments preserved).
   - `theirsNewer=false` → return `ours.line`, `true` (tie goes to ours).

The counter rule therefore wins over recency: `trigger_count: 3` vs `trigger_count: 0` resolves to `3` whether ours or theirs is newer.

## 3. Wire the lazy timestamp lookup into `applyScalarFrontmatterRule`

In `applyScalarFrontmatterRule`, build one lazy closure per file and pass it to `resolveScalarEntry`:

```go
var (
	timesLoaded bool
	timesOK     bool
	theirsNewer bool
)
newerTheirs := func() (bool, bool) {
	if !timesLoaded {
		timesLoaded = true
		oursT, theirsT, err := r.sideTimes(ctx)
		if err != nil {
			glog.Warningf(
				"yaml-merge-resolver: %q merge-parent timestamps unavailable: %v",
				path,
				err,
			)
			timesOK = false
		} else {
			timesOK = true
			theirsNewer = theirsT > oursT
		}
	}
	return theirsNewer, timesOK
}
```

Rules:

- The getter is called at most once per file, and only when at least one key needs the newer side. A counter-only file must not touch git at all — this is what keeps prompt 1's counter specs passing in repos that are not mid-merge.
- Any `resolveScalarEntry` returning `ok=false` aborts before the write, so `applyScalarFrontmatterRule` returns `(false, nil)` and the caller quarantines with `no_frontmatter`. No partial write, ever.
- The existing write / `git add` / `write_failed` / `git_add_failed` behavior is unchanged.

## 4. Expose a stubbed constructor to the external test package

Create `pkg/git/export_test.go` (`package git`, test-only, with the repo's copyright header):

```go
// NewYAMLMergeResolverWithSideTimes exposes a resolver whose merge-parent
// committer timestamps are stubbed, so external tests can choose the newer side
// deterministically. Test builds only.
func NewYAMLMergeResolverWithSideTimes(
	repoPath string,
	m metrics.Metrics,
	ours, theirs int64,
) ConflictResolver {
	return &yamlMergeResolver{
		repoPath: repoPath,
		metrics:  m,
		sideTimes: func(context.Context) (int64, int64, error) {
			return ours, theirs, nil
		},
	}
}
```

Production code must not reference this symbol. `NewYAMLMergeResolver`'s signature does not change; `main.go` and `main_resolver_selection_test.go` stay untouched.

## 5. Tests

All new specs go inside the existing `Describe("Scalar frontmatter rule", …)` block in `pkg/git/yaml_merge_resolver_test.go` (`package git_test`). Prompt-1 specs stay byte-identical and must still pass.

1. **Per-key table (spec AC 2).** A Ginkgo `DescribeTable` driving `Resolve` on `setupHandWrittenConflict("note.md", …)` with `git.NewYAMLMergeResolverWithSideTimes(workDir, fakeMetrics, oursTime, theirsTime)`. Each row: one hand-written region placed inside the frontmatter with a closing `---` after it (`---\n<<<<<<< HEAD\n<ours line>\n=======\n<theirs line>\n>>>>>>> theirs\n---\nbody\n`, the same fixture shape as prompt 1's rejection table); assert the exact resolved line is present and no `<<<<<<<` line remains.

   | ours line | theirs line | oursTime | theirsTime | expected line |
   |---|---|---|---|---|
   | `trigger_count: 3` | `trigger_count: 0` | 10 | 5 | `trigger_count: 3` |
   | `trigger_count: 3` | `trigger_count: 0` | 5 | 10 | `trigger_count: 3` |
   | `retry_count: 1` | `retry_count: 5` | 10 | 5 | `retry_count: 5` |
   | `metrics_retries: 2` | `metrics_retries: 7` | 5 | 10 | `metrics_retries: 7` |
   | `assignee: alice` | `assignee: bob` | 5 | 10 | `assignee: bob` |
   | `assignee: alice` | `assignee: bob` | 10 | 5 | `assignee: alice` |
   | `assignee: alice` | `assignee: bob` | 10 | 10 | `assignee: alice` |
   | `attempt_count: 1` | `attempt_count: 9` | 5 | 10 | `attempt_count: 9` |
   | `trigger_count: "3"` | `trigger_count: 0` | 5 | 10 | `trigger_count: 0` |

   The last row is the non-integer-counter fallback: a quoted `"3"` carries tag `!!str`, so the counter rule does not apply and the newer side wins. The `attempt_count` row is an unlisted integer, so it takes the newer side too.

2. **Non-counter newer-side is proven through `Resolve` (spec AC 3).** One spec via `setupHandWrittenConflict` with the stubbed constructor, theirs newer, one region inside the frontmatter with a closing `---` after it, with a quoted value that also carries a trailing comment: ours `assignee: "alice"`, theirs `assignee: "bob" # newer`. Assert the written file contains the exact raw theirs line `assignee: "bob" # newer` and does not contain `assignee: "alice"`. This proves the verbatim re-emission, not a re-serialised value.

3. **The git-backed getter is exercised end-to-end (boundary test).** Use `setupScalarMergeRepo` with `oursDate = "2026-01-01T00:00:00Z"` and `theirsDate = "2026-01-02T00:00:00Z"`, base `---\nassignee: base\n---\nbody\n`, ours `---\nassignee: alice\n---\nbody\n`, theirs `---\nassignee: bob\n---\nbody\n` (all three are full file contents, per the helper docstring in prompt 1), and the default `git.NewYAMLMergeResolver(workDir, fakeMetrics)`. Assert `Resolve` succeeds and the file contains `assignee: bob`. This traverses the real `git rev-parse` / `git rev-parse --git-path` / `git show` path, not the stub. Add one variant of this case in a linked worktree (`git worktree add ../wt -b wt` inside the temp repo, then run `Resolve` from the worktree path) so the absolute `--git-path` result is exercised; if you decide to leave that branch untested, say so in a comment above it rather than leaving it silent.

4. **Timestamp-unavailable cases quarantine (spec Failure Modes).** Each uses a hand-written conflict with a non-counter key (`assignee: alice` vs `assignee: bob`) placed INSIDE the frontmatter with a closing `---` after the region (the same fixture shape as prompt 1's rejection table), so `planScalarRegions` succeeds and the getter is actually reached — a region outside the frontmatter would short-circuit to `no_frontmatter` before the timestamp lookup and pass for the wrong reason — and the default constructor, so the real getter runs:
   - no merge in progress (plain repo, no `.git/MERGE_HEAD`);
   - `.git/MERGE_HEAD` written with two real commit SHAs (e.g. `git rev-parse HEAD` twice) on separate lines (multi-valued — this is the case `git rev-parse MERGE_HEAD` cannot detect, so it must fail via the file read);
   - `.git/MERGE_HEAD` written with a 40-hex string that names no object (unreadable timestamp).
   For each: `errors.Is(err, git.ErrConflictResolutionFailed)` is true, `fakeMetrics.IncResolverFailureArgsForCall(0) == "no_frontmatter"`, and the file bytes equal the pre-call bytes.

5. **A counter-only file resolves without a merge parent.** A hand-written counter conflict (`trigger_count: 3` vs `0`) in a plain repo with no `.git/MERGE_HEAD` resolves successfully through the default constructor. This pins the lazy lookup: a counter-only file must never touch the timestamp getter.

6. **All-or-nothing survives the new rule.** Two regions: one counter conflict, one non-counter conflict whose timestamp cannot be read (plain repo, no merge parent). Assert `ErrConflictResolutionFailed`, one `no_frontmatter`, and unchanged file bytes.

## 6. Self-check before finishing

Run every command in `<verification>` from the repo root and confirm each result. Then walk each requirement: every table row resolves to the expected line, the quoted non-counter line is re-emitted verbatim, the real git-backed getter picks the newer parent, a missing/multi-valued/unreadable merge parent quarantines with the file untouched, a counter-only file resolves without any merge parent, and prompt-1 specs still pass unedited.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Extend `pkg/git/yaml_merge_resolver.go`; add only `pkg/git/export_test.go` as a new file. Do NOT change the `ConflictResolver` interface (`pkg/git/conflict_resolver.go`) or `mocks/conflict_resolver.go`; both stay byte-identical.
- Do NOT change `MarkerResolver`, the quarantine path, `pkg/git/git.go`, or the pull loop.
- `NewYAMLMergeResolver`'s signature does not change; `main.go` and `main_resolver_selection_test.go` stay untouched.
- The existing deep-merge path and its tests must not regress; prompt-1 specs stay byte-identical and must still pass.
- The conflict-failure sentinel (`ErrConflictResolutionFailed`) remains the only failure error; no new exported error types.
- Errors via `github.com/bborbe/errors` — no `fmt.Errorf`, no bare `return err`.
- Keep `glog` in `pkg/git/yaml_merge_resolver.go`; do not introduce `slog` there and do not mix loggers.
- Timestamps come from git, not `time.Now()`. No new dependency; `gopkg.in/yaml.v3` is already direct.
- Production code must not introduce `context.Background()`; tests may use it as the existing suite does.
- Do NOT add a metric in this prompt. `pkg/metrics/metrics.go` and `mocks/metrics.go` stay byte-identical here.
- Do NOT edit `CHANGELOG.md` or `docs/` in this prompt.
- Do NOT add per-repository or per-key configuration for the rule.
- Do NOT auto-resolve body conflicts, list items, block scalars, nested mappings, one-sided add/delete, or duplicate keys — those keep quarantining.
- The rule applies to markdown files only; non-markdown conflicts keep the current path.
- Do NOT add a scenario file under `scenarios/`.
- Do NOT run `go mod vendor`, and do NOT write `-mod=vendor` in any command — this repo does not commit `vendor/`.
- Every new `.go` file needs the repo's copyright header (`// Copyright (c) 2026 Benjamin Borbe All rights reserved.` / BSD-style licence lines), because `make precommit` runs `addlicense`.
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
go test ./pkg/git/... -mod=mod -ginkgo.focus='Scalar frontmatter rule'
```

The new pieces are present (each `grep -n` must print at least one match):

```bash
grep -n 'sideTimes' pkg/git/yaml_merge_resolver.go
grep -n 'func gitMergeSideTimes' pkg/git/yaml_merge_resolver.go
grep -n -- '--git-path' pkg/git/yaml_merge_resolver.go
grep -n 'theirsT > oursT' pkg/git/yaml_merge_resolver.go
grep -n 'NewYAMLMergeResolverWithSideTimes' pkg/git/export_test.go
! grep -q 'NewYAMLMergeResolverWithSideTimes' pkg/git/yaml_merge_resolver.go
grep -n 'func NewYAMLMergeResolver(' pkg/git/yaml_merge_resolver.go
grep -n 'func resolveScalarEntry' pkg/git/yaml_merge_resolver.go
grep -n 'NewYAMLMergeResolver(' main.go main_resolver_selection_test.go
```

Frozen surfaces and absence checks (each `! grep -q` must exit 0):

```bash
grep -n 'deepMergeTheirsWins' pkg/git/yaml_merge_resolver.go
grep -n 'ConflictResolver interface' pkg/git/conflict_resolver.go
ls mocks/conflict_resolver.go
! grep -q 'time.Now()' pkg/git/yaml_merge_resolver.go
! grep -q 'fmt.Errorf' pkg/git/yaml_merge_resolver.go
! grep -q '"log/slog"' pkg/git/yaml_merge_resolver.go
! grep -n 'MERGE_HEAD' pkg/git/yaml_merge_resolver.go | grep -v -q -- '--git-path'
! grep -q 'autoresolved' pkg/git/yaml_merge_resolver.go
! grep -q 'git_rest_autoresolved_total' pkg/metrics/metrics.go
! grep -q '^## Unreleased' CHANGELOG.md
```

The `rev-parse MERGE_HEAD` absence check is load-bearing: that command prints only the first SHA of a multi-valued merge head, so using it would silently accept a multi-valued file.

Absence is written as `! grep -q`, never as `grep -c` — `grep -c` prints `0` and exits non-zero, so an absence assertion written that way fails the step it was meant to pass.

Self-check before finishing: re-run `ROOTDIR=/workspace make precommit`, then walk each requirement above against the change — every table row resolves to the expected line, the quoted non-counter line is re-emitted verbatim, the git-backed getter picks the newer parent, every timestamp-unavailable case quarantines with an untouched file, and a counter-only file still resolves without a merge parent.
</verification>
