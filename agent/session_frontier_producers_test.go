package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

type frontierFileGate struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newFrontierFileGate(t *testing.T) *frontierFileGate {
	t.Helper()
	g := &frontierFileGate{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(g.resume)
	return g
}

func (g *frontierFileGate) resume() { g.once.Do(func() { close(g.release) }) }

func (g *frontierFileGate) wait() {
	close(g.entered)
	<-g.release
}

// frontierTestFS gates real file operations at the commit and recovery edges.
// The writer, session, event stream and transcript bytes remain production code.
type frontierTestFS struct {
	afero.Fs
	mu               sync.Mutex
	writeGate        *frontierFileGate
	continuationOnly bool
	recoveryGate     *frontierFileGate
	failSyncs        int
	failTruncate     bool
}

func (fs *frontierTestFS) OpenFile(name string, flags int, mode os.FileMode) (afero.File, error) {
	f, err := fs.Fs.OpenFile(name, flags, mode)
	if err != nil {
		return nil, err
	}
	return &frontierTestFile{File: f, fs: fs}, nil
}

type frontierTestFile struct {
	afero.File
	fs *frontierTestFS
}

var errFrontierFileFault = errors.New("injected frontier file operation failure")

func (f *frontierTestFile) Write(p []byte) (int, error) {
	f.fs.mu.Lock()
	gate := f.fs.writeGate
	if gate != nil && f.fs.continuationOnly {
		var entry transcript.Entry
		if err := json.NewDecoder(bytes.NewReader(p)).Decode(&entry); err != nil || entry.Turn.GoalContinuation == nil {
			gate = nil
		}
	}
	if gate != nil {
		f.fs.writeGate = nil
	}
	f.fs.mu.Unlock()
	if gate != nil {
		gate.wait()
	}
	return f.File.Write(p)
}

func (f *frontierTestFile) Sync() error {
	f.fs.mu.Lock()
	if f.fs.failSyncs > 0 {
		f.fs.failSyncs--
		f.fs.mu.Unlock()
		return errFrontierFileFault
	}
	gate := f.fs.recoveryGate
	f.fs.recoveryGate = nil
	f.fs.mu.Unlock()
	if gate != nil {
		gate.wait()
	}
	return f.File.Sync()
}

func (f *frontierTestFile) Truncate(n int64) error {
	f.fs.mu.Lock()
	fail := f.fs.failTruncate
	f.fs.failTruncate = false
	f.fs.mu.Unlock()
	if fail {
		return errFrontierFileFault
	}
	return f.File.Truncate(n)
}

func installFrontierTestWriter(t *testing.T, s *Session) *frontierTestFS {
	t.Helper()
	if err := s.closeAttachedTranscript(); err != nil {
		t.Fatal(err)
	}
	fs := &frontierTestFS{Fs: afero.NewOsFs()}
	w, _, err := transcript.OpenWriterForSessionWithFS(fs, s.TranscriptPath(), s.ID())
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.setTranscriptLocked(w)
	s.mu.Unlock()
	return fs
}

func awaitFrontierGate(t *testing.T, gate *frontierFileGate) {
	t.Helper()
	select {
	case <-gate.entered:
	case <-time.After(30 * time.Second): // TRIPWIRE: the local file barrier normally signals immediately; 30s guards a missing signal.
		t.Fatal("file operation did not reach its barrier")
	}
}

func TestGoalContinuationEventFollowsItsRecord(t *testing.T) {
	t.Parallel()
	s, _ := newExecutionSession(t)
	fs := installFrontierTestWriter(t, s)
	s.beginExecution("turn_m401")
	gate := newFrontierFileGate(t)
	fs.mu.Lock()
	fs.writeGate = gate
	fs.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.acceptContinuationInput(context.Background(), "opaque-continuation", "turn_m401")
		close(done)
	}()
	awaitFrontierGate(t, gate)
	for {
		select {
		case ev := <-s.Events():
			if ev.Kind == events.EventGoalContinuation {
				t.Error("continuation event escaped before its transcript write")
			}
		default:
			if got := s.LatestExecutionTurnID(); got != "" {
				t.Errorf("frontier before continuation write = %q", got)
			}
			gate.resume()
			<-done
			if got := s.LatestExecutionTurnID(); got != "turn_m401" {
				t.Errorf("frontier after continuation write = %q", got)
			}
			return
		}
	}
}

func TestExecutionFirstRecordSignalPrecedesRecoverySync(t *testing.T) {
	t.Parallel()
	s, _ := newExecutionSession(t)
	fs := installFrontierTestWriter(t, s)
	s.beginExecution("turn_m402")
	gate := newFrontierFileGate(t)
	fs.mu.Lock()
	fs.failSyncs = 1
	fs.failTruncate = true
	fs.recoveryGate = gate
	fs.mu.Unlock()
	type outcome struct {
		rec transcript.Record
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		rec, err := s.attachedTranscript().Record(schema.Turn{Kind: schema.TurnCheckpoint}, transcript.RecordOptions{Door: transcript.DoorSynced, Place: transcript.PlaceSession})
		done <- outcome{rec, err}
	}()
	awaitFrontierGate(t, gate)
	if got := s.LatestExecutionTurnID(); got != "turn_m402" {
		t.Errorf("frontier while recovery Sync blocked = %q", got)
	}
	for {
		select {
		case ev := <-s.Events():
			if ev.Kind != events.EventExecutionFirstRecorded {
				continue
			}
			data := ev.Data.(events.ExecutionFirstRecordedData)
			if data.TurnID != "turn_m402" {
				t.Errorf("signal execution = %q", data.TurnID)
			}
			gate.resume()
			got := <-done
			if !got.rec.Recorded || got.err != nil {
				t.Fatalf("settled retained record = %+v, %v", got.rec, got.err)
			}
			warnings := s.attachedTranscript().DrainWarnings()
			if len(warnings) != 0 {
				t.Fatalf("settled synced record left diagnostics = %v", warnings)
			}
			return
		case <-time.After(30 * time.Second): // TRIPWIRE: only queued signal delivery remains while Sync is held; 30s guards a delivery deadlock.
			t.Fatal("first-record signal waited for recovery Sync")
		}
	}
}

func TestExecutionFrontierRetainedRecordsThroughEveryDoor(t *testing.T) {
	t.Parallel()
	for _, door := range []transcript.Door{transcript.DoorBuffered, transcript.DoorDurable, transcript.DoorSynced} {
		t.Run([]string{"buffered", "durable", "synced"}[door], func(t *testing.T) {
			s, _ := newExecutionSession(t)
			fs := installFrontierTestWriter(t, s)
			w := s.attachedTranscript()
			w.SyncInterval = 0
			s.beginExecution("turn_m403")
			fs.mu.Lock()
			fs.failSyncs = 1
			if door == transcript.DoorSynced {
				fs.failSyncs = 2
			}
			fs.failTruncate = true
			fs.mu.Unlock()
			turn := schema.NewTurn(schema.TurnSteering, llm.User("opaque-retained-input"))
			var record transcript.Record
			var writeErr error
			err := s.appendTurnAfterTranscriptWrite(turn, func() error {
				record, writeErr = s.recordTranscriptLocked(turn, door, transcript.PlaceSession)
				return writeErr
			}, func() { s.history = append(s.history, turn) })
			if err != nil || !record.Recorded {
				t.Fatalf("adopted record = %+v, write %v, pair %v", record, writeErr, err)
			}
			if errors.Is(writeErr, transcript.ErrRetainedUnsynced) != (door == transcript.DoorSynced) {
				t.Fatalf("door %d retained outcome = %v", door, writeErr)
			}
			if got := s.LatestExecutionTurnID(); got != "turn_m403" {
				t.Errorf("retained frontier = %q", got)
			}
			entries := transcriptTurnsOf(t, s)
			var matching int
			for _, persisted := range entries {
				if persisted.TurnID == "turn_m403" && persisted.Kind == schema.TurnSteering {
					matching++
				}
			}
			if matching != 1 {
				t.Fatalf("adopted persisted entries = %d, want 1", matching)
			}
			s.Close()
			var signals, diagnostics int
			for ev := range s.Events() {
				switch ev.Kind {
				case events.EventExecutionFirstRecorded:
					signals++
				case events.EventWarning:
					diagnostics++
				}
			}
			if signals != 1 || diagnostics == 0 {
				t.Fatalf("retained record signals = %d, diagnostics = %d", signals, diagnostics)
			}
		})
	}
}

func TestExecutionFrontierIgnoresUnrecordedWriterOutcomes(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"nil", "closed", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := newExecutionSession(t)
			fs := installFrontierTestWriter(t, s)
			w := s.attachedTranscript()
			s.beginExecution("turn_m404")
			switch mode {
			case "nil":
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
				w = nil
			case "closed":
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
			case "rollback":
				fs.mu.Lock()
				fs.failSyncs = 1
				fs.mu.Unlock()
			}
			rec, err := w.Record(schema.Turn{Kind: schema.TurnSteering}, transcript.RecordOptions{Door: transcript.DoorDurable, Place: transcript.PlaceSession})
			if rec.Recorded || (err != nil) != (mode == "rollback") {
				t.Fatalf("unrecorded %s outcome = %+v, %v", mode, rec, err)
			}
			if got := s.LatestExecutionTurnID(); got != "" {
				t.Errorf("unrecorded %s frontier = %q", mode, got)
			}
			s.Close()
			for ev := range s.Events() {
				if ev.Kind == events.EventExecutionFirstRecorded {
					t.Fatalf("unrecorded %s emitted a first-record signal", mode)
				}
			}
		})
	}
}

func TestExecutionFrontierListenerDoesNotAcquireSessionLock(t *testing.T) {
	t.Parallel()
	s, _ := newExecutionSession(t)
	s.beginExecution("turn_m405")
	w := s.attachedTranscript()
	done := make(chan error, 1)
	s.mu.Lock()
	go func() {
		_, err := w.Record(schema.Turn{Kind: schema.TurnSteering}, transcript.RecordOptions{Place: transcript.PlaceAsync})
		done <- err
	}()
	select {
	case err := <-done:
		s.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second): // TRIPWIRE: this local record needs no held Session.mu; 30s guards a lock inversion.
		s.mu.Unlock()
		<-done
		t.Fatal("recorded listener waited for Session.mu")
	}
	if got := s.LatestExecutionTurnID(); got != "turn_m405" {
		t.Fatalf("leaf stamp = %q", got)
	}
}

func TestExecutionFrontierWithoutEventSinkDoesNotStartEmitter(t *testing.T) {
	t.Parallel()
	w, err := transcript.NewWriter(filepath.Join(t.TempDir(), "transcript.jsonl"), transcript.Header{SessionID: "frontier-no-event-sink"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	s := &Session{}
	t.Cleanup(func() { s.stopFrontierEmitter(); s.sendersWG.Wait() })
	s.attachTranscript(w, nil)
	w.BeginExecution("turn_m406", false)
	if rec, err := w.Record(schema.Turn{Kind: schema.TurnSteering}, transcript.RecordOptions{Place: transcript.PlaceAsync}); err != nil || !rec.Recorded {
		t.Fatalf("no-sink record = %+v, %v", rec, err)
	}
	if got := s.LatestExecutionTurnID(); got != "turn_m406" {
		t.Errorf("no-sink frontier = %q", got)
	}
	if s.frontierWake != nil {
		t.Fatal("session without an event sink started an unowned emitter")
	}
}
