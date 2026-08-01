// @vitest-environment node
import { expect, test } from "vitest";
import {
  CONSEQUENCE_RANK,
  consequenceLevel,
  consequenceRank,
  RANKED_TOOL_NAMES,
  type ToolCall,
} from "./consequenceRank";

function call(toolName: string | undefined, args?: Record<string, unknown>): ToolCall {
  return { toolName, argumentsJSON: args === undefined ? undefined : JSON.stringify(args) };
}

// --- completeness: every tool in definitions.go has an explicit rank ------

// Literal transcription of agent/internal/tool/definitions.go's registry (24
// `Name:` string literals - verified with
// `grep -oE 'Name:\s*"[a-z_]+"' agent/internal/tool/definitions.go | sort -u`
// on 2026-07-26). A tool added there later and never taught to this module
// falls through consequenceLevel's default case to "destructive", not
// silently to "read-only" - but THIS test is what makes the drift itself
// visible the moment it happens, rather than waiting for someone to notice a
// rank looks wrong in the UI.
const DEFINITIONS_GO_TOOL_NAMES = [
  "apply_patch",
  "ask_user",
  "delegate",
  "delegate_send",
  "edit_file",
  "find_session_transcripts",
  "glob",
  "grep",
  "job_list",
  "job_read_output",
  "job_status",
  "job_stop",
  "job_watch",
  "list_dir",
  "manage_worktree",
  "read_file",
  "read_session_transcript",
  "read_transcript",
  "shell",
  "task_list",
  "use_skill",
  "web_fetch",
  "web_search",
  "write_file",
].sort();

test("RANKED_TOOL_NAMES matches definitions.go's registry exactly - nothing missing, nothing stale", () => {
  expect([...RANKED_TOOL_NAMES].sort()).toEqual(DEFINITIONS_GO_TOOL_NAMES);
});

test("communicate is absent from the registry on purpose - internal/appprojector's EventToolCallStart handler suppresses it into an agentMessage, so it never becomes a commandExecution item this module could be asked to rank", () => {
  expect(RANKED_TOOL_NAMES).not.toContain("communicate");
});

// --- ordering: a rank, not a boolean ---------------------------------------
//
// Clustering has to pick a winner among several calls in one run, and
// force_dirty needs to outrank an ordinary write in that pick - a yes/no
// flag can't express that, a total order can.

test("CONSEQUENCE_RANK is a strict order: read-only < mutating < destructive", () => {
  expect(CONSEQUENCE_RANK["read-only"]).toBeLessThan(CONSEQUENCE_RANK.mutating);
  expect(CONSEQUENCE_RANK.mutating).toBeLessThan(CONSEQUENCE_RANK.destructive);
});

test("consequenceRank returns the numeric rank matching consequenceLevel", () => {
  expect(consequenceRank(call("read_file"))).toBe(CONSEQUENCE_RANK["read-only"]);
  expect(consequenceRank(call("write_file"))).toBe(CONSEQUENCE_RANK.mutating);
  expect(consequenceRank(call("shell"))).toBe(CONSEQUENCE_RANK.destructive);
});

test("consequenceRank surfaces manage_worktree's force_dirty escalation as the maximum rank, same as shell", () => {
  const dirtyRemove = call("manage_worktree", { operation: "remove", name: "kata-1", force_dirty: true });
  expect(consequenceRank(dirtyRemove)).toBe(CONSEQUENCE_RANK.destructive);
});

// --- plainly read-only: verified against each tool's own description ------

test("read_file ranks read-only - returns file contents, never writes them", () => {
  expect(consequenceLevel(call("read_file"))).toBe("read-only");
});

test("glob ranks read-only - matches existing file paths, no filesystem change", () => {
  expect(consequenceLevel(call("glob"))).toBe("read-only");
});

test("grep ranks read-only - searches file contents, changes nothing", () => {
  expect(consequenceLevel(call("grep"))).toBe("read-only");
});

test("list_dir ranks read-only - lists existing entries, like ls", () => {
  expect(consequenceLevel(call("list_dir"))).toBe("read-only");
});

test("read_transcript ranks read-only - raw evidence reader; its own description says not to even poll it, let alone mutate anything", () => {
  expect(consequenceLevel(call("read_transcript"))).toBe("read-only");
});

test("read_session_transcript ranks read-only - views archived conversation history, never edits it", () => {
  expect(consequenceLevel(call("read_session_transcript"))).toBe("read-only");
});

test("find_session_transcripts ranks read-only - searches archived sessions by content or lineage, returns refs", () => {
  expect(consequenceLevel(call("find_session_transcripts"))).toBe("read-only");
});

test("job_list ranks read-only - lists this session's durable jobs, no state change", () => {
  expect(consequenceLevel(call("job_list"))).toBe("read-only");
});

test("job_status ranks read-only - inspects one job for orientation, no state change", () => {
  expect(consequenceLevel(call("job_status"))).toBe("read-only");
});

test("job_read_output ranks read-only - a retired reader that never consumed or acknowledged output", () => {
  expect(consequenceLevel(call("job_read_output"))).toBe("read-only");
});

test("job_watch ranks read-only across every operation, including create/clear - a standing trigger is this session's own notification plumbing, never a file, job, or branch", () => {
  expect(consequenceLevel(call("job_watch", { operation: "create", source: "self", events: ["communicate"] }))).toBe(
    "read-only",
  );
  expect(consequenceLevel(call("job_watch", { operation: "clear", watch_id: "w1" }))).toBe("read-only");
  expect(consequenceLevel(call("job_watch", { operation: "list" }))).toBe("read-only");
});

test("web_fetch ranks read-only - fetches and caches a URL's content, no local side effect on the user's work", () => {
  expect(consequenceLevel(call("web_fetch"))).toBe("read-only");
});

test("web_search ranks read-only - searches the web, changes nothing local", () => {
  expect(consequenceLevel(call("web_search"))).toBe("read-only");
});

test("use_skill ranks read-only - loads a skill's instructions into the model's own context, no file/job/branch is touched", () => {
  expect(consequenceLevel(call("use_skill"))).toBe("read-only");
});

// --- mutating: ordinary, expected side effects -----------------------------

test("write_file ranks mutating - creates or replaces a file's entire contents", () => {
  expect(consequenceLevel(call("write_file"))).toBe("mutating");
});

test("edit_file ranks mutating - replaces an exact string occurrence in an existing file", () => {
  expect(consequenceLevel(call("edit_file"))).toBe("mutating");
});

test("apply_patch ranks mutating - creates, deletes, or modifies files via a v4a patch", () => {
  expect(consequenceLevel(call("apply_patch"))).toBe("mutating");
});

test("ask_user ranks mutating - not a file/job/branch mutation, but not a passive read either: it ends the turn and blocks the session on a reply", () => {
  expect(consequenceLevel(call("ask_user"))).toBe("mutating");
});

// --- destructive: assume the worst -----------------------------------------

test("shell always ranks destructive regardless of the command - ls and rm -rf are the same tool call, and this module refuses to parse the command to tell them apart", () => {
  expect(consequenceLevel(call("shell", { command: "ls" }))).toBe("destructive");
  expect(consequenceLevel(call("shell", { command: "rm -rf /tmp/whatever" }))).toBe("destructive");
});

test("job_stop always ranks destructive - unconditionally ends a running job; whatever it hadn't finished is abandoned", () => {
  expect(consequenceLevel(call("job_stop", { job_id: "j1" }))).toBe("destructive");
});

test("delegate always ranks destructive - spawns an independently-acting agent with unbounded blast radius, the same reasoning as shell one level removed", () => {
  expect(consequenceLevel(call("delegate", { task: "do something" }))).toBe("destructive");
});

test("delegate_send always ranks destructive - steers a running or idle delegate, equally unbounded, and is clusterable like every other tool here (2026-07-26 ruling: no exemption for owning a subagent module)", () => {
  expect(consequenceLevel(call("delegate_send", { to: "dlg_1", message: "keep going" }))).toBe("destructive");
});

// --- manage_worktree: ranked from parsed arguments, never the tool name ----

test("manage_worktree(list) ranks read-only - only inspects existing worktrees", () => {
  expect(consequenceLevel(call("manage_worktree", { operation: "list" }))).toBe("read-only");
});

test("manage_worktree(switch) ranks read-only - only moves between existing worktrees", () => {
  expect(consequenceLevel(call("manage_worktree", { operation: "switch", name: "kata-1" }))).toBe("read-only");
});

test("manage_worktree(exit) ranks read-only - only returns to the main checkout", () => {
  expect(consequenceLevel(call("manage_worktree", { operation: "exit" }))).toBe("read-only");
});

test("manage_worktree(create) ranks mutating - makes a new worktree, an ordinary and easily-undone change", () => {
  expect(consequenceLevel(call("manage_worktree", { operation: "create", name: "kata-1" }))).toBe("mutating");
});

test("manage_worktree(prune) ranks mutating - by the tool's own description it only removes worktrees with no unmerged work, low-risk cleanup by design", () => {
  expect(consequenceLevel(call("manage_worktree", { operation: "prune" }))).toBe("mutating");
});

test("manage_worktree(remove) without force_dirty ranks mutating - the tool refuses to discard uncommitted changes at all unless the flag is set", () => {
  expect(consequenceLevel(call("manage_worktree", { operation: "remove", name: "kata-1" }))).toBe("mutating");
});

test("manage_worktree(remove) with force_dirty ranks destructive - definitions.go: force_dirty makes it 'proceed even if the worktree has uncommitted changes (they are discarded)'", () => {
  expect(consequenceLevel(call("manage_worktree", { operation: "remove", name: "kata-1", force_dirty: true }))).toBe(
    "destructive",
  );
});

test("manage_worktree(remove) with plain force (no force_dirty) stays mutating - definitions.go is explicit that force 'does NOT discard uncommitted changes', only force_dirty does", () => {
  expect(consequenceLevel(call("manage_worktree", { operation: "remove", name: "kata-1", force: true }))).toBe(
    "mutating",
  );
});

test("manage_worktree(dispose) without force_dirty ranks mutating - same refusal-without-the-flag reasoning as remove", () => {
  expect(consequenceLevel(call("manage_worktree", { operation: "dispose", id: "dlg_1" }))).toBe("mutating");
});

test("manage_worktree(dispose) with force_dirty ranks destructive - discards the delegate lane's uncommitted changes", () => {
  expect(consequenceLevel(call("manage_worktree", { operation: "dispose", id: "dlg_1", force_dirty: true }))).toBe(
    "destructive",
  );
});

test("manage_worktree with an unrecognized operation ranks destructive - a future operation this table has never seen defaults to the safe reading, not to harmless", () => {
  expect(consequenceLevel(call("manage_worktree", { operation: "merge" }))).toBe("destructive");
});

test("manage_worktree with no operation at all ranks destructive - same defensive fallback as an unrecognized operation", () => {
  expect(consequenceLevel(call("manage_worktree", {}))).toBe("destructive");
});

// --- task_list: ranked from parsed arguments, never the tool name ----------

test("task_list(view) ranks read-only - inspects tasks and reasoning effort levels, changes nothing", () => {
  expect(consequenceLevel(call("task_list", { action: "view" }))).toBe("read-only");
});

test("task_list(append) ranks mutating - adds new tasks (empty array here on purpose - ranking reads the action, never the payload)", () => {
  expect(consequenceLevel(call("task_list", { action: "append", tasks: [] }))).toBe("mutating");
});

test("task_list(update) ranks mutating - changes status, notes, dependencies, or reasoning effort", () => {
  expect(consequenceLevel(call("task_list", { action: "update", updates: [] }))).toBe("mutating");
});

test("task_list with an unrecognized action ranks destructive - the schema enum is closed today, but a malformed or future call still defaults to the safe reading", () => {
  expect(consequenceLevel(call("task_list", { action: "bogus" }))).toBe("destructive");
});

// --- unrecognized tool / missing data: never silently harmless ------------

test("an unrecognized tool name ranks destructive - a tool this table has never seen must never silently default to harmless", () => {
  expect(consequenceLevel(call("some_future_tool"))).toBe("destructive");
});

test("a call with no toolName at all ranks destructive - same defensive default", () => {
  expect(consequenceLevel({})).toBe("destructive");
});
