package contextmgr

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"unicode"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/searchresult"
	"primeradiant.com/evener/llm"
)

// Leaving any portion of the test-owned note after a character cut would
// propagate it into the real compaction summary and fail these cases.
func TestSummarizeToolResult_RegistryCharacterCutHasNoNoteFragment(t *testing.T) {
	t.Parallel()
	for _, toolName := range []string{"grep", "glob", "memory_search"} {
		for _, fitting := range []bool{false, true} {
			name := toolName + "/cut"
			if fitting {
				name = toolName + "/fits"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				entrySize := 19_919
				if fitting {
					entrySize--
				}
				raw := searchresult.WithNotes(strings.Repeat("x", entrySize), strings.Repeat("§", 80))
				reg := tool.NewRegistry()
				if err := reg.Register(tool.RegisteredTool{
					Definition: llm.ToolDefinition{Name: toolName, Description: "Return the search fixture"},
					Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
						return raw, nil
					},
				}); err != nil {
					t.Fatal(err)
				}
				got := reg.ExecuteCall(t.Context(), execenv.NewLocalExecutionEnvironment(t.TempDir()), llm.ToolCallData{
					ID: "search-note", Name: toolName, Arguments: json.RawMessage(`{"pattern":"x"}`),
				})
				if got.IsError || got.Truncated == fitting || got.RecoverableOutput != raw {
					t.Fatal("unexpected dispatch or recovery state")
				}
				summary := summarizeToolResult(toolName, got.Output, got.IsError, json.RawMessage(`{"pattern":"x"}`))
				if strings.Contains(summary, "§") != fitting {
					t.Fatalf("note preservation disagrees with fitting state %v: %q", fitting, summary)
				}
				counts := strings.FieldsFunc(summary, func(r rune) bool { return !unicode.IsDigit(r) })
				wantCounts := []string{"1"}
				if !fitting {
					wantCounts = append(wantCounts, "82") // The note and its two-rune separator.
				}
				if !slices.Equal(counts, wantCounts) {
					t.Fatalf("summary lost entry/removal counts: got %v, want %v, summary=%q", counts, wantCounts, summary)
				}
			})
		}
	}
}
