import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import { CloseIcon as FromBarrel } from "..";
import { IconButton } from "../iconbutton";
import { requireClass } from "../internal/requireClass";
import { CloseIcon } from "./CloseIcon";
import rawStyles from "./dialog.module.css";
import { Dialog } from "./index";

const dialogClass = {
  panel: requireClass(rawStyles.panel, "dialog.module.css", "panel"),
  body: requireClass(rawStyles.body, "dialog.module.css", "body"),
  dialogVariant: requireClass(rawStyles.dialogVariant, "dialog.module.css", "dialogVariant"),
};

test("its close glyph is exported from the widget barrel", () => {
  expect(FromBarrel).toBe(CloseIcon);
});

// The 10px remove controls (Chip, CollectionEditor, AttachmentTile) share
// this glyph, so size must keep the stroke at 1.5 RENDERED px. The 16-grid
// viewBox scales the attribute by size/16; leaving the close buttons' 1.5
// attribute unadjusted at 10px would paint 0.94px.
test("the close glyph defaults to the close buttons' 14px box on the 16 grid", () => {
  const { container } = render(<CloseIcon />);
  const svg = container.querySelector("svg");
  const path = container.querySelector("path");
  expect(svg?.getAttribute("width")).toBe("14");
  expect(svg?.getAttribute("height")).toBe("14");
  expect(svg?.getAttribute("viewBox")).toBe("0 0 16 16");
  expect(svg?.getAttribute("aria-hidden")).toBe("true");
  expect(Number(path?.getAttribute("stroke-width")) * (14 / 16)).toBeCloseTo(1.5, 1);
});

test("size keeps the rendered stroke at 1.5px, so the 10px remove glyphs are not thinned", () => {
  const { container } = render(<CloseIcon size={10} />);
  const svg = container.querySelector("svg");
  const path = container.querySelector("path");
  expect(svg?.getAttribute("width")).toBe("10");
  expect(svg?.getAttribute("height")).toBe("10");
  expect(Number(path?.getAttribute("stroke-width")) * (10 / 16)).toBeCloseTo(1.5, 1);
});

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

// --- leaving with motion ---------------------------------------------------
// jsdom runs no animations, so these stand in a running exit animation on
// every element the layer marks closing, and finish it by hand.

function withRunningExit(): { finish: () => Promise<void>; restore: () => void } {
  let resolve: () => void = () => {};
  const finished = new Promise<void>((res) => {
    resolve = res;
  });
  const exit = {
    playState: "running",
    effect: { getComputedTiming: () => ({ endTime: 80 }) },
    finished,
  } as unknown as Animation;
  Object.defineProperty(Element.prototype, "getAnimations", {
    configurable: true,
    value(this: Element) {
      return this.getAttribute("data-motion") === "closing" ? [exit] : [];
    },
  });
  return {
    finish: async () => {
      await act(async () => resolve());
    },
    restore: () => {
      delete (Element.prototype as { getAnimations?: unknown }).getAnimations;
    },
  };
}

test("a closing dialog stays while it leaves, already inert, unmodal and handing focus back", async () => {
  const exit = withRunningExit();
  try {
    render(<button type="button">Open dialog</button>);
    const trigger = screen.getByRole("button", { name: "Open dialog" });
    trigger.focus();
    const { rerender, container } = render(
      <Dialog open onClose={vi.fn()} title="t">
        <button type="button">Inside</button>
      </Dialog>,
    );
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Inside" }));

    rerender(
      <Dialog open={false} onClose={vi.fn()} title="t">
        <button type="button">Inside</button>
      </Dialog>,
    );
    const scrim = container.firstElementChild as HTMLElement;
    expect(scrim.getAttribute("data-motion")).toBe("closing");
    expect(scrim.hasAttribute("inert")).toBe(true);
    // Nothing that asks whether a modal is open still finds one, and focus is
    // back on the trigger before the exit has finished.
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(document.querySelector('[aria-modal="true"]')).toBeNull();
    expect(document.activeElement).toBe(trigger);

    await exit.finish();
    expect(container.firstElementChild).toBeNull();
    expect(document.activeElement).toBe(trigger);
  } finally {
    exit.restore();
  }
});

test("reopening a dialog during its exit restores it whole: modal, trapped and focused inside", async () => {
  const exit = withRunningExit();
  try {
    const { rerender, container } = render(
      <Dialog open onClose={vi.fn()} title="t">
        <button type="button">Inside</button>
      </Dialog>,
    );
    rerender(
      <Dialog open={false} onClose={vi.fn()} title="t">
        <button type="button">Inside</button>
      </Dialog>,
    );
    rerender(
      <Dialog open onClose={vi.fn()} title="t">
        <button type="button">Inside</button>
      </Dialog>,
    );
    await exit.finish();
    expect(screen.getByRole("dialog").getAttribute("aria-modal")).toBe("true");
    expect((container.firstElementChild as HTMLElement).hasAttribute("inert")).toBe(false);
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Inside" }));
  } finally {
    exit.restore();
  }
});

test("a closing dialog keeps showing what it held when it closed", () => {
  const exit = withRunningExit();
  try {
    const { rerender } = render(
      <Dialog open onClose={vi.fn()} title="t">
        <p data-testid="content">kept</p>
      </Dialog>,
    );
    // A caller that clears its own state on close passes empty children.
    rerender(
      <Dialog open={false} onClose={vi.fn()} title="t">
        {null}
      </Dialog>,
    );
    expect(screen.getByTestId("content")).toBeTruthy();
  } finally {
    exit.restore();
  }
});

// jsdom does not evaluate real CSS animations or media queries, so the motion
// is pinned at the declaration level: both panel sizes float by the shared
// recipe (which owns the keyframes and the reduced-motion gate,
// styles/motion.contract.test.ts) and the scrim fades with them.
test("both panel sizes float in and out by the shared recipe, and the scrim fades with them", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "dialog.module.css"), "utf8");
  for (const variant of ["dialogVariant", "dialogVariantLarge"]) {
    const rule = new RegExp(`\\.${variant}\\s*\\{([^}]*)\\}`).exec(css)?.[1] ?? "";
    expect(rule).toMatch(/composes:\s*float from "\.\.\/\.\.\/styles\/motion\.module\.css"/);
    expect(rule).not.toMatch(/animation/);
  }
  expect(/\.scrimFloat\s*\{([^}]*)\}/.exec(css)?.[1]).toMatch(/composes:\s*fade from/);
  expect(/\.scrimEdge\s*\{([^}]*)\}/.exec(css)?.[1]).toMatch(/composes:\s*scrimSlide from/);
  expect(css).not.toMatch(/@keyframes/);
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

// --- caller classes and the no-motion variant (DirectoryPicker supplies its
// own panel geometry and scrollport through Dialog) -------------------------
test("panelClassName and bodyClassName append to the shared panel and body classes", () => {
  const { rerender } = render(
    <Dialog open onClose={vi.fn()} title="t" panelClassName="demoPanel" bodyClassName="demoBody">
      Body
    </Dialog>,
  );
  const panel = screen.getByRole("dialog");
  expect(panel.classList.contains("demoPanel")).toBe(true);
  expect(panel.classList.contains(dialogClass.dialogVariant)).toBe(true);
  const body = screen.getByText("Body");
  expect(body.classList.contains("demoBody")).toBe(true);
  expect(body.classList.contains(dialogClass.body)).toBe(true);

  rerender(
    <Dialog open onClose={vi.fn()} title="t" panelClassName="demoPanel" motion="none">
      Body
    </Dialog>,
  );
  const nonePanel = screen.getByRole("dialog");
  expect(nonePanel.classList.contains("demoPanel")).toBe(true);
  expect(nonePanel.classList.contains(dialogClass.panel)).toBe(true);
  expect(nonePanel.classList.contains(dialogClass.dialogVariant)).toBe(false);
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

// The close control is the shared IconButton at its quiet xs square, so its
// desktop box and its phone tap floor are the widget's own (iconbutton's
// square class carries both; iconbutton.test.tsx asserts the floor). This
// pins the rendered control's classes to the widget's, rather than reading
// this stylesheet for a floor it no longer restates.
test("the close button renders as the shared IconButton's quiet xs square", () => {
  const { unmount } = render(
    <Dialog open onClose={vi.fn()} title="t">
      Body
    </Dialog>,
  );
  const closeButtonClasses = [...screen.getByRole("button", { name: "Close" }).classList].sort();
  unmount();
  render(<IconButton label="Close" icon={<CloseIcon />} variant="quiet" size="xs" />);
  expect(closeButtonClasses).toEqual([...screen.getByRole("button", { name: "Close" }).classList].sort());
});
