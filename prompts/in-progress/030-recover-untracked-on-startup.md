---
status: committing
summary: 'Added recoverUntracked helper to main.go that detects untracked files at startup and commits them, wired it into bootstrap() after configureUserIfSet, exposed via export_test.go, added 5 Ginkgo test cases in main_test.go, and added CHANGELOG entry under ## Unreleased.'
container: git-rest-030-recover-untracked-on-startup
dark-factory-version: v0.135.19-1-gc08c946
created: "2026-04-28T15:35:00Z"
queued: "2026-04-28T15:40:58Z"
started: "2026-04-28T15:48:45Z"
---

<summary>
- Auto-commit any untracked files in the working tree on startup
- Recovers from crashes that interrupt the file-then-commit-then-push write path
- git-rest is the sole writer (single-replica StatefulSet) — untracked files at boot are by definition orphan partial writes whose `git add` step never ran
- Atomic file writes (tmp+rename) mean the bytes on disk are complete; only the index entry is missing
- Push is NOT performed here — the existing periodic puller and the next API call's push already handle that
- Live incident: `vault-obsidian-trading-0` OOMKilled mid-write left `30 Analysis/dev/2026-04-20 ORB DE40 V25 5cd94a5b.md` untracked, blocking readiness (Status.Clean = false → 503 forever) even after lockfile cleanup landed in v0.13.0
- Runs after `cleanupStaleLocks`, before HTTP server starts
</summary>

<objective>
Add a `recoverUntracked` helper that runs in `bootstrap()` after `cleanupStaleLocks` (and after init/clone/configure). If `git status --short` lists any untracked files, run `git add -A && git commit -m "git-rest: recover untracked from prior crash"`. Self-heals from crashes between `os.WriteFile` and `git add`.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read coding guides before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-error-wrapping-guide.md`: error wrapping rules — never `fmt.Errorf`, never bare `return err`
- `go-logging-guide.md`: this repo uses `log/slog` exclusively. Do NOT introduce `glog`.
- `go-testing-guide.md`: Ginkgo/Gomega test patterns

Files to read before making changes:
- `main.go` — `bootstrap()` (~line 67) and the existing `cleanupStaleLocks` helper (search for it; it lives near other bootstrap helpers); `application` struct (~line 34)
- `main_test.go` — existing Ginkgo suite at repo root using `package main_test`; existing `Describe("CleanupStaleLocks", ...)` block is the model to mirror
- `export_test.go` — already exposes `CleanupStaleLocks = cleanupStaleLocks`; you'll add `RecoverUntracked = recoverUntracked` here
- `pkg/git/git.go` — existing `runCmdOutput` and `runCmd` helpers (these are unexported; use the pattern from `cleanupStaleLocks` which calls `git` directly via `os/exec`)
- `pkg/puller/puller.go` — example of `slog.WarnContext(ctx, ..., "error", err)` pattern
- `CHANGELOG.md` — versioned sections; add a `## Unreleased` block above `## v0.13.0` if missing
</context>

<requirements>

## 1. Add package-level `recoverUntracked` to `main.go`

Implement as a package-level function (NOT a method on `application`) so it can be tested via `export_test.go`. Place near `cleanupStaleLocks`.

Use `os/exec` directly (consistent with the lockfile cleanup pattern — no dependency on `pkg/git` from `main.go`).

```go
// recoverUntracked detects untracked files in the working tree and commits
// them with a recovery message. Called from bootstrap() after init/clone/
// configure. git-rest is the sole writer (single-replica StatefulSet), so
// any untracked file at startup is an orphan partial write whose `git add`
// never ran (e.g. process killed between os.WriteFile and the commit step).
//
// Push is NOT performed here — the periodic puller and the next API call's
// push already handle remote sync; doing it here would duplicate retry logic.
//
// Best-effort: errors are logged and do NOT abort startup. A failure here
// just means readiness will fall back to the existing 503 wait until manual
// intervention; that's no worse than today.
//
// No-op when:
//   - .git/ does not exist (pre-init / pre-clone)
//   - the working tree is clean (no untracked files)
func recoverUntracked(ctx context.Context, repoDir string) error {
    gitDir := filepath.Join(repoDir, ".git")
    if _, err := os.Stat(gitDir); os.IsNotExist(err) {
        return nil
    } else if err != nil {
        return errors.Wrapf(ctx, err, "stat %s", gitDir)
    }

    out, err := runGitCmd(ctx, repoDir, "status", "--short")
    if err != nil {
        slog.WarnContext(ctx, "git status failed during untracked recovery", "error", err)
        return nil
    }
    if !hasUntracked(out) {
        return nil
    }

    slog.InfoContext(ctx, "recovering untracked files from prior crash")
    if _, err := runGitCmd(ctx, repoDir, "add", "-A"); err != nil {
        slog.WarnContext(ctx, "git add -A failed during recovery", "error", err)
        return nil
    }
    if _, err := runGitCmd(ctx, repoDir, "commit", "-m", "git-rest: recover untracked from prior crash"); err != nil {
        slog.WarnContext(ctx, "git commit failed during recovery", "error", err)
        return nil
    }
    slog.InfoContext(ctx, "recovered untracked files into a commit")
    return nil
}

// hasUntracked reports whether `git status --short` output contains any
// untracked-file lines (prefix `??`).
func hasUntracked(statusOutput string) bool {
    for _, line := range strings.Split(statusOutput, "\n") {
        if strings.HasPrefix(line, "??") {
            return true
        }
    }
    return false
}

// runGitCmd is a small helper to run `git -C repoDir <args>` and capture stdout.
// It exists so recoverUntracked stays self-contained in main.go (matching the
// no-pkg/git-dependency pattern used by cleanupStaleLocks).
func runGitCmd(ctx context.Context, repoDir string, args ...string) (string, error) {
    full := append([]string{"-C", repoDir}, args...)
    cmd := exec.CommandContext(ctx, "git", full...) // #nosec G204 -- args caller-controlled, internal use
    out, err := cmd.CombinedOutput()
    if err != nil {
        return string(out), errors.Wrapf(ctx, err, "git %v: %s", args, string(out))
    }
    return string(out), nil
}
```

Imports to ensure are present in `main.go` (add only what is missing — check first):
- `os/exec` — needed for `exec.CommandContext`
- `strings` — likely already imported (`cleanupStaleLocks` uses it)
- `log/slog` — likely already imported
- `path/filepath`, `os` — likely already imported

Do NOT add `github.com/golang/glog`.

## 2. Wire into `bootstrap()`

In `bootstrap()`, call `recoverUntracked` AFTER `configureUserIfSet` (the chain currently ends there) so the commit uses the configured author identity:

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
    return nil
}
```

## 3. Expose for testing via `export_test.go`

Append to the existing `export_test.go`:

```go
// RecoverUntracked is exported for testing via the main_test package.
var RecoverUntracked = recoverUntracked
```

Do NOT touch the existing `CleanupStaleLocks` line.

## 4. Add Ginkgo tests

Extend the existing `main_test.go` (package `main_test`). Append a new top-level `Describe("RecoverUntracked", ...)` block AFTER the existing `Describe("CleanupStaleLocks", ...)`. Mirror its structure (BeforeEach with `GinkgoT().TempDir()`, etc.).

Test cases (each as `It`):

1. **No `.git/` directory** → returns nil; no-op
2. **Clean working tree (no untracked files)** → returns nil; no commit added
3. **Single untracked file** → commits it; `git log` shows the recovery message; file is now tracked
4. **Multiple untracked files in nested directories** → all committed in one recovery commit
5. **Untracked file alongside a tracked-but-modified file** → both end up in the same recovery commit (the helper uses `git add -A`)

For tests that need a real git repo, initialize a tmp dir with `git init`, `git config user.email`, `git config user.name`, and create at least one initial commit so `git commit` doesn't fail on an empty branch (use a placeholder file like `.gitkeep`). Mirror the setup pattern from any existing repo-backed test in the project; if none exists yet under main, this is the first one — keep helpers local to the Describe block.

Test sketch (adapt to the existing patterns you find):

```go
var _ = Describe("RecoverUntracked", func() {
    var (
        ctx     context.Context
        repoDir string
    )

    BeforeEach(func() {
        ctx = context.Background()
        repoDir = GinkgoT().TempDir()
    })

    initRepo := func() {
        // Initialize a git repo with one initial commit so subsequent
        // commits don't fail on an empty branch.
        run := func(args ...string) {
            full := append([]string{"-C", repoDir}, args...)
            cmd := exec.Command("git", full...)
            out, err := cmd.CombinedOutput()
            Expect(err).NotTo(HaveOccurred(), string(out))
        }
        run("init")
        run("config", "user.email", "test@example.com")
        run("config", "user.name", "Test")
        Expect(os.WriteFile(filepath.Join(repoDir, ".gitkeep"), nil, 0o644)).To(Succeed())
        run("add", ".gitkeep")
        run("commit", "-m", "init")
    }

    Context("when .git/ does not exist", func() {
        It("returns nil", func() {
            Expect(main.RecoverUntracked(ctx, repoDir)).To(Succeed())
        })
    })

    Context("when working tree is clean", func() {
        BeforeEach(func() { initRepo() })
        It("returns nil and adds no commit", func() {
            // capture HEAD before
            // ... run RecoverUntracked ...
            // assert HEAD unchanged
            Expect(main.RecoverUntracked(ctx, repoDir)).To(Succeed())
            // (assertion: git rev-parse HEAD before and after equal)
        })
    })

    Context("when an untracked file exists", func() {
        BeforeEach(func() {
            initRepo()
            Expect(os.WriteFile(filepath.Join(repoDir, "orphan.md"), []byte("data"), 0o644)).To(Succeed())
        })
        It("commits it with the recovery message", func() {
            Expect(main.RecoverUntracked(ctx, repoDir)).To(Succeed())
            // assert: orphan.md is now tracked
            // assert: HEAD commit message contains "recover untracked from prior crash"
        })
    })

    Context("when multiple untracked files in nested dirs exist", func() {
        BeforeEach(func() {
            initRepo()
            nested := filepath.Join(repoDir, "30 Analysis", "dev")
            Expect(os.MkdirAll(nested, 0o755)).To(Succeed())
            Expect(os.WriteFile(filepath.Join(nested, "a.md"), []byte("a"), 0o644)).To(Succeed())
            Expect(os.WriteFile(filepath.Join(repoDir, "b.md"), []byte("b"), 0o644)).To(Succeed())
        })
        It("commits all of them in one commit", func() {
            Expect(main.RecoverUntracked(ctx, repoDir)).To(Succeed())
            // assert: both files tracked, single new commit on HEAD
        })
    })
})
```

Imports to add to `main_test.go` (some may already be present from the prior test block):
- `os/exec`
- (others — `context`, `os`, `path/filepath`, `main "github.com/bborbe/git-rest"` — should already be there from the `CleanupStaleLocks` tests)

## 5. Add CHANGELOG entry

`CHANGELOG.md` uses versioned sections; latest is `## v0.13.0`. Add a `## Unreleased` section directly after the preamble, above `## v0.13.0`:

```markdown
## Unreleased

- feat: Auto-commit untracked files in the working tree on startup. Recovers from crashes between `os.WriteFile` and `git add` (e.g. OOM mid-write) without manual intervention. Push is deferred to the existing puller / next API call.

## v0.13.0
```

If `## Unreleased` already exists, append the bullet under it instead of recreating the section. dark-factory autoRelease will rename `## Unreleased` to a versioned heading on release.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass (the v0.13.0 `CleanupStaleLocks` Describe block stays untouched and green)
- Use `errors.Wrap`/`errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf`, never bare `return err`
- Use `log/slog` — do NOT add `github.com/golang/glog`
- `context.Background()` must NOT appear in `pkg/`; in `main.go` and tests it is fine
- No new external dependencies
- Best-effort: recovery errors must NOT abort startup; only log warnings (the bootstrap step returns nil except on the `os.Stat` error which is genuinely catastrophic)
- Recovery runs ONLY at startup. Do NOT add a periodic recoverer.
- Push is NOT performed here. Defer to existing puller / next API call. Adding push here would duplicate retry logic and could race with the puller.
- `recoverUntracked` is a package-level function (NOT a method on `application`) so it is testable in isolation via `export_test.go`.
- The single recovery commit's message MUST be exactly `git-rest: recover untracked from prior crash` (consumers may grep for this in audit logs)
</constraints>

<verification>
`make precommit` — must pass.

Manual probe (optional, after deploy):
```bash
# Plant an orphan untracked file, restart pod, observe recovery
kubectlquant -n dev exec vault-obsidian-trading-0 -- sh -c 'echo orphan > "/data/30 Analysis/dev/probe-recover.md"'
kubectlquant -n dev delete pod vault-obsidian-trading-0
kubectlquant -n dev logs vault-obsidian-trading-0 | grep -i "recover"
# Expected:
#   "recovering untracked files from prior crash"
#   "recovered untracked files into a commit"
# Then readiness flips to 200 within ~30s as the puller pushes the recovery commit.
```
</verification>
