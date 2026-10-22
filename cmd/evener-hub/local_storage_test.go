package hub

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostlock"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/internal/hubendpoint"
)

func TestLocalStorageRefusalBeforeMutation(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	// The OS boundary alone is injected, not the storage policy or command flow.
	deps := defaultMainDeps()
	var inspected int
	deps.inspectFilesystem = func(file *os.File) (string, bool, error) {
		inspected++
		info, err := file.Stat()
		if err != nil || !info.IsDir() {
			t.Fatalf("not a real directory: %v", err)
		}
		return "fixture-network", true, nil
	}
	reached := errors.New("mutation boundary reached")
	deps.ensureDirs = func() error { return reached }
	for _, args := range [][]string{
		{"--config", path},
		{"endpoint", "--json", "--config", path},
		{"login-url", "--config", path},
		{"attach", "--stdio", "--config", path},
	} {
		t.Run(args[0], func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			deps.stdout = &stdout
			deps.stdin = bytes.NewReader(nil)
			err := runMain(args, &stderr, deps)
			var refusal *hostlock.StorageError
			if !errors.As(err, &refusal) || !errors.Is(err, hostlock.ErrNonlocalStorage) || refusal.Path != state || refusal.Filesystem != "fixture-network" {
				t.Fatalf("command returned %v, want selected-root storage refusal", err)
			}
			if errors.Is(err, errNoHubAt) || errors.Is(err, ErrLoginURLUnavailable) || stdout.Len() != 0 {
				t.Fatalf("storage refusal became absence or output: err=%v bytes=%d", err, stdout.Len())
			}
		})
	}
	if inspected != 4 {
		t.Fatalf("OS inspections=%d", inspected)
	}
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 0 {
		t.Fatalf("refusal mutated root: entries=%v err=%v", entries, err)
	}
}

func TestLocalStorageAbsentInspectionDoesNotCreate(t *testing.T) {
	root, _ := endpointTestRoot(t)
	state := filepath.Join(root, "absent", "fresh")
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	deps := defaultMainDeps()
	report, err := resolveHubEndpoint(context.Background(), endpointOptions{configPath: path, configExplicit: true}, deps)
	if err != nil || report.State != "stopped" || report.HubStateRoot != state {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "absent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection created ancestry: %v", err)
	}
	deps.inspectFilesystem = func(*os.File) (string, bool, error) { return "fixture-network", true, nil }
	var out bytes.Buffer
	if err := runEndpoint([]string{"--json", "--config", path}, &out, io.Discard, deps); !errors.Is(err, hostlock.ErrNonlocalStorage) || out.Len() != 0 {
		t.Fatalf("absent rejected root err=%v bytes=%d", err, out.Len())
	}
	if _, err := os.Lstat(filepath.Join(root, "absent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refusal created ancestry: %v", err)
	}
}

func TestLocalStoragePreservingMigration(t *testing.T) {
	root, old := endpointTestRoot(t)
	path := endpointTestConfig(t, root, old, "127.0.0.1:0")
	base, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pastPath := filepath.Join(root, "independent", "index.db")
	base = bytes.Replace(base, []byte(strconv.Quote(filepath.Join(old, "index.db"))), []byte(strconv.Quote(pastPath)), 1)
	if err := os.WriteFile(path, base, 0600); err != nil {
		t.Fatal(err)
	}
	beforeConfig, err := LoadConfigExplicit(path)
	if err != nil {
		t.Fatal(err)
	}
	// A config sibling and independent project/run files are not hub-root data.
	projectPath := hubtest.ProjectDir(t, filepath.Join(root, "projects"), "preserved")
	unchanged := map[string][]byte{path + ".private-backup": []byte("retained-config-family"), filepath.Join(projectPath, "marker"): []byte("project-state"), filepath.Join(root, "run", "marker"): []byte("runtime-state")}
	for name, data := range unchanged {
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	const token = "fixture-retained-auth-bearer"
	if err := os.WriteFile(filepath.Join(old, "auth-token"), []byte(token+"\n"), 0400); err != nil {
		t.Fatal(err)
	}
	sessionID := hubtest.SessionID(t)
	projectID := filepath.Base(hubtest.ProjectDir(t, filepath.Join(root, "deleted-projects"), "deleted"))
	target := hubcore.DeletionTarget{Ref: "local:" + sessionID, ThreadID: sessionID}
	fences, err := hubcore.NewDeletionStore(old)
	if err != nil {
		t.Fatal(err)
	}
	deletion, err := fences.Begin(projectID, []hubcore.DeletionTarget{target})
	if err != nil {
		t.Fatal(err)
	}
	if err := fences.MarkDeleted(projectID, deletion.Generation); err != nil {
		t.Fatal(err)
	}
	settings, err := hubcore.NewTranscriptDisplayStore(old)
	if err != nil {
		t.Fatal(err)
	}
	preference := appwire.TranscriptDisplayShippedDefaults().Desktop.Config
	preference.Content.Level = appwire.TranscriptLevelActivity
	if _, err := settings.Patch(appwire.TranscriptDisplayDefaultsPatchParams{Layout: appwire.TranscriptViewportDesktop, Config: preference}); err != nil {
		t.Fatal(err)
	}
	wantSettings := settings.Snapshot()
	favorite := hubcore.NewFavoriteStore(pastPath)
	if err := favorite.Set("local", "session", sessionID, true, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	wantFavorites, err := favorite.Favorites()
	if err != nil || len(wantFavorites) != 1 {
		t.Fatalf("seed favorites: count=%d err=%v", len(wantFavorites), err)
	}
	// Retain stale source coordination, but never copy it into the new root.
	for _, name := range []string{"hub.lock", "hub-endpoint.json"} {
		if err := os.WriteFile(filepath.Join(old, name), []byte("stale-source-coordination"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := storageFixtureSnapshot(t, old)
	refused := defaultMainDeps()
	refused.inspectFilesystem = func(*os.File) (string, bool, error) { return "fixture-network", true, nil }
	refused.ensureDirs = func() error { return errors.New("unexpected mutation boundary") }
	// Only an external OS classification is false here, all state is real.
	if err := runMain([]string{"--config", path}, io.Discard, refused); !errors.Is(err, hostlock.ErrNonlocalStorage) {
		t.Fatalf("old root not refused: %v", err)
	}
	if !reflect.DeepEqual(before, storageFixtureSnapshot(t, old)) {
		t.Fatal("refusal changed source bytes or modes")
	}

	local := filepath.Join(root, "local-hub-state")
	storageFixtureCopy(t, old, local)
	copied := storageFixtureSnapshot(t, local)
	for name, want := range before {
		if name == "hub.lock" || name == "hub-endpoint.json" {
			if _, ok := copied[name]; ok {
				t.Fatalf("copied coordination: %s", name)
			}
			continue
		}
		if !reflect.DeepEqual(copied[name], want) {
			t.Fatalf("copied bytes/mode differ: %s", name)
		}
	}
	// Deliberately edit just this one field in the same config document.
	selected := bytes.Replace(base, []byte("hub_state_root = "+strconv.Quote(old)), []byte("hub_state_root = "+strconv.Quote(local)), 1)
	if bytes.Equal(selected, base) {
		t.Fatal("root key not changed")
	}
	if err := os.WriteFile(path, selected, 0600); err != nil {
		t.Fatal(err)
	}
	afterConfig, err := LoadConfigExplicit(path)
	if err != nil {
		t.Fatal(err)
	}
	wantConfig := beforeConfig
	wantConfig.HubStateRoot = local
	if !reflect.DeepEqual(afterConfig, wantConfig) {
		t.Fatal("root edit changed independent configuration")
	}
	if report := endpointCommand(t, path); report.State != "stopped" || report.HubStateRoot != local {
		t.Fatalf("before launch: %+v", report)
	}
	deps := defaultMainDeps() // real auth loading, singleton lock, stores and listener
	deps.loadRegistry = hermeticRegistryLoader
	deps.startLivePrefetch = func(context.Context, *hubcore.ProviderRegistry, *hubAuthController, func(func()), func()) {}
	deps.startLaunchPrefetch = func(context.Context, *WebServer, func(func())) {}
	report, stop := startPublicationHubWithDeps(t, path, io.Discard, deps)
	defer stop()
	if report.Running.HubStateRoot != local || report.Running.ConfigPath != path || report.Running.BoundAddr == "127.0.0.1:0" || report.Running.BootID != hubBootID {
		t.Fatalf("fresh publication: %+v", report)
	}
	if _, err := hubendpoint.Read(local); err != nil {
		t.Fatal(err)
	}
	// The old credential, not a fresh generated bearer, must authorize real RPC.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	origin, err := hubendpoint.ParseOrigin(report.Running.LocalURL)
	if err != nil {
		t.Fatal(err)
	}
	clientHTTP, err := hubendpoint.HTTPClientWithBearer(nil, token, origin, report.Running)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := appwire.DialWebSocketWithHeaders(ctx, "ws"+strings.TrimPrefix(origin.String(), "http")+"/rpc", clientHTTP, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := appwire.NewClient(transport)
	client.Start(ctx)
	defer client.Close()
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	var gotSettings appwire.TranscriptDisplayDefaults
	if err := client.Request(ctx, appwire.MethodEvenerSettingsTranscriptDisplayGet, appwire.EmptyParams{}, &gotSettings); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotSettings, wantSettings) {
		t.Fatal("authenticated RPC lost saved preference/revision")
	}
	response, err := http.Get(origin.String() + "/rpc")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated access=%d", response.StatusCode)
	}
	var link bytes.Buffer
	if err := runLoginURL([]string{"--config", path}, &link, io.Discard, defaultMainDeps()); err != nil {
		t.Fatal(err)
	}
	if link.String() != origin.String()+"/auth/"+token+"\n" {
		t.Fatal("login recovery changed credential or origin")
	}
	reloaded, err := hubcore.NewDeletionStore(local)
	if err != nil {
		t.Fatal(err)
	}
	if state, ok := reloaded.TargetState(target.Ref, target.ThreadID); !ok || state != hubcore.DeletionStateDeleted {
		t.Fatalf("deletion fence lost: %s %v", state, ok)
	}
	gotFavorites, err := hubcore.NewFavoriteStore(pastPath).Favorites()
	if err != nil || !reflect.DeepEqual(gotFavorites, wantFavorites) {
		t.Fatalf("independent past-index preferences changed: %v", err)
	}
	for name, want := range unchanged {
		got, err := os.ReadFile(name)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("independent path changed: %s err=%v", name, err)
		}
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, selected) {
		t.Fatalf("startup rewrote config: %v", err)
	}
	destination := storageFixtureSnapshot(t, local)
	for _, name := range []string{"auth-token", "deletions/state.json", "transcript-display/state.json"} {
		if !reflect.DeepEqual(destination[name], before[name]) {
			t.Fatalf("destination durable file changed: %s", name)
		}
	}
	if !reflect.DeepEqual(before, storageFixtureSnapshot(t, old)) {
		t.Fatal("source changed during destination startup/authentication")
	}
}

type storageFixtureEntry struct {
	Mode fs.FileMode
	Data []byte
}

func storageFixtureSnapshot(t *testing.T, root string) map[string]storageFixtureEntry {
	t.Helper()
	result := make(map[string]storageFixtureEntry)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		value := storageFixtureEntry{Mode: info.Mode()}
		if !entry.IsDir() {
			value.Data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		result[name] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// Copy only fixture-owned hub entries, into a new private destination. The
// accepted hostops relocation proof separately exercises completed custody.
func storageFixtureCopy(t *testing.T, source, destination string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if name == "hub.lock" || name == "hub-endpoint.json" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		target := filepath.Join(destination, name)
		if entry.IsDir() {
			return os.Mkdir(target, info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, writeErr := file.Write(data)
		closeErr := file.Close()
		return errors.Join(writeErr, closeErr)
	})
	if err != nil {
		t.Fatal(err)
	}
}
