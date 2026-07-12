//go:build serffuzz

package tool

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func FuzzToolPackageUnion(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) {
		toolPackageApplyEdges(t)
		toolPackageRegistryEdges(t)
	})
}

func toolPackageApplyEdges(t *testing.T) {
	t.Helper()
	_, _ = parseV4APatchLines([]string{"*** Begin Patch", "*** Add File: x", "*** End Patch"})
	_ = hintFromHunk([]string{" context"})
	_ = formatLineSnippet([]string{"x"}, 0, 1)
	_ = mismatchLineForMissingSequence([]string{"x"}, "x", 0)
	_ = mismatchLineForMissingSequence(nil, "x", 0)
	for _, tc := range []struct {
		lines, pattern []string
		start          int
	}{
		{nil, nil, 3}, {[]string{"x"}, []string{"x"}, -1}, {[]string{"x"}, []string{"x", "y"}, 0},
		{[]string{"x  "}, []string{"x"}, 0}, {[]string{" x "}, []string{"x"}, 0},
		{[]string{"“x”"}, []string{"\"x\""}, 0}, {[]string{"two  words"}, []string{"two words"}, 0},
	} {
		_ = seekLineSequence(tc.lines, tc.pattern, tc.start)
	}
	_ = fuzzyLineMatch("", "x")
	_ = indexOfLine([]string{"x"}, "", 0)
	_ = indexOfLine([]string{" x "}, "x", 0)
	_ = indexOfLine([]string{"x"}, "missing", 0)
}

func toolPackageRegistryEdges(t *testing.T) {
	t.Helper()
	for _, tc := range []struct{ path, raw string }{
		{"x.bin", "[image: x]"}, {"x.bin", "[image: x]\n%%%"}, {"x.bin", "[image: x]\neA=="},
		{"x.bin", "[document: x]"}, {"x.bin", "[document: x]\n%%%"}, {"x.bin", "[document: x]\neA=="},
	} {
		_ = ParseImageResult(tc.path, tc.raw)
		_ = ParseDocumentResult(tc.path, tc.raw)
	}

	r := NewRegistry()
	if err := r.Register(RegisteredTool{Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "missing_exec"}}}); err == nil {
		t.Fatal("missing executor registered")
	}
	if err := r.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "bad_schema", Parameters: map[string]any{"type": "object", "properties": map[string]any{"x": make(chan int)}}}},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) { return nil, nil },
	}); err == nil {
		t.Fatal("bad schema registered")
	}
	allowed := map[string]bool{}
	r.RestrictKeepingResultTool(allowed, "")
	r.OverrideLimits(nil)

	state := RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "state_bad"}},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			return StateResult{Output: "ok", State: make(chan int)}, nil
		},
	}
	if err := r.Register(state); err != nil {
		t.Fatal(err)
	}
	res := r.ExecuteCall(context.Background(), nil, llm.ToolCallData{Name: "state_bad", Arguments: json.RawMessage("null")})
	if res.IsError || res.Output != "ok" {
		t.Fatalf("state result = %+v", res)
	}
	_ = truncateResult("x", "c", "a\nb", false, schema.ToolOutputLimit{MaxLines: 1})

	_, _ = compileSchema(map[string]any{"type": "object", "properties": map[string]any{"x": nil}})
	_, _ = compileSchema(map[string]any{"type": "object", "required": []string{"missing"}})
	_, _ = compileSchemaWith(map[string]any{"type": "object"}, func(*jsonschema.Compiler, string, io.Reader) error { return errors.New("add resource") }, nil)
	_, _ = compileSchemaWith(map[string]any{"type": "object"}, func(*jsonschema.Compiler, string, io.Reader) error { panic("compile schema") }, nil)
	if !strings.Contains(res.Output, "ok") {
		t.Fatal("missing output")
	}
}
