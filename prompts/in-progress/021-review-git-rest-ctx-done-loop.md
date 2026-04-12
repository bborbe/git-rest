---
status: approved
created: "2026-04-12T13:29:16Z"
queued: "2026-04-12T13:49:20Z"
---

<summary>
- ListFiles iterates over git ls-files output without checking context cancellation
- Large repos with thousands of files would ignore cancellation during iteration
- Adding a ctx.Done() select in the loop ensures timely cancellation
</summary>

<objective>
Add `ctx.Done()` cancellation check in the `ListFiles` range loop to respect context cancellation for large repositories.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read coding guide before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-context-cancellation-in-loops.md`: context cancellation patterns in loops

Files to read before making changes:
- `pkg/git/git.go` (~line 259) — range loop over strings.Split without ctx.Done()
</context>

<requirements>

## 1. Add ctx.Done() check in ListFiles loop

In `pkg/git/git.go`, find the `ListFiles` method's range loop over `strings.Split(...)` (~line 259). Add a select statement at the top of the loop body:

```go
for _, line := range lines {
    select {
    case <-ctx.Done():
        return nil, errors.Wrap(ctx, ctx.Err(), "list files cancelled")
    default:
    }
    // existing logic...
}
```

</requirements>

<constraints>
- Only change files in `.`
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Use `errors.Wrap` from `github.com/bborbe/errors`
</constraints>

<verification>
make precommit
</verification>
