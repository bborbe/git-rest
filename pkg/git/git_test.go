// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/git"
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
		g = git.New(workDir, &noopMetrics{}, libtime.NewCurrentDateTime(), "")
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
			g := git.New(workDir, &noopMetrics{}, libtime.NewCurrentDateTime(), "")
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
		g := git.New(targetDir, &noopMetrics{}, libtime.NewCurrentDateTime(), "")

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
		g := git.New(targetDir, &noopMetrics{}, libtime.NewCurrentDateTime(), "")

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
		noRemoteGit = git.New(noRemoteDir, &noopMetrics{}, libtime.NewCurrentDateTime(), "")
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

		g := git.New(dir, &noopMetrics{}, libtime.NewCurrentDateTime(), "")
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
		pg = git.New(workDir, fakeMetrics, libtime.NewCurrentDateTime(), "")
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

	Context("diverged, no content conflict (rebase+push)", func() {
		BeforeEach(func() {
			externalPush("remote.txt", "from remote\n")
			writeLocalCommit(workDir, "local.txt", "local only\n")
		})

		It("rebases and pushes, leaving linear history with both commits", func() {
			Expect(pg.Pull(ctx)).To(BeNil())
			log := gitOutputStr(workDir, "log", "--oneline")
			Expect(log).To(ContainSubstring("remote.txt"))
			Expect(log).To(ContainSubstring("local.txt"))
			Expect(log).NotTo(ContainSubstring("Merge branch"))
		})

		It("leaves nothing unpushed after rebase", func() {
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

	Context("diverged, content conflict during rebase", func() {
		BeforeEach(func() {
			externalPush("conflict.txt", "remote content\n")
			writeLocalCommit(workDir, "conflict.txt", "local content\n")
		})

		It("returns a RebaseConflictError with the conflicting path", func() {
			err := pg.Pull(ctx)
			Expect(err).To(HaveOccurred())
			var conflictErr *git.RebaseConflictError
			Expect(errors.As(err, &conflictErr)).To(BeTrue())
			Expect(conflictErr.Path).To(Equal("conflict.txt"))
		})

		It("does not contain the git config hint substring", func() {
			err := pg.Pull(ctx)
			Expect(err).To(HaveOccurred())
			Expect(
				err.Error(),
			).NotTo(ContainSubstring("Need to specify how to reconcile divergent branches"))
		})

		It("increments the rebase conflict metric exactly once", func() {
			_ = pg.Pull(ctx)
			Expect(fakeMetrics.IncRebaseConflictCallCount()).To(Equal(1))
		})
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
		g = git.New(repoDir, &noopMetrics{}, libtime.NewCurrentDateTime(), "")
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
