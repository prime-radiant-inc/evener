// Find over a steer's notification cards, mounted in the real session: the
// card, its outer disclosure, its raw disclosure and Find's marks are all the
// production ones. Tools level leaves the cards collapsed, so a card Find had
// to open is a card the reader never touched.
import { makeTranscriptDisplayConfig, type ThreadItem } from "@evener/appwire-client";
import { notificationWireItem } from "@evener/appwire-client/testing/notificationWireFixtures";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { ClientProvider } from "../../../shell/clientContext";
import { resetMobileViewportForTests } from "../../../shell/useIsMobile";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { resetConnectionStoreForTests } from "../../../stores/connection";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests } from "../../../stores/threads";
import { resetTranscriptDisplayStoreForTests, transcriptDisplayStore } from "../../../stores/transcriptDisplay";
import { connectReadyFakeClient } from "../../../testing/connectionTestUtils";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import "../index";
import { resetAskDockStoreForTests } from "../composer/askDock/askDockStore";
import { resetPendingTurnsStoreForTests } from "../composer/queue/pendingTurnsStore";
import { stubSessionSlots, testThread } from "../sessionTestUtils";
import { CommittedSession as Session } from "../testing/CommittedSession";
import "../testing/editorGeometry";
import { resetTranscriptPagingForTests } from "../transcript/useTranscript";
import { FIND_CURRENT_ATTRIBUTE } from "./findMarks";
import { resetFindRequestsForTests } from "./findRequest";

stubSessionSlots();

// jsdom lays nothing out; a stubbed height is what makes the virtual list
// render rows at all (see Session.test.tsx).
const CONTAINER_HEIGHT = 500;
let offsetHeightDescriptor: PropertyDescriptor | undefined;

const TOOLS = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });

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
  resetTranscriptDisplayStoreForTests();
  resetAskDockStoreForTests();
  resetMobileViewportForTests();
  resetFindRequestsForTests();
  resetDisclosureStoreForTests();
  resetPendingTurnsStoreForTests();
  setMutationStorageForTests(new MutationOutboxIndexedDB());
  localStorage.clear();
  // Collapsed cards, so every disclosure the assertions read was opened by
  // Find rather than by the display preset.
  transcriptDisplayStore.getState().setLocal(transcriptDisplayStore.getState().viewport, TOOLS);
  offsetHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: CONTAINER_HEIGHT });
});

afterEach(() => {
  cleanup();
  resetAskDockStoreForTests();
  resetMobileViewportForTests();
  resetWorkspaceStoreForTests();
  vi.restoreAllMocks();
  if (offsetHeightDescriptor) Object.defineProperty(HTMLElement.prototype, "offsetHeight", offsetHeightDescriptor);
});

function steer(id: string, text: string): ThreadItem {
  return { id, turnId: "turn_1", type: "steering", text, status: "completed" } as ThreadItem;
}

function renderSession(items: ThreadItem[]) {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => ({
    thread: testThread("ref_a", {
      turns: [{ id: "turn_1", status: "completed", itemsView: "full", items }],
    }),
  }));
  workspaceStore.setState({
    panes: [{ id: "p1", type: "session", params: { ref: "ref_a" }, slot: "main" }],
    focusedPaneId: "p1",
  });
  return render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
}

/** Opens Find over the mounted conversation and types `query` into it. */
async function search(query: string): Promise<void> {
  await screen.findAllByTestId("notification-card");
  act(() => {
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "f", code: "KeyF", ctrlKey: true, cancelable: true }));
  });
  fireEvent.change(screen.getByRole("searchbox"), { target: { value: query } });
}

const cards = () => screen.getAllByTestId("notification-card");
const barCount = () => screen.getByTestId("find-bar").getAttribute("data-find-count");
const marked = () => document.querySelector(`[${FIND_CURRENT_ATTRIBUTE}]`);
/** The card's outer disclosure: the first <details> it holds (its own). */
const cardOpen = (card: HTMLElement) => (card.querySelector("details") as HTMLDetailsElement).open;

test("a delegate card's report is a match and opens the card, while the words only its frame holds are not", async () => {
  renderSession([steer("steer_1", notificationWireItem("delegate-reported").text ?? "")]);
  await search("the settle pass now waits for the drain");

  expect(barCount()).toBe("1");
  const card = cards()[0] as HTMLElement;
  await waitFor(() => expect(cardOpen(card)).toBe(true));
  expect(screen.getByTestId("notification-message").textContent).toContain("settle pass now waits");
  await waitFor(() => expect(marked()).not.toBeNull());

  // A delegate card renders no raw disclosure, so a frame attribute is no match.
  fireEvent.change(screen.getByRole("searchbox"), { target: { value: 'delegate_id="dlg_1"' } });
  expect(barCount()).toBe("0");
});

test("a match in a job card's ANSI excerpt opens the card and leaves its raw frame closed", async () => {
  const text =
    '<job-notification job_id="job_1" event="completed" job_type="shell" intent="Colour run">\nJob completed.\nexcerpt:\nstarting\n\u001b[31mneedleRed\u001b[0m\n</job-notification>';
  renderSession([steer("steer_1", text)]);
  await search("needleRed");

  const card = cards()[0] as HTMLElement;
  await waitFor(() => expect(cardOpen(card)).toBe(true));
  expect(screen.getByTestId("notification-field-excerpt").textContent).toContain("needleRed");
  // The frame stays shut: its words are not where the match is.
  expect((screen.getByTestId("notification-raw-disclosure") as HTMLDetailsElement).open).toBe(false);
});

test("a match the excerpt preview cut away opens the card and its raw frame so the word is on screen", async () => {
  const filler = "padding line\n".repeat(60);
  const text = `<job-notification job_id="job_9" event="completed" job_type="shell" intent="Tail the log">\nJob completed.\nexcerpt:\nquokkaAtHead\n${filler}</job-notification>`;
  renderSession([steer("steer_1", text)]);
  await search("quokkaAtHead");

  const card = cards()[0] as HTMLElement;
  await waitFor(() => expect(cardOpen(card)).toBe(true));
  const raw = screen.getByTestId("notification-raw-disclosure") as HTMLDetailsElement;
  await waitFor(() => expect(raw.open).toBe(true));
  const frame = screen.getByTestId("notification-raw");
  expect(frame.textContent).toContain("quokkaAtHead");
  // Not hidden from Find: marking skips an aria-hidden region, so this is the
  // proof the word is in the region Find paints.
  expect(frame.getAttribute("aria-hidden")).toBeNull();
  // The preview really did cut the word away.
  expect(screen.getByTestId("notification-field-excerpt").textContent).not.toContain("quokkaAtHead");
  await waitFor(() => expect(marked()).not.toBeNull());
});

test("only the card of a steer holding the words opens, and its sibling stays closed", async () => {
  const first =
    '<job-notification job_id="job_1" event="completed" intent="First card">\nJob completed.\nexcerpt:\nfirst output\n</job-notification>';
  const second =
    '<job-notification job_id="job_2" event="completed" intent="Second card">\nJob completed.\nexcerpt:\nsecond output\n</job-notification>';
  renderSession([steer("steer_1", `${first}\n${second}`)]);
  await search("second output");

  const [one, two] = cards() as [HTMLElement, HTMLElement];
  await waitFor(() => expect(cardOpen(two)).toBe(true));
  expect(cardOpen(one)).toBe(false);
});

test("a static delegate head is a match with no disclosure to open", async () => {
  const text =
    '<delegate-notification delegate_id="dlg_9" name="quiet-runner" kind="report">{"kind":"reported","message":"","metadata":{"outcome":"stopped","reason":"stopped_by_parent"}}</delegate-notification>';
  renderSession([steer("steer_1", text)]);
  await search("quiet-runner");

  expect(barCount()).toBe("1");
  // Nothing to expand: the head is the whole row.
  expect(screen.queryByTestId("notification-card-root")).toBeNull();
  await waitFor(() => expect(marked()).not.toBeNull());
});
