// Tests for abbreviateModel helper (kata fbfh).
// Covers: provider prefix stripping, date suffix stripping, passthrough cases.

const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const spawnSrc = fs.readFileSync(path.resolve(__dirname, "../assets/spawn.js"), "utf8");

function assert(cond, msg) {
  if (!cond) {
    console.error("FAIL: " + msg);
    process.exit(1);
  }
}

const dom = new JSDOM(`<!DOCTYPE html><html><body></body></html>`, {
  runScripts: "outside-only",
  pretendToBeVisual: true,
  url: "http://127.0.0.1:9180/new",
});

dom.window.eval(spawnSrc);
const { abbreviateModel } = dom.window.SerfSpawn;

assert(typeof abbreviateModel === "function", "abbreviateModel is exported");

// Provider prefix stripping
assert(
  abbreviateModel("anthropic/claude-haiku-4-5-20251001") === "claude-haiku-4-5",
  "anthropic prefix + date suffix stripped: " + abbreviateModel("anthropic/claude-haiku-4-5-20251001")
);
assert(
  abbreviateModel("anthropic/claude-opus-4-7") === "claude-opus-4-7",
  "anthropic prefix stripped, no date suffix: " + abbreviateModel("anthropic/claude-opus-4-7")
);
assert(
  abbreviateModel("openai/gpt-5.5") === "gpt-5.5",
  "openai prefix stripped: " + abbreviateModel("openai/gpt-5.5")
);
assert(
  abbreviateModel("openai/gpt-5-20250501") === "gpt-5",
  "openai prefix + date suffix stripped: " + abbreviateModel("openai/gpt-5-20250501")
);
assert(
  abbreviateModel("google/gemini-2.5-flash-20250417") === "gemini-2.5-flash",
  "google prefix + date suffix stripped: " + abbreviateModel("google/gemini-2.5-flash-20250417")
);
assert(
  abbreviateModel("google/gemini-2.5-flash") === "gemini-2.5-flash",
  "google prefix stripped, no date suffix: " + abbreviateModel("google/gemini-2.5-flash")
);

// openrouter: strip the openrouter/ prefix, keep the rest (may include sub-provider)
assert(
  abbreviateModel("openrouter/anthropic/claude-opus-4") === "anthropic/claude-opus-4",
  "openrouter prefix stripped, sub-provider kept: " + abbreviateModel("openrouter/anthropic/claude-opus-4")
);

// Date suffix only (no provider prefix) — should strip the date suffix
assert(
  abbreviateModel("claude-haiku-4-5-20251001") === "claude-haiku-4-5",
  "date suffix stripped when no provider prefix: " + abbreviateModel("claude-haiku-4-5-20251001")
);

// Already short — no change
assert(
  abbreviateModel("claude-opus-4-7") === "claude-opus-4-7",
  "no-op on already short name: " + abbreviateModel("claude-opus-4-7")
);
assert(
  abbreviateModel("gpt-5") === "gpt-5",
  "no-op on bare model name: " + abbreviateModel("gpt-5")
);

// Passthrough for empty/null
assert(
  abbreviateModel("") === "",
  "empty string passthrough"
);
assert(
  abbreviateModel(null) === null,
  "null passthrough"
);

// Date suffix of exactly 8 digits only
assert(
  abbreviateModel("anthropic/claude-3-5-sonnet-20241022") === "claude-3-5-sonnet",
  "8-digit date suffix stripped: " + abbreviateModel("anthropic/claude-3-5-sonnet-20241022")
);

// Does not strip 7-digit or 9-digit pseudo-dates
assert(
  abbreviateModel("some-model-2025001") === "some-model-2025001",
  "7-digit pseudo-date not stripped: " + abbreviateModel("some-model-2025001")
);

console.log("PASS — abbreviateModel strips provider prefix and date suffix correctly");
