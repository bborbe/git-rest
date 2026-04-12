// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

import (
	"net/http"

	libhttp "github.com/bborbe/http"
	libtime "github.com/bborbe/time"

	"github.com/bborbe/git-rest/pkg/git"
	"github.com/bborbe/git-rest/pkg/handler"
	"github.com/bborbe/git-rest/pkg/metrics"
)

// CreateGitClient returns a Git implementation for the given repository path.
func CreateGitClient(
	repoPath string,
	m metrics.Metrics,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
	sshKeyPath git.SSHKeyPath,
) git.Git {
	return git.New(repoPath, m, currentDateTimeGetter, sshKeyPath)
}

// CreateFilesGetHandler returns an http.Handler for GET /api/v1/files/{path}.
func CreateFilesGetHandler(g git.Git) http.Handler {
	return libhttp.NewJSONErrorHandler(handler.NewFilesGetHandler(g))
}

// CreateFilesPostHandler returns an http.Handler for POST /api/v1/files/{path}.
func CreateFilesPostHandler(g git.Git) http.Handler {
	return libhttp.NewJSONErrorHandler(handler.NewFilesPostHandler(g))
}

// CreateFilesDeleteHandler returns an http.Handler for DELETE /api/v1/files/{path}.
func CreateFilesDeleteHandler(g git.Git) http.Handler {
	return libhttp.NewJSONErrorHandler(handler.NewFilesDeleteHandler(g))
}

// CreateFilesListHandler returns an http.Handler for GET /api/v1/files/ with glob query param.
func CreateFilesListHandler(g git.Git) http.Handler {
	return libhttp.NewJSONErrorHandler(handler.NewFilesListHandler(g))
}

// CreateHealthzHandler returns an http.Handler for GET /healthz.
func CreateHealthzHandler() http.Handler {
	return libhttp.NewPrintHandler("OK")
}

// CreateReadinessHandler returns an http.Handler for GET /readiness.
func CreateReadinessHandler(g git.Git) http.Handler {
	return libhttp.NewJSONErrorHandler(handler.NewReadinessHandler(g))
}

// CreateFilesDispatchHandler returns a handler that routes between get and list.
func CreateFilesDispatchHandler(getH, listH http.Handler) http.Handler {
	return handler.NewFilesDispatchHandler(getH, listH)
}

// CreateMetricsMiddleware wraps next with Prometheus HTTP request counting.
func CreateMetricsMiddleware(m metrics.Metrics, next http.Handler) http.Handler {
	return handler.NewMetricsMiddleware(m, next)
}
