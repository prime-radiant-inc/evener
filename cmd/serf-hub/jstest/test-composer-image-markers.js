// Test harness for kata 2stz: positional [image N] markers in the web
// composer. When a user attaches an image (paste / drop / file-picker),
// the helper assigns item.marker = N (max existing + 1, default 1) and
// inserts the literal text "[image N]" at the textarea cursor. On chip
// remove, the first occurrence of "[image N]" for the removed item is
// stripped from the textarea value. Numbering is monotonic — never reuse.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const MODULE_PATH = path.resolve(__dirname, "../assets/composer-attachments.js");
const moduleSrc = fs.readFileSync(MODULE_PATH, "utf8");

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

function buildPasteEvent(window, file) {
  const ev = new window.Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(ev, "clipboardData", {
    value: {
      items: [{
        kind: "file",
        type: file.type,
        getAsFile() { return file; },
        getAsString(cb) { cb && cb(""); },
      }],
      getData() { return ""; },
    },
  });
  return ev;
}

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

function setCursor(ta, start, end) {
  ta.selectionStart = start;
  ta.selectionEnd = (typeof end === "number") ? end : start;
}

async function waitMicrotasks(n = 30) {
  for (let i = 0; i < n; i++) await new Promise((r) => setTimeout(r, 1));
}

const failures = [];
function pass(cond, msg) { if (!cond) failures.push("FAIL: " + msg); }

(async () => {
  // ---------- Assertion 1: paste one image at cursor=5 inserts [image 1] ----------
  {
    const w = buildDom();
    const ta = w.document.getElementById("ta");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerImageHandlers(ta, pending);

    ta.value = "hello world";
    setCursor(ta, 5);
    const file = makeFile(w, PNG_BYTES, "shot.png", "image/png");
    ta.dispatchEvent(buildPasteEvent(w, file));
    await waitMicrotasks();

    pass(ta.value === "hello[image 1] world",
      "expected '[image 1]' inserted at cursor=5, got: " + JSON.stringify(ta.value));
    pass(ta.selectionStart === 5 + "[image 1]".length,
      "expected cursor after marker (=" + (5 + "[image 1]".length) + "), got " + ta.selectionStart);
    pass(pending.items.length === 1 && pending.items[0].marker === 1,
      "expected items[0].marker===1, got " + (pending.items[0] && pending.items[0].marker));
  }

  // ---------- Assertion 2: two sequential pastes get markers 1 and 2 ----------
  {
    const w = buildDom();
    const ta = w.document.getElementById("ta");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerImageHandlers(ta, pending);

    ta.value = "ab";
    setCursor(ta, 1);
    ta.dispatchEvent(buildPasteEvent(w, makeFile(w, PNG_BYTES, "a.png", "image/png")));
    await waitMicrotasks();

    // After first insert: "a[image 1]b", cursor=1+9=10. Move cursor to end.
    setCursor(ta, ta.value.length);
    ta.dispatchEvent(buildPasteEvent(w, makeFile(w, PNG_BYTES, "b.png", "image/png")));
    await waitMicrotasks();

    pass(pending.items.length === 2, "expected 2 items, got " + pending.items.length);
    pass(pending.items[0].marker === 1 && pending.items[1].marker === 2,
      "expected markers [1,2], got [" + pending.items.map(i => i.marker).join(",") + "]");
    pass(ta.value.indexOf("[image 1]") >= 0 && ta.value.indexOf("[image 2]") >= 0,
      "expected both markers in text, got: " + JSON.stringify(ta.value));
    pass(ta.value === "a[image 1]b[image 2]",
      "expected 'a[image 1]b[image 2]', got: " + JSON.stringify(ta.value));
  }

  // ---------- Assertion 3: drop one image inserts [image 1] at end ----------
  {
    const w = buildDom();
    const dropZone = w.document.getElementById("drop");
    const ta = w.document.getElementById("ta");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerImageHandlers(ta, pending);
    w.SerfComposerAttachments.attachComposerDropHandlers(dropZone, pending);

    ta.value = "look ";
    setCursor(ta, ta.value.length);
    const file = makeFile(w, PNG_BYTES, "drop.png", "image/png");
    dropZone.dispatchEvent(buildDropEvent(w, "drop", [file]));
    await waitMicrotasks();

    pass(pending.items.length === 1, "expected 1 dropped item, got " + pending.items.length);
    pass(pending.items[0].marker === 1, "expected marker=1, got " + pending.items[0].marker);
    pass(ta.value === "look [image 1]",
      "expected '[image 1]' appended at cursor, got: " + JSON.stringify(ta.value));
  }

  // ---------- Assertion 4: file picker change inserts [image 1] at cursor ----------
  {
    const w = buildDom();
    const picker = w.document.getElementById("picker");
    const btn = w.document.getElementById("attach-btn");
    const ta = w.document.getElementById("ta");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerImageHandlers(ta, pending);
    w.SerfComposerAttachments.attachComposerFilePickerHandlers(btn, picker, pending);

    ta.value = "before|after";
    setCursor(ta, 6);
    const file = makeFile(w, PNG_BYTES, "pick.png", "image/png");
    Object.defineProperty(picker, "files", { value: [file], configurable: true });
    picker.dispatchEvent(new w.Event("change", { bubbles: true }));
    await waitMicrotasks();

    pass(pending.items.length === 1, "expected 1 picked item, got " + pending.items.length);
    pass(pending.items[0].marker === 1, "expected marker=1, got " + pending.items[0].marker);
    pass(ta.value === "before[image 1]|after",
      "expected '[image 1]' inserted at cursor=6, got: " + JSON.stringify(ta.value));
  }

  // ---------- Assertion 5: remove first chip strips [image 1] only ----------
  {
    const w = buildDom();
    const ta = w.document.getElementById("ta");
    const container = w.document.getElementById("attachments");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerImageHandlers(ta, pending);
    w.SerfComposerAttachments.renderAttachmentChips(container, pending);

    setCursor(ta, 0);
    ta.dispatchEvent(buildPasteEvent(w, makeFile(w, PNG_BYTES, "a.png", "image/png")));
    await waitMicrotasks();
    setCursor(ta, ta.value.length);
    ta.dispatchEvent(buildPasteEvent(w, makeFile(w, PNG_BYTES, "b.png", "image/png")));
    await waitMicrotasks();

    pass(ta.value === "[image 1][image 2]",
      "precondition: textarea has both markers, got: " + JSON.stringify(ta.value));
    pass(pending.items.length === 2, "precondition: 2 items");

    // Click the first chip's remove button.
    const removeBtns = container.querySelectorAll("[data-attachment-remove]");
    pass(removeBtns.length === 2, "precondition: 2 remove buttons, got " + removeBtns.length);
    removeBtns[0].click();

    pass(pending.items.length === 1, "expected 1 item after remove, got " + pending.items.length);
    pass(pending.items[0].marker === 2, "expected surviving marker=2, got " + pending.items[0].marker);
    pass(ta.value === "[image 2]",
      "expected '[image 1]' stripped, '[image 2]' remains, got: " + JSON.stringify(ta.value));
  }

  // ---------- Assertion 6: numbering after gap — never reuse ----------
  {
    const w = buildDom();
    const ta = w.document.getElementById("ta");
    const container = w.document.getElementById("attachments");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerImageHandlers(ta, pending);
    w.SerfComposerAttachments.renderAttachmentChips(container, pending);

    setCursor(ta, 0);
    ta.dispatchEvent(buildPasteEvent(w, makeFile(w, PNG_BYTES, "a.png", "image/png")));
    await waitMicrotasks();
    setCursor(ta, ta.value.length);
    ta.dispatchEvent(buildPasteEvent(w, makeFile(w, PNG_BYTES, "b.png", "image/png")));
    await waitMicrotasks();

    // Remove the first chip → items=[{marker:2}], textarea="[image 2]"
    const removeBtns = container.querySelectorAll("[data-attachment-remove]");
    removeBtns[0].click();
    pass(pending.items.length === 1 && pending.items[0].marker === 2,
      "precondition: items=[{marker:2}], got " + JSON.stringify(pending.items.map(i => i.marker)));

    // Paste a new image — marker MUST be 3, not 1.
    setCursor(ta, ta.value.length);
    ta.dispatchEvent(buildPasteEvent(w, makeFile(w, PNG_BYTES, "c.png", "image/png")));
    await waitMicrotasks();

    pass(pending.items.length === 2, "expected 2 items, got " + pending.items.length);
    pass(pending.items[1].marker === 3,
      "expected new marker=3 (never reuse), got " + pending.items[1].marker);
    pass(ta.value.indexOf("[image 3]") >= 0,
      "expected '[image 3]' in text, got: " + JSON.stringify(ta.value));
    pass(ta.value.indexOf("[image 1]") < 0,
      "expected no '[image 1]' (it was removed earlier), got: " + JSON.stringify(ta.value));
  }

  // ---------- Assertion 7: remove without __textarea wired does not throw ----------
  {
    const w = buildDom();
    const container = w.document.getElementById("attachments");
    const pending = { items: [{ type: "image", mediaType: "image/png", data: new ArrayBuffer(1), name: "x.png", width: 1, height: 1, marker: 1 }] };
    w.SerfComposerAttachments.renderAttachmentChips(container, pending);

    const removeBtn = container.querySelector("[data-attachment-remove]");
    pass(removeBtn !== null, "precondition: remove button rendered");
    let threw = false;
    try {
      removeBtn.click();
    } catch (e) {
      threw = true;
    }
    pass(!threw, "expected remove to not throw when __textarea unset");
    pass(pending.items.length === 0, "expected items spliced even without __textarea, got " + pending.items.length);
  }

  // ---------- Assertion 8: marker is inserted synchronously at original cursor ----------
  {
    const w = buildDom();
    const ta = w.document.getElementById("ta");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerImageHandlers(ta, pending);

    ta.value = "alpha omega";
    setCursor(ta, 5);
    ta.dispatchEvent(buildPasteEvent(w, makeFile(w, PNG_BYTES, "slow.png", "image/png")));
    pass(ta.value === "alpha[image 1] omega",
      "expected marker immediately at original cursor, got: " + JSON.stringify(ta.value));
    pass(pending.items.length === 1 && pending.items[0].pending === true,
      "expected synchronous pending placeholder, got " + JSON.stringify(pending.items));
    setCursor(ta, ta.value.length);
    ta.value += " typed-later";
    await waitMicrotasks();
    pass(pending.items.length === 1 && pending.items[0].marker === 1 && pending.items[0].pending === false,
      "expected async decode to attach marker=1 after synchronous insertion");
    pass(ta.value === "alpha[image 1] omega typed-later",
      "expected later typing not to move marker, got: " + JSON.stringify(ta.value));
  }

  // ---------- Assertion 9: oversized image is rejected before decode ----------
  {
    const w = buildDom();
    const ta = w.document.getElementById("ta");
    const errors = w.document.getElementById("errors");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerImageHandlers(ta, pending);

    const big = makeFile(w, new Uint8Array(8 * 1024 * 1024 + 1), "big.png", "image/png");
    setCursor(ta, 0);
    ta.dispatchEvent(buildPasteEvent(w, big));
    await waitMicrotasks();
    pass(pending.items.length === 0, "expected oversized image rejected, got " + pending.items.length);
    pass(ta.value === "", "expected no marker for oversized image, got: " + JSON.stringify(ta.value));
    pass(!errors.hidden && /maximum 8 MB/.test(errors.textContent),
      "expected size rejection banner, got hidden=" + errors.hidden + " text=" + JSON.stringify(errors.textContent));
  }

  // ---------- Assertion 10: attachment count limit is enforced before decode ----------
  {
    const w = buildDom();
    const ta = w.document.getElementById("ta");
    const errors = w.document.getElementById("errors");
    const pending = { items: [] };
    for (let i = 1; i <= 8; i++) {
      pending.items.push({ type: "image", mediaType: "image/png", data: new ArrayBuffer(1), marker: i, name: "pre-" + i + ".png" });
    }
    w.SerfComposerAttachments.attachComposerImageHandlers(ta, pending);

    ta.dispatchEvent(buildPasteEvent(w, makeFile(w, PNG_BYTES, "ninth.png", "image/png")));
    await waitMicrotasks();
    pass(pending.items.length === 8, "expected ninth image rejected, got " + pending.items.length);
    pass(ta.value === "", "expected no marker for ninth image, got: " + JSON.stringify(ta.value));
    pass(!errors.hidden && /maximum 8 images/.test(errors.textContent),
      "expected count rejection banner, got hidden=" + errors.hidden + " text=" + JSON.stringify(errors.textContent));
  }

  if (failures.length === 0) {
    console.log("PASS: all assertions");
    process.exit(0);
  } else {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
})();
