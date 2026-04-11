---
status: draft
created: "2026-04-11T00:00:00Z"
---

<summary>
- filesDispatch and metricsMiddleware are defined directly in main.go
- Functions in the main package cannot be unit-tested from an external package
- Both functions are handler/middleware concerns that belong in pkg/handler/ per the project architecture
- Moving them to pkg/handler/ makes them independently testable and aligns with the stated architecture
- Factory wiring in main.go is simplified to calls into pkg/factory/
</summary>

<objective>
Extract `filesDispatch` and `metricsMiddleware` from `main.go` into `pkg/handler/`, add corresponding factory functions in `pkg/factory/`, and add unit tests for both. `main.go` should call the factory functions instead of the inline definitions.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Files to read before making changes (read ALL first):
- `main.go` (~line 97-118): filesDispatch and metricsMiddleware definitions
- `pkg/handler/files_get.go`: existing handler structure to follow
- `pkg/factory/factory.go`: existing factory function structure to follow
- `pkg/handler/handler_suite_test.go`: test suite setup to understand how to add tests
- `pkg/metrics/metrics.go`: HTTPRequestsTotal metric used by metricsMiddleware
</context>

<requirements>
1. Create `pkg/handler/files_dispatch.go`:
   ```go
   // Copyright (c) 2026 Benjamin Borbe All rights reserved.
   // Use of this source code is governed by a BSD-style
   // license that can be found in the LICENSE file.

   package handler

   import "net/http"

   // NewFilesDispatchHandler routes GET /api/v1/files/ to listH when the glob query
   // parameter is present, and to getH otherwise.
   func NewFilesDispatchHandler(getH, listH http.Handler) http.Handler {
       return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
           if r.URL.Query().Has("glob") {
               listH.ServeHTTP(w, r)
               return
           }
           getH.ServeHTTP(w, r)
       })
   }
   ```

2. Create `pkg/handler/metrics_middleware.go`:
   ```go
   // Copyright (c) 2026 Benjamin Borbe All rights reserved.
   // Use of this source code is governed by a BSD-style
   // license that can be found in the LICENSE file.

   package handler

   import (
       "net/http"
       "strconv"

       "github.com/felixge/httpsnoop"

       "github.com/bborbe/git-rest/pkg/metrics"
   )

   // NewMetricsMiddleware wraps next with Prometheus HTTP request counting.
   func NewMetricsMiddleware(next http.Handler) http.Handler {
       return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
           m := httpsnoop.CaptureMetrics(next, w, r)
           metrics.HTTPRequestsTotal.WithLabelValues(
               r.Method,
               r.URL.Path,
               strconv.Itoa(m.Code),
           ).Inc()
       })
   }
   ```

3. Add factory functions to `pkg/factory/factory.go`:
   ```go
   // CreateFilesDispatchHandler returns a handler that routes between get and list.
   func CreateFilesDispatchHandler(getH, listH http.Handler) http.Handler {
       return handler.NewFilesDispatchHandler(getH, listH)
   }

   // CreateMetricsMiddleware wraps next with Prometheus HTTP request counting.
   func CreateMetricsMiddleware(next http.Handler) http.Handler {
       return handler.NewMetricsMiddleware(next)
   }
   ```

4. Update `main.go`:
   - Remove the `filesDispatch` function definition (~line 97-106)
   - Remove the `metricsMiddleware` function definition (~line 108-118)
   - Replace the inline call `filesDispatch(getH, listH)` with `factory.CreateFilesDispatchHandler(getH, listH)`
   - Replace `metricsMiddleware(mux)` with `factory.CreateMetricsMiddleware(mux)`
   - Remove any imports that become unused (e.g., `"strconv"`, `"github.com/felixge/httpsnoop"`, `"github.com/bborbe/git-rest/pkg/metrics"`)

5. Add tests in `pkg/handler/files_dispatch_test.go` (using Ginkgo, follow the pattern from existing handler test files):
   - Request with `?glob=*.txt` routes to listH (verify via a spy handler that records calls)
   - Request without `glob` parameter routes to getH

6. Add tests in `pkg/handler/metrics_middleware_test.go`:
   - A successful request increments the counter (check that ServeHTTP is called on next and the response passes through)
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Follow the Ginkgo/Gomega test pattern used in existing handler test files
- Factory functions contain zero business logic — pure constructor calls
- Use `errors.Wrap`/`errors.Errorf` from `github.com/bborbe/errors` for any new error handling
</constraints>

<verification>
make precommit
</verification>
