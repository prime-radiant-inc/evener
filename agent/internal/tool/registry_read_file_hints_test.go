package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// A read_file or memory_read result cut by the character cap names the lines it
// dropped as an offset/limit window, so the model can fetch what it is missing.
func TestTruncatedReadFileExposesOmittedLineRange(t *testing.T) {
	t.Parallel()
	const total = 5000
	buildNumbered := func(sep string) string {
		var sb strings.Builder
		for i := 1; i <= total; i++ {
			fmt.Fprintf(&sb, "%d%ssome source line of moderate length\n", i, sep)
		}
		return sb.String()
	}
	for _, tc := range []struct {
		name string
		def  llm.ToolDefinition
		args string
		sep  string
	}{
		{name: "read_file_tab", def: DefReadFile(), args: `{"file_path":"f"}`, sep: "\t"},
		{name: "read_file_colon", def: DefReadFile(), args: `{"file_path":"f"}`, sep: ":"},
		{name: "memory_read_tab", def: DefMemoryRead(), args: `{"scope":"personal","file_path":"f.md"}`, sep: "\t"},
		{name: "memory_read_colon", def: DefMemoryRead(), args: `{"scope":"personal","file_path":"f.md"}`, sep: ":"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			text := buildNumbered(tc.sep)
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
			reg.OverrideLimits(map[string]schema.ToolOutputLimit{
				tc.def.Name: {MaxChars: 50_000, Strategy: schema.TruncHeadTail},
			})
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
				num, _, ok := strings.Cut(line, tc.sep)
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

func TestReadFileDefaultLimitUsesTruncHead(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	body := "1\tfirst line sentinel\n" + strings.Repeat("2\tmiddle padding line\n", 3000) + "3002\ttail line sentinel\n"
	if err := reg.Register(RegisteredTool{
		Definition: DefReadFile(),
		ReadOnly:   true,
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			return body, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	got := reg.ExecuteCall(t.Context(), nil, llm.ToolCallData{ID: "r", Name: "read_file", Arguments: json.RawMessage(`{"file_path":"f"}`)})
	if !got.Truncated || got.TruncatedBy != TruncatedByChars {
		t.Fatalf("truncated=%v by=%q, want true/%q", got.Truncated, got.TruncatedBy, TruncatedByChars)
	}
	if !strings.Contains(got.Output, "first line sentinel") {
		t.Fatalf("TruncHead must preserve line 1: %.200q", got.Output)
	}
	if strings.Contains(got.Output, "tail line sentinel") {
		t.Fatalf("TruncHead must drop the tail, not keep it")
	}
}

func TestTextResultPassesThroughReadMetadataAndTruncation(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	if err := reg.Register(RegisteredTool{
		Definition: DefReadFile(),
		ReadOnly:   true,
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			return TextResult{
				Output:        "1\ta\n",
				Truncated:     true,
				TruncatedBy:   TruncatedByChars,
				ReadStartLine: 1,
				ReadEndLine:   40,
			}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	got := reg.ExecuteCall(t.Context(), nil, llm.ToolCallData{ID: "r", Name: "read_file", Arguments: json.RawMessage(`{"file_path":"f"}`)})
	if got.Output != "1\ta\n" {
		t.Fatalf("Output = %q, want %q unchanged", got.Output, "1\ta\n")
	}
	if !got.Truncated || got.TruncatedBy != TruncatedByChars || got.ReadStartLine != 1 || got.ReadEndLine != 40 {
		t.Fatalf("ExecResult metadata = truncated=%v by=%q start=%d end=%d", got.Truncated, got.TruncatedBy, got.ReadStartLine, got.ReadEndLine)
	}
}

func TestReadFileLongPathDoesNotTripRegistryTruncHead(t *testing.T) {
	t.Parallel()
	baseDir := t.TempDir()
	seg := strings.Repeat("d", 100)
	var relParts []string
	for len(filepath.Join(relParts...)) < 2450 {
		relParts = append(relParts, seg)
	}
	relPath := filepath.Join(append(relParts, "target.txt")...)
	fullPath := filepath.Join(baseDir, relPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(strings.Repeat("x", 60000)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := execenv.NewLocalExecutionEnvironment(baseDir)
	t.Cleanup(env.Cleanup)

	reg := NewRegistry()
	if err := reg.Register(RegisteredTool{
		Definition: DefReadFile(),
		ReadOnly:   true,
		Exec: func(ctx context.Context, e execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			off, lim := 1, 1
			out, meta, err := e.(execenv.RichFileReader).ReadFileRichCtx(ctx, args["file_path"].(string), &off, &lim)
			if err != nil {
				return nil, err
			}
			return TextResult{
				Output:        out,
				Truncated:     meta.Truncated,
				TruncatedBy:   TruncationReason(meta.TruncatedBy),
				ReadStartLine: meta.StartLine,
				ReadEndLine:   meta.EndLine,
			}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	rawArgs, _ := json.Marshal(map[string]any{"file_path": relPath, "offset": 1, "limit": 1})
	got := reg.ExecuteCall(t.Context(), env, llm.ToolCallData{ID: "r", Name: "read_file", Arguments: rawArgs})
	if got.IsError {
		t.Fatalf("unexpected error: %s", got.Output)
	}
	if utf8.RuneCountInString(got.Output) > 50_000 {
		t.Fatalf("output runes = %d, want <= 50000", utf8.RuneCountInString(got.Output))
	}
	if !strings.Contains(got.Output, relPath) || !strings.Contains(got.Output, "60000") {
		t.Fatalf("registry TruncHead chopped off execenv footer")
	}
	if got.Output != got.RecoverableOutput {
		t.Fatalf("registry TruncHead modified execenv output (output len=%d recoverable len=%d)", len(got.Output), len(got.RecoverableOutput))
	}
}
