---
status: draft
created: "2026-04-11T00:00:00Z"
---

<summary>
- The Puller interface in pkg/puller/puller.go is exported but has no counterfeiter:generate directive
- Every exported interface must have the directive directly above it to enable mock generation
- The directive is needed for any future test that needs to inject a mock Puller
- This is a one-line addition that follows the established pattern from pkg/git/git.go
</summary>

<objective>
Add a `//counterfeiter:generate` directive immediately above the `Puller` interface in `pkg/puller/puller.go` and add the `//go:generate` runner to `pkg/puller/puller_suite_test.go`.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Files to read before making changes (read ALL first):
- `pkg/puller/puller.go` (~line 15-18): the Puller interface definition
- `pkg/puller/puller_suite_test.go`: the test suite file to add go:generate to
- `pkg/git/git.go` (~line 23): the counterfeiter:generate pattern to follow
- `mocks/mocks.go`: the package stub for the mocks directory
</context>

<requirements>
1. In `pkg/puller/puller.go`, add the counterfeiter directive on the line directly preceding `// Puller periodically runs git pull on a repository.` (~line 15):
   ```go
   //counterfeiter:generate -o ../../mocks/puller.go --fake-name FakePuller . Puller
   ```

2. In `pkg/puller/puller_suite_test.go`, add the `//go:generate` directive immediately before the `func TestSuite(t *testing.T)` function:
   ```go
   //go:generate go run -mod=mod github.com/maxbrunsfeld/counterfeiter/v6 -generate
   ```

3. Run `go generate ./pkg/puller/...` to generate `mocks/puller.go`.

4. Verify `mocks/puller.go` was created and compiles correctly.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Output path must be `../../mocks/puller.go` — mocks always live at the project root mocks/ directory
- Use `--fake-name FakePuller` to match the FakeGit naming convention in this project
</constraints>

<verification>
make precommit
</verification>
