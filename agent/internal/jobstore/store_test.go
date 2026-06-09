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
