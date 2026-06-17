package main

import (
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

func TestArchiveDecisionsFlowIntoTree(t *testing.T) {
	dir := t.TempDir()
	store := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
	if err := store.Set("project", "alpha", true, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	got, err := store.Decisions()
	if err != nil || !got[hubcore.ArchiveKey{Kind: "project", ID: "alpha"}] {
		t.Fatalf("decision not stored: %v %v", got, err)
	}
}

func TestArchiveDecisionsHelperNilSafe(t *testing.T) {
	// A WebServer whose cfg.Archive is nil must return an empty map, never panic.
	s := &WebServer{cfg: hubcore.WebConfig{}}
	got := s.archiveDecisions()
	if got == nil {
		t.Fatal("archiveDecisions() returned nil; want empty map")
	}
	if len(got) != 0 {
		t.Fatalf("archiveDecisions() returned %v; want empty map", got)
	}
}

func TestArchiveDecisionsHelperWithStore(t *testing.T) {
	dir := t.TempDir()
	store := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
	if err := store.Set("project", "beta", true, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	s := &WebServer{cfg: hubcore.WebConfig{Archive: store}}
	got := s.archiveDecisions()
	if !got[hubcore.ArchiveKey{Kind: "project", ID: "beta"}] {
		t.Fatalf("archiveDecisions() missing expected decision; got %v", got)
	}
}
