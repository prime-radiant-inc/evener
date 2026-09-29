// @vitest-environment node

import { expect, test } from "vitest";
import { composeStepWords, type StepWords } from "./stepWords";
import { type ToolWireCall, toolWireCwd, toolWireStep } from "./testing/toolWireFixtures";
import { toolStepSummary, toolStepWords } from "./toolSummaries";

// Every recorded call's line as the web drew it before a step's words came in
// parts. Composing the parts must give these back byte for byte, so the web's
// text doesn't move.
const WEB_TEXT: Record<ToolWireCall, string> = {
  call_read_file: "Read agent/tree.go · lines 1-4",
  call_read_file_range: "Read agent/tree.go · lines 2-3",
  call_grep: 'Searched "func settle" in agent (*.go) · 2 hits',
  call_glob: "Matched agent/**/*_test.go · 2 matches",
  call_list_dir: "Listed agent · 3 entries",
  call_list_dir_empty: "Listed empty · 0 entries",
  call_list_dir_page: "Listed agent · 2 entries",
  call_edit_file: "Edited agent/tree.go · +4 -2",
  call_write_file: "Wrote agent/tree_order.go",
  call_apply_patch: "Patched agent/tree.go, agent/tree_drain.go · +2 -0",
  call_shell: "Ran cat agent/tree_order.go",
  call_shell_failed: "Ran test -f agent/missing.go",
  call_shell_windowed: "Ran seq 1 3000",
  call_shell_timeout: "Ran printf 'started\\n'; sleep 10",
  call_read_transcript: "Read transcript 02wMz5Txv5aIxgf9yVdd0N · all 1 turn",
  call_read_transcript_outline: "Read transcript 02wMz5Txv5aIxgf9yVdd0N · outline of 1 turn",
  call_find_sessions: 'Searched sessions for "settle race" · 1 match',
  call_find_sessions_catalog: "Listed recent sessions · 2 sessions",
  call_task_list_add: "☐ Run the race detector again",
  call_task_list_start: "→ Reproduce the settle race",
  call_task_list_done: "→ Order the drain before settle",
  call_task_list_view: "Checked the task list",
  call_worktree_create: "Created worktree settle-fix",
  call_worktree_list: "Listed worktrees · 1 found",
  call_worktree_exit: "Exited worktree at /home/jesse/.local/state/evener/projects/evener/worktrees/evener/settle-fix",
  call_worktree_switch: "Switched to worktree settle-fix",
  call_worktree_switch_again: "Already in worktree settle-fix",
  call_worktree_exit_again:
    "Exited worktree at /home/jesse/.local/state/evener/projects/evener/worktrees/evener/settle-fix",
  call_worktree_remove: "Removed worktree settle-fix",
  call_worktree_prune: "Pruned worktrees · 0 removed, 2 skipped",
  call_worktree_adopt: "Adopted worktree lane",
  call_web_fetch: "Fetched https://example.com/release-notes · 48213 bytes",
  call_web_search: 'Searched the web for "go race detector settle drain" · 2 results',
  call_use_skill: "Activated skill: systematic-debugging",
  call_mcp: "Used github: create issue",
  call_mcp_hyphenated: "Used linear app: list issues",
  call_unknown: "Used compact context",
};

test.each(Object.entries(WEB_TEXT) as [ToolWireCall, string][])(
  "composes %s's words to the web's text",
  (call, text) => {
    const step = toolWireStep(call);
    const ctx = { cwd: toolWireCwd() };
    expect(composeStepWords(toolStepWords(step, ctx))).toBe(text);
    expect(toolStepSummary(step, ctx)).toBe(text);
  },
);

// The target is what the step acted on, which the phone sets in Menlo: a
// path, a command, a query or a ref. Text after it stays in the sentence.
test.each<[ToolWireCall, StepWords]>([
  ["call_read_file", { verb: "Read", target: "agent/tree.go", detail: "lines 1-4" }],
  ["call_grep", { verb: "Searched", target: '"func settle"', after: "in agent (*.go)", detail: "2 hits" }],
  ["call_glob", { verb: "Matched", target: "agent/**/*_test.go", detail: "2 matches" }],
  ["call_list_dir", { verb: "Listed", target: "agent", detail: "3 entries" }],
  ["call_edit_file", { verb: "Edited", target: "agent/tree.go", detail: "+4 -2" }],
  ["call_write_file", { verb: "Wrote", target: "agent/tree_order.go" }],
  ["call_apply_patch", { verb: "Patched", target: "agent/tree.go, agent/tree_drain.go", detail: "+2 -0" }],
  ["call_shell", { verb: "Ran", target: "cat agent/tree_order.go" }],
  ["call_web_fetch", { verb: "Fetched", target: "https://example.com/release-notes", detail: "48213 bytes" }],
  ["call_web_search", { verb: "Searched the web for", target: '"go race detector settle drain"', detail: "2 results" }],
  ["call_use_skill", { verb: "Activated skill:", target: "systematic-debugging" }],
  ["call_read_transcript", { verb: "Read transcript", target: "02wMz5Txv5aIxgf9yVdd0N", detail: "all 1 turn" }],
  ["call_find_sessions", { verb: "Searched sessions for", target: '"settle race"', detail: "1 match" }],
  ["call_find_sessions_catalog", { verb: "Listed recent sessions", detail: "2 sessions" }],
  ["call_worktree_create", { verb: "Created worktree", target: "settle-fix" }],
  ["call_worktree_list", { verb: "Listed worktrees", detail: "1 found" }],
  [
    "call_worktree_exit",
    {
      verb: "Exited worktree at",
      target: "/home/jesse/.local/state/evener/projects/evener/worktrees/evener/settle-fix",
    },
  ],
  ["call_worktree_adopt", { verb: "Adopted worktree", target: "lane" }],
  // A line with nothing machine-shaped in it is its verb alone.
  ["call_task_list_add", { verb: "☐ Run the race detector again" }],
  ["call_task_list_view", { verb: "Checked the task list" }],
  ["call_mcp", { verb: "Used github: create issue" }],
])("words %s in parts", (call, words) => {
  expect(toolStepWords(toolWireStep(call), { cwd: toolWireCwd() })).toEqual(words);
});

test("keeps text after the target out of it, and a step that names nothing is its verb alone", () => {
  const words = (toolName: string, args: Record<string, unknown>, output?: string) =>
    toolStepWords({ toolName, argumentsJSON: JSON.stringify(args), output });
  expect(words("manage_worktree", { operation: "create", name: "settle-fix", base_ref: "main" })).toEqual({
    verb: "Created worktree",
    target: "settle-fix",
    after: "(from main)",
  });
  expect(words("list_dir", { path: "agent", pattern: "*.go" })).toEqual({
    verb: "Listed",
    target: "agent",
    after: "(*.go)",
  });
  expect(words("manage_worktree", { operation: "dispose", id: "dlg_1", force_dirty: true })).toEqual({
    verb: "Disposed",
    target: "dlg_1",
    detail: "discarded uncommitted changes",
  });
  expect(words("find_session_transcripts", { children_of: "local:abc" })).toEqual({
    verb: "Searched sessions spawned by",
    target: "local:abc",
  });
  expect(words("read_transcript", { transcript_ref: "job:job_x" })).toEqual({ verb: "Read job log", target: "job_x" });
  expect(words("read_transcript", {})).toEqual({ verb: "Read this session's transcript" });
  expect(words("read_file", {})).toEqual({ verb: "Read a file" });
  expect(words("manage_worktree", { operation: "dispose" })).toEqual({ verb: "Disposed a worktree" });
});

test("composes words the way the web draws them", () => {
  expect(composeStepWords({ verb: "Read a file" })).toBe("Read a file");
  expect(composeStepWords({ verb: "Read", target: "a.go", detail: "lines 1-4" })).toBe("Read a.go · lines 1-4");
  expect(composeStepWords({ verb: "Searched", target: '"x"', after: "in agent", detail: "2 hits" })).toBe(
    'Searched "x" in agent · 2 hits',
  );
});
