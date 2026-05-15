const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const appwireSrc = fs.readFileSync(path.resolve(__dirname, "../assets/appwire.js"), "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body></body></html>`, {
  runScripts: "outside-only",
  url: "http://127.0.0.1:9180/s/codex:th_codex",
});

dom.window.eval(appwireSrc);

const events = dom.window.SerfAppwire.eventsFromThread({
  id: "th_codex",
  sessionId: "th_codex",
  modelProvider: "gpt-5",
  serf: { ref: "codex:th_codex" },
  turns: [],
});

const start = events[0] && events[0][1];
const failures = [];
if (!start) {
  failures.push("missing SESSION_START");
} else {
  if (start.session_id !== "codex:th_codex") {
    failures.push(`session_id=${JSON.stringify(start.session_id)}, want "codex:th_codex"`);
  }
  if (start.ref !== "codex:th_codex") {
    failures.push(`ref=${JSON.stringify(start.ref)}, want "codex:th_codex"`);
  }
}

if (failures.length > 0) {
  for (const failure of failures) console.log("FAIL: " + failure);
  process.exit(1);
}

console.log("PASS: appwire replay preserves source-qualified session ref");
