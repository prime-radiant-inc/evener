"use strict";
const fs = require("fs");
const path = require("path");
const assert = require("assert");

const css = fs.readFileSync(path.join(__dirname, "..", "assets", "style.css"), "utf8");

// The processing→working rename must leave zero references to the old name.
assert.ok(!/--state-processing/.test(css), "style.css must not reference --state-processing after the rename");
assert.ok(/--state-working:\s*#/.test(css), "style.css must define --state-working");

// Four theme blocks must all define --state-working and --state-awaiting.
const working = css.match(/--state-working:\s*(#[0-9a-fA-F]{6})/g) || [];
const awaiting = css.match(/--state-awaiting:\s*(#[0-9a-fA-F]{6})/g) || [];
assert.strictEqual(working.length, 4, "expected 4 --state-working definitions (default/light-media/dark-forced/light-forced), got " + working.length);
assert.strictEqual(awaiting.length, 4, "expected 4 --state-awaiting definitions, got " + awaiting.length);

console.log("test-style-palette.js: OK");
