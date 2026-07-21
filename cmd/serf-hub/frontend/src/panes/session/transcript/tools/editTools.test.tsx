import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { toolRendererFor } from "../toolRenderers";
import "./editTools";
import type { ItemModel } from "../../../../protocol/model";

afterEach(cleanup);

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

// --- edit_file --------------------------------------------------------
// Ground truth: agent/execenv/local.go's EditFile returns a plain
// confirmation string ("edited <path>: N replacement(s)"), not a diff - so
// the diff shown here is SYNTHESIZED from the old_string/new_string INPUT
// args, exactly as the legacy editDiffText did (it never trusted raw
// output for this either).

test("edit_file: summary shows the target and a +added/-removed line stat", () => {
  const d = toolRendererFor("edit_file");
  const args = JSON.stringify({ file_path: "a.ts", old_string: "one\ntwo", new_string: "uno" });
  expect(d.summary(item({ toolName: "edit_file", argumentsJSON: args }))).toBe("Edited a.ts · +1 -2");
});

test("edit_file: an edit with identical line counts still reports both sides", () => {
  const d = toolRendererFor("edit_file");
  const args = JSON.stringify({ file_path: "a.ts", old_string: "x", new_string: "y" });
  expect(d.summary(item({ toolName: "edit_file", argumentsJSON: args }))).toBe("Edited a.ts · +1 -1");
});

test("edit_file: an empty old_string/new_string still counts as one blank line per side (not a fabricated `ok`)", () => {
  // "".split("\n") is ["" ] (one element, not zero) - a blank line is real
  // diff content, same as DiffBlock's own semantics for an actual deleted/
  // added blank line, so this reports +1 -1 rather than "ok".
  const d = toolRendererFor("edit_file");
  const args = JSON.stringify({ file_path: "a.ts", old_string: "", new_string: "" });
  expect(d.summary(item({ toolName: "edit_file", argumentsJSON: args }))).toBe("Edited a.ts · +1 -1");
});

test("edit_file: body renders a synthesized unified-style diff via DiffBlock", () => {
  const d = toolRendererFor("edit_file");
  const Body = d.body!;
  const args = JSON.stringify({ file_path: "a.ts", old_string: "old line", new_string: "new line" });
  render(<Body item={item({ toolName: "edit_file", argumentsJSON: args })} live={false} />);
  expect(screen.getByText("old line")).toBeTruthy();
  expect(screen.getByText("new line")).toBeTruthy();
});

test("edit_file never auto-expands (no autoExpand on this descriptor)", () => {
  const d = toolRendererFor("edit_file");
  expect(d.autoExpand).toBeUndefined();
});

test("edit_file: old_string/new_string survive settlement when argumentsJSON goes missing, via rememberedArgs", () => {
  const d = toolRendererFor("edit_file");
  const callId = "edit_settle_1";
  const args = JSON.stringify({ file_path: "a.ts", old_string: "one\ntwo", new_string: "uno" });
  d.summary(item({ toolName: "edit_file", callId, argumentsJSON: args }));
  const settled = item({ toolName: "edit_file", callId, argumentsJSON: undefined, output: "edited a.ts: 1 replacement" });
  expect(d.summary(settled)).toBe("Edited a.ts · +1 -2");
});

// --- write_file -------------------------------------------------------
// Ground truth: agent/execenv/local.go's WriteFile returns a plain
// confirmation string ("wrote N bytes to path"), never a diff (there's no
// previous-content signal anywhere on the wire to synthesize one from) -
// this is a deliberate parity DEVIATION from the legacy diffRenderer, which
// assumed the raw tool output itself was already diff text; that
// assumption no longer holds against the current tool implementation.

test("write_file: summary names the target file, no fabricated diff stat", () => {
  const d = toolRendererFor("write_file");
  const args = JSON.stringify({ file_path: "new.ts" });
  expect(d.summary(item({ toolName: "write_file", argumentsJSON: args }))).toBe("Wrote new.ts");
});

test("write_file: falls back to `path` when `file_path` is absent", () => {
  const d = toolRendererFor("write_file");
  const args = JSON.stringify({ path: "new.ts" });
  expect(d.summary(item({ toolName: "write_file", argumentsJSON: args }))).toBe("Wrote new.ts");
});

test("write_file: body shows the tool's own confirmation output verbatim", () => {
  const d = toolRendererFor("write_file");
  const Body = d.body!;
  render(<Body item={item({ toolName: "write_file", output: "wrote 128 bytes to new.ts" })} live={false} />);
  expect(screen.getByText("wrote 128 bytes to new.ts")).toBeTruthy();
});

// --- apply_patch ------------------------------------------------------

test("apply_patch: summary lists the touched files from the v4a patch envelope", () => {
  const d = toolRendererFor("apply_patch");
  const patch = ["*** Begin Patch", "*** Update File: a.ts", "@@", "-old", "+new", "*** Delete File: b.ts", "*** End Patch"].join(
    "\n",
  );
  expect(d.summary(item({ toolName: "apply_patch", argumentsJSON: JSON.stringify({ patch }) }))).toBe(
    "Patched a.ts, b.ts · +1 -1",
  );
});

test("apply_patch: preserves first-seen file order and de-duplicates repeats", () => {
  const d = toolRendererFor("apply_patch");
  const patch = [
    "*** Begin Patch",
    "*** Update File: a.ts",
    "@@",
    "+x",
    "*** Update File: a.ts",
    "@@",
    "+y",
    "*** End Patch",
  ].join("\n");
  const summary = d.summary(item({ toolName: "apply_patch", argumentsJSON: JSON.stringify({ patch }) }));
  expect(summary.startsWith("Patched a.ts ·")).toBe(true);
});

test("apply_patch: body renders the raw v4a patch text through DiffBlock", () => {
  const d = toolRendererFor("apply_patch");
  const Body = d.body!;
  const patch = ["*** Begin Patch", "*** Update File: a.ts", "@@", "-old line", "+new line", "*** End Patch"].join("\n");
  render(<Body item={item({ toolName: "apply_patch", argumentsJSON: JSON.stringify({ patch }) })} live={false} />);
  expect(screen.getByText("old line")).toBeTruthy();
  expect(screen.getByText("new line")).toBeTruthy();
});
