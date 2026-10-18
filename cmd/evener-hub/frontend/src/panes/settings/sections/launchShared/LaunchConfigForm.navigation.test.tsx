import type {
  LaunchConfigLayer,
  LaunchConfigResolved,
  LaunchOption,
  PathValidateResponse,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { navigate } from "../../../../shell/routing";
import { connectionStore } from "../../../../stores/connection";
import { resetToastStoreForTests } from "../../../../widgets/toast/store";
import { SettingsDraftsProvider } from "../../settingsDrafts";
import { LaunchConfigForm } from "./LaunchConfigForm";

const OPTIONS: LaunchOption[] = [
  {
    field: "agent",
    wireField: "agent",
    label: "Agent",
    group: "Agent",
    kind: "text",
    perLaunch: true,
    defaultableLayers: ["global", "project"],
  },
];
const RESOLVED: LaunchConfigResolved = { effective: {}, layers: {}, provenance: {} };

const INTEGER_OPTIONS: LaunchOption[] = [
  ["max_rounds", "maxRounds", "Max rounds"],
  ["max_subagent_depth", "maxSubagentDepth", "Max subagent depth"],
  ["max_concurrent_delegate_turns", "maxConcurrentDelegateTurns", "Max concurrent delegates"],
  ["max_retained_terminal", "maxRetainedTerminal", "Max retained terminal delegates"],
  ["app_replay_size", "appReplaySize", "App replay size"],
].map(([field, wireField, label]) => ({
  field: field!,
  wireField: wireField!,
  label: label!,
  group: "Limits",
  kind: "integer",
  perLaunch: field !== "app_replay_size",
  defaultableLayers: field === "app_replay_size" ? ["global"] : ["global", "project"],
}));

const PROMPT_OPTIONS: LaunchOption[] = [
  ...OPTIONS,
  ...["systemPrompt", "systemPromptAppend"].flatMap((prefix): LaunchOption[] => [
    {
      field: `${prefix}Mode`,
      wireField: `${prefix}Mode`,
      label: prefix,
      group: "Prompt",
      kind: "radio",
      perLaunch: true,
      defaultableLayers: ["global"],
      choices: [
        { value: "", label: "Default" },
        { value: "file", label: "File" },
        { value: "inline", label: "Inline" },
      ],
    },
    ...["File", "Text"].map(
      (suffix): LaunchOption => ({
        field: `${prefix}${suffix}`,
        wireField: `${prefix}${suffix}`,
        label: `${prefix}${suffix}`,
        group: "Prompt",
        kind: suffix === "File" ? "path" : "multilineText",
        pathKind: suffix === "File" ? "file" : undefined,
        perLaunch: true,
        defaultableLayers: ["global"],
      }),
    ),
  ]),
];

afterEach(() => {
  cleanup();
  resetToastStoreForTests();
  connectionStore.setState({ state: "idle", client: null });
  window.history.replaceState({}, "", "/");
});

function unloadPrevented() {
  const event = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(event);
  return event.defaultPrevented;
}

function numericForm(
  current: LaunchConfigLayer,
  onSave: (config: LaunchConfigLayer) => Promise<LaunchConfigResolved>,
  layer: "global" | "project" = "global",
) {
  return (
    <SettingsDraftsProvider>
      <LaunchConfigForm
        options={[...OPTIONS, ...INTEGER_OPTIONS]}
        layer={layer}
        current={current}
        validatePath={async (path) => ({ valid: true, path })}
        successToast="Saved"
        onSave={onSave}
      />
    </SettingsDraftsProvider>
  );
}

test.each([
  ...INTEGER_OPTIONS.map((option) => ({ label: option.label, layer: "global" as const })),
  ...INTEGER_OPTIONS.filter((option) => option.defaultableLayers?.includes("project")).map((option) => ({
    label: option.label,
    layer: "project" as const,
  })),
])("invalid $label refuses the $layer write and retains edits and navigation protection", async ({ label, layer }) => {
  window.history.replaceState({}, "", "/settings/launch");
  const save = vi.fn(async () => RESOLVED);
  const view = render(numericForm({ agent: "original" }, save, layer));
  const user = userEvent.setup();
  const editor = screen.getByRole("spinbutton", { name: label });
  await user.type(editor, "1.5");
  expect(editor).toHaveProperty("value", "1.5");
  fireEvent.change(screen.getByRole("textbox", { name: "Agent" }), { target: { value: "submitted" } });
  expect(unloadPrevented()).toBe(true);

  await user.click(
    screen.getByRole("button", { name: layer === "project" ? "Save local overrides" : "Save launch defaults" }),
  );

  expect(save).not.toHaveBeenCalled();
  expect(screen.getByText("Enter an exact safe integer.")).toBeTruthy();
  expect(screen.queryByText(/Saved at/)).toBeNull();
  expect(editor).toHaveProperty("value", "1.5");
  expect(unloadPrevented()).toBe(true);
  // A same-owner refresh cannot erase either unsaved field after refusal.
  view.rerender(numericForm({ agent: "host-newer" }, save, layer));
  expect(editor).toHaveProperty("value", "1.5");
  expect(screen.getByRole("textbox", { name: "Agent" })).toHaveProperty("value", "submitted");
  act(() => navigate("/settings/theme"));
  expect(window.location.pathname).toBe("/settings/launch");
  await user.click(screen.getByRole("button", { name: "Stay" }));
  expect(screen.getByRole("spinbutton", { name: label })).toBe(editor);
});

test.each(["1e2", "1.0", "1.0000000000000000001", "9007199254740992", "-9007199254740992"])(
  "nonempty integer spelling %s cannot silently become unset",
  async (raw) => {
    const save = vi.fn(async () => RESOLVED);
    render(numericForm({}, save));
    const editor = screen.getByRole("spinbutton", { name: "Max rounds" });
    fireEvent.change(editor, { target: { value: raw } });
    expect(editor).toHaveProperty("value", raw);
    await userEvent.setup().click(screen.getByRole("button", { name: "Save launch defaults" }));
    expect(save).not.toHaveBeenCalled();
    expect(editor).toHaveProperty("value", raw);
    expect(unloadPrevented()).toBe(true);
  },
);

test.each([
  { raw: "-1", value: -1 },
  { raw: "0", value: 0 },
  { raw: "7", value: 7 },
  { raw: "007", value: 7 },
  { raw: "-0", value: -0 },
  { raw: "9007199254740991", value: 9007199254740991 },
  { raw: "-9007199254740991", value: -9007199254740991 },
])("valid integer spelling $raw persists and clears its dirty guard", async ({ raw, value }) => {
  const save = vi.fn(async () => RESOLVED);
  render(numericForm({}, save));
  const editor = screen.getByRole("spinbutton", { name: "Max rounds" });
  fireEvent.change(editor, { target: { value: raw } });
  expect(editor).toHaveProperty("value", raw);
  expect(unloadPrevented()).toBe(true);

  await userEvent.setup().click(screen.getByRole("button", { name: "Save launch defaults" }));

  expect(save).toHaveBeenCalledExactlyOnceWith({ maxRounds: value });
  expect(editor).toHaveProperty("value", raw);
  expect(unloadPrevented()).toBe(false);
});

test("correcting invalid numeric input clears its error and saved forms still follow host refreshes", async () => {
  const save = vi.fn(async () => RESOLVED);
  const view = render(numericForm({}, save));
  const user = userEvent.setup();
  const editor = screen.getByRole("spinbutton", { name: "Max rounds" });
  fireEvent.change(editor, { target: { value: "1.5" } });
  await user.click(screen.getByRole("button", { name: "Save launch defaults" }));
  expect(save).not.toHaveBeenCalled();
  expect(screen.getByText("Enter an exact safe integer.")).toBeTruthy();
  fireEvent.change(editor, { target: { value: "12" } });

  await user.click(screen.getByRole("button", { name: "Save launch defaults" }));

  expect(save).toHaveBeenCalledExactlyOnceWith({ maxRounds: 12 });
  expect(screen.queryByText("Enter an exact safe integer.")).toBeNull();
  expect(unloadPrevented()).toBe(false);
  view.rerender(numericForm({ maxRounds: 20 }, save));
  expect(editor).toHaveProperty("value", "20");
  expect(unloadPrevented()).toBe(false);
});

test("clearing every numeric override saves unset rather than rejecting empty input", async () => {
  const save = vi.fn(async () => RESOLVED);
  render(
    numericForm(
      { maxRounds: 3, maxSubagentDepth: 2, maxConcurrentDelegateTurns: 4, maxRetainedTerminal: 5, appReplaySize: 6 },
      save,
    ),
  );
  const user = userEvent.setup();
  for (const option of INTEGER_OPTIONS) await user.clear(screen.getByRole("spinbutton", { name: option.label }));
  expect(unloadPrevented()).toBe(true);

  await user.click(screen.getByRole("button", { name: "Save launch defaults" }));

  expect(save).toHaveBeenCalledExactlyOnceWith({});
  expect(unloadPrevented()).toBe(false);
  expect(screen.queryByText("Enter an exact safe integer.")).toBeNull();
});

test.each([
  { raw: "  normalized-agent  ", payload: { agent: "normalized-agent" } },
  { raw: "   ", payload: {} },
])("saving normalized text '$raw' remains clean without erasing its visible spelling", async ({ raw, payload }) => {
  const save = vi.fn(async () => RESOLVED);
  render(numericForm({ agent: "original" }, save));
  const editor = screen.getByRole("textbox", { name: "Agent" });
  fireEvent.change(editor, { target: { value: raw } });
  await userEvent.setup().click(screen.getByRole("button", { name: "Save launch defaults" }));
  expect(save).toHaveBeenCalledExactlyOnceWith(payload);
  expect(editor).toHaveProperty("value", raw);
  expect(unloadPrevented()).toBe(false);
});

function promptForm(current: LaunchConfigLayer, onSave: (config: LaunchConfigLayer) => Promise<LaunchConfigResolved>) {
  return (
    <SettingsDraftsProvider>
      <LaunchConfigForm
        options={PROMPT_OPTIONS}
        layer="global"
        current={current}
        validatePath={async (path) => ({ valid: true, path })}
        successToast="Saved"
        onSave={onSave}
      />
    </SettingsDraftsProvider>
  );
}

test.each(["System prompt text", "System prompt from file", "Append text", "Append from file"])(
  "saving Agent leaves the inactive %s draft protected",
  async (label) => {
    window.history.replaceState({}, "", "/settings/launch");
    const client = new FakeClient("ready");
    client.on("evener/paths/complete", () => ({ data: [] }));
    connectionStore.getState().connect(client);
    const save = vi.fn(async () => RESOLVED);
    render(promptForm({ agent: "original" }, save));
    const user = userEvent.setup();
    const editor = screen.getByLabelText(label);
    if (editor instanceof HTMLButtonElement) {
      await user.click(editor);
      await user.type(screen.getByRole("combobox"), "/pending-prompt");
      await user.keyboard("{Escape}");
    } else {
      await user.type(editor, "/pending-prompt");
    }
    fireEvent.change(screen.getByRole("textbox", { name: "Agent" }), { target: { value: "submitted" } });
    expect(unloadPrevented()).toBe(true);

    await user.click(screen.getByRole("button", { name: "Save launch defaults" }));

    expect(save).toHaveBeenCalledExactlyOnceWith({ agent: "submitted" });
    expect(unloadPrevented()).toBe(true);
    act(() => navigate("/settings/theme"));
    expect(window.location.pathname).toBe("/settings/launch");
    await user.click(screen.getByRole("button", { name: "Stay" }));
    expect(screen.getByLabelText(label)).toBe(editor);

    // Only the omitted prompt is still dirty, not the successfully saved Agent.
    if (editor instanceof HTMLButtonElement) {
      await user.click(editor);
      const input = screen.getByRole("combobox");
      expect(input).toHaveProperty("value", "/pending-prompt");
      await user.clear(input);
      await user.keyboard("{Escape}");
    } else {
      expect(editor).toHaveProperty("value", "/pending-prompt");
      await user.clear(editor);
    }
    expect(unloadPrevented()).toBe(false);
    act(() => navigate("/settings/theme"));
    expect(window.location.pathname).toBe("/settings/theme");
  },
);

test("canonical refresh after Save retains an omitted inline prompt draft", async () => {
  const save = vi.fn(async () => RESOLVED);
  const view = render(promptForm({ agent: "original" }, save));
  const user = userEvent.setup();
  const editor = screen.getByRole("textbox", { name: "System prompt text" });
  await user.type(editor, "pending-inline");
  fireEvent.change(screen.getByRole("textbox", { name: "Agent" }), { target: { value: "submitted" } });
  await user.click(screen.getByRole("button", { name: "Save launch defaults" }));

  view.rerender(promptForm({ agent: "submitted" }, save));

  expect(editor).toHaveProperty("value", "pending-inline");
  expect(screen.getByRole("textbox", { name: "System prompt text" })).toBe(editor);
  expect(unloadPrevented()).toBe(true);
  expect(save).toHaveBeenCalledExactlyOnceWith({ agent: "submitted" });
});

test("saving an active inline prompt clears its guard and still adopts later host values", async () => {
  const save = vi.fn(async () => RESOLVED);
  const view = render(promptForm({ systemPromptMode: "inline" }, save));
  const user = userEvent.setup();
  const editor = screen.getByRole("textbox", { name: "System prompt text" });
  await user.type(editor, "  persisted-inline  ");
  await user.click(screen.getByRole("button", { name: "Save launch defaults" }));

  expect(save).toHaveBeenCalledExactlyOnceWith({ systemPromptMode: "inline", systemPromptText: "persisted-inline" });
  expect(editor).toHaveProperty("value", "  persisted-inline  ");
  expect(unloadPrevented()).toBe(false);
  view.rerender(promptForm({ systemPromptMode: "inline", systemPromptText: "host-inline" }, save));
  expect(editor).toHaveProperty("value", "host-inline");
  expect(unloadPrevented()).toBe(false);
});

test("launch save clears only submitted edits, leaving newer input protected", async () => {
  let release!: (result: LaunchConfigResolved) => void;
  const save = vi.fn(
    () =>
      new Promise<LaunchConfigResolved>((resolve) => {
        release = resolve;
      }),
  );
  render(
    <SettingsDraftsProvider>
      <LaunchConfigForm
        options={OPTIONS}
        layer="global"
        current={{ agent: "original" }}
        validatePath={async (path) => ({ valid: true, path })}
        successToast="Saved"
        onSave={save}
      />
    </SettingsDraftsProvider>,
  );
  const user = userEvent.setup();
  const editor = screen.getByLabelText("Agent");
  fireEvent.change(editor, { target: { value: "submitted" } });
  expect(unloadPrevented()).toBe(true);
  await user.click(screen.getByRole("button", { name: "Save launch defaults" }));
  fireEvent.change(editor, { target: { value: "newer edit" } });
  await act(async () => {
    release(RESOLVED);
  });
  expect(unloadPrevented()).toBe(true);
  expect(editor).toHaveProperty("value", "newer edit");
  // Matching the confirmed baseline is clean, even after intermediate edits.
  fireEvent.change(editor, { target: { value: "submitted" } });
  expect(unloadPrevented()).toBe(false);
  act(() => navigate("/settings/theme"));
  expect(window.location.pathname).toBe("/settings/theme");
});

test("a temporary connection failure retains dirty input but does not strand Save busy or accept the old completion", async () => {
  let release!: (result: LaunchConfigResolved) => void;
  const save = vi.fn(
    () =>
      new Promise<LaunchConfigResolved>((resolve) => {
        release = resolve;
      }),
  );
  render(
    <SettingsDraftsProvider>
      <LaunchConfigForm
        options={OPTIONS}
        layer="global"
        current={{}}
        validatePath={async (path) => ({ valid: true, path })}
        successToast="Saved"
        onSave={save}
      />
    </SettingsDraftsProvider>,
  );
  const user = userEvent.setup();
  fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "retained" } });
  await user.click(screen.getByRole("button", { name: "Save launch defaults" }));
  act(() => connectionStore.setState({ state: "connecting" }));
  act(() => connectionStore.setState({ state: "ready" }));
  await act(async () => {
    release(RESOLVED);
  });
  expect(screen.getByLabelText("Agent")).toHaveProperty("value", "retained");
  expect(screen.getByRole("button", { name: "Save launch defaults" })).toHaveProperty("disabled", false);
  expect(unloadPrevented()).toBe(true);
  expect(screen.queryByText(/Saved at/)).toBeNull();
});

test("an old path preflight cannot publish errors or save into a replacement owner", async () => {
  let release!: (value: PathValidateResponse) => void;
  const validate = vi.fn(
    () =>
      new Promise<PathValidateResponse>((resolve) => {
        release = resolve;
      }),
  );
  const save = vi.fn(async () => RESOLVED);
  const options: LaunchOption[] = [{ ...OPTIONS[0]!, kind: "path", pathKind: "command" }];
  const form = (owner: object) => (
    <SettingsDraftsProvider>
      <LaunchConfigForm
        options={options}
        layer="global"
        current={{ agent: "" }}
        draftOwner={owner}
        validatePath={validate}
        successToast="Saved"
        onSave={save}
      />
    </SettingsDraftsProvider>
  );
  const view = render(form({ registration: 1 }));
  const user = userEvent.setup();
  fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "/old/path" } });
  await user.click(screen.getByRole("button", { name: "Save launch defaults" }));
  view.rerender(form({ registration: 2 }));
  await act(async () => {
    release({ valid: false, path: "/old/path", error: "old registration error" });
  });
  expect(save).not.toHaveBeenCalled();
  expect(screen.queryByText("old registration error")).toBeNull();
  expect(screen.getByLabelText("Agent")).toHaveProperty("value", "");
  expect(unloadPrevented()).toBe(false);
});
