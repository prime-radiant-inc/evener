import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { Tooltip } from "./index";
import { hoverForTooltip } from "./tooltipTestUtils";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

function renderSave(): HTMLElement {
  render(
    <Tooltip label="Save your changes">
      <button type="button">Save</button>
    </Tooltip>,
  );
  return screen.getByRole("button", { name: "Save" });
}

test("hoverForTooltip returns the shown tooltip and leaves the test on real timers", () => {
  const trigger = renderSave();

  expect(hoverForTooltip(trigger).textContent).toBe("Save your changes");
  expect(vi.isFakeTimers()).toBe(false);
});

test("hoverForTooltip refuses to replace a test's own fake clock", () => {
  const trigger = renderSave();
  vi.useFakeTimers();

  expect(() => hoverForTooltip(trigger)).toThrow("call it on real timers");
  expect(vi.isFakeTimers()).toBe(true);
  expect(screen.queryByRole("tooltip")).toBeNull();
});
