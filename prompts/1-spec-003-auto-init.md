---
spec: ["003"]
status: created
created: "2026-04-12T17:47:00Z"
---

<summary>
- Starting git-rest with a non-existent repo path and no remote URL creates a local git repository automatically
- Parent directories are created if they do not exist
- Starting with an existing repository (with or without remote) works identically to the current version
- If the repo path exists as a regular file instead of a directory, startup fails with a clear error
- If git init fails (e.g. permissions), startup fails with a clear error
</summary>

<objective>
Add local repository auto-initialization to the bootstrap phase so git-rest can operate without a remote URL. When no remote is configured and the repo path has no `.git` directory, git-rest creates parent directories and runs `git init` before starting the HTTP server.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read coding guides before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-error-wrapping-guide.md`: error wrapping rules

Files to read before making changes (read ALL first):
- `main.go` — application struct (~line 34), `Run` method (~line 49), `bootstrap` method (~line 67), `cloneIfNeeded` (~line 77)
- `pkg/git/git.go` — Git interface (~line 48), git struct, `New` constructor (~line 60)
</context>

<requirements>

## 1. Add Init method to Git interface

Add to the `Git` interface in `pkg/git/git.go` (~line 48):
```go
Init(ctx context.Context) error
```

Implement on the `git` struct:
```go
func (g *git) Init(ctx context.Context) error {
    g.mu.Lock()
    defer g.mu.Unlock()
    start := g.currentDateTimeGetter.Now()
    defer func() { g.metrics.ObserveGitOperation("init", time.Since(time.Time(start)).Seconds()) }()
    return g.runCmd(ctx, g.repoPath, "init")
}
```

Note: `Init` runs in `g.repoPath` (the directory must exist before calling Init).

## 2. Add initIfNeeded method to main.go

In `main.go`, add a new method `initIfNeeded` to the `application` struct. This handles the case where no remote URL is configured:

```go
func (a *application) initIfNeeded(ctx context.Context) error {
    if a.GitRemoteURL != "" {
        return nil // remote URL set, clone handles this (spec 002)
    }

    gitDir := filepath.Join(a.Repo, ".git")
    if _, err := os.Stat(gitDir); err == nil {
        return nil // already a git repo
    }

    // Check if path exists as a file (not directory)
    if info, err := os.Stat(a.Repo); err == nil && !info.IsDir() {
        return errors.Errorf(ctx, "repo path %s exists but is not a directory", a.Repo)
    }

    // Create directory (and parents) if needed
    if err := os.MkdirAll(a.Repo, 0o750); err != nil {
        return errors.Wrapf(ctx, err, "create repo directory %s", a.Repo)
    }

    tmpGit := factory.CreateGitClient(
        a.Repo,
        metrics.NewMetrics(),
        libtime.NewCurrentDateTime(),
        a.GitSSHKey,
    )
    if err := tmpGit.Init(ctx); err != nil {
        return errors.Wrapf(ctx, err, "git init %s", a.Repo)
    }

    return nil
}
```

## 3. Call initIfNeeded in bootstrap

In the `bootstrap` method (~line 67), call `initIfNeeded` after `cloneIfNeeded`:

```go
func (a *application) bootstrap(ctx context.Context) error {
    if err := a.cloneIfNeeded(ctx); err != nil {
        return errors.Wrap(ctx, err, "clone if needed")
    }
    if err := a.initIfNeeded(ctx); err != nil {
        return errors.Wrap(ctx, err, "init if needed")
    }
    if err := a.configureUserIfSet(ctx); err != nil {
        return errors.Wrap(ctx, err, "configure user if set")
    }
    return nil
}
```

Order matters: clone first (if remote URL set), then init (if no remote URL), then configure user.

## 4. Update mock for Git interface

Run `go generate ./...` to regenerate the mock with the new `Init` method.

## 5. Add tests for Init method

In `pkg/git/git_test.go`, add a `Describe("Init")` block:

- It initializes a new git repo in an empty directory
- After init, `.git` directory exists
- After init, `git status` succeeds

## 6. Add tests for initIfNeeded

Test cases:
- Remote URL set → `initIfNeeded` returns nil without init (clone handles it)
- No remote URL + existing `.git` → skips init
- No remote URL + non-existent dir → creates dir + runs init
- No remote URL + parent dir doesn't exist → creates parent dirs + runs init
- No remote URL + path exists as file → returns error mentioning "not a directory"

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- All new logic runs before the HTTP server starts (bootstrap phase)
- No new external dependencies
- Must not break spec 002 behavior (remote clone still works)
- Use `errors.Wrap`/`errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf` or bare `return err`
- Backward compatible: existing repos with remotes work identically
</constraints>

<verification>
make precommit
</verification>
