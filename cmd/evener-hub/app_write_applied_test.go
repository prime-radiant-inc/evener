package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
)

// writeApplied must not change the message or wire class of what it wraps:
// only errors.Is(err, hubcore.ErrWriteApplied) should see anything different.
func TestWriteAppliedKeepsTheInnerMessageAndWireClass(t *testing.T) {
	plain := errors.New("the previous config could not be restored")
	wrapped := writeApplied(plain)
	if got := wrapped.Error(); got != plain.Error() {
		t.Fatalf("writeApplied(plain).Error() = %q, want %q with no sentinel prefix", got, plain.Error())
	}
	if !writeDidApply(wrapped) {
		t.Fatal("writeApplied(plain) does not answer writeDidApply")
	}

	wire := appwire.Conflict("stale revision")
	wrappedWire := writeApplied(wire)
	if got := wrappedWire.Error(); got != wire.Error() {
		t.Fatalf("writeApplied(wire).Error() = %q, want %q", got, wire.Error())
	}
	got, ok := errors.AsType[appwire.WireError](wrappedWire)
	if !ok || got.Code != wire.Code {
		t.Fatalf("writeApplied(wire) = %v, want the WireError still resolvable with code %d", wrappedWire, wire.Code)
	}
}

// blockProvidersWrites leaves a directory where WriteConfigFile stages its
// bytes, so every later write of providers.toml fails on a real filesystem
// refusal (the technique breakCredentialWrites uses on the credentials file).
func blockProvidersWrites(t *testing.T, tomlPath string) {
	t.Helper()
	// The reload this runs from can be attempted more than once in a call, and
	// the path only has to be occupied, not freshly created.
	if err := os.Mkdir(tomlPath+".tmp", 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		t.Errorf("blocking the providers.toml temp path: %v", err)
	}
}

// instanceRollbackFixture serves an RPC hub whose registry reloads normally
// until failReload is set, and then fails the reload after blocking the
// rollback write that follows it — the double failure #1543 is about, which
// leaves providers.toml carrying a change no rollback could undo.
type instanceRollbackFixture struct {
	hub        *httptest.Server
	tomlPath   string
	failReload *atomic.Bool
}

func newInstanceRollbackFixture(t *testing.T) *instanceRollbackFixture {
	t.Helper()
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)
	credsStore := newTestCredentialsStore(t)
	failReload := &atomic.Bool{}
	load := testRegistryLoader(t.TempDir(), tomlPath, credsStore, nil)
	reg := hubcore.NewProviderRegistry(func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		if failReload.Load() {
			blockProvidersWrites(t, tomlPath)
			return nil, nil, errors.New("the registry refused to load")
		}
		return load(extra...)
	})
	if err := reg.Reload(); err != nil {
		t.Fatalf("registry: %v", err)
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            reg,
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        dir,
		CredsStore:          credsStore,
	})
	t.Cleanup(hub.Close)
	return &instanceRollbackFixture{hub: hub, tomlPath: tomlPath, failReload: failReload}
}

// waitForAuthUpdatedBroadcast fails the test unless evener/auth/updated
// arrives, which is what tells every other client its instance list is stale.
func waitForAuthUpdatedBroadcast(t *testing.T, client *appwire.Client, what string) {
	t.Helper()
	for {
		select {
		case got := <-client.Notifications():
			if got.Method == appwire.NotifyEvenerAuthUpdated {
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for evener/auth/updated after %s", what)
		}
	}
}

// A create whose reload failed and whose rollback could not be written leaves
// the new instance in providers.toml: the config changed, so every other
// client's list is stale and the hub owes them evener/auth/updated, even
// though this caller is told the create failed (#1543).
func TestHubRPCInstanceCreateBroadcastsWhenTheRollbackCouldNotBeWritten(t *testing.T) {
	f := newInstanceRollbackFixture(t)
	client := dialHubRPC(t, f.hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	f.failReload.Store(true)

	var resp appwire.InstanceListResponse
	err := client.Request(context.Background(), appwire.MethodEvenerInstanceCreate,
		appwire.InstanceCreateParams{Base: "anthropic", Name: "mywork"}, &resp)
	if err == nil {
		t.Fatal("evener/instance/create = nil, want the failed rollback reported")
	}
	if !strings.Contains(err.Error(), "restoring the previous config failed") {
		t.Fatalf("evener/instance/create = %v, want the double failure this test is about", err)
	}
	waitForAuthUpdatedBroadcast(t, client, "a create whose rollback could not be written")
}

// The removal sibling of the create case above: the entry is gone from the
// config and the rollback could not put it back, so the removal stands and
// every other client is holding a list with an instance that no longer exists.
func TestHubRPCInstanceRemoveBroadcastsWhenTheRollbackCouldNotBeWritten(t *testing.T) {
	f := newInstanceRollbackFixture(t)
	client := dialHubRPC(t, f.hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	var created appwire.InstanceListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerInstanceCreate,
		appwire.InstanceCreateParams{Base: "anthropic", Name: "doomed"}, &created); err != nil {
		t.Fatalf("evener/instance/create: %v", err)
	}
	waitForAuthUpdatedBroadcast(t, client, "the create this removal undoes")
	f.failReload.Store(true)

	var resp appwire.InstanceListResponse
	err := client.Request(context.Background(), appwire.MethodEvenerInstanceRemove,
		appwire.InstanceRemoveParams{Name: "doomed"}, &resp)
	if err == nil {
		t.Fatal("evener/instance/remove = nil, want the failed rollback reported")
	}
	if !strings.Contains(err.Error(), "the removal stands in the config") {
		t.Fatalf("evener/instance/remove = %v, want the double failure this test is about", err)
	}
	// The removal stood, so the client is told so through the wire data rather
	// than left to retry a failed remove against an instance that is gone.
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInternalError {
		t.Fatalf("error = %T %v, want wire code %d", err, err, appwire.CodeInternalError)
	}
	dataJSON, merr := json.Marshal(wire.Data)
	if merr != nil {
		t.Fatal(merr)
	}
	var data appwire.ErrorData
	if err := json.Unmarshal(dataJSON, &data); err != nil {
		t.Fatalf("decode remove-applied error data: %v", err)
	}
	if data.EvenerErrorInfo != appwire.ErrorInstanceRemoveApplied {
		t.Fatalf("evenerErrorInfo = %q, want %q", data.EvenerErrorInfo, appwire.ErrorInstanceRemoveApplied)
	}
	waitForAuthUpdatedBroadcast(t, client, "a removal whose rollback could not be written")
}

// The cleanup's own failure can also fail to restore what it already
// deleted: TestInstances_RemoveRestoresTheStoredKeyWhenTheOAuthRecordCannotBeDeleted
// covers the OAuth delete failing after the stored key is already gone, with
// a clean restore of that key. This is its double-failure sibling - putting
// the key back also fails - so the key stays deleted even though [providers.work]
// never moved, and every other client's credential status for it is stale.
func TestInstances_RemoveMarksAppliedWhenTheDeletedCredentialCannotBeRestored(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := authopenai.SaveAuth(f.stateDir, "work", makeOAuthRecord("work", "")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	f.ctl.auth.deleteAuth = func(string, string) (bool, error) { return false, errors.New("delete refused") }
	f.ctl.auth.setCredential = func(string, string) error { return errors.New("restore refused") }

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})
	if err == nil {
		t.Fatal("Remove = nil, want the cleanup and restore failures reported")
	}
	if !writeDidApply(err) {
		t.Fatalf("Remove = %v (%T), want an applied write: the stored key stayed deleted", err, err)
	}
	// supplyAny here: the authored entry never moved, so the instance is still
	// configured (a Codex OAuth record carries it). The applied marker is owed
	// for the broadcast, but the standing-removal discriminator is not - the
	// removal did not stand.
	if _, applied := errors.AsType[removeAppliedError](err); applied {
		t.Fatalf("Remove = %v (%T), want a plain applied write: [providers.work] never moved", err, err)
	}
}

// TestInstances_RemoveRestoresCredentialsWhenTheConfigWriteFails covers the
// config write failing with a clean credential restore. This is its
// double-failure sibling: the credentials stay deleted even though
// [providers.work] never moved, so every other client's credential status
// for it is stale.
func TestInstances_RemoveMarksAppliedWhenTheConfigWriteFailsAndTheCredentialCannotBeRestored(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := authopenai.SaveAuth(f.stateDir, "work", makeOAuthRecord("work", "")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	originalDelete := f.ctl.auth.deleteAuth
	f.ctl.auth.deleteAuth = func(dir, name string) (bool, error) {
		if err := os.Remove(f.tomlPath); err != nil {
			t.Errorf("Remove(%s): %v", f.tomlPath, err)
		}
		if err := os.Mkdir(f.tomlPath, 0o700); err != nil {
			t.Errorf("Mkdir(%s): %v", f.tomlPath, err)
		}
		return originalDelete(dir, name)
	}
	f.ctl.auth.setCredential = func(string, string) error { return errors.New("restore refused") }

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})
	if err == nil {
		t.Fatal("Remove = nil, want the config write failure reported")
	}
	if !writeDidApply(err) {
		t.Fatalf("Remove = %v (%T), want an applied write: the credentials stayed deleted", err, err)
	}
	// The config write failed, so the authored entry is still there and the
	// instance still resolves: a plain applied write, not a standing removal.
	if _, applied := errors.AsType[removeAppliedError](err); applied {
		t.Fatalf("Remove = %v (%T), want a plain applied write: [providers.work] never moved", err, err)
	}
	if !strings.Contains(err.Error(), "the instance is still configured") {
		t.Fatalf("Remove = %v, want the configured frame: [providers.work] never moved", err)
	}
	if strings.Contains(err.Error(), "the removal stands") {
		t.Fatalf("Remove = %v, want no standing frame: the authored entry never moved", err)
	}
}

// The implicit sibling of the case above: an instance with no authored entry,
// named only by `default`, is kept alive by its stored key alone. The config
// write that would have dropped the pointer never landed, so the strict
// supplyAny question is the wrong one to ask - the instance does not resolve
// from a file that never changed, it resolves from the key this call deleted.
// When the key cannot be put back the removal stands, and the caller must be
// told so: the standing frame and the removeApplied discriminator are what
// instanceRemoveError maps to ErrorInstanceRemoveApplied, which is what makes
// the client reconcile a removal that already applied instead of presenting a
// failed remove and retrying it against an instance that is gone. The
// writeApplied mark stays, because the deleted key is what every other
// client's credential status for this name is stale against.
func TestInstances_RemoveStandsWhenTheImplicitConfigWriteFailsAndTheStoredKeyCannotBeRestored(t *testing.T) {
	f := newFlakyReloadFixture(t, "groq", func(int) bool { return false })
	if before := entry(t, f.ctl.List(), "groq"); !before.Implicit || before.ActiveSource != "store" {
		t.Fatalf("fixture: groq = %+v, want an implicit instance resolving the stored key", before)
	}
	// The `default` pointer is the only thing in the file naming this instance,
	// and it is what makes the removal write at all: with no [providers.groq]
	// to delete, dropping it is the change the failing write would have made.
	if err := f.ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "groq"}); err != nil {
		t.Fatalf("SetDefault: %v", err)
	}
	l, _, err := registry.ReadConfigFile(f.tomlPath)
	if err != nil {
		t.Fatalf("ReadConfigFile: %v", err)
	}
	if l.Default != "groq" || len(l.Providers) != 0 {
		t.Fatalf("fixture: providers.toml = %+v, want a default pointer and no authored entry", l)
	}
	blockProvidersWrites(t, f.tomlPath)
	f.ctl.auth.setCredential = func(string, string) error { return errors.New("restore refused") }

	err = f.ctl.Remove(appwire.InstanceRemoveParams{Name: "groq"})
	if err == nil {
		t.Fatal("Remove = nil, want the config write failure reported")
	}
	if strings.Contains(err.Error(), "the instance is still configured") {
		t.Fatalf("Remove = %v, want no configured frame: the key that carried the instance stayed deleted", err)
	}
	if !strings.Contains(err.Error(), "the removal stands") {
		t.Fatalf("Remove = %v, want the standing removal named", err)
	}
	if !strings.Contains(err.Error(), "stored key could not be restored") {
		t.Fatalf("Remove = %v, want the unrestored layer named", err)
	}
	if !writeDidApply(err) {
		t.Fatalf("Remove = %v (%T), want an applied write: the stored key stayed deleted", err, err)
	}
	if _, applied := errors.AsType[removeAppliedError](err); !applied {
		t.Fatalf("Remove = %v (%T), want the standing-removal discriminator: the carrying key stayed deleted", err, err)
	}
	// The report is honest only if the key really is gone: this is a removal no
	// retry can complete.
	if v, _ := f.store.Get("groq"); v != "" {
		t.Fatalf("the stored key = %q, want it gone with the standing removal", v)
	}
}

// The OAuth-carrying variant of the case above: the implicit instance resolves
// from its Codex record instead of a stored key, and that record - the layer
// that carries it - cannot be put back. Same standing removal, same frame and
// discriminator, because supplyOAuth names the layer that is gone.
func TestInstances_RemoveStandsWhenTheImplicitConfigWriteFailsAndTheOAuthRecordCannotBeRestored(t *testing.T) {
	f := newFlakyReloadFixture(t, "", func(int) bool { return false })
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")
	if before := entry(t, f.ctl.List(), "openai-codex"); !before.Implicit || before.ActiveSource != "oauth" {
		t.Fatalf("fixture: openai-codex = %+v, want an implicit instance resolving the OAuth record", before)
	}
	if err := f.ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "openai-codex"}); err != nil {
		t.Fatalf("SetDefault: %v", err)
	}
	blockProvidersWrites(t, f.tomlPath)
	authPath := authopenai.AuthFilePath(f.stateDir, "openai-codex")
	originalDelete := f.ctl.auth.deleteAuth
	f.ctl.auth.deleteAuth = func(dir, name string) (bool, error) {
		removed, err := originalDelete(dir, name)
		if err == nil && removed {
			// Occupy the record's path so the atomic restore cannot land: the
			// layer that carried the instance stays deleted.
			if mkErr := os.Mkdir(authPath, 0o700); mkErr != nil {
				t.Errorf("Mkdir(%s): %v", authPath, mkErr)
			}
		}
		return removed, err
	}

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"})
	if err == nil {
		t.Fatal("Remove = nil, want the failed restore reported")
	}
	if strings.Contains(err.Error(), "the instance is still configured") {
		t.Fatalf("Remove = %v, want no configured frame: the record that carried the instance stayed deleted", err)
	}
	if !strings.Contains(err.Error(), "the removal stands") {
		t.Fatalf("Remove = %v, want the standing removal named", err)
	}
	if !strings.Contains(err.Error(), "OAuth record could not be restored") {
		t.Fatalf("Remove = %v, want the unrestored layer named", err)
	}
	if !writeDidApply(err) {
		t.Fatalf("Remove = %v (%T), want an applied write: the carrying record stayed deleted", err, err)
	}
	if _, applied := errors.AsType[removeAppliedError](err); !applied {
		t.Fatalf("Remove = %v (%T), want the standing-removal discriminator: the carrying record stayed deleted", err, err)
	}
	if info, statErr := os.Stat(authPath); statErr != nil || !info.IsDir() {
		t.Fatalf("auth path = %v (err %v), want the record still not restored", info, statErr)
	}
}

// The other half of the implicit classification: when the layer that carries
// the instance is back, the removal did not stand, so neither the standing
// frame nor the discriminator may appear. A stray second credential that stays
// deleted is still an applied change - every other client's credential status
// for the name is stale against it - so the write failure and the writeApplied
// mark have to come back around the leftovers, exactly as the reload-rollback
// path folds them.
func TestInstances_RemoveRollsBackWhenTheImplicitConfigWriteFailsAndTheCarryingRecordIsRestored(t *testing.T) {
	f := newFlakyReloadFixture(t, "", func(int) bool { return false })
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")
	// The stray key beside the carrying record: it cannot be restored, while
	// the record can.
	if err := f.store.Set("openai-codex", "sk-stray"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("priming reload: %v", err)
	}
	if inst, ok := f.ctl.reg.Get().Instance("openai-codex"); !ok || inst.CredentialSource != "oauth" {
		t.Fatalf("openai-codex = %+v ok=%v, want an OAuth-backed instance", inst, ok)
	}
	if err := f.ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "openai-codex"}); err != nil {
		t.Fatalf("SetDefault: %v", err)
	}
	blockProvidersWrites(t, f.tomlPath)
	f.ctl.auth.setCredential = func(string, string) error { return errors.New("restore refused") }

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"})
	if err == nil {
		t.Fatal("Remove = nil, want the config write failure reported")
	}
	if strings.Contains(err.Error(), "the removal stands") {
		t.Fatalf("Remove = %v, want a rollback: the carrying record is back", err)
	}
	if _, applied := errors.AsType[removeAppliedError](err); applied {
		t.Fatalf("Remove = %v (%T), want no standing-removal discriminator: the carrying record is back", err, err)
	}
	if !strings.Contains(err.Error(), "some credentials were not put back") {
		t.Fatalf("Remove = %v, want the leftover stray key reported", err)
	}
	if !writeDidApply(err) {
		t.Fatalf("Remove = %v (%T), want an applied write: the stray key stayed deleted", err, err)
	}
	if _, statErr := os.Stat(authopenai.AuthFilePath(f.stateDir, "openai-codex")); statErr != nil {
		t.Fatalf("the OAuth record was not restored: %v", statErr)
	}
	if v, _ := f.store.Get("openai-codex"); v != "" {
		t.Fatalf("stored key = %q, want the stray key still deleted", v)
	}
}

// TestInstances_RemoveRestoresTheCredentialBeforeTheRollbackReload covers the
// removal's reload failing, the config rollback landing, and the rollback's
// own reload succeeding, with a clean credential restore - the removal never
// applied, so that test wants a plain refusal. This is its double-failure
// sibling: the credential restore itself fails, so the key stays deleted
// even though [providers.work] is back, and every other client's credential
// status for it is stale.
func TestInstances_RemoveMarksAppliedWhenTheRollbackSucceedsButTheCredentialCannotBeRestored(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// The layer the removal's reload is made to fail on: an entry that parses
	// but cannot resolve an endpoint (#711), the same technique
	// TestInstances_RemoveRestoresTheCredentialBeforeTheRollbackReload uses.
	brokenPath := filepath.Join(filepath.Dir(f.tomlPath), "broken.toml")
	if err := os.WriteFile(brokenPath, []byte("[providers.standalone]\nprotocol = \"openai-chat\"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	var loads int
	loadFn := func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		loads++
		store, err := credentials.LoadStore(f.credsPath)
		if err != nil {
			return nil, nil, err
		}
		path := f.tomlPath
		if loads == 2 {
			path = brokenPath
		}
		opts := append(
			testProbeRegistryOptions(f.stateDir, store, func(string) (string, bool) { return "", false }),
			registry.WithConfigPath(path),
		)
		r, err := registry.Load(append(opts, extra...)...)
		return r, store, err
	}
	replacement := hubcore.NewProviderRegistry(loadFn)
	f.ctl.reg = replacement
	f.ctl.auth.reg = replacement
	if err := replacement.Reload(); err != nil {
		t.Fatalf("prime Reload: %v", err)
	}
	f.ctl.auth.setCredential = func(string, string) error { return errors.New("restore refused") }

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})
	if err == nil {
		t.Fatal("Remove = nil, want the reload failure reported")
	}
	if !writeDidApply(err) {
		t.Fatalf("Remove = %v (%T), want an applied write: the stored key stayed deleted despite the config rollback", err, err)
	}
	// The config rolled back, so the instance is configured again: the removal
	// did not stand, and the client must not be steered off a live instance.
	if _, applied := errors.AsType[removeAppliedError](err); applied {
		t.Fatalf("Remove = %v (%T), want a plain applied write: the config rollback left it configured", err, err)
	}
}

// An implicit instance carried by its Codex OAuth record can carry a stray
// stored key beside it. The removal's reload fails, and only the stray key
// cannot be put back while the record - the layer the instance actually
// resolves from - is restored. The instance is configured again, so the
// caller must hear a rollback, not "the removal stands", and the row must be
// republished by the recovery reload. The stray key is still gone, though, so
// the error also answers writeApplied: every other client's credential status
// for the name is stale against that deletion, and the marker is what makes
// the RPC layer broadcast it.
func TestInstances_RemoveRollsBackWhenTheCarryingRecordIsRestored(t *testing.T) {
	// Load 1 is the fixture's own, load 2 this test's priming reload, load 3
	// the removal's, and load 4 the rollback's recovery reload.
	f := newFlakyReloadFixture(t, "openai-codex", func(load int) bool { return load == 3 })
	if err := authopenai.SaveAuth(f.stateDir, "openai-codex", makeOAuthRecord("openai-codex", "")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("priming reload: %v", err)
	}
	if inst, ok := f.ctl.reg.Get().Instance("openai-codex"); !ok || inst.CredentialSource != "oauth" {
		t.Fatalf("openai-codex = %+v ok=%v, want an OAuth-backed instance", inst, ok)
	}
	// The stray stored key cannot be restored; the carrying record can.
	f.ctl.auth.setCredential = func(string, string) error { return errors.New("restore refused") }

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"})
	if err == nil {
		t.Fatal("Remove = nil, want the failed reload reported")
	}
	if strings.Contains(err.Error(), "the removal stands") {
		t.Fatalf("Remove = %v, want a rollback: the carrying record is back", err)
	}
	if !strings.Contains(err.Error(), "was rolled back") {
		t.Fatalf("Remove = %v, want the rollback reported", err)
	}
	if !writeDidApply(err) {
		t.Fatalf("Remove = %v (%T), want an applied write: the stray key stayed deleted", err, err)
	}
	if _, statErr := os.Stat(authopenai.AuthFilePath(f.stateDir, "openai-codex")); statErr != nil {
		t.Fatalf("the OAuth record was not restored: %v", statErr)
	}
	if inst, ok := f.ctl.reg.Get().Instance("openai-codex"); !ok || inst.CredentialSource != "oauth" {
		t.Fatalf("after rollback openai-codex = %+v ok=%v, want it resolving again", inst, ok)
	}
}

// The inverse of the case above: the OAuth record is the layer that carries
// the instance and it cannot be restored (only the stray key can). The
// instance no longer resolves, so the removal stands and the caller is told
// the write applied.
func TestInstances_RemoveStandsWhenTheCarryingRecordCannotBeRestored(t *testing.T) {
	f := newFlakyReloadFixture(t, "openai-codex", func(load int) bool { return load == 3 })
	if err := authopenai.SaveAuth(f.stateDir, "openai-codex", makeOAuthRecord("openai-codex", "")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("priming reload: %v", err)
	}
	authPath := authopenai.AuthFilePath(f.stateDir, "openai-codex")
	originalDelete := f.ctl.auth.deleteAuth
	f.ctl.auth.deleteAuth = func(dir, name string) (bool, error) {
		removed, err := originalDelete(dir, name)
		if err == nil && removed {
			// Occupy the record's path so the atomic restore cannot land: the
			// stray key restores fine, the carrying record does not.
			if mkErr := os.Mkdir(authPath, 0o700); mkErr != nil {
				t.Errorf("Mkdir(%s): %v", authPath, mkErr)
			}
		}
		return removed, err
	}

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"})
	if err == nil {
		t.Fatal("Remove = nil, want the failed restore reported")
	}
	if !strings.Contains(err.Error(), "the removal stands") {
		t.Fatalf("Remove = %v, want the removal to stand: the carrying record is gone", err)
	}
	if !writeDidApply(err) {
		t.Fatalf("Remove = %v (%T), want an applied write: the carrying record stayed deleted", err, err)
	}
	// The layer that carried the instance is gone, so the removal stands: this
	// is one of the two supplies that must carry the discriminator.
	if _, applied := errors.AsType[removeAppliedError](err); !applied {
		t.Fatalf("Remove = %v (%T), want the standing-removal discriminator: the carrying record is gone", err, err)
	}
	if info, statErr := os.Stat(authPath); statErr != nil || !info.IsDir() {
		t.Fatalf("auth path = %v (err %v), want the record still not restored", info, statErr)
	}
}

// partialRollbackFixture serves an RPC hub whose implicit Codex instance can
// be given both an OAuth record and a stray stored key. failNext makes the
// next registry load fail and, in the same step, blocks the credentials file:
// the carrying OAuth record lives under the state root and restores, while the
// stray key cannot.
type partialRollbackFixture struct {
	hub       *httptest.Server
	reg       *hubcore.ProviderRegistry
	stateDir  string
	credsPath string
	store     *credentials.Store
	failNext  *atomic.Bool
}

func newPartialRollbackFixture(t *testing.T) *partialRollbackFixture {
	t.Helper()
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)
	credsStore := newTestCredentialsStore(t)
	load := testRegistryLoader(stateDir, tomlPath, credsStore, nil)
	failNext := &atomic.Bool{}
	reg := hubcore.NewProviderRegistry(func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		if failNext.CompareAndSwap(true, false) {
			// The store writes through <path>.tmp and renames, so a directory
			// occupying that name refuses the open. The mkdir result is
			// asserted from the test goroutine, not here.
			_ = os.Mkdir(credsStore.Path()+".tmp", 0o700)
			return nil, nil, errors.New("the registry refused to load")
		}
		return load(extra...)
	})
	if err := reg.Reload(); err != nil {
		t.Fatalf("registry: %v", err)
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            reg,
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        dir,
		CredsStore:          credsStore,
	})
	t.Cleanup(hub.Close)
	return &partialRollbackFixture{
		hub:       hub,
		reg:       reg,
		stateDir:  stateDir,
		credsPath: credsStore.Path(),
		store:     credsStore,
		failNext:  failNext,
	}
}

// A credential-only removal whose reload fails can restore the layer that
// carries the instance while a stray credential stays deleted. The instance is
// configured again - the caller is told the removal was rolled back, not that
// it stands - but a credential is gone, so the hub still owes every other
// client evener/auth/updated. This is the RPC-level proof that the applied
// marker rides the rollback error into instanceWrite's broadcast.
func TestHubRPCInstanceRemoveBroadcastsWhenAPartialRollbackLeavesACredentialDeleted(t *testing.T) {
	f := newPartialRollbackFixture(t)
	if err := f.store.Set("openai-codex", "sk-stray"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	if err := authopenai.SaveAuth(f.stateDir, "openai-codex", makeOAuthRecord("openai-codex", "")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	if err := f.reg.Reload(); err != nil {
		t.Fatalf("priming reload: %v", err)
	}
	if inst, ok := f.reg.Get().Instance("openai-codex"); !ok || inst.CredentialSource != "oauth" {
		t.Fatalf("openai-codex = %+v ok=%v, want an OAuth-backed instance", inst, ok)
	}

	client := dialHubRPC(t, f.hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	f.failNext.Store(true)

	var resp appwire.InstanceListResponse
	err := client.Request(context.Background(), appwire.MethodEvenerInstanceRemove,
		appwire.InstanceRemoveParams{Name: "openai-codex"}, &resp)
	if err == nil {
		t.Fatal("evener/instance/remove = nil, want the partial rollback reported")
	}
	if strings.Contains(err.Error(), "the removal stands") || !strings.Contains(err.Error(), "was rolled back") {
		t.Fatalf("evener/instance/remove = %v, want a rollback: the carrying record is back", err)
	}
	if !strings.Contains(err.Error(), "some credentials were not put back") {
		t.Fatalf("evener/instance/remove = %v, want the leftover stray key reported", err)
	}
	if st, statErr := os.Stat(f.credsPath + ".tmp"); statErr != nil || !st.IsDir() {
		t.Fatalf("credentials temp path = %v (err %v), want the blocking directory this case needs", st, statErr)
	}
	if v, _ := f.store.Get("openai-codex"); v != "" {
		t.Fatalf("stored key = %q, want the stray key still deleted", v)
	}
	if _, statErr := os.Stat(authopenai.AuthFilePath(f.stateDir, "openai-codex")); statErr != nil {
		t.Fatalf("the OAuth record was not restored: %v", statErr)
	}
	if inst, ok := f.reg.Get().Instance("openai-codex"); !ok || inst.CredentialSource != "oauth" {
		t.Fatalf("after the recovery reload openai-codex = %+v ok=%v, want it resolving again", inst, ok)
	}
	waitForAuthUpdatedBroadcast(t, client, "a partial rollback that left the stray key deleted")
}

// SetLayer persists the layer and then resolves the effective view. The file is
// written before the resolution runs, so a resolution that fails leaves every
// other client's launch config stale — the save applied.
func TestLaunch_SetLayerWhoseResolutionFailsIsStillApplied(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()
	ctl := &hubLaunchController{stateRoot: root, getenv: func(string) string { return "" }}
	original := hubLaunchResolve
	t.Cleanup(func() { hubLaunchResolve = original })
	hubLaunchResolve = func(string, string, launchconfig.Layer) (launchconfig.Resolved, error) {
		return launchconfig.Resolved{}, errors.New("the launch config could not be resolved")
	}

	_, err := ctl.SetLayer(context.Background(), appwire.LaunchConfigSetLayerParams{
		CWD:    cwd,
		Layer:  "global",
		Config: appwire.LaunchConfigLayer{},
	})
	if err == nil {
		t.Fatal("SetLayer = nil, want the failed resolution reported")
	}
	if !writeDidApply(err) {
		t.Fatalf("SetLayer = %v (%T), want an applied write: the layer is already on disk", err, err)
	}
}

// TrustRepo saves the trust decision to meta.toml and then resolves again to
// answer with the post-trust view. A failed resolution leaves the trust
// decision on disk, so it is applied however the call ends.
func TestLaunch_TrustRepoWhoseResolutionFailsIsStillApplied(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := canonicalTempDir(t)
	repoPath := filepath.Join(cwd, ".evener", "launch.toml")
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := []byte(`model = "from-repo"`)
	if err := os.WriteFile(repoPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	hash, err := launchconfig.CanonicalHashTOML(contents)
	if err != nil {
		t.Fatal(err)
	}

	c := newHubLaunchControllerWithEnv(stateRoot, func(string) string { return "" }, true)
	original := hubLaunchResolve
	t.Cleanup(func() { hubLaunchResolve = original })
	var calls int
	hubLaunchResolve = func(root, dir string, overrides launchconfig.Layer) (launchconfig.Resolved, error) {
		calls++
		if calls == 1 {
			// The pre-save check that reads the repo's current trust state
			// and hash must succeed so the save is reached.
			return original(root, dir, overrides)
		}
		return launchconfig.Resolved{}, errors.New("the launch config could not be resolved")
	}

	_, err = c.TrustRepo(context.Background(), appwire.LaunchConfigTrustRepoParams{CWD: cwd, Hash: hash})
	if err == nil {
		t.Fatal("TrustRepo = nil, want the failed resolution reported")
	}
	if !writeDidApply(err) {
		t.Fatalf("TrustRepo = %v (%T), want an applied write: the trust decision is already on disk", err, err)
	}
	paths, perr := launchconfig.PathsFor(stateRoot, cwd)
	if perr != nil {
		t.Fatalf("PathsFor: %v", perr)
	}
	meta, merr := launchconfig.LoadMeta(paths.Meta)
	if merr != nil {
		t.Fatalf("LoadMeta: %v", merr)
	}
	if meta.Trust.Decision != "trusted" || !launchconfig.HashInSet(hash, meta.Trust.Hashes) {
		t.Fatalf("trust decision = %#v, want %q trusted on disk", meta.Trust, hash)
	}
}

// SetDefault writes the new default and then reloads the registry. A failed
// reload leaves the new default on disk, so it is applied however the call ends.
func TestInstances_SetDefaultWhoseReloadFailsIsStillApplied(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	before, err := os.ReadFile(f.tomlPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// An entry that parses but cannot resolve an endpoint (#711): the registry
	// loaded before it appeared, so the write still lands, and the reload that
	// follows it fails.
	raw := append(append([]byte(nil), before...), []byte("\n[providers.standalone]\nprotocol = \"openai-chat\"\n")...)
	if err := os.WriteFile(f.tomlPath, raw, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	err = f.ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "work"})
	if err == nil {
		t.Fatal("SetDefault = nil, want the failed reload reported")
	}
	if !writeDidApply(err) {
		t.Fatalf("SetDefault = %v (%T), want an applied write: the default is already on disk", err, err)
	}
	after, _, err := registry.ReadConfigFile(f.tomlPath)
	if err != nil {
		t.Fatalf("re-reading providers.toml: %v", err)
	}
	if after.Default != "work" {
		t.Fatalf("providers.toml default = %q, want the write this call made to stand", after.Default)
	}
}

// The applied record belongs to the call that made the write, not to the
// controller: a mutation that lands a write and then fails reports the marker
// on its own error and leaves the controller's record clear, so a later call
// that writes nothing cannot read or inherit it. A record left on the
// controller for the RPC layer to read after the lock is released is exactly
// what let a concurrent call reset or steal the mark (roborev on PR #2042);
// this pins the per-call property that folding the mark onto the mutation's
// own error while its lock is held gives.
func TestInstances_AppliedMarkerBelongsToTheCallThatWrote(t *testing.T) {
	// Load 1 is the fixture's own, load 2 Create's reload, load 3 this
	// SetDefault's: SetDefault's write lands and the reload after it fails.
	f := newFlakyReloadFixture(t, "", func(load int) bool { return load == 3 })
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	err := f.ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "work"})
	if err == nil {
		t.Fatal("SetDefault = nil, want the failed reload reported")
	}
	if !writeDidApply(err) {
		t.Fatalf("SetDefault = %v (%T), want the applied marker on the call's own error", err, err)
	}
	if f.ctl.applied.peekApplied() {
		t.Fatal("SetDefault left its applied record on the controller: a later call could read or steal it")
	}
	// A call that writes nothing carries no marker and cannot inherit one.
	err = f.ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "missing"})
	if err == nil {
		t.Fatal("SetDefault(missing) = nil, want the refusal")
	}
	if writeDidApply(err) {
		t.Fatalf("SetDefault(missing) = %v (%T), want a plain refusal with no applied marker", err, err)
	}
}

// LoginComplete saves the OAuth record and then reads the instance's status.
// The record is on disk before the read, so a read that fails still leaves
// every other client's provider list stale.
func TestAuth_LoginCompleteWhoseStatusReadFailsIsStillApplied(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	ctrl := newHubAuthController()
	ctrl.stateDir = t.TempDir()
	attachTestRegistry(t, ctrl)
	ctrl.cfg = authopenai.Config{IssuerBaseURL: "https://auth.example.test"}
	ctrl.client = &http.Client{}
	ctrl.exchangeCode = func(_ context.Context, _ *http.Client, _ authopenai.Config, _ authopenai.TokenExchangeRequest) (authopenai.TokenSet, error) {
		return authopenai.TokenSet{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			IDToken:      hubAuthTestJWT(t, map[string]any{"email": "oauth@example.com"}),
			TokenType:    "Bearer",
			Scope:        "openid profile email",
			Expiry:       time.Now().Add(time.Hour),
		}, nil
	}
	start, err := ctrl.LoginStart(appwire.AuthLoginStartParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("LoginStart: %v", err)
	}
	state := mustAuthorizeState(t, start.URL)
	// The status read runs after the record is saved, and this is the read it
	// makes: a record that cannot be read at all (neither absent nor corrupt)
	// is the failure the status call passes on.
	saved := false
	realSave := ctrl.saveAuth
	ctrl.saveAuth = func(dir, name string, rec authopenai.AuthRecord) error {
		err := realSave(dir, name, rec)
		if err == nil {
			saved = true
		}
		return err
	}
	realLoad := ctrl.loadAuth
	ctrl.loadAuth = func(dir, name string) (authopenai.AuthRecord, error) {
		if saved {
			return authopenai.AuthRecord{}, errors.New("the auth record could not be read")
		}
		return realLoad(dir, name)
	}

	_, err = ctrl.LoginComplete(context.Background(), appwire.AuthLoginCompleteParams{
		Provider:    "openai-codex",
		FlowID:      start.FlowID,
		RedirectURL: "http://localhost:1455/auth/callback?code=auth-code&state=" + url.QueryEscape(state),
	})
	if err == nil {
		t.Fatal("LoginComplete = nil, want the failed status read reported")
	}
	if !writeDidApply(err) {
		t.Fatalf("LoginComplete = %v (%T), want an applied write: the record is already saved", err, err)
	}
	// Read through the real loader, so this is the state directory's answer and
	// not the failing hook's: without a saved record this would not be the
	// applied-then-failed case at all.
	if _, loadErr := realLoad(ctrl.stateDir, "openai-codex"); loadErr != nil {
		t.Fatalf("the OAuth record is not readable (%v), so this test is not the applied-then-failed case", loadErr)
	}
}

// mustAuthorizeState pulls the flow state out of an authorize URL.
func mustAuthorizeState(t *testing.T, authorizeURL string) string {
	t.Helper()
	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatalf("authorize URL %q carries no state", authorizeURL)
	}
	return state
}
