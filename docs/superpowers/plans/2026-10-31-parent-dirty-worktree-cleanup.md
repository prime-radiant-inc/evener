# Parent dirty worktree cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the owning parent actionable cleanup guidance for dirty delegate worktrees through inline and terminal delivery without deleting worktrees automatically.

**Architecture:** Reuse the existing owner- and tool-gated `disposal_hint`. Make its content depend on the report's dirty bit, propagate it through terminal packet metadata and inline result reconstruction, and document the existing explicit disposal policy.

**Tech Stack:** Go, existing delegate terminal packet JSON, Markdown product docs.

**Spec:** `docs/superpowers/specs/2026-10-31-parent-dirty-worktree-cleanup-design.md`

## Global Constraints

- Evener must never discard dirty worktree changes automatically.
- Include a hint only when the receiving parent owns the delegate and has the worktree tool.
- Preserve the existing ordinary-dispose refusal for dirty worktrees and explicit `force_dirty` behavior.
- Do not add a worktree API or change collection policy.

## Review Focus

- Dirty worktree hint reaches both inline results and immutable terminal packets; test both paths.
- Clean worktree retains ordinary disposal guidance; test dirty-state-specific guidance selection.
- Foreign owners and sessions without `manage_worktree` get no hint; retain and run the gate test.
- Missing worktree report does not claim clean state; retain and run the non-isolated report test.
- No code path invokes forced disposal; run the existing disposal refusal/force tests.

---

### Task 1: Test and propagate parent-facing cleanup guidance

**Files:**
- Modify: `agent/job_delegate.go`
- Modify: `agent/subagents.go`
- Modify: `agent/delegate_send.go`
- Test: `agent/delegate_disposal_hint_test.go`
- Test: relevant terminal metadata and delegate send tests

**Interfaces:**
- `delegateWorktreeReport.DisposalHint` remains the existing inline hint field.
- Add `DisposalHint string` with JSON name `disposal_hint,omitempty` to `delegateTerminalWorktreeReport`.
- Preserve the field when `delegateTerminalMetadataFromRun` and `populateStableDelegateSendResult` project reports.

- [ ] **Step 1: Write behavior tests** for dirty versus clean hints, terminal metadata propagation, and inline terminal-result reconstruction. Assert presence/absence and meaningful state, not exact prose.
- [ ] **Step 2: Run those tests** with `go test ./agent -run 'DelegateDisposalHint|DelegateRunFinished|StableDelegateWorktree' -count=1`; confirm the terminal propagation assertions fail first.
- [ ] **Step 3: Implement the minimal propagation** and make dirty guidance direct the parent to inspect or resume before disposal, noting that `force_dirty` discards changes.
- [ ] **Step 4: Run focused tests** with the same command and confirm they pass, including ownership/tool gates and existing dirty disposal behavior.
- [ ] **Step 5: Commit** the code and tests with a detailed intent-focused message.

### Task 2: Document parent cleanup behavior

**Files:**
- Modify: `docs/product/subsystems.md` (S12)
- Modify: `docs/product/session-activity.md` (delegate terminal report/notification behavior)

- [ ] **Step 1: Update the S12 and session activity descriptions** to explain that parent-facing dirty reports point parents to inspect or resume the child, ordinary disposal refuses dirty lanes, and `force_dirty` explicitly discards changes; no automatic deletion occurs.
- [ ] **Step 2: Run verification** with `git diff --check` and `go test ./agent -run 'DelegateDisposalHint|DelegateRunFinished|StableDelegateWorktree' -count=1`.
- [ ] **Step 3: Commit** the documentation separately.
