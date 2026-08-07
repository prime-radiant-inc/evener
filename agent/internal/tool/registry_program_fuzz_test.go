package tool

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzToolRegistryProgram exercises the real Registry from registration through
// execution. The ordinary tool executor uses DenyEnv, which cannot touch disk,
// fork a process, or open a socket. The apply_patch executor is the one
// intentional FileMutator path and receives a fresh LocalExecutionEnvironment
// rooted in t.TempDir, so its mutation is confined to the test's own root.
//
// Its semantic checks cover registration/clone/restrict isolation, purpose
// handling, schema and JSON rejection, middleware, typed execution results,
// error propagation, output limits, and FileMutator capability handling.
func FuzzToolRegistryProgram(f *testing.F) {
	for _, seed := range []struct {
		payload string
		seed    uint64
	}{
		{payload: "hello", seed: 1},
		{payload: "line one\nline two", seed: 9},
		{payload: "", seed: 42},
		{payload: "unicode \U0001f642", seed: 99},
	} {
		f.Add(seed.payload, seed.seed)
	}

	f.Fuzz(func(t *testing.T, raw string, seed uint64) {
		if len(raw) > 512 {
			return
		}
		payload := toolProgramPayload(raw)
		reg, calls, readFileSawPurpose := toolProgramRegistry(t)
		deny := &agenttest.DenyEnv{WorkDir: t.TempDir(), Seed: seed}

		beforeCalls := *calls
		unknown := reg.ExecuteCall(context.Background(), deny, llm.ToolCallData{Name: "missing_program_tool", Arguments: []byte(`{}`)})
		if !unknown.IsError || !strings.Contains(unknown.Output, "unknown tool") || *calls != beforeCalls {
			t.Fatalf("unknown call = %+v calls=%d want no execution", unknown, *calls)
		}

		badJSON := reg.ExecuteCall(context.Background(), deny, llm.ToolCallData{Name: "echo_program", Arguments: []byte(`{not json`)})
		if !badJSON.IsError || !strings.Contains(badJSON.Output, "invalid tool arguments JSON") || *calls != beforeCalls {
			t.Fatalf("invalid JSON call = %+v calls=%d want no execution", badJSON, *calls)
		}

		badSchema := reg.ExecuteCall(context.Background(), deny, llm.ToolCallData{Name: "echo_program", Arguments: []byte(`{"value":7,"mode":0}`)})
		if !badSchema.IsError || !strings.Contains(badSchema.Output, "schema validation failed") || *calls != beforeCalls {
			t.Fatalf("invalid schema call = %+v calls=%d want no execution", badSchema, *calls)
		}

		blocked := toolProgramCall(t, reg, deny, "echo_program", "blocked", 0, true, "blocked-id")
		if !blocked.IsError || blocked.Err != nil || !strings.Contains(blocked.Output, "program middleware blocked") || *calls != beforeCalls {
			t.Fatalf("middleware result = %+v calls=%d want no execution", blocked, *calls)
		}

		for mode := range 6 {
			callID := "mode-id"
			if mode == 0 {
				callID = "" // synthesized shortHash branch
			}
			result := toolProgramCall(t, reg, deny, "echo_program", payload, mode, false, callID)
			if result.ToolName != "echo_program" || result.CallID == "" {
				t.Fatalf("mode %d malformed result: %+v", mode, result)
			}
			switch mode {
			case 1:
				if result.IsError || len(result.ToolState) == 0 || !strings.Contains(string(result.ToolState), payload) {
					t.Fatalf("state result = %+v", result)
				}
			case 2:
				if result.IsError || result.FullOutput != "full:"+payload || result.Output != "text:"+payload {
					t.Fatalf("text result = %+v", result)
				}
			case 3:
				if result.IsError || string(result.ImageData) != payload || result.ImageMediaType != "image/png" {
					t.Fatalf("image result = %+v", result)
				}
			case 4:
				if !result.IsError || result.Err == nil || result.FullOutput != "partial:"+payload {
					t.Fatalf("error result = %+v", result)
				}
			default:
				if result.IsError || result.FullOutput == "" {
					t.Fatalf("success result for mode %d = %+v", mode, result)
				}
			}
		}
		if *calls != beforeCalls+6 {
			t.Fatalf("executor calls = %d, want %d", *calls, beforeCalls+6)
		}

		if got := toolProgramCall(t, reg, deny, "read_file", payload, 0, false, "read-file"); got.IsError || !*readFileSawPurpose {
			t.Fatalf("read_file purpose preservation = result=%+v saw=%v", got, *readFileSawPurpose)
		}

		toolProgramPatchBoundary(t, reg, deny, payload)
		toolProgramIsolation(t, reg)
		toolProgramHelpers(t, payload)
		toolProgramRegistryEdges(t)
		toolProgramPatchCases(t, payload)
	})
}

func toolProgramRegistry(t *testing.T) (*Registry, *int, *bool) {
	t.Helper()
	reg := NewRegistry()
	calls := new(int)
	readFileSawPurpose := new(bool)

	if err := reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name:        "echo_program",
			Description: "deterministic fuzz registry executor",
			Parameters: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"value": map[string]any{"type": "string"},
					"mode":  map[string]any{"type": "integer"},
					"block": map[string]any{"type": "boolean"},
				},
				"required": []string{"value", "mode"},
			},
		}},
		Exec: func(_ context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			*calls++
			if _, found := args["purpose"]; found {
				return nil, errors.New("purpose leaked to non-read_file executor")
			}
			value, _ := args["value"].(string)
			mode, _ := args["mode"].(float64)
			switch int(mode) {
			case 1:
				return StateResult{Output: "state:" + value, State: map[string]string{"value": value}}, nil
			case 2:
				return TextResult{Output: "text:" + value, FullOutput: "full:" + value}, nil
			case 3:
				return ImageResult{Text: "image:" + value, Data: []byte(value), MediaType: "image/png", Purpose: "program"}, nil
			case 4:
				return "partial:" + value, errors.New("program executor error")
			case 5:
				return map[string]string{"value": value}, nil
			default:
				return "plain:" + value, nil
			}
		},
	}); err != nil {
		t.Fatalf("register echo_program: %v", err)
	}

	if err := reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name:        "read_file",
			Description: "program read_file purpose witness",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"value": map[string]any{"type": "string"}, "mode": map[string]any{"type": "integer"}, "block": map[string]any{"type": "boolean"}},
				"required":   []string{"value", "mode"},
			},
		}},
		Exec: func(_ context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_, *readFileSawPurpose = args["purpose"]
			return "read:" + fmt.Sprint(args["value"]), nil
		},
	}); err != nil {
		t.Fatalf("register read_file: %v", err)
	}

	if err := reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "communicate", Description: "program result tool", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}},
		Exec: func(_ context.Context, _ execenv.ExecutionEnvironment, _ map[string]any) (any, error) {
			return "communicated", nil
		},
	}); err != nil {
		t.Fatalf("register communicate: %v", err)
	}

	if err := reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name:        "apply_patch",
			Description: "program FileMutator boundary",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{"patch": map[string]any{"type": "string"}}, "required": []string{"patch"}},
		}},
		Exec: func(_ context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			fm, ok := env.(execenv.FileMutator)
			if !ok {
				return nil, errors.New("apply_patch requires FileMutator")
			}
			patch, _ := args["patch"].(string)
			return ApplyPatch(fm, patch)
		},
	}); err != nil {
		t.Fatalf("register apply_patch: %v", err)
	}

	reg.Use(func(_ context.Context, name string, args map[string]any) error {
		if name == "echo_program" && args["block"] == true {
			return errors.New("program middleware blocked")
		}
		return nil
	})
	return reg, calls, readFileSawPurpose
}

func toolProgramCall(t *testing.T, reg *Registry, env execenv.ExecutionEnvironment, name, value string, mode int, block bool, callID string) ExecResult {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"value": value, "mode": mode, "block": block, "purpose": "fuzz registry program"})
	if err != nil {
		t.Fatalf("marshal %s args: %v", name, err)
	}
	return reg.ExecuteCall(context.Background(), env, llm.ToolCallData{ID: callID, Name: name, Arguments: raw})
}

func toolProgramPatchBoundary(t *testing.T, reg *Registry, deny *agenttest.DenyEnv, payload string) {
	t.Helper()
	patch := "*** Begin Patch\n*** Add File: program.txt\n+" + payload + "\n*** End Patch\n"
	raw, err := json.Marshal(map[string]string{"patch": patch, "purpose": "exercise FileMutator"})
	if err != nil {
		t.Fatalf("marshal patch args: %v", err)
	}
	withoutMutator := reg.ExecuteCall(context.Background(), deny, llm.ToolCallData{ID: "missing-mutator", Name: "apply_patch", Arguments: raw})
	if !withoutMutator.IsError || withoutMutator.Err == nil || !strings.Contains(withoutMutator.Output, "FileMutator") {
		t.Fatalf("missing FileMutator result = %+v", withoutMutator)
	}

	root := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(root)
	withMutator := reg.ExecuteCall(context.Background(), env, llm.ToolCallData{ID: "with-mutator", Name: "apply_patch", Arguments: raw})
	if withMutator.IsError || !strings.Contains(withMutator.Output, "program.txt") {
		t.Fatalf("FileMutator patch result = %+v", withMutator)
	}
	got, err := os.ReadFile(filepath.Join(root, "program.txt"))
	if err != nil || string(got) != payload+"\n" {
		t.Fatalf("FileMutator output = %q err=%v, want %q", got, err, payload+"\n")
	}
}

func toolProgramIsolation(t *testing.T, reg *Registry) {
	t.Helper()
	original := reg.RegisteredNames()
	clone := reg.Clone()
	clone.Remove("apply_patch")
	allowed := map[string]bool{"echo_program": true}
	clone.Restrict(allowed)
	if !allowed["communicate"] || clone.Get("echo_program") == nil || clone.Get("communicate") == nil || len(clone.Names()) != 2 {
		t.Fatalf("Restrict clone = names=%v allowed=%v", clone.Names(), allowed)
	}
	if reg.Get("apply_patch") == nil || len(reg.RegisteredNames()) != len(original) {
		t.Fatalf("clone mutation changed original registry: %v", reg.Names())
	}

	custom := reg.Clone()
	custom.RestrictKeepingResultTool(map[string]bool{"echo_program": true}, "read_file")
	if custom.Get("echo_program") == nil || custom.Get("read_file") == nil || len(custom.Names()) != 2 {
		t.Fatalf("custom result restrict = %v", custom.Names())
	}

	reg.OverrideLimits(map[string]schema.ToolOutputLimit{
		"echo_program": {MaxChars: 4, MaxLines: 1, Strategy: schema.TruncTail},
		"missing":      {MaxChars: 1},
	})
	got := reg.Get("echo_program")
	if got == nil || got.Limit.MaxChars != 4 || got.Limit.MaxLines != 1 || got.Limit.Strategy != schema.TruncTail {
		t.Fatalf("OverrideLimits = %+v", got)
	}
	reg.Unregister("communicate")
	if reg.Get("communicate") != nil {
		t.Fatal("Unregister left communicate registered")
	}
	reg.Remove("communicate") // idempotent missing removal
}

func toolProgramHelpers(t *testing.T, payload string) {
	t.Helper()
	imageData := base64.StdEncoding.EncodeToString([]byte(payload))
	if got := ParseImageResult("asset.png", "[image: asset]\n"+imageData); got == nil || string(got.Data) != payload || got.MediaType != "image/png" {
		t.Fatalf("ParseImageResult = %+v", got)
	}
	if got := ParseImageResult("asset.png", "[image: asset]\nnot-base64"); got != nil {
		t.Fatalf("invalid image result = %+v, want nil", got)
	}
	if got := ParseDocumentResult("asset.pdf", "[document: asset]\n"+imageData); got == nil || string(got.Data) != payload || got.MediaType != "application/pdf" {
		t.Fatalf("ParseDocumentResult = %+v", got)
	}
	if got := ParseDocumentResult("asset.pdf", "plain text"); got != nil {
		t.Fatalf("plain document result = %+v, want nil", got)
	}

	for _, name := range []string{"read_file", "shell", "grep", "glob", "edit_file", "apply_patch", "write_file", "delegate", "task_list", "web_fetch", "communicate", "use_skill", "other"} {
		// grep/glob bound by entry count (MaxLines) via TruncHeadCount rather
		// than by character count; every other tool still bounds by MaxChars.
		if lim := defaultToolLimit(name); (lim.MaxChars <= 0 && lim.MaxLines <= 0) || lim.Strategy == "" {
			t.Fatalf("defaultToolLimit(%q) = %+v", name, lim)
		}
	}
	if got := truncateChars("abcdef", 4, schema.TruncTail); !strings.HasSuffix(got, "cdef") {
		t.Fatalf("tail truncation = %q", got)
	}
	if got := truncateLines("a\nb\nc\nd", 2); !strings.Contains(got, "lines omitted") {
		t.Fatalf("line truncation = %q", got)
	}
	if got := shortHash([]byte(payload)); len(got) != 16 {
		t.Fatalf("shortHash length = %d, want 16", len(got))
	}
	if got := toolValueToString([]byte(payload)); got != payload {
		t.Fatalf("toolValueToString bytes = %q, want %q", got, payload)
	}
}

// toolProgramRegistryEdges is a compact behavioral matrix for Registry's
// exceptional paths. The fuzzer's mutable payload is deliberately not used for
// schemas here: each case has one stable, independently checkable contract.
func toolProgramRegistryEdges(t *testing.T) {
	t.Helper()
	if got := WithPurposeParameter(llm.ToolDefinition{}); got.Parameters["type"] != "object" {
		t.Fatalf("nil schema purpose injection = %#v", got.Parameters)
	}
	nonObject := llm.ToolDefinition{Parameters: map[string]any{"type": "string"}}
	if got := WithPurposeParameter(nonObject); got.Parameters["type"] != "string" {
		t.Fatalf("non-object schema changed: %#v", got.Parameters)
	}
	withRequired := llm.ToolDefinition{Parameters: map[string]any{
		"type":       "object",
		"properties": map[string]any{"purpose": map[string]any{"type": "string"}},
		"required":   []any{"purpose", 9, "value"},
	}}
	without := WithoutPurposeParameter(withRequired)
	if got := without.Parameters["required"].([]any); len(got) != 2 || got[0] != 9 || got[1] != "value" {
		t.Fatalf("purpose removal retained wrong required list: %#v", got)
	}

	cloneSource := map[string]any{
		"strings": []string{"a"},
		"ints":    []int{1},
		"floats":  []float64{1.5},
		"bools":   []bool{true},
		"any":     []any{map[string]any{"nested": "keep"}},
	}
	clone := CloneSchemaMap(cloneSource)
	clone["strings"].([]string)[0] = "mutated"
	clone["ints"].([]int)[0] = 2
	clone["floats"].([]float64)[0] = 2.5
	clone["bools"].([]bool)[0] = false
	clone["any"].([]any)[0].(map[string]any)["nested"] = "mutated"
	if cloneSource["strings"].([]string)[0] != "a" || cloneSource["ints"].([]int)[0] != 1 || cloneSource["floats"].([]float64)[0] != 1.5 || !cloneSource["bools"].([]bool)[0] || cloneSource["any"].([]any)[0].(map[string]any)["nested"] != "keep" {
		t.Fatalf("CloneSchemaMap shared a value: %#v", cloneSource)
	}

	var nilRegistry *Registry
	if clone := nilRegistry.Clone(); clone == nil || len(clone.Names()) != 0 {
		t.Fatalf("nil registry clone = %#v", clone)
	}
	emptyBacking := &Registry{}
	if err := emptyBacking.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "late_map", Description: "late map", Parameters: map[string]any{"type": "object"}}},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) { return "ok", nil },
	}); err != nil || emptyBacking.Get("late_map") == nil {
		t.Fatalf("register into nil backing map: err=%v names=%v", err, emptyBacking.Names())
	}
	for _, bad := range []RegisteredTool{
		{Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "bad name", Parameters: map[string]any{"type": "object"}}}, Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) { return nil, nil }},
		{Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "missing_exec", Description: "missing executor", Parameters: map[string]any{"type": "object"}}}},
		{Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "bad_root", Description: "bad root", Parameters: map[string]any{"type": "array"}}}, Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) { return nil, nil }},
	} {
		if err := NewRegistry().Register(bad); err == nil {
			t.Fatalf("invalid registration unexpectedly succeeded: %+v", bad.Definition)
		}
	}

	params := map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}}
	reuse := NewRegistry()
	firstExec := func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) { return "first", nil }
	if err := reuse.Register(RegisteredTool{Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "schema_reuse", Description: "reuse", Parameters: params}}, Exec: firstExec}); err != nil {
		t.Fatalf("initial schema registration: %v", err)
	}
	first := reuse.Get("schema_reuse")
	if first == nil || first.Schema == nil || first.Execute == nil {
		t.Fatalf("initial registered tool incomplete: %+v", first)
	}
	if _, err := first.Execute(context.Background(), "not-a-map"); err == nil {
		t.Fatal("bridged Execute accepted a non-map argument")
	}
	if out, err := first.Execute(context.Background(), map[string]any{"value": "x"}); err != nil || out != "first" {
		t.Fatalf("bridged Execute = %v, %v", out, err)
	}
	if err := reuse.Register(RegisteredTool{Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "schema_reuse", Description: "reuse", Parameters: params}}, Exec: firstExec}); err != nil {
		t.Fatalf("schema re-registration: %v", err)
	}
	if second := reuse.Get("schema_reuse"); second == nil || second.Schema != first.Schema {
		t.Fatalf("schema was not reused: first=%p second=%+v", first.Schema, second)
	}

	if _, err := compileSchema(nil); err != nil {
		t.Fatalf("nil schema did not compile: %v", err)
	}
	if _, err := compileSchema(map[string]any{"type": "string"}); err == nil {
		t.Fatal("non-object schema compiled")
	}
	if _, err := compileSchema(map[string]any{"type": "object", "properties": map[string]any{"bad": make(chan int)}}); err == nil {
		t.Fatal("non-JSON schema compiled")
	}
	if _, err := compileSchema(map[string]any{"type": "object", "properties": map[string]any{"bad": nil}}); err == nil {
		t.Fatal("invalid property schema compiled")
	}

	unserializable := make(chan int)
	if got := toolValueToString(unserializable); got == "" || got != toolValueToString(unserializable) {
		t.Fatalf("unserializable tool value = %q", got)
	}
	if got := truncateChars("short", 0, schema.TruncHeadTail); got != "short" {
		t.Fatalf("nonpositive char limit = %q", got)
	}
	if got := truncateChars("abcdef", 4, schema.TruncHeadTail); !strings.Contains(got, "removed from the middle") {
		t.Fatalf("head-tail truncation = %q", got)
	}
	if got := truncateLines("a\nb", 0); got != "a\nb" {
		t.Fatalf("nonpositive line limit = %q", got)
	}
	if got := truncateLines("a\nb", 3); got != "a\nb" {
		t.Fatalf("untruncated lines = %q", got)
	}
}

// toolProgramMemMutator is a small deterministic FileMutator used only to
// observe ApplyPatch's operation-level error propagation. Successful patch
// behavior is separately checked through the real LocalExecutionEnvironment in
// toolProgramPatchBoundary.
type toolProgramMemMutator struct {
	files map[string][]byte
	fail  string
}

func (m *toolProgramMemMutator) ReadFileRaw(path string) ([]byte, error) {
	if m.fail == "read" {
		return nil, errors.New("program read failure")
	}
	b, ok := m.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), b...), nil
}

func (m *toolProgramMemMutator) WriteFileRaw(path string, data []byte, _ os.FileMode) error {
	if m.fail == "write" {
		return errors.New("program write failure")
	}
	m.files[path] = append([]byte(nil), data...)
	return nil
}

func (m *toolProgramMemMutator) RemovePath(path string) error {
	if m.fail == "remove" {
		return errors.New("program remove failure")
	}
	delete(m.files, path)
	return nil
}

func (m *toolProgramMemMutator) RenamePath(oldPath, newPath string) error {
	if m.fail == "rename" {
		return errors.New("program rename failure")
	}
	b, ok := m.files[oldPath]
	if !ok {
		return os.ErrNotExist
	}
	delete(m.files, oldPath)
	m.files[newPath] = b
	return nil
}

func toolProgramPatchCases(t *testing.T, payload string) {
	t.Helper()
	for _, patch := range []string{
		"",
		"*** Begin Patch\n",
		"*** Begin Patch\n*** Add File: x\nnot plus\n*** End Patch\n",
		"*** Begin Patch\nunknown\n*** End Patch\n",
	} {
		if _, err := parseV4APatch(patch); err == nil {
			t.Fatalf("malformed patch parsed: %q", patch)
		}
	}
	if ops, err := parseV4APatch("*** Begin Patch\n\n*** End of File\n*** End Patch\n"); err != nil || len(ops) != 0 {
		t.Fatalf("empty patch parse = %#v, %v", ops, err)
	}

	m := &toolProgramMemMutator{files: map[string][]byte{"file.txt": []byte("target\nhint\nkeep\n")}}
	update := "*** Begin Patch\n*** Update File: file.txt\n@@ hint\n-target\n+" + payload + "\n*** End Patch\n"
	if _, err := ApplyPatch(m, update); err != nil || string(m.files["file.txt"]) != payload+"\nhint\nkeep\n" {
		t.Fatalf("hint fallback update = %q err=%v", m.files["file.txt"], err)
	}
	if _, err := ApplyPatch(m, "*** Begin Patch\n*** Update File: file.txt\n@@\n+prefix\n*** End Patch\n"); err != nil || !strings.HasPrefix(string(m.files["file.txt"]), "prefix\n") {
		t.Fatalf("pure-add update = %q err=%v", m.files["file.txt"], err)
	}
	if _, err := ApplyPatch(m, "*** Begin Patch\n*** Update File: file.txt\n*** Move to: moved.txt\n@@\n hint\n+after\n*** End Patch\n"); err != nil || m.files["moved.txt"] == nil || m.files["file.txt"] != nil {
		t.Fatalf("move update files=%#v err=%v", m.files, err)
	}
	if _, err := ApplyPatch(m, "*** Begin Patch\n*** Delete File: moved.txt\n*** End Patch\n"); err != nil || m.files["moved.txt"] != nil {
		t.Fatalf("delete update files=%#v err=%v", m.files, err)
	}

	for _, tc := range []struct {
		name  string
		fail  string
		patch string
	}{
		{name: "add write", fail: "write", patch: "*** Begin Patch\n*** Add File: a.txt\n+x\n*** End Patch\n"},
		{name: "delete remove", fail: "remove", patch: "*** Begin Patch\n*** Delete File: a.txt\n*** End Patch\n"},
		{name: "update read", fail: "read", patch: "*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n*** End Patch\n"},
		{name: "update write", fail: "write", patch: "*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n*** End Patch\n"},
		{name: "update rename", fail: "rename", patch: "*** Begin Patch\n*** Update File: a.txt\n*** Move to: b.txt\n@@\n-old\n+new\n*** End Patch\n"},
	} {
		m := &toolProgramMemMutator{files: map[string][]byte{"a.txt": []byte("old\n")}, fail: tc.fail}
		if _, err := ApplyPatch(m, tc.patch); err == nil || !strings.Contains(err.Error(), "program ") {
			t.Fatalf("%s error = %v", tc.name, err)
		}
	}
	if _, err := ApplyPatch(&toolProgramMemMutator{files: map[string][]byte{"a.txt": []byte("one\n")}}, "*** Begin Patch\n*** Update File: a.txt\n@@\n-missing\n+new\n*** End Patch\n"); err == nil || !strings.Contains(err.Error(), "expected lines not found") {
		t.Fatalf("mismatch diagnostic error = %v", err)
	}

	toolProgramPatchHelpers(t)
}

func toolProgramPatchHelpers(t *testing.T) {
	t.Helper()
	lines := []string{"alpha", "target", "hint", "target", "target again", "omega"}
	if got := candidateLineIndexes(lines, "target", 2, 2); len(got) == 0 || got[0] == 1 {
		t.Fatalf("candidate lines = %v", got)
	}
	if got := candidateSequenceIndexes(lines, []string{"target", "omega"}, 0, 2); len(got) == 0 {
		t.Fatalf("loose sequence candidates = %v", got)
	}
	if got := candidateSequenceIndexes(lines, nil, 0, 2); got != nil {
		t.Fatalf("empty sequence candidates = %v", got)
	}
	if got := looseCandidateSequenceIndexes(lines, []string{"target", "omega"}, 3, 2); len(got) == 0 {
		t.Fatalf("loose candidates = %v", got)
	}
	if got := nearestIndexes([]int{5, 1, 3, 0}, 3, 2); len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Fatalf("nearest indexes = %v", got)
	}
	if got := nearestIndexes([]int{4, 3, 2}, 0, 2); len(got) != 2 || got[0] != 4 {
		t.Fatalf("zero-line nearest indexes = %v", got)
	}
	if got := formatPatchMismatchError(patchMismatchDiagnostic{kind: "missing", path: "f", want: "target", gotEOF: true, line: 7, lines: lines, hunkLines: []string{"@@ hint", "-target", "+new"}}); !strings.Contains(got, "end of file") || !strings.Contains(got, "Expected old/context") {
		t.Fatalf("mismatch diagnostic = %q", got)
	}
	if got := formatLineSnippet(nil, 0, 1); !strings.Contains(got, "empty") {
		t.Fatalf("empty line snippet = %q", got)
	}
	if got := formatExpectedLines(make([]string, 13)); !strings.Contains(got, "1 more lines") {
		t.Fatalf("expected-line truncation = %q", got)
	}
	if got := replacementEntriesFromHunk([]string{"@@ h", "", "?ignored", " ", "-old", "+new"}); len(got) != 3 {
		t.Fatalf("replacement entries = %#v", got)
	}
	if got := applyReplacementEntries(nil, []replacementEntry{{prefix: ' ', body: "fallback"}, {prefix: '-', body: "old"}, {prefix: '+', body: "new"}}); strings.Join(got, ",") != "fallback,new" {
		t.Fatalf("replacement application = %v", got)
	}
	for _, tc := range []struct {
		mode lineMatchMode
		a    string
		b    string
	}{
		{matchExact, "value", "value"},
		{matchTrimRight, "value  ", "value"},
		{matchTrimBoth, " value ", "value"},
		{matchUnicodeNormalized, "say \u201Chi\u201D", "say \"hi\""},
		{matchFuzzyLine, "two  words", "two words"},
	} {
		if !lineMatchesMode(tc.a, tc.b, tc.mode) {
			t.Fatalf("line mode %d failed for %q / %q", tc.mode, tc.a, tc.b)
		}
	}
	if lineMatchesMode("x", "x", lineMatchMode(99)) {
		t.Fatal("unknown line match mode succeeded")
	}
	if lineAt(lines, -1) != "" || lineAt(lines, len(lines)) != "" || lineAt(lines, 1) != "target" {
		t.Fatalf("lineAt bounds broken")
	}
	if got := indexOfLine([]string{"say \u201Chi\u201D"}, "say \"hi\"", 0); got != 0 {
		t.Fatalf("unicode index = %d", got)
	}
	if got := indexOfLine([]string{"x"}, "  ", 0); got != -1 {
		t.Fatalf("blank index = %d", got)
	}
}

func toolProgramPayload(raw string) string {
	if len(raw) > 96 {
		raw = raw[:96]
	}
	return hex.EncodeToString([]byte(raw))
}
