package agent

// A step in a transcript is a tool call and its result: a commandExecution
// item carrying the tool's name and arguments, and the result item that
// settles it. Both clients read a step's summary ("Read agent/tree.go ·
// lines 1-40", "Ran go test ./agent/...") from those arguments and that
// output, so a summary written against hand-built items pins argument names
// the tools may not use.
//
// This test is the corpus for those summaries. It announces one call per
// tool, with the arguments each tool's own definition names
// (agent/internal/tool/definitions.go), and returns results the way a session
// records them (one ASSISTANT entry, one TOOL_RESULTS entry), projected
// through apptranscript the way history reaches the wire. The outputs are
// hand-written in each tool's shape. The AppWire package's toolSummaries
// tests and the phone's step tests read the file this test pins.
//
// Regenerate after an intentional change with `make fuzz-goldens`
// (wire_fixture_test.go).

import (
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// toolWireFixturePath is the committed corpus both clients read.
const toolWireFixturePath = "testdata/toolwire/calls.json"

type toolWireCall struct {
	id     string
	tool   string
	note   string
	args   map[string]any
	output string
	isErr  bool
}

func TestToolCallWireFixtures(t *testing.T) {
	t.Parallel()
	webFetch, err := json.Marshal(map[string]any{
		"answer":       "The release notes list three fixes to the tree settle pass.",
		"raw_file":     "/tmp/evener/web/release-notes.html",
		"url":          "https://example.com/release-notes",
		"content_type": "text/html",
		"size_bytes":   48213,
	})
	if err != nil {
		t.Fatalf("web_fetch output: %v", err)
	}
	patch := "*** Begin Patch\n*** Update File: agent/tree.go\n@@\n-\tdrain()\n+\tsettle()\n+\tdrain()\n*** Add File: agent/tree_order.go\n+package agent\n*** End Patch"

	calls := []toolWireCall{
		{
			id: "call_read_file", tool: "read_file",
			note:   "A whole-file read: the line range comes from the output's line count.",
			args:   map[string]any{"file_path": "agent/tree.go"},
			output: "package agent\n\nfunc settle() {}\n",
		},
		{
			id: "call_read_file_range", tool: "read_file",
			note:   "A read with an offset and a limit.",
			args:   map[string]any{"file_path": "agent/tree.go", "offset": 120, "limit": 40},
			output: "func settle() {\n}\n",
		},
		{
			id: "call_grep", tool: "grep",
			note:   "A search in a path, filtered by a glob.",
			args:   map[string]any{"pattern": "func settle", "path": "agent", "glob_filter": "*.go"},
			output: "agent/tree.go:12:func settle() {}\nagent/tree_test.go:40:func settleForTest() {}\n",
		},
		{
			id: "call_glob", tool: "glob",
			note:   "A glob match.",
			args:   map[string]any{"pattern": "agent/**/*_test.go"},
			output: "agent/tree_test.go\nagent/drain_test.go\nagent/settle_test.go\n",
		},
		{
			id: "call_list_dir", tool: "list_dir",
			note:   "A directory listing.",
			args:   map[string]any{"path": "agent/internal"},
			output: "tool/\nmcp/\ncontextmgr/\ndelegatestore/\n",
		},
		{
			id: "call_edit_file", tool: "edit_file",
			note:   "One replacement in one file.",
			args:   map[string]any{"file_path": "agent/tree.go", "old_string": "\tdrain()\n", "new_string": "\tsettle()\n\tdrain()\n"},
			output: "edited agent/tree.go: 1 replacement(s)",
		},
		{
			id: "call_write_file", tool: "write_file",
			note:   "A new file written whole.",
			args:   map[string]any{"file_path": "agent/tree_order.go", "content": "package agent\n"},
			output: "wrote 14 bytes to agent/tree_order.go",
		},
		{
			id: "call_apply_patch", tool: "apply_patch",
			note:   "A v4a patch updating one file and adding another.",
			args:   map[string]any{"patch": patch},
			output: "applied patch: 2 files",
		},
		{
			id: "call_shell", tool: "shell",
			note:   "A command run from the session's own directory, prefixed with cd to it.",
			args:   map[string]any{"command": "cd /home/jesse/git/evener && go test ./agent/...", "description": ""},
			output: "ok  \tprimeradiant.com/evener/agent\t12.3s\n",
		},
		{
			id: "call_shell_failed", tool: "shell",
			note:   "A command that exited 1.",
			args:   map[string]any{"command": "go vet ./agent/..."},
			output: "agent/tree.go:14:2: unreachable code\nexit status 1\n",
			isErr:  true,
		},
		{
			id: "call_web_fetch", tool: "web_fetch",
			note:   "A fetched page: the output is the tool's JSON result, with size_bytes.",
			args:   map[string]any{"url": "https://example.com/release-notes", "question": "What changed in the settle pass?"},
			output: string(webFetch),
		},
		{
			id: "call_web_search", tool: "web_search",
			note:   "A web search: one result per line.",
			args:   map[string]any{"query": "go race detector settle drain"},
			output: "Go race detector docs https://go.dev/doc/articles/race_detector\nData races in Go https://example.com/races\n",
		},
		{
			id: "call_use_skill", tool: "use_skill",
			note:   "A skill activation.",
			args:   map[string]any{"skill_name": "systematic-debugging"},
			output: "# Systematic debugging\n\nFind the root cause first.",
		},
		{
			id: "call_mcp", tool: "github__create_issue",
			note:   "An MCP tool, named <server>__<tool> the way agent/internal/mcp's manager namespaces it.",
			args:   map[string]any{"title": "Tree settle races the drain", "body": "Seen in go test -race."},
			output: `{"number":3210,"url":"https://github.com/prime-radiant-inc/evener/issues/3210"}`,
		},
		{
			id: "call_mcp_hyphenated", tool: "linear_app__list_issues",
			note:   "An MCP tool whose server name had a hyphen (linear-app), which the manager turns into an underscore.",
			args:   map[string]any{"team": "EVE"},
			output: `{"issues":[]}`,
		},
		{
			id: "call_unknown", tool: "compact_context",
			note:   "A tool no step summary covers yet.",
			args:   map[string]any{"note_to_self": "Next: run the race detector."},
			output: "compacted",
		},
	}

	announce := llm.Message{Role: llm.RoleAssistant}
	results := llm.Message{Role: llm.RoleTool}
	notes := make(map[string]string, len(calls))
	for _, call := range calls {
		args, err := json.Marshal(call.args)
		if err != nil {
			t.Fatalf("%s arguments: %v", call.id, err)
		}
		announce.Content = append(announce.Content, llm.ContentPart{
			Kind:     llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{ID: call.id, Name: call.tool, Arguments: args},
		})
		results.Content = append(results.Content, llm.ContentPart{
			Kind:       llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{ToolCallID: call.id, Name: call.tool, Content: call.output, IsError: call.isErr},
		})
		notes[call.id] = call.note
	}
	reg := apptranscript.NewToolCallRegistry()
	items := apptranscript.ProjectTurn("turn_1", 1, schema.Turn{Kind: schema.TurnAssistant, Message: announce, Timestamp: wireFixtureStart}, reg, nil, nil)
	items = append(items, apptranscript.ProjectTurn("turn_1", 2, schema.Turn{Kind: schema.TurnToolResults, Message: results, Timestamp: wireFixtureStart.Add(2 * time.Second)}, reg, nil, nil)...)

	checkWireFixture(t, toolWireFixturePath, struct {
		Note  string               `json:"note"`
		Cwd   string               `json:"cwd"`
		Notes map[string]string    `json:"notes"`
		Items []appwire.ThreadItem `json:"items"`
	}{
		Note:  "One ASSISTANT entry announcing one call per tool, and the TOOL_RESULTS entry answering them, projected through apptranscript. Arguments follow each tool's definition; outputs are hand-written in each tool's shape. cwd is the session directory the shell case's cd names.",
		Cwd:   "/home/jesse/git/evener",
		Notes: notes,
		Items: items,
	}, "the AppWire package and mobile-native tests that read it")
}
