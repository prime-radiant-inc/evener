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
// registries are live: the hub owns them, mutations swap entries under mu,
// and reads never dial.
type hostManagerConfig struct {
	// hosts is the live host registry: hub.toml entries at boot plus
	// sidecar entries as they are added. Mutations hold mu.
	hosts *hostreg.Registry
	// sidecar holds the UI-added entries in add order with their key paths.
	// It is the durable side of hosts for sidecar names; hub.toml names are
	// never recorded here.
	sidecar *hostSidecarStore
	// sidecarPath is the sidecar file beside the selected hub.toml. Empty
	// (tests, embedders without a file) disables persistence: the store stays
	// memory-only and every method still works.
	sidecarPath string
	// sources is the component-05 registry; a remote host's source is where
	// attachment state (Online) and the per-host client live.
	sources *appsource.Registry
	// manager owns every live SSH channel; add wires nothing live, remove
	// detaches through it. Nil in tests that only exercise validation.
	manager *sshconn.Manager
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
	// mu serializes sidecar read-modify-write cycles so concurrent add/remove
	// calls cannot lose updates.
	mu sync.Mutex
}

// hostSidecarEntry is one UI-added host: the registry entry plus the SSH key
// path the dial uses. KeyPath lives here — not in hostreg — because the
// registry mirrors hub.toml's [[hosts]] schema, which has no key field: key
// material is controller-local dial configuration.
type hostSidecarEntry struct {
	Host    hostreg.Host
	KeyPath string
}

// hostSidecarStore is the durable sidecar: UI-added entries in add order.
// The zero value is usable; all methods are safe for concurrent use. Callers
// that also mutate the registry hold hostManagerConfig.mu across both, so
// the two cannot drift apart under concurrency.
type hostSidecarStore struct {
	mu      sync.Mutex
	entries []hostSidecarEntry
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
func loadHostSidecar(path string) ([]hostSidecarEntry, error) {
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
	entries := make([]hostSidecarEntry, 0, len(file.Hosts))
	for _, h := range file.Hosts {
		entries = append(entries, hostSidecarEntry{
			Host: hostreg.Host{
				Name:       h.Name,
				SSH:        h.SSH,
				User:       h.User,
				EvenerPath: h.EvenerPath,
				ConfigPath: h.ConfigPath,
				Addr:       h.Addr,
				Roots:      h.Roots,
			},
			KeyPath: h.KeyPath,
		})
	}
	return entries, nil
}

// saveHostSidecar persists entries atomically: a 0600 temp file in the same
// directory, fsynced, renamed over the target, so a crash lands the old file
// or the new one, never a half-write. 0600 keeps key paths from ever landing
// world-readable. An empty path (no config file) skips the write; the
// in-memory store stays authoritative for the process lifetime.
func saveHostSidecar(path string, entries []hostSidecarEntry) error {
	if path == "" {
		return nil
	}
	file := hostSidecarFile{Hosts: make([]hostSidecarFileEntry, 0, len(entries))}
	for _, e := range entries {
		file.Hosts = append(file.Hosts, hostSidecarFileEntry{
			Name:       e.Host.Name,
			SSH:        e.Host.SSH,
			User:       e.Host.User,
			EvenerPath: e.Host.EvenerPath,
			ConfigPath: e.Host.ConfigPath,
			Addr:       e.Host.Addr,
			Roots:      e.Host.Roots,
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
func (s *hostSidecarStore) add(entry hostSidecarEntry) {
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
		if e.Host.Name == name {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			return true
		}
	}
	return false
}

// snapshot returns entries in add order; the slice is a copy. Callers hold
// hostManagerConfig.mu.
func (s *hostSidecarStore) snapshot() []hostSidecarEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]hostSidecarEntry(nil), s.entries...)
}

// keyPathFor returns name's sidecar key path, or "" for hub.toml names (which
// carry no key configuration) and unknown names.
func (s *hostSidecarStore) keyPathFor(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.Host.Name == name {
			return e.KeyPath
		}
	}
	return ""
}

// isSidecar reports whether name is a live sidecar entry.
func (s *hostSidecarStore) isSidecar(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.Host.Name == name {
			return true
		}
	}
	return false
}

// hubHostManager serves evener/host/add, evener/host/list,
// evener/host/status, and evener/host/remove: the slice-1 host registry
// surface. It owns no connections: list and status resolve through the
// attached-only seams, add validates hub.toml-authoritatively, and remove
// detaches through the manager. It never dials.
//
// Like the host-admin controller, it is controller-LOCAL: these methods act
// on the controller's own config and channels, so they MUST NOT be added to
// remoteHostAdminMethods (pinned by TestHostManageNotForwarded).
type hubHostManager struct {
	cfg *hostManagerConfig
}

// newHubHostManager builds the manager over the live registries. configPath
// is the selected hub.toml path ("" disables sidecar persistence); hubHosts
// are the validated hub.toml entries the file declared.
//
// The sidecar loads after the hub.toml registry builds, so sidecar entries
// join the live set before the first list serves. A sidecar name colliding
// with a live hub.toml entry is dropped — hub.toml is authoritative for its
// own names. An invalid sidecar entry is dropped the same way: a corrupt or
// stale file stays loud in the logs path (LoadConfig refuses bad hub.toml at
// startup) without taking the whole host surface down.
func newHubHostManager(sources *appsource.Registry, manager *sshconn.Manager, cfg hubcore.WebConfig, configPath string, hubHosts []hostreg.Host) *hubHostManager {
	store := &hostSidecarStore{}
	hosts, err := hostreg.New(hubHosts)
	if err != nil {
		// Config loading already validated every entry, so this cannot fail
		// in production. Fall back to an empty registry rather than a nil
		// one, so a hypothetical duplicate refuses every host instead of
		// panicking.
		hosts, _ = hostreg.New(nil)
	}
	m := &hubHostManager{cfg: &hostManagerConfig{
		hosts:            hosts,
		sidecar:          store,
		sidecarPath:      sidecarPathFor(configPath),
		sources:          sources,
		manager:          manager,
		online:           cfg.RemoteHostOnline,
		clientIfAttached: cfg.RemoteHostClientIfAttached,
		handshake:        cfg.RemoteHostHandshake,
		facts:            cfg.RemoteHostFacts,
	}}
	if entries, err := loadHostSidecar(m.cfg.sidecarPath); err == nil {
		for _, e := range entries {
			if _, ok := hosts.Get(e.Host.Name); ok {
				continue
			}
			if err := hosts.Add(e.Host); err != nil {
				continue
			}
			store.add(e)
		}
	}
	return m
}

// registerHostManageHandlers installs the four slice-1 host-management
// handlers. configPath is the selected hub.toml path for sidecar
// persistence; hubHosts are the validated hub.toml entries. navigation, when
// non-nil, is invalidated after add/remove commits so the manifest's sources
// converge without waiting for the next refresh tick. It returns the manager
// so tests can drive it directly.
func registerHostManageHandlers(server *appserver.Server, sources *appsource.Registry, manager *sshconn.Manager, cfg hubcore.WebConfig, configPath string, hubHosts []hostreg.Host, navigation *NavigationService) *hubHostManager {
	m := newHubHostManager(sources, manager, cfg, configPath, hubHosts)
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
// evener/host/attach alone.
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
// through the attached-only lookups; offline rows carry no facts (slice 1
// keeps no last-known store — the row renders the entry as offline). It never
// dials: every seam here is attached-only.
func (m *hubHostManager) hostRow(host hostreg.Host, origin string) appwire.HostRow {
	row := appwire.HostRow{
		Name:    host.Name,
		Address: host.SSH,
		KeyPath: m.cfg.sidecar.keyPathFor(host.Name),
		Origin:  origin,
	}
	if !m.hostOnline(host.Name) {
		return row
	}
	row.Attached = true
	if m.cfg.clientIfAttached == nil {
		return row
	}
	client, ok := m.cfg.clientIfAttached(host.Name)
	if !ok || client == nil {
		// The online signal fired but the channel is already gone: render
		// offline rather than an online row with no facts behind it.
		row.Attached = false
		return row
	}
	if m.cfg.handshake != nil {
		if hs, ok := m.cfg.handshake(host.Name, client); ok {
			row.ServerName = hs.ServerInfo.Name
			row.ServerVersion = hs.ServerInfo.Version
		}
	}
	if m.cfg.facts != nil {
		if facts, err := m.cfg.facts(context.Background(), host.Name, client); err == nil {
			row.HubVersion = facts.HubVersion
			row.OS = facts.OS
			row.Arch = facts.Arch
		}
		// A facts-read failure keeps the row attached: the dial the attach
		// already completed is authoritative (app_host_attach.go's
		// dial-authoritative rule), and a failed facts read is not a detach.
	}
	return row
}

// Add registers one sidecar host entry: name + SSH address + key path. It
// validates exactly like hub.toml loading (component-03 rules) and refuses a
// name hub.toml or the live set already holds — the duplicate refusal applies
// to live entries only: a removed name is gone, so re-add works. It wires no
// live channel: the host attaches on the first explicit Connect.
func (m *hubHostManager) Add(_ context.Context, params appwire.HostAddParams) (appwire.HostRow, error) {
	name := strings.TrimSpace(params.Name)
	address := strings.TrimSpace(params.Address)
	keyPath := strings.TrimSpace(params.KeyPath)
	entry := hostreg.Normalize(hostreg.Host{Name: name, SSH: address})
	m.cfg.mu.Lock()
	defer m.cfg.mu.Unlock()
	if err := m.cfg.hosts.Add(entry); err != nil {
		return appwire.HostRow{}, appwire.InvalidParams(fmt.Sprintf("host %q: %v", name, err))
	}
	m.cfg.sidecar.add(hostSidecarEntry{Host: entry, KeyPath: keyPath})
	if err := saveHostSidecar(m.cfg.sidecarPath, m.cfg.sidecar.snapshot()); err != nil {
		_ = m.cfg.hosts.Remove(entry.Name)
		m.cfg.sidecar.remove(entry.Name)
		return appwire.HostRow{}, err
	}
	if m.cfg.sources != nil {
		if _, ok := m.cfg.sources.Source(entry.Name); !ok {
			// The fresh source reports offline until the first Connect
			// attaches it: with no signal a source defaults online, which
			// would render a never-attached host as attached. (Production
			// startup rewires every source's signal from the manager; a host
			// added at runtime carries this refusal until then. The func
			// answers a constant — never m.hostOnline — so hostRow cannot
			// recurse through it.)
			source := appsource.NewRemoteHubSource(entry.Name, entry.Roots, remoteClientFor(entry.Name))
			source.SetHostOnline(func() bool { return false })
			m.cfg.sources.Add(source)
		}
	}
	return m.hostRow(entry, hostOriginSidecar), nil
}

// remoteClientFor returns the dialing client func for a newly added host's
// source. The source serves attached-only reads until the first explicit
// Connect: with no lookup installed it falls back to this func, which refuses
// SessionUnavailable while detached — exactly like a hub.toml host before its
// first attach. (Production rewires the source's lookup at startup; a host
// added at runtime carries this refusal until the hub restarts and registers
// it with the full seams.)
func remoteClientFor(host string) func(ctx context.Context, _ string) (*appwire.Client, error) {
	return func(_ context.Context, _ string) (*appwire.Client, error) {
		return nil, appwire.SessionUnavailable(fmt.Sprintf("host %q is not attached", host))
	}
}

// List returns every known host with truthful online state: hub.toml entries
// first in registry (name-sorted) order with their origin, then the same
// rows for sidecar entries. Attached rows report live channel facts; rows
// without a live channel render as offline. It never dials.
func (m *hubHostManager) List(_ context.Context, _ appwire.EmptyParams) (appwire.HostListResponse, error) {
	rows := make([]appwire.HostRow, 0, len(m.cfg.hosts.All()))
	for _, host := range m.cfg.hosts.All() {
		origin := hostOriginHubTOML
		if m.cfg.sidecar.isSidecar(host.Name) {
			origin = hostOriginSidecar
		}
		rows = append(rows, m.hostRow(host, origin))
	}
	if rows == nil {
		rows = []appwire.HostRow{}
	}
	return appwire.HostListResponse{Hosts: rows}, nil
}

// Status returns one host's row: the same HostRow evener/host/list serves.
// Unknown names are InvalidParams. Never dials.
func (m *hubHostManager) Status(_ context.Context, params appwire.HostStatusParams) (appwire.HostStatusResponse, error) {
	name := strings.TrimSpace(params.Name)
	host, ok := m.cfg.hosts.Get(name)
	if !ok {
		return appwire.HostStatusResponse{}, appwire.InvalidParams(fmt.Sprintf("unknown host %q", name))
	}
	origin := hostOriginHubTOML
	if m.cfg.sidecar.isSidecar(host.Name) {
		origin = hostOriginSidecar
	}
	return appwire.HostStatusResponse{Host: m.hostRow(host, origin)}, nil
}

// Remove deregisters one sidecar host entry: its supervisor stops, its
// channel drops, and the name is gone until re-added. hub.toml-declared names
// are refused (edit the file); unknown names are InvalidParams. Removed stays
// removed, re-add works, and nothing is resurrected: the entry, its source,
// and its channel are all dropped by the time Remove returns.
func (m *hubHostManager) Remove(_ context.Context, params appwire.HostRemoveParams) (appwire.HostRemoveResponse, error) {
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
	if m.cfg.manager != nil {
		// A detach failure refuses the removal before anything is dropped:
		// the entry, source, and sidecar row all stay in place, so the caller
		// can retry rather than inherit a half-removed host.
		if err := m.cfg.manager.DetachHost(host.Name); err != nil {
			return appwire.HostRemoveResponse{}, fmt.Errorf("detach host %q: %w", host.Name, err)
		}
	}
	if m.cfg.sources != nil {
		m.cfg.sources.Remove(host.Name)
	}
	if err := m.cfg.hosts.Remove(host.Name); err != nil {
		return appwire.HostRemoveResponse{}, err
	}
	m.cfg.sidecar.remove(host.Name)
	if err := saveHostSidecar(m.cfg.sidecarPath, m.cfg.sidecar.snapshot()); err != nil {
		return appwire.HostRemoveResponse{}, err
	}
	return appwire.HostRemoveResponse{Host: appwire.HostRow{
		Name:    host.Name,
		Address: host.SSH,
		Origin:  hostOriginSidecar,
		Removed: true,
	}}, nil
}
