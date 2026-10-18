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
    defaultableLayers: ["global"],
  },
];
const RESOLVED: LaunchConfigResolved = { effective: {}, layers: {}, provenance: {} };

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
