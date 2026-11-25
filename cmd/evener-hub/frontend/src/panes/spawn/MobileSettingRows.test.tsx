import type { PluginPreviewResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import { readModuleCss } from "../../styles/cssBlock";
import { MobileSettingRows, type MobileSettingRowsProps } from "./MobileSettingRows";
import { GLOBAL_LAST_WORKING_DIR_KEY, setGlobalLastWorkingDir } from "./spawnDefaults";

function props(overrides: Partial<MobileSettingRowsProps> = {}): MobileSettingRowsProps {
  return {
    harness: "evener",
    harnessOptions: [
      { value: "evener", label: "evener" },
      { value: "external", label: "external" },
    ],
    onHarnessChange: vi.fn(),
    cwd: "/tmp/project",
    onCwdChange: vi.fn(),
    complete: vi.fn(async () => []),
    listRecents: vi.fn(async () => []),
    fallbackDir: "/tmp",
    validatePath: async (path) => ({ valid: true, path }),
    createDirectory: async () => {},
    onCwdPanelClose: vi.fn(),
    branch: "main",
    hostOptions: [],
    hostValue: "local",
    hostLabel: "this host (hub)",
    connectingHosts: new Set(),
    onHostSelect: vi.fn(),
    onHostConnect: vi.fn(),
    onConnectHost: vi.fn(),
    accessMode: "",
    accessOptions: [
      { value: "", label: "(default)" },
      { value: "workspace-write", label: "workspace write" },
    ],
    effectiveAccessLabel: "(default)",
    advancedAccessOverride: false,
    onAccessChange: vi.fn(),
    pluginPreview: { status: "ready", response: { plugins: [] } as PluginPreviewResponse },
    pluginSelection: { mode: "default" },
    pluginClient: new FakeClient(),
    onPluginSelectionChange: vi.fn(),
    onPluginRetry: vi.fn(),
    ...overrides,
  };
}

function renderRows(overrides: Partial<MobileSettingRowsProps> = {}) {
  return render(<MobileSettingRows {...props(overrides)} />);
}

test("renders all Treatment A rows in order with full-row controls", () => {
  renderRows();

  const rows = within(screen.getByTestId("mobile-spawn-config")).getAllByTestId("mobile-spawn-row");
  expect(rows.map((row) => row.getAttribute("data-label"))).toEqual([
    "Harness",
    "Working directory",
    "Branch",
    "Access mode",
    "Plugins",
  ]);
  expect(rows[0]!.tagName).toBe("BUTTON");
  expect(rows[0]!.getAttribute("aria-haspopup")).toBe("dialog");
  // Branch is a read-only readout, not a picker.
  expect(rows[2]!.tagName).not.toBe("BUTTON");
});

test("each picker row ends in one drill-in chevron; the read-only row has none", () => {
  renderRows();

  const rows = within(screen.getByTestId("mobile-spawn-config")).getAllByTestId("mobile-spawn-row");
  for (const row of rows) {
    // ListRow draws the row's one drill-in chevron; the row must not draw a
    // second one of its own before it.
    expect(row.querySelectorAll('[aria-hidden="true"] svg')).toHaveLength(row.tagName === "BUTTON" ? 1 : 0);
  }
});

test("a picker row exposes its value and its disclosure state", async () => {
  const user = userEvent.setup();
  renderRows();

  const row = screen.getByRole("button", { name: "Harness: evener" });
  expect(row.getAttribute("data-label")).toBe("Harness");
  expect(row.getAttribute("aria-expanded")).toBe("false");

  await user.click(row);
  await screen.findByRole("dialog", { name: "Choose harness" });
  expect(row.getAttribute("aria-expanded")).toBe("true");

  await user.keyboard("{Escape}");
  await waitFor(() => expect(row.getAttribute("aria-expanded")).toBe("false"));
});

test("every mobile setting row keeps the 48px tap floor", () => {
  renderRows();

  const rows = within(screen.getByTestId("mobile-spawn-config")).getAllByTestId("mobile-spawn-row");
  for (const row of rows) expect((row as HTMLElement).style.minHeight).toBe("48px");
});

test("a long working-directory path wraps in full", () => {
  const cwd = "/tmp/a-very-long-directory-name/another-long-directory-name/the-final-project";
  renderRows({ cwd });

  const row = screen.getByRole("button", { name: `Working directory: ${cwd}` });
  const value = within(row).getByText(cwd);
  expect(value.style.whiteSpace).toBe("normal");
  expect(value.style.overflowWrap).toBe("anywhere");
});

// Issue #198: choosing a model is one act, so it uses one control everywhere -
// the prompt card's ModelSwitchTrigger, the same one the session composer
// carries. This list renders no model row and opens no model sheet, and the
// bespoke "Use default" row that sheet used to carry went with it ("(default)"
// is the trigger's own resting label).
test("no model row and no model sheet - the prompt card owns that setting now", () => {
  renderRows();

  const labels = within(screen.getByTestId("mobile-spawn-config"))
    .getAllByTestId("mobile-spawn-row")
    .map((row) => row.getAttribute("data-label"));
  expect(labels).not.toContain("Model");
  expect(screen.queryByRole("button", { name: /^Model:/ })).toBeNull();
  expect(screen.queryByRole("dialog", { name: "Choose model" })).toBeNull();
  expect(screen.queryByText("Use default")).toBeNull();
});

test("the host row opens the one host list in a sheet", async () => {
  const user = userEvent.setup();
  const onHostSelect = vi.fn();
  const onHostConnect = vi.fn();
  const onConnectHost = vi.fn();
  renderRows({
    hostOptions: [
      { id: "local", label: "magic-kingdom (hub)", online: true },
      { id: "buildbox", label: "buildbox", online: true },
      { id: "offline-host", label: "offline-host", online: false },
    ],
    hostValue: "local",
    hostLabel: "magic-kingdom (hub)",
    onHostSelect,
    onHostConnect,
    onConnectHost,
  });

  const labels = within(screen.getByTestId("mobile-spawn-config"))
    .getAllByTestId("mobile-spawn-row")
    .map((row) => row.getAttribute("data-label"));
  expect(labels[0]).toBe("Host");

  const rowButton = screen.getByRole("button", { name: "Host: magic-kingdom (hub)" });
  await user.click(rowButton);
  const dialog = await screen.findByRole("dialog", { name: "Host" });
  const group = within(dialog).getByRole("group", { name: "Host options" });
  await user.click(within(group).getByRole("button", { name: "Connect offline-host" }));
  expect(onHostConnect).toHaveBeenCalledWith("offline-host");
  expect(screen.getByRole("dialog", { name: "Host" })).toBe(dialog);

  await user.click(within(group).getByRole("button", { name: "buildbox" }));
  expect(onHostSelect).toHaveBeenCalledWith("buildbox");
  expect(screen.queryByRole("dialog", { name: "Host" })).toBeNull();

  await user.click(screen.getByRole("button", { name: "Host: magic-kingdom (hub)" }));
  const reopened = await screen.findByRole("dialog", { name: "Host" });
  await user.click(within(reopened).getByRole("button", { name: "Connect host" }));
  expect(onConnectHost).toHaveBeenCalledTimes(1);
  expect(screen.queryByRole("dialog", { name: "Host" })).toBeNull();
});

// Connect disables its own button while the attach is in flight. Focus must
// stay in the open sheet, where Escape and Tab still belong to it.
test("a pending Connect keeps focus inside the open host sheet", async () => {
  const user = userEvent.setup();
  const hostOptions = [
    { id: "local", label: "magic-kingdom (hub)", online: true },
    { id: "offline-host", label: "offline-host", online: false },
  ];
  const hostLabel = "magic-kingdom (hub)";
  const { rerender } = render(<MobileSettingRows {...props({ hostOptions, hostLabel })} />);

  await user.click(screen.getByRole("button", { name: `Host: ${hostLabel}` }));
  const dialog = await screen.findByRole("dialog", { name: "Host" });
  const connect = within(dialog).getByRole("button", { name: "Connect offline-host" });
  act(() => connect.focus());
  expect(document.activeElement).toBe(connect);

  rerender(<MobileSettingRows {...props({ hostOptions, hostLabel, connectingHosts: new Set(["offline-host"]) })} />);

  expect(document.activeElement).not.toBe(connect);
  expect(dialog.contains(document.activeElement)).toBe(true);
});

test("option sheets commit a selection and return focus to the row", async () => {
  const user = userEvent.setup();
  const onHarnessChange = vi.fn();
  renderRows({ onHarnessChange });

  const rowButton = screen.getByRole("button", { name: "Harness: evener" });
  await user.click(rowButton);
  const dialog = await screen.findByRole("dialog", { name: "Choose harness" });
  expect(within(dialog).getByRole("radiogroup", { name: "Harness" })).toBeTruthy();
  await user.click(within(dialog).getByRole("radio", { name: "external" }));

  expect(onHarnessChange).toHaveBeenCalledWith("external");
  expect(screen.queryByRole("dialog", { name: "Choose harness" })).toBeNull();
  expect(document.activeElement).toBe(rowButton);
});

test("the option sheet marks exactly the checked radio", async () => {
  const user = userEvent.setup();
  const onAccessChange = vi.fn();
  renderRows({ onAccessChange });

  await user.click(screen.getByRole("button", { name: "Access mode: (default)" }));
  const dialog = await screen.findByRole("dialog", { name: "Choose access mode" });
  const checked = within(dialog).getByRole("radio", { name: "(default)" });
  const unchecked = within(dialog).getByRole("radio", { name: "workspace write" });
  expect(checked.getAttribute("aria-checked")).toBe("true");
  expect(checked.querySelector("[data-selected-mark]")).not.toBeNull();
  expect(unchecked.getAttribute("aria-checked")).toBe("false");
  expect(unchecked.querySelector("[data-selected-mark]")).toBeNull();

  await user.click(unchecked);
  expect(onAccessChange).toHaveBeenCalledWith("workspace-write");
});

// The row reports what the launch will actually be allowed to do, including
// when Advanced options' sandbox override is what decides it. The sheet stays
// on the requested mode so it remains the thing the user can change.
test("the access row reads the resolved level and its Advanced source, and the sheet keeps the requested mode", async () => {
  const user = userEvent.setup();
  renderRows({ effectiveAccessLabel: "read-only", advancedAccessOverride: true });

  const row = within(screen.getByTestId("mobile-spawn-config"))
    .getAllByTestId("mobile-spawn-row")
    .find((candidate) => candidate.dataset.label === "Access mode") as HTMLElement;
  expect(row.dataset.state).toBe("advanced");
  const value = within(row).getByTitle(/^read-only/);
  expect(value.textContent).toContain("read-only");

  await user.click(row);
  const dialog = await screen.findByRole("dialog", { name: "Choose access mode" });
  expect(within(dialog).getByRole("radio", { name: "(default)" }).getAttribute("aria-checked")).toBe("true");
});

test("arrow keys preview the access choice without committing; Enter commits it", async () => {
  const user = userEvent.setup();
  const onAccessChange = vi.fn();
  renderRows({
    onAccessChange,
    accessOptions: [
      { value: "", label: "(default)" },
      { value: "read-only", label: "read-only" },
      { value: "workspace-write", label: "workspace write" },
    ],
  });

  const trigger = screen.getByRole("button", { name: "Access mode: (default)" });
  await user.click(trigger);
  const dialog = await screen.findByRole("dialog", { name: "Choose access mode" });
  within(dialog).getByRole("radio", { name: "(default)" }).focus();

  await user.keyboard("{ArrowDown}");
  expect(within(dialog).getByRole("radio", { name: "read-only" }).getAttribute("aria-checked")).toBe("true");
  await user.keyboard("{ArrowDown}");
  expect(within(dialog).getByRole("radio", { name: "workspace write" }).getAttribute("aria-checked")).toBe("true");

  expect(onAccessChange).not.toHaveBeenCalled();
  expect(screen.getByRole("dialog", { name: "Choose access mode" })).toBe(dialog);
  expect(document.activeElement).toBe(within(dialog).getByRole("radio", { name: "workspace write" }));

  await user.keyboard("{Enter}");

  expect(onAccessChange).toHaveBeenCalledWith("workspace-write");
  expect(screen.queryByRole("dialog", { name: "Choose access mode" })).toBeNull();
  expect(document.activeElement).toBe(trigger);
});

test("Escape after an arrow preview discards the uncommitted access choice", async () => {
  const user = userEvent.setup();
  const onAccessChange = vi.fn();
  renderRows({
    onAccessChange,
    accessOptions: [
      { value: "", label: "(default)" },
      { value: "read-only", label: "read-only" },
      { value: "workspace-write", label: "workspace write" },
    ],
  });

  const trigger = screen.getByRole("button", { name: "Access mode: (default)" });
  await user.click(trigger);
  let dialog = await screen.findByRole("dialog", { name: "Choose access mode" });
  within(dialog).getByRole("radio", { name: "(default)" }).focus();
  await user.keyboard("{ArrowDown}");
  expect(within(dialog).getByRole("radio", { name: "read-only" }).getAttribute("aria-checked")).toBe("true");

  await user.keyboard("{Escape}");

  expect(screen.queryByRole("dialog", { name: "Choose access mode" })).toBeNull();
  expect(trigger.getAttribute("aria-label")).toBe("Access mode: (default)");
  expect(onAccessChange).not.toHaveBeenCalled();

  await user.click(trigger);
  dialog = await screen.findByRole("dialog", { name: "Choose access mode" });
  await waitFor(() =>
    expect(within(dialog).getByRole("radio", { name: "(default)" }).getAttribute("aria-checked")).toBe("true"),
  );
  expect(within(dialog).getByRole("radio", { name: "read-only" }).getAttribute("aria-checked")).toBe("false");
});

test("browsing a directory and cancelling preserves the session directory", async () => {
  const user = userEvent.setup();
  const onCwdChange = vi.fn();
  renderRows({ onCwdChange, complete: async () => ["/tmp/project/child"] });

  const rowButton = screen.getByRole("button", { name: "Working directory: /tmp/project" });
  await user.click(rowButton);
  const dialog = await screen.findByRole("dialog", { name: "Choose directory" });
  await user.click(await within(dialog).findByText("child"));
  expect(onCwdChange).not.toHaveBeenCalled();
  await user.keyboard("{Escape}");

  expect(screen.queryByRole("dialog", { name: "Choose directory" })).toBeNull();
  expect(document.activeElement).toBe(rowButton);
});

test("selecting a recent working directory stamps the committed value, not the previous cwd", async () => {
  const user = userEvent.setup();
  localStorage.setItem(GLOBAL_LAST_WORKING_DIR_KEY, "/old/project");
  const onCwdChange = vi.fn();
  renderRows({
    cwd: "/old/project",
    onCwdChange,
    listRecents: vi.fn(async () => ["/new/project"]),
    onCwdPanelClose: setGlobalLastWorkingDir,
  });

  await user.click(screen.getByRole("button", { name: "Working directory: /old/project" }));
  const dialog = await screen.findByRole("dialog", { name: "Choose directory" });
  await user.click(await within(dialog).findByRole("button", { name: "Open recent /new/project" }));

  expect(onCwdChange).not.toHaveBeenCalled();
  await user.click(within(dialog).getByRole("button", { name: "Use this folder" }));
  expect(onCwdChange).toHaveBeenLastCalledWith("/new/project");
  expect(localStorage.getItem(GLOBAL_LAST_WORKING_DIR_KEY)).toBe("/new/project");
  expect(localStorage.getItem(GLOBAL_LAST_WORKING_DIR_KEY)).not.toBe("/old/project");
});

test("confirming a typed working directory stamps the committed value, not the previous cwd", async () => {
  const user = userEvent.setup();
  localStorage.setItem(GLOBAL_LAST_WORKING_DIR_KEY, "/old/project");
  const onCwdChange = vi.fn();
  renderRows({
    cwd: "/old/project",
    onCwdChange,
    onCwdPanelClose: setGlobalLastWorkingDir,
  });

  await user.click(screen.getByRole("button", { name: "Working directory: /old/project" }));
  const dialog = await screen.findByRole("dialog", { name: "Choose directory" });
  const pathInput = within(dialog).getByRole("textbox", { name: "Path" });
  await user.clear(pathInput);
  await user.type(pathInput, "/new/typed/project{Enter}");
  await user.click(within(dialog).getByRole("button", { name: "Use this folder" }));

  expect(onCwdChange).toHaveBeenLastCalledWith("/new/typed/project");
  expect(localStorage.getItem(GLOBAL_LAST_WORKING_DIR_KEY)).toBe("/new/typed/project");
  expect(localStorage.getItem(GLOBAL_LAST_WORKING_DIR_KEY)).not.toBe("/old/project");
});

// Issue #198 (extended): effort is one act too, so it uses one control
// everywhere - the prompt card's quiet effort control. This list renders no
// effort row and opens no effort sheet.
test("no reasoning row and no reasoning sheet - the prompt card owns that setting now", () => {
  renderRows();

  const labels = within(screen.getByTestId("mobile-spawn-config"))
    .getAllByTestId("mobile-spawn-row")
    .map((row) => row.getAttribute("data-label"));
  expect(labels).not.toContain("Reasoning effort");
  expect(screen.queryByRole("button", { name: /^Reasoning effort:/ })).toBeNull();
  expect(screen.queryByRole("dialog", { name: "Choose reasoning effort" })).toBeNull();
});

test("plugin sheet stays open across toggles, Done applies, and Cancel restores focus", async () => {
  const user = userEvent.setup();
  const onPluginSelectionChange = vi.fn();
  const pluginPreview: PluginPreviewResponse = {
    plugins: [
      {
        name: "alpha",
        source: "installed",
        selected: true,
        skillCount: 1,
        agentCount: 0,
        commandCount: 0,
        hookCount: 0,
        mcpCount: 0,
      },
      {
        name: "beta",
        source: "directory",
        path: "/tmp/beta",
        selected: true,
        skillCount: 0,
        agentCount: 0,
        commandCount: 1,
        hookCount: 0,
        mcpCount: 0,
      },
    ],
  };
  renderRows({ pluginPreview: { status: "ready", response: pluginPreview }, onPluginSelectionChange });

  const rowButton = screen.getByRole("button", { name: "Plugins: 2 of 2" });
  await user.click(rowButton);
  const dialog = await screen.findByRole("dialog", { name: "Plugins for this session" });
  const alpha = within(dialog).getByRole("switch", { name: "alpha" });
  await user.click(alpha);
  expect(screen.getByRole("dialog", { name: "Plugins for this session" })).toBeTruthy();
  await user.click(within(dialog).getByRole("button", { name: "Done" }));
  expect(onPluginSelectionChange).toHaveBeenCalledWith({ mode: "explicit", names: ["beta"] });
  expect(document.activeElement).toBe(rowButton);

  await user.click(rowButton);
  const secondDialog = await screen.findByRole("dialog", { name: "Plugins for this session" });
  await user.click(within(secondDialog).getByRole("switch", { name: "beta" }));
  await user.click(within(secondDialog).getByRole("button", { name: "Cancel" }));
  expect(onPluginSelectionChange).toHaveBeenCalledTimes(1);
  expect(document.activeElement).toBe(rowButton);
});

test("plugin Details expand inline and preserve the mobile selection draft", async () => {
  const user = userEvent.setup();
  const onPluginSelectionChange = vi.fn();
  const pluginClient = new FakeClient();
  pluginClient.on("evener/plugin/components", (params) => ({
    results: params.targets.map((target) => ({ ...target, components: { skills: ["draft-skill"] } })),
  }));
  renderRows({
    pluginClient,
    onPluginSelectionChange,
    pluginPreview: {
      status: "ready",
      response: {
        plugins: [
          {
            name: "alpha",
            source: "directory",
            path: "/tmp/alpha",
            selected: true,
            skillCount: 1,
            agentCount: 0,
            commandCount: 0,
            hookCount: 0,
            mcpCount: 0,
          },
        ],
      },
    },
  });

  await user.click(screen.getByRole("button", { name: "Plugins: 1 of 1" }));
  const parent = screen.getByRole("dialog", { name: "Plugins for this session" });
  await user.click(within(parent).getByRole("switch", { name: "alpha" }));
  await user.click(within(parent).getByRole("button", { name: "Details" }));
  // The details expand inline under the row - no second dialog opens over the
  // selection sheet.
  const details = document.querySelector('[data-kind="plugin-detail"]');
  if (!(details instanceof HTMLElement)) throw new Error("no detail body expanded");
  expect(screen.queryByRole("dialog", { name: "alpha" })).toBeNull();
  const included = within(details).getByRole("switch", { name: "Included in this session" });
  expect(included.getAttribute("aria-checked")).toBe("false");
  await user.click(within(details).getByRole("button", { name: "Hide details" }));

  expect(document.querySelector('[data-kind="plugin-detail"]')).toBeNull();
  expect(screen.getByRole("dialog", { name: "Plugins for this session" })).toBe(parent);
  expect(within(parent).getByRole("switch", { name: "alpha" }).getAttribute("aria-checked")).toBe("false");
  expect(onPluginSelectionChange).not.toHaveBeenCalled();
  await user.click(within(parent).getByRole("button", { name: "Done" }));
  expect(onPluginSelectionChange).toHaveBeenCalledExactlyOnceWith({ mode: "explicit", names: [] });
});

test("Escape right after opening details preserves the mobile selection draft", async () => {
  const user = userEvent.setup();
  const onPluginSelectionChange = vi.fn();
  renderRows({
    onPluginSelectionChange,
    pluginPreview: {
      status: "ready",
      response: {
        plugins: [
          {
            name: "alpha",
            source: "directory",
            path: "/tmp/alpha",
            selected: true,
            skillCount: 1,
            agentCount: 0,
            commandCount: 0,
            hookCount: 0,
            mcpCount: 0,
          },
        ],
      },
    },
  });

  await user.click(screen.getByRole("button", { name: "Plugins: 1 of 1" }));
  const parent = screen.getByRole("dialog", { name: "Plugins for this session" });
  // A draft change, then Details - the keyboard never leaves the row.
  await user.click(within(parent).getByRole("switch", { name: "alpha" }));
  await user.click(within(parent).getByRole("button", { name: "Details" }));
  expect(document.querySelector('[data-kind="plugin-detail"]')).not.toBeNull();

  await user.keyboard("{Escape}");

  // Escape dismisses the details the row opened, not the sheet over the draft.
  expect(document.querySelector('[data-kind="plugin-detail"]')).toBeNull();
  expect(screen.getByRole("dialog", { name: "Plugins for this session" })).toBe(parent);
  expect(within(parent).getByRole("switch", { name: "alpha" }).getAttribute("aria-checked")).toBe("false");
  await user.click(within(parent).getByRole("button", { name: "Done" }));
  expect(onPluginSelectionChange).toHaveBeenCalledExactlyOnceWith({ mode: "explicit", names: [] });
});

test("contents expansion inside the details keeps the keyboard local to the sheet's draft", async () => {
  const user = userEvent.setup();
  const onPluginSelectionChange = vi.fn();
  const pluginClient = new FakeClient();
  const nineSkills = Array.from({ length: 9 }, (_, i) => `skill-0${i + 1}`);
  pluginClient.on("evener/plugin/components", (params) => ({
    results: params.targets.map((target) => ({ ...target, components: { skills: nineSkills } })),
  }));
  renderRows({
    pluginClient,
    onPluginSelectionChange,
    pluginPreview: {
      status: "ready",
      response: {
        plugins: [
          {
            name: "alpha",
            source: "directory",
            path: "/tmp/alpha",
            selected: true,
            skillCount: 9,
            agentCount: 0,
            commandCount: 0,
            hookCount: 0,
            mcpCount: 0,
          },
        ],
      },
    },
  });

  await user.click(screen.getByRole("button", { name: "Plugins: 1 of 1" }));
  const parent = screen.getByRole("dialog", { name: "Plugins for this session" });
  await user.click(within(parent).getByRole("button", { name: "Details" }));
  const details = document.querySelector('[data-kind="plugin-detail"]');
  if (!(details instanceof HTMLElement)) throw new Error("no detail body expanded");
  // The contents block appears when the components read settles.
  const expander = await within(details).findByRole("button", { name: "+1 more" });
  expander.focus();
  await user.keyboard("{Enter}");
  expect(expander.isConnected).toBe(false);

  // The expansion rehomes focus to the contents row inside the details - not
  // to the sheet's first control, where the next Escape would cancel the sheet.
  expect(details.contains(document.activeElement)).toBe(true);
  await user.keyboard("{Escape}");

  expect(document.querySelector('[data-kind="plugin-detail"]')).toBeNull();
  expect(screen.getByRole("dialog", { name: "Plugins for this session" })).toBe(parent);
  await user.click(within(parent).getByRole("button", { name: "Done" }));
  expect(onPluginSelectionChange).toHaveBeenCalledExactlyOnceWith({ mode: "default" });
});

test("retrying the contents read inside the details keeps the keyboard local to the sheet's draft", async () => {
  const user = userEvent.setup();
  const onPluginSelectionChange = vi.fn();
  const pluginClient = new FakeClient();
  pluginClient.on("evener/plugin/components", () => {
    throw new Error("read-failed");
  });
  renderRows({
    pluginClient,
    onPluginSelectionChange,
    pluginPreview: {
      status: "ready",
      response: {
        plugins: [
          {
            name: "alpha",
            source: "directory",
            path: "/tmp/alpha",
            selected: true,
            skillCount: 1,
            agentCount: 0,
            commandCount: 0,
            hookCount: 0,
            mcpCount: 0,
          },
        ],
      },
    },
  });

  await user.click(screen.getByRole("button", { name: "Plugins: 1 of 1" }));
  const parent = screen.getByRole("dialog", { name: "Plugins for this session" });
  await user.click(within(parent).getByRole("button", { name: "Details" }));
  const details = document.querySelector('[data-kind="plugin-detail"]');
  if (!(details instanceof HTMLElement)) throw new Error("no detail body expanded");
  const retry = await within(details).findByRole("button", { name: "Retry" });
  retry.focus();
  await user.keyboard("{Enter}");
  expect(retry.isConnected).toBe(false);

  // The retry swaps the block for loading content; the rehome stays inside the
  // details rather than jumping to the sheet's first control, where the next
  // Escape would cancel the sheet over the draft.
  expect(details.contains(document.activeElement)).toBe(true);
  await user.keyboard("{Escape}");

  expect(document.querySelector('[data-kind="plugin-detail"]')).toBeNull();
  expect(screen.getByRole("dialog", { name: "Plugins for this session" })).toBe(parent);
  await user.click(within(parent).getByRole("button", { name: "Done" }));
  expect(onPluginSelectionChange).toHaveBeenCalledExactlyOnceWith({ mode: "default" });
});

test("Escape inside the expanded details collapses them without closing the selection sheet", async () => {
  const user = userEvent.setup();
  const onPluginSelectionChange = vi.fn();
  const pluginClient = new FakeClient();
  pluginClient.on("evener/plugin/components", (params) => ({
    results: params.targets.map((target) => ({ ...target, components: { skills: ["draft-skill"] } })),
  }));
  renderRows({
    pluginClient,
    onPluginSelectionChange,
    pluginPreview: {
      status: "ready",
      response: {
        plugins: [
          {
            name: "alpha",
            source: "directory",
            path: "/tmp/alpha",
            selected: true,
            skillCount: 1,
            agentCount: 0,
            commandCount: 0,
            hookCount: 0,
            mcpCount: 0,
          },
        ],
      },
    },
  });

  await user.click(screen.getByRole("button", { name: "Plugins: 1 of 1" }));
  const parent = screen.getByRole("dialog", { name: "Plugins for this session" });
  await user.click(within(parent).getByRole("button", { name: "Details" }));
  const details = document.querySelector('[data-kind="plugin-detail"]');
  if (!(details instanceof HTMLElement)) throw new Error("no detail body expanded");
  // A draft change made from inside the details, with the keyboard still in
  // the body: deselecting the candidate through its own switch.
  const included = within(details).getByRole("switch", { name: "Included in this session" });
  await user.click(included);
  expect(included.getAttribute("aria-checked")).toBe("false");

  await user.keyboard("{Escape}");

  // Escape dismisses the details, not the sheet over the draft.
  expect(document.querySelector('[data-kind="plugin-detail"]')).toBeNull();
  expect(screen.getByRole("dialog", { name: "Plugins for this session" })).toBe(parent);
  expect(within(parent).getByRole("switch", { name: "alpha" }).getAttribute("aria-checked")).toBe("false");
  await user.click(within(parent).getByRole("button", { name: "Done" }));
  expect(onPluginSelectionChange).toHaveBeenCalledExactlyOnceWith({ mode: "explicit", names: [] });
});

test("plugin preview error allows only safe removal from the last preview and exposes retry", async () => {
  const user = userEvent.setup();
  const onPluginRetry = vi.fn();
  const onPluginSelectionChange = vi.fn();
  const response: PluginPreviewResponse = {
    plugins: [
      {
        name: "alpha",
        source: "installed",
        selected: true,
        skillCount: 1,
        agentCount: 0,
        commandCount: 0,
        hookCount: 0,
        mcpCount: 0,
      },
      {
        name: "beta",
        source: "installed",
        selected: false,
        skillCount: 0,
        agentCount: 0,
        commandCount: 0,
        hookCount: 0,
        mcpCount: 0,
      },
    ],
    selectionErrors: [{ name: "alpha", reason: "plugin is unavailable" }],
  };
  renderRows({
    pluginPreview: { status: "error", message: "offline", response },
    pluginSelection: { mode: "explicit", names: ["alpha"] },
    onPluginSelectionChange,
    onPluginRetry,
  });

  const pluginRow = screen.getByTestId("mobile-spawn-config").querySelector('[data-label="Plugins"]') as HTMLElement;
  expect(pluginRow.dataset.state).toBe("error");
  await user.click(pluginRow);
  const dialog = await screen.findByRole("dialog", { name: "Plugins for this session" });
  expect((within(dialog).getByRole("switch", { name: "alpha" }) as HTMLButtonElement).disabled).toBe(false);
  expect((within(dialog).getByRole("switch", { name: "beta" }) as HTMLButtonElement).disabled).toBe(true);
  expect((within(dialog).getByRole("button", { name: "All" }) as HTMLButtonElement).disabled).toBe(true);
  await user.click(within(dialog).getByRole("button", { name: "None" }));
  expect(within(dialog).queryByRole("alert")).toBeNull();
  await user.click(within(dialog).getByRole("button", { name: "Done" }));
  expect(onPluginSelectionChange).toHaveBeenCalledWith({ mode: "explicit", names: [] });
  await user.click(screen.getByRole("button", { name: "Retry" }));
  expect(onPluginRetry).toHaveBeenCalledOnce();
  expect(screen.queryByText("0 of 0")).toBeNull();
});

test("the mobile setting value keeps the approved body-text baseline", () => {
  const css = readModuleCss(import.meta.url, "MobileSettingRows.module.css");
  const valueRule = css.match(/\.rowValue\s*\{([\s\S]*?)\n\}/)?.[1];

  expect(valueRule).toContain("font-size: var(--font-size-body)");
  expect(valueRule).not.toContain("font-size: var(--font-size-ui)");
});
