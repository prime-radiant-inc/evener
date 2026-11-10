package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/searchresult"
	"primeradiant.com/evener/llm"
)

// grepThroughRegistry runs the real grep producer behind a registry whose
// grep tool carries the default output limit, over a single file of lines.
func grepThroughRegistry(t *testing.T, lines []string, args string) ExecResult {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "matches.txt"), []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	env := execenv.NewLocalExecutionEnvironment(dir)
	reg := NewRegistry()
	if err := reg.Register(RegisteredTool{
		Definition: DefGrep(),
		ReadOnly:   true,
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			max := execenv.DefaultGrepMaxResults
			if v, ok := args["max_results"].(float64); ok {
				max = int(v)
			}
			// A skip predicate selects the deterministic native reader.
			return env.(*execenv.LocalExecutionEnvironment).GrepSkipping(ctx, "x", "matches.txt", "", false, max, "content", 0, func(string) bool { return false })
		},
		Limit: defaultToolLimit("grep"),
	}); err != nil {
		t.Fatal(err)
	}
	return reg.ExecuteCall(t.Context(), env, llm.ToolCallData{ID: "g", Name: "grep", Arguments: json.RawMessage(args)})
}

func repeatLines(n int, line string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = line
	}
	return out
}

func TestGrepRaisedMaxResultsIsNotRecappedByRegistry(t *testing.T) {
	t.Parallel()
	got := grepThroughRegistry(t, repeatLines(400, "x"), `{"pattern":"x","max_results":400}`)
	if got.IsError || got.Truncated {
		t.Fatalf("raised max_results was recapped: error=%v truncated=%v", got.IsError, got.Truncated)
	}
	if n := searchresult.EntryCount(got.Output); n != 400 {
		t.Fatalf("entries = %d, want 400", n)
	}
}

func TestGrepRegistryTruncationReasonDistinguishesLineCapFromCharCap(t *testing.T) {
	t.Parallel()
	byChars := grepThroughRegistry(t, repeatLines(400, "x"+strings.Repeat("y", 100)), `{"pattern":"x","max_results":400}`)
	if !byChars.Truncated || byChars.TruncatedBy != TruncatedByChars {
		t.Fatalf("char-cap truncation: truncated=%v by=%q", byChars.Truncated, byChars.TruncatedBy)
	}

	reg := NewRegistry()
	if err := reg.Register(RegisteredTool{
		Definition: DefGrep(),
		ReadOnly:   true,
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			return strings.Join(repeatLines(300, "x"), "\n"), nil
		},
		Limit: defaultToolLimit("grep"),
	}); err != nil {
		t.Fatal(err)
	}
	lineCapped := reg.ExecuteCall(t.Context(), nil, llm.ToolCallData{ID: "g", Name: "grep", Arguments: json.RawMessage(`{"pattern":"x"}`)})
	if !lineCapped.Truncated || lineCapped.TruncatedBy != TruncatedByLines {
		t.Fatalf("line-cap truncation: truncated=%v by=%q", lineCapped.Truncated, lineCapped.TruncatedBy)
	}
	if lineCapped.TruncatedBy == byChars.TruncatedBy {
		t.Fatal("line cap and char cap are indistinguishable")
	}
}
