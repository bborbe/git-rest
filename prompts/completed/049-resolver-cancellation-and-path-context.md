---
status: completed
summary: Add ctx.Done() cancellation guard to yamlMergeResolver.Resolve and markerResolver.Resolve loops, and include failing path name in YAML resolver error
execution_id: repo-exec-049-resolver-cancellation-and-path-context
dark-factory-version: v0.193.0
created: "2026-08-09T19:45:00Z"
queued: "2026-08-09T19:58:57Z"
started: "2026-08-09T19:59:52Z"
completed: "2026-08-09T20:01:19Z"
---

<summary>
- Two conflict resolvers loop over paths doing real work per path
- Neither stops when the caller cancels, unlike the equivalent loop elsewhere in the package
- One of them also reports failure without saying which path failed
- The package already contains the exact pattern to copy
- No change to the success path or to resolution semantics
</summary>

<objective>
Both resolver loops stop promptly on cancellation, matching the existing convention in the same package, and the YAML resolver's error names the path that failed.
</objective>

<context>
Read `CLAUDE.md` for project conventions if present (this repo has none; conventions come from sibling bborbe repos and the code below).

Files to read before making changes (read ALL first):
- `pkg/git/git.go` — find `resolveEachPath`. Its `for _, path := range conflictPaths` loop opens with a non-blocking `select` on `ctx.Done()`. **This is the convention to copy.** Note its guard `return`s the accumulated slices rather than an error, because of its signature — do not copy that part blindly.
- `pkg/git/yaml_merge_resolver.go` — `yamlMergeResolver.Resolve`, the `for _, path := range conflictedPaths` loop. `resolveOne` does file read + YAML parse + write per path.
- `pkg/git/conflict_resolver.go` — `markerResolver.Resolve`, the same loop shape, spawning `exec.CommandContext(ctx, "git", "add", ...)` per path.

Both `Resolve` methods return a bare `error`.
</context>

<requirements>
1. In `pkg/git/yaml_merge_resolver.go`, add a non-blocking cancellation guard at the very top of the `for _, path := range conflictedPaths` loop body in `Resolve`:
   ```go
   select {
   case <-ctx.Done():
       return errors.Wrap(ctx, ctx.Err(), "context cancelled")
   default:
   }
   ```
2. Add the identical guard at the top of the loop body in `markerResolver.Resolve` in `pkg/git/conflict_resolver.go`.
3. In `pkg/git/yaml_merge_resolver.go`, change the loop's `return err` to `return errors.Wrapf(ctx, err, "resolve conflict %q", path)` so the failing path is identifiable. `resolveOne`'s internal wraps do not carry the path.
4. Do NOT change `resolveEachPath` in `pkg/git/git.go` — it already has its guard and its different return shape is correct for its signature.
5. Do NOT change `resolveOne` or any of its internal `errors.Wrap` calls.
6. Confirm `github.com/bborbe/errors` is imported in both edited files; add the import only if it is genuinely missing.
7. Add a bullet under `## Unreleased` in `CHANGELOG.md` using a conventional prefix. If no `## Unreleased` section exists, create it directly above the newest released section without touching that section.
</requirements>

<constraints>
- Only change `pkg/git/yaml_merge_resolver.go`, `pkg/git/conflict_resolver.go`, and `CHANGELOG.md`
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Use `errors.Wrap`/`errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf` or a bare `return err`
- Preserve the documented abort-on-first-failure semantics of `yamlMergeResolver.Resolve`; the guard must not change which paths get attempted on the success path
</constraints>

<verification>
make precommit
</verification>
