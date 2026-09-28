package hub

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/plugins"
)

// The notices name what blocks sessions (S11, spec 7.1): a provider instance
// that needs signing in again, with this hub's live sessions whose current
// model runs on it; a host that is offline, with its sessions that were live
// when last reached; and a broken plugin, with no count. Sign-ins come first,
// then hosts, then plugins, as the phone lists them.
func TestHubNoticesNameWhatBlocksSessions(t *testing.T) {
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{PID: 1, SessionID: "s1", Status: appwire.ThreadStatusIdle, Profile: "codex-jesse-fsck.com"},
		hubcore.LiveEntry{PID: 2, SessionID: "s2", Status: appwire.ThreadStatusActive, Profile: "codex-jesse-fsck.com"},
		// A crashed session's process is gone, so it runs on nothing.
		hubcore.LiveEntry{PID: 3, SessionID: "s3", Status: appwire.ThreadStatusActive, Profile: "codex-jesse-fsck.com", Crashed: true},
		hubcore.LiveEntry{PID: 4, SessionID: "s4", Status: appwire.ThreadStatusIdle, Profile: "lunaroute"},
	)
	sources := appsource.NewRegistry()
	sources.Add(&offlineStubSource{scriptedAppSource: &scriptedAppSource{id: "paradise-park"}, online: false})
	sources.Add(&offlineStubSource{scriptedAppSource: &scriptedAppSource{id: "m4"}, online: true})
	remote := &hubcore.RemoteThreadCache{}
	remote.Store([]appwire.Thread{
		remoteNoticeThread("paradise-park", "p1", appwire.ThreadStatusIdle, ""),
		remoteNoticeThread("paradise-park", "p2", appwire.ThreadStatusActive, ""),
		// A subagent is summarized on its coordinator; an ended session is not
		// blocked by anything.
		remoteNoticeThread("paradise-park", "p3", appwire.ThreadStatusActive, "subagent"),
		remoteNoticeThread("paradise-park", "p4", appwire.ThreadStatusClosed, ""),
		remoteNoticeThread("m4", "m1", appwire.ThreadStatusActive, ""),
	})
	notices := &hubNotices{
		auth: func() (appwire.AuthListResponse, error) {
			return appwire.AuthListResponse{Providers: []appwire.AuthStatusResponse{
				{Provider: "lunaroute", SignedIn: true},
				{Provider: "codex-jesse-fsck.com", NeedsLogin: true},
			}}, nil
		},
		plugins: func(context.Context) (appwire.PluginListResponse, error) {
			return appwire.PluginListResponse{Plugins: []appwire.PluginEntry{
				{Plugin: "go", Marketplace: "acme"},
				{Plugin: "superpowers", Marketplace: "obra", Broken: true},
			}}, nil
		},
		sources: sources,
		roster:  roster,
		remote:  remote,
	}

	got, failed := notices.derive(context.Background())
	want := []appwire.HubNotice{
		{ID: "signInRequired:codex-jesse-fsck.com", Kind: appwire.NoticeKindSignInRequired, Subject: "codex-jesse-fsck.com", AffectedSessions: 2},
		{ID: "hostOffline:paradise-park", Kind: appwire.NoticeKindHostOffline, Subject: "paradise-park", AffectedSessions: 2},
		{ID: "pluginBroken:superpowers@obra", Kind: appwire.NoticeKindPluginBroken, Subject: "superpowers", Marketplace: "obra"},
	}
	if !reflect.DeepEqual(got, want) || len(failed) != 0 {
		t.Fatalf("notices = %+v (failed %v), want %+v", got, failed, want)
	}
}

func remoteNoticeThread(source, id, status, kind string) appwire.Thread {
	return appwire.Thread{
		ID: id, SessionID: id, Source: source,
		Status: appwire.ThreadStatus{Type: status},
		Evener: appwire.EvenerThread{Ref: source + ":" + id, Kind: kind},
	}
}

// With nothing wrong there are no notices, and the list is empty rather than
// null, so a client can render it as it comes.
func TestHubNoticesAreEmptyWhenNothingIsWrong(t *testing.T) {
	got, _ := (&hubNotices{}).derive(context.Background())
	if got == nil || len(got) != 0 {
		t.Fatalf("notices = %#v, want an empty list", got)
	}
}

// The sign-in and plugin notices come from the controllers that answer
// evener/auth/list and evener/plugin/list, so a notice and the screen its
// action opens always agree: an OAuth record whose access token expired with
// no usable refresh token needs signing in again (#2483), and a plugin whose
// install directory is gone is broken.
func TestHubNoticesReadTheAuthAndPluginControllers(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	auth := newHubAuthController(map[string]string{"OPENAI_API_KEY": ""})
	auth.stateDir = t.TempDir()
	attachTestRegistry(t, auth)
	auth.now = func() time.Time { return now }
	if err := authopenai.SaveAuth(auth.stateDir, "openai-codex", authopenai.AuthRecord{
		Version: 1, Provider: "openai", Source: authopenai.AuthSourceOAuth,
		ObtainedAt: now.Add(-2 * time.Hour), TokenType: "Bearer", AccessToken: "stored-access-token",
		RefreshToken: "   ", Expiry: now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	pluginRoot := t.TempDir()
	if err := plugins.SaveRegistry(filepath.Join(pluginRoot, "installed_plugins.json"), plugins.Registry{
		Plugins: map[string][]plugins.InstallEntry{
			"superpowers@obra": {{
				InstallPath: filepath.Join(pluginRoot, "gone"), Version: "1.0.0", Enabled: true,
				Source: plugins.Source{Kind: plugins.SourceDirectory, Path: filepath.Join(pluginRoot, "gone")},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	pluginsController := &hubPluginsController{mgr: plugins.NewManager(pluginRoot)}
	notices := &hubNotices{
		auth:    func() (appwire.AuthListResponse, error) { return auth.List(appwire.EmptyParams{}) },
		plugins: pluginsController.ListPlugins,
	}

	got, failed := notices.derive(context.Background())
	want := []appwire.HubNotice{
		{ID: "signInRequired:openai-codex", Kind: appwire.NoticeKindSignInRequired, Subject: "openai-codex"},
		{ID: "pluginBroken:superpowers@obra", Kind: appwire.NoticeKindPluginBroken, Subject: "superpowers", Marketplace: "obra"},
	}
	if !reflect.DeepEqual(got, want) || len(failed) != 0 {
		t.Fatalf("notices = %+v (failed %v), want %+v", got, failed, want)
	}
}

// The watcher announces each change once, with the whole new list: a notice
// appearing, its count moving, and it clearing. A tick that finds the same
// notices announces nothing. Only a change in which providers need signing in
// also broadcasts the no-data evener/auth/updated, so a providers pane
// refreshes; a count moving does not.
func TestNoticeWatcherAnnouncesEachChangeOnce(t *testing.T) {
	signIn := func(count int) appwire.HubNotice {
		return appwire.HubNotice{ID: "signInRequired:codex", Kind: appwire.NoticeKindSignInRequired, Subject: "codex", AffectedSessions: count}
	}
	steps := [][]appwire.HubNotice{
		{},          // the baseline, taken before the first tick
		{},          // unchanged: nothing announced
		{signIn(1)}, // a sign-in needed: auth updated, then notices
		{signIn(1)}, // unchanged
		{signIn(3)}, // the count moved: notices only
		{},          // resolved: auth updated, then notices
	}
	derived := make(chan []appwire.HubNotice, len(steps))
	for _, step := range steps {
		derived <- step
	}
	read := func(context.Context) []appwire.HubNotice { return <-derived }
	broadcaster := newRecordingBroadcaster()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runNoticeWatcher(ctx, ticks, read, broadcaster)
		close(done)
	}()
	for range steps[1:] {
		ticks <- time.Time{}
	}
	// The watcher handles a tick before it can see the cancel, and done
	// closes only once it has returned, so every broadcast is in by then.
	cancel()
	<-done

	want := []recordedBroadcast{
		{method: appwire.NotifyEvenerAuthUpdated, params: appwire.EvenerAuthUpdatedParams{}},
		{method: appwire.NotifyEvenerNoticesChanged, params: appwire.NoticesListResponse{Notices: []appwire.HubNotice{signIn(1)}}},
		{method: appwire.NotifyEvenerNoticesChanged, params: appwire.NoticesListResponse{Notices: []appwire.HubNotice{signIn(3)}}},
		{method: appwire.NotifyEvenerAuthUpdated, params: appwire.EvenerAuthUpdatedParams{}},
		{method: appwire.NotifyEvenerNoticesChanged, params: appwire.NoticesListResponse{Notices: []appwire.HubNotice{}}},
	}
	if got := broadcaster.broadcasts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("broadcasts = %+v, want %+v", got, want)
	}
}

// A read that fails is no news: the kind whose read failed keeps the notices
// the last read gave, for evener/notices/list and the watcher alike, so a
// transient failure never reports a problem resolved and then new again.
func TestHubNoticesKeepAFailedKindsLastNotices(t *testing.T) {
	sources := appsource.NewRegistry()
	host := &offlineStubSource{scriptedAppSource: &scriptedAppSource{id: "paradise-park"}, online: true}
	sources.Add(host)
	var authErr error
	notices := &hubNotices{
		auth: func() (appwire.AuthListResponse, error) {
			return appwire.AuthListResponse{Providers: []appwire.AuthStatusResponse{{Provider: "codex", NeedsLogin: true}}}, authErr
		},
		sources: sources,
	}
	signIn := appwire.HubNotice{ID: "signInRequired:codex", Kind: appwire.NoticeKindSignInRequired, Subject: "codex"}
	offline := appwire.HubNotice{ID: "hostOffline:paradise-park", Kind: appwire.NoticeKindHostOffline, Subject: "paradise-park"}
	if got := notices.read(context.Background()); !reflect.DeepEqual(got, []appwire.HubNotice{signIn}) {
		t.Fatalf("first read = %+v, want the sign-in", got)
	}
	// The auth read fails and the host goes offline in the same read.
	authErr = errors.New("credentials unreadable")
	host.online = false
	if got := notices.read(context.Background()); !reflect.DeepEqual(got, []appwire.HubNotice{signIn, offline}) {
		t.Fatalf("read with a failed auth read = %+v, want the sign-in kept beside the new host notice", got)
	}
}

// A kind that fails to read is left out of evener/notices/list's answer rather
// than failing the whole read: the other kinds still show.
func TestHubNoticesLeaveOutAKindThatFailsToRead(t *testing.T) {
	sources := appsource.NewRegistry()
	sources.Add(&offlineStubSource{scriptedAppSource: &scriptedAppSource{id: "paradise-park"}, online: false})
	notices := &hubNotices{
		auth: func() (appwire.AuthListResponse, error) {
			return appwire.AuthListResponse{}, errors.New("credentials unreadable")
		},
		plugins: func(context.Context) (appwire.PluginListResponse, error) {
			return appwire.PluginListResponse{}, errors.New("store unreadable")
		},
		sources: sources,
	}
	got, failed := notices.derive(context.Background())
	want := []appwire.HubNotice{{ID: "hostOffline:paradise-park", Kind: appwire.NoticeKindHostOffline, Subject: "paradise-park"}}
	wantFailed := map[string]bool{appwire.NoticeKindSignInRequired: true, appwire.NoticeKindPluginBroken: true}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(failed, wantFailed) {
		t.Fatalf("notices = %+v failed = %v, want %+v failed %v", got, failed, want, wantFailed)
	}
}
