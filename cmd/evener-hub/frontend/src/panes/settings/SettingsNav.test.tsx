import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import { readModuleCss } from "../../styles/cssBlock";
import { SettingsNav } from "./SettingsNav";
import { SETTINGS_SECTIONS } from "./sections";

function readCss(): string {
  return readModuleCss(import.meta.url, "settings.module.css");
}

// The mobile rules live in one trailing @media block whose inner rules are
// indented - the closing brace at column 0 is the block's own terminator
// (iconbutton.test.tsx's own media-block extraction idiom).
function mobileBlock(css: string): string {
  const block = /@media\s*\(max-width:\s*899px\)\s*\{([\s\S]*?)\n\}/.exec(css);
  expect(block, "settings.module.css must have a max-width:899px media block").not.toBeNull();
  return block?.[1] ?? "";
}

// FIX 3a (real-browser report): the selected nav item's text used
// var(--accent) - the raw hue, meant for glyphs/borders, not text
// (tokens.css's own "-ink companions" comment) - which read at borderline
// contrast against --accent-bg's own pale tint in the light theme, close
// enough to the neutral grey .link:hover wash that a real user read the
// selected item as barely distinguishable from a merely-hovered one.
// --accent-ink is the AA-derived text form of the same hue (the same
// substitution toast.module.css already makes for text on this exact
// --accent-bg fill).
test("the selected nav item's text uses the AA-derived --accent-ink, not the raw --accent hue", () => {
  const css = readModuleCss(import.meta.url, "settings.module.css");
  const linkActiveRule = /\.linkActive\s*\{[^}]*\}/.exec(css);
  expect(linkActiveRule).not.toBeNull();
  expect(linkActiveRule?.[0]).toMatch(/color:\s*var\(--accent-ink\)/);
});

test("renders a nav landmark labelled Settings sections", () => {
  render(<SettingsNav activeId="theme" onNavigate={vi.fn()} />);
  expect(screen.getByRole("navigation", { name: "Settings sections" })).toBeTruthy();
});

test("navigation activates every destination in the approved group order", async () => {
  const user = userEvent.setup();
  const onNavigate = vi.fn();
  render(<SettingsNav activeId="theme" onNavigate={onNavigate} />);
  for (const button of screen.getAllByRole("button")) await user.click(button);
  expect(onNavigate.mock.calls.map(([id]) => id)).toEqual([
    "theme",
    "transcript",
    "keybindings",
    "notifications",
    "credentials",
    "agents",
    "agents-md",
    "launch-evener",
    "plugins-manager",
    "skills",
    "mcp",
    "hosts",
    "mobile",
    "hub",
    "storage",
    "about",
  ]);
});

function clusterHeaders(container: HTMLElement): (string | null)[] {
  return [...container.querySelectorAll("[data-section]")].map((header) => header.getAttribute("data-section"));
}

test("renders the four task groups", () => {
  const { container } = render(<SettingsNav activeId="theme" onNavigate={vi.fn()} />);
  expect(clusterHeaders(container)).toEqual(["preferences", "agents-models", "extensions", "system"]);
});

test("the active section's link carries aria-current=page; others don't", () => {
  render(<SettingsNav activeId="theme" onNavigate={vi.fn()} />);
  expect(screen.getByRole("button", { name: "Appearance" }).getAttribute("aria-current")).toBe("page");
  expect(screen.getByRole("button", { name: "Storage" }).getAttribute("aria-current")).toBeNull();
});

test("clicking a link calls onNavigate with that section's id", async () => {
  const user = userEvent.setup();
  const onNavigate = vi.fn();
  render(<SettingsNav activeId="theme" onNavigate={onNavigate} />);
  await user.click(screen.getByRole("button", { name: "Hub" }));
  expect(onNavigate).toHaveBeenCalledWith("hub");
});

test("the filter input has an accessible name", () => {
  render(<SettingsNav activeId="theme" onNavigate={vi.fn()} />);
  expect(screen.getByRole("searchbox", { name: "Filter settings" })).toBeTruthy();
});

test.each([
  ["Desktop notifications", "notifications", "preferences"],
  ["Phone density", "theme", "preferences"],
  ["mobile layout", "transcript", "preferences"],
  ["desktop layout", "transcript", "preferences"],
  ["quiet", "notifications", "preferences"],
  ["font", "theme", "preferences"],
  ["width", "theme", "preferences"],
  [" ENTER ", "keybindings", "preferences"],
  ["sound", "notifications", "preferences"],
  ["tokens", "transcript", "preferences"],
  ["marketplace", "plugins-manager", "extensions"],
  ["plugin directory", "plugins-manager", "extensions"],
  ["skill directory", "skills", "extensions"],
  ["cache", "storage", "system"],
  ["pairing", "mobile", "system"],
])("finds %s and navigates to %s", async (query, id, cluster) => {
  const user = userEvent.setup();
  const onNavigate = vi.fn();
  const { container } = render(<SettingsNav activeId={null} onNavigate={onNavigate} />);
  await user.type(screen.getByRole("searchbox"), query);
  expect(clusterHeaders(container)).toEqual([cluster]);
  await user.click(screen.getByRole("button"));
  expect(onNavigate).toHaveBeenCalledExactlyOnceWith(id);
});

test("no matches announces an empty state and clearing restores destination order", async () => {
  const user = userEvent.setup();
  const onNavigate = vi.fn();
  const { container } = render(<SettingsNav activeId={null} onNavigate={onNavigate} />);
  const filter = screen.getByRole("searchbox");
  await user.type(filter, "no-such-control");
  expect(screen.queryAllByRole("button")).toHaveLength(0);
  expect(clusterHeaders(container)).toEqual([]);
  expect(within(screen.getByRole("status")).getByTestId("empty-state").getAttribute("data-reason")).toBe("empty");
  await user.clear(filter);
  expect(screen.queryByRole("status")).toBeNull();
  for (const button of screen.getAllByRole("button")) await user.click(button);
  expect(onNavigate.mock.calls.map(([id]) => id)).toEqual([
    "theme",
    "transcript",
    "keybindings",
    "notifications",
    "credentials",
    "agents",
    "agents-md",
    "launch-evener",
    "plugins-manager",
    "skills",
    "mcp",
    "hosts",
    "mobile",
    "hub",
    "storage",
    "about",
  ]);
});

test("a keyword result supports keyboard activation", async () => {
  const user = userEvent.setup();
  const onNavigate = vi.fn();
  render(<SettingsNav activeId={null} onNavigate={onNavigate} />);
  await user.type(screen.getByRole("searchbox"), "font");
  await user.tab();
  expect(document.activeElement).toBe(screen.getByRole("button"));
  await user.keyboard("{Enter}");
  expect(onNavigate).toHaveBeenCalledExactlyOnceWith("theme");
});

test('filtering "agents" hides Appearance but keeps Agents', async () => {
  const user = userEvent.setup();
  render(<SettingsNav activeId="theme" onNavigate={vi.fn()} />);
  await user.type(screen.getByRole("searchbox", { name: "Filter settings" }), "agents");

  expect(screen.queryByRole("button", { name: "Appearance" })).toBeNull();
  expect(screen.getByRole("button", { name: "Agents" })).toBeTruthy();
});

test("filtering is case-insensitive", async () => {
  const user = userEvent.setup();
  render(<SettingsNav activeId="theme" onNavigate={vi.fn()} />);
  await user.type(screen.getByRole("searchbox", { name: "Filter settings" }), "APPEARANCE");
  expect(screen.getByRole("button", { name: "Appearance" })).toBeTruthy();
});

test("a cluster header hides once every one of its links is filtered out", async () => {
  const user = userEvent.setup();
  const { container } = render(<SettingsNav activeId="theme" onNavigate={vi.fn()} />);
  await user.type(screen.getByRole("searchbox", { name: "Filter settings" }), "storage");

  // Storage matches, so its cluster stays; nothing in the other two matches.
  expect(clusterHeaders(container)).toEqual(["system"]);
});

test("clearing the filter re-shows every link", async () => {
  const user = userEvent.setup();
  render(<SettingsNav activeId="theme" onNavigate={vi.fn()} />);
  const filter = screen.getByRole("searchbox", { name: "Filter settings" });
  await user.type(filter, "storage");
  expect(screen.queryByRole("button", { name: "Appearance" })).toBeNull();

  await user.clear(filter);

  expect(screen.getByRole("button", { name: "Appearance" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Storage" })).toBeTruthy();
});

// --- mobile master list (2026-08-16 settings mobile-nav design) ------------
//
// Below the shared shell's <900px breakpoint the nav IS a full-screen master
// list: full-width 44px rows with a drill-in chevron each, the filter pinned
// to the top of its own scroll, and no "active" row (there is no content
// beside it for a highlight to refer to). jsdom has no layout, so the
// geometry half of this is asserted against the CSS source - the same idiom
// as the FIX 3a contrast test at the top of this file and iconbutton.test.tsx's
// own --tap-min checks.

test("every section link carries a drill-in chevron affordance, decorative only", () => {
  render(<SettingsNav activeId="theme" onNavigate={vi.fn()} />);
  const links = screen.getAllByRole("button");
  expect(links).toHaveLength(SETTINGS_SECTIONS.length);
  for (const link of links) {
    const chevron = within(link).getByTestId("settings-nav-chevron");
    expect(chevron.getAttribute("aria-hidden")).toBe("true");
  }
});

test("a null activeId marks no link current - the mobile list has no active row", () => {
  render(<SettingsNav activeId={null} onNavigate={vi.fn()} />);
  for (const link of screen.getAllByRole("button")) {
    expect(link.getAttribute("aria-current")).toBeNull();
  }
});

test("mobile: the list takes the full width, not the desktop 200px rail", () => {
  const nav = /\.nav\s*\{([^}]*)\}/.exec(mobileBlock(readCss()))?.[1] ?? "";
  expect(nav).toContain("width: 100%");
});

test("mobile: list rows meet the 44px --tap-min touch floor", () => {
  const link = /\.link\s*\{([^}]*)\}/.exec(mobileBlock(readCss()))?.[1] ?? "";
  expect(link).toContain("min-height: var(--tap-min)");
});

test("mobile: the drill-in chevron shows only below the breakpoint", () => {
  const css = readCss();
  const base = /\.linkChevron\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
  expect(base).toContain("display: none");
  const mobile = /\.linkChevron\s*\{([^}]*)\}/.exec(mobileBlock(css))?.[1] ?? "";
  // The `?? ""` makes the negative assertion below vacuous on its own: delete
  // the mobile rule entirely and "" satisfies not.toContain, so this test could
  // not fail on the chevron never showing -- the one thing it is named for.
  // Assert the rule EXISTS first (markdown.module.css's test already does).
  expect(mobile).not.toBe("");
  // The base rule is display:none, so "the mobile block does not re-add
  // display:none" is not the contract -- delete `display: inline-flex` and the
  // chevron stays hidden at every width while that assertion still passes.
  // Showing it is what the test is named for, so assert the showing.
  expect(mobile).toContain("display: inline-flex");
  expect(mobile).not.toContain("display: none");
});

test("mobile: the filter row is pinned to the top of the list's own scroll", () => {
  const filterRow = /\.filterRow\s*\{([^}]*)\}/.exec(mobileBlock(readCss()))?.[1] ?? "";
  expect(filterRow).toContain("position: sticky");
  expect(filterRow).toContain("top: 0");
});

test("mobile: a focused section hides the list without unmounting it (.shellDetail marker)", () => {
  const hidden = /\.shellDetail\s+\.nav\s*\{([^}]*)\}/.exec(mobileBlock(readCss()))?.[1] ?? "";
  expect(hidden).toContain("display: none");
});

test("the in-content mobile back button's styles are gone - back lives in the shell top bar", () => {
  expect(readCss()).not.toMatch(/\.back\s*\{/);
});
