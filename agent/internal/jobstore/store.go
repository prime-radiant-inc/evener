package jobstore

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// ErrStoreClosed is returned when an operation is attempted after Close.
var ErrStoreClosed = errors.New("jobstore: store closed")

// Store is an append-only jobs.jsonl event log for one session. It assigns a
// monotonic Seq to each appended event, fsyncs, and reconstructs records via
// Fold. It is safe for concurrent use.
type Store struct {
	mu     sync.Mutex
	path   string
	f      *os.File
	seq    int64
	closed bool
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
	if err := s.ensureOpenLocked(); err != nil {
		return err
	}
	nextSeq := s.seq + 1
	e.Seq = nextSeq
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("jobstore: marshal event: %w", err)
	}
	startOffset, err := s.f.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("jobstore: seek append start: %w", err)
	}
	if err := s.writeLineLocked(append(b, '\n')); err != nil {
		return s.appendFailureLocked("write event", err, startOffset)
	}
	if err := s.f.Sync(); err != nil {
		return s.appendFailureLocked("sync event", err, startOffset)
	}
	s.seq = nextSeq
	return nil
}

// Load reads every event and folds them to the current records.
func (s *Store) Load() (map[string]*JobRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return nil, err
	}
	events, err := s.readAllLocked()
	if err != nil {
		return nil, err
	}
	return Fold(events), nil
}

// LoadOrdered reads every event and folds them to the current records, returning
// them in durable APPEND ORDER — sorted by the seq of each job's FIRST event.
// Append order is the total order the append-only log defines; callers that must
// resolve "the latest record" (the one appended last) read it here rather than
// from a wall-clock field, which can skew across restore.
func (s *Store) LoadOrdered() ([]*JobRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return nil, err
	}
	events, err := s.readAllLocked()
	if err != nil {
		return nil, err
	}
	return FoldOrdered(events), nil
}

// LoadWatchSends reads every event and folds durable pending watch-send state.
func (s *Store) LoadWatchSends() (WatchSendRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return WatchSendRecord{}, err
	}
	events, err := s.readAllLocked()
	if err != nil {
		return WatchSendRecord{}, err
	}
	return FoldWatchSends(events), nil
}

// LoadGrants reads every event and folds the observer read-grant table
// (observer session id → watched job ids).
func (s *Store) LoadGrants() (map[string]map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return nil, err
	}
	events, err := s.readAllLocked()
	if err != nil {
		return nil, err
	}
	return FoldGrants(events), nil
}

// readAll is the locked-public test/helper variant of readAllLocked.
func (s *Store) readAll() ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return nil, err
	}
	return s.readAllLocked()
}

func (s *Store) readAllLocked() (events []Event, err error) {
	if err := s.recoverTrailingPartialLineLocked(); err != nil {
		return nil, err
	}
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("jobstore: read %s: %w", s.path, err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("jobstore: close %s: %w", s.path, closeErr)
		}
	}()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("jobstore: parse event line %d: %w", lineNo, err)
		}
		events = append(events, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: scan %s: %w", s.path, err)
	}
	return events, nil
}

func (s *Store) recoverTrailingPartialLineLocked() (err error) {
	info, err := os.Stat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("jobstore: stat %s: %w", s.path, err)
	}
	if info.Size() == 0 {
		return nil
	}
	f, err := os.Open(s.path)
	if err != nil {
		return fmt.Errorf("jobstore: inspect %s: %w", s.path, err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("jobstore: close %s: %w", s.path, closeErr)
		}
	}()
	if _, err := f.Seek(-1, io.SeekEnd); err != nil {
		return fmt.Errorf("jobstore: inspect trailing byte: %w", err)
	}
	last := make([]byte, 1)
	if _, err := io.ReadFull(f, last); err != nil {
		return fmt.Errorf("jobstore: read trailing byte: %w", err)
	}
	if last[0] == '\n' {
		return nil
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("jobstore: read %s: %w", s.path, err)
	}
	cut := bytes.LastIndexByte(raw, '\n')
	if cut < 0 {
		return s.recoverTrailingJSONLineLocked(raw, 0)
	}
	return s.recoverTrailingJSONLineLocked(raw[cut+1:], int64(cut+1))
}

func (s *Store) recoverTrailingJSONLineLocked(line []byte, offset int64) error {
	var e Event
	err := json.Unmarshal(line, &e)
	if err == nil {
		return s.finishTrailingJSONLineLocked()
	}
	if !isIncompleteTrailingJSON(line, err) {
		return nil
	}
	if err := s.f.Truncate(offset); err != nil {
		return fmt.Errorf("jobstore: truncate trailing partial line: %w", err)
	}
	if _, err := s.f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("jobstore: seek after trailing recovery: %w", err)
	}
	if err := s.f.Sync(); err != nil {
		return fmt.Errorf("jobstore: sync trailing recovery: %w", err)
	}
	return nil
}

func (s *Store) finishTrailingJSONLineLocked() error {
	if _, err := s.f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("jobstore: seek after trailing recovery: %w", err)
	}
	if err := s.writeLineLocked([]byte{'\n'}); err != nil {
		return fmt.Errorf("jobstore: terminate trailing event: %w", err)
	}
	if err := s.f.Sync(); err != nil {
		return fmt.Errorf("jobstore: sync trailing recovery: %w", err)
	}
	return nil
}

func isIncompleteTrailingJSON(line []byte, err error) bool {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return false
	}
	if err.Error() == "unexpected end of JSON input" {
		return true
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		return false
	}
	if syntaxErr.Offset < int64(len(trimmed)) {
		return false
	}
	last := trimmed[len(trimmed)-1]
	if last == '}' || last == ']' {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "literal") || strings.Contains(msg, "numeric literal")
}

func (s *Store) ensureOpenLocked() error {
	if s.closed {
		return ErrStoreClosed
	}
	return nil
}

func (s *Store) writeLineLocked(line []byte) error {
	for len(line) > 0 {
		n, err := s.f.Write(line)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		line = line[n:]
	}
	return nil
}

func (s *Store) appendFailureLocked(operation string, err error, startOffset int64) error {
	if rollbackErr := s.rollbackAppendLocked(startOffset); rollbackErr != nil {
		return fmt.Errorf("jobstore: %s: %w; rollback failed: %w", operation, err, rollbackErr)
	}
	return fmt.Errorf("jobstore: %s: %w", operation, err)
}

func (s *Store) rollbackAppendLocked(startOffset int64) error {
	truncateErr := s.f.Truncate(startOffset)
	_, seekErr := s.f.Seek(0, io.SeekEnd)
	syncErr := error(nil)
	if truncateErr == nil && seekErr == nil {
		syncErr = s.f.Sync()
	}
	if truncateErr != nil && seekErr != nil {
		return fmt.Errorf("truncate to %d: %w; seek eof: %w", startOffset, truncateErr, seekErr)
	}
	if truncateErr != nil {
		return fmt.Errorf("truncate to %d: %w", startOffset, truncateErr)
	}
	if seekErr != nil {
		return fmt.Errorf("seek eof: %w", seekErr)
	}
	if syncErr != nil {
		return fmt.Errorf("sync rollback truncate: %w", syncErr)
	}
	return nil
}

// Close closes the underlying file.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return err
	}
	if err := s.f.Close(); err != nil {
		return fmt.Errorf("jobstore: close %s: %w", s.path, err)
	}
	s.closed = true
	return nil
}
