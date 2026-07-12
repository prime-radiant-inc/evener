package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
}

func (s *exactWebSessionSource) ReadThread(ctx context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	resp, err := s.scriptedAppSource.ReadThread(ctx, p)
	if s.registry != nil {
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
		call(func(w http.ResponseWriter, r *http.Request) { web.handleSteer(w, r, "remote:live") }, http.MethodPost, "/s/remote:live/steer", `{"text":"go"}`)
		call(func(w http.ResponseWriter, r *http.Request) { web.handleQueue(w, r, "remote:live") }, http.MethodPost, "/s/remote:live/queue", `{"text":"later"}`)
		call(func(w http.ResponseWriter, r *http.Request) { web.handleDrainAsSteer(w, r, "remote:live") }, http.MethodPost, "/s/remote:live/drain-as-steer", `{}`)
		for _, action := range []string{"interrupt", "clear", "shutdown", "unknown"} {
			call(func(w http.ResponseWriter, r *http.Request) { web.handleSessionAction(w, r, "remote:live", action) }, http.MethodPost, "/s/remote:live/"+action, `{}`)
		}
		call(func(w http.ResponseWriter, r *http.Request) { web.handleSessionAction(w, r, "remote:live", "compact") }, http.MethodPost, "/s/remote:live/compact", `{}`)
		src.err = nil
		call(func(w http.ResponseWriter, r *http.Request) { web.handleSessionAction(w, r, "remote:live", "compact") }, http.MethodPost, "/s/remote:live/compact", `{}`)

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
			v := &exactWebSessionSource{scriptedAppSource: &scriptedAppSource{id: "remote", thread: thread}, registry: w.sources}
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

		// Exercise the polling miss and its sleep edge with a bounded local roster.
		_ = waitForRosterMatch(hubcore.NewRosterWithEntries(), "missing", 99, 151*time.Millisecond)
	})
}
