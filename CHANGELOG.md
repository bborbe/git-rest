# Changelog

All notable changes to this project will be documented in this file.

Please choose versions by [Semantic Versioning](http://semver.org/).

* MAJOR version when you make incompatible API changes,
* MINOR version when you add functionality in a backwards-compatible manner, and
* PATCH version when you make backwards-compatible bug fixes.

## v0.24.3

- chore: update Go to 1.27.0 and github.com/bborbe/errors to v1.5.21, github.com/bborbe/http to v1.26.24, github.com/bborbe/log to v1.6.25, github.com/bborbe/run to v1.9.37, github.com/bborbe/sentry to v1.9.27, github.com/bborbe/service to v1.10.9, github.com/bborbe/time to v1.27.10

## v0.24.2

- chore: Bump golangci-lint to v2.13.1 and errcheck to v1.20.0, and move `gofmt -w` to run after golines in the `format` target so its wrapping is normalized before the gofmt lint check (Go 1.27 toolchain compatibility)

## v0.24.1

- update Go to 1.26.6 and update dependencies (GO-2026-6179, GO-2026-6180)

## v0.24.0

- feat: Add `/gc` (garbage collector) and `/setloglevel/{level}` admin endpoints to the probe route block, replacing the previous ad-hoc `curl` invocations for log level changes and manual GC triggering
- fix: Add `ctx.Done()` cancellation guard to `yamlMergeResolver.Resolve` and `markerResolver.Resolve` loops so both abort promptly when the caller cancels; the YAML resolver now also wraps failures with the failing path name

## v0.23.3

- chore: delete `tools.go` and remove tool-dependency pollution from `go.mod` — tool CLIs (golangci-lint, osv-scanner, counterfeiter, etc.) are no longer declared as module dependencies; versions stay pinned via `tools.env` and `go run pkg@$(VERSION)`; `go.mod` shrunk from 462 to 50 lines; five `replace` workarounds removed; `go-git` removed entirely

## v0.23.2

- fix(deps): bump go-git v5.19.1 → v5.19.2 (GHSA-hc8v-wwc9-vgxm CVSS 7.1, GHSA-qgq7-7hm3-q39j CVSS 6.3) and grpc v1.80.0 → v1.83.0 (GHSA-hrxh-6v49-42gf), plus the recurring bborbe/x/containerd/otel set — master precommit was red on osv-scanner and trivy

## v0.23.1

- fix(deps): bump x/text v0.39.0 (CVE-2026-56852) + Go 1.26.5 (GO-2026-5856); suppress unreachable/unfixable transitive CVEs (containerd, x/crypto/openpgp)

## v0.23.0

- chore: ignore 3 unfixable containerd indirect-dep CVEs (GO-2026-5064/5338/5622) in .osv-scanner.toml — no upstream fix, govulncheck-clean (unreachable); unblocks CI (master was red on the newly-published advisories)
- feat: add standalone Helm chart in helm/ — one git-rest instance per `vaults` entry (StatefulSet + Service, optional Secret + monitoring Alert CRs); `existingSecret` or inline secret; `make helm-publish` pushes it to OCI. Lets quant (and third parties) deploy `vault-obsidian-<name>` instances via Helm instead of raw manifests.

## v0.22.0

- feat: Add `git_rest_quarantined_files_total` unlabeled Prometheus counter for per-file quarantine events. The counter is registered at process start and pre-initialised to zero, so the time series is visible on `/metrics` before any quarantine event has occurred. Operators can `rate(git_rest_quarantined_files_total[5m])` to spot a sudden uptick of corrupt files in a served repo.
- feat: Add `quarantine_io_failed` label value to the existing `git_rest_resolver_failures_total{category}` counter (alongside the pre-existing `unsafe_path`). The bucket covers any I/O failure in the quarantine flow (read source, git rm source, mkdir destination, write destination, git add destination) — quarantine does not use `git mv` (git refuses to move conflicted files), so the label reflects what it actually counts.
- feat: Per-file quarantine in `resolveConflictMerge` — when the conflict resolver fails on a single file, the puller moves the file to `_conflicts/<path>.<unix-ts>.md` (or `.<ts>.quarantined` for non-`.md` files) and continues the merge, instead of aborting and wedging the pod. The merge commit message uses the fixed format `merge: resolved=[<sorted-paths>] quarantined=[<sorted-paths>]`. The pod only wedges if every conflicted path fails BOTH resolve and quarantine, or if `_conflicts/` already exists as a regular file in the repo. WARN log line names each quarantined path with the resolver error and the destination. Fixes the 2026-06-02 vault-obsidian-openclaw-0 incident (3.5h pod-wedge from a single corrupt-frontmatter file).

## v0.21.0

- feat: Add `YAMLMergeResolver` for conflict resolution on markdown files with YAML frontmatter. Deep-merges frontmatter keys (theirs wins on overlap), concatenates bodies, and stages the result. On YAML parse failure, missing frontmatter delimiter, file-write error, or `git add` failure, returns the existing `ErrConflictResolutionFailed` sentinel so the puller aborts the merge (no invalid file is ever committed).
- feat: Add `--vault-write` flag / `VAULT_WRITE_MODE` env (default `false`). When `true`, the pod uses `YAMLMergeResolver`; when `false`, behavior is unchanged (`MarkerResolver`). Selection is per-process; non-vault pods are unaffected.
- feat: Add `git_rest_resolver_failures_total{category}` Prometheus counter with the four labels `yaml_parse_failed`, `no_frontmatter`, `write_failed`, `git_add_failed` pre-initialised to zero. Operators can distinguish resolver failure modes without log scraping.
- Fixes agent vault scanner skipping markdown files whose frontmatter was clobbered by `<<<<<<<` markers (157 skips observed in prod the first minutes after `agent_controller_vault_scanner_skipped_files_total{reason="duplicate_frontmatter_invalid"}` went live).

## v0.20.1

- fix: Replace `git rebase` with `git merge` in the puller's diverged-history path. Non-overlapping concurrent writes now auto-merge into a single commit (no operator action). Same-line conflicts are delegated to a pluggable `ConflictResolver`; the default `MarkerResolver` commits the merge with `<<<<<<<` / `=======` / `>>>>>>>` markers intact so both versions survive. Resolver failure aborts the merge and returns `ErrConflictResolutionFailed`. Two new counters (`git_rest_merge_outcome_total`, `git_rest_conflict_paths_total`) track merge outcomes and conflict frequency. Entry-state recovery extended to abort leftover `.git/MERGE_HEAD` from interrupted merges. Fixes vault-obsidian-openclaw-0 52h desync incident (2026-05-12): the dropped `tasks/analyse-trades-2026-05-11.md` commit would have been preserved under conflict markers instead of silently discarded.

## v0.20.0

- feat: Add `ConflictResolver` interface and `MarkerResolver` implementation for pluggable merge conflict resolution; `ErrConflictResolutionFailed` sentinel for `errors.Is` detection
- feat: Add `git_rest_merge_outcome_total` (by result label: clean/resolved/aborted) and `git_rest_conflict_paths_total` Prometheus counters with `Metrics` interface methods `IncMergeOutcome`/`IncConflictPaths`

## v0.19.7

- fix: `Pull` now auto-recovers from abandoned-rebase (`.git/rebase-merge/` or `.git/rebase-apply/` present) and bare-detached-HEAD states on entry. Previously any path leaving HEAD detached permanently wedged the puller with "fatal: HEAD does not point to a branch" until manual `kubectl exec` recovery. New `ErrRepoUnrecoverable` sentinel returned for unrecoverable states (missing `refs/remotes/origin/HEAD`, failed `git rebase --abort`); callers use `errors.Is`. Recovery actions log at INFO with `"branch"` field. Fixes `vault-obsidian-openclaw-0` stuck 0/1 Running for 2d2h (prod incident 2026-05-12).

## v0.19.6

- fix: Install `tini` as PID 1 init reaper in Docker image to prevent zombie `[git] <defunct>` accumulation. With `/main` running as PID 1, grandchild processes spawned by `git` (ssh helpers, credential helpers) were reparented to `/main` on exit but never reaped — growing to ~18,000 zombies per pod after 3 days at `PULL_INTERVAL=30s` and blocking `k3s-killall.sh` node shutdown. `tini` reaps all orphaned children and forwards SIGTERM to `/main` for graceful shutdown (prod incident 2026-05-09, `vault-obsidian-trading-0`).

## v0.19.5

- Update Go runtime from 1.26.2 to 1.26.3
- Update core bborbe/* dependencies (errors, http, run, sentry, service, time)
- Update golangci-lint to v2.12.2 and osv-scanner to v2.3.8
- Replace jingyugao/rowserrcheck with golangci/rowserrcheck
- Add docs/verifying-specs.md for spec verification workflow

## v0.19.4

- fix: `WriteFile` and `DeleteFile` are now idempotent. Re-writing a file with identical content returns 200 and logs "no changes to commit" at INFO instead of returning 500 "nothing to commit, working tree clean". Re-deleting an already-absent file returns 200 instead of 404. Fixes CQRS retry loops in `bborbe/agent` task-controller that read the 500/404 as failure and retried up to 5 times (prod incident 2026-05-06, `vault-obsidian-openclaw-0`).

## v0.19.3

- fix: `PullStateCache.ReadinessStatus()` now returns 503 immediately on `*git.RebaseConflictError`, naming the conflict path in the body (`last pull failed: rebase conflict at <path>`). Transient errors (network, auth) retain the freshness-threshold approach. A subsequent successful pull restores readiness automatically.

## v0.19.2

- fix: Replace plain `git pull` with a deterministic 4-state sync (no-op / fast-forward / push / rebase+push). Divergence between local and remote is now auto-resolved via rebase without operator intervention. Rebase content conflicts return a typed `RebaseConflictError{Path}` — repo left for human inspection, no auto-abort. The `git_rest_git_operation_errors_total` counter gains a `conflict` label; rebase conflicts increment `{operation="rebase",conflict="true"}`. Fixes the "Need to specify how to reconcile divergent branches" forever-loop (prod incident 2026-05-04, `vault-obsidian-openclaw-0`).

## v0.19.1

- fix: Decouple `/readiness` from the pull mutex. Readiness now reads a `PullStateCache` written by the background puller — the handler never invokes a git subprocess. Before the first successful pull the probe returns 503 `no successful pull yet`; after a stale cache it returns 503 with the last pull error. Body never contains `context canceled`. Fixes false-NotReady during SSH timeouts (prod incident 2026-05-03, `vault-obsidian-openclaw-0`).

## v0.19.0

- feat: Introduce `PullStateCache` in `pkg/puller/pull_state.go` recording every pull outcome (success timestamp + last error) with a configurable freshness threshold; add `--pull-timeout` / `PULL_TIMEOUT` flag (default 60s) bounding each per-pull subprocess so a stalled SSH connection cannot hold the git mutex indefinitely

## v0.18.0

- feat: Optional shared-secret HTTP auth on `/api/v1/*` via `--gateway-secret` / `GATEWAY_SECRET`. Missing or wrong `X-Gateway-Secret` → 401; missing `X-Gateway-Initator` → 500. Probes (`/healthz`, `/readiness`, `/metrics`) remain unauthenticated. Empty secret (default) disables auth and logs a startup warning — no behavior change for existing deployments.

## v0.17.0

- feat: Add `NewGatewaySecretMiddleware` in `pkg/handler/` and `CreateGatewaySecretMiddleware` in `pkg/factory/` enforcing `X-Gateway-Initator` (500) and `X-Gateway-Secret` (401) header checks, stripping the secret before forwarding to the inner handler

## v0.16.0

- fix: `runGitCmd` now sets `GIT_SSH_COMMAND` for git network ops at startup, fixing the v0.15.0 bug where `syncOnStartup`'s pull/push failed with "Host key verification failed" because the SSH wrapper used by the periodic puller was not applied to the bootstrap path.
- feat: New `GIT_SSH_COMMAND` env var (and `--git-ssh-command` arg) for explicit SSH wrapper override. When unset, derives from `GIT_SSH_KEY` using the same format `pkg/git` already uses (`ssh -i <key> -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no`). Existing deployments need no config change.

## v0.15.0

- feat: Pull and push the configured remote at startup, after `recoverUntracked`. Closes the gap where recovery commits sat locally until the next API write (live incident 2026-04-28: `vault-obsidian-trading` recovered the orphan untracked file but readiness stayed 503 because the recovery commit was never pushed). No-op for local-only repos.

## v0.14.0

- feat: Auto-commit untracked files in the working tree on startup. Recovers from crashes between `os.WriteFile` and `git add` (e.g. OOM mid-write) without manual intervention. Push is deferred to the existing puller / next API call.

## v0.13.0

- feat: Auto-remove stale `*.lock` files in `.git/` on startup. Self-heals from prior crashes (OOM, signal kill) without manual intervention.

## v0.12.1

- docs: Rewrite README with features, quick start, configuration tables, and bootstrap modes
- docs: Add docs/api.md (full endpoint reference) and docs/deployment.md (standalone + K8s patterns)

## v0.12.0

- feat: Skip push and pull operations gracefully when no remote is configured

## v0.11.0

- feat: Add local repository initialization when no remote URL is configured

## v0.10.0

- feat: Add `--git-user-name` / `GIT_USER_NAME` and `--git-user-email` / `GIT_USER_EMAIL` flags; when set, configure `user.name` and `user.email` in the repository via `git config` on startup

## v0.9.0

- feat: Add `--git-remote-url` / `GIT_REMOTE_URL` flag; when set and repo has no `.git` directory, clone the remote on startup with parent directory creation via `os.MkdirAll`

## v0.8.0

- feat: Add SSH key support via `--git-ssh-key` / `GIT_SSH_KEY` flag; sets `GIT_SSH_COMMAND` on all git operations when key path is configured, with fast-fail validation that the key file exists at startup

## v0.7.1

- refactor: Replace `http.NewServeMux` with `gorilla/mux.NewRouter().SkipClean(true)` so path traversal attempts reach `validatePath` and return 400 Bad Request

## v0.7.0

- feat: Pre-initialize `HTTPRequestsTotal` counter for known method/path/status combinations to ensure time series are present before first request
- chore: Add missing `factory_suite_test.go` Ginkgo test suite bootstrap for `pkg/factory`

## v0.6.10

- fix: Add `ctx.Done()` cancellation check in `ListFiles` range loop to respect context cancellation for large repositories

## v0.6.9

- refactor: Fix file layout ordering in `pkg/metrics` and `pkg/git` — move counterfeiter directives directly above interfaces, reorder struct/constructor to follow Interface → Constructor → Struct → Methods pattern

## v0.6.8

- refactor: Inject `libtime.CurrentDateTimeGetter` into `pkg/git` to replace `time.Now()` calls; replace `time.Duration` with `libtime.Duration` in `pkg/puller` and `main.go`

## v0.6.7

- refactor: Replace `errors.Wrapf` calls without format verbs with `errors.Wrap` in `main.go` and `pkg/git/git.go`

## v0.6.6

- refactor: Convert all HTTP handlers from `http.Handler` with manual JSON error writing to `libhttp.WithError` + `libhttp.NewJSONErrorHandler` pattern; replace `writeJSONError`/`writeJSONOK` helpers with `libhttp.WrapWithStatusCode` and `libhttp.SendJSONResponse`; replace custom healthz handler with `libhttp.NewPrintHandler("OK")`; update factory functions to wrap handlers with `NewJSONErrorHandler`

## v0.6.5

- test: Add targeted tests for `pkg/git` error paths (invalid glob pattern, non-existent repo path, no-remote repo) and `pkg/handler.NewHealthzHandler` to bring `pkg/git` coverage to ≥80%

## v0.6.4

- refactor: Define `Metrics` interface in `pkg/metrics/` with `NewMetrics()` Prometheus-backed implementation; inject into `git.New()` and `NewMetricsMiddleware()` via constructor params, removing direct package-level var access from `pkg/git/`; normalize HTTP path labels to `/api/v1/files/{path}` to prevent unbounded cardinality; add `FakeMetrics` counterfeiter mock; pre-initialize `GitOperationErrors` counter for all known operations

## v0.6.3

- fix: Set ReadTimeout (60s), WriteTimeout (60s), and IdleTimeout (120s) on HTTP server to prevent slow-client resource exhaustion attacks

## v0.6.2

- refactor: Extract `filesDispatch` and `metricsMiddleware` from `main.go` into `pkg/handler/` as `NewFilesDispatchHandler` and `NewMetricsMiddleware`; add corresponding `CreateFilesDispatchHandler` and `CreateMetricsMiddleware` factory functions in `pkg/factory/`; add unit tests for both handlers

## v0.6.1

- refactor: Replace fragile `err.Error() == "http: request body too large"` string check with typed `errors.As(err, &maxBytesErr)` using `*http.MaxBytesError` in files_post handler

## v0.6.0

- feat: Add ErrInvalidPath sentinel error to pkg/git and update validatePath to wrap all validation failures with it, including new .git directory component check; update all three file handlers to use errors.Is(err, git.ErrInvalidPath) instead of string matching for 400 vs 500 routing

## v0.5.5

- chore: Add counterfeiter:generate directive to Puller interface and generate FakePuller mock in mocks/puller.go

## v0.5.4

- chore: Align main_test.go suite setup with canonical pattern (time.Local, format.TruncatedDiff, GinkgoConfiguration timeout, //go:generate directive, -mod=vendor)

## v0.5.3

- refactor: Replace errors.Wrapf with errors.Wrap for plain string messages (no format verbs) in pkg/git/git.go

## v0.5.2

- refactor: Replace flag-based main with service.Main pattern supporting CLI flags and environment variables via github.com/bborbe/service; add BuildInfoMetrics gauge git_rest_build_info; use libhttp.NewServer for graceful shutdown

## v0.5.1

- chore: Run full automated code review and generate fix prompts for Critical/Important findings

## v0.5.0

- feat: Add production Dockerfile and docker build/upload/clean/buca targets to Makefile; remove Makefile.docker

## v0.4.1

- refactor: Move Counterfeiter FakeGit mock from pkg/git/mocks/ to top-level mocks/ directory, update counterfeiter:generate annotation and all test imports

## v0.4.0

- feat: Implement git-rest HTTP server with file CRUD, periodic git pull, health/readiness probes, and Prometheus metrics

## v0.3.0

- feat: Add pkg/handler package with HTTP handlers for files CRUD, healthz, readiness, and JSON error helpers
- feat: Add pkg/factory package with Create* factory functions wiring handlers to git.Git

## v0.2.0

- feat: Add pkg/git package with Git interface, serialized shell operations, path validation, and Counterfeiter mock

## v0.1.0

- Initial project setup
- Add dark-factory config, spec, and definition of done
