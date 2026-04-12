// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"bytes"
	"context"
	stderrors "errors"
	"fmt"
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

	if err := g.runCmd(ctx, g.repoPath, "commit", "-m", commitMsg); err != nil {
		g.metrics.IncGitOperationError("write_file")
		return errors.Wrap(ctx, err, "git commit")
	}

	if err := g.runCmd(ctx, g.repoPath, "push"); err != nil {
		g.metrics.IncGitOperationError("write_file")
		return errors.Wrap(ctx, err, "git push")
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
		return ErrNotFound
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

	if err := g.runCmd(ctx, g.repoPath, "push"); err != nil {
		g.metrics.IncGitOperationError("delete_file")
		return errors.Wrap(ctx, err, "git push")
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

// Pull fetches and integrates changes from the remote repository.
func (g *git) Pull(ctx context.Context) error {
	start := g.currentDateTimeGetter.Now()
	defer func() {
		g.metrics.ObserveGitOperation("pull", time.Since(time.Time(start)).Seconds())
	}()

	g.mu.Lock()
	defer g.mu.Unlock()

	if err := g.runCmd(ctx, g.repoPath, "pull"); err != nil {
		g.metrics.IncGitOperationError("pull")
		return errors.Wrap(ctx, err, "git pull")
	}
	return nil
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
