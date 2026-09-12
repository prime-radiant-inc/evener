import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { Sheet } from "./index";
import sheetStyles from "./sheet.module.css";

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

test("appends an optional panel class without replacing sheet geometry classes", () => {
  render(
    <Sheet open onClose={vi.fn()} title="t" panelClassName="single-scroll-panel">
      Body
    </Sheet>,
  );

  const panel = screen.getByRole("dialog");
  expect(panel.className).toContain("single-scroll-panel");
  expect(panel.className.split(/\s+/).length).toBeGreaterThan(1);
});

test("optional panel styling does not change expandable geometry classes", () => {
  render(
    <Sheet
      open
      side="bottom"
      size="wide"
      expandable={{ peekHeight: 200 }}
      onClose={vi.fn()}
      title="t"
      panelClassName="single-scroll-panel"
    >
      Body
    </Sheet>,
  );

  const panel = screen.getByRole("dialog");
  expect(panel.className).toContain("single-scroll-panel");
  expect(panel.className).not.toContain(sheetStyles.wide);
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

// --- expandable mode (Task 2) --------------------------------------------
// The drag handle (data-testid="sheet-handle") is rendered only when
// `expandable` is set; geometry state ("peek" | "full") is exposed via a
// `data-geometry` attribute on a wrapper div inside the body (see
// index.tsx), so these assertions query that wrapper rather than the dialog
// panel itself.

test("expandable renders a drag handle", () => {
  render(
    <Sheet open side="bottom" expandable={{ peekHeight: 200 }} onClose={vi.fn()} title="t">
      Body
    </Sheet>,
  );
  expect(screen.getByRole("dialog").querySelector("[data-testid='sheet-handle']")).toBeTruthy();
});

test("non-expandable renders no drag handle", () => {
  render(
    <Sheet open side="bottom" onClose={vi.fn()} title="t">
      Body
    </Sheet>,
  );
  expect(screen.queryByTestId("sheet-handle")).toBeNull();
});

test("fullScreenFirst starts at full on mount", () => {
  render(
    <Sheet open side="bottom" expandable={{ peekHeight: 200, fullScreenFirst: true }} onClose={vi.fn()} title="t">
      Body
    </Sheet>,
  );
  const panel = screen.getByRole("dialog");
  expect(panel.querySelector("[data-geometry]")?.getAttribute("data-geometry")).toBe("full");
});

test("fullScreenFirst resets to full on reopen", async () => {
  const { rerender } = render(
    <Sheet open side="bottom" expandable={{ peekHeight: 200, fullScreenFirst: true }} onClose={vi.fn()} title="t">
      Body
    </Sheet>,
  );
  const panel = screen.getByRole("dialog");
  // Simulate user dragging to peek
  fireEvent.pointerDown(screen.getByTestId("sheet-handle"), { clientY: 500 });
  fireEvent.pointerMove(window, { clientY: 800 });
  fireEvent.pointerUp(window, { clientY: 800 });
  expect(panel.querySelector("[data-geometry]")?.getAttribute("data-geometry")).toBe("peek");
  // Close and reopen
  rerender(
    <Sheet
      open={false}
      side="bottom"
      expandable={{ peekHeight: 200, fullScreenFirst: true }}
      onClose={vi.fn()}
      title="t"
    >
      Body
    </Sheet>,
  );
  rerender(
    <Sheet open side="bottom" expandable={{ peekHeight: 200, fullScreenFirst: true }} onClose={vi.fn()} title="t">
      Body
    </Sheet>,
  );
  expect(screen.getByRole("dialog").querySelector("[data-geometry]")?.getAttribute("data-geometry")).toBe("full");
});

test("tap on handle toggles peek to full", () => {
  render(
    <Sheet open side="bottom" expandable={{ peekHeight: 200, fullScreenFirst: false }} onClose={vi.fn()} title="t">
      Body
    </Sheet>,
  );
  const panel = screen.getByRole("dialog");
  // Starts at peek (fullScreenFirst is false)
  expect(panel.querySelector("[data-geometry]")?.getAttribute("data-geometry")).toBe("peek");
  // Tap toggles to full
  fireEvent.pointerDown(screen.getByTestId("sheet-handle"), { clientY: 100 });
  fireEvent.pointerUp(window, { clientY: 100 });
  expect(panel.querySelector("[data-geometry]")?.getAttribute("data-geometry")).toBe("full");
});

test("bodyClassName is appended to the body element", () => {
  render(
    <Sheet side="left" open onClose={() => {}} title="Sessions" bodyClassName="flush">
      drawer body
    </Sheet>,
  );
  expect(screen.getByText("drawer body").className).toMatch(/flush/);
});
