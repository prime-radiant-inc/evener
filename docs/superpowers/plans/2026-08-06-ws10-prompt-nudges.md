# WS10: Prompt and Skill-Layer Nudges Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The four evidence-backed prose fixes from the session study land in
the prompts and the doctoring-serf skill: deliverable self-check before
end_turn, tool-call batching guidance, delegate-brief staging hygiene, and
doctor-skill updates for the shipped WS1/WS9 tooling.

**Architecture:** Implements the WS10 section of
`docs/superpowers/plans/2026-08-06-agentic-ux-remediation.md`. Prose only —
no runtime behavior changes anywhere. Each edit cites its study pattern in
the commit message. WS1 and WS9 are merged, so item 4's references are
real; item 1 complements (does not depend on) WS3's in-flight end_turn
warning.

**Tech stack:** Go string constants / template files in `agent` (prompt
sections, delegation templates, `internal/bundled`), plus the
doctoring-serf skill markdown in-repo.

## Global Constraints

- Prose only: if an edit would require changing runtime behavior to be
  true, STOP and report BLOCKED — do not change runtime code.
- Never describe behavior that does not exist on main at commit time; when
  referencing another workstream's feature, describe only what is merged.
- Every touched prompt that has a snapshot/rendering test gets its test
  updated in the same commit; markdown-only edits need no new tests.
- Match the surrounding prompt voice and formatting exactly; these are
  model-facing contract surfaces.
- Gates before every commit: `go build ./...` and `go test ./...` in the
  agent module and root, exit codes checked directly — never `$?` after a
  pipe; redirect output to a file.
- Smallest reasonable change; no drive-by rewording of neighboring prose.

---

### Task 1: Deliverable self-check before end_turn

**Files:**
- Investigate: the prompt section documenting the result/communicate tool
  (`grep -rn "end_turn" agent/session_prompts.go agent/internal/bundled/`)
- Modify: that section; its snapshot test if one renders it

- [ ] **Step 1 (locate):** find where the session prompt instructs on
  ending a turn / communicating results. Record the file and constant in
  the task report.
- [ ] **Step 2:** add one short paragraph: before `end_turn=true`, re-read
  the task's stated deliverables and name each one in the output —
  work that was never started is the failure mode (10 study sessions of
  "done-but-deliverable-missing"). Keep it to ~3 sentences in the section's
  existing voice.
- [ ] **Step 3:** update any snapshot test; gates; commit
  (`docs(prompts): deliverable self-check before end_turn`).

### Task 2: Batching guidance for independent tool calls

**Files:**
- Investigate: the tool-use prompt section
  (`grep -rn "tool" agent/session_prompts.go` — the section instructing on
  making tool calls)
- Modify: that section; snapshot test if any

- [ ] **Step 1 (locate):** find the tool-use guidance section; record it.
- [ ] **Step 2:** add 1-2 sentences: independent reads/greps/status checks
  belong in one round, not sequential rounds (study evidence: 62 calls vs
  29 for identical review work).
- [ ] **Step 3:** snapshot test if any; gates; commit
  (`docs(prompts): batch independent tool calls in one round`).

### Task 3: Delegate-brief staging hygiene

**Files:**
- Investigate: delegation prompt templates
  (`grep -rn "git add" agent/ --include="*.go"` and
  `agent/internal/bundled/`)
- Modify: the delegate/subagent brief template(s)

- [ ] **Step 1 (locate):** find where delegate briefs instruct on
  committing. Record every template that mentions staging or committing.
- [ ] **Step 2:** add the rule: never `git add -A` / `git add .`; stage
  named paths only (study evidence: a ~1600-file accidental staging).
  If a template already forbids it, leave it alone and note that.
- [ ] **Step 3:** gates; commit
  (`docs(prompts): delegate briefs stage named paths, never -A`).

### Task 4: doctoring-serf skill catches up to WS1/WS9

**Files:**
- Modify: the doctoring-serf skill markdown in-repo (locate via
  `grep -rn "doctoring-serf" --include="*.md" .` from the worktree root;
  the WS9 work already touched its SKILL.md)
- Reference: `docs/superpowers/plans/2026-08-06-ws9-doctor-batch.md` and
  the merged serf-doctor subcommands (`sessions`, `transcript --health`,
  `apilog --health/--recompute/--validate`, `jobs`, `mutations`,
  `watches`, `tree`, `audit`)

- [ ] **Step 1:** verify each WS9 command exists on main (run
  `go run ./cmd/serf-doctor --help` or read `agent/doctor/`) — document
  only what is real.
- [ ] **Step 2:** update the skill's tool table with the batch `audit`
  driver and any WS9 commands it lacks; add the caveat that api logs
  recorded before WS1's fix show `recorded=0` for Responses-API sessions
  and need `--recompute` (state the exact flag).
- [ ] **Step 3:** gates (markdown-only edit still runs them — the repo has
  doc-consistency tests); commit
  (`docs(skill): doctoring-serf covers the audit driver and pre-WS1 recompute caveat`).

## Acceptance (whole workstream)

- All four edits landed, each citing its study pattern in the commit.
- No runtime behavior changed anywhere (diff touches only prose/templates
  and their snapshot tests).
- Nothing describes a feature absent from main.
