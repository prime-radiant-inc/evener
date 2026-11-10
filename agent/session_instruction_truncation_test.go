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

// An instruction doc over its budget loses its tail from the prompt. Before
// #1048 the session set projectDocsTruncated and never told anyone, so the
// user could not learn why the model stopped following the end of the file.
func TestOversizedInstructionsEmitAWarningAfterSessionStart(t *testing.T) {
	line := "standing instruction\n"
	body := strings.Repeat(line, projectDocByteBudget/len(line)+1)
	cases := []struct {
		name    string
		workDir string
		cfg     SessionConfig
	}{
		{
			name: "personal",
			cfg: SessionConfig{
				AgentsDocPath: filepath.Join(t.TempDir(), "AGENTS.md"),
				testOnly:      testConfig{skipGitSnapshot: true, noSyncJobStore: true},
			},
		},
		{
			name: "project",
			cfg: SessionConfig{
				AgentsDocPath: filepath.Join(t.TempDir(), "absent-AGENTS.md"),
				testOnly:      testConfig{skipGitSnapshot: true, noSyncJobStore: true},
			},
		},
	}
	personalDoc := cases[0].cfg.AgentsDocPath
	if err := os.WriteFile(personalDoc, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cases[1].workDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(cases[1].workDir, "AGENTS.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := llm.NewClient()
			client.Register(&fakeAdapter{name: "openai"})
			workDir := tc.workDir
			if workDir == "" {
				workDir = t.TempDir()
			}
			sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"),
				execenv.NewLocalExecutionEnvironment(workDir), tc.cfg)
			if err != nil {
				t.Fatalf("NewSession: %v", err)
			}
			assertInstructionTruncationWarning(t, sess)
		})
	}
}

func assertInstructionTruncationWarning(t *testing.T, sess *Session) {
	t.Helper()
	sawStart := false
	for _, ev := range transcriptWarningOrderingDrainAll(t, sess) {
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
		if warning.Source != "evener" {
			t.Fatalf("instruction truncation warning source = %q, want evener", warning.Source)
		}
		return
	}
	t.Fatal("oversized instruction docs produced no warning")
}
