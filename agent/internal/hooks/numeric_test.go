package hooks

import (
	"encoding/json"
	"testing"
)

func TestHookUpdatedInputExactNumbers(t *testing.T) {
	raw := `{"continue":true,"hookSpecificOutput":{"permissionDecision":"allow","updatedInput":{"large":9007199254740993,"nested":[1.0000000000000001,1e-1000000000]}}}`
	result := parseHookOutput(raw, 0)
	if !result.Continue || result.PermissionDecision != "allow" {
		t.Fatalf("control fields changed: %+v", result)
	}
	encoded, err := json.Marshal(result.UpdatedInput)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"large":9007199254740993,"nested":[1.0000000000000001,1e-1000000000]}`
	if string(encoded) != want {
		t.Fatalf("hook response numbers changed: %s want %s", encoded, want)
	}
	for _, bad := range []string{`{"hookSpecificOutput":{"updatedInput":{"n":1e400}}}`, raw + ` {}`} {
		parsed := parseHookOutput(bad, 0)
		if parsed.UpdatedInput != nil || parsed.SystemMessage != bad {
			t.Fatalf("invalid hook JSON accepted: %+v", parsed)
		}
	}
}
