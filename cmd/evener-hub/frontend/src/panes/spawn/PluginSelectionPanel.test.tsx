import {
  type PluginComponents,
  type PluginPreviewResponse,
  SCOPED_HOST_READ_DEBOUNCE_MS,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
import { PluginSelectionPanel } from "./PluginSelectionPanel";

const preview: PluginPreviewResponse = {
  plugins: [
    {
      name: "alpha",
      version: "1.0.0",
      description: "Alpha tools",
      source: "installed",
      marketplace: "acme",
      path: "/plugins/alpha",
      selected: true,
      skillCount: 2,
      agentCount: 1,
      commandCount: 1,
      hookCount: 0,
      mcpCount: 0,
    },
    {
      name: "beta",
      description: "Beta helpers",
      source: "directory",
      path: "/tmp/beta",
      selected: false,
      skillCount: 0,
      agentCount: 0,
      commandCount: 0,
      hookCount: 1,
      mcpCount: 1,
    },
  ],
  diagnostics: [
    { name: "ignored", source: "directory", message: "could not be loaded" },
    {
      name: "superpowers",
      source: "installed",
      path: "/cache/superpowers",
      message: 'duplicate plugin name "superpowers"; keeping the first of: /dev/superpowers, /cache/superpowers',
    },
  ],
  selectionErrors: [{ name: "missing", reason: "no valid current winner" }],
};

function renderPanel(overrides: Partial<React.ComponentProps<typeof PluginSelectionPanel>> = {}) {
  const onSelectionChange = vi.fn();
  render(
    <PluginSelectionPanel
      preview={preview}
      selection={{ mode: "default" }}
      client={new FakeClient()}
      onSelectionChange={onSelectionChange}
      onRetry={vi.fn()}
      {...overrides}
    />,
  );
  return { onSelectionChange };
}

test("renders named switches, source subheading, counts, and description", () => {
  renderPanel();

  expect(screen.getByRole("switch", { name: "alpha" }).getAttribute("aria-checked")).toBe("true");
  expect(screen.getByRole("switch", { name: "beta" }).getAttribute("aria-checked")).toBe("false");
  expect(screen.getByText("installed from acme")).toBeTruthy();
  expect(screen.getByText("directory: /tmp/beta")).toBeTruthy();
  expect(screen.getByText("2 skills · 1 agent · 1 command")).toBeTruthy();
  expect(screen.getByText("Alpha tools")).toBeTruthy();
  expect(screen.getByText("1 of 2 selected")).toBeTruthy();
});

test("each row orders source subheading, then counts, then description", () => {
  renderPanel();

  const text = screen.getByTestId("plugin-selection-panel").textContent ?? "";
  const sourceAt = text.indexOf("installed from acme");
  const countsAt = text.indexOf("2 skills · 1 agent · 1 command");
  const descriptionAt = text.indexOf("Alpha tools");
  expect(sourceAt).toBeGreaterThanOrEqual(0);
  expect(sourceAt).toBeLessThan(countsAt);
  expect(countsAt).toBeLessThan(descriptionAt);
});

test("keyboard toggles a switch and materializes explicit selection", async () => {
  const user = userEvent.setup();
  const { onSelectionChange } = renderPanel();
  const beta = screen.getByRole("switch", { name: "beta" });

  beta.focus();
  await user.keyboard(" ");

  expect(onSelectionChange).toHaveBeenCalledWith({ mode: "explicit", names: ["alpha", "beta"] });
});

test("supports All and None", async () => {
  const user = userEvent.setup();
  const { onSelectionChange } = renderPanel();

  await user.click(screen.getByRole("button", { name: "None" }));
  expect(onSelectionChange).toHaveBeenLastCalledWith({ mode: "explicit", names: [] });
  await user.click(screen.getByRole("button", { name: "All" }));
  expect(onSelectionChange).toHaveBeenLastCalledWith({ mode: "explicit", names: ["alpha", "beta"] });
});

test("exposes diagnostics and blocking selection errors without relying on color", async () => {
  const user = userEvent.setup();
  renderPanel();
  expect(screen.getByRole("alert").textContent).toContain("missing");
  expect(screen.queryByText("could not be loaded")).toBeNull();
  await user.click(screen.getByText(/preview diagnostic/));
  expect(screen.getByText("could not be loaded")).toBeTruthy();
});

test("diagnostics render above the plugin list", async () => {
  const user = userEvent.setup();
  renderPanel();
  await user.click(screen.getByText(/preview diagnostic/));

  const text = screen.getByTestId("plugin-selection-panel").textContent ?? "";
  const diagnosticAt = text.indexOf("could not be loaded");
  const firstRowAt = text.indexOf("2 skills · 1 agent · 1 command");
  expect(diagnosticAt).toBeGreaterThanOrEqual(0);
  expect(diagnosticAt).toBeLessThan(firstRowAt);
});

test("a diagnostic whose message already names its path does not repeat it in parentheses", async () => {
  const user = userEvent.setup();
  renderPanel();
  await user.click(screen.getByText(/preview diagnostic/));

  const item = screen.getByText(/duplicate plugin name/).closest("li");
  if (item === null) throw new Error("duplicate diagnostic list item not found");
  expect(item.textContent).toBe(
    'superpowers: duplicate plugin name "superpowers"; keeping the first of: /dev/superpowers, /cache/superpowers',
  );
});

test("uses explicit names over preview selected flags", () => {
  renderPanel({ selection: { mode: "explicit", names: ["beta"] } });
  expect(screen.getByRole("switch", { name: "alpha" }).getAttribute("aria-checked")).toBe("false");
  expect(screen.getByRole("switch", { name: "beta" }).getAttribute("aria-checked")).toBe("true");
  expect(within(screen.getByTestId("plugin-selection-panel")).getByText("1 of 2 selected")).toBeTruthy();
});

test("retry is reachable", async () => {
  const user = userEvent.setup();
  const onRetry = vi.fn();
  renderPanel({ onRetry });
  await user.click(screen.getByRole("button", { name: "Retry" }));
  expect(onRetry).toHaveBeenCalledOnce();
});

test("shows a removable retained stale name while preserving other selected names", async () => {
  const user = userEvent.setup();
  const onSelectionChange = vi.fn();
  renderPanel({ selection: { mode: "explicit", names: ["alpha", "missing"] }, onSelectionChange });

  expect(screen.getByText("missing")).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Remove missing" }));
  expect(onSelectionChange).toHaveBeenLastCalledWith({ mode: "explicit", names: ["alpha"] });
});

describe("the Details action opens the launcher sheet", () => {
  const COMPONENTS: PluginComponents = { skills: ["run"], commands: ["fmt"] };

  afterEach(() => {
    vi.useRealTimers();
  });

  function fakeWithComponents(): FakeClient {
    const fake = new FakeClient();
    fake.on("evener/plugin/components", (params) => ({
      results: params.targets.map((target) => ({ ...target, components: COMPONENTS })),
    }));
    return fake;
  }

  async function settleComponentsRead(): Promise<void> {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(SCOPED_HOST_READ_DEBOUNCE_MS);
      await Promise.resolve();
    });
  }

  function rowOf(name: string): HTMLElement {
    const row = screen.getByRole("switch", { name }).closest("div");
    if (row === null) throw new Error(`no row renders the ${name} switch`);
    return row;
  }

  test("every row's description ends in a Details action", () => {
    renderPanel();
    expect(document.querySelectorAll('[data-kind="plugin-row"][data-mode="launcher"]')).toHaveLength(2);
    const metadata = document.querySelectorAll('[data-testid="plugin-row-metadata"]');
    expect(metadata).toHaveLength(2);
    for (const container of metadata) {
      if (!(container instanceof HTMLElement)) throw new Error("a row's metadata is not an element");
      const details = within(container).getByRole("button", { name: "Details" });
      // The action is the description's last element, after source, counts and
      // any description text - a quiet link-button, not a trailing control.
      const last = container.lastElementChild;
      if (last === null) throw new Error("a row's metadata has no last element");
      expect(last.contains(details)).toBe(true);
    }
  });

  test("Details opens the candidate's sheet, reading Contents by the candidate's own target", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const fake = fakeWithComponents();
    renderPanel({ client: fake });

    // alpha is installed from a marketplace (and also carries a path): the
    // marketplace+name pair picks the read target.
    await user.click(within(rowOf("alpha")).getByRole("button", { name: "Details" }));
    await settleComponentsRead();
    const alphaSheet = screen.getByRole("dialog", { name: "alpha" });
    expect(fake.calls.map((call) => call.params)).toEqual([{ targets: [{ plugin: "alpha", marketplace: "acme" }] }]);
    expect(alphaSheet.querySelector('[data-kind="plugin-contents"]')).toBeTruthy();
    expect(alphaSheet.querySelector('[data-kind="skills"]')).toBeTruthy();
    expect(within(alphaSheet).getByText("acme")).toBeTruthy();
    expect(screen.getAllByRole("dialog")).toHaveLength(1);

    await user.click(within(alphaSheet).getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).toBeNull();

    // beta is a directory source with no marketplace: its path picks the
    // target. One sheet is open at a time.
    await user.click(within(rowOf("beta")).getByRole("button", { name: "Details" }));
    await settleComponentsRead();
    expect(fake.calls.map((call) => call.params)).toEqual([
      { targets: [{ plugin: "alpha", marketplace: "acme" }] },
      { targets: [{ path: "/tmp/beta" }] },
    ]);
    const betaSheet = screen.getByRole("dialog", { name: "beta" });
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    expect(betaSheet.querySelector('[data-kind="plugin-contents"]')).toBeTruthy();
  });

  test("the sheet's switch reflects the row's selection and toggles it", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const fake = fakeWithComponents();
    const { onSelectionChange } = renderPanel({ client: fake });

    await user.click(within(rowOf("alpha")).getByRole("button", { name: "Details" }));
    const alphaSheet = screen.getByRole("dialog", { name: "alpha" });
    const included = within(alphaSheet).getByRole("switch", { name: "Included in this session" });
    // alpha is selected in the preview's default selection; beta is not.
    expect(included.getAttribute("aria-checked")).toBe("true");
    await user.click(included);
    // Toggling the only selected plugin off materializes an empty selection.
    expect(onSelectionChange).toHaveBeenCalledWith({ mode: "explicit", names: [] });

    await user.click(within(alphaSheet).getByRole("button", { name: "Close" }));
    await user.click(within(rowOf("beta")).getByRole("button", { name: "Details" }));
    const betaSheet = screen.getByRole("dialog", { name: "beta" });
    const betaIncluded = within(betaSheet).getByRole("switch", { name: "Included in this session" });
    expect(betaIncluded.getAttribute("aria-checked")).toBe("false");
    await user.click(betaIncluded);
    expect(onSelectionChange).toHaveBeenLastCalledWith({ mode: "explicit", names: ["alpha", "beta"] });
  });

  test("closing the sheet unmounts it and a reopen reads again", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const fake = fakeWithComponents();
    renderPanel({ client: fake });

    await user.click(within(rowOf("alpha")).getByRole("button", { name: "Details" }));
    await settleComponentsRead();
    expect(fake.calls.filter((call) => call.method === "evener/plugin/components")).toHaveLength(1);
    await user.click(within(screen.getByRole("dialog", { name: "alpha" })).getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).toBeNull();

    // The panel mounts the sheet only while it is open, so each open performs
    // its own read.
    await user.click(within(rowOf("alpha")).getByRole("button", { name: "Details" }));
    await settleComponentsRead();
    expect(fake.calls.filter((call) => call.method === "evener/plugin/components")).toHaveLength(2);
    expect(screen.getByRole("dialog", { name: "alpha" }).querySelector('[data-kind="plugin-contents"]')).toBeTruthy();
  });

  test("an open sheet follows the current winner and closes when its identity disappears", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const fake = fakeWithComponents();
    const onSelectionChange = vi.fn();
    const panel = (current: PluginPreviewResponse) => (
      <PluginSelectionPanel
        preview={current}
        selection={{ mode: "default" }}
        client={fake}
        onSelectionChange={onSelectionChange}
        onRetry={vi.fn()}
      />
    );
    const { rerender } = render(panel(preview));
    await user.click(within(rowOf("beta")).getByRole("button", { name: "Details" }));
    await settleComponentsRead();
    const candidate = preview.plugins[1];
    if (!candidate) throw new Error("missing beta fixture");
    const refreshed: PluginPreviewResponse = {
      plugins: [
        { ...candidate, selected: true, path: "/new/beta", version: "2.0.0", description: "replacement-sentinel" },
      ],
    };
    rerender(panel(refreshed));
    const sheet = screen.getByRole("dialog", { name: "beta" });
    const included = within(sheet).getByRole("switch", { name: "Included in this session" });
    expect(included.getAttribute("aria-checked")).toBe("true");
    expect(sheet.textContent).toContain(refreshed.plugins[0]?.description);
    expect(sheet.querySelector('[data-kind="plugin-contents"]')).toBeNull();
    await settleComponentsRead();
    expect(fake.calls.at(-1)?.params).toEqual({ targets: [{ path: "/new/beta" }] });
    await user.click(included);
    expect(onSelectionChange).toHaveBeenLastCalledWith({ mode: "explicit", names: [] });
    rerender(panel({ plugins: [] }));
    expect(screen.queryByRole("dialog")).toBeNull();
    rerender(panel(refreshed));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  test("an installed candidate revision re-reads contents even when its pair stays the same", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const fake = fakeWithComponents();
    const panel = (current: PluginPreviewResponse) => (
      <PluginSelectionPanel
        preview={current}
        selection={{ mode: "default" }}
        client={fake}
        onSelectionChange={vi.fn()}
        onRetry={vi.fn()}
      />
    );
    const { rerender } = render(panel(preview));
    await user.click(within(rowOf("alpha")).getByRole("button", { name: "Details" }));
    await settleComponentsRead();
    const candidate = preview.plugins[0];
    if (!candidate) throw new Error("missing alpha fixture");
    fake.on("evener/plugin/components", () => ({
      results: [{ plugin: "alpha", marketplace: "acme", components: { commands: ["revision-command"] } }],
    }));
    rerender(panel({ plugins: [{ ...candidate, version: "2.0.0" }] }));
    await settleComponentsRead();
    expect(fake.calls).toHaveLength(2);
    expect(fake.calls.at(-1)?.params).toEqual({ targets: [{ plugin: "alpha", marketplace: "acme" }] });
    expect(
      screen.getByRole("dialog", { name: "alpha" }).querySelector('[data-kind="commands"]')?.textContent,
    ).toContain("revision-command");
  });

  test("remove-only mode visibly disables refused inclusion but still allows removal", async () => {
    const user = userEvent.setup();
    const onSelectionChange = vi.fn();
    const fake = fakeWithComponents();
    const panel = (names: string[]) => (
      <PluginSelectionPanel
        preview={preview}
        selection={{ mode: "explicit", names }}
        removeOnly
        client={fake}
        onSelectionChange={onSelectionChange}
        onRetry={vi.fn()}
      />
    );
    const { rerender } = render(panel(["alpha"]));
    await user.click(within(rowOf("alpha")).getByRole("button", { name: "Details" }));
    const included = () =>
      within(screen.getByRole("dialog", { name: "alpha" })).getByRole("switch", {
        name: "Included in this session",
      }) as HTMLButtonElement;
    expect(included().disabled).toBe(false);
    await user.click(included());
    expect(onSelectionChange).toHaveBeenCalledWith({ mode: "explicit", names: [] });
    rerender(panel([]));
    expect(included().disabled).toBe(true);
    await user.click(included());
    expect(onSelectionChange).toHaveBeenCalledOnce();
    await user.click(within(screen.getByRole("dialog", { name: "alpha" })).getByRole("button", { name: "Close" }));
    expect((screen.getByRole("switch", { name: "alpha" }) as HTMLButtonElement).disabled).toBe(true);
  });
});
