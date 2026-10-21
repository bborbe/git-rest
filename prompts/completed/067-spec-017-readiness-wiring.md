---
status: completed
spec: [017-bug-readiness-decoupled-from-sync-outcome]
summary: Wired /readiness to the repo-path serving verdict, deleted PullStateCache/PullStateWriter and its mock, and added integration coverage for 200-while-failing and 503-when-unreadable.
execution_id: git-rest-readiness-decouple-exec-067-spec-017-readiness-wiring
dark-factory-version: v0.198.7
created: "2026-10-21T15:45:28Z"
queued: "2026-10-21T15:52:46Z"
started: "2026-10-21T15:54:38Z"
completed: "2026-10-21T15:56:12Z"
branch: dark-factory/bug-readiness-decoupled-from-sync-outcome
---

# Wire `/readiness` to the serving verdict and remove the pull-outcome cache

<summary>
- `/readiness` now answers 200 whenever the served repository path is readable, regardless of the last sync outcome.
- A missing repository path answers 503 with body `repo path unreadable`.
- A rejected push, a failed fetch or a merge conflict leaves `/readiness` at 200 and leaves reads working.
- The puller no longer records pull outcomes for readiness; it keeps logging `git pull failed` on every failed attempt.
- The pull-outcome cache and its writer interface are deleted, along with their generated mock and unit specs.
- The handler, factory, `main.go`, `export_test.go` and integration tests all use the repo path instead of the cache.
- Integration coverage proves 200 while the puller keeps failing and 503 once the repository path is removed.
- No metric, alert, log line, HTTP route or write-path behaviour changes.
</summary>

<objective>
Replace the pull-outcome readiness dependency with the repo-path serving verdict added by prompt 1. The puller keeps pulling and keeps logging failures, but nothing about readiness reads its outcome. Delete `PullStateCache`/`PullStateWriter` and their mock, update every call site together, and add integration coverage for the 200-while-pushing and 503-while-unreadable cases.
</objective>

<context>
There is no repo-root `CLAUDE.md` in this worktree — do not look for one. The project conventions are restated in `<constraints>` below.

Precondition — prompt 1 must have shipped. Verify before starting:

```bash
grep -n 'func NewRepoReadiness' pkg/handler/repo_readiness.go
```

If missing, stop and report status: failed with message "NewRepoReadiness not found — run 1-spec-017-serving-verdict first".

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` — interface at point of use, constructor returns the interface
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md` — `Create*` prefix, factory has zero business logic
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-handler-refactoring-guide.md` — handlers in `pkg/handler/`, factories in `pkg/factory/`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, counterfeiter mocks, external test packages
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` — do not add a raw `go func()` to production code; the pull loop is unchanged
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors`; never `fmt.Errorf`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — linter limits and the precommit gate

Files to read in full before editing (anchor by symbol name, not line number):

- `pkg/handler/repo_readiness.go` — the verdict added by prompt 1; do not change its behaviour
- `pkg/handler/readiness.go` — the `ReadinessStateReader` interface and `NewReadinessHandler`; the handler body stays, its doc comments change
- `pkg/handler/readiness_test.go` — handler tests; rewrite the not-ready contexts
- `pkg/factory/factory.go` — `CreateReadinessHandler`
- `pkg/puller/puller.go` — `New`, the `puller` struct and `runOnce`
- `pkg/puller/puller_test.go` — every `puller.New` call site and the pull-state assertions
- `pkg/puller/pull_state.go` — the cache and `PullStateWriter`; delete this file
- `pkg/puller/pull_state_test.go` — delete this file
- `main.go` — `Run`, `createGitRefresher`, `createHTTPServer`, and the `recoverUntracked` doc comment
- `export_test.go` — `CreateServerHandler`
- `main_test.go` — the `AdminRoutes` describe and its `CreateServerHandler` call

**Verified facts — do not re-derive, do not contradict:**

- Current `puller.New` signature is `func New(g git.Git, interval libtime.Duration, pullTimeout time.Duration, state PullStateWriter) Puller`. The `state` field is used only by `runOnce` (`p.state.RecordPull(err)`).
- `pkg/puller/puller.go` already logs `slog.WarnContext(ctx, "git pull failed", "error", err)` when `Pull` returns an error. That line is the sync signal the spec requires to stay intact; keep it exactly.
- The error metric `git_rest_git_operation_errors_total{operation="push"|"pull"|"fetch"|"rebase"}` is incremented inside `pkg/git` and is untouched by this prompt.
- `mocks/` is wiped and regenerated by `make generate` from the `//counterfeiter:generate` directives. Deleting `pkg/puller/pull_state.go` removes its directive, so `mocks/pull_state_writer.go` disappears on the next generate. Never hand-edit `mocks/`.
- `mocks/git.go` exposes `FakeGit` with `PullStub` and `PullCallCount()`; `mocks/readiness_state_reader.go` exposes `FakeReadinessStateReader` and is unchanged because the interface is unchanged.
- `main_test.go` already imports `mocks`, `factory`, `git`, `puller` and `libtime`. `pkg/factory/factory.go` currently has `CreateReadinessHandler(s handler.ReadinessStateReader) http.Handler`.
- `main.go`'s `recoverUntracked` doc comment says a recovery failure "just means readiness will fall back to the existing 503 wait until manual intervention". That sentence is false after this change and must be corrected in the same edit.
- The `ReadinessStateReader` interface signature (`ReadinessStatus() (bool, string)`) does NOT change. Only its implementation, its doc comment and the call sites change.

</context>

<requirements>

## 1. Update the handler doc comments in `pkg/handler/readiness.go`

Change only comments; keep the interface and handler bodies byte-identical apart from the text below.

- Interface comment becomes:
  ```go
  // ReadinessStateReader is the interface the readiness handler reads from.
  // Implemented by the repo-path verdict returned by NewRepoReadiness.
  ```
- `NewReadinessHandler` comment becomes:
  ```go
  // NewReadinessHandler returns a handler that reports readiness from the given
  // serving-state reader. It never invokes a git subprocess and never blocks on
  // a mutex.
  ```
- Keep the 200 body `ok`, the 503 status code and the verbatim reason body unchanged.

## 2. Update the readiness factory in `pkg/factory/factory.go`

Replace the parameter with the repo path and compose the verdict inside the factory:

```go
// CreateReadinessHandler returns an http.Handler for GET /readiness.
func CreateReadinessHandler(repoPath string) http.Handler {
	return libhttp.NewJSONErrorHandler(
		handler.NewReadinessHandler(handler.NewRepoReadiness(repoPath)),
	)
}
```

The factory stays pure composition — no conditionals, no error handling, no stat call of its own.

## 3. Remove pull state from the puller

In `pkg/puller/puller.go`:

- Change `New` to:
  ```go
  // New returns a Puller that calls g.Pull on the given interval.
  // pullTimeout bounds each individual pull subprocess; 0 means no timeout.
  func New(
      g git.Git,
      interval libtime.Duration,
      pullTimeout time.Duration,
  ) Puller {
      return &puller{
          git:         g,
          interval:    interval,
          pullTimeout: pullTimeout,
      }
  }
  ```
- Remove the `state PullStateWriter` field from the `puller` struct and its assignment.
- In `runOnce`, replace `err := p.git.Pull(pullCtx); p.state.RecordPull(err); if err != nil { ... }` with:
  ```go
  if err := p.git.Pull(pullCtx); err != nil {
      slog.WarnContext(ctx, "git pull failed", "error", err)
  }
  ```
- Correct `runOnce`'s doc comment, which is false once `RecordPull` is gone: change `// runOnce executes one pull attempt, bounded by pullTimeout, and records the outcome.` to `// runOnce executes one pull attempt, bounded by pullTimeout.`
- Delete `pkg/puller/pull_state.go` and `pkg/puller/pull_state_test.go`.
- Do NOT delete or alter `pkg/git/git.go`, its metric calls, or the `git pull failed` log line.

Then run `make generate` so `mocks/pull_state_writer.go` is removed. Confirm the file no longer exists.

## 4. Rewrite `pkg/puller/puller_test.go`

- Remove the `fakePullState` variable and its `BeforeEach` initialisation. Keep the `gitmocks` alias — it is still used for `FakeGit`.
- Update every `puller.New(...)` call to the three-argument form `puller.New(fakeGit, libtime.Duration(10*time.Millisecond), 0)`.
- Delete the specs that assert `RecordPull`:
  - `It("records success when Pull returns nil")`
  - `It("records failure when Pull returns an error")`
  - `It("wires RecordPull into PullStateCache correctly")`
- Keep `It("calls Pull on each tick and stops when context is cancelled")`, `It("continues running after a Pull error")` and `It("returns ctx.Err() when context is cancelled")`.
- Rewrite `It("timeout aborts pull and records non-nil error")` as `It("bounds each pull with pullTimeout")`:
  ```go
  It("bounds each pull with pullTimeout", func() {
      var observedDeadline bool
      fakeGit.PullStub = func(ctx context.Context) error {
          _, observedDeadline = ctx.Deadline()
          <-ctx.Done()
          return ctx.Err()
      }
      p = puller.New(fakeGit, libtime.Duration(10*time.Millisecond), 10*time.Millisecond)

      ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
      defer cancel()
      _ = p.Run(ctx)

      Expect(fakeGit.PullCallCount()).To(BeNumerically(">=", 1))
      Expect(observedDeadline).To(BeTrue())
  })
  ```
- Keep the `errors` import: it is still used by `continues running after a Pull error`.

## 5. Rewrite `pkg/handler/readiness_test.go`

Keep the `FakeReadinessStateReader` harness and the ready-path context. Replace the two not-ready contexts with a single context for the new contract:

- Delete `Context("when ReadinessStatus returns not ready: no successful pull yet")` entirely.
- Delete `Context("when ReadinessStatus returns not ready: stale cache")` entirely.
- Add:
  ```go
  Context("when ReadinessStatus returns not ready: repo path unreadable", func() {
      BeforeEach(func() {
          fakeState.ReadinessStatusReturns(false, "repo path unreadable")
      })

      It("returns 503", func() {
          req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
          err := h.ServeHTTP(ctx, rec, req)
          Expect(err).To(BeNil())
          Expect(rec.Code).To(Equal(http.StatusServiceUnavailable))
      })

      It("returns the reason verbatim", func() {
          req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
          _ = h.ServeHTTP(ctx, rec, req)
          Expect(rec.Body.String()).To(Equal("repo path unreadable"))
      })

      It("body contains no sync word or host path", func() {
          req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
          _ = h.ServeHTTP(ctx, rec, req)
          Expect(rec.Body.String()).NotTo(MatchRegexp(`(?i)push|pull|sync|conflict|/tmp/`))
      })
  })
  ```
- Keep the ready context's `returns 200` and `returns body 'ok'` specs unchanged.

## 6. Wire `main.go` and `export_test.go`

In `main.go` `Run`:

- Delete the `pullState := puller.NewPullStateCache(...)` block.
- Change the `service.Run` arguments to:
  ```go
  return service.Run(ctx,
      a.createGitRefresher(gitClient),
      a.createHTTPServer(gitClient, metrics.NewMetrics(), a.Repo),
      a.createMemoryReporter(),
  )
  ```

In `main.go`:

- `createGitRefresher` becomes:
  ```go
  func (a *application) createGitRefresher(gitClient git.Git) run.Func {
      return func(ctx context.Context) error {
          return puller.New(gitClient, a.PullInterval, time.Duration(a.PullTimeout)).Run(ctx)
      }
  }
  ```
- `createHTTPServer` third parameter becomes `repoPath string`, and the readiness line becomes:
  ```go
  readinessH := factory.CreateReadinessHandler(repoPath)
  ```
- In the `recoverUntracked` doc comment, replace this exact three-line block (it continues past the quoted sentence — a partial replace leaves a dangling clause no check would catch):
  ```
  // Best-effort: errors are logged and do NOT abort startup. A failure here
  // just means readiness will fall back to the existing 503 wait until manual
  // intervention; that's no worse than today.
  ```
  with:
  `A failure here just means the untracked files stay uncommitted; readiness is unaffected because it tracks whether the served repository path is readable.`
- Keep the `puller` import — `createGitRefresher` still uses it. Keep `libtime` (argument struct) and `time` (timeouts).

In `export_test.go`:

- Change `CreateServerHandler` to:
  ```go
  func CreateServerHandler(
      gitClient git.Git,
      m metrics.Metrics,
      repoPath string,
  ) http.Handler {
      router := gorillamux.NewRouter().SkipClean(true)

      // Probe routes — always unauthenticated (kubelet + Prometheus have no secret).
      router.Handle("/healthz", factory.CreateHealthzHandler())
      router.Handle("/readiness", factory.CreateReadinessHandler(repoPath))
      router.Handle("/gc", libhttp.NewGarbageCollectorHandler())
      router.Handle(
          "/setloglevel/{level}",
          liblog.NewSetLoglevelHandler(
              context.Background(),
              liblog.NewLogLevelSetter(2, 5*time.Minute),
          ),
      )

      return factory.CreateMetricsMiddleware(m, router)
  }
  ```
- Remove the `puller` import.

## 7. Update `main_test.go`

In the `AdminRoutes` `BeforeEach`:

- Delete the `pullState := puller.NewPullStateCache(...)` block.
- Change the call to `handler = main.CreateServerHandler(gitClient, &mocks.FakeMetrics{}, repoDir)`.

Add a new top-level `var _ = Describe("Readiness integration", func() { ... })` in `main_test.go` (the suite already exists). Declare `var repoDir string`, `var handler http.Handler` and `var rec *httptest.ResponseRecorder` at the Describe level, then assign them in `BeforeEach` (use `=`, not `:=`, so the `It` bodies see them):

```go
BeforeEach(func() {
    repoDir = GinkgoT().TempDir()
    rec = httptest.NewRecorder()
    gitClient := factory.CreateGitClient(
        repoDir,
        &mocks.FakeMetrics{},
        libtime.NewCurrentDateTime(),
        "",
        git.NewMarkerResolver(repoDir),
    )
    handler = main.CreateServerHandler(gitClient, &mocks.FakeMetrics{}, repoDir)
})
```

Specs:

1. **Readable path → 200 `ok`** — `GET /readiness` returns `http.StatusOK` and body `ok`.
2. **Cold start → 200 before any pull** — the handler answers 200 on the same readable path with no puller ever started. This is the integration-level counterpart to the cold-start 503 behaviour deleted with `pkg/puller/pull_state_test.go`.
3. **Missing path → 503 `repo path unreadable`** — `Expect(os.RemoveAll(repoDir)).To(Succeed())`, then `GET /readiness` returns `http.StatusServiceUnavailable`, body exactly `repo path unreadable`, and `Expect(rec.Body.String()).NotTo(MatchRegexp(`(?i)push|pull|sync|conflict|/tmp/`))`.
4. **200 while the puller records a push rejection (AC4(a))**:
   ```go
   fakeGit := &mocks.FakeGit{}
   fakeGit.PullStub = func(ctx context.Context) error {
       return errors.New("push failed")
   }
   p := puller.New(fakeGit, libtime.Duration(10*time.Millisecond), 0)
   ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
   defer cancel()
   _ = p.Run(ctx)
   Expect(fakeGit.PullCallCount()).To(BeNumerically(">=", 1))

   req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
   handler.ServeHTTP(rec, req)
   Expect(rec.Code).To(Equal(http.StatusOK))
   Expect(rec.Body.String()).To(Equal("ok"))
   ```
   Run the puller synchronously with a short timeout; do NOT add a raw `go func()`.
5. **200 while the puller returns a `RebaseConflictError` (AC4(c))** — same shape, with `fakeGit.PullStub = func(ctx context.Context) error { return &git.RebaseConflictError{Path: "tasks/foo.md"} }`. Assert 200 and body `ok`. This is the spec that a hybrid implementation keeping the conflict branch would fail.

Add the standard-library `errors` import (unqualified `"errors"`, as `pkg/puller/puller_test.go` does) to `main_test.go` — not `github.com/bborbe/errors`, whose `New` takes a context. Keep the existing `puller` and `libtime` imports; both remain used.

## 8. Self-check before finishing

Run every command in `<verification>` from the repo root. Confirm:

- `/readiness` is 200 on a readable path and 503 with `repo path unreadable` on a missing one.
- The puller keeps calling `Pull` and logging `git pull failed`; it no longer records state.
- No `.go` file references `PullStateCache`, `PullStateWriter`, `NewPullStateCache` or `RecordPull`.
- `mocks/pull_state_writer.go` is gone after `make generate`.
- No metric, alert, route, status-code contract or write-path behaviour changed.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT change `pkg/handler/repo_readiness.go` behaviour — the verdict is fixed by prompt 1.
- Do NOT change the `ReadinessStateReader` interface signature; only its doc comment changes.
- Do NOT change `NewReadinessHandler`'s 200/503 behaviour, its `ok` body or its verbatim-reason 503 body.
- Do NOT change `pkg/git/git.go`, its `git_rest_git_operation_errors_total` increments, or any git operation semantics. A failed push still returns an error to the API caller.
- Do NOT change the `git pull failed` log line or its level.
- Do NOT add a metric, gauge, alert, HTTP route, environment variable, CLI flag or configuration field.
- Do NOT keep `PullStateCache` or `PullStateWriter` for a future endpoint — the spec forbids retaining dead state. Delete both.
- Do NOT hand-edit anything under `mocks/`; run `make generate`.
- Do NOT add a scenario file under `scenarios/`.
- Do NOT edit `specs/`.
- Do NOT run `go mod vendor`, and do NOT write `-mod=vendor` in any command — this repo does not commit `vendor/`.
- Errors via `github.com/bborbe/errors` — no `fmt.Errorf`, no bare `return err` in new code.
- Interface → Constructor → Struct → Method; factories stay pure composition.
- New and modified code needs ≥80% statement coverage; every deleted spec is replaced by one of the specs above.
- Tests use Ginkgo v2 / Gomega; `make precommit` is the gate.
- Files must carry the license header or `addlicense` fails `make precommit`.
</constraints>

<verification>
Run from the repo root. Do not put git commands in this block.

Focused runs while implementing:

```bash
go test ./pkg/puller/... -mod=mod -ginkgo.focus='Puller'
go test ./pkg/handler/... -mod=mod -ginkgo.focus='ReadinessHandler|RepoReadiness'
go test . -mod=mod -ginkgo.focus='Readiness integration|AdminRoutes'
```

Each must exit 0. A `0 of 0` result means the focus string matched nothing — fix the focus, do not accept it.

The cache and its mock are gone (each `! grep -q` / `! test -e` must exit 0):

```bash
! test -e pkg/puller/pull_state.go
! test -e pkg/puller/pull_state_test.go
! test -e mocks/pull_state_writer.go
! grep -rq 'PullStateCache\|PullStateWriter\|NewPullStateCache\|RecordPull' --include='*.go' .
```

The new wiring is present (each `grep -n` must print at least one match):

```bash
grep -n 'func CreateReadinessHandler(repoPath string)' pkg/factory/factory.go
grep -n 'handler.NewRepoReadiness(repoPath)' pkg/factory/factory.go
grep -n 'factory.CreateReadinessHandler(repoPath)' main.go export_test.go
grep -n 'func (a \*application) createGitRefresher(gitClient git.Git) run.Func' main.go
grep -n 'puller.New(gitClient, a.PullInterval, time.Duration(a.PullTimeout))' main.go
grep -n 'slog.WarnContext(ctx, "git pull failed"' pkg/puller/puller.go
grep -n 'repo path unreadable' pkg/handler/readiness_test.go main_test.go
grep -n 'RebaseConflictError{Path: "tasks/foo.md"}' main_test.go
```

The puller no longer takes a state argument (each `! grep -q` must exit 0):

```bash
! grep -q 'state PullStateWriter' pkg/puller/puller.go
! grep -q 'p.state' pkg/puller/puller.go
```

Final gate:

```bash
ROOTDIR=/workspace make precommit
```

Must exit 0. `make precommit` runs `generate` before `test`, so a stale `mocks/pull_state_writer.go` would fail the build — regenerate rather than hand-delete the mock.
</verification>
