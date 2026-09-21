package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// hostManageWiringConfig builds the production remote-host wiring shape the
// real server construction path consumes: the shared live registry, the SSH
// manager over it, the Ensure-backed dial seam, the attached-only lookups, and
// the selected hub.toml path whose sidecar persists UI-added hosts — the same
// values runMain threads through WebConfig. The runner refuses every dial, so
// no test here reaches a real ssh; nothing in add/list/status/remove/update
// dials at all.
func hostManageWiringConfig(t *testing.T, configPath string, entries []hostreg.Host, runner sshconn.Runner) (hubcore.WebConfig, *hostreg.Registry, *sshconn.Manager) {
	t.Helper()
	reg, err := hostreg.New(entries)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	manager := sshconn.New(reg, sshconn.Options{Runner: runner})
	t.Cleanup(func() { _ = manager.Close() })
	cfg := hubcore.WebConfig{
		Past:                 hubcore.NewPastIndex(""),
		RemoteHosts:          entries,
		RemoteHostRegistry:   reg,
		RemoteHostSSHManager: manager,
		RemoteHostConfigPath: configPath,
		RemoteHostClient: func(ctx context.Context, host string) (*appwire.Client, error) {
			ch, err := manager.Ensure(ctx, host)
			if err != nil {
				return nil, err
			}
			return ch.Client(), nil
		},
		RemoteHostClientIfAttached: manager.ClientIfAttached,
		RemoteHostOnline:           manager.Attached,
	}
	return cfg, reg, manager
}

// TestHostManageConfiguredHostThroughRealServer is the proof the host surface
// is wired, not a placeholder: it drives the real server construction path
// (newHubRPCTestServerWithWeb -> NewWebServer over a WebConfig threaded the way
// runMain threads it) and asserts over a real /rpc dispatch that a configured
// [[hosts]] host is listed by evener/host/list with its hub.toml origin, is
// refused by evener/host/add (hub.toml is authoritative for its own names),
// and is not removable — with its live source intact afterwards.
func TestHostManageConfiguredHostThroughRealServer(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	cfg, reg, _ := hostManageWiringConfig(t, configPath,
		[]hostreg.Host{{Name: "m4", SSH: "m4.example", User: "ops"}},
		detachRefusingRunner{})
	hub, web := newHubRPCTestServerWithWeb(t, cfg)
	defer hub.Close()
	if web.hostManage == nil {
		t.Fatal("the real server construction did not install the host-management surface")
	}
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// host/list shows the configured hub.toml host with its origin and entry
	// fields, rendered offline because nothing dialed.
	var list appwire.HostListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerHostList, appwire.EmptyParams{}, &list); err != nil {
		t.Fatalf("evener/host/list: %v", err)
	}
	if len(list.Hosts) != 1 {
		t.Fatalf("evener/host/list = %+v, want exactly the configured host", list.Hosts)
	}
	row := list.Hosts[0]
	if row.Name != "m4" || row.Origin != hostOriginHubTOML || row.Address != "m4.example" || row.Attached {
		t.Fatalf("configured row = %+v, want m4/hub.toml/offline", row)
	}

	// evener/host/add of the configured name is refused: hub.toml is
	// authoritative for its own names.
	addErr := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "m4", Address: "other.example"}}, nil)
	assertWireCode(t, addErr, appwire.CodeInvalidParams)

	// evener/host/remove of the configured name is refused the same way.
	removeErr := client.Request(context.Background(), appwire.MethodEvenerHostRemove, appwire.HostRemoveParams{Name: "m4"}, nil)
	assertWireCode(t, removeErr, appwire.CodeInvalidParams)

	// The refused calls changed nothing: the host is still listed with its
	// origin, still registered in the shared registry the SSH manager dials
	// through, and still has its live source.
	var relist appwire.HostListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerHostList, appwire.EmptyParams{}, &relist); err != nil {
		t.Fatalf("second evener/host/list: %v", err)
	}
	if len(relist.Hosts) != 1 || relist.Hosts[0].Name != "m4" || relist.Hosts[0].Origin != hostOriginHubTOML {
		t.Fatalf("list after refused calls = %+v, want m4 unchanged", relist.Hosts)
	}
	if _, ok := reg.Get("m4"); !ok {
		t.Fatal("the shared registry lost the configured host")
	}
	if _, ok := web.sources.Source("m4"); !ok {
		t.Fatal("the refused remove dropped the hub.toml host's live source")
	}
	// And the sidecar was never written: the refused add committed nothing.
	if _, err := os.Stat(filepath.Join(dir, hostSidecarFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sidecar exists after a refused add: %v", err)
	}
}

// TestHostManageUIAddedHostThroughRealServer pins the runtime half of the
// wiring over the same real construction path: a host added through /rpc is
// persisted in the sidecar beside the selected hub.toml, is attachable
// immediately (an attach reaches the SSH manager's dial rather than an
// unknown-host refusal — no restart), and removes cleanly — registry entry,
// source, row, and sidecar entry all go.
func TestHostManageUIAddedHostThroughRealServer(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	cfg, reg, _ := hostManageWiringConfig(t, configPath,
		[]hostreg.Host{{Name: "m4", SSH: "m4.example"}},
		&dialRecordingRunner{})
	hub, web := newHubRPCTestServerWithWeb(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Add through the real server: the row renders sidecar + offline.
	var added appwire.HostRow
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "web-side", Address: "ws.example", KeyPath: "/keys/ws"}}, &added); err != nil {
		t.Fatalf("evener/host/add: %v", err)
	}
	if added.Name != "web-side" || added.Origin != hostOriginSidecar || added.Attached {
		t.Fatalf("add row = %+v, want web-side/sidecar/offline", added)
	}
	// The sidecar persisted beside the selected hub.toml with the key path.
	data, err := os.ReadFile(filepath.Join(dir, hostSidecarFileName))
	if err != nil {
		t.Fatalf("sidecar not written: %v", err)
	}
	var file hostSidecarFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("sidecar unparsable: %v", err)
	}
	if len(file.Hosts) != 1 || file.Hosts[0].Name != "web-side" || file.Hosts[0].KeyPath != "/keys/ws" {
		t.Fatalf("sidecar = %s, want the added entry with its key", data)
	}

	// Attach without a restart: the request reaches the SSH manager's dial
	// (the recording runner fails it; a real dial is out of scope here) — it
	// is not an unknown-host InvalidParams, which is what the pre-wiring
	// attach path answered for UI-added hosts.
	attachErr := client.Request(context.Background(), appwire.MethodEvenerHostAttach, appwire.HostAttachParams{Host: "web-side"}, nil)
	if attachErr == nil {
		t.Fatal("attach against the recording runner succeeded; it must not")
	}
	var attachWire appwire.WireError
	if errors.As(attachErr, &attachWire) && attachWire.Code == appwire.CodeInvalidParams {
		t.Fatalf("attach for the UI-added host = %v: the real wiring did not make it attachable", attachErr)
	}

	// Remove through the real server: everything goes together.
	var removed appwire.HostRemoveResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerHostRemove, appwire.HostRemoveParams{Name: "web-side"}, &removed); err != nil {
		t.Fatalf("evener/host/remove: %v", err)
	}
	if !removed.Host.Removed {
		t.Fatalf("remove row = %+v, want removed", removed.Host)
	}
	if _, ok := reg.Get("web-side"); ok {
		t.Fatal("removed host still in the shared registry")
	}
	if _, ok := web.sources.Source("web-side"); ok {
		t.Fatal("removed host still has a source")
	}
	var after appwire.HostListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerHostList, appwire.EmptyParams{}, &after); err != nil {
		t.Fatalf("evener/host/list after remove: %v", err)
	}
	if len(after.Hosts) != 1 || after.Hosts[0].Name != "m4" {
		t.Fatalf("list after remove = %+v, want only the hub.toml host", after.Hosts)
	}
	// The sidecar no longer carries the entry: a restart will not resurrect it.
	data, err = os.ReadFile(filepath.Join(dir, hostSidecarFileName))
	if err != nil {
		t.Fatalf("read sidecar after remove: %v", err)
	}
	var fileAfter hostSidecarFile
	if err := json.Unmarshal(data, &fileAfter); err != nil {
		t.Fatalf("sidecar unparsable after remove: %v", err)
	}
	if len(fileAfter.Hosts) != 0 {
		t.Fatalf("sidecar after remove = %s, want empty", data)
	}
}

// TestHostManageNilRegistryAddAttachThroughRealServer pins the round-2
// medium: with RemoteHosts configured but no live registry threaded, the real
// server construction builds ONE fallback registry and hands the same instance
// to the attach and host-management handlers — so a host added at runtime is
// attachable over a real /rpc dispatch, never "unknown host" to an attach that
// validated against a second, separate registry copy.
func TestHostManageNilRegistryAddAttachThroughRealServer(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	// The dial stub runs on server goroutines (the attach handler, and the
	// navigation snapshot's reads of the configured source), so the record
	// needs its own lock.
	var dialMu sync.Mutex
	var dialed []string
	cfg := hubcore.WebConfig{
		Past: hubcore.NewPastIndex(""),
		// RemoteHosts set, RemoteHostRegistry deliberately nil: the embedder
		// shape the constructor's once-only fallback exists for.
		RemoteHosts:          []hostreg.Host{{Name: "m4", SSH: "m4.example"}},
		RemoteHostConfigPath: configPath,
		RemoteHostClient: func(_ context.Context, host string) (*appwire.Client, error) {
			dialMu.Lock()
			dialed = append(dialed, host)
			dialMu.Unlock()
			return &appwire.Client{}, nil
		},
	}
	hub, web := newHubRPCTestServerWithWeb(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	var added appwire.HostRow
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "web-side", Address: "ws.example"}}, &added); err != nil {
		t.Fatalf("evener/host/add: %v", err)
	}
	if added.Origin != hostOriginSidecar {
		t.Fatalf("add row = %+v, want a sidecar row", added)
	}
	// Add → attach through the real server: the attach validates against the
	// same fallback registry the add committed to, so the request reaches the
	// dial seam instead of an unknown-host refusal.
	var attached appwire.HostAttachResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAttach, appwire.HostAttachParams{Host: "web-side"}, &attached); err != nil {
		t.Fatalf("evener/host/attach for the added host = %v, want success: the handlers must share one registry", err)
	}
	if !attached.Attached || attached.Host != "web-side" {
		t.Fatalf("attach response = %+v, want attached web-side", attached)
	}
	// The m4 source may dial too (reads over a source with no attached-only
	// seam resolve through the dialing client, the documented default); what
	// matters is that the added host was dialed through the shared registry.
	dialMu.Lock()
	defer dialMu.Unlock()
	if !slices.Contains(dialed, "web-side") {
		t.Fatalf("dialed %v, want the added host through the shared registry", dialed)
	}
	// The constructor's fallback is the one shared instance: it carries both
	// the configured and the runtime-added host.
	reg := web.cfg.RemoteHostRegistry
	if reg == nil {
		t.Fatal("the real server construction left no shared fallback registry")
	}
	for _, name := range []string{"m4", "web-side"} {
		if _, ok := reg.Get(name); !ok {
			t.Fatalf("the shared fallback registry lost %q", name)
		}
	}
}

// TestHostManageHubTOMLRowWithoutAttachedLookupStaysOffline pins the round-2
// truthful-status fix end to end: with RemoteHostClient wired (so hub.toml
// sources register) but no online signal and no attached-only lookup — the
// shape where a hub.toml source's Online() fails open — the configured host
// must render offline, never Attached with no facts behind it (an online row
// whose Connect action the UI hides because it looks already up).
func TestHostManageHubTOMLRowWithoutAttachedLookupStaysOffline(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	cfg := hubcore.WebConfig{
		Past: hubcore.NewPastIndex(""),
		// RemoteHostClient wired so newHubSourceRegistry registers the
		// hub.toml source with its fail-open online signal; RemoteHostOnline
		// and RemoteHostClientIfAttached deliberately absent.
		RemoteHosts:          []hostreg.Host{{Name: "m4", SSH: "m4.example"}},
		RemoteHostConfigPath: configPath,
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
			return nil, errors.New("test dial refused")
		},
	}
	hub, _ := newHubRPCTestServerWithWeb(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	var list appwire.HostListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerHostList, appwire.EmptyParams{}, &list); err != nil {
		t.Fatalf("evener/host/list: %v", err)
	}
	if len(list.Hosts) != 1 {
		t.Fatalf("evener/host/list = %+v, want exactly the configured host", list.Hosts)
	}
	row := list.Hosts[0]
	if row.Attached || row.ServerName != "" || row.HubVersion != "" {
		t.Fatalf("configured row = %+v, want offline with no facts while no live channel can be confirmed", row)
	}
}

// TestHostManageRuntimeHostClassifiedRemoteThroughRealServer pins the round-2
// medium over the production wiring shape: a host added at runtime is
// classified remote by the same gates a configured one is, because they read
// the live registry. An explicit thread/list attaches it (the dial reaches the
// SSH manager), and the host admin proxy accepts it instead of rejecting it as
// an unknown name against a static copy of the configured entries.
func TestHostManageRuntimeHostClassifiedRemoteThroughRealServer(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	runner := &dialRecordingRunner{}
	cfg, _, _ := hostManageWiringConfig(t, configPath,
		[]hostreg.Host{{Name: "m4", SSH: "m4.example"}}, runner)
	hub, _ := newHubRPCTestServerWithWeb(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	var added appwire.HostRow
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "web-side", Address: "ws.example"}}, &added); err != nil {
		t.Fatalf("evener/host/add: %v", err)
	}

	// An explicit, host-targeted thread/list attaches the runtime host: the
	// SSH manager dials ws.example (and fails in the refusing runner), where
	// the configured-entries-only gate would have skipped the attach and
	// failed as an unavailable source instead.
	tlErr := client.Request(context.Background(), appwire.MethodThreadList, appwire.ThreadListParams{SourceIDs: []string{"web-side"}}, nil)
	if tlErr == nil {
		t.Fatal("thread/list against the refusing runner succeeded; it must not")
	}
	dialForSide := false
	for _, argv := range runner.argvs() {
		for _, arg := range argv {
			if strings.Contains(arg, "ws.example") {
				dialForSide = true
			}
		}
	}
	if !dialForSide {
		t.Fatalf("no recorded dial carried ws.example: the explicit list did not attach the runtime host (argvs=%v)", runner.argvs())
	}

	// The host admin proxy resolves the runtime host instead of refusing it
	// as unknown: the request passes the registry gate and reaches the
	// host's own (detached) source refusal.
	adminErr := client.Request(context.Background(), appwire.MethodEvenerHostRequest, appwire.HostRequestParams{Host: "web-side", Method: appwire.MethodEvenerInstanceList}, nil)
	if adminErr == nil {
		t.Fatal("host/request against a detached source succeeded; it must not")
	}
	var wire appwire.WireError
	if errors.As(adminErr, &wire) && wire.Code == appwire.CodeInvalidParams {
		t.Fatalf("host/request for the runtime-added host = %v: the proxy validated against a registry that never saw the add", adminErr)
	}
	// A genuinely unknown name is still refused.
	ghostErr := client.Request(context.Background(), appwire.MethodEvenerHostRequest, appwire.HostRequestParams{Host: "ghost", Method: appwire.MethodEvenerInstanceList}, nil)
	assertWireCode(t, ghostErr, appwire.CodeInvalidParams)
}

// TestHostManageManagerWithoutRegistrySharesTheManagers pins the round-4 M2
// finding: when a caller threads RemoteHostSSHManager but leaves
// RemoteHostRegistry nil (a supported embedder shape), the constructor's
// fallback used to be a fresh registry built from RemoteHosts while Add and
// Remove mutated the manager's own registry — so boot sidecar entries landed
// where the manager never dialed (Ensure -> ErrHostNotFound) and a runtime Add
// inserted where host/list and host/attach never read. The fallback is the
// manager's registry now: one instance behind every surface.
func TestHostManageManagerWithoutRegistrySharesTheManagers(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	// A sidecar from a previous run: its entry loads at construction and must
	// land in the registry the manager dials through, or Ensure would refuse
	// it as unknown after a restart.
	if err := os.WriteFile(filepath.Join(dir, hostSidecarFileName), []byte(`{"hosts":[{"name":"boot-side","ssh":"bs.example"}]}`), 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	// The manager over its own registry, exactly the way runMain builds it —
	// but the config deliberately threads no RemoteHostRegistry alongside it.
	entries := []hostreg.Host{{Name: "m4", SSH: "m4.example"}}
	reg, err := hostreg.New(entries)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	manager := sshconn.New(reg, sshconn.Options{Runner: detachRefusingRunner{}})
	t.Cleanup(func() { _ = manager.Close() })
	var dialMu sync.Mutex
	var dialed []string
	cfg := hubcore.WebConfig{
		Past:                 hubcore.NewPastIndex(""),
		RemoteHosts:          entries,
		RemoteHostSSHManager: manager,
		RemoteHostConfigPath: configPath,
		RemoteHostClient: func(_ context.Context, host string) (*appwire.Client, error) {
			dialMu.Lock()
			dialed = append(dialed, host)
			dialMu.Unlock()
			return &appwire.Client{}, nil
		},
	}
	hub, web := newHubRPCTestServerWithWeb(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// The fallback the constructor built IS the manager's registry: attach,
	// management, and the admin proxy all validate against the instance the
	// dial paths mutate.
	if web.cfg.RemoteHostRegistry != manager.Registry() {
		t.Fatal("the fallback registry is not the SSH manager's: host surfaces would split between two registries")
	}
	// The boot sidecar entry landed where the manager dials: Ensure resolves
	// hosts from its own registry, so an entry only in the fallback would be
	// refused as unknown after a restart.
	if _, ok := manager.Registry().Get("boot-side"); !ok {
		t.Fatal("the boot sidecar entry is missing from the SSH manager's registry")
	}

	// A runtime Add goes through the manager's AddHost; host/list must show it.
	var added appwire.HostRow
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "web-side", Address: "ws.example"}}, &added); err != nil {
		t.Fatalf("evener/host/add: %v", err)
	}
	var list appwire.HostListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerHostList, appwire.EmptyParams{}, &list); err != nil {
		t.Fatalf("evener/host/list: %v", err)
	}
	var listed []string
	for _, row := range list.Hosts {
		listed = append(listed, row.Name)
	}
	for _, want := range []string{"boot-side", "m4", "web-side"} {
		if !slices.Contains(listed, want) {
			t.Fatalf("evener/host/list = %v, want it to include %q", listed, want)
		}
	}
	// Attach validates against the same registry the add committed to, so the
	// request reaches the dial seam instead of an unknown-host refusal.
	var attached appwire.HostAttachResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAttach, appwire.HostAttachParams{Host: "web-side"}, &attached); err != nil {
		t.Fatalf("evener/host/attach for the added host = %v, want success: the handlers must share the manager's registry", err)
	}
	if !attached.Attached || attached.Host != "web-side" {
		t.Fatalf("attach response = %+v, want attached web-side", attached)
	}
	dialMu.Lock()
	defer dialMu.Unlock()
	if !slices.Contains(dialed, "web-side") {
		t.Fatalf("dialed %v, want the added host dialed through the shared registry", dialed)
	}
}

// TestHostManageRemovePrunesRemoteThreadCache pins the round-5 M2 finding
// end to end over the real server construction path: Remove dropped the
// host's source but left its last-refreshed rows in the RemoteThreadCache,
// and sourceOnline fail-opens for the now-unregistered source ID — so the
// removed host's sessions kept rendering live until the refresher's next
// 30-second tick rewrote the cache. Remove prunes the host's cached rows with
// the same commit, so the first tree render after it sees nothing.
func TestHostManageRemovePrunesRemoteThreadCache(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	cache := &hubcore.RemoteThreadCache{}
	cfg := hubcore.WebConfig{
		Past:                 hubcore.NewPastIndex(""),
		RemoteHostConfigPath: configPath,
		RemoteThreadCache:    cache,
	}
	hub, web := newHubRPCTestServerWithWeb(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// A UI-added host, then the cache the background refresher would have
	// populated for it: one live session row it owns.
	var added appwire.HostRow
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "web-side", Address: "ws.example"}}, &added); err != nil {
		t.Fatalf("evener/host/add: %v", err)
	}
	cache.StoreSnapshot([]appwire.Thread{{ID: "t1", Source: "web-side", CWD: "/srv/ws", Name: "side session"}}, true)

	// rowsFor counts the removed host's presence in the tree inputs: its
	// session metas and its live entries.
	rowsFor := func() (metas, live int) {
		t.Helper()
		metasList, liveList, _ := web.navigationTreeInputs(context.Background())
		for _, meta := range metasList {
			if meta.ProfileID == "web-side" {
				metas++
			}
		}
		for _, entry := range liveList {
			if entry.SourceID == "web-side" {
				live++
			}
		}
		return metas, live
	}
	if metas, live := rowsFor(); metas != 1 || live != 1 {
		t.Fatalf("tree rows before Remove = %d metas, %d live; want the seeded session rendered live (fixture sanity)", metas, live)
	}

	var removed appwire.HostRemoveResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerHostRemove, appwire.HostRemoveParams{Name: "web-side"}, &removed); err != nil {
		t.Fatalf("evener/host/remove: %v", err)
	}
	if !removed.Host.Removed {
		t.Fatalf("remove response = %+v, want removed", removed.Host)
	}
	if metas, live := rowsFor(); metas != 0 || live != 0 {
		t.Fatalf("tree rows after Remove = %d metas, %d live; want none: the removed host's cached rows must go with the removal", metas, live)
	}
	// The cache itself carries no rows for the removed host, so no later
	// render can resurrect them either.
	for _, thread := range cache.Snapshot().Threads {
		if thread.Source == "web-side" {
			t.Fatalf("cache kept thread %q owned by the removed host", thread.ID)
		}
	}
	if _, ok := cache.Snapshot().Sources["web-side"]; ok {
		t.Fatal("cache kept a source snapshot for the removed host")
	}
}

// TestHostManageRemoveBlocksInFlightRefreshRepublish pins the round-6 M2
// finding end to end over the real server construction path: RemoveSource
// pruned only the cache's current snapshot, so a remote-thread refresh that
// started before the remove could finish afterwards and republish the removed
// host's rows — its sessions rendered live again until the refresher's next
// tick. The removal now drops the source's registration generation, so the
// late publish — which carries the generations the walk captured before the
// remove, the way the background refresher's does — is rejected. A remove/
// re-add — the churn Remove exists for — registers the name under a new
// generation: the re-added host's own refreshes publish normally, while the
// walk captured under the old registration stays rejected (round-7 M1: the
// old host's sessions must not publish under the re-added identity).
func TestHostManageRemoveBlocksInFlightRefreshRepublish(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	cache := &hubcore.RemoteThreadCache{}
	cfg := hubcore.WebConfig{
		Past:                 hubcore.NewPastIndex(""),
		RemoteHostConfigPath: configPath,
		RemoteThreadCache:    cache,
	}
	hub, web := newHubRPCTestServerWithWeb(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// rowsFor counts the host's presence in the tree inputs.
	rowsFor := func() (metas, live int) {
		t.Helper()
		metasList, liveList, _ := web.navigationTreeInputs(context.Background())
		for _, meta := range metasList {
			if meta.ProfileID == "web-side" {
				metas++
			}
		}
		for _, entry := range liveList {
			if entry.SourceID == "web-side" {
				live++
			}
		}
		return metas, live
	}

	// A UI-added host, then what a refresh that started before the remove
	// captured for it: the identity generations first — exactly where the
	// background refresher captures them, before it reads anything — then one
	// live session row the host owns.
	var added appwire.HostRow
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "web-side", Address: "ws.example"}}, &added); err != nil {
		t.Fatalf("evener/host/add: %v", err)
	}
	cache.StoreSnapshot([]appwire.Thread{{ID: "t1", Source: "web-side", CWD: "/srv/ws", Name: "side session"}}, true)
	walkGenerations := cache.SourceGenerations()
	inFlight := cache.Snapshot()
	if metas, live := rowsFor(); metas != 1 || live != 1 {
		t.Fatalf("tree rows before Remove = %d metas, %d live; want the seeded session rendered live (fixture sanity)", metas, live)
	}

	// The remove commits while that refresh is in flight.
	var removed appwire.HostRemoveResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerHostRemove, appwire.HostRemoveParams{Name: "web-side"}, &removed); err != nil {
		t.Fatalf("evener/host/remove: %v", err)
	}
	if !removed.Host.Removed {
		t.Fatalf("remove response = %+v, want removed", removed.Host)
	}
	if metas, live := rowsFor(); metas != 0 || live != 0 {
		t.Fatalf("tree rows after Remove = %d metas, %d live; want none", metas, live)
	}

	// The refresh finishes and publishes its pre-remove walk, generations
	// included. Nothing may come back: not the tree rows, not the cache's
	// rows, not the per-source entry.
	cache.StoreWalkSnapshot(inFlight, walkGenerations)
	for _, thread := range cache.Snapshot().Threads {
		if thread.Source == "web-side" {
			t.Fatalf("in-flight refresh resurrected thread %q for the removed host", thread.ID)
		}
	}
	if _, ok := cache.Snapshot().Sources["web-side"]; ok {
		t.Fatal("in-flight refresh resurrected the removed host's per-source snapshot")
	}
	if metas, live := rowsFor(); metas != 0 || live != 0 {
		t.Fatalf("tree rows after the late publish = %d metas, %d live; want none", metas, live)
	}

	// The churn completes with a re-add: the name registers again under a new
	// identity generation, so the re-added host's own refreshes publish
	// normally — the hold cannot outlive the registration it guards. The walk
	// captured before the remove belongs to the OLD registration, so it stays
	// rejected instead of putting the old host's sessions under the re-added
	// identity (round-7 M1).
	var readded appwire.HostRow
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "web-side", Address: "ws.example"}}, &readded); err != nil {
		t.Fatalf("evener/host/add (re-add): %v", err)
	}
	cache.StoreWalkSnapshot(inFlight, walkGenerations)
	if metas, live := rowsFor(); metas != 0 || live != 0 {
		t.Fatalf("tree rows after the re-add's stale publish = %d metas, %d live; want none: the old registration's walk must not publish under the re-added name", metas, live)
	}
	freshGenerations := cache.SourceGenerations()
	cache.StoreWalkSnapshot(inFlight, freshGenerations)
	if metas, live := rowsFor(); metas != 1 || live != 1 {
		t.Fatalf("tree rows after the re-add's fresh walk = %d metas, %d live; want the re-added host's rows published again", metas, live)
	}
}

// TestHostManageRemoveDropsLastGoodThreads pins the round-9 M1 finding: the
// remove finish phase pruned the remote-thread cache, the source registry,
// the attach state, and the fan-out, but never the web server's
// lastGoodThreads map — which the background walk populates for every
// attached host — so a removed host's last successful list outlived the host
// for the process lifetime, and churning distinct host names grew the map
// without bound. The removal now drops the removed host's retained rows with
// every other piece of its per-name state, while an untouched source's
// retention survives.
func TestHostManageRemoveDropsLastGoodThreads(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	cfg, _, _ := hostManageWiringConfig(t, configPath,
		[]hostreg.Host{{Name: "m4", SSH: "m4.example"}},
		detachRefusingRunner{})
	hub, web := newHubRPCTestServerWithWeb(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "web-side", Address: "ws.example"}}, nil); err != nil {
		t.Fatalf("evener/host/add: %v", err)
	}
	// What the background walk retains for each host after a successful
	// list: one entry per source name.
	web.storeLastGoodThreads("web-side", []appwire.Thread{{ID: "t1", Source: "web-side", CWD: "/srv/ws", Name: "side session"}})
	web.storeLastGoodThreads("m4", []appwire.Thread{{ID: "c1", Source: "m4", CWD: "/srv/m4", Name: "configured session"}})

	if err := client.Request(context.Background(), appwire.MethodEvenerHostRemove, appwire.HostRemoveParams{Name: "web-side"}, nil); err != nil {
		t.Fatalf("evener/host/remove: %v", err)
	}

	if threads := web.lastGoodThreadsForSource("web-side"); len(threads) != 0 {
		t.Fatalf("lastGoodThreads for the removed host = %+v, want none", threads)
	}
	web.lastGoodMu.Lock()
	_, retained := web.lastGoodThreads["web-side"]
	live := len(web.lastGoodThreads)
	web.lastGoodMu.Unlock()
	if retained {
		t.Fatal("remove left the removed host's lastGoodThreads entry behind")
	}
	if live != 1 {
		t.Fatalf("lastGoodThreads holds %d entries after the remove, want only the configured host's", live)
	}
	if threads := web.lastGoodThreadsForSource("m4"); len(threads) != 1 || threads[0].ID != "c1" {
		t.Fatalf("lastGoodThreads for the configured host = %+v, want its retained row untouched", threads)
	}
}

// TestHostManageRemoveChurnBoundedLastGoodThreads pins the bounded-retention
// half of the round-9 M1 finding: each removed host's lastGoodThreads entry
// goes with the removal, so cycling distinct host names through add → walk
// retention → remove leaves the map holding only the live sources instead of
// one entry (and its thread rows) per name ever added.
func TestHostManageRemoveChurnBoundedLastGoodThreads(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	cfg, _, _ := hostManageWiringConfig(t, configPath, nil, detachRefusingRunner{})
	hub, web := newHubRPCTestServerWithWeb(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	const churn = 3
	for i := range churn {
		name := fmt.Sprintf("churn-%d", i)
		if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Entry: appwire.HostEntry{Name: name, Address: "ws.example"}}, nil); err != nil {
			t.Fatalf("evener/host/add %s: %v", name, err)
		}
		web.storeLastGoodThreads(name, []appwire.Thread{{ID: "t1", Source: name, CWD: "/srv/ws", Name: "session"}})
		if err := client.Request(context.Background(), appwire.MethodEvenerHostRemove, appwire.HostRemoveParams{Name: name}, nil); err != nil {
			t.Fatalf("evener/host/remove %s: %v", name, err)
		}
	}
	web.lastGoodMu.Lock()
	retained := len(web.lastGoodThreads)
	web.lastGoodMu.Unlock()
	if retained != 0 {
		t.Fatalf("lastGoodThreads holds %d entries after churning %d removed hosts; removal must not retain per-name state", retained, churn)
	}
}

// TestHostManageRemoveDuringInFlightListSkipsLastGoodStore pins the round-11
// finding: the walk's last-known-good store runs only after the ListThreads
// round-trip returns, while the removal's forgetLastGoodThreads runs under
// the host manager's own lock — so a remove that committed while a walk was
// parked inside ListThreads was followed by the walk storing the removed
// host's rows, and nothing ever deleted them again: the source is gone from
// the registry, so no later walk lists or prunes the name, and
// forgetLastGoodThreads is the only delete in the codebase. The store now
// follows the same read-time generation rule the walk's publish does (rounds
// 9-10): the walk captures the source's generation immediately before the
// read, and the store — running after ListThreads returns — skips when that
// generation is gone (removed) or moved (removed/re-added). Deterministic,
// no sleeps: the parked list signals entry and blocks on release, so the
// remove lands inside the round-trip exactly.
func TestHostManageRemoveDuringInFlightListSkipsLastGoodStore(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	cache := &hubcore.RemoteThreadCache{}
	cfg, _, _ := hostManageWiringConfig(t, configPath, nil, detachRefusingRunner{})
	// The cache the walk's store fence and the remove's RemoveSource share.
	// No attached-only lookup — the walk gate's documented test mode — so
	// the walk reaches the victim's list without a live SSH channel.
	cfg.RemoteThreadCache = cache
	cfg.RemoteHostClientIfAttached = nil
	hub, web := newHubRPCTestServerWithWeb(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "web-side", Address: "ws.example"}}, nil); err != nil {
		t.Fatalf("evener/host/add: %v", err)
	}

	// Control: a walk over the live registration retains its successful
	// list — the same store the parked walk below hits — proving the
	// fixture's retention path is live before the remove closes it.
	scripted := appwire.Thread{ID: "t1", Source: "web-side", CWD: "/srv/ws", Name: "side session"}
	web.sources.Remove("web-side")
	web.sources.Add(&scriptedAppSource{id: "web-side", thread: scripted})
	control := web.refreshRemoteThreadSnapshot(context.Background())
	if len(control.threads) != 1 || control.threads[0].ID != "t1" {
		t.Fatalf("control walk threads = %+v, want the scripted row", control.threads)
	}
	if threads := web.lastGoodThreadsForSource("web-side"); len(threads) != 1 || threads[0].ID != "t1" {
		t.Fatalf("control walk retained = %+v, want the scripted row retained", threads)
	}

	// The victim's source becomes one whose list parks for the round-trip:
	// entered fires once the walk is inside ListThreads, and release lets
	// it return. The cleanup releases a failing test's park so the walk
	// goroutine never outlives the test blocked.
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	web.sources.Remove("web-side")
	web.sources.Add(&hookedRemoteSource{
		scriptedAppSource: &scriptedAppSource{id: "web-side", thread: scripted},
		onList: func() {
			close(entered)
			<-release
		},
	})

	// The background refresher's own walk, parked inside ListThreads.
	walkDone := make(chan remoteThreadFetch, 1)
	go func() {
		walkDone <- web.refreshRemoteThreadSnapshot(context.Background())
	}()
	<-entered

	// The remove completes while the list is parked: the finish phase drops
	// the registration generation and forgets the retained rows (the
	// round-9 M1 fix) while the walk holds nothing it needs.
	if err := client.Request(context.Background(), appwire.MethodEvenerHostRemove, appwire.HostRemoveParams{Name: "web-side"}, nil); err != nil {
		t.Fatalf("evener/host/remove: %v", err)
	}
	if threads := web.lastGoodThreadsForSource("web-side"); len(threads) != 0 {
		t.Fatalf("lastGoodThreads after the in-flight remove = %+v, want none", threads)
	}

	// Release the parked list. The walk's read succeeded — its fetch still
	// carries the row — but the registration that owned the read is gone,
	// so the late store must skip and leave no entry for the removed name.
	releaseOnce.Do(func() { close(release) })
	inFlight := <-walkDone
	if len(inFlight.threads) != 1 || inFlight.threads[0].ID != "t1" {
		t.Fatalf("in-flight walk threads = %+v, want the scripted row the parked list returned", inFlight.threads)
	}
	if threads := web.lastGoodThreadsForSource("web-side"); len(threads) != 0 {
		t.Fatalf("lastGoodThreads after the parked walk finished = %+v, want none: a remove that commits inside ListThreads must not be followed by the walk storing the removed host's rows", threads)
	}
	web.lastGoodMu.Lock()
	_, retained := web.lastGoodThreads["web-side"]
	live := len(web.lastGoodThreads)
	web.lastGoodMu.Unlock()
	if retained {
		t.Fatal("the parked walk resurrected the removed host's lastGoodThreads entry after the remove forgot it")
	}
	if live != 0 {
		t.Fatalf("lastGoodThreads holds %d entries after the remove, want none", live)
	}
}

// TestHostManageRemoveBlocksPostCaptureAddedHostRepublish pins the round-9
// M2 finding end to end over the real server construction path: the remove
// finish phase dropped the host's registration generation, but a refresh walk
// that started before the host was added never captured it, so the late
// publish read "not captured" as "belongs to the current registration" and
// republished the removed host's rows — its sessions rendered live again
// until the refresher's next tick. The publish now treats an uncaptured
// source whose registration is gone as stale, so a host added and removed
// inside one refresh walk stays removed.
func TestHostManageRemoveBlocksPostCaptureAddedHostRepublish(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	cache := &hubcore.RemoteThreadCache{}
	cfg := hubcore.WebConfig{
		Past:                 hubcore.NewPastIndex(""),
		RemoteHostConfigPath: configPath,
		RemoteThreadCache:    cache,
	}
	hub, web := newHubRPCTestServerWithWeb(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	rowsFor := func() (metas, live int) {
		t.Helper()
		metasList, liveList, _ := web.navigationTreeInputs(context.Background())
		for _, meta := range metasList {
			if meta.ProfileID == "web-side" {
				metas++
			}
		}
		for _, entry := range liveList {
			if entry.SourceID == "web-side" {
				live++
			}
		}
		return metas, live
	}

	// The refresh walk starts on a hub whose registry holds no remote
	// source: it captures the identity generations — an empty capture —
	// before it reads anything, exactly where the background refresher
	// captures them.
	walkGenerations := cache.SourceGenerations()
	// The host is added mid-walk and the walk reads its one session row
	// before the remove commits.
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "web-side", Address: "ws.example"}}, nil); err != nil {
		t.Fatalf("evener/host/add: %v", err)
	}
	walk := hubcore.RemoteThreadSnapshot{
		Threads:  []appwire.Thread{{ID: "t1", Source: "web-side", CWD: "/srv/ws", Name: "side session"}},
		Complete: true,
		Sources: map[string]hubcore.RemoteSourceSnapshot{
			"web-side": {Threads: []appwire.Thread{{ID: "t1", Source: "web-side", CWD: "/srv/ws", Name: "side session"}}, Complete: true},
		},
	}
	// The remove commits while the walk is still in flight.
	if err := client.Request(context.Background(), appwire.MethodEvenerHostRemove, appwire.HostRemoveParams{Name: "web-side"}, nil); err != nil {
		t.Fatalf("evener/host/remove: %v", err)
	}

	// The walk finishes and publishes what it read. Nothing may come back:
	// not the tree rows, not the cache's rows, not the per-source entry.
	cache.StoreWalkSnapshot(walk, walkGenerations)
	for _, thread := range cache.Snapshot().Threads {
		if thread.Source == "web-side" {
			t.Fatalf("late walk resurrected thread %q for the removed host", thread.ID)
		}
	}
	if _, ok := cache.Snapshot().Sources["web-side"]; ok {
		t.Fatal("late walk resurrected the removed host's per-source snapshot")
	}
	if metas, live := rowsFor(); metas != 0 || live != 0 {
		t.Fatalf("tree rows after the late publish = %d metas, %d live; want none", metas, live)
	}
}

// TestHostManageRemoveReAddStillBlocksPostCaptureAddedHostRepublish pins the
// round-10 finding end to end over the real server construction path: a host
// added after the walk's start is read by the walk, and the round-9 rule let
// its rows publish while its registration stayed live — but "live" cannot
// tell the first registration's rows from a re-added registration's. When
// the host is removed and re-added before the walk publishes, the rows the
// walk read belong to the removed registration, and publishing them would
// render the old machine's sessions as the re-added host's until the next
// refresh tick (the round-7 M1 harm through the uncaptured path). The
// publish now drops an uncaptured source's rows unconditionally: no
// read-time capture claims them, so no registration is known to own them.
func TestHostManageRemoveReAddStillBlocksPostCaptureAddedHostRepublish(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	cache := &hubcore.RemoteThreadCache{}
	cfg := hubcore.WebConfig{
		Past:                 hubcore.NewPastIndex(""),
		RemoteHostConfigPath: configPath,
		RemoteThreadCache:    cache,
	}
	hub, web := newHubRPCTestServerWithWeb(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	rowsFor := func() (metas, live int) {
		t.Helper()
		metasList, liveList, _ := web.navigationTreeInputs(context.Background())
		for _, meta := range metasList {
			if meta.ProfileID == "web-side" {
				metas++
			}
		}
		for _, entry := range liveList {
			if entry.SourceID == "web-side" {
				live++
			}
		}
		return metas, live
	}

	// The refresh walk starts on a hub whose registry holds no remote source:
	// its capture holds nothing for the host about to be added — the
	// walk-start state the pre-round-10 refresher captured once, before it
	// read anything.
	walkGenerations := cache.SourceGenerations()
	// The host is added mid-walk and the walk reads its one session row under
	// that registration.
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "web-side", Address: "ws.example"}}, nil); err != nil {
		t.Fatalf("evener/host/add: %v", err)
	}
	walk := hubcore.RemoteThreadSnapshot{
		Threads:  []appwire.Thread{{ID: "t1", Source: "web-side", CWD: "/srv/ws", Name: "side session"}},
		Complete: true,
		Sources: map[string]hubcore.RemoteSourceSnapshot{
			"web-side": {Threads: []appwire.Thread{{ID: "t1", Source: "web-side", CWD: "/srv/ws", Name: "side session"}}, Complete: true},
		},
	}
	// The churn completes while the walk is still in flight: the remove drops
	// the registration the walk read, and the re-add registers the name under
	// a new generation — live again, but not the registration that owned the
	// rows the walk holds.
	if err := client.Request(context.Background(), appwire.MethodEvenerHostRemove, appwire.HostRemoveParams{Name: "web-side"}, nil); err != nil {
		t.Fatalf("evener/host/remove: %v", err)
	}
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "web-side", Address: "ws.example"}}, nil); err != nil {
		t.Fatalf("evener/host/add (re-add): %v", err)
	}

	// The walk finishes and publishes what it read. Nothing may come back
	// under the re-added identity: not the tree rows, not the cache's rows,
	// not the per-source entry.
	cache.StoreWalkSnapshot(walk, walkGenerations)
	for _, thread := range cache.Snapshot().Threads {
		if thread.Source == "web-side" {
			t.Fatalf("late walk published thread %q under the re-added identity", thread.ID)
		}
	}
	if _, ok := cache.Snapshot().Sources["web-side"]; ok {
		t.Fatal("late walk published the re-added name's per-source snapshot from the removed registration's rows")
	}
	if metas, live := rowsFor(); metas != 0 || live != 0 {
		t.Fatalf("tree rows after the late publish = %d metas, %d live; want none under the re-added identity", metas, live)
	}
}

// TestHostManageStillLiveHostAddedMidWalkPublishes guards the immediacy the
// round-10 read-time capture preserves, end to end: a host added while the
// walk is already running is captured when the walk reads it — under the
// registration that owns the rows — and its rows render on that tick. Round
// 9 served this case through the "uncaptured but live" rule, which the
// add → read → remove → re-add sequence broke; the read-time capture keeps
// the same visible behavior without the unsound rule.
func TestHostManageStillLiveHostAddedMidWalkPublishes(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	cache := &hubcore.RemoteThreadCache{}
	cfg := hubcore.WebConfig{
		Past:                 hubcore.NewPastIndex(""),
		RemoteHostConfigPath: configPath,
		RemoteThreadCache:    cache,
	}
	hub, web := newHubRPCTestServerWithWeb(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	// The host registers mid-walk; the walk enumerates it and captures its
	// generation immediately before reading its row — the read-time capture
	// the background walk has performed since round 10.
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "web-side", Address: "ws.example"}}, nil); err != nil {
		t.Fatalf("evener/host/add: %v", err)
	}
	readGeneration, ok := cache.SourceGeneration("web-side")
	if !ok {
		t.Fatal("added host registered no cache generation for the walk's read-time capture")
	}
	walk := hubcore.RemoteThreadSnapshot{
		Threads:  []appwire.Thread{{ID: "t1", Source: "web-side", CWD: "/srv/ws", Name: "side session"}},
		Complete: true,
		Sources: map[string]hubcore.RemoteSourceSnapshot{
			"web-side": {Threads: []appwire.Thread{{ID: "t1", Source: "web-side", CWD: "/srv/ws", Name: "side session"}}, Complete: true},
		},
	}
	cache.StoreWalkSnapshot(walk, map[string]uint64{"web-side": readGeneration})
	metasList, liveList, _ := web.navigationTreeInputs(context.Background())
	var metas, live int
	for _, meta := range metasList {
		if meta.ProfileID == "web-side" {
			metas++
		}
	}
	for _, entry := range liveList {
		if entry.SourceID == "web-side" {
			live++
		}
	}
	if metas != 1 || live != 1 {
		t.Fatalf("tree rows after the publish = %d metas, %d live; want the still-live host's row rendered", metas, live)
	}
	if _, ok := cache.Snapshot().Sources["web-side"]; !ok {
		t.Fatal("publish dropped the still-live host's per-source snapshot")
	}
}

// TestHostManageConfiguredHostsRegisterCacheGeneration pins the hub-side
// precondition the round-9 M2 fix relies on: every source a refresh walk can
// enumerate must carry a cache generation, or the publish's
// absent-from-both rule would drop its rows as belonging to no live
// registration. Configured hub.toml hosts never pass through the host
// manager, so their generation is assigned at source construction — before
// the source becomes registry-visible, the same discipline the manager's
// runtime add follows.
func TestHostManageConfiguredHostsRegisterCacheGeneration(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	cache := &hubcore.RemoteThreadCache{}
	cfg, _, _ := hostManageWiringConfig(t, configPath,
		[]hostreg.Host{{Name: "m4", SSH: "m4.example"}},
		detachRefusingRunner{})
	cfg.RemoteThreadCache = cache
	hub, _ := newHubRPCTestServerWithWeb(t, cfg)
	defer hub.Close()
	generations := cache.SourceGenerations()
	if _, ok := generations["m4"]; !ok {
		t.Fatalf("configured host registered no cache generation (%v); a walk that captured it would drop its rows as unowned", generations)
	}
}
