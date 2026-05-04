---
status: approved
spec: [005-bug-readiness-blocks-on-pull-mutex]
created: "2026-05-04T09:15:00Z"
queued: "2026-05-04T13:48:59Z"
branch: dark-factory/bug-readiness-blocks-on-pull-mutex
---

<summary>
- The `/readiness` handler no longer invokes any git subprocess or blocks on the pull mutex
- Readiness is now driven entirely by the `PullStateCache` written by the background puller
- Before the first successful pull, `/readiness` returns 503 with body `no successful pull yet`
- When the last successful pull is older than the freshness threshold, returns 503 with a stale-cache message naming the cause (never `context canceled`)
- The 503 body is plain text describing the real failure — operators can triage from the probe alone
- A new `ReadinessStateReader` interface in `pkg/handler` decouples the handler from the puller package
- The factory `CreateReadinessHandler` is updated to accept `ReadinessStateReader` instead of `git.Git`
- `main.go` wires `pullState` through `createHTTPServer` to the readiness factory
- Existing healthy-path behavior (200 with body `ok` while pulls are succeeding) is preserved
- `make precommit` passes; CHANGELOG updated
</summary>

<objective>
Update the readiness handler to read from the `PullStateCache` introduced in prompt 1 instead of calling `git.Status()`. This eliminates the mutex contention that caused false-NotReady under SSH timeout. The handler never calls git, never blocks, and always answers within the Kubernetes probe timeout.

Precondition: `pkg/puller/pull_state.go` must exist with `PullStateCache`, `NewPullStateCache`, and `RecordPull` (added by prompt 1 of spec 005). Verify before starting:

```bash
grep -n "PullStateCache\|NewPullStateCache" /workspace/pkg/puller/pull_state.go
```

If missing, stop and report status: failed with message "PullStateCache not found — run 1-spec-005-pull-state first".
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read coding guides before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-patterns.md` — public interface + private struct + New* constructor; interfaces at point of use
- `go-http-handler-refactoring-guide.md` — handler location in `pkg/handler/`, factory naming
- `go-json-error-handler-guide.md` — house style is `WrapWithStatusCode` + `NewJSONErrorHandler`. THIS handler intentionally deviates: it writes the 503 body directly to `resp` (plain text) so `context canceled` cannot leak through error wrapping. The factory still wraps it with `NewJSONErrorHandler` to preserve the metrics middleware contract; the wrapper is a no-op when the handler returns nil.
- `go-testing-guide.md` — Ginkgo/Gomega, counterfeiter mocks, external test packages (`package handler_test`)
- `go-error-wrapping-guide.md` — never `fmt.Errorf`

Files to read in full before implementing:
- `pkg/handler/readiness.go` — current handler; you will replace its constructor and remove the `git.Git` dependency
- `pkg/handler/readiness_test.go` — existing tests; rewrite them for the new interface; confirm test file exists with `ls pkg/handler/`
- `pkg/handler/handler_suite_test.go` — Ginkgo suite; your tests join it (no changes needed)
- `pkg/factory/factory.go` — `CreateReadinessHandler`; update its parameter type
- `main.go` — `createHTTPServer` method (~line 381) and `Run()` method (~line 55); you will thread `pullState` through
- `pkg/puller/pull_state.go` — read the `ReadinessStatus() (bool, string)` method signature (you will define the matching interface in `pkg/handler`)
- `CHANGELOG.md` — add `## Unreleased` section
</context>

<requirements>

## 1. Add `ReadinessStateReader` interface to `pkg/handler/readiness.go`

Add the interface above `NewReadinessHandler`. This interface is defined at the point of use (in the handler package) so the handler has no dependency on `pkg/puller`.

```go
// ReadinessStateReader is the interface the readiness handler reads from.
// Implemented by *puller.PullStateCache.
type ReadinessStateReader interface {
	ReadinessStatus() (bool, string)
}
```

## 2. Rewrite `NewReadinessHandler` in `pkg/handler/readiness.go`

Replace the existing implementation entirely. The new handler reads from the cache without calling any git method:

```go
// NewReadinessHandler returns a handler that reports readiness based on cached pull state.
// It never invokes a git subprocess and never blocks on the pull mutex.
func NewReadinessHandler(s ReadinessStateReader) libhttp.WithError {
	return libhttp.WithErrorFunc(
		func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
			ready, reason := s.ReadinessStatus()
			if !ready {
				resp.WriteHeader(http.StatusServiceUnavailable)
				_, _ = resp.Write([]byte(reason))
				return nil
			}
			_, _ = resp.Write([]byte("ok"))
			return nil
		},
	)
}
```

Remove the import of `"github.com/bborbe/git-rest/pkg/git"` from this file — it is no longer needed. Keep `"github.com/bborbe/errors"` only if still used; remove it if not. Keep `libhttp "github.com/bborbe/http"`.

The full updated `pkg/handler/readiness.go`:

```go
// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"

	libhttp "github.com/bborbe/http"
)

// ReadinessStateReader is the interface the readiness handler reads from.
// Implemented by *puller.PullStateCache.
type ReadinessStateReader interface {
	ReadinessStatus() (bool, string)
}

// NewReadinessHandler returns a handler that reports readiness based on cached pull state.
// It never invokes a git subprocess and never blocks on the pull mutex.
func NewReadinessHandler(s ReadinessStateReader) libhttp.WithError {
	return libhttp.WithErrorFunc(
		func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
			ready, reason := s.ReadinessStatus()
			if !ready {
				resp.WriteHeader(http.StatusServiceUnavailable)
				_, _ = resp.Write([]byte(reason))
				return nil
			}
			_, _ = resp.Write([]byte("ok"))
			return nil
		},
	)
}
```

## 3. Generate a counterfeiter mock for `ReadinessStateReader`

Add a counterfeiter annotation directly above the interface declaration:

```go
//counterfeiter:generate -o ../../mocks/readiness_state_reader.go --fake-name FakeReadinessStateReader . ReadinessStateReader
```

Then run mock generation. Check the Makefile for the correct generation command:

```bash
grep -n "counterfeiter\|generate" /workspace/Makefile | head -20
```

Run the appropriate command (likely `make generate` or `go generate ./pkg/handler/...`).

## 4. Update `pkg/factory/factory.go` — `CreateReadinessHandler`

Change the parameter type from `git.Git` to `handler.ReadinessStateReader`:

```go
// CreateReadinessHandler returns an http.Handler for GET /readiness.
func CreateReadinessHandler(s handler.ReadinessStateReader) http.Handler {
	return libhttp.NewJSONErrorHandler(handler.NewReadinessHandler(s))
}
```

Keep the `git` import — other factory functions (`CreateGitClient`, `CreateFilesGetHandler`, etc.) still use it.

**Audit all callers** — `CreateReadinessHandler` is a public function whose signature is changing. Find every call site and update accordingly:

```bash
grep -rn "CreateReadinessHandler" /workspace --include='*.go'
```

Expected callers: `main.go` `createHTTPServer` (updated in §5). Update any other call site that appears.

**Check `pkg/factory/factory_test.go`** — if it exists and exercises `CreateReadinessHandler`:

```bash
ls /workspace/pkg/factory/
```

If `factory_test.go` references `CreateReadinessHandler`, update those tests to pass a `*mocks.FakeReadinessStateReader` (generated in §3) or a real `puller.NewPullStateCache(libtime.NewCurrentDateTime(), 0)` instead of a `git.Git`.

## 5. Update `main.go` — `createHTTPServer` signature and body

Add `pullState *puller.PullStateCache` as a parameter and pass it to `CreateReadinessHandler` instead of `gitClient`:

```go
func (a *application) createHTTPServer(
	gitClient git.Git,
	m metrics.Metrics,
	pullState *puller.PullStateCache,
) run.Func {
	return func(ctx context.Context) error {
		getH := factory.CreateFilesGetHandler(gitClient)
		postH := factory.CreateFilesPostHandler(gitClient)
		deleteH := factory.CreateFilesDeleteHandler(gitClient)
		listH := factory.CreateFilesListHandler(gitClient)
		healthzH := factory.CreateHealthzHandler()
		readinessH := factory.CreateReadinessHandler(pullState)

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

## 6. Update `main.go` — `Run()` — pass `pullState` to `createHTTPServer`

Change the `service.Run` call to pass `pullState`:

```go
return service.Run(ctx,
	a.createGitRefresher(gitClient, pullState),
	a.createHTTPServer(gitClient, metrics.NewMetrics(), pullState),
)
```

The full updated `Run()` method:

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

	pullState := puller.NewPullStateCache(3 * time.Duration(a.PullInterval))

	return service.Run(ctx,
		a.createGitRefresher(gitClient, pullState),
		a.createHTTPServer(gitClient, metrics.NewMetrics(), pullState),
	)
}
```

## 7. Rewrite `pkg/handler/readiness_test.go`

Replace the content entirely. The new tests use `FakeReadinessStateReader` (generated in step 3) instead of `FakeGit`. The handler is tested as `libhttp.WithError` (the raw return type of `NewReadinessHandler`) — matching the pattern of the existing test file. Since the new handler writes directly to the response and always returns `nil`, the error return is always nil.

```go
// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"

	libhttp "github.com/bborbe/http"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/handler"
)

var _ = Describe("ReadinessHandler", func() {
	var (
		fakeState *mocks.FakeReadinessStateReader
		h         libhttp.WithError
		rec       *httptest.ResponseRecorder
		ctx       context.Context
	)

	BeforeEach(func() {
		fakeState = &mocks.FakeReadinessStateReader{}
		h = handler.NewReadinessHandler(fakeState)
		rec = httptest.NewRecorder()
		ctx = context.Background()
	})

	Context("when ReadinessStatus returns ready", func() {
		BeforeEach(func() {
			fakeState.ReadinessStatusReturns(true, "ok")
		})

		It("returns 200", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			err := h.ServeHTTP(ctx, rec, req)
			Expect(err).To(BeNil())
			Expect(rec.Code).To(Equal(http.StatusOK))
		})

		It("returns body 'ok'", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			_ = h.ServeHTTP(ctx, rec, req)
			Expect(rec.Body.String()).To(Equal("ok"))
		})
	})

	Context("when ReadinessStatus returns not ready: no successful pull yet", func() {
		BeforeEach(func() {
			fakeState.ReadinessStatusReturns(false, "no successful pull yet")
		})

		It("returns 503", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			err := h.ServeHTTP(ctx, rec, req)
			Expect(err).To(BeNil())
			Expect(rec.Code).To(Equal(http.StatusServiceUnavailable))
		})

		It("body contains 'no successful pull yet'", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			_ = h.ServeHTTP(ctx, rec, req)
			Expect(rec.Body.String()).To(ContainSubstring("no successful pull yet"))
		})

		It("body does not contain 'context canceled'", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			_ = h.ServeHTTP(ctx, rec, req)
			Expect(rec.Body.String()).NotTo(ContainSubstring("context canceled"))
		})
	})

	Context("when ReadinessStatus returns not ready: stale cache", func() {
		BeforeEach(func() {
			fakeState.ReadinessStatusReturns(false, "last successful pull stale (2m30s ago): last pull failed: ssh timeout")
		})

		It("returns 503", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			err := h.ServeHTTP(ctx, rec, req)
			Expect(err).To(BeNil())
			Expect(rec.Code).To(Equal(http.StatusServiceUnavailable))
		})

		It("body contains the stale reason", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			_ = h.ServeHTTP(ctx, rec, req)
			Expect(rec.Body.String()).To(ContainSubstring("last successful pull stale"))
		})

		It("body does not contain 'context canceled'", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			_ = h.ServeHTTP(ctx, rec, req)
			Expect(rec.Body.String()).NotTo(ContainSubstring("context canceled"))
		})
	})
})
```

## 8. Add CHANGELOG entry

In `CHANGELOG.md`, add a `## Unreleased` section directly after the preamble, before `## v0.18.0`. If `## Unreleased` already exists, append the bullet to it:

```markdown
## Unreleased

- fix: Decouple `/readiness` from the pull mutex. Readiness now reads a `PullStateCache` written by the background puller — the handler never invokes a git subprocess. Adds `--pull-timeout` / `PULL_TIMEOUT` (default 60s) to bound each pull subprocess. Before the first successful pull the probe returns 503 `no successful pull yet`; after a stale cache it returns 503 with the last pull error. Body never contains `context canceled`. Fixes false-NotReady during SSH timeouts (prod incident 2026-05-03, `vault-obsidian-openclaw-0`).
```

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass (`make test` green before and after)
- MUST NOT change the public HTTP contract: `/readiness` URL and status codes (200/503) per `docs/api.md:20` are frozen
- The `ReadinessStateReader` interface is defined in `pkg/handler/` — NOT in `pkg/puller/` — to avoid introducing a handler→puller import cycle and to keep the interface at its point of use
- `NewReadinessHandler` writes the 503 body directly to `resp` (plain text); it does NOT return an error through the libhttp error handler path — this prevents any `context canceled` from leaking through error wrapping
- `pkg/handler/readiness.go` must NOT import `pkg/git` after this change
- `pkg/factory/factory.go` still imports `pkg/git` (other factory functions use it) — do NOT remove that import
- `createHTTPServer` in `main.go` takes `*puller.PullStateCache` (concrete type) so the compiler can verify it satisfies `handler.ReadinessStateReader`
- Use `errors.Wrap`/`errors.Wrapf` from `github.com/bborbe/errors` in `pkg/` code — never `fmt.Errorf`, never bare `return err`
- `context.Background()` must NOT appear in `pkg/` — only in `main.go` and test files
- No new external dependencies
- The old `git.Status()` call in the readiness handler is REMOVED entirely — the new handler has no git dependency at all
</constraints>

<verification>
```bash
cd /workspace && make test
```
Must pass.

Spot-check readiness handler tests:
```bash
cd /workspace && go test ./pkg/handler/... -v -run "ReadinessHandler"
```
Expected: all It blocks pass (200/ok, 503/no-successful-pull-yet, 503/stale, no-context-canceled).

Confirm readiness no longer imports git (precise match — avoids false positives on transitive identifier matches):
```bash
grep -E '"github.com/bborbe/git-rest/pkg/git"' /workspace/pkg/handler/readiness.go
```
Expected: no output (import removed).

Final validation:
```bash
cd /workspace && make precommit
```
Must pass with exit code 0.
</verification>
