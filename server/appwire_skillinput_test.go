package server

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestTurnMutationsConsumeSkillInputWhenWired pins the runtime half of the
// canonical skill input contract now that per-endpoint consumption is wired:
// each of the four input-bearing turn mutations passes a canonical skill
// selection through to its retry-safe seam instead of answering an
// unsupported-input error, and the advertised ThreadCapabilities.SkillInput
// is true exactly when the daemon wired the seams that consume selections.
func TestTurnMutationsConsumeSkillInputWhenWired(t *testing.T) {
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

	assertConsumed := func(t *testing.T, method string, err error, reached bool) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s with skill input error = %T %v, want the seam to consume it", method, err, err)
		}
		if !reached {
			t.Fatalf("%s seam never ran for a skill-only selection", method)
		}
	}

	t.Run("turn/start", func(t *testing.T) {
		_, err := srv.handleAppTurnStart(context.Background(), appwire.TurnStartParams{
			Ref:                "local:th_1",
			ClientMutationID:   "skill-start",
			ExpectedInstanceID: "th_1",
			Input:              skillSelection,
		})
		assertConsumed(t, appwire.MethodTurnStart, err, called["start"])
	})
	t.Run("turn/steer", func(t *testing.T) {
		_, err := srv.handleAppTurnSteer(context.Background(), appwire.TurnSteerParams{
			Ref:                "local:th_1",
			ClientMutationID:   "skill-steer",
			ExpectedInstanceID: "th_1",
			Input:              skillSelection,
		})
		assertConsumed(t, appwire.MethodTurnSteer, err, called["steer"])
	})
	t.Run("turn/queue", func(t *testing.T) {
		_, err := srv.handleAppTurnQueue(context.Background(), appwire.TurnQueueParams{
			Ref:                "local:th_1",
			ClientMutationID:   "skill-queue",
			ExpectedInstanceID: "th_1",
			Input:              skillSelection,
		})
		assertConsumed(t, appwire.MethodTurnQueue, err, called["queue"])
	})
	t.Run("turn/drainAsSteer", func(t *testing.T) {
		_, err := srv.handleAppTurnDrainAsSteer(context.Background(), appwire.TurnDrainAsSteerParams{
			Ref:                   "local:th_1",
			ClientMutationID:      "skill-drain",
			ExpectedInstanceID:    "th_1",
			ExpectedQueueRevision: 1,
			Input:                 skillSelection,
		})
		assertConsumed(t, appwire.MethodTurnDrainAsSteer, err, called["drain"])
	})

	if caps := srv.appCapabilities("idle", false); !caps.SkillInput {
		t.Fatal("advertised ThreadCapabilities.SkillInput is false with all input-bearing turn mutations wired")
	}

	// A server that wired none of the input-bearing seams has nothing that
	// consumes a selection, so it must not advertise the capability.
	bare := NewServer(ServerConfig{})
	bare.SetAppIdentity("local", "th_2")
	if caps := bare.appCapabilities("idle", false); caps.SkillInput {
		t.Fatal("advertised ThreadCapabilities.SkillInput is true with no input-bearing turn mutations wired")
	}
	// Partial wiring is not a consumable input surface either: the thread
	// advertises skill input only when every input-bearing endpoint consumes
	// selections, so a client reading the capability can trust every one of
	// them.
	partial := NewServer(ServerConfig{})
	partial.SetAppIdentity("local", "th_3")
	partial.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Queue: func(appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
			return appwire.TurnQueueResponse{}, nil
		},
	})
	if caps := partial.appCapabilities("idle", false); caps.SkillInput {
		t.Fatal("advertised ThreadCapabilities.SkillInput is true with only part of the input surface wired")
	}
}

// TestSkillInputValidationMatchesAdvertisedCapability pins the per-endpoint
// truth the literal-true gate broke: every input-bearing turn mutation
// validates skill selections against its OWN seam's wiring, so no endpoint
// ever accepts a selection it cannot consume. A partially wired server must
// not advertise the capability (the conjunction stays false) — its wired
// endpoint still consumes selections at the claim, while its unwired
// endpoints refuse them at validation with the typed unsupported-input error
// instead of the bare Unavailable a pass-through gate used to leave. The
// fully wired server keeps consuming selections at the claim on all four
// (TestTurnMutationsConsumeSkillInputWhenWired above).
func TestSkillInputValidationMatchesAdvertisedCapability(t *testing.T) {
	partial := NewServer(ServerConfig{})
	partial.SetAppIdentity("local", "th_3")
	queueReached := false
	partial.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Queue: func(appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
			queueReached = true
			return appwire.TurnQueueResponse{}, nil
		},
	})
	if caps := partial.appCapabilities("idle", false); caps.SkillInput {
		t.Fatal("advertised ThreadCapabilities.SkillInput is true with only part of the input surface wired")
	}
	skillSelection := []appwire.InputItem{{Type: "skill", Name: "pkg:probe"}}

	// The WIRED endpoint keeps consuming selections at the claim even while
	// the thread-level capability is unadvertised: per-endpoint truth, not
	// the literal true that also spoke for the unwired siblings.
	_, err := partial.handleAppTurnQueue(context.Background(), appwire.TurnQueueParams{
		Ref:                "local:th_3",
		ClientMutationID:   "skill-queue-partial",
		ExpectedInstanceID: "th_3",
		Input:              skillSelection,
	})
	if err != nil {
		t.Fatalf("turn/queue skill input error = %T %v, want the wired seam to consume it", err, err)
	}
	if !queueReached {
		t.Fatal("turn/queue seam never ran for a skill-only selection on the wired endpoint")
	}

	// An UNWIRED endpoint answers the typed unsupported-input error at the
	// gate, instead of passing the selection through to a bare Unavailable
	// that would conflate "endpoint not wired" with "input unsupported" —
	// and worse, before the fix, the literal true asserted support the
	// endpoint never had.
	_, err = partial.handleAppTurnStart(context.Background(), appwire.TurnStartParams{
		Ref:                "local:th_3",
		ClientMutationID:   "skill-start-partial",
		ExpectedInstanceID: "th_3",
		Input:              skillSelection,
	})
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("turn/start skill input error = %T %v, want InvalidParams, not the unwired seam's Unavailable", err, err)
	}
	if !strings.Contains(wire.Message, "skill input is unsupported") {
		t.Fatalf("turn/start skill input error message = %q, want the unsupported-input explanation", wire.Message)
	}

	// The gate only rejects SKILL items: an unwired endpoint keeps its bare
	// Unavailable for ordinary text, and a wired one consumes it.
	_, err = partial.handleAppTurnStart(context.Background(), appwire.TurnStartParams{
		Ref:                "local:th_3",
		ClientMutationID:   "text-start-partial",
		ExpectedInstanceID: "th_3",
		Input:              []appwire.InputItem{{Type: "text", Text: "plain text"}},
	})
	var unavailable appwire.WireError
	if !errors.As(err, &unavailable) || unavailable.Code != appwire.CodeUnavailable {
		t.Fatalf("turn/start text input error = %T %v, want the endpoint's own Unavailable", err, err)
	}
	queueReached = false
	_, err = partial.handleAppTurnQueue(context.Background(), appwire.TurnQueueParams{
		Ref:                "local:th_3",
		ClientMutationID:   "text-queue-partial",
		ExpectedInstanceID: "th_3",
		Input:              []appwire.InputItem{{Type: "text", Text: "plain text"}},
	})
	if err != nil {
		t.Fatalf("turn/queue text input error = %v, want the wired seam to consume it", err)
	}
	if !queueReached {
		t.Fatal("turn/queue seam never ran for ordinary text input")
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

// TestThreadReadCarriesSkillDiagnostics proves the Stage 1 discovery
// diagnostics survive the whole projection (agent DetailedStatus -> server
// DetailedStatus -> appwire EvenerDiagnostics): thread/read must surface
// SkillDiagnostics with category/name/source detail verbatim, not drop them
// at a conversion boundary.
func TestThreadReadCarriesSkillDiagnostics(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetStatus(StatusInfo{SessionID: "sess_1", State: "idle"})
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) {
		e.detailedStatus = DetailedStatus{
			Skills: []SkillInfo{{Name: "clean", Description: "a plain user skill", UserInvocable: true, Available: true}},
			SkillDiagnostics: []SkillDiagnosticInfo{{
				Category:    "collision",
				Name:        "selected",
				Source:      "/fixture/first/skills/probe/SKILL.md",
				OtherSource: "/fixture/second/skills/probe/SKILL.md",
				Message:     "higher-precedence skill replaces earlier source",
			}},
		}
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
	if diagnostics == nil || len(diagnostics.SkillDiagnostics) != 1 {
		t.Fatalf("wire skill diagnostics = %+v, want the one collision entry", diagnostics)
	}
	got := diagnostics.SkillDiagnostics[0]
	if got.Category != "collision" || got.Name != "selected" ||
		got.Source != "/fixture/first/skills/probe/SKILL.md" ||
		got.OtherSource != "/fixture/second/skills/probe/SKILL.md" ||
		got.Message != "higher-precedence skill replaces earlier source" {
		t.Fatalf("wire skill diagnostic = %+v, want the Stage 1 values verbatim", got)
	}
}
