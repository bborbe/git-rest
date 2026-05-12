// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"bytes"
	"context"
	stderrors "errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"

	"github.com/bborbe/git-rest/pkg/metrics"
)

// SSHKeyPath is the path to an SSH private key used for git operations.
type SSHKeyPath string

// RemoteURL is the URL of a remote git repository.
type RemoteURL string

// ErrNotFound is returned when a requested file does not exist in the repository.
var ErrNotFound = stderrors.New("file not found")

// ErrInvalidPath is returned when the requested path fails validation.
var ErrInvalidPath = stderrors.New("invalid path")

// ErrRepoUnrecoverable is returned by Pull when the repository is in a state
// that cannot be automatically healed (e.g., git rebase --abort fails, or
// refs/remotes/origin/HEAD is missing for detached-HEAD recovery).
// Callers detect this via errors.Is(err, ErrRepoUnrecoverable).
var ErrRepoUnrecoverable = stderrors.New("repo state unrecoverable")

// RebaseConflictError is returned by Pull when git rebase encounters a content conflict.
// The repo is left in its conflicted state for human inspection.
// git rebase --abort is NEVER invoked automatically.
type RebaseConflictError struct {
	Path string // conflicted file path, from "CONFLICT (content): Merge conflict in <path>"
}

func (e *RebaseConflictError) Error() string {
	return "rebase conflict at " + e.Path
}

// parseRebaseConflictPath extracts the first conflicting file path from git rebase output.
// git rebase emits "CONFLICT (content): Merge conflict in <path>" on content conflicts.
// Returns empty string if no conflict line is found (non-conflict failure).
func parseRebaseConflictPath(output string) string {
	const prefix = "Merge conflict in "
	for _, line := range strings.Split(output, "\n") {
		if idx := strings.Index(line, prefix); idx >= 0 {
			return strings.TrimSpace(line[idx+len(prefix):])
		}
	}
	return ""
}

// Status represents the current state of the git working tree.
type Status struct {
	// Clean is true when the working tree has no uncommitted changes.
	Clean bool
	// NoPushPending is true when there are no commits ahead of the remote.
	NoPushPending bool
}

// Git abstracts all git shell operations on a local repository.
//
//counterfeiter:generate -o ../../mocks/git.go --fake-name FakeGit . Git
type Git interface {
	WriteFile(ctx context.Context, path string, content []byte) error
	DeleteFile(ctx context.Context, path string) error
	ReadFile(ctx context.Context, path string) ([]byte, error)
	ListFiles(ctx context.Context, pattern string) ([]string, error)
	Pull(ctx context.Context) error
	Status(ctx context.Context) (Status, error)
	Clone(ctx context.Context, remoteURL RemoteURL) error
	ConfigureUser(ctx context.Context, name string, email string) error
	Init(ctx context.Context) error
}

// New returns a Git implementation backed by the system git binary for the given repository path.
func New(
	repoPath string,
	m metrics.Metrics,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
	sshKeyPath SSHKeyPath,
) Git {
	return &git{
		repoPath:              repoPath,
		metrics:               m,
		currentDateTimeGetter: currentDateTimeGetter,
		sshKeyPath:            sshKeyPath,
	}
}

type git struct {
	repoPath              string
	mu                    sync.Mutex
	metrics               metrics.Metrics
	currentDateTimeGetter libtime.CurrentDateTimeGetter
	sshKeyPath            SSHKeyPath
}

// validatePath rejects empty, absolute, path-traversal, and .git paths.
func validatePath(ctx context.Context, path string) error {
	if path == "" {
		return errors.Wrap(ctx, ErrInvalidPath, "path must not be empty")
	}
	if filepath.IsAbs(path) {
		return errors.Wrap(ctx, ErrInvalidPath, "absolute paths not allowed")
	}
	// Check for .. components in both slash and OS separator forms.
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return errors.Wrap(ctx, ErrInvalidPath, "path traversal not allowed")
		}
	}
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".." {
			return errors.Wrap(ctx, ErrInvalidPath, "path traversal not allowed")
		}
	}
	cleaned := filepath.Clean(path)
	if strings.HasPrefix(cleaned, "..") {
		return errors.Wrap(ctx, ErrInvalidPath, "path traversal not allowed")
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".git" {
			return errors.Wrap(ctx, ErrInvalidPath, ".git directory access not allowed")
		}
	}
	return nil
}

// runCmd executes a git subcommand in the repo directory, combining stdout+stderr into any error message.
func (g *git) runCmd(ctx context.Context, dir string, args ...string) error {
	// #nosec G204 -- binary is hardcoded to "git"; args are internal subcommands, not user input
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if g.sshKeyPath != "" {
		cmd.Env = append(
			os.Environ(),
			fmt.Sprintf(
				"GIT_SSH_COMMAND=ssh -i %s -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no",
				string(g.sshKeyPath),
			),
		)
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return errors.Wrapf(ctx, err, "git %v: %s", args, buf.String())
	}
	return nil
}

// runCmdOutput executes a git subcommand in dir and returns its stdout.
func (g *git) runCmdOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	// #nosec G204 -- binary is hardcoded to "git"; args are internal subcommands, not user input
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if g.sshKeyPath != "" {
		cmd.Env = append(
			os.Environ(),
			fmt.Sprintf(
				"GIT_SSH_COMMAND=ssh -i %s -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no",
				string(g.sshKeyPath),
			),
		)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, errors.Wrapf(ctx, err, "git %v: %s", args, stderr.String())
	}
	return stdout.Bytes(), nil
}

// runCmdRaw executes a git subcommand in dir and returns combined stdout+stderr
// regardless of exit code. Use when the output must be inspected on failure
// (e.g., detecting rebase conflict paths from git rebase output).
func (g *git) runCmdRaw(ctx context.Context, dir string, args ...string) ([]byte, error) {
	// #nosec G204 -- binary is hardcoded to "git"; args are internal subcommands, not user input
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if g.sshKeyPath != "" {
		cmd.Env = append(
			os.Environ(),
			fmt.Sprintf(
				"GIT_SSH_COMMAND=ssh -i %s -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no",
				string(g.sshKeyPath),
			),
		)
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.Bytes(), err
}

// WriteFile writes content to path, stages and commits it, then pushes.
func (g *git) WriteFile(ctx context.Context, path string, content []byte) error {
	start := g.currentDateTimeGetter.Now()
	defer func() {
		g.metrics.ObserveGitOperation("write_file", time.Since(time.Time(start)).Seconds())
	}()

	if err := validatePath(ctx, path); err != nil {
		g.metrics.IncGitOperationError("write_file")
		return errors.Wrap(ctx, err, "validate path")
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	fullPath := filepath.Join(g.repoPath, path)
	_, statErr := os.Stat(fullPath)
	fileExists := statErr == nil

	if err := os.MkdirAll(filepath.Dir(fullPath), 0750); err != nil {
		g.metrics.IncGitOperationError("write_file")
		return errors.Wrapf(ctx, err, "create directories for %s", path)
	}

	if err := os.WriteFile(fullPath, content, 0600); err != nil { //nolint:gosec
		g.metrics.IncGitOperationError("write_file")
		return errors.Wrapf(ctx, err, "write file %s", path)
	}

	if err := g.runCmd(ctx, g.repoPath, "add", path); err != nil {
		g.metrics.IncGitOperationError("write_file")
		return errors.Wrapf(ctx, err, "git add %s", path)
	}

	commitMsg := "git-rest: create " + path
	if fileExists {
		commitMsg = "git-rest: update " + path
	}

	commitOut, err := g.runCmdRaw(ctx, g.repoPath, "commit", "-m", commitMsg)
	if err != nil {
		if strings.Contains(string(commitOut), "nothing to commit") {
			slog.InfoContext(
				ctx,
				"write file: no changes to commit (content unchanged)",
				"path",
				path,
			)
			return nil
		}
		g.metrics.IncGitOperationError("write_file")
		return errors.Wrapf(ctx, err, "git commit: %s", strings.TrimSpace(string(commitOut)))
	}

	if g.hasRemote(ctx) {
		if err := g.runCmd(ctx, g.repoPath, "push"); err != nil {
			g.metrics.IncGitOperationError("write_file")
			return errors.Wrap(ctx, err, "git push")
		}
	}

	return nil
}

// DeleteFile removes a file from the repository, commits and pushes the deletion.
func (g *git) DeleteFile(ctx context.Context, path string) error {
	start := g.currentDateTimeGetter.Now()
	defer func() {
		g.metrics.ObserveGitOperation("delete_file", time.Since(time.Time(start)).Seconds())
	}()

	if err := validatePath(ctx, path); err != nil {
		g.metrics.IncGitOperationError("delete_file")
		return errors.Wrap(ctx, err, "validate path")
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	fullPath := filepath.Join(g.repoPath, path)
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		slog.InfoContext(
			ctx,
			"delete file: no changes to commit (file already absent)",
			"path",
			path,
		)
		return nil
	}

	if err := g.runCmd(ctx, g.repoPath, "rm", path); err != nil {
		g.metrics.IncGitOperationError("delete_file")
		return errors.Wrapf(ctx, err, "git rm %s", path)
	}

	commitMsg := "git-rest: delete " + path
	if err := g.runCmd(ctx, g.repoPath, "commit", "-m", commitMsg); err != nil {
		g.metrics.IncGitOperationError("delete_file")
		return errors.Wrap(ctx, err, "git commit")
	}

	if g.hasRemote(ctx) {
		if err := g.runCmd(ctx, g.repoPath, "push"); err != nil {
			g.metrics.IncGitOperationError("delete_file")
			return errors.Wrap(ctx, err, "git push")
		}
	}

	return nil
}

// ReadFile reads the content of path from the working tree.
func (g *git) ReadFile(ctx context.Context, path string) ([]byte, error) {
	start := g.currentDateTimeGetter.Now()
	defer func() {
		g.metrics.ObserveGitOperation("read_file", time.Since(time.Time(start)).Seconds())
	}()

	if err := validatePath(ctx, path); err != nil {
		g.metrics.IncGitOperationError("read_file")
		return nil, errors.Wrap(ctx, err, "validate path")
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	fullPath := filepath.Join(g.repoPath, path)
	// #nosec G304 -- path is validated by validatePath before this point, rejecting traversal and absolute paths
	data, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		g.metrics.IncGitOperationError("read_file")
		return nil, errors.Wrapf(ctx, err, "read file %s", path)
	}
	return data, nil
}

// ListFiles returns relative file paths tracked by git that match pattern.
// If pattern is empty, all tracked files are returned.
func (g *git) ListFiles(ctx context.Context, pattern string) ([]string, error) {
	start := g.currentDateTimeGetter.Now()
	defer func() {
		g.metrics.ObserveGitOperation("list_files", time.Since(time.Time(start)).Seconds())
	}()

	g.mu.Lock()
	defer g.mu.Unlock()

	out, err := g.runCmdOutput(ctx, g.repoPath, "ls-files")
	if err != nil {
		g.metrics.IncGitOperationError("list_files")
		return nil, errors.Wrap(ctx, err, "git ls-files")
	}

	var result []string
	for _, line := range strings.Split(string(out), "\n") {
		select {
		case <-ctx.Done():
			return nil, errors.Wrap(ctx, ctx.Err(), "list files cancelled")
		default:
		}
		if line == "" {
			continue
		}
		if pattern == "" {
			result = append(result, line)
			continue
		}
		matched, matchErr := filepath.Match(pattern, line)
		if matchErr != nil {
			g.metrics.IncGitOperationError("list_files")
			return nil, errors.Wrapf(ctx, matchErr, "match pattern %s against %s", pattern, line)
		}
		if matched {
			result = append(result, line)
		}
	}
	return result, nil
}

// pullFetchSHAs fetches from remote and returns the three SHAs needed for state detection.
func (g *git) pullFetchSHAs(
	ctx context.Context,
	upstream string,
) (localSHA, remoteSHA, baseSHA string, err error) {
	if fetchErr := g.runCmd(ctx, g.repoPath, "fetch"); fetchErr != nil {
		g.metrics.IncGitOperationError("fetch")
		return "", "", "", errors.Wrap(ctx, fetchErr, "fetch failed")
	}
	localOut, e := g.runCmdOutput(ctx, g.repoPath, "rev-parse", "HEAD")
	if e != nil {
		g.metrics.IncGitOperationError("pull")
		return "", "", "", errors.Wrap(ctx, e, "rev-parse HEAD failed")
	}
	remoteOut, e := g.runCmdOutput(ctx, g.repoPath, "rev-parse", upstream)
	if e != nil {
		g.metrics.IncGitOperationError("pull")
		return "", "", "", errors.Wrapf(ctx, e, "rev-parse %s failed", upstream)
	}
	baseOut, e := g.runCmdOutput(ctx, g.repoPath, "merge-base", "HEAD", upstream)
	if e != nil {
		g.metrics.IncGitOperationError("pull")
		return "", "", "", errors.Wrap(ctx, e, "merge-base failed")
	}
	return strings.TrimSpace(string(localOut)),
		strings.TrimSpace(string(remoteOut)),
		strings.TrimSpace(string(baseOut)),
		nil
}

// pullRebaseAndPush rebases the current branch onto upstream, then pushes.
// Returns *RebaseConflictError on content conflicts (repo left in conflicted state).
func (g *git) pullRebaseAndPush(ctx context.Context, upstream string) error {
	out, rebaseErr := g.runCmdRaw(ctx, g.repoPath, "rebase", upstream)
	if rebaseErr != nil {
		if conflictPath := parseRebaseConflictPath(string(out)); conflictPath != "" {
			g.metrics.IncRebaseConflict()
			return &RebaseConflictError{Path: conflictPath}
		}
		g.metrics.IncGitOperationError("rebase")
		return errors.Wrapf(ctx, rebaseErr, "rebase %s: %s", upstream, out)
	}
	if err := g.runCmd(ctx, g.repoPath, "push"); err != nil {
		g.metrics.IncGitOperationError("push")
		return errors.Wrap(ctx, err, "push after rebase failed")
	}
	return nil
}

// recoverRepoState checks whether the repository is in an abandoned-rebase or
// bare-detached-HEAD state and heals it before the normal pull flow runs.
// Returns nil for a healthy repo (no-op, emits no log lines).
// Returns errors.Wrap(ErrRepoUnrecoverable, ...) for states that cannot be healed.
func (g *git) recoverRepoState(ctx context.Context) error {
	rebaseMergeDir := filepath.Join(g.repoPath, ".git", "rebase-merge")
	rebaseApplyDir := filepath.Join(g.repoPath, ".git", "rebase-apply")

	_, errMerge := os.Stat(rebaseMergeDir)
	_, errApply := os.Stat(rebaseApplyDir)

	if errMerge == nil || errApply == nil {
		// Abandoned rebase: abort it so Pull can run the normal fetch→state-machine flow.
		out, err := g.runCmdRaw(ctx, g.repoPath, "rebase", "--abort")
		if err != nil {
			return errors.Wrapf(
				ctx,
				ErrRepoUnrecoverable,
				"git rebase --abort failed: %s",
				strings.TrimSpace(string(out)),
			)
		}
		headOut, _ := g.runCmdOutput(ctx, g.repoPath, "rev-parse", "--abbrev-ref", "HEAD")
		slog.InfoContext(
			ctx,
			"git-rest: recovered from abandoned rebase",
			"branch",
			strings.TrimSpace(string(headOut)),
		)
		return nil
	}

	// Check if HEAD is detached (no rebase in progress).
	headOut, err := g.runCmdOutput(ctx, g.repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		// Cannot determine HEAD state — let Pull's @{u} resolution surface the error.
		return nil
	}
	if strings.TrimSpace(string(headOut)) != "HEAD" {
		// HEAD is on a branch — healthy state, no-op.
		return nil
	}

	// Bare detached HEAD: resolve the default branch from refs/remotes/origin/HEAD.
	symrefOut, err := g.runCmdOutput(ctx, g.repoPath, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err != nil {
		return errors.Wrap(
			ctx,
			ErrRepoUnrecoverable,
			"cannot determine default branch: refs/remotes/origin/HEAD not set",
		)
	}
	// symrefOut = "refs/remotes/origin/main\n" — strip the known prefix.
	const remotePrefix = "refs/remotes/origin/"
	trimmed := strings.TrimSpace(string(symrefOut))
	if !strings.HasPrefix(trimmed, remotePrefix) {
		return errors.Wrapf(
			ctx,
			ErrRepoUnrecoverable,
			"unexpected symbolic-ref format: %s",
			trimmed,
		)
	}
	branch := strings.TrimPrefix(trimmed, remotePrefix)

	if err := g.runCmd(ctx, g.repoPath, "checkout", branch); err != nil {
		return errors.Wrapf(
			ctx,
			ErrRepoUnrecoverable,
			"git checkout %s failed during detached-HEAD recovery",
			branch,
		)
	}
	if err := g.runCmd(ctx, g.repoPath, "branch", "--set-upstream-to=origin/"+branch, branch); err != nil {
		return errors.Wrapf(
			ctx,
			ErrRepoUnrecoverable,
			"git branch --set-upstream-to=origin/%s failed",
			branch,
		)
	}
	slog.InfoContext(ctx, "git-rest: recovered from detached HEAD", "branch", branch)
	return nil
}

// Pull implements a deterministic 4-state sync:
//   - local == remote        → no-op
//   - local clean, remote new → fast-forward (git merge --ff-only)
//   - local ahead, remote same → push
//   - diverged (both ahead)  → rebase onto remote tracking ref, then push
//
// On a rebase content conflict, Pull returns *RebaseConflictError and leaves
// the repo in its conflicted state. git rebase --abort is NEVER invoked.
// Branch name is derived from HEAD's upstream tracking ref, never hardcoded.
func (g *git) Pull(ctx context.Context) error {
	start := g.currentDateTimeGetter.Now()
	defer func() {
		g.metrics.ObserveGitOperation("pull", time.Since(time.Time(start)).Seconds())
	}()

	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.hasRemote(ctx) {
		slog.DebugContext(ctx, "git pull skipped: no remote configured")
		return nil
	}

	// Entry-state recovery: abort any abandoned rebase or restore a detached HEAD
	// before @{u} resolution, which fails on any non-branch HEAD.
	if err := g.recoverRepoState(ctx); err != nil {
		g.metrics.IncGitOperationError("pull")
		return err
	}

	upstreamOut, err := g.runCmdOutput(
		ctx, g.repoPath, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}",
	)
	if err != nil {
		g.metrics.IncGitOperationError("pull")
		return errors.Wrap(ctx, err, "no upstream configured")
	}
	upstream := strings.TrimSpace(string(upstreamOut))

	localSHA, remoteSHA, baseSHA, err := g.pullFetchSHAs(ctx, upstream)
	if err != nil {
		return err
	}

	switch {
	case localSHA == remoteSHA:
		return nil
	case localSHA == baseSHA:
		if err := g.runCmd(ctx, g.repoPath, "merge", "--ff-only", upstream); err != nil {
			g.metrics.IncGitOperationError("pull")
			return errors.Wrap(ctx, err, "fast-forward merge failed")
		}
		return nil
	case remoteSHA == baseSHA:
		if err := g.runCmd(ctx, g.repoPath, "push"); err != nil {
			g.metrics.IncGitOperationError("push")
			return errors.Wrap(ctx, err, "push failed")
		}
		return nil
	default:
		return g.pullRebaseAndPush(ctx, upstream)
	}
}

// Clone clones remoteURL into the repository path.
func (g *git) Clone(ctx context.Context, remoteURL RemoteURL) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	start := g.currentDateTimeGetter.Now()
	defer func() { g.metrics.ObserveGitOperation("clone", time.Since(time.Time(start)).Seconds()) }()
	return g.runCmd(
		ctx,
		filepath.Dir(g.repoPath),
		"clone",
		string(remoteURL),
		filepath.Base(g.repoPath),
	)
}

// Init initialises a new empty git repository at the repo path.
func (g *git) Init(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	start := g.currentDateTimeGetter.Now()
	defer func() { g.metrics.ObserveGitOperation("init", time.Since(time.Time(start)).Seconds()) }()
	if err := g.runCmd(ctx, g.repoPath, "init"); err != nil {
		g.metrics.IncGitOperationError("init")
		return errors.Wrap(ctx, err, "git init")
	}
	return nil
}

// ConfigureUser sets the git user.name and user.email in the repository config.
// Empty strings are skipped. This runs once at startup before concurrent operations.
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

// hasRemote reports whether the repository has at least one configured remote.
// It runs git remote and returns true when the output is non-empty.
// On error (e.g. invalid repo), returns true so the caller proceeds and fails naturally.
func (g *git) hasRemote(ctx context.Context) bool {
	out, err := g.runCmdOutput(ctx, g.repoPath, "remote")
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(out)) != ""
}

// Status returns the current working-tree and push-pending state.
func (g *git) Status(ctx context.Context) (Status, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	var s Status

	out, err := g.runCmdOutput(ctx, g.repoPath, "status", "--porcelain")
	if err != nil {
		return s, errors.Wrap(ctx, err, "git status --porcelain")
	}
	s.Clean = strings.TrimSpace(string(out)) == ""

	// Check for commits not yet pushed; if no upstream is configured, treat as no push pending.
	out, err = g.runCmdOutput(ctx, g.repoPath, "log", "@{u}..HEAD", "--oneline")
	if err != nil {
		s.NoPushPending = true
	} else {
		s.NoPushPending = strings.TrimSpace(string(out)) == ""
	}

	return s, nil
}
