package agent

import "primeradiant.com/serf/agent/events"

// This file re-exports the public session-event cluster, which now lives in
// package primeradiant.com/serf/agent/events, back into package agent. The
// type aliases preserve type identity and method sets, so every in-package
// reference (the ~60 emit sites, the strategy seams) and every external
// agent.XData reference keeps compiling against the moved definitions.
//
// The alias and const lists are exhaustive by construction: one alias per
// payload type and one const per EventKind. A missing entry would break a
// consumer at compile time, so this file is the single source that keeps the
// agent surface identical to before the carve.

// Core envelope and taxonomy.
type (
	// SessionEvent re-exports events.SessionEvent.
	SessionEvent = events.SessionEvent
	// EventKind re-exports events.EventKind.
	EventKind = events.EventKind
)

// Payload structs and their shared helper types.
type (
	SessionStartData        = events.SessionStartData
	SessionEndData          = events.SessionEndData
	UserInputData           = events.UserInputData
	UserInputImage          = events.UserInputImage
	AssistantTextStartData  = events.AssistantTextStartData
	AssistantTextDeltaData  = events.AssistantTextDeltaData
	AssistantTextEndData    = events.AssistantTextEndData
	ToolCallStartData       = events.ToolCallStartData
	ToolCallOutputDeltaData = events.ToolCallOutputDeltaData
	ToolCallEndData         = events.ToolCallEndData
	SteeringInjectedData    = events.SteeringInjectedData
	QueueChangedData        = events.QueueChangedData
	TurnLimitData           = events.TurnLimitData
	LoopDetectionData       = events.LoopDetectionData
	CommunicateData         = events.CommunicateData
	SkillActivatedData      = events.SkillActivatedData
	ContextCompactionData   = events.ContextCompactionData
	CompactionTurnData      = events.CompactionTurnData
	WarningData             = events.WarningData
	ErrorData               = events.ErrorData
	ErrorCause              = events.ErrorCause
	SubagentStartData       = events.SubagentStartData
	SubagentEndData         = events.SubagentEndData
	PluginLoadedData        = events.PluginLoadedData
	HookStartData           = events.HookStartData
	HookEndData             = events.HookEndData
	ForkSummaryData         = events.ForkSummaryData
	PromptLoadedData        = events.PromptLoadedData
	RoundTimings            = events.RoundTimings
)

// EventKind constants, one per kind defined in package events.
const (
	EventSessionStart        = events.EventSessionStart
	EventSessionEnd          = events.EventSessionEnd
	EventUserInput           = events.EventUserInput
	EventAssistantTextStart  = events.EventAssistantTextStart
	EventAssistantTextDelta  = events.EventAssistantTextDelta
	EventAssistantTextEnd    = events.EventAssistantTextEnd
	EventToolCallStart       = events.EventToolCallStart
	EventToolCallOutputDelta = events.EventToolCallOutputDelta
	EventToolCallEnd         = events.EventToolCallEnd
	EventSteeringInjected    = events.EventSteeringInjected
	EventQueueChanged        = events.EventQueueChanged
	EventTurnLimit           = events.EventTurnLimit
	EventLoopDetection       = events.EventLoopDetection
	EventCommunicate         = events.EventCommunicate
	EventSkillActivated      = events.EventSkillActivated
	EventContextCompaction   = events.EventContextCompaction
	EventCompactionTurn      = events.EventCompactionTurn
	EventWarning             = events.EventWarning
	EventError               = events.EventError
	EventSubagentStart       = events.EventSubagentStart
	EventSubagentEnd         = events.EventSubagentEnd
	EventPluginLoaded        = events.EventPluginLoaded
	EventHookStart           = events.EventHookStart
	EventHookEnd             = events.EventHookEnd
	EventForkSummary         = events.EventForkSummary
	EventPromptLoaded        = events.EventPromptLoaded
	EventRoundTimings        = events.EventRoundTimings
)
