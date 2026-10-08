// Pins the skillguard driver's rail readiness predicate. The wait that gates
// the "two live sessions in the rail" assertion evaluates railRowsExpr's
// expression in the page; when that expression returned an always-truthy
// { rows: [...] } the wait could never wait, so the following >= 2 assertion
// raced the rail (observed: "found 0" while the failure dump held 2 rows).
//
// These cases evaluate the REAL expression string in jsdom, so the
// not-ready (null) / ready (rows) contract is pinned without a browser.

import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { rmSync } from "node:fs";

import { CHROME_SOCKET_PATH_LIMIT, chromeSingletonSocketPath } from "../browserGuardProcess.mjs";
import { Driver, skillGuardChromeProfileDir } from "./run.mjs";

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
// openSession presses a rail row in the same page turn it finds and measures
// it. A press measured in one turn and sent in the next landed on the
// neighbouring session's row once the rail shifted, and the target's composer
// never mounted (#3874).
describe("pressRailRowExpr finds, hit-tests and presses in one turn", () => {
  function renderRows() {
    document.body.innerHTML = ["other", "target"]
      .map((ref) => `<span data-session-ref="${ref}"><span class="text">${ref}</span><span>now</span></span>`)
      .join("");
    const clicks = [];
    for (const text of document.querySelectorAll(".text")) {
      text.addEventListener("click", () => clicks.push(text.textContent));
      text.scrollIntoView = () => {};
    }
    return clicks;
  }

  test("presses the target row's text when it is topmost at its center", () => {
    const clicks = renderRows();
    document.elementFromPoint = () => document.querySelector('[data-session-ref="target"] .text');
    expect(evaluateExpr(driver.pressRailRowExpr("target"))).toBe(true);
    expect(clicks).toEqual(["target"]);
  });

  test("presses nothing and reports not ready while another row is at its center", () => {
    const clicks = renderRows();
    document.elementFromPoint = () => document.querySelector('[data-session-ref="other"] .text');
    expect(evaluateExpr(driver.pressRailRowExpr("target"))).toBeNull();
    expect(clicks).toEqual([]);
  });

  test("reports not ready while the row is absent", () => {
    document.elementFromPoint = () => null;
    expect(evaluateExpr(driver.pressRailRowExpr("target"))).toBeNull();
  });
});

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

  // Each re-selection is announced on stderr, the log a failing skillguard run
  // leaves behind; the tests that re-select assert those lines.
  const reselecting = (attempt) =>
    `skillguard: ref_a: a render reset the selection after selectAll; re-selecting (attempt ${attempt}/4)`;

  afterEach(() => {
    vi.restoreAllMocks();
  });

  test("returns after one selection when the editor holds it", async () => {
    const { driver: d, ranges } = scriptedDriver([editState(0, 0), editState(0, 37)]);
    await d.selectAll("ref_a");
    expect(ranges).toEqual([[0, 37]]);
  });

  test("re-applies the selection a render reset, then returns", async () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
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
    expect(consoleError.mock.calls).toEqual([[reselecting(1)]]);
  });

  test("re-applies when the draft grew after the selection was placed", async () => {
    // The range was placed for the short draft; the editor then rendered a
    // longer one and mapped the selection to the old prefix. Comparing against
    // the length read before the range (11) would read that as a whole-text
    // hold and strand the tail, so the settled text is the yardstick.
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
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
    expect(consoleError.mock.calls).toEqual([[reselecting(1)]]);
  });

  test("reports an editor that will not hold the selection", async () => {
    // Every settle reports the reset caret, so no attempt ever holds: the guard
    // must fail, not type into the collapsed caret.
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
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
    // The fourth, last attempt fails the guard instead of announcing a retry.
    expect(consoleError.mock.calls).toEqual([[reselecting(1)], [reselecting(2)], [reselecting(3)]]);
  });
});

// The guard runs under a deep ambient TMPDIR (a test harness nested in an agent
// sandbox is two temp layers deep). Minting the profile straight under
// os.tmpdir() there pushed Chrome's singleton socket past sun_path, so Chrome
// aborted with "Socket path too long" before DevTools was ready and the guard
// failed on a path-length accident (issue #3198). The profile must be minted
// under a short root and its derived socket path must fit, whatever TMPDIR is.
describe("skillguard Chrome profile fits the singleton socket budget", () => {
  test("mints under a short root when the ambient TMPDIR is too deep", () => {
    const ambient = `/tmp/${"nested/".repeat(16)}sandbox`;
    expect(ambient.length).toBeGreaterThan(CHROME_SOCKET_PATH_LIMIT);
    const dir = skillGuardChromeProfileDir({ ambient });
    try {
      expect(chromeSingletonSocketPath(dir).length).toBeLessThanOrEqual(CHROME_SOCKET_PATH_LIMIT);
      expect(dir.startsWith(ambient)).toBe(false);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });
});
