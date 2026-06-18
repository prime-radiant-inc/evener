package jobstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreAppendThenLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	s := openStore(t, path)
	start := time.Unix(1, 0).UTC()
	appendEvent(t, s, Event{Kind: EventJobStarted, JobID: "job_A", Type: JobShell, OwnerSessionID: "S1", VisibleToSession: "S1", StartedAt: &start})
	appendEvent(t, s, Event{Kind: EventJobFinished, JobID: "job_A", Status: StatusCompleted, TerminalGen: "GEN1"})
	recs, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if recs["job_A"].Status != StatusCompleted {
		t.Errorf("status = %q, want completed", recs["job_A"].Status)
	}
}

func TestStoreLoadDelegates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s := openStore(t, path)
	start := time.Unix(1, 0).UTC()
	appendEvent(t, s, Event{
		Kind:       EventDelegateCreated,
		DelegateID: "dlg_A",
		Delegate: &DelegateEvent{
			ChildSessionID: "child_A",
			TranscriptRef:  "local:child_A",
			Generation:     "dg_1",
			Resumable:      true,
		},
	})
	appendEvent(t, s, Event{
		Kind:       EventJobStarted,
		JobID:      "job_A",
		Type:       JobDelegate,
		DelegateID: "dlg_A",
		StartedAt:  &start,
	})

	delegates, err := s.LoadDelegates()
	if err != nil {
		t.Fatalf("load delegates: %v", err)
	}
	d := delegates["dlg_A"]
	if d == nil {
		t.Fatal("delegate dlg_A missing")
	}
	if d.CurrentJobID != "job_A" || d.LatestJobID != "job_A" || d.Status != DelegateRunning {
		t.Fatalf("delegate = %+v, want running job_A", d)
	}
}

func TestStoreAssignsMonotonicSeq(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s := openStore(t, path)
	appendEvent(t, s, Event{Kind: EventJobStarted, JobID: "job_A"})
	appendEvent(t, s, Event{Kind: EventJobStarted, JobID: "job_B"})
	raw := readAllEvents(t, s)
	if len(raw) != 2 || raw[0].Seq != 1 || raw[1].Seq != 2 {
		t.Errorf("seqs not monotonic from 1: %+v", raw)
	}
}

func TestStoreRecoversSeqAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s1 := openStore(t, path)
	appendEvent(t, s1, Event{Kind: EventJobStarted, JobID: "job_A"})
	closeStore(t, s1)

	s2, err := Open(path) // reopen: must continue seq at 2, not restart at 1
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	appendEvent(t, s2, Event{Kind: EventJobFinished, JobID: "job_A", Status: StatusCompleted})
	raw := readAllEvents(t, s2)
	if raw[len(raw)-1].Seq != 2 {
		t.Errorf("seq after reopen = %d, want 2", raw[len(raw)-1].Seq)
	}
}

func TestStoreOpenTerminatesValidTrailingEventBeforeAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	first, err := json.Marshal(Event{Kind: EventJobStarted, Seq: 1, JobID: "job_A"})
	if err != nil {
		t.Fatalf("marshal first event: %v", err)
	}
	if err := os.WriteFile(path, first, 0o644); err != nil {
		t.Fatalf("write unterminated store: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	appendEvent(t, s, Event{Kind: EventJobStarted, JobID: "job_B"})
	raw := readAllEvents(t, s)
	if len(raw) != 2 {
		t.Fatalf("events after recovery and append = %+v, want two events", raw)
	}
	if raw[0].JobID != "job_A" || raw[1].JobID != "job_B" {
		t.Fatalf("events after recovery and append = %+v, want job_A then job_B", raw)
	}

	recovered, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recovered store: %v", err)
	}
	if !bytes.Contains(recovered, []byte("}\n{")) || recovered[len(recovered)-1] != '\n' {
		t.Fatalf("recovered store = %q, want JSONL-delimited events", recovered)
	}
}

func TestStoreDoesNotAdvanceSeqAfterFailedAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s := openStore(t, path)
	appendEvent(t, s, Event{Kind: EventJobStarted, JobID: "job_A"})
	if err := s.f.Close(); err != nil {
		t.Fatalf("close underlying file: %v", err)
	}

	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_B"}); err == nil {
		t.Fatal("append after underlying file close succeeded, want error")
	}
	if s.seq != 1 {
		t.Fatalf("seq after failed append = %d, want 1", s.seq)
	}

	reopened := openStore(t, path)
	appendEvent(t, reopened, Event{Kind: EventJobStarted, JobID: "job_C"})
	raw := readAllEvents(t, reopened)
	if raw[len(raw)-1].Seq != 2 {
		t.Errorf("seq after failed append and reopen = %d, want 2", raw[len(raw)-1].Seq)
	}
}

func TestStoreRollbackTruncatesTouchedAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s := openStore(t, path)
	appendEvent(t, s, Event{Kind: EventJobStarted, JobID: "job_A"})

	s.mu.Lock()
	startOffset, err := s.f.Seek(0, io.SeekEnd)
	if err != nil {
		s.mu.Unlock()
		t.Fatalf("seek eof: %v", err)
	}
	if _, err := s.f.WriteString("{partial"); err != nil {
		s.mu.Unlock()
		t.Fatalf("write partial append: %v", err)
	}
	writeErr := errors.New("write failed")
	err = s.appendFailureLocked("write event", writeErr, startOffset)
	s.mu.Unlock()
	if !errors.Is(err, writeErr) {
		t.Fatalf("rollback error = %v, want wrapped write error", err)
	}

	raw := readAllEvents(t, s)
	if len(raw) != 1 || raw[0].Seq != 1 {
		t.Fatalf("rollback left unexpected events: %+v", raw)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != startOffset {
		t.Fatalf("size after rollback = %d, want %d", info.Size(), startOffset)
	}
}

func TestStoreRejectsOperationsAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s := openStore(t, path)
	appendEvent(t, s, Event{Kind: EventJobStarted, JobID: "job_A"})
	closeStore(t, s)

	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_B"}); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("append after close error = %v, want %v", err, ErrStoreClosed)
	}
	if _, err := s.Load(); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("load after close error = %v, want %v", err, ErrStoreClosed)
	}
	if _, err := s.readAll(); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("readAll after close error = %v, want %v", err, ErrStoreClosed)
	}
	if err := s.Close(); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("second close error = %v, want %v", err, ErrStoreClosed)
	}

	reopened := openStore(t, path)
	raw := readAllEvents(t, reopened)
	if len(raw) != 1 || raw[0].Seq != 1 {
		t.Fatalf("closed append changed file: %+v", raw)
	}
}

func TestStoreParseErrorIncludesLineNumber(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	data := []byte("{\"kind\":\"job_started\",\"seq\":1,\"job_id\":\"job_A\"}\n{bad json}\n")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write corrupt store: %v", err)
	}

	_, err := Open(path)
	if err == nil {
		t.Fatal("open corrupt store succeeded, want error")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("parse error = %q, want line number", err)
	}
}

func TestStoreOpenTruncatesTrailingPartialLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	data := []byte("{\"kind\":\"job_started\",\"seq\":1,\"job_id\":\"job_A\"}\n{\"kind\":\"job_finished\"")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write store: %v", err)
	}

	s := openStore(t, path)
	raw := readAllEvents(t, s)
	if len(raw) != 1 || raw[0].JobID != "job_A" {
		t.Fatalf("events after recovery = %+v, want first event only", raw)
	}
	recovered, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recovered store: %v", err)
	}
	if string(recovered) != "{\"kind\":\"job_started\",\"seq\":1,\"job_id\":\"job_A\"}\n" {
		t.Fatalf("recovered store = %q, want partial line truncated", recovered)
	}
}

func TestStoreOpenTruncatesWholeFileTrailingPartialLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	data := []byte("{\"kind\":\"job_started\",\"seq\":1")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write store: %v", err)
	}

	s := openStore(t, path)
	raw := readAllEvents(t, s)
	if len(raw) != 0 {
		t.Fatalf("events after recovery = %+v, want none", raw)
	}
	recovered, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recovered store: %v", err)
	}
	if len(recovered) != 0 {
		t.Fatalf("recovered store = %q, want empty file", recovered)
	}
}

func TestStoreOpenTruncatesTrailingPartialJSONCuts(t *testing.T) {
	prefix := "{\"kind\":\"job_started\",\"seq\":1,\"job_id\":\"job_A\"}\n"
	for _, tc := range []struct {
		name    string
		partial string
	}{
		{
			name:    "boolean",
			partial: "{\"kind\":\"job_session_assigned\",\"seq\":2,\"job_id\":\"job_A\",\"resumable\":tru",
		},
		{
			name:    "number",
			partial: "{\"kind\":\"job_finished\",\"seq\":2,\"job_id\":\"job_A\",\"output_bytes\":12e",
		},
		{
			name:    "string",
			partial: "{\"kind\":\"job_started\",\"seq\":2,\"job_id\":\"job_",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "jobs.jsonl")
			data := []byte(prefix + tc.partial)
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatalf("write store: %v", err)
			}

			s := openStore(t, path)
			raw := readAllEvents(t, s)
			if len(raw) != 1 || raw[0].JobID != "job_A" {
				t.Fatalf("events after recovery = %+v, want first event only", raw)
			}
			recovered, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read recovered store: %v", err)
			}
			if string(recovered) != prefix {
				t.Fatalf("recovered store = %q, want partial line truncated", recovered)
			}
		})
	}
}

func TestStoreOpenPreservesCorruptCompleteLineError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	data := []byte("{\"kind\":\"job_started\",\"seq\":1,\"job_id\":\"job_A\"}\n{bad json}\n")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write store: %v", err)
	}

	_, err := Open(path)
	if err == nil {
		t.Fatal("open corrupt store succeeded, want error")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("parse error = %q, want line number", err)
	}
}

func TestStoreOpenPreservesCorruptTrailingLineError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	data := []byte("{\"kind\":\"job_started\",\"seq\":1,\"job_id\":\"job_A\"}\n{\"kind\":}")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write store: %v", err)
	}

	_, err := Open(path)
	if err == nil {
		t.Fatal("open corrupt trailing store succeeded, want error")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("parse error = %q, want line number", err)
	}
	recovered, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read recovered store: %v", readErr)
	}
	if !bytes.Equal(recovered, data) {
		t.Fatalf("recovered store = %q, want corrupt trailing line preserved", recovered)
	}
}

func openStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

func appendEvent(t *testing.T, s *Store, e Event) {
	t.Helper()
	if err := s.Append(e); err != nil {
		t.Fatalf("append %s/%s: %v", e.Kind, e.JobID, err)
	}
}

func readAllEvents(t *testing.T, s *Store) []Event {
	t.Helper()
	raw, err := s.readAll()
	if err != nil {
		t.Fatalf("readAll: %v", err)
	}
	return raw
}

func closeStore(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
