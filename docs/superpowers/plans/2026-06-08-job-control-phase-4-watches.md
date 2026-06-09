# Job Control — Phase 4: watches + observer sidecars (Implementation Plan)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `job_watch` — extra triggers on a running job or a visible session — and the observer-sidecar composition. A watch fires on an `output_match` regex, a periodic `progress_interval_ms`, or selected session/job `events`; it either notifies the caller (`send` omitted) or delivers a bounded frame to another target via `job_send_message` (`send` present). Observers are not a new tool: they are `delegate` + `job_watch(send={to:"job_obs"})` + alias-target `job_send_message`, composed.

**Architecture:** The seam from spec §8 is mandatory. The pure `output_match` matcher lives in `agent/internal/jobstore/watch.go` — RE2 over the append stream, line-buffered so a match in bytes appended while the watch is active is **never** missed by preview eviction; it imports only stdlib + `regexp` and **cannot** see session event kinds. The `events`/`trigger` event-frame gating, the periodic timer, the watch registry keyed `(visible_session_id, target, send.to)`, and delivery all live in the **JobManager** (package `agent`), because event kinds are `agent/events` concepts a `Session`-free package cannot name. The JobManager taps the one event choke point — `Session.emit` (`agent/session_events.go:45`) — to feed the event-frame matcher, wraps the per-job output writer to feed the `output_match` matcher, and owns the Nth-event counter for `trigger.every`. The `job_watch` tool registers alongside the legacy subagent tools (Phase 6 removes the legacy surface).

**Tech Stack:** Go, `agent/internal/jobstore` (Phase 1), the `JobManager` + `jobNotification` queue (Phase 2), `job_send_message` (Phase 3), `agent/events` (`EventKind` constants), the tool registry (`tool.RegisteredTool`/`Exec`). Module: `primeradiant.com/serf/agent`.

This is **Phase 4 of 6**, implementing spec `docs/superpowers/specs/2026-06-08-job-control-design.md` §5.9 (`job_watch`), §8 (watches internals — the seam split), §9 (observer/sidecar composition), §5.10 (errors). It depends on Phase 1 (`agent/internal/jobstore`), Phase 2 (`JobManager`, `pendingJobNotifs`), and Phase 3 (`job_send_message`) being merged.

**Conventions:** run Go commands from `/Users/jesse/prime-radiant/toil-suite/serf/agent`; package tests with `cd agent && go test ./... -run <name>`. Pure-matcher tests: `cd agent && go test ./internal/jobstore/ -run <name> -v`. Commit per task. Full `make test` + `make lint` from repo root before the final task.

---

## Dependency note: the Phase 3 `job_send_message` Go seam

Phase 3 ships the delivery as a **`*Session` method** (these types are owned by Phase 3 —
`agent/job_delegate.go`; **do NOT redeclare them here**, that is a compile error):

```go
type sendMessageArgs struct {
	Target         string // job_id or alias: caller|main|watched
	Message        string
	OnFinished     string // "resume" (default) | "fail"
	Background     bool
	BlockTimeoutMS int
}
type sendMessageResult struct {
	Target, JobID, Type string
	Status              jobstore.Status
	RunningInBackground bool
	Action              string // "sent" | "resumed"
	ResumedFromJobID    string
	TranscriptRef       string
	Delivered           bool   // alias targets
	MessageType         string // "runtime" for alias targets
	Err                 error  // the method returns by value; errors ride here
}
func (s *Session) sendDelegateMessage(ctx context.Context, args sendMessageArgs) sendMessageResult
```

A fired watch's `send` reuses this (spec §5.5: "`job_send_message` is also the substrate for
`job_watch.send`"). Because the watch registry lives in the `jobManager` but `sendDelegateMessage`
is a `*Session` method, this phase gives the `jobManager` an **injected** delivery func (mirroring
Phase 2's injected `enqueue`/`now`):

```go
// added to the jobManager struct (Phase 4):
send func(context.Context, sendMessageArgs) sendMessageResult
```

wired to `s.sendDelegateMessage` when the session builds the manager, and set to a capture in tests.
**This phase also adds a `FromWatch bool` field to Phase 3's `sendMessageArgs`** plus the matching
behavior in `sendDelegateMessage`: when `FromWatch` is true, skip the caller-role re-authorization the
watch already performed at configure time and exclude the delivery from observer telemetry to avoid
feedback loops (spec §9). Before writing Tasks 6/9, `grep -n "func (s \*Session) sendDelegateMessage\|type sendMessageArgs" agent/job_delegate.go`
to confirm the real names and adapt if Phase 3 drifted. The watch registry and matcher (Tasks 1–5)
have **no** Phase 3 dependency and can be built first.

---

## Shared contracts this phase establishes

```go
// agent/internal/jobstore/watch.go — the pure output_match matcher (no agent/events import)
type OutputMatcher struct {
	re      *regexp.Regexp
	carry   []byte // trailing partial line held across Feed calls (no silent miss)
}
func NewOutputMatcher(re *regexp.Regexp) *OutputMatcher
// Feed appends a chunk of newly-produced output and returns each newly-completed
// line that matches re. Lines are split on '\n'; a trailing partial line is held
// in carry until the next Feed completes it, so a match never falls between chunks.
func (m *OutputMatcher) Feed(chunk []byte) []string
// Flush returns a match for any buffered final partial line (call when the job ends).
func (m *OutputMatcher) Flush() []string

// agent/job_watch.go — the JobManager-side registry + gating + delivery (package agent)
type watchKey struct {
	VisibleSessionID string
	Target           string // job_id | caller | main | watched | *
	SendTo           string // "" for a caller-notification watch
}
type watchConfig struct {
	OutputMatch    *regexp.Regexp // nil if not configured
	matcher        *jobstore.OutputMatcher
	ProgressEveryMS int
	EventKinds     map[events.EventKind]bool // resolved from model-facing names; nil unless events configured
	wildcardEvents bool                      // events:["*"]
	TriggerEvent   events.EventKind          // trigger.event (zero value = none)
	TriggerEvery   int                       // Nth-event modulus; counter below
	eventCount     int                       // monotonic count of gated events (for trigger.every)
	Send           *watchSend                // nil → notify the caller
	progressStop   chan struct{}             // closes when the progress timer is torn down
}
type watchSend struct {
	To             string
	Message        string
	IncludeFrame   bool
	IncludeExcerpt bool
}
```

The registry lives on the `jobManager` added in Phase 2:

```go
// added to jobManager (agent/jobs.go) in Task 3
watches map[watchKey]*watchConfig
```

---

## File structure

```
agent/internal/jobstore/
  watch.go        OutputMatcher: NewOutputMatcher / Feed / Flush (pure, RE2, no-silent-miss)
  watch_test.go

agent/
  job_watch.go         JobManager-side: watch registry; configure/clear; event-frame gating
                       (the s.emit tap); progress timer; output-tap wiring; send-vs-notify delivery
  job_watch_test.go
  job_watch_observer_test.go   observer-sidecar composition (delegate + job_watch send + alias job_send_message)

agent/internal/tool/definitions.go   EDIT — add DefJobWatch(eventKinds []string)
agent/session_tools_jobs.go          EDIT — register job_watch handler (reg, s, deps)
agent/session_events.go              EDIT — Session.emit taps jm.onSessionEvent for event-frame watches
agent/provider/profile.go            EDIT — capabilityJobControl block adds DefJobWatch (root-only presence)
```

`job_watch` tool presence is **root-only** (spec §5.1: root-only-by-presence = `{delegate, job_watch}`). It is added under the same `capabilityJobControl` block as the other job tools but gated to the root profile the same way `delegate` is — see Task 8 for the exact wiring against whatever Phase 2/3 left in `profile.go`.

---

## Task 1: pure `output_match` matcher (no silent miss)

**Files:**
- Create: `agent/internal/jobstore/watch.go`
- Test: `agent/internal/jobstore/watch_test.go`

The matcher is the only piece of watches that lives in the pure `jobstore` package (spec §8). It must match a line even when the newline arrives in a later chunk than the matching text — the no-silent-miss guarantee for "bytes appended while the watch is active" (spec §5.9). It splits the byte stream on `\n`, emits matching completed lines, and holds the trailing partial line in `carry` until the next `Feed` completes it.

- [ ] **Step 1: Write the failing test** — `agent/internal/jobstore/watch_test.go`:

```go
package jobstore

import (
	"regexp"
	"testing"
)

func TestOutputMatcherMatchesCompletedLine(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`(?i)ready`))
	got := m.Feed([]byte("starting\nserver ready\n"))
	if len(got) != 1 || got[0] != "server ready" {
		t.Fatalf("matches = %#v, want [\"server ready\"]", got)
	}
}

func TestOutputMatcherNoSilentMissAcrossChunks(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`READY`))
	// The match text and its terminating newline arrive in SEPARATE Feed calls.
	if got := m.Feed([]byte("server REA")); len(got) != 0 {
		t.Fatalf("partial line must not match yet: %#v", got)
	}
	if got := m.Feed([]byte("DY now\n")); len(got) != 1 || got[0] != "server READY now" {
		t.Fatalf("split-across-chunks line must match once joined: %#v", got)
	}
}

func TestOutputMatcherDoesNotRematchOldBytes(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	_ = m.Feed([]byte("ready\n"))            // first line matches
	got := m.Feed([]byte("still going\n"))   // a later, non-matching line
	if len(got) != 0 {
		t.Errorf("already-consumed line must not re-match: %#v", got)
	}
}

func TestOutputMatcherFlushReturnsFinalPartial(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`done`))
	if got := m.Feed([]byte("all done")); len(got) != 0 { // no trailing newline
		t.Fatalf("unterminated line must not match on Feed: %#v", got)
	}
	if got := m.Flush(); len(got) != 1 || got[0] != "all done" {
		t.Errorf("Flush must match the buffered final line: %#v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./internal/jobstore/ -run TestOutputMatcher -v`. Expected: FAIL to compile (`undefined: NewOutputMatcher`).

- [ ] **Step 3: Write minimal implementation** — `agent/internal/jobstore/watch.go`:

```go
package jobstore

import (
	"bytes"
	"regexp"
)

// OutputMatcher applies an RE2 regex line-by-line to a stream of output chunks
// appended while a watch is active. Lines are split on '\n'; a trailing partial
// line is held in carry across Feed calls so a match split across chunk
// boundaries is never missed (the no-silent-miss guarantee, spec §5.9). It is
// pure: it imports only stdlib + regexp and knows nothing about sessions or
// event kinds (spec §8).
type OutputMatcher struct {
	re    *regexp.Regexp
	carry []byte
}

// NewOutputMatcher returns a matcher over re.
func NewOutputMatcher(re *regexp.Regexp) *OutputMatcher {
	return &OutputMatcher{re: re}
}

// Feed appends chunk to the pending buffer and returns each newly-completed line
// (terminated by '\n' within chunk or carried from a prior Feed) that matches re.
// A trailing partial line is retained for the next Feed.
func (m *OutputMatcher) Feed(chunk []byte) []string {
	m.carry = append(m.carry, chunk...)
	var matches []string
	for {
		i := bytes.IndexByte(m.carry, '\n')
		if i < 0 {
			break
		}
		line := string(m.carry[:i])
		m.carry = m.carry[i+1:]
		if m.re.MatchString(line) {
			matches = append(matches, line)
		}
	}
	// Compact carry so it does not retain the whole backing array indefinitely.
	if len(m.carry) == 0 {
		m.carry = nil
	} else {
		m.carry = append([]byte(nil), m.carry...)
	}
	return matches
}

// Flush returns a match for any buffered final partial line (no trailing
// newline). Call it once when the watched job goes terminal.
func (m *OutputMatcher) Flush() []string {
	if len(m.carry) == 0 {
		return nil
	}
	line := string(m.carry)
	m.carry = nil
	if m.re.MatchString(line) {
		return []string{line}
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./internal/jobstore/ -run TestOutputMatcher -v`. Expected: PASS (all four).

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/internal/jobstore/watch.go agent/internal/jobstore/watch_test.go
git commit -m "feat(jobstore): pure output_match matcher (no silent miss)"
```

---

## Task 2: `DefJobWatch(eventKinds)` tool definition

**Files:**
- Modify: `agent/internal/tool/definitions.go` (add `DefJobWatch`)
- Test: `agent/internal/tool/definitions_test.go` (add)

`DefJobWatch(eventKinds []string)` mirrors the build-time interpolation that `DefTaskList(efforts)` (`agent/internal/tool/definitions.go:444`) uses: it takes a `[]string` of the **model-facing** event-kind names available this session and interpolates them into the description's `{kinds}` slot (spec §5.11). Package `internal/tool` does **not** import `agent/events` (verified: nothing under `internal/tool` imports it), so this parameter is plain strings, not `events.EventKind` — the JobManager maps the names to internal kinds in Task 4.

Copy the parameter set and the description **verbatim** from spec §5.9.

- [ ] **Step 1: Write the failing test** — add to `agent/internal/tool/definitions_test.go`:

```go
func TestDefJobWatchParamsAndKinds(t *testing.T) {
	def := DefJobWatch([]string{"assistant.message", "job.notification"})
	if def.Name != "job_watch" {
		t.Fatalf("name = %q, want job_watch", def.Name)
	}
	props := def.Parameters["properties"].(map[string]any)
	for _, p := range []string{"target", "output_match", "progress_interval_ms", "events", "trigger", "send", "clear"} {
		if _, ok := props[p]; !ok {
			t.Errorf("DefJobWatch missing param %q", p)
		}
	}
	req := def.Parameters["required"].([]string)
	if len(req) != 1 || req[0] != "target" {
		t.Errorf("required = %#v, want [target]", req)
	}
	// The available event kinds are interpolated into the description.
	if !strings.Contains(def.Description, "assistant.message") || !strings.Contains(def.Description, "job.notification") {
		t.Errorf("description must enumerate the available event kinds:\n%s", def.Description)
	}
}
```

(Add `"strings"` to the test file imports if not already present.)

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./internal/tool/ -run TestDefJobWatch -v`. Expected: FAIL to compile (`undefined: DefJobWatch`).

- [ ] **Step 3: Implement** `DefJobWatch` in `agent/internal/tool/definitions.go`. Params from spec §5.9: `target` (string, required), `output_match` (string), `progress_interval_ms` (integer, min 1000 / max 3600000 — note in the schema description, the handler clamps), `events` (array of string), `trigger` (object `{event:string, every:integer}`), `send` (object `{to:string, message:string, include_frame:bool, include_excerpt:bool}`), `clear` (boolean). Interpolate the kinds into the `{kinds}` slot of the spec §5.9 description:

```go
// DefJobWatch defines the root-only job_watch tool. eventKinds are the
// model-facing session/job event-kind names available this session; they are
// interpolated into the description so the model can discover them (spec §5.11).
func DefJobWatch(eventKinds []string) llm.ToolDefinition {
	kinds := strings.Join(eventKinds, ", ")
	if kinds == "" {
		kinds = "none available this session"
	}
	desc := "Add an extra trigger on a running job or a visible session. Omit `send` to get a notification " +
		"yourself when the trigger fires; include `send` to deliver a bounded frame to another target, " +
		"such as an observer delegate. Triggers: `output_match`, a regex over output produced while " +
		"the watch is active; `progress_interval_ms`, periodic; or `events`/`trigger`, selected " +
		"session/job event frames (kinds available this session: " + kinds + ", or `*`). This is not how you " +
		"learn a job finished — terminal notifications are automatic. Pass `clear=true` to remove a watch."
	return llm.ToolDefinition{
		Name:        "job_watch",
		Description: desc,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"target":               map[string]any{"type": "string", "description": "job_id, or a visible session: caller | main | watched, or * for all visible."},
				"output_match":         map[string]any{"type": "string", "description": "RE2 regex over output appended while the watch is active. Case-sensitive unless (?i). Invalid regex errors at creation."},
				"progress_interval_ms": map[string]any{"type": "integer", "description": "Periodic trigger interval in ms (min 1000, max 3600000). Omit for none."},
				"events": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Event kinds to watch; [\"*\"] = all visible. Available: " + kinds + ".",
				},
				"trigger": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"description":          "Fire only on the Nth occurrence of a named event.",
					"properties": map[string]any{
						"event": map[string]any{"type": "string"},
						"every": map[string]any{"type": "integer"},
					},
				},
				"send": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"description":          "Deliver to another target instead of notifying the caller.",
					"properties": map[string]any{
						"to":              map[string]any{"type": "string", "description": "job_id or alias: caller | main | watched."},
						"message":         map[string]any{"type": "string"},
						"include_frame":   map[string]any{"type": "boolean"},
						"include_excerpt": map[string]any{"type": "boolean"},
					},
				},
				"clear": map[string]any{"type": "boolean", "description": "Remove the matching watch. The only unwatch operation."},
			},
			"required": []string{"target"},
		},
	}
}
```

Confirm `strings` is already imported in `definitions.go` (it is, used by other Defs); if not, add it.

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./internal/tool/ -run TestDefJobWatch -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/internal/tool/definitions.go agent/internal/tool/definitions_test.go
git commit -m "feat(tool): DefJobWatch tool definition with event-kind interpolation"
```

---

## Task 3: watch registry — configure / clear / key dedupe

**Files:**
- Create: `agent/job_watch.go`
- Modify: `agent/jobs.go` (add `watches map[watchKey]*watchConfig` to `jobManager`; init in `newJobManager`)
- Test: `agent/job_watch_test.go`

The registry is keyed `(visible_session_id, target, send.to)` (spec §5.9). At most one config per key; a duplicate is idempotent; a different config replaces it and returns `replaced_existing=true`. `clear=true` removes. Synchronous errors: `target_not_found` (unknown concrete job), `target_not_watchable` (not permitted), and "no condition supplied and not `clear`" (spec §5.9 / §5.10). This task builds the registry against the resolved key and validation only; the trigger evaluation (Tasks 4–6) is wired next.

- [ ] **Step 1: Write the failing test** — `agent/job_watch_test.go`:

```go
package agent

import (
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func TestConfigureWatchRequiresCondition(t *testing.T) {
	jm := newTestJM(t) // Phase 2 helper
	_, err := jm.configureWatch(watchArgs{Target: "caller"}) // no condition, not clear
	if err == nil {
		t.Fatal("a watch with no condition and clear=false must error")
	}
}

func TestConfigureWatchTargetNotFound(t *testing.T) {
	jm := newTestJM(t)
	_, err := jm.configureWatch(watchArgs{Target: "job_does_not_exist", OutputMatch: "ready"})
	if err == nil {
		t.Fatal("an unknown concrete job target must error (target_not_found)")
	}
}

func TestConfigureWatchIdempotentAndReplace(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"}) // Phase 2: running shell job
	first, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if first.ReplacedExisting {
		t.Error("first watch must not report replaced_existing")
	}
	// Same config on the same key → idempotent, still not "replaced".
	same, _ := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if same.ReplacedExisting {
		t.Error("identical re-config must be idempotent, not a replacement")
	}
	// Different config on the same key → replaced_existing=true.
	diff, _ := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "blocked"})
	if !diff.ReplacedExisting {
		t.Error("changed config on the same key must report replaced_existing")
	}
}

func TestClearWatchRemovesIt(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, _ = jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if jm.watchCount() != 0 {
		t.Errorf("clear must remove the watch; count = %d", jm.watchCount())
	}
}

var _ = jobstore.JobShell // keep the import if the file does not otherwise use it
```

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run 'TestConfigureWatch|TestClearWatch' -v`. Expected: FAIL to compile (`configureWatch`, `watchArgs`, `watchResult` undefined).

- [ ] **Step 3: Implement.**
  - In `agent/jobs.go`: add `watches map[watchKey]*watchConfig` to the `jobManager` struct and `jm.watches = map[watchKey]*watchConfig{}` in `newJobManager`.
  - In `agent/job_watch.go`: define `watchKey`, `watchConfig`, `watchSend` (from shared contracts), plus:
    - `watchArgs{ Target, OutputMatch string; ProgressIntervalMS int; Events []string; TriggerEvent string; TriggerEvery int; Send *watchSendArgs; Clear bool }` and `watchSendArgs{ To, Message string; IncludeFrame, IncludeExcerpt bool }`.
    - `watchResult{ Target string; Watching bool; OutputMatch string; Events []string; ProgressIntervalMS int; Send *watchSendArgs; ReplacedExisting bool }` — the spec §5.9 return shape.
    - `func (jm *jobManager) configureWatch(a watchArgs) (watchResult, error)`:
      1. **Validate.** If `a.Clear` is false and there is **no** condition (`output_match` empty, `progress_interval_ms <= 0`, `events` empty, no `trigger.event`): return an `invalid_request`-class error ("nothing to watch").
      2. **Resolve+authorize the target.** `caller`/`main`/`watched`/`*` are session aliases (always watchable for the root caller in v1). A concrete `job_id` must exist in `jm.store.Load()` or the running overlay → else `target_not_found`. (Authorization beyond existence — `target_not_watchable` — is the cross-session/permission case; for v1's single owning session, only the alias/`job_id` existence check is enforced here. Leave a TODO citing spec §5.9 for cross-session watch authorization, which Phase 5 nested-job visibility extends.)
      3. **Build the key.** `sendTo := ""; if a.Send != nil { sendTo = a.Send.To }`. `key := watchKey{VisibleSessionID: jm.sessionID, Target: a.Target, SendTo: sendTo}`. (Use whatever the Phase 2 `jobManager` stores the session id as — `jm.sessionID` or equivalent; verify the field name.)
      4. **Clear path.** If `a.Clear`: with a `send.to`, delete that one key; without, delete every key whose `VisibleSessionID == jm.sessionID && Target == a.Target` (spec §5.9: clears all watches for `(visible_session_id, target)`). Tear down any progress timer (`close(cfg.progressStop)`). Return `watchResult{Target: a.Target, Watching: false}`.
      5. **Compile.** If `output_match != ""`: `regexp.Compile` it; an invalid regex is a synchronous error (spec §5.9). Build `watchConfig` (compile the matcher via `jobstore.NewOutputMatcher`; resolve `events`/`trigger` into `EventKinds`/`wildcardEvents`/`TriggerEvent`/`TriggerEvery` — Task 4 supplies the name→kind map helper `resolveEventKinds`).
      6. **Insert with dedupe.** Under `jm.mu`: if a config already exists at `key`, compare it to the new one; equal → idempotent (`ReplacedExisting=false`), unequal → replace (tear down the old timer) and set `ReplacedExisting=true`. Else insert fresh.
      7. Start the progress timer if `ProgressEveryMS > 0` (Task 5 implements `startProgressTimer`; for this task a stubbed `progressStop` channel is enough — the timer goroutine is added in Task 5).
      8. Return the `watchResult` echoing the stored config.
    - `func (jm *jobManager) watchCount() int` — `len(jm.watches)` under the mutex (test helper).

  Keep equality comparison explicit (regex source string, progress interval, sorted event kinds, send fields) — do not rely on struct `==` because `watchConfig` holds pointers/maps.

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run 'TestConfigureWatch|TestClearWatch' -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/jobs.go agent/job_watch.go agent/job_watch_test.go
git commit -m "feat(agent): job_watch registry (configure/clear/key dedupe)"
```

---

## Task 4: event-frame gating — the `Session.emit` tap

**Files:**
- Modify: `agent/job_watch.go` (add `resolveEventKinds`, `onSessionEvent`)
- Modify: `agent/session_events.go` (`Session.emit` calls `s.jobs.onSessionEvent(kind, data)`)
- Test: `agent/job_watch_test.go` (extend)

The `events`/`trigger` axis (spec §8) is the JobManager's job because it names `agent/events` kinds. `Session.emit` (`agent/session_events.go:45`) is the single choke point through which every session event flows — the JobManager taps it. `resolveEventKinds` maps the **model-facing** names accepted by `job_watch` (and enumerated in `DefJobWatch`) to internal `events.EventKind` constants; `onSessionEvent` checks each registered watch whose `Target` is a session alias / `*`, gates by kind (and `trigger.event`/`trigger.every`), and fires.

The model-facing → internal name map is **defined here** — there is no existing one (verified: no `assistant.message`/`job.notification` mapping exists in the tree). Keep it small and aligned with the spec §5.9 return-shape examples (`assistant.message`, `job.notification`). Pick the available set from the `events.EventKind` constants in `agent/events/events.go`:

```go
// modelEventKinds maps the model-facing event-kind names that job_watch accepts
// (and DefJobWatch enumerates) to the internal events.EventKind taxonomy. This is
// the discoverable vocabulary of spec §5.9; it is intentionally a small, stable
// subset of the full event stream, not every internal kind.
var modelEventKinds = map[string]events.EventKind{
	"assistant.message": events.EventAssistantTextEnd,
	"assistant.tool":    events.EventToolCallEnd,
	"job.notification":  events.EventSubagentEnd, // job lifecycle; repointed to the job event in Phase 6
	"communicate":       events.EventCommunicate,
}
```

The model-facing names are the single source of truth for both the gating (here) and the tool description (Task 8). Declare them once as an exported slice and key the map off it:

```go
// WatchEventKindNames is the canonical, stable list of model-facing event-kind
// names job_watch accepts. DefJobWatch enumerates them in its description; the
// JobManager gates on them via modelEventKinds. Exported so the provider-side
// capabilityJobControl block (which cannot import agent/events) passes the same
// literal into DefJobWatch (Task 8).
var WatchEventKindNames = []string{"assistant.message", "assistant.tool", "communicate", "job.notification"}

func availableEventKindNames() []string { return append([]string(nil), WatchEventKindNames...) }
```

Keep `modelEventKinds`'s key set exactly equal to `WatchEventKindNames` (a `_test.go` assertion that every name resolves and that the counts match guards against drift).

- [ ] **Step 1: Write the failing test** — append to `agent/job_watch_test.go`:

```go
import "primeradiant.com/serf/agent/events" // add to the existing import block

func TestEventWatchFiresAndNotifiesCaller(t *testing.T) {
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) } // Phase 2 field

	if _, err := jm.configureWatch(watchArgs{Target: "caller", Events: []string{"assistant.message"}}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	// Simulate the session emitting an assistant-message-end event.
	jm.onSessionEvent(events.EventAssistantTextEnd, nil)

	if len(notified) != 1 {
		t.Fatalf("an assistant.message event must notify the caller once, got %d", len(notified))
	}
}

func TestEventWatchTriggerEveryNth(t *testing.T) {
	jm := newTestJM(t)
	var fires int
	jm.enqueue = func(jobNotification) { fires++ }

	_, err := jm.configureWatch(watchArgs{
		Target:       "caller",
		Events:       []string{"assistant.message"},
		TriggerEvent: "assistant.message",
		TriggerEvery: 3,
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	for i := 0; i < 7; i++ {
		jm.onSessionEvent(events.EventAssistantTextEnd, nil)
	}
	if fires != 2 { // fires on the 3rd and 6th
		t.Errorf("trigger.every=3 over 7 events should fire twice, got %d", fires)
	}
}

func TestEventWatchIgnoresUnwatchedKind(t *testing.T) {
	jm := newTestJM(t)
	var fires int
	jm.enqueue = func(jobNotification) { fires++ }
	_, _ = jm.configureWatch(watchArgs{Target: "caller", Events: []string{"assistant.message"}})
	jm.onSessionEvent(events.EventToolCallEnd, nil) // a different kind
	if fires != 0 {
		t.Errorf("an unwatched event kind must not fire; fires = %d", fires)
	}
}
```

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run TestEventWatch -v`. Expected: FAIL to compile (`onSessionEvent`, `modelEventKinds` undefined).

- [ ] **Step 3: Implement.**
  - In `agent/job_watch.go`: add `modelEventKinds`, `availableEventKindNames()`, `resolveEventKinds(names []string) (map[events.EventKind]bool, wildcard bool)` (`["*"]` → `wildcard=true`; unknown names are dropped silently, matching the spec's "free-form array" + `*` fallback). Use `resolveEventKinds` in `configureWatch` (Task 3 step 5) to populate `EventKinds`/`wildcardEvents`, and map `trigger.event` via `modelEventKinds` to `TriggerEvent`.
  - Add `func (jm *jobManager) onSessionEvent(kind events.EventKind, data events.EventData)`:
    1. Under `jm.mu`, iterate `jm.watches`. Consider only watches whose `Target` is a session alias (`caller`/`main`/`watched`) or `*` (concrete-`job_id` watches do not gate on session events in v1 — they gate on that job's `output_match`/progress).
    2. For each, decide if `kind` is in scope: `cfg.wildcardEvents || cfg.EventKinds[kind]`.
    3. If a `trigger.event` is set, only the matching kind counts; increment `cfg.eventCount` and fire only when `cfg.eventCount % cfg.TriggerEvery == 0`. With no `trigger`, fire on every in-scope event.
    4. Fire = build a bounded frame (Task 6) and **deliver**: `cfg.Send == nil` → `jm.enqueue(jobNotification{...})` (the caller-notification path — reuse the Phase 2 `jobNotification` queue); `cfg.Send != nil` → `jm.deliverWatchSend(...)` (Task 6). For Task 4, the send branch can be a stub that records the intent; Task 6 wires real delivery.
    Take care with lock ordering: `onSessionEvent` runs **inside** `Session.emit`, so it must not call back into anything that re-takes the session lock or re-enters `emit` (which would recurse). Build the notification payload and enqueue it (the enqueue path is lock-discrete per Phase 2's `enqueueJobNotification`), but defer any model-turn-driving to the existing notification machinery. Document this constraint in a comment.
  - In `agent/session_events.go`: at the end of `emit` (after `sendEvent`), add:

```go
	if s.jobs != nil {
		s.jobs.onSessionEvent(kind, data)
	}
```

  Place it so it cannot recurse: `onSessionEvent` must only enqueue (never emit). The `s.jobs` field was added in Phase 2 (Task 6).

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run TestEventWatch -v`. Expected: PASS (all three). Then `cd agent && go test ./ -run 'TestConfigureWatch|TestClearWatch|TestEventWatch' -v` for no regressions.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/job_watch.go agent/session_events.go agent/job_watch_test.go
git commit -m "feat(agent): event-frame watch gating via Session.emit tap"
```

---

## Task 5: `output_match` output tap + `progress_interval_ms` timer

**Files:**
- Modify: `agent/job_watch.go` (output-tap wiring; `startProgressTimer`)
- Modify: `agent/jobs.go` and/or `agent/job_shell.go` (feed appended output bytes to active matchers)
- Test: `agent/job_watch_test.go` (extend)

`output_match` fires on a **running** watched job's newly appended output (spec §5.9), using the Task 1 pure matcher. The bytes must reach the matcher as they are appended. Phase 2 writes job output through the per-job `*jobstore.OutputStore` (the `io.Writer` the `StreamingExecutor` streams into); the cleanest tap is a JobManager hook called right after each append for a job that has an active `output_match` watch. `progress_interval_ms` is a periodic timer per watch.

- [ ] **Step 1: Write the failing test** — append to `agent/job_watch_test.go`:

```go
func TestOutputMatchWatchFiresOnAppendedBytes(t *testing.T) {
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"}) // running shell job (Phase 2)
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "(?i)ready"}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	// Bytes appended AFTER the watch is active must be evaluated.
	jm.feedJobOutput(rec.JobID, []byte("booting\nserver READY\n"))

	if len(notified) != 1 {
		t.Fatalf("output_match must fire once on the matching appended line, got %d", len(notified))
	}
}

func TestProgressTimerFiresPeriodically(t *testing.T) {
	jm := newTestJM(t)
	fired := make(chan struct{}, 4)
	jm.enqueue = func(jobNotification) { fired <- struct{}{} }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, ProgressIntervalMS: 1000}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	select {
	case <-fired: // at least one progress tick within the window
	case <-time.After(3 * time.Second):
		t.Fatal("progress timer did not fire within 3s")
	}
	_, _ = jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true}) // stop the timer
}
```

(Add `"time"` to the test imports if missing.)

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run 'TestOutputMatchWatch|TestProgressTimer' -v`. Expected: FAIL to compile (`feedJobOutput`, the timer wiring undefined).

- [ ] **Step 3: Implement.**
  - `func (jm *jobManager) feedJobOutput(jobID string, chunk []byte)`: under `jm.mu`, for each watch whose `Target == jobID` and whose `cfg.matcher != nil`, call `cfg.matcher.Feed(chunk)`; for each returned matching line, fire (notify or send, as Task 4). This is the bytes→matcher bridge.
  - **Wire the tap.** Find the Phase 2 append site (`runningJob.output.Append`, written by the `StreamingExecutor` stream in `job_shell.go` / by `jm` helpers). Add a thin wrapper so every append to a job's output also calls `jm.feedJobOutput(jobID, chunk)` **when** that job has an active `output_match` watch. Prefer a per-job `io.Writer` that the JobManager installs: a `teeOutput{store *jobstore.OutputStore, onAppend func([]byte)}` whose `Write` appends to the store and then calls `onAppend(p)`. Install the tee as the `StreamingExecutor`'s `out` for jobs that gain an `output_match` watch (or unconditionally, with `onAppend` a no-op until a watch exists — simpler and avoids a mid-stream writer swap). Verify the exact Phase 2 wiring (`grep -n "StreamCommand\|\.output\b\|teeOutput" agent/job_shell.go agent/jobs.go`) and match it; do **not** introduce a second output path.
    - Important: a watch added **after** a job started should still see **future** appended bytes (the matcher is created at `configureWatch` time and only fed subsequent chunks — that satisfies "output appended while the watch is active"; it does not retroactively scan already-written bytes, which is correct per spec §5.9).
  - `func (jm *jobManager) startProgressTimer(key watchKey, cfg *watchConfig)`: spawn a goroutine with a `time.NewTicker(cfg.ProgressEveryMS ms)` that fires a progress notification/send on each tick until `cfg.progressStop` is closed. Call it from `configureWatch` when `ProgressEveryMS > 0`; close `progressStop` on clear/replace. Guard against firing for a job that has gone terminal (check the record; expire the watch — see Task 7).

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run 'TestOutputMatchWatch|TestProgressTimer' -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/job_watch.go agent/jobs.go agent/job_shell.go agent/job_watch_test.go
git commit -m "feat(agent): output_match output tap + progress_interval timer"
```

---

## Task 6: `send` delivery via Phase 3 `job_send_message` + bounded frames

**Files:**
- Modify: `agent/job_watch.go` (`deliverWatchSend`, `buildWatchFrame`)
- Test: `agent/job_watch_test.go` (extend)

When `send` is present, a fired watch delivers the configured message plus a bounded frame/excerpt to `send.to` via the Phase 3 `job_send_message` seam (spec §5.9, §5.5). Frames are bounded and filtered (spec §5.9). The message sent is the configuration **current at fire time** (read `cfg.Send` live).

> **Dependency:** this task delivers via the injected `jm.send(ctx, sendMessageArgs{...})` (wired to Phase 3's `s.sendDelegateMessage`, see the dependency note at the top). Verify the real names first and adapt.

- [ ] **Step 1: Write the failing test** — append to `agent/job_watch_test.go`:

```go
func TestWatchSendDeliversFrameToTarget(t *testing.T) {
	jm := newTestJM(t)
	// Capture deliveries by setting the injected send func (production wires it
	// to s.sendDelegateMessage; here we capture).
	var sent []sendMessageArgs
	jm.send = func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "(?i)ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "saw ready", IncludeFrame: true},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.feedJobOutput(rec.JobID, []byte("server READY\n"))

	if len(sent) != 1 {
		t.Fatalf("a send watch must deliver once, got %d", len(sent))
	}
	if sent[0].Target != "job_obs" {
		t.Errorf("delivery target = %q, want job_obs", sent[0].Target)
	}
	if !strings.Contains(sent[0].Message, "saw ready") {
		t.Errorf("delivery must carry the configured message + frame; got %q", sent[0].Message)
	}
}
```

(Phase 3's `sendMessageArgs` names the target field `Target` and the body `Message`. Add `"context"`/`"strings"` imports if missing.)

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run TestWatchSend -v`. Expected: FAIL to compile (`deliverWatchSend`, the `jm.send` field undefined).

- [ ] **Step 3: Implement.**
  - Add the `send func(context.Context, sendMessageArgs) sendMessageResult` field to `jobManager` (per the dependency note), and add `FromWatch bool` to Phase 3's `sendMessageArgs` + the configure-time-auth skip in `sendDelegateMessage`. Wire `jm.send = s.sendDelegateMessage` at the session-build site (where the JobManager is constructed).
  - `func (jm *jobManager) buildWatchFrame(cfg *watchConfig, jobID string, trigger string) string` — compose the delivered message: `cfg.Send.Message`, plus (when `IncludeFrame`) a bounded frame describing the trigger (`jobID`, the matched line / event kind / progress tick), plus (when `IncludeExcerpt`) a bounded tail of the job's output via the Phase 2 `readOutput`. Bound every piece (e.g. cap the excerpt at a few KB). Exclude observer telemetry to avoid loops (spec §9): never include a watched job's own watch-send output in a frame.
  - `func (jm *jobManager) deliverWatchSend(ctx context.Context, cfg *watchConfig, jobID, trigger string)` — build the frame and deliver: `res := jm.send(ctx, sendMessageArgs{Target: cfg.Send.To, Message: jm.buildWatchFrame(...), Background: true, FromWatch: true})`. A delivery error (`res.Err`) must be swallowed into diagnostics (spec §9: "sidecar failure produces diagnostics, never fails the watched session") — log/record, do not propagate. If `jm.send` is nil (e.g. persistence-off / no delegate runtime), skip silently.
  - Route the Task 4/Task 5 fire points through `deliverWatchSend` for `cfg.Send != nil` and through `jm.enqueue` for `cfg.Send == nil`.

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run TestWatchSend -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/job_watch.go agent/job_watch_test.go
git commit -m "feat(agent): job_watch send delivery via job_send_message + bounded frames"
```

---

## Task 7: watch expiry when the watched job goes terminal

**Files:**
- Modify: `agent/job_watch.go` (expire concrete-job watches on finalize; flush ordering)
- Modify: `agent/jobs.go` (`finalize` calls into watch teardown before the terminal notification)
- Test: `agent/job_watch_test.go` (extend)

Spec §5.9: a watch expires when the **concrete** watched job goes terminal; session-level watches persist until scope ends. Spec §5.9 ordering: "flush queued watch sends for a concrete job, then deliver its terminal notification." So `finalize` (Phase 2) must, for the finalizing job: (1) flush its `output_match` matcher (`Flush()`), delivering any final partial-line match; (2) tear down its progress timer; (3) remove its concrete-job watches; **then** (4) proceed to the terminal notification. Watches keyed on session aliases/`*` are untouched.

- [ ] **Step 1: Write the failing test** — append to `agent/job_watch_test.go`:

```go
func TestConcreteWatchExpiresOnTerminal(t *testing.T) {
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, _ = jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if jm.watchCount() != 1 {
		t.Fatalf("watch not registered")
	}
	code := 0
	jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code) // Phase 2 finalize
	if jm.watchCount() != 0 {
		t.Errorf("a concrete-job watch must expire when the job goes terminal; count = %d", jm.watchCount())
	}
}

func TestSessionWatchSurvivesAJobTerminal(t *testing.T) {
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, _ = jm.configureWatch(watchArgs{Target: "caller", Events: []string{"assistant.message"}})
	code := 0
	jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code)
	if jm.watchCount() != 1 {
		t.Errorf("a session-alias watch must survive a job going terminal; count = %d", jm.watchCount())
	}
}
```

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run 'TestConcreteWatchExpires|TestSessionWatchSurvives' -v`. Expected: FAIL (watches not expired on finalize).

- [ ] **Step 3: Implement.**
  - `func (jm *jobManager) expireJobWatches(jobID string)`: under `jm.mu`, for each watch whose `Target == jobID`: if it has an `output_match` matcher, `Flush()` it and deliver any final match (notify/send); close its `progressStop`; delete the key.
  - In `agent/jobs.go` `finalize`: call `jm.expireJobWatches(jobID)` **before** writing `EventJobNotificationPending` / calling `jm.enqueue` for the terminal notification, so the spec §5.9 "flush watch sends, then terminal notification" ordering holds. Mind the lock: `finalize` already holds (or takes) `jm.mu` in Phase 2 — fold `expireJobWatches`'s body into the same critical section rather than re-locking (extract a `…Locked` helper if Phase 2's `finalize` holds the mutex).

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run 'TestConcreteWatchExpires|TestSessionWatchSurvives' -v`. Expected: PASS. Then `cd agent && go test ./ -run 'TestConfigureWatch|TestClearWatch|TestEventWatch|TestOutputMatchWatch|TestProgressTimer|TestWatchSend' -v` to confirm no regressions.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/job_watch.go agent/jobs.go agent/job_watch_test.go
git commit -m "feat(agent): expire concrete-job watches on terminal (flush-then-notify ordering)"
```

---

## Task 8: register the `job_watch` tool (root-only)

**Files:**
- Modify: `agent/session_tools_jobs.go` (`registerJobTools` — add the `job_watch` handler)
- Modify: `agent/provider/profile.go` (`capabilityJobControl` block adds `DefJobWatch`; root-only presence)
- Test: `agent/session_tools_jobs_test.go` (add)

Register `job_watch` alongside the other job tools (Phase 2 created `registerJobTools(reg, s, deps)` and the `capabilityJobControl` block). The handler parses the args, builds `watchArgs`, and calls `s.jobs.configureWatch`. Presence is **root-only** (`{delegate, job_watch}`, spec §5.1): wire it the same way `delegate` is gated to the root profile in `profile.go` — do **not** add it to the subagent tool set.

**The two-def reality (resolve it explicitly).** There are two `job_watch` definition instances: the **advertised** def in `profile.toolDefs` (built in `provider`'s `toolDefinitionsForCapabilities` — this is the description and schema the model actually sees, name-mapped per provider via `ToolDefinitions()`), and the **registered** def in `registerJobTools` (package `agent`, which carries the `Exec` executor). The interpolated event-kind description must land in the **advertised** def, so `DefJobWatch(kinds)` is called in `provider` — but `provider` **cannot** import `agent/events` (verified: the import graph is `agent → provider`, never the reverse) and cannot call `agent.availableEventKindNames()`. Resolve this the way `DefTaskList(efforts)` already resolves the analogous problem: the canonical model-facing event-kind names are a **fixed, stable list**, so declare that list once as an exported slice in package `agent` (`var WatchEventKindNames = []string{"assistant.message", "assistant.tool", "communicate", "job.notification"}`, returned by `availableEventKindNames()` and used by `modelEventKinds`'s keys), and pass the **same literal slice** into `DefJobWatch` from `provider`'s `capabilityJobControl` block with a comment naming `agent.modelEventKinds` as the source of truth. The `agent`-side registered def reuses the identical call. Add a tiny test asserting the `provider` slice and `agent.WatchEventKindNames` are equal so they cannot drift. (Do not try to thread the names from `agent` into `provider` at runtime — the profile is built before the session exists, and the list is static, so a shared constant is correct and matches the existing `efforts` precedent which is resolved inside `provider` itself.)

- [ ] **Step 1: Write the failing test** — add to `agent/session_tools_jobs_test.go`:

```go
func TestJobWatchToolConfiguresWatch(t *testing.T) {
	s := newTestSessionWithLocalEnv(t) // reuse the Phase 2 helper
	// Start a background shell job to watch.
	out, err := s.callTool(t, "shell", map[string]any{"command": "sleep 30", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	jobID := extractJobID(t, out) // reuse the Phase 2 test helper

	res, err := s.callTool(t, "job_watch", map[string]any{
		"target":       jobID,
		"output_match": "(?i)ready",
	})
	if err != nil {
		t.Fatalf("job_watch: %v", err)
	}
	if !strings.Contains(res, `"watching":true`) {
		t.Errorf("expected watching:true in %q", res)
	}
	_, _ = s.callTool(t, "job_stop", map[string]any{"job_id": jobID}) // cleanup
}

func TestJobWatchNoConditionErrors(t *testing.T) {
	s := newTestSessionWithLocalEnv(t)
	_, err := s.callTool(t, "job_watch", map[string]any{"target": "caller"}) // no condition, no clear
	if err == nil {
		t.Error("job_watch with no condition and clear=false must error")
	}
}
```

(Use whatever tool-invocation/`job_id`-extraction helpers Phase 2 left in the job tests; if the names differ, match them.)

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run TestJobWatch -v`. Expected: FAIL (`job_watch` not registered).

- [ ] **Step 3: Implement.**
  - In `agent/session_tools_jobs.go` `registerJobTools`: register `job_watch`:

```go
_ = reg.Register(tool.RegisteredTool{
	Tool: llm.Tool{Definition: tool.DefJobWatch(availableEventKindNames())},
	Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
		_ = env
		a := watchArgs{Target: fmt.Sprint(args["target"])}
		if v, ok := args["output_match"]; ok && v != nil {
			a.OutputMatch = fmt.Sprint(v)
		}
		if v, ok := args["progress_interval_ms"].(float64); ok {
			a.ProgressIntervalMS = int(v)
		}
		if raw, ok := args["events"].([]any); ok {
			for _, it := range raw {
				if s, ok := it.(string); ok {
					a.Events = append(a.Events, s)
				}
			}
		}
		if tr, ok := args["trigger"].(map[string]any); ok {
			if v, ok := tr["event"]; ok && v != nil {
				a.TriggerEvent = fmt.Sprint(v)
			}
			if v, ok := tr["every"].(float64); ok {
				a.TriggerEvery = int(v)
			}
		}
		if sn, ok := args["send"].(map[string]any); ok {
			ws := &watchSendArgs{}
			if v, ok := sn["to"]; ok && v != nil {
				ws.To = fmt.Sprint(v)
			}
			if v, ok := sn["message"]; ok && v != nil {
				ws.Message = fmt.Sprint(v)
			}
			if v, ok := sn["include_frame"].(bool); ok {
				ws.IncludeFrame = v
			}
			if v, ok := sn["include_excerpt"].(bool); ok {
				ws.IncludeExcerpt = v
			}
			a.Send = ws
		}
		if v, ok := args["clear"].(bool); ok {
			a.Clear = v
		}
		return s.jobs.configureWatch(a)
	},
})
```

  Marshal `watchResult` to the spec §5.9 return shape (the registry serializes the returned value; confirm Phase 2's job handlers return a struct/marshalable map and match that convention). Clamp `progress_interval_ms` to [1000, 3600000] inside `configureWatch` (negative → `invalid_request`).
  - In `agent/provider/profile.go`: inside the `capabilityJobControl` block (added by Phase 2/3), `add(tool.DefJobWatch(<names>))` **only for the root profile**, matching exactly how `delegate` is restricted to root presence. Verify the mechanism Phase 3 used for `delegate`'s root-only presence (a separate root-only capability, or a depth check) and reuse it; `job_watch` and `delegate` share the same root-only-presence rule (spec §5.1). Do not grant `job_watch` to subagents.

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run TestJobWatch -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/session_tools_jobs.go agent/provider/profile.go agent/session_tools_jobs_test.go
git commit -m "feat(agent): register job_watch tool (root-only presence)"
```

---

## Task 9: observer-sidecar composition (end-to-end)

**Files:**
- Test: `agent/job_watch_observer_test.go`

Observers are **not** a new tool (spec §9): `delegate(...)` starts the sidecar (`job_obs`); `job_watch(target=..., events=[...], send={to:"job_obs", include_frame:true})` feeds it frames; the sidecar (a subagent) advises with `job_send_message(target="caller"|"main"|"watched", ...)` — the alias targets Phase 3 made subagent-available. This task proves the composition: a frame reaches the sidecar **as a message**, and the sidecar's alias-target `job_send_message` reaches the watched session. No new production code — if a test gap forces one, stop and reconsider.

> **Dependency:** this is the one task that genuinely needs Phase 3's `delegate` + `job_send_message`. Build it last; if Phase 3 is not yet merged when you reach it, write the test against the Phase 3 seams and mark the task blocked rather than stubbing a fake delegate runtime (no mocks in e2e — global rule).

- [ ] **Step 1: Write the test** — `agent/job_watch_observer_test.go`. Drive a real `*Session` (the Phase 2/3 `newTestSessionWithLocalEnv` helper). Assert the composition, not the internals:

```go
package agent

import (
	"strings"
	"testing"
)

// The observer sidecar is pure composition: delegate starts it, job_watch.send
// feeds it a frame as a message, and its alias-target job_send_message reaches
// the watched session. (Spec §9.)
func TestObserverSidecarReceivesFrameAndAdvisesBack(t *testing.T) {
	s := newTestSessionWithLocalEnv(t)

	// 1. Start a watched background shell job.
	watched, err := s.callTool(t, "shell", map[string]any{"command": "sleep 30", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	watchedID := extractJobID(t, watched)

	// 2. Configure a send-watch that delivers a frame to the alias "job_obs".
	//    (In the full flow, delegate(job_obs) starts the sidecar first; here we
	//     verify the SEND path reaches the JobManager delivery seam with a frame.)
	if _, err := s.callTool(t, "job_watch", map[string]any{
		"target":       watchedID,
		"output_match": "(?i)ready",
		"send":         map[string]any{"to": "job_obs", "include_frame": true, "message": "observe"},
	}); err != nil {
		t.Fatalf("job_watch: %v", err)
	}

	// 3. Feed matching output; assert a frame was delivered toward job_obs.
	captured := captureWatchSends(t, s.jobs) // sets s.jobs.send to a capture, returns a getter
	s.jobs.feedJobOutput(watchedID, []byte("server READY\n"))
	sends := captured()
	if len(sends) != 1 || sends[0].Target != "job_obs" {
		t.Fatalf("expected one frame delivered to job_obs, got %#v", sends)
	}
	if !strings.Contains(sends[0].Message, "READY") && !strings.Contains(sends[0].Message, "observe") {
		t.Errorf("frame must carry the trigger context / configured message: %q", sends[0].Message)
	}
	_, _ = s.callTool(t, "job_stop", map[string]any{"job_id": watchedID})
}
```

  `captureWatchSends` is a small test helper that sets `s.jobs.send` (the injected delivery func) to a capture and returns a closure exposing the captured `[]sendMessageArgs`. If Phase 3 is merged and you want the **fuller** flow (a real sidecar reading the frame and calling alias-target `job_send_message` back), add a second test that: spawns a `delegate` sidecar, configures the watch to its real `job_id`, drives output, and asserts (via the watched session's transcript/notification) that the sidecar's alias `job_send_message(target="watched")` landed. Keep it real (no mocked child) per the global rule; if that is too heavy for a unit test, cover it in the live e2e scenario (Task 10) instead and keep this unit test at the delivery-seam level.

- [ ] **Step 2: Run the test** — `cd agent && go test ./ -run TestObserverSidecar -v`. Expected: PASS (it exercises only existing seams).

- [ ] **Step 3: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/job_watch_observer_test.go
git commit -m "test(agent): observer-sidecar composition (frame delivery + alias advise)"
```

---

## Task 10: full-suite green + live e2e scenario

**Files:** none (verification), plus any test/lint fixups.

- [ ] **Step 1: Run the full module test + lint**

Run: `cd /Users/jesse/prime-radiant/toil-suite/serf && make test && make lint`
Expected: all modules PASS; lint clean (golangci ×4 + `serf-namingcheck`/`serf-internalcheck`/`docscheck`). Fix any fallout. The new `DefJobWatch` adds a tool to the root profile, so the profile/parity/snapshot tests (`agent/provider/profile_test.go`, any `ToolDefinitions()` golden) may need the `job_watch` entry added — update them to the new expected tool set, do not delete assertions.

- [ ] **Step 2: Live e2e scenario** (spec §14 "an observer sidecar commenting back through `job_send_message`"). Per `reference_serf_live_run`: build a standalone binary; do **not** touch a running serve.

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
go build -o /tmp/serf ./cmd/serf
. "$PWD/.env"
# In a scratch dir, with --model oai-work/<model>:
#  1. Start a background shell job that will print a "ready" line.
#  2. job_watch(target=<job>, output_match="(?i)ready") with send omitted
#     → confirm a <job-notification>-style watch notification reaches the model.
#  3. Start a delegate sidecar (job_obs); job_watch(target=<job>, events=[...],
#     send={to:"job_obs", include_frame:true}); confirm the sidecar receives the
#     frame as a message and its job_send_message(target="watched") lands.
```

Use the `e2e-scenario-testing` skill to write falsifiable scenario cards. Expected: caller-notification watch fires on the match; the observer sidecar receives a frame and advises back through an alias-target `job_send_message`.

- [ ] **Step 3: Commit any test/lint fixups**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git status   # review first
git add -A
git commit -m "test(job-control): phase 4 watches suite + profile fixups green"
```

---

## Phase 4 self-review (run against the spec)

- **Spec coverage:** pure `output_match` matcher with no-silent-miss over a chunked stream (Task 1) ↔ §5.9 / §8 seam split; `DefJobWatch` params + event-kind interpolation (Task 2) ↔ §5.9 / §5.11; registry keyed `(visible_session_id, target, send.to)` with idempotent-duplicate / `replaced_existing` / `clear` / `target_not_found` / no-condition error (Task 3) ↔ §5.9 / §5.10; `events`/`trigger` event-frame gating via the `Session.emit` tap + the Nth-event counter (Task 4) ↔ §8; `output_match` output tap + `progress_interval_ms` timer (Task 5) ↔ §5.9; `send` delivery via the Phase 3 `job_send_message` seam + bounded frames with observer-telemetry exclusion (Task 6) ↔ §5.9 / §9; concrete-job watch expiry with flush-then-terminal ordering (Task 7) ↔ §5.9; root-only tool registration (Task 8) ↔ §5.1; observer-sidecar composition end-to-end (Task 9) ↔ §9; full green + live e2e (Task 10) ↔ §14.
- **Seam discipline (§8, mandatory):** the only `jobstore` addition is the pure `OutputMatcher` (stdlib + `regexp`, no `agent/events` import — verified `internal/tool` and `provider`/`jobstore` do not import `events`). All event-kind naming, the registry, gating, timers, and delivery live in package `agent`, where `events.EventKind` is nameable. The model-facing event-name → `events.EventKind` map is **new** and defined once in `job_watch.go` (no prior mapping exists in the tree).
- **Phase 2/3 reuse (no parallel paths):** the caller-notification path reuses the Phase 2 `jobNotification` queue (`jm.enqueue`); the `send` path reuses the Phase 3 delivery via the injected `jm.send` (wired to `s.sendDelegateMessage`); the output tap reuses the Phase 2 `OutputStore` append path (a tee `io.Writer`, not a second store); expiry folds into the Phase 2 `finalize`. The injectable `jm.send` mirrors Phase 2's injectable `now`.
- **Dependency honesty:** Phase 3 is unwritten at authoring time — the `job_send_message` Go seam is documented as an assumption at the top with a verify-and-adapt instruction, and Tasks 6/9 (the only Phase-3 consumers) are last and explicitly call it out. Tasks 1–5, 7, 8 have no Phase 3 dependency.
- **Lock safety:** `onSessionEvent` runs inside `Session.emit`, so it must only enqueue and never re-emit/re-enter the session lock (documented in Task 4). `expireJobWatches` folds into `finalize`'s existing critical section (Task 7).
- **Verify-against-code reminders embedded:** every task that touches a Phase 2/3 symbol (`jm.enqueue`, `jm.sessionID`, `finalize`, `s.jobs`, the `OutputStore` append site, `delegate`'s root-only gating, `newTestSessionWithLocalEnv`/`extractJobID`/`callTool`) instructs the implementer to grep-confirm the real name before use, because Phase 2/3 fix those names.
- **Placeholder scan:** none — every step has complete Go and an exact run command; the two genuinely Phase-3-dependent call sites are flagged with the documented seam and an adapt instruction, not invented.
