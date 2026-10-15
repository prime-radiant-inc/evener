// Reader correction candidate tests (SECOND-review fixes).
//
// Drives the REAL owning helpers/compositions in
// frontend/scripts/backgroundtranscriptguard/run.mjs with injected browser
// boundaries, so helpers the journey does not use cannot pass.
//
// @vitest-environment jsdom

import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { createElement } from "react";
import { afterAll, afterEach, beforeAll, describe, expect, test } from "vitest";
import { ToolCallItem } from "../../src/panes/session/transcript/ToolCallItem";
import { TranscriptRenderProvider } from "../../src/transcriptDisplay/renderContext";
import "../../src/panes/session/transcript/tools/shellTool"; // registers the real "shell" descriptor
import { connectionStore } from "../../src/stores/connection";
import { resetDisclosureStoreForTests } from "../../src/widgets/disclosure/disclosureStore";
import * as guard from "./run.mjs";

const REF = "local:tgt";
const _PRODUCER = "local:alpha";
const TOOL_OUTPUT = "BACKGROUND_TOOL_OUTPUT";
const AFTER = "BACKGROUND_AFTER_EVICTION";
const FULL_WORKLOAD = "W".repeat(16 * 1024);
const COMMAND = `printf '${TOOL_OUTPUT}'`;
const sentinel = (n) => `BACKGROUND_HISTORY_${String(n).padStart(2, "0")}`;
const ASSISTANT = "BACKGROUND_HISTORY_REPLY";

const scratchRoot = mkdtempSync(path.join(process.env.EVENER_SCRATCH_DIR ?? tmpdir(), "reader-outcome-red-"));

function frame(connection, direction, body) {
  return JSON.stringify({ event: "frame", connection, direction, frame: JSON.stringify(body) });
}
const readReq = (connection, id) =>
  frame(connection, "browser_to_hub", {
    id,
    method: "thread/read",
    params: { ref: REF, includeTurns: true, subscribe: true, itemLimit: 40 },
  });
const readRes = (connection, id, cursor, turns) =>
  frame(connection, "hub_to_browser", {
    id,
    result: { olderCursor: cursor, ...(turns === undefined ? {} : { thread: { turns } }) },
  });
const sameConnPath = path.join(scratchRoot, "same-connection.jsonl");
const newConnPath = path.join(scratchRoot, "new-connection.jsonl");
writeFileSync(
  sameConnPath,
  [readReq("conn-1", 1), readRes("conn-1", 1, "c1"), readReq("conn-1", 2), readRes("conn-1", 2, "c2")].join("\n"),
);

// Wire shape result.thread.turns, with the real argumentsJson field and status.
const WINDOW_TURNS = [
  { id: "turn_system", items: [{ id: "s1", type: "agentMessage", text: "system prompt", status: "completed" }] },
  {
    id: "turn_new",
    items: [
      { id: "u1", type: "userMessage", text: "please run", status: "completed" },
      {
        id: "c1",
        type: "commandExecution",
        text: "",
        output: "TOOL_OUTPUT_WINDOW",
        argumentsJson: '{"command":"run"}',
        status: "completed",
      },
      { id: "a1", type: "agentMessage", text: "ran it", status: "completed" },
    ],
  },
];
writeFileSync(
  newConnPath,
  [
    readReq("conn-1", 1),
    readRes("conn-1", 1, "c1"),
    readReq("conn-2", 1),
    readRes("conn-2", 1, "c2", WINDOW_TURNS),
  ].join("\n"),
);

function appliedWindow(overrides = {}) {
  return {
    cursor: "c2",
    items: [
      {
        turnId: "turn_new",
        id: "u1",
        type: "userMessage",
        text: "please run",
        output: "",
        arguments: undefined,
        status: "completed",
      },
      {
        turnId: "turn_new",
        id: "c1",
        type: "commandExecution",
        text: "",
        output: "TOOL_OUTPUT_WINDOW",
        arguments: '{"command":"run"}',
        status: "completed",
      },
      {
        turnId: "turn_new",
        id: "a1",
        type: "agentMessage",
        text: "ran it",
        output: "",
        arguments: undefined,
        status: "completed",
      },
    ],
    ...overrides,
  };
}

let evidenceModel = null;
beforeAll(() => {
  Object.defineProperty(window, "backgroundTranscriptEvidence", {
    configurable: true,
    value: Object.freeze({
      connectionState: () => connectionStore.getState().state,
      model: () => evidenceModel,
    }),
  });
});
afterAll(() => rmSync(scratchRoot, { recursive: true, force: true }));
afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
  connectionStore.setState({ state: "idle" });
  evidenceModel = null;
  document.body.innerHTML = "";
});

function evalExpr(expr) {
  return new Function(`return (${expr});`)();
}
function fakeDriver({ online = true } = {}) {
  const send = (method, params = {}) => {
    if (method !== "Runtime.evaluate") return Promise.resolve({ result: {} });
    const value = params.expression === "navigator.onLine" ? online : evalExpr(params.expression);
    return Promise.resolve({ result: { result: { value } } });
  };
  return {
    send,
    waitPage: async (expr) => {
      const seen = evalExpr(expr);
      if (!seen) throw new Error("waitPage: not ready");
      return seen;
    },
    click: async (selector) => {
      const el = document.querySelector(selector);
      if (!el) throw new Error(`click: no element ${selector}`);
      fireEvent.click(el);
    },
  };
}
function domainOf(ids, cursor) {
  return {
    cursor,
    items: ids.map((id) => ({
      turnId: "t",
      id,
      type: "commandExecution",
      text: id,
      output: "",
      arguments: undefined,
      status: "completed",
    })),
  };
}
function structuredItem(overrides = {}) {
  return {
    turnId: "t",
    id: "i1",
    type: "commandExecution",
    text: "same",
    output: "same",
    arguments: '{"a":1}',
    status: "completed",
    ...overrides,
  };
}
function renderScroller({ scrollTop = 0, rows = [], scroller = true, rowId = true }) {
  const pane = document.createElement("div");
  pane.setAttribute("data-pane-scaffold", `session:${REF}`);
  if (!scroller) {
    document.body.appendChild(pane);
    return;
  }
  const list = document.createElement("div");
  list.setAttribute("data-testid", "transcript-virtual-list");
  const port = document.createElement("div");
  Object.defineProperty(port, "scrollTop", { value: scrollTop, writable: true });
  port.getBoundingClientRect = () => ({ top: 0, bottom: 400, left: 0, right: 100, width: 100, height: 400 });
  for (const row of rows) {
    const el = document.createElement("div");
    el.setAttribute("data-testid", "transcript-row");
    // The real transcript row exposes data-row-id (TranscriptBody.tsx:422/438/483).
    if (rowId) el.setAttribute("data-row-id", row.itemId);
    el.getBoundingClientRect = () => ({
      top: row.top,
      bottom: row.bottom,
      left: 0,
      right: 100,
      width: 100,
      height: row.bottom - row.top,
    });
    port.appendChild(el);
  }
  list.appendChild(port);
  pane.appendChild(list);
  document.body.appendChild(pane);
}
function _renderPaneText(text) {
  const pane = document.createElement("div");
  pane.setAttribute("data-pane-scaffold", `session:${REF}`);
  pane.textContent = text;
  document.body.appendChild(pane);
}

// --- finding 2: shell OUTPUT region ---

const toolsConfig = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
const shellItem = {
  id: "item_shell",
  turnId: "turn_1",
  type: "commandExecution",
  text: "",
  toolName: "shell",
  argumentsJSON: JSON.stringify({ command: COMMAND }),
  output: TOOL_OUTPUT,
  status: "completed",
};
const turn = { id: "turn_1", status: "completed", items: [shellItem] };
function renderShellPane() {
  const pane = document.createElement("div");
  pane.setAttribute("data-pane-scaffold", `session:${REF}`);
  document.body.appendChild(pane);
  render(
    createElement(
      TranscriptRenderProvider,
      { config: toolsConfig, surface: "readOnly", disclosureScope: "reader-outcome" },
      createElement(ToolCallItem, { item: shellItem, turn, live: false }),
    ),
    { container: pane },
  );
}
describe("finding 2: shell OUTPUT region", () => {
  test("control: the real 'tools' default mounts the body collapsed", () => {
    renderShellPane();
    expect(screen.queryByTestId("tool-call-body")).toBeNull();
    expect(evalExpr(guard.renderedOutputExpr(REF))).toEqual({ shellRows: 1, outputRegions: 0, outputs: [] });
  });
  test("repaired: readShellOutputRegion opens the disclosure and reads the output", async () => {
    renderShellPane();
    // jsdom performs no layout, so every element reports a zero box. The harness
    // (run.mjs readShellOutputRegion) now waits for the disclosure control to be
    // present AND have a non-zero box before clicking it - a real precondition on
    // a genuinely asynchronous UI state, not a blind retry. Give the real
    // rendered control the box a laid-out browser would, so the wait can pass.
    const trigger = document.querySelector(
      "[data-pane-scaffold='session:local:tgt'] [data-testid='tool-call-item'] button[aria-controls]",
    );
    expect(trigger).not.toBeNull();
    trigger.getBoundingClientRect = () => ({
      x: 0,
      y: 0,
      top: 0,
      left: 0,
      right: 12,
      bottom: 12,
      width: 12,
      height: 12,
    });
    expect(await guard.readShellOutputRegion(fakeDriver(), REF, TOOL_OUTPUT, 80)).toEqual({
      shellRows: 1,
      outputRegions: 1,
      outputs: [TOOL_OUTPUT],
    });
    expect(screen.queryByTestId("tool-call-body")).not.toBeNull();
  });
});

// --- finding 1: exact normalized reconnect window (result.thread.turns) ---

describe("finding 1: reconnect window", () => {
  const before = { readResponses: 1, connections: ["conn-1"] };

  test("required: the exact normalized latest window is accepted", () => {
    const ev = guard.targetPagingEvidence(REF, newConnPath);
    expect(guard.reconnectValidator(before, ev, { cursor: "c2", domain: appliedWindow() }).appliedMatches).toBe(true);
  });
  test("required: a model with equivalent argumentsJSON canonicalizes and matches", () => {
    const ev = guard.targetPagingEvidence(REF, newConnPath);
    const equivalent = appliedWindow({
      items: appliedWindow().items.map((i) =>
        i.id === "c1" ? { ...i, arguments: undefined, argumentsJSON: '{"command":"run"}' } : i,
      ),
    });
    expect(guard.reconnectValidator(before, ev, { cursor: "c2", domain: equivalent }).appliedMatches).toBe(true);
  });
  test("RED: a changed wire argument is rejected", () => {
    const ev = guard.targetPagingEvidence(REF, newConnPath);
    const changed = appliedWindow({
      items: appliedWindow().items.map((i) => (i.id === "c1" ? { ...i, arguments: '{"command":"other"}' } : i)),
    });
    expect(guard.reconnectValidator(before, ev, { cursor: "c2", domain: changed }).appliedMatches).toBe(false);
  });
  test("RED: wrong applied content is rejected", () => {
    const ev = guard.targetPagingEvidence(REF, newConnPath);
    const wrong = appliedWindow({
      items: appliedWindow().items.map((i) => (i.id === "a1" ? { ...i, text: "WRONG" } : i)),
    });
    expect(guard.reconnectValidator(before, ev, { cursor: "c2", domain: wrong }).appliedMatches).toBe(false);
  });
  test("RED: extra stale content inside the read's own returned turn is rejected", () => {
    // The reconnect validator compares per returned turn by turnId (run.mjs
    // reconnectValidator) because the ordinary entry route deliberately keeps the
    // history a reader had already loaded, so an EXTRA older turn outside the
    // reconnect read's window is now tolerated by design. Extra RECORDED content
    // inside a turn the read did return is still a mismatch of the applied
    // window against the read, and must still be rejected.
    const ev = guard.targetPagingEvidence(REF, newConnPath);
    const extra = appliedWindow({
      items: [
        { turnId: "turn_new", id: "extra1", type: "agentMessage", text: "stale", output: "", status: "completed" },
        ...appliedWindow().items,
      ],
    });
    expect(guard.reconnectValidator(before, ev, { cursor: "c2", domain: extra }).appliedMatches).toBe(false);
  });
  test("RED: a missing tool output is rejected", () => {
    const ev = guard.targetPagingEvidence(REF, newConnPath);
    const missing = appliedWindow({
      items: appliedWindow().items.map((i) => (i.id === "c1" ? { ...i, output: "" } : i)),
    });
    expect(guard.reconnectValidator(before, ev, { cursor: "c2", domain: missing }).appliedMatches).toBe(false);
  });
  test("RED: absent response evidence must not become an empty-window waiver", () => {
    const noWindowPath = path.join(scratchRoot, "no-window.jsonl");
    writeFileSync(noWindowPath, [readReq("conn-2", 1), readRes("conn-2", 1, "c2")].join("\n"));
    const ev = guard.targetPagingEvidence(REF, noWindowPath);
    expect(
      guard.reconnectValidator({ readResponses: 0, connections: [] }, ev, { cursor: "c2", domain: appliedWindow() })
        .appliedMatches,
    ).toBe(false);
  });
  test("repaired: a same-connection refresh is not a reconnect", () => {
    const ev = guard.targetPagingEvidence(REF, sameConnPath);
    expect(guard.reconnectValidator(before, ev, { cursor: "c2", domain: appliedWindow() }).reconnected).toBe(false);
  });
});

// --- finding 2: producer new-operation identity + completion ---

describe("finding 2: producer operation identity and completion", () => {
  const earlierSix = Array.from({ length: 6 }, (_, n) => ({
    turnId: `alpha_old_${n}`,
    id: `old_${n}`,
    type: "agentMessage",
    text: FULL_WORKLOAD,
    output: "",
    status: "completed",
  }));
  const newTurn = (text) => ({
    turnId: "alpha_new",
    id: "new_1",
    type: "agentMessage",
    text,
    output: "",
    status: "completed",
  });

  test("RED: earlier alpha turns must not certify the new turn", () => {
    expect(
      guard.producerOperationApplied(
        { cursor: "", items: earlierSix },
        { turnId: "alpha_new", content: FULL_WORKLOAD, releaseSentinel: AFTER },
      ),
    ).toBe(false);
  });
  test("RED: the new turn applied with the workload but WITHOUT completion is rejected", () => {
    expect(
      guard.producerOperationApplied(
        { cursor: "", items: [...earlierSix, newTurn(FULL_WORKLOAD)] },
        { turnId: "alpha_new", content: FULL_WORKLOAD, releaseSentinel: AFTER },
      ),
    ).toBe(false);
  });
  test("control: the correlated new turn with workload AND completion is accepted", () => {
    expect(
      guard.producerOperationApplied(
        { cursor: "", items: [...earlierSix, newTurn(`${FULL_WORKLOAD}\n${AFTER}`)] },
        { turnId: "alpha_new", content: FULL_WORKLOAD, releaseSentinel: AFTER },
      ),
    ).toBe(true);
  });
});

// --- finding 3: owning holder capture composition + real row attribute ---

describe("finding 3: reader anchor through holderSnapshot", () => {
  const model = {
    olderCursor: "c1",
    turns: [
      {
        id: "t",
        items: [
          { id: "i1", type: "commandExecution", text: "i1", status: "completed" },
          { id: "i2", type: "commandExecution", text: "i2", status: "completed" },
        ],
      },
    ],
  };

  test("repaired: transcriptState captures the visible item from data-row-id with within-item offset", async () => {
    renderScroller({
      scrollTop: 130,
      rows: [
        { itemId: "i2", top: -30, bottom: 70 },
        { itemId: "i3", top: 70, bottom: 170 },
      ],
    });
    const seen = await guard.transcriptState(fakeDriver(), REF);
    expect(seen.anchor).toEqual({ itemId: "i2", offset: 30 });
  });

  test("RED: the owning holderSnapshot composition with a moved anchor is rejected", async () => {
    evidenceModel = model;
    renderScroller({
      scrollTop: 130,
      rows: [
        { itemId: "i2", top: -30, bottom: 70 },
        { itemId: "i3", top: 70, bottom: 170 },
      ],
    });
    const before = await guard.holderSnapshot(fakeDriver(), REF);
    document.body.innerHTML = "";
    renderScroller({
      scrollTop: 160,
      rows: [
        { itemId: "i2", top: -60, bottom: 40 },
        { itemId: "i3", top: 40, bottom: 140 },
      ],
    });
    const after = await guard.holderSnapshot(fakeDriver(), REF);
    expect(guard.holderValidator(before, after).preserved).toBe(false);
  });

  test("control: holderSnapshot with the same anchor preserves", async () => {
    evidenceModel = model;
    renderScroller({
      scrollTop: 130,
      rows: [
        { itemId: "i2", top: -30, bottom: 70 },
        { itemId: "i3", top: 70, bottom: 170 },
      ],
    });
    const before = await guard.holderSnapshot(fakeDriver(), REF);
    const after = await guard.holderSnapshot(fakeDriver(), REF);
    expect(guard.holderValidator(before, after).preserved).toBe(true);
  });

  test("RED: absent rows must not falsely preserve", async () => {
    evidenceModel = model;
    renderScroller({ rows: [] });
    const before = await guard.holderSnapshot(fakeDriver(), REF);
    const after = await guard.holderSnapshot(fakeDriver(), REF);
    expect(guard.holderValidator(before, after).preserved).toBe(false);
  });

  test("RED: an absent scroller must not falsely preserve", async () => {
    evidenceModel = model;
    renderScroller({ scroller: false });
    const before = await guard.holderSnapshot(fakeDriver(), REF);
    const after = await guard.holderSnapshot(fakeDriver(), REF);
    expect(guard.holderValidator(before, after).preserved).toBe(false);
  });

  test("RED: a row missing its identity must not falsely preserve", async () => {
    evidenceModel = model;
    renderScroller({ scrollTop: 130, rowId: false, rows: [{ itemId: "i2", top: -30, bottom: 70 }] });
    const before = await guard.holderSnapshot(fakeDriver(), REF);
    const after = await guard.holderSnapshot(fakeDriver(), REF);
    expect(guard.holderValidator(before, after).preserved).toBe(false);
  });
});

// --- finding 4: reconnect paged range through the owning composition ---

describe("finding 4: reconnectPagedRange composition", () => {
  const expected = [];
  for (let n = 1; n <= 40; n += 1) {
    expected.push({ kind: "prompt", text: sentinel(n) });
    expected.push({ kind: "assistant", text: ASSISTANT });
  }
  const itemFor = (entry, index) => ({
    id: `${entry.kind}-${index}`,
    turnId: "t",
    type: entry.kind === "prompt" ? "userMessage" : "agentMessage",
    text: entry.text,
    status: "completed",
  });
  const allIndices = expected.map((_, index) => index);
  const fromIndex = (start) => allIndices.filter((index) => index >= start);
  const rangeDomain = (indices) => ({ cursor: "", items: indices.map((index) => itemFor(expected[index], index)) });

  // A scripted page boundary: each page advances to the next frame; reads clamp
  // to the latest frame once the pages are exhausted.
  function scripted(frames) {
    let step = 0;
    return {
      read: async () => frames[Math.min(step, frames.length - 1)],
      page: async () => {
        step += 1;
      },
      visible: async () => true,
    };
  }

  test("control: paging continues until the sentinel AND the full range complete", async () => {
    // HISTORY_07..40 initially, then pages prepend HISTORY_05..06 and HISTORY_01..04.
    const deps = scripted([rangeDomain(fromIndex(12)), rangeDomain(fromIndex(8)), rangeDomain(fromIndex(0))]);
    const back = await guard.reconnectPagedRange(null, REF, sentinel(7), expected, 6, deps);
    expect(back.pages).toBeGreaterThanOrEqual(1);
    expect(back.noDuplicates).toBe(true);
    expect(back.sentinelVisible).toBe(true);
    expect(back.rangeCheck.ordered).toBe(true);
    expect(back.complete).toBe(true);
  });

  test("RED: a missing assistant item stays incomplete", async () => {
    const omit = expected.findIndex((entry) => entry.kind === "assistant");
    const frames = [
      rangeDomain(fromIndex(12)),
      rangeDomain(fromIndex(8)),
      rangeDomain(allIndices.filter((index) => index !== omit)),
    ];
    const back = await guard.reconnectPagedRange(null, REF, sentinel(7), expected, 6, scripted(frames));
    expect(back.rangeCheck.ordered).toBe(false);
    expect(back.complete).toBe(false);
  });

  test("control: a reordered intermediate item stays incomplete", async () => {
    const swapped = allIndices.slice();
    [swapped[8], swapped[9]] = [swapped[9], swapped[8]];
    const frames = [rangeDomain(fromIndex(12)), rangeDomain(fromIndex(8)), rangeDomain(swapped)];
    const back = await guard.reconnectPagedRange(null, REF, sentinel(7), expected, 6, scripted(frames));
    expect(back.rangeCheck.ordered).toBe(false);
    expect(back.complete).toBe(false);
  });
});

// --- finding 4: native checkpoint baseline + correlated commit ---

describe("finding 4: checkpoint commit", () => {
  const oldTurn = {
    id: "oldB",
    items: [{ id: "ob1", type: "agentMessage", text: `before\n${AFTER}`, output: "", status: "completed" }],
  };
  const expected = { turnId: "newB", workload: FULL_WORKLOAD, releaseSentinel: AFTER };
  const newTurn = (workload) => ({
    id: "newB",
    items: [
      { id: "nb1", type: "agentMessage", text: `before\n${workload}\n${AFTER}`, output: "", status: "completed" },
    ],
  });

  test("RED: old AFTER plus a full pre-release payload with no advance must reject", () => {
    const pre = { appliedSequence: 5, turns: [oldTurn] };
    const post = { appliedSequence: 5, turns: [oldTurn, newTurn(FULL_WORKLOAD)] };
    expect(guard.batchingCheckpointSatisfied(pre, post, expected)).toBe(false);
  });
  test("RED: the payload committed in the OLD turn must reject", () => {
    const pre = { appliedSequence: 5, turns: [oldTurn] };
    const post = {
      appliedSequence: 6,
      turns: [
        {
          id: "oldB",
          items: [
            {
              id: "ob1",
              type: "agentMessage",
              text: `before\n${FULL_WORKLOAD}\n${AFTER}`,
              output: "",
              status: "completed",
            },
          ],
        },
      ],
    };
    expect(guard.batchingCheckpointSatisfied(pre, post, expected)).toBe(false);
  });
  test("control: a partial new payload must reject", () => {
    const pre = { appliedSequence: 5, turns: [oldTurn] };
    const post = { appliedSequence: 6, turns: [oldTurn, newTurn("W".repeat(1024))] };
    expect(guard.batchingCheckpointSatisfied(pre, post, expected)).toBe(false);
  });
  test("control: the correlated new turn advanced with full content is accepted", () => {
    const pre = { appliedSequence: 5, turns: [oldTurn] };
    const post = { appliedSequence: 6, turns: [oldTurn, newTurn(FULL_WORKLOAD)] };
    expect(guard.batchingCheckpointSatisfied(pre, post, expected)).toBe(true);
  });
});

// --- finding 6: structured fields preserved ---

describe("finding 6: structured-field preservation", () => {
  test("RED: changed command arguments with identical text/ids/cursor is rejected", () => {
    const retained = { domain: { cursor: "c1", items: [structuredItem()] }, cursor: "c1", sentinel: "same" };
    expect(
      guard.retainedHistoryValidator(retained, { cursor: "c1", items: [structuredItem({ arguments: '{"a":2}' })] }).ok,
    ).toBe(false);
  });
  test("RED: changed item status with identical text/ids/cursor is rejected", () => {
    const retained = { domain: { cursor: "c1", items: [structuredItem()] }, cursor: "c1", sentinel: "same" };
    expect(
      guard.retainedHistoryValidator(retained, { cursor: "c1", items: [structuredItem({ status: "failed" })] }).ok,
    ).toBe(false);
  });
  test("RED: changed item type with identical text/ids/cursor is rejected", () => {
    const retained = { domain: { cursor: "c1", items: [structuredItem()] }, cursor: "c1", sentinel: "same" };
    expect(
      guard.retainedHistoryValidator(retained, { cursor: "c1", items: [structuredItem({ type: "agentMessage" })] }).ok,
    ).toBe(false);
  });
  test("control: identical structured fields pass", () => {
    const snap = { cursor: "c1", items: [structuredItem()] };
    expect(guard.retainedHistoryValidator({ domain: snap, cursor: "c1", sentinel: "same" }, snap).ok).toBe(true);
  });
});

// --- retained-history validator + expectations ---

describe("retained-history validator", () => {
  test("a reordered same-id window fails the ordered content check", () => {
    const retained = { domain: domainOf(["h1", "h2", "h3"], "c1"), cursor: "c1", sentinel: "h2" };
    expect(guard.retainedHistoryValidator(retained, domainOf(["h1", "h3", "h2"], "c1")).ok).toBe(false);
  });
  test("control: identical ordered full content, sentinel and cursor passes", () => {
    const retained = { domain: domainOf(["h1", "h2"], "c1"), cursor: "c1", sentinel: "h2" };
    expect(guard.retainedHistoryValidator(retained, domainOf(["h1", "h2"], "c1")).ok).toBe(true);
  });
  test("required: further paging must load the WHOLE expected range", () => {
    const seq = guard.expectedHistorySequence();
    const truncated = { items: seq.slice(0, 10).map((text, i) => ({ id: `i${i}`, text, output: "" })) };
    expect(guard.furtherHistoryValidator(truncated, seq).ordered).toBe(false);
    const full = { items: seq.map((text, i) => ({ id: `i${i}`, text, output: "" })) };
    expect(guard.furtherHistoryValidator(full, seq).ordered).toBe(true);
  });
});

// --- finding 4: observed disconnection through the dev evidence getter ---

describe("finding 4: observed disconnection", () => {
  test("required: a disconnected app state is observed", async () => {
    connectionStore.setState({ state: "reconnecting" });
    expect(await guard.waitForDisconnected(fakeDriver(), 200)).toBe(true);
  });
  test("required: a connected app while navigator.onLine=false is not disconnected", async () => {
    connectionStore.setState({ state: "ready" });
    expect(await guard.waitForDisconnected(fakeDriver({ online: false }), 200)).toBe(false);
  });
  test("control: observedConnection reads the app state, not navigator.onLine", async () => {
    connectionStore.setState({ state: "reconnecting" });
    const seen = await guard.observedConnection(fakeDriver({ online: true }));
    expect(seen.appState).toBe("reconnecting");
    expect(seen.online).toBe(true);
  });
});
