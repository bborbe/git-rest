// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	libtime "github.com/bborbe/time"
	gorillamux "github.com/gorilla/mux"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
	"github.com/onsi/gomega/gexec"

	main "github.com/bborbe/git-rest"
	"github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/factory"
	"github.com/bborbe/git-rest/pkg/git"
	"github.com/bborbe/git-rest/pkg/metrics"
	"github.com/bborbe/git-rest/pkg/puller"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate

var _ = Describe("Main", func() {
	It("Compiles", func() {
		var err error
		_, err = gexec.Build(".", "-mod=mod")
		Expect(err).NotTo(HaveOccurred())
	})
})

var _ = Describe("ShutdownFlusher", func() {
	It("flushes pending writes on cancellation with a bounded context", func() {
		fakeGit := &mocks.FakeGit{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		Expect(main.CreateShutdownFlusher(fakeGit)(ctx)).To(Succeed())
		Expect(fakeGit.FlushPendingCallCount()).To(Equal(1))

		flushCtx := fakeGit.FlushPendingArgsForCall(0)
		deadline, ok := flushCtx.Deadline()
		Expect(ok).To(BeTrue())
		Expect(time.Until(deadline)).To(BeNumerically("~", 10*time.Second, time.Second))
	})

	It("logs the deadline line when the flush is cut short", func() {
		fakeGit := &mocks.FakeGit{}
		fakeGit.FlushPendingStub = func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}

		buf := &bytes.Buffer{}
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})))
		defer slog.SetDefault(prev)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		Expect(main.CreateShutdownFlusher(fakeGit)(ctx)).To(Succeed())
		Expect(buf.String()).To(ContainSubstring("git-rest: flush deadline exceeded"))
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

	newStartupGit := func(repoDir string) git.Git {
		return git.New(
			repoDir,
			metrics.NewMetrics(),
			libtime.NewCurrentDateTime(),
			"",
			git.NewMarkerResolver(repoDir),
		)
	}

	gitOutput := func(dir string, args ...string) string {
		full := append([]string{"-C", dir}, args...)
		out, err := exec.Command("git", full...).Output()
		Expect(err).NotTo(HaveOccurred())
		return string(out)
	}

	Context("when .git/ does not exist", func() {
		It("does not panic", func() {
			main.SyncOnStartup(ctx, newStartupGit(repoDir))
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
			main.SyncOnStartup(ctx, newStartupGit(repoDir))
		})
	})

	Context("when repo has remote and working tree is clean and synced", func() {
		It("returns nil", func() {
			_, localDir := initRepoWithLocalRemote()
			main.SyncOnStartup(ctx, newStartupGit(localDir))
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

			main.SyncOnStartup(ctx, newStartupGit(localDir))
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
			main.SyncOnStartup(ctx, newStartupGit(localDir))
			Expect(localLogCount(localDir)).To(Equal(before + 1))
		})
	})

	Context("when both local and remote have new commits (disjoint files)", func() {
		It("merges and pushes the diverged history", func() {
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

			// Pull merges the diverged history and pushes the merge commit.
			before := localLogCount(localDir)
			main.SyncOnStartup(ctx, newStartupGit(localDir))
			Expect(strings.TrimSpace(gitOutput(localDir, "status", "--porcelain"))).To(BeEmpty())
			// The merge commit plus the remote commit it brings into local history.
			Expect(localLogCount(localDir)).To(Equal(before + 2))
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

			main.SyncOnStartup(ctx, newStartupGit(localDir))
		})
	})

	Context("when the working tree is dirty and the branch is diverged at boot", func() {
		It("rescues the dirty tree, merges and pushes", func() {
			remoteDir, localDir := initRepoWithLocalRemote()
			runLocal := func(args ...string) {
				full := append([]string{"-C", localDir}, args...)
				cmd := exec.Command("git", full...)
				out, err := cmd.CombinedOutput()
				Expect(err).NotTo(HaveOccurred(), string(out))
			}
			Expect(os.WriteFile(
				filepath.Join(localDir, "local-file.md"), []byte("local"), 0o644,
			)).To(Succeed())
			runLocal("add", "local-file.md")
			runLocal("commit", "-m", "local commit")

			secondDir := filepath.Join(GinkgoT().TempDir(), "second")
			Expect(exec.Command("git", "clone", remoteDir, secondDir).Run()).To(Succeed())
			runSecond := func(args ...string) {
				full := append([]string{"-C", secondDir}, args...)
				cmd := exec.Command("git", full...)
				out, err := cmd.CombinedOutput()
				Expect(err).NotTo(HaveOccurred(), string(out))
			}
			runSecond("config", "user.email", "test@example.com")
			runSecond("config", "user.name", "Test")
			Expect(os.WriteFile(
				filepath.Join(secondDir, ".gitkeep"), []byte("remote\n"), 0o644,
			)).To(Succeed())
			runSecond("add", ".gitkeep")
			runSecond("commit", "-m", "remote commit")
			runSecond("push", "origin", "main")

			// Dirty the file the remote commit touches, so the diverged merge aborts.
			Expect(os.WriteFile(
				filepath.Join(localDir, ".gitkeep"), []byte("LOCAL UNCOMMITTED\n"), 0o644,
			)).To(Succeed())

			main.SyncOnStartup(ctx, newStartupGit(localDir))

			Expect(strings.TrimSpace(gitOutput(localDir, "status", "--porcelain"))).To(BeEmpty())
			Expect(gitOutput(localDir, "log", "--oneline")).To(ContainSubstring("local commit"))
			Expect(
				gitOutput(localDir, "for-each-ref", "--format=%(refname)", "refs/heads/rescue/"),
			).NotTo(BeEmpty())
			Expect(
				gitOutput(remoteDir, "for-each-ref", "--format=%(refname)", "refs/heads/rescue/"),
			).NotTo(BeEmpty())
			content, readErr := os.ReadFile(filepath.Join(localDir, ".gitkeep"))
			Expect(readErr).NotTo(HaveOccurred())
			Expect(content).To(Equal([]byte("remote\n")))
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

var _ = Describe("GatewaySecretRouting", func() {
	const secret = "routing-test-secret"

	var (
		wrapped     http.Handler
		fakeMetrics *mocks.FakeMetrics
		rec         *httptest.ResponseRecorder
	)

	BeforeEach(func() {
		rec = httptest.NewRecorder()
		fakeMetrics = &mocks.FakeMetrics{}

		// Mirror the topology of createHTTPServer:
		// - /api/v1 subrouter with auth middleware
		// - probe routes on root router (no auth)
		// - metrics middleware wraps EVERYTHING so it sees auth failures too
		router := gorillamux.NewRouter().SkipClean(true)

		apiRouter := router.PathPrefix("/api/v1").Subrouter()
		apiRouter.Use(factory.CreateGatewaySecretMiddleware(secret))

		apiRouter.HandleFunc("/files/{path:.*}", func(w http.ResponseWriter, r *http.Request) {
			// Echo whether X-Gateway-Secret reached the inner handler.
			if r.Header.Get("X-Gateway-Secret") != "" {
				w.WriteHeader(http.StatusInternalServerError) // test signal: secret leaked
				return
			}
			w.WriteHeader(http.StatusOK)
		})

		router.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		router.HandleFunc("/readiness", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		wrapped = factory.CreateMetricsMiddleware(fakeMetrics, router)
	})

	Context("API routes — missing X-Gateway-Initator", func() {
		It("returns 500 with exact body", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/README.md", nil)
			wrapped.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusInternalServerError))
			Expect(
				strings.TrimSpace(rec.Body.String()),
			).To(Equal("header 'X-Gateway-Initator' missing"))
		})

		It("metrics middleware records the 500", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/README.md", nil)
			wrapped.ServeHTTP(rec, req)
			Expect(fakeMetrics.IncHTTPRequestCallCount()).To(Equal(1))
			method, path, status := fakeMetrics.IncHTTPRequestArgsForCall(0)
			Expect(method).To(Equal(http.MethodGet))
			Expect(path).To(Equal("/api/v1/files/{path}"))
			Expect(status).To(Equal("500"))
		})
	})

	Context("API routes — wrong X-Gateway-Secret", func() {
		It("returns 401 with exact body", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/README.md", nil)
			req.Header.Set("X-Gateway-Initator", "integration-test")
			req.Header.Set("X-Gateway-Secret", "wrong-secret")
			wrapped.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusUnauthorized))
			Expect(
				strings.TrimSpace(rec.Body.String()),
			).To(Equal("secret in header 'X-Gateway-Secret' is invalid => access denied"))
		})

		It("metrics middleware records the 401", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/README.md", nil)
			req.Header.Set("X-Gateway-Initator", "integration-test")
			req.Header.Set("X-Gateway-Secret", "wrong-secret")
			wrapped.ServeHTTP(rec, req)
			Expect(fakeMetrics.IncHTTPRequestCallCount()).To(Equal(1))
			method, path, status := fakeMetrics.IncHTTPRequestArgsForCall(0)
			Expect(method).To(Equal(http.MethodGet))
			Expect(path).To(Equal("/api/v1/files/{path}"))
			Expect(status).To(Equal("401"))
		})
	})

	Context("API routes — correct headers", func() {
		It("reaches the inner handler and strips X-Gateway-Secret", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/README.md", nil)
			req.Header.Set("X-Gateway-Initator", "integration-test")
			req.Header.Set("X-Gateway-Secret", secret)
			wrapped.ServeHTTP(rec, req)
			// Inner handler returns 200 only when X-Gateway-Secret is absent
			Expect(rec.Code).To(Equal(http.StatusOK))
		})
	})

	Context("probe routes — always unauthenticated", func() {
		It("/healthz returns 200 with no headers", func() {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			wrapped.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusOK))
		})

		It("/readiness returns 200 with no headers", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			wrapped.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusOK))
		})
	})
})

var _ = Describe("AdminRoutes", func() {
	var (
		handler http.Handler
		rec     *httptest.ResponseRecorder
		repoDir string
	)

	BeforeEach(func() {
		rec = httptest.NewRecorder()
		repoDir = GinkgoT().TempDir()

		// Initialize a git repo so the app bootstrap passes
		run := func(args ...string) {
			full := append([]string{"-C", repoDir}, args...)
			cmd := exec.Command("git", full...)
			out, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), string(out))
		}
		run("init")
		run("config", "user.email", "test@example.com")
		run("config", "user.name", "Test")

		gitClient := factory.CreateGitClient(
			repoDir,
			&mocks.FakeMetrics{},
			libtime.NewCurrentDateTime(),
			"",
			git.NewMarkerResolver(repoDir),
		)
		// CreateServerHandler returns the http.Handler for the probe routes
		handler = main.CreateServerHandler(gitClient, &mocks.FakeMetrics{}, repoDir)
	})

	Context("GET /gc", func() {
		It("returns 200", func() {
			req := httptest.NewRequest(http.MethodGet, "/gc", nil)
			handler.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusOK))
		})
	})

	Context("GET /setloglevel/2", func() {
		It("returns 200", func() {
			req := httptest.NewRequest(http.MethodGet, "/setloglevel/2", nil)
			handler.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusOK))
		})
	})
})

var _ = Describe("Readiness integration", func() {
	var (
		handler http.Handler
		rec     *httptest.ResponseRecorder
		repoDir string
	)

	BeforeEach(func() {
		repoDir = GinkgoT().TempDir()
		rec = httptest.NewRecorder()
		gitClient := factory.CreateGitClient(
			repoDir,
			&mocks.FakeMetrics{},
			libtime.NewCurrentDateTime(),
			"",
			git.NewMarkerResolver(repoDir),
		)
		handler = main.CreateServerHandler(gitClient, &mocks.FakeMetrics{}, repoDir)
	})

	It("returns 200 'ok' on a readable repository path", func() {
		req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
		handler.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(Equal("ok"))
	})

	It("returns 200 on a cold start before any pull", func() {
		// No puller is ever started: readiness depends only on the path.
		req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
		handler.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(Equal("ok"))
	})

	It("returns 503 'repo path unreadable' when the repository path is missing", func() {
		Expect(os.RemoveAll(repoDir)).To(Succeed())
		req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
		handler.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusServiceUnavailable))
		Expect(rec.Body.String()).To(Equal("repo path unreadable"))
		Expect(rec.Body.String()).NotTo(MatchRegexp(`(?i)push|pull|sync|conflict|/tmp/`))
	})

	It("returns 200 while the puller records a push rejection", func() {
		fakeGit := &mocks.FakeGit{}
		fakeGit.PullStub = func(ctx context.Context) error {
			return errors.New("push failed")
		}
		p := puller.New(fakeGit, libtime.Duration(10*time.Millisecond), 0)
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		defer cancel()
		_ = p.Run(ctx)
		Expect(fakeGit.PullCallCount()).To(BeNumerically(">=", 1))

		req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
		handler.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(Equal("ok"))
	})

	It("returns 200 while the puller returns a rebase conflict", func() {
		fakeGit := &mocks.FakeGit{}
		fakeGit.PullStub = func(ctx context.Context) error {
			return &git.RebaseConflictError{Path: "tasks/foo.md"}
		}
		p := puller.New(fakeGit, libtime.Duration(10*time.Millisecond), 0)
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		defer cancel()
		_ = p.Run(ctx)
		Expect(fakeGit.PullCallCount()).To(BeNumerically(">=", 1))

		req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
		handler.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(Equal("ok"))
	})
})

// TestWriteCoalesceFlags exercises the three coalescing flags through the real
// argument parser. The flags exist only in struct tags, so a misspelled tag
// produces no compile error and is rejected only when an operator runs the
// binary — this test is the only guard against that.
func TestWriteCoalesceFlags(t *testing.T) {
	bin := buildGitRestTestBinary(t)

	t.Run("flags are accepted", func(t *testing.T) {
		cmd, stderr, done := startFlagTestBinary(
			t,
			bin,
			initFlagTestRepo(t),
			nil,
			"--write-coalesce=false",
			"--write-coalesce-delay=1ms",
			"--write-coalesce-max-delay=2ms",
		)
		assertBinaryKeepsRunning(t, cmd, stderr, done)
	})

	t.Run("WRITE_COALESCE env is accepted", func(t *testing.T) {
		cmd, stderr, done := startFlagTestBinary(
			t, bin, initFlagTestRepo(t), []string{"WRITE_COALESCE=false"},
		)
		assertBinaryKeepsRunning(t, cmd, stderr, done)
	})

	t.Run("invalid WRITE_COALESCE_DELAY is rejected at parse time", func(t *testing.T) {
		cmd := exec.Command(bin, "--repo="+initFlagTestRepo(t), "--listen=127.0.0.1:0")
		cmd.Env = append(os.Environ(), "WRITE_COALESCE_DELAY=bogus")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected non-zero exit for invalid duration, output=%s", out)
		}
		if !strings.Contains(string(out), "parse app failed") {
			t.Fatalf("expected parse app failed, got %s", out)
		}
	})
}

// buildGitRestTestBinary builds the git-rest binary. gexec.Build writes its
// artifact under os.TempDir, which is mounted noexec in this container, so the
// binary is built into a directory under the package dir instead.
func buildGitRestTestBinary(t *testing.T) string {
	t.Helper()
	binDir, err := os.MkdirTemp(".", ".gitrest-testbin-")
	if err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(binDir) })
	bin := filepath.Join(binDir, "git-rest")
	if out, err := exec.Command("go", "build", "-mod=mod", "-o", bin, ".").
		CombinedOutput(); err != nil {
		t.Fatalf("build binary: %v: %s", err, out)
	}
	return bin
}

// initFlagTestRepo creates an empty git repository the binary can serve.
func initFlagTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	out, err := exec.Command("git", "init", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("git init %s: %v: %s", dir, err, out)
	}
	return dir
}

// startFlagTestBinary starts the binary against repo and returns its process,
// captured stderr and a channel that receives the result of Wait.
func startFlagTestBinary(
	t *testing.T,
	bin, repo string,
	extraEnv []string,
	args ...string,
) (*exec.Cmd, *bytes.Buffer, <-chan error) {
	t.Helper()
	stderr := &bytes.Buffer{}
	cmd := exec.Command(
		bin,
		append([]string{"--repo=" + repo, "--listen=127.0.0.1:0"}, args...)...,
	)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start binary: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return cmd, stderr, done
}

// assertBinaryKeepsRunning fails when the binary exits within a second or
// reports an unknown flag, then kills it.
func assertBinaryKeepsRunning(
	t *testing.T,
	cmd *exec.Cmd,
	stderr *bytes.Buffer,
	done <-chan error,
) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("binary exited early: %v: stderr=%s", err, stderr.String())
	case <-time.After(time.Second):
	}
	_ = cmd.Process.Kill()
	<-done
	// Read the buffer only after Wait has returned: until then the process's
	// stderr copier goroutine is still writing into it.
	for _, marker := range []string{"flag provided but not defined", "unknown flag"} {
		if strings.Contains(stderr.String(), marker) {
			t.Fatalf("stderr contains %q: %s", marker, stderr.String())
		}
	}
}

func TestSuite(t *testing.T) {
	time.Local = time.UTC
	format.TruncatedDiff = false
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	suiteConfig.Timeout = 60 * time.Second
	RunSpecs(t, "Main Suite", suiteConfig, reporterConfig)
}
