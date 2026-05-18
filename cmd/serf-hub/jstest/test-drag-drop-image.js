// Test harness for kata 65mm: drag-drop + file-picker image attachment in
// the web composer. Extends the composer-attachments.js module with two
// additional helpers that funnel image gestures into the SAME pendingState
// shape the paste handler uses (kata r6a1).
//
//   window.SerfComposerAttachments.attachComposerDropHandlers(dropZoneEl, pendingState)
//   window.SerfComposerAttachments.attachComposerFilePickerHandlers(buttonEl, fileInputEl, pendingState)
//
// Both call renderAttachmentChips after attaching. Non-image files are
// rejected and surface ONE inline banner under the composer at the element
// matching [data-attachment-error] inside the drop zone (or sibling — the
// helper locates it from the dropZone / fileInput's enclosing form-ish
// ancestor).
//
// The visual drop-active class is added on dragenter, removed on dragleave
// / drop.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const MODULE_PATH = path.resolve(__dirname, "../assets/composer-attachments.js");
const moduleSrc = fs.readFileSync(MODULE_PATH, "utf8");

// Minimal 1x1 transparent PNG. Same fixture used by test-paste-image.js so
// the canvas stub round-trips deterministic bytes.
const PNG_BASE64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+P+/HgAFhAJ/wlseKgAAAABJRU5ErkJggg==";
const PNG_BYTES = Buffer.from(PNG_BASE64, "base64");

function buildDom() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <form id="form">
      <div id="errors" data-attachment-error hidden></div>
      <div id="attachments"></div>
      <div id="drop" data-drop-zone>
        <textarea id="ta"></textarea>
      </div>
      <button id="attach-btn" type="button">attach</button>
      <input id="picker" type="file" accept="image/*" multiple hidden>
    </form>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
  const { window } = dom;
  // Canvas stubs mirroring test-paste-image.js so reencodeToPng works.
  window.HTMLCanvasElement.prototype.toBlob = function (cb, mimeType) {
    cb(new window.Blob([new Uint8Array(PNG_BYTES)], { type: mimeType || "image/png" }));
  };
  window.HTMLCanvasElement.prototype.getContext = function () {
    return { drawImage() {} };
  };
  class FakeImage {
    constructor() {
      this._src = "";
      this.width = 8;
      this.height = 4;
      this.onload = null;
      this.onerror = null;
    }
    set src(v) {
      this._src = v;
      Promise.resolve().then(() => { if (this.onload) this.onload(); });
    }
    get src() { return this._src; }
  }
  window.Image = FakeImage;
  window.URL.createObjectURL = () => "blob:fake";
  window.URL.revokeObjectURL = () => {};
  window.eval(moduleSrc);
  return window;
}

function makeFile(window, bytes, name, type) {
  return new window.File([new Uint8Array(bytes)], name, { type });
}

// buildDropEvent constructs a JSDOM-compatible 'drop' event with a
// DataTransfer-ish stub carrying File objects. JSDOM doesn't ship a real
// DataTransfer constructor on drag events.
function buildDropEvent(window, kind, files) {
  const ev = new window.Event(kind, { bubbles: true, cancelable: true });
  Object.defineProperty(ev, "dataTransfer", {
    value: {
      files,
      items: files.map((f) => ({ kind: "file", type: f.type, getAsFile: () => f })),
      types: ["Files"],
    },
  });
  return ev;
}

function buildChangeEvent(window, files) {
  const ev = new window.Event("change", { bubbles: true });
  return ev;
}

async function waitMicrotasks(n = 20) {
  for (let i = 0; i < n; i++) await new Promise((r) => setTimeout(r, 1));
}

const failures = [];
function pass(cond, msg) { if (!cond) failures.push("FAIL: " + msg); }

(async () => {
  // ---------- Assertion 1: API surface present ----------
  {
    const w = buildDom();
    pass(typeof w.SerfComposerAttachments.attachComposerDropHandlers === "function",
      "attachComposerDropHandlers should be exported");
    pass(typeof w.SerfComposerAttachments.attachComposerFilePickerHandlers === "function",
      "attachComposerFilePickerHandlers should be exported");
  }

  // ---------- Assertion 2: drop with single PNG → 1 entry ----------
  {
    const w = buildDom();
    const dropZone = w.document.getElementById("drop");
    const container = w.document.getElementById("attachments");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerDropHandlers(dropZone, pending);
    w.SerfComposerAttachments.renderAttachmentChips(container, pending);

    const file = makeFile(w, PNG_BYTES, "drop.png", "image/png");
    const ev = buildDropEvent(w, "drop", [file]);
    dropZone.dispatchEvent(ev);
    await waitMicrotasks();

    pass(pending.items.length === 1, "expected 1 queued item after PNG drop, got " + pending.items.length);
    const item = pending.items[0];
    pass(item && item.type === "image", "expected type=image, got " + (item && item.type));
    pass(item && item.mediaType === "image/png", "expected mediaType=image/png, got " + (item && item.mediaType));
    pass(item && item.data instanceof w.ArrayBuffer,
      "expected ArrayBuffer data, got " + (item && Object.prototype.toString.call(item.data)));
    pass(container.querySelectorAll("[data-attachment]").length === 1,
      "expected chip rendered after drop, got " + container.querySelectorAll("[data-attachment]").length);
  }

  // ---------- Assertion 3: drop with 2 PNGs → 2 entries ----------
  {
    const w = buildDom();
    const dropZone = w.document.getElementById("drop");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerDropHandlers(dropZone, pending);

    const files = [
      makeFile(w, PNG_BYTES, "one.png", "image/png"),
      makeFile(w, PNG_BYTES, "two.png", "image/png"),
    ];
    const ev = buildDropEvent(w, "drop", files);
    dropZone.dispatchEvent(ev);
    await waitMicrotasks();

    pass(pending.items.length === 2, "expected 2 queued items, got " + pending.items.length);
  }

  // ---------- Assertion 4: drop PNG + .txt → 1 entry + banner ----------
  {
    const w = buildDom();
    const dropZone = w.document.getElementById("drop");
    const errBox = w.document.getElementById("errors");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerDropHandlers(dropZone, pending);

    const files = [
      makeFile(w, PNG_BYTES, "ok.png", "image/png"),
      makeFile(w, Buffer.from("hello"), "notes.txt", "text/plain"),
    ];
    const ev = buildDropEvent(w, "drop", files);
    dropZone.dispatchEvent(ev);
    await waitMicrotasks();

    pass(pending.items.length === 1, "expected 1 image queued (txt rejected), got " + pending.items.length);
    pass(!errBox.hidden, "expected error banner visible after rejection");
    pass(errBox.textContent && errBox.textContent.length > 0, "expected error banner text content");
  }

  // ---------- Assertion 5: drop with only .txt → 0 entries + banner ----------
  {
    const w = buildDom();
    const dropZone = w.document.getElementById("drop");
    const errBox = w.document.getElementById("errors");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerDropHandlers(dropZone, pending);

    const files = [makeFile(w, Buffer.from("hello"), "notes.txt", "text/plain")];
    const ev = buildDropEvent(w, "drop", files);
    dropZone.dispatchEvent(ev);
    await waitMicrotasks();

    pass(pending.items.length === 0, "expected 0 queued items, got " + pending.items.length);
    pass(!errBox.hidden, "expected error banner visible");
  }

  // ---------- Assertion 6: dragenter adds .drop-active; dragleave removes ----------
  {
    const w = buildDom();
    const dropZone = w.document.getElementById("drop");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerDropHandlers(dropZone, pending);

    const enter = buildDropEvent(w, "dragenter", []);
    dropZone.dispatchEvent(enter);
    pass(dropZone.classList.contains("drop-active"),
      "expected .drop-active after dragenter, classes=" + dropZone.className);

    const leave = buildDropEvent(w, "dragleave", []);
    dropZone.dispatchEvent(leave);
    pass(!dropZone.classList.contains("drop-active"),
      "expected .drop-active removed after dragleave, classes=" + dropZone.className);
  }

  // ---------- Assertion 7: drop also removes .drop-active ----------
  {
    const w = buildDom();
    const dropZone = w.document.getElementById("drop");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerDropHandlers(dropZone, pending);

    dropZone.dispatchEvent(buildDropEvent(w, "dragenter", []));
    pass(dropZone.classList.contains("drop-active"), "precondition: drop-active set");

    const file = makeFile(w, PNG_BYTES, "x.png", "image/png");
    dropZone.dispatchEvent(buildDropEvent(w, "drop", [file]));
    await waitMicrotasks();
    pass(!dropZone.classList.contains("drop-active"),
      "expected .drop-active removed after drop, classes=" + dropZone.className);
  }

  // ---------- Assertion 8: drop calls preventDefault so browser doesn't navigate ----------
  {
    const w = buildDom();
    const dropZone = w.document.getElementById("drop");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerDropHandlers(dropZone, pending);

    const file = makeFile(w, PNG_BYTES, "x.png", "image/png");
    const ev = buildDropEvent(w, "drop", [file]);
    dropZone.dispatchEvent(ev);
    await waitMicrotasks();
    pass(ev.defaultPrevented, "expected drop event to be preventDefault'd");
  }

  // ---------- Assertion 9: file picker change with a PNG → 1 entry ----------
  {
    const w = buildDom();
    const picker = w.document.getElementById("picker");
    const btn = w.document.getElementById("attach-btn");
    const container = w.document.getElementById("attachments");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerFilePickerHandlers(btn, picker, pending);
    w.SerfComposerAttachments.renderAttachmentChips(container, pending);

    // Simulate the user selecting one file. JSDOM doesn't let us assign
    // .files directly, so override the property.
    const file = makeFile(w, PNG_BYTES, "pick.png", "image/png");
    Object.defineProperty(picker, "files", { value: [file], configurable: true });
    picker.dispatchEvent(new w.Event("change", { bubbles: true }));
    await waitMicrotasks();

    pass(pending.items.length === 1, "expected 1 picked item, got " + pending.items.length);
    pass(container.querySelectorAll("[data-attachment]").length === 1,
      "expected chip rendered after pick, got " + container.querySelectorAll("[data-attachment]").length);
  }

  // ---------- Assertion 10: file picker accept attribute is image/* ----------
  {
    const w = buildDom();
    const picker = w.document.getElementById("picker");
    pass(picker.getAttribute("accept") === "image/*",
      "expected accept=image/*, got " + picker.getAttribute("accept"));
  }

  // ---------- Assertion 11: file picker change with 2 PNGs → 2 entries ----------
  {
    const w = buildDom();
    const picker = w.document.getElementById("picker");
    const btn = w.document.getElementById("attach-btn");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerFilePickerHandlers(btn, picker, pending);

    const files = [
      makeFile(w, PNG_BYTES, "a.png", "image/png"),
      makeFile(w, PNG_BYTES, "b.png", "image/png"),
    ];
    Object.defineProperty(picker, "files", { value: files, configurable: true });
    picker.dispatchEvent(new w.Event("change", { bubbles: true }));
    await waitMicrotasks();

    pass(pending.items.length === 2, "expected 2 picked items, got " + pending.items.length);
  }

  // ---------- Assertion 12: picker rejects .txt with banner ----------
  {
    const w = buildDom();
    const picker = w.document.getElementById("picker");
    const btn = w.document.getElementById("attach-btn");
    const errBox = w.document.getElementById("errors");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerFilePickerHandlers(btn, picker, pending);

    const files = [makeFile(w, Buffer.from("hi"), "notes.txt", "text/plain")];
    Object.defineProperty(picker, "files", { value: files, configurable: true });
    picker.dispatchEvent(new w.Event("change", { bubbles: true }));
    await waitMicrotasks();

    pass(pending.items.length === 0, "expected 0 items (txt rejected), got " + pending.items.length);
    pass(!errBox.hidden, "expected banner visible after picker rejection");
  }

  // ---------- Assertion 13: button click triggers hidden file input ----------
  {
    const w = buildDom();
    const picker = w.document.getElementById("picker");
    const btn = w.document.getElementById("attach-btn");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerFilePickerHandlers(btn, picker, pending);

    let clicked = false;
    picker.click = () => { clicked = true; };
    btn.click();
    pass(clicked, "expected button click to trigger picker.click()");
  }

  // ---------- Assertion 14: button is keyboard-reachable (tagName button OR tabindex) ----------
  {
    const w = buildDom();
    const btn = w.document.getElementById("attach-btn");
    const ok = btn.tagName === "BUTTON" || btn.hasAttribute("tabindex");
    pass(ok, "expected button to be keyboard-reachable (tagName=BUTTON or tabindex), got tag=" + btn.tagName);
  }

  // ---------- Assertion 15: only ONE banner surfaced even when multiple non-images rejected ----------
  {
    const w = buildDom();
    const dropZone = w.document.getElementById("drop");
    const errBox = w.document.getElementById("errors");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerDropHandlers(dropZone, pending);

    const files = [
      makeFile(w, Buffer.from("a"), "a.txt", "text/plain"),
      makeFile(w, Buffer.from("b"), "b.pdf", "application/pdf"),
    ];
    dropZone.dispatchEvent(buildDropEvent(w, "drop", files));
    await waitMicrotasks();

    pass(pending.items.length === 0, "expected 0 items, got " + pending.items.length);
    // There should be at most one [data-attachment-error] banner element
    // (we don't append additional banner nodes — the single element's text
    // content updates).
    const banners = w.document.querySelectorAll("[data-attachment-error]");
    pass(banners.length === 1, "expected exactly 1 banner element, got " + banners.length);
    pass(!banners[0].hidden, "expected banner to be visible");
  }

  if (failures.length === 0) {
    console.log("PASS: all assertions");
    process.exit(0);
  } else {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
})();
