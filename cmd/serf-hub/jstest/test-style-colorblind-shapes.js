"use strict";
const fs = require("fs");
const path = require("path");
const assert = require("assert");

const css = fs.readFileSync(path.join(__dirname, "..", "assets", "style.css"), "utf8");

// warning must no longer share the diamond transform with awaiting.
// style.css has two rule blocks per selector: an earlier color-only one
// (background: var(--state-*)) and the shape one lower in the file
// (border-radius/transform). Take the last match so we inspect the shape
// rule rather than the color-only rule that happens to differ trivially.
const awaitingMatches = [...css.matchAll(/\.status-dot\[data-state="awaiting"\]\s*\{([^}]*)\}/g)];
const warningMatches = [...css.matchAll(/\.status-dot\[data-state="warning"\]\s*\{([^}]*)\}/g)];
assert.ok(awaitingMatches.length && warningMatches.length, "expected both awaiting and warning status-dot shape rules");
// Strip comments before comparing so the assertion reflects the actual
// declarations (border-radius/transform), not incidental comment text.
const stripComments = (s) => s.replace(/\/\*.*?\*\//g, "").trim();
const awaitingShape = stripComments(awaitingMatches[awaitingMatches.length - 1][1]);
const warningShape = stripComments(warningMatches[warningMatches.length - 1][1]);
assert.notStrictEqual(awaitingShape, warningShape, "warning must have its own distinct shape now that it is no longer amber-paired with awaiting");

console.log("test-style-colorblind-shapes.js: OK");
