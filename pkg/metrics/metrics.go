// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// HTTPRequestsTotal counts HTTP requests by method, path template, and status code.
var HTTPRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "git_rest_http_requests_total",
	Help: "Total HTTP requests by method, path template, and status code.",
}, []string{"method", "path", "status"})

// GitOperationDuration records the duration of git operations.
var GitOperationDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "git_rest_git_operation_duration_seconds",
	Help:    "Duration of git operations.",
	Buckets: prometheus.DefBuckets,
}, []string{"operation"})

// GitOperationErrors counts git operation errors by operation type.
var GitOperationErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "git_rest_git_operation_errors_total",
	Help: "Total git operation errors by operation type.",
}, []string{"operation"})

func init() {
	prometheus.MustRegister(HTTPRequestsTotal, GitOperationDuration, GitOperationErrors)
	for _, op := range []string{"write_file", "delete_file", "read_file", "list_files", "pull"} {
		GitOperationErrors.WithLabelValues(op).Add(0)
	}
}

// Metrics records git operation instrumentation.
//
//counterfeiter:generate -o ../../mocks/metrics.go --fake-name FakeMetrics . Metrics
type Metrics interface {
	ObserveGitOperation(operation string, duration float64)
	IncGitOperationError(operation string)
	IncHTTPRequest(method, path, statusCode string)
}

// NewMetrics returns a Prometheus-backed Metrics implementation.
func NewMetrics() Metrics {
	return &prometheusMetrics{}
}

type prometheusMetrics struct{}

func (p *prometheusMetrics) ObserveGitOperation(operation string, duration float64) {
	GitOperationDuration.WithLabelValues(operation).Observe(duration)
}

func (p *prometheusMetrics) IncGitOperationError(operation string) {
	GitOperationErrors.WithLabelValues(operation).Inc()
}

func (p *prometheusMetrics) IncHTTPRequest(method, path, statusCode string) {
	HTTPRequestsTotal.WithLabelValues(method, path, statusCode).Inc()
}
