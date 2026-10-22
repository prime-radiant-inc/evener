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
import { captureAccessViews, captureAcquisition, captureSession, record } from "./record.mjs";

const args = Object.fromEntries(
  Array.from({ length: (process.argv.length - 2) / 2 }, (_, i) => [
    process.argv[2 + i * 2].slice(2),
    process.argv[3 + i * 2],
  ]),
);
const scenario = args.scenario ?? "auto";
const width = Number(args.width ?? 1440);
const height = Number(args.height ?? (width === 390 ? 844 : 1000));
const theme = args.theme ?? "dark";
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
// A stage that records another process's progress: poll the Go checkpoint until
// it observes the condition, rather than sleeping a guessed interval.
async function waitStage(stage, timeoutMs = 15000) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const response = await fetch(`${args.checkpoint}?stage=${encodeURIComponent(stage)}`);
    const body = await response.text();
    if (response.status === 200) return body;
    if (Date.now() > deadline) throw new Error(`stage ${stage} not observed: ${body}`);
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
}
// A passive tap on the page's real WebSocket. It records the AppWire frames the
// production client sends and receives and changes neither, so the guard can
// wait for the device-poll RPC's actual reply instead of mistaking the issuer's
// receipt of the token request for production's completion. Installed before
// the document loads.
const appWireTap = `(() => {
  const Native = window.WebSocket;
  if (!Native) return;
  const records = { sent: [], received: [] };
  Object.defineProperty(window, "__onboardingAppWireFrames", { value: records, enumerable: false });
  class ObservedWebSocket extends Native {
    constructor(...args) {
      super(...args);
      this.addEventListener("message", (event) => {
        if (typeof event.data === "string") records.received.push(event.data);
      });
    }
    send(data) {
      if (typeof data === "string") records.sent.push(data);
      return super.send(data);
    }
  }
  window.WebSocket = ObservedWebSocket;
})()`;
function parseFrame(frame) {
  try {
    return JSON.parse(frame);
  } catch {
    return null;
  }
}
// The request the browser actually sent. Its id identifies the reply on the
// same wire; matching the method keeps a later recovery flow from standing in
// for the retired operation.
async function observedRequest(method, label) {
  const sent = JSON.parse(await read(`JSON.stringify(window.__onboardingAppWireFrames?.sent ?? [])`));
  const frames = sent.map(parseFrame);
  const found = frames.find((frame) => frame?.method === method);
  assert.ok(found, `${label}: the browser never sent ${method} (saw ${JSON.stringify(frames.map((f) => f?.method))})`);
  return found;
}
// The request is sent by a flow that first saves and refreshes, so it may
// arrive after the click returns. Poll for it instead of racing the handoff.
async function waitObservedRequest(method, label, timeoutMs = 20000) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const sent = JSON.parse(await read(`JSON.stringify(window.__onboardingAppWireFrames?.sent ?? [])`));
    const found = sent.map(parseFrame).find((frame) => frame?.method === method);
    if (found) return found;
    if (Date.now() > deadline) {
      throw new Error(`${label}: the browser never sent ${method} (saw ${JSON.stringify(sent.map(parseFrame).map((f) => f?.method))})`);
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
}
// Wait for the real reply to one already-sent request. Production writes this
// frame only after the RPC handler returned, which for device polling means
// the token exchange, the endpoint verification and the credential write or
// refusal have all run.
async function observedResponse(requestId, label, timeoutMs = 20000) {
  await wait(
    `(() => {
      const records = window.__onboardingAppWireFrames;
      if (!records) return false;
      return records.received.some((frame) => {
        try { return JSON.parse(frame).id === ${JSON.stringify(requestId)}; } catch { return false; }
      });
    })()`,
    `${label}: real RPC reply`,
    { timeoutMs },
  );
  const frames = JSON.parse(await read(`JSON.stringify(window.__onboardingAppWireFrames.received)`)).map(parseFrame);
  return frames.find((frame) => frame?.id === requestId);
}
// One page task turn after the observed reply, so the client's message handler
// and the continuation it releases run before the UI state is read. The reply
// is the synchronization; this only lets the page drain what it started.
async function browserHandledRpcReply() {
  await read(`new Promise((resolve) => setTimeout(() => resolve(true), 0))`);
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
  // A raw pointer click can miss the directory control in the narrow layout's
  // responsive chrome; activate its real handler in the page instead.
  await evaluate(driver.send, `document.querySelector('#spawn-cwd').click()`);
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
  await driver.page.send("Page.addScriptToEvaluateOnNewDocument", { source: appWireTap });
  // The journey runs in its target layout from the moment the UI opens: the
  // acquisition, dismissal and focus restoration below happen at this size and
  // theme, not after a later resize.
  await navigateTo(driver.page, args.url);
  await driver.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile: false });
  await driver.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: theme }] });
  await wait(
    `(document.documentElement.dataset.theme ?? 'dark') === ${JSON.stringify(theme)} && innerWidth === ${width} && innerHeight === ${height}`,
    `${width}x${height} ${theme} target layout`,
  );
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
    await button("Save and test");
    // The automatic path must survive a real failed check: observe the
    // connector's own evener/auth/test reply as a failure, then let the
    // fixture answer healthy, so completion requires the connector's own
    // retry timer rather than another reader consuming a one-off 503.
    const firstCheck = await waitObservedRequest("evener/auth/test", "automatic check");
    const firstReply = await observedResponse(firstCheck.id, "automatic check");
    assert.equal(firstReply?.result?.status, "endpoint_failure", JSON.stringify(firstReply));
    await browserHandledRpcReply();
    await checkpoint(undefined, "auto-check-failed");
    await waitForAutomaticCompletion();
    await pause(3000);
    // The checkpoint also proves no session, launch or generation exists
    // before Start.
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
    await captureAcquisition(driver, {
      artifactDir: args["artifact-dir"],
      evidenceDir,
      label: "automatic",
      width,
      height,
      theme,
      work: args.work,
      task,
      adopted,
    });
    await pause(5000);
    await start();
    const mobile = width <= 390;
    let firstSession;
    if (mobile) {
      await wait(`document.body.innerText.includes('ONBOARDING_REPLY_72e') && !document.querySelector('[role="dialog"]')`, "mobile session producer reply");
      // The narrow layout has no dock tab strip, but the rendered session pane
      // still carries the real hub ref the Go owner joins to its daemon.
      const ref = await read(`(() => {
        const bodies = [...document.querySelectorAll('[data-pane-scaffold^="session:"]')].filter((e) => e.getBoundingClientRect().width > 0);
        return bodies.length === 1 ? bodies[0].dataset.paneScaffold.slice("session:".length) : null;
      })()`);
      assert.ok(ref, "mobile session ref");
      firstSession = { ref };
    } else {
      await wait(`${active}?.reply === true`, "active session producer reply");
      firstSession = await read(active);
    }
    driver.milestone("session-observed", firstSession);
    await pause(3000);
    await captureSession(driver, {
      artifactDir: args["artifact-dir"],
      evidenceDir,
      label: "automatic",
      width,
      height,
      theme,
    });
    await pause(2000);
    if (!mobile) {
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
    }
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
    const providerLabel = "OpenAI Codex";
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
        // The issuer holds the first poll until the user's intent is settled.
        await waitStage("oauth-poll-observed");
        // Capture the real in-flight poll before cancelling, so its reply (not
        // a later recovery flow's) is what settlement waits for.
        const retiredPoll = await observedRequest("evener/auth/device/poll", "cancelled sign-in");
        await button("Cancel");
        // Cancelling the sign-in editor returns to the connection editor; the
        // outer guided dialog stays until access is actually acquired.
        await wait(
          `!document.querySelector('[role="status"][data-state="waiting-for-authorization"]') && ${hasButton("Sign in")}`,
          "cancelled device flow closed",
        );
        // The state the person left behind: the connection editor's focus owner
        // and a draft that has not adopted anything. The held late success
        // must not change either.
        const intent = `(() => ({
          focusId: document.activeElement?.id ?? '',
          focusText: (document.activeElement?.textContent ?? '').trim(),
          model: document.querySelector('[data-testid="spawn-model-slot"]')?.dataset.state ?? 'missing',
          modelValue: document.querySelector('[data-testid="spawn-model-value"]')?.textContent?.trim() ?? '',
          saved: document.querySelector('[data-testid="credential-saved"]') !== null
        }))()`;
        const beforeLate = await read(intent);
        assert.equal(beforeLate.saved, false, "cancel itself saved nothing");
        await checkpoint(undefined, "oauth-cancelled-intent");
        // Now the held poll answers with a real success. For this unchanged
        // cancellation destination production saves access; the UI, focus,
        // model and draft effects must stay retired.
        await checkpoint(undefined, "oauth-release-late-success");
        // The held poll now answers. Production receives the tokens and runs
        // its save/refusal before it writes this RPC reply.
        const retiredReply = await observedResponse(retiredPoll.id, "cancelled sign-in");
        assert.equal(retiredReply?.error, undefined, JSON.stringify(retiredReply));
        assert.equal(retiredReply?.result?.state, "authorized", JSON.stringify(retiredReply));
        await browserHandledRpcReply();
        await waitStage("oauth-late-settled");
        const afterLate = await read(intent);
        assert.deepEqual(afterLate, beforeLate, "late success did not mutate the cancelled intent or steal focus");
        assert.equal(
          await read(`document.querySelector('[data-testid="credential-saved"]') === null && document.body.innerText.includes("Signed in to") === false`),
          true,
          "cancelled flow did not complete the UI",
        );
        await checkpoint(undefined, "oauth-cancelled");
        // Recovery: a fresh sign-in on the still-open connection editor
        // completes automatically.
        await button("Sign in");
      }
      if (scenario === "oauth-endpoint-change") {
        await waitStage("oauth-poll-observed");
        const retiredPoll = await observedRequest("evener/auth/device/poll", "changed sign-in");
        await checkpoint(undefined, "oauth-endpoint-change");
        await checkpoint(undefined, "oauth-changed-intent");
        await checkpoint(undefined, "oauth-release-late-success");
        // The changed destination makes the poll's credential write refuse, so
        // the real reply is an error rather than an authorized state. Its
        // arrival still means production finished the exchange and the check.
        const retiredReply = await observedResponse(retiredPoll.id, "changed sign-in");
        assert.ok(retiredReply?.error, `changed sign-in: expected the refusal reply, got ${JSON.stringify(retiredReply)}`);
        await browserHandledRpcReply();
        await waitStage("oauth-late-settled");
        // The listing refresh the instance edit scheduled may itself wait for
        // the in-flight poll; once that completes, the changed destination is
        // what the UI must show. The intent was set before the release.
        await wait(
          `!document.querySelector('[role="status"][data-state="waiting-for-authorization"]') &&
           document.querySelector('[data-reason="changed"]') !== null`,
          "changed destination refused",
        );
        assert.equal(
          await read(`document.querySelector('[data-testid="credential-saved"]') === null`),
          true,
          "changed-destination flow did not save access",
        );
        await checkpoint(undefined, "oauth-endpoint-changed");
        console.log("PASS endpoint change refused the pending sign-in with its changed alert and late success");
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
    await captureAcquisition(driver, {
      artifactDir: args["artifact-dir"],
      evidenceDir,
      label: scenario,
      width,
      height,
      theme,
      work: args.work,
      task,
      adopted,
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
