package hubcore

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/afero"
)

const (
	attachedHostsSnapshotVersion uint64 = 1
	attachedHostsStateSubdir            = "attached-hosts"
	attachedHostsStateFilename          = "state.json"
)

// AttachedHostRecord is the registration identity of a remote host that last
// reached attached state; Generation and IncarnationID fence re-added hosts.
type AttachedHostRecord struct {
	Generation    uint64 `json:"generation"`
	IncarnationID string `json:"incarnation_id"`
}

type attachedHostsSnapshot struct {
	Version uint64                        `json:"version"`
	Hosts   map[string]AttachedHostRecord `json:"hosts"`
}

type attachedHostsStoreFaults struct {
	BeforeRename func() error
	AfterRename  func() error
}

// AttachedHostsStore durably remembers which remote hosts reached attached
// state under <HubStateRoot>/attached-hosts/state.json, keyed by trimmed name.
// An empty stateRoot keeps it in memory; a corrupt file is left untouched.
type AttachedHostsStore struct {
	mu     sync.Mutex
	fs     afero.Fs
	root   string
	hosts  map[string]AttachedHostRecord
	faults attachedHostsStoreFaults
}

// NewAttachedHostsStore opens the attached hosts store under stateRoot.
func NewAttachedHostsStore(stateRoot string) (*AttachedHostsStore, error) {
	return newAttachedHostsStoreFS(afero.NewOsFs(), stateRoot, attachedHostsStoreFaults{})
}

func newAttachedHostsStoreFS(fs afero.Fs, stateRoot string, faults attachedHostsStoreFaults) (*AttachedHostsStore, error) {
	stateRoot = strings.TrimSpace(stateRoot)
	hosts, err := loadAttachedHostsFS(fs, stateRoot)
	if err != nil {
		return nil, err
	}
	return &AttachedHostsStore{fs: fs, root: stateRoot, hosts: hosts, faults: faults}, nil
}

// Snapshot returns a copy of the remembered hosts; mutating it does not change the store.
func (s *AttachedHostsStore) Snapshot() map[string]AttachedHostRecord {
	if s == nil {
		return map[string]AttachedHostRecord{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.hosts)
}

// Record remembers name with rec. Names are trimmed and must not be empty; an unchanged record writes nothing.
func (s *AttachedHostsStore) Record(name string, rec AttachedHostRecord) error {
	if s == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("attached host name is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.hosts[name]; ok && existing == rec {
		return nil
	}
	next := maps.Clone(s.hosts)
	next[name] = rec
	return s.persistLocked(next)
}

// Remove forgets name. Names are trimmed; an empty or absent name writes nothing.
func (s *AttachedHostsStore) Remove(name string) error {
	if s == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.hosts[name]; !ok {
		return nil
	}
	next := maps.Clone(s.hosts)
	delete(next, name)
	return s.persistLocked(next)
}

// Prune forgets every host keep rejects in one write; a nil keep keeps every entry.
func (s *AttachedHostsStore) Prune(keep func(name string, rec AttachedHostRecord) bool) error {
	if s == nil || keep == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[string]AttachedHostRecord, len(s.hosts))
	for name, rec := range s.hosts {
		if keep(name, rec) {
			next[name] = rec
		}
	}
	if len(next) == len(s.hosts) {
		return nil
	}
	return s.persistLocked(next)
}

func (s *AttachedHostsStore) persistLocked(next map[string]AttachedHostRecord) error {
	if s.root == "" {
		s.hosts = next
		return nil
	}
	data, err := json.Marshal(attachedHostsSnapshot{Version: attachedHostsSnapshotVersion, Hosts: next})
	if err != nil {
		return fmt.Errorf("marshal attached hosts state: %w", err)
	}
	data = append(data, '\n')
	renamed, err := writeStateFileAtomic(s.fs, attachedHostsStatePath(s.root), "attached hosts", data, s.faults.BeforeRename, s.faults.AfterRename)
	if renamed {
		s.hosts = next
	}
	return err
}

func attachedHostsStatePath(stateRoot string) string {
	return filepath.Join(stateRoot, attachedHostsStateSubdir, attachedHostsStateFilename)
}

func loadAttachedHostsFS(fs afero.Fs, stateRoot string) (map[string]AttachedHostRecord, error) {
	hosts := map[string]AttachedHostRecord{}
	if strings.TrimSpace(stateRoot) == "" {
		return hosts, nil
	}
	data, ok, err := readStateFile(fs, attachedHostsStatePath(stateRoot), "attached hosts")
	if err != nil {
		return nil, err
	}
	if !ok {
		return hosts, nil
	}
	var snap attachedHostsSnapshot
	if err := decodeStateFileStrict(data, "attached hosts", &snap); err != nil {
		return nil, err
	}
	if snap.Version != attachedHostsSnapshotVersion {
		return nil, fmt.Errorf("unsupported attached hosts state version %d", snap.Version)
	}
	if snap.Hosts == nil {
		return nil, errors.New("decode attached hosts state: missing hosts")
	}
	hosts = make(map[string]AttachedHostRecord, len(snap.Hosts))
	for name, rec := range snap.Hosts {
		if name == "" || strings.TrimSpace(name) != name {
			return nil, fmt.Errorf("decode attached hosts state: invalid host name %q", name)
		}
		hosts[name] = rec
	}
	return hosts, nil
}
