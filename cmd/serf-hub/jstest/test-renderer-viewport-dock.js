const assert = require("assert");
const { JSDOM } = require("jsdom");

const { window } = new JSDOM(`<!doctype html><html><body class="app">
  <main id="workspace"><div id="conversation" data-session-id="sess-1" data-state="idle"></div></main>
  <form class="workspace-input" data-input-form><textarea class="message-input"></textarea></form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

let frame = null;
window.requestAnimationFrame = (fn) => { frame = fn; return 1; };
window.cancelAnimationFrame = () => { frame = null; };
const listeners = new Map();
const removedListeners = [];
window.visualViewport = {
  height: 700,
  addEventListener: (name, fn) => listeners.set(name, fn),
  removeEventListener: (name, fn) => {
    removedListeners.push([name, fn]);
    if (listeners.get(name) === fn) listeners.delete(name);
  },
};
window.marked = { parse: (text) => text };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]) });

require("./load-renderer").evalRenderer(window);

const renderer = window.SerfRenderer;
renderer.init(window.document.getElementById("conversation"));

const workspace = window.document.getElementById("workspace");
assert.strictEqual(workspace.style.getPropertyValue("--workspace-visible-height"), "700px",
  "init should set the visible viewport height on the current workspace");

window.visualViewport.height = 420;
const oldResizeListener = listeners.get("resize");
const oldScrollListener = listeners.get("scroll");
oldResizeListener();
oldScrollListener();
assert.ok(frame, "viewport events should schedule one animation frame");
const staleFrame = frame;
staleFrame();
assert.strictEqual(workspace.style.getPropertyValue("--workspace-visible-height"), "420px",
  "the scheduled frame should apply the current visual viewport height");

const newWorkspace = window.document.createElement("main");
newWorkspace.id = "workspace";
const newConversation = window.document.createElement("div");
newConversation.id = "conversation";
newConversation.dataset.sessionId = "sess-2";
newConversation.dataset.state = "idle";
newWorkspace.appendChild(newConversation);
workspace.replaceWith(newWorkspace);

window.visualViewport.height = 333;
oldResizeListener();
assert.ok(frame, "the old listener should be able to schedule a frame before cleanup");
const staleFrameAfterReplacement = frame;
renderer.init(newConversation);

assert.ok(removedListeners.some(([name, fn]) => name === "resize" && fn === oldResizeListener),
  "session replacement should remove the old resize listener");
assert.ok(removedListeners.some(([name, fn]) => name === "scroll" && fn === oldScrollListener),
  "session replacement should remove the old scroll listener");
assert.notStrictEqual(listeners.get("resize"), oldResizeListener, "new session should own a new resize listener");
assert.notStrictEqual(listeners.get("scroll"), oldScrollListener, "new session should own a new scroll listener");
assert.strictEqual(newWorkspace.style.getPropertyValue("--workspace-visible-height"), "333px",
  "replacement session should synchronously bind its current workspace");

newWorkspace.style.setProperty("--workspace-visible-height", "new-session");
staleFrameAfterReplacement();
assert.strictEqual(newWorkspace.style.getPropertyValue("--workspace-visible-height"), "new-session",
  "a stale viewport callback must not mutate the replacement workspace");

console.log("PASS: renderer tracks visual viewport for composer dock");
process.exit(0);
