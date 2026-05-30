// Loads credentials.html's inline script into JSDOM, mocks the device-code RPC
// wrappers (keyed by instance name), and asserts the copy-first device flow:
// the editor shows the code without auto-opening OpenAI, copying enables the
// "Send me to OpenAI" button which opens the verification URL, polling reaches
// "authorized"; and that a fallback response switches to the paste-back
// (oauth-redirect) editor.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

function assert(cond, msg) { if (!cond) { console.error("FAIL: " + msg); process.exit(1); } }

const html = fs.readFileSync(path.resolve(__dirname, "../templates/partials/credentials.html"), "utf8");
const src = html.match(/<script>([\s\S]*?)<\/script>/)[1];

function makeDom() {
  return new JSDOM(`<!DOCTYPE html><html><body>
    <button type="button" id="btn-add-provider-instance">+ Add provider instance</button>
    <section id="instances-root" class="settings-collection" data-loaded="false">
      <ul class="settings-collection-list" role="list"><li class="settings-collection-empty">Loading…</li></ul>
    </section></body></html>`,
    { runScripts: "outside-only", pretendToBeVisual: true, url: "http://127.0.0.1:9180/credentials" });
}
const wait = (dom, ms) => new Promise((r) => dom.window.setTimeout(r, ms));
const openaiRow = (dom) => dom.window.document.querySelector('li[data-instance="openai"]');
async function waitLoaded(dom) {
  const root = dom.window.document.getElementById("instances-root");
  for (let i = 0; i < 100 && root.dataset.loaded !== "true"; i++) await wait(dom, 0);
  assert(root.dataset.loaded === "true", "instances list should finish loading");
}

(async function main() {
  // Case 1: copy-first device flow — no auto-open; copying the code enables the
  // "Send me to OpenAI" button; clicking it opens the verification URL; polling
  // then reaches authorized and closes the editor.
  {
    const dom = makeDom();
    let listed = [{ name: "openai", type: "openai", authModes: ["apiKey", "oauth"], activeSource: "absent" }];
    let pollCalls = 0;
    const opened = [];
    dom.window.launchconfig = {
      instanceList: async () => ({ instances: listed, availableTypes: ["openai"] }),
      authDeviceStart: async () => ({ flowId: "f1", userCode: "WXYZ-1234", verificationUrl: "https://auth.openai.com/codex/device", intervalSeconds: 1 }),
      authDevicePoll: async () => { pollCalls++; if (pollCalls >= 2) { listed = [{ name: "openai", type: "openai", authModes: ["apiKey", "oauth"], activeSource: "oauth", hasStoredOAuth: true }]; return { state: "authorized" }; } return { state: "pending" }; },
      authLoginStart: async () => ({ flowId: "x", url: "https://x" }),
    };
    dom.window.open = (url) => { opened.push(url); return null; };
    dom.window.document.execCommand = () => true; // legacy clipboard path (insecure http)
    dom.window.eval(src);
    await waitLoaded(dom);
    openaiRow(dom).querySelector('button[data-action="oauth"]').click();
    for (let i = 0; i < 100 && !openaiRow(dom).querySelector('[data-editor="device"]'); i++) await wait(dom, 0);
    const editor = openaiRow(dom).querySelector('[data-editor="device"]');
    assert(editor, "device editor should render after clicking Sign in");
    assert(/WXYZ-1234/.test(editor.textContent), "device editor should show the user code");
    assert(opened.length === 0, "must NOT auto-open OpenAI before the user copies the code");
    assert(editor.querySelector('button[data-action="device-copy"]'), "Copy code button should be present");
    const sendBefore = editor.querySelector('button[data-action="device-open"]');
    assert(sendBefore && sendBefore.disabled, "Send-to-OpenAI button should be disabled before copy");
    // copy the code → enables Send
    openaiRow(dom).querySelector('button[data-action="device-copy"]').click();
    for (let i = 0; i < 100 && openaiRow(dom).querySelector('button[data-action="device-open"]').disabled; i++) await wait(dom, 0);
    assert(!openaiRow(dom).querySelector('button[data-action="device-open"]').disabled, "Send-to-OpenAI should enable after copying");
    // clicking Send opens the verification URL
    openaiRow(dom).querySelector('button[data-action="device-open"]').click();
    assert(opened.includes("https://auth.openai.com/codex/device"), "Send-to-OpenAI should open the verification URL");
    // polling completes
    for (let i = 0; i < 400 && openaiRow(dom).querySelector('[data-editor="device"]'); i++) await wait(dom, 10);
    assert(!openaiRow(dom).querySelector('[data-editor="device"]'), "device editor should close after authorized");
    const oauthBtn = openaiRow(dom).querySelector('button[data-action="oauth"]');
    assert(oauthBtn && /Refresh OAuth/.test(oauthBtn.textContent), "openai row should show the signed-in (Refresh OAuth) state after authorized");
  }

  // Case 2: fallback switches to the paste-back (oauth-redirect) editor.
  {
    const dom = makeDom();
    dom.window.launchconfig = {
      instanceList: async () => ({ instances: [{ name: "openai", type: "openai", authModes: ["apiKey", "oauth"], activeSource: "absent" }], availableTypes: ["openai"] }),
      authDeviceStart: async () => ({ fallback: true }),
      authLoginStart: async () => ({ flowId: "f2", url: "https://auth.openai.com/oauth/authorize?x=1" }),
      authDevicePoll: async () => ({ state: "pending" }),
    };
    dom.window.open = () => null;
    dom.window.eval(src);
    await waitLoaded(dom);
    openaiRow(dom).querySelector('button[data-action="oauth"]').click();
    for (let i = 0; i < 100 && !openaiRow(dom).querySelector('[data-editor="oauth-redirect"]'); i++) await wait(dom, 0);
    assert(openaiRow(dom).querySelector('[data-editor="oauth-redirect"]'), "fallback should open the paste-back editor");
  }

  console.log("test-credentials-device.js: OK");
})().catch((err) => { console.error(err && err.stack ? err.stack : err); process.exit(1); });
