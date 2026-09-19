package agent

import (
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

func TestManagedPreparationNeverRepairsRejectedRaw(t *testing.T) {
	tr := &tool.RegisteredTool{ValidateRaw: func(json.RawMessage) error { return errors.New("strict rejection") }}
	got := prepareToolCall(llm.ToolCallData{Name: "managed", Arguments: json.RawMessage(`{"n":1`)}, tr, nil, "managed", "communicate", "")
	if got.PrevalErr == "" || !got.RawArgumentsRejected || len(got.Changes) != 0 {
		t.Fatalf("preparation=%+v", got)
	}
}

func TestManagedHookUpdatePreservesUntouchedRawValues(t *testing.T) {
	call := llm.ToolCallData{Arguments: json.RawMessage(`{"n":9007199254740993,"e":1e+03,"label":"old"}`)}
	if err := applyUpdatedManagedToolInput(&call, map[string]any{"label": "new"}); err != nil {
		t.Fatal(err)
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(call.Arguments, &values); err != nil {
		t.Fatal(err)
	}
	if string(values["n"]) != "9007199254740993" || string(values["e"]) != "1e+03" || string(values["label"]) != `"new"` {
		t.Fatalf("lost original value: %s", call.Arguments)
	}
}
