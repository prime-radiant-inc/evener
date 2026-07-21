import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import type { ItemModel, TurnModel } from "../../../../protocol/model";
import { ignoringTurn, itemRendererFor } from "../types";
import { SteeringItem } from "./SteeringItem";

afterEach(cleanup);

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
  render(<SteeringItem item={item({ text: "look", source: "user", images: ["a", "b"] })} turn={turn} live={false} />);
  expect(screen.getAllByTestId("image-gallery-thumb")).toHaveLength(2);
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

test("the divider's summary uses sentence case, not shouting chrome", () => {
  render(<SteeringItem item={item({ text: "nudge text" })} turn={turn} live={false} />);
  const summary = screen.getByTestId("steering-item").querySelector("summary");
  expect(summary?.textContent).toBe("Steering injected");
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
    <SteeringItem item={item({ text: "daemon nudge with a picture", images: ["a", "b"] })} turn={turn} live={false} />,
  );
  const el = screen.getByTestId("steering-item");
  expect(el.textContent).toContain("daemon nudge with a picture");
  expect(screen.queryByTestId("user-message-image-placeholder")).toBeNull();
  expect(el.textContent).not.toMatch(/\[\d+ images?\]/);
});

// --- T5: task-bookkeeping steering suppression ------------------------------
// The legacy renderer's classifySteering (cmd/serf-hub/assets/renderer-
// format.js:415-493) recognizes several daemon-steering "kinds"; current-
// task/full-list/task-nudge were rendered specially there (cache-seeding
// side effects, or a compact pointer) rather than as a plain divider - and
// with T5's tasks panel now owning that surface (model.tasks, chrome/
// TasksPanel.tsx), those three kinds should render NOTHING here rather than
// a divider that duplicates what the panel already shows. Every other kind
// (tasks-done, loop detection, read-only nudges, transcript pointers,
// notifications, unknown) keeps the existing divider treatment unchanged -
// this is a narrow, additive suppression, not a rewrite of the divider path
// itself (every test above this section still exercises it verbatim).
//
// Fixtures are wire-true: the daemon's own steering-message templates
// (agent/task_reminders.go), not invented text, so the classification is
// tested against exactly what a real session would send.

const CURRENT_TASK_STEERING =
  '<SYSTEM-REMINDER>\n<CURRENT-TASK id="3">\n<TITLE>Fix the bug</TITLE>\n<INSTRUCTIONS>\ndo the thing\n</INSTRUCTIONS>\n</CURRENT-TASK>\nCall your next tool: use task_list to mark task 3 as done when this step is complete.\n</SYSTEM-REMINDER>';

const FULL_LIST_STEERING = "<SYSTEM-REMINDER>\nTask list:\n  [open] #1: Do a thing\n</SYSTEM-REMINDER>";

const TASKS_DONE_STEERING =
  "<SYSTEM-REMINDER>\nYou have completed all tasks on your task list. If you have other work to do, add it to the task list now. Otherwise, deliver your final output with the communicate tool.\n</SYSTEM-REMINDER>";

const TASK_NUDGE_STEERING =
  "<SYSTEM-REMINDER>\nYou have a task_list tool available for organizing multi-step work. Consider creating a task list to track your progress.\n</SYSTEM-REMINDER>";

test("a current-task restatement renders nothing - the tasks panel already shows the in-progress task", () => {
  const { container } = render(<SteeringItem item={item({ text: CURRENT_TASK_STEERING })} turn={turn} live={false} />);
  expect(container.firstChild).toBeNull();
});

test("a full task-list restatement renders nothing - the tasks panel already shows the list", () => {
  const { container } = render(<SteeringItem item={item({ text: FULL_LIST_STEERING })} turn={turn} live={false} />);
  expect(container.firstChild).toBeNull();
});

test("the one-time task_list tool nudge renders nothing - not user-meaningful", () => {
  const { container } = render(<SteeringItem item={item({ text: TASK_NUDGE_STEERING })} turn={turn} live={false} />);
  expect(container.firstChild).toBeNull();
});

test("'all tasks complete' steering is NOT suppressed - it's a genuine event, not task-list bookkeeping the panel already covers", () => {
  render(<SteeringItem item={item({ text: TASKS_DONE_STEERING })} turn={turn} live={false} />);
  const details = screen.getByTestId("steering-item") as HTMLDetailsElement;
  expect(details.textContent).toContain("completed all tasks");
});

test("suppression only ever applies to the daemon divider path - a 'user'-sourced message with task-list-shaped text still renders as a normal user bubble", () => {
  render(<SteeringItem item={item({ text: FULL_LIST_STEERING, source: "user" })} turn={turn} live={false} />);
  expect(screen.getByTestId("user-message-item")).toBeTruthy();
  expect(screen.queryByTestId("steering-item")).toBeNull();
});
