---
status: completed
spec: [001-git-rest-server]
summary: Moved Counterfeiter FakeGit mock from pkg/git/mocks/ to top-level mocks/git.go, updated the annotation in pkg/git/git.go to use //counterfeiter:generate, deleted the old mocks directory, and updated all test imports across handler and puller packages.
container: git-rest-004-fix-mocks-location
dark-factory-version: v0.108.0-dirty
created: "2026-04-11T21:50:00Z"
queued: "2026-04-11T20:05:20Z"
started: "2026-04-11T20:05:22Z"
completed: "2026-04-11T20:09:26Z"
---

<summary>
- Counterfeiter mocks move from pkg/git/mocks/ to the project-level mocks/ directory
- Mock generation annotation follows the project convention: output to ../../mocks/ with --fake-name prefix
- All test imports update to reference the new mock location
- The nested pkg/git/mocks/ directory is removed
- The go:generate directive stays in the test suite file, matching the counterfeiter -generate pattern
</summary>

<objective>
Move the Counterfeiter-generated mock from `pkg/git/mocks/` to the top-level `mocks/` directory, matching the project convention used across all bborbe Go projects.
</objective>

<context>
Read `CLAUDE.md` and `docs/dod.md` for project conventions.

The project convention for Counterfeiter mocks (used in agent/task/controller, trading services, etc.):
- Mocks always live in the top-level `mocks/` directory
- The `//counterfeiter:generate` annotation uses relative `-o` path to reach `mocks/`
- Example from `pkg/gitclient/git_client.go`:
  ```go
  //counterfeiter:generate -o ../../mocks/git_client.go --fake-name FakeGitClient . GitClient
  ```
- The `//go:generate` directive lives in the test suite file, not the interface file:
  ```go
  //go:generate go run -mod=mod github.com/maxbrunsfeld/counterfeiter/v6 -generate
  ```

Current state:
- `pkg/git/git.go` — has `//go:generate counterfeiter -o mocks/fakes.go . Git`
- `pkg/git/mocks/fakes.go` — generated mock (wrong location)
- `mocks/mocks.go` — package bootstrap (correct location, already exists)
- Handler tests in `pkg/handler/*_test.go` import `pkg/git/mocks`
</context>

<requirements>
1. In `pkg/git/git.go`:
   - Remove the existing `//go:generate` directive
   - Add `//counterfeiter:generate -o ../../mocks/git.go --fake-name FakeGit . Git` above the `Git` interface

2. Create or update `pkg/git/git_suite_test.go` to include:
   ```go
   //go:generate go run -mod=mod github.com/maxbrunsfeld/counterfeiter/v6 -generate
   ```

3. Delete the `pkg/git/mocks/` directory entirely

4. Run `go generate ./...` to regenerate the mock at `mocks/git.go`

5. Update all test files that import `github.com/bborbe/git-rest/pkg/git/mocks` to import `github.com/bborbe/git-rest/mocks` instead

6. Update all references from `mocks.FakeGit` to match the new fake name `FakeGit` (should already match if using `--fake-name FakeGit`)

7. Run `make precommit` to confirm everything compiles and tests pass
</requirements>

<constraints>
- Mocks must live in top-level `mocks/` directory — not nested under `pkg/`
- Use `//counterfeiter:generate` annotation (not `//go:generate counterfeiter`)
- Use `--fake-name FakeGit` prefix convention
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Errors must be wrapped with `github.com/bborbe/errors`
</constraints>

<verification>
```bash
make precommit
```

Additional checks:
```bash
# Confirm mock is at correct location
ls /workspace/mocks/git.go

# Confirm old location is gone
test ! -d /workspace/pkg/git/mocks && echo "OK: pkg/git/mocks removed"

# Confirm no imports of old path
grep -rn "pkg/git/mocks" /workspace/pkg/ && echo "FAIL: old import found" || echo "OK"
```
</verification>
