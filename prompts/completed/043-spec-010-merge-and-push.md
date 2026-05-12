---
status: completed
spec: [010-merge-with-conflict-resolver]
summary: Replaced pullRebaseAndPush with pullMergeAndPush in Pull's default case, added parseMergeConflictPaths and resolveConflictMerge helpers, extended recoverRepoState with MERGE_HEAD detection via recoverAbandonedMerge, wired ConflictResolver through New()/factory/main.go, updated all git.New call sites in tests, and added AC1/AC2/AC3/AC5 integration tests.
container: git-rest-043-spec-010-merge-and-push
dark-factory-version: v0.156.1-1-g04f3863-dirty
created: "2026-05-12T21:00:00Z"
queued: "2026-05-12T21:14:19Z"
started: "2026-05-12T21:20:35Z"
completed: "2026-05-12T21:32:37Z"
branch: dark-factory/merge-with-conflict-resolver
---

<summary>
- The puller's diverged-history path uses `git merge` instead of `git rebase` — non-overlapping changes auto-merge into a merge commit, `result="clean"` counter increments, and a structured log line is emitted
- Same-line conflicts delegate to the wired `ConflictResolver`; on success the puller commits the merge with the frozen message `git-rest: merge with marker-preserved conflicts in <paths>`, `result="resolved"` counter increments, and conflict path count is recorded
- If the resolver returns an error, the merge is aborted (`git merge --abort`), the repo is left clean, `result="aborted"` increments, and `ErrConflictResolutionFailed` is returned so operators can alert specifically on resolver failure vs. network/push failure
- `recoverRepoState` now also detects and aborts abandoned merges (`.git/MERGE_HEAD` present) before the normal pull flow runs — leftover merge state from a killed process self-heals on the next tick
- `git.New()` constructor gains a `ConflictResolver` parameter; `MarkerResolver` is wired by default in `main.go`; factory is updated accordingly
- All existing `git.New(...)` call sites in tests are updated to pass the resolver; the diverged-rebase test context is updated to match merge semantics
- AC1, AC2, AC3 tests are added to the Pull state machine suite, exercising clean merge, marker-resolved conflict, and aborted merge paths end-to-end
- `make precommit` passes; CHANGELOG updated
</summary>

<objective>
Replace `pullRebaseAndPush` with `pullMergeAndPush` in `pkg/git/git.go`, update `recoverRepoState` to abort abandoned merges, wire `ConflictResolver` through `New()` / factory / `main.go`, update all test call sites, and add AC1/AC2/AC3 integration tests. Prompt 1 must be completed and `make precommit` passing before this prompt begins — the `ConflictResolver` interface, `ErrConflictResolutionFailed`, and new `Metrics` methods must already exist.
</objective>

<context>
**Cross-reference to prompt 1:** This prompt depends on `ConflictResolver` interface, `MarkerResolver` type, `ErrConflictResolutionFailed` sentinel, `MergeOutcomeTotal`/`ConflictPathsTotal` Prometheus counters, and the `IncMergeOutcome`/`IncConflictPaths` metrics-interface methods — all introduced by prompt `1-spec-010-resolver-interface.md`. Spec AC4 (MarkerResolver Resolve behavior unit test) is covered by prompt 1; this prompt covers AC1, AC2, AC3, AC5, AC6 (and the `make precommit` half of AC6).

Read `CLAUDE.md` for project conventions.

Read these coding guides before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-patterns.md` — interface→struct→constructor; error wrapping conventions
- `go-error-wrapping-guide.md` — `errors.Wrap`/`errors.Wrapf` from `github.com/bborbe/errors`; sentinel errors; never `fmt.Errorf`
- `go-logging-guide.md` — `slog.InfoContext`, key/value structured args
- `go-testing-guide.md` — Ginkgo/Gomega, external test packages, `BeforeEach` fixture setup
- `test-pyramid-triggers.md` — which test types to write for each code change

Files to read in full before implementing (re-verify line numbers at current HEAD):

- `pkg/git/git.go` (full, ~640 lines):
  - `git` struct (line ~106) — add `resolver ConflictResolver` field
  - `New()` (line ~92) — add `resolver ConflictResolver` as 5th param
  - `recoverRepoState` (line ~449) — add MERGE_HEAD detection before existing rebase check
  - `Pull()` switch statement (line ~574, `default:` branch) — change from `pullRebaseAndPush` to `pullMergeAndPush`
  - `pullRebaseAndPush` (line ~428) — do NOT remove; do NOT change; just stop calling it from Pull
  - Existing imports: `bytes`, `context`, `stderrors "errors"`, `fmt`, `log/slog`, `os`, `os/exec`, `path/filepath`, `strings`, `sync`, `time`. Add `"sort"`.
  - `ErrConflictResolutionFailed` and `ConflictResolver` interface are already present (added by prompt 1)

- `pkg/git/git_test.go` (full, ~1000 lines):
  - `noopMetrics` struct (line ~35) — already has `IncMergeOutcome` and `IncConflictPaths` stubs (added by prompt 1)
  - All `git.New(workDir, m, t, sshKey)` calls — must each gain a 5th argument (see requirement 4)
  - `Describe("Pull state machine", ...)` (line ~641):
    - `BeforeEach` wires `pg = git.New(...)` — update to 5-arg form with `git.NewMarkerResolver(workDir)`
    - `Context("diverged, no content conflict (rebase+push)", ...)` (line ~703) — update for merge semantics
    - `Context("diverged, content conflict during rebase", ...)` (line ~742) — replace with AC1/AC2/AC3 tests
  - `Describe("Entry-state recovery", ...)` (line ~777) — update all `git.New(...)` calls here too

- `pkg/factory/factory.go` (~74 lines): `CreateGitClient` — add `resolver git.ConflictResolver` param, thread through to `git.New()`
- `main.go` (~380 lines): `createGitClient` method (line ~362) — construct `git.NewMarkerResolver(a.Repo)` and pass to `CreateGitClient`
- `CHANGELOG.md` — add `## Unreleased` at the top after implementing

Inline contract for `parseMergeConflictPaths` (already exists as `parseRebaseConflictPath` in the same file — model yours similarly):
```go
// parseMergeConflictPaths extracts all conflicting file paths from git merge output.
// git merge emits "CONFLICT (content): Merge conflict in <path>" for each content conflict.
// Returns an empty slice if no conflict lines are found (non-conflict merge failure).
func parseMergeConflictPaths(output string) []string {
    const prefix = "Merge conflict in "
    var paths []string
    seen := make(map[string]bool)
    for _, line := range strings.Split(output, "\n") {
        if idx := strings.Index(line, prefix); idx >= 0 {
            path := strings.TrimSpace(line[idx+len(prefix):])
            if path != "" && !seen[path] {
                paths = append(paths, path)
                seen[path] = true
            }
        }
    }
    return paths
}
```
</context>

<requirements>

## 1. Add `"sort"` import and `resolver` field in `pkg/git/git.go`

Add `"sort"` to the import block (keep alphabetical order within the stdlib group).

Add `resolver ConflictResolver` as the last field on the `git` struct:
```go
type git struct {
    repoPath              string
    mu                    sync.Mutex
    metrics               metrics.Metrics
    currentDateTimeGetter libtime.CurrentDateTimeGetter
    sshKeyPath            SSHKeyPath
    resolver              ConflictResolver
}
```

## 2. Update `New()` constructor in `pkg/git/git.go`

Add `resolver ConflictResolver` as the 5th parameter and wire it into the struct:

```go
func New(
    repoPath string,
    m metrics.Metrics,
    currentDateTimeGetter libtime.CurrentDateTimeGetter,
    sshKeyPath SSHKeyPath,
    resolver ConflictResolver,
) Git {
    return &git{
        repoPath:              repoPath,
        metrics:               m,
        currentDateTimeGetter: currentDateTimeGetter,
        sshKeyPath:            sshKeyPath,
        resolver:              resolver,
    }
}
```

## 3. Add `parseMergeConflictPaths` and `pullMergeAndPush` to `pkg/git/git.go`

Add `parseMergeConflictPaths` immediately after `parseRebaseConflictPath` (they are siblings). The body is inlined in the `<context>` section above — use it verbatim.

Add `pullMergeAndPush` immediately after `pullRebaseAndPush` (do NOT remove `pullRebaseAndPush`):

```go
// pullMergeAndPush merges upstream into the current branch and pushes.
// Non-overlapping changes are auto-merged by git's three-way merge (result="clean").
// Content conflicts are delegated to g.resolver (result="resolved" on success, "aborted" on failure).
// On resolver failure the merge is aborted so the repo is left in a clean state.
func (g *git) pullMergeAndPush(ctx context.Context, upstream string) error {
    out, mergeErr := g.runCmdRaw(ctx, g.repoPath, "merge", "--no-edit", upstream)
    if mergeErr != nil {
        conflictPaths := parseMergeConflictPaths(string(out))
        if len(conflictPaths) == 0 {
            // merge failed for a non-conflict reason (e.g. corrupted objects, bad upstream ref)
            g.metrics.IncGitOperationError("merge")
            return errors.Wrapf(ctx, mergeErr, "merge %s: %s", upstream, strings.TrimSpace(string(out)))
        }

        // Delegate conflict resolution to the wired resolver.
        if resolveErr := g.resolver.Resolve(ctx, conflictPaths); resolveErr != nil {
            // Abort to restore a clean working tree before returning.
            _ = g.runCmdRaw(ctx, g.repoPath, "merge", "--abort")
            g.metrics.IncMergeOutcome("aborted")
            return errors.Wrap(ctx, ErrConflictResolutionFailed, "conflict resolver failed")
        }

        // Commit the resolved merge with the frozen message (sorted paths for greppability).
        sort.Strings(conflictPaths)
        commitMsg := "git-rest: merge with marker-preserved conflicts in " + strings.Join(conflictPaths, ", ")
        if commitErr := g.runCmd(ctx, g.repoPath, "commit", "-m", commitMsg); commitErr != nil {
            _ = g.runCmdRaw(ctx, g.repoPath, "merge", "--abort")
            g.metrics.IncGitOperationError("merge")
            return errors.Wrap(ctx, commitErr, "commit resolved merge")
        }

        g.metrics.IncMergeOutcome("resolved")
        g.metrics.IncConflictPaths(len(conflictPaths))
        slog.InfoContext(ctx, "git-rest: merge committed with conflict markers", "paths", conflictPaths)

        if pushErr := g.runCmd(ctx, g.repoPath, "push"); pushErr != nil {
            g.metrics.IncGitOperationError("push")
            return errors.Wrap(ctx, pushErr, "push after resolved merge failed")
        }
        return nil
    }

    // Clean merge: git auto-produced a merge commit.
    g.metrics.IncMergeOutcome("clean")
    slog.InfoContext(ctx, "git-rest: merged diverged history", "upstream", upstream)

    if pushErr := g.runCmd(ctx, g.repoPath, "push"); pushErr != nil {
        g.metrics.IncGitOperationError("push")
        return errors.Wrap(ctx, pushErr, "push after clean merge failed")
    }
    return nil
}
```

## 4. Update `Pull()` switch statement in `pkg/git/git.go`

In the `switch` block inside `Pull()`, change only the `default:` branch:

```go
    default:
        return g.pullMergeAndPush(ctx, upstream)
```

`pullRebaseAndPush` remains in the file but is no longer called from `Pull`. Do NOT remove it (spec constraint: must not break callers that still reference it, even if no callers currently exist).

## 5. Update `recoverRepoState` in `pkg/git/git.go`

Add MERGE_HEAD detection as the FIRST check inside `recoverRepoState`, BEFORE the existing rebase-dir checks. Insert immediately after the function's opening brace:

```go
    // Abandoned merge: .git/MERGE_HEAD present means a previous merge was interrupted
    // (e.g. process killed mid-merge or resolver panicked). Abort it so Pull can run cleanly.
    mergeHeadFile := filepath.Join(g.repoPath, ".git", "MERGE_HEAD")
    if _, err := os.Stat(mergeHeadFile); err == nil {
        out, abortErr := g.runCmdRaw(ctx, g.repoPath, "merge", "--abort")
        if abortErr != nil {
            return errors.Wrapf(
                ctx,
                ErrRepoUnrecoverable,
                "git merge --abort failed: %s",
                strings.TrimSpace(string(out)),
            )
        }
        slog.InfoContext(ctx, "git-rest: recovered from abandoned merge")
        return nil
    }
```

The existing rebase-dir check (`os.Stat(rebaseMergeDir)` / `os.Stat(rebaseApplyDir)`) follows after this block, unchanged.

## 6. Update `pkg/factory/factory.go`

Add `resolver git.ConflictResolver` as the 5th parameter to `CreateGitClient` and thread it to `git.New()`:

```go
func CreateGitClient(
    repoPath string,
    m metrics.Metrics,
    currentDateTimeGetter libtime.CurrentDateTimeGetter,
    sshKeyPath git.SSHKeyPath,
    resolver git.ConflictResolver,
) git.Git {
    return git.New(repoPath, m, currentDateTimeGetter, sshKeyPath, resolver)
}
```

## 7. Update `main.go` — ALL FOUR `factory.CreateGitClient` call sites

`main.go` calls `factory.CreateGitClient(...)` in **four** places — all four MUST be updated to pass the new resolver argument, or `make precommit` will fail to compile.

Find them with:
```bash
grep -n "factory.CreateGitClient" /workspace/main.go
```

Expected output (line numbers may drift; exact count must be 4):
- `initRepoIfNeeded()` around line 307 — used to call `Init` on the new repo
- `cloneIfNeeded()` around line 330 — used to call `Clone` on the freshly-created dir
- `configureUserIfSet()` around line 350 — used to call `ConfigureUser`
- `createGitClient()` around line 373 — used to return the long-lived git client

For every call site, add `git.NewMarkerResolver(a.Repo)` as the 5th argument:

```go
factory.CreateGitClient(
    a.Repo,
    metrics.NewMetrics(),
    libtime.NewCurrentDateTime(),
    a.GitSSHKey,
    git.NewMarkerResolver(a.Repo),
)
```

After the edit, grep check: `grep -c "factory.CreateGitClient" /workspace/main.go` MUST still return `4`, and every match MUST be followed within ~6 lines by `git.NewMarkerResolver(a.Repo)`.

## 8. Update all `git.New(...)` call sites in `pkg/git/git_test.go`

Every call to `git.New(workDir, m, t, sshKey)` in `pkg/git/git_test.go` must gain a 5th argument. Use `git.NewMarkerResolver(workDir)` for call sites that do not test the resolver seam (all paths except AC3).

To find all call sites:
```bash
grep -n "git\.New(" /workspace/pkg/git/git_test.go
```

For each match, add `git.NewMarkerResolver(workDir)` (or the appropriate `workDir` variable for that scope) as the 5th argument. The variable name holding the repo path varies by test context — identify it from the surrounding code.

**Important edge cases:**
- The `Pull state machine` `BeforeEach` uses `workDir` — add `git.NewMarkerResolver(workDir)`
- The `Entry-state recovery` `Describe` blocks use `workDir` inside each `It` — add `git.NewMarkerResolver(workDir)` at each `git.New` call
- Some tests use a local `dir`, `targetDir`, `repoDir`, or `noRemoteDir` variable — use the correct variable name from that scope

## 9. Update the `Pull state machine` diverged test contexts in `pkg/git/git_test.go`

### 9a. Replace "diverged, no content conflict (rebase+push)" context (currently around line 703)

The existing context tests rebase semantics. Replace the entire `Context(...)` block with:

```go
Context("diverged, no content conflict (merge+push)", func() {
    BeforeEach(func() {
        externalPush("remote.txt", "from remote\n")
        writeLocalCommit(workDir, "local.txt", "local only\n")
    })

    It("AC1: merges and pushes, producing a merge commit with both files", func() {
        Expect(pg.Pull(ctx)).To(BeNil())
        log := gitOutputStr(workDir, "log", "--oneline")
        Expect(log).To(ContainSubstring("remote.txt"), "remote commit must be in log")
        Expect(log).To(ContainSubstring("local.txt"), "local commit must be in log")
        Expect(log).To(ContainSubstring("Merge"), "merge commit must be present")
    })

    It("AC1: increments merge outcome clean counter exactly once", func() {
        Expect(pg.Pull(ctx)).To(BeNil())
        Expect(fakeMetrics.IncMergeOutcomeCallCount()).To(Equal(1))
        Expect(fakeMetrics.IncMergeOutcomeArgsForCall(0)).To(Equal("clean"))
    })

    It("AC1: leaves nothing unpushed after merge", func() {
        Expect(pg.Pull(ctx)).To(BeNil())
        unpushed := strings.TrimSpace(gitOutputStr(workDir, "log", "@{u}..HEAD", "--oneline"))
        Expect(unpushed).To(BeEmpty())
    })
})
```

### 9b. Replace "diverged, content conflict during rebase" context (currently around line 742)

Replace the entire `Context(...)` block with AC2 and AC3 tests:

```go
Context("diverged, same-line conflict (merge + MarkerResolver)", func() {
    BeforeEach(func() {
        externalPush("conflict.txt", "remote content\n")
        writeLocalCommit(workDir, "conflict.txt", "local content\n")
    })

    It("AC2: Pull returns nil; merge commit message starts with frozen prefix", func() {
        Expect(pg.Pull(ctx)).To(BeNil())
        commitMsg := strings.TrimSpace(gitOutputStr(workDir, "log", "-1", "--format=%s"))
        Expect(commitMsg).To(HavePrefix("git-rest: merge with marker-preserved conflicts in"))
        Expect(commitMsg).To(ContainSubstring("conflict.txt"))
    })

    It("AC2: conflict.txt in merge commit contains both versions under markers", func() {
        Expect(pg.Pull(ctx)).To(BeNil())
        // Read file content from the latest merge commit tree (not working tree).
        content := gitOutputStr(workDir, "show", "HEAD:conflict.txt")
        Expect(content).To(ContainSubstring("<<<<<<<"), "must contain open marker")
        Expect(content).To(ContainSubstring("======="), "must contain separator")
        Expect(content).To(ContainSubstring(">>>>>>>"), "must contain close marker")
        Expect(content).To(ContainSubstring("remote content"), "remote version must be present")
        Expect(content).To(ContainSubstring("local content"), "local version must be present")
    })

    It("AC2: increments merge outcome resolved counter exactly once", func() {
        Expect(pg.Pull(ctx)).To(BeNil())
        Expect(fakeMetrics.IncMergeOutcomeCallCount()).To(Equal(1))
        Expect(fakeMetrics.IncMergeOutcomeArgsForCall(0)).To(Equal("resolved"))
    })

    It("AC2: increments conflict paths counter by 1 (one conflicted file)", func() {
        Expect(pg.Pull(ctx)).To(BeNil())
        Expect(fakeMetrics.IncConflictPathsCallCount()).To(Equal(1))
        Expect(fakeMetrics.IncConflictPathsArgsForCall(0)).To(Equal(1))
    })

    It("AC2: nothing unpushed after resolved merge", func() {
        Expect(pg.Pull(ctx)).To(BeNil())
        unpushed := strings.TrimSpace(gitOutputStr(workDir, "log", "@{u}..HEAD", "--oneline"))
        Expect(unpushed).To(BeEmpty())
    })
})

Context("diverged, same-line conflict, resolver returns error (AC3)", func() {
    var stubResolver *mocks.FakeConflictResolver

    BeforeEach(func() {
        externalPush("conflict.txt", "remote content\n")
        writeLocalCommit(workDir, "conflict.txt", "local content\n")

        // Wire a stub resolver that always errors.
        stubResolver = &mocks.FakeConflictResolver{}
        stubResolver.ResolveReturns(errors.New("stub resolver failed"))
        pg = git.New(workDir, fakeMetrics, libtime.NewCurrentDateTime(), "", stubResolver)
    })

    It("AC3: Pull returns error matching ErrConflictResolutionFailed via errors.Is", func() {
        err := pg.Pull(ctx)
        Expect(err).To(HaveOccurred())
        Expect(errors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue(),
            "expected ErrConflictResolutionFailed, got: %v", err)
    })

    It("AC3: repo is clean after aborted merge (on branch, no MERGE_HEAD)", func() {
        _ = pg.Pull(ctx)
        _, statErr := os.Stat(filepath.Join(workDir, ".git", "MERGE_HEAD"))
        Expect(os.IsNotExist(statErr)).To(BeTrue(), ".git/MERGE_HEAD must not exist after aborted merge")
        head := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "--abbrev-ref", "HEAD"))
        Expect(head).NotTo(Equal("HEAD"), "HEAD must be on a branch")
    })

    It("AC3: increments merge outcome aborted counter exactly once", func() {
        _ = pg.Pull(ctx)
        Expect(fakeMetrics.IncMergeOutcomeCallCount()).To(Equal(1))
        Expect(fakeMetrics.IncMergeOutcomeArgsForCall(0)).To(Equal("aborted"))
    })

    It("AC5: seam is genuinely pluggable — stub resolver Resolve was called once", func() {
        _ = pg.Pull(ctx)
        Expect(stubResolver.ResolveCallCount()).To(Equal(1),
            "resolver.Resolve must be called exactly once per conflict merge attempt")
        paths := stubResolver.ResolveArgsForCall(0)
        Expect(paths).To(ContainElement("conflict.txt"))
    })
})
```

**Imports for the new tests:**

`pkg/git/git_test.go` already imports stdlib `"errors"` as plain `errors` (see line 10). Use it directly:
- `errors.New("stub resolver failed")` — stdlib `errors.New` for the stub error
- `errors.Is(err, git.ErrConflictResolutionFailed)` — stdlib `errors.Is` for sentinel detection

Do NOT add an alias like `stderrors "errors"` — the plain `errors` import already covers both calls. Do NOT import `github.com/bborbe/errors` in this test file — bborbe/errors is used for *wrapping* errors in production code (`pkg/git/git.go`), but `errors.Is` from stdlib correctly traverses the bborbe-wrapped chain when called from tests.

Other imports needed (verify each before adding):
- `"path/filepath"` — already present
- `"os"` — already present
- `"github.com/bborbe/git-rest/mocks"` — already present

## 10. Add CHANGELOG entry

In `CHANGELOG.md`, add `## Unreleased` immediately before the first versioned heading. If `## Unreleased` already exists, append to it:

```markdown
## Unreleased

- fix: Replace `git rebase` with `git merge` in the puller's diverged-history path. Non-overlapping concurrent writes now auto-merge into a single commit (no operator action). Same-line conflicts are delegated to a pluggable `ConflictResolver`; the default `MarkerResolver` commits the merge with `<<<<<<<` / `=======` / `>>>>>>>` markers intact so both versions survive. Resolver failure aborts the merge and returns `ErrConflictResolutionFailed`. Two new counters (`git_rest_merge_outcome_total`, `git_rest_conflict_paths_total`) track merge outcomes and conflict frequency. Entry-state recovery extended to abort leftover `.git/MERGE_HEAD` from interrupted merges. Fixes vault-obsidian-openclaw-0 52h desync incident (2026-05-12): the dropped `tasks/analyse-trades-2026-05-11.md` commit would have been preserved under conflict markers instead of silently discarded.
```

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Do NOT remove `pullRebaseAndPush` — spec constraint: must not break if anything still references it
- Do NOT remove `RebaseConflictError` — spec constraint: must not change its meaning
- `context.Background()` MUST NOT appear in `pkg/` — only in `main.go` and test files
- Errors MUST be wrapped with `errors.Wrap`/`errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf`, never bare `return err`
- `ErrConflictResolutionFailed` MUST be returned wrapped via `errors.Wrap(ctx, ErrConflictResolutionFailed, ...)` so `errors.Is` traverses the chain
- The MERGE_HEAD check in `recoverRepoState` MUST come BEFORE the rebase-dir checks — this ordering ensures leftover merge state from the new code path is healed before the rebase path is checked
- `git merge --abort` in `pullMergeAndPush` (resolver failure path) MUST use `runCmdRaw` so errors can be logged (use `_` to discard the returned error — merge abort failure should not mask the resolver error)
- Commit message for resolved merge MUST use `-m` flag explicitly — never `--no-edit` for this path (we override the auto-generated message)
- Paths in the commit message MUST be sorted (`sort.Strings(conflictPaths)`) before joining — frozen format for operator `git log` grepping
- `pullMergeAndPush` MUST call `g.metrics.IncMergeOutcome("clean")` BEFORE `g.metrics.IncGitOperationError("push")` to ensure the outcome is recorded even if push fails
- `pullMergeAndPush` MUST call `g.metrics.IncConflictPaths(len(conflictPaths))` only on the resolved-success path (not aborted path)
- The `Pull()` switch `default:` branch change MUST only change the called function name — the surrounding switch structure is unchanged
- All `git.New(...)` call sites in `pkg/git/git_test.go` MUST be updated — a missed call site causes a compile error; use grep to find all of them
- All FOUR `factory.CreateGitClient(...)` call sites in `main.go` MUST be updated (see requirement 7) — partial updates cause compile errors. Grep check: `grep -c "factory.CreateGitClient" main.go` returns `4` BEFORE the edit and `4` AFTER; every match MUST be followed within ~6 lines by `git.NewMarkerResolver(a.Repo)` after the edit
- `g.resolver.Resolve(ctx, ...)` MUST be called with the inherited `ctx` (not a fresh one) so resolver implementations honor cancellation and timeouts driven by the surrounding `Pull` call
- The `FakeConflictResolver` mock (`mocks/conflict_resolver.go`) must already exist from prompt 1 — do NOT redefine it
- AC3 context MUST create its own `pg` in `BeforeEach` (with the stub resolver) — it must NOT reuse the outer `BeforeEach` `pg` which is wired with `MarkerResolver`
- Do NOT change the public HTTP contract (`/api/v1/files/*`, `/readiness`, `/healthz`, `/metrics`)
- MUST preserve entry-state recovery from spec 009 (the existing rebase-dir check is unchanged)
- Existing tests must still pass
</constraints>

<verification>
Run tests (iterative — repeat after each meaningful change):
```bash
cd /workspace && make test
```

Verify `pullMergeAndPush` is present and `pullRebaseAndPush` still present:
```bash
grep -n "func (g \*git) pull.*Push" /workspace/pkg/git/git.go
```
Expected: two lines — `pullRebaseAndPush` and `pullMergeAndPush`.

Verify `Pull()` default case calls merge not rebase:
```bash
grep -n "pullMergeAndPush\|pullRebaseAndPush" /workspace/pkg/git/git.go
```
Expected: `pullMergeAndPush` on the `default:` line of Pull's switch; `pullRebaseAndPush` is defined but NOT called from Pull's switch.

Verify MERGE_HEAD detection added to `recoverRepoState`:
```bash
grep -n "MERGE_HEAD" /workspace/pkg/git/git.go
```
Expected: one match inside `recoverRepoState`.

Verify `New()` has 5 parameters:
```bash
grep -n -A 7 "^func New(" /workspace/pkg/git/git.go
```
Expected: `resolver ConflictResolver` as the 5th param.

Verify factory is updated:
```bash
grep -n "resolver" /workspace/pkg/factory/factory.go
```
Expected: param in `CreateGitClient` and threaded to `git.New`.

Verify main.go wires `MarkerResolver`:
```bash
grep -n "NewMarkerResolver" /workspace/main.go
```
Expected: one match in `createGitClient`.

Verify no stale 4-arg `git.New` calls remain in test file:
```bash
grep -n 'git\.New(' /workspace/pkg/git/git_test.go | grep -v '5\|NewMarkerResolver\|stubResolver\|FakeConflict'
```
(This is approximate — review the output manually. Every `git.New` call should have 5 arguments.)

Spot-check AC1/AC2/AC3 tests:
```bash
cd /workspace && go test ./pkg/git/... -v -run "AC1|AC2|AC3|AC5"
```
Expected: all It blocks pass.

Spot-check entry-state recovery tests still pass:
```bash
cd /workspace && go test ./pkg/git/... -v -run "Entry-state recovery"
```
Expected: all four It blocks pass.

Final validation:
```bash
cd /workspace && make precommit
```
Must pass with exit code 0.
</verification>
