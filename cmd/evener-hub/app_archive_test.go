package hub

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
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
		Archive:      store,
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
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
