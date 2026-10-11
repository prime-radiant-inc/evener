package jobstore

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/identifier"
)

func TestGeneratedJobstoreIDsUseIdentifierDomains(t *testing.T) {
	const owner = "02wMz5TxvEMoJEDTDGOTil"
	tests := []struct {
		name     string
		newID    func() string
		validate func(string) error
		prefix   string
	}{
		{"job", func() string { return NewJobID(owner) }, identifier.ValidateJobID, "job_"},
		{"watch", NewWatchID, identifier.ValidateWatchID, "watch_"},
		{"watch generation", NewWatchGeneration, identifier.ValidateWatchGeneration, "wg_"},
		{"watch delivery", NewWatchSendDeliveryID, identifier.ValidateWatchDeliveryID, "wd_"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.newID()
			if !strings.HasPrefix(got, tt.prefix) {
				t.Fatalf("ID = %q, want prefix %s", got, tt.prefix)
			}
			if err := tt.validate(got); err != nil {
				t.Fatalf("validate %q: %v", got, err)
			}
		})
	}
}

// TestWatchSendState_TimestampsAlwaysShipOnWire locks in that CreatedAt and
// UpdatedAt have no "omitempty" tag: encoding/json can never omit a struct
// value regardless of the tag, so both keys ship even for the zero
// time.Time. WatchSendState is purely internal durable job-store state (no
// external wire consumer decodes for key absence), so the tag was already a
// no-op lie.
func TestWatchSendState_TimestampsAlwaysShipOnWire(t *testing.T) {
	data, err := json.Marshal(WatchSendState{})
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, `"created_at":`) {
		t.Errorf("expected created_at key present even for zero time.Time, got %s", got)
	}
	if !strings.Contains(got, `"updated_at":`) {
		t.Errorf("expected updated_at key present even for zero time.Time, got %s", got)
	}
}

// TestJobRecord_BackgroundAndPhaseStayOffTheWire keeps the JobRecord JSON
// shape unchanged. An unmarked journal start carries no background evidence;
// Phase remains live-only.
func TestJobRecord_BackgroundAndPhaseStayOffTheWire(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	folded := Fold([]Event{
		ev(EventJobStarted, 1, "job_bg", func(e *Event) {
			e.Type = JobShell
			e.Command = "npm run dev"
			e.OwnerSessionID = "S1"
			e.VisibleToSession = "S1"
			e.StartedAt = &start
		}),
	})["job_bg"]
	if folded == nil {
		t.Fatal("expected record for job_bg")
	}
	if folded.Background || folded.Phase != "" {
		t.Fatalf("folded record claims background/phase the log never carried: %+v", folded)
	}

	live := *folded
	live.Background = true
	live.Phase = "process_running"
	b, err := json.Marshal(live)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	if _, ok := wire["background"]; ok {
		t.Errorf("background reached the wire: %s", b)
	}
	if _, ok := wire["phase"]; ok {
		t.Errorf("phase reached the wire: %s", b)
	}
	if !live.Background || live.Phase != "process_running" {
		t.Errorf("live record lost its in-memory background/phase: %+v", live)
	}
}

func TestFoldBackgroundEvidenceSurvivesTerminal(t *testing.T) {
	var start Event
	if err := json.Unmarshal([]byte(`{"kind":"job_started","seq":1,"job_id":"bg","type":"shell","background":true}`), &start); err != nil {
		t.Fatal(err)
	}
	finished := Event{Kind: EventJobFinished, Seq: 2, JobID: "bg", Status: StatusFailed, TerminalGen: "first"}
	duplicateStart := Event{Kind: EventJobStarted, Seq: 3, JobID: "bg", Type: JobShell}
	duplicateFinish := Event{Kind: EventJobFinished, Seq: 4, JobID: "bg", Status: StatusCompleted, TerminalGen: "duplicate"}
	events := []Event{start, finished, duplicateStart, duplicateFinish}
	assertRecord := func(t *testing.T, record *JobRecord) {
		t.Helper()
		if record == nil || !record.Background || record.Status != StatusFailed || record.TerminalGen != "first" {
			t.Fatalf("fold lost background evidence or first terminal outcome: %+v", record)
		}
	}
	t.Run("full fold", func(t *testing.T) { assertRecord(t, Fold(events)["bg"]) })
	t.Run("incremental apply", func(t *testing.T) {
		records := make(map[string]*JobRecord)
		for _, event := range events {
			Apply(records, nil, event)
		}
		assertRecord(t, records["bg"])
	})
	t.Run("event JSON round trip", func(t *testing.T) {
		data, err := json.Marshal(start)
		if err != nil {
			t.Fatal(err)
		}
		var roundTrip Event
		if err := json.Unmarshal(data, &roundTrip); err != nil {
			t.Fatal(err)
		}
		assertRecord(t, Fold([]Event{roundTrip, finished})["bg"])
	})
	t.Run("unmarked history", func(t *testing.T) {
		var unmarked Event
		if err := json.Unmarshal([]byte(`{"kind":"job_started","seq":1,"job_id":"old","type":"shell"}`), &unmarked); err != nil {
			t.Fatal(err)
		}
		if record := Fold([]Event{unmarked})["old"]; record == nil || record.Background {
			t.Fatalf("unmarked history claimed background evidence: %+v", record)
		}
	})
}

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

func TestStatusIsTerminal_Exhausted(t *testing.T) {
	if !StatusExhausted.IsTerminal() {
		t.Fatal("exhausted status is not terminal")
	}
}

func TestNewJobIDFormatAndUniqueness(t *testing.T) {
	const owner = "02wMz5TxvEMoJEDTDGOTil"
	a := NewJobID(owner)
	b := NewJobID(owner)
	if !strings.HasPrefix(a, "job_") {
		t.Errorf("job id %q should start with job_", a)
	}
	if a == b {
		t.Errorf("two job ids should differ: %q == %q", a, b)
	}
	if len(a) != len("job_")+22+1+12 {
		t.Errorf("job id %q has unexpected length %d", a, len(a))
	}
}
