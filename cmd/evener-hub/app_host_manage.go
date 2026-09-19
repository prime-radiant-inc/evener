package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
	"primeradiant.com/evener/internal/appserver"
)

// hostOriginHubTOML and hostOriginSidecar are the HostRow origin markers: the
// entry's source after the hub.toml-then-sidecar merge. hub.toml is
// authoritative for its own names; the sidecar is the UI's only writable
// source.
const (
	hostOriginHubTOML = "hub.toml"
	hostOriginSidecar = "sidecar"
)

// hostSidecarFileName is the managed sidecar beside the selected hub.toml that
// carries UI-added host entries. hub.toml stays hand-authored (a TOML
// re-marshal would strip its comments), so the UI never rewrites it; the hub
// loads this file after hub.toml and merges the two sets.
const hostSidecarFileName = "hub.hosts.json"

// hostManagerConfig carries what the host-management handlers need. The
// registry is the controller's live one in production — the same
// *hostreg.Registry the SSH manager dials through and the attach handler
// validates against — so a host added at runtime is attachable without a
// restart. The stores are live: the hub owns them, mutations swap entries
// under mu, and reads never dial.
type hostManagerConfig struct {
	// hosts is the live host registry: hub.toml entries at boot plus sidecar
	// entries as they are added. Mutations hold mu; the SSH manager and the
	// attach handler consult the same instance in production.
	hosts *hostreg.Registry
	// sidecar holds the UI-added entries in add order. It is the durable side
	// of hosts for sidecar names; hub.toml names are never recorded here.
	sidecar *hostSidecarStore
	// sidecarPath is the sidecar file beside the selected hub.toml. Empty
	// (tests, embedders without a file) disables persistence: the store stays
	// memory-only and every method still works.
	sidecarPath string
	// sources is the component-05 registry; a remote host's source is where
	// attachment state (Online) and the per-host client live.
	sources *appsource.Registry
	// remoteCache is the controller's remote-thread snapshot cache. Remove
	// prunes the removed host's rows from it, so its sessions stop rendering
	// with the removal instead of lingering live until the refresher's next
	// tick. Nil (tests, embedders without a cache): every tree read then
	// walks the live sources per request, and a removed host — no source —
	// contributes no rows on its own.
	remoteCache *hubcore.RemoteThreadCache
	// manager owns every live SSH channel; removal goes through its atomic
	// RemoveHost so a concurrent attach cannot publish past deregistration.
	// Nil in tests that only exercise validation.
	manager *sshconn.Manager
	// client is the Ensure-backed dialing seam a new source's client func
	// uses (cfg.RemoteHostClient). Nil (tests, embedders) leaves the source
	// on the detached refusal remoteClientFor serves.
	client func(ctx context.Context, host string) (*appwire.Client, error)
	// online reports whether the controller's channel to host is currently
	// attached. Nil leaves every remote host to the source registry's own
	// Online (tests).
	online func(host string) bool
	// clientIfAttached is the non-dialing attached-only client lookup list
	// and status resolve through. Nil leaves rows without live facts.
	clientIfAttached func(host string) (*appwire.Client, bool)
	// handshake returns the attach handshake facts for the channel behind
	// client, or false when no live channel backs it. Nil leaves rows
	// without handshake identity.
	handshake func(host string, client *appwire.Client) (appwire.InitializeResponse, bool)
	// facts returns the preflight facts for the connection behind client.
	// Nil leaves rows without preflight facts.
	facts func(ctx context.Context, host string, client *appwire.Client) (appsource.HostFacts, error)
	// state retains per-host attach state from the manager's lifecycle
	// events plus the last-known facts of the last attached render, so
	// offline and in-progress rows keep the metadata the wire contract
	// promises.
	state *hostAttachState
	// logf is the hub's logging path for sidecar load problems; nil drops
	// the lines (tests that never load a broken file).
	logf func(format string, args ...any)
	// mu serializes add/remove read-modify-write cycles so concurrent calls
	// cannot lose updates or interleave a save with a registry mutation.
	mu sync.Mutex
}

// hostSidecarStore is the durable sidecar: UI-added entries in add order.
// The zero value is usable; all methods are safe for concurrent use. Callers
// that also mutate the registry hold hostManagerConfig.mu across both, so the
// two cannot drift apart under concurrency.
type hostSidecarStore struct {
	mu      sync.Mutex
	entries []hostreg.Host
	// loadErr records why the on-disk sidecar cannot be treated as fully
	// loaded: the file failed to parse, or an entry failed validation. While
	// it is set the in-memory snapshot is known incomplete, so saves refuse —
	// rewriting the file would clobber the entries that never made it into
	// memory — and add/remove fail loudly instead of silently losing them.
	loadErr error
}

// poison records err as the reason the on-disk sidecar is not fully loaded.
// The first reason wins; later ones only add log lines at the call site.
func (s *hostSidecarStore) poison(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr == nil {
		s.loadErr = err
	}
}

// poisoned reports why the sidecar's on-disk entries cannot be trusted as
// fully loaded, or nil when every entry is loaded (or there is no file).
func (s *hostSidecarStore) poisoned() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadErr
}

// hostSidecarFile is the on-disk shape of the sidecar: entries in add order.
type hostSidecarFile struct {
	Hosts []hostSidecarFileEntry `json:"hosts"`
}

// hostSidecarFileEntry is one persisted sidecar entry: the seven HostConfig
// fields in snake_case (hub.toml's spelling for the same fields) plus the key
// path. The sidecar never crosses the wire — the hub is its only writer and
// reader — so it follows the repo's snake_case json default; the appwire
// package's camelCase HostRow is what clients see.
type hostSidecarFileEntry struct {
	Name       string   `json:"name"`
	SSH        string   `json:"ssh"`
	User       string   `json:"user,omitempty"`
	EvenerPath string   `json:"evener_path,omitempty"`
	ConfigPath string   `json:"config_path,omitempty"`
	Addr       string   `json:"addr,omitempty"`
	Roots      []string `json:"roots,omitempty"`
	KeyPath    string   `json:"key_path,omitempty"`
}

// sidecarPathFor returns the sidecar path beside the selected hub.toml. An
// empty config path (tests, embedders without a file) disables persistence:
// the store stays memory-only and every method still works.
func sidecarPathFor(configPath string) string {
	if strings.TrimSpace(configPath) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(configPath), hostSidecarFileName)
}

// loadHostSidecar reads path into entries in file order. A missing file is an
// empty sidecar, not an error: a hub that never added a host has no file.
func loadHostSidecar(path string) ([]hostreg.Host, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read host sidecar: %w", err)
	}
	var file hostSidecarFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse host sidecar: %w", err)
	}
	entries := make([]hostreg.Host, 0, len(file.Hosts))
	for _, h := range file.Hosts {
		entries = append(entries, hostreg.Host{
			Name:       h.Name,
			SSH:        h.SSH,
			User:       h.User,
			EvenerPath: h.EvenerPath,
			ConfigPath: h.ConfigPath,
			Addr:       h.Addr,
			Roots:      h.Roots,
			KeyPath:    h.KeyPath,
		})
	}
	return entries, nil
}

// saveHostSidecar persists entries atomically: a 0600 temp file in the same
// directory, fsynced, renamed over the target, so a crash lands the old file
// or the new one, never a half-write. 0600 keeps key paths from ever landing
// world-readable. An empty path (no config file) skips the write; the
// in-memory store stays authoritative for the process lifetime.
func saveHostSidecar(path string, entries []hostreg.Host) error {
	if path == "" {
		return nil
	}
	file := hostSidecarFile{Hosts: make([]hostSidecarFileEntry, 0, len(entries))}
	for _, e := range entries {
		file.Hosts = append(file.Hosts, hostSidecarFileEntry{
			Name:       e.Name,
			SSH:        e.SSH,
			User:       e.User,
			EvenerPath: e.EvenerPath,
			ConfigPath: e.ConfigPath,
			Addr:       e.Addr,
			Roots:      e.Roots,
			KeyPath:    e.KeyPath,
		})
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal host sidecar: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("host sidecar mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hub.hosts.json-*.tmp")
	if err != nil {
		return fmt.Errorf("host sidecar temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("host sidecar chmod: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("host sidecar write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("host sidecar sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("host sidecar close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("host sidecar rename: %w", err)
	}
	return nil
}

// add inserts entry at the end. Callers hold hostManagerConfig.mu.
func (s *hostSidecarStore) add(entry hostreg.Host) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
}

// remove deletes name, reporting whether it was present. Removed stays
// removed: nothing is retained, so a later add of the same name starts clean.
// Callers hold hostManagerConfig.mu.
func (s *hostSidecarStore) remove(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range s.entries {
		if e.Name == name {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			return true
		}
	}
	return false
}

// snapshot returns entries in add order; the slice is a copy. Callers hold
// hostManagerConfig.mu.
func (s *hostSidecarStore) snapshot() []hostreg.Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]hostreg.Host(nil), s.entries...)
}

// without returns a copy of the entries minus name, in add order — the
// snapshot a removal persists before it mutates anything. Callers hold
// hostManagerConfig.mu.
func (s *hostSidecarStore) without(name string) []hostreg.Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]hostreg.Host, 0, len(s.entries))
	for _, e := range s.entries {
		if e.Name != name {
			out = append(out, e)
		}
	}
	return out
}

// isSidecar reports whether name is a live sidecar entry.
func (s *hostSidecarStore) isSidecar(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.Name == name {
			return true
		}
	}
	return false
}

// hostAttachRecord is one host's retained attach state: what the lifecycle
// events say about an in-progress or failed attach, plus the last-known facts
// of the last row that rendered attached.
type hostAttachRecord struct {
	midAttach bool
	lastErr   string
	known     appwire.HostRow // only the fact fields are read back
}

// hostAttachState retains per-host attach records from the SSH manager's
// lifecycle events and from attached rows this surface renders, so offline
// and in-progress rows keep the metadata the wire contract promises (a host
// mid-attach renders midAttach; a host that failed renders lastAttachError;
// an offline row keeps its last-known facts). Records for removed hosts are
// dropped, so a re-add starts clean.
type hostAttachState struct {
	mu      sync.Mutex
	records map[string]*hostAttachRecord
}

func newHostAttachState() *hostAttachState {
	return &hostAttachState{records: map[string]*hostAttachRecord{}}
}

// observe records one SSH lifecycle event. It only writes the map: it runs
// from sshconn's OnEvent with the per-host lock held, so it must never call
// back into the manager.
func (s *hostAttachState) observe(ev sshconn.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[ev.Host]
	if rec == nil {
		rec = &hostAttachRecord{}
		s.records[ev.Host] = rec
	}
	switch ev.Kind {
	case sshconn.EventAttached:
		// A fresh attach supersedes the previous attempt's error and ends
		// the in-progress state.
		rec.midAttach = false
		rec.lastErr = ""
	case sshconn.EventFailed:
		rec.midAttach = false
		if ev.Err != nil {
			rec.lastErr = ev.Err.Error()
		}
	case sshconn.EventDetached:
		rec.midAttach = false
	case sshconn.EventState:
		switch ev.State {
		case sshconn.StatePreflighting, sshconn.StateDeploying, sshconn.StateRestarting,
			sshconn.StateAttaching, sshconn.StateReconnecting:
			rec.midAttach = true
		default:
			rec.midAttach = false
		}
	}
}

// hostFactsValidity records which of an attached row's fact fields the live
// lookups actually refreshed: the handshake seam owns the server identity
// pair, the facts seam the hub/os/arch triple. The seams report one success
// flag per group, so validity is per lookup, not per field — a successful
// lookup's values are authoritative even when empty.
type hostFactsValidity struct {
	handshake bool
	facts     bool
}

// recordKnown keeps the facts of the last row that rendered attached, so the
// host's later offline rows still render them. Only the fields whose live
// lookup succeeded are written: a transient handshake or facts failure on an
// attached host leaves those fields empty in the row, and recording them would
// blank the previously retained facts (the round-5 M1 finding) — the offline
// rows that follow would lose the metadata the wire contract promises.
func (s *hostAttachState) recordKnown(row appwire.HostRow, validity hostFactsValidity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[row.Name]
	if rec == nil {
		rec = &hostAttachRecord{}
		s.records[row.Name] = rec
	}
	if validity.handshake {
		rec.known.ServerName = row.ServerName
		rec.known.ServerVersion = row.ServerVersion
	}
	if validity.facts {
		rec.known.HubVersion = row.HubVersion
		rec.known.OS = row.OS
		rec.known.Arch = row.Arch
	}
}

// apply folds the retained record into row: the attach state always, the
// last-known facts only when the row is not attached (an attached row's facts
// come from the live channel).
func (s *hostAttachState) apply(row *appwire.HostRow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[row.Name]
	if rec == nil {
		return
	}
	row.MidAttach = rec.midAttach
	row.LastAttachErr = rec.lastErr
	if !row.Attached {
		row.ServerName = rec.known.ServerName
		row.ServerVersion = rec.known.ServerVersion
		row.HubVersion = rec.known.HubVersion
		row.OS = rec.known.OS
		row.Arch = rec.known.Arch
	}
}

// remove drops name's record; a removed or re-added host starts clean.
func (s *hostAttachState) remove(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, name)
}

// hubHostManager serves evener/host/add, evener/host/list,
// evener/host/status, and evener/host/remove: the slice-1 host registry
// surface. It owns no connections: list and status resolve through the
// attached-only seams, add validates hub.toml-authoritatively and wires the
// new host's source with the same seams startup uses, and remove tears the
// host down through the manager's atomic RemoveHost. It never dials.
//
// It is controller-LOCAL: these methods act on the controller's own config and
// channels, so they MUST NOT be added to remoteHostAdminMethods (pinned by
// TestHostManageNotForwarded), and every handler refuses a request whose
// routing origin is non-empty (a peer hub calling over its attach bridge) —
// pinned by TestHostManageRefusesRemoteOrigin.
type hubHostManager struct {
	cfg *hostManagerConfig
}

// newHubHostManager builds the manager over the live registries. hosts is the
// controller's live registry — in production the one *hostreg.Registry the
// SSH manager dials through and the attach handler validates against, so
// sidecar entries loaded here and hosts added at runtime are attachable
// without a restart; a nil hosts falls back to an empty registry rather than
// a panic. configPath is the selected hub.toml path ("" disables sidecar
// persistence); logf is the hub's logging path for sidecar load problems.
//
// The sidecar loads after the hub.toml registry builds, so sidecar entries
// join the live set before the first list serves. A sidecar name colliding
// with a live hub.toml entry is dropped — hub.toml is authoritative for its
// own names. A sidecar that fails to load, or an entry that fails validation,
// is logged here and poisons saves: the file keeps every entry it had until
// an operator fixes it, instead of the next save rewriting it without the
// entries that never loaded.
func newHubHostManager(sources *appsource.Registry, manager *sshconn.Manager, cfg hubcore.WebConfig, configPath string, hosts *hostreg.Registry, logf func(format string, args ...any)) *hubHostManager {
	if hosts == nil {
		hosts, _ = hostreg.New(nil)
	}
	m := &hubHostManager{cfg: &hostManagerConfig{
		hosts:            hosts,
		sidecar:          &hostSidecarStore{},
		sidecarPath:      sidecarPathFor(configPath),
		sources:          sources,
		remoteCache:      cfg.RemoteThreadCache,
		manager:          manager,
		client:           cfg.RemoteHostClient,
		online:           cfg.RemoteHostOnline,
		clientIfAttached: cfg.RemoteHostClientIfAttached,
		handshake:        cfg.RemoteHostHandshake,
		facts:            cfg.RemoteHostFacts,
		state:            newHostAttachState(),
		logf:             logf,
	}}
	entries, err := loadHostSidecar(m.cfg.sidecarPath)
	if err != nil {
		// Loud, not fatal: the hub.toml hosts still serve. The store stays
		// poisoned so no later save can clobber the file's unloaded entries.
		m.logf("host sidecar %s not loaded: %v", m.cfg.sidecarPath, err)
		m.cfg.sidecar.poison(err)
		return m
	}
	for _, e := range entries {
		// Normalize before any use of the entry. The registry's Add would
		// normalize before storing, but the hub.toml collision check, the
		// sidecar row, and the source registration all read the entry as
		// decoded: a padded sidecar name would register a source and a
		// sidecar row under the padded spelling while the registry stores
		// the trimmed one, so the row would list with a hub.toml origin and
		// removal would refuse it as hub.toml-declared.
		e = hostreg.Normalize(e)
		if _, ok := hosts.Get(e.Name); ok {
			// hub.toml is authoritative for its own names: a colliding sidecar
			// entry is a policy drop, not a load failure.
			continue
		}
		if err := hosts.Add(e); err != nil {
			m.logf("host sidecar entry %q not loaded: %v", e.Name, err)
			m.cfg.sidecar.poison(fmt.Errorf("entry %q: %w", e.Name, err))
			continue
		}
		m.cfg.sidecar.add(e)
		m.registerSource(e)
	}
	return m
}

// logf emits through the hub logging path when one is wired (tests may pass
// nil).
func (m *hubHostManager) logf(format string, args ...any) {
	if m.cfg.logf != nil {
		m.cfg.logf(format, args...)
	}
}

// observeEvent records one SSH lifecycle event into the host rows' retained
// attach state (main.go binds it to the sshconn manager's OnEvent). It only
// records: the event arrives with the per-host lock held, so it must never
// call back into the manager.
func (m *hubHostManager) observeEvent(ev sshconn.Event) {
	m.cfg.state.observe(ev)
}

// registerHostManageHandlers installs the four slice-1 host-management
// handlers. hosts is the one live registry the server constructor resolved —
// the same instance the attach handler validates against — and the manager
// and hub.toml path come from cfg: main.go threads the live sshconn.Manager,
// the live host registry, and the selected config path through WebConfig, so
// the surface is wired in production (a nil manager or config path there —
// tests, embedders — leaves the fallbacks: no channel teardown, no sidecar
// persistence). navigation, when non-nil, is invalidated after add/remove
// commits so the manifest's sources converge without waiting for the next
// refresh tick. It returns the manager so tests can drive it directly.
func registerHostManageHandlers(server *appserver.Server, sources *appsource.Registry, cfg hubcore.WebConfig, hosts *hostreg.Registry, navigation *NavigationService, logf func(format string, args ...any)) *hubHostManager {
	m := newHubHostManager(sources, cfg.RemoteHostSSHManager, cfg, cfg.RemoteHostConfigPath, hosts, logf)
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostAdd, func(ctx context.Context, params appwire.HostAddParams) (appwire.HostRow, error) {
		row, err := m.Add(ctx, params)
		if err == nil && navigation != nil {
			navigation.Invalidate(navigationChangeHint{Sources: true})
		}
		return row, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostList, m.List)
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostStatus, m.Status)
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostRemove, func(ctx context.Context, params appwire.HostRemoveParams) (appwire.HostRemoveResponse, error) {
		resp, err := m.Remove(ctx, params)
		if err == nil && navigation != nil {
			navigation.Invalidate(navigationChangeHint{Sources: true})
		}
		return resp, err
	})
	return m
}

// hostOnline reports whether host currently has a live channel. It reads the
// attached-only signal without spawning SSH: the dial seam belongs to
// evener/host/attach alone. A true here is necessary but not sufficient for a
// row to render Attached — hostRow also requires the attached-only client
// lookup to confirm a live channel, so the source registry's fail-open
// default (Online() true with no signal wired) cannot mark a host attached
// that nothing dialed.
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

// hostRow renders one host's list row: the effective entry fields plus live
// state. Attached rows read the live channel's handshake and preflight facts
// through the attached-only lookups and record them as last-known; a failed
// lookup keeps the previously retained fields instead of blanking them, so a
// transient probe failure cannot cost a later offline row its facts. Offline
// and in-progress rows render the retained attach state (midAttach,
// lastAttachError) and last-known facts from the record. It never dials:
// every seam here is attached-only. ctx is the caller's handler context — the
// facts read runs on it, so a caller that goes away cancels the read instead
// of leaving it running behind the mutation mutex List and Status hold.
func (m *hubHostManager) hostRow(ctx context.Context, host hostreg.Host, origin string) appwire.HostRow {
	row := appwire.HostRow{
		Name:    host.Name,
		Address: host.SSH,
		KeyPath: host.KeyPath,
		Origin:  origin,
	}
	// Attached is reported only when the attached-only client lookup
	// confirms a live channel (the round-2 truthful-status fix). The online
	// signal alone is not sufficient: with no signal wired a hub.toml source
	// fails open (Online() true), and trusting that would render every
	// configured host Attached with no facts behind it — an online row the
	// UI then refuses to Connect because it looks already up. No lookup
	// wired (tests, embedders) leaves the row honestly offline too: nothing
	// can confirm a channel, so nothing may claim one.
	//
	// validity tracks which live lookups refreshed the row's fact fields, so
	// the retention below keeps the previously known values for the fields
	// whose lookup failed instead of blanking them with the failed read's
	// empties.
	var validity hostFactsValidity
	if m.hostOnline(host.Name) && m.cfg.clientIfAttached != nil {
		client, ok := m.cfg.clientIfAttached(host.Name)
		if ok && client != nil {
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
	}
	m.cfg.state.apply(&row)
	if row.Attached {
		m.cfg.state.recordKnown(row, validity)
	}
	return row
}

// registerSource wires entry's appsource source exactly the way startup does
// (newHubSourceRegistry): the Ensure-backed dialing client when one is
// wired, the attached-only client/handshake/facts lookups, and a live online
// signal from the manager — so a host added at runtime is Connect-able and
// serves attached-only reads without a restart. With no dialing seam (tests,
// embedders) the client func refuses SessionUnavailable while detached, the
// pre-attach contract a hub.toml host already follows. A source that already
// exists is left alone.
func (m *hubHostManager) registerSource(entry hostreg.Host) {
	if m.cfg.sources == nil {
		return
	}
	if _, ok := m.cfg.sources.Source(entry.Name); ok {
		return
	}
	client := m.cfg.client
	if client == nil {
		client = remoteClientFor(entry.Name)
	}
	source := appsource.NewRemoteHubSource(entry.Name, entry.Roots, client)
	source.SetHostClientIfAttached(m.cfg.clientIfAttached)
	source.SetHostFacts(m.cfg.facts)
	source.SetHostHandshake(m.cfg.handshake)
	// The signal answers from the manager's own attached state, never from
	// this manager's hostOnline (that reads the source and would recurse).
	// With no signal wired (embedders, tests) it fails open exactly like a
	// startup source in the same configuration — newHubSourceRegistry installs
	// "cfg.RemoteHostOnline == nil || signal", the pre-06 default — so an
	// explicitly attached runtime host stays usable by every source-mediated
	// call that gates on Online(). hostRow never trusts the signal alone: its
	// attached-client guard still decides Attached, so the fail-open default
	// cannot render a channel-less row online.
	source.SetHostOnline(func() bool {
		return m.cfg.online == nil || m.cfg.online(entry.Name)
	})
	m.cfg.sources.Add(source)
}

// Add registers one sidecar host entry: name + SSH address + key path. It
// validates exactly like hub.toml loading (component-03 rules) and refuses a
// name hub.toml or the live set already holds — the duplicate refusal applies
// to live entries only: a removed name is gone, so re-add works.
//
// The commit is durable-first: the sidecar file is written before anything is
// exposed, so a save failure commits nothing (no registry entry, no sidecar
// row, no source) and the caller can retry. Only after the save lands does
// the live set gain the entry — registry, sidecar row, and a fully wired
// source — at which point the host is attachable without a restart. A live
// insert that fails after the save rolls the sidecar back to the pre-add
// contents (rollbackSidecar), so the durable state never keeps an add the API
// reported as failed.
func (m *hubHostManager) Add(ctx context.Context, params appwire.HostAddParams) (appwire.HostRow, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostRow{}, err
	}
	name := strings.TrimSpace(params.Name)
	address := strings.TrimSpace(params.Address)
	keyPath := strings.TrimSpace(params.KeyPath)
	entry := hostreg.Normalize(hostreg.Host{Name: name, SSH: address, KeyPath: keyPath})
	// Validate without inserting: a scratch registry runs the exact add-time
	// checks (name grammar, reserved name, ssh destination) over this one
	// entry without touching live state, so nothing is exposed before the
	// durable save below.
	if _, err := hostreg.New([]hostreg.Host{entry}); err != nil {
		return appwire.HostRow{}, appwire.InvalidParams(fmt.Sprintf("host %q: %v", name, err))
	}
	m.cfg.mu.Lock()
	defer m.cfg.mu.Unlock()
	if _, ok := m.cfg.hosts.Get(entry.Name); ok {
		return appwire.HostRow{}, appwire.InvalidParams(fmt.Sprintf("host %q: %v", name, hostreg.ErrDuplicateHost))
	}
	// Durable commit first: the sidecar file is the record of truth for
	// sidecar names, so the entry is exposed only after the save landed. The
	// pre-save snapshot is the rollback copy: should the live insert below
	// fail, the file must not keep the entry (a failed add resurrecting on
	// the next start, the round-4 L2 finding).
	prev := m.cfg.sidecar.snapshot()
	if err := m.saveSidecar(append(prev, entry)); err != nil {
		return appwire.HostRow{}, err
	}
	if err := m.addHostToRegistry(entry); err != nil {
		// The live insert refused — e.g. an SSH manager with no registry, the
		// one seam that can fail here — so the API reports failure and the
		// file rolls back to the pre-add contents: a retry starts from the
		// same durable state, instead of the next start silently completing
		// an add this call reported as failed.
		return appwire.HostRow{}, m.rollbackSidecar(prev, err)
	}
	m.cfg.sidecar.add(entry)
	m.cfg.state.remove(entry.Name) // a re-added name starts with no stale record
	m.registerSource(entry)
	return m.hostRow(ctx, entry, hostOriginSidecar), nil
}

// addHostToRegistry inserts entry into the live registry. With the SSH
// manager wired it goes through the manager's AddHost, which registers under
// the same per-host gate the attach paths and RemoveHost coordinate on, so a
// remove/re-add of a name can never interleave with an in-flight attach for
// it; without one (tests, embedders) the registry's own Add is the whole
// story, since nothing can be mid-attach through this hub.
func (m *hubHostManager) addHostToRegistry(entry hostreg.Host) error {
	if m.cfg.manager != nil {
		return m.cfg.manager.AddHost(entry)
	}
	return m.cfg.hosts.Add(entry)
}

// remoteClientFor returns the refusing client func for a host whose source
// has no dialing seam (an embedder or a test built without
// cfg.RemoteHostClient): the source serves attached-only reads until the
// first explicit Connect, exactly like a hub.toml host before its first
// attach.
func remoteClientFor(host string) func(ctx context.Context, _ string) (*appwire.Client, error) {
	return func(_ context.Context, _ string) (*appwire.Client, error) {
		return nil, appwire.SessionUnavailable(fmt.Sprintf("host %q is not attached", host))
	}
}

// saveSidecar persists entries durably, refusing while the on-disk sidecar is
// known unread or partially loaded: rewriting the file then would clobber the
// entries that never made it into memory. Callers treat the refusal as fatal
// (add/remove report it; the entry stays un-exposed or the host stays
// intact), so the operator hears about the broken file instead of losing it.
func (m *hubHostManager) saveSidecar(entries []hostreg.Host) error {
	if err := m.cfg.sidecar.poisoned(); err != nil {
		return fmt.Errorf("host sidecar %s not rewritten: %w (fix or remove the unloaded entries in the file first)", m.cfg.sidecarPath, err)
	}
	return saveHostSidecar(m.cfg.sidecarPath, entries)
}

// rollbackSidecar re-persists previous after a post-save live mutation failed,
// keeping the durable sidecar in step with the live set: the API reported the
// mutation as failed, so the file must not keep a copy the next start would
// resurrect (Add) or drop an entry the live set still holds (Remove). This is
// the compensating half of the durable-first ordering round 1 chose — the save
// still leads, so a save failure still commits nothing live and the caller can
// retry — closing the window round 1 left open, the live mutation failing
// after the save landed (the round-4 L2 finding). A rollback save failure is
// surfaced alongside cause: the file is then known to diverge, and the caller
// must hear it rather than a clean-looking refusal.
func (m *hubHostManager) rollbackSidecar(previous []hostreg.Host, cause error) error {
	if err := m.saveSidecar(previous); err != nil {
		return fmt.Errorf("%w; host sidecar rollback failed: %w", cause, err)
	}
	return cause
}

// List returns every known host with truthful online state in name-sorted
// order (the registry's own; the origin field distinguishes hub.toml entries
// from sidecar ones). Attached rows report live channel facts and retain them
// as last-known; rows without a live channel render as offline with the
// retained attach state and last-known facts. It never dials. It holds the
// mutation mutex Add and Remove commit under, so a concurrent reader never
// observes the window between a registry insert and the sidecar row and
// source registration that finish it — a half-committed host would list
// with a hub.toml origin and no source.
func (m *hubHostManager) List(ctx context.Context, _ appwire.EmptyParams) (appwire.HostListResponse, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostListResponse{}, err
	}
	m.cfg.mu.Lock()
	defer m.cfg.mu.Unlock()
	rows := make([]appwire.HostRow, 0, len(m.cfg.hosts.All()))
	for _, host := range m.cfg.hosts.All() {
		origin := hostOriginHubTOML
		if m.cfg.sidecar.isSidecar(host.Name) {
			origin = hostOriginSidecar
		}
		rows = append(rows, m.hostRow(ctx, host, origin))
	}
	if rows == nil {
		rows = []appwire.HostRow{}
	}
	return appwire.HostListResponse{Hosts: rows}, nil
}

// Status returns one host's row: the same HostRow evener/host/list serves.
// Unknown names are InvalidParams. Never dials. It holds the same mutation
// mutex List does, for the same fully-committed-row guarantee.
func (m *hubHostManager) Status(ctx context.Context, params appwire.HostStatusParams) (appwire.HostStatusResponse, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostStatusResponse{}, err
	}
	m.cfg.mu.Lock()
	defer m.cfg.mu.Unlock()
	name := strings.TrimSpace(params.Name)
	host, ok := m.cfg.hosts.Get(name)
	if !ok {
		return appwire.HostStatusResponse{}, appwire.InvalidParams(fmt.Sprintf("unknown host %q", name))
	}
	origin := hostOriginHubTOML
	if m.cfg.sidecar.isSidecar(host.Name) {
		origin = hostOriginSidecar
	}
	return appwire.HostStatusResponse{Host: m.hostRow(ctx, host, origin)}, nil
}

// Remove deregisters one sidecar host entry: its sidecar row, its source, its
// registry entry, and its channel all go, and the name is gone until
// re-added. hub.toml-declared names are refused (edit the file); unknown names
// are InvalidParams.
//
// The commit is durable-first — the sidecar file loses the entry before any
// live state changes, so a save failure leaves the host fully intact and the
// caller can retry — and the teardown is manager-owned: with a manager wired,
// RemoveHost drops the registry entry, stops the supervisor, and clears the
// channel under the host lock in one step, so a concurrent Ensure cannot
// publish a fresh channel after deregistration. A teardown that fails after
// the save rolls the sidecar forward again to keep the entry (rollbackSidecar),
// so the durable state never forgets a host the live set still holds. Nothing
// is resurrected: the entry, its source, its channel, and its retained attach
// state are all gone by the time a successful Remove returns — and so are its
// cached remote-thread rows, so its sessions stop rendering with the removal
// instead of lingering live until the refresher's next tick.
func (m *hubHostManager) Remove(ctx context.Context, params appwire.HostRemoveParams) (appwire.HostRemoveResponse, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostRemoveResponse{}, err
	}
	name := strings.TrimSpace(params.Name)
	m.cfg.mu.Lock()
	defer m.cfg.mu.Unlock()
	host, ok := m.cfg.hosts.Get(name)
	if !ok {
		return appwire.HostRemoveResponse{}, appwire.InvalidParams(fmt.Sprintf("unknown host %q", name))
	}
	if !m.cfg.sidecar.isSidecar(host.Name) {
		return appwire.HostRemoveResponse{}, appwire.InvalidParams(fmt.Sprintf("host %q is declared in hub.toml; remove it by editing the file", host.Name))
	}
	// Persist first: the durable sidecar loses the entry before any live
	// state changes, so a save failure resurrects nothing — the host stays
	// fully intact and the caller can retry. The pre-save snapshot is the
	// rollback copy: should the live teardown below fail, the file regains
	// the entry, keeping the durable state in step with the live one.
	prev := m.cfg.sidecar.snapshot()
	if err := m.saveSidecar(m.cfg.sidecar.without(host.Name)); err != nil {
		return appwire.HostRemoveResponse{}, err
	}
	if m.cfg.manager != nil {
		// Manager-owned teardown: registry entry, supervisor, channel, and
		// per-host caches drop together under the host lock.
		if err := m.cfg.manager.RemoveHost(host.Name); err != nil {
			return appwire.HostRemoveResponse{}, m.rollbackSidecar(prev, fmt.Errorf("remove host %q: %w", host.Name, err))
		}
	} else if err := m.cfg.hosts.Remove(host.Name); err != nil {
		return appwire.HostRemoveResponse{}, m.rollbackSidecar(prev, err)
	}
	if m.cfg.sources != nil {
		m.cfg.sources.Remove(host.Name)
	}
	m.cfg.sidecar.remove(host.Name)
	m.cfg.state.remove(host.Name)
	// The remote-thread cache still holds the host's last-refreshed rows,
	// and the refresher's next tick is up to 30s away; sourceOnline
	// fail-opens for the now-unregistered source ID, so those rows would
	// keep rendering the removed host's sessions as live until the tick
	// rewrote the cache (the round-5 M2 finding). The removal is committed
	// and the host can serve no future refresh, so the rows go with it. No
	// configured cache means every tree read walks the live sources, which
	// no longer list the host — nothing to prune.
	if m.cfg.remoteCache != nil {
		m.cfg.remoteCache.RemoveSource(host.Name)
	}
	return appwire.HostRemoveResponse{Host: appwire.HostRow{
		Name:    host.Name,
		Address: host.SSH,
		KeyPath: host.KeyPath,
		Origin:  hostOriginSidecar,
		Removed: true,
	}}, nil
}
