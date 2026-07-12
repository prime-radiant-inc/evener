package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/rendezvous"
)

type exactWebSessionSource struct {
	*scriptedAppSource
	err      error
	registry *appsource.Registry
	reads    int
	removeAt int
}

type exactWebSessionSpawner struct {
	resume func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error)
}

func (s exactWebSessionSpawner) Spawn(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
	return rendezvous.Entry{}, errors.New("unused spawn")
}
func (s exactWebSessionSpawner) Resume(ctx context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
	return s.resume(ctx, req)
}

func (s *exactWebSessionSource) ReadThread(ctx context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	resp, err := s.scriptedAppSource.ReadThread(ctx, p)
	s.reads++
	if s.registry != nil && s.reads == s.removeAt {
		s.registry.Remove(s.ID())
	}
	return resp, err
}

func (s *exactWebSessionSource) SteerTurn(context.Context, appwire.TurnSteerParams) error {
	return s.err
}
func (s *exactWebSessionSource) QueueTurn(context.Context, appwire.TurnQueueParams) error {
	return s.err
}
func (s *exactWebSessionSource) DrainAsSteer(context.Context, appwire.TurnDrainAsSteerParams) error {
	return s.err
}
func (s *exactWebSessionSource) InterruptTurn(context.Context, appwire.TurnInterruptParams) error {
	return s.err
}
func (s *exactWebSessionSource) ShutdownThread(context.Context, appwire.ThreadShutdownParams) error {
	return s.err
}
func (s *exactWebSessionSource) ClearThread(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	return appwire.ThreadClearResponse{}, s.err
}
func (s *exactWebSessionSource) CompactThread(context.Context, appwire.ThreadCompactStartParams) error {
	return s.err
}

func FuzzCovExactWebSession(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) {
		oldManaged := webManagedLaunchSourceIDForRef
		oldManagedSource := webSourceForThreadWithManagedLaunch
		oldEnsure := webEnsureThreadActionAvailable
		oldRosterFind := webRosterFind
		oldSourceForThread := webSourceForThread
		oldWaitForRoster := webWaitForRosterMatch
		defer func() {
			webManagedLaunchSourceIDForRef = oldManaged
			webSourceForThreadWithManagedLaunch = oldManagedSource
			webEnsureThreadActionAvailable = oldEnsure
			webRosterFind = oldRosterFind
			webSourceForThread = oldSourceForThread
			webWaitForRosterMatch = oldWaitForRoster
		}()
		caps := appwire.ThreadCapabilities{
			Send: true, Steer: true, Interrupt: true, Compact: true,
			Clear: true, Shutdown: true, Queue: true,
		}
		thread := appwire.Thread{
			ID: "live", SessionID: "live", Source: "remote",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
			Serf:   appwire.SerfThread{Ref: "remote:live", Capabilities: caps},
		}
		roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{Entry: rendezvous.Entry{PID: 11, Address: "unused"}, SessionID: "live", Status: "active"})
		src := &exactWebSessionSource{scriptedAppSource: &scriptedAppSource{id: "remote", thread: thread}, err: errors.New("scripted action failure")}
		web := NewWebServer(hubcore.WebConfig{Roster: roster})
		web.sources.Add(src)

		call := func(fn func(http.ResponseWriter, *http.Request), method, target, body string) {
			req := httptest.NewRequest(method, target, strings.NewReader(body))
			fn(httptest.NewRecorder(), req)
		}
		remoteDenied := NewWebServer(hubcore.WebConfig{})
		remoteDenied.sources.Add(&exactWebSessionSource{scriptedAppSource: &scriptedAppSource{id: "denied-remote", thread: appwire.Thread{ID: "thread", SessionID: "thread", Source: "denied-remote", Serf: appwire.SerfThread{Ref: "denied-remote:thread"}}}})
		call(func(w http.ResponseWriter, r *http.Request) { remoteDenied.handleSend(w, r, "denied-remote:thread") }, http.MethodPost, "/s/denied-remote:thread/send", `{"text":"hi"}`)

		missingSource := func(*appsource.Registry, string, string) (appsource.Source, error) {
			return nil, errors.New("scripted source missing")
		}
		webSourceForThread = missingSource
		noRosterSend := NewWebServer(hubcore.WebConfig{})
		call(func(w http.ResponseWriter, r *http.Request) { noRosterSend.handleSend(w, r, "missing") }, http.MethodPost, "/s/missing/send", `{"text":"hi"}`)

		rosterHit := hubcore.LiveEntry{Entry: rendezvous.Entry{PID: 41, Address: "127.0.0.1:1"}, SessionID: "hit"}
		webRosterFind = func(*hubcore.Roster, string) (hubcore.LiveEntry, bool) { return rosterHit, true }
		firstHit := NewWebServer(hubcore.WebConfig{Roster: hubcore.NewRosterWithEntries()})
		call(func(w http.ResponseWriter, r *http.Request) { firstHit.handleSend(w, r, "hit") }, http.MethodPost, "/s/hit/send", `{"text":"hi"}`)

		findCalls := 0
		webRosterFind = func(*hubcore.Roster, string) (hubcore.LiveEntry, bool) {
			findCalls++
			return rosterHit, findCalls >= 2
		}
		secondHit := NewWebServer(hubcore.WebConfig{Roster: hubcore.NewRosterWithEntries(), Spawner: exactWebSessionSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			return rendezvous.Entry{}, errors.New("must not resume")
		}}})
		call(func(w http.ResponseWriter, r *http.Request) { secondHit.handleSend(w, r, "hit") }, http.MethodPost, "/s/hit/send", `{"text":"hi"}`)
		webRosterFind = oldRosterFind
		webSourceForThread = oldSourceForThread
		call(func(w http.ResponseWriter, r *http.Request) { web.handleSteer(w, r, "remote:live") }, http.MethodPost, "/s/remote:live/steer", `{"text":"go"}`)
		call(func(w http.ResponseWriter, r *http.Request) { web.handleQueue(w, r, "remote:live") }, http.MethodPost, "/s/remote:live/queue", `{"text":"later"}`)
		call(func(w http.ResponseWriter, r *http.Request) { web.handleDrainAsSteer(w, r, "remote:live") }, http.MethodPost, "/s/remote:live/drain-as-steer", `{}`)
		for _, action := range []string{"interrupt", "clear", "shutdown", "unknown"} {
			call(func(w http.ResponseWriter, r *http.Request) { web.handleSessionAction(w, r, "remote:live", action) }, http.MethodPost, "/s/remote:live/"+action, `{}`)
		}
		call(func(w http.ResponseWriter, r *http.Request) { web.handleSessionAction(w, r, "remote:live", "compact") }, http.MethodPost, "/s/remote:live/compact", `{}`)
		src.err = nil
		call(func(w http.ResponseWriter, r *http.Request) { web.handleSessionAction(w, r, "remote:live", "compact") }, http.MethodPost, "/s/remote:live/compact", `{}`)
		call(func(w http.ResponseWriter, r *http.Request) { web.handleSessionAction(w, r, "remote:live", "queue") }, http.MethodPost, "/s/remote:live/queue", `{}`)

		managedThread := thread
		managedThread.ID, managedThread.SessionID, managedThread.Source = "thread", "thread", "managed"
		managedThread.Serf.Ref = "managed:thread"
		managedSource := &exactWebSessionSource{scriptedAppSource: &scriptedAppSource{id: "managed", thread: managedThread, startTurn: func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			return appwire.TurnStartResponse{}, nil
		}}}
		managedWeb := NewWebServer(hubcore.WebConfig{})
		managedWeb.sources.Add(managedSource)
		webManagedLaunchSourceIDForRef = func(hubcore.WebConfig, string) (string, bool) { return "managed", true }
		webSourceForThreadWithManagedLaunch = func(context.Context, hubcore.WebConfig, *appsource.Registry, string, string) (appsource.Source, error) {
			return nil, errors.New("managed launch failure")
		}
		call(func(w http.ResponseWriter, r *http.Request) { managedWeb.handleSend(w, r, "managed:thread") }, http.MethodPost, "/s/managed:thread/send", `{"text":"hi"}`)
		webSourceForThreadWithManagedLaunch = func(context.Context, hubcore.WebConfig, *appsource.Registry, string, string) (appsource.Source, error) {
			return managedSource, nil
		}
		webEnsureThreadActionAvailable = func(context.Context, appsource.Source, string, string, string) error {
			return appwire.Unavailable("managed send unavailable")
		}
		call(func(w http.ResponseWriter, r *http.Request) { managedWeb.handleSend(w, r, "managed:thread") }, http.MethodPost, "/s/managed:thread/send", `{"text":"hi"}`)
		webEnsureThreadActionAvailable = oldEnsure
		call(func(w http.ResponseWriter, r *http.Request) { managedWeb.handleSend(w, r, "managed:thread") }, http.MethodPost, "/s/managed:thread/send", `{"text":"hi"}`)
		webManagedLaunchSourceIDForRef = oldManaged

		invalidItems := make([]appwire.InputItem, 9)
		for i := range invalidItems {
			invalidItems[i].Type = "image"
		}
		invalidBody, err := json.Marshal(queueRequest{Items: invalidItems})
		if err != nil {
			t.Fatal(err)
		}
		call(func(w http.ResponseWriter, r *http.Request) { web.handleQueue(w, r, "remote:live") }, http.MethodPost, "/s/remote:live/queue", string(invalidBody))
		call(func(w http.ResponseWriter, r *http.Request) { web.handleDrainAsSteer(w, r, "remote:live") }, http.MethodPost, "/s/remote:live/drain-as-steer", string(invalidBody))

		vanishing := func(run func(*WebServer)) {
			w := NewWebServer(hubcore.WebConfig{Roster: roster})
			v := &exactWebSessionSource{scriptedAppSource: &scriptedAppSource{id: "remote", thread: thread}, registry: w.sources, removeAt: 2}
			w.sources.Add(v)
			run(w)
		}
		vanishing(func(w *WebServer) {
			call(func(rw http.ResponseWriter, r *http.Request) { w.handleSteer(rw, r, "remote:live") }, http.MethodPost, "/s/remote:live/steer", `{"text":"go"}`)
		})
		vanishing(func(w *WebServer) {
			call(func(rw http.ResponseWriter, r *http.Request) { w.handleQueue(rw, r, "remote:live") }, http.MethodPost, "/s/remote:live/queue", `{"text":"later"}`)
		})
		vanishing(func(w *WebServer) {
			call(func(rw http.ResponseWriter, r *http.Request) { w.handleDrainAsSteer(rw, r, "remote:live") }, http.MethodPost, "/s/remote:live/drain-as-steer", `{}`)
		})
		vanishing(func(w *WebServer) {
			call(func(rw http.ResponseWriter, r *http.Request) {
				w.handleSessionAction(rw, r, "remote:live", "interrupt")
			}, http.MethodPost, "/s/remote:live/interrupt", `{}`)
		})

		// A local send with neither a registered source nor a spawner reaches the
		// deterministic resume-configuration error without launching anything.
		empty := NewWebServer(hubcore.WebConfig{Roster: hubcore.NewRosterWithEntries()})
		call(func(w http.ResponseWriter, r *http.Request) { empty.handleSend(w, r, "missing") }, http.MethodPost, "/s/missing/send", `{"text":"hi"}`)
		emptyNoRoster := NewWebServer(hubcore.WebConfig{})
		call(func(w http.ResponseWriter, r *http.Request) { emptyNoRoster.handleSend(w, r, "missing") }, http.MethodPost, "/s/missing/send", `{"text":"hi"}`)

		// Resume a known local session entirely through scripted boundaries. The
		// spawner publishes a rendezvous record and registers the resumed source;
		// the real roster refresh and send flow run below those boundaries.
		root := t.TempDir()
		stateDir := filepath.Join(root, "projects", "past")
		localID := buildRPCParentSession(t, stateDir)
		past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
		if err := past.Rebuild(); err != nil {
			t.Fatal(err)
		}
		runDir := filepath.Join(root, "run")
		localRoster := hubcore.NewRoster(runDir, fakeProber{sessionID: localID, status: "idle"})
		var localWeb *WebServer
		localThread := appwire.Thread{ID: localID, SessionID: localID, Source: "local", Serf: appwire.SerfThread{Ref: "local:" + localID, Capabilities: appwire.ThreadCapabilities{Send: true}}}
		localSource := &exactWebSessionSource{scriptedAppSource: &scriptedAppSource{id: "local", thread: localThread, startTurn: func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn"}}, nil
		}}}
		webSourceForThread = missingSource
		webWaitForRosterMatch = func(*hubcore.Roster, string, int, time.Duration) hubcore.LiveEntry { return hubcore.LiveEntry{} }
		timedOut := NewWebServer(hubcore.WebConfig{Roster: hubcore.NewRosterWithEntries(), Past: past, Spawner: exactWebSessionSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			return rendezvous.Entry{PID: 99}, nil
		}}})
		call(func(w http.ResponseWriter, r *http.Request) { timedOut.handleSend(w, r, localID) }, http.MethodPost, "/s/"+localID+"/send", `{"text":"hi"}`)
		webSourceForThread = oldSourceForThread
		webWaitForRosterMatch = oldWaitForRoster
		resume := func(_ context.Context, _ hubcore.ResumeRequest) (rendezvous.Entry, error) {
			entry := rendezvous.Entry{PID: os.Getpid(), Address: "127.0.0.1:1", SessionID: localID, SourceID: "local", ThreadID: localID}
			if _, err := rendezvous.Write(runDir, entry); err != nil {
				return rendezvous.Entry{}, err
			}
			localRoster.Refresh()
			if _, ok := localRoster.Find(localID); !ok {
				t.Fatalf("resumed rendezvous entry was not indexed")
			}
			localWeb.sources.Add(localSource)
			return entry, nil
		}
		localWeb = NewWebServer(hubcore.WebConfig{RunDir: runDir, Roster: localRoster, Past: past, Spawner: exactWebSessionSpawner{resume: resume}})
		call(func(w http.ResponseWriter, r *http.Request) { localWeb.handleSend(w, r, localID) }, http.MethodPost, "/s/"+localID+"/send", `{"text":"resume"}`)
		_, _ = localWeb.forkSession(localID, forkRequest{Turn: 3, EditedMessage: "edited", Label: "branch"})

		rosterOnly := NewWebServer(hubcore.WebConfig{Roster: hubcore.NewRosterWithEntries(hubcore.LiveEntry{Entry: rendezvous.Entry{PID: 33, Address: "127.0.0.1:1"}, SessionID: "roster-only"})})
		call(func(w http.ResponseWriter, r *http.Request) { rosterOnly.handleSend(w, r, "roster-only") }, http.MethodPost, "/s/roster-only/send", `{"text":"hi"}`)
		deniedLocal := NewWebServer(hubcore.WebConfig{Roster: hubcore.NewRosterWithEntries(hubcore.LiveEntry{Entry: rendezvous.Entry{PID: 34, Address: "127.0.0.1:1"}, SessionID: "denied"})})
		deniedLocal.sources.Add(&exactWebSessionSource{scriptedAppSource: &scriptedAppSource{id: "local", thread: appwire.Thread{ID: "denied", SessionID: "denied", Source: "local", Serf: appwire.SerfThread{Ref: "local:denied"}}}})
		call(func(w http.ResponseWriter, r *http.Request) { deniedLocal.handleSend(w, r, "denied") }, http.MethodPost, "/s/denied/send", `{"text":"hi"}`)
		remoteRetry := NewWebServer(hubcore.WebConfig{})
		remoteRetry.sources.Add(&exactWebSessionSource{scriptedAppSource: &scriptedAppSource{id: "retry", thread: appwire.Thread{ID: "thread", SessionID: "thread", Source: "retry", Serf: appwire.SerfThread{Ref: "retry:thread", Capabilities: appwire.ThreadCapabilities{Send: true}}}, startTurn: func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			return appwire.TurnStartResponse{}, appwire.SessionUnavailable("gone")
		}}})
		call(func(w http.ResponseWriter, r *http.Request) { remoteRetry.handleSend(w, r, "retry:thread") }, http.MethodPost, "/s/retry:thread/send", `{"text":"hi"}`)

		// A valid past state plus a failing spawner reaches the launcher error
		// mapping without starting a process.
		failSpawn := NewWebServer(hubcore.WebConfig{Roster: hubcore.NewRosterWithEntries(), Past: past, Spawner: exactWebSessionSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			return rendezvous.Entry{}, errors.New("scripted resume failure")
		}}})
		call(func(w http.ResponseWriter, r *http.Request) { failSpawn.handleSend(w, r, localID) }, http.MethodPost, "/s/"+localID+"/send", `{"text":"hi"}`)

		// Exercise the polling miss and its sleep edge with a bounded local roster.
		_ = waitForRosterMatch(hubcore.NewRosterWithEntries(), "missing", 99, 151*time.Millisecond)
	})
}
