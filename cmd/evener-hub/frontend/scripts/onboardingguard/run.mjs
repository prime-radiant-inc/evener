#!/usr/bin/env node
// Real production hub UI. The Go test owns only the external HTTP provider.
// Reuse skillguard's exported Chrome lifecycle/input driver, never its fixtures.
import assert from "node:assert/strict";
import { writeFileSync } from "node:fs";
import path from "node:path";
import { evaluate, navigateTo } from "../browserGuardCdp.mjs";
import { Driver } from "../skillguard/run.mjs";

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
const wait = (source, label) => driver.waitPage(source, { label });
const hasButton = (name) =>
  `[...document.querySelectorAll('button')].some(b => (b.textContent.trim() === ${JSON.stringify(name)} || b.getAttribute('aria-label') === ${JSON.stringify(name)}) && !b.disabled)`;
async function button(name) {
  await wait(hasButton(name), `button ${name}`);
  await driver.clickByText(name);
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
let stopping;
async function stop() {
  stopping ??= driver.stop();
  await stopping;
}
process.once("SIGTERM", () => {
  void stop().finally(() => process.exit(143));
});
try {
  await driver.start();
  await navigateTo(driver.page, args.url);
  await wait(`!!document.querySelector('[data-testid="spawn-provider-prerequisite"]')`, "fresh missing access route");
  assert.ok(
    await read(
      `!!(document.querySelector('[data-testid="spawn-provider-prerequisite"]').compareDocumentPosition(document.querySelector('[data-testid="spawn-prompt-card"]')) & Node.DOCUMENT_POSITION_FOLLOWING)`,
    ),
    "provider prerequisite must precede composer",
  );
  await fill('[contenteditable="true"]', "ONBOARDING_TASK_72e");
  await driver.click("#spawn-cwd");
  await wait(hasButton("Use this folder"), "initial directory resolved");
  await fill('input[aria-label="Path"]', args.work);
  await button("Go");
  await button("Use this folder");
  await button("Connect provider");
  await button("OpenAI");
  await fill("#provider-credential", "onboarding-fixture-key-not-a-secret");
  await button("Save and check");
  await wait(hasButton("Retry check"), "failed model listing offers retry");
  await checkpoint(1);
  await button("Retry check");
  await button("Choose model");
  await wait(`document.querySelectorAll('[role="option"]').length > 0`, "provider scoped catalogue");
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
  assert.ok(await read(`document.activeElement !== document.body`), "model handoff restores focus");
  assert.equal(await read(`document.querySelector('[contenteditable="true"]').textContent`), "ONBOARDING_TASK_72e");
  assert.ok(
    await read(
      `document.querySelector('#spawn-cwd').getAttribute('aria-label').includes(${JSON.stringify(args.work)})`,
    ),
    "working directory retained",
  );
  await checkpoint(2);
  driver.milestone("before-start");
  await button("Start");
  await wait(`document.body.textContent.includes('ONBOARDING_REPLY_72e')`, "real producer reply");
  driver.milestone("reply");
  const firstComposer = await read(`document.querySelector('[data-composer]').getAttribute('data-composer')`);
  // A second launch is deliberately blank. Production routing, not injected
  // client state, must open a session with an editable composer and no turn.
  await navigateTo(driver.page, new URL("/new", args.url).href);
  await wait(`!!document.querySelector('[data-testid="spawn-submit"]')`, "configured user new session");
  assert.equal(await read(`!!document.querySelector('[data-testid="spawn-provider-prerequisite"]')`), false);
  await fill('[contenteditable="true"]', "");
  await button("Start");
  const blank = `[data-composer]:not([data-composer=${JSON.stringify(firstComposer)}]) [contenteditable="true"]`;
  await wait(`!!document.querySelector(${JSON.stringify(blank)})`, "new blank session composer");
  assert.equal(await read(`document.querySelector(${JSON.stringify(blank)}).textContent`), "");
  driver.milestone("blank-composer");
  console.log(
    "PASS prerequisite ordering, failed check/retry, explicit model, retained draft/directory, reply, blank composer",
  );
} catch (error) {
  try {
    writeFileSync(path.join(args["artifact-dir"], "page.txt"), await read("document.body.innerText"));
    console.error(await read("document.body.innerText"));
  } catch {}
  throw error;
} finally {
  await stop();
}
