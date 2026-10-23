package contextmgr

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestDeterministicCheckpointProvenanceRejectsAmbiguousMetadata(t *testing.T) {
	t.Parallel()
	conversation := renderCheckpointConversation([]checkpointConversationEntry{{Role: "user", Text: "EXAMPLE_539"}})
	for _, tc := range []struct{ name, metadata string }{
		{"plain-prose", "SOURCE_447\n"},
		{"indented-prose", "  SOURCE_447\n  EXPLANATION_638\n"},
		{"indented-prose-after-files", "Files modified: file.go\n  SOURCE_447\n"},
		{"shell-record-without-header", "  \"pwd\" → exit 0\n"},
		{"prose-under-shell-header", "Last shell results:\n  SOURCE_447\n"},
		{"prose-after-shell-record", "Last shell results:\n  \"pwd\" → exit 0\n  SOURCE_447\n"},
		{"shell-header-without-records", "Last shell results:\n"},
		{"shell-header-repeated", "Last shell results:\n  \"pwd\" → exit 0\nLast shell results:\n  \"pwd\" → exit 0\n"},
		{"shell-record-after-skills", "Activated skills: inspect\n  \"pwd\" → exit 0\n"},
		{"shell-command-unquoted", "Last shell results:\n  pwd → exit 0\n"},
		{"shell-command-invalid-escape", "Last shell results:\n  \"\\z\" → exit 0\n"},
		{"shell-command-wrong-indent", "Last shell results:\n    \"pwd\" → exit 0\n"},
		{"shell-exit-prose", "Last shell results:\n  \"pwd\" → exit SOURCE_447\n"},
		{"shell-exit-suffix", "Last shell results:\n  \"pwd\" → exit 0 SOURCE_447\n"},
		{"shell-four-records", "Last shell results:\n" + strings.Repeat("  \"pwd\" → exit 0\n", 4)},
		{"files-repeated", "Files modified: a.go\nFiles modified: b.go\n"},
		{"files-after-actions", "Actions taken: 1 tool calls (1 shell)\nFiles modified: a.go\n"},
		{"skills-before-actions", "Activated skills: inspect\nActions taken: 1 tool calls (1 shell)\n"},
		{"files-empty", "Files modified: \n"},
		{"actions-prose", "Actions taken: SOURCE_447\n"},
		{"actions-total-mismatch", "Actions taken: 2 tool calls (1 shell)\n"},
		{"actions-trailing-prose", "Actions taken: 1 tool calls (1 shell) SOURCE_447\n"},
		{"pointer-prose", "Earlier history was compacted into this checkpoint SOURCE_447\n"},
		{"metadata-blank-line", "Files modified: a.go\n\nActivated skills: inspect\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := "[CONTEXT CHECKPOINT]\n" + tc.metadata + "\n" + conversation + "[END CHECKPOINT]\n"
			if isRenderedDeterministicCheckpoint(text) {
				t.Error("ambiguous metadata accepted as renderer-owned")
			}
			for _, kind := range []schema.TurnKind{schema.TurnCheckpoint, schema.TurnUserInput} {
				source := []schema.Turn{schema.NewTurn(kind, llm.User(text))}
				data := collectStrategyCheckpointData(source, len(source), "communicate")
				if len(data.conversation) != 0 || !reflect.DeepEqual(data.earlierSummaries, []string{text}) {
					t.Errorf("kind %v: ambiguous source was mined or lost: conversation=%#v, earlier=%#v", kind, data.conversation, data.earlierSummaries)
				}
			}
		})
	}
}

func TestDeterministicCheckpointProvenanceRejectsNoncanonicalSections(t *testing.T) {
	t.Parallel()
	conversation := renderCheckpointConversation([]checkpointConversationEntry{{Role: "user", Text: "EXAMPLE_539"}})
	earlier := renderCheckpointEarlierSummaries([]string{"SOURCE_447"})
	notes := renderCheckpointWorkingNotes([]string{"NOTE_638"})
	for name, body := range map[string]string{
		"earlier-after-conversation": "\n" + conversation + earlier,
		"notes-before-conversation":  "\n" + notes + "\n" + conversation,
		"duplicate-conversation":     "\n" + conversation + "\n" + conversation,
		"prose-between-sections":     earlier + "  SOURCE_447\n\n" + conversation,
		"unfenced-conversation":      "\n## Conversation\n\n### User\n\nEXAMPLE_539\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			text := "[CONTEXT CHECKPOINT]\n" + body + "[END CHECKPOINT]\n"
			data := collectStrategyCheckpointData([]schema.Turn{schema.NewTurn(schema.TurnCheckpoint, llm.User(text))}, 1, "communicate")
			if len(data.conversation) != 0 || !reflect.DeepEqual(data.earlierSummaries, []string{text}) {
				t.Fatalf("noncanonical sections were mined or lost: %#v", data)
			}
		})
	}
}

func TestDeterministicCheckpointProvenanceReadsRendererMetadata(t *testing.T) {
	t.Parallel()
	conversation := []checkpointConversationEntry{{Role: "user", Text: "PARTNER_705"}, {Role: "agent", Text: "AGENT_816"}}
	for _, tools := range [][]string{nil, {"read_transcript"}, {"find_session_transcripts"}, {"read_transcript", "find_session_transcripts"}} {
		meta := &CompactionMeta{SessionID: "SESSION_927", AvailableTranscriptTools: tools, ActivatedSkills: []string{"meta-skill"}}
		for _, fields := range []string{"none", "files", "actions", "shell", "skills", "all"} {
			t.Run(strings.Join(tools, "+")+"/"+fields, func(t *testing.T) {
				data := checkpointData{conversation: conversation, earlierSummaries: []string{"EARLIER_038"}, workingNotes: []string{"NOTE_149"}}
				if fields == "files" || fields == "all" {
					data.fileWrites = map[string]checkpointWriteStatus{"a file.go": writeConfirmed, "b.go": writeFailed, "c.go": writeUnconfirmed}
				}
				if fields == "actions" || fields == "all" {
					data.toolCounts = map[string]int{"read_file": 3, "shell": 2}
				}
				if fields == "shell" || fields == "all" {
					// Collect real shell records, including quoted delimiters, control
					// characters, Unicode and a missing result (unknown exit).
					history := []schema.Turn{
						schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "shell1", Name: "shell", Arguments: []byte(`{"command":"printf \"x\"\\n\tλ → exit 0"}`)}},
						}}),
					}
					data.lastShellResults = collectCheckpointData(history, len(history), "communicate").lastShellResults
					if len(data.lastShellResults) != 1 {
						t.Fatal("shell fixture did not produce a real collected record")
					}
					data.lastShellResults = append(data.lastShellResults, `  "pwd" → exit 0`, `  "false" → exit 17`)
				}
				if fields == "skills" || fields == "all" {
					data.activatedSkills = map[string]bool{"history-skill": true}
				}
				text := formatCheckpoint(data, meta, maxCheckpointChars)
				if !isRenderedDeterministicCheckpoint(text) {
					t.Fatal("real renderer metadata rejected")
				}
				got := collectStrategyCheckpointData([]schema.Turn{schema.NewTurn(schema.TurnCheckpoint, llm.User(text))}, 1, "communicate")
				if !reflect.DeepEqual(got.conversation, conversation) || !reflect.DeepEqual(got.earlierSummaries, data.earlierSummaries) || !reflect.DeepEqual(got.workingNotes, data.workingNotes) {
					t.Fatalf("renderer sections not recovered: %#v", got)
				}
			})
		}
	}
	for _, data := range []checkpointData{
		{earlierSummaries: []string{"EARLIER_038"}},
		{conversation: conversation},
		{workingNotes: []string{"NOTE_149"}},
		{conversation: conversation, lastShellResults: []string{`  "pwd" → exit 0`}},
		{workingNotes: []string{"NOTE_149"}, lastShellResults: []string{`  "pwd" → exit 000`}},
		{earlierSummaries: []string{"EARLIER_038"}, lastShellResults: []string{`  "pwd" → exit ?`}},
	} {
		if text := formatCheckpoint(data, nil, maxCheckpointChars); !isRenderedDeterministicCheckpoint(text) {
			t.Errorf("real standalone section rejected: %q", text)
		}
	}
	// The opt-in fallback keeps earlier context before the ordinary renderer's
	// metadata, without applying the default earlier-summary cap.
	history := []schema.Turn{
		schema.NewTurn(schema.TurnSummary, llm.User(strings.TrimSpace(strings.Repeat("EARLIER_038 ", 1000)))),
		schema.NewTurn(schema.TurnUserInput, llm.User("PARTNER_705")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("TAIL_260")),
	}
	folded, _ := strategyCheckpointFold(history, 1, nil, "communicate")
	if !isRenderedDeterministicCheckpoint(folded[0].Message.Text()) {
		t.Fatal("real opt-in deterministic fallback rejected")
	}
}

func TestStrategyIndentedCheckpointRetainedWhole(t *testing.T) {
	t.Parallel()
	const source = "[CONTEXT CHECKPOINT]\n  SOURCE_HOLD_447\n  EXPLANATION_638\n## Conversation\n\n### User\n\n```text\nEXAMPLE_539\n```\n\n[END CHECKPOINT]"
	for _, name := range []string{"predictive", "session-log"} {
		t.Run(name, func(t *testing.T) {
			cm := authorityRequestManager(func(llm.Request) (llm.Response, error) {
				return llm.Response{Message: llm.Assistant("MODEL_821")}, nil
			})
			history := []schema.Turn{
				schema.NewTurn(schema.TurnCheckpoint, llm.User(source)),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("WORK_932")),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("TAIL_043")),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("TAIL_154")),
			}
			for fold := range 2 {
				var err error
				if name == "predictive" {
					history, err = NewCheckpointPredStrategy(cm).predictiveCheckpoint(context.Background(), history, 2)
				} else {
					sls := &SessionLogStrategy{cm: cm, log: mustNewSessionLog(t, filepath.Join(t.TempDir(), "log.jsonl"))}
					history = sls.sessionLogCheckpoint(history, 2)
				}
				if err != nil {
					t.Fatal(err)
				}
				data := collectCheckpointData(history, 1, "communicate")
				if len(data.conversation) != 0 || !strings.Contains(strings.Join(data.earlierSummaries, "\n"), source) {
					t.Fatalf("fold %d: opaque source was mined or lost: %#v", fold, data)
				}
				history = append(history, schema.NewTurn(schema.TurnAssistant, llm.Assistant("NEXT_265")), schema.NewTurn(schema.TurnAssistant, llm.Assistant("NEXT_376")))
			}
		})
	}
}
