import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createPortal } from "react-dom";
import { createRoot } from "react-dom/client";
import { expect, test, vi } from "vitest";
import { Dialog } from "../dialog";
import { appendPopout } from "../internal/popoutTestUtils";
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

test("a viewport resize still closes the menu regardless of scroll semantics", async () => {
  const user = userEvent.setup();
  render(<Menu trigger="Actions" items={items()} />);
  await user.click(screen.getByRole("button", { name: "Actions" }));
  expect(screen.getByRole("menu")).toBeTruthy();

  fireEvent(window, new Event("resize"));

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
    /@media \(max-width: 899px\), \(any-pointer: coarse\)\s*\{[\s\S]*?\.trigger\s*\{[^}]*min-height:\s*var\(--tap-min\)/,
  );
  expect(css).toMatch(
    /@media \(max-width: 899px\), \(any-pointer: coarse\)\s*\{[\s\S]*?\.item\s*\{[^}]*min-height:\s*var\(--tap-min\)/,
  );
});

// jsdom does not evaluate real CSS animations or media queries - see the
// exemplar/dialog pattern this mirrors.
test("the popup's open animation honors prefers-reduced-motion, using only tokens", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "menu.module.css"), "utf8");
  expect(css).toContain("animation:");
  expect(css).toContain("var(--motion-duration-overlay)");
  expect(css).toMatch(/@media \(prefers-reduced-motion: reduce\)/);
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
