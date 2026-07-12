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
	ops  bucketStoreOps

	mu      sync.Mutex
	buckets map[string]string // signature.String() -> test path
}

type atomicFile interface {
	Write([]byte) (int, error)
	Close() error
	Name() string
}

type bucketStoreOps struct {
	readFile   func(string) ([]byte, error)
	mkdirAll   func(string, os.FileMode) error
	createTemp func(string, string) (atomicFile, error)
	rename     func(string, string) error
	remove     func(string) error
}

var osBucketStoreOps = bucketStoreOps{
	readFile: os.ReadFile,
	mkdirAll: os.MkdirAll,
	createTemp: func(dir, pattern string) (atomicFile, error) {
		return os.CreateTemp(dir, pattern)
	},
	rename: os.Rename,
	remove: os.Remove,
}

// OpenBucketStore loads the bucket store at path, creating an empty one if the
// file does not exist.
func OpenBucketStore(path string) (*BucketStore, error) {
	return openBucketStoreWithOps(path, osBucketStoreOps)
}

func openBucketStoreWithOps(path string, ops bucketStoreOps) (*BucketStore, error) {
	s := &BucketStore{path: path, ops: ops, buckets: map[string]string{}}
	data, err := ops.readFile(path)
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
	data, _ := json.MarshalIndent(s.buckets, "", "  ")
	if dir := filepath.Dir(s.path); dir != "" {
		if err := s.ops.mkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create bucket store dir: %w", err)
		}
	}
	tmp, err := s.ops.createTemp(filepath.Dir(s.path), ".buckets-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp bucket store: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = s.ops.remove(tmpName)
		return fmt.Errorf("write temp bucket store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = s.ops.remove(tmpName)
		return fmt.Errorf("close temp bucket store: %w", err)
	}
	if err := s.ops.rename(tmpName, s.path); err != nil {
		_ = s.ops.remove(tmpName)
		return fmt.Errorf("commit bucket store: %w", err)
	}
	return nil
}
