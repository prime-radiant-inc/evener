package hub

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
)

func TestHubArchiveSetAppWirePersistsSessionDecision(t *testing.T) {
	store := hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db"))
	var attentionPokes int
	web := NewWebServer(hubcore.WebConfig{
		Archive:       store,
		HubStateRoot:  t.TempDir(),
		Past:          hubcore.NewPastIndex(""),
		PokeAttention: func() { attentionPokes++ },
	})

	response, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:     appwire.ArchiveTargetSession,
		ID:       "session-1",
		Archived: true,
	})
	if err != nil {
		t.Fatalf("dispatch archive set: %v", err)
	}
	if !response.OK {
		t.Fatalf("response = %+v, want success", response)
	}
	if attentionPokes != 1 {
		t.Fatalf("attention pokes = %d, want 1", attentionPokes)
	}
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if !decisions[hubcore.ArchiveKey{Kind: "session", ID: "session-1"}] {
		t.Fatalf("session archive decision not persisted: %v", decisions)
	}
}

func TestHubArchiveSetAppWireValidatesAndUnarchivesProject(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	store := hubcore.NewArchiveStore(filepath.Join(root, "archive.db"))
	web := NewWebServer(hubcore.WebConfig{
		Archive:      store,
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
	})
	params := appwire.ArchiveParams{
		Kind:       appwire.ArchiveTargetProject,
		ID:         project.ID,
		WorkingDir: project.CanonicalPath,
		Archived:   true,
	}
	if _, err := dispatchArchiveSet(t, web, params); err != nil {
		t.Fatalf("archive project: %v", err)
	}
	params.Archived = false
	if _, err := dispatchArchiveSet(t, web, params); err != nil {
		t.Fatalf("unarchive project: %v", err)
	}

	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if decisions[hubcore.ArchiveKey{Kind: "project", ID: project.ID}] {
		t.Fatalf("project decision remains archived: %v", decisions)
	}
}

func TestHubArchiveSetAppWireRejectsProjectPathMismatch(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{
		Archive:      hubcore.NewArchiveStore(filepath.Join(root, "archive.db")),
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
	})

	_, err = dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:       appwire.ArchiveTargetProject,
		ID:         project.ID,
		WorkingDir: filepath.Join(root, "different"),
		Archived:   true,
	})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("error = %v, want AppWire error", err)
	}
	if wireErr.Code != appwire.CodeInvalidParams {
		t.Fatalf("error code = %d, want %d (%v)", wireErr.Code, appwire.CodeInvalidParams, wireErr)
	}
}

func dispatchArchiveSet(t *testing.T, web *WebServer, params appwire.ArchiveParams) (appwire.ArchiveResponse, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal archive params: %v", err)
	}
	result, err := web.appRPC.Router().Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodEvenerArchiveSet,
		Params: raw,
	})
	if err != nil {
		return appwire.ArchiveResponse{}, err
	}
	response, ok := result.(appwire.ArchiveResponse)
	if !ok {
		t.Fatalf("response type = %T, want appwire.ArchiveResponse", result)
	}
	return response, nil
}

// A remote project's working directory does not exist on the controller, so the
// archive must not resolve it against the controller's filesystem. Each host's
// decision is keyed by its own source, distinct from the controller key.
func TestHubArchiveSetAppWireKeysNonLocalProjectBySource(t *testing.T) {
	store := hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db"))
	web := NewWebServer(hubcore.WebConfig{
		Archive:          store,
		HubStateRoot:     t.TempDir(),
		Past:             hubcore.NewPastIndex(""),
		RemoteHosts:      []hostreg.Host{{Name: "host-a"}, {Name: "host-b"}},
		RemoteHostClient: unusedRemoteHostClient,
	})
	projectID := identifier.ProjectFromCanonicalPath("/srv/remote/project").ID

	for _, host := range []string{"host-a", "host-b"} {
		if _, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
			Kind:     appwire.ArchiveTargetProject,
			ID:       projectID,
			Source:   host,
			Archived: true,
		}); err != nil {
			t.Fatalf("archive remote project on %s: %v", host, err)
		}
	}

	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"host-a", "host-b"} {
		if !decisions[hubcore.ArchiveKey{Kind: "project", ID: projectID, Source: host}] {
			t.Fatalf("decision for %s not persisted under its own source: %v", host, decisions)
		}
	}
	if decisions[hubcore.ArchiveKey{Kind: "project", ID: projectID}] {
		t.Fatalf("remote decision leaked onto the controller key: %v", decisions)
	}
}

// The optional non-local WorkingDir is cross-checked against the identity the
// host already reported, never resolved locally: a mismatched path is rejected
// with the same message the local path uses.
func TestHubArchiveSetAppWireCrossChecksRemoteWorkingDir(t *testing.T) {
	cache := &hubcore.RemoteThreadCache{}
	projectID := identifier.ProjectFromCanonicalPath("/srv/remote/project").ID
	cache.StoreSnapshotData(hubcore.RemoteThreadSnapshot{
		Threads: []appwire.Thread{{
			ID:          "t1",
			SessionID:   "t1",
			Source:      "host-a",
			ProjectID:   projectID,
			ProjectPath: "/srv/remote/project",
		}},
	})
	web := NewWebServer(hubcore.WebConfig{
		Archive:           hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db")),
		RemoteThreadCache: cache,
		HubStateRoot:      t.TempDir(),
		Past:              hubcore.NewPastIndex(""),
		RemoteHosts:       []hostreg.Host{{Name: "host-a"}},
		RemoteHostClient:  unusedRemoteHostClient,
	})

	if _, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:       appwire.ArchiveTargetProject,
		ID:         projectID,
		Source:     "host-a",
		WorkingDir: "/srv/remote/project",
		Archived:   true,
	}); err != nil {
		t.Fatalf("archive remote project with the reported path: %v", err)
	}

	_, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:       appwire.ArchiveTargetProject,
		ID:         projectID,
		Source:     "host-a",
		WorkingDir: "/srv/elsewhere",
		Archived:   true,
	})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams {
		t.Fatalf("error = %v, want InvalidParams for a mismatched remote workingDir", err)
	}
	if !strings.Contains(err.Error(), "project ID does not match workingDir") {
		t.Fatalf("error = %q, want the project/workingDir mismatch message", err)
	}
}

// A non-local archive source must name a configured host. An unknown source
// would persist a successful but permanently inert decision row that no read
// path addresses.
func TestHubArchiveSetAppWireRejectsUnknownSource(t *testing.T) {
	store := hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db"))
	web := NewWebServer(hubcore.WebConfig{
		Archive:          store,
		HubStateRoot:     t.TempDir(),
		Past:             hubcore.NewPastIndex(""),
		RemoteHosts:      []hostreg.Host{{Name: "host-a"}},
		RemoteHostClient: unusedRemoteHostClient,
	})
	projectID := identifier.ProjectFromCanonicalPath("/srv/remote/project").ID

	_, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:     appwire.ArchiveTargetProject,
		ID:       projectID,
		Source:   "host-b",
		Archived: true,
	})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams {
		t.Fatalf("error = %v, want InvalidParams for an unknown source", err)
	}
	if !strings.Contains(err.Error(), "unknown source") {
		t.Fatalf("error = %q, want the unknown-source message", err)
	}
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 0 {
		t.Fatalf("unknown source wrote a decision: %v", decisions)
	}

	// A whitespace variant still names the configured host: normalization trims
	// it before validation and keying.
	if _, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:     appwire.ArchiveTargetProject,
		ID:       projectID,
		Source:   "  host-a  ",
		Archived: true,
	}); err != nil {
		t.Fatalf("archive with a padded configured source: %v", err)
	}
	decisions, err = store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if !decisions[hubcore.ArchiveKey{Kind: "project", ID: projectID, Source: "host-a"}] {
		t.Fatalf("padded source did not normalize to host-a: %v", decisions)
	}
}

// A configured host is only a registered source when the hub wired a remote
// client: newHubSourceRegistry skips cfg.RemoteHosts entirely when
// RemoteHostClient is nil, so accepting its name here would persist a decision
// no source, and no navigation row, can ever address. The same request against a
// wired hub succeeds.
func TestHubArchiveSetRejectsConfiguredHostWithoutRemoteClient(t *testing.T) {
	store := hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db"))
	projectID := identifier.ProjectFromCanonicalPath("/srv/remote/project").ID
	params := appwire.ArchiveParams{
		Kind:     appwire.ArchiveTargetProject,
		ID:       projectID,
		Source:   "host-a",
		Archived: true,
	}

	unwired := NewWebServer(hubcore.WebConfig{
		Archive:      store,
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
		RemoteHosts:  []hostreg.Host{{Name: "host-a"}},
	})
	_, err := dispatchArchiveSet(t, unwired, params)
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams || !strings.Contains(err.Error(), "unknown source") {
		t.Fatalf("error = %v, want InvalidParams for an unregistered source", err)
	}

	wired := NewWebServer(hubcore.WebConfig{
		Archive:          store,
		HubStateRoot:     t.TempDir(),
		Past:             hubcore.NewPastIndex(""),
		RemoteHosts:      []hostreg.Host{{Name: "host-a"}},
		RemoteHostClient: unusedRemoteHostClient,
	})
	if _, err := dispatchArchiveSet(t, wired, params); err != nil {
		t.Fatalf("archive against a wired hub: %v", err)
	}
}

// A session archive carries no source dimension: the session ID is already its
// host-qualified ref, so a non-local source must be rejected instead of
// silently writing an inert controller-key row.
func TestHubArchiveSetAppWireRejectsSessionSource(t *testing.T) {
	store := hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db"))
	web := NewWebServer(hubcore.WebConfig{
		Archive:      store,
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
		RemoteHosts:  []hostreg.Host{{Name: "host-a"}},
	})
	_, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:     appwire.ArchiveTargetSession,
		ID:       "session-1",
		Source:   "host-a",
		Archived: true,
	})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams {
		t.Fatalf("error = %v, want InvalidParams for a session source", err)
	}
	if !strings.Contains(err.Error(), "source is not supported for session archive") {
		t.Fatalf("error = %q, want the session-source message", err)
	}
}

// A "local:thread" ref addresses the controller's own row. Both spellings must
// land on the single key the local rows and every stored local decision use, so
// a client that sends the canonical ref neither misses the row nor splits the
// decision into a second inert key.
func TestHubArchiveSetAppWireNormalizesLocalSessionRef(t *testing.T) {
	store := hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db"))
	web := NewWebServer(hubcore.WebConfig{
		Archive:      store,
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
	})

	if _, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:     appwire.ArchiveTargetSession,
		ID:       "  local:session-1  ",
		Archived: true,
	}); err != nil {
		t.Fatalf("archive local session by ref: %v", err)
	}
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if !decisions[hubcore.ArchiveKey{Kind: "session", ID: "session-1"}] {
		t.Fatalf("local ref did not normalize onto the bare session key: %v", decisions)
	}
	if decisions[hubcore.ArchiveKey{Kind: "session", ID: "local:session-1"}] {
		t.Fatalf("local ref was stored under a spelling no local row is read by: %v", decisions)
	}

	// The bare spelling unarchives the row the ref spelling archived.
	if _, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:     appwire.ArchiveTargetSession,
		ID:       "session-1",
		Archived: false,
	}); err != nil {
		t.Fatalf("unarchive bare session id: %v", err)
	}
	decisions, err = store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if decisions[hubcore.ArchiveKey{Kind: "session", ID: "session-1"}] {
		t.Fatalf("bare-ID unarchive did not clear the decision the ref set: %v", decisions)
	}
}

// A remote session's tree row carries its canonical ref as the node ID, and the
// Live tier filters on exactly that identity. Archiving the ref must therefore
// clear the host's row: the bare session ID the rail sends today is the local
// identity and reaches no remote row.
func TestHubArchiveSetAppWireArchivesRemoteSessionByRef(t *testing.T) {
	cache := &hubcore.RemoteThreadCache{}
	projectID := identifier.ProjectFromCanonicalPath("/srv/remote/project").ID
	// A current timestamp keeps the row in the Current tier: an age-based
	// auto-archive would clear it from the Live tier for reasons of its own.
	now := time.Now().Unix()
	cache.StoreSnapshotData(hubcore.RemoteThreadSnapshot{
		Threads: []appwire.Thread{{
			ID:          "t1",
			SessionID:   "t1",
			Source:      "host-a",
			CWD:         "/srv/remote/project",
			ProjectID:   projectID,
			ProjectPath: "/srv/remote/project",
			CreatedAt:   now,
			UpdatedAt:   now,
			Status:      appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener:      appwire.EvenerThread{Ref: "host-a:t1"},
		}},
	})
	store := hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db"))
	web := NewWebServer(hubcore.WebConfig{
		Archive:           store,
		RemoteThreadCache: cache,
		HubStateRoot:      t.TempDir(),
		Past:              hubcore.NewPastIndex(""),
		RemoteHosts:       []hostreg.Host{{Name: "host-a"}},
		RemoteHostClient:  unusedRemoteHostClient,
	})
	liveTree := func(t *testing.T) hubcore.Tree {
		t.Helper()
		metas, live, projects := web.navigationTreeInputs(context.Background())
		decisions, err := store.Decisions()
		if err != nil {
			t.Fatal(err)
		}
		return hubcore.BuildTreeWithProjects(metas, live, decisions, projects)
	}

	if live := liveTree(t).Live; len(live) != 1 || live[0].ID != "host-a:t1" {
		t.Fatalf("live = %#v, want host-a's remote row before the archive", live)
	}
	if _, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:     appwire.ArchiveTargetSession,
		ID:       "host-a:t1",
		Archived: true,
	}); err != nil {
		t.Fatalf("archive remote session by ref: %v", err)
	}
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if !decisions[hubcore.ArchiveKey{Kind: "session", ID: "host-a:t1"}] {
		t.Fatalf("remote session decision not keyed by its ref: %v", decisions)
	}
	if live := liveTree(t).Live; len(live) != 0 {
		t.Fatalf("live = %#v, want the archived remote row cleared from the Live tier", live)
	}
}
