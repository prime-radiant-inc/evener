package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
		{Entry: appwire.HostEntry{Name: "", Address: "h.example"}},
		{Entry: appwire.HostEntry{Name: "   ", Address: "h.example"}},
		{Entry: appwire.HostEntry{Name: "m4", Address: ""}},
		{Entry: appwire.HostEntry{Name: "m4", Address: "   "}},
		{Entry: appwire.HostEntry{Name: "local", Address: "h.example"}},
		{Entry: appwire.HostEntry{Name: "a/b", Address: "h.example"}},
		{Entry: appwire.HostEntry{Name: "a..b", Address: "h.example"}},
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

// TestHostManageValidationRefusalsNameTheInput pins the mapping: each refusal
// the add path can raise names the input the operator can fix, in the spelling
// the dialog's own inputs use (HostEntry's wire fields) — so a message lands on
// the right control without anyone parsing prose. Add-only fields (name) are
// named too, because the edit dialog has no name input to land on.
func TestHostManageValidationRefusalsNameTheInput(t *testing.T) {
	m := testHostManager(nil, nil)
	for _, tc := range []struct {
		entry appwire.HostEntry
		field string
	}{
		{appwire.HostEntry{Name: "m4", Address: "   "}, "address"},
		{appwire.HostEntry{Name: "m4", Address: "u@h.example", User: "bob"}, "user"},
		{appwire.HostEntry{Name: "m4", Address: "h.example", Roots: []string{"  "}}, "roots"},
		{appwire.HostEntry{Name: "bad name", Address: "h.example"}, "name"},
		{appwire.HostEntry{Name: "local", Address: "h.example"}, "name"},
	} {
		_, err := m.Add(context.Background(), appwire.HostAddParams{Entry: tc.entry})
		if err == nil {
			t.Errorf("Add(%+v) accepted, want a validation refusal", tc.entry)
			continue
		}
		var wire appwire.WireError
		if !errors.As(err, &wire) {
			t.Errorf("Add(%+v) error = %v, want a WireError", tc.entry, err)
			continue
		}
		data, ok := wire.Data.(appwire.HostFieldErrorData)
		if !ok {
			t.Errorf("Add(%+v) data = %T, want HostFieldErrorData", tc.entry, wire.Data)
			continue
		}
		if data.EvenerErrorInfo != appwire.ErrorInvalidHostField || data.Field != tc.field {
			t.Errorf("Add(%+v) blamed %q (%s), want %q", tc.entry, data.Field, data.EvenerErrorInfo, tc.field)
		}
	}
}

// TestHostManageAddCarriesEveryEntryField pins the apply half of the wire
// reshape: the add request's entry is stored whole — the seven HostConfig
// fields under the wire's spellings plus the slice-1 key path — and the row
// reports them back, so an edit dialog prefilled from a row sees what the host
// actually is.
func TestHostManageAddCarriesEveryEntryField(t *testing.T) {
	m := testHostManager(nil, nil)
	entry := appwire.HostEntry{
		Name:       "m4",
		Address:    "m4.example",
		User:       "operator",
		KeyPath:    "/keys/m4",
		EvenerPath: "/opt/evener",
		ConfigPath: "/etc/evener/hub.toml",
		Addr:       "127.0.0.1:9180",
		Roots:      []string{"/srv/one"},
	}
	row, err := m.Add(context.Background(), appwire.HostAddParams{Entry: entry})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	for _, tc := range []struct {
		field string
		got   string
		want  string
	}{
		{"address", row.Address, entry.Address},
		{"user", row.User, entry.User},
		{"keyPath", row.KeyPath, entry.KeyPath},
		{"evenerPath", row.EvenerPath, entry.EvenerPath},
		{"configPath", row.ConfigPath, entry.ConfigPath},
		{"addr", row.Addr, entry.Addr},
	} {
		if tc.got != tc.want {
			t.Errorf("row.%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}
	if !slices.Equal(row.Roots, entry.Roots) {
		t.Errorf("row.Roots = %v, want %v", row.Roots, entry.Roots)
	}
	stored, ok := m.cfg.hosts.Get("m4")
	if !ok {
		t.Fatal("the added host is not in the registry")
	}
	if stored.SSH != entry.Address || stored.User != entry.User || stored.KeyPath != entry.KeyPath ||
		stored.EvenerPath != entry.EvenerPath || stored.ConfigPath != entry.ConfigPath ||
		stored.Addr != entry.Addr || !slices.Equal(stored.Roots, entry.Roots) {
		t.Fatalf("stored entry = %+v, want the request's entry", stored)
	}
}

// TestHostManageAddDuplicateRefusal pins that add refuses a name hub.toml or
// the live set already holds — and that a failed add commits nothing.
func TestHostManageAddDuplicateRefusal(t *testing.T) {
	m := testHostManager([]hostreg.Host{{Name: "m4", SSH: "m4.example"}}, nil)
	for _, name := range []string{"m4", "  m4  "} {
		if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: name, Address: "other.example"}}); err == nil {
			t.Errorf("Add(%q) accepted over a hub.toml name, want refusal", name)
		}
	}
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example"}}); err != nil {
		t.Fatalf("Add(side) = %v, want success", err)
	}
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s2.example"}}); err == nil {
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
	row, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example", KeyPath: "/keys/s"}})
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
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example", KeyPath: "/keys/s"}}); err != nil {
		t.Fatalf("Add = %v", err)
	}
	resp, err := m.Remove(context.Background(), appwire.HostRemoveParams{Name: "side"})
	if err != nil {
		t.Fatalf("Remove = %v", err)
	}
	if !resp.Host.Removed || resp.Host.Attached {
		t.Fatalf("remove row = %+v, want removed + detached", resp.Host)
	}
	// HostRemoveResponse documents a removed HostRow, and the row echoes the
	// entry's key path like every other rendered row does.
	if resp.Host.KeyPath != "/keys/s" {
		t.Fatalf("remove row key path = %q, want the removed entry's /keys/s", resp.Host.KeyPath)
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
	row, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s2.example"}})
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
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example"}}); err != nil {
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

// TestHostManageNotForwarded pins the spec's negative requirement: the
// host-management methods (add/list/status/remove/update) are controller-local
// and MUST NOT be added to remoteHostAdminMethods — otherwise the proxy would
// forward them to a remote hub instead of acting on the controller's own
// config.
func TestHostManageNotForwarded(t *testing.T) {
	for _, name := range []string{
		appwire.MethodEvenerHostAdd,
		appwire.MethodEvenerHostList,
		appwire.MethodEvenerHostStatus,
		appwire.MethodEvenerHostRemove,
		appwire.MethodEvenerHostUpdate,
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

// TestHostSidecarSchemaRequiresHostsArray pins the round-13 finding: a
// JSON-valid document without a usable "hosts" array — {} or {"hosts":null}
// — is a load error, not an empty sidecar. Loading it as "no entries" left
// the store unpoisoned, so the next add rewrote the file from an empty
// snapshot and silently discarded whatever the document carried. A present
// empty array is the one legitimately empty sidecar; a non-array "hosts"
// stays a typed-decode failure, exactly as before the schema gate.
func TestHostSidecarSchemaRequiresHostsArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.hosts.json")
	for _, doc := range []string{"{}", `{"hosts":null}`, "null"} {
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatalf("write sidecar: %v", err)
		}
		if entries, err := loadHostSidecar(path); err == nil {
			t.Errorf("%q loaded as %d entries without error, want a schema refusal", doc, len(entries))
		}
	}
	// A wrong-type "hosts" still fails the typed decode — the gate must not
	// shadow the parse error with a coarser schema message.
	if err := os.WriteFile(path, []byte(`{"hosts":{}}`), 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	if _, err := loadHostSidecar(path); err == nil {
		t.Error(`{"hosts":{}} loaded without error, want a decode failure`)
	}
	// A present empty array is the one legitimately empty sidecar.
	if err := os.WriteFile(path, []byte(`{"hosts":[]}`), 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	entries, err := loadHostSidecar(path)
	if err != nil || len(entries) != 0 {
		t.Fatalf(`{"hosts":[]} = %v, %v; want empty, nil`, entries, err)
	}
}

// TestHostSidecarWellFormedRoundTripUnchanged pins that the schema gate
// leaves the normal path alone: a well-formed file loads and resaves
// byte-identical, so an untouched sidecar never churns under a hub that
// only read it.
func TestHostSidecarWellFormedRoundTripUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.hosts.json")
	if err := saveHostSidecar(path, []hostreg.Host{
		{Name: "a", SSH: "a.example", KeyPath: "/k/a"},
		{Name: "b", SSH: "b.example", User: "u"},
	}); err != nil {
		t.Fatalf("save sidecar: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	entries, err := loadHostSidecar(path)
	if err != nil {
		t.Fatalf("load sidecar: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("load sidecar = %d entries, want 2", len(entries))
	}
	if err := saveHostSidecar(path, entries); err != nil {
		t.Fatalf("resave sidecar: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reread sidecar: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("resaved sidecar changed bytes: before %q, after %q", before, after)
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
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example", KeyPath: "/k/s"}}); err != nil {
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
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example"}}); err != nil {
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
// attach bridge) may not add, list, status, remove, or update the controller's
// hosts. Every method fails InvalidParams and none of them mutates or exposes
// host data; the same calls from a local origin succeed, so the guard is
// origin-keyed rather than broken.
func TestHostManageRefusesRemoteOrigin(t *testing.T) {
	m := testHostManager([]hostreg.Host{{Name: "m4", SSH: "m4.example"}}, nil)
	ctx := withHostRoutingOrigin(context.Background(), hostRoutingOriginBridge)
	if _, err := m.Add(ctx, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example"}}); err == nil {
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
	if _, err := m.Update(ctx, appwire.HostUpdateParams{Name: "m4", Entry: appwire.HostEntry{Address: "edited.example"}}); err == nil {
		t.Fatal("bridge-originated Update accepted, want refusal")
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
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example"}}); err != nil {
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
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example", KeyPath: "/keys/side"}}); err != nil {
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

// TestHostManageRowsThreadTheHandlerContext pins that hostRow's facts read
// runs on the handler's own context — the context List and Status hold the
// mutation mutex under — and not a detached context.Background(), so a caller
// that goes away mid-read cancels the facts call instead of leaving it running
// behind a mutex nothing else can take.
func TestHostManageRowsThreadTheHandlerContext(t *testing.T) {
	live := &appwire.Client{}
	var gotCtx context.Context
	cfg := hubcore.WebConfig{
		RemoteHostOnline:           func(string) bool { return true },
		RemoteHostClientIfAttached: func(string) (*appwire.Client, bool) { return live, true },
		RemoteHostFacts: func(ctx context.Context, _ string, _ *appwire.Client) (appsource.HostFacts, error) {
			gotCtx = ctx
			return appsource.HostFacts{}, ctx.Err()
		},
	}
	hosts, err := hostreg.New([]hostreg.Host{{Name: "h", SSH: "h.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	m := newHubHostManager(appsource.NewRegistry(), nil, cfg, "", hosts, nil)

	// A canceled handler context must reach the facts read.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp, err := m.Status(ctx, appwire.HostStatusParams{Name: "h"})
	if err != nil {
		t.Fatalf("Status = %v", err)
	}
	if gotCtx == nil || !errors.Is(gotCtx.Err(), context.Canceled) {
		t.Fatal("the Status facts read ran on a context other than the handler's: the canceled handler context never reached it")
	}
	// A failed facts read is not a detach: the row stays attached.
	if !resp.Host.Attached {
		t.Fatalf("row = %+v, want attached despite the failed facts read (the dial is authoritative)", resp.Host)
	}

	// List threads its handler context the same way.
	listCtx, listCancel := context.WithCancel(context.Background())
	listCancel()
	if _, err := m.List(listCtx, appwire.EmptyParams{}); err != nil {
		t.Fatalf("List = %v", err)
	}
	if gotCtx == nil || !errors.Is(gotCtx.Err(), context.Canceled) {
		t.Fatal("the List facts read ran on a context other than the handler's: the canceled handler context never reached it")
	}
}

// TestHostManageRuntimeSourceMatchesStartupNilOnlineSignal pins the round-3
// medium: with RemoteHostOnline nil (embedders, tests), every startup source
// fails open — newHubSourceRegistry installs "cfg.RemoteHostOnline == nil ||
// signal", the pre-06 default — but the runtime-added source's signal returned
// false, leaving an explicitly attached runtime host unusable by every
// source-mediated call (the host admin proxy and the notification fan-out both
// gate on Online()). The runtime signal must match startup's semantics, while
// hostRow's attached-client guard keeps status truthful: no live client behind
// the fail-open signal keeps a row offline.
func TestHostManageRuntimeSourceMatchesStartupNilOnlineSignal(t *testing.T) {
	client, calls, _ := newScriptedAdminClient(t, func(method string, _ json.RawMessage) hostAdminReply {
		if method != appwire.MethodEvenerInstanceList {
			t.Errorf("forwarded method = %q, want %q", method, appwire.MethodEvenerInstanceList)
		}
		return hostAdminReply{result: map[string]any{"ok": true}}
	})
	live := false
	cfg := hubcore.WebConfig{
		// RemoteHostOnline deliberately nil: the embedder/test shape where
		// startup sources fail open.
		RemoteHostClientIfAttached: func(host string) (*appwire.Client, bool) {
			if host == "side" && live {
				return client, true
			}
			return nil, false
		},
	}
	hosts, err := hostreg.New(nil)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	sources := appsource.NewRegistry()
	m := newHubHostManager(sources, nil, cfg, "", hosts, nil)
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example"}}); err != nil {
		t.Fatalf("Add = %v", err)
	}

	// The added source carries the same fail-open signal a startup source gets
	// in this configuration.
	src, ok := sources.Source("side")
	if !ok {
		t.Fatal("Add registered no source")
	}
	online, ok := src.(appsource.OnlineSource)
	if !ok || !online.Online() {
		t.Fatal("the runtime-added source reports offline with no online signal wired; startup sources fail open in this configuration")
	}

	// A channel-less row stays honestly offline: hostRow's attached-client
	// guard, not the fail-open signal, decides Attached.
	resp, err := m.Status(context.Background(), appwire.HostStatusParams{Name: "side"})
	if err != nil {
		t.Fatalf("Status before the attach = %v", err)
	}
	if resp.Host.Attached {
		t.Fatalf("row = %+v, want offline: no live client backs the fail-open signal", resp.Host)
	}

	// An explicitly attached runtime host is usable by source-mediated calls:
	// the host admin proxy gates on Online() and resolves the live
	// attached-only client behind it.
	live = true
	recorder := newRecordingBroadcaster()
	controller := newHubHostAdminController(recorder, hosts, sources)
	if _, err := controller.Request(context.Background(), appwire.HostRequestParams{
		Host:   "side",
		Method: appwire.MethodEvenerInstanceList,
	}); err != nil {
		t.Fatalf("host/request over the attached runtime host = %v, want the forwarded call to serve", err)
	}
	forwarded := calls()
	if len(forwarded) != 2 || forwarded[1].method != appwire.MethodEvenerInstanceList {
		t.Fatalf("remote calls = %+v, want initialize + the one forwarded request", forwarded)
	}

	// With a live client behind the guard the row renders attached — truthful,
	// not fail-open fiction.
	resp, err = m.Status(context.Background(), appwire.HostStatusParams{Name: "side"})
	if err != nil {
		t.Fatalf("Status after the attach = %v", err)
	}
	if !resp.Host.Attached {
		t.Fatalf("row = %+v, want attached once the attached-only lookup confirms a live client", resp.Host)
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
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example"}}); err == nil {
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
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "keep", Address: "k.example"}}); err != nil {
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
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example"}}); err == nil {
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

// TestHostManageAddRollsSidecarBackWhenLiveInsertFails pins the round-4 L2
// finding: the durable-first commit saves the sidecar before the live insert,
// so a failed insert used to leave the entry in the file — the API reported
// failure, but the next start resurrected the add. The post-failure rollback
// re-persists the pre-add contents, so the durable state never keeps a change
// the API reported as failed.
func TestHostManageAddRollsSidecarBackWhenLiveInsertFails(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	// A manager with no registry: its AddHost refuses deterministically — the
	// one seam that fails between the durable save and the live insert.
	manager := sshconn.New(nil, sshconn.Options{})
	t.Cleanup(func() { _ = manager.Close() })
	sources := appsource.NewRegistry()
	m := newHubHostManager(sources, manager, hubcore.WebConfig{}, configPath, nil, nil)

	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "resurrect", Address: "r.example"}}); err == nil {
		t.Fatal("Add over a manager with no registry succeeded, want the live-insert refusal")
	}
	// The live set committed nothing...
	if _, ok := m.cfg.hosts.Get("resurrect"); ok {
		t.Fatal("Add exposed a registry entry the live insert never committed")
	}
	if m.cfg.sidecar.isSidecar("resurrect") {
		t.Fatal("Add recorded a sidecar row the live insert never committed")
	}
	// ...and neither may the durable file: a restart must not resurrect the
	// add this call reported as failed.
	entries, err := loadHostSidecar(sidecarPathFor(configPath))
	if err != nil {
		t.Fatalf("loadHostSidecar: %v", err)
	}
	for _, e := range entries {
		if e.Name == "resurrect" {
			t.Fatalf("the sidecar kept %q after the live insert failed: the next start would resurrect it", e.Name)
		}
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
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example"}}); err == nil {
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

// TestHostManageSidecarSchemaIsLoudAndKeepsFile pins the round-13 finding end
// to end, extending the corrupt-file contract to the schema-shaped hole: a
// JSON-valid document without a "hosts" array loads as no sidecar entries
// and poisons the store, so the file is never rewritten from the empty
// snapshot. The hub.toml hosts keep serving, the broken document is logged
// at startup, an add fails loudly, and the file keeps its bytes for the
// operator to fix.
func TestHostManageSidecarSchemaIsLoudAndKeepsFile(t *testing.T) {
	for _, doc := range []string{"{}", `{"hosts":null}`} {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "hub.toml")
		if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
			t.Fatalf("write hub.toml: %v", err)
		}
		sidecar := sidecarPathFor(configPath)
		if err := os.WriteFile(sidecar, []byte(doc), 0o600); err != nil {
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
			t.Errorf("%q produced no log line at startup, want the schema refusal logged", doc)
		}
		// The hub.toml host still serves: loud, not fatal.
		if _, err := m.Status(context.Background(), appwire.HostStatusParams{Name: "m4"}); err != nil {
			t.Errorf("Status(m4) over %q = %v, want the hub.toml host to serve", doc, err)
		}
		// The poisoned store refuses to persist: the add fails and commits
		// nothing.
		if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example"}}); err == nil {
			t.Errorf("Add over %q succeeded, want a loud refusal", doc)
		}
		if _, ok := m.cfg.hosts.Get("side"); ok {
			t.Errorf("the refused add over %q committed a host", doc)
		}
		// The schema-invalid document was not rewritten: its bytes survive
		// for the operator to fix.
		data, err := os.ReadFile(sidecar)
		if err != nil {
			t.Fatalf("read sidecar: %v", err)
		}
		if string(data) != doc {
			t.Errorf("%q was rewritten to %q, want the original bytes", doc, data)
		}
	}
}

// TestHostManageSidecarEmptyArrayAcceptsAdd pins the legitimate-empty boundary
// the schema gate must not break: {"hosts":[]} is a well-formed empty sidecar,
// not a schema error — it loads with no log lines and no poison, and an add
// works and persists.
func TestHostManageSidecarEmptyArrayAcceptsAdd(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	sidecar := sidecarPathFor(configPath)
	if err := os.WriteFile(sidecar, []byte(`{"hosts":[]}`), 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	var logs []string
	logf := func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, configPath, nil, logf)
	if len(logs) != 0 {
		t.Fatalf(`{"hosts":[]} logged at startup: %v, want a legitimate empty sidecar`, logs)
	}
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example"}}); err != nil {
		t.Fatalf(`Add over {"hosts":[]} = %v, want success`, err)
	}
	// The add rewrote the file with the entry: a reload serves it.
	m2 := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, configPath, nil, nil)
	if _, err := m2.Status(context.Background(), appwire.HostStatusParams{Name: "side"}); err != nil {
		t.Fatalf("reloaded Status = %v, want the added entry persisted", err)
	}
}

// TestHostManageSidecarLoadNormalizesEntries pins the round-2 LOW: a padded
// sidecar entry is normalized before the collision check, storage, and source
// registration, so the loaded host keys, lists, removes, and reloads under its
// trimmed name instead of splitting its identity between the registry and the
// sidecar row (which would list it with a hub.toml origin and refuse its
// removal as file-declared).
func TestHostManageSidecarLoadNormalizesEntries(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	padded := `{"hosts":[{"name":"  side  ","ssh":"  s.example  ","key_path":"  /keys/s  "}]}`
	if err := os.WriteFile(sidecarPathFor(configPath), []byte(padded), 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	// boot simulates a hub start: fresh registry and sources over the same
	// on-disk sidecar, the shape a restart sees.
	boot := func() *hubHostManager {
		hosts, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
		if err != nil {
			t.Fatalf("hostreg.New: %v", err)
		}
		return newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, configPath, hosts, nil)
	}
	sideRow := func(t *testing.T, m *hubHostManager) appwire.HostRow {
		t.Helper()
		list, err := m.List(context.Background(), appwire.EmptyParams{})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, row := range list.Hosts {
			if row.Name == "side" {
				return row
			}
		}
		t.Fatalf("List = %+v, want the padded sidecar entry loaded under its trimmed name", list.Hosts)
		return appwire.HostRow{}
	}

	// Origin: the padded entry lists under the trimmed name with the sidecar
	// origin and normalized fields.
	m := boot()
	row := sideRow(t, m)
	if row.Origin != hostOriginSidecar || row.Address != "s.example" || row.KeyPath != "/keys/s" {
		t.Fatalf("padded sidecar row = %+v, want side/s.example with the trimmed key", row)
	}
	// Source ID: the source registered under the trimmed name too.
	if _, ok := m.cfg.sources.Source("side"); !ok {
		t.Fatal("the padded sidecar entry registered no source under its trimmed name")
	}
	// Reload: the same padded file loads identically on the next boot.
	m2 := boot()
	if row := sideRow(t, m2); row.Origin != hostOriginSidecar {
		t.Fatalf("reloaded sidecar row = %+v, want the sidecar origin again", row)
	}
	// Removal works: the sidecar row keys off the normalized entry, so the
	// host removes as UI-added rather than being refused as hub.toml-declared.
	if _, err := m2.Remove(context.Background(), appwire.HostRemoveParams{Name: "side"}); err != nil {
		t.Fatalf("Remove(side) = %v, want success for a normalized sidecar entry", err)
	}
	// The file lost the entry, so the next boot does not resurrect it.
	m3 := boot()
	list, err := m3.List(context.Background(), appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("List after remove: %v", err)
	}
	if len(list.Hosts) != 1 || list.Hosts[0].Name != "m4" {
		t.Fatalf("list after the removal-and-reload = %+v, want only m4", list.Hosts)
	}
}

// TestHostManageListStatusSerializeWithCommit pins the round-2 LOW: List and
// Status hold the same mutation mutex Add commits under, so no concurrent
// reader can observe the window between a registry insert and the sidecar
// row and source registration that finish it. Every row a reader sees is
// fully committed or absent — never a sidecar host listed with a hub.toml
// origin or without its source.
func TestHostManageListStatusSerializeWithCommit(t *testing.T) {
	sources := appsource.NewRegistry()
	hosts, err := hostreg.New(nil)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	m := newHubHostManager(sources, nil, hubcore.WebConfig{}, "", hosts, nil)

	var mu sync.Mutex
	var failures []string
	fail := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		failures = append(failures, fmt.Sprintf(format, args...))
	}
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				list, err := m.List(context.Background(), appwire.EmptyParams{})
				if err != nil {
					fail("List: %v", err)
					continue
				}
				for _, row := range list.Hosts {
					if row.Origin != hostOriginSidecar {
						fail("row %q listed with origin %q mid-commit", row.Name, row.Origin)
					}
					if _, ok := sources.Source(row.Name); !ok {
						fail("row %q listed with no source mid-commit", row.Name)
					}
				}
				if _, err := m.Status(context.Background(), appwire.HostStatusParams{Name: "side-00"}); err == nil {
					// A readable row must be fully committed too; Status is
					// the same lock, so this only probes it stays consistent.
					list, err := m.List(context.Background(), appwire.EmptyParams{})
					if err != nil {
						fail("List: %v", err)
						continue
					}
					for _, row := range list.Hosts {
						if row.Name == "side-00" && row.Origin != hostOriginSidecar {
							fail("row side-00 listed with origin %q mid-commit", row.Origin)
						}
					}
				}
			}
		})
	}
	const adds = 24
	var adders sync.WaitGroup
	for i := range adds {
		adders.Go(func() {
			if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: fmt.Sprintf("side-%02d", i), Address: "s.example"}}); err != nil {
				fail("Add(%d): %v", i, err)
			}
		})
	}
	adders.Wait()
	close(stop)
	readers.Wait()
	if len(failures) > 0 {
		t.Fatalf("%d torn reads observed: %v", len(failures), failures[:min(len(failures), 5)])
	}
	list, err := m.List(context.Background(), appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("final List: %v", err)
	}
	if len(list.Hosts) != adds {
		t.Fatalf("final list = %d rows, want %d", len(list.Hosts), adds)
	}
}

// TestHostManageTransientProbeFailureKeepsLastKnownFacts pins the round-5 M1
// finding: an attached row's handshake or facts read can fail transiently
// while the channel stays up, and the failed read must not overwrite the
// last-known facts offline rows later render — every attached read used to
// record all fact fields, so the empty values the failed probes left behind
// blanked the retained identity. The attached row itself still renders the
// failed reads honestly (the live channel is an attached row's only facts
// authority); only the retention is per-lookup.
func TestHostManageTransientProbeFailureKeepsLastKnownFacts(t *testing.T) {
	live := &appwire.Client{}
	var handshakeOK, factsOK bool
	online := true
	sources := appsource.NewRegistry()
	cfg := hubcore.WebConfig{
		RemoteHostOnline: func(string) bool { return online },
		RemoteHostClientIfAttached: func(host string) (*appwire.Client, bool) {
			if host == "h" && online {
				return live, true
			}
			return nil, false
		},
		RemoteHostHandshake: func(host string, client *appwire.Client) (appwire.InitializeResponse, bool) {
			if host == "h" && client == live && handshakeOK {
				return appwire.InitializeResponse{ServerInfo: appwire.ServerInfo{Name: "evener-hub", Version: "0.1.0"}}, true
			}
			return appwire.InitializeResponse{}, false
		},
		RemoteHostFacts: func(_ context.Context, host string, client *appwire.Client) (appsource.HostFacts, error) {
			if host == "h" && client == live && factsOK {
				return appsource.HostFacts{HubVersion: "9.9.9", OS: "linux", Arch: "amd64"}, nil
			}
			return appsource.HostFacts{}, errors.New("transient probe failure")
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
	// Attached with healthy probes: the live facts render and are retained.
	handshakeOK, factsOK = true, true
	m.observeEvent(sshconn.Event{Host: "h", Kind: sshconn.EventAttached})
	row := status()
	if !row.Attached || row.ServerName != "evener-hub" || row.HubVersion != "9.9.9" {
		t.Fatalf("attached row = %+v, want the live handshake and facts", row)
	}
	// The probes start failing while the channel stays up: the attached row
	// renders without the failed reads' fields, but the failure is transient,
	// not a change of identity — it must not blank what the row retains.
	handshakeOK, factsOK = false, false
	row = status()
	if !row.Attached {
		t.Fatalf("row = %+v, want attached: the dial, not the probe, is authoritative", row)
	}
	if row.ServerName != "" || row.ServerVersion != "" || row.HubVersion != "" {
		t.Fatalf("attached row = %+v, want the failed probes' fields to render empty, not stale", row)
	}
	// Offline: the row keeps the last-known facts the healthy read retained —
	// the transient failure never touched them.
	online = false
	m.observeEvent(sshconn.Event{Host: "h", Kind: sshconn.EventDetached})
	row = status()
	if row.Attached {
		t.Fatalf("offline row = %+v, want detached", row)
	}
	if row.ServerName != "evener-hub" || row.ServerVersion != "0.1.0" || row.HubVersion != "9.9.9" || row.OS != "linux" || row.Arch != "amd64" {
		t.Fatalf("offline row = %+v, want the last-known facts the healthy read retained", row)
	}
}

// TestHostManageRetainedFactsFollowPerLookupValidity pins the per-lookup half
// of the round-5 M1 fix: the handshake seam owns the server identity pair and
// the facts seam the hub/os/arch triple, and only a successful lookup replaces
// its own fields — a failed one keeps the previously retained pair/triple,
// and a later success updates only its own. The offline row renders exactly
// the mix of the last successful lookups, never a transient failure's empties.
func TestHostManageRetainedFactsFollowPerLookupValidity(t *testing.T) {
	live := &appwire.Client{}
	var handshakeOK, factsOK bool
	var handshakeName, factsVersion string
	online := true
	sources := appsource.NewRegistry()
	cfg := hubcore.WebConfig{
		RemoteHostOnline: func(string) bool { return online },
		RemoteHostClientIfAttached: func(host string) (*appwire.Client, bool) {
			if host == "h" && online {
				return live, true
			}
			return nil, false
		},
		RemoteHostHandshake: func(host string, client *appwire.Client) (appwire.InitializeResponse, bool) {
			if host == "h" && client == live && handshakeOK {
				return appwire.InitializeResponse{ServerInfo: appwire.ServerInfo{Name: handshakeName, Version: "0.1.0"}}, true
			}
			return appwire.InitializeResponse{}, false
		},
		RemoteHostFacts: func(_ context.Context, host string, client *appwire.Client) (appsource.HostFacts, error) {
			if host == "h" && client == live && factsOK {
				return appsource.HostFacts{HubVersion: factsVersion, OS: "linux", Arch: "amd64"}, nil
			}
			return appsource.HostFacts{}, errors.New("transient probe failure")
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
	// First healthy attach: server identity and facts both retained.
	handshakeOK, factsOK = true, true
	handshakeName, factsVersion = "evener-hub", "9.9.9"
	m.observeEvent(sshconn.Event{Host: "h", Kind: sshconn.EventAttached})
	if row := status(); !row.Attached || row.ServerName != "evener-hub" || row.HubVersion != "9.9.9" {
		t.Fatalf("attached row = %+v, want the live handshake and facts", row)
	}
	// The handshake fails while a facts read succeeds with a new version: the
	// attached row renders the new facts without the server identity, and the
	// retention takes the facts but keeps the previous pair.
	handshakeOK, factsOK = false, true
	factsVersion = "8.8.8"
	if row := status(); !row.Attached || row.ServerName != "" || row.HubVersion != "8.8.8" {
		t.Fatalf("attached row = %+v, want the facts update to render without the failed handshake's fields", row)
	}
	// The reverse: a successful handshake with a new name while the facts
	// read fails — the row renders the new identity without the facts, and
	// the retention takes the pair but keeps the previous triple.
	handshakeOK, factsOK = true, false
	handshakeName = "renamed-hub"
	if row := status(); !row.Attached || row.ServerName != "renamed-hub" || row.HubVersion != "" {
		t.Fatalf("attached row = %+v, want the handshake update to render without the failed facts' fields", row)
	}
	// Offline: the retained record is the mix — the latest successful
	// handshake's pair with the latest successful facts' triple.
	online = false
	m.observeEvent(sshconn.Event{Host: "h", Kind: sshconn.EventDetached})
	row := status()
	if row.Attached {
		t.Fatalf("offline row = %+v, want detached", row)
	}
	if row.ServerName != "renamed-hub" || row.ServerVersion != "0.1.0" {
		t.Fatalf("offline row = %+v, want the last successful handshake's pair", row)
	}
	if row.HubVersion != "8.8.8" || row.OS != "linux" || row.Arch != "amd64" {
		t.Fatalf("offline row = %+v, want the last successful facts' triple", row)
	}
}

// TestHostManageRemoveRollsSidecarBackWhenLiveTeardownFails pins the round-5
// M3 fix from the failing side, mirroring
// TestHostManageAddRollsSidecarBackWhenLiveInsertFails: with a threaded SSH
// manager that owns no registry — the supported embedder shape
// hostRegistryFromConfig keeps a fresh copy for — RemoveHost refuses loudly
// the way AddHost always has, instead of reporting success for a teardown
// that never happened. Pre-fix, RemoveHost returned nil for a nil registry, so
// Remove dropped the sidecar row, the source, and the retained state and
// answered Removed:true while the host stayed in the live registry: listed
// forever under a mislabeled hub.toml origin, refused by add as a duplicate,
// and unremovable. Now the failed teardown rolls the sidecar back — round-4's
// defensive Remove rollback branch, exercised here for the first time — and
// the host stays fully intact for the operator to retry.
func TestHostManageRemoveRollsSidecarBackWhenLiveTeardownFails(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	// A sidecar entry from a previous run: it loads at construction into the
	// fallback registry, the shape hostRegistryFromConfig builds for a
	// manager with no registry — Add would refuse over this manager, so the
	// boot path is the only way the entry gets here.
	if err := saveHostSidecar(sidecarPathFor(configPath), []hostreg.Host{
		{Name: "side", SSH: "s.example", KeyPath: "/keys/s"},
	}); err != nil {
		t.Fatalf("save sidecar: %v", err)
	}
	manager := sshconn.New(nil, sshconn.Options{})
	t.Cleanup(func() { _ = manager.Close() })
	sources := appsource.NewRegistry()
	m := newHubHostManager(sources, manager, hubcore.WebConfig{}, configPath, nil, nil)

	if _, err := m.Remove(context.Background(), appwire.HostRemoveParams{Name: "side"}); err == nil {
		t.Fatal("Remove over a manager with no registry succeeded, want the live-teardown refusal")
	}
	// The live set is fully intact: registry entry, sidecar row, source...
	if _, ok := m.cfg.hosts.Get("side"); !ok {
		t.Fatal("the refused Remove dropped the registry entry")
	}
	if !m.cfg.sidecar.isSidecar("side") {
		t.Fatal("the refused Remove dropped the sidecar row")
	}
	if _, ok := sources.Source("side"); !ok {
		t.Fatal("the refused Remove dropped the source")
	}
	// ...and so is the durable file: the rollback re-persisted the entry, so
	// a restart does not lose the host the API just reported as still present.
	entries, err := loadHostSidecar(sidecarPathFor(configPath))
	if err != nil {
		t.Fatalf("loadHostSidecar: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "side" || entries[0].KeyPath != "/keys/s" {
		t.Fatalf("sidecar after the refused Remove = %+v, want the entry rolled back intact", entries)
	}
	// The row still serves with its sidecar origin — nothing half-removed.
	resp, err := m.Status(context.Background(), appwire.HostStatusParams{Name: "side"})
	if err != nil {
		t.Fatalf("Status after the refused Remove = %v, want the host intact", err)
	}
	if resp.Host.Origin != hostOriginSidecar || resp.Host.Removed {
		t.Fatalf("row = %+v, want the sidecar origin and no removed marker", resp.Host)
	}
	// The failed removal cleared its in-flight mark, so the name stays usable:
	// the retry reports the same live-teardown refusal — not an in-progress
	// conflict leaking from the first attempt — and the host stays intact for
	// it, so the failed teardown never fences the name for good.
	if _, retryErr := m.Remove(context.Background(), appwire.HostRemoveParams{Name: "side"}); retryErr == nil {
		t.Fatal("the retried Remove over a manager with no registry succeeded, want the live-teardown refusal again")
	} else {
		var retryWire appwire.WireError
		if errors.As(retryErr, &retryWire) && retryWire.Code == appwire.CodeConflict {
			t.Fatalf("the retried Remove = %v; the removal-in-progress mark leaked past the failed teardown", retryErr)
		}
	}
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "x.example"}}); err == nil {
		t.Fatal("Add over the intact host succeeded, want the duplicate refusal")
	} else {
		assertWireCode(t, err, appwire.CodeInvalidParams)
	}
	retryEntries, retryFileErr := loadHostSidecar(sidecarPathFor(configPath))
	if retryFileErr != nil {
		t.Fatalf("loadHostSidecar: %v", retryFileErr)
	}
	if len(retryEntries) != 1 || retryEntries[0].Name != "side" {
		t.Fatalf("sidecar after the retried Remove = %+v, want the entry intact again", retryEntries)
	}
}

// TestHostManageListStatusReleaseLockDuringRowReads pins the round-7 M4 fix:
// List, Status, and Add used to hold the mutation mutex across hostRow, whose
// facts read runs on the network — one slow or hung host facts read blocked
// every concurrent Add and Remove commit and serialized concurrent lists. The
// mutex now covers only the snapshot (registry rows plus sidecar origins), so
// a reader parked mid-row no longer holds it: the concurrent Add below must
// commit while the reader is parked. The snapshot keeps the round-2 guarantee
// the parked-reader test used to pin — the reader never observes a
// half-committed host — because the snapshot and the commit serialize on the
// same mutex; the reader sees the pre-commit state, all of it or none.
func TestHostManageListStatusReleaseLockDuringRowReads(t *testing.T) {
	// gate blocks the first online-seam call and lets every later one through
	// immediately — a sync.Once would park later callers (the Add's own row
	// render) behind the gate too, and the Add below must run freely.
	newGate := func() (online func(string) bool, wait func(), release func()) {
		entered := make(chan struct{})
		releaseCh := make(chan struct{})
		var first atomic.Bool
		online = func(string) bool {
			if !first.Load() && first.CompareAndSwap(false, true) {
				close(entered)
				<-releaseCh
			}
			return false
		}
		return online, func() { <-entered }, func() { close(releaseCh) }
	}
	for _, read := range []struct {
		name string
		call func(m *hubHostManager) ([]appwire.HostRow, error)
	}{
		{"List", func(m *hubHostManager) ([]appwire.HostRow, error) {
			list, err := m.List(context.Background(), appwire.EmptyParams{})
			return list.Hosts, err
		}},
		{"Status", func(m *hubHostManager) ([]appwire.HostRow, error) {
			resp, err := m.Status(context.Background(), appwire.HostStatusParams{Name: "m4"})
			return []appwire.HostRow{resp.Host}, err
		}},
	} {
		t.Run(read.name, func(t *testing.T) {
			hosts, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
			if err != nil {
				t.Fatalf("hostreg.New: %v", err)
			}
			online, wait, release := newGate()
			m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{RemoteHostOnline: online}, "", hosts, nil)
			rowsCh := make(chan []appwire.HostRow, 1)
			errCh := make(chan error, 1)
			go func() {
				rows, err := read.call(m)
				errCh <- err
				rowsCh <- rows
			}()
			wait() // the reader is parked mid-row, inside its row reads.
			// The Add must commit while the reader is parked: row reads hold no
			// mutation mutex, so a slow facts read cannot block a commit.
			addDone := make(chan error, 1)
			go func() {
				_, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example"}})
				addDone <- err
			}()
			select {
			case err := <-addDone:
				if err != nil {
					t.Fatalf("Add while the reader was parked mid-row: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Add never committed while a reader was parked mid-row: List/Status still hold the mutation mutex across their row reads")
			}
			release()
			if err := <-errCh; err != nil {
				t.Fatalf("reader = %v", err)
			}
			// The reader snapshotted before the Add committed, so its rows are
			// the pre-commit state: the added host is absent, not half-committed.
			for _, row := range <-rowsCh {
				if row.Name == "side" {
					t.Fatalf("reader observed the added host %q as a half-committed row: %+v", row.Name, row)
				}
			}
			list, err := m.List(context.Background(), appwire.EmptyParams{})
			if err != nil {
				t.Fatalf("final List: %v", err)
			}
			if len(list.Hosts) != 2 {
				t.Fatalf("final list = %d rows, want both hosts after the commit", len(list.Hosts))
			}
		})
	}
}

// blockingRunner parks the first process call the SSH manager makes — a
// preflight probe or the attach dial, whichever comes first — until the test
// releases it. An Ensure that parks there holds the manager's per-host gate
// for the probe's whole duration, which is exactly what makes
// Manager.RemoveHost block: the removal's teardown waits for the same gate a
// supervisor's reconnect/ensure cycle can hold for minutes. Later calls fail
// fast, so the released sequence unwinds promptly.
type blockingRunner struct {
	entered  chan struct{}
	release  chan struct{}
	parked   atomic.Bool
	returned atomic.Bool
}

// block parks the first caller until release (or ctx dies, so a hung test
// still unwinds) and refuses every later call.
func (r *blockingRunner) block(ctx context.Context) error {
	if r.parked.CompareAndSwap(false, true) {
		close(r.entered)
		select {
		case <-r.release:
			r.returned.Store(true)
			return errors.New("blockingRunner: released")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return errors.New("blockingRunner: refusing a follow-up call")
}

func (r *blockingRunner) Run(ctx context.Context, _ []string, _ io.Reader) ([]byte, error) {
	return nil, r.block(ctx)
}

func (r *blockingRunner) Start(ctx context.Context, _ []string, _ io.Writer) (sshconn.Stdio, error) {
	return nil, r.block(ctx)
}

// removeOutcome carries the parked Remove's result back to the test.
type removeOutcome struct {
	resp appwire.HostRemoveResponse
	err  error
}

// parkedRemoval is one removal driven into its released teardown window: an
// Ensure parked inside the manager's first probe holds the per-host gate, so
// the Remove that follows parks inside Manager.RemoveHost's gate wait. The
// removal's commit has landed by the time the helper returns — the sidecar
// row and the file already lost the entry, and the mark fences the name — and
// the window stays open until release.
type parkedRemoval struct {
	m          *hubHostManager
	sources    *appsource.Registry
	configPath string
	runner     *blockingRunner
	release    func()
	ensureDone chan error
	removeDone chan removeOutcome
}

// startParkedRemoval drives the removal of name into its teardown window over
// a real SSH manager with the blocking runner: the Add calls commit through
// the manager's registry, the parked Ensure holds the per-host gate inside its
// first probe, and the Remove parks behind it. "keep" is a second sidecar
// host so the window's refusals and saves have an unrelated entry to leave
// alone. The cleanup releases the parks and drains both goroutines
// (registered after the manager's, so it runs first): a failing test's
// teardown never races a removal still writing its sidecar into the
// temp dir.
func startParkedRemoval(t *testing.T, name string) *parkedRemoval {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	reg, err := hostreg.New(nil)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	runner := &blockingRunner{entered: make(chan struct{}), release: make(chan struct{})}
	manager := sshconn.New(reg, sshconn.Options{Runner: runner})
	t.Cleanup(func() { _ = manager.Close() })
	sources := appsource.NewRegistry()
	m := newHubHostManager(sources, manager, hubcore.WebConfig{}, configPath, reg, nil)
	for _, host := range []appwire.HostAddParams{
		{Entry: appwire.HostEntry{Name: "keep", Address: "keep.example"}},
		{Entry: appwire.HostEntry{Name: name, Address: name + ".example"}},
	} {
		if _, err := m.Add(context.Background(), host); err != nil {
			t.Fatalf("Add(%s): %v", host.Entry.Name, err)
		}
	}
	// Park an Ensure for the host inside its first probe: it holds the
	// per-host gate the removal's teardown below must wait for.
	var parked sync.WaitGroup
	parked.Add(2)
	ensureDone := make(chan error, 1)
	go func() {
		defer parked.Done()
		_, err := manager.Ensure(context.Background(), name)
		ensureDone <- err
	}()
	<-runner.entered
	// The removal parks in its teardown, inside RemoveHost's gate wait.
	removeDone := make(chan removeOutcome, 1)
	go func() {
		defer parked.Done()
		resp, err := m.Remove(context.Background(), appwire.HostRemoveParams{Name: name})
		removeDone <- removeOutcome{resp: resp, err: err}
	}()
	// The removal's durable commit landed once the file lost the entry: the
	// save runs under the mutation mutex, ahead of the teardown, on both the
	// pre-fix and post-fix code. The condition persists until release, so the
	// poll cannot race past the window.
	waitSidecarLacks(t, configPath, name)
	var releaseOnce sync.Once
	pr := &parkedRemoval{
		m:          m,
		sources:    sources,
		configPath: configPath,
		runner:     runner,
		release:    func() { releaseOnce.Do(func() { close(runner.release) }) },
		ensureDone: ensureDone,
		removeDone: removeDone,
	}
	t.Cleanup(func() {
		// Release the parks, then drain both goroutines before the manager's
		// Close and the temp dir removal run: a failing test's teardown must
		// not race a removal still writing its sidecar.
		pr.release()
		parked.Wait()
	})
	return pr
}

// waitSidecarLacks polls the sidecar file until it holds no entry for name,
// with a deadline that only a genuine failure to commit can hit.
func waitSidecarLacks(t *testing.T, configPath, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, err := loadHostSidecar(sidecarPathFor(configPath))
		found := false
		for _, e := range entries {
			if e.Name == name {
				found = true
			}
		}
		if err == nil && !found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the removal of %q never committed: sidecar = %+v (%v)", name, entries, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// assertSidecarNames pins the sidecar file's exact entry list, in order — the
// durable state the window's refusals and commits are asserted against.
func assertSidecarNames(t *testing.T, configPath string, want ...string) {
	t.Helper()
	entries, err := loadHostSidecar(sidecarPathFor(configPath))
	if err != nil {
		t.Fatalf("loadHostSidecar: %v", err)
	}
	got := make([]string, 0, len(entries))
	for _, e := range entries {
		got = append(got, e.Name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sidecar entries = %v, want %v", got, want)
	}
}

// served runs call and fails the test if it does not return while the removal
// window is open — so a regression reads as "%s never served", never as a
// hung test binary.
func served(t *testing.T, name string, call func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- call() }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("%s never served while a removal was parked in its teardown", name)
		return nil
	}
}

// waitRemovalDone collects the parked Remove's result once the window closes.
func (pr *parkedRemoval) waitRemovalDone(t *testing.T) removeOutcome {
	t.Helper()
	select {
	case done := <-pr.removeDone:
		return done
	case <-time.After(5 * time.Second):
		t.Fatal("the removal never finished after its teardown was released")
		return removeOutcome{}
	}
}

// TestHostManageRemoveReleasesLockDuringTeardown pins the round-8 finding:
// Remove used to hold the mutation mutex across the manager teardown, whose
// RemoveHost blocks on the per-host gate a supervisor's reconnect/ensure
// cycle holds — and on the ssh child's exit — so one host's slow or hung
// removal froze every concurrent host/list, host/status, and host/add for
// every host (the Settings hosts pane polls host/list every two seconds).
// The teardown now runs mutex-free: while the removal below is parked inside
// RemoveHost (an Ensure holding the gate through a probe the test keeps
// blocked), List and Status keep serving, and the mid-removal row still
// renders its sidecar origin. Every park is a channel the test holds open,
// never a timed sleep.
func TestHostManageRemoveReleasesLockDuringTeardown(t *testing.T) {
	pr := startParkedRemoval(t, "side")

	// Liveness, the finding itself: host/list must serve while the removal
	// is parked in its teardown. Pre-fix this deadlocked — Remove held the
	// mutation mutex across the parked RemoveHost.
	var list appwire.HostListResponse
	if err := served(t, "host/list", func() error {
		resp, err := pr.m.List(context.Background(), appwire.EmptyParams{})
		list = resp
		return err
	}); err != nil {
		t.Fatalf("host/list during the teardown: %v", err)
	}
	// The teardown was still parked when List served — the two genuinely
	// overlapped; this is not a list that ran after the removal finished.
	if !pr.runner.parked.Load() || pr.runner.returned.Load() {
		t.Fatal("the parked teardown finished before host/list served; the test did not hold the window open")
	}
	// The mid-removal row is fully committed, never half-gone: the registry
	// entry still lists, under its sidecar origin — the removal mark stands
	// in for the store row the commit already dropped.
	if len(list.Hosts) != 2 {
		t.Fatalf("list during the removal = %d rows, want keep + side: %+v", len(list.Hosts), list.Hosts)
	}
	for _, row := range list.Hosts {
		if row.Name == "side" && (row.Origin != hostOriginSidecar || row.Removed) {
			t.Fatalf("mid-removal row = %+v, want the sidecar origin and no removed marker", row)
		}
	}
	// host/status serves through the same window, with the same origin.
	var status appwire.HostStatusResponse
	if err := served(t, "host/status", func() error {
		resp, err := pr.m.Status(context.Background(), appwire.HostStatusParams{Name: "side"})
		status = resp
		return err
	}); err != nil {
		t.Fatalf("host/status during the teardown: %v", err)
	}
	if status.Host.Origin != hostOriginSidecar {
		t.Fatalf("mid-removal status row = %+v, want the sidecar origin", status.Host)
	}

	// Let the teardown finish and the removal complete.
	pr.release()
	if err := <-pr.ensureDone; err == nil {
		t.Fatal("the parked Ensure succeeded; the blocking runner must fail the probe")
	}
	done := pr.waitRemovalDone(t)
	if done.err != nil {
		t.Fatalf("Remove: %v", done.err)
	}
	if !done.resp.Host.Removed {
		t.Fatalf("remove row = %+v, want Removed", done.resp.Host)
	}
	// The end state is the pre-fix one: gone from the registry, the sources,
	// the sidecar store, and the durable file.
	if _, ok := pr.m.cfg.hosts.Get("side"); ok {
		t.Fatal("removed host still in the registry")
	}
	if _, ok := pr.sources.Source("side"); ok {
		t.Fatal("removed host still has a source")
	}
	if pr.m.cfg.sidecar.isSidecar("side") {
		t.Fatal("removed host still in the sidecar store")
	}
	assertSidecarNames(t, pr.configPath, "keep")
}

// TestHostManageRemoveWindowFencesTheNameAndKeepsConcurrentCommits pins the
// correctness the released window needs: while a removal is parked in its
// mutex-free teardown, a concurrent Add and a second Remove of the same name
// refuse with the typed conflict and commit nothing (no re-exposed host, no
// double teardown, no sidecar write), while an Add of a different name
// commits — and its save must not resurrect the entry the removal already
// committed, because the removal dropped its store row with its save. The
// reloaded manager proves the durable outcome: the removed host stays gone
// across a restart and the concurrently added host survives it. The fence
// lifts with the removal, so the name is addable again afterwards.
func TestHostManageRemoveWindowFencesTheNameAndKeepsConcurrentCommits(t *testing.T) {
	pr := startParkedRemoval(t, "side")

	// The fenced name refuses both callers with the typed conflict...
	if err := served(t, "host/add", func() error {
		_, err := pr.m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "resurrect.example"}})
		return err
	}); err == nil {
		t.Fatal("Add of the mid-removal name committed, want the removal conflict")
	} else {
		assertWireCode(t, err, appwire.CodeConflict)
	}
	if err := served(t, "host/remove", func() error {
		_, err := pr.m.Remove(context.Background(), appwire.HostRemoveParams{Name: "side"})
		return err
	}); err == nil {
		t.Fatal("a second Remove of the mid-removal name committed, want the removal conflict")
	} else {
		assertWireCode(t, err, appwire.CodeConflict)
	}
	// ...committing nothing: the sidecar the removal's commit wrote is the
	// whole durable state of the window.
	assertSidecarNames(t, pr.configPath, "keep")

	// A different name commits through the window — one host's teardown holds
	// up no other host's add — and its save derives from the live store,
	// which no longer holds the entry being removed.
	if err := served(t, "host/add (other)", func() error {
		_, err := pr.m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "other", Address: "other.example"}})
		return err
	}); err != nil {
		t.Fatalf("Add(other) during the removal window: %v", err)
	}
	assertSidecarNames(t, pr.configPath, "keep", "other")

	// Release the window: the removal completes and its end state holds.
	pr.release()
	if err := <-pr.ensureDone; err == nil {
		t.Fatal("the parked Ensure succeeded; the blocking runner must fail the probe")
	}
	done := pr.waitRemovalDone(t)
	if done.err != nil {
		t.Fatalf("Remove: %v", done.err)
	}
	if !done.resp.Host.Removed {
		t.Fatalf("remove row = %+v, want Removed", done.resp.Host)
	}
	if _, ok := pr.m.cfg.hosts.Get("side"); ok {
		t.Fatal("removed host still in the registry")
	}
	assertSidecarNames(t, pr.configPath, "keep", "other")

	// A fresh boot over the same config is the resurrection check: the
	// removed host must stay gone, the concurrently added one must survive.
	boot := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, pr.configPath, nil, nil)
	list, err := boot.List(context.Background(), appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("reloaded List: %v", err)
	}
	names := make([]string, 0, len(list.Hosts))
	for _, row := range list.Hosts {
		names = append(names, row.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"keep", "other"}) {
		t.Fatalf("reloaded hosts = %v, want keep + other only: a mid-window save resurrected or lost an entry", names)
	}

	// The fence lifted with the removal: the name is addable again.
	if _, err := pr.m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "fresh.example"}}); err != nil {
		t.Fatalf("re-Add after the removal finished: %v", err)
	}
	assertSidecarNames(t, pr.configPath, "keep", "other", "side")
}
