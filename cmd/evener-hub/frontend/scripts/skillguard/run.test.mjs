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
    expect(evaluateExpr(driver.railRowsExpr(2))).toBeNull();
  });

  test("reports not ready (null) with only one row", () => {
    renderRail(["a"]);
    expect(evaluateExpr(driver.railRowsExpr(2))).toBeNull();
  });

  test("returns the rows once at least two are present", () => {
    renderRail(["a", "b", "c"]);
    const ready = evaluateExpr(driver.railRowsExpr(2));
    expect(ready).not.toBeNull();
    expect(ready.rows.map((row) => row.ref)).toEqual(["a", "b", "c"]);
  });

  test("keeps the always-truthy { rows } shape when no minimum is given", () => {
    expect(evaluateExpr(driver.railRowsExpr())).toEqual({ rows: [] });
  });
});
