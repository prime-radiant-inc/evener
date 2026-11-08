package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

// A personal AGENTS.md over its budget loses its tail from the prompt. Before
// #1048 the session set projectDocsTruncated and never told anyone, so the
// user could not learn why the model stopped following the end of the file.
func TestOversizedPersonalInstructionsEmitAWarningAfterSessionStart(t *testing.T) {
	doc := filepath.Join(t.TempDir(), "AGENTS.md")
	line := "standing instruction\n"
	body := strings.Repeat(line, projectDocByteBudget/len(line)+1)
	if err := os.WriteFile(doc, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		SessionConfig{
			AgentsDocPath: doc,
			testOnly:      testConfig{skipGitSnapshot: true, noSyncJobStore: true},
		})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.Close()

	sawStart := false
	for ev := range sess.Events() {
		if ev.Kind == events.EventSessionStart {
			sawStart = true
			continue
		}
		warning, ok := ev.Data.(events.WarningData)
		if !ok || warning.Code != "instruction_docs_truncated" {
			continue
		}
		if !sawStart {
			t.Fatal("the instruction truncation warning arrived before SESSION_START")
		}
		if warning.Message == "" {
			t.Fatal("the instruction truncation warning has no message")
		}
		return
	}
	t.Fatal("oversized instruction docs produced no warning")
}
