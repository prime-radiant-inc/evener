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
import { cdpDrag, evaluate, navigateTo, nextPaint } from "../browserGuardCdp.mjs";
import { Driver } from "../skillguard/run.mjs";
import { noteStackContained, STORES } from "./assertions.mjs";
// The literal expectations below are independent of production
// mapping/formatter code: the short values are FIRST's own 1:19-1:22.
import { FIRST, JOURNEY_APPENDED, NOTE, SOURCE, SOURCE_SHA256 } from "./fixtures.mjs";

const fixture = JSON.parse(readFileSync(0, "utf8"));
const driver = new Driver(fixture);
const q = JSON.stringify;
const artifact = (name) => path.join(fixture.artifactDir, name);

// The assertion names the Go test validates independently. The driver only
// records whether it reached each one.
const assertions = {
  "finished-selection": false,
  "exact-draft": false,
  "exact-highlight": false,
  "literal-note": false,
  "unchanged-reopen": false,
};

const FORBIDDEN = new Set(["turn/start", "turn/steer", "turn/queue", "thread/start", "thread/resume"]);

// Literal expectations independent of production mapping/formatter code. The
// source positions are FIRST's own: 1:19-1:22 (line 1, columns 19..22).
const FIRST_TEXT = SOURCE.split("\n")[0];
const FIRST_SELECTED = SOURCE.slice(FIRST.start, FIRST.end);
const FIRST_POSITIONS = { start: { line: 1, column: 19 }, endInclusive: { line: 1, column: 22 } };
// The accepted contextForRange bounds for FIRST's opening paragraph.
const FIRST_CONTEXT_BEFORE = "First occurrence: ";
const FIRST_CONTEXT_AFTER = "";
const NOTE_EDITOR_SELECTOR = "textarea, input, [role='textbox'], [contenteditable=''], [contenteditable='true']";
const INITIAL_DRAFT = "Existing draft";
const DOCUMENT_PATH = "NOTES.md";
// A real second session supplied by the Go test. The driver types its own draft
// through the production composer so the annotation journey holds a genuine
// unrelated-session draft row in the store.
const OTHER_REF = fixture.otherRef;
const OTHER_DRAFT = "Other session draft";

// Set on the current navigation from the real location.origin; used to match
// the annotation document key against fixture facts, not an arbitrary payload.
let expectedDocumentKey = null;
let expectedDocumentKeyEncoded = null;

const LOCAL_DATABASE_NAME = "evener-mutation-outbox";

const consoleEvents = [];
const errors = [];
const frames = [];
const docResponses = [];
let annotationBaseline = null;
let failure = null;
let storageBaseline = null;
let draftBaselineRows = null;

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
  return evaluate(
    driver.send,
    `(() => {
    if (window.__annotationInputs) return true;
    // A stable path for the ACTUAL event target, shared by the observer and the
    // lanes so an intended target recorded by a lane is the same string the
    // observer captured for a real event. Element steps are tag:index; a text
    // node is "#text". This changes no product markup.
    const nodePath = (node) => {
      const parts = [];
      let n = node;
      while (n && n.nodeType !== 1 && n.parentElement) n = n.parentElement;
      while (n) {
        if (n.nodeType === 3) { parts.unshift("#text"); n = n.parentElement; continue; }
        if (n.nodeType !== 1) break;
        let index = 0;
        let s = n;
        while (s.previousElementSibling) { index += 1; s = s.previousElementSibling; }
        parts.unshift(n.tagName + ":" + index);
        n = n.parentElement;
      }
      return parts.join("/");
    };
    window.__annotationPath = nodePath;
    const record = window.__annotationInputs = { pointerdown: [], pointerup: [], mousemove: [], keydown: [], selectionchange: [], wheel: [], touchstart: [], touchmove: [], touchend: [] };
    const describeTarget = (target) => {
      if (!(target instanceof Element)) return { node: target === null ? "null" : typeof target };
      const band = target.closest("[data-document-highlight]");
      return {
        tag: target.tagName,
        className: String(target.className || ""),
        ariaLabel: target.getAttribute("aria-label"),
        bandOperationId: band ? band.getAttribute("data-operation-id") : null,
        inBand: band !== null,
        path: nodePath(target),
      };
    };
    document.addEventListener("pointerdown", (event) => {
      record.pointerdown.push({ trusted: event.isTrusted, x: event.clientX, y: event.clientY, shiftKey: event.shiftKey, buttons: event.buttons, button: event.button, target: describeTarget(event.target) });
    }, { capture: true, passive: true });
    document.addEventListener("pointerup", (event) => {
      record.pointerup.push({ trusted: event.isTrusted, x: event.clientX, y: event.clientY, shiftKey: event.shiftKey, buttons: event.buttons, button: event.button, target: describeTarget(event.target) });
    }, { capture: true, passive: true });
    document.addEventListener("mousemove", (event) => {
      record.mousemove.push({ trusted: event.isTrusted, x: event.clientX, y: event.clientY, buttons: event.buttons, shiftKey: event.shiftKey, target: nodePath(event.target) });
    }, { capture: true, passive: true });
    // keydown records the actual modifier state the browser observed, so a
    // discriminating Shift or Ctrl lane can assert it rather than assume its own
    // input. The real target is recorded too (a keyboard target may be body or
    // another focus owner, so it is recorded, never assumed to be the reference).
    document.addEventListener("keydown", (event) => {
      record.keydown.push({ trusted: event.isTrusted, key: event.key, code: event.code, shiftKey: event.shiftKey, ctrlKey: event.ctrlKey, target: nodePath(event.target) });
    }, { capture: true, passive: true });
    // selectionchange carries the selected STRING only; the browser fires no
    // trust flag for it, so no trust is claimed for these entries.
    document.addEventListener("selectionchange", () => {
      const s = window.getSelection();
      record.selectionchange.push(s ? s.toString() : null);
    }, { capture: true, passive: true });
    // wheel carries the actual target, the input point, deltas and Ctrl state so
    // a nested-scroll receipt can prove delivered input and its geometry.
    document.addEventListener("wheel", (event) => {
      record.wheel.push({ trusted: event.isTrusted, x: event.clientX, y: event.clientY, deltaX: event.deltaX, deltaY: event.deltaY, ctrlKey: event.ctrlKey, target: nodePath(event.target) });
    }, { capture: true, passive: true });
    // touch phases carry trust, coordinates and the ACTUAL target node path; the
    // delivered count is the length of these records, never a command count.
    document.addEventListener("touchstart", (event) => {
      const t = event.touches[0] ?? event.changedTouches[0];
      record.touchstart.push({ trusted: event.isTrusted, x: t ? t.clientX : null, y: t ? t.clientY : null, target: nodePath(event.target) });
    }, { capture: true, passive: true });
    document.addEventListener("touchmove", (event) => {
      const t = event.touches[0] ?? event.changedTouches[0];
      record.touchmove.push({ trusted: event.isTrusted, x: t ? t.clientX : null, y: t ? t.clientY : null, target: nodePath(event.target) });
    }, { capture: true, passive: true });
    document.addEventListener("touchend", (event) => {
      const t = event.changedTouches[0];
      record.touchend.push({ trusted: event.isTrusted, x: t ? t.clientX : null, y: t ? t.clientY : null, target: nodePath(event.target) });
    }, { capture: true, passive: true });
    return true;
  })()`,
  );
}

// A read-only snapshot of the real annotation/draft/outbox storage. A missing
// database or a missing required store is an observation FAILURE, never coerced
// to an empty array; a database that did not exist is never created as an
// observer. Every required store is read in one readonly transaction, the
// promise resolves only on successful transaction completion, request and
// transaction failures are joined, and the connection is always closed. No
// writes and no seeding.
async function readStorage() {
  return evaluate(
    driver.send,
    `new Promise((resolve, reject) => {
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
  })`,
  );
}

// A detached copy of the real stores: value comparisons see a stable snapshot
// even if the page writes again mid-assertion.
const cloneStorage = async () => JSON.parse(JSON.stringify(await readStorage()));

// Wait boundedly for a storage snapshot satisfying `predicate`. Reading the
// real storage repeatedly (never a sleep-then-assume) is how the asynchronous
// draft/payload/receipt transitions are observed before exact equality.
async function waitForStorage(predicate, label, timeoutMs = 15000) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const snapshot = await cloneStorage();
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
    return (
      drafts.length === 1 && drafts[0].text === INITIAL_DRAFT && drafts[0].token && drafts[0].token.sessionRevision >= 1
    );
  }, "the committed initial composer draft row");
}

function sessionRows(snapshot, store) {
  return (snapshot[store] ?? []).filter(
    (row) => row?.key?.sessionRef === fixture.rootRef && row?.key?.namespace?.kind === "browser",
  );
}

function annotationPayloads(snapshot) {
  return (snapshot.annotationPayloads ?? []).filter(
    (row) => row?.operation?.lease?.key?.sessionRef === fixture.rootRef,
  );
}

function annotationOperations(snapshot, operationId) {
  return (snapshot.annotationOperations ?? []).filter((row) => row?.operationId === operationId);
}

// ---- browser scope / geometry helpers ----

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

// ---- rendered draft helpers ----

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

// ---- the retained journey continuation ----

// The forward accessible contract (see README): the document pane must expose a
// note editor with value semantics, an annotation Send control, highlight bands
// carrying their operation id, and a note surface a highlight interaction opens.

async function typeNoteIntoDocumentEditor(note = NOTE) {
  const before = await evaluate(driver.send, noteEditorExpr);
  if (!before) throw new Error("the document-scoped note editor is not present");
  await evaluate(
    driver.send,
    `(() => { const scope = window.__annotationDocumentScope;
    const editor = scope.querySelector(${q(NOTE_EDITOR_SELECTOR)}); editor.focus(); return true; })()`,
  );
  await driver.send("Input.insertText", { text: note });
  await driver.waitPage(
    `(() => { const state = ${noteEditorExpr}; return state && state.value === ${q(note)} ? true : null; })()`,
    {
      label: "the document note editor holds the typed note",
    },
  );
}

async function clickDocumentSend() {
  const point = await evaluate(
    driver.send,
    `(() => {
    const scope = window.__annotationDocumentScope;
    if (!scope || !scope.isConnected) return null;
    const button = [...scope.querySelectorAll("button")].find((b) => b.textContent.trim() === "Send" || (b.getAttribute("aria-label") ?? "") === "Send");
    if (!button) return null;
    button.scrollIntoView({ block: "center" });
    const r = button.getBoundingClientRect();
    return { x: r.x + r.width / 2, y: r.y + r.height / 2 };
  })()`,
  );
  if (!point) throw new Error("the document annotation Send control is not present");
  await driver.clickAt(point.x, point.y);
}

// The real reveal surface: band count, revealed title/text and the active
// element, read from the live document scope.
const revealStateExpr = `(() => {
  const scope = window.__annotationDocumentScope;
  if (!scope || !scope.isConnected) return null;
  const active = document.activeElement;
  return {
    bandCount: scope.querySelectorAll("[data-document-highlight]").length,
    noteCount: scope.querySelectorAll("[data-annotation-note]").length,
    activeElementTag: active ? active.tagName : null,
    activeElementInBand: !!(active && active.closest && active.closest("[data-document-highlight]")),
  };
})()`;

// A genuine CDP mouse drag between two independently computed points (no
// Selection.addRange, no synthetic events). An ordinary approach move (no
// button) precedes the press, exactly as a reader moves the pointer to the drag
// start first; the resulting hover state is recorded rather than routed around.
async function dragBetween(from, to, modifiers = 0, label = "drag") {
  // The genuine shared input sequence; the hook records the hover state the
  // approach move itself created, between approach and press.
  await cdpDrag(driver.send, from, to, {
    modifiers,
    onApproach: async () => {
      await nextPaint(driver.send);
      const approachState = await evaluate(driver.send, revealStateExpr);
      driver.milestone(`${label}-approach`, { from, modifiers, reveal: approachState });
      writeFileSync(
        artifact(`${label}-approach.json`),
        JSON.stringify({ from, modifiers, reveal: approachState }, null, 2),
      );
    },
  });
}

// The journey's single drag entry point; delegates to the reusable `dragBetween`
// so there is one input implementation.
async function dragSelection(geometry) {
  await dragBetween(geometry.start, geometry.end);
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
  assert.deepEqual(
    operation.lease.key,
    expectedDocumentKey,
    "the payload document key matches the fixture namespace/origin/session/path",
  );
  assert.equal(
    payloads[0].documentKey,
    expectedDocumentKeyEncoded,
    "the payload row's encoded document key matches the fixture document",
  );
  assert.equal(operation.note, NOTE, "the stored operation note is the literal note");
  assert.deepEqual(operation.capture.sourceRange, FIRST, "the stored capture source range is literal FIRST");
  assert.deepEqual(operation.capture.start, FIRST_POSITIONS.start, "the stored capture start position is literal 1:19");
  assert.deepEqual(
    operation.capture.endInclusive,
    FIRST_POSITIONS.endInclusive,
    "the stored capture end position is literal 1:22",
  );
  assert.equal(
    operation.capture.selectedRenderedText,
    FIRST_SELECTED,
    "the stored capture selected text is literal FIRST text",
  );
  assert.equal(operation.capture.contextBefore, FIRST_CONTEXT_BEFORE, "the stored capture context-before is literal");
  assert.equal(operation.capture.contextAfter, FIRST_CONTEXT_AFTER, "the stored capture context-after is literal");
  assert.equal(
    operation.capture.evidence.wholeRevision,
    SOURCE_SHA256,
    "the stored capture revision is the literal SHA256",
  );
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

// Reach the saved note through a genuine highlight interaction, not the editor.
// The note surface is waited on boundedly before exact equality.
async function assertSavedNoteViaHighlight(operationId) {
  const bands = await evaluate(driver.send, highlightBandsExpr);
  assert.ok(Array.isArray(bands) && bands.length > 0, "the document exposes its saved highlight bands");
  const band = bands.find((candidate) => candidate.operationId === operationId);
  assert.ok(band, "a saved band carries the applied operation id");
  // The click coordinates come from a separate CDP roundtrip after the rect
  // read: a layout reflow between the two (a font swap, an async diagram
  // growth) moves the painted band and the click lands on bare prose, so the
  // reveal never fires and the wait times out. Re-read the CURRENT rect and
  // click again, bounded, so the reveal is always exercised against where the
  // band actually is. A repeated tap only toggles a note that is already
  // revealed, and each attempt waits for exactly that note, so a missed click
  // (no note) is always safe to retry.
  let opened = false;
  for (let attempt = 1; attempt <= 3 && !opened; attempt += 1) {
    if (attempt > 1) {
      const fresh = await evaluate(driver.send, highlightBandsExpr);
      const moved = Array.isArray(fresh) ? fresh.find((candidate) => candidate.operationId === operationId) : null;
      assert.ok(moved, "the saved band is still painted for its reveal click");
      await driver.clickAt((moved.left + moved.right) / 2, (moved.top + moved.bottom) / 2);
    } else {
      await driver.clickAt((band.left + band.right) / 2, (band.top + band.bottom) / 2);
    }
    try {
      await driver.waitPage(
        `(() => {
        const scope = window.__annotationDocumentScope;
        if (!scope || !scope.isConnected) return null;
        const node = scope.querySelector("[data-annotation-note]");
        return node && node.textContent === ${q(NOTE)} ? true : null;
      })()`,
        { label: "the highlight click opens the literal saved note", timeoutMs: 1800 },
      );
      opened = true;
    } catch (error) {
      if (attempt === 3) throw error;
    }
  }
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
// suppresses no error; the comparison below is untouched. The independently
// measured expected rect is attached as `measured`.
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
    receipt = { error: String(error?.message ? error.message : error) };
  }
  writeFileSync(
    artifact("highlight-geometry.json"),
    JSON.stringify({ operationId, measured, bands, receipt }, null, 2),
  );
  const mine = bands.filter((band) => band.operationId === operationId);
  assert.equal(mine.length, measured.clipped.length, "one saved band per clipped FIRST fragment");
  const edges = ["left", "top", "right", "bottom"];
  for (const [index, band] of mine.entries()) {
    for (const edge of edges) {
      assert.ok(Number.isFinite(band[edge]), `saved band ${index} ${edge} is finite geometry`);
      assert.ok(
        Math.abs(band[edge] - measured.clipped[index][edge]) <= 1,
        `saved band ${index} ${edge} within 1 CSS pixel`,
      );
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
// assertions without a second Send. It also requires that exactly the one
// stored draft row remains, so an unrelated draft cannot have been dropped.
async function assertUnchangedReopen(retained) {
  const previousReads = docResponses.length;
  await navigateTo(driver.page, fixture.url);
  // A navigation replaces the window; reinstall the passive trusted-input
  // observer so a later gesture still has fresh pointer/selection records.
  await installPassiveInputObservation();
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
    if (candidatePayloads.length !== 1 || candidatePayloads[0].operation.operationId !== retained.operationId)
      return false;
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
  assert.deepEqual(
    payloads[0].operation.destination,
    retained.destination,
    "reopen retains the original destination value",
  );

  const operations = annotationOperations(snapshot, retained.operationId);
  assert.equal(operations.length, 1, "reopen retains exactly one applied operation identity");
  assert.equal(operations[0].state, "applied", "reopen retains the applied receipt state");
  assert.equal(
    operations[0].draftRevision,
    retained.draftRevision,
    "reopen retains the unchanged applied draft revision",
  );

  await assertSavedNoteViaHighlight(retained.operationId);
  await assertHighlightGeometry(retained.operationId, measured);
}

// Explicit preservation of unrelated drafts: the composerDrafts store is
// compared as a WHOLE, so a row that belongs to another session or namespace
// can never be dropped by the annotation. Observed rows are the baseline; only
// the target session's own row may change. The single target row must route to
// the original session ref.
async function assertUnrelatedDraftsPreserved() {
  const snapshot = await cloneStorage();
  const rows = snapshot.composerDrafts ?? [];
  const baseline = draftBaselineRows ?? [];
  const target = rows.filter((row) => row.key?.sessionRef === fixture.rootRef);
  const foreign = rows.filter((row) => row.key?.sessionRef !== fixture.rootRef);
  const baselineForeign = baseline.filter((row) => row.key?.sessionRef !== fixture.rootRef);
  assert.deepEqual(
    foreign,
    baselineForeign,
    "every unrelated composer draft is unchanged by value across the annotation journey",
  );
  assert.equal(target.length, 1, "exactly one composer draft row is routed to the original session");
  assert.equal(target[0].key.sessionRef, fixture.rootRef, "the annotated draft row routes to the original session ref");
  writeFileSync(artifact("drafts-final.json"), JSON.stringify(rows, null, 2));
}

// The pre-annotation outbox/recovery/sequence baseline. The FULL rows are
// retained (not counts) so the post-journey comparison is by value, and the
// snapshot is written as an artifact showing what was observed.
async function captureStorageBaseline() {
  const snapshot = await cloneStorage();
  storageBaseline = {
    outbox: snapshot.outbox ?? [],
    optimistic: snapshot.optimistic ?? [],
    recovery: snapshot.recovery ?? [],
    sequences: snapshot.sequences ?? [],
  };
  draftBaselineRows = snapshot.composerDrafts ?? [];
  writeFileSync(artifact("storage-baseline.json"), JSON.stringify(storageBaseline, null, 2));
  writeFileSync(artifact("drafts-baseline.json"), JSON.stringify(draftBaselineRows, null, 2));
  return storageBaseline;
}

async function assertNoQueuedMutation() {
  const snapshot = await cloneStorage();
  writeFileSync(
    artifact("storage-final.json"),
    JSON.stringify(
      {
        outbox: snapshot.outbox ?? [],
        optimistic: snapshot.optimistic ?? [],
        recovery: snapshot.recovery ?? [],
        sequences: snapshot.sequences ?? [],
      },
      null,
      2,
    ),
  );
  assert.deepEqual(
    snapshot.outbox ?? [],
    storageBaseline.outbox,
    "the outbox rows are unchanged across the annotation journey",
  );
  assert.deepEqual(
    snapshot.optimistic ?? [],
    storageBaseline.optimistic,
    "the optimistic rows are unchanged across the annotation journey",
  );
  assert.deepEqual(
    snapshot.recovery ?? [],
    storageBaseline.recovery,
    "the recovery rows are unchanged across the annotation journey",
  );
  assert.deepEqual(
    snapshot.sequences ?? [],
    storageBaseline.sequences,
    "the mutation sequences are unchanged across the annotation journey",
  );
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
  await assertUnrelatedDraftsPreserved();
  await assertNoQueuedMutation();
  assertions["unchanged-reopen"] = true;
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
        frames.push({
          direction: value.method,
          requestId: value.params.requestId,
          ...JSON.parse(value.params.response.payloadData),
        });
      } catch {}
    }
    if (value.method === "Network.responseReceived") {
      const response = value.params.response;
      if (typeof response.url === "string" && response.url.includes("/doc/file")) {
        const etag = Object.entries(response.headers ?? {}).find(([key]) => key.toLowerCase() === "etag")?.[1] ?? "";
        docResponses.push({
          url: response.url,
          status: response.status,
          revision: /^"([0-9a-f]{64})"$/.exec(String(etag).trim())?.[1],
        });
      }
    }
  });
  await navigateTo(driver.page, fixture.url);

  // Passive trusted-input observation is installed before the composer input and
  // the drag. Limits: `Input.insertText` fires no pointer events, so the
  // composer typing is visible only through selectionchange strings; there is no
  // trust flag for selectionchange.
  await installPassiveInputObservation();

  // A real unrelated-session draft, created through the production composer
  // BEFORE the annotation flow, so the pre-annotation baseline holds a genuine
  // foreign row that the annotation must not disturb.
  assert.ok(OTHER_REF, "the fixture supplies a second session ref for the unrelated draft");
  await driver.openSession(OTHER_REF);
  await driver.focusComposer(OTHER_REF);
  await driver.typeText(OTHER_REF, OTHER_DRAFT);
  await waitForStorage(
    (snapshot) =>
      (snapshot.composerDrafts ?? []).some((row) => row.key?.sessionRef === OTHER_REF && row.text === OTHER_DRAFT),
    "the committed unrelated-session draft",
  );

  await driver.openSession(fixture.rootRef);

  assert.ok(frames.length > 0, "no real AppWire traffic was observed");

  await driver.waitPage(
    `(() => document.querySelector('button[aria-label="Open beside: NOTES.md"]') !== null ? true : null)()`,
    { label: "the real read_file file card's Open-beside control" },
  );

  await driver.focusComposer(fixture.rootRef);
  await driver.typeText(fixture.rootRef, "Existing draft");

  await driver.click('button[aria-label="Open beside: NOTES.md"]');
  await driver.waitPage(firstParagraphExpr, {
    label: "the production document viewer rendered the literal first paragraph",
  });

  assert.ok(docResponses.length > 0, "no real /doc/file read occurred");
  const read = docResponses.at(-1);
  assert.equal(read.status, 200, "the document read succeeded");
  assert.equal(read.revision, SOURCE_SHA256, "the read revision equals the literal fixture SHA256");

  const scope = await observeDocumentScope();

  // The fixture document key, derived from the real origin and fixture facts.
  const documentOrigin = await evaluate(driver.send, "location.origin");
  expectedDocumentKey = {
    namespace: { kind: "browser", origin: documentOrigin },
    sessionRef: fixture.rootRef,
    path: DOCUMENT_PATH,
  };
  expectedDocumentKeyEncoded = JSON.stringify(["browser", documentOrigin, fixture.rootRef, DOCUMENT_PATH]);

  // The observed settled boundary; wait for the real committed initial draft,
  // then capture the full pre-annotation storage baseline.
  annotationBaseline = frames.length;
  await waitForInitialDraft();
  await captureStorageBaseline();
  assert.ok(
    draftBaselineRows.some((row) => row.key?.sessionRef !== fixture.rootRef),
    "the pre-annotation baseline holds a real unrelated-session draft",
  );

  const geometry = await evaluate(driver.send, firstGeometryExpr);
  assert.ok(geometry, "independent FIRST geometry must be nonempty before dragging");
  await dragSelection(geometry);
  await driver.waitPage(
    `(() => { const s = window.getSelection(); return s && s.toString().length > 0 ? true : null; })()`,
    { label: "a real browser selection exists after the drag", timeoutMs: 5000 },
  );

  // Pin the actual endpoints to the literal text node and offsets 18..22.
  const endpoints = await evaluate(driver.send, selectionEndpointsExpr);
  assert.ok(endpoints, "the browser selection has a range");
  assert.equal(
    endpoints.text,
    FIRST_SELECTED,
    `the drag selected the literal FIRST passage, got ${JSON.stringify(endpoints.text)}`,
  );
  assert.equal(endpoints.range.startLiteral, true, "the selection range starts in the literal FIRST text node");
  assert.equal(endpoints.range.endLiteral, true, "the selection range ends in the literal FIRST text node");
  assert.deepEqual(
    [endpoints.range.startOffset, endpoints.range.endOffset].sort((a, b) => a - b),
    [FIRST.start, FIRST.end],
    "the selection range covers exactly offsets 18..22",
  );
  assert.equal(
    endpoints.anchor.literalNode && endpoints.focus.literalNode,
    true,
    "anchor and focus are both in the literal FIRST text node",
  );
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
  assert.ok(
    inputs && inputs.pointerdown.length > 0 && inputs.pointerup.length > 0,
    "trusted pointerdown/pointerup were observed",
  );
  assert.ok(
    inputs.pointerdown.every((event) => event.trusted) && inputs.pointerup.every((event) => event.trusted),
    "the observed pointer events are trusted",
  );
  assert.ok(
    inputs.mousemove.some((event) => event.trusted === true && event.buttons === 1),
    "a trusted drag move was observed",
  );
  assert.ok(inputs.selectionchange.includes(FIRST_SELECTED), "a selectionchange observed the literal FIRST text");

  assertions["finished-selection"] = true;
  driver.milestone("finished-selection", { selected: endpoints.text, endpoints, rects: geometry.rects });
  writeFileSync(artifact("input-observations.json"), JSON.stringify(inputs, null, 2));

  // The real pre-UI geometry preflight: the SAME independent scope/Range/clip
  // measurement the later highlight comparison uses, exercised now.
  const preflight = await measureClippedFirst();
  driver.milestone("geometry-preflight", { scope, preflight });
  writeFileSync(artifact("geometry-preflight.json"), JSON.stringify({ scope, preflight }, null, 2));

  // The retained journey continuation.
  await driver.waitPage(noteEditorExpr, {
    label: "the annotation comment box opens after the finished selection",
    timeoutMs: 10000,
  });
  driver.milestone("comment-box-opened");
  await assertsContinuation();
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
  writeFileSync(
    artifact("result.json"),
    JSON.stringify(
      {
        assertions,
        forbiddenMutationCalls,
        failure: failure ? String(failure.message ?? failure) : null,
        blocked: Object.entries(assertions)
          .filter(([, reached]) => !reached)
          .map(([name]) => name),
        consoleErrors: errors.length,
      },
      null,
      2,
    ),
  );
  await driver.stop();
}
if (failure) process.exitCode = 1;
