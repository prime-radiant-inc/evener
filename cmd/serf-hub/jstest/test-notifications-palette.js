"use strict";
const fs = require("fs");
const path = require("path");
const assert = require("assert");

const src = fs.readFileSync(path.join(__dirname, "..", "assets", "notifications.js"), "utf8");
const match = src.match(/const STATE_COLORS = \{([^}]*)\}/);
assert.ok(match, "STATE_COLORS block must exist");
assert.ok(/working:\s*"#7dc98f"/.test(match[1]), "working must be the new green, matching --state-working");
assert.ok(/needs_you:\s*"#7aa2f7"/.test(match[1]), "needs_you must be the new blue, matching --state-awaiting");
assert.ok(/error:\s*"#f7768e"/.test(match[1]), "error stays unchanged");

console.log("test-notifications-palette.js: OK");
