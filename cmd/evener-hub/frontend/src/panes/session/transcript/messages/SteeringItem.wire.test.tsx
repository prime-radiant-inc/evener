// The daemon's steers as it actually sends them
// (agent/testdata/systemeventwire): each folds to the label both clients
// share (@evener/appwire-client steeringLabel), and the current task and the
// task list render nothing.
import {
  type ItemModel,
  parseSteeringNotifications,
  stripSystemReminder,
  type TurnModel,
} from "@evener/appwire-client";
import { type SystemEventWireCase, systemEventWireItem } from "@evener/appwire-client/testing/systemEventWireFixtures";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import reports from "../../../../../../../../agent/testdata/notificationwire/reports.json";
import { resetDisclosureStoreForTests } from "../../../../widgets/disclosure/disclosureStore";
import { SteeringItem } from "./SteeringItem";

afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
});

const turn: TurnModel = { id: "turn_1", status: "completed", items: [] };

test.each(reports)("renders real $case publication through the shared adapter", (recorded) => {
  const notifications = parseSteeringNotifications(recorded.item.text).flatMap((fragment) =>
    fragment.kind === "notification" ? [fragment.notification] : [],
  );
  expect(notifications).toHaveLength(1);
  expect(notifications[0]?.delegateId).toBe("dlg_projection");
  const settled = ["done", "blocked", "interruption"].includes(recorded.case);
  expect(Boolean(notifications[0]?.reportNotice)).toBe(settled);
  if (!settled) expect(notifications[0]?.outcome).toBeUndefined();
  render(<SteeringItem item={recorded.item as ItemModel} turn={turn} live={false} />);
  if (!screen.queryByTestId("notification-message")) {
    const summary = document.querySelector("details > summary");
    if (summary) fireEvent.click(summary);
  }
  if (recorded.case !== "interruption") {
    const message = recorded.case.endsWith("-json")
      ? '{"message":"JSON example, not a captured result","data":{"unproven":"must remain prose"},"artifacts":[]}'
      : `payload-${recorded.case}-9c2f`;
    expect.soft(screen.getByTestId("notification-message").textContent?.trim()).toBe(message);
    if (recorded.case.startsWith("waiting-empty")) {
      expect(screen.queryByTestId("notification-structured-result")).toBeNull();
      expect(notifications[0]?.update).toBe(true);
    } else {
      expect(screen.getByTestId("notification-structured-result").textContent).toContain("result-9c2f");
      expect(screen.getByTestId("notification-structured-result").textContent).not.toContain("unproven");
    }
  }
  if (recorded.case === "done") {
    expect(recorded.generation).toBe(1);
    expect(recorded.activity?.runGeneration).toBe(1);
    expect(recorded.nextActivity).toMatchObject({ runGeneration: 2, status: "running", terminal: false });
    expect(recorded.nextActivity).not.toHaveProperty("outcome");
    expect(recorded.nextActivity).not.toHaveProperty("reportPreview");
    expect(notifications[0]?.outcome).toBe("completed");
  }
});

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
