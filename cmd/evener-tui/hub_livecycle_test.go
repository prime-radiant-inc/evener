package tui

// Tests for the alt+shift+right / alt+shift+left session-view bindings that
// switch the viewed session to the next/previous LIVE session in dashboard
// order (the web shell's session.liveNext/session.livePrevious counterpart,
// same chords). The selection walks buildDashboardRows(m.tree) filtered to
// live session rows; the switch reuses fetchHubSession's thread/read path.

import (
	"context"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

// liveCycleTree holds three live sessions in one project (A, B, C, sorted by
// UpdatedAt desc) plus one non-live session (X) that must never be a cycling
// target.
func liveCycleTree() hubTreeResponse {
	return hubTreeResponse{Projects: []hubTreeProject{{
		Key:  "p1",
		Name: "One",
		Sessions: []hubTreeNode{
			{Ref: "local:01A", SessionID: "sess_a", Title: "A", State: "active", Live: true, UpdatedAt: 300},
			{Ref: "local:01B", SessionID: "sess_b", Title: "B", State: "active", Live: true, UpdatedAt: 200},
			{Ref: "local:01C", SessionID: "sess_c", Title: "C", State: "active", Live: true, UpdatedAt: 100},
			{Ref: "local:01X", SessionID: "sess_x", Title: "X", State: "ended", Live: false, UpdatedAt: 50},
		},
	}}}
}

type liveCycleReads struct {
	mu   sync.Mutex
	refs []string
}

func (r *liveCycleReads) record(params appwire.ThreadReadParams) appwire.ThreadReadResponse {
	r.mu.Lock()
	r.refs = append(r.refs, params.Ref)
	r.mu.Unlock()
	return appwire.ThreadReadResponse{Thread: responseOnlyHubThread(params.Ref)}
}

func (r *liveCycleReads) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.refs...)
}

func newLiveCycleModel(t *testing.T, currentRef string, tree hubTreeResponse) (hubModel, *liveCycleReads, func()) {
	t.Helper()
	reads := &liveCycleReads{}
	client, feed, cleanup := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return reads.record(params), nil
		})
	})
	m := newHubModel(client, "")
	m.frames = feed
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: currentRef, SessionID: "sess_current"}
	m.tree = tree
	return m, reads, cleanup
}

// pressLiveCycleKey drives one key through updateSessionKey and synchronously
// runs the command it returned, returning the resulting hubSessionMsg.
func pressLiveCycleKey(t *testing.T, m hubModel, msg tea.KeyMsg) hubSessionMsg {
	t.Helper()
	_, cmd := m.updateSessionKey(msg)
	if cmd == nil {
		t.Fatal("expected a session-fetch command, got nil")
	}
	result, ok := cmd().(hubSessionMsg)
	if !ok {
		t.Fatalf("command result = %T, want hubSessionMsg", cmd())
	}
	if result.err != nil {
		t.Fatalf("session fetch err = %v", result.err)
	}
	if result.capture != nil {
		result.capture.Release()
	}
	return result
}

func TestHubSessionAltShiftRightSwitchesToNextLiveSession(t *testing.T) {
	m, reads, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	msg := pressLiveCycleKey(t, m, tea.KeyMsg{Type: tea.KeyShiftRight, Alt: true})
	if msg.ref != "local:01C" {
		t.Fatalf("switched to ref %q, want local:01C", msg.ref)
	}
	if got := reads.get(); len(got) != 1 || got[0] != "local:01C" {
		t.Fatalf("thread/read refs = %v, want [local:01C]", got)
	}
}

func TestHubSessionAltShiftLeftSwitchesToPreviousLiveSession(t *testing.T) {
	m, _, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	msg := pressLiveCycleKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft, Alt: true})
	if msg.ref != "local:01A" {
		t.Fatalf("switched to ref %q, want local:01A", msg.ref)
	}
}

func TestHubSessionLiveCycleWrapsBothEnds(t *testing.T) {
	m, _, cleanup := newLiveCycleModel(t, "local:01C", liveCycleTree())
	defer cleanup()

	if msg := pressLiveCycleKey(t, m, tea.KeyMsg{Type: tea.KeyShiftRight, Alt: true}); msg.ref != "local:01A" {
		t.Fatalf("next from last live session = %q, want wrap to local:01A", msg.ref)
	}
	if msg := pressLiveCycleKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft, Alt: true}); msg.ref != "local:01B" {
		t.Fatalf("previous from local:01C = %q, want local:01B", msg.ref)
	}

	mFirst, _, cleanupFirst := newLiveCycleModel(t, "local:01A", liveCycleTree())
	defer cleanupFirst()
	if msg := pressLiveCycleKey(t, mFirst, tea.KeyMsg{Type: tea.KeyShiftLeft, Alt: true}); msg.ref != "local:01C" {
		t.Fatalf("previous from first live session = %q, want wrap to local:01C", msg.ref)
	}
}

func TestHubSessionLiveCycleFromNonLiveSessionLandsOnAnEnd(t *testing.T) {
	m, _, cleanup := newLiveCycleModel(t, "local:01X", liveCycleTree())
	defer cleanup()

	if msg := pressLiveCycleKey(t, m, tea.KeyMsg{Type: tea.KeyShiftRight, Alt: true}); msg.ref != "local:01A" {
		t.Fatalf("next from a non-live session = %q, want first live local:01A", msg.ref)
	}
	if msg := pressLiveCycleKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft, Alt: true}); msg.ref != "local:01C" {
		t.Fatalf("previous from a non-live session = %q, want last live local:01C", msg.ref)
	}
}

func TestHubSessionLiveCycleNoOpWithSingleLiveSession(t *testing.T) {
	tree := liveCycleTree()
	tree.Projects[0].Sessions = tree.Projects[0].Sessions[:1]
	m, reads, cleanup := newLiveCycleModel(t, "local:01A", tree)
	defer cleanup()

	_, cmd := m.updateSessionKey(tea.KeyMsg{Type: tea.KeyShiftRight, Alt: true})
	if cmd != nil {
		t.Fatalf("single live session: expected nil command, got one")
	}
	if got := reads.get(); len(got) != 0 {
		t.Fatalf("single live session issued thread/read for %v", got)
	}
}

func TestHubSessionLiveCycleNoOpWithoutTree(t *testing.T) {
	m, reads, cleanup := newLiveCycleModel(t, "local:01B", hubTreeResponse{})
	defer cleanup()

	_, cmd := m.updateSessionKey(tea.KeyMsg{Type: tea.KeyShiftRight, Alt: true})
	if cmd != nil {
		t.Fatal("empty tree: expected nil command, got one")
	}
	if got := reads.get(); len(got) != 0 {
		t.Fatalf("empty tree issued thread/read for %v", got)
	}
}

func TestHubCommandRegistryExposesLiveSessionCycling(t *testing.T) {
	for _, name := range []string{"next-live-session", "previous-live-session"} {
		cmd, ok := hubCommandByName(name)
		if !ok {
			t.Fatalf("no /%s command in the registry", name)
		}
		if cmd.Scopes&hubCommandSession == 0 {
			t.Fatalf("/%s must be session-scoped", name)
		}
		available, reason := hubCommandAvailable(cmd, hubCommandContext{mode: hubModeSession})
		if !available {
			t.Fatalf("/%s unavailable in session mode: %s", name, reason)
		}
	}
}
