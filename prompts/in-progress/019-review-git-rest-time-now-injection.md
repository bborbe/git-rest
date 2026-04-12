---
status: approved
created: "2026-04-12T13:29:16Z"
queued: "2026-04-12T13:49:15Z"
---

<summary>
- Five direct time calls in production code for metrics timing measurements
- Three duration fields use the standard library type instead of the project wrapper type
- Injecting a time provider makes the code testable and follows project conventions
- All direct time calls are in the git package for Prometheus observe timing
</summary>

<objective>
Replace all `time.Now()` calls in production code with injected `libtime.CurrentDateTimeGetter`. Replace `time.Duration` struct fields with `libtime.Duration`.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read coding guide before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-time-injection.md`: time injection rules, CurrentDateTimeGetter pattern

Files to read before making changes:
- `pkg/git/git.go` (~lines 124, 176, 215, 244, 281) — five time.Now() calls for metrics timing
- `pkg/puller/puller.go` (~lines 23, 32) — time.Duration in constructor and struct
- `main.go` (~line 37) — time.Duration in application struct PullInterval field
- `pkg/factory/factory.go` (~line 18) — CreateGitClient constructor call
</context>

<requirements>

## 1. Inject CurrentDateTimeGetter into git package

In `pkg/git/git.go`, add `currentDateTimeGetter libtime.CurrentDateTimeGetter` to the `git` struct fields (~line 57). Update the constructor `New` (~line 50) to accept and store it as a parameter.

## 2. Replace time.Now() in git methods

Replace all five `start := time.Now()` calls (~lines 124, 176, 215, 244, 281) with:
```go
start := g.currentDateTimeGetter.Now()
```

For `time.Since(start)`, use:
```go
time.Since(time.Time(start))
```

## 3. Create CurrentDateTimeGetter in main.go and pass through factory

In `main.go`, create `libtime.NewCurrentDateTimeGetter()` and pass it to factory. Never create it inside the factory — the time injection guide requires creation at the composition root.

Update `createGitClient` (~line 57) to pass the getter:
```go
return factory.CreateGitClient(a.Repo, metrics.NewMetrics(), libtime.NewCurrentDateTimeGetter()), nil
```

Update `pkg/factory/factory.go` `CreateGitClient` to accept `currentDateTimeGetter libtime.CurrentDateTimeGetter` as a parameter and pass it to `git.New`.

## 4. Replace time.Duration with libtime.Duration

In `main.go` (~line 37), change `PullInterval time.Duration` to `PullInterval libtime.Duration`.

In `pkg/puller/puller.go` (~lines 23, 32), change the constructor parameter and struct field from `time.Duration` to `libtime.Duration`. Update the `time.NewTicker` call to use `time.Duration(p.interval)`.

## 5. Update tests

Update any tests that construct `git` or `puller` structs to pass the new dependencies. Use `libtime.NewCurrentDateTimeGetter()` in tests.

</requirements>

<constraints>
- Only change files in `.`
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Import `libtime "github.com/bborbe/time"` — use the existing import alias if already present
- Never create CurrentDateTimeGetter inside factory — always receive as parameter
</constraints>

<verification>
make precommit
</verification>
