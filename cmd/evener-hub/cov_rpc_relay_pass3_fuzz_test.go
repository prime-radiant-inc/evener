package hub

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// FuzzRPCRelayPass3 replays lifecycle requests through the real hub router. The
// source is the package's external-boundary fake; no Evener internals are mocked.
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
			Evener: appwire.EvenerThread{Ref: "remote:thread", Profile: "profile", Capabilities: caps},
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
		dispatch(appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "remote:thread", IncludeTurns: true, ItemLimit: 1})
		dispatch(appwire.MethodThreadTurnsList, appwire.ThreadTurnsListParams{Ref: "remote:thread", ItemLimit: 1})
		dispatch(appwire.MethodEvenerSubagentPreview, appwire.EvenerSubagentPreviewParams{})
		dispatch(appwire.MethodEvenerSubagentPreview, appwire.EvenerSubagentPreviewParams{Ref: "remote:thread", Limit: 1})
		dispatch(appwire.MethodThreadStart, appwire.ThreadStartParams{Harness: "remote"})
		dispatch(appwire.MethodThreadResume, appwire.ThreadResumeParams{Ref: "remote:thread"})
		dispatch(appwire.MethodThreadFork, appwire.ThreadForkParams{Ref: "remote:thread"})
		dispatch(appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "test-mutation", Ref: "remote:thread"})
		dispatch(appwire.MethodTurnSteer, appwire.TurnSteerParams{ClientMutationID: "test-mutation", Ref: "remote:thread"})
		dispatch(appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{ClientMutationID: "test-mutation", Ref: "remote:thread"})
		dispatch(appwire.MethodEvenerSandboxEscalationResolve, appwire.SandboxEscalationResolveParams{Ref: "remote:thread"})
		dispatch(appwire.MethodTurnQueue, appwire.TurnQueueParams{ClientMutationID: "test-mutation", Ref: "remote:thread"})
		dispatch(appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{ClientMutationID: "test-mutation", ExpectedQueueRevision: 0, Ref: "remote:thread"})
		dispatch(appwire.MethodThreadClear, appwire.ThreadClearParams{Ref: "remote:thread"})
		dispatch(appwire.MethodThreadCompactStart, appwire.ThreadCompactStartParams{Ref: "remote:thread"})
		dispatch(appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: "remote:thread"})
		dispatch(appwire.MethodThreadModelSet, appwire.ThreadModelSetParams{Ref: "remote:thread"})
		dispatch(appwire.MethodEvenerThreadNameSet, appwire.ThreadNameSetParams{Ref: "remote:thread"})
		dispatch(appwire.MethodThreadReasoningEffortSet, appwire.ThreadReasoningEffortSetParams{Ref: "remote:thread"})
		dispatch(appwire.MethodGoalSet, appwire.GoalSetParams{Ref: "remote:thread"})
		dispatch(appwire.MethodEvenerThreadTranscriptsList, appwire.ThreadTranscriptListParams{Ref: "remote:thread"})

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
		_ = threadListSourceID("fallback", appwire.Thread{Evener: appwire.EvenerThread{Ref: "parsed:id"}})
		_ = threadListSourceID("fallback", appwire.Thread{Source: "explicit"})
		_ = sourceAllowedForList("remote", appwire.ThreadListParams{})
		_ = sourceAllowedForList("remote", appwire.ThreadListParams{SourceIDs: []string{"x", "remote"}})
		_ = sourceExplicitlyRequestedForList("remote", appwire.ThreadListParams{SourceIDs: []string{"x", "remote"}})

		_ = relayOnThreadRead(source)
		_, _ = sourceForThread(registry, "remote:thread", "")
		_, _ = sourceForThread(registry, "", "")
		_ = hubKnowsRef(hubcore.WebConfig{}, "remote:thread")
		_ = isSessionUnavailableError(nil)
		_ = isSessionUnavailableError(errors.New("plain"))
		_ = isSessionUnavailableError(appwire.SessionUnavailable("gone"))
		_ = launchSourceID(appwire.ThreadStartParams{})
		_ = launchSourceID(appwire.ThreadStartParams{Harness: "evener"})
		_ = launchSourceID(appwire.ThreadStartParams{Harness: "remote"})
		for _, raw := range []string{"", "turn_0", "turn_bad", "turn_2", " 3 "} {
			_, _ = parseSourceTurnID(raw)
		}
		_ = threadForkRequiresTurnCapability(appwire.ThreadForkParams{})
		_ = threadForkRequiresTurnCapability(appwire.ThreadForkParams{Label: "x"})
		_ = threadRef(appwire.Thread{})
		_ = threadRef(appwire.Thread{Evener: appwire.EvenerThread{Ref: "remote:x"}})
		_ = threadRef(appwire.Thread{Source: "remote", SessionID: "x"})
		_ = transcriptTargetSource("remote:x", "fallback")
		_ = transcriptTargetSource("bad ref", "fallback")
		if variant&1 != 0 {
			_, _ = mergePastMetadataForList(context.Background(), hubcore.WebConfig{}, "remote", thread)
		}
	})
}
