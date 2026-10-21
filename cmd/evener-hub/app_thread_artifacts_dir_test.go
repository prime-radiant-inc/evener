package hub

import (
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// The hub announces the directory its doc route serves a session's retained
// artifacts out of, so a client can tell an openable file: link from one the
// hub would refuse. The value must match what the route joins.
func TestThreadReadAnnouncesTheArtifactsDirTheDocRouteServes(t *testing.T) {
	web, _, session := docServeTestServer(t)
	entry, ok := web.cfg.Past.Find(session)
	if !ok {
		t.Fatalf("fixture session %q is missing from the past index", session)
	}
	if _, _, ok := web.resolveArtifactsDocTarget(t.Context(), session, "../"); ok {
		t.Fatal("fixture: an escaping path must not resolve")
	}
	thread, err := pastEntryThreadForList(t.Context(), web.cfg, entry)
	if err != nil {
		t.Fatal(err)
	}
	got := applyHubCapabilities(web.cfg, thread).Evener.ArtifactsDir
	want := artifactsDirOf(t, web, session)
	if got != want {
		t.Fatalf("ArtifactsDir = %q, want %q", got, want)
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

// A remote thread's EvenerThread may carry whatever the remote host chose; the
// hub never stamps a local path over it, because that path does not name the
// remote's files.
func TestThreadReadLeavesRemoteArtifactsDirUnset(t *testing.T) {
	web, _, session := docServeTestServer(t)
	thread := appwire.Thread{ID: session, SessionID: session, Evener: appwire.EvenerThread{Ref: "remote:" + session}}
	if got := applyHubCapabilities(web.cfg, thread).Evener.ArtifactsDir; got != "" {
		t.Fatalf("remote thread advertised a local path: %q", got)
	}
}
