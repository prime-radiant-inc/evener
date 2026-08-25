#!/usr/bin/env node
// a11yguard - a real Chrome/CDP accessibility and interaction regression guard.
// The harness intentionally keeps the disclosure trigger, summary link, and
// action button as siblings: axe-core proves the semantic boundary while CDP
// keyboard/pointer events prove the controls still activate independently.
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  connectPage,
  devtoolsHttpURL,
  evaluate,
  navigateTo,
  waitForHttp,
  createStartupDeadline,
} from "../browserGuardCdp.mjs";
import { describeBrowserStartupFailure, startBrowserGuard } from "../browserGuardProcess.mjs";

const FRONTEND = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const HARNESS_URL = "/scripts/a11yguard/harness.html";
const axeSource = readFileSync(path.join(FRONTEND, "node_modules/axe-core/axe.min.js"), "utf8");

async function clickAt(page, point) {
  await page.send("Input.dispatchMouseEvent", { type: "mouseMoved", x: point.x, y: point.y, button: "none" });
  await page.send("Input.dispatchMouseEvent", { type: "mousePressed", x: point.x, y: point.y, button: "left", buttons: 1, clickCount: 1 });
  await page.send("Input.dispatchMouseEvent", { type: "mouseReleased", x: point.x, y: point.y, button: "left", buttons: 0, clickCount: 1 });
}

async function dispatchKey(page, selector, key) {
  await evaluate(page.send, `document.querySelector(${JSON.stringify(selector)}).focus()`);
  const code = key === " " ? "Space" : "Enter";
  const keyCode = key === " " ? 32 : 13;
  await page.send("Input.dispatchKeyEvent", {
    type: "keyDown",
    key,
    code,
    windowsVirtualKeyCode: keyCode,
    nativeVirtualKeyCode: keyCode,
  });
  await page.send("Input.dispatchKeyEvent", {
    type: "keyUp",
    key,
    code,
    windowsVirtualKeyCode: keyCode,
    nativeVirtualKeyCode: keyCode,
  });
}

function assertResult({ axe, state, consoleMessages, structure }) {
  const failures = [];
  if (axe.violations.length > 0) {
    failures.push(`axe violations: ${axe.violations.map((violation) => `${violation.id} (${violation.impact})`).join(", ")}`);
  }
  if (consoleMessages.length > 0) {
    failures.push(`browser console messages: ${consoleMessages.map((message) => `${message.type}: ${message.text}`).join("; ")}`);
  }
  if (structure.triggerInteractiveDescendants !== 0) {
    failures.push(`trigger has ${structure.triggerInteractiveDescendants} interactive descendants`);
  }
  if (structure.controlsId !== structure.bodyId || structure.triggerTag !== "BUTTON") {
    failures.push(`trigger/body association is ${structure.controlsId} -> ${structure.bodyId}, trigger ${structure.triggerTag}`);
  }
  if (state.toggles !== 3 || state.linkActivations !== 1 || state.actionActivations !== 2 || state.railActivations !== 1) {
    failures.push(`interaction counts were ${JSON.stringify(state)}, expected 3/1/2/1`);
  }
  if (structure.railTag !== "SPAN" || structure.railTabIndex !== null || structure.railFocused) {
    failures.push(`aria-hidden rail chevron retained focusability: ${JSON.stringify(structure)}`);
  }
  if (structure.expanded !== "true" || !structure.bodyVisible) {
    failures.push("disclosure state did not remain expanded after exactly-once activation");
  }
  return failures;
}

async function main() {
  let guard;
  try {
    guard = await startBrowserGuard({ frontend: FRONTEND, profilePrefix: "a11yguard-chrome-" });
  } catch (error) {
    throw new Error(describeBrowserStartupFailure({ error, subsystem: "launch" }));
  }
  const { vitePort, cleanup } = guard;
  let endpoint;
  let page;
  const consoleMessages = [];
  try {
    try {
      await waitForHttp(`http://127.0.0.1:${vitePort}${HARNESS_URL}`, "vite dev server", guard.getViteLaunchError);
    } catch (error) {
      throw new Error(describeBrowserStartupFailure({ error, subsystem: "vite", viteStderr: guard.getViteError() }));
    }
    const deadline = createStartupDeadline();
    try {
      endpoint = await guard.waitForChrome({ signal: deadline.signal });
      await waitForHttp(devtoolsHttpURL(endpoint, "/json/version"), "chrome devtools endpoint", guard.getChromeLaunchError, {
        signal: deadline.signal,
        failure: guard.getChromeFailure(),
      });
    } catch (error) {
      throw new Error(
        describeBrowserStartupFailure({
          error,
          subsystem: "chrome",
          chromeBinary: guard.chromeBinary,
          chromeArgv: guard.getChromeArgv(),
          chromeStderr: guard.getChromeError(),
          viteStderr: guard.getViteError(),
        }),
      );
    } finally {
      deadline.clear();
    }

    page = await connectPage(endpoint);
    page.ws.addEventListener("message", (event) => {
      const message = JSON.parse(event.data);
      if (message.method === "Runtime.consoleAPICalled") {
        const type = message.params?.type;
        if (type === "error" || type === "warning") {
          consoleMessages.push({ type, text: (message.params.args ?? []).map((arg) => arg.value ?? "").join(" ") });
        }
      }
    });
    await page.send("Runtime.enable");
    await navigateTo(page, `http://127.0.0.1:${vitePort}${HARNESS_URL}`);
    const axe = await evaluate(page.send, `(function(){${axeSource}; return axe.run(document, { runOnly: ["nested-interactive"] });})()`);

    await dispatchKey(page, "#trigger", "Enter");
    await dispatchKey(page, "#trigger", " ");
    await dispatchKey(page, "#summary-link", "Enter");
    await dispatchKey(page, "#action", " ");
    const triggerPoint = await evaluate(page.send, "(() => { const rect = document.querySelector('#trigger').getBoundingClientRect(); return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 }; })()");
    await clickAt(page, triggerPoint);
    await evaluate(page.send, "document.querySelector('#trigger').click()");
    const actionPoint = await evaluate(page.send, "(() => { const rect = document.querySelector('#action').getBoundingClientRect(); return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 }; })()");
    await clickAt(page, actionPoint);
    const railPoint = await evaluate(page.send, "(() => { const rect = document.querySelector('#rail-chevron').getBoundingClientRect(); return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 }; })()");
    await clickAt(page, railPoint);
    const result = await evaluate(
      page.send,
      `({
        state: window.disclosureGuardState,
        structure: {
          triggerTag: document.querySelector('#trigger').tagName,
          triggerInteractiveDescendants: document.querySelector('#trigger').querySelectorAll('a,button,input,select,textarea,[tabindex]:not([tabindex="-1"])').length,
          controlsId: document.querySelector('#trigger').getAttribute('aria-controls'),
          bodyId: document.querySelector('#disclosure-body').id,
          expanded: document.querySelector('#trigger').getAttribute('aria-expanded'),
          bodyVisible: !document.querySelector('#disclosure-body').hidden,
          railTag: document.querySelector('#rail-chevron').tagName,
          railTabIndex: document.querySelector('#rail-chevron').getAttribute('tabindex'),
          railFocused: document.activeElement === document.querySelector('#rail-chevron'),
        },
      })`,
    );
    const failures = assertResult({ axe, state: result.state, consoleMessages, structure: result.structure });
    if (failures.length > 0) {
      for (const failure of failures) console.error(`a11yguard FAIL: ${failure}`);
      process.exitCode = 1;
    } else {
      console.log(`a11yguard PASS: axe nested-interactive clean; trigger/body ${result.structure.controlsId}; keyboard/pointer counts ${JSON.stringify(result.state)}; console clean`);
    }
  } finally {
    page?.close();
    await cleanup();
  }
}

main().catch((error) => {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
});
