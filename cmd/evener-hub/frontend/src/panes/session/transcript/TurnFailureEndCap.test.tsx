import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { ItemModel, ThreadModel, TurnModel } from "../../../protocol/model";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { TurnBlock } from "./TurnBlock";
import { originatingInput, TurnFailureEndCap } from "./TurnFailureEndCap";

beforeEach(() => resetThreadsStoreForTests());
afterEach(cleanup);

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_u", turnId: "turn_1", type: "userMessage", text: "do the thing", ...overrides };
}

function failedTurn(overrides: Partial<TurnModel> = {}): TurnModel {
  return {
    id: "turn_1",
    status: "failed",
    items: [item()],
    error: { message: "the provider exploded" },
    ...overrides,
  };
}

test("renders the taxonomy badge and the error message", () => {
  render(<TurnFailureEndCap error={{ message: "the provider exploded" }} turn={failedTurn()} sessionRef="ref_a" />);
  expect(screen.getByTestId("turn-failure")).toBeTruthy();
  expect(screen.getByText("error")).toBeTruthy(); // no source/cause -> generic badge
  expect(screen.getByText("the provider exploded")).toBeTruthy();
});

test("a provider failure shows a provider-status badge and a Retry action", () => {
  render(
    <TurnFailureEndCap
      error={{ message: "429 rate limited", cause: { kind: "provider", status: 429 } }}
      turn={failedTurn()}
      sessionRef="ref_a"
    />,
  );
  expect(screen.getByText("provider 429")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
});

test("a connection failure offers a Reconnect & retry action", () => {
  render(
    <TurnFailureEndCap
      error={{ message: "local daemon unavailable", source: "hub" }}
      turn={failedTurn()}
      sessionRef="ref_a"
    />,
  );
  expect(screen.getByText("connection")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Reconnect & retry" })).toBeTruthy();
});

test("the hint renders when present", () => {
  render(
    <TurnFailureEndCap
      error={{ message: "boom", hint: "check your API key" }}
      turn={failedTurn()}
      sessionRef="ref_a"
    />,
  );
  expect(screen.getByText("What can I do?")).toBeTruthy();
  expect(screen.queryByText("check your API key")).toBeNull();
});

test("hint sits behind a disclosure; retry is inline in the head row", () => {
  render(
    <TurnFailureEndCap error={{ message: "boom", hint: "check your API key" }} turn={failedTurn()} sessionRef="s1" />,
  );
  const cap = screen.getByTestId("turn-failure");
  const head = cap.firstElementChild as HTMLElement;
  expect(head.contains(screen.getByRole("button", { name: /retry/i }))).toBe(true);
  expect(screen.queryByText("check your API key")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "What can I do?" }));
  expect(screen.getByText("check your API key")).toBeTruthy();
});

test("clicking retry re-issues the turn's user input via threadsStore.send", async () => {
  const sendSpy = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue(undefined);
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={failedTurn()} sessionRef="ref_a" />);
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(sendSpy).toHaveBeenCalledWith("ref_a", "do the thing"));
});

test("without a session ref the diagnostic still renders but the recovery action is withheld", () => {
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={failedTurn()} sessionRef={undefined} />);
  expect(screen.getByTestId("turn-failure")).toBeTruthy();
  expect(screen.getByText("boom")).toBeTruthy();
  expect(screen.queryByRole("button")).toBe(null);
});

test("a failed turn with no user-input item to retry withholds the action even with a ref", () => {
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={failedTurn({ items: [] })} sessionRef="ref_a" />);
  expect(screen.queryByRole("button")).toBe(null);
});

// --- a reloaded failure offers the same recovery a live one does (kata 0wb6)
// One persisted transcript entry is one turn, so a RELOADED failure is a turn
// of its own carrying only the failure item - the input that opened the
// exchange sits in an earlier turn. Retry was therefore offered live and
// withheld after reload, for the same failure.

function seedThread(ref: string, turns: TurnModel[]): void {
  threadsStore.setState({ threads: new Map([[ref, { ref, turns } as unknown as ThreadModel]]) });
}

const RELOADED_FAILURE: TurnModel = {
  id: "turn_2",
  status: "failed",
  items: [{ id: "item_turn_failure_2", turnId: "turn_2", type: "systemMessage", text: "boom", eventKind: "error" }],
  error: { message: "boom" },
};

function reloadedThread(): TurnModel[] {
  return [
    { id: "turn_1", status: "completed", items: [item({ turnId: "turn_1", text: "explain parser.go" })] },
    RELOADED_FAILURE,
  ];
}

test("a reloaded failure offers Retry, sourced from the input that opened the exchange", () => {
  seedThread("ref_a", reloadedThread());
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={RELOADED_FAILURE} sessionRef="ref_a" />);
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
});

test("retrying a reloaded failure re-issues that earlier input", async () => {
  const sendSpy = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue(undefined);
  seedThread("ref_a", reloadedThread());
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={RELOADED_FAILURE} sessionRef="ref_a" />);
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(sendSpy).toHaveBeenCalledWith("ref_a", "explain parser.go"));
});

test("the lookback stops at the failed turn, never re-issuing an input sent after it", () => {
  seedThread("ref_a", [
    ...reloadedThread(),
    { id: "turn_3", status: "completed", items: [item({ turnId: "turn_3", text: "a later, unrelated prompt" })] },
  ]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={RELOADED_FAILURE} sessionRef="ref_a" />);
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  expect(screen.queryByText("a later, unrelated prompt")).toBe(null);
  expect(originatingInput(threadsStore.getState().threads.get("ref_a")?.turns ?? [], "turn_2")).toBe(
    "explain parser.go",
  );
});

test("a thread whose turns hold no user input at all still withholds the action", () => {
  seedThread("ref_a", [RELOADED_FAILURE]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={RELOADED_FAILURE} sessionRef="ref_a" />);
  expect(screen.queryByRole("button")).toBe(null);
});

test("originatingInput skips a whitespace-only input rather than re-issuing nothing", () => {
  const turns: TurnModel[] = [
    { id: "turn_1", status: "completed", items: [item({ turnId: "turn_1", text: "real work" })] },
    { id: "turn_2", status: "completed", items: [item({ turnId: "turn_2", id: "item_blank", text: "   " })] },
    RELOADED_FAILURE,
  ];
  expect(originatingInput(turns, "turn_2")).toBe("real work");
});

test("originatingInput takes the LAST input at or before the failed turn", () => {
  const turns: TurnModel[] = [
    { id: "turn_1", status: "completed", items: [item({ turnId: "turn_1", text: "first" })] },
    { id: "turn_2", status: "completed", items: [item({ turnId: "turn_2", id: "item_u2", text: "second" })] },
    RELOADED_FAILURE,
  ];
  expect(originatingInput(turns, "turn_2")).toBe("second");
});

// --- TurnBlock integration: the end-cap is driven by turn.error presence ----

test("TurnBlock renders the failure end-cap for a failed turn (turn.error present)", () => {
  render(<TurnBlock turn={failedTurn()} sessionRef="ref_a" />);
  expect(screen.getByTestId("turn-failure")).toBeTruthy();
});

test("TurnBlock renders NO end-cap for a clean turn (no error)", () => {
  render(<TurnBlock turn={{ id: "turn_2", status: "completed", items: [item()] }} sessionRef="ref_a" />);
  expect(screen.queryByTestId("turn-failure")).toBe(null);
});
