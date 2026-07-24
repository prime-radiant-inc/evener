import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { Popover } from "./index";

afterEach(() => cleanup());

// The §3.4 guarantee: the panel is an out-of-flow overlay portaled to
// document.body, so opening it never pushes page content down (never reflows).

test("the panel is not in the document while closed", () => {
  render(
    <Popover open={false} onClose={() => {}} trigger={<button type="button">open</button>} data-testid="panel">
      <div>panel body</div>
    </Popover>,
  );
  expect(screen.queryByTestId("panel")).toBeNull();
});

test("when open, the panel is portaled to document.body, not nested under the trigger wrapper", () => {
  render(
    <Popover open onClose={() => {}} trigger={<button type="button">open</button>} data-testid="panel">
      <div>panel body</div>
    </Popover>,
  );
  const panel = screen.getByTestId("panel");
  expect(panel).toBeTruthy();

  // The trigger button lives inside Popover's in-flow wrapper <span>; the
  // panel must NOT share that wrapper (that would reflow). Walk the panel's
  // ancestry: it reaches document.body without passing through the trigger's
  // own wrapper span.
  const triggerButton = screen.getByRole("button", { name: "open" });
  const triggerWrapper = triggerButton.parentElement;
  expect(triggerWrapper).not.toBeNull();

  let ancestor: HTMLElement | null = panel.parentElement;
  let reachedBody = false;
  while (ancestor) {
    expect(ancestor).not.toBe(triggerWrapper);
    if (ancestor === document.body) {
      reachedBody = true;
      break;
    }
    ancestor = ancestor.parentElement;
  }
  expect(reachedBody).toBe(true);
});
