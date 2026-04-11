---
status: completed
summary: Ran full 18-agent code review of git-rest and generated 12 fix prompts covering Critical and Important findings in the prompts/ inbox directory.
container: git-rest-006-code-review-git-rest
dark-factory-version: v0.108.0-dirty
created: "2026-04-11T20:10:39Z"
queued: "2026-04-11T20:16:18Z"
started: "2026-04-11T20:16:20Z"
completed: "2026-04-11T20:27:34Z"
---

<summary>
- Service reviewed using full automated code review with all specialist agents
- Fix prompts generated for each Critical or Important finding
- Each fix prompt is independently verifiable and scoped to one concern
- No code changes made — review-only prompt that produces fix prompts
- Clean services produce no fix prompts
</summary>

<objective>
Run a full code review of the git-rest project and generate a fix prompt for each Critical or Important finding.
</objective>

<context>
Read `CLAUDE.md` for project conventions.
Read `docs/dod.md` for Definition of Done criteria.

Read 3 recent completed prompts from `prompts/completed/` (highest-numbered) to understand prompt style and XML tag structure. If none exist, use the prompt structure defined in this requirements section.

Service directory: project root (`.`)
</context>

<requirements>

## 1. Read Config

Read `.dark-factory.yaml` to find `prompts.inboxDir` (default: `prompts`). Use this as the output directory for fix prompts.

## 2. Run Code Review

Run `/coding:code-review full .` to get a comprehensive review with all specialist agents.

Collect the consolidated findings categorized as:
- **Must Fix (Critical)** — will generate fix prompts
- **Should Fix (Important)** — will generate fix prompts
- **Nice to Have** — skip, do NOT generate prompts

## 3. Generate Fix Prompts

For each Critical or Important finding (or group of related findings in the same file/package), write a prompt file to the prompts inbox directory.

**Filename:** `review-git-rest-<fix-description>.md`

Each fix prompt must follow this exact structure (frontmatter, then XML sections):

    ---
    status: draft
    created: "<current UTC timestamp in ISO8601>"
    ---

    <summary>
    5-10 plain-language bullets. No file paths, struct names, or function signatures.
    </summary>

    <objective>
    What to fix and why (1-3 sentences). End state, not steps.
    </objective>

    <context>
    Read `CLAUDE.md` for project conventions.

    Files to read before making changes (read ALL first):
    - list specific files with line numbers as hints
    </context>

    <requirements>
    Numbered, specific, unambiguous steps.
    Anchor by function/type name (~line N as hint only).
    Include function signatures where helpful.
    </requirements>

    <constraints>
    - Do NOT commit — dark-factory handles git
    - Existing tests must still pass
    - Use `errors.Wrap`/`errors.Errorf` from `github.com/bborbe/errors` — never `fmt.Errorf` or bare `return err`
    </constraints>

    <verification>
    make precommit
    </verification>

**Grouping rules:**
- One concern per prompt (e.g., "fix error wrapping in package X")
- Group coupled findings that must change together
- Split unrelated findings into separate prompts
- If order matters, prefix filenames with `1-`, `2-`, `3-`

## 4. Summary

Print a summary of findings and generated prompt files.

</requirements>

<constraints>
- Do NOT modify any source code — this is a review-only prompt
- Only write files to the prompts inbox directory
- Never write to `in-progress/` or `completed/` subdirectories
- Never number prompt filenames — dark-factory assigns numbers on approve
- Repo-relative paths only in generated prompts (no absolute, no `~/`)
- If no findings at Critical/Important level → report clean bill of health, generate no prompts
</constraints>

<verification>
After generating fix prompts, list them:
```bash
ls prompts/review-git-rest-*.md 2>/dev/null || echo "No fix prompts generated (clean)"
```
</verification>
