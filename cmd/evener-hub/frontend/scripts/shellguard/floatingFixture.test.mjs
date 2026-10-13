// Pins the floating fixture's FAILURE witness (#3747). The panel wait in
// measureFloatingDock can time out with nothing but its own label - "timed out
// after 15000ms (waited 15035ms) waiting for real floating fixture's workspace
// panel" - and the wait covers only the openPane -> addPanel hop, so the
// operator learns nothing about what the page actually held.
//
// These cases drive the SAME exported `waitForFloatingState` /
// `describeFloatingFailure` run.mjs calls, through the REAL `evaluate` and the
// REAL `floatingWitnessExpr`, with a fake CDP send that runs the page
// expression in jsdom and a fake driver whose `waitPage` fails the way the real
// one does. The fakes stub only the browser transport and the wait loop (the
// pattern in sessionHoverCard.test.mjs); the conditions under test are the
// guard's own exported ones. No browser.

import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import {
  DOCKVIEW_READY_LABEL,
  DOCKVIEW_READY_WAIT_EXPR,
  describeFloatingFailure,
  FLOATING_PANEL_LABEL,
  FLOATING_PANEL_WAIT_EXPR,
  floatingWitnessExpr,
  WITNESS_READ_MS,
  waitForFloatingState,
} from "./floatingFixture.mjs";

// Evaluate a page expression the way the guard's evaluate does, in jsdom.
function evaluateInPage(expression) {
  return new Function(`return (${expression});`)();
}

// A fake CDP `send`: Runtime.evaluate runs the page expression and returns the
// response shape `evaluate` unwraps.
function pageSend() {
  return vi.fn(async (method, params) => {
    if (method !== "Runtime.evaluate") throw new Error(`unexpected CDP method ${method}`);
    return { result: { result: { value: evaluateInPage(params.expression) } } };
  });
}

// A fake Driver: `page.send` is the CDP transport, and `waitPage` polls it the
// way the real one does before failing with the real wait's message. It records
// the condition and options it was asked for, so a test can pin the wiring
// run.mjs installs.
function fakeDriver(send) {
  const page = { send };
  return {
    page,
    // Driver.send is a getter onto page.send (skillguard/run.mjs), which is
    // what makes watchEvaluateFailures' wrapper visible to waitPage.
    get send() {
      return page.send;
    },
    waitPage: vi.fn(async (expression, { label }) => {
      for (let poll = 0; poll < 3; poll += 1) {
        await page.send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true }).catch(() => {});
      }
      throw new Error(`timed out after 15000ms (waited 15035ms) waiting for ${label}`);
    }),
  };
}

// The page global the guard installs before it opens Settings, plus the store
// and api reads the witness makes. `panels` is what the live api holds.
function installFixture({ panes = [], panels = [], paneId = null, focusedPaneId = null } = {}) {
  window.shellguardFloat = {
    paneId,
    workspaceStore: { getState: () => ({ panes, focusedPaneId }) },
    getDockviewApi: () => ({
      panels: panels.map((id) => ({ id })),
      getPanel: (id) => (panels.includes(id) ? { id } : null),
    }),
  };
}

// The conditions the guard waits on, taken from the module rather than retyped,
// so a test cannot pass against a condition the guard no longer uses.
const PANEL_EXPR = FLOATING_PANEL_WAIT_EXPR;
const READY_EXPR = DOCKVIEW_READY_WAIT_EXPR;

// The composer appends the witness JSON after `; witness `. Asserting the exact
// prefix is half the contract: the wait's own message, then the witness.
function witnessOf(failure, original) {
  const prefix = `${original.message}; witness `;
  if (!failure.message.startsWith(prefix)) {
    throw new Error(`failure message lost the original error: ${failure.message}`);
  }
  return JSON.parse(failure.message.slice(prefix.length));
}

const PANEL_TIMEOUT = new Error(`timed out after 15000ms (waited 15035ms) waiting for ${FLOATING_PANEL_LABEL}`);
const CONTEXT = {
  label: FLOATING_PANEL_LABEL,
  stage: "first measurement",
  openPaneMs: 14,
  failedPolls: 0,
  lastPollError: null,
};
const SETTINGS_PANE = { id: "pane_settings_2", type: "settings", slot: "main" };

beforeEach(() => {
  window.__shellGuardErrors = [];
});

afterEach(() => {
  Reflect.deleteProperty(window, "shellguardFloat");
  Reflect.deleteProperty(window, "__shellGuardErrors");
  Reflect.deleteProperty(document, "elementFromPoint");
});

describe("floating fixture panel-wait witness", () => {
  test("the predicates read the live api and store, not a fixed answer", () => {
    // The four states the two conditions can be in, read through the REAL
    // expression: a green page cannot look like the failing one.
    installFixture({ panes: [SETTINGS_PANE], panels: [], paneId: SETTINGS_PANE.id });
    let witness = evaluateInPage(floatingWitnessExpr());
    expect(witness.apiReady).toBe(true);
    expect(witness.panelPresent).toBe(false);

    installFixture({ panes: [SETTINGS_PANE], panels: [SETTINGS_PANE.id], paneId: SETTINGS_PANE.id });
    witness = evaluateInPage(floatingWitnessExpr());
    expect(witness.apiReady).toBe(true);
    expect(witness.panelPresent).toBe(true);

    installFixture({ panes: [SETTINGS_PANE], panels: [], paneId: SETTINGS_PANE.id });
    window.shellguardFloat.getDockviewApi = () => null;
    witness = evaluateInPage(floatingWitnessExpr());
    expect(witness.apiReady).toBe(false);
    expect(witness.panels).toBeNull();
    expect(witness.panelPresent).toBe(false);
  });

  test("the waited-on condition and options are the guard's own", async () => {
    installFixture({ panes: [SETTINGS_PANE], panels: [], paneId: SETTINGS_PANE.id });
    const driver = fakeDriver(pageSend());
    await expect(
      waitForFloatingState(driver, PANEL_EXPR, { label: FLOATING_PANEL_LABEL, stage: "first measurement" }),
    ).rejects.toThrow(/; witness /);
    expect(driver.waitPage).toHaveBeenCalledTimes(1);
    expect(driver.waitPage.mock.calls[0][0]).toBe(PANEL_EXPR);
    expect(driver.waitPage.mock.calls[0][1]).toEqual({ label: FLOATING_PANEL_LABEL });
  });

  test("a timed-out wait names the fixture, the pane and what the page held", async () => {
    // The CI shape: openPane published the record, dockview never gave it a
    // panel - which is exactly the state the bare timeout hid.
    installFixture({ panes: [SETTINGS_PANE], panels: [], paneId: SETTINGS_PANE.id, focusedPaneId: SETTINGS_PANE.id });
    const driver = fakeDriver(pageSend());
    const failure = await waitForFloatingState(driver, PANEL_EXPR, {
      label: FLOATING_PANEL_LABEL,
      stage: "first measurement",
      openPaneMs: 14,
    }).catch((error) => error);

    expect(failure).toBeInstanceOf(Error);
    expect(failure.cause).toBeInstanceOf(Error);
    expect(failure.cause.message).toContain(FLOATING_PANEL_LABEL);
    // The operator-facing message is pinned whole: the wait's own line, then
    // the witness. Nothing here is a substring of the text under test.
    expect(
      failure.message.startsWith(
        "timed out after 15000ms (waited 15035ms) waiting for real floating fixture's workspace panel; witness ",
      ),
    ).toBe(true);
    const witness = witnessOf(failure, PANEL_TIMEOUT);
    expect(witness.fixture).toBe(FLOATING_PANEL_LABEL);
    expect(witness.stage).toBe("first measurement");
    expect(witness.paneId).toBe("pane_settings_2");
    expect(witness.focusedPaneId).toBe("pane_settings_2");
    expect(witness.apiReady).toBe(true);
    expect(witness.paneRecordPresent).toBe(true);
    expect(witness.panelPresent).toBe(false);
    expect(witness.panes).toEqual([SETTINGS_PANE]);
    expect(witness.panels).toEqual([]);
    expect(witness.errors).toEqual([]);
    expect(witness.openPaneMs).toBe(14);
    expect(witness.failedPolls).toBe(0);
    expect(witness.unreadable).toEqual([]);
  });

  test("the Dockview-ready wait names its state too", async () => {
    installFixture({
      panes: [{ id: "pane_welcome_1", type: "welcome", slot: "main" }],
      panels: ["pane_welcome_1"],
      paneId: "pane_welcome_1",
    });
    const driver = fakeDriver(pageSend());
    const readyTimeout = new Error(`timed out after 15000ms (waited 15035ms) waiting for ${DOCKVIEW_READY_LABEL}`);
    const failure = await waitForFloatingState(driver, READY_EXPR, {
      label: DOCKVIEW_READY_LABEL,
      stage: "first measurement",
    }).catch((error) => error);

    expect(driver.waitPage.mock.calls[0][0]).toBe(READY_EXPR);
    expect(driver.waitPage.mock.calls[0][1]).toEqual({ label: DOCKVIEW_READY_LABEL });
    const witness = witnessOf(failure, readyTimeout);
    expect(witness.fixture).toBe(DOCKVIEW_READY_LABEL);
    expect(witness.apiReady).toBe(true);
    expect(witness.panels).toEqual(["pane_welcome_1"]);
  });

  test("the store dropping the pane it was asked to open is visible", async () => {
    // The other way the panel can be missing: the requested record is gone and
    // only the welcome pane survives - the re-mint shape the guard's own
    // comment worries about.
    installFixture({
      panes: [{ id: "pane_welcome_1", type: "welcome", slot: "main" }],
      panels: ["pane_welcome_1"],
      paneId: "pane_settings_2",
      focusedPaneId: "pane_welcome_1",
    });
    const failure = await describeFloatingFailure({ send: pageSend() }, CONTEXT, PANEL_TIMEOUT);

    const witness = witnessOf(failure, PANEL_TIMEOUT);
    expect(witness.paneRecordPresent).toBe(false);
    expect(witness.panelPresent).toBe(false);
    expect(witness.panes).toEqual([{ id: "pane_welcome_1", type: "welcome", slot: "main" }]);
    expect(witness.panels).toEqual(["pane_welcome_1"]);
  });

  test("a page that never answered is named, not hidden behind the timeout", async () => {
    // Driver.waitPage treats a rejected evaluate as "not held yet" and says
    // nothing about it, so a page that stopped answering used to be
    // indistinguishable from a live page whose state is absent.
    const send = vi.fn(async () => {
      throw new Error("Runtime.evaluate transport lost");
    });
    const driver = fakeDriver(send);
    const failure = await waitForFloatingState(driver, PANEL_EXPR, {
      label: FLOATING_PANEL_LABEL,
      stage: "first measurement",
      openPaneMs: 14,
    }).catch((error) => error);

    const witness = witnessOf(failure, PANEL_TIMEOUT);
    expect(witness.failedPolls).toBe(3);
    expect(witness.lastPollError).toContain("Runtime.evaluate transport lost");
    expect(witness.unreadable).toEqual(["witness"]);
  });

  test("a poll whose expression threw is counted, not reported as a healthy zero", async () => {
    // A page exception comes back as a SUCCESSFUL CDP response carrying
    // exceptionDetails, so a transport-only watcher counts nothing while no
    // predicate poll produced a reading.
    installFixture({ panes: [SETTINGS_PANE], panels: [], paneId: SETTINGS_PANE.id });
    const send = vi.fn(async () => ({
      result: {
        result: { type: "object" },
        exceptionDetails: { text: "ReferenceError: shellguardFloat is not defined" },
      },
    }));
    const driver = fakeDriver(send);
    const failure = await waitForFloatingState(driver, PANEL_EXPR, {
      label: FLOATING_PANEL_LABEL,
      stage: "first measurement",
    }).catch((error) => error);

    const witness = witnessOf(failure, PANEL_TIMEOUT);
    expect(witness.failedPolls).toBe(3);
    expect(witness.lastPollError).toContain("ReferenceError");
    expect(witness.unreadable).toEqual(["witness"]);
  });

  test("a witness read that never returns is bounded, not left to the CDP tripwire", async () => {
    // A page that stopped answering must not turn a named 15s failure into a
    // silent 30s one, so the witness read has its own short bound.
    vi.useFakeTimers();
    try {
      const send = vi.fn(() => new Promise(() => {}));
      const pending = describeFloatingFailure({ send }, CONTEXT, PANEL_TIMEOUT);
      await vi.advanceTimersByTimeAsync(WITNESS_READ_MS + 1);
      const witness = witnessOf(await pending, PANEL_TIMEOUT);
      expect(witness.unreadable).toEqual(["witness"]);
      expect(witness.error).toContain(`did not return within ${WITNESS_READ_MS}ms`);
    } finally {
      vi.useRealTimers();
    }
  });

  test("a stored layout is reported, which is what a restore would need", () => {
    installFixture({ panes: [SETTINGS_PANE], panels: [SETTINGS_PANE.id], paneId: SETTINGS_PANE.id });
    expect(evaluateInPage(floatingWitnessExpr()).savedLayout).toBe(false);
    localStorage.setItem("evener.workspace.layout.v2", "{}");
    try {
      expect(evaluateInPage(floatingWitnessExpr()).savedLayout).toBe(true);
    } finally {
      localStorage.removeItem("evener.workspace.layout.v2");
    }
  });

  test("full evidence when the fixture really holds the panel", () => {
    // The control that keeps the witness honest: it reports what is there, so
    // a green page cannot look like the failing one.
    installFixture({ panes: [SETTINGS_PANE], panels: [SETTINGS_PANE.id], paneId: SETTINGS_PANE.id });
    const witness = evaluateInPage(floatingWitnessExpr());
    expect(witness.unreadable).toEqual([]);
    expect(witness.apiReady).toBe(true);
    expect(witness.panelPresent).toBe(true);
    expect(witness.paneRecordPresent).toBe(true);
  });

  test("a torn-down page still returns the state instead of throwing", () => {
    // No window.shellguardFloat: every fixture read is unreadable, and the
    // witness says so rather than replacing the failure with its own TypeError.
    const witness = evaluateInPage(floatingWitnessExpr());
    expect(witness.paneId).toBeNull();
    expect(witness.unreadable).toEqual(
      expect.arrayContaining(["apiReady", "panes", "focusedPaneId", "paneRecordPresent", "panels", "panelPresent"]),
    );
    expect(witness.errors).toEqual([]);
  });

  test("a witness collection that cannot run at all is a note, never a replacement error", async () => {
    installFixture();
    const send = vi.fn(async () => {
      throw new Error("Runtime.evaluate transport lost");
    });
    const failure = await describeFloatingFailure({ send }, CONTEXT, PANEL_TIMEOUT);

    expect(failure).toBeInstanceOf(Error);
    expect(failure.cause).toBe(PANEL_TIMEOUT);
    const witness = witnessOf(failure, PANEL_TIMEOUT);
    expect(witness.unreadable).toEqual(["witness"]);
    expect(witness.error).toContain("Runtime.evaluate transport lost");
  });

  test("an unserializable witness keeps the host's own fields", async () => {
    const send = vi.fn(async () => ({ result: { result: { value: { unreadable: [], big: 1n } } } }));
    const failure = await describeFloatingFailure({ send }, CONTEXT, PANEL_TIMEOUT);

    expect(failure.cause).toBe(PANEL_TIMEOUT);
    const witness = witnessOf(failure, PANEL_TIMEOUT);
    expect(witness.unreadable).toEqual(["payload"]);
    expect(witness.fixture).toBe(FLOATING_PANEL_LABEL);
    expect(witness.openPaneMs).toBe(14);
    expect(witness.failedPolls).toBe(0);
  });
});
