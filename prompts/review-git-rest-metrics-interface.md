---
status: draft
created: "2026-04-11T00:00:00Z"
---

<summary>
- pkg/git/git.go imports pkg/metrics directly and calls package-level variables (metrics.GitOperationDuration, metrics.GitOperationErrors)
- This violates the project's composition rule: business logic must not call pkg.Function() directly
- pkg/metrics is architecturally inverted — it should be a leaf consumed only at the factory/main level
- A Metrics interface injected via the git.New constructor fixes the coupling and makes tests independent of Prometheus
- The metricsMiddleware also calls metrics.HTTPRequestsTotal directly; it should receive the interface too
- Additionally, the HTTPRequestsTotal label uses raw URL paths, causing unbounded cardinality when unique file paths are requested
</summary>

<objective>
Define a `Metrics` interface in `pkg/metrics/`, implement it with a Prometheus-backed struct, inject it into `pkg/git.New()` and the metrics middleware, and normalize the HTTP path label to prevent unbounded cardinality. Remove the direct `pkg/metrics` import from `pkg/git/git.go`.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Files to read before making changes (read ALL first):
- `pkg/metrics/metrics.go`: current global var declarations and init()
- `pkg/git/git.go`: direct metrics calls (~lines 116-121, 133, 138, 143, 153, 158, 168-175, 187, 193, 198, 208-215, 228, 238-241, 248, 263, 276-278, 284)
- `pkg/factory/factory.go`: factory function that calls git.New
- `main.go`: wiring of metricsMiddleware and git.New
- `mocks/mocks.go`: package stub to understand mock structure
- `pkg/git/git_test.go`: existing tests to understand what needs updating
</context>

<requirements>
1. In `pkg/metrics/metrics.go`, define a `Metrics` interface and a private implementation:
   ```go
   //counterfeiter:generate -o ../../mocks/metrics.go --fake-name FakeMetrics . Metrics

   // Metrics records git operation instrumentation.
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
   ```

2. In `pkg/git/git.go`, update the `git` struct and `New` constructor to accept a `Metrics` parameter:
   ```go
   func New(repoPath string, m metrics.Metrics) Git {
       return &git{repoPath: repoPath, metrics: m}
   }

   type git struct {
       repoPath string
       mu       sync.Mutex
       metrics  metrics.Metrics
   }
   ```
   Replace all `metrics.GitOperationDuration.WithLabelValues(...)` calls with `g.metrics.ObserveGitOperation(...)` and all `metrics.GitOperationErrors.WithLabelValues(...)` with `g.metrics.IncGitOperationError(...)`. Remove the `import "github.com/bborbe/git-rest/pkg/metrics"` from git.go since it is no longer needed.

3. Update `pkg/handler/metrics_middleware.go` (or `main.go` if still inline) to accept a `metrics.Metrics` parameter and call `m.IncHTTPRequest(...)` instead of accessing the package-level var.

   Also normalize the path label to prevent cardinality explosion:
   ```go
   func routeLabel(path string) string {
       if strings.HasPrefix(path, "/api/v1/files/") {
           return "/api/v1/files/{path}"
       }
       return path
   }
   ```
   Use `routeLabel(r.URL.Path)` as the path label value.

4. Update `pkg/factory/factory.go` to pass a `metrics.Metrics` to `git.New(...)`:
   ```go
   func CreateGitClient(repoPath string, m metrics.Metrics) git.Git {
       return git.New(repoPath, m)
   }
   ```
   Or pass the `Metrics` value directly through the existing factory chain — whichever is less disruptive to `main.go`.

5. Update `main.go` to construct `metrics.NewMetrics()` and pass it to the git client and middleware:
   ```go
   m := metrics.NewMetrics()
   gitClient := git.New(*repo, m)
   ```
   Pass `m` to the metrics middleware as well.

6. Run `go generate ./pkg/metrics/...` to create `mocks/metrics.go`.

7. Update `pkg/git/git_test.go` to inject a `*mocks.FakeMetrics` instead of relying on the global Prometheus registry. Replace the git.New call in BeforeEach with `git.New(workDir, fakeMetrics)`.

8. Add pre-initialization of `GitOperationErrors` counter for all known operations in `pkg/metrics/metrics.go`:
   ```go
   func init() {
       prometheus.MustRegister(HTTPRequestsTotal, GitOperationDuration, GitOperationErrors)
       for _, op := range []string{"write_file", "delete_file", "read_file", "list_files", "pull"} {
           GitOperationErrors.WithLabelValues(op).Add(0)
       }
   }
   ```
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- pkg/git must not import pkg/metrics after this change
- Use `errors.Wrap`/`errors.Errorf` from `github.com/bborbe/errors` — never `fmt.Errorf`
- Factory functions must have zero business logic
- The Metrics interface must have a counterfeiter:generate annotation
</constraints>

<verification>
make precommit
</verification>
