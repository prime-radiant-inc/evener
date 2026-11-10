package tool

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

// A read_file result cut by the character cap names the lines it dropped as an
// offset/limit window, so the model can fetch exactly what it is missing.
func TestTruncatedReadFileExposesOmittedLineRange(t *testing.T) {
	t.Parallel()
	const total = 5000
	text := execenv.NumberLines(strings.Repeat("some source line of moderate length\n", total), nil, intPtr(total))
	reg := NewRegistry()
	if err := reg.Register(RegisteredTool{
		Definition: DefReadFile(),
		ReadOnly:   true,
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			return text, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	got := reg.ExecuteCall(t.Context(), nil, llm.ToolCallData{ID: "r", Name: "read_file", Arguments: json.RawMessage(`{"file_path":"f"}`)})
	if !got.Truncated || got.OmittedLines == nil {
		t.Fatalf("truncated=%v omitted=%v", got.Truncated, got.OmittedLines)
	}
	from, to := got.OmittedLines.From, got.OmittedLines.To
	if from <= 1 || to >= total || from > to {
		t.Fatalf("omitted range %d-%d is not interior to 1-%d", from, to, total)
	}
	// Every line outside the window must be present whole; the window is
	// exactly what offset/limit must refetch.
	shown := map[int]bool{}
	for _, line := range strings.Split(got.Output, "\n") {
		num, _, ok := strings.Cut(line, "\t")
		if n, err := strconv.Atoi(strings.TrimSpace(num)); ok && err == nil {
			shown[n] = true
		}
	}
	for n := 1; n <= total; n++ {
		inWindow := n >= from && n <= to
		if !inWindow && !shown[n] {
			t.Fatalf("line %d is outside the omitted window %d-%d but missing from output", n, from, to)
		}
	}
}

func intPtr(n int) *int { return &n }
