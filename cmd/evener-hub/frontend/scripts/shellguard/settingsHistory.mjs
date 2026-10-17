// Real Chrome behavior, production AppShell and AGENTS.md editor. Only AppWire
// transport and pre-mount history are scripted. Never synthesize Navigation API.
import assert from "node:assert/strict";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { applyViewport, closePage, connectPage, evaluate, navigateTo, openPage } from "../browserGuardCdp.mjs";
import { startBrowserGuard, waitForBrowserReady } from "../browserGuardProcess.mjs";

const EDITOR = 'textarea[aria-label="AGENTS.md contents"]';
const DIALOG = '[role="dialog"]';
const BASELINE = "browser-history-baseline";
const DRAFT = "browser-history-baseline unsaved-browser-input";
const VIEWPORTS = [
  { width: 1400, height: 900 },
  { width: 390, height: 844, mobile: true, touch: true },
];
const BOOT = { bootExpression: "typeof window.settledShell !== 'undefined'", bootLabel: "shellguard fixture" };

async function wait(send, predicate, label) {
  return evaluate(
    send,
    `(async () => {
    const deadline = performance.now() + 15000;
    while (!(${predicate})) {
      if (performance.now() > deadline) throw new Error(${JSON.stringify(label)} + ': ' + JSON.stringify(window.settingsHistoryWitness?.()) + ' ' + document.body.innerText.slice(-500));
      await new Promise(resolve => requestAnimationFrame(resolve));
    }
  })()`,
  );
}

async function witness(send) {
  return evaluate(send, "window.settingsHistoryWitness()");
}

async function assertDraft(send, stage) {
  const observed = await evaluate(
    send,
    `({ value: document.querySelector(${JSON.stringify(EDITOR)})?.value,
    sameEditor: document.querySelector(${JSON.stringify(EDITOR)}) === window.settingsHistoryEditor,
    originalConnected: window.settingsHistoryEditor?.isConnected,
    calls: window.settingsHistoryCalls(), state: window.settingsHistoryWitness() })`,
  );
  assert.equal(observed.value, DRAFT, `${stage}: ${JSON.stringify(observed)}`);
}

async function choose(send, clickControl, name) {
  // Accessible name selects the requested action, never serves as the oracle.
  await evaluate(
    send,
    `(() => {
    document.querySelector('[data-history-action]')?.removeAttribute('data-history-action');
    const button = Array.from(document.querySelectorAll('${DIALOG} button')).find(button => button.textContent.trim() === ${JSON.stringify(name)});
    if (!button) throw new Error('Confirmation action missing');
    button.dataset.historyAction = '';
  })()`,
  );
  await clickControl(send, "[data-history-action]");
  await wait(send, `!document.querySelector('${DIALOG}')`, "confirmation dismissed");
}

async function edit(send, clickControl) {
  await wait(
    send,
    `document.querySelector(${JSON.stringify(EDITOR)})?.value === ${JSON.stringify(BASELINE)}`,
    "scripted document in real editor",
  );
  await clickControl(send, EDITOR);
  // Trusted CDP input gives Chrome sticky activation for native beforeunload.
  await send("Input.dispatchKeyEvent", {
    type: "keyDown",
    key: "End",
    code: "End",
    windowsVirtualKeyCode: 35,
    modifiers: 2,
  });
  await send("Input.dispatchKeyEvent", {
    type: "keyUp",
    key: "End",
    code: "End",
    windowsVirtualKeyCode: 35,
    modifiers: 2,
  });
  // Selection is preparation only, editing and React onChange use native input.
  await evaluate(
    send,
    `document.querySelector(${JSON.stringify(EDITOR)}).setSelectionRange(${BASELINE.length}, ${BASELINE.length})`,
  );
  await send("Input.insertText", { text: " unsaved-browser-input" });
  await wait(
    send,
    `document.querySelector(${JSON.stringify(EDITOR)})?.value === ${JSON.stringify(DRAFT)}`,
    "trusted input retained",
  );
  await wait(
    send,
    `Array.from(document.querySelectorAll('button')).some(button => button.textContent.trim() === 'Save' && !button.disabled)`,
    "React editor has committed dirty state",
  );
  await evaluate(send, `void (window.settingsHistoryEditor = document.querySelector(${JSON.stringify(EDITOR)}))`);
}

async function traverse(send, delta, expectedIndex, confirming = true) {
  const before = (await witness(send)).traversals.length;
  await evaluate(send, `history.go(${delta})`);
  await wait(
    send,
    `window.settingsHistoryWitness().traversals.length > ${before} && window.settingsHistoryWitness().current.index === ${expectedIndex} && ${confirming ? `!!document.querySelector('${DIALOG}')` : `!document.querySelector('${DIALOG}')`}`,
    "real traversal and cursor settlement",
  );
}

function assertRetained(state, initial) {
  assert.deepEqual(state.entries, initial.entries, "history entries and native keys must not be rewritten");
  assert.deepEqual(state.writes, [], "guard must move the cursor, not write history");
  assert.ok(
    state.traversals.every((event) => event.trusted),
    "native cases must use trusted browser popstate",
  );
}

async function historyCase(page, viewport, mode, delta, clickControl) {
  const { send } = page;
  await edit(send, clickControl);
  const initial = await witness(send);
  assert.equal(initial.nativeAvailable, true);
  assert.equal(initial.productNavigationAvailable, mode === "native");
  assert.equal(initial.address, "/settings/agents-md");
  const acceptedIndex = initial.current.index;
  const first = initial.entries.find((entry) => entry.index === acceptedIndex + delta);
  assert.ok(first, "requested native entry must exist");
  const firstURL = new URL(first.url).pathname + new URL(first.url).search;

  await traverse(send, delta, acceptedIndex);
  await assertDraft(send, "first traversal restored");
  let state = await witness(send);
  assert.equal(state.address, initial.address);
  assert.equal(state.accepted, initial.address);
  assert.equal(state.current.key, initial.current.key, "Stay question must restore exact native key");
  assert.equal(state.traversals[0].index, acceptedIndex + delta);
  assert.deepEqual(
    state.traversals[0].state,
    mode === "native"
      ? { fixtureEntry: acceptedIndex + delta - initial.entries[0].index }
      : { evenerNavigationIndex: acceptedIndex + delta - initial.entries[0].index },
  );
  await choose(send, clickControl, "Stay");
  await assertDraft(send, "Stay answered");
  assertRetained(await witness(send), initial);

  // Re-open, then attempt a DIFFERENT destination while the question is open.
  // Discard must honor the first frozen destination, not the later traversal.
  await traverse(send, delta, acceptedIndex);
  const repeatedDelta = delta === -2 ? 1 : -2;
  await traverse(send, repeatedDelta, acceptedIndex);
  await assertDraft(send, "repeated traversal restored");
  await choose(send, clickControl, "Discard changes");
  await wait(
    send,
    `window.settingsHistoryWitness().current.key === ${JSON.stringify(first.key)} && window.settingsHistoryWitness().accepted === ${JSON.stringify(firstURL)} && !document.querySelector(${JSON.stringify(EDITOR)})`,
    "Discard adopts frozen native entry once",
  );
  state = await witness(send);
  assert.equal(state.address, firstURL);
  assertRetained(state, initial);

  // The old editor unmounts, so returning must load the baseline, not a cache.
  await traverse(send, -delta, acceptedIndex, false);
  await wait(
    send,
    `document.querySelector(${JSON.stringify(EDITOR)})?.value === ${JSON.stringify(BASELINE)}`,
    "Back/Forward returns clean editor",
  );
  assert.equal((await witness(send)).current.key, initial.current.key);
  assertRetained(await witness(send), initial);
  return {
    mode,
    delta,
    viewport: viewport.width,
    nativeAvailable: true,
    nativeKeysPreserved: true,
    stayRetainedInput: true,
    frozenDiscard: firstURL,
    returnedBaseline: true,
    traversals: (await witness(send)).traversals,
  };
}

async function legacyCase(page, viewport, clickControl) {
  const { send } = page;
  await edit(send, clickControl);
  const initial = await witness(send);
  assert.equal(initial.nativeAvailable, true);
  assert.equal(
    initial.productNavigationAvailable,
    false,
    "legacy is deliberately API-disabled, not native recovery evidence",
  );
  const acceptedIndex = initial.current.index;
  await traverse(send, -1, acceptedIndex - 1);
  await choose(send, clickControl, "Stay");
  assert.equal(await evaluate(send, `document.querySelector(${JSON.stringify(EDITOR)})?.value`), DRAFT);
  assert.equal((await witness(send)).accepted, initial.address);
  assert.equal(await evaluate(send, `!!document.querySelector('[data-reason="history-unrestored"]')`), true);
  assertRetained(await witness(send), initial);
  // Forward really returns to the original entry, then Back asks again.
  await traverse(send, 1, acceptedIndex, false);
  await traverse(send, -1, acceptedIndex - 1);
  await traverse(send, -1, acceptedIndex - 2);
  await choose(send, clickControl, "Discard changes");
  await wait(
    send,
    `window.settingsHistoryWitness().accepted === '/settings/theme?history=first' && !document.querySelector(${JSON.stringify(EDITOR)})`,
    "legacy frozen accepted context",
  );
  const state = await witness(send);
  assert.equal(state.address, "/");
  assert.equal(state.current.index, acceptedIndex - 2);
  assert.equal(await evaluate(send, `!!document.querySelector('[data-reason="history-unrestored"]')`), true);
  // Legacy adoption emits one synthetic context publication, not a browser move.
  assert.deepEqual(state.entries, initial.entries);
  assert.deepEqual(state.writes, []);
  assert.equal(state.traversals.filter((event) => !event.trusted).length, 1);
  await traverse(send, 1, acceptedIndex - 1, false);
  await wait(
    send,
    `!document.querySelector('[data-reason="history-unrestored"]')`,
    "ordinary traversal clears mismatch",
  );
  await traverse(send, 1, acceptedIndex, false);
  await wait(
    send,
    `document.querySelector(${JSON.stringify(EDITOR)})?.value === ${JSON.stringify(BASELINE)}`,
    "legacy forward entries remain distinct",
  );
  return {
    mode: "legacy, Navigation API deliberately hidden",
    viewport: viewport.width,
    noHistoryWrites: true,
    stayRetainedInput: true,
    frozenAccepted: state.accepted,
    mismatchedAddress: state.address,
    mismatchNotice: true,
    forwardPreserved: true,
  };
}

async function existingSessionCase(page, viewport, clickControl) {
  const { send } = page;
  // Arrange a background session while Settings is clean, then edit normally.
  await evaluate(
    send,
    `(async () => {
    const { workspaceStore } = await import('/src/shell/workspace.ts');
    const state = workspaceStore.getState();
    const settings = state.focusedPaneId;
    state.openPane('session', { ref: 'local:p0-s0' }, { slot: 'secondary' });
    workspaceStore.getState().focusPane(settings);
  })()`,
  );
  await edit(send, clickControl);
  await clickControl(send, 'button[aria-label="Sessions"]');
  await clickControl(send, '[data-session-ref="local:p0-s0"]');
  await wait(
    send,
    `!!document.querySelector('${DIALOG}') || !document.querySelector(${JSON.stringify(EDITOR)})`,
    "rail selection resolves",
  );
  const state = await evaluate(
    send,
    `({ editor: document.querySelector(${JSON.stringify(EDITOR)})?.value,
    dialog: !!document.querySelector('${DIALOG}'), stacked: document.querySelector('[data-stacked-pane-type]')?.dataset.stackedPaneType,
    route: window.settingsHistoryWitness(), workspace: window.overviewGuardState() })`,
  );
  assert.equal(
    state.editor,
    DRAFT,
    `already-open session must not unmount the dirty Settings editor: ${JSON.stringify(state)}`,
  );
  assert.equal(state.dialog, true, "rail selection must ask before changing pane focus");
  await choose(send, clickControl, "Stay");
  assert.equal(await evaluate(send, `document.querySelector(${JSON.stringify(EDITOR)})?.value`), DRAFT);
  assert.equal((await witness(send)).accepted, "/settings/agents-md");
  return { viewport: viewport.width, existingSession: "local:p0-s0", stayRetainedInput: true };
}

async function nativePrompt(page, action, accept) {
  const { ws, send } = page;
  let timer;
  let handler;
  const opened = new Promise((resolve, reject) => {
    timer = setTimeout(() => reject(new Error("native beforeunload dialog did not open")), 15000);
    handler = (event) => {
      const message = JSON.parse(event.data);
      if (message.method === "Page.javascriptDialogOpening") resolve(message.params);
    };
    ws.addEventListener("message", handler);
  });
  try {
    const command = action();
    const dialog = await Promise.race([opened, command.then(() => new Promise(() => {}))]);
    assert.equal(dialog.type, "beforeunload");
    assert.equal(dialog.hasBrowserHandler, true);
    await send("Page.handleJavaScriptDialog", { accept });
    await command;
    return { type: dialog.type, hasBrowserHandler: dialog.hasBrowserHandler, accepted: accept };
  } finally {
    clearTimeout(timer);
    ws.removeEventListener("message", handler);
  }
}

async function unloadCase(page, viewport, clickControl) {
  const { send } = page;
  await edit(send, clickControl);
  const initial = await witness(send);
  const canceledReload = await nativePrompt(page, () => send("Page.reload"), false);
  assert.equal(await evaluate(send, `document.querySelector(${JSON.stringify(EDITOR)})?.value`), DRAFT);
  assert.equal((await witness(send)).current.key, initial.current.key);
  assert.equal(
    await evaluate(send, `document.querySelector('${DIALOG}') === null`),
    true,
    "native prompt is not the app confirmation",
  );
  await assertPageClean(send);
  const acceptedLeave = await nativePrompt(page, () => send("Page.navigate", { url: "about:blank" }), true);
  await wait(
    send,
    "location.href === 'about:blank' && typeof window.settingsHistoryWitness === 'undefined'",
    "accepting native prompt leaves old document",
  );
  return {
    viewport: viewport.width,
    canceledReload,
    reloadRetainedInput: true,
    acceptedLeave,
    oldDocumentDestroyed: true,
  };
}

async function assertPageClean(send) {
  const errors = await evaluate(send, "window.__shellGuardErrors || []");
  assert.deepEqual(errors, [], "production page must have no uncaught errors");
  const calls = await evaluate(send, "window.settingsHistoryCalls()");
  assert.ok(calls.includes("evener/settings/agentsDoc/get"), "real editor must read through scripted transport");
  assert.ok(!calls.includes("evener/settings/agentsDoc/set"), "acceptance never saves the document");
}

async function withFixture(endpoint, vitePort, viewport, mode, run) {
  const target = await openPage(endpoint, "about:blank");
  const page = await connectPage(endpoint, target.id);
  try {
    await page.send("Page.addScriptToEvaluateOnNewDocument", { source: "try { localStorage.clear(); } catch {}" });
    await applyViewport(page.send, viewport);
    await navigateTo(page, `http://127.0.0.1:${vitePort}/shellguard.html?settingsHistory=${mode}`, BOOT);
    await evaluate(page.send, "window.settledShell");
    const result = await run(page);
    if (!result.oldDocumentDestroyed) await assertPageClean(page.send);
    return result;
  } finally {
    page.close();
    await closePage(endpoint, target.id);
  }
}

export async function checkSettingsHistory(endpoint, vitePort, clickControl) {
  const versionTarget = await openPage(endpoint, "about:blank");
  const versionPage = await connectPage(endpoint, versionTarget.id);
  try {
    console.log(`settings history browser: ${JSON.stringify((await versionPage.send("Browser.getVersion")).result)}`);
  } finally {
    versionPage.close();
    await closePage(endpoint, versionTarget.id);
  }
  const failures = [];
  for (const viewport of VIEWPORTS) {
    for (const mode of ["native", "indexed"]) {
      for (const delta of [-1, -2, 1]) {
        const label = `settings history ${viewport.width}px ${mode} go(${delta})`;
        try {
          const result = await withFixture(endpoint, vitePort, viewport, mode, (page) =>
            historyCase(page, viewport, mode, delta, clickControl),
          );
          console.log(`${label}: ${JSON.stringify(result)}`);
        } catch (error) {
          failures.push(`${label}: ${error.stack}`);
        }
      }
    }
    for (const [label, mode, run] of [
      ["legacy", "legacy", (page) => legacyCase(page, viewport, clickControl)],
      ["beforeunload", "native", (page) => unloadCase(page, viewport, clickControl)],
      ...(viewport.mobile
        ? [["existing-session", "native", (page) => existingSessionCase(page, viewport, clickControl)]]
        : []),
    ]) {
      try {
        console.log(
          `settings history ${viewport.width}px ${label}: ${JSON.stringify(await withFixture(endpoint, vitePort, viewport, mode, run))}`,
        );
      } catch (error) {
        failures.push(`settings history ${viewport.width}px ${label}: ${error.stack}`);
      }
    }
  }
  return failures;
}

// Focused acceptance entry point, also run unconditionally by normal shellguard.
async function main() {
  const { clickControl } = await import("./run.mjs");
  const frontend = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
  const guard = await startBrowserGuard({ frontend, profilePrefix: "settings-history-chrome-" });
  try {
    const endpoint = await waitForBrowserReady(guard);
    const failures = await checkSettingsHistory(endpoint, guard.vitePort, clickControl);
    for (const failure of failures) console.error(failure);
    if (failures.length) process.exitCode = 1;
  } finally {
    await guard.cleanup();
  }
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    console.error(error);
    process.exitCode = 1;
  });
}
