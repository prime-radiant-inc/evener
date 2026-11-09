package evener_test

import (
	"os"
	"strings"
	"testing"
)

// TestCustomContentSelectionHasOneRule keeps the rule that seeds a Custom
// transcript selection in the shared client (#3668). Web remembers the last
// custom vector; native seeds from the current preset. That difference is a
// call-shape difference (whether a remembered vector is passed), not a reason
// for each surface to spell the transition itself.
func TestCustomContentSelectionHasOneRule(t *testing.T) {
	t.Parallel()
	root, err := os.ReadFile("appwire-client/typescript/index.ts")
	if err != nil {
		t.Fatalf("read the package root: %v", err)
	}
	if strings.Contains(string(root), "\n  presetContent,") {
		t.Error("appwire-client still exports presetContent at the package root; surfaces can re-derive a preset vector instead of asking contentVectorForConfig/customContentForSelection (#3668)")
	}
	if !strings.Contains(string(root), "customContentForSelection") {
		t.Error("the package root does not export customContentForSelection; the shared transition rule must be reachable by both surfaces")
	}
	for _, path := range []string{
		"cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptDetailEditor.tsx",
		"mobile-native/src/hub/DetailLevelPage.tsx",
	} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(body), "customContentForSelection") {
			t.Errorf("%s does not call customContentForSelection; the Custom seed rule belongs to the shared client (#3668)", path)
		}
	}
}
