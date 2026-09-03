import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, onTestFinished, test } from "vitest";
import type { ConnectionState } from "../../../../protocol/client";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import {
  readMutationPersistence,
  resetThreadsStoreForTests,
  subscribeMutationPersistence,
  threadsStore,
} from "../../../../stores/threads";
import { AskDock } from "./AskDock";
import { askDockStore, resetAskDockStoreForTests } from "./askDockStore";

afterEach(() => {
  cleanup();
  // Every test here calls ensureThread(ref) directly for setup - AskDock
  // takes its ref as a prop and never calls ensureThread/releaseThread
  // itself, so cleanup()'s unmount leaves that ref refcounted after the LAST
  // test. Under isolate:false that is what a later file's own
  // connectionStore.connect() re-triggers via rewireClient.
  resetThreadsStoreForTests();
  // Every test here writes real durable outbox records into this file's own
  // globalThis.indexedDB instance - the beforeEach below only replaces it
  // BEFORE each test, so whatever the LAST test wrote stays installed as the
  // global indexedDB after this file finishes. Under isolate:false that
  // leftover, populated database is what a later file's own default
  // getMutationRuntime() (no setMutationStorageForTests override) discovers
  // and re-pins.
  globalThis.indexedDB = new IDBFactory();
});

// --- fixtures (mirrors askDockStore.test.ts's own harness) ---------------

const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: true,
  goal: true,
  rename: true,
};

function testThread(ref: string): Thread {
  return {
    id: `thr_${ref}`,
    sessionId: `sess_${ref}`,
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "evener",
    evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
  };
}

function readResponse(ref: string): ThreadReadResponse {
  return { thread: testThread(ref) };
}

function connectFakeClient(state: ConnectionState = "ready"): FakeClient {
  const fake = new FakeClient(state);
  connectionStore.getState().connect(fake);
  return fake;
}

function nextMutationPersistence(targetRef: string): Promise<void> {
  return new Promise((resolve) => {
    const unsubscribe = subscribeMutationPersistence((targetRefs) => {
      if (!targetRefs.includes(targetRef)) return;
      unsubscribe();
      resolve();
    });
    onTestFinished(unsubscribe);
  });
}

function askArgs(questions: Array<Record<string, unknown>>): string {
  return JSON.stringify({ questions });
}

const ONE_QUESTION = [{ header: "Deploy?", question: "Ship now?", options: [{ label: "Yes", detail: "" }] }];

function startTurn(fake: FakeClient, ref: string, turnId: string): void {
  fake.emitNotification({
    method: "turn/started",
    params: { threadId: `thr_${ref}`, ref, turn: { id: turnId, status: "inProgress", itemsView: "" } },
  });
}

function ackAskUserCall(
  fake: FakeClient,
  ref: string,
  turnId: string,
  itemId: string,
  callId: string,
  questions: Array<Record<string, unknown>> = ONE_QUESTION,
): void {
  const base = {
    threadId: `thr_${ref}`,
    ref,
    turnId,
    item: {
      type: "commandExecution",
      id: itemId,
      turnId,
      toolName: "ask_user",
      callId,
      argumentsJson: askArgs(questions),
    },
  };
  fake.emitNotification({
    method: "item/started",
    params: { ...base, item: { ...base.item, status: "inProgress" } },
  });
  fake.emitNotification({
    method: "item/completed",
    params: { ...base, item: { ...base.item, status: "completed" } },
  });
}

async function hydrateWithOneAsk(fake: FakeClient, ref = "ref_a"): Promise<void> {
  fake.on("thread/read", () => readResponse(ref));
  await threadsStore.getState().ensureThread(ref);
  startTurn(fake, ref, "turn_1");
  ackAskUserCall(fake, ref, "turn_1", "item_1", "call_1");
}

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetAskDockStoreForTests();
});

test("renders nothing when there is no pending ask for this ref", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");

  const { container } = render(<AskDock ref="ref_a" />);
  expect(container.firstChild).toBeNull();
});

// Dark-theme legibility (UX fix): the amber envelope's default --attention-bg/
// -edge (15%/40% mixes, tokens.css) barely reads as amber against dark
// surfaces. This file is already on the semantic-hue exception list
// (token-contract.test.ts's SEMANTIC_PATH_EXCEPTIONS), so it strengthens the
// mix locally rather than changing the global token (which every OTHER
// --attention consumer also reads).
test("the batch envelope strengthens the amber mix locally, past the token default, for dark-theme legibility", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "askdock.module.css"), "utf8");
  const batchRule = css.match(/\.batch\s*\{([^}]*)\}/);
  expect(batchRule, "askdock.module.css must declare a .batch rule").not.toBeNull();
  const body = batchRule![1]!;
  expect(body).toMatch(/background:\s*color-mix\(in oklab,\s*var\(--attention\)\s*24%,\s*var\(--surface-1\)\)/);
  expect(body).toMatch(/border:\s*1px solid color-mix\(in oklab,\s*var\(--attention\)\s*55%,\s*var\(--edge\)\)/);
});

// Touch target (UX fix): the tab strip's own buttons are dense (kata 99yf's
// question-switcher), so a coarse pointer (phone/tablet touch) needs the
// platform's 44px tap floor - the same --tap-min button.module.css's own
// phone block already applies to Button, mirrored here via the (pointer:
// coarse) media feature since this is a plain <button>, not that widget.
test("the tab strip buttons reach the tap floor on a coarse pointer", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "askdock.module.css"), "utf8");
  const coarse = css.match(/@media \(pointer: coarse\) \{([\s\S]*?)\n\}/);
  expect(coarse, "askdock.module.css must have a (pointer: coarse) media block").not.toBeNull();
  const rule = coarse![1]!.match(/\.tab\s*\{([^}]*)\}/);
  expect(rule, "the coarse-pointer block must override .tab").not.toBeNull();
  expect(rule![1]).toContain("min-height: var(--tap-min)");
});

// The dock is the transcript's trailing virtual row now (Session.tsx passes
// it as TranscriptBody's trailingRow): it must size to its content so the
// list's measureElement can measure it - a max-height or overflow of its own
// would clip the batch behind an internal scrollbar and lie to the
// measurement, and any flex-basis sizing belongs to a footer slot it no
// longer occupies. It centers on the transcript's 76rem reading measure
// (turnblock.module.css's --session-measure).
test("the dock sizes to its content as a transcript row, with no internal scroll boundary", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "askdock.module.css"), "utf8");
  const rule = css.match(/\.dock\s*\{([^}]*)\}/);
  expect(rule, "askdock.module.css must declare a .dock rule").not.toBeNull();
  const body = rule![1]!;
  expect(body).toContain("max-width: 76rem");
  expect(body).toContain("margin-inline: auto");
  expect(body).not.toContain("max-height");
  expect(body).not.toContain("overflow");
  expect(body).not.toContain("flex:");
});

test("renders the pending question's header and question text once acked", async () => {
  const fake = connectFakeClient();
  await hydrateWithOneAsk(fake);

  render(<AskDock ref="ref_a" />);

  expect(screen.getByText("Deploy?")).toBeTruthy();
  expect(screen.getByText("Ship now?")).toBeTruthy();
});

test("a question acked after the dock is already mounted appears without remounting the whole dock", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");
  render(<AskDock ref="ref_a" />);
  expect(screen.queryByText("Deploy?")).toBeNull();

  await act(async () => {
    startTurn(fake, "ref_a", "turn_1");
    ackAskUserCall(fake, "ref_a", "turn_1", "item_1", "call_1");
  });

  expect(screen.getByText("Deploy?")).toBeTruthy();
});

test("shows an N of M answered footer count that updates as questions are answered", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  const twoQuestions = [
    { header: "First", question: "q1", options: [{ label: "a", detail: "b" }] },
    { header: "Second", question: "q2", options: [{ label: "c", detail: "d" }] },
  ];
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");
  startTurn(fake, "ref_a", "turn_1");
  ackAskUserCall(fake, "ref_a", "turn_1", "item_1", "call_1", twoQuestions);

  render(<AskDock ref="ref_a" />);

  expect(screen.getByText(/0 of 2 questions answered/i)).toBeTruthy();
  await user.click(screen.getByRole("radio", { name: "a" }));
  expect(screen.getByText(/1 of 2 questions answered/i)).toBeTruthy();
});

test("Send is enabled even with nothing answered - an unresolved question composes as skip", async () => {
  const fake = connectFakeClient();
  await hydrateWithOneAsk(fake);
  render(<AskDock ref="ref_a" />);

  const sendBtn = screen.getByRole("button", { name: /send answers/i });
  expect(sendBtn).not.toHaveProperty("disabled", true);
});

test("clicking Send composes and submits through the plain send() path, then the settled batch disappears", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  await hydrateWithOneAsk(fake);
  fake.on("turn/start", () => new Promise(() => {}));
  render(<AskDock ref="ref_a" />);

  const persisted = nextMutationPersistence("ref_a");
  await user.click(screen.getByRole("button", { name: /send answers/i }));
  await persisted;

  const [record] = (await readMutationPersistence("ref_a")).outbox;
  expect(record?.payload).toMatchObject({
    ref: "ref_a",
    input: [{ type: "text", text: "[answers]\n1. [Deploy?] → skipped (no answer)" }],
  });
  await waitFor(() => expect(askDockStore.getState().byRef.get("ref_a")?.batches ?? []).toEqual([]));
  expect(screen.queryByText("Deploy?")).toBeNull();
});

test("Escape does not dismiss the dock or clear any in-progress selection", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  await hydrateWithOneAsk(fake);
  render(<AskDock ref="ref_a" />);

  await user.click(screen.getByRole("radio", { name: "Yes" }));
  await user.keyboard("{Escape}");

  expect(screen.getByText("Deploy?")).toBeTruthy();
  expect(screen.getByRole("radio", { name: "Yes" })).toHaveProperty("checked", true);
});

test("auto-focuses the first answer control when the dock first activates", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");
  render(<AskDock ref="ref_a" />);

  await act(async () => {
    startTurn(fake, "ref_a", "turn_1");
    ackAskUserCall(fake, "ref_a", "turn_1", "item_1", "call_1");
  });

  expect(document.activeElement).toBe(screen.getByRole("radio", { name: "Yes" }));
});

test("does not steal focus from an in-progress answer when a later ask_user call adds more questions", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  await hydrateWithOneAsk(fake);
  render(<AskDock ref="ref_a" />);
  await user.click(screen.getByRole("radio", { name: /something else/i }));
  const freeInput = screen.getByPlaceholderText(/type your answer/i);
  await user.type(freeInput, "partial");
  expect(document.activeElement).toBe(freeInput);

  await act(async () => {
    ackAskUserCall(fake, "ref_a", "turn_1", "item_2", "call_2", [
      { header: "Second", question: "q2", options: [{ label: "x", detail: "y" }] },
    ]);
  });

  expect(document.activeElement).toBe(freeInput);
  expect((freeInput as HTMLInputElement).value).toBe("partial");
});

test("useAskDockPending reports whether a given ref currently has a pending ask", async () => {
  const { useAskDockPending } = await import("./AskDock");
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");

  function Probe() {
    return <span>{useAskDockPending("ref_a") ? "pending" : "clear"}</span>;
  }
  render(<Probe />);
  expect(screen.getByText("clear")).toBeTruthy();
  cleanup();

  startTurn(fake, "ref_a", "turn_1");
  ackAskUserCall(fake, "ref_a", "turn_1", "item_1", "call_1");
  render(<Probe />);
  expect(screen.getByText("pending")).toBeTruthy();
});

test("has no residual askDockStore state for this ref after the batch settles and the component unmounts", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  await hydrateWithOneAsk(fake);
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_2", status: "inProgress", itemsView: "" },
  }));
  const { unmount } = render(<AskDock ref="ref_a" />);

  await user.click(screen.getByRole("button", { name: /send answers/i }));
  await waitFor(() => expect(askDockStore.getState().byRef.get("ref_a")?.batches ?? []).toEqual([]));
  unmount();

  expect(askDockStore.getState().byRef.get("ref_a")?.batches ?? []).toEqual([]);
});

// --- kata 99yf: one question on screen at a time, behind a tab strip ------

const TWO_QUESTIONS = [
  { header: "First", question: "q1", options: [{ label: "a", detail: "b" }] },
  { header: "Second", question: "q2", options: [{ label: "c", detail: "d" }] },
];

async function hydrateWithTwoAsk(
  fake: FakeClient,
  questions: Array<Record<string, unknown>> = TWO_QUESTIONS,
): Promise<void> {
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");
  startTurn(fake, "ref_a", "turn_1");
  ackAskUserCall(fake, "ref_a", "turn_1", "item_1", "call_1", questions);
}

test("a multi-question batch shows only the first question behind a tab strip", async () => {
  const fake = connectFakeClient();
  await hydrateWithTwoAsk(fake);
  render(<AskDock ref="ref_a" />);

  expect(screen.getByRole("tablist", { name: "Questions" })).toBeTruthy();
  expect(screen.getByRole("tab", { name: /1\. First/ }).getAttribute("aria-selected")).toBe("true");
  expect(screen.getByRole("tab", { name: /2\. Second/ }).getAttribute("aria-selected")).toBe("false");
  expect(screen.getByText("q1")).toBeTruthy();
  // The whole point of the kata: the second question is NOT on the screen.
  expect(screen.queryByText("q2")).toBeNull();
});

test("a single-question batch renders no tab strip", async () => {
  const fake = connectFakeClient();
  await hydrateWithOneAsk(fake);
  render(<AskDock ref="ref_a" />);

  expect(screen.queryByRole("tablist")).toBeNull();
  expect(screen.getByText("Ship now?")).toBeTruthy();
});

test("clicking a tab switches the visible question", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  await hydrateWithTwoAsk(fake);
  render(<AskDock ref="ref_a" />);

  await user.click(screen.getByRole("tab", { name: /2\. Second/ }));

  expect(screen.queryByText("q1")).toBeNull();
  expect(screen.getByText("q2")).toBeTruthy();
  expect(screen.getByRole("tab", { name: /2\. Second/ }).getAttribute("aria-selected")).toBe("true");
});

test("a one-click answer auto-advances to the next unanswered question and checks its tab", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  await hydrateWithTwoAsk(fake);
  render(<AskDock ref="ref_a" />);

  await user.click(screen.getByRole("radio", { name: "a" }));

  expect(screen.queryByText("q1")).toBeNull();
  expect(screen.getByText("q2")).toBeTruthy();
  // The answered tab keeps its place but gains the answered marker (a
  // visually-hidden "(answered)" for AT, a check glyph on screen).
  expect(screen.getByRole("tab", { name: /1\. First \(answered\)/ })).toBeTruthy();
  expect(screen.getByText(/1 of 2 questions answered/i)).toBeTruthy();
});

test("a multi-select answer does not auto-advance - more boxes may follow", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  await hydrateWithTwoAsk(fake, [
    {
      header: "Pick",
      question: "q1",
      multi_select: true,
      options: [
        { label: "a", detail: "b" },
        { label: "c", detail: "d" },
      ],
    },
    { header: "Second", question: "q2", options: [{ label: "e", detail: "f" }] },
  ]);
  render(<AskDock ref="ref_a" />);

  await user.click(screen.getByRole("checkbox", { name: "a" }));

  expect(screen.getByText("q1")).toBeTruthy();
  expect(screen.queryByText("q2")).toBeNull();
});

test("arrow keys move the active tab within the strip", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  await hydrateWithTwoAsk(fake);
  render(<AskDock ref="ref_a" />);

  const firstTab = screen.getByRole("tab", { name: /1\. First/ });
  firstTab.focus();
  await user.keyboard("{ArrowRight}");

  const secondTab = screen.getByRole("tab", { name: /2\. Second/ });
  expect(document.activeElement).toBe(secondTab);
  expect(secondTab.getAttribute("aria-selected")).toBe("true");
  expect(screen.getByText("q2")).toBeTruthy();

  await user.keyboard("{ArrowLeft}");
  expect(document.activeElement).toBe(firstTab);
  expect(screen.getByText("q1")).toBeTruthy();
});

test("a late-arriving question growing the batch keeps the in-progress answer's focus", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  await hydrateWithOneAsk(fake);
  render(<AskDock ref="ref_a" />);
  await user.click(screen.getByRole("radio", { name: /something else/i }));
  const freeInput = screen.getByPlaceholderText(/type your answer/i);
  await user.type(freeInput, "partial");
  expect(document.activeElement).toBe(freeInput);

  // The batch grows 1 -> 2 questions: the tab strip appears and the
  // in-progress card must survive that exact re-render with focus intact.
  await act(async () => {
    ackAskUserCall(fake, "ref_a", "turn_1", "item_2", "call_2", [
      { header: "Second", question: "q2", options: [{ label: "x", detail: "y" }] },
    ]);
  });

  expect(screen.getByRole("tablist", { name: "Questions" })).toBeTruthy();
  expect(document.activeElement).toBe(freeInput);
  expect((freeInput as HTMLInputElement).value).toBe("partial");
});

// --- kata w2zy: the primary button advances before it submits ------------
//
// AskDock's footer has exactly one primary action (the bottom-right
// button). Before this kata it was hardcoded to "Send answers" and always
// submitted the WHOLE batch on click, even with other questions still
// unanswered - correct for a single question (there is nothing else to do
// with it) but wrong once kata 99yf's one-question-at-a-time tab strip
// shipped: a free-text/multi-select answer (askDockStore's advancesOnAnswer
// intentionally never auto-advances those - more typing or more boxes may
// follow) leaves the reader looking at the question they just answered, and
// the only button on screen would silently submit everything else as
// skipped. (The ask-dialog UX rework later widened this into the full walk
// model - see the next section.)

test("the primary button advances to the next unanswered question, not sends, once the current one is answered", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  await hydrateWithTwoAsk(fake);
  fake.on("turn/start", () => new Promise(() => {}));
  render(<AskDock ref="ref_a" />);

  // Free text does not auto-advance (kata 99yf), so after answering Q1
  // this way the reader is still looking at Q1 - Jesse's exact repro: one
  // answer filled in, the bottom-right button is the only next move.
  await user.click(screen.getByRole("radio", { name: /something else/i }));
  await user.type(screen.getByPlaceholderText(/type your answer/i), "custom answer");
  expect(screen.getByText("q1")).toBeTruthy();

  await user.click(screen.getByRole("button", { name: /next question/i }));

  expect(screen.getByText("q2")).toBeTruthy();
  expect(screen.queryByText("q1")).toBeNull();
  expect(fake.calls.some((c) => c.method === "turn/start")).toBe(false);
});

test("the primary button sends once the last unanswered question is answered - the final advance position", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  await hydrateWithTwoAsk(fake);
  fake.on("turn/start", () => new Promise(() => {}));
  render(<AskDock ref="ref_a" />);

  await user.click(screen.getByRole("radio", { name: /something else/i }));
  await user.type(screen.getByPlaceholderText(/type your answer/i), "first");
  await user.click(screen.getByRole("button", { name: /next question/i }));

  // Answer Q2 with another non-auto-advancing resolution kind - nothing is
  // left unanswered once this lands, so the button's job reverts to send.
  await user.click(screen.getByRole("radio", { name: /something else/i }));
  await user.type(screen.getByPlaceholderText(/type your answer/i), "second");

  const persisted = nextMutationPersistence("ref_a");
  await user.click(screen.getByRole("button", { name: /send answers/i }));
  await persisted;

  const [record] = (await readMutationPersistence("ref_a")).outbox;
  expect(record?.payload).toMatchObject({ ref: "ref_a" });
});

// --- ask-dialog UX rework: the primary button walks every question --------
//
// The button is "Next question" on every question but the last - answered or
// not - so a multi-question batch is a walk the reader takes, not a form
// they can submit past. "Send answers" only appears on the last question,
// and stays DISABLED while any question lacks an answer (the last one
// included): with every recommended default pre-selected, walking the batch
// is all it takes, and skipping through it is impossible.

test("the primary button reads Next question on any question but the last, answered or not", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  await hydrateWithTwoAsk(fake);
  fake.on("turn/start", () => new Promise(() => {}));
  render(<AskDock ref="ref_a" />);

  // Q1 is unanswered, yet the only move the footer offers is onward.
  await user.click(screen.getByRole("button", { name: /next question/i }));

  expect(screen.getByText("q2")).toBeTruthy();
  expect(screen.queryByText("q1")).toBeNull();
  expect(fake.calls.some((c) => c.method === "turn/start")).toBe(false);
});

test("on the last question with an earlier one still unanswered, the button wraps back to it", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  await hydrateWithTwoAsk(fake);
  render(<AskDock ref="ref_a" />);

  await user.click(screen.getByRole("button", { name: /next question/i })); // -> q2
  expect(screen.getByText("q2")).toBeTruthy();

  await user.click(screen.getByRole("button", { name: /next question/i })); // q1 still open: wrap
  expect(screen.getByText("q1")).toBeTruthy();
  expect(screen.queryByText("q2")).toBeNull();
});

test("Send is disabled on the last question while it is the only unanswered one", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  await hydrateWithTwoAsk(fake);
  render(<AskDock ref="ref_a" />);

  await user.click(screen.getByRole("radio", { name: "a" })); // auto-advances to q2

  const sendBtn = screen.getByRole("button", { name: /send answers/i });
  expect(sendBtn).toHaveProperty("disabled", true);
});

test("a recommended option starts selected and counts as answered", async () => {
  const fake = connectFakeClient();
  await hydrateWithTwoAsk(fake, [
    {
      header: "Design",
      question: "Approve?",
      options: [
        { label: "Approve", detail: "go", recommended: true },
        { label: "Revise", detail: "no" },
      ],
    },
  ]);
  render(<AskDock ref="ref_a" />);

  expect(screen.getByRole("radio", { name: "Approve" })).toHaveProperty("checked", true);
  expect(screen.getByText(/1 of 1 question answered/i)).toBeTruthy();
});

// --- keyboard submit (UX fix): Mod+Enter on the batch, plain Enter in the
// single-line free-text answer input, both invoking the same primary action
// a click on the footer button already does.

test("Mod+Enter on the batch invokes the primary action (send) for a single-question batch", async () => {
  const fake = connectFakeClient();
  await hydrateWithOneAsk(fake);
  fake.on("turn/start", () => new Promise(() => {}));
  render(<AskDock ref="ref_a" />);

  const persisted = nextMutationPersistence("ref_a");
  fireEvent.keyDown(screen.getByRole("radio", { name: "Yes" }), { key: "Enter", metaKey: true });
  await persisted;

  const [record] = (await readMutationPersistence("ref_a")).outbox;
  expect(record?.payload).toMatchObject({ ref: "ref_a" });
});

test("Mod+Enter with ctrlKey also invokes the primary action", async () => {
  const fake = connectFakeClient();
  await hydrateWithOneAsk(fake);
  fake.on("turn/start", () => new Promise(() => {}));
  render(<AskDock ref="ref_a" />);

  const persisted = nextMutationPersistence("ref_a");
  fireEvent.keyDown(screen.getByRole("radio", { name: "Yes" }), { key: "Enter", ctrlKey: true });
  await persisted;

  const [record] = (await readMutationPersistence("ref_a")).outbox;
  expect(record?.payload).toMatchObject({ ref: "ref_a" });
});

test("Mod+Enter advances rather than sends when another question is still unanswered", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  await hydrateWithTwoAsk(fake);
  fake.on("turn/start", () => new Promise(() => {}));
  render(<AskDock ref="ref_a" />);

  await user.click(screen.getByRole("radio", { name: /something else/i }));
  await user.type(screen.getByPlaceholderText(/type your answer/i), "custom answer");
  fireEvent.keyDown(screen.getByPlaceholderText(/type your answer/i), { key: "Enter", metaKey: true });

  expect(screen.getByText("q2")).toBeTruthy();
  expect(screen.queryByText("q1")).toBeNull();
  expect(fake.calls.some((c) => c.method === "turn/start")).toBe(false);
});

test("plain Enter in the free-text answer input invokes the primary action", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  await hydrateWithOneAsk(fake);
  fake.on("turn/start", () => new Promise(() => {}));
  render(<AskDock ref="ref_a" />);

  await user.click(screen.getByRole("radio", { name: /something else/i }));
  await user.type(screen.getByPlaceholderText(/type your answer/i), "custom answer");

  const persisted = nextMutationPersistence("ref_a");
  fireEvent.keyDown(screen.getByPlaceholderText(/type your answer/i), { key: "Enter" });
  await persisted;

  const [record] = (await readMutationPersistence("ref_a")).outbox;
  expect(record?.payload).toMatchObject({ ref: "ref_a" });
});

test("plain Enter elsewhere in the batch (not the free-text input) does not invoke the primary action", async () => {
  const fake = connectFakeClient();
  await hydrateWithOneAsk(fake);
  fake.on("turn/start", () => new Promise(() => {}));
  render(<AskDock ref="ref_a" />);

  fireEvent.keyDown(screen.getByRole("radio", { name: "Yes" }), { key: "Enter" });

  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(fake.calls.some((c) => c.method === "turn/start")).toBe(false);
});

test("Mod+Enter is ignored while an IME composition is in progress", async () => {
  const fake = connectFakeClient();
  await hydrateWithOneAsk(fake);
  fake.on("turn/start", () => new Promise(() => {}));
  render(<AskDock ref="ref_a" />);

  fireEvent.keyDown(screen.getByRole("radio", { name: "Yes" }), { key: "Enter", metaKey: true, isComposing: true });

  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(fake.calls.some((c) => c.method === "turn/start")).toBe(false);
});

// The dock is the transcript's trailing virtual row (Session.tsx), so
// scrolling far away UNMOUNTs it and scrolling back remounts it. Activation
// auto-focus must fire once per pending set, not once per mount: a remount
// of an already-pending dock must leave focus wherever the reader put it
// (roborev PR #854). The edge therefore lives in askDockStore, not in a
// component ref that resets on every mount.
test("a virtualized remount of an already-pending dock does not steal focus", async () => {
  const fake = connectFakeClient();
  await hydrateWithOneAsk(fake);

  const first = render(<AskDock ref="ref_a" />);
  // The initial activation contract is unchanged: first appearance focuses
  // the first answer control.
  const firstDock = document.querySelector("[data-ask-response-dock]");
  expect(firstDock?.contains(document.activeElement)).toBe(true);
  first.unmount();

  // The reader has scrolled away and put focus elsewhere.
  const elsewhere = render(
    <button type="button" data-testid="elsewhere">
      x
    </button>,
  );
  elsewhere.getByTestId("elsewhere").focus();

  render(<AskDock ref="ref_a" />);

  expect(document.activeElement).toBe(elsewhere.getByTestId("elsewhere"));
});

test("a fresh pending set after a fully resolved one re-activates auto-focus", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  await hydrateWithOneAsk(fake);
  fake.on("turn/start", () => new Promise(() => {}));
  const first = render(<AskDock ref="ref_a" />);
  await user.click(screen.getByRole("button", { name: /send answers/i }));
  await waitFor(() => expect(askDockStore.getState().byRef.get("ref_a")?.batches ?? []).toEqual([]));
  first.unmount();

  // A second, genuinely new question arrives after everything resolved.
  startTurn(fake, "ref_a", "turn_2");
  ackAskUserCall(fake, "ref_a", "turn_2", "item_2", "call_2");
  render(<AskDock ref="ref_a" />);

  const dock = document.querySelector("[data-ask-response-dock]");
  await waitFor(() => expect(dock?.contains(document.activeElement)).toBe(true));
});
