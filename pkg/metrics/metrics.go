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

// GitOperationErrors counts git operation errors by operation type and conflict status.
var GitOperationErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "git_rest_git_operation_errors_total",
	Help: "Total git operation errors by operation type.",
}, []string{"operation", "conflict"})

// MergeOutcomeTotal counts merge outcomes by result: clean, resolved, aborted.
var MergeOutcomeTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "git_rest_merge_outcome_total",
	Help: "Total merge outcomes by result type (clean=auto-merged, resolved=resolver succeeded, aborted=resolver failed).",
}, []string{"result"})

// ConflictPathsTotal counts total conflicted file paths passed to the resolver across pod lifetime.
var ConflictPathsTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "git_rest_conflict_paths_total",
	Help: "Total count of conflicted file paths passed to the ConflictResolver across pod lifetime.",
})

// QuarantinedFilesTotal counts conflicted files moved to _conflicts/ during pull.
var QuarantinedFilesTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "git_rest_quarantined_files_total",
	Help: "Total number of conflicted files quarantined to _conflicts/ during pull (per-file quarantine for the resolver-failure branch).",
})

// ResolverFailuresTotal counts conflict-resolver failures by category.
// Categories: yaml_parse_failed, no_frontmatter, write_failed, git_add_failed,
// quarantine_io_failed, unsafe_path. The quarantine_io_failed bucket covers
// any I/O failure in the quarantine flow (read source, git rm source, mkdir
// destination, write destination, git add destination) — the implementation
// does not use git mv (git refuses to move conflicted files), so the
// historical "git_mv_failed" label is renamed to reflect what it actually
// counts.
var ResolverFailuresTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "git_rest_resolver_failures_total",
	Help: "Total conflict-resolver failures by category. Resolver failures: yaml_parse_failed, no_frontmatter, write_failed, git_add_failed. Quarantine failures: quarantine_io_failed (any I/O step of the quarantine flow), unsafe_path (path-traversal rejection).",
}, []string{"category"})

func init() {
	prometheus.MustRegister(
		HTTPRequestsTotal,
		GitOperationDuration,
		GitOperationErrors,
		MergeOutcomeTotal,
		ConflictPathsTotal,
		ResolverFailuresTotal,
		QuarantinedFilesTotal,
	)
	for _, op := range []string{"write_file", "delete_file", "read_file", "list_files", "pull", "fetch", "push", "rebase"} {
		GitOperationErrors.WithLabelValues(op, "").Add(0)
	}
	GitOperationErrors.WithLabelValues("rebase", "true").Add(0)
	for _, combo := range []struct{ method, path string }{
		{"GET", "/api/v1/files/{path}"},
		{"POST", "/api/v1/files/{path}"},
		{"DELETE", "/api/v1/files/{path}"},
		{"GET", "/healthz"},
		{"GET", "/readiness"},
		{"GET", "/metrics"},
	} {
		for _, status := range []string{"200", "400", "404", "500"} {
			HTTPRequestsTotal.WithLabelValues(combo.method, combo.path, status).Add(0)
		}
	}
	for _, result := range []string{"clean", "resolved", "aborted"} {
		MergeOutcomeTotal.WithLabelValues(result).Add(0)
	}
	// Explicit .Add(0) on the unlabeled counter. Unlabeled prometheus.NewCounter
	// is already initialised to 0 at MustRegister time (so /metrics exposes the
	// time series before any event), but the explicit Add(0) makes the
	// pre-initialisation visible alongside the labelled ones and silences the
	// bot reviewer's defensive check.
	QuarantinedFilesTotal.Add(0)
	for _, category := range []string{
		"yaml_parse_failed",
		"no_frontmatter",
		"write_failed",
		"git_add_failed",
		"unsafe_path",
		"quarantine_io_failed",
	} {
		ResolverFailuresTotal.WithLabelValues(category).Add(0)
	}
}

// Metrics records git operation instrumentation.
//
//counterfeiter:generate -o ../../mocks/metrics.go --fake-name FakeMetrics . Metrics
type Metrics interface {
	ObserveGitOperation(operation string, duration float64)
	IncGitOperationError(operation string)
	IncHTTPRequest(method, path, statusCode string)
	IncRebaseConflict()
	// IncMergeOutcome records a merge outcome. result must be "clean", "resolved", or "aborted".
	IncMergeOutcome(result string)
	// IncConflictPaths records n conflicted paths passed to the resolver in one merge cycle.
	IncConflictPaths(n int)
	// IncResolverFailure records a conflict-resolver failure by category.
	// category must be one of: yaml_parse_failed, no_frontmatter, write_failed,
	// git_add_failed, unsafe_path, quarantine_io_failed.
	IncResolverFailure(category string)
	// IncQuarantinedFiles records a single file moved into _conflicts/ during pull.
	IncQuarantinedFiles()
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
	GitOperationErrors.WithLabelValues(operation, "").Inc()
}

func (p *prometheusMetrics) IncRebaseConflict() {
	GitOperationErrors.WithLabelValues("rebase", "true").Inc()
}

func (p *prometheusMetrics) IncHTTPRequest(method, path, statusCode string) {
	HTTPRequestsTotal.WithLabelValues(method, path, statusCode).Inc()
}

func (p *prometheusMetrics) IncMergeOutcome(result string) {
	MergeOutcomeTotal.WithLabelValues(result).Inc()
}

func (p *prometheusMetrics) IncConflictPaths(n int) {
	ConflictPathsTotal.Add(float64(n))
}

func (p *prometheusMetrics) IncResolverFailure(category string) {
	ResolverFailuresTotal.WithLabelValues(category).Inc()
}

func (p *prometheusMetrics) IncQuarantinedFiles() {
	QuarantinedFilesTotal.Inc()
}
