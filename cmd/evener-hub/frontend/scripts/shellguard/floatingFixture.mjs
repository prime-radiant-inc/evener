// The floating fixture's readiness waits, and the state they name when they
// time out.
//
// `measureFloatingDock` (run.mjs) waits for the restored Dockview api, opens a
// real Settings pane, then waits for dockview to hold a panel for the id
// `openPane` returned. The panel wait timed out once in CI with nothing but its
// own label (issue #3747):
//
//   timed out after 15000ms (waited 15035ms) waiting for real floating
//   fixture's workspace panel
//
// It covers only the `openPane` -> `addPanel` hop, measured at single-digit
// milliseconds on a green run (openPane 10-14ms, panel 5-7ms), so a bare
// timeout here does NOT mean the budget was tight. `Driver.waitPage` also
// swallows a rejected `Runtime.evaluate` silently (skillguard/run.mjs), so the
// same message covers a page that stopped answering at all. `waitForFloatingState`
// therefore counts the polls that produced no reading and, when a wait fails,
// attaches what the page actually held: which fixture and stage, which pane id
// was asked for, whether the store kept that pane, whether the api held that
// panel, every pane and panel that WAS there, the page's own errors, the
// `openPane` step's duration, and whether a stored layout existed.
//
// The witness expression is a named export so a Node test can evaluate the REAL
// expression without a browser; `waitForFloatingState` and
// `describeFloatingFailure` are named exports so that test can drive the same
// functions run.mjs calls. Both follow sessionHoverCard.mjs's pattern for the
// hover-card witness (#3741).

import { evaluate } from "../browserGuardCdp.mjs";

// The waits' labels, shared by the wait and the witness so the fixture and step
// a recurrence names cannot drift from the ones that timed out.
export const DOCKVIEW_READY_LABEL = "real Dockview api ready";
export const FLOATING_PANEL_LABEL = "real floating fixture's workspace panel";

// The conditions the fixture waits on, exported so run.mjs and its test name the
// same expression: the wait cannot be pointed at a different condition than the
// one the witness explains.
export const DOCKVIEW_READY_WAIT_EXPR = "window.shellguardFloat.getDockviewApi() != null";
export const FLOATING_PANEL_WAIT_EXPR =
  "window.shellguardFloat.getDockviewApi()?.getPanel(window.shellguardFloat.paneId) != null";

// The witness read gets its own short bound: a page that stopped answering must
// not turn a named 15s failure into a silent 30s one (CDP_EVALUATE_TRIPWIRE_MS).
export const WITNESS_READ_MS = 2000;

// The saved-layout key DockHost writes. Reported as `savedLayout`: a stored
// layout is exactly what a restore would need, so its presence is the first
// thing to check against the re-mint hazard the guard's own comment names. It
// does NOT name the call site - the first measurement's own boot saves a layout
// too - which is why the call site is carried explicitly as `stage`.
const LAYOUT_STORAGE_KEY = "evener.workspace.layout.v2";

const messageOf = (error) => (error instanceof Error ? error.message : String(error));

// Reject after `ms` even if the promise never settles, so the named failure is
// never withheld by a page that stopped answering.
function bounded(promise, ms) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`witness read did not return within ${ms}ms`)), ms);
    timer.unref?.();
    promise.then(
      (value) => {
        clearTimeout(timer);
        resolve(value);
      },
      (error) => {
        clearTimeout(timer);
        reject(error);
      },
    );
  });
}

// The page expression that collects the floating fixture's state. It runs only
// after a wait already failed - exactly when `window.shellguardFloat`, the store
// or the api may be gone - so every read is guarded: a witness that threw here
// would replace the guard's real failure with its own TypeError and lose both
// the error and the diagnostic (the #3741 lesson). Whatever cannot be read is
// listed in `unreadable` instead.
export function floatingWitnessExpr() {
  return `(() => {
    const witness = { unreadable: [], errors: window.__shellGuardErrors || [] };
    const read = (name, fn) => {
      try { witness[name] = fn(); }
      catch (error) {
        witness.unreadable.push(name);
        witness[name] = { unreadable: String(error instanceof Error ? error.message : error) };
      }
    };
    const guard = window.shellguardFloat;
    const paneId = guard?.paneId ?? null;
    read('paneId', () => paneId);
    read('apiReady', () => guard.getDockviewApi() != null);
    read('panes', () => guard.workspaceStore.getState().panes.map(p => ({ id: p.id, type: p.type, slot: p.slot })));
    read('focusedPaneId', () => guard.workspaceStore.getState().focusedPaneId);
    read('paneRecordPresent', () => guard.workspaceStore.getState().panes.some(p => p.id === paneId));
    read('panels', () => guard.getDockviewApi()?.panels.map(p => p.id) ?? null);
    read('panelPresent', () => guard.getDockviewApi()?.getPanel(paneId) != null);
    read('savedLayout', () => localStorage.getItem(${JSON.stringify(LAYOUT_STORAGE_KEY)}) != null);
    read('pathname', () => location.pathname);
    return witness;
  })()`;
}

// Counts the polls that produced no reading while `driver`'s send is wrapped,
// and records the last failure. A poll produces no reading when its
// `Runtime.evaluate` REJECTED (the transport did not answer) or RESOLVED
// carrying `exceptionDetails` (the expression threw) - both are "not held yet"
// to `Driver.waitPage`, which says nothing about either, so without this a page
// that stopped answering is indistinguishable from a live page whose state is
// simply absent. The exception case is easy to miss: it is a SUCCESSFUL CDP
// response, so a transport-only catch counts nothing.
//
// A request that never returns at all is bounded by `evaluate`'s own 30s
// tripwire, which is longer than any of these waits, so it cannot be counted
// here; the witness's own bounded read reports that page as unreadable instead.
//
// Composable with the wrapper measureDelayedFloatingDock already installs: it
// calls through and restores exactly what it found.
export function watchEvaluateFailures(driver, expression) {
  const page = driver.page;
  const originalSend = typeof page?.send === "function" ? page.send : null;
  if (originalSend === null) {
    return { report: () => ({ failedPolls: 0, lastPollError: null }), restore: () => {} };
  }
  let failedPolls = 0;
  let lastPollError = null;
  const failed = (detail) => {
    failedPolls += 1;
    lastPollError = detail;
  };
  page.send = async (method, params) => {
    // Only the watched condition's polls count. `Driver.waitPage` evaluates a
    // toast expression of its own on timeout, and that read failing says
    // nothing about the predicate - it must not inflate the count or overwrite
    // the predicate's last error.
    const watched = method === "Runtime.evaluate" && params?.expression === expression;
    try {
      const response = await originalSend(method, params);
      if (watched && response?.result?.exceptionDetails) {
        failed(`page eval threw: ${JSON.stringify(response.result.exceptionDetails)}`);
      }
      return response;
    } catch (error) {
      if (watched) failed(messageOf(error));
      throw error;
    }
  };
  return {
    report: () => ({ failedPolls, lastPollError }),
    restore: () => {
      page.send = originalSend;
    },
  };
}

// Runs one floating-fixture readiness wait. On timeout it rethrows the wait's
// own message with the fixture's state attached, so the failure names what the
// page held instead of only how long it waited.
export async function waitForFloatingState(driver, expression, { label, stage = null, openPaneMs = null } = {}) {
  const watch = watchEvaluateFailures(driver, expression);
  let failure = null;
  try {
    return await driver.waitPage(expression, { label });
  } catch (error) {
    failure = error;
  } finally {
    // The witness must read the page unwrapped: its own failed read is already
    // reported by `unreadable`, and counting it as a poll would inflate the
    // signal the wrapper exists to give.
    watch.restore();
  }
  throw await describeFloatingFailure(driver, { label, stage, openPaneMs, ...watch.report() }, failure);
}

// The error the guard throws when a floating-fixture wait times out: the wait's
// own message with the witness and the `openPane` step's duration attached.
// Collecting the witness must never replace the failure it describes, so a
// rejected or bounded-out evaluation, or an unserializable payload, becomes a
// note and the original error (and its cause) is still what the operator sees.
export async function describeFloatingFailure(driver, context, error) {
  const host = {
    fixture: context.label,
    stage: context.stage,
    openPaneMs: context.openPaneMs,
    failedPolls: context.failedPolls,
    lastPollError: context.lastPollError,
  };
  let witness;
  try {
    witness = await bounded(evaluate(driver.send, floatingWitnessExpr()), WITNESS_READ_MS);
  } catch (witnessError) {
    witness = { unreadable: ["witness"], error: messageOf(witnessError) };
  }
  let body;
  try {
    body = JSON.stringify({ ...host, ...witness });
  } catch {
    // The page's own fields are what could not be serialized; the host's always
    // can be, so they are never dropped with them.
    body = JSON.stringify({ ...host, unreadable: ["payload"], error: "witness not serializable" });
  }
  return new Error(`${messageOf(error)}; witness ${body}`, { cause: error });
}
