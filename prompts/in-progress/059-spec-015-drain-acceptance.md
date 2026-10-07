---
status: approved
spec: [015-bug-pull-wedges-on-modify-delete-conflict]
created: "2026-10-07T18:52:08Z"
queued: "2026-10-07T19:36:29Z"
branch: dark-factory/bug-pull-wedges-on-modify-delete-conflict
---

# Accept an upstream drain of a quarantined path, and still refuse a re-quarantine

<summary>
- A `_conflicts/` entry the operator has drained upstream no longer wedges the replica
- The pull completes, the drained path leaves the tree, and readiness returns to ready within one pull interval
- The operator's own commit is the authority: the upstream deletion is accepted, not re-quarantined
- Nothing is discarded — the replica's local commit is a parent of the merge, so its content stays reachable in history
- The drained file counts down on the existing quarantine backlog gauge, with no new metric
- An accepted drain is visible in the pod log as one INFO line naming the path
- A genuine re-quarantine — a `_conflicts/` path both sides changed — is still refused exactly as before, with no second `_conflicts/` level
- The nesting guard's ordering property is preserved: both pre-flights still run before the quarantine directory is created
</summary>

<objective>
Make the puller accept an operator's upstream drain of a `_conflicts/` entry: a conflicted `_conflicts/` path whose upstream change is a deletion is resolved by accepting the deletion (removed from the tree, merge committed and pushed) and logged at INFO, while a `_conflicts/` path with no upstream deletion is still refused by the nesting guard and the merge still aborts cleanly.
</objective>

<context>
Read `CLAUDE.md` at the repo root for project conventions.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors`; never `fmt.Errorf`, never `context.Background()` in `pkg/`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, external test packages, real `git` via `os/exec` against a temp working tree
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md` — `log/slog`; INFO for the accepted drain, WARN for the refusal
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — linter limits (funlen 80, nestif 4, golines 100)

Files to read in full before editing (line numbers are hints; anchor by symbol name):

- `pkg/git/git.go` — `nestedConflictPath` (~435), `validateConflictPathsNotNested` (~450), `resolveConflictPaths` (~807), `resolveEachPath` (~880), `resolveModifyDelete` and `classifyConflicts` and `conflictKind` and `stageUpstreamVersion` (all added by prompt 1), `refreshQuarantinedBacklog` (~387), `Pull` (~1222)
- `pkg/git/git_test.go` — `setupQuarantineFixturePaths` (~1019), the `Pull nested quarantine guard` Describe (~1937) and its two Contexts, `captureSlogLogs` (~1607), `gatherResolverFailure` (~995), `hasResolverFailureSeries` (~973), `gatherQuarantinedBacklog` (~953), `runGit` (~29), `gitOutputStr` (~833), `countFilesUnder` (added by prompt 1)
- `pkg/metrics/metrics.go` — `ResolverFailuresTotal` and its `nested_source` pre-initialisation, `QuarantinedBacklog`
- `specs/in-progress/015-bug-pull-wedges-on-modify-delete-conflict.md` — `## Problem` (second defect), `## Goal`, `## Desired Behavior` 4, 5, 7, `## Acceptance Criteria` 6, 7, 8, `## Constraints`, `## Failure Modes`
- `specs/in-progress/013-quarantine-nesting-and-drain.md` — `## Desired Behavior` 2 and 3 and `## Non-goals` (the rule this prompt narrows; prompt 3 updates its text, this prompt does NOT touch the spec)

**Preconditions from prompt 1 (already on this branch — do not re-implement):**

- `parseMergeConflictPaths` recognises the `CONFLICT (modify/delete):` form in both ref orders.
- `resolveConflictMerge` aborts the merge on the empty-conflict-list branch before returning.
- `type conflictKind int` with `conflictKindContent`, `conflictKindOursDeleted`, `conflictKindTheirsDeleted`.
- `func (g *git) classifyConflicts(ctx context.Context) map[string]conflictKind` reads the unmerged index once with `git ls-files -u` and classifies per path.
- `func (g *git) stageUpstreamVersion(ctx context.Context, path string) error` resolves an ours-deleted modify/delete conflict.
- `func (g *git) resolveModifyDelete(ctx context.Context, path string, kind conflictKind) bool` handles **only** `conflictKindOursDeleted` today; it returns false for `conflictKindTheirsDeleted`.
- `func (g *git) resolveEachPath(ctx context.Context, conflictPaths []string, kinds map[string]conflictKind, ts int64) ([]string, []string)` calls `resolveModifyDelete` before `g.resolver.Resolve`.
- `resolveConflictPaths` computes `kinds := g.classifyConflicts(ctx)` after `validateConflictPathsSafe` and before `validateConflictPathsNotNested`, and still calls `g.validateConflictPathsNotNested(ctx, conflictPaths)` — the guard's signature is unchanged so far.

**Verified facts (do not re-derive; do not contradict):**

- `validateConflictPathsNotNested` currently reads: `path := nestedConflictPath(conflictPaths)`; if empty, return nil; otherwise `g.metrics.IncResolverFailure(quarantineFailureNested)` (the const is `"nested_source"`), one `slog.WarnContext` with message `git-rest: nested conflicted path already under _conflicts/ rejected; aborting merge`, attributes `path` and `reason`, reason text `path already under _conflicts/; re-quarantining would nest the tree one level deeper`, then `_, _ = g.runCmdRaw(ctx, g.repoPath, "merge", "--abort")`, `g.metrics.IncMergeOutcome("aborted")`, and `errors.Wrap(ctx, ErrConflictResolutionFailed, "nested conflict path already quarantined")`.
- `nestedConflictPath(paths []string) string` is a pure function with no receiver; `TestNestedConflictPath` in `pkg/git/resolve_conflict_merge_test.go` calls it directly with plain string slices and no repository. Its signature and behaviour must not change.
- Verified live against git, for the drain shape (upstream deleted a `_conflicts/` path, HEAD modified it): the merge reports `CONFLICT (modify/delete): <path> deleted in origin/main and modified in HEAD.  Version HEAD of <path> left in tree.`; `git status --porcelain` prints `UD "<path>"`; `git ls-files -u` prints exactly stages 1 and 2 for the path (no stage 3) — which `classifyConflicts` maps to `conflictKindTheirsDeleted`; the working tree holds the HEAD version; and `git rm -f -- <path>` stages the deletion and clears the unmerged entry, after which `git commit` produces `merge: resolved=[<path>] quarantined=[]` and the path is absent from `git ls-tree -r --name-only HEAD`.
- Verified live: `git merge-base --is-ancestor <localSHA> HEAD` exits 0 after the drain resolution — the local commit is the merge's first parent, so the pre-drain content stays reachable. This is the no-content-discarded assertion.
- `Pull` defers `g.refreshQuarantinedBacklog(ctx)` after the mutex is taken, so the gauge is refreshed on every exit path of a pull cycle. Once the drained path leaves `_conflicts/`, the gauge reads 0 with no code change.
- The existing `Pull nested quarantine guard` Describe already asserts the re-quarantine refusal for a `_conflicts/25 Tasks/Prev A.md` content conflict (remote side invalid YAML, local side valid frontmatter) — `errors.Is(err, ErrConflictResolutionFailed)`, `nested_source` delta 1, `_conflicts/_conflicts` absent, source content/mode untouched, and one WARN containing `nested` and the path. Do NOT modify or rewrite those specs; this prompt adds an explicit fixture-C spec that asserts the AC 8 probe set in one place, including `git status --porcelain` being empty.

**Sibling prompts (do not do their work):**

- Prompt 1 shipped the parser, the empty-conflict-list abort, `classifyConflicts`, `conflictKind`, `stageUpstreamVersion`, the ours-deleted arm of `resolveModifyDelete` and the fixture-A specs. Do NOT change any of them, and do NOT touch `parseMergeConflictPaths`.
- Prompt 3 adds the empty-conflict-list regression test, the lock-in Describe, the spec 013 text amendment and the `CHANGELOG.md` entry. Do NOT touch `specs/`, `CHANGELOG.md` or the lock-in specs.
- Prompt 4 documents the rung-1 recipe in `docs/verifying-specs.md`. Do NOT touch `docs/`.
</context>

<requirements>

## 1. Accept the upstream deletion at the pipeline level

In `pkg/git/git.go`, add:

```go
// acceptUpstreamDeletion resolves a modify/delete conflict in which the upstream
// side deleted the path and HEAD modified it — the shape an operator drain of
// _conflicts/ produces — by accepting the deletion: `git rm -f -- <path>` removes
// the path from the index and the working tree, clearing the unmerged entry so the
// merge can be committed. -f is required because the working-tree copy may differ
// from HEAD's; nothing is lost, because the HEAD commit is the merge's first
// parent and the pre-drain content therefore stays reachable in history.
func (g *git) acceptUpstreamDeletion(ctx context.Context, path string) error
```

Body: `g.runCmd(ctx, g.repoPath, "rm", "-f", "--", path)`, wrapped with `errors.Wrapf(ctx, err, ...)` naming the operation and the path. Return nil only on success.

Then extend `resolveModifyDelete` with the `conflictKindTheirsDeleted` arm, **bounded to quarantined paths**: when `kinds[path]` is `conflictKindTheirsDeleted` **and** the path is under `_conflicts/`, call `g.acceptUpstreamDeletion(ctx, path)`; on nil, log **exactly one** `slog.InfoContext` line and return true; on error, log one `slog.WarnContext` naming the path and the error and return false (so the caller falls through to the resolver and the quarantine fallback and the pull still completes). A `conflictKindTheirsDeleted` path that is **not** under `_conflicts/` returns false and falls through to the configured resolver, exactly as prompt 1 left it — spec 015's Security / Abuse Cases bounds the drain deletion to `_conflicts/`, and Desired Behavior 4 / AC 6 define the behaviour only for a quarantined path.

Factor the "under `_conflicts/`" rule into one shared predicate so the guard and the resolution arm cannot drift:

```go
// isUnderConflictsDir reports whether path lives in the quarantine directory:
// the path equals conflictsDirName or has the prefix conflictsDirName + "/".
func isUnderConflictsDir(path string) bool
```

Call it from both `dropAcceptedDrains` (requirement 2) and this arm.

The INFO line is an acceptance criterion (AC 7 / DB 7), so its shape is fixed: message `git-rest: accepted upstream deletion of quarantined path`, attributes `path` = the conflicted path and `reason` = `upstream deletion`. The rendered record must name `_conflicts/25 Tasks/Prev A.1791388434.md` and contain the literal substring `upstream deletion`. Emit it once per accepted drain — not once per pull cycle, not once per guard evaluation.

Leave the `conflictKindOursDeleted` arm and the `conflictKindContent` default exactly as prompt 1 left them.

## 2. Narrow the nesting guard to skip accepted drains

In `pkg/git/git.go`, change `validateConflictPathsNotNested` to:

```go
func (g *git) validateConflictPathsNotNested(
	ctx context.Context,
	conflictPaths []string,
	kinds map[string]conflictKind,
) error
```

and add:

```go
// dropAcceptedDrains returns paths minus the ones that are an operator drain: a
// path under _conflicts/ whose upstream change is a deletion
// (conflictKindTheirsDeleted). Those are resolved by accepting the deletion, so
// the nesting guard must not reject them.
func dropAcceptedDrains(paths []string, kinds map[string]conflictKind) []string
```

Contract:

- `dropAcceptedDrains` keeps a path unless it is both under `_conflicts/` **and** `kinds[path] == conflictKindTheirsDeleted`. Determine "under `_conflicts/`" by calling the shared `isUnderConflictsDir(path)` predicate (also used by the `resolveModifyDelete` arm in requirement 1). Do not re-state the rule inline — the guard and the resolution arm must not be able to drift apart.
- `validateConflictPathsNotNested` becomes `path := nestedConflictPath(dropAcceptedDrains(conflictPaths, kinds))`; everything else in the function stays byte-identical — the `nested_source` increment, the WARN message, the `path` and `reason` attributes, the `git merge --abort`, `IncMergeOutcome("aborted")`, and the wrapped `ErrConflictResolutionFailed` with message `nested conflict path already quarantined`.
- Do NOT change `nestedConflictPath` or `TestNestedConflictPath`.
- A `_conflicts/` path with kind `conflictKindContent` (a genuine re-quarantine) is still rejected. A `_conflicts/` path absent from `kinds` is treated as content and still rejected, preserving today's behaviour for every caller that passes no classification.
- Keep the guard's ordering property: it still runs before `ensureConflictsDir`, and it still performs no file writes, so neither abort path creates `_conflicts/` as a side effect.

Then in `resolveConflictPaths`, pass the already-computed map: `g.validateConflictPathsNotNested(ctx, conflictPaths, kinds)`. Do not move the `classifyConflicts` call — it must stay after `validateConflictPathsSafe` and before the guard.

## 3. Merge-level fixture for the drain and the refusal (AC 6, AC 7, AC 8)

In `pkg/git/git_test.go` (`package git_test`, Ginkgo v2 + Gomega), add a new top-level `var _ = Describe("Upstream drain of a quarantined path (spec 015)", func() { ... })` with `ctx = context.Background()` in its `BeforeEach`. Do not modify any existing spec or helper.

Add one fixture helper beside the existing ones:

```go
// setupQuarantinePathFixture seeds the given _conflicts/ path at the merge base on
// a local bare remote, then returns closures that advance the remote by deleting
// the path (the operator's drain) or by modifying it, that commit a local change
// to the same path and return the resulting local SHA, and a cleanup func.
func setupQuarantinePathFixture(
	path string,
) (
	workDir string,
	remoteDelete func(),
	remoteModify func(content string),
	localEdit func(content string) string,
	cleanup func(),
)
```

Follow `setupQuarantineFixturePaths` (~1019) for the idioms: `os.MkdirTemp`, `exec.Command("git", ...)` with `cmd.Dir`, `Expect(err).NotTo(HaveOccurred(), ...)`, `os.MkdirAll(filepath.Dir(abs), 0o750)`, `os.WriteFile(abs, ..., 0o644)`, a bare remote initialised with `git init --bare -b main`, push, then clone into the work repo. Seed the path with `---\ntitle: a\n---\nbody\n`. `remoteDelete()` clones the bare remote into a temp dir, runs `git rm -q -- <path>`, commits `operator drains the quarantine` and pushes. `remoteModify(content)` clones, writes `content` to the path, commits and pushes. `localEdit(content)` writes `content` to the path in the work repo, runs `git commit -q -am ...`, and returns `strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "HEAD"))`.

Use the const path `_conflicts/25 Tasks/Prev A.1791388434.md` in the specs.

Spec 1 — **the drain is accepted** (AC 6, AC 7, and AC 3's probe pair). Fixture: `remoteDelete()` then `localEdit("---\ntitle: a\n---\nLOCAL REPLICA EDIT\n")`, capturing the returned local SHA. Construct the puller with `git.New(workDir, metrics.NewMetrics(), libtime.NewCurrentDateTime(), "", git.NewMarkerResolver(workDir))` — the marker resolver is the deployed default for the puller, and the reproduction fixture runs with it. Wrap the pull in `captureSlogLogs()` and assert, after `pg.Pull(ctx)` returns nil:

- `os.Stat(filepath.Join(workDir, ".git", "MERGE_HEAD"))` satisfies `os.IsNotExist`, and `strings.TrimSpace(gitOutputStr(workDir, "status", "--porcelain"))` is empty (AC 3's probe pair for the drain shape);
- `os.Stat(filepath.Join(workDir, "_conflicts", "25 Tasks", "Prev A.1791388434.md"))` satisfies `os.IsNotExist`;
- `strings.Split(strings.TrimSpace(gitOutputStr(workDir, "ls-tree", "-r", "--name-only", "HEAD")), "\n")` contains no element containing `Prev A.1791388434` (assert with `NotTo(ContainElement(ContainSubstring(...)))` over the line slice — never `strings.Count` over the whole output);
- `strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "HEAD"))` equals `strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "origin/main"))`;
- the pull succeeded (`err` nil) — the in-process equivalent of the fixture's readiness-200 probe; and `gatherQuarantinedBacklog()` equals 0 (the gauge is refreshed by `Pull`'s defer, so this is the same evidence as the reproduction's `git_rest_quarantined_backlog 0` line);
- `countFilesUnder(filepath.Join(workDir, "_conflicts"))` equals 0;
- the no-content-discarded probe: `gitOutputStr(workDir, "merge-base", "--is-ancestor", localSHA, "HEAD")` does not panic (it exits 0), i.e. the local commit is an ancestor of the merge;
- AC 7: `logs.String()` contains one record with `level=INFO` naming `_conflicts/25 Tasks/Prev A.1791388434.md` and containing `upstream deletion`. Assert `strings.Count(logs.String(), "upstream deletion")` equals 1 so a second, duplicate line fails.

Spec 2 — **the drain does not trip the nesting guard** (AC 8, first half). Same fixture as spec 1. Read `before := gatherResolverFailure("nested_source")` immediately before the pull and assert `gatherResolverFailure("nested_source") - before` equals 0 after it. This is the metric delta the AC names; a delta, not an absolute value, because the counter lives in the process-global registry and sibling specs increment it too.

Spec 3 — **a genuine re-quarantine is still refused** (AC 8, second half, fixture C). Fixture: `remoteModify("---\ntitle: [unclosed\n---\nREMOTE CHANGE\n")` then `localEdit("---\ntitle: a\n---\nLOCAL CHANGE\n")`. Construct the puller with `git.NewYAMLMergeResolver(workDir, metrics.NewMetrics())` — the deployed configuration for the vault-write pods, and the configuration the incident was observed under. Assert:

- `pg.Pull(ctx)` returns a non-nil error satisfying `errors.Is(err, git.ErrConflictResolutionFailed)`;
- `gatherResolverFailure("nested_source")` increases by exactly 1 relative to a `before` read taken immediately before the pull;
- `os.Stat(filepath.Join(workDir, "_conflicts", "_conflicts"))` satisfies `os.IsNotExist`;
- `os.Stat(filepath.Join(workDir, ".git", "MERGE_HEAD"))` satisfies `os.IsNotExist` — the abort concluded the merge, so the repository is not left mid-merge. This is AC 3's **second clause**, the `MERGE_HEAD` half; it is asserted here because no other prompt in the set covers it (prompt 3's lock-in Describe asserts only the `status --porcelain` half for its own shapes).
- `strings.TrimSpace(gitOutputStr(workDir, "status", "--porcelain"))` is empty (the abort restored the worktree);
- `hasResolverFailureSeries("nested_source")` is true before the pull (the category is pre-initialised in `init()`, not merely valued 0);
- `captureSlogLogs()` output contains `level=WARN`, the substring `nested`, and the path.

## 4. Self-check before finishing

Run every command in `<verification>` from the repo root and confirm each result. Then walk each requirement above against the change: an upstream deletion of a `_conflicts/` path is resolved by deleting the path, the merge commits and pushes, the gauge drops to 0, one INFO line names the path and `upstream deletion`, `nested_source` does not move, the local commit stays an ancestor of the merge, and a `_conflicts/` content conflict is still refused with `nested_source` +1, no `_conflicts/_conflicts`, and a clean worktree.

End your final message with the standard dark-factory completion report (the template dark-factory appends to this prompt), then stop. Report `"status":"success"` only if `make precommit` exited 0; report `"partial"` if the code works but `make precommit` failed on an unrelated pre-existing issue; report `"failed"` otherwise. Include the verification command and its exit code.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT change the `ConflictResolver` interface (`pkg/git/conflict_resolver.go`), its counterfeiter directive, `MarkerResolver` or `YAMLMergeResolver`.
- Do NOT change the `merge: resolved=[…] quarantined=[…]` commit-message format established by spec 012.
- Do NOT change `nestedConflictPath` (or its test), `validateConflictPathsSafe`, `ensureConflictsDir`, `quarantineOne`, `commitAndPushMerge`, `parseMergeConflictPaths` or the `conflictKindOursDeleted` arm of `resolveModifyDelete`.
- Do NOT change the guard's rejection surface: the `nested_source` increment, the WARN message text, the `path` / `reason` attributes, the `git merge --abort`, the `aborted` merge outcome and the wrapped `ErrConflictResolutionFailed` with message `nested conflict path already quarantined` all stay exactly as they are. Only *which* paths reach the rejection changes.
- Do NOT reorder the pre-flights or move the `classifyConflicts` call: `validateConflictPathsSafe` → `classifyConflicts` → `validateConflictPathsNotNested` → `ensureConflictsDir` → per-path resolution. Both pre-flights MUST run before `ensureConflictsDir` so neither abort path creates `_conflicts/` as a side effect.
- **No nesting, ever.** No path is quarantined twice and the tree never gains a second `_conflicts/` level. A `_conflicts/` path with no upstream deletion is refused rather than re-quarantined.
- Spec 013's no-auto-delete rule stays: quarantined content is never deleted automatically outside an operator drain. The drain is the operator's own recorded commit on the remote; the local pre-drain content stays reachable in git history. Do NOT add any drain mechanism of git-rest's own.
- Do NOT add a metric, a gauge, an alert or a counter. `pkg/metrics/metrics.go` and `mocks/metrics.go` must stay byte-identical. No metric may be added outside the `git_rest_` prefix.
- Do NOT change the public HTTP contract in `docs/api.md`; readiness returning to ready is the observable, not a redefined contract. Do NOT touch `docs/`.
- Do NOT touch `specs/` or `CHANGELOG.md` — prompt 3 owns the spec 013 amendment and the changelog entry.
- Preserve the repo's documented invariants: errors wrapped with `github.com/bborbe/errors` (never `fmt.Errorf`), no `context.Background()` in `pkg/`, `log/slog` for logging, timestamps from the injected `libtime` getter rather than `time.Now()`, and counters pre-initialised in `init()`.
- The drain path performs a deletion, and it is bounded: it applies only to a path under `_conflicts/`, only when the upstream side deleted that same path in an operator-visible commit, and the path still passes the existing containment pre-flight so a traversal path cannot reach it.
- Merge-level specs live in `pkg/git/git_test.go` (Ginkgo v2 + Gomega, `package git_test`) and use real `git` via `os/exec` against a temp working tree with no network. They extend the existing specs; every existing `It` stays byte-identical.
- The `_conflicts/` and counter assertions MUST be deltas or file-absence checks, never absolute values **for the counter**: `nested_source` accumulates and lives in the process-global registry shared by every spec in that test binary. The **gauge** (`gatherQuarantinedBacklog`) is safe to read absolutely immediately after a pull, because `Pull` *sets* it to the served repo's live count on every exit path — an absolute `equals 0` there is correct, not a violation of this rule.
- Existing tests must still pass, including every spec in the `Pull nested quarantine guard` and `Quarantine backlog gauge` Describe blocks.
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
go test ./pkg/git/... -ginkgo.focus='Upstream drain of a quarantined path'
go test ./pkg/git/... -ginkgo.focus='Pull nested quarantine guard'
go test ./pkg/git/... -run 'TestNestedConflictPath'
```

All pass. A focused run that reports `0 of 0` specs means the focus string matched nothing — fix the focus, do not accept it.

Evidence that the change landed (each must print at least one match, and each exits 0 on a match):

```bash
grep -n 'acceptUpstreamDeletion' pkg/git/git.go
grep -n 'dropAcceptedDrains' pkg/git/git.go
grep -n 'accepted upstream deletion of quarantined path' pkg/git/git.go
grep -n 'upstream deletion' pkg/git/git.go
grep -n 'nested conflicted path already under _conflicts/ rejected; aborting merge' pkg/git/git.go
grep -n 'nested conflict path already quarantined' pkg/git/git.go
```

The last two prove the refusal surface was preserved, not rewritten.

Spec-count check — the new specs are present and the existing guard specs were not removed:

```bash
grep -c 'Upstream drain of a quarantined path' pkg/git/git_test.go
grep -c 'setupQuarantinePathFixture' pkg/git/git_test.go
grep -c 'a conflicted path already under _conflicts/ is the only conflict' pkg/git/git_test.go
grep -c 'a merge contains a nested path and an ordinary resolvable path' pkg/git/git_test.go
grep -c 'Quarantine backlog gauge' pkg/git/git_test.go
```

Each must print at least `1`.

Frozen-surface and no-new-metric checks (each `grep -n` must print at least one match and exit 0; each `! grep -q` must exit 0):

```bash
grep -n 'Resolve(ctx context.Context, conflictedPaths \[\]string) error' pkg/git/conflict_resolver.go
grep -n 'merge: resolved=' pkg/git/git.go
grep -n 'git_rest_quarantined_backlog' pkg/metrics/metrics.go
grep -n 'nested_source' pkg/metrics/metrics.go
! grep -q 'git_rest_modify' pkg/metrics/metrics.go
! grep -q 'git_rest_drain' pkg/metrics/metrics.go
ls mocks/metrics.go mocks/conflict_resolver.go
```

Absence is written as `! grep -q`, never as `grep -c` — `grep -c` prints `0` and exits non-zero, so an absence assertion written that way fails the step it was meant to pass. The `ls` must list both files.

Out-of-scope checks:

```bash
! grep -q '^## Unreleased' CHANGELOG.md
ls docs/
```

The first must exit 0 — prompt 3 creates the `## Unreleased` section and this prompt must not. If it exits 1, a sibling prompt already ran and this prompt must not touch the changelog. The `ls` must list exactly `api.md`, `deployment.md`, `dod.md`, `verifying-specs.md`.

Self-check before finishing: re-run `make precommit`, then walk each requirement above against the change — the drain is accepted with one INFO line naming the path and `upstream deletion`, the gauge drops to 0, `nested_source` does not move for a drain, the local commit remains an ancestor of the merge, and a `_conflicts/` content conflict is still refused with `nested_source` +1, no `_conflicts/_conflicts`, and an empty `git status --porcelain`.
</verification>
