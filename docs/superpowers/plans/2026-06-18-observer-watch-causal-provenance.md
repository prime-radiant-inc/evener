# Observer Watch Causal Provenance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement causal provenance for watch deliveries so observer sidecars get content-bearing frames and cannot retrigger the same watch through injected steering or notification acknowledgements.

**Architecture:** Add a tiny shared `agent/provenance` package, attach provenance to the `events.SessionEvent` envelope, and carry that provenance explicitly through session active-turn state, steering queues, job records, watch-send state, delegate resumes, and job notifications. Replace the existing global `FromWatch` watch guard with same-watch `(watch_id, watch_generation)` suppression before any delivery accounting.

**Tech Stack:** Go, Serf session event stream, Serf jobstore JSONL, existing Go unit tests, existing markdown scenario tests, live Kimi e2e scenario verification.

---

## Scope Check

This is one coupled plan because provenance must cross the same async boundaries that caused the live failure: event emission, watch delivery, observer delegate messaging, caller steering, job notifications, durable watch-send restore, and job lifecycle completion. Splitting those into separate implementation plans would leave a false sense of safety where one path suppresses loops while another path still reopens them.

This plan does not add a public `observer` primitive, a new model-facing injection tool, or a user-configurable loop escape knob.

## File Structure

- Create `agent/provenance/provenance.go`: load-bearing watch-key set, diagnostic chain, deterministic union, cloning, membership checks, and watch-entry appending.
- Test `agent/provenance/provenance_test.go`: set-union semantics, diagnostic truncation, membership by watch id and watch generation, nil/empty behavior.
- Modify `agent/events/events.go`: add `Provenance *provenance.Causal` to `SessionEvent`.
- Test `agent/events/events_test.go`: JSON event envelope includes provenance without adding provenance to payload structs.
- Modify `agent/session.go`: add `activeProvenance provenance.Causal` to `Session`.
- Create `agent/session_provenance.go`: helpers to replace, union, snapshot, and drain steering into active provenance.
- Modify `agent/session_events.go`: stamp active provenance onto emitted events and pass the full event envelope to watches.
- Modify `agent/session_queue.go`: add provenance to `steeringMessage` and `queuedInput`; add `trySteerWithProvenance`.
- Modify `agent/session_lifecycle.go`: reset active provenance at new top-level inputs, adopt notification provenance, and drain steering through provenance-aware helpers.
- Modify `agent/session_tool_round.go`: union mid-turn steering provenance before downstream model reactions.
- Modify `agent/session_tool_registry.go` and `agent/session_tools_communicate.go`: make `communicate` inbox drains union provenance and preserve provenance for deferred image-bearing steering.
- Test `agent/session_provenance_test.go`: reset, union, mid-turn drain, communicate inbox drain, and deferred steering preservation.
- Modify `agent/internal/jobstore/record.go`: add provenance to `JobRecord`, `DelegateRestoreDescriptor`, and `WatchSendState`.
- Modify `agent/internal/jobstore/event.go`: add provenance to durable `Event`.
- Modify `agent/internal/jobstore/fold.go`: fold job and notification provenance.
- Test `agent/internal/jobstore/fold_test.go`, `agent/internal/jobstore/watch_test.go`, and `agent/internal/jobstore/event_test.go`: durable provenance survives fold, watch-send restore, and JSON round-trip.
- Modify `agent/jobs.go`: add current-provenance getter, store provenance on started jobs, emit detached lifecycle events with stored provenance, and enqueue notifications with provenance.
- Modify `agent/job_notify.go`: include notification provenance in in-memory delivery records.
- Modify `agent/job_delegate.go`: inherit provenance through `delegate_send(to="caller")`, observer delegate starts/resumes, running delegate steering, and delegate restore descriptors.
- Modify `agent/subagents.go` and `agent/session_config.go`: change parent steering callback signatures to carry provenance without persisting the callback fields.
- Modify `agent/job_watch.go`: pass full session events to watches, suppress same-watch echoes, create provenance for watch deliveries, persist provenance on watch-send state, and render content-bearing `communicate` frames.
- Test `agent/job_watch_test.go`, `agent/job_watch_observer_test.go`, `agent/job_delegate_test.go`, and `agent/session_tools_jobs_test.go`: same-watch suppression, generation semantics, watch-send persistence, delegate caller route propagation, notification acknowledgement propagation, and frame content.
- Modify `test/scenarios/job-watch-actually-monty-python-injection.md`: turn the current-failure scenario into the expected passing contract.
- Modify `test/scenarios/job-watch-observer-snide-thread.md`: assert observer commentary remains in the observer thread and does not inject into the caller.
- Modify `test/scenarios/INDEX.md`: update the scenario descriptions.

---

### Task 1: Causal Provenance Primitives

**Files:**
- Create: `agent/provenance/provenance.go`
- Test: `agent/provenance/provenance_test.go`

- [ ] **Step 1: Write failing provenance tests**

Create `agent/provenance/provenance_test.go`:

```go
package provenance

import "testing"

func TestUnionDedupesWatchKeysAndKeepsStableOrder(t *testing.T) {
	a := &Causal{WatchKeys: []WatchKey{
		{WatchID: "watch_A", WatchGeneration: "wg_1"},
		{WatchID: "watch_B", WatchGeneration: "wg_1"},
	}}
	b := &Causal{WatchKeys: []WatchKey{
		{WatchID: "watch_B", WatchGeneration: "wg_1"},
		{WatchID: "watch_A", WatchGeneration: "wg_2"},
	}}

	got := Union(a, b)
	want := []WatchKey{
		{WatchID: "watch_A", WatchGeneration: "wg_1"},
		{WatchID: "watch_B", WatchGeneration: "wg_1"},
		{WatchID: "watch_A", WatchGeneration: "wg_2"},
	}
	if len(got.WatchKeys) != len(want) {
		t.Fatalf("watch key count = %d, want %d: %+v", len(got.WatchKeys), len(want), got.WatchKeys)
	}
	for i := range want {
		if got.WatchKeys[i] != want[i] {
			t.Fatalf("watch key %d = %+v, want %+v", i, got.WatchKeys[i], want[i])
		}
	}
}

func TestContainsWatchRequiresGenerationMatch(t *testing.T) {
	p := &Causal{WatchKeys: []WatchKey{{WatchID: "watch_A", WatchGeneration: "wg_1"}}}
	if !ContainsWatch(p, "watch_A", "wg_1") {
		t.Fatal("same generation should match")
	}
	if ContainsWatch(p, "watch_A", "wg_2") {
		t.Fatal("different generation must not match")
	}
	if ContainsWatch(nil, "watch_A", "wg_1") {
		t.Fatal("nil provenance must not match")
	}
}

func TestWithWatchAddsLoadBearingKeyAndDiagnosticEntry(t *testing.T) {
	root := &Causal{WatchKeys: []WatchKey{{WatchID: "watch_root", WatchGeneration: "wg_root"}}}
	got := WithWatch(root, "watch_A", "wg_1", "wd_1", "session_1", "job_1")

	if !ContainsWatch(got, "watch_root", "wg_root") || !ContainsWatch(got, "watch_A", "wg_1") {
		t.Fatalf("provenance keys = %+v, want root and added watch", got.WatchKeys)
	}
	if len(got.Chain) != 1 {
		t.Fatalf("chain length = %d, want 1", len(got.Chain))
	}
	entry := got.Chain[0]
	if entry.Kind != "watch" || entry.WatchID != "watch_A" || entry.WatchGeneration != "wg_1" ||
		entry.DeliveryID != "wd_1" || entry.SessionID != "session_1" || entry.JobID != "job_1" {
		t.Fatalf("entry = %+v, want watch diagnostic entry", entry)
	}
}

func TestDiagnosticChainTruncatesWithoutDroppingWatchKeys(t *testing.T) {
	p := &Causal{}
	for i := 0; i < maxDiagnosticChain+5; i++ {
		p = WithWatch(p, "watch_"+string(rune('A'+i)), "wg_1", "wd", "session", "job")
	}
	if len(p.WatchKeys) != maxDiagnosticChain+5 {
		t.Fatalf("watch keys = %d, want all keys retained", len(p.WatchKeys))
	}
	if len(p.Chain) > maxDiagnosticChain {
		t.Fatalf("chain length = %d, want at most %d", len(p.Chain), maxDiagnosticChain)
	}
	if !p.ChainTruncated {
		t.Fatal("chain_truncated should be true")
	}
}

func TestNilIfEmpty(t *testing.T) {
	if NilIfEmpty(nil) != nil {
		t.Fatal("nil stays nil")
	}
	if NilIfEmpty(&Causal{}) != nil {
		t.Fatal("empty provenance should serialize as nil")
	}
	if NilIfEmpty(&Causal{WatchKeys: []WatchKey{{WatchID: "watch_A", WatchGeneration: "wg_1"}}}) == nil {
		t.Fatal("non-empty provenance should survive")
	}
}
```

- [ ] **Step 2: Run the provenance tests to verify they fail**

Run:

```bash
go test ./agent/provenance -count=1
```

Expected: FAIL because package `agent/provenance` does not exist.

- [ ] **Step 3: Add the provenance package**

Create `agent/provenance/provenance.go`:

```go
package provenance

const maxDiagnosticChain = 16

type WatchKey struct {
	WatchID         string `json:"watch_id"`
	WatchGeneration string `json:"watch_generation"`
}

type Causal struct {
	WatchKeys      []WatchKey `json:"watch_keys,omitempty"`
	Chain          []Entry    `json:"chain,omitempty"`
	ChainTruncated bool       `json:"chain_truncated,omitempty"`
}

type Entry struct {
	Kind            string `json:"kind"`
	WatchID         string `json:"watch_id,omitempty"`
	WatchGeneration string `json:"watch_generation,omitempty"`
	DeliveryID      string `json:"delivery_id,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	JobID           string `json:"job_id,omitempty"`
}

func Clone(p *Causal) *Causal {
	if p == nil {
		return nil
	}
	out := &Causal{ChainTruncated: p.ChainTruncated}
	out.WatchKeys = append(out.WatchKeys, p.WatchKeys...)
	out.Chain = append(out.Chain, p.Chain...)
	return NilIfEmpty(out)
}

func NilIfEmpty(p *Causal) *Causal {
	if p == nil {
		return nil
	}
	if len(p.WatchKeys) == 0 && len(p.Chain) == 0 && !p.ChainTruncated {
		return nil
	}
	return p
}

func ContainsWatch(p *Causal, watchID, watchGeneration string) bool {
	if p == nil || watchID == "" || watchGeneration == "" {
		return false
	}
	for _, key := range p.WatchKeys {
		if key.WatchID == watchID && key.WatchGeneration == watchGeneration {
			return true
		}
	}
	return false
}

func Union(parts ...*Causal) *Causal {
	var out Causal
	seen := make(map[WatchKey]bool)
	for _, part := range parts {
		if part == nil {
			continue
		}
		for _, key := range part.WatchKeys {
			if key.WatchID == "" || key.WatchGeneration == "" || seen[key] {
				continue
			}
			seen[key] = true
			out.WatchKeys = append(out.WatchKeys, key)
		}
		out.Chain = append(out.Chain, part.Chain...)
		out.ChainTruncated = out.ChainTruncated || part.ChainTruncated
	}
	truncateChain(&out)
	return NilIfEmpty(&out)
}

func WithWatch(base *Causal, watchID, watchGeneration, deliveryID, sessionID, jobID string) *Causal {
	added := &Causal{
		WatchKeys: []WatchKey{{WatchID: watchID, WatchGeneration: watchGeneration}},
		Chain: []Entry{{
			Kind:            "watch",
			WatchID:         watchID,
			WatchGeneration: watchGeneration,
			DeliveryID:      deliveryID,
			SessionID:       sessionID,
			JobID:           jobID,
		}},
	}
	return Union(base, added)
}

func LatestDeliveryID(p *Causal) string {
	if p == nil {
		return ""
	}
	for i := len(p.Chain) - 1; i >= 0; i-- {
		if p.Chain[i].DeliveryID != "" {
			return p.Chain[i].DeliveryID
		}
	}
	return ""
}

func truncateChain(p *Causal) {
	if p == nil || len(p.Chain) <= maxDiagnosticChain {
		return
	}
	keepHead := maxDiagnosticChain / 2
	keepTail := maxDiagnosticChain - keepHead
	next := make([]Entry, 0, maxDiagnosticChain)
	next = append(next, p.Chain[:keepHead]...)
	next = append(next, p.Chain[len(p.Chain)-keepTail:]...)
	p.Chain = next
	p.ChainTruncated = true
}
```

- [ ] **Step 4: Run provenance tests to verify they pass**

Run:

```bash
go test ./agent/provenance -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit provenance primitives**

Run:

```bash
git status --short
git add agent/provenance/provenance.go agent/provenance/provenance_test.go
git commit -m "feat(observer): add causal provenance primitives" -m "Add a small shared provenance package for watch-origin loop suppression. The load-bearing structure is a deduped watch key set keyed by watch_id and watch_generation; the diagnostic chain is bounded separately so truncation cannot reopen loops."
```

Expected: commit succeeds.

---

### Task 2: Event Envelope Provenance

**Files:**
- Modify: `agent/events/events.go`
- Test: `agent/events/events_test.go`

- [ ] **Step 1: Write failing event envelope test**

Create `agent/events/events_test.go`:

```go
package events_test

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/provenance"
)

func TestSessionEventCarriesCausalProvenanceOnEnvelope(t *testing.T) {
	ev := events.New(events.CommunicateData{AwaitReply: false, Message: "actually alpha marker"})
	ev.SessionID = "session_1"
	ev.Provenance = provenance.WithWatch(nil, "watch_A", "wg_1", "wd_1", "session_1", "caller")

	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	s := string(b)
	for _, want := range []string{`"provenance"`, `"watch_id":"watch_A"`, `"watch_generation":"wg_1"`, `"delivery_id":"wd_1"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("event JSON missing %s: %s", want, s)
		}
	}
	if strings.Contains(s, `"data":{"provenance"`) {
		t.Fatalf("provenance must live on event envelope, not payload: %s", s)
	}
}
```

- [ ] **Step 2: Run event tests to verify they fail**

Run:

```bash
go test ./agent/events -run TestSessionEventCarriesCausalProvenanceOnEnvelope -count=1
```

Expected: FAIL because `SessionEvent.Provenance` does not exist.

- [ ] **Step 3: Add provenance to the session event envelope**

In `agent/events/events.go`, add the import and field:

```go
import (
	"time"

	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/llm"
)
```

Update `SessionEvent`:

```go
type SessionEvent struct {
	Kind       EventKind          `json:"kind"`
	Timestamp  time.Time          `json:"timestamp"`
	SessionID  string             `json:"session_id"`
	Data       EventData          `json:"data,omitempty"`
	Provenance *provenance.Causal `json:"provenance,omitempty"`
}
```

- [ ] **Step 4: Run event tests to verify they pass**

Run:

```bash
go test ./agent/events -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit event envelope provenance**

Run:

```bash
git status --short
git add agent/events/events.go agent/events/events_test.go
git commit -m "feat(observer): carry provenance on session events" -m "Add causal provenance to the SessionEvent envelope. Provenance stays off individual payload structs so all event kinds share one propagation path and watches can make suppression decisions from the emitted event."
```

Expected: commit succeeds.

---

### Task 3: Session Active Provenance and Steering Propagation

**Files:**
- Modify: `agent/session.go`
- Create: `agent/session_provenance.go`
- Modify: `agent/session_events.go`
- Modify: `agent/session_queue.go`
- Modify: `agent/session_lifecycle.go`
- Modify: `agent/session_tool_round.go`
- Modify: `agent/session_tool_registry.go`
- Modify: `agent/session_tools_communicate.go`
- Test: `agent/session_provenance_test.go`

- [ ] **Step 1: Write failing session provenance tests**

Create `agent/session_provenance_test.go`:

```go
package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/provenance"
)

func testProvenance(watchID, generation string) *provenance.Causal {
	return provenance.WithWatch(nil, watchID, generation, "wd_"+watchID, "session_parent", "caller")
}

func TestEmitAttachesActiveProvenance(t *testing.T) {
	s := &Session{id: "session_1", events: make(chan events.SessionEvent, 1)}
	s.replaceActiveProvenance(testProvenance("watch_A", "wg_1"))

	s.emit(events.EventCommunicate, events.CommunicateData{Message: "ack", AwaitReply: false})

	ev := <-s.events
	if !provenance.ContainsWatch(ev.Provenance, "watch_A", "wg_1") {
		t.Fatalf("event provenance = %+v, want watch_A/wg_1", ev.Provenance)
	}
}

func TestActiveProvenanceResetsForExternalInput(t *testing.T) {
	s := &Session{}
	s.replaceActiveProvenance(testProvenance("watch_A", "wg_1"))
	s.replaceActiveProvenance(nil)

	if provenance.ContainsWatch(s.activeCausalProvenance(), "watch_A", "wg_1") {
		t.Fatal("external top-level input must replace active provenance with empty")
	}
}

func TestDrainSteeringForTurnUnionsMessageProvenance(t *testing.T) {
	s := &Session{}
	s.steeringQueue = []steeringMessage{
		{Text: "from A", Provenance: testProvenance("watch_A", "wg_1")},
		{Text: "from B", Provenance: testProvenance("watch_B", "wg_1")},
	}

	got := s.drainSteeringForTurn()
	if len(got) != 2 {
		t.Fatalf("drained = %d, want 2", len(got))
	}
	active := s.activeCausalProvenance()
	if !provenance.ContainsWatch(active, "watch_A", "wg_1") || !provenance.ContainsWatch(active, "watch_B", "wg_1") {
		t.Fatalf("active provenance = %+v, want union of A and B", active)
	}
}

func TestPrependSteeringPreservesProvenanceForDeferredImages(t *testing.T) {
	s := &Session{}
	p := testProvenance("watch_A", "wg_1")
	s.prependSteering([]steeringMessage{{Text: "image reminder", Provenance: p}})

	got := s.drainSteeringForTurn()
	if len(got) != 1 {
		t.Fatalf("drained = %d, want 1", len(got))
	}
	if !provenance.ContainsWatch(got[0].Provenance, "watch_A", "wg_1") {
		t.Fatalf("deferred provenance = %+v, want watch_A/wg_1", got[0].Provenance)
	}
	if !provenance.ContainsWatch(s.activeCausalProvenance(), "watch_A", "wg_1") {
		t.Fatalf("active provenance = %+v, want watch_A/wg_1", s.activeCausalProvenance())
	}
}

func TestCommunicateInboxDrainUnionsProvenance(t *testing.T) {
	s := &Session{id: "session_1", events: make(chan events.SessionEvent, 4)}
	deps := newToolDeps(s)
	s.steeringQueue = []steeringMessage{{Text: "observer steering", Provenance: testProvenance("watch_A", "wg_1")}}

	drained := deps.drainSteering()
	if len(drained) != 1 || !strings.Contains(drained[0].Text, "observer steering") {
		t.Fatalf("drained = %+v, want observer steering", drained)
	}
	if !provenance.ContainsWatch(s.activeCausalProvenance(), "watch_A", "wg_1") {
		t.Fatalf("communicate inbox drain did not union provenance: %+v", s.activeCausalProvenance())
	}
}

func TestDrainAsSteerCreatesExternalSteering(t *testing.T) {
	s := &Session{state: SessionProcessing}
	if err := s.DrainAsSteerWithInput(context.Background(), "human queued text", nil); err != nil {
		t.Fatalf("DrainAsSteerWithInput: %v", err)
	}
	got := s.drainSteeringForTurn()
	if len(got) != 1 {
		t.Fatalf("drained = %d, want 1", len(got))
	}
	if got[0].Provenance != nil {
		t.Fatalf("human queue steering provenance = %+v, want nil", got[0].Provenance)
	}
}
```

- [ ] **Step 2: Run session provenance tests to verify they fail**

Run:

```bash
go test ./agent -run 'Test(EmitAttachesActiveProvenance|ActiveProvenanceResetsForExternalInput|DrainSteeringForTurnUnionsMessageProvenance|PrependSteeringPreservesProvenanceForDeferredImages|CommunicateInboxDrainUnionsProvenance|DrainAsSteerCreatesExternalSteering)' -count=1
```

Expected: FAIL because active provenance helpers and steering provenance fields do not exist.

- [ ] **Step 3: Add active provenance state and helpers**

In `agent/session.go`, add a field next to the other `s.mu` protected session state:

```go
activeProvenance provenance.Causal
```

Add the import:

```go
"primeradiant.com/serf/agent/provenance"
```

Create `agent/session_provenance.go`:

```go
package agent

import "primeradiant.com/serf/agent/provenance"

func (s *Session) activeCausalProvenance() *provenance.Causal {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return provenance.Clone(provenance.NilIfEmpty(&s.activeProvenance))
}

func (s *Session) replaceActiveProvenance(p *provenance.Causal) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeProvenance = provenance.Causal{}
	if cloned := provenance.Clone(p); cloned != nil {
		s.activeProvenance = *cloned
	}
}

func (s *Session) unionActiveProvenance(p *provenance.Causal) {
	if s == nil || p == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	merged := provenance.Union(provenance.NilIfEmpty(&s.activeProvenance), p)
	s.activeProvenance = provenance.Causal{}
	if merged != nil {
		s.activeProvenance = *merged
	}
}

func (s *Session) drainSteeringForTurn() []steeringMessage {
	msgs := s.drainSteering()
	for _, msg := range msgs {
		s.unionActiveProvenance(msg.Provenance)
	}
	return msgs
}
```

- [ ] **Step 4: Add provenance to steering messages**

In `agent/session_queue.go`, add provenance to `steeringMessage` and `queuedInput`:

```go
type steeringMessage struct {
	Text       string
	Images     []ImageAttachment
	Provenance *provenance.Causal
}

type queuedInput struct {
	Text       string
	Images     []ImageAttachment
	Provenance *provenance.Causal
}
```

Add the import:

```go
"primeradiant.com/serf/agent/provenance"
```

Add provenance-aware steering helpers:

```go
func (s *Session) trySteerWithProvenance(msg string, p *provenance.Causal) bool {
	return s.trySteerWithImagesAndProvenance(msg, nil, p)
}

func (s *Session) trySteerWithImagesAndProvenance(msg string, images []ImageAttachment, p *provenance.Causal) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closingOrClosedLocked() {
		return false
	}
	if strings.TrimSpace(msg) == "" && len(images) == 0 {
		return false
	}
	entry := steeringMessage{Text: msg, Provenance: provenance.Clone(p)}
	if len(images) > 0 {
		entry.Images = append([]ImageAttachment(nil), images...)
	}
	s.steeringQueue = append(s.steeringQueue, entry)
	return true
}
```

Change `trySteerWithImages` to delegate to the provenance-aware helper:

```go
func (s *Session) trySteerWithImages(msg string, images []ImageAttachment) bool {
	return s.trySteerWithImagesAndProvenance(msg, images, nil)
}
```

When `DrainAsSteerWithInput` copies queued entries into a combined steering message, keep the union of their provenance:

```go
var combinedProvenance *provenance.Causal
for _, entry := range entries {
	if strings.TrimSpace(entry.Text) != "" {
		texts = append(texts, entry.Text)
	}
	drainedImages = append(drainedImages, entry.Images...)
	combinedProvenance = provenance.Union(combinedProvenance, entry.Provenance)
}
combined := strings.Join(texts, "\n\n")
if len(drainedImages) == 0 {
	s.trySteerWithProvenance(combined, combinedProvenance)
} else {
	s.trySteerWithImagesAndProvenance(combined, drainedImages, combinedProvenance)
}
```

- [ ] **Step 5: Stamp emitted events and pass envelopes to watches**

In `agent/session_events.go`, change `emit` so it gets the event envelope back from `sendEvent`:

```go
func (s *Session) emit(kind events.EventKind, data events.EventData) {
	s.emitWithProvenance(kind, data, s.activeCausalProvenance())
}

func (s *Session) emitWithProvenance(kind events.EventKind, data events.EventData, p *provenance.Causal) {
	if s == nil || s.events == nil {
		return
	}
	data, ev := s.sendEvent(kind, data, p)
	if s.jobManager != nil {
		s.jobManager.onSessionEvent(ev)
	}
	if kind == events.EventWarning {
		s.fireNotificationHook(warningHookMessage(data))
	}
}
```

Update `emitDiagnosticWarning`:

```go
func (s *Session) emitDiagnosticWarning(data events.WarningData) {
	if s == nil || s.events == nil {
		return
	}
	s.sendEvent(events.EventWarning, data, s.activeCausalProvenance())
}
```

Replace `sendEvent` with:

```go
func (s *Session) sendEvent(kind events.EventKind, data events.EventData, p *provenance.Causal) (events.EventData, events.SessionEvent) {
	data = enrichDiagnosticData(kind, data)
	ev := events.New(data)
	ev.SessionID = s.id
	ev.Provenance = provenance.Clone(p)
	s.eventsMu.RLock()
	if !s.eventsClosed {
		select {
		case s.events <- ev:
		default:
		}
	}
	s.eventsMu.RUnlock()
	return data, ev
}
```

Add the import:

```go
"primeradiant.com/serf/agent/provenance"
```

- [ ] **Step 6: Reset and union provenance at turn boundaries**

In `agent/session_lifecycle.go`, add these calls:

```go
func (s *Session) acceptUserInput(ctx context.Context, input string, images []ImageAttachment) (proceed bool) {
	s.replaceActiveProvenance(nil)
	...
	for _, msg := range s.drainSteeringForTurn() {
		...
	}
}
```

```go
func (s *Session) acceptContinuationInput(ctx context.Context, input string) {
	s.replaceActiveProvenance(nil)
	...
	for _, msg := range s.drainSteeringForTurn() {
		...
	}
}
```

For now, in `acceptNotificationInput`, keep the reset empty before notification provenance is added in Task 5:

```go
func (s *Session) acceptNotificationInput(ctx context.Context) (proceed bool) {
	s.replaceActiveProvenance(nil)
	...
	for _, msg := range s.drainSteeringForTurn() {
		...
	}
}
```

In `agent/session_tool_round.go`, replace the mid-turn steering drain:

```go
for _, msg := range s.drainSteeringForTurn() {
	s.appendTurn(schema.TurnSteering, steeringMessageToLLM(msg))
	s.emit(events.EventSteeringInjected, steeringInjectedDataFromMessage(msg))
}
```

- [ ] **Step 7: Make communicate inbox drains provenance-aware**

In `agent/session_tool_registry.go`, wire the tool dependency to the provenance-aware drain:

```go
drainSteering:   s.drainSteeringForTurn,
prependSteering: s.prependSteering,
```

Leave `agent/session_tools_communicate.go` using `deps.drainSteering()` and `deps.prependSteering(deferred)`. The deferred entries already carry `Provenance`.

- [ ] **Step 8: Run session provenance tests to verify they pass**

Run:

```bash
go test ./agent -run 'Test(EmitAttachesActiveProvenance|ActiveProvenanceResetsForExternalInput|DrainSteeringForTurnUnionsMessageProvenance|PrependSteeringPreservesProvenanceForDeferredImages|CommunicateInboxDrainUnionsProvenance|DrainAsSteerCreatesExternalSteering)' -count=1
```

Expected: PASS.

- [ ] **Step 9: Run focused regression tests around steering and communicate**

Run:

```bash
go test ./agent -run 'Test.*(Steer|DrainAsSteer|Communicate)' -count=1
```

Expected: PASS.

- [ ] **Step 10: Commit session provenance propagation**

Run:

```bash
git status --short
git add agent/session.go agent/session_provenance.go agent/session_events.go agent/session_queue.go agent/session_lifecycle.go agent/session_tool_round.go agent/session_tool_registry.go agent/session_tools_communicate.go agent/session_provenance_test.go
git commit -m "feat(observer): propagate provenance through session turns" -m "Stamp active causal provenance onto emitted events, reset it for each new external top-level input, and union it when steering is consumed before or during a model turn. The communicate inbox drain now participates in the same provenance path."
```

Expected: commit succeeds.

---

### Task 4: Durable Jobstore Provenance

**Files:**
- Modify: `agent/internal/jobstore/record.go`
- Modify: `agent/internal/jobstore/event.go`
- Modify: `agent/internal/jobstore/fold.go`
- Test: `agent/internal/jobstore/fold_test.go`
- Test: `agent/internal/jobstore/watch_test.go`
- Test: `agent/internal/jobstore/event_test.go`

- [ ] **Step 1: Write failing jobstore fold tests**

Append to `agent/internal/jobstore/fold_test.go`:

```go
func TestFoldStoresJobProvenanceFromStartedEvent(t *testing.T) {
	p := provenance.WithWatch(nil, "watch_A", "wg_1", "wd_1", "session_1", "caller")
	events := []Event{
		ev(EventJobStarted, 1, "job_A", func(e *Event) {
			e.Type = JobDelegate
			e.OwnerSessionID = "session_1"
			e.VisibleToSession = "session_1"
			e.Provenance = p
		}),
	}

	rec := Fold(events)["job_A"]
	if rec == nil {
		t.Fatal("job_A missing")
	}
	if !provenance.ContainsWatch(rec.Provenance, "watch_A", "wg_1") {
		t.Fatalf("record provenance = %+v, want watch_A/wg_1", rec.Provenance)
	}
}

func TestFoldStoresNotificationProvenanceFromPendingEvent(t *testing.T) {
	p := provenance.WithWatch(nil, "watch_A", "wg_1", "wd_1", "session_1", "caller")
	events := []Event{
		ev(EventJobStarted, 1, "job_A", func(e *Event) {
			e.Type = JobDelegate
			e.OwnerSessionID = "session_1"
			e.VisibleToSession = "session_1"
		}),
		ev(EventJobFinished, 2, "job_A", func(e *Event) {
			e.Status = StatusCompleted
			e.TerminalGen = "GEN1"
		}),
		ev(EventJobNotificationPending, 3, "job_A", func(e *Event) {
			e.TerminalGen = "GEN1"
			e.Provenance = p
		}),
	}

	rec := Fold(events)["job_A"]
	if rec == nil {
		t.Fatal("job_A missing")
	}
	if !provenance.ContainsWatch(rec.NotificationProvenance, "watch_A", "wg_1") {
		t.Fatalf("notification provenance = %+v, want watch_A/wg_1", rec.NotificationProvenance)
	}
}
```

Add the import to `agent/internal/jobstore/fold_test.go`:

```go
"primeradiant.com/serf/agent/provenance"
```

Append to `agent/internal/jobstore/watch_test.go`:

```go
func TestFoldWatchSendsPreservesProvenance(t *testing.T) {
	key := WatchSendKey{
		VisibleSessionID:        "session_1",
		WatchID:                 "watch_A",
		WatchTarget:             "caller",
		ResolvedWatchedIdentity: "caller",
		ResolvedSendTo:          "dlg_1",
		WatchGeneration:         "wg_1",
	}
	p := provenance.WithWatch(nil, "watch_A", "wg_1", "wd_1", "session_1", "caller")
	rec := FoldWatchSends([]Event{{
		Kind: EventWatchSendPending,
		Seq:  1,
		WatchSend: &WatchSendState{
			Key:        key,
			DeliveryID: "wd_1",
			UpdateSeq:  1,
			Provenance: p,
		},
	}})

	pending := rec.Pending[key]
	if pending == nil {
		t.Fatal("pending watch send missing")
	}
	if !provenance.ContainsWatch(pending.Provenance, "watch_A", "wg_1") {
		t.Fatalf("pending provenance = %+v, want watch_A/wg_1", pending.Provenance)
	}
}
```

Add the import to `agent/internal/jobstore/watch_test.go`:

```go
"primeradiant.com/serf/agent/provenance"
```

- [ ] **Step 2: Run jobstore tests to verify they fail**

Run:

```bash
go test ./agent/internal/jobstore -run 'Test(FoldStoresJobProvenanceFromStartedEvent|FoldStoresNotificationProvenanceFromPendingEvent|FoldWatchSendsPreservesProvenance)' -count=1
```

Expected: FAIL because jobstore provenance fields do not exist.

- [ ] **Step 3: Add provenance fields to durable records and events**

In `agent/internal/jobstore/record.go`, add the import:

```go
"primeradiant.com/serf/agent/provenance"
```

Add to `DelegateRestoreDescriptor`:

```go
Provenance *provenance.Causal `json:"provenance,omitempty"`
```

Add to `WatchSendState`:

```go
Provenance *provenance.Causal `json:"provenance,omitempty"`
```

Add to `JobRecord`:

```go
Provenance             *provenance.Causal `json:"provenance,omitempty"`
NotificationProvenance *provenance.Causal `json:"notification_provenance,omitempty"`
```

In `agent/internal/jobstore/event.go`, add the import:

```go
import (
	"time"

	"primeradiant.com/serf/agent/provenance"
)
```

Add to `Event`:

```go
Provenance *provenance.Causal `json:"provenance,omitempty"`
```

- [ ] **Step 4: Fold provenance**

In `agent/internal/jobstore/fold.go`, apply provenance in `applyEvent`:

```go
case EventJobStarted:
	...
	r.Provenance = provenance.Clone(e.Provenance)
```

```go
case EventJobNotificationPending:
	if !notificationMatchesTerminalGeneration(r, e) {
		return
	}
	if r.NotifyState == NotifyNotArmed {
		r.NotifyState = NotifyPending
	}
	if e.Provenance != nil {
		r.NotificationProvenance = provenance.Clone(e.Provenance)
	}
```

Add the import:

```go
"primeradiant.com/serf/agent/provenance"
```

- [ ] **Step 5: Run jobstore tests to verify they pass**

Run:

```bash
go test ./agent/internal/jobstore -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit durable provenance fields**

Run:

```bash
git status --short
git add agent/internal/jobstore/record.go agent/internal/jobstore/event.go agent/internal/jobstore/fold.go agent/internal/jobstore/fold_test.go agent/internal/jobstore/watch_test.go
git commit -m "feat(observer): persist causal provenance in jobstore" -m "Store causal provenance on job records, notification-pending events, delegate restore descriptors, and watch-send state so suppression can be re-derived after restart without an in-memory fired ledger."
```

Expected: commit succeeds.

---

### Task 5: Job Lifecycle and Notification Provenance

**Files:**
- Modify: `agent/jobs.go`
- Modify: `agent/job_notify.go`
- Modify: `agent/session_lifecycle.go`
- Modify: `agent/session_init.go`
- Test: `agent/job_notify_test.go`
- Test: `agent/session_provenance_test.go`

- [ ] **Step 1: Write failing lifecycle and notification tests**

Append to `agent/job_notify_test.go`:

```go
func TestJobNotificationFromRecordUsesNotificationProvenance(t *testing.T) {
	jobProv := provenance.WithWatch(nil, "watch_job", "wg_1", "wd_job", "session_1", "caller")
	notificationProv := provenance.WithWatch(nil, "watch_note", "wg_1", "wd_note", "session_1", "caller")
	n := jobNotificationFromRecord(&jobstore.JobRecord{
		JobID:                  "job_A",
		Type:                   jobstore.JobDelegate,
		Status:                 jobstore.StatusCompleted,
		Provenance:             jobProv,
		NotificationProvenance: notificationProv,
	})
	if !provenance.ContainsWatch(n.Provenance, "watch_note", "wg_1") {
		t.Fatalf("notification provenance = %+v, want notification provenance", n.Provenance)
	}
	if provenance.ContainsWatch(n.Provenance, "watch_job", "wg_1") {
		t.Fatalf("notification provenance = %+v, should prefer notification provenance over job provenance", n.Provenance)
	}
}

func TestJobNotificationFromRecordFallsBackToJobProvenance(t *testing.T) {
	jobProv := provenance.WithWatch(nil, "watch_job", "wg_1", "wd_job", "session_1", "caller")
	n := jobNotificationFromRecord(&jobstore.JobRecord{
		JobID:      "job_A",
		Type:       jobstore.JobDelegate,
		Status:     jobstore.StatusCompleted,
		Provenance: jobProv,
	})
	if !provenance.ContainsWatch(n.Provenance, "watch_job", "wg_1") {
		t.Fatalf("notification provenance = %+v, want job provenance fallback", n.Provenance)
	}
}
```

Add imports to `agent/job_notify_test.go` if missing:

```go
"primeradiant.com/serf/agent/internal/jobstore"
"primeradiant.com/serf/agent/provenance"
```

Append to `agent/session_provenance_test.go`:

```go
func TestAcceptNotificationInputAdoptsNotificationProvenance(t *testing.T) {
	s := newTestSession(t)
	s.events = make(chan events.SessionEvent, 4)
	s.enqueueJobNotification(jobNotification{
		JobID:      "job_A",
		JobType:    string(jobstore.JobDelegate),
		Status:     string(jobstore.StatusCompleted),
		Provenance: testProvenance("watch_A", "wg_1"),
	})

	if !s.acceptNotificationInput(context.Background()) {
		t.Fatal("notification input should proceed")
	}
	if !provenance.ContainsWatch(s.activeCausalProvenance(), "watch_A", "wg_1") {
		t.Fatalf("active provenance = %+v, want notification provenance", s.activeCausalProvenance())
	}
	ev := <-s.events
	if ev.Kind != events.EventSteeringInjected {
		t.Fatalf("first event = %s, want STEERING_INJECTED", ev.Kind)
	}
	if !provenance.ContainsWatch(ev.Provenance, "watch_A", "wg_1") {
		t.Fatalf("steering event provenance = %+v, want watch_A/wg_1", ev.Provenance)
	}
}
```

- [ ] **Step 2: Run lifecycle tests to verify they fail**

Run:

```bash
go test ./agent -run 'Test(JobNotificationFromRecordUsesNotificationProvenance|JobNotificationFromRecordFallsBackToJobProvenance|AcceptNotificationInputAdoptsNotificationProvenance)' -count=1
```

Expected: FAIL because `jobNotification.Provenance` and notification adoption do not exist.

- [ ] **Step 3: Add provenance to in-memory job notifications**

In `agent/jobs.go`, update `jobNotification`:

```go
type jobNotification struct {
	JobID, JobType, Status, Reason, TranscriptRef string
	OutputBytes                                   int64
	ExitCode                                      *int
	Provenance                                    *provenance.Causal
	WatchSend                                     *watchSendToken
	watchSendFrame                                string
}
```

Add the import:

```go
"primeradiant.com/serf/agent/provenance"
```

In `agent/job_notify.go`, update `jobNotificationFromRecord`:

```go
func jobNotificationFromRecord(rec *jobstore.JobRecord) jobNotification {
	p := rec.NotificationProvenance
	if p == nil {
		p = rec.Provenance
	}
	return jobNotification{
		JobID:         rec.JobID,
		JobType:       string(rec.Type),
		Status:        string(rec.Status),
		Reason:        rec.Reason,
		TranscriptRef: rec.TranscriptRef,
		OutputBytes:   rec.OutputBytes,
		ExitCode:      rec.ExitCode,
		Provenance:    provenance.Clone(p),
	}
}
```

Add the import:

```go
"primeradiant.com/serf/agent/provenance"
```

- [ ] **Step 4: Store provenance on jobs and emitted lifecycle events**

In `agent/jobs.go`, add a getter to `jobManager`:

```go
currentProvenance func() *provenance.Causal
```

Add a helper:

```go
func (jm *jobManager) currentCausalProvenance() *provenance.Causal {
	if jm == nil || jm.currentProvenance == nil {
		return nil
	}
	return provenance.Clone(jm.currentProvenance())
}
```

In `agent/session_init.go`, after `newJobManager`, set:

```go
jm.currentProvenance = s.activeCausalProvenance
```

In shell job creation, set `rec.Provenance` and `started.Provenance` from one snapshot:

```go
jobProvenance := jm.currentCausalProvenance()
rec.Provenance = provenance.Clone(jobProvenance)
...
started := jobstore.Event{
	...
	Provenance: provenance.Clone(jobProvenance),
}
```

In `emitJobStarted` and `emitJobFinished`, use `emitWithProvenance` through a new jobManager signature:

```go
emit func(events.EventKind, events.EventData, *provenance.Causal)
```

Update callers in `agent/session_init.go`:

```go
jm.emit = s.emitWithProvenance
```

Update lifecycle emitters:

```go
func (jm *jobManager) emitJobStarted(e jobstore.Event, run *runningJob) {
	if jm == nil || jm.emit == nil {
		return
	}
	jm.emit(events.EventJobStarted, events.JobStartedData{
		JobID:   e.JobID,
		JobType: string(e.Type),
		Status:  string(jobstore.StatusRunning),
	}, e.Provenance)
}
```

```go
func (jm *jobManager) emitJobFinished(e jobstore.Event, run *runningJob) {
	if jm == nil || jm.emit == nil {
		return
	}
	jobType := ""
	transcriptRef := ""
	if run != nil && run.rec != nil {
		jobType = string(run.rec.Type)
		transcriptRef = run.rec.TranscriptRef
	}
	jm.emit(events.EventJobFinished, events.JobFinishedData{
		JobID:         e.JobID,
		JobType:       jobType,
		Status:        string(e.Status),
		Reason:        e.Reason,
		ExitCode:      e.ExitCode,
		OutputBytes:   e.OutputBytes,
		TranscriptRef: transcriptRef,
	}, e.Provenance)
}
```

Remove `FromWatch` from `events.JobStartedData` and `events.JobFinishedData` in Task 7 when `job_watch` no longer reads it.

- [ ] **Step 5: Persist provenance on pending notifications and enqueue it**

In `agent/jobs.go`, every `EventJobNotificationPending` built from a job must include provenance:

```go
pending := jobstore.Event{
	Kind:        jobstore.EventJobNotificationPending,
	TS:          terminal.endedAt,
	JobID:       run.rec.JobID,
	TerminalGen: terminal.generation,
	Provenance:  provenance.Clone(run.rec.Provenance),
}
```

Every `jm.enqueue(jobNotification{...})` for a terminal job must include:

```go
Provenance: provenance.Clone(run.rec.Provenance),
```

For restore/reconcile paths that only have a folded record, use:

```go
Provenance: provenance.Clone(rec.Provenance),
```

- [ ] **Step 6: Adopt notification provenance in notification turns**

In `agent/session_lifecycle.go`, after `jobNotifs` is known and before `formatJobNotificationReminder`, replace active provenance with the union of delivered notification provenance:

```go
var notificationProvenance *provenance.Causal
for _, n := range jobNotifs {
	notificationProvenance = provenance.Union(notificationProvenance, n.notification.Provenance)
}
s.replaceActiveProvenance(notificationProvenance)
```

Add the import:

```go
"primeradiant.com/serf/agent/provenance"
```

- [ ] **Step 7: Run lifecycle tests to verify they pass**

Run:

```bash
go test ./agent -run 'Test(JobNotificationFromRecordUsesNotificationProvenance|JobNotificationFromRecordFallsBackToJobProvenance|AcceptNotificationInputAdoptsNotificationProvenance)' -count=1
```

Expected: PASS.

- [ ] **Step 8: Run job and notification regression tests**

Run:

```bash
go test ./agent -run 'Test.*(JobNotification|Restart|Delegate|Shell)' -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit job lifecycle provenance**

Run:

```bash
git status --short
git add agent/jobs.go agent/job_notify.go agent/session_lifecycle.go agent/session_init.go agent/job_notify_test.go agent/session_provenance_test.go
git commit -m "feat(observer): carry provenance through jobs and notifications" -m "Store active provenance on started jobs, use stored provenance for detached lifecycle events, persist notification provenance, and adopt notification provenance when rendering notification turns."
```

Expected: commit succeeds.

---

### Task 6: Delegate Caller Route Provenance

**Files:**
- Modify: `agent/session_config.go`
- Modify: `agent/subagents.go`
- Modify: `agent/job_delegate.go`
- Modify: `agent/jobs.go`
- Test: `agent/job_delegate_test.go`

- [ ] **Step 1: Write failing delegate caller route tests**

Append to `agent/job_delegate_test.go`:

```go
func TestDelegateSendCallerCarriesActiveProvenanceToParentSteering(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	p := provenance.WithWatch(nil, "watch_A", "wg_1", "wd_1", parent.ID(), "caller")
	child.replaceActiveProvenance(p)
	child.cfg.spawn.parentSteerDelivered = parent.trySteerWithProvenance

	res := child.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  runtimeMessageAliasCaller,
		Message: "PYTHON_QUOTE delivery=wd_1 quote=Ni!",
	})
	if res.Err != nil || !res.Delivered {
		t.Fatalf("sendDelegateMessage = %+v, want delivered", res)
	}

	parent.mu.Lock()
	defer parent.mu.Unlock()
	if len(parent.steeringQueue) != 1 {
		t.Fatalf("parent steering queue = %d, want 1", len(parent.steeringQueue))
	}
	if !provenance.ContainsWatch(parent.steeringQueue[0].Provenance, "watch_A", "wg_1") {
		t.Fatalf("steering provenance = %+v, want watch_A/wg_1", parent.steeringQueue[0].Provenance)
	}
}

func TestRunningDelegateWatchSendCarriesProvenanceToObserverSteering(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := &subagent{sess: child, running: true}
	childID := child.ID()
	parent.subagents.set(childID, sub)
	run, err := parent.attachDelegateJob(parent.jobManager, childID, "observer", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}
	p := provenance.WithWatch(nil, "watch_A", "wg_1", "wd_1", parent.ID(), "caller")

	res := parent.sendRunningDelegateMessage(run.rec.DelegateID, "Watch frame", run.rec, true, p)
	if res.Err != nil {
		t.Fatalf("sendRunningDelegateMessage: %+v", res)
	}
	child.mu.Lock()
	defer child.mu.Unlock()
	if len(child.steeringQueue) != 1 {
		t.Fatalf("child steering queue = %d, want 1", len(child.steeringQueue))
	}
	if !provenance.ContainsWatch(child.steeringQueue[0].Provenance, "watch_A", "wg_1") {
		t.Fatalf("child steering provenance = %+v, want watch_A/wg_1", child.steeringQueue[0].Provenance)
	}
}
```

Add imports if missing:

```go
"primeradiant.com/serf/agent/provenance"
```

- [ ] **Step 2: Run delegate provenance tests to verify they fail**

Run:

```bash
go test ./agent -run 'Test(DelegateSendCallerCarriesActiveProvenanceToParentSteering|RunningDelegateWatchSendCarriesProvenanceToObserverSteering)' -count=1
```

Expected: FAIL because parent steering callbacks and delegate send paths do not accept provenance.

- [ ] **Step 3: Change parent steering callback signatures**

In `agent/session_config.go`, update callback types:

```go
parentSteer func(string, *provenance.Causal)

parentSteerDelivered func(string, *provenance.Causal) bool
```

Add import:

```go
"primeradiant.com/serf/agent/provenance"
```

In `agent/subagents.go`, set callbacks:

```go
subCfg.spawn.parentSteer = s.SteerWithProvenance
subCfg.spawn.parentSteerDelivered = s.trySteerWithProvenance
```

Add exported or unexported session helper in `agent/session_queue.go`:

```go
func (s *Session) SteerWithProvenance(msg string, p *provenance.Causal) {
	_ = s.trySteerWithProvenance(msg, p)
}
```

Update tests that assign `parentSteer` or `parentSteerDelivered` with this shape:

```go
s.cfg.spawn.parentSteer = func(string, *provenance.Causal) { called = true }
s.cfg.spawn.parentSteerDelivered = func(string, *provenance.Causal) bool { return true }
```

- [ ] **Step 4: Carry provenance in send message arguments**

In `agent/job_delegate.go`, update `sendMessageArgs`:

```go
Provenance *provenance.Causal
```

When `delegate_send(to="caller")` uses runtime alias, pass the sender's active provenance:

```go
callerProvenance := provenance.Clone(args.Provenance)
if callerProvenance == nil {
	callerProvenance = s.activeCausalProvenance()
}
if steer := s.cfg.spawn.parentSteerDelivered; steer != nil {
	delivered = steer(message, callerProvenance)
} else if steer := s.cfg.spawn.parentSteer; steer != nil {
	steer(message, callerProvenance)
} else {
	delivered = s.trySteerWithProvenance(message, callerProvenance)
}
```

Update `sendRunningDelegateMessage` signature:

```go
func (s *Session) sendRunningDelegateMessage(target, message string, rec *jobstore.JobRecord, fromWatch bool, p *provenance.Causal) sendMessageResult
```

Inside it, steer with provenance:

```go
delivered := sub.sess.trySteerWithProvenance(message, p)
```

Update all call sites to pass `args.Provenance` for watch sends and `nil` for ordinary model sends.

- [ ] **Step 5: Store provenance on observer delegate starts and resumes**

In `attachDelegateJobWithRestoreAndDelegate`, choose the job provenance from active session state:

```go
jobProvenance := s.activeCausalProvenance()
if previousRestore != nil && previousRestore.Provenance != nil {
	jobProvenance = previousRestore.Provenance
}
```

Add to the restore descriptor:

```go
restore.Provenance = provenance.Clone(jobProvenance)
```

Add to `run.rec` and `started`:

```go
Provenance: provenance.Clone(jobProvenance),
```

When resuming from a watch send, pass `args.Provenance` through `resumeOrFindRunningDelegate` and into the attach path. Keep `fromWatch` only as an internal watch-send delivery classification flag until Task 7 removes event-payload dependence on it.

- [ ] **Step 6: Pass watch-send provenance into delivery**

In `agent/job_watch.go`, update the send call in `deliverPendingWatchSend`:

```go
res := send(ctx, sendMessageArgs{
	Target:        state.Key.ResolvedSendTo,
	Message:       state.Frame,
	OnIdle:        "start",
	Background:    true,
	BackgroundSet: true,
	FromWatch:     true,
	Provenance:    provenance.Clone(state.Provenance),
})
```

- [ ] **Step 7: Run delegate provenance tests to verify they pass**

Run:

```bash
go test ./agent -run 'Test(DelegateSendCallerCarriesActiveProvenanceToParentSteering|RunningDelegateWatchSendCarriesProvenanceToObserverSteering)' -count=1
```

Expected: PASS.

- [ ] **Step 8: Run delegate regression tests**

Run:

```bash
go test ./agent -run 'Test.*Delegate' -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit delegate provenance propagation**

Run:

```bash
git status --short
git add agent/session_config.go agent/subagents.go agent/session_queue.go agent/job_delegate.go agent/jobs.go agent/job_watch.go agent/job_delegate_test.go
git commit -m "feat(observer): propagate provenance through delegate routes" -m "Carry active provenance through delegate_send(to=caller), watch-delivered observer steering, delegate resumes, and delegate restore descriptors. This makes observer injections into the caller causally attributable to the watch delivery that produced them."
```

Expected: commit succeeds.

---

### Task 7: Watch Delivery Provenance and Same-Watch Suppression

**Files:**
- Modify: `agent/job_watch.go`
- Modify: `agent/events/payloads.go`
- Modify: `agent/jobs.go`
- Test: `agent/job_watch_test.go`
- Test: `agent/job_watch_observer_test.go`

- [ ] **Step 1: Write failing watch suppression tests**

Append to `agent/job_watch_test.go`:

```go
func onlyWatchConfigForTest(t *testing.T, jm *jobManager) *watchConfig {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if len(jm.watches) != 1 {
		t.Fatalf("watch count = %d, want 1", len(jm.watches))
	}
	for _, cfg := range jm.watches {
		return cfg
	}
	panic("unreachable")
}

func TestJobWatchSuppressesSameWatchProvenanceBeforeDeliveryAccounting(t *testing.T) {
	jm := newTestJM(t)
	seedWatchSendDelegateTarget(t, jm, "dlg_1")
	installWatchBelowValidation(t, jm, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{string(events.EventCommunicate)},
		Send:   &watchSendArgs{To: "dlg_1", Message: "observe"},
	})
	cfg := onlyWatchConfigForTest(t, jm)

	ev := events.New(events.CommunicateData{Message: "PYTHON_QUOTE quote=Ni!", AwaitReply: false})
	ev.SessionID = jm.sessionID
	ev.Provenance = provenance.WithWatch(nil, cfg.watchID, cfg.generation, "wd_1", jm.sessionID, "caller")

	jm.onSessionEvent(ev)

	if cfg.deliveries != 0 {
		t.Fatalf("deliveries = %d, want 0 for suppressed event", cfg.deliveries)
	}
	if len(cfg.pending) != 0 {
		t.Fatalf("pending sends = %d, want 0 for suppressed event", len(cfg.pending))
	}
}

func TestJobWatchDoesNotSuppressDifferentGeneration(t *testing.T) {
	jm := newTestJM(t)
	seedWatchSendDelegateTarget(t, jm, "dlg_1")
	installWatchBelowValidation(t, jm, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{string(events.EventCommunicate)},
		Send:   &watchSendArgs{To: "dlg_1", Message: "observe"},
	})
	cfg := onlyWatchConfigForTest(t, jm)
	oldGeneration := cfg.generation
	cfg.generation = "wg_recreated"

	ev := events.New(events.CommunicateData{Message: "actually alpha marker", AwaitReply: false})
	ev.SessionID = jm.sessionID
	ev.Provenance = provenance.WithWatch(nil, cfg.watchID, oldGeneration, "wd_old", jm.sessionID, "caller")

	jm.onSessionEvent(ev)

	if cfg.deliveries == 0 && len(cfg.pending) == 0 {
		t.Fatal("old generation provenance must not suppress new generation")
	}
}
```

Append to `agent/job_watch_observer_test.go`:

```go
func TestWatchSendStateCarriesDeliveryProvenance(t *testing.T) {
	s := newTestSession(t)
	cfg := &watchConfig{
		target:           runtimeMessageAliasCaller,
		watchID:          "watch_A",
		generation:       "wg_1",
		send:             &watchSendArgs{To: "dlg_1", Message: "observe"},
		pending:          make(map[jobstore.WatchSendKey]*jobstore.WatchSendState),
		settledUpdateSeq: make(map[jobstore.WatchSendKey]uint64),
	}
	ev := events.New(events.CommunicateData{Message: "actually alpha marker", AwaitReply: false})
	ev.SessionID = s.ID()

	d := s.jobManager.watchSendSnapshot(cfg, runtimeMessageAliasCaller, "event: COMMUNICATE", ev)
	state := s.jobManager.watchSendState(d, "dlg_1")

	if !provenance.ContainsWatch(state.Provenance, "watch_A", "wg_1") {
		t.Fatalf("watch send provenance = %+v, want watch_A/wg_1", state.Provenance)
	}
	if provenance.LatestDeliveryID(state.Provenance) != d.deliveryID {
		t.Fatalf("latest delivery id = %q, want %q", provenance.LatestDeliveryID(state.Provenance), d.deliveryID)
	}
}
```

Add imports where needed:

```go
"primeradiant.com/serf/agent/events"
"primeradiant.com/serf/agent/internal/jobstore"
"primeradiant.com/serf/agent/provenance"
```

- [ ] **Step 2: Run watch suppression tests to verify they fail**

Run:

```bash
go test ./agent -run 'TestJobWatch(SuppressesSameWatchProvenanceBeforeDeliveryAccounting|DoesNotSuppressDifferentGeneration)|TestWatchSendStateCarriesDeliveryProvenance' -count=1
```

Expected: FAIL because watch events are not envelope-based and watch-send delivery provenance does not exist.

- [ ] **Step 3: Change watch event handling to use session event envelopes**

In `agent/session_events.go`, Task 3 already calls:

```go
s.jobManager.onSessionEvent(ev)
```

In `agent/job_watch.go`, replace:

```go
func (jm *jobManager) onSessionEvent(kind events.EventKind, data events.EventData)
```

with:

```go
func (jm *jobManager) onSessionEvent(ev events.SessionEvent)
```

Inside the function, derive:

```go
kind := ev.Kind
data := ev.Data
```

Remove the call to `isWatchOriginEventData(data)`.

- [ ] **Step 4: Add same-watch suppression**

In `agent/job_watch.go`, add:

```go
func shouldSuppressWatch(cfg *watchConfig, p *provenance.Causal) bool {
	if cfg == nil {
		return false
	}
	return provenance.ContainsWatch(p, cfg.watchID, cfg.generation)
}
```

In `onSessionEvent`, after active target and event-kind checks but before `triggerEvery` accounting, add:

```go
if shouldSuppressWatch(cfg, ev.Provenance) {
	continue
}
```

Delete `isWatchOriginEventData`. In `agent/events/payloads.go`, remove `FromWatch` from `JobStartedData` and `JobFinishedData`. In `agent/jobs.go`, remove `FromWatch` fields from lifecycle payload construction.

- [ ] **Step 5: Create watch delivery provenance**

In `agent/job_watch.go`, update `watchSendDelivery`:

```go
provenance *provenance.Causal
eventKind  events.EventKind
eventData  events.EventData
```

Change `watchSendSnapshot`:

```go
func (jm *jobManager) watchSendSnapshot(cfg *watchConfig, jobID, trigger string, root events.SessionEvent) watchSendDelivery {
	sendTo := ""
	if cfg.send != nil {
		sendTo = cfg.send.To
	}
	cfg.nextUpdateSeq++
	deliveryID := jobstore.NewWatchSendDeliveryID()
	return watchSendDelivery{
		cfg:                      cfg,
		key:                      watchKey{VisibleSessionID: jm.sessionID, Target: cfg.target, SendTo: sendTo},
		generation:               cfg.generation,
		updateSeq:                cfg.nextUpdateSeq,
		allowAfterTerminalExpiry: cfg.allowAfterTerminalExpiry,
		send:                     cloneWatchSendArgs(cfg.send),
		deliveryID:               deliveryID,
		visibleSessionID:         jm.sessionID,
		watchTarget:              cfg.target,
		watchedIdentity:          jobID,
		trigger:                  trigger,
		provenance:               provenance.WithWatch(root.Provenance, cfg.watchID, cfg.generation, deliveryID, root.SessionID, jobID),
		eventKind:                root.Kind,
		eventData:                root.Data,
	}
}
```

For non-session-event watch paths such as output match and progress ticks, use:

```go
root := events.SessionEvent{SessionID: jm.sessionID, Provenance: provenance.Clone(jobProvenanceForWatch(jm, jobID))}
delivery := jm.watchSendSnapshot(cfg, jobID, "output_match: "+match, root)
```

Add helper:

```go
func jobProvenanceForWatch(jm *jobManager, jobID string) *provenance.Causal {
	if jm == nil || jobID == "" || isWatchSessionTarget(jobID) {
		return nil
	}
	if run := jm.running[jobID]; run != nil && run.rec != nil {
		return provenance.Clone(run.rec.Provenance)
	}
	recs, err := jm.store.Load()
	if err != nil {
		return nil
	}
	if rec := recs[jobID]; rec != nil {
		return provenance.Clone(rec.Provenance)
	}
	return nil
}
```

Use this helper only while holding no `jm.mu` lock because it may load from the store.

- [ ] **Step 6: Persist watch-send provenance**

In `watchSendState`, include:

```go
Provenance: provenance.Clone(d.provenance),
```

In watch-send coalescing, union the existing and new provenance when replacing a pending state:

```go
state.Provenance = provenance.Union(existing.Provenance, state.Provenance)
```

This is required because coalesced deliveries may represent multiple causes.

- [ ] **Step 7: Run watch suppression tests to verify they pass**

Run:

```bash
go test ./agent -run 'TestJobWatch(SuppressesSameWatchProvenanceBeforeDeliveryAccounting|DoesNotSuppressDifferentGeneration)|TestWatchSendStateCarriesDeliveryProvenance' -count=1
```

Expected: PASS.

- [ ] **Step 8: Run watch regression tests**

Run:

```bash
go test ./agent -run 'Test.*Watch' -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit watch suppression**

Run:

```bash
git status --short
git add agent/job_watch.go agent/events/payloads.go agent/jobs.go agent/job_watch_test.go agent/job_watch_observer_test.go
git commit -m "feat(observer): suppress same-watch causal echoes" -m "Replace the global FromWatch watch guard with per-watch provenance membership. Watch deliveries now create and persist causal provenance, coalesced deliveries union provenance, and suppression happens before counters or pending send state mutate."
```

Expected: commit succeeds.

---

### Task 8: Content-Bearing Communicate Watch Frames

**Files:**
- Modify: `agent/job_watch.go`
- Test: `agent/job_watch_test.go`

- [ ] **Step 1: Write failing frame content tests**

Append to `agent/job_watch_test.go`:

```go
func TestBuildWatchFrameIncludesCommunicateEventContent(t *testing.T) {
	jm := newTestJM(t)
	cfg := &watchConfig{watchID: "watch_A", generation: "wg_1", send: &watchSendArgs{Message: "Filter this caller message."}}
	ev := events.New(events.CommunicateData{Message: "actually alpha marker", AwaitReply: false})
	ev.SessionID = "session_1"

	frame := jm.buildWatchFrame(cfg, runtimeMessageAliasCaller, "event: COMMUNICATE", "wd_1", ev, nil)

	for _, want := range []string{
		"Watch frame",
		"watch_id: watch_A",
		"delivery_id: wd_1",
		"job_id: caller",
		"trigger: event: COMMUNICATE",
		"provenance: external",
		"event:",
		"  kind: communicate",
		"  message: actually alpha marker",
		"  await_reply: false",
		"  truncated: false",
	} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame missing %q:\n%s", want, frame)
		}
	}
}

func TestBuildWatchFrameIncludesCompactProvenanceSummary(t *testing.T) {
	jm := newTestJM(t)
	cfg := &watchConfig{watchID: "watch_B", generation: "wg_1", send: &watchSendArgs{Message: "observe"}}
	p := provenance.WithWatch(nil, "watch_A", "wg_1", "wd_A", "session_1", "caller")
	ev := events.New(events.CommunicateData{Message: "observer caused text", AwaitReply: false})
	ev.SessionID = "session_1"
	ev.Provenance = p

	frame := jm.buildWatchFrame(cfg, runtimeMessageAliasCaller, "event: COMMUNICATE", "wd_B", ev, p)

	for _, want := range []string{
		"provenance:",
		"  watch_keys:",
		"    - watch_id: watch_A",
		"      watch_generation: wg_1",
		"  latest_delivery_id: wd_A",
	} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame missing %q:\n%s", want, frame)
		}
	}
}
```

Add imports if missing:

```go
"strings"
"primeradiant.com/serf/agent/provenance"
```

- [ ] **Step 2: Run frame tests to verify they fail**

Run:

```bash
go test ./agent -run 'TestBuildWatchFrameIncludes(CommunicateEventContent|CompactProvenanceSummary)' -count=1
```

Expected: FAIL because `buildWatchFrame` does not accept event data or provenance.

- [ ] **Step 3: Pass event data into frame building**

In `snapshotWatchSendFrame`, call:

```go
d.frame = jm.buildWatchFrame(&watchConfig{
	watchID:    d.cfg.watchID,
	generation: d.cfg.generation,
	send:       d.send,
}, d.watchedIdentity, d.trigger, d.deliveryID, events.SessionEvent{
	Kind:       d.eventKind,
	SessionID:  jm.sessionID,
	Data:       d.eventData,
	Provenance: provenance.Clone(d.provenance),
}, d.provenance)
```

Change `buildWatchFrame` signature:

```go
func (jm *jobManager) buildWatchFrame(cfg *watchConfig, jobID string, trigger string, deliveryID string, ev events.SessionEvent, p *provenance.Causal) string
```

- [ ] **Step 4: Render watch id and provenance summary**

In `buildWatchFrame`, after `Watch frame`, render:

```go
if cfg.watchID != "" {
	b.WriteString("watch_id: ")
	b.WriteString(limitWatchText(cfg.watchID, watchTriggerMaxChars))
	b.WriteString("\n")
}
b.WriteString("delivery_id: ")
b.WriteString(limitWatchText(deliveryID, watchTriggerMaxChars))
b.WriteString("\njob_id: ")
b.WriteString(limitWatchText(jobID, watchTriggerMaxChars))
b.WriteString("\ntrigger: ")
b.WriteString(limitWatchText(trigger, watchTriggerMaxChars))
b.WriteString("\n")
writeWatchFrameProvenance(&b, p)
```

Add helper:

```go
func writeWatchFrameProvenance(b *strings.Builder, p *provenance.Causal) {
	if p == nil || len(p.WatchKeys) == 0 {
		b.WriteString("provenance: external\n")
		return
	}
	b.WriteString("provenance:\n")
	b.WriteString("  watch_keys:\n")
	for _, key := range p.WatchKeys {
		b.WriteString("    - watch_id: ")
		b.WriteString(limitWatchText(key.WatchID, watchTriggerMaxChars))
		b.WriteString("\n      watch_generation: ")
		b.WriteString(limitWatchText(key.WatchGeneration, watchTriggerMaxChars))
		b.WriteString("\n")
	}
	if latest := provenance.LatestDeliveryID(p); latest != "" {
		b.WriteString("  latest_delivery_id: ")
		b.WriteString(limitWatchText(latest, watchTriggerMaxChars))
		b.WriteString("\n")
	}
}
```

- [ ] **Step 5: Render communicate event block**

Add helper:

```go
func writeWatchFrameEvent(b *strings.Builder, ev events.SessionEvent) {
	switch data := ev.Data.(type) {
	case events.CommunicateData:
		writeCommunicateWatchEvent(b, data)
	case *events.CommunicateData:
		if data != nil {
			writeCommunicateWatchEvent(b, *data)
		}
	}
}

func writeCommunicateWatchEvent(b *strings.Builder, data events.CommunicateData) {
	const maxMessageChars = 1000
	message := limitWatchText(data.Message, maxMessageChars)
	truncated := message != data.Message
	b.WriteString("event:\n")
	b.WriteString("  kind: communicate\n")
	b.WriteString("  message: ")
	b.WriteString(message)
	b.WriteString("\n")
	b.WriteString("  await_reply: ")
	if data.AwaitReply {
		b.WriteString("true\n")
	} else {
		b.WriteString("false\n")
	}
	b.WriteString("  truncated: ")
	if truncated {
		b.WriteString("true\n")
	} else {
		b.WriteString("false\n")
	}
}
```

Call it after provenance:

```go
writeWatchFrameEvent(&b, ev)
```

- [ ] **Step 6: Run frame tests to verify they pass**

Run:

```bash
go test ./agent -run 'TestBuildWatchFrameIncludes(CommunicateEventContent|CompactProvenanceSummary)' -count=1
```

Expected: PASS.

- [ ] **Step 7: Run watch frame regression tests**

Run:

```bash
go test ./agent -run 'Test.*Watch.*Frame|Test.*Watch.*Send' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit content-bearing watch frames**

Run:

```bash
git status --short
git add agent/job_watch.go agent/job_watch_test.go
git commit -m "feat(observer): render content-bearing watch frames" -m "Pass the triggering SessionEvent through the watch-send snapshot path and render communicate payload content plus compact provenance summaries in observer frames."
```

Expected: commit succeeds.

---

### Task 9: End-to-End Observer Loop Tests

**Files:**
- Test: `agent/job_watch_observer_test.go`
- Test: `agent/session_tools_jobs_test.go`

- [ ] **Step 1: Write a Monty Python in-process regression**

Append to `agent/job_watch_observer_test.go`:

```go
func TestObserverInjectionDoesNotRetriggerSameWatch(t *testing.T) {
	parent := newTestSession(t)
	observer := newTestSession(t)
	observerID := observer.ID()
	sub := &subagent{id: observerID, sess: observer, running: true, done: make(chan struct{})}
	parent.subagents.track(sub)

	run, err := parent.attachDelegateJob(parent.jobManager, observerID, "actually watcher", sub)
	if err != nil {
		t.Fatalf("attach observer: %v", err)
	}
	installWatchBelowValidation(t, parent.jobManager, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{string(events.EventCommunicate)},
		Send:   &watchSendArgs{To: run.rec.DelegateID, Message: "Filter this caller message."},
	})
	cfg := onlyWatchConfigForTest(t, parent.jobManager)

	parent.emit(events.EventCommunicate, events.CommunicateData{Message: "actually alpha marker", AwaitReply: false})
	if cfg.deliveries == 0 {
		t.Fatal("external communicate should trigger observer watch")
	}
	var state jobstore.WatchSendState
	for _, pending := range cfg.pending {
		state = *pending
	}
	if !provenance.ContainsWatch(state.Provenance, cfg.watchID, cfg.generation) {
		t.Fatalf("delivery provenance = %+v, want %s/%s", state.Provenance, cfg.watchID, cfg.generation)
	}

	observer.replaceActiveProvenance(state.Provenance)
	res := observer.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  runtimeMessageAliasCaller,
		Message: "PYTHON_QUOTE delivery=" + state.DeliveryID + " quote=Ni!",
	})
	if res.Err != nil || !res.Delivered {
		t.Fatalf("observer caller send = %+v, want delivered", res)
	}

	for _, msg := range parent.drainSteeringForTurn() {
		parent.appendTurn(schema.TurnSteering, steeringMessageToLLM(msg))
		parent.emit(events.EventSteeringInjected, steeringInjectedDataFromMessage(msg))
	}
	parent.emit(events.EventCommunicate, events.CommunicateData{Message: "acknowledged quote", AwaitReply: false})

	if cfg.deliveries != 1 {
		t.Fatalf("deliveries = %d, want only the original external trigger", cfg.deliveries)
	}
}
```

Add imports where needed:

```go
"context"
"primeradiant.com/serf/agent/schema"
```

- [ ] **Step 2: Write notification acknowledgement regression**

Append to `agent/job_watch_observer_test.go`:

```go
func TestObserverNotificationAcknowledgementDoesNotRetriggerSameWatch(t *testing.T) {
	parent := newTestSession(t)
	seedWatchSendDelegateTarget(t, parent.jobManager, "dlg_observer")
	installWatchBelowValidation(t, parent.jobManager, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{string(events.EventCommunicate)},
		Send:   &watchSendArgs{To: "dlg_observer", Message: "observe"},
	})
	cfg := onlyWatchConfigForTest(t, parent.jobManager)
	parent.enqueueJobNotification(jobNotification{
		JobID:      "job_observer",
		JobType:    string(jobstore.JobDelegate),
		Status:     string(jobstore.StatusCompleted),
		Provenance: provenance.WithWatch(nil, cfg.watchID, cfg.generation, "wd_1", parent.ID(), "caller"),
	})

	if !parent.acceptNotificationInput(context.Background()) {
		t.Fatal("notification input should proceed")
	}
	parent.emit(events.EventCommunicate, events.CommunicateData{Message: "observer done acknowledged", AwaitReply: false})

	if cfg.deliveries != 0 {
		t.Fatalf("deliveries = %d, want notification acknowledgement suppressed", cfg.deliveries)
	}
}
```

- [ ] **Step 3: Run observer loop tests**

Run:

```bash
go test ./agent -run 'TestObserver(InjectionDoesNotRetriggerSameWatch|NotificationAcknowledgementDoesNotRetriggerSameWatch)' -count=1
```

Expected: PASS.

- [ ] **Step 4: Run scenario-adjacent tests**

Run:

```bash
go test ./agent -run 'Test.*(Observer|Watch|DelegateSend|Notification)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit observer loop regressions**

Run:

```bash
git status --short
git add agent/job_watch_observer_test.go agent/session_tools_jobs_test.go
git commit -m "test(observer): cover injection and notification loop suppression" -m "Add in-process regressions for the two live-risk paths: observer delegate_send(to=caller) causing caller steering, and parent acknowledgement of observer terminal notifications. Both must carry provenance and suppress same-watch re-entry."
```

Expected: commit succeeds.

---

### Task 10: Scenario Documentation Updates

**Files:**
- Modify: `test/scenarios/job-watch-actually-monty-python-injection.md`
- Modify: `test/scenarios/job-watch-observer-snide-thread.md`
- Modify: `test/scenarios/INDEX.md`

- [ ] **Step 1: Update Monty Python scenario expected results**

In `test/scenarios/job-watch-actually-monty-python-injection.md`, replace the "Current Expected" and "Desired Future Expected" sections with:

```md
## Expected

- Turn 1's `job_watch` result has `watching: true`, a `watch_id`,
  target `"caller"`, events `["communicate"]`, and `send.to` equal to
  the observer `delegate_id`.
- The observer first turn completes with `PYTHON_READY`, so each later
  watched `communicate` event starts or steers that durable observer
  conversation.
- Each delivered frame visible in the observer transcript has
  `watch_id:`, `delivery_id:`, `trigger: event: COMMUNICATE`, and an
  `event:` block with `kind: communicate`, `message: ...`,
  `await_reply: false`, and `truncated: false`.
- The observer transcript shows `PYTHON_INJECTED` for the two trigger
  turns and `PYTHON_IGNORED` for the plain turn, each with the frame's
  `delivery_id`.
- The parent transcript receives exactly two caller-steering entries
  containing `PYTHON_QUOTE delivery=<delivery_id> quote=Ni!`, and none
  for the plain turn or setup chatter.
- The injected caller steering entries and any parent acknowledgement
  turns do not create extra observer jobs for the same watch. The
  parent's `jobs.jsonl` has no additional `watch_send_pending` entries
  caused by `PYTHON_QUOTE` or acknowledgement traffic.
- A later external human message containing `Actually` triggers a second
  legitimate observer delivery, proving top-level external input resets
  active provenance to empty.
```

Update the title line to:

```md
# job-watch-actually-monty-python-injection: content-bearing observer frames suppress self-loops
```

Update the first paragraph to describe the fixed contract:

```md
**What this covers**: an observer that injects `Ni!` whenever the
caller says the whole word `actually`, and the causal-provenance safety
rule that prevents observer-injected steering or notification
acknowledgements from retriggering the same watch.
```

- [ ] **Step 2: Update snide observer scenario**

In `test/scenarios/job-watch-observer-snide-thread.md`, ensure the expected section contains these bullets:

```md
- The observer transcript contains at least two later `SNIDE_NOTE`
  messages in response to watched caller activity.
- The observer does not inject into the caller: its transcript has no
  `delegate_send` tool call.
- Watch frames include `watch_id:`, `delivery_id:`, and the triggering
  event metadata needed for the observer to understand what it is
  commenting on.
- Observer lifecycle and notification traffic does not recursively
  trigger the same caller watch.
```

- [ ] **Step 3: Update scenario index**

In `test/scenarios/INDEX.md`, update the observer scenario entries so they read:

```md
- `job-watch-actually-monty-python-injection.md` - caller `communicate`
  watch frames include content, an observer injects `PYTHON_QUOTE` only
  for external `actually` messages, and causal provenance suppresses
  injection and acknowledgement loops.
- `job-watch-observer-snide-thread.md` - observer commentary stays in
  the observer transcript while watch frames carry enough metadata for
  useful sidecar work.
```

- [ ] **Step 4: Run scenario documentation tests**

Run:

```bash
go test ./test/scenarios -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit scenario updates**

Run:

```bash
git status --short
git add test/scenarios/job-watch-actually-monty-python-injection.md test/scenarios/job-watch-observer-snide-thread.md test/scenarios/INDEX.md
git commit -m "docs(observer): update observer loop scenarios" -m "Turn the Monty Python observer card from a current-failure note into the passing contract for content-bearing frames and provenance suppression. Clarify the snide observer scenario expectations around non-injection and loop safety."
```

Expected: commit succeeds.

---

### Task 11: Full Test Sweep and Live Kimi E2E

**Files:**
- No source files unless this task exposes a bug; fix any bug in the smallest relevant file and add a regression test before committing.

- [ ] **Step 1: Run focused package tests**

Run:

```bash
go test ./agent/provenance ./agent/events ./agent/internal/jobstore ./agent -count=1
```

Expected: PASS.

- [ ] **Step 2: Run the full Go test suite**

Run:

```bash
go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Build the CLI and hub binaries**

Run:

```bash
go build ./cmd/serf ./cmd/serf-hub
```

Expected: PASS and binaries are produced in the working directory if the Go tool writes them there.

- [ ] **Step 4: Run live Monty Python scenario with Kimi**

Use the existing live scenario harness or the repo's documented manual e2e flow in `docs/agentic-testing.md`. The required model is:

```text
kimi/kimi-for-coding
```

Run the scenario from:

```text
test/scenarios/job-watch-actually-monty-python-injection.md
```

Use a fresh state directory and working directory. Capture the parent session id, observer `delegate_id`, observer `transcript_ref`, parent transcript path, observer transcript path, and parent `jobs.jsonl` path.

Expected live result:

```text
two external trigger messages produce exactly two PYTHON_QUOTE steering entries
plain alpha marker produces zero PYTHON_QUOTE steering entries
observer-injected PYTHON_QUOTE traffic produces zero additional observer jobs for the same watch
parent acknowledgement traffic produces zero additional observer jobs for the same watch
observer transcript frames include the communicated message text
```

- [ ] **Step 5: Read transcripts and durable log after the live run**

Inspect the parent transcript:

```bash
rg -n 'PYTHON_QUOTE|actually alpha marker|plain alpha marker|Actually beta marker|acknowledged' <parent-transcript-path>
```

Expected:

```text
exactly two lines or transcript entries containing PYTHON_QUOTE
one external actually alpha marker input
one external Actually beta marker input
one plain alpha marker input
no PYTHON_QUOTE caused by plain alpha marker
```

Inspect the observer transcript:

```bash
rg -n 'Watch frame|message: actually alpha marker|message: plain alpha marker|message: Actually beta marker|PYTHON_INJECTED|PYTHON_IGNORED' <observer-transcript-path>
```

Expected:

```text
frames contain message: actually alpha marker, message: plain alpha marker, and message: Actually beta marker
PYTHON_INJECTED appears for the two trigger frames
PYTHON_IGNORED appears for the plain frame
```

Inspect jobs:

```bash
rg -n 'watch_send_pending|watch_send_delivered|watch_send_dropped|provenance|watch_A|wg_' <jobs-jsonl-path>
```

Expected:

```text
watch_send_pending records include provenance.watch_keys
no extra watch_send_pending records correspond to PYTHON_QUOTE or acknowledgement traffic
no unexpected watch_send_dropped records are needed for loop suppression
```

- [ ] **Step 6: Run live snide observer scenario with Kimi**

Run:

```text
test/scenarios/job-watch-observer-snide-thread.md
```

Use `kimi/kimi-for-coding`.

Expected:

```text
observer transcript contains SNIDE_NOTE commentary
parent transcript contains no injected delegate_send output from the observer
same-watch observer lifecycle and notification traffic does not create recursive observer jobs
```

- [ ] **Step 7: Commit any test-only scenario evidence updates**

If the scenario docs need exact live-run notes, update only the relevant scenario markdown. Then run:

```bash
git status --short
git add test/scenarios/job-watch-actually-monty-python-injection.md test/scenarios/job-watch-observer-snide-thread.md
git commit -m "docs(observer): record live Kimi observer scenario results" -m "Record the live Kimi verification for the Monty Python and snide observer scenarios, including transcript inspection criteria for loop suppression and content-bearing frames."
```

Expected: commit succeeds if docs changed. If no docs changed, skip the commit and record the live test output in the final handoff.

---

## Definition of Done

- `events.SessionEvent` has event-level causal provenance; individual event payloads do not grow bespoke provenance fields.
- The load-bearing structure is a deduped set of `(watch_id, watch_generation)` keys. Diagnostic chain truncation cannot remove load-bearing watch keys.
- Each new top-level external user input replaces active provenance with empty. Mid-turn steering and notification turns union provenance only within the current driven turn.
- `delegate_send(to="caller")` from a watch-delivered observer stores provenance on caller steering, and the caller's downstream events inherit it.
- Observer terminal notifications carry provenance in memory and durably, and notification acknowledgement turns inherit it.
- Watch-send pending records persist provenance and restored deliveries re-use that provenance.
- `job_watch` suppresses same-watch echoes before notification enqueue, watch-send recording, pending-frame replacement, and delivery counters.
- The old `FromWatch` watch guard is not used for suppression. Cross-watch lifecycle observation remains possible unless the active watch's own key is present.
- Communicate watch frames include `watch_id`, `delivery_id`, trigger metadata, provenance summary, and bounded `event:` fields: `kind`, `message`, `await_reply`, and `truncated`.
- Unit tests cover same-watch suppression, generation mismatch, watch-send persistence, delegate caller steering, mid-turn steering union, fresh external reset, notification acknowledgement provenance, and detached job lifecycle provenance.
- The Monty Python scenario passes live with `kimi/kimi-for-coding` after transcript inspection: exactly two external trigger inputs create exactly two `PYTHON_QUOTE` injections, and injected or acknowledgement traffic creates no extra observer jobs.
- The snide observer scenario passes live with `kimi/kimi-for-coding`: commentary stays in the observer thread and does not inject into the caller.

## Self-Review Notes

- Spec coverage: the plan covers provenance set semantics, active-provenance reset, mid-turn union, notification acknowledgement, watch-send restore, delegate caller route, `FromWatch` replacement, generation matching, content-bearing frames, and live Kimi transcript inspection.
- Placeholder scan: no step uses forbidden placeholder language. Each task has concrete test code, implementation snippets, commands, and expected results.
- Type consistency: the plan consistently uses `provenance.Causal`, `provenance.WatchKey`, `SessionEvent.Provenance`, `WatchSendState.Provenance`, and `watch_generation` as distinct from delegate generation.
