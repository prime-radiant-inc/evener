package hub

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
)

// rootSignalHub is a hub over a disk-backed past index holding one root and one
// subagent of it, wired to navigation the way runMain wires it.
type rootSignalHub struct {
	t        *testing.T
	web      *WebServer
	past     *hubcore.PastIndex
	inputs   *hubcore.InputsVersion
	project  identifier.Project
	stateDir string
	rootID   string
	subID    string
	base     time.Time
}

func newRootSignalHub(t *testing.T) *rootSignalHub {
	t.Helper()
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(work)
	if err != nil {
		t.Fatal(err)
	}
	h := &rootSignalHub{
		t:        t,
		project:  project,
		stateDir: filepath.Join(root, "projects", project.ID),
		rootID:   projectDeleteCanonicalSessionIDs[0],
		subID:    projectDeleteCanonicalSessionIDs[1],
		base:     time.Unix(1_700_000_000, 0).UTC(),
		inputs:   &hubcore.InputsVersion{},
	}
	h.save(h.rootID, "root", h.base, "")
	h.save(h.subID, "sub", h.base, h.rootID)
	h.past = hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := h.past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	h.web = NewWebServer(hubcore.WebConfig{Past: h.past, Roster: hubcore.NewRosterWithEntries(), Inputs: h.inputs})
	wirePastNavigation(h.past, h.inputs.Bump, h.web.navigation)
	h.read() // prime the retained snapshot
	return h
}

// save writes a session meta; a non-empty parent makes it a subagent of it.
func (h *rootSignalHub) save(id, name string, updatedAt time.Time, parent string) {
	h.t.Helper()
	meta := schema.SessionMeta{ID: id, Name: name, UpdatedAt: updatedAt, EnvInfo: schema.EnvironmentInfo{WorkingDir: h.project.CanonicalPath}}
	if parent != "" {
		meta.ParentSessionID, meta.JobTreeRootSessionID, meta.IsSubagent = parent, parent, true
	}
	if err := schema.SaveSessionMeta(h.stateDir, meta); err != nil {
		h.t.Fatal(err)
	}
}

// read serves the manifest, which rebuilds navigation when it is stale.
func (h *rootSignalHub) read() {
	h.t.Helper()
	if _, err := h.web.navigation.readV3(h.t.Context(), navigationResourceKey{Kind: navigationResourceManifest}, nil); err != nil {
		h.t.Fatal(err)
	}
}

func (h *rootSignalHub) builds() uint64 { return h.web.navigation.Stats().CoreBuilds }

func (h *rootSignalHub) locationStatus(id string) string {
	h.t.Helper()
	response, err := h.web.navigation.readV3(h.t.Context(), navigationResourceKey{Kind: navigationResourceLocation, ID: "local:" + id}, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	return response.Response.Status
}

func TestNavigationRootSignal_SubagentAutosaveDoesNotRebuild(t *testing.T) {
	h := newRootSignalHub(t)
	builds, inputs := h.builds(), h.inputs.Load()

	h.save(h.subID, "sub", h.base.Add(time.Minute), h.rootID) // an autosave
	h.past.RefreshOne(h.subID)
	h.save(h.subID, "sub retitled", h.base.Add(2*time.Minute), h.rootID)
	h.past.RefreshOne(h.subID)
	h.read()

	if got := h.builds(); got != builds {
		t.Fatalf("navigation core builds = %d after subagent writes, want %d", got, builds)
	}
	if got := h.inputs.Load(); got != inputs {
		t.Fatalf("inputs version moved from %d to %d on subagent-only writes", inputs, got)
	}
}

func TestNavigationRootSignal_RootRenameRebuilds(t *testing.T) {
	h := newRootSignalHub(t)
	builds, inputs := h.builds(), h.inputs.Load()

	h.save(h.rootID, "renamed", h.base.Add(time.Minute), "")
	h.past.RefreshOne(h.rootID)
	h.read()

	if got := h.builds(); got != builds+1 {
		t.Fatalf("navigation core builds = %d after a root rename, want %d", got, builds+1)
	}
	if h.inputs.Load() == inputs {
		t.Fatal("inputs version did not move on a root rename")
	}
}

func TestNavigationRootSignal_SubagentAddedOrRemovedRebuilds(t *testing.T) {
	h := newRootSignalHub(t)
	otherID := mustSessionID(t)
	builds := h.builds()

	h.save(otherID, "other", h.base, h.rootID)
	if _, err := h.past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	h.read()
	if got := h.builds(); got != builds+1 {
		t.Fatalf("navigation core builds = %d after a subagent was added, want %d", got, builds+1)
	}

	if err := os.Remove(filepath.Join(h.stateDir, "sessions", otherID+".meta.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	h.read()
	if got := h.builds(); got != builds+2 {
		t.Fatalf("navigation core builds = %d after a subagent was removed, want %d", got, builds+2)
	}
}

// Deleting a subagent session removes its meta and rebuilds the index, which
// must notify navigation so the subagent's alias location goes gone.
func TestSessionDelete_SubagentInvalidatesNavigation(t *testing.T) {
	h := newRootSignalHub(t)
	if got := h.locationStatus(h.subID); got == "gone" {
		t.Fatalf("subagent location = %q before delete, want it aliased to its root", got)
	}
	builds := h.builds()

	if _, err := dispatchSessionDelete(t, h.web, appwire.SessionDeleteParams{Ref: "local:" + h.subID}); err != nil {
		t.Fatalf("delete subagent session: %v", err)
	}
	h.read()

	if got := h.builds(); got == builds {
		t.Fatal("deleting a subagent session did not rebuild navigation")
	}
	if got := h.locationStatus(h.subID); got != "gone" {
		t.Fatalf("subagent location = %q after delete, want gone", got)
	}
}

func TestProjectDelete_SubagentsInvalidateNavigation(t *testing.T) {
	h := newRootSignalHub(t)
	h.web.navigation.DrainPublications()

	if _, err := dispatchProjectDelete(t, h.web, appwire.ProjectDeleteParams{Key: h.project.ID, WorkingDir: h.project.CanonicalPath}); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	if len(h.web.navigation.DrainPublications()) == 0 {
		t.Fatal("deleting a project with a subagent emitted no navigation invalidation")
	}
	h.read()
	if got := h.locationStatus(h.subID); got != "gone" {
		t.Fatalf("subagent location = %q after its project was deleted, want gone", got)
	}
}
