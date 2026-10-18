#!/usr/bin/env node
// Real production hub UI. The Go test owns only the external HTTP provider.
// Reuse skillguard's exported Chrome lifecycle/input driver, never its fixtures.
import assert from "node:assert/strict";
import { writeFileSync } from "node:fs";
import path from "node:path";
import { evaluate, navigateTo } from "../browserGuardCdp.mjs";
import { Driver } from "../skillguard/run.mjs";
import { button as activateButton } from "./button.mjs";
import { captureAccessViews, record } from "./record.mjs";

const args = Object.fromEntries(
  Array.from({ length: (process.argv.length - 2) / 2 }, (_, i) => [
    process.argv[2 + i * 2].slice(2),
    process.argv[3 + i * 2],
  ]),
);
const driver = new Driver({
  url: args.url,
  artifactDir: args["artifact-dir"],
  milestonePath: path.join(args["artifact-dir"], "milestones.jsonl"),
});
const read = (source) => evaluate(driver.send, source);
const pause = async (ms = 1800) => {
  if (args["record-video"]) await new Promise((resolve) => setTimeout(resolve, ms));
};
const wait = (source, label) => driver.waitPage(source, { label });
const hasButton = (name) =>
  `[...document.querySelectorAll('button')].some(b => (b.textContent.trim() === ${JSON.stringify(name)} || b.getAttribute('aria-label') === ${JSON.stringify(name)}) && !b.disabled)`;
async function button(name) {
  await activateButton(driver, pause, name);
}
async function fill(selector, value) {
  await wait(
    `!!document.querySelector(${JSON.stringify(selector)}) && !document.querySelector(${JSON.stringify(selector)}).disabled`,
    selector,
  );
  await driver.click(selector);
  await driver.send("Input.dispatchKeyEvent", {
    type: "keyDown",
    key: "a",
    code: "KeyA",
    modifiers: process.platform === "darwin" ? 4 : 2,
    windowsVirtualKeyCode: 65,
  });
  await driver.send("Input.dispatchKeyEvent", {
    type: "keyUp",
    key: "a",
    code: "KeyA",
    modifiers: process.platform === "darwin" ? 4 : 2,
    windowsVirtualKeyCode: 65,
  });
  await driver.send("Input.insertText", { text: value });
  assert.equal(
    await read(
      `document.querySelector(${JSON.stringify(selector)}).value ?? document.querySelector(${JSON.stringify(selector)}).textContent`,
    ),
    value,
    `input ${selector}`,
  );
}
async function checkpoint(minChecks) {
  const response = await fetch(args.checkpoint);
  const body = await response.text();
  assert.equal(response.status, 200, body);
  assert.ok(JSON.parse(body).checks >= minChecks);
}
// Read the selected tab in the active dock group, then its controlled panel.
// Never accept a reply/composer merely mounted in another pane.
function activeSession() {
  const tab = document.querySelector('.dv-groupview.dv-active-group [role="tab"][aria-selected="true"]');
  const panel = tab && document.getElementById(tab.getAttribute("aria-controls"));
  const bodies =
    panel &&
    [...panel.querySelectorAll('[data-pane-scaffold^="session:"]')].filter((e) => e.getBoundingClientRect().width > 0);
  if (bodies?.length !== 1) return null;
  const body = bodies[0];
  const ref = body.dataset.paneScaffold.slice("session:".length);
  const pane = body.parentElement;
  const composer = pane.querySelector(`[data-composer="${ref}"] [contenteditable="true"]`);
  if (!composer) return null;
  return { ref, text: composer.textContent, reply: pane.textContent.includes("ONBOARDING_REPLY_72e") };
}
const active = `(${activeSession.toString()})()`;
async function start() {
  await wait(hasButton("Start"), "Start enabled");
  // The timestamp is captured by the actual activation event, synchronously
  // before React handles it, not before an asynchronous wait or CDP round trip.
  const activatedAt = await read(`(() => {
    const button = document.querySelector('[data-testid="spawn-submit"]');
    if (!button || button.disabled) return null;
    let at = null;
    const record = () => { at = performance.timeOrigin + performance.now(); };
    button.addEventListener('click', record, { capture: true, once: true });
    button.click();
    button.removeEventListener('click', record, true);
    return at;
  })()`);
  assert.ok(activatedAt);
  driver.milestone("start-activated", { activatedAt });
}
let stopping;
let finishRecording;
async function stop() {
  stopping ??= driver.stop();
  await stopping;
}
process.once("SIGTERM", () => {
  void stop().finally(() => process.exit(143));
});
try {
  await driver.start();
  if (args["record-video"]) {
    await driver.send("Emulation.setDeviceMetricsOverride", {
      width: 1440,
      height: 1000,
      deviceScaleFactor: 1,
      mobile: false,
    });
  }
  await navigateTo(driver.page, args.url);
  await wait(`!!document.querySelector('[data-testid="spawn-provider-prerequisite"]')`, "fresh missing access route");
  if (args["record-video"]) finishRecording = await record(driver.page, args["artifact-dir"], args["record-video"]);
  await pause(3000);
  assert.ok(
    await read(
      `!!(document.querySelector('[data-testid="spawn-provider-prerequisite"]').compareDocumentPosition(document.querySelector('[data-testid="spawn-prompt-card"]')) & Node.DOCUMENT_POSITION_FOLLOWING)`,
    ),
    "provider prerequisite must precede composer",
  );
  await fill('[contenteditable="true"]', "ONBOARDING_TASK_72e");
  await pause();
  await driver.click("#spawn-cwd");
  await wait(hasButton("Use this folder"), "initial directory resolved");
  await fill('input[aria-label="Path"]', args.work);
  await pause();
  await button("Go");
  await button("Use this folder");
  await button("Connect provider");
  await button("OpenAI");
  await fill("#provider-credential", "onboarding-fixture-key-not-a-secret");
  await button("Save and check");
  await wait(hasButton("Retry check"), "failed model listing offers retry");
  await checkpoint(1);
  await pause(3000);
  await button("Retry check");
  await button("Choose model");
  await wait(`document.querySelectorAll('[role="option"]').length > 0`, "provider scoped catalogue");
  await pause(3000);
  assert.ok(
    await read(`[...document.querySelectorAll('[role="option"]')].every(e => e.id.includes(':openai/'))`),
    "handoff is scoped to connected instance",
  );

  // Catalogue loading can move rows between CDP mouse-down and mouse-up.
  // Activate the exact visible option atomically, as clickByText does for buttons.
  assert.ok(
    await read(`(() => {
    const option = document.querySelector('[role="option"][id$=":openai/gpt-4o-mini"]');
    if (!option || !option.getBoundingClientRect().width) return false;
    option.click(); return true;
  })()`),
  );
  await wait(
    `document.querySelector('[data-testid="spawn-model-slot"]')?.dataset.state === 'chosen' && !document.querySelector('[role="option"]')`,
    "explicit model committed",
  );
  await wait(`!document.querySelector('[role="dialog"]')`, "model choice closes dialog");
  assert.ok(
    await read(
      `document.activeElement === document.querySelector('[data-testid="spawn-prompt-card"] [contenteditable="true"]') || document.activeElement === document.querySelector('[data-testid="spawn-model-trigger"]')`,
    ),
    "model handoff restores focus to task or model control",
  );
  assert.equal(await read(`document.querySelector('[contenteditable="true"]').textContent`), "ONBOARDING_TASK_72e");
  assert.ok(
    await read(
      `document.querySelector('#spawn-cwd').getAttribute('aria-label').includes(${JSON.stringify(args.work)})`,
    ),
    "working directory retained",
  );
  await checkpoint(2);
  await pause(5000);
  await start();
  await wait(`${active}?.reply === true`, "active session producer reply");
  const firstSession = await read(active);
  driver.milestone("session-observed", firstSession);
  await pause(5000);
  // A second launch is deliberately blank. Production routing, not injected
  // client state, must open a session with an editable composer and no turn.
  await navigateTo(driver.page, new URL("/new", args.url).href);
  await wait(`!!document.querySelector('[data-testid="spawn-submit"]')`, "configured user new session");
  assert.equal(await read(`!!document.querySelector('[data-testid="spawn-provider-prerequisite"]')`), false);
  await fill('[contenteditable="true"]', "");
  await pause(3000);
  await start();
  await wait(
    `${active} !== null && ${active}.ref !== ${JSON.stringify(firstSession.ref)}`,
    "second active session composer",
  );
  const blankSession = await read(active);
  assert.equal(blankSession.text, "");
  assert.equal(blankSession.reply, false);
  driver.milestone("session-observed", blankSession);
  await pause(4000);
  if (finishRecording) {
    await finishRecording();
    finishRecording = null;
    await captureAccessViews(driver, args.url, args["record-video"]);
  }
  console.log(
    "PASS prerequisite ordering, failed check/retry, explicit model, retained draft/directory, reply, blank composer",
  );
} catch (error) {
  try {
    const body = await read("document.body.innerText");
    writeFileSync(path.join(args["artifact-dir"], "page.txt"), body);
    console.error(body);
  } catch {}
  throw error;
} finally {
  await stop();
}
