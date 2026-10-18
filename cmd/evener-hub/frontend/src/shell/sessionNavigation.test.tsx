import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { lazy, useState } from "react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { SettingsDraftsProvider, useSettingsDirty } from "../panes/settings/settingsDrafts";
import { Input } from "../widgets";
import { StackHost } from "./mobile/StackHost";
import { registerPaneForTests } from "./paneRegistry";
import { cycleSessionPane } from "./sessionCycle";
import { openSessionByRef } from "./sessionPlacement";
import { resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

function Draft() {
  const [value, setValue] = useState("");
  useSettingsDirty(value !== "");
  return <Input aria-label="draft" value={value} onChange={(event) => setValue(event.target.value)} />;
}
function Settings() {
  return (
    <SettingsDraftsProvider>
      <Draft />
    </SettingsDraftsProvider>
  );
}
const undos: (() => void)[] = [];
beforeEach(() => {
  resetWorkspaceStoreForTests();
  undos.push(
    registerPaneForTests({
      id: "settings",
      singleton: true,
      title: () => "Settings",
      component: lazy(async () => ({ default: Settings })),
    }),
  );
  undos.push(
    registerPaneForTests({
      id: "session",
      title: () => "Session",
      component: lazy(async () => ({ default: () => null })),
    }),
  );
  window.history.replaceState({}, "", "/settings/theme");
});
afterEach(() => {
  cleanup();
  resetWorkspaceStoreForTests();
  for (const undo of undos.splice(0)) undo();
});

test.each(["open", "next", "previous"] as const)(
  "%s asks before session focus can unmount a dirty mobile editor",
  async (action) => {
    const settings = workspaceStore.getState().replacePrimary("settings", { section: "theme" });
    const first = workspaceStore.getState().openPane("session", { ref: "local:first" }, { slot: "secondary" });
    const last = workspaceStore.getState().openPane("session", { ref: "local:last" }, { slot: "secondary" });
    workspaceStore.getState().focusPane(settings);
    await act(async () => {
      render(<StackHost />);
    });
    const editor = await screen.findByRole("textbox", { name: "draft" });
    fireEvent.change(editor, { target: { value: "unsaved" } });
    const request = () => (action === "open" ? openSessionByRef("local:first") : cycleSessionPane(action));
    await act(async () => request());
    expect(screen.getByRole("textbox", { name: "draft" })).toBe(editor);
    expect(workspaceStore.getState().focusedPaneId).toBe(settings);
    expect(window.location.pathname).toBe("/settings/theme");
    fireEvent.click(screen.getByRole("button", { name: "Stay" }));
    expect(editor).toHaveProperty("value", "unsaved");
    await act(async () => request());
    // A later request cannot replace the original session target.
    act(() => openSessionByRef("local:unopened"));
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Discard changes" })));
    expect(workspaceStore.getState().focusedPaneId).toBe(action === "previous" ? last : first);
    expect(editor.isConnected).toBe(false);
  },
);
