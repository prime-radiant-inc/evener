package artifactstore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

var (
	ErrInvalidRef = errors.New("invalid artifact reference")
	ErrExpired    = errors.New("artifact reference expired")
)

const (
	refPrefix   = "artifact:"
	refIDBytes  = 16
	refIDLength = refIDBytes * 2
)

type Store struct {
	mu     sync.RWMutex
	dir    string
	closed bool
	refs   map[string]string
}

func New(base string) (*Store, error) {
	dir, err := os.MkdirTemp(base, "evener-artifacts-*")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &Store{dir: dir, refs: make(map[string]string)}, nil
}

func (s *Store) Put(data []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return "", ErrExpired
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
