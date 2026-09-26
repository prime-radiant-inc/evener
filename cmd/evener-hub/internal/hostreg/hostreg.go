// Package hostreg holds the controller hub's in-memory registry of remote
// hosts: the validated [[hosts]] entries from the machine-managed hub.toml plus the add-time cycle
// check that keeps the host graph acyclic.
//
// It is the data layer only. It opens no connections, spawns no SSH, and
// defines no AppWire source; component 05 iterates Registry.All() to register
// one source per host.
package hostreg

import (
	"errors"
	"fmt"
	"slices"
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
	// ErrUnknownHost marks an operation naming a host the registry does not hold.
	ErrUnknownHost = errors.New("unknown host")
)

// Host is one validated remote-host entry. It mirrors the hub's HostConfig
// fields; Name is the source ID surfaced in refs and URLs.
type Host struct {
	Name string
	SSH  string
	User string
	// EvenerPath, ConfigPath, and Addr carry the host's non-default locations:
	// the binary, its hub.toml, and the hub's listen address. Consumers need
	// them to attach with the host's own configuration rather than a default
	// that would address the wrong process.
	EvenerPath string
	ConfigPath string
	Addr       string
	Roots      []string
	// KeyPath is the SSH private-key file the controller dials with. The
	// machine-managed hub.toml stores it as key_path (registry spec 08 §6), so
	// a UI-added host round-trips its key through a rewrite; a host that never
	// set one resolves its identity the way the operator's ssh_config does.
	KeyPath string
	// Generation is the registry-wide insert generation: a counter the
	// registry advances on every insert and never reuses, so a name's
	// re-added entry — even with byte-identical content — is a different
	// entry from the one an earlier Get handed out. It carries the 08 spec
	// series' generation semantics for attach identity: callers construct
	// Hosts with it zero, Add (AddWithUpstreams) overwrites whatever they
	// set, and an attach that captured an entry pins itself to the
	// generation as much as to the content — content equality alone cannot
	// tell a removed entry from its re-added twin (see Equal).
	Generation uint64
}

// ValidateName reports whether name is an acceptable host name: non-empty, not
// ReservedName, matching the AppWire ref grammar, and neither "." nor
// containing "..".
//
// The dot rules are stricter than appwire.ParseRef, which rejects ".." only in
// the thread part (appwire/refs.go). We reject both in names because a host name
// also appears in URL paths and as a filesystem segment, where "." and ".." are
// path-cleaning hazards.
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
	if name == "." {
		return fmt.Errorf("%w: %q is not a usable host name", ErrInvalidName, name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("%w: %q must not contain %q", ErrInvalidName, name, "..")
	}
	return nil
}

// Normalize returns entry with every whitespace-sensitive field trimmed, so the
// registry stores exactly what it validated. Without this, ssh = "  m4.local  "
// passes validation (which trims) and is then stored untrimmed, and every
// consumer fails to resolve a host the registry called valid. It also gives the
// entry its own Roots backing array, so the caller's slice is never aliased by
// registry state.
//
// It is exported because validation and storage have to agree: a config loader
// that validates through this package must apply the same normalization to the
// values it keeps, or it hands consumers what the registry refused to store.
func Normalize(entry Host) Host {
	entry.Name = strings.TrimSpace(entry.Name)
	entry.SSH = strings.TrimSpace(entry.SSH)
	entry.User = strings.TrimSpace(entry.User)
	entry.EvenerPath = strings.TrimSpace(entry.EvenerPath)
	entry.ConfigPath = strings.TrimSpace(entry.ConfigPath)
	entry.Addr = strings.TrimSpace(entry.Addr)
	entry.KeyPath = strings.TrimSpace(entry.KeyPath)
	entry.Roots = slices.Clone(entry.Roots)
	for i, root := range entry.Roots {
		entry.Roots[i] = strings.TrimSpace(root)
	}
	return entry
}

// Equal reports whether h and other carry the same configured content: every
// field equal, with Roots compared by content. Generation is deliberately
// excluded — content equality cannot tell a removed entry from a byte-identical
// re-add — so an identity recheck compares the generation alongside this
// (sshconn's Ensure and reconnectOnce), never this alone.
func (h Host) Equal(other Host) bool {
	return h.Name == other.Name && h.SSH == other.SSH && h.User == other.User &&
		h.EvenerPath == other.EvenerPath && h.ConfigPath == other.ConfigPath &&
		h.Addr == other.Addr && h.KeyPath == other.KeyPath &&
		slices.Equal(h.Roots, other.Roots)
}

// cloneHost deep-copies the one field a caller could otherwise mutate through a
// returned value: Host is a value type, but Roots is a slice.
func cloneHost(host Host) Host {
	host.Roots = slices.Clone(host.Roots)
	return host
}

// validateEntry checks a single entry's shape. It expects an entry already
// normalized by normalize, so the values it checks are the values that will be
// stored. It does not consult the registry (duplicates and cycles are handled by
// Registry.Add).
func validateEntry(entry Host) error {
	if err := ValidateName(entry.Name); err != nil {
		return err
	}
	if entry.SSH == "" {
		return fmt.Errorf("%w: host %q", ErrMissingSSH, entry.Name)
	}
	if entry.User != "" && strings.ContainsRune(entry.SSH, '@') {
		return fmt.Errorf("%w: host %q sets user and ssh %q already carries one", ErrAmbiguousSSHUser, entry.Name, entry.SSH)
	}
	if slices.Contains(entry.Roots, "") {
		return fmt.Errorf("%w: host %q", ErrEmptyRoot, entry.Name)
	}
	return nil
}

// ValidateEntry reports whether entry could be added to a registry: the same
// shape checks AddWithUpstreams runs after normalizing — the name grammar, a
// non-empty ssh destination, user/ssh agreement, and non-empty roots —
// available on its own so a caller validating one entry commits nothing and
// builds no scratch registry. Duplicates and cycles stay Add's job.
func ValidateEntry(entry Host) error {
	return validateEntry(Normalize(entry))
}

// Registry is a concurrency-safe set of validated hosts that also carries the
// directed upstream edges used for cycle rejection.
type Registry struct {
	mu    sync.RWMutex
	hosts map[string]Host
	edges map[string][]string // host name -> names of its upstream hosts
	// gen is the registry-wide insert generation: one monotonic counter,
	// advanced by every Add and never reused, that AddWithUpstreams stamps
	// on each inserted entry. One registry-wide counter rather than one
	// per name keeps the retained state a single integer: a per-name
	// counter map would grow unboundedly under name churn and could not be
	// pruned — dropping a name's count would let a byte-identical re-add
	// reuse the removed entry's generation. Every Host.Generation consumer
	// compares a captured entry with the live entry of the same name, so a
	// registry-wide counter preserves those semantics exactly — a
	// remove/re-add of any name still always advances past the removed
	// entry's generation.
	gen uint64
}

// New validates every entry and builds a registry. Entries are added in order,
// so a duplicate name fails with ErrDuplicateHost. Entries carry no upstream
// edges; a config-loaded host gets them from SetUpstreams once component 05 has
// learned them (AddWithUpstreams inserts, so it cannot be used for a host New
// already registered).
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
	entry = Normalize(entry)
	if err := validateEntry(entry); err != nil {
		return err
	}
	upstreamNames, err := normalizeUpstreams(upstreamNames)
	if err != nil {
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
	// The generation is assigned under the lock, from the registry-wide
	// counter: a re-add of the same name — byte-identical or not, interleaved
	// with other hosts' churn or not — always carries a generation the
	// removed entry never had.
	r.stampAndStoreLocked(entry)
	r.edges[entry.Name] = append([]string(nil), upstreamNames...)
	return nil
}

// stampAndStoreLocked gives entry its fresh registry identity and stores it.
// Callers hold r.mu, so advancing the registry-wide generation and exposing the
// entry remain one atomic operation.
func (r *Registry) stampAndStoreLocked(entry Host) {
	entry.Generation = r.gen + 1
	r.gen = entry.Generation
	r.hosts[entry.Name] = entry
}

// Update replaces the entry registered under entry.Name in place and stamps it
// with a fresh generation from the registry-wide counter — the identity fence
// every capture-compare consumer reads (the SSH manager's pre-publish rechecks
// and the hub's row fence), so a capture taken before an update stops matching
// once the update lands.
//
// It normalizes and validates exactly as an add does — Normalize, then the same
// validateEntry — so an update can never store what an add would refuse, and a
// refusal leaves the registry untouched. It runs no host-count cap: the
// registry has none, config load has none, and the slice that owns the cap
// (the design document's [03] follow-up) is where it arrives.
//
// Unlike AddWithUpstreams it never inserts: update targets a live entry, so a
// name the registry does not hold is ErrUnknownHost rather than a create. The
// name's upstream edges are preserved verbatim, which is why the cycle check is
// not rerun — the edges are unchanged, and the graph they describe is the one
// that was already acyclic.
func (r *Registry) Update(entry Host) error {
	entry = Normalize(entry)
	if err := validateEntry(entry); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.hosts[entry.Name]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownHost, entry.Name)
	}
	// The generation is assigned under the lock from the registry-wide counter,
	// exactly as AddWithUpstreams does, so an update is as much a new identity
	// as a remove/re-add: no generation a capture can hold is ever reused.
	r.stampAndStoreLocked(entry)
	return nil
}

// SetUpstreams attaches or replaces the upstream edges of an already registered
// host, applying the same cycle check as AddWithUpstreams. New registers
// config-loaded hosts without edges, so this — not AddWithUpstreams — is how
// component 05 records what the attach handshake learned about them. A refusal
// leaves the recorded edges unchanged.
func (r *Registry) SetUpstreams(name string, upstreamNames []string) error {
	// The target is normalized like every other name, so a padded spelling still
	// finds its host instead of failing as unknown and skipping the cycle check.
	name = strings.TrimSpace(name)
	if err := ValidateName(name); err != nil {
		return err
	}
	upstreamNames, err := normalizeUpstreams(upstreamNames)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.hosts[name]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownHost, name)
	}
	if err := r.checkCycleLocked(name, upstreamNames); err != nil {
		return err
	}
	r.edges[name] = append([]string(nil), upstreamNames...)
	return nil
}

// normalizeUpstreams trims upstream names and validates each one as a host name.
// Storing them verbatim made " b " a key distinct from "b", so the padded
// spelling looked like a host with no edges and a cycle through it went
// undetected. The grammar check matters for the same reason: an edge that could
// never name a registered host would sit forever as an unresolvable leaf.
func normalizeUpstreams(names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if err := ValidateName(trimmed); err != nil {
			return nil, err
		}
		out = append(out, trimmed)
	}
	return out, nil
}

// checkCycleLocked walks upstream edges from the candidate. It refuses if it
// reaches the candidate itself (a self-edge or a back-edge) or a node already
// on the current path (a cycle among the upstreams). Callers hold r.mu.
//
// An upstream name the registry has no entry for is a leaf: it has no recorded
// edges, so nothing beyond it can be traversed. That is deliberate for v1 — a
// cycle that closes through a host running on another hub is undetectable here,
// because no AppWire method reports a hub's configured hosts, so component 05
// can never supply those edges. Cross-hub (multi-hop) cycle detection is
// deferred in the component spec; this function detects every cycle whose edges
// are all known to this registry.
func (r *Registry) checkCycleLocked(candidate string, upstreamNames []string) error {
	onPath := map[string]bool{candidate: true}
	done := map[string]bool{}
	var walk func(name string) error
	walk = func(name string) error {
		// candidate is pre-seeded in onPath, so reaching it — a self-edge
		// or a back-edge — trips the same refusal as any other node
		// already on the current path (a cycle among the upstreams).
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
	// Trimmed like every other name, so a padded spelling finds the host rather
	// than silently missing it.
	name = strings.TrimSpace(name)
	r.mu.RLock()
	defer r.mu.RUnlock()
	host, ok := r.hosts[name]
	if !ok {
		return Host{}, false
	}
	return cloneHost(host), true
}

// SameRegistration reports whether two captures describe the same insert: equal
// configured content and the same generation. It is the one predicate an
// identity recheck states, wrapped here by Registry.SameRegistration and
// applied by sshconn's channels to pair a live channel with an entry. Content
// equality alone cannot tell a removed entry from its byte-identical re-add
// (Equal excludes Generation, and a re-add takes a fresh generation from the
// registry-wide counter); generation equality alone cannot refuse a hand-built
// capture whose generation is current but whose content is stale, even though
// the registry never mutates an inserted entry. Both are needed.
func SameRegistration(a, b Host) bool {
	return a.Equal(b) && a.Generation == b.Generation
}

// SameRegistration reports whether name is currently registered as the same
// insert captured describes: present, carrying the same configured content,
// and stamped with the same generation. It is the identity recheck for
// callers that captured an entry and are about to act on it — attach paths
// before publishing a channel, host rows before folding retained state: a
// name whose entry was removed, or removed and re-added even byte-identically
// (the re-add takes a fresh generation from the registry-wide counter), now
// resolves to a different insert, and the caller must refuse to act on its
// stale capture. The content comparison rides along even though the registry
// never mutates an inserted entry: Host is a plain exported value, and a
// hand-built capture with a current generation but stale content must not
// pass for the live registration.
func (r *Registry) SameRegistration(name string, captured Host) bool {
	current, ok := r.Get(name)
	return ok && SameRegistration(current, captured)
}

// Remove deletes the host registered under name along with its upstream edges.
// A removed host stays removed: Get and All no longer report it, and a later
// Add of the same name starts clean rather than inheriting the old edges (a
// stale edges entry under the re-added name would false-positive the cycle
// check against upstreams that no longer apply). The registry-wide generation
// counter needs no cleanup here — it only advances, so the re-add's Add
// assigns the next generation and the re-added entry stays distinguishable
// from the one Remove deleted even when every configured byte matches.
//
// Edges recorded on other hosts that name the removed host are left alone: the
// cycle walk already treats an unknown upstream as a leaf, so they dangle
// harmlessly until the target is re-added or the dependent is removed.
func (r *Registry) Remove(name string) error {
	// Trimmed like every other name, so a padded spelling removes its host
	// instead of failing as unknown while the host stays registered.
	name = strings.TrimSpace(name)
	if err := ValidateName(name); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.hosts[name]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownHost, name)
	}
	delete(r.hosts, name)
	delete(r.edges, name)
	return nil
}

// Names returns every registered host's name sorted by name: the name set
// of All() without the entry copies, for callers that only need membership.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.hosts))
	for name := range r.hosts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
		hosts = append(hosts, cloneHost(r.hosts[name]))
	}
	return hosts
}
