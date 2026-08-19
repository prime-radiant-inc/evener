package main

import (
	"os"
	"strings"
	"testing"
)

// TestTestMainDerivesScrubListFromEnvvarsAll is the RED phase of issue #188:
// cmd/evener/testmain_test.go must derive its TestMain scrub set from
// envvars.All() (filtering EVENER_* + retired vars) rather than hand-clearing
// individual variables. A hand-kept list silently falls behind the product —
// the hub's list missed EVENER_PROVIDERS_CONFIG until a developer exported it
// and hub E2E tests failed on a configured machine.
//
// This source-scanning test pins the contract: the testmain must reference
// envvars.All(), and must not hand-clear EVENER_PROVIDERS_CONFIG by name. The
// GREEN phase replaces the hand-clear with the derived loop.
func TestTestMainDerivesScrubListFromEnvvarsAll(t *testing.T) {
	src, err := os.ReadFile("testmain_test.go")
	if err != nil {
		t.Fatalf("read testmain_test.go: %v", err)
	}
	s := string(src)

	if !strings.Contains(s, "envvars.All()") {
		t.Fatalf("cmd/evener/testmain_test.go does not derive its scrub list from envvars.All(); see issue #188")
	}

	for line := range strings.SplitSeq(s, "\n") {
		if strings.Contains(line, `os.Unsetenv("EVENER_PROVIDERS_CONFIG")`) {
			t.Fatalf("cmd/evener/testmain_test.go hand-clears EVENER_PROVIDERS_CONFIG by name instead of deriving from envvars.All(); see issue #188\noffending line: %s", line)
		}
	}
}
