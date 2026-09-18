package hub

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
