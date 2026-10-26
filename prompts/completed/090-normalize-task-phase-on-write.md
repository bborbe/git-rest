---
status: completed
summary: Added normalizeTaskPhase and applied it to all four task-file write paths (WriteFile/WriteFileIfAbsent funnel plus YAML and vault-api conflict resolvers), with unit, integration, CHANGELOG and API docs coverage.
execution_id: git-rest-exec-090-normalize-task-phase-on-write
dark-factory-version: v0.198.7
created: "2026-10-26T21:52:00Z"
queued: "2026-10-26T21:57:36Z"
started: "2026-10-26T22:05:03Z"
completed: "2026-10-26T22:08:33Z"
---

<summary>
- git-rest normalizes a documented phase alias when it writes a task file
- A write carrying `phase: in_progress` lands `phase: execution` on disk, matching what vault-cli already writes
- The normalization applies on every task-file write path: overwrite, create, and all three conflict-resolver writes
- A non-task file, a file with no frontmatter, a quarantined copy, and every other phase value pass through byte-identical
- The two writers stop disagreeing about what a phase may be
</summary>

<objective>
Make `git-rest` stop persisting the status value `in_progress` into a task file's `phase:` field. It must normalize that documented alias to the canonical `execution` on every task-file write, using the same alias map as `vault-cli`'s `NormalizeTaskPhase` (`in_progress` → `execution`) but not its unknown-value rejection, so a write through either writer produces the same phase vocabulary.
</objective>

<context>
Read `CLAUDE.md` for project conventions and `docs/dod.md` for the Definition of Done.

Read `pkg/git/git.go` — `WriteFile` (line ~708), `WriteFileIfAbsent` (~736), `writeFileLocked` (~761), the existing `hasFrontmatter` guard (~773), `hasFrontmatter` (~846), and `isTaskFilePath` (~855). Note that `writeFileLocked` receives `fileExists`, and the existing frontmatter guard is gated on it.

Read `pkg/git/yaml_merge_resolver.go` — `splitFrontmatter` (~394) and the two `os.WriteFile` calls (~136 for `"write merged file"`, ~839 for `"write scalar-resolved file"`).

Read `pkg/git/vault_api_merge_resolver.go` — `writeMerged` (~183) and its `os.WriteFile` (~188). `main.go`'s `selectResolver` (~274) makes this resolver primary when `VAULT_API_URL` is set, with the YAML resolver as its per-path fallback.

Read `pkg/git/export_test.go` — the established way this package exposes unexported internals to its external Ginkgo suite (`RetryPushForTest`, `PushBackoffForTest`, …).

The canonical vocabulary lives in `vault-cli`'s `pkg/domain/task_phase.go` (`NormalizeTaskPhase`): canonical values are `todo`, `planning`, `execution`, `ai_review`, `human_review`, `done`, and the only alias mapping is `in_progress` → `execution`.
</context>

<requirements>
1. Add `pkg/git/task_phase.go` with a single unexported function:

   ```go
   // normalizeTaskPhase rewrites the documented `in_progress` phase alias to the
   // canonical `execution` inside a task file's frontmatter. It returns content
   // byte-identical when path is not a task file, when there is no frontmatter
   // block, or when no line carries the alias.
   func normalizeTaskPhase(path string, content []byte) []byte
   ```

   Behaviour:
   - Return `content` unchanged when `isTaskFilePath(path)` is false.
   - Use the existing `splitFrontmatter` only to decide whether a frontmatter block exists (`ok`). It returns the frontmatter and body without the delimiter lines and joins on `\n`, so **do not reassemble** with a hard-coded `"---\n"`: splice the matched line into the original bytes instead, so the delimiter line endings survive.
   - Within the frontmatter block only, find a **top-level** line (zero leading whitespace) whose key is `phase:`. An indented `phase:` nested under another key or inside a list is left untouched — vault-cli reads and writes flat frontmatter.
   - Take the value after the colon, trim surrounding spaces and tabs, strip one pair of surrounding single or double quotes, and compare to `in_progress`. On a match, rewrite the **first** matching top-level `phase:` line as `phase: execution`, preserving a trailing `\r` when the original line had one. On no match, leave the line byte-identical.
   - Leave `status:` and every other line untouched — `in_progress` is also a valid **status** value, so a whole-file string replace would corrupt `status: in_progress`.
   - If no line matched, the returned bytes must equal the input bytes exactly.

2. Call `normalizeTaskPhase` at all four task-file write sites, before the bytes reach disk — one inside `writeFileLocked` (the shared funnel for both entry points, `WriteFile` and `WriteFileIfAbsent`) plus the three conflict-resolver writes:
   - `writeFileLocked` — once, near the top, so both entry points are covered. It must run **unconditionally**, not inside the `fileExists &&` branch: the create path currently skips the guard entirely. Assign the result back to `content` **before** the `bytes.Equal(existing, content)` unchanged-content check (~786), so a repeat of an already-normalized write still short-circuits instead of re-committing.
   - `yaml_merge_resolver.go` — before the `os.WriteFile` at ~136 and before the one at ~839.
   - `vault_api_merge_resolver.go` — before the `os.WriteFile` in `writeMerged` (~188).

3. Add tests in `pkg/git/task_phase_test.go` as `package git_test` (external, joining the suite bootstrapped in `pkg/git/git_suite_test.go`), using Ginkgo v2 / Gomega with `DescribeTable` / `Entry`. `normalizeTaskPhase` is unexported, so expose it for tests the way this package already does — add to `pkg/git/export_test.go`:

   ```go
   // NormalizeTaskPhaseForTest exposes normalizeTaskPhase to the external test
   // package. Test builds only.
   var NormalizeTaskPhaseForTest = normalizeTaskPhase
   ```

   Do NOT put a Ginkgo `Describe` in a `package git` file: the only `RunSpecs` bootstrap in `pkg/git` lives in `package git_test`, so specs registered from `package git` never execute while `go test` still exits 0 — a false green. Every existing `package git` test file here (`claim_refs_internal_test.go`, `frontmatter_internal_test.go`, `yaml_merge_scalar_internal_test.go`, `resolve_conflict_merge_test.go`) uses stdlib `testing` for that reason.

   Cases:
   - a task file (`tasks/x.md`) with `phase: in_progress` becomes `phase: execution`
   - the same for `25 Tasks/x.md`
   - a quoted value `phase: "in_progress"` becomes `phase: execution`
   - `phase: planning` and `phase: execution` are unchanged
   - `status: in_progress` is unchanged when no `phase:` alias is present
   - a non-task path (`notes/x.md`) is unchanged even with the alias present
   - content with no frontmatter is unchanged
   - an indented `phase: in_progress` (nested under another key) is unchanged
   - a CRLF document carrying `phase: in_progress` becomes `phase: execution` and keeps its `\r\n` line endings
   - an already-canonical document is returned byte-identical

4. Add one integration-level test per write path, reusing the existing test harness in `pkg/git/git_test.go` and the resolver test files: a write through `WriteFile`, through `WriteFileIfAbsent`, through the YAML resolver, and through the vault-api resolver each lands `phase: execution`. Cover each resolver through its own resolver API. Both fixtures are expressible with the existing harnesses (`setupYAMLConflictRepo` / `setupHandWrittenConflict` + `parseFrontmatterOf` in `pkg/git/yaml_merge_resolver_test.go`; `mocks.FakeVaultAPIMergeClient.MergeReturns` in `pkg/git/vault_api_merge_resolver_test.go`), so a missing per-path integration test is a blocker, not a comment.

5. Add a `## Unreleased` section at the top of `CHANGELOG.md` (it currently starts at `## v0.36.3`) with one bullet: git-rest now normalizes the `in_progress` phase alias to `execution` on every task-file write path — overwrite, create, and all three conflict-resolver writes — matching vault-cli's `NormalizeTaskPhase` alias map; any other phase value passes through unchanged.

   Also append one sentence to `docs/api.md` §Files, beside the existing task-file `422` paragraph (~line 81): a task file's top-level `phase:` line is normalized on every write — the documented alias `phase: in_progress` is stored as `phase: execution`, matching vault-cli's `NormalizeTaskPhase`; no other phase value and no `status:` line is rewritten.

6. Self-check before finishing: re-run `<verification>` and confirm it passes; walk each requirement above against the change and report any unmet one as a blocker.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT reject or validate unknown phase values. Only the documented `in_progress` alias is normalized; every other value is left untouched. A new error sentinel and its `pkg/handler/files_post.go` mapping are out of scope.
- Do NOT change the existing `hasFrontmatter` behaviour or its `fileExists` gating — the new normalization is additive and independent.
- Do NOT normalize the quarantine write in `quarantineOne` (`pkg/git/git.go`, the `os.WriteFile(destAbs, raw, …)` call around ~1863): a quarantined copy preserves one side's bytes verbatim. `_conflicts/25 Tasks/x.md.<ts>.md` does match `isTaskFilePath`, so this is a deliberate exclusion, not an oversight — `_conflicts/` is operator-owned and must not be rewritten.
- Existing tests must still pass.
- Exported symbols need doc comments; new code targets ≥80% coverage.
- Use `github.com/bborbe/errors` for any error handling; this change is expected to need none.
</constraints>

<verification>
Run `make precommit` — must pass.
</verification>
