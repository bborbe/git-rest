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

// NewRefMetricsMiddleware records the final HTTP status of every request that
// reaches a claim-ref handler under the given op ("get", "put" or "delete").
func NewRefMetricsMiddleware(m metrics.Metrics, op string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured := httpsnoop.CaptureMetrics(next, w, r)
		m.IncRefRequest(op, strconv.Itoa(captured.Code))
	})
}
