import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import {
  type HubTranscriptDisplayDefault,
  makeTranscriptDisplayConfig,
  type TranscriptDisplayConfigV1,
} from "@evener/appwire-client";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { resetTranscriptDisplayStoreForTests, transcriptDisplayStore } from "../../../stores/transcriptDisplay";
import { readModuleCss } from "../../../styles/cssBlock";
import { TranscriptDetailControl } from "./TranscriptDetailControl";

const hubDesktop: HubTranscriptDisplayDefault = {
  revision: 4,
  config: makeTranscriptDisplayConfig({ kind: "preset", level: "tools" }),
};
const hubMobile: HubTranscriptDisplayDefault = {
  revision: 5,
  config: makeTranscriptDisplayConfig({ kind: "preset", level: "intent" }),
};

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

function ControlledDetail({
  layout,
  onEditHubDefaults = () => {},
}: {
  layout: "desktop" | "mobile";
  onEditHubDefaults?: () => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        Open verbosity
      </button>
      <TranscriptDetailControl
        open={open}
        onClose={() => setOpen(false)}
        layout={layout}
        onEditHubDefaults={onEditHubDefaults}
      />
    </>
  );
}

// Whose configuration the control is showing: "local" or "hub".
function detailScope(): string | null {
  return screen.getByTestId("transcript-detail-scope").getAttribute("data-scope");
}

function statusKinds(region: HTMLElement): (string | null)[] {
  return [...region.querySelectorAll("[data-kind]")].map((message) => message.getAttribute("data-kind"));
}

beforeEach(() => {
  resetTranscriptDisplayStoreForTests();
  seedStore();
});

afterEach(() => {
  cleanup();
  resetTranscriptDisplayStoreForTests();
});

test("renders a controlled desktop Dialog with full editor, scope, reset, and hub-default callback", async () => {
  const user = userEvent.setup();
  const onEditHubDefaults = vi.fn();
  render(<TranscriptDetailControl open onClose={() => {}} layout="desktop" onEditHubDefaults={onEditHubDefaults} />);

  const dialog = screen.getByRole("dialog", { name: "Verbosity" });
  expect(dialog.getAttribute("aria-modal")).toBe("true");
  expect(dialog.getAttribute("aria-labelledby")).not.toBeNull();
  expect(screen.getByRole("heading", { name: "Verbosity" })).toBeTruthy();

  expect(screen.getByRole("radio", { name: "Tools" })).toBeTruthy();
  expect(detailScope()).toBe("hub");
  expect(screen.queryByRole("button", { name: "Use hub default" })).toBeNull();

  const editButton = screen.getByRole("button", { name: "Edit hub defaults" });
  expect(editButton.className).toMatch(/quiet/);
  await user.click(screen.getByRole("button", { name: "Edit hub defaults" }));
  expect(onEditHubDefaults).toHaveBeenCalledOnce();
});

test("keeps the desktop Dialog body as the container for compact descendants", () => {
  render(<TranscriptDetailControl open onClose={() => {}} layout="desktop" onEditHubDefaults={() => {}} />);

  const dialog = screen.getByRole("dialog", { name: "Verbosity" });
  expect(dialog.querySelector('[class*="detailPanel"]')).toBeTruthy();

  const css = readModuleCss(import.meta.url, "transcriptDisplay.module.css");
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
  render(<TranscriptDetailControl open onClose={() => {}} layout="desktop" onEditHubDefaults={() => {}} />);
  await user.click(screen.getByRole("radio", { name: "Activity" }));

  expect(transcriptDisplayStore.getState().local.desktop).toEqual(config("activity"));
  expect(transcriptDisplayStore.getState().hub.desktop).toEqual(hubDesktop);
  expect(detailScope()).toBe("local");
  const useDefaultButton = screen.getByRole("button", { name: "Use hub default" });
  expect(useDefaultButton.className).toMatch(/secondary/);

  await user.click(screen.getByRole("button", { name: "Use hub default" }));
  expect(transcriptDisplayStore.getState().local.desktop).toBeUndefined();
  expect(transcriptDisplayStore.getState().hub.desktop).toEqual(hubDesktop);
  expect(detailScope()).toBe("hub");
});

test("keeps Custom and Advanced extras truthful in the editor summary", () => {
  const custom = makeTranscriptDisplayConfig(
    { kind: "custom", toolIntent: true, toolCalls: false, reasoning: true, expandByDefault: false },
    { roundTimings: true, systemEvents: true },
  );
  seedStore({ local: { desktop: custom } });
  render(<TranscriptDetailControl open onClose={() => {}} layout="desktop" onEditHubDefaults={() => {}} />);
  const summary = screen.getByTestId("transcript-detail-summary");
  expect(summary.getAttribute("data-content")).toBe("custom");
  expect(summary.getAttribute("data-extras")).toBe("2");
  expect(detailScope()).toBe("local");
});

test("shows older-hub/loading/storage status without disabling local editing", async () => {
  const user = userEvent.setup();
  seedStore({ hubSupport: "unsupported", hubLoading: true, storageWarning: "Storage warning" });
  render(<TranscriptDetailControl open onClose={() => {}} layout="desktop" onEditHubDefaults={() => {}} />);

  expect(statusKinds(screen.getByRole("status"))).toEqual(["hub-loading", "hub-unsupported"]);
  expect(screen.getByRole("alert").textContent).toContain("Storage warning");

  const activityRadio = screen.getByRole("radio", { name: "Activity" });
  expect((activityRadio as HTMLButtonElement).disabled).toBe(false);
  await user.click(activityRadio);
  expect(transcriptDisplayStore.getState().local.desktop).toEqual(config("activity"));
});

test("consolidates passive status and failures into one announcement region each", async () => {
  seedStore({
    hubSupport: "unsupported",
    hubLoading: true,
    hubError: "Hub unavailable",
    storageWarning: "Storage warning",
  });
  render(<TranscriptDetailControl open onClose={() => {}} layout="desktop" onEditHubDefaults={() => {}} />);

  const statuses = screen.getAllByRole("status");
  expect(statuses).toHaveLength(1);
  expect(statusKinds(statuses[0] as HTMLElement)).toEqual(["hub-loading", "hub-unsupported"]);
  expect(statuses[0]?.textContent).not.toContain("Hub unavailable");

  const alerts = screen.getAllByRole("alert");
  expect(alerts).toHaveLength(1);
  expect(alerts[0]?.textContent).toContain("Hub unavailable");
  expect(alerts[0]?.textContent).toContain("Storage warning");
  expect(screen.getAllByText("Storage warning")).toHaveLength(1);
});

test("deduplicates identical raw hub and storage failures while retaining hub context", async () => {
  seedStore({ hubError: "Same failure", storageWarning: "Same failure" });
  render(<TranscriptDetailControl open onClose={() => {}} layout="desktop" onEditHubDefaults={() => {}} />);

  const alert = screen.getByRole("alert");
  expect(alert.querySelectorAll("p")).toHaveLength(1);
  expect(alert.textContent).toBe("Hub default status: Same failure");
});

test("controlled close requests dismissal without owning open state", async () => {
  const user = userEvent.setup();
  const onClose = vi.fn();
  render(<TranscriptDetailControl open onClose={onClose} layout="desktop" onEditHubDefaults={() => {}} />);

  await user.keyboard("{Escape}");
  expect(onClose).toHaveBeenCalledOnce();
  expect(screen.getByRole("dialog", { name: "Verbosity" })).toBeTruthy();
});

test("uses a controlled bottom mobile Sheet and restores focus to the external trigger", async () => {
  const user = userEvent.setup();
  seedStore({ local: { mobile: config("intent") } });
  render(<ControlledDetail layout="mobile" />);

  const trigger = screen.getByRole("button", { name: "Open verbosity" });
  await user.click(trigger);

  const dialog = screen.getByRole("dialog", { name: "Verbosity" });
  expect(dialog).toBeTruthy();
  expect(dialog.getAttribute("aria-modal")).toBe("true");
  expect(dialog.className).toContain("bottom");
  expect(screen.getByRole("radio", { name: "Intent" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Use hub default" }).className).toMatch(/secondary/);
  expect(screen.getByRole("button", { name: "Edit hub defaults" }).className).toMatch(/quiet/);

  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog", { name: "Verbosity" })).toBeNull();
  expect(document.activeElement).toBe(trigger);
});

test("keeps Desktop focus after reset so Escape closes and restores external trigger focus", async () => {
  const user = userEvent.setup();
  seedStore({ local: { desktop: config("activity") } });
  render(<ControlledDetail layout="desktop" />);

  const trigger = screen.getByRole("button", { name: "Open verbosity" });
  await user.click(trigger);
  const useDefault = screen.getByRole("button", { name: "Use hub default" });
  const edit = screen.getByRole("button", { name: "Edit hub defaults" });
  await user.click(useDefault);

  expect(document.activeElement).toBe(edit);
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog", { name: "Verbosity" })).toBeNull();
  expect(document.activeElement).toBe(trigger);
});

test("keeps Mobile focus after reset so Escape closes and restores external trigger focus", async () => {
  const user = userEvent.setup();
  seedStore({ local: { mobile: config("activity") } });
  render(<ControlledDetail layout="mobile" />);

  const trigger = screen.getByRole("button", { name: "Open verbosity" });
  await user.click(trigger);
  const useDefault = screen.getByRole("button", { name: "Use hub default" });
  const edit = screen.getByRole("button", { name: "Edit hub defaults" });
  await user.click(useDefault);

  expect(document.activeElement).toBe(edit);
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog", { name: "Verbosity" })).toBeNull();
  expect(document.activeElement).toBe(trigger);
});

test("sets and resets only Mobile local state when Desktop local state already exists", async () => {
  const user = userEvent.setup();
  const desktopLocal = config("tools");
  seedStore({ local: { desktop: desktopLocal } });
  render(<TranscriptDetailControl open onClose={() => {}} layout="mobile" onEditHubDefaults={() => {}} />);

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

test.each(["desktop", "mobile"] as const)(
  "%s presentation selection does not create a content override",
  async (layout) => {
    const user = userEvent.setup();
    render(<TranscriptDetailControl open onClose={() => {}} layout={layout} onEditHubDefaults={() => {}} />);
    const presentation = screen.getByRole("radiogroup", { name: "Presentation" });
    const standard = within(presentation).getByRole("radio", { name: "Standard" });
    const comic = within(presentation).getByRole("radio", { name: "Comic" });
    expect(standard.getAttribute("aria-checked")).toBe("true");
    const before = transcriptDisplayStore.getState();
    await user.click(comic);
    expect(comic.getAttribute("aria-checked")).toBe("true");
    expect(transcriptDisplayStore.getState().presentation[layout]).toBe("comic");
    expect(transcriptDisplayStore.getState().presentation[layout === "desktop" ? "mobile" : "desktop"]).toBe(
      "standard",
    );
    expect(transcriptDisplayStore.getState().local).toBe(before.local);
    expect(transcriptDisplayStore.getState().hub).toBe(before.hub);
    expect(transcriptDisplayStore.getState().drafts).toBe(before.drafts);
    expect(detailScope()).toBe("hub");
    expect(screen.queryByRole("button", { name: "Use hub default" })).toBeNull();
    await user.click(standard);
    expect(transcriptDisplayStore.getState().presentation[layout]).toBe("standard");
    expect(transcriptDisplayStore.getState().local).toBe(before.local);
  },
);

test.each(["desktop", "mobile"] as const)(
  "%s keeps the same standard editor mounted, disabled in Comic, and restores advanced state",
  async (layout) => {
    const user = userEvent.setup();
    const custom = makeTranscriptDisplayConfig(
      { kind: "custom", toolIntent: false, toolCalls: true, reasoning: true, expandByDefault: true },
      { roundTimings: true, systemEvents: true },
    );
    transcriptDisplayStore.getState().setLocal(layout, custom);
    const persisted = localStorage.getItem(`evener.prefs.transcriptDisplay.${layout}`);
    render(<TranscriptDetailControl open onClose={() => {}} layout={layout} onEditHubDefaults={() => {}} />);
    const editor = screen.getByRole("region", { name: "Transcript detail editor" });
    const disclosure = screen.getByTestId("transcript-detail-summary");
    await user.click(disclosure);
    const timing = within(editor).getByRole("switch", { name: "Round timings" });
    expect(timing.getAttribute("aria-checked")).toBe("true");
    await user.click(screen.getByRole("radio", { name: "Comic" }));
    expect(screen.getByRole("region", { name: "Transcript detail editor" })).toBe(editor);
    expect(disclosure.closest("details")?.open).toBe(true);
    expect(disclosure.closest("summary")?.getAttribute("aria-disabled")).toBe("true");
    for (const control of editor.querySelectorAll<HTMLButtonElement | HTMLSelectElement>("button, select")) {
      expect(control.disabled).toBe(true);
    }
    await user.click(within(editor).getByRole("radio", { name: "Activity" }));
    await user.click(timing);
    expect(transcriptDisplayStore.getState().local[layout]).toEqual(custom);
    expect(localStorage.getItem(`evener.prefs.transcriptDisplay.${layout}`)).toBe(persisted);
    await user.click(screen.getByRole("radio", { name: "Standard" }));
    expect(screen.getByRole("region", { name: "Transcript detail editor" })).toBe(editor);
    expect(disclosure.closest("details")?.open).toBe(true);
    expect((timing as HTMLButtonElement).disabled).toBe(false);
    expect(timing.getAttribute("aria-checked")).toBe("true");
    expect(within(editor).getByRole("radio", { name: "Custom" }).getAttribute("aria-checked")).toBe("true");
    expect(transcriptDisplayStore.getState().local[layout]).toEqual(custom);
    await user.click(screen.getByRole("radio", { name: "Comic" }));
    await user.click(screen.getByRole("button", { name: "Use hub default" }));
    expect(transcriptDisplayStore.getState().local[layout]).toBeUndefined();
    expect(transcriptDisplayStore.getState().presentation[layout]).toBe("comic");
    expect((within(editor).getByRole("radio", { name: "Activity" }) as HTMLButtonElement).disabled).toBe(true);
  },
);

test("composes the controlled overlay only from shared controls and Dialog/Sheet public APIs", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const source = readFileSync(join(here, "TranscriptDetailControl.tsx"), "utf8");

  expect(source).toMatch(
    /import \{ Button, Dialog, InlineNotice, SegmentedControl, Sheet \} from "\.\.\/\.\.\/\.\.\/widgets";/,
  );
  expect(source).not.toMatch(/<button\b/);
  expect(source.match(/<Button\b/g)).toHaveLength(2);
  expect(source.match(/variant="secondary"/g)).toHaveLength(1);
  expect(source).toMatch(/<Button ref=\{editButtonRef\} size="sm" variant="quiet"/);
});

test("CSS keeps the shared overlay body layout-only with no private motion or chrome", () => {
  const css = readModuleCss(import.meta.url, "transcriptDisplay.module.css");

  expect(css).toMatch(/\.detailPanel\s*\{/);
  const directPanelRules = [...css.matchAll(/(^|\n)(?!\s*@)([^{}]+)\{([^{}]*)\}/gm)].filter((match) =>
    match[2]?.split(",").some((selector) => selector.trim() === ".detailPanel"),
  );
  expect(directPanelRules).toHaveLength(1);
  const panelSelector = directPanelRules[0]?.[2] ?? "";
  expect(panelSelector.trim()).toBe(".detailPanel");
  const panelBody = directPanelRules[0]?.[3] ?? "";
  const panelProperties = [...panelBody.matchAll(/(?:^|;)\s*([a-z-]+)\s*:/g)].map((match) => match[1]).sort();
  expect(panelProperties).toEqual(["container-name", "container-type", "min-width"].sort());
  expect(panelBody).toMatch(/min-width:\s*0/);
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
  expect(css).not.toMatch(/\.detailSheetContent\s*\{[^}]*container-(?:type|name)/);
  expect(css).not.toMatch(/\.detailActions\s+button/);
  expect(css).not.toMatch(/\.detailSheetContent\s+\.detailActions/);
  expect(css).not.toMatch(/\.detailTrigger\s*\{/);
});
