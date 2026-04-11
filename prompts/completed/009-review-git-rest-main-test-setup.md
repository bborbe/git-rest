---
status: completed
summary: 'Updated main_test.go to match canonical suite setup: added time.Local=time.UTC, format.TruncatedDiff=false, GinkgoConfiguration with 60s timeout, //go:generate directive, and changed gexec.Build flag from -mod=mod to -mod=vendor; also ran go mod vendor to fix inconsistent vendor directory.'
container: git-rest-009-review-git-rest-main-test-setup
dark-factory-version: v0.108.0-dirty
created: "2026-04-11T00:00:00Z"
queued: "2026-04-11T21:05:28Z"
started: "2026-04-11T21:10:25Z"
completed: "2026-04-11T21:15:23Z"
---

<summary>
- main_test.go TestSuite is missing the three required suite setup lines: time.Local=time.UTC, format.TruncatedDiff=false, and suiteConfig.Timeout
- It also lacks the //go:generate counterfeiter directive that every suite file in the project carries
- gexec.Build passes -mod=mod instead of -mod=vendor, contradicting the project vendoring convention
- These gaps mean tests may behave differently from other packages (timezone differences, truncated diff output, no build timeout)
</summary>

<objective>
Fix `main_test.go` to match the standard suite setup used across all other test files in the project: add `time.Local`, `format.TruncatedDiff`, `GinkgoConfiguration()` with timeout, the `//go:generate` directive, and correct the `gexec.Build` build flag to `-mod=vendor`.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Files to read before making changes (read ALL first):
- `main_test.go`: current incomplete TestSuite function
- `pkg/git/git_suite_test.go`: canonical suite setup pattern to follow
- `pkg/handler/handler_suite_test.go`: another canonical example
</context>

<requirements>
1. In `main_test.go`, update the imports to add:
   ```go
   "time"
   "github.com/onsi/gomega/format"
   ```

2. Add the `//go:generate` directive before `func TestSuite`:
   ```go
   //go:generate go run -mod=mod github.com/maxbrunsfeld/counterfeiter/v6 -generate
   ```

3. Replace the `TestSuite` function body to match the canonical pattern:
   ```go
   func TestSuite(t *testing.T) {
       time.Local = time.UTC
       format.TruncatedDiff = false
       RegisterFailHandler(Fail)
       suiteConfig, reporterConfig := GinkgoConfiguration()
       suiteConfig.Timeout = 60 * time.Second
       RunSpecs(t, "Main Suite", suiteConfig, reporterConfig)
   }
   ```

4. In the `gexec.Build` call, change `-mod=mod` to `-mod=vendor`:
   ```go
   // Before:
   compiledPath, err = gexec.Build(".", "-mod=mod", fmt.Sprintf("-ldflags=%s", ldflags))
   // After:
   compiledPath, err = gexec.Build(".", "-mod=vendor", fmt.Sprintf("-ldflags=%s", ldflags))
   ```
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Follow the exact pattern from pkg/git/git_suite_test.go and pkg/handler/handler_suite_test.go
- The suiteConfig.Timeout value must be 60 * time.Second
</constraints>

<verification>
make precommit
</verification>
