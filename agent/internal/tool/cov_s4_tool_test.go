package tool

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// TestApplyPatch_MismatchReportsNearbyMatchesAndPotentialLocations drives the
// full diagnostic path: seekLineSequence fails, so updateFileOp.apply builds a
// patchMismatchDiagnostic exercising formatPatchMismatchError, formatLineSnippet,
// candidateLineIndexes (Nearby matches), candidateSequenceIndexes +
// looseCandidateSequenceIndexes (Potential locations), nearestIndexes,
// mismatchLineForMissingSequence, lineAt and formatExpectedLines.
//
// The file has "model := ..." twice (so the wanted line has a nearby match) and
// the second block's first two old/context lines partially match near line 7 (so
// the loose sequence search reports a potential location), while the third
// old/context line ("MISSING_LINE()") appears nowhere, forcing the failure.
func TestApplyPatch_MismatchReportsNearbyMatchesAndPotentialLocations(t *testing.T) {
	dir := t.TempDir()
	content := strings.Join([]string{
		"func first() {",
		"\tmodel := \"openai/gpt-5\"",
		"\treturn model",
		"}",
		"",
		"func second() {",
		"\tmodel := \"openai/gpt-5\"",
		"\tconfig := loadConfig()",
		"\treturn config",
		"}",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	patch := "*** Begin Patch\n*** Update File: f.go\n@@\n \tmodel := \"openai/gpt-5\"\n \tconfig := loadConfig()\n \tMISSING_LINE()\n+\tadded()\n*** End Patch\n"
	_, err := ApplyPatch(dir, patch)
	if err == nil {
		t.Fatal("expected patch mismatch")
	}
	msg := err.Error()
	for _, want := range []string{
		"apply_patch: expected lines not found in f.go at line 2",
		"wanted:",
		"got:",
		"File context around line 2:",
		"Nearby matches for wanted line:",
		"Expected old/context lines from patch:",
		"  \tmodel := \"openai/gpt-5\"",
		"  \tMISSING_LINE()",
		"Potential locations for old/context block:",
		"candidate at line 7:",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error missing %q:\n%s", want, msg)
		}
	}
}

func TestNearestIndexes(t *testing.T) {
	cases := []struct {
		name       string
		indexes    []int
		failedLine int
		limit      int
		want       []int
	}{
		{name: "passthrough len<=limit", indexes: []int{3, 1, 2}, failedLine: 0, limit: 5, want: []int{3, 1, 2}},
		{name: "prefix when no failed line", indexes: []int{10, 20, 30, 40}, failedLine: 0, limit: 2, want: []int{10, 20}},
		{name: "distance sort around failed line", indexes: []int{0, 5, 10}, failedLine: 7, limit: 2, want: []int{5, 10}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nearestIndexes(tc.indexes, tc.failedLine, tc.limit)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestSeekLineSequence(t *testing.T) {
	cases := []struct {
		name    string
		lines   []string
		pattern []string
		start   int
		want    int
	}{
		{name: "empty pattern returns start", lines: []string{"a", "b"}, pattern: nil, start: 1, want: 1},
		{name: "pattern longer than lines", lines: []string{"a"}, pattern: []string{"a", "b"}, start: 0, want: -1},
		{name: "start past end", lines: []string{"a", "b", "c"}, pattern: []string{"a"}, start: 5, want: -1},
		{name: "exact match", lines: []string{"alpha", "beta", "gamma"}, pattern: []string{"beta"}, start: 0, want: 1},
		{name: "trim-right match", lines: []string{"beta  ", "x"}, pattern: []string{"beta"}, start: 0, want: 0},
		{name: "trim-both match", lines: []string{"  beta"}, pattern: []string{"beta"}, start: 0, want: 0},
		{name: "unicode-normalized match", lines: []string{"say “hi”"}, pattern: []string{"say \"hi\""}, start: 0, want: 0},
		{name: "fuzzy internal whitespace match", lines: []string{"const  value = 1"}, pattern: []string{"const value = 1"}, start: 0, want: 0},
		{name: "no match", lines: []string{"a", "b"}, pattern: []string{"zzz"}, start: 0, want: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := seekLineSequence(tc.lines, tc.pattern, tc.start); got != tc.want {
				t.Fatalf("seekLineSequence = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestIndexOfLine(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  string
		start int
		exp   int
	}{
		{name: "exact", lines: []string{"a", "b", "c"}, want: "b", start: 0, exp: 1},
		{name: "whitespace normalized", lines: []string{"a", "  b  c "}, want: "b c", start: 0, exp: 1},
		{name: "unicode normalized", lines: []string{"say “hi”"}, want: "say \"hi\"", start: 0, exp: 0},
		{name: "not found", lines: []string{"a"}, want: "zzz", start: 0, exp: -1},
		{name: "blank want returns not found", lines: []string{"a", "b"}, want: "   ", start: 0, exp: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := indexOfLine(tc.lines, tc.want, tc.start); got != tc.exp {
				t.Fatalf("indexOfLine = %d, want %d", got, tc.exp)
			}
		})
	}
}

func TestSafeJoin(t *testing.T) {
	root := t.TempDir()

	// Success: relative path under root.
	got, err := safeJoin(root, "a/b.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != filepath.Join(root, "a", "b.txt") {
		t.Fatalf("safeJoin = %q, want %q", got, filepath.Join(root, "a", "b.txt"))
	}

	// Success: absolute path under root is stripped to a relative join.
	abs := filepath.Join(root, "sub", "c.txt")
	got, err = safeJoin(root, abs)
	if err != nil {
		t.Fatalf("unexpected error for abs-under-root: %v", err)
	}
	if got != abs {
		t.Fatalf("safeJoin abs = %q, want %q", got, abs)
	}

	errCases := []struct {
		name    string
		rel     string
		wantSub string
	}{
		{name: "empty path", rel: "   ", wantSub: "empty path"},
		{name: "absolute outside root", rel: "/etc/shadow", wantSub: "absolute path outside working directory"},
		{name: "path is rootDir itself", rel: root, wantSub: "path is rootDir itself"},
		{name: "parent traversal", rel: "../escape.txt", wantSub: "path traversal not allowed"},
	}
	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := safeJoin(root, tc.rel)
			if err == nil {
				t.Fatalf("expected error for %q", tc.rel)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q missing %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestParseV4APatchErrorBranches(t *testing.T) {
	cases := []struct {
		name    string
		patch   string
		wantSub string
	}{
		{
			name:    "missing begin patch",
			patch:   "not a patch\n",
			wantSub: "expected '*** Begin Patch'",
		},
		{
			name:    "missing end patch",
			patch:   "*** Begin Patch\n*** Add File: x.txt\n+hi",
			wantSub: "missing '*** End Patch'",
		},
		{
			name:    "add file expecting plus line",
			patch:   "*** Begin Patch\n*** Add File: x.txt\nnot a plus\n*** End Patch\n",
			wantSub: "expected '+' line",
		},
		{
			name:    "unexpected line",
			patch:   "*** Begin Patch\nrandom junk\n*** End Patch\n",
			wantSub: "unexpected line",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseV4APatch(tc.patch)
			if err == nil {
				t.Fatalf("expected error for patch %q", tc.patch)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q missing %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestWithoutPurposeParameter_RemovesFromRequired(t *testing.T) {
	// []any required branch.
	tdAny := llm.ToolDefinition{
		Name: "t",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"purpose": map[string]any{"type": "string"},
				"foo":     map[string]any{"type": "string"},
			},
			"required": []any{"purpose", "foo"},
		},
	}
	got := WithoutPurposeParameter(tdAny)
	props, _ := got.Parameters["properties"].(map[string]any)
	if _, ok := props["purpose"]; ok {
		t.Fatal("purpose should be removed from properties")
	}
	reqAny, ok := got.Parameters["required"].([]any)
	if !ok {
		t.Fatalf("required should stay []any, got %T", got.Parameters["required"])
	}
	if len(reqAny) != 1 || reqAny[0] != "foo" {
		t.Fatalf("required = %v, want [foo]", reqAny)
	}

	// []string required branch.
	tdStr := llm.ToolDefinition{
		Name: "t",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"purpose": map[string]any{"type": "string"}},
			"required":   []string{"purpose", "bar"},
		},
	}
	got = WithoutPurposeParameter(tdStr)
	reqStr, ok := got.Parameters["required"].([]string)
	if !ok {
		t.Fatalf("required should stay []string, got %T", got.Parameters["required"])
	}
	if len(reqStr) != 1 || reqStr[0] != "bar" {
		t.Fatalf("required = %v, want [bar]", reqStr)
	}
}

func TestCloneSchemaMap_DeepClonesEverySliceKind(t *testing.T) {
	orig := map[string]any{
		"scalar":   "keep",
		"anyslice": []any{"a", map[string]any{"k": "v"}},
		"strs":     []string{"x", "y"},
		"ints":     []int{1, 2},
		"floats":   []float64{1.5, 2.5},
		"bools":    []bool{true, false},
		"nested":   map[string]any{"inner": []string{"z"}},
	}
	clone := CloneSchemaMap(orig)

	// Mutate every slice/map kind in the clone.
	clone["strs"].([]string)[0] = "MUT"
	clone["ints"].([]int)[0] = 99
	clone["floats"].([]float64)[0] = 9.9
	clone["bools"].([]bool)[0] = false
	clone["nested"].(map[string]any)["inner"].([]string)[0] = "MUT"
	clone["anyslice"].([]any)[1].(map[string]any)["k"] = "MUT"

	// Original must be untouched.
	if orig["strs"].([]string)[0] != "x" {
		t.Fatal("strs mutated in original")
	}
	if orig["ints"].([]int)[0] != 1 {
		t.Fatal("ints mutated in original")
	}
	if orig["floats"].([]float64)[0] != 1.5 {
		t.Fatal("floats mutated in original")
	}
	if orig["bools"].([]bool)[0] != true {
		t.Fatal("bools mutated in original")
	}
	if orig["nested"].(map[string]any)["inner"].([]string)[0] != "z" {
		t.Fatal("nested inner mutated in original")
	}
	if orig["anyslice"].([]any)[1].(map[string]any)["k"] != "v" {
		t.Fatal("anyslice nested map mutated in original")
	}

	// And the clone actually took the mutations (independence, not aliasing).
	if clone["ints"].([]int)[0] != 99 {
		t.Fatal("clone mutation lost")
	}
}

func TestCompileSchema(t *testing.T) {
	// nil params defaults to an object schema.
	s, err := compileSchema(nil)
	if err != nil {
		t.Fatalf("nil params: %v", err)
	}
	if s == nil {
		t.Fatal("nil params should compile to a non-nil schema")
	}

	// Non-object root type is rejected.
	if _, err := compileSchema(map[string]any{"type": "string"}); err == nil {
		t.Fatal("expected error for non-object root type")
	} else if !strings.Contains(err.Error(), "root type must be") {
		t.Fatalf("error %q missing 'root type must be'", err.Error())
	}

	// A schema that fails compilation (nil property value) returns an error.
	if _, err := compileSchema(map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": nil},
	}); err == nil {
		t.Fatal("expected error for nil property schema")
	}

	// A valid object schema compiles, and the cache returns the same pointer.
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"cov_s4_unique_field": map[string]any{"type": "string"}},
	}
	s1, err := compileSchema(params)
	if err != nil {
		t.Fatalf("valid schema: %v", err)
	}
	s2, err := compileSchema(map[string]any{
		"type":       "object",
		"properties": map[string]any{"cov_s4_unique_field": map[string]any{"type": "string"}},
	})
	if err != nil {
		t.Fatalf("cached schema: %v", err)
	}
	if s1 != s2 {
		t.Fatal("identical params should return the cached compiled schema")
	}
}

func TestParseImageResult(t *testing.T) {
	body := base64.StdEncoding.EncodeToString([]byte("PNGDATA"))
	cases := []struct {
		name    string
		path    string
		input   string
		wantNil bool
		wantMT  string
	}{
		{name: "valid png", path: "x.png", input: "[image: a chart]\n" + body, wantNil: false, wantMT: "image/png"},
		{name: "unknown extension falls back to png", path: "x.unknownext", input: "[image: a chart]\n" + body, wantNil: false, wantMT: "image/png"},
		{name: "not an image", path: "x.png", input: "plain text output", wantNil: true},
		{name: "no newline", path: "x.png", input: "[image: no newline here", wantNil: true},
		{name: "bad base64", path: "x.png", input: "[image: bad]\n!!!not base64!!!", wantNil: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseImageResult(tc.path, tc.input)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil ImageResult")
			}
			if string(got.Data) != "PNGDATA" {
				t.Fatalf("Data = %q, want PNGDATA", string(got.Data))
			}
			if got.MediaType != tc.wantMT {
				t.Fatalf("MediaType = %q, want %q", got.MediaType, tc.wantMT)
			}
			if got.Text != "[image: a chart]" {
				t.Fatalf("Text = %q", got.Text)
			}
		})
	}
}

func TestParseDocumentResult(t *testing.T) {
	body := base64.StdEncoding.EncodeToString([]byte("PDFDATA"))
	cases := []struct {
		name    string
		path    string
		input   string
		wantNil bool
		wantMT  string
	}{
		{name: "valid pdf", path: "x.pdf", input: "[document: a report]\n" + body, wantNil: false, wantMT: "application/pdf"},
		{name: "unknown extension falls back to pdf", path: "x.noext", input: "[document: a report]\n" + body, wantNil: false, wantMT: "application/pdf"},
		{name: "not a document", path: "x.pdf", input: "plain text", wantNil: true},
		{name: "no newline", path: "x.pdf", input: "[document: no newline", wantNil: true},
		{name: "bad base64", path: "x.pdf", input: "[document: bad]\n!!!not base64!!!", wantNil: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseDocumentResult(tc.path, tc.input)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil ImageResult")
			}
			if string(got.Data) != "PDFDATA" {
				t.Fatalf("Data = %q, want PDFDATA", string(got.Data))
			}
			if got.MediaType != tc.wantMT {
				t.Fatalf("MediaType = %q, want %q", got.MediaType, tc.wantMT)
			}
		})
	}
}

func TestRegister_InvalidNameAndMissingExecutor(t *testing.T) {
	r := NewRegistry()

	// Invalid tool name (empty).
	err := r.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: ""}},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			return "ok", nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "tool name") {
		t.Fatalf("expected tool-name error, got %v", err)
	}

	// Invalid tool name (space).
	err = r.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "bad name"}},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			return "ok", nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("expected invalid-name error, got %v", err)
	}

	// Nil executor.
	err = r.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "no_exec", Description: "x"}},
	})
	if err == nil || !strings.Contains(err.Error(), "missing executor") {
		t.Fatalf("expected missing-executor error, got %v", err)
	}
}

func TestRegister_BridgesExecuteAndAppliesDefaultLimit(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "apply_patch", Description: "x"}},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			return "bridged", nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got := r.Get("apply_patch")
	if got == nil {
		t.Fatal("tool not registered")
	}
	// Default limit for apply_patch is applied when none is set.
	want := defaultToolLimit("apply_patch")
	if got.Limit != want {
		t.Fatalf("Limit = %+v, want %+v", got.Limit, want)
	}
	// Execute was bridged from Exec.
	if got.Execute == nil {
		t.Fatal("Execute should be bridged from Exec")
	}
	out, err := got.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("bridged Execute: %v", err)
	}
	if out != "bridged" {
		t.Fatalf("bridged Execute output = %v, want bridged", out)
	}
}

func TestDefJobWatch_DescriptionInterpolatesKinds(t *testing.T) {
	none := DefJobWatch(nil)
	if none.Name != "job_watch" {
		t.Fatalf("Name = %q, want job_watch", none.Name)
	}
	if !strings.Contains(none.Description, "none available this session") {
		t.Fatalf("nil kinds should read 'none available this session':\n%s", none.Description)
	}

	some := DefJobWatch([]string{"a", "b"})
	if !strings.Contains(some.Description, "available: a, b") {
		t.Fatalf("kinds should interpolate 'a, b':\n%s", some.Description)
	}
	if strings.Contains(some.Description, "none available this session") {
		t.Fatal("non-empty kinds should not say 'none available this session'")
	}
}
