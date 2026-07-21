import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { Dialog } from "../dialog";
import { Menu, type MenuItem } from "./index";

afterEach(cleanup);

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
