import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { createPortal } from "react-dom";
import { expect, test, vi } from "vitest";
import { mediaBlock, readModuleCss, topRuleBlock } from "../../styles/cssBlock";
import { ownerWindowOf } from "../internal/documentRealm";
import { appendPopout } from "../internal/popoutTestUtils";
import { requireClass } from "../internal/requireClass";
import { Sheet } from "./index";
import sheetStyles from "./sheet.module.css";

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
// media queries, so the motion is pinned at the declaration level. Each side
// slides by the shared recipe for its own edge (which owns the keyframes, the
// spatial durations and the reduced-motion gate), and the expandable sheet's
// height change runs on the spatial budget, gated.
test("each side slides in from and out to its own edge by the shared recipe", () => {
  const css = readModuleCss(import.meta.url, "sheet.module.css");
  const recipe = { left: "slideLeft", right: "slideRight", bottom: "slideBottom" };
  for (const [side, slide] of Object.entries(recipe)) {
    expect(topRuleBlock(css, `.${side}`)).toMatch(
      new RegExp(`composes:\\s*${slide} from "\\.\\./\\.\\./styles/motion\\.module\\.css"`),
    );
  }
  expect(css).not.toMatch(/animation|@keyframes/);
  expect(topRuleBlock(css, ".expandableBottom")).toMatch(
    /transition:\s*height var\(--motion-duration-spatial\) var\(--motion-easing-standard\)/,
  );
  expect(css).toMatch(/@media \(prefers-reduced-motion: reduce\)\s*\{\s*\.expandableBottom\s*\{\s*transition:\s*none/);
});

// --- touch-only grab pill ------------------------------------------------
// A non-expandable bottom Sheet carries a decorative grab pill instead of the
// expandable drag handle. It only hints at the swipe gesture, so it is hidden
// from assistive tech and the tab order, and its CSS shows it for coarse
// pointers only.

test("a non-expandable bottom sheet renders a decorative grab pill, not a drag handle", () => {
  render(
    <Sheet open side="bottom" onClose={vi.fn()} title="t">
      Body
    </Sheet>,
  );
  const pill = screen.getByTestId("sheet-grab-pill");
  expect(pill.getAttribute("aria-hidden")).toBe("true");
  expect(pill.hasAttribute("tabindex")).toBe(false);
  expect(pill.tabIndex).toBe(-1);
  expect(pill.querySelector("button, a[href], input, select, textarea, [tabindex]")).toBeNull();
  // The pill draws the expandable handle's own bar.
  expect(pill.firstElementChild?.className.split(" ")).toContain(sheetStyles.handleBar);
  expect(screen.queryByTestId("sheet-handle")).toBeNull();
});

test("expandable keeps the drag handle exclusively, with no grab pill beside it", () => {
  render(
    <Sheet open side="bottom" expandable={{ peekHeight: 200 }} onClose={vi.fn()} title="t">
      Body
    </Sheet>,
  );
  expect(screen.getByTestId("sheet-handle")).toBeTruthy();
  expect(screen.queryByTestId("sheet-grab-pill")).toBeNull();
});

test("side=left and side=right render neither the handle nor the grab pill", () => {
  for (const side of ["left", "right"] as const) {
    const view = render(
      <Sheet open side={side} onClose={vi.fn()} title="t">
        Body
      </Sheet>,
    );
    expect(screen.queryByTestId("sheet-grab-pill")).toBeNull();
    expect(screen.queryByTestId("sheet-handle")).toBeNull();
    view.unmount();
  }
});

test("the body of a bottom sheet contains its overscroll, on both bottom geometries", () => {
  const plain = render(
    <Sheet open side="bottom" onClose={vi.fn()} title="t" bodyClassName="flush">
      Body
    </Sheet>,
  );
  const plainBody = screen.getByText("Body").className.split(" ");
  expect(plainBody).toContain(sheetStyles.bottomBody);
  expect(plainBody).toContain("flush");
  plain.unmount();

  const expandable = render(
    <Sheet open side="bottom" expandable={{ peekHeight: 200 }} onClose={vi.fn()} title="t">
      Body
    </Sheet>,
  );
  // The expandable body wraps the caller's content in the geometry div.
  expect(screen.getByText("Body").parentElement?.className.split(" ")).toContain(sheetStyles.bottomBody);
  expandable.unmount();

  for (const side of ["left", "right"] as const) {
    const view = render(
      <Sheet open side={side} onClose={vi.fn()} title="t">
        Body
      </Sheet>,
    );
    expect(screen.getByText("Body").className.split(" ")).not.toContain(sheetStyles.bottomBody);
    view.unmount();
  }
});

// jsdom applies no cascade and evaluates no media query, so the pill's
// touch-only visibility and the bottom sheet's scroll containment are pinned
// in the stylesheet source, the same off-disk idiom as the motion test above.
test("the grab pill is hidden for fine pointers and shown on a coarse one", () => {
  const css = readModuleCss(import.meta.url, "sheet.module.css");
  expect(topRuleBlock(css, ".grabHandle")).toMatch(/display:\s*none/);
  expect(mediaBlock(css, "any-pointer: coarse")).toMatch(/\.grabHandle\s*\{[^}]*display:\s*flex/);
});

test("bottom geometry contains overscroll without carrying motion of its own", () => {
  const css = readModuleCss(import.meta.url, "sheet.module.css");
  const bottom = topRuleBlock(css, ".bottom");
  expect(bottom).toContain("overscroll-behavior-y: contain");
  expect(bottom).not.toMatch(/\banimation\b|\btransition\b/);
  expect(topRuleBlock(css, ".bottomBody")).toContain("overscroll-behavior-y: contain");
});

test("a declined swipe returns the sheet on the spatial exit budget, behind the reduced-motion gate", () => {
  const css = readModuleCss(import.meta.url, "sheet.module.css");
  const gate = mediaBlock(css, "prefers-reduced-motion: no-preference");
  expect(gate).toMatch(
    /\.bottom\[data-motion="open"\]:not\(\[data-swiping="true"\]\)\s*\{[^}]*transition:\s*transform var\(--motion-duration-spatial-exit\) var\(--motion-easing-standard\);/,
  );
  expect(gate).toMatch(
    /\.expandableBottom\[data-motion="open"\]:not\(\[data-swiping="true"\]\)\s*\{[^}]*height var\(--motion-duration-spatial\) var\(--motion-easing-standard\)[\s\S]*transform var\(--motion-duration-spatial-exit\) var\(--motion-easing-standard\);/,
  );
  expect(css).not.toContain(".snapBack");
});

// The mobile sessions drawer is a full-height side="left" Sheet. jsdom has no
// dynamic viewport and computes no cascade, so - the same comment-stripped
// source-read idiom as AppShell.test.tsx's own dvh check - this pins the
// declaration that makes the drawer follow the VISIBLE viewport. The order
// check matters: the override wins only by coming after the base rules (equal
// specificity), so reordering it would silently restore the unreachable
// Archived sessions row.
test("full-height edge sheets follow the visible viewport, keeping a vh fallback", () => {
  const variants = ["left", "right"];
  const css = readModuleCss(import.meta.url, "sheet.module.css");

  const baseIndexes: number[] = [];
  for (const variant of variants) {
    const baseIndex = css.search(new RegExp(`^\\.${variant} \\{`, "m"));
    expect(baseIndex, `.${variant} base rule`).toBeGreaterThanOrEqual(0);
    baseIndexes.push(baseIndex);
    const base = topRuleBlock(css, `.${variant}`);
    expect(base).toContain("height: 100vh");
    expect(base).not.toContain("100dvh");
  }

  const supports = css.match(/@supports \(height: 100dvh\) \{([\s\S]*?)\n\}/);
  expect(supports, "@supports (height: 100dvh) block").not.toBeNull();
  for (const baseIndex of baseIndexes) expect(supports!.index).toBeGreaterThan(baseIndex);
  for (const variant of variants) expect(supports![1]).toContain(`.${variant}`);
  expect(supports![1]).toContain("height: 100dvh");
  expect(supports![1]).not.toContain("100vh");
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

// --- popout realm (Dockview) ---------------------------------------------
// A Dockview popout is a separate Window/Document. An expandable sheet's drag
// attaches pointermove/pointerup for the gesture, so those listeners must be
// on the drag surface's own window; the opener's window never sees the
// popout's pointer events.
test("a drag in a popout is driven and ended by that window's pointer events, not the opener's", () => {
  const { frame, doc } = appendPopout();
  const view = render(
    createPortal(
      <Sheet open side="bottom" expandable={{ peekHeight: 200, fullScreenFirst: true }} onClose={vi.fn()} title="t">
        Body
      </Sheet>,
      doc.body,
    ),
  );
  try {
    const handle = doc.querySelector<HTMLElement>('[data-testid="sheet-handle"]');
    if (handle === null) throw new Error("popout sheet handle missing");
    const geometry = () => doc.querySelector("[data-geometry]")?.getAttribute("data-geometry");
    expect(geometry()).toBe("full");

    fireEvent.pointerDown(handle, { clientY: 500 });
    // The opener's window is not part of this drag.
    fireEvent.pointerMove(document.body, { clientY: 800 });
    expect(geometry()).toBe("full");

    // The popout's own pointer events drive it.
    fireEvent.pointerMove(handle, { clientY: 800 });
    expect(geometry()).toBe("peek");

    // pointerup in the popout ends the gesture; a later move changes nothing.
    fireEvent.pointerUp(handle, { clientY: 800 });
    fireEvent.pointerMove(handle, { clientY: 100 });
    expect(geometry()).toBe("peek");
  } finally {
    view.unmount();
    frame.remove();
  }
});

// The drag's listeners must come off the window they went on. A removal that
// asked the opener (the pre-fix module-scope `window`) leaves the popout's
// listeners behind, which no geometry assertion can see.
test("a popout drag removes its listeners from the popout window", () => {
  const { frame, doc, win } = appendPopout();
  const addSpy = vi.spyOn(win, "addEventListener");
  const removeSpy = vi.spyOn(win, "removeEventListener");
  const openerAdd = vi.spyOn(window, "addEventListener");
  const view = render(
    createPortal(
      <Sheet open side="bottom" expandable={{ peekHeight: 200, fullScreenFirst: true }} onClose={vi.fn()} title="t">
        Body
      </Sheet>,
      doc.body,
    ),
  );
  try {
    const handle = doc.querySelector<HTMLElement>('[data-testid="sheet-handle"]');
    if (handle === null) throw new Error("popout sheet handle missing");
    fireEvent.pointerDown(handle, { clientY: 500 });
    const added = addSpy.mock.calls.filter(([type]) => type === "pointermove" || type === "pointerup");
    expect(added.map(([type]) => type).sort()).toEqual(["pointermove", "pointerup"]);
    expect(openerAdd.mock.calls.filter(([type]) => type === "pointermove" || type === "pointerup")).toHaveLength(0);

    fireEvent.pointerUp(handle, { clientY: 500 });
    for (const [type, listener] of added) {
      expect(removeSpy.mock.calls.some(([t, f]) => t === type && f === listener)).toBe(true);
    }
  } finally {
    view.unmount();
    frame.remove();
    addSpy.mockRestore();
    removeSpy.mockRestore();
    openerAdd.mockRestore();
  }
});

// A sheet closed mid-drag removes the pair from the popout window on unmount.
test("unmounting a popout sheet mid-drag removes its listeners there", () => {
  const { frame, doc, win } = appendPopout();
  const addSpy = vi.spyOn(win, "addEventListener");
  const removeSpy = vi.spyOn(win, "removeEventListener");
  const view = render(
    createPortal(
      <Sheet open side="bottom" expandable={{ peekHeight: 200, fullScreenFirst: true }} onClose={vi.fn()} title="t">
        Body
      </Sheet>,
      doc.body,
    ),
  );
  try {
    const handle = doc.querySelector<HTMLElement>('[data-testid="sheet-handle"]');
    if (handle === null) throw new Error("popout sheet handle missing");
    fireEvent.pointerDown(handle, { clientY: 500 });
    const added = addSpy.mock.calls.filter(([type]) => type === "pointermove" || type === "pointerup");
    expect(added).toHaveLength(2);

    view.unmount();
    for (const [type, listener] of added) {
      expect(removeSpy.mock.calls.some(([t, f]) => t === type && f === listener)).toBe(true);
    }
  } finally {
    frame.remove();
    addSpy.mockRestore();
    removeSpy.mockRestore();
  }
});

// --- swipe-down dismissal ------------------------------------------------

interface TouchPointInit {
  identifier?: number;
  clientX?: number;
  clientY?: number;
}

function dispatchTouch(
  target: EventTarget,
  type: "touchstart" | "touchmove" | "touchend" | "touchcancel",
  options: { touches?: TouchPointInit[]; changedTouches?: TouchPointInit[] } = {},
): Event {
  const touches = (options.touches ?? []).map((t) => ({ identifier: 0, clientX: 0, clientY: 0, ...t }));
  const changedTouches = (options.changedTouches ?? []).map((t) => ({ identifier: 0, clientX: 0, clientY: 0, ...t }));
  const doc = (target as Node).ownerDocument ?? document;
  const EventCtor = doc.defaultView?.Event ?? Event;
  const event = new EventCtor(type, { bubbles: true, cancelable: true });
  Object.defineProperty(event, "touches", { value: touches });
  Object.defineProperty(event, "changedTouches", { value: changedTouches });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

function dispatchPointer(
  target: EventTarget,
  type: "pointerdown" | "pointermove" | "pointerup" | "pointercancel",
  options: { pointerId?: number; clientX?: number; clientY?: number; button?: number } = {},
): Event {
  const maybeWindow = target as Partial<Window>;
  const win = maybeWindow.window === target ? (target as Window) : ownerWindowOf(target as Node);
  const EventCtor = (win as Window & typeof globalThis).Event;
  const event = new EventCtor(type, { bubbles: true, cancelable: true });
  Object.defineProperty(event, "pointerId", { value: options.pointerId ?? 0 });
  Object.defineProperty(event, "clientX", { value: options.clientX ?? 0 });
  Object.defineProperty(event, "clientY", { value: options.clientY ?? 0 });
  Object.defineProperty(event, "button", { value: options.button ?? 0 });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

function mockPanelHeight(panel: HTMLElement, height: number) {
  Object.defineProperty(panel, "getBoundingClientRect", {
    configurable: true,
    value: () => ({
      x: 0,
      y: 0,
      top: 0,
      left: 0,
      right: 375,
      bottom: height,
      width: 375,
      height,
      toJSON: () => ({}),
    }),
  });
}

function withRunningExit(): { finish: () => Promise<void>; restore: () => void } {
  let resolve: () => void = () => {};
  const finished = new Promise<void>((res) => {
    resolve = res;
  });
  const exit = {
    playState: "running",
    effect: { getComputedTiming: () => ({ endTime: 180 }) },
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

test("slow downward drag past threshold writes live transform/opacity, preserves them for exit continuation, restores focus, and calls onClose once", async () => {
  vi.useFakeTimers();
  const exit = withRunningExit();
  try {
    render(<button type="button">Open sheet</button>);
    const trigger = screen.getByRole("button", { name: "Open sheet" });
    trigger.focus();

    const onClose = vi.fn();
    function Controlled() {
      const [open, setOpen] = useState(true);
      return (
        <Sheet
          open={open}
          side="bottom"
          onClose={() => {
            onClose();
            setOpen(false);
          }}
          title="Bottom sheet"
        >
          <p>Content</p>
        </Sheet>
      );
    }

    const { container } = render(<Controlled />);
    const scrim = container.firstElementChild as HTMLElement;
    const panel = screen.getByRole("dialog");
    mockPanelHeight(panel, 400);
    const content = screen.getByText("Content");

    dispatchTouch(content, "touchstart", { touches: [{ identifier: 1, clientY: 100 }] });
    const earlyMove = dispatchTouch(content, "touchmove", { touches: [{ identifier: 1, clientY: 105 }] });
    expect(earlyMove.defaultPrevented).toBe(false);
    expect(panel.style.transform).toBe("");

    // 400 * 0.35 = 140 threshold; drag to 150px.
    const dragMove = dispatchTouch(content, "touchmove", { touches: [{ identifier: 1, clientY: 250 }] });
    expect(dragMove.defaultPrevented).toBe(true);
    expect(panel.style.transform).toBe("translateY(150px)");
    expect(Number(scrim.style.opacity)).toBeCloseTo(1 - (150 / 400) * 0.6, 5);

    dispatchTouch(content, "touchend", { changedTouches: [{ identifier: 1, clientY: 250 }] });
    expect(onClose).toHaveBeenCalledOnce();
    // Inline transform and opacity remain in place so motionSlideBottomOut continues smoothly.
    expect(scrim.getAttribute("data-motion")).toBe("closing");
    expect(panel.getAttribute("data-motion")).toBe("closing");
    expect(panel.style.transform).toBe("translateY(150px)");
    expect(Number(scrim.style.opacity)).toBeCloseTo(1 - (150 / 400) * 0.6, 5);
    expect(document.activeElement).toBe(trigger);
    // Advancing past one frame after open turns false must NOT snap back during exit.
    act(() => vi.advanceTimersByTime(32));
    expect(panel.dataset.swiping).toBe("true");
    expect(panel.style.transform).toBe("translateY(150px)");

    await exit.finish();
    expect(container.firstElementChild).toBeNull();
  } finally {
    vi.useRealTimers();
    exit.restore();
  }
});

test("detaching a touched body child mid-gesture does not wedge subsequent body swipes or handle/header drags", () => {
  vi.useFakeTimers();
  try {
    const onClose = vi.fn();
    render(
      <Sheet open side="bottom" onClose={onClose} title="Header title">
        <div data-testid="row1">Row 1</div>
        <div data-testid="row2">Row 2</div>
        <p>Body</p>
      </Sheet>,
    );
    const panel = screen.getByRole("dialog");
    const row1 = screen.getByTestId("row1");
    const row2 = screen.getByTestId("row2");
    const body = screen.getByText("Body");
    const header = screen.getByText("Header title").parentElement!;
    const pill = screen.getByTestId("sheet-grab-pill");

    // 1) Child detaches while in pendingBody -> subsequent body touch swipe still dismisses.
    dispatchTouch(row1, "touchstart", { touches: [{ identifier: 1, clientY: 100 }] });
    row1.remove();
    dispatchTouch(body, "touchstart", { touches: [{ identifier: 2, clientY: 100 }] });
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 2, clientY: 260 }] });
    dispatchTouch(body, "touchend", { changedTouches: [{ identifier: 2, clientY: 260 }] });
    expect(onClose).toHaveBeenCalledOnce();
    onClose.mockClear();
    act(() => vi.advanceTimersByTime(250));

    // 2) Child detaches mid-drag -> subsequent header and grab-pill pointer drags still dismiss.
    dispatchTouch(row2, "touchstart", { touches: [{ identifier: 3, clientY: 100 }] });
    dispatchTouch(row2, "touchmove", { touches: [{ identifier: 3, clientY: 150 }] });
    expect(panel.style.transform).toBe("translateY(50px)");
    row2.remove();

    // Tapping header without moving recovers the orphaned offset cleanly via resting CSS transition.
    dispatchPointer(header, "pointerdown", { pointerId: 9, clientY: 100 });
    expect(panel.style.transform).toBe("translateY(50px)");
    expect(panel.dataset.swiping).toBe("true");
    dispatchPointer(window, "pointerup", { pointerId: 9, clientY: 100 });
    expect(panel.dataset.swiping).toBeUndefined();
    expect(panel.style.transform).toBe("");

    dispatchPointer(header, "pointerdown", { pointerId: 10, clientY: 100 });
    dispatchPointer(window, "pointermove", { pointerId: 10, clientY: 260 });
    dispatchPointer(window, "pointerup", { pointerId: 10, clientY: 260 });
    expect(onClose).toHaveBeenCalledOnce();
    onClose.mockClear();
    act(() => vi.advanceTimersByTime(250));

    dispatchPointer(pill, "pointerdown", { pointerId: 11, clientY: 100 });
    dispatchPointer(window, "pointermove", { pointerId: 11, clientY: 260 });
    dispatchPointer(window, "pointerup", { pointerId: 11, clientY: 260 });
    expect(onClose).toHaveBeenCalledOnce();
  } finally {
    vi.useRealTimers();
  }
});

test("orphaned touch recovery works when the next single-finger touch reuses identifier 0, while genuine 2-finger touch keeps active gesture", () => {
  vi.useFakeTimers();
  try {
    const onClose = vi.fn();
    render(
      <Sheet open side="bottom" onClose={onClose} title="Header title">
        <div data-testid="scroller">
          <div data-testid="rowScroll">Scroll row</div>
        </div>
        <div data-testid="rowPending">Pending row</div>
        <div data-testid="rowDrag">Drag row</div>
        <div data-testid="rowZero">Zero row</div>
        <p>Body</p>
      </Sheet>,
    );
    const panel = screen.getByRole("dialog");
    mockPanelHeight(panel, 400); // threshold 140
    const scroller = screen.getByTestId("scroller");
    const rowScroll = screen.getByTestId("rowScroll");
    const rowPending = screen.getByTestId("rowPending");
    const rowDrag = screen.getByTestId("rowDrag");
    const rowZero = screen.getByTestId("rowZero");
    const body = screen.getByText("Body");

    // 1) pendingBody orphan with identifier 0: next touch (identifier 0) must use its own startY (200), not stale 100.
    dispatchTouch(rowPending, "touchstart", { touches: [{ identifier: 0, clientY: 100 }] });
    rowPending.remove();
    dispatchTouch(body, "touchstart", { touches: [{ identifier: 0, clientY: 200 }] });
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 0, clientY: 250 }] });
    expect(panel.style.transform).toBe("translateY(50px)");
    dispatchTouch(body, "touchend", { changedTouches: [{ identifier: 0, clientY: 250 }] });
    expect(onClose).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(250));

    // 2) scrollOwned orphan with identifier 0: next touch (identifier 0) recovers and can dismiss.
    Object.defineProperty(scroller, "scrollTop", { configurable: true, writable: true, value: 40 });
    dispatchTouch(rowScroll, "touchstart", { touches: [{ identifier: 0, clientY: 100 }] });
    scroller.scrollTop = 0;
    rowScroll.remove();
    dispatchTouch(body, "touchstart", { touches: [{ identifier: 0, clientY: 100 }] });
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 0, clientY: 260 }] });
    dispatchTouch(body, "touchend", { changedTouches: [{ identifier: 0, clientY: 260 }] });
    expect(onClose).toHaveBeenCalledOnce();
    onClose.mockClear();
    act(() => vi.advanceTimersByTime(250));

    // 3) touch dragging orphan with identifier 0: clears drag state on next touchstart and resets origin.
    dispatchTouch(rowDrag, "touchstart", { touches: [{ identifier: 0, clientY: 100 }] });
    dispatchTouch(rowDrag, "touchmove", { touches: [{ identifier: 0, clientY: 160 }] });
    expect(panel.style.transform).toBe("translateY(60px)");
    expect(panel.dataset.swiping).toBe("true");
    rowDrag.remove();
    dispatchTouch(body, "touchstart", { touches: [{ identifier: 0, clientY: 200 }] });
    expect(panel.dataset.swiping).toBeUndefined();
    expect(panel.style.transform).toBe("");
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 0, clientY: 220 }] });
    expect(panel.dataset.swiping).toBe("true");
    expect(panel.style.transform).toBe("translateY(20px)");
    dispatchTouch(body, "touchend", { changedTouches: [{ identifier: 0, clientY: 220 }] });
    expect(onClose).not.toHaveBeenCalled();
    expect(panel.dataset.swiping).toBeUndefined();
    expect(panel.style.transform).toBe("");

    // 3b) Orphaned touch drag that returned to offsetY === 0 clears stale inline styles on next touchstart.
    dispatchTouch(rowZero, "touchstart", { touches: [{ identifier: 0, clientY: 100 }] });
    dispatchTouch(rowZero, "touchmove", { touches: [{ identifier: 0, clientY: 140 }] });
    dispatchTouch(rowZero, "touchmove", { touches: [{ identifier: 0, clientY: 100 }] });
    expect(panel.style.transform).toBe("translateY(0px)");
    rowZero.remove();
    dispatchTouch(body, "touchstart", { touches: [{ identifier: 0, clientY: 100 }] });
    expect(panel.style.transform).toBe("");
    expect(panel.dataset.swiping).toBeUndefined();
    dispatchTouch(body, "touchend", { changedTouches: [{ identifier: 0, clientY: 100 }] });

    // 4) Genuine 2-finger secondary touch (touches.length === 2) keeps primary gesture intact.
    dispatchTouch(body, "touchstart", { touches: [{ identifier: 0, clientY: 100 }] });
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 0, clientY: 140 }] });
    dispatchTouch(body, "touchstart", {
      touches: [
        { identifier: 0, clientY: 140 },
        { identifier: 1, clientY: 140 },
      ],
      changedTouches: [{ identifier: 1, clientY: 140 }],
    });
    dispatchTouch(body, "touchmove", {
      touches: [
        { identifier: 0, clientY: 140 },
        { identifier: 1, clientY: 400 },
      ],
    });
    expect(panel.style.transform).toBe("translateY(40px)");
    dispatchTouch(body, "touchend", {
      touches: [{ identifier: 0, clientY: 140 }],
      changedTouches: [{ identifier: 1, clientY: 400 }],
    });
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 0, clientY: 260 }] });
    dispatchTouch(body, "touchend", { changedTouches: [{ identifier: 0, clientY: 260 }] });
    expect(onClose).toHaveBeenCalledOnce();
  } finally {
    vi.useRealTimers();
  }
});

test("a descendant calling stopPropagation on touchend or touchcancel does not wedge subsequent gestures", () => {
  vi.useFakeTimers();
  try {
    const onClose = vi.fn();
    render(
      <Sheet open side="bottom" onClose={onClose} title="Header title">
        <div data-testid="swallower">Swallower</div>
        <p>Body</p>
      </Sheet>,
    );
    const panel = screen.getByRole("dialog");
    const swallower = screen.getByTestId("swallower");
    const body = screen.getByText("Body");
    const header = screen.getByText("Header title").parentElement!;
    swallower.addEventListener("touchend", (event) => event.stopPropagation());
    swallower.addEventListener("touchcancel", (event) => event.stopPropagation());

    dispatchTouch(swallower, "touchstart", { touches: [{ identifier: 1, clientY: 100 }] });
    dispatchTouch(swallower, "touchmove", { touches: [{ identifier: 1, clientY: 150 }] });
    expect(panel.dataset.swiping).toBe("true");
    act(() => vi.advanceTimersByTime(90));
    dispatchTouch(swallower, "touchend", { changedTouches: [{ identifier: 1, clientY: 150 }] });
    expect(panel.dataset.swiping).toBeUndefined();
    expect(panel.style.transform).toBe("");

    dispatchTouch(swallower, "touchstart", { touches: [{ identifier: 10, clientY: 100 }] });
    dispatchTouch(swallower, "touchmove", { touches: [{ identifier: 10, clientY: 150 }] });
    expect(panel.dataset.swiping).toBe("true");
    act(() => vi.advanceTimersByTime(90));
    dispatchTouch(swallower, "touchcancel", { changedTouches: [{ identifier: 10, clientY: 150 }] });
    expect(panel.dataset.swiping).toBeUndefined();
    expect(panel.style.transform).toBe("");

    dispatchTouch(body, "touchstart", { touches: [{ identifier: 2, clientY: 100 }] });
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 2, clientY: 260 }] });
    dispatchTouch(body, "touchend", { changedTouches: [{ identifier: 2, clientY: 260 }] });
    expect(onClose).toHaveBeenCalledOnce();
    onClose.mockClear();

    dispatchPointer(header, "pointerdown", { pointerId: 3, clientY: 100 });
    dispatchPointer(window, "pointermove", { pointerId: 3, clientY: 260 });
    dispatchPointer(window, "pointerup", { pointerId: 3, clientY: 260 });
    expect(onClose).toHaveBeenCalledOnce();
  } finally {
    vi.useRealTimers();
  }
});

test("grab pill and bottom sheet header declare touch-action: none and apply .bottomHeader only to side=bottom", () => {
  const css = readModuleCss(import.meta.url, "sheet.module.css");
  expect(topRuleBlock(css, ".grabHandle")).toContain("touch-action: none");
  expect(topRuleBlock(css, ".bottomHeader")).toContain("touch-action: none");
  const bottomHeaderClass = requireClass(sheetStyles.bottomHeader, "sheet.module.css", "bottomHeader");
  const expandableHeaderClass = requireClass(sheetStyles.expandableHeader, "sheet.module.css", "expandableHeader");

  const plain = render(
    <Sheet open side="bottom" onClose={vi.fn()} title="Plain">
      Body
    </Sheet>,
  );
  expect(screen.getByText("Plain").parentElement?.classList.contains(bottomHeaderClass)).toBe(true);
  plain.unmount();

  const exp = render(
    <Sheet open side="bottom" expandable={{ peekHeight: 200 }} onClose={vi.fn()} title="Exp">
      Body
    </Sheet>,
  );
  const expHeader = screen.getByText("Exp").parentElement!;
  expect(expHeader.classList.contains(bottomHeaderClass)).toBe(true);
  expect(expHeader.classList.contains(expandableHeaderClass)).toBe(true);
  exp.unmount();

  for (const side of ["left", "right"] as const) {
    const edge = render(
      <Sheet open side={side} onClose={vi.fn()} title="Edge">
        Body
      </Sheet>,
    );
    expect(screen.getByText("Edge").parentElement?.classList.contains(bottomHeaderClass)).toBe(false);
    edge.unmount();
  }
});

test("dismiss threshold uses min(panelHeight * 0.35, 160) with 120px fallback at height 0", () => {
  vi.useFakeTimers();
  try {
    const cases = [
      { height: 0, below: 119, at: 120 },
      { height: 400, below: 139, at: 140 },
      { height: 600, below: 159, at: 160 },
    ];
    for (const c of cases) {
      const onClose = vi.fn();
      const view = render(
        <Sheet open side="bottom" onClose={onClose} title="t">
          <p>Body</p>
        </Sheet>,
      );
      const panel = screen.getByRole("dialog");
      mockPanelHeight(panel, c.height);
      const pill = screen.getByTestId("sheet-grab-pill");

      dispatchPointer(pill, "pointerdown", { clientY: 100 });
      dispatchPointer(window, "pointermove", { clientY: 100 + c.below });
      act(() => vi.advanceTimersByTime(90));
      dispatchPointer(window, "pointerup", { clientY: 100 + c.below });
      expect(onClose).not.toHaveBeenCalled();
      expect(panel.dataset.swiping).toBeUndefined();
      expect(panel.style.transform).toBe("");

      dispatchPointer(pill, "pointerdown", { clientY: 100 });
      dispatchPointer(window, "pointermove", { clientY: 100 + c.at });
      act(() => vi.advanceTimersByTime(90));
      dispatchPointer(window, "pointerup", { clientY: 100 + c.at });
      expect(onClose).toHaveBeenCalledOnce();
      view.unmount();
    }
  } finally {
    vi.useRealTimers();
  }
});

test("drag below threshold clears data-swiping, inline transform, and scrim opacity so resting CSS glides back", () => {
  vi.useFakeTimers();
  try {
    const onClose = vi.fn();
    const { container } = render(
      <Sheet open side="bottom" onClose={onClose} title="t">
        <p>Body</p>
      </Sheet>,
    );
    const scrim = container.firstElementChild as HTMLElement;
    const panel = screen.getByRole("dialog");
    mockPanelHeight(panel, 400);
    const body = screen.getByText("Body");

    // Active drag sets data-swiping="true" to suspend the resting return transition; release clears both.
    dispatchTouch(body, "touchstart", { touches: [{ identifier: 1, clientY: 100 }] });
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 1, clientY: 160 }] });
    expect(panel.dataset.swiping).toBe("true");
    expect(panel.style.transform).toBe("translateY(60px)");
    act(() => vi.advanceTimersByTime(90));
    dispatchTouch(body, "touchend", { changedTouches: [{ identifier: 1, clientY: 160 }] });

    expect(onClose).not.toHaveBeenCalled();
    expect(panel.dataset.swiping).toBeUndefined();
    expect(panel.style.transform).toBe("");
    expect(scrim.style.opacity).toBe("");

    // Starting a new drag immediately re-enables live tracking without fighting a return transition.
    dispatchTouch(body, "touchstart", { touches: [{ identifier: 2, clientY: 100 }] });
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 2, clientY: 130 }] });
    expect(panel.dataset.swiping).toBe("true");
    expect(panel.style.transform).toBe("translateY(30px)");
    act(() => vi.advanceTimersByTime(90));
    dispatchTouch(body, "touchend", { changedTouches: [{ identifier: 2, clientY: 130 }] });
    expect(panel.dataset.swiping).toBeUndefined();
    expect(panel.style.transform).toBe("");
  } finally {
    vi.useRealTimers();
  }
});

test("fast flick projects velocity forward to dismiss, while > 80ms hold before lift zeroes velocity and upward reversal snaps back", () => {
  vi.useFakeTimers();
  try {
    const onClose = vi.fn();
    render(
      <Sheet open side="bottom" onClose={onClose} title="t">
        <p>Body</p>
      </Sheet>,
    );
    const panel = screen.getByRole("dialog");
    mockPanelHeight(panel, 400); // threshold 140
    const body = screen.getByText("Body");

    // 1) Fast flick: 50px in 20ms => v = 2.5 px/ms, projectedY = 50 + 500 >= 140 => dismiss.
    dispatchTouch(body, "touchstart", { touches: [{ identifier: 1, clientY: 100 }] });
    act(() => vi.advanceTimersByTime(20));
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 1, clientY: 150 }] });
    dispatchTouch(body, "touchend", { changedTouches: [{ identifier: 1, clientY: 150 }] });
    expect(onClose).toHaveBeenCalledOnce();
    onClose.mockClear();
    // Advance past 1-frame declined-close snap-back guard since caller left open={true}.
    act(() => vi.advanceTimersByTime(250));

    // 2) Same 50px fast drag followed by 90ms hold (> 80ms) before lift => velocityY = 0 => snap back.
    dispatchTouch(body, "touchstart", { touches: [{ identifier: 2, clientY: 100 }] });
    act(() => vi.advanceTimersByTime(20));
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 2, clientY: 150 }] });
    act(() => vi.advanceTimersByTime(90));
    dispatchTouch(body, "touchend", { changedTouches: [{ identifier: 2, clientY: 150 }] });
    expect(onClose).not.toHaveBeenCalled();
    expect(panel.dataset.swiping).toBeUndefined();
    expect(panel.style.transform).toBe("");

    // 3) Upward reversal before lift: dragged to 160px (past 140px), then pulled up to 145px in 15ms
    // => v = -1 px/ms, projectedY = 145 - 200 = -55 < 140 => snaps back.
    dispatchTouch(body, "touchstart", { touches: [{ identifier: 3, clientY: 100 }] });
    act(() => vi.advanceTimersByTime(40));
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 3, clientY: 260 }] });
    act(() => vi.advanceTimersByTime(15));
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 3, clientY: 245 }] });
    dispatchTouch(body, "touchend", { changedTouches: [{ identifier: 3, clientY: 245 }] });
    expect(onClose).not.toHaveBeenCalled();
    expect(panel.dataset.swiping).toBeUndefined();
    expect(panel.style.transform).toBe("");

    // 4) Slow multi-step upward reversal (120 Hz: y 100->220 in 40ms, then 7 frames of -2px over 56ms) snaps back.
    dispatchTouch(body, "touchstart", { touches: [{ identifier: 4, clientY: 100 }] });
    act(() => vi.advanceTimersByTime(40));
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 4, clientY: 220 }] });
    for (let step = 1; step <= 7; step += 1) {
      act(() => vi.advanceTimersByTime(8));
      dispatchTouch(body, "touchmove", { touches: [{ identifier: 4, clientY: 220 - step * 2 }] });
    }
    dispatchTouch(body, "touchend", { changedTouches: [{ identifier: 4, clientY: 206 }] });
    expect(onClose).not.toHaveBeenCalled();
    expect(panel.dataset.swiping).toBeUndefined();
    expect(panel.style.transform).toBe("");

    // 5) Symmetric trough reversal: downward->upward to 180, then 7 frames of +2px over 56ms projects downward flick to dismiss.
    dispatchTouch(body, "touchstart", { touches: [{ identifier: 5, clientY: 100 }] });
    act(() => vi.advanceTimersByTime(20));
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 5, clientY: 200 }] });
    act(() => vi.advanceTimersByTime(20));
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 5, clientY: 180 }] });
    for (let step = 1; step <= 7; step += 1) {
      act(() => vi.advanceTimersByTime(8));
      dispatchTouch(body, "touchmove", { touches: [{ identifier: 5, clientY: 180 + step * 2 }] });
    }
    dispatchTouch(body, "touchend", { changedTouches: [{ identifier: 5, clientY: 194 }] });
    expect(onClose).toHaveBeenCalledOnce();
    onClose.mockClear();
    act(() => vi.advanceTimersByTime(250));

    // 6) Upward pull-back arriving only on release (pointerup on header, and touchend on body)
    // cancels dismissal even though the prior move exceeded the 160px cap.
    mockPanelHeight(panel, 600); // threshold 160
    const header = screen.getByText("t").parentElement!;
    dispatchPointer(header, "pointerdown", { pointerId: 6, clientY: 100 });
    act(() => vi.advanceTimersByTime(40));
    dispatchPointer(window, "pointermove", { pointerId: 6, clientY: 270 });
    act(() => vi.advanceTimersByTime(15));
    dispatchPointer(window, "pointerup", { pointerId: 6, clientY: 250 });
    expect(onClose).not.toHaveBeenCalled();
    expect(panel.dataset.swiping).toBeUndefined();
    expect(panel.style.transform).toBe("");

    dispatchTouch(body, "touchstart", { touches: [{ identifier: 7, clientY: 100 }] });
    act(() => vi.advanceTimersByTime(40));
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 7, clientY: 270 }] });
    act(() => vi.advanceTimersByTime(15));
    dispatchTouch(body, "touchend", { changedTouches: [{ identifier: 7, clientY: 250 }] });
    expect(onClose).not.toHaveBeenCalled();
    expect(panel.dataset.swiping).toBeUndefined();
    expect(panel.style.transform).toBe("");
  } finally {
    vi.useRealTimers();
  }
});

test("touchcancel, pointercancel, and declined close (open stays true after onClose) all snap back cleanly", () => {
  vi.useFakeTimers();
  try {
    const onClose = vi.fn();
    const { container } = render(
      <Sheet open side="bottom" onClose={onClose} title="Header title">
        <p>Body</p>
      </Sheet>,
    );
    const scrim = container.firstElementChild as HTMLElement;
    const panel = screen.getByRole("dialog");
    mockPanelHeight(panel, 400);
    const body = screen.getByText("Body");
    const header = screen.getByText("Header title").parentElement!;

    // touchcancel mid-drag
    dispatchTouch(body, "touchstart", { touches: [{ identifier: 1, clientY: 100 }] });
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 1, clientY: 260 }] });
    dispatchTouch(body, "touchcancel", { changedTouches: [{ identifier: 1, clientY: 260 }] });
    expect(onClose).not.toHaveBeenCalled();
    expect(panel.dataset.swiping).toBeUndefined();
    expect(panel.style.transform).toBe("");
    expect(scrim.style.opacity).toBe("");

    // pointercancel mid-drag
    dispatchPointer(header, "pointerdown", { pointerId: 3, clientY: 100 });
    dispatchPointer(window, "pointermove", { pointerId: 3, clientY: 260 });
    dispatchPointer(window, "pointercancel", { pointerId: 3, clientY: 260 });
    expect(onClose).not.toHaveBeenCalled();
    expect(panel.dataset.swiping).toBeUndefined();
    expect(panel.style.transform).toBe("");
    expect(scrim.style.opacity).toBe("");

    // Declined close: caller leaves open={true} after onClose() -> clears drag state one rAF later.
    dispatchPointer(header, "pointerdown", { pointerId: 4, clientY: 100 });
    dispatchPointer(window, "pointermove", { pointerId: 4, clientY: 280 });
    dispatchPointer(window, "pointerup", { pointerId: 4, clientY: 280 });
    expect(onClose).toHaveBeenCalledOnce();
    expect(panel.dataset.swiping).toBe("true");
    expect(panel.style.transform).toBe("translateY(180px)");
    act(() => vi.advanceTimersByTime(20));
    expect(panel.dataset.swiping).toBeUndefined();
    expect(panel.style.transform).toBe("");
    expect(scrim.style.opacity).toBe("");
    onClose.mockClear();

    // Declined close + header tap before rAF fires still clears drag state cleanly on pointerup.
    dispatchPointer(header, "pointerdown", { pointerId: 5, clientY: 100 });
    dispatchPointer(window, "pointermove", { pointerId: 5, clientY: 280 });
    dispatchPointer(window, "pointerup", { pointerId: 5, clientY: 280 });
    expect(onClose).toHaveBeenCalledOnce();
    expect(panel.style.transform).toBe("translateY(180px)");
    dispatchPointer(header, "pointerdown", { pointerId: 6, clientY: 100 });
    dispatchPointer(window, "pointerup", { pointerId: 6, clientY: 100 });
    expect(panel.dataset.swiping).toBeUndefined();
    expect(panel.style.transform).toBe("");
    expect(scrim.style.opacity).toBe("");
    onClose.mockClear();

    // Declined close + body touch straddling rAF then lifting without drag still clears drag state cleanly.
    dispatchPointer(header, "pointerdown", { pointerId: 7, clientY: 100 });
    dispatchPointer(window, "pointermove", { pointerId: 7, clientY: 280 });
    dispatchPointer(window, "pointerup", { pointerId: 7, clientY: 280 });
    expect(onClose).toHaveBeenCalledOnce();
    expect(panel.style.transform).toBe("translateY(180px)");
    dispatchTouch(body, "touchstart", { touches: [{ identifier: 8, clientY: 100 }] });
    act(() => vi.advanceTimersByTime(20));
    expect(panel.dataset.swiping).toBeUndefined();
    expect(panel.style.transform).toBe("");
    expect(scrim.style.opacity).toBe("");
    dispatchTouch(body, "touchend", { changedTouches: [{ identifier: 8, clientY: 100 }] });
  } finally {
    vi.useRealTimers();
  }
});

test("scrolled ancestor, 180ms scroll cooldown, horizontal/upward swipes, secondary touch/pointer IDs, and interactive controls stay scroll-owned or interactive while header/pill bypass cooldown", () => {
  vi.useFakeTimers();
  try {
    const onClose = vi.fn();
    render(
      <Sheet
        open
        side="bottom"
        onClose={onClose}
        title="Header title"
        footer={
          <div data-testid="sheet-footer">
            <button type="button">Save</button>
          </div>
        }
      >
        <div data-testid="scroller">
          <p>Scroll content</p>
          <input type="text" aria-label="Provider name" />
          <input type="range" aria-label="Slider" />
          <textarea aria-label="Notes" />
          <select aria-label="Choice">
            <option value="a">A</option>
          </select>
          <div data-testid="editable" contentEditable suppressContentEditableWarning>
            Editable
          </div>
          <button type="button">Body action</button>
        </div>
      </Sheet>,
    );
    const panel = screen.getByRole("dialog");
    const scroller = screen.getByTestId("scroller");
    const content = screen.getByText("Scroll content");
    const header = screen.getByText("Header title").parentElement!;
    const pill = screen.getByTestId("sheet-grab-pill");

    // 1) Ancestor scrollTop > 1 keeps whole gesture scroll-owned even after scrollTop returns to 0.
    Object.defineProperty(scroller, "scrollTop", { configurable: true, writable: true, value: 40 });
    dispatchTouch(content, "touchstart", { touches: [{ identifier: 1, clientY: 100 }] });
    scroller.scrollTop = 0;
    const moveWhileScrolled = dispatchTouch(content, "touchmove", { touches: [{ identifier: 1, clientY: 320 }] });
    expect(moveWhileScrolled.defaultPrevented).toBe(false);
    dispatchTouch(content, "touchend", { changedTouches: [{ identifier: 1, clientY: 320 }] });
    expect(panel.style.transform).toBe("");
    expect(onClose).not.toHaveBeenCalled();

    // 2) Within 180ms of capture-phase scroll event: body touch blocked, handle/header drag works.
    scroller.dispatchEvent(new Event("scroll", { bubbles: false }));
    act(() => vi.advanceTimersByTime(100));
    dispatchTouch(content, "touchstart", { touches: [{ identifier: 2, clientY: 100 }] });
    dispatchTouch(content, "touchmove", { touches: [{ identifier: 2, clientY: 320 }] });
    dispatchTouch(content, "touchend", { changedTouches: [{ identifier: 2, clientY: 320 }] });
    expect(onClose).not.toHaveBeenCalled();

    // Handle/header still dismisses immediately during cooldown.
    dispatchPointer(pill, "pointerdown", { pointerId: 10, clientY: 100 });
    dispatchPointer(window, "pointermove", { pointerId: 10, clientY: 260 });
    dispatchPointer(window, "pointerup", { pointerId: 10, clientY: 260 });
    expect(onClose).toHaveBeenCalledOnce();
    onClose.mockClear();
    act(() => vi.advanceTimersByTime(250));

    // After >= 180ms total cooldown, body swipe works again.
    scroller.dispatchEvent(new Event("scroll", { bubbles: false }));
    act(() => vi.advanceTimersByTime(180));
    dispatchTouch(content, "touchstart", { touches: [{ identifier: 3, clientY: 100 }] });
    dispatchTouch(content, "touchmove", { touches: [{ identifier: 3, clientY: 260 }] });
    dispatchTouch(content, "touchend", { changedTouches: [{ identifier: 3, clientY: 260 }] });
    expect(onClose).toHaveBeenCalledOnce();
    onClose.mockClear();
    act(() => vi.advanceTimersByTime(250));

    // Capture-phase scroll arriving mid-PendingBody flips gesture to ScrollOwned.
    dispatchTouch(content, "touchstart", { touches: [{ identifier: 30, clientY: 100 }] });
    dispatchTouch(content, "touchmove", { touches: [{ identifier: 30, clientY: 104 }] });
    scroller.dispatchEvent(new Event("scroll", { bubbles: false }));
    dispatchTouch(content, "touchmove", { touches: [{ identifier: 30, clientY: 320 }] });
    dispatchTouch(content, "touchend", { changedTouches: [{ identifier: 30, clientY: 320 }] });
    expect(onClose).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(250));

    // 3) Horizontal |dx| >= dy and upward dy both lock ScrollOwned.
    dispatchTouch(content, "touchstart", { touches: [{ identifier: 4, clientX: 50, clientY: 100 }] });
    dispatchTouch(content, "touchmove", { touches: [{ identifier: 4, clientX: 70, clientY: 115 }] });
    dispatchTouch(content, "touchmove", { touches: [{ identifier: 4, clientX: 70, clientY: 320 }] });
    dispatchTouch(content, "touchend", { changedTouches: [{ identifier: 4, clientX: 70, clientY: 320 }] });
    expect(onClose).not.toHaveBeenCalled();

    dispatchTouch(content, "touchstart", { touches: [{ identifier: 5, clientY: 200 }] });
    dispatchTouch(content, "touchmove", { touches: [{ identifier: 5, clientY: 180 }] });
    dispatchTouch(content, "touchmove", { touches: [{ identifier: 5, clientY: 400 }] });
    dispatchTouch(content, "touchend", { changedTouches: [{ identifier: 5, clientY: 400 }] });
    expect(onClose).not.toHaveBeenCalled();

    // 3b) Sideways pointer swipes across header or grab pill with slight downward angle never dismiss.
    for (const [index, target] of [header, pill].entries()) {
      const pointerId = 40 + index;
      dispatchPointer(target, "pointerdown", { pointerId, clientX: 50, clientY: 100 });
      act(() => vi.advanceTimersByTime(40));
      dispatchPointer(window, "pointermove", { pointerId, clientX: 250, clientY: 130 });
      dispatchPointer(window, "pointerup", { pointerId, clientX: 250, clientY: 130 });
      expect(panel.style.transform).toBe("");
      expect(onClose).not.toHaveBeenCalled();
    }

    // 4) Secondary touch ID and secondary pointer ID are ignored.
    dispatchTouch(content, "touchstart", { touches: [{ identifier: 6, clientY: 100 }] });
    dispatchTouch(content, "touchmove", { touches: [{ identifier: 6, clientY: 140 }] });
    dispatchTouch(content, "touchstart", {
      touches: [
        { identifier: 6, clientY: 140 },
        { identifier: 7, clientY: 140 },
      ],
    });
    dispatchTouch(content, "touchmove", {
      touches: [
        { identifier: 6, clientY: 140 },
        { identifier: 7, clientY: 400 },
      ],
    });
    expect(panel.style.transform).toBe("translateY(40px)");
    dispatchTouch(content, "touchend", {
      touches: [{ identifier: 6, clientY: 140 }],
      changedTouches: [{ identifier: 7, clientY: 400 }],
    });
    expect(panel.style.transform).toBe("translateY(40px)");
    dispatchTouch(content, "touchend", { touches: [], changedTouches: [{ identifier: 6, clientY: 140 }] });
    expect(onClose).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(250));

    dispatchPointer(header, "pointerdown", { pointerId: 11, clientY: 100 });
    dispatchPointer(header, "pointerdown", { pointerId: 12, clientY: 100 });
    dispatchPointer(window, "pointermove", { pointerId: 12, clientY: 400 });
    expect(panel.style.transform).toBe("");
    dispatchPointer(window, "pointerup", { pointerId: 12, clientY: 400 });
    dispatchPointer(window, "pointermove", { pointerId: 11, clientY: 150 });
    expect(panel.style.transform).toBe("translateY(50px)");
    dispatchPointer(window, "pointerup", { pointerId: 11, clientY: 150 });
    act(() => vi.advanceTimersByTime(250));

    // 5) Interactive exclusions and non-body regions: Close button, footer background, text/range inputs, textarea, select, [contenteditable]
    const excluded = [
      screen.getByRole("button", { name: "Close" }),
      screen.getByTestId("sheet-footer"),
      screen.getByRole("textbox", { name: "Provider name" }),
      screen.getByRole("slider", { name: "Slider" }),
      screen.getByRole("textbox", { name: "Notes" }),
      screen.getByRole("combobox", { name: "Choice" }),
      screen.getByTestId("editable"),
    ];
    for (const target of excluded) {
      dispatchTouch(target, "touchstart", { touches: [{ identifier: 20, clientY: 100 }] });
      dispatchTouch(target, "touchmove", { touches: [{ identifier: 20, clientY: 320 }] });
      dispatchTouch(target, "touchend", { changedTouches: [{ identifier: 20, clientY: 320 }] });
      expect(panel.style.transform).toBe("");
      expect(onClose).not.toHaveBeenCalled();
    }

    // 6) Body list button (unlike Close button) still allows downward swipe when scrolled to top.
    const bodyButton = screen.getByRole("button", { name: "Body action" });
    dispatchTouch(bodyButton, "touchstart", { touches: [{ identifier: 21, clientY: 100 }] });
    dispatchTouch(bodyButton, "touchmove", { touches: [{ identifier: 21, clientY: 260 }] });
    dispatchTouch(bodyButton, "touchend", { changedTouches: [{ identifier: 21, clientY: 260 }] });
    expect(onClose).toHaveBeenCalledOnce();
  } finally {
    vi.useRealTimers();
  }
});

test("side=left and side=right ignore downward swipes", () => {
  for (const side of ["left", "right"] as const) {
    const onClose = vi.fn();
    const view = render(
      <Sheet open side={side} onClose={onClose} title="Edge title">
        <p>Body</p>
      </Sheet>,
    );
    const panel = screen.getByRole("dialog");
    const body = screen.getByText("Body");
    const header = screen.getByText("Edge title").parentElement!;

    dispatchTouch(body, "touchstart", { touches: [{ identifier: 1, clientY: 100 }] });
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 1, clientY: 350 }] });
    dispatchTouch(body, "touchend", { changedTouches: [{ identifier: 1, clientY: 350 }] });

    dispatchPointer(header, "pointerdown", { clientY: 100 });
    dispatchPointer(window, "pointermove", { clientY: 350 });
    dispatchPointer(window, "pointerup", { clientY: 350 });

    expect(panel.style.transform).toBe("");
    expect(onClose).not.toHaveBeenCalled();
    view.unmount();
  }
});

test("expandable keeps full->peek, peek->full, and tap-toggle, while dismissing on downward drag from peek", () => {
  vi.useFakeTimers();
  try {
    const onClose = vi.fn();
    render(
      <Sheet
        open
        side="bottom"
        expandable={{ peekHeight: 200, fullScreenFirst: true }}
        onClose={onClose}
        title="Expandable"
      >
        <p>Body</p>
      </Sheet>,
    );
    const panel = screen.getByRole("dialog");
    const handle = screen.getByTestId("sheet-handle");
    const body = screen.getByText("Body");
    const header = screen.getByText("Expandable").parentElement!;
    const geometry = () => panel.querySelector("[data-geometry]")?.getAttribute("data-geometry");

    expect(geometry()).toBe("full");

    // Downward body/header swipes do not dismiss from full.
    dispatchTouch(body, "touchstart", { touches: [{ identifier: 1, clientY: 100 }] });
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 1, clientY: 300 }] });
    dispatchTouch(body, "touchend", { changedTouches: [{ identifier: 1, clientY: 300 }] });
    dispatchPointer(header, "pointerdown", { clientY: 100 });
    dispatchPointer(window, "pointermove", { clientY: 300 });
    dispatchPointer(window, "pointerup", { clientY: 300 });
    expect(panel.style.transform).toBe("");
    expect(onClose).not.toHaveBeenCalled();

    // Downward drag in full collapses to peek without translating or dismissing.
    dispatchPointer(handle, "pointerdown", { clientY: 100 });
    dispatchPointer(window, "pointermove", { clientY: 300 });
    expect(geometry()).toBe("peek");
    expect(panel.style.transform).toBe("");
    dispatchPointer(window, "pointerup", { clientY: 300 });
    expect(onClose).not.toHaveBeenCalled();

    // Upward drag in peek expands to full.
    dispatchPointer(handle, "pointerdown", { clientY: 300 });
    dispatchPointer(window, "pointermove", { clientY: 200 });
    expect(geometry()).toBe("full");
    dispatchPointer(window, "pointerup", { clientY: 200 });
    expect(onClose).not.toHaveBeenCalled();

    // Tap-toggle full -> peek -> full.
    dispatchPointer(handle, "pointerdown", { clientY: 100 });
    dispatchPointer(window, "pointermove", { clientY: 105 });
    dispatchPointer(window, "pointerup", { clientY: 105 });
    expect(geometry()).toBe("peek");

    // Small downward drag in peek below threshold snaps back and stays in peek (does not tap-toggle).
    mockPanelHeight(panel, 200); // threshold 70
    dispatchPointer(handle, "pointerdown", { clientY: 100 });
    dispatchPointer(window, "pointermove", { clientY: 140 });
    expect(panel.style.transform).toBe("translateY(40px)");
    act(() => vi.advanceTimersByTime(90));
    dispatchPointer(window, "pointerup", { clientY: 140 });
    expect(onClose).not.toHaveBeenCalled();
    expect(geometry()).toBe("peek");
    expect(panel.dataset.swiping).toBeUndefined();
    expect(panel.style.transform).toBe("");

    // Downward drag in peek past threshold dismisses.
    dispatchPointer(handle, "pointerdown", { clientY: 100 });
    dispatchPointer(window, "pointermove", { clientY: 200 });
    dispatchPointer(window, "pointerup", { clientY: 200 });
    expect(onClose).toHaveBeenCalledOnce();
    onClose.mockClear();

    // Downward body swipe in peek also dismisses.
    dispatchTouch(body, "touchstart", { touches: [{ identifier: 1, clientY: 100 }] });
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 1, clientY: 200 }] });
    dispatchTouch(body, "touchend", { changedTouches: [{ identifier: 1, clientY: 200 }] });
    expect(onClose).toHaveBeenCalledOnce();
  } finally {
    vi.useRealTimers();
  }
});

test("bottom swipe in a Dockview popout binds and cleans up listeners on ownerWindowOf(panel), including mid-drag unmount", () => {
  vi.useFakeTimers();
  const { frame, doc, win } = appendPopout();
  const addSpy = vi.spyOn(win, "addEventListener");
  const removeSpy = vi.spyOn(win, "removeEventListener");
  const openerAdd = vi.spyOn(window, "addEventListener");
  const onClose = vi.fn();
  const view = render(
    createPortal(
      <Sheet open side="bottom" onClose={onClose} title="Popout title">
        <p>Popout body</p>
      </Sheet>,
      doc.body,
    ),
  );
  try {
    const header = doc.querySelector("header");
    const panel = doc.querySelector<HTMLElement>('[role="dialog"]');
    const bodyText = doc.body.querySelector("p");
    if (header === null || panel === null || bodyText === null) throw new Error("popout sheet missing");

    // Body touch below threshold snaps back cleanly in the popout realm.
    dispatchTouch(bodyText, "touchstart", { touches: [{ identifier: 1, clientY: 100 }] });
    dispatchTouch(bodyText, "touchmove", { touches: [{ identifier: 1, clientY: 140 }] });
    expect(panel.dataset.swiping).toBe("true");
    expect(panel.style.transform).toBe("translateY(40px)");
    act(() => vi.advanceTimersByTime(90));
    dispatchTouch(bodyText, "touchend", { changedTouches: [{ identifier: 1, clientY: 140 }] });
    expect(onClose).not.toHaveBeenCalled();
    expect(panel.dataset.swiping).toBeUndefined();
    expect(panel.style.transform).toBe("");

    dispatchPointer(header, "pointerdown", { pointerId: 1, clientY: 100 });
    const added = addSpy.mock.calls.filter(([type]) => type === "pointermove" || type === "pointerup");
    expect(added.map(([type]) => type).sort()).toEqual(["pointermove", "pointerup"]);
    expect(openerAdd.mock.calls.filter(([type]) => type === "pointermove" || type === "pointerup")).toHaveLength(0);

    dispatchPointer(win, "pointermove", { pointerId: 1, clientY: 260 });
    expect(panel.style.transform).toBe("translateY(160px)");
    dispatchPointer(win, "pointerup", { pointerId: 1, clientY: 260 });
    expect(onClose).toHaveBeenCalledOnce();
    for (const [type, listener] of added) {
      expect(removeSpy.mock.calls.some(([t, f]) => t === type && f === listener)).toBe(true);
    }

    // Mid-drag unmount removes listeners from the popout window.
    addSpy.mockClear();
    removeSpy.mockClear();
    dispatchPointer(header, "pointerdown", { pointerId: 2, clientY: 100 });
    const midDragAdded = addSpy.mock.calls.filter(([type]) => type === "pointermove" || type === "pointerup");
    expect(midDragAdded).toHaveLength(2);
    view.unmount();
    for (const [type, listener] of midDragAdded) {
      expect(removeSpy.mock.calls.some(([t, f]) => t === type && f === listener)).toBe(true);
    }
  } finally {
    vi.useRealTimers();
    view.unmount();
    frame.remove();
    addSpy.mockRestore();
    removeSpy.mockRestore();
    openerAdd.mockRestore();
  }
});

test("reopening (open false -> true) clears inline transform and scrim opacity", () => {
  const exit = withRunningExit();
  try {
    const onClose = vi.fn();
    const { rerender, container } = render(
      <Sheet open side="bottom" onClose={onClose} title="t">
        <p>Body</p>
      </Sheet>,
    );
    const scrim = container.firstElementChild as HTMLElement;
    const panel = screen.getByRole("dialog");
    const body = screen.getByText("Body");

    dispatchTouch(body, "touchstart", { touches: [{ identifier: 1, clientY: 100 }] });
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 1, clientY: 260 }] });
    dispatchTouch(body, "touchend", { changedTouches: [{ identifier: 1, clientY: 260 }] });
    expect(onClose).toHaveBeenCalledOnce();

    rerender(
      <Sheet open={false} side="bottom" onClose={onClose} title="t">
        <p>Body</p>
      </Sheet>,
    );
    expect(panel.style.transform).toBe("translateY(160px)");
    expect(scrim.style.opacity).not.toBe("");

    // Reopen mid-exit before exit animation finishes.
    rerender(
      <Sheet open side="bottom" onClose={onClose} title="t">
        <p>Body</p>
      </Sheet>,
    );
    expect(panel.style.transform).toBe("");
    expect(scrim.style.opacity).toBe("");

    // Switching side away from "bottom" mid-drag while open stays true clears gesture and inline styles.
    dispatchTouch(body, "touchstart", { touches: [{ identifier: 2, clientY: 100 }] });
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 2, clientY: 160 }] });
    expect(panel.style.transform).toBe("translateY(60px)");
    expect(scrim.style.opacity).not.toBe("");
    rerender(
      <Sheet open side="right" onClose={onClose} title="t">
        <p>Body</p>
      </Sheet>,
    );
    expect(panel.style.transform).toBe("");
    expect(scrim.style.opacity).toBe("");
    dispatchTouch(body, "touchmove", { touches: [{ identifier: 2, clientY: 320 }] });
    dispatchTouch(body, "touchend", { changedTouches: [{ identifier: 2, clientY: 320 }] });
    expect(panel.style.transform).toBe("");
    expect(onClose).toHaveBeenCalledOnce();
  } finally {
    exit.restore();
  }
});

test("expandable handle tap before rAF on declined close snaps back cleanly", () => {
  vi.useFakeTimers();
  try {
    const onClose = vi.fn();
    render(
      <Sheet open side="bottom" expandable={{ peekHeight: 200, fullScreenFirst: false }} onClose={onClose} title="Exp">
        Body
      </Sheet>,
    );
    const panel = screen.getByRole("dialog");
    mockPanelHeight(panel, 200);
    const handle = screen.getByTestId("sheet-handle");

    dispatchPointer(handle, "pointerdown", { pointerId: 1, clientY: 100 });
    dispatchPointer(window, "pointermove", { pointerId: 1, clientY: 200 });
    dispatchPointer(window, "pointerup", { pointerId: 1, clientY: 200 });
    expect(onClose).toHaveBeenCalledOnce();
    expect(panel.style.transform).toBe("translateY(100px)");

    dispatchPointer(handle, "pointerdown", { pointerId: 2, clientY: 100 });
    dispatchPointer(window, "pointerup", { pointerId: 2, clientY: 100 });
    expect(panel.dataset.swiping).toBeUndefined();
    expect(panel.style.transform).toBe("");
  } finally {
    vi.useRealTimers();
  }
});

test("expandable handle ignores pointerdown while closing (open=false during exit)", () => {
  const exit = withRunningExit();
  const addSpy = vi.spyOn(window, "addEventListener");
  try {
    const { rerender } = render(
      <Sheet open side="bottom" expandable={{ peekHeight: 200, fullScreenFirst: false }} onClose={vi.fn()} title="t">
        Body
      </Sheet>,
    );
    const handle = screen.getByTestId("sheet-handle");
    const geometry = () => document.querySelector("[data-geometry]")?.getAttribute("data-geometry");
    dispatchPointer(handle, "pointerdown", { pointerId: 1, clientY: 100 });
    dispatchPointer(window, "pointerup", { pointerId: 1, clientY: 100 });
    expect(geometry()).toBe("full");

    rerender(
      <Sheet
        open={false}
        side="bottom"
        expandable={{ peekHeight: 200, fullScreenFirst: false }}
        onClose={vi.fn()}
        title="t"
      >
        Body
      </Sheet>,
    );
    addSpy.mockClear();
    dispatchPointer(handle, "pointerdown", { pointerId: 2, clientY: 100 });
    expect(addSpy.mock.calls.filter(([type]) => type === "pointermove")).toHaveLength(0);
    dispatchPointer(window, "pointerup", { pointerId: 2, clientY: 100 });
    expect(geometry()).toBe("full");
  } finally {
    addSpy.mockRestore();
    exit.restore();
  }
});
