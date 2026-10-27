package hub

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// hostOnline reports whether host currently has a live channel. It reads the
// attached-only signal without spawning SSH: the dial seam belongs to
// evener/host/attach alone. It is the gate on the no-manager fallback only: a
// manager-backed row never consults it, because attachedClient confirms the
// live channel itself, so the source registry's fail-open default (Online()
// true with no signal wired) cannot mark a host attached that nothing dialed.
func (m *hubHostManager) hostOnline(host string) bool {
	if m.cfg.online != nil {
		return m.cfg.online(host)
	}
	// No signal wired (tests, embedders): fall back to the source registry's
	// own Online, which defaults true with no signal — the pre-06 default.
	// An unknown name is offline: the registry entry exists (the caller
	// resolved it), so a missing source means no channel was ever wired for
	// it. Every source this manager creates carries an explicit signal, so
	// this path cannot recurse.
	if m.cfg.sources == nil {
		return false
	}
	source, ok := m.cfg.sources.Source(host)
	if !ok {
		return false
	}
	online, ok := source.(appsource.OnlineSource)
	if !ok {
		return true
	}
	return online.Online()
}

// hostEntryRow builds the row fields every host row carries from the effective
// entry: the configured values and a clone of roots (never an alias — the row
// crosses the wire and a caller mutating its Roots must not be able to reach
// back into the registry entry). List, status, add, update, and remove rows all
// start from this, so the removal response carries exactly the entry it removed
// instead of a hand-picked subset that can silently drift from the wire shape.
// The origin marker is set here because it is one value: every host lives in
// the machine-managed hub.toml (registry spec 08 §6/§19, decided 2026-09-26;
// the spec-correction PR carries the §6/§19 rewrite).
func hostEntryRow(host hostreg.Host) appwire.HostRow {
	row := appwire.HostRow{
		Name:          host.Name,
		Address:       host.SSH,
		User:          host.User,
		KeyPath:       host.KeyPath,
		EvenerPath:    host.EvenerPath,
		ConfigPath:    host.ConfigPath,
		Addr:          host.Addr,
		Roots:         slices.Clone(host.Roots),
		Origin:        hostOriginHubTOML,
		Generation:    host.Generation,
		IncarnationID: host.IncarnationID,
	}
	return row
}

// hostRow renders one host's list row: the effective entry fields plus live
// state. Attached rows read the live channel's handshake and preflight facts
// through the attached-only lookups and record them as last-known — but only
// the channel paired with this row's entry, never one built from another
// generation of the name (attachedClient); a failed
// lookup keeps the previously retained fields instead of blanking them, so a
// transient probe failure cannot cost a later offline row its facts. Offline
// and in-progress rows render the retained attach state (midAttach,
// lastAttachError) and last-known facts from the record. It never dials:
// every seam here is attached-only. ctx is the caller's handler context — the
// facts read runs on it, so a caller that goes away cancels the read; List,
// Status, and Add build their rows without holding the mutation mutex, so a
// parked facts read holds up no commit either. The one
// mutex the row build itself takes is the brief mutation-mutex hold around
// the fenced retained-state fold at the end — after every network read has
// returned — so the parked read the fence exists for still holds up no
// commit.
func (m *hubHostManager) hostRow(ctx context.Context, host hostreg.Host) appwire.HostRow {
	row := hostEntryRow(host)
	// Attached is reported only when attachedClient confirms a live channel
	// built from the very entry this row renders — a channel under the same
	// name but another registration leaves the row offline rather than
	// borrowing its attached state. With a manager wired that confirmation is
	// the liveness signal itself: attachedClient resolves the channel once,
	// and reading the name again through hostOnline would resolve the same
	// channel a second time. The signal alone would not be sufficient anyway:
	// with no signal wired a hub.toml source fails open (Online() true), and
	// trusting that would render every configured host Attached with no facts
	// behind it — an online row the UI then refuses to Connect because it
	// looks already up. With no manager (tests, embedders) there is no
	// channel identity to confirm, so hostOnline stays the gate on the
	// name-only fallback seam, and no lookup wired leaves the row honestly
	// offline too: nothing can confirm a channel, so nothing may claim one.
	//
	// validity tracks which live lookups refreshed the row's fact fields, so
	// the retention below keeps the previously known values for the fields
	// whose lookup failed instead of blanking them with the failed read's
	// empties.
	var validity hostFactsValidity
	var (
		client   *appwire.Client
		attached bool
	)
	if m.cfg.manager != nil {
		client, attached = m.attachedClient(host)
	} else if m.hostOnline(host.Name) {
		client, attached = m.attachedClient(host)
	}
	if attached && client != nil {
		row.Attached = true
		if m.cfg.handshake != nil {
			if hs, ok := m.cfg.handshake(host.Name, client); ok {
				row.ServerName = hs.ServerInfo.Name
				row.ServerVersion = hs.ServerInfo.Version
				validity.handshake = true
			}
		}
		if m.cfg.facts != nil {
			if facts, err := m.cfg.facts(ctx, host.Name, client); err == nil {
				row.HubVersion = facts.HubVersion
				row.OS = facts.OS
				row.Arch = facts.Arch
				validity.facts = true
			}
			// A facts-read failure keeps the row attached: the dial
			// the attach already completed is authoritative
			// (app_host_attach.go's dial-authoritative rule), and a
			// failed facts read is not a detach. The row's empty fields
			// are the honest render of the failed read; what the row
			// retains for its later offline rows is decided by validity.
		}
	}
	// The retained-state fold and record are fenced on the entry generation
	// the row was built from: the row build runs outside the mutation mutex
	// so a parked facts read holds up no commit, and an unfenced recordKnown
	// would let a delayed row finish after a remove/re-add and recreate the
	// removed host's name-keyed attach record with facts the re-added host's
	// offline rows then rendered as their own. The fence and the state writes
	// share the mutation mutex, so
	// they serialize with both record drops (Remove's finish phase and Add's
	// clean re-add): a row that wins the race records before the removal's
	// drop deletes its record, and a row that loses sees the moved or missing
	// generation and records nothing. Either order leaves the re-added name's
	// record exactly as clean as its own commit left it.
	//
	// The fence also excludes a name whose retained state is in flight. A
	// mutation holds the name's mark from its commit phase to its finish
	// phase, and that span covers the window where an update's new entry is
	// already visible in the registry but its retirement has not run — the
	// swap and the retire hook are adjacent only inside the per-host gate,
	// with the mutation mutex released between them. A concurrent, gate-free
	// row that snapshots the new, higher-generation entry in that window still
	// passes hostEntryCurrent, and gen > retiredThrough still holds because
	// the retire has not run, so without this condition the row would fold the
	// retired identity's midAttach, lastAttachError, and last-known facts.
	// Suppressing both the fold and the record for the whole mark means no row
	// can fold a retired identity's state, and the rows render honestly blank
	// until the mutation's finish phase, after which the next row build folds
	// whatever the mutation left behind. Two consequences are deliberate:
	// during a removal window the host's own row also renders without its
	// retained state — the destination state of a removal anyway — and an
	// attached host's facts lookup that succeeds during the window is simply
	// re-recorded on the next row build.
	m.cfg.mu.Lock()
	if m.hostEntryCurrent(host) && !m.isMutating(host.Name) {
		m.cfg.state.apply(&row, host.Generation)
		if row.Attached {
			m.cfg.state.recordKnown(row, validity, host.Generation)
		}
	}
	m.cfg.mu.Unlock()
	return row
}

// hostEntryCurrent reports whether the live registry still holds host's name
// under the same registration host carries — the registration the row was
// built from, unchanged by any remove or remove/re-add: a re-add is a new
// insert (hostreg's registry-wide generation counter never reuses one), and
// a removed name has no live entry at all. It is hostreg's own
// same-registration predicate, so the row fence and the SSH manager's attach
// identity rechecks cannot drift apart. Callers hold the mutation mutex.
func (m *hubHostManager) hostEntryCurrent(host hostreg.Host) bool {
	return m.cfg.hosts.SameRegistration(host.Name, host)
}

// List returns every known host with truthful online state in name-sorted
// order (the registry's own; every row's origin field reads `hub.toml`).
// Attached rows report live channel facts and retain them
// as last-known; rows without a live channel render as offline with the
// retained attach state and last-known facts. It never dials.
//
// The mutation mutex covers only the snapshot — the registry rows — so a
// concurrent reader never observes the window between a registry insert and
// the store row and source registration that finish it: a half-committed host
// would list with no source.
// The row building that follows runs lock-free: hostRow's
// attached-only lookups and its facts read run on the network, and one slow
// or hung host must not block every concurrent Add and Remove commit or
// serialize other lists. The rows are the snapshot's point-in-time view: a
// host added after the snapshot is absent from that response, never
// half-committed in it. A host mid-removal still lists while its registry
// entry lasts — the removal mark stands in for the store row its commit
// already dropped — and every row carries the one origin marker, because every
// host lives in the machine-managed hub.toml.
func (m *hubHostManager) List(ctx context.Context, _ appwire.EmptyParams) (appwire.HostListResponse, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostListResponse{}, err
	}
	m.cfg.mu.Lock()
	hosts := m.cfg.hosts.All()
	m.cfg.mu.Unlock()
	// rows is non-nil even when no host is registered: make returns a usable
	// empty slice, and the wire type's non-nullable hosts array must never
	// marshal as JSON null.
	rows := make([]appwire.HostRow, 0, len(hosts))
	live := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		live[host.Name] = struct{}{}
		rows = append(rows, m.hostRow(ctx, host))
	}
	// Tombstone rows render from the durable tombstone set: `removed: true`
	// plus the retained-row count, from the tombstone's retained effective
	// HostConfig with attached/midEnsure false and the removed entry's origin,
	// generation, and incarnation id; the facts/error optionals stay absent
	// (spec §4/§11). The expiry filter below is IN MEMORY only — `list` takes
	// no mutation lock and prunes nothing durably (spec §15: "the read path
	// omits it from the response without taking the lock, and the next
	// mutation-path atomic hub.toml write under the lock prunes it durably");
	// a tombstone whose name still holds an open teardown remnant is rendered
	// even past its retention (the gate keeps it alive).
	for name, tombstone := range m.cfg.store.tombstoneSnapshot() {
		if _, isLive := live[name]; isLive {
			// Defensive only: the derivation drops a live name's tombstone, so
			// a loaded set cannot hold one.
			continue
		}
		if !m.remnantGated(name) && tombstoneExpired(tombstone, m.nowTime(), m.cfg.policy.tombstoneRetention) {
			continue
		}
		rows = append(rows, tombstoneRow(tombstone))
	}
	// Every row — live or tombstone — carries the remnant fence's own fields
	// (spec §11): `openRemnantId` exactly when the name holds an open remnant,
	// and `escalationAgeSec` when that remnant is past the escalation bound, so
	// a reader can name the blocking remnant for the teardown-retry affordance
	// without a second lookup.
	for i := range rows {
		m.stampRemnantRow(&rows[i])
	}
	slices.SortStableFunc(rows, func(a, b appwire.HostRow) int { return strings.Compare(a.Name, b.Name) })
	return appwire.HostListResponse{Hosts: rows}, nil
}

// Status returns one host's row: the same HostRow evener/host/list serves.
// Unknown names are InvalidParams. Never dials. It snapshots the host under
// the same mutation mutex List does, for the same
// fully-committed-row guarantee, and then builds the row lock-free for the
// same reason List does: the facts read must not hold up commits.
func (m *hubHostManager) Status(ctx context.Context, params appwire.HostStatusParams) (appwire.HostStatusResponse, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostStatusResponse{}, err
	}
	name := strings.TrimSpace(params.Name)
	m.cfg.mu.Lock()
	host, ok := m.cfg.hosts.Get(name)
	if !ok {
		m.cfg.mu.Unlock()
		return appwire.HostStatusResponse{}, appwire.InvalidParams(fmt.Sprintf("unknown host %q", name))
	}
	m.cfg.mu.Unlock()
	row := m.hostRow(ctx, host)
	m.stampRemnantRow(&row)
	return appwire.HostStatusResponse{Host: row}, nil
}

// stampRemnantRow fills a row's remnant-fence fields from the durable remnant
// set: `openRemnantId` exactly when the name holds an open remnant, and
// `escalationAgeSec` only when that remnant is past the escalation bound. Both
// stay absent otherwise, per the absent-when-unknown rule.
func (m *hubHostManager) stampRemnantRow(row *appwire.HostRow) {
	remnantID, open := m.cfg.store.markedRemnantFor(row.Name)
	if !open {
		return
	}
	row.OpenRemnantID = remnantID
	if remnant, ok := m.cfg.store.remnantByID(remnantID); ok {
		row.EscalationAgeSec = m.escalationAgeSec(remnant)
	}
}

// sortedStagedMarkerNames returns a staged-marker set's host names in sorted
// order, so the boot's finalization pass walks a deterministic order.
func sortedStagedMarkerNames(markers map[string]HostStagedReceipt) []string {
	names := make([]string, 0, len(markers))
	for name := range markers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
