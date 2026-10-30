import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRoot } from "react-dom/client";
import { expect, test } from "vitest";
import { FocusScope } from "./index";

test("renders its children", () => {
  render(
    <FocusScope>
      <button type="button">Go</button>
    </FocusScope>,
  );
  expect(screen.getByRole("button", { name: "Go" })).toBeTruthy();
});

test("on mount, focuses the first tabbable descendant", () => {
  render(
    <FocusScope>
      <button type="button">First</button>
      <button type="button">Second</button>
    </FocusScope>,
  );
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "First" }));
});

test("on mount, skips a disabled or hidden descendant to focus the first real tabbable one", () => {
  render(
    <FocusScope>
      <button type="button" disabled>
        Disabled
      </button>
      <button type="button" hidden>
        Hidden
      </button>
      <button type="button">Reachable</button>
    </FocusScope>,
  );
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "Reachable" }));
});

test("on mount, skips an element with tabindex=-1", () => {
  render(
    <FocusScope>
      <button type="button" tabIndex={-1}>
        Skip me
      </button>
      <button type="button">Land here</button>
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
  render(<button type="button">Outside trigger</button>);
  const trigger = screen.getByRole("button", { name: "Outside trigger" });
  trigger.focus();
  expect(document.activeElement).toBe(trigger);

  const { unmount } = render(
    <FocusScope>
      <button type="button">Inside</button>
    </FocusScope>,
  );
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "Inside" }));

  unmount();
  expect(document.activeElement).toBe(trigger);
});

test("does not throw restoring focus when the previous element was removed from the DOM", () => {
  const { unmount: unmountTrigger } = render(<button type="button">Gone soon</button>);
  const trigger = screen.getByRole("button", { name: "Gone soon" });
  trigger.focus();
  unmountTrigger(); // trigger leaves the document entirely before the scope unmounts

  const { unmount } = render(
    <FocusScope>
      <button type="button">Inside</button>
    </FocusScope>,
  );
  expect(() => unmount()).not.toThrow();
});

test("trap=false (default): Tab is not intercepted, focus moves past the scope normally", async () => {
  const user = userEvent.setup();
  render(
    <div>
      <FocusScope>
        <button type="button">First</button>
        <button type="button">Last</button>
      </FocusScope>
      <button type="button">Outside</button>
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
        <button type="button">First</button>
        <button type="button">Last</button>
      </FocusScope>
      <button type="button">Outside</button>
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
        <button type="button">First</button>
        <button type="button">Last</button>
      </FocusScope>
      <button type="button">Outside</button>
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
      <button type="button">First</button>
      <button type="button">Middle</button>
      <button type="button">Last</button>
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
      <button type="button">Outside</button>
    </div>,
  );
  expect(document.activeElement).toBe(container.querySelector("[tabindex='-1']"));
  await user.tab();
  expect(document.activeElement).toBe(container.querySelector("[tabindex='-1']"));
});

// A Dockview popout is its own document. The scope has to read the active
// element and restore focus within that document; the opener's document knows
// neither the focused element nor the element it should return to.
// Dockview *moves* existing DOM into a popout: the node changes document but
// keeps its original realm and prototype, so `instanceof` from either window
// rejects it. The scope must still restore focus to an adopted trigger.
test("a scope restores focus to a trigger adopted from another document", ({ onTestFinished }) => {
  const frame = document.createElement("iframe");
  document.body.appendChild(frame);
  const popout = frame.contentDocument;
  if (!popout) throw new Error("missing popout document");
  // Created here, adopted there: exactly what a moved panel does.
  const adopted = document.createElement("button");
  adopted.textContent = "adopted trigger";
  popout.body.appendChild(adopted);
  adopted.focus();
  expect(popout.activeElement).toBe(adopted);

  const host = popout.createElement("div");
  popout.body.appendChild(host);
  const root = createRoot(host);
  onTestFinished(() => frame.remove());
  act(() =>
    root.render(
      <FocusScope>
        <button type="button">First</button>
      </FocusScope>,
    ),
  );
  expect(popout.activeElement?.textContent).toBe("First");

  act(() => root.unmount());
  expect(popout.activeElement).toBe(adopted);
});

test("a scope in a popout focuses and restores within its own document", ({ onTestFinished }) => {
  const frame = document.createElement("iframe");
  document.body.appendChild(frame);
  const popout = frame.contentDocument;
  if (!popout) throw new Error("missing popout document");
  const host = popout.createElement("div");
  popout.body.appendChild(host);
  const opener = popout.createElement("button");
  opener.textContent = "before the scope";
  popout.body.appendChild(opener);
  opener.focus();
  const root = createRoot(host);
  onTestFinished(() => {
    act(() => root.unmount());
    frame.remove();
  });

  act(() =>
    root.render(
      <FocusScope>
        <button type="button">First</button>
      </FocusScope>,
    ),
  );
  expect(popout.activeElement?.textContent).toBe("First");
  expect(document.activeElement).not.toBe(popout.activeElement);

  act(() => root.unmount());
  expect(popout.activeElement).toBe(opener);
});
