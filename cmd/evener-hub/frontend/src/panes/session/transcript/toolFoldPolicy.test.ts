import { expect, test } from "vitest";
// Registers every tool descriptor the same way the session pane does.
import "./TurnBlock";
import { toolRendererFor } from "./toolRenderers";

// The fold policy table (toolRenderers.ts's `fold`): folding is opt-in, so a
// read-only tool that forgets to opt in silently stops folding (roborev on
// PR #947 caught find_session_transcripts) and a mutating tool that opts
// into "quiet" would hide its effect inside a run. Pin every registered
// tool's value here; a new tool has to take a position.
const QUIET = [
  "read_file",
  "grep",
  "grep_files",
  "grep_search",
  "list_dir",
  "list_directory",
  "glob",
  "web_fetch",
  "web_search",
  "read_transcript",
  "read_session_transcript",
  "find_session_transcripts",
];
const CONSEQUENTIAL = [
  "edit_file",
  "write_file",
  "apply_patch",
  "shell",
  "exec_command",
  "run_shell_command",
  "manage_worktree",
];
const NEVER = [
  "delegate",
  "ask_user",
  "task_list",
  "use_skill",
  "job_status",
  "job_read_output",
  "job_list",
  "job_stop",
  "delegate_send",
  "job_send_message",
  "job_anything_else",
];

test.each(QUIET)("%s is a read-only step: fold quiet", (name) => {
  expect(toolRendererFor(name).fold).toBe("quiet");
});

test.each(CONSEQUENTIAL)("%s mutates: fold consequential", (name) => {
  expect(toolRendererFor(name).fold).toBe("consequential");
});

test.each(NEVER)("%s is a card the reader must see: fold never", (name) => {
  expect(toolRendererFor(name).fold).toBe("never");
});

test("an unregistered (MCP) tool has no fold policy, which toolRuns treats as never", () => {
  expect(toolRendererFor("mcp__some_server__deploy").fold).toBeUndefined();
});
