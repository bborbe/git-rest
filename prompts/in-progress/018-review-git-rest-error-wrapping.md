---
status: failed
container: git-rest-018-review-git-rest-error-wrapping
dark-factory-version: v0.108.0-dirty
created: "2026-04-12T13:29:16Z"
queued: "2026-04-12T13:49:12Z"
started: "2026-04-12T13:49:14Z"
completed: "2026-04-12T13:53:28Z"
lastFailReason: 'validate completion report: completion report status: partial'
---

<summary>
- Several error wrapping calls use the format variant without any format verbs
- Using Wrap instead of Wrapf when no formatting is needed is cleaner and more explicit
- Consistent error wrapping improves debuggability and follows project conventions
- No functional behavior changes, only code quality improvements
</summary>

<objective>
Replace all `errors.Wrapf` calls that have no `%` format verbs with `errors.Wrap` for consistency with the error wrapping guide.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read coding guide before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-error-wrapping-guide.md`: error wrapping rules, Wrap vs Wrapf

Files to read before making changes:
- `main.go` (~line 48) — Wrapf without format verb
- `pkg/git/git.go` (~lines 60-90) — Wrapf without format verbs in validatePath
</context>

<requirements>

## 1. Fix Wrapf without format verb in main.go

In `createGitClient` method (~line 48), find `errors.Wrapf(ctx, err, "create git client failed")` — the message has no `%` format verb. Replace with `errors.Wrap(ctx, err, "create git client failed")`.

## 2. Fix Wrapf without format verbs in git.go

In `validatePath` function (~lines 64-91), find all `errors.Wrapf(ctx, ErrInvalidPath, "message")` calls where the message contains no `%` format verb. Replace each with `errors.Wrap(ctx, ErrInvalidPath, "message")`.

There should be approximately 6 occurrences.

</requirements>

<constraints>
- Only change files in `.`
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Use `errors.Wrap`/`errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf` or bare `return err`
</constraints>

<verification>
make precommit
</verification>
