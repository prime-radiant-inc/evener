package server

import (
	"context"
	"errors"
	"slices"
	"sort"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestTurnMutationsRejectSkillInputUntilWired pins the runtime half of the
// canonical skill input contract: the daemon decodes and normalizes canonical
// skill selections, but no mutation endpoint consumes them yet, so each
// input-bearing mutation answers an unsupported-input invalidParams error
// without ever reaching the retry-safe seam, and the advertised
// ThreadCapabilities keeps SkillInput false until per-endpoint consumption is
// wired.
func TestTurnMutationsRejectSkillInputUntilWired(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	called := map[string]bool{}
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Start: func(appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			called["start"] = true
			return appwire.TurnStartResponse{}, nil
		},
		Steer: func(appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
			called["steer"] = true
			return appwire.TurnSteerResponse{}, nil
		},
		Queue: func(appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
			called["queue"] = true
			return appwire.TurnQueueResponse{}, nil
		},
		Drain: func(appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
			called["drain"] = true
			return appwire.TurnDrainAsSteerResponse{}, nil
		},
	})
	skillSelection := []appwire.InputItem{{Type: "skill", Name: "pkg:probe"}}

	assertUnsupported := func(t *testing.T, method string, err error) {
		t.Helper()
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
			t.Fatalf("%s with skill input error = %T %v, want invalidParams", method, err, err)
		}
		want := appwire.ValidateSkillInputSupport(skillSelection, false)
		if err.Error() != want.Error() {
			t.Fatalf("%s error is not the skill-unsupported verdict: got %q want %q", method, err, want)
		}
	}

	t.Run("turn/start", func(t *testing.T) {
		_, err := srv.handleAppTurnStart(context.Background(), appwire.TurnStartParams{
			Ref:                "local:th_1",
			ClientMutationID:   "skill-start",
			ExpectedInstanceID: "th_1",
			Input:              skillSelection,
		})
		assertUnsupported(t, appwire.MethodTurnStart, err)
	})
	t.Run("turn/steer", func(t *testing.T) {
		_, err := srv.handleAppTurnSteer(context.Background(), appwire.TurnSteerParams{
			Ref:                "local:th_1",
			ClientMutationID:   "skill-steer",
			ExpectedInstanceID: "th_1",
			Input:              skillSelection,
		})
		assertUnsupported(t, appwire.MethodTurnSteer, err)
	})
	t.Run("turn/queue", func(t *testing.T) {
		_, err := srv.handleAppTurnQueue(context.Background(), appwire.TurnQueueParams{
			Ref:                "local:th_1",
			ClientMutationID:   "skill-queue",
			ExpectedInstanceID: "th_1",
			Input:              skillSelection,
		})
		assertUnsupported(t, appwire.MethodTurnQueue, err)
	})
	t.Run("turn/drainAsSteer", func(t *testing.T) {
		_, err := srv.handleAppTurnDrainAsSteer(context.Background(), appwire.TurnDrainAsSteerParams{
			Ref:                   "local:th_1",
			ClientMutationID:      "skill-drain",
			ExpectedInstanceID:    "th_1",
			ExpectedQueueRevision: 1,
			Input:                 skillSelection,
		})
		assertUnsupported(t, appwire.MethodTurnDrainAsSteer, err)
	})

	for method, invoked := range called {
		if invoked {
			t.Fatalf("%s seam ran for a skill-only selection", method)
		}
	}
	if caps := srv.appCapabilities("idle", false); caps.SkillInput {
		t.Fatal("advertised ThreadCapabilities.SkillInput is true before runtime consumption is wired")
	}
}

// TestThreadReadAdvertisesRealSkillControls proves the daemon-sourced wire
// keeps the Stage 1 invocation controls through the whole projection: a
// server-side skill inventory with real control values reaches
// EvenerDiagnostics.Skills on thread/read with those values intact, so
// completion (Available && UserInvocable) can trust the wire instead of
// reading placeholder false booleans.
func TestThreadReadAdvertisesRealSkillControls(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetStatus(StatusInfo{SessionID: "sess_1", State: "idle"})
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) {
		e.detailedStatus = DetailedStatus{Skills: []SkillInfo{
			{Name: "clean", Description: "a plain user skill", UserInvocable: true, Available: true, AllowedTools: []string{"read_file", "grep"}},
			{Name: "hidden", Description: "hidden from the model", DisableModelInvocation: true, UserInvocable: true, Available: true},
			{Name: "stale", Description: "no longer readable", UserInvocable: false, Available: false},
		}}
	})

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1"}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	data, ok := resp.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("result=%T", resp.Response.Result)
	}
	diagnostics := data.Thread.Evener.Diagnostics
	if diagnostics == nil || len(diagnostics.Skills) != 3 {
		t.Fatalf("diagnostics skills = %+v, want 3 entries", diagnostics)
	}
	byName := map[string]appwire.EvenerSkillInfo{}
	for _, s := range diagnostics.Skills {
		byName[s.Name] = s
	}
	clean := byName["clean"]
	if !clean.Available || !clean.UserInvocable || clean.DisableModelInvocation ||
		len(clean.AllowedTools) != 2 || clean.AllowedTools[0] != "read_file" || clean.AllowedTools[1] != "grep" {
		t.Fatalf("clean wire skill = %+v, want available user-invocable with [read_file grep]", clean)
	}
	hidden := byName["hidden"]
	if !hidden.DisableModelInvocation || !hidden.UserInvocable || !hidden.Available {
		t.Fatalf("hidden wire skill = %+v, want disable-model-invocation with user-invocable available", hidden)
	}
	// The completion predicate over the WIRE values: Available && UserInvocable
	// keeps the user-invocable skills and excludes the unavailable one. A
	// projection that dropped the controls would collapse every skill to
	// false/false/false and empty the completion set.
	var kept []string
	for _, s := range diagnostics.Skills {
		if s.Available && s.UserInvocable {
			kept = append(kept, s.Name)
		}
	}
	sort.Strings(kept)
	if !slices.Equal(kept, []string{"clean", "hidden"}) {
		t.Fatalf("wire completion predicate kept = %v, want [clean hidden]", kept)
	}
}
