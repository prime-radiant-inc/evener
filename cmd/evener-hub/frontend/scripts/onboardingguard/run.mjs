#!/usr/bin/env node
// Real production hub UI. The Go test owns only the external HTTP provider and
// the scripted OAuth issuer's external HTTP boundary.
// Reuse skillguard's exported Chrome lifecycle/input driver, never its fixtures.
import assert from "node:assert/strict";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { evaluate, navigateTo } from "../browserGuardCdp.mjs";
import { Driver } from "../skillguard/run.mjs";
import { button as activateButton } from "./button.mjs";
import { captureAccessViews, captureJourneyViews, record } from "./record.mjs";

const args = Object.fromEntries(
  Array.from({ length: (process.argv.length - 2) / 2 }, (_, i) => [
    process.argv[2 + i * 2].slice(2),
    process.argv[3 + i * 2],
  ]),
);
const scenario = args.scenario ?? "auto";
const task = "ONBOARDING_TASK_72e";
const adopted = args["adopted-model"] ?? "openai/gpt-4o";
const evidenceDir = args["evidence-dir"];
if (evidenceDir) mkdirSync(evidenceDir, { recursive: true });
const driver = new Driver({
  url: args.url,
  artifactDir: args["artifact-dir"],
  milestonePath: path.join(args["artifact-dir"], "milestones.jsonl"),
});
const read = (source) => evaluate(driver.send, source);
const pause = async (ms = 1800) => {
  if (args["record-video"]) await new Promise((resolve) => setTimeout(resolve, ms));
};
const wait = (source, label, options = {}) => driver.waitPage(source, { label, ...options });
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
// Minimal 1x1 PNG fixture written into the artifact directory so
// DOM.setFileInputFiles hands the browser a real local file.
function fixtureImage() {
  const filePath = path.join(args["artifact-dir"], "onboarding-fixture.png");
  writeFileSync(
    filePath,
    Buffer.from(
      "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
      "base64",
    ),
  );
  return filePath;
}
async function attachImage() {
  const document = await driver.send("DOM.getDocument", { depth: -1 });
  const rootId = document.result.root.nodeId;
  const found = await driver.send("DOM.querySelector", {
    nodeId: rootId,
    selector: 'input[type=file][accept="image/*"]',
  });
  if (!found.result?.nodeId) throw new Error("spawn file input not found");
  await driver.send("DOM.setFileInputFiles", { files: [fixtureImage()], nodeId: found.result.nodeId });
  await wait(
    `(() => {
      const tiles = [...document.querySelectorAll('[data-testid="spawn-attachments"] [data-testid="attachment-tile"]')];
      return tiles.length === 1 && tiles.every((t) => t.querySelector("button[aria-label^='View ']") !== null);
    })()`,
    "attachment decoded",
    { timeoutMs: 30000 },
  );
}
async function checkpoint(minChecks, stage) {
  const url = stage ? `${args.checkpoint}?stage=${encodeURIComponent(stage)}` : args.checkpoint;
  const response = await fetch(url);
  const body = await response.text();
  assert.equal(response.status, 200, body);
  if (minChecks !== undefined) assert.ok(JSON.parse(body).checks >= minChecks, body);
  return body;
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
async function chooseWorkingDirectory() {
  await driver.click("#spawn-cwd");
  await wait(hasButton("Use this folder"), "initial directory resolved");
  await fill('input[aria-label="Path"]', args.work);
  await pause();
  await button("Go");
  await button("Use this folder");
}
// The draft settles without any Retry, Continue or model-picker action: the
// first model-list failure recovers on the connector's own timer, the dialog
// closes, and only the connected instance's fresh usable configured default is
// adopted. Focus, task, directory and attachment stay as the person left them.
async function waitForAutomaticCompletion() {
  await wait(
    `!document.querySelector('[role="dialog"]') &&
     !document.querySelector('[data-testid="spawn-provider-prerequisite"]') &&
     document.querySelector('[data-testid="spawn-model-slot"]')?.dataset.state === 'chosen' &&
     (document.querySelector('[data-testid="spawn-model-value"]')?.textContent ?? '').includes(${JSON.stringify(adopted)})`,
    "automatic completion adopted the configured default",
    { timeoutMs: 45000 },
  );
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
  await fill('[contenteditable="true"]', task);
  await pause();
  await attachImage();
  await chooseWorkingDirectory();

  if (scenario === "auto") {
    await button("Connect provider");
    await button("OpenAI");
    await fill("#provider-credential", "onboarding-fixture-key-not-a-secret");
    await button("Save and check");
    await waitForAutomaticCompletion();
    await pause(3000);
    // The connector's own first failure and retry are what made this return
    // possible; no manual recovery action ran. The checkpoint also proves no
    // session, launch or generation exists before Start.
    await checkpoint(2);
    assert.equal(await read(`document.querySelector('[contenteditable="true"]').textContent.includes(${JSON.stringify(task)})`), true);
    assert.ok(
      await read(
        `document.querySelector('#spawn-cwd').getAttribute('aria-label').includes(${JSON.stringify(args.work)})`,
      ),
      "working directory retained",
    );
    assert.equal(
      await read(`document.querySelectorAll('[data-testid="spawn-attachments"] [data-testid="attachment-tile"]').length`),
      1,
      "attachment retained",
    );
    await captureJourneyViews(driver, {
      artifactDir: args["artifact-dir"],
      evidenceDir,
      label: "automatic",
      phase: "draft",
      work: args.work,
      task,
      adopted,
      strictFocus: true,
    });
    await driver.send("Emulation.setDeviceMetricsOverride", { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
    await driver.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: "light" }] });
    await pause(5000);
    await start();
    await wait(`${active}?.reply === true`, "active session producer reply");
    const firstSession = await read(active);
    driver.milestone("session-observed", firstSession);
    await pause(3000);
    await captureJourneyViews(driver, {
      artifactDir: args["artifact-dir"],
      evidenceDir,
      label: "automatic",
      phase: "session",
      work: args.work,
      task,
      adopted,
    });
    await driver.send("Emulation.setDeviceMetricsOverride", { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
    await pause(2000);
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
      "PASS prerequisite ordering, automatic transient retry/completion, configured instance default adoption, retained task/directory/attachment, explicit Start, visible reply, blank composer",
    );
  } else {
    // OAuth journeys: the connector offers sign-in only for an instance whose
    // scheme is the Codex device/redirect flow. Every step below is production
    // UI and real AppWire; the Go owner scripts only the external issuer.
    const providerLabel = scenario === "oauth-redirect" ? "OpenAI Codex" : "OpenAI Codex";
    await button("Connect provider");
    await button(providerLabel);
    await button("Sign in");
    if (scenario === "oauth-device" || scenario === "oauth-cancel" || scenario === "oauth-endpoint-change") {
      await wait(
        `!!document.querySelector('[role="dialog"]') && [...document.querySelectorAll('[role="dialog"] [role="status"]')].some(s => s.dataset.state === 'waiting-for-authorization')`,
        "device authorization pending",
      );
      await checkpoint(undefined, "oauth-pending");
      if (scenario === "oauth-cancel") {
        await button("Cancel");
        // Cancelling the sign-in editor returns to the connection editor; the
        // outer guided dialog stays until access is actually acquired.
        await wait(
          `!document.querySelector('[role="status"][data-state="waiting-for-authorization"]') && ${hasButton("Sign in")}`,
          "cancelled device flow closed",
        );
        await pause(3500);
        // The cancelled flow must not check or complete later.
        await checkpoint(undefined, "oauth-cancelled");
        // Recovery: a fresh sign-in on the still-open connection editor
        // completes automatically.
        await button("Sign in");
      }
      if (scenario === "oauth-endpoint-change") {
        await checkpoint(undefined, "oauth-endpoint-change");
        await wait(
          `!document.querySelector('[role="status"][data-state="waiting-for-authorization"]') &&
           document.querySelector('[data-reason="changed"]') !== null`,
          "changed destination refused",
        );
        await pause(3000);
        await checkpoint(undefined, "oauth-endpoint-changed");
        console.log("PASS endpoint change refused the pending sign-in with its changed alert");
        await stop();
        process.exit(0);
      }
      await waitForAutomaticCompletion();
    } else {
      await wait(`!!document.querySelector('#oauth-redirect-url')`, "redirect paste-back editor");
      const authUrl = await read(`document.querySelector('[role="dialog"] a[href*="/oauth/authorize"]')?.href ?? ''`);
      assert.ok(authUrl, "authorize URL visible");
      const state = new URL(authUrl).searchParams.get("state");
      assert.ok(state, "authorize URL carries state");
      await fill(
        "#oauth-redirect-url",
        `http://localhost:1455/auth/callback?code=onboarding-fixture-code&state=${encodeURIComponent(state)}`,
      );
      await button("Finish");
      await waitForAutomaticCompletion();
    }
    await pause(3000);
    // At least the acquisition's own automatic access check must have run; the
    // key journey separately requires the fixture's failure and retry.
    await checkpoint(1);
    // The acquisition's own background listing refresh must leave this
    // destination alone: a moved row would have retired the flow with the
    // changed alert instead of completing it.
    assert.equal(
      await read(`document.querySelector('[data-reason="changed"]') === null`),
      true,
      "background listing refresh did not move the reviewed destination",
    );
    const oauthSettled = await read(`(() => {
      const button = document.querySelector('[data-testid="spawn-submit"]');
      const slot = document.querySelector('[data-testid="spawn-model-slot"]');
      const value = document.querySelector('[data-testid="spawn-model-value"]');
      const rect = button?.getBoundingClientRect();
      return { visible: !!rect && rect.width > 0 && rect.height > 0, enabled: !!button && !button.disabled,
        modelState: slot?.dataset.state ?? 'missing', model: value?.textContent?.trim() ?? '' };
    })()`);
    assert.equal(oauthSettled.visible && oauthSettled.enabled, true, `explicit Start enabled after automatic OAuth completion (${JSON.stringify(oauthSettled)})`);
    assert.equal(oauthSettled.modelState, "chosen", "OAuth completion adopted the configured instance default");
    assert.ok(oauthSettled.model.includes(adopted), `OAuth adopted model ${oauthSettled.model} missing ${adopted}`);
    assert.equal(await read(`document.querySelector('[contenteditable="true"]').textContent.includes(${JSON.stringify(task)})`), true);
    await captureJourneyViews(driver, {
      artifactDir: args["artifact-dir"],
      evidenceDir,
      label: scenario,
      phase: "draft",
      work: args.work,
      task,
      adopted,
      strictFocus: true,
      requireChosen: true,
    });
    console.log(`PASS ${scenario}: real issuer flow, automatic access check and completion, explicit Start, no pre-Start generation`);
  }
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
