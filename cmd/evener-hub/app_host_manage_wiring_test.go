package hub

import (
	"context"
	"encoding/json"
	"errors"
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
// no test here reaches a real ssh; nothing in add/list/status/remove dials at
// all.
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
	addErr := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Name: "m4", Address: "other.example"}, nil)
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
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Name: "web-side", Address: "ws.example", KeyPath: "/keys/ws"}, &added); err != nil {
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
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Name: "web-side", Address: "ws.example"}, &added); err != nil {
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
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Name: "web-side", Address: "ws.example"}, &added); err != nil {
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
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Name: "web-side", Address: "ws.example"}, &added); err != nil {
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
