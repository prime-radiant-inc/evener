"use strict";
const fs = require("fs");
const path = require("path");
const assert = require("assert");

const css = fs.readFileSync(path.join(__dirname, "..", "assets", "style.css"), "utf8");

// The processing→working rename must leave zero references to the old name.
assert.ok(!/--state-processing/.test(css), "style.css must not reference --state-processing after the rename");
assert.ok(/--state-working:\s*var\(--accent\)/.test(css), "style.css must define --state-working (blue = live)");

// Four theme blocks must all define --state-working and --state-awaiting.
// Post-recolor (Task 8) both are var() refs into the Task 7 canonicals:
// working = blue --accent, awaiting = amber --attention.
const working = css.match(/--state-working:\s*var\(--accent\)/g) || [];
const awaiting = css.match(/--state-awaiting:\s*var\(--attention\)/g) || [];
assert.strictEqual(working.length, 4, "expected 4 --state-working definitions (default/light-media/dark-forced/light-forced), got " + working.length);
assert.strictEqual(awaiting.length, 4, "expected 4 --state-awaiting definitions, got " + awaiting.length);

// The retired --state-warning name is gone; diagnostics own amber now, and
// --diagnostic-hub holds the freed green as a per-theme hex (4 blocks).
assert.ok(!/--state-warning/.test(css), "style.css must not reference --state-warning after the diagnostics-lane rename");
const diagHub = css.match(/--diagnostic-hub:\s*(#[0-9a-fA-F]{6})/g) || [];
assert.strictEqual(diagHub.length, 4, "expected 4 hex --diagnostic-hub definitions, got " + diagHub.length);

console.log("test-style-palette.js: OK");
