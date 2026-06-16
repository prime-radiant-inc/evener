package events

import "time"

// EventData is the sealed set of payloads that may ride on a SessionEvent. The
// unexported eventKind method both (a) restricts the SessionEvent.Data field to
// this package's payload types — a foreign type cannot satisfy the interface —
// and (b) records which EventKind a payload belongs to, so the emit chokepoint
// can derive Kind from the payload instead of trusting a separately passed
// argument.
//
// What this guarantees and what it does NOT: the interface gives compile-checked
// payload VALIDITY (you cannot put a non-payload value on the stream). It does
// NOT give compile-time per-Kind checking. Kind is derived at runtime from
// data.eventKind(), so a hypothetical New(WarningData{}) emitted where an
// EventError was intended would compile and be silently tagged WARNING. Kind
// and payload can never disagree, but the choice of payload is not itself
// checked against caller intent.
type EventData interface {
	eventKind() EventKind
}

// New builds a SessionEvent for the given payload, owning the Kind (derived
// from the payload) and Timestamp. The caller sets SessionID afterward.
func New(data EventData) SessionEvent {
	return SessionEvent{
		Kind:      data.eventKind(),
		Timestamp: time.Now().UTC(),
		Data:      data,
	}
}

// Per-payload eventKind markers. Each binds a payload struct to its EventKind
// and, via the compile-time assertions below, to the EventData interface.

func (SessionStartData) eventKind() EventKind       { return EventSessionStart }
func (SessionEndData) eventKind() EventKind         { return EventSessionEnd }
func (UserInputData) eventKind() EventKind          { return EventUserInput }
func (AssistantTextStartData) eventKind() EventKind { return EventAssistantTextStart }
func (AssistantTextDeltaData) eventKind() EventKind { return EventAssistantTextDelta }
func (AssistantTextEndData) eventKind() EventKind   { return EventAssistantTextEnd }
func (AssistantTextResetData) eventKind() EventKind { return EventAssistantTextReset }
func (ReasoningSummaryDeltaData) eventKind() EventKind {
	return EventReasoningSummaryDelta
}
func (ToolCallStartData) eventKind() EventKind       { return EventToolCallStart }
func (ToolCallOutputDeltaData) eventKind() EventKind { return EventToolCallOutputDelta }
func (ToolCallEndData) eventKind() EventKind         { return EventToolCallEnd }
func (SteeringInjectedData) eventKind() EventKind    { return EventSteeringInjected }
func (QueueChangedData) eventKind() EventKind        { return EventQueueChanged }
func (TurnLimitData) eventKind() EventKind           { return EventTurnLimit }
func (LoopDetectionData) eventKind() EventKind       { return EventLoopDetection }
func (CommunicateData) eventKind() EventKind         { return EventCommunicate }
func (SkillActivatedData) eventKind() EventKind      { return EventSkillActivated }
func (ContextCompactionData) eventKind() EventKind   { return EventContextCompaction }
func (CompactionTurnData) eventKind() EventKind      { return EventCompactionTurn }
func (WarningData) eventKind() EventKind             { return EventWarning }
func (ErrorData) eventKind() EventKind               { return EventError }
func (JobStartedData) eventKind() EventKind          { return EventJobStarted }
func (JobFinishedData) eventKind() EventKind         { return EventJobFinished }
func (PluginLoadedData) eventKind() EventKind        { return EventPluginLoaded }
func (HookStartData) eventKind() EventKind           { return EventHookStart }
func (HookEndData) eventKind() EventKind             { return EventHookEnd }
func (ForkSummaryData) eventKind() EventKind         { return EventForkSummary }
func (PromptLoadedData) eventKind() EventKind        { return EventPromptLoaded }
func (RoundTimings) eventKind() EventKind            { return EventRoundTimings }
func (GoalContinuationData) eventKind() EventKind    { return EventGoalContinuation }
func (GoalEndedData) eventKind() EventKind           { return EventGoalEnded }

// Compile-time assertions that every payload satisfies EventData. A new payload
// added without a marker fails to build here.
var (
	_ EventData = SessionStartData{}
	_ EventData = SessionEndData{}
	_ EventData = UserInputData{}
	_ EventData = AssistantTextStartData{}
	_ EventData = AssistantTextDeltaData{}
	_ EventData = ReasoningSummaryDeltaData{}
	_ EventData = AssistantTextEndData{}
	_ EventData = ToolCallStartData{}
	_ EventData = ToolCallOutputDeltaData{}
	_ EventData = ToolCallEndData{}
	_ EventData = SteeringInjectedData{}
	_ EventData = QueueChangedData{}
	_ EventData = TurnLimitData{}
	_ EventData = LoopDetectionData{}
	_ EventData = CommunicateData{}
	_ EventData = SkillActivatedData{}
	_ EventData = ContextCompactionData{}
	_ EventData = CompactionTurnData{}
	_ EventData = WarningData{}
	_ EventData = ErrorData{}
	_ EventData = JobStartedData{}
	_ EventData = JobFinishedData{}
	_ EventData = PluginLoadedData{}
	_ EventData = HookStartData{}
	_ EventData = HookEndData{}
	_ EventData = ForkSummaryData{}
	_ EventData = PromptLoadedData{}
	_ EventData = RoundTimings{}
	_ EventData = GoalContinuationData{}
	_ EventData = GoalEndedData{}
)
