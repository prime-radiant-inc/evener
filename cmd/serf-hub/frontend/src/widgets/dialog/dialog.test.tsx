import { afterEach, test, expect, vi } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Dialog } from "./index";

afterEach(cleanup);

test("renders nothing when closed", () => {
  render(
    <Dialog open={false} onClose={vi.fn()} title="Delete session">
      Body
    </Dialog>,
  );
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("renders as a modal dialog when open, labelled by its title", () => {
  render(
    <Dialog open onClose={vi.fn()} title="Delete session">
      Are you sure?
    </Dialog>,
  );
  const dialog = screen.getByRole("dialog");
  expect(dialog.getAttribute("aria-modal")).toBe("true");
  const labelledBy = dialog.getAttribute("aria-labelledby");
  expect(labelledBy).toBeTruthy();
  expect(document.getElementById(labelledBy!)?.textContent).toBe("Delete session");
  expect(screen.getByText("Are you sure?")).toBeTruthy();
});

test("renders a footer when provided, and none when omitted", () => {
  const { rerender } = render(
    <Dialog open onClose={vi.fn()} title="t" footer={<span data-testid="footer-content">Footer</span>}>
      Body
    </Dialog>,
  );
  expect(screen.getByTestId("footer-content")).toBeTruthy();

  rerender(
    <Dialog open onClose={vi.fn()} title="t">
      Body
    </Dialog>,
  );
  expect(screen.queryByTestId("footer-content")).toBeNull();
});

test("Escape calls onClose", async () => {
  const user = userEvent.setup();
  const onClose = vi.fn();
  render(
    <Dialog open onClose={onClose} title="t">
      Body
    </Dialog>,
  );
  await user.keyboard("{Escape}");
  expect(onClose).toHaveBeenCalledOnce();
});

test("clicking the scrim (outside the panel) calls onClose", async () => {
  const user = userEvent.setup();
  const onClose = vi.fn();
  const { container } = render(
    <Dialog open onClose={onClose} title="t">
      Body
    </Dialog>,
  );
  // The scrim is the outermost element Dialog renders (dialog role sits
  // two levels down: scrim > FocusScope's wrapper > the panel itself), so
  // a click that lands directly on it - not bubbled from a descendant -
  // simulates a click on the backdrop, outside the panel.
  const scrim = container.firstElementChild!;
  await user.click(scrim);
  expect(onClose).toHaveBeenCalledOnce();
});

test("clicking inside the panel does not call onClose", async () => {
  const user = userEvent.setup();
  const onClose = vi.fn();
  render(
    <Dialog open onClose={onClose} title="t">
      <p>Body text</p>
    </Dialog>,
  );
  await user.click(screen.getByText("Body text"));
  expect(onClose).not.toHaveBeenCalled();
});

// --- fix-wave: scrim drag guard (Important) -----------------------------
// A click event's target reflects where the mouse was released, not where
// the press started - so selecting body text and dragging the release
// point out onto the scrim previously produced a click whose target WAS
// the scrim (indistinguishable from a genuine backdrop click) even though
// the interaction began inside the panel. See OverlayPanel.tsx's own
// comment on scrimPressStartedOnScrimRef for the fuller picture, including
// the REVERSE drag (press on the scrim, release inside the panel): a real
// browser computes that click's target as the nearest common ancestor of
// the mousedown/mouseup targets, which - since the scrim contains the
// whole panel - is the scrim itself (verified live in a real browser, not
// just reasoned about; jsdom's fireEvent does not reproduce this
// ancestor-collapsing computation, so it can't be exercised as a jsdom
// test distinct from "mousedown and click both landing on the scrim"
// below - that test's fireEvent calls ARE what a real reverse-drag
// produces as input to this component once account for it, by design:
// the scrim-press-outside-the-panel is what should close it, matching
// Radix's pointer-down-outside pattern, not requiring the release to
// symmetrically land on the scrim too).

test("a mousedown inside the panel followed by a click landing on the scrim (a drag out) does not close it", () => {
  const onClose = vi.fn();
  const { container } = render(
    <Dialog open onClose={onClose} title="t">
      <p>Body text</p>
    </Dialog>,
  );
  const scrim = container.firstElementChild!;
  fireEvent.mouseDown(screen.getByText("Body text"));
  fireEvent.click(scrim);
  expect(onClose).not.toHaveBeenCalled();
});

test("a mousedown and click both landing on the scrim closes it (this is also what a reverse drag - press on the scrim, release inside the panel - produces, per the block comment above)", () => {
  const onClose = vi.fn();
  const { container } = render(
    <Dialog open onClose={onClose} title="t">
      Body
    </Dialog>,
  );
  const scrim = container.firstElementChild!;
  fireEvent.mouseDown(scrim);
  fireEvent.click(scrim);
  expect(onClose).toHaveBeenCalledOnce();
});

test("the close button calls onClose when clicked", async () => {
  const user = userEvent.setup();
  const onClose = vi.fn();
  render(
    <Dialog open onClose={onClose} title="t">
      Body
    </Dialog>,
  );
  await user.click(screen.getByRole("button", { name: "Close" }));
  expect(onClose).toHaveBeenCalledOnce();
});

test("focus starts inside the dialog on open, on the first tabbable element", () => {
  render(
    <Dialog open onClose={vi.fn()} title="t">
      <button>First field</button>
    </Dialog>,
  );
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "First field" }));
});

test("Tab is trapped within the dialog", async () => {
  const user = userEvent.setup();
  render(
    <Dialog open onClose={vi.fn()} title="t">
      <button>Only field</button>
    </Dialog>,
  );
  // Only tabbable elements: "Only field" then the close button (which is
  // last in DOM order so body content gets initial focus, not the close
  // button - see this task's report). Tab from the last must loop to the
  // first, never escaping to document.body.
  screen.getByRole("button", { name: "Close" }).focus();
  await user.tab();
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "Only field" }));
});

test("closing returns focus to whatever triggered the dialog", () => {
  render(<button>Open dialog</button>);
  const trigger = screen.getByRole("button", { name: "Open dialog" });
  trigger.focus();

  const { rerender } = render(
    <Dialog open onClose={vi.fn()} title="t">
      Body
    </Dialog>,
  );
  expect(screen.getByRole("dialog")).toBeTruthy();

  rerender(
    <Dialog open={false} onClose={vi.fn()} title="t">
      Body
    </Dialog>,
  );
  expect(document.activeElement).toBe(trigger);
});

// jsdom does not evaluate real CSS animations or media queries, so (as in
// the button/cadence exemplars) the 120ms fade-scale and its
// prefers-reduced-motion opt-out are verified by reading the CSS module's
// own source, the same way token-contract.test.ts reads stylesheets.
test("the panel's open animation honors prefers-reduced-motion, using only tokens", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "dialog.module.css"), "utf8");
  expect(css).toContain("animation:");
  expect(css).toContain("var(--motion-duration-overlay)");
  expect(css).toMatch(/@media \(prefers-reduced-motion: reduce\)/);
});

test("declares a :focus-visible rule in its CSS module, using only tokens", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "dialog.module.css"), "utf8");
  expect(css).toContain(":focus-visible");
});
