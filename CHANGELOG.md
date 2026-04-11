# Changelog

All notable changes to this project will be documented in this file.

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

Please choose versions by [Semantic Versioning](http://semver.org/).

* MAJOR version when you make incompatible API changes,
* MINOR version when you add functionality in a backwards-compatible manner, and
* PATCH version when you make backwards-compatible bug fixes.

## v0.3.0

- feat: Add pkg/handler package with HTTP handlers for files CRUD, healthz, readiness, and JSON error helpers
- feat: Add pkg/factory package with Create* factory functions wiring handlers to git.Git

## v0.2.0

- feat: Add pkg/git package with Git interface, serialized shell operations, path validation, and Counterfeiter mock

## v0.1.0

- Initial project setup
- Add dark-factory config, spec, and definition of done
