// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
	"github.com/onsi/gomega/gexec"

	main "github.com/bborbe/git-rest"
)

//go:generate go run -mod=mod github.com/maxbrunsfeld/counterfeiter/v6 -generate

var _ = Describe("Main", func() {
	It("Compiles", func() {
		var err error
		_, err = gexec.Build(".", "-mod=mod")
		Expect(err).NotTo(HaveOccurred())
	})
})

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

	headSHA := func() string {
		cmd := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD")
		out, err := cmd.Output()
		Expect(err).NotTo(HaveOccurred())
		return string(out)
	}

	lastCommitMsg := func() string {
		cmd := exec.Command("git", "-C", repoDir, "log", "-1", "--pretty=%s")
		out, err := cmd.Output()
		Expect(err).NotTo(HaveOccurred())
		return string(out)
	}

	isTracked := func(name string) bool {
		cmd := exec.Command("git", "-C", repoDir, "ls-files", "--error-unmatch", name)
		return cmd.Run() == nil
	}

	Context("when .git/ does not exist", func() {
		It("returns nil", func() {
			Expect(main.RecoverUntracked(ctx, repoDir, "")).To(Succeed())
		})
	})

	Context("when working tree is clean", func() {
		BeforeEach(func() { initRepo() })
		It("returns nil and adds no commit", func() {
			before := headSHA()
			Expect(main.RecoverUntracked(ctx, repoDir, "")).To(Succeed())
			Expect(headSHA()).To(Equal(before))
		})
	})

	Context("when a single untracked file exists", func() {
		BeforeEach(func() {
			initRepo()
			Expect(
				os.WriteFile(filepath.Join(repoDir, "orphan.md"), []byte("data"), 0o644),
			).To(Succeed())
		})
		It("commits it with the recovery message", func() {
			Expect(main.RecoverUntracked(ctx, repoDir, "")).To(Succeed())
			Expect(isTracked("orphan.md")).To(BeTrue())
			Expect(lastCommitMsg()).To(ContainSubstring("recover untracked from prior crash"))
		})
	})

	Context("when multiple untracked files in nested directories exist", func() {
		BeforeEach(func() {
			initRepo()
			nested := filepath.Join(repoDir, "30 Analysis", "dev")
			Expect(os.MkdirAll(nested, 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(nested, "a.md"), []byte("a"), 0o644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(repoDir, "b.md"), []byte("b"), 0o644)).To(Succeed())
		})
		It("commits all of them in one commit", func() {
			before := headSHA()
			Expect(main.RecoverUntracked(ctx, repoDir, "")).To(Succeed())
			Expect(isTracked("30 Analysis/dev/a.md")).To(BeTrue())
			Expect(isTracked("b.md")).To(BeTrue())
			Expect(headSHA()).NotTo(Equal(before))
			Expect(lastCommitMsg()).To(ContainSubstring("recover untracked from prior crash"))
		})
	})

	Context("when an untracked file and a tracked-but-modified file both exist", func() {
		BeforeEach(func() {
			initRepo()
			// tracked file that is modified
			Expect(
				os.WriteFile(filepath.Join(repoDir, ".gitkeep"), []byte("modified"), 0o644),
			).To(Succeed())
			// untracked file
			Expect(
				os.WriteFile(filepath.Join(repoDir, "orphan.md"), []byte("new"), 0o644),
			).To(Succeed())
		})
		It("commits both in the recovery commit", func() {
			Expect(main.RecoverUntracked(ctx, repoDir, "")).To(Succeed())
			Expect(isTracked("orphan.md")).To(BeTrue())
			Expect(lastCommitMsg()).To(ContainSubstring("recover untracked from prior crash"))
		})
	})
})

var _ = Describe("SyncOnStartup", func() {
	var (
		ctx     context.Context
		repoDir string
	)

	BeforeEach(func() {
		ctx = context.Background()
		repoDir = GinkgoT().TempDir()
	})

	initRepoWithLocalRemote := func() (string, string) {
		remoteDir := filepath.Join(GinkgoT().TempDir(), "remote.git")
		localDir := filepath.Join(GinkgoT().TempDir(), "repo")
		run := func(dir string, args ...string) {
			full := append([]string{"-C", dir}, args...)
			cmd := exec.Command("git", full...)
			out, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), string(out))
		}
		Expect(os.MkdirAll(remoteDir, 0o755)).To(Succeed())
		cmd := exec.Command("git", "init", "--bare", "--initial-branch=main", remoteDir)
		Expect(cmd.Run()).To(Succeed())

		cmd = exec.Command("git", "clone", remoteDir, localDir)
		Expect(cmd.Run()).To(Succeed())
		run(localDir, "config", "user.email", "test@example.com")
		run(localDir, "config", "user.name", "Test")
		run(localDir, "checkout", "-b", "main")
		Expect(os.WriteFile(filepath.Join(localDir, ".gitkeep"), nil, 0o644)).To(Succeed())
		run(localDir, "add", ".gitkeep")
		run(localDir, "commit", "-m", "init")
		run(localDir, "push", "-u", "origin", "main")
		return remoteDir, localDir
	}

	remoteLogCount := func(remoteDir string) int {
		cmd := exec.Command("git", "-C", remoteDir, "log", "--oneline")
		out, err := cmd.Output()
		Expect(err).NotTo(HaveOccurred())
		lines := 0
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if l != "" {
				lines++
			}
		}
		return lines
	}

	localLogCount := func(dir string) int {
		cmd := exec.Command("git", "-C", dir, "log", "--oneline")
		out, err := cmd.Output()
		Expect(err).NotTo(HaveOccurred())
		lines := 0
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if l != "" {
				lines++
			}
		}
		return lines
	}

	Context("when .git/ does not exist", func() {
		It("returns nil", func() {
			Expect(main.SyncOnStartup(ctx, repoDir, "")).To(Succeed())
		})
	})

	Context("when .git/ exists but no remote configured", func() {
		BeforeEach(func() {
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
		})
		It("returns nil without error", func() {
			Expect(main.SyncOnStartup(ctx, repoDir, "")).To(Succeed())
		})
	})

	Context("when repo has remote and working tree is clean and synced", func() {
		It("returns nil", func() {
			_, localDir := initRepoWithLocalRemote()
			Expect(main.SyncOnStartup(ctx, localDir, "")).To(Succeed())
		})
	})

	Context("when repo has one local commit ahead of remote", func() {
		It("pushes the commit and returns nil", func() {
			remoteDir, localDir := initRepoWithLocalRemote()
			before := remoteLogCount(remoteDir)

			run := func(args ...string) {
				full := append([]string{"-C", localDir}, args...)
				cmd := exec.Command("git", full...)
				out, err := cmd.CombinedOutput()
				Expect(err).NotTo(HaveOccurred(), string(out))
			}
			Expect(
				os.WriteFile(filepath.Join(localDir, "recovery.md"), []byte("data"), 0o644),
			).To(Succeed())
			run("add", "recovery.md")
			run("commit", "-m", "git-rest: recover untracked from prior crash")

			Expect(main.SyncOnStartup(ctx, localDir, "")).To(Succeed())
			Expect(remoteLogCount(remoteDir)).To(Equal(before + 1))
		})
	})

	Context("when remote has a new commit ahead of local", func() {
		It("pulls the commit and returns nil", func() {
			remoteDir, localDir := initRepoWithLocalRemote()

			// Add a commit directly to the remote via a second clone
			secondDir := filepath.Join(GinkgoT().TempDir(), "second")
			cmd := exec.Command("git", "clone", remoteDir, secondDir)
			Expect(cmd.Run()).To(Succeed())
			run2 := func(args ...string) {
				full := append([]string{"-C", secondDir}, args...)
				c := exec.Command("git", full...)
				out, err := c.CombinedOutput()
				Expect(err).NotTo(HaveOccurred(), string(out))
			}
			run2("config", "user.email", "test@example.com")
			run2("config", "user.name", "Test")
			Expect(
				os.WriteFile(filepath.Join(secondDir, "remote-file.md"), []byte("remote"), 0o644),
			).To(Succeed())
			run2("add", "remote-file.md")
			run2("commit", "-m", "remote commit")
			run2("push", "origin", "main")

			before := localLogCount(localDir)
			Expect(main.SyncOnStartup(ctx, localDir, "")).To(Succeed())
			Expect(localLogCount(localDir)).To(Equal(before + 1))
		})
	})

	Context("when both local and remote have new commits (disjoint files)", func() {
		It("returns nil (pull warns, push is non-fast-forward, both warn-logged)", func() {
			remoteDir, localDir := initRepoWithLocalRemote()

			// Local commit
			run := func(args ...string) {
				full := append([]string{"-C", localDir}, args...)
				cmd := exec.Command("git", full...)
				out, err := cmd.CombinedOutput()
				Expect(err).NotTo(HaveOccurred(), string(out))
			}
			Expect(
				os.WriteFile(filepath.Join(localDir, "local-file.md"), []byte("local"), 0o644),
			).To(Succeed())
			run("add", "local-file.md")
			run("commit", "-m", "local commit")

			// Remote commit via second clone
			secondDir := filepath.Join(GinkgoT().TempDir(), "second")
			cmd := exec.Command("git", "clone", remoteDir, secondDir)
			Expect(cmd.Run()).To(Succeed())
			run2 := func(args ...string) {
				full := append([]string{"-C", secondDir}, args...)
				c := exec.Command("git", full...)
				out, err := c.CombinedOutput()
				Expect(err).NotTo(HaveOccurred(), string(out))
			}
			run2("config", "user.email", "test@example.com")
			run2("config", "user.name", "Test")
			Expect(
				os.WriteFile(filepath.Join(secondDir, "remote-file.md"), []byte("remote"), 0o644),
			).To(Succeed())
			run2("add", "remote-file.md")
			run2("commit", "-m", "remote commit")
			run2("push", "origin", "main")

			// Pull will fail (divergent branches, no strategy configured); push will also
			// fail (non-fast-forward). Both must be warn-logged; function must return nil.
			Expect(main.SyncOnStartup(ctx, localDir, "")).To(Succeed())
		})
	})

	Context("when pull fails (broken remote)", func() {
		It("still returns nil", func() {
			_, localDir := initRepoWithLocalRemote()

			// Create a local commit ahead of remote
			run := func(args ...string) {
				full := append([]string{"-C", localDir}, args...)
				cmd := exec.Command("git", full...)
				out, err := cmd.CombinedOutput()
				Expect(err).NotTo(HaveOccurred(), string(out))
			}
			Expect(
				os.WriteFile(filepath.Join(localDir, "recovery.md"), []byte("data"), 0o644),
			).To(Succeed())
			run("add", "recovery.md")
			run("commit", "-m", "git-rest: recover untracked from prior crash")

			// Break the remote URL
			run("remote", "set-url", "origin", "/nonexistent/path")

			Expect(main.SyncOnStartup(ctx, localDir, "")).To(Succeed())
		})
	})
})

var _ = Describe("ResolveGitSSHCommand", func() {
	Context("when both gitSSHCommand and sshKeyPath are empty", func() {
		It("returns empty string", func() {
			Expect(main.ResolveGitSSHCommand("", "")).To(Equal(""))
		})
	})

	Context("when only sshKeyPath is set", func() {
		It("returns the derived ssh command with the key path", func() {
			result := main.ResolveGitSSHCommand("", "/ssh/id_ed25519")
			Expect(
				result,
			).To(Equal("ssh -i /ssh/id_ed25519 -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no"))
		})
	})

	Context("when only gitSSHCommand is set", func() {
		It("returns it verbatim", func() {
			result := main.ResolveGitSSHCommand("ssh -vvv -i /custom/key", "")
			Expect(result).To(Equal("ssh -vvv -i /custom/key"))
		})
	})

	Context("when both gitSSHCommand and sshKeyPath are set", func() {
		It("returns gitSSHCommand verbatim (override wins)", func() {
			result := main.ResolveGitSSHCommand("ssh -vvv -i /custom/key", "/ssh/id_ed25519")
			Expect(result).To(Equal("ssh -vvv -i /custom/key"))
		})
	})
})

func TestSuite(t *testing.T) {
	time.Local = time.UTC
	format.TruncatedDiff = false
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	suiteConfig.Timeout = 60 * time.Second
	RunSpecs(t, "Main Suite", suiteConfig, reporterConfig)
}
