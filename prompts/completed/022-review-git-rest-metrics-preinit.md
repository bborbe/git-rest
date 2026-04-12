---
status: completed
summary: Pre-initialized HTTPRequestsTotal counter for known method/path/status combinations in pkg/metrics/metrics.go and added missing factory_suite_test.go bootstrap file.
container: git-rest-022-review-git-rest-metrics-preinit
dark-factory-version: v0.108.0-dirty
created: "2026-04-12T13:29:16Z"
queued: "2026-04-12T13:49:23Z"
started: "2026-04-12T14:50:33Z"
completed: "2026-04-12T14:54:59Z"
---

<summary>
- HTTP request counter not pre-initialized for known label combinations
- Missing pre-initialization causes absent time series in Prometheus queries
- The git operation errors counter already follows the correct pre-init pattern as reference
- Factory package is missing the standard test suite bootstrap file
</summary>

<objective>
Pre-initialize `HTTPRequestsTotal` counter for known method/path combinations and add missing `factory_suite_test.go`.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read coding guide before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-prometheus-metrics-guide.md`: counter pre-initialization patterns
- `go-testing-guide.md`: test suite bootstrap pattern

Files to read before making changes:
- `pkg/metrics/metrics.go` (~lines 12-35) — counter declarations and init()
- `main.go` (~lines 82-91) — actual route registrations with exact paths
- `main_test.go` — reference for test suite structure
</context>

<requirements>

## 1. Pre-initialize HTTPRequestsTotal

In `pkg/metrics/metrics.go`, in the `init()` function after `prometheus.MustRegister(...)`, add `.Add(0)` calls for known label combinations. 

First check the actual route paths in `main.go` to determine exact path labels used by the metrics middleware. The routes use Go 1.22+ method+path patterns like `"GET /api/v1/files/"`. Determine what the metrics middleware records as the path label (with or without trailing slash, with or without method prefix) and use those exact values.

Pre-initialize for common status codes (200, 400, 404, 500) across the main routes. Follow the same pattern used by `GitOperationErrors` pre-initialization.

## 2. Add factory_suite_test.go

Create `pkg/factory/factory_suite_test.go` following the project pattern from `main_test.go`. Include Ginkgo/Gomega imports, `time.Local = time.UTC`, `format.TruncatedDiff = false`, suite config with 60s timeout.

</requirements>

<constraints>
- Only change files in `.`
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Check actual route paths and metrics middleware before hardcoding label values
</constraints>

<verification>
make precommit
</verification>
