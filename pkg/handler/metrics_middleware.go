// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/felixge/httpsnoop"

	"github.com/bborbe/git-rest/pkg/metrics"
)

// routeLabel normalizes URL paths to prevent unbounded cardinality in metrics.
func routeLabel(path string) string {
	if strings.HasPrefix(path, "/api/v1/files/") {
		return "/api/v1/files/{path}"
	}
	return path
}

// NewMetricsMiddleware wraps next with Prometheus HTTP request counting.
func NewMetricsMiddleware(m metrics.Metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured := httpsnoop.CaptureMetrics(next, w, r)
		m.IncHTTPRequest(
			r.Method,
			routeLabel(r.URL.Path),
			strconv.Itoa(captured.Code),
		)
	})
}
