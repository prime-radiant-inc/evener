import type { LaunchConfigResolved, LaunchOption, PathValidateResponse } from "@evener/appwire-client";
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
