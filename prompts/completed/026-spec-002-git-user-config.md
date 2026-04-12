---
status: completed
spec: ["002"]
summary: Added ConfigureUser method to Git interface and implementation, GitUserName/GitUserEmail CLI flags, bootstrap integration via extracted helper methods, and tests for all ConfigureUser code paths.
container: git-rest-026-spec-002-git-user-config
dark-factory-version: v0.108.0-dirty
created: "2026-04-12T15:59:22Z"
queued: "2026-04-12T16:21:43Z"
started: "2026-04-12T16:33:05Z"
completed: "2026-04-12T16:38:28Z"
---

<summary>
- Git commits now use a configurable author name and email instead of system defaults
- The identity is configured in the repository on startup using git config commands
- Services without the identity flags use whatever git identity is already configured
- Both name and email are independently optional
</summary>

<objective>
Add git user identity configuration so git-rest sets `user.name` and `user.email` in the repository on startup when provided via CLI flags.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read coding guides before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-error-wrapping-guide.md`: error wrapping rules

Files to read before making changes:
- `main.go` — application struct, Run method, bootstrap method (added by prior prompt)
- `pkg/git/git.go` — git struct, runCmd method
</context>

<requirements>

## 1. Add CLI flags to application struct

In `main.go`, add to the `application` struct:
```go
GitUserName  string `required:"false" arg:"git-user-name"  env:"GIT_USER_NAME"  usage:"Git author name for commits"`
GitUserEmail string `required:"false" arg:"git-user-email" env:"GIT_USER_EMAIL" usage:"Git author email for commits"`
```

## 2. Add ConfigureUser method to Git interface

Add to the `Git` interface in `pkg/git/git.go`:
```go
ConfigureUser(ctx context.Context, name string, email string) error
```

Implement on the `git` struct:
```go
func (g *git) ConfigureUser(ctx context.Context, name string, email string) error {
    if name != "" {
        if err := g.runCmd(ctx, g.repoPath, "config", "user.name", name); err != nil {
            return errors.Wrapf(ctx, err, "set git user.name %s", name)
        }
    }
    if email != "" {
        if err := g.runCmd(ctx, g.repoPath, "config", "user.email", email); err != nil {
            return errors.Wrapf(ctx, err, "set git user.email %s", email)
        }
    }
    return nil
}
```

No mutex needed — this runs once at startup before any concurrent operations.

## 3. Call ConfigureUser in bootstrap

In `main.go`, in the `bootstrap` method (added by the prior prompt), after clone (or skip-clone), configure the git user:

```go
// After clone logic...

if a.GitUserName != "" || a.GitUserEmail != "" {
    gitClient := factory.CreateGitClient(a.Repo, metrics.NewMetrics(), libtime.NewCurrentDateTime(), a.GitSSHKey)
    if err := gitClient.ConfigureUser(ctx, a.GitUserName, a.GitUserEmail); err != nil {
        return errors.Wrap(ctx, err, "configure git user")
    }
}
```

If the bootstrap method already creates a git client for cloning, reuse it for ConfigureUser instead of creating a new one.

## 4. Update mock for Git interface

Run `go generate ./...` to regenerate the mock with the new `ConfigureUser` method.

## 5. Add tests

Add test cases:
- Both name and email set → `git config user.name` and `git config user.email` both called
- Only name set → only `git config user.name` called
- Only email set → only `git config user.email` called
- Neither set → no git config calls, existing behavior

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- All new logic runs before the HTTP server starts
- No new external dependencies
- Use `errors.Wrap`/`errors.Wrapf` from `github.com/bborbe/errors`
- Backward compatible: no new flags → identical behavior
</constraints>

<verification>
make precommit
</verification>
