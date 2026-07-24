import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import type { LaunchConfigResolved, LaunchOption } from "../../protocol/types.gen";
import { AdvancedOptions } from "./AdvancedOptions";

afterEach(() => cleanup());

function option(partial: Partial<LaunchOption> & { wireField: string; kind: string; label: string }): LaunchOption {
  return { field: partial.wireField, group: "general", perLaunch: true, ...partial };
}

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
  render(
    <AdvancedOptions
      options={options}
      onOverridesChange={onOverridesChange as (o: unknown) => void}
      validatePath={validatePath as (p: string, k: string) => Promise<{ valid: boolean; error?: string }>}
      resolveConfig={resolveConfig as (o: unknown) => Promise<LaunchConfigResolved>}
    >
      {children}
    </AdvancedOptions>,
  );
  return { onOverridesChange, validatePath, resolveConfig };
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
