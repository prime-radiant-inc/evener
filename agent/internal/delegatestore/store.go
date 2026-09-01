package delegatestore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

type fileOps struct {
	write    func(*os.File, []byte) (int, error)
	sync     func(*os.File) error
	truncate func(*os.File, int64) error
}

type Store struct {
	mu       sync.Mutex
	path     string
	f        *os.File
	seq      uint64
	ops      fileOps
	unusable error
	closed   bool
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("delegatestore: path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("delegatestore: create parent directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("delegatestore: open %s: %w", path, err)
	}
	store := &Store{path: path, f: file, ops: defaultFileOps()}
	if err := store.initializeOrRecover(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Load() ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureUsableLocked(); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("delegatestore: read %s: %w", s.path, err)
	}
	events, err := decodeLog(raw, false)
	if err != nil {
		return nil, err
	}
	if _, err := Fold(events); err != nil {
		return nil, fmt.Errorf("delegatestore: fold: %w", err)
	}
	return cloneEvents(events), nil
}

func (s *Store) Append(state State, event Event) (Event, State, error) {
	assigned, accepted, err := s.AppendBatch(state, []Event{event})
	if err != nil {
		return Event{}, nil, err
	}
	return assigned[0], accepted, nil
}

func (s *Store) AppendBatch(state State, events []Event) ([]Event, State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureUsableLocked(); err != nil {
		return nil, nil, err
	}
	accepted, err := cloneState(state)
	if err != nil {
		return nil, nil, fmt.Errorf("delegatestore: clone supplied state: %w", err)
	}
	if len(events) == 0 {
		return []Event{}, accepted, nil
	}

	assigned := cloneEvents(events)
	for i := range assigned {
		assigned[i].Seq = s.seq + uint64(i) + 1
		if err := Apply(accepted, assigned[i]); err != nil {
			return nil, nil, fmt.Errorf("delegatestore: preflight event %d: %w", assigned[i].Seq, err)
		}
	}
	record, err := json.Marshal(batchRecord{Events: assigned})
	if err != nil {
		return nil, nil, fmt.Errorf("delegatestore: marshal batch: %w", err)
	}
	record = append(record, '\n')
	offset, err := s.f.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, nil, fmt.Errorf("delegatestore: seek append start: %w", err)
	}
	n, err := s.ops.write(s.f, record)
	if err == nil && n != len(record) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return nil, nil, s.rollbackLocked(offset, fmt.Errorf("delegatestore: write batch: %w", err))
	}
	if err := s.ops.sync(s.f); err != nil {
		return nil, nil, s.rollbackLocked(offset, fmt.Errorf("delegatestore: sync batch: %w", err))
	}
	s.seq += uint64(len(assigned))
	return cloneEvents(assigned), accepted, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.f == nil {
		return nil
	}
	if err := s.f.Close(); err != nil {
		return fmt.Errorf("delegatestore: close %s: %w", s.path, err)
	}
	return nil
}

func (s *Store) initializeOrRecover() error {
	info, err := s.f.Stat()
	if err != nil {
		return fmt.Errorf("delegatestore: stat %s: %w", s.path, err)
	}
	if info.Size() == 0 {
		return s.writeVersionHeader()
	}

	raw, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("delegatestore: read %s: %w", s.path, err)
	}
	committed := raw
	recoverOffset := int64(-1)
	if raw[len(raw)-1] != '\n' {
		lastNewline := bytes.LastIndexByte(raw, '\n')
		if lastNewline < 0 {
			// A file with no newline at all can only be the initial version
			// header write torn by a crash before its sync: every later append
			// lands after a terminated header line. No committed events can
			// exist, so truncating to zero and rewriting the header is lossless.
			if err := s.ops.truncate(s.f, 0); err != nil {
				return fmt.Errorf("delegatestore: truncate torn version header: %w", err)
			}
			return s.writeVersionHeader()
		}
		committed = raw[:lastNewline+1]
		recoverOffset = int64(lastNewline + 1)
	}
	events, err := decodeLog(committed, false)
	if err != nil {
		return err
	}
	if _, err := Fold(events); err != nil {
		return fmt.Errorf("delegatestore: fold: %w", err)
	}
	if recoverOffset >= 0 {
		if err := s.ops.truncate(s.f, recoverOffset); err != nil {
			return fmt.Errorf("delegatestore: truncate unterminated trailing batch: %w", err)
		}
		if err := s.ops.sync(s.f); err != nil {
			return fmt.Errorf("delegatestore: sync trailing-batch recovery: %w", err)
		}
	}
	if _, err := s.f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("delegatestore: seek end: %w", err)
	}
	if len(events) > 0 {
		s.seq = events[len(events)-1].Seq
	}
	return nil
}

// writeVersionHeader writes and syncs a fresh version header. Callers must
// ensure the file is empty with its offset at zero.
func (s *Store) writeVersionHeader() error {
	header, err := json.Marshal(versionRecord{Version: CurrentVersion})
	if err != nil {
		return fmt.Errorf("delegatestore: marshal version header: %w", err)
	}
	header = append(header, '\n')
	n, err := s.ops.write(s.f, header)
	if err == nil && n != len(header) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fmt.Errorf("delegatestore: write version header: %w", err)
	}
	if err := s.ops.sync(s.f); err != nil {
		return fmt.Errorf("delegatestore: sync version header: %w", err)
	}
	return nil
}

func (s *Store) rollbackLocked(offset int64, operationErr error) error {
	if err := s.ops.truncate(s.f, offset); err != nil {
		rollbackErr := fmt.Errorf("delegatestore: rollback truncate: %w", err)
		return s.latchRollbackFailureLocked(operationErr, rollbackErr)
	}
	if _, err := s.f.Seek(offset, io.SeekStart); err != nil {
		rollbackErr := fmt.Errorf("delegatestore: rollback seek: %w", err)
		return s.latchRollbackFailureLocked(operationErr, rollbackErr)
	}
	if err := s.ops.sync(s.f); err != nil {
		rollbackErr := fmt.Errorf("delegatestore: rollback sync: %w", err)
		return s.latchRollbackFailureLocked(operationErr, rollbackErr)
	}
	return operationErr
}

func (s *Store) latchRollbackFailureLocked(operationErr, rollbackErr error) error {
	combined := errors.Join(operationErr, rollbackErr)
	s.unusable = combined
	return fmt.Errorf("delegatestore: append failed and rollback failed: %w", combined)
}

func (s *Store) ensureUsableLocked() error {
	if s.closed {
		return errors.New("delegatestore: store is closed")
	}
	if s.unusable != nil {
		return fmt.Errorf("delegatestore: store is unusable after failed rollback: %w", s.unusable)
	}
	if s.f == nil {
		return errors.New("delegatestore: store has no file")
	}
	return nil
}

func defaultFileOps() fileOps {
	return fileOps{
		write:    func(file *os.File, data []byte) (int, error) { return file.Write(data) },
		sync:     func(file *os.File) error { return file.Sync() },
		truncate: func(file *os.File, size int64) error { return file.Truncate(size) },
	}
}

// decodeLog decodes a whole, in-memory version-header-plus-batch-lines log.
// It has exactly one caller-facing shape: Store.Load/Store.Open's own
// buffer, never a context or an event ceiling — ScanEvents (read_events.go)
// is the actual context-aware, bounded read path, and does its own inline,
// line-by-line decoding rather than calling this (roborev finding on #807:
// an earlier round added ctx/maxEvents parameters here that nothing ever
// exercised — decodeLog's own only caller always passed
// context.Background() and 0 — so this reverts to the original, unbounded
// shape rather than carrying dead branches).
func decodeLog(raw []byte, tolerateUnterminatedTail bool) ([]Event, error) {
	if len(raw) == 0 {
		return nil, errors.New("delegatestore: missing version header")
	}
	if raw[len(raw)-1] != '\n' {
		if !tolerateUnterminatedTail {
			return nil, errors.New("delegatestore: unterminated trailing batch")
		}
		lastNewline := bytes.LastIndexByte(raw, '\n')
		if lastNewline < 0 {
			return nil, errors.New("delegatestore: unterminated version header")
		}
		raw = raw[:lastNewline+1]
	}
	lines := bytes.Split(raw, []byte{'\n'})
	lines = lines[:len(lines)-1]
	if len(lines) == 0 {
		return nil, errors.New("delegatestore: missing version header")
	}
	var header versionRecord
	if err := decodeJSONLine(lines[0], &header); err != nil {
		return nil, fmt.Errorf("delegatestore: decode version header: %w", err)
	}
	if header.Version != CurrentVersion {
		return nil, fmt.Errorf("delegatestore: unsupported version %d", header.Version)
	}

	var events []Event
	for i, line := range lines[1:] {
		var batch batchRecord
		if err := decodeJSONLine(line, &batch); err != nil {
			return nil, fmt.Errorf("delegatestore: decode batch line %d: %w", i+2, err)
		}
		if len(batch.Events) == 0 {
			return nil, fmt.Errorf("delegatestore: batch line %d has no events", i+2)
		}
		events = append(events, batch.Events...)
	}
	return events, nil
}

func decodeJSONLine(line []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func cloneEvents(events []Event) []Event {
	clones := make([]Event, len(events))
	for i := range events {
		clones[i] = cloneEvent(events[i])
	}
	return clones
}

func cloneEvent(event Event) Event {
	clone := event
	if event.Created != nil {
		clone.Created = &DelegateCreated{Descriptor: cloneDescriptor(event.Created.Descriptor)}
	}
	if event.RunStarted != nil {
		payload := *event.RunStarted
		clone.RunStarted = &payload
	}
	if event.TerminalPrepared != nil {
		payload := *event.TerminalPrepared
		payload.Packet = *cloneTerminalPacket(&event.TerminalPrepared.Packet)
		clone.TerminalPrepared = &payload
	}
	if event.RunFinished != nil {
		payload := *event.RunFinished
		payload.Outcome = *cloneOutcome(&event.RunFinished.Outcome)
		payload.Packet = cloneTerminalPacket(event.RunFinished.Packet)
		clone.RunFinished = &payload
	}
	if event.ResumabilityClosed != nil {
		payload := *event.ResumabilityClosed
		clone.ResumabilityClosed = &payload
	}
	if event.SubtreeStopRequested != nil {
		payload := *event.SubtreeStopRequested
		clone.SubtreeStopRequested = &payload
	}
	if event.SubtreeStopCompleted != nil {
		payload := *event.SubtreeStopCompleted
		clone.SubtreeStopCompleted = &payload
	}
	if event.DeliveryAcknowledged != nil {
		payload := *event.DeliveryAcknowledged
		clone.DeliveryAcknowledged = &payload
	}
	if event.AttentionChanged != nil {
		payload := *event.AttentionChanged
		clone.AttentionChanged = &payload
	}
	return clone
}
