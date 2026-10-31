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
  Reflect.deleteProperty(document, "elementFromPoint");
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

// openSession presses a rail row in the same page turn it finds and measures
// it. A press measured in one turn and sent in the next landed on the
// neighbouring session's row once the rail shifted, and the target's composer
// never mounted (#3874).
describe("pressRailRowExpr finds, hit-tests and presses in one turn", () => {
  // jsdom has no hit test, so each case stubs elementFromPoint on the
  // document; dropping that own property leaves later tests without one again.
  afterEach(() => {
    Reflect.deleteProperty(document, "elementFromPoint");
  });

  function renderRows() {
    document.body.innerHTML = `<button data-session-ref="target">status chip</button><nav data-sidebar-rail>${["other", "target"]
      .map((ref) => `<span data-session-ref="${ref}"><span class="text"><b>${ref}</b></span><span>now</span></span>`)
      .join("")}</nav>`;
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

  test("presses the row when its center hits a child of the text, as in a browser", () => {
    const clicks = renderRows();
    document.elementFromPoint = () => document.querySelector('[data-session-ref="target"] .text b');
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

// The slash menu row keeps its native press (its mousedown keeps the editor
// focused), but is found, scrolled to and hit-tested in the turn that returns
// the point, never measured a turn earlier (#4071).
describe("slashRowPointExpr hit-tests the skill's own row", () => {
  function renderMenu() {
    document.body.innerHTML = `<div data-testid="composer-slash-menu"><button><span>/other</span></button><button><span>/review</span></button></div>`;
    for (const button of document.querySelectorAll("button")) button.scrollIntoView = () => {};
  }

  test("returns the row's center while the row is topmost there", () => {
    renderMenu();
    document.elementFromPoint = () => document.querySelectorAll("button span")[1];
    expect(evaluateExpr(driver.slashRowPointExpr("/review"))).toEqual({ x: 0, y: 0 });
  });

  test("reports not ready while another row is at its center", () => {
    renderMenu();
    document.elementFromPoint = () => document.querySelector("button span");
    expect(evaluateExpr(driver.slashRowPointExpr("/review"))).toBeNull();
  });

  test("reports not ready while the row is absent", () => {
    document.body.innerHTML = `<div data-testid="composer-slash-menu"><button><span>/other</span></button></div>`;
    expect(evaluateExpr(driver.slashRowPointExpr("/review"))).toBeNull();
  });
});

// A tile's remove button is clicked in the turn it is found and hit-tested.
describe("removeAttachmentExpr presses the first tile's remove button in one turn", () => {
  function renderTiles() {
    document.body.innerHTML = `<div><div data-pane-scaffold="session:a"></div>${["one.png", "two.png"]
      .map((name) => `<div data-testid="attachment-tile"><button aria-label="Remove ${name}"><b>x</b></button></div>`)
      .join("")}</div>`;
    const removed = [];
    for (const button of document.querySelectorAll("button")) {
      button.scrollIntoView = () => {};
      button.addEventListener("click", () => removed.push(button.getAttribute("aria-label")));
    }
    return removed;
  }

  test("clicks the first remove button and returns its label", () => {
    const removed = renderTiles();
    document.elementFromPoint = () => document.querySelector("button b");
    expect(evaluateExpr(driver.removeAttachmentExpr("a"))).toBe("Remove one.png");
    expect(removed).toEqual(["Remove one.png"]);
  });

  test("clicks nothing while the button is covered", () => {
    const removed = renderTiles();
    document.elementFromPoint = () => null;
    expect(evaluateExpr(driver.removeAttachmentExpr("a"))).toBe("covered");
    expect(removed).toEqual([]);
  });

  test("reports a pane with no tile", () => {
    document.body.innerHTML = `<div><div data-pane-scaffold="session:a"></div></div>`;
    expect(evaluateExpr(driver.removeAttachmentExpr("a"))).toBe("missing");
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

// The transcript is a virtual list: a row exists in the DOM only while the
// reader's position (plus a small overscan) keeps it mounted, and a reader
// away from the bottom does not follow appends. A reply that arrived while the
// guard was editing a newer draft was therefore rendered by the app yet absent
// from every mounted turn-block, and the old wait polled those blocks until
// its 25s timeout -- the failure reported as "timed out ... waiting for the
// reply to PROSE_DELAY_14i ..." (#4823). waitForReply must put the tail in view
// first, the reader's own jump-to-latest gesture, and only then look for the
// turn. This scripts that exact virtual-list behaviour: the reply's row mounts
// only once the transcript port is scrolled to the bottom.
describe("waitForReply brings a virtualized transcript tail into view", () => {
  const PROSE = "PROSE_DELAY_14i submitted then edited while held";

  function renderVirtualTranscript({ atBottom }) {
    document.body.innerHTML = `<div><div data-pane-scaffold="session:ref_a">
      <section data-testid="transcript-virtual-list"><div id="port"></div></section>
    </div></div>`;
    const port = document.getElementById("port");
    let scrollTopValue = atBottom ? 900 : 0;
    const mountTailRow = () => {
      if (scrollTopValue < 896) return;
      if (document.querySelector("[data-testid='turn-block']")) return;
      const row = document.createElement("div");
      row.setAttribute("data-testid", "turn-block");
      row.setAttribute("data-turn-id", "turn_m12");
      row.textContent = `${PROSE} /pkg:probe skillguard turn complete`;
      document.querySelector("[data-pane-scaffold='session:ref_a']").appendChild(row);
    };
    Object.defineProperty(port, "scrollHeight", { configurable: true, get: () => 1000 });
    Object.defineProperty(port, "clientHeight", { configurable: true, get: () => 100 });
    Object.defineProperty(port, "scrollTop", {
      configurable: true,
      get: () => scrollTopValue,
      set: (value) => {
        scrollTopValue = value;
        mountTailRow();
      },
    });
    if (atBottom) mountTailRow();
    return port;
  }

  // The driver's waitPage loop, minus the clock: it re-evaluates the real
  // expression until it reads non-null, as the real poll does once per tick.
  function scriptWaitPage(d, { attempts = 20, delayMs = 0 } = {}) {
    d.waitPage = async (exprSource, { label } = {}) => {
      for (let attempt = 0; attempt < attempts; attempt += 1) {
        const value = evaluateExpr(exprSource);
        if (value !== null && value !== undefined && value !== false) return value;
        await new Promise((resolve) => setTimeout(resolve, delayMs));
      }
      throw new Error(`timed out waiting for ${label}`);
    };
  }

  test("the reply predicate alone cannot see the unmounted tail row", () => {
    renderVirtualTranscript({ atBottom: false });
    const replyExpr = `(() => ${driver.turnTextsExpr("ref_a")}.some((text) => text.includes(${JSON.stringify(PROSE)}) && text.includes("skillguard turn complete")) ? true : null)()`;
    expect(evaluateExpr(replyExpr)).toBeNull();
  });

  test("waitForReply scrolls the tail into view, mounting and observing the reply", async () => {
    const port = renderVirtualTranscript({ atBottom: false });
    const d = new Driver({ url: "http://127.0.0.1/", artifactDir: "", controlPath: "", milestonePath: "" });
    scriptWaitPage(d);
    await expect(d.waitForReply("ref_a", PROSE)).resolves.toBeUndefined();
    // The barrier moved the port to the end (the row mounted), it did not
    // poll a row the reader's position never exposed.
    expect(port.scrollTop).toBeGreaterThanOrEqual(896);
    expect(document.querySelector("[data-testid='turn-block']")?.getAttribute("data-turn-id")).toBe("turn_m12");
  });

  test("an already-following transcript needs no scroll", async () => {
    const port = renderVirtualTranscript({ atBottom: true });
    const d = new Driver({ url: "http://127.0.0.1/", artifactDir: "", controlPath: "", milestonePath: "" });
    scriptWaitPage(d);
    await expect(d.waitForReply("ref_a", PROSE)).resolves.toBeUndefined();
    expect(port.scrollTop).toBe(900);
  });

  // The app applies a retained-view restore only after its measurements and
  // opening settlement, so the restore can land AFTER a one-shot barrier has
  // already observed the bottom. This scripts that ordering: the restore fires
  // 5ms into the wait, brings the reply with it, and scrolls back to the top.
  // A barrier that resolved on the first bottom observation would leave the
  // reply unmounted for the rest of the wait; correcting the tail on every
  // poll brings it back and observes the reply.
  test("a retained-view restore that lands after the first bottom observation is corrected", async () => {
    document.body.innerHTML = `<div><div data-pane-scaffold="session:ref_a">
      <section data-testid="transcript-virtual-list"><div id="port"></div></section>
    </div></div>`;
    const pane = document.querySelector("[data-pane-scaffold='session:ref_a']");
    const port = document.getElementById("port");
    const replyExpr = `(() => ${driver.turnTextsExpr("ref_a")}.some((text) => text.includes(${JSON.stringify(PROSE)}) && text.includes("skillguard turn complete")) ? true : null)()`;
    let scrollTopValue = 0;
    let replyReady = false;
    let restoredAwayWithReplyUnseen = false;
    const mountReplyIfReady = () => {
      if (!replyReady || scrollTopValue < 896) return;
      if (pane.querySelector("[data-testid='turn-block']")) return;
      const row = document.createElement("div");
      row.setAttribute("data-testid", "turn-block");
      row.setAttribute("data-turn-id", "turn_m12");
      row.textContent = `${PROSE} /pkg:probe skillguard turn complete`;
      pane.appendChild(row);
    };
    Object.defineProperty(port, "scrollHeight", { configurable: true, get: () => 1000 });
    Object.defineProperty(port, "clientHeight", { configurable: true, get: () => 100 });
    Object.defineProperty(port, "scrollTop", {
      configurable: true,
      get: () => scrollTopValue,
      set: (value) => {
        scrollTopValue = value;
        mountReplyIfReady();
      },
    });
    const restore = setTimeout(() => {
      replyReady = true;
      scrollTopValue = 0;
      for (const row of pane.querySelectorAll("[data-testid='turn-block']")) row.remove();
      // At this instant the wait's own predicate cannot see the reply: the
      // restore moved the reader away from the row the virtual list unmounted.
      restoredAwayWithReplyUnseen = evaluateExpr(replyExpr) === null;
    }, 5);
    try {
      const d = new Driver({ url: "http://127.0.0.1/", artifactDir: "", controlPath: "", milestonePath: "" });
      // A timed-out wait dumps the page; with no CDP page, let that dump fail
      // through its own catch instead of masking the timeout with a TypeError.
      d.page = { send: () => Promise.reject(new Error("no page in this scripted test")) };
      scriptWaitPage(d, { attempts: 200, delayMs: 1 });
      await expect(d.waitForReply("ref_a", PROSE)).resolves.toBeUndefined();
    } finally {
      clearTimeout(restore);
    }
    expect(restoredAwayWithReplyUnseen).toBe(true);
    expect(port.scrollTop).toBeGreaterThanOrEqual(896);
    expect(document.querySelector("[data-testid='turn-block']")?.getAttribute("data-turn-id")).toBe("turn_m12");
  });
});

// Script only the Runtime.evaluate transport boundary, as budgets.test.mjs
// does, so the real waitPage loop and reaction observations own the verdict.
describe("waitPage composed deadlines", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(0);
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  function pageDriver(read) {
    const d = new Driver({ url: "http://127.0.0.1/", artifactDir: "", controlPath: "", milestonePath: "" });
    d.page = {
      send: vi.fn(async (_method, { expression }) => ({ result: { result: { value: await read(expression) } } })),
    };
    return d;
  }

  test("spends the original adaptive allowance across a 1000ms open action", async () => {
    const d = pageDriver((expression) => expression === "prior" && Date.now() >= 10000 ? true : null);
    const prior = d.waitPage("prior", { timeoutMs: 60000 });
    await vi.advanceTimersByTimeAsync(10000);
    await expect(prior).resolves.toBe(true);
    expect(d.reactions.slowestMs).toBe(10000);

    const startedAt = Date.now();
    const deadlineAt = startedAt + d.reactions.deadline(60000);
    expect(deadlineAt - startedAt).toBe(100000);
    await vi.advanceTimersByTimeAsync(1000); // The new open action's cost.
    let rejectedAt;
    const waiting = d.waitPage("gap", { timeoutMs: 60000, deadlineAt }).catch((error) => {
      rejectedAt = Date.now();
      return error;
    });
    await vi.advanceTimersByTimeAsync(101000);
    expect(await waiting).toBeInstanceOf(Error);
    // The existing 80ms polling quantum can cross the tripwire once, but the
    // new open action cannot buy an extra 1000ms of adaptive waiting.
    expect(rejectedAt - startedAt).toBeGreaterThan(100000);
    expect(rejectedAt - startedAt).toBeLessThanOrEqual(100080);
  });

  test("keeps adaptive expansion for callers without a precomputed deadline", async () => {
    const d = pageDriver(() => Date.now() >= 90000 ? true : null);
    d.reactions.observe(10000);
    const waiting = d.waitPage("condition", { timeoutMs: 60000 });
    await vi.advanceTimersByTimeAsync(90000);
    await expect(waiting).resolves.toBe(true);
    expect(d.reactions.slowestMs).toBe(90000);
  });

  test.each([
    { remainder: -1, rejectedAt: 0 },
    { remainder: 0, rejectedAt: 80 },
  ])("does not re-expand an exhausted deadline with remainder $remainder", async ({ remainder, rejectedAt }) => {
    const d = pageDriver(() => null);
    d.reactions.observe(10000);
    let finishedAt;
    const waiting = d.waitPage("condition", { timeoutMs: 60000, deadlineAt: Date.now() + remainder }).catch((error) => {
      finishedAt = Date.now();
      return error;
    });
    await vi.advanceTimersByTimeAsync(160);
    expect(await waiting).toBeInstanceOf(Error);
    expect(finishedAt).toBe(rejectedAt);
    expect(d.reactions.slowestMs).toBe(10000);
  });

  test.each([0, ""])("retains the first-poll success predicate for value %j at a zero remainder", async (value) => {
    const d = pageDriver(() => value);
    await expect(d.waitPage("condition", { deadlineAt: Date.now() })).resolves.toBe(value);
  });

  describe.each(["adaptive", "precomputed"])("%s wait behavior", (mode) => {
    const deadlineOption = (allowance) => mode === "precomputed" ? { deadlineAt: Date.now() + allowance } : {};

    test("retries a transient evaluate error and observes the successful reaction", async () => {
      let attempts = 0;
      const d = pageDriver(() => {
        if (++attempts === 1) throw new Error("SCRIPTED_EVALUATE_FAILURE");
        return 0;
      });
      const waiting = d.waitPage("condition", { timeoutMs: 500, ...deadlineOption(500) });
      await vi.advanceTimersByTimeAsync(80);
      await expect(waiting).resolves.toBe(0);
      expect(attempts).toBe(2);
      expect(d.reactions.slowestMs).toBe(80);
    });

    test("keeps the observed settle time across a refunded evaluate failure", async () => {
      const d = pageDriver(() => {
        if (Date.now() === 80) throw new Error("SCRIPTED_EVALUATE_FAILURE");
        return true;
      });
      let finishedAt;
      const waiting = d.waitPage("condition", { timeoutMs: 80, settleMs: 160, ...deadlineOption(80) }).then((value) => {
        finishedAt = Date.now();
        return value;
      });
      await vi.advanceTimersByTimeAsync(240);
      await expect(waiting).resolves.toBe(true);
      expect(finishedAt).toBe(240);
      expect(d.reactions.slowestMs).toBe(80);
    });

    test("limits error refunds to one settle window and fails an unobservable condition", async () => {
      const d = pageDriver(() => {
        if (Date.now() > 0) throw new Error("SCRIPTED_EVALUATE_FAILURE");
        return true;
      });
      let rejectedAt;
      const waiting = d.waitPage("condition", { timeoutMs: 80, settleMs: 160, ...deadlineOption(80) }).catch((error) => {
        rejectedAt = Date.now();
        return error;
      });
      await vi.advanceTimersByTimeAsync(480);
      expect(await waiting).toBeInstanceOf(Error);
      expect(rejectedAt).toBe(400);
      expect(d.reactions.slowestMs).toBe(0);
    });
  });
});
