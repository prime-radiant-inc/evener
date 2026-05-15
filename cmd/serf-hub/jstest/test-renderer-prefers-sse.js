const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const rendererSrc = fs.readFileSync(path.resolve(__dirname, "../assets/renderer.js"), "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div id="conversation"
       data-session-id="codex:th_codex"
       data-replay-url=""
       data-events-url="/s/codex%3Ath_codex/events"
       data-state="idle"></div>
  <form data-input-form data-session-id="codex:th_codex">
    <textarea class="message-input"></textarea>
    <button class="send-btn" type="submit">send</button>
  </form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: (t) => t };

let readThreadCalls = 0;
window.SerfAppwire = {
  tasks: () => Promise.resolve([]),
  onNotification: () => () => {},
  readThread: () => {
    readThreadCalls++;
    return Promise.resolve({ thread: { turns: [] } });
  },
};

class MockEventSource {
  constructor(url) {
    this.url = url;
    this.listeners = new Map();
    MockEventSource.instances.push(this);
  }
  addEventListener(name, fn) {
    const listeners = this.listeners.get(name) || [];
    listeners.push(fn);
    this.listeners.set(name, listeners);
  }
  set onerror(_) {}
  close() {}
}
MockEventSource.instances = [];
window.EventSource = MockEventSource;

window.eval(rendererSrc);
window.SerfRenderer.init(window.document.getElementById("conversation"));

setTimeout(() => {
  const failures = [];
  const source = MockEventSource.instances[0];
  if (!source || source.url !== "/s/codex%3Ath_codex/events") {
    failures.push("FAIL: renderer did not open the workspace SSE events URL");
  }
  if (readThreadCalls !== 0) {
    failures.push("FAIL: renderer used AppWire transcript hydration despite events URL");
  }
  if (failures.length) {
    for (const failure of failures) console.log(failure);
    process.exit(1);
  }
  console.log("PASS: renderer prefers workspace SSE when events URL is present");
  process.exit(0);
}, 20);
