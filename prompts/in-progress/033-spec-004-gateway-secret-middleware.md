---
status: committing
spec: [004-gateway-secret-auth]
summary: Implemented NewGatewaySecretMiddleware in pkg/handler/ and CreateGatewaySecretMiddleware in pkg/factory/, with 7 unit tests covering all error and success paths including header-strip verification.
container: git-rest-033-spec-004-gateway-secret-middleware
dark-factory-version: v0.143.0-5-g73d1db8
created: "2026-05-02T19:35:00Z"
queued: "2026-05-02T19:47:21Z"
started: "2026-05-02T19:49:52Z"
branch: dark-factory/gateway-secret-auth
---

<summary>
- A new HTTP middleware is available that enforces X-Gateway-Secret and X-Gateway-Initator header checks
- Requests missing X-Gateway-Initator return HTTP 500 with the exact body the spec requires
- Requests with missing, empty, or wrong X-Gateway-Secret return HTTP 401 with the exact body the spec requires
- When both headers are valid, the secret header is stripped from the request before forwarding to prevent downstream logging exposure
- The middleware is composable as a func(http.Handler) http.Handler, compatible with gorilla mux subrouter.Use()
- A zero-logic factory function wraps the middleware for use in the composition layer
- Seven unit tests cover all error and success paths, including header-strip verification
</summary>

<objective>
Implement `NewGatewaySecretMiddleware` in `pkg/handler/` and expose it via a factory function in `pkg/factory/`. The middleware enforces the header contract from spec 004: reject missing/empty `X-Gateway-Initator` with 500, reject wrong/missing/empty `X-Gateway-Secret` with 401, strip the secret header on success. No auth library dependencies — stdlib only.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read coding guides before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-http-handler-refactoring-guide.md` — handler location in pkg/handler/, factory naming conventions
- `go-testing-guide.md` — Ginkgo/Gomega test patterns, external test packages (package handler_test)
- `go-patterns.md` — public interface + private struct + New* constructor pattern

Files to read in full before implementing:
- `pkg/handler/metrics_middleware.go` — existing middleware in the same package; the pattern for `func(next http.Handler) http.Handler` used as wrapping middleware
- `pkg/handler/metrics_middleware_test.go` — test pattern to mirror: package handler_test, Ginkgo Describe, httptest.NewRecorder + httptest.NewRequest, mocks from mocks package
- `pkg/handler/handler_suite_test.go` — Ginkgo suite setup for the handler package; your test file joins this suite
- `pkg/factory/factory.go` — all Create* factory functions; append one new function after CreateMetricsMiddleware
</context>

<requirements>

## 1. Create `pkg/handler/gateway_secret_middleware.go`

Create a new file. Exact content:

```go
// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"net/http"
)

// HeaderGatewayInitator is the request-header name the auth middleware
// expects to carry caller identity. The misspelling (missing the second
// 'i' in "Initator") is deliberate — it matches the existing caller-side
// convention and is part of the frozen public contract. Do not "fix" it.
const HeaderGatewayInitator = "X-Gateway-Initator"

// HeaderGatewaySecret is the request-header name the auth middleware
// expects to carry the shared secret value.
const HeaderGatewaySecret = "X-Gateway-Secret"

// NewGatewaySecretMiddleware returns a gorilla mux-compatible middleware
// (func(http.Handler) http.Handler) that enforces shared-secret header auth.
//
// Check order (first failure wins):
//  1. X-Gateway-Initator missing or empty → 500 (initiator identity required)
//  2. X-Gateway-Secret missing, empty, or not equal to secret → 401
//  3. Both present and matching → strip X-Gateway-Secret, call next
//
// "Missing" and "empty string" are treated identically for both headers:
// r.Header.Get returning "" triggers the same response regardless of whether
// the client omitted the header or sent an empty value.
//
// X-Gateway-Secret is deleted from the request before the inner handler is
// called so it cannot appear in downstream request logs or metrics labels.
func NewGatewaySecretMiddleware(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(HeaderGatewayInitator) == "" {
				http.Error(w, "header 'X-Gateway-Initator' missing", http.StatusInternalServerError)
				return
			}
			if r.Header.Get(HeaderGatewaySecret) != secret {
				http.Error(w, "secret in header 'X-Gateway-Secret' is invalid => access denied", http.StatusUnauthorized)
				return
			}
			r.Header.Del(HeaderGatewaySecret)
			next.ServeHTTP(w, r)
		})
	}
}
```

The two `const` declarations centralize the header names so the deliberate `Initator` misspelling is referenced in exactly one place per name. Body strings remain literal (the spec freezes them, and `http.Error` already controls them).

Imports: `"net/http"` only.

## 2. Add factory function to `pkg/factory/factory.go`

Append after the existing `CreateMetricsMiddleware` function (before the closing of the file):

```go
// CreateGatewaySecretMiddleware returns a gorilla mux-compatible middleware that
// validates X-Gateway-Secret and X-Gateway-Initator request headers.
// Mount with: subrouter.Use(CreateGatewaySecretMiddleware(secret))
func CreateGatewaySecretMiddleware(secret string) func(http.Handler) http.Handler {
	return handler.NewGatewaySecretMiddleware(secret)
}
```

No new imports needed — `handler` is already imported.

## 3. Create `pkg/handler/gateway_secret_middleware_test.go`

Create in `package handler_test`. Mirror the structure of `metrics_middleware_test.go`.

Test file structure:

```go
// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/pkg/handler"
)

var _ = Describe("GatewaySecretMiddleware", func() {
	const secret = "test-secret-value"

	var (
		mw          func(http.Handler) http.Handler
		nextCalled  int
		nextH       http.Handler
		rec         *httptest.ResponseRecorder
	)

	BeforeEach(func() {
		nextCalled = 0
		nextH = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			nextCalled++
			w.WriteHeader(http.StatusOK)
		})
		mw = handler.NewGatewaySecretMiddleware(secret)
		rec = httptest.NewRecorder()
	})

	// --- 500: missing / empty X-Gateway-Initator ---

	Context("when X-Gateway-Initator header is absent", func() {
		It("returns 500 with exact body", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/foo.md", nil)
			req.Header.Set("X-Gateway-Secret", secret)
			mw(nextH).ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusInternalServerError))
			Expect(strings.TrimSpace(rec.Body.String())).To(Equal("header 'X-Gateway-Initator' missing"))
			Expect(nextCalled).To(Equal(0))
		})
	})

	Context("when X-Gateway-Initator header is present but empty", func() {
		It("returns 500 with exact body (empty treated as missing)", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/foo.md", nil)
			req.Header.Set("X-Gateway-Initator", "")
			req.Header.Set("X-Gateway-Secret", secret)
			mw(nextH).ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusInternalServerError))
			Expect(strings.TrimSpace(rec.Body.String())).To(Equal("header 'X-Gateway-Initator' missing"))
			Expect(nextCalled).To(Equal(0))
		})
	})

	// --- 401: bad / missing X-Gateway-Secret (with valid initiator) ---

	Context("when X-Gateway-Secret header is absent", func() {
		It("returns 401 with exact body", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/foo.md", nil)
			req.Header.Set("X-Gateway-Initator", "test-caller")
			mw(nextH).ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusUnauthorized))
			Expect(strings.TrimSpace(rec.Body.String())).To(Equal("secret in header 'X-Gateway-Secret' is invalid => access denied"))
			Expect(nextCalled).To(Equal(0))
		})
	})

	Context("when X-Gateway-Secret header contains wrong value", func() {
		It("returns 401 with exact body", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/foo.md", nil)
			req.Header.Set("X-Gateway-Initator", "test-caller")
			req.Header.Set("X-Gateway-Secret", "wrong-secret")
			mw(nextH).ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusUnauthorized))
			Expect(strings.TrimSpace(rec.Body.String())).To(Equal("secret in header 'X-Gateway-Secret' is invalid => access denied"))
			Expect(nextCalled).To(Equal(0))
		})
	})

	Context("when X-Gateway-Secret header is present but empty", func() {
		It("returns 401 with exact body (empty treated as missing)", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/foo.md", nil)
			req.Header.Set("X-Gateway-Initator", "test-caller")
			req.Header.Set("X-Gateway-Secret", "")
			mw(nextH).ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusUnauthorized))
			Expect(strings.TrimSpace(rec.Body.String())).To(Equal("secret in header 'X-Gateway-Secret' is invalid => access denied"))
			Expect(nextCalled).To(Equal(0))
		})
	})

	// --- 200: correct headers ---

	Context("when both headers are correct", func() {
		It("calls the inner handler and returns its status", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/foo.md", nil)
			req.Header.Set("X-Gateway-Initator", "test-caller")
			req.Header.Set("X-Gateway-Secret", secret)
			mw(nextH).ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(nextCalled).To(Equal(1))
		})

		It("strips X-Gateway-Secret from the request before forwarding", func() {
			var capturedSecret string
			capturingHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedSecret = r.Header.Get("X-Gateway-Secret")
				w.WriteHeader(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/foo.md", nil)
			req.Header.Set("X-Gateway-Initator", "test-caller")
			req.Header.Set("X-Gateway-Secret", secret)
			mw(capturingHandler).ServeHTTP(rec, req)
			Expect(capturedSecret).To(Equal(""))
		})
	})
})
```

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- No new external dependencies — `"net/http"` stdlib only for the middleware implementation
- Header name `X-Gateway-Initator` is intentionally spelt without a second 'i'. Do NOT correct it.
- Secret comparison is plain string equality (`!=`), not `hmac.Equal` — no constant-time compare (cluster-internal trust model)
- Empty string and missing header are treated identically: `r.Header.Get(...)` returning `""` triggers the same response whether the header was omitted or sent empty
- Error response body exact strings (checked with `strings.TrimSpace` to absorb the trailing newline `http.Error` appends):
  - 500 body: `header 'X-Gateway-Initator' missing`
  - 401 body: `secret in header 'X-Gateway-Secret' is invalid => access denied`
- `r.Header.Del("X-Gateway-Secret")` must run BEFORE `next.ServeHTTP(w, r)` — delete, then forward
- `context.Background()` must NOT appear in `pkg/` — tests use httptest.NewRequest which includes a context
- The factory function `CreateGatewaySecretMiddleware` is a pure wrapper — zero logic, zero conditionals
</constraints>

<verification>
`make precommit` — must pass.

Spot-check the handler tests specifically:
```bash
cd /workspace && go test ./pkg/handler/... -v -run "GatewaySecret"
```
Expected: all 7 It blocks pass.
</verification>
