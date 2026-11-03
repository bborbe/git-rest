---
status: draft
spec: [025-bug-empty-clone-never-recloned]
created: "2026-11-03T15:46:47Z"
branch: dark-factory/bug-empty-clone-never-recloned
---

# Re-clone a repository whose HEAD cannot be resolved

<summary>
- A repository directory that contains a `.git` but no resolvable HEAD is no longer treated as "already cloned".
- On startup such a directory is discarded and cloned again from the configured remote.
- The clone is built in a scratch directory first, so a failed clone removes nothing and startup fails with an error that names the remote.
- Before anything is discarded, every path that exists locally but not on the remote is logged at WARN, one line per path.
- Paths that do exist on the remote are not named in the discard log.
- A successful re-clone logs the remote and the number of discarded paths at INFO, so the operator can see the clone happened.
- A repository that already resolves HEAD is left exactly as it is: no re-clone, no reset, no data loss.
- Removal is confined to the configured repository directory and never follows a symlink out of it; only paths are logged, never file contents.
- The pull state machine, dirty-tree rescue, quarantine paths, conflict resolver and readiness contract are untouched.
</summary>

<objective>
Make startup cloning treat a repository directory whose `.git` cannot resolve HEAD as not cloned: clone it from the configured remote, name the local-only paths being discarded, and leave a healthy repository untouched. This removes the state in which a partially failed clone is mistaken for a usable one and every later pull fails at upstream resolution forever.
</objective>

<context>
Read `docs/dod.md` for the project's Definition of Done, then read these files before changing anything:

- `main.go` — the whole file. Focus on `application.bootstrap`, `cleanupStaleLocks`, `initIfNeeded`, `cloneIfNeeded`, `runGitCmd` and the `application` struct. `cloneIfNeeded` currently keys only on `os.Stat(filepath.Join(a.Repo, ".git"))`.
- `export_test.go` — the existing test seams (`CleanupStaleLocks`, `RecoverUntracked`, `SyncOnStartup`, `ResolveGitSSHCommand`). The new startup-clone hook follows the same pattern.
- `main_test.go` — the `Describe("RecoverUntracked")` and `Describe("CleanupStaleLocks")` blocks for helper style, and `TestSuite` at the bottom. Root tests are `package main_test`.
- `pkg/git/git.go` — `Clone`, `runCmd`, `runCmdOutput`, `hasRemote`, and the `RemoteURL` / `SSHKeyPath` types. Do not modify this file in this prompt.
- `pkg/factory/factory.go` — `CreateGitClient(repoPath string, m metrics.Metrics, currentDateTimeGetter libtime.CurrentDateTimeGetter, sshKeyPath git.SSHKeyPath, resolver git.ConflictResolver, options ...git.GitOption) git.Git`.
- `specs/in-progress/025-bug-empty-clone-never-recloned.md` — `## Desired Behavior` items 1-5, `## Acceptance Criteria` 1-3, `## Failure Modes`, `## Security / Abuse Cases`, and the `## Suggested Decomposition` table row 1.

Coding guides (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors` only, never `fmt.Errorf`; `errors.Wrapf(ctx, err, format, args...)`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 / Gomega conventions, external test package `main_test`, `export_test.go` as the test seam.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` — unexported helper conventions.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — funlen 80, nestif 4, golines 100.
- `/home/node/.claude/plugins/marketplaces/dark-factory/docs/bug-workflow.md` — the bug verification step: replay the reproduction.

The replay in `<verification>` seeds the bare remote with one commit before cloning, matching the spec's `## Reproduction`: a bare remote with no commits also leaves HEAD unborn after a successful clone, so it could never satisfy the post-fix assertion. The bug is the local directory being HEAD-less while the remote is cloneable. Do not edit the spec.
</context>

<requirements>

## 1. Treat an unresolvable HEAD as "not cloned" (`main.go`)

1.1 Replace the body of `func (a *application) cloneIfNeeded(ctx context.Context) error` with the following. Keep the existing `GitRemoteURL == ""` early return and the existing clone path for a repository directory with no `.git` at all; the change is the `.git`-exists branch, which now checks whether HEAD resolves instead of returning unconditionally. Leave `initIfNeeded` unchanged: it applies the same `.git` test only when `GitRemoteURL` is empty, and a HEAD-less local repository with no remote is out of this spec's scope.

```go
// cloneIfNeeded ensures a repository exists at a.Repo before the puller runs.
//
// A directory whose .git exists but cannot resolve HEAD is not a usable
// repository: git reports "No commits yet", every pull fails at @{u}, and the
// state lives on the volume, so no restart changes anything. Such a directory
// is treated as absent and re-cloned from a.GitRemoteURL.
func (a *application) cloneIfNeeded(ctx context.Context) error {
	if a.GitRemoteURL == "" {
		return nil
	}
	gitDir := filepath.Join(a.Repo, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		if headResolvable(ctx, a.Repo) {
			return nil
		}
		return a.recloneHeadless(ctx)
	}
	if err := os.MkdirAll(a.Repo, 0o750); err != nil { //nolint:gosec
		return errors.Wrapf(ctx, err, "create repo directory %s", a.Repo)
	}
	m := metrics.NewMetrics()
	tmpGit := factory.CreateGitClient(
		a.Repo,
		m,
		libtime.NewCurrentDateTime(),
		a.GitSSHKey,
		a.selectResolver(m),
	)
	if err := tmpGit.Clone(ctx, a.GitRemoteURL); err != nil {
		return errors.Wrapf(ctx, err, "clone %s", a.GitRemoteURL)
	}
	return nil
}
```

Do not change the `os.Stat` error handling beyond this: when `.git` does not exist (`os.IsNotExist`), the existing `MkdirAll` + `Clone` path runs unchanged. A `Stat` error that is neither nil nor `IsNotExist` also falls through to that path, exactly as today.

1.2 Add the HEAD probe next to `cloneIfNeeded`:

```go
// headResolvable reports whether repoDir holds a repository whose HEAD points
// at a commit. An unborn HEAD — a freshly `git init`ed directory, or the
// residue of a clone that died before any ref arrived — reports false.
func headResolvable(ctx context.Context, repoDir string) bool {
	_, err := runGitCmd(ctx, repoDir, "", "rev-parse", "--verify", "--quiet", "HEAD")
	return err == nil
}
```

`runGitCmd` already exists in `main.go` with the signature `runGitCmd(ctx context.Context, repoDir, sshCommand string, args ...string) (string, error)`; use it, do not add a second git-exec helper. `--quiet` keeps the expected non-zero exit for an unborn HEAD from producing noise.

1.3 Add `recloneHeadless` as a method on `*application`, plus the three helpers below. Use `log/slog` (already imported in `main.go`) — never `github.com/golang/glog` in this file.

```go
// recloneHeadless replaces a repository directory whose .git cannot resolve
// HEAD with a fresh clone of a.GitRemoteURL.
//
// The clone is built inside a scratch directory under a.Repo first, so a failed
// clone discards nothing: the remote being unreachable, or the disk filling up,
// leaves the existing residue exactly where it was. Only after the clone
// succeeds are the local-only paths logged and the residue removed.
func (a *application) recloneHeadless(ctx context.Context) error {
	localPaths := repoPathsExceptGit(a.Repo)

	tmpDir, err := os.MkdirTemp(a.Repo, ".git-rest-reclone-")
	if err != nil {
		return errors.Wrapf(ctx, err, "create re-clone directory in %s", a.Repo)
	}
	defer func() {
		if rmErr := os.RemoveAll(tmpDir); rmErr != nil {
			slog.WarnContext(ctx, "remove re-clone directory failed", "path", tmpDir, "error", rmErr)
		}
	}()

	m := metrics.NewMetrics()
	tmpGit := factory.CreateGitClient(
		tmpDir,
		m,
		libtime.NewCurrentDateTime(),
		a.GitSSHKey,
		a.selectResolver(m),
	)
	if err := tmpGit.Clone(ctx, a.GitRemoteURL); err != nil {
		return errors.Wrapf(ctx, err, "clone %s", a.GitRemoteURL)
	}

	out, err := runGitCmd(ctx, tmpDir, "", "ls-tree", "-r", "--name-only", "HEAD")
	if err != nil {
		return errors.Wrapf(ctx, err, "list remote paths for %s", a.GitRemoteURL)
	}
	remotePaths := parsePathSet(out)

	discarded := 0
	for _, path := range localPaths {
		if _, ok := remotePaths[path]; ok {
			continue
		}
		discarded++
		slog.WarnContext(
			ctx,
			"git-rest: discarding local-only path before re-clone",
			"path", path,
			"remote", string(a.GitRemoteURL),
		)
	}

	if err := removeRepoEntries(ctx, a.Repo, filepath.Base(tmpDir)); err != nil {
		return err
	}
	if err := moveEntriesInto(ctx, tmpDir, a.Repo); err != nil {
		return err
	}

	slog.InfoContext(
		ctx,
		"git-rest: re-cloned repository with unresolvable HEAD",
		"remote", string(a.GitRemoteURL),
		"discarded_paths", discarded,
	)
	return nil
}

// repoPathsExceptGit returns the slash-separated relative paths of every
// non-directory entry under repoDir, excluding the top-level .git directory.
// Symlinks are reported as paths and never followed.
func repoPathsExceptGit(repoDir string) []string {
	var paths []string
	_ = filepath.WalkDir(repoDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == repoDir {
			return nil
		}
		rel, relErr := filepath.Rel(repoDir, path)
		if relErr != nil {
			return nil
		}
		if d.IsDir() {
			if rel == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	return paths
}

// parsePathSet turns newline-separated git path output into a set of
// slash-separated paths.
func parsePathSet(output string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		set[filepath.ToSlash(line)] = struct{}{}
	}
	return set
}

// removeRepoEntries deletes every entry in repoDir except keepName. Removal is
// confined to repoDir and never follows a symlink: os.RemoveAll unlinks a
// symlinked entry rather than traversing it.
func removeRepoEntries(ctx context.Context, repoDir, keepName string) error {
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		return errors.Wrapf(ctx, err, "read %s", repoDir)
	}
	for _, entry := range entries {
		if entry.Name() == keepName {
			continue
		}
		target := filepath.Join(repoDir, entry.Name())
		if err := os.RemoveAll(target); err != nil {
			return errors.Wrapf(ctx, err, "remove %s", target)
		}
	}
	return nil
}

// moveEntriesInto moves every entry of srcDir into dstDir. The .git directory is
// moved last, so an interrupted swap leaves dstDir without a resolvable HEAD and
// the next start re-clones instead of serving a partial working tree.
func moveEntriesInto(ctx context.Context, srcDir, dstDir string) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return errors.Wrapf(ctx, err, "read %s", srcDir)
	}
	move := func(entry os.DirEntry) error {
		from := filepath.Join(srcDir, entry.Name())
		to := filepath.Join(dstDir, entry.Name())
		if err := os.Rename(from, to); err != nil {
			return errors.Wrapf(ctx, err, "move %s to %s", from, to)
		}
		return nil
	}
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		if err := move(entry); err != nil {
			return err
		}
	}
	for _, entry := range entries {
		if entry.Name() != ".git" {
			continue
		}
		if err := move(entry); err != nil {
			return err
		}
	}
	return nil
}
```

1.4 `io/fs`, `os`, `path/filepath`, `strings`, `log/slog`, `github.com/bborbe/errors`, `libtime`, `metrics` and `factory` are already imported in `main.go`. Do not add imports that are not needed.

1.5 If the remote has no commits at all, `git ls-tree ... HEAD` fails and the wrapped error names the remote. Keep that behaviour: an empty remote cannot satisfy the Goal because there is no default branch for HEAD to resolve to. Do not silently continue.

## 2. Export the test seam (`export_test.go`)

2.1 Add the hook next to the existing seams, keeping the file's existing imports:

```go
// CloneIfNeeded is exported for testing via the main_test package.
func CloneIfNeeded(
	ctx context.Context,
	repo string,
	remoteURL git.RemoteURL,
	sshKey git.SSHKeyPath,
) error {
	a := &application{
		Repo:         repo,
		GitRemoteURL: remoteURL,
		GitSSHKey:    sshKey,
	}
	return a.cloneIfNeeded(ctx)
}
```

The struct field names are exactly `Repo`, `GitRemoteURL`, `GitSSHKey` (see the `application` struct in `main.go`). Do not construct a fake `git.Git` and do not make `cloneIfNeeded` a package-level function.

## 3. Tests (`main_test.go`)

Add a new top-level `var _ = Describe("CloneIfNeeded", func() { ... })` block. Put it after `Describe("RecoverUntracked")` and before `Describe("SyncOnStartup")`. Use only imports the file already has (`bytes`, `context`, `os`, `os/exec`, `path/filepath`, `strings`, `log/slog`, `github.com/bborbe/git-rest/pkg/git`).

Inside the block, define these local helpers (do not modify the existing helpers):

```go
var _ = Describe("CloneIfNeeded", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	gitRun := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
	}

	// seedRemote creates a bare remote whose default branch is main and whose
	// HEAD carries one commit and one tracked file, remote.md.
	seedRemote := func() string {
		remoteDir := GinkgoT().TempDir()
		gitRun(remoteDir, "init", "--bare", "--initial-branch=main")

		seedDir := GinkgoT().TempDir()
		gitRun(seedDir, "init", "--initial-branch=main")
		gitRun(seedDir, "config", "user.email", "test@example.com")
		gitRun(seedDir, "config", "user.name", "Test")
		Expect(os.WriteFile(
			filepath.Join(seedDir, "remote.md"), []byte("remote"), 0o600,
		)).To(Succeed())
		gitRun(seedDir, "add", "remote.md")
		gitRun(seedDir, "commit", "-m", "seed")
		gitRun(seedDir, "remote", "add", "origin", remoteDir)
		gitRun(seedDir, "push", "origin", "main")
		gitRun(remoteDir, "symbolic-ref", "HEAD", "refs/heads/main")
		return remoteDir
	}

	captureLogs := func() (*bytes.Buffer, func()) {
		buf := &bytes.Buffer{}
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(
			buf, &slog.HandlerOptions{Level: slog.LevelInfo},
		)))
		return buf, func() { slog.SetDefault(prev) }
	}
	// ... It blocks below ...
})
```

3.1 AC1 — an empty repository is cloned on startup:

```go
It("clones a HEAD-less repository and resolves the default branch", func() {
	remoteDir := seedRemote()
	repoDir := GinkgoT().TempDir()
	gitRun(repoDir, "init", "--initial-branch=main")

	buf, restore := captureLogs()
	defer restore()

	Expect(main.CloneIfNeeded(ctx, repoDir, git.RemoteURL(remoteDir), "")).To(Succeed())

	out, err := exec.Command("git", "-C", repoDir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	Expect(err).NotTo(HaveOccurred())
	Expect(strings.TrimSpace(string(out))).To(Equal("main"))
	Expect(buf.String()).To(ContainSubstring("re-cloned repository with unresolvable HEAD"))
	Expect(buf.String()).To(ContainSubstring(remoteDir))
})
```

3.2 AC1 second clause — a failing remote returns an error naming the remote:

```go
It("returns an error naming the remote when the clone fails", func() {
	repoDir := GinkgoT().TempDir()
	gitRun(repoDir, "init", "--initial-branch=main")
	missing := filepath.Join(GinkgoT().TempDir(), "missing.git")

	err := main.CloneIfNeeded(ctx, repoDir, git.RemoteURL(missing), "")
	Expect(err).To(HaveOccurred())
	Expect(err.Error()).To(ContainSubstring(missing))
})
```

3.3 AC2 — a healthy clone is not re-cloned:

```go
It("leaves a healthy repository untouched", func() {
	remoteDir := seedRemote()
	repoDir := GinkgoT().TempDir()
	gitRun(repoDir, "init", "--initial-branch=main")
	Expect(main.CloneIfNeeded(ctx, repoDir, git.RemoteURL(remoteDir), "")).To(Succeed())

	before, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").Output()
	Expect(err).NotTo(HaveOccurred())

	buf, restore := captureLogs()
	defer restore()

	Expect(main.CloneIfNeeded(ctx, repoDir, git.RemoteURL(remoteDir), "")).To(Succeed())

	after, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").Output()
	Expect(err).NotTo(HaveOccurred())
	Expect(after).To(Equal(before))

	status, err := exec.Command("git", "-C", repoDir, "status", "--short").Output()
	Expect(err).NotTo(HaveOccurred())
	Expect(strings.TrimSpace(string(status))).To(BeEmpty())
	Expect(buf.String()).NotTo(ContainSubstring("re-cloned repository"))
})
```

3.4 AC3 — the discard log names local-only paths and only those:

```go
It("names only local-only paths before discarding them", func() {
	remoteDir := seedRemote()
	repoDir := GinkgoT().TempDir()
	gitRun(repoDir, "init", "--initial-branch=main")
	Expect(os.WriteFile(
		filepath.Join(repoDir, "remote.md"), []byte("remote"), 0o600,
	)).To(Succeed())
	Expect(os.WriteFile(
		filepath.Join(repoDir, "local-only.md"), []byte("local"), 0o600,
	)).To(Succeed())

	buf, restore := captureLogs()
	defer restore()

	Expect(main.CloneIfNeeded(ctx, repoDir, git.RemoteURL(remoteDir), "")).To(Succeed())

	logStr := buf.String()
	Expect(logStr).To(MatchRegexp(`level=WARN[^\n]*local-only\.md`))
	Expect(logStr).NotTo(MatchRegexp(`level=WARN[^\n]*remote\.md`))
	// DB4: the successful re-clone reports how many paths it discarded.
	Expect(logStr).To(MatchRegexp(`level=INFO[^\n]*discarded_paths=1`))

	_, statErr := os.Stat(filepath.Join(repoDir, "local-only.md"))
	Expect(os.IsNotExist(statErr)).To(BeTrue())

	content, err := os.ReadFile(filepath.Join(repoDir, "remote.md"))
	Expect(err).NotTo(HaveOccurred())
	Expect(string(content)).To(Equal("remote"))
})
```

3.5 Do not weaken or rename the existing `Describe` blocks. `TestSuite` in `main_test.go` must keep running every spec.

3.6 Security — a symlinked entry is unlinked, never traversed:

```go
It("unlinks a symlink instead of following it out of the repository", func() {
	remoteDir := seedRemote()
	repoDir := GinkgoT().TempDir()
	gitRun(repoDir, "init", "--initial-branch=main")

	outside := GinkgoT().TempDir()
	outsideFile := filepath.Join(outside, "keep.md")
	Expect(os.WriteFile(outsideFile, []byte("outside"), 0o600)).To(Succeed())
	Expect(os.Symlink(outsideFile, filepath.Join(repoDir, "link.md"))).To(Succeed())

	buf, restore := captureLogs()
	defer restore()

	Expect(main.CloneIfNeeded(ctx, repoDir, git.RemoteURL(remoteDir), "")).To(Succeed())

	// The symlink is gone, the file it pointed at is untouched.
	_, linkErr := os.Lstat(filepath.Join(repoDir, "link.md"))
	Expect(os.IsNotExist(linkErr)).To(BeTrue())
	content, err := os.ReadFile(outsideFile)
	Expect(err).NotTo(HaveOccurred())
	Expect(string(content)).To(Equal("outside"))
	Expect(buf.String()).To(MatchRegexp(`level=WARN[^\n]*link\.md`))
})
```

3.7 A non-empty repository directory with **no** `.git` at all must also be cloned, not fail. Today the `os.Stat` branch falls through to `MkdirAll` + `Clone`, and `git clone` refuses a non-empty destination — so after the swap in 1.3 has removed `.git`, a crash in that window would leave exactly this shape and turn into a crashloop. Cover it: when `a.Repo` exists and is non-empty and has no `.git`, take the same scratch-clone-then-swap path as the unresolvable-HEAD case, and add a spec asserting the clone succeeds from that starting state.

## 4. Changelog (`CHANGELOG.md`)

4.1 The newest heading is `## v0.40.2`; there is no `## Unreleased` section. Add one directly after the SemVer preamble and before `## v0.40.2`, containing exactly this bullet:

```
- fix: Re-clone a repository whose `.git` cannot resolve HEAD at startup instead of treating it as already cloned, naming every local-only path discarded first, so a partially failed clone no longer leaves the pod serving an empty working tree forever
```

If a later prompt has already created `## Unreleased`, append the bullet to that section instead of creating a second one. Do not add any other changelog bullet.

## 5. Self-check before finishing

5.1 Walk each of AC1, AC2, AC3 and AC5 in the spec against the change and confirm the corresponding test exists and passes.

5.2 Confirm `cloneIfNeeded` has exactly one definition, `recloneHeadless` exactly one, and `CloneIfNeeded` appears in `export_test.go` exactly once — see `<verification>`.
</requirements>

<constraints>
- A repository that already resolves HEAD is left exactly as it is — no re-clone, no reset, no `git clean`, no data loss.
- If the re-clone fails, startup returns an error whose message contains the remote URL; the process does not continue into the pull loop pretending the repository is present.
- Nothing is removed before the clone has succeeded. Disk-full and unreachable-remote failures leave the existing residue untouched.
- Removal is confined to the configured `--repo` directory. No path outside it is deleted, and no symlink is followed out of it — a symlinked entry inside the repo is unlinked, never traversed.
- The discard log names paths only, never file contents.
- No re-clone retry loop, no backoff, no startup health signal.
- No change to the readiness contract, the pull state machine, the dirty-tree rescue, the quarantine paths or the conflict resolver.
- No new configuration field or flag.
- Errors are wrapped with `github.com/bborbe/errors`, never `fmt.Errorf`.
- Non-test `pkg/git` code must not call `context.Background()`; contexts are passed in. This prompt does not modify `pkg/git`.
- Do not modify `pkg/git/git.go` in this prompt; the `Pull` change is a separate prompt.
- Tests are Ginkgo v2 / Gomega in `package main_test`; the startup clone is reached through the `export_test.go` hook.
- Precommit budgets: funlen 80, gocognit 20, nestif 4, golines 100.
- `.dark-factory.yaml` sets `workflow: direct`, `pr: false`, `autoRelease: false`: commit to the branch directly; no PR or auto-release is expected.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run inside the container, from the repository root:

```
GOFLAGS=-buildvcs=false go build ./...
GOFLAGS=-buildvcs=false go test . -count=1
make test
make precommit
```

All must exit 0. `make precommit` is the authoritative gate; if it fails, fix the failure and re-run only the failing target until it passes, then run `make precommit` once more.

Then replay the reproduction (the mandatory bug check). The bare remote is seeded with one commit — see the reviewer note in `<context>` for why an empty bare remote cannot satisfy the post-fix assertion:

```bash
set -eu
work=$(mktemp -d)
mkdir -p "$work/repo"
git -C "$work/repo" init --initial-branch=main
git init --bare --initial-branch=main "$work/remote.git"
if git -C "$work/repo" rev-parse --verify HEAD >/dev/null 2>&1; then
  echo "unexpected: HEAD resolved before the run"
  exit 1
fi
mkdir -p "$work/seed"
git -C "$work/seed" init --initial-branch=main
git -C "$work/seed" -c user.email=test@example.com -c user.name=Test commit --allow-empty -m seed
git -C "$work/seed" remote add origin "$work/remote.git"
git -C "$work/seed" push origin main
git -C "$work/remote.git" symbolic-ref HEAD refs/heads/main
binDir=$(mktemp -d)
go build -mod=mod -o "$binDir/git-rest" .
timeout 15s "$binDir/git-rest" --repo="$work/repo" --git-remote-url="$work/remote.git" --listen=127.0.0.1:0 >"$work/log" 2>&1 || true
test "$(git -C "$work/repo" rev-parse --abbrev-ref HEAD)" = "main"
grep -q "re-cloned repository with unresolvable HEAD" "$work/log"
rm -rf "$work" "$binDir"
```

The `test` must pass — the re-cloned repository's HEAD must be the branch `main` — and the `grep` must match. Before the fix the `rev-parse` exits 128 (`fatal: ambiguous argument 'HEAD'`) and the `grep` finds nothing. Every `git` call in this block targets a `mktemp` directory outside `/workspace`, so `hideGit` does not mask it.

Then confirm the observable outcomes (each line must print `ok`):

```
test "$(grep -c 'func (a \*application) cloneIfNeeded' main.go)" = "1" && echo ok
test "$(grep -c 'func (a \*application) recloneHeadless' main.go)" = "1" && echo ok
test "$(grep -c 'func headResolvable' main.go)" = "1" && echo ok
test "$(grep -c 'func CloneIfNeeded' export_test.go)" = "1" && echo ok
test "$(grep -c 're-cloned repository with unresolvable HEAD' main.go)" = "1" && echo ok
test "$(grep -c 'discarding local-only path before re-clone' main.go)" = "1" && echo ok
test "$(grep -c '^## Unreleased' CHANGELOG.md)" = "1" && echo ok
! grep -q 'fmt.Errorf' main.go && echo ok
```
</verification>
