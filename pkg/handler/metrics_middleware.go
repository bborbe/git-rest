// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"net/http"
	"strconv"

	"github.com/felixge/httpsnoop"

	"github.com/bborbe/git-rest/pkg/metrics"
)

// NewMetricsMiddleware wraps next with Prometheus HTTP request counting.
func NewMetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := httpsnoop.CaptureMetrics(next, w, r)
		metrics.HTTPRequestsTotal.WithLabelValues(
			r.Method,
			r.URL.Path,
			strconv.Itoa(m.Code),
		).Inc()
	})
}
