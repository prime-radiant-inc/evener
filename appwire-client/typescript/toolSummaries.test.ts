// @vitest-environment node

import { expect, test } from "vitest";
import { type ToolWireCall, toolWireCwd, toolWireStep } from "./testing/toolWireFixtures";
import { mcpToolParts, toolFamily, toolStepSummary } from "./toolSummaries";

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
