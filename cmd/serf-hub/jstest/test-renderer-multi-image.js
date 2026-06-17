// Multi-image layout (mockup #20 Alt B — contact-sheet grid).
// A user message with MULTIPLE images lays them out as a contact-sheet grid
// inside ONE neutral card, each cell captioned (filename · dims). A single
// image keeps today's single-card path. The shared lightbox steps across the
// whole set with ←/→ (Esc closes).
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <header class="workspace-header" data-session-id="01TEST">
    <span class="status-dot" data-state="active"></span>
  </header>
  <div class="conversation" id="conversation" data-session-id="01TEST" data-state="active"></div>
  <form data-input-form data-session-id="01TEST">
    <textarea class="message-input"></textarea>
  </form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
window.HTMLElement.prototype.contains = window.HTMLElement.prototype.contains || function () { return false; };

require("./load-renderer").evalRenderer(window);

const doc = window.document;
const conv = doc.getElementById("conversation");
window.SerfRenderer.init(conv);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const R = window.SerfRenderer;

function b64png(tag) {
  // A tiny distinct base64 stub per image; the bytes are never decoded in jsdom.
  return "iVBORw0KGgo=" + tag;
}

(async () => {
  await new Promise((r) => setTimeout(r, 30)); // let the cold-load fetch flush

  // ── 1. A 3-image message renders ONE card holding a 3-cell grid ──────────
  const threeImages = [
    { data: b64png("a"), media_type: "image/png", name: "hero-a.png" },
    { data: b64png("b"), media_type: "image/png", name: "hero-b.png" },
    { data: b64png("c"), media_type: "image/png", name: "hero-c.png" },
  ];
  const wrap = R.appendUserMessage("compare these", 1, threeImages);

  const sheets = wrap.querySelectorAll(".user-image-sheet");
  pass(sheets.length === 1, "multi-image renders exactly ONE neutral sheet card (got " + sheets.length + ")");
  // Not a card-of-cards: the per-image single-card class must be absent.
  pass(wrap.querySelectorAll(".user-image-card").length === 0,
    "multi-image does NOT use the single .user-image-card path");

  const cells = wrap.querySelectorAll(".user-image-grid .user-image-cell");
  pass(cells.length === 3, "the grid holds one cell per image (got " + cells.length + ")");

  // Each cell carries a thumbnail and a per-cell caption with the filename in mono.
  let captioned = 0;
  for (const cell of cells) {
    if (cell.querySelector("img.user-image-thumb") && cell.querySelector(".user-image-caption .user-image-filename")) {
      captioned++;
    }
  }
  pass(captioned === 3, "every cell has a thumbnail + a filename caption (got " + captioned + ")");
  const firstFn = cells[0] && cells[0].querySelector(".user-image-filename");
  pass(firstFn && firstFn.textContent === "hero-a.png",
    "the caption shows the real filename (got " + (firstFn && firstFn.textContent) + ")");

  // ── 2. A single-image message keeps the existing single-card path ────────
  const oneImage = [{ data: b64png("solo"), media_type: "image/png", name: "solo.png" }];
  const solo = R.appendUserMessage("just one", 2, oneImage);
  pass(solo.querySelectorAll(".user-image-card").length === 1,
    "single image keeps the single .user-image-card path");
  pass(solo.querySelectorAll(".user-image-sheet").length === 0,
    "single image does NOT build a contact-sheet grid");

  // ── 3. The shared lightbox steps across the whole set with prev/next ─────
  // Opening any cell opens one shared lightbox positioned at that cell, and
  // exposes prev/next nav across the message's images.
  cells[0].click();
  let lb = doc.getElementById("image-lightbox");
  pass(lb, "clicking a cell opens the shared lightbox");
  pass(doc.querySelectorAll("#image-lightbox").length === 1, "exactly one lightbox instance exists");
  const nav = lb && lb.querySelector(".image-lightbox-next");
  const prev = lb && lb.querySelector(".image-lightbox-prev");
  pass(nav && prev, "the lightbox exposes prev/next nav controls for the set");
  const posAt0 = lb && lb.querySelector(".image-lightbox-pos");
  pass(posAt0 && /1\s*\/\s*3/.test(posAt0.textContent),
    "the lightbox shows the position within the set (got " + (posAt0 && posAt0.textContent) + ")");

  // Next advances to image 2; the caption updates.
  nav.click();
  lb = doc.getElementById("image-lightbox");
  const posAt1 = lb.querySelector(".image-lightbox-pos");
  pass(posAt1 && /2\s*\/\s*3/.test(posAt1.textContent),
    "next advances within the set (got " + (posAt1 && posAt1.textContent) + ")");
  const cap1 = lb.querySelector(".image-lightbox-caption");
  pass(cap1 && /hero-b\.png/.test(cap1.textContent),
    "the lightbox caption follows navigation (got " + (cap1 && cap1.textContent) + ")");

  // ArrowRight wraps 3 → 1 (one image at a time).
  const right = new window.KeyboardEvent("keydown", { key: "ArrowRight" });
  doc.dispatchEvent(right); // → 3
  doc.dispatchEvent(right); // wrap → 1
  lb = doc.getElementById("image-lightbox");
  const posWrap = lb.querySelector(".image-lightbox-pos");
  pass(posWrap && /1\s*\/\s*3/.test(posWrap.textContent),
    "ArrowRight wraps around the set (got " + (posWrap && posWrap.textContent) + ")");

  // Esc closes the single shared instance.
  doc.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Escape" }));
  pass(!doc.getElementById("image-lightbox"), "Esc closes the lightbox");

  if (failures.length > 0) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: multi-image contact-sheet grid + set-navigating lightbox");
  process.exit(0); // renderer pollers keep the loop alive otherwise
})();
