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

test("Details expands the candidate's details inline under its row, and again collapses", async () => {
  const user = userEvent.setup();
  renderPanel();
  expect(document.querySelector('[data-kind="plugin-detail"]')).toBeNull();
  const detailsAction = screen.getAllByRole("button", { name: "Details" })[0];
  if (!(detailsAction instanceof HTMLElement)) throw new Error("no Details action renders");
  await user.click(detailsAction);
  // The details expand in place under the row - no sheet dialog opens.
  expect(document.querySelector('[data-kind="plugin-detail"][data-mode="launcher"]')).not.toBeNull();
  expect(screen.queryByRole("dialog")).toBeNull();
  // The same action collapses it again.
  const alphaRow = screen.getByRole("switch", { name: "alpha" }).closest("div");
  if (!(alphaRow instanceof HTMLElement)) throw new Error("no alpha row renders");
  await user.click(within(alphaRow).getByRole("button", { name: "Hide details" }));
  expect(document.querySelector('[data-kind="plugin-detail"]')).toBeNull();
});

test("collapsing the details from the body's action returns focus to the row", async () => {
  const user = userEvent.setup();
  renderPanel();
  const detailsAction = screen.getAllByRole("button", { name: "Details" })[0];
  if (!(detailsAction instanceof HTMLElement)) throw new Error("no Details action renders");
  await user.click(detailsAction);
  const body = document.querySelector('[data-kind="plugin-detail"]');
  if (!(body instanceof HTMLElement)) throw new Error("no detail body expanded");
  // The body's own exit is the focused control; collapsing unmounts it.
  await user.click(within(body).getByRole("button", { name: "Hide details" }));
  expect(document.querySelector('[data-kind="plugin-detail"]')).toBeNull();
  expect(document.activeElement).toBe(screen.getByRole("switch", { name: "alpha" }));
});

test("Escape inside the expanded details collapses them", async () => {
  const user = userEvent.setup();
  renderPanel();
  const detailsAction = screen.getAllByRole("button", { name: "Details" })[0];
  if (!(detailsAction instanceof HTMLElement)) throw new Error("no Details action renders");
  await user.click(detailsAction);
  const body = document.querySelector('[data-kind="plugin-detail"]');
  if (!(body instanceof HTMLElement)) throw new Error("no detail body expanded");
  // The keyboard sits inside the body, the way it does after tabbing in.
  within(body).getByRole("button", { name: "Hide details" }).focus();
  await user.keyboard("{Escape}");
  expect(document.querySelector('[data-kind="plugin-detail"]')).toBeNull();
  expect(screen.getByRole("switch", { name: "alpha" })).toBeTruthy();
});

test("Escape on the expanded row right after opening its details collapses them", async () => {
  const user = userEvent.setup();
  renderPanel();
  const detailsAction = screen.getAllByRole("button", { name: "Details" })[0];
  if (!(detailsAction instanceof HTMLElement)) throw new Error("no Details action renders");
  await user.click(detailsAction);
  expect(document.querySelector('[data-kind="plugin-detail"]')).not.toBeNull();
  // The opener kept focus: no tab into the body, no click anywhere else.
  expect(document.activeElement).toBe(detailsAction);

  await user.keyboard("{Escape}");

  expect(document.querySelector('[data-kind="plugin-detail"]')).toBeNull();
  expect(detailsAction).toBeTruthy();
});

test("shows a removable retained stale name while preserving other selected names", async () => {
  const user = userEvent.setup();
  const onSelectionChange = vi.fn();
  renderPanel({ selection: { mode: "explicit", names: ["alpha", "missing"] }, onSelectionChange });

  expect(screen.getByText("missing")).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Remove missing" }));
  expect(onSelectionChange).toHaveBeenLastCalledWith({ mode: "explicit", names: ["alpha"] });
});

describe("the Details action expands the launcher's inline details", () => {
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

  /** The one expanded detail body (the accordion keeps it unique). */
  function detailsBody(): HTMLElement {
    const body = document.querySelector('[data-kind="plugin-detail"]');
    if (!(body instanceof HTMLElement)) throw new Error("no detail body is expanded");
    return body;
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

  test("Details expands the candidate's details inline, reading Contents by the candidate's own target", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const fake = fakeWithComponents();
    renderPanel({ client: fake });

    // alpha is installed from a marketplace: the resolved winner's path
    // picks the read target, not its manifest name.
    await user.click(within(rowOf("alpha")).getByRole("button", { name: "Details" }));
    await settleComponentsRead();
    const alphaBody = detailsBody();
    expect(alphaBody.getAttribute("data-mode")).toBe("launcher");
    expect(fake.calls.map((call) => call.params)).toEqual([{ targets: [{ path: "/plugins/alpha" }] }]);
    expect(alphaBody.querySelector('[data-kind="plugin-contents"]')).toBeTruthy();
    expect(alphaBody.querySelector('[data-kind="skills"]')).toBeTruthy();
    expect(within(alphaBody).getByText("acme")).toBeTruthy();
    expect(screen.queryByRole("dialog")).toBeNull();

    await user.click(within(rowOf("alpha")).getByRole("button", { name: "Hide details" }));
    expect(document.querySelector('[data-kind="plugin-detail"]')).toBeNull();

    // beta is a directory source with no marketplace: its path picks the
    // target. One row's details are expanded at a time.
    await user.click(within(rowOf("beta")).getByRole("button", { name: "Details" }));
    await settleComponentsRead();
    expect(fake.calls.map((call) => call.params)).toEqual([
      { targets: [{ path: "/plugins/alpha" }] },
      { targets: [{ path: "/tmp/beta" }] },
    ]);
    expect(document.querySelectorAll('[data-kind="plugin-detail"]')).toHaveLength(1);
    expect(detailsBody().querySelector('[data-kind="plugin-contents"]')).toBeTruthy();
  });

  test("the expanded body's switch reflects the row's selection and toggles it", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const fake = fakeWithComponents();
    const { onSelectionChange } = renderPanel({ client: fake });

    await user.click(within(rowOf("alpha")).getByRole("button", { name: "Details" }));
    const included = within(detailsBody()).getByRole("switch", { name: "Included in this session" });
    // alpha is selected in the preview's default selection; beta is not.
    expect(included.getAttribute("aria-checked")).toBe("true");
    await user.click(included);
    // Toggling the only selected plugin off materializes an empty selection.
    expect(onSelectionChange).toHaveBeenCalledWith({ mode: "explicit", names: [] });

    await user.click(within(rowOf("alpha")).getByRole("button", { name: "Hide details" }));
    await user.click(within(rowOf("beta")).getByRole("button", { name: "Details" }));
    const betaIncluded = within(detailsBody()).getByRole("switch", { name: "Included in this session" });
    expect(betaIncluded.getAttribute("aria-checked")).toBe("false");
    await user.click(betaIncluded);
    expect(onSelectionChange).toHaveBeenLastCalledWith({ mode: "explicit", names: ["alpha", "beta"] });
  });

  test("collapsing the details unmounts the body and a re-expand reads again", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const fake = fakeWithComponents();
    renderPanel({ client: fake });

    await user.click(within(rowOf("alpha")).getByRole("button", { name: "Details" }));
    await settleComponentsRead();
    expect(fake.calls.filter((call) => call.method === "evener/plugin/components")).toHaveLength(1);
    await user.click(within(rowOf("alpha")).getByRole("button", { name: "Hide details" }));
    expect(document.querySelector('[data-kind="plugin-detail"]')).toBeNull();

    // The panel mounts the body only while it is expanded, so each expansion
    // performs its own read.
    await user.click(within(rowOf("alpha")).getByRole("button", { name: "Details" }));
    await settleComponentsRead();
    expect(fake.calls.filter((call) => call.method === "evener/plugin/components")).toHaveLength(2);
    expect(detailsBody().querySelector('[data-kind="plugin-contents"]')).toBeTruthy();
  });

  test("an expanded body follows the current winner and collapses when its identity disappears", async () => {
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
    const included = within(detailsBody()).getByRole("switch", { name: "Included in this session" });
    expect(included.getAttribute("aria-checked")).toBe("true");
    expect(detailsBody().textContent).toContain(refreshed.plugins[0]?.description);
    expect(detailsBody().querySelector('[data-kind="plugin-contents"]')).toBeNull();
    await settleComponentsRead();
    expect(fake.calls.at(-1)?.params).toEqual({ targets: [{ path: "/new/beta" }] });
    await user.click(included);
    expect(onSelectionChange).toHaveBeenLastCalledWith({ mode: "explicit", names: [] });
    rerender(panel({ plugins: [] }));
    expect(document.querySelector('[data-kind="plugin-detail"]')).toBeNull();
    rerender(panel(refreshed));
    expect(document.querySelector('[data-kind="plugin-detail"]')).toBeNull();
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
      results: [{ path: "/plugins/alpha", components: { commands: ["revision-command"] } }],
    }));
    rerender(panel({ plugins: [{ ...candidate, version: "2.0.0" }] }));
    await settleComponentsRead();
    expect(fake.calls).toHaveLength(2);
    expect(fake.calls.at(-1)?.params).toEqual({ targets: [{ path: "/plugins/alpha" }] });
    expect(detailsBody().querySelector('[data-kind="commands"]')?.textContent).toContain("revision-command");
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
      within(detailsBody()).getByRole("switch", {
        name: "Included in this session",
      }) as HTMLButtonElement;
    expect(included().disabled).toBe(false);
    await user.click(included());
    expect(onSelectionChange).toHaveBeenCalledWith({ mode: "explicit", names: [] });
    rerender(panel([]));
    expect(included().disabled).toBe(true);
    await user.click(included());
    expect(onSelectionChange).toHaveBeenCalledOnce();
    await user.click(within(rowOf("alpha")).getByRole("button", { name: "Hide details" }));
    expect((screen.getByRole("switch", { name: "alpha" }) as HTMLButtonElement).disabled).toBe(true);
  });
});
