package msgrender

import (
	"testing"

	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
)

// FuzzRenderToolCall drives the package's real render-input decode seam:
// RenderToolCall parses the untrusted tool ArgumentsJSON via toolArgsFromJSON
// (json.Unmarshal into ToolArgs) and routes it through the per-tool renderer's
// Verb/Target/Result/Body funcs. Fuzzing (toolName, rawArgs, output, error)
// exercises the decoder plus the known-tool, MCP-fallback ("provider__op"), and
// unknown-tool renderers. Oracle: no-panic floor — a malformed args payload or
// surprising output must never crash the transcript renderer.
func FuzzRenderToolCall(f *testing.F) {
	seeds := []struct {
		name, args, output, errStr string
	}{
		{"read_file", `{"file_path":"/a/b.go","offset":1,"limit":5}`, "line\nline", ""},
		{"shell", `{"command":"ls -la","purpose":"list"}`, "out", ""},
		{"edit_file", `{"file_path":"/a.go","old_string":"x","new_string":"y"}`, "", ""},
		{"grep", `{"pattern":"TODO"}`, "match", ""},
		{"glob", `{"pattern":"**/*"}`, "", "boom"},
		{"prov__operation", `{"q":"hi","n":3}`, `{"data":1}`, ""},
		{"weird-unknown", `not-json`, "", ""},
		{"delegate", `{"task":"do\nthing"}`, "", ""},
		{"", "", "", ""},
		{"write_file", `{"file_path":"/x","content":"a\nb"}`, "", ""},
	}
	for _, s := range seeds {
		f.Add(s.name, s.args, s.output, s.errStr)
	}

	f.Fuzz(func(t *testing.T, name, args, output, errStr string) {
		// toolArgsFromJSON is the raw decode seam; assert it never returns nil.
		if a := toolArgsFromJSON(args); a == nil {
			t.Fatalf("toolArgsFromJSON(%q) returned nil map", args)
		}

		tc := transcript.ToolCallInfo{
			Name:     name,
			RawArgs:  args,
			Output:   output,
			Error:    errStr,
			Done:     true,
			Expanded: true,
		}
		// Two widths exercise the wrap/indent math; both must not panic.
		_ = RenderToolCall(tc, 80, false)
		_ = RenderToolCall(tc, 12, true)
	})
}
