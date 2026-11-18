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

// jsdom's DOMRect derives the edges from the box, so a scenario states only the
// four numbers it actually cares about and the arithmetic lives in one place.
const box = (left: number, top: number, width: number, height: number): DOMRect =>
  new DOMRect(left, top, width, height);

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

// A scroller emits `scroll` for two reasons, and only one takes the trigger
// away. The reader scrolling it slides a trigger out from under a popup that is
// fixed in the viewport's coordinates. A browser CLAMPING the offset does not:
// make the window taller and the rail's maximum offset drops, so the browser
// pulls scrollTop back to a valid place with no user behind it. A popup asked to
// trust the event rather than the geometry closes on that correction, which is
// the menu vanishing because the window got taller.
test("a taller viewport that clamps an ancestor's scroll leaves the menu open and re-anchored", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 400 });
  let triggerTop = 300;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, triggerTop, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 160, 120);
  const { flush } = captureFrames();
  try {
    render(
      <div data-testid="owning-scroller" style={{ overflowY: "auto" }}>
        <Menu trigger="Actions" items={items()} />
      </div>,
    );
    const scroller = screen.getByTestId("owning-scroller");
    // The rail near its end: 300 of scroll in a 200px box, so 300 is the
    // furthest the reader can be and that is where they left it.
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 500 });
    Object.defineProperty(scroller, "clientHeight", { configurable: true, value: 200 });
    scroller.scrollTop = 300;
    await user.click(screen.getByRole("button", { name: "Actions" }));
    expect(screen.getByRole("menu")).toBeTruthy();

    // The window gets taller and the rail with it, so the content below the
    // offset shrinks: the maximum drops to 200 and 300 is past the end. The
    // browser pulls the offset back to that maximum and the trigger, whose own
    // row did not move, is held in view by the correction.
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
    Object.defineProperty(scroller, "clientHeight", { configurable: true, value: 300 });
    fireEvent(window, new Event("resize"));
    flush();
    const anchoredTop = screen.getByRole("menu").style.top;

    scroller.scrollTop = 200;
    triggerTop = 280;
    fireEvent.scroll(scroller);
    flush();

    // Still open, and following the trigger the correction carried.
    expect(screen.getByRole("menu")).toBeTruthy();
    expect(screen.getByRole("menu").style.top).toBe("312px"); // 280 + 28 + 4
    expect(anchoredTop).toBe("332px");
  } finally {
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// A clamp does not need the WINDOW to grow. The rail's list gets taller when
// its Needs You chip disappears, and `innerHeight` never changes - so a
// dismissal keyed on window growth cannot see it. What can see it is the
// scroller's own numbers: its offset was past the end, and the end moved up
// under it, leaving the offset exactly at the new maximum. That is the browser
// correcting an invalid position, not the reader leaving.
test("a scroller that grows with the window unchanged clamps without closing the menu", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  // The trigger's own row sits inside the list that clamps, so it moves UP by
  // the correction: the content above it shrank, which is what un-stuck the
  // offset in the first place.
  let triggerTop = 400;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, triggerTop, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 160, 120);
  const { flush } = captureFrames();
  try {
    render(
      <div data-testid="owning-scroller" style={{ overflowY: "auto" }}>
        <Menu trigger="Actions" items={items()} />
      </div>,
    );
    const scroller = screen.getByTestId("owning-scroller");
    // The rail at its end: 300 of scroll in a 200px box, so the offset is
    // 300 and that is where the reader left it.
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 500 });
    Object.defineProperty(scroller, "clientHeight", { configurable: true, value: 200 });
    scroller.scrollTop = 300;
    await user.click(screen.getByRole("button", { name: "Actions" }));
    expect(screen.getByRole("menu")).toBeTruthy();

    // The chip above the list disappears and the list grows: 400 of content in
    // the same box, so 300 is past the end and the browser pulls it back to the
    // new maximum of 200. No window resize anywhere.
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 400 });
    triggerTop = 390;
    scroller.scrollTop = 200;
    fireEvent.scroll(scroller);
    flush();

    // Still open. The trigger really did move - a confirmed clamp carries the
    // content, and therefore the trigger - so the popup follows it instead of
    // the menu reading the movement as the reader leaving.
    expect(screen.getByRole("menu")).toBeTruthy();
    expect(screen.getByRole("menu").style.top).toBe("422px"); // 390 + 28 + TRIGGER_GAP
  } finally {
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// A scroller emits `scroll` when a LIVE list grows above the reader too, and
// that one must not take the menu away. A row arriving in the rail's Live
// section moves the pinned section - and the trigger inside it - deeper into
// the rail's content, and the browser answers by moving the offset by the same
// amount, which is what holds the reader's view still. The offset is inside
// the valid range the whole time and no window ever grew, so neither a clamp
// nor a resize explains it. What explains it is the geometry: the offset moved
// by exactly as much as the trigger moved inside the content.
test("a live row arriving above the trigger anchors the scroll and leaves the menu open", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  // The trigger's own screen position: the browser's adjustment exists to hold
  // it still, so it does not move.
  const triggerTop = 300;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, triggerTop, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 160, 120);
  const { flush } = captureFrames();
  try {
    render(
      <div data-testid="owning-scroller" style={{ overflowY: "auto" }}>
        <Menu trigger="Actions" items={items()} />
      </div>,
    );
    const scroller = screen.getByTestId("owning-scroller");
    // The rail scrolled past Live: 300 of 300 available in a 200px box.
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 500 });
    Object.defineProperty(scroller, "clientHeight", { configurable: true, value: 200 });
    scroller.scrollTop = 300;
    await user.click(screen.getByRole("button", { name: "Actions" }));
    expect(screen.getByRole("menu").style.top).toBe("332px"); // 300 + 28 + 4

    // A session enters Live above the visible area. The rail's content grows by
    // the 40px the row takes, and the browser moves the offset by the same 40
    // to keep the pinned section where the reader is looking: 340 of a new
    // 360. Inside the range, with no clamp and no resize anywhere.
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 560 });
    scroller.scrollTop = 340;
    fireEvent.scroll(scroller);
    flush();

    // The trigger never left the reader's screen, so the menu is still theirs -
    // still open, and still sitting on the trigger.
    expect(screen.getByRole("menu")).toBeTruthy();
    expect(screen.getByRole("menu").style.top).toBe("332px");
  } finally {
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// The other side of that reading, and the one that keeps this a fix rather than
// a suppression: a scroll that really does slide the trigger out from under the
// popup still closes the menu. Here the rail's content is untouched and the
// reader scrolled 40px down it, so the trigger ends 40px HIGHER on screen while
// the offset gains exactly those 40 - its place in the content is unmoved, and
// that is what makes the offset change the reader's.
// A live update can move the trigger twice before any scroll is classified, and
// only the SECOND of those movements is the browser's correction. A row
// arriving inside the visible area moves the trigger down while the offset
// stays put; the menu re-anchors around it. A later insertion above the visible
// area then makes the browser move the offset to hold the trigger where it now
// is, and that step has to be judged against the RE-ANCHORED position. Judged
// against the position from before the first insertion, both shifts land in one
// comparison, the offset step looks like a reader's scroll, and the menu closes.
test("a clamp after a live insertion still leaves the menu open and re-anchored", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  // The trigger's screen position, moved by the first insertion and then held
  // still by the browser's correction.
  let triggerTop = 300;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, triggerTop, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 160, 120);
  // The menu follows the first insertion through this observer: no scroll
  // event is emitted for it, which is the whole difficulty.
  class FiringResizeObserver {
    static callback: ResizeObserverCallback | null = null;
    constructor(callback: ResizeObserverCallback) {
      FiringResizeObserver.callback = callback;
    }
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  vi.stubGlobal("ResizeObserver", FiringResizeObserver);
  const { flush } = captureFrames();
  try {
    render(
      <div data-testid="owning-scroller" style={{ overflowY: "auto" }}>
        <Menu trigger="Actions" items={items()} />
      </div>,
    );
    const scroller = screen.getByTestId("owning-scroller");
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 500 });
    Object.defineProperty(scroller, "clientHeight", { configurable: true, value: 200 });
    scroller.scrollTop = 300;
    await user.click(screen.getByRole("button", { name: "Actions" }));
    expect(screen.getByRole("menu").style.top).toBe("332px"); // 300 + 28 + 4

    // A session enters Live INSIDE the visible area: the trigger slides 40
    // down the content, and the offset is untouched. The pane reports a reflow
    // and the popup follows to 340 + 28 + 4.
    triggerTop = 340;
    act(() => {
      FiringResizeObserver.callback?.([], {} as ResizeObserver);
    });
    flush();
    expect(screen.getByRole("menu").style.top).toBe("372px");

    // Now a second session enters ABOVE the visible area. The content grows by
    // 40, and the browser moves the offset by the same 40 so the trigger stays
    // where the reader is looking: 340 of a new 580.
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 580 });
    scroller.scrollTop = 340;
    fireEvent.scroll(scroller);
    flush();

    // The correction is the browser's, not the reader's: the menu is still
    // open and still on its trigger.
    expect(screen.getByRole("menu")).toBeTruthy();
    expect(screen.getByRole("menu").style.top).toBe("372px");
  } finally {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// The other side of that reading, and the one that keeps this a fix rather than
// a suppression: a scroll that really does slide the trigger out from under the
// popup still closes the menu. Here the rail's content is untouched and the
// reader scrolled 40px down it, so the trigger ends 40px HIGHER on screen while
// the offset gains exactly those 40 - its place in the content is unmoved, and
// that is what makes the offset change the reader's.
// One rail refresh can insert ABOVE the row the browser is anchoring on and
// BETWEEN that row and the trigger. The browser then moves the offset by what
// arrived above its anchor, while the trigger - which sits below both
// insertions - moves deeper by the sum of them. Requiring the offset step to
// EQUAL the trigger's movement dismisses exactly this case, even though the
// trigger is still on screen and the content above it did all the moving. The
// offset is a lower bound: it grew, and the content explains at least that much.
test("a refresh that inserts on both sides of the anchor still leaves the menu open", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  let triggerTop = 300;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, triggerTop, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 160, 120);
  const { flush } = captureFrames();
  try {
    render(
      <div data-testid="owning-scroller" style={{ overflowY: "auto" }}>
        <Menu trigger="Actions" items={items()} />
      </div>,
    );
    const scroller = screen.getByTestId("owning-scroller");
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 500 });
    Object.defineProperty(scroller, "clientHeight", { configurable: true, value: 200 });
    scroller.scrollTop = 300;
    await user.click(screen.getByRole("button", { name: "Actions" }));
    expect(screen.getByRole("menu")).toBeTruthy();

    // Live inserts 40px above the browser's anchor, so the offset moves by 40;
    // it also inserts 20px between that anchor and the trigger, so the trigger
    // sits 60 deeper in the content. 340 is inside the range throughout.
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 560 });
    scroller.scrollTop = 340;
    // 600 before, 660 after, against an offset that gained 40: the anchor
    // caught the 40 above it and the extra 20 between it and the trigger moved
    // the trigger with the content.
    triggerTop = 320;
    fireEvent.scroll(scroller);
    flush();

    // Nobody dragged the rail: the trigger did not leave the screen.
    expect(screen.getByRole("menu")).toBeTruthy();
    expect(screen.getByRole("menu").style.top).toBe("352px"); // 320 + 28 + TRIGGER_GAP
  } finally {
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

test("a scroll of the trigger's scroller that moves the trigger still closes the menu", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  let triggerTop = 300;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, triggerTop, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 160, 120);
  const { flush } = captureFrames();
  try {
    render(
      <div data-testid="owning-scroller" style={{ overflowY: "auto" }}>
        <Menu trigger="Actions" items={items()} />
      </div>,
    );
    const scroller = screen.getByTestId("owning-scroller");
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 560 });
    Object.defineProperty(scroller, "clientHeight", { configurable: true, value: 200 });
    scroller.scrollTop = 300;
    await user.click(screen.getByRole("button", { name: "Actions" }));
    expect(screen.getByRole("menu")).toBeTruthy();

    // The reader drags the rail down 40px. Nothing above the trigger changed,
    // so the trigger slides up the screen by those 40.
    scroller.scrollTop = 340;
    triggerTop = 260;
    fireEvent.scroll(scroller);
    flush();

    expect(screen.queryByRole("menu")).toBeNull();
  } finally {
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// The correction can land a fraction short of the endpoint the whole-pixel
// `scrollHeight - clientHeight` describes: layout endpoints are fractional and
// those two properties are integers, so a browser makes 199.5 of a stated 200.
// An exact equality reads that as a reader's scroll and closes the menu while
// its trigger is still on screen. The tolerance must not become a licence,
// though: the offset still has to have been PAST the range it now allows, which
// a reader scrolling to the end never was.
test("a clamp that lands a fraction short of the endpoint still leaves the menu open", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  let triggerTop = 400;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, triggerTop, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 160, 120);
  const { flush } = captureFrames();
  try {
    render(
      <div data-testid="owning-scroller" style={{ overflowY: "auto" }}>
        <Menu trigger="Actions" items={items()} />
      </div>,
    );
    const scroller = screen.getByTestId("owning-scroller");
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 500 });
    Object.defineProperty(scroller, "clientHeight", { configurable: true, value: 200 });
    scroller.scrollTop = 300;
    await user.click(screen.getByRole("button", { name: "Actions" }));
    expect(screen.getByRole("menu")).toBeTruthy();

    // The chip above the list disappears, so 500 of content becomes 400 in the
    // same 200px box: 300 is past the new 200, and the browser lands at 199.5
    // instead of 200 because its own endpoint is fractional.
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 400 });
    scroller.scrollTop = 199.5;
    triggerTop = 390;
    fireEvent.scroll(scroller);
    flush();

    // Still open, and following the trigger the correction moved.
    expect(screen.getByRole("menu")).toBeTruthy();
    expect(screen.getByRole("menu").style.top).toBe("422px"); // 390 + 28 + 4
  } finally {
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// A row that was fully hidden stays fully hidden however it grows. The reader
// focused Overview, then scrolled down to the lower actions with focus still on
// it: all 44px of Overview are above a scrollport starting at 100. A live
// activity count then arrives and wraps its label onto a second line, so it is
// 60px tall - still entirely above the scrollport. A comparison that only counts
// hidden pixels calls that 44px hidden becoming 60px hidden, and scrolls back up
// to a row that was never on screen while the reader was reading Archive.
test("a label that wraps above a fully hidden focused row does not pull the reader back", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  let overviewHeight = 44;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, 300, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 160, 120);
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "UL") return 120;
    if (this.tagName !== "LI" || !this.parentElement) return 0;
    const index = Array.prototype.indexOf.call(this.parentElement.children, this);
    return index === 0 ? overviewHeight : 44;
  });
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    return this.tagName === "UL" ? 160 : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetTop", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName !== "LI" || !this.parentElement) return 0;
    const rows = Array.prototype.slice.call(this.parentElement.children) as HTMLElement[];
    const index = rows.indexOf(this);
    let top = 0;
    for (let i = 0; i < index; i += 1) top += i === 0 ? overviewHeight : 44;
    return top;
  });
  const { flush } = captureFrames();
  // Five 44px rows in a 120px scrollport give 100px of scroll range, so the
  // offset below is one a browser can actually reach. Four would have allowed
  // only 56px, and the test would have exercised a position the fixture's own
  // geometry forbids.
  const five: MenuItem[] = [
    { id: "overview", label: "Overview", onSelect: vi.fn() },
    { id: "archive", label: "Archive", onSelect: vi.fn() },
    { id: "rename", label: "Rename", onSelect: vi.fn() },
    { id: "delete", label: "Delete", onSelect: vi.fn() },
    { id: "pin", label: "Pin", onSelect: vi.fn() },
  ];
  try {
    render(<Menu trigger="Actions" items={five} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    // Focus stays on Overview, at 0..44. The reader scrolls down to 100, which
    // puts all of Overview above the scrollport.
    const first = screen.getByRole("menuitem", { name: "Overview" });
    expect(document.activeElement).toBe(first);
    const popup = screen.getByRole("menu");
    popup.scrollTop = 100;
    expect(popup.scrollTop).toBe(100);

    // The activity count arrives and wraps the Overview label. Overview is 60px
    // now, so it spans -60..0 above the scrollport: still nothing visible.
    overviewHeight = 60;
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 801 });
    fireEvent(window, new Event("resize"));
    flush();

    // The reader stays where they were, reading the lower actions.
    expect(popup.scrollTop).toBe(100);
    expect(document.activeElement).toBe(first);
  } finally {
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// A shrinking scrollport does not hide more of a row that was never visible in
// the first place. The reader focused the last row (132..176), then scrolled to
// the top and left it below: all 44px of it were already hidden. Shortening the
// scrollport from 120 to 100 leaves all 44px hidden - not one pixel of the row
// came back into view, and the space between the row and the scrollport is not
// the row's. Counting that gap makes 44px look like 56px and yanks the reader
// down to a row they deliberately left.
test("an equally hidden row is not revealed when the scrollport shrinks", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  let popupHeight = 120;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, 300, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, popupHeight);
    return box(0, 0, 0, 0);
  });
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "UL") return popupHeight;
    return this.tagName === "LI" ? 44 : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    return this.tagName === "UL" ? 160 : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetTop", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName !== "LI" || !this.parentElement) return 0;
    return Array.prototype.indexOf.call(this.parentElement.children, this) * 44;
  });
  const { flush } = captureFrames();
  const four: MenuItem[] = [
    { id: "rename", label: "Rename", onSelect: vi.fn() },
    { id: "duplicate", label: "Duplicate", onSelect: vi.fn() },
    { id: "archive", label: "Archive", onSelect: vi.fn() },
    { id: "delete", label: "Delete", onSelect: vi.fn() },
  ];
  try {
    render(<Menu trigger="Actions" items={four} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    const last = screen.getByRole("menuitem", { name: "Delete" });
    last.focus();
    expect(document.activeElement).toBe(last);
    const popup = screen.getByRole("menu");
    // The reader goes to the top of the list. Delete spans 132..176, so all of
    // it is below a 120px scrollport: 44 of 44 pixels hidden.
    popup.scrollTop = 0;

    // The window shrinks and the scrollport with it, to 100. Delete is still
    // beyond the end, still entirely hidden.
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 700 });
    popupHeight = 100;
    fireEvent(window, new Event("resize"));
    flush();

    // Not revealed: no pixel of the row changed from visible to hidden.
    expect(popup.scrollTop).toBe(0);
    expect(document.activeElement).toBe(last);
  } finally {
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
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
  const pending = new Map<number, FrameRequestCallback>();
  let nextHandle = 0;
  vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
    nextHandle += 1;
    pending.set(nextHandle, callback);
    return nextHandle;
  });
  // A frame the widget cancels must never run, or a test could not tell
  // "cancelled" from "ran and happened to agree".
  vi.spyOn(window, "cancelAnimationFrame").mockImplementation((handle) => {
    pending.delete(handle);
  });
  return {
    flush: () => {
      act(() => {
        const callbacks = [...pending.values()];
        pending.clear();
        for (const callback of callbacks) callback(0);
      });
    },
  };
}

// A visual viewport can shrink while the LAYOUT viewport does not - desktop
// pinch zoom is the everyday case, and the keyboard inset is the same shape.
// Placement that flips and clamps against innerHeight then uses an area the
// reader cannot see: an 800px layout viewport with a 400px visible one put a
// 120px popup from a trigger ending at 328 down to 452, off the visible area,
// when it fits above the trigger. innerHeight is deliberately NOT changed
// here; only the visual viewport is.
test("a visual viewport smaller than the layout viewport is what the popup flips against", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  const fakeViewport = new EventTarget() as EventTarget & {
    width: number;
    height: number;
    offsetLeft: number;
    offsetTop: number;
  };
  fakeViewport.width = 800;
  fakeViewport.height = 400;
  fakeViewport.offsetLeft = 0;
  fakeViewport.offsetTop = 0;
  vi.stubGlobal("visualViewport", fakeViewport);
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    // The trigger ends at 328: below it a 120px popup would reach 452.
    if (this.tagName === "BUTTON") return box(100, 300, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 160, 120);
  try {
    render(<Menu trigger="Actions" items={items()} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));

    // Only the visible viewport is 400 tall, so 452 is offscreen and the popup
    // flips above: 300 - 4 - 120 = 176. Clamping against innerHeight's 800
    // would have left it at 332 and put the bottom of the popup out of sight.
    expect(screen.getByRole("menu").style.top).toBe("176px");
  } finally {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// Placement is in layout-viewport coordinates, but the visible region can be
// OFFSET inside it (a visual viewport scrolled or zoomed into a corner). Both
// rect and offsets share the layout-viewport pixel space, so the popup has to
// come back out translated, or it lands somewhere the reader is not looking.
test("a scrolled visual viewport places the popup in the reader's visible region", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  const fakeViewport = new EventTarget() as EventTarget & {
    width: number;
    height: number;
    offsetLeft: number;
    offsetTop: number;
  };
  fakeViewport.width = 800;
  fakeViewport.height = 400;
  fakeViewport.offsetLeft = 0;
  fakeViewport.offsetTop = 600;
  vi.stubGlobal("visualViewport", fakeViewport);
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    // The trigger sits NEAR THE BOTTOM of the scrolled-in region (600..1000):
    // below it a 120px popup overflows the visible area, though it would fit
    // in the taller layout viewport.
    if (this.tagName === "BUTTON") return box(100, 950, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 500);
    return box(0, 0, 0, 0);
  });
  // A menu TALLER than the visible region: pinch-zoomed, a long menu cannot
  // fit either side, so placement clamps. That is the only case where the
  // translation does not cancel out - a flip is symmetric, but a clamp pins to
  // the visible edge, and pinning to the layout edge puts it offscreen.
  stubPopupLayoutBox(window, 160, 500);
  try {
    render(<Menu trigger="Actions" items={items()} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));

    // Local: trigger top 350, bottom 378. Neither side fits a 500px popup in a
    // 400px region, so it clamps to the visible top edge (local 8) = 608.
    // Against the layout viewport, flipping "fits" at 446 - 162px too high, in
    // a region the reader is not looking at.
    expect(screen.getByRole("menu").style.top).toBe("608px");
  } finally {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// --- iOS resizes only the VISUAL viewport ----------------------------------
// The mobile shell spends a visual-viewport shrink as `--keyboard-inset`
// padding, which slides the composer and this trigger down. No window
// `resize` fires for it, so a menu listening only on the window stayed at the
// coordinates it measured while the keyboard was still up.
test("a visual-viewport resize re-anchors the menu to the trigger it moved", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  const fakeViewport = new EventTarget() as EventTarget & {
    width: number;
    height: number;
    offsetLeft: number;
    offsetTop: number;
  };
  fakeViewport.width = 400;
  fakeViewport.height = 400;
  fakeViewport.offsetLeft = 0;
  fakeViewport.offsetTop = 0;
  vi.stubGlobal("visualViewport", fakeViewport);
  const { flush } = captureFrames();
  let triggerTop = 300;
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 400 });
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

// The visual viewport can also SCROLL without resizing: iOS moves it under a
// fixed layout as the keyboard's inset settles, which translates the trigger
// relative to the popup and fires no `resize`. The order matters - the frame
// drains before the footer moves, so only a `scroll` listener placed after
// that can bring the popup back to the trigger.
test("a visual-viewport scroll with no resize re-anchors the menu", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  const fakeViewport = new EventTarget() as EventTarget & {
    width: number;
    height: number;
    offsetLeft: number;
    offsetTop: number;
  };
  fakeViewport.width = 400;
  fakeViewport.height = 400;
  fakeViewport.offsetLeft = 0;
  fakeViewport.offsetTop = 0;
  vi.stubGlobal("visualViewport", fakeViewport);
  const { flush } = captureFrames();
  let triggerTop = 300;
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 400 });
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, triggerTop, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 160, 120);
  // No ResizeObserver: this case must work through the visual viewport alone.
  class SilentResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  vi.stubGlobal("ResizeObserver", SilentResizeObserver);
  try {
    render(<Menu trigger="Actions" items={items()} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    expect(screen.getByRole("menu").style.top).toBe("176px"); // flipped above

    // The frame the listener scheduled drains FIRST, with the footer still
    // where it was: it measures the old position and that is all it can know.
    fakeViewport.dispatchEvent(new Event("scroll"));
    flush();
    expect(screen.getByRole("menu").style.top).toBe("176px"); // unchanged

    // Only now does the inset land and the footer slide down - after the
    // frame, with no resize and no window scroll to announce it.
    triggerTop = 500;
    fakeViewport.offsetTop = 100;

    // A second scroll notification is what the settled viewport reports.
    fakeViewport.dispatchEvent(new Event("scroll"));
    flush();

    expect(screen.getByRole("menu")).toBeTruthy();
    // The layout viewport stayed short (this is a scroll, not a resize), so
    // the trigger at 500 no longer fits below it and the popup flips above:
    // 500 - 4 - 120 = 376. The point is that it moved off 176 at all.
    expect(screen.getByRole("menu").style.top).toBe("376px");
  } finally {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// A trigger does not have to be near the visible area to be an honest trigger.
// A desktop rail row sits at its own y inside the rail's scroller, and shrinking
// the window over it moves nothing: the row keeps y=700 with no scroll. There a
// popup 120 tall has no side that fits in a 500px-tall window, and the only
// answer that leaves the reader's keyboard focus onscreen is the clamp. Deciding
// "it fits above" from the near edge alone put the whole popup at y=576 - below
// the window - because its TOP cleared the margin.
test("a trigger the window shrinks past still gets a popup inside the visible region", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  let triggerTop = 300;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, triggerTop, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 160, 120);
  const { flush } = captureFrames();
  try {
    render(<Menu trigger="Actions" items={items()} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    expect(screen.getByRole("menu").style.top).toBe("332px"); // fits below at 800px tall

    // The window shrinks and the rail row stays where it is. No scroll follows:
    // scrolling the rail is not something a window resize does.
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 500 });
    triggerTop = 700;
    fireEvent(window, new Event("resize"));
    flush();

    // Neither side fits a 120px popup (below is 732, above is 576 and would end
    // at 696 in a 500px window), so the clamp wins: 500 - 120 - 8 = 372, on
    // screen and under the keyboard focus.
    expect(screen.getByRole("menu")).toBeTruthy();
    expect(screen.getByRole("menu").style.top).toBe("372px");
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
  // Dockview's settle is a box CHANGE on the trigger here, so this case speaks
  // through the trigger's own notification and nothing else: the popup's height
  // below is read from the trigger's bottom, which moves with it.
  let triggerHeight = 28;
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 400 });
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, triggerTop, 40, triggerHeight);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 160, 120);
  // A ResizeObserver the test fires by hand, the way a browser fires it when
  // the observed trigger's box changes. Keyed by the observed element, so this
  // test can fire the TRIGGER's own notification and no other: firing every
  // callback would also fire the ancestors', and then a widget that never
  // observed the trigger at all would still pass.
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
    // it - detached from the trigger by exactly the distance the trigger then
    // moves.
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
    fireEvent(window, new Event("resize"));
    expect(screen.getByRole("menu").style.top).toBe("332px"); // 300 + 28 + 4

    // The frame the resize scheduled drains FIRST, still on the stale rect. If
    // it were left pending it could run after the trigger moved and place the
    // popup correctly all by itself, and this test would then pass with the
    // trigger never observed at all.
    flush();
    expect(screen.getByRole("menu").style.top).toBe("332px");

    // Dockview finishes: the trigger is at its final position and taller, and
    // its own observed box is what says so. Only the trigger's callback runs
    // here - no window event, and no ancestor notification.
    triggerTop = 500;
    triggerHeight = 60;
    const trigger = screen.getByRole("button", { name: "Actions" });
    act(() => {
      callbacks.get(trigger)?.();
    });
    flush();

    expect(screen.getByRole("menu").style.top).toBe("564px"); // 500 + 60 + 4
  } finally {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// A trigger can move without its box changing, and a fixed-size footer button
// that only MOVES reports no box change at all, so an observer on the trigger
// alone stays silent while it travels. The popup must still land on the
// trigger's settled rect, which is why the resize path re-measures after layout
// as well as observing boxes.
test("a trigger that only moves, with its box unchanged, is still followed after layout", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  let triggerTop = 300;
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 400 });
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    // Same 40x28 box at every position: nothing about its SIZE changes.
    if (this.tagName === "BUTTON") return box(100, triggerTop, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 160, 120);
  // Records what gets observed, so the test can fire only what a pane reflow
  // would fire: a notification about an ANCESTOR of the trigger. Firing the
  // trigger's own callback would not prove the pure-translation case, because
  // a real browser never sends that notification for a fixed-size element.
  const observed = new Map<Element, () => void>();
  class RecordingResizeObserver {
    constructor(private readonly callback: () => void) {}
    observe(target: Element): void {
      observed.set(target, this.callback);
    }
    unobserve(target: Element): void {
      observed.delete(target);
    }
    disconnect(): void {}
  }
  vi.stubGlobal("ResizeObserver", RecordingResizeObserver);
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

    // The frame the resize scheduled runs FIRST, still before the pane has
    // reflowed - Dockview's layout frame was queued from its own observer and
    // can land after this one. Measuring here must not be the last word.
    flush();
    expect(screen.getByRole("menu").style.top).toBe("332px"); // still the old rect

    // The trigger itself must be observed, and so must the boxes around it -
    // the pane is what changes size when the footer only translates.
    const observedTags = [...observed.keys()].map((element) => element.tagName);
    expect(observedTags).toContain("BUTTON");
    expect(observedTags).toContain("DIV");

    // Dockview finishes its layout: the footer moves, its SIZE does not, and
    // no window event fires. Nothing about the trigger changed, so only the
    // pane box around it can report this.
    triggerTop = 500;
    const paneCallbacks = [...observed]
      .filter(([element]) => element.tagName !== "BUTTON" && element.tagName !== "UL")
      .map(([, callback]) => callback);
    expect(paneCallbacks.length).toBeGreaterThan(0);
    act(() => {
      for (const callback of paneCallbacks) callback();
    });
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

// A frame queued by a resize measures once more after layout. If the menu
// closes before that frame runs, the frame must not run at all: the popup
// stays mounted while it leaves (usePresence), so it would re-place itself
// mid-exit and visibly jump away from the position it was last seen at.
// Closing cancels the frame, the exit keeps the last open position, and
// reopening re-measures.
test("a placement frame queued by a resize cannot re-place the popup during its exit", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  const originalGetAnimations = Object.getOwnPropertyDescriptor(Element.prototype, "getAnimations");
  let triggerTop = 300;
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 400 });
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, triggerTop, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 160, 120);
  // jsdom runs no animations, so without this the popup unmounts in the same
  // commit it closes and there is nothing left for a stray frame to move. This
  // stands in a running exit and finishes it by hand.
  let resolveExit: () => void = () => {};
  const exit = {
    playState: "running",
    effect: { getComputedTiming: () => ({ endTime: 80 }) },
    finished: new Promise<void>((resolve) => {
      resolveExit = resolve;
    }),
  } as unknown as Animation;
  Object.defineProperty(Element.prototype, "getAnimations", {
    configurable: true,
    value(this: Element) {
      return this.getAttribute("data-motion") === "closing" ? [exit] : [];
    },
  });
  const { flush } = captureFrames();
  try {
    render(<Menu trigger="Actions" items={items()} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    expect(screen.getByRole("menu").style.top).toBe("176px"); // flipped above

    // A resize places the popup at the rect it can measure right now, and
    // queues a frame to measure again once layout has settled.
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
    fireEvent(window, new Event("resize"));
    expect(screen.getByRole("menu").style.top).toBe("332px"); // 300 + 28 + 4

    await user.keyboard("{Escape}");
    const exiting = document.querySelector('[role="menu"]');
    expect(exiting).not.toBeNull();
    expect(exiting?.getAttribute("data-motion")).toBe("closing");

    // The pane settles after the close: the trigger is now exactly where that
    // queued frame would measure it, so a frame that survives would move the
    // leaving popup to 532.
    triggerTop = 500;
    flush();

    // Still mounted, and still where the reader last saw it.
    expect(document.querySelector('[role="menu"]')).toBe(exiting);
    expect((exiting as HTMLElement).style.top).toBe("332px");

    await act(async () => {
      resolveExit();
    });
    expect(document.querySelector('[role="menu"]')).toBeNull();

    // Reopening measures fresh instead of trusting the exit's stale position.
    await user.click(screen.getByRole("button", { name: "Actions" }));
    expect(screen.getByRole("menu").style.top).toBe("532px"); // 500 + 28 + 4
  } finally {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
    if (originalGetAnimations) Object.defineProperty(Element.prototype, "getAnimations", originalGetAnimations);
    else Reflect.deleteProperty(Element.prototype, "getAnimations");
  }
});

// The other direction, and the one a cap-vs-viewport comparison gets wrong: a
// popup with room does NOT wrap. The cap always reserves both edge margins, so
// `visible.width - 2 * EDGE_MARGIN` is below the viewport width on every
// screen; keying the class on that comparison wrapped every menu on the hub and
// let a long label break in a popup that had space for it. The class turns on
// when the cap decided the popup's width, which the measured width reports.
test("a menu with room for its label keeps the rows on one line", async () => {
  const user = userEvent.setup();
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(20, 300, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 220, 120);
    return box(0, 0, 0, 0);
  });
  // 220px of natural width against a viewport far wider than the cap.
  stubPopupLayoutBox(window, 220, 120);
  try {
    render(
      <Menu
        trigger="Actions"
        items={[{ id: "archive", label: "Archive all ten sessions in this workspace", onSelect: vi.fn() }]}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Actions" }));

    const popup = screen.getByRole("menu");
    expect(popup.style.maxInlineSize).toBe(`${window.innerWidth - 16}px`);
    expect(popup.className).not.toContain("popupNarrow");
    // That predicate reads a measured width, and an out-of-flow box with
    // `width: auto` shrink-to-fits into whatever its offsets leave - so with
    // `auto` the measurement would always report the cap and every menu would
    // wrap. jsdom resolves no CSS-module rules, so the stylesheet states the
    // requirement the predicate rests on.
    const here = dirname(fileURLToPath(import.meta.url));
    const css = readFileSync(join(here, "menu.module.css"), "utf8");
    expect(css).toMatch(/\.popup \{[^}]*width: max-content/);
  } finally {
    vi.restoreAllMocks();
  }
});

// A cap does not have to be narrow to matter. At a 196px visible width the
// popup is capped at 180 - its stylesheet floor exactly - and a long label (the
// session activity count at the largest text setting) is wider than that padded
// row, so it still runs off the scrollport. Wrapping has to follow the cap
// having applied, not the cap being narrower than the floor.
test("a width cap at or above the popup's floor still lets a long label wrap", async () => {
  const user = userEvent.setup();
  const originalInnerWidth = Object.getOwnPropertyDescriptor(window, "innerWidth");
  Object.defineProperty(window, "innerWidth", { configurable: true, value: 800 });
  const fakeViewport = new EventTarget() as EventTarget & {
    width: number;
    height: number;
    offsetLeft: number;
    offsetTop: number;
  };
  fakeViewport.width = 196;
  fakeViewport.height = 800;
  fakeViewport.offsetLeft = 0;
  fakeViewport.offsetTop = 0;
  vi.stubGlobal("visualViewport", fakeViewport);
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(20, 300, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 400, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 400, 120);
  try {
    render(
      <Menu
        trigger="Actions"
        items={[{ id: "archive", label: "Archive all ten sessions in this workspace", onSelect: vi.fn() }]}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Actions" }));

    const popup = screen.getByRole("menu");
    // 196 - 16 = 180: the cap equals the floor, so the floor does not yield -
    // but the popup IS capped, and the row has to be able to wrap.
    expect(popup.style.maxInlineSize).toBe("180px");
    expect(popup.className).toContain("popupNarrow");
  } finally {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    if (originalInnerWidth) Object.defineProperty(window, "innerWidth", originalInnerWidth);
  }
});

// A popup can be TALLER than the area the reader can see. Its stylesheet caps
// it at 100dvh - two edge margins, which is the LAYOUT viewport: after a
// trackpad pinch zoom the visible area can be half that, so the popup is placed
// against the visible top edge and still extends past the visible bottom. Its
// internal scrollport then ends below the visible area too, so the lower
// actions cannot be scrolled to at all - they are offscreen no matter what the
// reader does inside the popup. The cap has to be the visible height.
test("a popup taller than the visible viewport is capped to it so its last action can scroll in", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  // A 900px layout viewport, a 300px visible one: 3x zoom.
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 900 });
  const fakeViewport = new EventTarget() as EventTarget & {
    width: number;
    height: number;
    offsetLeft: number;
    offsetTop: number;
  };
  fakeViewport.width = 800;
  fakeViewport.height = 300;
  fakeViewport.offsetLeft = 0;
  fakeViewport.offsetTop = 0;
  vi.stubGlobal("visualViewport", fakeViewport);
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, 100, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 400);
    return box(0, 0, 0, 0);
  });
  // A 400px popup against a 300px visible viewport. It is a real element whose
  // height and its own cap both come from the widget, so this stubs the natural
  // size only: whatever the widget caps it to is what gets measured.
  stubPopupLayoutBox(window, 160, 400);
  try {
    render(<Menu trigger="Actions" items={items()} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));

    const popup = screen.getByRole("menu");
    // 300 - 2*8: inside the visible area with its own edge margins, which is
    // what makes the bottom of its scrollport reachable.
    expect(popup.style.maxBlockSize).toBe("284px");
  } finally {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// The same zoom can make the visible viewport NARROWER than the popup's own
// 180px minimum width. Height alone is capped, so the oversized popup gets
// pinned to the visible left edge with its labels running off the right, and
// its right edge moves with every pan - there is no position at which the text
// becomes readable. The width has to give instead: cap it to the visible width,
// which lets the min-width yield and the labels wrap.
test("a popup wider than the visible viewport is capped to it so its labels wrap", async () => {
  const user = userEvent.setup();
  const originalInnerWidth = Object.getOwnPropertyDescriptor(window, "innerWidth");
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerWidth", { configurable: true, value: 800 });
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  const fakeViewport = new EventTarget() as EventTarget & {
    width: number;
    height: number;
    offsetLeft: number;
    offsetTop: number;
  };
  // Zoomed in far enough that only 160px of the layout viewport is visible -
  // narrower than the popup's own 180px minimum.
  fakeViewport.width = 160;
  fakeViewport.height = 800;
  fakeViewport.offsetLeft = 0;
  fakeViewport.offsetTop = 0;
  vi.stubGlobal("visualViewport", fakeViewport);
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(20, 300, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 400, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 400, 120);
  try {
    render(<Menu trigger="Actions" items={items()} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));

    const popup = screen.getByRole("menu");
    // 160 - 2*8: inside the visible area with the same edge margins the height
    // cap keeps, and narrow enough that min-width no longer governs.
    expect(popup.style.maxBlockSize).toBe("784px");
    expect(popup.style.maxInlineSize).toBe("144px");
    // The cap only binds if the stylesheet's 180px floor yields to it: max-width
    // cannot beat min-width in CSS, so a popup left at 180 would still be wider
    // than the 160px the reader can see. Both halves are asserted because either
    // one alone leaves the popup off the right edge.
    expect(popup.style.minWidth).toBe("0px"); // jsdom normalises the unit
    // Capping alone would clip the label: `.item` is nowrap, so it runs past
    // the 144px scrollport and reading it needs a horizontal scroll. The row
    // has to wrap instead, which is what makes the cap worth having. jsdom does
    // not resolve CSS-module rules, so the two halves are asserted separately:
    // the popup carries the class, and the module gives that class the rule.
    expect(popup.className).toContain("popupNarrow");
    const here = dirname(fileURLToPath(import.meta.url));
    const css = readFileSync(join(here, "menu.module.css"), "utf8");
    expect(css).toMatch(/\.popupNarrow \.item \{[^}]*white-space: normal/);
  } finally {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    if (originalInnerWidth) Object.defineProperty(window, "innerWidth", originalInnerWidth);
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// Shrinking the window shrinks the popup's scrollport. If the reader is on a
// lower row, that row can end up below the scrollport while still holding
// focus - Enter activates a row whose label and focus ring are out of sight.
// The reveal moves the POPUP's own scroll so the focused row is visible again.
// Rows are 44px tall at 0/44/88; a 60px scrollport showing from 0 ends at 60,
// and Delete's bottom is 132, so the minimal reveal lands at 132 - 60.
test("a shrink that shortens the popup scrolls its focused row back into view", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  let popupHeight = 120;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, 300, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, popupHeight);
    return box(0, 0, 0, 0);
  });
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "UL") return popupHeight;
    return this.tagName === "LI" ? 44 : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    return this.tagName === "UL" ? 160 : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetTop", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName !== "LI" || !this.parentElement) return 0;
    return Array.prototype.indexOf.call(this.parentElement.children, this) * 44;
  });
  const reveal = vi.spyOn(Element.prototype, "scrollIntoView").mockImplementation(() => {});
  const { flush } = captureFrames();
  try {
    render(<Menu trigger="Actions" items={items()} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    // The reader moves down to the last row.
    const last = screen.getByRole("menuitem", { name: "Delete" });
    last.focus();
    expect(document.activeElement).toBe(last);
    const popup = screen.getByRole("menu");
    expect(popup.scrollTop).toBe(0);
    reveal.mockClear();

    // The window shrinks and the popup's scrollport with it. The popup is now
    // short enough that Delete - the row holding focus - is below it.
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 400 });
    popupHeight = 60;
    fireEvent(window, new Event("resize"));
    flush();

    expect(popup.scrollTop).toBe(72);
    // Not scrollIntoView: on a zoomed page it answers the same question in the
    // VISUAL viewport and can pan the page toward where the popup used to be.
    expect(reveal).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(last);
  } finally {
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// A narrower popup wraps its labels, and a wrapped label makes every row
// taller. The popup can then keep its height or even grow while the row the
// reader is on moves below the scrollport, so a height-only guard misses it.
// The cap applying is the trigger here, and the reveal is still the popup's own
// scrollTop.
test("a zoom that narrows the popup wraps labels and keeps the focused row in view", async () => {
  const user = userEvent.setup();
  const originalInnerWidth = Object.getOwnPropertyDescriptor(window, "innerWidth");
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerWidth", { configurable: true, value: 800 });
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  const fakeViewport = new EventTarget() as EventTarget & {
    width: number;
    height: number;
    offsetLeft: number;
    offsetTop: number;
  };
  fakeViewport.width = 800;
  fakeViewport.height = 800;
  fakeViewport.offsetLeft = 0;
  fakeViewport.offsetTop = 0;
  vi.stubGlobal("visualViewport", fakeViewport);
  // A popup with room for its label, then the same popup under a cap that
  // forces the break. The popup stays 120px tall in both cases, so only the cap
  // distinguishes them - and the break is modelled where it really happens, in
  // the ROWS: a label wrapped onto a second line makes a row taller, which is
  // the movement that matters. A fixture that kept every row at 44px would
  // pin nothing but the class name.
  let popupWidth = 220;
  let rowHeight = 44;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(20, 100, 40, 28);
    if (this.tagName === "UL") return box(0, 0, popupWidth, 120);
    return box(0, 0, 0, 0);
  });
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    return this.tagName === "UL" ? popupWidth : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "UL") return 120;
    return this.tagName === "LI" ? rowHeight : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetTop", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName !== "LI" || !this.parentElement) return 0;
    return Array.prototype.indexOf.call(this.parentElement.children, this) * rowHeight;
  });
  const { flush } = captureFrames();
  try {
    render(<Menu trigger="Actions" items={items()} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    const popup = screen.getByRole("menu");
    expect(popup.className).not.toContain("popupNarrow");

    // The reader is on the last row, which is below the 120px scrollport.
    const last = screen.getByRole("menuitem", { name: "Delete" });
    last.focus();
    expect(document.activeElement).toBe(last);
    expect(popup.scrollTop).toBe(0);

    // Pinch zoom: the visible width no longer fits the popup, so the cap now
    // decides its width and the labels wrap: each row takes a second line, so
    // Delete moves from 88..132 to 120..180. The popup's own box height does
    // NOT change.
    fakeViewport.width = 200;
    popupWidth = 184;
    rowHeight = 60;
    fakeViewport.dispatchEvent(new Event("resize"));
    flush();

    expect(popup.className).toContain("popupNarrow");
    // Delete now spans 120..180; a 120px scrollport from 0 ends at 120.
    expect(popup.scrollTop).toBe(60);
    expect(document.activeElement).toBe(last);
  } finally {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    if (originalInnerWidth) Object.defineProperty(window, "innerWidth", originalInnerWidth);
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// The row held focus, the reader scrolled AWAY from it, and then the viewport
// grew. The row is still below the scrollport - the taller scrollport did not
// reach it - so a reveal keyed on "is it clipped" pulls the reader back down to
// a row they had deliberately left. It is the same question the reveal exists
// to ask, answered the other way: layout hid nothing, the reader moved, and
// only the scrollport changed size.
test("a focused row the reader scrolled away from is not pulled back by a taller viewport", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  // Four 44px rows in a 120px scrollport: End focuses the fourth (132..176),
  // which is below the scrollport by 56px.
  let popupHeight = 120;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, 300, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, popupHeight);
    return box(0, 0, 0, 0);
  });
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "UL") return popupHeight;
    return this.tagName === "LI" ? 44 : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    return this.tagName === "UL" ? 160 : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetTop", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName !== "LI" || !this.parentElement) return 0;
    return Array.prototype.indexOf.call(this.parentElement.children, this) * 44;
  });
  const { flush } = captureFrames();
  const four: MenuItem[] = [
    { id: "rename", label: "Rename", onSelect: vi.fn() },
    { id: "duplicate", label: "Duplicate", onSelect: vi.fn() },
    { id: "archive", label: "Archive", onSelect: vi.fn() },
    { id: "delete", label: "Delete", onSelect: vi.fn() },
  ];
  try {
    render(<Menu trigger="Actions" items={four} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    const last = screen.getByRole("menuitem", { name: "Delete" });
    const popup = screen.getByRole("menu");
    // The reader is on the last row, out of sight below the scrollport: 176 is
    // its bottom and a 120px scrollport from 0 ends at 120.
    last.focus();
    expect(document.activeElement).toBe(last);
    // They scroll back up by hand, to the top of the list. Focus stays on
    // Delete, out of sight below, because that is where they left it.
    popup.scrollTop = 0;
    expect(popup.scrollTop).toBe(0);

    // The window is a little taller, and the popup's scrollport with it - still
    // not enough to reach Delete at 132..176.
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 880 });
    popupHeight = 140;
    fireEvent(window, new Event("resize"));
    flush();

    // The reader is not yanked back down: nothing above them moved.
    expect(popup.scrollTop).toBe(0);
    expect(document.activeElement).toBe(last);
  } finally {
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// A zoom can leave the cap fractional - 180.25 for this visual viewport - while
// offsetWidth reports the rounded integer. Comparing them exactly would leave
// wrapping off in the one case that still needs it, so a popup within a pixel
// of its cap counts as capped.
test("a fractional width cap still wraps the labels it forces to the break", async () => {
  const user = userEvent.setup();
  const originalInnerWidth = Object.getOwnPropertyDescriptor(window, "innerWidth");
  Object.defineProperty(window, "innerWidth", { configurable: true, value: 800 });
  const fakeViewport = new EventTarget() as EventTarget & {
    width: number;
    height: number;
    offsetLeft: number;
    offsetTop: number;
  };
  // 196.25 - 16 = 180.25: the cap, while the rounded measurement reports 180.
  fakeViewport.width = 196.25;
  fakeViewport.height = 800;
  fakeViewport.offsetLeft = 0;
  fakeViewport.offsetTop = 0;
  vi.stubGlobal("visualViewport", fakeViewport);
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(20, 300, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 400, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 180, 120);
  try {
    render(
      <Menu
        trigger="Actions"
        items={[{ id: "archive", label: "Archive all ten sessions in this workspace", onSelect: vi.fn() }]}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Actions" }));

    const popup = screen.getByRole("menu");
    expect(popup.style.maxInlineSize).toBe("180.25px");
    expect(popup.className).toContain("popupNarrow");
  } finally {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    if (originalInnerWidth) Object.defineProperty(window, "innerWidth", originalInnerWidth);
  }
});

// The case a cap-flip guard misses: the popup is ALREADY capped and
// already wrapping, and the pane narrows once more. Another line wraps above
// the focused row - which pushes it below the scrollport - while the popup's
// box height stays exactly what it was and `capped` stays true. Only the width
// changing says anything happened.
test("a second narrowing still keeps the focused row in view when the popup is already capped", async () => {
  const user = userEvent.setup();
  const originalInnerWidth = Object.getOwnPropertyDescriptor(window, "innerWidth");
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerWidth", { configurable: true, value: 800 });
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  const fakeViewport = new EventTarget() as EventTarget & {
    width: number;
    height: number;
    offsetLeft: number;
    offsetTop: number;
  };
  fakeViewport.width = 260;
  fakeViewport.height = 800;
  fakeViewport.offsetLeft = 0;
  fakeViewport.offsetTop = 0;
  vi.stubGlobal("visualViewport", fakeViewport);
  // Capped from the start (244 <= 260 - 16). The narrowing below takes it to
  // 204, still capped, still 120px tall.
  let popupWidth = 244;
  // The row grows when wrapping adds a line, so the narrowing below moves
  // Delete's offset inside the popup without changing the popup's box.
  let rowHeight = 44;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(20, 100, 40, 28);
    if (this.tagName === "UL") return box(0, 0, popupWidth, 120);
    return box(0, 0, 0, 0);
  });
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    return this.tagName === "UL" ? popupWidth : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "UL") return 120;
    return this.tagName === "LI" ? rowHeight : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetTop", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName !== "LI" || !this.parentElement) return 0;
    return Array.prototype.indexOf.call(this.parentElement.children, this) * rowHeight;
  });
  const { flush } = captureFrames();
  try {
    render(<Menu trigger="Actions" items={items()} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    const popup = screen.getByRole("menu");
    expect(popup.className).toContain("popupNarrow");

    const last = screen.getByRole("menuitem", { name: "Delete" });
    last.focus();
    expect(document.activeElement).toBe(last);
    expect(popup.scrollTop).toBe(0);

    // Narrowed again: another line wraps above Delete, so its bottom moves
    // from 132 to 176. The popup is no taller, and it was already wrapping.
    rowHeight = 60;
    fakeViewport.width = 220;
    popupWidth = 204;
    fakeViewport.dispatchEvent(new Event("resize"));
    flush();

    expect(popup.className).toContain("popupNarrow");
    // Delete now spans 120..180; a 120px scrollport from 0 ends at 120.
    expect(popup.scrollTop).toBe(60);
    expect(document.activeElement).toBe(last);
  } finally {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    if (originalInnerWidth) Object.defineProperty(window, "innerWidth", originalInnerWidth);
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// A cap tall enough for three 44px rows, and the reader on the third one, which
// is fully visible. Then a row ABOVE it wraps onto a second line: no id changes,
// the popup's own box does not change, and the row holding focus keeps its
// identity - only its geometry moves. A placement pass keyed on a shrinking
// scrollport or a changed item set sees none of that, and the row the person is
// looking at ends up clipped while Enter still invokes it.
function CountingOverviewMenu({ counted }: { counted: boolean }) {
  const list: MenuItem[] = [
    {
      id: "overview",
      label: counted ? "Overview 12 sessions need you" : "Overview",
      onSelect: vi.fn(),
    },
    { id: "rename", label: "Rename", onSelect: vi.fn() },
    { id: "delete", label: "Delete", onSelect: vi.fn() },
  ];
  return <Menu trigger="Actions" items={list} />;
}

test("a label that wraps above the focused row reveals it, without any item or popup size changing", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  // The popup's scrollport is 140px in both cases: its own box never changes.
  const PORT = 140;
  // The first row is 44px, or 88px once its label wraps onto a second line.
  // Later rows sit after it, so a wrap ABOVE them translates them down.
  let overviewHeight = 44;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, 100, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, PORT);
    return box(0, 0, 0, 0);
  });
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "UL") return PORT;
    if (this.tagName !== "LI" || !this.parentElement) return 0;
    return Array.prototype.indexOf.call(this.parentElement.children, this) === 0 ? overviewHeight : 44;
  });
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    return this.tagName === "UL" ? 160 : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetTop", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName !== "LI" || !this.parentElement) return 0;
    const index = Array.prototype.indexOf.call(this.parentElement.children, this);
    return index === 0 ? 0 : overviewHeight + (index - 1) * 44;
  });
  const { flush } = captureFrames();
  try {
    const view = render(<CountingOverviewMenu counted={false} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    // Delete spans 88..132 inside a 140px scrollport: fully visible.
    const last = screen.getByRole("menuitem", { name: "Delete" });
    last.focus();
    expect(document.activeElement).toBe(last);
    const popup = screen.getByRole("menu");
    expect(popup.scrollTop).toBe(0);

    // An activity count arrives on Overview's label and it wraps. Delete moves
    // to 132..176, below the scrollport - and nothing else about the menu
    // changed: same ids, same popup box, same cap.
    overviewHeight = 88;
    view.rerender(<CountingOverviewMenu counted />);
    // The content change is delivered to the observer before the next frame,
    // not inside the render that caused it; drain that delivery, then the frame
    // it queues.
    await act(async () => {});
    flush();

    // Delete's bottom is 176 and the scrollport ends at 140.
    expect(popup.scrollTop).toBe(176 - PORT);
    expect(document.activeElement).toBe(last);
    // Enter still invokes the row the reader is on, which is now in sight.
    await user.keyboard("{Enter}");
    expect(screen.queryByRole("menu")).toBeNull();
  } finally {
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// A list change can push a row that SURVIVED out of the scrollport: a turn
// starting adds Stop and Steer above the inspection group, and the popup's own
// box does not change at all (it is height-capped, so it stays capped at the
// same size). No placement pass sees it, because nothing about the menu's
// geometry moved - only what sits above the row holding focus.
// The mirror of the case above, and the one a hidden-pixel count gets wrong. The
// reader scrolled 50px past the first row, which is 44px tall: all of it is
// hidden. The popup then narrows and that row's label wraps to 60px, so the
// same 50px offset leaves the row's LAST 10px showing. It went from entirely
// out of sight to partly in sight without the reader touching the scroll - and
// a count of hidden pixels calls 50 worse than 44 and yanks them back to the
// top, out of the actions they were reading.
test("a wrap that brings part of a hidden row into view leaves the reader's scroll alone", async () => {
  const user = userEvent.setup();
  const originalInnerWidth = Object.getOwnPropertyDescriptor(window, "innerWidth");
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  const PORT = 120;
  // Four rows; the first is 44px, or 60px once its label wraps.
  let firstHeight = 44;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, 300, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, PORT);
    return box(0, 0, 0, 0);
  });
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "UL") return PORT;
    if (this.tagName !== "LI" || !this.parentElement) return 0;
    return Array.prototype.indexOf.call(this.parentElement.children, this) === 0 ? firstHeight : 44;
  });
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    return this.tagName === "UL" ? 160 : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetTop", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName !== "LI" || !this.parentElement) return 0;
    const index = Array.prototype.indexOf.call(this.parentElement.children, this);
    return index === 0 ? 0 : firstHeight + (index - 1) * 44;
  });
  const { flush } = captureFrames();
  const four: MenuItem[] = [
    { id: "overview", label: "Overview", onSelect: vi.fn() },
    { id: "archive", label: "Archive", onSelect: vi.fn() },
    { id: "rename", label: "Rename", onSelect: vi.fn() },
    { id: "delete", label: "Delete", onSelect: vi.fn() },
  ];
  try {
    render(<Menu trigger="Actions" items={four} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    const first = screen.getByRole("menuitem", { name: "Overview" });
    // Focus stays on Overview, and the reader scrolls to 50: the row spans
    // 0..44, so all 44 of its pixels are above a scrollport starting at 50.
    expect(document.activeElement).toBe(first);
    const popup = screen.getByRole("menu");
    popup.scrollTop = 50;
    expect(popup.scrollTop).toBe(50);

    // The popup narrows and Overview's label wraps. The row is 60px now, so it
    // spans 0..60 and the scrollport at 50 shows its last 10px. Nothing the
    // reader did moved; layout brought part of the row INTO view.
    firstHeight = 60;
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 600 });
    fireEvent(window, new Event("resize"));
    flush();

    // They stay where they were, reading the lower actions.
    expect(popup.scrollTop).toBe(50);
    expect(document.activeElement).toBe(first);
  } finally {
    vi.restoreAllMocks();
    if (originalInnerWidth) Object.defineProperty(window, "innerWidth", originalInnerWidth);
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// A row can go from fully in sight to clipped while SHOWING MORE of itself.
// Archive sits near the bottom of a capped popup: at 44px it fits with room to
// spare. An activity count arrives and its label wraps, so the row is 56px -
// it now shows 52 pixels instead of 44, and 4 pixels of the action the reader's
// Enter would invoke are past the edge. A guard that compares visible pixels
// alone calls that an improvement and says nothing. The row still fits the
// scrollport, so revealing it can put the whole action back on screen.
test("a focused row that wraps until it is clipped is revealed", async () => {
  const user = userEvent.setup();
  const originalInnerWidth = Object.getOwnPropertyDescriptor(window, "innerWidth");
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  const PORT = 56;
  // Archive, the row holding focus, is the second one: 44px, or 56px once its
  // label wraps onto a second line.
  let archiveHeight = 44;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, 300, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, PORT);
    return box(0, 0, 0, 0);
  });
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "UL") return PORT;
    if (this.tagName !== "LI" || !this.parentElement) return 0;
    return Array.prototype.indexOf.call(this.parentElement.children, this) === 1 ? archiveHeight : 44;
  });
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    return this.tagName === "UL" ? 160 : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetTop", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName !== "LI" || !this.parentElement) return 0;
    const index = Array.prototype.indexOf.call(this.parentElement.children, this);
    let top = 0;
    for (let i = 0; i < index; i += 1) top += i === 1 ? archiveHeight : 44;
    return top;
  });
  const { flush } = captureFrames();
  const four: MenuItem[] = [
    { id: "overview", label: "Overview", onSelect: vi.fn() },
    { id: "archive", label: "Archive", onSelect: vi.fn() },
    { id: "rename", label: "Rename", onSelect: vi.fn() },
    { id: "delete", label: "Delete", onSelect: vi.fn() },
  ];
  try {
    render(<Menu trigger="Actions" items={four} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    const focused = screen.getByRole("menuitem", { name: "Archive" });
    focused.focus();
    const popup = screen.getByRole("menu");
    expect(document.activeElement).toBe(focused);
    // The reader scrolls to 40. Archive spans 44..88 in a 56px scrollport from
    // 40, which ends at 96: fully visible, 8px to spare.
    popup.scrollTop = 40;
    expect(popup.scrollTop).toBe(40);

    // The label wraps. Archive is 44..100 at the same offset, so 4px of it now
    // sit past the scrollport - and the pixels showing went UP, 44 to 52.
    archiveHeight = 56;
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 600 });
    fireEvent(window, new Event("resize"));
    flush();

    // Revealed by the least scroll that fits the whole row: 100 - 56.
    expect(popup.scrollTop).toBe(44);
    expect(document.activeElement).toBe(focused);
  } finally {
    vi.restoreAllMocks();
    if (originalInnerWidth) Object.defineProperty(window, "innerWidth", originalInnerWidth);
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// A row that loses height is not a row that got hidden. The reader focused
// Overview and scrolled 35px past its top, leaving 25 of its 60px showing. The
// window widens, the label unwraps back to 44px at the same offset, and the
// same 35px now leaves 9 pixels on screen - fewer, although the row is 16px
// shorter rather than 16px more hidden. Counting visible pixels calls that a
// loss and scrolls back to Overview; the reader's offset is theirs, and the
// scrollport clips no more of the row than it did before.
test("a focused row that unwraps above the scrollport keeps the reader's offset", async () => {
  const user = userEvent.setup();
  const originalInnerWidth = Object.getOwnPropertyDescriptor(window, "innerWidth");
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  const PORT = 120;
  let overviewHeight = 60;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, 300, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, PORT);
    return box(0, 0, 0, 0);
  });
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "UL") return PORT;
    if (this.tagName !== "LI" || !this.parentElement) return 0;
    return Array.prototype.indexOf.call(this.parentElement.children, this) === 0 ? overviewHeight : 44;
  });
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    return this.tagName === "UL" ? 160 : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetTop", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName !== "LI" || !this.parentElement) return 0;
    const index = Array.prototype.indexOf.call(this.parentElement.children, this);
    return index === 0 ? 0 : overviewHeight + (index - 1) * 44;
  });
  const { flush } = captureFrames();
  const four: MenuItem[] = [
    { id: "overview", label: "Overview", onSelect: vi.fn() },
    { id: "archive", label: "Archive", onSelect: vi.fn() },
    { id: "rename", label: "Rename", onSelect: vi.fn() },
    { id: "delete", label: "Delete", onSelect: vi.fn() },
  ];
  try {
    render(<Menu trigger="Actions" items={four} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    const focused = screen.getByRole("menuitem", { name: "Overview" });
    focused.focus();
    const popup = screen.getByRole("menu");
    expect(document.activeElement).toBe(focused);
    // Overview is 0..60 and the reader scrolls to 35, so 25 of its pixels show.
    popup.scrollTop = 35;

    // The window widens and the label unwraps: Overview is 0..44, and the same
    // 35px offset leaves 9 pixels of it on screen.
    overviewHeight = 44;
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 900 });
    fireEvent(window, new Event("resize"));
    flush();

    // Nothing was hidden that was not already: the reader keeps their offset.
    expect(popup.scrollTop).toBe(35);
    expect(document.activeElement).toBe(focused);
  } finally {
    vi.restoreAllMocks();
    if (originalInnerWidth) Object.defineProperty(window, "innerWidth", originalInnerWidth);
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// A popup that loses rows loses scroll range with them, and the browser pulls
// an offset that no longer fits back to the new maximum. The focused row,
// Rename, was already above the scrollport when the reader scrolled to the
// bottom to reach the lower actions; the clamp moves the WHOLE list down, so
// Rename is still entirely hidden. Judged at the clamped offset against the
// frame recorded before it, the row looks as though it had been in sight - and
// the reveal scrolls back to it, taking the actions the reader reached for.
test("a clamp leaves an already hidden focused row hidden", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  const PORT = 120;
  const rowHeight = 44;
  // A leading group sits above Rename - the Stop/Steer block a turn's end takes
  // away - and it is 100px of it that disappears.
  let leadingRows = 0;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, 300, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, PORT);
    return box(0, 0, 0, 0);
  });
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "UL") return PORT;
    return this.tagName === "LI" ? rowHeight : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    return this.tagName === "UL" ? 160 : 0;
  });
  // Every row sits below the leading group, whose disappearance is what shrinks
  // the content and clamps the offset.
  vi.spyOn(HTMLElement.prototype, "offsetTop", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName !== "LI" || !this.parentElement) return 0;
    return leadingRows + Array.prototype.indexOf.call(this.parentElement.children, this) * rowHeight;
  });
  const { flush } = captureFrames();
  const four: MenuItem[] = [
    { id: "rename", label: "Rename", onSelect: vi.fn() },
    { id: "duplicate", label: "Duplicate", onSelect: vi.fn() },
    { id: "archive", label: "Archive", onSelect: vi.fn() },
    { id: "delete", label: "Delete", onSelect: vi.fn() },
  ];
  try {
    render(<Menu trigger="Actions" items={four} />);
    // Four 44px rows under a 300px leading group: 476 of content in a 120px
    // scrollport, so the reader can be at 356 and that is the end of the range.
    // Rename spans 300..344 there, entirely above the scrollport.
    leadingRows = 300;
    await user.click(screen.getByRole("button", { name: "Actions" }));
    const focused = screen.getByRole("menuitem", { name: "Rename" });
    const popup = screen.getByRole("menu");
    Object.defineProperty(popup, "scrollHeight", { configurable: true, value: 476 });
    focused.focus();
    expect(document.activeElement).toBe(focused);
    popup.scrollTop = 356;
    fireEvent.scroll(popup);
    expect(popup.scrollTop).toBe(356);

    // The turn ends, the Stop group above leaves, and the content shrinks by
    // 100. Rename moves up with it to 200..244, the range ends at 256, and the
    // browser clamps the offset to that - so the row is still entirely hidden.
    leadingRows = 200;
    Object.defineProperty(popup, "scrollHeight", { configurable: true, value: 376 });
    popup.scrollTop = 256;
    fireEvent(window, new Event("resize"));
    flush();

    // Still at the clamp, not scrolled back: no pixel of Rename came into view
    // that the reader had not already scrolled past.
    expect(popup.scrollTop).toBe(256);
    expect(document.activeElement).toBe(focused);
  } finally {
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// The row focus lands on at OPEN is a row nobody has chosen yet: FocusScope
// puts focus on it with `preventScroll`, so the browser will not scroll toward
// it - the page must not move on a pinch-zoomed screen. In a capped popup that
// row can already sit below the scrollport before anything on the page has
// changed, because the rows above it fill the whole box. Enter would invoke an
// action the reader has never seen. The reveal belongs to the open, once, and
// it moves the popup's own scroll.
test("the row focus lands on at open is brought into the popup's view", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  const PORT = 56;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, 300, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, PORT);
    return box(0, 0, 0, 0);
  });
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "UL") return PORT;
    return this.tagName === "LI" ? 44 : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    return this.tagName === "UL" ? 160 : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetTop", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName !== "LI" || !this.parentElement) return 0;
    return Array.prototype.indexOf.call(this.parentElement.children, this) * 44;
  });
  const { flush } = captureFrames();
  // Stop and Steer are already disabled - an action is in flight - so FocusScope
  // skips them and lands on Overview, the third row (88..132), while the 56px
  // scrollport shows only 0..56.
  const busy: MenuItem[] = [
    { id: "stop", label: "Stop", onSelect: vi.fn(), disabled: true },
    { id: "steer", label: "Steer", onSelect: vi.fn(), disabled: true },
    { id: "overview", label: "Overview", onSelect: vi.fn() },
    { id: "archive", label: "Archive", onSelect: vi.fn() },
  ];
  try {
    render(<Menu trigger="Actions" items={busy} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    flush();

    const focused = screen.getByRole("menuitem", { name: "Overview" });
    expect(document.activeElement).toBe(focused);
    const popup = screen.getByRole("menu");
    // Revealed by the least scroll that fits it: 132 - 56.
    expect(popup.scrollTop).toBe(76);
    // Enter invokes the row the reader can now see.
    await user.keyboard("{Enter}");
    expect(screen.queryByRole("menu")).toBeNull();
  } finally {
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// A row can wrap taller than the whole scrollport, and then no offset shows all
// of it. The least scroll that fits its END takes its BEGINNING - the label of
// the action the reader's Enter would invoke - off the top. The start is what a
// reader reads first and what the focus ring lands on, so that is what the
// reveal keeps: the row's own top, with the rest reachable by scrolling.
test("a focused row taller than the scrollport keeps its start in view", async () => {
  const user = userEvent.setup();
  const originalInnerWidth = Object.getOwnPropertyDescriptor(window, "innerWidth");
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  const PORT = 56;
  let archiveHeight = 44;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, 300, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, PORT);
    return box(0, 0, 0, 0);
  });
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "UL") return PORT;
    if (this.tagName !== "LI" || !this.parentElement) return 0;
    return Array.prototype.indexOf.call(this.parentElement.children, this) === 1 ? archiveHeight : 44;
  });
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    return this.tagName === "UL" ? 160 : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetTop", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName !== "LI" || !this.parentElement) return 0;
    const index = Array.prototype.indexOf.call(this.parentElement.children, this);
    let top = 0;
    for (let i = 0; i < index; i += 1) top += i === 1 ? archiveHeight : 44;
    return top;
  });
  const { flush } = captureFrames();
  const four: MenuItem[] = [
    { id: "overview", label: "Overview", onSelect: vi.fn() },
    { id: "archive", label: "Archive", onSelect: vi.fn() },
    { id: "rename", label: "Rename", onSelect: vi.fn() },
    { id: "delete", label: "Delete", onSelect: vi.fn() },
  ];
  try {
    render(<Menu trigger="Actions" items={four} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    const focused = screen.getByRole("menuitem", { name: "Archive" });
    focused.focus();
    const popup = screen.getByRole("menu");
    expect(document.activeElement).toBe(focused);
    // Archive spans 44..88 inside a scrollport from 40 to 96: fully in sight.
    popup.scrollTop = 40;
    expect(popup.scrollTop).toBe(40);

    // The label wraps and the row is 100px - taller than the scrollport. Its
    // start is at 44 and its end at 144, so no offset can show it whole.
    archiveHeight = 100;
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 600 });
    fireEvent(window, new Event("resize"));
    flush();

    // The START is on screen. Fitting the end (144 - 56 = 88) would put the
    // label at -44, above the scrollport, so the row's own top wins.
    expect(popup.scrollTop).toBe(44);
    expect(document.activeElement).toBe(focused);
  } finally {
    vi.restoreAllMocks();
    if (originalInnerWidth) Object.defineProperty(window, "innerWidth", originalInnerWidth);
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

test("a row inserted above the focused row leaves it in view", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 400 });
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, 100, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  // Rows are 44px and the popup is a fixed 120px: three rows fit, and it stays
  // exactly that size when two more arrive above them.
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "UL") return 120;
    return this.tagName === "LI" ? 44 : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    return this.tagName === "UL" ? 160 : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetTop", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName !== "LI" || !this.parentElement) return 0;
    return Array.prototype.indexOf.call(this.parentElement.children, this) * 44;
  });
  const { flush } = captureFrames();
  function GrowingMenu({ turning }: { turning: boolean }) {
    const leading: MenuItem[] = turning
      ? [
          { id: "turn-stop", label: "Stop", onSelect: vi.fn() },
          { id: "turn-steer", label: "Steer", onSelect: vi.fn() },
        ]
      : [];
    return <Menu trigger="Actions" items={[...leading, ...items()]} />;
  }
  try {
    const view = render(<GrowingMenu turning={false} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    // Delete is the third row (88..132) and the reader has scrolled it into a
    // 120px scrollport: the least scroll that shows it is 132 - 120.
    const last = screen.getByRole("menuitem", { name: "Delete" });
    last.focus();
    expect(document.activeElement).toBe(last);
    const popup = screen.getByRole("menu");
    popup.scrollTop = 12;
    expect(popup.scrollTop).toBe(12);

    // A turn starts. Stop and Steer arrive ABOVE Delete, so it moves to
    // 176..220 while the popup stays 160x120 and its cap is unchanged.
    view.rerender(<GrowingMenu turning />);
    flush();

    expect(screen.getByRole("menuitem", { name: "Stop" })).toBeTruthy();
    // Delete survived by key and still holds focus - and is back in view.
    expect(document.activeElement).toBe(last);
    expect(popup.scrollTop).toBe(220 - 120);
  } finally {
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// Revealing the focused row is only ever an answer to a scrollport that got
// SHORTER. A reader who scrolls down to the lower actions has deliberately
// moved away from the row that still holds focus, and every other placement
// pass - a pan, a row inserted above, a trigger moving - must leave that scroll
// alone, or the actions they just reached jump back out of reach.
// The same rule holds for the list-change effect: a row arriving BELOW the
// focused row moves nothing the reader can see, so it must not scroll the
// popup back toward the row that still holds focus.
test("a row inserted below the focused row leaves the reader's scroll where they put it", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 400 });
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, 100, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "UL") return 120;
    return this.tagName === "LI" ? 44 : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    return this.tagName === "UL" ? 160 : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetTop", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName !== "LI" || !this.parentElement) return 0;
    return Array.prototype.indexOf.call(this.parentElement.children, this) * 44;
  });
  const { flush } = captureFrames();
  // Five 44px rows in a 120px scrollport give 100px of scroll range, so the
  // 40px offset below is one a browser can actually reach. Three rows would
  // allow only 12px and the test would assert a position its own fixture
  // forbids.
  const five: MenuItem[] = [
    ...items(),
    { id: "archive", label: "Archive", onSelect: vi.fn() },
    { id: "pin", label: "Pin", onSelect: vi.fn() },
  ];
  function GrowingBelow({ trailing }: { trailing: boolean }) {
    const extra: MenuItem[] = trailing ? [{ id: "turn-stop", label: "Stop", onSelect: vi.fn() }] : [];
    return <Menu trigger="Actions" items={[...five, ...extra]} />;
  }
  try {
    const view = render(<GrowingBelow trailing={false} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    // Focus stays on the first row, and the reader scrolls down to the last.
    const first = screen.getByRole("menuitem", { name: "Rename" });
    expect(document.activeElement).toBe(first);
    const popup = screen.getByRole("menu");
    popup.scrollTop = 40;
    expect(popup.scrollTop).toBe(40);

    // A turn starts under the menu: Stop arrives BELOW Delete, so the row
    // holding focus does not move at all.
    view.rerender(<GrowingBelow trailing />);
    flush();

    expect(screen.getByRole("menuitem", { name: "Stop" })).toBeTruthy();
    expect(document.activeElement).toBe(first);
    // Still where the reader put it: nothing above their row moved.
    expect(popup.scrollTop).toBe(40);
  } finally {
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

test("a position-only placement leaves the reader's scroll where they put it", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 400 });
  let triggerTop = 300;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, triggerTop, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 160, 120);
  const reveal = vi.spyOn(Element.prototype, "scrollIntoView").mockImplementation(() => {});
  const { flush } = captureFrames();
  // Five 44px rows in a 120px scrollport, so the 40px offset the reader takes
  // below is inside the 100px the fixture actually allows.
  const rows: MenuItem[] = [
    ...items(),
    { id: "archive", label: "Archive", onSelect: vi.fn() },
    { id: "pin", label: "Pin", onSelect: vi.fn() },
  ];
  try {
    render(<Menu trigger="Actions" items={rows} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    // Focus stays on the first row; the reader scrolls down to the last one.
    expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Rename" }));
    const popup = screen.getByRole("menu");
    popup.scrollTop = 40;
    reveal.mockClear();

    // The trigger moves and the popup follows - the popup's own height does
    // not change, so nothing has been hidden.
    triggerTop = 200;
    fireEvent(window, new Event("resize"));
    flush();

    expect(screen.getByRole("menu").style.top).toBe("232px"); // followed
    expect(popup.scrollTop).toBe(40); // still where the reader put it
    expect(reveal).not.toHaveBeenCalled();
  } finally {
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// A wider popup is not a moved row. An activity count on a label BELOW the
// focused one can widen the popup without wrapping anything: the row holding
// focus keeps its own top and height, and the scrollport is as tall as it was.
// A guard that treats any changed box as movement then scrolls back up to the
// focused row and takes the reader's own place in the list away from them.
test("a width-only change leaves the reader's scroll where they put it", async () => {
  const user = userEvent.setup();
  const originalInnerWidth = Object.getOwnPropertyDescriptor(window, "innerWidth");
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerWidth", { configurable: true, value: 800 });
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  let popupWidth = 160;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(20, 100, 40, 28);
    if (this.tagName === "UL") return box(0, 0, popupWidth, 120);
    return box(0, 0, 0, 0);
  });
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    return this.tagName === "UL" ? popupWidth : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "UL") return 120;
    return this.tagName === "LI" ? 44 : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetTop", "get").mockImplementation(function (this: HTMLElement) {
    if (this.tagName !== "LI" || !this.parentElement) return 0;
    return Array.prototype.indexOf.call(this.parentElement.children, this) * 44;
  });
  const { flush } = captureFrames();
  // Five 44px rows in a 120px scrollport give 100px of scroll range, so the
  // reader's 40px offset below is one the fixture can actually hold. Three
  // rows would allow only 12px, a position a browser would clamp away.
  const rows: MenuItem[] = [
    ...items(),
    { id: "archive", label: "Archive", onSelect: vi.fn() },
    { id: "pin", label: "Pin", onSelect: vi.fn() },
  ];
  try {
    render(<Menu trigger="Actions" items={rows} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    // Focus stays on the first row, and the reader scrolls down to the last.
    const first = screen.getByRole("menuitem", { name: "Rename" });
    expect(document.activeElement).toBe(first);
    const popup = screen.getByRole("menu");
    popup.scrollTop = 40;
    expect(popup.scrollTop).toBe(40);

    // A label further down the list widens the popup. No row's top or height
    // changes, and the scrollport is exactly as tall as it was.
    popupWidth = 200;
    fireEvent(window, new Event("resize"));
    flush();

    // Still where the reader put it: nothing they can see moved.
    expect(popup.scrollTop).toBe(40);
    expect(document.activeElement).toBe(first);
  } finally {
    vi.restoreAllMocks();
    if (originalInnerWidth) Object.defineProperty(window, "innerWidth", originalInnerWidth);
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// The popup grows out of the corner nearest its trigger, so the motion reads as
// coming from the control that opened it. That corner is decided by comparing
// the placed position with the trigger's rect - and with a panned visual
// viewport those two are in DIFFERENT coordinate spaces unless the placed
// position is translated back. Panned 60px down, a popup below its trigger
// looks like it is above it: 260 against a trigger at 300, so it would grow
// from the wrong edge and appear to come out of the trigger's top.
test("a panned visual viewport still grows the popup out of its trigger's edge", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  const fakeViewport = new EventTarget() as EventTarget & {
    width: number;
    height: number;
    offsetLeft: number;
    offsetTop: number;
  };
  fakeViewport.width = 800;
  fakeViewport.height = 800;
  fakeViewport.offsetLeft = 0;
  fakeViewport.offsetTop = 60;
  vi.stubGlobal("visualViewport", fakeViewport);
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, 300, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 160, 120);
  try {
    render(<Menu trigger="Actions" items={items()} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));

    const popup = screen.getByRole("menu");
    // Placed below the trigger in layout coordinates (332), which is 272 in the
    // visible viewport's. Either way it is below 300, so it grows from its top
    // left corner, not its bottom.
    expect(popup.style.top).toBe("332px");
    expect(popup.style.transformOrigin).toBe("left top");
  } finally {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// Watching for a mutation is how a translated trigger reports itself, but the
// box that has to be watched also holds the transcript. Streaming replaces
// transcript DOM continuously, so a record per token would schedule a
// synchronous geometry read per token - for a popup the stream cannot possibly
// have moved. A scroller's content is laid out on its own: it cannot displace a
// trigger outside it, so those records say nothing about placement.
const TRANSCRIPT_TOKENS = ["token 0", "token 1", "token 2"];

function ComposerBesideTranscript({ tokens }: { tokens: string[] }) {
  return (
    <div>
      <h2 data-testid="heading">Pinned sessions</h2>
      <div data-testid="transcript" style={{ overflowY: "auto", height: 400 }}>
        {tokens.map((token) => (
          <p key={token}>{token}</p>
        ))}
      </div>
      <Menu trigger="Actions" items={items()} />
    </div>
  );
}

test("a transcript streaming beside the composer does not re-place the open menu", async () => {
  const user = userEvent.setup();
  const triggerTop = 300;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(100, triggerTop, 40, 28);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, 160, 120);
  // The widget's own mutation callback, captured so the test can hand it the
  // record a streaming token produces. Counting geometry reads would not work:
  // React's own rerender reads rects for reasons that have nothing to do with
  // this widget, so the count moves whether or not the filter is there.
  // The widget registers two observers - one on the trigger's own layout
  // context, one on the popup's content - so the capture keys them by the
  // element they watch rather than by registration order.
  const captured: { byTarget: Map<Element, MutationCallback> } = { byTarget: new Map() };
  class CapturingMutationObserver {
    private callback: MutationCallback;
    constructor(callback: MutationCallback) {
      this.callback = callback;
    }
    observe(target: Element) {
      captured.byTarget.set(target, this.callback);
    }
    disconnect() {}
    takeRecords(): MutationRecord[] {
      return [];
    }
  }
  vi.stubGlobal("MutationObserver", CapturingMutationObserver);
  // What the filter changes is whether a frame is SCHEDULED - re-measuring an
  // unmoved trigger lands on the same position either way, so the position
  // cannot tell the two apart.
  const scheduled: FrameRequestCallback[] = [];
  vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
    scheduled.push(callback);
    return scheduled.length;
  });
  try {
    const { rerender } = render(<ComposerBesideTranscript tokens={TRANSCRIPT_TOKENS.slice(0, 1)} />);
    await user.click(screen.getByRole("button", { name: "Actions" }));
    expect(screen.getByRole("menu").style.top).toBe("332px");
    // The observer on the trigger's own layout context: the outermost ancestor
    // inside the body, which is the one whose filter decides whether a
    // streaming record re-measures the trigger.
    const wrapper = screen.getByRole("button", { name: "Actions" }).parentElement;
    if (wrapper === null) throw new Error("the trigger has no wrapper");
    let layoutContext: Element = wrapper;
    while (layoutContext.parentElement !== null && layoutContext.parentElement !== document.body) {
      layoutContext = layoutContext.parentElement;
    }
    const callback = captured.byTarget.get(layoutContext);
    if (callback === undefined)
      throw new Error("the menu registered no mutation observer for the trigger's layout context");
    const record = (target: Node) => {
      const next = { addedNodes: [], removedNodes: [] } as unknown as MutationRecord;
      Object.defineProperty(next, "target", { value: target });
      return next;
    };
    // The widget coalesces placement into one frame, so a request made while
    // one is already pending never reaches requestAnimationFrame. Running the
    // pending frame is what makes the NEXT case's count mean anything.
    const drain = () => {
      act(() => {
        for (const callback of scheduled.splice(0)) callback(0);
      });
    };
    drain();

    // A token arrives inside the transcript's own scroller, beside the
    // composer. Nothing outside that scroller is laid out by it, so no frame.
    const transcript = screen.getByTestId("transcript");

    act(() => {
      callback([record(transcript)], {} as MutationObserver);
    });
    expect(scheduled.length).toBe(0);

    // A token streamed INTO the transcript is the same shape as the heading
    // below - a text node changing in place - and still must not re-measure,
    // because its scroller is somebody else's. The filter's answer does not
    // depend on the node type, only on where the node sits.
    const token = transcript.firstElementChild?.firstChild ?? null;
    if (token === null) throw new Error("the transcript has no text node");
    act(() => {
      callback([record(token)], {} as MutationObserver);
    });
    expect(scheduled.length).toBe(0);
    drain();

    // A HEADING above the trigger can be renamed in place: another tab's
    // session gets a longer title, the heading wraps, and the trigger moves
    // down with no node inserted, removed or resized anywhere. The record's
    // target is the text node itself, which is why the filter starts its walk
    // at that node's parent - a Text node has no parentElement of its own.
    const heading = screen.getByTestId("heading");
    const text = heading.firstChild;
    if (text === null) throw new Error("the heading has no text node");
    act(() => {
      callback([record(text)], {} as MutationObserver);
    });
    expect(scheduled.length).toBe(1);
    drain();

    // The same callback DOES schedule from a record that could move the
    // trigger: the composer's own wrapper is not inside any other scroller.
    act(() => {
      callback([record(wrapper)], {} as MutationObserver);
    });
    expect(scheduled.length).toBe(1);

    // And streaming never leaves the popup anywhere but its placed position.
    rerender(<ComposerBesideTranscript tokens={TRANSCRIPT_TOKENS.slice(0, 2)} />);
    await act(async () => {});
    expect(screen.getByRole("menu").style.top).toBe("332px");
  } finally {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  }
});

// The record above is handed to the widget's own callback, which proves what
// the FILTER does with a text-node record but not that the observer ever emits
// one. A child-list-only registration emits nothing for a text node whose value
// changes in place, so the heading has to be renamed on a REAL observer and the
// popup has to follow. That is the whole case: a session title elsewhere gets
// longer, the pinned heading above this trigger wraps, and the trigger moves
// down while every observed box keeps its size.
test("a heading renamed in place above the trigger moves the open popup with it", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  let pinnedTop = 300;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON" && this.textContent === "Pinned actions") return box(100, pinnedTop, 40, 28);
    if (this.tagName === "BUTTON") return box(0, 0, 80, 24);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 100, 40);
  });
  stubPopupLayoutBox(window, 160, 120);
  // Every box observer stays silent: the heading's box does not change either,
  // because its wrapper keeps the height the wrapped text needs.
  class SilentResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  vi.stubGlobal("ResizeObserver", SilentResizeObserver);
  const { flush } = captureFrames();
  function RenamedAbove({ title }: { title: string }) {
    return (
      <div>
        <h2 data-testid="heading">{title}</h2>
        <Menu trigger="Pinned actions" items={items()} />
      </div>
    );
  }
  try {
    const { rerender } = render(<RenamedAbove title="Pinned" />);
    // Open and place FIRST, so the popup's position cannot come from measuring
    // a trigger that had already moved.
    await user.click(screen.getByRole("button", { name: "Pinned actions" }));
    expect(screen.getByRole("menu").style.top).toBe("332px"); // 300 + 28 + 4

    // The heading is renamed to a longer title and wraps, pushing the trigger
    // down. The text node's value changes in place - no node is added, removed
    // or resized - and the trigger keeps its own 40x28 box.
    const heading = screen.getByTestId("heading");
    const text = heading.firstChild;
    if (text === null) throw new Error("the heading has no text node");
    pinnedTop = 380;
    act(() => {
      text.nodeValue = "Pinned sessions, all of them";
    });
    rerender(<RenamedAbove title="Pinned sessions, all of them" />);
    // A real observer delivers its records as a microtask after the mutation,
    // and the widget answers them by scheduling a frame. Both steps have to
    // run: the first is what a child-list-only registration never produces.
    await act(async () => {});
    flush();

    expect(screen.getByRole("menu")).toBeTruthy();
    expect(screen.getByRole("menu").style.top).toBe("412px"); // 380 + 28 + 4
  } finally {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// --- a layout change above the trigger moves it without resizing anything ---
// The rail's Live section grows when a session appears inside it, and the pinned
// section below it - the trigger's own section - slides down. Nothing changes
// size: the rail scroller is fixed-height, the pinned section keeps its height,
// the trigger keeps its 40x28 box, and the popup keeps its own. A ResizeObserver
// on any of them stays silent, there is no window resize, and no scroll, so the
// popup used to sit at the coordinates it opened with while its trigger left it
// behind.
// A session arriving in Live is not a press anywhere: it comes from the
// session list, so this grows through a rerender rather than a click. A click
// would be an outside press and close the menu for a reason that has nothing
// to do with placement.
const LIVE_ROWS = [{ id: "live-1", label: "live-1" }];

function PinnedSectionUnderLive({ liveRows }: { liveRows: typeof LIVE_ROWS }) {
  return (
    <div>
      <div data-testid="live">
        {liveRows.map((row) => (
          <div key={row.id} style={{ height: 40 }}>
            {row.label}
          </div>
        ))}
      </div>
      <Menu trigger="Pinned actions" items={items()} />
    </div>
  );
}

test("a row inserted above the trigger moves the open popup with it", async () => {
  const user = userEvent.setup();
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  // The pinned section's own top in the rail scroller; the trigger keeps a
  // fixed 28px height and a fixed 40px width wherever the section puts it.
  let pinnedTop = 300;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON" && this.textContent === "Pinned actions") return box(100, pinnedTop, 40, 28);
    if (this.tagName === "BUTTON") return box(0, 0, 80, 24);
    if (this.tagName === "UL") return box(0, 0, 160, 120);
    return box(0, 0, 100, 40);
  });
  stubPopupLayoutBox(window, 160, 120);
  // Every observer this widget registers stays silent: that is the case. Firing
  // one here would be the fix arranging its own notification, not a pane
  // reflow reporting itself.
  class SilentResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  vi.stubGlobal("ResizeObserver", SilentResizeObserver);
  const { flush } = captureFrames();
  try {
    const { rerender } = render(<PinnedSectionUnderLive liveRows={[]} />);
    // Open and place FIRST. A test that adds the row before opening passes
    // against the unfixed code, because placement then measures the moved
    // trigger at open time.
    await user.click(screen.getByRole("button", { name: "Pinned actions" }));
    expect(screen.getByRole("menu").style.top).toBe("332px"); // 300 + 28 + 4

    // A session enters Live above it, and the pinned section slides down. No
    // window resize, no scroll, and every observed box keeps its size.
    pinnedTop = 380;
    rerender(<PinnedSectionUnderLive liveRows={LIVE_ROWS} />);
    // An observer's records are delivered before the next frame, not inside the
    // render that caused them; drain that delivery, then the frame it queues.
    await act(async () => {});
    flush();

    expect(screen.getByRole("menu")).toBeTruthy();
    expect(screen.getByRole("menu").style.top).toBe("412px"); // 380 + 28 + 4
  } finally {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  }
});

// --- the menu follows its item set, not a snapshot -------------------------
// Opening is not a recovery. Nothing was focused inside a popup that did not
// exist yet, so no row was lost, and the menu must not move focus on the way
// open: it would be focusing a row while the popup still sits at the
// provisional below-trigger position, and a plain focus() pans a pinch-zoomed
// page toward that coordinate before the flipped placement commits. The one
// initial focus move is FocusScope's, and it carries preventScroll for this
// reason. A trigger that takes no focus of its own (the rail's roving rows) and
// a click that does not focus the button both leave <body> active, which is the
// state this guards.
test("opening the menu moves no focus itself, so a zoomed page is not panned to the provisional position", () => {
  const focus = vi.spyOn(HTMLElement.prototype, "focus");
  try {
    render(<Menu trigger="Actions" items={items()} />);
    fireEvent.click(screen.getByRole("button", { name: "Actions" }));

    const rowCalls = focus.mock.calls.filter((_, index) => {
      const target = focus.mock.contexts[index] as HTMLElement;
      return target.getAttribute("role") === "menuitem";
    });
    // FocusScope's single move is the only row focus an open makes, and it is
    // the scrolling-suppressing form. A second call - or one without the
    // option - is the menu's own recovery path running on an open that had no
    // row to lose, which is what pans a pinch-zoomed page.
    expect(rowCalls.map(([options]) => options)).toEqual([{ preventScroll: true }]);
  } finally {
    vi.restoreAllMocks();
  }
});

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

// A disabled row that keeps focus is focused but not TABBABLE, so FocusScope's
// trap - which wraps at the ends of the tabbable list - does not recognize it
// as an edge. Tab then continued from it in DOM order and, when the single tab
// stop sat earlier in the list, left the open menu: Escape from the new target
// no longer reached the popup. Tab from such a row has to land on a menu item.
test("Tab from a row disabled in place stays inside the open menu", async () => {
  const user = userEvent.setup();
  render(<DisablingMenu />);
  await user.click(screen.getByRole("button", { name: "Actions" }));
  await user.keyboard("{ArrowDown}");
  const focusedRow = screen.getByRole("menuitem", { name: "Duplicate" });
  expect(document.activeElement).toBe(focusedRow);

  fireEvent(window, new Event("resize"));
  expect(document.activeElement).toBe(focusedRow); // still the disabled row

  await user.keyboard("{Tab}");

  // Focus is on a menu item, so the trap still contains it and Escape still
  // closes the menu rather than being lost to whatever was outside.
  const menu = screen.getByRole("menu");
  expect(menu.contains(document.activeElement)).toBe(true);
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("menu")).toBeNull();
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
    // Its far edge exactly EDGE_MARGIN inside the viewport: close enough that
    // left-aligning the popup overflows, so the flip is still the answer, and
    // far enough that the right-aligned popup keeps its own margin.
    if (this.tagName === "BUTTON") return box(window.innerWidth - 48, 20, 40, 20);
    if (this.tagName === "UL") return box(0, 20, ANIMATING_WIDTH, 20);
    return box(0, 0, 0, 0);
  });
  stubPopupLayoutBox(window, SETTLED_WIDTH, 100);
  render(<Menu trigger="Actions" items={items()} />);
  fireEvent.click(screen.getByRole("button", { name: "Actions" }));
  // Near the right edge the popup right-aligns to the trigger, so its own width
  // lands in the arithmetic: the settled 200 gives 816, the animating 192 would
  // give 824.
  expect(screen.getByRole("menu").style.left).toBe(`${window.innerWidth - 8 - SETTLED_WIDTH}px`);
});

// Menu placement has always flipped and clamped against a viewport. In a
// popout that has to be the popout's own viewport: a 600x150 window places a
// menu near its bottom-right corner very differently from the opener's.
test("places a popout menu from the popout window's own viewport", () => {
  const { frame, doc, win } = appendPopout();
  Object.defineProperty(win, "innerWidth", { configurable: true, value: 600 });
  Object.defineProperty(win, "innerHeight", { configurable: true, value: 150 });
  vi.spyOn(win.HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.tagName === "BUTTON") return box(450, 20, 140, 125);
    if (this.tagName === "UL") return box(0, 0, 200, 120);
    return box(0, 0, 0, 0);
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
