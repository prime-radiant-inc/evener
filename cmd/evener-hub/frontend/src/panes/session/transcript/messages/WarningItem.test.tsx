import type { AnyNotification, ItemModel, Thread, TurnModel } from "@evener/appwire-client";
import { applyNotification, hydrateThread, WarningCodeContextBudget } from "@evener/appwire-client";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { ignoringTurn, itemRendererFor } from "../types";
import { WarningItem } from "./WarningItem";

afterEach(cleanup);

const turn: TurnModel = { id: "turn_1", status: "completed", items: [] };

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "warning", text: "", ...overrides };
}

test('self-registers under the wire\'s warning item type ("warning"), exactly once', () => {
  expect(itemRendererFor("warning")).toBe(WarningItem);
});

test("is memoized ignoring turn identity - a fresh turn object on every streaming delta must not re-render an unrelated settled warning row", () => {
  expect(WarningItem.$$typeof).toBe(Symbol.for("react.memo"));
  expect((WarningItem as unknown as { compare: unknown }).compare).toBe(ignoringTurn);
});

test("a full payload (title, message, hint) renders all three", () => {
  render(
    <WarningItem
      item={item({
        text: "the sandbox blocked write access",
        warning: { title: "Sandbox blocked", hint: "retry with --sandbox off" },
      })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getByText("Sandbox blocked")).toBeTruthy();
  expect(screen.getByText("the sandbox blocked write access")).toBeTruthy();
  expect(screen.getByText("retry with --sandbox off")).toBeTruthy();
});

test("message-only (no title, no hint) still renders, with a generic label", () => {
  render(<WarningItem item={item({ text: "something concerning happened" })} turn={turn} live={false} />);
  expect(screen.getByText("something concerning happened")).toBeTruthy();
  expect(screen.getByText("Warning")).toBeTruthy(); // generic fallback label, no title given
  expect(screen.queryByTestId("warning-hint")).toBeNull();
});

test("title-only (no message, no hint) renders the title with no message/hint lines", () => {
  render(<WarningItem item={item({ text: "", warning: { title: "Heads up" } })} turn={turn} live={false} />);
  expect(screen.getByText("Heads up")).toBeTruthy();
  expect(screen.queryByTestId("warning-message")).toBeNull();
  expect(screen.queryByTestId("warning-hint")).toBeNull();
});

test("nothing at all (no title, no text, no hint) renders nothing", () => {
  const { container } = render(<WarningItem item={item({ text: "" })} turn={turn} live={false} />);
  expect(container.firstChild).toBeNull();
});

test("whitespace-only title/hint counts as absent, the same reading hasWarningText gives item.text's raw-frame fallback", () => {
  // A frame whose title/hint are whitespace-only carries no real message
  // either (the reducer's own hasWarningText check treats them as absent
  // too, so item.text falls back to the raw frame JSON — see reducer.ts's
  // warning fold). Both readings must agree: the blank title/hint never
  // renders alongside the raw-frame text.
  render(
    <WarningItem
      item={item({ text: '{"threadId":"thr_t"}', warning: { title: "   ", hint: "  " } })}
      turn={turn}
      live={false}
    />,
  );
  // Blank strings are not real content: the generic "Warning" label stands
  // in for the blank title, and no hint line renders — matching
  // hasWarningText's trim check instead of WarningItem's own truthiness.
  expect(screen.getByText("Warning")).toBeTruthy();
  expect(screen.getByText('{"threadId":"thr_t"}')).toBeTruthy();
  expect(screen.queryByTestId("warning-hint")).toBeNull();
});

test("an object-valued title/hint (a malformed wire frame bypassing ItemModel's string type) never reaches the DOM as a React child", () => {
  const objectTitle = { nested: "object" } as unknown as string;
  const arrayHint = ["a", "b"] as unknown as string;
  expect(() =>
    render(
      <WarningItem
        item={item({ text: "something happened", warning: { title: objectTitle, hint: arrayHint } })}
        turn={turn}
        live={false}
      />,
    ),
  ).not.toThrow();
  // Falls back to the generic label instead of stringifying/crashing on the
  // non-string value.
  expect(screen.getByText("Warning")).toBeTruthy();
  expect(screen.queryByTestId("warning-hint")).toBeNull();
});

// Whitespace is not content: a title of spaces or a hint of newlines would
// otherwise render a row whose text a reader sees as blank — the reading the
// phone's projector already takes (mobile/src/conversation/project.ts's warning
// branch filters parts on trimmed content).
test.each([
  ["a whitespace title", { text: "   ", warning: { title: "  " } }],
  ["a whitespace hint", { text: " ", warning: { hint: "\n\t" } }],
  ["whitespace everywhere", { text: "\n", warning: { title: " ", hint: "  " } }],
])("%s renders nothing", (_case, overrides) => {
  const { container } = render(<WarningItem item={item(overrides)} turn={turn} live={false} />);
  expect(container.firstChild).toBeNull();
});

// End to end for the frame a malformed producer sends: a `warning` notification
// that carries no message anywhere folds to an item whose text is the frame
// itself (reducer.ts's rawWarningFrame — appwire/warning.go's DecodeWarningParams
// contract: a malformed warning stays visible, not silent), and this renderer
// shows that row rather than nothing — the reader sees `{"threadId":"…","ref":"…"}`,
// never prose, but never a blank bubble either.
test("a warning frame with no message anywhere renders the frame itself", () => {
  const thread = {
    id: "thr_1",
    sessionId: "sess_1",
    preview: "",
    ephemeral: false,
    modelProvider: "anthropic",
    createdAt: 0,
    updatedAt: 0,
    status: { type: "idle" },
    cwd: "",
    cliVersion: "",
    source: "",
    turns: [{ id: "turn_1", status: "inProgress", itemsView: "default", items: [] }],
    evener: { ref: "ref_1", activeTurnId: "turn_1" },
  } as unknown as Thread;
  const params = { threadId: "thr_1", ref: "ref_1" };
  const folded = applyNotification(
    hydrateThread({ thread }, "ref_1", 0),
    { method: "warning", params } as AnyNotification,
    1_000,
  );
  const warning = folded.turns[0]?.items[0];
  if (warning === undefined) throw new Error("the fold produced no warning item");
  expect(warning.text).toBe(JSON.stringify(params));

  const { container } = render(<WarningItem item={warning} turn={folded.turns[0] as TurnModel} live={false} />);
  expect(container.firstChild).not.toBeNull();
  expect(container.textContent).toContain(JSON.stringify(params));
});

// A malformed wire frame can carry non-string title/hint values (the daemon's
// own contract only promises "visible, not silent" for the message text —
// title/hint ride an untyped warning param map). This renderer must not throw
// when the wire sends something other than a string or undefined for either.
test.each([
  ["a numeric title", { text: "body", warning: { title: 42 as unknown as string } }],
  ["a null hint", { text: "body", warning: { hint: null as unknown as string } }],
  ["an object title", { text: "body", warning: { title: { nested: true } as unknown as string } }],
])("%s does not throw while rendering", (_case, overrides) => {
  expect(() => render(<WarningItem item={item(overrides)} turn={turn} live={false} />)).not.toThrow();
});

// An informational warning (a coded "no action needed" notice, the projector
// only lets through at high verbosity) renders as ONE quiet line instead of
// the attention-chip block: no chip, no separate hint row, the message as the
// line's text, the hint reachable on the line's hover title.
test("an informational warning renders one quiet line with the hint on the hover title", () => {
  render(
    <WarningItem
      item={item({
        text: "Output allocation reduced for inst/model: requested=100 admitted=50",
        warning: {
          title: "Context budget",
          hint: "The model's output allocation was reduced to fit its context window. No action needed.",
          code: WarningCodeContextBudget,
        },
      })}
      turn={turn}
      live={false}
    />,
  );
  const line = screen.getByTestId("warning-quiet-line");
  expect(line.textContent).toBe("Output allocation reduced for inst/model: requested=100 admitted=50");
  expect(screen.queryByText("Context budget")).toBeNull(); // no chip label
  expect(line.getAttribute("title")).toContain("No action needed");
});

test("an informational warning with no message renders its hint as the quiet line", () => {
  render(
    <WarningItem
      item={item({
        text: "",
        warning: {
          title: "Context budget",
          hint: "Compaction will manage the window.",
          code: WarningCodeContextBudget,
        },
      })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getByTestId("warning-quiet-line").textContent).toBe("Compaction will manage the window.");
});

test("an actionable (uncoded) warning keeps the attention-chip rendering", () => {
  render(
    <WarningItem
      item={item({
        text: "provider degraded",
        warning: { title: "Provider", hint: "check the provider status" },
      })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getByText("Provider")).toBeTruthy();
  expect(screen.getByText("provider degraded")).toBeTruthy();
  expect(screen.queryByTestId("warning-quiet-line")).toBeNull();
});
