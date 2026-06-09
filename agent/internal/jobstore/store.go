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
