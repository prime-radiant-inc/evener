package hub

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
)

// reachFixture is two projects on disk. Project A holds a root, a subagent that
// ran from project B's directory, and an orphan subagent (its parent is gone)
// that ran from A's directory. Project B holds an unrelated root with its own
// subagent.
type reachFixture struct {
	root         string
	a, b         identifier.Project
	stateA       string
	stateB       string
	rootA        string
	roamingChild string // subagent of rootA whose working dir is project B
	orphanA      string // subagent in A's directory whose parent is not indexed
	rootB        string
	childB       string // subagent of rootB
	web          *WebServer
	dbPath       string
	pins         *hubcore.PinSectionStore
	archive      *hubcore.ArchiveStore
	favorite     *hubcore.FavoriteStore
}

func newReachFixture(t *testing.T) reachFixture {
	t.Helper()
	f := reachFixture{root: t.TempDir()}
	resolve := func(name string) identifier.Project {
		dir := filepath.Join(f.root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		project, err := identifier.ResolveProject(dir)
		if err != nil {
			t.Fatal(err)
		}
		return project
	}
	f.a, f.b = resolve("a"), resolve("b")
	f.stateA = filepath.Join(f.root, "projects", f.a.ID)
	f.stateB = filepath.Join(f.root, "projects", f.b.ID)
	newID := func() string {
		id, err := identifier.NewSessionID()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	f.rootA, f.roamingChild, f.orphanA, f.rootB, f.childB = newID(), newID(), newID(), newID(), newID()
	subagent := func(stateDir, id, parent, dir string) {
		writeSession(t, stateDir, id, dir)
		meta, err := schema.LoadSessionMeta(stateDir, id)
		if err != nil {
			t.Fatal(err)
		}
		meta.ParentSessionID, meta.JobTreeRootSessionID, meta.IsSubagent = parent, parent, true
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatal(err)
		}
	}
	writeSession(t, f.stateA, f.rootA, f.a.CanonicalPath)
	subagent(f.stateB, f.roamingChild, f.rootA, f.b.CanonicalPath)
	subagent(f.stateA, f.orphanA, newID(), f.a.CanonicalPath)
	writeSession(t, f.stateB, f.rootB, f.b.CanonicalPath)
	subagent(f.stateB, f.childB, f.rootB, f.b.CanonicalPath)

	f.dbPath = filepath.Join(f.root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(f.root, "projects", "*"), f.dbPath)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	f.archive = hubcore.NewArchiveStore(f.dbPath)
	f.favorite = hubcore.NewFavoriteStore(f.dbPath)
	f.pins = hubcore.NewPinSectionStore(f.dbPath)
	f.web = NewWebServer(hubcore.WebConfig{
		HubStateRoot: f.root, StateDir: f.root, Past: past, Archive: f.archive,
		Favorite: f.favorite, PinSections: f.pins, Roster: hubcore.NewRosterWithEntries(),
	})
	return f
}

func (f reachFixture) metaExists(stateDir, id string) bool {
	_, err := os.Stat(filepath.Join(stateDir, "sessions", id+".meta.json"))
	return err == nil
}

func (f reachFixture) deleteProjectA(t *testing.T) appwire.ProjectDeleteResponse {
	t.Helper()
	resp, err := dispatchProjectDelete(t, f.web, appwire.ProjectDeleteParams{Key: f.a.ID, WorkingDir: f.a.CanonicalPath})
	if err != nil {
		t.Fatalf("project delete: %v", err)
	}
	return resp
}

func TestProjectDeleteReachesSubagentFromAnotherDirectory(t *testing.T) {
	f := newReachFixture(t)
	f.deleteProjectA(t)
	if f.metaExists(f.stateB, f.roamingChild) {
		t.Fatal("subagent that ran from another directory was leaked by its project's delete")
	}
	if f.metaExists(f.stateA, f.rootA) {
		t.Fatal("project root survived")
	}
}

func TestProjectDeleteKeepsOrphanSubagentInProjectDirectory(t *testing.T) {
	f := newReachFixture(t)
	f.deleteProjectA(t)
	if f.metaExists(f.stateA, f.orphanA) {
		t.Fatal("orphan subagent in the project directory survived")
	}
}

func TestProjectDeleteLeavesOtherProjectsRootAndSubagent(t *testing.T) {
	f := newReachFixture(t)
	f.deleteProjectA(t)
	for _, id := range []string{f.rootB, f.childB} {
		if !f.metaExists(f.stateB, id) {
			t.Fatalf("session %s of another project was deleted", id)
		}
	}
}

func TestProjectDeleteReportsExactlyTheReachSet(t *testing.T) {
	f := newReachFixture(t)
	resp := f.deleteProjectA(t)
	want := []string{f.rootA, f.roamingChild, f.orphanA}
	got := append([]string(nil), resp.Deleted...)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("deleted = %v, want %v", got, want)
	}
}

func TestProjectDeleteScrubsDecisionsOfReachedSubagent(t *testing.T) {
	f := newReachFixture(t)
	now := timeNowForTest()
	for _, id := range []string{f.roamingChild, f.childB} {
		if err := f.archive.Set("", "session", id, true, now); err != nil {
			t.Fatal(err)
		}
		if err := f.favorite.Set("", "session", id, true, now); err != nil {
			t.Fatal(err)
		}
	}
	f.deleteProjectA(t)
	assertArchiveDecisionAbsent(t, f.archive, "session", f.roamingChild)
	assertProjectDeleteDecisionAbsent(t, f.dbPath, "session", f.roamingChild)
	assertArchiveDecisionPresent(t, f.archive, "session", f.childB, true)
	assertProjectDeleteDecisionPresent(t, f.dbPath, "session", f.childB, true)
}

// A delete interrupted after fencing must resume against the state directory
// each target lives in, including a reached subagent that lives in another
// project's directory.
func TestProjectDeleteResumeFindsReachedSubagentInAnotherProjectDirectory(t *testing.T) {
	f := newReachFixture(t)
	oldRemove := removeProjectSessionFile
	t.Cleanup(func() { removeProjectSessionFile = oldRemove })
	removeProjectSessionFile = func(path string) error {
		if filepath.Base(path) == f.roamingChild+".future-artifact" {
			return errors.New("interrupted cleanup")
		}
		return oldRemove(path)
	}
	f.deleteProjectA(t)
	if !f.metaExists(f.stateB, f.roamingChild) {
		t.Fatal("fixture: interrupted cleanup should have retained the subagent")
	}
	removeProjectSessionFile = oldRemove

	restored := hubcore.NewPastIndex(filepath.Join(f.root, "projects", "*"))
	if _, err := restored.Rebuild(); err != nil {
		t.Fatal(err)
	}
	_ = NewWebServer(hubcore.WebConfig{HubStateRoot: f.root, StateDir: f.root, Past: restored, Roster: hubcore.NewRosterWithEntries()})
	if f.metaExists(f.stateB, f.roamingChild) {
		t.Fatal("resume did not finish deleting the subagent from its own project's directory")
	}
	if !f.metaExists(f.stateB, f.rootB) {
		t.Fatal("resume deleted another project's root")
	}
}
