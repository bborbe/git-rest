# Changelog

All notable changes to this project will be documented in this file.

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
