import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { WireError } from "../../../protocol/errors";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { AgentsDocResponse, AnyNotification } from "../../../protocol/types.gen";
import { resetAgentsDocStoreForTests } from "../../../stores/agentsDoc";
import { connectionStore } from "../../../stores/connection";
import { Toast } from "../../../widgets";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import { AgentsDocSection } from "./agentsDoc";

const DOC: AgentsDocResponse = { path: "/home/u/.config/evener/AGENTS.md", exists: true, content: "# hi\n" };

function connectFakeClient(doc: AgentsDocResponse = DOC): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("evener/settings/agentsDoc/get", () => doc);
  fake.on("evener/settings/agentsDoc/set", (params) => ({ ...doc, exists: true, content: params.content }));
  connectionStore.getState().connect(fake);
  return fake;
}

function renderSection() {
  return render(
    <>
      <Toast />
      <AgentsDocSection sectionId="agents-md" />
    </>,
  );
}

function editor(): HTMLTextAreaElement {
  return screen.getByRole("textbox", { name: "AGENTS.md contents" }) as HTMLTextAreaElement;
}

function saveButton(): HTMLButtonElement {
  return screen.getByRole("button", { name: "Save" }) as HTMLButtonElement;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetAgentsDocStoreForTests();
  resetToastStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

test("loads the file on mount and shows its path and content", async () => {
  connectFakeClient();
  renderSection();
  await waitFor(() => expect(editor().value).toBe("# hi\n"));
  expect(screen.getByText("/home/u/.config/evener/AGENTS.md")).toBeTruthy();
});

test("a missing file renders an empty editor, not an error", async () => {
  connectFakeClient({ path: "/home/u/.config/evener/AGENTS.md", exists: false, content: "" });
  renderSection();
  await waitFor(() => expect(editor().value).toBe(""));
  expect(screen.queryByText(/Failed to load/)).toBeNull();
});

test("a failed load shows the error", async () => {
  const fake = new FakeClient("ready");
  fake.on("evener/settings/agentsDoc/get", () => {
    throw new Error("disk on fire");
  });
  connectionStore.getState().connect(fake);
  renderSection();
  await waitFor(() => expect(screen.getByText(/Failed to load/)).toBeTruthy());
});

test("Save and Revert are disabled until the draft differs from the loaded file", async () => {
  connectFakeClient();
  renderSection();
  await waitFor(() => expect(editor().value).toBe("# hi\n"));
  expect(saveButton().disabled).toBe(true);
  expect((screen.getByRole("button", { name: "Revert" }) as HTMLButtonElement).disabled).toBe(true);

  const user = userEvent.setup();
  await user.type(editor(), "more");
  expect(saveButton().disabled).toBe(false);
  expect((screen.getByRole("button", { name: "Revert" }) as HTMLButtonElement).disabled).toBe(false);
});

test("Revert restores the loaded content", async () => {
  connectFakeClient();
  renderSection();
  await waitFor(() => expect(editor().value).toBe("# hi\n"));
  const user = userEvent.setup();
  await user.type(editor(), "more");
  await user.click(screen.getByRole("button", { name: "Revert" }));
  expect(editor().value).toBe("# hi\n");
  expect(saveButton().disabled).toBe(true);
});

test("Save sends the draft, toasts, and the editor is clean afterwards", async () => {
  const fake = connectFakeClient();
  renderSection();
  await waitFor(() => expect(editor().value).toBe("# hi\n"));
  const user = userEvent.setup();
  await user.type(editor(), "more");
  await user.click(saveButton());
  await waitFor(() => expect(getToasts().some((t) => t.text === "Saved AGENTS.md")).toBe(true));
  const set = fake.calls.find((c) => c.method === "evener/settings/agentsDoc/set");
  expect(set?.params).toEqual({ content: "# hi\nmore" });
  expect(saveButton().disabled).toBe(true);
  expect(editor().value).toBe("# hi\nmore");
});

test("a failed save shows the hub's error inline and keeps the draft", async () => {
  const fake = connectFakeClient();
  // A WireError, as the hub really sends: friendlyErrorMessage passes a
  // wire message through but replaces a plain Error with a generic line
  // (mcp.test.tsx pins that), so a plain Error here would never reach the DOM.
  fake.on("evener/settings/agentsDoc/set", () => {
    throw new WireError("read-only file system", -1);
  });
  renderSection();
  await waitFor(() => expect(editor().value).toBe("# hi\n"));
  const user = userEvent.setup();
  await user.type(editor(), "more");
  await user.click(saveButton());
  await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("read-only file system"));
  expect(editor().value).toBe("# hi\nmore");
  expect(saveButton().disabled).toBe(false);
});

test("a changed broadcast while the draft is clean replaces the editor", async () => {
  const fake = connectFakeClient();
  renderSection();
  await waitFor(() => expect(editor().value).toBe("# hi\n"));
  act(() => {
    fake.emitNotification({
      method: "evener/settings/agentsDoc/changed",
      params: { ...DOC, content: "# from the TUI\n" },
    } as AnyNotification);
  });
  await waitFor(() => expect(editor().value).toBe("# from the TUI\n"));
  expect(screen.queryByText(/changed on disk/)).toBeNull();
});

test("a changed broadcast while the draft is dirty keeps the draft and offers Load current", async () => {
  const fake = connectFakeClient();
  renderSection();
  await waitFor(() => expect(editor().value).toBe("# hi\n"));
  const user = userEvent.setup();
  await user.type(editor(), "more");
  act(() => {
    fake.emitNotification({
      method: "evener/settings/agentsDoc/changed",
      params: { ...DOC, content: "# from the TUI\n" },
    } as AnyNotification);
  });
  await waitFor(() => expect(screen.getByText(/changed on disk/)).toBeTruthy());
  expect(editor().value).toBe("# hi\nmore");

  await user.click(screen.getByRole("button", { name: "Load current" }));
  expect(editor().value).toBe("# from the TUI\n");
  expect(screen.queryByText(/changed on disk/)).toBeNull();
  expect(saveButton().disabled).toBe(true);
});
