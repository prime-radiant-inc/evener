import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { ItemModel, TurnModel } from "../../../protocol/model";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { TurnBlock } from "./TurnBlock";
import { TurnFailureEndCap } from "./TurnFailureEndCap";

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

// --- TurnBlock integration: the end-cap is driven by turn.error presence ----

test("TurnBlock renders the failure end-cap for a failed turn (turn.error present)", () => {
  render(<TurnBlock turn={failedTurn()} sessionRef="ref_a" />);
  expect(screen.getByTestId("turn-failure")).toBeTruthy();
});

test("TurnBlock renders NO end-cap for a clean turn (no error)", () => {
  render(<TurnBlock turn={{ id: "turn_2", status: "completed", items: [item()] }} sessionRef="ref_a" />);
  expect(screen.queryByTestId("turn-failure")).toBe(null);
});
