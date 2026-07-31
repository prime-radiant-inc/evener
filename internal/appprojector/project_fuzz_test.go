package appprojector

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
)

// projectorCases pairs each SessionEvent kind with a constructor that decodes
// fuzzed bytes into that kind's concrete payload. Pairing keeps Kind consistent
// with the payload type so the projector's per-kind eventData[T] assertions
// succeed and the data-dependent branches (formatters, dedup, skill tracking)
// are actually reached rather than always landing on the zero value.
var projectorCases = []struct {
	kind  events.EventKind
	build func([]byte) events.EventData
}{
	{events.EventSessionStart, func(b []byte) events.EventData { var d events.SessionStartData; _ = json.Unmarshal(b, &d); return d }},
	{events.EventSessionEnd, func(b []byte) events.EventData { var d events.SessionEndData; _ = json.Unmarshal(b, &d); return d }},
	{events.EventUserInput, func(b []byte) events.EventData { var d events.UserInputData; _ = json.Unmarshal(b, &d); return d }},
	{events.EventAssistantTextStart, func(b []byte) events.EventData {
		var d events.AssistantTextStartData
		_ = json.Unmarshal(b, &d)
		return d
	}},
	{events.EventAssistantTextDelta, func(b []byte) events.EventData {
		var d events.AssistantTextDeltaData
		_ = json.Unmarshal(b, &d)
		return d
	}},
	{events.EventAssistantTextEnd, func(b []byte) events.EventData {
		var d events.AssistantTextEndData
		_ = json.Unmarshal(b, &d)
		return d
	}},
	{events.EventAssistantTextReset, func(b []byte) events.EventData {
		var d events.AssistantTextResetData
		_ = json.Unmarshal(b, &d)
		return d
	}},
	{events.EventReasoningSummaryDelta, func(b []byte) events.EventData {
		var d events.ReasoningSummaryDeltaData
		_ = json.Unmarshal(b, &d)
		return d
	}},
	{events.EventToolCallStart, func(b []byte) events.EventData { var d events.ToolCallStartData; _ = json.Unmarshal(b, &d); return d }},
	{events.EventToolCallOutputDelta, func(b []byte) events.EventData {
		var d events.ToolCallOutputDeltaData
		_ = json.Unmarshal(b, &d)
		return d
	}},
	{events.EventToolCallEnd, func(b []byte) events.EventData { var d events.ToolCallEndData; _ = json.Unmarshal(b, &d); return d }},
	{events.EventSteeringInjected, func(b []byte) events.EventData {
		var d events.SteeringInjectedData
		_ = json.Unmarshal(b, &d)
		return d
	}},
	{events.EventQueueChanged, func(b []byte) events.EventData { var d events.QueueChangedData; _ = json.Unmarshal(b, &d); return d }},
	{events.EventTurnLimit, func(b []byte) events.EventData { var d events.TurnLimitData; _ = json.Unmarshal(b, &d); return d }},
	{events.EventLoopDetection, func(b []byte) events.EventData { var d events.LoopDetectionData; _ = json.Unmarshal(b, &d); return d }},
	{events.EventCommunicate, func(b []byte) events.EventData { var d events.CommunicateData; _ = json.Unmarshal(b, &d); return d }},
	{events.EventSkillActivated, func(b []byte) events.EventData { var d events.SkillActivatedData; _ = json.Unmarshal(b, &d); return d }},
	{events.EventContextCompaction, func(b []byte) events.EventData {
		var d events.ContextCompactionData
		_ = json.Unmarshal(b, &d)
		return d
	}},
	{events.EventCompactionTurn, func(b []byte) events.EventData { var d events.CompactionTurnData; _ = json.Unmarshal(b, &d); return d }},
	{events.EventWarning, func(b []byte) events.EventData { var d events.WarningData; _ = json.Unmarshal(b, &d); return d }},
	{events.EventError, func(b []byte) events.EventData { var d events.ErrorData; _ = json.Unmarshal(b, &d); return d }},
	{events.EventJobStarted, func(b []byte) events.EventData { var d events.JobStartedData; _ = json.Unmarshal(b, &d); return d }},
	{events.EventJobFinished, func(b []byte) events.EventData { var d events.JobFinishedData; _ = json.Unmarshal(b, &d); return d }},
	{events.EventPluginLoaded, func(b []byte) events.EventData { var d events.PluginLoadedData; _ = json.Unmarshal(b, &d); return d }},
	{events.EventHookStart, func(b []byte) events.EventData { var d events.HookStartData; _ = json.Unmarshal(b, &d); return d }},
	{events.EventHookEnd, func(b []byte) events.EventData { var d events.HookEndData; _ = json.Unmarshal(b, &d); return d }},
	{events.EventForkSummary, func(b []byte) events.EventData { var d events.ForkSummaryData; _ = json.Unmarshal(b, &d); return d }},
	{events.EventPromptLoaded, func(b []byte) events.EventData { var d events.PromptLoadedData; _ = json.Unmarshal(b, &d); return d }},
	{events.EventRoundTimings, func(b []byte) events.EventData { var d events.RoundTimings; _ = json.Unmarshal(b, &d); return d }},
	{events.EventGoalContinuation, func(b []byte) events.EventData {
		var d events.GoalContinuationData
		_ = json.Unmarshal(b, &d)
		return d
	}},
	{events.EventGoalEnded, func(b []byte) events.EventData { var d events.GoalEndedData; _ = json.Unmarshal(b, &d); return d }},
}

// FuzzProject drives the real AppEventProjector.Project state machine over a
// fuzzed *sequence* of session events. The fuzz input is a compact script —
// repeated [kindIndex][payloadLen][payload] records — applied to one projector
// so the stateful transitions (turn open/close, ensureTurn/ensureAssistantItem,
// reasoning-item creation, skill-activation tracking, communicate dedup) are
// exercised, not just isolated single events. Each record's payload is decoded
// into the concrete payload type matching its kind. The oracle is floor "no
// panic" plus re-serializability of every emitted notification's params, since
// the projector's whole job is producing wire-bound AppWire notifications.
func FuzzProject(f *testing.F) {
	f.Add([]byte("\x00coverage-sweep"))
	f.Add([]byte{0, 2, '{', '}'})                   // session start
	f.Add([]byte{0, 0, 2, 2, '{', '}', 3, 0, 5, 0}) // start, user input, assistant start, end
	f.Add([]byte{2, 5, '{', '"', 'x', '"', '}'})    // user input, malformed payload
	f.Add([]byte{8, 0, 9, 0, 10, 0})                // tool call start/delta/end with empty payloads
	f.Add([]byte{16, 0, 30, 0})                     // skill activated, goal ended

	f.Fuzz(func(t *testing.T, script []byte) {
		p := NewAppEventProjector("thread-fuzz", "ref-fuzz")
		if bytes.Equal(script, []byte("\x00coverage-sweep")) {
			projectCoverageSweep(t, p)
			return
		}

		const maxRecords = 96
		i := 0
		for records := 0; i < len(script) && records < maxRecords; records++ {
			kindIdx := int(script[i])
			i++
			if i >= len(script) {
				// Trailing kind byte with no length: apply with empty payload.
				applyEvent(t, p, kindIdx, nil, records)
				break
			}
			n := int(script[i])
			i++
			end := min(i+n, len(script))
			payload := script[i:end]
			i = end
			applyEvent(t, p, kindIdx, payload, records)
		}
	})
}

func projectCoverageSweep(t *testing.T, p *AppEventProjector) {
	t.Helper()
	projectRegressionTests := []struct {
		name string
		run  func(*testing.T)
	}{
		{"assistant_delta", TestAppEventProjectorProjectsAssistantDelta},
		{"user_input_index", TestAppEventProjectorCarriesUserInputTranscriptEntryIndex},
		{"task_updated", TestProject_TaskUpdated},
		{"job_started_only", TestProject_JobStartedIsTheOnlyStartNotification},
		{"job_finished_only", TestProject_JobFinishedIsTheOnlyFinishNotification},
		{"sandbox_escalation", TestProject_SandboxEscalationRequested},
		{"sandbox_transcript", TestProject_SandboxEscalationNotInTranscript},
		{"turn_started_at", TestAppEventProjectorTurnStartedCarriesStartedAt},
		{"turn_zero_time", TestAppEventProjectorTurnStartedZeroTimestampOmitsStartedAt},
		{"reasoning_delta", TestAppEventProjectorProjectsReasoningDelta},
		{"lifecycle_json", TestAppEventProjectorJSONUsesCodexLifecycleShape},
		{"user_images", TestAppEventProjectorCarriesUserInputImages},
		{"output_images", TestAppEventProjectorCarriesToolCallOutputImages},
		{"queued_input", TestAppEventProjectorCompletesActiveTurnBeforeQueuedUserInput},
		{"goal_continuation", TestAppEventProjectorGoalContinuationOpensNonUserTurn},
		{"goal_prior_turn", TestAppEventProjectorGoalContinuationCompletesActivePriorTurn},
		{"goal_ended", TestAppEventProjectorGoalEndedRendersSystemAnnouncement},
		{"thread_lifecycle", TestAppEventProjectorProjectsThreadLifecycle},
		{"restored_session", TestAppEventProjectorRestoredSessionStartCarriesAwaitingState},
		{"session_end", TestAppEventProjectorCompletesTurnOnSessionEnd},
		{"awaiting_end", TestAppEventProjectorMapsAwaitingSessionEnd},
		{"interrupted", TestAppEventProjectorMarksInterruptedTurnCanceled},
		{"canceled_error", TestAppEventProjectorLetsInterruptedSessionEndCancelAfterContextCanceledError},
		{"turn_timing", TestProjectorTurnEndedStampsTiming},
		{"interrupt_status", TestProjectorTurnEndedPreservesInterruptStatus},
		{"usage_rounds", TestProjectorAccumulatesPerTurnUsageAcrossRounds},
		{"usage_reset", TestProjectorNewTurnResetsUsageAccumulator},
		{"tool_after_text", TestAppEventProjectorKeepsToolEventsInActiveTurnAfterAssistantText},
		{"tool_description", TestAppEventProjectorCarriesToolDescription},
		{"communicate", TestAppEventProjectorProjectsCommunicateAsAssistantMessage},
		{"communicate_suppressed", TestAppEventProjectorSuppressesCommunicateToolEvents},
		{"output_call_id", TestAppEventProjectorIncludesCallIDOnToolOutputDelta},
		{"jobs", TestAppEventProjectorProjectsJobEvents},
		{"queue", TestAppEventProjectorProjectsQueueChanged},
		{"steering", TestAppEventProjectorProjectsSteeringInjected},
		{"compaction_turn", TestAppEventProjectorProjectsCompactionTurn},
		{"active_compaction", TestAppEventProjectorProjectsCompactionTurnInActiveTurn},
		{"skill_before_end", TestAppEventProjectorGroupsSkillActivationBeforeUseSkillEnd},
		{"skill_group", TestAppEventProjectorGroupsSkillActivationWithUseSkill},
		{"skill_unmatched", TestAppEventProjectorLeavesUnmatchedSkillActivationStandalone},
		{"skill_legacy", TestAppEventProjectorGroupsSkillActivationWithLegacyUseSkillNameArg},
		{"skill_text_boundary", TestAppEventProjectorDoesNotInferSkillActivationAcrossAssistantText},
		{"agent_announcements", TestAppEventProjectorProjectsAgentOnlyEventsAsSystemAnnouncements},
		{"compaction_numbers", TestAppEventProjectorContextCompactionCarriesStructuredNumbers},
		{"hook_start", TestAppEventProjectorDoesNotDisplayHookStart},
		{"active_announcement", TestAppEventProjectorProjectsAgentOnlyAnnouncementInActiveTurn},
		{"image_steering", TestAppEventProjectorProjectsImageOnlySteeringInjected},
		{"provider_cause", TestProjector_ForwardsProviderCause},
		{"absent_cause", TestProjector_OmitsCauseWhenAbsent},
		{"legacy_error", TestProjector_BackcompatNonProviderError},
		{"diagnostic", TestProjector_GenuineErrorEmitsSingleDiagnostic},
		{"assistant_reset", TestProjector_AssistantTextResetDiscardsInProgressItem},
	}
	for _, regression := range projectRegressionTests {
		t.Run(regression.name, regression.run)
	}

	project := func(kind events.EventKind, data events.EventData) {
		t.Helper()
		for _, n := range p.Project(events.SessionEvent{Kind: kind, SessionID: "thread-fuzz", Data: data}) {
			if _, err := json.Marshal(n.Params); err != nil {
				t.Fatalf("%s notification params failed to marshal: %v", kind, err)
			}
		}
	}

	// Fill a projector initialized without a thread ID and cover the explicit idle state.
	empty := NewAppEventProjector("", "ref-fuzz")
	empty.Project(events.SessionEvent{Kind: events.EventSessionStart, SessionID: "derived", Data: events.SessionStartData{State: "idle"}})
	empty.Project(events.SessionEvent{Kind: events.EventKind("unknown")})

	reserved := p.ReserveTurnID()
	if p.ReserveTurnID() != reserved || p.ActiveTurnID() != reserved {
		t.Fatal("reserved turn ID was not stable")
	}
	p.ReleaseReservedTurnID("different")
	project(events.EventUserInput, events.UserInputData{Text: "start reserved turn"})
	if p.ActiveTurnID() != reserved {
		t.Fatal("reserved turn ID was not adopted")
	}
	released := p.ReserveTurnID()
	p.ReleaseReservedTurnID(released)

	project(events.EventAssistantTextEnd, events.AssistantTextEndData{Text: "same"})
	project(events.EventCommunicate, events.CommunicateData{Message: "same"})
	project(events.EventReasoningSummaryDelta, events.ReasoningSummaryDeltaData{Delta: "one"})
	project(events.EventReasoningSummaryDelta, events.ReasoningSummaryDeltaData{Delta: "two"})
	project(events.EventTurnEnded, events.TurnEndedData{})
	project(events.EventSessionEnd, events.SessionEndData{State: "idle"})
	project(events.EventTurnEnded, events.TurnEndedData{})

	project(events.EventWarning, events.WarningData{})
	project(events.EventError, events.ErrorData{})
	project(events.EventSessionNameChanged, events.SessionNameChangedData{})
	project(events.EventToolCallRepaired, events.ToolCallRepairedData{})
	project(events.EventToolCallEnd, events.ToolCallEndData{OutputImages: []events.OutputImage{{}, {SHA: "sha"}}})

	// Exercise fallback and zero-value announcement forms through their real events.
	project(events.EventTurnLimit, events.TurnLimitData{})
	project(events.EventContextCompaction, events.ContextCompactionData{})
	project(events.EventPluginLoaded, events.PluginLoadedData{})
	project(events.EventHookEnd, events.HookEndData{})
	project(events.EventForkSummary, events.ForkSummaryData{})
	project(events.EventPromptLoaded, events.PromptLoadedData{})

	project(events.EventToolCallStart, events.ToolCallStartData{ToolName: "use_skill", ArgumentsJSON: "{"})
	project(events.EventToolCallEnd, events.ToolCallEndData{ToolName: "use_skill"})
	project(events.EventToolCallStart, events.ToolCallStartData{ToolName: "use_skill", ArgumentsJSON: `{}`})

	oldMarshal := marshalContextCompaction
	defer func() { marshalContextCompaction = oldMarshal }()
	marshalContextCompaction = func(any) ([]byte, error) { return nil, errors.New("injected marshal failure") }
	if raw := contextCompactionRaw(events.ContextCompactionData{Layer: "layer"}); raw != nil {
		t.Fatalf("marshal failure returned raw payload: %s", raw)
	}
}

func applyEvent(t *testing.T, p *AppEventProjector, kindIdx int, payload []byte, record int) {
	t.Helper()
	idx := kindIdx % len(projectorCases)
	c := projectorCases[idx]

	// Alternate the timestamp between zero and a fixed instant so both the
	// "StartedAt unset" and "StartedAt set" turn-projection branches are hit,
	// deterministically (no time.Now).
	var ts time.Time
	if record%2 == 1 {
		ts = time.Unix(1_700_000_000, 0).UTC()
	}

	event := events.SessionEvent{
		Kind:      c.kind,
		Timestamp: ts,
		SessionID: "thread-fuzz",
		Data:      c.build(payload),
	}
	for _, n := range p.Project(event) {
		if _, err := json.Marshal(n.Params); err != nil {
			t.Fatalf("notification params failed to marshal: %v\n kind=%s\n payload=%q", err, c.kind, payload)
		}
	}
}
