# Session Memory Implementation Plan

> Superseded: the session memory scope was removed (2026-10-06); see docs/product/memory.md.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a third memory scope, `session`, that holds knowledge about the current work. The root session owns it, delegates can only read it, forks copy it, and the prompt tells agents to use it and to promote lasting lessons out of it.

**Architecture:** The scope is a third case in the existing memory machinery in `agent/session_memory.go` and `agent/session_tools_memory.go`. Its storage is `<state-root>/memory/sessions/<root-session-id>/`, behind the same confined roots, tools, index projection and compaction refresh as the other scopes. A forked session seeds its session memory from its parent's directory the first time it opens the scope.

**Tech Stack:** Go (`agent` package), Go `text/template` system prompt, the memory lab harness (git-ignored, `tools/prompt-eval/results/memory-lab/`).

**Spec:** `docs/superpowers/specs/2026-10-05-session-memory-design.md`

## Global Constraints

- Scope names are exactly `personal`, `project` and `session`. Projection message names are `memory_personal`, `memory_project` and `memory_session`.
- Session storage path: `<MemoryStateRoot>/memory/sessions/<session id>/`. The id is the root session's id: `s.id` for a root session, `s.delegateRootSessionID` for a delegate (`s.depth > 0`).
- Delegate write refusal text, verbatim: `session memory belongs to the root session; report this to your parent instead`
- `--disable-memory` disables session memory too. There is no separate switch.
- Guidance names only tools and scopes the session can use. `canInstructTool` is the gate.
- Run gofmt as `$(go env GOROOT)/bin/gofmt`. Before pushing, run `golangci-lint run --allow-serial-runners ./agent/...`, plus `go vet ./agent/`, `go vet -tags evenerfuzz ./agent/` and `GOOS=windows go vet ./agent/`.
- Default tests stay deterministic: scripted providers only, no network.

## Review Focus

1. **A delegate whose root id is empty** (for example a delegate restored standalone). Session scope must refuse cleanly with "session memory is not bound". It must never fall back to the delegate's own id and quietly give it a private writable session memory. The test lives in Task 1.
2. **A fork whose parent has no session memory directory.** The child must start with an empty, usable scope and no warning. The test lives in Task 3.
3. **A fork whose child directory already exists**, for example on a second restore. The copy must not run again, because a second copy would pull in the parent's later notes. The test lives in Task 3.
4. **A session id with path characters**, defending against a hostile or corrupt meta. Ids reach the path only after `schema.ValidateSessionID`, and resolution refuses otherwise. The test lives in Task 1.
5. **Restored history that projected all three scopes.** The restore seeding must mark `session` as observed, so an empty or missing session index after resume supersedes the old one instead of being skipped. The test lives in Task 2.

---

### Task 1: Session scope resolution, storage and delegate write refusal

**Files:**
- Modify: `agent/session_memory.go` (`memoryEnvironment` scope switch, around lines 134–147; add `memorySessionID`)
- Modify: `agent/session_tools_memory.go` (`memoryFileArgs`)
- Modify: `agent/internal/tool/definitions.go:14` (scope enum)
- Test: `agent/session_memory_test.go`

**Interfaces:**
- Produces: `func (s *Session) memorySessionID() string`. It returns the root session id, or `""` when a delegate has no root id.
- Produces: `func memoryScopes() []string`. It returns `[]string{"personal", "project", "session"}`, in this order. Task 2 uses it.
- Produces: `const memorySessionReadOnly = "session memory belongs to the root session; report this to your parent instead"`

- [ ] **Step 1: Write the failing tests**

Append to `agent/session_memory_test.go`:

```go
func TestMemorySessionScopeRootWrites(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{MemoryStateRoot: root, MemoryProjectID: "fixture-project"}))
	if _, err := s.execMemoryWrite(context.Background(), nil, map[string]any{"scope": "session", "file_path": "MEMORY.md", "content": "opaque-session-11\n"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "memory", "sessions", s.id, "MEMORY.md"))
	if err != nil || string(got) != "opaque-session-11\n" {
		t.Fatalf("bytes=%q err=%v", got, err)
	}
}

func TestMemorySessionScopeDelegateReadsButCannotWrite(t *testing.T) {
	t.Parallel()
	workspace, project := memoryGitFixture(t)
	root := t.TempDir()
	s := newSession(t, withDir(workspace), withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root, MemoryProjectID: project.ID, Project: project, testOnly: testConfig{sandboxProber: bwrapCapableProber(workspace), disableDelegateIdleRelease: true}}), withSteps(func(llm.Request) llm.Response { return finalResponse("child finished") }))
	memorySeed(t, root, filepath.Join("sessions", s.id), "opaque-root-session-12\n")
	res := s.createDelegate(context.Background(), delegateArgs{Task: "fixture child", AgentType: "explorer", DelegationAllowance: new(0)})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	child := memoryWaitChild(t, s, res.ChildSessionID)
	read, err := child.sess.execMemoryRead(context.Background(), nil, map[string]any{"scope": "session", "file_path": "MEMORY.md"})
	if err != nil || !strings.Contains(fmt.Sprint(read), "opaque-root-session-12") {
		t.Fatalf("delegate read=%v err=%v", read, err)
	}
	for _, call := range []func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error){child.sess.execMemoryWrite, child.sess.execMemoryEdit, child.sess.execMemoryDelete} {
		_, err := call(context.Background(), nil, map[string]any{"scope": "session", "file_path": "MEMORY.md", "content": "x", "old_string": "opaque", "new_string": "y"})
		if err == nil || err.Error() != memorySessionReadOnly {
			t.Fatalf("delegate session write err=%v", err)
		}
	}
	got, _ := os.ReadFile(filepath.Join(root, "memory", "sessions", s.id, "MEMORY.md"))
	if string(got) != "opaque-root-session-12\n" {
		t.Fatalf("root session memory changed: %q", got)
	}
}

func TestMemorySessionScopeUnboundDelegateRefuses(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(SessionConfig{MemoryStateRoot: t.TempDir()}))
	s.depth = 1
	s.delegateRootSessionID = ""
	if _, err := s.execMemoryRead(context.Background(), nil, map[string]any{"scope": "session", "file_path": "MEMORY.md"}); err == nil || !strings.Contains(err.Error(), "session memory is not bound") {
		t.Fatalf("err=%v", err)
	}
	s.delegateRootSessionID = "../escape"
	if _, err := s.execMemoryRead(context.Background(), nil, map[string]any{"scope": "session", "file_path": "MEMORY.md"}); err == nil || !strings.Contains(err.Error(), "session memory is not bound") {
		t.Fatalf("hostile id err=%v", err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./agent -run 'TestMemorySessionScope' -count=1`
Expected: build failure on `memorySessionReadOnly`. Once the constant exists, the failure is `unknown memory scope "session"`.

- [ ] **Step 3: Implement**

In `agent/session_memory.go`, add near `nativeMemoryToolNames`:

```go
const memorySessionReadOnly = "session memory belongs to the root session; report this to your parent instead"

// memoryScopes lists every memory scope in projection order.
func memoryScopes() []string { return []string{"personal", "project", "session"} }

// memorySessionID names the session memory this session uses: its own for a
// root session, its root's for a delegate. A delegate without a root id gets
// none rather than a private writable scope.
func (s *Session) memorySessionID() string {
	if s.depth > 0 {
		return s.delegateRootSessionID
	}
	return s.id
}
```

In `memoryEnvironment`, add a case after `case "project":`:

```go
	case "session":
		id := s.memorySessionID()
		if id == "" || schema.ValidateSessionID(id) != nil {
			return nil, errors.New("session memory is not bound")
		}
		relative = filepath.Join("memory", "sessions", id)
```

Add `"primeradiant.com/evener/agent/schema"` to the imports, if it isn't already there.

In `agent/session_tools_memory.go` `memoryFileArgs`, before `acquireMemoryEnvironment`:

```go
	if stringArg(args, "scope") == "session" && s.depth > 0 && operation != "read" && operation != "search" {
		return nil, nil, nil, errors.New(memorySessionReadOnly)
	}
```

In `agent/internal/tool/definitions.go:14`:

```go
	props["scope"] = map[string]any{"type": "string", "enum": []any{"personal", "project", "session"}}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./agent ./agent/internal/tool -run 'Memory' -count=1`
Expected: PASS. If a tool-definition test pins the old two-value enum, update its expectation to the three-value enum. Don't delete the assertion.

- [ ] **Step 5: Commit**

```bash
git add agent/session_memory.go agent/session_tools_memory.go agent/internal/tool/definitions.go agent/session_memory_test.go
git commit -m "agent: session memory scope, owned by the root session"
```

---

### Task 2: Session index projection, restore seeding and delegate framing

**Files:**
- Modify: `agent/session_memory.go` (`maybeAppendMemoryContext` around lines 426–490, `restoreMemoryProjection` around 405–422, `appendMemoryProjection` around 360–392)
- Test: `agent/session_memory_test.go`

**Interfaces:**
- Consumes: `memoryScopes()` and `memorySessionID()` from Task 1.
- Produces: the projection message named `memory_session`. For delegates, its block text contains `memorySessionProjectionReadOnly`.

- [ ] **Step 1: Write the failing tests**

```go
func TestMemorySessionIndexProjected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var body string
	s := newSession(t, withConfig(SessionConfig{MemoryStateRoot: root}), withSteps(func(req llm.Request) llm.Response {
		_, body, _ = memoryRequestIndex(t, req, "session")
		return finalResponse("done")
	}))
	memorySeed(t, root, filepath.Join("sessions", s.id), "opaque-session-index-21\n")
	if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if body != "opaque-session-index-21\n" {
		t.Fatalf("session index body=%q", body)
	}
}

func TestMemorySessionRestoreSeedsObservedScope(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(SessionConfig{MemoryStateRoot: t.TempDir()}))
	msg := llm.User("x")
	msg.Name = "memory_session"
	s.restoreMemoryProjection([]schema.Turn{{Kind: schema.TurnMemoryContext, Message: msg}})
	if !s.memoryEverProjected["session"] {
		t.Fatal("restored session observation not seeded")
	}
}
```

`memoryRequestIndex(t, req, scope)` is the existing helper that finds a scope's projection in a request. If it only accepts `personal` and `project`, extend it to match any `memory_<scope>` name instead.

For the delegate framing, add to `TestMemorySessionScopeDelegateReadsButCannotWrite` from Task 1, after the child is waited on:

```go
	child.sess.appendMemoryProjection(memoryProjection{Scope: "session", Status: "current", Content: "opaque-root-session-12\n"})
	if text := lastMemoryContextText(t, child.sess, "memory_session"); !strings.Contains(text, memorySessionProjectionReadOnly) {
		t.Fatalf("delegate session projection lacks read-only framing: %q", text)
	}
```

`lastMemoryContextText` is a new test helper. It scans `child.sess.history` from the end for the newest `schema.TurnMemoryContext` turn whose `Message.Name` matches, and returns `Message.Text()`. Write it next to `memoryContextCount`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./agent -run 'TestMemorySession' -count=1`
Expected: FAIL. The session index is never projected, and `memorySessionProjectionReadOnly` is undefined.

- [ ] **Step 3: Implement**

In `maybeAppendMemoryContext`, replace both `[]string{"personal", "project"}` loops with `memoryScopes()`. Extend the unavailable condition so the session scope is revoked when it isn't bound:

```go
		if !s.memoryContextEnabled() || (scope == "project" && s.cfg.MemoryProjectID == "") || (scope == "session" && s.memorySessionID() == "") {
```

Replace the two-channel wait with a sequential wait under the same shared deadline. That's equivalent, because the timer bounds the total wait:

```go
	for _, scope := range memoryScopes() {
		flight := flights[scope]
		if flight == nil {
			continue
		}
		select {
		case <-flight.done:
		case <-timer.C():
			goto publish
		case <-ctx.Done():
			goto publish
		case <-closed:
			return
		}
	}
```

Then delete the now-unused `personalDone`/`projectDone` variables and their assignments.

In `restoreMemoryProjection`, loop over `memoryScopes()`, and break only once every scope is seeded:

```go
		if turn.Kind == schema.TurnMemoryContext {
			for _, scope := range memoryScopes() {
				if turn.Message.Name == "memory_"+scope {
					s.memoryEverProjected[scope] = true
				}
			}
		}
		if len(s.memoryEverProjected) == len(memoryScopes()) {
			break
		}
```

In `appendMemoryProjection`, add the delegate framing:

```go
const memorySessionProjectionReadOnly = " Session memory belongs to your root session: you can read it, not write it."
```

Then, where `block` is built, append the constant for delegates:

```go
	if p.Scope == "session" && s.depth > 0 {
		block += memorySessionProjectionReadOnly
	}
```

Insert this before `msg := llm.User(block)`. The appended sentence follows the quoted data line, so the existing `Sscanf` header parse in the tests stays valid.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./agent -run 'Memory' -count=1`
Expected: PASS, including every existing memory test. Existing tests that iterate `[]string{"personal", "project"}` over projections keep passing, because session projections are separate messages. If one counts *all* memory-context turns (`memoryContextCount`) and now sees an extra session turn, change its expected count by the session projection. Don't drop the assertion. A missing or empty session index on a fresh session is skipped by the existing "never projected" rule, so most counts don't change.

- [ ] **Step 5: Commit**

```bash
git add agent/session_memory.go agent/session_memory_test.go
git commit -m "agent: project and restore the session memory index"
```

---

### Task 3: Fork and resume-with seed session memory from the parent

**Files:**
- Modify: `agent/session.go` (add a field next to `restoredMetaParentSessionID`)
- Modify: `agent/session_init.go` (around line 1347, where the restored meta is read)
- Modify: `agent/session_memory.go` (`memoryEnvironment`, before `NewConfinedFileRoot`)
- Create: `agent/session_memory_fork.go`
- Test: `agent/session_memory_fork_test.go`

**Interfaces:**
- Consumes: `memorySessionID()` from Task 1.
- Produces: `func (s *Session) seedForkedSessionMemory()`. Session-scope environment setup calls it. It is idempotent.

- [ ] **Step 1: Write the failing tests**

Create `agent/session_memory_fork_test.go`:

```go
package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
)

func forkWithSessionMemory(t *testing.T, seed bool) (root, parentID string, child *Session) {
	t.Helper()
	root, history, workspace := t.TempDir(), t.TempDir(), t.TempDir()
	p := newSession(t, withDir(workspace), withConfig(SessionConfig{StateDir: history, MemoryStateRoot: root}), withSteps(func(llm.Request) llm.Response { return finalResponse("done") }))
	if _, err := p.ProcessInput(context.Background(), "parent turn", nil); err != nil {
		t.Fatal(err)
	}
	if seed {
		memorySeed(t, root, filepath.Join("sessions", p.id), "opaque-parent-31\n")
	}
	p.Close()
	childID, err := AsideSession(history, p.id)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := schema.LoadSessionMeta(history, childID)
	if err != nil {
		t.Fatal(err)
	}
	c, err := RestoreSessionFromMetaWithConfig(p.client, p.profile, execenv.NewLocalExecutionEnvironment(workspace), meta, RestoreSessionConfig{StateDir: history, MemoryStateRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return root, p.id, c
}

func TestMemoryForkCopiesParentSessionMemory(t *testing.T) {
	t.Parallel()
	root, parentID, c := forkWithSessionMemory(t, true)
	if _, err := c.execMemoryRead(context.Background(), nil, map[string]any{"scope": "session", "file_path": "MEMORY.md"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "memory", "sessions", c.id, "MEMORY.md"))
	if err != nil || string(got) != "opaque-parent-31\n" {
		t.Fatalf("child copy=%q err=%v", got, err)
	}
	// Divergence: a later parent change does not reach the child, even after
	// the child's environment is rebuilt.
	memorySeed(t, root, filepath.Join("sessions", parentID), "opaque-parent-later-32\n")
	c.seedForkedSessionMemory()
	got, _ = os.ReadFile(filepath.Join(root, "memory", "sessions", c.id, "MEMORY.md"))
	if string(got) != "opaque-parent-31\n" {
		t.Fatalf("child re-copied parent: %q", got)
	}
}

func TestMemoryForkWithoutParentMemoryStartsEmpty(t *testing.T) {
	t.Parallel()
	root, _, c := forkWithSessionMemory(t, false)
	if _, err := c.execMemoryWrite(context.Background(), nil, map[string]any{"scope": "session", "file_path": "MEMORY.md", "content": "opaque-child-33\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "memory", "sessions", c.id, "MEMORY.md")); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryForkCopyFailureDoesNotBlock(t *testing.T) {
	t.Parallel()
	root, parentID, c := forkWithSessionMemory(t, true)
	parentDir := filepath.Join(root, "memory", "sessions", parentID)
	if err := os.Chmod(parentDir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parentDir, 0o700) })
	seen, stop := captureEvents(c)
	if _, err := c.execMemoryWrite(context.Background(), nil, map[string]any{"scope": "session", "file_path": "MEMORY.md", "content": "opaque-child-34\n"}); err != nil {
		t.Fatalf("copy failure blocked session memory: %v", err)
	}
	stop()
	if !hasWarningContaining(seen(), "could not copy the parent session's memory") {
		t.Fatal("copy failure was not reported")
	}
}
```

Add `"primeradiant.com/evener/llm"` to that file's imports. `captureEvents` already exists in the agent tests. `hasWarningContaining(events, substr) bool` is a new helper in this file: it loops over the captured events and returns true for an `events.EventWarning` whose `events.WarningData.Message` contains `substr`. Running as root makes `chmod 000` ineffective, so add `if os.Geteuid() == 0 { t.Skip("root bypasses permissions") }` at the top of the failure test.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./agent -run 'TestMemoryFork' -count=1`
Expected: build failure on `seedForkedSessionMemory`. Once that compiles, the copy test fails because the child's `MEMORY.md` doesn't exist.

- [ ] **Step 3: Implement**

In `agent/session.go`, next to `restoredMetaParentSessionID`:

```go
	// forkParentSessionID names the session this one was forked or
	// resumed-with from, when it was; empty otherwise. Session memory seeds
	// from that session's directory the first time this session opens it.
	forkParentSessionID string
```

In `agent/session_init.go`, after `s.restoredMetaParentSessionID = meta.ParentSessionID`:

```go
	if !meta.IsSubagent && meta.DivergenceTurn > 0 {
		s.forkParentSessionID = meta.ParentSessionID
	}
```

Create `agent/session_memory_fork.go`:

```go
package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
)

// seedForkedSessionMemory gives a forked root session a copy of its parent's
// session memory the first time the scope is opened. The copy happens only
// while the child's directory does not exist yet, so the branches diverge
// after the fork. A failure is reported and leaves an empty scope; it never
// blocks the session.
func (s *Session) seedForkedSessionMemory() {
	parent, child := s.forkParentSessionID, s.memorySessionID()
	if parent == "" || s.depth > 0 || schema.ValidateSessionID(parent) != nil || schema.ValidateSessionID(child) != nil {
		return
	}
	base := filepath.Join(s.cfg.MemoryStateRoot, "memory", "sessions")
	dst := filepath.Join(base, child)
	if _, err := os.Lstat(dst); !errors.Is(err, os.ErrNotExist) {
		return
	}
	src := filepath.Join(base, parent)
	if _, err := os.Lstat(src); errors.Is(err, os.ErrNotExist) {
		return
	}
	if err := copySessionMemory(src, dst); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("could not copy the parent session's memory into this fork; session memory starts empty: %v", err)})
	}
}

// copySessionMemory copies into a sibling temporary directory and renames it
// into place, so a partial copy is never mistaken for a finished one.
func copySessionMemory(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dst), ".fork-copy-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := os.CopyFS(tmp, os.DirFS(src)); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
```

In `memoryEnvironment`, just before `root, err = execenv.NewConfinedFileRoot(...)` (inside `if err == nil && root == nil {`), add:

```go
		if scope == "session" {
			s.seedForkedSessionMemory()
		}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./agent -run 'Memory' -count=1`
Expected: PASS.

- [ ] **Step 5: Correct the spec**

In `docs/superpowers/specs/2026-10-05-session-memory-design.md`, under "Fork, and `--resume-with`", replace "A copy failure is reported in the fork's result and the fork still proceeds" with: "The copy happens in the forked session's own process the first time it opens session memory, because memory belongs to the host that runs the session. A copy failure is reported as a session warning and leaves an empty session scope; it never blocks the fork."

- [ ] **Step 6: Commit**

```bash
git add agent/session.go agent/session_init.go agent/session_memory.go agent/session_memory_fork.go agent/session_memory_fork_test.go docs/superpowers/specs/2026-10-05-session-memory-design.md
git commit -m "agent: forks seed session memory from their parent"
```

---

### Task 4: Session memory prompting

**Files:**
- Modify: `agent/session_memory.go` (`memoryGuidance()`)
- Modify: `agent/prompt_data.go` (add `SessionMemorySaves`)
- Modify: `agent/session_prompts.go` (set it)
- Modify: `agent/prompts/system.md.tmpl` (Finishing promotion row)
- Test: `agent/session_memory_test.go` (`TestMemoryGuidanceFollowsCapabilities`)

**Interfaces:**
- Consumes: `memorySaveInstructionsEnabled()`, `memoryTrustGuard` and `memorySaveTriggersIntro`, which already exist.
- Produces: `const memorySessionScopeLine`, `const memorySessionDelegateLine`, and the promptData field `SessionMemorySaves bool` (true when saves are enabled and `s.depth == 0`).

- [ ] **Step 1: Extend the failing test**

In `TestMemoryGuidanceFollowsCapabilities`, add a `session` expectation column. It means the root session can save to session memory. Cases: enabled → true, personal-only → true, write-revoked → false, search-revoked → true, disabled → false, unbound → false. Then add these assertions inside `if tc.read {`:

```go
				if !strings.Contains(guidance, memorySessionScopeLine) {
					t.Fatal("read guidance lacks the session scope line")
				}
```

Then add these after the promptData check:

```go
			if data.SessionMemorySaves != tc.session {
				t.Fatalf("SessionMemorySaves=%v, want %v", data.SessionMemorySaves, tc.session)
			}
```

Add a delegate test:

```go
func TestMemoryGuidanceDelegateSessionReadOnly(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(SessionConfig{MemoryStateRoot: t.TempDir(), MemoryProjectID: "fixture-project"}))
	s.depth = 1
	s.delegateRootSessionID = "034aRootFixture0000000"
	guidance := s.memoryGuidance()
	if !strings.Contains(guidance, memorySessionDelegateLine) || strings.Contains(guidance, "Save to session memory") {
		t.Fatalf("delegate guidance: %q", guidance)
	}
	if data, _ := s.buildPromptData(s.currentEnv()); data.SessionMemorySaves {
		t.Fatal("delegate offered session saves")
	}
}
```

If `"034aRootFixture0000000"` fails `schema.ValidateSessionID`, use any valid id from an existing test fixture. Guidance generation doesn't validate the id, but keep it valid anyway.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./agent -run 'TestMemoryGuidance' -count=1`
Expected: build failure on the new constants and field.

- [ ] **Step 3: Implement**

In `agent/session_memory.go`, add constants next to `memoryTrustGuard`:

```go
	memorySessionScopeLine    = "Session memory holds knowledge about the current work: its plan, the constraints and decisions that apply to it, and what you tried and found."
	memorySessionDelegateLine = "Session memory belongs to your root session. Read it, and report what you learn to your parent."
```

In `memoryGuidance()`:
- After the project scope sentence, append `" " + memorySessionScopeLine`.
- If `s.depth > 0`, also append `" " + memorySessionDelegateLine`.
- In the save part, for root sessions only (`s.depth == 0`), add this bullet after the project-told bullet (or after the first bullet, when project memory isn't bound):

```go
		b.WriteString("\n- you are doing work you would need to pick up again if it were interrupted and resumed after compaction: its plan, decisions and why, and what you tried and ruled out. Save it to session memory; what your partner says about this work goes there too.")
```

- For root sessions, replace the skip sentence `"Skip what the repository already says and details only the current task needs. A constraint or plan that shaped this task usually outlives it."` with `"Skip what the repository already says. Details only this work needs belong in session memory, and a constraint or plan that shaped this work usually outlives it."` Keep the original sentence for delegates.

In `agent/prompt_data.go`, next to `ProjectMemory`:

```go
	// SessionMemorySaves is true for a root session that may save to its
	// session memory; delegates read their root's and never write it.
	SessionMemorySaves bool
```

In `agent/session_prompts.go`, next to `ProjectMemory:`:

```go
		SessionMemorySaves:       s.memorySaveInstructionsEnabled() && s.depth == 0,
```

In `agent/prompts/system.md.tmpl`, inside `{{- if .MemorySaves }}` after the existing rows, before its `{{- end }}`:

```
{{- if .SessionMemorySaves }}
| "The work is done and session memory has notes." | Before you report, copy anything that holds beyond this work into project or personal memory. |
{{- end }}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./agent -run 'Memory|Prompt' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session_memory.go agent/prompt_data.go agent/session_prompts.go agent/prompts/system.md.tmpl agent/session_memory_test.go
git commit -m "agent: prompt for session memory and promotion out of it"
```

---

### Task 5: Documentation

**Files:**
- Modify: `docs/product/memory.md`
- Modify: `docs/tools/memory.md`
- Modify: `docs/product/subsystems.md` (the S24 row, only if its responsibility text names the scopes)
- Modify: `internal/bundled/skills/gardening-memory/SKILL.md`

- [ ] **Step 1: Update `docs/product/memory.md`**
  - Add `<state-root>/memory/sessions/<root-session-id>/` to the storage roots block.
  - In Ownership, add one paragraph: "Session memory holds knowledge about the current work. The root session owns it; delegates resolve the scope to their root and can read but not write it. Resume keeps it. A fork or `--resume-with` child copies its parent's session memory the first time it opens the scope, then the two diverge; a copy failure is a session warning and leaves the scope empty. Deleting a session keeps its session memory."
  - In Context and editing, change "separate personal and project `MEMORY.md` projections" to "separate personal, project and session `MEMORY.md` projections". Add to the guidance paragraph that root sessions are told what goes in session memory and to promote lasting lessons out of it before reporting, and that delegates are told session memory is their root's, to read.

- [ ] **Step 2: Update `docs/tools/memory.md`**

Document `session` in the `scope` parameter, the delegate refusal error (quote the Global Constraints text), and the storage path.

- [ ] **Step 3: Update `internal/bundled/skills/gardening-memory/SKILL.md`**

After the scope sentence ("Use personal scope for cross-project preferences and project scope for project lessons."), add: "Session scope holds notes about the current work; promote anything in it that outlasts the work into project or personal memory, and leave session notes from finished sessions for cleanup when they no longer help."

- [ ] **Step 4: Run the bundled-skill catalog test, if one exists, plus docs lints**

Run: `go test ./internal/bundled/... -count=1` and `make lint-repository` if that target exists (`make help | grep -i lint`).
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add docs/product/memory.md docs/tools/memory.md docs/product/subsystems.md internal/bundled/skills/gardening-memory/SKILL.md
git commit -m "docs(memory): session scope"
```

---

### Task 6: Lab verification and the PR

The memory lab lives in the git-ignored `tools/prompt-eval/results/memory-lab/`, and nothing in this task is committed except the PR text. The harness is `memory-lab` (`run`, `report`, `show`, `ask`). Build `bin/evener-v9` from main (the baseline) and `bin/evener-s1` from this branch.

- [ ] **Step 1: Write three scenarios** under `scenarios/`
  - **`session-local`:**
    - A: a two-part task with a constraint that only applies to this work, for example "for this refactor only, keep the old function name exported as a deprecated alias until the follow-up lands".
    - Check: session memory has the constraint (`"memory": [{"regex": "(?i)alias", "scope": ...}]`), and project memory doesn't. Add a `"session"` value to the harness's memory-check `scope` map, matching the path prefix `sessions/`.
  - **`promote`:**
    - A: partner states a lasting project fact while asking for task-local work.
    - Check: the fact is in project memory at the end of A (promotion), and B, a fresh session, follows it.
  - **`delegate-reads`:**
    - Seeded root session memory with a constraint.
    - A: a prompt that asks the agent to have a delegate do a sub-part.
    - Checks: the delegate's trace includes `memory_read` with `scope: session`, and there are no session writes by the delegate.

- [ ] **Step 2: Run** v9 against s1 on the three new scenarios plus `feedback`, `migration` and `sed-quirk` (regressions), 6 reps, on `lunarouter/deepseek-4.1-flash-background`. Then run a smaller pass (4 reps) on `lunarouter/glm-5.3-flash-background`.

- [ ] **Step 3: Interview** every failing trial with `memory-lab ask`: "did you consider saving that, and which scope did you choose and why?" If the answers show a wording problem, change the guidance, rebuild, and rerun only the affected scenarios. Each wording change gets its own commit, made with the Task 4 test passing.

- [ ] **Step 4: Gates, then the PR**
  - Run `go test ./agent/... ./cmd/evener/... -count=1`, the three `go vet` variants and `golangci-lint` on `./agent/...`. All must be clean.
  - Run `/simplify` on the branch.
  - Open a regular PR with base `main`. Include a results table (baseline against branch, per scenario: A saves / B uses) and the `/simplify` outcome.
  - Shepherd it with `/shepherd-pr`.
