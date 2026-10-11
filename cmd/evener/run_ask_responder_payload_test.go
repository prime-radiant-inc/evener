package main

import (
	"strings"
	"testing"
)

// TestAskResponderPayloadNamesUnrecordedArguments: the loop builds a payload
// only while a question is pending, so an empty argument list means the
// pending asks were rebuilt from the transcript (after a failed turn) and
// their call arguments were never recorded. The diagnostic must say that,
// not that nothing is pending.
func TestAskResponderPayloadNamesUnrecordedArguments(t *testing.T) {
	t.Parallel()
	_, err := askResponderPayload(nil)
	if err == nil {
		t.Fatal("want an error for a pending ask with no recorded arguments")
	}
	if strings.Contains(err.Error(), "no pending") || !strings.Contains(err.Error(), "not recorded") {
		t.Fatalf("error = %q, want it to say the pending questions' arguments were not recorded", err)
	}
}
