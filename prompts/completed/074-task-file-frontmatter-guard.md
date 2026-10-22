---
status: completed
summary: Added an ErrMissingFrontmatter guard in the shared write path that refuses overwriting existing 25 Tasks/tasks files with frontmatter-less content, mapped it to HTTP 422, and covered it with unit, helper, and handler tests plus docs and changelog.
execution_id: git-rest-exec-074-task-file-frontmatter-guard
dark-factory-version: v0.198.7
created: "2026-10-22T13:09:12Z"
queued: "2026-10-22T13:15:51Z"
started: "2026-10-22T13:24:05Z"
completed: "2026-10-22T13:27:52Z"
branch: dark-factory/074-task-file-frontmatter-guard
---

# Refuse a task-file overwrite that would drop its frontmatter

<summary>
- A write that would replace an existing task file with content carrying no frontmatter is now refused
- The existing file is left byte-identical; nothing is created, staged, committed or pushed
- The refusal is reported as a new distinguishable error, so callers can tell it apart from a bad path or an occupied path
- The HTTP API answers that refusal with a dedicated client-error status instead of a generic server error
- New files in a task directory are still written normally
- Overwrites that carry frontmatter are still written normally
- Writes outside a task directory are unchanged, overwrites included
- The guard sits in the shared write path, so both write entry points (create and upsert) are covered, not only the HTTP POST route
- The API reference and the changelog describe the new behaviour
</summary>

<objective>
Stop git-rest from destroying a task file's frontmatter. Two production incidents replaced task files with a bare error string and with zero bytes, and autocommit made the loss permanent within seconds. A task file without its frontmatter is no longer a task file, so an overwrite that drops it must be refused before anything touches disk.
</objective>

<context>
Read `CLAUDE.md` for repo standards and `docs/dod.md` for the definition of done this change must satisfy.

Pattern references — read these before writing any code:
- `pkg/git/git.go`: `writeFileLocked` (the chokepoint this change hooks into), its two callers `WriteFile` and `WriteFileIfAbsent`, the sentinel block at the top of the file (`ErrNotFound`, `ErrInvalidPath`, `ErrFileExists`), and `validatePath` for the path-component comparison style already used in this file.
- `pkg/git/claim_refs.go`: `ErrRefConflict` / `ErrInvalidRefName` — the second sentinel exemplar, including how they are declared and how they are wrapped with `errors.Wrapf`.
- `pkg/git/git_test.go`: `initRepo()`, `noopMetrics`, and the `WriteFileIfAbsent` case "returns ErrFileExists and preserves content when the path is occupied" — the shape for asserting bytes-unchanged plus an unchanged commit count on the path.
- `pkg/git/claim_refs_internal_test.go` and the `DescribeTable` blocks in `pkg/git/yaml_merge_resolver_test.go`: internal `package git` test-file and table-test conventions.
- `pkg/handler/files_post.go`: the existing `errors.Is` chain and the `writeFile` helper that was extracted to keep the handler inside the linter budget.
- `pkg/handler/refs_put.go`: `mapRefWriteError` — the extracted sentinel-to-status helper this change should mirror.
- `pkg/handler/files_post_test.go`: `mocks.FakeGit`, `httptest`, and the `libhttp.ErrorWithStatusCode` assertion shape.
- `docs/api.md` § Write File, and `CHANGELOG.md` (no `## Unreleased` heading exists today; the newest heading is `## v0.33.1`).
</context>

<requirements>

1. **Sentinel error.** In `pkg/git/git.go`, declare an exported sentinel alongside `ErrInvalidPath` and `ErrFileExists`:
   - name: `ErrMissingFrontmatter`
   - declared with `stderrors.New(...)` (the file already aliases `errors` to `stderrors`); message lowercase, no path in it
   - doc comment in the style of its neighbours, stating what it means and that callers detect it via `errors.Is`

2. **Guard placement.** Add the refusal at the very top of `writeFileLocked`, before the `os.MkdirAll` call, and update that function's doc comment — it currently says `fileExists only selects the commit message ("create" vs "update")`, which stops being true once the guard reads it. `writeFileLocked` is the single chokepoint both `WriteFile` and `WriteFileIfAbsent` pass through, and it already receives `fileExists`, so no new parameter, no new plumbing, and no second check inside either caller.

3. **Refusal condition.** Refuse only when all three hold:
   - `fileExists` is true
   - the parent directory element of `path` is `25 Tasks` or `tasks`
   - `hasFrontmatter(content)` is false

   Compare the directory as a whole element (the base of `filepath.Dir(path)`), never a `strings.HasPrefix` on the full path: `25 Tasks/…` must match, `25 Tasks Archive/…` must not. Only the immediate parent element is compared — a file nested deeper (e.g. `tasks/sub/x.md`) is out of scope for this change. Do not add a file-extension condition or any other extra condition.

   On refusal: increment `g.metrics.IncGitOperationError("write_file")` first (every other early return in `writeFileLocked` does), then return `errors.Wrapf` over `ErrMissingFrontmatter` with a message that names `path`. Nothing else happens — no `MkdirAll`, no `os.WriteFile`, no `git add`, no commit, no push — so the existing file stays byte-identical.

4. **Frontmatter contract.** A body carries frontmatter iff its first line is exactly `---` and a later line is exactly `---` (the closing delimiter). A `\r` before the line break is tolerated, so CRLF files pass. Everything else lacks frontmatter: an empty body, a body whose first line is anything else, a body with blank or whitespace lines before the opening `---`, a body with an opening `---` and no closing line, and a body closed with `...` instead of `---`.

   Keep this rationale in the code comment: a lone opening delimiter is not a frontmatter block, so requiring the closing line stops the guard from admitting a write that would still destroy the file.

5. **Helpers.** Add two unexported helpers in `pkg/git/git.go` — `hasFrontmatter(content []byte) bool` and `isTaskFilePath(path string) bool` — so `writeFileLocked` stays readable and each rule is testable on its own. `hasFrontmatter` must delegate to the existing `splitFrontmatter` in `pkg/git/yaml_merge_resolver.go` (same package: `_, _, ok := splitFrontmatter(string(content)); return ok`) rather than re-implement the delimiter contract — that contract is already load-bearing for the conflict resolver, and two copies will drift. Put the requirement-4 rationale in `hasFrontmatter`'s doc comment. Keep both helpers pure: no I/O, no `ctx`, no logging.

6. **HTTP mapping.** In `pkg/handler/files_post.go`, extract the existing `errors.Is` chain into `mapWriteFileError(ctx context.Context, err error) error`, modelled on `mapRefWriteError` in `pkg/handler/refs_put.go`:
   - `git.ErrInvalidPath` → `http.StatusBadRequest` (400), unchanged
   - `git.ErrFileExists` → `http.StatusConflict` (409), unchanged
   - `git.ErrMissingFrontmatter` → `http.StatusUnprocessableEntity` (422), new
   - anything else → `errors.Wrap(ctx, err, "write file")`

   The handler calls the helper from its single write-error branch, so `NewFilesPostHandler` keeps its current cognitive complexity (the linter budget is `gocognit: min-complexity: 20`). The `*http.MaxBytesError` branch stays where it is — it is a body-read failure, not a write failure. 422 rather than 400 or 409 because the path is valid and nothing is in conflict: the request is syntactically fine, its content is simply unacceptable for that target.

7. **`pkg/git` tests.** In `pkg/git/git_test.go`, add a `Context` under the existing `Describe("Git", …)` so `workDir`, `g`, `ctx` and the `initRepo()` / `cleanup()` fixtures come from its `BeforeEach`. Use `DescribeTable` / `Entry` for the refusal cases — six near-identical `It` blocks are unreadable and drift (note: `dupl` is enabled in `.golangci.yml` but excluded for `_test.go`, so this is a readability choice, not a lint requirement). Every refusal case asserts all four: `errors.Is(err, git.ErrMissingFrontmatter)` is true, the message contains the path, the bytes read back from disk equal the original bytes exactly, and the commit count on that path is still one (mirroring the existing `ErrFileExists` case).

   Cases:
   - existing `25 Tasks/<file>.md` (frontmatter written first), overwritten with a frontmatter-less body such as `[]byte("error: something went wrong")` → refused, bytes unchanged
   - existing `tasks/<file>.md`, same → refused, bytes unchanged
   - existing `tasks/<file>.md`, overwritten with an empty body → refused, bytes unchanged
   - positive control: frontmatter-bearing body over the same existing `25 Tasks/<file>.md` → succeeds, bytes equal the new body
   - positive control: frontmatter-less body to a NEW path in `tasks/` → succeeds, bytes equal the new body
   - positive control: create `README.md` at the repo root first (the `initRepo()` fixture has no files besides the empty init commit), then overwrite it with a frontmatter-less body → succeeds, bytes equal the new body (assert the second commit is an update, so the case cannot silently degrade into a create)

8. **Helper table test.** Add a `package git` internal test file (e.g. `pkg/git/frontmatter_internal_test.go`, plain `testing` style like `claim_refs_internal_test.go`) with a table over both helpers: LF and CRLF frontmatter accepted; empty body, bare error string, leading blank line, opening `---` with no closing line, and `...`-terminated body all rejected; `tasks/x.md` and `25 Tasks/x.md` true; `README.md`, `25 Tasks Archive/x.md` and `tasks/sub/x.md` false.

9. **Handler test.** In `pkg/handler/files_post_test.go`, add a `Context` where `fakeGit.WriteFileReturns(git.ErrMissingFrontmatter)` and the POST returns a `libhttp.ErrorWithStatusCode` whose `StatusCode()` is `http.StatusUnprocessableEntity` (422) — the same assertion shape as the existing 409 and 400 cases.

10. **Docs.** In `docs/api.md` § Write File, add a `422` row to the status table and a short paragraph stating the guard: an overwrite of an existing file whose parent directory is `25 Tasks` or `tasks` is refused when the body has no `---` frontmatter block, and the existing file is left untouched. The same table is already missing a `409` row for the existing `create_only`-on-an-occupied-path case (`pkg/handler/files_post.go`) — add that row too while you are there. The endpoint table row carries no status codes — leave it alone and do not invent a status column.

11. **Changelog.** In `CHANGELOG.md`, create `## Unreleased` directly above `## v0.33.1` and add one `- fix:` bullet describing the refusal, the affected directory names, the untouched-file guarantee, and the new 422. The bullet must contain the word `frontmatter` — the verification below greps for it.

12. **Self-check.** Before finishing, re-run `<verification>`, then walk requirements 1–11 against the change one by one.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- `github.com/bborbe/errors` only; no `fmt.Errorf`
- Do not run `go mod vendor`; do not write `-mod=vendor` anywhere. `CLAUDE.md`'s "vendored dependencies (`-mod=vendor`)" line is stale — the `Makefile`'s `ensure` target runs `rm -rf vendor` and every target passes `-mod=mod`
- Anchor by function/type name, never by line number
- Repo-relative paths only
- No `context.Background()` in production code under `pkg/` — the guard takes the caller's `ctx`; test files may use it as the existing tests do
- New logging uses `log/slog` only; this change needs no new log line, and must add no `fmt.Print*` and no `glog`
- The `git.Git` interface does not change, so the regenerated mock must come out byte-identical — do not hand-edit `mocks/`, and do not run `go generate` by hand (`make precommit`'s `generate` target already runs it)
- No new Prometheus metric: reuse the existing `IncGitOperationError("write_file")` counter
- Interface → Constructor → Struct → Method: the new helpers are unexported functions in `pkg/git/git.go`, not new types
- Scope: the guard covers existing files whose immediate parent directory is `25 Tasks` or `tasks`. Do not widen it to other directories, to deeper nesting, or to `DeleteFile`. The resolver and quarantine writes (`os.WriteFile` in `pkg/git/yaml_merge_resolver.go` and in the quarantine move in `pkg/git/git.go`) bypass `writeFileLocked` and stay out of scope — if either is the incident's writer, that is a separate prompt
- `docs/dod.md` is the validation prompt for this change — exported declarations carry doc comments, new behaviour has tests, and the changelog is updated
</constraints>

<verification>
Run from the repo root. `ROOTDIR=/workspace` is the dark-factory convention and is harmless here (this repo's `Makefile` does not reference it). Do not put bare `git` commands in this block.

```
ROOTDIR=/workspace make precommit
```

Must pass.

Then confirm the change landed where it belongs:

```
grep -q 'ErrMissingFrontmatter' pkg/git/git.go &&
grep -q 'StatusUnprocessableEntity' pkg/handler/files_post.go &&
grep -q '| `422` |' docs/api.md &&
awk '/^## /{sec=$0} /frontmatter/{print "sits under: " sec; exit}' CHANGELOG.md \
  | grep -q 'sits under: ## Unreleased'
```

Every line must pass. The block is chained so a missing sentinel, a missing status mapping, a missing docs row or a CHANGELOG bullet filed under a released heading each fails the whole block — an unchained trailing `awk` would exit 0 regardless and the check would pass on a broken change.

Self-check: re-run `ROOTDIR=/workspace make precommit` before declaring done, then walk requirements 1–11 against the change — the refusal fires for both task directory names and not for a new path or a non-task path, the file bytes survive a refusal, the 422 reaches the HTTP client, and no writer route lost its guard.
</verification>
