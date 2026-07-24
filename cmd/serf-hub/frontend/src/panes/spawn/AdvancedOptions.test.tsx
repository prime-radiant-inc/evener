import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import type { LaunchConfigResolved, LaunchOption } from "../../protocol/types.gen";
import type { ModelCatalog as ModelCatalogEnvelope } from "../../widgets";
import { AdvancedOptions } from "./AdvancedOptions";

afterEach(() => cleanup());

function option(partial: Partial<LaunchOption> & { wireField: string; kind: string; label: string }): LaunchOption {
  return { field: partial.wireField, group: "general", perLaunch: true, ...partial };
}

// Every model-valued advanced field renders the shared ModelCatalog picker, so
// the panel needs a catalog loader; this is the smallest real envelope.
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

function renderPanel(
  options: LaunchOption[],
  over: Partial<Parameters<typeof AdvancedOptions>[0]> = {},
  children?: ReactNode,
) {
  const onOverridesChange = over.onOverridesChange ?? vi.fn();
  const validatePath = over.validatePath ?? vi.fn().mockResolvedValue({ valid: true });
  const resolveConfig = over.resolveConfig ?? vi.fn().mockResolvedValue(RESOLVED);
  const loadCatalog = over.loadCatalog ?? vi.fn().mockResolvedValue(CATALOG);
  render(
    <AdvancedOptions
      options={options}
      onOverridesChange={onOverridesChange as (o: unknown) => void}
      validatePath={validatePath as (p: string, k: string) => Promise<{ valid: boolean; error?: string }>}
      resolveConfig={resolveConfig as (o: unknown) => Promise<LaunchConfigResolved>}
      loadCatalog={loadCatalog}
    >
      {children}
    </AdvancedOptions>,
  );
  return { onOverridesChange, validatePath, resolveConfig, loadCatalog };
}

test("is collapsed by default and reveals the panel on toggle", async () => {
  const user = userEvent.setup();
  renderPanel([option({ wireField: "maxRounds", kind: "integer", label: "Max rounds" })]);

  expect(screen.queryByLabelText("Max rounds")).toBeNull();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  expect(screen.getByLabelText("Max rounds")).toBeTruthy();
});

test("a boolean control collects true/false and drops the (default)", async () => {
  const user = userEvent.setup();
  const { onOverridesChange } = renderPanel([
    option({ wireField: "noProjectPrompts", kind: "boolean", label: "No project prompts" }),
  ]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.selectOptions(screen.getByLabelText("No project prompts"), "On");

  expect(onOverridesChange).toHaveBeenLastCalledWith({ noProjectPrompts: true });
});

test("an integer control collects a parsed number", async () => {
  const user = userEvent.setup();
  const { onOverridesChange } = renderPanel([option({ wireField: "maxRounds", kind: "integer", label: "Max rounds" })]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.type(screen.getByLabelText("Max rounds"), "7");

  await waitFor(() => expect(onOverridesChange).toHaveBeenLastCalledWith({ maxRounds: 7 }));
});

test("a failing path validation flags the field invalid so it is dropped from the overrides", async () => {
  const user = userEvent.setup();
  const validatePath = vi.fn().mockResolvedValue({ valid: false, error: "path is a directory" });
  const { onOverridesChange } = renderPanel(
    [option({ wireField: "systemPromptFile", kind: "text", label: "System prompt file", pathKind: "file" })],
    { validatePath },
  );

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.type(screen.getByLabelText("System prompt file"), "/etc");

  expect(await screen.findByText("path is a directory")).toBeTruthy();
  await waitFor(() => expect(validatePath).toHaveBeenCalledWith("/etc", "file"));
  // Collected overrides must not include the invalid field.
  await waitFor(() => expect(onOverridesChange).toHaveBeenLastCalledWith({}));
});

test("show resolved config previews the effective launch config", async () => {
  const user = userEvent.setup();
  renderPanel([option({ wireField: "maxRounds", kind: "integer", label: "Max rounds" })]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.click(screen.getByRole("button", { name: "Show resolved config" }));

  const pre = await screen.findByLabelText("Resolved config");
  expect(pre.textContent).toContain('"sandbox": "off"');
  expect(pre.textContent).toContain('"maxRounds": 5');
});

test("a pathList control adds and collects entries", async () => {
  const user = userEvent.setup();
  const { onOverridesChange } = renderPanel([
    option({ wireField: "skillsDirs", kind: "pathList", label: "Skills dirs" }),
  ]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const field = screen.getByRole("textbox", { name: "Skills dirs" });
  await user.type(field, "/opt/skills");
  await user.keyboard("{Enter}");

  await waitFor(() => expect(onOverridesChange).toHaveBeenLastCalledWith({ skillsDirs: ["/opt/skills"] }));
});

test("renders children inside the expanded panel, before any schema control (9ct0)", async () => {
  const user = userEvent.setup();
  renderPanel(
    [option({ wireField: "maxRounds", kind: "integer", label: "Max rounds" })],
    {},
    <div data-testid="child-slot">hi</div>,
  );

  expect(screen.queryByTestId("child-slot")).toBeNull(); // panel is collapsed by default
  await user.click(screen.getByRole("button", { name: "Advanced options" }));

  const toggleButton = screen.getByRole("button", { name: "Advanced options" });
  const panelId = toggleButton.getAttribute("aria-controls");
  if (!panelId) throw new Error("expected aria-controls on the toggle");
  const panel = document.getElementById(panelId);
  if (!panel) throw new Error("expected the expanded panel to be in the document");

  // The child slot is the panel's first child, ahead of the schema controls.
  expect(panel.firstElementChild).toBe(screen.getByTestId("child-slot"));
});

// --- model-valued fields all use the shared searchable picker ---------------

test("a modelPicker field renders the shared model picker, not a free-text box", async () => {
  const user = userEvent.setup();
  const { onOverridesChange } = renderPanel([
    option({ wireField: "fastCheapModel", kind: "modelPicker", label: "Fast cheap model" }),
  ]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));

  expect(screen.getByText("Fast cheap model")).toBeTruthy();
  expect(screen.queryByRole("textbox")).toBeNull();
  await user.click(screen.getByRole("button", { name: /change model/i }));
  await user.click(await screen.findByText("GPT-5"));

  await waitFor(() => expect(onOverridesChange).toHaveBeenCalledWith({ fastCheapModel: "openai/gpt-5" }));
});

test("a modelList field adds from the picker instead of a hand-typed provider/model", async () => {
  const user = userEvent.setup();
  const { onOverridesChange } = renderPanel([
    option({ wireField: "modelFallbacks", kind: "modelList", label: "Model fallbacks" }),
  ]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.click(screen.getByRole("button", { name: /change model/i }));
  await user.click(await screen.findByText("Claude Sonnet 4.5"));
  await user.click(screen.getByRole("button", { name: "Add" }));

  await waitFor(() =>
    expect(onOverridesChange).toHaveBeenCalledWith({ modelFallbacks: ["anthropic/claude-sonnet-4-5"] }),
  );
});

test("a modelList field rejects a model already in the list", async () => {
  const user = userEvent.setup();
  const { onOverridesChange } = renderPanel([
    option({ wireField: "modelFallbacks", kind: "modelList", label: "Model fallbacks" }),
  ]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  async function addSonnet() {
    await user.click(screen.getByRole("button", { name: /change model/i }));
    await user.click(await screen.findByText("Claude Sonnet 4.5"));
    await user.click(screen.getByRole("button", { name: "Add" }));
  }
  await addSonnet();
  await waitFor(() => expect(onOverridesChange).toHaveBeenCalled());
  vi.mocked(onOverridesChange as (o: unknown) => void).mockClear();
  await addSonnet();

  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(onOverridesChange).not.toHaveBeenCalled();
});
