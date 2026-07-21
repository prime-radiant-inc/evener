import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { Button } from "../button";
import { Tooltip } from "./index";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

// The show-delay timer fires outside any React-tracked event, so advancing
// it must be wrapped in act() or the resulting setVisible(true) update
// isn't flushed before the next assertion reads the DOM.
function advance(ms: number) {
  act(() => {
    vi.advanceTimersByTime(ms);
  });
}

test("renders its trigger children", () => {
  render(
    <Tooltip label="Save your changes">
      <button>Save</button>
    </Tooltip>,
  );
  expect(screen.getByRole("button", { name: "Save" })).toBeTruthy();
});

test("the tooltip is not shown initially", () => {
  render(
    <Tooltip label="Save your changes">
      <button>Save</button>
    </Tooltip>,
  );
  expect(screen.queryByRole("tooltip")).toBeNull();
});

test("hovering shows the tooltip only after a 300ms delay", () => {
  vi.useFakeTimers();
  render(
    <Tooltip label="Save your changes">
      <button>Save</button>
    </Tooltip>,
  );
  fireEvent.mouseEnter(screen.getByRole("button", { name: "Save" }));
  expect(screen.queryByRole("tooltip")).toBeNull();

  advance(299);
  expect(screen.queryByRole("tooltip")).toBeNull();

  advance(1);
  expect(screen.getByRole("tooltip").textContent).toBe("Save your changes");
});

test("moving the mouse away before the delay elapses cancels the show", () => {
  vi.useFakeTimers();
  render(
    <Tooltip label="Save your changes">
      <button>Save</button>
    </Tooltip>,
  );
  const trigger = screen.getByRole("button", { name: "Save" });
  fireEvent.mouseEnter(trigger);
  advance(150);
  fireEvent.mouseLeave(trigger);
  advance(300);
  expect(screen.queryByRole("tooltip")).toBeNull();
});

test("moving the mouse away after the tooltip is showing hides it immediately", () => {
  vi.useFakeTimers();
  render(
    <Tooltip label="Save your changes">
      <button>Save</button>
    </Tooltip>,
  );
  const trigger = screen.getByRole("button", { name: "Save" });
  fireEvent.mouseEnter(trigger);
  advance(300);
  expect(screen.getByRole("tooltip")).toBeTruthy();

  fireEvent.mouseLeave(trigger);
  expect(screen.queryByRole("tooltip")).toBeNull();
});

test("keyboard focus shows the tooltip after the same delay, blur hides it immediately", () => {
  vi.useFakeTimers();
  render(
    <Tooltip label="Save your changes">
      <button>Save</button>
    </Tooltip>,
  );
  const trigger = screen.getByRole("button", { name: "Save" });
  fireEvent.focus(trigger);
  expect(screen.queryByRole("tooltip")).toBeNull();
  advance(300);
  expect(screen.getByRole("tooltip")).toBeTruthy();

  fireEvent.blur(trigger);
  expect(screen.queryByRole("tooltip")).toBeNull();
});

test("associates a single native-element trigger with the tooltip via aria-describedby while visible", () => {
  vi.useFakeTimers();
  render(
    <Tooltip label="Save your changes">
      <button>Save</button>
    </Tooltip>,
  );
  const trigger = screen.getByRole("button", { name: "Save" });
  expect(trigger.getAttribute("aria-describedby")).toBeNull();

  fireEvent.focus(trigger);
  advance(300);
  const tooltip = screen.getByRole("tooltip");
  expect(trigger.getAttribute("aria-describedby")).toBe(tooltip.id);

  fireEvent.blur(trigger);
  expect(trigger.getAttribute("aria-describedby")).toBeNull();
});

// --- fix-wave: integration test with the real Button widget (Important) ---
// Before Button forwarded its ref and spread rest props onto its own
// <button> (see button.test.tsx), the cloneElement aria-describedby prop
// below still reached Button's props object but had nowhere to go once
// there - Button's render body only ever used its fixed, explicit prop
// list, so the description was silently dropped. This exercises the real
// cross-widget composition, not a synthetic stand-in, so a regression in
// either widget breaks it.

test("wrapping the real Button widget: focusing shows the tooltip and associates it via aria-describedby", () => {
  vi.useFakeTimers();
  render(
    <Tooltip label="Save your changes">
      <Button>Save</Button>
    </Tooltip>,
  );
  const trigger = screen.getByRole("button", { name: "Save" });
  expect(trigger.getAttribute("aria-describedby")).toBeNull();

  fireEvent.focus(trigger);
  advance(300);
  const tooltip = screen.getByRole("tooltip");
  expect(trigger.getAttribute("aria-describedby")).toBe(tooltip.id);

  fireEvent.blur(trigger);
  expect(trigger.getAttribute("aria-describedby")).toBeNull();
});

test("wrapping the real Button widget: hovering shows the tooltip and associates it via aria-describedby", () => {
  vi.useFakeTimers();
  render(
    <Tooltip label="Save your changes">
      <Button>Save</Button>
    </Tooltip>,
  );
  const trigger = screen.getByRole("button", { name: "Save" });

  fireEvent.mouseEnter(trigger);
  advance(300);
  const tooltip = screen.getByRole("tooltip");
  expect(trigger.getAttribute("aria-describedby")).toBe(tooltip.id);

  fireEvent.mouseLeave(trigger);
  expect(trigger.getAttribute("aria-describedby")).toBeNull();
});

test("degrades gracefully (no crash, no aria wiring) when children is not a single element", () => {
  vi.useFakeTimers();
  render(
    <Tooltip label="Info">
      <span>Text</span>
      <span>More text</span>
    </Tooltip>,
  );
  fireEvent.mouseEnter(screen.getByText("Text").parentElement!);
  advance(300);
  expect(screen.getByRole("tooltip")).toBeTruthy();
});

test("never traps focus: Tab moves through and past the trigger normally", async () => {
  const user = userEvent.setup();
  render(
    <div>
      <button>Before</button>
      <Tooltip label="Save your changes">
        <button>Save</button>
      </Tooltip>
      <button>After</button>
    </div>,
  );
  screen.getByRole("button", { name: "Before" }).focus();
  await user.tab();
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "Save" }));
  await user.tab();
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "After" }));
});

test("is hidden from touch (no hover capability) via CSS, using only tokens", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "tooltip.module.css"), "utf8");
  expect(css).toMatch(/@media \(hover: none\)/);
});
