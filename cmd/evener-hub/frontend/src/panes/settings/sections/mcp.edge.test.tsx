// Edge cases for mcp.tsx uncovered lines:
// - handleAddConfig invalid path (line 113)
// - handleAddConfig setLayer error (line 121)
// - handleAddServer setLaunchLayer error (line 159)
// - handleConfirmRemoveServer error (line 174)
// - ConfirmDialog onCancel (line 311)

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { WireError } from "../../../protocol/errors";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import { connectionStore } from "../../../stores/connection";
import { resetExtensionsStoreForTests } from "../../../stores/extensions";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import { McpSection, type SettingsOverviewLike } from "./mcp";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function overviewHook(overrides: Partial<SettingsOverviewLike> = {}): () => SettingsOverviewLike {
  const state: SettingsOverviewLike = {
    data: null,
    loading: false,
    error: null,
    fetch: vi.fn(async () => {}),
    ...overrides,
  };
  return () => state;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
  resetToastStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

// Line 113: handleAddConfig invalid path returns error
test("adding a config file with invalid path shows error without saving", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/launch/getLayer", () => ({ mcpConfigs: [], mcps: [] }));
  fake.on("evener/path/validate", () => ({ path: "", valid: false, error: "path does not exist" }));
  const setLayerSpy = vi.fn();
  fake.on("evener/launch/setLayer", setLayerSpy);
  fake.on("evener/paths/complete", () => ({ data: [] }));
  render(<McpSection useOverviewStore={overviewHook()} />);
  await screen.findByText("No MCP config files. Add one below.");
  await user.click(screen.getByRole("button", { name: "New config file" }));
  await user.keyboard("/bad/path");
  await user.keyboard("{Enter}");
  const addButtons = screen.getAllByRole("button", { name: "Add" });
  await user.click(addButtons[0]!);
  expect(await screen.findByText("path does not exist")).toBeTruthy();
  expect(setLayerSpy).not.toHaveBeenCalled();
});

// Line 121: handleAddConfig setLayer error
test("adding a config file when setLayer fails returns error to PathListEditor", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/launch/getLayer", () => ({ mcpConfigs: [], mcps: [] }));
  fake.on("evener/path/validate", () => ({ path: "/etc/mcp.json", valid: true }));
  fake.on("evener/launch/setLayer", () => {
    throw new WireError("server error", -1);
  });
  fake.on("evener/paths/complete", () => ({ data: [] }));
  render(<McpSection useOverviewStore={overviewHook()} />);
  await screen.findByText("No MCP config files. Add one below.");
  await user.click(screen.getByRole("button", { name: "New config file" }));
  await user.keyboard("/etc/mcp.json");
  await user.keyboard("{Enter}");
  const addButtons = screen.getAllByRole("button", { name: "Add" });
  await user.click(addButtons[0]!);
  // The add fails — the error text should appear (friendlyErrorMessage converts WireError)
  await screen.findByText("server error");
});

// Line 159: handleAddServer setLaunchLayer error
test("adding an inline server when setLayer fails shows error message", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/launch/getLayer", () => ({ mcpConfigs: [], mcps: [] }));
  fake.on("evener/path/validate", () => ({ path: "/usr/bin/srv", valid: true }));
  fake.on("evener/launch/setLayer", () => {
    throw new WireError("persist failed", -1);
  });
  render(<McpSection useOverviewStore={overviewHook()} />);
  await screen.findByText("No inline MCP servers. Add one below.");
  await user.type(screen.getByPlaceholderText("name"), "srv");
  await user.type(screen.getByPlaceholderText("command"), "/usr/bin/srv");
  const addButtons = screen.getAllByRole("button", { name: "Add" });
  await user.click(addButtons[addButtons.length - 1]!);
  expect(await screen.findByText("persist failed")).toBeTruthy();
});

// Line 174: handleConfirmRemoveServer error toast
test("removing an inline server when setLayer fails shows error toast", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/launch/getLayer", () => ({
    mcpConfigs: [],
    mcps: [{ name: "srv", command: "/usr/bin/srv", args: [] }],
  }));
  fake.on("evener/launch/setLayer", () => {
    throw new WireError("remove denied", -1);
  });
  render(<McpSection useOverviewStore={overviewHook()} />);
  await screen.findByText("srv → /usr/bin/srv");
  await user.click(screen.getByRole("button", { name: "Remove srv" }));
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await waitFor(() => expect(getToasts().map((toast) => toast.text)).toContain("Remove failed: remove denied"));
});

// Line 311: ConfirmDialog onCancel
test("cancelling the remove server dialog does not call setLayer", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/launch/getLayer", () => ({
    mcpConfigs: [],
    mcps: [{ name: "srv", command: "/usr/bin/srv", args: [] }],
  }));
  const setLayerSpy = vi.fn();
  fake.on("evener/launch/setLayer", setLayerSpy);
  render(<McpSection useOverviewStore={overviewHook()} />);
  await screen.findByText("srv → /usr/bin/srv");
  await user.click(screen.getByRole("button", { name: "Remove srv" }));
  expect(screen.getByRole("dialog", { name: "Remove MCP server" })).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Cancel" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(setLayerSpy).not.toHaveBeenCalled();
});
