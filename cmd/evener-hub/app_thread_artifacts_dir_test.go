package hub

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// The hub announces the directory its doc route serves a session's retained
// artifacts out of, so a client can tell an openable file:// link from one the
// hub would refuse. The test proves the announced directory is the one a real
// retained file resolves through, not merely that two helpers spell a path the
// same way.
func TestThreadReadAnnouncesTheArtifactsDirTheDocRouteServes(t *testing.T) {
	web, _, session := docServeTestServer(t)
	entry, ok := web.cfg.Past.Find(session)
	if !ok {
		t.Fatalf("fixture session %q is missing from the past index", session)
	}
	want, err := schema.SessionArtifactsDir(entry.StateDir, session)
	if err != nil {
		t.Fatalf("fixture artifacts dir: %v", err)
	}
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(want, "report.md")
	if err := os.WriteFile(target, []byte("retained"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, abs, ok := web.resolveArtifactsDocTarget(t.Context(), session, target)
	if !ok || root != want || abs != target {
		t.Fatalf("doc route resolved (%q, %q, %v), want (%q, %q, true)", root, abs, ok, want, target)
	}
	thread, err := pastEntryThreadForList(t.Context(), web.cfg, entry)
	if err != nil {
		t.Fatal(err)
	}
	got := applyHubCapabilities(web.cfg, thread).Evener.ArtifactsDir
	if got != root {
		t.Fatalf("ArtifactsDir = %q, want the root the doc route served from, %q", got, root)
	}
}

// A ref this hub does not serve locally names no path here, so it must not
// advertise one: the doc route could not serve it either.
func TestHubArtifactsDirStaysEmptyForRefsThisHubDoesNotServe(t *testing.T) {
	web, _, session := docServeTestServer(t)
	for _, ref := range []string{
		"",
		"remote:" + session,
		"local:",
		"local:../../etc",
		"not a ref",
	} {
		if got := hubArtifactsDir(web.cfg, ref); got != "" {
			t.Errorf("hubArtifactsDir(%q) = %q, want no path", ref, got)
		}
	}
	if got := hubArtifactsDir(hubcore.WebConfig{}, "local:"+session); got != "" {
		t.Errorf("a hub serving no state dir advertised %q", got)
	}
	if got := hubArtifactsDir(web.cfg, "local:"+session); got == "" {
		t.Error("the fixture session must advertise its artifacts dir")
	}
}

// A remote thread's artifacts live on its own host, so this hub advertises no
// local path for one: it clears whatever value the thread arrived with rather
// than letting a path this hub served stand in for the remote's files.
func TestThreadReadLeavesRemoteArtifactsDirUnset(t *testing.T) {
	web, _, session := docServeTestServer(t)
	thread := appwire.Thread{
		ID: session, SessionID: session,
		Evener: appwire.EvenerThread{Ref: "remote:" + session, ArtifactsDir: "/remote/host/artifacts"},
	}
	if got := applyHubCapabilities(web.cfg, thread).Evener.ArtifactsDir; got != "" {
		t.Fatalf("remote thread advertised %q, want no local path", got)
	}
}

// A running delegate's artifacts are announced by the same resolver the doc
// route serves from. Roster.Find never holds a child id, so a stamp that asked
// only it would advertise nothing while the route would happily serve the
// file, and the client would refuse a link the hub could open.
func TestRunningDelegateArtifactsDirMatchesTheRoute(t *testing.T) {
	const ownerID = "02wMz5Txv1C3Hut0M8GCeC"
	const childID = "02wMz5Txv1C3Hut0M8GCeD"
	stateDir := t.TempDir()
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry:                 rendezvous.Entry{SessionID: ownerID, StateDir: stateDir, StartedAt: time.Now().UTC()},
		SessionID:             ownerID,
		RunningSubagentIDs:    []string{childID},
		RunningSubagentStates: map[string]string{childID: "active"},
	})
	cfg := hubcore.WebConfig{Roster: roster}
	want, dirErr := schema.SessionArtifactsDir(stateDir, childID)
	if dirErr != nil {
		t.Fatalf("fixture artifacts dir: %v", dirErr)
	}
	if got := sessionArtifactsRoot(t.Context(), cfg, "local:"+childID); got != want {
		t.Fatalf("route root = %q, want %q", got, want)
	}
	thread := appwire.Thread{ID: childID, SessionID: childID, Evener: appwire.EvenerThread{Ref: "local:" + childID}}
	if got := applyHubCapabilities(cfg, thread).Evener.ArtifactsDir; got != want {
		t.Fatalf("advertised artifacts dir = %q, want the route's %q", got, want)
	}
}

// A live session keeps the stable workspace ref it was launched under across
// Clear, while its current session id changes. The roster keys live entries by
// the current id, so resolving the stable ref must follow it to the session
// that owns it; otherwise a cleared session advertises (and serves) an
// artifacts directory that belongs to nobody.
func TestArtifactsRootFollowsAStableRefToItsCurrentSession(t *testing.T) {
	const stableRef = "local:02wMz5Txv1C3Hut0M8GCeG"
	const currentSession = "02wMz5Txv1C3Hut0M8GCeH"
	stateDir := t.TempDir()
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry:     rendezvous.Entry{SessionID: currentSession, WorkspaceRef: stableRef, StateDir: stateDir, StartedAt: time.Now().UTC()},
		SessionID: currentSession,
	})
	cfg := hubcore.WebConfig{Roster: roster}
	want, err := schema.SessionArtifactsDir(stateDir, currentSession)
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionArtifactsRoot(t.Context(), cfg, stableRef); got != want {
		t.Fatalf("stable ref root = %q, want the current session's %q", got, want)
	}
	thread := appwire.Thread{ID: currentSession, SessionID: currentSession, Evener: appwire.EvenerThread{Ref: stableRef}}
	if got := applyHubCapabilities(cfg, thread).Evener.ArtifactsDir; got != want {
		t.Fatalf("advertised artifacts dir = %q, want %q", got, want)
	}
}
