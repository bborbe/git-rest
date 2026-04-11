// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

import (
	"net/http"

	"github.com/bborbe/git-rest/pkg/git"
	"github.com/bborbe/git-rest/pkg/handler"
)

// CreateFilesGetHandler returns an http.Handler for GET /api/v1/files/{path}.
func CreateFilesGetHandler(g git.Git) http.Handler {
	return handler.NewFilesGetHandler(g)
}

// CreateFilesPostHandler returns an http.Handler for POST /api/v1/files/{path}.
func CreateFilesPostHandler(g git.Git) http.Handler {
	return handler.NewFilesPostHandler(g)
}

// CreateFilesDeleteHandler returns an http.Handler for DELETE /api/v1/files/{path}.
func CreateFilesDeleteHandler(g git.Git) http.Handler {
	return handler.NewFilesDeleteHandler(g)
}

// CreateFilesListHandler returns an http.Handler for GET /api/v1/files/ with glob query param.
func CreateFilesListHandler(g git.Git) http.Handler {
	return handler.NewFilesListHandler(g)
}

// CreateHealthzHandler returns an http.Handler for GET /healthz.
func CreateHealthzHandler() http.Handler {
	return handler.NewHealthzHandler()
}

// CreateReadinessHandler returns an http.Handler for GET /readiness.
func CreateReadinessHandler(g git.Git) http.Handler {
	return handler.NewReadinessHandler(g)
}

// CreateFilesDispatchHandler returns a handler that routes between get and list.
func CreateFilesDispatchHandler(getH, listH http.Handler) http.Handler {
	return handler.NewFilesDispatchHandler(getH, listH)
}

// CreateMetricsMiddleware wraps next with Prometheus HTTP request counting.
func CreateMetricsMiddleware(next http.Handler) http.Handler {
	return handler.NewMetricsMiddleware(next)
}
