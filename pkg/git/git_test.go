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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/pkg/git"
)

// noopMetrics satisfies metrics.Metrics without recording anything, for use in git tests.
type noopMetrics struct{}

func (n *noopMetrics) ObserveGitOperation(_ string, _ float64) {}

func (n *noopMetrics) IncGitOperationError(_ string) {}

func (n *noopMetrics) IncHTTPRequest(_, _, _ string) {}

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
		g = git.New(workDir, &noopMetrics{})
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

		It("returns ErrNotFound for non-existent file", func() {
			err := g.DeleteFile(ctx, "doesnotexist.txt")
			Expect(err).To(MatchError(git.ErrNotFound))
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
