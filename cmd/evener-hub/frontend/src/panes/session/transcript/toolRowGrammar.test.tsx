// The shared tool-row grammar: one row component every tool renderer composes
// (ToolRow.tsx). These tests are about the ROW, not about any one tool's
// content - they drive it both directly and through ToolCallItem, which is the
// only production caller.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, expect, test, vi } from "vitest";
import type { ItemModel, TurnModel } from "../../../protocol/model";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { makeTranscriptDisplayConfig } from "../../../transcriptDisplay/config";
import { TranscriptRenderProvider } from "../../../transcriptDisplay/renderContext";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import { ToolCallItem } from "./ToolCallItem";
import { statedIntentOf, ToolRow } from "./ToolRow";
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

// At activity level (the default config when no provider is used),
// expandByDefault is now true — bodies auto-expand. Tests that need a
// collapsed-by-default row use a tools-level config where expandByDefault
// is false.
const toolsConfig = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });

function renderTools(node: ReactElement) {
  return render(
    <TranscriptRenderProvider config={toolsConfig} surface="readOnly" disclosureScope="trg:tools">
      {node}
    </TranscriptRenderProvider>,
  );
}

// --- A1: one row grammar, composed not copied -----------------------------

test("a non-expandable row renders the summary in the shared row element", () => {
  render(<ToolRow summary="Ran ls" failed={false} expandable={false} expanded={false} />);
  const row = screen.getByTestId("tool-row");
  expect(row.tagName).toBe("DIV");
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("Ran ls");
});

test("an intent-bearing row stacks: intent on line 1, demoted summary on line 2 - never one composed line", () => {
  render(
    <ToolRow
      summary="npm test -- src/foo"
      intent="Running the foo tests"
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
  // hover title; the unclamped intent needs none.
  expect(screen.getByTestId("tool-row-summary").getAttribute("title")).toBe("npm test -- src/foo");
  expect(screen.getByTestId("tool-row-intent").getAttribute("title")).toBe(null);
});

test("an expanded row has the same stacked grammar - open vs collapsed differs only in the body below", () => {
  render(
    <ToolRow
      summary="npm test -- src/foo"
      intent="Running the foo tests"
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
  const summary = "Ran cd ~/prime-radiant/toil-suite/evener && git merge --no-ff transcript-view-design";
  render(
    <ToolRow
      summary={summary}
      intent="Merging the redesign"
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
  const summary = "Ran cd ~/prime-radiant/toil-suite/evener && git merge --no-ff transcript-view-design";
  render(
    <ToolRow summary={summary} intent="Merging the redesign" failed={false} expandable expanded onToggle={() => {}} />,
  );
  expect(screen.queryByTestId("tool-row-summary-head")).toBe(null);
  expect(screen.getByTestId("tool-row-summary").textContent).toBe(summary);
});

// The clamped head/tail spans are FLEX ITEMS (.clamped is display:flex), and
// CSS white-space processing drops whitespace at a flex item's line edges - so
// a 60% cut that lands next to a space renders "Ran go test./...": the space
// fell at the tail's start (or the head's end) and the browser removed it.
// The split must walk the cut off any whitespace so every space stays
// INTERIOR to one span. jsdom runs no layout, so this asserts the boundary
// condition directly.
test("the collapsed head/tail split never leaves whitespace at a span boundary - the browser drops it", () => {
  const summary = "Ran go test ./..."; // the 60% cut lands exactly on the space
  render(
    <ToolRow
      summary={summary}
      intent="Running the tests"
      failed={false}
      expandable
      expanded={false}
      onToggle={() => {}}
    />,
  );
  const head = screen.getByTestId("tool-row-summary-head").textContent ?? "";
  const tail = screen.getByTestId("tool-row-summary-tail").textContent ?? "";
  // Nothing lost, nothing added...
  expect(head + tail).toBe(summary);
  // ...and no character the browser would collapse sits at a span edge.
  expect(head).not.toMatch(/\s$/);
  expect(tail).not.toMatch(/^\s/);
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

test("an intent-less row is a single line: summary text followed by its disclosure button", () => {
  render(<ToolRow summary="npm test" failed={false} expandable expanded={false} onToggle={() => {}} />);
  const row = screen.getByTestId("tool-row");
  expect(row.textContent).toBe("npm test");
  expect(row.lastElementChild).toBe(screen.getByTestId("tool-row-trigger"));
  expect(screen.getByTestId("tool-row-trigger").lastElementChild).toBe(screen.getByTestId("tool-row-chevron"));
});

test("an expandable row renders as a real button with no interactive descendants", () => {
  render(<ToolRow summary="Ran ls" failed={false} expandable expanded={false} onToggle={() => {}} />);
  const row = screen.getByTestId("tool-row");
  const trigger = screen.getByTestId("tool-row-trigger") as HTMLButtonElement;
  expect(row.tagName).toBe("DIV");
  expect(trigger.tagName).toBe("BUTTON");
  expect(trigger.type).toBe("button");
  expect(trigger.tabIndex).toBe(0);
  expect(trigger.querySelectorAll("a, button, input, select, textarea, [tabindex]:not([tabindex='-1'])")).toHaveLength(
    0,
  );
});

test("an intent-less disclosure button covers the visible summary while sibling controls stay outside it", () => {
  const css = rowCss();
  render(
    <ToolRow
      summary="Fetched https://example.com/page"
      summaryLink="https://example.com/page"
      failed={false}
      expandable
      expanded={false}
      onToggle={() => {}}
      trailing={<button type="button">Open beside</button>}
    />,
  );
  const row = screen.getByTestId("tool-row");
  const trigger = screen.getByTestId("tool-row-trigger");
  expect(trigger.parentElement).toBe(row);
  expect(trigger.contains(screen.getByRole("link"))).toBe(false);
  expect(trigger.contains(screen.getByRole("button", { name: "Open beside" }))).toBe(false);
  expect(css).toMatch(/\.row:not\(\[data-intent="true"\]\) \.trigger\s*\{[^}]*position:\s*absolute[^}]*inset:\s*0/);
  expect(css).toMatch(/\.row:not\(\[data-intent="true"\]\) \.summaryLine\s*\{[^}]*pointer-events:\s*none/);
  expect(css).toMatch(/\.row:not\(\[data-intent="true"\]\) \.summaryLine a,[\s\S]*pointer-events:\s*auto/);
});

test("native Enter and Space activation toggle disclosure exactly once", async () => {
  const user = userEvent.setup();
  const onToggle = vi.fn();
  render(<ToolRow summary="Ran ls" failed={false} expandable expanded={false} onToggle={onToggle} />);
  const trigger = screen.getByTestId("tool-row-trigger");

  trigger.focus();
  await user.keyboard("{Enter}");
  expect(onToggle).toHaveBeenCalledTimes(1);
  await user.keyboard(" ");
  expect(onToggle).toHaveBeenCalledTimes(2);
});

test("keyboard activation of sibling link and action does not toggle disclosure", async () => {
  const user = userEvent.setup();
  const onToggle = vi.fn();
  const onAction = vi.fn();
  const linkActivated = vi.fn();
  render(
    <ToolRow
      summary="Fetched https://example.com/page"
      summaryLink="https://example.com/page"
      failed={false}
      expandable
      expanded
      onToggle={onToggle}
      trailing={
        <button type="button" onClick={onAction}>
          Open beside
        </button>
      }
    />,
  );
  const link = screen.getByRole("link");
  link.addEventListener("click", linkActivated);
  link.focus();
  await user.keyboard("{Enter}");
  const action = screen.getByRole("button", { name: "Open beside" });
  action.focus();
  await user.keyboard("{Enter}");
  await user.keyboard(" ");

  expect(linkActivated).toHaveBeenCalledTimes(1);
  expect(onAction).toHaveBeenCalledTimes(2);
  expect(onToggle).not.toHaveBeenCalled();
});

test("pointer activation toggles the trigger once while sibling actions stay independent", async () => {
  const user = userEvent.setup();
  const onToggle = vi.fn();
  const onAction = vi.fn();
  render(
    <ToolRow
      summary="Fetched https://example.com/page"
      summaryLink="https://example.com/page"
      failed={false}
      expandable
      expanded={false}
      onToggle={onToggle}
      trailing={
        <button type="button" onClick={onAction}>
          Open transcript
        </button>
      }
    />,
  );
  await user.click(screen.getByTestId("tool-row-trigger"));
  await user.click(screen.getByRole("button", { name: "Open transcript" }));
  expect(onToggle).toHaveBeenCalledTimes(1);
  expect(onAction).toHaveBeenCalledTimes(1);
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

// --- A1b: the agent's stated intent comes back ---------------------------

test("the row renders item.description as the call's stated intent", () => {
  registerToolRenderer({ match: "trg_intent", summary: () => "Ran ls -la" });
  render(
    <ToolCallItem
      item={item({ toolName: "trg_intent", description: "Check the working directory." })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getByTestId("tool-row-intent").textContent).toBe("Check the working directory.");
});

test("the intent LEADS the verb/target summary in document order", () => {
  registerToolRenderer({ match: "trg_intent_order", summary: () => "Ran ls -la" });
  render(
    <ToolCallItem
      item={item({ toolName: "trg_intent_order", description: "Check the working directory." })}
      turn={turn}
      live={false}
    />,
  );
  const row = screen.getByTestId("tool-row");
  const intent = screen.getByTestId("tool-row-intent");
  const summary = screen.getByTestId("tool-row-summary");
  const children = Array.from(row.querySelectorAll("[data-testid]"));
  expect(children.indexOf(intent)).toBeLessThan(children.indexOf(summary));
});

test("no description means no intent element at all - no placeholder, no empty separator", () => {
  registerToolRenderer({ match: "trg_no_intent", summary: () => "Ran ls" });
  render(<ToolCallItem item={item({ toolName: "trg_no_intent" })} turn={turn} live={false} />);
  expect(screen.queryByTestId("tool-row-intent")).toBe(null);
});

test("a whitespace-only description is absence, not an intent", () => {
  registerToolRenderer({ match: "trg_blank_intent", summary: () => "Ran ls" });
  render(<ToolCallItem item={item({ toolName: "trg_blank_intent", description: "   " })} turn={turn} live={false} />);
  expect(screen.queryByTestId("tool-row-intent")).toBe(null);
});

// The subagent activity feed reads the SAME field with a very different
// presentation; the two must at least agree on when it exists, which is what
// this shared helper is for (see its doc comment).
test("statedIntentOf is the one absent-vs-present rule both surfaces share", () => {
  expect(statedIntentOf({ description: "  Check the tree.  " })).toBe("Check the tree.");
  expect(statedIntentOf({ description: "   " })).toBeUndefined();
  expect(statedIntentOf({ description: "" })).toBeUndefined();
  expect(statedIntentOf({})).toBeUndefined();
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
// grammar): inside the intent when there is one, otherwise inside the
// summary. It is never a flex item of the row, so nothing can justify it a
// column of whitespace away from the words it opens.
test("the chevron rides inline at the end of the intent text when an intent exists", () => {
  registerToolRenderer({ match: "trg_chev_inline", summary: () => "Ran ls", body: () => <div>more</div> });
  render(
    <ToolCallItem
      item={item({ toolName: "trg_chev_inline", description: "List the directory" })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getByTestId("tool-row-intent").lastElementChild).toBe(screen.getByTestId("tool-row-chevron"));
});

test("the chevron rides inline at the end of the summary when there is no intent", () => {
  registerToolRenderer({ match: "trg_chev_trail", summary: () => "Ran ls", body: () => <div>more</div> });
  render(<ToolCallItem item={item({ toolName: "trg_chev_trail" })} turn={turn} live={false} />);
  expect(screen.getByTestId("tool-row-trigger").lastElementChild).toBe(screen.getByTestId("tool-row-chevron"));
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
  // A failed shell row auto-expands, and an expanded shell row drops its
  // summary (the body's pretty-printed block is the single copy of the
  // command) - so assert the intent directly: the exit code is nowhere in
  // the row's TEXT, only on its hover title.
  expect(screen.getByTestId("tool-row").textContent).not.toContain("exit 1");
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

// --- the kind icon: a per-family glyph in the rail beside the rationale -----

test("a descriptor with an icon puts it in the RAIL as the row's first flex item, beside the rationale - not inside either text line", () => {
  registerToolRenderer({ match: "trg_icon", summary: () => "Ran ls", icon: "terminal" });
  render(
    <ToolCallItem
      item={item({ toolName: "trg_icon", description: "Check the working directory." })}
      turn={turn}
      live={false}
    />,
  );
  const row = screen.getByTestId("tool-row");
  const intent = screen.getByTestId("tool-row-intent");
  const summary = screen.getByTestId("tool-row-summary");
  const icon = screen.getByTestId("tool-row-icon");
  expect(row.firstElementChild).toBe(icon);
  expect(intent.contains(icon)).toBe(false);
  expect(summary.contains(icon)).toBe(false);
});

test("a summary-less row (a delegate's intent-only row) also rails the icon beside its rationale line", () => {
  render(
    <ToolRow summary="" intent="Scout the repo" icon="delegate" failed={false} expandable={false} expanded={false} />,
  );
  const row = screen.getByTestId("tool-row");
  const icon = screen.getByTestId("tool-row-icon");
  expect(row.firstElementChild).toBe(icon);
  expect(screen.getByTestId("tool-row-intent").contains(icon)).toBe(false);
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

test("the rail icon is 50% opacity in the speaker-avatar column, pulled into the gutter only above the breakpoint", () => {
  const css = rowCss();
  const iconRule = css.match(/\.rowIcon\s*\{([^}]*)\}/);
  expect(iconRule).not.toBe(null);
  expect(iconRule?.[1]).toContain("opacity: 0.5");
  expect(iconRule?.[1]).toContain("width: var(--speaker-avatar-size)");
  // slot + margin + the row's own column-gap = one speaker-gutter, so the
  // rationale lands exactly on the content edge (aligned with its summary).
  expect(iconRule?.[1]).toContain("margin-right: calc(var(--speaker-gap) - var(--space-2))");
  // The gutter pull shares the runContent indent's media query (the negative
  // margin is only safe when the wrapper's reserved padding exists).
  const mediaRule = css.match(/@media \(min-width: 700px\) \{\s*\.rowIcon\s*\{([^}]*)\}/);
  expect(mediaRule).not.toBe(null);
  expect(mediaRule?.[1]).toContain("margin-left: calc(-1 * var(--speaker-gutter))");
  // The retired inline grammar (the icon inside the summary's text flow) is
  // gone entirely.
  expect(css).not.toContain(".summaryIcon");
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
  // At activity level the body auto-expands; use tools level to test the
  // collapsed→expanded transition.
  renderTools(<ToolCallItem item={item({ toolName: "trg_aria" })} turn={turn} live={false} />);
  const trigger = screen.getByTestId("tool-row-trigger");
  expect(trigger.getAttribute("aria-expanded")).toBe("false");
  fireEvent.click(trigger);
  expect(trigger.getAttribute("aria-expanded")).toBe("true");
});

test("a non-expandable row carries no aria-expanded (there is nothing to expand)", () => {
  registerToolRenderer({ match: "trg_no_aria", summary: () => "s" });
  render(<ToolCallItem item={item({ toolName: "trg_no_aria" })} turn={turn} live={false} />);
  expect(screen.getByTestId("tool-row").querySelector("button")).toBe(null);
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
  // At activity level the body auto-expands; use tools level to test the
  // collapsed→expanded chevron state transition.
  renderTools(<ToolCallItem item={item({ toolName: "trg_chev_state" })} turn={turn} live={false} />);
  const chevron = screen.getByTestId("tool-row-chevron");
  expect(chevron.getAttribute("aria-hidden")).toBe("true");
  expect(chevron.getAttribute("data-open")).toBe("false");
  fireEvent.click(screen.getByTestId("tool-row-trigger"));
  expect(screen.getByTestId("tool-row-chevron").getAttribute("data-open")).toBe("true");
});

test("an expandable row reads as clickable - a pointer cursor and a hover state", () => {
  const css = rowCss();
  expect(css).toMatch(/\.trigger\s*\{[^}]*cursor:\s*pointer/);
  expect(css).toMatch(/\.trigger:hover\s*\{[^}]*background:/);
  expect(css).toMatch(/\.trigger:focus-visible\s*\{[^}]*outline:/);
});

// Measured in the running app: the light theme resolves --surface-1 AND
// --surface-2 to the same #FFFFFF as the pane, so a surface-token hover was
// literally invisible there. The hover must be an ink wash instead.
test("the row hover is an ink wash, not a surface token that can match the pane", () => {
  const hover = /\.trigger:hover\s*\{([^}]*)\}/.exec(rowCss());
  expect(hover).not.toBeNull();
  expect(hover![1]).toMatch(/var\(--ink-/);
  expect(hover![1]).not.toMatch(/var\(--surface-/);
});

// A1: with an intent present the summary demotes onto its own line, and the
// affordances ride THAT line - the tool call they act on - not the rationale
// line. Rendered inline at the end of the summary text (same idiom as the
// chevron on the headline line), so the row still never wraps to three.
test("with an intent, a trailing affordance rides the tool-call line, not the rationale line", () => {
  render(
    <ToolRow
      summary="Read a.ts"
      intent="Check the source."
      failed={false}
      expandable={false}
      expanded={false}
      trailing={<button type="button">Open beside</button>}
    />,
  );
  const button = screen.getByRole("button", { name: "Open beside" });
  expect(screen.getByTestId("tool-row-summary").contains(button)).toBe(true);
  expect(screen.getByTestId("tool-row-intent").contains(button)).toBe(false);
});

// An intent-only row (the delegate card: intent, no summary) has no
// tool-call line, so its affordance rides the disclosure line (ToolRow's
// grammar). Regression guard: it used to drop onto a second line of its own.
test("an intent-only row trails its affordance on the disclosure line, not a line of its own", () => {
  render(
    <ToolRow
      summary=""
      intent="Proving family scheduler quiescence"
      failed={false}
      expandable
      expanded={false}
      onToggle={() => {}}
      trailing={<button type="button">Open transcript</button>}
    />,
  );
  const row = screen.getByTestId("tool-row");
  const button = screen.getByRole("button", { name: "Open transcript" });
  // Exactly one rendering, in the intent-line slot, outside the trigger.
  expect(screen.getAllByRole("button", { name: "Open transcript" })).toHaveLength(1);
  expect(screen.getByTestId("tool-row-intent-trailing").contains(button)).toBe(true);
  const trigger = screen.getByTestId("tool-row-trigger");
  const intent = screen.getByTestId("tool-row-intent");
  const chevron = screen.getByTestId("tool-row-chevron");
  expect(trigger.contains(button)).toBe(false);
  expect(trigger.contains(chevron)).toBe(false);
  // Valid sibling controls in binding visual order: intent text, Open,
  // aria-hidden chevron. The overlay trigger still owns disclosure semantics.
  expect(intent.compareDocumentPosition(button) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  expect(button.compareDocumentPosition(chevron) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  expect(chevron.getAttribute("aria-hidden")).toBe("true");
  expect(trigger.getAttribute("aria-expanded")).toBe("false");
  // No second line at all: nothing renders a summary element or summaryLine.
  expect(screen.queryByTestId("tool-row-summary")).toBeNull();
  expect(row.getAttribute("data-intent-trailing")).toBe("true");
  // The stylesheet keeps visible intent content, control, and chevron on one
  // line: the content's max-width reserves both trailing items and both gaps,
  // flex line-breaking is decided on hypothetical main sizes (the base size
  // CLAMPED by max-width), so a long intent wraps inside the trigger instead
  // of the control wrapping to its own line - the regression a layoutguard
  // geometry case (delegate-open-widget-inline) pins, since jsdom computes
  // no cascade and can't see the wrap.
  const css = rowCss();
  expect(css).toMatch(/\.row\[data-intent-trailing="true"\] \.intentTriggerContent\s*\{[^}]*flex:\s*0 1 auto/);
});

// Without an affordance an intent-only row changes shape not at all: no
// slot, no data attribute, and the (empty) summaryLine stays as it was.
test("an intent-only row with no affordance renders no intent-line trailing slot", () => {
  render(
    <ToolRow summary="" intent="Just a rationale." failed={false} expandable expanded={false} onToggle={() => {}} />,
  );
  expect(screen.queryByTestId("tool-row-intent-trailing")).toBeNull();
  expect(screen.getByTestId("tool-row").getAttribute("data-intent-trailing")).toBe(null);
});

// --- trailingAfter: the affordance rides INLINE mid-summary (read_file's
// "open beside" lands between the file name and the line range). The caller
// supplies the COMPLETE PREFIX of `summary` up to and including the anchor
// (never a bare substring) and ToolRow verifies it with summary.startsWith -
// never searches for it. A substring search is ambiguous whenever the
// anchor text recurs elsewhere in the summary, no matter which direction it
// searches from; requiring a literal, from-the-start prefix has no
// direction to be ambiguous in (kata ledger #97). A value that is not a
// literal prefix of `summary` keeps the default end-of-line placement (same
// "never a dead anchor" contract as summaryLink). ---------------------------------------

test("trailingAfter places the control between the anchor text and the meta on a collapsed intent-bearing row", () => {
  const summary = "Read cmd/evener-hub/frontend/src/widgets/sheet/sheet.test.tsx · lines 1-260";
  render(
    <ToolRow
      summary={summary}
      intent="Reviewing Sheet tests before adding size coverage"
      failed={false}
      expandable
      expanded={false}
      onToggle={() => {}}
      trailing={<button type="button">Open beside</button>}
      trailingAfter="Read cmd/evener-hub/frontend/src/widgets/sheet/sheet.test.tsx"
    />,
  );
  // A long path spans the truncation cut: head clamps, the path's visible
  // tail stays whole, the control follows it, and the line range trails last.
  const head = screen.getByTestId("tool-row-summary-head").textContent ?? "";
  const tail = screen.getByTestId("tool-row-summary-tail").textContent ?? "";
  const meta = screen.getByTestId("tool-row-summary-meta").textContent ?? "";
  expect(head + tail + meta).toBe(summary);
  expect(tail.endsWith("sheet.test.tsx")).toBe(true);
  expect(meta).toBe(" · lines 1-260");
  // Document order: path tail, control, meta.
  const summaryEl = screen.getByTestId("tool-row-summary");
  const children = Array.from(summaryEl.children);
  const tailEl = screen.getByTestId("tool-row-summary-tail");
  const trailingEl = screen.getByTestId("tool-row-trailing");
  const metaEl = screen.getByTestId("tool-row-summary-meta");
  expect(children.indexOf(tailEl)).toBeLessThan(children.indexOf(trailingEl));
  expect(children.indexOf(trailingEl)).toBeLessThan(children.indexOf(metaEl));
  expect(trailingEl.contains(screen.getByRole("button", { name: "Open beside" }))).toBe(true);
});

test("trailingAfter with a short path (anchor inside the clamped head) keeps the control right after the anchor", () => {
  const summary = "Read a.ts · lines 1-3";
  render(
    <ToolRow
      summary={summary}
      intent="Check the source"
      failed={false}
      expandable
      expanded={false}
      onToggle={() => {}}
      trailing={<button type="button">Open beside</button>}
      trailingAfter="Read a.ts"
    />,
  );
  const head = screen.getByTestId("tool-row-summary-head").textContent ?? "";
  const tail = screen.getByTestId("tool-row-summary-tail").textContent ?? "";
  expect(head).toBe("Read a.ts");
  expect(head + tail).toBe(summary);
  const children = Array.from(screen.getByTestId("tool-row-summary").children);
  expect(children.indexOf(screen.getByTestId("tool-row-summary-head"))).toBeLessThan(
    children.indexOf(screen.getByTestId("tool-row-trailing")),
  );
  expect(children.indexOf(screen.getByTestId("tool-row-trailing"))).toBeLessThan(
    children.indexOf(screen.getByTestId("tool-row-summary-tail")),
  );
});

test("trailingAfter on an expanded row splits the full summary around the control - no text lost, no clamp", () => {
  const summary = "Read src/a.ts · lines 1-3";
  render(
    <ToolRow
      summary={summary}
      intent="Check the source"
      failed={false}
      expandable
      expanded
      onToggle={() => {}}
      trailing={<button type="button">Open beside</button>}
      trailingAfter="Read src/a.ts"
    />,
  );
  // The control splits the text exactly at the anchor: the words are all
  // still there, once each, in order around it.
  expect(screen.queryByTestId("tool-row-summary-head")).toBe(null);
  const trailingEl = screen.getByTestId("tool-row-trailing");
  expect(trailingEl.previousSibling?.textContent).toBe("Read src/a.ts");
  expect(trailingEl.nextSibling?.textContent).toBe(" · lines 1-3");
  expect((trailingEl.previousSibling?.textContent ?? "") + (trailingEl.nextSibling?.textContent ?? "")).toBe(summary);
});

test("a trailingAfter anchor NOT present at all in the summary falls back to the end placement", () => {
  render(
    <ToolRow
      summary="Read a.ts · lines 1-3"
      intent="Check the source"
      failed={false}
      expandable
      expanded={false}
      onToggle={() => {}}
      trailing={<button type="button">Open beside</button>}
      trailingAfter="somewhere/else.ts"
    />,
  );
  expect(screen.queryByTestId("tool-row-trailing")).toBe(null);
  expect(screen.queryByTestId("tool-row-summary-meta")).toBe(null);
  // The control still renders, after the whole summary text.
  const summaryEl = screen.getByTestId("tool-row-summary");
  expect(summaryEl.contains(screen.getByRole("button", { name: "Open beside" }))).toBe(true);
  expect(summaryEl.lastElementChild?.contains(screen.getByRole("button", { name: "Open beside" }))).toBe(true);
});

// The contract is startsWith, not "appears somewhere" - an anchor that IS
// literally present in the summary but NOT as a from-the-start prefix (e.g.
// the caller passes only the bare target, mid-string) must fall back too,
// exactly like an absent anchor. Accepting "present anywhere" is what made
// indexOf/lastIndexOf substring search ambiguous in the first place.
test("a trailingAfter anchor that is present but NOT a prefix of the summary also falls back to the end placement", () => {
  render(
    <ToolRow
      summary="Read a.ts · lines 1-3"
      intent="Check the source"
      failed={false}
      expandable
      expanded={false}
      onToggle={() => {}}
      trailing={<button type="button">Open beside</button>}
      trailingAfter="a.ts"
    />,
  );
  expect(screen.queryByTestId("tool-row-trailing")).toBe(null);
  expect(screen.queryByTestId("tool-row-summary-meta")).toBe(null);
  const summaryEl = screen.getByTestId("tool-row-summary");
  expect(summaryEl.lastElementChild?.contains(screen.getByRole("button", { name: "Open beside" }))).toBe(true);
});

// --- kata ledger #97: a substring search is ambiguous from EITHER
// direction - the real target can be the FIRST occurrence of the anchor
// text with a coincidental match later, or the LAST occurrence with a
// coincidental match earlier. Both scenarios below use the bare, ambiguous
// target text a caller might pass and prove the row does not land the
// control on the WRONG occurrence for either - it safely falls back to the
// end placement instead, exactly like any other anchor that isn't a real
// prefix. ---------------------------------------------------------------

// read_file's own summary format always contains the literal word "lines"
// in its meta suffix (readLineRange, fsTools.tsx). A file bare-named
// "lines" makes the anchor text recur LATER in the string than the real
// target, right after "Read ".
test("an ambiguous bare anchor that also recurs LATER in the summary (the meta-suffix collision) does not land on the later, wrong occurrence", () => {
  const summary = "Read lines · lines 1-25";
  render(
    <ToolRow
      summary={summary}
      intent="Check the source"
      failed={false}
      expandable
      expanded={false}
      onToggle={() => {}}
      trailing={<button type="button">Open beside</button>}
      trailingAfter="lines"
    />,
  );
  // Must NOT split mid-meta: the control must not land between the meta's
  // "lines" and "1-25".
  expect(screen.queryByTestId("tool-row-summary-meta")).toBe(null);
  expect(screen.queryByTestId("tool-row-trailing")).toBe(null);
  const summaryEl = screen.getByTestId("tool-row-summary");
  expect(summaryEl.lastElementChild?.contains(screen.getByRole("button", { name: "Open beside" }))).toBe(true);
});

// From the opposite direction: the real target is the EARLIER occurrence,
// and a coincidental match (a backup filename that happens to start with
// the same text) recurs later. The split must not anchor on the later,
// coincidental match instead of the real, earlier target.
test("an ambiguous bare anchor whose real target is the EARLIER occurrence does not land on a later coincidental match", () => {
  const summary = "Read a.ts and backed up a.ts.bak · lines 1-3";
  render(
    <ToolRow
      summary={summary}
      intent="Check the source"
      failed={false}
      expandable
      expanded={false}
      onToggle={() => {}}
      trailing={<button type="button">Open beside</button>}
      trailingAfter="a.ts"
    />,
  );
  // Must NOT split after the coincidental "a.ts" inside "a.ts.bak".
  expect(screen.queryByTestId("tool-row-summary-meta")).toBe(null);
  expect(screen.queryByTestId("tool-row-trailing")).toBe(null);
  const summaryEl = screen.getByTestId("tool-row-summary");
  expect(summaryEl.lastElementChild?.contains(screen.getByRole("button", { name: "Open beside" }))).toBe(true);
});

// The affirmative case for both collisions above: when the caller supplies
// the CORRECT, unambiguous full prefix (what fsTools.tsx's openBesideInline
// supplies) instead of the bare target, the control lands exactly right
// even though the bare target text recurs elsewhere in the summary.
test("the complete prefix anchors correctly even when the bare target text recurs elsewhere in the summary", () => {
  const summary = "Read lines · lines 1-25";
  render(
    <ToolRow
      summary={summary}
      intent="Check the source"
      failed={false}
      expandable
      expanded
      onToggle={() => {}}
      trailing={<button type="button">Open beside</button>}
      trailingAfter="Read lines"
    />,
  );
  const trailingEl = screen.getByTestId("tool-row-trailing");
  expect(trailingEl.previousSibling?.textContent).toBe("Read lines");
  expect(trailingEl.nextSibling?.textContent).toBe(" · lines 1-25");
});

// Associativity rhythm (Jesse's review call): the gap between a rationale
// and the call it executes must read TIGHTER than the gap between separate
// calls - 16px outside (.call padding), and inside now NO row gap at all plus
// the title line-height on both lines, so the inter-line gap is only the two
// tightened half-leadings. Before this the inside gap was a 4px row-gap over
// the body's 1.5 line-height, which read nearly as loose as a separate call.
test("the rationale-to-call gap is tightened to line-leading only, still tighter than the gap between calls", () => {
  const css = rowCss();
  expect(css).toMatch(/\.row\s*\{[^}]*row-gap:\s*0/);
  expect(css).toMatch(/\.intent\s*\{[^}]*line-height:\s*var\(--line-height-title\)/);
  expect(css).toMatch(/\.demoted\s*\{[^}]*line-height:\s*var\(--line-height-title\)/);
  const call = /\.call\s*\{([^}]*)\}/.exec(css);
  expect(call).not.toBeNull();
  expect(call![1]).toContain("padding: var(--rhythm-item) 0");
});

// The intent is the agent's stated rationale for the call - commentary on
// the machine text, set off in italics rather than a colour or size of its
// own (Jesse's review call on the tiered-density follow-up).
test("the stated intent renders in italics", () => {
  expect(rowCss()).toMatch(/\.intent\s*\{[^}]*font-style:\s*italic/);
});

// kata rdry: the demoted line is a tool-RESULT ("Wrote fizzbuzz.py"), not a
// placeholder/disabled/timestamp - --ink-low's documented job (design-system.md).
// --ink-low clears AA since the 2026-09-06 raise (4.72:1 dark / 4.76:1 light
// on --surface-1), but text a reader reads takes --ink-mid (6.51 / 5.84).
test("the demoted summary line is readable text (--ink-mid), not the placeholder --ink-low", () => {
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

// --- kata xw3t: summaryLink linkifies a URL embedded in the row's own
// summary text - the collapsed-row counterpart to tcp9's expanded-body link
// on web_fetch (tcp9 deliberately left this surface inert; toolRenderers.ts's
// own summaryLink field doc explains why a parallel field, not a widened
// summary() return type). Same http(s)-only/target/rel idiom throughout. ----

test("a summaryLink matching text inside summary() renders as a real link, same target/rel idiom as tcp9's expanded-body link", () => {
  render(
    <ToolRow
      summary="Fetched https://example.com/page · 4096 bytes"
      summaryLink="https://example.com/page"
      failed={false}
      expandable={false}
      expanded={false}
    />,
  );
  const link = screen.getByRole("link", { name: "https://example.com/page" }) as HTMLAnchorElement;
  expect(link.getAttribute("href")).toBe("https://example.com/page");
  expect(link.getAttribute("target")).toBe("_blank");
  expect(link.getAttribute("rel")).toBe("noopener noreferrer");
  // No text lost or duplicated splitting the summary around the link.
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("Fetched https://example.com/page · 4096 bytes");
});

test("a summaryLink NOT literally present in summary() renders plain text - never a fabricated or mismatched link", () => {
  render(
    <ToolRow
      summary="Fetched (no url) · 8 bytes"
      summaryLink="https://example.com"
      failed={false}
      expandable={false}
      expanded={false}
    />,
  );
  expect(screen.queryByRole("link")).toBeNull();
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("Fetched (no url) · 8 bytes");
});

test("no summaryLink means the summary renders exactly as before - every descriptor but web_fetch, today", () => {
  render(<ToolRow summary="Ran ls" failed={false} expandable={false} expanded={false} />);
  expect(screen.queryByRole("link")).toBeNull();
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("Ran ls");
});

// The clamped state middle-truncates on raw character position (ToolRow's
// own middleSplit) and can cut a URL mid-way, or split it across the two
// independently-ellipsis-clamped head/tail spans - there is no sound "which
// half is clickable" answer there, so the collapsed+intent state stays
// plain text; opening the row (one click, the same chevron already on the
// row) shows the summary in full, with the link.
test("a collapsed row WITH an intent keeps the clamped plain-text split - no link inside the ellipsis-truncated head/tail", () => {
  render(
    <ToolRow
      summary="Fetched https://example.com/page · 4096 bytes"
      summaryLink="https://example.com/page"
      intent="Read the docs"
      failed={false}
      expandable
      expanded={false}
      onToggle={() => {}}
    />,
  );
  expect(screen.queryByRole("link")).toBeNull();
  expect(screen.getByTestId("tool-row-summary-head")).toBeTruthy();
});

test("the SAME intent-bearing row, expanded, drops the clamp and shows the real link", () => {
  render(
    <ToolRow
      summary="Fetched https://example.com/page · 4096 bytes"
      summaryLink="https://example.com/page"
      intent="Read the docs"
      failed={false}
      expandable
      expanded
      onToggle={() => {}}
    />,
  );
  const link = screen.getByRole("link", { name: "https://example.com/page" }) as HTMLAnchorElement;
  expect(link.getAttribute("target")).toBe("_blank");
  expect(link.getAttribute("rel")).toBe("noopener noreferrer");
});

// The trailing-affordance anchor had to become a positional prefix because a
// searched one could place the CONTROL at a coincidental occurrence - a
// different spot in the line, a different meaning (kata ledger #97, the three
// collision tests above). summaryLink's anchor is not that shape: it is the
// complete href, so every occurrence is the same characters denoting the same
// target and there is no wrong one to land on. What the search does owe is
// that it marks up ONE of them and leaves the visible text byte-identical.
test("a summaryLink that recurs in the summary links the first occurrence only, leaving the text intact", () => {
  const url = "https://example.com/page";
  const summary = `Fetched ${url} · redirected from ${url}`;
  render(<ToolRow summary={summary} summaryLink={url} failed={false} expandable expanded onToggle={() => {}} />);
  const links = screen.getAllByRole("link");
  expect(links).toHaveLength(1);
  expect(links[0]?.getAttribute("href")).toBe(url);
  // Positional pin: the anchor sits after the lead-in word, with the SECOND
  // occurrence still in the plain text that follows it.
  expect(links[0]?.previousSibling?.textContent).toBe("Fetched ");
  expect(links[0]?.nextSibling?.textContent).toBe(` · redirected from ${url}`);
  expect(screen.getByTestId("tool-row-summary").textContent).toBe(summary);
});

// The summary link and the disclosure trigger are siblings, so the link's
// pointer activation cannot reach the trigger at all.
test("clicking the linkified URL does not toggle the sibling disclosure trigger", () => {
  const onToggle = vi.fn();
  render(
    <ToolRow
      summary="Fetched https://example.com/page · 4096 bytes"
      summaryLink="https://example.com/page"
      failed={false}
      expandable
      expanded={false}
      onToggle={onToggle}
    />,
  );
  fireEvent.click(screen.getByRole("link"));
  expect(onToggle).not.toHaveBeenCalled();
  // Clicking anywhere else on the row still toggles, unaffected.
  fireEvent.click(screen.getByTestId("tool-row-trigger"));
  expect(onToggle).toHaveBeenCalledTimes(1);
});

// #93: an expanded row whose descriptor hides the summary while open (shell's
// summaryHiddenWhenExpanded) and carries no intent either renders NOTHING but
// the aria-hidden chevron inside the disclosure trigger - the disclosure has
// no accessible name at all. The fix must not resurrect the hidden summary text
// (that suppression is deliberate, ToolCallItem.tsx:259); it needs a stable
// label of its own.
test("an expanded summary-less, intent-less row's disclosure trigger still has a nonempty accessible name", () => {
  render(<ToolRow summary="" failed={false} expandable expanded onToggle={() => {}} />);
  const trigger = screen.getByTestId("tool-row-trigger");
  expect(trigger.tagName).toBe("BUTTON");
  expect((trigger.getAttribute("aria-label") ?? "").trim()).not.toBe("");
});

test("a failed summary-less row keeps its failure name on the sibling trigger", () => {
  render(<ToolRow summary="" failed expandable expanded onToggle={() => {}} />);
  expect(screen.getByTestId("tool-row-trigger").getAttribute("aria-label")).toBe("Failed");
  expect(screen.getByRole("img", { name: "Failed" })).toBeTruthy();
});

test("a status-bearing summary-less row keeps the status name outside the trigger", () => {
  render(
    <ToolRow
      summary=""
      failed={false}
      status={
        <span role="img" aria-label="Working">
          ●
        </span>
      }
      expandable
      expanded
      onToggle={() => {}}
    />,
  );
  expect(screen.getByTestId("tool-row-trigger").getAttribute("aria-label")).toBe("Tool call");
  expect(screen.getByRole("img", { name: "Working" })).toBeTruthy();
});

test("a failed and status-bearing row keeps both names outside the trigger", () => {
  render(
    <ToolRow
      summary=""
      failed
      status={
        <span role="img" aria-label="Working">
          ●
        </span>
      }
      expandable
      expanded
      onToggle={() => {}}
    />,
  );
  expect(screen.getByTestId("tool-row-trigger").getAttribute("aria-label")).toBe("Failed");
  expect(screen.getByRole("img", { name: "Failed" })).toBeTruthy();
  expect(screen.getByRole("img", { name: "Working" })).toBeTruthy();
});

// `status` is typed ReactNode, which admits values that carry no accessible
// name at all - null, false, an empty string - not just a real status node.
// Suppressing the fallback on `status !== undefined` alone treats any of
// those as "status present" and leaves the disclosure with no accessible
// name: no aria-label (suppressed) and no descendant name (nothing renders
// one). The gate must reject nameless status values, not just absent ones.
test("a null status does not suppress the fallback label - the disclosure is never left unnamed", () => {
  render(<ToolRow summary="" failed={false} status={null} expandable expanded onToggle={() => {}} />);
  expect((screen.getByTestId("tool-row-trigger").getAttribute("aria-label") ?? "").trim()).not.toBe("");
});

test("a false status does not suppress the fallback label - the disclosure is never left unnamed", () => {
  render(<ToolRow summary="" failed={false} status={false} expandable expanded onToggle={() => {}} />);
  expect((screen.getByTestId("tool-row-trigger").getAttribute("aria-label") ?? "").trim()).not.toBe("");
});

// The mechanism-level ToolRow tests above prove the row CAN linkify a
// summaryLink; this proves ToolCallItem actually THREADS a descriptor's
// summaryLink through to it - a wiring bug (the prop never passed) would
// pass every test above and still ship a plain-text collapsed row.
test("ToolCallItem threads a descriptor's summaryLink through to the row, not only a ToolRow-level contract", () => {
  registerToolRenderer({
    match: "trg_summarylink",
    summary: () => "Fetched https://example.com/page · 4096 bytes",
    summaryLink: () => "https://example.com/page",
    body: () => <div>b</div>,
  });
  render(<ToolCallItem item={item({ toolName: "trg_summarylink" })} turn={turn} live={false} />);
  const link = screen.getByRole("link", { name: "https://example.com/page" }) as HTMLAnchorElement;
  expect(link.getAttribute("target")).toBe("_blank");
  expect(link.getAttribute("rel")).toBe("noopener noreferrer");
});

// Mirrors the summaryLink threading test above: the descriptor declares DATA,
// ToolCallItem owns the control - a wiring bug (ref read but never rendered,
// or rendered without the parent ref) must fail here, not in production.
test("ToolCallItem threads a descriptor's openTranscriptRef to a working OpenTranscriptButton", async () => {
  await import("../"); // pane registrations, same harness as openTranscript.test.tsx's beforeAll
  resetWorkspaceStoreForTests();
  registerToolRenderer({
    match: "trg_opentranscript",
    summary: () => "Sent a message to delegate dlg_x",
    openTranscriptRef: () => "local:child",
    body: () => <div>b</div>,
  });
  render(
    <ToolCallItem item={item({ toolName: "trg_opentranscript" })} turn={turn} live={false} sessionRef="local:owner" />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Open transcript" }));
  const panes = workspaceStore
    .getState()
    .panes.filter((pane) => pane.type === "transcript" && (pane.params as { ref?: unknown }).ref === "local:child");
  expect(panes).toHaveLength(1);
  expect(panes[0]?.params).toEqual({ ref: "local:child", parentRef: "local:owner" });
});

test("ToolCallItem renders no transcript button when the descriptor has no openTranscriptRef", () => {
  registerToolRenderer({
    match: "trg_no_opentranscript",
    summary: () => "plain",
    body: () => <div>b</div>,
  });
  render(<ToolCallItem item={item({ toolName: "trg_no_opentranscript" })} turn={turn} live={false} />);
  expect(screen.queryByRole("button", { name: "Open transcript" })).toBeNull();
});

// --- two-level disclosure: intent button controls summaryOpen, body chevron
//     controls expanded -----------------------------------------------------
//
// When onToggleSummary is provided, ToolRow splits its single disclosure into
// two: the intent button toggles `summaryOpen` (whether the summary line is
// shown), and a separate `.bodyTrigger` chevron button toggles `expanded` (the
// body below). Intent-less rows are unchanged - the overlay pattern keeps one
// trigger controlling the body. The opt-in is the prop itself: callers that
// do not pass onToggleSummary get the legacy single-level behavior exactly.

test("two-level: summaryOpen=false expanded=false renders only the intent button, aria-expanded=false", () => {
  render(
    <ToolRow
      summary="npm test -- src/foo"
      intent="Running the foo tests"
      failed={false}
      expandable
      expanded={false}
      onToggle={() => {}}
      summaryOpen={false}
      onToggleSummary={() => {}}
    />,
  );
  const trigger = screen.getByTestId("tool-row-trigger");
  // The intent button controls the summary, so it reflects summaryOpen.
  expect(trigger.getAttribute("aria-expanded")).toBe("false");
  // No summary line and no body chevron render yet.
  expect(screen.queryByTestId("tool-row-summary")).toBeNull();
  expect(screen.queryByTestId("tool-row-body-trigger")).toBeNull();
});

test("two-level: summaryOpen=true expanded=false renders the summary line and body chevron, intent expanded, body collapsed", () => {
  render(
    <ToolRow
      summary="npm test -- src/foo"
      intent="Running the foo tests"
      failed={false}
      expandable
      expanded={false}
      onToggle={() => {}}
      summaryOpen
      onToggleSummary={() => {}}
    />,
  );
  const trigger = screen.getByTestId("tool-row-trigger");
  expect(trigger.getAttribute("aria-expanded")).toBe("true");
  // The summary line renders...
  expect(screen.getByTestId("tool-row-summary")).toBeTruthy();
  // ...and the body chevron renders, collapsed.
  const bodyTrigger = screen.getByTestId("tool-row-body-trigger");
  expect(bodyTrigger.getAttribute("aria-expanded")).toBe("false");
});

test("two-level: summaryOpen=true expanded=true renders summary line and body chevron, both expanded", () => {
  render(
    <ToolRow
      summary="npm test -- src/foo"
      intent="Running the foo tests"
      failed={false}
      expandable
      expanded
      onToggle={() => {}}
      summaryOpen
      onToggleSummary={() => {}}
    />,
  );
  const trigger = screen.getByTestId("tool-row-trigger");
  expect(trigger.getAttribute("aria-expanded")).toBe("true");
  const bodyTrigger = screen.getByTestId("tool-row-body-trigger");
  expect(bodyTrigger.getAttribute("aria-expanded")).toBe("true");
  expect(screen.getByTestId("tool-row-summary")).toBeTruthy();
});

test("two-level: summaryOpen=false expanded=true (auto-expand) puts the body chevron on the intent line with data-intent-trailing=true", () => {
  render(
    <ToolRow
      summary="npm test -- src/foo"
      intent="Running the foo tests"
      failed={false}
      expandable
      expanded
      onToggle={() => {}}
      summaryOpen={false}
      onToggleSummary={() => {}}
    />,
  );
  const row = screen.getByTestId("tool-row");
  // The summary is hidden, so the body chevron shares the intent line.
  expect(row.getAttribute("data-intent-trailing")).toBe("true");
  expect(screen.queryByTestId("tool-row-summary")).toBeNull();
  const bodyTrigger = screen.getByTestId("tool-row-body-trigger");
  expect(bodyTrigger.getAttribute("aria-expanded")).toBe("true");
  // The intent button reports summaryOpen=false (summary not visible).
  const trigger = screen.getByTestId("tool-row-trigger");
  expect(trigger.getAttribute("aria-expanded")).toBe("false");
});

test("two-level: clicking the intent button calls onToggleSummary, not onToggle", async () => {
  const user = userEvent.setup();
  const onToggle = vi.fn();
  const onToggleSummary = vi.fn();
  render(
    <ToolRow
      summary="npm test -- src/foo"
      intent="Running the foo tests"
      failed={false}
      expandable
      expanded={false}
      onToggle={onToggle}
      summaryOpen={false}
      onToggleSummary={onToggleSummary}
    />,
  );
  await user.click(screen.getByTestId("tool-row-trigger"));
  expect(onToggleSummary).toHaveBeenCalledTimes(1);
  expect(onToggle).not.toHaveBeenCalled();
});

test("two-level: clicking the body chevron calls onToggle, not onToggleSummary", async () => {
  const user = userEvent.setup();
  const onToggle = vi.fn();
  const onToggleSummary = vi.fn();
  render(
    <ToolRow
      summary="npm test -- src/foo"
      intent="Running the foo tests"
      failed={false}
      expandable
      expanded={false}
      onToggle={onToggle}
      summaryOpen
      onToggleSummary={onToggleSummary}
    />,
  );
  await user.click(screen.getByTestId("tool-row-body-trigger"));
  expect(onToggle).toHaveBeenCalledTimes(1);
  expect(onToggleSummary).not.toHaveBeenCalled();
});

test("two-level: summaryHidden hides the summary line while expanded, body chevron moves to intent line", () => {
  render(
    <ToolRow
      summary="npm test -- src/foo"
      intent="Running the foo tests"
      failed={false}
      expandable
      expanded
      onToggle={() => {}}
      summaryOpen
      onToggleSummary={() => {}}
      summaryHidden
    />,
  );
  // summaryVisible = summaryOpen && !summaryHidden = false, so the summary
  // line is gone and the intent button reports not-expanded.
  expect(screen.queryByTestId("tool-row-summary")).toBeNull();
  expect(screen.getByTestId("tool-row-trigger").getAttribute("aria-expanded")).toBe("false");
  const row = screen.getByTestId("tool-row");
  expect(row.getAttribute("data-intent-trailing")).toBe("true");
  // The body chevron still renders on the intent line, expanded.
  const bodyTrigger = screen.getByTestId("tool-row-body-trigger");
  expect(bodyTrigger.getAttribute("aria-expanded")).toBe("true");
});

test("two-level: the intent button controls the summary region via aria-controls", () => {
  render(
    <ToolRow
      summary="npm test -- src/foo"
      intent="Running the foo tests"
      failed={false}
      expandable
      expanded={false}
      onToggle={() => {}}
      summaryOpen
      onToggleSummary={() => {}}
    />,
  );
  const trigger = screen.getByTestId("tool-row-trigger");
  const summaryRegionId = trigger.getAttribute("aria-controls");
  expect(summaryRegionId).toBeTruthy();
  // The summary div carries that id.
  const summaryEl = screen.getByTestId("tool-row-summary");
  expect(summaryEl.closest("div")?.getAttribute("id")).toBe(summaryRegionId);
});

test("two-level: the body chevron controls the body region via aria-controls", () => {
  const bodyId = "body-region-1";
  render(
    <ToolRow
      summary="npm test -- src/foo"
      intent="Running the foo tests"
      failed={false}
      expandable
      expanded={false}
      onToggle={() => {}}
      bodyId={bodyId}
      summaryOpen
      onToggleSummary={() => {}}
    />,
  );
  const bodyTrigger = screen.getByTestId("tool-row-body-trigger");
  expect(bodyTrigger.getAttribute("aria-controls")).toBe(bodyId);
});

test("two-level: the body chevron has an accessible name from the summary label", () => {
  render(
    <ToolRow
      summary="npm test -- src/foo"
      intent="Running the foo tests"
      failed={false}
      expandable
      expanded={false}
      onToggle={() => {}}
      summaryOpen
      onToggleSummary={() => {}}
    />,
  );
  const bodyTrigger = screen.getByTestId("tool-row-body-trigger");
  expect((bodyTrigger.getAttribute("aria-label") ?? "").trim()).not.toBe("");
});

test("two-level: a failed row's body chevron label starts with 'Failed'", () => {
  render(
    <ToolRow
      summary="npm test -- src/foo"
      intent="Running the foo tests"
      failed
      expandable
      expanded={false}
      onToggle={() => {}}
      summaryOpen
      onToggleSummary={() => {}}
    />,
  );
  const bodyTrigger = screen.getByTestId("tool-row-body-trigger");
  expect(bodyTrigger.getAttribute("aria-label")).toBe("Failed npm test -- src/foo");
});

test("two-level: the summary line carries the chevron span inside the intent button, not a separate body chevron, when expanded and summary visible", () => {
  render(
    <ToolRow
      summary="npm test -- src/foo"
      intent="Running the foo tests"
      failed={false}
      expandable
      expanded
      onToggle={() => {}}
      summaryOpen
      onToggleSummary={() => {}}
    />,
  );
  // The intent button still carries the inline chevron (chrome)...
  expect(screen.getByTestId("tool-row-intent").contains(screen.getByTestId("tool-row-chevron"))).toBe(true);
  // ...and a separate body-trigger chevron renders too.
  expect(screen.getByTestId("tool-row-body-trigger")).toBeTruthy();
});

test("two-level: intent-less rows keep the unchanged overlay pattern - onToggleSummary is ignored", () => {
  const onToggle = vi.fn();
  const onToggleSummary = vi.fn();
  render(
    <ToolRow
      summary="npm test"
      failed={false}
      expandable
      expanded={false}
      onToggle={onToggle}
      summaryOpen
      onToggleSummary={onToggleSummary}
    />,
  );
  const trigger = screen.getByTestId("tool-row-trigger");
  // Intent-less rows keep the overlay: one trigger controls the body, aria-expanded=expanded.
  expect(trigger.getAttribute("aria-expanded")).toBe("false");
  // No separate body chevron renders for intent-less rows.
  expect(screen.queryByTestId("tool-row-body-trigger")).toBeNull();
});

test("two-level: an intent-less row's overlay trigger calls onToggle, not onToggleSummary", async () => {
  const user = userEvent.setup();
  const onToggle = vi.fn();
  const onToggleSummary = vi.fn();
  render(
    <ToolRow
      summary="npm test"
      failed={false}
      expandable
      expanded={false}
      onToggle={onToggle}
      summaryOpen
      onToggleSummary={onToggleSummary}
    />,
  );
  await user.click(screen.getByTestId("tool-row-trigger"));
  expect(onToggle).toHaveBeenCalledTimes(1);
  expect(onToggleSummary).not.toHaveBeenCalled();
});

test("two-level: without onToggleSummary, an intent row keeps the legacy single-level behavior", () => {
  // The opt-in is the prop: callers that do not pass onToggleSummary get the
  // old behavior - the intent button controls `expanded` directly.
  render(
    <ToolRow
      summary="npm test -- src/foo"
      intent="Running the foo tests"
      failed={false}
      expandable
      expanded={false}
      onToggle={() => {}}
    />,
  );
  const trigger = screen.getByTestId("tool-row-trigger");
  expect(trigger.getAttribute("aria-expanded")).toBe("false");
  // No separate body chevron renders in the legacy single-level mode.
  expect(screen.queryByTestId("tool-row-body-trigger")).toBeNull();
});

test("two-level: the summary div gets id={summaryRegionId} when rendered", () => {
  render(
    <ToolRow
      summary="npm test -- src/foo"
      intent="Running the foo tests"
      failed={false}
      expandable
      expanded={false}
      onToggle={() => {}}
      summaryOpen
      onToggleSummary={() => {}}
    />,
  );
  const trigger = screen.getByTestId("tool-row-trigger");
  const summaryRegionId = trigger.getAttribute("aria-controls");
  expect(summaryRegionId).toBeTruthy();
  // The summary line wrapper div carries the region id.
  const summaryLine = screen.getByTestId("tool-row-summary").parentElement;
  expect(summaryLine?.getAttribute("id")).toBe(summaryRegionId);
});

test("two-level: when summaryHidden the intent button drops aria-controls (no region to point at)", () => {
  render(
    <ToolRow
      summary="npm test -- src/foo"
      intent="Running the foo tests"
      failed={false}
      expandable
      expanded
      onToggle={() => {}}
      summaryOpen
      onToggleSummary={() => {}}
      summaryHidden
    />,
  );
  const trigger = screen.getByTestId("tool-row-trigger");
  // summaryVisible is false, so no summary region exists to control.
  expect(trigger.getAttribute("aria-expanded")).toBe("false");
  expect(trigger.getAttribute("aria-controls")).toBeFalsy();
});

test("two-level: the body chevron's chevron span rotates with expanded state", () => {
  const { rerender } = render(
    <ToolRow
      summary="npm test -- src/foo"
      intent="Running the foo tests"
      failed={false}
      expandable
      expanded={false}
      onToggle={() => {}}
      summaryOpen
      onToggleSummary={() => {}}
    />,
  );
  const bodyTrigger = screen.getByTestId("tool-row-body-trigger");
  const chevron = bodyTrigger.querySelector("[data-open]");
  expect(chevron?.getAttribute("data-open")).toBe("false");
  rerender(
    <ToolRow
      summary="npm test -- src/foo"
      intent="Running the foo tests"
      failed={false}
      expandable
      expanded
      onToggle={() => {}}
      summaryOpen
      onToggleSummary={() => {}}
    />,
  );
  const bodyTriggerOpen = screen.getByTestId("tool-row-body-trigger");
  const chevronOpen = bodyTriggerOpen.querySelector("[data-open]");
  expect(chevronOpen?.getAttribute("data-open")).toBe("true");
});

// --- the intent-trailing control and the clamp's clip ------------------------

const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "toolcallitem.module.css"), "utf8");

test("the intent-trailing content reserves Open and chevron instead of growing past them", () => {
  expect(css).toMatch(/\.row\[data-intent-trailing="true"\]\s+\.intentTriggerContent\s*\{[^}]*flex:\s*0 1 auto/);
  expect(css).toMatch(
    /\.row\[data-intent-trailing="true"\]\s+\.intentTriggerContent\s*\{[^}]*max-width:\s*calc\(100% - var\(--tap-min, 28px\) - 14px - var\(--space-2\) - var\(--space-2\)\)/,
  );
});

test("the collapsed summary line clips with a margin, so the open control's hit area survives", () => {
  expect(css).toMatch(/\.clamped\s*\{[^}]*overflow:\s*clip/);
  expect(css).toMatch(/\.clamped\s*\{[^}]*overflow-clip-margin:\s*16px/);
});

test("the intent-only overlay trigger describes itself with the row's status", () => {
  // The overlay branch renders the visible status as a SIBLING of the trigger
  // (valid DOM order text/Open/chevron); aria-describedby keeps the state
  // ("Working", "Needs you") announced on focus, as it was when the trigger
  // contained the status.
  render(
    <ToolRow
      summary=""
      intent="Delegate on the parser"
      failed={false}
      expandable
      expanded={false}
      onToggle={() => {}}
      status={<span>Working</span>}
      trailing={<button type="button" aria-label="Open transcript" />}
    />,
  );
  const trigger = screen.getByTestId("tool-row-trigger");
  const status = screen.getByTestId("tool-row-status");
  expect(status.id).not.toBe("");
  expect(trigger.getAttribute("aria-describedby")).toBe(status.id);
});

test("the body-trigger-on-intent-line row marks itself, and the stylesheet constrains the plain trigger", () => {
  // Two-level row with the summary hidden and the body expanded: the body
  // chevron rides the intent line. The plain trigger must keep a constrained
  // basis - the [data-intent] rule's flex: 1 1 100% would wrap the chevron
  // onto its own line - and the Open-plus-chevron reservation widens by one
  // more chevron and gap when both ride the line.
  render(
    <ToolRow
      summary="npm test -- src/foo"
      intent="Running the foo tests"
      failed={false}
      expandable
      expanded
      onToggle={() => {}}
      summaryOpen={false}
      onToggleSummary={() => {}}
    />,
  );
  const row = screen.getByTestId("tool-row");
  expect(row.getAttribute("data-intent-trailing")).toBe("true");
  expect(row.getAttribute("data-body-trigger-intent")).toBe("true");
  expect(css).toMatch(
    /\.row\[data-intent-trailing="true"\]\s*>\s*\.trigger:not\(\.intentOverlayTrigger\)\s*\{[^}]*flex:\s*0 1 auto/,
  );
  expect(css).toMatch(/\[data-body-trigger-intent="true"\]\s+\.intentTriggerContent\s*\{[^}]*max-width/);
});

test("a body chevron sharing the intent line is raised above the overlay trigger", () => {
  // Without its own layer the absolute overlay swallows the body trigger's
  // clicks and toggles the wrong disclosure (roborev).
  expect(css).toMatch(/\.row\[data-intent-trailing="true"\]\s*>\s*\.bodyTrigger\s*\{[^}]*z-index:\s*var\(--z-raised\)/);
});
