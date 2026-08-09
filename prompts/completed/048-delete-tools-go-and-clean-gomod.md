---
status: completed
summary: Deleted tools.go and rewrote go.mod to remove all tool CLIs as module dependencies — go.mod shrunk from 462 to 50 lines, 5 replace workarounds removed, go-git removed entirely, all bborbe/* deps bumped to latest, counterfeiter directives pinned to v6.12.2
execution_id: git-rest-exec-048-delete-tools-go-and-clean-gomod
dark-factory-version: v0.192.9
created: "2026-08-09T14:14:04Z"
queued: "2026-08-09T14:14:04Z"
started: "2026-08-09T14:15:34Z"
completed: "2026-08-09T14:17:38Z"
branch: dark-factory/048-delete-tools-go-and-clean-gomod
---

# Delete tools.go and remove tool-dependency pollution from go.mod

<summary>
- Tool CLIs are no longer declared as Go module dependencies of this project
- The project's dependency list shrinks to only what the application actually uses
- Linters, scanners, and code generators keep running at exactly the same pinned versions
- Long-standing version-conflict workarounds are no longer needed and go away
- The dependency graph no longer drags in unrelated tooling internals
- Mock generation keeps working, pinned to the same generator version as before
- No application behavior changes — this is dependency hygiene only
</summary>

<objective>
Delete `tools.go` and rewrite `go.mod` so tool CLIs are no longer module dependencies, because importing them pulls every tool's transitive dependency tree into this project — inflating `go.mod` to 462 lines, forcing five `replace` workarounds, and repeatedly surfacing CVEs in packages this service never executes. Tool versions stay pinned via `tools.env` and `go run pkg@$(VERSION)`, which already works in the Makefile.
</objective>

<context>
Read the coding plugin's `docs/go-tools-versioning-guide.md` (in-container path: `/home/node/.claude/plugins/marketplaces/coding/docs/go-tools-versioning-guide.md`) — it is the canonical source for this migration. Requirements 4 and 6 below are taken verbatim from its "Migration Steps" §5 and §7. Read its "Pitfalls" section in particular: one unbumped `bborbe/*` dep brings the entire cascade back, and `go mod tidy -e` can truncate `go.mod`.

Read `CLAUDE.md` for project conventions.

Note: `CLAUDE.md` line 82 claims "Go 1.26.2, vendored dependencies (`-mod=vendor`)". That line is stale and contradicts this prompt — `go.mod` declares `go 1.26.5` and there is no `vendor/` directory (`.gitignore` lists `/vendor`). Ignore it; the constraints below win.

Read `tools.go` — the file to delete. It imports 11 CLI tools under a `//go:build tools` tag.

Read `Makefile` — note it already has `include tools.env` (line 9) and already invokes every tool as `go run pkg@$(VERSION)`. **No Makefile tool invocation needs changing.** The only `-mod=mod` usages that remain (`go run -mod=mod main.go`, `go generate -mod=mod`, `go test -mod=mod`, `go vet -mod=mod`, `go list -mod=mod`) are correct and must stay.

Read `tools.env` — the pinned tool versions. `COUNTERFEITER_VERSION` is `v6.12.2`.

Read `go.mod` — note the `replace` block at the top and the mixed direct-require list containing both real application deps and tool-only deps.
</context>

<requirements>
1. Update all five `//go:generate` counterfeiter directives to pin the version explicitly. In each of these files:
   - `main_test.go`
   - `pkg/handler/handler_suite_test.go`
   - `pkg/metrics/metrics_suite_test.go`
   - `pkg/puller/puller_suite_test.go`
   - `pkg/git/git_suite_test.go`

   Change:
   ```
   //go:generate go run -mod=mod github.com/maxbrunsfeld/counterfeiter/v6 -generate
   ```
   to:
   ```
   //go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate
   ```
   The version must match `COUNTERFEITER_VERSION` in `tools.env`.

2. Delete `tools.go` entirely.

3. Rewrite `go.mod` to a minimal form: keep the `module` line and `go 1.26.5`, delete the entire `replace` block, and keep ONLY these direct requires (these are the deps the application and its tests actually import):
   ```
   github.com/bborbe/errors
   github.com/bborbe/http
   github.com/bborbe/run
   github.com/bborbe/sentry
   github.com/bborbe/service
   github.com/bborbe/time
   github.com/felixge/httpsnoop
   github.com/golang/glog
   github.com/gorilla/mux
   github.com/onsi/ginkgo/v2
   github.com/onsi/gomega
   github.com/prometheus/client_golang
   gopkg.in/yaml.v3
   ```
   Drop every tool-only direct require: `golangci-lint/v2`, `google/addlicense`, `osv-scanner/v2`, `goimports-reviser/v3`, `kisielk/errcheck`, `counterfeiter/v6`, `gosec/v2`, `segmentio/golines`, `shoenig/go-modtool`, `golang.org/x/vuln`.

   Keep the existing version for each retained dep. Delete the entire indirect require block — `go mod tidy` repopulates it.

4. Bump every `github.com/bborbe/*` dependency — direct AND indirect, do not filter on `// indirect` — to its latest release. An unbumped bborbe dep still carrying its own `tools.go` re-drags the whole pollution cascade back in:
   ```
   grep '^	github.com/bborbe/' go.mod | awk '{print $1}' | xargs -I {} go get {}@latest
   ```
   Run this after step 3, and re-run it once more after `go mod tidy` in case tidy surfaces additional indirect bborbe deps.

5. Run `go mod tidy` to repopulate legitimate indirect requires. Do NOT use `go mod tidy -e` — it can truncate `go.mod` when resolution partially fails.

6. Confirm zero tools.go-era pollution remains:
   ```
   grep -E '(cellbuf|go-header|go-diskfs|golangci-lint|osv-scanner|ginkgolinter|charmbracelet/x|denis-tingaikin)' go.mod
   ```
   This must return no matches. If any appear, run `go mod why <package>` — it will name the unbumped `bborbe/*` dep still pulling the old cascade — then re-run step 4 for that dep.

7. Confirm `go-git` is gone entirely: `grep go-git go.mod` must return no matches. It was only ever reachable through the tool imports; this service shells out to the system `git` binary and never imports a git library.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles all git operations.
- Do NOT run `go mod vendor`, and never use `-mod=vendor` in any command. This repo has no `vendor/` directory. If any generic dependency-fix guidance you encounter suggests a `go mod vendor` step, ignore it — this constraint takes precedence.
- Do NOT change any application code under `pkg/` or `main.go` beyond the five `//go:generate` comment lines listed in requirement 1.
- Do NOT add, remove, or reorder tool versions in `tools.env`.
- Do NOT modify any tool invocation in the `Makefile` — every one is already correct.
- Do NOT edit `.osv-scanner.toml`. Some of its ignore entries will become unused once the pollution is gone; that is expected and is not an error.
- Existing tests must still pass unchanged; mock regeneration must produce no diff in behavior.
- If a retained direct dep in requirement 3 turns out to be genuinely unused by the code, let `go mod tidy` drop it rather than forcing it back in.
</constraints>

<verification>
Run `make precommit` — must pass (exit 0).

Then confirm the pollution is gone:

```
grep -E '(cellbuf|go-header|go-diskfs|golangci-lint|osv-scanner|ginkgolinter|charmbracelet/x|denis-tingaikin)' go.mod
grep go-git go.mod
ls tools.go
```

The two greps must produce no output, and `ls tools.go` must report that the file does not exist.

Confirm `go.mod` shrank substantially — `wc -l go.mod` should report well under 150 lines (it was 462).

Confirm the generator pin landed:

```
grep -rn 'counterfeiter/v6' --include='*_test.go' .
```

Every match must read `go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate`; none may still contain `-mod=mod`.
</verification>
