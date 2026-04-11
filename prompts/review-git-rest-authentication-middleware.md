---
status: draft
created: "2026-04-11T00:00:00Z"
---

<summary>
- Every HTTP endpoint (files read/write/delete, list, metrics, healthz, readiness) is completely unauthenticated
- Any network-reachable client can read, overwrite, or delete arbitrary files in the git repository
- A bearer token middleware protects all API endpoints with a single secret token passed via Authorization header
- The token is read from a CLI flag with no hardcoded default, so the server refuses to start without it
- The /healthz endpoint is exempted from authentication (needed for liveness probes)
</summary>

<objective>
Add a bearer token authentication middleware in `pkg/handler/` and wire it in `main.go` so that all endpoints except `/healthz` require a valid `Authorization: Bearer <token>` header. The token is provided via a `--api-token` CLI flag.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Files to read before making changes (read ALL first):
- `main.go`: CLI flag parsing, mux setup, server wiring
- `pkg/handler/files_get.go`: existing handler pattern to follow for the new middleware
- `pkg/factory/factory.go`: existing factory function pattern to follow
- `pkg/handler/handler_suite_test.go`: test suite setup for adding middleware tests
</context>

<requirements>
1. Create `pkg/handler/auth_middleware.go`:
   ```go
   // Copyright (c) 2026 Benjamin Borbe All rights reserved.
   // Use of this source code is governed by a BSD-style
   // license that can be found in the LICENSE file.

   package handler

   import (
       "net/http"
       "strings"
   )

   // NewAuthMiddleware returns a middleware that requires a valid Bearer token.
   // Requests without or with a wrong Authorization header receive 401 Unauthorized.
   func NewAuthMiddleware(token string, next http.Handler) http.Handler {
       return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
           authHeader := r.Header.Get("Authorization")
           if !strings.HasPrefix(authHeader, "Bearer ") ||
               strings.TrimPrefix(authHeader, "Bearer ") != token {
               w.Header().Set("Content-Type", "application/json")
               w.WriteHeader(http.StatusUnauthorized)
               _, _ = w.Write([]byte(`{"error":"unauthorized"}`))
               return
           }
           next.ServeHTTP(w, r)
       })
   }
   ```

2. Add a factory function in `pkg/factory/factory.go`:
   ```go
   // CreateAuthMiddleware wraps next with bearer token authentication.
   func CreateAuthMiddleware(token string, next http.Handler) http.Handler {
       return handler.NewAuthMiddleware(token, next)
   }
   ```

3. In `main.go`, add a new CLI flag after the existing flags:
   ```go
   apiToken := flag.String("api-token", "", "bearer token required for all API requests (required)")
   ```
   Add validation after `flag.Parse()`:
   ```go
   if *apiToken == "" {
       fmt.Fprintln(os.Stderr, "error: --api-token is required")
       flag.Usage()
       os.Exit(1)
   }
   ```

4. In `main.go`, wrap the API mux routes with the auth middleware. The simplest approach is to create a protected sub-mux:
   ```go
   apiMux := http.NewServeMux()
   apiMux.Handle("GET /api/v1/files/", factory.CreateFilesDispatchHandler(getH, listH))
   apiMux.Handle("POST /api/v1/files/", postH)
   apiMux.Handle("DELETE /api/v1/files/", deleteH)
   apiMux.Handle("/readiness", readinessH)
   apiMux.Handle("/metrics", promhttp.Handler())

   mux := http.NewServeMux()
   mux.Handle("/healthz", healthzH)  // no auth
   mux.Handle("/", factory.CreateAuthMiddleware(*apiToken, apiMux))
   ```

5. Add tests in `pkg/handler/auth_middleware_test.go` using Ginkgo/Gomega:
   - Request with correct Bearer token: passes through to next handler
   - Request with no Authorization header: returns 401 with JSON body `{"error":"unauthorized"}`
   - Request with wrong token: returns 401
   - Request with malformed header (not "Bearer "): returns 401

6. Update `pkg/handler/helpers_test.go` or handler test files to include the auth token in test requests for any integration-style tests that hit the full mux (if applicable). Individual handler unit tests do not go through the middleware so they are unaffected.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Do not hardcode any default token value — the flag must be required (empty default + validation)
- /healthz must remain unauthenticated
- Use `errors.Wrap`/`errors.Errorf` from `github.com/bborbe/errors` — never `fmt.Errorf`
- The constant-time comparison concern: for internal APIs where token length is not secret, strings.TrimPrefix + == is acceptable; if timing-safety is required add `subtle.ConstantTimeCompare`
</constraints>

<verification>
make precommit
</verification>
