import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { toolRendererFor } from "../toolRenderers";
import "./worktreeTool";
import type { ItemModel } from "../../../../protocol/model";

afterEach(cleanup);

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

// --- manage_worktree: create --------------------------------------------

test("create: summary leads with the operation and the new worktree's name", () => {
  const d = toolRendererFor("manage_worktree");
  const args = JSON.stringify({ operation: "create", name: "kata-8m70" });
  expect(d.summary(item({ toolName: "manage_worktree", argumentsJSON: args }))).toBe("Created worktree kata-8m70");
});

test("create: a base_ref arg is parenthesized", () => {
  const d = toolRendererFor("manage_worktree");
  const args = JSON.stringify({ operation: "create", name: "kata-8m70", base_ref: "main" });
  expect(d.summary(item({ toolName: "manage_worktree", argumentsJSON: args }))).toBe(
    "Created worktree kata-8m70 (from main)",
  );
});

// --- list -----------------------------------------------------------------

test("list: with no settled output yet, summary names the operation with no count", () => {
  const d = toolRendererFor("manage_worktree");
  const args = JSON.stringify({ operation: "list" });
  expect(d.summary(item({ toolName: "manage_worktree", argumentsJSON: args }))).toBe("Listed worktrees");
});

test("list: a settled result reports how many worktrees were found", () => {
  const d = toolRendererFor("manage_worktree");
  const args = JSON.stringify({ operation: "list" });
  const output = JSON.stringify({ status: "listed", entries: [{ name: "a" }, { name: "b" }, { name: "c" }] });
  expect(d.summary(item({ toolName: "manage_worktree", argumentsJSON: args, output }))).toBe(
    "Listed worktrees · 3 found",
  );
});

// --- switch -----------------------------------------------------------------

test("switch: summary names the target from `name`", () => {
  const d = toolRendererFor("manage_worktree");
  const args = JSON.stringify({ operation: "switch", name: "kata-8m70" });
  expect(d.summary(item({ toolName: "manage_worktree", argumentsJSON: args }))).toBe("Switched to worktree kata-8m70");
});

test("switch: falls back to `path` when `name` is absent", () => {
  const d = toolRendererFor("manage_worktree");
  const args = JSON.stringify({ operation: "switch", path: "/repo/.worktrees/kata-8m70" });
  expect(d.summary(item({ toolName: "manage_worktree", argumentsJSON: args }))).toBe(
    "Switched to worktree /repo/.worktrees/kata-8m70",
  );
});

test("switch: a settled `unchanged` result reads as a no-op, not a switch", () => {
  const d = toolRendererFor("manage_worktree");
  const args = JSON.stringify({ operation: "switch", name: "kata-8m70" });
  const output = JSON.stringify({ status: "unchanged", path: "/repo/.worktrees/kata-8m70", branch: "kata-8m70" });
  expect(d.summary(item({ toolName: "manage_worktree", argumentsJSON: args, output }))).toBe(
    "Already in worktree kata-8m70",
  );
});

// --- exit -----------------------------------------------------------------

test("exit: with no settled output yet, summary names the bare operation (exit takes no args of its own)", () => {
  const d = toolRendererFor("manage_worktree");
  const args = JSON.stringify({ operation: "exit" });
  expect(d.summary(item({ toolName: "manage_worktree", argumentsJSON: args }))).toBe("Exited worktree");
});

test("exit: a settled result names the worktree left, read from output.left_path", () => {
  const d = toolRendererFor("manage_worktree");
  const args = JSON.stringify({ operation: "exit" });
  const output = JSON.stringify({
    status: "exited",
    restored_root: "/repo",
    left_path: "/repo/.worktrees/kata-8m70",
  });
  expect(d.summary(item({ toolName: "manage_worktree", argumentsJSON: args, output }))).toBe(
    "Exited worktree at /repo/.worktrees/kata-8m70",
  );
});

// --- remove: the kata's own motivating case --------------------------------

test("remove: summary leads with the destructive verb and the target name", () => {
  const d = toolRendererFor("manage_worktree");
  const args = JSON.stringify({ operation: "remove", name: "kata-8m70" });
  expect(d.summary(item({ toolName: "manage_worktree", argumentsJSON: args }))).toBe("Removed worktree kata-8m70");
});

test("remove: force_dirty is called out on the row, not hidden behind an expand", () => {
  const d = toolRendererFor("manage_worktree");
  const args = JSON.stringify({ operation: "remove", name: "kata-8m70", force_dirty: true });
  expect(d.summary(item({ toolName: "manage_worktree", argumentsJSON: args }))).toBe(
    "Removed worktree kata-8m70 · discarded uncommitted changes",
  );
});

test("remove: a plain `force` (merge-safety override, not a dirty-tree override) earns no dirty-discard claim", () => {
  const d = toolRendererFor("manage_worktree");
  const args = JSON.stringify({ operation: "remove", name: "kata-8m70", force: true });
  expect(d.summary(item({ toolName: "manage_worktree", argumentsJSON: args }))).toBe("Removed worktree kata-8m70");
});

test("remove: a read-only list and a force_dirty remove of the same name read unmistakably differently", () => {
  const d = toolRendererFor("manage_worktree");
  const listSummary = d.summary(
    item({ toolName: "manage_worktree", argumentsJSON: JSON.stringify({ operation: "list" }) }),
  );
  const removeSummary = d.summary(
    item({
      toolName: "manage_worktree",
      argumentsJSON: JSON.stringify({ operation: "remove", name: "kata-8m70", force_dirty: true }),
    }),
  );
  expect(listSummary).not.toBe(removeSummary);
  expect(removeSummary).toContain("discarded uncommitted changes");
});

// --- prune -----------------------------------------------------------------

test("prune: with no settled output yet, summary names the bare operation", () => {
  const d = toolRendererFor("manage_worktree");
  const args = JSON.stringify({ operation: "prune" });
  expect(d.summary(item({ toolName: "manage_worktree", argumentsJSON: args }))).toBe("Pruned worktrees");
});

test("prune: a settled result reports removed/skipped counts", () => {
  const d = toolRendererFor("manage_worktree");
  const args = JSON.stringify({ operation: "prune" });
  const output = JSON.stringify({
    status: "pruned",
    removed: [{ name: "a" }, { name: "b" }],
    skipped: [{ name: "c" }],
    registry_pruned: true,
  });
  expect(d.summary(item({ toolName: "manage_worktree", argumentsJSON: args, output }))).toBe(
    "Pruned worktrees · 2 removed, 1 skipped",
  );
});

// --- dispose -----------------------------------------------------------------

test("dispose: summary leads with the destructive verb and the delegate id", () => {
  const d = toolRendererFor("manage_worktree");
  const args = JSON.stringify({ operation: "dispose", id: "dlg_abc123" });
  expect(d.summary(item({ toolName: "manage_worktree", argumentsJSON: args }))).toBe("Disposed dlg_abc123");
});

test("dispose: force_dirty is called out on the row", () => {
  const d = toolRendererFor("manage_worktree");
  const args = JSON.stringify({ operation: "dispose", id: "dlg_abc123", force_dirty: true });
  expect(d.summary(item({ toolName: "manage_worktree", argumentsJSON: args }))).toBe(
    "Disposed dlg_abc123 · discarded uncommitted changes",
  );
});

test("dispose: a settled `already_disposed` result reads as the idempotent no-op it is", () => {
  const d = toolRendererFor("manage_worktree");
  const args = JSON.stringify({ operation: "dispose", id: "dlg_abc123" });
  const output = JSON.stringify({ status: "already_disposed", id: "dlg_abc123" });
  expect(d.summary(item({ toolName: "manage_worktree", argumentsJSON: args, output }))).toBe(
    "Already disposed dlg_abc123",
  );
});

test("dispose: an already-disposed result never claims a dirty-discard even if force_dirty was passed (nothing was torn down to discard)", () => {
  const d = toolRendererFor("manage_worktree");
  const args = JSON.stringify({ operation: "dispose", id: "dlg_abc123", force_dirty: true });
  const output = JSON.stringify({ status: "already_disposed", id: "dlg_abc123" });
  expect(d.summary(item({ toolName: "manage_worktree", argumentsJSON: args, output }))).toBe(
    "Already disposed dlg_abc123",
  );
});

// --- defensive fallback ------------------------------------------------

test("an unrecognized operation still renders a non-crashing, tool-name-prefixed summary", () => {
  const d = toolRendererFor("manage_worktree");
  const args = JSON.stringify({ operation: "reticulate" });
  expect(d.summary(item({ toolName: "manage_worktree", argumentsJSON: args }))).toBe("manage_worktree: reticulate");
});

// --- shared body: manage_worktree's own output is real JSON (its Exec
// returns a map, so the registry's toolValueToString json.MarshalIndents it -
// see this file's own header), so a bare head-clip is enough; no per-op body
// is worth building for a kata scoped to the summary row. --------------------

test("body head-clips the raw JSON output", () => {
  const d = toolRendererFor("manage_worktree");
  const Body = d.body!;
  render(<Body item={item({ toolName: "manage_worktree", output: '{"status":"removed"}' })} live={false} />);
  expect(screen.getByText('{"status":"removed"}')).toBeTruthy();
});

// --- find_session_transcripts -----------------------------------------

test("find_session_transcripts: a bare catalog call (no query/children_of) is a listing, not a search", () => {
  const d = toolRendererFor("find_session_transcripts");
  expect(d.summary(item({ toolName: "find_session_transcripts", argumentsJSON: "{}" }))).toBe("Listed recent sessions");
});

test("find_session_transcripts: a query call names what was searched for", () => {
  const d = toolRendererFor("find_session_transcripts");
  const args = JSON.stringify({ query: "parser regression" });
  expect(d.summary(item({ toolName: "find_session_transcripts", argumentsJSON: args }))).toBe(
    'Searched sessions for "parser regression"',
  );
});

test("find_session_transcripts: children_of takes precedence over query in the summary, matching the tool's own precedence", () => {
  const d = toolRendererFor("find_session_transcripts");
  const args = JSON.stringify({ query: "ignored", children_of: "local:01K835WYZ0X3XZ5CJ54RVE2QP4" });
  expect(d.summary(item({ toolName: "find_session_transcripts", argumentsJSON: args }))).toBe(
    "Searched sessions spawned by local:01K835WYZ0X3XZ5CJ54RVE2QP4",
  );
});

test("find_session_transcripts: a settled result appends the match count parsed off the tool's own text footer", () => {
  const d = toolRendererFor("find_session_transcripts");
  const args = JSON.stringify({ query: "parser regression" });
  const output =
    "1. local:01K835WYZ0X3XZ5CJ54RVE2QP4 — Fixed the parser regression\n" +
    "   root · ~15 turns · updated 2026-07-20 14:32\n\n" +
    "1 match (scope: current_project)";
  expect(d.summary(item({ toolName: "find_session_transcripts", argumentsJSON: args, output }))).toBe(
    'Searched sessions for "parser regression" · 1 matches',
  );
});

test("find_session_transcripts: a zero-match settled result reports 0, not a missing count", () => {
  const d = toolRendererFor("find_session_transcripts");
  const args = JSON.stringify({ query: "nonexistent" });
  const output = "No matching sessions (scope: current_project).";
  expect(d.summary(item({ toolName: "find_session_transcripts", argumentsJSON: args, output }))).toBe(
    'Searched sessions for "nonexistent" · 0 matches',
  );
});

test("find_session_transcripts: a settled catalog result uses its own noun (sessions, not matches)", () => {
  const d = toolRendererFor("find_session_transcripts");
  const output =
    "1. local:01K835WYZ0X3XZ5CJ54RVE2QP4 — Some session\n" +
    "   root · ~5 turns · updated 2026-07-20 14:32\n\n" +
    "1 match (scope: current_project)";
  expect(d.summary(item({ toolName: "find_session_transcripts", argumentsJSON: "{}", output }))).toBe(
    "Listed recent sessions · 1 sessions",
  );
});

test("find_session_transcripts: body head-clips the raw text output", () => {
  const d = toolRendererFor("find_session_transcripts");
  const Body = d.body!;
  render(
    <Body
      item={item({ toolName: "find_session_transcripts", output: "No matching sessions (scope: current_project)." })}
      live={false}
    />,
  );
  expect(screen.getByText("No matching sessions (scope: current_project).")).toBeTruthy();
});

// A descriptor that is never imported by tools/index.ts is never registered in
// the running app, however green its own tests are - this file imports the
// module directly, so every assertion above would keep passing with the
// barrel entry missing and both tools still rendering as a bare name. Assert
// the wiring, not just the behaviour.
test("both descriptors are reachable through the tools barrel, not only by direct import", async () => {
  await import(".");
  expect(toolRendererFor("manage_worktree").summary(item({ toolName: "manage_worktree" }))).not.toBe("manage_worktree");
  expect(toolRendererFor("find_session_transcripts").summary(item({ toolName: "find_session_transcripts" }))).not.toBe(
    "find_session_transcripts",
  );
});
