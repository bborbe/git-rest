---
status: completed
summary: Fixed errcheck violation in pkg/git/git_test.go by adding blank identifier to os.RemoveAll call in DeferCleanup; all tests pass with pkg/git at 86.3% coverage and pkg/handler at 97.7% coverage.
container: git-rest-016-review-git-rest-git-test-coverage
dark-factory-version: v0.108.0-dirty
created: "2026-04-11T00:00:00Z"
queued: "2026-04-11T21:05:58Z"
started: "2026-04-12T11:22:26Z"
completed: "2026-04-12T11:26:36Z"
lastFailReason: 'validate completion report: completion report status: partial'
---

<summary>
- pkg/git has 79.0% statement coverage, which is below the required 80% threshold
- The gaps are concentrated in error paths: broken repo directory, filesystem errors, and an invalid glob pattern
- The easiest single fix is the invalid glob pattern test for ListFiles (filepath.Match returns an error for unclosed brackets)
- Additional gaps in WriteFile, DeleteFile, Pull, and Status also need tests for repos with no remote configured
- Adding a healthz handler test covers the zero-coverage NewHealthzHandler
</summary>

<objective>
Add targeted tests to bring `pkg/git` to ≥80% coverage and add a missing test for `NewHealthzHandler` in `pkg/handler`. Focus on error paths that are currently untested: invalid glob pattern, broken repo path, and no-remote repo.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Files to read before making changes (read ALL first):
- `pkg/git/git_test.go`: existing test structure, BeforeEach/AfterEach repo setup to understand how to create temp repos
- `pkg/git/git_suite_test.go`: suite setup
- `pkg/handler/healthz.go`: the handler to test
- `pkg/handler/handler_suite_test.go`: suite setup and existing test helpers
- `pkg/handler/helpers_test.go`: test helper patterns
</context>

<requirements>
1. In `pkg/git/git_test.go`, add the following test cases (use the existing temp repo setup pattern from BeforeEach):

   **a) Invalid glob pattern for ListFiles:**
   Inside the existing `Describe("ListFiles")` block, add:
   ```go
   Context("invalid glob pattern", func() {
       It("returns an error", func() {
           _, err := g.ListFiles(ctx, "[invalid")
           Expect(err).To(HaveOccurred())
       })
   })
   ```

   **b) Repo path that does not exist (for runCmd/runCmdOutput error paths):**
   Add a new `Describe` block or `Context` block that creates a git.New with a non-existent path:
   ```go
   Describe("with non-existent repo path", func() {
       var brokenGit git.Git
       BeforeEach(func() {
           brokenGit = git.New("/nonexistent/path/that/does/not/exist")
       })

       It("ListFiles returns error", func() {
           _, err := brokenGit.ListFiles(ctx, "")
           Expect(err).To(HaveOccurred())
       })

       It("Pull returns error", func() {
           err := brokenGit.Pull(ctx)
           Expect(err).To(HaveOccurred())
       })

       It("Status returns error on porcelain", func() {
           _, err := brokenGit.Status(ctx)
           Expect(err).To(HaveOccurred())
       })
   })
   ```

   **c) Repo with no remote configured (for Pull error path and Status NoPushPending=true path):**
   Add a `Context` that initializes a bare git repo without a remote:
   ```go
   Describe("with no remote configured", func() {
       var noRemoteDir string
       var noRemoteGit git.Git
       BeforeEach(func() {
           var err error
           noRemoteDir, err = os.MkdirTemp("", "git-no-remote-*")
           Expect(err).NotTo(HaveOccurred())
           DeferCleanup(func() { os.RemoveAll(noRemoteDir) })

           runGit(noRemoteDir, "init")
           runGit(noRemoteDir, "config", "user.email", "test@test.com")
           runGit(noRemoteDir, "config", "user.name", "Test")
           noRemoteGit = git.New(noRemoteDir)
       })

       It("Pull returns error", func() {
           err := noRemoteGit.Pull(ctx)
           Expect(err).To(HaveOccurred())
       })

       It("Status sets NoPushPending=true when no upstream", func() {
           s, err := noRemoteGit.Status(ctx)
           Expect(err).NotTo(HaveOccurred())
           Expect(s.NoPushPending).To(BeTrue())
       })
   })
   ```
   Use a helper `runGit(dir string, args ...string)` that calls exec.Command("git", args...) with Dir=dir — check if such a helper already exists in the test file; if not, add it as a package-level function.

2. In `pkg/handler/`, create `healthz_test.go` (or add a `Context` block to an existing test file):
   ```go
   Describe("NewHealthzHandler", func() {
       It("returns 200 with body ok", func() {
           h := handler.NewHealthzHandler()
           rec := httptest.NewRecorder()
           req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
           h.ServeHTTP(rec, req)
           Expect(rec.Code).To(Equal(http.StatusOK))
           Expect(rec.Body.String()).To(Equal("ok"))
       })
   })
   ```

3. After adding tests, verify coverage:
   ```bash
   go test -coverprofile=/tmp/cover.out -mod=vendor ./pkg/git/... && go tool cover -func=/tmp/cover.out
   ```
   The `pkg/git` total must be ≥80%.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Use Ginkgo/Gomega patterns matching the existing test files
- Use external test packages (`package git_test`, `package handler_test`)
- Use `context.Background()` in test files only — it is explicitly allowed in tests
- DeferCleanup is preferred over AfterEach for temp directory cleanup
</constraints>

<verification>
make precommit
</verification>
