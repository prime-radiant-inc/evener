import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
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

test("Escape marks the keydown as handled, so an ancestor sees defaultPrevented true", async () => {
  const user = userEvent.setup();
  const onClose = vi.fn();
  const ancestorKeyDown = vi.fn();
  render(
    // biome-ignore lint/a11y/noStaticElementInteractions: test-only ancestor listener standing in for a real ancestor (e.g. Settings) that must see this Escape but not act on it.
    <div onKeyDown={ancestorKeyDown}>
      <Dialog open onClose={onClose} title="t">
        Body
      </Dialog>
    </div>,
  );
  await user.keyboard("{Escape}");
  expect(onClose).toHaveBeenCalledOnce();
  expect(ancestorKeyDown).toHaveBeenCalledOnce();
  const event = ancestorKeyDown.mock.calls[0]?.[0];
  expect(event?.defaultPrevented).toBe(true);
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
      <button type="button">First field</button>
    </Dialog>,
  );
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "First field" }));
});

test("Tab is trapped within the dialog", async () => {
  const user = userEvent.setup();
  render(
    <Dialog open onClose={vi.fn()} title="t">
      <button type="button">Only field</button>
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
  render(<button type="button">Open dialog</button>);
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

// --- size (kata b4xf: ImageGallery's lightbox needs to fill almost the
// whole window, unlike every other Dialog caller, which wants the compact
// default) -------------------------------------------------------------

test('size is omittable: an unset size renders the same panel class as size="default"', () => {
  const { rerender } = render(
    <Dialog open onClose={vi.fn()} title="t">
      Body
    </Dialog>,
  );
  const omittedClass = screen.getByRole("dialog").className;

  rerender(
    <Dialog open size="default" onClose={vi.fn()} title="t">
      Body
    </Dialog>,
  );
  const explicitDefaultClass = screen.getByRole("dialog").className;

  expect(omittedClass).not.toBe("");
  expect(omittedClass).toBe(explicitDefaultClass);
});

test('size="default" and size="large" render distinct, non-empty panel classes', () => {
  const { rerender } = render(
    <Dialog open size="default" onClose={vi.fn()} title="t">
      Body
    </Dialog>,
  );
  const defaultClass = screen.getByRole("dialog").className;

  rerender(
    <Dialog open size="large" onClose={vi.fn()} title="t">
      Body
    </Dialog>,
  );
  const largeClass = screen.getByRole("dialog").className;

  expect(defaultClass).not.toBe("");
  expect(largeClass).not.toBe("");
  expect(defaultClass).not.toBe(largeClass);
});

test("the large size variant's CSS fills most of the viewport instead of the compact default's fixed cap", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "dialog.module.css"), "utf8");
  const rule = /\.dialogVariantLarge\s*{([^}]*)}/.exec(css);
  expect(rule).toBeTruthy();
  const body = rule![1]!;
  expect(body).toMatch(/max-width:\s*calc\(100vw/);
  expect(body).toMatch(/max-height:\s*calc\(100vh/);
  expect(body).not.toContain("560px");
});

test("the large size variant also honors prefers-reduced-motion", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "dialog.module.css"), "utf8");
  const reducedMotionBlock = /@media \(prefers-reduced-motion: reduce\)\s*{([\s\S]*?)}\s*}/.exec(css);
  expect(reducedMotionBlock).toBeTruthy();
  expect(reducedMotionBlock![1]).toContain("dialogVariantLarge");
});

// Beautiful UI chrome bands (design doc §6): the header and footer sit on an
// inset surface, set off from the body by the hairline they already drew.
// Sheet reuses these same classes (see OverlayPanel), so this covers both.
test("the header is a chrome band on the inset surface", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "dialog.module.css"), "utf8");
  const rule = /\.header\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
  expect(rule).toContain("background: var(--surface-inset)");
  expect(rule).toContain("border-bottom: 1px solid var(--edge)");
});

test("the footer is a chrome band on the inset surface", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "dialog.module.css"), "utf8");
  const rule = /\.footer\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
  expect(rule).toContain("background: var(--surface-inset)");
  expect(rule).toContain("border-top: 1px solid var(--edge)");
});

// FIX 5 (real-phone measurement, mirrors Rail.module.css's own coarse-
// pointer precedent for its row action buttons): .closeButton is a plain
// <button>, not the shared IconButton widget (see OverlayPanel.tsx's own
// comment on why - FocusScope's initial-focus ordering needs it last in
// markup), so it never picked up IconButton's own pointer:coarse 44px
// floor (iconbutton.module.css). Measured 24x24 on TreeDrawer's own Sheet
// (Dialog and Sheet share this one closeButton via OverlayPanel), under the
// platform's 44px tap floor every other icon-only control in the app
// already gets on a coarse pointer.
test("the close button reaches the 44px tap floor on a coarse (touch) pointer", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "dialog.module.css"), "utf8");
  const coarseBlock = /@media \(pointer: coarse\)\s*\{([\s\S]*?)\n\}/.exec(css);
  expect(coarseBlock).toBeTruthy();
  const closeButtonRule = /\.closeButton\s*\{([^}]*)\}/.exec(coarseBlock?.[1] ?? "")?.[1] ?? "";
  expect(closeButtonRule).toContain("min-width: var(--tap-min)");
  expect(closeButtonRule).toContain("min-height: var(--tap-min)");
});
