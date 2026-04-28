---
status: completed
spec: [002-auto-clone-and-git-config]
summary: Added auto-clone capability via --git-remote-url flag with RemoteURL type, Clone method on Git interface, and bootstrap logic in main.go that clones on startup when .git directory is absent
container: git-rest-025-spec-002-auto-clone
dark-factory-version: v0.108.0-dirty
created: "2026-04-12T15:59:22Z"
queued: "2026-04-12T16:21:40Z"
started: "2026-04-12T16:27:15Z"
completed: "2026-04-12T16:33:03Z"
---

<summary>
- The service now clones a remote repository automatically when the repo directory is empty
- Parent directories are created if they do not exist when a remote URL is configured
- Existing repositories with a .git directory skip cloning and proceed normally
- Starting without the remote URL flag works identically to the current version
- Clone failures at startup produce clear git error output and prevent the service from starting
</summary>

<objective>
Add auto-clone capability so git-rest can bootstrap a repository from a remote URL on startup. When `--git-remote-url` is set and the repo path has no `.git` directory, clone it. Create parent directories if needed. Skip clone if repo already exists.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read coding guides before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-error-wrapping-guide.md`: error wrapping rules
- `go-factory-pattern.md`: factory wiring rules

Files to read before making changes:
- `main.go` — application struct (~line 33), `Run` method (~line 44), `createGitClient` method (~line 58)
- `pkg/git/git.go` — git struct, New constructor, runCmd method
</context>

<requirements>

## 1. Add remote URL type and CLI flag

Add a type (in `pkg/git/git.go` or a new types file):
```go
type RemoteURL string
```

In `main.go`, add to the `application` struct:
```go
GitRemoteURL git.RemoteURL `required:"false" arg:"git-remote-url" env:"GIT_REMOTE_URL" usage:"Git remote URL to clone from on startup"`
```

## 2. Add Clone method to Git interface and implementation

Add to the `Git` interface:
```go
Clone(ctx context.Context, remoteURL RemoteURL) error
```

Implement on the `git` struct:
```go
func (g *git) Clone(ctx context.Context, remoteURL RemoteURL) error {
    g.mu.Lock()
    defer g.mu.Unlock()
    start := g.currentDateTimeGetter.Now()
    defer func() { g.metrics.ObserveGitOperation("clone", time.Since(time.Time(start)).Seconds()) }()
    return g.runCmd(ctx, filepath.Dir(g.repoPath), "clone", string(remoteURL), filepath.Base(g.repoPath))
}
```

Note: clone runs in the parent directory, cloning into the repo directory name.

## 3. Add bootstrap logic to main.go

Replace the current `createGitClient` method with a `bootstrap` method that runs before `createGitClient`. The bootstrap method handles:

1. If `GitRemoteURL` is set and repo path does not exist → create parent directories with `os.MkdirAll`, then clone
2. If `GitRemoteURL` is set and repo path exists but has no `.git` subdirectory → clone (repo dir exists but is empty)
3. If `GitRemoteURL` is set and `.git` exists → skip clone
4. If `GitRemoteURL` is NOT set → current behavior (fail if repo doesn't exist)

In the `Run` method, call bootstrap before createGitClient:
```go
func (a *application) Run(ctx context.Context, sentryClient libsentry.Client) error {
    metrics.NewBuildInfoMetrics(a.BuildGitVersion, a.BuildGitCommit).SetBuildInfo(a.BuildDate)

    if err := a.bootstrap(ctx); err != nil {
        return errors.Wrap(ctx, err, "bootstrap failed")
    }

    gitClient, err := a.createGitClient(ctx)
    ...
}
```

The bootstrap method needs a temporary git client for cloning. Create it with the SSH key path but without the repo needing to exist yet. The clone operation uses `runCmd` with the parent directory, so the repo path can be empty.

## 4. Handle the bootstrap-clone flow

```go
func (a *application) bootstrap(ctx context.Context) error {
    if a.GitRemoteURL == "" {
        return nil // no remote URL, use current behavior
    }

    gitDir := filepath.Join(a.Repo, ".git")
    if _, err := os.Stat(gitDir); err == nil {
        return nil // already cloned, skip
    }

    // Create parent directories if needed
    if err := os.MkdirAll(a.Repo, 0o755); err != nil {
        return errors.Wrapf(ctx, err, "create repo directory %s", a.Repo)
    }

    // Clone using a temporary git client (only needs SSH key, not a valid repo)
    tmpGit := factory.CreateGitClient(a.Repo, metrics.NewMetrics(), libtime.NewCurrentDateTime(), a.GitSSHKey)
    if err := tmpGit.Clone(ctx, a.GitRemoteURL); err != nil {
        return errors.Wrapf(ctx, err, "clone %s", a.GitRemoteURL)
    }

    return nil
}
```

Note: `Clone` runs in the parent directory of `a.Repo`, so the repo path being empty is fine. The `MkdirAll` ensures the parent exists.

## 5. Update createGitClient

Remove the `os.Stat` check from `createGitClient` — the bootstrap method now handles the case where the repo doesn't exist (either by cloning or by failing with the current error). Keep the `os.Stat` check but make it clearer:

```go
func (a *application) createGitClient(ctx context.Context) (git.Git, error) {
    if _, err := os.Stat(filepath.Join(a.Repo, ".git")); err != nil {
        return nil, errors.Wrapf(ctx, err, "repo %s has no .git directory", a.Repo)
    }
    // ... validate SSH key, create client
}
```

## 6. Update mock for Git interface

Run `go generate ./...` to regenerate the mock with the new `Clone` method.

## 7. Add tests

Add test cases covering:
- Remote URL set + empty dir → clones repo
- Remote URL set + existing .git → skips clone
- Remote URL not set + missing dir → fails (current behavior)
- Remote URL set + parent dir doesn't exist → creates dirs + clones
- Clone failure → startup returns error with git output

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- All new logic runs before the HTTP server starts
- `--git-remote-url` without `--git-ssh-key` is valid (repo may use local path)
- No new external dependencies
- Use `errors.Wrap`/`errors.Wrapf` from `github.com/bborbe/errors`
- Backward compatible: no new flags → identical behavior to current version
</constraints>

<verification>
make precommit
</verification>
