package server

import (
	"context"
	"errors"
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
