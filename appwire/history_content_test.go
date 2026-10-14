package appwire

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestThreadContentDefaultsAndExplicitFalse(t *testing.T) {
	t.Parallel()
	var defaults, enabled, disabled ThreadReadParams
	for raw, dst := range map[string]*ThreadReadParams{
		`{}`: &defaults,
		`{"includeConversation":true,"includeThinking":true,"includeToolCalls":true,"includeToolCallIntents":true,"includeSystemMessages":true,"includeDiagnostics":true}`:       &enabled,
		`{"includeConversation":false,"includeThinking":false,"includeToolCalls":false,"includeToolCallIntents":false,"includeSystemMessages":false,"includeDiagnostics":false}`: &disabled,
	} {
		if err := json.Unmarshal([]byte(raw), dst); err != nil {
			t.Fatal(err)
		}
	}
	if defaults.Selection() != enabled.Selection() || defaults.Selection() != 0 {
		t.Fatal("omitted and enabled selections differ")
	}
	if disabled.Selection() == defaults.Selection() {
		t.Fatal("explicit exclusions were lost")
	}
	raw, err := json.Marshal(disabled)
	if err != nil {
		t.Fatal(err)
	}
	var listed ThreadTurnsListParams
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "includeTurns")
	raw, _ = json.Marshal(fields)
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Selection() != disabled.Selection() {
		t.Fatal("read/list selection differs")
	}
}

func TestThreadContentIndependentCategories(t *testing.T) {
	t.Parallel()
	items := []ThreadItem{
		{Type: "userMessage", ID: "u"}, {Type: "agentMessage", ID: "a"},
		{Type: "steering", ID: "s", Source: "user"}, {Type: "reasoning", ID: "r"},
		{Type: "commandExecution", ID: "t", Description: "intent-data", ArgumentsJSON: `{"x":1}`, Output: "result-data"},
		{Type: "systemMessage", ID: "m"}, {Type: "steering", ID: "n"},
	}
	for _, tc := range []struct {
		field  string
		absent []string
	}{
		{"includeConversation", []string{"u", "a", "s"}},
		{"includeThinking", []string{"r"}},
		{"includeSystemMessages", []string{"m", "n"}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			var params ThreadReadParams
			if err := json.Unmarshal([]byte(`{"`+tc.field+`":false}`), &params); err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				got, keep := params.Selection().Item(item)
				want := true
				for _, id := range tc.absent {
					if item.ID == id {
						want = false
					}
				}
				if keep != want {
					t.Errorf("item %s: keep=%v want=%v", item.ID, keep, want)
				}
				if keep && !reflect.DeepEqual(got, item) {
					t.Errorf("included item %s changed", item.ID)
				}
			}
		})
	}
}

func TestThreadContentToolIntentsRetainIdentityWithoutToolPayload(t *testing.T) {
	t.Parallel()
	no := false
	position := ThreadItemPosition{Entry: 7, Item: 2}
	item := ThreadItem{Type: "commandExecution", ID: "t", TranscriptKey: "key", Position: &position, TurnID: "turn", CallID: "call", ToolName: "shell", Description: "intent-data", ArgumentsJSON: `{"intent":"intent-data","command":"payload"}`, Output: "output-data", Error: "error-data", Raw: json.RawMessage(`{"private":"state"}`), Version: 9, Status: "completed"}
	selection := ThreadContentParams{IncludeToolCalls: &no}.Selection()
	got, keep := selection.Item(item)
	if !keep || got.ID != item.ID || got.TranscriptKey != item.TranscriptKey || got.Position != item.Position || got.Description != item.Description || got.Version != item.Version || got.Status != item.Status {
		t.Fatalf("intent identity/lifecycle lost: %+v", got)
	}
	if got.ArgumentsJSON != "" || got.Output != "" || got.Error != "" || len(got.Raw) != 0 || len(got.OutputImages) != 0 {
		t.Fatal("intent contains tool payload")
	}
	if item.Output == "" || len(item.Raw) == 0 {
		t.Fatal("projection mutated input")
	}
	item.Description = ""
	if _, keep := selection.Item(item); keep {
		t.Fatal("invented an intent")
	}
	item.Description = "intent-data"
	selection = ThreadContentParams{IncludeToolCallIntents: &no}.Selection()
	got, keep = selection.Item(item)
	if !keep || got.Description != "" || got.Output != item.Output || got.ArgumentsJSON != item.ArgumentsJSON {
		t.Fatal("intent flag changed tool payload")
	}
	selection = ThreadContentParams{IncludeToolCalls: &no, IncludeToolCallIntents: &no}.Selection()
	if _, keep := selection.Item(item); keep {
		t.Fatal("excluded call returned")
	}
}
