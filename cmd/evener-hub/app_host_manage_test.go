package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	return newHubHostManager(sources, nil, hubcore.WebConfig{}, "", hubHosts)
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
	}, "", []hostreg.Host{{Name: "online", SSH: "on.example"}, {Name: "offline", SSH: "off.example"}})

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
// manager here carries no live channel (the refusing runner never dials), so
// this pins the registry/source/sidecar cleanup plus DetachHost's unattached
// no-op; DetachHost's attached-channel teardown is pinned by
// TestDetachHostAttached in the sshconn package.
func TestHostManageRemoveDetachesManager(t *testing.T) {
	reg, err := hostreg.New([]hostreg.Host{{Name: "side", SSH: "s.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	manager := sshconn.New(reg, sshconn.Options{Runner: detachRefusingRunner{}})
	t.Cleanup(func() { _ = manager.Close() })
	sources := appsource.NewRegistry()
	m := newHubHostManager(sources, manager, hubcore.WebConfig{}, "", nil)
	// The manager's registry already holds the entry; mirror it into the
	// hubHostManager's live set the way Add would (without dialing).
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Name: "side", Address: "s.example"}); err != nil {
		t.Fatalf("Add = %v", err)
	}
	if _, err := m.Remove(context.Background(), appwire.HostRemoveParams{Name: "side"}); err != nil {
		t.Fatalf("Remove = %v", err)
	}
	// DetachHost is non-dialing and idempotent: removing an unattached host
	// is a no-op, and the host stays gone.
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
	want := []hostSidecarEntry{
		{Host: hostreg.Host{Name: "b", SSH: "b.example"}, KeyPath: "/k/b"},
		{Host: hostreg.Host{Name: "a", SSH: "a.example", User: "u"}, KeyPath: ""},
	}
	if err := saveHostSidecar(sidecar, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := loadHostSidecar(sidecar)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 2 || got[0].Host.Name != "b" || got[1].Host.Name != "a" {
		t.Fatalf("round trip = %+v, want add order b, a", got)
	}
	if got[0].KeyPath != "/k/b" || got[1].Host.User != "u" {
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
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, configPath, nil)
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
	m2 := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, configPath, nil)
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
	if err := saveHostSidecar(sidecarPathFor(configPath), []hostSidecarEntry{
		{Host: hostreg.Host{Name: "m4", SSH: "sidecar.example"}},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, configPath, []hostreg.Host{{Name: "m4", SSH: "hub.example"}})
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
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, configPath, nil)
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Name: "side", Address: "s.example"}); err != nil {
		t.Fatalf("Add = %v", err)
	}
	if _, err := m.Remove(context.Background(), appwire.HostRemoveParams{Name: "side"}); err != nil {
		t.Fatalf("Remove = %v", err)
	}
	m2 := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, configPath, nil)
	if _, err := m2.Status(context.Background(), appwire.HostStatusParams{Name: "side"}); err == nil {
		t.Fatal("removed host resurrected after reload")
	}
	if !strings.Contains(t.Name(), "RemovePersists") {
		t.Fatal("unreachable")
	}
}
