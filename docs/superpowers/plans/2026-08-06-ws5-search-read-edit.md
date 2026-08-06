# WS5: Search, Read, and Edit Tools Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The file tools stop wasting agent effort: the glob/list_dir naming
collision ends, unscoped searches return bounded useful results instead of
front-truncated megabytes, silent failure modes become loud, and the edit
tools point agents at recovery instead of guessing.

**Architecture:** Implements the WS5 section of
`docs/superpowers/plans/2026-08-06-agentic-ux-remediation.md`; the naming
decision (glob aliases to `find_files`) was ruled by Jesse 2026-08-06.
Anchors verified 2026-08-06; trust symbol names over line numbers.

**Tech stack:** Go: `agent/provider` (tool-name aliasing), `agent/execenv`
(Glob/Grep/ReadFile), `agent/internal/tool` (definitions, truncation),
`agent/internal/globpattern`.

**Sequencing:** launches after WS6 merges (both edit
`agent/internal/tool/definitions.go`).

## Global Constraints

- Model-facing names and descriptions are contract surface: every rename or
  description change updates the tool definition, the profile alias map,
  and any prompt section naming the tool, in the same task.
- Silent-empty is the enemy: unsupported syntax errors loudly; scoping
  limits state themselves in the result.
- TDD; multi-module gates per commit (agent module + root), exit codes
  only; smallest reasonable change; no drive-by refactors.

---

### Task 1: End the naming collision — glob aliases to find_files

**Files:**
- Modify: `agent/provider/profile.go` (`NewOpenAIProfile` `toolNameMap` ~:831-835), `agent/session_tools.go` (collision handling ~:844-858), `agent/internal/tool/definitions.go` (`DefGlob` description)
- Test: profile alias tests

- [ ] **Step 1 (failing test):** in the OpenAI profile, the canonical
  `glob` tool surfaces as `find_files`, the real `list_dir` keeps its name
  and is NOT shadowed, and `grep`→`grep_files` still holds. Assert no two
  canonical tools map to one wire name across all profiles.
- [ ] **Step 2:** implement; delete the shadowing workaround at the
  collision site; `find_files`'s description states it is a recursive
  glob over file paths ("find files by pattern") to anchor the right
  prior.
- [ ] **Step 3:** gates; commit
  (`fix(provider): glob aliases to find_files — list_dir is a directory listing again`).

### Task 2: Glob excludes and bounded results

**Files:**
- Modify: `agent/execenv/local.go` (`Glob` ~:1008-1043), `agent/execenv/securepath_browse.go` (sandboxed variant ~:106-139), `agent/internal/tool/registry.go` (glob/grep limits ~:624-651, truncation ~:572-605)
- Test: table-driven glob tests; truncation-summary tests

- [ ] **Step 1 (failing tests):**
  (a) glob over a fixture tree with `.git/`, `.claude/worktrees/x/`,
  gitignored and node_modules entries: dot-dirs and gitignored paths
  excluded by default; `include_ignored: true` restores them;
  (b) a glob whose full result exceeds the cap returns the FIRST N
  entries plus a structural summary line ("N total matches; showing first
  M; narrow the pattern") — no front-truncation, no dropped head;
  (c) grep results get the same head-first bounded shape.
- [ ] **Step 2:** implement excludes (match grepNative's dot-skip plus
  gitignore awareness — reuse whatever ignore machinery the repo already
  has before adding a dependency) and replace `TruncTail` with counted
  head-first bounding for glob and grep.
- [ ] **Step 3:** gates; commit
  (`fix(execenv): scoped-by-default glob and bounded head-first search results`).

### Task 3: Loud search failure modes

**Files:**
- Modify: `agent/execenv/local.go` (`buildRipgrepArgsWithFilters` ~:1077-1097), `agent/sandbox/denial.go` (symlink message ~:119-144), `agent/internal/tool/definitions.go` (`DefGrep` — `context_lines` param)
- Test: regression tests per item

- [ ] **Step 1 (failing tests):**
  (a) pattern `--font-size-body` searches literally (insert `--` before
  the pattern in the rg argv; native fallback already fine);
  (b) symlink denial names the symlinked path component and suggests the
  resolved target path or scoping below it — not just the basename;
  (c) `context_lines` (0–10) returns surrounding lines via `rg -C` and
  the native equivalent.
- [ ] **Step 2:** implement all three.
- [ ] **Step 3:** gates; commit
  (`fix(execenv): grep handles dash-patterns, names symlink denials, gains context_lines`).

### Task 4: read_file discoverability and ENOENT suggestions

**Files:**
- Modify: `agent/internal/tool/definitions.go` (`DefReadFile` ~:9-25 — offset/limit descriptions), `agent/execenv/local.go` (`ReadFile` ~:659-708 — ENOENT handling)
- Test: suggestion tests

- [ ] **Step 1 (failing tests):**
  (a) `read_file` on a missing path whose parent dir exists returns the
  error plus up to 3 fuzzy-matched candidates from that dir ("did you
  mean agent/session_tools.go?");
  (b) a doubled-path-segment miss (`…/worktrees/x/worktrees/x/file.go`)
  suggests the existing collapsed path — walk up to the nearest existing
  ancestor and fuzzy-match the remaining suffix;
  (c) schema snapshot: offset/limit carry prose descriptions ("for large
  files read in slices: offset = 1-based start line, limit = line count,
  default 2000").
- [ ] **Step 2:** implement; reuse the repair package's fuzzy-match
  approach (`agent/internal/tool/repair/suggest.go`) rather than a new
  algorithm.
- [ ] **Step 3:** gates; commit
  (`feat(execenv): read_file ENOENT suggestions; slice params documented`).

### Task 5: Edit-tool recovery guidance

**Files:**
- Modify: the apply_patch context-mismatch error site (locate in `agent/` — the "expected lines not found" message), and edit_file's read-tracking set (locate where "file not read in this session" is enforced and where write_file records writes)
- Test: message and crediting tests

- [ ] **Step 1 (failing tests):**
  (a) apply_patch context-mismatch error ends with: "the file has changed
  since the content this patch was built from; re-read the target region
  with read_file, then rebuild the patch" (exact-match context behavior
  itself unchanged — no fuzzy matching, per the plan);
  (b) a file created or fully written via write_file this session is
  editable by edit_file without the not-read warning; a shell-heredoc
  write still warns (serf can't know that content).
- [ ] **Step 2:** implement both.
- [ ] **Step 3:** gates; commit
  (`fix(tools): apply_patch points at re-read; edit_file credits session writes`).

## Acceptance (whole workstream)

- OpenAI-profile sessions have `find_files` + a real `list_dir`; no
  collision anywhere.
- A `**/*` search over this repo returns a bounded head + count line, with
  worktrees/gitignored noise excluded, in every mode.
- The `--`-pattern, brace-glob-on-empty (now via bounded results +
  excludes), symlink, ENOENT, stale-patch, and write-crediting cases each
  have a pinned regression test.
