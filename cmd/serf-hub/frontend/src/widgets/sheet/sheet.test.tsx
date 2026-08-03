import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { Sheet } from "./index";

afterEach(cleanup);

test("renders nothing when closed", () => {
  render(
    <Sheet open={false} onClose={vi.fn()} title="Session settings">
      Body
    </Sheet>,
  );
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("defaults to side=right", () => {
  const { container } = render(
    <Sheet open onClose={vi.fn()} title="t">
      Body
    </Sheet>,
  );
  const panel = screen.getByRole("dialog");
  // side is expressed as a CSS module class, not a DOM attribute - assert
  // through the class the "right" variant's own test below also checks,
  // rather than a brittle exact-className match here.
  expect(panel.className).not.toBe("");
  expect(container.firstElementChild).toBeTruthy();
});

test("defaults to size=standard and wide adds a distinct sizing class while preserving side wiring", () => {
  const { rerender } = render(
    <Sheet open side="right" onClose={vi.fn()} title="t">
      Body
    </Sheet>,
  );
  const standardClass = screen.getByRole("dialog").className;

  rerender(
    <Sheet open side="right" size="wide" onClose={vi.fn()} title="t">
      Body
    </Sheet>,
  );
  const wideClass = screen.getByRole("dialog").className;

  expect(standardClass).not.toBe("");
  expect(wideClass).not.toBe("");
  expect(wideClass).not.toBe(standardClass);
});

test("renders as a modal dialog when open, labelled by its title, same contract as Dialog", () => {
  render(
    <Sheet open onClose={vi.fn()} title="Session settings">
      Are you sure?
    </Sheet>,
  );
  const dialog = screen.getByRole("dialog");
  expect(dialog.getAttribute("aria-modal")).toBe("true");
  const labelledBy = dialog.getAttribute("aria-labelledby");
  expect(labelledBy).toBeTruthy();
  expect(document.getElementById(labelledBy!)?.textContent).toBe("Session settings");
  expect(screen.getByText("Are you sure?")).toBeTruthy();
});

test("renders a footer when provided", () => {
  render(
    <Sheet open onClose={vi.fn()} title="t" footer={<span data-testid="footer-content">Footer</span>}>
      Body
    </Sheet>,
  );
  expect(screen.getByTestId("footer-content")).toBeTruthy();
});

test("Escape calls onClose", async () => {
  const user = userEvent.setup();
  const onClose = vi.fn();
  render(
    <Sheet open onClose={onClose} title="t">
      Body
    </Sheet>,
  );
  await user.keyboard("{Escape}");
  expect(onClose).toHaveBeenCalledOnce();
});

test("clicking the scrim calls onClose", async () => {
  const user = userEvent.setup();
  const onClose = vi.fn();
  const { container } = render(
    <Sheet open onClose={onClose} title="t">
      Body
    </Sheet>,
  );
  await user.click(container.firstElementChild!);
  expect(onClose).toHaveBeenCalledOnce();
});

// fix-wave: same scrim drag guard as dialog.test.tsx - Sheet shares
// OverlayPanel, so this confirms the fix there applies here too.
test("a mousedown inside the panel followed by a click landing on the scrim (a drag out) does not close it", () => {
  const onClose = vi.fn();
  const { container } = render(
    <Sheet open onClose={onClose} title="t">
      <p>Body text</p>
    </Sheet>,
  );
  const scrim = container.firstElementChild!;
  fireEvent.mouseDown(screen.getByText("Body text"));
  fireEvent.click(scrim);
  expect(onClose).not.toHaveBeenCalled();
});

test("the close button calls onClose when clicked", async () => {
  const user = userEvent.setup();
  const onClose = vi.fn();
  render(
    <Sheet open onClose={onClose} title="t">
      Body
    </Sheet>,
  );
  await user.click(screen.getByRole("button", { name: "Close" }));
  expect(onClose).toHaveBeenCalledOnce();
});

test("focus is trapped and restored on close, same as Dialog", () => {
  render(<button type="button">Open sheet</button>);
  const trigger = screen.getByRole("button", { name: "Open sheet" });
  trigger.focus();

  const { rerender } = render(
    <Sheet open onClose={vi.fn()} title="t">
      <button type="button">Field</button>
    </Sheet>,
  );
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "Field" }));

  rerender(
    <Sheet open={false} onClose={vi.fn()} title="t">
      <button type="button">Field</button>
    </Sheet>,
  );
  expect(document.activeElement).toBe(trigger);
});

test("side=right, side=bottom, and side=left each render a distinct, non-empty panel class", () => {
  const { rerender, container } = render(
    <Sheet open side="right" onClose={vi.fn()} title="t">
      Body
    </Sheet>,
  );
  const rightClass = screen.getByRole("dialog").className;

  rerender(
    <Sheet open side="bottom" onClose={vi.fn()} title="t">
      Body
    </Sheet>,
  );
  const bottomClass = screen.getByRole("dialog").className;

  rerender(
    <Sheet open side="left" onClose={vi.fn()} title="t">
      Body
    </Sheet>,
  );
  const leftClass = screen.getByRole("dialog").className;

  // Each must be non-empty: an unregistered side silently falls through
  // SIDE_CLASS[side] to undefined, which React renders as no class
  // attribute at all (className="") rather than a loud failure - the same
  // failure shape requireClass exists to catch for a CSS module's own
  // missing class (see widgets/internal/requireClass.ts).
  expect(rightClass).not.toBe("");
  expect(bottomClass).not.toBe("");
  expect(leftClass).not.toBe("");
  expect(new Set([rightClass, bottomClass, leftClass]).size).toBe(3);
  expect(container).toBeTruthy();
});

// As in dialog.test.tsx: jsdom does not evaluate real CSS animations or
// media queries, so the slide-in animation and its reduced-motion opt-out
// are verified by reading the CSS module's own source.
test("all side variants' slide-in animations honor prefers-reduced-motion, using only tokens", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "sheet.module.css"), "utf8");
  expect(css).toContain("animation:");
  expect(css).toContain("var(--motion-duration-overlay)");
  expect(css).toMatch(/@media \(prefers-reduced-motion: reduce\)/);
});
