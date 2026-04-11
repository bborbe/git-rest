---
status: approved
spec: [001-git-rest-server]
created: "2026-04-11T22:20:00Z"
queued: "2026-04-11T20:24:36Z"
---

<summary>
- Server configuration supports both CLI flags and environment variables using the service.Main pattern
- Sentry error reporting is integrated for production observability
- Build metadata (version, commit, date) is tracked via Prometheus metrics
- The application struct declares all configuration with struct tags for arg, env, defaults, and validation
- Graceful shutdown and signal handling is delegated to the service library
</summary>

<objective>
Refactor main.go from raw `flag` parsing to the `github.com/bborbe/service` application pattern, enabling configuration via both CLI flags and environment variables. This is required for K8s deployments where config comes from env vars in the pod spec.
</objective>

<context>
Read `CLAUDE.md` and `docs/dod.md` for project conventions.

Reference implementation (go-skeleton/main.go):
```go
func main() {
    app := &application{}
    os.Exit(service.Main(context.Background(), app, &app.SentryDSN, &app.SentryProxy))
}

type application struct {
    SentryDSN       string            `required:"true"  arg:"sentry-dsn"        env:"SENTRY_DSN"        usage:"SentryDSN"                             display:"length"`
    SentryProxy     string            `required:"false" arg:"sentry-proxy"      env:"SENTRY_PROXY"      usage:"Sentry Proxy"`
    Listen          string            `required:"true"  arg:"listen"            env:"LISTEN"            usage:"address to listen to"`
    BuildGitVersion string            `required:"false" arg:"build-git-version" env:"BUILD_GIT_VERSION" usage:"Build Git version"                                      default:"dev"`
    BuildGitCommit  string            `required:"false" arg:"build-git-commit"  env:"BUILD_GIT_COMMIT"  usage:"Build Git commit hash"                                  default:"none"`
    BuildDate       *libtime.DateTime `required:"false" arg:"build-date"        env:"BUILD_DATE"        usage:"Build timestamp (RFC3339)"`
}

func (a *application) Run(ctx context.Context, sentryClient libsentry.Client) error {
    pkg.NewBuildInfoMetrics().SetBuildInfo(a.BuildDate)
    return service.Run(ctx, a.createHTTPServer(sentryClient))
}
```

Current state:
- `main.go` — uses `flag` package directly, `signal.NotifyContext` for shutdown
- `pkg/puller/puller.go` — periodic git pull loop (must be started in Run)
- `pkg/git/git.go` — Git interface with `New(repoPath)` constructor
- `pkg/factory/factory.go` — handler factory functions
- `pkg/metrics/metrics.go` — Prometheus metrics (add BuildInfoMetrics)
</context>

<requirements>
1. Add dependencies to `go.mod` and vendor:
   - `github.com/bborbe/service` — application lifecycle, arg/env parsing, signal handling
   - `github.com/bborbe/sentry` — Sentry integration
   - `github.com/bborbe/time` — `libtime.DateTime` for build date
   - `github.com/bborbe/http` — `libhttp.NewServer` for HTTP server with graceful shutdown
   Run `go get` for each, then `go mod tidy && go mod vendor`

2. Rewrite `main.go`:
   - Replace `flag` parsing with `application` struct + `service.Main`
   - Application struct fields:
     ```go
     type application struct {
         SentryDSN       string             `required:"false" arg:"sentry-dsn"        env:"SENTRY_DSN"        usage:"Sentry DSN"                display:"length"`
         SentryProxy     string             `required:"false" arg:"sentry-proxy"      env:"SENTRY_PROXY"      usage:"Sentry Proxy"`
         Listen          string             `required:"true"  arg:"listen"            env:"LISTEN"            usage:"HTTP listen address"                   default:":8080"`
         Repo            string             `required:"true"  arg:"repo"              env:"REPO"              usage:"path to git repository on disk"`
         PullInterval    time.Duration      `required:"false" arg:"pull-interval"     env:"PULL_INTERVAL"     usage:"git pull interval"                     default:"30s"`
         BuildGitVersion string             `required:"false" arg:"build-git-version" env:"BUILD_GIT_VERSION" usage:"Build Git version"                     default:"dev"`
         BuildGitCommit  string             `required:"false" arg:"build-git-commit"  env:"BUILD_GIT_COMMIT"  usage:"Build Git commit hash"                 default:"none"`
         BuildDate       *libtime.DateTime  `required:"false" arg:"build-date"        env:"BUILD_DATE"        usage:"Build timestamp (RFC3339)"`
     }
     ```
   - `SentryDSN` and `SentryProxy` are `required:"false"` (optional for git-rest)
   - `main()` function: `app := &application{}; os.Exit(service.Main(context.Background(), app, &app.SentryDSN, &app.SentryProxy))`

3. Implement `(a *application) Run(ctx context.Context, sentryClient libsentry.Client) error`:
   - Validate repo directory exists (`os.Stat`), return error if not
   - Call `metrics.NewBuildInfoMetrics().SetBuildInfo(a.BuildDate)` (create this in step 4)
   - Instantiate `gitClient := git.New(a.Repo)`
   - Build all handlers via `factory.Create*`
   - Set up `http.NewServeMux()` with routes (same as current main.go)
   - Wrap mux with `metricsMiddleware` (keep existing implementation)
   - Use `service.Run(ctx, ...)` with multiple `run.Func` args to start both the HTTP server and puller concurrently:
     ```go
     return service.Run(ctx,
         a.createHTTPServer(gitClient, sentryClient),
         a.createPuller(gitClient),
     )
     ```
   - Signal handling and graceful shutdown is handled by `service.Main` — remove manual `signal.NotifyContext` and `server.Shutdown`

4. Create `pkg/metrics/build_info.go` (package `metrics`):
   - Follow the go-skeleton `pkg/build-info-metrics.go` pattern but in the `metrics` package:
     ```go
     type BuildInfoMetrics interface {
         SetBuildInfo(buildDate *libtime.DateTime)
     }

     func NewBuildInfoMetrics() BuildInfoMetrics
     ```
   - Register a Prometheus gauge `git_rest_build_info` with labels `version`, `commit`, `date`
   - The caller in `main.go` uses `metrics.NewBuildInfoMetrics().SetBuildInfo(a.BuildDate)`

5. Implement `(a *application) createHTTPServer(gitClient git.Git, sentryClient libsentry.Client) run.Func`:
   - Returns a `run.Func` that sets up routes and starts `libhttp.NewServer(a.Listen, handler).Run(ctx)`
   - `libhttp.NewServer` handles `ReadHeaderTimeout` and graceful shutdown internally

6. Implement `(a *application) createPuller(gitClient git.Git) run.Func`:
   - Returns a `run.Func` that calls `puller.New(gitClient, a.PullInterval).Run(ctx)`

7. Keep `filesDispatch` and `metricsMiddleware` helper functions in `main.go`

8. Remove: `flag` import, manual `signal.NotifyContext`, manual `server.Shutdown`, `http.Server` struct literal

9. Update `main_test.go` — ensure compile test still passes

10. Run `go mod tidy && go mod vendor` after all changes
</requirements>

<constraints>
- Git operations use the system `git` binary via `os/exec` — no embedded library
- Git operations must be serialized via `sync.Mutex` inside `pkg/git` — the main wiring must not add additional locking
- Commit messages: `git-rest: create {path}`, `git-rest: update {path}`, `git-rest: delete {path}` — set in `pkg/git`, not in handlers
- Error responses are JSON: `{"error": "message"}`
- Prometheus metrics must use `git_rest_` prefix
- `context.Background()` must NOT appear in `pkg/` — only in `main.go`
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Errors must be wrapped with `github.com/bborbe/errors`
</constraints>

<verification>
```bash
make precommit
```

Additional checks:
```bash
# Confirm service.Main pattern
grep -n "service.Main\|application" /workspace/main.go

# Confirm env var support
grep -n 'env:' /workspace/main.go

# Confirm no flag package
grep -n '"flag"' /workspace/main.go && echo "FAIL: flag still used" || echo "OK"

# Confirm binary starts with --help
cd /workspace && go build -o /tmp/git-rest . && /tmp/git-rest --help
```
</verification>
