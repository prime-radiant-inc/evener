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
import { registerToolRenderer, toolRendererFor } from "./toolRenderers";
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

test("a purpose-bearing row stacks: purpose on line 1, demoted summary on line 2 - never one composed line", () => {
  render(
    <ToolRow
      summary="npm test -- src/foo"
      purpose="Running the foo tests"
      failed={false}
      expandable
      expanded={false}
      onToggle={() => {}}
    />,
  );
  const row = screen.getByTestId("tool-row");
  // No em-dash composition separator: the two spans are separate lines (the
  // one-line compose was tried in tiered density and reverted on review).
  expect(row.textContent).toBe("Running the foo testsnpm test -- src/foo");
  // The demoted second line ellipsis-clamps, so the full summary rides the
  // hover title; the unclamped purpose needs none.
  expect(screen.getByTestId("tool-row-summary").getAttribute("title")).toBe("npm test -- src/foo");
  expect(screen.getByTestId("tool-row-purpose").getAttribute("title")).toBe(null);
});

test("an expanded row has the same stacked grammar - open vs collapsed differs only in the body below", () => {
  render(
    <ToolRow
      summary="npm test -- src/foo"
      purpose="Running the foo tests"
      failed={false}
      expandable
      expanded
      onToggle={() => {}}
    />,
  );
  expect(screen.getByTestId("tool-row").textContent).toBe("Running the foo testsnpm test -- src/foo");
});

// The collapsed second line middle-truncates (Jesse's review call): the
// command's ENDING stays on screen - end-truncation kept hiding the file
// being written / the branch being merged. The head ellipsis-clamps under
// pressure; the tail never shrinks. Expanded rows show the WHOLE call,
// wrapping in full with no clamp at all.
test("a collapsed row splits the summary into a clampable head and an always-full tail", () => {
  const summary = "Ran cd ~/prime-radiant/toil-suite/serf && git merge --no-ff transcript-view-design";
  render(
    <ToolRow
      summary={summary}
      purpose="Merging the redesign"
      failed={false}
      expandable
      expanded={false}
      onToggle={() => {}}
    />,
  );
  const head = screen.getByTestId("tool-row-summary-head");
  const tail = screen.getByTestId("tool-row-summary-tail");
  expect((head.textContent ?? "") + (tail.textContent ?? "")).toBe(summary);
  // The split lands mid-string, and the command's ending is the part kept whole.
  expect(head.textContent?.length).toBeGreaterThan(0);
  expect(tail.textContent?.length).toBeGreaterThan(0);
  expect(tail.textContent).toBe(summary.slice(-(tail.textContent?.length ?? 0)));
  expect(summary.endsWith(tail.textContent ?? "")).toBe(true);
  // The full text also rides the hover title.
  expect(screen.getByTestId("tool-row-summary").getAttribute("title")).toBe(summary);
});

test("an expanded row drops the clamp entirely - the full call wraps, no head/tail split", () => {
  const summary = "Ran cd ~/prime-radiant/toil-suite/serf && git merge --no-ff transcript-view-design";
  render(
    <ToolRow summary={summary} purpose="Merging the redesign" failed={false} expandable expanded onToggle={() => {}} />,
  );
  expect(screen.queryByTestId("tool-row-summary-head")).toBe(null);
  expect(screen.getByTestId("tool-row-summary").textContent).toBe(summary);
});

test("the clamp mechanics: head ellipsis-clamps, tail never shrinks, and the clamp lives off .demoted", () => {
  const css = rowCss();
  expect(css).toMatch(/\.clampedHead\s*\{[^}]*text-overflow:\s*ellipsis/);
  // The tail never SHRINKS (flex: none) - but a command whose tail alone
  // passes 60% of the line ellipsizes the tail too, because a glyph-less
  // clip at the container edge reads as a rendering bug, an ellipsis does
  // not.
  const tail = /\.clampedTail\s*\{([^}]*)\}/.exec(css);
  expect(tail).not.toBeNull();
  expect(tail![1]).toMatch(/flex:\s*none/);
  expect(tail![1]).toContain("max-width: 60%");
  expect(tail![1]).toContain("text-overflow: ellipsis");
  // The clamp is collapsed-only (the .clamped modifier); .demoted itself
  // must not reintroduce end-truncation for expanded rows.
  const demoted = /\.demoted\s*\{([^}]*)\}/.exec(css);
  expect(demoted).not.toBeNull();
  expect(demoted![1]).not.toContain("text-overflow");
  expect(demoted![1]).not.toContain("nowrap");
});

test("a purpose-less row is a single line: summary text with the chevron inline at its end", () => {
  render(<ToolRow summary="npm test" failed={false} expandable expanded={false} onToggle={() => {}} />);
  const row = screen.getByTestId("tool-row");
  expect(row.textContent).toBe("npm test");
  expect(screen.getByTestId("tool-row-summary").lastElementChild).toBe(screen.getByTestId("tool-row-chevron"));
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

// The chevron is row CHROME and trails the row (see the grammar), so on a
// failed row the glyph itself is the first element. What A2 actually promises
// is that failure leads the CONTENT: nothing a reader would call part of the
// call itself comes before it.
test("a failed call leads with the failure glyph", () => {
  registerToolRenderer({ match: "trg_failed", summary: () => "Ran false" });
  render(<ToolCallItem item={item({ toolName: "trg_failed", error: "boom" })} turn={turn} live={false} />);
  const row = screen.getByTestId("tool-row");
  expect(row.firstElementChild).toBe(screen.getByTestId("failure-glyph"));
});

// The other half of A2, and the case where the row has no leading chrome at
// all: a clean call with nothing to open reserves space for neither affordance,
// so its summary starts flush with the prose around it.
test("a clean call with nothing to open leads with its summary - no chevron, no glyph", () => {
  registerToolRenderer({ match: "trg_flat", summary: () => "Ran ls" });
  render(<ToolCallItem item={item({ toolName: "trg_flat" })} turn={turn} live={false} />);
  expect(screen.queryByTestId("tool-row-chevron")).toBe(null);
  expect(screen.queryByTestId("failure-glyph")).toBe(null);
  expect(screen.getByTestId("tool-row").firstElementChild).toBe(screen.getByTestId("tool-row-summary"));
});

// The chevron rides INLINE at the end of the headline text (see ToolRow.tsx's
// grammar): inside the purpose when there is one, otherwise inside the
// summary. It is never a flex item of the row, so nothing can justify it a
// column of whitespace away from the words it opens.
test("the chevron rides inline at the end of the purpose text when a purpose exists", () => {
  registerToolRenderer({ match: "trg_chev_inline", summary: () => "Ran ls", body: () => <div>more</div> });
  render(
    <ToolCallItem
      item={item({ toolName: "trg_chev_inline", description: "List the directory" })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getByTestId("tool-row-purpose").lastElementChild).toBe(screen.getByTestId("tool-row-chevron"));
});

test("the chevron rides inline at the end of the summary when there is no purpose", () => {
  registerToolRenderer({ match: "trg_chev_trail", summary: () => "Ran ls", body: () => <div>more</div> });
  render(<ToolCallItem item={item({ toolName: "trg_chev_trail" })} turn={turn} live={false} />);
  expect(screen.getByTestId("tool-row-summary").lastElementChild).toBe(screen.getByTestId("tool-row-chevron"));
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

// --- the kind icon: a per-family glyph rides the tool-use line --------------

test("a descriptor with an icon rides it INLINE at the start of the tool-use line, not the rationale line", () => {
  registerToolRenderer({ match: "trg_icon", summary: () => "Ran ls", icon: "terminal" });
  render(
    <ToolCallItem
      item={item({ toolName: "trg_icon", description: "Check the working directory." })}
      turn={turn}
      live={false}
    />,
  );
  const purpose = screen.getByTestId("tool-row-purpose");
  const summary = screen.getByTestId("tool-row-summary");
  const icon = screen.getByTestId("tool-row-icon");
  expect(summary.contains(icon)).toBe(true);
  expect(purpose.contains(icon)).toBe(false);
  expect(summary.firstElementChild).toBe(icon);
});

test("a summary-less row (a delegate's purpose-only row) rides the icon on the purpose line instead", () => {
  render(
    <ToolRow summary="" purpose="Scout the repo" icon="delegate" failed={false} expandable={false} expanded={false} />,
  );
  const purpose = screen.getByTestId("tool-row-purpose");
  const icon = screen.getByTestId("tool-row-icon");
  expect(purpose.contains(icon)).toBe(true);
  expect(purpose.firstElementChild).toBe(icon);
});

test("a descriptor WITHOUT an icon renders no icon element - the icon-less grammar is unchanged", () => {
  registerToolRenderer({ match: "trg_no_icon", summary: () => "Ran ls" });
  render(<ToolCallItem item={item({ toolName: "trg_no_icon" })} turn={turn} live={false} />);
  expect(screen.queryByTestId("tool-row-icon")).toBe(null);
});

test("an unregistered tool - every MCP tool - inherits the default descriptor's generic wrench", () => {
  render(<ToolCallItem item={item({ toolName: "trg_unregistered_mcp_thing" })} turn={turn} live={false} />);
  expect(screen.getByTestId("tool-row-icon")).toBeTruthy();
});

test("the kind icon is decorative - the row's text already names the action", () => {
  render(<ToolRow summary="Ran ls" icon="terminal" failed={false} expandable={false} expanded={false} />);
  const icon = screen.getByTestId("tool-row-icon");
  expect(icon.getAttribute("aria-hidden")).toBe("true");
});

test("the icon is inline with the tool-use text - no outdented gutter, no reserved column", () => {
  const css = rowCss();
  const iconRule = css.match(/\.summaryIcon\s*\{([^}]*)\}/);
  expect(iconRule).not.toBe(null);
  expect(iconRule?.[1]).toContain("display: inline-flex");
  expect(iconRule?.[1]).toContain("vertical-align: middle");
  // The retired gutter grammar (a data-icon row attribute driving an indent
  // override) is gone entirely.
  expect(css).not.toContain("data-icon");
});

// --- the summary face: sans by default, fixed-width for shell only ----------

test("the summary face is proportional by default - fixed-width is reserved for shell commands", () => {
  const css = rowCss();
  const summaryRule = css.match(/\.summary\s*\{([^}]*)\}/);
  expect(summaryRule).not.toBe(null);
  expect(summaryRule?.[1]).toContain("font-family: var(--font-sans)");
  expect(summaryRule?.[1]).not.toContain("--font-mono");
  const monoRule = css.match(/\.mono\s*\{([^}]*)\}/);
  expect(monoRule).not.toBe(null);
  expect(monoRule?.[1]).toContain("font-family: var(--font-mono)");
});

test("a descriptor's monoSummary flag puts its summary in fixed-width - shell's command line opts in", () => {
  render(<ToolRow summary="Ran false" monoSummary failed={false} expandable={false} expanded={false} />);
  const withMono = screen.getByTestId("tool-row-summary");
  cleanup();
  render(<ToolRow summary="Read src/app.ts" failed={false} expandable={false} expanded={false} />);
  const withoutMono = screen.getByTestId("tool-row-summary");
  expect(withMono.className).not.toBe(withoutMono.className);
  expect(toolRendererFor("shell").monoSummary).toBe(true);
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

// Associativity rhythm (Jesse's review call): the gap between a rationale
// and the call it executes must read TIGHTER than the gap between separate
// calls - 4px inside (row-gap), 16px outside (.call padding). Before this
// split both gaps measured the same 8px and a run of calls read as one
// undifferentiated list.
test("the rationale-to-call gap is tighter than the gap between separate calls", () => {
  const css = rowCss();
  expect(css).toMatch(/\.row\s*\{[^}]*row-gap:\s*var\(--space-1\)/);
  const call = /\.call\s*\{([^}]*)\}/.exec(css);
  expect(call).not.toBeNull();
  expect(call![1]).toContain("padding: var(--space-2) 0");
});

// The purpose is the agent's stated rationale for the call - commentary on
// the machine text, set off in italics rather than a colour or size of its
// own (Jesse's review call on the tiered-density follow-up).
test("the stated purpose renders in italics", () => {
  expect(rowCss()).toMatch(/\.purpose\s*\{[^}]*font-style:\s*italic/);
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
  expect(css).toMatch(/\.chevron\s*>\s*svg\s*\{[^}]*transition:\s*transform/);
  expect(css).toMatch(/\.body\s*\{[^}]*animation:\s*tool-body-in/);
});

// The chevron SPAN is 1lh tall (first-line alignment) and so not square:
// turning IT paints ~3.5px past its 14px layout box on each side, which at
// the trailing edge escapes the row (overflowguard, 2026-07-28). The square
// svg turns instead - a square rotates within its own bounds.
test("the open-state rotation turns the square svg, never the 1lh-tall span", () => {
  const css = rowCss();
  expect(css).toMatch(/\.chevron\[data-open="true"\]\s*>\s*svg\s*\{[^}]*transform:\s*rotate\(90deg\)/);
  expect(css).not.toMatch(/\.chevron\[data-open="true"\]\s*\{/);
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
