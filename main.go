// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"net/http"
	"os"
	"time"

	libhttp "github.com/bborbe/http"
	"github.com/bborbe/run"
	libsentry "github.com/bborbe/sentry"
	"github.com/bborbe/service"
	libtime "github.com/bborbe/time"
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
	SentryDSN       string            `required:"false" arg:"sentry-dsn"        env:"SENTRY_DSN"        usage:"Sentry DSN"                     display:"length"`
	SentryProxy     string            `required:"false" arg:"sentry-proxy"      env:"SENTRY_PROXY"      usage:"Sentry Proxy"`
	Listen          string            `required:"true"  arg:"listen"            env:"LISTEN"            usage:"HTTP listen address"                             default:":8080"`
	Repo            string            `required:"true"  arg:"repo"              env:"REPO"              usage:"path to git repository on disk"`
	PullInterval    time.Duration     `required:"false" arg:"pull-interval"     env:"PULL_INTERVAL"     usage:"git pull interval"                               default:"30s"`
	BuildGitVersion string            `required:"false" arg:"build-git-version" env:"BUILD_GIT_VERSION" usage:"Build Git version"                               default:"dev"`
	BuildGitCommit  string            `required:"false" arg:"build-git-commit"  env:"BUILD_GIT_COMMIT"  usage:"Build Git commit hash"                           default:"none"`
	BuildDate       *libtime.DateTime `required:"false" arg:"build-date"        env:"BUILD_DATE"        usage:"Build timestamp (RFC3339)"`
}

func (a *application) Run(ctx context.Context, sentryClient libsentry.Client) error {
	if _, err := os.Stat(a.Repo); err != nil {
		return err
	}

	metrics.NewBuildInfoMetrics(a.BuildGitVersion, a.BuildGitCommit).SetBuildInfo(a.BuildDate)

	gitClient := git.New(a.Repo)

	return service.Run(ctx,
		a.createHTTPServer(gitClient, sentryClient),
		a.createPuller(gitClient),
	)
}

func (a *application) createHTTPServer(gitClient git.Git, _ libsentry.Client) run.Func {
	return func(ctx context.Context) error {
		getH := factory.CreateFilesGetHandler(gitClient)
		postH := factory.CreateFilesPostHandler(gitClient)
		deleteH := factory.CreateFilesDeleteHandler(gitClient)
		listH := factory.CreateFilesListHandler(gitClient)
		healthzH := factory.CreateHealthzHandler()
		readinessH := factory.CreateReadinessHandler(gitClient)

		mux := http.NewServeMux()

		// Files routes using Go 1.22+ method+path routing.
		mux.Handle("GET /api/v1/files/", factory.CreateFilesDispatchHandler(getH, listH))
		mux.Handle("POST /api/v1/files/", postH)
		mux.Handle("DELETE /api/v1/files/", deleteH)

		mux.Handle("/healthz", healthzH)
		mux.Handle("/readiness", readinessH)
		mux.Handle("/metrics", promhttp.Handler())

		return libhttp.NewServer(
			a.Listen,
			factory.CreateMetricsMiddleware(mux),
			func(o *libhttp.ServerOptions) {
				o.ReadTimeout = 60 * time.Second
				o.WriteTimeout = 60 * time.Second
				o.IdleTimeout = 120 * time.Second
			},
		).Run(ctx)
	}
}

func (a *application) createPuller(gitClient git.Git) run.Func {
	return func(ctx context.Context) error {
		return puller.New(gitClient, a.PullInterval).Run(ctx)
	}
}
