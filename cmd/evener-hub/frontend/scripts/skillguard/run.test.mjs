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

// A press is measured on an element at rest inside the viewport: an element
// still moving (the activity sidebar sliding in from translateX, #3897)
// measured mid-move gives a center past the viewport edge, and the press
// misses (cascadeguard's "selected column at level 1"). elementBox re-measures
// each frame until two frames agree and the center is on screen.
describe("elementBox measures a moving element once it rests", () => {
  let frames;
  beforeEach(() => {
    frames = 0;
    vi.stubGlobal("requestAnimationFrame", (callback) =>
      setTimeout(() => {
        frames += 1;
        callback();
      }, 0),
    );
    vi.stubGlobal("innerWidth", 1440);
    vi.stubGlobal("innerHeight", 900);
    driver.page = {
      send: async (method, params) => {
        expect(method).toBe("Runtime.evaluate");
        return { result: { result: { value: await evaluateExpr(params.expression) } } };
      },
    };
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    driver.page = undefined;
  });

  function row(lefts) {
    document.body.innerHTML = '<div id="row">row</div>';
    const element = document.getElementById("row");
    element.scrollIntoView = () => {};
    element.getBoundingClientRect = () => ({ x: lefts[Math.min(frames, lefts.length - 1)], y: 160, width: 200, height: 40 });
    return element;
  }

  test("waits out a slide in from past the right edge", async () => {
    // The row slides in over four frames, then rests at x 1180.
    row([1500, 1500, 1400, 1300, 1200, 1180]);
    expect(await driver.elementBox("#row")).toEqual({ x: 1280, y: 180, w: 200, h: 40 });
  });

  test("measures the row that replaced one detached mid-wait, never the detached zeros", async () => {
    const original = row([1500]);
    // Detached, an element measures all zeros, as Chrome's does.
    original.getBoundingClientRect = () =>
      original.isConnected ? { x: 1500, y: 160, width: 200, height: 40 } : { x: 0, y: 0, width: 0, height: 0 };
    setTimeout(() => {
      original.remove();
      row([1180]);
    }, 0);
    expect(await driver.elementBox("#row")).toEqual({ x: 1280, y: 180, w: 200, h: 40 });
  });

  test("takes a hidden page's box as final, since it paints no frames", async () => {
    vi.stubGlobal("requestAnimationFrame", () => {
      throw new Error("a hidden page runs no frames");
    });
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    row([1500]);
    expect(await driver.elementBox("#row")).toEqual({ x: 1600, y: 180, w: 200, h: 40 });
  });
});
