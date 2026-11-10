import {
  type PluginComponents,
  type PluginLaunchCandidate,
  SCOPED_HOST_READ_DEBOUNCE_MS,
  WireError,
} from "@evener/appwire-client";
import { deferRequest, FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { useIsMobile } from "../../shell/useIsMobile";
import sheetStyles from "../../widgets/sheet/sheet.module.css";
import { LauncherPluginSheet } from "./LauncherPluginSheet";

vi.mock("../../shell/useIsMobile", () => ({ useIsMobile: vi.fn(() => false) }));

// A directory-source candidate: no marketplace, so the preview resolved it
// by path - the same path the sheet's read must target.
const DIRECTORY_CANDIDATE: PluginLaunchCandidate = {
  name: "beta",
  version: "0.9.0",
  description: "Beta helpers",
  source: "directory",
  path: "/tmp/beta",
  selected: false,
  skillCount: 0,
  agentCount: 0,
  commandCount: 0,
  hookCount: 1,
  mcpCount: 1,
};

const COMPONENTS: PluginComponents = { skills: ["run"], warnings: ["launcher-warning-sentinel"] };

function componentsClient(): FakeClient {
  const fake = new FakeClient();
  fake.on("evener/plugin/components", (params) => ({
    results: params.targets.map((target) => ({ ...target, components: COMPONENTS })),
  }));
  return fake;
}

function renderSheet(overrides: Partial<React.ComponentProps<typeof LauncherPluginSheet>> = {}) {
  const onClose = vi.fn();
  const onToggleSelected = vi.fn();
  render(
    <LauncherPluginSheet
      open
      candidate={DIRECTORY_CANDIDATE}
      selected={false}
      onClose={onClose}
      onToggleSelected={onToggleSelected}
      client={componentsClient()}
      {...overrides}
    />,
  );
  return { onClose, onToggleSelected };
}

async function settleRead(): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(SCOPED_HOST_READ_DEBOUNCE_MS);
    await Promise.resolve();
  });
}

afterEach(() => {
  vi.useRealTimers();
  vi.mocked(useIsMobile).mockReturnValue(false);
});

test("a closed sheet renders no dialog", () => {
  renderSheet({ open: false });
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("a directory-source candidate reads by its path and renders the answer as Contents", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
  const fake = new FakeClient();
  fake.on("evener/plugin/components", (params) => {
    expect(params).toEqual({ targets: [{ path: "/tmp/beta" }] });
    return { results: [{ path: "/tmp/beta", components: COMPONENTS }] };
  });
  const { onToggleSelected } = renderSheet({ client: fake });
  const sheet = screen.getByRole("dialog", { name: "beta" });
  await settleRead();
  // The one-target batch named the candidate by path alone.
  expect(fake.calls).toEqual([{ method: "evener/plugin/components", params: { targets: [{ path: "/tmp/beta" }] } }]);
  expect(sheet.querySelector('[data-kind="plugin-contents"]')).toBeTruthy();
  expect(sheet.querySelector('[data-kind="skills"]')).toBeTruthy();
  expect(within(sheet).getByRole("alert").getAttribute("data-reason")).toBe("load-warnings");
  expect(within(sheet).getByRole("alert").textContent).toContain(COMPONENTS.warnings?.[0]);
  expect(sheet.querySelector('[data-reason="manifest-only"]')).toBeNull();
  // Metadata: Version when the candidate carries one, no Marketplace row for
  // a directory source, and the candidate's source kind.
  expect(within(sheet).getByText("v0.9.0")).toBeTruthy();
  expect(sheet.querySelector('[data-kind="plugin-marketplace"]')).toBeNull();
  expect(sheet.querySelector('[data-kind="plugin-detail"]')?.getAttribute("data-mode")).toBe("launcher");
  expect(within(sheet).getByText("directory")).toBeTruthy();
  // Launcher mode has no editor: one selection switch, no footer actions.
  const included = within(sheet).getByRole("switch", { name: "Included in this session" });
  expect(included.getAttribute("aria-checked")).toBe("false");
  await user.click(included);
  expect(onToggleSelected).toHaveBeenCalledWith(true);
  expect(within(sheet).queryByRole("button", { name: "Install" })).toBeNull();
  expect(within(sheet).queryByRole("button", { name: "Remove" })).toBeNull();
});

test("an installed winner reads its resolved path when its manifest name differs from its catalog name", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  const path = "/plugins/cache/m/zzz-enabled/1.0.0";
  const fake = new FakeClient();
  fake.on("evener/plugin/components", (params) => ({
    results: params.targets.map((target) =>
      target.path === path
        ? { ...target, components: { commands: ["winner-command"] } }
        : { ...target, error: "unresolved-pair-sentinel" },
    ),
  }));
  renderSheet({
    client: fake,
    candidate: { ...DIRECTORY_CANDIDATE, name: "shared", source: "installed", marketplace: "m", path },
  });
  await settleRead();

  expect(fake.calls).toEqual([{ method: "evener/plugin/components", params: { targets: [{ path }] } }]);
  const sheet = screen.getByRole("dialog", { name: "shared" });
  expect(sheet.querySelector('[data-kind="commands"]')?.textContent).toContain("winner-command");
  expect(sheet.querySelector('[data-reason="plugin-broken"]')).toBeNull();
});

test("launcher details use bottom sheet geometry on mobile", () => {
  vi.mocked(useIsMobile).mockReturnValue(true);
  renderSheet();
  const sheet = screen.getByRole("dialog", { name: DIRECTORY_CANDIDATE.name });
  expect(sheet.classList.contains(sheetStyles.bottom ?? "missing-bottom-class")).toBe(true);
  expect(sheet.classList.contains(sheetStyles.right ?? "missing-right-class")).toBe(false);
});

test("an erroring read degrades with the quiet notice where Contents would be, and retry re-reads", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
  const fake = new FakeClient();
  fake.on("evener/plugin/components", () => {
    throw new WireError("method not found: evener/plugin/components", -32601);
  });
  renderSheet({ client: fake });
  const sheet = screen.getByRole("dialog", { name: "beta" });
  await settleRead();
  // The read failed for the whole method, not for this plugin: a quiet
  // degrade notice where Contents would be, never the failure alert.
  const notice = within(sheet).getByRole("status");
  expect(notice.getAttribute("data-reason")).toBe("components-degraded");
  expect(within(sheet).queryByRole("alert")).toBeNull();
  expect(sheet.querySelector('[data-kind="plugin-contents"]')).toBeNull();
  expect(document.querySelector('[data-reason="manifest-only"]')).toBeNull();

  // The hub learns the method: the retry's fresh read now answers.
  fake.on("evener/plugin/components", () => ({ results: [{ path: "/tmp/beta", components: COMPONENTS }] }));
  await user.click(within(sheet).getByRole("button", { name: "Retry" }));
  await settleRead();
  expect(fake.calls.filter((call) => call.method === "evener/plugin/components")).toHaveLength(2);
  expect(sheet.querySelector('[data-kind="plugin-contents"]')).toBeTruthy();
  expect(within(sheet).queryByRole("status")).toBeNull();
});

test("keyboard Retry keeps focus inside launcher details while loading and Escape dismisses", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
  const fake = new FakeClient();
  fake.on("evener/plugin/components", () => {
    throw new WireError("unavailable-method-sentinel", -32601);
  });
  function Harness() {
    const [open, setOpen] = useState(true);
    return (
      <LauncherPluginSheet
        open={open}
        candidate={DIRECTORY_CANDIDATE}
        selected={false}
        onClose={() => setOpen(false)}
        onToggleSelected={() => {}}
        client={fake}
      />
    );
  }
  render(<Harness />);
  await settleRead();
  const sheet = screen.getByRole("dialog");
  const retry = within(within(sheet).getByRole("status")).getByRole("button");
  for (let i = 0; i < 5 && document.activeElement !== retry; i++) await user.tab();
  expect(document.activeElement).toBe(retry);
  const release = deferRequest<{ results: [] }>(fake, "evener/plugin/components");
  await user.keyboard("{Enter}");
  await settleRead();
  expect(fake.calls.filter((call) => call.method === "evener/plugin/components")).toHaveLength(2);
  expect(retry.isConnected).toBe(false);
  expect(sheet.contains(document.activeElement)).toBe(true);
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog")).toBeNull();
  await act(async () => release({ results: [] }));
});

test("a remove-only inclusion toggle rehomes focus when its own activation disables it", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
  const fake = componentsClient();
  function Harness() {
    const [open, setOpen] = useState(true);
    const [selected, setSelected] = useState(true);
    return (
      <LauncherPluginSheet
        open={open}
        candidate={DIRECTORY_CANDIDATE}
        selected={selected}
        selectionDisabled={!selected}
        onClose={() => setOpen(false)}
        onToggleSelected={setSelected}
        client={fake}
      />
    );
  }
  render(<Harness />);
  await settleRead();
  const sheet = screen.getByRole("dialog");
  const included = within(sheet).getByRole("switch");
  for (let i = 0; i < 5 && document.activeElement !== included; i++) await user.tab();
  expect(document.activeElement).toBe(included);
  await user.keyboard("{Enter}");
  expect(included.getAttribute("aria-checked")).toBe("false");
  expect((included as HTMLButtonElement).disabled).toBe(true);
  expect(document.activeElement).not.toBe(included);
  expect(sheet.contains(document.activeElement)).toBe(true);
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("a plugin whose own load fails shows the failure where Contents would be, never manifest-only", async () => {
  vi.useFakeTimers();
  const fake = new FakeClient();
  fake.on("evener/plugin/components", () => ({
    results: [{ path: "/tmp/beta", error: "target-load-error-sentinel" }],
  }));
  renderSheet({ client: fake });
  const sheet = screen.getByRole("dialog", { name: "beta" });
  await settleRead();
  // The row carries the plugin's own load failure: the failure alert, with no
  // retry (the plugin is the problem, not the read) and no Contents.
  const failure = within(sheet).getByRole("alert");
  expect(failure.getAttribute("data-reason")).toBe("plugin-broken");
  expect(within(sheet).queryByRole("status")).toBeNull();
  expect(within(sheet).queryByRole("button", { name: "Retry" })).toBeNull();
  expect(sheet.querySelector('[data-kind="plugin-contents"]')).toBeNull();
  expect(document.querySelector('[data-reason="manifest-only"]')).toBeNull();
});
