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
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"

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
	// ErrInvalidKeyPath marks a key_path that is not an absolute path: the
	// value goes to ssh verbatim, and a relative path would resolve against
	// whatever working directory the hub happened to launch with.
	ErrInvalidKeyPath = errors.New("invalid key_path")
	// ErrUnknownHost marks an operation naming a host the registry does not hold.
	ErrUnknownHost = errors.New("unknown host")
	// ErrStaleStamp marks an update whose entry carries a matching pending stamp
	// whose generation no longer advances past the live entry's: the durable
	// write the caller made recorded that generation, and applying a different
	// one would leave the file and the live entry disagreeing.
	ErrStaleStamp = errors.New("stamped generation does not advance")
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
	// Hosts with it zero, Add (AddWithUpstreams) mints the next generation
	// unless the entry carries a complete identity Stamp minted for it — the
	// durable-first add path's reservation — and an attach that captured an
	// entry pins itself to the generation as much as to the content: content
	// equality alone cannot tell a removed entry from its re-added twin (see
	// Equal).
	Generation uint64
	// IncarnationID is the opaque server-generated identity minted beside the
	// generation on every add/re-add and never reused (registry spec 08 §1):
	// "minted fresh on every add/re-add in the same atomic hub.toml write that
	// mints the generation, persisted per live entry alongside it (never
	// derived from the generation, never reused across incarnations even when
	// generation numbers collide)" (§4). An update advances the generation but
	// is neither an add nor a re-add, so it keeps the incarnation id it
	// replaces. The pair (Generation, IncarnationID) is the guarded-mutation
	// and dedup identity every later slice pins.
	IncarnationID string
	// PresenceEpoch is the per-host presence counter spec 08 §1 defines:
	// "advanced on every add, remove, re-add, and expiry purge", persisted in
	// the name's hub.toml machine record and per tombstone, and mirrored into
	// the operation store's boundary record. It is monotonic per name and never
	// lowered: a removal advances it so a re-add can never reuse the removed
	// incarnation's epoch.
	PresenceEpoch uint64
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
	if entry.KeyPath != "" && !filepath.IsAbs(entry.KeyPath) {
		return fmt.Errorf("%w: host %q key_path %q must be an absolute path (the hub performs no ~ or environment expansion)", ErrInvalidKeyPath, entry.Name, entry.KeyPath)
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
	// presence is the per-name presence counter (spec 08 §1): advanced by every
	// add and remove — insertion and re-insertion included — and never lowered.
	// A name's count survives its removal, which is what keeps a re-add from
	// reusing the removed incarnation's presence epoch. Unlike gen it is per
	// name by definition: the epoch is per-host state the file persists per
	// name, and the map's size is bounded by the names the process has seen.
	presence map[string]uint64
	// pending holds the identity Stamp minted for a name whose mutation has not
	// been applied yet, one per name: Add and Update consume it only when the
	// entry carries exactly it (consumeStampLocked), which is what proves the
	// identity came from this registry's Stamp rather than from a caller. The
	// map is bounded by the names a mutation was stamped for, and each entry is
	// replaced by the name's next stamp or consumed by its insert/edit.
	pending map[string]Identity
}

// New validates every entry and builds a registry. Entries are added in order,
// so a duplicate name fails with ErrDuplicateHost. Entries carry no upstream
// edges; a config-loaded host gets them from SetUpstreams once component 05 has
// learned them (AddWithUpstreams inserts, so it cannot be used for a host New
// already registered).
//
// New is the boot load's entry point, so each entry carries the identity the
// durable file persisted and New keeps it: a restored generation is held and
// the registry counter moves above it (spec 08 §15: "The boot load restores
// persisted generations before the store serves any request"), a restored
// incarnation id is held unchanged, and a restored presence epoch is the
// name's counter. What the file did not persist is minted at load as spec §15
// prescribes — "boot mints a fresh incarnation id for every hub.toml host name
// with no persisted incarnation" — with the name's first presence value of 1;
// a hub.toml host that predates the machine records therefore loads complete
// and the caller's boot write records the pair, so the pair survives the next
// reload unchanged. A host with no persisted record is never mistaken for a
// re-registration: no generation is minted above any mark (it keeps the
// initial assignment), the name stays live, and nothing a re-add's mint does —
// a generation above a retained high-water mark, a purge of the removed name's
// history — happens here.
func New(entries []Host) (*Registry, error) {
	return NewSeeded(entries, nil)
}

// NewSeeded is New with the durable file's retained high-water marks: the
// counters start above every mark before the first entry is inserted, so the
// initial identity a hub.toml host with no persisted record is minted below can
// never land at or below a mark the file still carries (spec 08 §1: "Re-add
// mints strictly above every retained high-water mark for the name"). New
// passes nil; the boot load, which has the file's [generations] records in
// hand, passes them so the whole registry starts above what it read.
func NewSeeded(entries []Host, marks map[string]HighWater) (*Registry, error) {
	r := &Registry{
		hosts:    make(map[string]Host, len(entries)),
		edges:    make(map[string][]string, len(entries)),
		presence: make(map[string]uint64, len(entries)),
	}
	r.SeedHighWater(marks)
	for _, entry := range entries {
		if err := r.seed(entry); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// seed inserts one boot-loaded entry, preserving the identity the durable file
// persisted and minting only what it did not carry. New is its only caller.
func (r *Registry) seed(entry Host) error {
	entry = Normalize(entry)
	if err := validateEntry(entry); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.hosts[entry.Name]; ok {
		return fmt.Errorf("%w: %q", ErrDuplicateHost, entry.Name)
	}
	return r.seedLocked(entry)
}

// seedLocked gives entry the identity a boot-loaded entry carries: the
// persisted generation when it has one (with the registry counter moved above
// it), a freshly minted incarnation id when the file carried none, and the
// name's first presence value when the file carried none. Callers hold r.mu.
func (r *Registry) seedLocked(entry Host) error {
	if entry.Generation == 0 {
		entry.Generation = r.gen + 1
	}
	if entry.Generation > r.gen {
		r.gen = entry.Generation
	}
	if entry.IncarnationID == "" {
		incarnation, err := mintIncarnationID()
		if err != nil {
			return err
		}
		entry.IncarnationID = incarnation
	}
	if entry.PresenceEpoch == 0 {
		// A boot-loaded host with no persisted pair is a new incarnation, and
		// its first presence value advances past whatever the file's mark for
		// the name retained (the marks seed the counter before this runs), so
		// the name never records a value the file already recorded — not even
		// the removed incarnation's epoch.
		entry.PresenceEpoch = r.nextPresenceLocked(entry.Name)
	}
	if entry.PresenceEpoch > r.presence[entry.Name] {
		r.presence[entry.Name] = entry.PresenceEpoch
	}
	r.hosts[entry.Name] = entry
	return nil
}

// Identity is one minted (generation, incarnation id, presence epoch) triple:
// the identity Stamp reserves and the durable-first mutation path records
// before the live change lands.
type Identity struct {
	Generation    uint64
	IncarnationID string
	PresenceEpoch uint64
}

// Stamp mints the identity this name's next mutation will carry, without
// applying the mutation: a free name gets an insert identity (the registry's
// next generation, a freshly minted incarnation id, and the name's presence
// epoch advanced by one), and a live name gets a replace identity (the next
// generation, with the live incarnation id and presence epoch preserved —
// spec 08 §4: an update advances the generation, and nothing else).
//
// The mint is separable because the durable-first mutation paths write hub.toml
// before they change anything live, and spec 08 §15 requires the write that
// first records the host to also record its identity — "minted fresh on every
// add/re-add in the same atomic hub.toml write that mints the generation". So
// the caller stamps, persists the stamped triple, and then applies the change
// through Add/Update, which keep a complete identity the entry carries instead
// of minting a second one.
//
// Stamping advances the counters, so the stamped values are the counter's own
// next values and no other mint can produce them. A stamp whose mutation never
// lands leaves a gap — and a gap is exactly what "monotone and never reused"
// means: the next mutation takes the value after it.
func (r *Registry) Stamp(entry Host) (Identity, error) {
	entry = Normalize(entry)
	if err := validateEntry(entry); err != nil {
		return Identity{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	identity := Identity{Generation: r.nextGenerationLocked()}
	if live, ok := r.hosts[entry.Name]; ok {
		// A replace keeps the live identity's incarnation and presence epoch;
		// the name's next generation is the one thing an edit advances.
		identity.IncarnationID = live.IncarnationID
		identity.PresenceEpoch = live.PresenceEpoch
	} else {
		identity.PresenceEpoch = r.nextPresenceLocked(entry.Name)
		incarnation, err := mintIncarnationID()
		if err != nil {
			return Identity{}, err
		}
		identity.IncarnationID = incarnation
	}
	r.gen = identity.Generation
	if identity.PresenceEpoch > r.presence[entry.Name] {
		r.presence[entry.Name] = identity.PresenceEpoch
	}
	if r.pending == nil {
		r.pending = map[string]Identity{}
	}
	r.pending[entry.Name] = identity
	return identity, nil
}

// peekStampLocked returns the identity a pending Stamp minted for entry's name,
// when entry carries exactly that identity, without consuming it. It is the
// provenance check the durable-first insert and edit paths rely on: the only way
// a caller can hold an identity that matches a pending stamp is to have received
// it from Stamp, so Add and Update mint rather than trust a caller-supplied
// generation, incarnation id or presence epoch — a hand-built entry that merely
// carries three non-zero values proves nothing. A stamp whose mutation never
// landed stays pending and a later mutation of the same name may consume it,
// which is correct: that identity was minted and never persisted. Callers hold
// r.mu.
func (r *Registry) peekStampLocked(entry Host) (Identity, bool) {
	pending, ok := r.pending[entry.Name]
	if !ok {
		return Identity{}, false
	}
	if entry.Generation != pending.Generation ||
		entry.IncarnationID != pending.IncarnationID ||
		entry.PresenceEpoch != pending.PresenceEpoch {
		return Identity{}, false
	}
	return pending, true
}

// consumeStampLocked is peekStampLocked plus the consumption the insert path
// needs: it clears the pending stamp it returns. Callers hold r.mu.
func (r *Registry) consumeStampLocked(entry Host) (Identity, bool) {
	identity, ok := r.peekStampLocked(entry)
	if ok {
		delete(r.pending, entry.Name)
	}
	return identity, ok
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
	// The identity is assigned under the lock: from the registry-wide counter
	// when the caller stamped none, so a re-add of the same name — byte-
	// identical or not, interleaved with other hosts' churn or not — always
	// carries a generation the removed entry never had.
	if err := r.insertLocked(entry); err != nil {
		return err
	}
	r.edges[entry.Name] = append([]string(nil), upstreamNames...)
	return nil
}

// insertLocked gives entry its identity and stores it: the identity a pending
// Stamp minted for it, when the entry carries exactly that identity (see
// consumeStampLocked), or a fresh mint — the next registry generation, a
// freshly minted incarnation id, and the name's presence epoch advanced by one
// — otherwise. Callers hold r.mu, so advancing the counters and exposing the
// entry remain one atomic operation.
func (r *Registry) insertLocked(entry Host) error {
	identity, stamped := r.consumeStampLocked(entry)
	if !stamped {
		minted, err := r.mintIdentityLocked(entry.Name)
		if err != nil {
			return err
		}
		identity = minted
	}
	entry.Generation, entry.IncarnationID, entry.PresenceEpoch = identity.Generation, identity.IncarnationID, identity.PresenceEpoch
	if entry.Generation > r.gen {
		r.gen = entry.Generation
	}
	if entry.PresenceEpoch > r.presence[entry.Name] {
		r.presence[entry.Name] = entry.PresenceEpoch
	}
	r.hosts[entry.Name] = entry
	return nil
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
	// as a remove/re-add: no generation a capture can hold is ever reused. The
	// incarnation id is the live entry's — an update is neither an add nor a
	// re-add, so it never rotates a never-reused id — and the presence epoch is
	// preserved for the same reason.
	live := r.hosts[entry.Name]
	// The generation is the one thing an edit advances, and only a stamp from
	// this registry may name it. The stamp is inspected before it is consumed,
	// because consuming one this call cannot apply would leave the caller's
	// durable write (which recorded the stamped generation) and the live entry
	// disagreeing: a stamped generation that no longer advances is refused, and
	// everything else — zero, a copy of the live entry's value, a hand-built
	// number — mints, so an update always advances the generation and never
	// adopts a caller's.
	identity, stamped := r.peekStampLocked(entry)
	switch {
	case stamped && identity.Generation > live.Generation:
		entry.Generation = identity.Generation
		delete(r.pending, entry.Name)
	case stamped:
		return fmt.Errorf("%w: the generation %d stamped for %q does not advance past the live %d; re-read the host and stamp again",
			ErrStaleStamp, identity.Generation, entry.Name, live.Generation)
	default:
		entry.Generation = r.nextGenerationLocked()
	}
	if entry.Generation > r.gen {
		r.gen = entry.Generation
	}
	// The incarnation id and presence epoch are the live entry's, never the
	// caller's: an update is neither an add nor a re-add, so it rotates nothing
	// but the generation.
	entry.IncarnationID = live.IncarnationID
	entry.PresenceEpoch = live.PresenceEpoch
	// The name's counter only rises: seeding (SeedHighWater) can have raised it
	// above the live entry's own value, and lowering it here would let a later
	// re-add mint at or below a mark the file already carries.
	if entry.PresenceEpoch > r.presence[entry.Name] {
		r.presence[entry.Name] = entry.PresenceEpoch
	}
	r.hosts[entry.Name] = entry
	return nil
}

// mintIdentityLocked mints a fresh insert identity for name: the registry's
// next generation and the name's presence epoch advanced by one, with a freshly
// minted incarnation id. Callers hold r.mu.
func (r *Registry) mintIdentityLocked(name string) (Identity, error) {
	incarnation, err := mintIncarnationID()
	if err != nil {
		return Identity{}, err
	}
	return Identity{
		Generation:    r.nextGenerationLocked(),
		IncarnationID: incarnation,
		PresenceEpoch: r.nextPresenceLocked(name),
	}, nil
}

// nextGenerationLocked is the one place the registry's next generation comes
// from: the counter's next value. Callers hold r.mu.
func (r *Registry) nextGenerationLocked() uint64 { return r.gen + 1 }

// HighWater is one name's retained high-water record: the presence epoch the
// name's last presence event reached and the greatest generation it carried. A
// boot that reads the durable file seeds the registry with these before any
// mutation, so a name whose live entry is gone still mints above what the file
// recorded — spec 08 §1: "Re-add mints strictly above every retained high-water
// mark for the name" — and a removed name's presence epoch is never restarted
// at a value the file already carried.
type HighWater struct {
	Generation    uint64
	PresenceEpoch uint64
}

// SeedHighWater raises the registry's counters to the retained marks a boot
// read from the durable file: the registry-wide generation counter moves above
// every mark, and each name's presence counter rises to its mark. Counters
// only ever rise here — a mark below a counter the registry already reached
// changes nothing — so seeding is safe to run beside live entries the boot
// already restored, and it is idempotent.
func (r *Registry) SeedHighWater(marks map[string]HighWater) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.presence == nil {
		r.presence = map[string]uint64{}
	}
	for name, mark := range marks {
		if mark.Generation > r.gen {
			r.gen = mark.Generation
		}
		if mark.PresenceEpoch > r.presence[name] {
			r.presence[name] = mark.PresenceEpoch
		}
		// A live entry carries the name's counter: seeding can arrive after the
		// entry was minted (a registry built by New and seeded later), and an
		// entry left below the counter would let the next write lower the file's
		// retained mark when it records the entry's epoch.
		if live, ok := r.hosts[name]; ok && live.PresenceEpoch < r.presence[name] {
			live.PresenceEpoch = r.presence[name]
			r.hosts[name] = live
		}
	}
}

// NextPresenceEpoch returns the presence epoch name's next presence event
// carries: one above the name's highest recorded value, exactly the rule the
// registry's own add and remove paths apply. It is exported for the
// durable-first removal, which must record the advance in hub.toml before the
// registry's own Remove runs — the removal's write lands before any live state
// changes.
func (r *Registry) NextPresenceEpoch(name string) uint64 {
	name = strings.TrimSpace(name)
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.nextPresenceLocked(name)
}

// nextPresenceLocked is the one place the advance's arithmetic lives: one above
// the name's highest recorded value, counting both the counter and (defensively)
// a live entry whose epoch somehow runs ahead of it. Callers hold r.mu.
func (r *Registry) nextPresenceLocked(name string) uint64 {
	next := r.presence[name]
	if live, ok := r.hosts[name]; ok && live.PresenceEpoch > next {
		next = live.PresenceEpoch
	}
	return next + 1
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

// Remove deletes the host registered under name along with its upstream edges,
// and advances the name's presence counter (spec 08 §1: the counter advances on
// every remove, so a re-add can never reuse the removed incarnation's presence
// epoch). The durable-first removal records the advanced triple before it tears
// the live entry down — the removal's hub.toml write lands before any live
// state changes — so it computes the same value with NextPresenceEpoch, which
// is the one place the advance's arithmetic lives.
//
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
	host, ok := r.hosts[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownHost, name)
	}
	delete(r.hosts, name)
	delete(r.edges, name)
	// A pending stamp describes the live entry this call removes (or an insert
	// that never landed); either way it must not survive to be consumed by a
	// later insert, which would re-adopt the removed incarnation's id and epoch.
	delete(r.pending, name)
	host.PresenceEpoch = r.nextPresenceLocked(name)
	r.presence[name] = host.PresenceEpoch
	return nil
}

// mintIncarnationID mints one incarnation id: spec 08 §1's "opaque
// server-generated string minted beside the generation on every add/re-add,
// never derived from it and never reused: at most 128 bytes, and the generator
// pins its output to 36 bytes (canonical UUID text)". The pin is what keeps the
// deploy-pipeline spec §8 cursor bound (8 KiB) true by construction. A failure
// is a csprng failure — no safe identity can be minted — so it is returned, not
// papered over with a predictable value.
func mintIncarnationID() (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("mint incarnation id: %w", err)
	}
	return id.String(), nil
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
