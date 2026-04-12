---
status: created
spec: [002-auto-clone-and-git-config]
created: "2026-04-12T16:10:00Z"
branch: dark-factory/auto-clone-and-git-config
---

<summary>
- Four new optional CLI flags are accepted: --git-remote-url, --git-ssh-key, --git-user-name, --git-user-email (plus corresponding env vars)
- On startup, before the HTTP server starts, the bootstrap package runs: validates SSH key file, creates parent dirs, clones if needed, configures git identity
- The SSH key path is threaded through to the git client so all ongoing git operations (pull, push, commit) use the correct SSH command
- When no new flags are set, startup behavior is identical to the current version
- Missing SSH key file at startup produces a clear, immediate error message
- Existing deployments with pre-cloned repos continue to work without any configuration changes
</summary>

<objective>
Wire the new CLI flags and the bootstrap package (created in the previous prompt) into main.go. After this prompt, git-rest can self-bootstrap: clone a repo, configure git identity, and use an SSH key for all git operations — all from flags alone.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read coding guides before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-patterns.md` — Interface→Constructor→Struct, error wrapping
- `go-testing-guide.md` — Ginkgo/Gomega, coverage ≥80%

Files to read before making changes:
- `main.go` — application struct, Run method, createGitClient — this is the primary change target
- `main_test.go` — test suite structure to follow for any new tests
- `pkg/bootstrap/bootstrap.go` — Bootstrapper interface and New constructor (created in prompt 1)
- `pkg/factory/factory.go` — CreateGitClient signature (updated in prompt 1 to accept sshKeyPath)

**Prerequisite**: Prompt 1 (1-spec-002-git-ssh-layer-and-bootstrap.md) must be completed first. The `pkg/bootstrap/` package and updated `factory.CreateGitClient` signature must exist before implementing this prompt.
</context>

<requirements>

## 1. Add new fields to the application struct

In `main.go`, add to the `application` struct:
```go
GitRemoteURL string `required:"false" arg:"git-remote-url" env:"GIT_REMOTE_URL" usage:"remote git URL to clone on startup (optional)"`
GitSSHKey    string `required:"false" arg:"git-ssh-key"    env:"GIT_SSH_KEY"    usage:"path to SSH private key for git operations (optional)"`
GitUserName  string `required:"false" arg:"git-user-name"  env:"GIT_USER_NAME"  usage:"git user.name to configure in the repo (optional)"`
GitUserEmail string `required:"false" arg:"git-user-email" env:"GIT_USER_EMAIL" usage:"git user.email to configure in the repo (optional)"`
```

Place them after `PullInterval` and before `BuildGitVersion` for logical grouping.

## 2. Call bootstrap in Run before createGitClient

In `main.go`, update the `Run` method to invoke bootstrap before calling `createGitClient`:

```go
func (a *application) Run(ctx context.Context, sentryClient libsentry.Client) error {
    metrics.NewBuildInfoMetrics(a.BuildGitVersion, a.BuildGitCommit).SetBuildInfo(a.BuildDate)

    if err := bootstrap.New(a.Repo, a.GitRemoteURL, a.GitSSHKey, a.GitUserName, a.GitUserEmail).Bootstrap(ctx); err != nil {
        return errors.Wrap(ctx, err, "bootstrap failed")
    }

    gitClient, err := a.createGitClient(ctx)
    if err != nil {
        return errors.Wrap(ctx, err, "create git client failed")
    }

    return service.Run(ctx,
        a.createGitRefresher(gitClient),
        a.createHTTPServer(gitClient, metrics.NewMetrics()),
    )
}
```

Add the import `"github.com/bborbe/git-rest/pkg/bootstrap"`.

## 3. Update createGitClient

The `createGitClient` method currently does `os.Stat(a.Repo)` to verify the repo exists before creating the git client. After bootstrap runs, this check is still valid — if bootstrap succeeded, the directory exists. No change needed to the stat check.

Update the `factory.CreateGitClient` call to pass `a.GitSSHKey`:
```go
func (a *application) createGitClient(ctx context.Context) (git.Git, error) {
    if _, err := os.Stat(a.Repo); err != nil {
        return nil, errors.Wrapf(ctx, err, "os stat %s failed", a.Repo)
    }

    return factory.CreateGitClient(a.Repo, a.GitSSHKey, metrics.NewMetrics(), libtime.NewCurrentDateTime()), nil
}
```

## 4. Add CHANGELOG.md entry

After implementing, add to `CHANGELOG.md` under `## Unreleased` (create the section if it doesn't exist):
```
- feat: Auto-clone repo and configure git identity on startup via --git-remote-url, --git-ssh-key, --git-user-name, --git-user-email flags
```

## 5. Update main_test.go — verify binary still compiles

The `main_test.go` "Compiles" test uses `gexec.Build`. Verify it still passes after the import and struct changes. No new test cases are needed in main_test.go since the bootstrap logic is tested in `pkg/bootstrap/bootstrap_test.go`.

If `main_test.go` has a test that starts the application with specific flags and checks behavior, update it to ensure the new optional flags don't break existing flag parsing.

</requirements>

<constraints>
- Only change files in `/workspace`
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Use `errors.Wrap`/`errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf` or bare `return err`
- No `context.Background()` in `pkg/` — only in `main.go`
- All four new flags are optional (`required:"false"`) — existing deployments must work without them
- Bootstrap runs before `service.Run` so failures abort startup, not after the HTTP server has started accepting traffic
</constraints>

<verification>
Run `make test` after implementing to catch issues early.

```bash
cd /workspace && make precommit
```
</verification>
