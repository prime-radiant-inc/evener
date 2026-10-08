// The daemon's steers as it actually sends them
// (agent/testdata/systemeventwire): each folds to the label both clients
// share (@evener/appwire-client steeringLabel), and the current task and the
// task list render nothing.
import { type ItemModel, stripSystemReminder, type TurnModel } from "@evener/appwire-client";
import { type SystemEventWireCase, systemEventWireItem } from "@evener/appwire-client/testing/systemEventWireFixtures";
import { cleanup, render, screen } from "@testing-library/react";
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

test.each([
  ["steer-hook-context", "System steered: Hook context"],
  ["steer-precompact-hook", "System steered: Hook context before compacting"],
  ["steer-compact-nudge", "System steered: Running low on context"],
  ["steer-no-tool-calls", "System steered: Reminded to keep working"],
  ["steer-loop-detected", "System steered: Loop detected"],
  ["steer-provider-failure", "System steered: Provider failed"],
  ["steer-transcript-pointer", "System steered: Where to find the full transcript"],
  ["steer-note-handoff", "System steered: Note to self"],
] as const)("folds %s to %s, opening to what it said", (name, label) => {
  render(<SteeringItem item={steer(name)} turn={turn} live={false} />);
  expect(screen.getByText(label)).toBeTruthy();
  const body = screen.getByTestId("steering-item").querySelector("pre")?.textContent;
  expect(body).toBe(stripSystemReminder(systemEventWireItem(name).text ?? ""));
});

test.each(["steer-current-task", "steer-task-list"] as const)("renders nothing for %s", (name) => {
  const { container } = render(<SteeringItem item={steer(name)} turn={turn} live={false} />);
  expect(container.textContent).toBe("");
});
