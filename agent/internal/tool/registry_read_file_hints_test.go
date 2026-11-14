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

// A read_file or memory_read result cut by the character cap names the lines it
// dropped as an offset/limit window, so the model can fetch what it is missing.
func TestTruncatedReadFileExposesOmittedLineRange(t *testing.T) {
	t.Parallel()
	const total = 5000
	text := execenv.NumberLines(strings.Repeat("some source line of moderate length\n", total), nil, new(total))
	for _, tc := range []struct {
		def  llm.ToolDefinition
		args string
	}{
		{def: DefReadFile(), args: `{"file_path":"f"}`},
		{def: DefMemoryRead(), args: `{"scope":"personal","file_path":"f.md"}`},
	} {
		t.Run(tc.def.Name, func(t *testing.T) {
			t.Parallel()
			reg := NewRegistry()
			if err := reg.Register(RegisteredTool{
				Definition: tc.def,
				ReadOnly:   true,
				Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
					return text, nil
				},
			}); err != nil {
				t.Fatal(err)
			}
			got := reg.ExecuteCall(t.Context(), nil, llm.ToolCallData{ID: "r", Name: tc.def.Name, Arguments: json.RawMessage(tc.args)})
			if !got.Truncated || got.OmittedLines == nil {
				t.Fatalf("truncated=%v omitted=%v", got.Truncated, got.OmittedLines)
			}
			from, to := got.OmittedLines.From, got.OmittedLines.To
			if from <= 1 || to >= total || from > to {
				t.Fatalf("omitted range %d-%d is not interior to 1-%d", from, to, total)
			}
			// The rendered output must tell the model what it cut: the window's
			// own numbers, from the structured field — never the hint's wording.
			if !strings.Contains(got.Output, strconv.Itoa(from)+"-"+strconv.Itoa(to)) {
				t.Fatalf("truncated output must name the omitted window %d-%d: %.300q", from, to, got.Output)
			}
			// Every line outside the window must be present whole; the window is
			// exactly what offset/limit must refetch.
			shown := map[int]bool{}
			for line := range strings.SplitSeq(got.Output, "\n") {
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
		})
	}
}
