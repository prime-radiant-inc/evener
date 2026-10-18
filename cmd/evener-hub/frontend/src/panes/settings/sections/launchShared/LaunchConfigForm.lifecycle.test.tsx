import type { LaunchConfigLayer, LaunchOption, PathValidateResponse } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
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
  {
    field: "traceFile",
    wireField: "traceFile",
    label: "Trace command",
    group: "Agent",
    kind: "path",
    pathKind: "command",
    perLaunch: true,
    defaultableLayers: ["global"],
  },
];
const RESOLVED = { effective: {}, layers: {}, provenance: {} };

function setup() {
  const validation = deferred<PathValidateResponse>();
  const write = deferred<typeof RESOLVED>();
  const validate = vi.fn(() => validation.promise);
  const save = vi.fn(async (_config: LaunchConfigLayer) => RESOLVED);
  const current = {};
  const tree = (owner: object | undefined = undefined) => (
    <SettingsDraftsProvider>
      <LaunchConfigForm
        options={OPTIONS}
        layer="global"
        current={current}
        draftOwner={owner}
        validatePath={validate}
        successToast="Saved"
        onSave={save}
      />
    </SettingsDraftsProvider>
  );
  const view = render(tree());
  fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "first" } });
  fireEvent.change(screen.getByLabelText("Trace command"), { target: { value: "/first" } });
  return {
    validation,
    write,
    validate,
    save,
    view,
    tree,
    user: userEvent.setup(),
    button: screen.getByRole("button", { name: "Save launch defaults" }),
  };
}

function dirty() {
  const event = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(event);
  return event.defaultPrevented;
}

afterEach(() => {
  cleanup();
  resetToastStoreForTests();
  connectionStore.setState({ state: "idle", client: null });
});

test("Save holds admission from deferred validation through write while newer input stays editable and dirty", async () => {
  const { validation, write, validate, save, user, button } = setup();
  save.mockImplementationOnce(() => write.promise);
  await user.click(button);
  fireEvent.change(screen.getByLabelText("Trace command"), { target: { value: "" } });
  fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "newer" } });
  await user.click(button);
  expect(save).not.toHaveBeenCalled();
  expect(button).toHaveProperty("disabled", true);
  expect(validate).toHaveBeenCalledTimes(1);
  await act(async () => validation.resolve({ valid: true, path: "/first" }));
  expect(save).toHaveBeenCalledExactlyOnceWith({ agent: "first", traceFile: "/first" });
  expect(button).toHaveProperty("disabled", true);
  await act(async () => write.resolve(RESOLVED));
  expect(button).toHaveProperty("disabled", false);
  expect(screen.getByLabelText("Agent")).toHaveProperty("value", "newer");
  expect(dirty()).toBe(true);
  await user.click(button);
  expect(save).toHaveBeenCalledTimes(2);
  expect(save).toHaveBeenLastCalledWith({ agent: "newer" });
  expect(dirty()).toBe(false);
});

test("synchronous duplicate Save invocation admits only one preflight and write", async () => {
  const { validation, validate, save, button } = setup();
  act(() => {
    fireEvent.click(button);
    fireEvent.click(button);
  });
  expect(validate).toHaveBeenCalledTimes(1);
  await act(async () => validation.resolve({ valid: true, path: "/first" }));
  expect(save).toHaveBeenCalledTimes(1);
});

test("rejected launch preflight reports failure, retains the draft and permits explicit retry", async () => {
  const { validation, validate, save, user, button } = setup();
  await user.click(button);
  await act(async () => validation.reject(new Error("validation disconnected")));
  expect(screen.getByText("Error: validation disconnected")).toBeTruthy();
  expect(save).not.toHaveBeenCalled();
  expect(button).toHaveProperty("disabled", false);
  expect(screen.getByLabelText("Trace command")).toHaveProperty("value", "/first");
  expect(dirty()).toBe(true);
  validate.mockImplementation(async () => ({ valid: true, path: "/first" }));
  await user.click(button);
  expect(save).toHaveBeenCalledExactlyOnceWith({ agent: "first", traceFile: "/first" });
  expect(dirty()).toBe(false);
});

test.each(["owner", "connection", "client", "unmount"] as const)(
  "old rejected validation cannot publish after %s change",
  async (change) => {
    const { validation, save, user, button, view, tree } = setup();
    await user.click(button);
    if (change === "owner") view.rerender(tree({ replacement: true }));
    else if (change === "connection") act(() => connectionStore.setState({ state: "connecting" }));
    else if (change === "client") act(() => connectionStore.setState({ client: new FakeClient("ready") }));
    else view.unmount();
    await act(async () => validation.reject(new Error("obsolete validation")));
    expect(save).not.toHaveBeenCalled();
    expect(screen.queryByText(/obsolete validation/)).toBeNull();
  },
);

test("old invalidated preflight finally cannot release a newer submission lock", async () => {
  const { validation, write, validate, save, user, button } = setup();
  await user.click(button);
  act(() => connectionStore.setState({ state: "connecting" }));
  act(() => connectionStore.setState({ state: "ready" }));
  validate.mockImplementation(async () => ({ valid: true, path: "/first" }));
  save.mockImplementationOnce(() => write.promise);
  await user.click(button);
  expect(save).toHaveBeenCalledTimes(1);
  await act(async () => validation.resolve({ valid: true, path: "/first" }));
  expect(button).toHaveProperty("disabled", true);
  await user.click(button);
  expect(save).toHaveBeenCalledTimes(1);
  await act(async () => write.resolve(RESOLVED));
  expect(button).toHaveProperty("disabled", false);
  expect(dirty()).toBe(false);
});
