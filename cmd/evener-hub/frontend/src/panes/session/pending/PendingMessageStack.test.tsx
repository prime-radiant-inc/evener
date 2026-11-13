import { formatElapsed, type ThreadModel } from "@evener/appwire-client";
import type { PendingMessage } from "@evener/appwire-client/state/mutation";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { threadsStore } from "../../../stores/threads";
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

test("activating an edited recovery keeps the original row with the same wire id", () => {
  renderStack(
    [
      row({ id: "m1", text: "original" }),
      row({
        id: "m1:edited:m1:1",
        text: "edited",
        recovery: { clientMutationId: "m1" } as PendingMessage["recovery"],
      }),
    ],
    { ...context, activeRecoveryId: "m1" },
  );
  const rows = screen.getAllByTestId("pending-message-row");
  expect(rows).toHaveLength(1);
  expect(within(rows[0]!).getByTestId("user-bubble").textContent).toContain("original");
});

test("the active recovery is excluded by its storage key when the row is wire-keyed", () => {
  renderStack(
    [
      row({
        id: "m1",
        recovery: { clientMutationId: "fresh" } as PendingMessage["recovery"],
      }),
    ],
    { ...context, activeRecoveryId: "fresh" },
  );
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

test("a queued row with an unedited recovery still offers queue actions", () => {
  renderStack(
    [
      row({
        queueIndex: 0,
        queueEntryId: "q1",
        recovery: { clientMutationId: "m1", compositionRevision: 0 } as PendingMessage["recovery"],
      }),
    ],
    context,
  );
  expect(screen.getByRole("button", { name: "Edit message" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Remove from queue" })).toBeTruthy();
});

test("a daemon queue row with a local display copy removes through the queue entry", async () => {
  const cancel = vi.spyOn(threadsStore.getState(), "cancelQueued").mockResolvedValue(undefined);
  try {
    renderStack(
      [
        row({
          queueIndex: 0,
          queueEntryId: "q1",
          outbox: { clientMutationId: "m1" } as PendingMessage["outbox"],
        }),
      ],
      context,
    );
    fireEvent.click(screen.getByRole("button", { name: "Remove from queue" }));
    await waitFor(() => expect(cancel).toHaveBeenCalledWith("ref_a", 0, "q1"));
  } finally {
    cancel.mockRestore();
  }
});

test("an image-only queue row promotes with its placeholder display", async () => {
  const previous = threadsStore.getState().threads;
  act(() => {
    threadsStore.setState({
      threads: new Map([
        [
          "ref_a",
          {
            id: "thr_ref_a",
            status: { type: "active" },
            capabilities: { steer: true },
            queue: { depth: 1, revision: 1 },
          } as unknown as ThreadModel,
        ],
      ]),
    });
  });
  const promote = vi.spyOn(threadsStore.getState(), "promoteQueuedAsSteer").mockResolvedValue(undefined);
  try {
    renderStack([row({ queueIndex: 0, queueEntryId: "q1", text: "", displayText: "[image]", imageCount: 1 })], context);
    fireEvent.click(screen.getByRole("button", { name: "Steer now" }));
    await waitFor(() =>
      expect(promote).toHaveBeenCalledWith("ref_a", 0, "q1", expect.objectContaining({ text: "[image]" })),
    );
  } finally {
    promote.mockRestore();
    act(() => {
      threadsStore.setState({ threads: previous });
    });
  }
});

test("Steer now stays mounted as an aria-disabled control when draining is unavailable", () => {
  const previous = threadsStore.getState().threads;
  act(() => {
    threadsStore.setState({
      threads: new Map([
        [
          "ref_a",
          {
            id: "thr_ref_a",
            status: { type: "awaiting" },
            capabilities: { steer: true },
            queue: { depth: 1, revision: 1 },
          } as unknown as ThreadModel,
        ],
      ]),
    });
  });
  const promote = vi.spyOn(threadsStore.getState(), "promoteQueuedAsSteer").mockResolvedValue(undefined);
  try {
    renderStack([row({ queueIndex: 0, queueEntryId: "q1" })], context);
    const button = screen.getByRole("button", { name: "Steer now" });
    expect(button.getAttribute("aria-disabled")).toBe("true");
    fireEvent.click(button);
    expect(promote).not.toHaveBeenCalled();
  } finally {
    promote.mockRestore();
    act(() => {
      threadsStore.setState({ threads: previous });
    });
  }
});

test("a legacy queue row with only a preview shows it but offers no Edit", () => {
  renderStack([row({ text: "truncated…", textComplete: false, queueIndex: 0, queueEntryId: "q1" })], context);
  expect(screen.getByTestId("user-bubble").textContent).toContain("truncated…");
  expect(screen.queryByRole("button", { name: "Edit message" })).toBeNull();
  expect(screen.getByRole("button", { name: "Remove from queue" })).toBeTruthy();
});

test("a queue row with no entry id exposes no edit or remove action", () => {
  renderStack([row({ id: "queue:0", queueIndex: 0, queueEntryId: undefined, textComplete: false })], context);
  expect(screen.queryByRole("button", { name: "Edit message" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Remove from queue" })).toBeNull();
});

// A promotion refused before it was attempted still carries its source entry's
// queueIndex. That index belongs to the message the promotion would consume,
// not to an entry the daemon owns for this row, so the row must stay local: an
// Edit here would race the daemon's entry, and Remove must discard only the
// local promotion rather than cancel that entry.
test("a refused unattempted promotion keeps its own actions, not the queue entry's", async () => {
  const cancelQueued = vi.spyOn(threadsStore.getState(), "cancelQueued").mockResolvedValue(undefined);
  try {
    renderStack(
      [
        row({
          method: "promote",
          state: "waiting",
          queueIndex: 0,
          queueEntryId: "q1",
          outbox: {
            clientMutationId: "m1",
            method: "turn/promoteQueuedAsSteer",
            state: "submitting",
            attempted: false,
            queueEntryIds: ["q1"],
          } as unknown as PendingMessage["outbox"],
        }),
      ],
      context,
    );
    expect(screen.queryByRole("button", { name: "Edit message" })).toBeNull();
    const removeButton = screen.getByRole("button", { name: "Remove from queue" });
    await act(async () => {
      fireEvent.click(removeButton);
    });
    // Remove must not reach the daemon's queue: the index belongs to the entry
    // this promotion would consume, not to a row this action owns.
    expect(cancelQueued).not.toHaveBeenCalled();
  } finally {
    cancelQueued.mockRestore();
  }
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
