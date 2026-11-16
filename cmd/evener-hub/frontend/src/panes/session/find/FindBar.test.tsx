import { cleanup, fireEvent, render } from "@testing-library/react";
import { createPortal } from "react-dom";
import { afterEach, expect, test, vi } from "vitest";
import { FindBar } from "./FindBar";
import type { FindView } from "./useFindInSession";

afterEach(() => {
  cleanup();
  document.body.innerHTML = "";
});

function view(overrides: Partial<FindView> = {}): FindView {
  return {
    query: "needle",
    status: "matches",
    position: 1,
    count: 2,
    moreHistory: false,
    error: null,
    canStepOlder: true,
    canStepNewer: false,
    focusEpoch: 0,
    setQuery: vi.fn(),
    step: vi.fn(),
    close: vi.fn(),
    ...overrides,
  };
}

test("Enter and Shift+Enter step from the field, in a popped-out window too", () => {
  // A popped-out session renders into another window, whose elements are not
  // instances of this window's classes.
  const frame = document.createElement("iframe");
  document.body.appendChild(frame);
  const elsewhere = frame.contentDocument?.body;
  if (!elsewhere) throw new Error("no popout document");
  const shown = view();
  render(createPortal(<FindBar view={shown} />, elsewhere));
  const field = elsewhere.querySelector("input");
  if (!field) throw new Error("no field");
  expect(field instanceof HTMLInputElement).toBe(false);

  fireEvent.keyDown(field, { key: "Enter" });
  expect(shown.step).toHaveBeenLastCalledWith(-1);
  fireEvent.keyDown(field, { key: "Enter", shiftKey: true });
  expect(shown.step).toHaveBeenLastCalledWith(1);
  // Enter on one of the bar's buttons is that button's own key.
  const button = elsewhere.querySelector("button");
  if (!button) throw new Error("no button");
  vi.mocked(shown.step).mockClear();
  fireEvent.keyDown(button, { key: "Enter" });
  expect(shown.step).not.toHaveBeenCalled();
});
