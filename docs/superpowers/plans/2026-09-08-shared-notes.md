# Shared Notes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement per-session shared notes (human paragraph, agent paragraph, agent-curated URL list) across daemon, wire, hub, web UI, and TUI.

**Architecture:** Follow the goal precedent end to end: persisted `SessionMeta` fields, session events projected to appwire pushes, daemon RPC handlers behind func callbacks, hub relay with pre-flight capability gate plus qp94 resume, web `ThreadModel` hydration plus reducer push handling, TUI detail mapping plus drawer rendering.

**Tech Stack:** Go (daemon, appwire, hub, TUI), TypeScript/React (hub frontend, Biome scope `src/`), `make generate` for `types.gen.ts`.

**Spec:** `docs/superpowers/specs/2026-09-08-shared-notes-design.md`

## Global Constraints

- Default tests stay hermetic: scripted provider at the LLM boundary, no live network fetches. URL add performs no fetch.
- `types.gen.ts` is never hand-edited. Run `make generate` (source: `internal/appwirets/emit.go` from `agent/events` kinds).
- New steering kind `human-note` must join `events.AllSteeringKinds` (`agent/events/payloads.go:369`), `KIND_LABELS` in `SteeringItem.tsx`, and the producer-coverage test (`agent/steering_kind_coverage_test.go`).
- New `EvenerThread` fields need deep-copy support in `appwire/clone.go` (append-copy idiom for slices).
- Paragraph: collapse every run of whitespace (including newlines) to a single space, cap at 1000 Unicode characters, server clamps, downstream uses post-clamp value, no-op detection clamp-then-compares.
- URL list caps at 50 entries; URL 2048 chars; label 280 chars; over-limit adds fail typed.
- Canonical dedup key: bare paths resolve against session cwd first; http(s) lowercases scheme+host, drops default ports, collapses trailing slash on empty path, drops fragment; re-add updates label, keeps id/addedBy/addedAt.
- Display rule: (1) capability unset hides section; (2) set but not live shows read-only; (3) set and live is full. Hub liveness uses composer `ENDED_STATUSES` (`ended|closed|notLoaded`); TUI uses `Live` derivation. Export the hub set or restate values at the section so it cannot drift from `DetailsPanel.tsx:87-89`.
- Commit after every task with `git add <named paths>` (never `git add -A`).

---

### Task 1: Persisted notes model

**Files:**
- Modify: `agent/schema/snapshot.go` (add `HumanNote`, `AgentNote`, `SessionURLs` fields to `SessionMeta` near `Goal`/`PinnedNote`; add `SessionURL{ID, URL, Label, AddedBy, AddedAt}` type)
- Test: `agent/schema/snapshot_shared_notes_test.go` (create)

**Interfaces:**
- Consumes: nothing.
- Produces: `schema.SessionURL` struct; `schema.SessionMeta` fields `HumanNote string json:"human_note,omitempty"`, `AgentNote string json:"agent_note,omitempty"`, `SessionURLs []SessionURL json:"session_urls,omitempty"` for Tasks 2-4.

- [ ] **Step 1: Write the failing test**

```go
func TestSessionMetaSharedNotesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	id := "notesrt1"
	seed := SessionMeta{ID: id, HumanNote: "human hello", AgentNote: "agent hello",
		SessionURLs: []SessionURL{{ID: "u1", URL: "https://x.test/y", Label: "x", AddedBy: "agent"}}}
	if err := SaveSessionMeta(dir, seed); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	got, err := LoadSessionMeta(dir, id)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if got.HumanNote != "human hello" || got.AgentNote != "agent hello" || len(got.SessionURLs) != 1 || got.SessionURLs[0].URL != "https://x.test/y" {
		t.Fatalf("round trip = %+v", got)
	}
}

func TestSessionMetaSharedNotesDefaultsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := SaveSessionMeta(dir, SessionMeta{ID: "notesempty1"}); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	got, err := LoadSessionMeta(dir, "notesempty1")
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if got.HumanNote != "" || got.AgentNote != "" || len(got.SessionURLs) != 0 {
		t.Fatalf("defaults = %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/schema/ -run 'TestSessionMetaSharedNotes' -v`
Expected: FAIL (undefined fields/types)

- [ ] **Step 3: Write minimal implementation**

Add near `Goal`/`PinnedNote` in `SessionMeta` (follow the comment style of those fields: what it is, why it persists):

```go
// HumanNote is the human's one-paragraph session whiteboard, persisted so it
// survives daemon restart and evener resume. Empty means unset.
HumanNote string `json:"human_note,omitempty"`
// AgentNote is the agent's one-paragraph session whiteboard, persisted like
// HumanNote. Empty means unset.
AgentNote string `json:"agent_note,omitempty"`
// SessionURLs is the agent-curated session URL list, persisted so it
// survives daemon restart and evener resume. Empty/nil means no links.
SessionURLs []SessionURL `json:"session_urls,omitempty"`
```

And the entry type beside `SessionMeta`:

```go
// SessionURL is one entry in a session's shared-notes URL list.
type SessionURL struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	Label   string `json:"label,omitempty"`
	AddedBy string `json:"added_by,omitempty"`
	AddedAt int64  `json:"added_at,omitempty"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/schema/ -run 'TestSessionMetaSharedNotes' -v`
Expected: PASS. Then: `go test ./agent/schema/`
Expected: PASS (no regressions in save/load/ObservedBy/lock tests)

- [ ] **Step 5: Commit**

```bash
git add agent/schema/snapshot.go agent/schema/snapshot_shared_notes_test.go
git commit -m "feat(notes): persist human/agent whiteboards and session URL list"
```

### Task 2: Session events, steering kind, wire types

**Files:**
- Modify: `agent/events/events.go` (add `EventNotesUpdated`, `EventUrlsUpdated` kinds near `EventGoalUpdated`)
- Modify: `agent/events/eventdata.go` (add `NotesUpdatedData{HumanNote, AgentNote string}`, `UrlsUpdatedData{URLs []SessionURLData}` payload types with `eventKind()` methods; add `SessionURLData{ID, URL, Label, AddedBy string, AddedAt int64}`)
- Modify: `agent/events/payloads.go` (add `SteeringKindHumanNote = "human-note"` const; append to `AllSteeringKinds`)
- Modify: `appwire/types.go` (add `MethodNotesHumanSet = "notes/human/set"`, `MethodNotesAgentSet = "notes/agent/set"`, `MethodUrlsAdd = "urls/add"`, `MethodUrlsRemove = "urls/remove"`, `NotifyEvenerNotesUpdated = "evener/notes/updated"`, `NotifyEvenerUrlsUpdated = "evener/urls/updated"`; add `HumanNote`, `AgentNote string`, `SessionURLs []SessionURL` to `EvenerThread` near `Goal`; add `SharedNotes bool` to `ThreadCapabilities` beside `Goal`; add `NotesHumanSetParams{Ref, ClientMutationID, ExpectedInstanceID, Note}`, `NotesHumanSetResponse{Note}`, `UrlsRemoveParams{Ref, ClientMutationID, ExpectedInstanceID, ID}`, `UrlsRemoveResponse{}`, `NotesUpdatedParams{ThreadID, Ref, HumanNote, AgentNote}`, `UrlsUpdatedParams{ThreadID, Ref, URLs []SessionURL}`; add `SessionURL` wire type)
- Test: `agent/events/notes_events_test.go` (create: kinds distinct, payload `eventKind()` mapping)

**Interfaces:**
- Consumes: `schema.SessionURL` shape from Task 1 (mirrored, not imported: events must not import schema).
- Produces: event kinds/payloads for Task 5 (projector); method/notification constants + params for Tasks 3-4, 6-8.

- [ ] **Step 1: Write the failing test**

```go
func TestNotesEventKinds(t *testing.T) {
	if EventNotesUpdated == EventUrlsUpdated || EventNotesUpdated == EventGoalUpdated {
		t.Fatalf("note event kinds collide: %q %q", EventNotesUpdated, EventUrlsUpdated)
	}
	var _ EventData = NotesUpdatedData{}
	var _ EventData = UrlsUpdatedData{}
	if (NotesUpdatedData{}).eventKind() != EventNotesUpdated {
		t.Fatalf("NotesUpdatedData kind mismatch")
	}
	if (UrlsUpdatedData{}).eventKind() != EventUrlsUpdated {
		t.Fatalf("UrlsUpdatedData kind mismatch")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/events/ -run 'TestNotesEventKinds' -v`
Expected: FAIL (undefined identifiers)

- [ ] **Step 3: Write minimal implementation**

In `events.go`, beside `EventGoalUpdated`:

```go
// EventNotesUpdated reports a change to the session's shared-notes whiteboards.
EventNotesUpdated EventKind = "NOTES_UPDATED"
// EventUrlsUpdated reports a change to the session's shared-notes URL list.
EventUrlsUpdated EventKind = "URLS_UPDATED"
```

In `eventdata.go` (copy the `GoalUpdatedData` pattern exactly, including the `eventKind()` method and the `_ EventData = ...` assertion line):

```go
// NotesUpdatedData carries the session whiteboards after a mutation.
type NotesUpdatedData struct {
	HumanNote string `json:"human_note"`
	AgentNote string `json:"agent_note"`
}

// UrlsUpdatedData carries the session URL list after a mutation.
type UrlsUpdatedData struct {
	URLs []SessionURLData `json:"urls"`
}

// SessionURLData is one URL list entry on the event stream.
type SessionURLData struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	Label   string `json:"label,omitempty"`
	AddedBy string `json:"added_by,omitempty"`
	AddedAt int64  `json:"added_at,omitempty"`
}
```

In `payloads.go`: `SteeringKindHumanNote = "human-note"` plus append to `AllSteeringKinds`.

In `appwire/types.go`: constants near `MethodGoalSet`/`NotifyEvenerGoalUpdated`; `EvenerThread` fields near `Goal` with `omitempty` (nil/empty means unset, additive for old daemons); `SharedNotes` beside `Goal` with a comment in the style of the `Goal` comment; params/responses following the `GoalSetParams`/`GoalUpdatedParams` shapes (hub RPC params carry `Ref`, `ClientMutationID`, `ExpectedInstanceID`; push params carry `ThreadID`, `Ref`, state).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/events/ -run 'TestNotesEventKinds' -v`
Expected: PASS. Then: `go test ./agent/events/ ./appwire/`
Expected: PASS (including `TestSteeringKindConstants`; update its `want` map with `"human-note": SteeringKindHumanNote` — that map is exhaustive, so add the entry in this same task)

- [ ] **Step 5: Commit**

```bash
git add agent/events/events.go agent/events/eventdata.go agent/events/payloads.go agent/events/notes_events_test.go appwire/types.go agent/events/payloads_test.go
git commit -m "feat(notes): session note/URL events, human-note steering kind, wire types"
```

### Task 3: Session notes store (normalize, clamp, dedup, URL ops)

**Files:**
- Create: `agent/session_notes.go` (notes store: `normalizeNote`, `setHumanNote`, `setAgentNote`, `addSessionURL`, `removeSessionURL`, canonicalization)
- Test: `agent/session_notes_test.go` (create)

**Interfaces:**
- Consumes: `schema.SessionMeta` fields + `schema.SessionURL` from Task 1.
- Produces: `normalizeNote(text string) string`; `(s *Session) setHumanNote(note string) (stored string, changed bool)`; `(s *Session) setAgentNote(note string) (stored string, changed bool)`; `(s *Session) addSessionURL(url, label string) (schema.SessionURL, error)`; `(s *Session) removeSessionURL(id string) bool`; `canonicalSessionURL(raw, cwd string) (string, error)` for Tasks 4-5.

- [ ] **Step 1: Write the failing test**

```go
func TestNormalizeNoteCollapsesWhitespaceAndClamps(t *testing.T) {
	in := "  hello\n\n  world\t\tfoo  "
	if got := normalizeNote(in); got != "hello world foo" {
		t.Fatalf("normalizeNote(%q) = %q", in, got)
	}
	long := strings.Repeat("a", 2000)
	if got := normalizeNote(long); len([]rune(got)) != 1000 {
		t.Fatalf("clamped length = %d, want 1000", len([]rune(got)))
	}
}

func TestAddSessionURLDedupsCanonically(t *testing.T) {
	s := newTestNotesSession(t, "/tmp/proj")
	a, err := s.addSessionURL("docs/x.md", "first")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	b, err := s.addSessionURL("./docs/x.md", "second")
	if err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if a.ID != b.ID || b.Label != "second" {
		t.Fatalf("dedup = %+v vs %+v, want same id with updated label", a, b)
	}
	if n := len(s.sessionURLsForTest()); n != 1 {
		t.Fatalf("list length = %d, want 1", n)
	}
}

func TestAddSessionURLRejectsOverCapAndBadScheme(t *testing.T) {
	s := newTestNotesSession(t, "/tmp/proj")
	if _, err := s.addSessionURL("gopher://x.test/y", ""); err == nil {
		t.Fatalf("bad scheme accepted")
	}
	for i := 0; i < 50; i++ {
		if _, err := s.addSessionURL(fmt.Sprintf("https://x.test/%d", i), ""); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}
	if _, err := s.addSessionURL("https://x.test/overflow", ""); err == nil {
		t.Fatalf("51st URL accepted")
	}
}
```

(`newTestNotesSession` and `sessionURLsForTest` are test helpers defined in the test file: build a minimal `Session` with cwd `/tmp/proj` and empty notes state. Keep them unexported in the test file.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run 'TestNormalizeNote|TestAddSessionURL' -v`
Expected: FAIL (undefined functions)

- [ ] **Step 3: Write minimal implementation**

`agent/session_notes.go`:

```go
package agent

// Shared-notes store: normalization, clamping, URL canonicalization and list
// ops. All methods hold s.mu; persistence callers save SessionMeta after.

const (
	sessionNoteMaxRunes = 1000
	sessionURLMax       = 50
	sessionURLMaxLen    = 2048
	sessionLabelMaxLen  = 280
)

// normalizeNote collapses every run of whitespace (including newlines) to one
// space and clamps to sessionNoteMaxRunes Unicode characters.
func normalizeNote(text string) string { ... }

// canonicalSessionURL resolves raw against cwd (bare paths) and normalizes:
// http(s) lowercases scheme+host, drops default ports, collapses a trailing
// slash on empty path, drops fragment. Rejects non-http(s)/file schemes,
// over-length url/label, and out-of-scope file paths (securepath).
func canonicalSessionURL(raw, cwd string) (string, error) { ... }
```

`setHumanNote`/`setAgentNote`: normalize+clamp, clamp-then-compare against stored, store on change, return `(stored, changed)`. `addSessionURL`: validate, canonicalize, dedup-on-key (re-add updates label, keeps id/addedBy/addedAt), enforce 50-cap, mint server UUID (`identifier` package), return entry. `removeSessionURL`: delete by id, return found. Lock with the session's existing mutex discipline (check how goal state is guarded in `agent/session_goal.go` and mirror it). Persist by updating `SessionMeta` through the existing meta-save path (mirror how goal/pinned-note persist).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run 'TestNormalizeNote|TestAddSessionURL|TestRemoveSessionURL|TestSetHumanNote|TestSetAgentNote' -v`
Expected: PASS. Then: `go test ./agent/`
Expected: PASS (no regressions)

- [ ] **Step 5: Commit**

```bash
git add agent/session_notes.go agent/session_notes_test.go
git commit -m "feat(notes): session notes store with normalize, clamp, canonical dedup"
```

### Task 4: Agent tools (notes/agent/set, urls/add, urls/remove, notes/read)

**Files:**
- Modify: `agent/internal/tool/definitions.go` (add `DefNotesAgentSet`, `DefUrlsAdd`, `DefUrlsRemove`, `DefNotesRead`)
- Create: `agent/session_tools_notes.go` (register + handlers, beside `session_tools_goal.go`)
- Test: `agent/session_tools_notes_test.go` (create: each tool happy path; wrong-channel absence is Task 8)

**Interfaces:**
- Consumes: Task 3 store methods; `tool.RegisteredTool` pattern from `registerGoalTools`.
- Produces: registered tools `notes/agent/set`, `urls/add`, `urls/remove`, `notes/read` for Task 5 (context injection reads the same store).

- [ ] **Step 1: Write the failing test**

```go
func TestNotesAgentSetTool(t *testing.T) {
	s, ctx := newNotesToolSession(t)
	res := stmExec(t, s, ctx, "n1", "notes/agent/set", map[string]any{"note": "agent says hi"})
	out, ok := res.Output.(string)
	if !ok || out == "" {
		t.Fatalf("output = %#v", res.Output)
	}
	if s.agentNoteForTest() != "agent says hi" {
		t.Fatalf("agent note = %q", s.agentNoteForTest())
	}
}

func TestUrlsAddRemoveTool(t *testing.T) {
	s, ctx := newNotesToolSession(t)
	added := stmExec(t, s, ctx, "u1", "urls/add", map[string]any{"url": "https://x.test/y", "label": "why"})
	// entry id must round-trip into remove
	...
}
```

(Follow the `session_tools_goal_test.go` harness: `stmExec(t, s, ctx, id, name, args)`. Check that file for the exact helper signature before writing.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run 'TestNotesAgentSetTool|TestUrlsAddRemoveTool|TestNotesReadTool' -v`
Expected: FAIL (unknown tools)

- [ ] **Step 3: Write minimal implementation**

Definitions (mirror `DefUpdateGoal` shape: `Name`, `Description`, `Parameters` with `type/object`, `additionalProperties: false`, `properties`, `required`):

```go
func DefNotesAgentSet() llm.ToolDefinition { ... name "notes/agent/set", required ["note"] ... }
func DefUrlsAdd() llm.ToolDefinition { ... name "urls/add", required ["url"], optional "label" ... }
func DefUrlsRemove() llm.ToolDefinition { ... name "urls/remove", required ["id"] ... }
func DefNotesRead() llm.ToolDefinition { ... name "notes/read", no required params ... }
```

Handlers in `agent/session_tools_notes.go` follow the `registerGoalTools`
structure (`agent/session_tools_goal.go:13-42`: `_ = reg.Register(tool.RegisteredTool{Definition: ..., Exec: func(ctx, env, args) (any, error) {...}})`):
`notes/agent/set` calls `setAgentNote`, emits `EventNotesUpdated` on change; `urls/add` calls `addSessionURL`, emits `EventUrlsUpdated`; `urls/remove` calls `removeSessionURL`, emits on found; `notes/read` returns `{humanNote, agentNote, urls}`. Errors are typed (`fmt.Errorf("urls/add: ...")`, matching the `update_goal:` prefix style). No `notes/human/set` tool is registered here — ownership by channel absence.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run 'TestNotesAgentSetTool|TestUrlsAddRemoveTool|TestNotesReadTool' -v`
Expected: PASS. Then: `go test ./agent/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/internal/tool/definitions.go agent/session_tools_notes.go agent/session_tools_notes_test.go
git commit -m "feat(notes): agent tools for agent note, URL add/remove, notes read"
```

### Task 5: Daemon RPCs, projector pushes, agent read path

**Files:**
- Modify: `server/appwire_runtime.go` (register `notes/human/set` + hub `urls/remove` handlers beside `handleAppGoalSet`; each routes through a session callback like `goalFunc`)
- Modify: `agent/session_notes.go` or new `agent/session_notes_rpc.go` (`SetHumanNote` with derived inner-steer id `outer + "/note-steer"`, no-op on equal text, cleared-marker on non-empty→empty, `AcceptClientMutationSteer` injection; `RemoveSessionURL` for daemon path)
- Modify: `internal/appprojector/appwire_projection.go` (project `EventNotesUpdated`→`NotifyEvenerNotesUpdated`, `EventUrlsUpdated`→`NotifyEvenerUrlsUpdated`, mirroring the `EventGoalUpdated` case at line 388)
- Modify: `agent/internal/goal/prompt.go` or session prompt render (inject current notes + URL list into agent context at turn start/resume; refresh from note events before next round)
- Test: `server/appwire_notes_test.go` (create: human-set stores + single steer on retry of one outer id; remove by id); `internal/appprojector/appwire_projection_notes_test.go` (create: mirror `TestProject_GoalUpdated`/`TestProject_GoalUpdatedClear`); agent context test in `agent/session_notes_test.go` (append: context contains notes/URLs)

**Interfaces:**
- Consumes: Task 2 events/wire types; Task 3 store; Task 4 tool emissions.
- Produces: working daemon RPCs + pushes consumed by Tasks 6-9.

- [ ] **Step 1: Write the failing test**

```go
func TestProject_NotesUpdated(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventNotesUpdated,
		Data: events.NotesUpdatedData{HumanNote: "h", AgentNote: "a"},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifyEvenerNotesUpdated {
		t.Fatalf("want one evener/notes/updated notification, got %+v", out)
	}
	params, ok := out[0].Params.(appwire.NotesUpdatedParams)
	if !ok || params.HumanNote != "h" || params.AgentNote != "a" {
		t.Fatalf("params = %+v", out[0].Params)
	}
}
```

(Plus `TestProject_UrlsUpdated` mirroring it, and server tests following `TestServerAppWireGoalSetInvokesGoalFunc` in `server/appwire_server_test.go`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/appprojector/ -run 'TestProject_NotesUpdated|TestProject_UrlsUpdated' -v`
Expected: FAIL (unknown event kinds)

- [ ] **Step 3: Write minimal implementation**

Projector: add two cases copying the `EventGoalUpdated` case structure
(`internal/appprojector/appwire_projection.go:388-402`: extract typed data via
`eventData[...]`, build the wire state pointer, return
`[]AppNotification{p.notification(Method, Params{ThreadID: p.threadID, Ref: p.ref, ...})}`):

```go
case events.EventNotesUpdated:
	data := eventData[events.NotesUpdatedData](event.Data)
	return []AppNotification{p.notification(appwire.NotifyEvenerNotesUpdated, appwire.NotesUpdatedParams{
		ThreadID:  p.threadID,
		Ref:       p.ref,
		HumanNote: data.HumanNote,
		AgentNote: data.AgentNote,
	})}
case events.EventUrlsUpdated:
	data := eventData[events.UrlsUpdatedData](event.Data)
	urls := make([]appwire.SessionURL, 0, len(data.URLs))
	for _, u := range data.URLs {
		urls = append(urls, appwire.SessionURL{ID: u.ID, URL: u.URL, Label: u.Label, AddedBy: u.AddedBy, AddedAt: u.AddedAt})
	}
	return []AppNotification{p.notification(appwire.NotifyEvenerUrlsUpdated, appwire.UrlsUpdatedParams{
		ThreadID: p.threadID,
		Ref:      p.ref,
		URLs:     urls,
	})}
```

Daemon handlers: copy `handleAppGoalSet` structure — `requireRootMutationTarget`, nil-func → `Unavailable`, callback, return stored value; never emit pushes directly (projector owns emission). `SetHumanNote(outerID, note)`: normalize+clamp, clamp-then-compare (no-op returns current, no event, no steer), store, append `EventNotesUpdated`, inject steer via `AcceptClientMutationSteer` with id `outer + "/note-steer"` and kind `SteeringKindHumanNote`, text `"human updated their whiteboard: <post-clamp text>"` or `"(whiteboard cleared)"` marker. Context injection: beside goal continuation-prompt rendering, include `HumanNote`/`AgentNote`/URLs; refresh from note events pre-round.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/appprojector/ ./server/ -run 'Notes|Urls|GoalSet' -v`
Expected: PASS. Then: `go test ./internal/appprojector/ ./server/ ./agent/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add server/appwire_runtime.go agent/session_notes.go agent/session_notes_rpc.go internal/appprojector/appwire_projection.go agent/internal/goal/prompt.go server/appwire_notes_test.go internal/appprojector/appwire_projection_notes_test.go agent/session_notes_test.go
git commit -m "feat(notes): daemon RPCs, projector pushes, agent context injection"
```

### Task 6: Wire catalog, client, clone, codegen

**Files:**
- Modify: `appwire/protocol.go` (method catalog entries for 4 RPCs; notification catalog for 2 pushes; `ValidateMutationParams` required fields for the 2 hub RPCs)
- Modify: `appwire/client.go` (`NotesHumanSet`, `UrlsRemove` methods mirroring `GoalSet`/`TurnSteer`)
- Modify: `appwire/clone.go` (deep-copy `HumanNote`/`AgentNote` strings trivially; `SessionURLs` via append-copy idiom)
- Modify: `docs/appwire-protocol.md` (method + notification rows)
- Run: `make generate` (refresh `types.gen.ts` + `STEERING_KINDS` with `human-note`)
- Test: `appwire/protocol_notes_test.go` (create: catalog contains methods/notifications; mutation params validate; clone does not alias URL slice)

**Interfaces:**
- Consumes: Task 2 types.
- Produces: generated TS types + `STEERING_KINDS` for Task 7; clone safety for all consumers.

- [ ] **Step 1: Write the failing test**

```go
func TestNotesWireCatalog(t *testing.T) {
	for _, m := range []string{MethodNotesHumanSet, MethodNotesAgentSet, MethodUrlsAdd, MethodUrlsRemove} {
		if !methodInCatalog(m) {
			t.Fatalf("method %q missing from catalog", m)
		}
	}
	a := EvenerThread{SessionURLs: []SessionURL{{ID: "u1", URL: "https://x.test/"}}}
	b := cloneEvenerThread(a)
	b.SessionURLs[0].URL = "mutated"
	if a.SessionURLs[0].URL != "https://x.test/" {
		t.Fatalf("clone aliases URL slice")
	}
}
```

(Check `protocol_test.go` for the real catalog-lookup helper name and use it.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./appwire/ -run 'TestNotesWireCatalog' -v`
Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

Catalog entries copy the `MethodGoalSet` row style (`{Method, Params{}, Response{}, ScopeBoth, "description"}`). `ValidateMutationParams`: hub RPCs require `clientMutationId` + `expectedInstanceId` (copy the `MethodTurnSteer` line). Client methods copy `GoalSet` (`appwire/client.go:498`: `func (c *Client) GoalSet(ctx, params) (out, error) { err := c.request(ctx, MethodGoalSet, params, &out); ... }`):

```go
func (c *Client) NotesHumanSet(ctx context.Context, params NotesHumanSetParams) (NotesHumanSetResponse, error) {
	var out NotesHumanSetResponse
	err := c.request(ctx, MethodNotesHumanSet, params, &out)
	return out, err
}

func (c *Client) UrlsRemove(ctx context.Context, params UrlsRemoveParams) (UrlsRemoveResponse, error) {
	var out UrlsRemoveResponse
	err := c.request(ctx, MethodUrlsRemove, params, &out)
	return out, err
}
```

`clone.go`: `e.SessionURLs = append([]SessionURL(nil), e.SessionURLs...)`. Docs rows copy the goal rows. Then `make generate`; verify `types.gen.ts` contains `human-note` in `STEERING_KINDS` and the new interfaces; verify `internal/appwirets/emit_test.go` passes.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./appwire/ ./internal/appwirets/ -v 2>&1 | tail -5`
Expected: PASS. Then: `git status --porcelain` shows only intended files (spec, wire, generated output, docs).

- [ ] **Step 5: Commit**

```bash
git add appwire/protocol.go appwire/client.go appwire/clone.go docs/appwire-protocol.md cmd/evener-hub/frontend/src/protocol/types.gen.ts internal/appwirets/ appwire/protocol_notes_test.go
git commit -m "feat(notes): wire catalog, client, clone, codegen"
```

### Task 7: Hub relay, capability, past sessions, web UI

**Files:**
- Modify: `cmd/evener-hub/app_rpc.go` (register `notes/human/set` + `urls/remove` relays beside `MethodGoalSet`, via `setNotesWithResume`-style helpers with pre-flight `SharedNotes` capability gate returning Unavailable)
- Modify: `cmd/evener-hub/app_session_resume.go` (add `setNotesHumanWithResume` + `removeURLWithResume` following `setGoalWithResume`, including `ensureThreadActionAvailable` "notes"/"urls" actions)
- Modify: `cmd/evener-hub/app_threadread.go` (`pastThreadCapabilities` sets `SharedNotes: true`; `pastEntryThread` projects stored notes/URLs)
- Modify: `cmd/evener-hub/app_relay.go` (close frame carries the same set — verify it marshals `pastThreadCapabilities()`, no change if so)
- Modify: `cmd/evener-hub/web_session.go` (`fetchStatus`/`daemonStatus` project notes/URLs/capability)
- Modify: `cmd/evener-hub/frontend/src/protocol/model.ts` (add `humanNote: string`, `agentNote: string`, `sessionUrls: SessionURL[]`, `sharedNotes: boolean` capability passthrough)
- Modify: `cmd/evener-hub/frontend/src/protocol/reducer.ts` (hydrateThread mapping + `evener/notes/updated` + `evener/urls/updated` cases mirroring `evener/goal/updated` at line 1323; thread + watched models)
- Modify: `cmd/evener-hub/frontend/src/stores/threads.ts` (push dispatch + `setHumanNote`/`removeURL` actions with generation guard mirroring `setGoal`)
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/DetailsPanel.tsx` (Shared notes section per spec display rule; export the liveness set or restate values — do NOT import unexported `ENDED_STATUSES`)
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/SteeringItem.tsx` (`KIND_LABELS` entry `"human-note": "Human note"`)
- Test: `cmd/evener-hub/app_rpc_notes_test.go` (create: pre-flight reject-when-unset for both RPCs, `TestHubRPCGoalSetGatedByCapability` pattern; resume-first on ended session); frontend `DetailsPanel.notes.test.tsx` (create) + `reducer.notes.test.ts` (create)

**Interfaces:**
- Consumes: Tasks 2, 5, 6 (wire, pushes, generated types).
- Produces: working hub relay + web UI for Task 9 (e2e).

- [ ] **Step 1: Write the failing test (Go)**

```go
func TestHubRPCNotesHumanSetGatedByCapability(t *testing.T) {
	// Mirror TestHubRPCGoalSetGatedByCapability (app_rpc_test.go:8186):
	// daemon ThreadRead returns caps without SharedNotes; notes/human/set
	// must fail Unavailable before reaching the source.
}
```

(Copy that test's body, swapping `MethodGoalSet`/`GoalSetParams`/goal func for the notes equivalents.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/evener-hub/ -run 'TestHubRPCNotesHumanSetGatedByCapability' -v`
Expected: FAIL (unknown method)

- [ ] **Step 3: Write minimal implementation (Go relay)**

Copy `setGoalWithResume` twice (human-set with `clientMutationID` passthrough for retry-safe fencing; remove likewise). Pre-flight gate: read capabilities from hub's own `thread/read` (same call the goal gate makes), return `appwire.Unavailable` when `SharedNotes` is false. `pastThreadCapabilities`: add `SharedNotes: true`. `pastEntryThread`: project stored notes/URLs from meta (check how `Goal` is projected there and mirror). `web_session.go`: add fields to `daemonStatus` + `fetchStatus` (mirror `Goal`).

- [ ] **Step 4: Write the failing test (frontend)**

```tsx
test("shared notes section hides when capability unset", () => {
  render(<DetailsPanelBody sessionRef="r" model={model({ capabilities: caps({ sharedNotes: false }) })} now={0} />);
  expect(screen.queryByTestId("shared-notes-section")).toBeNull();
});
```

(Mirror `DetailsPanel.test.tsx` harness; check its `model()` helper for capability overrides.)

- [ ] **Step 5: Run frontend test to verify it fails**

Run: `make test-web` scoped to the new file (check `cmd/evener-hub/frontend/package.json` test script for the file filter flag)
Expected: FAIL

- [ ] **Step 6: Write minimal implementation (frontend)**

`model.ts`: fields + comments in the `goal` style. `reducer.ts`: hydrate mapping (`?? null`/`?? []`) + two push cases copying the goal case (thread + watched, fallback invalidation, `lastFrameAt`). `threads.ts`: dispatch wiring + actions with generation guard + `mapConflict` error mapping (copy `setGoal`). `DetailsPanel.tsx`: new section implementing the ordered display rule with `data-testid="shared-notes-section"`; human edit popover copying `GoalControl` (toast on failure, draft kept, generation guard); URL rows with remove buttons; `file:`/paths via `docContent.ts` + `OpenButton`. `SteeringItem.tsx`: add `"human-note": "Human note"` to `KIND_LABELS` (exhaustive record — build fails without it). Run `npx biome check --write` on touched files under `src/`.

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./cmd/evener-hub/ -run 'Notes|Urls|SharedNotes' -v`
Expected: PASS. Then: `make test-web`
Expected: PASS (includes Biome gate)

- [ ] **Step 8: Commit**

```bash
git add cmd/evener-hub/app_rpc.go cmd/evener-hub/app_session_resume.go cmd/evener-hub/app_threadread.go cmd/evener-hub/web_session.go cmd/evener-hub/app_rpc_notes_test.go cmd/evener-hub/frontend/src/protocol/model.ts cmd/evener-hub/frontend/src/protocol/reducer.ts cmd/evener-hub/frontend/src/stores/threads.ts cmd/evener-hub/frontend/src/panes/session/chrome/DetailsPanel.tsx cmd/evener-hub/frontend/src/panes/session/chrome/SteeringItem.tsx cmd/evener-hub/frontend/src/panes/session/chrome/DetailsPanel.notes.test.tsx cmd/evener-hub/frontend/src/protocol/reducer.notes.test.ts
git commit -m "feat(notes): hub relay, capability, past sessions, Details UI"
```

### Task 8: TUI drawer, commands, capability mapping

**Files:**
- Modify: `cmd/evener-tui/hub_types.go` (add `SharedNotes` to `hubSessionCapabilities`; copy in `hubDetailFromThread`; add `HumanNote`, `AgentNote`, `SessionURLs` to `hubSessionDetail`; map from `thread.Evener` mirroring `Goal`)
- Modify: `cmd/evener-tui/details_drawer.go` (Shared notes section per display rule; read views + edit/remove gating on capability + `Live`)
- Modify: `cmd/evener-tui/hub_command_registry.go` (human-note edit + url-remove commands with `capabilityAvailable` on SharedNotes, mirroring goal commands)
- Modify: `cmd/evener-tui/internal/transcript/reducer.go` (handle both pushes)
- Test: `cmd/evener-tui/hub_notes_test.go` (create: mapping test covering SharedNotes; drawer render per display rule; coverage gate passes)

**Interfaces:**
- Consumes: Tasks 2, 5, 6 (wire, pushes).
- Produces: working TUI surface for Task 9.

- [ ] **Step 1: Write the failing test**

```go
func TestHubDetailFromThreadMapsSharedNotes(t *testing.T) {
	thread := appwire.Thread{... Evener: appwire.EvenerThread{
		HumanNote: "h", SessionURLs: []appwire.SessionURL{{ID: "u1", URL: "https://x.test/"}},
		Capabilities: appwire.ThreadCapabilities{SharedNotes: true},
	}}
	detail := hubDetailFromThread(thread)
	if !detail.Capabilities.SharedNotes || detail.HumanNote != "h" || len(detail.SessionURLs) != 1 {
		t.Fatalf("detail = %+v", detail)
	}
}
```

(Mirror `TestHubDetailFromThreadMapsWorkingStateFields` in `hub_status_test.go:195` for the thread fixture shape.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/evener-tui/ -run 'TestHubDetailFromThreadMapsSharedNotes' -v`
Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

`hub_types.go`: fields + copy lines (do NOT add SharedNotes to the non-live zeroing block — notes stay readable on ended sessions). `details_drawer.go`: section following the ordered display rule using `detail.Live` + capability; read-only text when not live:

```go
if detail.Capabilities.SharedNotes {
	b.WriteString(sectionLabel("shared notes"))
	b.WriteString("\n")
	if detail.HumanNote != "" {
		fmt.Fprintf(&b, "You:    %s\n", detail.HumanNote)
	}
	if detail.AgentNote != "" {
		fmt.Fprintf(&b, "Agent:  %s\n", detail.AgentNote)
	}
	for _, u := range detail.SessionURLs {
		label := u.URL
		if u.Label != "" {
			label = fmt.Sprintf("%s (%s)", u.Label, u.URL)
		}
		fmt.Fprintf(&b, "Link:   %s\n", label)
	}
	if detail.HumanNote == "" && detail.AgentNote == "" && len(detail.SessionURLs) == 0 && detail.Live {
		b.WriteString(ghostText("Add a note with /notes"))
		b.WriteString("\n")
	}
}
```

Commands: add note-edit + url-remove entries in `hub_command_registry.go` with `Available: capabilityAvailable(func(c hubSessionCapabilities) bool { return c.SharedNotes }, "source does not advertise shared notes")`, following the `sendHubGoal`/`runHubGoal` handler shape (`hub_commands.go:1074+`). Reducer: two push cases updating the cached detail (pushes must update the drawer, so real cases, not explicit ignores).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/evener-tui/ -run 'SharedNotes|Notes|DetailsDrawer' -v`
Expected: PASS. Then: `go test ./cmd/evener-tui/`
Expected: PASS (including `TestEveryWireNotificationIsHandledOrExplicitlyIgnored`)

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-tui/hub_types.go cmd/evener-tui/details_drawer.go cmd/evener-tui/hub_command_registry.go cmd/evener-tui/internal/transcript/reducer.go cmd/evener-tui/hub_notes_test.go
git commit -m "feat(notes): TUI drawer, commands, capability mapping"
```

### Task 9: E2E scenarios + full gates

**Files:**
- Create: `test/scenarios/web-notes-human-interrupts.md`, `test/scenarios/web-notes-url-add-remove.md` (mirror goal set-and-complete scenarios)
- Test: full gates

**Interfaces:**
- Consumes: Tasks 1-8 (whole feature).
- Produces: verified feature.

- [ ] **Step 1: Write scenario files**

Mirror `test/scenarios/web-goal-set-and-complete.md` structure (check `test/scenarios/INDEX.md` for the registration format; register both scenarios there too).

- [ ] **Step 2: Run the feature e2e paths**

Run: `go test ./cmd/evener-hub/ -run 'Notes' -v`
Expected: PASS. Run: `go test ./server/ ./internal/appprojector/ ./agent/ -count=1 2>&1 | tail -5`
Expected: PASS

- [ ] **Step 3: Run full gates**

Run: `make vet`
Expected: exit 0. Run: `make lint`
Expected: exit 0. Run: `make test`
Expected: exit 0 (all modules + frontend gate). On Chrome-capable hosts also run `make test-web-browser`.

- [ ] **Step 4: Commit**

```bash
git add test/scenarios/web-notes-human-interrupts.md test/scenarios/web-notes-url-add-remove.md test/scenarios/INDEX.md
git commit -m "test(notes): e2e scenarios for human interrupt and URL add/remove"
```
