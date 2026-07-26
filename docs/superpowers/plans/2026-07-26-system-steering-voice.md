# System Steering Voice Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a daemon-originated steering message announce itself as one — `◇ System steered: <kind> ▸` — with the kind carried on the wire instead of guessed from prose.

**Architecture:** An additive `Kind` field rides `SteeringInjectedData` (event), `schema.Turn` (persistence), `SerfSteeringInjectedParams` + `ThreadItem` (wire), reaching the frontend on both the live and reload paths. Seventeen kinds, set at eighteen injection sites. `SteeringItem.tsx` routes on that field; the prose classifier in `steeringClassify.ts` is deleted, keeping only its structured `<job-notification>` parsing. A new `design-system.md` §8 states the family rule the transcript's glyph gutter now enforces.

**Tech Stack:** Go 1.x (multi-module workspace), React 19 + TypeScript + CSS Modules, vitest + @testing-library/react, biome.

**Spec:** `docs/superpowers/specs/2026-07-26-system-steering-voice-design.md`

## Global Constraints

- No backward compatibility for the *kind*. Absent `Kind` renders `◇ System steered ▸` with no kind and no colon. Do not add a prose fallback.
- This does NOT extend to notification cards. Their trigger is `<job-notification>` markup — structured data that cannot false-positive — so card routing stays content-driven and a pre-`Kind` transcript still renders its cards. Reading markup is parsing; inferring a kind from prose is guessing. Only the guessing is being removed. (Task 6 has a test pinning this; it is intended behaviour, not a violation of the line above.)
- Sentence case for all UI copy. A colon promises a value — omit it when there is no kind.
- `--ink-mid` for the whole steering row, glyph and chevron included. Not `--ink-low` (2.97:1 dark / 3.64:1 light, under the 4.5:1 AA floor).
- No chromatic literals outside `tokens.css` (`src/styles/token-contract.test.ts` enforces). The steering glyph uses `currentColor` and needs no allowlist entry.
- ◇ is outside the IBM Plex latin1 subset (`global.css:23-24` declares no U+25xx range at all). It MUST be inline SVG, never a text character.
- Gates, all by exit code: **`make test` and `make lint`** from the repo root; `npm run typecheck`, `npm run lint`, `npm test` from `cmd/serf-hub/frontend`.
- **`go test ./...` from the repo root is NOT a gate.** It resolves per-module and says nothing about `agent` or `llm` — where most of this work lives (`docs/conventions/go-workspace.md:9-20`). `make test` and `make lint` loop the modules explicitly; a green `./...` is not evidence the workspace builds. If you want a fast inner-loop check while iterating, `(cd agent && go test ./...)` covers the agent module, but the gate you report is `make test`.
- Use the frontend npm scripts, NOT `npx tsc` — `npx tsc` from the repo root resolves to a decoy `tsc@2.0.4` package and silently does not typecheck.
- `make generate` regenerates BOTH `cmd/serf-hub/frontend/src/protocol/types.gen.ts` and `docs/appwire-protocol.md`. Commit both, or `make lint-generated` fails.
- Never `git stash`, never `git checkout <file>` to undo, never `npm ci`, never `git add` a directory containing the `node_modules` symlink.

## File Structure

**Go — field definitions**
- `agent/events/payloads.go` — `SteeringInjectedData.Kind` + the `SteeringKind*` constants (the enum's home).
- `agent/schema/turn.go` — `Turn.SteeringKind`, so the kind survives reload.
- `appwire/types.go` — `SerfSteeringInjectedParams.Kind`, `ThreadItem.SteeringKind`.

**Go — plumbing**
- `agent/session_queue.go` — `steeringMessage.Kind`, `trySteerEnqueue`'s kind parameter, `SteerKind`, `consumeSteeringMessage`.
- `internal/appprojector/appwire_projection.go` — live projection.
- `internal/apptranscript/apptranscript.go` — reload projection.

**Go — call sites**
- `agent/subagents.go`, `agent/session_namer.go`, `agent/session_compaction.go`, `agent/session_init.go`, `agent/session_tool_round.go`, `agent/session_self_compact.go`, `agent/session_lifecycle.go`, `agent/session_tools.go`.

**Frontend**
- `src/protocol/model.ts`, `src/protocol/reducer.ts` — carry `steeringKind` on both paths.
- `src/widgets/steeringglyph/` — new widget (`index.tsx`, `steeringglyph.module.css`, `steeringglyph.test.tsx`).
- `src/widgets/index.ts`, `src/dev/gallery-sections/steeringglyph.tsx` — barrel + gallery.
- `src/panes/session/transcript/messages/SteeringItem.tsx` + `steeringitem.module.css` — routing and the row.
- `src/panes/session/transcript/messages/steeringClassify.ts` — lose the prose patterns, keep notification parsing.

**Docs**
- `docs/web-ui/design-system.md` — new §8.

---

### Task 1: The kind enum and its fields

**Files:**
- Modify: `agent/events/payloads.go:199-206`
- Modify: `agent/schema/turn.go:123-136`
- Modify: `appwire/types.go:1323-1329`
- Test: `agent/events/payloads_test.go` (create if absent)

**Interfaces:**
- Consumes: nothing.
- Produces: `events.SteeringKind*` string constants; `events.SteeringInjectedData.Kind`; `schema.Turn.SteeringKind`; `appwire.SerfSteeringInjectedParams.Kind`; `appwire.ThreadItem.SteeringKind`. Every later task uses these exact names.

- [ ] **Step 1: Write the failing test**

In `agent/events/payloads_test.go`:

```go
package events

import "testing"

// Every kind the UI can label must exist as a constant. This list is the
// contract Task 3's call sites are checked against.
func TestSteeringKindConstants(t *testing.T) {
	want := map[string]string{
		"interrupted":        SteeringKindInterrupted,
		"agent-message":      SteeringKindAgentMessage,
		"hook-context":       SteeringKindHookContext,
		"precompact-hook":    SteeringKindPrecompactHook,
		"compact-nudge":      SteeringKindCompactNudge,
		"image-description":  SteeringKindImageDescription,
		"no-tool-calls":      SteeringKindNoToolCalls,
		"loop-detected":      SteeringKindLoopDetected,
		"tasks-done":         SteeringKindTasksDone,
		"task-nudge":         SteeringKindTaskNudge,
		"task-inactive":      SteeringKindTaskInactive,
		"note-handoff":       SteeringKindNoteHandoff,
		"goal-objective":     SteeringKindGoalObjective,
		"transcript-pointer": SteeringKindTranscriptPointer,
		"current-task":       SteeringKindCurrentTask,
		"task-list":          SteeringKindTaskList,
		"notification":       SteeringKindNotification,
	}
	for literal, got := range want {
		if got != literal {
			t.Errorf("constant for %q = %q, want %q", literal, got, literal)
		}
	}
	if len(AllSteeringKinds) != len(want) {
		t.Errorf("AllSteeringKinds has %d entries, want %d", len(AllSteeringKinds), len(want))
	}
	for _, k := range AllSteeringKinds {
		if _, ok := want[k]; !ok {
			t.Errorf("AllSteeringKinds contains unknown kind %q", k)
		}
	}
}

func TestSteeringInjectedDataCarriesKind(t *testing.T) {
	d := SteeringInjectedData{Text: "x", Kind: SteeringKindTasksDone}
	if d.Kind != "tasks-done" {
		t.Errorf("Kind = %q, want %q", d.Kind, "tasks-done")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/events/ -run TestSteeringKind -v`
Expected: FAIL — undefined: `SteeringKindInterrupted` (and the rest).

- [ ] **Step 3: Add the constants and fields**

In `agent/events/payloads.go`, directly after the existing `SteeringSourceUser` const (line ~196):

```go
// Steering kinds name what the daemon injected, set at the injection site and
// carried to the UI so a label is ground truth rather than a guess at the
// message's prose. Absent kind means "unknown" and the UI claims nothing.
const (
	SteeringKindInterrupted       = "interrupted"
	SteeringKindAgentMessage      = "agent-message"
	SteeringKindHookContext       = "hook-context"
	SteeringKindPrecompactHook    = "precompact-hook"
	SteeringKindCompactNudge      = "compact-nudge"
	SteeringKindImageDescription  = "image-description"
	SteeringKindNoToolCalls       = "no-tool-calls"
	SteeringKindLoopDetected      = "loop-detected"
	SteeringKindTasksDone         = "tasks-done"
	SteeringKindTaskNudge         = "task-nudge"
	SteeringKindTaskInactive      = "task-inactive"
	SteeringKindNoteHandoff       = "note-handoff"
	SteeringKindGoalObjective     = "goal-objective"
	SteeringKindTranscriptPointer = "transcript-pointer"
	SteeringKindCurrentTask       = "current-task"
	SteeringKindTaskList          = "task-list"
	SteeringKindNotification      = "notification"
)

// AllSteeringKinds is every kind a call site may emit. Task 3's coverage test
// asserts each one is produced somewhere; a kind that stops being emitted
// fails that test rather than going stale unnoticed (the failure mode the
// deleted read-only classifier rule demonstrated).
var AllSteeringKinds = []string{
	SteeringKindInterrupted,
	SteeringKindAgentMessage,
	SteeringKindHookContext,
	SteeringKindPrecompactHook,
	SteeringKindCompactNudge,
	SteeringKindImageDescription,
	SteeringKindNoToolCalls,
	SteeringKindLoopDetected,
	SteeringKindTasksDone,
	SteeringKindTaskNudge,
	SteeringKindTaskInactive,
	SteeringKindNoteHandoff,
	SteeringKindGoalObjective,
	SteeringKindTranscriptPointer,
	SteeringKindCurrentTask,
	SteeringKindTaskList,
	SteeringKindNotification,
}
```

Add to `SteeringInjectedData` (after `Source`):

```go
	// Kind names what was injected (events.SteeringKind*). Optional and
	// additive; absent means the daemon did not say, and the UI shows no kind.
	Kind string `json:"kind,omitempty"`
```

In `agent/schema/turn.go`, after `SteeringSource` (line ~135):

```go
	// SteeringKind records what a TurnSteering entry was (events.SteeringKind*),
	// so a reloaded transcript labels a steer the same way the live path did.
	SteeringKind string `json:"steering_kind,omitempty"`
```

In `appwire/types.go`, add to `SerfSteeringInjectedParams` (after `Source`):

```go
	Kind string `json:"kind,omitempty"`
```

And to `ThreadItem`, beside its existing `Source` field:

```go
	SteeringKind string `json:"steeringKind,omitempty"`
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/events/ -run TestSteeringKind -v`
Expected: PASS (both tests).

- [ ] **Step 5: Regenerate the wire types and confirm the build**

Run: `make generate && go build ./... && npx tsc --noEmit --incremental false --project cmd/serf-hub/frontend`
Expected: exit 0. `cmd/serf-hub/frontend/src/protocol/types.gen.ts` now has `kind?: string` on `SerfSteeringInjectedParams` and `steeringKind?: string` on `ThreadItem`.

- [ ] **Step 6: Commit**

```bash
git add agent/events/payloads.go agent/events/payloads_test.go agent/schema/turn.go appwire/types.go cmd/serf-hub/frontend/src/protocol/types.gen.ts
git commit -m "steering: add an additive Kind field and its enum

The UI infers a steering label by pattern-matching prose because the event
carries no kind. Add one at every layer it must survive: the event, the
persisted turn, and both wire params."
```

---

### Task 2: Plumb the kind through queue, emit, persist and project

**Files:**
- Modify: `agent/session_queue.go:51-69` (`steeringMessage`, `steeringInjectedDataFromMessage`), `:73-75` (`Steer`), `:125-147` (`trySteerEnqueue`), `:666-675` (`consumeSteeringMessage`)
- Modify: `internal/appprojector/appwire_projection.go:620-644`
- Modify: `internal/apptranscript/apptranscript.go:352-363`
- Test: `agent/session_queue_test.go`, `internal/appprojector/appwire_projection_test.go`, `internal/apptranscript/apptranscript_test.go`

**Interfaces:**
- Consumes: `events.SteeringKind*`, `SteeringInjectedData.Kind`, `schema.Turn.SteeringKind`, `appwire.*` fields from Task 1.
- Produces: `func (s *Session) SteerKind(msg, kind string)` — the kinded public entry point. `Steer(msg string)` remains, delegating with an empty kind; Task 3 converts every production caller to `SteerKind`, leaving `Steer` for tests.

- [ ] **Step 1: Write the failing tests**

Append to `agent/session_queue_test.go`:

```go
func TestSteerKindReachesTheInjectedEvent(t *testing.T) {
	s := newTestSession(t)
	s.SteerKind("nudge", events.SteeringKindCompactNudge)
	msgs := s.drainSteeringForTest()
	if len(msgs) != 1 {
		t.Fatalf("queued %d messages, want 1", len(msgs))
	}
	if msgs[0].Kind != events.SteeringKindCompactNudge {
		t.Errorf("queued Kind = %q, want %q", msgs[0].Kind, events.SteeringKindCompactNudge)
	}
	got := steeringInjectedDataFromMessage(msgs[0])
	if got.Kind != events.SteeringKindCompactNudge {
		t.Errorf("event Kind = %q, want %q", got.Kind, events.SteeringKindCompactNudge)
	}
}

func TestSteerLeavesKindEmpty(t *testing.T) {
	s := newTestSession(t)
	s.Steer("no kind here")
	msgs := s.drainSteeringForTest()
	if len(msgs) != 1 {
		t.Fatalf("queued %d messages, want 1", len(msgs))
	}
	if msgs[0].Kind != "" {
		t.Errorf("queued Kind = %q, want empty", msgs[0].Kind)
	}
}

func TestConsumeSteeringMessagePersistsKindOnTheTurn(t *testing.T) {
	s := newTestSession(t)
	s.consumeSteeringMessage(steeringMessage{Text: "x", Kind: events.SteeringKindLoopDetected})
	last := s.history[len(s.history)-1]
	if last.SteeringKind != events.SteeringKindLoopDetected {
		t.Errorf("turn SteeringKind = %q, want %q", last.SteeringKind, events.SteeringKindLoopDetected)
	}
}
```

If `newTestSession` / `drainSteeringForTest` do not already exist under those names, read the neighbouring tests in the file and use whatever construction and queue-inspection helpers they use — do not add new helpers if equivalents exist.

Append to `internal/appprojector/appwire_projection_test.go`:

```go
func TestSteeringInjectedProjectsKind(t *testing.T) {
	p := newTestProjector(t)
	notes := p.project(events.Event{
		Kind: events.EventSteeringInjected,
		Data: events.SteeringInjectedData{Text: "done", Kind: events.SteeringKindTasksDone},
	})
	params := notes[0].Params.(map[string]any)
	if params["kind"] != events.SteeringKindTasksDone {
		t.Errorf("kind = %v, want %q", params["kind"], events.SteeringKindTasksDone)
	}
}

func TestSteeringInjectedOmitsEmptyKind(t *testing.T) {
	p := newTestProjector(t)
	notes := p.project(events.Event{
		Kind: events.EventSteeringInjected,
		Data: events.SteeringInjectedData{Text: "mystery"},
	})
	params := notes[0].Params.(map[string]any)
	if _, present := params["kind"]; present {
		t.Error("kind key present for an unkinded steer; want it omitted")
	}
}
```

Match `newTestProjector`/`project` to the helpers the surrounding tests in that file actually use.

Append to `internal/apptranscript/apptranscript_test.go`:

```go
func TestSteeringItemCarriesKindOnReload(t *testing.T) {
	turn := schema.NewTurn(schema.TurnSteering, llm.User("done"))
	turn.SteeringKind = events.SteeringKindTasksDone
	items := itemsForTurn(turn, 0, "turn_0")
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].SteeringKind != events.SteeringKindTasksDone {
		t.Errorf("SteeringKind = %q, want %q", items[0].SteeringKind, events.SteeringKindTasksDone)
	}
}
```

Match `itemsForTurn` to the real conversion entry point used by neighbouring tests in that file.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/ ./internal/appprojector/ ./internal/apptranscript/ -run 'Steer|Steering' -v`
Expected: FAIL — undefined `SteerKind`, and `Kind`/`SteeringKind` not carried.

- [ ] **Step 3: Implement**

`agent/session_queue.go` — add to `steeringMessage`:

```go
	// Kind names what the daemon injected (events.SteeringKind*), empty when
	// the caller did not say. Surfaced on SteeringInjectedData and persisted on
	// the turn so reload labels a steer the way the live path did.
	Kind string `json:"kind,omitempty"`
```

`steeringInjectedDataFromMessage` gains `Kind: msg.Kind,`.

Add beside `Steer`:

```go
// SteerKind queues a text-only steering message naming what it is
// (events.SteeringKind*). Prefer it over Steer at every daemon injection site:
// the kind is what a reader's label is built from, and only the site knows it.
func (s *Session) SteerKind(msg, kind string) {
	_ = s.trySteerEnqueue(msg, nil, nil, "", kind)
}
```

`trySteerEnqueue` takes a trailing `kind string` parameter and sets it on the entry:

```go
	entry := steeringMessage{Text: msg, Provenance: provenance.Clone(p), Source: source, Kind: kind}
```

Update its four existing callers (`trySteerWithImagesAndProvenance` at :126, and the `SteeringSourceUser` sites at :106, :328, :377) to pass `""` as the new argument — user-sourced steering renders as a user message and never shows a kind.

`consumeSteeringMessage` sets the kind on the turn beside the source:

```go
	t.SteeringKind = msg.Kind
```

`internal/appprojector/appwire_projection.go`, after the existing `data.Source` block:

```go
		// Kind names what the daemon injected, so the UI labels a steer from
		// the wire rather than pattern-matching its prose. Omitted when unset.
		if data.Kind != "" {
			params["kind"] = data.Kind
		}
```

`internal/apptranscript/apptranscript.go`, on the steering `ThreadItem` literal beside `Source`:

```go
			SteeringKind: turn.SteeringKind,
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/ ./internal/appprojector/ ./internal/apptranscript/ -run 'Steer|Steering' -v`
Expected: PASS.

- [ ] **Step 5: Run the full Go suite**

Run: `make test`
Expected: exit 0. `trySteerEnqueue`'s signature changed, so any missed caller fails to compile here. NOT `go test ./...` — it skips the `agent` module entirely, which is where this change lives.

- [ ] **Step 6: Commit**

```bash
git add agent/session_queue.go agent/session_queue_test.go internal/appprojector/ internal/apptranscript/
git commit -m "steering: carry Kind from the queue through emit, persist and project

One enqueue primitive and one drain emitter cover all eight Steer callers;
the two projectors pass the kind to the live and reload paths."
```

---

### Task 3: Set the kind at every injection site

**Files:**
- Modify: `agent/subagents.go:696,885`; `agent/session_namer.go:292`; `agent/session_compaction.go:78,181`; `agent/session_init.go:241,1296`; `agent/session_tool_round.go:36,280,340,373`; `agent/session_self_compact.go:116`; `agent/session_lifecycle.go:646,1344`
- Modify: `agent/session_tool_registry.go:32,171` (the `steer` dep gains a kind parameter) and `agent/session_tools_task.go:194,217,224` (its three callers)
- Modify: `agent/session_tools.go:902` (`maybeInjectTaskReminder` returns `(string, string)`)
- Test: `agent/steering_kind_coverage_test.go` (create)

**Interfaces:**
- Consumes: `SteerKind` and `events.SteeringKind*` from Tasks 1-2.
- Produces: `maybeInjectTaskReminder() (text string, kind string)` — its one caller at `session_tool_round.go:373` must use the returned kind, never re-derive it from the text. `toolDeps.steer` becomes `func(msg, kind string)`, wired to `s.SteerKind`.

**Site count:** 18, not the 15 a `grep '\.Steer('` finds. Three reach the queue
through the `deps.steer` indirection in `session_tools_task.go`; that is why the
coverage test below scans for the constants rather than trusting a call-site
grep. `prependSteering` (`session_queue.go:683`) is NOT a site — it re-queues
already-built `steeringMessage` entries, which carry their kind already.

- [ ] **Step 1: Write the failing test**

Create `agent/steering_kind_coverage_test.go`:

```go
package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"serf/agent/events"
)

// Every kind in the enum must be produced by at least one non-test call site.
// This is the net that catches a kind going stale — the failure mode the
// deleted read-only classifier rule showed, where the UI kept a rule for a
// message the daemon had stopped sending and nothing noticed.
func TestEverySteeringKindHasAProducer(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var src strings.Builder
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		src.Write(b)
	}
	body := src.String()
	for _, kind := range events.AllSteeringKinds {
		constName := steeringKindConstName(kind)
		if !strings.Contains(body, constName) {
			t.Errorf("kind %q (events.%s) has no producer in agent/*.go", kind, constName)
		}
	}
}

// steeringKindConstName maps "tasks-done" to "SteeringKindTasksDone".
func steeringKindConstName(kind string) string {
	out := "SteeringKind"
	for _, part := range strings.Split(kind, "-") {
		if part == "" {
			continue
		}
		out += strings.ToUpper(part[:1]) + part[1:]
	}
	return out
}

func TestMaybeInjectTaskReminderReturnsItsKind(t *testing.T) {
	s := newTestSession(t)
	// Trigger 3: task_list never used, 10+ rounds in.
	s.totalRounds = 10
	text, kind := s.maybeInjectTaskReminder()
	if text == "" {
		t.Fatal("expected a reminder text")
	}
	if kind != events.SteeringKindTaskNudge {
		t.Errorf("kind = %q, want %q", kind, events.SteeringKindTaskNudge)
	}
}
```

Adjust the import path prefix (`serf/agent/events`) to whatever this module actually uses — read the imports of a neighbouring file in `agent/`. Adjust `s.totalRounds` assignment to whatever the surrounding tests use to reach trigger 3; if they drive it through a helper, use the helper.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run 'EverySteeringKind|MaybeInjectTaskReminder' -v`
Expected: FAIL — kinds have no producer, and `maybeInjectTaskReminder` returns one value.

- [ ] **Step 3: Convert every site**

`s.Steer(...)` → `s.SteerKind(..., events.SteeringKind…)`:

| file:line | kind constant |
|---|---|
| `subagents.go:696` | `SteeringKindCurrentTask` |
| `subagents.go:885` | `SteeringKindAgentMessage` |
| `session_namer.go:292` | `SteeringKindTaskList` |
| `session_compaction.go:78` | `SteeringKindTranscriptPointer` |
| `session_init.go:241` | `SteeringKindCurrentTask` |
| `session_tool_round.go:280` | `SteeringKindImageDescription` |
| `session_self_compact.go:116` | `SteeringKindCompactNudge` |
| `session_queue.go:175` (`deliverHookContext`) | `SteeringKindHookContext` |

`toolDeps.steer` (`session_tool_registry.go:32`) becomes `func(msg, kind string)`,
wired at `:171` to `s.SteerKind`. Its three callers pass a kind:

| file:line | kind constant |
|---|---|
| `session_tools_task.go:194` | `SteeringKindCurrentTask` |
| `session_tools_task.go:217` | `SteeringKindCurrentTask` |
| `session_tools_task.go:224` | `SteeringKindTasksDone` |

Direct `SteeringInjectedData{...}` literals gain `Kind:`:

| file:line | kind constant |
|---|---|
| `session_lifecycle.go:646` | `SteeringKindInterrupted` |
| `session_lifecycle.go:1344` | `SteeringKindNotification` |
| `session_compaction.go:181` | `SteeringKindPrecompactHook` |
| `session_init.go:1296` | `SteeringKindHookContext` |
| `session_tool_round.go:36` | `SteeringKindNoToolCalls` |
| `session_tool_round.go:340` | `SteeringKindLoopDetected` |
| `session_tool_round.go:373` | the kind returned by `maybeInjectTaskReminder` |

Change `maybeInjectTaskReminder` (`session_tools.go:902`) to `func (s *Session) maybeInjectTaskReminder() (string, string)`. It has exactly two returning triggers:

- `return taskReminderNudge()` (`:917`, never used task_list, 10+ rounds) → `return taskReminderNudge(), events.SteeringKindTaskNudge`
- `return taskReminderForInactivity(store)` (`:927`, tasks exist but untouched for 25+ rounds) → `return taskReminderForInactivity(store), events.SteeringKindTaskInactive`
- the final `return ""` → `return "", ""`

Note it does NOT produce `tasks-done`. `taskReminderAllDone()` is sent from
`session_tools_task.go:224` instead. Do not wire `tasks-done` here.

Its caller at `session_tool_round.go:372-374` becomes:

```go
		if reminder, kind := s.maybeInjectTaskReminder(); reminder != "" {
			s.appendTurn(schema.TurnSteering, llm.User(reminder))
			s.emit(events.EventSteeringInjected, events.SteeringInjectedData{Text: reminder, Kind: kind})
		}
```

The call site must NOT inspect `reminder` to decide the kind — that rebuilds the classifier in Go.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/ -run 'EverySteeringKind|MaybeInjectTaskReminder' -v`
Expected: PASS.

- [ ] **Step 5: Run the full Go suite and lint**

Run: `make test && make lint`
Expected: exit 0 for both.

- [ ] **Step 6: Commit**

```bash
git add agent/
git commit -m "steering: name what every injection site injects

Fifteen sites, fourteen kinds. maybeInjectTaskReminder returns its kind
rather than letting the call site re-derive it from the text it just built."
```

---

### Task 4: Carry the kind to the frontend model

**Files:**
- Modify: `cmd/serf-hub/frontend/src/protocol/model.ts:15-45` (`ItemModel`)
- Modify: `cmd/serf-hub/frontend/src/protocol/reducer.ts:755-784` (live path) and its `wireItemToModel` (snapshot path)
- Test: `cmd/serf-hub/frontend/src/protocol/reducer.test.ts`

**Interfaces:**
- Consumes: `types.gen.ts`'s regenerated `SerfSteeringInjectedParams.kind` and `ThreadItem.steeringKind` from Task 1.
- Produces: `ItemModel.steeringKind?: string` — Task 6 routes on it.

- [ ] **Step 1: Write the failing tests**

Append to `src/protocol/reducer.test.ts`, matching the file's existing helper style for building a model and applying a notification:

```ts
test("a live steer carries its wire kind onto the item", () => {
  const model = withActiveTurn("turn_1");
  const next = reduce(model, notification("serf/steering/injected", {
    threadId: model.threadId,
    text: "You have completed all tasks",
    kind: "tasks-done",
  }));
  const item = lastItem(next, "turn_1");
  expect(item.type).toBe("steering");
  expect(item.steeringKind).toBe("tasks-done");
});

test("a live steer with no wire kind leaves steeringKind undefined", () => {
  const model = withActiveTurn("turn_1");
  const next = reduce(model, notification("serf/steering/injected", {
    threadId: model.threadId,
    text: "something unclassified",
  }));
  expect(lastItem(next, "turn_1").steeringKind).toBeUndefined();
});

test("a reloaded steering item carries steeringKind from the snapshot", () => {
  const item = wireItemToModel({
    type: "steering",
    id: "item_steering_0",
    turnId: "turn_0",
    text: "done",
    steeringKind: "tasks-done",
  });
  expect(item.steeringKind).toBe("tasks-done");
});
```

Use the file's real helpers (`withActiveTurn`, `notification`, `lastItem` are illustrative names) — read the neighbouring steering tests and reuse exactly what they use.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npm test -- src/protocol/reducer.test.ts`
Expected: FAIL — `steeringKind` is undefined in all three.

- [ ] **Step 3: Implement**

`src/protocol/model.ts`, on `ItemModel` beside `eventKind`:

```ts
  // The wire's steering kind (events.SteeringKind* on the Go side): what the
  // daemon injected, named at the injection site. The transcript labels a
  // steer from this rather than pattern-matching its prose. Undefined for
  // non-steering items, for user-sourced steering, and for a steer projected
  // by a daemon predating the field — in which case the UI shows no kind.
  steeringKind?: string;
```

`src/protocol/reducer.ts`, in the live `serf/steering/injected` item literal, beside `source`:

```ts
            steeringKind: params.kind,
```

In `wireItemToModel`, carry it the same way the existing `source`/`eventKind` fields are carried:

```ts
    steeringKind: wire.steeringKind,
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npm test -- src/protocol/reducer.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/frontend/src/protocol/
git commit -m "steering: carry the wire kind onto ItemModel on both paths

Live notification and snapshot reload both populate steeringKind, so a
reloaded transcript labels a steer the way the live one did."
```

---

### Task 5: The SteeringGlyph widget

**Files:**
- Create: `cmd/serf-hub/frontend/src/widgets/steeringglyph/index.tsx`
- Create: `cmd/serf-hub/frontend/src/widgets/steeringglyph/steeringglyph.module.css`
- Create: `cmd/serf-hub/frontend/src/widgets/steeringglyph/steeringglyph.test.tsx`
- Create: `cmd/serf-hub/frontend/src/dev/gallery-sections/steeringglyph.tsx`
- Modify: `cmd/serf-hub/frontend/src/widgets/index.ts:30` (barrel, beside `FailureGlyph`)

**Interfaces:**
- Consumes: nothing.
- Produces: `export function SteeringGlyph(): JSX.Element` — no props. Renders an `aria-hidden` `<span data-testid="steering-glyph">` wrapping a 10×10 SVG in `currentColor`.

- [ ] **Step 1: Write the failing test**

Create `src/widgets/steeringglyph/steeringglyph.test.tsx`:

```tsx
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { SteeringGlyph } from ".";

afterEach(cleanup);

test("renders the mark", () => {
  render(<SteeringGlyph />);
  expect(screen.getByTestId("steering-glyph")).toBeTruthy();
});

// The row's own text ("System steered: Tasks done") is the summary's
// accessible name and already says what the glyph says. Unlike FailureGlyph,
// which is often the only failure signal on its row, this is never the only
// signal - so naming it would make a screen reader say it twice.
test("is decorative - no accessible name of its own", () => {
  const { container } = render(<SteeringGlyph />);
  const el = container.querySelector('[data-testid="steering-glyph"]');
  expect(el?.getAttribute("aria-hidden")).toBe("true");
  expect(el?.getAttribute("aria-label")).toBeNull();
  expect(el?.getAttribute("role")).toBeNull();
});

// U+25C7 is outside the IBM Plex latin1 subset (global.css:23-24), so a
// literal would render from a system fallback font.
test("draws SVG, never the ◇ character", () => {
  const { container } = render(<SteeringGlyph />);
  expect(container.querySelector("svg")).toBeTruthy();
  expect(container.textContent).not.toContain("◇");
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/serf-hub/frontend && npm test -- src/widgets/steeringglyph/`
Expected: FAIL — cannot resolve `.`.

- [ ] **Step 3: Implement**

`src/widgets/steeringglyph/index.tsx`:

```tsx
import { requireClass } from "../internal/requireClass";
import styles from "./steeringglyph.module.css";

const CLASS = {
  glyph: requireClass(styles.glyph, "steeringglyph.module.css", "glyph"),
};

/** The ◇ that marks a system steering message: a hollow diamond, sized to sit
 * on a line of text.
 *
 * SVG rather than the U+25C7 character because global.css subsets IBM Plex Sans
 * to a unicode-range that stops at U+203A and resumes at U+2044 - a literal ◇
 * would be the one glyph in the app rendering from a system fallback font.
 *
 * Decorative and unnamed, unlike FailureGlyph: the row's own text says
 * "System steered: <kind>", so naming the glyph would repeat it. Inherits
 * currentColor, so it needs no token-contract allowlist entry. */
export function SteeringGlyph() {
  return (
    <span aria-hidden="true" className={CLASS.glyph} data-testid="steering-glyph">
      <svg viewBox="0 0 10 10" width="10" height="10" aria-hidden="true">
        <path
          d="M5 1.2 V4.4 M2.3 2.9 L7.7 6.1 M7.7 2.9 L2.3 6.1 M1.8 8.4 H8.2"
          stroke="currentColor"
          strokeWidth="1.1"
          strokeLinecap="round"
        />
      </svg>
    </span>
  );
}
```

`src/widgets/steeringglyph/steeringglyph.module.css`:

```css
/* Inherits currentColor from the steering row (--ink-mid), so this stylesheet
 * references no colour token at all and needs no token-contract allowlist
 * entry - the allowlist gates the three attention hues, and this uses none. */
.glyph {
  display: inline-flex;
  flex: none;
  align-items: center;
}
```

`src/widgets/index.ts`, beside the `FailureGlyph` export:

```ts
export { SteeringGlyph } from "./steeringglyph";
```

`src/dev/gallery-sections/steeringglyph.tsx` — read `src/dev/gallery-sections/failureglyph.tsx` and mirror its structure exactly, substituting the steering glyph. The gallery completeness test (`src/dev/WidgetGallery.test.tsx`) requires one section per widget in the barrel.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npm test -- src/widgets/steeringglyph/ src/dev/WidgetGallery.test.tsx src/styles/token-contract.test.ts`
Expected: PASS for all three.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/frontend/src/widgets/steeringglyph/ cmd/serf-hub/frontend/src/widgets/index.ts cmd/serf-hub/frontend/src/dev/gallery-sections/steeringglyph.tsx
git commit -m "widgets: add SteeringGlyph, the ◇ that marks a system steer

SVG because U+25C7 falls in a gap in the Plex latin1 subset. Decorative,
unlike FailureGlyph - the row's text already names it."
```

---

### Task 6: Route on the kind and build the row

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/SteeringItem.tsx` (whole file)
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/steeringitem.module.css`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/steeringClassify.ts` (delete the prose classifier)
- Test: `SteeringItem.test.tsx`, `steeringClassify.test.ts`

**Interfaces:**
- Consumes: `ItemModel.steeringKind` (Task 4), `SteeringGlyph` (Task 5).
- Produces: nothing downstream.

- [ ] **Step 1: Write the failing tests**

In `steeringClassify.ts`, the surviving exports are `stripSystemReminder`, `parseSteeringNotifications`, `ParsedNotification`, `NotificationTone`. Add to `SteeringItem.test.tsx`:

```tsx
test("labels a steer from its wire kind", () => {
  render(<SteeringItem item={item({ text: "You have completed all tasks", steeringKind: "tasks-done" })} turn={turn} live={false} />);
  expect(screen.getByText("System steered: Tasks done")).toBeTruthy();
  expect(screen.getByTestId("steering-glyph")).toBeTruthy();
});

// No kind means the daemon did not say. A colon promises a value.
test("claims nothing when the wire carries no kind", () => {
  render(<SteeringItem item={item({ text: "unclassifiable" })} turn={turn} live={false} />);
  expect(screen.getByText("System steered")).toBeTruthy();
  expect(screen.queryByText(/System steered:/)).toBeNull();
});

// The prose that used to drive classification must no longer do so.
test("does not infer a kind from the text", () => {
  render(<SteeringItem item={item({ text: "You have completed all tasks on your task list." })} turn={turn} live={false} />);
  expect(screen.getByText("System steered")).toBeTruthy();
  expect(screen.queryByText(/Tasks done/)).toBeNull();
});

test.each([
  ["current-task"],
  ["task-list"],
])("suppresses %s - the tasks panel owns that surface", (kind) => {
  const { container } = render(<SteeringItem item={item({ text: "x", steeringKind: kind })} turn={turn} live={false} />);
  expect(container.firstChild).toBeNull();
});

test("routes a notification kind to a card", () => {
  const text = '<job-notification job_id="j1" status="completed">done\nexcerpt:\nall good</job-notification>';
  render(<SteeringItem item={item({ text, steeringKind: "notification" })} turn={turn} live={false} />);
  expect(screen.getByTestId("notification-card")).toBeTruthy();
});

// The card's trigger is <job-notification> markup, not the kind: structured
// markup cannot false-positive the way a prose pattern can, so a pre-Kind
// transcript still gets its card.
test("a pre-Kind steer carrying a job-notification block still renders a card", () => {
  const text = '<job-notification job_id="j1" status="completed">done\nexcerpt:\nall good</job-notification>';
  render(<SteeringItem item={item({ text })} turn={turn} live={false} />);
  expect(screen.getByTestId("notification-card")).toBeTruthy();
});

test("the chevron trails the label", () => {
  render(<SteeringItem item={item({ text: "x", steeringKind: "loop-detected" })} turn={turn} live={false} />);
  const summary = screen.getByTestId("steering-item").querySelector("summary");
  const kids = Array.from(summary?.children ?? []);
  expect(kids[0]?.getAttribute("data-testid")).toBe("steering-glyph");
  expect(kids[kids.length - 1]?.getAttribute("data-testid")).toBe("steering-chevron");
});

test("the body opens with the SYSTEM-REMINDER wrapper stripped", () => {
  render(<SteeringItem item={item({ text: "<SYSTEM-REMINDER>the note</SYSTEM-REMINDER>", steeringKind: "hook-context" })} turn={turn} live={false} />);
  fireEvent.click(screen.getByText("System steered: Hook context"));
  expect(screen.getByText("the note")).toBeTruthy();
});
```

Also delete every existing test in `steeringClassify.test.ts` that asserts a prose-inferred kind (`current-task`, `full-list`, `tasks-done`, `task-nudge`, `loop`, `read-only`, `transcript`, `unknown`), and every test in `SteeringItem.test.tsx` asserting a prose-derived divider label. Keep and keep passing every notification-parsing test.

Add to `steeringClassify.test.ts`:

```ts
test("no longer exports a prose classifier", async () => {
  const mod = await import("./steeringClassify");
  expect("classifySteering" in mod).toBe(false);
  expect("steeringTreatment" in mod).toBe(false);
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npm test -- src/panes/session/transcript/messages/`
Expected: FAIL on the new assertions.

- [ ] **Step 3: Implement**

In `steeringClassify.ts`, delete `SteeringKind`, `SteeringClass`, `SteeringTreatment`, `steeringTreatment`, `classifySteering`, `classifyStripped`, and every regex in the classification cascade — including the `read-only` rule, which matches a string the Go source never emits. Export `stripSystemReminder`. Replace the notification entry point with:

```ts
// Notification blocks are STRUCTURED markup (<job-notification …>) and a fixed
// "Observer callback:\n" header, so reading them is parsing, not guessing: they
// cannot false-positive the way a prose pattern like /completed all tasks/ can.
// This is why the card's trigger stayed content-driven while the kind moved to
// the wire, and why a pre-Kind transcript still renders its cards.
export function parseSteeringNotifications(text: string): {
  notifications: ParsedNotification[];
  leftover: string;
} {
  const stripped = stripSystemReminder(text);
  const { blocks, leftover } = splitJobNotificationBlocks(stripped);
  const notifications = blocks.map(parseJobNotification).filter((n): n is ParsedNotification => n !== null);
  if (notifications.length > 0) return { notifications, leftover };
  const observer = parseObserverCallback(stripped);
  if (observer) return { notifications: [observer], leftover: "" };
  return { notifications: [], leftover: stripped };
}
```

Keep `parseQuotedAttrs`, `splitJobNotificationBlocks`, `splitNotificationExcerpt`, `compactStringArray`, `parseCommunicateEnvelope`, `notificationTone`, `titleForJobNotification`, `notificationSecondary`, `parseJobNotification`, `parseObserverCallback`, `ParsedNotification`, `NotificationTone` unchanged.

Rewrite `SteeringItem.tsx`'s daemon branch:

```tsx
// Labels for the wire's steering kinds (events.SteeringKind* on the Go side).
// A kind with no entry here renders unlabelled rather than showing its raw
// slug: an unknown kind means this UI is older than the daemon, and inventing
// a label from a slug is the guessing this field exists to end.
const KIND_LABELS: Record<string, string> = {
  interrupted: "Interrupted",
  "agent-message": "Message sent",
  "hook-context": "Hook context",
  "precompact-hook": "Pre-compact hook",
  "compact-nudge": "Compaction nudge",
  "image-description": "Image description",
  "no-tool-calls": "No tool calls",
  "loop-detected": "Loop detection",
  "tasks-done": "Tasks done",
  "task-nudge": "Task nudge",
  "task-inactive": "Task list idle",
  "note-handoff": "Note to self",
  "goal-objective": "Goal objective",
  "transcript-pointer": "Transcript pointer",
};

// The tasks panel and the task-update card already own these surfaces
// (parity-m4 §8:209-217), so they render nothing inline.
const SUPPRESSED = new Set(["current-task", "task-list"]);
```

The row:

```tsx
function SteeringDivider({ id, label, text }: { id: string; label: string; text: string }) {
  const open = isDisclosureOpen(id, false);
  return (
    <details className={CLASS.details} data-testid="steering-item" open={open}>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: <summary> is natively keyboard-operable; controlled to keep the store the single source of truth (see ToolCallItem.tsx) */}
      <summary
        className={CLASS.summary}
        onClick={(e) => {
          e.preventDefault();
          toggleDisclosure(id, false);
        }}
      >
        <SteeringGlyph />
        <span className={CLASS.label}>{label}</span>
        <span className={CLASS.chevron} aria-hidden="true" data-open={open ? "true" : "false"} data-testid="steering-chevron">
          ▸
        </span>
      </summary>
      <pre className={CLASS.body}>{text}</pre>
    </details>
  );
}
```

And the component body, replacing the classify/treatment cascade:

```tsx
  if (item.source === "user") return <UserMessageView item={item} opensExchange={false} />;
  if (!item.text) return null;
  const kind = item.steeringKind ?? "";
  if (SUPPRESSED.has(kind)) return null;

  // Card routing stays content-driven: the trigger is <job-notification>
  // markup, which cannot false-positive, so a steer projected before the kind
  // field existed still renders its cards.
  const { notifications, leftover } = parseSteeringNotifications(item.text);
  if (notifications.length > 0) {
    return (
      <>
        {notifications.map((n) => (
          <NotificationCard key={n.rawText} notification={n} />
        ))}
        {leftover && <SteeringDivider id={item.id} label={STEERED} text={leftover} />}
      </>
    );
  }

  const label = KIND_LABELS[kind];
  return <SteeringDivider id={item.id} label={label ? `${STEERED}: ${label}` : STEERED} text={leftover} />;
```

with `const STEERED = "System steered";` at module scope. Add `label` and `chevron` to the `CLASS` map, import `SteeringGlyph` from `../../../../widgets`, and drop the now-unused `sentenceCase` helper and `classifySteering`/`steeringTreatment` imports.

`steeringitem.module.css` — replace `.summary` and add the two new classes:

```css
/* One 12px gutter column, one ink for the whole row (design-system.md §8).
 * --ink-mid, not --ink-low: --ink-low measures 2.97:1 dark / 3.64:1 light
 * against --surface-1, under the 4.5:1 AA floor, and the kind is the payload a
 * reader auditing steering came for. */
.summary {
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
  cursor: pointer;
  list-style: none;
  font-family: var(--font-sans);
  font-size: var(--font-size-caption);
  color: var(--ink-mid);
}

.summary::-webkit-details-marker {
  display: none;
}

.label {
  flex: 1 1 auto;
  min-width: 0;
}

/* Trailing, and on the row's own colour rather than a step down: it reads as
 * the end of the label line, not as a leading affordance. */
.chevron {
  display: inline-flex;
  flex: none;
  font-size: 9px;
}

.chevron[data-open="true"] {
  transform: rotate(90deg);
}

@media (prefers-reduced-motion: no-preference) {
  .chevron {
    transition: transform var(--motion-duration-overlay) var(--motion-easing-standard);
  }
}
```

Delete the `.detail` rule — the divider no longer renders a detail suffix.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npm test -- src/panes/session/transcript/messages/`
Expected: PASS.

- [ ] **Step 5: Run every frontend gate**

Run: `cd cmd/serf-hub/frontend && npm run typecheck && npm run lint && npm test`
Expected: exit 0 for all three. Deleting exports breaks any other importer — the typecheck is what catches it.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/frontend/src/panes/session/transcript/messages/
git commit -m "steering: label a steer from the wire, and mark it as one

Routes on ItemModel.steeringKind and deletes the prose classifier, including
its read-only rule for a message the daemon never sent. Card routing stays
content-driven: <job-notification> is markup, not a prose guess."
```

---

### Task 7: The style-guide section

**Files:**
- Modify: `docs/web-ui/design-system.md` (insert §8 before the current §7 "Known gaps", renumbering it to §8 → §9)

**Interfaces:**
- Consumes: everything above, as shipped.
- Produces: nothing.

- [ ] **Step 1: Verify the implementation matches what you are about to document**

Run: `cd cmd/serf-hub/frontend && grep -n "ink-mid\|space-2\|align-items" src/panes/session/transcript/messages/steeringitem.module.css`
Expected: confirms the row is `--ink-mid` with a `var(--space-2)` gutter gap. Document what is actually there; if it differs from the spec, the code is the truth and the doc follows it.

- [ ] **Step 2: Write §8**

Insert before "## 7. Known gaps", and renumber that heading to "## 9. Known gaps" after inserting:

```markdown
## 8. The system voice

Three voices appear in a transcript: the human, the agent, and the system
steering the agent. The first two are marked. This section marks the third.

**The rule: a glyph in the gutter means the agent's instructions changed. An
empty gutter means it is a passive fact.**

The transcript already has a 12px glyph gutter — `toolcallitem`'s `.row` and
`systemnoticeitem`'s `.failure` share one `display: flex; align-items: baseline;
gap: var(--space-2)` grammar. This section assigns that column.

| gutter | member | treatment |
|---|---|---|
| `◇` | **steering** | `SteeringGlyph`, `--ink-mid` for the whole row, kind from the wire, chevron trailing |
| `✗` | **failure** | `FailureGlyph` in `--danger`, text in `--ink-hi` |
| *(empty)* | **lifecycle fact** | `--ink-low` one-liner; a run of 3+ collapses into one disclosure |
| `▸` box | **scaffolding** | hairline-bordered box: the system prompt, compaction summaries, round timings |

Notification cards sit outside the rule — a card is not a row and has no gutter.

**Steering labels come from the wire, never from the text.** `SteeringInjectedData.Kind`
(`agent/events/payloads.go`) is set at each injection site and reaches the
renderer on both the live and reload paths. A steer with no kind renders
`System steered` with no colon: a colon promises a value, and the UI does not
guess at one.

**The two sides cannot drift.** `make generate` emits the Go enum into
`types.gen.ts` as `STEERING_KINDS` plus the union `SteeringKind`, and
`SteeringItem.tsx` types its label map as `Record<LabelledKind, string>` over that
union. Adding a kind in Go and regenerating fails `tsc` with a missing-key error
naming the kind, until it is given a label, suppressed, or routed to a card. This
is the only mechanism enforcing that — deliberately, since a second one covering
the same property would be worse than either alone.

This replaced a prose classifier that pattern-matched steering text to infer a
kind. It knew 8 patterns against 15 injection sites, and one of its rules
matched `/reading without writing/` — a string the daemon has never sent. That
is the failure mode this rule exists to prevent: a renderer's idea of what the
daemon says, drifting silently from what it says.

**Why `--ink-mid` and not `--ink-low`.** Every other quiet system row uses
`--ink-low`. Measured against `--surface-1` it is 2.97:1 in dark and 3.64:1 in
light — under the 4.5:1 AA floor, as `usermessageitem.module.css` and
`systemnoticeitem.module.css` both already record. A reader scanning steering is
auditing which kind fired, so the kind is the payload rather than furniture, and
it sits one ink step up at 6.86:1 / 6.56:1. That step also separates a steer
from the lifecycle line beneath it by weight as well as by glyph.

**The glyph is SVG, not the character.** `global.css`'s `unicode-range` subsets IBM Plex Sans with no
U+25xx block at all, so a literal ◇ would be the only glyph in the app rendering
from a system fallback font. `SteeringGlyph` draws it, inherits `currentColor`, and — unlike
`FailureGlyph` — carries no accessible name, because the row's own text already
says "System steered: <kind>".
```

- [ ] **Step 3: Verify every claim in the section**

Run:

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf/.claude/worktrees/kh-steering-voice
grep -n "U+2039-203A, U+2044" cmd/serf-hub/frontend/src/styles/global.css
grep -rn "reading without writing" --include="*.go" . | grep -v _test | wc -l   # must be 0
grep -c "SteeringKind" agent/events/payloads.go                                  # must be > 0
```

Expected: the unicode-range line prints, the Go grep counts 0, the constant count is non-zero. A claim that does not verify gets corrected in the doc, not left standing.

- [ ] **Step 4: Commit**

```bash
git add docs/web-ui/design-system.md
git commit -m "design-system: §8, the system voice

States the rule the glyph gutter now enforces, and why steering's label comes
from the wire rather than from its prose."
```

---

---

### Task 8: Generate the kind union, so drift is a compile error

**Files:**
- Modify: `internal/appwirets/emit.go` (add one catalog + one import)
- Modify: `cmd/serf-hub/frontend/src/protocol/types.gen.ts` (regenerated, never hand-edited)
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/SteeringItem.tsx`
- Test: `internal/appwirets/emit_test.go`

**Interfaces:**
- Consumes: `events.AllSteeringKinds` (Task 1), `KIND_LABELS` / `SUPPRESSED` (Task 6).
- Produces: `STEERING_KINDS` (a `readonly string[]` const) and the union type `SteeringKind` in `types.gen.ts`.

**Why this exists.** `KIND_LABELS` in the frontend and the `SteeringKind*` enum in Go
agree today. Nothing makes them keep agreeing. A kind added on the Go side later
renders unlabelled and silently — which is the exact failure this whole plan removes,
one layer up, and it is not hypothetical: the deleted `read-only` classifier rule went
stale precisely this way when a separate plan removed its Go-side feature, and no test
anywhere failed.

A generated union makes that a **compile error** rather than a test failure, using
machinery the repo already has: `writeNameCatalog` (`internal/appwirets/emit.go:265`)
already emits a `const [...] as const` plus a derived union and is called twice today,
and `TestGeneratedFileCurrent` (`internal/appwirets/emit_test.go:507`) already fails when
the committed output is stale.

**Deliberately NO runtime test asserting every kind is labelled.** The `Record<>` type
below is the mechanism; a test would be a second, weaker mechanism for the same
guarantee, and two mechanisms for one property is worse than either alone.

- [ ] **Step 1: Write the failing generator test**

Append to `internal/appwirets/emit_test.go`, matching the file's existing style:

```go
func TestEmitsSteeringKindCatalog(t *testing.T) {
	out := Generate()
	if !strings.Contains(out, "export const STEERING_KINDS = [") {
		t.Error("generated output has no STEERING_KINDS catalog")
	}
	if !strings.Contains(out, "export type SteeringKind = (typeof STEERING_KINDS)[number];") {
		t.Error("generated output has no SteeringKind union")
	}
	for _, kind := range events.AllSteeringKinds {
		if !strings.Contains(out, fmt.Sprintf("%q", kind)) {
			t.Errorf("kind %q missing from generated output", kind)
		}
	}
}
```

Match `Generate()` to the generator's real entry point — read the neighbouring tests
rather than assuming that name.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/appwirets/ -run TestEmitsSteeringKindCatalog -v`
Expected: FAIL — no `STEERING_KINDS` in the output.

- [ ] **Step 3: Emit the catalog**

In `internal/appwirets/emit.go`, add the import `"primeradiant.com/serf/agent/events"`
(cross-module, already done by `internal/appprojector`), then one call beside the two
existing catalogs at `:326`/`:332`:

```go
	writeNameCatalog(&b, "STEERING_KINDS", "SteeringKind", events.AllSteeringKinds)
```

Emission order is deterministic output — put it adjacent to the existing two, and do not
sort or reorder `AllSteeringKinds`.

- [ ] **Step 4: Regenerate and confirm both gates**

Run: `make generate && go test ./internal/appwirets/ && make lint-generated`
Expected: exit 0. `types.gen.ts` now has `STEERING_KINDS` and `SteeringKind`.
Commit BOTH regenerated files — `types.gen.ts` and `docs/appwire-protocol.md`.

- [ ] **Step 5: Make the frontend's label map exhaustive**

In `SteeringItem.tsx`, import the generated type and constrain the map:

```tsx
import type { SteeringKind } from "../../../../protocol/types.gen";

// current-task and task-list are suppressed (the tasks panel owns them) and
// notification routes to a card, so those three carry no label. Every OTHER
// kind the daemon can emit must have one: this Record is exhaustive over the
// generated union, so adding a kind in Go and regenerating fails the build here
// until it is given a label. That is the point - the frontend's idea of what
// the daemon sends cannot drift from what it sends.
type LabelledKind = Exclude<SteeringKind, "current-task" | "task-list" | "notification">;

const KIND_LABELS: Record<LabelledKind, string> = { /* unchanged entries */ };
```

`item.steeringKind` is a plain `string | undefined`, and an unknown kind must still
degrade gracefully rather than throw, so the lookup stays tolerant:

```tsx
function labelFor(kind: string): string | undefined {
  return Object.hasOwn(KIND_LABELS, kind) ? KIND_LABELS[kind as LabelledKind] : undefined;
}
```

Type `SUPPRESSED` as `ReadonlySet<SteeringKind>` so a typo in it fails too. Keep every
existing test passing — behaviour does not change here, only its type safety.

- [ ] **Step 6: Prove the guard actually guards**

Temporarily add a kind to the Go const block and `AllSteeringKinds`, run `make generate`,
then `npm run typecheck` from `cmd/serf-hub/frontend`. It MUST fail with a missing-key
error naming your new kind. Revert the Go change, regenerate, confirm green. Report the
exact error text you saw — a guard nobody has seen fire is not a guard.

- [ ] **Step 7: Commit**

```bash
git add internal/appwirets/ cmd/serf-hub/frontend/src/protocol/types.gen.ts docs/appwire-protocol.md cmd/serf-hub/frontend/src/panes/session/transcript/messages/SteeringItem.tsx
git commit -m "steering: generate the kind union so drift fails the build

KIND_LABELS and the Go enum agreed by inspection and nothing kept them
agreeing. The generated union makes a new Go kind a missing-key error in
tsc, using the catalog helper and drift test the generator already has."
```


## Final verification

- [ ] Run every gate from the repo root:

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf/.claude/worktrees/kh-steering-voice
make test && make lint
cd cmd/serf-hub/frontend && npm run typecheck && npm run lint && npm test
```

Expected: exit 0 for every command. Report the actual exit codes, not a summary of a scrolled log.

- [ ] Confirm nothing outside this feature moved: `git status --short` shows only files this plan names.
