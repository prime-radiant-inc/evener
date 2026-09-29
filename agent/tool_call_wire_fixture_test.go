package agent

// A step in a transcript is a tool call and its result: a commandExecution
// item carrying the tool's name and arguments, and the result item that
// settles it. Both clients read a step's summary ("Read agent/tree.go ·
// lines 1-4", "Ran ls agent") from those arguments and that output, so a
// summary written against hand-built items pins argument names and output
// shapes the tools may not use.
//
// This test is the corpus for those summaries. It runs each core tool for
// real, through a session's own registry against a temp workspace, with the
// arguments each tool's definition names (agent/internal/tool/definitions.go),
// and records the calls and results the way a session does (one ASSISTANT
// entry, one TOOL_RESULTS entry), projected through apptranscript the way
// history reaches the wire.
//
// Four outputs are hand-written in their tool's shape, because the tool can't
// run here: web_fetch and web_search need the network (and a model), and the
// two MCP tools need an MCP server. The uncovered tool is hand-written too;
// only its name matters to a summary.
//
// To keep the corpus the same on every machine, the temp workspace's path is
// rewritten to a fixed one (toolWireCwd), and grep's and glob's result lines
// are sorted: glob orders by modification time and grep by its walk, which
// neither pins. grep's output also differs by whether ripgrep is installed
// (execenv's Grep uses it when it can): ripgrep prints absolute paths and a
// trailing newline, the native fallback paths relative to the searched
// directory and none. The corpus records the fallback's form.
//
// Regenerate after an intentional change with `make fuzz-goldens`
// (wire_fixture_test.go).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// toolWireFixturePath is the committed corpus both clients read.
const toolWireFixturePath = "testdata/toolwire/calls.json"

// toolWireCwd is the fixed path the temp workspace is recorded as.
const toolWireCwd = "/home/jesse/git/evener"

type toolWireCall struct {
	id   string
	tool string
	note string
	args map[string]any
	// output is the recorded result for a tool that can't run here; empty
	// means the tool runs for real.
	output string
	// sortLines sorts the real output's lines, for a tool whose order
	// depends on the filesystem.
	sortLines bool
	// grepForm writes the output in the native grep fallback's form: paths
	// relative to the searched directory, no trailing newline.
	grepForm bool
	// noState leaves the tool's state off its result, for a tool whose state
	// changes every run.
	noState bool
}

// toolWireWorkspace writes the files the calls read, search and edit, and
// installs the skill use_skill activates.
func toolWireWorkspace(t *testing.T) (string, *Session) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"agent/tree.go":       "package agent\n\nfunc settle() {}\n",
		"agent/tree_test.go":  "package agent\n\nfunc settleForTest() {}\n",
		"agent/drain_test.go": "package agent\n",
		"skills/systematic-debugging/SKILL.md": "---\nname: systematic-debugging\ndescription: Find the root cause first\n---\n" +
			"# Systematic debugging\n\nFind the root cause first.\n",
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := newSession(t, withDir(dir))
	skillDir := filepath.Join(dir, "skills", "systematic-debugging")
	s.skills.Entries["systematic-debugging"] = skill.Descriptor{
		CatalogName: "systematic-debugging",
		Controls:    skill.InvocationControls{UserInvocable: true},
		Meta: skill.SkillMeta{
			Name:        "systematic-debugging",
			Description: "Find the root cause first",
			Dir:         skillDir,
			SkillFile:   filepath.Join(skillDir, "SKILL.md"),
		},
	}
	return dir, s
}

// nativeGrepForm writes ripgrep's output the way execenv's native Grep
// fallback does: each path relative to the searched directory, no trailing
// newline. The fallback's own output passes through unchanged.
func nativeGrepForm(output, dir, searched string) string {
	base := filepath.Join(dir, searched) + string(filepath.Separator)
	resolvedBase := base
	if resolved, err := filepath.EvalSymlinks(filepath.Join(dir, searched)); err == nil {
		resolvedBase = resolved + string(filepath.Separator)
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimPrefix(strings.TrimPrefix(line, resolvedBase), base)
	}
	return strings.Join(lines, "\n")
}

func sortedLines(text string) string {
	trailing := strings.HasSuffix(text, "\n")
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	sort.Strings(lines)
	joined := strings.Join(lines, "\n")
	if trailing {
		joined += "\n"
	}
	return joined
}

func TestToolCallWireFixtures(t *testing.T) {
	t.Parallel()
	dir, s := toolWireWorkspace(t)

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
	patch := "*** Begin Patch\n*** Update File: agent/tree.go\n@@\n func settle() {\n+\tlog()\n*** Add File: agent/tree_drain.go\n+package agent\n*** End Patch"

	// In order: each call runs against the workspace the calls before it left.
	calls := []toolWireCall{
		{
			id: "call_read_file", tool: "read_file",
			note: "A whole-file read: the output numbers each line.",
			args: map[string]any{"file_path": "agent/tree.go"},
		},
		{
			id: "call_read_file_range", tool: "read_file",
			note: "A read with an offset and a limit.",
			args: map[string]any{"file_path": "agent/tree.go", "offset": 2, "limit": 2},
		},
		{
			id: "call_grep", tool: "grep",
			note:      "A search in a path, filtered by a glob: one path:line:text line per hit (sorted here).",
			args:      map[string]any{"pattern": "func settle", "path": "agent", "glob_filter": "*.go"},
			sortLines: true,
			grepForm:  true,
		},
		{
			id: "call_glob", tool: "glob",
			note:      "A glob match: one path per match (sorted here).",
			args:      map[string]any{"pattern": "agent/**/*_test.go"},
			sortLines: true,
		},
		{
			id: "call_list_dir", tool: "list_dir",
			note: "A directory listing: one name and size per entry, then a blank line and the entry count.",
			args: map[string]any{"path": "agent"},
		},
		{
			id: "call_edit_file", tool: "edit_file",
			note: "One replacement in one file.",
			args: map[string]any{"file_path": "agent/tree.go", "old_string": "func settle() {}\n", "new_string": "func settle() {\n\tdrain()\n}\n"},
		},
		{
			id: "call_write_file", tool: "write_file",
			note: "A new file written whole.",
			args: map[string]any{"file_path": "agent/tree_order.go", "content": "package agent\n"},
		},
		{
			id: "call_apply_patch", tool: "apply_patch",
			note: "A v4a patch updating one file and adding another.",
			args: map[string]any{"patch": patch},
		},
		{
			id: "call_shell", tool: "shell",
			note: "A command run from the session's own directory, prefixed with cd to it: the output ends with its exit code.",
			args: map[string]any{"command": "cd " + dir + " && cat agent/tree_order.go"},
		},
		{
			id: "call_shell_failed", tool: "shell",
			note: "A command that exited 1: still a result, not an error, with [exit 1] at its end.",
			args: map[string]any{"command": "test -f agent/missing.go"},
		},
		{
			id: "call_web_fetch", tool: "web_fetch",
			note:   "Hand-written (web_fetch needs the network and a model): a fetched page's JSON result, with size_bytes.",
			args:   map[string]any{"url": "https://example.com/release-notes", "question": "What changed in the settle pass?"},
			output: string(webFetch),
		},
		{
			id: "call_web_search", tool: "web_search",
			note:   "Hand-written (web_search needs the network and a model): one result per line.",
			args:   map[string]any{"query": "go race detector settle drain"},
			output: "Go race detector docs https://go.dev/doc/articles/race_detector\nData races in Go https://example.com/races\n",
		},
		{
			id: "call_use_skill", tool: "use_skill",
			note:    "A skill activation, the skill installed in the workspace. Its tool state (the session's random id, a digest of the rendered skill) changes every run, so it is left off.",
			args:    map[string]any{"skill_name": "systematic-debugging"},
			noState: true,
		},
		{
			id: "call_mcp", tool: "github__create_issue",
			note:   "Hand-written (needs an MCP server): an MCP tool, named <server>__<tool> the way agent/internal/mcp's manager namespaces it.",
			args:   map[string]any{"title": "Tree settle races the drain", "body": "Seen in go test -race."},
			output: `{"number":3210,"url":"https://github.com/prime-radiant-inc/evener/issues/3210"}`,
		},
		{
			id: "call_mcp_hyphenated", tool: "linear_app__list_issues",
			note:   "Hand-written (needs an MCP server): an MCP tool whose server name had a hyphen (linear-app), which the manager turns into an underscore.",
			args:   map[string]any{"team": "EVE"},
			output: `{"issues":[]}`,
		},
		{
			id: "call_unknown", tool: "compact_context",
			note:   "Hand-written: a tool no step summary covers; only its name matters.",
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
		output, isErr := call.output, false
		var state json.RawMessage
		if output == "" {
			res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{ID: call.id, Name: call.tool, Arguments: args})
			// The result as a session records it, less its duration, which
			// differs every run.
			output, isErr, state = res.Output, res.IsError, res.ToolState
			if call.noState {
				state = nil
			}
			if isErr {
				t.Fatalf("%s: the %s call failed: %s", call.id, call.tool, output)
			}
			if call.grepForm {
				searched, _ := call.args["path"].(string)
				output = nativeGrepForm(output, dir, searched)
			}
			if call.sortLines {
				output = sortedLines(output)
			}
		}
		announce.Content = append(announce.Content, llm.ContentPart{
			Kind:     llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{ID: call.id, Name: call.tool, Arguments: args},
		})
		results.Content = append(results.Content, llm.ContentPart{
			Kind:       llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{ToolCallID: call.id, Name: call.tool, Content: output, IsError: isErr, ToolState: state},
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
		Note:  "One ASSISTANT entry announcing one call per tool, and the TOOL_RESULTS entry answering them, projected through apptranscript. Each core tool ran for real against a temp workspace, recorded as cwd; notes marks the few outputs hand-written because their tool can't run in a test.",
		Cwd:   toolWireCwd,
		Notes: notes,
		Items: toolWireRelocated(t, items, dir),
	}, "the AppWire package and mobile-native tests that read it")
}

// toolWireRelocated rewrites the temp workspace's path, wherever it appears
// in the items (a shell command's cd, grep's and glob's paths, a skill's
// source), to toolWireCwd. macOS resolves its temp dir through /private, so
// both spellings are rewritten.
func toolWireRelocated(t *testing.T, items []appwire.ThreadItem, dir string) []appwire.ThreadItem {
	t.Helper()
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("encode items: %v", err)
	}
	text := string(encoded)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != dir {
		text = strings.ReplaceAll(text, resolved, toolWireCwd)
	}
	text = strings.ReplaceAll(text, dir, toolWireCwd)
	var relocated []appwire.ThreadItem
	if err := json.Unmarshal([]byte(text), &relocated); err != nil {
		t.Fatalf("decode items: %v", err)
	}
	return relocated
}
