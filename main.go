// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/felixge/httpsnoop"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/bborbe/git-rest/pkg/factory"
	"github.com/bborbe/git-rest/pkg/git"
	"github.com/bborbe/git-rest/pkg/metrics"
	"github.com/bborbe/git-rest/pkg/puller"
)

func main() {
	repo := flag.String("repo", "", "path to the git repository on disk (required)")
	addr := flag.String("addr", ":8080", "HTTP listen address")
	pullInterval := flag.Duration("pull-interval", 30*time.Second, "how often to run git pull")
	flag.Parse()

	if *repo == "" {
		fmt.Fprintln(os.Stderr, "error: --repo is required")
		flag.Usage()
		os.Exit(1)
	}

	if _, err := os.Stat(*repo); err != nil {
		fmt.Fprintf(os.Stderr, "error: repo directory does not exist: %s\n", *repo)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gitClient := git.New(*repo)

	getH := factory.CreateFilesGetHandler(gitClient)
	postH := factory.CreateFilesPostHandler(gitClient)
	deleteH := factory.CreateFilesDeleteHandler(gitClient)
	listH := factory.CreateFilesListHandler(gitClient)
	healthzH := factory.CreateHealthzHandler()
	readinessH := factory.CreateReadinessHandler(gitClient)

	mux := http.NewServeMux()

	// Files routes using Go 1.22+ method+path routing.
	mux.Handle("GET /api/v1/files/", filesDispatch(getH, listH))
	mux.Handle("POST /api/v1/files/", postH)
	mux.Handle("DELETE /api/v1/files/", deleteH)

	mux.Handle("/healthz", healthzH)
	mux.Handle("/readiness", readinessH)
	mux.Handle("/metrics", promhttp.Handler())

	server := &http.Server{
		Addr:              *addr,
		Handler:           metricsMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	p := puller.New(gitClient, *pullInterval)
	go func() {
		if err := p.Run(ctx); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "puller exited unexpectedly", "error", err)
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.ErrorContext(shutdownCtx, "server shutdown error", "error", err)
		}
	}()

	slog.Info("starting git-rest server", "addr", *addr, "repo", *repo)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

// filesDispatch routes GET /api/v1/files/ requests: glob query param → list, otherwise → get.
func filesDispatch(getH, listH http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("glob") {
			listH.ServeHTTP(w, r)
			return
		}
		getH.ServeHTTP(w, r)
	})
}

// metricsMiddleware records git_rest_http_requests_total after each request.
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := httpsnoop.CaptureMetrics(next, w, r)
		metrics.HTTPRequestsTotal.WithLabelValues(
			r.Method,
			r.URL.Path,
			strconv.Itoa(m.Code),
		).Inc()
	})
}
