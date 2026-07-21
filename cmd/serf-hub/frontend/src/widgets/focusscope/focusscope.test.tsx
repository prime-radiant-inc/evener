import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test } from "vitest";
import { FocusScope } from "./index";

afterEach(cleanup);

test("renders its children", () => {
  render(
    <FocusScope>
      <button>Go</button>
    </FocusScope>,
  );
  expect(screen.getByRole("button", { name: "Go" })).toBeTruthy();
});

test("on mount, focuses the first tabbable descendant", () => {
  render(
    <FocusScope>
      <button>First</button>
      <button>Second</button>
    </FocusScope>,
  );
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "First" }));
});

test("on mount, skips a disabled or hidden descendant to focus the first real tabbable one", () => {
  render(
    <FocusScope>
      <button disabled>Disabled</button>
      <button hidden>Hidden</button>
      <button>Reachable</button>
    </FocusScope>,
  );
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "Reachable" }));
});

test("on mount, skips an element with tabindex=-1", () => {
  render(
    <FocusScope>
      <button tabIndex={-1}>Skip me</button>
      <button>Land here</button>
    </FocusScope>,
  );
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "Land here" }));
});

test("with no tabbable descendants, focuses the scope itself as a fallback", () => {
  const { container } = render(
    <FocusScope>
      <p>Nothing focusable here.</p>
    </FocusScope>,
  );
  expect(document.activeElement).toBe(container.firstElementChild);
});

test("restores focus to the previously focused element when the scope unmounts", () => {
  render(<button>Outside trigger</button>);
  const trigger = screen.getByRole("button", { name: "Outside trigger" });
  trigger.focus();
  expect(document.activeElement).toBe(trigger);

  const { unmount } = render(
    <FocusScope>
      <button>Inside</button>
    </FocusScope>,
  );
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "Inside" }));

  unmount();
  expect(document.activeElement).toBe(trigger);
});

test("does not throw restoring focus when the previous element was removed from the DOM", () => {
  const { unmount: unmountTrigger } = render(<button>Gone soon</button>);
  const trigger = screen.getByRole("button", { name: "Gone soon" });
  trigger.focus();
  unmountTrigger(); // trigger leaves the document entirely before the scope unmounts

  const { unmount } = render(
    <FocusScope>
      <button>Inside</button>
    </FocusScope>,
  );
  expect(() => unmount()).not.toThrow();
});

test("trap=false (default): Tab is not intercepted, focus moves past the scope normally", async () => {
  const user = userEvent.setup();
  render(
    <div>
      <FocusScope>
        <button>First</button>
        <button>Last</button>
      </FocusScope>
      <button>Outside</button>
    </div>,
  );
  screen.getByRole("button", { name: "Last" }).focus();
  await user.tab();
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "Outside" }));
});

test("trap=true: Tab from the last tabbable loops back to the first", async () => {
  const user = userEvent.setup();
  render(
    <div>
      <FocusScope trap>
        <button>First</button>
        <button>Last</button>
      </FocusScope>
      <button>Outside</button>
    </div>,
  );
  screen.getByRole("button", { name: "Last" }).focus();
  await user.tab();
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "First" }));
});

test("trap=true: Shift+Tab from the first loops back to the last", async () => {
  const user = userEvent.setup();
  render(
    <div>
      <FocusScope trap>
        <button>First</button>
        <button>Last</button>
      </FocusScope>
      <button>Outside</button>
    </div>,
  );
  screen.getByRole("button", { name: "First" }).focus();
  await user.tab({ shift: true });
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "Last" }));
});

test("trap=true: Tab in the middle of the scope moves naturally between items, not just first/last", async () => {
  const user = userEvent.setup();
  render(
    <FocusScope trap>
      <button>First</button>
      <button>Middle</button>
      <button>Last</button>
    </FocusScope>,
  );
  screen.getByRole("button", { name: "First" }).focus();
  await user.tab();
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "Middle" }));
});

test("trap=true: Tab with zero tabbable descendants keeps focus on the scope itself", async () => {
  const user = userEvent.setup();
  const { container } = render(
    <div>
      <FocusScope trap>
        <p>Nothing focusable here.</p>
      </FocusScope>
      <button>Outside</button>
    </div>,
  );
  expect(document.activeElement).toBe(container.querySelector("[tabindex='-1']"));
  await user.tab();
  expect(document.activeElement).toBe(container.querySelector("[tabindex='-1']"));
});
