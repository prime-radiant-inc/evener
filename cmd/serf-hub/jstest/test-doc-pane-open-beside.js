// Document panes — "open beside" affordances on transcript cards.
//
//  1. An IMAGE card whose src is a sha-addressed /s/<id>/images/<sha> URL
//     gains an ⇲ button that calls SerfPanes.open(<that URL>, filename).
//     A data: image (no stable URL, CSP-blocked in an iframe) gets no button.
//  2. A file-referencing TOOL card (read_file / edit_file / write_file) gains
//     an ⇲ button that calls SerfPanes.open("/doc/file?session=..&path=..",
//     filename) with the path relativized against the session cwd.
//  Guard: when window.SerfPanes is absent, no button is added (iframe guard).
const { JSDOM } = require("jsdom");

function newHarness(opts) {
  opts = opts || {};
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <header class="workspace-header" data-session-id="01TEST"></header>
    <div class="conversation" id="conversation" data-session-id="01TEST" data-cwd="/work/repo" data-home="/home/u" data-state="active"></div>
    <form data-input-form data-session-id="01TEST">
      <textarea class="message-input"></textarea>
    </form>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
  const { window } = dom;
  window.marked = { parse: (t) => String(t || "") };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
  window.HTMLElement.prototype.contains = window.HTMLElement.prototype.contains || function () { return false; };
  if (opts.withPanes !== false) {
    window.SerfPanes = { open: opts.panesOpen || (() => {}) };
  }
  require("./load-renderer").evalRenderer(window);
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);
  return { window, conv };
}

let allPass = true;
async function scenario(name, run) {
  const result = await run();
  console.log((result.ok ? "PASS" : "FAIL") + " — " + name);
  if (!result.ok) { allPass = false; console.log("  detail: " + result.detail); }
}

(async () => {

await scenario("image card with a sha URL gains an ⇲ that opens that URL beside", async () => {
  let openedWith = null;
  const { window } = newHarness({ panesOpen: (href, title) => { openedWith = { href, title }; } });
  await new Promise(r => setTimeout(r, 30));
  // A transcript image is referenced by sha → imageSrc builds a /images/<sha> URL.
  const sha = "a".repeat(64);
  const wrap = window.SerfRenderer.appendUserMessage("look", 1, [{ sha, name: "diagram.png" }]);
  const card = wrap.querySelector(".user-image-card");
  if (!card) return { ok: false, detail: "no single image card rendered" };
  // The ⇲ is a SIBLING of the card (the card is a <button>) inside the wrap.
  const frame = wrap.querySelector(".image-beside-wrap");
  if (!frame) return { ok: false, detail: "no .image-beside-wrap around the image card" };
  const btn = frame.querySelector(".open-beside-btn");
  if (!btn) return { ok: false, detail: "no .open-beside-btn beside the image card" };
  if (card.querySelector(".open-beside-btn")) return { ok: false, detail: "⇲ must not nest inside the <button> card" };
  btn.click();
  if (!openedWith) return { ok: false, detail: "SerfPanes.open not called" };
  const wantHref = "/s/01TEST/images/" + sha;
  if (openedWith.href !== wantHref) return { ok: false, detail: "wrong href: " + openedWith.href };
  if (openedWith.title !== "diagram.png") return { ok: false, detail: "wrong title: " + openedWith.title };
  return { ok: true };
});

await scenario("clicking the image ⇲ does not open the lightbox", async () => {
  const { window } = newHarness();
  await new Promise(r => setTimeout(r, 30));
  const sha = "b".repeat(64);
  const wrap = window.SerfRenderer.appendUserMessage("x", 1, [{ sha, name: "p.png" }]);
  const btn = wrap.querySelector(".image-beside-wrap .open-beside-btn");
  if (!btn) return { ok: false, detail: "no ⇲ button" };
  btn.click();
  if (window.document.getElementById("image-lightbox")) {
    return { ok: false, detail: "⇲ must not open the lightbox" };
  }
  return { ok: true };
});

await scenario("data: image (no stable URL) gets NO ⇲ button", async () => {
  const { window } = newHarness();
  await new Promise(r => setTimeout(r, 30));
  const wrap = window.SerfRenderer.appendUserMessage("y", 1, [{ data: "iVBORw0KGgo=", media_type: "image/png", name: "live.png" }]);
  const card = wrap.querySelector(".user-image-card");
  if (!card) return { ok: false, detail: "no card rendered" };
  if (card.querySelector(".open-beside-btn")) {
    return { ok: false, detail: "data: image must not get an ⇲ (no stable same-origin URL)" };
  }
  return { ok: true };
});

await scenario("image ⇲ absent when SerfPanes unavailable", async () => {
  const { window } = newHarness({ withPanes: false });
  await new Promise(r => setTimeout(r, 30));
  const sha = "c".repeat(64);
  const wrap = window.SerfRenderer.appendUserMessage("z", 1, [{ sha, name: "p.png" }]);
  const card = wrap.querySelector(".user-image-card");
  if (card && card.querySelector(".open-beside-btn")) {
    return { ok: false, detail: "no ⇲ should appear without SerfPanes" };
  }
  return { ok: true };
});

await scenario("read_file tool card gains an ⇲ that opens /doc/file with relativized path", async () => {
  let openedWith = null;
  const { window } = newHarness({ panesOpen: (href, title) => { openedWith = { href, title }; } });
  await new Promise(r => setTimeout(r, 30));
  window.SerfRenderer.handleData("TOOL_CALL_START", { call_id: "r1", tool_name: "read_file", arguments_json: JSON.stringify({ file_path: "/work/repo/src/main.go" }) });
  window.SerfRenderer.handleData("TOOL_CALL_END", { call_id: "r1", tool_name: "read_file", output: "package main" });
  await new Promise(r => setTimeout(r, 10));
  const btn = window.document.querySelector(".tool-call.read_file .open-beside-btn");
  if (!btn) return { ok: false, detail: "no ⇲ on read_file tool card" };
  btn.click();
  if (!openedWith) return { ok: false, detail: "SerfPanes.open not called" };
  // session=01TEST and the path relativized against cwd /work/repo.
  if (openedWith.href.indexOf("/doc/file?") !== 0) return { ok: false, detail: "wrong route: " + openedWith.href };
  if (openedWith.href.indexOf("session=01TEST") < 0) return { ok: false, detail: "missing session: " + openedWith.href };
  if (openedWith.href.indexOf("path=src%2Fmain.go") < 0) return { ok: false, detail: "path not relativized/encoded: " + openedWith.href };
  if (openedWith.title !== "main.go") return { ok: false, detail: "wrong title: " + openedWith.title };
  return { ok: true };
});

await scenario("edit_file tool card gains an ⇲ for /doc/file", async () => {
  let openedWith = null;
  const { window } = newHarness({ panesOpen: (href, title) => { openedWith = { href, title }; } });
  await new Promise(r => setTimeout(r, 30));
  window.SerfRenderer.handleData("TOOL_CALL_START", { call_id: "e1", tool_name: "edit_file", arguments_json: JSON.stringify({ file_path: "/work/repo/a/b.txt", old_string: "x", new_string: "y" }) });
  window.SerfRenderer.handleData("TOOL_CALL_END", { call_id: "e1", tool_name: "edit_file", output: "" });
  await new Promise(r => setTimeout(r, 10));
  const btn = window.document.querySelector(".tool-call.edit_file .open-beside-btn");
  if (!btn) return { ok: false, detail: "no ⇲ on edit_file tool card" };
  btn.click();
  if (!openedWith || openedWith.href.indexOf("path=a%2Fb.txt") < 0) {
    return { ok: false, detail: "wrong href: " + (openedWith && openedWith.href) };
  }
  return { ok: true };
});

await scenario("non-file tool (grep) gets NO ⇲ button", async () => {
  const { window } = newHarness();
  await new Promise(r => setTimeout(r, 30));
  window.SerfRenderer.handleData("TOOL_CALL_START", { call_id: "g1", tool_name: "grep_files", arguments_json: JSON.stringify({ pattern: "foo", path: "/work/repo" }) });
  window.SerfRenderer.handleData("TOOL_CALL_END", { call_id: "g1", tool_name: "grep_files", output: "x\n" });
  await new Promise(r => setTimeout(r, 10));
  const btn = window.document.querySelector(".tool-call.grep_files .open-beside-btn");
  if (btn) return { ok: false, detail: "grep should not get a file ⇲ (it targets a directory/pattern)" };
  return { ok: true };
});

await scenario("file ⇲ absent when SerfPanes unavailable", async () => {
  const { window } = newHarness({ withPanes: false });
  await new Promise(r => setTimeout(r, 30));
  window.SerfRenderer.handleData("TOOL_CALL_START", { call_id: "r2", tool_name: "read_file", arguments_json: JSON.stringify({ file_path: "/work/repo/x.go" }) });
  window.SerfRenderer.handleData("TOOL_CALL_END", { call_id: "r2", tool_name: "read_file", output: "x" });
  await new Promise(r => setTimeout(r, 10));
  if (window.document.querySelector(".tool-call.read_file .open-beside-btn")) {
    return { ok: false, detail: "no file ⇲ should appear without SerfPanes" };
  }
  return { ok: true };
});

if (!allPass) { console.error("FAIL: doc-pane open-beside tests failed"); process.exit(1); }
console.log("OK\ttest-doc-pane-open-beside.js");
process.exit(0);

})();
