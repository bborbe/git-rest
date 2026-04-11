---
status: draft
created: "2026-04-11T00:00:00Z"
---

<summary>
- main.go launches the puller and manages graceful shutdown via two raw go func() goroutines
- Raw goroutines are not tracked, errors are not propagated, and panics crash the process without controlled teardown
- The project coding standards require run.CancelOnFirstErrorWait from github.com/bborbe/run
- Migrating to run.CancelOnFirstErrorWait gives structured concurrency: all goroutines are tracked and any failure cancels the context
- The server ListenAndServe call should also move into the run group so an unexpected server exit triggers shutdown
</summary>

<objective>
Replace the two raw `go func()` goroutines in `main.go` with `run.CancelOnFirstErrorWait` from `github.com/bborbe/run`, including the HTTP server itself, so all three concurrent operations (puller, HTTP server, shutdown handler) are managed uniformly.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Files to read before making changes (read ALL first):
- `main.go` (~line 74-94): the two raw goroutines and the ListenAndServe call
- `go.mod`: check if github.com/bborbe/run is already a dependency
- `vendor/` directory: check if github.com/bborbe/run/run.go exists
</context>

<requirements>
1. Check if `github.com/bborbe/run` is already in `go.mod`. If not, add it:
   ```bash
   go get github.com/bborbe/run
   go mod tidy
   go mod vendor
   ```

2. In `main.go`, replace the two `go func()` blocks and the trailing `server.ListenAndServe` call (~line 74-94) with:
   ```go
   if err := run.CancelOnFirstErrorWait(ctx,
       func(ctx context.Context) error {
           return p.Run(ctx)
       },
       func(ctx context.Context) error {
           <-ctx.Done()
           shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
           defer cancel()
           return server.Shutdown(shutdownCtx)
       },
       func(ctx context.Context) error {
           slog.Info("starting git-rest server", "addr", *addr, "repo", *repo)
           if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
               return errors.Wrap(ctx, err, "server listen and serve")
           }
           return nil
       },
   ); err != nil {
       slog.ErrorContext(ctx, "service exited with error", "error", err)
       os.Exit(1)
   }
   ```

3. Remove the old `slog.Info("starting git-rest server", ...)` and `fmt.Fprintf(os.Stderr, "server error: ...")` lines that existed before the refactor, since they are now inside the run group.

4. Add `"github.com/bborbe/run"` and `"github.com/bborbe/errors"` to the import block if not already present. Remove any imports that become unused (e.g., `"fmt"` if the only usage was the server error fprintf).

5. The `context.WithTimeout(context.Background(), ...)` inside the shutdown function is acceptable — this is the only use of `context.Background()` in `main.go` for the shutdown deadline, and `main.go` is the only allowed location for `context.Background()` per project conventions.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Use `errors.Wrap` from `github.com/bborbe/errors` — never `fmt.Errorf`
- context.Background() is only allowed in main.go — this usage is valid
- The puller error is non-fatal if ctx is already cancelled (this is handled by run.CancelOnFirstErrorWait's context propagation)
</constraints>

<verification>
make precommit
</verification>
