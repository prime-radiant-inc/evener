#!/usr/bin/env node
// documentannotationguard/components - the SMALL production-component browser
// cases for the document annotation reader. It mounts the real scaffold,
// markdown renderer, source binding, saved-highlight overlay and comment
// controls (src/dev/documentannotationguard-entry.tsx) and drives them with
// genuine CDP input, so real layout decides every result:
//
//   case pane-resize      - a pane-only width change (no browser viewport
//                           resize) reflows the text; the saved highlight's
//                           PAINTED EXTENT must follow the browser's own text
//                           ranges within a one-pixel tolerance, and the
//                           SELECTED GLYPHS must remain visible.
//   case nested-scroll    - a genuine wheel over a pending reference nested in a
//                          scrollable note stack scrolls the stack, never the
//                          document, and exposes the final reference.
//                           long scrollable note stack must scroll THAT
//                           reference, leave the outer stack and document
//                           unmoved, and not cancel the wheel. The outer stack
//                           must also reach its independently computed maximum
//                           through genuine wheel input.
//   case touch-scroll     - under a coarse-pointer (touch) viewport, a genuine
//                          emulated swipe over an overflowing revealed note pans
//                          the note stack itself and exposes its final lines,
//                          never the underlying document.
//   case selection        - genuine forward and reverse drags across a saved band.
//   case selection-shift  - genuine Shift+Arrow adjustment of the selection.
//   case copy             - genuine Ctrl+C, read back at the clipboard boundary.
//   case overlap-access   - hover, keyboard and emulated-tap access, each from a
//                           freshly unrevealed state, with before/after focus
//                           and reading-position checks.
//   case comment-controls - controls inside the real footer/pane/viewport clips
//                           and a long saved note that really wraps and stays reachable.
//
// Every case is expected-pass now that the pane-resize and nested-scroll
// defects are fixed; a harness/import/launch/timeout failure is reported
// separately and is NOT a product failure. See
// ../browser-test-redesign/unit-1-report.md for scope and the mutation each
// check answers to.
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  applyViewport,
  clearViewportOverride,
  connectOnlyPage,
  createStartupDeadline,
  cdpDrag,
  evaluate,
  nextPaint,
  navigateTo,
  waitForFonts,
  waitForHttp,
} from "../browserGuardCdp.mjs";
import { describeBrowserStartupFailure, startBrowserGuard, waitForBrowserReady } from "../browserGuardProcess.mjs";
import { rectInside } from "./assertions.mjs";
import {
  COMMENT_NOTE,
  COMMENT_PASSAGE,
  COMMENT_SAVED_NOTE,
  COMMENT_SAVED_RANGE,
  COMMENT_SOURCE,
  NESTED_FIRST,
  NESTED_NOTES,
  NESTED_RANGE,
  NESTED_REFERENCES,
  NESTED_TARGET,
  OVERLAP_FIRST,
  OVERLAP_NEUTRAL_RANGE,
  OVERLAP_NOTES,
  OVERLAP_RANGE,
  PANE_CONTROL_RANGE,
  PANE_FIRST,
  PANE_RANGE,
  PANE_SELECTED,
  PANE_SECOND,
  SELECTION_BAND,
  SELECTION_FIRST,
  SELECTION_RANGE,
  SELECTION_SOURCE,
  SELECTION_SELECTED,
  SELECTION_SHIFT_RANGE,
  SELECTION_SHIFT_SELECTED,
  nthParagraph,
} from "./component-fixtures.mjs";

const FRONTEND = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const REPO = path.resolve(FRONTEND, "..", "..", "..");
// Where this run's receipts go. A gate run must never write into the repository
// or `.superpowers/`, so the default is a fresh temporary directory; set
// DOCUMENTANNOTATIONGUARD_EVIDENCE_DIR to keep a run's receipts somewhere
// specific.
const EVIDENCE =
  process.env.DOCUMENTANNOTATIONGUARD_EVIDENCE_DIR ??
  mkdtempSync(path.join(os.tmpdir(), "documentannotationguard-components-"));

const VIEWPORT = { width: 1300, height: 2600 };
const PAINT_TOLERANCE = 10; // max per-channel difference that still counts as "same color"
// A fragment is legible when its ink fraction reaches an absolute floor and a
// fraction of the unannotated control's own ink fraction. Both are deliberately
// generous; the defect hides ALL glyphs, so real ink clears them easily.
const INK_FLOOR = 0.01;
const INK_RELATIVE = 0.4;
const BOOT = {
  bootExpression: "window.__annotationGuardReady === true",
  bootLabel: "the documentannotationguard entry global window.__annotationGuardReady",
};

// The guard page is served through Vite's /@fs/ route rather than its bare
// path. The dev server proxies every request whose URL starts with /doc to a
// running evener-hub (vite.config.ts: proxy["/doc"]), and the authorized guard
// page is named documentannotationguard.html - so the bare URL is answered by
// the hub with 401 unauthorized. /@fs/ serves the same file from the allowed
// frontend root with no proxy prefix, and the page's absolute /src/dev/*
// script URL still boots through Vite.
const guardPageURL = (vitePort) => `http://127.0.0.1:${vitePort}/@fs${FRONTEND}/documentannotationguard.html`;

const q = JSON.stringify;

// --- case failure plumbing --------------------------------------------------

class CaseFailure extends Error {
  constructor({ expected, actual, detail, findings }) {
    super(detail ?? `${expected}; observed ${actual}`);
    this.expected = expected;
    this.actual = actual;
    this.findings = findings ?? null;
  }
}
function check(condition, expected, actual, detail) {
  if (!condition) throw new CaseFailure({ expected, actual, detail });
}
// A soft, non-aborting check: product failures are collected so one RED cannot
// hide another (e.g. stale geometry vs a hidden-text band), then thrown together.
function require_(findings, condition, name, expected, actual) {
  if (!condition) findings.push({ name, expected, actual });
}
function throwFindings(findings) {
  if (findings.length === 0) return;
  throw new CaseFailure({
    expected: findings.map((finding) => `[${finding.name}] ${finding.expected}`).join(" ; "),
    actual: findings.map((finding) => finding.actual).join(" ; "),
    findings,
  });
}

function saveEvidence(name, value) {
  mkdirSync(EVIDENCE, { recursive: true });
  const target = path.join(EVIDENCE, name);
  if (typeof value === "string") writeFileSync(target, value);
  else if (value instanceof Uint8Array) writeFileSync(target, value);
  else writeFileSync(target, `${JSON.stringify(value, null, 2)}\n`);
  return path.relative(REPO, target);
}
function saveScreenshot(name, base64) {
  return saveEvidence(name, Buffer.from(base64, "base64"));
}

// --- CDP helpers ------------------------------------------------------------

// Bounded observable-condition wait: returns the last observed value and
// whether the condition ever held. It never widens a timeout and never throws
// on a RED condition - the caller asserts the observed value.
async function settle(send, expression, { label, timeoutMs = 3000 } = {}) {
  const deadline = Date.now() + timeoutMs;
  let value = null;
  for (;;) {
    value = await evaluate(send, expression);
    if (value) return { settled: true, value };
    if (Date.now() > deadline) return { settled: false, value };
    await nextPaint(send);
  }
}

async function mouseMove(send, x, y, buttons = 0) {
  await send("Input.dispatchMouseEvent", { type: "mouseMoved", x, y, buttons });
}
async function mouseClick(send, x, y, modifiers = 0) {
  await send("Input.dispatchMouseEvent", {
    type: "mousePressed",
    x,
    y,
    button: "left",
    buttons: 1,
    clickCount: 1,
    modifiers,
  });
  await send("Input.dispatchMouseEvent", {
    type: "mouseReleased",
    x,
    y,
    button: "left",
    buttons: 0,
    clickCount: 1,
    modifiers,
  });
  await nextPaint(send);
}
async function drag(send, from, to) {
  // The component cases end every input gesture on a settled paint, so the
  // shared drag runs bare and the frame wait stays with the caller.
  await cdpDrag(send, from, to);
  await nextPaint(send);
}
async function wheel(send, x, y, deltaY) {
  await send("Input.dispatchMouseEvent", { type: "mouseWheel", x, y, deltaX: 0, deltaY, buttons: 0 });
  await nextPaint(send);
}
async function key(send, type, k, code, vk, modifiers = 0) {
  await send("Input.dispatchKeyEvent", {
    type,
    key: k,
    code,
    windowsVirtualKeyCode: vk,
    nativeVirtualKeyCode: vk,
    modifiers,
  });
}
async function pressKey(send, k, code, vk, modifiers = 0) {
  await key(send, "rawKeyDown", k, code, vk, modifiers);
  await key(send, "keyUp", k, code, vk, modifiers);
}
async function emulatedTouchTap(send, point) {
  await send("Emulation.setTouchEmulationEnabled", { enabled: true, maxTouchPoints: 5 });
  try {
    await send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x: point.x, y: point.y }] });
    await send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
    await nextPaint(send);
  } finally {
    await send("Emulation.setTouchEmulationEnabled", { enabled: false });
  }
}

// Touch gestures for a case whose viewport already carries touch emulation
// (applyViewport's touch flag), so the (pointer: coarse) rules stay live for
// the whole case instead of toggling per gesture.
async function touchTap(send, point) {
  await send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x: point.x, y: point.y }] });
  await send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  await nextPaint(send);
}

async function touchSwipe(send, from, to) {
  await send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x: from.x, y: from.y }] });
  for (let step = 1; step <= 10; step += 1) {
    const x = from.x + ((to.x - from.x) * step) / 10;
    const y = from.y + ((to.y - from.y) * step) / 10;
    await send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ x, y }] });
  }
  await send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  await nextPaint(send);
}

async function noteStackMetrics(send, id) {
  return evaluate(
    send,
    `(() => {
      const stack = document.querySelector('[data-case="${id}"] [data-annotation-note]')?.parentElement;
      if (!stack) return null;
      return { scrollTop: stack.scrollTop, scrollHeight: stack.scrollHeight, clientHeight: stack.clientHeight };
    })()`,
  );
}

async function noteStackTail(send, id) {
  return evaluate(
    send,
    `(() => {
      const stack = document.querySelector('[data-case="${id}"] [data-annotation-note]')?.parentElement;
      if (!stack) return null;
      const notes = stack.querySelectorAll('[data-annotation-note]');
      const last = notes[notes.length - 1];
      if (!last) return null;
      const noteRect = last.getBoundingClientRect();
      const stackRect = stack.getBoundingClientRect();
      return {
        visible: noteRect.bottom <= stackRect.bottom + 1,
        noteBottom: noteRect.bottom,
        stackBottom: stackRect.bottom,
      };
    })()`,
  );
}

// A point over a note body VISIBLE in the stack's current clip. The first
// note scrolls up as the swipes advance, so a fixed first-note point would
// leave the visible area; the pending reference re-enables its own pointer
// events even on the fine-pointer rules, so a swipe over it would pan the
// stack without proving anything about the note surface itself.
async function noteBodyPoint(send, id) {
  return evaluate(
    send,
    `(() => {
      const stack = document.querySelector('[data-case="${id}"] [data-annotation-note]')?.parentElement;
      if (!stack) return null;
      const clip = stack.getBoundingClientRect();
      for (const note of stack.querySelectorAll('[data-annotation-note]')) {
        const rect = note.getBoundingClientRect();
        const top = Math.max(rect.top, clip.top);
        const bottom = Math.min(rect.bottom, clip.bottom);
        if (bottom - top > 24) return { x: rect.left + rect.width / 2, y: top + (bottom - top) / 2 };
      }
      return null;
    })()`,
  );
}

// --- independent geometry + paint oracle ------------------------------------

async function describeLiteral(send, id, literal, range) {
  return evaluate(
    send,
    `window.__annotationGuard.describeLiteral(${q(id)}, ${q(literal)}, ${range.start}, ${range.end})`,
  );
}

// Capture the case and run the implementation-neutral paint analysis: painted
// EXTENT along a strip above the glyphs and visible-GLYPH ink fraction per
// browser text-range fragment, against an unannotated control paragraph. It
// never reads the CSS highlight registry or the overlay DOM, so a DOM band and
// a future native paint are judged identically.
async function captureCase(send, id, literal, range, controlLiteral, controlRange) {
  const described = await describeLiteral(send, id, literal, range);
  const control = await describeLiteral(send, id, controlLiteral, controlRange);
  const caseRect = await evaluate(send, `window.__annotationGuard.caseRect(${q(id)})`);
  const sample = await send("Page.captureScreenshot", {
    format: "png",
    clip: { x: caseRect.left, y: caseRect.top, width: caseRect.width, height: caseRect.height, scale: 1 },
    captureBeyondViewport: false,
  });
  const base64 = sample.result.data;
  const relative = (rect) => ({
    left: rect.left - caseRect.left,
    top: rect.top - caseRect.top,
    right: rect.right - caseRect.left,
    bottom: rect.bottom - caseRect.top,
  });
  const analysis =
    described && control && described.clipped.length > 0 && control.clipped.length > 0
      ? await evaluate(
          send,
          `window.__annotationGuard.analyzePaint(${q(base64)}, ${Math.round(caseRect.width)}, ${Math.round(caseRect.height)}, ` +
            `${JSON.stringify(described.clipped.map(relative))}, ${JSON.stringify(relative(control.clipped[0]))}, ${PAINT_TOLERANCE})`,
        )
      : null;
  return { described, control, caseRect, base64, analysis };
}

// A fragment's paint is within tolerance only when BOTH inclusive raster edges
// align with the independently described browser Range within one pixel.
function coverageOf(analysis) {
  return analysis.fragments.map((fragment) => ({
    covered: fragment.covered,
    paintedFraction: fragment.paintedFraction,
    extentLeft: fragment.extentLeft,
    extentRight: fragment.extentRight,
    // Browser Range geometry is right-exclusive; paint columns are inclusive.
    expectedExtentLeft: fragment.expectedExtentLeft,
    expectedExtentRight: fragment.expectedExtentRight,
  }));
}
function legibilityOf(analysis) {
  const threshold = Math.max(INK_FLOOR, INK_RELATIVE * analysis.controlInk);
  const painted = analysis.fragments.filter((fragment) => fragment.covered);
  return {
    threshold,
    controlInk: analysis.controlInk,
    fragments: painted.map((fragment) => ({ inkFraction: fragment.inkFraction })),
    legible: painted.every((fragment) => fragment.inkFraction >= threshold),
  };
}

// --- input evidence helpers -------------------------------------------------

async function selectionState(send, literal) {
  return evaluate(
    send,
    `(() => {
      const s = window.getSelection();
      if (!s || s.rangeCount === 0) return { text: "", rangeCount: s ? s.rangeCount : 0 };
      const lit = (n) => n && n.nodeType === Node.TEXT_NODE && n.data === ${q(literal)};
      return {
        text: s.toString(),
        rangeCount: s.rangeCount,
        anchorLiteral: lit(s.anchorNode),
        focusLiteral: lit(s.focusNode),
        anchorOffset: s.anchorOffset,
        focusOffset: s.focusOffset,
      };
    })()`,
  );
}

// A genuine CDP drag over the independently described fragment rectangles.
function dragPoints(described) {
  const first = described.clipped[0];
  const last = described.clipped[described.clipped.length - 1];
  return {
    start: { x: first.left + 1, y: first.top + (first.bottom - first.top) / 2 },
    end: { x: last.right - 1, y: last.top + (last.bottom - last.top) / 2 },
  };
}

// Collapse any live selection with a real click in an unannotated paragraph, so
// the next drag starts a NEW selection instead of dragging the existing one.
async function clearSelection(send, id, paragraph) {
  const described = await describeLiteral(send, id, paragraph, { start: 0, end: Math.min(4, paragraph.length) });
  if (!described || described.clipped.length === 0) return;
  const rect = described.clipped[0];
  await mouseClick(send, rect.left + 1, rect.top + (rect.bottom - rect.top) / 2);
}

// --- cases ------------------------------------------------------------------

async function casePaneResize(send, record) {
  const id = "pane-resize";
  const findings = [];
  const args = [id, PANE_FIRST, PANE_RANGE, PANE_SECOND, PANE_CONTROL_RANGE];

  const wide = await captureCase(send, ...args);
  check(
    wide.described && wide.described.nodeData === PANE_FIRST && wide.described.text === PANE_SELECTED,
    "pane-resize: the wide passage is the literal fixture text",
    wide.described ? `node ${q(wide.described.nodeData)} selecting ${q(wide.described.text)}` : "missing",
  );
  check(wide.analysis, "pane-resize: the wide paint analysis ran", "no analysis");
  record.evidence.push(saveScreenshot("pane-resize-wide.png", wide.base64));
  record.evidence.push(
    saveEvidence("pane-resize-wide.json", {
      fragments: wide.described.clipped,
      coverage: coverageOf(wide.analysis),
      legibility: legibilityOf(wide.analysis),
    }),
  );
  // Baseline: at the wide width the band must cover the fragment within one
  // pixel (the oracle detects real paint before the reflow).
  for (const [index, fragment] of coverageOf(wide.analysis).entries()) {
    require_(
      findings,
      fragment.covered,
      `wide-coverage-${index}`,
      "the saved highlight's painted extent matches the browser range within 1px before the width change",
      `extent ${fragment.extentLeft}..${fragment.extentRight} vs expected inclusive paint columns ${fragment.expectedExtentLeft}..${fragment.expectedExtentRight}`,
    );
  }
  // Legibility: the selected glyphs must remain visible under the highlight.
  const wideLegibility = legibilityOf(wide.analysis);
  require_(
    findings,
    wideLegibility.legible,
    "wide-legibility",
    "the saved highlight leaves the selected glyphs visible (an unannotated control shows ink)",
    `annotated ink ${wideLegibility.fragments.map((f) => f.inkFraction.toFixed(4)).join(",")} vs control ${wideLegibility.controlInk.toFixed(4)} threshold ${wideLegibility.threshold.toFixed(4)}`,
  );

  // Pane-only width change: set the case section's width. This is a real reflow
  // with NO browser viewport resize, so the product's window-resize listener
  // never fires.
  await evaluate(send, `(() => { const el = document.querySelector('[data-case="${id}"]'); el.style.width = '230px'; return el.getBoundingClientRect().width; })()`);
  await nextPaint(send);

  const narrow = await captureCase(send, ...args);
  check(narrow.analysis, "pane-resize: the narrow paint analysis ran", "no analysis");
  record.evidence.push(saveScreenshot("pane-resize-narrow.png", narrow.base64));
  record.evidence.push(
    saveEvidence("pane-resize-narrow.json", {
      fragments: narrow.described.clipped,
      coverage: coverageOf(narrow.analysis),
      legibility: legibilityOf(narrow.analysis),
    }),
  );
  check(
    narrow.described.nodeData === PANE_FIRST,
    "pane-resize: the authored paragraph text is unchanged after the width change",
    `rendered ${q(narrow.described.nodeData)}`,
  );
  require_(
    findings,
    narrow.described.clipped.length > wide.described.clipped.length,
    "reflow",
    "the narrower pane really wraps the passage onto more lines",
    `fragments before ${wide.described.clipped.length}, after ${narrow.described.clipped.length}`,
  );
  // THE RESIZE DEFECT: the painted saved highlight stays at the stale geometry.
  for (const [index, fragment] of coverageOf(narrow.analysis).entries()) {
    require_(
      findings,
      fragment.covered,
      `reflowed-coverage-${index}`,
      "the saved highlight's painted extent still matches the reflowed browser range within 1px",
      `extent ${fragment.extentLeft}..${fragment.extentRight} vs expected inclusive paint columns ${fragment.expectedExtentLeft}..${fragment.expectedExtentRight}`,
    );
  }
  throwFindings(findings);
}

async function caseNestedScroll(send, record) {
  const id = "nested-scroll";
  const findings = [];
  const described = await describeLiteral(send, id, NESTED_FIRST, NESTED_RANGE);
  check(
    described && described.nodeData === NESTED_FIRST && described.text === NESTED_TARGET,
    `${NESTED_FIRST} rendered`,
    described ? `${q(described.nodeData)} selecting ${q(described.text)}` : "missing",
  );
  const points = dragPoints(described);
  // The reading position immediately before the reveal click, so the click's
  // own movement is bounded without any helper repairing it.
  const revealPositionBefore = await evaluate(
    send,
    `(() => { const o = document.querySelector('[data-case="${id}"] [data-pane-scaffold]'); return o ? o.scrollTop : null; })()`,
  );
  await mouseClick(send, points.start.x, points.start.y);

  const revealed = await settle(
    send,
    `(() => { const n = document.querySelectorAll('[data-case="${id}"] [data-annotation-note]'); return n.length >= 3 ? n.length : 0; })()`,
    { label: "three overlapping notes revealed" },
  );
  check(revealed.value >= 3, "nested-scroll: hovering the touched passage reveals every overlapping note", `revealed ${revealed.value}`);
  const revealPositionAfter = await evaluate(
    send,
    `(() => { const o = document.querySelector('[data-case="${id}"] [data-pane-scaffold]'); return o ? o.scrollTop : null; })()`,
  );
  require_(
    findings,
    revealPositionBefore !== null && revealPositionAfter === revealPositionBefore,
    "reveal-position",
    "the reveal click does not move the reading position",
    `scrollTop ${revealPositionAfter} vs ${revealPositionBefore}`,
  );

  const setup = await evaluate(
    send,
    `(() => {
      const stack = document.querySelector('[data-case="${id}"] [data-annotation-reference]')?.parentElement;
      const owner = document.querySelector('[data-case="${id}"] [data-pane-scaffold]');
      if (!stack || !owner) return null;
      return {
        noteTexts: [...stack.querySelectorAll('[data-annotation-note]')].map((n) => n.textContent),
        referenceTexts: [...stack.querySelectorAll('[data-annotation-reference]')].map((n) => n.textContent),
        stackScrollHeight: stack.scrollHeight,
        stackClientHeight: stack.clientHeight,
        referenceScrollHeight: stack.querySelector('[data-annotation-reference]').scrollHeight,
        referenceClientHeight: stack.querySelector('[data-annotation-reference]').clientHeight,
      };
    })()`,
  );
  check(setup, "nested-scroll: the revealed stack and pending references are present", "missing stack");
  check(
    setup.noteTexts.length === 3 && setup.noteTexts.every((text, index) => text === NESTED_NOTES[index]),
    "nested-scroll: the revealed notes are the full literal fixture notes",
    `${q(setup.noteTexts)}`,
  );
  // Every pending reference is compared with ITS OWN range, selected text, note
  // and independently computed locator - never the first one reused.
  for (const expected of NESTED_REFERENCES) {
    const matching = setup.referenceTexts.filter((text) => text.includes(expected.locator));
    require_(
      findings,
      matching.length === 1,
      `reference-${expected.index}`,
      `exactly one pending reference carries the independently computed locator ${q(expected.locator)}`,
      `references ${q(setup.referenceTexts.map((text) => (text.match(/NOTES\.md:1:[^`]+/) ?? ["<none>"])[0]))}`,
    );
    if (matching.length === 1) {
      require_(
        findings,
        matching[0].includes(JSON.stringify(expected.note)) && matching[0].includes(expected.selected),
        `reference-${expected.index}-body`,
        `the reference for ${q(expected.locator)} carries its full literal note and selected text`,
        `${q(matching[0].slice(0, 100))}...`,
      );
    }
  }
  check(setup.stackScrollHeight > setup.stackClientHeight + 4, "nested-scroll: the outer note stack is itself scrollable", `scrollHeight ${setup.stackScrollHeight}, clientHeight ${setup.stackClientHeight}`);
  check(setup.referenceScrollHeight > setup.referenceClientHeight + 4, "nested-scroll: the pending reference is itself scrollable", `scrollHeight ${setup.referenceScrollHeight}, clientHeight ${setup.referenceClientHeight}`);
  record.evidence.push(saveEvidence("nested-scroll-setup.json", setup));

  // Reachability: genuine wheel input scrolls the outer stack to its
  // independently computed maximum, then the LAST reference must be visible
  // within the stack's actual client clip. The stack starts at its fresh
  // reveal position (0); its scrollTop is never set to manufacture this.
  const reachStart = await evaluate(
    send,
    `(() => { const stack = document.querySelector('[data-case="${id}"] [data-annotation-reference]')?.parentElement; return stack ? { scrollTop: stack.scrollTop, maxScroll: stack.scrollHeight - stack.clientHeight } : null; })()`,
  );
  check(reachStart, "nested-scroll: the stack is present for the reachability wheel", "missing stack");
  const reachAttempts = Math.ceil(reachStart.maxScroll / 60) + 6;
  let reachScroll = reachStart.scrollTop;
  for (let attempt = 0; attempt < reachAttempts; attempt += 1) {
    const point = await evaluate(send, `window.__annotationGuard.visibleNotePoint(${q(id)})`);
    if (!point) break;
    const delta = Math.max(60, Math.min(240, reachStart.maxScroll - reachScroll));
    await wheel(send, point.x, point.y, delta);
    const next = await evaluate(
      send,
      `(() => { const stack = document.querySelector('[data-case="${id}"] [data-annotation-reference]')?.parentElement; return stack ? stack.scrollTop : 0; })()`,
    );
    if (next >= reachStart.maxScroll - 1 || next === reachScroll) {
      reachScroll = next;
      break;
    }
    reachScroll = next;
  }
  const reach = await evaluate(
    send,
    `(() => {
      const stack = document.querySelector('[data-case="${id}"] [data-annotation-reference]')?.parentElement;
      const refs = [...stack.querySelectorAll('[data-annotation-reference]')];
      const last = refs[refs.length - 1];
      const sr = stack.getBoundingClientRect();
      const lr = last.getBoundingClientRect();
      return {
        scrollTop: stack.scrollTop,
        maxScroll: stack.scrollHeight - stack.clientHeight,
        lastVisible: lr.top >= sr.top - 1 && lr.bottom <= sr.bottom + 1,
        lastTop: lr.top, lastBottom: lr.bottom, stackTop: sr.top, stackBottom: sr.bottom,
      };
    })()`,
  );
  record.evidence.push(saveEvidence("nested-scroll-reach.json", { reachStart, reach }));
  require_(
    findings,
    reach.scrollTop >= reach.maxScroll - 1,
    "reach-max",
    "genuine wheel input reaches the outer stack's independently computed maximum",
    `scrollTop ${reach.scrollTop} of maxScroll ${reach.maxScroll}`,
  );
  require_(
    findings,
    reach.lastVisible,
    "reach-last",
    "the last pending reference is exposed within the stack's actual client clip",
    `last ref ${reach.lastTop.toFixed(1)}..${reach.lastBottom.toFixed(1)} vs clip ${reach.stackTop.toFixed(1)}..${reach.stackBottom.toFixed(1)}`,
  );

  // THE NESTED-WHEEL DEFECT: a wheel over the interactive nested reference must
  // scroll the reference, leave the outer stack and document unmoved, and not
  // cancel the wheel. Reset both scroll positions first (setup, not the act).
  await evaluate(
    send,
    `(() => {
      const stack = document.querySelector('[data-case="${id}"] [data-annotation-reference]')?.parentElement;
      const reference = stack.querySelector('[data-annotation-reference]');
      stack.scrollTop = 0;
      reference.scrollTop = 0;
      window.__guardWheels = [];
    })()`,
  );
  const referenceRegion = await evaluate(
    send,
    `(() => {
      const stack = document.querySelector('[data-case="${id}"] [data-annotation-reference]')?.parentElement;
      const reference = stack.querySelector('[data-annotation-reference]');
      const r = reference.getBoundingClientRect();
      const s = stack.getBoundingClientRect();
      return { left: Math.max(r.left, s.left), top: Math.max(r.top, s.top), right: Math.min(r.right, s.right), bottom: Math.min(r.bottom, s.bottom) };
    })()`,
  );
  check(referenceRegion && referenceRegion.bottom > referenceRegion.top + 4, "nested-scroll: a visible reference region exists", "no visible reference region");
  await wheel(send, (referenceRegion.left + referenceRegion.right) / 2, referenceRegion.top + 12, 48);
  await settle(
    send,
    `(() => {
      const stack = document.querySelector('[data-case="${id}"] [data-annotation-reference]')?.parentElement;
      const reference = stack.querySelector('[data-annotation-reference]');
      return reference.scrollTop > 0 || stack.scrollTop > 0;
    })()`,
    { label: "nested wheel takes effect somewhere" },
  );
  const observed = await evaluate(
    send,
    `(() => {
      const stack = document.querySelector('[data-case="${id}"] [data-annotation-reference]')?.parentElement;
      const reference = stack.querySelector('[data-annotation-reference]');
      const owner = document.querySelector('[data-case="${id}"] [data-pane-scaffold]');
      return { referenceScrollTop: reference.scrollTop, stackScrollTop: stack.scrollTop, ownerScrollTop: owner.scrollTop, wheels: window.__guardWheels.slice() };
    })()`,
  );
  record.evidence.push(saveEvidence("nested-scroll-wheel.json", observed));
  const prevented = observed.wheels.length > 0 ? observed.wheels[observed.wheels.length - 1].defaultPrevented : null;
  require_(
    findings,
    observed.referenceScrollTop > 0 && observed.stackScrollTop === 0 && observed.ownerScrollTop === 0 && prevented !== true,
    "nested-wheel",
    "a wheel over the nested reference scrolls it, leaves the outer stack and document unmoved, and is not cancelled",
    `reference ${observed.referenceScrollTop} (want >0), outer stack ${observed.stackScrollTop} (want 0), document ${observed.ownerScrollTop} (want 0), defaultPrevented ${prevented} (want false)`,
  );
  throwFindings(findings);
}

async function selectionCase(send, record) {
  const id = "selection";
  const second = nthParagraph(SELECTION_SOURCE, 1);
  const described = await describeLiteral(send, id, SELECTION_FIRST, SELECTION_RANGE);
  check(
    described && described.nodeData === SELECTION_FIRST && described.text === SELECTION_SELECTED,
    `selection: the passage is ${q(SELECTION_SELECTED)}`,
    described ? `node ${q(described.nodeData)} selecting ${q(described.text)}` : "missing",
  );
  const points = dragPoints(described);

  await clearSelection(send, id, second);
  await drag(send, points.start, points.end);
  const forward = await settle(
    send,
    `window.getSelection() && window.getSelection().toString() === ${q(SELECTION_SELECTED)}`,
    { label: "forward selection settles" },
  );
  const forwardState = await selectionState(send, SELECTION_FIRST);
  check(
    forwardState.text === SELECTION_SELECTED && forwardState.rangeCount === 1,
    `selection: the forward drag selects the literal ${q(SELECTION_SELECTED)}`,
    `text ${q(forwardState.text)}, ranges ${forwardState.rangeCount}`,
  );
  check(
    forwardState.anchorLiteral && forwardState.focusLiteral && forwardState.anchorOffset === SELECTION_RANGE.start,
    "selection: both endpoints sit in the literal paragraph text node, anchored at the drag start",
    `anchorLiteral ${forwardState.anchorLiteral}, focusLiteral ${forwardState.focusLiteral}, anchorOffset ${forwardState.anchorOffset} (want ${SELECTION_RANGE.start})`,
  );
  const bandCount = await evaluate(send, `document.querySelectorAll('[data-case="${id}"] [data-document-highlight]').length`);
  check(bandCount >= 1, "selection: the saved highlight band is still present under the drag", `bands ${bandCount}`);
  check(forward.settled, "selection: the forward selection completed", `text ${q(forwardState.text)}`);

  await clearSelection(send, id, second);
  await drag(send, points.end, points.start);
  const reverseState = await selectionState(send, SELECTION_FIRST);
  check(
    reverseState.text === SELECTION_SELECTED && reverseState.rangeCount === 1,
    `selection: the reverse drag also selects the literal ${q(SELECTION_SELECTED)}`,
    `text ${q(reverseState.text)}, ranges ${reverseState.rangeCount}`,
  );
  check(
    reverseState.anchorLiteral && reverseState.focusLiteral && reverseState.anchorOffset > 0,
    "selection: the reverse drag anchors at the far end",
    `anchorOffset ${reverseState.anchorOffset}`,
  );
  record.evidence.push(saveEvidence("selection.json", { forwardState, reverseState, bandCount, bandRange: SELECTION_BAND }));
}

async function selectionShiftCase(send, record) {
  const id = "selection";
  const second = nthParagraph(SELECTION_SOURCE, 1);
  const described = await describeLiteral(send, id, SELECTION_FIRST, SELECTION_SHIFT_RANGE);
  check(
    described && described.nodeData === SELECTION_FIRST && described.text === SELECTION_SHIFT_SELECTED,
    `selection-shift: the passage is ${q(SELECTION_SHIFT_SELECTED)}`,
    described ? `node ${q(described.nodeData)} selecting ${q(described.text)}` : "missing",
  );
  const points = dragPoints(described);
  await clearSelection(send, id, second);
  await drag(send, points.start, points.end);
  const before = await selectionState(send, SELECTION_FIRST);
  check(
    before.text === SELECTION_SHIFT_SELECTED,
    `selection-shift: the base drag selects ${q(SELECTION_SHIFT_SELECTED)}`,
    `text ${q(before.text)}`,
  );

  await pressKey(send, "ArrowRight", "ArrowRight", 39, 8);
  const extended = await selectionState(send, SELECTION_FIRST);
  check(
    extended.text === `${SELECTION_SHIFT_SELECTED} `,
    "selection-shift: Shift+ArrowRight extends the selection by the next literal character",
    `text ${q(extended.text)}`,
  );

  await pressKey(send, "ArrowLeft", "ArrowLeft", 37, 8);
  const restored = await selectionState(send, SELECTION_FIRST);
  check(
    restored.text === SELECTION_SHIFT_SELECTED,
    "selection-shift: Shift+ArrowLeft restores the base selection",
    `text ${q(restored.text)}`,
  );
  record.evidence.push(saveEvidence("selection-shift.json", { before, extended, restored }));
}

async function copyCase(send, record) {
  const id = "selection";
  const second = nthParagraph(SELECTION_SOURCE, 1);
  const described = await describeLiteral(send, id, SELECTION_FIRST, SELECTION_RANGE);
  check(
    described && described.nodeData === SELECTION_FIRST && described.text === SELECTION_SELECTED,
    "copy: the passage rendered",
    described ? `node ${q(described.nodeData)} selecting ${q(described.text)}` : "missing",
  );
  const points = dragPoints(described);
  await clearSelection(send, id, second);
  await drag(send, points.start, points.end);
  const selected = await settle(send, `window.getSelection() && window.getSelection().toString() === ${q(SELECTION_SELECTED)}`, {
    label: "copy selection settles",
  });
  check(selected.settled, "copy: the source selection is in place", "selection did not settle");

  const origin = await evaluate(send, "location.origin");
  await send("Browser.grantPermissions", { origin, permissions: ["clipboardReadWrite", "clipboardSanitizedWrite"] });
  await pressKey(send, "c", "KeyC", 67, 2);
  const clipboard = await evaluate(send, "navigator.clipboard.readText()");
  check(
    clipboard === SELECTION_SELECTED,
    `copy: the native Copy result is the literal ${q(SELECTION_SELECTED)}`,
    `clipboard ${q(clipboard)}`,
  );
  record.evidence.push(saveEvidence("copy.json", { clipboard }));
}

// The focus/reading-position state a reveal may (not) disturb, scoped to one
// case so focus elsewhere on the page can never satisfy it.
async function accessState(send, id) {
  return evaluate(
    send,
    `(() => {
      const a = document.activeElement;
      const scope = document.querySelector('[data-case="${id}"]');
      const owner = document.querySelector('[data-case="${id}"] [data-pane-scaffold]');
      const inCase = !!(a && a.closest && a.closest('[data-case="${id}"]'));
      const band = a && a.closest ? a.closest('[data-document-highlight]') : null;
      const activeKey = (() => {
        if (!a) return null;
        if (a === document.body) return 'BODY';
        if (a === document.documentElement) return 'HTML';
        if (a === owner) return 'PANE_SCAFFOLD';
        if (band) return 'HIGHLIGHT:' + (band.getAttribute('data-operation-id') ?? '<missing>');
        if (scope && scope.contains(a)) {
          const parts = [];
          let node = a;
          while (node && node !== scope) {
            const parent = node.parentElement;
            if (!parent) break;
            const sameTag = [...parent.children].filter((child) => child.tagName === node.tagName);
            parts.unshift(node.tagName + ':' + sameTag.indexOf(node));
            node = parent;
          }
          return 'CASE/' + parts.join('/');
        }
        return 'OUTSIDE:' + a.tagName;
      })();
      return {
        activeTag: a ? a.tagName : null,
        activeKey,
        activeIsEditor: !!(a && a.tagName === 'TEXTAREA'),
        activeInCase: inCase,
        activeInBand: band !== null,
        noteCount: document.querySelectorAll('[data-case="${id}"] [data-annotation-note]').length,
        scrollTop: owner ? owner.scrollTop : null,
        scrollable: owner ? owner.scrollHeight > owner.clientHeight + 4 : false,
      };
    })()`,
  );
}

async function overlapAccessCase(send, record, ctx) {
  const id = "overlap-access";
  const findings = [];
  const evidence = {};

  for (const kind of ["hover", "keyboard", "click", "touch"]) {
    let neutralBaseline = null;
    if (kind === "click" || kind === "touch") {
      // Production's annotation pointer handlers only record the hit and update
      // reveal state; they do not focus or cancel default behavior. Establish
      // the browser's ordinary focus transition with an equivalent genuine input
      // on unannotated prose in this same production pane, from a fresh load: a
      // real click for click, an emulated tap for touch. The annotation
      // transition must start and end on the same actual active element.
      await ctx.reload();
      await settle(send, `document.querySelectorAll('[data-case="${id}"] [data-document-highlight]').length > 0`, {
        label: `${kind} baseline: overlap bands painted`,
      });
      const neutral = await describeLiteral(send, id, OVERLAP_FIRST, OVERLAP_NEUTRAL_RANGE);
      require_(
        findings,
        !!neutral && neutral.clipped.length > 0,
        `${kind}-baseline-surface`,
        `the equivalent neutral ${kind} surface is rendered from the fixture range`,
        neutral ? `fragments ${neutral.clipped.length}` : "missing",
      );
      if (neutral && neutral.clipped.length > 0) {
        const neutralPoints = dragPoints(neutral);
        const neutralCenter = {
          x: (neutralPoints.start.x + neutralPoints.end.x) / 2,
          y: neutralPoints.start.y,
        };
        const baselineBefore = await accessState(send, id);
        if (kind === "touch") {
          await emulatedTouchTap(send, neutralCenter);
        } else {
          await mouseClick(send, neutralCenter.x, neutralCenter.y);
        }
        const baselineAfter = await accessState(send, id);
        require_(
          findings,
          baselineAfter.noteCount === 0,
          `${kind}-baseline-neutral`,
          `an equivalent neutral ${kind} on unannotated prose reveals no annotation`,
          `notes ${baselineAfter.noteCount}`,
        );
        require_(
          findings,
          baselineAfter.scrollTop === baselineBefore.scrollTop,
          `${kind}-baseline-position`,
          `the neutral ${kind} does not move the reading position`,
          `scrollTop ${baselineAfter.scrollTop} vs ${baselineBefore.scrollTop}`,
        );
        neutralBaseline = { before: baselineBefore, after: baselineAfter };
      }
    }

    // Fresh, genuinely unrevealed state for every transition path.
    await ctx.reload();
    await settle(send, `document.querySelectorAll('[data-case="${id}"] [data-document-highlight]').length > 0`, {
      label: `${kind}: overlap bands painted`,
    });
    const described = await describeLiteral(send, id, OVERLAP_FIRST, OVERLAP_RANGE);
    require_(findings, !!described, `${kind}-passage`, "the overlap passage rendered", described ? "ok" : "missing");
    if (!described) continue;
    const points = dragPoints(described);
    const center = { x: (points.start.x + points.end.x) / 2, y: points.start.y };
    const before = await accessState(send, id);
    require_(
      findings,
      before.noteCount === 0,
      `${kind}-unrevealed`,
      "the fresh state shows no revealed note before this transition",
      `notes ${before.noteCount}`,
    );
    require_(
      findings,
      before.scrollable,
      `${kind}-baseline`,
      "the reading body is scrollable so a reading-position jump would be detectable",
      `scrollable ${before.scrollable}`,
    );

    if (kind === "hover") {
      await mouseMove(send, center.x, center.y);
    } else if (kind === "keyboard") {
      // Real Tab, scoped to this case's own saved-note band button.
      let focused = null;
      for (let step = 0; step < 120; step += 1) {
        await pressKey(send, "Tab", "Tab", 9);
        focused = await accessState(send, id);
        if (focused.activeInCase && focused.activeInBand) break;
      }
      require_(
        findings,
        !!(focused && focused.activeInCase && focused.activeInBand),
        "keyboard-focus",
        "real Tab reaches a saved-note band button inside this case",
        `focus ${q(focused && { tag: focused.activeTag, inCase: focused.activeInCase, inBand: focused.activeInBand })}`,
      );
    } else if (kind === "click") {
      // A genuine click at the band's coordinates: bands are pointer-events:none,
      // so the click lands on the prose beneath and reveal is resolved from the
      // pointer's coordinates.
      await mouseClick(send, center.x, center.y);
    } else {
      await emulatedTouchTap(send, center);
    }

    const revealed = await settle(
      send,
      `(() => { const n = [...document.querySelectorAll('[data-case="${id}"] [data-annotation-note]')]; return n.length >= 2 ? n.map((e) => e.textContent) : 0; })()`,
      { label: `${kind} reveals every overlapping note` },
    );
    const after = await accessState(send, id);
    require_(
      findings,
      revealed.settled && [...revealed.value].sort().join("|") === [...OVERLAP_NOTES].sort().join("|"),
      `${kind}-reveal`,
      "this transition reveals every overlapping literal note",
      `${q(revealed.value)}`,
    );
    require_(
      findings,
      !after.activeIsEditor,
      `${kind}-no-focus-theft`,
      "the reveal does not focus the comment editor",
      `active ${after.activeTag}`,
    );
    if (kind === "hover") {
      require_(
        findings,
        after.activeKey === before.activeKey,
        "hover-focus-preserved",
        "hover leaves the actual active element unchanged",
        `active ${after.activeKey} vs before ${before.activeKey}`,
      );
    } else if (kind === "keyboard") {
      require_(
        findings,
        after.activeInCase && after.activeInBand,
        "keyboard-focus-preserved",
        "keyboard reveal retains intentional focus on this fixture's saved-note band",
        `active ${after.activeKey}`,
      );
    } else {
      require_(
        findings,
        !!neutralBaseline && before.activeKey === neutralBaseline.before.activeKey,
        `${kind}-focus-start`,
        `the annotation ${kind} starts from the same active element as the neutral ${kind}`,
        `annotation ${before.activeKey}, neutral ${neutralBaseline?.before.activeKey ?? "missing"}`,
      );
      require_(
        findings,
        !!neutralBaseline && after.activeKey === neutralBaseline.after.activeKey,
        `${kind}-focus-preserved`,
        `the annotation ${kind} leaves the same actual active element as the equivalent neutral ${kind}`,
        `annotation ${after.activeKey}, neutral ${neutralBaseline?.after.activeKey ?? "missing"}`,
      );
    }
    require_(
      findings,
      after.scrollTop === before.scrollTop,
      `${kind}-no-position-jump`,
      "the reveal does not jump the reading position",
      `scrollTop ${after.scrollTop} vs ${before.scrollTop}`,
    );
    evidence[kind] = {
      before,
      after,
      baseline: neutralBaseline,
      revealed: revealed.value,
      touch: kind === "touch" ? "emulated tap; not physical-phone behavior" : undefined,
    };
  }

  record.evidence.push(saveEvidence("overlap-access.json", evidence));
  throwFindings(findings);
}

async function commentControlsCase(send, record) {
  const id = "comment-controls";
  const findings = [];
  const literal = COMMENT_SOURCE.split("\n\n")[0] ?? COMMENT_SOURCE;

  // Reveal the long SAVED note with genuine hover (not the editor's draft).
  const described = await describeLiteral(send, id, literal, COMMENT_SAVED_RANGE);
  check(described && described.nodeData === literal, "comment-controls: the saved-note passage rendered", described ? q(described.nodeData) : "missing");
  const points = dragPoints(described);
  await mouseMove(send, (points.start.x + points.end.x) / 2, points.start.y);
  const revealed = await settle(
    send,
    `document.querySelectorAll('[data-case="${id}"] [data-annotation-note]').length >= 1`,
    { label: "the long saved note revealed" },
  );
  require_(findings, revealed.settled, "note-revealed", "hovering the passage reveals the long saved note", "no note revealed");

  const state = await evaluate(
    send,
    `(() => {
      const scope = document.querySelector('[data-case="${id}"]');
      const pane = scope.querySelector('[data-pane-scaffold]')?.parentElement ?? scope;
      const footer = scope.querySelector('[data-testid="pane-footer"]');
      const editor = scope.querySelector('textarea');
      const passage = scope.querySelector('[data-annotation-passage]');
      const send = [...scope.querySelectorAll('button')].find((b) => b.textContent.trim() === 'Send');
      const note = scope.querySelector('[data-annotation-note]');
      const stack = note ? note.parentElement : null;
      const rect = (el) => { const r = el.getBoundingClientRect(); return { left: r.left, top: r.top, right: r.right, bottom: r.bottom }; };
      const clientClip = (el) => {
        const r = el.getBoundingClientRect();
        const left = r.left + el.clientLeft;
        const top = r.top + el.clientTop;
        return { left, top, right: left + el.clientWidth, bottom: top + el.clientHeight };
      };
      return {
        viewport: { left: 0, top: 0, right: window.innerWidth, bottom: window.innerHeight },
        footer: footer ? rect(footer) : null,
        pane: clientClip(pane),
        editor: editor ? { ...rect(editor), value: editor.value } : null,
        send: send ? rect(send) : null,
        passageText: passage ? passage.textContent : null,
        noteText: note ? note.textContent : null,
        noteWraps: note ? note.scrollWidth <= note.clientWidth + 1 : false,
        noteRect: note ? rect(note) : null,
        stackRect: stack ? rect(stack) : null,
      };
    })()`,
  );
  record.evidence.push(saveEvidence("comment-controls.json", state));

  // The guard's own node-tested containment oracle; the local null-guard keeps
  // a state row that legitimately measured nothing from throwing.
  const contains = (outer, inner, slack = 1) => !!outer && !!inner && rectInside(outer, inner, slack);
  require_(findings, !!state.footer, "footer", "the comment footer is present", "missing footer");
  require_(findings, contains(state.footer, state.editor), "editor-in-footer", "the note editor is inside the actual footer clip", `editor ${q(state.editor)} footer ${q(state.footer)}`);
  require_(findings, contains(state.footer, state.send), "send-in-footer", "Send is inside the actual footer clip", `send ${q(state.send)} footer ${q(state.footer)}`);
  require_(findings, contains(state.pane, state.editor), "editor-in-pane", "the note editor is inside the pane's actual client clip", `editor ${q(state.editor)} pane ${q(state.pane)}`);
  require_(findings, contains(state.pane, state.send), "send-in-pane", "Send is inside the pane's actual client clip", `send ${q(state.send)} pane ${q(state.pane)}`);
  require_(findings, contains(state.viewport, state.editor), "editor-in-viewport", "the note editor is visible within the viewport clip", `editor ${q(state.editor)} viewport ${q(state.viewport)}`);
  require_(findings, contains(state.viewport, state.send), "send-in-viewport", "Send is visible within the viewport clip", `send ${q(state.send)} viewport ${q(state.viewport)}`);
  require_(findings, !!state.editor && state.editor.value === COMMENT_NOTE, "editor-literal", "the editor preserves the full literal note", q(state.editor && state.editor.value));
  require_(findings, state.passageText === COMMENT_PASSAGE, "passage-literal", "the captured passage is the full literal text", q(state.passageText));
  require_(findings, state.noteText === COMMENT_SAVED_NOTE, "note-literal", "the revealed saved note is the full literal note", q(state.noteText));
  require_(findings, state.noteWraps, "note-wraps", "the long saved note wraps instead of overflowing horizontally", "note overflows horizontally");
  require_(
    findings,
    !!state.stackRect && !!state.noteRect && state.noteRect.top >= state.stackRect.top - 1 && state.noteRect.bottom <= state.stackRect.bottom + 1,
    "note-reachable",
    "the long saved note is exposed within the stack's actual client clip",
    `note ${state.noteRect && `${state.noteRect.top.toFixed(1)}..${state.noteRect.bottom.toFixed(1)}`} clip ${state.stackRect && `${state.stackRect.top.toFixed(1)}..${state.stackRect.bottom.toFixed(1)}`}`,
  );
  require_(findings, state.noteWraps && state.noteText === COMMENT_SAVED_NOTE, "note-content", "wrapping is proven on real note content", "note content/wrap mismatch");
  throwFindings(findings);
}

// A coarse-pointer (touch) surface case: the revealed note stack must accept
// the gesture itself, because a phone has no wheel for the component's
// forwarded scroll. A genuine emulated swipe pans the stack - never the
// underlying document - until the overflowing applied note's final lines are
// exposed inside the stack's own clip.
async function caseTouchScroll(send, record, ctx) {
  // Reuses the nested-scroll section's page: describeLiteral resolves a
  // literal by the FIRST text node that contains it document-wide, so a
  // duplicate fixture section could never describe its own passage. Every
  // case reloads and drives its own fresh state, so sharing the section is
  // honest; the nested-scroll wheel case has already finished by now.
  const id = "nested-scroll";
  const findings = [];
  // Touch emulation is what makes (pointer: coarse) match (see applyViewport),
  // so this is the phone's note surface, not the fine-pointer one.
  await applyViewport(send, { ...VIEWPORT, touch: true });
  try {
    await ctx.reload();
    await settle(send, `document.querySelectorAll('[data-case="${id}"] [data-document-highlight]').length > 0`, {
      label: "touch-scroll: bands painted",
    });
    const described = await describeLiteral(send, id, NESTED_FIRST, NESTED_RANGE);
    check(
      described && described.nodeData === NESTED_FIRST,
      "touch-scroll: the fixture passage rendered",
      described ? "ok" : "missing",
    );
    const points = dragPoints(described);
    const center = { x: (points.start.x + points.end.x) / 2, y: points.start.y };
    const readingPositionBefore = await evaluate(
      send,
      `(() => { const o = document.querySelector('[data-case="${id}"] [data-pane-scaffold]'); return o ? o.scrollTop : null; })()`,
    );
    await touchTap(send, center);
    const revealed = await settle(
      send,
      `(() => { const n = document.querySelectorAll('[data-case="${id}"] [data-annotation-note]'); return n.length >= 3 ? n.length : 0; })()`,
      { label: "touch-scroll: overlapping notes revealed" },
    );
    check(
      revealed.value >= 3,
      "touch-scroll: an emulated tap reveals every overlapping note",
      `revealed ${revealed.value}`,
    );
    const stack = await noteStackMetrics(send, id);
    check(stack, "touch-scroll: the revealed note stack is present", "missing stack");
    require_(
      findings,
      stack.scrollHeight > stack.clientHeight + 4,
      "overflow",
      "the revealed note stack overflows its own scroll clip",
      `scrollHeight ${stack.scrollHeight}, clientHeight ${stack.clientHeight}`,
    );
    const tail = await noteStackTail(send, id);
    require_(
      findings,
      !!tail && !tail.visible,
      "tail-clipped",
      "the overflowing note's final lines start clipped below the stack's clip",
      tail ? `note bottom ${tail.noteBottom}, stack clip ${tail.stackBottom}` : "missing",
    );
    // Bounded swipes toward the stack's independently computed maximum, the
    // touch counterpart of the nested-scroll wheel's reachability pass.
    const attempts = Math.ceil((stack.scrollHeight - stack.clientHeight) / 120) + 6;
    let progress = 0;
    for (let attempt = 0; attempt < attempts; attempt += 1) {
      const point = await noteBodyPoint(send, id);
      if (!point) break;
      await touchSwipe(send, { x: point.x, y: point.y }, { x: point.x, y: point.y - 160 });
      const next = await noteStackMetrics(send, id);
      if (!next || next.scrollTop >= next.scrollHeight - next.clientHeight - 1 || next.scrollTop === progress) {
        progress = next ? next.scrollTop : progress;
        break;
      }
      progress = next.scrollTop;
    }
    const panned = await noteStackMetrics(send, id);
    require_(
      findings,
      !!panned && panned.scrollTop > 0,
      "swipe-pans-stack",
      "the emulated swipe pans the note stack instead of the document",
      `scrollTop ${panned ? panned.scrollTop : "missing"}`,
    );
    const tailAfter = await noteStackTail(send, id);
    require_(
      findings,
      !!tailAfter && tailAfter.visible,
      "tail-reachable",
      "the swiped note stack exposes the applied note's final lines",
      tailAfter ? `note bottom ${tailAfter.noteBottom}, stack clip ${tailAfter.stackBottom}` : "missing",
    );
    const readingPositionAfter = await evaluate(
      send,
      `(() => { const o = document.querySelector('[data-case="${id}"] [data-pane-scaffold]'); return o ? o.scrollTop : null; })()`,
    );
    require_(
      findings,
      readingPositionBefore !== null && readingPositionAfter === readingPositionBefore,
      "reading-position",
      "the swipe over the note never scrolls the underlying document",
      `scrollTop ${readingPositionAfter} vs ${readingPositionBefore}`,
    );
    record.evidence.push(saveEvidence("touch-scroll.json", { stack: panned, tail: tailAfter }));
    throwFindings(findings);
  } finally {
    // Restore the shared fine-pointer viewport for the remaining cases. The
    // clear is explicit: applyViewport only ENABLES touch when asked, so the
    // global re-apply alone would leave touch emulation on for later cases.
    await clearViewportOverride(send);
    await applyViewport(send, VIEWPORT);
  }
}

const CASES = [
  { name: "pane-resize", expects: "PASS", run: casePaneResize },
  { name: "nested-scroll", expects: "PASS", run: caseNestedScroll },
  { name: "touch-scroll", expects: "PASS", run: caseTouchScroll },
  { name: "selection", expects: "PASS", run: selectionCase },
  { name: "selection-shift", expects: "PASS", run: selectionShiftCase },
  { name: "copy", expects: "PASS", run: copyCase },
  { name: "overlap-access", expects: "PASS", run: overlapAccessCase },
  { name: "comment-controls", expects: "PASS", run: commentControlsCase },
];

async function runCase(send, ctx, spec) {
  const record = { name: spec.name, expects: spec.expects, ok: false, expected: "", actual: "", detail: "", findings: null, evidence: [] };
  try {
    await spec.run(send, record, ctx);
    record.ok = true;
  } catch (error) {
    if (error instanceof CaseFailure) {
      record.expected = error.expected;
      record.actual = error.actual;
      record.detail = error.message;
      record.findings = error.findings;
    } else {
      record.expected = "the case to run to completion";
      record.actual = "an unexpected harness error";
      record.detail = error instanceof Error ? error.message : String(error);
    }
  }
  return record;
}

async function main() {
  let guard;
  try {
    guard = await startBrowserGuard({ frontend: FRONTEND, profilePrefix: "documentannotationguard-chrome-" });
  } catch (error) {
    throw new Error(describeBrowserStartupFailure({ error, subsystem: "launch" }));
  }
  const { vitePort, cleanup } = guard;
  let page;
  try {
    const viteDeadline = createStartupDeadline();
    try {
      await waitForHttp(guardPageURL(vitePort), "vite dev server", guard.getViteLaunchError, {
        signal: viteDeadline.signal,
      });
    } catch (error) {
      throw new Error(describeBrowserStartupFailure({ error, subsystem: "vite", viteStderr: guard.getViteError() }));
    } finally {
      viteDeadline.clear();
    }
    const cdpEndpoint = await waitForBrowserReady(guard);
    page = await connectOnlyPage(cdpEndpoint);
    const { send } = page;
    try {
      await send("Page.enable");
      await applyViewport(send, VIEWPORT);
      const url = guardPageURL(vitePort);
      await navigateTo(page, url, BOOT);
      await waitForFonts(send);
      // Bands exist only after the committed maps and the overlay's first
      // measure; a bounded observable wait, not a frame count.
      await settle(send, "document.querySelectorAll('[data-document-highlight]').length > 0", {
        label: "the initial saved highlight bands painted",
        timeoutMs: 5000,
      });
      const ctx = {
        send,
        page,
        url,
        reload: async () => {
          await navigateTo(page, url, BOOT);
          await waitForFonts(send);
        },
      };

      const results = [];
      for (const spec of CASES) {
        // eslint-disable-next-line no-await-in-loop
        results.push(await runCase(send, ctx, spec));
      }

      mkdirSync(EVIDENCE, { recursive: true });
      writeFileSync(path.join(EVIDENCE, "components-results.json"), `${JSON.stringify(results, null, 2)}\n`);

      const red = results.filter((result) => !result.ok);
      for (const result of results) {
        const verdict = result.ok ? "PASS" : "FAIL";
        console.log(`${verdict} ${result.name} (expected ${result.expects})`);
        if (!result.ok && result.findings) {
          for (const finding of result.findings) {
            console.log(`    [${finding.name}] expected: ${finding.expected}`);
            console.log(`    [${finding.name}] actual:   ${finding.actual}`);
          }
        } else if (!result.ok) {
          console.log(`    expected: ${result.expected}`);
          console.log(`    actual:   ${result.actual}`);
          if (result.detail) console.log(`    detail:   ${result.detail}`);
        }
      }
      const unexpected = results.filter((result) => result.ok !== (result.expects === "PASS"));
      console.log(
        `\ndocumentannotationguard: ${results.length - red.length}/${results.length} cases passed; ` +
          `${red.length} failed (${red.map((result) => result.name).join(", ") || "none"}); ` +
          `${unexpected.length} unexpected against the expected verdicts. ` +
          `evidence: ${EVIDENCE}`,
      );
      process.exitCode = red.length > 0 ? 1 : 0;
    } finally {
      await clearViewportOverride(send);
      page.close();
    }
  } finally {
    await cleanup();
  }
}

main().catch((error) => {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
});
