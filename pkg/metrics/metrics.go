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

// QuarantinedBacklog reports the number of regular files currently under _conflicts/.
var QuarantinedBacklog = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "git_rest_quarantined_backlog",
	Help: "Number of regular files currently under _conflicts/ in the served repo (counted recursively).",
})

// PullRescuesTotal counts dirty-working-tree rescues performed during pull. One
// increment per rescue event: the working-tree state was captured on a
// rescue/<timestamp> branch and pushed, then the working tree was returned to the
// upstream state.
var PullRescuesTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "git_rest_pull_rescues_total",
	Help: "Total number of dirty-working-tree rescues performed during pull (working-tree state captured on a rescue/<timestamp> branch and pushed, then the working tree returned to the upstream state).",
})

// AutoresolvedTotal counts conflicted paths resolved by the scalar-frontmatter
// rule (a conflict confined to single-line scalar frontmatter keys) during pull.
var AutoresolvedTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "git_rest_autoresolved_total",
	Help: "Total conflicted paths auto-resolved by the scalar-frontmatter rule during pull.",
})

// RefRequestsTotal counts claim-ref requests by operation and HTTP status code.
// op is "get", "put" or "delete"; result is the status code returned as a string.
var RefRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "git_rest_ref_requests_total",
	Help: "Total claim-ref requests by operation and HTTP status code.",
}, []string{"op", "result"})

// PushRetriesTotal counts push retries performed after a non-fast-forward
// rejection. One increment per retry: the first push attempt is not counted.
var PushRetriesTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "git_rest_push_retries_total",
	Help: "Total push retries after a non-fast-forward rejection.",
})

// WriteFlushesTotal counts coalesced flush attempts by outcome ("ok" or "error").
var WriteFlushesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "git_rest_write_flushes_total",
	Help: "Total write-coalescing flush attempts by outcome.",
}, []string{"outcome"})

// WriteCoalescedTotal counts file writes absorbed into a coalesced commit batch.
var WriteCoalescedTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "git_rest_write_coalesced_total",
	Help: "Total file writes absorbed into a coalesced commit batch.",
})

// WritePending reports the number of paths currently staged and waiting for a coalesced commit.
var WritePending = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "git_rest_write_pending",
	Help: "Number of paths currently staged and waiting for a coalesced commit.",
})

// ResolverFailuresTotal counts conflict-resolver failures by category.
// Categories: yaml_parse_failed, no_frontmatter, write_failed, git_add_failed,
// quarantine_io_failed, unsafe_path, nested_source. The quarantine_io_failed bucket covers
// any I/O failure in the quarantine flow (read source, git rm source, mkdir
// destination, write destination, git add destination) — the implementation
// does not use git mv (git refuses to move conflicted files), so the
// historical "git_mv_failed" label is renamed to reflect what it actually
// counts.
var ResolverFailuresTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "git_rest_resolver_failures_total",
	Help: "Total conflict-resolver failures by category. Resolver failures: yaml_parse_failed, no_frontmatter, write_failed, git_add_failed. Quarantine failures: quarantine_io_failed (any I/O step of the quarantine flow), unsafe_path (path-traversal rejection), nested_source (a conflicted path already under _conflicts/ was rejected by the nesting guard).",
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
		QuarantinedBacklog,
		PullRescuesTotal,
		AutoresolvedTotal,
		RefRequestsTotal,
		PushRetriesTotal,
		WriteFlushesTotal,
		WriteCoalescedTotal,
		WritePending,
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
	// Explicit .Add(0) on the unlabeled gauge, mirroring QuarantinedFilesTotal:
	// the series is registered at init() time so /metrics exposes it as 0 before
	// the first pull cycle refreshes it.
	QuarantinedBacklog.Add(0)
	// Explicit .Add(0) on the unlabeled rescue counter, mirroring
	// QuarantinedFilesTotal: the series is registered at init() time so /metrics
	// exposes it as 0 before the first rescue has ever happened.
	PullRescuesTotal.Add(0)
	// Explicit .Add(0) on the unlabeled autoresolved counter, mirroring
	// QuarantinedFilesTotal: the series is registered at init() time so /metrics
	// exposes it as 0 before the first auto-resolution has ever happened.
	AutoresolvedTotal.Add(0)
	// Explicit .Add(0) on the unlabeled push-retry counter, mirroring
	// PullRescuesTotal: the series is registered at init() time so /metrics
	// exposes it as 0 before the first retry has ever happened.
	PushRetriesTotal.Add(0)
	// Explicit pre-initialisation of the write-coalescing series, mirroring
	// PullRescuesTotal: /metrics exposes them as 0 before the first write has
	// been staged or the first flush has run.
	WriteFlushesTotal.WithLabelValues("ok").Add(0)
	WriteFlushesTotal.WithLabelValues("error").Add(0)
	WriteCoalescedTotal.Add(0)
	WritePending.Set(0)
	for _, category := range []string{
		"yaml_parse_failed",
		"no_frontmatter",
		"write_failed",
		"git_add_failed",
		"unsafe_path",
		"quarantine_io_failed",
		"nested_source",
	} {
		ResolverFailuresTotal.WithLabelValues(category).Add(0)
	}
	for _, op := range []string{"get", "put", "delete"} {
		for _, result := range []string{"200", "400", "409", "500"} {
			RefRequestsTotal.WithLabelValues(op, result).Add(0)
		}
	}
	// Only PUT carries a body, so 413 (body too large) is a put-only result.
	RefRequestsTotal.WithLabelValues("put", "413").Add(0)
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
	// git_add_failed, unsafe_path, quarantine_io_failed, nested_source.
	IncResolverFailure(category string)
	// IncQuarantinedFiles records a single file moved into _conflicts/ during pull.
	IncQuarantinedFiles()
	// SetQuarantinedBacklog records the number of regular files currently under
	// _conflicts/ in the served repo. It is a setter rather than an increment so a
	// deletion reduces the reported backlog.
	SetQuarantinedBacklog(count int)
	// IncPullRescue records one dirty-working-tree rescue: the working-tree state
	// was captured on a rescue/<timestamp> branch and pushed to the upstream
	// remote, and the working tree was then returned to the upstream state.
	IncPullRescue()
	// IncAutoresolved records one conflicted path auto-resolved by the
	// scalar-frontmatter rule. Called once per path, after the file is written and
	// staged.
	IncAutoresolved()
	// IncRefRequest records one claim-ref request by operation ("get", "put",
	// "delete") and the HTTP status code returned as a string.
	IncRefRequest(op, result string)
	// IncPushRetry records one push retry performed after a non-fast-forward
	// rejection. The first push attempt is never counted.
	IncPushRetry()
	// IncWriteFlush records one coalesced flush attempt. outcome is "ok" or "error".
	IncWriteFlush(outcome string)
	// IncWriteCoalesced records one file write absorbed into a coalesced batch.
	// It increments once per staged write, so a three-write burst increments it by 3.
	IncWriteCoalesced()
	// SetWritePending records the current pending-path count. It is a setter so a
	// successful flush returns the gauge to 0.
	SetWritePending(count int)
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

func (p *prometheusMetrics) SetQuarantinedBacklog(count int) {
	QuarantinedBacklog.Set(float64(count))
}

func (p *prometheusMetrics) IncPullRescue() {
	PullRescuesTotal.Inc()
}

func (p *prometheusMetrics) IncAutoresolved() {
	AutoresolvedTotal.Inc()
}

func (p *prometheusMetrics) IncRefRequest(op, result string) {
	RefRequestsTotal.WithLabelValues(op, result).Inc()
}

func (p *prometheusMetrics) IncPushRetry() {
	PushRetriesTotal.Inc()
}

func (p *prometheusMetrics) IncWriteFlush(outcome string) {
	WriteFlushesTotal.WithLabelValues(outcome).Inc()
}

func (p *prometheusMetrics) IncWriteCoalesced() {
	WriteCoalescedTotal.Inc()
}

func (p *prometheusMetrics) SetWritePending(count int) {
	WritePending.Set(float64(count))
}
