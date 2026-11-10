import { formatElapsed } from "@evener/appwire-client";
import type { PendingMessage } from "@evener/appwire-client/state/mutation";
import { render, screen, within } from "@testing-library/react";
import { expect, test } from "vitest";
import { SessionNowContext } from "../liveness";
import type { PendingActionContext } from "./actionContext";
import { PendingMessageStack } from "./PendingMessageStack";
import { pendingMessageCaption } from "./pendingMessageCaption";

const NOW = 43_000;

function row(overrides: Partial<PendingMessage> = {}): PendingMessage {
  const text = overrides.text ?? "hello";
  return {
    id: "m1",
    ref: "ref_a",
    method: "queue",
    state: "queued",
    text,
    displayText: overrides.displayText ?? text,
    textComplete: true,
    images: [],
    imageCount: 0,
    skillNames: [],
    commandNames: [],
    fromThisClient: true,
    queueEntryIds: [],
    ...overrides,
  };
}

const context: PendingActionContext = {
  ref: "ref_a",
  getComposerText: () => ({ text: "", hasPending: false }),
  onRestoreToComposer: () => undefined,
  onDrainSuccess: () => undefined,
  busy: false,
  onDrainBusyChange: () => undefined,
};

function renderStack(messages: PendingMessage[], actions?: PendingActionContext) {
  return render(
    <SessionNowContext.Provider value={NOW}>
      <PendingMessageStack ref="ref_a" messages={messages} context={actions} />
    </SessionNowContext.Provider>,
  );
}

test("renders one user-shaped row per pending message, in the order given", () => {
  renderStack([
    row({ id: "m1", text: "first" }),
    row({ id: "m2", text: "second", state: "steering", method: "steer" }),
  ]);
  const rows = screen.getAllByTestId("pending-message-row");
  expect(rows).toHaveLength(2);
  expect(rows[0]!.dataset.messageId).toBe("m1");
  expect(rows[0]!.dataset.state).toBe("queued");
  expect(within(rows[0]!).getByTestId("user-bubble").textContent).toContain("first");
  expect(rows[1]!.dataset.state).toBe("steering");
  expect(within(rows[1]!).getByTestId("user-bubble").textContent).toContain("second");
  for (const item of rows) {
    expect(within(item).getByTestId("user-message-item").dataset.opensExchange).toBe("true");
  }
});

test("the status caption occupies the delivered timestamp slot", () => {
  renderStack([row({ state: "uncertain" })]);
  const item = screen.getByTestId("user-message-item");
  expect(item.dataset.status).toBe("true");
  expect(item.querySelector("[data-testid='user-bubble']")).toBeTruthy();
});

test("the composer's active recovery row is not duplicated in the stack", () => {
  renderStack([row({ id: "m1", state: "refused", recoveryKind: "rejected" })], {
    ...context,
    activeRecoveryId: "m1",
  });
  expect(screen.queryByTestId("pending-message-stack")).toBeNull();
});

test("a row without a composer context renders no actions", () => {
  renderStack([row({ state: "waiting" })]);
  expect(screen.getByTestId("pending-message-row")).toBeTruthy();
  expect(screen.queryByRole("button")).toBeNull();
});

test("a queued row offers edit and remove", () => {
  renderStack([row({ queueIndex: 0, queueEntryId: "q1" })], context);
  expect(screen.getByRole("button", { name: "Edit message" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Remove from queue" })).toBeTruthy();
});

test("a legacy queue row with only a preview shows it but offers no Edit", () => {
  renderStack([row({ text: "truncated…", textComplete: false, queueIndex: 0, queueEntryId: "q1" })], context);
  expect(screen.getByTestId("user-bubble").textContent).toContain("truncated…");
  expect(screen.queryByRole("button", { name: "Edit message" })).toBeNull();
  expect(screen.getByRole("button", { name: "Remove from queue" })).toBeTruthy();
});

test("steering captions distinguish the running step, the next turn, a hub wait, and elapsed time", () => {
  const steering = row({
    state: "steering",
    method: "steer",
    createdAt: 1_000,
    optimistic: {} as PendingMessage["optimistic"],
  });
  const running = pendingMessageCaption(steering, true, NOW);
  const next = pendingMessageCaption(steering, false, NOW);
  const waiting = pendingMessageCaption(steering, true, NOW, true);
  expect(running).not.toBe(next);
  expect(waiting).not.toBe(running);
  expect(running).toContain(formatElapsed(NOW - 1_000));
});

test("an orphaned row offers copy rather than edit", () => {
  renderStack([row({ state: "orphaned", recoveryKind: "orphaned" })], context);
  expect(screen.getByRole("button", { name: "Copy" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Edit message" })).toBeNull();
});

test("a refused Stop offers Dismiss rather than Edit", () => {
  renderStack([row({ state: "refused", recoveryKind: "rejected", interrupt: true })], context);
  expect(screen.getByRole("button", { name: "Dismiss" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Edit message" })).toBeNull();
});

test("image bytes render through the delivered image gallery", () => {
  renderStack([row({ images: [{ src: "data:image/png;base64,AAAA", name: "a.png" }], imageCount: 1 })]);
  expect(screen.getByRole("img")).toBeTruthy();
});
