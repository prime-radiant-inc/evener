// Pins the skillguard driver's rail readiness predicate. The wait that gates
// the "two live sessions in the rail" assertion evaluates railRowsExpr's
// expression in the page; when that expression returned an always-truthy
// { rows: [...] } the wait could never wait, so the following >= 2 assertion
// raced the rail (observed: "found 0" while the failure dump held 2 rows).
//
// These cases evaluate the REAL expression string in jsdom, so the
// not-ready (null) / ready (rows) contract is pinned without a browser.

import { beforeEach, describe, expect, test } from "vitest";

import { Driver } from "./run.mjs";

const driver = new Driver({ url: "http://127.0.0.1/", artifactDir: "", controlPath: "", milestonePath: "" });

// Evaluate the driver's own expression the way waitPage does: in a page scope
// where `document` resolves. jsdom supplies that global.
function evaluateExpr(expr) {
  return new Function(`return (${expr});`)();
}

function renderRail(refs) {
  document.body.innerHTML = refs.map((ref) => `<div data-session-ref="${ref}">row ${ref}</div>`).join("");
}

beforeEach(() => {
  document.body.innerHTML = "";
});

describe("railRowsExpr readiness predicate", () => {
  test("reports not ready (null) while the rail is empty", () => {
    expect(evaluateExpr(driver.railRowsExpr({ atLeast: 2 }))).toBeNull();
  });

  test("reports not ready (null) with only one row", () => {
    renderRail(["a"]);
    expect(evaluateExpr(driver.railRowsExpr({ atLeast: 2 }))).toBeNull();
  });

  test("returns the rows once at least two are present", () => {
    renderRail(["a", "b", "c"]);
    const ready = evaluateExpr(driver.railRowsExpr({ atLeast: 2 }));
    expect(ready).not.toBeNull();
    expect(ready.rows.map((row) => row.ref)).toEqual(["a", "b", "c"]);
  });

  test("keeps the always-truthy { rows } shape when no minimum is given", () => {
    expect(evaluateExpr(driver.railRowsExpr())).toEqual({ rows: [] });
  });
});

// selectAll feeds the queue journey's replacement edit: the driver selects the
// draft a queued entry returned and types over it. The editor adopts a DOM
// selection asynchronously, so a render landing in between can leave the caret
// where it was and the next typeText would then read -- and type into -- a
// position the scenario never meant. These pin the confirm/re-apply/fail
// contract against a scripted state sequence, no browser needed.
describe("selectAll holds the whole-text selection", () => {
  const editState = (start, end, value = "PROSE_QUEUE_14c first pass /pkg:probe") => ({
    value,
    start,
    end,
    focused: true,
  });

  function scriptedDriver(script) {
    const driver = new Driver({ url: "http://127.0.0.1/", artifactDir: "", controlPath: "", milestonePath: "" });
    const ranges = [];
    // Both reads come from the same script: the pre-selection state, then the
    // state the editor settled on.
    driver.composerEditState = async () => script.shift();
    driver.settleComposer = async () => script.shift();
    driver.selectRange = async (_ref, start, end) => {
      ranges.push([start, end]);
    };
    return { driver, ranges };
  }

  test("returns after one selection when the editor holds it", async () => {
    const { driver: d, ranges } = scriptedDriver([editState(0, 0), editState(0, 37)]);
    await d.selectAll("ref_a");
    expect(ranges).toEqual([[0, 37]]);
  });

  test("re-applies the selection a render reset, then returns", async () => {
    const { driver: d, ranges } = scriptedDriver([
      editState(0, 0),
      editState(37, 37),
      editState(37, 37),
      editState(0, 37),
    ]);
    await d.selectAll("ref_a");
    expect(ranges).toEqual([
      [0, 37],
      [0, 37],
    ]);
  });

  test("re-applies when the draft grew after the selection was placed", async () => {
    // The range was placed for the short draft; the editor then rendered a
    // longer one and mapped the selection to the old prefix. Comparing against
    // the length read before the range (11) would read that as a whole-text
    // hold and strand the tail, so the settled text is the yardstick.
    const { driver: d, ranges } = scriptedDriver([
      editState(0, 0, "SHORT_DRAFT"),
      editState(0, 11),
      editState(0, 37),
      editState(0, 37),
    ]);
    await d.selectAll("ref_a");
    expect(ranges).toEqual([
      [0, 11],
      [0, 37],
    ]);
  });

  test("reports an editor that will not hold the selection", async () => {
    // Every settle reports the reset caret, so no attempt ever holds: the guard
    // must fail, not type into the collapsed caret.
    const { driver: d, ranges } = scriptedDriver([
      editState(0, 0),
      editState(37, 37),
      editState(37, 37),
      editState(37, 37),
      editState(37, 37),
      editState(37, 37),
      editState(37, 37),
      editState(37, 37),
    ]);
    await expect(d.selectAll("ref_a")).rejects.toThrow(/would not hold the whole-text selection/);
    expect(ranges.length).toBe(4);
  });
});
