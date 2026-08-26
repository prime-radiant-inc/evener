import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
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
  await user.click(trigger);

  expect(screen.getByRole("radio", { name: "Tools" })).toBeTruthy();
  expect(screen.getByText("Using hub default")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Use hub default" })).toBeNull();

  await user.click(screen.getByRole("button", { name: "Edit hub defaults" }));
  expect(onEditHubDefaults).toHaveBeenCalledOnce();
});

test("keeps the portaled desktop panel as the container for compact descendants", async () => {
  const user = userEvent.setup();
  render(<TranscriptDetailControl layout="desktop" onEditHubDefaults={() => {}} />);

  await user.click(screen.getByRole("button", { name: "Detail: Tools" }));

  const portalPanel = screen.getByTestId("transcript-detail-popover");
  expect(portalPanel.querySelector('[class*="detailPanel"]')).toBeTruthy();

  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "transcriptDisplay.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  expect(css).toMatch(/\.detailPanel,\s*\.detailSheetContent\s*\{[^}]*container-type:\s*inline-size/);
  expect(css).toMatch(/@container\s+transcript-detail-panel\s*\([^)]*\)[\s\S]*\.detailPanel\s+\.fieldsets/);
});

test("sets and clears only the browser-local value for the selected layout", async () => {
  const user = userEvent.setup();
  render(<TranscriptDetailControl layout="desktop" onEditHubDefaults={() => {}} />);

  await user.click(screen.getByRole("button", { name: "Detail: Tools" }));
  await user.click(screen.getByRole("radio", { name: "Activity" }));

  expect(transcriptDisplayStore.getState().local.desktop).toEqual(config("activity"));
  expect(transcriptDisplayStore.getState().hub.desktop).toEqual(hubDesktop);
  expect(screen.getByText("Local Desktop view")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Use hub default" })).toBeTruthy();

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

  await user.click(screen.getByRole("button", { name: "Detail: Custom · 2 advanced" }));
  expect(screen.getByRole("button", { name: /Advanced · Custom content · 2 extras/ })).toBeTruthy();
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

test("uses a bottom mobile Sheet and restores focus to the forwarded trigger ref", async () => {
  const user = userEvent.setup();
  mobileQuery(true);
  const triggerRef = { current: null } as React.RefObject<HTMLButtonElement | null>;
  render(<TranscriptDetailControl layout="mobile" onEditHubDefaults={() => {}} triggerRef={triggerRef} />);

  const trigger = screen.getByRole("button", { name: "Detail: Intent" });
  expect(triggerRef.current).toBe(trigger);
  await user.click(trigger);

  const dialog = screen.getByRole("dialog", { name: "Transcript display details" });
  expect(dialog).toBeTruthy();
  expect(dialog.className).toContain("bottom");
  expect(screen.getByRole("radio", { name: "Intent" })).toBeTruthy();

  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog", { name: "Transcript display details" })).toBeNull();
  expect(document.activeElement).toBe(trigger);
});

test("CSS provides wrapper, panel, container compaction, and reduced-motion contracts", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "transcriptDisplay.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

  expect(css).toMatch(/\.detailControl\s*\{/);
  expect(css).toMatch(/\.detailPanel\s*\{/);
  expect(css).toMatch(/@container\s*\(/);
  expect(css).toMatch(/prefers-reduced-motion:\s*reduce/);
  expect(css).not.toMatch(/@media\s*\([^)]*width/);
  expect(css).toMatch(/min-height:\s*44px/);
});
