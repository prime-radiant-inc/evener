package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/hubapi"
)

// Both hubs use their real navigation source and RPC handlers. Only persisted
// session inputs are seeded; no source or alias resolver is replaced.
type remoteLocationFixture struct {
	controller, remote  *WebServer
	source              *appsource.RemoteHubSource
	client              *appwire.Client
	wire                *remoteLocationTransport
	attached            atomic.Bool
	root, child, nested string
	stateDir            string
}

// The gate is at the network boundary; both navigation handlers stay real.
type remoteLocationTransport struct {
	appwire.Transport
	calls      atomic.Int32
	beforeRead func(context.Context) error
}

func (w *remoteLocationTransport) Send(ctx context.Context, message appwire.Message) error {
	if message.Request != nil && message.Request.Method == appwire.MethodEvenerNavigationRead {
		w.calls.Add(1)
		if w.beforeRead != nil {
			if err := w.beforeRead(ctx); err != nil {
				return err
			}
		}
	}
	return w.Transport.Send(ctx, message)
}

func newRemoteLocationFixture(t *testing.T, entries ...hubcore.LiveEntry) *remoteLocationFixture {
	t.Helper()
	stateRoot := t.TempDir()
	stateDir := hubtest.ProjectDir(t, filepath.Join(stateRoot, "projects"), "navigation")
	root, child, nested := hubtest.SessionID(t), hubtest.SessionID(t), hubtest.SessionID(t)
	now := time.Now().UTC()
	for _, meta := range []schema.SessionMeta{
		{ID: root, ProfileID: "local"},
		{ID: child, ProfileID: "local", IsSubagent: true, ParentSessionID: root},
		{ID: nested, ProfileID: "local", IsSubagent: true, ParentSessionID: child},
	} {
		meta.CreatedAt, meta.UpdatedAt = now, now
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatal(err)
		}
	}
	past := hubcore.NewPastIndex(filepath.Join(stateRoot, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	remote := NewWebServer(hubcore.WebConfig{HubAddr: server.Listener.Addr().String(), HubStateRoot: stateRoot, Past: past, Roster: hubcore.NewRosterWithEntries(entries...)})
	server.Config.Handler = remote.Handler()
	server.Start()
	t.Cleanup(server.Close)
	transport, err := appwire.DialWebSocket(t.Context(), "ws"+server.URL[len("http"):]+"/rpc", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	wire := &remoteLocationTransport{Transport: transport}
	client := appwire.NewClient(wire)
	client.Start(t.Context())
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	fixture := &remoteLocationFixture{remote: remote, client: client, wire: wire, root: root, child: child, nested: nested, stateDir: stateDir}
	fixture.attached.Store(true)
	source := appsource.NewRemoteHubSource("remote", nil, nil)
	source.SetHostClientIfAttached(func(string) (*appwire.Client, bool) {
		return client, fixture.attached.Load()
	})
	controller := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), RemoteThreadCache: &hubcore.RemoteThreadCache{}})
	t.Cleanup(func() {
		_ = controller.appRPC.Shutdown(context.Background())
		_ = remote.appRPC.Shutdown(context.Background())
	})
	controller.sources.Add(source)
	fixture.controller, fixture.source = controller, source
	fixture.refresh(t)
	return fixture
}

func (f *remoteLocationFixture) refresh(t *testing.T) {
	t.Helper()
	listed, err := f.source.ListThreads(t.Context(), appwire.ThreadListParams{IncludeSubagents: true})
	if err != nil {
		t.Fatal(err)
	}
	f.controller.cfg.RemoteThreadCache.StoreSnapshotData(hubcore.RemoteThreadSnapshot{Threads: listed.Data})
	for _, web := range []*WebServer{f.remote, f.controller} {
		if _, err := web.navigation.Refresh(t.Context(), navigationChangeHint{}); err != nil {
			t.Fatal(err)
		}
	}
}

func remoteLocationRead(t *testing.T, web *WebServer, ref string, base *appwire.NavigationReadBase) appwire.NavigationReadResponse {
	t.Helper()
	read, err := web.navigation.readV3(t.Context(), navigationResourceKey{Kind: navigationResourceLocation, ID: ref}, base)
	if err != nil {
		t.Fatal(err)
	}
	return read.Response
}

func remoteLocationData(t *testing.T, response appwire.NavigationReadResponse) hubapi.NavigationSessionLocation {
	t.Helper()
	if response.Status != "ok" || response.Representation != appwire.NavigationRepresentationSnapshot {
		t.Fatalf("location response = %+v, want full successful location", response)
	}
	var snapshot hubapi.NavigationSnapshot
	if err := json.Unmarshal(response.Data, &snapshot); err != nil {
		t.Fatal(err)
	}
	var location hubapi.NavigationSessionLocation
	if err := json.Unmarshal(snapshot.Metadata, &location); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entities) != 1 {
		t.Fatalf("location has %d entities, want one session", len(snapshot.Entities))
	}
	if err := json.Unmarshal(snapshot.Entities[0].Value, &location.Session); err != nil {
		t.Fatal(err)
	}
	return location
}

func TestNavigationLocationRemoteChild(t *testing.T) {
	f := newRemoteLocationFixture(t)
	rootRef := "remote:" + f.root
	initialCalls := f.wire.calls.Load()
	root := remoteLocationData(t, remoteLocationRead(t, f.controller, rootRef, nil))
	if f.wire.calls.Load() != initialCalls {
		t.Fatal("indexed root location performed a source read")
	}
	for _, id := range []string{f.child, f.nested} {
		ref := "remote:" + id
		read, err := f.source.ReadThread(t.Context(), appwire.ThreadReadParams{Ref: ref})
		if err != nil || read.Thread.Evener.Ref != ref || read.Thread.Evener.Kind != "subagent" {
			t.Fatalf("real remote child read = %+v, %v", read, err)
		}
		if _, indexed := f.controller.navigation.core.projection.locations[ref]; indexed {
			t.Fatal("fixture child unexpectedly present in root-only navigation index")
		}
		before := f.wire.calls.Load()
		location := remoteLocationData(t, remoteLocationRead(t, f.controller, ref, nil))
		if calls := f.wire.calls.Load() - before; calls != 1 {
			t.Fatalf("location used %d source calls, want one irrespective of ancestry depth", calls)
		}
		if location.Ref != ref || location.TopLevel || location.TopLevelRef != rootRef || location.ProjectKey != root.ProjectKey || location.Tier != root.Tier {
			t.Fatalf("remote child placement = %+v, root = %+v", location, root)
		}
		if location.Session.Ref != ref || location.Session.SessionID != id || location.Session.Kind != "subagent" || location.Session.HostID != "remote" {
			t.Fatalf("remote child identity = %+v", location.Session)
		}
	}
}

func (f *remoteLocationFixture) save(t *testing.T, meta schema.SessionMeta) {
	t.Helper()
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = time.Now().UTC()
		meta.UpdatedAt = meta.CreatedAt
	}
	if err := schema.SaveSessionMeta(f.stateDir, meta); err != nil {
		t.Fatal(err)
	}
	if _, err := f.remote.cfg.Past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.remote.navigation.Refresh(t.Context(), navigationChangeHint{}); err != nil {
		t.Fatal(err)
	}
}

func TestNavigationLocationRemoteFreshChildAndForkRefresh(t *testing.T) {
	f := newRemoteLocationFixture(t)
	fresh := hubtest.SessionID(t)
	// The child is created after the controller's remote list snapshot. A
	// successful location must consult the host, not assume that cache has it.
	f.save(t, schema.SessionMeta{ID: fresh, ProfileID: "local", IsSubagent: true, ParentSessionID: f.nested})
	ref := "remote:" + fresh
	before := remoteLocationRead(t, f.controller, ref, nil)
	if got := remoteLocationData(t, before); got.TopLevelRef != "remote:"+f.root {
		t.Fatalf("fresh descendant = %+v", got)
	}
	if same := remoteLocationRead(t, f.controller, ref, aliasBase(before)); same.Status != "not_modified" {
		t.Fatalf("unchanged child = %+v", same)
	}

	// A fork continuation replaces the original's top-level row. Its existing
	// child and grandchild now route through the original to the continuation.
	entry, ok := f.remote.cfg.Past.FindIndexed(f.root)
	if !ok {
		t.Fatal("root missing")
	}
	entry.Meta.ForkLabel = "original"
	f.save(t, entry.Meta)
	continuation := hubtest.SessionID(t)
	f.save(t, schema.SessionMeta{ID: continuation, ProfileID: "local", ParentSessionID: f.root})
	f.refresh(t)
	changed := remoteLocationRead(t, f.controller, ref, aliasBase(before))
	if changed.Status != "ok" || changed.Revision <= before.Revision {
		t.Fatalf("fork placement revision = %+v, before = %+v", changed, before)
	}
	got := remoteLocationData(t, remoteLocationRead(t, f.controller, ref, nil))
	if got.TopLevelRef != "remote:"+continuation || got.Session.Kind != "subagent" {
		t.Fatalf("mixed child/fork ancestry = %+v", got)
	}
	ancestor, kind, err := f.controller.navigationSubagentParent(t.Context(), "remote:"+f.root)
	if err != nil || ancestor != "remote:"+continuation || kind != "fork" {
		t.Fatalf("fork alias hop = %s, %s, %v", ancestor, kind, err)
	}
}

func TestNavigationLocationRemoteSourceIsolationAndAbsence(t *testing.T) {
	f := newRemoteLocationFixture(t)
	// The same IDs exist here under a different local root. Remote resolution
	// may never borrow that lineage, including after the host is removed.
	localRoot := hubtest.SessionID(t)
	f.controller.cfg.Past.SeedForTest([]schema.SessionMeta{
		{ID: localRoot, ProfileID: "local"},
		{ID: f.child, ProfileID: "local", IsSubagent: true, ParentSessionID: localRoot},
	})
	f.refresh(t)
	ref := "remote:" + f.child
	before := remoteLocationRead(t, f.controller, ref, nil)
	if got := remoteLocationData(t, before); got.TopLevelRef != "remote:"+f.root {
		t.Fatalf("remote lineage borrowed local identity: %+v", got)
	}
	for _, missing := range []string{"remote:" + hubtest.SessionID(t), "absent:" + f.child} {
		if got := remoteLocationRead(t, f.controller, missing, nil); got.Status != "gone" {
			t.Fatalf("unknown location %s = %+v", missing, got)
		}
	}

	// Remove only this fixture's persisted metadata, then rebuild its real
	// source index. The controller still holds the old list, not an authority
	// for this child's continued existence.
	if err := os.Remove(filepath.Join(f.stateDir, "sessions", f.child+".meta.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.remote.cfg.Past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.remote.navigation.Refresh(t.Context(), navigationChangeHint{}); err != nil {
		t.Fatal(err)
	}
	gone := remoteLocationRead(t, f.controller, ref, aliasBase(before))
	if gone.Status != "gone" || gone.Revision <= before.Revision {
		t.Fatalf("deleted child = %+v, before = %+v", gone, before)
	}
	if same := remoteLocationRead(t, f.controller, ref, aliasBase(gone)); same.Status != "not_modified" {
		t.Fatalf("stable deletion = %+v", same)
	}
	f.controller.sources.Remove("remote")
	if got := remoteLocationRead(t, f.controller, "remote:"+f.nested, nil); got.Status != "gone" {
		t.Fatalf("removed source = %+v", got)
	}
}

func TestNavigationLocationRemoteUnavailableRecovers(t *testing.T) {
	f := newRemoteLocationFixture(t)
	ref := "remote:" + f.child
	before := remoteLocationRead(t, f.controller, ref, nil)
	f.attached.Store(false)
	result, err := f.controller.navigation.readV3(t.Context(), navigationResourceKey{Kind: navigationResourceLocation, ID: ref}, aliasBase(before))
	var unavailable navigationAvailabilityError
	if !errors.As(err, &unavailable) || result.Response.Status != "" {
		t.Fatalf("unattached source = %+v, %v, want unavailable, not gone", result, err)
	}
	f.attached.Store(true)
	if got := remoteLocationRead(t, f.controller, ref, aliasBase(before)); got.Status != "not_modified" {
		t.Fatalf("reattached unchanged source = %+v", got)
	}
}

func TestNavigationLocationRemoteReadFencesSourceReplacement(t *testing.T) {
	f := newRemoteLocationFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	f.wire.beforeRead = func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		_, err := f.controller.navigation.readV3(ctx, navigationResourceKey{Kind: navigationResourceLocation, ID: "remote:" + f.child}, nil)
		done <- err
	}()
	<-entered
	// The source call must not hold the navigation lock: an indexed root can
	// still be read while the child's network operation is parked.
	remoteLocationData(t, remoteLocationRead(t, f.controller, "remote:"+f.root, nil))
	replacement := appsource.NewRemoteHubSource("remote", nil, func(context.Context, string) (*appwire.Client, error) { return f.client, nil })
	f.controller.sources.Add(replacement)
	close(release)
	var unavailable navigationAvailabilityError
	if err := <-done; !errors.As(err, &unavailable) {
		t.Fatalf("replaced source read = %v, want unavailable", err)
	}
	f.wire.beforeRead = nil
	remoteLocationData(t, remoteLocationRead(t, f.controller, "remote:"+f.child, nil))
}

func TestNavigationLocationRemoteReadCancellation(t *testing.T) {
	f := newRemoteLocationFixture(t)
	entered := make(chan struct{})
	f.wire.beforeRead = func(ctx context.Context) error {
		if _, bounded := ctx.Deadline(); !bounded {
			t.Error("remote location read has no deadline")
		}
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := f.controller.navigation.readV3(ctx, navigationResourceKey{Kind: navigationResourceLocation, ID: "remote:" + f.child}, nil)
		done <- err
	}()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled source read = %v", err)
	}
}

func TestNavigationLocationRemoteCurrentSessionUsesStableWorkspaceRow(t *testing.T) {
	stable, current, child := hubtest.SessionID(t), hubtest.SessionID(t), hubtest.SessionID(t)
	entry := liveActivityEntry(1, current, appwire.ThreadStatusActive, nil)
	entry.Entry.SourceID = "local"
	entry.Entry.WorkspaceRef = "local:" + stable
	entry.Crashed = true
	f := newRemoteLocationFixture(t, entry)
	f.save(t, schema.SessionMeta{ID: stable, ProfileID: "local"})
	f.refresh(t)
	// The controller retains the real pre-resume list while the source has
	// already catalogued the new identity and its child.
	f.save(t, schema.SessionMeta{ID: current, ProfileID: "local"})
	f.save(t, schema.SessionMeta{ID: child, ProfileID: "local", IsSubagent: true, ParentSessionID: current})
	location := remoteLocationData(t, remoteLocationRead(t, f.controller, "remote:"+current, nil))
	if !location.TopLevel || location.TopLevelRef != "remote:"+stable || location.Session.Kind != "session" || location.Session.HostID != "remote" || location.Session.SessionID != current {
		t.Fatalf("remote resumed identity = %+v, session = %+v", location, location.Session)
	}
	nested := remoteLocationData(t, remoteLocationRead(t, f.controller, "remote:"+child, nil))
	if nested.TopLevel || nested.TopLevelRef != "remote:"+stable || nested.Session.Kind != "subagent" {
		t.Fatalf("remote child of resumed identity = %+v", nested)
	}
}

func TestNavigationLocationRemoteWaitsForRootSnapshot(t *testing.T) {
	f := newRemoteLocationFixture(t)
	root := hubtest.SessionID(t)
	f.save(t, schema.SessionMeta{ID: root, ProfileID: "local"})
	f.save(t, schema.SessionMeta{ID: f.child, ProfileID: "local", IsSubagent: true, ParentSessionID: root})
	ref := "remote:" + f.child
	before := f.wire.calls.Load()
	result, err := f.controller.navigation.readV3(t.Context(), navigationResourceKey{Kind: navigationResourceLocation, ID: ref}, nil)
	var unavailable navigationAvailabilityError
	if !errors.As(err, &unavailable) || result.Response.Status != "" {
		t.Fatalf("root absent from controller snapshot = %+v, %v, want unavailable, not gone", result, err)
	}
	if calls := f.wire.calls.Load() - before; calls != 1 {
		t.Fatalf("location used %d source calls, want one", calls)
	}
	result, err = f.controller.navigation.readV3(t.Context(), navigationResourceKey{Kind: navigationResourceLocation, ID: "remote:" + root}, nil)
	if !errors.As(err, &unavailable) || result.Response.Status != "" {
		t.Fatalf("new root absent from controller snapshot = %+v, %v, want unavailable, not gone", result, err)
	}
	f.refresh(t)
	location := remoteLocationData(t, remoteLocationRead(t, f.controller, ref, nil))
	if location.TopLevelRef != "remote:"+root {
		t.Fatalf("refreshed root location = %+v", location)
	}
}
