package main

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// TestTurnFailureSummaryWithholdsProviderBody pins the formatter directly: a
// provider body that looks like a leaked credential never appears in the
// summary, which names the kind and status instead (#3418).
func TestTurnFailureSummaryWithholdsProviderBody(t *testing.T) {
	const canary = "Incorrect API key provided: sk-svcac-secret"
	err := llm.ErrorFromHTTPStatus("openai", 401, canary, map[string]any{"error": map[string]any{"message": canary}}, nil)
	got := turnFailureSummary(err)
	if !strings.Contains(got, "401") || !strings.Contains(got, llm.KindAuthentication.String()) {
		t.Fatalf("turnFailureSummary = %q, want it to name the status and the kind", got)
	}
	if strings.Contains(got, "sk-svcac") {
		t.Fatalf("turnFailureSummary = %q, want the provider body withheld", got)
	}
}

// TestTurnFailureSummaryKeepsEvenerOwnErrors pins the other half: an error
// Evener itself produced carries no provider body, so the log keeps its
// message instead of collapsing it to "unknown" (#3418).
func TestTurnFailureSummaryKeepsEvenerOwnErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"plain error", errors.New("session is closed")},
		{"configuration diagnosis", &llm.ConfigurationError{Message: "model is not configured"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := turnFailureSummary(tc.err); got != tc.err.Error() {
				t.Fatalf("turnFailureSummary = %q, want the error's own message %q", got, tc.err.Error())
			}
		})
	}
	// A sign-in failure is summarized as one, whatever the diagnosis around
	// it said.
	const diagnosis = "diagnosis-fixture"
	got := turnFailureSummary(&llm.ConfigurationError{Message: diagnosis, Cause: llm.ErrSignInRequired})
	if got == "" || strings.Contains(got, diagnosis) {
		t.Fatalf("turnFailureSummary = %q, want the sign-in summary rather than the diagnosis", got)
	}
}

// TestTurnFailureSummarySummarizesProviderWrappingConfigurationError pins the
// boundary the other way: a configuration diagnosis can wrap a provider
// failure and quote its body in the message (llm/providers/google's Vertex
// regional-404 remedy does). That body must not reach the log, so the summary
// falls back to the kind (#3418).
func TestTurnFailureSummarySummarizesProviderWrappingConfigurationError(t *testing.T) {
	const canary = "Publisher model leaked-secret-body was not found"
	err := &llm.ConfigurationError{
		Message: "instance \"vertex\": use global (set GOOGLE_VERTEX_LOCATION); provider said: " + canary,
		Cause:   llm.ErrorFromHTTPStatus("vertex", 404, canary, nil, nil),
	}
	got := turnFailureSummary(err)
	if strings.Contains(got, "leaked-secret-body") {
		t.Fatalf("turnFailureSummary = %q, want the provider body withheld", got)
	}
	if got != llm.KindNotFound.String() {
		t.Fatalf("turnFailureSummary = %q, want the failure's kind only", got)
	}
}

// TestServe_TurnFailureLogWithholdsProviderBody drives a real daemon through a
// turn its provider fails and reads the daemon log. The failed-turn line must
// name the failure's kind and HTTP status, not the provider's own error body,
// which can carry a credential fragment or the user's request text (#3418).
// Serial: it swaps the process-global stderr.
func TestServe_TurnFailureLogWithholdsProviderBody(t *testing.T) {
	workDir, stateDir, runDir := t.TempDir(), t.TempDir(), t.TempDir()

	procStderr, err := os.CreateTemp(t.TempDir(), "proc-stderr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { procStderr.Close() })
	oldStderr := os.Stderr
	os.Stderr = procStderr
	t.Cleanup(func() { os.Stderr = oldStderr })

	failTurnOnServeDaemon(t, workDir, stateDir, runDir)

	if _, err := procStderr.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	logged, err := io.ReadAll(procStderr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), "error: HTTP 403 (access_denied)") {
		t.Fatalf("daemon log = %q, want the failed-turn line to carry the kind and status", logged)
	}
	if strings.Contains(string(logged), "sign-in rejected") {
		t.Fatalf("daemon log = %q, want the provider's error body withheld", logged)
	}
}
