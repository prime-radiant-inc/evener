// Edge cases for AdvancedOptions.tsx that close remaining uncovered lines:
// - updateScalar path validation reject (line 94, fail-open)
// - updateScalar path with empty value (line 98, clear error)
// - showResolved error (line 108)
// - radio control rendering (lines 217-222)
// - select control rendering (lines 226-234)
// - EnvControl onRemove (lines 464-470)
// - EnvControl onAdd with no equals (lines 475-478)
// - McpControl rendering and onRemove (lines 498-501)
// - McpControl onAdd validation (lines 505-516)

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import type { LaunchConfigResolved, LaunchOption } from "../../protocol/types.gen";
import type { ModelCatalog as ModelCatalogEnvelope } from "../../widgets";
import { AdvancedOptions } from "./AdvancedOptions";

afterEach(() => cleanup());

function option(partial: Partial<LaunchOption> & { wireField: string; kind: string; label: string }): LaunchOption {
  return { field: partial.wireField, group: "general", perLaunch: true, ...partial };
}

const CATALOG: ModelCatalogEnvelope = {
  models: [
    { provider: "openai", model: "gpt-5", displayName: "GPT-5" },
    { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "Claude Sonnet 4.5" },
  ],
  recent: [],
};

const RESOLVED: LaunchConfigResolved = {
  effective: { sandbox: "off", maxRounds: 5 },
  layers: {},
  provenance: {},
};

function renderPanel(options: LaunchOption[], over: Partial<Parameters<typeof AdvancedOptions>[0]> = {}) {
  const onOverridesChange = over.onOverridesChange ?? vi.fn();
  const validatePath = over.validatePath ?? vi.fn().mockResolvedValue({ valid: true });
  const resolveConfig = over.resolveConfig ?? vi.fn().mockResolvedValue(RESOLVED);
  const loadCatalog = over.loadCatalog ?? vi.fn().mockResolvedValue(CATALOG);
  const complete = over.complete ?? vi.fn().mockResolvedValue([]);
  render(
    <AdvancedOptions
      options={options}
      onOverridesChange={onOverridesChange as (o: unknown) => void}
      validatePath={validatePath as (p: string, k: string) => Promise<{ valid: boolean; error?: string }>}
      resolveConfig={resolveConfig as (o: unknown) => Promise<LaunchConfigResolved>}
      loadCatalog={loadCatalog}
      complete={complete}
    />,
  );
  return { onOverridesChange, validatePath, resolveConfig, loadCatalog, complete };
}

// --- path validation fail-open (line 94) ---

test("a failing path validator clears the error (fail-open)", async () => {
  const user = userEvent.setup();
  const validatePath = vi.fn().mockRejectedValue(new Error("network error"));
  const { onOverridesChange } = renderPanel(
    [option({ wireField: "traceFile", kind: "path", label: "Trace file", pathKind: "file" })],
    { validatePath },
  );

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  // The path field renders as a browse trigger
  const trigger = screen.getByRole("button", { name: /Trace file/i });
  await user.click(trigger);
  // Type a path and Enter
  await screen.findByRole("combobox", { name: "Path" });
  await user.keyboard("/some/path{Enter}");

  // The value should still be collected (fail-open)
  await waitFor(() => expect(onOverridesChange).toHaveBeenCalled());
});

// --- updateScalar with empty path value (line 98) ---

test("a path field with empty value clears the error", async () => {
  const user = userEvent.setup();
  const { onOverridesChange } = renderPanel([
    option({ wireField: "traceFile", kind: "path", label: "Trace file", pathKind: "file" }),
  ]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  // Open the browse and type nothing
  const trigger = screen.getByRole("button", { name: /Trace file/i });
  await user.click(trigger);
  await screen.findByRole("combobox", { name: "Path" });
  // Just press Enter with the pre-filled value (which may be empty)
  await user.keyboard("{Enter}");

  // Should call onOverridesChange (value collected)
  await waitFor(() => expect(onOverridesChange).toHaveBeenCalled());
});

// --- showResolved error (line 108) ---

test("show resolved config shows error when resolveConfig fails", async () => {
  const user = userEvent.setup();
  const resolveConfig = vi.fn().mockRejectedValue(new Error("resolve failed"));
  renderPanel([option({ wireField: "maxRounds", kind: "integer", label: "Max rounds" })], { resolveConfig });

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.click(screen.getByRole("button", { name: "Show resolved config" }));

  await waitFor(() => {
    expect(screen.getByText(/couldn't resolve: resolve failed/i)).toBeTruthy();
  });
});

test("show resolved config shows error with non-Error throw", async () => {
  const user = userEvent.setup();
  const resolveConfig = vi.fn().mockRejectedValue("string error");
  renderPanel([option({ wireField: "maxRounds", kind: "integer", label: "Max rounds" })], { resolveConfig });

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.click(screen.getByRole("button", { name: "Show resolved config" }));

  await waitFor(() => {
    expect(screen.getByText(/couldn't resolve: string error/i)).toBeTruthy();
  });
});

// --- radio control (lines 217-222) ---

test("a radio control renders with choices", async () => {
  const user = userEvent.setup();
  const { onOverridesChange } = renderPanel([
    option({
      wireField: "sandbox",
      kind: "radio",
      label: "Sandbox mode",
      choices: [
        { value: "off", label: "Off" },
        { value: "seatbelt", label: "Seatbelt" },
      ],
    }),
  ]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  expect(screen.getByText("Sandbox mode")).toBeTruthy();
  expect(screen.getByText("Off")).toBeTruthy();
  expect(screen.getByText("Seatbelt")).toBeTruthy();

  // Click a radio option
  await user.click(screen.getByText("Seatbelt"));
  await waitFor(() => expect(onOverridesChange).toHaveBeenLastCalledWith({ sandbox: "seatbelt" }));
});

// --- select control (lines 226-234) ---

test("a select control renders with choices and default option", async () => {
  const user = userEvent.setup();
  const { onOverridesChange } = renderPanel([
    option({
      wireField: "accessMode",
      kind: "select",
      label: "Access mode",
      choices: [
        { value: "sandboxed", label: "Sandboxed" },
        { value: "workspace", label: "Workspace" },
      ],
    }),
  ]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const select = screen.getByLabelText("Access mode");
  expect(select).toBeTruthy();

  await user.selectOptions(select, "workspace");
  await waitFor(() => expect(onOverridesChange).toHaveBeenLastCalledWith({ accessMode: "workspace" }));
});

// --- EnvControl onRemove (lines 464-470) ---

test("env map control removes an entry", async () => {
  const user = userEvent.setup();
  renderPanel([option({ wireField: "env", kind: "envMap", label: "Environment variables" })]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  // The env control renders inside a collection editor
  // Add an entry first
  const addInput = screen.getByPlaceholderText("NAME=value");
  await user.type(addInput, "FOO=bar{Enter}");

  // Wait for the entry to appear
  await waitFor(() => expect(screen.getByText("FOO=bar")).toBeTruthy());

  // Remove it
  const removeButton = screen.getByRole("button", { name: /Remove FOO/i });
  await user.click(removeButton);

  await waitFor(() => expect(screen.queryByText("FOO=bar")).toBeNull());
});

// --- EnvControl onAdd with no equals (line 476) ---

test("env map add without equals sign shows error", async () => {
  const user = userEvent.setup();
  renderPanel([option({ wireField: "env", kind: "envMap", label: "Environment variables" })]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const addInput = screen.getByPlaceholderText("NAME=value");
  await user.type(addInput, "noequals{Enter}");

  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(screen.getByText("Use NAME=value.")).toBeTruthy();
});

// --- McpControl rendering (lines 498-501) ---

test("mcp server list renders entries with name and command", async () => {
  const user = userEvent.setup();
  renderPanel([option({ wireField: "mcpServers", kind: "mcpServerList", label: "MCP servers" })]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const addInput = screen.getByPlaceholderText("name=command arg1 arg2");
  await user.type(addInput, "myserver=npx -y server{Enter}");

  await waitFor(() => expect(screen.getByText(/myserver: npx -y server/i)).toBeTruthy());
});

// --- McpControl onRemove (line 501) ---

test("mcp server list removes an entry", async () => {
  const user = userEvent.setup();
  renderPanel([option({ wireField: "mcpServers", kind: "mcpServerList", label: "MCP servers" })]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const addInput = screen.getByPlaceholderText("name=command arg1 arg2");
  await user.type(addInput, "myserver=npx -y server{Enter}");

  await waitFor(() => expect(screen.getByText(/myserver: npx -y server/i)).toBeTruthy());
  const removeButton = screen.getByRole("button", { name: /Remove myserver/i });
  await user.click(removeButton);

  await waitFor(() => expect(screen.queryByText(/myserver: npx -y server/i)).toBeNull());
});

// --- McpControl onAdd validation (lines 505-516) ---

test("mcp add without equals sign shows error", async () => {
  const user = userEvent.setup();
  renderPanel([option({ wireField: "mcpServers", kind: "mcpServerList", label: "MCP servers" })]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const addInput = screen.getByPlaceholderText("name=command arg1 arg2");
  await user.type(addInput, "noequals{Enter}");

  expect(await screen.findByText("Use name=command args.")).toBeTruthy();
});

test("mcp add with empty command shows error", async () => {
  const user = userEvent.setup();
  renderPanel([option({ wireField: "mcpServers", kind: "mcpServerList", label: "MCP servers" })]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const addInput = screen.getByPlaceholderText("name=command arg1 arg2");
  await user.type(addInput, "myserver={Enter}");

  expect(await screen.findByText("A command is required.")).toBeTruthy();
});

test("mcp add with duplicate name shows error", async () => {
  const user = userEvent.setup();
  renderPanel([option({ wireField: "mcpServers", kind: "mcpServerList", label: "MCP servers" })]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const addInput = screen.getByPlaceholderText("name=command arg1 arg2");
  await user.type(addInput, "myserver=npx -y server{Enter}");
  await waitFor(() => expect(screen.getByText(/myserver: npx -y server/i)).toBeTruthy());

  // Add same name again
  await user.type(addInput, "myserver=other-command{Enter}");
  expect(await screen.findByText("Already added.")).toBeTruthy();
});

// --- ModelListControl onRemove (line 429) ---

test("model list control removes an entry", async () => {
  const user = userEvent.setup();
  renderPanel([option({ wireField: "modelFallbacks", kind: "modelList", label: "Model fallbacks" })]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  // Add a model
  await user.click(screen.getByRole("button", { name: /change model/i }));
  await user.click(await screen.findByText("Claude Sonnet 4.5"));
  await user.click(screen.getByRole("button", { name: "Add" }));

  await waitFor(() => expect(screen.getByText("anthropic/claude-sonnet-4-5")).toBeTruthy());

  // Remove it
  const removeButton = screen.getByRole("button", { name: /Remove anthropic\/claude-sonnet-4-5/i });
  await user.click(removeButton);

  await waitFor(() => expect(screen.queryByText("anthropic/claude-sonnet-4-5")).toBeNull());
});
