package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/llm"
)

// The root's answer in `evener run` is its latest accepted report. A drain
// turn driven by a finished delegate either reports again, and the new report
// wins, or ends without a report, and the earlier report stands and the run
// still succeeds.
func TestRunAnswerIsTheRootsLatestReport(t *testing.T) {
	tests := []struct {
		name          string
		afterDelegate llm.Response
		want          string
	}{
		{"drain turn reports again", scriptedCommunicate("REPORT_B"), "REPORT_B"},
		{"drain turn ends in bare text", llm.Response{Message: llm.Assistant("housekeeping")}, "REPORT_A"},
		{"drain turn gets empty responses", llm.Response{Message: llm.Assistant("")}, "REPORT_A"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			childArtifact := filepath.Join(tmp, "child-artifact.txt")
			const rootPrompt = "ROOT-REPORT-PROMPT"
			// The delegate waits at a gate that opens when the drain starts,
			// so the root has reported A before any completion can reach it.
			gate := filepath.Join(tmp, "drain-started")
			releaseOnDrainStart(t, func() {
				if err := os.WriteFile(gate, []byte("go\n"), 0o600); err != nil {
					t.Errorf("open gate: %v", err)
				}
			}, nil)
			gateWait := ": GATE-WAIT; while [ ! -f " + shellquote.Literal(gate) + " ]; do sleep 0.02; done"
			notified := false
			step := func(req llm.Request) llm.Response {
				text := requestFullText(req)
				if strings.Contains(text, rootPrompt) {
					switch {
					case strings.Contains(requestDeliveredText(req), "<delegate-notification"):
						notified = true
						return tt.afterDelegate
					case strings.Contains(text, "CHILD-TASK"):
						return scriptedCommunicate("REPORT_A")
					default:
						return scriptedDelegateCall("del_1", "CHILD-TASK write the artifact")
					}
				}
				if !strings.Contains(text, "GATE-WAIT") {
					return scriptedToolCalls(scriptedForegroundShellCall("gate_1", gateWait))
				}
				if _, err := os.Stat(childArtifact); err != nil {
					return scriptedToolCalls(scriptedWriteFileCall("cw_1", childArtifact, "artifact"))
				}
				return scriptedCommunicate("child wrote artifact")
			}
			steps := make([]func(llm.Request) llm.Response, 24)
			for i := range steps {
				steps[i] = step
			}
			installRunScriptedProvider(t, &scriptedProvider{name: "openai", steps: steps})

			var stdout, stderr bytes.Buffer
			err := run(context.Background(), runConfig{
				prompt:  rootPrompt + ": delegate, report, then handle its completion.",
				model:   "openai/gpt-test",
				workDir: tmp,
				stdout:  &stdout,
				stderr:  &stderr,
			})
			if err != nil {
				t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
			}
			if !notified {
				t.Fatalf("the delegate's completion never reached the root as a drain turn\nstderr: %s", stderr.String())
			}
			if got := strings.TrimSpace(stdout.String()); got != tt.want {
				t.Fatalf("stdout = %q, want %q", got, tt.want)
			}
		})
	}
}
