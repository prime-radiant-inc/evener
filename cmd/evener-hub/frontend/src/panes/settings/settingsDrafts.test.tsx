import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { navigate, requestNavigation } from "../../shell/routing";
import { resetSettingsHostForTests, settingsHostStore } from "../../stores/settingsHost";
import { SettingsDraftsProvider, useSettingsDirty } from "./settingsDrafts";
import { useSettingsHost, useSettingsHostURLSync } from "./settingsHost";

function Editor() {
  const [value, setValue] = useState("");
  useSettingsDirty(value !== "");
  return <input aria-label="draft" value={value} onChange={(event) => setValue(event.target.value)} />;
}

function NavigationActions() {
  useSettingsHostURLSync();
  const { selectHost } = useSettingsHost();
  return (
    <>
      <button type="button" onClick={() => navigate("/settings/theme")}>
        Appearance
      </button>
      <button type="button" onClick={() => selectHost("gamma")}>
        Choose gamma
      </button>
    </>
  );
}

function unloadPrevented() {
  const event = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(event);
  return event.defaultPrevented;
}

afterEach(() => {
  cleanup();
  resetSettingsHostForTests();
  act(() => navigate("/", { carrySettingsHost: false }));
});

test.each([
  { action: "Appearance", expected: "/settings/theme?host=alpha", host: "alpha" },
  { action: "Choose gamma", expected: "/settings/agents-md?host=gamma&cwd=%2Faccepted", host: "gamma" },
])(
  "$action after Stay derives its target from accepted context while restoration is pending",
  async ({ action, expected, host }) => {
    navigate("/settings/project?host=beta&cwd=%2Fother");
    navigate("/settings/agents-md?host=alpha&cwd=%2Faccepted");
    const view = render(
      <SettingsDraftsProvider>
        <Editor />
        <NavigationActions />
      </SettingsDraftsProvider>,
    );
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "retained draft" } });
    const actualGo = window.history.go.bind(window.history);
    const go = vi.spyOn(window.history, "go").mockImplementation(() => {});
    try {
      // Back itself is real. Hold only the guard's recovery traversal so both
      // UI answers happen while the address bar still names the rejected route.
      await act(async () => {
        window.history.back();
        await vi.waitFor(() => expect(go).toHaveBeenCalledWith(1));
      });
      expect(window.location.search).toBe("?host=beta&cwd=%2Fother");
      fireEvent.click(screen.getByRole("button", { name: "Stay" }));
      fireEvent.click(screen.getByRole("button", { name: action }));
      fireEvent.click(screen.getByRole("button", { name: "Discard changes" }));
      expect(settingsHostStore.getState().host).toBe("alpha");
      await act(async () => {
        actualGo(1);
        await vi.waitFor(() => expect(`${window.location.pathname}${window.location.search}`).toBe(expected));
      });
      expect(settingsHostStore.getState().host).toBe(host);
    } finally {
      go.mockRestore();
      view.unmount();
    }
  },
);

test("host selection after legacy Stay retains accepted section and cwd without rewriting the rejected entry", async () => {
  window.history.pushState({}, "", "/settings/project?host=beta&cwd=%2Fother");
  window.history.pushState({}, "", "/settings/agents-md?host=alpha&cwd=%2Faccepted");
  render(
    <SettingsDraftsProvider>
      <Editor />
      <NavigationActions />
    </SettingsDraftsProvider>,
  );
  const editor = screen.getByRole("textbox");
  fireEvent.change(editor, { target: { value: "retained draft" } });
  await act(async () => {
    window.history.back();
    await vi.waitFor(() => expect(window.location.pathname).toBe("/settings/project"));
  });
  fireEvent.click(screen.getByRole("button", { name: "Stay" }));
  expect(editor).toHaveProperty("value", "retained draft");
  expect(window.location.search).toBe("?host=beta&cwd=%2Fother");
  expect(settingsHostStore.getState().host).toBe("alpha");
  fireEvent.click(screen.getByRole("button", { name: "Choose gamma" }));
  fireEvent.click(screen.getByRole("button", { name: "Discard changes" }));
  expect(`${window.location.pathname}${window.location.search}`).toBe("/settings/agents-md?host=gamma&cwd=%2Faccepted");
});

test("clean editors leave immediately, dirty registrations follow each editor lifetime", () => {
  window.history.replaceState({}, "", "/settings/agents-md");
  const view = render(
    <SettingsDraftsProvider>
      <Editor />
      <Editor />
    </SettingsDraftsProvider>,
  );
  expect(unloadPrevented()).toBe(false);
  act(() => navigate("/settings/theme"));
  expect(window.location.pathname).toBe("/settings/theme");
  const editors = screen.getAllByRole("textbox");
  fireEvent.change(editors[0]!, { target: { value: "first" } });
  fireEvent.change(editors[1]!, { target: { value: "second" } });
  expect(unloadPrevented()).toBe(true);
  fireEvent.change(editors[0]!, { target: { value: "" } });
  expect(unloadPrevented()).toBe(true);
  view.rerender(
    <SettingsDraftsProvider>
      <Editor />
    </SettingsDraftsProvider>,
  );
  expect(unloadPrevented()).toBe(false);
  view.unmount();
  act(() => navigate("/new"));
  expect(window.location.pathname).toBe("/new");
});

test("a save completing during confirmation does not retarget the original destination", async () => {
  const user = userEvent.setup();
  const first = vi.fn();
  const late = vi.fn();
  render(
    <SettingsDraftsProvider>
      <Editor />
    </SettingsDraftsProvider>,
  );
  const editor = screen.getByRole("textbox");
  fireEvent.change(editor, { target: { value: "dirty" } });
  act(() => requestNavigation(first));
  // An existing save can clear dirty while the confirmation remains open.
  fireEvent.change(editor, { target: { value: "" } });
  act(() => requestNavigation(late));
  expect(first).not.toHaveBeenCalled();
  expect(late).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "Discard changes" }));
  expect(first).toHaveBeenCalledTimes(1);
  expect(late).not.toHaveBeenCalled();
});

test("unmount clears an open confirmation, unload handler and pending continuation", () => {
  const pending = vi.fn();
  const view = render(
    <SettingsDraftsProvider>
      <Editor />
    </SettingsDraftsProvider>,
  );
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "dirty" } });
  act(() => requestNavigation(pending));
  view.unmount();
  expect(unloadPrevented()).toBe(false);
  const fresh = vi.fn();
  act(() => requestNavigation(fresh));
  expect(fresh).toHaveBeenCalledTimes(1);
  expect(pending).not.toHaveBeenCalled();
});
