---
status: completed
summary: Added syncOnStartup to main.go (git pull then push at boot), wired into bootstrap() after recoverUntracked, exposed via export_test.go, added 7 Ginkgo tests, and updated CHANGELOG.md.
container: git-rest-031-sync-on-startup
dark-factory-version: v0.135.19-1-gc08c946
created: "2026-04-28T17:25:00Z"
queued: "2026-04-28T19:22:46Z"
started: "2026-04-28T20:04:57Z"
completed: "2026-04-28T20:09:49Z"
---

<summary>
- The service synchronizes with the configured remote once at startup
- Local commits accumulated during downtime get pushed up immediately, not after the next write
- The local working copy is current the moment the HTTP server starts accepting traffic
- A boot-time network stall has a hard ceiling — the sync cannot hang the pod indefinitely
- Local-only deployments are unaffected (no remote = no-op)
- A failed sync warns and proceeds — the periodic background pull and the next write keep things eventually consistent
- Closes the readiness-stuck-at-503 failure mode where a recovery commit existed but never reached the remote
</summary>

<objective>
Add `syncOnStartup` to `bootstrap()` after `recoverUntracked`. Runs `git pull` then `git push`. Brings the local working copy fully in sync with the remote at boot, eliminating the "recovery commit stranded locally" failure mode and the "stale local working copy until first puller tick" delay.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read coding guides before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-error-wrapping-guide.md`: error wrapping rules — never `fmt.Errorf`, never bare `return err`
- `go-logging-guide.md`: this repo uses `log/slog` exclusively. Do NOT introduce `glog`.
- `go-testing-guide.md`: Ginkgo/Gomega test patterns

Files to read before making changes:
- `main.go` — `bootstrap()` (~line 67), existing `cleanupStaleLocks` and `recoverUntracked` helpers
- `main_test.go` — existing Ginkgo suite at repo root using `package main_test`; `Describe("CleanupStaleLocks", ...)` and `Describe("RecoverUntracked", ...)` are the pattern to mirror
- `export_test.go` — exposes `CleanupStaleLocks` + `RecoverUntracked`; you'll add `SyncOnStartup`
- Existing `runGitCmd` helper in `main.go` (added by the recover-untracked prompt) — reuse it; don't write a new one
- `pkg/git/git.go` — `Pull()` and the existing `hasRemote` check (for understanding only; we use `git remote` directly via runGitCmd)
- `CHANGELOG.md` — versioned sections; latest is `## v0.14.0`. Add a `## Unreleased` section above it
</context>

<requirements>

## 1. Add package-level `syncOnStartup` to `main.go`

Place near `cleanupStaleLocks` and `recoverUntracked`. Reuse the existing `runGitCmd` helper added by the recover-untracked prompt — do NOT duplicate it.

```go
// syncOnStartupTimeout is the hard ceiling for the boot-time sync. Generous
// enough for a normal pull+push, tight enough that a hung remote does not
// stall pod startup until kubelet liveness fires.
const syncOnStartupTimeout = 60 * time.Second

// syncOnStartup runs `git pull` and then `git push` once at startup, after
// recoverUntracked. Brings the local working copy fully in sync with the
// remote before the HTTP server starts serving.
//
// No-op when:
//   - .git/ does not exist (pre-init)
//   - no remote is configured (spec 003 local-only mode)
//
// Behavior on partial failure:
//   - pull failure → warn-log, STILL attempt push (a recovery commit may be
//     pending; next API write would push anyway). If pull failed because the
//     local is behind (non-fast-forward), the subsequent push will also fail
//     with "non-fast-forward" — both are warn-logged and the function returns
//     nil. The next puller tick / API write will recover.
//   - push failure → warn-log, return nil (next API write retries).
//
// Best-effort: only the catastrophic `os.Stat(.git)` error returns non-nil.
// All git network errors are warn-logged and never abort startup.
func syncOnStartup(parentCtx context.Context, repoDir string) error {
    gitDir := filepath.Join(repoDir, ".git")
    if _, err := os.Stat(gitDir); os.IsNotExist(err) {
        return nil
    } else if err != nil {
        return errors.Wrapf(parentCtx, err, "stat %s", gitDir)
    }

    ctx, cancel := context.WithTimeout(parentCtx, syncOnStartupTimeout)
    defer cancel()

    // No-op when no remote configured.
    out, err := runGitCmd(ctx, repoDir, "remote")
    if err != nil {
        slog.WarnContext(ctx, "git remote check failed during startup sync", "error", err)
        return nil
    }
    if strings.TrimSpace(out) == "" {
        slog.InfoContext(ctx, "no remote configured, skipping startup sync")
        return nil
    }

    if _, err := runGitCmd(ctx, repoDir, "pull"); err != nil {
        slog.WarnContext(ctx, "startup git pull failed (puller will retry)", "error", err)
        // Do not return — still attempt push so a recovery commit is pushed
        // even if the pull failed (e.g. transient network at boot).
    } else {
        slog.InfoContext(ctx, "startup git pull succeeded")
    }

    if _, err := runGitCmd(ctx, repoDir, "push"); err != nil {
        slog.WarnContext(ctx, "startup git push failed (next API write will retry)", "error", err)
        return nil
    }
    slog.InfoContext(ctx, "startup git push succeeded")
    return nil
}
```

Imports to add: `time` (for `60 * time.Second`). The rest (`context`, `os`, `path/filepath`, `strings`, `log/slog`, `github.com/bborbe/errors`) are already imported via recover-untracked.

## 2. Wire into `bootstrap()`

Add `syncOnStartup` AFTER `recoverUntracked`. Final chain:

```go
func (a *application) bootstrap(ctx context.Context) error {
    if err := cleanupStaleLocks(ctx, a.Repo); err != nil {
        return errors.Wrap(ctx, err, "cleanup stale locks")
    }
    if err := a.initIfNeeded(ctx); err != nil {
        return errors.Wrap(ctx, err, "init if needed")
    }
    if err := a.cloneIfNeeded(ctx); err != nil {
        return errors.Wrap(ctx, err, "clone if needed")
    }
    if err := a.configureUserIfSet(ctx); err != nil {
        return errors.Wrap(ctx, err, "configure user if set")
    }
    if err := recoverUntracked(ctx, a.Repo); err != nil {
        return errors.Wrap(ctx, err, "recover untracked")
    }
    if err := syncOnStartup(ctx, a.Repo); err != nil {
        return errors.Wrap(ctx, err, "sync on startup")
    }
    return nil
}
```

## 3. Expose for testing via `export_test.go`

Append:

```go
// SyncOnStartup is exported for testing via the main_test package.
var SyncOnStartup = syncOnStartup
```

## 4. Add Ginkgo tests

Append a new `Describe("SyncOnStartup", ...)` block to `main_test.go` AFTER the existing `Describe("RecoverUntracked", ...)`. Mirror its setup pattern.

Tests need a real git repo with a remote — use a local "remote" (a separate bare repo in tmp) and clone it into `repoDir` so push/pull are real but local. Pattern:

```go
initRepoWithLocalRemote := func() (string, string) {
    remoteDir := filepath.Join(GinkgoT().TempDir(), "remote.git")
    repoDir := filepath.Join(GinkgoT().TempDir(), "repo")
    run := func(dir string, args ...string) {
        full := append([]string{"-C", dir}, args...)
        cmd := exec.Command("git", full...)
        out, err := cmd.CombinedOutput()
        Expect(err).NotTo(HaveOccurred(), string(out))
    }
    Expect(os.MkdirAll(remoteDir, 0o755)).To(Succeed())
    cmd := exec.Command("git", "init", "--bare", "--initial-branch=main", remoteDir)
    Expect(cmd.Run()).To(Succeed())

    cmd = exec.Command("git", "clone", remoteDir, repoDir)
    Expect(cmd.Run()).To(Succeed())
    run(repoDir, "config", "user.email", "test@example.com")
    run(repoDir, "config", "user.name", "Test")
    // Force the local default branch to `main` so the test does not depend
    // on the runner's `init.defaultBranch` setting (some CI runners default
    // to `master`).
    run(repoDir, "checkout", "-b", "main")
    Expect(os.WriteFile(filepath.Join(repoDir, ".gitkeep"), nil, 0o644)).To(Succeed())
    run(repoDir, "add", ".gitkeep")
    run(repoDir, "commit", "-m", "init")
    run(repoDir, "push", "-u", "origin", "main")
    return remoteDir, repoDir
}
```

Test cases (each as `It`):

1. **No `.git/` directory** → returns nil; no-op
2. **`.git/` exists but no remote configured** → returns nil; logs the no-remote skip; no error
3. **Repo with remote, working tree clean and synced** → returns nil; no commits added; pull+push succeed (or no-op if nothing to do)
4. **Repo with one local commit ahead of remote** → returns nil; bare remote shows the commit after `SyncOnStartup` (assert via `git log` on the bare remote). Mirrors the "recovery commit pushed at boot" behavior we want
5. **Repo with remote that has a new commit ahead** → returns nil; local repo has the new commit after `SyncOnStartup` (assert via `git log` on the local repo)
6. **Repo with both local-ahead AND remote-ahead** (no merge conflict, just disjoint files) → returns nil; both ends converge
7. **Pull fails, function still returns nil** (regression-guard for the pull-fails-but-keep-going constraint): point `origin` to a non-existent path (`git -C repoDir remote set-url origin /nonexistent/path`) so `git pull` fails with "could not read from remote"; create a local commit ahead; assert `SyncOnStartup` returns nil. The push will also fail (same broken origin) and that is fine — the assertion is just that the function does not return an error.

For each test that requires a remote, use the `initRepoWithLocalRemote` helper above (or inline it — author's choice).

## 5. Add CHANGELOG entry

`CHANGELOG.md` uses versioned sections; latest is `## v0.14.0`. Add `## Unreleased` directly after the preamble, above `## v0.14.0`:

```markdown
## Unreleased

- feat: Pull and push the configured remote at startup, after `recoverUntracked`. Closes the gap where recovery commits sat locally until the next API write (live incident 2026-04-28: `vault-obsidian-trading` recovered the orphan untracked file but readiness stayed 503 because the recovery commit was never pushed). No-op for local-only repos.

## v0.14.0
```

If `## Unreleased` already exists when the agent runs (e.g. autoRelease hasn't renamed it yet from the prior prompt), append the bullet under it instead.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass (the `CleanupStaleLocks` and `RecoverUntracked` Describe blocks stay green)
- Use `errors.Wrap`/`errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf`, never bare `return err`
- Use `log/slog` — do NOT add `github.com/golang/glog`
- `context.Background()` must NOT appear in `pkg/`; in `main.go` and tests it is fine
- No new external dependencies
- Best-effort: sync errors must NOT abort startup; only log warnings (the only `return err` case is the catastrophic `os.Stat` failure on `.git/` itself)
- Sync runs ONLY at startup. Do NOT add a periodic re-sync (the existing puller already pulls every PullInterval)
- `syncOnStartup` is a package-level function (NOT a method on `application`) so it is testable in isolation via `export_test.go`
- Reuse the existing `runGitCmd` helper from main.go (added in the recover-untracked prompt) — do NOT duplicate it
- Pull failure does NOT short-circuit the push attempt (we still want to push the recovery commit even if pull was transiently flaky)
- The whole sync runs under `context.WithTimeout(parentCtx, 60 * time.Second)` so a hung remote cannot stall pod boot until kubelet liveness fires
- Non-fast-forward push (when the remote has commits we did not pull) is an EXPECTED outcome on the failed-pull-then-push path. It MUST warn-log and the function MUST return nil. Do not retry, do not abort startup. The next puller tick will fetch and the next API write will push.
</constraints>

<verification>
`make precommit` — must pass.

Manual probe (optional, after deploy):
```bash
# Plant an orphan, restart pod, observe full self-recovery + sync
kubectlquant -n dev exec vault-obsidian-trading-0 -- sh -c 'echo orphan > "/data/30 Analysis/dev/probe-sync.md"'
kubectlquant -n dev delete pod vault-obsidian-trading-0
kubectlquant -n dev logs vault-obsidian-trading-0 | grep -iE "stale lock|recover|startup"
# Expected log lines:
#   "removed stale lock: ..."          (only if a lock existed)
#   "recovering untracked files from prior crash"
#   "recovered untracked files into a commit"
#   "startup git pull succeeded"
#   "startup git push succeeded"
# Then readiness flips to 200 within seconds (no waiting for periodic puller, no waiting for an API write).
kubectlquant -n dev exec vault-obsidian-trading-0 -- sh -c "cd /data && git log @{u}..HEAD --oneline"
# Expected: empty (recovery commit was pushed)
```
</verification>
