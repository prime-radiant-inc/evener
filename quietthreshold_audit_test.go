package evener_test

import (
	"os"
	"strings"
	"testing"
)

// quietThresholdConsumer is a surface that renders the shared "gone quiet"
// rule. It may name the constant, but it may not carry a second 20-second
// literal: #4520 found the shared client's DELEGATE_QUIET_AFTER_MS and the web
// transcript's QUIET_THRESHOLD_MS each spelling the same value.
type quietThresholdConsumer struct {
	path string
}

var quietThresholdConsumers = []quietThresholdConsumer{
	{path: "appwire-client/typescript/delegateDetails.ts"},
	{path: "cmd/evener-hub/frontend/src/panes/session/transcript/flow/liveness.ts"},
}

// TestQuietThresholdHasOneSource keeps the quiet threshold in one place: the
// shared client's QUIET_AFTER_MS, which both consumers import. A local 20_000
// literal is the copy #4520 collapsed and would drift the moment the rule
// changes.
func TestQuietThresholdHasOneSource(t *testing.T) {
	t.Parallel()
	for _, tc := range quietThresholdConsumers {
		body, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatalf("read %s: %v", tc.path, err)
		}
		source := string(body)
		if !strings.Contains(source, "QUIET_AFTER_MS") {
			t.Errorf("%s no longer names QUIET_AFTER_MS; the quiet threshold must come from the shared client", tc.path)
		}
		for _, literal := range []string{"= 20_000", "= 20000"} {
			if strings.Contains(source, literal) {
				t.Errorf("%s declares its own quiet threshold (%q); import QUIET_AFTER_MS from the shared client instead, so every surface flips at the same instant", tc.path, literal)
			}
		}
	}
}
