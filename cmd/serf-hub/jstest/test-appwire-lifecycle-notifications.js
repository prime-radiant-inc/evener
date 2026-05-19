const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const appwireSrc = fs.readFileSync(path.resolve(__dirname, "../assets/appwire.js"), "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body></body></html>`, {
  runScripts: "outside-only",
  url: "http://127.0.0.1:9180/s/01TEST",
});

dom.window.eval(appwireSrc);

const failures = [];
const pass = (condition, message) => { if (!condition) failures.push("FAIL: " + message); };

const started = dom.window.SerfAppwire.eventsFromNotification("thread/started", {
  thread: {
    id: "th_codex",
    sessionId: "th_codex",
    modelProvider: "gpt-5",
    serf: { ref: "codex:th_codex", profile: "openai" },
  },
});
pass(started.length === 1, "thread/started should produce one event");
pass(started[0] && started[0][0] === "SESSION_START", "thread/started should map to SESSION_START");
pass(started[0] && started[0][1].session_id === "codex:th_codex", "SESSION_START should use canonical ref");
pass(started[0] && started[0][1].ref === "codex:th_codex", "SESSION_START should carry ref");
pass(started[0] && started[0][1].model === "gpt-5", "SESSION_START should carry model");
pass(started[0] && started[0][1].profile === "openai", "SESSION_START should carry profile");

const closed = dom.window.SerfAppwire.eventsFromNotification("thread/closed", { reason: "done" });
pass(closed.length === 1, "thread/closed should produce one event");
pass(closed[0] && closed[0][0] === "SESSION_END", "thread/closed should map to SESSION_END");
pass(closed[0] && closed[0][1].reason === "done", "SESSION_END should carry reason");

if (failures.length > 0) {
  for (const failure of failures) console.log(failure);
  process.exit(1);
}

console.log("PASS: appwire lifecycle notifications map to renderer events");
