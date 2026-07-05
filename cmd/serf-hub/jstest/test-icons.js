"use strict";
const { JSDOM } = require("jsdom");
const fs = require("fs");
const path = require("path");
const assert = require("assert");

const dom = new JSDOM("<!doctype html><html><body></body></html>", { runScripts: "outside-only" });
const script = fs.readFileSync(path.join(__dirname, "..", "assets", "icons.js"), "utf8");
dom.window.eval(script);

const EXPECTED_KEYS = ["working", "questionWaiting", "yourMove", "warning", "error", "idle", "ended"];
const icons = dom.window.SerfIcons;
assert.ok(icons, "window.SerfIcons must be defined");
for (const key of EXPECTED_KEYS) {
  const markup = icons[key];
  assert.ok(typeof markup === "string" && markup.length > 0, `SerfIcons.${key} must be a non-empty string`);
  const div = dom.window.document.createElement("div");
  div.innerHTML = markup;
  const svg = div.querySelector("svg");
  assert.ok(svg, `SerfIcons.${key} must parse to an <svg> element`);
  assert.strictEqual(svg.getAttribute("stroke"), "currentColor", `SerfIcons.${key} must use stroke="currentColor" to inherit color`);
}

console.log("test-icons.js: OK");
