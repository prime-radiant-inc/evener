import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { HoverCard } from ".";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

test("the plain-node gallery child keeps the automatic description association", () => {
  vi.useFakeTimers();
  render(
    <HoverCard label={<div>Tests passed in 14 seconds</div>}>
      <button type="button">job_01HZX7P9</button>
    </HoverCard>,
  );
  const trigger = screen.getByRole("button", { name: "job_01HZX7P9" });
  expect(trigger.getAttribute("aria-describedby")).toBeNull();

  fireEvent.focus(trigger);
  act(() => vi.advanceTimersByTime(300));

  const card = screen.getByRole("tooltip");
  expect(trigger.getAttribute("aria-describedby")).toBe(card.id);
  fireEvent.blur(trigger);
  expect(screen.queryByRole("tooltip")).toBeNull();
  expect(trigger.getAttribute("aria-describedby")).toBeNull();
});
