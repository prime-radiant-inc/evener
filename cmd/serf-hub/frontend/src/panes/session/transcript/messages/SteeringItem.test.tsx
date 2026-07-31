import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { lazy } from "react";
import { afterEach, expect, test } from "vitest";
import type { ItemModel, TurnModel } from "../../../../protocol/model";
import { registerPane } from "../../../../shell/paneRegistry";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../../../shell/workspace";
import { resetDisclosureStoreForTests } from "../../../../widgets/disclosure/disclosureStore";
import { ignoringTurn, itemRendererFor } from "../types";
import { SteeringItem } from "./SteeringItem";

afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
  resetWorkspaceStoreForTests();
});

registerPane({
  id: "session",
  title: () => "test session",
  component: lazy(() => Promise.resolve({ default: () => null })),
});

const turn: TurnModel = { id: "turn_1", status: "completed", items: [] };

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "steering", text: "", ...overrides };
}

test('self-registers under the wire\'s steering item type ("steering")', () => {
  expect(itemRendererFor("steering")).toBe(SteeringItem);
});

test("is memoized ignoring turn identity - a fresh turn object on every streaming delta must not re-render an unrelated settled steering row", () => {
  expect(SteeringItem.$$typeof).toBe(Symbol.for("react.memo"));
  expect((SteeringItem as unknown as { compare: unknown }).compare).toBe(ignoringTurn);
});

// --- source: "user" -> a normal user message, never the divider ------------
// Parity issue #24 (internal/appprojector/appwire_projection.go:588,
// internal/apptranscript/apptranscript.go:225-228): steering the human
// typed themselves is indistinguishable from a normal prompt.

test('source "user" renders as a normal user message bubble (the "You" tag), not the divider', () => {
  render(<SteeringItem item={item({ text: "focus on the tests", source: "user" })} turn={turn} live={false} />);
  expect(screen.getByTestId("user-message-item")).toBeTruthy();
  expect(screen.getByText("You")).toBeTruthy();
  expect(screen.getByText("focus on the tests")).toBeTruthy();
  expect(screen.queryByTestId("steering-item")).toBeNull();
});

test('source "user" steering with images renders the same gallery thumbnails a real user message would', () => {
  render(
    <SteeringItem
      item={item({ text: "look", source: "user", images: [{ src: "a" }, { src: "b" }] })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getAllByTestId("image-gallery-thumb")).toHaveLength(2);
});

// A user steer lands MID-turn, interrupting work already under way rather than
// starting new work, so it reuses the prompt's look without claiming the
// exchange boundary a prompt marks. Asserted here, at the branch SteeringItem
// actually takes, not only at UserMessageView's own opensExchange prop.
test('source "user" steering does NOT open an exchange - it interrupts one', () => {
  render(<SteeringItem item={item({ text: "actually, stop", source: "user" })} turn={turn} live={false} />);
  expect(screen.getByTestId("user-message-item").getAttribute("data-opens-exchange")).toBeNull();
});

// --- daemon-sourced (no source, or any non-"user" source) -> quiet divider --

test("no source at all renders the collapsible steering divider, not a user bubble", () => {
  render(<SteeringItem item={item({ text: "<SYSTEM-REMINDER>nudge</SYSTEM-REMINDER>" })} turn={turn} live={false} />);
  expect(screen.getByTestId("steering-item")).toBeTruthy();
  expect(screen.queryByTestId("user-message-item")).toBeNull();
});

test("the divider is collapsed by default and expands to show the verbatim text", () => {
  render(<SteeringItem item={item({ text: "the raw steering body" })} turn={turn} live={false} />);
  const details = screen.getByTestId("steering-item") as HTMLDetailsElement;
  expect(details.open).toBe(false);
  expect(details.textContent).toContain("the raw steering body");
});

// yt2q: the steering divider's open/closed state lives in the shared
// disclosureStore keyed by item.id, so expanding it survives a remount.
test("an expanded steering divider stays open across an unmount+remount with the same item id (store-backed)", () => {
  const steer = item({ id: "item_steer_remount", text: "the raw steering body" });
  const { unmount } = render(<SteeringItem item={steer} turn={turn} live={false} />);
  const details = screen.getByTestId("steering-item") as HTMLDetailsElement;
  expect(details.open).toBe(false);
  fireEvent.click(details.querySelector("summary")!);
  expect((screen.getByTestId("steering-item") as HTMLDetailsElement).open).toBe(true);

  unmount();
  render(<SteeringItem item={steer} turn={turn} live={false} />);
  expect((screen.getByTestId("steering-item") as HTMLDetailsElement).open).toBe(true);
});

test("the same steering item id has independent disclosure state in different sessions", () => {
  const shared = item({ id: "same_item", text: "the raw steering body" });
  render(
    <>
      <SteeringItem item={shared} turn={turn} live={false} sessionRef="session_a" />
      <SteeringItem item={shared} turn={turn} live={false} sessionRef="session_b" />
    </>,
  );

  const dividers = screen.getAllByTestId("steering-item") as HTMLDetailsElement[];
  expect(dividers).toHaveLength(2);
  expect(dividers[0]?.open).toBe(false);
  expect(dividers[1]?.open).toBe(false);

  fireEvent.click(dividers[0]!.querySelector("summary")!);
  expect(dividers[0]?.open).toBe(true);
  expect(dividers[1]?.open).toBe(false);
});

test("blank text with no source and no images renders nothing distinguishable", () => {
  const { container } = render(<SteeringItem item={item({ text: "" })} turn={turn} live={false} />);
  expect(container.firstChild).toBeNull();
});

test('a non-"user", non-empty source (defensive - only "user" is special-cased) still gets the divider treatment', () => {
  render(<SteeringItem item={item({ text: "daemon nudge", source: "system" })} turn={turn} live={false} />);
  expect(screen.getByTestId("steering-item")).toBeTruthy();
});

test("a daemon-sourced steering item with BOTH real text and images renders the text and drops the image count (documented limitation, matches legacy §8 - pinned so a future change is deliberate)", () => {
  render(
    <SteeringItem
      item={item({ text: "daemon nudge with a picture", images: [{ src: "a" }, { src: "b" }] })}
      turn={turn}
      live={false}
    />,
  );
  const el = screen.getByTestId("steering-item");
  expect(el.textContent).toContain("daemon nudge with a picture");
  expect(screen.queryByTestId("user-message-image-placeholder")).toBeNull();
  expect(el.textContent).not.toMatch(/\[\d+ images?\]/);
});

// --- the wire's steering kind labels the row, or claims nothing at all -----
// The daemon names what it injected (events.SteeringKind* on the Go side);
// the row shows that label instead of pattern-matching the message's prose.
// Absent a kind - a daemon predating the field, or a kind with no entry in
// KIND_LABELS - the row claims nothing: a bare "System steered" with no
// colon, since a colon promises a value.

test("labels a steer from its wire kind", () => {
  render(
    <SteeringItem
      item={item({ text: "You have completed all tasks", steeringKind: "tasks-done" })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getByText("System steered: Tasks done")).toBeTruthy();
  expect(screen.getByTestId("steering-glyph")).toBeTruthy();
});

// No kind means the daemon did not say. A colon promises a value.
test("claims nothing when the wire carries no kind", () => {
  render(<SteeringItem item={item({ text: "unclassifiable" })} turn={turn} live={false} />);
  expect(screen.getByText("System steered")).toBeTruthy();
  expect(screen.queryByText(/System steered:/)).toBeNull();
});

// The OTHER half of "no colon": a kind the wire DID send, but this UI has no
// label for - a daemon newer than this UI (KIND_LABELS §comment above), or -
// live today - a "notification" kind whose markup fails to parse and falls
// through to the divider path instead of a card. Absent and unmapped must
// both render bare; only one of the two was pinned before this test.
test("claims nothing when the wire kind is unmapped, not just when it's absent", () => {
  render(<SteeringItem item={item({ text: "x", steeringKind: "some-future-kind" })} turn={turn} live={false} />);
  expect(screen.getByText("System steered")).toBeTruthy();
  expect(screen.queryByText(/System steered:/)).toBeNull();
});

// The prose that used to drive classification must no longer do so.
test("does not infer a kind from the text", () => {
  render(
    <SteeringItem item={item({ text: "You have completed all tasks on your task list." })} turn={turn} live={false} />,
  );
  expect(screen.getByText("System steered")).toBeTruthy();
  expect(screen.queryByText(/Tasks done/)).toBeNull();
});

// current-task/task-list: the tasks panel + task-update card already own
// this surface (parity-m4 §8:209-217), so these kinds render nothing here.
test.each([["current-task"], ["task-list"]])("suppresses %s - the tasks panel owns that surface", (kind) => {
  const { container } = render(
    <SteeringItem item={item({ text: "x", steeringKind: kind })} turn={turn} live={false} />,
  );
  expect(container.firstChild).toBeNull();
});

// task-nudge is a labeled kind (KIND_LABELS above), NOT a suppressed one -
// only current-task/task-list are in SUPPRESSED. Before wave-8/this task,
// classifySteering's cascade suppressed task-nudge outright (a one-time
// tool-availability nudge, judged not user-meaningful); the brief's
// SUPPRESSED set narrows that to current-task/task-list only, so a
// task-nudge steer now renders where it previously rendered nothing. That
// flip is brief-mandated and correct - this pins the new visible outcome.
test("a task-nudge kind renders labeled, not suppressed", () => {
  render(<SteeringItem item={item({ text: "x", steeringKind: "task-nudge" })} turn={turn} live={false} />);
  expect(screen.getByText("System steered: Task nudge")).toBeTruthy();
});

// Ordering pin: the source==="user" check must run BEFORE the SUPPRESSED
// check, so a user-sourced message is never silently dropped just because
// its (irrelevant, human-typed) steeringKind happens to collide with a
// suppressed daemon kind. Unreachable today - every user-sourced enqueue
// site passes an empty kind - but the ordering is load-bearing the moment
// that changes, and nothing else in this file pins the ordering itself.
test('source "user" is checked before suppression - a user-sourced steer is never dropped by a colliding kind', () => {
  render(
    <SteeringItem
      item={item({ text: "focus on the tests", source: "user", steeringKind: "current-task" })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getByTestId("user-message-item")).toBeTruthy();
  expect(screen.queryByTestId("steering-item")).toBeNull();
});

// --- card routing stays content-driven, independent of the kind ------------
// The trigger is <job-notification> markup, or the fixed "Observer callback:\n"
// header - structured payloads that can't false-positive the way a prose
// pattern like /completed all tasks/ could - so a card renders whether or not
// the steer carries a kind at all (contracts §17).

const JOB_NOTIFICATION_STEERING = `<job-notification job_id="job_7" event="completed" job_type="delegate" status="completed" reason="" output_bytes="9" transcript_ref="local:ref_x">
Job job_7 completed.
excerpt:
finished the lane
</job-notification>`;

test("a job-notification steer renders a notification card (not a steering divider)", () => {
  render(<SteeringItem item={item({ text: JOB_NOTIFICATION_STEERING })} turn={turn} live={false} />);
  expect(screen.getByTestId("notification-card")).toBeTruthy();
  expect(screen.queryByTestId("steering-item")).toBeNull();
  expect(screen.getByText("Job completed")).toBeTruthy();
});

test("a job-notification steer expands the notification before asserting card body content", () => {
  render(<SteeringItem item={item({ text: JOB_NOTIFICATION_STEERING })} turn={turn} live={false} />);
  fireEvent.click(screen.getByTestId("notification-card"));
  expect(screen.getByTestId("notification-card-root")).toBeTruthy();
  expect(screen.getByText("Job completed")).toBeTruthy();
});

test("a completed shell job renders its ANSI excerpt as styled text instead of escape fragments", () => {
  const text = `<job-notification job_id="job_shell" event="completed" job_type="shell" status="completed" reason="exit_zero" output_bytes="80" exit_code="0">
Job job_shell completed.
excerpt:
\u001b[2m Test Files \u001b[22m \u001b[1m\u001b[31m1 failed\u001b[39m\u001b[22m\u001b[2m | \u001b[1m\u001b[32m283 passed\u001b[39m\u001b[22m
</job-notification>`;
  render(<SteeringItem item={item({ text })} turn={turn} live={false} />);

  fireEvent.click(screen.getByTestId("notification-card"));

  const excerpt = screen.getByTestId("notification-field-excerpt");
  expect(excerpt.textContent).toBe(" Test Files  1 failed | 283 passed");
  expect(screen.getByText("1 failed").closest('[data-ansi-fg="red"]')).toBeTruthy();
  expect(screen.getByText("283 passed").closest('[data-ansi-fg="green"]')).toBeTruthy();
  expect(excerpt.querySelector("[data-ansi-dim]")?.textContent).toBe(" Test Files ");
});

// --- kata cq7j: the bounded primary shell excerpt must be tail-directional -
// the newest (most useful) retained output visible, the oldest elided - and
// must never cut inside an SGR escape sequence. NotificationCard's
// EXCERPT_PREVIEW is 500; this constructs retained shell output long enough
// to force truncation, with an EARLY_TAIL sentinel near the start (must be
// elided) and a FINAL_RESULT sentinel at the end (must stay visible), and an
// SGR "turn red" sequence positioned so the naive `decoded.slice(0, 500)`
// head-cut - and even a naive tail-cut - would land inside the escape bytes
// themselves (mirrors shellTool.test.tsx's "a live shell tail never starts
// inside an ANSI sequence", scaled to the notification card's smaller
// preview budget).
test("a completed shell job's primary excerpt shows the final retained output, not the oldest, and never cuts inside an SGR sequence", () => {
  const EARLY_TAIL = "EARLY_TAIL_MARKER";
  const FINAL_RESULT = "FINAL_RESULT_MARKER";
  const prefix = EARLY_TAIL + "e".repeat(50 - EARLY_TAIL.length); // exactly 50 chars
  const colorOn = "[31m"; // 5 chars; the kept tail starts 2 chars into this
  const suffixFiller = "x".repeat(497 - FINAL_RESULT.length);
  const decoded = prefix + colorOn + suffixFiller + FINAL_RESULT; // 552 chars total, 52 over the 500 budget

  const text = `<job-notification job_id="job_tail" event="completed" job_type="shell" status="completed" reason="exit_zero" output_bytes="552" exit_code="0">
Job job_tail completed.
excerpt:
${decoded}
</job-notification>`;
  render(<SteeringItem item={item({ text })} turn={turn} live={false} />);
  fireEvent.click(screen.getByTestId("notification-card"));

  const excerpt = screen.getByTestId("notification-field-excerpt");
  // Explicit truncation affordance, final content visible, older content
  // elided, and the reconstructed style spans exactly the kept text - no
  // leaked "31m" or "[31m" fragment from the cut escape sequence.
  expect(excerpt.textContent).toBe(`…${suffixFiller}${FINAL_RESULT}`);
  expect(excerpt.textContent).not.toContain(EARLY_TAIL);
  expect(excerpt.querySelector('[data-ansi-fg="red"]')?.textContent).toBe(`${suffixFiller}${FINAL_RESULT}`);
});

// --- kata 9cnq: communicate-envelope parsing must be gated on job_type, not
// detected from JSON shape. A shell job's stdout is literal output; it must
// render literally even when it coincidentally parses as JSON with the same
// message/data keys a delegate's communicate envelope uses. -----------------

test("a completed shell job whose stdout happens to be JSON renders the literal JSON, not a communicate card", () => {
  const text = `<job-notification job_id="job_shell" event="completed" job_type="shell" status="completed" reason="exit_zero" output_bytes="80" exit_code="0">
Job job_shell completed.
excerpt:
{"message":"**literal shell output**","data":{"status":"ok","concerns":["from stdout"]}}
</job-notification>`;
  render(<SteeringItem item={item({ text })} turn={turn} live={false} />);
  fireEvent.click(screen.getByTestId("notification-card"));

  const root = screen.getByTestId("notification-card-root");
  // The raw JSON text is reader-visible verbatim (not consumed as an envelope).
  expect(root.textContent).toContain('{"message":"**literal shell output**"');
  // Markdown is not activated: "**literal shell output**" stays literal text,
  // never a <strong> element.
  expect(root.querySelector("strong")).toBeNull();
  // The coincidental data.concerns array is not promoted to the card's
  // concerns line.
  expect(root.textContent).not.toContain("Concerns:");
  // A clean exit with no promoted concerns reads as success, not warning.
  expect(screen.getByTestId("notification-card").getAttribute("data-tone")).toBe("success");
});

// Companion coverage for the same kata: the communicate-envelope gate must
// not disturb the unrelated ANSI excerpt path for ordinary (non-JSON) shell
// output - the fix is a job_type gate, not a change to excerpt rendering.
test("a shell excerpt that merely starts with '{' still renders through the ANSI path when it is not valid JSON", () => {
  const text = `<job-notification job_id="job_shell2" event="completed" job_type="shell" status="completed" reason="exit_zero" output_bytes="20" exit_code="0">
Job job_shell2 completed.
excerpt:
{not json} [31mFAIL[39m
</job-notification>`;
  render(<SteeringItem item={item({ text })} turn={turn} live={false} />);
  fireEvent.click(screen.getByTestId("notification-card"));

  expect(screen.getByText("FAIL").closest('[data-ansi-fg="red"]')).toBeTruthy();
});

test("a notification restores its owning session to main before opening the child beside it", async () => {
  workspaceStore.getState().openPane("session", { ref: "local:unrelated" });
  const user = userEvent.setup();
  render(
    <SteeringItem item={item({ text: JOB_NOTIFICATION_STEERING })} turn={turn} live={false} sessionRef="local:owner" />,
  );

  await user.click(screen.getByRole("button", { name: "Open subagent" }));

  const panes = workspaceStore.getState().panes;
  const owner = panes.find(
    (pane) => pane.type === "session" && pane.params && (pane.params as { ref?: string }).ref === "local:owner",
  );
  const child = panes.find((pane) => pane.type === "transcript");
  expect(owner?.slot).toBe("main");
  expect(owner?.params).toEqual({ ref: "local:owner" });
  expect(
    panes.some((pane) => pane.type === "session" && (pane.params as { ref?: string }).ref === "local:unrelated"),
  ).toBe(false);
  expect(child?.slot).toBe("secondary");
  expect(child?.params).toEqual({ ref: "local:ref_x", parentRef: "local:owner" });
});

test("a notification card always keeps the verbatim block inspectable in a raw disclosure", () => {
  render(<SteeringItem item={item({ text: JOB_NOTIFICATION_STEERING })} turn={turn} live={false} />);
  fireEvent.click(screen.getByTestId("notification-card"));
  expect(screen.getByTestId("notification-raw").textContent).toContain("job_7");
});

test("two job-notification blocks in one steer render two distinct cards", () => {
  const two = `${JOB_NOTIFICATION_STEERING}\n<job-notification job_id="job_8" event="failed" job_type="shell" status="failed" reason="bad" output_bytes="1" exit_code="3">
Job job_8 failed.
</job-notification>`;
  render(<SteeringItem item={item({ text: two })} turn={turn} live={false} />);
  expect(screen.getAllByTestId("notification-card")).toHaveLength(2);
});

test("routes a notification kind to a card", () => {
  const text = '<job-notification job_id="j1" status="completed">done\nexcerpt:\nall good</job-notification>';
  render(<SteeringItem item={item({ text, steeringKind: "notification" })} turn={turn} live={false} />);
  expect(screen.getByTestId("notification-card")).toBeTruthy();
});

test("a mixed notification steer keeps its wire-kind label on the leftover divider", () => {
  render(
    <SteeringItem
      item={item({ text: `${JOB_NOTIFICATION_STEERING}\ntrailing steering prose`, steeringKind: "tasks-done" })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getByTestId("notification-card")).toBeTruthy();
  expect(screen.getByText("System steered: Tasks done")).toBeTruthy();
  expect(screen.getByText("trailing steering prose")).toBeTruthy();
});

// The card's trigger is <job-notification> markup, not the kind: structured
// markup cannot false-positive the way a prose pattern can, so a pre-Kind
// transcript still gets its card.
test("a pre-Kind steer carrying a job-notification block still renders a card", () => {
  const text = '<job-notification job_id="j1" status="completed">done\nexcerpt:\nall good</job-notification>';
  render(<SteeringItem item={item({ text })} turn={turn} live={false} />);
  expect(screen.getByTestId("notification-card")).toBeTruthy();
});

// Pins the same guarantee for the OTHER structured trigger: a review of an
// earlier task raised a concern that deleting the prose classifier would
// silently kill the observer-callback card too. It does not -
// parseObserverCallback keys off the fixed "Observer callback:\n" header, not
// a kind - but that must be pinned, not left resting on an argument.
test("an observer callback with no steeringKind still renders its notification card", () => {
  render(
    <SteeringItem
      item={item({ text: "Observer callback:\nmessage: the sidecar noticed the build broke" })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getByTestId("notification-card")).toBeTruthy();
  expect(screen.queryByTestId("steering-item")).toBeNull();
});

// --- structure: glyph leading, chevron trailing, one ink for the row -------

test("the chevron trails the label, and the diamond leads inside its rail slot", () => {
  render(<SteeringItem item={item({ text: "x", steeringKind: "loop-detected" })} turn={turn} live={false} />);
  const summary = screen.getByTestId("steering-item").querySelector("summary");
  const kids = Array.from(summary?.children ?? []);
  expect(kids[0]?.getAttribute("data-testid")).toBe("steering-rail-icon");
  expect(kids[0]?.querySelector('[data-testid="steering-glyph"]')).not.toBeNull();
  expect(kids[kids.length - 1]?.getAttribute("data-testid")).toBe("steering-chevron");
});

test("the diamond rails at 50% opacity in the speaker-avatar column, gutter-pulled on the summary above the breakpoint", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "steeringitem.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const slot = /\.railIcon\s*\{([^}]*)\}/.exec(css);
  expect(slot).not.toBeNull();
  expect(slot![1]).toContain("opacity: 0.5");
  expect(slot![1]).toContain("width: var(--speaker-avatar-size)");
  // slot + margin + the row's own gap = one speaker-gutter, so the label
  // lands exactly on the content edge (same arithmetic as ToolRow's rail).
  expect(slot![1]).toContain("margin-right: calc(var(--speaker-gap) - var(--space-2))");
  const media = /@media \(min-width: 700px\) \{\s*\.summary\s*\{([^}]*)\}/.exec(css);
  expect(media).not.toBeNull();
  expect(media![1]).toContain("margin-left: calc(-1 * var(--speaker-gutter))");
});

test("the body opens with the SYSTEM-REMINDER wrapper stripped", () => {
  render(
    <SteeringItem
      item={item({ text: "<SYSTEM-REMINDER>the note</SYSTEM-REMINDER>", steeringKind: "hook-context" })}
      turn={turn}
      live={false}
    />,
  );
  fireEvent.click(screen.getByText("System steered: Hook context"));
  expect(screen.getByText("the note")).toBeTruthy();
});
