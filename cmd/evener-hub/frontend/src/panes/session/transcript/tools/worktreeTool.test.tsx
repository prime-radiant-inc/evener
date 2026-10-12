import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { toolRendererFor } from "../toolRenderers";
import "./worktreeTool";
import type { ItemModel } from "@evener/appwire-client";

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

// One manage_worktree call's summary, from its arguments and settled output.
function worktreeSummary(args: Record<string, unknown>, output?: string): string {
  return toolRendererFor("manage_worktree").summary(
    item({ toolName: "manage_worktree", argumentsJSON: JSON.stringify(args), output }),
  );
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
  const summary = worktreeSummary({ operation: "list" });
  expect(summary).not.toMatch(/\d/);
  expect(worktreeSummary({ operation: "list" }, JSON.stringify({ status: "listed", entries: [] }))).not.toBe(summary);
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
  const summary = worktreeSummary({ operation: "exit" });
  expect(summary).not.toContain("/");
  const settled = JSON.stringify({ status: "exited", left_path: "/repo/.worktrees/kata-8m70" });
  expect(worktreeSummary({ operation: "exit" }, settled)).not.toBe(summary);
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
  const plain = worktreeSummary({ operation: "remove", name: "kata-8m70" });
  const dirty = worktreeSummary({ operation: "remove", name: "kata-8m70", force_dirty: true });
  expect(dirty.startsWith(plain)).toBe(true);
  expect(dirty.length).toBeGreaterThan(plain.length);
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
  expect(removeSummary).not.toBe(worktreeSummary({ operation: "remove", name: "kata-8m70" }));
});

// --- prune -----------------------------------------------------------------

test("prune: with no settled output yet, summary names the bare operation", () => {
  const summary = worktreeSummary({ operation: "prune" });
  expect(summary).not.toMatch(/\d/);
  const settled = JSON.stringify({ status: "pruned", removed: [], skipped: [] });
  expect(worktreeSummary({ operation: "prune" }, settled)).not.toBe(summary);
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
  const plain = worktreeSummary({ operation: "dispose", id: "dlg_abc123" });
  const dirty = worktreeSummary({ operation: "dispose", id: "dlg_abc123", force_dirty: true });
  expect(dirty.startsWith(plain)).toBe(true);
  expect(dirty.length).toBeGreaterThan(plain.length);
});

test("dispose: a settled `already_disposed` result reads as the idempotent no-op it is", () => {
  const output = JSON.stringify({ status: "already_disposed", id: "dlg_abc123" });
  const noOp = worktreeSummary({ operation: "dispose", id: "dlg_abc123" }, output);
  expect(noOp.endsWith(" dlg_abc123")).toBe(true);
  expect(noOp).not.toBe(worktreeSummary({ operation: "dispose", id: "dlg_abc123" }));
});

test("dispose: an already-disposed result never claims a dirty-discard even if force_dirty was passed (nothing was torn down to discard)", () => {
  const d = toolRendererFor("manage_worktree");
  const args = JSON.stringify({ operation: "dispose", id: "dlg_abc123", force_dirty: true });
  const output = JSON.stringify({ status: "already_disposed", id: "dlg_abc123" });
  expect(d.summary(item({ toolName: "manage_worktree", argumentsJSON: args, output }))).toBe(
    worktreeSummary({ operation: "dispose", id: "dlg_abc123" }, output),
  );
});

// --- defensive fallback ------------------------------------------------

test("an unrecognized operation still says which one it was, in words, never the raw tool name", () => {
  const d = toolRendererFor("manage_worktree");
  const args = JSON.stringify({ operation: "reticulate" });
  expect(d.summary(item({ toolName: "manage_worktree", argumentsJSON: args }))).toBe(
    "Used manage worktree: reticulate",
  );
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
  const listing = d.summary(item({ toolName: "find_session_transcripts", argumentsJSON: "{}" }));
  const search = d.summary(item({ toolName: "find_session_transcripts", argumentsJSON: '{"query":"x"}' }));
  expect(listing).not.toContain('"');
  expect(listing.split(" ")[0]).not.toBe(search.split(" ")[0]);
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
    'Searched sessions for "parser regression" · 1 match',
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
    "Listed recent sessions · 1 session",
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

test("find_session_transcripts: body shows the pretty-printed query above the output", () => {
  const d = toolRendererFor("find_session_transcripts");
  const Body = d.body!;
  const args = JSON.stringify({ query: "flaky test" });
  const { container } = render(
    <Body
      item={item({
        toolName: "find_session_transcripts",
        argumentsJSON: args,
        output: "1 match (scope: current_project).",
      })}
      live={false}
    />,
  );
  const section = screen.getByRole("region", { name: "Tool call arguments" });
  expect(section.textContent).toContain('"query": "flaky test"');
  const argsIndex = container.textContent!.indexOf('"query"');
  const outputIndex = container.textContent!.indexOf("1 match (scope: current_project).");
  expect(argsIndex).toBeGreaterThanOrEqual(0);
  expect(argsIndex).toBeLessThan(outputIndex);
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
