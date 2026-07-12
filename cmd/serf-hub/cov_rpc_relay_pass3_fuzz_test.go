package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// FuzzRPCRelayPass3 replays lifecycle requests through the real hub router. The
// source is the package's external-boundary fake; no Serf internals are mocked.
func FuzzRPCRelayPass3(f *testing.F) {
	f.Add(uint8(0))
	f.Add(uint8(1))
	f.Fuzz(func(t *testing.T, variant uint8) {
		ctx := context.Background()
		caps := appwire.ThreadCapabilities{
			Send: true, Steer: true, Interrupt: true, Compact: true, Clear: true,
			ForkFromTurn: true, Shutdown: true, ChangeModel: true, Rename: true,
			Queue: true, Goal: true,
		}
		thread := appwire.Thread{
			ID: "thread", SessionID: "session", Source: "remote", Name: "name",
			Preview: "preview", CWD: "/work", Path: "/work/file", ModelProvider: "provider",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
			Serf:   appwire.SerfThread{Ref: "remote:thread", Profile: "profile", Capabilities: caps},
			Turns:  []appwire.Turn{{ID: "turn_1"}, {ID: "turn_2"}},
		}
		source := &scriptedAppSource{id: "remote", thread: thread}
		registry := appsource.NewRegistry()
		registry.Add(source)
		server := newHubAppServer(hubcore.WebConfig{}, registry)

		dispatch := func(method string, params any) {
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = server.Router().Dispatch(ctx, appwire.Request{ID: appwire.NewIntID(1), Method: method, Params: raw})
		}
		dispatch(appwire.MethodThreadList, appwire.ThreadListParams{})
		dispatch(appwire.MethodThreadList, appwire.ThreadListParams{SourceIDs: []string{"remote"}, Statuses: []string{"active"}, SearchTerm: "name", Limit: 1})
		dispatch(appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "remote:thread", IncludeTurns: true, TurnLimit: 1})
		dispatch(appwire.MethodThreadTurnsList, appwire.ThreadTurnsListParams{Ref: "remote:thread", Limit: 1})
		dispatch(appwire.MethodSerfSubagentPreview, appwire.SerfSubagentPreviewParams{})
		dispatch(appwire.MethodSerfSubagentPreview, appwire.SerfSubagentPreviewParams{Ref: "remote:thread", Limit: 1})
		dispatch(appwire.MethodThreadStart, appwire.ThreadStartParams{Harness: "remote"})
		dispatch(appwire.MethodThreadResume, appwire.ThreadResumeParams{Ref: "remote:thread"})
		dispatch(appwire.MethodThreadFork, appwire.ThreadForkParams{Ref: "remote:thread"})
		dispatch(appwire.MethodTurnStart, appwire.TurnStartParams{Ref: "remote:thread"})
		dispatch(appwire.MethodTurnSteer, appwire.TurnSteerParams{Ref: "remote:thread"})
		dispatch(appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{Ref: "remote:thread"})
		dispatch(appwire.MethodSerfSandboxEscalationResolve, appwire.SandboxEscalationResolveParams{Ref: "remote:thread"})
		dispatch(appwire.MethodTurnQueue, appwire.TurnQueueParams{Ref: "remote:thread"})
		dispatch(appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{Ref: "remote:thread"})
		dispatch(appwire.MethodThreadClear, appwire.ThreadClearParams{Ref: "remote:thread"})
		dispatch(appwire.MethodThreadCompactStart, appwire.ThreadCompactStartParams{Ref: "remote:thread"})
		dispatch(appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: "remote:thread"})
		dispatch(appwire.MethodThreadModelSet, appwire.ThreadModelSetParams{Ref: "remote:thread"})
		dispatch(appwire.MethodSerfThreadNameSet, appwire.ThreadNameSetParams{Ref: "remote:thread"})
		dispatch(appwire.MethodThreadReasoningEffortSet, appwire.ThreadReasoningEffortSetParams{Ref: "remote:thread"})
		dispatch(appwire.MethodGoalSet, appwire.GoalSetParams{Ref: "remote:thread"})
		dispatch(appwire.MethodSerfThreadTranscriptsList, appwire.ThreadTranscriptListParams{Ref: "remote:thread"})

		for _, action := range []string{"send", "steer", "interrupt", "compact", "clear", "fork", "shutdown", "model", "rename", "queue", "goal", "unknown"} {
			_ = threadActionAvailable(caps, action)
		}
		for _, status := range []string{"active", "notloaded", "systemerror", " IDLE "} {
			_ = normalizeThreadListStatusFilter(status)
		}
		for _, p := range []appwire.ThreadListParams{
			{}, {Statuses: []string{"idle"}}, {SourceIDs: []string{"remote"}},
			{SourceIDs: []string{"other"}}, {SearchTerm: "missing"}, {SearchTerm: "profile"},
		} {
			_ = appThreadMatches(thread, p)
		}
		_ = threadListSourceID("fallback", appwire.Thread{})
		_ = threadListSourceID("fallback", appwire.Thread{Serf: appwire.SerfThread{Ref: "parsed:id"}})
		_ = threadListSourceID("fallback", appwire.Thread{Source: "explicit"})
		_ = sourceAllowedForList("remote", appwire.ThreadListParams{})
		_ = sourceAllowedForList("remote", appwire.ThreadListParams{SourceIDs: []string{"x", "remote"}})
		_ = sourceExplicitlyRequestedForList("remote", appwire.ThreadListParams{SourceIDs: []string{"x", "remote"}})

		_ = relayOnThreadRead(source)
		_, _ = sourceForThread(registry, "remote:thread", "")
		_, _ = sourceForThread(registry, "", "")
		_, _ = managedLaunchSourceIDForRef(hubcore.WebConfig{}, "remote:thread")
		_ = hubKnowsRef(hubcore.WebConfig{}, "remote:thread")
		_ = isSessionUnavailableError(nil)
		_ = isSessionUnavailableError(errors.New("plain"))
		_ = isSessionUnavailableError(appwire.SessionUnavailable("gone"))
		_ = launchSourceID(appwire.ThreadStartParams{})
		_ = launchSourceID(appwire.ThreadStartParams{Harness: "serf"})
		_ = launchSourceID(appwire.ThreadStartParams{Harness: "remote"})
		for _, raw := range []string{"", "turn_0", "turn_bad", "turn_2", " 3 "} {
			_, _ = parseSourceTurnID(raw)
		}
		_ = threadForkRequiresTurnCapability(appwire.ThreadForkParams{})
		_ = threadForkRequiresTurnCapability(appwire.ThreadForkParams{Label: "x"})
		_ = threadRef(appwire.Thread{})
		_ = threadRef(appwire.Thread{Serf: appwire.SerfThread{Ref: "remote:x"}})
		_ = threadRef(appwire.Thread{Source: "remote", SessionID: "x"})
		_ = transcriptTargetSource("remote:x", "fallback")
		_ = transcriptTargetSource("bad ref", "fallback")
		if variant&1 != 0 {
			_ = mergePastMetadataForList(hubcore.WebConfig{}, "remote", thread)
		}
	})
}
