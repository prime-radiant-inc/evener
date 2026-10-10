// Production-stack browser journey for document annotations. This driver runs
// the COMPLETE journey against the real hub, real SPA, real IndexedDB and real
// AppWire; the only scripted piece is the LLM provider on the Go side. It opens
// the production document viewer from a real session file card, selects the
// literal FIRST passage with real CDP mouse input, sends the saved annotation,
// reaches the literal note through a genuine highlight click, and measures the
// saved bands against an independent clipped Range.
//
// The driver reports its own assertion inventory, forbidden-mutation count and
// console-error count in result.json so the Go test can validate the five
// required assertion names (finished-selection, exact-draft, exact-highlight,
// literal-note, unchanged-reopen) independently. It also observes the real
// durable storage and authoritative server/mutation state around the annotation
// action, so a passing run proves that action sent no agent input. A setup
// failure (a compile error, a missing element before the drag, a timeout before
// selection) is not an assertion failure.
//
// See README.md for prerequisites, the run command, artifacts and scope limits.

import assert from "node:assert/strict";
import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { evaluate, navigateTo } from "../browserGuardCdp.mjs";
import { Driver } from "../skillguard/run.mjs";
import { markerVisible, noteStackContained } from "./assertions.mjs";
import {
  FIRST as SHORT_FIRST,
  JOURNEY_APPENDED as SHORT_JOURNEY_APPENDED,
  LONG_FIRST,
  LONG_FIRST_POSITIONS,
  LONG_JOURNEY_APPENDED,
  LONG_SOURCE,
  LONG_SOURCE_SHA256,
  NOTE,
  NOTE_STACK_END_MARKERS,
  NOTE_STACK_LINE_RANGES,
  NOTE_STACK_NOTES,
  NOTE_STACK_POSITIONS,
  NOTE_STACK_RANGES,
  NOTE_STACK_REFERENCES,
  NOTE_STACK_TARGET_LINE,
  SOURCE as SHORT_SOURCE,
  SOURCE_SHA256 as SHORT_SOURCE_SHA256,
} from "./fixtures.mjs";

const fixture = JSON.parse(readFileSync(0, "utf8"));
const driver = new Driver(fixture);
const q = JSON.stringify;
const artifact = (name) => path.join(fixture.artifactDir, name);

// Scenario selection is test-only fixture input. The default short journey is
// unchanged; "long" drives the same real stack against the overflowing fixture
// so control and note placement at the reading position can be observed;
// "notestack" saves overlapping notes on the same overflowing fixture to check
// the bounded/wrapped/reachable revealed-note stack.
const SCENARIO =
  fixture.scenario === "long" ? "long" : fixture.scenario === "notestack" ? "notestack" : "short";
const LONG = SCENARIO === "long";
const STACK = SCENARIO === "notestack";
// Both the long and note-stack scenarios use the overflowing fixture.
const OVERFLOW = LONG || STACK;

// Every literal expectation below is scenario-selected and still independent of
// production mapping/formatter code. The short values are FIRST's own
// 1:19-1:22; the overflowing values are LONG_FIRST's 1:31-1:34.
const SOURCE = SCENARIO === "short" ? SHORT_SOURCE : LONG_SOURCE;
// FIRST holds LINE-RELATIVE offsets for the DOM Range expressions; the stored
// source ranges for the note-stack scenario are NOTE_STACK_RANGES (absolute).
const FIRST = STACK ? NOTE_STACK_LINE_RANGES[0] : SCENARIO === "short" ? SHORT_FIRST : LONG_FIRST;
const SOURCE_SHA256 = SCENARIO === "short" ? SHORT_SOURCE_SHA256 : LONG_SOURCE_SHA256;
const JOURNEY_APPENDED = SCENARIO === "short" ? SHORT_JOURNEY_APPENDED : LONG_JOURNEY_APPENDED;

// The assertion names the Go test validates independently, by scenario. The
// driver only records whether it reached each one.
const assertions = STACK
  ? {
      "finished-selection": false,
      "exact-stack-notes": false,
      "stack-bounded": false,
      "unbroken-wrapped": false,
      "stack-reachable": false,
    }
  : LONG
    ? {
        "finished-selection": false,
        "controls-visible": false,
        "exact-draft": false,
        "note-visible": false,
        "exact-highlight": false,
      }
    : {
        "finished-selection": false,
        "exact-draft": false,
        "exact-highlight": false,
        "literal-note": false,
        "unchanged-reopen": false,
      };

const FORBIDDEN = new Set(["turn/start", "turn/steer", "turn/queue", "thread/start", "thread/resume"]);

// Literal expectations independent of production mapping/formatter code. The
// source positions are FIRST's own: 1:19-1:22 (line 1, columns 19..22).
// The note-stack scenario selects a paragraph lower in the overflowing fixture,
// so the revealed stack (pinned to the clip top) stays clear of the target.
const FIRST_TEXT = STACK ? NOTE_STACK_TARGET_LINE : SOURCE.split("\n")[0];
const FIRST_SELECTED = STACK ? FIRST_TEXT.slice(FIRST.start, FIRST.end) : SOURCE.slice(FIRST.start, FIRST.end);
const FIRST_POSITIONS = STACK
  ? NOTE_STACK_POSITIONS[0]
  : SCENARIO === "short"
    ? { start: { line: 1, column: 19 }, endInclusive: { line: 1, column: 22 } }
    : LONG_FIRST_POSITIONS;
// The accepted contextForRange bounds for FIRST's opening paragraph.
const FIRST_CONTEXT_BEFORE = SCENARIO === "short" ? "First occurrence: " : "Long document opening target: ";
const FIRST_CONTEXT_AFTER = "";
const NOTE_EDITOR_SELECTOR = "textarea, input, [role='textbox'], [contenteditable=''], [contenteditable='true']";
const INITIAL_DRAFT = "Existing draft";
const DOCUMENT_PATH = "NOTES.md";

// Set on the current navigation from the real location.origin; used to match
// the annotation document key against fixture facts, not an arbitrary payload.
let expectedDocumentKey = null;
let expectedDocumentKeyEncoded = null;

const LOCAL_DATABASE_NAME = "evener-mutation-outbox";
const STORES = [
  "composerDrafts",
  "annotationPayloads",
  "annotationOperations",
  "annotationHeads",
  "annotationEvidence",
  "annotationSequences",
  "outbox",
  "optimistic",
  "recovery",
  "sequences",
];

const consoleEvents = [];
const errors = [];
const frames = [];
const docResponses = [];
let annotationBaseline = null;
let failure = null;
let storageBaseline = null;

const sent = (after = 0) => frames.slice(after).filter((f) => f.direction === "Network.webSocketFrameSent");
const forbiddenCount = (after) => sent(after).filter((f) => FORBIDDEN.has(f.method)).length;

// ---- independent DOM expressions (no production mapper) ----

const firstParagraphExpr = `(() => {
  const wanted = ${q(FIRST_TEXT)};
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  let node;
  while ((node = walker.nextNode())) {
    if (node.data === wanted) return { found: true, length: node.data.length };
  }
  return null;
})()`;

// FIRST's independent pointer geometry from a DOM Range over the literal text
// node. Returned as the fragment rectangles and the drag start/end points.
const firstGeometryExpr = `(() => {
  const wanted = ${q(FIRST_TEXT)};
  const start = ${FIRST.start};
  const end = ${FIRST.end};
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  let node;
  while ((node = walker.nextNode())) {
    if (node.data === wanted) break;
  }
  if (!node) return null;
  const range = document.createRange();
  range.setStart(node, start);
  range.setEnd(node, end);
  const rects = [...range.getClientRects()].filter((r) => r.width > 0 || r.height > 0);
  if (rects.length === 0) return null;
  const first = rects[0];
  const last = rects[rects.length - 1];
  return {
    start: { x: first.left, y: first.top + first.height / 2 },
    end: { x: last.right, y: last.top + last.height / 2 },
    rects: rects.map((r) => ({ left: r.left, top: r.top, right: r.right, bottom: r.bottom })),
  };
})()`;

// Reacquire the ACTUAL document scope AND its CHILD scroll owner structurally,
// from the literal document text node (PaneScaffold's body is
// `overflow-y: auto`), never from the optional `data-pane-scaffold` marker,
// which current DocPane does not supply. Both elements are stashed on `window`
// (observation state, not product DOM). The scope is the scroll owner's pane
// (its parent). Run again after any navigation; nothing detached is retained.
const observeScopeExpr = `(() => {
  const wanted = ${q(FIRST_TEXT)};
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  let node;
  while ((node = walker.nextNode())) {
    if (node.data === wanted && node.parentElement) break;
  }
  node = node && node.parentElement ? node : null;
  if (!node) return null;
  let scroll = null;
  for (let el = node.parentElement; el; el = el.parentElement) {
    const style = getComputedStyle(el);
    if ((style.overflowY === "auto" || style.overflowY === "scroll") && el.clientHeight > 0) {
      scroll = el;
      break;
    }
  }
  if (!scroll) return null;
  window.__annotationDocumentScroll = scroll;
  window.__annotationDocumentScope = scroll.parentElement ?? scroll;
  const r = scroll.getBoundingClientRect();
  return {
    connected: scroll.isConnected && scroll.ownerDocument === document && window.__annotationDocumentScope.isConnected,
    scrollTag: scroll.tagName,
    scopeTag: window.__annotationDocumentScope.tagName,
    clip: { left: r.left, top: r.top, right: r.right, bottom: r.bottom },
  };
})()`;

// The SAME independent Range/actual-clip measurement the later highlight
// comparison uses, derived from the reacquired child scroll owner (not a
// different ancestor). Returns null when the scope is absent or detached, so a
// caller can wait boundedly.
const clippedFirstExpr = `(() => {
  const scroll = window.__annotationDocumentScroll;
  const scope = window.__annotationDocumentScope;
  if (!scroll || !scroll.isConnected || scroll.ownerDocument !== document) return null;
  if (!scope || !scope.isConnected || scope.ownerDocument !== document) return null;
  const wanted = ${q(FIRST_TEXT)};
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  let node;
  while ((node = walker.nextNode())) {
    if (node.data === wanted) break;
  }
  if (!node || !scroll.contains(node)) return null;
  const range = document.createRange();
  range.setStart(node, ${FIRST.start});
  range.setEnd(node, ${FIRST.end});
  const rects = [...range.getClientRects()].filter((r) => r.width > 0 || r.height > 0);
  if (rects.length === 0) return null;
  const cr = scroll.getBoundingClientRect();
  const clip = { left: cr.left, top: cr.top, right: cr.right, bottom: cr.bottom };
  const clipped = rects.map((rect) => ({
    left: Math.max(rect.left, clip.left), top: Math.max(rect.top, clip.top),
    right: Math.min(rect.right, clip.right), bottom: Math.min(rect.bottom, clip.bottom),
  })).filter((rect) => rect.right > rect.left && rect.bottom > rect.top);
  return { clip, clipped: clipped.map((rect, index) => ({ index, ...rect })), rectCount: rects.length };
})()`;

// The selection endpoints, validated as the specific literal text node and
// offsets 18..22 — not merely the string "same".
const selectionEndpointsExpr = `(() => {
  const wanted = ${q(FIRST_TEXT)};
  const s = window.getSelection();
  if (!s || s.rangeCount === 0) return null;
  const range = s.getRangeAt(0);
  const describe = (node, offset) => ({
    literalNode: node?.nodeType === Node.TEXT_NODE && node.data === wanted,
    offset,
    nodeName: node?.nodeName ?? null,
  });
  const literal = (node) => node?.nodeType === Node.TEXT_NODE && node.data === wanted;
  return {
    text: s.toString(),
    anchor: describe(s.anchorNode, s.anchorOffset),
    focus: describe(s.focusNode, s.focusOffset),
    range: {
      startLiteral: literal(range.startContainer),
      startOffset: range.startOffset,
      endLiteral: literal(range.endContainer),
      endOffset: range.endOffset,
    },
  };
})()`;

// The passive trusted-input observations, installed before any input.
const inputObservationExpr = `window.__annotationInputs ?? null`;

// The note editor, scoped to the document pane and read with value semantics
// (textarea/input `.value`, contenteditable `textContent`) so a native multiline
// textarea is recognized. It never resolves to the session composer.
const noteEditorExpr = `(() => {
  const scope = window.__annotationDocumentScope;
  if (!scope || !scope.isConnected) return null;
  const editor = [...scope.querySelectorAll(${q(NOTE_EDITOR_SELECTOR)})].find((node) => !node.disabled && node.offsetParent !== null);
  if (!editor) return null;
  const value = editor.tagName === "TEXTAREA" || editor.tagName === "INPUT" ? editor.value : editor.textContent;
  return { tag: editor.tagName, value, multiline: editor.tagName === "TEXTAREA" || editor.isContentEditable === true };
})()`;

// The document's actual saved highlight bands, carrying their operation id, and
// the note surface a genuine highlight interaction opens. This is the forward
// accessible contract the reader UI grant must expose (see README).
const highlightBandsExpr = `(() => {
  const scope = window.__annotationDocumentScope;
  if (!scope || !scope.isConnected || scope.ownerDocument !== document) return null;
  return [...scope.querySelectorAll("[data-document-highlight]")].map((node) => {
    const r = node.getBoundingClientRect();
    return {
      operationId: node.getAttribute("data-operation-id"),
      left: r.left, top: r.top, right: r.right, bottom: r.bottom,
      note: node.getAttribute("data-note") ?? node.textContent ?? "",
    };
  });
})()`;

function installPassiveInputObservation() {
  return evaluate(driver.send, `(() => {
    if (window.__annotationInputs) return true;
    const record = window.__annotationInputs = { pointerdown: [], pointerup: [], mousemove: [], selectionchange: [] };
    document.addEventListener("pointerdown", (event) => {
      record.pointerdown.push({ trusted: event.isTrusted, x: event.clientX, y: event.clientY });
    }, { capture: true, passive: true });
    document.addEventListener("pointerup", (event) => {
      record.pointerup.push({ trusted: event.isTrusted, x: event.clientX, y: event.clientY });
    }, { capture: true, passive: true });
    document.addEventListener("mousemove", (event) => {
      record.mousemove.push({ trusted: event.isTrusted, x: event.clientX, y: event.clientY, buttons: event.buttons });
    }, { capture: true, passive: true });
    // selectionchange carries the selected STRING only; the browser fires no
    // trust flag for it, so no trust is claimed for these entries.
    document.addEventListener("selectionchange", () => {
      const s = window.getSelection();
      record.selectionchange.push(s ? s.toString() : null);
    }, { capture: true, passive: true });
    return true;
  })()`);
}

// A genuine CDP mouse press/move/release over the independently computed
// geometry. No Selection.addRange and no synthetic events.
async function dragSelection(geometry) {
  const steps = 10;
  await driver.send("Input.dispatchMouseEvent", { type: "mouseMoved", x: geometry.start.x, y: geometry.start.y, button: "none" });
  await driver.send("Input.dispatchMouseEvent", { type: "mousePressed", x: geometry.start.x, y: geometry.start.y, button: "left", buttons: 1, clickCount: 1 });
  for (let i = 1; i <= steps; i += 1) {
    const t = i / steps;
    await driver.send("Input.dispatchMouseEvent", {
      type: "mouseMoved",
      x: geometry.start.x + (geometry.end.x - geometry.start.x) * t,
      y: geometry.start.y + (geometry.end.y - geometry.start.y) * t,
      button: "left",
      buttons: 1,
    });
  }
  await driver.send("Input.dispatchMouseEvent", { type: "mouseReleased", x: geometry.end.x, y: geometry.end.y, button: "left", buttons: 1, clickCount: 1 });
}

// A read-only snapshot of the real annotation/draft/outbox storage. A missing
// database or a missing required store is an observation FAILURE, never coerced
// to an empty array; a database that did not exist is never created as an
// observer. Every required store is read in one readonly transaction, the
// promise resolves only on successful transaction completion, request and
// transaction failures are joined, and the connection is always closed. No
// writes and no seeding.
async function readStorage() {
  return evaluate(driver.send, `new Promise((resolve, reject) => {
    const name = ${q(LOCAL_DATABASE_NAME)};
    const required = ${q(STORES)};
    const fail = (message) => reject(new Error(message));
    const open = () => {
      const request = indexedDB.open(name);
      request.onerror = () => fail("storage open failed: " + (request.error ? request.error.message : "unknown"));
      request.onblocked = () => fail("storage open blocked");
      request.onupgradeneeded = () => {
        // The database did not exist. Abort the upgrade so observation never
        // creates it.
        try { request.transaction.abort(); } catch {}
        fail("storage database " + name + " is absent; observation refused to create it");
      };
      request.onsuccess = () => {
        const db = request.result;
        const missing = required.filter((store) => !db.objectStoreNames.contains(store));
        if (missing.length > 0) {
          db.close();
          fail("storage is missing required stores: " + missing.join(", "));
          return;
        }
        let tx;
        try { tx = db.transaction(required, "readonly"); }
        catch (error) { db.close(); fail("storage transaction failed: " + error.message); return; }
        const reads = required.map((store) => new Promise((res, rej) => {
          const r = tx.objectStore(store).getAll();
          r.onsuccess = () => res([store, r.result]);
          r.onerror = () => rej(r.error ?? new Error("request failed for " + store));
        }));
        const completion = new Promise((res, rej) => {
          tx.oncomplete = () => res();
          tx.onerror = () => rej(tx.error ?? new Error("transaction error"));
          tx.onabort = () => rej(tx.error ?? new Error("transaction aborted"));
        });
        const requestAll = Promise.all(reads);
        // Observe both outcomes immediately so neither a request nor a
        // transaction failure is ever an unhandled rejection, then JOIN them: a
        // failing request aborts the transaction, so both must settle before the
        // connection is closed and the error is rethrown. No swallowed errors and
        // no test-only success.
        void requestAll.catch(() => {});
        void completion.catch(() => {});
        Promise.allSettled([requestAll, completion]).then(([requests, transaction]) => {
          db.close();
          if (requests.status === "rejected") { reject(requests.reason); return; }
          if (transaction.status === "rejected") { reject(transaction.reason); return; }
          resolve(Object.fromEntries(requests.value));
        });
      };
    };
    if (typeof indexedDB.databases === "function") {
      indexedDB.databases()
        .then((list) => {
          if (!list.some((entry) => entry && entry.name === name)) {
            fail("storage database " + name + " is absent");
            return;
          }
          open();
        })
        .catch(() => open());
    } else {
      open();
    }
  })`);
}

// Wait boundedly for a storage snapshot satisfying `predicate`. Reading the
// real storage repeatedly (never a sleep-then-assume) is how the asynchronous
// draft/payload/receipt transitions are observed before exact equality.
async function waitForStorage(predicate, label, timeoutMs = 15000) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const snapshot = JSON.parse(JSON.stringify(await readStorage()));
    if (predicate(snapshot)) return snapshot;
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${label}; observed ${JSON.stringify(snapshot)}`);
    await new Promise((resolve) => setTimeout(resolve, 120));
  }
}

// Wait boundedly for the real committed initial composer draft row before the
// pre-annotation baseline is taken.
async function waitForInitialDraft() {
  return waitForStorage((snapshot) => {
    const drafts = sessionRows(snapshot, "composerDrafts");
    return drafts.length === 1 && drafts[0].text === INITIAL_DRAFT && drafts[0].token && drafts[0].token.sessionRevision >= 1;
  }, "the committed initial composer draft row");
}

function sessionRows(snapshot, store) {
  return (snapshot[store] ?? []).filter((row) => row?.key?.sessionRef === fixture.rootRef && row?.key?.namespace?.kind === "browser");
}

function annotationPayloads(snapshot) {
  return (snapshot.annotationPayloads ?? []).filter((row) => row?.operation?.lease?.key?.sessionRef === fixture.rootRef);
}

function annotationOperations(snapshot, operationId) {
  return (snapshot.annotationOperations ?? []).filter((row) => row?.operationId === operationId);
}

// ---- browser scope / geometry helpers (Unit A) ----

// Reacquire the actual document scope and its child scroll owner on the CURRENT
// navigation, verifying both are connected to the current document.
async function observeDocumentScope() {
  const observed = await evaluate(driver.send, observeScopeExpr);
  assert.ok(observed, "the document scope and its child scroll owner were located from the rendered text");
  assert.equal(observed.connected, true, "the observed scope and scroll owner are connected to the current document");
  return observed;
}

// The independent Range/actual-clip measurement, run as a real geometry
// preflight and again before the highlight comparison. Requires nonempty finite
// positive fragments.
async function measureClippedFirst() {
  const measured = await evaluate(driver.send, clippedFirstExpr);
  assert.ok(measured, "the independent scope/clip/Range measurement ran on the loaded document");
  assert.ok(measured.clipped.length > 0, "FIRST has positive independent clipped fragments");
  const edges = ["left", "top", "right", "bottom"];
  for (const fragment of measured.clipped) {
    for (const edge of edges) {
      assert.ok(Number.isFinite(fragment[edge]), `clipped fragment ${fragment.index} ${edge} is finite geometry`);
    }
  }
  return measured;
}

// ---- rendered draft helpers (Unit A) ----

async function waitRenderedDraft(expected) {
  await driver.waitPage(
    `(() => { const state = ${driver.composerEditStateExpr(fixture.rootRef)}; return state && state.value === ${q(expected)} ? true : null; })()`,
    { label: "the original composer renders the exact JOURNEY_APPENDED draft" },
  );
}

async function assertRenderedDraft() {
  const state = await driver.composerState(fixture.rootRef);
  assert.equal(state.text, JOURNEY_APPENDED, "the mounted original composer renders the exact JOURNEY_APPENDED draft");
}

// ---- complete journey continuation (written now, unreached while RED) ----

// The forward accessible contract (see README): the document pane must expose a
// note editor with value semantics, an annotation Send control, highlight bands
// carrying their operation id, and a note surface a highlight interaction opens.

async function typeNoteIntoDocumentEditor(note = NOTE) {
  const before = await evaluate(driver.send, noteEditorExpr);
  if (!before) throw new Error("the document-scoped note editor is not present");
  await evaluate(driver.send, `(() => { const scope = window.__annotationDocumentScope;
    const editor = scope.querySelector(${q(NOTE_EDITOR_SELECTOR)}); editor.focus(); return true; })()`);
  await driver.send("Input.insertText", { text: note });
  await driver.waitPage(`(() => { const state = ${noteEditorExpr}; return state && state.value === ${q(note)} ? true : null; })()`, {
    label: "the document note editor holds the typed note",
  });
}

async function clickDocumentSend() {
  const point = await evaluate(driver.send, `(() => {
    const scope = window.__annotationDocumentScope;
    if (!scope || !scope.isConnected) return null;
    const button = [...scope.querySelectorAll("button")].find((b) => b.textContent.trim() === "Send" || (b.getAttribute("aria-label") ?? "") === "Send");
    if (!button) return null;
    button.scrollIntoView({ block: "center" });
    const r = button.getBoundingClientRect();
    return { x: r.x + r.width / 2, y: r.y + r.height / 2 };
  })()`);
  if (!point) throw new Error("the document annotation Send control is not present");
  await driver.clickAt(point.x, point.y);
}

// Durable exact draft + one payload/operation, with independently literal
// expectations. Values are never derived from the rows under test. Also returns
// the retained initial snapshot the reopen tail compares against.
async function assertStoredDraftAndOperation() {
  // Wait boundedly for the REAL committed exact draft, payload and applied
  // receipt rather than racing the asynchronous save.
  const snapshot = await waitForStorage((candidate) => {
    const candidateDrafts = sessionRows(candidate, "composerDrafts");
    const candidatePayloads = annotationPayloads(candidate);
    if (candidateDrafts.length !== 1 || candidateDrafts[0].text !== JOURNEY_APPENDED) return false;
    if (candidatePayloads.length !== 1 || candidatePayloads[0].documentKey !== expectedDocumentKeyEncoded) return false;
    const candidateOperations = annotationOperations(candidate, candidatePayloads[0].operation.operationId);
    return candidateOperations.length === 1 && candidateOperations[0].state === "applied";
  }, "the committed exact annotation draft, payload and applied receipt");
  const drafts = sessionRows(snapshot, "composerDrafts");
  assert.equal(drafts.length, 1, "exactly one stored composer draft row for the session");
  assert.equal(drafts[0].text, JOURNEY_APPENDED, "the stored draft text is the exact JOURNEY_APPENDED");

  const payloads = annotationPayloads(snapshot);
  assert.equal(payloads.length, 1, "exactly one stored annotation payload for the document");
  const operation = payloads[0].operation;
  assert.deepEqual(operation.lease.key, expectedDocumentKey, "the payload document key matches the fixture namespace/origin/session/path");
  assert.equal(payloads[0].documentKey, expectedDocumentKeyEncoded, "the payload row's encoded document key matches the fixture document");
  assert.equal(operation.note, NOTE, "the stored operation note is the literal note");
  assert.deepEqual(operation.capture.sourceRange, FIRST, "the stored capture source range is literal FIRST");
  assert.deepEqual(operation.capture.start, FIRST_POSITIONS.start, "the stored capture start position is literal 1:19");
  assert.deepEqual(operation.capture.endInclusive, FIRST_POSITIONS.endInclusive, "the stored capture end position is literal 1:22");
  assert.equal(operation.capture.selectedRenderedText, FIRST_SELECTED, "the stored capture selected text is literal FIRST text");
  assert.equal(operation.capture.contextBefore, FIRST_CONTEXT_BEFORE, "the stored capture context-before is literal");
  assert.equal(operation.capture.contextAfter, FIRST_CONTEXT_AFTER, "the stored capture context-after is literal");
  assert.equal(operation.capture.evidence.wholeRevision, SOURCE_SHA256, "the stored capture revision is the literal SHA256");
  assert.equal(operation.destination.kind, "browser-origin", "the stored destination is the browser origin");

  const operations = annotationOperations(snapshot, operation.operationId);
  assert.equal(operations.length, 1, "exactly one applied annotation operation for the payload");
  assert.equal(operations[0].state, "applied", "the stored annotation operation is applied");
  return {
    operation,
    retained: {
      operationId: operation.operationId,
      capture: operation.capture,
      note: operation.note,
      destination: operation.destination,
      draftText: drafts[0].text,
      token: drafts[0].token,
      operationState: operations[0].state,
      draftRevision: operations[0].draftRevision,
    },
  };
}

// Reach the saved note through a genuine highlight interaction, not the editor.
// The note surface is waited on boundedly before exact equality.
async function assertSavedNoteViaHighlight(operationId) {
  const bands = await evaluate(driver.send, highlightBandsExpr);
  assert.ok(Array.isArray(bands) && bands.length > 0, "the document exposes its saved highlight bands");
  const band = bands.find((candidate) => candidate.operationId === operationId);
  assert.ok(band, "a saved band carries the applied operation id");
  await driver.clickAt((band.left + band.right) / 2, (band.top + band.bottom) / 2);
  await driver.waitPage(
    `(() => {
      const scope = window.__annotationDocumentScope;
      if (!scope || !scope.isConnected) return null;
      const node = scope.querySelector("[data-annotation-note]");
      return node && node.textContent === ${q(NOTE)} ? true : null;
    })()`,
    { label: "the highlight click opens the literal saved note", timeoutMs: 5000 },
  );
  const view = await evaluate(driver.send, noteVisibilityExpr);
  assert.ok(view, "the revealed note surface was measured on the current navigation");
  assert.equal(view.text, NOTE, "the note surface renders the literal saved note");
  // The note is clipped by the document's ACTUAL scroll body, so it must be
  // measured against that smaller root, never the pane that also holds the
  // legal footer.
  assert.ok(
    noteStackContained({ paneRect: view.paneClip, scrollRect: view.scrollClip }, view.note),
    `the revealed note is fully inside the actual scroll body, note ${JSON.stringify(view.note)} scrollClip ${JSON.stringify(view.scrollClip)}`,
  );
}

// Compare the SAME independent clipped FIRST measurement against this
// document's actual saved bands for the operation, with the unchanged 1 CSS
// pixel bound.
//
// Before that comparison this writes a READ-ONLY geometry receipt to
// highlight-geometry.json: the failing band's viewport rectangle and inline
// position, the overlay and its positioned containing block, the scroll owner's
// clip, offsets and computed position/padding/border, and the positioned
// ancestor chain. It moves no element, changes no style, seeds no state and
// suppresses no error; the comparison below and every continuation are
// untouched. The independently measured expected rect is attached as
// `measured`.
const geometryReceiptExpr = (operationId) => `(() => {
  const scope = window.__annotationDocumentScope;
  const scroll = window.__annotationDocumentScroll;
  if (!scroll || !scroll.isConnected || scroll.ownerDocument !== document) return { scrollPresent: false };
  const rect = (el) => { const r = el.getBoundingClientRect(); return { left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: r.width, height: r.height }; };
  const describe = (el) => {
    if (!el) return null;
    const cs = getComputedStyle(el);
    return {
      tag: el.tagName,
      className: String(el.className),
      rect: rect(el),
      position: cs.position,
      overflowX: cs.overflowX,
      overflowY: cs.overflowY,
      paddingLeft: cs.paddingLeft,
      paddingTop: cs.paddingTop,
      borderLeftWidth: cs.borderLeftWidth,
      borderTopWidth: cs.borderTopWidth,
      offsetLeft: el.offsetLeft,
      offsetTop: el.offsetTop,
    };
  };
  const bands = [...scroll.querySelectorAll("[data-document-highlight]")];
  const band = bands.find((b) => b.getAttribute("data-operation-id") === ${q(operationId)}) ?? bands[0] ?? null;
  const base = {
    scrollPresent: true,
    bandCount: bands.length,
    scopeTag: scope ? scope.tagName : null,
    scroll: describe(scroll),
    scrollOffsets: { scrollLeft: scroll.scrollLeft, scrollTop: scroll.scrollTop },
  };
  if (!band) return { ...base, bandPresent: false };
  const chain = [];
  for (let el = band.parentElement; el && chain.length < 6; el = el.parentElement) {
    chain.push({ tag: el.tagName, className: String(el.className), position: getComputedStyle(el).position });
  }
  return {
    ...base,
    bandPresent: true,
    operationId: band.getAttribute("data-operation-id"),
    bandInlineStyle: { left: band.style.left, top: band.style.top, width: band.style.width, height: band.style.height },
    bandRect: rect(band),
    overlay: describe(band.parentElement),
    offsetParent: describe(band.offsetParent),
    containingBlockChain: chain,
  };
})()`;

async function assertHighlightGeometry(operationId, measured) {
  const bands = await evaluate(driver.send, highlightBandsExpr);
  assert.ok(Array.isArray(bands), "the document exposes its saved highlight bands");
  // Additive, read-only geometry receipt BEFORE the unchanged comparison. It
  // never changes the oracle or tolerance; a receipt failure is recorded, not
  // raised, so it cannot mask the real geometry failure below.
  let receipt;
  try {
    receipt = await evaluate(driver.send, geometryReceiptExpr(operationId));
  } catch (error) {
    receipt = { error: String(error && error.message ? error.message : error) };
  }
  writeFileSync(artifact("highlight-geometry.json"), JSON.stringify({ operationId, measured, bands, receipt }, null, 2));
  const mine = bands.filter((band) => band.operationId === operationId);
  assert.equal(mine.length, measured.clipped.length, "one saved band per clipped FIRST fragment");
  const edges = ["left", "top", "right", "bottom"];
  for (const [index, band] of mine.entries()) {
    for (const edge of edges) {
      assert.ok(Number.isFinite(band[edge]), `saved band ${index} ${edge} is finite geometry`);
      assert.ok(Math.abs(band[edge] - measured.clipped[index][edge]) <= 1, `saved band ${index} ${edge} within 1 CSS pixel`);
    }
  }
}

async function waitForFreshDocumentRead(previousCount) {
  const deadline = Date.now() + 20000;
  while (Date.now() < deadline) {
    if (docResponses.length > previousCount && docResponses.at(-1).status === 200) return docResponses.at(-1);
    await new Promise((resolve) => setTimeout(resolve, 80));
  }
  throw new Error("a fresh successful /doc/file response did not arrive after reopen");
}

// Reopen unchanged source: reload from the authenticated URL, REACQUIRE the
// actual document scope on the new navigation, wait for a NEW successful
// document response, then compare the retained original evidence against fresh
// state and re-run the rendered draft, literal note and clipped geometry
// assertions without a second Send.
async function assertUnchangedReopen(retained) {
  const previousReads = docResponses.length;
  await navigateTo(driver.page, fixture.url);
  await driver.openSession(fixture.rootRef);
  await driver.click('button[aria-label="Open beside: NOTES.md"]');
  await driver.waitPage(firstParagraphExpr, { label: "the unchanged source reopens after reload" });
  const fresh = await waitForFreshDocumentRead(previousReads);
  assert.equal(fresh.revision, SOURCE_SHA256, "the reopened read revision equals the literal SHA256");

  // Rebind the scope/scroll on the CURRENT navigation before any DOM lookup.
  await observeDocumentScope();

  const measured = await measureClippedFirst();

  await waitRenderedDraft(JOURNEY_APPENDED);
  await assertRenderedDraft();

  const snapshot = await waitForStorage((candidate) => {
    const candidateDrafts = sessionRows(candidate, "composerDrafts");
    const candidatePayloads = annotationPayloads(candidate);
    if (candidateDrafts.length !== 1 || candidateDrafts[0].text !== JOURNEY_APPENDED) return false;
    if (candidatePayloads.length !== 1 || candidatePayloads[0].operation.operationId !== retained.operationId) return false;
    const candidateOperations = annotationOperations(candidate, retained.operationId);
    return candidateOperations.length === 1 && candidateOperations[0].state === "applied";
  }, "the reopened committed exact annotation state");
  const drafts = sessionRows(snapshot, "composerDrafts");
  assert.equal(drafts.length, 1, "reopen retains exactly one stored draft row");
  assert.equal(drafts[0].text, retained.draftText, "reopen retains the exact original stored draft text");
  assert.equal(drafts[0].text, JOURNEY_APPENDED, "reopen retains the exact JOURNEY_APPENDED draft");
  assert.deepEqual(drafts[0].token, retained.token, "reopen retains the unchanged draft token/revision");

  const payloads = annotationPayloads(snapshot);
  assert.equal(payloads.length, 1, "reopen retains exactly one annotation payload — no second append");
  assert.equal(payloads[0].operation.operationId, retained.operationId, "reopen retains the original operation id");
  assert.deepEqual(payloads[0].operation.capture, retained.capture, "reopen retains the original capture value");
  assert.equal(payloads[0].operation.note, retained.note, "reopen retains the literal note value");
  assert.deepEqual(payloads[0].operation.destination, retained.destination, "reopen retains the original destination value");

  const operations = annotationOperations(snapshot, retained.operationId);
  assert.equal(operations.length, 1, "reopen retains exactly one applied operation identity");
  assert.equal(operations[0].state, "applied", "reopen retains the applied receipt state");
  assert.equal(operations[0].draftRevision, retained.draftRevision, "reopen retains the unchanged applied draft revision");

  await assertSavedNoteViaHighlight(retained.operationId);
  await assertHighlightGeometry(retained.operationId, measured);
}

// The pre-annotation outbox/recovery/sequence baseline. The FULL rows are
// retained (not counts) so the post-journey comparison is by value, and the
// snapshot is written as an artifact showing what was observed.
async function captureStorageBaseline() {
  const snapshot = JSON.parse(JSON.stringify(await readStorage()));
  storageBaseline = {
    outbox: snapshot.outbox ?? [],
    optimistic: snapshot.optimistic ?? [],
    recovery: snapshot.recovery ?? [],
    sequences: snapshot.sequences ?? [],
  };
  writeFileSync(artifact("storage-baseline.json"), JSON.stringify(storageBaseline, null, 2));
  return storageBaseline;
}

async function assertNoQueuedMutation() {
  const snapshot = JSON.parse(JSON.stringify(await readStorage()));
  writeFileSync(artifact("storage-final.json"), JSON.stringify({
    outbox: snapshot.outbox ?? [],
    optimistic: snapshot.optimistic ?? [],
    recovery: snapshot.recovery ?? [],
    sequences: snapshot.sequences ?? [],
  }, null, 2));
  assert.deepEqual(snapshot.outbox ?? [], storageBaseline.outbox, "the outbox rows are unchanged across the annotation journey");
  assert.deepEqual(snapshot.optimistic ?? [], storageBaseline.optimistic, "the optimistic rows are unchanged across the annotation journey");
  assert.deepEqual(snapshot.recovery ?? [], storageBaseline.recovery, "the recovery rows are unchanged across the annotation journey");
  assert.deepEqual(snapshot.sequences ?? [], storageBaseline.sequences, "the mutation sequences are unchanged across the annotation journey");
}

async function capture(name) {
  writeFileSync(artifact(`${name}.html`), await evaluate(driver.send, "document.documentElement.outerHTML"));
  const screenshot = await driver.send("Page.captureScreenshot", { format: "png" });
  writeFileSync(artifact(`${name}.png`), Buffer.from(screenshot.result.data, "base64"));
}

async function assertsContinuation() {
  await typeNoteIntoDocumentEditor();
  await clickDocumentSend();
  // Bounded wait on the actual rendered state before any exact equality.
  await waitRenderedDraft(JOURNEY_APPENDED);
  const { operation, retained } = await assertStoredDraftAndOperation();
  await assertRenderedDraft();
  assertions["exact-draft"] = true;

  await assertSavedNoteViaHighlight(operation.operationId);
  assertions["literal-note"] = true;

  const measured = await measureClippedFirst();
  await assertHighlightGeometry(operation.operationId, measured);
  assertions["exact-highlight"] = true;

  await assertUnchangedReopen(retained);
  await assertNoQueuedMutation();
  assertions["unchanged-reopen"] = true;
}

// ---- long-document visibility continuation (findings 3-5, real layout) ----

// The long document's REAL control rectangles against its actual scroll clip,
// plus the reading position and the active element. The comment box must place
// its editor, Send and Cancel where the user already is; a normal-flow box after
// the whole document reports all three OUTSIDE this clip. It resolves controls
// through the document pane (their legal same-pane footer included), never the
// session composer.
const controlsVisibilityExpr = `(() => {
  const scope = window.__annotationDocumentScope;
  const scroll = window.__annotationDocumentScroll;
  if (!scroll || !scroll.isConnected || scroll.ownerDocument !== document) return null;
  if (!scope || !scope.isConnected || scope.ownerDocument !== document) return null;
  const rect = (el) => { const r = el.getBoundingClientRect(); return { left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: r.width, height: r.height }; };
  // The VISIBLE PANE is the scaffold root (the scroll owner's parent): a legal
  // same-pane footer sits below the scroll body but still inside this clip,
  // while the actual child scroll clip still governs band geometry.
  const clip = rect(scope);
  const scrollClip = rect(scroll);
  const contained = (r) => r.left >= clip.left && r.top >= clip.top && r.right <= clip.right && r.bottom <= clip.bottom && r.right > r.left && r.bottom > r.top;
  const editor = [...scope.querySelectorAll(${q(NOTE_EDITOR_SELECTOR)})].find((node) => !node.disabled && node.offsetParent !== null) ?? null;
  const buttons = [...scope.querySelectorAll("button")];
  const findButton = (label) => buttons.find((b) => b.textContent.trim() === label || (b.getAttribute("aria-label") ?? "") === label) ?? null;
  const describe = (el) => el ? { rect: rect(el), contained: contained(rect(el)) } : null;
  return {
    clip,
    scrollClip,
    scrollTop: scroll.scrollTop,
    scrollHeight: scroll.scrollHeight,
    clientHeight: scroll.clientHeight,
    scrollMax: scroll.scrollHeight - scroll.clientHeight,
    editor: editor ? { rect: rect(editor), contained: contained(rect(editor)), focused: document.activeElement === editor } : null,
    send: describe(findButton("Send")),
    cancel: describe(findButton("Cancel")),
    activeElementTag: document.activeElement ? document.activeElement.tagName : null,
    activeElementInBox: buttons.some((b) => b === document.activeElement),
  };
})()`;

// The revealed note's REAL rectangle against the same scroll clip, and the
// reading position before and after the reveal. The note must appear where the
// user is reading, never pinned to the document's bottom.
const noteVisibilityExpr = `(() => {
  const scope = window.__annotationDocumentScope;
  const scroll = window.__annotationDocumentScroll;
  if (!scroll || !scroll.isConnected || scroll.ownerDocument !== document) return null;
  if (!scope || !scope.isConnected || scope.ownerDocument !== document) return null;
  const rect = (el) => { const r = el.getBoundingClientRect(); return { left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: r.width, height: r.height }; };
  const paneClip = rect(scope);
  const scrollClip = rect(scroll);
  const note = scope.querySelector("[data-annotation-note]");
  if (!note) return null;
  const r = rect(note);
  // The note lives inside the overflow-clipped scroll body, so its containment
  // is measured against that ACTUAL body, never the larger pane that also holds
  // the legal footer. No stale pane-based field is carried.
  const containedInBody =
    r.left >= scrollClip.left &&
    r.top >= scrollClip.top &&
    r.right <= scrollClip.right &&
    r.bottom <= scrollClip.bottom &&
    r.right > r.left &&
    r.bottom > r.top;
  return {
    paneClip,
    scrollClip,
    note: r,
    containedInBody,
    text: note.textContent,
    scrollTop: scroll.scrollTop,
    scrollMax: scroll.scrollHeight - scroll.clientHeight,
  };
})()`;

// The whole long-document control surface, observed WITHOUT any scrolling or
// focusing helper so a defective placement cannot be repaired before it is
// asserted. A missing control element is a setup failure, not a product RED.
async function assertLongControlsVisible(preSelectionScroll) {
  await driver.waitPage(
    `(() => { const v = ${controlsVisibilityExpr}; return v && v.editor && v.send && v.cancel ? true : null; })()`,
    { label: "the long-document comment controls are present", timeoutMs: 10000 },
  );
  const view = await evaluate(driver.send, controlsVisibilityExpr);
  assert.ok(view, "the long-document comment controls were located on the current navigation");
  assert.ok(view.scrollMax > 0, `the long fixture genuinely overflows its scroll owner (scrollMax ${view.scrollMax})`);
  assert.ok(view.editor.contained, `the note editor is fully inside the visible pane, editor ${JSON.stringify(view.editor.rect)} clip ${JSON.stringify(view.clip)}`);
  assert.ok(view.send.contained, `the Send control is fully inside the visible pane, Send ${JSON.stringify(view.send.rect)} clip ${JSON.stringify(view.clip)}`);
  assert.ok(view.cancel.contained, `the Cancel control is fully inside the visible pane, Cancel ${JSON.stringify(view.cancel.rect)} clip ${JSON.stringify(view.clip)}`);
  assert.equal(view.editor.focused, false, "opening the finished selection did not focus the note editor");
  assert.ok(
    view.activeElementTag !== "TEXTAREA" && view.activeElementTag !== "INPUT",
    `the finished selection did not move focus into the note editor (active element ${view.activeElementTag})`,
  );
  assert.ok(
    Math.abs(view.scrollTop - preSelectionScroll) <= 1,
    `the reading position moved ${Math.abs(view.scrollTop - preSelectionScroll)} CSS px on the finished selection (want <= 1)`,
  );
  writeFileSync(artifact("controls-visibility.json"), JSON.stringify({ preSelectionScroll, view }, null, 2));
  assertions["controls-visible"] = true;
}

// Reveal the saved note through a genuine highlight interaction and require the
// actual note surface to be inside the visible pane, WITHOUT the reveal jumping
// the reading position to the document's bottom.
async function assertNoteVisibleViaHighlight(operationId) {
  // Bring the saved band into the visible clip (a legitimate reveal
  // precondition), then capture the reading position immediately before it.
  const scrolled = await evaluate(driver.send, `(() => {
    const scroll = window.__annotationDocumentScroll;
    const band = [...scroll.querySelectorAll("[data-document-highlight]")].find((b) => b.getAttribute("data-operation-id") === ${q(operationId)});
    if (!band) return false;
    band.scrollIntoView({ block: "center" });
    return true;
  })()`);
  assert.ok(scrolled, "the saved highlight band is present for the reveal");
  const before =
    (await evaluate(driver.send, `(() => { const s = window.__annotationDocumentScroll; return s ? s.scrollTop : null; })()`)) ?? 0;
  const bands = await evaluate(driver.send, highlightBandsExpr);
  assert.ok(Array.isArray(bands) && bands.length > 0, "the long document exposes its saved highlight bands");
  const band = bands.find((candidate) => candidate.operationId === operationId);
  assert.ok(band, "a saved band carries the applied operation id");
  await driver.clickAt((band.left + band.right) / 2, (band.top + band.bottom) / 2);
  await driver.waitPage(
    `(() => { const v = ${noteVisibilityExpr}; return v && v.text === ${q(NOTE)} ? true : null; })()`,
    { label: "the highlight click opens the literal saved note at the reading position", timeoutMs: 5000 },
  );
  const view = await evaluate(driver.send, noteVisibilityExpr);
  assert.equal(view.text, NOTE, "the note surface renders the literal saved note");
  // The revealed note lives inside the overflow-clipped scroll body, so its
  // containment is measured against that actual body, not the larger pane.
  assert.ok(
    noteStackContained({ paneRect: view.paneClip, scrollRect: view.scrollClip }, view.note),
    `the revealed note is fully inside the actual scroll body, note ${JSON.stringify(view.note)} scrollClip ${JSON.stringify(view.scrollClip)}`,
  );
  assert.ok(view.scrollMax > 0, "the long document genuinely overflows the scroll owner");
  assert.ok(
    Math.abs(view.scrollTop - before) <= 1,
    `revealing the note did not jump the reading position (scrollTop ${view.scrollTop}, before ${before}, max ${view.scrollMax})`,
  );
  writeFileSync(artifact("note-visibility.json"), JSON.stringify({ before, view }, null, 2));
}

async function assertsLongContinuation() {
  await typeNoteIntoDocumentEditor();
  await clickDocumentSend();
  await waitRenderedDraft(JOURNEY_APPENDED);
  const { operation } = await assertStoredDraftAndOperation();
  await assertRenderedDraft();
  assertions["exact-draft"] = true;

  await assertNoteVisibleViaHighlight(operation.operationId);
  assertions["note-visible"] = true;

  const measured = await measureClippedFirst();
  await assertHighlightGeometry(operation.operationId, measured);
  assertions["exact-highlight"] = true;

  await assertNoQueuedMutation();
}

// ---- note-stack continuation (finding 3: bounded, wrapped, reachable) ----

// Independent geometry for a literal range inside the opening line, computed
// from a DOM Range over the literal text node (no production mapper).
function rangeGeometryExpr(lineText, range) {
  return `(() => {
    const wanted = ${q(lineText)};
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    let node;
    while ((node = walker.nextNode())) { if (node.data === wanted) break; }
    if (!node) return null;
    const r = document.createRange();
    r.setStart(node, ${range.start});
    r.setEnd(node, ${range.end});
    const rects = [...r.getClientRects()].filter((rect) => rect.width > 0 || rect.height > 0);
    if (rects.length === 0) return null;
    const first = rects[0];
    const last = rects[rects.length - 1];
    return {
      start: { x: first.left, y: first.top + first.height / 2 },
      end: { x: last.right, y: last.top + last.height / 2 },
      rects: rects.map((rect) => ({ left: rect.left, top: rect.top, right: rect.right, bottom: rect.bottom })),
    };
  })()`;
}

// Bring the lower note-stack target paragraph into the body's visible clip
// before the real selection (a legitimate selection precondition).
async function scrollStackTargetIntoView() {
  const ok = await evaluate(driver.send, `(() => {
    const wanted = ${q(NOTE_STACK_TARGET_LINE)};
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    let node;
    while ((node = walker.nextNode())) { if (node.data === wanted) break; }
    if (!node || !node.parentElement) return false;
    node.parentElement.scrollIntoView({ block: "center" });
    return true;
  })()`);
  assert.ok(ok, "the note-stack target paragraph was scrolled into view before selecting");
}

// A real CDP drag selecting one literal range. The ranges are ordered so each
// drag STARTS left of and ENDS right of every earlier band, keeping the pointer
// endpoints on authored text (a mouseup on a band button yields a runaway
// cross-paragraph selection). The drag still passes over earlier bands, which is
// the reader's own hover reveal. No Selection.addRange.
async function dragStackRange(range, label) {
  const geometry = await evaluate(driver.send, rangeGeometryExpr(FIRST_TEXT, range));
  assert.ok(geometry, `${label}: the independent range geometry is nonempty before dragging`);
  driver.milestone(`${label}-geometry`, { range, geometry });
  await dragSelection(geometry);
  const selected = await evaluate(driver.send, `(() => { const s = window.getSelection(); return s ? s.toString() : null; })()`);
  driver.milestone(`${label}-selected`, { selected });
  await driver.waitPage(
    `(() => { const s = window.getSelection(); return s && s.toString().length > 0 ? true : null; })()`,
    { label: `${label}: a real browser selection exists after the drag`, timeoutMs: 5000 },
  );
}

// Save one stack note through the real editor and Send, then wait for the real
// committed payload and the cleared box before the next selection.
async function saveStackNote(range, note, index) {
  await dragStackRange(range, `stack note ${index}`);
  await driver.waitPage(noteEditorExpr, { label: `stack note ${index}: the comment box opens`, timeoutMs: 10000 });
  await typeNoteIntoDocumentEditor(note);
  await clickDocumentSend();
  await waitForStorage(
    (candidate) => annotationPayloads(candidate).length === index,
    `stack note ${index}: the committed annotation payload`,
  );
  await driver.waitPage(
    `(() => { const v = ${noteEditorExpr}; return v === null ? true : null; })()`,
    { label: `stack note ${index}: the comment box clears after Send`, timeoutMs: 10000 },
  );
}

// The three literal notes/captures and their independently formatted references.
async function assertStackStored() {
  const snapshot = await waitForStorage((candidate) => {
    const payloads = annotationPayloads(candidate);
    const drafts = sessionRows(candidate, "composerDrafts");
    if (payloads.length !== 3 || drafts.length !== 1) return false;
    return NOTE_STACK_REFERENCES.every((reference) => drafts[0].text.includes(reference));
  }, "the three committed stack notes and their appended references");

  const payloads = annotationPayloads(snapshot);
  for (const [index, range] of NOTE_STACK_RANGES.entries()) {
    const payload = payloads.find((row) => JSON.stringify(row.operation.capture.sourceRange) === JSON.stringify(range));
    assert.ok(payload, `a committed stack payload for range ${JSON.stringify(range)}`);
    assert.equal(payload.operation.note, NOTE_STACK_NOTES[index], `stack note ${index + 1} exact literal text`);
    assert.equal(
      payload.operation.capture.selectedRenderedText,
      SOURCE.slice(range.start, range.end),
      `stack note ${index + 1} literal selected text`,
    );
    assert.equal(payload.operation.capture.evidence.wholeRevision, SOURCE_SHA256, `stack note ${index + 1} literal revision`);
    const operations = annotationOperations(snapshot, payload.operation.operationId);
    assert.equal(operations.length, 1, `stack note ${index + 1}: exactly one operation receipt`);
    assert.equal(operations[0].state, "applied", `stack note ${index + 1}: applied receipt`);
  }

  // The COMPLETE expected draft: the initial edited draft plus the three
  // independent fixture references, joined by the append separator.
  const expectedDraftText = [INITIAL_DRAFT, ...NOTE_STACK_REFERENCES].join("\n\n");
  const draft = sessionRows(snapshot, "composerDrafts")[0];
  assert.equal(draft.text, expectedDraftText, "the stored draft equals the initial draft plus the three literal references");
  await waitRenderedDraft(expectedDraftText);
  const rendered = await driver.composerState(fixture.rootRef);
  assert.equal(
    rendered.text,
    expectedDraftText,
    "the rendered original composer equals the initial draft plus the three literal references",
  );
}

// The revealed note stack's real rectangles, internal scroll dimensions and the
// document reading position. Read-only: it writes no product geometry, moves no
// element and never repairs visibility or focus.
const stackStateExpr = `(() => {
  const scope = window.__annotationDocumentScope;
  const scroll = window.__annotationDocumentScroll;
  if (!scroll || !scroll.isConnected || scroll.ownerDocument !== document) return null;
  if (!scope || !scope.isConnected || scope.ownerDocument !== document) return null;
  const rect = (el) => { const r = el.getBoundingClientRect(); return { left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: r.width, height: r.height }; };
  const first = scope.querySelector("[data-annotation-note]");
  if (!first) return null;
  const stack = first.parentElement;
  if (!stack) return null;
  const notes = [...stack.querySelectorAll("[data-annotation-note]")];
  return {
    paneClip: rect(scope),
    scrollClip: rect(scroll),
    stack: rect(stack),
    stackScroll: { scrollTop: stack.scrollTop, scrollHeight: stack.scrollHeight, clientHeight: stack.clientHeight, scrollWidth: stack.scrollWidth, clientWidth: stack.clientWidth },
    notes: notes.map((node) => ({ text: node.textContent, rect: rect(node), scrollWidth: node.scrollWidth, clientWidth: node.clientWidth })),
    documentScrollTop: scroll.scrollTop,
    documentScrollMax: scroll.scrollHeight - scroll.clientHeight,
  };
})()`;

// Read-only marker measurement for the reachability tail. For EACH note it
// measures the DOM Range fragments of that note's OWN beginning text and end
// marker text, and reports the visible region as the intersection of the note
// stack's viewport, the actual scroll-body clip and the browser viewport.
// Literal text existing in a node is never proof; the marker predicate requires
// an actual fragment inside this region.
function stackMarkersExpr(beginMarkers, endMarkers) {
  return `(() => {
    const scope = window.__annotationDocumentScope;
    const scroll = window.__annotationDocumentScroll;
    if (!scroll || !scroll.isConnected || scroll.ownerDocument !== document) return null;
    if (!scope || !scope.isConnected || scope.ownerDocument !== document) return null;
    const rect = (el) => { const r = el.getBoundingClientRect(); return { left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: r.width, height: r.height }; };
    const note0 = scope.querySelector("[data-annotation-note]");
    if (!note0) return null;
    const stack = note0.parentElement;
    if (!stack) return null;
    const beginMarkers = ${q(beginMarkers)};
    const endMarkers = ${q(endMarkers)};
    const viewport = { left: 0, top: 0, right: window.innerWidth, bottom: window.innerHeight };
    const intersect = (...rects) => rects.reduce((a, b) => ({ left: Math.max(a.left, b.left), top: Math.max(a.top, b.top), right: Math.min(a.right, b.right), bottom: Math.min(a.bottom, b.bottom) }));
    const fragmentsFor = (node, marker, fromEnd) => {
      const textNode = node.firstChild;
      if (!textNode || textNode.nodeType !== Node.TEXT_NODE) return [];
      const at = fromEnd ? textNode.data.lastIndexOf(marker) : textNode.data.indexOf(marker);
      if (at < 0) return [];
      const range = document.createRange();
      range.setStart(textNode, at);
      range.setEnd(textNode, at + marker.length);
      return [...range.getClientRects()]
        .filter((r) => r.width > 0 || r.height > 0)
        .map((r) => ({ left: r.left, top: r.top, right: r.right, bottom: r.bottom }));
    };
    const notes = [...stack.querySelectorAll("[data-annotation-note]")].map((node, index) => ({
      text: node.textContent,
      scrollWidth: node.scrollWidth,
      clientWidth: node.clientWidth,
      begin: fragmentsFor(node, beginMarkers[index], false),
      end: fragmentsFor(node, endMarkers[index], true),
    }));
    return {
      paneClip: rect(scope),
      scrollClip: rect(scroll),
      stack: rect(stack),
      visibleClip: intersect(rect(stack), rect(scroll), viewport),
      stackScroll: { scrollTop: stack.scrollTop, scrollHeight: stack.scrollHeight, clientHeight: stack.clientHeight, scrollWidth: stack.scrollWidth, clientWidth: stack.clientWidth },
      notes,
      documentScrollTop: scroll.scrollTop,
    };
  })()`;
}

// The stack's own scroll offset and the document's, for settlement polling.
const stackScrollProbeExpr = `(() => {
  const scroll = window.__annotationDocumentScroll;
  const note = document.querySelector("[data-annotation-note]");
  const stack = note ? note.parentElement : null;
  return { stackTop: stack ? stack.scrollTop : null, documentTop: scroll ? scroll.scrollTop : null };
})()`;

// Resolve after two animation frames: a real browser paint boundary.
async function nextPaint() {
  await evaluate(driver.send, `new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve(true))))`);
}

// Wait for a genuine wheel step to MOVE the stack's own offset and then settle
// (stable observations across a paint boundary). A deadline FAILS with a
// diagnostic rather than masquerading as settlement. No state is repaired.
async function settleAfterWheel(previousTop) {
  const moveDeadline = Date.now() + 2000;
  for (;;) {
    const probe = await evaluate(driver.send, stackScrollProbeExpr);
    const top = probe ? probe.stackTop : null;
    if (top !== null && top !== previousTop) break;
    if (Date.now() > moveDeadline) {
      throw new Error(`the note stack did not move after the wheel step (scrollTop stayed ${previousTop})`);
    }
    await nextPaint();
  }
  let previous;
  let stable = 0;
  const stableDeadline = Date.now() + 2000;
  for (;;) {
    const probe = await evaluate(driver.send, stackScrollProbeExpr);
    const top = probe ? probe.stackTop : null;
    if (previous !== undefined && top === previous) stable += 1;
    else stable = 0;
    previous = top;
    if (stable >= 2) return probe;
    if (Date.now() > stableDeadline) {
      throw new Error(`the note stack never settled after the wheel step (scrollTop ${top})`);
    }
    await nextPaint();
  }
}

async function assertNoteStackSurface() {
  const beginMarkers = NOTE_STACK_NOTES.map((note) => note.split("\n")[0]);
  const endMarkers = NOTE_STACK_END_MARKERS;

  // The document reading position immediately BEFORE the reveal.
  const positionBefore =
    (await evaluate(driver.send, `(() => { const s = window.__annotationDocumentScroll; return s ? s.scrollTop : null; })()`)) ?? 0;

  // Reveal the complete overlapping stack through a real highlight click.
  const bands = await evaluate(driver.send, highlightBandsExpr);
  assert.ok(Array.isArray(bands) && bands.length > 0, "the stacked annotations expose their saved highlight bands");
  const band = bands[0];
  await driver.clickAt((band.left + band.right) / 2, (band.top + band.bottom) / 2);
  await driver.waitPage(
    `(() => { const v = ${stackStateExpr}; return v && v.notes.length === 3 ? true : null; })()`,
    { label: "the highlight click opens the complete overlapping note stack", timeoutMs: 5000 },
  );

  // Immediately measure, with no helper-induced visibility repair, and record
  // the first rectangles and overflow/wrapping dimensions even when a later
  // assertion fails.
  const view = await evaluate(driver.send, stackStateExpr);
  writeFileSync(artifact("note-stack.json"), JSON.stringify(view, null, 2));
  assert.deepEqual(view.notes.map((note) => note.text), NOTE_STACK_NOTES, "the stack renders the three literal notes in creation order");
  assert.ok(
    view.notes.every((note, index) => note.text.endsWith(endMarkers[index])),
    "each stack note keeps its own literal end marker",
  );
  assert.ok(
    Math.abs(view.documentScrollTop - positionBefore) <= 1,
    `revealing the note stack held the document reading position (${view.documentScrollTop} vs ${positionBefore})`,
  );

  // 1. The complete revealed stack is inside the actual scroll body.
  assert.ok(
    noteStackContained({ paneRect: view.paneClip, scrollRect: view.scrollClip }, view.stack),
    `the complete revealed note stack is inside the actual scroll body, stack ${JSON.stringify(view.stack)} scrollClip ${JSON.stringify(view.scrollClip)}`,
  );
  assertions["stack-bounded"] = true;

  // 2. Unbroken text wraps with no hidden horizontal remainder.
  const overflowing = view.notes.filter((note) => note.scrollWidth > note.clientWidth + 1);
  assert.equal(
    overflowing.length,
    0,
    `every note wraps its text without hidden horizontal remainder; overflowing ${JSON.stringify(
      overflowing.map((note) => ({ text: note.text.slice(0, 24), scrollWidth: note.scrollWidth, clientWidth: note.clientWidth })),
    )}`,
  );
  assertions["unbroken-wrapped"] = true;

  // 3. Reachability: every note's OWN beginning and end marker must become
  //    visible across bounded, genuine wheel steps with observed settlement,
  //    while the document reading position is held. A tall note's beginning and
  //    end become visible at DIFFERENT internal scroll offsets; the whole note
  //    is never required to fit at once.
  const required = [];
  for (let index = 0; index < NOTE_STACK_NOTES.length; index += 1) required.push(`${index}:begin`, `${index}:end`);
  const observed = new Set();
  const steps = [];
  // Every observation ASSERTS literal notes/order, stack bounds and horizontal
  // readability. The evidence is written BEFORE any assertion can abort, so a
  // failing step stays inspectable.
  const collect = (state, phase) => {
    state.notes.forEach((note, index) => {
      if (markerVisible(state.visibleClip, note.begin)) observed.add(`${index}:begin`);
      if (markerVisible(state.visibleClip, note.end)) observed.add(`${index}:end`);
    });
    steps.push({
      phase,
      stackScrollTop: state.stackScroll.scrollTop,
      documentScrollTop: state.documentScrollTop,
      visibleClip: state.visibleClip,
      observed: [...observed],
      notes: state.notes.map((note) => ({
        text: note.text,
        scrollWidth: note.scrollWidth,
        clientWidth: note.clientWidth,
        beginVisible: markerVisible(state.visibleClip, note.begin),
        endVisible: markerVisible(state.visibleClip, note.end),
      })),
      bounded: noteStackContained({ paneRect: state.paneClip, scrollRect: state.scrollClip }, state.stack),
      horizontalRemainder: state.notes.map((note) => note.scrollWidth - note.clientWidth),
    });
    writeFileSync(artifact("note-stack-steps.json"), JSON.stringify({ positionBefore, required, observed: [...observed], steps }, null, 2));
    assert.deepEqual(
      state.notes.map((note) => note.text),
      NOTE_STACK_NOTES,
      `${phase}: the stack preserves the three literal notes and creation order`,
    );
    assert.ok(
      noteStackContained({ paneRect: state.paneClip, scrollRect: state.scrollClip }, state.stack),
      `${phase}: the revealed stack is inside the actual scroll body, stack ${JSON.stringify(state.stack)} scrollClip ${JSON.stringify(state.scrollClip)}`,
    );
    const overflowing = state.notes.filter((note) => note.scrollWidth > note.clientWidth + 1);
    assert.equal(
      overflowing.length,
      0,
      `${phase}: every note keeps its text horizontally readable; overflowing ${JSON.stringify(
        overflowing.map((note) => ({ text: note.text.slice(0, 24), scrollWidth: note.scrollWidth, clientWidth: note.clientWidth })),
      )}`,
    );
  };

  const initial = await evaluate(driver.send, stackMarkersExpr(beginMarkers, endMarkers));
  writeFileSync(artifact("note-stack-markers-initial.json"), JSON.stringify(initial, null, 2));
  collect(initial, "initial");

  const center = { x: (view.stack.left + view.stack.right) / 2, y: (view.stack.top + view.stack.bottom) / 2 };
  const MAX_STEPS = 16;
  let lastState = initial;
  for (let step = 0; step < MAX_STEPS && observed.size < required.length; step += 1) {
    // Dispatch only within the latest observed internal remaining range: stop
    // before sending input beyond the boundary instead of demanding an
    // overscroll lock.
    const latest = await evaluate(driver.send, stackMarkersExpr(beginMarkers, endMarkers));
    const remaining =
      latest.stackScroll.scrollHeight - latest.stackScroll.clientHeight - latest.stackScroll.scrollTop;
    if (remaining <= 0) break;
    await driver.send("Input.dispatchMouseEvent", {
      type: "mouseWheel",
      x: center.x,
      y: center.y,
      deltaX: 0,
      deltaY: Math.min(200, remaining),
    });
    await settleAfterWheel(latest.stackScroll.scrollTop);
    const state = await evaluate(driver.send, stackMarkersExpr(beginMarkers, endMarkers));
    collect(state, `step-${step + 1}`);
    assert.ok(
      Math.abs(state.documentScrollTop - positionBefore) <= 1,
      `step ${step + 1}: scrolling the note stack held the document reading position (${state.documentScrollTop} vs ${positionBefore})`,
    );
    lastState = state;
  }
  writeFileSync(
    artifact("note-stack-steps.json"),
    JSON.stringify({ positionBefore, required, observed: [...observed], steps }, null, 2),
  );
  assert.deepEqual(
    [...observed].sort(),
    [...required].sort(),
    `every note beginning and end became visible across bounded in-range scroll steps (observed ${JSON.stringify([...observed].sort())}; stack ${JSON.stringify(lastState.stack)} scrollClip ${JSON.stringify(lastState.scrollClip)} scrollTop ${lastState.stackScroll.scrollTop} of ${lastState.stackScroll.scrollHeight - lastState.stackScroll.clientHeight})`,
  );
  assertions["stack-reachable"] = true;
}

async function assertsNoteStackContinuation() {
  // Note 1 reuses the FIRST selection the shared flow already made.
  await typeNoteIntoDocumentEditor(NOTE_STACK_NOTES[0]);
  await clickDocumentSend();
  await waitForStorage((candidate) => annotationPayloads(candidate).length >= 1, "stack note 1: the committed payload");
  await driver.waitPage(
    `(() => { const v = ${noteEditorExpr}; return v === null ? true : null; })()`,
    { label: "stack note 1: the comment box clears after Send", timeoutMs: 10000 },
  );

  await saveStackNote(NOTE_STACK_LINE_RANGES[1], NOTE_STACK_NOTES[1], 2);
  await saveStackNote(NOTE_STACK_LINE_RANGES[2], NOTE_STACK_NOTES[2], 3);

  await assertStackStored();
  assertions["exact-stack-notes"] = true;

  await assertNoteStackSurface();
  await assertNoQueuedMutation();
}

// ---- run ----

try {
  await driver.start();
  driver.page.ws.addEventListener("message", (event) => {
    const value = JSON.parse(event.data);
    if (value.method === "Runtime.exceptionThrown") errors.push(value.params);
    if (value.method === "Runtime.consoleAPICalled") {
      consoleEvents.push(value.params);
      if (["error", "warning"].includes(value.params.type)) errors.push(value.params);
    }
    if (value.method === "Network.webSocketFrameSent" || value.method === "Network.webSocketFrameReceived") {
      try {
        frames.push({ direction: value.method, requestId: value.params.requestId, ...JSON.parse(value.params.response.payloadData) });
      } catch {}
    }
    if (value.method === "Network.responseReceived") {
      const response = value.params.response;
      if (typeof response.url === "string" && response.url.includes("/doc/file")) {
        const etag = Object.entries(response.headers ?? {}).find(([key]) => key.toLowerCase() === "etag")?.[1] ?? "";
        docResponses.push({ url: response.url, status: response.status, revision: /^"([0-9a-f]{64})"$/.exec(String(etag).trim())?.[1] });
      }
    }
  });
  await navigateTo(driver.page, fixture.url);

  // Passive trusted-input observation is installed before the composer input and
  // the drag. Limits: `Input.insertText` fires no pointer events, so the
  // composer typing is visible only through selectionchange strings; there is no
  // trust flag for selectionchange.
  await installPassiveInputObservation();

  await driver.openSession(fixture.rootRef);

  assert.ok(frames.length > 0, "no real AppWire traffic was observed");

  await driver.waitPage(
    `(() => document.querySelector('button[aria-label="Open beside: NOTES.md"]') !== null ? true : null)()`,
    { label: "the real read_file file card's Open-beside control" },
  );

  await driver.focusComposer(fixture.rootRef);
  await driver.typeText(fixture.rootRef, "Existing draft");

  await driver.click('button[aria-label="Open beside: NOTES.md"]');
  await driver.waitPage(firstParagraphExpr, { label: "the production document viewer rendered the literal first paragraph" });

  assert.ok(docResponses.length > 0, "no real /doc/file read occurred");
  const read = docResponses.at(-1);
  assert.equal(read.status, 200, "the document read succeeded");
  assert.equal(read.revision, SOURCE_SHA256, "the read revision equals the literal fixture SHA256");

  const scope = await observeDocumentScope();

  // The fixture document key, derived from the real origin and fixture facts.
  const documentOrigin = await evaluate(driver.send, "location.origin");
  expectedDocumentKey = { namespace: { kind: "browser", origin: documentOrigin }, sessionRef: fixture.rootRef, path: DOCUMENT_PATH };
  expectedDocumentKeyEncoded = JSON.stringify(["browser", documentOrigin, fixture.rootRef, DOCUMENT_PATH]);

  // The observed settled boundary; wait for the real committed initial draft,
  // then capture the full pre-annotation storage baseline.
  annotationBaseline = frames.length;
  await waitForInitialDraft();
  await captureStorageBaseline();

  // The note-stack target sits low in the overflowing fixture; bring it into the
  // visible clip before the real selection.
  if (STACK) await scrollStackTargetIntoView();

  const geometry = await evaluate(driver.send, firstGeometryExpr);
  assert.ok(geometry, "independent FIRST geometry must be nonempty before dragging");
  // The reading position immediately before the selection, so the finished
  // selection's own scroll movement can be bounded without any helper.
  const preSelectionScroll =
    (await evaluate(driver.send, `(() => { const s = window.__annotationDocumentScroll; return s ? s.scrollTop : null; })()`)) ??
    0;
  await dragSelection(geometry);
  await driver.waitPage(
    `(() => { const s = window.getSelection(); return s && s.toString().length > 0 ? true : null; })()`,
    { label: "a real browser selection exists after the drag", timeoutMs: 5000 },
  );

  // Pin the actual endpoints to the literal text node and offsets 18..22.
  const endpoints = await evaluate(driver.send, selectionEndpointsExpr);
  assert.ok(endpoints, "the browser selection has a range");
  assert.equal(endpoints.text, FIRST_SELECTED, `the drag selected the literal FIRST passage, got ${JSON.stringify(endpoints.text)}`);
  assert.equal(endpoints.range.startLiteral, true, "the selection range starts in the literal FIRST text node");
  assert.equal(endpoints.range.endLiteral, true, "the selection range ends in the literal FIRST text node");
  assert.deepEqual(
    [endpoints.range.startOffset, endpoints.range.endOffset].sort((a, b) => a - b),
    [FIRST.start, FIRST.end],
    "the selection range covers exactly offsets 18..22",
  );
  assert.equal(endpoints.anchor.literalNode && endpoints.focus.literalNode, true, "anchor and focus are both in the literal FIRST text node");
  assert.deepEqual(
    [endpoints.anchor.offset, endpoints.focus.offset].sort((a, b) => a - b),
    [FIRST.start, FIRST.end],
    "anchor and focus offsets are exactly 18 and 22",
  );

  // Genuine trusted pointer/input observations, installed before the input. The
  // browser dispatches the final selectionchange asynchronously, so wait
  // boundedly for that observation before reading the trace.
  await driver.waitPage(
    `(() => { const r = window.__annotationInputs; return r && r.selectionchange.includes(${q(FIRST_SELECTED)}) ? true : null; })()`,
    { label: "a selectionchange observed the literal FIRST text", timeoutMs: 3000 },
  );
  const inputs = await evaluate(driver.send, inputObservationExpr);
  assert.ok(inputs && inputs.pointerdown.length > 0 && inputs.pointerup.length > 0, "trusted pointerdown/pointerup were observed");
  assert.ok(inputs.pointerdown.every((event) => event.trusted) && inputs.pointerup.every((event) => event.trusted), "the observed pointer events are trusted");
  assert.ok(inputs.mousemove.some((event) => event.trusted === true && event.buttons === 1), "a trusted drag move was observed");
  assert.ok(inputs.selectionchange.includes(FIRST_SELECTED), "a selectionchange observed the literal FIRST text");

  assertions["finished-selection"] = true;
  driver.milestone("finished-selection", { selected: endpoints.text, endpoints, rects: geometry.rects });
  writeFileSync(artifact("input-observations.json"), JSON.stringify(inputs, null, 2));

  // The real pre-UI geometry preflight: the SAME independent scope/Range/clip
  // measurement the later highlight comparison uses, exercised now.
  const preflight = await measureClippedFirst();
  driver.milestone("geometry-preflight", { scope, preflight });
  writeFileSync(artifact("geometry-preflight.json"), JSON.stringify({ scope, preflight }, null, 2));

  // --- the RED boundary: the unchanged reader has no comment box ---
  await driver.waitPage(noteEditorExpr, {
    label: "the annotation comment box opens after the finished selection",
    timeoutMs: 10000,
  });
  driver.milestone("comment-box-opened");

  if (STACK) {
    await assertsNoteStackContinuation();
  } else if (LONG) {
    // Observed BEFORE any scrolling/focusing helper can repair the placement.
    await assertLongControlsVisible(preSelectionScroll);
    await assertsLongContinuation();
  } else {
    await assertsContinuation();
  }
} catch (error) {
  failure = error;
  console.error(error.stack ?? String(error));
  if (driver.page) await capture("failure").catch((captureError) => console.error(captureError));
} finally {
  if (annotationBaseline === null) annotationBaseline = frames.length;
  const forbiddenMutationCalls = forbiddenCount(annotationBaseline);
  writeFileSync(artifact("console.json"), JSON.stringify(consoleEvents, null, 2));
  writeFileSync(artifact("rpc-frames.json"), JSON.stringify(frames, null, 2));
  writeFileSync(artifact("doc-reads.json"), JSON.stringify(docResponses, null, 2));
  writeFileSync(artifact("result.json"), JSON.stringify({
    assertions,
    forbiddenMutationCalls,
    failure: failure ? String(failure.message ?? failure) : null,
    blocked: Object.entries(assertions).filter(([, reached]) => !reached).map(([name]) => name),
    consoleErrors: errors.length,
  }, null, 2));
  await driver.stop();
}
if (failure) process.exitCode = 1;
