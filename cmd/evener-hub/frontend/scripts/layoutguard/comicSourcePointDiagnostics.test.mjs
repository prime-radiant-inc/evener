import { randomUUID } from "node:crypto";
import { afterEach, expect, test, vi } from "vitest";
import { earlyAppearancePopup } from "./cases/comic-mobile-viewport/assert.mjs";

const key = "evener:comic:position:local:comic-geometry";
const expected = {
  sessionRef: "local:comic-geometry", path: [], mode: "away", entryId: "geometry-item",
  offset: 2871, viewportOffset: 85.9375,
};
const stage = { width: 320, blockedFonts: true };
const realms = [];

afterEach(() => {
  vi.restoreAllMocks();
  for (const frame of realms.splice(0)) frame.remove();
});

// Execute the guard's actual page expressions against DOM/Storage/Range. Only
// CDP input, geometry (absent in jsdom), and the polling clock are supplied here.
function fixture(glyphOffset = 71) {
  const frame = document.createElement("iframe");
  document.body.append(frame);
  realms.push(frame);
  const window = frame.contentWindow;
  const doc = window.document;
  doc.body.innerHTML = `
    <main data-comic-font="unavailable" data-session-ref="local:comic-geometry">
      <button>Appearance</button><div role="textbox"></div>
      <div data-comic-scroll><div><article data-comic-entry="geometry-item"
        data-comic-session="local:comic-geometry" data-comic-path="[]"
        data-comic-start="2800" data-comic-end="3000"><span data-comic-text></span>
      </article></div></div>
    </main>`;
  const source = doc.querySelector("article");
  const text = source.querySelector("span");
  const port = doc.querySelector("[data-comic-scroll]").firstElementChild;
  const button = doc.querySelector("button");
  const body = "x".repeat(4000);
  window.comicGeometry = { source: body };
  text.textContent = body.slice(2800, 3000);
  port.scrollTop = 12684;
  const rect = { top: 100, bottom: 200, left: 0, right: 100, x: 0, y: 100, width: 100, height: 100 };
  window.HTMLElement.prototype.getBoundingClientRect = () => rect;
  Object.defineProperty(window, "innerWidth", { value: stage.width });
  let glyphY = expected.viewportOffset;
  window.Range.prototype.getBoundingClientRect = function () {
    // Verify the witness selected the saved character, not the containing box.
    expect(this.endOffset - this.startOffset).toBe(1);
    expect(this.startContainer).toBe(text.firstChild);
    // Fixture character 2871 is at 71 in the mounted slice, the drift case at 72.
    expect(this.startOffset).toBe(glyphOffset);
    return { top: rect.top + glyphY };
  };
  window.requestAnimationFrame = (callback) => queueMicrotask(callback);
  let clock = 0;
  vi.spyOn(Date, "now").mockImplementation(() => (clock += 5001));
  const save = (position) => window.sessionStorage.setItem(key, JSON.stringify({ position }));
  save(expected);
  let popup;
  let onDismiss = () => {};
  let onEvaluate = () => {};
  doc.elementFromPoint = (x) => popup
    ? [...popup.querySelectorAll("select")].find((select) => select.getBoundingClientRect().x === x)
    : button;
  const evaluations = [];
  const send = async (method, params) => {
    if (method === "Runtime.evaluate") {
      evaluations.push(params.expression);
      onEvaluate(params.expression, evaluations.length);
      return { result: { result: { value: await window.eval(params.expression) } } };
    }
    if (method === "Input.dispatchMouseEvent" && params.type === "mouseReleased") {
      popup = doc.createElement("div");
      popup.setAttribute("role", "dialog");
      popup.setAttribute("aria-label", "Appearance");
      for (const choices of [["comic", "plain"], ["room", "field", "white"], ["neutral", "happy-wave", "angry", "scared", "point", "celebrate"]]) {
        const label = doc.createElement("label");
        const select = doc.createElement("select");
        for (const choice of choices) select.add(new window.Option(choice, choice));
        label.append(select);
        popup.append(label);
        // Each native select's center hit belongs to that select.
        const x = popup.children.length * 20;
        select.getBoundingClientRect = () => ({ ...rect, x });
      }
      doc.body.append(popup);
    }
    if (method === "Input.dispatchKeyEvent" && params.type === "keyUp") {
      popup.remove();
      popup = null;
      button.focus();
      onDismiss();
    }
    return {};
  };
  return {
    window, document: doc, source, text, port, send, save, evaluations,
    set glyphY(value) { glyphY = value; },
    set onDismiss(callback) { onDismiss = callback; },
    set onEvaluate(callback) { onEvaluate = callback; },
  };
}

async function failedPopup(f) {
  const result = await earlyAppearancePopup(f.send, expected, stage).catch((error) => error);
  expect(result).toBeInstanceOf(Error);
  expect(f.evaluations.some((expression) => expression.includes("function savedSourcePoint"))).toBe(true);
  expect(result.sourcePointDiagnostic).toBeDefined();
  return result;
}

test("source timeout records the pre-open phase and current glyph delta, not the old viewport delta", async () => {
  const f = fixture();
  f.save({ ...expected, viewportOffset: 91.9375 });
  f.glyphY = 94.9375;
  const error = await failedPopup(f);
  const d = error.sourcePointDiagnostic;
  expect(d).toMatchObject({
    ...stage, phase: "before-open", capture: "collected", font: "unavailable",
    expected, current: { ...expected, viewportOffset: 91.9375 }, scrollTop: 12684,
    mounted: { count: 1, truncated: false, ranges: [{ start: 2800, end: 3000, matchesExpected: true, matchesCurrent: true }] },
    source: { start: 2800, end: 3000, textPresent: true, literal: true, y: 94.9375, delta: 3 },
  });
  expect(f.document.querySelector('[role="dialog"]')).toBeNull();
  expect(error.message.endsWith(JSON.stringify(d))).toBe(true);
});

test.each([
  ["offset drift", (f) => f.save({ ...expected, offset: 2872 }), { current: { offset: 2872 }, source: { literal: true } }, 72],
  ["identity drift", (f) => f.save({ ...expected, entryId: "other-item" }), { current: { entryId: "other-item" }, source: null }],
  ["missing saved position", (f) => f.save(null), { current: null, source: null }],
  ["unmounted source", (f) => f.source.remove(), { mounted: { count: 0, ranges: [] }, source: null }],
  ["missing text", (f) => f.text.remove(), { source: { textPresent: false, literal: null, y: null, delta: null } }],
  ["nonliteral text", (f) => { f.text.textContent = "z".repeat(200); }, { source: { textPresent: true, literal: false } }],
])("captures the actual state after %s rejects the source predicate", async (_name, change, fields, glyphOffset) => {
  const f = fixture(glyphOffset);
  change(f);
  const { sourcePointDiagnostic: d } = await failedPopup(f);
  expect(d).toMatchObject({ ...stage, phase: "before-open", capture: "collected", expected, ...fields });
});

test("the post-dismiss source timeout identifies the other popup phase", async () => {
  const f = fixture();
  f.onDismiss = () => { f.glyphY = expected.viewportOffset + 6; };
  const { sourcePointDiagnostic: d } = await failedPopup(f);
  expect(d).toMatchObject({ phase: "after-dismiss", current: expected, source: { y: 91.9375, delta: 6 } });
  expect(f.document.activeElement).toBe(f.document.querySelector("button"));
});

test("success still accepts an updated current viewport offset without collecting diagnostics", async () => {
  const f = fixture();
  f.save({ ...expected, viewportOffset: 91.9375 });
  f.glyphY = 91.9375;
  const result = await earlyAppearancePopup(f.send, expected, stage);
  expect(result.sourceBefore.y).toBe(91.9375);
  expect(result.sourceAfter.saved.viewportOffset).toBe(91.9375);
  // Neither poll nor successful popup should execute the failure-only probe.
  expect(f.evaluations.some((expression) => expression.includes("sourcePointSnapshot"))).toBe(false);
});

test("a diagnostic page exception cannot replace the original transport failure", async () => {
  const f = fixture();
  const original = new Error(randomUUID());
  const originalMessage = original.message;
  const originalStack = original.stack;
  f.onEvaluate = (_expression, count) => {
    if (count === 2) {
      f.window.sessionStorage.setItem(key, "{");
      throw original;
    }
  };
  const error = await failedPopup(f);
  expect(error).toBe(original);
  expect(error.stack).toBe(originalStack);
  expect(error.message.slice(0, originalMessage.length)).toBe(originalMessage);
  expect(error.sourcePointDiagnostic).toMatchObject({ ...stage, phase: "before-open", expected, capture: "unavailable" });
  expect(f.evaluations).toHaveLength(3);
});

test("diagnostic output is bounded metadata even with many mounted ranges and oversized saved fields", async () => {
  const f = fixture();
  const privateText = randomUUID().repeat(1000);
  f.window.comicGeometry.source = privateText;
  f.text.textContent = privateText;
  for (let i = 0; i < 100; i++) f.port.append(f.source.cloneNode(true));
  f.save({ ...expected, sessionRef: "s".repeat(10000), path: Array(100).fill("p".repeat(1000)), secret: privateText });
  const { sourcePointDiagnostic: d } = await failedPopup(f);
  expect(d.mounted.count).toBe(101);
  expect(d.mounted.ranges.length).toBeLessThanOrEqual(16);
  expect(d.mounted.truncated).toBe(true);
  expect(d.current.sessionRef.length).toBeLessThanOrEqual(96);
  expect(d.current.path.length).toBeLessThanOrEqual(8);
  expect(d.current).not.toHaveProperty("secret");
  expect(JSON.stringify(d).length).toBeLessThanOrEqual(16384);
  expect(JSON.stringify(d).includes(privateText)).toBe(false);
});
