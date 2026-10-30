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
import { revealControl, revealControlStep } from "./revealControl.mjs";

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
const fold = ({ id = "inactive-fold", attrs = "" } = {}) => `<div><button id="${id}" ${attrs}>Inactive subagents (51)</button></div>`;
const show = (id = "show-more") => `<button id="${id}">Show 20 more · 31 remaining</button>`;
const loadMore = (id = "load-more") => `<button id="${id}">Load more subagents</button>`;

const pick = (root, refused = REFUSED_BUTTON_SELECTOR) => revealControl(root, refused)?.id ?? null;

// jsdom has no layout or hit testing. Install the two browser boundaries the
// driver's expression uses and evaluate the expression exactly as run.mjs does.
function evaluateStep(root, selector, topmost) {
  root.ownerDocument.defaultView.Element.prototype.scrollIntoView = () => {};
  root.ownerDocument.elementFromPoint = () => topmost ?? root.querySelector("#inactive-fold");
  return new Function(`return ${revealControlStep(selector, CONTAINER, REFUSED_BUTTON_SELECTOR)}`)();
}

function clickRecorder(root) {
  const clicked = { id: null };
  for (const button of root.querySelectorAll("button"))
    button.addEventListener("click", (event) => { clicked.id = event.currentTarget.id; });
  return clicked;
}

test("a closed inactive disclosure beside a current row is the control", () => {
  expect(pick(sidebar(`${current("a")}${fold()}`))).toBe("inactive-fold");
});

test("a closed inactive disclosure without current rows stays the control", () => {
  expect(pick(sidebar(fold()))).toBe("inactive-fold");
});

test("an open fold (an anchor follows it) presses Show more", () => {
  expect(pick(sidebar(`${current("a")}${fold()}${inactive("b")}${show()}`))).toBe("show-more");
});

test("an open fold with every loaded inactive row visible presses the remote boundary", () => {
  expect(pick(sidebar(`${current("a")}${fold()}${inactive("b")}${loadMore()}`))).toBe("load-more");
});

test("an open fold with both local and remote pages presses Show first", () => {
  expect(pick(sidebar(`${current("a")}${fold()}${inactive("b")}${show()}${loadMore()}`))).toBe("show-more");
});

test("without a fold only the remote page boundary can reveal a later page", () => {
  expect(pick(sidebar(`${current("a")}${loadMore()}`))).toBe("load-more");
});

test("a closed inactive disclosure outranks a rendered page boundary", () => {
  expect(pick(sidebar(`${current("a")}${fold()}${loadMore()}`))).toBe("inactive-fold");
});

test("a refused fold is skipped and the page boundary is chosen", () => {
  expect(pick(sidebar(`${current("a")}${fold({ attrs: 'aria-disabled="true"' })}${loadMore()}`))).toBe("load-more");
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
  const evaluate = new Function("container", "refused", "Node", "document", `return (${revealControl})(container, refused);`);
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
