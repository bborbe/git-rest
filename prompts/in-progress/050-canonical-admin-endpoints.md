---
status: approved
created: "2026-08-09T19:45:00Z"
queued: "2026-08-09T19:58:57Z"
---

<summary>
- The service exposes health, readiness and metrics probes but not the two other standard admin routes
- Operators cannot change log level or trigger a garbage collection without a restart
- Both handlers already exist in libraries the project depends on
- One of them needs a helper constructed with a sensible default, which this prompt pins down
- A test drives the real server so a missing route cannot pass unnoticed
</summary>

<objective>
The service exposes the full set of canonical admin endpoints, and a test exercises them through the real server construction path.
</objective>

<context>
Read `CLAUDE.md` for project conventions if present (this repo has none).

Files to read before making changes (read ALL first):
- `main.go` — find `createHTTPServer` and the block registering the probe routes. It registers three routes on `router` under a comment noting they are always unauthenticated because kubelet and Prometheus have no secret. The two new routes belong in that same block, for the same reason.
- `main_test.go` — note that the existing router test builds its **own** `mux.Router` mirroring the topology instead of calling `createHTTPServer`. That is why it cannot catch a route missing from production, and why requirement 5 exists.
- `helm/values.yaml` — `logLevel: "2"` is this service's deployed log level, wired through `helm/templates/statefulset.yaml` as `-v={{ .Values.logLevel }}`. That is where the default below comes from.

`main.go` already imports `libhttp "github.com/bborbe/http"`. It does **not** yet import `github.com/bborbe/log`.

The exact constructor calls are given in requirements 1 and 2 — use them verbatim rather than looking them up. Do not read anything outside this repository; the dependency sources are not available in this environment.
</context>

<requirements>
1. Register the garbage-collector route in the probe-route block:
   ```go
   router.Handle("/gc", libhttp.NewGarbageCollectorHandler())
   ```
2. Register the set-loglevel route in the same block:
   ```go
   router.Handle("/setloglevel/{level}", liblog.NewSetLoglevelHandler(ctx, liblog.NewLogLevelSetter(2, 5*time.Minute)))
   ```
   The `2` is the deployed baseline from `helm/values.yaml`, and the 5-minute auto-reset means a manually raised level reverts on its own. **Do not use `0`** — it masks INFO and above once the level reverts.
3. Add the import `liblog "github.com/bborbe/log"`, matching the existing alias style in the file (`libhttp`, `libtime`, …). Keep the import grouping as it is.
4. If `createHTTPServer` has no `ctx` in scope at that point, thread the one already available in its caller rather than creating a new one — do not use `context.Background()`.
5. Add a test that drives the **real** server path, not a mirrored router: assert that `GET /gc` and `GET /setloglevel/2` both return 200 when served by the router `createHTTPServer` actually builds. Follow the Ginkgo/Gomega style already used in `main_test.go`.

   `createHTTPServer` is an unexported method on `*application` and `main_test.go` is `package main_test`, so it is not reachable directly. This repo's established convention for that is an export hook — see `export_test.go`, which already exposes `CleanupStaleLocks`, `RecoverUntracked`, `SyncOnStartup`, and `ResolveGitSSHCommand` the same way. Add one more in that file following the existing style, then use it from the test with `httptest`.

   Do **not** work around this by building another mirrored router in the test — that is the exact antipattern this requirement exists to remove.
6. Do NOT change the listen address or port, any existing route, the API router, the gateway-secret middleware, or the server options. Everything here is additive.
7. Add a bullet under `## Unreleased` in `CHANGELOG.md` using a conventional prefix. If no `## Unreleased` section exists, create it directly above the newest released section without touching that section.
</requirements>

<constraints>
- Only change `main.go`, `main_test.go`, `export_test.go`, and `CHANGELOG.md`
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Use `errors.Wrap`/`errors.Errorf` from `github.com/bborbe/errors` if error handling is needed — never `fmt.Errorf` or a bare `return err`
- Do not read or reference paths outside this repository
</constraints>

<verification>
make precommit
</verification>
