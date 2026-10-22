import assert from "node:assert/strict";
import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { evaluate, navigateTo } from "../browserGuardCdp.mjs";
import { Driver } from "../skillguard/run.mjs";
import { installRetryWireFault } from "./wireFault.mjs";

const fixture = JSON.parse(readFileSync(0, "utf8"));
const driver = new Driver(fixture);
const q = JSON.stringify;

// The continuation sentinel the shared client submits for Retry. The journey
// asserts the wire payload structurally, not the button's copy.
const CONTINUE = "continue";

// Blocker for pending projection reads: IndexedDB's getAll success callbacks
// are parked until the driver releases them, so the page's durable-state owner
// is genuinely unknown rather than merely slow. This is a browser storage
// boundary, not Evener logic: no record is fabricated or rewritten.
function installDurableReadHold() {
  const state = { armed: false, held: [], count: 0, writeArmed: false, writeReached: 0, heldWrites: [] };
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
  state.holdNextOutboxWrite = () => {
    state.writeArmed = true;
  };
  state.releaseWrite = () => {
    state.writeArmed = false;
    for (const deliver of state.heldWrites.splice(0)) deliver();
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
  // One-shot: the next readwrite transaction that includes the outbox store
  // commits, but its completion callback is parked until the driver releases
  // it. The durable row exists while every caller still awaits the enqueue.
  const originalTransaction = IDBDatabase.prototype.transaction;
  IDBDatabase.prototype.transaction = function transaction(storeNames, mode, ...rest) {
    const tx = originalTransaction.call(this, storeNames, mode, ...rest);
    if (!state.writeArmed || mode !== "readwrite") return tx;
    const names = typeof storeNames === "string" ? [storeNames] : Array.from(storeNames);
    if (!names.includes("outbox")) return tx;
    state.writeArmed = false;
    state.writeReached++;
    return new Proxy(tx, {
      get(target, property) {
        if (property === "addEventListener") {
          return (type, listener, options) => {
            if (type !== "complete") return target.addEventListener(type, listener, options);
            return target.addEventListener(
              "complete",
              (event) => state.heldWrites.push(() => listener.call(target, event)),
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
    if (b.disabled) return "disabled";
    ${Array.from({ length: times }, () => "b.click();").join(" ")}
    return "clicked"; })()`;
}

async function waitRetry(driver, count, { enabled = true, label } = {}) {
  await driver.waitPage(
    `(() => { const buttons = ${retryButtonsExpr()};
      return buttons.length === ${count} && buttons.every((b) => b.disabled === ${!enabled}) ? true : null; })()`,
    { label: label ?? `Retry buttons=${count} enabled=${enabled}` },
  );
}

// Press the latest failure's action in the page. Two presses in one task are
// the pre-render window: the first click's admission runs synchronously, so
// the second is a stale activation the owner must refuse.
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

// The enabled check and the click are not one transaction: a publication can
// advance Retry's admission between the DOM snapshot and the React handler.
// Allow one fresh press after a bounded no-wire window. A second refusal still
// fails the journey, so a genuinely closed control is never reported as sent.
async function pressRetryUntilTransport(driver) {
  await clickRetryWhenReady(driver, { times: 1 });
  try {
    await driver.waitPage("window.__retryWire.requests.length > 0", {
      label: "Retry reached transport",
      timeoutMs: 1500,
    });
    return;
  } catch {
    // Fall through to one enabled re-press.
  }
  await clickRetryWhenReady(driver, { times: 1, timeoutMs: 5000 });
  await driver.waitPage("window.__retryWire.requests.length > 0", {
    label: "Retry reached transport after one enabled re-press",
    timeoutMs: 5000,
  });
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

async function stageAttachment(driver, ref, artifactDir) {
  const filePath = path.join(artifactDir, "retryguard-fixture.png");
  writeFileSync(
    filePath,
    Buffer.from(
      "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
      "base64",
    ),
  );
  const document = await driver.send("DOM.getDocument", { depth: 0 });
  const found = await driver.send("DOM.querySelector", {
    nodeId: document.result.root.nodeId,
    selector: `${driver.composerSelector(ref)} input[type=file]`,
  });
  if (!found.result?.nodeId) throw new Error("composer file input not found in the DOM");
  await driver.send("DOM.setFileInputFiles", { files: [filePath], nodeId: found.result.nodeId });
  await driver.waitPage(
    `(() => { const state = ${driver.composerStateExpr(ref)}; return state && state.tiles === 1 ? true : null; })()`,
    { label: "staged attachment tile" },
  );
  await driver.waitPage(
    `(() => { const pane = ${driver.paneScopeExpr(ref)};
      const tiles = pane ? [...pane.querySelectorAll("[data-testid='attachment-tile']")] : [];
      return tiles.length === 1 && tiles.every((t) => t.querySelector("button[aria-label^='View ']") !== null) ? true : null; })()`,
    { timeoutMs: 30000, label: "attachment decoded" },
  );
}

async function stageDraft(driver, ref, artifactDir, text) {
  await evaluate(driver.send, `${driver.editorExpr(ref)}.focus()`);
  await driver.typeText(ref, text);
  await driver.selectSkillChip(ref);
  await stageAttachment(driver, ref, artifactDir);
  const state = await driver.composerState(ref);
  assert.equal(state.tiles, 1, "one staged image");
  assert.ok(state.chips.includes("/pkg:probe"), `skill chip staged: ${JSON.stringify(state.chips)}`);
  return state;
}

async function storedDraft(driver, ref) {
  const entries = await evaluate(driver.send, driver.draftStorageExpr());
  const key = `evener.composer.draft.v2.${ref}`;
  const entry = entries.find((item) => item.key === key);
  return entry === undefined ? null : JSON.parse(entry.value);
}

async function scrollTranscriptToTop(driver) {
  const result = await evaluate(
    driver.send,
    `(() => {
      const root = document.querySelector("[data-testid='transcript-virtual-list']");
      if (!root) return "missing";
      const nodes = [root, ...root.querySelectorAll("*")];
      const scroller = nodes.find((node) => {
        const style = getComputedStyle(node);
        return (style.overflowY === "auto" || style.overflowY === "scroll") && node.scrollHeight > node.clientHeight;
      });
      if (!scroller) return "no-scroller";
      scroller.scrollTop = 0;
      scroller.dispatchEvent(new Event("scroll", { bubbles: true }));
      return { scrollHeight: scroller.scrollHeight, clientHeight: scroller.clientHeight };
    })()`,
  );
  if (result === "missing") throw new Error("transcript scroll root missing");
}

async function pageOlderUntil(driver, text, { attempts = 6 } = {}) {
  for (let attempt = 0; attempt < attempts; attempt++) {
    const before = await evaluate(driver.send, `window.__retryWire.methodCounts["thread/turns/list"] ?? 0`);
    await scrollTranscriptToTop(driver);
    try {
      await driver.waitPage(
        `(() => document.body.innerText.includes(${q(text)}) || (window.__retryWire.methodCounts["thread/turns/list"] ?? 0) > ${before} ? true : null)()`,
        { label: `older page ${attempt + 1}` },
      );
    } catch (error) {
      if (attempt === attempts - 1) throw error;
    }
    const seen = await evaluate(driver.send, `document.body.innerText.includes(${q(text)})`);
    if (seen) {
      // Let the virtualized rows commit the prepended page before reading the
      // failure end caps and their controls.
      await new Promise((resolve) => setTimeout(resolve, 250));
      return;
    }
  }
  throw new Error(`older history never exposed ${JSON.stringify(text)}`);
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
  await pressRetryUntilTransport(driver);
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
  await driver.waitPage(`document.body.textContent.includes("RETRY_DONE_${scenario.mode}")`, {
    label: "continued turn completed",
  });
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
  } else {
    assert.equal(wire.requests.length, scenario.mode === "refused" ? 2 : 1);
  }
  driver.milestone(scenario.mode, { requests: wire.requests.length });
}

async function runEligibility(driver, scenario) {
  const ref = scenario.ref;
  await driver.openSession(ref);
  await driver.waitPage(`document.querySelector('[data-testid="turn-failure"]') !== null`, {
    label: "terminal provider failure",
  });
  await waitRetry(driver, 1, { enabled: true, label: "eligible latest Retry enabled" });

  const staged = await stageDraft(driver, ref, driver.artifactDir, "RETRY_DRAFT_ELIGIBILITY");
  driver.milestone("draft-staged", staged);

  // Hold the acceptance response and the new-turn events independently. The
  // request still reaches the hub; only this page's view of its result and of
  // the continuation's arrival are withheld.
  await evaluate(
    driver.send,
    `(() => { const wire = window.__retryWire;
      wire.requests.length = 0;
      wire.holdResponses("turn/start", "turn/queue", "thread/read");
      wire.holdNotifications(
        "thread/started",
        "thread/status/changed",
        "history/updated",
        "overlay/upserted",
        "overlay/delta",
        "overlay/reset",
        "overlay/end",
      );
      return true; })()`,
  );

  // Repeated activation, the second press in the same task as the first.
  await clickRetry(driver, { times: 2 });
  await driver.waitPage("window.__retryWire.requests.length === 1", {
    label: "one transport submission for two activations",
  });
  const { rows } = await waitDurable(driver, ref, (rows) => rows.length === 1, "one durable outbox row");
  assert.equal(rows[0].method, "turn/start", "durable submission method");
  assert.deepEqual(rows[0].input, [{ type: "text", text: CONTINUE }], "durable continuation payload");
  const mutationID = rows[0].clientMutationId;
  assert.equal(typeof mutationID, "string");
  assert.notEqual(mutationID, "");
  const wire = await evaluate(driver.send, "window.__retryWire");
  assert.equal(wire.requests.length, 1, "one wire dispatch");
  assert.equal(wire.requests[0].params.clientMutationId, mutationID, "wire and durable identity agree");
  await waitRetry(driver, 1, { enabled: false, label: "visible latest Retry disabled while pending" });

  // A press on the now-disabled control is not an action. The in-page click
  // still fires the browser's event path; the owner must refuse it.
  await evaluate(
    driver.send,
    `(() => { const b = ${retryButtonsExpr()}[0]; if (!b) return false; b.click(); return true; })()`,
  );
  await new Promise((resolve) => setTimeout(resolve, 150));
  assert.equal(
    (await evaluate(driver.send, "window.__retryWire")).requests.length,
    1,
    "disabled press dispatched nothing",
  );
  const afterDisabled = await evaluate(driver.send, driver.durableRecordsExpr());
  assert.equal(
    (afterDisabled.outbox ?? []).filter((record) => record.targetRef === ref).length,
    1,
    "disabled press added no durable row",
  );
  driver.milestone("retry-held", { clientMutationId: mutationID });

  // Release the acceptance only. The continuation has been accepted, but the
  // new-turn events are still withheld: the old failure is not proof of the
  // tail, so Retry stays inactive.
  await evaluate(driver.send, `window.__retryWire.releaseResponses("turn/start", "turn/queue")`);
  await driver.waitPage(`window.__retryWire.heldResponses.every((held) => held.method === "thread/read")`, {
    label: "acceptance released, authoritative history still held",
  });
  await waitRetry(driver, 1, { enabled: false, label: "Retry still disabled after acceptance" });

  await evaluate(driver.send, `window.__retryWire.releaseResponses("thread/read")`);
  await evaluate(driver.send, "window.__retryWire.releaseNotifications()");
  await waitBodyIncludes(driver, "RETRY_DONE_ELIGIBILITY", { timeoutMs: 30000 });
  await waitRetry(driver, 0, { label: "old failure has no Retry after the continuation runs" });
  const preserved = await driver.composerState(ref);
  assert.ok(
    preserved.text.includes("RETRY_DRAFT_ELIGIBILITY"),
    `draft text preserved: ${JSON.stringify(preserved.text)}`,
  );
  assert.ok(preserved.chips.includes("/pkg:probe"), "structured selection preserved");
  assert.equal(preserved.tiles, 1, "staged image preserved");
  const settled = await evaluate(driver.send, driver.durableRecordsExpr());
  assert.equal(
    (settled.outbox ?? []).filter((record) => record.targetRef === ref && record.clientMutationId === mutationID)
      .length,
    0,
    "one durable mutation identity settled out of the outbox",
  );
  const wireAfter = await evaluate(driver.send, "window.__retryWire");
  assert.equal(
    new Set(wireAfter.requests.map((request) => request.params.clientMutationId)).size,
    1,
    "no second mutation identity",
  );
  driver.milestone("retry-completed", { clientMutationId: mutationID, requests: wireAfter.requests.length });

  // The preserved draft is now deliberately edited away by the user (allowed
  // by the contract), so the newer-turn scenarios start from an empty composer.
  await driver.clearComposerDraft(ref);
  await evaluate(driver.send, "window.__retryWire.clearHolds()");
  driver.milestone("ready-for-newer-turn", {});
  await evaluate(driver.send, `${driver.editorExpr(ref)}.focus()`);
  await driver.typeText(ref, "RETRY_NEWER_FAIL");
  const failing = await driver.composerState(ref);
  await driver.clickSubmit(ref, { text: failing.text, chips: [], tiles: 0 });
  await driver.waitPage(
    `(() => [...document.querySelectorAll("[data-testid='turn-failure']")].some((cap) => cap.textContent.includes("retry_fixture_newer")) ? true : null)()`,
    { label: "newer failure rendered" },
  );
  await waitRetry(driver, 1, { enabled: true, label: "newer latest failure offers its own Retry" });
  const caps = await evaluate(driver.send, failureCapsExpr());
  const withActions = caps.filter((cap) => cap.buttons.some((button) => button.label === "Retry"));
  assert.equal(withActions.length, 1, `only the latest failure offers Retry: ${JSON.stringify(caps)}`);
  assert.ok(
    caps.some((cap) => cap.buttons.length === 0),
    `the stale older failure stays readable without Retry: ${JSON.stringify(caps)}`,
  );
  driver.milestone("newest-failure-ready", {});

  await clickRetryWhenReady(driver, { times: 1 });
  await waitBodyIncludes(driver, "RETRY_NEWEST_DONE", { timeoutMs: 30000 });
  await waitRetry(driver, 0, { label: "newest retry settled with no remaining action" });

  // A fresh current draft: its text and structured selection must survive the
  // reload. Images stay per-mount, so this draft stages none.
  await evaluate(driver.send, `${driver.editorExpr(ref)}.focus()`);
  await driver.typeText(ref, "RETRY_RELOAD_DRAFT");
  await driver.selectSkillChip(ref);
  const beforeReload = await driver.composerState(ref);
  driver.milestone("reload-draft-staged", beforeReload);

  // Hard reload: text and structured selections are retained, staged images
  // are per-mount and do not survive (the documented attachment limit).
  await navigateTo(driver.page, driver.url);
  await driver.openSession(ref);
  await waitBodyIncludes(driver, "RETRY_NEWEST_DONE", { timeoutMs: 30000 });
  const reloaded = await driver.composerState(ref);
  assert.equal(reloaded.text, beforeReload.text, `reloaded draft text: ${JSON.stringify(reloaded.text)}`);
  assert.ok(reloaded.chips.includes("/pkg:probe"), "reloaded structured selection");
  assert.equal(reloaded.tiles, 0, "staged image did not promise a new reload lifetime");
  const stored = await storedDraft(driver, ref);
  assert.equal(stored?.text, beforeReload.text, "stored draft text matches the composer");
  assert.deepEqual(stored?.skillNames, ["pkg:probe"], "stored structured selection");
  driver.milestone("reload-draft", { text: reloaded.text, chips: reloaded.chips, tiles: reloaded.tiles });
}

async function runRecovery(driver, scenario) {
  const ref = scenario.ref;
  await driver.openSession(ref);
  await driver.waitPage(`document.querySelector('[data-testid="turn-failure"]') !== null`, {
    label: "terminal provider failure",
  });
  await waitRetry(driver, 1, { enabled: true, label: "eligible latest Retry enabled" });
  const staged = await stageDraft(driver, ref, driver.artifactDir, "RETRY_DRAFT_RECOVERY");
  driver.milestone("draft-staged", staged);

  // Lose the acceptance reply on the real socket. The request reached the hub;
  // this page never learns its result.
  await evaluate(
    driver.send,
    `Object.assign(window.__retryWire, { mode: "lost-reply", armed: true, requests: [], responseHolds: [], notificationHolds: [] })`,
  );
  await clickRetryWhenReady(driver, { times: 1 });
  await driver.waitPage("window.__retryWire.dropped === 1", { label: "acceptance reply dropped" });
  const { rows } = await waitDurable(driver, ref, (rows) => rows.length === 1, "the durable lost-reply row");
  const mutationID = rows[0].clientMutationId;
  assert.equal(rows[0].input[0].text, CONTINUE);
  driver.milestone("acceptance-dropped", { clientMutationId: mutationID });

  // The next document holds its durable pending read (unknown, not empty) and
  // the first authoritative history read fails transiently. Both recover
  // automatically; the driver never presses a refresh control.
  const config = JSON.stringify({ failNext: { "thread/read": 1 } });
  await evaluate(
    driver.send,
    `(() => { localStorage.setItem("__retryWireConfig", ${q(config)});
      localStorage.setItem("__retryStorageArmed", "1"); return true; })()`,
  );
  await navigateTo(driver.page, driver.url);
  await driver.openSession(ref);

  await driver.waitPage("window.__retryStorage.count > 0", { label: "durable pending read held unknown" });
  await driver.waitPage("window.__retryWire.failed === 1", { label: "authoritative history read failed once" });
  // The failed read recovers on the existing hydration owner. The failure cap
  // is readable again, but its action cannot act while pending is unknown.
  await driver.waitPage(
    `(() => { const caps = ${failureCapsExpr()}; const buttons = ${retryButtonsExpr()};
      return caps.length > 0 && buttons.every((button) => button.disabled) ? true : null; })()`,
    { label: "readable failure with Retry inactive while pending is unknown", timeoutMs: 30000 },
  );
  const reloaded = await driver.composerState(ref);
  assert.ok(
    reloaded.text.includes("RETRY_DRAFT_RECOVERY"),
    `reload preserved draft text: ${JSON.stringify(reloaded.text)}`,
  );
  assert.ok(reloaded.chips.includes("/pkg:probe"), "reload preserved structured selection");
  assert.equal(reloaded.tiles, 0, "staged images do not survive a hard reload");
  driver.milestone("recovery-window-asserted", { clientMutationId: mutationID });

  await evaluate(driver.send, "window.__retryStorage.release()");
  await waitBodyIncludes(driver, "RETRY_RECOVERED", { timeoutMs: 45000 });
  await waitRetry(driver, 0, { label: "recovered conversation offers no stale Retry" });
  const after = await driver.composerState(ref);
  assert.ok(after.text.includes("RETRY_DRAFT_RECOVERY"), "draft preserved through reconciliation");
  assert.ok(after.chips.includes("/pkg:probe"), "structured selection preserved through reconciliation");
  assert.equal(after.tiles, 0, "attachment limit unchanged across reload");
  assert.notEqual(after.submitDisabled, true, "useful operation restored");
  const settled = await evaluate(driver.send, driver.durableRecordsExpr());
  const identityRows = ["outbox", "optimistic", "recovery"].flatMap((store) =>
    (settled[store] ?? []).filter((record) => record.clientMutationId === mutationID),
  );
  assert.equal(identityRows.length, 0, "the recovered mutation identity left durable storage");
  assert.equal(
    (settled.outbox ?? []).filter((record) => record.targetRef === ref).length,
    0,
    "the recovered ref has no unresolved outbox rows",
  );
  assert.equal(
    (settled.optimistic ?? []).filter((record) => record.targetRef === ref).length,
    0,
    "the recovered ref has no optimistic rows",
  );
  const wire = await evaluate(driver.send, "window.__retryWire");
  const identities = wire.requests.map((request) => request.params.clientMutationId);
  for (const identity of identities) assert.equal(identity, mutationID, "recovery reused one mutation identity");
  driver.milestone("recovery-complete", { identities, connections: wire.connections });
}

// A still-latest failure must lose Retry when a newer turn's running identity
// arrives, before that turn has produced any transcript item. This scenario is
// separate from eligibility because the eligibility continuation has already
// ended the first failure's action; proving the running veto needs the action
// to be enabled first.
async function runRunning(driver, scenario) {
  const ref = scenario.ref;
  await driver.openSession(ref);
  await driver.waitPage(`document.querySelector('[data-testid="turn-failure"]') !== null`, {
    label: "terminal provider failure",
  });
  await waitRetry(driver, 1, { enabled: true, label: "latest failure Retry enabled" });

  // thread/started and thread/status/changed stay delivered: the running
  // identity is exactly what the rule must see. History and live overlays are
  // withheld so no display item can explain the missing action.
  await evaluate(
    driver.send,
    `(() => { const wire = window.__retryWire; wire.clearHolds();
      wire.holdResponses("thread/read");
      wire.holdNotifications("history/updated", "overlay/upserted", "overlay/delta", "overlay/reset", "overlay/end");
      return true; })()`,
  );
  await evaluate(driver.send, `${driver.editorExpr(ref)}.focus()`);
  await driver.typeText(ref, "RETRY_RUNNING_NEW");
  const state = await driver.composerState(ref);
  await driver.clickSubmit(ref, { text: state.text, chips: [], tiles: 0 });
  await driver.waitPage(
    `(() => { const composer = ${driver.composerStateExpr(ref)};
      const buttons = ${retryButtonsExpr()};
      const rows = [...document.querySelectorAll("[data-testid='transcript-virtual-list'] [data-testid='transcript-row']")];
      return composer && composer.steerVisible && buttons.length === 0 &&
        rows.every((row) => !row.textContent.includes("RETRY_RUNNING_NEW")) ? true : null; })()`,
    { label: "newer running turn hides the older Retry before its items" },
  );
  driver.milestone("running-empty", {});

  await evaluate(driver.send, 'window.__retryWire.releaseResponses("thread/read")');
  await evaluate(driver.send, "window.__retryWire.releaseNotifications()");
  await waitBodyIncludes(driver, "RETRY_RUNNING_NEW", { timeoutMs: 30000 });
  await waitBodyIncludes(driver, "RETRY_RUNNING_DONE", { timeoutMs: 30000 });
  await waitRetry(driver, 0, { label: "completed newer turn leaves the older failure readable without Retry" });
  const caps = await evaluate(driver.send, failureCapsExpr());
  assert.ok(caps.length >= 1, "the older failure is still readable");
  assert.ok(
    caps.every((cap) => cap.buttons.every((button) => button.label !== "Retry")),
    `the older failure has no Retry: ${JSON.stringify(caps)}`,
  );
  driver.milestone("running-complete", { caps: caps.length });
}

async function runHistory(driver, scenario) {
  const ref = scenario.ref;
  await driver.openSession(ref);
  await waitBodyIncludes(driver, "RETRY_HISTORY_LAST", { timeoutMs: 30000 });
  await waitRetry(driver, 0, { label: "latest successful turn offers no Retry" });
  assert.equal(
    await evaluate(driver.send, `document.body.innerText.includes(${q(scenario.original)})`),
    false,
    "the historical failure is outside the newest page",
  );
  driver.milestone("history-latest", {});

  await pageOlderUntil(driver, scenario.original);
  const firstPages = await evaluate(driver.send, `window.__retryWire.methodCounts["thread/turns/list"] ?? 0`);
  assert.ok(firstPages >= 1, "the real older-history owner served the first page");
  const caps = await evaluate(driver.send, failureCapsExpr());
  assert.ok(caps.length >= 1, `historical failure is readable: ${JSON.stringify(caps)}`);
  await waitRetry(driver, 0, { label: "historical failure never gains Retry" });
  driver.milestone("history-paged", { caps: caps.length, pages: firstPages });

  // Refresh/reconnect: the reopened conversation starts at the newest page and
  // must reach the same rule through the same paging owner.
  await navigateTo(driver.page, driver.url);
  await driver.openSession(ref);
  await waitBodyIncludes(driver, "RETRY_HISTORY_LAST", { timeoutMs: 30000 });
  await waitRetry(driver, 0, { label: "refreshed latest turn offers no Retry" });
  await pageOlderUntil(driver, scenario.original);
  await waitRetry(driver, 0, { label: "refreshed historical failure never gains Retry" });
  const pages = await evaluate(driver.send, `window.__retryWire.methodCounts["thread/turns/list"] ?? 0`);
  assert.ok(pages >= 1, "the real older-history owner served a page");
  driver.milestone("history-refreshed", { pages });
}

// The durable enqueue is held, the session is switched away and back in the
// same document (a binding replacement before enqueue returns), then the
// write is released. One mutation identity must survive the remount and no
// second uncertainty owner may strand the continuation.
async function runRemount(driver, scenario) {
  const ref = scenario.ref;
  const otherRef = scenario.otherRef;
  await driver.openSession(ref);
  await driver.waitPage(`document.querySelector('[data-testid="turn-failure"]') !== null`, {
    label: "terminal provider failure",
  });
  await waitRetry(driver, 1, { enabled: true, label: "latest failure Retry enabled" });

  await evaluate(driver.send, `${driver.editorExpr(ref)}.focus()`);
  await driver.typeText(ref, "RETRY_REMOUNT_DRAFT");
  await driver.selectSkillChip(ref);
  await evaluate(driver.send, "window.__retryStorage.holdNextOutboxWrite()");
  await clickRetryWhenReady(driver, { times: 1 });
  await driver.waitPage("window.__retryStorage.writeReached === 1", { label: "durable enqueue held" });
  assert.equal(
    (await evaluate(driver.send, "window.__retryWire")).requests.length,
    0,
    "no dispatch before enqueue settles",
  );

  await driver.openSession(otherRef);
  await driver.openSession(ref);
  const remounted = await driver.composerState(ref);
  assert.ok(
    remounted.text.includes("RETRY_REMOUNT_DRAFT"),
    `draft survived the switch: ${JSON.stringify(remounted.text)}`,
  );
  assert.ok(remounted.chips.includes("/pkg:probe"), "structured selection survived the switch");
  const buttons = await evaluate(driver.send, `${retryButtonsExpr()}.map((button) => ({ disabled: button.disabled }))`);
  assert.ok(
    buttons.every((button) => button.disabled),
    "Retry is not actionable through the remount",
  );
  driver.milestone("remount-window", { text: remounted.text });

  await evaluate(driver.send, "window.__retryStorage.releaseWrite()");
  await driver.waitPage("window.__retryWire.requests.length === 1", { label: "one continuation dispatched" });
  await waitBodyIncludes(driver, "RETRY_REMOUNT_DONE", { timeoutMs: 30000 });
  await waitRetry(driver, 0, { label: "the old failure has no Retry after recovery" });
  const durable = await waitDurableSettled(driver, ref);
  assert.equal(
    (durable.outbox ?? []).filter((record) => record.targetRef === ref).length,
    0,
    "the held enqueue settled out of the outbox",
  );
  assert.equal(
    (durable.optimistic ?? []).filter((record) => record.targetRef === ref).length,
    0,
    "no stranded optimistic row",
  );
  const wire = await evaluate(driver.send, "window.__retryWire");
  const identities = wire.requests.map((request) => request.params.clientMutationId);
  assert.equal(identities.length, 1, "one continuation dispatch");
  assert.equal(new Set(identities).size, 1, "one mutation identity");
  driver.milestone("remount-complete", { identities });
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
    if (scenario.mode === "eligibility") await runEligibility(driver, scenario);
    else if (scenario.mode === "recovery") await runRecovery(driver, scenario);
    else if (scenario.mode === "running") await runRunning(driver, scenario);
    else if (scenario.mode === "history") await runHistory(driver, scenario);
    else if (scenario.mode === "remount") await runRemount(driver, scenario);
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
