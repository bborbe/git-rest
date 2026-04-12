// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
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
