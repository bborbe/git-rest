// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"net/http"
	"time"

	libhttp "github.com/bborbe/http"
	liblog "github.com/bborbe/log"
	gorillamux "github.com/gorilla/mux"

	"github.com/bborbe/git-rest/pkg/factory"
	"github.com/bborbe/git-rest/pkg/git"
	"github.com/bborbe/git-rest/pkg/metrics"
	"github.com/bborbe/git-rest/pkg/puller"
)

// CleanupStaleLocks is exported for testing via the main_test package.
var CleanupStaleLocks = cleanupStaleLocks

// RecoverUntracked is exported for testing via the main_test package.
var RecoverUntracked = recoverUntracked

// SyncOnStartup is exported for testing via the main_test package.
var SyncOnStartup = syncOnStartup

// ResolveGitSSHCommand is exported for testing via the main_test package.
var ResolveGitSSHCommand = resolveGitSSHCommand

// CreateServerHandler returns an http.Handler wrapping the probe routes router,
// enabling integration tests to exercise the real server construction path.
func CreateServerHandler(
	gitClient git.Git,
	m metrics.Metrics,
	pullState *puller.PullStateCache,
) http.Handler {
	router := gorillamux.NewRouter().SkipClean(true)

	// Probe routes — always unauthenticated (kubelet + Prometheus have no secret).
	router.Handle("/healthz", factory.CreateHealthzHandler())
	router.Handle("/readiness", factory.CreateReadinessHandler(pullState))
	router.Handle("/gc", libhttp.NewGarbageCollectorHandler())
	router.Handle(
		"/setloglevel/{level}",
		liblog.NewSetLoglevelHandler(
			context.Background(),
			liblog.NewLogLevelSetter(2, 5*time.Minute),
		),
	)

	return factory.CreateMetricsMiddleware(m, router)
}
