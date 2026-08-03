# Read-Only Delegate Scratch Prompt Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Tell read-only delegates in their rendered system prompt that their per-session scratch directory is the only writable location.

**Architecture:** Reuse the existing `sandboxPromptLine` environment fact, which already carries the provisioned scratch path into both root and subagent prompts. Add read-only-specific wording at that single rendering boundary; do not alter sandbox grants or the behavior of other sandbox modes.

**Tech Stack:** Go, embedded text templates, package tests.

## Global Constraints

- Keep the existing per-session scratch grant and path disclosure unchanged.
- Do not imply that workspace-write or restricted delegates have read-only write limits.
- Test the rendered prompt contract through real prompt assembly, not a large snapshot.
- Preserve unrelated changes already present in the shared worktree.

---

### Task 1: Add read-only scratch-directory guidance to the environment prompt

**Files:**
- Modify: `agent/session_prompts.go:222-240` (`sandboxPromptLine`)
- Test: `agent/sandbox_delegate_create_test.go:266-299` (sandbox prompt tests)

**Interfaces:**
- Consumes: `execenv.LocalExecutionEnvironment.Sandbox.Mode` and `Wrapper.SessionTmp()`.
- Produces: The existing sandbox prompt line with an additional read-only-only write-scope sentence.

- [x] **Step 1: Write the failing test**

Add a test that enables a real read-only sandbox and asserts the rendered sandbox prompt names the scratch directory and says that read-only delegates may write only there.

- [x] **Step 2: Run the focused test to verify it fails**

Run:

```sh
go test ./agent -run '^TestSandboxPromptLineReadOnlyDelegateScratchGuidance$' -count=1
```

Expected: FAIL because the current prompt names the scratch directory but does not state that read-only delegates are limited to it.

- [x] **Step 3: Implement the minimal prompt change**

When `sandboxPromptLine` is rendering an enforced `ModeReadOnly` environment with a provisioned scratch directory, append a sentence stating that read-only delegates may write only inside that directory and that writes elsewhere are denied. Leave the existing mode/network/path text and all other modes unchanged.

- [x] **Step 4: Run the focused and neighboring prompt tests**

Run:

```sh
go test ./agent -run '^(TestSandboxPromptLine|TestSandboxPromptLineReadOnlyDelegateScratchGuidance)$' -count=1
```

Expected: PASS with no warnings.

- [x] **Step 5: Exercise the prompt with a literal delegate**

Add an integration-style test using a real child `Session`, a scripted LLM
adapter, and the real `write_file` tool. The deliberately simple model should
choose the scratch path only when it finds the explicit read-only write-boundary
sentence in its rendered system prompt, then write one file there and finish
with `communicate`.

Run:

```sh
go test ./agent -run '^TestReadOnlyDelegateDumbModelWritesOnlyToPromptNamedScratch$' -count=1
```

Expected: PASS. Removing the new sentence should make the dumb model fall back
to a worktree-relative path and fail the test, proving the wording is usable by
an actual delegate rather than only matching a unit-test string.

- [x] **Step 6: Commit**

```sh
git add docs/superpowers/plans/2026-08-02-read-only-delegate-scratch-prompt.md agent/session_prompts.go agent/sandbox_delegate_create_test.go
git commit -m "feat(agent): explain read-only delegate scratch writes"
```
