import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { evaluate, navigateTo } from "../browserGuardCdp.mjs";
import { Driver } from "../skillguard/run.mjs";
import { installRetryWireFault } from "./wireFault.mjs";

const fixture = JSON.parse(readFileSync(0, "utf8"));
const driver = new Driver(fixture);
const q = JSON.stringify;

// The continuation sentinel the shared client submits for Retry. The journey
// asserts the wire payload structurally, not the button's copy.
const CONTINUE = "continue";

// Blocker for durable pending projection reads: IndexedDB's getAll success
// callbacks are parked until the driver releases them, so the page's durable
// state owner is genuinely unknown rather than merely slow. This is a browser
// storage boundary, not Evener logic: no record is fabricated or rewritten.
function installDurableReadHold() {
  const state = { armed: false, held: [], count: 0 };
  globalThis.__retryStorage = state;
  try {
    state.armed = globalThis.localStorage?.getItem("__retryStorageArmed") === "1";
    globalThis.localStorage?.removeItem("__retryStorageArmed");
  } catch {
    // No storage means nothing to arm.
  }
  state.release = () => {
    state.armed = false;
    for (const deliver of state.held.splice(0)) deliver();
  };
  const originalGetAll = IDBObjectStore.prototype.getAll;
  IDBObjectStore.prototype.getAll = function getAll(...args) {
    const request = originalGetAll.apply(this, args);
    if (!state.armed) return request;
    state.count++;
    return new Proxy(request, {
      get(target, property) {
        if (property === "addEventListener") {
          return (type, listener, options) => {
            if (type !== "success") return target.addEventListener(type, listener, options);
            return target.addEventListener(
              "success",
              (event) => {
                if (state.armed) state.held.push(() => listener.call(target, event));
                else listener.call(target, event);
              },
              options,
            );
          };
        }
        const value = Reflect.get(target, property, target);
        return typeof value === "function" ? value.bind(target) : value;
      },
    });
  };
}

// Records whether the shipped Retry action ever reported itself enabled after
// the reload. A gap the driver only sampled could be missed, so the page's own
// mutation observer is the oracle: "stays inactive until the durable state
// resolves" means this never became true.
function installRetryEnabledWatch() {
  const state = { sawEnabled: false, samples: 0 };
  globalThis.__retryWatch = state;
  const label = (b) => ((b.textContent ?? "").trim()) || ((b.getAttribute("aria-label") ?? "").trim());
  const scan = () => {
    state.samples++;
    for (const b of document.querySelectorAll("[data-testid='turn-failure'] button")) {
      const name = label(b);
      if (name !== "Retry" && name !== "Reconnect & retry") continue;
      if (!b.disabled && !b.matches(':disabled, [aria-disabled="true"]')) state.sawEnabled = true;
    }
  };
  new MutationObserver(scan).observe(document.documentElement, {
    subtree: true,
    childList: true,
    attributes: true,
    attributeFilter: ["disabled", "aria-disabled"],
  });
  scan();
}

function retryButtonsExpr() {
  return `[...document.querySelectorAll("[data-testid='turn-failure'] button")].filter((b) => {
    const label = ((b.textContent ?? "").trim()) || ((b.getAttribute("aria-label") ?? "").trim());
    return label === "Retry" || label === "Reconnect & retry";
  })`;
}

function failureCapsExpr() {
  return `[...document.querySelectorAll("[data-testid='turn-failure']")].map((cap) => ({
    text: cap.textContent.slice(0, 240),
    buttons: [...cap.querySelectorAll("button")].map((b) => ({
      label: ((b.textContent ?? "").trim()) || ((b.getAttribute("aria-label") ?? "").trim()),
      disabled: b.disabled,
    })),
  }))`;
}

// One page expression for every Retry activation: find the latest failure's
// action, refuse an absent or disabled control, and click it `times` in the
// same task.
function retryClickExpr(times) {
  return `(() => { const buttons = ${retryButtonsExpr()}; const b = buttons[buttons.length - 1];
    if (!b) return "missing";
    if (b.matches(':disabled, [aria-disabled="true"]')) return "disabled";
    ${Array.from({ length: times }, () => "b.click();").join(" ")}
    return "clicked"; })()`;
}

async function waitRetry(driver, count, { enabled = true, label, timeoutMs = 20000 } = {}) {
  await driver.waitPage(
    `(() => { const buttons = ${retryButtonsExpr()};
      return buttons.length === ${count} && buttons.every((b) => b.disabled === ${!enabled}) ? true : null; })()`,
    { timeoutMs, label: label ?? `Retry buttons=${count} enabled=${enabled}` },
  );
}

async function clickRetry(driver, { times = 1 } = {}) {
  const result = await evaluate(driver.send, retryClickExpr(times));
  if (result !== "clicked") throw new Error(`clickRetry: ${result}`);
}

// Readiness is owner-backed and a projection refresh can unload it for a
// moment. Press again only after the action reports enabled again; a disabled
// or missing control is never treated as a successful press.
async function clickRetryWhenReady(driver, { times = 1, timeoutMs = 20000 } = {}) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const result = await evaluate(driver.send, retryClickExpr(times));
    if (result === "clicked") return;
    if (Date.now() > deadline) throw new Error(`clickRetryWhenReady: ${result}`);
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
}

async function waitDurable(driver, ref, predicate, label, timeoutMs = 20000) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const durable = await evaluate(driver.send, driver.durableRecordsExpr());
    const rows = (durable.outbox ?? []).filter((record) => record.targetRef === ref);
    if (predicate(rows)) return { durable, rows };
    if (Date.now() > deadline) {
      throw new Error(`timeout waiting for ${label}; outbox ${JSON.stringify(rows)}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
}

// Settlement is an owner read, not the same tick as the output. Wait until the
// ref has left both the outbox and the optimistic projection, then return the
// records the observed state was read from.
async function waitDurableSettled(driver, ref, timeoutMs = 20000) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const durable = await evaluate(driver.send, driver.durableRecordsExpr());
    const unresolved = (durable.outbox ?? []).some((record) => record.targetRef === ref);
    const optimistic = (durable.optimistic ?? []).some((record) => record.targetRef === ref);
    if (!unresolved && !optimistic) return durable;
    if (Date.now() > deadline) {
      throw new Error(`timeout waiting for ${ref} to settle; durable ${JSON.stringify(durable)}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
}

async function waitBodyIncludes(driver, text, options = {}) {
  await driver.waitPage(`document.body.innerText.includes(${q(text)}) ? true : null`, {
    label: `body contains ${JSON.stringify(text)}`,
    ...options,
  });
}

function wireRequests(driver) {
  return evaluate(driver.send, "window.__retryWire.requests");
}

// A new readonly connection sees committed durable state, not the pending UI
// projection. Abort creation so an absent database cannot masquerade as empty.
async function readDurableRetry(clientMutationId) {
  const database = await new Promise((resolve, reject) => {
    const request = indexedDB.open("evener-mutation-outbox");
    request.onupgradeneeded = () => request.transaction.abort();
    request.onerror = () => reject(request.error);
    request.onsuccess = () => resolve(request.result);
  });
  try {
    const stores = ["outbox", "optimistic", "recovery"];
    const transaction = database.transaction(stores, "readonly");
    const rows = {};
    await new Promise((resolve, reject) => {
      transaction.oncomplete = resolve;
      transaction.onabort = () => reject(transaction.error);
      transaction.onerror = () => reject(transaction.error);
      for (const store of stores) {
        const request = transaction.objectStore(store).get(clientMutationId);
        request.onsuccess = () => {
          const record = request.result;
          rows[store] = record ? { clientMutationId: record.clientMutationId, state: record.state } : null;
        };
      }
    });
    return rows;
  } finally {
    database.close();
  }
}

async function runLegacy(driver, scenario) {
  await driver.openSession(scenario.ref);
  const editor = driver.editorExpr(scenario.ref);
  await driver.waitPage(`document.querySelector('[data-testid="turn-failure"]') !== null`, {
    label: "terminal provider failure",
  });
  // Eligibility has an owner-backed readiness gate: an unknown pending read is
  // not an empty one, so the action can be visible before it may act.
  await waitRetry(driver, 1, { enabled: true, label: "eligible Retry enabled" });
  await evaluate(driver.send, `${editor}.focus()`);
  await driver.typeText(scenario.ref, "RETRY_UNSENT_DRAFT");
  await evaluate(driver.send, `Object.assign(window.__retryWire, {mode:${q(scenario.mode)}, armed:true, requests:[]})`);
  await clickRetryWhenReady(driver, { times: 1 });
  await driver.waitPage("window.__retryWire.requests.length > 0", { label: "Retry reached transport" });
  assert.deepEqual(await evaluate(driver.send, "window.__retryWire.requests[0].params.input"), [
    { type: "text", text: CONTINUE },
  ]);
  assert.equal(await evaluate(driver.send, `${editor}.textContent`), "RETRY_UNSENT_DRAFT");
  if (scenario.mode === "refused") {
    await driver.waitPage(`document.querySelector('button[aria-label="Edit message"]') !== null`, {
      label: "refused continuation recovery",
    });
    assert.equal(await evaluate(driver.send, `${editor}.textContent`), "RETRY_UNSENT_DRAFT");
    await driver.selectAll(scenario.ref);
    await driver.press(scenario.ref, "Backspace");
    await driver.waitPage(`${editor}.textContent === ""`, { label: "explicit draft clear" });
    await driver.clickByText("Edit message");
    await driver.waitPage(`${editor}.textContent === "continue"`, { label: "explicit recovery loaded continuation" });
    await driver.clickByText("Send");
  }
  await waitBodyIncludes(driver, `RETRY_DONE_${scenario.mode}`);
  assert.equal(
    await evaluate(driver.send, `${editor}.textContent`),
    scenario.mode === "refused" ? "" : "RETRY_UNSENT_DRAFT",
  );
  if (scenario.mode === "lost-reply") {
    await driver.waitPage("window.__retryWire.connections >= 2", { label: "transport reconnected" });
  }
  const wire = await evaluate(driver.send, "window.__retryWire");
  for (const request of wire.requests) {
    assert.deepEqual(request.params.input, [{ type: "text", text: CONTINUE }]);
    assert.equal(typeof request.params.clientMutationId, "string");
    assert.notEqual(request.params.clientMutationId, "");
  }
  if (scenario.mode === "lost-reply") {
    assert.equal(wire.dropped, 1);
    assert.equal(new Set(wire.requests.map((request) => request.params.clientMutationId)).size, 1);
    // Main's durable-settlement proof, kept on the one-owner journey: rebuild
    // the production client without clearing IndexedDB or the draft, then read
    // the committed stores over a separate connection.
    const clientMutationId = wire.requests[0].params.clientMutationId;
    console.log("retryguard: continued output observed before durable settlement check", { clientMutationId });
    await navigateTo(driver.page, await evaluate(driver.send, "location.href"));
    await driver.openSession(scenario.ref);
    await driver.waitPage(`window.__retryWire.reads.some((read) => read.ref === ${q(scenario.ref)} && read.subscribed)`, {
      label: "reopened client read the session",
    });
    await waitBodyIncludes(driver, `RETRY_DONE_${scenario.mode}`, { label: "continued output after reopen" });
    await driver.waitPage(`${editor}.textContent === "RETRY_UNSENT_DRAFT"`, { label: "draft restored after reopen" });
    const durableRead = `(${readDurableRetry.toString()})(${q(clientMutationId)})`;
    try {
      await driver.waitPage(`${durableRead}.then((rows) => Object.values(rows).every((row) => row === null))`, {
        label: `durable Retry settlement for ${clientMutationId}`,
      });
    } catch (error) {
      assert.deepEqual(
        await evaluate(driver.send, durableRead),
        { outbox: null, optimistic: null, recovery: null },
        `unsettled durable Retry ${clientMutationId}`,
      );
      throw error;
    }
    assert.deepEqual(await evaluate(driver.send, durableRead), {
      outbox: null,
      optimistic: null,
      recovery: null,
    });
    assert.deepEqual(await evaluate(driver.send, "window.__retryWire.requests"), [], "reopen must not resend Retry");
    assert.equal(await evaluate(driver.send, `${editor}.textContent`), "RETRY_UNSENT_DRAFT");
    console.log("retryguard: durable Retry settled after reopen", { clientMutationId });
  } else {
    assert.equal(wire.requests.length, scenario.mode === "refused" ? 2 : 1);
  }
  driver.milestone(scenario.mode, { requests: wire.requests.length });
}

// Repeated press while the first acceptance reply is withheld. The durable
// outbox row makes the action inactive; a second press must not add a wire
// submission or a second mutation identity. The new-turn events are withheld
// too, so the failure the action belongs to stays the rendered tail while the
// press is repeated.
async function runRepeatedPress(driver, scenario) {
  const ref = scenario.ref;
  const editor = driver.editorExpr(ref);
  await driver.openSession(ref);
  await driver.waitPage(`document.querySelector('[data-testid="turn-failure"]') !== null`, {
    label: "terminal provider failure",
  });
  await waitRetry(driver, 1, { enabled: true, label: "eligible Retry enabled" });
  await evaluate(driver.send, `${editor}.focus()`);
  await driver.typeText(ref, "RETRY_UNSENT_DRAFT");

  await evaluate(
    driver.send,
    `(() => { const wire = window.__retryWire; wire.clearHolds(); wire.requests.length = 0;
      wire.holdResponses("turn/start", "turn/queue");
      wire.holdNotifications(
        "thread/started", "thread/status/changed", "history/updated",
        "overlay/upserted", "overlay/delta", "overlay/reset", "overlay/end",
      );
      return true; })()`,
  );
  await clickRetryWhenReady(driver, { times: 1 });
  await driver.waitPage("window.__retryWire.requests.length === 1", {
    label: "one transport submission for the first press",
  });
  const { rows } = await waitDurable(driver, ref, (rows) => rows.length === 1, "one durable outbox row");
  assert.equal(rows[0].method, "turn/start", "durable submission method");
  assert.deepEqual(rows[0].input, [{ type: "text", text: CONTINUE }], "durable continuation payload");
  const mutationID = rows[0].clientMutationId;
  assert.equal(typeof mutationID, "string");
  assert.notEqual(mutationID, "");

  await waitRetry(driver, 1, { enabled: false, label: "Retry disabled while the acceptance is withheld" });
  const second = await evaluate(
    driver.send,
    `(() => { const buttons = ${retryButtonsExpr()}; const b = buttons[0];
      if (!b) return "missing";
      if (b.matches(':disabled, [aria-disabled="true"]')) { b.click(); return "disabled"; }
      b.click(); return "clicked"; })()`,
  );
  assert.equal(second, "disabled", "the repeated press must land on an inactive action");
  const afterSecond = await wireRequests(driver);
  assert.equal(afterSecond.length, 1, "the repeated press dispatched nothing");
  const durableAfterSecond = await evaluate(driver.send, driver.durableRecordsExpr());
  assert.equal(
    (durableAfterSecond.outbox ?? []).filter((record) => record.targetRef === ref).length,
    1,
    "the repeated press added no durable row",
  );
  driver.milestone("retry-held", { clientMutationId: mutationID });

  await evaluate(driver.send, `window.__retryWire.releaseResponses("turn/start", "turn/queue")`);
  await evaluate(driver.send, "window.__retryWire.releaseNotifications()");
  await waitBodyIncludes(driver, "RETRY_DONE_REPEATED_PRESS", { timeoutMs: 30000 });
  await waitRetry(driver, 0, { label: "the failure has no Retry after the continuation runs" });
  assert.equal(await evaluate(driver.send, `${editor}.textContent`), "RETRY_UNSENT_DRAFT", "draft survived");

  const wire = await evaluate(driver.send, "window.__retryWire");
  const turnStarts = wire.requests.filter((request) => request.method === "turn/start");
  assert.equal(turnStarts.length, 1, "exactly one wire turn/start");
  assert.equal(new Set(wire.requests.map((request) => request.params.clientMutationId)).size, 1);
  assert.equal(wire.requests[0].params.clientMutationId, mutationID, "wire and durable identity agree");
  driver.milestone("repeated-press-complete", { requests: wire.requests.length });
}

// Latest-only: the older failure loses its action as soon as a newer running
// turn exists (before that turn renders any display item), stays gone through
// a successful turn, and the newer failed turn offers its own action. A stale
// activation of the older action submits nothing.
async function runLatestOnly(driver, scenario) {
  const ref = scenario.ref;
  await driver.openSession(ref);
  await driver.waitPage(`document.querySelector('[data-testid="turn-failure"]') !== null`, {
    label: "terminal provider failure",
  });
  await waitRetry(driver, 1, { enabled: true, label: "eligible latest Retry enabled" });
  // Retain the exact shipped control the press below would revisit after the
  // model advances.
  await evaluate(
    driver.send,
    `(() => { window.__retryStale = ${retryButtonsExpr()}[0] || null; return window.__retryStale !== null; })()`,
  );

  await evaluate(
    driver.send,
    `(() => { const wire = window.__retryWire; wire.clearHolds(); wire.requests.length = 0; wire.dropped = 0;
      wire.holdResponses("thread/read");
      wire.holdNotifications("history/updated", "overlay/upserted", "overlay/delta", "overlay/reset", "overlay/end");
      return true; })()`,
  );
  await evaluate(driver.send, `${driver.editorExpr(ref)}.focus()`);
  await driver.typeText(ref, "RETRY_RUNNING_NEW");
  const runningDraft = await driver.composerState(ref);
  await driver.clickSubmit(ref, { text: runningDraft.text, chips: [], tiles: 0 });
  // The running identity arrives over thread/started and thread/status/changed;
  // history and live overlays are withheld, so the newer turn has zero display
  // items when the older action must already be gone.
  await driver.waitPage(
    `(() => { const composer = ${driver.composerStateExpr(ref)};
      const buttons = ${retryButtonsExpr()};
      const rows = [...(${driver.paneScopeExpr(ref)} ?? document).querySelectorAll("[data-testid='transcript-row']")];
      return composer && composer.steerVisible && buttons.length === 0 &&
        rows.every((row) => !row.textContent.includes("RETRY_RUNNING_NEW")) ? true : null; })()`,
    { label: "newer running turn hides the older Retry before its items" },
  );
  driver.milestone("running-empty", {});

  await evaluate(driver.send, 'window.__retryWire.releaseResponses("thread/read")');
  await evaluate(driver.send, "window.__retryWire.releaseNotifications()");
  await waitBodyIncludes(driver, "RETRY_RUNNING_DONE", { timeoutMs: 30000 });
  await waitRetry(driver, 0, { label: "completed newer turn leaves the older failure without Retry" });
  const capsAfterSuccess = await evaluate(driver.send, failureCapsExpr());
  assert.ok(capsAfterSuccess.length >= 1, "the older failure stays readable");
  // Wait for the composer to report the session idle before submitting again.
  await driver.waitPage(
    `(() => { const composer = ${driver.composerStateExpr(ref)}; return composer && composer.steerVisible === false ? true : null; })()`,
    { label: "session idle after the successful newer turn" },
  );

  await evaluate(driver.send, `${driver.editorExpr(ref)}.focus()`);
  await driver.typeText(ref, "RETRY_NEWER_FAIL");
  const failingDraft = await driver.composerState(ref);
  await driver.clickSubmit(ref, { text: failingDraft.text, chips: [], tiles: 0 });
  await driver.waitPage(
    `(() => ${failureCapsExpr()}.length >= 2 ? true : null)()`,
    { label: "newer failed turn rendered" },
  );
  await waitRetry(driver, 1, { enabled: true, label: "newer failure offers its own Retry" });
  const caps = await evaluate(driver.send, failureCapsExpr());
  const newestCap = caps.find((cap) => cap.text.includes("retry_fixture_newer"));
  assert.ok(newestCap, `the newer failure is readable: ${JSON.stringify(caps)}`);
  assert.ok(
    newestCap.buttons.some((button) => button.label === "Retry"),
    `the newer failure offers its own Retry: ${JSON.stringify(caps)}`,
  );
  const withActions = caps.filter((cap) => cap.buttons.some((button) => button.label === "Retry"));
  assert.equal(withActions.length, 1, `only the latest failure offers Retry: ${JSON.stringify(caps)}`);
  assert.equal(withActions[0], newestCap, `the action belongs to the newer failure: ${JSON.stringify(caps)}`);
  driver.milestone("newest-failure-ready", {});

  // A retained reference to the older action is detached from the document, so
  // the shipped activation path has no control to run; pressing it submits
  // nothing even though the newer failure's action exists.
  const stale = await evaluate(driver.send, `(() => ({ connected: window.__retryStale?.isConnected ?? null }))()`);
  assert.equal(stale.connected, false, "the older Retry control left the document");
  const beforeStale = (await wireRequests(driver)).length;
  await evaluate(driver.send, `window.__retryStale?.click()`);
  assert.equal((await wireRequests(driver)).length, beforeStale, "a stale activation submitted nothing");

  await clickRetryWhenReady(driver, { times: 1 });
  await waitBodyIncludes(driver, "RETRY_NEWEST_DONE", { timeoutMs: 30000 });
  await waitRetry(driver, 0, { label: "the newest retry settled with no remaining action" });
  const wire = await evaluate(driver.send, "window.__retryWire");
  assert.equal(
    new Set(wire.requests.map((request) => request.params.clientMutationId)).size,
    wire.requests.length,
    "each ordinary submission and the retry have distinct identities",
  );
  driver.milestone("latest-only-complete", { requests: wire.requests.length });
}

// Reload with an unresolved durable row left by a lost acceptance reply. The
// recovery owner resubmits the one identity; Retry may never report itself
// enabled while the row is unresolved, and the draft survives the reload.
async function runReloadUnresolved(driver, scenario) {
  const ref = scenario.ref;
  const editor = driver.editorExpr(ref);
  await driver.openSession(ref);
  await driver.waitPage(`document.querySelector('[data-testid="turn-failure"]') !== null`, {
    label: "terminal provider failure",
  });
  await waitRetry(driver, 1, { enabled: true, label: "eligible Retry enabled" });
  await evaluate(driver.send, `${editor}.focus()`);
  await driver.typeText(ref, "RETRY_UNSENT_DRAFT");

  await evaluate(
    driver.send,
    `Object.assign(window.__retryWire, {mode: "lost-reply", armed: true, requests: [], dropped: 0, responseHolds: [], notificationHolds: []})`,
  );
  await clickRetryWhenReady(driver, { times: 1 });
  await driver.waitPage("window.__retryWire.dropped === 1", { label: "acceptance reply dropped" });
  const { rows } = await waitDurable(driver, ref, (rows) => rows.length === 1, "the durable lost-reply row");
  const mutationID = rows[0].clientMutationId;
  assert.deepEqual(rows[0].input, [{ type: "text", text: CONTINUE }], "durable continuation payload");
  driver.milestone("acceptance-dropped", { clientMutationId: mutationID });

  // Hard reload into a fresh document with its pending read withheld, so the
  // unresolved window is observable rather than settled by the first hydrate.
  await evaluate(driver.send, `localStorage.setItem("__retryStorageArmed", "1")`);
  await navigateTo(driver.page, driver.url);
  await evaluate(driver.send, `(${installRetryEnabledWatch.toString()})()`);
  await driver.openSession(ref);
  await driver.waitPage("window.__retryStorage.count > 0", { label: "durable pending read withheld after reload" });
  await driver.waitPage(`(() => ${failureCapsExpr()}.length >= 1 ? true : null)()`, {
    label: "failed turn readable after reload",
    timeoutMs: 30000,
  });
  assert.equal(
    await evaluate(driver.send, `${editor}.textContent`),
    "RETRY_UNSENT_DRAFT",
    "draft survived the reload",
  );
  const attemptsBefore = (await wireRequests(driver)).length;
  const attempted = await evaluate(driver.send, retryClickExpr(1));
  assert.ok(attempted === "missing" || attempted === "disabled", `Retry was actionable after reload: ${attempted}`);
  assert.equal((await wireRequests(driver)).length, attemptsBefore, "a press before settlement submitted nothing");
  driver.milestone("reload-unresolved", { clientMutationId: mutationID });

  await evaluate(driver.send, "window.__retryStorage.release()");
  await waitBodyIncludes(driver, "RETRY_RECOVERED", { timeoutMs: 45000 });
  await waitRetry(driver, 0, { label: "the recovered conversation offers no stale Retry" });
  const watch = await evaluate(driver.send, "window.__retryWatch");
  assert.equal(watch.sawEnabled, false, "Retry never became enabled while the durable row was unresolved");
  const after = await evaluate(driver.send, `${editor}.textContent`);
  assert.equal(after, "RETRY_UNSENT_DRAFT", "draft preserved through recovery");
  await waitDurableSettled(driver, ref);
  const durable = await evaluate(driver.send, driver.durableRecordsExpr());
  for (const store of ["outbox", "optimistic", "recovery"]) {
    assert.equal(
      (durable[store] ?? []).filter((record) => record.clientMutationId === mutationID).length,
      0,
      `the recovered identity left ${store}`,
    );
  }
  const wire = await evaluate(driver.send, "window.__retryWire");
  for (const request of wire.requests) {
    assert.equal(request.params.clientMutationId, mutationID, "recovery reused the one mutation identity");
  }
  driver.milestone("reload-recovered", { requests: wire.requests.length });
}

// Delayed pending read on remount: Retry is unknown, not empty, until the
// durable read answers. A press in that window submits nothing; after the read
// establishes a settled empty state the action becomes active and sends one
// continuation.
async function runDelayedPending(driver, scenario) {
  const ref = scenario.ref;
  const editor = driver.editorExpr(ref);
  await driver.openSession(ref);
  await driver.waitPage(`document.querySelector('[data-testid="turn-failure"]') !== null`, {
    label: "terminal provider failure",
  });
  await waitRetry(driver, 1, { enabled: true, label: "eligible Retry enabled" });
  await evaluate(driver.send, `${editor}.focus()`);
  await driver.typeText(ref, "RETRY_UNSENT_DRAFT");

  await evaluate(driver.send, `localStorage.setItem("__retryStorageArmed", "1")`);
  await navigateTo(driver.page, driver.url);
  await driver.openSession(ref);
  await driver.waitPage("window.__retryStorage.count > 0", { label: "durable pending read withheld" });
  await driver.waitPage(
    `(() => { const caps = ${failureCapsExpr()}; const buttons = ${retryButtonsExpr()};
      return caps.length > 0 && buttons.length === 1 && buttons.every((b) => b.disabled) ? true : null; })()`,
    { label: "Retry inactive while the pending read is unknown", timeoutMs: 30000 },
  );
  const attempted = await evaluate(driver.send, retryClickExpr(1));
  assert.equal(attempted, "disabled", "the unknown-pending action must be inactive");
  assert.equal((await wireRequests(driver)).length, 0, "a press before the pending read submitted nothing");
  driver.milestone("pending-read-held", {});

  await evaluate(driver.send, "window.__retryStorage.release()");
  await waitRetry(driver, 1, { enabled: true, label: "Retry active once the read establishes empty state" });
  await waitDurable(driver, ref, (rows) => rows.length === 0, "settled empty durable outbox");
  await clickRetryWhenReady(driver, { times: 1 });
  await driver.waitPage("window.__retryWire.requests.length === 1", { label: "one continuation dispatched" });
  await waitBodyIncludes(driver, "RETRY_DONE_DELAYED_PENDING", { timeoutMs: 30000 });
  await waitRetry(driver, 0, { label: "the failure has no Retry after the continuation runs" });
  assert.equal(await evaluate(driver.send, `${editor}.textContent`), "RETRY_UNSENT_DRAFT", "draft survived");
  const wire = await evaluate(driver.send, "window.__retryWire");
  assert.equal(wire.requests.length, 1, "exactly one wire submission");
  assert.deepEqual(wire.requests[0].params.input, [{ type: "text", text: CONTINUE }]);
  driver.milestone("delayed-pending-complete", { requests: wire.requests.length });
}

try {
  await driver.start();
  await driver.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `(${installRetryWireFault.toString()})()`,
  });
  await driver.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `(${installDurableReadHold.toString()})()`,
  });
  await navigateTo(driver.page, fixture.url);
  for (const scenario of fixture.scenarios) {
    if (scenario.mode === "repeated-press") await runRepeatedPress(driver, scenario);
    else if (scenario.mode === "latest-only") await runLatestOnly(driver, scenario);
    else if (scenario.mode === "reload-unresolved") await runReloadUnresolved(driver, scenario);
    else if (scenario.mode === "delayed-pending") await runDelayedPending(driver, scenario);
    else await runLegacy(driver, scenario);
  }
} catch (error) {
  console.error(
    "retryguard wire:",
    JSON.stringify(
      await evaluate(
        driver.send,
        `(() => { const wire = window.__retryWire; return wire === undefined ? null : {
          responseHolds: wire.responseHolds, notificationHolds: wire.notificationHolds,
          heldResponses: wire.heldResponses.map((held) => held.method),
          heldNotifications: wire.heldNotifications.map((held) => held.method),
          methodCounts: wire.methodCounts, requests: wire.requests.length,
          connections: wire.connections, dropped: wire.dropped, failed: wire.failed }; })()`,
      ).catch(() => "unavailable"),
    ),
  );
  console.error(
    "retryguard failures:",
    JSON.stringify(await evaluate(driver.send, failureCapsExpr()).catch(() => "unavailable")),
  );
  console.error(await evaluate(driver.send, "document.body.innerText"));
  throw error;
} finally {
  await driver.stop();
}
