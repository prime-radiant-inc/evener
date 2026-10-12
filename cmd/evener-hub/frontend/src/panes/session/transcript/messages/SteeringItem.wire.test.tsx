// The daemon's steers as it actually sends them
// (agent/testdata/systemeventwire): each folds to the label both clients
// share (@evener/appwire-client steeringLabel), and the current task and the
// task list render nothing.
import { type ItemModel, stripSystemReminder, type TurnModel } from "@evener/appwire-client";
import { type SystemEventWireCase, systemEventWireItem } from "@evener/appwire-client/testing/systemEventWireFixtures";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { resetDisclosureStoreForTests } from "../../../../widgets/disclosure/disclosureStore";
import { SteeringItem } from "./SteeringItem";

afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
});

const turn: TurnModel = { id: "turn_1", status: "completed", items: [] };

function steer(name: SystemEventWireCase): ItemModel {
  return systemEventWireItem(name) as ItemModel;
}

// The kind the divider's label names (its label span's data-kind).
function steeringKind(): string | undefined {
  return screen.getByTestId("steering-item").querySelector<HTMLElement>(":scope > summary [data-kind]")?.dataset.kind;
}

test.each([
  ["steer-hook-context", "hook-context"],
  ["steer-precompact-hook", "precompact-hook"],
  ["steer-compact-nudge", "compact-nudge"],
  ["steer-no-tool-calls", "no-tool-calls"],
  ["steer-loop-detected", "loop-detected"],
  ["steer-provider-failure", "provider-failure"],
  ["steer-transcript-pointer", "transcript-pointer"],
  ["steer-note-handoff", "note-handoff"],
  ["steer-from-parent", "parent"],
] as const)("folds %s under its %s label, opening to what it said", (name, kind) => {
  render(<SteeringItem item={steer(name)} turn={turn} live={false} />);
  expect(steeringKind()).toBe(kind);
  fireEvent.click(screen.getByTestId("steering-item").querySelector("summary")!);
  const body = screen.getByTestId("steering-item").querySelector("pre")?.textContent;
  expect(body).toBe(stripSystemReminder(systemEventWireItem(name).text ?? ""));
});

// A parent's message is conversation, not the daemon's markup: it shows whole
// under "From parent" even when it quotes notification tags.
test.each([
  "Inspect how <job-notification> is rendered",
  `See <delegate-notification delegate_id="dlg_42" event="completed" status="completed" reason="" transcript_ref="local:sess_child">
Delegate dlg_42 completed.
</delegate-notification> for the format`,
])("renders a parent's message that quotes notification markup whole: %s", (text) => {
  const item = { ...steer("steer-from-parent"), text };
  render(<SteeringItem item={item} turn={turn} live={false} />);
  expect(steeringKind()).toBe("parent");
  fireEvent.click(screen.getByTestId("steering-item").querySelector("summary")!);
  expect(screen.getByTestId("steering-item").querySelector("pre")?.textContent).toBe(text);
});

test.each(["steer-current-task", "steer-task-list"] as const)("renders nothing for %s", (name) => {
  const { container } = render(<SteeringItem item={steer(name)} turn={turn} live={false} />);
  expect(container.textContent).toBe("");
});
