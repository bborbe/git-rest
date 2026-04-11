---
status: approved
spec: [001-git-rest-server]
created: "2026-04-11T19:30:00Z"
queued: "2026-04-11T19:37:18Z"
branch: dark-factory/git-rest-server
---

<summary>
- `main.go` is replaced with a full HTTP server that registers all routes and starts on a configurable address
- CLI flags configure the repo path, listen address, and git pull interval
- The server starts a background goroutine that calls `git pull` on the configured interval (default 30s)
- All routes are registered: `/api/v1/files/` (GET/POST/DELETE/LIST), `/healthz`, `/readiness`, `/metrics`
- `/metrics` exposes Prometheus metrics: per-endpoint request counts (labels: method, path, status), git operation durations (label: operation), git operation error counts (label: operation)
- Prometheus metrics are registered once at startup via `prometheus.MustRegister`
- The server handles OS signals (SIGINT, SIGTERM) and shuts down gracefully
- The periodic pull loop checks `ctx.Done()` on each iteration to exit cleanly on shutdown
- Startup fails with a clear error message if the repo directory does not exist
- `make precommit` passes on the completed codebase
</summary>

<objective>
Wire everything together in `main.go`: instantiate `git.New(repoPath)`, build all handlers via the factory, register routes on an `http.ServeMux`, expose Prometheus metrics at `/metrics`, start the periodic pull loop, and handle graceful shutdown. This is the final prompt — after it completes the server is fully functional.
</objective>

<context>
Read `CLAUDE.md` and `docs/dod.md` for project conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` for goroutine/context cancellation patterns.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` for the pull loop pattern.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` for metric naming and registration.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md` for slog usage.

Existing files:
- `pkg/git/git.go` — `Git` interface (prompt 1)
- `pkg/handler/*.go` — all HTTP handlers (prompt 2)
- `pkg/factory/factory.go` — `Create*` factory functions (prompt 2)
- `main_test.go` — compile test (must remain passing)
</context>

<requirements>
1. Replace `main.go` with a full implementation:
   - Parse CLI flags (use `flag` stdlib):
     - `--repo` (string, required): path to the git repository on disk
     - `--addr` (string, default `":8080"`): HTTP listen address
     - `--pull-interval` (duration, default `30s`): how often to run `git pull`
   - Validate that the repo directory exists (`os.Stat`); if not, print a clear error and `os.Exit(1)`
   - Instantiate `gitClient := git.New(repoDir)`
   - Build all handlers using `pkg/factory`:
     - `factory.CreateFilesGetHandler(gitClient)`
     - `factory.CreateFilesPostHandler(gitClient)`
     - `factory.CreateFilesDeleteHandler(gitClient)`
     - `factory.CreateFilesListHandler(gitClient)`
     - `factory.CreateHealthzHandler()`
     - `factory.CreateReadinessHandler(gitClient)`
   - Register routes on `http.NewServeMux()`:
     - `GET /api/v1/files/{path...}` → files get handler
     - `POST /api/v1/files/{path...}` → files post handler
     - `DELETE /api/v1/files/{path...}` → files delete handler
     - `GET /api/v1/files/` (path ends in `/`, has `glob` query param) → files list handler
     - `/healthz` → healthz handler
     - `/readiness` → readiness handler
     - `/metrics` → `promhttp.Handler()`
   - Route dispatch for `/api/v1/files/`: use a single mux entry that dispatches by HTTP method inside a wrapper handler:
     ```
     mux.Handle("/api/v1/files/", methodDispatch(getH, postH, deleteH, listH))
     ```
     Or register separate routes if the Go 1.22+ `http.ServeMux` method-prefixed routing is available (e.g., `mux.Handle("GET /api/v1/files/", ...)`). Use the idiomatic approach for the Go version in `go.mod` (1.26.2 supports method+path routing).
   - Wrap the mux with a Prometheus instrumentation middleware that records:
     - `git_rest_http_requests_total` counter (labels: `method`, `path`, `status`)
     - Record after handler returns using `httpsnoop` or a simple `ResponseWriter` wrapper to capture status code
   - Start the HTTP server with `http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}`
   - Implement graceful shutdown: listen for `os.Interrupt` / `syscall.SIGTERM` via `signal.NotifyContext`; on signal, call `server.Shutdown(ctx)` with a 30s timeout

2. Create `pkg/metrics/metrics.go`:
   - Define and register Prometheus metrics:
     ```go
     var (
         HTTPRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
             Name: "git_rest_http_requests_total",
             Help: "Total HTTP requests by method, path template, and status code.",
         }, []string{"method", "path", "status"})

         GitOperationDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
             Name:    "git_rest_git_operation_duration_seconds",
             Help:    "Duration of git operations.",
             Buckets: prometheus.DefBuckets,
         }, []string{"operation"})

         GitOperationErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
             Name: "git_rest_git_operation_errors_total",
             Help: "Total git operation errors by operation type.",
         }, []string{"operation"})
     )

     func init() {
         prometheus.MustRegister(HTTPRequestsTotal, GitOperationDuration, GitOperationErrors)
     }
     ```

3. Instrument git operations in `pkg/git/git.go` to record duration and errors:
   - At the start of `WriteFile`, `DeleteFile`, `ReadFile`, `ListFiles`, `Pull`: record `start := time.Now()`
   - On return, call `metrics.GitOperationDuration.WithLabelValues("write_file").Observe(time.Since(start).Seconds())` (use operation-appropriate label)
   - On error return (non-`ErrNotFound` errors), call `metrics.GitOperationErrors.WithLabelValues("write_file").Inc()`
   - Import `pkg/metrics` in `pkg/git/git.go`

4. Create a `pkg/puller/puller.go` with the periodic pull loop:
   - Interface and constructor:
     ```go
     type Puller interface {
         Run(ctx context.Context) error
     }

     func New(g git.Git, interval time.Duration) Puller
     ```
   - `Run` implementation: on each tick, call `g.Pull(ctx)`, log warning on error, continue; check `ctx.Done()` between ticks; exit cleanly when context is cancelled
   - Use a `time.NewTicker` — do NOT use `time.Sleep`
   - Context check pattern (from go-context-cancellation-in-loops.md):
     ```go
     for {
         select {
         case <-ctx.Done():
             return ctx.Err()
         case <-ticker.C:
             if err := g.Pull(ctx); err != nil {
                 slog.WarnContext(ctx, "git pull failed", "error", err)
             }
         }
     }
     ```

5. In `main.go`, start the puller as a concurrent goroutine using context cancellation (NOT raw `go func()`):
   - Create a root context with `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)`
   - Start `puller.Run(ctx)` in a goroutine; log if it returns an unexpected error
   - Start the HTTP server; on signal, cancel context and call `server.Shutdown`

6. Update `main_test.go` compile test — it must still pass after `main.go` is rewritten. The existing test only checks that the binary compiles; ensure it continues to do so.

7. Add an `## Unreleased` entry to `CHANGELOG.md`:
   ```
   ## Unreleased

   - feat: Implement git-rest HTTP server with file CRUD, periodic git pull, health/readiness probes, and Prometheus metrics
   ```
</requirements>

<constraints>
- Git operations use the system `git` binary via `os/exec` — no embedded library
- Git operations must be serialized via `sync.Mutex` inside `pkg/git` — the main wiring must not add additional locking
- Commit messages: `git-rest: create {path}`, `git-rest: update {path}`, `git-rest: delete {path}` — set in `pkg/git`, not in handlers
- Error responses are JSON: `{"error": "message"}`
- Request body size capped at 10 MB
- Path traversal returns 400
- Repo directory missing at startup → fail with clear error and exit code 1
- Prometheus metrics must use `git_rest_` prefix
- `ReadHeaderTimeout` must be set on `http.Server` (gosec requirement)
- `context.Background()` must NOT appear in `pkg/` — only in `main.go`
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Errors must be wrapped with `github.com/bborbe/errors`
</constraints>

<verification>
```bash
make test
make precommit
```

Additional checks:
```bash
# Confirm binary builds and --help works
cd /workspace && go build -o /tmp/git-rest . && /tmp/git-rest --help

# Confirm metrics endpoint is wired
grep -n "metrics\|promhttp" /workspace/main.go

# Confirm pull loop uses context select (not sleep)
grep -n "ctx.Done\|ticker.C" /workspace/pkg/puller/puller.go

# Confirm ReadHeaderTimeout set
grep -n "ReadHeaderTimeout" /workspace/main.go
```
</verification>
