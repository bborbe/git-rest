---
status: draft
spec: ["003"]
created: "2026-04-12T18:00:00Z"
---

<summary>
- git-rest initializes a local repository automatically on startup when no remote URL is configured
- Parent directories are created automatically when the repo path does not exist
- Repository initialization uses the same locking and metrics as all other git operations
- On startup, the server handles three cases: clone from remote, initialize locally, or use an existing repository
- Starting with an existing pre-cloned or pre-initialized repo continues to work identically
- Init failure (e.g. bad permissions) produces a clear startup error and prevents the HTTP server from starting
- If the repo path points to a file instead of a directory, startup fails with a clear error
</summary>

<objective>
Add a `git init` path to the startup bootstrap so that git-rest can work without any remote URL. When `--git-remote-url` is not set and the repository path has no `.git` directory, git-rest creates the directory tree and runs `git init` before starting the HTTP server.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read coding guides before implementing:
- `go-patterns.md`: Interface → Constructor → Struct pattern, error wrapping
- `go-error-wrapping-guide.md`: never use fmt.Errorf, always bborbe/errors
- `go-factory-pattern.md`: factory wiring rules

Files to read before making changes:
- `main.go` — application struct (~line 34), `bootstrap` method (~line 67), `cloneIfNeeded` (~line 77), `createGitClient` (~line 120)
- `pkg/git/git.go` — Git interface (~line 47), `git` struct (~line 74), `Clone` implementation (~line 339), `New` constructor (~line 60)
- `mocks/git.go` — to understand what `go generate` produces (do not hand-edit)
</context>

<requirements>

## 1. Add Init method to the Git interface

In `pkg/git/git.go`, add to the `Git` interface (after `ConfigureUser`):
```go
Init(ctx context.Context) error
```

## 2. Implement Init on the git struct

Add the implementation below `ConfigureUser`:
```go
// Init initialises a new empty git repository at the repo path.
func (g *git) Init(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	start := g.currentDateTimeGetter.Now()
	defer func() { g.metrics.ObserveGitOperation("init", time.Since(time.Time(start)).Seconds()) }()
	return g.runCmd(ctx, g.repoPath, "init")
}
```

## 3. Regenerate the FakeGit mock

Run:
```
go generate ./...
```

This adds `Init` to `mocks/git.go`. Do NOT hand-edit the mock.

## 4. Add initIfNeeded to main.go

Add a new method on `*application` (place it after `cloneIfNeeded`):
```go
func (a *application) initIfNeeded(ctx context.Context) error {
	// Only run when no remote URL is configured.
	if a.GitRemoteURL != "" {
		return nil
	}
	gitDir := filepath.Join(a.Repo, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		// .git already exists — repo is ready, nothing to do.
		return nil
	}
	// Reject --repo pointing at an existing file (not a directory).
	if info, err := os.Stat(a.Repo); err == nil && !info.IsDir() {
		return errors.Errorf(ctx, "repo path %s exists but is not a directory", a.Repo)
	}
	// Create the directory tree.
	if err := os.MkdirAll(a.Repo, 0o750); err != nil { //nolint:gosec
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

Note: `errors.Errorf` is from `github.com/bborbe/errors` — already used elsewhere in `main.go`.

## 5. Wire initIfNeeded into bootstrap

In `main.go`, update `bootstrap` to call `initIfNeeded` before `cloneIfNeeded`:
```go
func (a *application) bootstrap(ctx context.Context) error {
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

## 6. Add tests for Init

Add a new `Describe` block in `pkg/git/git_test.go`:
```go
var _ = Describe("Git Init", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("initialises a git repository in an existing empty directory", func() {
		dir, err := os.MkdirTemp("", "git-init-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = os.RemoveAll(dir) }()

		g := git.New(dir, &noopMetrics{}, libtime.NewCurrentDateTime(), "")
		err = g.Init(ctx)
		Expect(err).NotTo(HaveOccurred())
		_, err = os.Stat(filepath.Join(dir, ".git"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("returns error when directory does not exist", func() {
		g := git.New("/nonexistent/path/that/does/not/exist/repo", &noopMetrics{}, libtime.NewCurrentDateTime(), "")
		err := g.Init(ctx)
		Expect(err).To(HaveOccurred())
	})
})
```

## 7. Add CHANGELOG entry

Add a bullet under `## Unreleased` in `CHANGELOG.md`:
```
- Add local repository initialization when no remote URL is configured
```

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- All new logic runs before the HTTP server starts (bootstrap phase)
- Must not break spec 002 behavior — remote clone still works
- No new external dependencies
- Use `errors.Wrap`/`errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf`
- `context.Background()` must NOT appear in `pkg/` — only in `main.go`
- Factory functions are pure composition (no I/O, no conditionals) — do not add logic to factory.go
</constraints>

<verification>
Run `make precommit` — must pass.

Manual smoke test (optional):
```bash
# Should create a new repo from scratch
REPO=$(mktemp -d)/newrepo
go run . --listen :18080 --repo "$REPO" &
sleep 1
curl -s http://localhost:18080/healthz
kill %1
ls "$REPO/.git"  # should exist
```
</verification>
