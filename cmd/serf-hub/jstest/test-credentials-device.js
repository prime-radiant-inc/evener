// Loads credentials.html's inline script into JSDOM, mocks the device-code RPC
// wrappers, and asserts: the device editor renders the user code + verification
// link and polls to "authorized"; and that a fallback response switches to the
// paste-back (oauth-redirect) editor.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

function assert(cond, msg) { if (!cond) { console.error("FAIL: " + msg); process.exit(1); } }

const html = fs.readFileSync(path.resolve(__dirname, "../templates/partials/credentials.html"), "utf8");
const src = html.match(/<script>([\s\S]*?)<\/script>/)[1];

function makeDom() {
  return new JSDOM(`<!DOCTYPE html><html><body>
    <section id="credentials-rows" class="settings-collection" data-loaded="false">
      <header class="settings-collection-head"><h3>Providers</h3><span class="settings-collection-count" data-count></span></header>
      <ul class="settings-collection-list" role="list"><li class="settings-collection-empty">Loading…</li></ul>
    </section></body></html>`,
    { runScripts: "outside-only", pretendToBeVisual: true, url: "http://127.0.0.1:9180/credentials" });
}
const wait = (dom, ms) => new Promise((r) => dom.window.setTimeout(r, ms));
const openaiRow = (dom) => dom.window.document.querySelector('li[data-provider="openai"]');

(async function main() {
  // Case 1: device flow renders code + verification link, polls to authorized.
  {
    const dom = makeDom();
    let listed = [{ provider: "openai", supported: true, signedIn: false, activeSource: "absent", authModes: ["apiKey", "oauth"] }];
    let pollCalls = 0;
    dom.window.launchconfig = {
      authList: async () => ({ providers: listed }),
      authDeviceStart: async () => ({ provider: "openai", flowId: "f1", userCode: "WXYZ-1234", verificationUrl: "https://auth.openai.com/codex/device", intervalSeconds: 1 }),
      authDevicePoll: async () => { pollCalls++; if (pollCalls >= 2) { listed = [{ provider: "openai", supported: true, signedIn: true, activeSource: "oauth", authModes: ["apiKey", "oauth"], hasStoredOAuth: true }]; return { state: "authorized", status: listed[0] }; } return { state: "pending" }; },
      authLoginStart: async () => ({ flowId: "x", url: "https://x" }),
    };
    dom.window.open = () => null;
    dom.window.eval(src);
    const section = dom.window.document.getElementById("credentials-rows");
    for (let i = 0; i < 100 && section.dataset.loaded !== "true"; i++) await wait(dom, 0);
    openaiRow(dom).querySelector('button[data-action="oauth"]').click();
    for (let i = 0; i < 100 && !openaiRow(dom).querySelector('[data-editor="device"]'); i++) await wait(dom, 0);
    const editor = openaiRow(dom).querySelector('[data-editor="device"]');
    assert(editor, "device editor should render after clicking Sign in");
    assert(/WXYZ-1234/.test(editor.textContent), "device editor should show the user code");
    assert(editor.querySelector('a[href="https://auth.openai.com/codex/device"]'), "device editor should link to the verification URL");
    for (let i = 0; i < 400 && openaiRow(dom).querySelector('[data-editor="device"]'); i++) await wait(dom, 10);
    assert(!openaiRow(dom).querySelector('[data-editor="device"]'), "device editor should close after authorized");
    assert(/oauth/.test(openaiRow(dom).textContent), "openai row should show oauth after authorized");
  }

  // Case 2: fallback switches to the paste-back (oauth-redirect) editor.
  {
    const dom = makeDom();
    dom.window.launchconfig = {
      authList: async () => ({ providers: [{ provider: "openai", supported: true, signedIn: false, activeSource: "absent", authModes: ["apiKey", "oauth"] }] }),
      authDeviceStart: async () => ({ provider: "openai", fallback: true }),
      authLoginStart: async () => ({ flowId: "f2", url: "https://auth.openai.com/oauth/authorize?x=1" }),
      authDevicePoll: async () => ({ state: "pending" }),
    };
    dom.window.open = () => null;
    dom.window.eval(src);
    const section = dom.window.document.getElementById("credentials-rows");
    for (let i = 0; i < 100 && section.dataset.loaded !== "true"; i++) await wait(dom, 0);
    openaiRow(dom).querySelector('button[data-action="oauth"]').click();
    for (let i = 0; i < 100 && !openaiRow(dom).querySelector('[data-editor="oauth-redirect"]'); i++) await wait(dom, 0);
    assert(openaiRow(dom).querySelector('[data-editor="oauth-redirect"]'), "fallback should open the paste-back editor");
  }

  console.log("test-credentials-device.js: OK");
})().catch((err) => { console.error(err && err.stack ? err.stack : err); process.exit(1); });
