import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import type { PluginPreviewResponse } from "../../protocol/types.gen";
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
  diagnostics: [{ name: "ignored", source: "directory", message: "could not be loaded" }],
  selectionErrors: [{ name: "missing", reason: "no valid current winner" }],
};

function renderPanel(overrides: Partial<React.ComponentProps<typeof PluginSelectionPanel>> = {}) {
  const onSelectionChange = vi.fn();
  render(
    <PluginSelectionPanel
      preview={preview}
      selection={{ mode: "default" }}
      onSelectionChange={onSelectionChange}
      onRetry={vi.fn()}
      {...overrides}
    />,
  );
  return { onSelectionChange };
}

afterEach(cleanup);

test("renders named switches, metadata, counts, and honest selected state", () => {
  renderPanel();

  expect(screen.getByRole("switch", { name: "alpha" }).getAttribute("aria-checked")).toBe("true");
  expect(screen.getByRole("switch", { name: "beta" }).getAttribute("aria-checked")).toBe("false");
  expect(screen.getByText("@ acme")).toBeTruthy();
  expect(screen.getByText("/tmp/beta")).toBeTruthy();
  expect(screen.getByText("2 skills · 1 agent · 1 command")).toBeTruthy();
  expect(screen.getByText("off for session")).toBeTruthy();
  expect(screen.getByText("1 of 2 selected")).toBeTruthy();
});

test("keyboard toggles a switch and materializes explicit selection", async () => {
  const user = userEvent.setup();
  const { onSelectionChange } = renderPanel();
  const beta = screen.getByRole("switch", { name: "beta" });

  beta.focus();
  await user.keyboard(" ");

  expect(onSelectionChange).toHaveBeenCalledWith({ mode: "explicit", names: ["alpha", "beta"] });
});

test("filters by name, source, and description and supports All and None", async () => {
  const user = userEvent.setup();
  const { onSelectionChange } = renderPanel();
  const filter = screen.getByRole("searchbox", { name: "Filter plugins" });

  await user.type(filter, "helpers");
  expect(screen.queryByRole("switch", { name: "alpha" })).toBeNull();
  expect(screen.getByRole("switch", { name: "beta" })).toBeTruthy();
  await user.clear(filter);
  await user.click(screen.getByRole("button", { name: "None" }));
  expect(onSelectionChange).toHaveBeenLastCalledWith({ mode: "explicit", names: [] });
  await user.click(screen.getByRole("button", { name: "All" }));
  expect(onSelectionChange).toHaveBeenLastCalledWith({ mode: "explicit", names: ["alpha", "beta"] });
});

test("exposes diagnostics and blocking selection errors without relying on color", async () => {
  const user = userEvent.setup();
  renderPanel();
  expect(screen.getByRole("alert").textContent).toContain("missing");
  const details = screen.getByText(/preview diagnostic/);
  await user.click(details);
  expect(screen.getByText("could not be loaded")).toBeTruthy();
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
