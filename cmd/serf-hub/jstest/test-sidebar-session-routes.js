"use strict";
const assert = require("assert");
const fs = require("fs");
const { JSDOM } = require("jsdom");

const sidebarSrc = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");
const iconsSrc = fs.readFileSync(__dirname + "/../assets/icons.js", "utf8");

function emptyTree() {
  return {
    needs_you: [], favorites: [], projects: [], archived_projects: [], test_runs: [],
    attentionSummary: { needsYou: 0, error: 0, working: 0 },
  };
}

function boot(pathname) {
  const dom = new JSDOM(
    '<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>',
    { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost" + pathname },
  );
  const w = dom.window;
  w.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(emptyTree()) });
  w.htmx = { process() {} };
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.eval(iconsSrc);
  w.eval(sidebarSrc);
  return w;
}

function node(ref, sessionID, rowID) {
  return {
    row_id: rowID,
    ref,
    session_id: sessionID,
    title: ref,
    state: "idle",
    kind: "session",
    tier: "current",
    updated_at: "2026-07-13T00:00:00Z",
  };
}

const w = boot("/s/codex-local:th_codex");
const I = w.SerfSidebarInternal;
const local = node("local:01LOCAL", "01LOCAL", "project:p:local:01LOCAL");
const codex = node("codex-local:th_codex", "codex-session", "project:p:codex-local:th_codex");
const malformed = node("not a ref", "fallback-id", "project:p:local:fallback-id");

assert.strictEqual(I.sessionRouteID(local), "01LOCAL");
assert.strictEqual(I.sessionRouteID(codex), "codex-local:th_codex");
assert.strictEqual(I.sessionRouteID(malformed), "fallback-id");
assert.strictEqual(I.sessionHref(local), "/s/01LOCAL");
assert.strictEqual(I.sessionHref(codex), "/s/codex-local:th_codex");

const localRow = I.buildRow(local);
assert.strictEqual(localRow.getAttribute("href"), "/s/01LOCAL");
assert.strictEqual(localRow.getAttribute("hx-get"), "/_partials/s/01LOCAL/workspace");
assert.strictEqual(localRow.getAttribute("hx-push-url"), "/s/01LOCAL");

const codexRow = I.buildRow(codex);
assert.strictEqual(codexRow.getAttribute("href"), "/s/codex-local:th_codex");
assert.strictEqual(codexRow.getAttribute("hx-get"), "/_partials/s/codex-local:th_codex/workspace");
assert.strictEqual(codexRow.getAttribute("hx-push-url"), "/s/codex-local:th_codex");

const openItem = I.sessionMenuItems(codex).find((item) => item.label === "Open");
assert.ok(openItem, "Codex row menu must contain Open");
assert.strictEqual(openItem.href, "/s/codex-local:th_codex");

const tree = {
  needs_you: [], favorites: [], archived_projects: [], test_runs: [],
  projects: [{ key: "p", name: "p", working_dir: "/work/p", default_expanded: true, sessions: [codex] }],
  attentionSummary: { needsYou: 0, error: 0, working: 0 },
};
w.SerfSidebar.renderTree(tree);
const rendered = w.document.querySelector('[data-row-id="project:p:codex-local:th_codex"]');
assert.ok(rendered, "Codex row must render");
assert.ok(rendered.hasAttribute("data-active"), "qualified Codex URL must mark its row active");
assert.deepStrictEqual(Array.from(I.findRevealChain(tree, "codex-local:th_codex")), ["p"]);
assert.strictEqual(I.findRevealChain(tree, "th_codex"), null, "bare external thread ID must not match");

console.log("PASS: sidebar session routes preserve source identity");
process.exit(0);
