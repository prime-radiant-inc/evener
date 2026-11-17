// @vitest-environment jsdom

// The cascadeguard driver selects the control that reveals a delegate row from
// the Agents tab's rendered state. These cases pin that selection at the
// driver/test seam: `revealControlStep` is the exact expression run.mjs
// evaluates, and it interpolates `revealControl`, so a wrong choice fails here
// before a 15s browser timeout.
//
// Fixtures use document.body.innerHTML (or a detached createHTMLDocument
// document) so the helper never depends on a browsing context.
import { beforeEach, expect, test } from "vitest";
import { REFUSED_BUTTON_SELECTOR } from "../browserGuardCdp.mjs";
import { isHistoryReadMethod } from "../lib/historyMethods.mjs";
import { revealControl, revealControlStep, revealReadyExpr } from "./revealControl.mjs";

const CONTAINER = '[data-testid="activity-sidebar"]';
const MISSING_TARGET = `${CONTAINER} [data-activity-anchor="delegate:missing"]`;

beforeEach(() => {
  document.body.innerHTML = "";
});

function sidebar(markup, doc = document) {
  const root = doc.createElement("div");
  root.dataset.testid = "activity-sidebar";
  root.innerHTML = markup;
  doc.body.append(root);
  return root;
}

const current = (id) => `<button id="current-${id}" data-activity-anchor="delegate:${id}">running delegate</button>`;
const inactive = (id) => `<button id="inactive-${id}" data-activity-anchor="delegate:${id}">finished delegate</button>`;
const fold = ({ id = "inactive-fold", attrs = "" } = {}) =>
  `<details><summary id="${id}" ${attrs}>51 inactive subagents</summary></details>`;
const loadMore = (id = "load-more") =>
  `<div id="${id}" role="status" aria-label="Loading"><div data-testid="skeleton-line"></div></div>`;

const pick = (root, refused = REFUSED_BUTTON_SELECTOR) => revealControl(root, refused)?.id ?? null;

// jsdom has no layout or hit testing. Install the two browser boundaries the
// driver's expression uses and evaluate the expression exactly as run.mjs does.
function evaluateStep(root, selector, topmost) {
  root.ownerDocument.defaultView.Element.prototype.scrollIntoView = function () {
    this.dataset.scrolled = "true";
  };
  root.ownerDocument.elementFromPoint = () => topmost ?? root.querySelector("#inactive-fold");
  return new Function(`return ${revealControlStep(selector, CONTAINER, REFUSED_BUTTON_SELECTOR)}`)();
}

function clickRecorder(root) {
  const clicked = { id: null };
  for (const button of root.querySelectorAll("button, summary"))
    button.addEventListener("click", (event) => {
      clicked.id = event.currentTarget.id;
    });
  return clicked;
}

// The gate `reveal` polls before its loop. Evaluate it exactly as the driver
// does: one page expression over the mounted sidebar.
const gate = (selector) => new Function(`return ${revealReadyExpr(selector, CONTAINER, REFUSED_BUTTON_SELECTOR)}`)();

test("a closed inactive disclosure beside a current row is the control", () => {
  expect(pick(sidebar(`${current("a")}${fold()}`))).toBe("inactive-fold");
});

test("a closed inactive disclosure without current rows stays the control", () => {
  expect(pick(sidebar(fold()))).toBe("inactive-fold");
});

test("an unknown inactive total still exposes the native disclosure", () => {
  const root = sidebar(`${current("a")}<details><summary id="inactive-fold">Inactive subagents</summary></details>`);
  expect(pick(root)).toBe("inactive-fold");
  expect(gate(MISSING_TARGET)).toBe(true);
});

test("an open fold with every loaded inactive row visible has no control to press", () => {
  expect(pick(sidebar(`${current("a")}${fold()}${inactive("b")}${loadMore()}`))).toBeNull();
});

test("without a fold the remote page boundary loads automatically", () => {
  expect(pick(sidebar(`${current("a")}${loadMore()}`))).toBeNull();
});

test("a closed inactive disclosure outranks a rendered page boundary", () => {
  expect(pick(sidebar(`${current("a")}${fold()}${loadMore()}`))).toBe("inactive-fold");
});

test("a refused fold offers no control while the page boundary loads automatically", () => {
  expect(pick(sidebar(`${current("a")}${fold({ attrs: 'aria-disabled="true"' })}${loadMore()}`))).toBeNull();
});

test("a refused fold alone offers no control", () => {
  expect(pick(sidebar(`${current("a")}${fold({ attrs: 'aria-disabled="true"' })}`))).toBeNull();
});

test("an empty collection offers no control", () => {
  expect(pick(sidebar(""))).toBeNull();
});

test("the helper keeps working on a document with no browsing context", () => {
  const doc = document.implementation.createHTMLDocument("captured");
  expect(doc.defaultView).toBeNull();
  expect(pick(sidebar(`${current("a")}${fold()}`, doc))).toBe("inactive-fold");
});

test("the serialized function source needs neither the document nor the Node global", () => {
  const root = sidebar(`${current("a")}${fold()}`);
  const evaluate = new Function(
    "container",
    "refused",
    "Node",
    "document",
    `return (${revealControl})(container, refused);`,
  );
  expect(evaluate(root, REFUSED_BUTTON_SELECTOR, undefined, undefined)?.id).toBe("inactive-fold");
});

test("the driver's expression presses the closed fold beside a current row", () => {
  const root = sidebar(`${current("a")}${fold()}`);
  const clicked = clickRecorder(root);
  expect(evaluateStep(root, MISSING_TARGET)).toEqual({ before: root.textContent });
  expect(clicked.id).toBe("inactive-fold");
});

test("the driver's expression returns the target immediately when it is visible", () => {
  const root = sidebar(`${current("a")}${fold()}`);
  const clicked = clickRecorder(root);
  expect(evaluateStep(root, `${CONTAINER} [data-activity-anchor="delegate:a"]`)).toEqual({ revealed: true });
  expect(clicked.id).toBeNull();
});

test("the driver's expression does not press a control that is not topmost", () => {
  const root = sidebar(`${current("a")}${fold()}`);
  const clicked = clickRecorder(root);
  expect(evaluateStep(root, MISSING_TARGET, document.body)).toBeNull();
  expect(clicked.id).toBeNull();
});

test("the gate accepts a page boundary when the first page holds only current rows", () => {
  const root = sidebar(`${current("a")}${loadMore()}`);
  expect(gate(MISSING_TARGET)).toBe(true);
  const clicked = clickRecorder(root);
  expect(evaluateStep(root, MISSING_TARGET, root.querySelector("#load-more"))).toEqual({ before: root.textContent });
  expect(clicked.id).toBeNull();
});

test("the gate accepts a closed fold beside a current row", () => {
  sidebar(`${current("a")}${fold()}`);
  expect(gate(MISSING_TARGET)).toBe(true);
});

test("the gate is satisfied by the target itself", () => {
  sidebar(`${current("a")}${fold()}`);
  expect(gate(`${CONTAINER} [data-activity-anchor="delegate:a"]`)).toBe(true);
});

test("the gate stays shut for a collection with no usable control", () => {
  sidebar(current("a"));
  expect(gate(MISSING_TARGET)).toBe(false);
});

test("the gate stays shut while the sidebar is not mounted", () => {
  expect(gate(MISSING_TARGET)).toBe(false);
});

test("an automatic pending boundary is scrolled into view without a click", () => {
  const root = sidebar(`${current("a")}${loadMore()}`);
  const boundary = root.querySelector("#load-more");
  const clicked = clickRecorder(root);
  expect(evaluateStep(root, MISSING_TARGET, boundary)).toEqual({ before: root.textContent });
  expect(boundary.querySelector('[data-testid="skeleton-line"]').dataset.scrolled).toBe("true");
  expect(clicked.id).toBeNull();
});

test("reconnect observer recognizes both history read aliases", () => {
  for (const method of ["thread/read", "evener/thread/history/read"]) {
    expect(isHistoryReadMethod(method)).toBe(true);
  }
  expect(isHistoryReadMethod("evener/thread/delegates/list")).toBe(false);
});
