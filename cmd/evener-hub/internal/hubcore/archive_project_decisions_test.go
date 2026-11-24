package hubcore

import (
	"path/filepath"
	"testing"
	"time"
)

// DecisionStates returns every explicit decision with the instant it was made:
// the launch rule needs that time, which Decisions' bool cannot carry.
func TestDecisionStatesCarryTheDecisionTime(t *testing.T) {
	s := closeOnCleanup(t, NewArchiveStore(filepath.Join(t.TempDir(), "index.db")))
	archived := time.Unix(1_787_158_316, 0)
	unarchived := archived.Add(time.Hour)
	if err := s.Set("", "project", "p-archived", true, archived); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("", "project", "p-unarchived", false, archived); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("", "session", "s-archived", true, archived); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("host-a", "project", "p-archived", true, unarchived); err != nil {
		t.Fatal(err)
	}

	got, err := s.DecisionStates()
	if err != nil {
		t.Fatal(err)
	}
	want := map[ArchiveKey]ArchiveDecision{
		ProjectDecisionKey("", "p-archived"):       {Archived: true, DecidedAt: archived},
		ProjectDecisionKey("", "p-unarchived"):     {Archived: false, DecidedAt: archived},
		{Kind: "session", ID: "s-archived"}:        {Archived: true, DecidedAt: archived},
		ProjectDecisionKey("host-a", "p-archived"): {Archived: true, DecidedAt: unarchived},
	}
	if len(got) != len(want) {
		t.Fatalf("DecisionStates = %v, want %v", got, want)
	}
	for key, decision := range want {
		state, ok := got[key]
		if !ok || state.Archived != decision.Archived || !state.DecidedAt.Equal(decision.DecidedAt) {
			t.Errorf("%v: state = (%+v, present=%t), want %+v", key, state, ok, decision)
		}
	}
}

// SetIfArchived unarchives a row only while it still archives, so a decision
// written after the caller's read survives.
func TestSetIfArchivedLeavesAnUnarchivedDecisionAlone(t *testing.T) {
	s := closeOnCleanup(t, NewArchiveStore(filepath.Join(t.TempDir(), "index.db")))
	key := ProjectDecisionKey("", "p")
	decided := time.Unix(1_787_158_316, 0)
	if err := s.Set(key.Source, key.Kind, key.ID, false, decided); err != nil {
		t.Fatal(err)
	}
	changed, err := s.SetIfArchived(key.Source, key.Kind, key.ID, decided.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("SetIfArchived changed an already unarchived row")
	}
	state, err := s.DecisionStates()
	if err != nil {
		t.Fatal(err)
	}
	if got := state[key]; got.Archived || !got.DecidedAt.Equal(decided) {
		t.Fatalf("decision = %+v, want archived=false at %v", got, decided)
	}
}

func TestSetIfArchivedUnarchivesAnArchivedRow(t *testing.T) {
	s := closeOnCleanup(t, NewArchiveStore(filepath.Join(t.TempDir(), "index.db")))
	key := ProjectDecisionKey("", "p")
	decided := time.Unix(1_787_158_316, 0)
	if err := s.Set(key.Source, key.Kind, key.ID, true, decided); err != nil {
		t.Fatal(err)
	}
	changed, err := s.SetIfArchived(key.Source, key.Kind, key.ID, decided.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("SetIfArchived did not change an archived row")
	}
	state, err := s.DecisionStates()
	if err != nil {
		t.Fatal(err)
	}
	if got := state[key]; got.Archived {
		t.Fatalf("decision = %+v, want the row unarchived", got)
	}
}
