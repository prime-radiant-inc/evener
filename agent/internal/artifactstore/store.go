package artifactstore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

var (
	ErrInvalidRef = errors.New("invalid artifact reference")
	ErrExpired    = errors.New("artifact reference expired")
	// ErrBudgetExceeded reports that a new artifact was refused because
	// admitting it would exceed the root tree's byte or artifact-count budget.
	// References already issued stay readable; nothing is evicted.
	ErrBudgetExceeded = errors.New("artifact store budget exceeded")
)

const (
	refPrefix   = "artifact:"
	refIDBytes  = 16
	refIDLength = refIDBytes * 2

	// artifactDirPrefix and artifactDirPIDMarker make the owning process part
	// of a store directory's name, so a later store can prove a directory
	// abandoned by a dead process is safe to reclaim. Legacy directories
	// without the marker carry no provable owner and are never reclaimed.
	artifactDirPrefix    = "evener-artifacts-"
	artifactDirPIDMarker = "pid-"

	// defaultMaxBytes and defaultMaxRefs bound one root session tree's retained
	// artifacts. They are admission limits, not eviction policies: a full store
	// refuses new artifacts and reports retention_failed, leaving every issued
	// reference readable.
	defaultMaxBytes int64 = 512 << 20
	defaultMaxRefs        = 4096
)

// budget is a store's admission limit. A negative field disables that limit.
type budget struct {
	maxBytes int64
	maxRefs  int
}

type Store struct {
	mu        sync.RWMutex
	dir       string
	closed    bool
	refs      map[string]string
	usedBytes int64
	maxBytes  int64
	maxRefs   int
}

// New creates a store in a new directory under base, or under the process temp
// dir when base is empty. It first reclaims directories in base whose marked
// owner process is definitely dead and that this user owns.
func New(base string) (*Store, error) {
	return newStore(base, budget{maxBytes: defaultMaxBytes, maxRefs: defaultMaxRefs})
}

func newStore(base string, limit budget) (*Store, error) {
	if base == "" {
		base = os.TempDir()
	}
	reclaimDeadStores(base)
	pattern := fmt.Sprintf("%s%s%d-*", artifactDirPrefix, artifactDirPIDMarker, os.Getpid())
	dir, err := os.MkdirTemp(base, pattern)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &Store{
		dir:      dir,
		refs:     make(map[string]string),
		maxBytes: limit.maxBytes,
		maxRefs:  limit.maxRefs,
	}, nil
}

func (s *Store) Put(data []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return "", ErrExpired
	}
	if s.maxBytes >= 0 && s.usedBytes+int64(len(data)) > s.maxBytes {
		return "", ErrBudgetExceeded
	}
	if s.maxRefs >= 0 && len(s.refs) >= s.maxRefs {
		return "", ErrBudgetExceeded
	}

	idBytes := make([]byte, refIDBytes)
	if _, err := rand.Read(idBytes); err != nil {
		return "", err
	}
	id := hex.EncodeToString(idBytes)
	ref := refPrefix + id
	path := filepath.Join(s.dir, id)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	s.refs[ref] = path
	s.usedBytes += int64(len(data))
	return ref, nil
}

func (s *Store) Open(ref string) (*os.File, error) {
	if !validRef(ref) {
		return nil, ErrInvalidRef
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	path, ok := s.refs[ref]
	if !ok || s.closed {
		return nil, ErrExpired
	}
	return os.Open(path)
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return os.RemoveAll(s.dir)
	}
	s.closed = true
	s.refs = make(map[string]string)
	return os.RemoveAll(s.dir)
}

func validRef(ref string) bool {
	if len(ref) != len(refPrefix)+refIDLength || ref[:len(refPrefix)] != refPrefix {
		return false
	}
	for _, c := range ref[len(refPrefix):] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// reclaimDeadStores removes store directories in base that this user owns and
// whose marked owner process is definitely gone. Everything it cannot prove
// dead is left alone; reclamation is best-effort and never fails store
// creation.
func reclaimDeadStores(base string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, artifactDirPrefix+artifactDirPIDMarker) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if !deadStoreDir(name, info) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(base, name))
	}
}

// storeOwnerPID reads the pid encoded in a marked store directory name.
func storeOwnerPID(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, artifactDirPrefix+artifactDirPIDMarker)
	if !ok {
		return 0, false
	}
	pidText, _, ok := strings.Cut(rest, "-")
	if !ok || pidText == "" {
		return 0, false
	}
	for _, c := range pidText {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}
