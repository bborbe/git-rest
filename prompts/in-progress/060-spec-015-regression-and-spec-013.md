---
status: approved
spec: [015-bug-pull-wedges-on-modify-delete-conflict]
created: "2026-10-07T18:52:08Z"
queued: "2026-10-07T19:36:29Z"
branch: dark-factory/bug-pull-wedges-on-modify-delete-conflict
---

# Lock in the two new shapes, reconcile spec 013, and record the change in the changelog

<summary>
- The two shapes this spec fixes are pinned by tests that fail loudly if either half regresses
- A merge failure whose output carries no recognised conflict line is proven to leave the repository clean, not mid-merge
- A resolved modify/delete conflict is proven to create no quarantine residue and no second `_conflicts/` level
- An accepted drain is proven to record no quarantine event
- Spec 013 now states the operator-drain exception next to its no-auto-delete rule and next to its abort non-goal, so a reader of 013 finds the amendment
- The change is recorded in the changelog under a new `## Unreleased` section
- No production code, metric, alert or interface changes in this prompt
</summary>

<objective>
Lock in the two new shapes with additive regression specs (the mid-merge invariant pair, no quarantine residue, no second `_conflicts/` level, no quarantine event), reconcile spec 013's text with this spec's deliberate operator-drain exception, and record the fix in `CHANGELOG.md` under `## Unreleased`.
</objective>

<context>
Read the repo's `README.md` and `Makefile` for project conventions. (There is no `CLAUDE.md` at the repo root — it is untracked and present only in the operator's main checkout, so it is not visible from this worktree.)

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, external test packages, real `git` via `os/exec` against a temp working tree
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — entry format, the required `<prefix>:` style, and the `## Unreleased` rules
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — house style for prose and headings in the spec text

Files to read before editing:

- `pkg/git/resolve_conflict_merge_test.go` — the internal `package git` test file; its existing helpers `unsafeTestMetrics`, `fakeResolver`, `failingResolver` and the `run` closure pattern inside `TestResolveConflictPathsUnsafePath`
- `pkg/git/git_test.go` — `setupModifyDeleteFixture` and `countFilesUnder` (added by prompt 1), `setupQuarantinePathFixture` (added by prompt 2), `captureSlogLogs` (~1607), `gatherQuarantinedFiles` (~937), `gatherQuarantinedBacklog` (~953), `gitOutputStr` (~833), `runGit` (~29)
- `pkg/git/git.go` — `resolveConflictMerge` (~777) with the empty-conflict-list branch (prompt 1 added the abort), `resolveModifyDelete`, `acceptUpstreamDeletion`, `classifyConflicts`
- `specs/in-progress/013-quarantine-nesting-and-drain.md` — the whole file, in particular `## Non-goals` (both the no-auto-delete bullet and the "Do NOT suppress the abort for an all-rejected merge" bullet) and `## Desired Behavior` 2 and 3
- `specs/in-progress/015-bug-pull-wedges-on-modify-delete-conflict.md` — `## Constraints` (first bullet, "Amends spec 013"), `## Acceptance Criteria` 3, 9, 10, 11
- `CHANGELOG.md` — the file header and the most recent released sections, for entry style. There is currently **no** `## Unreleased` section.

**Preconditions from prompts 1 and 2 (already on this branch — do not re-implement):**

- `parseMergeConflictPaths` recognises the content and modify/delete forms.
- `resolveConflictMerge` aborts the merge on the empty-conflict-list branch before returning.
- `classifyConflicts`, `conflictKind`, `stageUpstreamVersion`, `resolveModifyDelete` (both arms) and `acceptUpstreamDeletion` exist.
- `validateConflictPathsNotNested(ctx, conflictPaths, kinds)` accepts an upstream deletion of a `_conflicts/` path and still refuses a `_conflicts/` path with no upstream deletion, with the unchanged WARN, `nested_source` increment and abort.
- `pkg/git/git_test.go` has `setupModifyDeleteFixture() (workDir string, remoteEdit func(), localDelete func(), cleanup func())`, `setupQuarantinePathFixture(path string) (workDir string, remoteDelete func(), remoteModify func(content string), localEdit func(content string) string, cleanup func())`, and `countFilesUnder(dir string) int`.
- Merge-level specs for both fixtures already exist in `pkg/git/git_test.go` under the Describes `Modify/delete conflict resolution (spec 015)` and `Upstream drain of a quarantined path (spec 015)`.

**Verified facts (do not re-derive; do not contradict):**

- The empty-conflict-list branch of `resolveConflictMerge` now runs the abort (prompt 1 **logs**, rather than discards, a non-nil abort error) and increments `IncMergeOutcome("aborted")` before returning the merge's wrapped error.
- `git merge --abort` fails with a non-zero exit when no merge is in progress — verified live. The branch must therefore discard the abort's error; the merge's own error is what surfaces.
- `gatherQuarantinedFiles()` reads the process-global `git_rest_quarantined_files_total` counter; `gatherQuarantinedBacklog()` reads the process-global `git_rest_quarantined_backlog` gauge. Both are shared by every spec in the test binary, so every assertion on them must be a **delta** against a read taken immediately before the pull.
- `os.Stat(filepath.Join(workDir, ".git", "MERGE_HEAD"))` satisfies `os.IsNotExist` after a completed or aborted pull. Do NOT use `git rev-parse --verify MERGE_HEAD`: `gitOutputStr` panics on a non-zero exit, and the absence case exits non-zero.
- `gitOutputStr` panics on a non-zero exit code, so it can only be used for commands expected to succeed. `git merge-base --is-ancestor` exits 0 on success and is therefore usable directly.
- Spec 013 is `status: verifying`. Its `## Non-goals` currently contains, verbatim: `- Do NOT auto-repair, auto-restore, or auto-delete quarantined content. Quarantine surfaces and preserves; the operator owns repair. The drain is a *signal*, not a remediation.` and `- Do NOT suppress the abort for an all-rejected merge. Calling a guard-rejected nested file "handled" so the merge could commit would either stage a conflicted file with its markers intact — the exact harm the Problem section calls out — or require a new commit-with-unmerged-entries path that git refuses.`
- Spec 013's `## Desired Behavior` 2 and 3 describe the guard as rejecting a conflicted path that "already begins with `_conflicts/`" and aborting the merge. Those statements remain true for a `_conflicts/` path with no upstream deletion; spec 015 narrows the guard for one case only.
- The literal strings `operator drain` and `modify/delete` currently appear **nowhere** in `specs/in-progress/013-quarantine-nesting-and-drain.md`, and `modify/delete` appears nowhere in `CHANGELOG.md`.
- `CHANGELOG.md` currently starts with the intro paragraphs (`# Changelog`, the SemVer explanation) followed directly by `## v0.29.1`. There is no `## Unreleased` section, so this prompt creates one.

**Sibling prompts (do not do their work):**

- Prompt 1 shipped the parser, the empty-conflict-list abort and the modify/delete resolution. Prompt 2 shipped the guard narrowing and the drain acceptance. Do NOT change any production code, any metric, `mocks/`, `main.go`, `helm/` or `docs/` in this prompt.
- Prompt 4 documents the rung-1 recipe in `docs/verifying-specs.md`. Do NOT touch `docs/`.
- The lock-in specs you add must be **additive negative evidence only**. Do NOT re-assert what prompts 1 and 2 already assert (the `merge: resolved=[…] quarantined=[…]` message, the restored upstream content, the `HEAD == origin/<branch>` equality, the `upstream deletion` INFO line) and do NOT modify or rewrite any existing spec.
</context>

<requirements>

## 1. Add the empty-conflict-list regression test (AC 3, third clause)

In `pkg/git/resolve_conflict_merge_test.go` (`package git`, standard `testing`), add a test that proves a merge failure whose output carries no parseable conflict line leaves the repository clean rather than mid-merge:

```go
// TestResolveConflictMergeEmptyListAborts verifies AC3's third clause: the
// empty-conflict-list branch aborts the in-progress merge before returning, so
// Pull never returns while the repository is mid-merge.
func TestResolveConflictMergeEmptyListAborts(t *testing.T)
```

Construction, using the file's existing idioms (`os.MkdirTemp` + `t.Cleanup`, a `run` closure over `exec.Command("git", ...)` with `cmd.Dir`, `t.Fatalf` on unexpected failure):

1. Initialise a temp repo (`git init -q -b main`), configure `user.email` / `user.name`, commit `--allow-empty -q -m init`.
2. Create a genuine conflict **in progress**: add a tracked file, commit and push to a bare remote is not required — instead create a second commit on a branch and merge it so git leaves an unmerged entry. The simplest deterministic construction is: commit `a.md` containing `one\n`, then `git checkout -q -b other`, write `two\n` to `a.md`, commit, `git checkout -q main`, write `three\n` to `a.md`, commit, then run `git merge --no-edit other` **expecting a non-zero exit** (use `exec.Command` directly and ignore the error — the `run` closure panics on failure). Assert `os.Stat(filepath.Join(workDir, ".git", "MERGE_HEAD"))` succeeds at this point, so the fixture is proven to be mid-merge before the call under test.
3. Construct the repo: `New(workDir, &unsafeTestMetrics{}, libtime.NewCurrentDateTime(), "", fakeResolver{}).(*git)`, using the existing internal fakes.
4. Call `repo.resolveConflictMerge(ctx, "other", []byte("fatal: refusing to merge unrelated histories\n"), stderrors.New("exit status 1"))`.
5. Assert the returned error is non-nil; `os.Stat(filepath.Join(workDir, ".git", "MERGE_HEAD"))` satisfies `os.IsNotExist`; and `git status --porcelain` (via a `exec.Command` capturing combined output, not `gitOutputStr`) prints 0 lines. Assert `metrics.abortedCount()` is 1 — the empty-list branch records the aborted outcome.

Do not change any existing test in that file, including `TestNestedConflictPath` and the two `resolveConflictPaths` tests.

## 2. Add the regression lock-in Describe (AC 3 first clause, plus AC 6/7 negative evidence for the drain)

AC 3's **second** clause — the aborted-re-quarantine fixture-C probes — is prompt 2's, not this prompt's: prompt 2's fixture-C spec owns both the `status --porcelain` empty assertion and the `MERGE_HEAD` absent assertion for that shape. This Describe must not re-create a fixture-C. (Relabelled 2026-10-07 after an audit found the original header claimed "first and second clauses" while the body delivered fixture A and the drain.)

In `pkg/git/git_test.go` (`package git_test`, Ginkgo v2 + Gomega), add a new top-level `var _ = Describe("Spec 015 regression lock-in", func() { ... })` with `ctx = context.Background()` in its `BeforeEach`. Reuse the fixture helpers from prompts 1 and 2 — do not add a third fixture.

Spec A — **a resolved modify/delete conflict leaves no merge in progress and no quarantine residue.** Fixture: `setupModifyDeleteFixture()`, then `remoteEdit()` and `localDelete()`. Pull with `git.New(workDir, metrics.NewMetrics(), libtime.NewCurrentDateTime(), "", git.NewMarkerResolver(workDir))`. Assert:

- `os.Stat(filepath.Join(workDir, ".git", "MERGE_HEAD"))` satisfies `os.IsNotExist`;
- `strings.TrimSpace(gitOutputStr(workDir, "status", "--porcelain"))` is empty;
- `countFilesUnder(filepath.Join(workDir, "_conflicts"))` equals 0;
- `os.Stat(filepath.Join(workDir, "_conflicts", "_conflicts"))` satisfies `os.IsNotExist`;
- `gatherQuarantinedFiles()` delta 0 against a read taken immediately before the pull.

Spec B — **an accepted drain leaves no merge in progress, no quarantine residue and no quarantine event.** Fixture: `setupQuarantinePathFixture("_conflicts/25 Tasks/Prev A.1791388434.md")`, then `remoteDelete()` and `localEdit("---\ntitle: a\n---\nLOCAL REPLICA EDIT\n")`. Pull with the marker resolver as in spec A. Assert the same five probes, with the `countFilesUnder` and `gatherQuarantinedFiles` delta assertions carrying the "the drain is not a quarantine event" half.

Assert **only** those probes. Do NOT assert the commit message, the restored content, the `HEAD == origin/main` equality or the `upstream deletion` log line — prompts 1 and 2 own those, and duplicating them here would make this Describe a second copy of the same tests rather than a regression lock-in. Do not modify or rewrite any existing spec or helper.

## 3. Reconcile spec 013 with this amendment (AC 9)

Edit `specs/in-progress/013-quarantine-nesting-and-drain.md`. This is the amendment spec 015's `## Constraints` requires: 013's `## Non-goals` says "Do NOT suppress the abort for an all-rejected merge", and this spec adds a deliberate, narrow exception. Make a reader of 013 find it in both places, and change nothing else in the file:

1. In `## Non-goals`, extend the **no-auto-delete** bullet — the one beginning `- Do NOT auto-repair, auto-restore, or auto-delete quarantined content.` — with one or two sentences stating the operator-drain exception: the operator drains a quarantined file by deleting it on the remote, that deletion arrives as an upstream deletion of a `_conflicts/` path, and spec 015 accepts it (the path leaves the tree, the merge commits and pushes, the local pre-drain content stays reachable in git history). State that the no-auto-delete rule is unchanged: git-rest still never deletes quarantined content on its own.
2. In `## Non-goals`, extend the **abort** bullet — the one beginning `- Do NOT suppress the abort for an all-rejected merge.` — with one or two sentences reconciling it with the new path: the abort rule stands for every rejected merge, including a `_conflicts/` path both sides changed; the only exception is a `_conflicts/` path whose upstream change is a deletion (the operator's drain), which is resolved rather than aborted, and which is a `git modify/delete` conflict rather than an all-rejected merge.
3. Do NOT renumber, reword or delete any existing bullet, do NOT touch any other section, and do NOT change 013's frontmatter or its status.

Both required literals must appear in the file after the edit: the substring `operator drain` and the substring `modify/delete`.

## 4. Record the change in `CHANGELOG.md` (AC 11)

`CHANGELOG.md` has no `## Unreleased` section today. Insert one **immediately after the file's intro paragraphs and before the most recent released section** (`## v0.29.1`), then add the entries beneath it. Follow `changelog-guide.md`: one bullet per logical change, each starting with a required prefix, specific enough to name types and commands, never a restatement of what was verified.

Add exactly two bullets:

1. A `fix:` bullet describing the modify/delete defect and its repair: the conflict-path parser matched only the content-conflict form, so a `CONFLICT (modify/delete):` line produced an empty conflict list, the resolver was never reached, and the pull returned without running `git merge --abort` — leaving the repository mid-merge, readiness non-200, and writes failing with `Committing is not possible because there are unmerged files`. The parser now recognises both conflict forms, a modify/delete conflict resolves by taking the upstream version under both resolver configurations (marker and YAML), and the empty-conflict-list branch aborts before returning so no pull leaves the repository mid-merge. Name the metric evidence in the text where it reads naturally (`git_rest_quarantined_backlog`, `git_rest_resolver_failures_total{category="nested_source"}`).
2. A `fix:` bullet describing the guard narrowing: the nesting guard aborted the merge for any conflicted path under `_conflicts/`, including a `_conflicts/` entry the upstream side had drained by deleting it, so the drain never landed and the replica stayed wedged. An upstream deletion of a `_conflicts/` path is now accepted — the path leaves the tree, the merge commits and pushes, the backlog gauge counts down, and one INFO line names the path — while a `_conflicts/` path both sides changed is still refused, so no path is ever quarantined twice and no second `_conflicts/` level is created.

The first bullet (the modify/delete defect) must contain the literal substring `modify/delete` **exactly once**, and no other line in `CHANGELOG.md` may contain it — the verification asserts `grep -c 'modify/delete' CHANGELOG.md` prints exactly `1`, so a natural write that mentions the substring twice fails an otherwise correct changelog. Neither bullet may contain a bash comment copied from a prompt's `<verification>` block, and neither may describe what was verified rather than what was implemented.

## 5. Self-check before finishing

Run every command in `<verification>` from the repo root and confirm each result. Then walk each requirement above against the change: the empty-list branch is proven to leave `MERGE_HEAD` absent and `git status --porcelain` empty; both lock-in specs assert the invariant pair, zero `_conflicts/` files, no second `_conflicts/` level and a zero quarantine-counter delta; spec 013 carries both the `operator drain` and the `modify/delete` prose; and `CHANGELOG.md` carries a `## Unreleased` section whose `modify/delete` bullet is the only one in the file.

End your final message with the standard dark-factory completion report (the template dark-factory appends to this prompt), then stop. Report `"status":"success"` only if `make precommit` exited 0; report `"partial"` if the code works but `make precommit` failed on an unrelated pre-existing issue; report `"failed"` otherwise. Include the verification command and its exit code.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- This prompt changes tests, one spec's prose and the changelog only. Do NOT change any Go production file, `mocks/`, `pkg/metrics/`, `main.go`, `helm/` or `docs/`.
- Do NOT modify or rewrite any existing spec in `pkg/git/git_test.go` or any existing test in `pkg/git/resolve_conflict_merge_test.go`. This prompt only ADDS.
- Do NOT re-assert in the lock-in Describe what prompts 1 and 2 already assert (the commit message, the restored upstream content, the `HEAD == origin/<branch>` equality, the `upstream deletion` INFO line). The lock-in asserts negative evidence and the invariant pair only.
- Every counter or gauge assertion MUST be a delta against a read taken immediately before the pull. `git_rest_quarantined_files_total` and `git_rest_quarantined_backlog` live in the process-global registry and are shared by every spec in that test binary.
- Do NOT use `git rev-parse --verify MERGE_HEAD` in a spec: `gitOutputStr` panics on a non-zero exit and the absence case exits non-zero. Use `os.Stat` + `os.IsNotExist`.
- Do NOT use `strings.Count` over the whole output of `git ls-tree -r --name-only HEAD`; split it into lines and assert on the slice.
- The `resolveConflictMerge` empty-list test MUST construct a genuine in-progress merge and prove `MERGE_HEAD` is present before the call under test, so the assertion is not vacuous.
- In `specs/in-progress/013-quarantine-nesting-and-drain.md`, change ONLY the two `## Non-goals` bullets named in requirement 3. Do NOT renumber, reword or delete any other bullet, do NOT touch another section, and do NOT change the frontmatter or `status`.
- In `CHANGELOG.md`, create the `## Unreleased` section in exactly one place, immediately before the most recent released section. Do NOT reorder or edit any released section. The literal `modify/delete` must appear only inside the new section.
- Do NOT add a metric, a gauge, an alert or a counter; the existing `nested_source` counter and `git_rest_quarantined_backlog` gauge carry the evidence.
- Do NOT change the `ConflictResolver` interface, the `merge: resolved=[…] quarantined=[…]` commit-message format, or the public HTTP contract in `docs/api.md`.
- Preserve the repo's documented invariants: errors wrapped with `github.com/bborbe/errors` (never `fmt.Errorf`), no `context.Background()` in `pkg/`, `log/slog` for logging, timestamps from the injected `libtime` getter rather than `time.Now()`, and counters pre-initialised in `init()`.
- Existing tests must still pass, including every spec in the `Modify/delete conflict resolution (spec 015)` and `Upstream drain of a quarantined path (spec 015)` Describe blocks added by prompts 1 and 2, and the `Pull nested quarantine guard` Describe.
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
go test ./pkg/git/... -run 'TestResolveConflictMergeEmptyListAborts'
go test ./pkg/git/... -ginkgo.focus='Spec 015 regression lock-in'
go test ./pkg/git/... -ginkgo.focus='Upstream drain of a quarantined path'
```

All pass. A focused run that reports `0 of 0` specs means the focus string matched nothing — fix the focus, do not accept it.

Regression-test presence (each must print at least one match, and each exits 0 on a match):

```bash
grep -n 'TestResolveConflictMergeEmptyListAborts' pkg/git/resolve_conflict_merge_test.go
grep -n 'Spec 015 regression lock-in' pkg/git/git_test.go
grep -c 'MERGE_HEAD' pkg/git/git_test.go
grep -n 'os.IsNotExist' pkg/git/git_test.go
```

Spec-013 reconciliation (each must print at least one match, and each exits 0 on a match):

```bash
grep -n 'operator drain' specs/in-progress/013-quarantine-nesting-and-drain.md
grep -n 'modify/delete' specs/in-progress/013-quarantine-nesting-and-drain.md
grep -n 'Do NOT auto-repair, auto-restore, or auto-delete quarantined content' specs/in-progress/013-quarantine-nesting-and-drain.md
grep -n 'Do NOT suppress the abort for an all-rejected merge' specs/in-progress/013-quarantine-nesting-and-drain.md
```

The last two prove the two amended bullets were extended, not replaced.

Changelog check — the `modify/delete` entry must be inside `## Unreleased` (AC 11, section-walking form; a line-scoped `grep -A` window would swallow a neighbouring section):

```bash
awk '/^## /{sec=$0} /modify\/delete/{print sec}' CHANGELOG.md
```

Must print `## Unreleased`. Exactly one line is expected, because `modify/delete` appears nowhere else in the file:

```bash
grep -c 'modify/delete' CHANGELOG.md
grep -c '^## Unreleased' CHANGELOG.md
```

The first must print `1` and the second must print `1`.

No-production-change checks (each `grep -n` must print at least one match and exit 0; each `! grep -q` must exit 0):

```bash
grep -n 'git_rest_quarantined_backlog' pkg/metrics/metrics.go
grep -n 'merge: resolved=' pkg/git/git.go
grep -n 'Resolve(ctx context.Context, conflictedPaths \[\]string) error' pkg/git/conflict_resolver.go
! grep -q 'git_rest_modify' pkg/metrics/metrics.go
! grep -q 'git_rest_drain' pkg/metrics/metrics.go
ls mocks/metrics.go mocks/conflict_resolver.go
```

Absence is written as `! grep -q`, never as `grep -c` — `grep -c` prints `0` and exits non-zero, so an absence assertion written that way fails the step it was meant to pass. The `ls` must list both files.

Out-of-scope check:

```bash
ls docs/
```

Must list exactly `api.md`, `deployment.md`, `dod.md`, `verifying-specs.md` — prompt 4 owns `docs/`.

Self-check before finishing: re-run `make precommit`, then walk each requirement above against the change — the empty-list branch test proves `MERGE_HEAD` absent and a clean worktree, both lock-in specs assert the invariant pair plus zero `_conflicts/` files, no second `_conflicts/` level and a zero quarantine-counter delta, spec 013 carries the `operator drain` and `modify/delete` prose in the two named bullets, and `CHANGELOG.md` carries one `## Unreleased` section whose `modify/delete` bullet is the only occurrence in the file.
</verification>
