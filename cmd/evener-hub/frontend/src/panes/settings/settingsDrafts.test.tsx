import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { navigate, requestNavigation } from "../../shell/routing";
import { SettingsDraftsProvider, useSettingsDirty } from "./settingsDrafts";

function Editor() {
  const [value, setValue] = useState("");
  useSettingsDirty(value !== "");
  return <input aria-label="draft" value={value} onChange={(event) => setValue(event.target.value)} />;
}

function unloadPrevented() {
  const event = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(event);
  return event.defaultPrevented;
}

afterEach(() => {
  cleanup();
  window.history.replaceState({}, "", "/");
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
