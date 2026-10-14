---
status: completed
summary: quarantineOne now restores the canonical path from HEAD after writing the conflicted bytes to _conflicts/, logs one INFO line, and two defect-encoding assertions plus a new AC2b spec, two docs sentences and a changelog entry record the fix
execution_id: git-rest-quarantine-restore-exec-062-restore-canonical-after-quarantine
dark-factory-version: v0.198.7
created: "2026-10-14T14:05:00Z"
queued: "2026-10-14T15:02:56Z"
started: "2026-10-14T15:30:19Z"
completed: "2026-10-14T15:38:51Z"
---

# Restore the canonical after a resolver-failure quarantine

<summary>
- A file quarantined because the resolver failed no longer disappears from the repository
- The conflicted bytes are still preserved under `_conflicts/`, unchanged
- The canonical path is left holding the local pre-merge version, so the path keeps existing
- Consumers that read the repository — dispatch, dedup, dashboards — no longer see a live file silently vanish
- A resolver failure now costs a copy of the file rather than the file itself
- The restore is visible in the log as one line naming the path, so an operator can tell it happened
- The existing quarantine copy, its destination naming and its log line are unchanged
- Nothing is discarded: the conflicted bytes are written to `_conflicts/` before the canonical is touched again
- No new metric, no new configuration, no change to the commit-message format
</summary>

<objective>
Make git-rest's resolver-failure quarantine preserve the canonical path instead of deleting it: after the conflicted bytes have been written under `_conflicts/`, restore the canonical from `HEAD` so a resolver failure costs a copy rather than the file, while a failed restore leaves the pre-existing removal behaviour and is reported.
</objective>

<context>
Read the coding-plugin guides below for the conventions this change touches. (A repo-root `CLAUDE.md` is gitignored and is **not** present in this worktree — do not look for it. The project rules it carries are restated in `<constraints>` below.)

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors`; never `fmt.Errorf`, never `context.Background()` in `pkg/`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, external test packages, real `git` via `os/exec` against a temp working tree
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md` — `log/slog`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — linter limits (funlen 80, nestif 4, golines 100)
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` heading; `feat:` / `fix:` prefix rules

Files to read in full before editing (line numbers are hints; anchor by symbol name):

- `pkg/git/git.go` — `quarantineOne` (~1255), `quarantineDestPath` (~359), `resolveEachPath` (~1116), `commitAndPushMerge` (~1157)
- `pkg/git/git_test.go` — `setupQuarantineFixture` (~861), `captureSlogLogs` (~1608), `gatherQuarantinedFiles` (~938), `gitOutputStr` (~834), the Describe containing `corruptPath = "note-00.md"`, and the Context `"diverged, same-line conflict, resolver returns error (AC3 — quarantine path)"`
- `CHANGELOG.md` — note there is **no** `## Unreleased` section; the newest heading is `## v0.29.4`

**The defect, established from source.** `quarantineOne` performs five sequential steps: read the conflicted bytes, `git rm -- <path>` (which is what clears the unmerged index entry), `os.MkdirAll` the destination directory, `os.WriteFile` the destination, `git add -- <destRel>`. There is **no** compensating restore of `HEAD:<path>`. The caller `resolveEachPath` appends the path to `quarantined` and moves on, and `commitAndPushMerge` commits that tree. So a resolver-failure quarantine removes a live file from the repository deterministically, and the commit message does not say so: `quarantined=[…]` reads as "preserved alongside" while it also means "deleted", and `resolved=[]` is the only visible tell.

**Observed, Personal vault 2026-10-14.** Three files were quarantined; all three canonicals were absent from the working tree, from `HEAD` and from `origin/master` at the quarantine merge. The pod log carried `resolver failed on path; attempting quarantine` followed by `quarantined conflicted file` and then `merge committed with per-file quarantine` with `resolved=[] quarantined=[…]`.

**Verified facts (do not re-derive; do not contradict):**

- `quarantineOne`'s failure paths all follow one shape: increment `g.metrics.IncResolverFailure(quarantineFailureIO)`, one `slog.ErrorContext` naming the step, `return false`. Keep that shape for any new failure path.
- `quarantineOne` returns `true` only after `git add -- <destRel>` succeeds, and it is the return value that decides whether the path lands in `quarantined` and therefore in the commit message.
- The existing success log line is `slog.WarnContext(ctx, "git-rest: quarantined conflicted file", "path", path, "dest", destRel)`. Existing specs assert on the message text and on the path; neither may change.
- `git checkout HEAD -- <path>` restores the path from the `HEAD` commit into both the index and the working tree and removes any unmerged index entry for it. During a merge `HEAD` is the local branch tip, so this is the local pre-merge side — the same side the vault runbook's mechanical default already picks.
- `quarantineDestPath` appends the timestamp differently by extension: a `.md` source becomes `_conflicts/<dir>/<base>.<ts>.md`, any other source becomes `_conflicts/<dir>/<base>.<ts>.quarantined`. Both fixtures below follow that split (`note-00.md` → `*.md`; `conflict.txt` → `*.quarantined`). Do not change this function.
- The commit-message format `merge: resolved=[…] quarantined=[…]` is fixed by an earlier spec and asserted by existing tests.
- **Two** existing assertions encode the defect, and this change must invert both. The first is in `pkg/git/git_test.go` inside the Describe with `corruptPath = "note-00.md"`, in the `It` titled `"AC1-AC6 + AC12: Pull returns nil; 1 file quarantined; 10 resolved; commit message in fixed format"`:

  ```go
  _, statErr := os.Stat(filepath.Join(workDir, corruptPath))
  Expect(os.IsNotExist(statErr)).To(BeTrue(),
      "corrupt file must not exist at original path after quarantine")
  ```

  In that fixture the local (`HEAD`) side of `note-00.md` is exactly `"---\nbad: : : colon: storm\n---\nlocal corrupt body\n"`, and the remote side is `"---\nshared: remote-0\n---\nremote body 0\n"`.

- The second is inside the Context `"diverged, same-line conflict, resolver returns error (AC3 — quarantine path)"`, in the `It` titled `"AC3: resolver-failed file is moved to _conflicts/"`:

  ```go
  _, statErr := os.Stat(filepath.Join(workDir, "conflict.txt"))
  Expect(os.IsNotExist(statErr)).To(BeTrue(),
      "original conflict.txt must not exist after quarantine")
  ```

  That fixture pushes `"remote content\n"` upstream and commits `"local content\n"` locally, then wires a stub resolver that always errors — so it drives the quarantine success path with `conflict.txt` present in `HEAD`, and its local side is exactly `"local content\n"`.

- The remaining `os.IsNotExist` assertions in the file concern `.git/MERGE_HEAD`, `_conflicts/_conflicts`, or an operator-drained path. None of them encodes this defect; leave all of them alone. `pkg/git/resolve_conflict_merge_test.go`'s `os.IsNotExist` assertions are likewise unrelated, and its two direct `quarantineOne` tests (`TestQuarantineOneGitRmFails`, `TestQuarantineOneSourceMissing`) exercise failure branches that return before the new restore step, so both keep passing unchanged.
</context>

<requirements>

## 1. Restore the canonical after the quarantine copy is durable

In `pkg/git/git.go`, in `quarantineOne`, after the existing `git add -- <destRel>` step succeeds and **before** the existing `IncQuarantinedFiles` / success `slog.WarnContext` pair, add a sixth step that restores the canonical:

```go
if coErr := g.runCmd(ctx, g.repoPath, "checkout", "HEAD", "--", path); coErr != nil {
    g.metrics.IncResolverFailure(quarantineFailureIO)
    slog.ErrorContext(
        ctx,
        "git-rest: restore canonical after quarantine failed",
        "path",
        path,
        "err",
        coErr.Error(),
    )
    return false
}
```

Ordering is load-bearing and must not be rearranged: the conflicted bytes are written and staged under `_conflicts/` **before** the canonical is restored, so a failure at the restore cannot lose content that the quarantine had not yet persisted. Do not move the `git rm -- <path>` step — it is what clears the unmerged entry, and the restore depends on the conflict already being cleared.

A failed restore returns `false`, exactly like the existing failure paths: the path then does not land in `quarantined` and the commit message does not claim it. This preserves today's behaviour for the failure case rather than introducing a new one.

Also update `quarantineOne`'s `// nolint:funlen` comment so its step list matches the code again — it currently reads `5 sequential IO steps (read → rm → mkdir → write → add)` and the change makes it six. Change the parenthetical to `6 sequential IO steps (read → rm → mkdir → write → add → checkout)`; leave the second explanation line as it is.

## 2. Make the restore observable in the log

Immediately after a successful restore, emit **exactly one** `slog.InfoContext` line:

```go
slog.InfoContext(ctx, "git-rest: restored canonical after quarantine", "path", path)
```

Message text and the `path` attribute are fixed — an operator greps for this line to tell a restore from a removal. Emit it once per quarantined path, not once per pull cycle. Do **not** modify the existing `git-rest: quarantined conflicted file` line or its attributes.

## 3. Invert the first assertion that encodes the defect

In `pkg/git/git_test.go`, in the `It` titled `"AC1-AC6 + AC12: Pull returns nil; 1 file quarantined; 10 resolved; commit message in fixed format"` (the Describe whose fixture sets `corruptPath = "note-00.md"`), replace the `os.IsNotExist` assertion quoted in `<context>` with:

```go
canonical, readErr := os.ReadFile(filepath.Join(workDir, corruptPath))
Expect(readErr).NotTo(HaveOccurred(),
    "the canonical must exist after a resolver-failure quarantine")
Expect(string(canonical)).To(Equal("---\nbad: : : colon: storm\n---\nlocal corrupt body\n"),
    "the canonical must be restored to the local pre-merge side after quarantine")
```

Assert the content, not merely presence — presence alone would also pass if the restore had brought back the remote side or a marker-laden copy.

The comment block immediately above that assertion also goes false, and it carries **two** stale fragments, not one: the clause `no longer at the original path` and the sentence `os.Stat of the original path returns IsNotExist.` Delete both. Leave every other assertion in that `It` unchanged — in particular the quarantine-glob, the `quarantined=[note-00.md]` commit-message assertion, and the clean-working-tree assertion must all still hold, since a canonical restored to `HEAD` content leaves the index clean.

## 3b. Invert the second assertion that encodes the defect

In `pkg/git/git_test.go`, inside the Context `"diverged, same-line conflict, resolver returns error (AC3 — quarantine path)"`, the `It` titled `"AC3: resolver-failed file is moved to _conflicts/"` asserts the canonical is absent after the pull. That assertion encodes the same defect as requirement 3 and fails once the restore lands.

Rename the `It` to `"AC3: resolver-failed file is copied to _conflicts/ and the canonical is restored"`, keep the `_conflicts/conflict.txt.*.quarantined` glob assertion above it unchanged, and replace the block quoted in `<context>` with:

```go
canonical, readErr := os.ReadFile(filepath.Join(workDir, "conflict.txt"))
Expect(readErr).NotTo(HaveOccurred(),
    "the canonical must exist after a resolver-failure quarantine")
Expect(string(canonical)).To(Equal("local content\n"),
    "the canonical must hold the local pre-merge side after a resolver-failure quarantine")
```

In this fixture the local (`HEAD`) side of `conflict.txt` is exactly `"local content\n"`, committed by `writeLocalCommit`. Leave every other assertion in that `It` and in the neighbouring `It`s (`"AC3: Pull returns nil; merge commits with file in quarantined[]"`, `"AC3: repo is clean after committed merge (on branch, no MERGE_HEAD)"`, `"AC3: increments merge outcome resolved counter exactly once"`) unchanged.

## 4. Add a focused regression spec for the restored canonical

In the same Describe (same fixture, its own `It`), add one spec asserting the whole contract in one place. Name it so it is greppable, e.g. `"AC2b: a quarantined path is restored to the local pre-merge side while the quarantine copy keeps both sides"`. Assert, after `pgLocal.Pull(ctx)` returns nil:

- the canonical exists at `filepath.Join(workDir, corruptPath)` and its bytes equal `"---\nbad: : : colon: storm\n---\nlocal corrupt body\n"`;
- exactly one file still matches `filepath.Join(workDir, "_conflicts", "note-00.*.md")`, and its content contains **both** `local corrupt body` and `remote body 0` — i.e. the conflicted bytes were preserved, not replaced by the restored side;
- `git status --porcelain` is empty, so the merge committed cleanly and left no residue;
- `.git/MERGE_HEAD` does not exist;
- the captured log contains `restored canonical after quarantine` naming `corruptPath`, and `strings.Count(logs.String(), "restored canonical after quarantine")` equals `1` — a duplicate line must fail the spec.

Wrap the pull in `captureSlogLogs()` as the neighbouring specs do, and drive it through the fixture's existing `pgLocal` — it is already built in the Describe's `BeforeEach` over `setupQuarantineFixture(11)` with `git.NewYAMLMergeResolver(workDir, metrics.NewMetrics())`, so the resolver failure is the one this change is about. Do not construct a second puller and do not modify any existing `It`.

## 5. Record the fix in the changelog

`CHANGELOG.md` has no `## Unreleased` section today. Add one at the top of the version list (immediately after the preamble, above `## v0.29.4`) containing exactly one bullet:

```
- fix: Restore the canonical path after a resolver-failure quarantine instead of deleting it. `quarantineOne` ran `git rm -- <path>` to clear the conflict and never put the path back, so every resolver-failure quarantine removed a live file from the repository while the commit message's `quarantined=[…]` read as "preserved alongside" — the canonical is now restored from `HEAD` after the conflicted bytes are written to `_conflicts/`, and a failed restore still reports rather than failing silently.
```

Do not touch any released section, and do not add a second bullet.

## 6. Self-check before finishing

Run every command in `<verification>` from the repo root and confirm each result. Then walk each requirement against the change: the canonical survives a resolver-failure quarantine holding the local pre-merge side, the quarantine copy still holds both sides, one INFO line names the path, the commit message format is unchanged, and a failed restore still returns false with an ERROR line.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT change `resolveEachPath`, `commitAndPushMerge`, `quarantineDestPath`, `ensureConflictsDir`, `nestedConflictPath`, `validateConflictPathsNotNested`, `parseMergeConflictPaths`, `resolveModifyDelete` or `classifyConflicts`.
- Do NOT change the `merge: resolved=[…] quarantined=[…]` commit-message format.
- Do NOT change the existing `git-rest: quarantined conflicted file` log line or its `path` / `dest` attributes.
- Do NOT add a metric, a gauge, a counter or a label. `pkg/metrics/metrics.go` and `mocks/metrics.go` must stay byte-identical. Reuse the existing `quarantineFailureIO` category for a failed restore.
- Do NOT add a metric name outside the `git_rest_` prefix, and do NOT add a new `quarantineFailure*` category.
- Do NOT change the `ConflictResolver` interface (`pkg/git/conflict_resolver.go`), its counterfeiter directive, `MarkerResolver` or `YAMLMergeResolver`.
- Do NOT change the public HTTP contract in `docs/api.md`. Do NOT touch `specs/`. Two `docs/` sentences go false with this change and both are corrected in-prompt; touch nothing else in `docs/`:
  - In `docs/deployment.md`, in the `**Vault-write mode**` bullet under `## Operational notes` (there is no `### Vault-write mode` heading — the label is bolded bullet text), change only the clause that reads `the puller moves the file to `_conflicts/<path>.<unix-ts>.md` and continues the merge` to `the puller copies the conflicted bytes to `_conflicts/<path>.<unix-ts>.md`, restores the canonical from `HEAD`, and continues the merge`.
  - In `docs/verifying-specs.md`, the sentence at line 168 opening `The quarantine flow manufactures exactly this shape: quarantining a file `git rm`s the original path, so that path is *deleted in HEAD* while it still exists upstream, and a later upstream edit to the canonical produces the modify/delete conflict.` no longer describes the quarantine flow. Replace **only its opening** so the shape is attributed to the recipe's own hand-built step: `The recipe below manufactures exactly this shape by hand: its `git rm` on the original path leaves that path *deleted in HEAD* while it still exists upstream, and a later upstream edit to the canonical produces the modify/delete conflict.` Leave the rest of that paragraph, and the recipe itself, untouched — the recipe's manual `git rm` step still produces the shape, so the recipe survives the change unchanged.
- The stale claims that are genuinely spec-ladder work, not yours to edit: `specs/in-progress/012-quarantine-on-conflict.md:45` (§ Desired Behavior 2 — `moved within the working tree` / `keeping both ends staged`) and `specs/in-progress/015-bug-pull-wedges-on-modify-delete-conflict.md:13`, `:24`, `:45` (the `deleted in HEAD` / `absent from HEAD's tree` premise). Both specs are superseded on this point by the change and are the operator's to amend. List them by file and line in the completion report as known-stale — do not edit them, and do not silently omit them either.
- The restore targets the canonical path only, from `HEAD`. It never touches the quarantined copy under `_conflicts/`, so spec 013's "no auto-repair / auto-restore / auto-delete of quarantined content" rule is unchanged — the operator still owns repair, and the drain is still the only removal path.
- The new failure branch (a restore that fails) is fixture-unreachable through a real merge — a `git rm` that succeeds implies `HEAD` holds the path, so the checkout succeeds — and is covered by inspection only. Do NOT invent a fixture to reach it; name it as inspection-covered in the completion report instead.
- Two existing assertions encode the defect and both MUST be inverted: the `os.IsNotExist` assertion in the `"AC1-AC6 + AC12: …"` It (requirement 3) and the `os.IsNotExist` assertion in the `"AC3: resolver-failed file is moved to _conflicts/"` It inside the Context `"diverged, same-line conflict, resolver returns error (AC3 — quarantine path)"` (requirement 3b). Every other existing `It` stays byte-identical and must still pass. The file's other `os.IsNotExist` assertions concern `.git/MERGE_HEAD`, `_conflicts/_conflicts`, or a drained path — leave them alone.
- Do NOT add a scenario file under `scenarios/`.
- Preserve the repo's documented invariants: errors wrapped with `github.com/bborbe/errors` (never `fmt.Errorf`), no `context.Background()` in `pkg/`, `log/slog` for logging (never `fmt.Print*`), timestamps from the injected `libtime` getter rather than `time.Now()`, and counters pre-initialised in `init()`.
- Interface → Constructor → Struct → Method; factory functions stay pure composition.
- Merge-level specs live in `pkg/git/git_test.go` (Ginkgo v2 + Gomega, `package git_test`) and use real `git` via `os/exec` against a temp working tree with no network.
- Do NOT run `go mod vendor`, and do NOT write `-mod=vendor` in any command — this repo does not commit `vendor/`.
</constraints>

<verification>
Run from the repo root. The daemon may run with `hideGit=true`, which masks `.git` inside the container — under that setting bare `git` commands die with `fatal: not a git repository`, so do not put one in this block. `ROOTDIR=/workspace` is the dark-factory convention for that case; this repo's `Makefile` does not itself reference `ROOTDIR`, so the prefix is inert here and harmless. Keep it.

```bash
ROOTDIR=/workspace make precommit
```

Must exit 0.

Iterative runs while implementing (use these after each meaningful change, before the final `make precommit`):

```bash
ROOTDIR=/workspace make test
go test ./pkg/git/... -mod=mod -ginkgo.focus='per-file quarantine'
go test ./pkg/git/... -mod=mod -ginkgo.focus='Pull nested quarantine guard'
go test ./pkg/git/... -mod=mod -ginkgo.focus='Quarantine backlog gauge'
```

All pass. A focused run that reports `0 of 0` specs means the focus string matched nothing — fix the focus, do not accept it.

Evidence that the change landed (each `grep -n` must print at least one match):

```bash
grep -n '"checkout", "HEAD", "--", path' pkg/git/git.go
grep -n 'restored canonical after quarantine' pkg/git/git.go
grep -n 'restore canonical after quarantine failed' pkg/git/git.go
grep -n '6 sequential IO steps' pkg/git/git.go
```

Both `docs/` corrections landed (each `grep -n` must print at least one match):

```bash
grep -n 'restores the canonical from `HEAD`, and continues the merge' docs/deployment.md
grep -n 'The recipe below manufactures exactly this shape by hand' docs/verifying-specs.md
```

The inverted assertions and the new spec are present, and both old claims are gone (each `! grep -q` must exit 0):

```bash
! grep -q 'corrupt file must not exist at original path after quarantine' pkg/git/git_test.go
! grep -q 'original conflict.txt must not exist after quarantine' pkg/git/git_test.go
! grep -q 'os.Stat of the original path returns IsNotExist' pkg/git/git_test.go
! grep -q 'no longer at the original path' pkg/git/git_test.go
grep -n 'the canonical must be restored to the local pre-merge side after quarantine' pkg/git/git_test.go
grep -n 'the canonical must hold the local pre-merge side after a resolver-failure quarantine' pkg/git/git_test.go
grep -c 'restored canonical after quarantine' pkg/git/git_test.go
```

The `grep -c` must print at least `1`.

Frozen surfaces and no-new-metric checks (each `grep -n` must print at least one match; each `! grep -q` must exit 0):

```bash
grep -n 'merge: resolved=' pkg/git/git.go
grep -n 'quarantined conflicted file' pkg/git/git.go
grep -n 'quarantineFailureIO' pkg/git/git.go
! grep -q 'git_rest_quarantine_restore' pkg/metrics/metrics.go
! grep -q 'restored_canonical' pkg/metrics/metrics.go
ls mocks/metrics.go mocks/conflict_resolver.go
```

Absence is written as `! grep -q`, never as `grep -c` — `grep -c` prints `0` and exits non-zero, so an absence assertion written that way fails the step it was meant to pass. The `ls` must list both files.

Changelog check — the section is new, and the released sections were not touched:

```bash
grep -n '^## Unreleased' CHANGELOG.md
awk '/^## /{sec=$0} /^## Unreleased/{print "first section: " sec; exit}' CHANGELOG.md
```

The first must print one match; the second must print `first section: ## Unreleased`, proving the new section is the topmost version heading.

Self-check before finishing: re-run `ROOTDIR=/workspace make precommit`, then walk each requirement above against the change — the canonical survives holding the local pre-merge side, the quarantine copy still holds both sides, one INFO line names the path, the commit-message format and the existing WARN line are unchanged, and a failed restore still returns false with an ERROR line.
</verification>
