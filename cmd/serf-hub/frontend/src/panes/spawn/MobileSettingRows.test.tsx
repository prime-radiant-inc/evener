import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import type { ModelCatalog } from "../../widgets";
import { MobileSettingRows, type MobileSettingRowsProps } from "./MobileSettingRows";

afterEach(cleanup);

const catalog: ModelCatalog = {
  models: [
    {
      provider: "anthropic",
      model: "claude-sonnet-4-5",
      displayName: "Fast model",
      supportsTools: true,
    },
  ],
  recent: [],
};

function props(overrides: Partial<MobileSettingRowsProps> = {}): MobileSettingRowsProps {
  return {
    harness: "serf",
    harnessOptions: [
      { value: "serf", label: "serf" },
      { value: "codex-cli", label: "codex-cli" },
    ],
    onHarnessChange: vi.fn(),
    model: "",
    modelDisplay: "(default)",
    modelRequired: false,
    loadCatalog: vi.fn(async () => catalog),
    onModelChange: vi.fn(),
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
    "Model",
    "Working directory",
    "Branch",
    "Reasoning effort",
    "Access mode",
  ]);
  expect(within(rows[0]!).getByRole("button", { name: /harness/i })).toBeTruthy();
  expect(within(rows[3]!).queryByRole("button")).toBeNull();
});

test("option sheets commit a selection and return focus to the row", async () => {
  const user = userEvent.setup();
  const onHarnessChange = vi.fn();
  renderRows({ onHarnessChange });

  const row = screen.getByTestId("mobile-spawn-row-Harness");
  await user.click(within(row).getByRole("button"));
  const dialog = await screen.findByRole("dialog", { name: "Choose harness" });
  await user.click(within(dialog).getByRole("button", { name: "codex-cli" }));

  expect(onHarnessChange).toHaveBeenCalledWith("codex-cli");
  expect(screen.queryByRole("dialog", { name: "Choose harness" })).toBeNull();
  expect(document.activeElement).toBe(within(row).getByRole("button"));
});

test("the model sheet uses the existing searchable catalog panel", async () => {
  const user = userEvent.setup();
  const onModelChange = vi.fn();
  renderRows({ onModelChange });

  await user.click(within(screen.getByTestId("mobile-spawn-row-Model")).getByRole("button"));
  const dialog = await screen.findByRole("dialog", { name: "Choose model" });
  expect(within(dialog).getByRole("combobox", { name: "Model" })).toBeTruthy();
  await user.click(within(dialog).getByRole("option", { name: /Fast model/ }));

  expect(onModelChange).toHaveBeenCalledWith("anthropic/claude-sonnet-4-5");
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "Choose model" })).toBeNull());
});

test("the working-directory sheet uses the existing path panel and closes with Escape", async () => {
  const user = userEvent.setup();
  const onCwdChange = vi.fn();
  renderRows({ onCwdChange });

  const row = screen.getByTestId("mobile-spawn-row-Working directory");
  await user.click(within(row).getByRole("button"));
  const dialog = await screen.findByRole("dialog", { name: "Choose working directory" });
  expect(within(dialog).getByRole("combobox", { name: "Path" })).toBeTruthy();
  await user.keyboard("{Escape}");

  expect(screen.queryByRole("dialog", { name: "Choose working directory" })).toBeNull();
  expect(document.activeElement).toBe(within(row).getByRole("button"));
});
