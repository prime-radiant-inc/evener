// Test harness: verify tool output image descriptors render inline under tool rows.
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation"
       data-session-id="01TEST"
       data-state="ended"></div>
  <form data-input-form data-session-id="01TEST">
    <textarea class="message-input"></textarea>
  </form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({
  ok: true,
  json: () => Promise.resolve([]),
  text: () => Promise.resolve(""),
});
window.HTMLElement.prototype.contains = window.HTMLElement.prototype.contains || function () { return false; };

require("./load-renderer").evalRenderer(window);

const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);

const events = [
  ["SESSION_START", { model: "test", profile: "test", restored: true, session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "call_img", tool_name: "shell", arguments_json: "{}" }],
  ["TOOL_CALL_END", { call_id: "call_img", tool_name: "shell", output: "created out.png", output_images: [
    { source: "shell-path", name: "out.png", mediaType: "image/png", url: "/doc/image?session=01TEST&path=out.png", path: "out.png" },
    { source: "shell-path", name: "external.png", mediaType: "image/png", url: "//external.example/image.png", path: "external.png" },
  ]}],
  ["TOOL_CALL_START", { call_id: "call_external", tool_name: "shell", arguments_json: "{}" }],
  ["TOOL_CALL_END", { call_id: "call_external", tool_name: "shell", output: "created external.png", output_images: [
    { source: "shell-path", name: "external-only.png", mediaType: "image/png", url: "//external.example/image.png", path: "external-only.png" },
  ]}],
];

async function flushAndAssert() {
  await new Promise(r => setTimeout(r, 30));
  for (const [name, data] of events) {
    window.SerfRenderer.handleData(name, data);
  }
  await new Promise(r => setTimeout(r, 10));
  runAssertions();
}

function runAssertions() {
  const failures = [];
  const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

  const tools = conv.querySelectorAll(".tool-call.shell");
  pass(tools.length === 2, "shell tool rows rendered");
  const tool = tools[0];
  pass(!!tool, "shell tool row rendered");
  const wrap = tool && tool.querySelector(".tool-output-images");
  pass(!!wrap, "tool output image wrapper rendered");
  const thumbs = wrap ? Array.from(wrap.querySelectorAll("img.user-image-thumb")) : [];
  pass(thumbs.length === 1, "invalid mixed descriptor should be omitted");
  const img = thumbs[0];
  pass(img && img.getAttribute("src") === "/doc/image?session=01TEST&path=out.png", "tool image src wrong");
  pass(!thumbs.some((thumb) => (thumb.getAttribute("src") || "").startsWith("//")), "protocol-relative tool image omitted");

  const externalTool = tools[1];
  pass(!!externalTool, "external-only shell tool row rendered");
  const externalWrap = externalTool && externalTool.querySelector(".tool-output-images");
  pass(!externalWrap, "external-only protocol-relative descriptor omitted");

  const card = wrap && wrap.querySelector(".user-image-card");
  if (card) {
    card.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
  }
  pass(!!window.document.querySelector(".image-lightbox"), "clicking tool image should open lightbox");

  if (failures.length === 0) {
    console.log("PASS: tool output images render inline");
    process.exit(0);
  } else {
    console.log("Rendered conversation HTML:");
    console.log(conv.innerHTML);
    console.log("");
    for (const f of failures) console.log(f);
    process.exit(1);
  }
}

flushAndAssert();
