package contextmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// A real registry refusal must not become a match/file count when summarized.
// The no-snippet variant keeps only that producer output's first paragraph.
func TestSummarizeToolResult_ParkedSearch(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"grep", "glob", "memory_search"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			parked := parkedSearchResult(t, name)
			for _, withSnippets := range []bool{false, true} {
				t.Run(fmt.Sprintf("snippets_%t", withSnippets), func(t *testing.T) {
					output := parked.Output
					if !withSnippets {
						output, _, _ = strings.Cut(output, "\n\n")
					}
					summary := summarizeToolResult(name, output, parked.IsError, json.RawMessage(`{"pattern":"p"}`))
					checkSearchSummaryGolden(t, fmt.Sprintf("%s_snippets_%t", name, withSnippets), summary+"\n")
				})
			}
		})
	}
}

// Neither marker-like error text nor successful data, even an exact park body,
// may be reclassified as a refusal. In particular, filenames can start with
// the exact marker at byte zero; the error flag must distinguish that data.
func TestSummarizeToolResult_ParkMarkerPreservesSearchData(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"grep", "glob", "memory_search"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			parked := parkedSearchResult(t, name)
			var summaries strings.Builder
			for _, tc := range []struct {
				name, output string
				isError      bool
			}{
				{"success", "a.go\nb.go", false},
				{"success_empty", "", false},
				{"success_marker_filename", "evener did not execute this call: result.go:1:SENTINEL", false},
				{"success_full_park_body", parked.Output, false},
				{"error_similar_words", "evener did not execute this callback: SENTINEL", true},
				{"error_missing_delimiter", "evener did not execute this call:\nSENTINEL", true},
				{"error_embedded_marker", "a.go:1:evener did not execute this call: SENTINEL", true},
				{"error_later_marker", "a.go:1:SENTINEL\nevener did not execute this call: SENTINEL", true},
				{"error_leading_space", " evener did not execute this call: SENTINEL", true},
				{"error_marker_in_note", "a.go\n\nNOTE_SENTINEL evener did not execute this call: SENTINEL", true},
				{"error_wrong_case", "Evener did not execute this call: SENTINEL", true},
			} {
				fmt.Fprintf(&summaries, "%s\n%s\n", tc.name,
					summarizeToolResult(name, tc.output, tc.isError, json.RawMessage(`{"pattern":"p"}`)))
			}
			checkSearchSummaryGolden(t, name+"_search_data", summaries.String())
		})
	}
}

// Parked registry results already bypass masking as errors. The helper fix
// must not change that admission rule or rewrite their original evidence.
func TestMaskObservations_PreservesParkedSearch(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"grep", "glob", "memory_search"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			parked := parkedSearchResult(t, name)
			history := []schema.Turn{
				{Kind: schema.TurnAssistant, Message: assistantWithToolCall("parked", name, `{"pattern":"p"}`)},
				{Kind: schema.TurnTool, Message: llm.ToolResultNamed("parked", name, parked.Output, parked.IsError)},
				{Kind: schema.TurnUserInput, Message: llm.User("RECENT_SENTINEL")},
			}
			before := *history[1].Message.Content[0].ToolResult
			maskObservations(history, 1, "communicate")
			if got := history[1].Message.Content[0].ToolResult; !reflect.DeepEqual(*got, before) {
				t.Fatalf("parked result changed during masking: got %#v, want %#v", *got, before)
			}
		})
	}
}

func parkedSearchResult(t *testing.T, name string) tool.ExecResult {
	t.Helper()
	reg := tool.NewRegistry()
	calls := 0
	if err := reg.Register(tool.RegisteredTool{
		Definition: llm.ToolDefinition{Name: name, Description: "parked search summary test"},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			calls++
			return nil, errors.New("FAILURE_SENTINEL")
		},
	}); err != nil {
		t.Fatal(err)
	}
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	call := llm.ToolCallData{ID: "parked", Name: name, Arguments: json.RawMessage(`{"pattern":"p"}`)}
	for range 2 {
		if result := reg.ExecuteCall(context.Background(), env, call); !result.IsError {
			t.Fatalf("expected a failed dispatch, got %#v", result)
		}
	}
	parked := reg.ExecuteCall(context.Background(), env, call)
	if calls != 2 || !parked.IsError {
		t.Fatalf("third call was not parked: executor calls=%d, result=%#v", calls, parked)
	}
	return parked
}

func checkSearchSummaryGolden(t *testing.T, name, summary string) {
	t.Helper()
	agenttest.CheckGolden(t, filepath.Join("testdata", "searchsummary", name+".txt"), []byte(summary), *updateSummaryPromptGoldens,
		"Regenerate with `go test ./agent/internal/contextmgr -run 'TestSummarizeToolResult_(ParkedSearch|ParkMarkerPreservesSearchData)$' -count=1 -update-prompt` and read the diff.")
}
