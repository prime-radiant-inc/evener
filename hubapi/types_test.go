package hubapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSpawnRequestMarshalUsesPromptField(t *testing.T) {
	data, err := json.Marshal(SpawnRequest{Prompt: "ship it", Model: "openai/gpt-5"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, `"prompt":"ship it"`) {
		t.Fatalf("missing prompt field: %s", got)
	}
	if strings.Contains(got, `"task"`) {
		t.Fatalf("current spawn request should not emit legacy task field: %s", got)
	}
}

func TestSpawnRequestMarshalIncludesHarness(t *testing.T) {
	data, err := json.Marshal(SpawnRequest{Prompt: "ship it", Harness: "codex-managed"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(data); !strings.Contains(got, `"harness":"codex-managed"`) {
		t.Fatalf("missing harness field: %s", got)
	}
}

func TestSpawnRequestMarshalCanEmitLegacyTaskField(t *testing.T) {
	data, err := json.Marshal(SpawnRequest{Task: "ship it"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(data); !strings.Contains(got, `"task":"ship it"`) {
		t.Fatalf("missing legacy task field: %s", got)
	}
}

func TestTreeNode_AskPendingRoundTrips(t *testing.T) {
	n := TreeNode{SessionID: "01A", State: "awaiting", AskPending: true}
	data, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"ask_pending":true`) {
		t.Fatalf("expected ask_pending:true in wire JSON, got %s", data)
	}
	var got TreeNode
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !got.AskPending {
		t.Fatal("round-trip must preserve AskPending")
	}
}

func TestTreeNode_MoreSubagentsRoundTrips(t *testing.T) {
	data, err := json.Marshal(TreeNode{MoreSubagents: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"more_subagents":10`) {
		t.Fatalf("expected more_subagents:10 in wire JSON, got %s", data)
	}
	var got TreeNode
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.MoreSubagents != 10 {
		t.Fatalf("MoreSubagents=%d, want 10", got.MoreSubagents)
	}
}

// TestTreeNode_UpdatedAtAlwaysShipsOnWire locks in that UpdatedAt has no
// "omitempty" tag: encoding/json can never omit a struct value regardless of
// the tag, so the tag was already a no-op lie. The key ships even for the
// zero time.Time.
func TestTreeNode_UpdatedAtAlwaysShipsOnWire(t *testing.T) {
	data, err := json.Marshal(TreeNode{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"updated_at":`) {
		t.Fatalf("expected updated_at key present even for zero time.Time, got %s", data)
	}

	data, err = json.Marshal(TreeNode{UpdatedAt: time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"updated_at":"2026-07-25T12:00:00Z"`) {
		t.Fatalf("expected populated updated_at, got %s", data)
	}
}
