package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEnvironmentInfo_WorkspaceAlwaysShipsOnWire locks in that Workspace has
// no "omitempty" tag: encoding/json can never omit a struct value regardless
// of the tag, so the "workspace" key ships even for a zero-valued
// WorkspaceInfo (e.g. the synthetic EnvironmentInfo built for non-native
// sessions, which never sets Workspace at all). Consumers (session_prompts.go)
// read Workspace.Tree/Workspace.BuildInfo directly and don't distinguish an
// absent Workspace from a present-but-empty one, so the tag was already a
// no-op lie.
func TestEnvironmentInfo_WorkspaceAlwaysShipsOnWire(t *testing.T) {
	data, err := json.Marshal(EnvironmentInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"workspace":{`) {
		t.Fatalf("expected workspace key present even when unset, got %s", data)
	}
}
