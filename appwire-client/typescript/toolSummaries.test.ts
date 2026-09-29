// @vitest-environment node

import { expect, test } from "vitest";
import { type ToolWireCall, toolWireCwd, toolWireStep } from "./testing/toolWireFixtures";
import { mcpToolParts, toolFamily, toolStepProgress, toolStepSummary, words } from "./toolSummaries";

// Every case reads a settled step the daemon actually sends
// (agent/testdata/toolwire), merged as a client holds it. The core tools'
// words are the web's, lifted unchanged.
test.each<[ToolWireCall, string]>([
  // The tool numbers a file's final empty line too.
  ["call_read_file", "Read agent/tree.go · lines 1-4"],
  ["call_read_file_range", "Read agent/tree.go · lines 2-3"],
  ["call_grep", 'Searched "func settle" in agent (*.go) · 2 hits'],
  ["call_glob", "Matched agent/**/*_test.go · 2 matches"],
  // The count list_dir states on its last line, not its line count.
  ["call_list_dir", "Listed agent · 3 entries"],
  ["call_list_dir_empty", "Listed empty · 0 entries"],
  ["call_list_dir_page", "Listed agent · 2 entries"],
  ["call_edit_file", "Edited agent/tree.go · +4 -2"],
  ["call_write_file", "Wrote agent/tree_order.go"],
  ["call_apply_patch", "Patched agent/tree.go, agent/tree_drain.go · +2 -0"],
  ["call_shell", "Ran cat agent/tree_order.go"],
  ["call_shell_failed", "Ran test -f agent/missing.go"],
  ["call_web_fetch", "Fetched https://example.com/release-notes · 48213 bytes"],
  ["call_web_search", 'Searched the web for "go race detector settle drain" · 2 results'],
  ["call_use_skill", "Activated skill: systematic-debugging"],
  // A task_list update names its latest touch, read against the task list
  // the call returned: the daemon's own start of the next task is the latest
  // when a completion set one off.
  ["call_task_list_add", "☐ Run the race detector again"],
  ["call_task_list_start", "→ Reproduce the settle race"],
  ["call_task_list_done", "→ Order the drain before settle"],
  ["call_task_list_view", "Checked the task list"],
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

// An update that changed no task's status (a note, a reopen) has no touch
// to name.
test("says a task_list update that changed no status as updating the list", () => {
  const noted = {
    toolName: "task_list",
    argumentsJSON: JSON.stringify({ update: [{ id: 1, notes: "Still flaky." }] }),
  };
  expect(toolStepSummary(noted)).toBe("Updated the task list");
});

// The historical action form: "append" with tasks, "update" with updates.
test("says a historical task_list action changed the list only when it carried a change", () => {
  const called = (args: Record<string, unknown>) =>
    toolStepSummary({ toolName: "task_list", argumentsJSON: JSON.stringify(args) });
  expect(called({ action: "append", tasks: [] })).toBe("Checked the task list");
  expect(called({ action: "update", updates: [] })).toBe("Checked the task list");
  expect(called({ action: "view" })).toBe("Checked the task list");
  expect(called({ action: "rename" })).toBe("Checked the task list");
  expect(called({ action: "update", updates: [{ id: 1, notes: "Still flaky." }] })).toBe("Updated the task list");
});

test("keeps a shell command's cd when the session is somewhere else", () => {
  expect(toolStepSummary(toolWireStep("call_shell"), { cwd: "/elsewhere" })).toBe(
    "Ran cd /home/jesse/git/evener && cat agent/tree_order.go",
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
  expect(toolFamily("task_list")).toBe("tasks");
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
  ["task_list", { update: [{ id: 1, status: "done" }] }, "Updating the task list"],
  ["task_list", {}, "Checking the task list"],
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

test("reads a tool name in words, hyphens and underscores alike", () => {
  expect(words("compact_context")).toBe("compact context");
  expect(words("foo-bar_baz")).toBe("foo bar baz");
  expect(toolStepSummary({ toolName: "foo-bar" })).toBe("Used foo bar");
});

test("says the count list_dir states, one entry or many", () => {
  const listed = (output: string) =>
    toolStepSummary({ toolName: "list_dir", argumentsJSON: JSON.stringify({ path: "a" }), output });
  expect(listed("b.go\t3\n\n1 entries")).toBe("Listed a · 1 entry");
  expect(listed("b.go\t3\nc.go\t4\n\n2 entries")).toBe("Listed a · 2 entries");
  // An empty directory is the count alone.
  expect(listed("0 entries")).toBe("Listed a · 0 entries");
  // A page of a longer listing counts the entries it returned.
  expect(listed("b.go\t3\nc.go\t4\n\n2 of 5 entries (offset 0) — more with list_dir(offset=2)")).toBe(
    "Listed a · 2 entries",
  );
  expect(listed("d.go\t3\n\n1 of 5 entries (offset 4)")).toBe("Listed a · 1 entry");
  // An entry named like a count is an entry: only the footer, after the blank
  // line (or alone, for an empty directory), is the count.
  expect(listed("2 entries.md\t9\nb.go\t3\nc.go\t4\n\n3 entries")).toBe("Listed a · 3 entries");
  expect(listed("0 entries.md\t9\n\n1 of 4 entries (offset 3)")).toBe("Listed a · 1 entry");
  // A trailing newline after the footer still leaves it the last line.
  expect(listed("b.go\t3\nc.go\t4\n\n2 entries\n")).toBe("Listed a · 2 entries");
  // An entry named like a count is an entry even as the last line of an
  // output without the footer, which counts its lines.
  expect(listed("2 entries.md\t9\n")).toBe("Listed a · 1 entry");
  expect(listed("b.go\t3\nc.go\t4\n2 entries.md\t9")).toBe("Listed a · 3 entries");
  // The registry's repetition nudge, appended after a blank line, is not the
  // listing's last line.
  expect(
    listed(
      "b.go\t3\nc.go\t4\nd.go\t5\n\n3 entries\n\nYou have now made this same call and received the identical result 2 times in a row.",
    ),
  ).toBe("Listed a · 3 entries");
  // Without a footer, a nudge's lines aren't counted as entries.
  expect(
    listed("a.go\nb.go\n\nYou have now made this same call and received the identical result 2 times in a row."),
  ).toBe("Listed a · 2 entries");
  // An output without the count counts its lines.
  expect(listed("b.go\nc.go\n")).toBe("Listed a · 2 entries");
});
