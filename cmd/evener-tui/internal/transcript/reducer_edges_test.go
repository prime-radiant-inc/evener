package transcript

import (
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/envvars"
)

// TestCloneUsageNil covers the nil path in cloneUsage.
func TestCloneUsageNil(t *testing.T) {
	if got := cloneUsage(nil); got != nil {
		t.Fatalf("cloneUsage(nil) should return nil, got %v", got)
	}
}

// TestCloneUsageValue covers the non-nil path in cloneUsage.
func TestCloneUsageValue(t *testing.T) {
	usage := &appwire.EvenerUsage{InputTokens: 100, OutputTokens: 50}
	got := cloneUsage(usage)
	if got == nil || got.InputTokens != 100 || got.OutputTokens != 50 {
		t.Fatalf("cloneUsage should clone values, got %v", got)
	}
	got.InputTokens = 999
	if usage.InputTokens != 100 {
		t.Fatalf("modifying clone should not affect original")
	}
}

// TestCloneWorktreeNil covers the nil path in cloneWorktree.
func TestCloneWorktreeNil(t *testing.T) {
	if got := cloneWorktree(nil); got != nil {
		t.Fatalf("cloneWorktree(nil) should return nil, got %v", got)
	}
}

// TestCloneWorktreeValue covers the non-nil path in cloneWorktree.
func TestCloneWorktreeValue(t *testing.T) {
	wt := &appwire.JobActivityWorktree{Branch: "test-branch", Path: "/test/path"}
	got := cloneWorktree(wt)
	if got == nil || got.Branch != "test-branch" || got.Path != "/test/path" {
		t.Fatalf("cloneWorktree should clone values, got %v", got)
	}
	got.Branch = "modified"
	if wt.Branch != "test-branch" {
		t.Fatalf("modifying clone should not affect original")
	}
}

// TestMergeSubagentRunNilDst covers the nil-dst path in mergeSubagentRun.
func TestMergeSubagentRunNilDst(t *testing.T) {
	src := SubagentRunInfo{DelegateID: "dlg_123", Status: "running"}
	got := mergeSubagentRun(nil, src)
	if got.DelegateID != "dlg_123" || got.Status != "running" {
		t.Fatalf("mergeSubagentRun(nil, src) should return src, got %+v", got)
	}
}

// TestMergeSubagentRunAllFields covers the path where every src field
// overwrites the dst.
func TestMergeSubagentRunAllFields(t *testing.T) {
	dst := &SubagentRunInfo{DelegateID: "old", JobID: "old", JobType: "old", Status: "old", Reason: "old", Task: "old", TranscriptRef: "old", OriginTurnID: "old", OriginToolCallID: "old", OriginItemID: "old", OutputBytes: 100}
	src := SubagentRunInfo{DelegateID: "new", JobID: "new", JobType: "new", Status: "new", Reason: "new", Task: "new", TranscriptRef: "new", OriginTurnID: "new", OriginToolCallID: "new", OriginItemID: "new", OutputBytes: 200}
	got := mergeSubagentRun(dst, src)
	if got.DelegateID != "new" || got.JobID != "new" || got.JobType != "new" || got.Status != "new" ||
		got.Reason != "new" || got.Task != "new" || got.TranscriptRef != "new" ||
		got.OriginTurnID != "new" || got.OriginToolCallID != "new" || got.OriginItemID != "new" || got.OutputBytes != 200 {
		t.Fatalf("mergeSubagentRun should overwrite all fields, got %+v", got)
	}
}

// TestMergeSubagentRunEmptySrcFields covers the path where empty src fields
// do not overwrite dst.
func TestMergeSubagentRunEmptySrcFields(t *testing.T) {
	dst := &SubagentRunInfo{DelegateID: "keep", Status: "keep", OutputBytes: 100}
	src := SubagentRunInfo{DelegateID: "", Status: "", OutputBytes: 0}
	got := mergeSubagentRun(dst, src)
	if got.DelegateID != "keep" || got.Status != "keep" || got.OutputBytes != 100 {
		t.Fatalf("mergeSubagentRun should not overwrite with empty fields, got %+v", got)
	}
}

// TestSystemMessageItemTextEmpty covers the empty-text path.
func TestSystemMessageItemTextEmpty(t *testing.T) {
	if got := systemMessageItemText(appwire.ThreadItem{Text: "  "}); got != "" {
		t.Fatalf("systemMessageItemText with empty text should return empty, got %q", got)
	}
}

// TestSystemMessageItemTextWithDescription covers the path with a description.
func TestSystemMessageItemTextWithDescription(t *testing.T) {
	got := systemMessageItemText(appwire.ThreadItem{Text: "hello", Description: "context"})
	if got != "context\nhello" {
		t.Fatalf("systemMessageItemText with description should return 'context\\nhello', got %q", got)
	}
}

// TestSystemMessageItemTextHookDescription covers the "Hook" description path.
func TestSystemMessageItemTextHookDescription(t *testing.T) {
	got := systemMessageItemText(appwire.ThreadItem{Text: "  multiple   spaces  ", Description: "Hook"})
	if got != "multiple spaces" {
		t.Fatalf("systemMessageItemText with Hook description should join fields, got %q", got)
	}
}

// TestSystemMessageItemTextNoDescription covers the no-description path.
func TestSystemMessageItemTextNoDescription(t *testing.T) {
	got := systemMessageItemText(appwire.ThreadItem{Text: "hello"})
	if got != "hello" {
		t.Fatalf("systemMessageItemText without description should return text, got %q", got)
	}
}

// TestUserMessageItemTextEmpty covers the empty path.
func TestUserMessageItemTextEmpty(t *testing.T) {
	if got := userMessageItemText(appwire.ThreadItem{}); got != "" {
		t.Fatalf("userMessageItemText with no text and no images should return empty, got %q", got)
	}
}

// TestUserMessageItemTextWithText covers the text path.
func TestUserMessageItemTextWithText(t *testing.T) {
	got := userMessageItemText(appwire.ThreadItem{Text: "hello"})
	if got != "hello" {
		t.Fatalf("userMessageItemText with text should return text, got %q", got)
	}
}

// TestUserMessageItemTextWithImages covers the images path.
func TestUserMessageItemTextWithImages(t *testing.T) {
	got := userMessageItemText(appwire.ThreadItem{Images: []appwire.InputItem{{}}})
	if got != "[image]" {
		t.Fatalf("userMessageItemText with 1 image should return '[image]', got %q", got)
	}
	got = userMessageItemText(appwire.ThreadItem{Images: []appwire.InputItem{{}, {}}})
	if got != "[2 images]" {
		t.Fatalf("userMessageItemText with 2 images should return '[2 images]', got %q", got)
	}
}

// TestSubagentTerminalStatus covers the terminal-status detector.
func TestSubagentTerminalStatus(t *testing.T) {
	for _, status := range []string{"completed", "done", "failed", "cancelled", "stopped", "succeeded", "exhausted", "  Completed  ", "FAILED"} {
		if !subagentTerminalStatus(status) {
			t.Errorf("subagentTerminalStatus(%q) should be true", status)
		}
	}
	for _, status := range []string{"running", "idle", "", "pending", "active"} {
		if subagentTerminalStatus(status) {
			t.Errorf("subagentTerminalStatus(%q) should be false", status)
		}
	}
}

// TestFirstNonEmptyString covers the firstNonEmptyString function.
func TestFirstNonEmptyString(t *testing.T) {
	if got := envvars.FirstNonEmpty("", "  ", "hello", "world"); got != "hello" {
		t.Fatalf("firstNonEmptyString should return 'hello', got %q", got)
	}
	if got := envvars.FirstNonEmpty("", "  ", "\t\n"); got != "" {
		t.Fatalf("firstNonEmptyString with all empty should return empty, got %q", got)
	}
	if got := envvars.FirstNonEmpty("first"); got != "first" {
		t.Fatalf("firstNonEmptyString with first non-empty should return 'first', got %q", got)
	}
}

// TestImageItemsPlaceholderMulti covers the multi-image path (0 and 1 are
// already covered by existing tests).
func TestImageItemsPlaceholderMulti(t *testing.T) {
	if got := ImageItemsPlaceholder([]appwire.InputItem{{}, {}}); got != "[2 images]" {
		t.Fatalf("ImageItemsPlaceholder(2) should return '[2 images]', got %q", got)
	}
}

// TestActiveReasoningIndexEmptyID covers the empty-itemID path.
func TestActiveReasoningIndexEmptyID(t *testing.T) {
	r := &TranscriptReducer{}
	if _, ok := r.activeReasoningIndex(""); ok {
		t.Fatalf("activeReasoningIndex('') should return false")
	}
}

// TestActiveReasoningIndexNotFound covers the not-found path.
func TestActiveReasoningIndexNotFound(t *testing.T) {
	r := &TranscriptReducer{}
	if _, ok := r.activeReasoningIndex("nonexistent"); ok {
		t.Fatalf("activeReasoningIndex with nonexistent ID should return false")
	}
}

// TestShiftActiveIndicesAfterRemoval covers the index-shift function.
func TestShiftActiveIndicesAfterRemoval(t *testing.T) {
	r := &TranscriptReducer{
		activeMessages: map[string]int{"a": 0, "b": 3, "c": 5},
		activeTools:    map[string]int{"x": 1, "y": 4},
	}
	r.shiftActiveIndicesAfterRemoval(2)
	if r.activeMessages["a"] != 0 {
		t.Fatalf("index before removal should not change, got %d", r.activeMessages["a"])
	}
	if r.activeMessages["b"] != 2 {
		t.Fatalf("index after removal should decrement, got %d", r.activeMessages["b"])
	}
	if r.activeMessages["c"] != 4 {
		t.Fatalf("index after removal should decrement, got %d", r.activeMessages["c"])
	}
	if r.activeTools["x"] != 1 {
		t.Fatalf("tool index before removal should not change, got %d", r.activeTools["x"])
	}
	if r.activeTools["y"] != 3 {
		t.Fatalf("tool index after removal should decrement, got %d", r.activeTools["y"])
	}
}

// TestSubagentRunFromToolItemEmptyOutput covers the empty-raw path.
func TestSubagentRunFromToolItemEmptyOutput(t *testing.T) {
	got := subagentRunFromToolItem(appwire.ThreadItem{})
	if got.DelegateID != "" || got.JobType != "" {
		t.Fatalf("subagentRunFromToolItem with empty item should return zero, got %+v", got)
	}
}

// TestSubagentRunFromToolItemInvalidJSONOutput covers the invalid-JSON path.
func TestSubagentRunFromToolItemInvalidJSONOutput(t *testing.T) {
	got := subagentRunFromToolItem(appwire.ThreadItem{Raw: []byte("not json")})
	if got.DelegateID != "" || got.JobType != "" {
		t.Fatalf("subagentRunFromToolItem with invalid JSON should return zero, got %+v", got)
	}
}

// TestSubagentRunFromToolItemFromOutput covers the path where raw is empty
// but Output contains JSON.
func TestSubagentRunFromToolItemFromOutput(t *testing.T) {
	got := subagentRunFromToolItem(appwire.ThreadItem{Output: `{"delegate_id":"dlg_1","type":"shell","status":"running"}`})
	if got.DelegateID != "dlg_1" || got.JobType != "shell" || got.Status != "running" {
		t.Fatalf("subagentRunFromToolItem from output should parse, got %+v", got)
	}
}

// TestSubagentRunFromToolItemWithTotalBytes covers the path where
// OutputBytes is 0 and TotalBytes is used instead.
func TestSubagentRunFromToolItemWithTotalBytes(t *testing.T) {
	got := subagentRunFromToolItem(appwire.ThreadItem{Raw: []byte(`{"delegate_id":"dlg_1","total_bytes":500}`)})
	if got.OutputBytes != 500 {
		t.Fatalf("OutputBytes should be 500 from TotalBytes, got %d", got.OutputBytes)
	}
}

// TestActiveMessageIndexEmptyID covers the empty-ID path.
func TestActiveMessageIndexEmptyID(t *testing.T) {
	r := &TranscriptReducer{}
	if _, ok := r.activeMessageIndex(appwire.ThreadItem{ID: ""}); ok {
		t.Fatalf("activeMessageIndex with empty ID should return false")
	}
}

// TestMessageIndexByItemIDEmptyID covers the empty-ID path.
func TestMessageIndexByItemIDEmptyID(t *testing.T) {
	r := &TranscriptReducer{}
	if _, ok := r.messageIndexByItemID("", MsgAssistant, "", 0); ok {
		t.Fatalf("messageIndexByItemID with empty ID should return false")
	}
}

// TestPendingUserEchoIndexNotFound covers the not-found path.
func TestPendingUserEchoIndexNotFound(t *testing.T) {
	r := &TranscriptReducer{}
	if _, ok := r.pendingUserEchoIndex("nonexistent"); ok {
		t.Fatalf("pendingUserEchoIndex with nonexistent text should return false")
	}
}

// TestPendingUserEchoIndexSkipsPending covers the path where the message
// is pending and should be skipped.
func TestPendingUserEchoIndexSkipsPending(t *testing.T) {
	r := &TranscriptReducer{
		messages: []ChatMessage{
			{Kind: MsgUser, Text: "hello", Pending: true},
		},
	}
	if _, ok := r.pendingUserEchoIndex("hello"); ok {
		t.Fatalf("pendingUserEchoIndex should skip pending messages")
	}
}

// TestPendingUserEchoIndexSkipsFailed covers the path where the message
// is failed and should be skipped.
func TestPendingUserEchoIndexSkipsFailed(t *testing.T) {
	r := &TranscriptReducer{
		messages: []ChatMessage{
			{Kind: MsgUser, Text: "hello", Failed: true},
		},
	}
	if _, ok := r.pendingUserEchoIndex("hello"); ok {
		t.Fatalf("pendingUserEchoIndex should skip failed messages")
	}
}

// TestPendingUserEchoIndexSkipsPendingID covers the path where the message
// has a PendingID and should be skipped.
func TestPendingUserEchoIndexSkipsPendingID(t *testing.T) {
	r := &TranscriptReducer{
		messages: []ChatMessage{
			{Kind: MsgUser, Text: "hello", PendingID: 5},
		},
	}
	if _, ok := r.pendingUserEchoIndex("hello"); ok {
		t.Fatalf("pendingUserEchoIndex should skip messages with PendingID")
	}
}

// TestPendingUserEchoIndexSkipsItemID covers the path where the message
// has an ItemID and should be skipped.
func TestPendingUserEchoIndexSkipsItemID(t *testing.T) {
	r := &TranscriptReducer{
		messages: []ChatMessage{
			{Kind: MsgUser, Text: "hello", ItemID: "item-1"},
		},
	}
	if _, ok := r.pendingUserEchoIndex("hello"); ok {
		t.Fatalf("pendingUserEchoIndex should skip messages with ItemID")
	}
}

// TestPendingUserEchoIndexFound covers the path where the message matches.
func TestPendingUserEchoIndexFound(t *testing.T) {
	r := &TranscriptReducer{
		messages: []ChatMessage{
			{Kind: MsgUser, Text: "hello"},
		},
	}
	idx, ok := r.pendingUserEchoIndex("hello")
	if !ok || idx != 0 {
		t.Fatalf("pendingUserEchoIndex should find 'hello' at index 0, got %d, %v", idx, ok)
	}
}
