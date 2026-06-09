# Job Control — Phase 1: jobstore core (Implementation Plan)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the pure, `Session`-free durable substrate for Serf's job-control system — the job record, the append-only event log, per-job output files, terminal-notification bookkeeping, and restart reconciliation — as a standalone, fully unit-tested `agent/internal/jobstore` package.

**Architecture:** A leaf package under `agent/internal/jobstore` that deals only in records, bytes, and an append-only JSONL event log. It imports only stdlib + `github.com/oklog/ulid/v2` and knows nothing about `Session`, providers, or the agent runtime — so every behavior is testable in isolation. Later phases (shell jobs, delegate jobs, watches, nested jobs) build the runtime glue in package `agent` on top of this substrate. The event log folds to `JobRecord`s the same way the existing `agent/transcript` writer appends turns.

**Tech Stack:** Go 1.x, `github.com/oklog/ulid/v2` (already an `agent` dependency), `encoding/json`, `regexp` (RE2), standard `os`/`sync`. Module: `primeradiant.com/serf/agent`. Tests: `go test`.

This is **Phase 1 of 6**. It is the design in `docs/superpowers/specs/2026-06-08-job-control-design.md` §3 (data model), §6 (notification bookkeeping), §7 (reconciliation). It produces a tested library with no wiring into the live agent yet; nothing in this phase changes model-facing behavior. The phase roadmap is at the end of this document.

**Conventions for every task below:**
- Work in the `agent` module: run Go commands from `/Users/jesse/prime-radiant/toil-suite/serf/agent` (the repo is a go.work workspace; `jobstore` lives in the `agent` module).
- Run the package's tests with: `cd agent && go test ./internal/jobstore/ -v`
- TDD: write the failing test first, watch it fail, write the minimal implementation, watch it pass, commit.
- Commit messages use the repo's `type(scope): subject` style, e.g. `feat(jobstore): ...`.

---

## File structure

All new, all in one package `jobstore`:

```
agent/internal/jobstore/
  record.go        JobRecord struct; JobType/Status/NotifyState enums; Status.IsTerminal; NewJobID
  record_test.go
  event.go         EventKind constants; Event envelope; (de)serialization
  event_test.go
  fold.go          Fold([]Event) -> map[job_id]*JobRecord  (pure reconstruction)
  fold_test.go
  store.go         Store: Open/Append/Load over an append-only jobs.jsonl (file I/O around Fold)
  store_test.go
  output.go        OutputStore: Append/Tail/Grep over a bounded per-job .log; Match
  output_test.go
  notify.go        NewTerminalGeneration; DedupeKey; JobRecord.DedupeKey; ShouldDeliver
  notify_test.go
  reconcile.go     Reconcile(records, liveJobIDs, now) -> []Event  (running-without-runtime -> stopped/runtime_lost)
  reconcile_test.go
```

Each file has one responsibility. `fold.go` is split out from `store.go` so the reconstruction logic is testable without touching the filesystem.

---

## Task 1: Job record, enums, ID minting

**Files:**
- Create: `agent/internal/jobstore/record.go`
- Test: `agent/internal/jobstore/record_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/internal/jobstore/record_test.go`:

```go
package jobstore

import (
	"strings"
	"testing"
)

func TestStatusIsTerminal(t *testing.T) {
	terminal := []Status{StatusCompleted, StatusFailed, StatusCancelled, StatusStopped}
	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Errorf("Status %q should be terminal", s)
		}
	}
	if StatusRunning.IsTerminal() {
		t.Errorf("Status %q should not be terminal", StatusRunning)
	}
}

func TestNewJobIDFormatAndUniqueness(t *testing.T) {
	a := NewJobID()
	b := NewJobID()
	if !strings.HasPrefix(a, "job_") {
		t.Errorf("job id %q should start with job_", a)
	}
	if a == b {
		t.Errorf("two job ids should differ: %q == %q", a, b)
	}
	// "job_" + 26-char ULID
	if len(a) != len("job_")+26 {
		t.Errorf("job id %q has unexpected length %d", a, len(a))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test ./internal/jobstore/ -run 'TestStatusIsTerminal|TestNewJobID' -v`
Expected: FAIL to compile — `undefined: Status`, `undefined: NewJobID`, etc.

- [ ] **Step 3: Write minimal implementation**

Create `agent/internal/jobstore/record.go`:

```go
// Package jobstore is the pure, Session-free durable substrate for Serf's
// job-control system: the job record, an append-only event log that folds to
// records, per-job output files, terminal-notification bookkeeping, and restart
// reconciliation. It imports only stdlib and a ULID library; the agent runtime
// glue that drives shell/delegate jobs lives in package agent on top of this.
package jobstore

import (
	"time"

	"github.com/oklog/ulid/v2"
)

// JobType is the class of work a job represents.
type JobType string

const (
	JobShell    JobType = "shell"
	JobDelegate JobType = "delegate"
)

// Status is a job's lifecycle state and the primary machine-branch field.
type Status string

const (
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
	StatusStopped   Status = "stopped"
)

// IsTerminal reports whether the status is a terminal (finished) state.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusCancelled, StatusStopped:
		return true
	default:
		return false
	}
}

// NotifyState tracks durable terminal-notification delivery.
type NotifyState string

const (
	NotifyNotArmed  NotifyState = "not_armed"
	NotifyPending   NotifyState = "pending"
	NotifyDelivered NotifyState = "delivered"
)

// JobRecord is the durable record for one job, reconstructed by folding the
// event log. Type-inapplicable fields are omitted from the model-facing
// projection by later phases; this struct is the storage shape.
type JobRecord struct {
	JobID            string      `json:"job_id"`
	Type             JobType     `json:"type"`
	Status           Status      `json:"status"`
	Reason           string      `json:"reason,omitempty"`
	Description      string      `json:"description,omitempty"`
	Command          string      `json:"command,omitempty"`
	Task             string      `json:"task,omitempty"`
	ParentSessionID  string      `json:"parent_session_id,omitempty"`
	OwnerSessionID   string      `json:"owner_session_id"`
	VisibleToSession string      `json:"visible_to_session_id"`
	ParentJobID      string      `json:"parent_job_id,omitempty"`
	OriginTurnID     string      `json:"origin_turn_id,omitempty"`
	OriginToolCallID string      `json:"origin_tool_call_id,omitempty"`
	TranscriptRef    string      `json:"transcript_ref,omitempty"`
	Resumable        *bool       `json:"resumable,omitempty"`
	NotResumableWhy  string      `json:"not_resumable_reason,omitempty"`
	StartedAt        time.Time   `json:"started_at"`
	EndedAt          *time.Time  `json:"ended_at,omitempty"`
	ExitCode         *int        `json:"exit_code,omitempty"`
	OutputPath       string      `json:"output_path,omitempty"`
	OutputBytes      int64       `json:"output_bytes"`
	TerminalGen      string      `json:"terminal_generation,omitempty"`
	NotifyState      NotifyState `json:"terminal_notification_state"`
}

// NewJobID mints a globally unique opaque job id: "job_" + ULID.
func NewJobID() string {
	return "job_" + ulid.Make().String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd agent && go test ./internal/jobstore/ -run 'TestStatusIsTerminal|TestNewJobID' -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/internal/jobstore/record.go agent/internal/jobstore/record_test.go
git commit -m "feat(jobstore): job record, status/type enums, job id minting"
```

---

## Task 2: Job event envelope

**Files:**
- Create: `agent/internal/jobstore/event.go`
- Test: `agent/internal/jobstore/event_test.go`

The event log is append-only JSONL. One flat `Event` struct carries every possible payload field (all `omitempty`); a fold applies each event's fields onto the record. This mirrors the spec §3.3 event table.

- [ ] **Step 1: Write the failing test**

Create `agent/internal/jobstore/event_test.go`:

```go
package jobstore

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEventJSONRoundTrip(t *testing.T) {
	ts := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	e := Event{
		Kind:        EventJobStarted,
		Seq:         1,
		TS:          ts,
		JobID:       "job_X",
		Type:        JobShell,
		Command:     "make test",
		Description: "run tests",
		StartedAt:   &ts,
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Event
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Kind != EventJobStarted || got.JobID != "job_X" || got.Command != "make test" {
		t.Errorf("round trip mismatch: %+v", got)
	}
	// Absent fields must stay absent in the wire form (omitempty).
	if got.Status != "" {
		t.Errorf("status should be empty, got %q", got.Status)
	}
}

func TestEventKindsAreStable(t *testing.T) {
	want := map[EventKind]string{
		EventJobStarted:               "job_started",
		EventJobSessionAssigned:       "job_session_assigned",
		EventJobFinished:              "job_finished",
		EventJobMessageSent:           "job_message_sent",
		EventJobNotificationPending:   "job_notification_pending",
		EventJobNotificationDelivered: "job_notification_delivered",
	}
	for k, s := range want {
		if string(k) != s {
			t.Errorf("event kind %v should serialize as %q", k, s)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test ./internal/jobstore/ -run TestEvent -v`
Expected: FAIL to compile — `undefined: Event`, `undefined: EventJobStarted`, etc.

- [ ] **Step 3: Write minimal implementation**

Create `agent/internal/jobstore/event.go`:

```go
package jobstore

import "time"

// EventKind identifies a durable job-lifecycle event in jobs.jsonl.
type EventKind string

const (
	EventJobStarted               EventKind = "job_started"
	EventJobSessionAssigned       EventKind = "job_session_assigned"
	EventJobFinished              EventKind = "job_finished"
	EventJobMessageSent           EventKind = "job_message_sent"
	EventJobNotificationPending   EventKind = "job_notification_pending"
	EventJobNotificationDelivered EventKind = "job_notification_delivered"
)

// Event is one line in the append-only jobs.jsonl log. It carries a flat union
// of every payload field used by any event kind; Fold applies the present
// fields onto the JobRecord. Seq is assigned by the Store at append time.
type Event struct {
	Kind EventKind `json:"kind"`
	Seq  int64     `json:"seq"`
	TS   time.Time `json:"ts"`

	JobID string `json:"job_id"`

	// job_started payload
	Type             JobType    `json:"type,omitempty"`
	Command          string     `json:"command,omitempty"`
	Task             string     `json:"task,omitempty"`
	Description      string     `json:"description,omitempty"`
	ParentSessionID  string     `json:"parent_session_id,omitempty"`
	OwnerSessionID   string     `json:"owner_session_id,omitempty"`
	VisibleToSession string     `json:"visible_to_session_id,omitempty"`
	ParentJobID      string     `json:"parent_job_id,omitempty"`
	OriginTurnID     string     `json:"origin_turn_id,omitempty"`
	OriginToolCallID string     `json:"origin_tool_call_id,omitempty"`
	StartedAt        *time.Time `json:"started_at,omitempty"`

	// job_session_assigned payload
	TranscriptRef   string `json:"transcript_ref,omitempty"`
	Resumable       *bool  `json:"resumable,omitempty"`
	NotResumableWhy string `json:"not_resumable_reason,omitempty"`

	// job_finished payload
	Status      Status     `json:"status,omitempty"`
	Reason      string     `json:"reason,omitempty"`
	ExitCode    *int       `json:"exit_code,omitempty"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	OutputBytes int64      `json:"output_bytes,omitempty"`
	TerminalGen string     `json:"terminal_generation,omitempty"`

	// job_message_sent payload
	Target string `json:"target,omitempty"`
	Action string `json:"action,omitempty"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd agent && go test ./internal/jobstore/ -run TestEvent -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/internal/jobstore/event.go agent/internal/jobstore/event_test.go
git commit -m "feat(jobstore): job-event envelope and kinds"
```

---

## Task 3: Fold events to records (pure reconstruction)

**Files:**
- Create: `agent/internal/jobstore/fold.go`
- Test: `agent/internal/jobstore/fold_test.go`

`Fold` is the pure heart of reconstruction: given events in seq order, produce the current `JobRecord` per job. It must (a) build a record from `job_started`, (b) overlay `job_session_assigned`/`job_finished`/notification events, and (c) **never re-mint `terminal_generation`** — the first `job_finished` wins, later terminal writes reuse it.

- [ ] **Step 1: Write the failing test**

Create `agent/internal/jobstore/fold_test.go`:

```go
package jobstore

import (
	"testing"
	"time"
)

func ev(kind EventKind, seq int64, jobID string, mut func(*Event)) Event {
	e := Event{Kind: kind, Seq: seq, TS: time.Unix(seq, 0).UTC(), JobID: jobID}
	if mut != nil {
		mut(&e)
	}
	return e
}

func TestFoldBuildsRunningShellRecord(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	events := []Event{
		ev(EventJobStarted, 1, "job_A", func(e *Event) {
			e.Type = JobShell
			e.Command = "npm run dev"
			e.Description = "dev server"
			e.OwnerSessionID = "S1"
			e.VisibleToSession = "S1"
			e.StartedAt = &start
		}),
	}
	recs := Fold(events)
	r := recs["job_A"]
	if r == nil {
		t.Fatal("expected record for job_A")
	}
	if r.Status != StatusRunning {
		t.Errorf("status = %q, want running", r.Status)
	}
	if r.Command != "npm run dev" || r.Description != "dev server" {
		t.Errorf("command/description not folded: %+v", r)
	}
	if r.NotifyState != NotifyNotArmed {
		t.Errorf("notify state = %q, want not_armed", r.NotifyState)
	}
}

func TestFoldAppliesFinishAndKeepsFirstGeneration(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	end := time.Unix(2, 0).UTC()
	code := 0
	events := []Event{
		ev(EventJobStarted, 1, "job_A", func(e *Event) {
			e.Type = JobShell
			e.OwnerSessionID = "S1"
			e.VisibleToSession = "S1"
			e.StartedAt = &start
		}),
		ev(EventJobFinished, 2, "job_A", func(e *Event) {
			e.Status = StatusCompleted
			e.Reason = "exit_zero"
			e.ExitCode = &code
			e.EndedAt = &end
			e.OutputBytes = 2048
			e.TerminalGen = "GEN1"
		}),
		// A duplicate reconstructed terminal write must NOT replace the generation.
		ev(EventJobFinished, 3, "job_A", func(e *Event) {
			e.Status = StatusCompleted
			e.TerminalGen = "GEN2"
		}),
	}
	r := Fold(events)["job_A"]
	if r.Status != StatusCompleted || r.Reason != "exit_zero" {
		t.Errorf("finish not folded: %+v", r)
	}
	if r.OutputBytes != 2048 || r.ExitCode == nil || *r.ExitCode != 0 {
		t.Errorf("finish payload not folded: %+v", r)
	}
	if r.TerminalGen != "GEN1" {
		t.Errorf("terminal_generation = %q, want GEN1 (first wins)", r.TerminalGen)
	}
}

func TestFoldNotificationStateTransitions(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	events := []Event{
		ev(EventJobStarted, 1, "job_A", func(e *Event) { e.Type = JobShell; e.OwnerSessionID = "S1"; e.VisibleToSession = "S1"; e.StartedAt = &start }),
		ev(EventJobFinished, 2, "job_A", func(e *Event) { e.Status = StatusCompleted; e.TerminalGen = "GEN1" }),
		ev(EventJobNotificationPending, 3, "job_A", func(e *Event) { e.TerminalGen = "GEN1" }),
		ev(EventJobNotificationDelivered, 4, "job_A", func(e *Event) { e.TerminalGen = "GEN1" }),
	}
	r := Fold(events)["job_A"]
	if r.NotifyState != NotifyDelivered {
		t.Errorf("notify state = %q, want delivered", r.NotifyState)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test ./internal/jobstore/ -run TestFold -v`
Expected: FAIL to compile — `undefined: Fold`.

- [ ] **Step 3: Write minimal implementation**

Create `agent/internal/jobstore/fold.go`:

```go
package jobstore

import "sort"

// Fold reconstructs the current JobRecord for each job by applying events in
// seq order. The first job_finished for a job fixes its terminal_generation and
// terminal status; later terminal writes do not overwrite them.
func Fold(events []Event) map[string]*JobRecord {
	sorted := append([]Event(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })

	recs := make(map[string]*JobRecord)
	for _, e := range sorted {
		r := recs[e.JobID]
		if r == nil {
			r = &JobRecord{JobID: e.JobID, NotifyState: NotifyNotArmed}
			recs[e.JobID] = r
		}
		applyEvent(r, e)
	}
	return recs
}

func applyEvent(r *JobRecord, e Event) {
	switch e.Kind {
	case EventJobStarted:
		r.Type = e.Type
		r.Command = e.Command
		r.Task = e.Task
		r.Description = e.Description
		r.ParentSessionID = e.ParentSessionID
		r.OwnerSessionID = e.OwnerSessionID
		r.VisibleToSession = e.VisibleToSession
		r.ParentJobID = e.ParentJobID
		r.OriginTurnID = e.OriginTurnID
		r.OriginToolCallID = e.OriginToolCallID
		if e.StartedAt != nil {
			r.StartedAt = *e.StartedAt
		}
		if r.Status == "" {
			r.Status = StatusRunning
		}
	case EventJobSessionAssigned:
		r.TranscriptRef = e.TranscriptRef
		r.Resumable = e.Resumable
		r.NotResumableWhy = e.NotResumableWhy
	case EventJobFinished:
		// First terminal write wins; later ones are duplicates/reconstructions.
		if r.Status.IsTerminal() {
			return
		}
		r.Status = e.Status
		r.Reason = e.Reason
		r.ExitCode = e.ExitCode
		r.EndedAt = e.EndedAt
		r.OutputBytes = e.OutputBytes
		r.TerminalGen = e.TerminalGen
	case EventJobMessageSent:
		// No record-field mutation; message events are diagnostic/history.
	case EventJobNotificationPending:
		if r.NotifyState == NotifyNotArmed {
			r.NotifyState = NotifyPending
		}
	case EventJobNotificationDelivered:
		r.NotifyState = NotifyDelivered
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd agent && go test ./internal/jobstore/ -run TestFold -v`
Expected: PASS (all three).

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/internal/jobstore/fold.go agent/internal/jobstore/fold_test.go
git commit -m "feat(jobstore): fold events to records (first-terminal-wins)"
```

---

## Task 4: Append-only store (file I/O around Fold)

**Files:**
- Create: `agent/internal/jobstore/store.go`
- Test: `agent/internal/jobstore/store_test.go`

`Store` is the file I/O wrapper: it opens/creates `jobs.jsonl`, assigns monotonic `Seq` at append, fsyncs, and reloads by reading all lines and calling `Fold`. It recovers the next `Seq` on open by scanning existing lines (survives restart).

- [ ] **Step 1: Write the failing test**

Create `agent/internal/jobstore/store_test.go`:

```go
package jobstore

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreAppendThenLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	start := time.Unix(1, 0).UTC()
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_A", Type: JobShell, OwnerSessionID: "S1", VisibleToSession: "S1", StartedAt: &start}); err != nil {
		t.Fatalf("append started: %v", err)
	}
	if err := s.Append(Event{Kind: EventJobFinished, JobID: "job_A", Status: StatusCompleted, TerminalGen: "GEN1"}); err != nil {
		t.Fatalf("append finished: %v", err)
	}
	recs, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if recs["job_A"].Status != StatusCompleted {
		t.Errorf("status = %q, want completed", recs["job_A"].Status)
	}
}

func TestStoreAssignsMonotonicSeq(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s, _ := Open(path)
	_ = s.Append(Event{Kind: EventJobStarted, JobID: "job_A"})
	_ = s.Append(Event{Kind: EventJobStarted, JobID: "job_B"})
	raw, err := s.readAll()
	if err != nil {
		t.Fatalf("readAll: %v", err)
	}
	if len(raw) != 2 || raw[0].Seq != 1 || raw[1].Seq != 2 {
		t.Errorf("seqs not monotonic from 1: %+v", raw)
	}
}

func TestStoreRecoversSeqAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s1, _ := Open(path)
	_ = s1.Append(Event{Kind: EventJobStarted, JobID: "job_A"})
	_ = s1.Close()

	s2, err := Open(path) // reopen: must continue seq at 2, not restart at 1
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = s2.Append(Event{Kind: EventJobFinished, JobID: "job_A", Status: StatusCompleted})
	raw, _ := s2.readAll()
	if raw[len(raw)-1].Seq != 2 {
		t.Errorf("seq after reopen = %d, want 2", raw[len(raw)-1].Seq)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test ./internal/jobstore/ -run TestStore -v`
Expected: FAIL to compile — `undefined: Open`.

- [ ] **Step 3: Write minimal implementation**

Create `agent/internal/jobstore/store.go`:

```go
package jobstore

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Store is an append-only jobs.jsonl event log for one session. It assigns a
// monotonic Seq to each appended event, fsyncs, and reconstructs records via
// Fold. It is safe for concurrent use.
type Store struct {
	mu   sync.Mutex
	path string
	f    *os.File
	seq  int64
}

// Open opens (creating if needed) the jobs.jsonl at path and recovers the next
// sequence number from any existing content.
func Open(path string) (*Store, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("jobstore: open %s: %w", path, err)
	}
	s := &Store{path: path, f: f}
	existing, err := s.readAllLocked()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	for _, e := range existing {
		if e.Seq > s.seq {
			s.seq = e.Seq
		}
	}
	return s, nil
}

// Append assigns the next Seq to e, writes it as a JSON line, and fsyncs.
func (s *Store) Append(e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	e.Seq = s.seq
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("jobstore: marshal event: %w", err)
	}
	if _, err := s.f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("jobstore: write event: %w", err)
	}
	return s.f.Sync()
}

// Load reads every event and folds them to the current records.
func (s *Store) Load() (map[string]*JobRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events, err := s.readAllLocked()
	if err != nil {
		return nil, err
	}
	return Fold(events), nil
}

// readAll is the locked-public test/helper variant of readAllLocked.
func (s *Store) readAll() ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readAllLocked()
}

func (s *Store) readAllLocked() ([]Event, error) {
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("jobstore: read %s: %w", s.path, err)
	}
	defer f.Close()
	var events []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("jobstore: parse event line: %w", err)
		}
		events = append(events, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: scan %s: %w", s.path, err)
	}
	return events, nil
}

// Close closes the underlying file.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Close()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd agent && go test ./internal/jobstore/ -run TestStore -v`
Expected: PASS (all three).

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/internal/jobstore/store.go agent/internal/jobstore/store_test.go
git commit -m "feat(jobstore): append-only store with seq recovery"
```

---

## Task 5: Per-job output store (append / tail / grep)

**Files:**
- Create: `agent/internal/jobstore/output.go`
- Test: `agent/internal/jobstore/output_test.go`

A bounded per-job `.log`. `Append` returns bytes written and tracks total. `Tail` returns the last N bytes plus the total and a truncated flag. `Grep` runs an RE2 regex over retained content, returning matching lines with their byte offset. Pruned-but-recorded output is reported via a sentinel error so the caller can surface `retention_pruned` (the model-facing translation happens in a later phase).

- [ ] **Step 1: Write the failing test**

Create `agent/internal/jobstore/output_test.go`:

```go
package jobstore

import (
	"path/filepath"
	"regexp"
	"testing"
)

func TestOutputAppendAndTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, err := OpenOutput(path, 1024)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, _ = o.Append([]byte("line1\n"))
	_, _ = o.Append([]byte("line2\n"))

	data, total, truncated, err := o.Tail(1024)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if string(data) != "line1\nline2\n" {
		t.Errorf("tail = %q", data)
	}
	if total != 12 || truncated {
		t.Errorf("total=%d truncated=%v, want 12/false", total, truncated)
	}
}

func TestOutputTailTruncatesToLastBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, _ := OpenOutput(path, 1<<20)
	_, _ = o.Append([]byte("aaaaaXXXXX")) // 10 bytes
	data, total, truncated, _ := o.Tail(4)
	if string(data) != "XXXX" {
		t.Errorf("tail(4) = %q, want last 4 bytes", data)
	}
	if total != 10 || !truncated {
		t.Errorf("total=%d truncated=%v, want 10/true", total, truncated)
	}
}

func TestOutputGrepReturnsMatchesWithOffsets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, _ := OpenOutput(path, 1<<20)
	_, _ = o.Append([]byte("starting\nserver ready\ndone\n"))
	re := regexp.MustCompile(`(?i)ready`)
	matches, err := o.Grep(re, 1<<16)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if len(matches) != 1 || matches[0].Line != "server ready" {
		t.Fatalf("matches = %+v", matches)
	}
	if matches[0].ByteOffset != int64(len("starting\n")) {
		t.Errorf("byte offset = %d, want %d", matches[0].ByteOffset, len("starting\n"))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test ./internal/jobstore/ -run TestOutput -v`
Expected: FAIL to compile — `undefined: OpenOutput`.

- [ ] **Step 3: Write minimal implementation**

Create `agent/internal/jobstore/output.go`:

```go
package jobstore

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sync"
)

// ErrOutputPruned is returned by output reads when the durable record remains
// but the bytes were pruned by retention policy. Callers translate this to the
// model-facing output_unavailable / retention_pruned signal in a later phase.
var ErrOutputPruned = errors.New("jobstore: output pruned")

// Match is one grep hit: the matching line and its byte offset in the log.
type Match struct {
	ByteOffset int64  `json:"byte_offset"`
	Line       string `json:"line"`
}

// OutputStore is a bounded append-only per-job output file.
type OutputStore struct {
	mu       sync.Mutex
	path     string
	f        *os.File
	capBytes int64
	total    int64
}

// OpenOutput opens (creating if needed) the per-job log at path with a soft cap.
func OpenOutput(path string, capBytes int64) (*OutputStore, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("jobstore: open output %s: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &OutputStore{path: path, f: f, capBytes: capBytes, total: info.Size()}, nil
}

// Append writes b to the log and returns the number of bytes written.
func (o *OutputStore) Append(b []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n, err := o.f.Write(b)
	o.total += int64(n)
	if err != nil {
		return n, fmt.Errorf("jobstore: append output: %w", err)
	}
	return n, nil
}

// Tail returns the last maxBytes bytes of the log, the total byte count, and
// whether the returned slice is a truncated tail of a larger log.
func (o *OutputStore) Tail(maxBytes int) ([]byte, int64, bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	info, err := os.Stat(o.path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("jobstore: stat output: %w", err)
	}
	total := info.Size()
	start := int64(0)
	truncated := false
	if total > int64(maxBytes) {
		start = total - int64(maxBytes)
		truncated = true
	}
	f, err := os.Open(o.path)
	if err != nil {
		return nil, total, truncated, fmt.Errorf("jobstore: open output: %w", err)
	}
	defer f.Close()
	if _, err := f.Seek(start, 0); err != nil {
		return nil, total, truncated, err
	}
	buf := make([]byte, total-start)
	if _, err := f.Read(buf); err != nil && len(buf) > 0 {
		return nil, total, truncated, fmt.Errorf("jobstore: read output: %w", err)
	}
	return buf, total, truncated, nil
}

// Grep scans the log line by line and returns up to limitBytes worth of lines
// matching re, each with its byte offset.
func (o *OutputStore) Grep(re *regexp.Regexp, limitBytes int) ([]Match, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	f, err := os.Open(o.path)
	if err != nil {
		return nil, fmt.Errorf("jobstore: open output: %w", err)
	}
	defer f.Close()
	var matches []Match
	var offset int64
	budget := limitBytes
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if re.MatchString(line) {
			matches = append(matches, Match{ByteOffset: offset, Line: line})
			budget -= len(line)
			if budget <= 0 {
				break
			}
		}
		offset += int64(len(line)) + 1 // +1 for the newline the scanner stripped
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: scan output: %w", err)
	}
	return matches, nil
}

// Close closes the underlying file.
func (o *OutputStore) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.f.Close()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd agent && go test ./internal/jobstore/ -run TestOutput -v`
Expected: PASS (all three).

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/internal/jobstore/output.go agent/internal/jobstore/output_test.go
git commit -m "feat(jobstore): bounded per-job output store (append/tail/grep)"
```

---

## Task 6: Terminal-generation + dedupe key + deliverability

**Files:**
- Create: `agent/internal/jobstore/notify.go`
- Test: `agent/internal/jobstore/notify_test.go`

`terminal_generation` is a fresh ULID minted once at finalize; the dedupe key is `(visible_session_id, job_id, terminal_generation)`; `ShouldDeliver` reports whether a record's terminal notification still needs injecting (`NotifyPending`).

- [ ] **Step 1: Write the failing test**

Create `agent/internal/jobstore/notify_test.go`:

```go
package jobstore

import "testing"

func TestNewTerminalGenerationUnique(t *testing.T) {
	a := NewTerminalGeneration()
	b := NewTerminalGeneration()
	if a == "" || a == b {
		t.Errorf("terminal generations should be non-empty and unique: %q %q", a, b)
	}
}

func TestDedupeKeyComposition(t *testing.T) {
	r := &JobRecord{JobID: "job_A", VisibleToSession: "S1", TerminalGen: "GEN1"}
	k := r.DedupeKey()
	if k != (DedupeKey{VisibleSessionID: "S1", JobID: "job_A", TerminalGen: "GEN1"}) {
		t.Errorf("dedupe key = %+v", k)
	}
}

func TestShouldDeliver(t *testing.T) {
	cases := []struct {
		state NotifyState
		want  bool
	}{
		{NotifyNotArmed, false},
		{NotifyPending, true},
		{NotifyDelivered, false},
	}
	for _, c := range cases {
		r := &JobRecord{NotifyState: c.state}
		if got := ShouldDeliver(r); got != c.want {
			t.Errorf("ShouldDeliver(%q) = %v, want %v", c.state, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test ./internal/jobstore/ -run 'TestNewTerminalGeneration|TestDedupeKey|TestShouldDeliver' -v`
Expected: FAIL to compile — `undefined: NewTerminalGeneration`, etc.

- [ ] **Step 3: Write minimal implementation**

Create `agent/internal/jobstore/notify.go`:

```go
package jobstore

import "github.com/oklog/ulid/v2"

// NewTerminalGeneration mints the stable identity of a job's first terminal
// event. It is minted once at finalize and copied verbatim onto the job's
// pending/delivered notification events (never re-derived from the event seq).
func NewTerminalGeneration() string {
	return ulid.Make().String()
}

// DedupeKey is the durable terminal-notification dedupe identity.
type DedupeKey struct {
	VisibleSessionID string
	JobID            string
	TerminalGen      string
}

// DedupeKey returns the record's terminal-notification dedupe key.
func (r *JobRecord) DedupeKey() DedupeKey {
	return DedupeKey{
		VisibleSessionID: r.VisibleToSession,
		JobID:            r.JobID,
		TerminalGen:      r.TerminalGen,
	}
}

// ShouldDeliver reports whether the record's terminal notification still needs
// to be injected into the visible session.
func ShouldDeliver(r *JobRecord) bool {
	return r.NotifyState == NotifyPending
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd agent && go test ./internal/jobstore/ -run 'TestNewTerminalGeneration|TestDedupeKey|TestShouldDeliver' -v`
Expected: PASS (all three).

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/internal/jobstore/notify.go agent/internal/jobstore/notify_test.go
git commit -m "feat(jobstore): terminal-generation, dedupe key, deliverability"
```

---

## Task 7: Restart reconciliation (running-without-runtime → stopped/runtime_lost)

**Files:**
- Create: `agent/internal/jobstore/reconcile.go`
- Test: `agent/internal/jobstore/reconcile_test.go`

`Reconcile` is pure: given the folded records, the set of job ids with a live in-memory runtime, and a clock, it returns the `job_finished` events to append for jobs that are `running` but have no live runtime — finalizing each exactly once as `stopped/runtime_lost` with a freshly minted `terminal_generation`. Already-terminal jobs and live jobs produce nothing.

- [ ] **Step 1: Write the failing test**

Create `agent/internal/jobstore/reconcile_test.go`:

```go
package jobstore

import (
	"testing"
	"time"
)

func TestReconcileFinalizesLostRunningJob(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	recs := map[string]*JobRecord{
		"job_live": {JobID: "job_live", Status: StatusRunning, VisibleToSession: "S1"},
		"job_lost": {JobID: "job_lost", Status: StatusRunning, VisibleToSession: "S1"},
		"job_done": {JobID: "job_done", Status: StatusCompleted, VisibleToSession: "S1"},
	}
	live := map[string]bool{"job_live": true}

	events := Reconcile(recs, live, now)

	if len(events) != 1 {
		t.Fatalf("expected exactly 1 reconcile event, got %d: %+v", len(events), events)
	}
	e := events[0]
	if e.JobID != "job_lost" || e.Kind != EventJobFinished {
		t.Errorf("wrong job finalized: %+v", e)
	}
	if e.Status != StatusStopped || e.Reason != "runtime_lost" {
		t.Errorf("status/reason = %q/%q, want stopped/runtime_lost", e.Status, e.Reason)
	}
	if e.TerminalGen == "" {
		t.Errorf("reconcile event must carry a minted terminal_generation")
	}
	if e.EndedAt == nil || !e.EndedAt.Equal(now) {
		t.Errorf("ended_at = %v, want %v", e.EndedAt, now)
	}
}

func TestReconcileIsIdempotentOnSecondPass(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	recs := map[string]*JobRecord{
		"job_lost": {JobID: "job_lost", Status: StatusRunning, VisibleToSession: "S1"},
	}
	live := map[string]bool{}

	first := Reconcile(recs, live, now)
	// Apply the first pass, then re-fold and reconcile again: no new events.
	applyEvent(recs["job_lost"], first[0])
	second := Reconcile(recs, live, now)
	if len(second) != 0 {
		t.Errorf("second reconcile should be a no-op, got %+v", second)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test ./internal/jobstore/ -run TestReconcile -v`
Expected: FAIL to compile — `undefined: Reconcile`.

- [ ] **Step 3: Write minimal implementation**

Create `agent/internal/jobstore/reconcile.go`:

```go
package jobstore

import (
	"sort"
	"time"
)

// Reconcile finalizes records whose durable state is running but which have no
// live in-memory runtime, returning one job_finished event per such job
// (stopped/runtime_lost, with a freshly minted terminal_generation). Records
// that are already terminal, or whose job id is in liveJobIDs, produce nothing.
// The returned events are sorted by job id for deterministic output.
func Reconcile(records map[string]*JobRecord, liveJobIDs map[string]bool, now time.Time) []Event {
	var lost []string
	for id, r := range records {
		if r.Status == StatusRunning && !liveJobIDs[id] {
			lost = append(lost, id)
		}
	}
	sort.Strings(lost)

	events := make([]Event, 0, len(lost))
	for _, id := range lost {
		ended := now
		events = append(events, Event{
			Kind:        EventJobFinished,
			JobID:       id,
			Status:      StatusStopped,
			Reason:      "runtime_lost",
			EndedAt:     &ended,
			TerminalGen: NewTerminalGeneration(),
		})
	}
	return events
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd agent && go test ./internal/jobstore/ -run TestReconcile -v`
Expected: PASS (both).

- [ ] **Step 5: Run the whole package + commit**

Run: `cd agent && go test ./internal/jobstore/ -v`
Expected: PASS (every test in the package).

Then run the module's lint to confirm the new package is clean. Run: `cd /Users/jesse/prime-radiant/toil-suite/serf && make lint` (or, if that is slow, `cd agent && go vet ./internal/jobstore/`).
Expected: no new findings in `internal/jobstore`.

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/internal/jobstore/reconcile.go agent/internal/jobstore/reconcile_test.go
git commit -m "feat(jobstore): restart reconciliation (running -> stopped/runtime_lost)"
```

---

## Phase roadmap (the remaining 5 plans)

Phase 1 (this plan) ships the tested substrate. Each later phase gets its own plan written the same way (bite-sized TDD), and each produces working, testable software. During Phases 2–5 the new job tools are registered **alongside** the legacy subagent tools (a temporary parallel surface for build safety); Phase 6 removes the legacy surface so the end state has no residue.

- **Phase 2 — Shell jobs end-to-end + notifications + restart wiring.** `execenv` `StreamingExecutor` optional interface; `agent/job_shell.go` (stream-to-log, foreground/ephemeral/promotion/background, `max_runtime_ms`); `agent/jobs.go` JobManager (create/list/read/stop for shell, in-memory running overlay over the `jobstore`); `agent/job_notify.go` (durable arm → `EntryNotification` injection, `<job-notification>`, the `filterDeliverableNotifications` durable-record keying); `session_init.go` reconciliation wiring; reworked `DefShell` + `job_read_output`/`job_list`/`job_stop` tools. First user-visible slice.
- **Phase 3 — Delegate jobs + `job_send_message`.** Reuse the child-session runtime under job records; `delegate` tool; the `result_schema`→`structured_result` **capture** (preserve raw `communicate.output` past `normalizeNodeOutput`; enforcement is free via the call-boundary validator); `job_send_message` (live inject / resume / alias targets, with the role-gated target rule).
- **Phase 4 — Watches + observer sidecars.** `jobstore/watch.go` pure `output_match` matcher; JobManager-side `events`/`trigger` event-frame gating + `send` delivery; `job_watch` tool; observer-sidecar composition (alias-target `job_send_message`).
- **Phase 5 — Nested shell jobs.** `parent_job_id` forwarding into parent-visible records; `include_nested`; parent read/stop routing; cross-store `terminal_generation` propagation.
- **Phase 6 — Cutover (no legacy residue).** Delete the 7 legacy tool defs + handlers; repoint every consumer the spec §13 inventory names (`toolname`, `tool/registry` truncation, `contextmgr` compaction, serf-tui renderers, serf-hub JS assets, `appprojector`→`serf/subagent/*` wire, `server` snapshot, the `<subagent-notification>` formatter, the events/snapshot chain); retire `docs/subagent-management/00`; run the §13 `rg` gate + `make build`/`make test` to green.

---

## Self-review (run against the spec — completed during authoring)

- **Spec coverage (Phase 1 scope = spec §3 data model, §6 notification bookkeeping, §7 reconciliation):** `JobRecord` + enums (Task 1) ↔ §3.2; job events (Task 2) + fold (Task 3) ↔ §3.3 incl. first-terminal-wins `terminal_generation`; append-only store with seq recovery (Task 4) ↔ §3.3 "mirror the transcript writer"; per-job output append/tail/grep + `retention_pruned` sentinel (Task 5) ↔ §3.4; `terminal_generation`/dedupe key/deliverability (Task 6) ↔ §6; reconciliation (Task 7) ↔ §7. The model-facing null-projection (`reason`/`resumable` as `null`/`false`) is deferred to the tool-handler phases (it is a wire-projection concern, not storage) — noted so it is not lost.
- **Placeholder scan:** none — every step has complete Go and an exact run command.
- **Type consistency:** `Event`/`JobRecord`/`Status`/`EventKind`/`NotifyState` are defined in Tasks 1–2 and used consistently in Tasks 3–7; `Fold`/`applyEvent` (Task 3) are reused by `Reconcile`'s idempotency test (Task 7); `Store.readAll` (Task 4 test helper) is defined in Task 4. `Match`/`OutputStore` (Task 5) are self-contained.
- **Determinism:** tests avoid wall-clock/random assertions — IDs/generations are checked for prefix/uniqueness only; `Reconcile` takes `now` as a parameter; `Fold`/`Reconcile` sort for stable output.
