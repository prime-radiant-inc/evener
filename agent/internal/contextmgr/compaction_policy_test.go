package contextmgr

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

const millionTokenWindow = 1 << 20

// policyHistory is a history whose early turns carry the content the removed
// masking and clearing layers used to rewrite: a large tool result and an
// assistant turn with thinking text.
func policyHistory() []schema.Turn {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("read some files")},
		{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: strings.Repeat("think ", 200)}},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call1", Name: "read_file", Arguments: []byte(`{"file_path": "/tmp/test.txt"}`)}},
		}}},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("call1", "read_file", strings.Repeat("x", 30000), false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("Done reading")},
	}
	for range 6 {
		history = append(history,
			schema.Turn{Kind: schema.TurnUserInput, Message: llm.User("next task")},
			schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("working on it")},
		)
	}
	return history
}

// usageAt records a measurement of fraction of the window as the last request's
// input size, with no turns appended since.
func usageAt(cm *Manager, history []schema.Turn, fraction float64) {
	cm.RecordInputTokens(int(fraction*float64(millionTokenWindow)), len(history))
}

func compactionLayers(layers *[]string) func(events.EventKind, events.EventData) {
	return func(kind events.EventKind, data events.EventData) {
		if cd, ok := data.(events.ContextCompactionData); ok && kind == events.EventContextCompaction {
			*layers = append(*layers, cd.Layer)
		}
	}
}

func newPolicyManager() *Manager {
	cm := NewManager(testOpenAIProfileWithContextWindow(millionTokenWindow), nil, cheapmodel.New(nil))
	cm.PreserveRecentTurns = 2
	return cm
}

func TestMaybeCompact_TriggersAtNinetyPercentByDefault(t *testing.T) {
	for _, tc := range []struct {
		usage float64
		want  bool
	}{{0.85, false}, {0.91, true}} {
		cm := newPolicyManager()
		history := policyHistory()
		usageAt(cm, history, tc.usage)
		var layers []string
		cm.MaybeCompact(context.Background(), &history, 0, compactionLayers(&layers))
		if got := len(layers) > 0; got != tc.want {
			t.Errorf("usage %.2f: compacted = %v, want %v (layers %v)", tc.usage, got, tc.want, layers)
		}
	}
}

func TestMaybeCompact_ConfiguredTriggerMovesTheGate(t *testing.T) {
	cm := newPolicyManager()
	cm.SetCompactAt(0.70)
	history := policyHistory()
	usageAt(cm, history, 0.75)
	var layers []string
	cm.MaybeCompact(context.Background(), &history, 0, compactionLayers(&layers))
	if len(layers) == 0 {
		t.Fatal("0.75 usage with trigger 0.70 should compact")
	}
}

func TestSetCompactAt_KeepsNudgeBelowAndBackstopAboveTrigger(t *testing.T) {
	for _, tc := range []struct {
		trigger, wantWarn, wantSummarize float64
	}{
		{0.90, 0.75, 0.95},
		{0.80, 0.75, 0.95},
		{0.75, 0.70, 0.95},
		{0.70, 0.65, 0.95},
		{0.50, 0.45, 0.95},
		{0.95, 0.75, 0.95},
		{0.99, 0.75, 0.99},
		{1.00, 0.75, 1.00},
	} {
		cm := newPolicyManager()
		cm.SetCompactAt(tc.trigger)
		if !approxEqual(cm.CheckpointThreshold, tc.trigger) || !approxEqual(cm.WarnThreshold, tc.wantWarn) || !approxEqual(cm.SummarizeThreshold, tc.wantSummarize) {
			t.Errorf("trigger %.2f: warn/checkpoint/summarize = %.2f/%.2f/%.2f, want %.2f/%.2f/%.2f",
				tc.trigger, cm.WarnThreshold, cm.CheckpointThreshold, cm.SummarizeThreshold, tc.wantWarn, tc.trigger, tc.wantSummarize)
		}
	}
}

func TestNewManager_DefaultsNudgeBeforeTriggerBeforeBackstop(t *testing.T) {
	cm := newPolicyManager()
	if !approxEqual(cm.WarnThreshold, 0.75) || !approxEqual(cm.CheckpointThreshold, 0.90) || !approxEqual(cm.SummarizeThreshold, 0.95) {
		t.Fatalf("defaults warn/checkpoint/summarize = %.2f/%.2f/%.2f", cm.WarnThreshold, cm.CheckpointThreshold, cm.SummarizeThreshold)
	}
}

func approxEqual(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }

// Rewriting early history changes the provider's cached prompt prefix, so no
// strategy may touch turns before the fold point at any pressure below the
// fold trigger.
func TestStrategies_LeaveHistoryByteIdenticalBelowTheTrigger(t *testing.T) {
	for _, usage := range []float64{0.65, 0.85} {
		cm := newPolicyManager()
		history := policyHistory()
		want := policyHistory()
		usageAt(cm, history, usage)
		sls, _ := newTestSessionLogStrategy(t, cm)
		var layers []string
		if err := sls.ManageContext(context.Background(), &history, 0, compactionLayers(&layers)); err != nil {
			t.Fatal(err)
		}
		if len(layers) != 0 {
			t.Errorf("usage %.2f: session-log strategy ran layers %v", usage, layers)
		}
		if !reflect.DeepEqual(history, want) {
			t.Errorf("usage %.2f: session-log strategy rewrote history", usage)
		}
	}
}
