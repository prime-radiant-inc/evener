# Job-Control Handle Split Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the handle split from `docs/superpowers/specs/2026-06-18-job-control-handle-split-design.md`: message durable delegates, read/stop/watch concrete jobs, and clear watches by watch id.

**Architecture:** Add durable delegate and watch projections to the existing jobstore event log, then route session tools through those projections instead of overloading delegate job records. Keep observer sidecars as `delegate` + `job_watch` + `delegate_send`; no observer-specific primitive is added.

**Tech Stack:** Go, Serf `jobstore` JSONL events, Serf session tool registry, existing Go test suites, existing TUI/web JavaScript renderers.

---

## Scope Check

This is one coupled plan because the model-facing API cutover depends on three shared identities being introduced together: `delegate_id`, `job_id`, and `watch_id`. Splitting the store work from the tool cutover would leave a half-valid public API that still lets models chase job ids as actors.

## File Structure

- Modify `agent/internal/jobstore/record.go`: add `DelegateRecord`, `WatchRecord`, `WatchConfigSnapshot`, `NewDelegateID`, `NewDelegateGeneration`, and `NewWatchID`.
- Modify `agent/internal/jobstore/event.go`: add durable event kinds and payload fields for delegates, watch registry rows, and delegate generations.
- Modify `agent/internal/jobstore/fold.go`: add `FoldDelegates`, `FoldWatches`, and keep existing job folds isolated from non-job events.
- Modify `agent/internal/jobstore/store.go`: add `LoadDelegates` and `LoadWatches`.
- Test `agent/internal/jobstore/fold_test.go`, `agent/internal/jobstore/store_test.go`, and `agent/internal/jobstore/event_test.go`: pin new event serialization and folds.
- Modify `agent/job_delegate.go`: mint `delegate_id`, link every delegate job turn, resolve `delegate_send` by `delegate_id`, and make idle starts explicit.
- Modify `agent/session_tools_jobs.go`: register `delegate_send`, marshal new result shapes, project `delegates[]` in `job_list`, and reject handle-type mismatches.
- Modify `agent/job_watch.go`: replace tuple-clear public behavior with a durable watch registry, accept `send.to=dlg_...`, and drop `send.to=job_...` plus `target="*"`.
- Modify `agent/jobs.go` and `agent/jobs_nested.go`: carry `DelegateID` on delegate job start/forward events and close the stop gate when stopping a delegate's current job.
- Modify `agent/internal/tool/definitions.go`: rename model-facing `job_send_message` definition to `delegate_send`, add `job_watch.operation`, and update descriptions.
- Modify `agent/provider/profile.go`, `agent/profile_test.go`, `agent/provider/profile_test.go`, `agent/session_tools.go`, `agent/session_outline.go`, and `agent/subagents_test.go`: replace model-facing tool names and root-only expectations.
- Modify `agent/prompts/sections/background-jobs.md`, `agent/prompts/sections/delegation.md`, `docs/job-control.md`, `docs/tools/transcripts.md`, `test/scenarios/*.md`, and `agent/bundled_plugins/coordinator-workflow/agents/coordinator.md`: update public guidance.
- Modify `agent/transcript_render.go`, `agent/transcript_render_test.go`, `agent/transcript_render_job_test.go`, `cmd/serf-tui/internal/msgrender/tool_renderers.go`, `cmd/serf-tui/internal/msgrender/tool_renderers_test.go`, `cmd/serf-hub/assets/renderer-tools.js`, and `cmd/serf-hub/jstest/test-tool-renderers.js`: add `delegate_send` rendering while preserving historical `job_send_message` transcript rendering.

---

### Task 1: Durable Delegate Projection

**Files:**
- Modify: `agent/internal/jobstore/record.go`
- Modify: `agent/internal/jobstore/event.go`
- Modify: `agent/internal/jobstore/fold.go`
- Modify: `agent/internal/jobstore/store.go`
- Test: `agent/internal/jobstore/fold_test.go`
- Test: `agent/internal/jobstore/store_test.go`
- Test: `agent/internal/jobstore/event_test.go`

- [ ] **Step 1: Write failing delegate fold tests**

Add these tests to `agent/internal/jobstore/fold_test.go`:

```go
func TestFoldDelegatesLinksJobsAndProjectsCurrentLatest(t *testing.T) {
	start1 := time.Unix(1, 0).UTC()
	end1 := time.Unix(2, 0).UTC()
	start2 := time.Unix(3, 0).UTC()
	events := []Event{
		ev(EventDelegateCreated, 1, "", func(e *Event) {
			e.DelegateID = "dlg_A"
			e.Delegate = &DelegateEvent{
				ChildSessionID:   "child_A",
				TranscriptRef:    "local:child_A",
				OwnerSessionID:   "owner",
				VisibleSessionID: "owner",
				AgentType:        "default",
				Generation:       "dg_1",
				Resumable:        true,
			}
		}),
		ev(EventJobStarted, 2, "job_1", func(e *Event) {
			e.Type = JobDelegate
			e.DelegateID = "dlg_A"
			e.OwnerSessionID = "owner"
			e.VisibleToSession = "owner"
			e.TranscriptRef = "local:child_A"
			e.StartedAt = &start1
		}),
		ev(EventJobFinished, 3, "job_1", func(e *Event) {
			e.Status = StatusCompleted
			e.EndedAt = &end1
		}),
		ev(EventJobStarted, 4, "job_2", func(e *Event) {
			e.Type = JobDelegate
			e.DelegateID = "dlg_A"
			e.OwnerSessionID = "owner"
			e.VisibleToSession = "owner"
			e.TranscriptRef = "local:child_A"
			e.StartedAt = &start2
		}),
	}

	delegates := FoldDelegates(events)
	d := delegates["dlg_A"]
	if d == nil {
		t.Fatal("delegate dlg_A missing")
	}
	if d.CurrentJobID != "job_2" || d.LatestJobID != "job_2" || d.Status != DelegateRunning {
		t.Fatalf("delegate projection = %+v, want current/latest job_2 running", d)
	}
	if d.ChildSessionID != "child_A" || d.TranscriptRef != "local:child_A" || !d.Resumable {
		t.Fatalf("delegate identity = %+v, want durable child metadata", d)
	}
}

func TestFoldDelegatesClosesStopGateForCurrentJob(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	end := time.Unix(2, 0).UTC()
	events := []Event{
		ev(EventDelegateCreated, 1, "", func(e *Event) {
			e.DelegateID = "dlg_A"
			e.Delegate = &DelegateEvent{ChildSessionID: "child_A", TranscriptRef: "local:child_A", Generation: "dg_1", Resumable: true}
		}),
		ev(EventJobStarted, 2, "job_1", func(e *Event) {
			e.Type = JobDelegate
			e.DelegateID = "dlg_A"
			e.StartedAt = &start
		}),
		ev(EventJobFinished, 3, "job_1", func(e *Event) {
			e.Status = StatusCancelled
			e.Reason = "stopped_by_parent"
			e.EndedAt = &end
		}),
		ev(EventDelegateStopGateClosed, 4, "", func(e *Event) {
			e.DelegateID = "dlg_A"
			e.Delegate = &DelegateEvent{Generation: "dg_2", StopJobID: "job_1"}
		}),
	}

	d := FoldDelegates(events)["dlg_A"]
	if d == nil {
		t.Fatal("delegate dlg_A missing")
	}
	if !d.StopGateClosed || d.Generation != "dg_2" || d.CurrentJobID != "" || d.LatestJobID != "job_1" {
		t.Fatalf("delegate after stop = %+v, want closed gate with latest job_1 and no current job", d)
	}
}
```

- [ ] **Step 2: Run delegate fold tests to verify they fail**

Run:

```bash
go test ./agent/internal/jobstore -run 'TestFoldDelegates' -count=1
```

Expected: build fails because `EventDelegateCreated`, `DelegateEvent`, `FoldDelegates`, and `DelegateRunning` do not exist.

- [ ] **Step 3: Add delegate event and record types**

In `agent/internal/jobstore/record.go`, add:

```go
type DelegateStatus string

const (
	DelegateRunning      DelegateStatus = "running"
	DelegateDriving      DelegateStatus = "driving"
	DelegateIdle         DelegateStatus = "idle"
	DelegateStopped      DelegateStatus = "stopped"
	DelegateNotResumable DelegateStatus = "not_resumable"
)

type DelegateRecord struct {
	DelegateID          string         `json:"delegate_id"`
	ChildSessionID      string         `json:"child_session_id"`
	TranscriptRef       string         `json:"transcript_ref"`
	OwnerSessionID      string         `json:"owner_session_id,omitempty"`
	VisibleSessionID    string         `json:"visible_session_id,omitempty"`
	ParentDelegateID    string         `json:"parent_delegate_id,omitempty"`
	AgentType           string         `json:"agent_type,omitempty"`
	Status              DelegateStatus `json:"status"`
	Resumable           bool           `json:"resumable"`
	NotResumableWhy     string         `json:"not_resumable_reason,omitempty"`
	CurrentJobID        string         `json:"current_job_id,omitempty"`
	LatestJobID         string         `json:"latest_job_id,omitempty"`
	Generation          string         `json:"generation,omitempty"`
	StopGateClosed      bool           `json:"stop_gate_closed,omitempty"`
	StopGateClosedJobID string         `json:"stop_gate_closed_job_id,omitempty"`
}

func NewDelegateID() string {
	return "dlg_" + ulid.Make().String()
}

func NewDelegateGeneration() string {
	return "dg_" + ulid.Make().String()
}
```

In `agent/internal/jobstore/event.go`, add these `EventKind` values:

```go
EventDelegateCreated        EventKind = "delegate_created"
EventDelegateStopGateClosed EventKind = "delegate_stop_gate_closed"
```

Then add these fields to `Event`:

```go
DelegateID string         `json:"delegate_id,omitempty"`
Delegate   *DelegateEvent `json:"delegate,omitempty"`
```

Add the payload type below `Event`:

```go
type DelegateEvent struct {
	ChildSessionID   string `json:"child_session_id,omitempty"`
	TranscriptRef    string `json:"transcript_ref,omitempty"`
	OwnerSessionID   string `json:"owner_session_id,omitempty"`
	VisibleSessionID string `json:"visible_session_id,omitempty"`
	ParentDelegateID string `json:"parent_delegate_id,omitempty"`
	AgentType        string `json:"agent_type,omitempty"`
	Generation       string `json:"generation,omitempty"`
	Resumable        bool   `json:"resumable,omitempty"`
	NotResumableWhy  string `json:"not_resumable_reason,omitempty"`
	StopJobID        string `json:"stop_job_id,omitempty"`
}
```

Add `DelegateID string `json:"delegate_id,omitempty"` to `JobRecord` in `agent/internal/jobstore/record.go`.

- [ ] **Step 4: Add the delegate fold and store loader**

In `agent/internal/jobstore/fold.go`, add:

```go
func FoldDelegates(events []Event) map[string]*DelegateRecord {
	sorted := append([]Event(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })

	delegates := make(map[string]*DelegateRecord)
	jobToDelegate := make(map[string]string)
	for _, e := range sorted {
		switch e.Kind {
		case EventDelegateCreated:
			if e.DelegateID == "" || e.Delegate == nil {
				continue
			}
			d := delegates[e.DelegateID]
			if d == nil {
				d = &DelegateRecord{DelegateID: e.DelegateID, Status: DelegateIdle}
				delegates[e.DelegateID] = d
			}
			d.ChildSessionID = e.Delegate.ChildSessionID
			d.TranscriptRef = e.Delegate.TranscriptRef
			d.OwnerSessionID = e.Delegate.OwnerSessionID
			d.VisibleSessionID = e.Delegate.VisibleSessionID
			d.ParentDelegateID = e.Delegate.ParentDelegateID
			d.AgentType = e.Delegate.AgentType
			d.Generation = e.Delegate.Generation
			d.Resumable = e.Delegate.Resumable
			d.NotResumableWhy = e.Delegate.NotResumableWhy
		case EventJobStarted:
			if e.DelegateID == "" {
				continue
			}
			d := delegates[e.DelegateID]
			if d == nil {
				d = &DelegateRecord{DelegateID: e.DelegateID}
				delegates[e.DelegateID] = d
			}
			jobToDelegate[e.JobID] = e.DelegateID
			d.CurrentJobID = e.JobID
			d.LatestJobID = e.JobID
			d.Status = DelegateRunning
			d.StopGateClosed = false
		case EventJobFinished:
			delegateID := jobToDelegate[e.JobID]
			if delegateID == "" {
				continue
			}
			d := delegates[delegateID]
			if d == nil {
				continue
			}
			if d.CurrentJobID == e.JobID {
				d.CurrentJobID = ""
			}
			if e.Status == StatusStopped || e.Status == StatusCancelled {
				d.Status = DelegateStopped
			} else if d.Resumable {
				d.Status = DelegateIdle
			} else {
				d.Status = DelegateNotResumable
			}
		case EventDelegateStopGateClosed:
			if e.DelegateID == "" || e.Delegate == nil {
				continue
			}
			d := delegates[e.DelegateID]
			if d == nil {
				d = &DelegateRecord{DelegateID: e.DelegateID}
				delegates[e.DelegateID] = d
			}
			d.Generation = e.Delegate.Generation
			d.StopGateClosed = true
			d.StopGateClosedJobID = e.Delegate.StopJobID
			if d.CurrentJobID == e.Delegate.StopJobID {
				d.CurrentJobID = ""
			}
			if d.Status == "" || d.Status == DelegateRunning {
				d.Status = DelegateStopped
			}
		}
	}
	return delegates
}
```

In `applyEvent`, fold `DelegateID` from `EventJobStarted`:

```go
r.DelegateID = e.DelegateID
```

In `agent/internal/jobstore/store.go`, add:

```go
func (s *Store) LoadDelegates() (map[string]*DelegateRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return nil, err
	}
	events, err := s.readAllLocked()
	if err != nil {
		return nil, err
	}
	return FoldDelegates(events), nil
}
```

- [ ] **Step 5: Run delegate fold/store tests**

Run:

```bash
go test ./agent/internal/jobstore -run 'TestFoldDelegates|TestStore' -count=1
```

Expected: pass.

- [ ] **Step 6: Add event serialization coverage**

In `agent/internal/jobstore/event_test.go`, update `TestEventKindStrings` to include:

```go
EventDelegateCreated:        "delegate_created",
EventDelegateStopGateClosed: "delegate_stop_gate_closed",
```

Add:

```go
func TestDelegateEventJSONRoundTrip(t *testing.T) {
	raw := []byte(`{"kind":"delegate_created","delegate_id":"dlg_A","delegate":{"child_session_id":"child_A","transcript_ref":"local:child_A","generation":"dg_1","resumable":true}}`)
	var e Event
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("unmarshal delegate event: %v", err)
	}
	if e.Kind != EventDelegateCreated || e.DelegateID != "dlg_A" || e.Delegate == nil || e.Delegate.ChildSessionID != "child_A" {
		t.Fatalf("event = %+v, want delegate-created payload", e)
	}
	out, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal delegate event: %v", err)
	}
	if !bytes.Contains(out, []byte(`"delegate_id":"dlg_A"`)) || !bytes.Contains(out, []byte(`"child_session_id":"child_A"`)) {
		t.Fatalf("encoded delegate event = %s", out)
	}
}
```

Ensure `encoding/json` and `bytes` are imported by that test file.

- [ ] **Step 7: Run the jobstore package**

Run:

```bash
go test ./agent/internal/jobstore -count=1
```

Expected: pass.

- [ ] **Step 8: Commit**

Run:

```bash
git add agent/internal/jobstore/record.go agent/internal/jobstore/event.go agent/internal/jobstore/fold.go agent/internal/jobstore/store.go agent/internal/jobstore/fold_test.go agent/internal/jobstore/store_test.go agent/internal/jobstore/event_test.go
git commit -m "feat(job-control): add durable delegate projection"
```

---

### Task 2: Durable Watch Registry Projection

**Files:**
- Modify: `agent/internal/jobstore/record.go`
- Modify: `agent/internal/jobstore/event.go`
- Modify: `agent/internal/jobstore/fold.go`
- Modify: `agent/internal/jobstore/store.go`
- Test: `agent/internal/jobstore/fold_test.go`
- Test: `agent/internal/jobstore/store_test.go`
- Test: `agent/internal/jobstore/event_test.go`

- [ ] **Step 1: Write failing watch registry fold tests**

Add to `agent/internal/jobstore/fold_test.go`:

```go
func TestFoldWatchesUpsertsByConfigHashAndClearsByID(t *testing.T) {
	events := []Event{
		ev(EventWatchRegistered, 1, "", func(e *Event) {
			e.WatchID = "watch_A"
			e.Watch = &WatchEvent{
				Generation:       "wg_1",
				OwnerSessionID:   "owner",
				VisibleSessionID: "owner",
				Target:           "job_1",
				SendTo:           "dlg_obs",
				ConfigHash:       "hash_A",
				Condition:        "events: [assistant.message]",
			}
		}),
		ev(EventWatchRegistered, 2, "", func(e *Event) {
			e.WatchID = "watch_A"
			e.Watch = &WatchEvent{
				Generation:       "wg_1",
				OwnerSessionID:   "owner",
				VisibleSessionID: "owner",
				Target:           "job_1",
				SendTo:           "dlg_obs",
				ConfigHash:       "hash_A",
				Condition:        "events: [assistant.message]",
			}
		}),
		ev(EventWatchCleared, 3, "", func(e *Event) {
			e.WatchID = "watch_A"
			e.Watch = &WatchEvent{Generation: "wg_1", EndReason: "cleared"}
		}),
	}

	watches := FoldWatches(events)
	w := watches["watch_A"]
	if w == nil {
		t.Fatal("watch_A missing")
	}
	if w.Active || w.EndReason != "cleared" || w.Target != "job_1" || w.SendTo != "dlg_obs" {
		t.Fatalf("watch = %+v, want inactive cleared registry row", w)
	}
}

func TestFoldWatchesRejectsStaleClearGeneration(t *testing.T) {
	events := []Event{
		ev(EventWatchRegistered, 1, "", func(e *Event) {
			e.WatchID = "watch_A"
			e.Watch = &WatchEvent{Generation: "wg_2", OwnerSessionID: "owner", VisibleSessionID: "owner", Target: "job_1", ConfigHash: "hash_2"}
		}),
		ev(EventWatchCleared, 2, "", func(e *Event) {
			e.WatchID = "watch_A"
			e.Watch = &WatchEvent{Generation: "wg_1", EndReason: "cleared"}
		}),
	}

	w := FoldWatches(events)["watch_A"]
	if w == nil || !w.Active || w.Generation != "wg_2" {
		t.Fatalf("watch = %+v, want stale clear ignored", w)
	}
}
```

- [ ] **Step 2: Run watch registry fold tests to verify they fail**

Run:

```bash
go test ./agent/internal/jobstore -run 'TestFoldWatches' -count=1
```

Expected: build fails because the watch registry event types and `FoldWatches` do not exist.

- [ ] **Step 3: Add watch registry types and IDs**

In `agent/internal/jobstore/record.go`, add:

```go
type WatchRecord struct {
	WatchID          string `json:"watch_id"`
	Generation       string `json:"generation"`
	OwnerSessionID   string `json:"owner_session_id"`
	VisibleSessionID string `json:"visible_session_id"`
	Target           string `json:"target"`
	SendTo           string `json:"send_to,omitempty"`
	ConfigHash       string `json:"config_hash"`
	Condition        string `json:"condition,omitempty"`
	Deliveries       int    `json:"deliveries,omitempty"`
	Active           bool   `json:"active"`
	EndReason        string `json:"end_reason,omitempty"`
}

type WatchConfigSnapshot struct {
	Target             string   `json:"target"`
	OutputMatch        string   `json:"output_match,omitempty"`
	ProgressIntervalMS int      `json:"progress_interval_ms,omitempty"`
	Events             []string `json:"events,omitempty"`
	Every              int      `json:"every,omitempty"`
	SendTo             string   `json:"send_to,omitempty"`
	SendMessage        string   `json:"send_message,omitempty"`
	IncludeExcerpt     bool     `json:"include_excerpt,omitempty"`
}

func NewWatchID() string {
	return "watch_" + ulid.Make().String()
}
```

In `agent/internal/jobstore/event.go`, add:

```go
EventWatchRegistered EventKind = "watch_registered"
EventWatchCleared    EventKind = "watch_cleared"
```

Add these fields to `Event`:

```go
WatchID string      `json:"watch_id,omitempty"`
Watch   *WatchEvent `json:"watch,omitempty"`
```

Add:

```go
type WatchEvent struct {
	Generation       string               `json:"generation,omitempty"`
	OwnerSessionID   string               `json:"owner_session_id,omitempty"`
	VisibleSessionID string               `json:"visible_session_id,omitempty"`
	Target           string               `json:"target,omitempty"`
	SendTo           string               `json:"send_to,omitempty"`
	ConfigHash       string               `json:"config_hash,omitempty"`
	Condition        string               `json:"condition,omitempty"`
	Config           *WatchConfigSnapshot `json:"config,omitempty"`
	Deliveries       int                  `json:"deliveries,omitempty"`
	EndReason        string               `json:"end_reason,omitempty"`
}
```

- [ ] **Step 4: Add `FoldWatches` and `LoadWatches`**

In `agent/internal/jobstore/fold.go`, add:

```go
func FoldWatches(events []Event) map[string]*WatchRecord {
	sorted := append([]Event(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })

	watches := make(map[string]*WatchRecord)
	for _, e := range sorted {
		if e.WatchID == "" || e.Watch == nil {
			continue
		}
		switch e.Kind {
		case EventWatchRegistered:
			watches[e.WatchID] = &WatchRecord{
				WatchID:          e.WatchID,
				Generation:       e.Watch.Generation,
				OwnerSessionID:   e.Watch.OwnerSessionID,
				VisibleSessionID: e.Watch.VisibleSessionID,
				Target:           e.Watch.Target,
				SendTo:           e.Watch.SendTo,
				ConfigHash:       e.Watch.ConfigHash,
				Condition:        e.Watch.Condition,
				Deliveries:       e.Watch.Deliveries,
				Active:           true,
			}
		case EventWatchCleared:
			w := watches[e.WatchID]
			if w == nil || w.Generation != e.Watch.Generation {
				continue
			}
			w.Active = false
			w.EndReason = e.Watch.EndReason
		}
	}
	return watches
}
```

In `agent/internal/jobstore/store.go`, add:

```go
func (s *Store) LoadWatches() (map[string]*WatchRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return nil, err
	}
	events, err := s.readAllLocked()
	if err != nil {
		return nil, err
	}
	return FoldWatches(events), nil
}
```

- [ ] **Step 5: Run watch registry tests**

Run:

```bash
go test ./agent/internal/jobstore -run 'TestFoldWatches|TestStore' -count=1
```

Expected: pass.

- [ ] **Step 6: Add watch event serialization coverage**

In `agent/internal/jobstore/event_test.go`, extend `TestEventKindStrings`:

```go
EventWatchRegistered: "watch_registered",
EventWatchCleared:    "watch_cleared",
```

Add:

```go
func TestWatchRegistryEventJSONRoundTrip(t *testing.T) {
	raw := []byte(`{"kind":"watch_registered","watch_id":"watch_A","watch":{"generation":"wg_1","owner_session_id":"owner","visible_session_id":"owner","target":"job_1","send_to":"dlg_obs","config_hash":"hash_A"}}`)
	var e Event
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("unmarshal watch event: %v", err)
	}
	if e.Kind != EventWatchRegistered || e.WatchID != "watch_A" || e.Watch == nil || e.Watch.SendTo != "dlg_obs" {
		t.Fatalf("event = %+v, want watch registry payload", e)
	}
	out, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal watch event: %v", err)
	}
	if !bytes.Contains(out, []byte(`"watch_id":"watch_A"`)) || !bytes.Contains(out, []byte(`"send_to":"dlg_obs"`)) {
		t.Fatalf("encoded watch event = %s", out)
	}
}
```

- [ ] **Step 7: Run the jobstore package**

Run:

```bash
go test ./agent/internal/jobstore -count=1
```

Expected: pass.

- [ ] **Step 8: Commit**

Run:

```bash
git add agent/internal/jobstore/record.go agent/internal/jobstore/event.go agent/internal/jobstore/fold.go agent/internal/jobstore/store.go agent/internal/jobstore/fold_test.go agent/internal/jobstore/store_test.go agent/internal/jobstore/event_test.go
git commit -m "feat(job-control): add durable watch registry projection"
```

---

### Task 3: Delegate Creation, Resume, And `job_list` Recovery

**Files:**
- Modify: `agent/job_delegate.go`
- Modify: `agent/session_tools_jobs.go`
- Modify: `agent/jobs.go`
- Modify: `agent/jobs_nested.go`
- Test: `agent/job_delegate_test.go`
- Test: `agent/session_tools_jobs_test.go`
- Test: `agent/job_nested_test.go`

- [ ] **Step 1: Write failing creation/result tests**

Add to `agent/job_delegate_test.go`:

```go
func TestDelegateResultIncludesDurableDelegateAndStartedJobIDs(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("done") },
	}})
	s := newDelegateTestSession(t, c)

	res := s.createDelegate(context.Background(), delegateArgs{
		Task:           "finish",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	if !strings.HasPrefix(res.DelegateID, "dlg_") {
		t.Fatalf("DelegateID = %q, want dlg_ prefix", res.DelegateID)
	}
	if res.StartedJobID != res.JobID || res.LatestJobID != res.JobID {
		t.Fatalf("result ids = %+v, want started/latest equal concrete job", res)
	}
	rec := loadShellRecord(t, s.jobManager, res.JobID)
	if rec.DelegateID != res.DelegateID {
		t.Fatalf("record DelegateID = %q, want %q", rec.DelegateID, res.DelegateID)
	}
	delegates, err := s.jobManager.store.LoadDelegates()
	if err != nil {
		t.Fatalf("LoadDelegates: %v", err)
	}
	if delegates[res.DelegateID] == nil || delegates[res.DelegateID].LatestJobID != res.JobID {
		t.Fatalf("delegates = %+v, want latest job linked", delegates)
	}
}
```

Add to `agent/session_tools_jobs_test.go`:

```go
func TestJobListIncludesDelegatesRecoverySurface(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("done") },
	}})
	s := newDelegateTestSession(t, c)
	res := s.createDelegate(context.Background(), delegateArgs{Task: "finish", Background: false, BlockTimeoutMS: 5000})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}

	call := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "list",
		Name:      "job_list",
		Arguments: json.RawMessage(`{}`),
	})
	if call.IsError {
		t.Fatalf("job_list returned error: %s", call.Output)
	}
	var out struct {
		Delegates []struct {
			DelegateID   string `json:"delegate_id"`
			CurrentJobID string `json:"current_job_id"`
			LatestJobID  string `json:"latest_job_id"`
			Status       string `json:"status"`
			Resumable    bool   `json:"resumable"`
		} `json:"delegates"`
		Jobs []struct {
			JobID      string `json:"job_id"`
			DelegateID string `json:"delegate_id"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(toolResultJSON(call), &out); err != nil {
		t.Fatalf("unmarshal job_list: %v", err)
	}
	if len(out.Delegates) != 1 || out.Delegates[0].DelegateID != res.DelegateID || out.Delegates[0].LatestJobID != res.JobID {
		t.Fatalf("delegates = %+v, want delegate recovery row", out.Delegates)
	}
	if len(out.Jobs) == 0 || out.Jobs[0].DelegateID != res.DelegateID {
		t.Fatalf("jobs = %+v, want job annotated with delegate_id", out.Jobs)
	}
}
```

- [ ] **Step 2: Run creation/recovery tests to verify they fail**

Run:

```bash
go test ./agent -run 'TestDelegateResultIncludesDurableDelegateAndStartedJobIDs|TestJobListIncludesDelegatesRecoverySurface' -count=1
```

Expected: build fails because `delegateResult` has no `DelegateID`, `StartedJobID`, or `LatestJobID`, and `job_list` has no `delegates` field.

- [ ] **Step 3: Mint and persist `delegate_id` on delegate creation**

In `agent/job_delegate.go`, extend `delegateResult`:

```go
DelegateID   string
StartedJobID string
LatestJobID  string
```

In `createDelegate`, mint both IDs:

```go
delegateID := jobstore.NewDelegateID()
delegateGeneration := jobstore.NewDelegateGeneration()
jobID := jobstore.NewJobID()
ctx = context.WithValue(ctx, ctxParentJobID, jobID)
ctx = context.WithValue(ctx, ctxDelegateID, delegateID)
```

Add `ctxDelegateID` beside the existing job context keys. In `attachDelegateJobWithPrepared` and the resume helpers, read the value and stamp `DelegateID` on the `EventJobStarted`.

Before or with the first job start append, append:

```go
jobstore.Event{
	Kind:       jobstore.EventDelegateCreated,
	TS:         jm.now(),
	DelegateID: delegateID,
	Delegate: &jobstore.DelegateEvent{
		ChildSessionID:   childID,
		TranscriptRef:    encodeRef("", childID),
		OwnerSessionID:   s.id,
		VisibleSessionID: s.id,
		AgentType:        args.AgentType,
		Generation:       delegateGeneration,
		Resumable:        true,
	},
}
```

Populate result fields on every `delegateResult` return path:

```go
DelegateID:   delegateID,
StartedJobID: run.rec.JobID,
JobID:        run.rec.JobID,
LatestJobID:  run.rec.JobID,
```

- [ ] **Step 4: Project delegates in `job_list`**

In `agent/session_tools_jobs.go`, add:

```go
type delegateListEntry struct {
	DelegateID       string `json:"delegate_id"`
	Status           string `json:"status"`
	CurrentJobID     string `json:"current_job_id,omitempty"`
	LatestJobID      string `json:"latest_job_id,omitempty"`
	TranscriptRef    string `json:"transcript_ref,omitempty"`
	Resumable        bool   `json:"resumable"`
	NotResumableWhy  string `json:"not_resumable_reason,omitempty"`
	ParentDelegateID string `json:"parent_delegate_id,omitempty"`
}
```

Extend `jobListResult`:

```go
Delegates []delegateListEntry `json:"delegates,omitempty"`
```

Extend `jobListEntry`:

```go
DelegateID string `json:"delegate_id,omitempty"`
```

Add a `projectDelegateRecord` helper:

```go
func projectDelegateRecord(rec *jobstore.DelegateRecord) delegateListEntry {
	if rec == nil {
		return delegateListEntry{}
	}
	return delegateListEntry{
		DelegateID:       rec.DelegateID,
		Status:           string(rec.Status),
		CurrentJobID:     rec.CurrentJobID,
		LatestJobID:      rec.LatestJobID,
		TranscriptRef:    rec.TranscriptRef,
		Resumable:        rec.Resumable,
		NotResumableWhy:  rec.NotResumableWhy,
		ParentDelegateID: rec.ParentDelegateID,
	}
}
```

In `jobListTool`, load delegates from `jm.store.LoadDelegates()`, sort by `DelegateID`, and set `result.Delegates`.

In `projectJobRecord`, assign `DelegateID: rec.DelegateID`.

- [ ] **Step 5: Run creation/recovery tests**

Run:

```bash
go test ./agent -run 'TestDelegateResultIncludesDurableDelegateAndStartedJobIDs|TestJobListIncludesDelegatesRecoverySurface' -count=1
```

Expected: pass.

- [ ] **Step 6: Preserve delegate id on resumed turns**

Add to `agent/job_delegate_test.go`:

```go
func TestDelegateResumeKeepsDelegateIDAndUpdatesLatestJob(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("first") },
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("second") },
	}})
	s := newDelegateTestSession(t, c)
	first := s.createDelegate(context.Background(), delegateArgs{Task: "first", Background: false, BlockTimeoutMS: 5000})
	if first.Err != nil {
		t.Fatalf("first delegate: %v", first.Err)
	}
	res := s.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         first.DelegateID,
		Message:        "second",
		OnIdle:         "start",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage: %v", res.Err)
	}
	if res.DelegateID != first.DelegateID || res.StartedJobID == first.JobID || res.LatestJobID != res.StartedJobID {
		t.Fatalf("resume result = %+v, want same delegate and new latest job", res)
	}
	rec := loadShellRecord(t, s.jobManager, res.StartedJobID)
	if rec.DelegateID != first.DelegateID {
		t.Fatalf("resumed job DelegateID = %q, want %q", rec.DelegateID, first.DelegateID)
	}
}
```

- [ ] **Step 7: Implement delegate-id resume linkage**

Extend `sendMessageArgs` and `sendMessageResult`:

```go
OnIdle      string
DelegateID  string
StartedJobID string
LatestJobID  string
```

When resuming a delegate, resolve the `DelegateRecord` first, pass its `DelegateID` into `resumeOrFindRunningDelegate`, and stamp that id on the new job start event. Keep `JobID` in `sendMessageResult` during this task for internal compatibility, but set it equal to `StartedJobID` for started results.

- [ ] **Step 8: Run resume/linkage tests**

Run:

```bash
go test ./agent -run 'TestDelegateResultIncludesDurableDelegateAndStartedJobIDs|TestDelegateResumeKeepsDelegateIDAndUpdatesLatestJob|TestJobListIncludesDelegatesRecoverySurface' -count=1
```

Expected: pass.

- [ ] **Step 9: Commit**

Run:

```bash
git add agent/job_delegate.go agent/session_tools_jobs.go agent/jobs.go agent/jobs_nested.go agent/job_delegate_test.go agent/session_tools_jobs_test.go agent/job_nested_test.go
git commit -m "feat(job-control): expose durable delegate recovery handles"
```

---

### Task 4: `delegate_send` Tool Cutover

**Files:**
- Modify: `agent/internal/tool/definitions.go`
- Modify: `agent/session_tools_jobs.go`
- Modify: `agent/job_delegate.go`
- Modify: `agent/provider/profile.go`
- Modify: `agent/profile_test.go`
- Modify: `agent/provider/profile_test.go`
- Modify: `agent/session_tools_jobs_test.go`
- Modify: `agent/internal/tool/definitions_test.go`
- Modify: `agent/subagents_test.go`

- [ ] **Step 1: Write failing tool definition tests**

In `agent/internal/tool/definitions_test.go`, replace the `job_send_message` definition test with:

```go
func TestDefDelegateSendShape(t *testing.T) {
	def := DefDelegateSend()
	if def.Name != "delegate_send" {
		t.Fatalf("name = %q, want delegate_send", def.Name)
	}
	required(t, def, "delegate_send", []string{"to", "message"})
	props := def.Parameters.(map[string]any)["properties"].(map[string]any)
	if _, ok := props["target"]; ok {
		t.Fatalf("delegate_send still exposes target: %+v", props)
	}
	if _, ok := props["on_finished"]; ok {
		t.Fatalf("delegate_send still exposes on_finished: %+v", props)
	}
	if _, ok := props["on_idle"]; !ok {
		t.Fatalf("delegate_send missing on_idle: %+v", props)
	}
	text := def.Description
	for _, bad := range []string{"job_send_message", "watched", "main"} {
		if strings.Contains(text, bad) {
			t.Fatalf("delegate_send description contains %q: %s", bad, text)
		}
	}
}
```

- [ ] **Step 2: Run tool definition test to verify it fails**

Run:

```bash
go test ./agent/internal/tool -run TestDefDelegateSendShape -count=1
```

Expected: build fails because `DefDelegateSend` does not exist.

- [ ] **Step 3: Add `DefDelegateSend` and remove model-facing `DefJobSendMessage` registration**

In `agent/internal/tool/definitions.go`, replace `DefJobSendMessage` with:

```go
func DefDelegateSend() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name: "delegate_send",
		Description: "Send a message to a durable delegate by delegate_id, or from a delegate/watch-delivered context to the contextual caller route. `to` accepts `dlg_...` or `caller`; it rejects `job_...`, transcript refs, `main`, and `watched`. If the delegate is running or being driven, the message is steered and returns on delivery. If the delegate is idle, set `on_idle=\"start\"` to start the next job; the default `on_idle=\"fail\"` rejects idle delegates instead of starting work.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"to":      map[string]any{"type": "string", "description": "A delegate_id (`dlg_...`) or contextual `caller`."},
				"message": map[string]any{"type": "string"},
				"on_idle": map[string]any{
					"type":        "string",
					"enum":        []string{"start", "fail"},
					"description": "Default fail: reject idle delegates. start: start the next job in the durable delegate conversation.",
				},
				"max_wait_ms": map[string]any{"type": "integer", "description": "0 (default): deliver/start without waiting. >0: for a newly started job, wait inline up to this many ms; steers and caller-route sends return on delivery and report wait_ignored_reason."},
			},
			"required": []string{"to", "message"},
		},
	}
}
```

In `agent/session_tools_jobs.go`, register `tool.DefDelegateSend()` under `delegate_send` and call a renamed `delegateSendTool`.

- [ ] **Step 4: Decode `delegate_send` arguments and reject old handles**

In `agent/session_tools_jobs.go`, replace `jobSendMessageTool` with:

```go
func delegateSendTool(ctx context.Context, s *Session, args map[string]any, maxChars int) (any, error) {
	a := sendMessageArgs{
		Target:     stringArg(args, "to"),
		Message:    stringArg(args, "message"),
		OnIdle:     stringArg(args, "on_idle"),
		Background: true,
	}
	if a.OnIdle == "" {
		a.OnIdle = "fail"
	}
	if n, ok := shellIntArg(args, "max_wait_ms"); ok && n != 0 {
		if n < 0 {
			return "", errors.New("invalid_request: max_wait_ms must be non-negative")
		}
		a.Background = false
		a.BackgroundSet = true
		a.BlockTimeoutMS = n
	}
	res := s.sendDelegateMessage(ctx, a)
	if res.Err != nil {
		return "", res.Err
	}
	return marshalDelegateSendResult(res, maxChars)
}
```

Keep an unregistered `jobSendMessageTool` only if a historical transcript test directly calls it. Do not register `job_send_message` in the model-facing registry.

- [ ] **Step 5: Write failing behavior tests for handle mismatch and idle default**

Add to `agent/session_tools_jobs_test.go`:

```go
func TestDelegateSendRejectsJobIDTargetWithGuidance(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("done") },
	}})
	s := newDelegateTestSession(t, c)
	first := s.createDelegate(context.Background(), delegateArgs{Task: "first", Background: false, BlockTimeoutMS: 5000})
	if first.Err != nil {
		t.Fatalf("createDelegate: %v", first.Err)
	}
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send",
		Name:      "delegate_send",
		Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"again"}`, first.JobID)),
	})
	if !res.IsError {
		t.Fatalf("delegate_send succeeded, want job_id rejection: %s", res.Output)
	}
	if !strings.Contains(res.Output, "job_id is a job/turn handle") || !strings.Contains(res.Output, first.DelegateID) {
		t.Fatalf("delegate_send error = %q, want job_id guidance with delegate_id", res.Output)
	}
}

func TestDelegateSendIdleDefaultFailsAndOnIdleStartResumes(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("first") },
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("second") },
	}})
	s := newDelegateTestSession(t, c)
	first := s.createDelegate(context.Background(), delegateArgs{Task: "first", Background: false, BlockTimeoutMS: 5000})
	if first.Err != nil {
		t.Fatalf("createDelegate: %v", first.Err)
	}

	fail := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send-fail",
		Name:      "delegate_send",
		Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"second"}`, first.DelegateID)),
	})
	if !fail.IsError || !strings.Contains(fail.Output, "target_idle") {
		t.Fatalf("delegate_send idle default = error:%v output:%s, want target_idle", fail.IsError, fail.Output)
	}

	start := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send-start",
		Name:      "delegate_send",
		Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"second","on_idle":"start","max_wait_ms":5000}`, first.DelegateID)),
	})
	if start.IsError {
		t.Fatalf("delegate_send on_idle=start returned error: %s", start.Output)
	}
	var out struct {
		DelegateID   string `json:"delegate_id"`
		StartedJobID string `json:"started_job_id"`
		CurrentJobID string `json:"current_job_id"`
		LatestJobID  string `json:"latest_job_id"`
		Action       string `json:"action"`
		Output       string `json:"output"`
	}
	if err := json.Unmarshal(toolResultJSON(start), &out); err != nil {
		t.Fatalf("unmarshal delegate_send: %v", err)
	}
	if out.DelegateID != first.DelegateID || out.Action != "started" || out.StartedJobID == "" || !strings.Contains(out.Output, "second") {
		t.Fatalf("delegate_send result = %+v, want started second turn", out)
	}
}
```

- [ ] **Step 6: Implement delegate-id resolution and action names**

In `sendDelegateMessage`, handle targets by prefix before job lookup:

```go
if strings.HasPrefix(target, "job_") {
	delegateID := s.delegateIDForJobID(target)
	if delegateID != "" {
		return sendMessageFailed(target, fmt.Errorf("invalid_request: job_id is a job/turn handle; send messages to delegate_id %s", delegateID))
	}
	return sendMessageFailed(target, errors.New("invalid_request: job_id is a job/turn handle; send messages to delegate_id"))
}
if strings.HasPrefix(target, "local:") || strings.HasPrefix(target, "proj:") {
	return sendMessageFailed(target, errors.New("invalid_request: transcript_ref is an archival read handle, not a control target"))
}
if target == "main" || target == runtimeMessageAliasWatched {
	return sendMessageFailed(target, fmt.Errorf("invalid_request: %s is not a delegate_send target", target))
}
```

Resolve `dlg_...` through `LoadDelegates`, then find its active/latest job record internally. Use `OnIdle` instead of `OnFinished`:

```go
if strings.TrimSpace(args.OnIdle) == "" {
	args.OnIdle = "fail"
}
if delegateIsIdle && args.OnIdle == "fail" {
	return sendMessageFailed(target, fmt.Errorf("target_idle: delegate %q is idle; pass on_idle=\"start\" to start the next job", target))
}
```

Map action names at the source:

```go
Action: "steered"
Action: "started"
Action: "delivered"
```

For runtime `caller`, reject root/top-level calls unless the session has a parent steering route:

```go
if target == runtimeMessageAliasCaller && !s.hasCallerRoute() {
	return sendMessageFailed(target, errors.New("invalid_request: caller is available only from delegate or watch-delivered contexts"))
}
```

- [ ] **Step 7: Marshal new result shapes**

Replace `jobSendMessageDelegateResult` with:

```go
type delegateSendResult struct {
	DelegateID             string  `json:"delegate_id,omitempty"`
	StartedJobID           string  `json:"started_job_id,omitempty"`
	CurrentJobID           string  `json:"current_job_id,omitempty"`
	LatestJobID            string  `json:"latest_job_id,omitempty"`
	Type                   string  `json:"type,omitempty"`
	Status                 string  `json:"status,omitempty"`
	Reason                 *string `json:"reason,omitempty"`
	RunningInBackground    bool    `json:"running_in_background,omitempty"`
	TimedOut               bool    `json:"timed_out,omitempty"`
	Action                 string  `json:"action"`
	TranscriptRef          string  `json:"transcript_ref,omitempty"`
	Output                 *string `json:"output,omitempty"`
	Truncated              *bool   `json:"truncated,omitempty"`
	StructuredResult       any     `json:"structured_result,omitempty"`
	StructuredResultValid  *bool   `json:"structured_result_valid,omitempty"`
	StructuredResultReason string  `json:"structured_result_reason,omitempty"`
	WaitIgnoredReason      string  `json:"wait_ignored_reason,omitempty"`
}
```

Make the formatted footer use `delegate_id` and `started_job_id`:

```go
foot := []string{"message to " + out.DelegateID, out.Action}
if out.StartedJobID != "" {
	foot = append(foot, "job "+out.StartedJobID)
}
```

- [ ] **Step 8: Run delegate_send tests**

Run:

```bash
go test ./agent/internal/tool -run TestDefDelegateSendShape -count=1
go test ./agent -run 'TestDelegateSendRejectsJobIDTargetWithGuidance|TestDelegateSendIdleDefaultFailsAndOnIdleStartResumes|TestDelegateResumeKeepsDelegateIDAndUpdatesLatestJob' -count=1
```

Expected: pass.

- [ ] **Step 9: Update profile/tool catalog tests**

Replace model-facing `job_send_message` with `delegate_send` in:

- `agent/profile_test.go`
- `agent/provider/profile_test.go`
- `agent/session_tools_jobs_test.go`
- `agent/subagents_test.go`

Keep any test that explicitly renders historical transcripts on `job_send_message`.

Run:

```bash
go test ./agent ./agent/provider ./agent/internal/tool -run 'Test.*Tool|Test.*Profile|Test.*Subagent' -count=1
```

Expected: pass.

- [ ] **Step 10: Commit**

Run:

```bash
git add agent/internal/tool/definitions.go agent/internal/tool/definitions_test.go agent/session_tools_jobs.go agent/job_delegate.go agent/session_tools_jobs_test.go agent/profile_test.go agent/provider/profile.go agent/provider/profile_test.go agent/subagents_test.go
git commit -m "feat(job-control): replace job_send_message with delegate_send"
```

---

### Task 5: `job_watch.operation`, Watch IDs, And Clear-By-ID

**Files:**
- Modify: `agent/job_watch.go`
- Modify: `agent/session_tools_jobs.go`
- Modify: `agent/internal/tool/definitions.go`
- Test: `agent/job_watch_test.go`
- Test: `agent/session_tools_jobs_test.go`
- Test: `agent/internal/tool/definitions_test.go`

- [ ] **Step 1: Write failing schema and clear-by-id tests**

Add to `agent/internal/tool/definitions_test.go`:

```go
func TestDefJobWatchRequiresOperationAndWatchIDForClear(t *testing.T) {
	def := DefJobWatch([]string{"assistant.message"})
	props := def.Parameters.(map[string]any)["properties"].(map[string]any)
	if _, ok := props["operation"]; !ok {
		t.Fatalf("job_watch missing operation property: %+v", props)
	}
	if _, ok := props["watch_id"]; !ok {
		t.Fatalf("job_watch missing watch_id property: %+v", props)
	}
	if _, ok := props["clear"]; ok {
		t.Fatalf("job_watch still exposes clear boolean: %+v", props)
	}
	if strings.Contains(def.Description, "`*`") || strings.Contains(def.Description, "watched") {
		t.Fatalf("job_watch description still advertises removed aliases: %s", def.Description)
	}
}
```

Add to `agent/session_tools_jobs_test.go`:

```go
func TestJobWatchCreateReturnsIDAndClearUsesIDOnly(t *testing.T) {
	s := newTestSession(t)
	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
	})
	if shellRes.IsError {
		t.Fatalf("shell returned error: %s", shellRes.Output)
	}
	var shellOut struct{ JobID string `json:"job_id"` }
	if err := json.Unmarshal(toolResultJSON(shellRes), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	create := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"create","target":%q,"output_match":"ready"}`, shellOut.JobID)),
	})
	if create.IsError {
		t.Fatalf("job_watch create returned error: %s", create.Output)
	}
	var created struct {
		WatchID  string `json:"watch_id"`
		Watching bool   `json:"watching"`
	}
	if err := json.Unmarshal(toolResultJSON(create), &created); err != nil {
		t.Fatalf("unmarshal watch create: %v", err)
	}
	if !strings.HasPrefix(created.WatchID, "watch_") || !created.Watching {
		t.Fatalf("create result = %+v, want watch id and watching=true", created)
	}

	clear := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "clear",
		Name:      "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"clear","watch_id":%q}`, created.WatchID)),
	})
	if clear.IsError {
		t.Fatalf("job_watch clear returned error: %s", clear.Output)
	}
	var cleared struct {
		WatchID  string `json:"watch_id"`
		Watching bool   `json:"watching"`
	}
	if err := json.Unmarshal(toolResultJSON(clear), &cleared); err != nil {
		t.Fatalf("unmarshal watch clear: %v", err)
	}
	if cleared.WatchID != created.WatchID || cleared.Watching {
		t.Fatalf("clear result = %+v, want same watch id and watching=false", cleared)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./agent/internal/tool -run TestDefJobWatchRequiresOperationAndWatchIDForClear -count=1
go test ./agent -run TestJobWatchCreateReturnsIDAndClearUsesIDOnly -count=1
```

Expected: fail because `operation` and `watch_id` are not implemented.

- [ ] **Step 3: Update the `job_watch` schema**

In `agent/internal/tool/definitions.go`, update `DefJobWatch` properties:

```go
"operation": map[string]any{"type": "string", "enum": []string{"create", "list", "inspect", "clear"}},
"watch_id":  map[string]any{"type": "string", "description": "watch_id returned by job_watch create/list; required for inspect and clear."},
```

Remove the public `clear` boolean and remove `*` and `watched` from descriptions. Keep `events:["*"]` only if event-kind wildcard is still supported; do not describe `target:"*"`.

- [ ] **Step 4: Decode watch operations**

In `agent/job_watch.go`, extend `watchArgs`:

```go
Operation string
WatchID   string
```

In `watchArgsFromToolArgs`, require:

```go
op := strings.TrimSpace(stringArg(args, "operation"))
if op == "" {
	return watchArgs{}, errors.New("invalid_request: operation is required")
}
switch op {
case "create":
	if strings.TrimSpace(stringArg(args, "target")) == "" {
		return watchArgs{}, errors.New("invalid_request: target is required for operation=create")
	}
case "list":
case "inspect", "clear":
	if strings.TrimSpace(stringArg(args, "watch_id")) == "" {
		return watchArgs{}, fmt.Errorf("invalid_request: watch_id is required for operation=%s", op)
	}
default:
	return watchArgs{}, fmt.Errorf("invalid_request: unsupported operation %q", op)
}
```

Reject removed targets:

```go
if a.Target == "*" {
	return watchArgs{}, errors.New("invalid_request: wildcard watch target is not supported in v1")
}
if a.Send != nil && strings.HasPrefix(a.Send.To, "job_") {
	return watchArgs{}, errors.New("invalid_request: job_id is a job/turn handle; watch sends target delegate_id")
}
if a.Send != nil && a.Send.To == runtimeMessageAliasWatched {
	return watchArgs{}, errors.New("invalid_request: watched is not a v1 delivery target")
}
```

- [ ] **Step 5: Add watch ids to live configs and registry events**

Extend `watchConfig`:

```go
watchID    string
configHash string
```

When creating a new config:

```go
cfg.watchID = jobstore.NewWatchID()
cfg.configHash = normalizedWatchConfigHash(a)
```

Before installing a new config, append `EventWatchRegistered`:

```go
jobstore.Event{
	Kind:    jobstore.EventWatchRegistered,
	TS:      jm.now(),
	WatchID: cfg.watchID,
	Watch: &jobstore.WatchEvent{
		Generation:       cfg.generation,
		OwnerSessionID:   jm.sessionID,
		VisibleSessionID: jm.sessionID,
		Target:           cfg.target,
		SendTo:           watchSendTo(cfg.send),
		ConfigHash:       cfg.configHash,
		Condition:        watchConditionSummary(cfg),
		Config:           watchConfigSnapshot(cfg),
	},
}
```

For idempotent re-create with the same config hash, return the existing `watchID` and do not append another `EventWatchRegistered`.

- [ ] **Step 6: Implement clear-by-watch-id**

Add:

```go
func (jm *jobManager) watchConfigByIDLocked(watchID string) (watchKey, *watchConfig, bool) {
	for key, cfg := range jm.watches {
		if cfg != nil && cfg.watchID == watchID {
			return key, cfg, true
		}
	}
	return watchKey{}, nil, false
}
```

Replace model-facing clear path with:

```go
func (jm *jobManager) clearWatchByID(watchID string) (watchResult, error) {
	jm.mu.Lock()
	key, cfg, ok := jm.watchConfigByIDLocked(watchID)
	if !ok {
		jm.mu.Unlock()
		return watchResult{WatchID: watchID, Watching: false}, nil
	}
	generation := cfg.generation
	jm.mu.Unlock()
	res, err := jm.clearWatch(key)
	if err != nil {
		return watchResult{}, err
	}
	res.WatchID = watchID
	if err := jm.appendEvent(jobstore.Event{
		Kind:    jobstore.EventWatchCleared,
		TS:      jm.now(),
		WatchID: watchID,
		Watch:   &jobstore.WatchEvent{Generation: generation, EndReason: "cleared"},
	}); err != nil {
		return watchResult{}, err
	}
	return res, nil
}
```

Extend `watchResult` and `jobWatchToolResult` with `WatchID string`.

- [ ] **Step 7: Run clear-by-id tests**

Run:

```bash
go test ./agent/internal/tool -run TestDefJobWatchRequiresOperationAndWatchIDForClear -count=1
go test ./agent -run 'TestJobWatchCreateReturnsIDAndClearUsesIDOnly|TestJobWatch.*' -count=1
```

Expected: pass.

- [ ] **Step 8: Add upsert and stale-id behavior tests**

Add to `agent/session_tools_jobs_test.go`:

```go
func TestJobWatchDuplicateCreateReturnsSameIDAndChangedConfigReturnsNewID(t *testing.T) {
	s := newTestSession(t)
	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{ID: "shell", Name: "shell", Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`)})
	var shellOut struct{ JobID string `json:"job_id"` }
	if err := json.Unmarshal(toolResultJSON(shellRes), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v", err)
	}
	t.Cleanup(func() { _, _ = s.jobManager.stop(shellOut.JobID); waitForShellDone(t, s.jobManager, shellOut.JobID) })

	callWatch := func(pattern string) string {
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "watch-" + pattern,
			Name:      "job_watch",
			Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"create","target":%q,"output_match":%q}`, shellOut.JobID, pattern)),
		})
		if res.IsError {
			t.Fatalf("job_watch %q returned error: %s", pattern, res.Output)
		}
		var out struct{ WatchID string `json:"watch_id"` }
		if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
			t.Fatalf("unmarshal watch %q: %v", pattern, err)
		}
		return out.WatchID
	}

	first := callWatch("ready")
	duplicate := callWatch("ready")
	changed := callWatch("done")
	if duplicate != first {
		t.Fatalf("duplicate watch id = %q, want %q", duplicate, first)
	}
	if changed == first {
		t.Fatalf("changed config reused watch id %q, want a new id", changed)
	}
}
```

Run:

```bash
go test ./agent -run TestJobWatchDuplicateCreateReturnsSameIDAndChangedConfigReturnsNewID -count=1
```

Expected: pass.

- [ ] **Step 9: Commit**

Run:

```bash
git add agent/job_watch.go agent/session_tools_jobs.go agent/internal/tool/definitions.go agent/job_watch_test.go agent/session_tools_jobs_test.go agent/internal/tool/definitions_test.go
git commit -m "feat(job-control): manage watches by durable watch id"
```

---

### Task 6: Watch Delivery To `delegate_id` And Observer Grants

**Files:**
- Modify: `agent/job_watch.go`
- Modify: `agent/job_delegate.go`
- Modify: `agent/internal/jobstore/record.go`
- Modify: `agent/internal/jobstore/fold.go`
- Test: `agent/job_watch_observer_test.go`
- Test: `agent/job_watch_test.go`
- Test: `agent/session_tools_jobs_test.go`

- [ ] **Step 1: Write failing observer delivery test**

In `agent/job_watch_observer_test.go`, replace the old job-id observer send test with:

```go
func TestJobWatchSendsToObserverDelegateIDAcrossResume(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("observer ready") },
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("observer saw ready") },
	}})
	s := newDelegateTestSession(t, c)
	observer := s.createDelegate(context.Background(), delegateArgs{Task: "observe", Background: false, BlockTimeoutMS: 5000})
	if observer.Err != nil {
		t.Fatalf("observer delegate: %v", observer.Err)
	}

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
	})
	var shellOut struct{ JobID string `json:"job_id"` }
	if err := json.Unmarshal(toolResultJSON(shellRes), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v", err)
	}
	t.Cleanup(func() { _, _ = s.jobManager.stop(shellOut.JobID); waitForShellDone(t, s.jobManager, shellOut.JobID) })

	watchRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:   "watch",
		Name: "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(
			`{"operation":"create","target":%q,"output_match":"READY","send":{"to":%q,"message":"observe","include_excerpt":true}}`,
			shellOut.JobID,
			observer.DelegateID,
		)),
	})
	if watchRes.IsError {
		t.Fatalf("job_watch returned error: %s", watchRes.Output)
	}

	feedJob(s.jobManager, shellOut.JobID, []byte("server READY\n"))
	if err := drainWatchSendsVia(t, s.jobManager, s.sendDelegateMessage); err != nil {
		t.Fatalf("drain watch sends: %v", err)
	}

	delegates, err := s.jobManager.store.LoadDelegates()
	if err != nil {
		t.Fatalf("LoadDelegates: %v", err)
	}
	got := delegates[observer.DelegateID]
	if got == nil || got.LatestJobID == observer.JobID {
		t.Fatalf("observer delegate = %+v, want resumed under same delegate_id", got)
	}
	grants, err := s.jobManager.store.LoadGrants()
	if err != nil {
		t.Fatalf("LoadGrants: %v", err)
	}
	if !grants[got.ChildSessionID][shellOut.JobID] {
		t.Fatalf("grants = %+v, want observer child session grant to watched job", grants)
	}
}
```

- [ ] **Step 2: Run observer delivery test to verify it fails**

Run:

```bash
go test ./agent -run TestJobWatchSendsToObserverDelegateIDAcrossResume -count=1
```

Expected: fails because `send.to=dlg_...` is not resolvable.

- [ ] **Step 3: Resolve watch send targets by delegate id**

In `agent/job_watch.go`, change `validateWatchSendTarget`:

```go
if strings.HasPrefix(target, "job_") {
	return errors.New("invalid_request: job_id is a job/turn handle; watch sends target delegate_id")
}
if strings.HasPrefix(target, "dlg_") {
	delegates, err := jm.store.LoadDelegates()
	if err != nil {
		return err
	}
	d := delegates[target]
	if d == nil {
		return fmt.Errorf("target_not_found: delegate %q not found", target)
	}
	if d.OwnerSessionID != jm.sessionID && d.VisibleSessionID != jm.sessionID {
		return fmt.Errorf("not_controllable: delegate %q is not owned by this session", target)
	}
	return nil
}
```

Update `resolveWatchSendTarget` so `dlg_...` remains `dlg_...`; it should not resolve to the current observer job id.

Update `watchSendKey` and `WatchSendKey` to carry `WatchID` and `ResolvedSendTo = dlg_...`.

- [ ] **Step 4: Mint grants by delegate session identity**

Replace `watchReadGrantObserver(observerJobID string)` with:

```go
func (jm *jobManager) watchReadGrantObserver(sendTo string) (childSessionID string, ok bool, err error) {
	if strings.HasPrefix(sendTo, "dlg_") {
		delegates, err := jm.store.LoadDelegates()
		if err != nil {
			return "", false, err
		}
		d := delegates[sendTo]
		if d == nil || d.ChildSessionID == "" {
			return "", false, nil
		}
		return d.ChildSessionID, true, nil
	}
	return "", false, nil
}
```

Keep read grants snapshot-only through existing `FoldGrants`.

- [ ] **Step 5: Deliver watch sends with `OnIdle: "start"`**

Where `drainPendingWatchSends` builds `sendMessageArgs`, set:

```go
OnIdle:        "start",
FromWatch:     true,
Background:    true,
BackgroundSet: true,
```

Keep direct `delegate_send` default at `fail`.

- [ ] **Step 6: Ensure frames omit transcript refs**

In `buildWatchFrame`, remove transcript-ref inclusion for ordinary watch frames. Assert with:

```go
func TestWatchFrameOmitsTranscriptRefForObserverGrant(t *testing.T) {
	jm := newTestSession(t).jobManager
	frame := jm.buildWatchFrame(&watchConfig{send: &watchSendArgs{To: "dlg_obs", IncludeExcerpt: true}}, "job_A", "output_match: ready", "wd_1")
	if strings.Contains(frame, "transcript_ref") || strings.Contains(frame, "local:") {
		t.Fatalf("watch frame leaked transcript ref: %s", frame)
	}
}
```

- [ ] **Step 7: Run observer/grant tests**

Run:

```bash
go test ./agent -run 'TestJobWatchSendsToObserverDelegateIDAcrossResume|TestWatchFrameOmitsTranscriptRefForObserverGrant|TestJobWatch.*send' -count=1
```

Expected: pass.

- [ ] **Step 8: Commit**

Run:

```bash
git add agent/job_watch.go agent/job_delegate.go agent/internal/jobstore/record.go agent/internal/jobstore/fold.go agent/job_watch_observer_test.go agent/job_watch_test.go agent/session_tools_jobs_test.go
git commit -m "feat(job-control): deliver watch frames to delegate handles"
```

---

### Task 7: Stop Gate For Pending Watch Deliveries

**Files:**
- Modify: `agent/internal/jobstore/record.go`
- Modify: `agent/job_watch.go`
- Modify: `agent/session_tools_jobs.go`
- Modify: `agent/jobs.go`
- Test: `agent/job_watch_test.go`
- Test: `agent/job_delegate_test.go`
- Test: `agent/session_tools_jobs_test.go`

- [ ] **Step 1: Write failing stop-gate test**

Add to `agent/job_watch_test.go`:

```go
func TestStoppedDelegateDropsPreStopPendingWatchSend(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("observer ready") },
	}})
	s := newDelegateTestSession(t, c)
	observer := s.createDelegate(context.Background(), delegateArgs{Task: "observe", Background: true})
	if observer.Err != nil {
		t.Fatalf("observer delegate: %v", observer.Err)
	}

	state := jobstore.WatchSendState{
		Key: jobstore.WatchSendKey{
			VisibleSessionID:        s.id,
			WatchTarget:             "job_target",
			ResolvedWatchedIdentity: "job_target",
			ResolvedSendTo:          observer.DelegateID,
			WatchGeneration:         "wg_1",
		},
		DeliveryID:         "wd_1",
		UpdateSeq:          1,
		Message:            "old frame",
		DelegateGeneration: observer.DelegateGeneration,
	}
	if err := s.jobManager.persistPendingWatchSendForTest(state); err != nil {
		t.Fatalf("persist pending: %v", err)
	}
	_, _ = s.jobManager.stop(observer.JobID)
	waitForShellDone(t, s.jobManager, observer.JobID)

	if err := drainWatchSendsVia(t, s.jobManager, s.sendDelegateMessage); err != nil {
		t.Fatalf("drain watch sends: %v", err)
	}
	pending, err := s.jobManager.store.LoadWatchSends()
	if err != nil {
		t.Fatalf("LoadWatchSends: %v", err)
	}
	if len(pending.Pending) != 0 {
		t.Fatalf("pending sends = %+v, want pre-stop delivery suppressed", pending.Pending)
	}
}
```

If no `persistPendingWatchSendForTest` helper exists, add it in `agent/job_watch_test.go` so the test uses the same append path as production:

```go
func (jm *jobManager) persistPendingWatchSendForTest(state jobstore.WatchSendState) error {
	return jm.appendEvent(jobstore.Event{Kind: jobstore.EventWatchSendPending, TS: jm.now(), WatchSend: &state})
}
```

- [ ] **Step 2: Run stop-gate test to verify it fails**

Run:

```bash
go test ./agent -run TestStoppedDelegateDropsPreStopPendingWatchSend -count=1
```

Expected: fails because pending watch sends do not carry delegate generation and stop does not close the gate.

- [ ] **Step 3: Add delegate generation to watch-send state**

In `agent/internal/jobstore/record.go`, extend `WatchSendState`:

```go
DelegateGeneration string `json:"delegate_generation,omitempty"`
```

When creating a watch send to `dlg_...`, load the delegate record and stamp its current generation:

```go
state.DelegateGeneration = delegate.Generation
```

- [ ] **Step 4: Close the stop gate when stopping a delegate's current job**

In `jobStopTool` after `stopNestedOrLocal`, find the delegate record for `jobID`. If `jobID` is that delegate's current job, append:

```go
jobstore.Event{
	Kind:       jobstore.EventDelegateStopGateClosed,
	TS:         jm.now(),
	DelegateID: delegateID,
	Delegate: &jobstore.DelegateEvent{
		Generation: jobstore.NewDelegateGeneration(),
		StopJobID:  jobID,
	},
}
```

Do the same in lower-level stop paths used by internal cascade stops so explicit `job_stop` and cascade stop share the invariant.

- [ ] **Step 5: Suppress stale pending deliveries during drain**

Before delivering a pending send to `dlg_...`, load delegates and check:

```go
if strings.HasPrefix(state.Key.ResolvedSendTo, "dlg_") {
	delegate := delegates[state.Key.ResolvedSendTo]
	if delegate == nil || delegate.StopGateClosed {
		if state.DelegateGeneration == "" || state.DelegateGeneration == delegate.Generation || state.DelegateGeneration <= delegate.Generation {
			return jm.dropWatchSend(state, cfg, "delegate stopped before delivery")
		}
	}
}
```

Do not compare ULID strings if generations are not ordered by lexical format in the code under edit; instead add `StoppedAtSeq int64` to `DelegateRecord` and `CreatedAtSeq int64` to `WatchSendState` using event `Seq`. Use those numeric fields for the final comparison. The test must assert stale sends are dropped and later sends after explicit `delegate_send(on_idle="start")` are delivered.

- [ ] **Step 6: Add explicit-resume clears-future-only test**

Add to `agent/job_watch_test.go`:

```go
func TestDelegateSendExplicitStartDoesNotReenablePreStopPendingWatchSend(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("observer ready") },
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("observer restarted") },
	}})
	s := newDelegateTestSession(t, c)
	observer := s.createDelegate(context.Background(), delegateArgs{Task: "observe", Background: false, BlockTimeoutMS: 5000})
	if observer.Err != nil {
		t.Fatalf("observer delegate: %v", observer.Err)
	}
	_, _ = s.jobManager.stop(observer.JobID)
	waitForShellDone(t, s.jobManager, observer.JobID)

	res := s.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:        observer.DelegateID,
		Message:       "restart observer",
		OnIdle:        "start",
		Background:    false,
		BackgroundSet: true,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("explicit restart: %v", res.Err)
	}
	if res.StartedJobID == "" || res.StartedJobID == observer.JobID {
		t.Fatalf("restart result = %+v, want a later concrete job", res)
	}
}
```

- [ ] **Step 7: Run stop-gate tests**

Run:

```bash
go test ./agent -run 'TestStoppedDelegateDropsPreStopPendingWatchSend|TestDelegateSendExplicitStartDoesNotReenablePreStopPendingWatchSend|TestJobStop' -count=1
```

Expected: pass.

- [ ] **Step 8: Commit**

Run:

```bash
git add agent/internal/jobstore/record.go agent/job_watch.go agent/session_tools_jobs.go agent/jobs.go agent/job_watch_test.go agent/job_delegate_test.go agent/session_tools_jobs_test.go
git commit -m "feat(job-control): stop stale watch deliveries after delegate stop"
```

---

### Task 8: Handle-Mismatch Errors And Tool Surface Cleanup

**Files:**
- Modify: `agent/session_tools_jobs.go`
- Modify: `agent/job_watch.go`
- Modify: `agent/internal/tool/definitions.go`
- Test: `agent/session_tools_jobs_test.go`
- Test: `agent/job_watch_test.go`
- Test: `agent/internal/tool/definitions_test.go`

- [ ] **Step 1: Write failing mismatch tests**

Add to `agent/session_tools_jobs_test.go`:

```go
func TestJobToolsRejectDelegateIDWithActionableGuidance(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("done") },
	}})
	s := newDelegateTestSession(t, c)
	res := s.createDelegate(context.Background(), delegateArgs{Task: "finish", Background: false, BlockTimeoutMS: 5000})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}

	for _, tc := range []struct {
		name string
		tool string
		args string
		want string
	}{
		{"read", "job_read_output", fmt.Sprintf(`{"job_id":%q}`, res.DelegateID), "delegate_id is a conversation handle; read output from job_id"},
		{"stop", "job_stop", fmt.Sprintf(`{"job_id":%q}`, res.DelegateID), "delegate_id is a conversation handle; stop a concrete job_id"},
		{"watch", "job_watch", fmt.Sprintf(`{"operation":"create","target":%q,"events":["assistant.message"]}`, res.DelegateID), "delegate_id is not watchable; watch current_job_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{ID: tc.name, Name: tc.tool, Arguments: json.RawMessage(tc.args)})
			if !call.IsError {
				t.Fatalf("%s succeeded, want error: %s", tc.tool, call.Output)
			}
			if !strings.Contains(call.Output, tc.want) {
				t.Fatalf("%s error = %q, want %q", tc.tool, call.Output, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run mismatch tests to verify they fail**

Run:

```bash
go test ./agent -run TestJobToolsRejectDelegateIDWithActionableGuidance -count=1
```

Expected: fail because tools currently treat `dlg_...` as missing job targets.

- [ ] **Step 3: Implement prefix guards in job tools**

At the top of `jobReadOutputTool` and `jobStopTool`, add:

```go
if strings.HasPrefix(jobID, "dlg_") {
	return "", errors.New("invalid_request: delegate_id is a conversation handle; read output from job_id")
}
```

Use the stop-specific message in `jobStopTool`.

In `watchArgsFromToolArgs`, reject `target` with `dlg_` using:

```go
return watchArgs{}, errors.New("invalid_request: delegate_id is not watchable; watch current_job_id")
```

- [ ] **Step 4: Remove model-facing `job_send_message` references from registered tools**

In `agent/session_tools_jobs.go`, update `jobToolResultMaxChars` loop:

```go
for _, name := range []string{"job_read_output", "job_list", "job_stop", "delegate", "job_watch", "delegate_send"} {
```

Search:

```bash
rg -n '"job_send_message"|DefJobSendMessage|jobSendMessageTool' agent --glob '*.go'
```

For production registry/profile/tool-definition files, replace model-facing usage with `delegate_send`. Leave transcript renderer tests that intentionally cover historical `job_send_message`.

- [ ] **Step 5: Run targeted surface tests**

Run:

```bash
go test ./agent ./agent/internal/tool ./agent/provider -run 'TestJobToolsRejectDelegateIDWithActionableGuidance|TestJobToolsDefinitions|Test.*Profile|TestDefDelegateSendShape|TestDefJobWatchRequiresOperationAndWatchIDForClear' -count=1
```

Expected: pass.

- [ ] **Step 6: Commit**

Run:

```bash
git add agent/session_tools_jobs.go agent/job_watch.go agent/internal/tool/definitions.go agent/session_tools_jobs_test.go agent/job_watch_test.go agent/internal/tool/definitions_test.go agent/provider/profile.go agent/profile_test.go agent/provider/profile_test.go
git commit -m "feat(job-control): reject mixed job delegate and watch handles"
```

---

### Task 9: Prompts, Docs, Scenarios, And Renderers

**Files:**
- Modify: `agent/prompts/sections/background-jobs.md`
- Modify: `agent/prompts/sections/delegation.md`
- Modify: `docs/job-control.md`
- Modify: `docs/tools/transcripts.md`
- Modify: `test/scenarios/job-watch-sidecar-observer.md`
- Modify: `test/scenarios/job-send-message-surface.md`
- Modify: `test/scenarios/subagent-cancel-runaway.md`
- Modify: `agent/bundled_plugins/coordinator-workflow/agents/coordinator.md`
- Modify: `agent/transcript_render.go`
- Modify: `agent/transcript_render_test.go`
- Modify: `agent/transcript_render_job_test.go`
- Modify: `cmd/serf-tui/internal/msgrender/tool_renderers.go`
- Modify: `cmd/serf-tui/internal/msgrender/tool_renderers_test.go`
- Modify: `cmd/serf-hub/assets/renderer-tools.js`
- Modify: `cmd/serf-hub/jstest/test-tool-renderers.js`

- [ ] **Step 1: Update public guidance text**

Apply these wording rules everywhere model-facing docs mention job control:

```text
Message delegates by delegate_id with delegate_send.
Read, stop, and watch concrete jobs by job_id.
Clear watches by watch_id.
Do not send to job_id.
Do not watch delegate_id.
Do not use transcript_ref as a job-control target.
```

Specific replacements:

- `job_send_message(target=<job_id>)` -> `delegate_send(to=<delegate_id>, on_idle="start")`
- `job_watch(..., send={to:<observer job_id>})` -> `job_watch(operation="create", ..., send={to:<observer delegate_id>})`
- `job_watch(clear=true, target=...)` -> `job_watch(operation="clear", watch_id=<watch_id>)`
- `send.to="watched"` -> remove the example
- `target="*"` -> remove the example

- [ ] **Step 2: Update scenario files**

For `test/scenarios/job-watch-sidecar-observer.md`, rewrite the core expected flow as:

```markdown
1. Start the observer with `delegate(...)`; record `delegate_id` and `current_job_id`.
2. Start the watched shell job; record its `job_id`.
3. Call `job_watch(operation="create", target=<watched_job_id>, output_match=..., send={to:<observer_delegate_id>, message:..., include_excerpt:true})`; record `watch_id`.
4. The observer reports with `delegate_send(to="caller", message=...)`.
5. Cleanup uses `job_watch(operation="clear", watch_id=<watch_id>)` and `job_stop(job_id=<watched_job_id>)`.
```

For `test/scenarios/job-send-message-surface.md`, rename the scenario to describe `delegate_send` and assert:

```markdown
- `delegate_send(to=<job_id>)` is rejected with job/turn handle guidance.
- `delegate_send(to=<delegate_id>)` while running returns `action:"steered"`.
- `delegate_send(to=<delegate_id>)` while idle fails by default.
- `delegate_send(to=<delegate_id>, on_idle="start")` starts a new concrete job and returns `started_job_id`.
```

- [ ] **Step 3: Add renderer tests for `delegate_send` while preserving historical `job_send_message`**

In `agent/transcript_render_test.go`, add:

```go
func TestTranscriptRenderDelegateSendToolCard(t *testing.T) {
	out := renderToolCardForResult("delegate_send", "call_send", `{"delegate_id":"dlg_01","started_job_id":"job_02","action":"started","status":"completed","output":"done"}`)
	if !strings.Contains(out, "delegate_send") || !strings.Contains(out, "dlg_01") || !strings.Contains(out, "job_02") {
		t.Fatalf("delegate_send card = %s, want delegate and job ids", out)
	}
}

func TestTranscriptRenderHistoricalJobSendMessageStillRenders(t *testing.T) {
	out := renderToolCardForResult("job_send_message", "call_old", `{"target":"job_01","job_id":"job_02","action":"resumed"}`)
	if !strings.Contains(out, "job_send_message") || !strings.Contains(out, "job_02") {
		t.Fatalf("historical job_send_message card = %s, want old transcript rendering", out)
	}
}
```

In `cmd/serf-tui/internal/msgrender/tool_renderers_test.go`, add a `delegate_send` renderer assertion:

```go
func TestDelegateSendRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("delegate_send")
	args := map[string]any{"to": "dlg_01", "message": "continue"}
	if got := r.Verb(args); got != "message" {
		t.Fatalf("delegate_send verb = %q, want message", got)
	}
	if got := r.Target(args); got != "dlg_01" {
		t.Fatalf("delegate_send target = %q, want dlg_01", got)
	}
}
```

In `cmd/serf-hub/jstest/test-tool-renderers.js`, add a fixture with `tool_name: "delegate_send"` and assert the rendered detail contains `dlg_01` and `started`.

- [ ] **Step 4: Update renderers**

In `agent/transcript_render.go`, add a `case "delegate_send"` next to the historical `case "job_send_message"`. Use the same lifecycle-card style but read `delegate_id`, `started_job_id`, and `latest_job_id`.

In `cmd/serf-tui/internal/msgrender/tool_renderers.go`, register:

```go
toolRenderers["delegate_send"] = jobControl("message")
```

Keep:

```go
toolRenderers["job_send_message"] = jobControl("message")
```

for archived transcript rendering only.

In `cmd/serf-hub/assets/renderer-tools.js`, add `delegate_send` to the same renderer family as the historical send card and map `to` as the primary target field.

- [ ] **Step 5: Run docs/rendering checks**

Run:

```bash
go test ./agent ./cmd/serf-tui/internal/msgrender -run 'TestTranscriptRender.*Send|TestDelegateSendRenderer|Test.*Renderer' -count=1
node cmd/serf-hub/jstest/test-tool-renderers.js
rg -n 'job_send_message|send=\\{to:<observer job_id>|target=\"\\*\"|send\\.to=\"watched\"' agent/prompts docs/job-control.md docs/tools/transcripts.md test/scenarios agent/bundled_plugins/coordinator-workflow/agents/coordinator.md
```

Expected: Go and Node tests pass. `rg` returns only historical transcript documentation or explicitly labeled archival-rendering references.

- [ ] **Step 6: Commit**

Run:

```bash
git add agent/prompts/sections/background-jobs.md agent/prompts/sections/delegation.md docs/job-control.md docs/tools/transcripts.md test/scenarios/job-watch-sidecar-observer.md test/scenarios/job-send-message-surface.md test/scenarios/subagent-cancel-runaway.md agent/bundled_plugins/coordinator-workflow/agents/coordinator.md agent/transcript_render.go agent/transcript_render_test.go agent/transcript_render_job_test.go cmd/serf-tui/internal/msgrender/tool_renderers.go cmd/serf-tui/internal/msgrender/tool_renderers_test.go cmd/serf-hub/assets/renderer-tools.js cmd/serf-hub/jstest/test-tool-renderers.js
git commit -m "docs(job-control): update handle split guidance and renderers"
```

---

### Task 10: Final Verification And Cleanup

**Files:**
- Verify all modified files from Tasks 1-9
- Modify no files unless a verification failure identifies a concrete bug

- [ ] **Step 1: Run focused Go packages**

Run:

```bash
go test ./agent/internal/jobstore ./agent/internal/tool ./agent/provider ./cmd/serf-tui/internal/msgrender -count=1
```

Expected: pass.

- [ ] **Step 2: Run the agent package with race for the mid-drive contract**

Run:

```bash
go test -race ./agent -run 'TestSendMessageMidDriveSteersNoSecondTurn|TestWatchResumeMidDriveSteersNotDropped|TestJobWatchSendsToObserverDelegateIDAcrossResume|TestStoppedDelegateDropsPreStopPendingWatchSend' -count=1
```

Expected: pass with no race report.

- [ ] **Step 3: Run JavaScript renderer checks**

Run:

```bash
node cmd/serf-hub/jstest/test-tool-renderers.js
node cmd/serf-hub/jstest/test-subagents.js
```

Expected: pass.

- [ ] **Step 4: Search for stale public API references**

Run:

```bash
rg -n 'job_send_message|on_finished|send\\.to.*job_id|target=\"\\*\"|send\\.to=\"watched\"|clear=true' agent docs test cmd tools --glob '*.go' --glob '*.md' --glob '*.js'
```

Expected: remaining `job_send_message` hits are historical transcript rendering/tests or explicitly archived research. No model-facing prompt, active tool definition, scenario, or provider profile advertises the old control path.

- [ ] **Step 5: Run broad tests**

Run:

```bash
go test ./...
```

Expected: pass.

- [ ] **Step 6: Inspect git state**

Run:

```bash
git status --short
```

Expected: only intentional tracked changes are present. Existing unrelated untracked files remain untracked unless Jesse explicitly asks to include them.

- [ ] **Step 7: Commit verification fixes or record clean verification**

If Step 5 passed without fixes, run:

```bash
git commit --allow-empty -m "test(job-control): verify handle split implementation"
```

If Step 5 required fixes, commit the specific fix files with:

```bash
git add <specific files fixed>
git commit -m "fix(job-control): resolve handle split verification failures"
```

## Self-Review

- Spec coverage: durable delegate identity is covered by Tasks 1 and 3; durable watch identity by Tasks 2 and 5; `delegate_send` handle split and idle behavior by Task 4; observer sidecars and read grants by Task 6; stop-generation safety by Task 7; mismatch errors by Task 8; docs/rendering migration by Task 9; verification by Task 10.
- Placeholder scan: this plan avoids open-ended placeholders and gives concrete paths, test names, command lines, and expected outcomes.
- Type consistency: the plan consistently uses `delegate_id`, `started_job_id`, `current_job_id`, `latest_job_id`, `watch_id`, `operation`, `on_idle`, `DelegateID`, `StartedJobID`, `LatestJobID`, `WatchID`, and `Generation`.
