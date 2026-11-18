import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { createRoot } from "react-dom/client";
import { expect, test, vi } from "vitest";
import { MOBILE_QUERY } from "../../shell/useIsMobile";
import { escapeRegExp } from "../../styles/cssBlock";
import { Button } from "../button";
import { Dialog } from "../dialog";
import { appendPopout } from "../internal/popoutTestUtils";
import { Textarea } from "../textarea";
import { Menu, type MenuItem } from "./index";

function items(overrides?: Partial<Record<string, Partial<MenuItem>>>): MenuItem[] {
  const base: MenuItem[] = [
    { id: "rename", label: "Rename", onSelect: vi.fn() },
    { id: "duplicate", label: "Duplicate", onSelect: vi.fn() },
    { id: "delete", label: "Delete", onSelect: vi.fn() },
  ];
  if (!overrides) return base;
  return base.map((item) => (overrides[item.id] ? { ...item, ...overrides[item.id] } : item));
}

// The composer's own shape, reduced to what the keyboard case needs: a text
// field the person is typing into (so the software keyboard is up) beside a
// Menu trigger in the same control row.
function ComposerLikeSurface() {
  const [draft, setDraft] = useState("");
  return (
    <div>
      <Textarea aria-label="Message" value={draft} onChange={(event) => setDraft(event.target.value)} />
      <Menu trigger="Actions" items={items()} />
    </div>
  );
}

// SessionMenu's own shape and order (shell/sessionMenu/SessionMenu.tsx), with
// the one entry SessionChrome drops on a short viewport: "Find in session…"
// sits BEFORE Pin and Archive in the inspection group. Dropping it moves both
// of them down one index.
function sessionActionsItems(showFind: boolean): MenuItem[] {
  const inspection: MenuItem[] = [
    { id: "overview", label: "Overview", onSelect: vi.fn() },
    ...(showFind ? [{ id: "find", label: "Find in session…", onSelect: vi.fn() }] : []),
  ];
  return [
    ...inspection,
    { id: "rename", label: "Rename", onSelect: vi.fn() },
    { id: "pin", label: "Pin this session…", onSelect: vi.fn() },
    { id: "archive", label: "Archive", onSelect: vi.fn() },
  ];
}

// The live shape of the bug: a window resize re-renders the caller with one
// entry gone, exactly as AppShell re-rendering SessionChrome does when the
// viewport crosses 480px (SessionChrome reads window.innerHeight when its
// items are built).
function ShrinkingSessionMenu({ onArchive }: { onArchive: () => void }) {
  const [showFind, setShowFind] = useState(() => window.innerHeight >= 480);
  useEffect(() => {
    const onResize = () => setShowFind(window.innerHeight >= 480);
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);
  const list = sessionActionsItems(showFind).map((entry) =>
    entry.id === "archive" ? { ...entry, onSelect: onArchive } : entry,
  );
  return <Menu trigger="Actions" items={list} />;
}

test("renders only the trigger when closed", () => {
  render(<Menu trigger="Actions" items={items()} />);
  expect(screen.getByRole("button", { name: "Actions" })).toBeTruthy();
  expect(screen.queryByRole("menu")).toBeNull();
});

test("clicking the trigger opens the menu with its items", async () => {
  const user = userEvent.setup();
  render(<Menu trigger="Actions" items={items()} />);
  await user.click(screen.getByRole("button", { name: "Actions" }));
  expect(screen.getByRole("menu")).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Rename" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Duplicate" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Delete" })).toBeTruthy();
});

test("clicking the trigger again closes the menu", async () => {
  const user = userEvent.setup();
  render(<Menu trigger="Actions" items={items()} />);
  const trigger = screen.getByRole("button", { name: "Actions" });
  await user.click(trigger);
  expect(screen.getByRole("menu")).toBeTruthy();
  await user.click(trigger);
  expect(screen.queryByRole("menu")).toBeNull();
});

test("the trigger announces its popup state via aria-haspopup/aria-expanded", async () => {
  const user = userEvent.setup();
  render(<Menu trigger="Actions" items={items()} />);
  const trigger = screen.getByRole("button", { name: "Actions" });
  expect(trigger.getAttribute("aria-haspopup")).toBe("menu");
  expect(trigger.getAttribute("aria-expanded")).toBe("false");
  await user.click(trigger);
  expect(trigger.getAttribute("aria-expanded")).toBe("true");
});

// --- fix-wave: role=menu accessible name (Important) -------------------
// A menu with no accessible name announces as just "menu" to a screen
// reader - indistinguishable from any other menu on the page. Labelling
// it via the trigger (whatever text/content opened it) is the standard
// menu-button pattern and needs no new prop: the trigger already carries
// the name a sighted user associates with this menu.
test("the popup's accessible name matches the trigger content (aria-labelledby)", async () => {
  const user = userEvent.setup();
  render(<Menu trigger="Actions" items={items()} />);
  await user.click(screen.getByRole("button", { name: "Actions" }));
  expect(screen.getByRole("menu", { name: "Actions" })).toBeTruthy();
});

test("opening focuses the first item", async () => {
  const user = userEvent.setup();
  render(<Menu trigger="Actions" items={items()} />);
  await user.click(screen.getByRole("button", { name: "Actions" }));
  expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Rename" }));
});

test("clicking an item selects it and closes the menu", async () => {
  const user = userEvent.setup();
  const list = items();
  render(<Menu trigger="Actions" items={list} />);
  await user.click(screen.getByRole("button", { name: "Actions" }));
  await user.click(screen.getByRole("menuitem", { name: "Duplicate" }));
  expect(list[1]!.onSelect).toHaveBeenCalledOnce();
  expect(screen.queryByRole("menu")).toBeNull();
});

test("a disabled item cannot be selected by clicking, and the menu stays open", async () => {
  const user = userEvent.setup();
  const list = items({ delete: { disabled: true } });
  render(<Menu trigger="Actions" items={list} />);
  await user.click(screen.getByRole("button", { name: "Actions" }));
  await user.click(screen.getByRole("menuitem", { name: "Delete" }));
  expect(list[2]!.onSelect).not.toHaveBeenCalled();
  expect(screen.getByRole("menu")).toBeTruthy();
});

test("ArrowDown moves roving focus to the next item, wrapping past the last", async () => {
  const user = userEvent.setup();
  render(<Menu trigger="Actions" items={items()} />);
  await user.click(screen.getByRole("button", { name: "Actions" }));
  expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Rename" }));
  await user.keyboard("{ArrowDown}");
  expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Duplicate" }));
  await user.keyboard("{ArrowDown}");
  expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Delete" }));
  await user.keyboard("{ArrowDown}");
  expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Rename" }));
});

test("ArrowUp from the first item wraps to the last", async () => {
  const user = userEvent.setup();
  render(<Menu trigger="Actions" items={items()} />);
  await user.click(screen.getByRole("button", { name: "Actions" }));
  await user.keyboard("{ArrowUp}");
  expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Delete" }));
});

test("Home and End jump to the first and last items", async () => {
  const user = userEvent.setup();
  render(<Menu trigger="Actions" items={items()} />);
  await user.click(screen.getByRole("button", { name: "Actions" }));
  await user.keyboard("{End}");
  expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Delete" }));
  await user.keyboard("{Home}");
  expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Rename" }));
});

test("ArrowDown/ArrowUp roving skips disabled items", async () => {
  const user = userEvent.setup();
  render(<Menu trigger="Actions" items={items({ duplicate: { disabled: true } })} />);
  await user.click(screen.getByRole("button", { name: "Actions" }));
  expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Rename" }));
  await user.keyboard("{ArrowDown}");
  expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Delete" }));
  await user.keyboard("{ArrowUp}");
  expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Rename" }));
});

test("Enter activates the focused item and closes the menu", async () => {
  const user = userEvent.setup();
  const list = items();
  render(<Menu trigger="Actions" items={list} />);
  await user.click(screen.getByRole("button", { name: "Actions" }));
  await user.keyboard("{ArrowDown}{Enter}");
  expect(list[1]!.onSelect).toHaveBeenCalledOnce();
  expect(screen.queryByRole("menu")).toBeNull();
});

test("Space activates the focused item and closes the menu", async () => {
  const user = userEvent.setup();
  const list = items();
  render(<Menu trigger="Actions" items={list} />);
  await user.click(screen.getByRole("button", { name: "Actions" }));
  await user.keyboard(" ");
  expect(list[0]!.onSelect).toHaveBeenCalledOnce();
  expect(screen.queryByRole("menu")).toBeNull();
});

test("Escape closes the menu and returns focus to the trigger", async () => {
  const user = userEvent.setup();
  render(<Menu trigger="Actions" items={items()} />);
  const trigger = screen.getByRole("button", { name: "Actions" });
  await user.click(trigger);
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("menu")).toBeNull();
  expect(document.activeElement).toBe(trigger);
});

test("clicking outside the menu closes it", async () => {
  const user = userEvent.setup();
  render(
    <div>
      <Menu trigger="Actions" items={items()} />
      <button type="button">Elsewhere</button>
    </div>,
  );
  await user.click(screen.getByRole("button", { name: "Actions" }));
  expect(screen.getByRole("menu")).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Elsewhere" }));
  expect(screen.queryByRole("menu")).toBeNull();
});

// --- scroll-close semantics (2026-08) --------------------------------------
// The popup is portaled and fixed-positioned off the trigger's rect, so the
// menu must close when a scroll can move the trigger out from under it - but
// ONLY then. The live case for "stays open": the session pane's "..." menu
// sits in the chrome while the transcript scrolls beside it (kata: scrolling
// a session must not dismiss its open context menu).

test("a scroll in a container that does not hold the trigger leaves the menu open", async () => {
  const user = userEvent.setup();
  render(
    <div>
      <Menu trigger="Actions" items={items()} />
      <div data-testid="unrelated-scroller" />
    </div>,
  );
  await user.click(screen.getByRole("button", { name: "Actions" }));
  expect(screen.getByRole("menu")).toBeTruthy();

  fireEvent.scroll(screen.getByTestId("unrelated-scroller"));

  expect(screen.getByRole("menu")).toBeTruthy();
});

test("a scroll inside the trigger's own container closes the menu", async () => {
  const user = userEvent.setup();
  render(
    <div data-testid="owning-scroller">
      <Menu trigger="Actions" items={items()} />
    </div>,
  );
  await user.click(screen.getByRole("button", { name: "Actions" }));
  expect(screen.getByRole("menu")).toBeTruthy();

  fireEvent.scroll(screen.getByTestId("owning-scroller"));

  expect(screen.queryByRole("menu")).toBeNull();
});

test("a document scroll closes the menu", async () => {
  const user = userEvent.setup();
  render(<Menu trigger="Actions" items={items()} />);
  await user.click(screen.getByRole("button", { name: "Actions" }));
  expect(screen.getByRole("menu")).toBeTruthy();

  fireEvent.scroll(document);

  expect(screen.queryByRole("menu")).toBeNull();
});

// A viewport resize re-anchors the menu to the trigger's new rect. It used to
// close: on Android the resize that matters is the software keyboard leaving
// when the menu's own FocusScope takes focus out of the composer's editor,
// and closing on it swallowed the tap that opened the menu. A reader's
// dismissal is a press (the test below), never a resize. See the keyboard
// block below for the full chain.
test("a viewport resize re-anchors the menu instead of closing it", async () => {
  const user = userEvent.setup();
  let triggerTop = 400;
  const box = (left: number, top: number, width: number, height: number) =>
    ({
      left,
      top,
      right: left + width,
      bottom: top + height,
      width,
      height,
      x: left,
      y: top,
      toJSON: () => ({}),
    }) as DOMRect;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, triggerTop, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 160, 120);
  try {
    render(<Menu trigger="Actions" items={items()} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    expect(screen.getByRole("menu").style.top).toBe("432px"); // 400 + 28 + TRIGGER_GAP

    triggerTop = 200;
    fireEvent(window, new Event("resize"));

    expect(screen.getByRole("menu").style.top).toBe("232px"); // 200 + 28 + TRIGGER_GAP
  } finally {
    vi.restoreAllMocks();
  }
});

// --- the software keyboard is not a dismiss gesture -----------------------
// index.html sets `interactive-widget=resizes-content`, so on Android Chrome
// the layout viewport grows back when the keyboard closes, and the window
// fires `resize` for it. Opening this menu from the composer's control row is
// what closes the keyboard: FocusScope moves focus out of the editor. That
// resize carries no pointer press, so it is not the reader dismissing
// anything. The menu must stay open AND follow the trigger (a fix that merely
// stopped closing could leave it detached).
test("a keyboard-dismissal resize leaves the composer's menu open and re-anchored", async () => {
  const user = userEvent.setup();
  // Geometry chosen so the placement can only come out right if BOTH the
  // trigger's new rect and the new viewport height are read after the resize.
  // Popup 160x120, TRIGGER_GAP 4, EDGE_MARGIN 8.
  //
  //   keyboard up   (height 400): trigger top 300 -> below overflows, flips
  //                               above: 300 - 4 - 120 = 176
  //   keyboard gone (height 800): trigger top 500 -> below fits: 528 + 4 = 532
  //
  // A stale trigger rect (300) with the new height still flips to 176; a stale
  // viewport (400) with the new rect overflows and flips to 376. Only the
  // fresh measurement gives 532, so this test fails on either stale input.
  let triggerTop = 300;
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 400 });
  const box = (left: number, top: number, width: number, height: number) =>
    ({
      left,
      top,
      right: left + width,
      bottom: top + height,
      width,
      height,
      x: left,
      y: top,
      toJSON: () => ({}),
    }) as DOMRect;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, triggerTop, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 160, 120);
  try {
    render(<ComposerLikeSurface />);

    const editor = screen.getByRole("textbox", { name: "Message" });
    await user.click(editor);
    expect(document.activeElement).toBe(editor);

    await user.click(screen.getByRole("button", { name: "Actions" }));
    expect(screen.getByRole("menu")).toBeTruthy();
    // The focus move that dismisses the Android keyboard: the editor the
    // person was typing in no longer has focus, and a menu item has it.
    expect(document.activeElement).not.toBe(editor);
    expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Rename" }));
    expect(screen.getByRole("menu").style.top).toBe("176px"); // flipped above

    // The keyboard is gone: the layout viewport grew by its height, the
    // composer moved down with it, and the browser reports both.
    triggerTop = 500;
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
    fireEvent(window, new Event("resize"));

    expect(screen.getByRole("menu")).toBeTruthy();
    expect(screen.getByRole("menu").style.top).toBe("532px"); // fits below again
  } finally {
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// The negative direction: the menu is still live after that resize, and a
// genuine press outside still dismisses it. Without this, a fix that simply
// stopped closing the menu would pass the test above.
test("an outside press still closes the menu after a keyboard-dismissal resize", async () => {
  const user = userEvent.setup();
  render(
    <>
      <ComposerLikeSurface />
      <Button type="button">Elsewhere</Button>
    </>,
  );
  await user.click(screen.getByRole("textbox", { name: "Message" }));
  await user.click(screen.getByRole("button", { name: "Actions" }));
  expect(screen.getByRole("menu")).toBeTruthy();

  fireEvent(window, new Event("resize"));
  expect(screen.getByRole("menu")).toBeTruthy();

  await user.click(screen.getByRole("button", { name: "Elsewhere" }));
  expect(screen.queryByRole("menu")).toBeNull();
});

// A frame the component will see once, with the position it computed inside
// the frame: rAF is captured rather than awaited so the test drives the exact
// moment the widget re-measures, instead of racing a timer.
function captureFrames(): { flush: () => void } {
  const pending: FrameRequestCallback[] = [];
  vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
    pending.push(callback);
    return pending.length;
  });
  return {
    flush: () => {
      act(() => {
        for (const callback of pending.splice(0)) callback(0);
      });
    },
  };
}

// --- iOS resizes only the VISUAL viewport ----------------------------------
// The mobile shell spends a visual-viewport shrink as `--keyboard-inset`
// padding, which slides the composer and this trigger down. No window
// `resize` fires for it, so a menu listening only on the window stayed at the
// coordinates it measured while the keyboard was still up.
test("a visual-viewport resize re-anchors the menu to the trigger it moved", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  const fakeViewport = new EventTarget() as EventTarget & { height: number };
  fakeViewport.height = 400;
  vi.stubGlobal("visualViewport", fakeViewport);
  const { flush } = captureFrames();
  let triggerTop = 300;
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 400 });
  const box = (left: number, top: number, width: number, height: number) =>
    ({
      left,
      top,
      right: left + width,
      bottom: top + height,
      width,
      height,
      x: left,
      y: top,
      toJSON: () => ({}),
    }) as DOMRect;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, triggerTop, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 160, 120);
  try {
    render(<Menu trigger="Actions" items={items()} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    expect(screen.getByRole("menu").style.top).toBe("176px"); // flipped above

    // The keyboard's inset lands: the composer and its trigger slide down, and
    // only the visual viewport reports it.
    triggerTop = 500;
    fakeViewport.height = 800;
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
    fakeViewport.dispatchEvent(new Event("resize"));
    flush();

    expect(screen.getByRole("menu")).toBeTruthy();
    expect(screen.getByRole("menu").style.top).toBe("532px"); // follows the trigger
  } finally {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// --- the trigger moves after the resize event ------------------------------
// Dockview lays out from an observed box, a frame after the window's own
// resize. Measuring in the resize handler therefore reads the trigger's OLD
// rect, and the footer then moves with no further window event. The
// ResizeObserver notification is the second chance to measure - it arrives
// after layout, which is the only moment the trigger's final rect exists.
test("a desktop resize that lands before the layout settles still ends on the trigger's final position", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  let triggerTop = 300;
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 400 });
  const box = (left: number, top: number, width: number, height: number) =>
    ({
      left,
      top,
      right: left + width,
      bottom: top + height,
      width,
      height,
      x: left,
      y: top,
      toJSON: () => ({}),
    }) as DOMRect;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, triggerTop, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 160, 120);
  // A ResizeObserver the test fires by hand, the way a browser fires it when
  // the observed trigger's box changes.
  const callbacks = new Map<Element, () => void>();
  class HandDrivenResizeObserver {
    constructor(private readonly callback: () => void) {}
    observe(target: Element): void {
      callbacks.set(target, this.callback);
    }
    unobserve(target: Element): void {
      callbacks.delete(target);
    }
    disconnect(): void {
      for (const [target, callback] of [...callbacks]) {
        if (callback === this.callback) callbacks.delete(target);
      }
    }
  }
  vi.stubGlobal("ResizeObserver", HandDrivenResizeObserver);
  const { flush } = captureFrames();
  try {
    render(<Menu trigger="Actions" items={items()} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    expect(screen.getByRole("menu").style.top).toBe("176px"); // flipped above

    // The window grows while the trigger is still where the keyboard left it:
    // the resize handler measures that stale rect and places the popup below
    // it, which is the detachment the reviewer described.
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
    fireEvent(window, new Event("resize"));
    expect(screen.getByRole("menu").style.top).toBe("332px"); // 300 + 28 + 4

    // Dockview finishes: the trigger is at its final position, and the
    // observed box is what says so.
    triggerTop = 500;
    act(() => {
      for (const callback of callbacks.values()) callback();
    });
    flush();

    expect(screen.getByRole("menu").style.top).toBe("532px"); // 500 + 28 + 4
  } finally {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// The reviewer's sharpest point about the trigger-only observer: a fixed-size
// footer trigger that only MOVES reports no box change, so an observer on the
// trigger alone stays silent. The popup must still land on the trigger's
// settled rect, which is why the resize path also re-measures after layout
// rather than relying on an observation.
test("a trigger that only moves, with its box unchanged, is still followed after layout", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  let triggerTop = 300;
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 400 });
  const box = (left: number, top: number, width: number, height: number) =>
    ({
      left,
      top,
      right: left + width,
      bottom: top + height,
      width,
      height,
      x: left,
      y: top,
      toJSON: () => ({}),
    }) as DOMRect;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    // Same 40x28 box at every position: nothing about its SIZE changes.
    if (this.tagName === "BUTTON") return box(100, triggerTop, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 160, 120);
  // An observer that never fires: this is the pure-translation case.
  class SilentResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  vi.stubGlobal("ResizeObserver", SilentResizeObserver);
  const { flush } = captureFrames();
  try {
    render(<Menu trigger="Actions" items={items()} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    expect(screen.getByRole("menu").style.top).toBe("176px"); // flipped above

    // The window grows, and the resize reaches the widget WHILE THE TRIGGER IS
    // STILL at its old position - which is what Dockview's deferred layout
    // means: this handler's measurement is of the pre-layout box.
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
    fireEvent(window, new Event("resize"));
    expect(screen.getByRole("menu").style.top).toBe("332px"); // measured the old rect

    // Dockview finishes its layout. The footer moves, its SIZE does not, and
    // no further window event fires - so the silent observer reports nothing
    // and only the frame the resize handler scheduled can see this.
    triggerTop = 500;
    flush();

    expect(screen.getByRole("menu")).toBeTruthy();
    expect(screen.getByRole("menu").style.top).toBe("532px");
  } finally {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// A caller that changes the item set changes the popup's HEIGHT without
// touching the trigger. For an upward-opening popup the top is computed from
// that height, so a stale height leaves the popup overlapping its trigger.
// Observing the popup's own box is what catches it (Popup does the same for
// its asynchronously-sized panel).
test("a change to the popup's own height re-anchors an upward-opening menu", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 400 });
  let popupHeight = 120;
  const box = (left: number, top: number, width: number, height: number) =>
    ({
      left,
      top,
      right: left + width,
      bottom: top + height,
      width,
      height,
      x: left,
      y: top,
      toJSON: () => ({}),
    }) as DOMRect;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, 300, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, popupHeight);
    return box(0, 0, 0, 0);
  });
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    return this.tagName === "UL" ? popupHeight : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    return this.tagName === "UL" ? 160 : 0;
  });
  const observers = new Map<Element, () => void>();
  class RecordingResizeObserver {
    constructor(private readonly callback: () => void) {}
    observe(target: Element): void {
      observers.set(target, this.callback);
    }
    unobserve() {}
    disconnect() {}
  }
  vi.stubGlobal("ResizeObserver", RecordingResizeObserver);
  const { flush } = captureFrames();
  try {
    render(<Menu trigger="Actions" items={items()} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    expect(screen.getByRole("menu").style.top).toBe("176px"); // 300 - 4 - 120

    // The caller adds an entry: the popup grows to 200px. The trigger has not
    // moved and its box is unchanged, so ONLY the popup's own observation
    // fires. Firing the trigger's as well would re-measure anything, and the
    // test would pass with the popup unobserved.
    popupHeight = 200;
    const popupCallback = [...observers].find(([element]) => element.tagName === "UL")?.[1];
    act(() => {
      popupCallback?.();
    });
    flush();

    // 300 - 4 - 200: the grown popup now clears its trigger instead of
    // keeping the 176px top its old height had earned.
    expect(screen.getByRole("menu").style.top).toBe("96px");
  } finally {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// --- the menu follows its item set, not a snapshot -------------------------
// With the menu open and "Pin this session…" focused, a caller that drops an
// EARLIER entry (SessionChrome drops "Find in session…" below 480px) left the
// stored index pointing at "Archive". Enter then archived the session while
// the person was looking at Pin. The active item is now tracked by its stable
// id, resolved against the list as it is at activation time, so the key
// activates what is focused.
test("an entry disappearing above the focused item cannot retarget Enter at the next one", async () => {
  const user = userEvent.setup();
  const onArchive = vi.fn();
  render(<ShrinkingSessionMenu onArchive={onArchive} />);

  await user.click(screen.getByRole("button", { name: "Actions" }));
  // Rove down to "Pin this session…" - the row whose index moves when Find
  // disappears.
  for (let step = 0; step < 3; step++) await user.keyboard("{ArrowDown}");
  const pin = screen.getByRole("menuitem", { name: "Pin this session…" });
  expect(document.activeElement).toBe(pin);

  // The viewport crossed the threshold: Find is gone, so Pin and Archive each
  // slid down one index. Focus is still on the same row the person is on.
  const tallerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 400 });
  try {
    fireEvent(window, new Event("resize"));
    expect(screen.queryByRole("menuitem", { name: "Find in session…" })).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Pin this session…" }));

    await user.keyboard("{Enter}");
  } finally {
    if (tallerHeight) Object.defineProperty(window, "innerHeight", tallerHeight);
  }

  expect(onArchive).not.toHaveBeenCalled();
  // Pin's own selection opens a picker in the app; here it closes the menu,
  // which proves Enter activated Pin and not its neighbour.
  expect(screen.queryByRole("menu")).toBeNull();
});

// --- activation resolves against the FOCUSED row ---------------------------
// Tracking the id fixed the entry that leaves the list. It did not fix the id
// naming an entry that has gone and come BACK: dropping Find moves focus to a
// surviving row, and if the id still names Find, Find's return re-points
// activation at it while the person is looking at the replacement. And it did
// not fix a row that becomes disabled in place, where focus stays put but the
// id resolution skips to the first enabled row instead.
function ReturningFindMenu({ onFind, onOverview }: { onFind: () => void; onOverview: () => void }) {
  const [showFind, setShowFind] = useState(() => window.innerHeight >= 480);
  useEffect(() => {
    const onResize = () => setShowFind(window.innerHeight >= 480);
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);
  const list: MenuItem[] = sessionActionsItems(showFind).map((entry) =>
    entry.id === "find"
      ? { ...entry, onSelect: onFind }
      : entry.id === "overview"
        ? { ...entry, onSelect: onOverview }
        : entry,
  );
  return <Menu trigger="Actions" items={list} />;
}

test("an entry that leaves and comes back cannot retarget Enter away from the row focus was moved to", async () => {
  const user = userEvent.setup();
  const onFind = vi.fn();
  const onOverview = vi.fn();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  try {
    render(<ReturningFindMenu onFind={onFind} onOverview={onOverview} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    // Focus Find itself - the entry that is about to leave.
    await user.keyboard("{ArrowDown}");
    expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Find in session…" }));

    Object.defineProperty(window, "innerHeight", { configurable: true, value: 400 });
    fireEvent(window, new Event("resize"));
    expect(screen.queryByRole("menuitem", { name: "Find in session…" })).toBeNull();
    // Focus had to move somewhere: the surviving row the id resolution names.
    expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Overview" }));

    // Find comes back. Focus is still on Overview, so Enter must activate
    // Overview - not the entry the id still named while focus moved.
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
    fireEvent(window, new Event("resize"));
    expect(screen.getByRole("menuitem", { name: "Find in session…" })).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Overview" }));

    await user.keyboard("{Enter}");
  } finally {
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }

  expect(onOverview).toHaveBeenCalledOnce();
  expect(onFind).not.toHaveBeenCalled();
});

// A row that becomes disabled while focused stays focused (nothing removes the
// node), so the id no longer resolves to an actionable entry and the old code
// fell back to the first enabled row - activating a row the person was not on.
function DisablingMenu() {
  const [disabled, setDisabled] = useState(false);
  useEffect(() => {
    const onResize = () => setDisabled(true);
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);
  const list = items();
  return (
    <Menu trigger="Actions" items={list.map((entry) => (entry.id === "duplicate" ? { ...entry, disabled } : entry))} />
  );
}

test("a row disabled in place cannot retarget Enter at the first enabled row", async () => {
  const user = userEvent.setup();
  render(<DisablingMenu />);
  const duplicate = screen.getByRole("button", { name: "Actions" });
  await user.click(duplicate);
  await user.keyboard("{ArrowDown}");
  const focusedRow = screen.getByRole("menuitem", { name: "Duplicate" });
  expect(document.activeElement).toBe(focusedRow);

  fireEvent(window, new Event("resize"));
  // Focus is still on the row the person was on, which is now disabled.
  expect(document.activeElement).toBe(focusedRow);

  await user.keyboard("{Enter}");
  // A disabled row activates nothing: it must not fall through to "Rename".
  expect(screen.getByRole("menu")).toBeTruthy();
});

test("Tab is trapped within the open menu", async () => {
  const user = userEvent.setup();
  render(
    <div>
      <Menu trigger="Actions" items={items()} />
      <button type="button">Elsewhere</button>
    </div>,
  );
  await user.click(screen.getByRole("button", { name: "Actions" }));
  await user.tab();
  // Roving tabindex means exactly one item is ever a Tab stop, so Tab from
  // it loops right back to itself rather than reaching "Elsewhere" -
  // FocusScope's trap contract (see focusscope.test.tsx), reused as-is.
  expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Rename" }));
});

test("ArrowDown on the closed trigger opens the menu and focuses the first item", async () => {
  const user = userEvent.setup();
  render(<Menu trigger="Actions" items={items()} />);
  screen.getByRole("button", { name: "Actions" }).focus();
  await user.keyboard("{ArrowDown}");
  expect(screen.getByRole("menu")).toBeTruthy();
  expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Rename" }));
});

test("declares a :focus-visible rule in its CSS module, using only tokens", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "menu.module.css"), "utf8");
  expect(css).toContain(":focus-visible");
});

test("keeps the menu trigger and items at the narrow-viewport tap floor", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "menu.module.css"), "utf8");
  expect(css).toMatch(
    new RegExp(
      `@media ${escapeRegExp(MOBILE_QUERY)}, \\(any-pointer: coarse\\)\\s*\\{[\\s\\S]*?\\.trigger\\s*\\{[^}]*min-height:\\s*var\\(--tap-min\\)`,
    ),
  );
  expect(css).toMatch(
    new RegExp(
      `@media ${escapeRegExp(MOBILE_QUERY)}, \\(any-pointer: coarse\\)\\s*\\{[\\s\\S]*?\\.item\\s*\\{[^}]*min-height:\\s*var\\(--tap-min\\)`,
    ),
  );
});

// jsdom does not evaluate real CSS animations or media queries - see the
// exemplar/dialog pattern this mirrors. The popup floats by the shared recipe
// (its keyframes and reduced-motion gate live there); on a phone it keeps the
// fade and drops the scale.
test("the popup floats by the shared recipe, and fades without the scale on a phone", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "menu.module.css"), "utf8");
  const popup = /\.popup\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
  expect(popup).toMatch(/composes:\s*float from "\.\.\/\.\.\/styles\/motion\.module\.css"/);
  expect(css).not.toMatch(/animation|@keyframes/);
  expect(css).toMatch(
    new RegExp(`@media ${escapeRegExp(MOBILE_QUERY)}\\s*\\{\\s*\\.popup\\s*\\{[^}]*--motion-float-from:\\s*1;`),
  );
});

// --- fix-wave: nested-overlay Escape containment (Important) -----------

// --- fix-wave: triggerTabIndex + consume-then-stop key handling --------
// A Menu nested inside another roving-tabindex widget's row (see
// shell/rail/RailRow.tsx) must not add its trigger as a SECOND,
// always-focusable Tab stop alongside that widget's own single roving one,
// and a key this trigger already gives meaning to (open the menu) must not
// also reach that widget's own key handling for the same keypress.

test("triggerTabIndex, when given, overrides the trigger's own tabIndex", () => {
  render(<Menu trigger="Actions" items={items()} triggerTabIndex={-1} />);
  expect(screen.getByRole("button", { name: "Actions" }).tabIndex).toBe(-1);
});

test("omitting triggerTabIndex leaves the trigger at its native default (a normal Tab stop) - every existing consumer is unaffected", () => {
  render(<Menu trigger="Actions" items={items()} />);
  expect(screen.getByRole("button", { name: "Actions" }).tabIndex).toBe(0);
});

test.each(["{ArrowDown}", "{ArrowUp}", "{Enter}", " "])(
  "%s on the trigger opens the menu but never bubbles to an ancestor's own key handling",
  async (key) => {
    const user = userEvent.setup();
    const onAncestorKeyDown = vi.fn();
    render(
      // Test-only bubbling probe, never a real widget: this div exists
      // purely to detect whether Menu's own key handling escapes upward.
      // biome-ignore lint/a11y/noStaticElementInteractions: test-only event-bubbling probe, not a rendered widget
      <div onKeyDown={onAncestorKeyDown}>
        <Menu trigger="Actions" items={items()} />
      </div>,
    );
    screen.getByRole("button", { name: "Actions" }).focus();
    await user.keyboard(key);
    expect(screen.getByRole("menu")).toBeTruthy();
    expect(onAncestorKeyDown).not.toHaveBeenCalled();
  },
);

// --- icon-only trigger --------------------------------------------------
// The rail's row and section menus and the session menu all open from a
// bare glyph: the accessible name is `label`, the glyph is decoration.

test("an icon-only trigger takes its accessible name from label and hides the glyph from assistive tech", () => {
  render(<Menu icon={<span data-testid="glyph">⋯</span>} label="Actions for Alpha" items={items()} />);
  const trigger = screen.getByRole("button", { name: "Actions for Alpha" });
  const glyph = screen.getByTestId("glyph");
  expect(trigger.contains(glyph)).toBe(true);
  expect(glyph.closest("[aria-hidden='true']")).not.toBeNull();
});

test("an icon-only trigger names the popup it opens", async () => {
  const user = userEvent.setup();
  render(<Menu icon="⋯" label="Actions for Alpha" items={items()} />);
  await user.click(screen.getByRole("button", { name: "Actions for Alpha" }));
  expect(screen.getByRole("menu", { name: "Actions for Alpha" })).toBeTruthy();
});

test("an icon-only trigger keeps triggerTabIndex and the trigger's key containment", async () => {
  const user = userEvent.setup();
  const onAncestorKeyDown = vi.fn();
  render(
    // biome-ignore lint/a11y/noStaticElementInteractions: test-only event-bubbling probe, not a rendered widget
    <div onKeyDown={onAncestorKeyDown}>
      <Menu icon="⋯" label="Actions" items={items()} triggerTabIndex={-1} />
    </div>,
  );
  const trigger = screen.getByRole("button", { name: "Actions" });
  expect(trigger.tabIndex).toBe(-1);
  trigger.focus();
  await user.keyboard("{ArrowDown}");
  expect(screen.getByRole("menu")).toBeTruthy();
  expect(onAncestorKeyDown).not.toHaveBeenCalled();
});

test("Escape closes only the menu when nested in a Dialog; a second Escape then closes the Dialog", async () => {
  const user = userEvent.setup();
  const onDialogClose = vi.fn();
  render(
    <Dialog open onClose={onDialogClose} title="t">
      <Menu trigger="Actions" items={items()} />
    </Dialog>,
  );
  await user.click(screen.getByRole("button", { name: "Actions" }));
  expect(screen.getByRole("menu")).toBeTruthy();

  await user.keyboard("{Escape}");
  expect(screen.queryByRole("menu")).toBeNull();
  expect(onDialogClose).not.toHaveBeenCalled();

  await user.keyboard("{Escape}");
  expect(onDialogClose).toHaveBeenCalledOnce();
});

test("renders a separator between groups and skips it in arrow navigation", async () => {
  const user = userEvent.setup();
  const onA = vi.fn();
  const onB = vi.fn();
  render(
    <Menu
      trigger="open"
      items={[
        { id: "a", label: "A", onSelect: onA },
        { kind: "separator", id: "sep-1" },
        { id: "b", label: "B", onSelect: onB },
      ]}
    />,
  );
  await user.click(screen.getByRole("button", { name: "open" }));
  expect(screen.getByRole("separator")).toBeTruthy();
  // First enabled item is A; one ArrowDown must land on B, never the separator.
  await user.keyboard("{ArrowDown}");
  await user.keyboard("{Enter}");
  expect(onB).toHaveBeenCalledTimes(1);
  expect(onA).not.toHaveBeenCalled();
});

test("End/Home skip separators at the edges", async () => {
  const user = userEvent.setup();
  render(
    <Menu
      trigger="open"
      items={[
        { kind: "separator", id: "sep-top" },
        { id: "a", label: "A", onSelect: () => {} },
        { kind: "separator", id: "sep-bottom" },
      ]}
    />,
  );
  await user.click(screen.getByRole("button", { name: "open" }));
  // A is the ONLY actionable item: Home, End, and ArrowDown all land on it.
  await user.keyboard("{End}");
  expect(screen.getByRole("menuitem", { name: "A" }).tabIndex).toBe(0);
});

// --- popout realm (Dockview) ---------------------------------------------
// A Dockview popout is a separate Window with its own Document and its own
// constructors, so a widget mounted there must resolve THAT document/window,
// never the opener's module-scope globals. jsdom's only real second realm is
// an iframe: createHTMLDocument() shares the opener's constructors, so it
// cannot catch a realm defect. Rendering through a portal into the iframe's
// body makes every element's ownerDocument the popout document.

test("a menu mounted in a popout document portals its panel into that document's body, not the opener's", () => {
  const { frame, doc } = appendPopout();
  const view = render(createPortal(<Menu trigger="Actions" items={items()} />, doc.body));
  try {
    fireEvent.click(doc.querySelector("button")!);
    const panel = doc.querySelector('[role="menu"]');
    expect(panel).not.toBeNull();
    // FocusScope renders the panel's own wrapper div directly into the body.
    expect(panel?.parentElement?.parentElement).toBe(doc.body);
    expect(doc.body.contains(panel)).toBe(true);
    expect(document.body.contains(panel)).toBe(false);
  } finally {
    view.unmount();
    frame.remove();
  }
});

test("a mousedown in the popout document closes a menu opened there", () => {
  const { frame, doc } = appendPopout();
  const view = render(createPortal(<Menu trigger="Actions" items={items()} />, doc.body));
  try {
    fireEvent.click(doc.querySelector("button")!);
    expect(doc.querySelector('[role="menu"]')).not.toBeNull();

    fireEvent.mouseDown(doc);

    expect(doc.querySelector('[role="menu"]')).toBeNull();
  } finally {
    view.unmount();
    frame.remove();
  }
});

test("a scroll on the popout window closes a menu opened there", () => {
  const { frame, doc, win } = appendPopout();
  const view = render(createPortal(<Menu trigger="Actions" items={items()} />, doc.body));
  try {
    fireEvent.click(doc.querySelector("button")!);
    expect(doc.querySelector('[role="menu"]')).not.toBeNull();

    fireEvent.scroll(win);

    expect(doc.querySelector('[role="menu"]')).toBeNull();
  } finally {
    view.unmount();
    frame.remove();
  }
});

// The realm-safe element test: this scroll's target is an iframe-realm
// element, which the opener's `instanceof Element` rejects.
test("a scroll inside the trigger's own popout container closes the menu", () => {
  const { frame, doc } = appendPopout();
  const view = render(
    createPortal(
      <div data-testid="popout-scroller">
        <Menu trigger="Actions" items={items()} />
      </div>,
      doc.body,
    ),
  );
  try {
    fireEvent.click(doc.querySelector("button")!);
    expect(doc.querySelector('[role="menu"]')).not.toBeNull();

    fireEvent.scroll(doc.querySelector('[data-testid="popout-scroller"]')!);

    expect(doc.querySelector('[role="menu"]')).toBeNull();
  } finally {
    view.unmount();
    frame.remove();
  }
});

test("a scroll in an unrelated popout container leaves the menu open", () => {
  const { frame, doc } = appendPopout();
  const view = render(
    createPortal(
      <div>
        <Menu trigger="Actions" items={items()} />
        <div data-testid="popout-unrelated-scroller" />
      </div>,
      doc.body,
    ),
  );
  try {
    fireEvent.click(doc.querySelector("button")!);
    expect(doc.querySelector('[role="menu"]')).not.toBeNull();

    fireEvent.scroll(doc.querySelector('[data-testid="popout-unrelated-scroller"]')!);

    expect(doc.querySelector('[role="menu"]')).not.toBeNull();
  } finally {
    view.unmount();
    frame.remove();
  }
});

// jsdom performs no layout: supplies the untransformed layout box the Menu
// measures its popup by (offsetWidth/offsetHeight) for the UL in `win`.
function stubPopupLayoutBox(win: Window & typeof globalThis, width: number, height: number) {
  const isPopup = (el: HTMLElement) => el.tagName === "UL";
  vi.spyOn(win.HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    return isPopup(this) ? width : 0;
  });
  vi.spyOn(win.HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    return isPopup(this) ? height : 0;
  });
}

// The popup animates in from a scaled-down keyframe in the commit that
// measures it, and a rect reports the box after transforms. Placement must
// come from the untransformed layout box: the two are given different widths
// here so only one placement can result.
test("places the popup from its untransformed layout box, not its mid-animation rect", () => {
  const SETTLED_WIDTH = 200;
  const ANIMATING_WIDTH = 192; // scale(0.96)
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    const box = (left: number, width: number) =>
      ({ left, right: left + width, top: 20, bottom: 40, width, height: 20 }) as DOMRect;
    if (this.tagName === "BUTTON") return box(window.innerWidth - 40, 40);
    if (this.tagName === "UL") return box(0, ANIMATING_WIDTH);
    return box(0, 0);
  });
  stubPopupLayoutBox(window, SETTLED_WIDTH, 100);
  render(<Menu trigger="Actions" items={items()} />);
  fireEvent.click(screen.getByRole("button", { name: "Actions" }));
  // Hard against the right edge the popup right-aligns to the trigger, so its
  // own width lands in the arithmetic.
  expect(screen.getByRole("menu").style.left).toBe(`${window.innerWidth - SETTLED_WIDTH}px`);
});

// Menu placement has always flipped and clamped against a viewport. In a
// popout that has to be the popout's own viewport: a 600x150 window places a
// menu near its bottom-right corner very differently from the opener's.
test("places a popout menu from the popout window's own viewport", () => {
  const { frame, doc, win } = appendPopout();
  Object.defineProperty(win, "innerWidth", { configurable: true, value: 600 });
  Object.defineProperty(win, "innerHeight", { configurable: true, value: 150 });
  const rect = (left: number, top: number, width: number, height: number): DOMRect =>
    ({
      left,
      top,
      right: left + width,
      bottom: top + height,
      width,
      height,
      x: left,
      y: top,
      toJSON: () => ({}),
    }) as DOMRect;
  vi.spyOn(win.HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return rect(450, 20, 140, 125);
    if (this.tagName === "UL") return rect(0, 0, 200, 120);
    return rect(0, 0, 0, 0);
  });
  stubPopupLayoutBox(win, 200, 120);
  const view = render(createPortal(<Menu trigger="Actions" items={items()} />, doc.body));
  try {
    const trigger = doc.querySelector("button");
    if (!trigger) throw new Error("popout trigger missing");
    fireEvent.click(trigger);
    const panel = doc.querySelector<HTMLElement>('[role="menu"]');
    if (!panel) throw new Error("popout menu missing");
    // 450 + 200 overflows 600, so it right-aligns to 590 - 200 = 390; opening
    // below (149 + 120) overflows 150 and above does not fit either, so the
    // clamp pins it to 150 - 120 - 8 = 22.
    expect(panel.style.left).toBe("390px");
    expect(panel.style.top).toBe("22px");
  } finally {
    view.unmount();
    frame.remove();
  }
});

// A menu moved while closed has no observer running. Opening it must resolve
// the new document on that first commit, not after: the panel's own focus
// handling would otherwise run in the document it left.
test("a menu moved while closed opens in its new document", async () => {
  const { frame, doc } = appendPopout();
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  let openerFocus = 0;
  const countFocus = () => {
    openerFocus += 1;
  };
  document.addEventListener("focusin", countFocus, true);
  try {
    act(() => root.render(<Menu trigger="Actions" items={items()} />));
    act(() => {
      doc.body.appendChild(host);
    });
    await act(async () => {});

    const trigger = doc.querySelector("button");
    if (!trigger) throw new Error("popout trigger missing");
    act(() => {
      fireEvent.click(trigger);
    });

    const panel = doc.querySelector('[role="menu"]');
    expect(panel).not.toBeNull();
    expect(document.querySelector('[role="menu"]')).toBeNull();
    expect(openerFocus).toBe(0);
  } finally {
    document.removeEventListener("focusin", countFocus, true);
    act(() => root.unmount());
    frame.remove();
    host.remove();
  }
});
