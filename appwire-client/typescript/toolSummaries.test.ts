// @vitest-environment node

import { expect, test } from "vitest";
import { type ToolWireCall, toolWireCwd, toolWireStep } from "./testing/toolWireFixtures";
import { mcpToolParts, toolFamily, toolStepProgress, toolStepSummary, words } from "./toolSummaries";

// Every case reads a settled step the daemon actually sends
// (agent/testdata/toolwire), merged as a client holds it. The core tools'
// words are the web's, lifted unchanged.
test.each<[ToolWireCall, string]>([
  ["call_read_file", "Read agent/tree.go · lines 1-3"],
  ["call_read_file_range", "Read agent/tree.go · lines 120-159"],
  ["call_grep", 'Searched "func settle" in agent (*.go) · 2 hits'],
  ["call_glob", "Matched agent/**/*_test.go · 3 matches"],
  ["call_list_dir", "Listed agent/internal · 4 entries"],
  ["call_edit_file", "Edited agent/tree.go · +3 -2"],
  ["call_write_file", "Wrote agent/tree_order.go"],
  ["call_apply_patch", "Patched agent/tree.go, agent/tree_order.go · +3 -1"],
  ["call_shell", "Ran go test ./agent/..."],
  ["call_shell_failed", "Ran go vet ./agent/..."],
  ["call_web_fetch", "Fetched https://example.com/release-notes · 48213 bytes"],
  ["call_web_search", 'Searched the web for "go race detector settle drain" · 2 results'],
  ["call_use_skill", "Activated skill: systematic-debugging"],
])("says %s as %s", (call, summary) => {
  expect(toolStepSummary(toolWireStep(call), { cwd: toolWireCwd() })).toBe(summary);
});

// A tool no summary covers, an MCP tool among them, never shows its raw name.
test.each<[ToolWireCall, string]>([
  ["call_mcp", "Used github: create issue"],
  ["call_mcp_hyphenated", "Used linear app: list issues"],
  ["call_unknown", "Used compact context"],
])("says %s, which no summary covers, as %s", (call, summary) => {
  expect(toolStepSummary(toolWireStep(call))).toBe(summary);
});

test("keeps a shell command's cd when the session is somewhere else", () => {
  expect(toolStepSummary(toolWireStep("call_shell"), { cwd: "/elsewhere" })).toBe(
    "Ran cd /home/jesse/git/evener && go test ./agent/...",
  );
});

test("sorts each tool into the family a run's summary counts it under", () => {
  expect(toolFamily("read_file")).toBe("read");
  for (const name of ["edit_file", "write_file", "apply_patch"]) expect(toolFamily(name)).toBe("edit");
  for (const name of ["grep", "grep_files", "grep_search", "glob", "list_dir", "list_directory"])
    expect(toolFamily(name)).toBe("search");
  expect(toolFamily("web_fetch")).toBe("fetch");
  expect(toolFamily("web_search")).toBe("webSearch");
  for (const name of ["shell", "exec_command", "run_shell_command"]) expect(toolFamily(name)).toBe("shell");
  expect(toolFamily("use_skill")).toBe("skill");
  expect(toolFamily("github__create_issue")).toBe("mcp");
  expect(toolFamily("compact_context")).toBe("tool");
});

test("reads an MCP tool's server and tool in words", () => {
  expect(mcpToolParts("github__create_issue")).toEqual({ server: "github", tool: "create issue" });
  expect(mcpToolParts("linear_app__list_issues")).toEqual({ server: "linear app", tool: "list issues" });
  expect(mcpToolParts("compact_context")).toBeUndefined();
  expect(mcpToolParts("__odd")).toBeUndefined();
});

// A call whose arguments are missing, empty or cut off still reads as a verb,
// and a step whose output hasn't arrived drops what the output would count.
test.each<[string, string]>([
  ["read_file", "Read a file"],
  ["grep", "Searched files"],
  ["glob", "Searched files"],
  ["list_dir", "Listed files"],
  ["edit_file", "Edited a file"],
  ["write_file", "Wrote a file"],
  ["apply_patch", "Patched files"],
  ["shell", "Ran a command"],
  ["web_fetch", "Fetched a page"],
  ["web_search", "Searched the web"],
  ["use_skill", "Activated a skill"],
])("says a %s with no arguments as %s", (toolName, summary) => {
  for (const argumentsJSON of ["", "{", "{}", undefined]) {
    expect(toolStepSummary({ toolName, argumentsJSON, output: "" })).toBe(summary);
  }
});

test.each<[string, Record<string, unknown>, string]>([
  ["read_file", { file_path: "a.go" }, "Read a.go"],
  ["read_file", { file_path: "a.go", offset: 5, limit: 10 }, "Read a.go · lines 5-14"],
  ["grep", { pattern: "x", path: "agent" }, 'Searched "x" in agent'],
  ["glob", { pattern: "*.go" }, "Matched *.go"],
  ["list_dir", { path: "agent" }, "Listed agent"],
  ["web_fetch", { url: "https://example.com" }, "Fetched https://example.com"],
  ["web_search", { query: "evener" }, 'Searched the web for "evener"'],
])("says a %s whose output hasn't arrived without its counts", (toolName, args, summary) => {
  expect(toolStepSummary({ toolName, argumentsJSON: JSON.stringify(args), output: "" })).toBe(summary);
});

// The tray says what a running step is doing, in the same words' live form.
test.each<[string, Record<string, unknown> | undefined, string]>([
  ["read_file", { file_path: "agent/tree.go" }, "Reading agent/tree.go"],
  ["read_file", undefined, "Reading a file"],
  ["grep", { pattern: "func settle", path: "agent" }, 'Searching "func settle" in agent'],
  ["grep", undefined, "Searching files"],
  ["glob", { pattern: "*.go" }, "Matching *.go"],
  ["list_dir", { path: "agent" }, "Listing agent"],
  ["edit_file", { file_path: "a.go" }, "Editing a.go"],
  ["write_file", { file_path: "a.go" }, "Writing a.go"],
  ["write_file", undefined, "Writing a file"],
  ["apply_patch", { patch: "*** Update File: a.go\n" }, "Patching a.go"],
  ["apply_patch", undefined, "Patching files"],
  ["shell", { command: "go test ./...\ngo vet ./..." }, "Running go test ./..."],
  ["shell", undefined, "Running a command"],
  ["web_fetch", { url: "https://example.com" }, "Fetching https://example.com"],
  ["web_search", { query: "evener" }, 'Searching the web for "evener"'],
  ["use_skill", { skill_name: "brainstorming" }, "Activating skill: brainstorming"],
  ["github__create_issue", {}, "Using github: create issue"],
  ["compact_context", {}, "Using compact context"],
])("says a running %s as %s", (toolName, args, progress) => {
  expect(toolStepProgress({ toolName, argumentsJSON: args ? JSON.stringify(args) : undefined })).toBe(progress);
});

test("strips the session's own directory from a running command", () => {
  expect(
    toolStepProgress(
      { toolName: "shell", argumentsJSON: JSON.stringify({ command: "cd /repo && make test" }) },
      { cwd: "/repo" },
    ),
  ).toBe("Running make test");
});

test("reads a tool name in words", () => {
  expect(words("compact_context")).toBe("compact context");
});
