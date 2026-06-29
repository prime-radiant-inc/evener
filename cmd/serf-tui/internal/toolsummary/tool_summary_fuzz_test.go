package toolsummary

import "testing"

// FuzzSummarizeTool drives the package's real one-line/detail renderer, whose
// parse seam is the json.Unmarshal of the tool's arguments JSON in
// SummarizeTool. Every branch (shell/read_file/edit_file/task_list/...) decodes
// the same untrusted argsJSON map, so a single fuzz over (toolName, argsJSON)
// reaches every per-tool summarizer. Oracle: no-panic floor — a malformed or
// surprising args payload must never crash the transcript renderer.
func FuzzSummarizeTool(f *testing.F) {
	seeds := []struct {
		tool string
		args string
	}{
		{"shell", `{"command":"ls -la","purpose":"list"}`},
		{"read_file", `{"file_path":"/a/b/c.go","offset":10,"limit":20}`},
		{"write_file", `{"file_path":"/a/b.go","content":"x\ny\nz"}`},
		{"edit_file", `{"file_path":"/a.go","old_string":"foo","new_string":"bar"}`},
		{"glob", `{"pattern":"**/*.go","path":"/src"}`},
		{"grep", `{"pattern":"TODO","path":"/src"}`},
		{"task_list", `{"action":"append","tasks":[{"description":"d","prompt":"p\nq"}]}`},
		{"task_list", `{"action":"update","updates":[{"id":1,"status":"done"}]}`},
		{"web_search", `{"query":"go fuzzing"}`},
		{"delegate", `{"task":"do a thing\nover two lines"}`},
		{"communicate", `{"message":"hi"}`},
		{"communicate", `{"output":{"message":"nested"}}`},
		{"some_mcp__op", `{"a":"b","n":3,"flag":true}`},
		{"", ""},
		{"unknown", `not json`},
		// A task_list update element lacking a numeric "id" once panicked via an
		// unchecked m["id"].(float64) assertion in renderTaskUpdate; the fix
		// (idF, _ := m["id"].(float64)) makes it a no-op. Kept as a seed so the
		// fix stays regression-guarded by the corpus, not only under -fuzz.
		{"task_list", `{"action":"update","updates":[{}]}`},
	}
	for _, s := range seeds {
		f.Add(s.tool, s.args)
	}

	f.Fuzz(func(t *testing.T, toolName, argsJSON string) {
		// Floor oracle: the renderer must not panic on any input.
		desc, detail := SummarizeTool(toolName, argsJSON)
		_ = desc
		_ = detail
	})
}
