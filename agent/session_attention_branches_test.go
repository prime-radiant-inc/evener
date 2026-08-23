package agent

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestDelegateTranscriptPathFromRef(t *testing.T) {
	t.Parallel()
	// Valid local ref.
	path, sessionID, err := delegateTranscriptPathFromRef("/state", "local:child123")
	if err != nil || sessionID != "child123" {
		t.Fatalf("path=%q session=%q err=%v", path, sessionID, err)
	}
	// Project ref crosses state dir boundary -> error.
	if _, _, err := delegateTranscriptPathFromRef("/state", "project:child"); err == nil {
		t.Fatal("project ref should error")
	}
	// Invalid ref format.
	if _, _, err := delegateTranscriptPathFromRef("/state", "notavalidref"); err == nil {
		t.Fatal("invalid ref should error")
	}
}

func TestValidateDelegateAttentionResolutions(t *testing.T) {
	t.Parallel()
	fold := newDelegateAttentionFold()
	fold.content["attn_1"] = llm.User("hello")
	fold.order = append(fold.order, "attn_1")

	// Valid: pending attention consumed.
	if err := validateDelegateAttentionResolutions(fold, []string{"attn_1"}, delegateAttentionConsumed, 0); err != nil {
		t.Fatalf("valid consume: %v", err)
	}
	// Valid: pending attention discarded.
	if err := validateDelegateAttentionResolutions(fold, []string{"attn_1"}, delegateAttentionDiscarded, 0); err != nil {
		t.Fatalf("valid discard: %v", err)
	}
	// Invalid disposition.
	if err := validateDelegateAttentionResolutions(fold, []string{"attn_1"}, "invalid", 0); err == nil {
		t.Fatal("invalid disposition should error")
	}
	// Empty ID.
	if err := validateDelegateAttentionResolutions(fold, []string{""}, delegateAttentionConsumed, 0); err == nil {
		t.Fatal("empty ID should error")
	}
	// Non-pending attention.
	if err := validateDelegateAttentionResolutions(fold, []string{"attn_missing"}, delegateAttentionConsumed, 0); err == nil {
		t.Fatal("non-pending attention should error")
	}
	// Resume generation with non-consumed disposition.
	if err := validateDelegateAttentionResolutions(fold, []string{"attn_1"}, delegateAttentionDiscarded, 1); err == nil {
		t.Fatal("resume generation with discarded should error")
	}
	// Resume generation with multiple IDs.
	if err := validateDelegateAttentionResolutions(fold, []string{"attn_1", "attn_2"}, delegateAttentionConsumed, 1); err == nil {
		t.Fatal("resume generation with multiple IDs should error")
	}
	// Resume generation with valid single consumed.
	if err := validateDelegateAttentionResolutions(fold, []string{"attn_1"}, delegateAttentionConsumed, 1); err != nil {
		t.Fatalf("valid resume generation: %v", err)
	}
}

func TestValidateDelegateAttentionResolutions_ConflictingResolution(t *testing.T) {
	t.Parallel()
	fold := newDelegateAttentionFold()
	fold.content["attn_1"] = llm.User("hello")
	fold.order = append(fold.order, "attn_1")
	fold.resolutions["attn_1"] = delegateAttentionConsumed
	fold.resumeGenerations["attn_1"] = 0

	// Same resolution + generation -> no error (idempotent).
	if err := validateDelegateAttentionResolutions(fold, []string{"attn_1"}, delegateAttentionConsumed, 0); err != nil {
		t.Fatalf("idempotent resolution: %v", err)
	}
	// Different disposition -> conflict.
	if err := validateDelegateAttentionResolutions(fold, []string{"attn_1"}, delegateAttentionDiscarded, 0); err == nil {
		t.Fatal("conflicting resolution should error")
	}
	// Different generation -> conflict.
	if err := validateDelegateAttentionResolutions(fold, []string{"attn_1"}, delegateAttentionConsumed, 1); err == nil {
		t.Fatal("conflicting generation should error")
	}
}

func TestValidateDelegateAttentionResolutions_ResumeGenerationConflict(t *testing.T) {
	t.Parallel()
	fold := newDelegateAttentionFold()
	fold.content["attn_1"] = llm.User("hello")
	fold.content["attn_2"] = llm.User("world")
	fold.order = append(fold.order, "attn_1", "attn_2")
	fold.resolutions["attn_2"] = delegateAttentionConsumed
	fold.resumeGenerations["attn_2"] = 5

	// attn_1 with resume generation 5 conflicts with attn_2 which already has gen 5.
	if err := validateDelegateAttentionResolutions(fold, []string{"attn_1"}, delegateAttentionConsumed, 5); err == nil {
		t.Fatal("resume generation already claimed by another attention should error")
	}
}

func TestFoldDelegateDeliveryCommits(t *testing.T) {
	t.Parallel()
	// Empty commits -> no-op.
	fold := newDelegateAttentionFold()
	turn := schema.NewTurn(schema.TurnToolResults, llm.Message{})
	if err := foldDelegateDeliveryCommits(&fold, turn); err != nil {
		t.Fatalf("empty commits: %v", err)
	}
	// Commits on a non-tool-results turn -> error.
	fold2 := newDelegateAttentionFold()
	badTurn := schema.NewTurn(schema.TurnAssistant, llm.Message{})
	badTurn.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: "c1", DeliveryID: "d1"}}
	if err := foldDelegateDeliveryCommits(&fold2, badTurn); err == nil {
		t.Fatal("commits on non-tool-results turn should error")
	}
	// Commit with empty identity -> error.
	fold3 := newDelegateAttentionFold()
	toolTurn := schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1"}},
	}})
	toolTurn.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: "", DeliveryID: "d1"}}
	if err := foldDelegateDeliveryCommits(&fold3, toolTurn); err == nil {
		t.Fatal("empty tool call ID should error")
	}
	// Commit referencing absent tool call -> error.
	fold4 := newDelegateAttentionFold()
	toolTurn2 := schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1"}},
	}})
	toolTurn2.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: "c_absent", DeliveryID: "d1"}}
	if err := foldDelegateDeliveryCommits(&fold4, toolTurn2); err == nil {
		t.Fatal("absent tool call reference should error")
	}
	// Valid commit is recorded.
	fold5 := newDelegateAttentionFold()
	toolTurn3 := schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1"}},
	}})
	toolTurn3.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: "c1", DeliveryID: "d1"}}
	if err := foldDelegateDeliveryCommits(&fold5, toolTurn3); err != nil {
		t.Fatalf("valid commit: %v", err)
	}
	if fold5.deliveryCommits["d1"] != "c1" {
		t.Fatalf("delivery commit not recorded: %+v", fold5.deliveryCommits)
	}
	// Conflicting delivery for same tool call -> error.
	fold6 := newDelegateAttentionFold()
	toolTurn4 := schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1"}},
	}})
	toolTurn4.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{
		{ToolCallID: "c1", DeliveryID: "d1"},
		{ToolCallID: "c1", DeliveryID: "d2"},
	}
	if err := foldDelegateDeliveryCommits(&fold6, toolTurn4); err == nil {
		t.Fatal("conflicting deliveries for same tool call should error")
	}
	// Conflicting tool call for same delivery -> error.
	fold7 := newDelegateAttentionFold()
	toolTurn5 := schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1"}},
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c2"}},
	}})
	toolTurn5.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{
		{ToolCallID: "c1", DeliveryID: "d1"},
		{ToolCallID: "c2", DeliveryID: "d1"},
	}
	if err := foldDelegateDeliveryCommits(&fold7, toolTurn5); err == nil {
		t.Fatal("conflicting tool calls for same delivery should error")
	}
}

func TestFoldDelegateAttention_InvalidResolutionTurn(t *testing.T) {
	t.Parallel()
	// Attention resolution turn with no resolution -> error.
	invalid := schema.NewTurn(schema.TurnAttentionResolution, llm.System("x"))
	if _, err := foldDelegateAttention([]transcript.Entry{{Turn: invalid}}); err == nil {
		t.Fatal("resolution turn with nil resolution should error")
	}
	// Resolution turn with attentionID set -> error (invalid shape).
	invalid2 := schema.NewTurn(schema.TurnAttentionResolution, llm.System("x"))
	invalid2.AttentionID = "attn_1"
	invalid2.AttentionResolution = &schema.AttentionResolutionInfo{AttentionID: "attn_1", Disposition: "consumed"}
	if _, err := foldDelegateAttention([]transcript.Entry{{Turn: invalid2}}); err == nil {
		t.Fatal("resolution turn with attentionID set should error")
	}
	// Resolution turn with empty resolution attentionID -> error.
	invalid3 := schema.NewTurn(schema.TurnAttentionResolution, llm.System("x"))
	invalid3.AttentionResolution = &schema.AttentionResolutionInfo{AttentionID: "", Disposition: "consumed"}
	if _, err := foldDelegateAttention([]transcript.Entry{{Turn: invalid3}}); err == nil {
		t.Fatal("resolution with empty attentionID should error")
	}
	// Invalid disposition -> error.
	invalid4 := schema.NewTurn(schema.TurnAttentionResolution, llm.System("x"))
	invalid4.AttentionResolution = &schema.AttentionResolutionInfo{AttentionID: "attn_1", Disposition: "unknown"}
	if _, err := foldDelegateAttention([]transcript.Entry{{Turn: invalid4}}); err == nil {
		t.Fatal("invalid disposition should error")
	}
	// Discarded with resume generation -> error.
	invalid5 := schema.NewTurn(schema.TurnAttentionResolution, llm.System("x"))
	invalid5.AttentionResolution = &schema.AttentionResolutionInfo{AttentionID: "attn_1", Disposition: "discarded", ResumeGeneration: 3}
	if _, err := foldDelegateAttention([]transcript.Entry{{Turn: invalid5}}); err == nil {
		t.Fatal("discarded with resume generation should error")
	}
	// Resolution before append -> error.
	invalid6 := schema.NewTurn(schema.TurnAttentionResolution, llm.System("x"))
	invalid6.AttentionResolution = &schema.AttentionResolutionInfo{AttentionID: "attn_missing", Disposition: "consumed"}
	if _, err := foldDelegateAttention([]transcript.Entry{{Turn: invalid6}}); err == nil {
		t.Fatal("resolution before append should error")
	}
}

func TestFoldDelegateAttention_ResumeGenerationConflict(t *testing.T) {
	t.Parallel()
	// Two attentions consumed with the same resume generation -> error.
	attn1 := schema.NewTurn(schema.TurnSteering, llm.User("hello"))
	attn1.AttentionID = "attn_1"
	res1 := schema.NewTurn(schema.TurnAttentionResolution, llm.System("x"))
	res1.AttentionResolution = &schema.AttentionResolutionInfo{AttentionID: "attn_1", Disposition: "consumed", ResumeGeneration: 5}
	attn2 := schema.NewTurn(schema.TurnSteering, llm.User("world"))
	attn2.AttentionID = "attn_2"
	res2 := schema.NewTurn(schema.TurnAttentionResolution, llm.System("x"))
	res2.AttentionResolution = &schema.AttentionResolutionInfo{AttentionID: "attn_2", Disposition: "consumed", ResumeGeneration: 5}
	if _, err := foldDelegateAttention([]transcript.Entry{
		{Turn: attn1}, {Turn: res1}, {Turn: attn2}, {Turn: res2},
	}); err == nil {
		t.Fatal("two attentions with same resume generation should error")
	}
}

func TestFoldDelegateAttention_ConflictingContent(t *testing.T) {
	t.Parallel()
	// Two steering turns with same attentionID but different content -> error.
	attn1 := schema.NewTurn(schema.TurnSteering, llm.User("hello"))
	attn1.AttentionID = "attn_1"
	attn2 := schema.NewTurn(schema.TurnSteering, llm.User("different"))
	attn2.AttentionID = "attn_1"
	if _, err := foldDelegateAttention([]transcript.Entry{
		{Turn: attn1}, {Turn: attn2},
	}); err == nil {
		t.Fatal("conflicting content for same attentionID should error")
	}
}

func TestFoldDelegateAttention_NotASteeringTurn(t *testing.T) {
	t.Parallel()
	// AttentionID on a non-steering turn -> error.
	bad := schema.NewTurn(schema.TurnAssistant, llm.User("hello"))
	bad.AttentionID = "attn_1"
	if _, err := foldDelegateAttention([]transcript.Entry{{Turn: bad}}); err == nil {
		t.Fatal("attentionID on non-steering turn should error")
	}
	// AttentionID on a steering turn with a resolution set -> error.
	bad2 := schema.NewTurn(schema.TurnSteering, llm.User("hello"))
	bad2.AttentionID = "attn_1"
	bad2.AttentionResolution = &schema.AttentionResolutionInfo{AttentionID: "attn_1", Disposition: "consumed"}
	if _, err := foldDelegateAttention([]transcript.Entry{{Turn: bad2}}); err == nil {
		t.Fatal("steering turn with resolution set should error")
	}
}

func TestDelegateAttentionResolutionTurn(t *testing.T) {
	t.Parallel()
	turn := delegateAttentionResolutionTurn("attn_1", delegateAttentionConsumed)
	if turn.Kind != schema.TurnAttentionResolution {
		t.Fatalf("kind = %q, want %q", turn.Kind, schema.TurnAttentionResolution)
	}
	if turn.AttentionResolution == nil || turn.AttentionResolution.AttentionID != "attn_1" || turn.AttentionResolution.Disposition != "consumed" {
		t.Fatalf("resolution = %+v", turn.AttentionResolution)
	}
	if turn.AttentionResolution.ResumeGeneration != 0 {
		t.Errorf("resume generation = %d, want 0", turn.AttentionResolution.ResumeGeneration)
	}
}

func TestDelegateAttentionResolutionTurnForGeneration(t *testing.T) {
	t.Parallel()
	turn := delegateAttentionResolutionTurnForGeneration("attn_1", delegateAttentionConsumed, 42)
	if turn.AttentionResolution == nil || turn.AttentionResolution.ResumeGeneration != 42 {
		t.Fatalf("resume generation = %d, want 42", turn.AttentionResolution.ResumeGeneration)
	}
}

func TestAttentionTransparentTurns(t *testing.T) {
	t.Parallel()
	// No resolution turns -> returns history as-is.
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("hello")),
		schema.NewTurn(schema.TurnAssistant, llm.User("hi")),
	}
	got := attentionTransparentTurns(history)
	if !reflect.DeepEqual(got, history) {
		t.Fatal("history without resolution turns should be returned as-is")
	}
	// With resolution turns -> filtered out.
	resTurn := delegateAttentionResolutionTurn("attn_1", delegateAttentionConsumed)
	historyWithRes := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("hello")),
		resTurn,
		schema.NewTurn(schema.TurnAssistant, llm.User("hi")),
	}
	got = attentionTransparentTurns(historyWithRes)
	if len(got) != 2 {
		t.Fatalf("got %d turns, want 2 (resolution filtered)", len(got))
	}
	if got[0].Kind != schema.TurnUserInput || got[1].Kind != schema.TurnAssistant {
		t.Fatalf("filtered turns = %+v", got)
	}
	// Empty history.
	if got := attentionTransparentTurns(nil); len(got) != 0 {
		t.Fatalf("empty history = %d, want 0", len(got))
	}
}

func TestAttentionTransparentRecentCutoff(t *testing.T) {
	t.Parallel()
	// preserveRecent <= 0 -> returns full length.
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("hello")),
	}
	cutoff, ok := attentionTransparentRecentCutoff(history, 0)
	if !ok || cutoff != 1 {
		t.Fatalf("cutoff=%d ok=%v, want 1 true for preserveRecent<=0", cutoff, ok)
	}
	// Empty history with preserveRecent > 0.
	cutoff, ok = attentionTransparentRecentCutoff(nil, 5)
	if ok || cutoff != 0 {
		t.Fatalf("cutoff=%d ok=%v, want 0 false for empty history", cutoff, ok)
	}
	// History with resolution turns interspersed.
	resTurn := delegateAttentionResolutionTurn("attn_1", delegateAttentionConsumed)
	history = []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first")),
		resTurn,
		schema.NewTurn(schema.TurnAssistant, llm.User("second")),
	}
	// preserveRecent=1: find the cutoff before the last 1 visible turn.
	// History: [user(0), res(1), assistant(2)]. Walking backward, assistant
	// is the 1st visible turn. The function returns the index of the first
	// visible turn (2), so the cutoff is at 2 (everything before it is
	// transparent to resolution filtering).
	cutoff, ok = attentionTransparentRecentCutoff(history, 1)
	if !ok {
		t.Fatalf("expected ok=true, got cutoff=%d", cutoff)
	}
	if cutoff != 2 {
		t.Errorf("cutoff = %d, want 2", cutoff)
	}
	// preserveRecent=2: need both visible turns. The 2nd visible turn is
	// at index 0. Walking from i-1=-1 finds nothing, so returns (0, false).
	cutoff, ok = attentionTransparentRecentCutoff(history, 2)
	if ok || cutoff != 0 {
		t.Fatalf("cutoff=%d ok=%v, want 0 false for preserveRecent=2 (nothing before index 0)", cutoff, ok)
	}
}

func TestFoldDelegateAttention_IdempotentResolution(t *testing.T) {
	t.Parallel()
	attn := schema.NewTurn(schema.TurnSteering, llm.User("hello"))
	attn.AttentionID = "attn_1"
	res := delegateAttentionResolutionTurn("attn_1", delegateAttentionConsumed)
	// Repeating the same resolution is idempotent (no error).
	fold, err := foldDelegateAttention([]transcript.Entry{
		{Turn: attn}, {Turn: res}, {Turn: res},
	})
	if err != nil {
		t.Fatalf("idempotent resolution: %v", err)
	}
	if fold.resolutions["attn_1"] != delegateAttentionConsumed {
		t.Fatalf("resolution = %q, want consumed", fold.resolutions["attn_1"])
	}
}

func TestReadPendingDelegateAttention_MissingFile(t *testing.T) {
	t.Parallel()
	// Non-existent file returns empty fold (not an error).
	ids, err := readPendingDelegateAttention("/nonexistent/path/transcript.jsonl", "session123")
	if err != nil {
		t.Fatalf("missing file: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("ids = %v, want empty", ids)
	}
}

func TestAppendColdDelegateAttentionMessageDurablyWithOpen_InvalidInputs(t *testing.T) {
	t.Parallel()
	// Empty sessionID.
	if _, err := appendColdDelegateAttentionMessageDurablyWithOpen("path", "", "attn_1", llm.User("hi"), testTime, nil); err == nil {
		t.Fatal("empty sessionID should error")
	}
	// Empty attentionID.
	if _, err := appendColdDelegateAttentionMessageDurablyWithOpen("path", "s1", "", llm.User("hi"), testTime, nil); err == nil {
		t.Fatal("empty attentionID should error")
	}
	// Non-user role message.
	if _, err := appendColdDelegateAttentionMessageDurablyWithOpen("path", "s1", "attn_1", llm.System("hi"), testTime, nil); err == nil {
		t.Fatal("non-user role should error")
	}
	// Empty content.
	if _, err := appendColdDelegateAttentionMessageDurablyWithOpen("path", "s1", "attn_1", llm.User(""), testTime, nil); err == nil {
		t.Fatal("empty content should error")
	}
	// Nil opener.
	if _, err := appendColdDelegateAttentionMessageDurablyWithOpen("path", "s1", "attn_1", llm.User("hi"), testTime, nil); err == nil {
		t.Fatal("nil opener should error")
	}
}

func TestAppendColdAttentionResolutionWithOpen_InvalidInputs(t *testing.T) {
	t.Parallel()
	// Empty sessionID.
	if err := appendColdAttentionResolutionWithOpen("path", "", nil, delegateAttentionConsumed, nil); err == nil {
		t.Fatal("empty sessionID should error")
	}
	// Nil opener.
	if err := appendColdAttentionResolutionWithOpen("path", "s1", nil, delegateAttentionConsumed, nil); err == nil {
		t.Fatal("nil opener should error")
	}
}

var testTime = time.Unix(1000, 0).UTC()

// Ensure errors package is used (for potential future assertions).
var _ = errors.Is
