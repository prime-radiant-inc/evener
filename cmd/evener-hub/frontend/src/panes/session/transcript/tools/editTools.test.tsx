import type { ItemModel } from "@evener/appwire-client";
import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import rawDiffStyles from "../../../../widgets/diffblock/diffblock.module.css";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { toolRendererFor } from "../toolRenderers";
import "./editTools";

const diffStyles = {
  line: requireClass(rawDiffStyles.line, "diffblock.module.css", "line"),
  content: requireClass(rawDiffStyles.content, "diffblock.module.css", "content"),
  header: requireClass(rawDiffStyles.header, "diffblock.module.css", "header"),
  add: requireClass(rawDiffStyles.add, "diffblock.module.css", "add"),
  del: requireClass(rawDiffStyles.del, "diffblock.module.css", "del"),
};

function diffLines(container: HTMLElement): Element[] {
  return Array.from(container.querySelectorAll(`.${diffStyles.line}`));
}

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

test("edit_file: old_string/new_string read straight from a settled item's own argumentsJSON (the model preserves it through item/completed - see R2)", () => {
  const d = toolRendererFor("edit_file");
  const args = JSON.stringify({ file_path: "a.ts", old_string: "one\ntwo", new_string: "uno" });
  const settled = item({ toolName: "edit_file", argumentsJSON: args, output: "edited a.ts: 1 replacement" });
  expect(d.summary(settled)).toBe("Edited a.ts · +1 -2");
});

test("edit_file: body renders multi-edit edits[] with all hunks and @@ edit N @@ headers", () => {
  const d = toolRendererFor("edit_file");
  const Body = d.body!;
  const args = JSON.stringify({
    file_path: "a.ts",
    edits: [
      { old_string: "first old", new_string: "first new" },
      { old_string: "second old", new_string: "second new" },
    ],
  });
  render(<Body item={item({ toolName: "edit_file", argumentsJSON: args })} live={false} />);
  expect(screen.getByText("@@ edit 1 @@")).toBeTruthy();
  expect(screen.getByText("@@ edit 2 @@")).toBeTruthy();
  expect(screen.getByText("first old")).toBeTruthy();
  expect(screen.getByText("first new")).toBeTruthy();
  expect(screen.getByText("second old")).toBeTruthy();
  expect(screen.getByText("second new")).toBeTruthy();
});

test("edit_file: body renders stringified pre-repair edits", () => {
  const d = toolRendererFor("edit_file");
  const Body = d.body!;
  const args = JSON.stringify({
    file_path: "a.ts",
    edits: JSON.stringify([
      { old_str: "alpha", new_str: "beta" },
      { oldText: "gamma", newText: "delta" },
    ]),
  });
  render(<Body item={item({ toolName: "edit_file", argumentsJSON: args })} live={false} />);
  expect(screen.getByText("@@ edit 1 @@")).toBeTruthy();
  expect(screen.getByText("@@ edit 2 @@")).toBeTruthy();
  expect(screen.getByText("alpha")).toBeTruthy();
  expect(screen.getByText("beta")).toBeTruthy();
  expect(screen.getByText("gamma")).toBeTruthy();
  expect(screen.getByText("delta")).toBeTruthy();
});

test("edit_file: malformed edit_file renders null", () => {
  const d = toolRendererFor("edit_file");
  const Body = d.body!;
  for (const malformed of [
    { file_path: "a.ts" },
    { file_path: "a.ts", edits: [] },
    { file_path: "a.ts", edits: [{ old_string: "a", new_string: "b" }], old_string: "c" },
  ]) {
    const { container, unmount } = render(
      <Body item={item({ toolName: "edit_file", argumentsJSON: JSON.stringify(malformed) })} live={false} />,
    );
    expect(container.firstChild).toBeNull();
    unmount();
  }
});

test("edit_file: removing a Markdown --- line renders with del class in flat single-edit and multi-edit diffs", () => {
  const d = toolRendererFor("edit_file");
  const Body = d.body!;

  const flatArgs = JSON.stringify({ file_path: "doc.md", old_string: "--", new_string: "++" });
  const flat = render(<Body item={item({ toolName: "edit_file", argumentsJSON: flatArgs })} live={false} />);
  const flatLines = diffLines(flat.container);
  expect(flatLines).toHaveLength(4);
  expect(flatLines[0]!.classList.contains(diffStyles.header)).toBe(true);
  expect(flatLines[1]!.classList.contains(diffStyles.header)).toBe(true);
  expect(flatLines[2]!.classList.contains(diffStyles.del)).toBe(true);
  expect(flatLines[2]!.querySelector(`.${diffStyles.content}`)?.textContent).toBe("--");
  expect(flatLines[3]!.classList.contains(diffStyles.add)).toBe(true);
  expect(flatLines[3]!.querySelector(`.${diffStyles.content}`)?.textContent).toBe("++");
  flat.unmount();

  const batchArgs = JSON.stringify({
    file_path: "doc.md",
    edits: [
      { old_string: "--", new_string: "kept" },
      { old_string: "old", new_string: "++" },
    ],
  });
  const batch = render(<Body item={item({ toolName: "edit_file", argumentsJSON: batchArgs })} live={false} />);
  const batchLines = diffLines(batch.container);
  expect(batchLines).toHaveLength(8);
  expect(batchLines[0]!.classList.contains(diffStyles.header)).toBe(true);
  expect(batchLines[1]!.classList.contains(diffStyles.header)).toBe(true);
  expect(batchLines[2]!.classList.contains(diffStyles.header)).toBe(true);
  expect(batchLines[3]!.classList.contains(diffStyles.del)).toBe(true);
  expect(batchLines[3]!.querySelector(`.${diffStyles.content}`)?.textContent).toBe("--");
  expect(batchLines[5]!.classList.contains(diffStyles.header)).toBe(true);
  expect(batchLines[7]!.classList.contains(diffStyles.add)).toBe(true);
  expect(batchLines[7]!.querySelector(`.${diffStyles.content}`)?.textContent).toBe("++");
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
  const patch = [
    "*** Begin Patch",
    "*** Update File: a.ts",
    "@@",
    "-old",
    "+new",
    "*** Delete File: b.ts",
    "*** End Patch",
  ].join("\n");
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
  const patch = ["*** Begin Patch", "*** Update File: a.ts", "@@", "-old line", "+new line", "*** End Patch"].join(
    "\n",
  );
  render(<Body item={item({ toolName: "apply_patch", argumentsJSON: JSON.stringify({ patch }) })} live={false} />);
  expect(screen.getByText("old line")).toBeTruthy();
  expect(screen.getByText("new line")).toBeTruthy();
});
