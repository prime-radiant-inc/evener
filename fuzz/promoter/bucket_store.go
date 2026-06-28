package promoter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// BucketStore is the on-disk dedup memory: a JSON file mapping a failure
// signature to the path of the regression test that already covers it. Reruns
// (and future fuzzing campaigns) consult it so a known bug is never promoted
// twice. Writes are serialized by an in-process mutex and committed atomically
// (write-temp-then-rename), so a crash mid-write cannot corrupt the file.
//
// Cross-PROCESS concurrent promotion is not supported: a single promoter owns
// the store. The nightly campaign drives targets sequentially, so this matches
// how the store is used today.
type BucketStore struct {
	path string

	mu      sync.Mutex
	buckets map[string]string // signature.String() -> test path
}

// OpenBucketStore loads the bucket store at path, creating an empty one if the
// file does not exist.
func OpenBucketStore(path string) (*BucketStore, error) {
	s := &BucketStore{path: path, buckets: map[string]string{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read bucket store %s: %w", path, err)
	}
	if len(data) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s.buckets); err != nil {
		return nil, fmt.Errorf("parse bucket store %s: %w", path, err)
	}
	if s.buckets == nil {
		s.buckets = map[string]string{}
	}
	return s, nil
}

// Has reports whether a regression test for sig already exists.
func (s *BucketStore) Has(sig Signature) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.buckets[sig.String()]
	return ok
}

// Get returns the regression-test path recorded for sig.
func (s *BucketStore) Get(sig Signature) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.buckets[sig.String()]
	return p, ok
}

// Add records that sig is now covered by the regression test at testPath and
// persists the store atomically.
func (s *BucketStore) Add(sig Signature, testPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buckets[sig.String()] = testPath
	return s.persistLocked()
}

// Len returns the number of buckets recorded.
func (s *BucketStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.buckets)
}

// persistLocked writes the store to disk atomically. Caller must hold s.mu.
func (s *BucketStore) persistLocked() error {
	data, err := json.MarshalIndent(s.buckets, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal bucket store: %w", err)
	}
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create bucket store dir: %w", err)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".buckets-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp bucket store: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temp bucket store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp bucket store: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("commit bucket store: %w", err)
	}
	return nil
}
