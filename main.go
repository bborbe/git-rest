// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	"github.com/bborbe/run"
	libsentry "github.com/bborbe/sentry"
	"github.com/bborbe/service"
	libtime "github.com/bborbe/time"
	gorillamux "github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/bborbe/git-rest/pkg/factory"
	"github.com/bborbe/git-rest/pkg/git"
	"github.com/bborbe/git-rest/pkg/metrics"
	"github.com/bborbe/git-rest/pkg/puller"
)

func main() {
	app := &application{}
	os.Exit(service.Main(context.Background(), app, &app.SentryDSN, &app.SentryProxy))
}

type application struct {
	SentryDSN       string            `required:"false" arg:"sentry-dsn"        env:"SENTRY_DSN"        usage:"Sentry DSN"                                 display:"length"`
	SentryProxy     string            `required:"false" arg:"sentry-proxy"      env:"SENTRY_PROXY"      usage:"Sentry Proxy"`
	Listen          string            `required:"true"  arg:"listen"            env:"LISTEN"            usage:"HTTP listen address"                                         default:":8080"`
	Repo            string            `required:"true"  arg:"repo"              env:"REPO"              usage:"path to git repository on disk"`
	PullInterval    libtime.Duration  `required:"false" arg:"pull-interval"     env:"PULL_INTERVAL"     usage:"git pull interval"                                           default:"30s"`
	BuildGitVersion string            `required:"false" arg:"build-git-version" env:"BUILD_GIT_VERSION" usage:"Build Git version"                                           default:"dev"`
	BuildGitCommit  string            `required:"false" arg:"build-git-commit"  env:"BUILD_GIT_COMMIT"  usage:"Build Git commit hash"                                       default:"none"`
	BuildDate       *libtime.DateTime `required:"false" arg:"build-date"        env:"BUILD_DATE"        usage:"Build timestamp (RFC3339)"`
	GitSSHKey       git.SSHKeyPath    `required:"false" arg:"git-ssh-key"       env:"GIT_SSH_KEY"       usage:"Path to SSH private key for git operations"`
	GitRemoteURL    git.RemoteURL     `required:"false" arg:"git-remote-url"    env:"GIT_REMOTE_URL"    usage:"Git remote URL to clone from on startup"`
	GitUserName     string            `required:"false" arg:"git-user-name"     env:"GIT_USER_NAME"     usage:"Git author name for commits"`
	GitUserEmail    string            `required:"false" arg:"git-user-email"    env:"GIT_USER_EMAIL"    usage:"Git author email for commits"`
}

func (a *application) Run(ctx context.Context, sentryClient libsentry.Client) error {
	metrics.NewBuildInfoMetrics(a.BuildGitVersion, a.BuildGitCommit).SetBuildInfo(a.BuildDate)

	if err := a.bootstrap(ctx); err != nil {
		return errors.Wrap(ctx, err, "bootstrap failed")
	}

	gitClient, err := a.createGitClient(ctx)
	if err != nil {
		return errors.Wrap(ctx, err, "create git client failed")
	}

	return service.Run(ctx,
		a.createGitRefresher(gitClient),
		a.createHTTPServer(gitClient, metrics.NewMetrics()),
	)
}

func (a *application) bootstrap(ctx context.Context) error {
	if err := cleanupStaleLocks(ctx, a.Repo); err != nil {
		return errors.Wrap(ctx, err, "cleanup stale locks")
	}
	if err := a.initIfNeeded(ctx); err != nil {
		return errors.Wrap(ctx, err, "init if needed")
	}
	if err := a.cloneIfNeeded(ctx); err != nil {
		return errors.Wrap(ctx, err, "clone if needed")
	}
	if err := a.configureUserIfSet(ctx); err != nil {
		return errors.Wrap(ctx, err, "configure user if set")
	}
	if err := recoverUntracked(ctx, a.Repo); err != nil {
		return errors.Wrap(ctx, err, "recover untracked")
	}
	if err := syncOnStartup(ctx, a.Repo); err != nil {
		return errors.Wrap(ctx, err, "sync on startup")
	}
	return nil
}

// cleanupStaleLocks removes any *.lock files under repoDir/.git at startup.
// Single-replica StatefulSet means any lock present at boot is stale —
// the binary just started and holds no other handles. Best-effort:
// individual errors are logged but never abort startup.
// No-op when .git/ does not exist (pre-init / pre-clone).
func cleanupStaleLocks(ctx context.Context, repoDir string) error {
	gitDir := filepath.Join(repoDir, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return errors.Wrapf(ctx, err, "stat %s", gitDir)
	}
	return filepath.WalkDir(gitDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			slog.WarnContext(ctx, "walk error during lock cleanup", "path", path, "error", walkErr)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".lock") {
			return nil
		}
		rmErr := os.Remove(path) // #nosec G122 -- boot-time only, single-replica StatefulSet
		if rmErr != nil && !os.IsNotExist(rmErr) {
			slog.WarnContext(ctx, "failed to remove stale lock", "path", path, "error", rmErr)
			return nil
		}
		slog.InfoContext(ctx, "removed stale lock", "path", path)
		return nil
	})
}

// recoverUntracked detects untracked files in the working tree and commits
// them with a recovery message. Called from bootstrap() after init/clone/
// configure. git-rest is the sole writer (single-replica StatefulSet), so
// any untracked file at startup is an orphan partial write whose `git add`
// never ran (e.g. process killed between os.WriteFile and the commit step).
//
// Push is NOT performed here — the periodic puller and the next API call's
// push already handle remote sync; doing it here would duplicate retry logic.
//
// Best-effort: errors are logged and do NOT abort startup. A failure here
// just means readiness will fall back to the existing 503 wait until manual
// intervention; that's no worse than today.
//
// No-op when:
//   - .git/ does not exist (pre-init / pre-clone)
//   - the working tree is clean (no untracked files)
func recoverUntracked(ctx context.Context, repoDir string) error {
	gitDir := filepath.Join(repoDir, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return errors.Wrapf(ctx, err, "stat %s", gitDir)
	}

	out, err := runGitCmd(ctx, repoDir, "status", "--short")
	if err != nil {
		slog.WarnContext(ctx, "git status failed during untracked recovery", "error", err)
		return nil
	}
	if !hasUntracked(out) {
		return nil
	}

	slog.InfoContext(ctx, "recovering untracked files from prior crash")
	if _, err := runGitCmd(ctx, repoDir, "add", "-A"); err != nil {
		slog.WarnContext(ctx, "git add -A failed during recovery", "error", err)
		return nil
	}
	if _, err := runGitCmd(ctx, repoDir, "commit", "-m", "git-rest: recover untracked from prior crash"); err != nil {
		slog.WarnContext(ctx, "git commit failed during recovery", "error", err)
		return nil
	}
	slog.InfoContext(ctx, "recovered untracked files into a commit")
	return nil
}

// hasUntracked reports whether `git status --short` output contains any
// untracked-file lines (prefix `??`).
func hasUntracked(statusOutput string) bool {
	for _, line := range strings.Split(statusOutput, "\n") {
		if strings.HasPrefix(line, "??") {
			return true
		}
	}
	return false
}

// syncOnStartupTimeout is the hard ceiling for the boot-time sync.
const syncOnStartupTimeout = 60 * time.Second

// syncOnStartup runs `git pull` and then `git push` once at startup, after
// recoverUntracked. Brings the local working copy fully in sync with the
// remote before the HTTP server starts serving.
//
// No-op when:
//   - .git/ does not exist (pre-init)
//   - no remote is configured (local-only mode)
//
// Best-effort: only the catastrophic os.Stat(.git) error returns non-nil.
// All git network errors are warn-logged and never abort startup.
func syncOnStartup(parentCtx context.Context, repoDir string) error {
	gitDir := filepath.Join(repoDir, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return errors.Wrapf(parentCtx, err, "stat %s", gitDir)
	}

	ctx, cancel := context.WithTimeout(parentCtx, syncOnStartupTimeout)
	defer cancel()

	out, err := runGitCmd(ctx, repoDir, "remote")
	if err != nil {
		slog.WarnContext(ctx, "git remote check failed during startup sync", "error", err)
		return nil
	}
	if strings.TrimSpace(out) == "" {
		slog.InfoContext(ctx, "no remote configured, skipping startup sync")
		return nil
	}

	if _, err := runGitCmd(ctx, repoDir, "pull"); err != nil {
		slog.WarnContext(ctx, "startup git pull failed (puller will retry)", "error", err)
	} else {
		slog.InfoContext(ctx, "startup git pull succeeded")
	}

	if _, err := runGitCmd(ctx, repoDir, "push"); err != nil {
		slog.WarnContext(ctx, "startup git push failed (next API write will retry)", "error", err)
		return nil
	}
	slog.InfoContext(ctx, "startup git push succeeded")
	return nil
}

// runGitCmd runs `git -C repoDir <args>` and returns combined output.
// It exists so recoverUntracked stays self-contained in main.go, matching
// the no-pkg/git-dependency pattern used by cleanupStaleLocks.
func runGitCmd(ctx context.Context, repoDir string, args ...string) (string, error) {
	full := append([]string{"-C", repoDir}, args...)
	cmd := exec.CommandContext(
		ctx,
		"git",
		full...) // #nosec G204 -- args caller-controlled, internal use
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), errors.Wrapf(ctx, err, "git %v: %s", args, string(out))
	}
	return string(out), nil
}

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

func (a *application) cloneIfNeeded(ctx context.Context) error {
	if a.GitRemoteURL == "" {
		return nil
	}
	gitDir := filepath.Join(a.Repo, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		return nil
	}
	if err := os.MkdirAll(a.Repo, 0o750); err != nil { //nolint:gosec
		return errors.Wrapf(ctx, err, "create repo directory %s", a.Repo)
	}
	tmpGit := factory.CreateGitClient(
		a.Repo,
		metrics.NewMetrics(),
		libtime.NewCurrentDateTime(),
		a.GitSSHKey,
	)
	if err := tmpGit.Clone(ctx, a.GitRemoteURL); err != nil {
		return errors.Wrapf(ctx, err, "clone %s", a.GitRemoteURL)
	}
	return nil
}

func (a *application) configureUserIfSet(ctx context.Context) error {
	if a.GitUserName == "" && a.GitUserEmail == "" {
		return nil
	}
	gitDir := filepath.Join(a.Repo, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return errors.Wrapf(ctx, err, "repo %s has no .git directory", a.Repo)
	}
	gitClient := factory.CreateGitClient(
		a.Repo,
		metrics.NewMetrics(),
		libtime.NewCurrentDateTime(),
		a.GitSSHKey,
	)
	if err := gitClient.ConfigureUser(ctx, a.GitUserName, a.GitUserEmail); err != nil {
		return errors.Wrap(ctx, err, "configure git user")
	}
	return nil
}

func (a *application) createGitClient(ctx context.Context) (git.Git, error) {
	if _, err := os.Stat(filepath.Join(a.Repo, ".git")); err != nil {
		return nil, errors.Wrapf(ctx, err, "repo %s has no .git directory", a.Repo)
	}

	if a.GitSSHKey != "" {
		if _, err := os.Stat(string(a.GitSSHKey)); err != nil {
			return nil, errors.Wrapf(ctx, err, "ssh key file %s", a.GitSSHKey)
		}
	}

	return factory.CreateGitClient(
		a.Repo,
		metrics.NewMetrics(),
		libtime.NewCurrentDateTime(),
		a.GitSSHKey,
	), nil
}

func (a *application) createGitRefresher(gitClient git.Git) run.Func {
	return func(ctx context.Context) error {
		return puller.New(gitClient, a.PullInterval).Run(ctx)
	}
}

func (a *application) createHTTPServer(
	gitClient git.Git,
	m metrics.Metrics,
) run.Func {
	return func(ctx context.Context) error {
		getH := factory.CreateFilesGetHandler(gitClient)
		postH := factory.CreateFilesPostHandler(gitClient)
		deleteH := factory.CreateFilesDeleteHandler(gitClient)
		listH := factory.CreateFilesListHandler(gitClient)
		healthzH := factory.CreateHealthzHandler()
		readinessH := factory.CreateReadinessHandler(gitClient)

		router := gorillamux.NewRouter().SkipClean(true)
		router.Handle("/api/v1/files/{path:.*}", factory.CreateFilesDispatchHandler(getH, listH)).
			Methods(http.MethodGet)
		router.Handle("/api/v1/files/{path:.*}", postH).Methods(http.MethodPost)
		router.Handle("/api/v1/files/{path:.*}", deleteH).Methods(http.MethodDelete)
		router.Handle("/healthz", healthzH)
		router.Handle("/readiness", readinessH)
		router.Handle("/metrics", promhttp.Handler())

		return libhttp.NewServer(
			a.Listen,
			factory.CreateMetricsMiddleware(m, router),
			func(o *libhttp.ServerOptions) {
				o.ReadTimeout = 60 * time.Second
				o.WriteTimeout = 60 * time.Second
				o.IdleTimeout = 120 * time.Second
			},
		).Run(ctx)
	}
}
