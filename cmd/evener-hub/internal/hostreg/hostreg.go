// Package hostreg holds the controller hub's in-memory registry of remote
// hosts: the validated [[hosts]] entries from hub.toml plus the add-time cycle
// check that keeps the host graph acyclic.
//
// It is the data layer only. It opens no connections, spawns no SSH, and
// defines no AppWire source; component 05 iterates Registry.All() to register
// one source per host.
package hostreg

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"primeradiant.com/evener/appwire"
)

// ReservedName is the source ID the hub reserves for its own local source, so
// a remote host may not claim it.
const ReservedName = "local"

// Named sentinels so callers and tests can assert with errors.Is. Each returned
// error wraps the sentinel and names the offending entry.
var (
	// ErrInvalidName marks a name that is empty or fails appwire.ValidRefPart.
	ErrInvalidName = errors.New("invalid host name")
	// ErrDuplicateHost marks a name already present in the registry.
	ErrDuplicateHost = errors.New("duplicate host")
	// ErrReservedName marks a name equal to ReservedName.
	ErrReservedName = errors.New("reserved host name")
	// ErrHostCycle marks an add that would close a cycle in the host graph.
	ErrHostCycle = errors.New("host cycle")
	// ErrAmbiguousSSHUser marks an entry that sets User while SSH already
	// carries a user.
	ErrAmbiguousSSHUser = errors.New("ambiguous ssh user")
	// ErrMissingSSH marks an entry whose ssh destination is empty after trim.
	ErrMissingSSH = errors.New("missing ssh destination")
	// ErrEmptyRoot marks an entry with a root that is empty after trim.
	ErrEmptyRoot = errors.New("empty root")
)

// Host is one validated remote-host entry. It mirrors the hub's HostConfig
// fields; Name is the source ID surfaced in refs and URLs.
type Host struct {
	Name       string
	SSH        string
	User       string
	EvenerPath string
	Roots      []string
}

// ValidateName reports whether name is an acceptable host name: non-empty, not
// ReservedName, matching the AppWire ref grammar, and free of "..".
//
// The ".." rule is stricter than appwire.ParseRef, which rejects ".." only in
// the thread part (appwire/refs.go). We reject it in names too because a host
// name also appears in URL paths, where ".." is a traversal hazard.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	if name == ReservedName {
		return fmt.Errorf("%w: %q", ErrReservedName, name)
	}
	if !appwire.ValidRefPart(name) {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("%w: %q must not contain %q", ErrInvalidName, name, "..")
	}
	return nil
}

// validateEntry checks a single entry's shape. It does not consult the
// registry (duplicates and cycles are handled by Registry.Add).
func validateEntry(entry Host) error {
	if err := ValidateName(entry.Name); err != nil {
		return err
	}
	ssh := strings.TrimSpace(entry.SSH)
	if ssh == "" {
		return fmt.Errorf("%w: host %q", ErrMissingSSH, entry.Name)
	}
	if entry.User != "" && strings.ContainsRune(ssh, '@') {
		return fmt.Errorf("%w: host %q sets user and ssh %q already carries one", ErrAmbiguousSSHUser, entry.Name, ssh)
	}
	for _, root := range entry.Roots {
		if strings.TrimSpace(root) == "" {
			return fmt.Errorf("%w: host %q", ErrEmptyRoot, entry.Name)
		}
	}
	return nil
}

// Registry is a concurrency-safe set of validated hosts that also carries the
// directed upstream edges used for cycle rejection.
type Registry struct {
	mu    sync.RWMutex
	hosts map[string]Host
	edges map[string][]string // host name -> names of its upstream hosts
}

// New validates every entry and builds a registry. Entries are added in order,
// so a duplicate name fails with ErrDuplicateHost. Entries carry no upstream
// edges; use AddWithUpstreams to attach them.
func New(entries []Host) (*Registry, error) {
	r := &Registry{
		hosts: make(map[string]Host, len(entries)),
		edges: make(map[string][]string, len(entries)),
	}
	for _, entry := range entries {
		if err := r.Add(entry); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Add validates entry and inserts it. It is AddWithUpstreams with no upstreams;
// a single config-loaded entry cannot close a cycle.
func (r *Registry) Add(entry Host) error {
	return r.AddWithUpstreams(entry, nil)
}

// AddWithUpstreams validates entry and inserts it iff doing so introduces no
// cycle. upstreamNames are the identities of the hosts upstream of entry, as
// learned by component 05 from the attach handshake; the registry never
// discovers them itself.
//
// A refusal leaves the registry unchanged: the candidate is not inserted and no
// edge is recorded.
func (r *Registry) AddWithUpstreams(entry Host, upstreamNames []string) error {
	if err := validateEntry(entry); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.hosts[entry.Name]; ok {
		return fmt.Errorf("%w: %q", ErrDuplicateHost, entry.Name)
	}
	if err := r.checkCycleLocked(entry.Name, upstreamNames); err != nil {
		return err
	}
	r.hosts[entry.Name] = entry
	r.edges[entry.Name] = append([]string(nil), upstreamNames...)
	return nil
}

// checkCycleLocked walks upstream edges from the candidate. It refuses if it
// reaches the candidate itself (a self-edge or a back-edge) or a node already
// on the current path (a cycle among the upstreams). Callers hold r.mu.
func (r *Registry) checkCycleLocked(candidate string, upstreamNames []string) error {
	onPath := map[string]bool{candidate: true}
	done := map[string]bool{}
	var walk func(name string) error
	walk = func(name string) error {
		if name == candidate {
			return fmt.Errorf("%w: %q", ErrHostCycle, candidate)
		}
		if onPath[name] {
			return fmt.Errorf("%w: %q", ErrHostCycle, candidate)
		}
		if done[name] {
			return nil
		}
		onPath[name] = true
		for _, upstream := range r.edges[name] {
			if err := walk(upstream); err != nil {
				return err
			}
		}
		delete(onPath, name)
		done[name] = true
		return nil
	}
	for _, upstream := range upstreamNames {
		if err := walk(upstream); err != nil {
			return err
		}
	}
	return nil
}

// Get returns the host registered under name.
func (r *Registry) Get(name string) (Host, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	host, ok := r.hosts[name]
	return host, ok
}

// All returns every registered host sorted by name, mirroring
// appsource.Registry.All. Component 05 iterates it to register a source per
// host.
func (r *Registry) All() []Host {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.hosts))
	for name := range r.hosts {
		names = append(names, name)
	}
	sort.Strings(names)
	hosts := make([]Host, 0, len(names))
	for _, name := range names {
		hosts = append(hosts, r.hosts[name])
	}
	return hosts
}
