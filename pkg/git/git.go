// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"bytes"
	"context"
	stderrors "errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bborbe/errors"

	"github.com/bborbe/git-rest/pkg/metrics"
)

//counterfeiter:generate -o ../../mocks/git.go --fake-name FakeGit . Git

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
type Git interface {
	WriteFile(ctx context.Context, path string, content []byte) error
	DeleteFile(ctx context.Context, path string) error
	ReadFile(ctx context.Context, path string) ([]byte, error)
	ListFiles(ctx context.Context, pattern string) ([]string, error)
	Pull(ctx context.Context) error
	Status(ctx context.Context) (Status, error)
}

// New returns a Git implementation backed by the system git binary for the given repository path.
func New(repoPath string) Git {
	return &git{
		repoPath: repoPath,
	}
}

type git struct {
	repoPath string
	mu       sync.Mutex
}

// validatePath rejects empty, absolute, path-traversal, and .git paths.
func validatePath(ctx context.Context, path string) error {
	if path == "" {
		return errors.Wrapf(ctx, ErrInvalidPath, "path must not be empty")
	}
	if filepath.IsAbs(path) {
		return errors.Wrapf(ctx, ErrInvalidPath, "absolute paths not allowed")
	}
	// Check for .. components in both slash and OS separator forms.
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return errors.Wrapf(ctx, ErrInvalidPath, "path traversal not allowed")
		}
	}
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".." {
			return errors.Wrapf(ctx, ErrInvalidPath, "path traversal not allowed")
		}
	}
	cleaned := filepath.Clean(path)
	if strings.HasPrefix(cleaned, "..") {
		return errors.Wrapf(ctx, ErrInvalidPath, "path traversal not allowed")
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".git" {
			return errors.Wrapf(ctx, ErrInvalidPath, ".git directory access not allowed")
		}
	}
	return nil
}

// runCmd executes a git subcommand in dir, combining stdout+stderr into any error message.
func runCmd(ctx context.Context, dir string, args ...string) error {
	// #nosec G204 -- binary is hardcoded to "git"; args are internal subcommands, not user input
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return errors.Wrapf(ctx, err, "git %v: %s", args, buf.String())
	}
	return nil
}

// runCmdOutput executes a git subcommand in dir and returns its stdout.
func runCmdOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	// #nosec G204 -- binary is hardcoded to "git"; args are internal subcommands, not user input
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
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
	start := time.Now()
	defer func() {
		metrics.GitOperationDuration.WithLabelValues("write_file").
			Observe(time.Since(start).Seconds())
	}()

	if err := validatePath(ctx, path); err != nil {
		metrics.GitOperationErrors.WithLabelValues("write_file").Inc()
		return errors.Wrap(ctx, err, "validate path")
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	fullPath := filepath.Join(g.repoPath, path)
	_, statErr := os.Stat(fullPath)
	fileExists := statErr == nil

	if err := os.MkdirAll(filepath.Dir(fullPath), 0750); err != nil {
		metrics.GitOperationErrors.WithLabelValues("write_file").Inc()
		return errors.Wrapf(ctx, err, "create directories for %s", path)
	}

	if err := os.WriteFile(fullPath, content, 0600); err != nil { //nolint:gosec
		metrics.GitOperationErrors.WithLabelValues("write_file").Inc()
		return errors.Wrapf(ctx, err, "write file %s", path)
	}

	if err := runCmd(ctx, g.repoPath, "add", path); err != nil {
		metrics.GitOperationErrors.WithLabelValues("write_file").Inc()
		return errors.Wrapf(ctx, err, "git add %s", path)
	}

	commitMsg := "git-rest: create " + path
	if fileExists {
		commitMsg = "git-rest: update " + path
	}

	if err := runCmd(ctx, g.repoPath, "commit", "-m", commitMsg); err != nil {
		metrics.GitOperationErrors.WithLabelValues("write_file").Inc()
		return errors.Wrap(ctx, err, "git commit")
	}

	if err := runCmd(ctx, g.repoPath, "push"); err != nil {
		metrics.GitOperationErrors.WithLabelValues("write_file").Inc()
		return errors.Wrap(ctx, err, "git push")
	}

	return nil
}

// DeleteFile removes a file from the repository, commits and pushes the deletion.
func (g *git) DeleteFile(ctx context.Context, path string) error {
	start := time.Now()
	defer func() {
		metrics.GitOperationDuration.WithLabelValues("delete_file").
			Observe(time.Since(start).Seconds())
	}()

	if err := validatePath(ctx, path); err != nil {
		metrics.GitOperationErrors.WithLabelValues("delete_file").Inc()
		return errors.Wrap(ctx, err, "validate path")
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	fullPath := filepath.Join(g.repoPath, path)
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		return ErrNotFound
	}

	if err := runCmd(ctx, g.repoPath, "rm", path); err != nil {
		metrics.GitOperationErrors.WithLabelValues("delete_file").Inc()
		return errors.Wrapf(ctx, err, "git rm %s", path)
	}

	commitMsg := "git-rest: delete " + path
	if err := runCmd(ctx, g.repoPath, "commit", "-m", commitMsg); err != nil {
		metrics.GitOperationErrors.WithLabelValues("delete_file").Inc()
		return errors.Wrap(ctx, err, "git commit")
	}

	if err := runCmd(ctx, g.repoPath, "push"); err != nil {
		metrics.GitOperationErrors.WithLabelValues("delete_file").Inc()
		return errors.Wrap(ctx, err, "git push")
	}

	return nil
}

// ReadFile reads the content of path from the working tree.
func (g *git) ReadFile(ctx context.Context, path string) ([]byte, error) {
	start := time.Now()
	defer func() {
		metrics.GitOperationDuration.WithLabelValues("read_file").
			Observe(time.Since(start).Seconds())
	}()

	if err := validatePath(ctx, path); err != nil {
		metrics.GitOperationErrors.WithLabelValues("read_file").Inc()
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
		metrics.GitOperationErrors.WithLabelValues("read_file").Inc()
		return nil, errors.Wrapf(ctx, err, "read file %s", path)
	}
	return data, nil
}

// ListFiles returns relative file paths tracked by git that match pattern.
// If pattern is empty, all tracked files are returned.
func (g *git) ListFiles(ctx context.Context, pattern string) ([]string, error) {
	start := time.Now()
	defer func() {
		metrics.GitOperationDuration.WithLabelValues("list_files").
			Observe(time.Since(start).Seconds())
	}()

	g.mu.Lock()
	defer g.mu.Unlock()

	out, err := runCmdOutput(ctx, g.repoPath, "ls-files")
	if err != nil {
		metrics.GitOperationErrors.WithLabelValues("list_files").Inc()
		return nil, errors.Wrap(ctx, err, "git ls-files")
	}

	var result []string
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		if pattern == "" {
			result = append(result, line)
			continue
		}
		matched, matchErr := filepath.Match(pattern, line)
		if matchErr != nil {
			metrics.GitOperationErrors.WithLabelValues("list_files").Inc()
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
	start := time.Now()
	defer func() {
		metrics.GitOperationDuration.WithLabelValues("pull").Observe(time.Since(start).Seconds())
	}()

	g.mu.Lock()
	defer g.mu.Unlock()

	if err := runCmd(ctx, g.repoPath, "pull"); err != nil {
		metrics.GitOperationErrors.WithLabelValues("pull").Inc()
		return errors.Wrap(ctx, err, "git pull")
	}
	return nil
}

// Status returns the current working-tree and push-pending state.
func (g *git) Status(ctx context.Context) (Status, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	var s Status

	out, err := runCmdOutput(ctx, g.repoPath, "status", "--porcelain")
	if err != nil {
		return s, errors.Wrap(ctx, err, "git status --porcelain")
	}
	s.Clean = strings.TrimSpace(string(out)) == ""

	// Check for commits not yet pushed; if no upstream is configured, treat as no push pending.
	out, err = runCmdOutput(ctx, g.repoPath, "log", "@{u}..HEAD", "--oneline")
	if err != nil {
		s.NoPushPending = true
	} else {
		s.NoPushPending = strings.TrimSpace(string(out)) == ""
	}

	return s, nil
}
