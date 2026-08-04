# Always-Waking `delegate_send` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove `on_idle` from `delegate_send` so every valid message wakes/resumes an idle delegate or steers a running one.

**Architecture:** Keep the existing `sendDelegateMessage` routing and resume/restore machinery. Remove only the idle-policy input and validation: after target ownership and resumability checks, all idle delegates enter the existing resume path. Synchronize the public tool definition, user documentation, deterministic tests, and fuzz/seed fixtures with the new contract.

**Tech Stack:** Go, standard `testing` package, JSON-schema-like tool definitions, Markdown documentation, existing Serf delegate/session test harness.

## Global Constraints

- Default tests must be deterministic and must not require provider credentials, network access, quota, current model behavior, or ambient machine state.
- Use the existing Serf plumbing test harness and external-boundary fakes; do not mock Serf internals to manufacture a pass.
- Preserve delegate ownership, restore validation, disposal protections, watch routing, and `max_wait_ms` behavior.
- Do not retain a hidden compatibility alias for `on_idle`; the public argument is removed.
- Keep unrelated pre-existing worktree changes untouched.

---

### Task 1: Remove idle-policy plumbing from the send path

**Files:**
- Modify: `agent/job_delegate.go` (`classifyDelegateSendTarget`, `sendDelegateMessage`)
- Modify: `agent/session_tools_jobs.go` (`delegateSendTool` argument construction)
- Test: `agent/job_delegate_send_test.go` (existing idle-default regression coverage)

**Interfaces:**
- Consumes: existing `sendMessageArgs`, delegate records, and `sendDelegateMessage` routing.
- Produces: `delegate_send` calls with no `on_idle` field classify and route identically for running delegates and through the existing resume/restore path for idle resumable delegates.

- [ ] **Step 1: Write the failing regression test**

  Update the existing test named `TestDelegateSendIdleDefaultFailsAndOnIdleStartResumes` (or rename it to reflect the new contract) so the no-`on_idle` call is expected to start/resume the idle delegate and deliver its message. Remove the test's explicit `on_idle="start"` success branch and add a separate assertion that the public tool call with an obsolete `on_idle` value is rejected by the argument contract if the existing harness validates unknown fields. Keep the test deterministic and preserve checks for the started job/result.

  Add or retain direct coverage for `sendDelegateMessage` with `sendMessageArgs{Target: delegateID, Message: "..."}` and no idle policy, asserting `Action == "started"` for an idle resumable delegate.

- [ ] **Step 2: Run the focused test to verify it fails**

  Run:

  ```sh
  go test ./agent -run 'TestDelegateSendIdleDefault' -count=1 -v
  ```

  Expected: FAIL because the current implementation defaults an omitted policy to `fail` and returns `target_idle`.

- [ ] **Step 3: Remove the policy from argument construction and validation**

  In `delegateSendTool`, stop populating `sendMessageArgs.OnIdle`. In `sendDelegateMessage`, remove the local defaulting of an empty `OnIdle` to `fail`. In `classifyDelegateSendTarget`, remove the `onIdle` parameter and the validation that accepts only `start` or `fail`; update its callers and comments accordingly.

  Delete the idle-failure branch:

  ```go
  if onIdle == "fail" {
      return sendMessageFailed(target, fmt.Errorf("target_idle: delegate %q is idle; pass on_idle=\"start\" to start the next job", target))
  }
  ```

  Leave the existing code below that branch in place so retained and runtime-lost idle delegates use the established restore/resume path. Remove or update any now-unused `OnIdle` field only if compilation proves it has no remaining legitimate callers; do not refactor unrelated message arguments.

- [ ] **Step 4: Run the focused tests to verify they pass**

  Run:

  ```sh
  go test ./agent -run 'TestDelegateSendIdleDefault|TestDelegateSend' -count=1
  ```

  Expected: PASS, with no `target_idle` expectation remaining for the normal resumable idle path.

- [ ] **Step 5: Commit the focused implementation and regression test**

  ```sh
  git add agent/job_delegate.go agent/session_tools_jobs.go agent/job_delegate_send_test.go
  git commit -m "fix: make delegate sends wake idle delegates"
  ```

### Task 2: Remove `on_idle` from the public tool contract

**Files:**
- Modify: `agent/internal/tool/definitions.go` (`DefDelegateSend`)
- Test: `agent/internal/tool/definitions_test.go` (delegate-send schema/description assertions)
- Modify: `agent/session_tools_jobs_stop_delegate_test.go` (tool contract expectations, if this file asserts the argument list)

**Interfaces:**
- Consumes: the runtime behavior from Task 1.
- Produces: a `delegate_send` definition whose required fields remain `to` and `message`, whose optional `max_wait_ms` remains available, and which does not expose `on_idle`.

- [ ] **Step 1: Add the failing schema assertion**

  In the existing delegate-send definition test, assert that `properties["on_idle"]` is absent and that the description says an idle delegate is started/resumed automatically. Update any test that currently requires `on_idle` to expect only the supported optional fields.

- [ ] **Step 2: Run the definition tests to verify they fail**

  Run:

  ```sh
  go test ./agent/internal/tool -run 'Test.*DelegateSend|Test.*Tool.*Definition' -count=1 -v
  ```

  Expected: FAIL because the current schema still includes and describes `on_idle`.

- [ ] **Step 3: Update the tool definition**

  Remove the `on_idle` property from `DefDelegateSend().Parameters`. Rewrite the description so it states that messages steer running delegates and automatically start the next job for idle delegates. Keep existing target restrictions, `max_wait_ms` semantics, and output behavior documented.

- [ ] **Step 4: Run definition and focused agent tests**

  Run:

  ```sh
  go test ./agent/internal/tool ./agent -run 'Test.*DelegateSend|TestDelegateSend' -count=1
  ```

  Expected: PASS.

- [ ] **Step 5: Commit the public contract change**

  ```sh
  git add agent/internal/tool/definitions.go agent/internal/tool/definitions_test.go agent/session_tools_jobs_stop_delegate_test.go
  git commit -m "feat: remove on_idle from delegate_send schema"
  ```

### Task 3: Synchronize documentation and deterministic fixtures

**Files:**
- Modify: `docs/job-control.md` (delegate-send semantics, legacy mapping, notification examples)
- Modify: `agent/session_tools_jobs_seed100_more_test.go` (seed replay names/expectations)
- Modify: `agent/job_delegate_send_fuzz_test.go`, `agent/delegate_seqfuzz_test.go`, and other focused fixtures found by search (remove obsolete policy inputs and `target_idle` expectations)
- Test: affected existing deterministic tests in `agent/`

**Interfaces:**
- Consumes: the final runtime and tool schema contract from Tasks 1–2.
- Produces: no user-facing documentation or deterministic fixture that claims `on_idle` is supported or that omitted policy fails for an otherwise resumable delegate.

- [ ] **Step 1: Locate stale contract references**

  Run:

  ```sh
  rg -n 'on_idle|target_idle|IdleDefaultFails' agent docs --glob '*.go' --glob '*.md'
  ```

  Classify each match as runtime code, public documentation, an intentional negative test for obsolete arguments, or fuzz/seed input. Preserve negative tests only where they verify the repository's generic unknown-argument behavior; remove policy-specific routing expectations.

- [ ] **Step 2: Update documentation**

  In `docs/job-control.md`, state that `delegate_send(to=<delegate_id>, message=...)` steers a running delegate or starts/resumes an idle delegate automatically. Remove `on_idle` from parameter tables, examples, legacy mappings, and notification text. Explain that genuine target/resumability errors remain possible, but there is no fail-fast idle option.

- [ ] **Step 3: Update deterministic seeds and fuzz fixtures**

  Replace generated/seeded calls that pass `on_idle="start"` with calls that omit the field. Remove branches whose only purpose is validating `on_idle="fail"` or the old `target_idle` result. Keep invalid-target, disposal, ownership, and wait-timeout coverage intact. Follow the existing fixture conventions; do not broaden fuzz search or add nondeterministic timing.

- [ ] **Step 4: Run the affected deterministic tests**

  Run:

  ```sh
  go test ./agent/... -run 'TestDelegateSend|Test.*Job.*Contract|Test.*Seed|Test.*Fuzz' -count=1
  ```

  Expected: PASS, or explicit compile/test diagnostics identifying any remaining stale fixture that must be updated before proceeding.

- [ ] **Step 5: Commit documentation and fixture synchronization**

  ```sh
  git add docs/job-control.md agent
  git commit -m "docs: document always-waking delegate sends"
  ```

### Task 4: Full verification and review

**Files:**
- No intended new files; inspect all changes from Tasks 1–3.

**Interfaces:**
- Consumes: completed implementation, schema, docs, and fixture commits.
- Produces: verified clean focused behavior and a report of any broader gate limitations.

- [ ] **Step 1: Review the diff and stale references**

  Run:

  ```sh
  git diff main...HEAD --check
  git diff main...HEAD --stat
  rg -n 'on_idle|target_idle|pass on_idle' agent docs --glob '*.go' --glob '*.md'
  git status --short
  ```

  Expected: only intentional obsolete-argument negative tests or historical notes remain; no stale public contract or runtime branch remains.

- [ ] **Step 2: Run focused package gates**

  Run:

  ```sh
  go test ./agent/... -count=1
  go test ./agent/internal/tool/... -count=1
  ```

  Expected: PASS with deterministic tests and no provider/network dependency.

- [ ] **Step 3: Run repository verification appropriate to the change**

  Run:

  ```sh
  make lint
  ROOT_FULL=1 WEB=0 make test
  ```

  Expected: PASS. If a gate fails because of a pre-existing dirty-worktree issue or unavailable optional tooling, capture the exact command, failure, and limitation rather than suppressing it.

- [ ] **Step 4: Inspect commits and final state**

  Run:

  ```sh
  git log --oneline main..HEAD
  git status --short
  ```

  Expected: focused commits only, no accidental modifications to pre-existing unrelated files, and a clean working tree.
