"use strict";
const fs = require("fs");
const path = require("path");
const assert = require("assert");

const src = fs.readFileSync(path.join(__dirname, "..", "assets", "notifications.js"), "utf8");
const match = src.match(/const STATE_COLORS = \{([^}]*)\}/);
assert.ok(match, "STATE_COLORS block must exist");
assert.ok(/working:\s*"#7aa2f7"/.test(match[1]), "working must be blue, matching dark --accent (--state-working)");
assert.ok(/needs_you:\s*"#e0af68"/.test(match[1]), "needs_you must be amber, matching dark --attention (--state-awaiting)");
assert.ok(/error:\s*"#f7768e"/.test(match[1]), "error stays unchanged");

console.log("test-notifications-palette.js: OK");
