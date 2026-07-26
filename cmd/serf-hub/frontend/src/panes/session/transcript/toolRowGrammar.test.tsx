// The shared tool-row grammar: one row component every tool renderer composes
// (ToolRow.tsx). These tests are about the ROW, not about any one tool's
// content - they drive it both directly and through ToolCallItem, which is the
// only production caller.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import type { ItemModel, TurnModel } from "../../../protocol/model";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import { ToolCallItem } from "./ToolCallItem";
import { statedPurposeOf, ToolRow } from "./ToolRow";
import { registerToolRenderer } from "./toolRenderers";
// The failure-glyph and exit-code tests below drive the REAL shell descriptor
// (its failed()/detail() hooks are the whole point of A2), so this file has to
// register it - without this import "shell" resolves to DEFAULT_DESCRIPTOR and
// those assertions test nothing. Same precedent as ToolCallItem.test.tsx.
import "./tools/shellTool";

afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
});

const turn: TurnModel = { id: "turn_1", status: "inProgress", items: [] };

// jsdom runs no animations and computes no cursor, so the row's affordances and
// A6's motion can only be asserted at the declaration level. Comments are
// stripped FIRST: a stylesheet grep that matches its own comment prose asserts
// nothing (this repo has that precedent).
function rowCss(): string {
  const path = join(dirname(fileURLToPath(import.meta.url)), "toolcallitem.module.css");
  return readFileSync(path, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
}

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

// --- A1: one row grammar, composed not copied -----------------------------

test("a non-expandable row renders the summary in the shared row element", () => {
  render(<ToolRow summary="Ran ls" failed={false} expandable={false} expanded={false} />);
  const row = screen.getByTestId("tool-row");
  expect(row.tagName).toBe("DIV");
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("Ran ls");
});

test("an expandable row renders as a <summary> so it is natively keyboard-operable", () => {
  render(<ToolRow summary="Ran ls" failed={false} expandable expanded={false} onToggle={() => {}} />);
  expect(screen.getByTestId("tool-row").tagName).toBe("SUMMARY");
});

test("trailing affordances render after the summary text", () => {
  render(
    <ToolRow
      summary="Read a.ts"
      failed={false}
      expandable={false}
      expanded={false}
      trailing={<button type="button">Open beside</button>}
    />,
  );
  expect(screen.getByRole("button", { name: "Open beside" })).toBeTruthy();
});

test("every tool renderer's row comes from ToolRow - ToolCallItem renders exactly one per call", () => {
  registerToolRenderer({ match: "trg_one_row", summary: () => "did a thing", body: () => <div>b</div> });
  render(<ToolCallItem item={item({ toolName: "trg_one_row" })} turn={turn} live={false} />);
  expect(screen.getAllByTestId("tool-row")).toHaveLength(1);
});

// --- A1b: the agent's stated purpose comes back ---------------------------

test("the row renders item.description as the call's stated purpose", () => {
  registerToolRenderer({ match: "trg_purpose", summary: () => "Ran ls -la" });
  render(
    <ToolCallItem
      item={item({ toolName: "trg_purpose", description: "Check the working directory." })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getByTestId("tool-row-purpose").textContent).toBe("Check the working directory.");
});

test("the purpose LEADS the verb/target summary in document order", () => {
  registerToolRenderer({ match: "trg_purpose_order", summary: () => "Ran ls -la" });
  render(
    <ToolCallItem
      item={item({ toolName: "trg_purpose_order", description: "Check the working directory." })}
      turn={turn}
      live={false}
    />,
  );
  const row = screen.getByTestId("tool-row");
  const purpose = screen.getByTestId("tool-row-purpose");
  const summary = screen.getByTestId("tool-row-summary");
  const children = Array.from(row.querySelectorAll("[data-testid]"));
  expect(children.indexOf(purpose)).toBeLessThan(children.indexOf(summary));
});

test("no description means no purpose element at all - no placeholder, no empty separator", () => {
  registerToolRenderer({ match: "trg_no_purpose", summary: () => "Ran ls" });
  render(<ToolCallItem item={item({ toolName: "trg_no_purpose" })} turn={turn} live={false} />);
  expect(screen.queryByTestId("tool-row-purpose")).toBe(null);
});

test("a whitespace-only description is absence, not a purpose", () => {
  registerToolRenderer({ match: "trg_blank_purpose", summary: () => "Ran ls" });
  render(<ToolCallItem item={item({ toolName: "trg_blank_purpose", description: "   " })} turn={turn} live={false} />);
  expect(screen.queryByTestId("tool-row-purpose")).toBe(null);
});

// The subagent activity feed reads the SAME field with a very different
// presentation; the two must at least agree on when it exists, which is what
// this shared helper is for (see its doc comment).
test("statedPurposeOf is the one absent-vs-present rule both surfaces share", () => {
  expect(statedPurposeOf({ description: "  Check the tree.  " })).toBe("Check the tree.");
  expect(statedPurposeOf({ description: "   " })).toBeUndefined();
  expect(statedPurposeOf({ description: "" })).toBeUndefined();
  expect(statedPurposeOf({})).toBeUndefined();
});

// --- A2: failure is a glyph on the left; success costs no space -----------

test("a failed call renders the failure glyph as the row's first element", () => {
  registerToolRenderer({ match: "trg_failed", summary: () => "Ran false" });
  render(<ToolCallItem item={item({ toolName: "trg_failed", error: "boom" })} turn={turn} live={false} />);
  const row = screen.getByTestId("tool-row");
  expect(row.firstElementChild).toBe(screen.getByTestId("failure-glyph"));
});

test("the failure glyph has a real accessible name, not a bare character", () => {
  registerToolRenderer({ match: "trg_failed_name", summary: () => "Ran false" });
  render(<ToolCallItem item={item({ toolName: "trg_failed_name", error: "boom" })} turn={turn} live={false} />);
  expect(screen.getByRole("img", { name: "Failed" })).toBeTruthy();
});

test("a successful call renders NO glyph element at all - the row reserves no space for one", () => {
  registerToolRenderer({ match: "trg_ok", summary: () => "Ran true", body: () => <div>b</div> });
  render(<ToolCallItem item={item({ toolName: "trg_ok" })} turn={turn} live={false} />);
  expect(screen.queryByTestId("failure-glyph")).toBe(null);
});

test("a shell call that exited nonzero is marked failed by the glyph, not only by its exit-code text", () => {
  render(
    <ToolCallItem
      item={item({ toolName: "shell", argumentsJSON: JSON.stringify({ command: "false" }), exitCode: 1 })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getByTestId("failure-glyph")).toBeTruthy();
});

test("a shell call that exited 0 gets no failure glyph", () => {
  render(
    <ToolCallItem
      item={item({ toolName: "shell", argumentsJSON: JSON.stringify({ command: "true" }), exitCode: 0 })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.queryByTestId("failure-glyph")).toBe(null);
});

test("the exit code stops being the headline: it is reachable via the row's title, not its text", () => {
  render(
    <ToolCallItem
      item={item({ toolName: "shell", argumentsJSON: JSON.stringify({ command: "false" }), exitCode: 1 })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("Ran false");
  expect(screen.getByTestId("tool-row").getAttribute("title")).toContain("exit 1");
});

// A title alone is mouse-only: no keyboard path, uneven screen-reader support.
// "Reachable" has to mean reachable without a mouse - but the exit code is
// ALREADY real text in the expanded body, because agent/session_tools_shell.go's
// formatShellResult bakes a trailing "[exit N]" footer into the captured
// output itself (the model reads that same text as its tool result). A
// second, client-synthesized copy of the same fact (kata wksf) is pure
// duplication, not a second reachability path, so ToolCallItem no longer
// renders one.
test("the exit code is reachable WITHOUT a mouse - via the raw output's own trailing footer, not a client-side duplicate", () => {
  render(
    <ToolCallItem
      item={item({
        toolName: "shell",
        argumentsJSON: JSON.stringify({ command: "false" }),
        exitCode: 1,
        output: "false\n[exit 1]",
      })}
      turn={turn}
      live={false}
    />,
  );
  // A nonzero exit auto-expands, so the body (and the footer inside it) is
  // already on screen.
  expect(screen.getByTestId("tool-call-body").textContent).toContain("exit 1");
  // No second, client-synthesized copy of the same fact (kata wksf).
  expect(screen.queryByTestId("tool-call-detail")).toBe(null);
});

// --- A3: the row looks clickable ------------------------------------------

test("an expandable row exposes aria-expanded reflecting its state", () => {
  registerToolRenderer({ match: "trg_aria", summary: () => "s", body: () => <div>b</div> });
  render(<ToolCallItem item={item({ toolName: "trg_aria" })} turn={turn} live={false} />);
  const row = screen.getByTestId("tool-row");
  expect(row.getAttribute("aria-expanded")).toBe("false");
  fireEvent.click(row);
  expect(screen.getByTestId("tool-row").getAttribute("aria-expanded")).toBe("true");
});

test("a non-expandable row carries no aria-expanded (there is nothing to expand)", () => {
  registerToolRenderer({ match: "trg_no_aria", summary: () => "s" });
  render(<ToolCallItem item={item({ toolName: "trg_no_aria" })} turn={turn} live={false} />);
  expect(screen.getByTestId("tool-row").getAttribute("aria-expanded")).toBe(null);
});

test("an expandable row shows a disclosure chevron; a non-expandable row shows none", () => {
  registerToolRenderer({ match: "trg_chev", summary: () => "s", body: () => <div>b</div> });
  registerToolRenderer({ match: "trg_no_chev", summary: () => "s" });
  const { unmount } = render(<ToolCallItem item={item({ toolName: "trg_chev" })} turn={turn} live={false} />);
  expect(screen.getByTestId("tool-row-chevron")).toBeTruthy();
  unmount();
  render(<ToolCallItem item={item({ id: "item_2", toolName: "trg_no_chev" })} turn={turn} live={false} />);
  expect(screen.queryByTestId("tool-row-chevron")).toBe(null);
});

test("the chevron reports its open state for the stylesheet's rotation, and is hidden from AT", () => {
  registerToolRenderer({ match: "trg_chev_state", summary: () => "s", body: () => <div>b</div> });
  render(<ToolCallItem item={item({ toolName: "trg_chev_state" })} turn={turn} live={false} />);
  const chevron = screen.getByTestId("tool-row-chevron");
  expect(chevron.getAttribute("aria-hidden")).toBe("true");
  expect(chevron.getAttribute("data-open")).toBe("false");
  fireEvent.click(screen.getByTestId("tool-row"));
  expect(screen.getByTestId("tool-row-chevron").getAttribute("data-open")).toBe("true");
});

test("an expandable row reads as clickable - a pointer cursor and a hover state", () => {
  const css = rowCss();
  expect(css).toMatch(/summary\.row\s*\{[^}]*cursor:\s*pointer/);
  expect(css).toMatch(/summary\.row:hover\s*\{[^}]*background:/);
  expect(css).toMatch(/summary\.row:focus-visible\s*\{[^}]*outline:/);
});

// Measured in the running app: the light theme resolves --surface-1 AND
// --surface-2 to the same #FFFFFF as the pane, so a surface-token hover was
// literally invisible there. The hover must be an ink wash instead.
test("the row hover is an ink wash, not a surface token that can match the pane", () => {
  const hover = /summary\.row:hover\s*\{([^}]*)\}/.exec(rowCss());
  expect(hover).not.toBeNull();
  expect(hover![1]).toMatch(/var\(--ink-/);
  expect(hover![1]).not.toMatch(/var\(--surface-/);
});

// A1: with a purpose present the summary demotes onto its own line, but the
// affordances and chevron must stay on the purpose's line - otherwise the row
// wraps to three lines (measured: 73px instead of 47px).
test("the demoted summary is ordered last so affordances do not wrap onto a third line", () => {
  expect(rowCss()).toMatch(/\.demoted\s*\{[^}]*order:\s*1/);
});

// kata rdry: the demoted line is a tool-RESULT ("Wrote fizzbuzz.py"), not a
// placeholder/disabled/timestamp - --ink-low's documented job (design-system.md)
// - and measures 2.97:1 dark / 3.64:1 light, under the 4.5:1 AA floor for body
// text. Same precedent as usermessageitem's .tag (moved off --ink-low for the
// same reason). --ink-mid clears AA at 6.86/6.56 in both themes.
test("the demoted summary line is readable text (--ink-mid), not the sub-AA --ink-low", () => {
  const demoted = /\.demoted\s*\{([^}]*)\}/.exec(rowCss());
  expect(demoted).not.toBeNull();
  expect(demoted![1]).toMatch(/var\(--ink-mid\)/);
  expect(demoted![1]).not.toMatch(/var\(--ink-low\)/);
});

// --- A6: the tool disclosure animates, subtly, honoring reduced motion ----

test("the chevron rotation and the body fade are declared with real motion", () => {
  const css = rowCss();
  expect(css).toMatch(/\.chevron\s*\{[^}]*transition:\s*transform/);
  expect(css).toMatch(/\.body\s*\{[^}]*animation:\s*tool-body-in/);
});

test("the row's motion uses only existing motion tokens - no invented duration", () => {
  const css = rowCss();
  expect(css.match(/(?:transition|animation):[^;]*?\d+m?s/g)).toBe(null);
  expect(css).toContain("var(--motion-duration-overlay)");
  expect(css).toContain("var(--motion-easing-standard)");
});

test("all of the row's motion sits behind a prefers-reduced-motion gate", () => {
  const css = rowCss();
  const gates = css.match(/@media\s*\(prefers-reduced-motion:\s*no-preference\)\s*\{[\s\S]*?\n\}/g);
  expect(gates).not.toBeNull();
  let outsideGates = css;
  for (const gate of gates ?? []) outsideGates = outsideGates.replace(gate, "");
  expect(outsideGates).not.toMatch(/\btransition:/);
  expect(outsideGates).not.toMatch(/\banimation:/);
});
