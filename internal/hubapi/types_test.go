package hubapi

import (
	"encoding/json"
	"strings"
	"testing"
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
