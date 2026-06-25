# Resume Hook Ordering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resume `SessionStart` hook output is delivered exactly once with the first accepted post-resume user input, never during restore or autonomous notification turns.

**Architecture:** Add a session-local pending resume `SessionStart` kind guarded by `Session.mu`. On restore/plugin initialization, record pending resume hooks instead of running them immediately. Drain that pending state only from `acceptUserInput`, before `UserPromptSubmit` hooks and before the current user prompt's first model request; leave startup `SessionStart` behavior unchanged.

**Tech Stack:** Go, Serf agent package, plugin hook runner, scripted `llm.ProviderAdapter` tests, `go test`.

## Global Constraints

- Preserve `SessionStart` hooks with matcher/source `resume`.
- Do not deliver resume hook injected context or user messages during restore.
- Deliver resume hook output exactly once with the first real post-resume user input.
- Do not let notification, continuation, watch, or other autonomous entries consume resume hook output.
- Do not change hook matcher names or plugin configuration syntax.
- Do not change startup-session hook behavior except where shared helper code needs to distinguish resume from startup.
- Do not change durable job notification semantics in this implementation.
- Default tests must be deterministic and must not depend on provider credentials, network access, quota, current model behavior, or ambient developer machine state.
- Use scripted providers at the LLM boundary and exercise real Serf code below it.

---

## File Structure

- `agent/session.go`
  - Add the `pendingSessionStartKind *plugin.SessionStartKind` field near plugin hook state or other `mu`-guarded session state.
  - The field is guarded by `Session.mu`.

- `agent/session_init.go`
  - Change restore initialization so resume `SessionStart` hooks become pending instead of immediately delivered.
  - Add helper methods to set/take/drain pending `SessionStart` hooks.
  - Keep startup `NewSession` hook behavior immediate.
  - Update deferred restore side effects so they do not run resume `SessionStart` hooks immediately.

- `agent/session_lifecycle.go`
  - Drain pending resume `SessionStart` hooks only inside `acceptUserInput`, after a user input has been accepted and before `UserPromptSubmit` hooks.
  - Do not change `acceptNotificationInput`, `acceptContinuationInput`, or watch-delivery behavior to drain pending hooks.

- `agent/session_resume_hooks_test.go`
  - Create focused regression tests for restore/no immediate injection, notification non-drain, first user ordering, exactly-once delivery, and rejected user input preserving pending state.

---

### Task 1: Add pending resume hook state and helpers

**Files:**
- Modify: `agent/session.go`
- Modify: `agent/session_init.go`

**Interfaces:**
- Consumes: `plugin.SessionStartKind`, `s.hookRunner.RunSessionStartFor(ctx, input, kind)`, `s.deliverHookContext(text)`, `s.deliverHookUserMessage(text)`.
- Produces:
  - `func (s *Session) deferSessionStartHooks(kind plugin.SessionStartKind)`
  - `func (s *Session) takePendingSessionStartKind() (plugin.SessionStartKind, bool)`
  - `func (s *Session) drainPendingSessionStartHooks(ctx context.Context)`

- [ ] **Step 1: Add the pending field to `Session`**

In `agent/session.go`, add this field in the plugin-provided components block after `hookRunner`:

```go
	// pendingSessionStartKind defers restore SessionStart hook execution until the
	// first accepted real user turn. Guarded by mu.
	pendingSessionStartKind *plugin.SessionStartKind
```

- [ ] **Step 2: Add pending-state helper methods**

In `agent/session_init.go`, immediately above `func (s *Session) runSessionStartHooks(...)`, add:

```go
func (s *Session) deferSessionStartHooks(kind plugin.SessionStartKind) {
	if s == nil || kind == "" {
		return
	}
	k := kind
	s.mu.Lock()
	s.pendingSessionStartKind = &k
	s.mu.Unlock()
}

func (s *Session) takePendingSessionStartKind() (plugin.SessionStartKind, bool) {
	if s == nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingSessionStartKind == nil {
		return "", false
	}
	kind := *s.pendingSessionStartKind
	s.pendingSessionStartKind = nil
	return kind, true
}

func (s *Session) drainPendingSessionStartHooks(ctx context.Context) {
	kind, ok := s.takePendingSessionStartKind()
	if !ok {
		return
	}
	s.runSessionStartHooksWithContext(ctx, kind)
}
```

- [ ] **Step 3: Split hook execution into context-aware helper**

Replace the existing `runSessionStartHooks` body in `agent/session_init.go` with this wrapper plus helper:

```go
func (s *Session) runSessionStartHooks(sessionStartKind plugin.SessionStartKind) {
	s.runSessionStartHooksWithContext(context.Background(), sessionStartKind)
}

func (s *Session) runSessionStartHooksWithContext(ctx context.Context, sessionStartKind plugin.SessionStartKind) {
	if s == nil || s.hookRunner == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result := s.hookRunner.RunSessionStartFor(ctx, s.hookInput(plugin.HookSessionStart), sessionStartKind)
	s.logSessionStartHookDispatch(sessionStartKind, len(result.ModelContext)+len(result.UserMessages))
	for _, m := range result.ModelContext {
		s.deliverHookContext(m)
	}
	for _, m := range result.UserMessages {
		s.deliverHookUserMessage(m)
	}
}
```

- [ ] **Step 4: Run focused compile test**

Run:

```bash
go test ./agent -run 'TestSession_PreCompactHookOnlyRunsWhenCompactionEmits' -count=1
```

Expected: PASS. If it fails to compile, fix only the helper signatures/field placement.

- [ ] **Step 5: Commit**

```bash
git add agent/session.go agent/session_init.go
git commit -m "refactor(agent): add pending session start hook state"
```

---

### Task 2: Defer resume `SessionStart` hooks during restore

**Files:**
- Modify: `agent/session_init.go`

**Interfaces:**
- Consumes from Task 1: `deferSessionStartHooks(kind plugin.SessionStartKind)`.
- Produces behavior: `RestoreSessionFromMetaWithConfig` creates a session with pending `SessionStartKindResume` hook state but no restore-time hook delivery.

- [ ] **Step 1: Change `initPlugins` dispatch decision**

In `agent/session_init.go`, replace the tail of `initPlugins`:

```go
	if !runSessionStartHooks {
		return nil
	}
	s.runSessionStartHooks(sessionStartKind)
	return nil
```

with:

```go
	if !runSessionStartHooks {
		return nil
	}
	if sessionStartKind == plugin.SessionStartKindResume {
		s.deferSessionStartHooks(sessionStartKind)
		return nil
	}
	s.runSessionStartHooks(sessionStartKind)
	return nil
```

This keeps startup/new-session hooks immediate and makes resume hooks pending.

- [ ] **Step 2: Remove immediate hook execution from deferred restore side effects**

In `agent/session_init.go`, inside `runDeferredRestoreSideEffects`, delete this line:

```go
	s.runSessionStartHooks(s.cfg.SessionStartKind)
```

Do not replace it. `initPlugins` already records pending resume hook state during restore. Deferred side effects are job/watch recovery, not hook delivery.

- [ ] **Step 3: Run focused tests**

Run:

```bash
go test ./agent -run 'TestRestoreSessionFromMetaWithConfig|TestSession_PreCompactHook' -count=1
```

Expected: PASS. If a test expects immediate resume `SessionStart` delivery, update that test in Task 3 instead of weakening the production behavior here.

- [ ] **Step 4: Commit**

```bash
git add agent/session_init.go
git commit -m "fix(agent): defer resume session start hooks"
```

---

### Task 3: Drain pending resume hooks only on accepted user input

**Files:**
- Modify: `agent/session_lifecycle.go`

**Interfaces:**
- Consumes from Task 1: `drainPendingSessionStartHooks(ctx context.Context)`.
- Produces behavior: first accepted `EntryUserInput` drains pending resume `SessionStart` hooks before `UserPromptSubmit`; autonomous entries do not drain.

- [ ] **Step 1: Insert drain point in `acceptUserInput`**

In `agent/session_lifecycle.go`, locate this block in `acceptUserInput`:

```go
	s.emit(events.EventUserInput, events.UserInputData{
		Text:   input,
		Images: userInputImagesFromAttachments(images),
		Turn:   userInputTurn,
	})
	s.appendTurn(schema.TurnUserInput, buildUserInputMessage(input, images))
	s.launchInitialPromptNamer(s.sessionCtx, input)

	// UserPromptSubmit hooks
```

Change it to:

```go
	s.emit(events.EventUserInput, events.UserInputData{
		Text:   input,
		Images: userInputImagesFromAttachments(images),
		Turn:   userInputTurn,
	})
	s.appendTurn(schema.TurnUserInput, buildUserInputMessage(input, images))
	s.launchInitialPromptNamer(s.sessionCtx, input)

	// Resume SessionStart hooks are intentionally lazy: they are recorded during
	// restore, but their model-facing output must join the first accepted real user
	// turn, never an autonomous notification/continuation/watch turn.
	s.drainPendingSessionStartHooks(ctx)

	// UserPromptSubmit hooks
```

This placement satisfies the rejected-input constraint because `ProcessInputKind` rejects closed sessions before `acceptUserInput`, and `acceptUserInput` reaches this point only after the user input has been accepted and appended.

- [ ] **Step 2: Do not modify autonomous handlers**

Confirm these functions do not call `drainPendingSessionStartHooks`:

```bash
rg 'drainPendingSessionStartHooks|func \(s \*Session\) accept(Notification|Continuation)Input' agent/session_lifecycle.go
```

Expected: one call inside `acceptUserInput`, zero calls inside `acceptNotificationInput` or `acceptContinuationInput`.

- [ ] **Step 3: Run focused compile test**

Run:

```bash
go test ./agent -run 'TestSession_Notification|TestSession_PreCompactHook' -count=1
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add agent/session_lifecycle.go
git commit -m "fix(agent): drain resume hooks on first user turn"
```

---

### Task 4: Add deterministic resume hook ordering regressions

**Files:**
- Create: `agent/session_resume_hooks_test.go`

**Interfaces:**
- Consumes from Tasks 1-3: pending resume hook state and first-user drain behavior.
- Produces tests:
  - `TestRestoreSessionDefersResumeSessionStartHooksUntilUserInput`
  - `TestRestoreSessionNotificationDoesNotDrainResumeSessionStartHooks`
  - `TestRestoreSessionStartHooksDrainOnlyOnce`
  - `TestRestoreSessionRejectedUserInputKeepsPendingResumeHooks`

- [ ] **Step 1: Create the test file with helpers**

Create `agent/session_resume_hooks_test.go` with this package/import/header and helpers:

```go
package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func newResumeHookPluginDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "resume-hook-plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("mkdir plugin dir: %v", err)
	}
	manifest := `{
  "name": "resume-hook-plugin",
  "version": "0.0.1",
  "hooks": {
    "SessionStart": [
      {
        "matcher": "resume",
        "type": "command",
        "command": "printf '{\"hookSpecificOutput\":{\"additionalContext\":\"RESUME_HOOK_CONTEXT\"}}\\n'; printf 'RESUME_HOOK_USER_MESSAGE\\n'",
        "timeout": 5
      }
    ]
  }
}`
	if err := os.WriteFile(filepath.Join(pluginDir, ".claude-plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write plugin manifest: %v", err)
	}
	return pluginDir
}

func restoredSessionWithResumeHook(t *testing.T, adapter *fakeAdapter) *Session {
	t.Helper()
	stateDir := t.TempDir()
	meta := schema.SessionMeta{
		ID:        "01KRESUMEHOOKTEST0000000000",
		ProfileID: "test",
		Model:     "gpt-5.2",
		Config: schema.ConfigSnapshot{
			PluginDirs: []string{newResumeHookPluginDir(t)},
		},
		TurnCount: 1,
	}
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := RestoreSessionFromMetaWithConfig(
		client,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		meta,
		RestoreSessionConfig{
			StateDir: stateDir,
			resumeHistory: []schema.Turn{
				schema.NewTurn(schema.TurnUserInput, llm.User("prior user task")),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("prior assistant answer")),
			},
		},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(sess.Close)
	return sess
}

func requestText(req llm.Request) string {
	parts := make([]string, 0, len(req.Messages))
	for _, msg := range req.Messages {
		parts = append(parts, msg.Text())
	}
	return strings.Join(parts, "\n---\n")
}

func sessionHistoryText(sess *Session) string {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	parts := make([]string, 0, len(sess.history))
	for _, turn := range sess.history {
		parts = append(parts, turn.Message.Text())
	}
	return strings.Join(parts, "\n---\n")
}

func requireRequestContainsInOrder(t *testing.T, req llm.Request, needles ...string) {
	t.Helper()
	text := requestText(req)
	pos := 0
	for _, needle := range needles {
		i := strings.Index(text[pos:], needle)
		if i < 0 {
			t.Fatalf("request text missing %q after offset %d; text:\n%s", needle, pos, text)
		}
		pos += i + len(needle)
	}
}

func closeAndCountSessionStartHooks(t *testing.T, sess *Session, eventsPtr *[]events.SessionEvent, mu *sync.Mutex, doneCh <-chan struct{}) int {
	t.Helper()
	sess.Close()
	<-doneCh
	mu.Lock()
	defer mu.Unlock()
	return countHookStarts(*eventsPtr, plugin.HookSessionStart)
}
```


- [ ] **Step 2: Add restore/no-immediate and first-user ordering test**

Append this test. It starts event collection immediately after restore so hook-start assertions are based on real emitted events.

```go
func TestRestoreSessionDefersResumeSessionStartHooksUntilUserInput(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			requireRequestContainsInOrder(t, req,
				"prior user task",
				"prior assistant answer",
				"RESUME_HOOK_CONTEXT",
				"current user task",
			)
			return finalResponse("done")
		},
	}}
	sess := restoredSessionWithResumeHook(t, adapter)
	eventsPtr, mu, doneCh := collectEvents(sess)

	preHistory := sessionHistoryText(sess)
	if strings.Contains(preHistory, "RESUME_HOOK_CONTEXT") || strings.Contains(preHistory, "RESUME_HOOK_USER_MESSAGE") {
		t.Fatalf("resume hook output was injected during restore; history:\n%s", preHistory)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "current user task", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("adapter requests = %d, want 1", got)
	}
	if got := closeAndCountSessionStartHooks(t, sess, eventsPtr, mu, doneCh); got != 1 {
		t.Fatalf("SessionStart hook starts after first user input = %d, want 1", got)
	}
}
```

- [ ] **Step 3: Add notification non-drain test**

Append this test. It proves an `EntryNotification` turn with pending steering can make a model request without consuming pending resume hooks.

```go
func TestRestoreSessionNotificationDoesNotDrainResumeSessionStartHooks(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			text := requestText(req)
			if strings.Contains(text, "RESUME_HOOK_CONTEXT") || strings.Contains(text, "RESUME_HOOK_USER_MESSAGE") {
				t.Fatalf("notification request drained resume hooks; text:\n%s", text)
			}
			if !strings.Contains(text, "notification steering without resume hooks") {
				t.Fatalf("notification request missing steering text; text:\n%s", text)
			}
			return finalResponse("notification done")
		},
		func(req llm.Request) llm.Response {
			requireRequestContainsInOrder(t, req, "RESUME_HOOK_CONTEXT", "first real user")
			return finalResponse("user done")
		},
	}}
	sess := restoredSessionWithResumeHook(t, adapter)
	eventsPtr, mu, doneCh := collectEvents(sess)
	sess.Steer("notification steering without resume hooks")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("adapter requests after notification = %d, want 1", got)
	}
	if _, err := sess.ProcessInput(ctx, "first real user", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := len(adapter.Requests()); got != 2 {
		t.Fatalf("adapter requests after user = %d, want 2", got)
	}
	if got := closeAndCountSessionStartHooks(t, sess, eventsPtr, mu, doneCh); got != 1 {
		t.Fatalf("SessionStart hook starts after notification plus user input = %d, want 1", got)
	}
}
```

- [ ] **Step 4: Add exactly-once test**

Append this test:

```go
func TestRestoreSessionStartHooksDrainOnlyOnce(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			requireRequestContainsInOrder(t, req, "RESUME_HOOK_CONTEXT", "first user")
			return finalResponse("first done")
		},
		func(req llm.Request) llm.Response {
			text := requestText(req)
			if strings.Count(text, "RESUME_HOOK_CONTEXT") != 1 {
				t.Fatalf("resume hook context count across second request = %d, want 1; text:\n%s", strings.Count(text, "RESUME_HOOK_CONTEXT"), text)
			}
			if !strings.Contains(text, "second user") {
				t.Fatalf("second request missing second user prompt; text:\n%s", text)
			}
			return finalResponse("second done")
		},
	}}
	sess := restoredSessionWithResumeHook(t, adapter)
	eventsPtr, mu, doneCh := collectEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "first user", nil); err != nil {
		t.Fatalf("first ProcessInput: %v", err)
	}
	if _, err := sess.ProcessInput(ctx, "second user", nil); err != nil {
		t.Fatalf("second ProcessInput: %v", err)
	}
	if got := closeAndCountSessionStartHooks(t, sess, eventsPtr, mu, doneCh); got != 1 {
		t.Fatalf("SessionStart hook starts across two user inputs = %d, want 1", got)
	}
}
```

- [ ] **Step 5: Add rejected-input preservation test**

Append this test:

```go
func TestRestoreSessionRejectedUserInputKeepsPendingResumeHooks(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			requireRequestContainsInOrder(t, req, "RESUME_HOOK_CONTEXT", "accepted user")
			return finalResponse("done")
		},
	}}
	sess := restoredSessionWithResumeHook(t, adapter)
	eventsPtr, mu, doneCh := collectEvents(sess)

	if err := sess.Enqueue(context.Background(), ""); err == nil {
		t.Fatal("empty enqueue succeeded, want rejection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "accepted user", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("adapter requests = %d, want 1", got)
	}
	if got := closeAndCountSessionStartHooks(t, sess, eventsPtr, mu, doneCh); got != 1 {
		t.Fatalf("SessionStart hook starts after rejected enqueue plus accepted input = %d, want 1", got)
	}
}
```

This uses the public rejected-input path `Enqueue(context.Background(), "")`, which returns `queue: text or images required` before any accepted turn.

- [ ] **Step 6: Run tests to verify they fail before implementation if running TDD from scratch**

When this task is executed before Tasks 1-3, run:

```bash
go test ./agent -run 'TestRestoreSession(DefersResumeSessionStartHooksUntilUserInput|NotificationDoesNotDrainResumeSessionStartHooks|StartHooksDrainOnlyOnce|RejectedUserInputKeepsPendingResumeHooks)' -count=1
```

Expected before implementation: at least `TestRestoreSessionDefersResumeSessionStartHooksUntilUserInput` fails because resume hooks run during restore or never drain. After Tasks 1-3, expected result is PASS.

- [ ] **Step 7: Run focused regression tests**

Run:

```bash
go test ./agent -run 'TestRestoreSession(DefersResumeSessionStartHooksUntilUserInput|NotificationDoesNotDrainResumeSessionStartHooks|StartHooksDrainOnlyOnce|RejectedUserInputKeepsPendingResumeHooks)' -count=1 -v
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add agent/session_resume_hooks_test.go
git commit -m "test(agent): cover deferred resume session start hooks"
```

---

### Task 5: Final verification and cleanup

**Files:**
- Verify only: `agent/session.go`, `agent/session_init.go`, `agent/session_lifecycle.go`, `agent/session_resume_hooks_test.go`

**Interfaces:**
- Consumes: all previous task outputs.
- Produces: verified branch ready for review.

- [ ] **Step 1: Run focused package tests**

Run:

```bash
go test ./agent -run 'TestRestoreSession|TestSession_Notification|TestSession_PreCompactHook|TestSession_Compact' -count=1
```

Expected: PASS.

- [ ] **Step 2: Run full agent package tests**

Run:

```bash
go test ./agent -count=1
```

Expected: PASS.

- [ ] **Step 3: Run broader deterministic tests**

Run:

```bash
go test ./... -count=1
```

Expected: PASS. If the command is promoted to a background job by the session timeout, wait for the completion notification and inspect the transcript before reporting the result.

- [ ] **Step 4: Inspect final diff**

Run:

```bash
git status --short
git diff --stat HEAD~3..HEAD
git log --oneline -5
```

Expected:

- only intended files are changed/committed;
- unrelated pre-existing hub renderer files remain unstaged if still present;
- commits include the implementation and tests from this plan.

- [ ] **Step 5: Report verification evidence**

Final report must include:

```text
Implemented deferred resume SessionStart hook delivery.
Commits: <hashes>
Tests: <commands and PASS/FAIL results>
Unrelated worktree changes left untouched: cmd/serf-hub/assets/renderer.js, cmd/serf-hub/jstest/test-renderer-notifications.js (if still modified)
```

---

## Self-Review Notes

- Spec coverage: restore-time non-injection is Task 2 and Task 4 test 1; notification non-drain is Task 3 and Task 4 test 2; first-user ordering is Task 3 and Task 4 tests 1/3; exactly-once delivery is Task 1 take semantics plus Task 4 test 3; rejected-input preservation is Task 3 placement plus Task 4 test 4; startup unchanged is Task 2 branch plus focused tests.
- Placeholder scan: no TBD/TODO/FIXME/implement-later/fill-in placeholders remain.
- Type consistency: helper names are consistent across tasks: `deferSessionStartHooks`, `takePendingSessionStartKind`, `drainPendingSessionStartHooks`, `runSessionStartHooksWithContext`.
