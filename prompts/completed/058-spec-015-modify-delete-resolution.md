---
status: completed
spec: [015-bug-pull-wedges-on-modify-delete-conflict]
summary: Taught the merge-conflict parser the modify/delete form, aborted the merge on the empty-conflict-list branch, and resolved ours-deleted modify/delete conflicts by taking the upstream version under both resolver configurations.
execution_id: git-rest-modify-delete-exec-058-spec-015-modify-delete-resolution
dark-factory-version: v0.196.0
created: "2026-10-07T18:52:08Z"
queued: "2026-10-07T19:36:29Z"
started: "2026-10-07T19:36:30Z"
completed: "2026-10-07T19:40:43Z"
branch: dark-factory/bug-pull-wedges-on-modify-delete-conflict
---

# Recognise a modify/delete conflict, resolve it by taking the upstream version, and never return mid-merge

<summary>
- A pull that meets a git modify/delete conflict no longer wedges the vault
- The conflicted path is recognised, where today the conflict list comes back empty and the resolver is never reached
- Such a conflict resolves by restoring the upstream version of the file, staged and included in the merge commit, which is then pushed
- The resolution holds under both resolver configurations — the marker resolver and the YAML frontmatter resolver
- A merge failure that yields no recognised conflict line now aborts the merge before returning, so the repository is never left mid-merge
- Content conflicts keep going to the configured resolver exactly as before, and still fall back to quarantine when the resolver fails
- Nothing is discarded: the resolution restores published upstream content, and the local side stays in the merge's history
- No metric, alert, interface or commit-message format changes
</summary>

<objective>
Make a `git merge` that produces `CONFLICT (modify/delete):` resolvable instead of wedging the puller: teach the conflict-path parser the modify/delete form, resolve such a conflict by taking the upstream version under both resolver configurations, and guarantee that after `Pull` returns the repository is never left mid-merge — including on the empty-conflict-list branch.
</objective>

<context>
Read `CLAUDE.md` at the repo root for project conventions.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors`; never `fmt.Errorf`, never `context.Background()` in `pkg/`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, external test packages, real `git` via `os/exec` against a temp working tree
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md` — `log/slog` for new code
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — linter limits (funlen 80, nestif 4, golines 100)

Files to read in full before editing (line numbers are hints; anchor by symbol name):

- `pkg/git/git.go` — `parseMergeConflictPaths` (~94), `parseRebaseConflictPath` (~81), `resolveConflictMerge` (~777), `resolveConflictPaths` (~807), `validateConflictPathsSafe` (~847), `resolveEachPath` (~880), `commitAndPushMerge` (~916), `ensureConflictsDir` (~954), `quarantineOne` (~1014), `nestedConflictPath` (~435), `validateConflictPathsNotNested` (~450), `unsafeConflictPath` (~413), `runCmd` (~196), `runCmdOutput` (~219), `runCmdRaw` (~244), `Pull` (~1222)
- `pkg/git/conflict_resolver.go` — the `ConflictResolver` interface (frozen) and `NewMarkerResolver`
- `pkg/git/yaml_merge_resolver.go` — `NewYAMLMergeResolver`, and `splitConflictSides` (the reason a marker-less file is rejected)
- `pkg/git/resolve_conflict_merge_test.go` — the internal `package git` test file the new parser and branch tests belong in
- `pkg/git/git_test.go` — the Ginkgo v2 + Gomega merge-level fixture patterns (`setupPullFixture`, `setupQuarantineFixturePaths`, `runGit`, `gitOutputStr`, `captureSlogLogs`, `gatherResolverFailure`); the `Pull nested quarantine guard` Describe is the closest precedent
- `specs/in-progress/015-bug-pull-wedges-on-modify-delete-conflict.md` — `## Problem`, `## Goal`, `## Acceptance Criteria`, `## Constraints`, `## Failure Modes`

**Verified facts about the current code (do not re-derive; do not contradict):**

- `func parseMergeConflictPaths(output string) []string` (unexported, `package git`) matches only the literal `"Merge conflict in "`. git emits that substring for `CONFLICT (content):` lines only.
- `func (g *git) resolveConflictMerge(ctx context.Context, upstream string, mergeOut []byte, mergeErr error) error` takes the `len(conflictPaths) == 0` branch and returns `errors.Wrapf(ctx, mergeErr, "merge %s: %s", upstream, ...)` **without** running `git merge --abort`, leaving `MERGE_HEAD` behind.
- `func (g *git) resolveConflictPaths(ctx context.Context, conflictPaths []string) error` runs, in this order: `validateConflictPathsSafe`, `validateConflictPathsNotNested`, `ensureConflictsDir`, then `resolved, quarantined := g.resolveEachPath(ctx, conflictPaths, ts)` with `ts := g.currentDateTimeGetter.Now().Unix()`, then `g.commitAndPushMerge(ctx, conflictPaths, resolved, quarantined)`. The ordering comment above it is load-bearing: both pre-flights MUST run before `ensureConflictsDir` so neither abort path creates `_conflicts/` as a side effect.
- `func (g *git) resolveEachPath(ctx context.Context, conflictPaths []string, ts int64) ([]string, []string)` loops, checks `ctx.Done()` non-blocking per iteration, calls `g.resolver.Resolve(ctx, []string{path})`, appends to `resolved` on nil, and on error logs `git-rest: resolver failed on path; attempting quarantine` then `if g.quarantineOne(ctx, path, ts) { quarantined = append(quarantined, path) }`.
- `commitAndPushMerge` sorts both lists, builds `"merge: resolved=[" + strings.Join(resolved, ",") + "] quarantined=[" + strings.Join(quarantined, ",") + "]"`, commits, then increments `IncMergeOutcome("resolved")` and `IncConflictPaths(len(conflictPaths))`, then pushes.
- `nestedConflictPath(paths []string) string` is a pure function (no receiver) that returns the first path equal to `conflictsDirName` (`"_conflicts"`) or prefixed `"_conflicts/"`. `TestNestedConflictPath` in `pkg/git/resolve_conflict_merge_test.go` calls it directly with plain string slices and no repository.
- `resolveConflictPaths` is called directly by the internal tests `TestResolveConflictPathsUnsafePath` and `TestResolveConflictPathsAllFailAbort` with the signature `repo.resolveConflictPaths(ctx, []string{...})` — **its signature must not change**.
- `New(repoPath string, m metrics.Metrics, currentDateTimeGetter libtime.CurrentDateTimeGetter, sshKeyPath SSHKeyPath, resolver ConflictResolver) Git` returns `Git`; the internal tests type-assert it to `*git`.
- `unsafeTestMetrics` (internal, `resolve_conflict_merge_test.go`) and `mocks.FakeMetrics` (external) both implement `metrics.Metrics`; the internal file cannot import `mocks` (import cycle).
- The YAML merge resolver calls `splitConflictSides` first and, when the file carries no `<<<<<<<` / `=======` / `>>>>>>>` region, increments `no_frontmatter` and returns `ErrConflictResolutionFailed` — so a marker-less conflicted file is quarantined by the caller, not restored.
- Verified live against git: for `CONFLICT (modify/delete): <path> deleted in HEAD and modified in <upstream>`, `git ls-files -u` prints exactly two entries — `<mode> <sha> 1\t<path>` and `<mode> <sha> 3\t<path>` — and git leaves the **upstream** version in the working tree with no conflict markers. `git add -- <path>` then resolves the conflict and stages the upstream content.
- Verified live: `git ls-files -u` exits 0 with empty output when nothing is unmerged, so it is safe to call on any repo.
- Verified live: `git merge --abort` fails when no merge is in progress. On the empty-conflict-list branch its error is therefore **logged, not returned** (spec 015 Failure Modes row 5: surfaced rather than silently swallowed) — the merge's own wrapped error is the one the operator needs.

**Design decision (spec DB2 vs AC5 — resolve it this way, do not re-litigate in code comments):**

Spec 015's Desired Behavior 2 says every parsed path flows through the existing pipeline including "the per-path resolver", while AC5's YAML-resolver run requires the modify/delete path **not** to be delegated to the configured resolver: a modify/delete conflict's in-tree version carries no conflict markers, so the YAML merge resolver rejects it and the path would be quarantined instead of restored. The design therefore resolves modify/delete conflicts at the pipeline level — inside the same per-path loop, after both pre-flights and after `ensureConflictsDir` — and keeps the configured resolver for content conflicts only. The `ConflictResolver` interface, `MarkerResolver`, `YAMLMergeResolver` and the `merge: resolved=[…] quarantined=[…]` commit-message format all stay unchanged. `validateConflictPathsNotNested` keeps its current signature in this prompt (prompt 2 narrows it).

**Sibling prompts (do not do their work):**

- Prompt 2 narrows the nesting guard to accept an upstream deletion of a `_conflicts/` path, adds the accepted-drain INFO log, and resolves the upstream-deletion kind. Do NOT touch `validateConflictPathsNotNested`, and do NOT handle the upstream-deletion kind in `resolveModifyDelete` here.
- Prompt 3 adds the empty-conflict-list regression test, a lock-in Describe and reconciles spec 013. Do NOT touch `specs/`, `CHANGELOG.md` or the lock-in specs.
- Prompt 4 documents the rung-1 recipe in `docs/verifying-specs.md`. Do NOT touch `docs/`.
</context>

<requirements>

## 1. Teach `parseMergeConflictPaths` the modify/delete form

In `pkg/git/git.go`, extend `parseMergeConflictPaths`. **Keep the signature verbatim**: `func parseMergeConflictPaths(output string) []string`. Keep the existing `seen` map de-duplication, the input order, and the skip-empty behaviour.

Recognise both forms git emits on a failed merge:

- **content form** — a line containing the literal `Merge conflict in `: the path is the trimmed remainder of the line, exactly as today.
- **modify/delete form** — a line containing the literal `CONFLICT (modify/delete): `: the path runs from immediately after that prefix up to the **first** occurrence of the literal ` deleted in `; if ` deleted in ` is absent, up to the first ` modified in `; then trimmed.

Both ref orders must work, because git swaps them depending on which side deleted the path:

```
CONFLICT (modify/delete): 25 Tasks/The Parity Harness Compares Generated UUIDs and Timestamps Raw.md deleted in HEAD and modified in origin/master.  Version origin/master of 25 Tasks/The Parity Harness Compares Generated UUIDs and Timestamps Raw.md left in tree.
CONFLICT (modify/delete): _conflicts/25 Tasks/Prev A.1791388434.md deleted in origin/main and modified in HEAD.  Version HEAD of _conflicts/25 Tasks/Prev A.1791388434.md left in tree.
```

A path may contain spaces, so it must never be split on whitespace. A line with no conflict of either form yields no path. Output containing no conflict line of either form returns a zero-length slice (never nil is not required, but the length must be 0).

Leave `parseRebaseConflictPath` untouched.

## 2. Add the internal parser tests (AC 1 and AC 2)

Add tests to `pkg/git/resolve_conflict_merge_test.go` (`package git`, standard `testing`, matching the file's existing style — table or subtest, `t.Fatalf` on mismatch). At minimum:

- The **verbatim incident line** from AC 1, asserted to return a slice of length 1 whose only element equals `25 Tasks/The Parity Harness Compares Generated UUIDs and Timestamps Raw.md`.
- The swapped-order line for `_conflicts/25 Tasks/Prev A.1791388434.md`, asserted to return exactly that path.
- The content form `CONFLICT (content): Merge conflict in a.md` still returns `["a.md"]` (AC 2, positive assertion on length and value).
- `fatal: refusing to merge unrelated histories` returns a slice of length 0 (AC 2, negative assertion).
- One output containing a content line **and** a modify/delete line returns both paths, and a duplicated path appears once.

This file must remain the internal `package git` test file — `parseMergeConflictPaths` is unexported.

Name the top-level test function `TestParseMergeConflictPaths`, so the `go test ./pkg/git/... -run 'TestParseMergeConflictPaths'` verification command selects it. Go's `-run` is an unanchored regex, so subtest names may be appended freely; but if the function is named anything else the pattern matches no tests, `go test` exits 0 with "no tests to run", and that check passes vacuously.

## 3. Abort the merge on the empty-conflict-list branch (AC 3, third clause)

In `resolveConflictMerge`, the `len(conflictPaths) == 0` branch must run `git merge --abort` before returning. Keep `g.metrics.IncGitOperationError("merge")` and add `g.metrics.IncMergeOutcome("aborted")`; then return the same wrapped `mergeErr` with the same `"merge %s: %s"` message. Capture the abort's own error rather than discarding it — spec 015's Failure Modes row 5 requires a failed abort to be surfaced, not silently swallowed: `if _, abortErr := g.runCmdRaw(ctx, g.repoPath, "merge", "--abort"); abortErr != nil { slog.WarnContext(ctx, "git-rest: git merge --abort failed; repository may still be mid-merge", "err", abortErr.Error()) }`. The abort is best-effort in the sense that its failure is **logged rather than returned**: `git merge --abort` legitimately fails when no merge is in progress (unrelated histories, an environmental failure), so the merge's own wrapped error remains the one the operator sees — but the operator must still be able to learn from the log that the abort failed.

This is the invariant: after `Pull` returns, on every path, the repository is never left mid-merge.

## 4. Classify conflicts from the unmerged index

In `pkg/git/git.go`, add:

```go
// conflictKind classifies a conflicted path by which sides of the merge carry
// content in the unmerged index.
type conflictKind int

const (
	// conflictKindContent: both sides changed the path (unmerged stages 1+2+3,
	// or 2+3 for an add/add). The configured ConflictResolver handles it.
	conflictKindContent conflictKind = iota
	// conflictKindOursDeleted: HEAD deleted the path and the upstream side
	// modified it (stages 1+3, no stage 2). git leaves the upstream version in
	// the working tree with no conflict markers.
	conflictKindOursDeleted
	// conflictKindTheirsDeleted: the upstream side deleted the path and HEAD
	// modified it (stages 1+2, no stage 3). This is the shape an operator drain
	// of _conflicts/ produces. git leaves the HEAD version in the working tree.
	conflictKindTheirsDeleted
)
```

and

```go
// classifyConflicts reads the unmerged index once and maps each conflicted
// repo-relative path to its conflictKind. A path absent from the returned map is
// treated as conflictKindContent by the caller, so the pre-existing
// resolver-then-quarantine path is the default.
func (g *git) classifyConflicts(ctx context.Context) map[string]conflictKind
```

Implementation contract:

- Run `git ls-files -u` once via `g.runCmdOutput(ctx, g.repoPath, "ls-files", "-u")`. On error, log one `slog.WarnContext` naming the failure and return an empty map — a failed read must never wedge a pull, and the empty map degrades to today's behaviour.
- Each output line is `<mode> <sha> <stage>\t<path>`. Split on the **first** tab (`strings.SplitN(line, "\t", 2)`); skip lines with no tab. `strings.Fields` on the part before the tab yields `[mode, sha, stage]`; parse `meta[2]` with `strconv.Atoi` (`strconv` is already imported) and skip entries whose stage is not 1, 2 or 3.
- Classify per path: stage 3 present and stage 2 absent → `conflictKindOursDeleted`; stage 2 present and stage 3 absent → `conflictKindTheirsDeleted`; otherwise → `conflictKindContent`.
- Read-only: this must create nothing on disk.

## 5. Add the take-the-upstream-version helper

In `pkg/git/git.go`, add:

```go
// stageUpstreamVersion resolves a modify/delete conflict in which HEAD deleted
// the path and the upstream side modified it, by taking the upstream version:
// `git checkout --theirs -- <path>` writes the upstream content into the working
// tree and `git add -- <path>` stages it, clearing the unmerged index entry.
// This is the automated form of the operator's manual repair
// (`git checkout origin/master -- <path>`).
func (g *git) stageUpstreamVersion(ctx context.Context, path string) error
```

Body: `g.runCmd(ctx, g.repoPath, "checkout", "--theirs", "--", path)` then `g.runCmd(ctx, g.repoPath, "add", "--", path)`, each wrapped with `errors.Wrapf(ctx, err, ...)` naming the operation and the path. Return nil only when both succeed. The `--` separator before the path is required (the path comes from git's own output and may contain spaces).

Note: `git checkout --theirs` alone does **not** clear the unmerged entry — the following `git add` is what stages the version and resolves the conflict. Both commands are needed.

## 6. Route modify/delete paths through the pipeline before the resolver

Add:

```go
// resolveModifyDelete resolves a modify/delete conflict at the pipeline level and
// reports whether it did. It is called before the configured resolver, because
// neither shipped resolver can resolve one: the marker resolver's git add happens
// to stage the right content for an ours-deleted path, but a marker-less file is
// rejected by the YAML merge resolver, which would quarantine the path instead of
// restoring it.
func (g *git) resolveModifyDelete(ctx context.Context, path string, kind conflictKind) bool
```

In this prompt it handles **only** `conflictKindOursDeleted`: call `g.stageUpstreamVersion(ctx, path)`; on nil, log one `slog.InfoContext` line naming the path and stating that the modify/delete conflict was resolved by taking the upstream version, and return true; on error, log one `slog.WarnContext` naming the path and the error and return false (the caller then falls through to the resolver and the quarantine fallback, so the pull still completes). `conflictKindContent` and `conflictKindTheirsDeleted` return false immediately — prompt 2 adds the upstream-deletion arm.

Then in `resolveConflictPaths`:

- Compute `kinds := g.classifyConflicts(ctx)` after `validateConflictPathsSafe` and before `validateConflictPathsNotNested` (read-only, so the ordering invariant that both pre-flights run before `ensureConflictsDir` is preserved). Leave `validateConflictPathsNotNested(ctx, conflictPaths)` unchanged.
- Pass `kinds` to `resolveEachPath`, changing it to `func (g *git) resolveEachPath(ctx context.Context, conflictPaths []string, kinds map[string]conflictKind, ts int64) ([]string, []string)`.

In `resolveEachPath`, keep the per-iteration non-blocking `ctx.Done()` check and the existing resolver-then-quarantine behaviour for content conflicts, and add the dispatch: before calling `g.resolver.Resolve`, call `g.resolveModifyDelete(ctx, path, kinds[path])`; when it returns true, append the path to `resolved` and `continue`. A path missing from `kinds` (including the crafted path lists the internal tests pass) must behave exactly as today — it goes to the resolver.

Do not add any metric. `commitAndPushMerge` already records `IncMergeOutcome("resolved")` and `IncConflictPaths(len(conflictPaths))` for this path.

## 7. Add the merge-level fixture-A specs (AC 3 first clause, AC 4, AC 5)

In `pkg/git/git_test.go` (`package git_test`, Ginkgo v2 + Gomega), add a new top-level `var _ = Describe("Modify/delete conflict resolution (spec 015)", func() { ... })` with `ctx = context.Background()` in its `BeforeEach`.

Add a fixture helper beside the existing ones:

```go
// setupModifyDeleteFixture seeds tasks/doomed.md at the merge base on a local
// bare remote, then returns closures that advance the remote by MODIFYING that
// path and delete it locally with `git rm` plus a commit — the shape the
// quarantine flow produces.
func setupModifyDeleteFixture() (
	workDir string,
	remoteEdit func(),
	localDelete func(),
	cleanup func(),
)
```

Use the existing fixture idioms: `os.MkdirTemp`, `exec.Command("git", ...)` with `cmd.Dir`, `Expect(err).NotTo(HaveOccurred())` (see `setupQuarantineFixturePaths` at ~1019 for the exact shape), `os.WriteFile(..., 0o644)`, a bare remote initialised with `git init --bare -b main`, seed `tasks/doomed.md` containing `line one\n`, push, then clone into the work repo. `remoteEdit()` clones the bare remote into a temp dir, writes `line one\nUPSTREAM EDIT\n` to `tasks/doomed.md`, commits and pushes. `localDelete()` runs `git rm -q -- tasks/doomed.md` and commits, so HEAD diverges from the merge base (a commit is required — without it the puller takes the fast-forward path and never merges).

Specs (each spec gets a fresh fixture via `BeforeEach` + `DeferCleanup`):

1. **Marker resolver run.** Construct `git.New(workDir, &mocks.FakeMetrics{}, libtime.NewCurrentDateTime(), "", git.NewMarkerResolver(workDir))` and assert `pg.Pull(ctx)` returns nil, then assert all of AC 4:
   - `os.Stat(filepath.Join(workDir, ".git", "MERGE_HEAD"))` satisfies `os.IsNotExist` (do **not** run `git rev-parse --verify MERGE_HEAD`: `gitOutputStr` panics on a non-zero exit, and the absence case exits non-zero);
   - `strings.TrimSpace(gitOutputStr(workDir, "status", "--porcelain"))` is empty;
   - `gitOutputStr(workDir, "show", "HEAD:tasks/doomed.md")` contains `UPSTREAM EDIT`;
   - `strings.Split(strings.TrimSpace(gitOutputStr(workDir, "ls-tree", "-r", "--name-only", "HEAD")), "\n")` contains exactly `tasks/doomed.md` (assert with `ContainElement` and count occurrences as lines — never `strings.Count` over the whole output);
   - `strings.TrimSpace(gitOutputStr(workDir, "log", "-1", "--format=%s"))` matches `^merge: resolved=\[tasks/doomed\.md\] quarantined=\[\]$`;
   - `strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "HEAD"))` equals `strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "origin/main"))`.
2. **YAML resolver run (the discriminating one).** Same assertions, with `git.NewYAMLMergeResolver(workDir, metrics.NewMetrics())` as the resolver and `metrics.NewMetrics()` as the metrics implementation. This is the run a naive implementation fails: the version git leaves in the tree carries no conflict markers, so a resolution that delegates to the YAML merge resolver quarantines the path instead of restoring it.
3. **AC 5 — the restored path is never quarantined, in both runs.** In each of the two specs above, additionally assert that the `_conflicts/` tree holds zero regular files. Add a small local helper `countFilesUnder(dir string) int` to `git_test.go` that walks `dir` with `filepath.WalkDir` (returning 0 when `dir` does not exist) and counts non-directory entries; add the `io/fs` import if it is not already present. Assert `countFilesUnder(filepath.Join(workDir, "_conflicts"))` equals 0. (An empty `_conflicts/` directory is expected — `ensureConflictsDir` creates it; it is the file count that must be 0.)

Do not modify any existing spec or helper in `git_test.go`; only add.

## 8. Self-check before finishing

Run every command in `<verification>` from the repo root and confirm each result. Then walk each requirement above against the change: the parser returns the incident path and still returns `["a.md"]` for a content line and nothing for a non-conflict line; the empty-conflict-list branch aborts; a modify/delete conflict resolves to the upstream content with `resolved=[tasks/doomed.md] quarantined=[]` and nothing under `_conflicts/` under both resolvers; content conflicts still reach the resolver; and no `_conflicts/` entry or second `_conflicts/` level is created.

End your final message with the standard dark-factory completion report (the template dark-factory appends to this prompt), then stop. Report `"status":"success"` only if `make precommit` exited 0; report `"partial"` if the code works but `make precommit` failed on an unrelated pre-existing issue; report `"failed"` otherwise. Include the verification command and its exit code.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT change the `ConflictResolver` interface (`pkg/git/conflict_resolver.go`), its counterfeiter directive, `MarkerResolver` or `YAMLMergeResolver`.
- Do NOT change the `merge: resolved=[…] quarantined=[…]` commit-message format established by spec 012.
- Do NOT change `validateConflictPathsNotNested`, `nestedConflictPath`, `validateConflictPathsSafe`, `ensureConflictsDir`, `quarantineOne` or `commitAndPushMerge` — prompt 2 owns the nesting guard.
- Do NOT change the signature of `parseMergeConflictPaths` or `resolveConflictPaths`; the internal tests call the latter directly.
- Do NOT change the public HTTP contract in `docs/api.md`, and do NOT touch `docs/` — prompt 4 owns the docs.
- Do NOT add a metric, a gauge, an alert or a counter. `pkg/metrics/metrics.go` and `mocks/metrics.go` must stay byte-identical. No metric may be added outside the `git_rest_` prefix.
- Do NOT touch `specs/` or `CHANGELOG.md` — prompts 3 and 4 own them.
- Do NOT change the `status: next` vs `in_progress` write race, and do NOT add a drain mechanism of git-rest's own.
- Preserve the repo's documented invariants: errors wrapped with `github.com/bborbe/errors` (never `fmt.Errorf`), no `context.Background()` in `pkg/`, `log/slog` for logging, timestamps from the injected `libtime` getter rather than `time.Now()`, and counters pre-initialised in `init()`.
- Parser tests are internal: `parseMergeConflictPaths` is unexported, so its cases live in `pkg/git/resolve_conflict_merge_test.go` (`package git`) and call the helper directly. The merge-level fixtures live in `pkg/git/git_test.go` (Ginkgo v2 + Gomega, `package git_test`) and extend, never replace, the existing specs.
- The merge-level fixtures use real `git` via `os/exec` against a temp working tree with no network.
- `resolveEachPath` must keep its per-iteration non-blocking `ctx.Done()` check (see `go-context-cancellation-in-loops.md`).
- Existing tests must still pass, including every spec in the `Pull state machine`, `Entry-state recovery`, `Pull nested quarantine guard`, `Quarantine backlog gauge` and `Dirty working tree rescue` Describe blocks, and the internal tests in `pkg/git/resolve_conflict_merge_test.go`.
- Do NOT add a scenario file under `scenarios/`.
- Build via `make precommit` from the repo root.
</constraints>

<verification>
Run from the repo root. All commands are container-executable — no `git`, no `docker`, no cluster commands (the daemon runs with `hideGit=true`, so an in-container `git log` prints 0 lines for the wrong reason and is indistinguishable from a passing absence check).

```bash
make precommit
```

Must exit 0.

Iterative runs while implementing (use these after each meaningful change, before the final `make precommit`):

```bash
make test
go test ./pkg/git/... -run 'TestParseMergeConflictPaths'
go test ./pkg/git/... -ginkgo.focus='Modify/delete conflict resolution'
```

All pass. A focused run that reports `0 of 0` specs means the focus string matched nothing — fix the focus, do not accept it.

Evidence that the change landed (each must print at least one match, and each exits 0 on a match):

```bash
grep -rn 'modify/delete' pkg/git/
grep -rl 'parseMergeConflictPaths' pkg/git/*_test.go
grep -n 'classifyConflicts' pkg/git/git.go
grep -n 'stageUpstreamVersion' pkg/git/git.go
grep -n 'resolveModifyDelete' pkg/git/git.go
grep -A6 'if len(conflictPaths) == 0 {' pkg/git/git.go | grep -q 'merge", "--abort"'
```

The second grep is deliberately scoped to `*_test.go`: unscoped (`pkg/git/`) it matches only the function's own definition and would pass before any change. The last grep is scoped to the `len(conflictPaths) == 0` branch for the same reason: an unscoped `grep -n 'merge", "--abort"' pkg/git/git.go` already matches 8 pre-existing sites and would pass before any change.

Frozen-surface checks (each must print at least one match, and each exits 0 on a match):

```bash
grep -n 'Resolve(ctx context.Context, conflictedPaths \[\]string) error' pkg/git/conflict_resolver.go
grep -n 'counterfeiter:generate -o ../../mocks/conflict_resolver.go' pkg/git/conflict_resolver.go
grep -n 'merge: resolved=' pkg/git/git.go
```

No-new-metric check — `pkg/metrics` and its mock must be untouched. The two `!` probes assert the absence of a new series; write absence as `! grep -q`, never as `grep -c` (which prints `0` and exits non-zero):

```bash
grep -n 'git_rest_quarantined_backlog' pkg/metrics/metrics.go
grep -n 'nested_source' pkg/metrics/metrics.go
! grep -q 'git_rest_modify' pkg/metrics/metrics.go
! grep -q 'git_rest_drain' pkg/metrics/metrics.go
ls mocks/metrics.go mocks/conflict_resolver.go
```

Both `grep -n` probes must print at least one match, both `! grep -q` probes must exit 0, and the `ls` must list both files.

Out-of-scope checks:

```bash
! grep -q '^## Unreleased' CHANGELOG.md
ls docs/
```

The first must exit 0 — prompt 3 creates the `## Unreleased` section and this prompt must not. If it exits 1, a sibling prompt already ran and this prompt must not touch the changelog. The `ls` must list exactly `api.md`, `deployment.md`, `dod.md`, `verifying-specs.md`.

Self-check before finishing: re-run `make precommit`, then walk each requirement above against the change — the parser handles both forms and leaves the content and no-conflict cases unchanged, the empty-conflict-list branch aborts, the modify/delete conflict resolves to the upstream version with the fixed commit message under both resolvers, and nothing is quarantined.
</verification>
