---
status: completed
spec: [018-leased-claim-ref-operations]
summary: Added validated claim-ref HTTP handlers, route wiring, fixed ref path metric labels, and the pre-initialised git_rest_ref_requests_total counter with handler-level and real-origin integration tests.
execution_id: git-rest-claim-refs-exec-070-spec-018-ref-http-handlers-metrics
dark-factory-version: v0.198.7
created: "2026-10-21T18:06:52Z"
queued: "2026-10-21T18:17:00Z"
started: "2026-10-21T18:26:23Z"
completed: "2026-10-21T18:33:42Z"
branch: dark-factory/leased-claim-ref-operations
---

# Expose claim refs over HTTP: handlers, routing, auth and the ref-request metric

<summary>
- git-rest exposes three claim-ref endpoints on the existing `/api/v1` subrouter: list, create/renew and delete.
- Invalid claim names, malformed bodies and invalid query parameters are answered with `400` before any origin-contacting git operation runs.
- A lost lease race is answered with `409`; every other failure is a `500`; nothing is retried.
- The gateway-secret middleware applies to the new routes exactly as it does to file routes.
- A new counter makes claim traffic and lost races visible on `/metrics`, with all twelve series present at zero before any request.
- HTTP request metrics record ref requests under one fixed path template per route, never the claim key.
- The production router construction is extracted into one function so tests exercise the real route topology.
- The fake git client observes zero ref calls for the entire namespace-rejection table.
- No documentation or changelog change is made in this prompt.
</summary>

<objective>
Put a thin, fully validated HTTP adapter on top of the claim-ref git layer: three handlers that parse the key, body and query, map the git layer's conflict sentinel to `409`, wire the routes onto the authenticated `/api/v1` subrouter, and count every request that reaches a ref handler in `git_rest_ref_requests_total{op,result}`. Prove the namespace guard, request validation, auth behaviour, bounded metric labels and pre-initialised series with handler-level and real-origin integration tests.
</objective>

<context>
There is no repo-root `CLAUDE.md` in this worktree — do not look for one. Project conventions live in `docs/dod.md` and the coding-plugin guides below.

Intentional: tests use plain `testing` (the spec's `-run` evidence commands need named Go tests); CHANGELOG is owned by prompt 3 — neither is a DoD blocker.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-handler-refactoring-guide.md` — handlers live in `pkg/handler/`, factories in `pkg/factory/`, no inline handlers in `main.go`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-json-error-handler-guide.md` — `libhttp.NewJSONErrorHandler`, `libhttp.WrapWithStatusCode`, status mapping
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` — interface-based metrics, `init()` pre-initialisation, label cardinality
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md` — `Create*` naming, zero logic in factories
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega, Counterfeiter mocks, external test packages
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — linter limits and license headers
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` — new-code coverage and verification rules

Files to read in full before editing (anchor by symbol name, not line number):

- `pkg/git/claim_refs.go` — the git layer added by prompt 1: `ClaimRef`, `ClaimRefRequest`, `ErrRefConflict`, `ErrInvalidRefName`, `ErrNoRemote`, `ValidateClaimKey`, `ListClaimRefs`, `PutClaimRef`, `DeleteClaimRef`
- `pkg/git/git.go` — the `Git` interface those methods belong to
- `pkg/handler/files_get.go`, `pkg/handler/files_post.go`, `pkg/handler/files_list.go` — the `libhttp.WithError` handler style, `MaxBytesReader` and `MaxBytesError` handling, `SendJSONResponse`
- `pkg/handler/metrics_middleware.go` — `routeLabel` and the `httpsnoop.CaptureMetrics` pattern
- `pkg/handler/gateway_secret_middleware.go` — the auth contract (`500` without `X-Gateway-Initator`, `401` with a wrong `X-Gateway-Secret`)
- `pkg/metrics/metrics.go` — the `Metrics` interface, `HTTPRequestsTotal`, and the `init()` pre-initialisation loop
- `pkg/metrics/metrics_test.go` — the default-registry gather helpers used to assert series presence
- `pkg/factory/factory.go` — `CreateFiles*Handler` and middleware constructors
- `main.go` — `createHTTPServer`, the `/api/v1` subrouter, `SkipClean(true)` and the probe routes
- `export_test.go` — the pattern for exposing a `main` function to `package main_test`
- `main_test.go` — `GatewaySecretRouting` (router topology and fake-metrics assertions) and the existing repository fixtures
- `pkg/git/git_test.go` and `pkg/git/resolve_conflict_merge_test.go` — the two hand-written `metrics.Metrics` implementations (`noopMetrics`, `unsafeTestMetrics`) that must gain the new method

**Verified facts — do not re-derive, do not contradict:**

- `libhttp.WithErrorFunc(func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error) libhttp.WithError` is the handler shape; `libhttp.NewJSONErrorHandler` defaults an unwrapped error to `500` and honours `libhttp.WrapWithStatusCode(err, code)`.
- `libhttp.SendJSONResponse(ctx, resp, data, statusCode) error` sets `application/json` and encodes the value.
- `http.MaxBytesReader(resp, req.Body, n)` returns an error that `errors.As(err, &maxBytesErr)` matches as `*http.MaxBytesError`; `files_post.go` maps it to `413`.
- `gorillamux.Vars(req)["key"]` returns the matched path value from the already percent-decoded `req.URL.Path`. `main.go` already builds the router with `gorillamux.NewRouter().SkipClean(true)`, so `..` is not cleaned away before matching.
- `httpsnoop.CaptureMetrics(next, w, r)` returns a struct whose `Code` field is the final status, including statuses written by `NewJSONErrorHandler`.
- `github.com/prometheus/client_golang/prometheus/testutil` is available and `testutil.ToFloat64(collector)` reads a counter child's current value. No new module dependency is needed.
- `pkg/metrics/metrics.go` pre-initialises every series in `init()` with `.Add(0)`; the new counter follows that pattern.
- `main.go`'s `createHTTPServer` currently constructs handlers and registers `/files/{path:.*}` on the `apiRouter`; the gateway-secret middleware is mounted on that subrouter only, so probes stay unauthenticated.
- The prompt-1 git layer already validates keys and expected shas, but the HTTP layer must reject a bad key before calling the fake in the handler test, so it validates first.

**Open question for the reviewer:** the spec fixes the body limit at 64 KiB but does not name a `413` body. This prompt uses the library's JSON error body, matching the existing oversized-file response.

**Scope:** this prompt touches `pkg/handler/`, `pkg/factory/`, `pkg/metrics/`, `main.go`, `export_test.go`, `main_test.go` (new test file only), the two hand-written metrics fakes in `pkg/git/` tests, and the regenerated mocks. It does not touch `pkg/git/claim_refs.go`'s behaviour, docs, `README.md`, `CHANGELOG.md` or `specs/`.
</context>

<requirements>

## 1. Add the ref-request metric

In `pkg/metrics/metrics.go`:

```go
// RefRequestsTotal counts claim-ref requests by operation and HTTP status code.
// op is "get", "put" or "delete"; result is the status code returned as a string.
var RefRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
    Name: "git_rest_ref_requests_total",
    Help: "Total claim-ref requests by operation and HTTP status code.",
}, []string{"op", "result"})
```

- Register it in `init()` alongside the existing metrics.
- Pre-initialise all twelve series with `.Add(0)`: `op` in `get`, `put`, `delete`; `result` in `200`, `400`, `409`, `500`.
- Add to the `Metrics` interface:

```go
// IncRefRequest records one claim-ref request by operation ("get", "put",
// "delete") and the HTTP status code returned as a string.
IncRefRequest(op, result string)
```

- Implement it on `prometheusMetrics` as `RefRequestsTotal.WithLabelValues(op, result).Inc()`.

Update both hand-written implementations of `metrics.Metrics`:

- `pkg/git/git_test.go` — `noopMetrics`: add `func (n *noopMetrics) IncRefRequest(_, _ string) {}`.
- `pkg/git/resolve_conflict_merge_test.go` — `unsafeTestMetrics`: add `func (u *unsafeTestMetrics) IncRefRequest(_, _ string) {}`.

Regenerate `mocks/metrics.go` with `go generate ./...` (or `make generate`); do not hand-edit it.

Add a spec to `pkg/metrics/metrics_test.go` asserting that gathering the default registry yields exactly 12 metrics named `git_rest_ref_requests_total`, each with value `0`, before any request is handled in that test process. Use `prometheus.DefaultGatherer.Gather()` and count the `GetMetric()` entries; do not use `testutil.ToFloat64` for this assertion, because it returns 0 for a series that does not exist.

## 2. Add the three handlers in `pkg/handler/`

Create `pkg/handler/refs_get.go`, `pkg/handler/refs_put.go`, `pkg/handler/refs_delete.go`, and a shared `pkg/handler/refs_validate.go`. Each file carries the three-line BSD license header.

Signatures:

```go
func NewRefsGetHandler(g git.Git) libhttp.WithError
func NewRefsPutHandler(g git.Git) libhttp.WithError
func NewRefsDeleteHandler(g git.Git) libhttp.WithError
```

The PUT and DELETE handlers read the key with `gorillamux.Vars(req)["key"]` (the GET route has no key and never calls `ValidateClaimKey`). The key is the full path below `/refs/`, e.g. `claims/task/t-42`. Before calling any git method, PUT and DELETE call `git.ValidateClaimKey(ctx, key)`; a failure is answered with `400` via `libhttp.WrapWithStatusCode`. This ordering is what makes the handler-level namespace test observe zero git calls.

`NewRefsGetHandler`:

1. Read `prefix := req.URL.Query().Get("prefix")`.
2. Validate the prefix; on failure return `400`:
   - non-empty;
   - starts with `claims/`;
   - does not contain `..`;
   - does not contain any of `*`, `?`, `[`.
3. Call `g.ListClaimRefs(ctx, prefix)`.
4. On error return a wrapped error; `git.ErrInvalidRefName` maps to `400`, everything else (including `git.ErrNoRemote`) maps to `500` by returning the wrapped error unwrapped with a status.
5. Return `libhttp.SendJSONResponse(ctx, resp, refs, http.StatusOK)`. The git layer already returns a non-nil empty slice, so the body is `[]` and never `null`.

`NewRefsPutHandler`:

1. Validate the key with `git.ValidateClaimKey`; failure → `400`.
2. Wrap the body with `http.MaxBytesReader(resp, req.Body, 64*1024)`, read it with `io.ReadAll`. A `*http.MaxBytesError` → `413`; any other read error is wrapped and returned.
3. `json.Unmarshal` the body into `git.ClaimRefRequest`. A decode error → `400`. Unknown keys (including `nonce`) are ignored by `encoding/json` and must not fail the request.
4. Validate the request; each failure → `400` with a distinct wrapped message:
   - `Cluster` non-empty;
   - `TaskID` non-empty;
   - `LeaseUntil` parses with `time.Parse(time.RFC3339, …)`;
   - `ExpectedSHA` is empty or matches `^([0-9a-f]{40}|[0-9a-f]{64})$`.
5. Call `g.PutClaimRef(ctx, key, request)`.
6. Map errors: `errors.Is(err, git.ErrRefConflict)` → `409`; `errors.Is(err, git.ErrInvalidRefName)` → `400`; anything else is wrapped and returned so the JSON error handler answers `500`.
7. On success return `200` with the JSON object `{"ref":"refs/"+key,"sha":<sha>}`.

`NewRefsDeleteHandler`:

1. Validate the key with `git.ValidateClaimKey`; failure → `400`.
2. Read `expected := req.URL.Query().Get("expected_sha")`; it must be non-empty and match `^([0-9a-f]{40}|[0-9a-f]{64})$`; failure → `400`.
3. Call `g.DeleteClaimRef(ctx, key, expected)`.
4. Map `git.ErrRefConflict` → `409`, `git.ErrInvalidRefName` → `400`, everything else → `500`.
5. On success return `200` with `{"ok":true}`.

Do not add any retry, fallback or background work to these handlers. Do not count metrics inside the handlers; requirement 3 does that for every status the handler can produce.

## 3. Add the ref metrics middleware in `pkg/handler/ref_metrics_middleware.go`

```go
// NewRefMetricsMiddleware records the final HTTP status of every request that
// reaches a claim-ref handler under the given op ("get", "put" or "delete").
func NewRefMetricsMiddleware(m metrics.Metrics, op string, next http.Handler) http.Handler
```

Use `httpsnoop.CaptureMetrics(next, w, r)` and `m.IncRefRequest(op, strconv.Itoa(captured.Code))`, mirroring `NewMetricsMiddleware` in `pkg/handler/metrics_middleware.go`. The middleware must sit inside the route handler chain, so requests rejected by the gateway-secret middleware never reach it and never increment the counter.

## 4. Bound the HTTP path label

In `pkg/handler/metrics_middleware.go`, extend `routeLabel`:

```go
if path == "/api/v1/refs" {
    return "/api/v1/refs"
}
if strings.HasPrefix(path, "/api/v1/refs/") {
    return "/api/v1/refs/{key}"
}
```

Keep the existing `/api/v1/files/` branch unchanged. Do not add the claim key, the prefix query or the method to the label value.

## 5. Add the factories in `pkg/factory/factory.go`

```go
func CreateRefsGetHandler(g git.Git, m metrics.Metrics) http.Handler
func CreateRefsPutHandler(g git.Git, m metrics.Metrics) http.Handler
func CreateRefsDeleteHandler(g git.Git, m metrics.Metrics) http.Handler
```

Each composes `handler.NewRefMetricsMiddleware(m, "<op>", libhttp.NewJSONErrorHandler(handler.NewRefs<X>Handler(g)))` and returns the `http.Handler`. Keep the functions pure composition — no conditionals, no I/O.

## 6. Wire the routes and extract the router construction in `main.go`

Extract the handler-tree construction out of `createHTTPServer` into:

```go
// newRouter builds the HTTP handler tree: the authenticated /api/v1 subrouter
// (file routes and claim-ref routes) plus the always-unauthenticated probes.
func newRouter(
    ctx context.Context,
    gitClient git.Git,
    m metrics.Metrics,
    repoPath string,
    gatewaySecret string,
) http.Handler
```

Move the existing handler construction, subrouter, auth middleware and probe registration into `newRouter` unchanged. Register the three new routes on the same `apiRouter`, after the file routes:

```go
apiRouter.Handle("/refs", factory.CreateRefsGetHandler(gitClient, m)).Methods(http.MethodGet)
apiRouter.Handle("/refs/{key:.*}", factory.CreateRefsPutHandler(gitClient, m)).Methods(http.MethodPut)
apiRouter.Handle("/refs/{key:.*}", factory.CreateRefsDeleteHandler(gitClient, m)).Methods(http.MethodDelete)
```

The `{key:.*}` pattern is required so a malformed key still reaches the handler and is answered (and counted) as `400` rather than a router-level `404`/`405`. `/refs` and `/refs/{key:.*}` do not collide because they are registered on different methods.

`createHTTPServer` then becomes:

```go
return libhttp.NewServer(
    a.Listen,
    factory.CreateMetricsMiddleware(m, newRouter(ctx, gitClient, m, repoPath, a.GatewaySecret)),
    func(o *libhttp.ServerOptions) { /* unchanged timeouts */ },
).Run(ctx)
```

Do not change the listen address, the timeouts, the probe paths or the file routes.

In `export_test.go`, add `var NewRouter = newRouter` with a doc comment, following the existing exported-for-test pattern.

## 7. Add handler-level tests in `pkg/handler/refs_test.go`

Use `package handler_test`, the existing Ginkgo suite in `pkg/handler/handler_suite_test.go`, and `mocks.FakeGit`. Build a `gorillamux.NewRouter().SkipClean(true)` in the test and register the handler under test with the same `/api/v1/refs` patterns and method as production. Use `httptest.NewRequest` / `httptest.NewRecorder`.

Required specs:

1. **Namespace guard (AC4a).** Table over `heads/main`, `tags/x`, `claims/../heads/main`, `claims/%2E%2E/heads/main`, `claims/`, `claims`, `claims/a..b`, `claims/x.lock`, `claims/a b`, `claims/a~1`, `claims/a:b`, `claims//x`, `claims/x/`, for both `PUT` and `DELETE`, with an otherwise valid body/query. Every request returns `400`. After the whole table assert `fake.PutClaimRefCallCount()`, `fake.DeleteClaimRefCallCount()` and `fake.ListClaimRefsCallCount()` are all `0`. For the percent-encoded row, build the request path with `%2E%2E` and confirm the handler sees the decoded `..` (the router must be built with `SkipClean(true)`). For the space row, `claims/a b` is sent URL-encoded as `claims/a%20b` (build the request path that way); the handler must see the decoded `claims/a b`.
2. **PUT validation (AC5).** Table rows, each `400` unless noted: missing/empty `cluster`; missing/empty `task_id`; `lease_until` not RFC3339; `expected_sha` not empty and not 40/64 lowercase hex (e.g. uppercase, short, `:`); body that is not JSON. A body over 64 KiB returns `413` (build it with `strings.Repeat`). Assert the fake's `PutClaimRefCallCount()` stays `0` for every invalid row.
3. **DELETE validation (AC5).** Missing `expected_sha` → `400`; invalid `expected_sha` → `400`; fake delete call count `0`.
4. **GET validation (AC5).** Missing `prefix`, `prefix` not starting with `claims/`, `prefix` containing `..`, and `prefix` equal to `claims/*`, `claims/task/?`, `claims/[a]` each return `400`; fake list call count `0`.
5. **Success bodies.** Fake `PutClaimRefReturns("abc123", nil)` → `200` and body decodes to `{"ref":"refs/claims/task/t-1","sha":"abc123"}`. Fake `DeleteClaimRefReturns(nil)` → `200` and body decodes to `{"ok":true}`. Fake `ListClaimRefsReturns([]git.ClaimRef{}, nil)` → `200` and the body is `[]` (compare the raw trimmed body, not a decoded nil). Fake `ListClaimRefsReturns` with one populated `git.ClaimRef` → `200` and the decoded JSON object has exactly the keys `ref`, `sha`, `cluster`, `task_id`, `lease_until` — assert no `nonce` key (AC7).
6. **Error mapping.** Fake returns `git.ErrRefConflict` → `409`; fake returns a generic error → `500`; fake returns `git.ErrInvalidRefName` → `400`.

Add a spec in `pkg/handler/ref_metrics_middleware_test.go` (or the same file) asserting `NewRefMetricsMiddleware` calls `IncRefRequest` once with the operation and the final status, including a handler that returns an error mapped to `409` through `NewJSONErrorHandler`.

## 8. Add real-origin HTTP integration tests in a new root-package file `claim_refs_http_test.go`

Use `package main_test`, the existing Main suite, `main.NewRouter`, and a real `git.Git` client built with `git.New(...)`. Create a bare origin plus one clone in `t.TempDir()`/`GinkgoT().TempDir()`, configure a git identity, commit and push an initial `main` branch, and point the clone's `origin` at the bare path. This exercises the production router topology, the real git layer and the real metric wiring.

Required specs:

1. **Namespace guard and validation against a real origin (AC4b, AC5).** Record `git -C <bare> for-each-ref` before the request table, send the full AC4 namespace table and the AC5 PUT/DELETE/GET validation table through `main.NewRouter`, assert the expected statuses, then assert the `for-each-ref` output is byte-identical.
2. **Ref counter (AC10).** Snapshot all twelve `git_rest_ref_requests_total` series with `testutil.ToFloat64(metrics.RefRequestsTotal.WithLabelValues(op, result))`. Send one successful `PUT` (fresh key, empty expected) and assert `{op="put",result="200"}` increased by exactly 1 and no other series changed. Then send one `PUT` with a stale non-empty expected sha and assert `{op="put",result="409"}` increased by exactly 1 and no other series changed.
3. **Bounded path label (AC11).** Wrap `main.NewRouter(...)` in `factory.CreateMetricsMiddleware(metrics.NewMetrics(), router)`. Send `PUT`s to three distinct claim keys. Gather `prometheus.DefaultGatherer` and assert no `git_rest_http_requests_total` series has a `path` label containing any of the three keys, and that each ref request was recorded under `/api/v1/refs/{key}`.
4. **Auth applies (AC12).** Build the router with a non-empty gateway secret. A `PUT` with `X-Gateway-Initator` but no `X-Gateway-Secret` returns `401`; a `PUT` with neither header returns `500`. In both cases the bare origin's ref listing is unchanged and all twelve ref-counter series read the same values as before the request.
5. **Nonce is ignored on input (AC19 HTTP clause).** Send a `PUT` whose JSON body includes `"nonce":"deadbeefdeadbeefdeadbeefdeadbeef"`. It returns `200`; `git cat-file -p <sha>` shows a `nonce` that matches `^[0-9a-f]{32}$` and differs from the supplied value.
6. **Metadata round-trip over HTTP (AC7 HTTP side).** `PUT` `{"expected_sha":"","cluster":"nuke","task_id":"t-42","lease_until":"2026-10-21T12:05:00Z"}` to a fresh key, then `GET /api/v1/refs?prefix=claims/`; decode into `[]map[string]any` and assert exactly one object whose key set is exactly `ref`,`sha`,`cluster`,`task_id`,`lease_until` (no `nonce`), `ref == "refs/claims/<key>"`, `sha` equals the PUT response sha, and the three written values.

Use `errors.Is` for sentinel assertions where needed, `exec.Command` for the test-side git fixtures, and arguments passed separately.

## 9. Self-check before finishing

Run every command in `<verification>` from the repo root. Confirm the fake observes zero ref calls across the namespace table, the bare origin is byte-identical after every invalid request, the twelve counter series are pre-initialised and move only for the asserted operation/result pair, and `make precommit` exits 0. Walk each acceptance criterion assigned to this prompt (AC4, AC5, AC7 HTTP side, AC10, AC11, AC12, AC19 HTTP clause) against the change.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT modify `pkg/git/claim_refs.go` or any prompt-1 behaviour; this prompt only consumes it.
- Do NOT add a configuration flag, environment variable or CLI argument to enable, disable or rename the endpoints — `refs/claims/` is invariant.
- Do NOT add a single-ref `GET /api/v1/refs/claims/<key>` route.
- Do NOT change the file routes, the puller, readiness, the probes, the server timeouts or the gateway-secret contract.
- Do NOT put the claim key, the prefix or the method into a metric label; use the fixed path templates.
- Do NOT count ref requests in the handlers; the middleware owns the counter so every status is counted exactly once.
- Do NOT hand-edit generated mocks; run `go generate ./...` (or `make generate`).
- Do NOT run `go mod vendor` and do NOT use `-mod=vendor` in any command — this repo does not commit `vendor/`.
- Use `github.com/bborbe/errors` for wrapping; no `fmt.Errorf`. Error responses are produced by `libhttp.NewJSONErrorHandler` with `libhttp.WrapWithStatusCode`.
- Every new Go file carries the three-line BSD license header, or `addlicense` fails `make precommit`.
- Keep functions under the linter limits (`funlen` 80, `nestif` 4, lines 100); split the request validation into helpers if a handler grows too long.
- New code needs ≥80% statement coverage; the tables in requirements 7 and 8 cover every validation branch, both conflict and non-conflict error mappings, and the metric paths.
- Requests rejected by auth must leave all twelve `git_rest_ref_requests_total` series unchanged.
- The served branch and every ref outside `refs/claims/` remain untouched by these endpoints.
</constraints>

<verification>
Run from the repo root. The container's `.git` is masked, so this block contains no git commands against the project repo. The `git` commands inside the tests run against temporary repositories they create.

Focused runs while implementing (must exit 0):

```bash
go test -race -count=1 ./pkg/handler/...
go test -race -count=1 ./pkg/metrics/...
go test -race -count=1 -run 'TestSuite' .
```

Full package run (must exit 0; includes prompt 1's race tests):

```bash
go test -race -count=1 ./...
```

The wiring and metric surfaces exist (each must print at least one match):

```bash
test -f pkg/handler/refs_get.go
test -f pkg/handler/refs_put.go
test -f pkg/handler/refs_delete.go
test -f pkg/handler/ref_metrics_middleware.go
grep -n 'git_rest_ref_requests_total' pkg/metrics/metrics.go
grep -n 'IncRefRequest' pkg/metrics/metrics.go
grep -n 'IncRefRequest' pkg/git/git_test.go
grep -n 'IncRefRequest' pkg/git/resolve_conflict_merge_test.go
grep -n 'CreateRefsGetHandler' pkg/factory/factory.go
grep -n 'CreateRefsPutHandler' pkg/factory/factory.go
grep -n 'CreateRefsDeleteHandler' pkg/factory/factory.go
grep -n '"/refs"' main.go
grep -n 'refs/{key' main.go
grep -n 'api/v1/refs/{key}' pkg/handler/metrics_middleware.go
grep -n 'NewRouter' export_test.go
```

Hard constraints on the wiring:

```bash
! grep -q 'ClaimRef' main.go
! grep -q 'refs/claims' pkg/handler/metrics_middleware.go
```

Final gate:

```bash
ROOTDIR=/workspace make precommit
```

Must exit 0.
</verification>
