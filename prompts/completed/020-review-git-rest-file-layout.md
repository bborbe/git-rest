---
status: completed
summary: 'Fixed file layout ordering violations: reordered struct/constructor in build_info.go to Interface→Constructor→Struct→Methods, and moved counterfeiter directives in metrics.go and git.go to be directly adjacent to their interfaces (linter reformatted to canonical Go style placing directive after GoDoc comment).'
container: git-rest-020-review-git-rest-file-layout
dark-factory-version: v0.108.0-dirty
created: "2026-04-12T13:29:16Z"
queued: "2026-04-12T13:49:18Z"
started: "2026-04-12T14:43:13Z"
completed: "2026-04-12T14:47:03Z"
---

<summary>
- Counterfeiter directives are not directly above their target interfaces
- Struct appears before constructor in build_info.go violating canonical order
- A blank line separates counterfeiter directive from interface in metrics.go
- Canonical file layout is Interface then Constructor then Struct then Methods
</summary>

<objective>
Fix file layout ordering violations in metrics and git packages: move counterfeiter directives directly above interfaces, reorder struct/constructor to follow Interface → Constructor → Struct → Methods pattern.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Files to read before making changes:
- `pkg/metrics/build_info.go` (~lines 19-26) — struct before constructor
- `pkg/metrics/metrics.go` (~lines 37-44) — blank line between counterfeiter directive and interface
- `pkg/git/git.go` (~lines 23-39) — counterfeiter directive far from interface
</context>

<requirements>

## 1. Fix build_info.go layout

In `pkg/metrics/build_info.go`, the `buildInfoMetrics` struct (~line 19) appears before the constructor `NewBuildInfoMetrics` (~line 26). Reorder to:
1. Interface (BuildInfoMetrics)
2. Constructor (NewBuildInfoMetrics)
3. Struct (buildInfoMetrics)
4. Methods (SetBuildInfo)

Keep package-level var and init() at the bottom.

## 2. Fix metrics.go counterfeiter directive

In `pkg/metrics/metrics.go`, remove the blank line between the counterfeiter `//go:generate` directive (~line 37) and the `// Metrics` GoDoc comment. The directive must be on the line immediately above the GoDoc comment.

## 3. Fix git.go counterfeiter directive

In `pkg/git/git.go`, the counterfeiter `//go:generate` directive (~line 23) is separated from the `Git` interface by var declarations and a struct. Move the directive to directly above the `// Git abstracts` GoDoc comment that precedes the interface definition.

</requirements>

<constraints>
- Only change files in `.`
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Only reorder declarations — do not change any logic or signatures
</constraints>

<verification>
make precommit
</verification>
