// @vitest-environment jsdom

// `drill` used to measure a sidebar row and press its coordinates in a later
// page turn. The Overview restores a retained scroll anchor as a page lands,
// so rows move between those turns and the press can hit a neighbour: #7314
// measured cascade-1 at y 175 and pressed cascade-page-23, which the cascade
// then correctly selected, leaving the drill to time out on cascade-1's
// column. These cases evaluate `pressRowExpr` exactly as the driver does and
// pin what closes the gap: the row is located, exposed, hit-tested and
// pressed in one page turn, and the press is refused unless the row's own
// control is topmost at its centre.
//
// jsdom has no layout or hit testing, so each fixture installs both browser
// boundaries the expression uses; positions live in `rects`. Each case is one
// page turn, where the old press spent two, so these cases pin the ordering
// inside the expression, not the list motion itself. The anchor attribute is
// set in code because its JSON shape holds quotes, which HTML markup cannot
// carry verbatim.
import { beforeEach, expect, test } from "vitest";
import { pressRowExpr } from "./pressRow.mjs";

const CONTAINER = '[data-testid="activity-sidebar"]';
const anchor = (id) => `delegate:["local:${id}","dlg_${id}"]`;
const ROW = (id) => `${CONTAINER} [data-activity-anchor=${JSON.stringify(anchor(id))}]`;
const at = (x, y, width = 285, height = 32) => ({ x, y, width, height });
const middle = (rect) => ({ x: rect.x + rect.width / 2, y: rect.y + rect.height / 2 });

let rects;
let pressed;
let hits;

beforeEach(() => {
  document.body.innerHTML = "";
  rects = new Map();
  pressed = [];
  hits = [];
});

function press(selector) {
  return new Function(`return ${pressRowExpr(selector)}`)();
}

function sidebar(markup) {
  const root = document.createElement("div");
  root.dataset.testid = "activity-sidebar";
  root.innerHTML = markup;
  document.body.append(root);
  const box = (el) => rects.get(el) ?? { x: 0, y: 0, width: 0, height: 0 };
  for (const el of [root, ...root.querySelectorAll("*")]) {
    el.getBoundingClientRect = () => box(el);
    el.scrollIntoView = () => {};
  }
  for (const button of root.querySelectorAll("button")) {
    button.addEventListener("click", (event) => pressed.push(event.currentTarget.id));
  }
  // Later document order wins, so a neighbour rendered after the row is the
  // topmost element where their boxes overlap.
  document.elementFromPoint = (x, y) => {
    hits.push({ x, y });
    return (
      [...root.querySelectorAll("*")]
        .filter((el) => {
          const r = box(el);
          return r.width > 0 && r.height > 0 && x >= r.x && x <= r.x + r.width && y >= r.y && y <= r.y + r.height;
        })
        .at(-1) ?? null
    );
  };
  return root;
}

function anchorRow(root, id) {
  const el = root.querySelector(`#${id}`);
  el.dataset.activityAnchor = anchor(id);
  return el;
}

test("a plain row presses the anchor's own button and names it", () => {
  const root = sidebar(`<button id="a">cascade-1</button>`);
  rects.set(anchorRow(root, "a"), at(700, 160));
  expect(press(ROW("a"))).toBe(anchor("a"));
  expect(pressed).toEqual(["a"]);
});

test("a split row presses its main button, not the toggle over its container's centre", () => {
  const root = sidebar(`<div id="row"><button id="main">cascade-2</button>
    <span><button id="toggle" aria-label="Collapse cascade-2"></button></span></div>`);
  const row = root.querySelector("#row");
  row.dataset.activityAnchor = anchor("b");
  rects.set(row, at(700, 160));
  rects.set(root.querySelector("#main"), at(700, 160, 125, 32));
  // The container's own centre (x 842.5) sits over the trailing toggle.
  rects.set(root.querySelector("#toggle"), at(830, 160, 25, 32));
  expect(press(ROW("b"))).toBe(anchor("b"));
  expect(pressed).toEqual(["main"]);
});

test("a row below the fold is scrolled into view before it is measured and pressed", () => {
  const root = sidebar(`<button id="h">cascade-7</button>`);
  const row = anchorRow(root, "h");
  rects.set(row, at(700, 2000));
  row.scrollIntoView = () => rects.set(row, at(700, 400));
  expect(press(ROW("h"))).toBe(anchor("h"));
  expect(hits).toEqual([middle(at(700, 400))]);
  expect(pressed).toEqual(["h"]);
});

test("a press that unmounts its row still names the row it pressed", () => {
  const root = sidebar(`<button id="c">cascade-3</button>`);
  const row = anchorRow(root, "c");
  rects.set(row, at(700, 160));
  row.addEventListener("click", () => row.remove());
  expect(press(ROW("c"))).toBe(anchor("c"));
  expect(pressed).toEqual(["c"]);
});

test("a row whose anchor its own press clears still names what it pressed", () => {
  const root = sidebar(`<button id="x">cascade-8</button>`);
  const row = anchorRow(root, "x");
  rects.set(row, at(700, 160));
  row.addEventListener("click", () => delete row.dataset.activityAnchor);
  // The driver retries a null result, which would press a second row; reading
  // the anchor before the press keeps the result non-null.
  expect(press(ROW("x"))).toBe(anchor("x"));
  expect(hits).toHaveLength(1);
  expect(pressed).toEqual(["x"]);
});

test("the press point comes from the row's current box, not a stale one", () => {
  const root = sidebar(`<button id="d">cascade-4</button><button id="e">cascade-page-23</button>`);
  const row = anchorRow(root, "d");
  anchorRow(root, "e");
  // The Overview's retained-anchor restore has moved the row down and the
  // paged delegate now sits in the y 160..192 band. jsdom has one page turn,
  // so this cannot reproduce #7314 by itself; it pins that the point pressed
  // is the box read in the same turn, and holds the moved-row fixture the
  // browser drill's one-turn press relies on.
  rects.set(row, at(700, 620));
  rects.set(root.querySelector("#e"), at(700, 160));
  expect(press(ROW("d"))).toBe(anchor("d"));
  expect(hits).toEqual([middle(at(700, 620))]);
  expect(pressed).toEqual(["d"]);
});

test("a covered row is refused, so the neighbour under its centre is never pressed", () => {
  const root = sidebar(`<button id="f">cascade-5</button><button id="neighbour">cascade-page-24</button>`);
  rects.set(anchorRow(root, "f"), at(700, 160));
  rects.set(root.querySelector("#neighbour"), at(700, 160));
  expect(press(ROW("f"))).toBeNull();
  expect(pressed).toEqual([]);
});

test("a missing row is not pressed", () => {
  sidebar("");
  expect(press(ROW("missing"))).toBeNull();
  expect(pressed).toEqual([]);
});

test("a row with no layout is not pressed", () => {
  const root = sidebar(`<button id="g">cascade-6</button>`);
  const row = anchorRow(root, "g");
  expect(press(ROW("g"))).toBeNull();
  rects.set(row, at(700, 160));
  expect(press(ROW("g"))).toBe(anchor("g"));
  expect(pressed).toEqual(["g"]);
});
