package main

import (
	"context"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appserver"
)

// TestSendHubGoalSetsObjective verifies sendHubGoal forwards the objective on
// goal/set and reports the daemon's Started flag.
func TestSendHubGoalSetsObjective(t *testing.T) {
	app := appserver.NewServer(appserver.ServerConfig{
		ServerName: "hub",
		SourceID:   "local",
		Features:   appwire.FeatureSet{},
	})
	var got appwire.GoalSetParams
	appserver.HandleTyped(app.Router(), appwire.MethodGoalSet, func(_ context.Context, params appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
		got = params
		return appwire.GoalSetResponse{Started: true}, nil
	})
	client, cleanup := newTUIAppWireClient(t, app)
	defer cleanup()

	msg := sendHubGoal(client, appwire.Ref{SourceID: "local", ThreadID: "th_1"}, "improve coverage")()
	goalMsg, ok := msg.(hubGoalMsg)
	if !ok || goalMsg.err != nil {
		t.Fatalf("msg=%T err=%v", msg, goalMsg.err)
	}
	if got.Ref != "local:th_1" || got.Objective != "improve coverage" {
		t.Fatalf("params=%+v, want ref=local:th_1 objective=improve coverage", got)
	}
	if goalMsg.cleared {
		t.Fatalf("cleared=%v, want false for a set", goalMsg.cleared)
	}
	if !goalMsg.started {
		t.Fatalf("started=%v, want true", goalMsg.started)
	}
}

// TestSendHubGoalClears verifies an empty objective drives the clear path:
// goal/set with an empty objective and hubGoalMsg.cleared set.
func TestSendHubGoalClears(t *testing.T) {
	app := appserver.NewServer(appserver.ServerConfig{
		ServerName: "hub",
		SourceID:   "local",
		Features:   appwire.FeatureSet{},
	})
	var got appwire.GoalSetParams
	called := false
	appserver.HandleTyped(app.Router(), appwire.MethodGoalSet, func(_ context.Context, params appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
		got = params
		called = true
		return appwire.GoalSetResponse{Started: false}, nil
	})
	client, cleanup := newTUIAppWireClient(t, app)
	defer cleanup()

	msg := sendHubGoal(client, appwire.Ref{SourceID: "local", ThreadID: "th_1"}, "")()
	goalMsg, ok := msg.(hubGoalMsg)
	if !ok || goalMsg.err != nil {
		t.Fatalf("msg=%T err=%v", msg, goalMsg.err)
	}
	if !called {
		t.Fatal("goal/set handler was not invoked for the clear path")
	}
	if got.Objective != "" {
		t.Fatalf("objective=%q, want empty for clear", got.Objective)
	}
	if !goalMsg.cleared {
		t.Fatalf("cleared=%v, want true", goalMsg.cleared)
	}
}

func TestHubGoalStatusText(t *testing.T) {
	if got := hubGoalStatusText(nil); got != "No goal set. Use /goal <objective> to set one." {
		t.Fatalf("nil goal status=%q", got)
	}
	got := hubGoalStatusText(&appwire.GoalState{Status: "active", Iterations: 2})
	if got != "Goal: active (2)" {
		t.Fatalf("status=%q, want Goal: active (2)", got)
	}
}
