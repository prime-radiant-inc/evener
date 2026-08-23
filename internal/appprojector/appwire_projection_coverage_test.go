package appprojector

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestDelegateActivityAfterEmptyCandidate(t *testing.T) {
	if delegateActivityAfter("", "2024-01-01T00:00:00Z") {
		t.Fatal("empty candidate should return false")
	}
}

func TestDelegateActivityAfterEmptyCurrent(t *testing.T) {
	if !delegateActivityAfter("2024-01-01T00:00:00Z", "") {
		t.Fatal("non-empty candidate with empty current should return true")
	}
}

func TestDelegateActivityAfterNewerCandidate(t *testing.T) {
	if !delegateActivityAfter("2024-01-02T00:00:00Z", "2024-01-01T00:00:00Z") {
		t.Fatal("newer candidate should return true")
	}
}

func TestDelegateActivityAfterOlderCandidate(t *testing.T) {
	if delegateActivityAfter("2024-01-01T00:00:00Z", "2024-01-02T00:00:00Z") {
		t.Fatal("older candidate should return false")
	}
}

func TestDelegateActivityAfterInvalidTimestamp(t *testing.T) {
	if delegateActivityAfter("not-a-timestamp", "2024-01-01T00:00:00Z") {
		t.Fatal("invalid candidate should return false")
	}
	if delegateActivityAfter("2024-01-01T00:00:00Z", "not-a-timestamp") {
		t.Fatal("invalid current should return false")
	}
}

func TestDelegateActivityAfterSameTimestamp(t *testing.T) {
	ts := "2024-01-01T00:00:00Z"
	if delegateActivityAfter(ts, ts) {
		t.Fatal("same timestamp should return false (not strictly after)")
	}
}

func TestCloneBoolPointerNil(t *testing.T) {
	if cloneBoolPointer(nil) != nil {
		t.Fatal("nil should return nil")
	}
}

func TestCloneBoolPointerValue(t *testing.T) {
	v := true
	got := cloneBoolPointer(&v)
	if got == nil || !*got {
		t.Fatal("should clone true")
	}
	*got = false
	if !v {
		t.Fatal("modifying clone should not affect original")
	}
}

func TestCloneInt64PointerNil(t *testing.T) {
	if cloneInt64Pointer(nil) != nil {
		t.Fatal("nil should return nil")
	}
}

func TestCloneInt64PointerValue(t *testing.T) {
	v := int64(42)
	got := cloneInt64Pointer(&v)
	if got == nil || *got != 42 {
		t.Fatal("should clone 42")
	}
	*got = 99
	if v != 42 {
		t.Fatal("modifying clone should not affect original")
	}
}

func TestCloneAppwireDelegateInfo(t *testing.T) {
	original := appwire.EvenerDelegateInfo{
		DelegateID:       "dlg_1",
		Message:          json.RawMessage(`{"x":1}`),
		Warnings:         []string{"w1", "w2"},
		StructuredValid:  &[]bool{true}[0],
		Usage:            &appwire.EvenerUsage{InputTokens: 100},
		Worktree:         &appwire.JobActivityWorktree{Path: "/tmp"},
	}
	clone := cloneAppwireDelegateInfo(original)
	if clone.DelegateID != "dlg_1" {
		t.Fatal("DelegateID should be preserved")
	}
	// Verify slices are cloned, not shared
	clone.Warnings[0] = "modified"
	if original.Warnings[0] != "w1" {
		t.Fatal("original should be unaffected by clone modification")
	}
	// Verify usage is cloned
	if clone.Usage == nil {
		t.Fatal("Usage should be cloned")
	}
	clone.Usage.InputTokens = 999
	if original.Usage.InputTokens != 100 {
		t.Fatal("original Usage should be unaffected")
	}
	// Verify worktree is cloned
	if clone.Worktree == nil {
		t.Fatal("Worktree should be cloned")
	}
	clone.Worktree.Path = "/other"
	if original.Worktree.Path != "/tmp" {
		t.Fatal("original Worktree should be unaffected")
	}
}

func TestMergeAppwireDelegateInfoNewIncoming(t *testing.T) {
	current := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1}
	incoming := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 2, Status: "running"}
	merged, changed := mergeAppwireDelegateInfo(current, incoming)
	if !changed {
		t.Fatal("higher projection revision should trigger merge")
	}
	if merged.ProjectionRevision != 2 || merged.Status != "running" {
		t.Fatal("should merge incoming fields")
	}
}

func TestMergeAppwireDelegateInfoSameRevisionNewerActivity(t *testing.T) {
	current := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1, LatestActivityAt: "2024-01-02T00:00:00Z"}
	incoming := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1, LatestActivityAt: "2024-01-03T00:00:00Z"}
	merged, changed := mergeAppwireDelegateInfo(current, incoming)
	if !changed {
		t.Fatal("newer activity at same revision should trigger update")
	}
	if merged.LatestActivityAt != "2024-01-03T00:00:00Z" {
		t.Fatalf("should use newer activity timestamp, got %q", merged.LatestActivityAt)
	}
}

func TestMergeAppwireDelegateInfoNoChange(t *testing.T) {
	current := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1, LatestActivityAt: "2024-01-02T00:00:00Z"}
	incoming := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1, LatestActivityAt: "2024-01-01T00:00:00Z"}
	merged, changed := mergeAppwireDelegateInfo(current, incoming)
	if changed {
		t.Fatal("older activity at same revision should not trigger update")
	}
	if merged.LatestActivityAt != "2024-01-02T00:00:00Z" {
		t.Fatal("should keep current activity timestamp")
	}
}

func TestMergeAppwireDelegateInfoEmptyCurrent(t *testing.T) {
	current := appwire.EvenerDelegateInfo{}
	incoming := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1}
	merged, changed := mergeAppwireDelegateInfo(current, incoming)
	if !changed {
		t.Fatal("empty current DelegateID should trigger merge")
	}
	if merged.DelegateID != "dlg_1" {
		t.Fatal("should use incoming")
	}
}

func TestUseSkillNameFromArgsSkillName(t *testing.T) {
	got := useSkillNameFromArgs(`{"skill_name":"my-skill"}`)
	if got != "my-skill" {
		t.Fatalf("expected 'my-skill', got %q", got)
	}
}

func TestUseSkillNameFromArgsName(t *testing.T) {
	got := useSkillNameFromArgs(`{"name":"other-skill"}`)
	if got != "other-skill" {
		t.Fatalf("expected 'other-skill', got %q", got)
	}
}

func TestUseSkillNameFromArgsSkillNamePreferred(t *testing.T) {
	got := useSkillNameFromArgs(`{"skill_name":"preferred","name":"fallback"}`)
	if got != "preferred" {
		t.Fatalf("skill_name should take precedence, got %q", got)
	}
}

func TestUseSkillNameFromArgsInvalidJSON(t *testing.T) {
	if useSkillNameFromArgs("not json") != "" {
		t.Fatal("invalid JSON should return empty string")
	}
}

func TestUseSkillNameFromArgsNoSkillName(t *testing.T) {
	if useSkillNameFromArgs(`{"other":"value"}`) != "" {
		t.Fatal("missing skill_name should return empty string")
	}
}

func TestUseSkillNameFromArgsTrimSpace(t *testing.T) {
	got := useSkillNameFromArgs(`{"skill_name":"  trimmed  "}`)
	if got != "trimmed" {
		t.Fatalf("expected 'trimmed', got %q", got)
	}
}

func TestSkillActivationRaw(t *testing.T) {
	raw := skillActivationRaw("test-skill")
	var payload struct {
		SkillActivation struct {
			Name string `json:"name"`
			Text string `json:"text"`
		} `json:"skillActivation"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("skillActivationRaw should produce valid JSON: %v", err)
	}
	if payload.SkillActivation.Name != "test-skill" {
		t.Fatalf("expected name 'test-skill', got %q", payload.SkillActivation.Name)
	}
	if !strings.Contains(payload.SkillActivation.Text, "test-skill") {
		t.Fatalf("text should contain skill name, got %q", payload.SkillActivation.Text)
	}
}
