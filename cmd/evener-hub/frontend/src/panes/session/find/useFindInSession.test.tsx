// Find against the real thread store and the real paging owner: a scripted
// client answers reads and older pages, and a plain list stands in for the
// virtualized transcript so the marks have anchors to land on.
import {
  entryDisplayKey,
  makeTranscriptDisplayConfig,
  projectThread,
  type Thread,
  type Turn,
  WireError,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useMemo, useRef } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { resetWorkspaceStoreForTests } from "../../../shell/workspace";
import { connectionStore, resetConnectionStoreForTests } from "../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { connectReadyFakeClient } from "../../../testing/connectionTestUtils";
import { testThread } from "../../../threadTestUtils";
import type { VirtualListHandle } from "../../../widgets";
import { transcriptRowsForProjection } from "../transcript/TranscriptBody";
import { resetTranscriptPagingForTests, useTranscript } from "../transcript/useTranscript";
import "../index";
import { FindBar } from "./FindBar";
import { FIND_CURRENT_ATTRIBUTE } from "./findMarks";
import { requestFindInSession, resetFindRequestsForTests } from "./findRequest";
import { useFindInSession } from "./useFindInSession";

const REF = "ref_a";
const CONFIG = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });

function turn(id: string, text: string): Turn {
  return {
    id,
    status: "completed",
    itemsView: "full",
    items: [{ id: `${id}-item`, turnId: id, type: "agentMessage", text, status: "completed" }],
  };
}

const thread = (turns: Turn[]): Thread => testThread(REF, { turns });

// Older history as a chain of pages, newest page first. Each page is one turn.
interface Script {
  pages: string[];
  requests: number;
}

function scriptHistory(client: FakeClient, recent: Turn[], pages: string[]): Script {
  const script: Script = { pages, requests: 0 };
  client.on("thread/read", (params) => ({
    thread: thread(recent),
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: recent.length },
    requestGeneration: params.requestGeneration,
    ...(pages.length > 0 ? { olderCursor: "page-0" } : {}),
  }));
  client.on("thread/turns/list", (params) => {
    script.requests += 1;
    const index = Number(String(params.cursor).split("-")[1]);
    return {
      data: [turn(`old-${index}`, script.pages[index] ?? "")],
      nextCursor: index + 1 < script.pages.length ? `page-${index + 1}` : undefined,
    };
  });
  return script;
}

const jumps: number[] = [];
let ownsKeyboard = true;

function Harness() {
  const transcript = useTranscript(REF, "pane_a");
  const { model } = transcript;
  const rows = useMemo(() => (model ? transcriptRowsForProjection(projectThread(model, CONFIG)) : []), [model]);
  const port = useRef<HTMLDivElement>(null);
  const list = useRef<VirtualListHandle>({
    scrollToIndex: () => {},
    getScrollElement: () => port.current,
    isLayoutCurrent: () => true,
    getVisibleRange: () => null,
  });
  const find = useFindInSession({
    ref: REF,
    paneId: "pane_a",
    rows,
    disclosureScope: "pane_a",
    listRef: list,
    jumpToRow: (index) => jumps.push(index),
    markGesture: () => {},
    olderPageKey: transcript.olderPageKey,
    olderLoading: transcript.loadingOlder,
    olderError: transcript.olderError,
    olderPermanent: transcript.olderPermanent,
    findOlder: transcript.findOlder,
    cancelFindOlder: transcript.cancelFindOlder,
    ownsKeyboard: () => ownsKeyboard,
  });
  return (
    <>
      <button type="button" data-testid="elsewhere">
        elsewhere
      </button>
      <button type="button" data-testid="reader-older" onClick={() => void transcript.loadOlder().catch(() => {})}>
        reader older
      </button>
      {find.view && <FindBar view={find.view} />}
      <div ref={port} data-testid="port">
        {rows.map((row, index) =>
          row.kind === "turn"
            ? row.turn.entries.map((entry) => (
                <div
                  key={entryDisplayKey(entry)}
                  data-view-anchor-id={entryDisplayKey(entry)}
                  data-view-anchor-index={index}
                >
                  {entry.kind === "intent" ? entry.rationale : entry.item.text}
                </div>
              ))
            : null,
        )}
      </div>
    </>
  );
}

async function mount(client: FakeClient): Promise<void> {
  await act(async () => {
    await threadsStore.getState().ensureThread(REF);
  });
  render(<Harness />);
  void client;
}

const bar = () => screen.getByTestId("find-bar");
const field = () => screen.getByRole("searchbox");
const state = () => ({
  status: bar().getAttribute("data-find-status"),
  position: Number(bar().getAttribute("data-find-position")),
  count: Number(bar().getAttribute("data-find-count")),
  more: bar().getAttribute("data-find-more") === "true",
});
const currentAnchor = () =>
  document.querySelector(`[${FIND_CURRENT_ATTRIBUTE}]`)?.getAttribute("data-view-anchor-id") ?? null;

function openFind(): void {
  act(() => {
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "f", code: "KeyF", ctrlKey: true, cancelable: true }));
  });
}

function type(query: string): void {
  fireEvent.change(field(), { target: { value: query } });
}

const older = () => fireEvent.keyDown(field(), { key: "Enter" });
const newer = () => fireEvent.keyDown(field(), { key: "Enter", shiftKey: true });

beforeEach(() => {
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  resetTranscriptPagingForTests();
  resetWorkspaceStoreForTests();
  resetFindRequestsForTests();
  jumps.length = 0;
  ownsKeyboard = true;
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

test("typing searches what is loaded, lands on the newest match, and reads no older history", async () => {
  const client = connectReadyFakeClient();
  const script = scriptHistory(
    client,
    [turn("t1", "a needle"), turn("t2", "nothing"), turn("t3", "Needle again")],
    ["older needle"],
  );
  await mount(client);
  openFind();
  expect(document.activeElement).toBe(field());
  expect(state().status).toBe("idle");

  type("needle");
  expect(state()).toEqual({ status: "matches", position: 1, count: 2, more: true });
  expect(currentAnchor()).toBe("t3-item");
  expect(script.requests).toBe(0);

  older();
  expect(state()).toMatchObject({ position: 2, count: 2 });
  expect(currentAnchor()).toBe("t1-item");
  newer();
  expect(currentAnchor()).toBe("t3-item");
  // The newest match has nothing newer: the step is refused, not wrapped.
  expect(screen.getByTestId("find-newer").hasAttribute("disabled")).toBe(true);
});

test("stepping older past the loaded matches reads older pages until one holds a match", async () => {
  const client = connectReadyFakeClient();
  const script = scriptHistory(client, [turn("t1", "needle here")], ["nothing", "still nothing", "an old needle", "x"]);
  await mount(client);
  openFind();
  type("needle");
  expect(state()).toEqual({ status: "matches", position: 1, count: 1, more: true });

  older();
  await waitFor(() => expect(state()).toMatchObject({ status: "matches", position: 2, count: 2 }));
  expect(currentAnchor()).toBe("old-2-item");
  // Three pages were read to reach it, and the one beyond it was left alone.
  expect(script.requests).toBe(3);
  expect(state().more).toBe(true);
});

test("the answer is 'none' only once the start of history is confirmed", async () => {
  const client = connectReadyFakeClient();
  const script = scriptHistory(client, [turn("t1", "hello")], ["older", "oldest"]);
  await mount(client);
  openFind();
  type("needle");
  // Nothing loaded matches, but history has not been read: not an answer yet.
  expect(state()).toEqual({ status: "unsearched", position: 0, count: 0, more: true });
  expect(script.requests).toBe(0);

  older();
  await waitFor(() => expect(state().status).toBe("none"));
  expect(script.requests).toBe(2);
  expect(state().more).toBe(false);
  expect(screen.getByTestId("find-older").hasAttribute("disabled")).toBe(true);
});

test("a refresh after several pages reads back to the current match instead of losing it", async () => {
  const client = connectReadyFakeClient();
  const script = scriptHistory(client, [turn("t1", "needle here")], ["nothing", "an old needle", "nothing more"]);
  await mount(client);
  openFind();
  type("needle");
  older();
  await waitFor(() => expect(currentAnchor()).toBe("old-1-item"));
  const before = state();
  const pagesRead = script.requests;

  // A refresh replaces the loaded window with the newest page: the page that
  // held the match is gone until it is read again.
  await act(async () => {
    await threadsStore.getState().refreshThread(REF);
  });
  await waitFor(() => expect(currentAnchor()).toBe("old-1-item"));
  expect(state()).toEqual(before);
  expect(script.requests).toBeGreaterThan(pagesRead);
  // The match is a row the reader can see again, on the page that brought it.
  expect(screen.getByTestId("port").textContent).toContain("an old needle");

  // And the search carries on from there: the last page holds nothing more.
  older();
  await waitFor(() => expect(state().more).toBe(false));
  expect(state()).toMatchObject({ status: "matches", position: 2, count: 2 });
});

test("a match whose words go away is no longer current, and nothing is read for it", async () => {
  const client = connectReadyFakeClient();
  const script = scriptHistory(client, [turn("t1", "needle here")], ["older"]);
  await mount(client);
  openFind();
  type("needle");
  expect(currentAnchor()).toBe("t1-item");
  type("needle here and more");
  expect(state()).toMatchObject({ status: "unsearched", position: 0, count: 0 });
  expect(currentAnchor()).toBeNull();
  expect(script.requests).toBe(0);
});

test("a search interrupted by a lost connection resumes on reconnect without another step", async () => {
  vi.useFakeTimers();
  const first = connectReadyFakeClient();
  const recent = [turn("t1", "hello")];
  first.on("thread/read", () => ({ thread: thread(recent), olderCursor: "page-0" }));
  let firstRequests = 0;
  first.on("thread/turns/list", (params) => {
    firstRequests += 1;
    // Two pages load, then the connection goes bad.
    if (firstRequests > 2) throw new Error("socket closed");
    const index = Number(String(params.cursor).split("-")[1]);
    return { data: [turn(`old-${index}`, "nothing")], nextCursor: `page-${index + 1}` };
  });
  await mount(first);
  openFind();
  type("needle");
  older();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(2_000);
  });
  expect(firstRequests).toBeGreaterThan(2);
  expect(state().status).toBe("searching");
  expect(screen.getByTestId("port").textContent).toContain("nothing");

  act(() => connectionStore.setState({ client: null, state: "reconnecting" }));
  expect(state().status).toBe("searching");
  const replacement = new FakeClient("ready");
  replacement.on("thread/read", () => ({ thread: thread(recent), olderCursor: "page-0" }));
  replacement.on("thread/turns/list", (params) => {
    const index = Number(String(params.cursor).split("-")[1]);
    return {
      data: [turn(`old-${index}`, index === 3 ? "the old needle" : "nothing")],
      nextCursor: index < 4 ? `page-${index + 1}` : undefined,
    };
  });
  act(() => connectionStore.getState().connect(replacement));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(60_000);
  });
  expect(state()).toMatchObject({ status: "matches", position: 1, count: 1 });
  expect(currentAnchor()).toBe("old-3-item");
});

test("history that can never be read leaves the search incomplete, with the reason", async () => {
  const client = connectReadyFakeClient();
  client.on("thread/read", () => ({ thread: thread([turn("t1", "hello")]), olderCursor: "page-0" }));
  client.on("thread/turns/list", () => {
    throw new WireError("opaque", -32000, { evenerErrorInfo: "upgradeRequired" });
  });
  await mount(client);
  openFind();
  type("needle");
  older();
  await waitFor(() => expect(state().status).toBe("incomplete"));
  // Not "none": the unread history may hold a match.
  expect(state().more).toBe(true);
  expect(screen.getByRole("alert").textContent).not.toBe("");
});

test("closing Find withdraws Find's demand and leaves the reader's standing", async () => {
  vi.useFakeTimers();
  const client = connectReadyFakeClient();
  client.on("thread/read", () => ({ thread: thread([turn("t1", "hello")]), olderCursor: "page-0" }));
  let attempts = 0;
  client.on("thread/turns/list", () => {
    attempts += 1;
    throw new Error("temporary");
  });
  await mount(client);
  const settle = () =>
    act(async () => {
      await vi.advanceTimersByTimeAsync(120_000);
    });

  // Find alone: its retries stop when it closes.
  openFind();
  type("needle");
  older();
  await settle();
  expect(attempts).toBeGreaterThan(1);
  fireEvent.keyDown(field(), { key: "Escape" });
  expect(screen.queryByTestId("find-bar")).toBeNull();
  await settle();
  const afterClose = attempts;
  await settle();
  expect(attempts).toBe(afterClose);

  // Reader and Find both waiting: closing Find leaves the reader's retries.
  fireEvent.click(screen.getByTestId("reader-older"));
  openFind();
  type("needle");
  older();
  await settle();
  fireEvent.keyDown(field(), { key: "Escape" });
  const withReader = attempts;
  await settle();
  expect(attempts).toBeGreaterThan(withReader);
});

test("changing the words withdraws the search for the old ones", async () => {
  vi.useFakeTimers();
  const client = connectReadyFakeClient();
  client.on("thread/read", () => ({ thread: thread([turn("t1", "hello")]), olderCursor: "page-0" }));
  let attempts = 0;
  client.on("thread/turns/list", () => {
    attempts += 1;
    throw new Error("temporary");
  });
  await mount(client);
  openFind();
  type("needle");
  older();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(60_000);
  });
  type("hello");
  expect(state()).toMatchObject({ status: "matches", count: 1 });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(60_000);
  });
  const settled = attempts;
  await act(async () => {
    await vi.advanceTimersByTimeAsync(120_000);
  });
  expect(attempts).toBe(settled);
});

test("Escape closes Find, clears its marks, and returns focus to where it was", async () => {
  const client = connectReadyFakeClient();
  scriptHistory(client, [turn("t1", "needle")], []);
  await mount(client);
  const elsewhere = screen.getByTestId("elsewhere");
  elsewhere.focus();
  openFind();
  type("needle");
  expect(currentAnchor()).toBe("t1-item");
  // History is fully loaded, so the single match is the whole answer.
  expect(state()).toEqual({ status: "matches", position: 1, count: 1, more: false });

  fireEvent.keyDown(field(), { key: "Escape" });
  expect(screen.queryByTestId("find-bar")).toBeNull();
  expect(currentAnchor()).toBeNull();
  expect(document.activeElement).toBe(elsewhere);
});

test("the find chord belongs to the conversation the keyboard is in", async () => {
  const client = connectReadyFakeClient();
  scriptHistory(client, [turn("t1", "needle")], []);
  await mount(client);
  const press = (target: EventTarget) => {
    const event = new KeyboardEvent("keydown", {
      key: "f",
      code: "KeyF",
      ctrlKey: true,
      cancelable: true,
      bubbles: true,
    });
    act(() => {
      target.dispatchEvent(event);
    });
    return event;
  };

  // Elsewhere in the app the chord stays the browser's.
  ownsKeyboard = false;
  expect(press(window).defaultPrevented).toBe(false);
  expect(screen.queryByTestId("find-bar")).toBeNull();

  ownsKeyboard = true;
  expect(press(window).defaultPrevented).toBe(true);
  expect(document.activeElement).toBe(field());

  // Pressed again inside the bar, it falls through to the browser's own find.
  expect(press(field()).defaultPrevented).toBe(false);
  expect(screen.getByTestId("find-bar")).toBeTruthy();

  // From anywhere else in the conversation it brings focus back to the bar.
  screen.getByTestId("elsewhere").focus();
  expect(press(screen.getByTestId("elsewhere")).defaultPrevented).toBe(true);
  expect(document.activeElement).toBe(field());
});

test("a caller can open Find on its words and on the item it picked", async () => {
  const client = connectReadyFakeClient();
  scriptHistory(client, [turn("t1", "needle one"), turn("t2", "needle two"), turn("t3", "needle three")], []);
  await mount(client);
  act(() => requestFindInSession(REF, { target: "pane_a", query: "needle", itemId: "t1-item" }));
  expect(state()).toEqual({ status: "matches", position: 3, count: 3, more: false });
  expect(currentAnchor()).toBe("t1-item");
  expect((field() as HTMLInputElement).value).toBe("needle");
  expect(document.activeElement).toBe(field());

  // A request for another seat's conversation is not this one's to take.
  fireEvent.keyDown(field(), { key: "Escape" });
  act(() => requestFindInSession(REF, { target: "pane_b", query: "needle" }));
  expect(screen.queryByTestId("find-bar")).toBeNull();
});

test("a match that arrives while Find is open joins the count without moving the reader", async () => {
  const client = connectReadyFakeClient();
  const recent = [turn("t1", "needle one")];
  scriptHistory(client, recent, []);
  await mount(client);
  openFind();
  type("needle");
  expect(state()).toMatchObject({ position: 1, count: 1 });
  jumps.length = 0;

  recent.push(turn("t2", "needle two"));
  await act(async () => {
    await threadsStore.getState().refreshThread(REF);
  });
  await waitFor(() => expect(state().count).toBe(2));
  // The reader's match is now the older of two, and still the current one.
  expect(state().position).toBe(2);
  expect(currentAnchor()).toBe("t1-item");
  expect(jumps).toEqual([]);
});
