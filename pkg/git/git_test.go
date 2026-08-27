// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/git"
	"github.com/bborbe/git-rest/pkg/metrics"
)

// runGit executes a git command in the given directory. Panics on error.
func runGit(dir string, args ...string) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		panic(string(out))
	}
}

// noopMetrics satisfies metrics.Metrics without recording anything, for use in git tests.
type noopMetrics struct{}

func (n *noopMetrics) ObserveGitOperation(_ string, _ float64) {}

func (n *noopMetrics) IncGitOperationError(_ string) {}

func (n *noopMetrics) IncHTTPRequest(_, _, _ string) {}

func (n *noopMetrics) IncRebaseConflict() {}

func (n *noopMetrics) IncMergeOutcome(_ string) {}

func (n *noopMetrics) IncConflictPaths(_ int) {}

func (n *noopMetrics) IncResolverFailure(_ string) {}

func (n *noopMetrics) IncQuarantinedFiles() {}

// initRepo creates a temporary git repo with a local bare remote so that push works.
func initRepo() (workDir string, cleanup func()) {
	remoteDir, err := os.MkdirTemp("", "git-remote-*")
	if err != nil {
		panic(err)
	}

	workDir, err = os.MkdirTemp("", "git-work-*")
	if err != nil {
		panic(err)
	}

	runGit := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			panic(string(out))
		}
	}

	// Set up bare remote
	runGit(remoteDir, "init", "--bare")

	// Set up working repo
	runGit(workDir, "init")
	runGit(workDir, "config", "user.email", "test@example.com")
	runGit(workDir, "config", "user.name", "Test User")
	runGit(workDir, "remote", "add", "origin", remoteDir)
	runGit(workDir, "commit", "--allow-empty", "-m", "init")
	runGit(workDir, "push", "-u", "origin", "HEAD")

	cleanup = func() {
		_ = os.RemoveAll(workDir)
		_ = os.RemoveAll(remoteDir)
	}
	return workDir, cleanup
}

var _ = Describe("Git", func() {
	var ctx context.Context
	var g git.Git
	var workDir string
	var cleanup func()

	BeforeEach(func() {
		ctx = context.Background()
		workDir, cleanup = initRepo()
		g = git.New(
			workDir,
			&noopMetrics{},
			libtime.NewCurrentDateTime(),
			"",
			git.NewMarkerResolver(workDir),
		)
	})

	AfterEach(func() {
		cleanup()
	})

	Context("ReadFile", func() {
		Context("path validation", func() {
			It("returns error for empty path", func() {
				_, err := g.ReadFile(ctx, "")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("must not be empty"))
				Expect(errors.Is(err, git.ErrInvalidPath)).To(BeTrue())
			})

			It("returns error for path traversal", func() {
				_, err := g.ReadFile(ctx, "../../../etc/passwd")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("traversal"))
				Expect(errors.Is(err, git.ErrInvalidPath)).To(BeTrue())
			})

			It("returns error for absolute path", func() {
				_, err := g.ReadFile(ctx, "/absolute/path")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("absolute"))
				Expect(errors.Is(err, git.ErrInvalidPath)).To(BeTrue())
			})

			It("returns ErrInvalidPath for .git path", func() {
				_, err := g.ReadFile(ctx, ".git/config")
				Expect(err).To(HaveOccurred())
				Expect(errors.Is(err, git.ErrInvalidPath)).To(BeTrue())
			})

			It("returns ErrNotFound for non-existent file", func() {
				_, err := g.ReadFile(ctx, "nonexistent.txt")
				Expect(err).To(MatchError(git.ErrNotFound))
			})
		})
	})

	Context("WriteFile", func() {
		Context("path validation", func() {
			It("returns error for empty path", func() {
				err := g.WriteFile(ctx, "", []byte("content"))
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("must not be empty"))
				Expect(errors.Is(err, git.ErrInvalidPath)).To(BeTrue())
			})

			It("returns error for path traversal", func() {
				err := g.WriteFile(ctx, "../escape.txt", []byte("content"))
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("traversal"))
				Expect(errors.Is(err, git.ErrInvalidPath)).To(BeTrue())
			})

			It("returns error for absolute path", func() {
				err := g.WriteFile(ctx, "/tmp/escape.txt", []byte("content"))
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("absolute"))
				Expect(errors.Is(err, git.ErrInvalidPath)).To(BeTrue())
			})

			It("returns ErrInvalidPath for .git path", func() {
				err := g.WriteFile(ctx, ".git/config", []byte("content"))
				Expect(err).To(HaveOccurred())
				Expect(errors.Is(err, git.ErrInvalidPath)).To(BeTrue())
			})
		})

		Context("round-trip with ReadFile", func() {
			It("writes and reads back file content", func() {
				content := []byte("hello world")
				err := g.WriteFile(ctx, "hello.txt", content)
				Expect(err).NotTo(HaveOccurred())

				got, err := g.ReadFile(ctx, "hello.txt")
				Expect(err).NotTo(HaveOccurred())
				Expect(got).To(Equal(content))
			})

			It("uses create commit message for new file", func() {
				err := g.WriteFile(ctx, "newfile.txt", []byte("new"))
				Expect(err).NotTo(HaveOccurred())

				out, err := exec.Command("git", "-C", workDir, "log", "--oneline", "-1").Output()
				Expect(err).NotTo(HaveOccurred())
				Expect(string(out)).To(ContainSubstring("git-rest: create newfile.txt"))
			})

			It("uses update commit message for existing file", func() {
				err := g.WriteFile(ctx, "existing.txt", []byte("v1"))
				Expect(err).NotTo(HaveOccurred())

				err = g.WriteFile(ctx, "existing.txt", []byte("v2"))
				Expect(err).NotTo(HaveOccurred())

				out, err := exec.Command("git", "-C", workDir, "log", "--oneline", "-1").Output()
				Expect(err).NotTo(HaveOccurred())
				Expect(string(out)).To(ContainSubstring("git-rest: update existing.txt"))
			})

			It("creates intermediate directories", func() {
				err := g.WriteFile(ctx, "a/b/c.txt", []byte("nested"))
				Expect(err).NotTo(HaveOccurred())

				got, err := g.ReadFile(ctx, "a/b/c.txt")
				Expect(err).NotTo(HaveOccurred())
				Expect(got).To(Equal([]byte("nested")))
			})

			It("returns nil on second write with identical body (no-op)", func() {
				Expect(g.WriteFile(ctx, "idempotent.txt", []byte("hello"))).To(Succeed())
				Expect(g.WriteFile(ctx, "idempotent.txt", []byte("hello"))).To(Succeed())
			})

			It("does not create a second commit when content is unchanged", func() {
				Expect(g.WriteFile(ctx, "same.txt", []byte("content"))).To(Succeed())
				Expect(g.WriteFile(ctx, "same.txt", []byte("content"))).To(Succeed())

				out, err := exec.Command("git", "-C", workDir, "log", "--oneline", "--", "same.txt").
					Output()
				Expect(err).NotTo(HaveOccurred())
				lines := strings.Split(strings.TrimSpace(string(out)), "\n")
				var nonEmpty []string
				for _, l := range lines {
					if l != "" {
						nonEmpty = append(nonEmpty, l)
					}
				}
				Expect(
					nonEmpty,
				).To(HaveLen(1), "expected exactly one commit for same.txt, got: %s", string(out))
			})

			It("creates a new commit when content changes after a same-content write", func() {
				Expect(g.WriteFile(ctx, "evolving.txt", []byte("v1"))).To(Succeed())
				Expect(g.WriteFile(ctx, "evolving.txt", []byte("v1"))).To(Succeed()) // no-op
				Expect(g.WriteFile(ctx, "evolving.txt", []byte("v2"))).To(Succeed()) // real update

				out, err := exec.Command("git", "-C", workDir, "log", "--oneline", "--", "evolving.txt").
					Output()
				Expect(err).NotTo(HaveOccurred())
				lines := strings.Split(strings.TrimSpace(string(out)), "\n")
				var nonEmpty []string
				for _, l := range lines {
					if l != "" {
						nonEmpty = append(nonEmpty, l)
					}
				}
				Expect(
					nonEmpty,
				).To(HaveLen(2), "expected create + update commits, got: %s", string(out))
			})
		})
	})

	Context("WriteFileIfAbsent", func() {
		Context("path validation", func() {
			It("returns error for empty path", func() {
				err := g.WriteFileIfAbsent(ctx, "", []byte("content"))
				Expect(err).To(HaveOccurred())
				Expect(errors.Is(err, git.ErrInvalidPath)).To(BeTrue())
			})

			It("returns error for path traversal", func() {
				err := g.WriteFileIfAbsent(ctx, "../escape.txt", []byte("content"))
				Expect(err).To(HaveOccurred())
				Expect(errors.Is(err, git.ErrInvalidPath)).To(BeTrue())
			})
		})

		Context("create-only semantics", func() {
			It("creates a file when the path is free", func() {
				err := g.WriteFileIfAbsent(ctx, "fresh.txt", []byte("hello"))
				Expect(err).NotTo(HaveOccurred())

				got, err := g.ReadFile(ctx, "fresh.txt")
				Expect(err).NotTo(HaveOccurred())
				Expect(got).To(Equal([]byte("hello")))

				out, err := exec.Command("git", "-C", workDir, "log", "--oneline", "-1").Output()
				Expect(err).NotTo(HaveOccurred())
				Expect(string(out)).To(ContainSubstring("git-rest: create fresh.txt"))
			})

			It("returns ErrFileExists and preserves content when the path is occupied", func() {
				Expect(g.WriteFile(ctx, "taken.txt", []byte("original"))).To(Succeed())

				err := g.WriteFileIfAbsent(ctx, "taken.txt", []byte("overwrite"))
				Expect(err).To(HaveOccurred())
				Expect(errors.Is(err, git.ErrFileExists)).To(BeTrue())

				// Original content intact, no new commit on the path.
				got, err := g.ReadFile(ctx, "taken.txt")
				Expect(err).NotTo(HaveOccurred())
				Expect(got).To(Equal([]byte("original")))

				out, err := exec.Command("git", "-C", workDir, "log", "--oneline", "--", "taken.txt").
					Output()
				Expect(err).NotTo(HaveOccurred())
				lines := strings.Split(strings.TrimSpace(string(out)), "\n")
				var nonEmpty []string
				for _, l := range lines {
					if l != "" {
						nonEmpty = append(nonEmpty, l)
					}
				}
				Expect(
					nonEmpty,
				).To(HaveLen(1), "expected only the create commit, got: %s", string(out))
			})

			It("creates intermediate directories", func() {
				err := g.WriteFileIfAbsent(ctx, "x/y/z.txt", []byte("nested"))
				Expect(err).NotTo(HaveOccurred())

				got, err := g.ReadFile(ctx, "x/y/z.txt")
				Expect(err).NotTo(HaveOccurred())
				Expect(got).To(Equal([]byte("nested")))
			})
		})
	})

	Context("DeleteFile", func() {
		Context("path validation", func() {
			It("returns error for empty path", func() {
				err := g.DeleteFile(ctx, "")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("must not be empty"))
				Expect(errors.Is(err, git.ErrInvalidPath)).To(BeTrue())
			})

			It("returns error for path traversal", func() {
				err := g.DeleteFile(ctx, "../../../etc/passwd")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("traversal"))
				Expect(errors.Is(err, git.ErrInvalidPath)).To(BeTrue())
			})

			It("returns ErrInvalidPath for .git path", func() {
				err := g.DeleteFile(ctx, ".git/config")
				Expect(err).To(HaveOccurred())
				Expect(errors.Is(err, git.ErrInvalidPath)).To(BeTrue())
			})
		})

		It("returns nil for a non-existent file (idempotent delete)", func() {
			err := g.DeleteFile(ctx, "doesnotexist.txt")
			Expect(err).To(BeNil())
		})

		It("returns nil on second delete of the same file (idempotent delete)", func() {
			Expect(g.WriteFile(ctx, "gone.txt", []byte("bye"))).To(Succeed())
			Expect(g.DeleteFile(ctx, "gone.txt")).To(Succeed())
			Expect(g.DeleteFile(ctx, "gone.txt")).To(Succeed())
		})

		It("deletes an existing file and uses delete commit message", func() {
			err := g.WriteFile(ctx, "todelete.txt", []byte("bye"))
			Expect(err).NotTo(HaveOccurred())

			err = g.DeleteFile(ctx, "todelete.txt")
			Expect(err).NotTo(HaveOccurred())

			_, err = g.ReadFile(ctx, "todelete.txt")
			Expect(err).To(MatchError(git.ErrNotFound))

			out, err := exec.Command("git", "-C", workDir, "log", "--oneline", "-1").Output()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(out)).To(ContainSubstring("git-rest: delete todelete.txt"))
		})
	})

	Context("ListFiles", func() {
		BeforeEach(func() {
			err := g.WriteFile(ctx, "README.md", []byte("readme"))
			Expect(err).NotTo(HaveOccurred())
			err = g.WriteFile(ctx, "main.go", []byte("package main"))
			Expect(err).NotTo(HaveOccurred())
			err = g.WriteFile(ctx, "docs/guide.md", []byte("guide"))
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns all files when pattern is empty", func() {
			files, err := g.ListFiles(ctx, "")
			Expect(err).NotTo(HaveOccurred())
			Expect(files).To(ContainElements("README.md", "main.go", "docs/guide.md"))
		})

		It("returns only .md files when pattern is *.md", func() {
			files, err := g.ListFiles(ctx, "*.md")
			Expect(err).NotTo(HaveOccurred())
			Expect(files).To(ContainElement("README.md"))
			Expect(files).NotTo(ContainElement("main.go"))
		})

		Context("invalid glob pattern", func() {
			It("returns an error", func() {
				_, err := g.ListFiles(ctx, "[invalid")
				Expect(err).To(HaveOccurred())
			})
		})
	})

	Context("Status", func() {
		It("returns Clean=true and NoPushPending=true on a clean synced repo", func() {
			s, err := g.Status(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Clean).To(BeTrue())
			Expect(s.NoPushPending).To(BeTrue())
		})

		It("returns Clean=false when there are uncommitted changes", func() {
			// Write a file directly without committing
			err := os.WriteFile(filepath.Join(workDir, "dirty.txt"), []byte("dirty"), 0600)
			Expect(err).NotTo(HaveOccurred())

			// Stage but do not commit
			cmd := exec.Command("git", "add", "dirty.txt")
			cmd.Dir = workDir
			Expect(cmd.Run()).To(Succeed())

			s, err := g.Status(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Clean).To(BeFalse())
		})
	})

	Context("Pull", func() {
		It("succeeds on a repo with a configured remote", func() {
			err := g.Pull(ctx)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})

var _ = Describe("Git with non-existent repo path", func() {
	var ctx context.Context
	var brokenGit git.Git

	BeforeEach(func() {
		ctx = context.Background()
		brokenGit = git.New(
			"/nonexistent/path/that/does/not/exist",
			&noopMetrics{},
			libtime.NewCurrentDateTime(),
			"",
			git.NewMarkerResolver("/nonexistent/path/that/does/not/exist"),
		)
	})

	It("ListFiles returns error", func() {
		_, err := brokenGit.ListFiles(ctx, "")
		Expect(err).To(HaveOccurred())
	})

	It("Pull returns error", func() {
		err := brokenGit.Pull(ctx)
		Expect(err).To(HaveOccurred())
	})

	It("Status returns error", func() {
		_, err := brokenGit.Status(ctx)
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("Git SSH key", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	Context("SSH key path empty", func() {
		It("does not set GIT_SSH_COMMAND environment variable", func() {
			workDir, cleanup := initRepo()
			defer cleanup()
			g := git.New(
				workDir,
				&noopMetrics{},
				libtime.NewCurrentDateTime(),
				"",
				git.NewMarkerResolver(workDir),
			)
			// Pull succeeds without GIT_SSH_COMMAND set
			err := g.Pull(ctx)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("SSH key path set to a valid file", func() {
		It("succeeds when key file exists", func() {
			workDir, cleanup := initRepo()
			defer cleanup()

			keyFile, err := os.CreateTemp("", "ssh-key-*")
			Expect(err).NotTo(HaveOccurred())
			defer func() { _ = os.Remove(keyFile.Name()) }()
			_ = keyFile.Close()

			g := git.New(
				workDir,
				&noopMetrics{},
				libtime.NewCurrentDateTime(),
				git.SSHKeyPath(keyFile.Name()),
				git.NewMarkerResolver(workDir),
			)
			// Pull will use GIT_SSH_COMMAND; the local file:// remote doesn't need SSH, so it still works
			err = g.Pull(ctx)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})

var _ = Describe("Git Clone", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("clones a local bare remote into an existing empty directory", func() {
		remoteDir, err := os.MkdirTemp("", "git-remote-clone-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = os.RemoveAll(remoteDir) }()
		runGit(remoteDir, "init", "--bare")

		// workDir exists but is empty (no .git)
		workDir, err := os.MkdirTemp("", "git-work-clone-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = os.RemoveAll(workDir) }()
		// Clone into a subdir so the repo name is deterministic
		targetDir := filepath.Join(workDir, "repo")
		g := git.New(
			targetDir,
			&noopMetrics{},
			libtime.NewCurrentDateTime(),
			"",
			git.NewMarkerResolver(targetDir),
		)

		err = g.Clone(ctx, git.RemoteURL(remoteDir))
		Expect(err).NotTo(HaveOccurred())
		_, err = os.Stat(filepath.Join(targetDir, ".git"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("returns error when remote URL is invalid", func() {
		workDir, err := os.MkdirTemp("", "git-work-clone-fail-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = os.RemoveAll(workDir) }()
		targetDir := filepath.Join(workDir, "repo")
		g := git.New(
			targetDir,
			&noopMetrics{},
			libtime.NewCurrentDateTime(),
			"",
			git.NewMarkerResolver(targetDir),
		)

		err = g.Clone(ctx, git.RemoteURL("/nonexistent/path/that/does/not/exist"))
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("Git with no remote configured", func() {
	var ctx context.Context
	var noRemoteDir string
	var noRemoteGit git.Git

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		noRemoteDir, err = os.MkdirTemp("", "git-no-remote-*")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = os.RemoveAll(noRemoteDir) })

		runGit(noRemoteDir, "init")
		runGit(noRemoteDir, "config", "user.email", "test@test.com")
		runGit(noRemoteDir, "config", "user.name", "Test")
		noRemoteGit = git.New(
			noRemoteDir,
			&noopMetrics{},
			libtime.NewCurrentDateTime(),
			"",
			git.NewMarkerResolver(noRemoteDir),
		)
	})

	It("Pull succeeds and skips when no remote configured", func() {
		err := noRemoteGit.Pull(ctx)
		Expect(err).NotTo(HaveOccurred())
	})

	It("WriteFile succeeds without pushing when no remote configured", func() {
		// Write a file — should commit locally without error
		err := noRemoteGit.WriteFile(ctx, "local.txt", []byte("local content"))
		Expect(err).NotTo(HaveOccurred())

		// File must be readable
		content, err := noRemoteGit.ReadFile(ctx, "local.txt")
		Expect(err).NotTo(HaveOccurred())
		Expect(content).To(Equal([]byte("local content")))

		// Commit must exist in git log
		out, err := exec.Command("git", "-C", noRemoteDir, "log", "--oneline", "-1").Output()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(ContainSubstring("git-rest: create local.txt"))
	})

	It("DeleteFile succeeds without pushing when no remote configured", func() {
		// Create a file first
		err := noRemoteGit.WriteFile(ctx, "todelete.txt", []byte("bye"))
		Expect(err).NotTo(HaveOccurred())

		// Delete it — should commit locally without error
		err = noRemoteGit.DeleteFile(ctx, "todelete.txt")
		Expect(err).NotTo(HaveOccurred())

		// File must be gone
		_, err = noRemoteGit.ReadFile(ctx, "todelete.txt")
		Expect(err).To(MatchError(git.ErrNotFound))
	})

	It("Status sets NoPushPending=true when no upstream", func() {
		s, err := noRemoteGit.Status(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.NoPushPending).To(BeTrue())
	})
})

var _ = Describe("Git Init", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("initialises a git repository in an existing empty directory", func() {
		dir, err := os.MkdirTemp("", "git-init-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = os.RemoveAll(dir) }()

		g := git.New(
			dir,
			&noopMetrics{},
			libtime.NewCurrentDateTime(),
			"",
			git.NewMarkerResolver(dir),
		)
		err = g.Init(ctx)
		Expect(err).NotTo(HaveOccurred())
		_, err = os.Stat(filepath.Join(dir, ".git"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("returns error when directory does not exist", func() {
		g := git.New(
			"/nonexistent/path/that/does/not/exist/repo",
			&noopMetrics{},
			libtime.NewCurrentDateTime(),
			"",
			git.NewMarkerResolver("/nonexistent/path/that/does/not/exist/repo"),
		)
		err := g.Init(ctx)
		Expect(err).To(HaveOccurred())
	})
})

// setupPullFixture creates a working repo backed by a local bare remote.
// Returns workDir, a function to simulate external pushes, and a cleanup func.
func setupPullFixture() (workDir string, externalPush func(file, content string), cleanup func()) {
	remoteDir, err := os.MkdirTemp("", "git-remote-*")
	if err != nil {
		panic(err)
	}
	workDir, err = os.MkdirTemp("", "git-work-*")
	if err != nil {
		panic(err)
	}

	rg := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			panic(string(out))
		}
	}

	rg(remoteDir, "init", "--bare")
	rg(workDir, "init")
	rg(workDir, "config", "user.email", "test@example.com")
	rg(workDir, "config", "user.name", "Test User")
	rg(workDir, "remote", "add", "origin", remoteDir)
	rg(workDir, "commit", "--allow-empty", "-m", "init")
	rg(workDir, "push", "-u", "origin", "HEAD")

	externalPush = func(file, content string) {
		extDir, err := os.MkdirTemp("", "git-ext-*")
		if err != nil {
			panic(err)
		}
		defer func() { _ = os.RemoveAll(extDir) }()
		rg(extDir, "clone", remoteDir, ".")
		rg(extDir, "config", "user.email", "ext@example.com")
		rg(extDir, "config", "user.name", "External")
		if err := os.WriteFile(filepath.Join(extDir, file), []byte(content), 0600); err != nil {
			panic(err)
		}
		rg(extDir, "add", file)
		rg(extDir, "commit", "-m", "external: "+file)
		rg(extDir, "push", "origin")
	}

	cleanup = func() {
		_ = os.RemoveAll(workDir)
		_ = os.RemoveAll(remoteDir)
	}
	return
}

// writeLocalCommit stages and commits a new file in workDir without pushing.
func writeLocalCommit(workDir, file, content string) {
	if err := os.WriteFile(filepath.Join(workDir, file), []byte(content), 0600); err != nil {
		panic(err)
	}
	runGit(workDir, "add", file)
	runGit(workDir, "commit", "-m", "local: "+file)
}

// gitOutputStr runs a git command in dir and returns combined output as a string. Panics on error.
func gitOutputStr(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		panic(string(out))
	}
	return string(out)
}

// setupQuarantineFixture creates a working repo backed by a local bare remote
// pre-seeded with fileCount markdown files. Returns workDir, two closures
// (localEdit commits a divergent version of one file locally; externalPush
// pushes a divergent version of one file from a temp clone), the seed file
// list, and a cleanup func.
//
// Use pattern in tests:
//
//	workDir, localEdit, externalPush, seedFiles, cleanup := setupQuarantineFixture(11)
//	defer cleanup()
//	for i, f := range seedFiles {
//	    externalPush(f, fmt.Sprintf("---\nshared: remote-%d\n---\nremote body %d\n", i, i)) // diverge remote
//	}
//	for i, f := range seedFiles {
//	    localEdit(f, fmt.Sprintf("---\nshared: local-%d\n---\nlocal body %d\n", i, i))       // diverge local differently
//	}
//	// Now g.Pull(ctx) produces fileCount conflicts.
func setupQuarantineFixture(fileCount int) (
	workDir string,
	localEdit func(file, content string),
	externalPush func(file, content string),
	seedFiles []string,
	cleanup func(),
) {
	remoteDir, err := os.MkdirTemp("", "git-remote-quarantine-*")
	Expect(err).NotTo(HaveOccurred())
	workDir, err = os.MkdirTemp("", "git-work-quarantine-*")
	Expect(err).NotTo(HaveOccurred())

	rg := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, e := cmd.CombinedOutput()
		Expect(e).NotTo(HaveOccurred(), "%s %v: %s", "git", args, string(out))
	}

	rg(remoteDir, "init", "--bare", "-b", "main")
	rg(workDir, "init", "-b", "main")
	rg(workDir, "config", "user.email", "test@example.com")
	rg(workDir, "config", "user.name", "Test")
	rg(workDir, "remote", "add", "origin", remoteDir)

	// Seed fileCount markdown files on main and push to origin.
	seedFiles = make([]string, 0, fileCount)
	for i := 0; i < fileCount; i++ {
		name := fmt.Sprintf("note-%02d.md", i)
		seedFiles = append(seedFiles, name)
		abs := filepath.Join(workDir, name)
		fm := fmt.Sprintf("key%d: 1\nshared: base\n", i)
		body := fmt.Sprintf("body %d base\n", i)
		content := "---\n" + fm + "---\n" + body
		Expect(os.WriteFile(abs, []byte(content), 0o600)).To(Succeed())
		rg(workDir, "add", "--", name)
	}
	rg(workDir, "commit", "-q", "-m", "seed")
	rg(workDir, "push", "-u", "origin", "main")

	// localEdit: write a divergent version of one file locally and commit
	// (does not push). Pair with externalPush for the same file to create
	// a real merge conflict.
	localEdit = func(file, content string) {
		abs := filepath.Join(workDir, file)
		Expect(os.WriteFile(abs, []byte(content), 0o600)).To(Succeed())
		rg(workDir, "add", "--", file)
		rg(workDir, "commit", "-q", "-m", "local: "+file)
	}

	// externalPush: clone the bare remote, change one file, commit, push.
	externalPush = func(file, content string) {
		extDir, err := os.MkdirTemp("", "git-ext-quarantine-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = os.RemoveAll(extDir) }()
		rg(extDir, "clone", remoteDir, ".")
		rg(extDir, "config", "user.email", "ext@example.com")
		rg(extDir, "config", "user.name", "External")
		abs := filepath.Join(extDir, file)
		Expect(os.WriteFile(abs, []byte(content), 0o600)).To(Succeed())
		rg(extDir, "add", "--", file)
		rg(extDir, "commit", "-q", "-m", "external: "+file)
		rg(extDir, "push", "origin", "main")
	}

	cleanup = func() {
		_ = os.RemoveAll(workDir)
		_ = os.RemoveAll(remoteDir)
	}
	return workDir, localEdit, externalPush, seedFiles, cleanup
}

// gatherQuarantinedFiles returns the current value of the process-global
// git_rest_quarantined_files_total counter. Returns 0 if the counter is not yet
// registered. Used by the per-file quarantine tests to capture a baseline before
// Pull and assert on the delta afterwards (so the test is order-independent with
// other tests in the suite that may also touch the counter).
func gatherQuarantinedFiles() float64 {
	mfs, err := prometheus.DefaultGatherer.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, mf := range mfs {
		if mf.GetName() != "git_rest_quarantined_files_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			return m.GetCounter().GetValue()
		}
	}
	return 0
}

var _ = Describe("Pull state machine", func() {
	var (
		workDir      string
		externalPush func(file, content string)
		pullCleanup  func()
		fakeMetrics  *mocks.FakeMetrics
		pg           git.Git
		ctx          context.Context
	)

	BeforeEach(func() {
		ctx = context.Background()
		workDir, externalPush, pullCleanup = setupPullFixture()
		fakeMetrics = &mocks.FakeMetrics{}
		pg = git.New(
			workDir,
			fakeMetrics,
			libtime.NewCurrentDateTime(),
			"",
			git.NewMarkerResolver(workDir),
		)
	})

	AfterEach(func() {
		pullCleanup()
	})

	Context("local clean, remote unchanged (no-op)", func() {
		It("returns nil and does not change HEAD", func() {
			headBefore := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "HEAD"))
			Expect(pg.Pull(ctx)).To(BeNil())
			Expect(
				strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "HEAD")),
			).To(Equal(headBefore))
		})
	})

	Context("local clean, remote has new commits (fast-forward)", func() {
		BeforeEach(func() {
			externalPush("remote.txt", "from remote\n")
		})

		It("fast-forwards local to include the remote commit", func() {
			Expect(pg.Pull(ctx)).To(BeNil())
			content, err := pg.ReadFile(ctx, "remote.txt")
			Expect(err).To(BeNil())
			Expect(string(content)).To(Equal("from remote\n"))
		})

		It("leaves nothing unpushed", func() {
			Expect(pg.Pull(ctx)).To(BeNil())
			unpushed := strings.TrimSpace(gitOutputStr(workDir, "log", "@{u}..HEAD", "--oneline"))
			Expect(unpushed).To(BeEmpty())
		})
	})

	Context("local ahead, remote unchanged (push)", func() {
		BeforeEach(func() {
			writeLocalCommit(workDir, "local.txt", "local only\n")
		})

		It("pushes the local commit to remote", func() {
			Expect(pg.Pull(ctx)).To(BeNil())
			unpushed := strings.TrimSpace(gitOutputStr(workDir, "log", "@{u}..HEAD", "--oneline"))
			Expect(unpushed).To(BeEmpty())
		})
	})

	Context("diverged, no content conflict (merge+push)", func() {
		BeforeEach(func() {
			externalPush("remote.txt", "from remote\n")
			writeLocalCommit(workDir, "local.txt", "local only\n")
		})

		It("AC1: merges and pushes, producing a merge commit with both files", func() {
			Expect(pg.Pull(ctx)).To(BeNil())
			log := gitOutputStr(workDir, "log", "--oneline")
			Expect(log).To(ContainSubstring("remote.txt"), "remote commit must be in log")
			Expect(log).To(ContainSubstring("local.txt"), "local commit must be in log")
			Expect(log).To(ContainSubstring("Merge"), "merge commit must be present")
		})

		It("AC1: increments merge outcome clean counter exactly once", func() {
			Expect(pg.Pull(ctx)).To(BeNil())
			Expect(fakeMetrics.IncMergeOutcomeCallCount()).To(Equal(1))
			Expect(fakeMetrics.IncMergeOutcomeArgsForCall(0)).To(Equal("clean"))
		})

		It("AC1: leaves nothing unpushed after merge", func() {
			Expect(pg.Pull(ctx)).To(BeNil())
			unpushed := strings.TrimSpace(gitOutputStr(workDir, "log", "@{u}..HEAD", "--oneline"))
			Expect(unpushed).To(BeEmpty())
		})
	})

	Context("HEAD has no upstream tracking ref", func() {
		BeforeEach(func() {
			runGit(workDir, "branch", "--unset-upstream")
		})

		It("returns an error wrapping 'no upstream configured'", func() {
			err := pg.Pull(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no upstream configured"))
		})

		It("does not attempt rebase/push", func() {
			_ = pg.Pull(ctx)
			Expect(fakeMetrics.IncRebaseConflictCallCount()).To(Equal(0))
			Expect(fakeMetrics.IncGitOperationErrorCallCount()).To(BeNumerically(">=", 1))
		})
	})

	Context("diverged, same-line conflict (merge + MarkerResolver)", func() {
		BeforeEach(func() {
			externalPush("conflict.txt", "remote content\n")
			writeLocalCommit(workDir, "conflict.txt", "local content\n")
		})

		It(
			"AC2: Pull returns nil; merge commit message uses the per-file quarantine format",
			func() {
				Expect(pg.Pull(ctx)).To(BeNil())
				commitMsg := strings.TrimSpace(gitOutputStr(workDir, "log", "-1", "--format=%s"))
				Expect(commitMsg).To(HavePrefix("merge: resolved=["))
				Expect(commitMsg).To(ContainSubstring("conflict.txt"))
				Expect(commitMsg).To(MatchRegexp(
					`^merge: resolved=\[[^\]]*\] quarantined=\[\]$`,
				))
			},
		)

		It("AC2: conflict.txt in merge commit contains both versions under markers", func() {
			Expect(pg.Pull(ctx)).To(BeNil())
			// Read file content from the latest merge commit tree (not working tree).
			content := gitOutputStr(workDir, "show", "HEAD:conflict.txt")
			Expect(content).To(ContainSubstring("<<<<<<<"), "must contain open marker")
			Expect(content).To(ContainSubstring("======="), "must contain separator")
			Expect(content).To(ContainSubstring(">>>>>>>"), "must contain close marker")
			Expect(content).To(ContainSubstring("remote content"), "remote version must be present")
			Expect(content).To(ContainSubstring("local content"), "local version must be present")
		})

		It("AC2: increments merge outcome resolved counter exactly once", func() {
			Expect(pg.Pull(ctx)).To(BeNil())
			Expect(fakeMetrics.IncMergeOutcomeCallCount()).To(Equal(1))
			Expect(fakeMetrics.IncMergeOutcomeArgsForCall(0)).To(Equal("resolved"))
		})

		It("AC2: increments conflict paths counter by 1 (one conflicted file)", func() {
			Expect(pg.Pull(ctx)).To(BeNil())
			Expect(fakeMetrics.IncConflictPathsCallCount()).To(Equal(1))
			Expect(fakeMetrics.IncConflictPathsArgsForCall(0)).To(Equal(1))
		})

		It("AC2: nothing unpushed after resolved merge", func() {
			Expect(pg.Pull(ctx)).To(BeNil())
			unpushed := strings.TrimSpace(gitOutputStr(workDir, "log", "@{u}..HEAD", "--oneline"))
			Expect(unpushed).To(BeEmpty())
		})
	})

	Context("diverged, same-line conflict, resolver returns error (AC3 — quarantine path)", func() {
		var stubResolver *mocks.FakeConflictResolver

		BeforeEach(func() {
			externalPush("conflict.txt", "remote content\n")
			writeLocalCommit(workDir, "conflict.txt", "local content\n")

			// Wire a stub resolver that always errors. Per spec, the puller
			// no longer aborts on the first resolver failure — it quarantines
			// the file via git mv (or the read+rm+write+add fallback for
			// conflicted files) and continues the merge.
			stubResolver = &mocks.FakeConflictResolver{}
			stubResolver.ResolveReturns(errors.New("stub resolver failed"))
			pg = git.New(workDir, fakeMetrics, libtime.NewCurrentDateTime(), "", stubResolver)
		})

		It("AC3: Pull returns nil; merge commits with file in quarantined[]", func() {
			Expect(pg.Pull(ctx)).To(BeNil())
			commitMsg := strings.TrimSpace(gitOutputStr(workDir, "log", "-1", "--format=%s"))
			Expect(commitMsg).To(MatchRegexp(
				`^merge: resolved=\[\] quarantined=\[[^\]]*conflict\.txt\]$`,
			))
		})

		It("AC3: resolver-failed file is moved to _conflicts/", func() {
			Expect(pg.Pull(ctx)).To(BeNil())
			qGlob, _ := filepath.Glob(
				filepath.Join(workDir, "_conflicts", "conflict.txt.*.quarantined"),
			)
			Expect(
				qGlob,
			).To(HaveLen(1), "exactly one _conflicts/conflict.txt.<ts>.quarantined must exist; got: %v", qGlob)
			_, statErr := os.Stat(filepath.Join(workDir, "conflict.txt"))
			Expect(os.IsNotExist(statErr)).To(BeTrue(),
				"original conflict.txt must not exist after quarantine")
		})

		It("AC3: repo is clean after committed merge (on branch, no MERGE_HEAD)", func() {
			Expect(pg.Pull(ctx)).To(BeNil())
			_, statErr := os.Stat(filepath.Join(workDir, ".git", "MERGE_HEAD"))
			Expect(
				os.IsNotExist(statErr),
			).To(BeTrue(), ".git/MERGE_HEAD must not exist after committed merge")
			head := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "--abbrev-ref", "HEAD"))
			Expect(head).NotTo(Equal("HEAD"), "HEAD must be on a branch")
		})

		It("AC3: increments merge outcome resolved counter exactly once", func() {
			Expect(pg.Pull(ctx)).To(BeNil())
			Expect(fakeMetrics.IncMergeOutcomeCallCount()).To(Equal(1))
			Expect(fakeMetrics.IncMergeOutcomeArgsForCall(0)).To(Equal("resolved"))
		})

		It("AC5: seam is genuinely pluggable — stub resolver Resolve was called once", func() {
			Expect(pg.Pull(ctx)).To(BeNil())
			Expect(stubResolver.ResolveCallCount()).To(Equal(1),
				"resolver.Resolve must be called exactly once per conflict merge attempt")
			_, paths := stubResolver.ResolveArgsForCall(0)
			Expect(paths).To(ContainElement("conflict.txt"))
		})
	})

	Context(
		"diverged, 1 of 11 conflicted files has invalid frontmatter (per-file quarantine)",
		func() {
			var (
				corruptPath = "note-00.md"
				workDir     string
				seedFiles   []string
				pgLocal     git.Git
			)
			BeforeEach(func() {
				var localEdit, externalPush func(file, content string)
				var qCleanup func()
				workDir, localEdit, externalPush, seedFiles, qCleanup = setupQuarantineFixture(11)
				DeferCleanup(qCleanup)

				// Push a divergent valid version of every file to the remote.
				for i, f := range seedFiles {
					externalPush(
						f,
						fmt.Sprintf("---\nshared: remote-%d\n---\nremote body %d\n", i, i),
					)
				}
				// Local divergence: invalid YAML on the corrupt file, valid divergence on the 10 clean ones.
				for i, f := range seedFiles {
					if f == corruptPath {
						localEdit(f, "---\nbad: : : colon: storm\n---\nlocal corrupt body\n")
					} else {
						localEdit(
							f,
							fmt.Sprintf("---\nshared: local-%d\n---\nlocal body %d\n", i, i),
						)
					}
				}

				pgLocal = git.New(
					workDir,
					metrics.NewMetrics(),
					libtime.NewCurrentDateTime(),
					"",
					git.NewYAMLMergeResolver(workDir, metrics.NewMetrics()),
				)
			})

			It(
				"AC1-AC6 + AC12: Pull returns nil; 1 file quarantined; 10 resolved; commit message in fixed format",
				func() {
					logs, restore := captureSlogLogs()
					defer restore()

					// AC4 setup: capture the pre-Pull baseline of the process-global counter
					// so the post-Pull assertion is a delta, not an absolute value.
					beforeQuarantined := gatherQuarantinedFiles()
					Expect(pgLocal.Pull(ctx)).To(BeNil())
					afterQuarantined := gatherQuarantinedFiles()
					Expect(afterQuarantined-beforeQuarantined).To(Equal(1.0),
						"git_rest_quarantined_files_total must increment by exactly 1 in this test")

					// AC2: corrupt file is in _conflicts/ tree; no longer at the original path.
					// Spec evidence: filepath.Glob finds exactly one matching quarantine file;
					// os.Stat of the original path returns IsNotExist. The quarantine
					// destination is "_conflicts/note-00.<ts>.md" (the timestamp is inserted
					// BEFORE the final ".md" — see quarantineDestPath in pkg/git/git.go).
					stripped := strings.TrimSuffix(corruptPath, ".md")
					qGlob, _ := filepath.Glob(
						filepath.Join(workDir, "_conflicts", stripped+".*.md"),
					)
					Expect(
						qGlob,
					).To(HaveLen(1), "exactly one _conflicts/<name-without-md>.<ts>.md must exist; got: %v", qGlob)
					_, statErr := os.Stat(filepath.Join(workDir, corruptPath))
					Expect(os.IsNotExist(statErr)).To(BeTrue(),
						"corrupt file must not exist at original path after quarantine")

					// AC3: 10 clean files at original paths with merged content (local body preserved).
					for i := 1; i < 11; i++ {
						name := fmt.Sprintf("note-%02d.md", i)
						abs := filepath.Join(workDir, name)
						data, readErr := os.ReadFile(abs)
						Expect(readErr).NotTo(HaveOccurred())
						Expect(string(data)).To(ContainSubstring(fmt.Sprintf("local body %d", i)))
					}

					// AC5: exactly one merge commit on top of pre-test HEAD; clean working tree.
					// The pre-test HEAD is the seed commit (one commit at the start of the test).
					// After Pull, there should be 1 merge commit plus 10+11 = 21 incoming commits.
					// Use git log to count merge commits on the local branch since the test started.
					preTestHead := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "HEAD@{1}"))
					mergeCount := strings.TrimSpace(
						gitOutputStr(
							workDir,
							"rev-list",
							"--count",
							"--merges",
							"HEAD",
							"^"+preTestHead,
						),
					)
					Expect(mergeCount).To(Equal("1"),
						"expected exactly one merge commit on top of pre-test HEAD, got: %s", mergeCount)
					Expect(strings.TrimSpace(gitOutputStr(workDir, "status", "--porcelain"))).
						To(BeEmpty(), "working tree must be clean after commit")

					// AC6: WARN log line naming the corrupt path and the substring "quarantined".
					logStr := logs.String()
					Expect(logStr).To(ContainSubstring("quarantined conflicted file"))
					Expect(logStr).To(ContainSubstring(corruptPath))

					// AC12: commit message matches the fixed format and lists the corrupt path
					// in quarantined plus all 10 clean paths in resolved (sorted alphabetically).
					commitMsg := strings.TrimSpace(
						gitOutputStr(workDir, "log", "-1", "--format=%s"),
					)
					Expect(commitMsg).To(MatchRegexp(
						`^merge: resolved=\[[^\]]*\] quarantined=\[[^\]]*\]$`,
					))
					Expect(commitMsg).To(ContainSubstring("quarantined=[" + corruptPath + "]"))
					for i := 1; i < 11; i++ {
						name := fmt.Sprintf("note-%02d.md", i)
						Expect(commitMsg).To(ContainSubstring(name),
							"clean path %s must be in commit message", name)
					}
				},
			)
		},
	)

	Context("diverged, _conflicts/ already exists as a regular file (pathological case)", func() {
		It(
			"AC8: Pull returns wrapped ErrConflictResolutionFailed; working tree is clean; ERROR log is emitted",
			func() {
				workDir, localEdit, externalPush, seedFiles, qCleanup := setupQuarantineFixture(1)
				defer qCleanup()
				pPath := seedFiles[0]

				// Capture slog output to assert the spec's failure-modes detection
				// requirement: ERROR log line is emitted on the `_conflicts/ is a
				// regular file` abort path (mirrors the WARN assertion in the
				// unsafe-path internal test for symmetry).
				logs, restoreLogs := captureSlogLogs()
				defer restoreLogs()

				// Force a single-file conflict with invalid frontmatter so the resolver fails.
				externalPush(pPath, "---\nshared: remote\n---\nremote body\n")
				localEdit(pPath, "---\nbad: : : colon: storm\n---\nlocal body\n")

				// Pre-create _conflicts/ as a regular file to trip the pre-flight stat check.
				conflictPath := filepath.Join(workDir, "_conflicts")
				Expect(os.WriteFile(conflictPath, []byte("not a directory"), 0o600)).To(Succeed())

				pgPath := git.New(
					workDir,
					fakeMetrics,
					libtime.NewCurrentDateTime(),
					"",
					git.NewYAMLMergeResolver(workDir, fakeMetrics),
				)

				err := pgPath.Pull(ctx)
				Expect(err).To(HaveOccurred())
				Expect(errors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue(),
					"expected wrapped ErrConflictResolutionFailed, got: %v", err)

				// Merge was aborted: no MERGE_HEAD, HEAD is on a branch.
				_, statErr := os.Stat(filepath.Join(workDir, ".git", "MERGE_HEAD"))
				Expect(
					os.IsNotExist(statErr),
				).To(BeTrue(), ".git/MERGE_HEAD must not exist after aborted merge")
				head := strings.TrimSpace(
					gitOutputStr(workDir, "rev-parse", "--abbrev-ref", "HEAD"),
				)
				Expect(head).NotTo(Equal("HEAD"), "HEAD must be on a branch after abort")

				// ERROR log line names the catastrophic config error (spec failure-modes row).
				Expect(logs.String()).To(ContainSubstring("_conflicts/ exists as a regular file"))
				Expect(logs.String()).To(ContainSubstring("level=ERROR"))
			},
		)
	})

	// AC11 (unsafe path) is tested in pkg/git/resolve_conflict_merge_test.go
	// (a separate internal test file in `package git`) because it requires injecting a
	// crafted conflictPaths list (containing "../escape.md") that a real git merge cannot
	// produce. The internal test file calls the unexported resolveConflictPaths helper
	// directly. See requirement 4b below for the file contents.
})

// captureSlogLogs swaps slog.Default() to a buffer-backed text handler for the
// duration of a test. Returns the buffer and a restore func (use with defer).
func captureSlogLogs() (*bytes.Buffer, func()) {
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return buf, func() { slog.SetDefault(prev) }
}

var _ = Describe("Entry-state recovery", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	Context("AC1: abandoned rebase (.git/rebase-merge/ present)", func() {
		It("aborts the abandoned rebase and completes pull successfully", func() {
			workDir, externalPush, cleanup := setupPullFixture()
			defer cleanup()

			// Non-conflicting divergence: remote pushes remote.txt, local commits local.txt.
			externalPush("remote.txt", "from remote\n")
			writeLocalCommit(workDir, "local.txt", "local only\n")
			runGit(workDir, "fetch")

			// Derive the upstream tracking ref without hardcoding "main".
			upstream := strings.TrimSpace(
				gitOutputStr(workDir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"),
			)

			// Force abandoned-rebase state via --exec false: git pauses the rebase after
			// the first commit (exec failure), leaving .git/rebase-merge/ populated.
			// Since the files don't conflict, after git rebase --abort the re-attempted
			// rebase in Pull will succeed cleanly.
			forceCmd := exec.Command("git", "rebase", "--exec", "false", upstream)
			forceCmd.Dir = workDir
			_ = forceCmd.Run() // error expected — we only want the side-effect

			_, statErr := os.Stat(filepath.Join(workDir, ".git", "rebase-merge"))
			Expect(
				statErr,
			).NotTo(HaveOccurred(), ".git/rebase-merge should exist after --exec false")

			logs, restore := captureSlogLogs()
			defer restore()

			pg := git.New(
				workDir,
				&noopMetrics{},
				libtime.NewCurrentDateTime(),
				"",
				git.NewMarkerResolver(workDir),
			)
			err := pg.Pull(ctx)

			Expect(err).NotTo(HaveOccurred(), "Pull should succeed after aborting abandoned rebase")
			head := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "--abbrev-ref", "HEAD"))
			Expect(head).NotTo(Equal("HEAD"), "HEAD must be on a branch after recovery")
			u := strings.TrimSpace(
				gitOutputStr(workDir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"),
			)
			Expect(u).To(ContainSubstring("origin/"), "@{u} must resolve after recovery")

			// AC4b: exactly one structured-log line for the recovery.
			logStr := logs.String()
			Expect(strings.Count(logStr, "recovered from abandoned rebase")).To(Equal(1),
				"expected exactly one 'recovered from abandoned rebase' log line, got: %s", logStr)
			Expect(logStr).To(ContainSubstring("branch="), "log line must include branch key")
		})
	})

	Context("AC2: bare detached HEAD (no rebase in progress, origin/HEAD set)", func() {
		It("checks out the default branch, sets upstream, and completes pull", func() {
			// Use git clone so refs/remotes/origin/HEAD is set automatically.
			remoteDir, err := os.MkdirTemp("", "git-remote-*")
			Expect(err).NotTo(HaveOccurred())

			// Seed the bare remote via a temp clone.
			seedDir, seedErr := os.MkdirTemp("", "git-seed-*")
			Expect(seedErr).NotTo(HaveOccurred())
			defer func() { _ = os.RemoveAll(seedDir) }()

			rg := func(dir string, args ...string) {
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				out, e := cmd.CombinedOutput()
				if e != nil {
					panic(string(out))
				}
			}
			rg(remoteDir, "init", "--bare")
			rg(seedDir, "init")
			rg(seedDir, "config", "user.email", "test@example.com")
			rg(seedDir, "config", "user.name", "Test")
			rg(seedDir, "remote", "add", "origin", remoteDir)
			rg(seedDir, "commit", "--allow-empty", "-m", "init")
			rg(seedDir, "push", "-u", "origin", "HEAD")

			// Clone so origin/HEAD is set automatically (git init+push does NOT set it).
			workDir, wdErr := os.MkdirTemp("", "git-work-*")
			Expect(wdErr).NotTo(HaveOccurred())
			defer func() {
				_ = os.RemoveAll(workDir)
				_ = os.RemoveAll(remoteDir)
			}()
			rg(workDir, "clone", remoteDir, ".")
			rg(workDir, "config", "user.email", "test@example.com")
			rg(workDir, "config", "user.name", "Test")

			// Detach HEAD (simulates operator running `git checkout HEAD --detach`).
			runGit(workDir, "checkout", "--detach", "HEAD")
			Expect(strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "--abbrev-ref", "HEAD"))).
				To(Equal("HEAD"), "fixture sanity: HEAD should be detached before Pull")

			logs, restore := captureSlogLogs()
			defer restore()

			pg := git.New(
				workDir,
				&noopMetrics{},
				libtime.NewCurrentDateTime(),
				"",
				git.NewMarkerResolver(workDir),
			)
			pullErr := pg.Pull(ctx)

			Expect(pullErr).NotTo(HaveOccurred())
			head := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "--abbrev-ref", "HEAD"))
			Expect(head).NotTo(Equal("HEAD"), "HEAD should be on a branch after recovery")
			u := strings.TrimSpace(
				gitOutputStr(workDir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"),
			)
			Expect(
				u,
			).To(ContainSubstring("origin/"), "@{u} must resolve after detached-HEAD recovery")

			// AC4b: exactly one structured-log line for the recovery.
			logStr := logs.String()
			Expect(strings.Count(logStr, "recovered from detached HEAD")).To(Equal(1),
				"expected exactly one 'recovered from detached HEAD' log line, got: %s", logStr)
			Expect(logStr).To(ContainSubstring("branch="), "log line must include branch key")
		})
	})

	Context("AC3 + AC4d: detached HEAD, refs/remotes/origin/HEAD absent", func() {
		It("returns ErrRepoUnrecoverable and errors.Is matches", func() {
			// setupPullFixture uses git-init+push, NOT git-clone, so origin/HEAD is not set.
			workDir, _, cleanup := setupPullFixture()
			defer cleanup()

			// Detach HEAD; explicitly delete origin/HEAD to guarantee it is absent.
			runGit(workDir, "checkout", "--detach", "HEAD")
			// git remote set-head --delete is idempotent (ok if ref didn't exist).
			delCmd := exec.Command("git", "remote", "set-head", "origin", "--delete")
			delCmd.Dir = workDir
			_ = delCmd.Run()

			pg := git.New(
				workDir,
				&noopMetrics{},
				libtime.NewCurrentDateTime(),
				"",
				git.NewMarkerResolver(workDir),
			)
			pullErr := pg.Pull(ctx)

			Expect(pullErr).To(HaveOccurred())
			Expect(errors.Is(pullErr, git.ErrRepoUnrecoverable)).To(BeTrue(),
				"expected ErrRepoUnrecoverable, got: %v", pullErr)
		})
	})

	Context("AC4a: healthy repo — state check is a no-op", func() {
		It(
			"Pull succeeds twice; HEAD stays on a branch, no side effects, no recovery logs",
			func() {
				workDir, _, cleanup := setupPullFixture()
				defer cleanup()

				pg := git.New(
					workDir,
					&noopMetrics{},
					libtime.NewCurrentDateTime(),
					"",
					git.NewMarkerResolver(workDir),
				)
				headBefore := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "HEAD"))

				logs, restore := captureSlogLogs()
				defer restore()

				Expect(pg.Pull(ctx)).NotTo(HaveOccurred())
				Expect(pg.Pull(ctx)).NotTo(HaveOccurred())

				headAfter := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "HEAD"))
				Expect(headAfter).To(Equal(headBefore), "HEAD SHA must not change on no-op pulls")
				head := strings.TrimSpace(
					gitOutputStr(workDir, "rev-parse", "--abbrev-ref", "HEAD"),
				)
				Expect(head).NotTo(Equal("HEAD"), "HEAD must remain on a branch")

				// AC4a: healthy repo emits ZERO recovery log lines.
				logStr := logs.String()
				Expect(logStr).NotTo(ContainSubstring("recovered from"),
					"healthy repo must not emit recovery log lines, got: %s", logStr)
			},
		)
	})
})

var _ = Describe("Git ConfigureUser", func() {
	var ctx context.Context
	var repoDir string
	var g git.Git

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		repoDir, err = os.MkdirTemp("", "git-configure-user-*")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = os.RemoveAll(repoDir) })

		runGit(repoDir, "init")
		g = git.New(
			repoDir,
			&noopMetrics{},
			libtime.NewCurrentDateTime(),
			"",
			git.NewMarkerResolver(repoDir),
		)
	})

	readConfig := func(key string) string {
		cmd := exec.Command("git", "config", "--local", key)
		cmd.Dir = repoDir
		out, _ := cmd.Output()
		return strings.TrimSpace(string(out))
	}

	It("sets both name and email when both are provided", func() {
		err := g.ConfigureUser(ctx, "Alice", "alice@example.com")
		Expect(err).NotTo(HaveOccurred())
		Expect(readConfig("user.name")).To(Equal("Alice"))
		Expect(readConfig("user.email")).To(Equal("alice@example.com"))
	})

	It("sets only name when email is empty", func() {
		err := g.ConfigureUser(ctx, "Bob", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(readConfig("user.name")).To(Equal("Bob"))
		Expect(readConfig("user.email")).To(BeEmpty())
	})

	It("sets only email when name is empty", func() {
		err := g.ConfigureUser(ctx, "", "carol@example.com")
		Expect(err).NotTo(HaveOccurred())
		Expect(readConfig("user.name")).To(BeEmpty())
		Expect(readConfig("user.email")).To(Equal("carol@example.com"))
	})

	It("does nothing when both name and email are empty", func() {
		err := g.ConfigureUser(ctx, "", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(readConfig("user.name")).To(BeEmpty())
		Expect(readConfig("user.email")).To(BeEmpty())
	})
})
