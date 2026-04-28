---
status: committing
summary: Added cleanupStaleLocks helper that runs at startup to remove stale *.lock files under .git/, with export_test.go for testability and Ginkgo tests covering all edge cases.
container: git-rest-029-cleanup-stale-git-locks
dark-factory-version: v0.135.19-1-gc08c946
created: "2026-04-28T16:30:00Z"
queued: "2026-04-28T14:25:26Z"
started: "2026-04-28T14:37:14Z"
---

<summary>
- Auto-remove stale `*.lock` files in `.git/` on startup
- Recovers from prior crash (OOM, signal kill) without manual `kubectl exec`
- Single-replica StatefulSet semantics: any lock at boot is by definition stale
- Recursive walk of `.git/` for `*.lock`; logs each removal at info level
- Best-effort: errors logged as warnings but do NOT abort startup
- No-op when `.git/` does not exist yet (pre-init / pre-clone)
- Live incident: `vault-obsidian-trading-0` was OOMKilled 2026-04-28T16:15:04Z, has been blocking trade-analysis vault writes ever since because the orphaned `/data/.git/index.lock` survives across restarts
</summary>

<objective>
Add a `cleanupStaleLocks` helper that runs at the start of `bootstrap()` (before init/clone/configure) and removes any stale `*.lock` files under `.git/`. Self-heals from prior process crashes without manual intervention.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read coding guides before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-error-wrapping-guide.md`: error wrapping rules — never `fmt.Errorf`, never bare `return err`
- `go-logging-guide.md`: this repo uses `log/slog` exclusively (see `pkg/puller/puller.go:9,47` and `pkg/git/git.go:12,338`). Do NOT introduce `glog`.
- `go-testing-guide.md`: Ginkgo/Gomega test patterns

Files to read before making changes:
- `main.go` — `bootstrap()` (~line 67), `initIfNeeded`, `cloneIfNeeded`, application struct (~line 34)
- `main_test.go` — existing Ginkgo suite at repo root using `package main_test` + `gexec.Build`
- `pkg/puller/puller.go` — example of `slog.WarnContext(ctx, ..., "error", err)` pattern
- `CHANGELOG.md` — uses versioned sections (`## v0.12.1`, etc.); no `## Unreleased` exists yet
</context>

<requirements>

## 1. Add package-level `cleanupStaleLocks` to `main.go`

Implement as a package-level function (NOT a method on `application`) so it can be tested via `export_test.go`. Place near other bootstrap helpers:

```go
// cleanupStaleLocks removes any *.lock files under repoDir/.git at startup.
// Single-replica StatefulSet means any lock present at boot is stale —
// the binary just started and holds no other handles. Best-effort:
// individual errors are logged but never abort startup.
// No-op when .git/ does not exist (pre-init / pre-clone).
func cleanupStaleLocks(ctx context.Context, repoDir string) error {
    gitDir := filepath.Join(repoDir, ".git")
    if _, err := os.Stat(gitDir); os.IsNotExist(err) {
        return nil
    } else if err != nil {
        return errors.Wrapf(ctx, err, "stat %s", gitDir)
    }
    return filepath.WalkDir(gitDir, func(path string, d fs.DirEntry, walkErr error) error {
        if walkErr != nil {
            slog.WarnContext(ctx, "walk error during lock cleanup", "path", path, "error", walkErr)
            return nil
        }
        if d.IsDir() {
            return nil
        }
        if !strings.HasSuffix(path, ".lock") {
            return nil
        }
        if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
            slog.WarnContext(ctx, "failed to remove stale lock", "path", path, "error", err)
            return nil
        }
        slog.InfoContext(ctx, "removed stale lock", "path", path)
        return nil
    })
}
```

Imports to ensure are present in `main.go` (add only what is missing — check first):
- `io/fs` — needed for `fs.DirEntry` (the canonical type for `filepath.WalkDir`)
- `log/slog` — needed for `slog.InfoContext` / `slog.WarnContext`
- `strings` — needed for `strings.HasSuffix`

Do NOT add `github.com/golang/glog` — this repo uses `log/slog` exclusively.

## 2. Call it first in `bootstrap()`

Edit `bootstrap()` (~line 67):

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
    return nil
}
```

## 3. Expose for testing via `export_test.go`

Create a new file `export_test.go` at repo root (package `main`):

```go
// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

// CleanupStaleLocks is exported for testing via the main_test package.
var CleanupStaleLocks = cleanupStaleLocks
```

## 4. Add Ginkgo tests

Extend the existing `main_test.go` (package `main_test`, Ginkgo + Gomega) — append a new top-level `Describe` block. Do NOT create a new test file.

```go
var _ = Describe("CleanupStaleLocks", func() {
    var (
        ctx     context.Context
        repoDir string
        gitDir  string
    )

    BeforeEach(func() {
        ctx = context.Background()
        repoDir = GinkgoT().TempDir()
        gitDir = filepath.Join(repoDir, ".git")
    })

    Context("when .git/ does not exist", func() {
        It("returns nil", func() {
            Expect(main.CleanupStaleLocks(ctx, repoDir)).To(Succeed())
        })
    })

    Context("when .git/ exists but is empty", func() {
        BeforeEach(func() {
            Expect(os.MkdirAll(gitDir, 0o755)).To(Succeed())
        })
        It("returns nil and removes nothing", func() {
            Expect(main.CleanupStaleLocks(ctx, repoDir)).To(Succeed())
        })
    })

    Context("when .git/index.lock exists", func() {
        var lockPath string
        BeforeEach(func() {
            Expect(os.MkdirAll(gitDir, 0o755)).To(Succeed())
            lockPath = filepath.Join(gitDir, "index.lock")
            Expect(os.WriteFile(lockPath, []byte("stale"), 0o644)).To(Succeed())
        })
        It("removes the lock", func() {
            Expect(main.CleanupStaleLocks(ctx, repoDir)).To(Succeed())
            _, err := os.Stat(lockPath)
            Expect(os.IsNotExist(err)).To(BeTrue())
        })
    })

    Context("when nested .git/refs/heads/main.lock exists", func() {
        var lockPath string
        BeforeEach(func() {
            refsHeads := filepath.Join(gitDir, "refs", "heads")
            Expect(os.MkdirAll(refsHeads, 0o755)).To(Succeed())
            lockPath = filepath.Join(refsHeads, "main.lock")
            Expect(os.WriteFile(lockPath, []byte("stale"), 0o644)).To(Succeed())
        })
        It("removes the nested lock", func() {
            Expect(main.CleanupStaleLocks(ctx, repoDir)).To(Succeed())
            _, err := os.Stat(lockPath)
            Expect(os.IsNotExist(err)).To(BeTrue())
        })
    })

    Context("when non-lock files exist", func() {
        var headPath string
        BeforeEach(func() {
            Expect(os.MkdirAll(gitDir, 0o755)).To(Succeed())
            headPath = filepath.Join(gitDir, "HEAD")
            Expect(os.WriteFile(headPath, []byte("ref: refs/heads/main"), 0o644)).To(Succeed())
        })
        It("leaves them untouched", func() {
            Expect(main.CleanupStaleLocks(ctx, repoDir)).To(Succeed())
            _, err := os.Stat(headPath)
            Expect(err).NotTo(HaveOccurred())
        })
    })
})
```

Add these imports to `main_test.go` (the existing file imports neither `main` nor these stdlib packages — they all need to be added):
- `context`
- `os`
- `path/filepath`
- `main "github.com/bborbe/git-rest"`

Note: do NOT add a "lock is unremovable" test — it's flaky on macOS/CI when running as root.

## 5. Add CHANGELOG entry

`CHANGELOG.md` uses versioned sections (latest is `## v0.12.1`); there is no `## Unreleased`. Add a new `## Unreleased` section directly after the preamble, above `## v0.12.1`:

```markdown
## Unreleased

- feat: Auto-remove stale `*.lock` files in `.git/` on startup. Self-heals from prior crashes (OOM, signal kill) without manual intervention.

## v0.12.1
```

dark-factory autoRelease will rename `## Unreleased` to a versioned heading on release.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Use `errors.Wrap`/`errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf`, never bare `return err`
- Use `log/slog` — do NOT add `github.com/golang/glog` to this repo
- `context.Background()` must NOT appear in `pkg/`; in `main.go` and tests it is fine
- No new external dependencies
- Best-effort: cleanup errors must NOT abort startup; only log warnings
- Cleanup runs ONLY at startup. Do NOT add a periodic cleaner.
- `cleanupStaleLocks` is a package-level function (NOT a method on `application`) so it is testable in isolation via `export_test.go`.
</constraints>

<verification>
`make precommit` — must pass.

Manual probe (optional, after deploy):
```bash
# Plant a fake lock, restart pod, observe recovery
kubectlquant -n dev exec vault-obsidian-trading-0 -- touch /data/.git/probe.lock
kubectlquant -n dev rollout restart sts vault-obsidian-trading
kubectlquant -n dev logs vault-obsidian-trading-0 | grep "removed stale lock"
# Expected: "removed stale lock: /data/.git/probe.lock"
```
</verification>
