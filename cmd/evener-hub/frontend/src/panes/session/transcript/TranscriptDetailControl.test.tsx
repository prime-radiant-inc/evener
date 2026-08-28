import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { resetMobileViewportForTests } from "../../../shell/useIsMobile";
import { resetTranscriptDisplayStoreForTests, transcriptDisplayStore } from "../../../stores/transcriptDisplay";
import {
  type HubTranscriptDisplayDefault,
  makeTranscriptDisplayConfig,
  type TranscriptDisplayConfigV1,
} from "../../../transcriptDisplay/config";
import { TranscriptDetailControl } from "./TranscriptDetailControl";

const hubDesktop: HubTranscriptDisplayDefault = {
  revision: 4,
  config: makeTranscriptDisplayConfig({ kind: "preset", level: "tools" }),
};
const hubMobile: HubTranscriptDisplayDefault = {
  revision: 5,
  config: makeTranscriptDisplayConfig({ kind: "preset", level: "intent" }),
};

function mobileQuery(matches: boolean): void {
  const query = {
    matches,
    media: "(max-width: 899px)",
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    dispatchEvent: vi.fn(),
  } satisfies MediaQueryList;
  vi.stubGlobal(
    "matchMedia",
    vi.fn(() => query),
  );
}

function seedStore(overrides: Partial<Parameters<typeof transcriptDisplayStore.setState>[0]> = {}): void {
  transcriptDisplayStore.setState({
    viewport: "desktop",
    local: {},
    hub: { desktop: hubDesktop, mobile: hubMobile },
    drafts: {},
    hubLoading: false,
    hubError: null,
    storageWarning: null,
    hubSupport: "supported",
    ...overrides,
  });
}

function config(level: "chat" | "intent" | "tools" | "activity" | "full"): TranscriptDisplayConfigV1 {
  return makeTranscriptDisplayConfig({ kind: "preset", level });
}

beforeEach(() => {
  resetTranscriptDisplayStoreForTests();
  mobileQuery(false);
  seedStore();
});

afterEach(() => {
  cleanup();
  resetTranscriptDisplayStoreForTests();
  resetMobileViewportForTests();
  vi.unstubAllGlobals();
});

test("renders a desktop Popover with effective compact summary, scope, reset, and hub-default callback", async () => {
  const user = userEvent.setup();
  const onEditHubDefaults = vi.fn();
  render(<TranscriptDetailControl layout="desktop" onEditHubDefaults={onEditHubDefaults} />);

  const trigger = screen.getByRole("button", { name: "Detail: Tools" });
  expect(trigger.className).toMatch(/secondary/);
  expect(trigger.className).toMatch(/sm/);
  expect(trigger.getAttribute("aria-haspopup")).toBe("dialog");
  expect(trigger.getAttribute("aria-expanded")).toBe("false");
  await user.click(trigger);

  expect(trigger.getAttribute("aria-expanded")).toBe("true");
  const dialog = screen.getByRole("dialog", { name: "Transcript display details" });
  expect(dialog.getAttribute("aria-modal")).toBe("false");
  expect(dialog.getAttribute("aria-labelledby")).not.toBeNull();
  expect(screen.getByRole("heading", { name: "Transcript display details" })).toBeTruthy();

  expect(screen.getByRole("radio", { name: "Tools" })).toBeTruthy();
  expect(screen.getByText("Using hub default")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Use hub default" })).toBeNull();

  const editButton = screen.getByRole("button", { name: "Edit hub defaults" });
  expect(editButton.className).toMatch(/quiet/);
  await user.click(screen.getByRole("button", { name: "Edit hub defaults" }));
  expect(onEditHubDefaults).toHaveBeenCalledOnce();
  expect(trigger.getAttribute("aria-expanded")).toBe("false");
});

test("keeps the portaled desktop panel as the container for compact descendants", async () => {
  const user = userEvent.setup();
  render(<TranscriptDetailControl layout="desktop" onEditHubDefaults={() => {}} />);

  await user.click(screen.getByRole("button", { name: "Detail: Tools" }));

  const portalPanel = screen.getByTestId("transcript-detail-popover");
  expect(portalPanel.querySelector('[class*="detailPanel"]')).toBeTruthy();

  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "transcriptDisplay.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  expect(css).toMatch(
    /\.detailPanel\s*\{[^}]*container-type:\s*inline-size[^}]*container-name:\s*transcript-detail-panel/,
  );
  expect(css).not.toMatch(/\.detailPanel,\s*\.detailSheetContent\s*\{/);
  expect(css).not.toMatch(/\.detailSheetContent\s*\{[^}]*container-type/);
  expect(css).toMatch(/@container\s+transcript-detail-panel\s*\([^)]*\)[\s\S]*\.detailPanel\s+\.detailActions/);
  expect(css).not.toMatch(/\.detailSheetContent\s+\.fieldsets/);
});

test("sets and clears only the browser-local value for the selected layout", async () => {
  const user = userEvent.setup();
  render(<TranscriptDetailControl layout="desktop" onEditHubDefaults={() => {}} />);

  await user.click(screen.getByRole("button", { name: "Detail: Tools" }));
  await user.click(screen.getByRole("radio", { name: "Activity" }));

  expect(transcriptDisplayStore.getState().local.desktop).toEqual(config("activity"));
  expect(transcriptDisplayStore.getState().hub.desktop).toEqual(hubDesktop);
  expect(screen.getByText("Local Desktop view")).toBeTruthy();
  const useDefaultButton = screen.getByRole("button", { name: "Use hub default" });
  expect(useDefaultButton.className).toMatch(/secondary/);

  await user.click(screen.getByRole("button", { name: "Use hub default" }));
  expect(transcriptDisplayStore.getState().local.desktop).toBeUndefined();
  expect(transcriptDisplayStore.getState().hub.desktop).toEqual(hubDesktop);
  expect(screen.getByText("Using hub default")).toBeTruthy();
});

test("keeps Custom and Advanced extras truthful in the trigger and editor summary", async () => {
  const user = userEvent.setup();
  const custom = makeTranscriptDisplayConfig(
    { kind: "custom", toolIntent: true, toolCalls: false, reasoning: true, expandByDefault: false },
    { roundTimings: true, systemEvents: true },
  );
  seedStore({ local: { desktop: custom } });
  render(<TranscriptDetailControl layout="desktop" onEditHubDefaults={() => {}} />);

  await user.click(screen.getByRole("button", { name: "Detail: Custom · 2 extras" }));
  expect(screen.getByText("Customize & advanced · Custom content · 2 extras")).toBeTruthy();
  expect(screen.getByText("Local Desktop view")).toBeTruthy();
});

test("shows older-hub/loading/storage status without disabling local editing", async () => {
  const user = userEvent.setup();
  seedStore({ hubSupport: "unsupported", hubLoading: true, storageWarning: "Storage warning" });
  render(<TranscriptDetailControl layout="desktop" onEditHubDefaults={() => {}} />);

  await user.click(screen.getByRole("button", { name: "Detail: Tools" }));
  expect(screen.getByRole("status").textContent).toContain("Loading hub default");
  expect(screen.getByText(/older hub does not support transcript display defaults/)).toBeTruthy();
  expect(screen.getByRole("alert").textContent).toContain("Storage warning");

  const activityRadio = screen.getByRole("radio", { name: "Activity" });
  expect((activityRadio as HTMLButtonElement).disabled).toBe(false);
  await user.click(activityRadio);
  expect(transcriptDisplayStore.getState().local.desktop).toEqual(config("activity"));
});

test("consolidates passive status and failures into one announcement region each", async () => {
  const user = userEvent.setup();
  seedStore({
    hubSupport: "unsupported",
    hubLoading: true,
    hubError: "Hub unavailable",
    storageWarning: "Storage warning",
  });
  render(<TranscriptDetailControl layout="desktop" onEditHubDefaults={() => {}} />);

  await user.click(screen.getByRole("button", { name: "Detail: Tools" }));

  const statuses = screen.getAllByRole("status");
  expect(statuses).toHaveLength(1);
  expect(statuses[0]?.textContent).toContain("Loading hub default");
  expect(statuses[0]?.textContent).toContain("older hub does not support");
  expect(statuses[0]?.textContent).not.toContain("Hub unavailable");

  const alerts = screen.getAllByRole("alert");
  expect(alerts).toHaveLength(1);
  expect(alerts[0]?.textContent).toContain("Hub unavailable");
  expect(alerts[0]?.textContent).toContain("Storage warning");
  expect(screen.getAllByText("Storage warning")).toHaveLength(1);
});

test("deduplicates identical raw hub and storage failures while retaining hub context", async () => {
  const user = userEvent.setup();
  seedStore({ hubError: "Same failure", storageWarning: "Same failure" });
  render(<TranscriptDetailControl layout="desktop" onEditHubDefaults={() => {}} />);

  await user.click(screen.getByRole("button", { name: "Detail: Tools" }));

  const alert = screen.getByRole("alert");
  expect(alert.querySelectorAll("p")).toHaveLength(1);
  expect(alert.textContent).toBe("Hub default status: Same failure");
});

test("does not dismiss the desktop Popover on internal or window scroll", async () => {
  const user = userEvent.setup();
  render(<TranscriptDetailControl layout="desktop" onEditHubDefaults={() => {}} />);

  const trigger = screen.getByRole("button", { name: "Detail: Tools" });
  await user.click(trigger);
  const popover = screen.getByTestId("transcript-detail-popover");
  const panel = popover.querySelector('[class*="detailPanel"]');
  expect(panel).not.toBeNull();

  fireEvent.scroll(panel as HTMLElement);
  window.dispatchEvent(new Event("scroll"));

  expect(trigger.getAttribute("aria-expanded")).toBe("true");
  expect(screen.getByRole("dialog", { name: "Transcript display details" })).toBeTruthy();
});

test("uses a bottom mobile Sheet and restores focus to the forwarded trigger ref", async () => {
  const user = userEvent.setup();
  mobileQuery(true);
  seedStore({ local: { mobile: config("intent") } });
  const triggerRef = { current: null } as React.RefObject<HTMLButtonElement | null>;
  render(<TranscriptDetailControl layout="mobile" onEditHubDefaults={() => {}} triggerRef={triggerRef} />);

  const trigger = screen.getByRole("button", { name: "Detail: Intent" });
  expect(triggerRef.current).toBe(trigger);
  expect(trigger.getAttribute("aria-haspopup")).toBe("dialog");
  expect(trigger.getAttribute("aria-expanded")).toBe("false");
  await user.click(trigger);
  expect(trigger.getAttribute("aria-expanded")).toBe("true");

  const dialog = screen.getByRole("dialog", { name: "Transcript display details" });
  expect(dialog).toBeTruthy();
  expect(dialog.getAttribute("aria-modal")).toBe("true");
  expect(dialog.className).toContain("bottom");
  expect(screen.getByRole("radio", { name: "Intent" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Use hub default" }).className).toMatch(/secondary/);
  expect(screen.getByRole("button", { name: "Edit hub defaults" }).className).toMatch(/quiet/);

  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog", { name: "Transcript display details" })).toBeNull();
  expect(trigger.getAttribute("aria-expanded")).toBe("false");
  expect(document.activeElement).toBe(trigger);
});

test("keeps Desktop focus after reset so Escape closes and restores trigger focus", async () => {
  const user = userEvent.setup();
  seedStore({ local: { desktop: config("activity") } });
  render(<TranscriptDetailControl layout="desktop" onEditHubDefaults={() => {}} />);

  const trigger = screen.getByRole("button", { name: "Detail: Activity" });
  await user.click(trigger);
  const useDefault = screen.getByRole("button", { name: "Use hub default" });
  const edit = screen.getByRole("button", { name: "Edit hub defaults" });
  await user.click(useDefault);

  expect(document.activeElement).toBe(edit);
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog", { name: "Transcript display details" })).toBeNull();
  expect(trigger.getAttribute("aria-expanded")).toBe("false");
  expect(document.activeElement).toBe(trigger);
});

test("keeps Mobile focus after reset so Escape closes and restores trigger focus", async () => {
  const user = userEvent.setup();
  mobileQuery(true);
  seedStore({ local: { mobile: config("activity") } });
  render(<TranscriptDetailControl layout="mobile" onEditHubDefaults={() => {}} />);

  const trigger = screen.getByRole("button", { name: "Detail: Activity" });
  await user.click(trigger);
  const useDefault = screen.getByRole("button", { name: "Use hub default" });
  const edit = screen.getByRole("button", { name: "Edit hub defaults" });
  await user.click(useDefault);

  expect(document.activeElement).toBe(edit);
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog", { name: "Transcript display details" })).toBeNull();
  expect(trigger.getAttribute("aria-expanded")).toBe("false");
  expect(document.activeElement).toBe(trigger);
});

test("sets and resets only Mobile local state when Desktop local state already exists", async () => {
  const user = userEvent.setup();
  mobileQuery(true);
  const desktopLocal = config("tools");
  seedStore({ local: { desktop: desktopLocal } });
  render(<TranscriptDetailControl layout="mobile" onEditHubDefaults={() => {}} />);

  await user.click(screen.getByRole("button", { name: "Detail: Intent" }));
  expect(screen.queryByRole("button", { name: "Use hub default" })).toBeNull();
  expect(screen.getByRole("button", { name: "Edit hub defaults" }).className).toMatch(/quiet/);
  await user.click(screen.getByRole("radio", { name: "Activity" }));
  expect(transcriptDisplayStore.getState().local.mobile).toEqual(config("activity"));
  expect(transcriptDisplayStore.getState().local.desktop).toEqual(desktopLocal);
  expect(transcriptDisplayStore.getState().hub).toEqual({ desktop: hubDesktop, mobile: hubMobile });

  await user.click(screen.getByRole("button", { name: "Use hub default" }));
  expect(transcriptDisplayStore.getState().local.mobile).toBeUndefined();
  expect(transcriptDisplayStore.getState().local.desktop).toEqual(desktopLocal);
  expect(transcriptDisplayStore.getState().hub).toEqual({ desktop: hubDesktop, mobile: hubMobile });
});

test("composes the detail control only from shared Buttons and public widget APIs", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const source = readFileSync(join(here, "TranscriptDetailControl.tsx"), "utf8");

  expect(source).toMatch(/import \{ Button, Popover, Sheet \} from "\.\.\/\.\.\/\.\.\/widgets";/);
  expect(source).not.toMatch(/<button\b/);
  expect(source.match(/<Button\b/g)).toHaveLength(3);
  expect(source.match(/variant="secondary"/g)).toHaveLength(2);
  expect(source).toMatch(/<Button ref=\{editButtonRef\} size="sm" variant="quiet"/);
});

test("CSS keeps live detail layout-only with no private motion or chrome", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "transcriptDisplay.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

  expect(css).toMatch(/\.detailControl\s*\{/);
  expect(css).toMatch(/\.detailPanel\s*\{/);
  const directPanelRules = [...css.matchAll(/(^|\n)(?!\s*@)([^{}]+)\{([^{}]*)\}/gm)].filter((match) =>
    match[2]?.split(",").some((selector) => selector.trim() === ".detailPanel"),
  );
  expect(directPanelRules).toHaveLength(1);
  const panelSelector = directPanelRules[0]?.[2] ?? "";
  expect(panelSelector.trim()).toBe(".detailPanel");
  const panelBody = directPanelRules[0]?.[3] ?? "";
  const panelProperties = [...panelBody.matchAll(/(?:^|;)\s*([a-z-]+)\s*:/g)].map((match) => match[1]).sort();
  expect(panelProperties).toEqual(
    [
      "box-sizing",
      "container-name",
      "container-type",
      "inline-size",
      "max-block-size",
      "overflow-y",
      "padding",
      "scrollbar-gutter",
    ].sort(),
  );
  expect(panelBody).toMatch(/box-sizing:\s*border-box/);
  expect(panelBody).toMatch(/inline-size:\s*min\(42rem, calc\(100vw - var\(--space-8\)\)\)/);
  expect(panelBody).toMatch(/padding:\s*var\(--space-4\)/);
  expect(panelBody).toMatch(/max-block-size:\s*calc\(100dvh - var\(--space-8\)\)/);
  expect(panelBody).toMatch(/overflow-y:\s*auto/);
  expect(panelBody).toMatch(/scrollbar-gutter:\s*stable/);
  expect(panelBody).not.toMatch(/background|box-shadow|border-radius/);
  expect(panelBody).not.toMatch(/(?:^|[;\n])\s*border(?:-[a-z-]+)?\s*:/);
  const statusMatch = /\.detailStatus,\s*\.detailWarning\s*\{([^}]*)\}/.exec(css);
  expect(statusMatch).not.toBeNull();
  const statusBody = statusMatch?.[1] ?? "";
  expect(statusBody).toMatch(/background:\s*var\(--surface-inset\)/);
  expect(statusBody).toMatch(/line-height:\s*var\(--line-height-body\)/);
  expect(css).not.toMatch(/border-inline-start|--accent|--warning/);
  expect(css).not.toMatch(/line-height:\s*1\.(?:45|5)\b/);
  expect(css).not.toMatch(/prefers-reduced-motion/);
  expect(css).toMatch(/@container\s+/);
  expect(css).not.toMatch(/@media\s*\([^)]*width/);
  expect(css).not.toMatch(/\.detailControl\s*\{[^}]*container-type/);
  expect(css).not.toMatch(/\.detailSheetContent\s*\{[^}]*container-(?:type|name)/);
  expect(css).not.toMatch(/\.detailActions\s+button/);
  expect(css).not.toMatch(/\.detailSheetContent\s+\.detailActions/);
  expect(css).not.toMatch(/\.detailTrigger\s*\{/);
});
