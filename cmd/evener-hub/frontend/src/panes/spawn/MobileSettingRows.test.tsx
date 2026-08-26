import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import type { PluginPreviewResponse } from "../../protocol/types.gen";
import { MobileSettingRows, type MobileSettingRowsProps } from "./MobileSettingRows";
import { GLOBAL_LAST_WORKING_DIR_KEY, setGlobalLastWorkingDir } from "./spawnDefaults";

afterEach(cleanup);

function props(overrides: Partial<MobileSettingRowsProps> = {}): MobileSettingRowsProps {
  return {
    harness: "evener",
    harnessOptions: [
      { value: "evener", label: "evener" },
      { value: "codex-cli", label: "codex-cli" },
    ],
    onHarnessChange: vi.fn(),
    cwd: "/tmp/project",
    onCwdChange: vi.fn(),
    complete: vi.fn(async () => []),
    listRecents: vi.fn(async () => []),
    fallbackDir: "/tmp",
    onCwdPanelClose: vi.fn(),
    branch: "main",
    reasoningEffort: "",
    reasoningOptions: [
      { value: "", label: "(default)" },
      { value: "low", label: "low" },
    ],
    reasoningDisabled: false,
    onReasoningChange: vi.fn(),
    accessMode: "",
    accessOptions: [
      { value: "", label: "(default)" },
      { value: "workspace-write", label: "workspace write" },
    ],
    onAccessChange: vi.fn(),
    pluginPreview: { status: "ready", response: { plugins: [] } as PluginPreviewResponse },
    pluginSelection: { mode: "default" },
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
    "Reasoning effort",
    "Access mode",
    "Plugins",
  ]);
  expect(within(rows[0]!).getByRole("button", { name: /harness/i })).toBeTruthy();
  // Branch is a read-only readout, not a picker.
  expect(within(rows[2]!).queryByRole("button")).toBeNull();
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

test("option sheets commit a selection and return focus to the row", async () => {
  const user = userEvent.setup();
  const onHarnessChange = vi.fn();
  renderRows({ onHarnessChange });

  const rowButton = screen.getByRole("button", { name: "Harness: evener" });
  const row = rowButton.parentElement!;
  await user.click(rowButton);
  const dialog = await screen.findByRole("dialog", { name: "Choose harness" });
  await user.click(within(dialog).getByRole("button", { name: "codex-cli" }));

  expect(onHarnessChange).toHaveBeenCalledWith("codex-cli");
  expect(screen.queryByRole("dialog", { name: "Choose harness" })).toBeNull();
  expect(document.activeElement).toBe(within(row).getByRole("button"));
});

test("the working-directory sheet uses the existing path panel and closes with Escape", async () => {
  const user = userEvent.setup();
  const onCwdChange = vi.fn();
  renderRows({ onCwdChange });

  const rowButton = screen.getByRole("button", { name: "Working directory: /tmp/project" });
  const row = rowButton.parentElement!;
  await user.click(rowButton);
  const dialog = await screen.findByRole("dialog", { name: "Choose working directory" });
  expect(within(dialog).getByRole("combobox", { name: "Path" })).toBeTruthy();
  await user.keyboard("{Escape}");

  expect(screen.queryByRole("dialog", { name: "Choose working directory" })).toBeNull();
  expect(document.activeElement).toBe(within(row).getByRole("button"));
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
  const dialog = await screen.findByRole("dialog", { name: "Choose working directory" });
  await user.click(await within(dialog).findByRole("option", { name: /new\/project/ }));

  expect(onCwdChange).toHaveBeenLastCalledWith("/new/project");
  expect(localStorage.getItem(GLOBAL_LAST_WORKING_DIR_KEY)).toBe("/new/project");
  expect(localStorage.getItem(GLOBAL_LAST_WORKING_DIR_KEY)).not.toBe("/old/project");
});

test("pressing Enter on a typed working directory stamps the committed value, not the previous cwd", async () => {
  const user = userEvent.setup();
  localStorage.setItem(GLOBAL_LAST_WORKING_DIR_KEY, "/old/project");
  const onCwdChange = vi.fn();
  renderRows({
    cwd: "/old/project",
    onCwdChange,
    onCwdPanelClose: setGlobalLastWorkingDir,
  });

  await user.click(screen.getByRole("button", { name: "Working directory: /old/project" }));
  const dialog = await screen.findByRole("dialog", { name: "Choose working directory" });
  expect(within(dialog).getByRole("combobox", { name: "Path" })).toBeTruthy();
  await user.keyboard("/new/typed/project{Enter}");

  expect(onCwdChange).toHaveBeenLastCalledWith("/new/typed/project");
  expect(localStorage.getItem(GLOBAL_LAST_WORKING_DIR_KEY)).toBe("/new/typed/project");
  expect(localStorage.getItem(GLOBAL_LAST_WORKING_DIR_KEY)).not.toBe("/old/project");
});

test("a disabled reasoning row is read-only and exposes no picker affordance", () => {
  renderRows({ reasoningDisabled: true });

  const row = screen.getByTestId("mobile-spawn-config").querySelector('[data-label="Reasoning effort"]') as HTMLElement;
  expect(row).toBeTruthy();
  expect(within(row).queryByRole("button")).toBeNull();
  expect(row.querySelector('[aria-haspopup="dialog"]')).toBeNull();
  expect(row.textContent).not.toContain("›");
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

test("plugin preview error keeps the row honest and exposes retry", async () => {
  const user = userEvent.setup();
  const onPluginRetry = vi.fn();
  renderRows({ pluginPreview: { status: "error", message: "offline" }, onPluginRetry });

  const pluginRow = screen.getByTestId("mobile-spawn-config").querySelector('[data-label="Plugins"]') as HTMLElement;
  expect(pluginRow.textContent).toContain("Couldn't inspect plugins");
  expect(within(pluginRow).queryByRole("button")).toBeNull();
  await user.click(screen.getByRole("button", { name: "Retry" }));
  expect(onPluginRetry).toHaveBeenCalledOnce();
  expect(screen.queryByText("0 of 0")).toBeNull();
});

test("mobile setting rows keep the approved 48px body-text baseline", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "MobileSettingRows.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const rowRule = css.match(/\.rowButton,\s*\.readOnly\s*\{([\s\S]*?)\n\}/)?.[1];

  expect(rowRule).toContain("min-height: 48px");
  expect(rowRule).toContain("font-size: var(--font-size-body)");
  expect(rowRule).not.toContain("font-size: var(--font-size-ui)");
});
