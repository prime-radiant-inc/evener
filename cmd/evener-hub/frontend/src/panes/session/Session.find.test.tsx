// Find in the real session pane: the chord, the bar above the transcript, and
// the marks on the transcript's own entries. The search itself, and its paging,
// are covered against the real stores in find/useFindInSession.test.tsx.
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { resetActivitySidebarStoreForTests } from "../../shell/activitybar/activitySidebarStore";
import { ClientProvider } from "../../shell/clientContext";
import { resetMobileViewportForTests } from "../../shell/useIsMobile";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { resetActivityPanelStoreForTests } from "../../stores/activityPanel";
import { resetConnectionStoreForTests } from "../../stores/connection";
import { MutationOutboxIndexedDB } from "../../stores/mutationOutboxIndexedDB";
import { resetNavigationStoreForTests } from "../../stores/navigation/store";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../stores/threads";
import { connectReadyFakeClient } from "../../testing/connectionTestUtils";
import { resetDisclosureStoreForTests } from "../../widgets/disclosure/disclosureStore";
import { resetAskDockStoreForTests } from "./composer/askDock/askDockStore";
import { resetPendingTurnsStoreForTests } from "./composer/queue/pendingTurnsStore";
import { FIND_CURRENT_ATTRIBUTE } from "./find/findMarks";
import { requestFindInSession, resetFindRequestsForTests } from "./find/findRequest";
import "./index";
import { stubSessionSlots, testThread } from "./sessionTestUtils";
import { CommittedSession as Session } from "./testing/CommittedSession";
import { resetTranscriptPagingForTests } from "./transcript/useTranscript";
import "./testing/editorGeometry";

stubSessionSlots();

// jsdom lays nothing out; a stubbed height is what makes the virtual list
// render rows at all (see Session.test.tsx).
const CONTAINER_HEIGHT = 500;
let offsetHeightDescriptor: PropertyDescriptor | undefined;

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

beforeEach(() => {
  stubSessionSlots();
  globalThis.indexedDB = new IDBFactory();
  globalThis.IDBKeyRange = IDBKeyRange;
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  resetTranscriptPagingForTests();
  resetAskDockStoreForTests();
  resetNavigationStoreForTests();
  resetActivityPanelStoreForTests();
  resetActivitySidebarStoreForTests();
  resetMobileViewportForTests();
  resetFindRequestsForTests();
  resetDisclosureStoreForTests();
  setMutationStorageForTests(new MutationOutboxIndexedDB());
  resetPendingTurnsStoreForTests();
  localStorage.clear();
  offsetHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: CONTAINER_HEIGHT });
});

afterEach(() => {
  cleanup();
  resetPendingTurnsStoreForTests();
  resetAskDockStoreForTests();
  resetActivityPanelStoreForTests();
  resetActivitySidebarStoreForTests();
  resetMobileViewportForTests();
  resetWorkspaceStoreForTests();
  vi.restoreAllMocks();
  if (offsetHeightDescriptor) {
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", offsetHeightDescriptor);
  }
});

function message(turnId: string, type: "userMessage" | "agentMessage", text: string) {
  return { id: `${turnId}-${type}`, turnId, type, text, status: "completed" as const };
}

function call(turnId: string, index: number, output: string) {
  return {
    id: `${turnId}-call-${index}`,
    turnId,
    type: "commandExecution",
    text: "",
    toolName: "read_file",
    callId: `${turnId}-call-${index}`,
    output,
    status: "completed" as const,
  };
}

function renderSession(focusedPane = "p1") {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => ({
    thread: testThread("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [
            message("turn_1", "userMessage", "read the files"),
            call("turn_1", 0, "plain"),
            call("turn_1", 1, "the quokka lives here"),
            call("turn_1", 2, "plain"),
            message("turn_1", "agentMessage", "Done reading."),
          ],
        },
        {
          id: "turn_2",
          status: "completed",
          itemsView: "full",
          items: [
            message("turn_2", "userMessage", "what about the quokka?"),
            message("turn_2", "agentMessage", "No more to say."),
          ],
        },
      ],
    }),
  }));
  workspaceStore.setState({
    panes: [
      { id: "p1", type: "session", params: { ref: "ref_a" }, slot: "main" },
      { id: "p2", type: "welcome", params: {}, slot: "secondary" },
    ],
    focusedPaneId: focusedPane,
  });
  return render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={focusedPane === "p1"} />
    </ClientProvider>,
  );
}

function pressFind(): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key: "f", code: "KeyF", ctrlKey: true, cancelable: true });
  act(() => {
    window.dispatchEvent(event);
  });
  return event;
}

const marked = () => document.querySelector(`[${FIND_CURRENT_ATTRIBUTE}]`);

test("the find chord opens Find over the focused conversation and marks the newest match's entry", async () => {
  const view = renderSession();
  await screen.findByText("No more to say.");

  expect(pressFind().defaultPrevented).toBe(true);
  const field = screen.getByRole("searchbox");
  expect(document.activeElement).toBe(field);
  // The bar sits above the transcript, clear of the composer below it.
  const transcript = screen.getByTestId("transcript-virtual-list");
  expect(
    screen.getByTestId("find-bar").compareDocumentPosition(transcript) & Node.DOCUMENT_POSITION_FOLLOWING,
  ).not.toBe(0);

  fireEvent.change(field, { target: { value: "quokka" } });
  const bar = screen.getByTestId("find-bar");
  expect(bar.getAttribute("data-find-count")).toBe("2");
  expect(bar.getAttribute("data-find-position")).toBe("1");
  await waitFor(() => expect(marked()?.getAttribute("data-view-anchor-id")).toBe("turn_2-userMessage"));

  fireEvent.keyDown(field, { key: "Escape" });
  expect(screen.queryByTestId("find-bar")).toBeNull();
  expect(marked()).toBeNull();
  view.unmount();
});

test("a match inside a folded run opens the run and the call, and marks the call itself", async () => {
  renderSession();
  await screen.findByText("No more to say.");
  // The three quiet calls fold into one closed run: the match is not on screen.
  expect(screen.queryByText("the quokka lives here")).toBeNull();

  pressFind();
  const field = screen.getByRole("searchbox");
  fireEvent.change(field, { target: { value: "quokka" } });
  fireEvent.keyDown(field, { key: "Enter" });

  expect(screen.getByTestId("find-bar").getAttribute("data-find-position")).toBe("2");
  await waitFor(() => expect(marked()?.getAttribute("data-item-id")).toBe("turn_1-call-1"));
  expect(marked()?.getAttribute(FIND_CURRENT_ATTRIBUTE)).toBe("entry");
  expect(await screen.findByText("the quokka lives here")).toBeTruthy();
});

test("a pane that does not hold the keyboard leaves the find chord to the browser", async () => {
  renderSession("p2");
  await screen.findByText("No more to say.");
  expect(pressFind().defaultPrevented).toBe(false);
  expect(screen.queryByTestId("find-bar")).toBeNull();
});

test("a request from the menu or the palette opens Find on the named item", async () => {
  renderSession();
  await screen.findByText("No more to say.");
  act(() => requestFindInSession("ref_a", { target: "p1", query: "quokka", itemId: "turn_1-call-1" }));

  const bar = screen.getByTestId("find-bar");
  expect((screen.getByRole("searchbox") as HTMLInputElement).value).toBe("quokka");
  expect(bar.getAttribute("data-find-position")).toBe("2");
  await waitFor(() => expect(marked()?.getAttribute("data-item-id")).toBe("turn_1-call-1"));
});

test("calls that fold into a run after a match was found are opened again when the reader steps back to it", async () => {
  // While the turn runs its calls are separate rows; once it completes they
  // fold into one closed run, with the same entries on the same row.
  let status: "inProgress" | "completed" = "inProgress";
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => ({
    thread: testThread("ref_a", {
      turns: [
        {
          id: "turn_1",
          status,
          itemsView: "full",
          items: [
            message("turn_1", "userMessage", "read the quokka files"),
            call("turn_1", 0, "plain"),
            call("turn_1", 1, "the quokka lives here"),
            call("turn_1", 2, "plain"),
          ],
        },
      ],
    }),
  }));
  workspaceStore.setState({
    panes: [{ id: "p1", type: "session", params: { ref: "ref_a" }, slot: "main" }],
    focusedPaneId: "p1",
  });
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused />
    </ClientProvider>,
  );
  await screen.findByText("read the quokka files");
  pressFind();
  const field = screen.getByRole("searchbox");
  fireEvent.change(field, { target: { value: "quokka" } });
  expect(screen.getByTestId("find-bar").getAttribute("data-find-count")).toBe("2");

  status = "completed";
  await act(async () => {
    await threadsStore.getState().refreshThread("ref_a");
  });
  await waitFor(() => expect(screen.getByTestId("tool-run")).toBeTruthy());
  // Stepping away and back lands on the call inside the run that now hides it.
  fireEvent.keyDown(field, { key: "Enter" });
  fireEvent.keyDown(field, { key: "Enter", shiftKey: true });
  await waitFor(() => expect(marked()?.getAttribute("data-item-id")).toBe("turn_1-call-1"));
  expect(marked()?.getAttribute(FIND_CURRENT_ATTRIBUTE)).toBe("entry");
});

test("a tool body that would not show the matched words shows them while it is the current match", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => ({
    thread: testThread("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [
            message("turn_1", "userMessage", "write and fetch"),
            {
              id: "write",
              turnId: "turn_1",
              type: "commandExecution",
              text: "",
              toolName: "write_file",
              callId: "write",
              argumentsJson: JSON.stringify({
                path: "/notes.md",
                content: `${"filler ".repeat(200)}the wombat paragraph`,
              }),
              output: "wrote 1420 bytes to /notes.md",
              status: "completed" as const,
            },
            {
              id: "fetch",
              turnId: "turn_1",
              type: "commandExecution",
              text: "",
              toolName: "web_fetch",
              callId: "fetch",
              argumentsJson: JSON.stringify({ url: "https://example.com" }),
              output: `${"page ".repeat(100)}a numbat sentence far past the preview`,
              status: "completed" as const,
            },
            message("turn_1", "agentMessage", "Done."),
          ],
        },
      ],
    }),
  }));
  workspaceStore.setState({
    panes: [{ id: "p1", type: "session", params: { ref: "ref_a" }, slot: "main" }],
    focusedPaneId: "p1",
  });
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused />
    </ClientProvider>,
  );
  await screen.findByText("Done.");
  pressFind();
  const field = screen.getByRole("searchbox");

  // write_file's body shows only its confirmation line.
  fireEvent.change(field, { target: { value: "wombat paragraph" } });
  await waitFor(() => expect(marked()?.getAttribute("data-view-anchor-id")).toBe("write"));
  await waitFor(() => expect(marked()?.textContent).toContain("the wombat paragraph"));

  // web_fetch's body shows a short preview of the page.
  fireEvent.change(field, { target: { value: "numbat sentence" } });
  await waitFor(() => expect(marked()?.getAttribute("data-view-anchor-id")).toBe("fetch"));
  await waitFor(() => expect(marked()?.textContent).toContain("a numbat sentence"));

  // Moving on puts the earlier call back as it was.
  expect(document.querySelector('[data-view-anchor-id="write"]')?.textContent).not.toContain("wombat");
});
