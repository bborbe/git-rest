---
status: approved
spec: [004-gateway-secret-auth]
created: "2026-05-02T19:35:00Z"
queued: "2026-05-02T19:47:24Z"
branch: dark-factory/gateway-secret-auth
---

<summary>
- The service accepts a new optional --gateway-secret / GATEWAY_SECRET configuration value
- When the secret is set, all /api/v1/* file endpoints require X-Gateway-Initator and X-Gateway-Secret headers
- Health, readiness, and metrics probes remain reachable without any headers regardless of auth config
- Auth failures (401, 500) appear in Prometheus HTTP metrics because auth wraps inside the metrics middleware
- When the secret is unset, the service emits exactly one startup warning and runs in backward-compatible open mode
- Existing deployments with no GATEWAY_SECRET keep working unchanged
- An integration test wires a real gorilla mux router identically to createHTTPServer and exercises all auth and probe paths
- Scenarios 008 (auth enabled) and 009 (auth disabled) are set to active status
</summary>

<objective>
Wire the gateway secret auth middleware (implemented in prompt 1-spec-004) into `createHTTPServer` in `main.go`. Use a gorilla mux subrouter on `/api/v1` so probes are never wrapped by auth. Add a `GatewaySecret` field to the application struct. Emit a startup warning when the secret is empty. Write an integration test that verifies the routing. Activate scenarios 008 and 009. Update CHANGELOG.

Precondition: `pkg/handler/NewGatewaySecretMiddleware` and `pkg/factory/CreateGatewaySecretMiddleware` must already exist (added by prompt 1-spec-004-gateway-secret-middleware). Verify with `grep -rn "NewGatewaySecretMiddleware" pkg/handler/` before starting implementation.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read coding guides before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-http-handler-refactoring-guide.md` — factory composition patterns
- `go-logging-guide.md` — this repo uses `log/slog` exclusively (slog.WarnContext pattern)
- `go-testing-guide.md` — Ginkgo/Gomega test patterns

Files to read in full before implementing:
- `main.go` — full file; focus on: `application` struct (~line 38), `Run()` method (~line 54), `createHTTPServer()` method (~line 376), existing route registrations
- `main_test.go` — existing Ginkgo suite at repo root (package main_test); existing Describe blocks are the pattern
- `export_test.go` — exports helpers for testing (you will NOT need to add anything here — the integration test imports factory directly)
- `pkg/factory/factory.go` — verify `CreateGatewaySecretMiddleware` exists (added by prompt 1)
- `pkg/handler/gateway_secret_middleware.go` — verify `NewGatewaySecretMiddleware` exists (added by prompt 1)
- `CHANGELOG.md` — add `## Unreleased` section
- `scenarios/008-gateway-secret-auth.md` — change status: idea → status: active
- `scenarios/009-gateway-secret-disabled.md` — change status: idea → status: active
</context>

<requirements>

## 1. Add `GatewaySecret` field to the `application` struct in `main.go`

In the `application` struct, add after the last existing field (`GitUserEmail`):

```go
GatewaySecret string `required:"false" arg:"gateway-secret" env:"GATEWAY_SECRET" usage:"Shared secret required in X-Gateway-Secret header for /api/v1/* requests. Empty = no auth (backward compatible)." display:"length"`
```

The `display:"length"` tag ensures the effective-config startup log shows the secret's length, never its value.

## 2. Add startup warning in `Run()` when GatewaySecret is empty

In `main.go`, in `(*application).Run()`, add the warning AFTER the `a.bootstrap(ctx)` call succeeds and BEFORE `service.Run(...)`. Insert:

```go
if a.GatewaySecret == "" {
    slog.WarnContext(ctx, "gateway-secret not set", "reason", "git-rest API is unauthenticated")
}
```

The slog output for this call will contain both substrings the spec requires:
- `gateway-secret not set` (in the message)
- `git-rest API is unauthenticated` (in the reason field)

The final `Run()` method should look like:

```go
func (a *application) Run(ctx context.Context, sentryClient libsentry.Client) error {
    metrics.NewBuildInfoMetrics(a.BuildGitVersion, a.BuildGitCommit).SetBuildInfo(a.BuildDate)

    if err := a.bootstrap(ctx); err != nil {
        return errors.Wrap(ctx, err, "bootstrap failed")
    }

    if a.GatewaySecret == "" {
        slog.WarnContext(ctx, "gateway-secret not set", "reason", "git-rest API is unauthenticated")
    }

    gitClient, err := a.createGitClient(ctx)
    if err != nil {
        return errors.Wrap(ctx, err, "create git client failed")
    }

    return service.Run(ctx,
        a.createGitRefresher(gitClient),
        a.createHTTPServer(gitClient, metrics.NewMetrics()),
    )
}
```

## 3. Refactor `createHTTPServer()` to use a gorilla mux subrouter

Replace the entire `createHTTPServer` method body with the version below. The key change: `/api/v1` routes move to a subrouter that optionally runs auth middleware. Probe routes stay on the root router.

```go
func (a *application) createHTTPServer(
    gitClient git.Git,
    m metrics.Metrics,
) run.Func {
    return func(ctx context.Context) error {
        getH := factory.CreateFilesGetHandler(gitClient)
        postH := factory.CreateFilesPostHandler(gitClient)
        deleteH := factory.CreateFilesDeleteHandler(gitClient)
        listH := factory.CreateFilesListHandler(gitClient)
        healthzH := factory.CreateHealthzHandler()
        readinessH := factory.CreateReadinessHandler(gitClient)

        router := gorillamux.NewRouter().SkipClean(true)

        // API subrouter — optionally wrapped with gateway secret auth.
        // Probes are NOT registered here so they are never wrapped by auth.
        apiRouter := router.PathPrefix("/api/v1").Subrouter()
        if a.GatewaySecret != "" {
            apiRouter.Use(factory.CreateGatewaySecretMiddleware(a.GatewaySecret))
        }
        apiRouter.Handle("/files/{path:.*}", factory.CreateFilesDispatchHandler(getH, listH)).
            Methods(http.MethodGet)
        apiRouter.Handle("/files/{path:.*}", postH).Methods(http.MethodPost)
        apiRouter.Handle("/files/{path:.*}", deleteH).Methods(http.MethodDelete)

        // Probe routes — always unauthenticated (kubelet + Prometheus have no secret).
        router.Handle("/healthz", healthzH)
        router.Handle("/readiness", readinessH)
        router.Handle("/metrics", promhttp.Handler())

        return libhttp.NewServer(
            a.Listen,
            factory.CreateMetricsMiddleware(m, router),
            func(o *libhttp.ServerOptions) {
                o.ReadTimeout = 60 * time.Second
                o.WriteTimeout = 60 * time.Second
                o.IdleTimeout = 120 * time.Second
            },
        ).Run(ctx)
    }
}
```

No new imports needed — `gorillamux` is already imported.

## 4. Add integration test in `main_test.go`

Append a new `Describe("GatewaySecretRouting", ...)` block to `main_test.go` AFTER all existing Describe blocks.

The test constructs a gorilla mux router with the same topology as `createHTTPServer` (subrouter on `/api/v1` with auth middleware, probe routes on the root router) and exercises all acceptance criteria paths via `httptest.NewServer` or `httptest.NewRecorder`.

Add the following imports to `main_test.go` if not already present:
- `"net/http"`
- `"net/http/httptest"`
- `"strings"`
- `gorillamux "github.com/gorilla/mux"`
- `"github.com/bborbe/git-rest/mocks"`
- `"github.com/bborbe/git-rest/pkg/factory"`

The integration test wraps the gorilla router with `factory.CreateMetricsMiddleware(fakeMetrics, router)` — exactly mirroring the production composition order in `createHTTPServer`. This is what verifies the spec AC "Existing HTTP metrics observe `401` and `500` auth-failure responses": after a request with bad auth headers, the `mocks.FakeMetrics` records the failure status as a label, proving auth wraps INSIDE the metrics middleware (i.e. metrics see auth's response code, not a synthetic `200`).

Test block:

```go
var _ = Describe("GatewaySecretRouting", func() {
    const secret = "routing-test-secret"

    var (
        wrapped     http.Handler
        fakeMetrics *mocks.FakeMetrics
        rec         *httptest.ResponseRecorder
    )

    BeforeEach(func() {
        rec = httptest.NewRecorder()
        fakeMetrics = &mocks.FakeMetrics{}

        // Mirror the topology of createHTTPServer:
        // - /api/v1 subrouter with auth middleware
        // - probe routes on root router (no auth)
        // - metrics middleware wraps EVERYTHING so it sees auth failures too
        router := gorillamux.NewRouter().SkipClean(true)

        apiRouter := router.PathPrefix("/api/v1").Subrouter()
        apiRouter.Use(factory.CreateGatewaySecretMiddleware(secret))

        apiRouter.HandleFunc("/files/{path:.*}", func(w http.ResponseWriter, r *http.Request) {
            // Echo whether X-Gateway-Secret reached the inner handler.
            if r.Header.Get("X-Gateway-Secret") != "" {
                w.WriteHeader(http.StatusInternalServerError) // test signal: secret leaked
                return
            }
            w.WriteHeader(http.StatusOK)
        })

        router.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
            w.WriteHeader(http.StatusOK)
        })
        router.HandleFunc("/readiness", func(w http.ResponseWriter, r *http.Request) {
            w.WriteHeader(http.StatusOK)
        })

        wrapped = factory.CreateMetricsMiddleware(fakeMetrics, router)
    })

    Context("API routes — missing X-Gateway-Initator", func() {
        It("returns 500 with exact body", func() {
            req := httptest.NewRequest(http.MethodGet, "/api/v1/files/README.md", nil)
            wrapped.ServeHTTP(rec, req)
            Expect(rec.Code).To(Equal(http.StatusInternalServerError))
            Expect(strings.TrimSpace(rec.Body.String())).To(Equal("header 'X-Gateway-Initator' missing"))
        })

        It("metrics middleware records the 500", func() {
            req := httptest.NewRequest(http.MethodGet, "/api/v1/files/README.md", nil)
            wrapped.ServeHTTP(rec, req)
            Expect(fakeMetrics.IncHTTPRequestCallCount()).To(Equal(1))
            method, path, status := fakeMetrics.IncHTTPRequestArgsForCall(0)
            Expect(method).To(Equal(http.MethodGet))
            Expect(path).To(Equal("/api/v1/files/{path}"))
            Expect(status).To(Equal("500"))
        })
    })

    Context("API routes — wrong X-Gateway-Secret", func() {
        It("returns 401 with exact body", func() {
            req := httptest.NewRequest(http.MethodGet, "/api/v1/files/README.md", nil)
            req.Header.Set("X-Gateway-Initator", "integration-test")
            req.Header.Set("X-Gateway-Secret", "wrong-secret")
            wrapped.ServeHTTP(rec, req)
            Expect(rec.Code).To(Equal(http.StatusUnauthorized))
            Expect(strings.TrimSpace(rec.Body.String())).To(Equal("secret in header 'X-Gateway-Secret' is invalid => access denied"))
        })

        It("metrics middleware records the 401", func() {
            req := httptest.NewRequest(http.MethodGet, "/api/v1/files/README.md", nil)
            req.Header.Set("X-Gateway-Initator", "integration-test")
            req.Header.Set("X-Gateway-Secret", "wrong-secret")
            wrapped.ServeHTTP(rec, req)
            Expect(fakeMetrics.IncHTTPRequestCallCount()).To(Equal(1))
            method, path, status := fakeMetrics.IncHTTPRequestArgsForCall(0)
            Expect(method).To(Equal(http.MethodGet))
            Expect(path).To(Equal("/api/v1/files/{path}"))
            Expect(status).To(Equal("401"))
        })
    })

    Context("API routes — correct headers", func() {
        It("reaches the inner handler and strips X-Gateway-Secret", func() {
            req := httptest.NewRequest(http.MethodGet, "/api/v1/files/README.md", nil)
            req.Header.Set("X-Gateway-Initator", "integration-test")
            req.Header.Set("X-Gateway-Secret", secret)
            wrapped.ServeHTTP(rec, req)
            // Inner handler returns 200 only when X-Gateway-Secret is absent
            Expect(rec.Code).To(Equal(http.StatusOK))
        })
    })

    Context("probe routes — always unauthenticated", func() {
        It("/healthz returns 200 with no headers", func() {
            req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
            wrapped.ServeHTTP(rec, req)
            Expect(rec.Code).To(Equal(http.StatusOK))
        })

        It("/readiness returns 200 with no headers", func() {
            req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
            wrapped.ServeHTTP(rec, req)
            Expect(rec.Code).To(Equal(http.StatusOK))
        })
    })
})
```

## 5. Activate scenario files

Update the frontmatter `status` in both scenario files from `idea` to `active`:

In `scenarios/008-gateway-secret-auth.md`, change:
```
---
status: idea
---
```
to:
```
---
status: active
---
```

In `scenarios/009-gateway-secret-disabled.md`, change:
```
---
status: idea
---
```
to:
```
---
status: active
---
```

## 6. Add CHANGELOG entry

In `CHANGELOG.md`, add a `## Unreleased` section directly after the preamble (before the first `## v` heading). If `## Unreleased` already exists, append the bullet to it instead of creating a new section.

```markdown
## Unreleased

- feat: Optional shared-secret HTTP auth on `/api/v1/*` via `--gateway-secret` / `GATEWAY_SECRET`. Missing or wrong `X-Gateway-Secret` → 401; missing `X-Gateway-Initator` → 500. Probes (`/healthz`, `/readiness`, `/metrics`) remain unauthenticated. Empty secret (default) disables auth and logs a startup warning — no behavior change for existing deployments.
```

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass (`CleanupStaleLocks`, `RecoverUntracked`, `SyncOnStartup`, `ResolveGitSSHCommand` Describe blocks, and all pkg/* tests)
- The `CreateGatewaySecretMiddleware` factory function from `pkg/factory/factory.go` must exist before running this prompt — it was added by prompt 1-spec-004. If missing, stop and report status: failed.
- Probe routes (`/healthz`, `/readiness`, `/metrics`) must be registered on the ROOT router, not the apiRouter subrouter — this is what keeps them unauthenticated
- Middleware composition order: `factory.CreateMetricsMiddleware(m, router)` wraps the ENTIRE router (root), so auth 401/500 responses appear in Prometheus metrics with their actual status codes
- The `if a.GatewaySecret != ""` conditional in `createHTTPServer` is wiring/composition logic on the application struct — NOT business logic in a factory function; it is correct to put it in the method body
- `display:"length"` tag on `GatewaySecret` field is required — effective-config logs must not print the secret value
- The startup warning must emit exactly once (it's a plain if-check, not a loop). It must appear in the Run() method after bootstrap, before the HTTP server is created
- Use `log/slog` (`slog.WarnContext`) — do NOT add `github.com/golang/glog`
- Use `errors.Wrap` from `github.com/bborbe/errors` — never `fmt.Errorf`
- `context.Background()` must NOT appear in `pkg/`
- No new external dependencies
- Header name `X-Gateway-Initator` (no second 'i') is frozen public API — do not correct the spelling anywhere
</constraints>

<verification>
`make precommit` — must pass.

Spot-check the new integration tests specifically:
```bash
cd /workspace && go test . -v -run "GatewaySecretRouting"
```
Expected: all 7 It blocks pass (missing-initiator + body, missing-initiator metrics, wrong-secret + body, wrong-secret metrics, correct-headers + strip, /healthz, /readiness).

Verify the new flag appears in help output:
```bash
cd /workspace && go run main.go --help 2>&1 | grep gateway-secret
```
Expected: line containing `--gateway-secret` and `GATEWAY_SECRET`.

Verify scenarios are active:
```bash
grep "status:" /workspace/scenarios/008-gateway-secret-auth.md /workspace/scenarios/009-gateway-secret-disabled.md
```
Expected: both show `status: active`.
</verification>
