import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
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

// A WireError, as the hub really sends: friendlyErrorMessage passes a wire
// message through but replaces a plain Error with a generic line
// (mcp.test.tsx pins that), so a plain Error here would never reach the DOM.
function failingSaveClient(): FakeClient {
  const fake = connectFakeClient();
  fake.on("evener/settings/agentsDoc/set", () => {
    throw new WireError("read-only file system", -1);
  });
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

// The hub's own text is the whole point for a file editor - "permission
// denied" and "is a directory" are what tell the user what to fix - and it
// has to be announced, not just drawn.
test("a failed load shows the hub's own reason and announces it", async () => {
  const fake = new FakeClient("ready");
  fake.on("evener/settings/agentsDoc/get", () => {
    throw new Error("disk on fire");
  });
  connectionStore.getState().connect(fake);
  renderSection();
  await waitFor(() => expect(screen.getByRole("alert").textContent).toBe("Failed to load: disk on fire"));
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
  failingSaveClient();
  renderSection();
  await waitFor(() => expect(editor().value).toBe("# hi\n"));
  const user = userEvent.setup();
  await user.type(editor(), "more");
  await user.click(saveButton());
  await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("read-only file system"));
  expect(editor().value).toBe("# hi\nmore");
  expect(saveButton().disabled).toBe(false);
});

test("Revert clears a failed save's error", async () => {
  failingSaveClient();
  renderSection();
  await waitFor(() => expect(editor().value).toBe("# hi\n"));
  const user = userEvent.setup();
  await user.type(editor(), "more");
  await user.click(saveButton());
  await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("read-only file system"));

  await user.click(screen.getByRole("button", { name: "Revert" }));
  expect(screen.queryByRole("alert")).toBeNull();
});

test("Load current clears a failed save's error", async () => {
  const fake = failingSaveClient();
  renderSection();
  await waitFor(() => expect(editor().value).toBe("# hi\n"));
  const user = userEvent.setup();
  await user.type(editor(), "more");
  await user.click(saveButton());
  await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("read-only file system"));

  act(() => {
    fake.emitNotification({
      method: "evener/settings/agentsDoc/changed",
      params: { ...DOC, content: "# from the TUI\n" },
    } as AnyNotification);
  });
  await user.click(screen.getByRole("button", { name: "Load current" }));
  expect(screen.queryByRole("alert")).toBeNull();
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
  expect(screen.queryByRole("status")).toBeNull();
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
  await waitFor(() => expect(screen.getByRole("status").textContent).toContain("changed on disk"));
  expect(editor().value).toBe("# hi\nmore");

  await user.click(screen.getByRole("button", { name: "Load current" }));
  expect(editor().value).toBe("# from the TUI\n");
  expect(screen.queryByRole("status")).toBeNull();
  expect(saveButton().disabled).toBe(true);
});

// The hub broadcasts every write, this client's own save included, and
// Task 4's store deliberately does not suppress that self-echo: the section's
// content-keyed stale test is what makes the echo inert. An echo arriving
// after the user has typed again is where a dirtiness-keyed test would go
// wrong, so that is the shape this pins.
test("a changed broadcast echoing this client's own save leaves later keystrokes alone", async () => {
  const fake = connectFakeClient();
  renderSection();
  await waitFor(() => expect(editor().value).toBe("# hi\n"));
  const user = userEvent.setup();
  await user.type(editor(), "more");
  await user.click(saveButton());
  // The whole round-trip has settled: the toast landed and the editor is
  // editable again (it is disabled only while saving).
  await waitFor(() => {
    expect(getToasts().some((t) => t.text === "Saved AGENTS.md")).toBe(true);
    expect(editor().disabled).toBe(false);
  });
  await user.type(editor(), " again");

  act(() => {
    fake.emitNotification({
      method: "evener/settings/agentsDoc/changed",
      params: { ...DOC, content: "# hi\nmore" },
    } as AnyNotification);
  });

  expect(editor().value).toBe("# hi\nmore again");
  expect(screen.queryByRole("status")).toBeNull();
});

// An automatic reconnect keeps the same client - only a banner retry brings
// a fresh one - and the editor heard no `changed` broadcast while the socket
// was down. A one-shot mount fetch would leave it holding pre-drop content,
// and the next Save would push that over whatever the reconnected hub has.
test("a reconnect refetches the file", async () => {
  const fake = connectFakeClient();
  renderSection();
  await waitFor(() => expect(editor().value).toBe("# hi\n"));

  act(() => {
    fake.on("evener/settings/agentsDoc/get", () => ({ ...DOC, content: "# while we were away\n" }));
    fake.emitStateChange("reconnecting");
    fake.emitReady();
  });
  await waitFor(() => expect(editor().value).toBe("# while we were away\n"));
});

// A refetch that fails leaves `doc` where it was, so the editor goes on
// showing a copy it can no longer confirm - and a Save from there would push
// that over whatever is on disk now. Save stays enabled anyway: a reload that
// failed because the hub is unreachable fails a save the same way, and
// disabling it would only trap the draft.
test("a failed reload after a successful load shows a reload notice and keeps the editor", async () => {
  const fake = connectFakeClient();
  renderSection();
  await waitFor(() => expect(editor().value).toBe("# hi\n"));
  const saveDisabledBefore = saveButton().disabled;

  act(() => {
    fake.on("evener/settings/agentsDoc/get", () => {
      throw new WireError("hub unreachable", -1);
    });
    fake.emitStateChange("reconnecting");
    fake.emitReady();
  });

  await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("Couldn't reload AGENTS.md"));
  expect(screen.getByRole("alert").textContent).toContain("hub unreachable");
  expect(editor().value).toBe("# hi\n");
  expect(saveButton().disabled).toBe(saveDisabledBefore);
});

test("a reconnect while the draft is dirty keeps the draft and offers Load current", async () => {
  connectFakeClient();
  renderSection();
  await waitFor(() => expect(editor().value).toBe("# hi\n"));
  const user = userEvent.setup();
  await user.type(editor(), "more");

  act(() => {
    connectFakeClient({ ...DOC, content: "# while we were away\n" });
  });
  await waitFor(() => expect(screen.getByRole("status").textContent).toContain("changed on disk"));
  expect(editor().value).toBe("# hi\nmore");

  await user.click(screen.getByRole("button", { name: "Load current" }));
  expect(editor().value).toBe("# while we were away\n");
});
