// Edge cases for LaunchConfigForm.tsx that close remaining uncovered lines:
// - updateList (line 106)
// - updateExplicitEmpty (lines 109-110)
// - updateEnvMap (lines 112-113)
// - updateMcpList (lines 115)
// - renderOption pathList branch (line 192)
// - renderOption modelList branch (lines 201-203)
// - renderOption envMap branch (line 211)
// - renderOption mcpServerList branch (lines 219-220)

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type {
  LaunchConfigLayer,
  LaunchConfigResolved,
  LaunchOption,
  PathValidateResponse,
} from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { resetExtensionsStoreForTests } from "../../../../stores/extensions";
import { resetToastStoreForTests } from "../../../../widgets/toast/store";
import { LaunchConfigForm } from "./LaunchConfigForm";

afterEach(() => {
  cleanup();
  resetToastStoreForTests();
  vi.useRealTimers();
});

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  vi.useRealTimers();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
  connectFakeClient();
});

const OK_VALIDATE = async (path: string): Promise<PathValidateResponse> => ({ path, valid: true });
const RESOLVED: LaunchConfigResolved = { effective: {}, layers: {}, provenance: {} };

const PATH_LIST_OPT: LaunchOption = {
  field: "skills_dirs",
  wireField: "skillsDirs",
  label: "Skill directories",
  group: "Resources",
  kind: "pathList",
  pathKind: "dir",
  perLaunch: true,
  defaultableLayers: ["global", "project"],
};

const MODEL_LIST_OPT: LaunchOption = {
  field: "model_fallbacks",
  wireField: "modelFallbacks",
  label: "Model fallbacks",
  group: "Resources",
  kind: "modelList",
  perLaunch: true,
  defaultableLayers: ["global", "project"],
};

const ENV_OPT: LaunchOption = {
  field: "env",
  wireField: "env",
  label: "Environment variables",
  group: "Environment",
  kind: "envMap",
  perLaunch: true,
  defaultableLayers: ["global", "project"],
};

const MCP_OPT: LaunchOption = {
  field: "mcps",
  wireField: "mcps",
  label: "MCP servers",
  group: "Resources",
  kind: "mcpServerList",
  perLaunch: true,
  defaultableLayers: ["global", "project"],
};

function renderForm(opts: LaunchOption[], current: LaunchConfigLayer = {}) {
  const onSave = vi.fn().mockResolvedValue(RESOLVED);
  const view = render(
    <LaunchConfigForm
      options={opts}
      layer="global"
      current={current}
      successToast="Launch defaults saved"
      validatePath={OK_VALIDATE}
      onSave={onSave}
    />,
  );
  return { ...view, onSave };
}

// --- renderOption pathList branch (line 192) ---

test("pathList field allows removing entries", async () => {
  const user = userEvent.setup();
  const { onSave } = renderForm([PATH_LIST_OPT], { skillsDirs: ["/opt/skills"] });
  const removeBtn = screen.getByRole("button", { name: /Remove \/opt\/skills/i });
  await user.click(removeBtn);
  await user.click(screen.getByRole("button", { name: "Save launch defaults" }));

  await waitFor(() => expect(onSave).toHaveBeenCalledWith({}));
});

// --- renderOption envMap branch (line 211) ---

test("envMap field allows removing entries", async () => {
  const user = userEvent.setup();
  const { onSave } = renderForm([ENV_OPT], { env: { FOO: "bar" } });
  const removeBtn = screen.getByRole("button", { name: /Remove FOO/i });
  await user.click(removeBtn);
  await user.click(screen.getByRole("button", { name: "Save launch defaults" }));

  await waitFor(() => expect(onSave).toHaveBeenCalledWith({}));
});

// --- renderOption mcpServerList branch (lines 219-220) ---

test("mcpServerList field allows removing entries", async () => {
  const user = userEvent.setup();
  const { onSave } = renderForm([MCP_OPT], {
    mcps: [{ name: "myserver", command: "npx", args: ["-y", "server"] }],
  });
  const removeBtn = screen.getByRole("button", { name: /Remove myserver/i });
  await user.click(removeBtn);
  await user.click(screen.getByRole("button", { name: "Save launch defaults" }));

  await waitFor(() => expect(onSave).toHaveBeenCalledWith({}));
});

// --- renderOption modelList with explicitEmpty (lines 201-203) ---

test("modelList explicit-empty switch serializes an intentional empty list", async () => {
  const user = userEvent.setup();
  const { onSave } = renderForm([MODEL_LIST_OPT]);

  await user.click(screen.getByRole("switch", { name: "No model fallbacks" }));
  await user.click(screen.getByRole("button", { name: "Save launch defaults" }));

  await waitFor(() => expect(onSave).toHaveBeenCalledWith({ modelFallbacks: [] }));
});
