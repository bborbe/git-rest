---
status: completed
summary: Replaced 10 errors.Wrapf calls without format verbs with errors.Wrap in pkg/git/git.go
container: git-rest-008-review-git-rest-errors-wrap-not-wrapf
dark-factory-version: v0.108.0-dirty
created: "2026-04-11T00:00:00Z"
queued: "2026-04-11T21:05:22Z"
started: "2026-04-11T21:05:24Z"
completed: "2026-04-11T21:10:22Z"
---

<summary>
- The github.com/bborbe/errors package provides both errors.Wrap (plain message) and errors.Wrapf (format string with %v/%s verbs)
- pkg/git/git.go contains 10 calls to errors.Wrapf with plain string messages and no format arguments
- Using Wrapf without format verbs is incorrect usage of the API — Wrap should be used instead
- This is a purely mechanical substitution with no behavioral change
</summary>

<objective>
Replace all `errors.Wrapf(ctx, err, "plain message")` calls that contain no format verbs with `errors.Wrap(ctx, err, "plain message")` in `pkg/git/git.go`.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Files to read before making changes (read ALL first):
- `pkg/git/git.go`: the 10 Wrapf calls to replace (lines 122, 154, 159, 175, 194, 199, 215, 249, 285, 299)
</context>

<requirements>
1. In `pkg/git/git.go`, replace the following 10 `errors.Wrapf` calls (which have no `%` format verbs) with `errors.Wrap`:

   - Line 122: `errors.Wrapf(ctx, err, "validate path")` → `errors.Wrap(ctx, err, "validate path")`
   - Line 154: `errors.Wrapf(ctx, err, "git commit")` → `errors.Wrap(ctx, err, "git commit")`
   - Line 159: `errors.Wrapf(ctx, err, "git push")` → `errors.Wrap(ctx, err, "git push")`
   - Line 175: `errors.Wrapf(ctx, err, "validate path")` → `errors.Wrap(ctx, err, "validate path")`
   - Line 194: `errors.Wrapf(ctx, err, "git commit")` → `errors.Wrap(ctx, err, "git commit")`
   - Line 199: `errors.Wrapf(ctx, err, "git push")` → `errors.Wrap(ctx, err, "git push")`
   - Line 215: `errors.Wrapf(ctx, err, "validate path")` → `errors.Wrap(ctx, err, "validate path")`
   - Line 249: `errors.Wrapf(ctx, err, "git ls-files")` → `errors.Wrap(ctx, err, "git ls-files")`
   - Line 285: `errors.Wrapf(ctx, err, "git pull")` → `errors.Wrap(ctx, err, "git pull")`
   - Line 299: `errors.Wrapf(ctx, err, "git status --porcelain")` → `errors.Wrap(ctx, err, "git status --porcelain")`

   Keep all existing `errors.Wrapf` calls that DO contain format verbs (lines 93, 107, 134, 139, 144, 188, 229, 264) unchanged.

2. No test changes are needed — this is a pure API-correctness fix with no behavioral change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Do NOT touch any Wrapf call that contains a % format verb
- Do NOT use fmt.Errorf under any circumstances
</constraints>

<verification>
make precommit
</verification>
