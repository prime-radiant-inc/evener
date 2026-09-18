package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// testHostManager builds a manager over hubHosts with an empty (memory-only)
// sidecar: configPath "" disables persistence. sources starts with one
// RemoteHubSource per hub host so list/status resolve attachment state. The
// sources report offline (no channel wired): attached rows are pinned by
// TestHostManageListTruthfulness with explicit online signals instead.
func testHostManager(hubHosts []hostreg.Host, sources *appsource.Registry) *hubHostManager {
	if sources == nil {
		sources = appsource.NewRegistry()
		for _, h := range hubHosts {
			source := appsource.NewRemoteHubSource(h.Name, h.Roots, remoteClientFor(h.Name))
			source.SetHostOnline(func() bool { return false })
			sources.Add(source)
		}
	}
	hosts, err := hostreg.New(hubHosts)
	if err != nil {
		// Test entries are literals this package validates; a refusal means
		// the literal is wrong, and the empty fallback surfaces that as loud
		// refusals in the test below.
		hosts, _ = hostreg.New(nil)
	}
	return newHubHostManager(sources, nil, hubcore.WebConfig{}, "", hosts, nil)
}

// TestHostManageAddValidation pins the failing-first contract: blank names,
// blank addresses, reserved names, and bad grammar are InvalidParams and
// commit nothing.
func TestHostManageAddValidation(t *testing.T) {
	m := testHostManager(nil, nil)
	for _, params := range []appwire.HostAddParams{
		{Name: "", Address: "h.example"},
		{Name: "   ", Address: "h.example"},
		{Name: "m4", Address: ""},
		{Name: "m4", Address: "   "},
		{Name: "local", Address: "h.example"},
		{Name: "a/b", Address: "h.example"},
		{Name: "a..b", Address: "h.example"},
	} {
		_, err := m.Add(context.Background(), params)
		if err == nil {
			t.Errorf("Add(%+v) accepted, want InvalidParams", params)
			continue
		}
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
			t.Errorf("Add(%+v) error = %v, want InvalidParams", params, err)
		}
	}
	if got := m.cfg.hosts.All(); len(got) != 0 {
		t.Fatalf("failed adds committed %d hosts, want none", len(got))
	}
}

// TestHostManageAddDuplicateRefusal pins that add refuses a name hub.toml or
// the live set already holds — and that a failed add commits nothing.
func TestHostManageAddDuplicateRefusal(t *testing.T) {
	m := testHostManager([]hostreg.Host{{Name: "m4", SSH: "m4.example"}}, nil)
	for _, name := range []string{"m4", "  m4  "} {
		if _, err := m.Add(context.Background(), appwire.HostAddParams{Name: name, Address: "other.example"}); err == nil {
			t.Errorf("Add(%q) accepted over a hub.toml name, want refusal", name)
		}
	}
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Name: "side", Address: "s.example"}); err != nil {
		t.Fatalf("Add(side) = %v, want success", err)
	}
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Name: "side", Address: "s2.example"}); err == nil {
		t.Error("Add(side) twice accepted, want duplicate refusal")
	}
	if got := m.cfg.hosts.All(); len(got) != 2 {
		t.Fatalf("hosts = %d, want 2 (m4 + side)", len(got))
	}
}

// TestHostManageAddListsWithOrigin pins that a successful add lists with the
// sidecar origin while hub.toml entries keep theirs.
func TestHostManageAddListsWithOrigin(t *testing.T) {
	m := testHostManager([]hostreg.Host{{Name: "m4", SSH: "m4.example"}}, nil)
	row, err := m.Add(context.Background(), appwire.HostAddParams{Name: "side", Address: "s.example", KeyPath: "/keys/s"})
	if err != nil {
		t.Fatalf("Add = %v", err)
	}
	if row.Name != "side" || row.Origin != hostOriginSidecar || row.Attached {
		t.Fatalf("add row = %+v, want side/sidecar/detached", row)
	}
	if row.KeyPath != "/keys/s" || row.Address != "s.example" {
		t.Fatalf("add row = %+v, want address and key echoed", row)
	}
	list, err := m.List(context.Background(), appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("List = %v", err)
	}
	if len(list.Hosts) != 2 {
		t.Fatalf("list = %d rows, want 2", len(list.Hosts))
	}
	origins := map[string]string{}
	for _, r := range list.Hosts {
		origins[r.Name] = r.Origin
	}
	if origins["m4"] != hostOriginHubTOML || origins["side"] != hostOriginSidecar {
		t.Fatalf("origins = %v, want m4=hub.toml side=sidecar", origins)
	}
}

// TestHostManageListTruthfulness pins attached vs unattached rows: the online
// host renders attached with handshake identity, the offline host renders
// detached with no facts — and neither row dials.
func TestHostManageListTruthfulness(t *testing.T) {
	live := &appwire.Client{}
	sources := appsource.NewRegistry()
	onlineSrc := appsource.NewRemoteHubSource("online", nil, remoteClientFor("online"))
	onlineSrc.SetHostOnline(func() bool { return true })
	offlineSrc := appsource.NewRemoteHubSource("offline", nil, remoteClientFor("offline"))
	offlineSrc.SetHostOnline(func() bool { return false })
	sources.Add(onlineSrc)
	sources.Add(offlineSrc)
	hosts, err := hostreg.New([]hostreg.Host{{Name: "online", SSH: "on.example"}, {Name: "offline", SSH: "off.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	m := newHubHostManager(sources, nil, hubcore.WebConfig{
		RemoteHostClientIfAttached: func(host string) (*appwire.Client, bool) {
			if host == "online" {
				return live, true
			}
			return nil, false
		},
		RemoteHostHandshake: func(host string, client *appwire.Client) (appwire.InitializeResponse, bool) {
			if host == "online" && client == live {
				return appwire.InitializeResponse{ServerInfo: appwire.ServerInfo{Name: "evener-hub", Version: "0.1.0"}}, true
			}
			return appwire.InitializeResponse{}, false
		},
		RemoteHostFacts: func(_ context.Context, host string, client *appwire.Client) (appsource.HostFacts, error) {
			if host == "online" && client == live {
				return appsource.HostFacts{HubVersion: "9.9.9", OS: "linux", Arch: "amd64"}, nil
			}
			return appsource.HostFacts{}, errors.New("no facts")
		},
	}, "", hosts, nil)

	list, err := m.List(context.Background(), appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("List = %v", err)
	}
	rows := map[string]appwire.HostRow{}
	for _, r := range list.Hosts {
		rows[r.Name] = r
	}
	on := rows["online"]
	if !on.Attached || on.ServerName != "evener-hub" || on.HubVersion != "9.9.9" || on.OS != "linux" || on.Arch != "amd64" {
		t.Errorf("online row = %+v, want attached with handshake + facts", on)
	}
	off := rows["offline"]
	if off.Attached || off.ServerName != "" || off.HubVersion != "" || off.OS != "" {
		t.Errorf("offline row = %+v, want detached with no facts", off)
	}
}

// TestHostManageStatusUnknownIsInvalidParams pins that status on an unknown
// name is InvalidParams and never dials.
func TestHostManageStatusUnknownIsInvalidParams(t *testing.T) {
	m := testHostManager([]hostreg.Host{{Name: "m4", SSH: "m4.example"}}, nil)
	if _, err := m.Status(context.Background(), appwire.HostStatusParams{Name: "ghost"}); err == nil {
		t.Fatal("Status(ghost) accepted, want InvalidParams")
	} else {
		assertWireCode(t, err, appwire.CodeInvalidParams)
	}
	resp, err := m.Status(context.Background(), appwire.HostStatusParams{Name: "  m4  "})
	if err != nil {
		t.Fatalf("Status(m4) = %v", err)
	}
	if resp.Host.Name != "m4" {
		t.Fatalf("status row = %+v, want m4", resp.Host)
	}
}

// TestHostManageRemoveRefusals pins that remove refuses hub.toml names (edit
// the file) and unknown names — and removes nothing in either case.
func TestHostManageRemoveRefusals(t *testing.T) {
	m := testHostManager([]hostreg.Host{{Name: "m4", SSH: "m4.example"}}, nil)
	if _, err := m.Remove(context.Background(), appwire.HostRemoveParams{Name: "m4"}); err == nil {
		t.Fatal("Remove(hub.toml name) accepted, want refusal")
	}
	if _, err := m.Remove(context.Background(), appwire.HostRemoveParams{Name: "ghost"}); err == nil {
		t.Fatal("Remove(unknown) accepted, want InvalidParams")
	} else {
		assertWireCode(t, err, appwire.CodeInvalidParams)
	}
	if _, ok := m.cfg.hosts.Get("m4"); !ok {
		t.Fatal("refused removes dropped m4")
	}
}

// TestHostManageRemoveThenReAdd pins clean deregistration: the entry, source,
// and sidecar row are gone, the response renders removed, and re-add works.
func TestHostManageRemoveThenReAdd(t *testing.T) {
	m := testHostManager(nil, nil)
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Name: "side", Address: "s.example"}); err != nil {
		t.Fatalf("Add = %v", err)
	}
	resp, err := m.Remove(context.Background(), appwire.HostRemoveParams{Name: "side"})
	if err != nil {
		t.Fatalf("Remove = %v", err)
	}
	if !resp.Host.Removed || resp.Host.Attached {
		t.Fatalf("remove row = %+v, want removed + detached", resp.Host)
	}
	if _, ok := m.cfg.hosts.Get("side"); ok {
		t.Fatal("removed host still in registry")
	}
	if _, ok := m.cfg.sources.Source("side"); ok {
		t.Fatal("removed host still has a source")
	}
	if m.cfg.sidecar.isSidecar("side") {
		t.Fatal("removed host still in sidecar")
	}
	list, err := m.List(context.Background(), appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("List = %v", err)
	}
	for _, r := range list.Hosts {
		if r.Name == "side" {
			t.Fatalf("removed host still listed: %+v", r)
		}
	}
	// Re-add works with a different address: no resurrection of the old row.
	row, err := m.Add(context.Background(), appwire.HostAddParams{Name: "side", Address: "s2.example"})
	if err != nil {
		t.Fatalf("re-Add = %v", err)
	}
	if row.Address != "s2.example" || row.Removed || row.Origin != hostOriginSidecar {
		t.Fatalf("re-add row = %+v, want fresh sidecar row", row)
	}
}

// detachRefusingRunner is an sshconn.Runner that refuses every spawn: the
// manager under test never dials through it (DetachHost is non-dialing), so
// any call is a test failure.
type detachRefusingRunner struct{}

func (detachRefusingRunner) Start(_ context.Context, _ []string, _ io.Writer) (sshconn.Stdio, error) {
	return nil, errors.New("detach test must not dial")
}

func (detachRefusingRunner) Run(_ context.Context, _ []string, _ io.Reader) ([]byte, error) {
	return nil, errors.New("detach test must not dial")
}

// TestHostManageRemoveDetachesManager pins that Remove drops the manager's
// channel for the host: after Remove, Attached and ClientIfAttached both
// report false, and a second Remove is InvalidParams (stays removed). The
// manager and the host surface share one registry — the production shape —
// so Add inserts the entry the manager consults and Remove's manager-owned
// teardown drops it from that same registry. The manager here carries no live
// channel (the refusing runner never dials), so this pins the registry/
// source/sidecar cleanup plus RemoveHost's unattached path; the attached
// channel teardown is pinned by TestDetachHostAttached and
// TestRemoveHostTearsDownChannel in the sshconn package.
func TestHostManageRemoveDetachesManager(t *testing.T) {
	reg, err := hostreg.New(nil)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	manager := sshconn.New(reg, sshconn.Options{Runner: detachRefusingRunner{}})
	t.Cleanup(func() { _ = manager.Close() })
	sources := appsource.NewRegistry()
	m := newHubHostManager(sources, manager, hubcore.WebConfig{}, "", reg, nil)
	// Add inserts into the one live registry the manager consults, so Ensure
	// would find it (no dial happens here: the refusing runner guards that).
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Name: "side", Address: "s.example"}); err != nil {
		t.Fatalf("Add = %v", err)
	}
	if _, ok := reg.Get("side"); !ok {
		t.Fatal("Add did not insert into the shared registry the manager consults")
	}
	if _, err := m.Remove(context.Background(), appwire.HostRemoveParams{Name: "side"}); err != nil {
		t.Fatalf("Remove = %v", err)
	}
	if _, ok := reg.Get("side"); ok {
		t.Fatal("Remove left the host in the shared registry the manager consults")
	}
	if manager.Attached("side") {
		t.Fatal("removed host still attached")
	}
	if _, ok := manager.ClientIfAttached("side"); ok {
		t.Fatal("removed host still has an attached client")
	}
	// Removal is idempotent from the host surface: the host stays gone.
	if _, err := m.Remove(context.Background(), appwire.HostRemoveParams{Name: "side"}); err == nil {
		t.Fatal("second Remove accepted, want InvalidParams (stays removed)")
	} else {
		assertWireCode(t, err, appwire.CodeInvalidParams)
	}
}

// TestHostManageNotForwarded pins the spec's negative requirement: the four
// slice-1 methods are controller-local and MUST NOT be added to
// remoteHostAdminMethods — otherwise the proxy would forward them to a remote
// hub instead of acting on the controller's own config.
func TestHostManageNotForwarded(t *testing.T) {
	for _, name := range []string{
		appwire.MethodEvenerHostAdd,
		appwire.MethodEvenerHostList,
		appwire.MethodEvenerHostStatus,
		appwire.MethodEvenerHostRemove,
	} {
		if _, ok := remoteHostAdminMethods[name]; ok {
			t.Errorf("controller-local %q is on the remote forward allow-list", name)
		}
	}
}

// TestHostSidecarRoundTrip pins the atomic sidecar: entries persist with key
// paths, reload in add order, and survive a corrupt-file refusal loudly.
func TestHostSidecarRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(path, []byte("addr = \"127.0.0.1:9180\"\n"), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	sidecar := sidecarPathFor(path)
	want := []hostreg.Host{
		{Name: "b", SSH: "b.example", KeyPath: "/k/b"},
		{Name: "a", SSH: "a.example", User: "u"},
	}
	if err := saveHostSidecar(sidecar, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := loadHostSidecar(sidecar)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 2 || got[0].Name != "b" || got[1].Name != "a" {
		t.Fatalf("round trip = %+v, want add order b, a", got)
	}
	if got[0].KeyPath != "/k/b" || got[1].User != "u" {
		t.Fatalf("round trip = %+v, want key path and user preserved", got)
	}
	info, err := os.Stat(sidecar)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("sidecar mode = %o, want 600", info.Mode().Perm())
	}
	// Missing file is an empty sidecar, not an error.
	empty, err := loadHostSidecar(filepath.Join(dir, "nowhere", "hub.hosts.json"))
	if err != nil || len(empty) != 0 {
		t.Fatalf("missing sidecar = %v, %v; want empty, nil", empty, err)
	}
	// Empty config path disables persistence: methods still work.
	if got := sidecarPathFor(""); got != "" {
		t.Fatalf("sidecarPathFor(empty) = %q, want empty", got)
	}
}

// TestHostSidecarCorruptIsLoud pins that a corrupt sidecar fails the load —
// callers decide loudly (startup drops it, never half-applies).
func TestHostSidecarCorruptIsLoud(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.hosts.json")
	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := loadHostSidecar(path); err == nil {
		t.Fatal("corrupt sidecar loaded without error")
	}
}

// TestHostManageAddPersistsSidecar pins that add writes the sidecar file and
// a fresh manager over the same config path reloads the entry.
func TestHostManageAddPersistsSidecar(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, configPath, nil, nil)
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Name: "side", Address: "s.example", KeyPath: "/k/s"}); err != nil {
		t.Fatalf("Add = %v", err)
	}
	data, err := os.ReadFile(sidecarPathFor(configPath))
	if err != nil {
		t.Fatalf("sidecar not written: %v", err)
	}
	var file hostSidecarFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("sidecar unparsable: %v", err)
	}
	if len(file.Hosts) != 1 || file.Hosts[0].Name != "side" || file.Hosts[0].KeyPath != "/k/s" {
		t.Fatalf("sidecar = %s, want one entry with key", data)
	}
	// A fresh manager reloads the entry with its origin and key.
	m2 := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, configPath, nil, nil)
	resp, err := m2.Status(context.Background(), appwire.HostStatusParams{Name: "side"})
	if err != nil {
		t.Fatalf("reloaded Status = %v", err)
	}
	if resp.Host.Origin != hostOriginSidecar || resp.Host.KeyPath != "/k/s" {
		t.Fatalf("reloaded row = %+v, want sidecar origin + key", resp.Host)
	}
}

// TestHostManageSidecarCollisionDrops pins hub.toml-authority at load: a
// sidecar name colliding with a hub.toml entry is dropped, never shadowed.
func TestHostManageSidecarCollisionDrops(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	if err := saveHostSidecar(sidecarPathFor(configPath), []hostreg.Host{
		{Name: "m4", SSH: "sidecar.example"},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	hosts, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "hub.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, configPath, hosts, nil)
	resp, err := m.Status(context.Background(), appwire.HostStatusParams{Name: "m4"})
	if err != nil {
		t.Fatalf("Status = %v", err)
	}
	if resp.Host.Address != "hub.example" || resp.Host.Origin != hostOriginHubTOML {
		t.Fatalf("row = %+v, want hub.toml entry authoritative", resp.Host)
	}
}

// TestHostManageRemovePersists pins that remove rewrites the sidecar without
// the entry, so a restart does not resurrect it.
func TestHostManageRemovePersists(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, configPath, nil, nil)
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Name: "side", Address: "s.example"}); err != nil {
		t.Fatalf("Add = %v", err)
	}
	if _, err := m.Remove(context.Background(), appwire.HostRemoveParams{Name: "side"}); err != nil {
		t.Fatalf("Remove = %v", err)
	}
	m2 := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, configPath, nil, nil)
	if _, err := m2.Status(context.Background(), appwire.HostStatusParams{Name: "side"}); err == nil {
		t.Fatal("removed host resurrected after reload")
	}
}

// TestHostManageRefusesRemoteOrigin pins HIGH 3's controller-local rule from
// the request side: a bridge-originated request (a peer hub calling over its
// attach bridge) may not add, list, status, or remove the controller's hosts.
// Every method fails InvalidParams and none of them mutates or exposes host
// data; the same calls from a local origin succeed, so the guard is
// origin-keyed rather than broken.
func TestHostManageRefusesRemoteOrigin(t *testing.T) {
	m := testHostManager([]hostreg.Host{{Name: "m4", SSH: "m4.example"}}, nil)
	ctx := withHostRoutingOrigin(context.Background(), hostRoutingOriginBridge)
	if _, err := m.Add(ctx, appwire.HostAddParams{Name: "side", Address: "s.example"}); err == nil {
		t.Fatal("bridge-originated Add accepted, want refusal")
	} else {
		assertWireCode(t, err, appwire.CodeInvalidParams)
	}
	if _, err := m.List(ctx, appwire.EmptyParams{}); err == nil {
		t.Fatal("bridge-originated List accepted, want refusal")
	} else {
		assertWireCode(t, err, appwire.CodeInvalidParams)
	}
	if _, err := m.Status(ctx, appwire.HostStatusParams{Name: "m4"}); err == nil {
		t.Fatal("bridge-originated Status accepted, want refusal")
	} else {
		assertWireCode(t, err, appwire.CodeInvalidParams)
	}
	if _, err := m.Remove(ctx, appwire.HostRemoveParams{Name: "m4"}); err == nil {
		t.Fatal("bridge-originated Remove accepted, want refusal")
	} else {
		assertWireCode(t, err, appwire.CodeInvalidParams)
	}
	// Nothing was mutated or exposed: the registry still holds exactly the
	// hub.toml host, and the refused add committed nothing anywhere.
	if got := m.cfg.hosts.All(); len(got) != 1 || got[0].Name != "m4" {
		t.Fatalf("remote-origin calls mutated the host set: %+v", got)
	}
	if m.cfg.sidecar.isSidecar("m4") || m.cfg.sidecar.isSidecar("side") {
		t.Fatal("remote-origin calls mutated the sidecar")
	}
	// The same add from a local origin succeeds, so the guard is the only
	// thing refusing.
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Name: "side", Address: "s.example"}); err != nil {
		t.Fatalf("local Add = %v, want success", err)
	}
}

// dialRecordingRunner records every argv the SSH manager tries to dial and
// refuses the call, so a test can assert which hosts the attach path reached
// and how the dial was shaped without a real ssh.
type dialRecordingRunner struct {
	mu     sync.Mutex
	called [][]string
}

func (r *dialRecordingRunner) record(argv []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.called = append(r.called, append([]string(nil), argv...))
}

func (r *dialRecordingRunner) argvs() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.called...)
}

func (r *dialRecordingRunner) Run(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
	r.record(argv)
	return nil, errors.New("test runner never dials")
}

func (r *dialRecordingRunner) Start(_ context.Context, argv []string, _ io.Writer) (sshconn.Stdio, error) {
	r.record(argv)
	return nil, errors.New("test runner never dials")
}

// TestHostManageAddWiresAttachableSource pins HIGH 2: a host added at runtime
// is attachable without a restart. The manager and the host surface share one
// live registry, so the added name validates on the attach path and the dial
// the attach triggers is the SSH manager's — carrying the entry's key path in
// front of the destination terminator — and the registered source carries the
// manager-backed seams (it reports offline while nothing is attached).
func TestHostManageAddWiresAttachableSource(t *testing.T) {
	reg, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	runner := &dialRecordingRunner{}
	manager := sshconn.New(reg, sshconn.Options{Runner: runner})
	t.Cleanup(func() { _ = manager.Close() })
	cfg := hubcore.WebConfig{
		RemoteHosts: []hostreg.Host{{Name: "m4", SSH: "m4.example"}},
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
	sources := appsource.NewRegistry()
	m := newHubHostManager(sources, manager, cfg, "", reg, nil)
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Name: "side", Address: "s.example", KeyPath: "/keys/side"}); err != nil {
		t.Fatalf("Add = %v", err)
	}
	// The added host is not an unknown name on the attach path: the dial the
	// attach triggers fails in the runner (no real ssh), never at validation.
	_, err = hubHostAttach(context.Background(), cfg, sources, reg, appwire.HostAttachParams{Host: "side"})
	if err == nil {
		t.Fatal("attach against the refusing runner succeeded; it must not")
	}
	var wire appwire.WireError
	if errors.As(err, &wire) && wire.Code == appwire.CodeInvalidParams {
		t.Fatalf("attach for the added host = %v; want a dial failure, not a validation refusal", err)
	}
	// An unknown name still fails validation: the add did not open the registry.
	if _, err := hubHostAttach(context.Background(), cfg, sources, reg, appwire.HostAttachParams{Host: "ghost"}); err == nil {
		t.Fatal("attach(ghost) accepted, want InvalidParams")
	} else {
		assertWireCode(t, err, appwire.CodeInvalidParams)
	}
	// The dial argv the manager built carries the entry's key path in front of
	// the -- destination terminator.
	found := false
	for _, argv := range runner.argvs() {
		for i := 0; i+2 < len(argv); i++ {
			if argv[i] == "-i" && argv[i+1] == "/keys/side" && argv[i+2] == "--" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("no recorded dial carried -i /keys/side before --: %v", runner.argvs())
	}
	// The registered source carries the manager-backed online signal and
	// reports offline while nothing is attached.
	src, ok := sources.Source("side")
	if !ok {
		t.Fatal("Add registered no source")
	}
	online, ok := src.(appsource.OnlineSource)
	if !ok || online.Online() {
		t.Fatal("added source does not report offline through its online signal")
	}
	resp, err := m.Status(context.Background(), appwire.HostStatusParams{Name: "side"})
	if err != nil {
		t.Fatalf("Status = %v", err)
	}
	if resp.Host.Attached {
		t.Fatal("added host renders attached before any attach")
	}
}

// TestHostManageRowsRetainAttachState pins the wire contract's retained
// metadata: an in-progress attach renders midAttach, a terminal failure
// renders lastAttachError, a successful attach clears both and records the
// live facts, and a later offline row keeps those last-known facts.
func TestHostManageRowsRetainAttachState(t *testing.T) {
	live := &appwire.Client{}
	sources := appsource.NewRegistry()
	online := false
	cfg := hubcore.WebConfig{
		RemoteHostOnline: func(string) bool { return online },
		RemoteHostClientIfAttached: func(host string) (*appwire.Client, bool) {
			if host == "h" && online {
				return live, true
			}
			return nil, false
		},
		RemoteHostHandshake: func(host string, client *appwire.Client) (appwire.InitializeResponse, bool) {
			if host == "h" && client == live {
				return appwire.InitializeResponse{ServerInfo: appwire.ServerInfo{Name: "evener-hub", Version: "0.1.0"}}, true
			}
			return appwire.InitializeResponse{}, false
		},
		RemoteHostFacts: func(_ context.Context, host string, client *appwire.Client) (appsource.HostFacts, error) {
			if host == "h" && client == live {
				return appsource.HostFacts{HubVersion: "9.9.9", OS: "linux", Arch: "amd64"}, nil
			}
			return appsource.HostFacts{}, errors.New("no facts")
		},
	}
	hosts, err := hostreg.New([]hostreg.Host{{Name: "h", SSH: "h.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	m := newHubHostManager(sources, nil, cfg, "", hosts, nil)
	status := func() appwire.HostRow {
		t.Helper()
		resp, err := m.Status(context.Background(), appwire.HostStatusParams{Name: "h"})
		if err != nil {
			t.Fatalf("Status = %v", err)
		}
		return resp.Host
	}
	// In-progress: a preflighting event renders midAttach on the offline row.
	m.observeEvent(sshconn.Event{Host: "h", Kind: sshconn.EventState, State: sshconn.StatePreflighting})
	if row := status(); row.Attached || !row.MidAttach {
		t.Fatalf("preflighting row = %+v, want offline midAttach", row)
	}
	// Terminal failure: lastAttachError renders and midAttach clears.
	m.observeEvent(sshconn.Event{Host: "h", Kind: sshconn.EventFailed, Err: errors.New("dial refused")})
	if row := status(); row.Attached || row.MidAttach || row.LastAttachErr != "dial refused" {
		t.Fatalf("failed row = %+v, want offline with lastAttachError", row)
	}
	// Attached: the live facts render, the attach clears the error, and the
	// facts are retained as last-known.
	online = true
	m.observeEvent(sshconn.Event{Host: "h", Kind: sshconn.EventAttached})
	row := status()
	if !row.Attached || row.MidAttach || row.LastAttachErr != "" {
		t.Fatalf("attached row = %+v, want attached with no stale error", row)
	}
	if row.ServerName != "evener-hub" || row.HubVersion != "9.9.9" || row.OS != "linux" || row.Arch != "amd64" {
		t.Fatalf("attached row = %+v, want the live handshake and facts", row)
	}
	// Offline again: the row keeps the last-known facts the contract promises
	// and a reconnect in progress renders midAttach alongside them.
	online = false
	m.observeEvent(sshconn.Event{Host: "h", Kind: sshconn.EventDetached})
	m.observeEvent(sshconn.Event{Host: "h", Kind: sshconn.EventState, State: sshconn.StateReconnecting})
	row = status()
	if row.Attached || !row.MidAttach {
		t.Fatalf("reconnecting row = %+v, want offline midAttach", row)
	}
	if row.ServerName != "evener-hub" || row.HubVersion != "9.9.9" || row.OS != "linux" || row.Arch != "amd64" {
		t.Fatalf("offline row = %+v, want retained last-known facts", row)
	}
}

// TestHostManageSidecarLoadFailureIsLoudAndKeepsFile pins the corrupt-sidecar
// discipline: the load failure is logged through the hub logging path, the
// hub.toml hosts keep serving, and no later save rewrites the unreadable file
// — an add fails loudly instead of clobbering the entries that never loaded.
func TestHostManageSidecarLoadFailureIsLoudAndKeepsFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	sidecar := sidecarPathFor(configPath)
	if err := os.WriteFile(sidecar, []byte("{corrupt"), 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	var logs []string
	logf := func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	hosts, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, configPath, hosts, logf)
	if len(logs) == 0 {
		t.Fatal("corrupt sidecar produced no log line at startup")
	}
	// The hub.toml host still serves.
	if _, err := m.Status(context.Background(), appwire.HostStatusParams{Name: "m4"}); err != nil {
		t.Fatalf("Status over a corrupt sidecar = %v, want the hub.toml host to serve", err)
	}
	// The poisoned store refuses to persist: the add fails and commits
	// nothing.
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Name: "side", Address: "s.example"}); err == nil {
		t.Fatal("Add over a corrupt sidecar succeeded, want a loud refusal")
	}
	if _, ok := m.cfg.hosts.Get("side"); ok {
		t.Fatal("the refused add committed a host")
	}
	// The unreadable file was not rewritten: its bytes survive for the
	// operator to fix.
	data, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if string(data) != "{corrupt" {
		t.Fatalf("corrupt sidecar was rewritten to %q", data)
	}
}

// TestHostManageSaveFailureCommitsNothing pins the durable-first discipline
// from the failing side: when the sidecar write fails, Add commits nothing
// (no registry entry, no sidecar row, no source) and Remove leaves the host
// fully intact — nothing is exposed before the durable commit lands, and no
// removal happens ahead of its persistence.
func TestHostManageSaveFailureCommitsNothing(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	sources := appsource.NewRegistry()
	m := newHubHostManager(sources, nil, hubcore.WebConfig{}, configPath, nil, nil)
	// First add succeeds and creates the sidecar file.
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Name: "keep", Address: "k.example"}); err != nil {
		t.Fatalf("Add(keep) = %v, want success", err)
	}
	// Make the atomic write fail: replace the sidecar file with a directory,
	// so the temp-file rename inside saveHostSidecar cannot land. The store
	// itself is healthy — this is a raw save failure, not a poison.
	if err := os.Remove(sidecarPathFor(configPath)); err != nil {
		t.Fatalf("remove sidecar: %v", err)
	}
	if err := os.MkdirAll(sidecarPathFor(configPath), 0o700); err != nil {
		t.Fatalf("mkdir sidecar: %v", err)
	}
	// Add fails and commits nothing.
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Name: "side", Address: "s.example"}); err == nil {
		t.Fatal("Add over a failing save succeeded, want refusal")
	}
	if _, ok := m.cfg.hosts.Get("side"); ok {
		t.Fatal("Add exposed a registry entry the save never committed")
	}
	if m.cfg.sidecar.isSidecar("side") {
		t.Fatal("Add recorded a sidecar row the save never committed")
	}
	if _, ok := sources.Source("side"); ok {
		t.Fatal("Add registered a source the save never committed")
	}
	// Remove fails and leaves the host fully intact.
	if _, err := m.Remove(context.Background(), appwire.HostRemoveParams{Name: "keep"}); err == nil {
		t.Fatal("Remove over a failing save succeeded, want refusal")
	}
	if _, ok := m.cfg.hosts.Get("keep"); !ok {
		t.Fatal("Remove dropped the registry entry before its persistence landed")
	}
	if !m.cfg.sidecar.isSidecar("keep") {
		t.Fatal("Remove dropped the sidecar row before its persistence landed")
	}
	if _, ok := sources.Source("keep"); !ok {
		t.Fatal("Remove dropped the source before its persistence landed")
	}
	resp, err := m.Status(context.Background(), appwire.HostStatusParams{Name: "keep"})
	if err != nil {
		t.Fatalf("Status(keep) after the refused remove = %v, want the host intact", err)
	}
	if resp.Host.Removed {
		t.Fatalf("row = %+v, want the host still present", resp.Host)
	}
}

// TestHostManageSidecarInvalidEntryIsLoudAndKeepsFile pins the per-entry
// discipline: a valid entry beside an invalid one loads and serves, the
// invalid one is logged by name and poisons saves, and the file keeps both
// entries for the operator to fix.
func TestHostManageSidecarInvalidEntryIsLoudAndKeepsFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	sidecar := sidecarPathFor(configPath)
	if err := saveHostSidecar(sidecar, []hostreg.Host{
		{Name: "good", SSH: "g.example"},
		{Name: "bad name", SSH: "b.example"},
	}); err != nil {
		t.Fatalf("save sidecar: %v", err)
	}
	var logs []string
	logf := func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	hosts, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, configPath, hosts, logf)
	// The valid entry loaded and serves; the invalid one was logged by name.
	if _, err := m.Status(context.Background(), appwire.HostStatusParams{Name: "good"}); err != nil {
		t.Fatalf("Status(good) = %v, want the valid sidecar entry to load", err)
	}
	logged := false
	for _, line := range logs {
		if strings.Contains(line, "bad name") {
			logged = true
		}
	}
	if !logged {
		t.Fatalf("invalid sidecar entry produced no log line: %v", logs)
	}
	// The poisoned store refuses to persist: an add fails loudly and the file
	// keeps both entries.
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Name: "side", Address: "s.example"}); err == nil {
		t.Fatal("Add over a partially loaded sidecar succeeded, want a loud refusal")
	}
	entries, err := loadHostSidecar(sidecar)
	if err != nil {
		t.Fatalf("reload sidecar: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("sidecar rewritten to %d entries, want both preserved", len(entries))
	}
}
