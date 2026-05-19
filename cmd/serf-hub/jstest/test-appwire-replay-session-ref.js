const fs = require("fs");
const path = require("path");
const vm = require("vm");

const appwireSrc = fs.readFileSync(path.resolve(__dirname, "../assets/appwire.js"), "utf8");

const window = {
  location: { protocol: "http:", host: "127.0.0.1:9180" },
};
const context = vm.createContext({
  window,
  WebSocket: function WebSocket() {},
  console,
});
vm.runInContext(appwireSrc, context, { filename: "appwire.js" });

const events = window.SerfAppwire.eventsFromThread({
  id: "th_codex",
  sessionId: "th_codex",
  modelProvider: "gpt-5",
  serf: { ref: "codex:th_codex" },
  turns: [{
    id: "turn_1",
    status: "completed",
    items: [{
      type: "userMessage",
      text: "look",
      images: [
        { type: "input_image", mediaType: "image/png", data: "aW1n", name: "data-url.png" },
        { type: "input_image", url: "https://example.com/screenshot.png", name: "url.png" },
      ],
    }],
  }],
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

const userInput = events.find((event) => event[0] === "USER_INPUT");
if (!userInput) {
  failures.push("missing USER_INPUT replay event");
} else {
  const images = userInput[1].images || [];
  if (images.length !== 2) {
    failures.push(`images.length=${images.length}, want 2`);
  } else {
    if (images[0].data !== "aW1n" || images[0].media_type !== "image/png") {
      failures.push(`data image=${JSON.stringify(images[0])}, want base64 data image`);
    }
    if (images[1].url !== "https://example.com/screenshot.png") {
      failures.push(`url image=${JSON.stringify(images[1])}, want URL preserved`);
    }
  }
}

if (failures.length > 0) {
  for (const failure of failures) console.log("FAIL: " + failure);
  process.exit(1);
}

console.log("PASS: appwire replay preserves source-qualified session ref");
