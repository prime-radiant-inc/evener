package repoaudit

import (
	"os"
	"strings"
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

// quietThresholdHasOneSource keeps the quiet threshold in one place: the
// shared client's QUIET_AFTER_MS, which both consumers import. A local 20_000
// literal is the copy #4520 collapsed and would drift the moment the rule
// changes.
func quietThresholdHasOneSource(a *audit) {
	for _, tc := range quietThresholdConsumers {
		body, err := os.ReadFile(tc.path)
		if err != nil {
			a.Fatalf("read %s: %v", tc.path, err)
		}
		source := string(body)
		if !strings.Contains(source, "QUIET_AFTER_MS") {
			a.Errorf("%s no longer names QUIET_AFTER_MS; the quiet threshold must come from the shared client", tc.path)
		}
		for _, literal := range []string{"= 20_000", "= 20000"} {
			if strings.Contains(source, literal) {
				a.Errorf("%s declares its own quiet threshold (%q); import QUIET_AFTER_MS from the shared client instead, so every surface flips at the same instant", tc.path, literal)
			}
		}
	}
}
