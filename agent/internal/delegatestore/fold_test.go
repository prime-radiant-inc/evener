package delegatestore

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/task"
)

func TestApplyAndFoldCloneCreatedDescriptor(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*Descriptor)
	}{
		{name: "task templates", mutate: func(descriptor *Descriptor) {
			descriptor.TaskTemplates[0] = task.TaskTemplate{Title: "mutated", Prompt: "mutated", ReasoningEffort: "low", Type: "fix", Insert: "mutated"}
		}},
		{name: "tool name ceiling", mutate: func(descriptor *Descriptor) { descriptor.ToolNameCeiling[0] = "mutated" }},
		{name: "frozen skill names", mutate: func(descriptor *Descriptor) { descriptor.FrozenSkillNames[0] = "mutated" }},
		{name: "frozen skill bodies", mutate: func(descriptor *Descriptor) { descriptor.FrozenSkillBodies[0] = "mutated" }},
		{name: "frozen skill metadata", mutate: func(descriptor *Descriptor) {
			descriptor.FrozenSkillMetadata[0].Description = "mutated"
		}},
		{name: "result schema", mutate: func(descriptor *Descriptor) { descriptor.ResultSchema[9] = 'b' }},
		{name: "explicit tool grants", mutate: func(descriptor *Descriptor) { descriptor.ExplicitToolGrants[0] = "mutated" }},
		{name: "sandbox", mutate: func(descriptor *Descriptor) { descriptor.Sandbox.Mode = "mutated" }},
		{name: "sandbox network", mutate: func(descriptor *Descriptor) { *descriptor.Sandbox.Network = false }},
		{name: "sandbox denylist additions", mutate: func(descriptor *Descriptor) { descriptor.Sandbox.DenylistAdd[0] = "mutated" }},
		{name: "sandbox denylist removals", mutate: func(descriptor *Descriptor) { descriptor.Sandbox.DenylistRemove[0] = "mutated" }},
		{name: "sandbox writable roots", mutate: func(descriptor *Descriptor) { descriptor.Sandbox.ExtraWritableRoots[0] = "mutated" }},
		{name: "sandbox read roots", mutate: func(descriptor *Descriptor) { descriptor.Sandbox.ExtraReadRoots[0] = "mutated" }},
		{name: "provenance watch keys", mutate: func(descriptor *Descriptor) { descriptor.Provenance.WatchKeys[0].WatchID = "mutated" }},
		{name: "provenance chain", mutate: func(descriptor *Descriptor) { descriptor.Provenance.Chain[0].DeliveryID = "mutated" }},
		{name: "config tool output limits", mutate: func(descriptor *Descriptor) {
			descriptor.Config.ToolOutputLimits["read_file"] = schema.ToolOutputLimit{MaxChars: 999}
		}},
		{name: "config skills dirs", mutate: func(descriptor *Descriptor) { descriptor.Config.SkillsDirs[0] = "mutated" }},
		{name: "config loop detection", mutate: func(descriptor *Descriptor) { *descriptor.Config.EnableLoopDetection = true }},
		{name: "config model fallbacks", mutate: func(descriptor *Descriptor) { descriptor.Config.ModelFallbacks[0] = "mutated" }},
		{name: "config sandbox network", mutate: func(descriptor *Descriptor) { *descriptor.Config.SandboxNet = false }},
	}
	paths := []struct {
		name  string
		apply func(Event) (State, error)
	}{
		{
			name: "Apply",
			apply: func(event Event) (State, error) {
				state := make(State)
				return state, Apply(state, event)
			},
		},
		{
			name: "Fold",
			apply: func(event Event) (State, error) {
				return Fold([]Event{event})
			},
		},
	}

	for _, path := range paths {
		for _, mutation := range mutations {
			t.Run(path.name+"/"+mutation.name, func(t *testing.T) {
				event := createdEventWithReferenceDescriptor("dlg_alpha")
				state, err := path.apply(event)
				if err != nil {
					t.Fatalf("accept created event: %v", err)
				}
				before := stateJSON(t, state)
				beforeRevision := state["dlg_alpha"].ProjectionRevision

				mutation.mutate(&event.Created.Descriptor)

				if got := stateJSON(t, state); got != before {
					t.Fatalf("accepted state changed after caller mutated %s:\n got %s\nwant %s", mutation.name, got, before)
				}
				if got := state["dlg_alpha"].ProjectionRevision; got != beforeRevision {
					t.Fatalf("projection revision = %d, want unchanged %d", got, beforeRevision)
				}
			})
		}
	}
}

func TestApplyRejectsInvalidToolNameCeiling(t *testing.T) {
	tests := []struct {
		name       string
		ceiling    []string
		resultTool string
		want       string
	}{
		{name: "empty", want: "ceiling is empty"},
		{name: "empty name", ceiling: []string{"", "communicate"}, want: "empty name"},
		{name: "wildcard", ceiling: []string{"*", "communicate"}, want: "wildcard"},
		{name: "duplicate", ceiling: []string{"communicate", "communicate"}, want: "duplicate"},
		{name: "unsorted", ceiling: []string{"shell", "communicate"}, want: "not sorted"},
		{name: "missing default result tool", ceiling: []string{"shell"}, want: `omits result tool "communicate"`},
		{name: "missing configured result tool", ceiling: []string{"communicate"}, resultTool: "report", want: `omits result tool "report"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			event := createdEvent("dlg_alpha", "")
			event.Seq = 1
			event.Created.Descriptor.ToolNameCeiling = tc.ceiling
			event.Created.Descriptor.Config.ResultToolName = tc.resultTool
			err := Apply(make(State), event)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Apply error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestApplyValidatesSandboxConfigProjection(t *testing.T) {
	trueValue := true
	alsoTrueValue := true
	falseValue := false
	tests := []struct {
		name    string
		sandbox *SandboxSnapshot
		mode    string
		network *bool
		wantErr string
	}{
		{name: "no sandbox has empty config projection"},
		{name: "matching mode", sandbox: &SandboxSnapshot{Mode: "read-only"}, mode: "read-only"},
		{name: "matching mode and network", sandbox: &SandboxSnapshot{Mode: "workspace-write", Network: &trueValue}, mode: "workspace-write", network: &alsoTrueValue},
		{name: "nil snapshot with mode", mode: "read-only", wantErr: "sandbox snapshot is nil"},
		{name: "nil snapshot with network", network: &falseValue, wantErr: "sandbox snapshot is nil"},
		{name: "snapshot with empty projection", sandbox: &SandboxSnapshot{}, wantErr: "sandbox snapshot requires a config projection"},
		{name: "mode mismatch", sandbox: &SandboxSnapshot{Mode: "workspace-write"}, mode: "read-only", wantErr: "sandbox mode"},
		{name: "snapshot network missing", sandbox: &SandboxSnapshot{Mode: "read-only"}, mode: "read-only", network: &trueValue, wantErr: "sandbox network"},
		{name: "config network missing", sandbox: &SandboxSnapshot{Mode: "read-only", Network: &trueValue}, mode: "read-only", wantErr: "sandbox network"},
		{name: "network value mismatch", sandbox: &SandboxSnapshot{Mode: "read-only", Network: &trueValue}, mode: "read-only", network: &falseValue, wantErr: "sandbox network"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			event := createdEvent("dlg_alpha", "")
			event.Seq = 1
			event.Created.Descriptor.Sandbox = tc.sandbox
			event.Created.Descriptor.Config.Sandbox = tc.mode
			event.Created.Descriptor.Config.SandboxNet = tc.network
			err := Apply(make(State), event)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Apply matching sandbox projection: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Apply error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestApplyStoppedExternalDeliveryRequiresID(t *testing.T) {
	state := applyEvents(t,
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerOwnerInput),
		stopRequestedEvent("dlg_alpha"),
	)
	want := stateJSON(t, state)

	err := Apply(state, finishedEvent("dlg_alpha", 1, OutcomeStopped, DispositionTerminalError, "", stoppedPacket()))
	if err == nil || !strings.Contains(err.Error(), "delivery id") {
		t.Fatalf("Apply stopped finish error = %v, want missing delivery id rejection", err)
	}
	if got := stateJSON(t, state); got != want {
		t.Fatalf("state mutated after missing delivery id:\n got %s\nwant %s", got, want)
	}
}

func TestApplyStoppedExternalDeliveryRejectsInvalidTerminalPacketJSON(t *testing.T) {
	tests := []struct {
		name    string
		wantErr string
		mutate  func(*TerminalPacket)
	}{
		{name: "message", wantErr: "message", mutate: func(packet *TerminalPacket) { packet.Message = json.RawMessage(`not-json`) }},
		{name: "structured result", wantErr: "structured result", mutate: func(packet *TerminalPacket) { packet.StructuredResult = json.RawMessage(`{`) }},
		{name: "oversized structured result", wantErr: "exceeds", mutate: func(packet *TerminalPacket) {
			packet.StructuredResult = json.RawMessage(`"` + strings.Repeat("x", MaxTerminalStructuredResultBytes) + `"`)
		}},
		{name: "metadata", wantErr: "metadata", mutate: func(packet *TerminalPacket) { packet.Metadata = json.RawMessage(`{`) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := applyEvents(t,
				createdEvent("dlg_alpha", ""),
				startedEvent("dlg_alpha", 1, TriggerOwnerInput),
				stopRequestedEvent("dlg_alpha"),
			)
			want := stateJSON(t, state)
			packet := stoppedPacket()
			test.mutate(packet)

			err := Apply(state, finishedEvent("dlg_alpha", 1, OutcomeStopped, DispositionTerminalError, "dlg_alpha/delivery/1", packet))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Apply stopped finish error = %v, want invalid %s rejection", err, test.wantErr)
			}
			if got := stateJSON(t, state); got != want {
				t.Fatalf("state mutated after invalid %s:\n got %s\nwant %s", test.name, got, want)
			}
		})
	}
}

func TestApplyTypedExhaustionValidatesAndClonesResumability(t *testing.T) {
	resumable := true
	state := applyEvents(t,
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerOwnerInput),
		preparedEvent("dlg_alpha", 1, TerminalPacket{Kind: PacketTerminalError, Message: json.RawMessage(`"tool budget exhausted"`)}),
	)
	event := finishedEvent("dlg_alpha", 1, OutcomeExhausted, DispositionTerminalError, "dlg_alpha/delivery/1", nil)
	event.RunFinished.Outcome.Reason = "tool_round_budget_exhausted"
	event.RunFinished.Outcome.ExhaustionBudget = ExhaustionBudgetToolRounds
	event.RunFinished.Outcome.ExhaustionLimit = 17
	event.RunFinished.Outcome.Resumable = &resumable
	if err := Apply(state, event); err != nil {
		t.Fatalf("Apply typed exhaustion: %v", err)
	}
	resumable = false
	got := state["dlg_alpha"].LatestOutcome
	if got == nil || got.Status != OutcomeExhausted || got.ExhaustionBudget != ExhaustionBudgetToolRounds || got.ExhaustionLimit != 17 || got.Resumable == nil || !*got.Resumable {
		t.Fatalf("typed exhaustion = %#v, want cloned tool-round metadata", got)
	}
}

func TestApplyRejectsIncoherentTypedExhaustion(t *testing.T) {
	trueValue := true
	falseValue := false
	tests := []struct {
		name    string
		outcome Outcome
		want    string
	}{
		{name: "metadata on completed", outcome: Outcome{Status: OutcomeCompleted, ExhaustionBudget: ExhaustionBudgetTurns}, want: "non-exhausted"},
		{name: "missing limit", outcome: Outcome{Status: OutcomeExhausted, Reason: "turn_budget_exhausted", ExhaustionBudget: ExhaustionBudgetTurns, Resumable: &falseValue}, want: "limit"},
		{name: "missing resumability", outcome: Outcome{Status: OutcomeExhausted, Reason: "turn_budget_exhausted", ExhaustionBudget: ExhaustionBudgetTurns, ExhaustionLimit: 4}, want: "resumability"},
		{name: "turn marked resumable", outcome: Outcome{Status: OutcomeExhausted, Reason: "turn_budget_exhausted", ExhaustionBudget: ExhaustionBudgetTurns, ExhaustionLimit: 4, Resumable: &trueValue}, want: "turn exhaustion is resumable"},
		{name: "tool round marked terminal", outcome: Outcome{Status: OutcomeExhausted, Reason: "tool_round_budget_exhausted", ExhaustionBudget: ExhaustionBudgetToolRounds, ExhaustionLimit: 4, Resumable: &falseValue}, want: "tool-round exhaustion is not resumable"},
		{name: "reason mismatch", outcome: Outcome{Status: OutcomeExhausted, Reason: "wrong", ExhaustionBudget: ExhaustionBudgetTurns, ExhaustionLimit: 4, Resumable: &falseValue}, want: "reason"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := applyEvents(t,
				createdEvent("dlg_alpha", ""),
				startedEvent("dlg_alpha", 1, TriggerOwnerInput),
				preparedEvent("dlg_alpha", 1, TerminalPacket{Kind: PacketTerminalError, Message: json.RawMessage(`"exhausted"`)}),
			)
			event := finishedEvent("dlg_alpha", 1, test.outcome.Status, DispositionTerminalError, "dlg_alpha/delivery/1", nil)
			event.RunFinished.Outcome = test.outcome
			event.RunFinished.Outcome.EndedAt = time.Unix(40, 0).UTC()
			if err := Apply(state, event); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Apply error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestApplyStoppedDeliverySuppressesCoveredOwnerWithoutDelivery(t *testing.T) {
	state := applyEvents(t,
		createdEvent("dlg_parent", ""),
		createdEvent("dlg_child", "dlg_parent"),
		startedEvent("dlg_child", 1, TriggerOwnerInput),
		stopRequestedEvent("dlg_parent"),
	)
	packet := stoppedPacket()
	packet.Message = json.RawMessage(`not-json`)

	if err := Apply(state, finishedEvent("dlg_child", 1, OutcomeStopped, DispositionTerminalError, "", packet)); err != nil {
		t.Fatalf("Apply covered stopped finish: %v", err)
	}
	child := state["dlg_child"]
	if child.CurrentRunOpen || child.LatestOutcome == nil || child.LatestOutcome.Status != OutcomeStopped {
		t.Fatalf("covered child = %#v, want closed stopped run", child)
	}
	if len(child.PendingDeliveries) != 0 {
		t.Fatalf("covered child deliveries = %#v, want suppressed", child.PendingDeliveries)
	}
}

func TestFoldUsesOneAggregatePerStableDelegate(t *testing.T) {
	events := sequence(
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerOwnerInput),
		preparedEvent("dlg_alpha", 1, reportedPacket("first")),
		finishedEvent("dlg_alpha", 1, OutcomeCompleted, DispositionReported, "dlg_alpha/delivery/1", nil),
		startedEvent("dlg_alpha", 2, TriggerOwnerInput),
	)

	state, err := Fold(events)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if len(state) != 1 {
		t.Fatalf("delegate aggregates = %d, want 1", len(state))
	}
	got := state["dlg_alpha"]
	if got == nil || got.Generation != 2 || got.Phase != PhaseRunning {
		t.Fatalf("aggregate = %#v, want generation 2 running", got)
	}
}

func TestApplyRejectsSequenceGapAndPayloadMismatch(t *testing.T) {
	_, err := Fold([]Event{{
		Seq:        2,
		Kind:       EventDelegateCreated,
		DelegateID: "dlg_alpha",
		Created:    &DelegateCreated{Descriptor: testDescriptor("dlg_alpha", "")},
	}})
	if err == nil || !strings.Contains(err.Error(), "sequence 2, want 1") {
		t.Fatalf("Fold sequence error = %v, want sequence gap", err)
	}

	state := make(State)
	event := createdEvent("dlg_alpha", "")
	event.RunStarted = &RunStarted{Generation: 1, Trigger: TriggerOwnerInput}
	if err := Apply(state, event); err == nil || !strings.Contains(err.Error(), "payload") {
		t.Fatalf("Apply payload mismatch error = %v, want payload mismatch", err)
	}
	if len(state) != 0 {
		t.Fatalf("state mutated after rejected payload: %#v", state)
	}
}

func TestApplyRejectsStaleGeneration(t *testing.T) {
	state := applyEvents(t,
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerOwnerInput),
	)
	want := stateJSON(t, state)

	err := Apply(state, finishedEvent("dlg_alpha", 2, OutcomeCompleted, DispositionReported, "", nil))
	if err == nil || !strings.Contains(err.Error(), "generation 2") {
		t.Fatalf("Apply stale generation error = %v", err)
	}
	if got := stateJSON(t, state); got != want {
		t.Fatalf("state mutated after stale generation:\n got %s\nwant %s", got, want)
	}
}

func TestApplyStopWinsOverPreparedNormalFinish(t *testing.T) {
	state := applyEvents(t,
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerOwnerInput),
		preparedEvent("dlg_alpha", 1, reportedPacket("normal result")),
		stopRequestedEvent("dlg_alpha"),
	)
	event := finishedEvent("dlg_alpha", 1, OutcomeCompleted, DispositionReported, "dlg_alpha/delivery/1", nil)
	event.RunFinished.Outcome.Reason = "normal_completion"
	if err := Apply(state, event); err != nil {
		t.Fatalf("Apply finish after stop: %v", err)
	}

	got := state["dlg_alpha"]
	if got.Phase != PhaseStopping || got.CurrentRunOpen {
		t.Fatalf("aggregate phase/open = %s/%t, want stopping/false", got.Phase, got.CurrentRunOpen)
	}
	if got.LatestOutcome == nil || got.LatestOutcome.Status != OutcomeStopped || got.LatestOutcome.Reason != "stopped_by_parent" {
		t.Fatalf("latest outcome = %#v, want stopped_by_parent", got.LatestOutcome)
	}
	if got.PreparedTerminal == nil || string(got.PreparedTerminal.Message) != `"normal result"` {
		t.Fatalf("prepared terminal = %#v, want retained diagnostic packet", got.PreparedTerminal)
	}
}

func TestApplyStopCompletionDiscardsInternalDeliveriesAndRetainsExternal(t *testing.T) {
	state := applyEvents(t,
		createdEvent("dlg_parent", ""),
		Event{Kind: EventDelegateAttentionChanged, DelegateID: "dlg_parent", AttentionChanged: &DelegateAttentionChanged{NeedsAttention: true}},
		startedEvent("dlg_parent", 1, TriggerOwnerInput),
		preparedEvent("dlg_parent", 1, reportedPacket("for root")),
		finishedEvent("dlg_parent", 1, OutcomeCompleted, DispositionReported, "dlg_parent/delivery/1", nil),
		createdEvent("dlg_child", "dlg_parent"),
		Event{Kind: EventDelegateAttentionChanged, DelegateID: "dlg_child", AttentionChanged: &DelegateAttentionChanged{NeedsAttention: true}},
		startedEvent("dlg_child", 1, TriggerOwnerInput),
		preparedEvent("dlg_child", 1, reportedPacket("for parent")),
		finishedEvent("dlg_child", 1, OutcomeCompleted, DispositionReported, "dlg_child/delivery/1", nil),
		stopRequestedEvent("dlg_parent"),
	)
	if state["dlg_parent"].NeedsAttention || state["dlg_child"].NeedsAttention {
		t.Fatalf("subtree stop request retained attention: parent=%t child=%t", state["dlg_parent"].NeedsAttention, state["dlg_child"].NeedsAttention)
	}
	requestSeq := state["dlg_parent"].PendingStopSeq
	if err := Apply(state, Event{
		Kind:                 EventDelegateSubtreeStopCompleted,
		DelegateID:           "dlg_parent",
		SubtreeStopCompleted: &SubtreeStopCompleted{RequestSeq: requestSeq},
	}); err != nil {
		t.Fatalf("Apply stop completion: %v", err)
	}

	if got := state["dlg_parent"].PendingDeliveries; len(got) != 1 || got[0].DeliveryID != "dlg_parent/delivery/1" {
		t.Fatalf("external deliveries = %#v, want parent delivery retained", got)
	}
	if got := state["dlg_child"].PendingDeliveries; len(got) != 0 {
		t.Fatalf("internal deliveries = %#v, want discarded", got)
	}
	if state["dlg_parent"].NeedsAttention || state["dlg_child"].NeedsAttention {
		t.Fatalf("subtree stop completion retained attention: parent=%t child=%t", state["dlg_parent"].NeedsAttention, state["dlg_child"].NeedsAttention)
	}
}

func TestApplyResumabilityClosureIsMonotonic(t *testing.T) {
	state := applyEvents(t,
		createdEvent("dlg_alpha", ""),
		Event{Kind: EventDelegateAttentionChanged, DelegateID: "dlg_alpha", AttentionChanged: &DelegateAttentionChanged{NeedsAttention: true}},
		createdEvent("dlg_child", "dlg_alpha"),
		Event{Kind: EventDelegateAttentionChanged, DelegateID: "dlg_child", AttentionChanged: &DelegateAttentionChanged{NeedsAttention: true}},
	)
	childRevision := state["dlg_child"].ProjectionRevision
	if err := Apply(state, Event{
		Kind:               EventDelegateResumabilityClosed,
		DelegateID:         "dlg_alpha",
		ResumabilityClosed: &ResumabilityClosed{Reason: "isolation_disposed"},
	}); err != nil {
		t.Fatalf("Apply first closure: %v", err)
	}
	if state["dlg_alpha"].NeedsAttention || state["dlg_child"].NeedsAttention {
		t.Fatalf("resumability closure retained attention: parent=%t child=%t", state["dlg_alpha"].NeedsAttention, state["dlg_child"].NeedsAttention)
	}
	if got := state["dlg_child"].ProjectionRevision; got != childRevision+1 {
		t.Fatalf("descendant projection revision = %d, want %d", got, childRevision+1)
	}
	want := stateJSON(t, state)
	err := Apply(state, Event{
		Kind:               EventDelegateResumabilityClosed,
		DelegateID:         "dlg_alpha",
		ResumabilityClosed: &ResumabilityClosed{Reason: "different_reason"},
	})
	if err == nil || !strings.Contains(err.Error(), "already closed") {
		t.Fatalf("Apply second closure error = %v, want monotonic refusal", err)
	}
	if got := stateJSON(t, state); got != want {
		t.Fatalf("state mutated after second closure:\n got %s\nwant %s", got, want)
	}
}

func TestApplyKeepsTwoUnacknowledgedDeliveriesInOrder(t *testing.T) {
	state := applyEvents(t,
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerOwnerInput),
		preparedEvent("dlg_alpha", 1, reportedPacket("first")),
		finishedEvent("dlg_alpha", 1, OutcomeCompleted, DispositionReported, "dlg_alpha/delivery/1", nil),
		startedEvent("dlg_alpha", 2, TriggerOwnerInput),
		preparedEvent("dlg_alpha", 2, reportedPacket("second")),
		finishedEvent("dlg_alpha", 2, OutcomeCompleted, DispositionReported, "dlg_alpha/delivery/2", nil),
	)

	got := state["dlg_alpha"].PendingDeliveries
	if len(got) != 2 || got[0].DeliveryID != "dlg_alpha/delivery/1" || got[1].DeliveryID != "dlg_alpha/delivery/2" {
		t.Fatalf("pending deliveries = %#v, want generation order", got)
	}
}

func TestApplyCompletedNoActionProjectsCompleted(t *testing.T) {
	state := applyEvents(t,
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerAttention),
	)
	if err := Apply(state, finishedEvent("dlg_alpha", 1, OutcomeCompleted, DispositionCompletedNoAction, "", nil)); err != nil {
		t.Fatalf("Apply completed-no-action finish: %v", err)
	}

	got := state["dlg_alpha"]
	if got.LatestOutcome == nil || got.LatestOutcome.Status != OutcomeCompleted {
		t.Fatalf("latest outcome = %#v, want completed", got.LatestOutcome)
	}
	if len(got.PendingDeliveries) != 0 {
		t.Fatalf("pending deliveries = %#v, want none", got.PendingDeliveries)
	}
}

func TestApplyAllowsOnlyOnePendingStopSequence(t *testing.T) {
	state := applyEvents(t,
		createdEvent("dlg_alpha", ""),
		createdEvent("dlg_beta", ""),
		stopRequestedEvent("dlg_alpha"),
	)
	want := stateJSON(t, state)

	secondStop := stopRequestedEvent("dlg_beta")
	secondStop.Seq = 4
	err := Apply(state, secondStop)
	if err == nil || !strings.Contains(err.Error(), "pending subtree stop") {
		t.Fatalf("second stop error = %v, want one-pending-stop refusal", err)
	}
	if got := stateJSON(t, state); got != want {
		t.Fatalf("state mutated after second stop:\n got %s\nwant %s", got, want)
	}
}

func TestApplyProjectionRevisionIncrementsOnlyAffectedDelegate(t *testing.T) {
	state := applyEvents(t,
		createdEvent("dlg_parent", ""),
		createdEvent("dlg_child", "dlg_parent"),
		createdEvent("dlg_other", ""),
	)
	attentionRevision := state["dlg_other"].ProjectionRevision
	for _, tc := range []struct {
		name           string
		needsAttention bool
		wantRevision   uint64
	}{
		{name: "false to true", needsAttention: true, wantRevision: attentionRevision + 1},
		{name: "repeated true", needsAttention: true, wantRevision: attentionRevision + 1},
		{name: "true to false", needsAttention: false, wantRevision: attentionRevision + 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := Apply(state, Event{
				Kind:             EventDelegateAttentionChanged,
				DelegateID:       "dlg_other",
				AttentionChanged: &DelegateAttentionChanged{NeedsAttention: tc.needsAttention},
			}); err != nil {
				t.Fatalf("Apply attention change: %v", err)
			}
			if got := state["dlg_other"].NeedsAttention; got != tc.needsAttention {
				t.Fatalf("needs attention = %t, want %t", got, tc.needsAttention)
			}
			if got := state["dlg_other"].ProjectionRevision; got != tc.wantRevision {
				t.Fatalf("attention revision = %d, want %d", got, tc.wantRevision)
			}
		})
	}
	beforeParent := state["dlg_parent"].ProjectionRevision
	beforeChild := state["dlg_child"].ProjectionRevision
	beforeOther := state["dlg_other"].ProjectionRevision

	stop := stopRequestedEvent("dlg_parent")
	stop.Seq = 4
	if err := Apply(state, stop); err != nil {
		t.Fatalf("Apply stop request: %v", err)
	}
	if got := state["dlg_parent"].ProjectionRevision; got != beforeParent+1 {
		t.Fatalf("parent revision = %d, want %d", got, beforeParent+1)
	}
	if got := state["dlg_child"].ProjectionRevision; got != beforeChild+1 {
		t.Fatalf("child revision = %d, want %d", got, beforeChild+1)
	}
	if got := state["dlg_other"].ProjectionRevision; got != beforeOther {
		t.Fatalf("unaffected revision = %d, want %d", got, beforeOther)
	}
}

func TestFoldReconstructsProjectionRevisionAcrossRestart(t *testing.T) {
	events := sequence(
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerOwnerInput),
		preparedEvent("dlg_alpha", 1, reportedPacket("done")),
		finishedEvent("dlg_alpha", 1, OutcomeCompleted, DispositionReported, "dlg_alpha/delivery/1", nil),
	)
	want, err := Fold(events)
	if err != nil {
		t.Fatalf("first Fold: %v", err)
	}
	got, err := Fold(events)
	if err != nil {
		t.Fatalf("restart Fold: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restart state differs:\n got %#v\nwant %#v", got, want)
	}
	if got["dlg_alpha"].ProjectionRevision != 4 {
		t.Fatalf("projection revision = %d, want 4", got["dlg_alpha"].ProjectionRevision)
	}
}

func TestApplyRunOpenDistinguishesUnfinishedAndSettledStopping(t *testing.T) {
	state := applyEvents(t,
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerOwnerInput),
		stopRequestedEvent("dlg_alpha"),
	)
	if !state["dlg_alpha"].CurrentRunOpen {
		t.Fatal("current run is closed before exact finish")
	}
	requestSeq := state["dlg_alpha"].PendingStopSeq
	if err := Apply(state, finishedEvent("dlg_alpha", 1, OutcomeStopped, DispositionTerminalError, "dlg_alpha/delivery/1", stoppedPacket())); err != nil {
		t.Fatalf("Apply stopped finish: %v", err)
	}
	if state["dlg_alpha"].CurrentRunOpen {
		t.Fatal("current run remains open after exact finish")
	}
	if state["dlg_alpha"].Phase != PhaseStopping {
		t.Fatalf("phase = %s, want stopping until stop completion", state["dlg_alpha"].Phase)
	}
	if err := Apply(state, Event{
		Kind:                 EventDelegateSubtreeStopCompleted,
		DelegateID:           "dlg_alpha",
		SubtreeStopCompleted: &SubtreeStopCompleted{RequestSeq: requestSeq},
	}); err != nil {
		t.Fatalf("Apply stop completion: %v", err)
	}
	if state["dlg_alpha"].Phase != PhaseIdle {
		t.Fatalf("phase = %s, want idle after stop completion", state["dlg_alpha"].Phase)
	}
}

func sequence(events ...Event) []Event {
	for i := range events {
		events[i].Seq = uint64(i + 1)
	}
	return events
}

func applyEvents(t *testing.T, events ...Event) State {
	t.Helper()
	state := make(State)
	for i := range events {
		if events[i].Seq == 0 {
			events[i].Seq = uint64(i + 1)
		}
		if err := Apply(state, events[i]); err != nil {
			t.Fatalf("Apply event %d (%s): %v", i+1, events[i].Kind, err)
		}
	}
	return state
}

func createdEvent(id, parentID string) Event {
	return Event{
		Kind:       EventDelegateCreated,
		DelegateID: id,
		Created:    &DelegateCreated{Descriptor: testDescriptor(id, parentID)},
	}
}

// Descriptor.Name is a display label set once at creation. A journal written
// before the field existed must fold with the zero name — which renders as
// absent — and a named descriptor must round-trip through the same envelope.
func TestDescriptorNameRoundTripAndLegacyTolerance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	named := createdEvent("dlg_named", "")
	named.Seq = 1
	named.Created.Descriptor.Name = "label-roundtrip"
	if _, _, err := store.AppendBatch(make(State), []Event{named}); err != nil {
		_ = store.Close()
		t.Fatalf("AppendBatch: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	events, err := ReadEvents(path)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	state, err := Fold(events)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if got := state["dlg_named"].Descriptor.Name; got != "label-roundtrip" {
		t.Fatalf("folded descriptor name = %q, want label-roundtrip", got)
	}

	// A legacy record whose descriptor lacks the name key folds to the zero
	// value, and the zero value serializes as absent.
	legacyPath := filepath.Join(t.TempDir(), "delegates.jsonl")
	legacy := "{\"version\":1}\n" +
		"{\"events\":[{\"kind\":\"delegate_created\",\"seq\":1,\"delegate_id\":\"dlg_legacy\"," +
		"\"created\":{\"descriptor\":{\"child_session_id\":\"child_legacy\",\"transcript_ref\":\"local:child_legacy\"," +
		"\"owner_session_id\":\"root\",\"task\":\"legacy task\",\"agent_type\":\"general\"," +
		"\"tool_name_ceiling\":[\"communicate\"],\"resumable\":true}}}]}\n"
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	legacyEvents, err := ReadEvents(legacyPath)
	if err != nil {
		t.Fatalf("ReadEvents legacy: %v", err)
	}
	legacyState, err := Fold(legacyEvents)
	if err != nil {
		t.Fatalf("Fold legacy: %v", err)
	}
	aggregate := legacyState["dlg_legacy"]
	if aggregate == nil {
		t.Fatal("legacy delegate missing from folded state")
	}
	if got := aggregate.Descriptor.Name; got != "" {
		t.Fatalf("legacy descriptor name = %q, want empty", got)
	}
	encoded, err := json.Marshal(aggregate.Descriptor)
	if err != nil {
		t.Fatalf("marshal legacy descriptor: %v", err)
	}
	if strings.Contains(string(encoded), "\"name\"") {
		t.Fatalf("zero name serialized as present: %s", encoded)
	}
}

func createdEventWithReferenceDescriptor(id string) Event {
	network := true
	loopDetection := false
	configNetwork := true
	descriptor := testDescriptor(id, "")
	descriptor.TaskTemplates = []task.TaskTemplate{
		{Title: "research", Prompt: "investigate", ReasoningEffort: "high", Type: "research"},
		{Title: "insert", Prompt: "preserve", Type: "verify", Insert: "parent_tasks"},
		{Title: "fix", Prompt: "implement", ReasoningEffort: "low", Type: "fix"},
	}
	descriptor.ToolNameCeiling = []string{"communicate", "shell"}
	descriptor.FrozenSkillNames = []string{"review"}
	descriptor.FrozenSkillBodies = []string{"review instructions"}
	descriptor.FrozenSkillMetadata = []schema.FrozenSkillPreload{{Name: "review", Description: "review provenance"}}
	descriptor.ResultSchema = json.RawMessage(`{"type":"alpha"}`)
	descriptor.ExplicitToolGrants = []string{"shell"}
	descriptor.Sandbox = &SandboxSnapshot{
		Mode:               "workspace-write",
		Network:            &network,
		DenylistAdd:        []string{"secret"},
		DenylistRemove:     []string{"public"},
		ExtraWritableRoots: []string{"/write"},
		ExtraReadRoots:     []string{"/read"},
	}
	descriptor.Provenance = &provenance.Causal{
		WatchKeys: []provenance.WatchKey{{WatchID: "watch", WatchGeneration: "generation"}},
		Chain:     []provenance.Entry{{Kind: "watch", DeliveryID: "delivery"}},
	}
	descriptor.Config = schema.ConfigSnapshot{
		ToolOutputLimits: map[string]schema.ToolOutputLimit{
			"read_file": {MaxChars: 100, MaxLines: 10, Strategy: schema.TruncHeadTail},
		},
		AgentName:              "reviewer",
		ReasoningEffort:        "high",
		SkillsDirs:             []string{"skills"},
		MCPConfigFiles:         []string{"mcp.json"},
		MCPInline:              []string{"inline"},
		PluginDirs:             []string{"plugins"},
		SystemPromptAppend:     []string{"append.md"},
		ShareTasksWithChildren: true,
		EnableLoopDetection:    &loopDetection,
		ModelFallbacks:         []string{"openai/fallback"},
		Sandbox:                "workspace-write",
		SandboxNet:             &configNetwork,
	}
	descriptor.SharedTaskStoreOwnerSessionID = "root_session"
	return Event{
		Kind:       EventDelegateCreated,
		Seq:        1,
		DelegateID: id,
		Created:    &DelegateCreated{Descriptor: descriptor},
	}
}

func startedEvent(id string, generation uint64, trigger RunTrigger) Event {
	return Event{
		Kind:       EventDelegateRunStarted,
		DelegateID: id,
		RunStarted: &RunStarted{
			Generation: generation,
			Trigger:    trigger,
			StartedAt:  time.Date(2026, 8, 12, 10, int(generation), 0, 0, time.UTC),
		},
	}
}

func preparedEvent(id string, generation uint64, packet TerminalPacket) Event {
	return Event{
		Kind:       EventDelegateTerminalPrepared,
		DelegateID: id,
		TerminalPrepared: &TerminalPrepared{
			Generation: generation,
			Packet:     packet,
		},
	}
}

func finishedEvent(id string, generation uint64, status OutcomeStatus, disposition RunDisposition, deliveryID string, packet *TerminalPacket) Event {
	return Event{
		Kind:       EventDelegateRunFinished,
		DelegateID: id,
		RunFinished: &RunFinished{
			Generation:  generation,
			Outcome:     Outcome{Status: status, EndedAt: time.Date(2026, 8, 12, 11, int(generation), 0, 0, time.UTC)},
			Disposition: disposition,
			DeliveryID:  deliveryID,
			Packet:      packet,
		},
	}
}

func stopRequestedEvent(id string) Event {
	return Event{
		Kind:                 EventDelegateSubtreeStopRequested,
		DelegateID:           id,
		SubtreeStopRequested: &SubtreeStopRequested{TargetDelegateID: id},
	}
}

func reportedPacket(message string) TerminalPacket {
	return TerminalPacket{
		Kind:             PacketReported,
		Message:          json.RawMessage(`"` + message + `"`),
		StructuredResult: json.RawMessage(`{"answer":true}`),
	}
}

func stoppedPacket() *TerminalPacket {
	return &TerminalPacket{
		Kind:    PacketTerminalError,
		Message: json.RawMessage(`"stopped by parent"`),
	}
}

func testDescriptor(id, parentID string) Descriptor {
	return Descriptor{
		ChildSessionID:   "session_" + id,
		TranscriptRef:    "transcript:" + id,
		OwnerSessionID:   "root_session",
		ParentDelegateID: parentID,
		Task:             "task for " + id,
		Description:      "description for " + id,
		AgentType:        "worker",
		ToolNameCeiling:  []string{"communicate"},
		Resumable:        true,
	}
}

func stateJSON(t *testing.T, state State) string {
	t.Helper()
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	return string(b)
}

// TestApplyLeavesUntouchedAggregatesShared pins the contract
// extendHistoricalDelegateFold (agent/jobs_activity_past.go) relies on: Apply
// may be handed a shallow maps.Clone of a state another reader still holds,
// so it must never mutate an aggregate in place. Every aggregate an event
// changes is replaced by a fresh clone, and every aggregate the event leaves
// alone keeps its pointer, so the shared prior state stays intact and the
// per-event cost scales with the aggregates the event touches.
func TestApplyLeavesUntouchedAggregatesShared(t *testing.T) {
	base := applyEvents(t,
		createdEvent("dlg_parent", ""),
		createdEvent("dlg_child", "dlg_parent"),
		createdEvent("dlg_other", ""),
		startedEvent("dlg_parent", 1, TriggerInitial),
		startedEvent("dlg_child", 1, TriggerInitial),
		startedEvent("dlg_other", 1, TriggerInitial),
		preparedEvent("dlg_child", 1, reportedPacket("done")),
		Event{Kind: EventDelegateAttentionChanged, DelegateID: "dlg_other", AttentionChanged: &DelegateAttentionChanged{NeedsAttention: true}},
	)
	stopped := applyEvents(t,
		createdEvent("dlg_parent", ""),
		createdEvent("dlg_child", "dlg_parent"),
		createdEvent("dlg_other", ""),
		startedEvent("dlg_child", 1, TriggerInitial),
		preparedEvent("dlg_child", 1, reportedPacket("done")),
		finishedEvent("dlg_child", 1, OutcomeCompleted, DispositionReported, "dlg_child/delivery/1", nil),
		Event{Kind: EventDelegateSubtreeStopRequested, DelegateID: "dlg_parent", Seq: 7, SubtreeStopRequested: &SubtreeStopRequested{TargetDelegateID: "dlg_parent"}},
	)
	tests := []struct {
		name       string
		prior      State
		event      Event
		wantShared []string
	}{
		{
			name:       "created leaves every existing aggregate shared",
			prior:      base,
			event:      createdEvent("dlg_new", "dlg_parent"),
			wantShared: []string{"dlg_parent", "dlg_child", "dlg_other"},
		},
		{
			name:       "attention change touches only its delegate",
			prior:      base,
			event:      Event{Kind: EventDelegateAttentionChanged, DelegateID: "dlg_child", AttentionChanged: &DelegateAttentionChanged{NeedsAttention: true}},
			wantShared: []string{"dlg_parent", "dlg_other"},
		},
		{
			name:       "run finished touches only its delegate",
			prior:      base,
			event:      finishedEvent("dlg_child", 1, OutcomeCompleted, DispositionReported, "dlg_child/delivery/1", nil),
			wantShared: []string{"dlg_parent", "dlg_other"},
		},
		{
			name:       "resumability closure touches the subtree",
			prior:      base,
			event:      Event{Kind: EventDelegateResumabilityClosed, DelegateID: "dlg_parent", ResumabilityClosed: &ResumabilityClosed{Reason: "isolation_disposed"}},
			wantShared: []string{"dlg_other"},
		},
		{
			name:       "subtree stop request touches the subtree",
			prior:      base,
			event:      Event{Kind: EventDelegateSubtreeStopRequested, DelegateID: "dlg_parent", Seq: 9, SubtreeStopRequested: &SubtreeStopRequested{TargetDelegateID: "dlg_parent"}},
			wantShared: []string{"dlg_other"},
		},
		{
			name:       "subtree stop completion rewrites every aggregate's deliveries",
			prior:      stopped,
			event:      Event{Kind: EventDelegateSubtreeStopCompleted, DelegateID: "dlg_parent", SubtreeStopCompleted: &SubtreeStopCompleted{RequestSeq: 7}},
			wantShared: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := stateJSON(t, tc.prior)
			state := maps.Clone(tc.prior)
			if err := Apply(state, tc.event); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if got := stateJSON(t, tc.prior); got != before {
				t.Fatalf("Apply mutated the shared prior state:\n got %s\nwant %s", got, before)
			}
			shared := make(map[string]bool, len(tc.wantShared))
			for _, id := range tc.wantShared {
				shared[id] = true
			}
			for id, aggregate := range state {
				prior := tc.prior[id]
				switch {
				case shared[id] && aggregate != prior:
					t.Errorf("delegate %q was cloned although the event does not touch it", id)
				case !shared[id] && prior != nil && aggregate == prior:
					t.Errorf("delegate %q was mutated in place", id)
				}
			}
		})
	}
}

// TestApplyRestoresStateWhenEventIsRejected pins that a rejected event leaves
// the state exactly as it was, including after applyRunFinished has already
// written to the aggregate before discovering the event is invalid.
func TestApplyRestoresStateWhenEventIsRejected(t *testing.T) {
	state := applyEvents(t,
		createdEvent("dlg_parent", ""),
		createdEvent("dlg_child", "dlg_parent"),
		startedEvent("dlg_child", 1, TriggerInitial),
		Event{Kind: EventDelegateAttentionChanged, DelegateID: "dlg_child", AttentionChanged: &DelegateAttentionChanged{NeedsAttention: true}},
		Event{Kind: EventDelegateSubtreeStopRequested, DelegateID: "dlg_child", Seq: 5, SubtreeStopRequested: &SubtreeStopRequested{TargetDelegateID: "dlg_child"}},
	)
	before := stateJSON(t, state)
	child := state["dlg_child"]
	rejected := finishedEvent("dlg_child", 1, OutcomeStopped, DispositionTerminalError, "dlg_child/delivery/1", stoppedPacket())
	rejected.RunFinished.ObserverCallbackDelivered = true
	err := Apply(state, rejected)
	if err == nil || !strings.Contains(err.Error(), "observer callback") {
		t.Fatalf("Apply error = %v, want observer callback rejection", err)
	}
	if got := stateJSON(t, state); got != before {
		t.Fatalf("rejected event changed state:\n got %s\nwant %s", got, before)
	}
	if state["dlg_child"] != child {
		t.Fatalf("rejected event replaced the delegate aggregate")
	}
	if err := Apply(state, createdEvent("dlg_parent", "")); err == nil {
		t.Fatal("Apply accepted a duplicate created event")
	}
	if got := stateJSON(t, state); got != before {
		t.Fatalf("rejected created event changed state:\n got %s\nwant %s", got, before)
	}
}

// TestApplyRejectsNilAggregateWalkedBySubtreeEvents pins that the event kinds
// whose apply functions walk every aggregate reject a state holding a nil
// entry instead of dereferencing it, even when the nil entry is outside the
// event's subtree.
func TestApplyRejectsNilAggregateWalkedBySubtreeEvents(t *testing.T) {
	for _, event := range []Event{
		{Kind: EventDelegateSubtreeStopRequested, DelegateID: "dlg_parent", Seq: 3, SubtreeStopRequested: &SubtreeStopRequested{TargetDelegateID: "dlg_parent"}},
		{Kind: EventDelegateResumabilityClosed, DelegateID: "dlg_parent", ResumabilityClosed: &ResumabilityClosed{Reason: "isolation_disposed"}},
		{Kind: EventDelegateSubtreeStopCompleted, DelegateID: "dlg_parent", SubtreeStopCompleted: &SubtreeStopCompleted{RequestSeq: 3}},
	} {
		t.Run(string(event.Kind), func(t *testing.T) {
			state := applyEvents(t, createdEvent("dlg_parent", ""), createdEvent("dlg_other", ""))
			state["dlg_nil"] = nil
			err := Apply(state, event)
			if err == nil || !strings.Contains(err.Error(), `delegate "dlg_nil" aggregate is nil`) {
				t.Fatalf("Apply error = %v, want nil aggregate rejection", err)
			}
		})
	}
}
