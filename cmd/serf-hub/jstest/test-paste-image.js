// Test harness: load composer-attachments.js into a JSDOM window and
// exercise the shared image-paste handler that both renderer.js and
// spawn.js will wire into. The module exposes window.SerfComposerAttachments
// with two functions:
//   attachComposerImageHandlers(textareaEl, pendingState)
//   renderAttachmentChips(containerEl, pendingState)
// The pendingState contract is {items: []} where each pasted image is
// stored as {type:"image", mediaType:"image/png", data:ArrayBuffer,
// name:"paste-<ts>.png", width, height}.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const MODULE_PATH = path.resolve(__dirname, "../assets/composer-attachments.js");
const moduleSrc = fs.readFileSync(MODULE_PATH, "utf8");

// A real 1x1 transparent PNG (base64). Used as the canonical "image is on
// the clipboard" fixture so the parser sees real PNG bytes if it tries.
const PNG_BASE64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+P+/HgAFhAJ/wlseKgAAAABJRU5ErkJggg==";
const PNG_BYTES = Buffer.from(PNG_BASE64, "base64");

// A 1x1 JPEG file (minimal valid JFIF). We use this to exercise the
// "non-PNG gets re-encoded via canvas to image/png" branch. The actual
// pixel decode happens in the (mocked) canvas, so the bytes don't need
// to be a perfectly valid JPEG — they just need a type prefix of
// "image/jpeg" coming through clipboardData.items.
const JPEG_BYTES = Buffer.from([0xff, 0xd8, 0xff, 0xe0, 0, 16, 0x4a, 0x46, 0x49, 0x46, 0]);

function buildDom() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <textarea id="ta"></textarea>
    <div id="attachments"></div>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
  const { window } = dom;
  // JSDOM lacks HTMLCanvasElement.toBlob in some versions; stub it to
  // return a PNG blob built from our fixture bytes. drawImage is a no-op.
  if (!window.HTMLCanvasElement.prototype.toBlob) {
    window.HTMLCanvasElement.prototype.toBlob = function (cb, mimeType) {
      cb(new window.Blob([new Uint8Array(PNG_BYTES)], { type: mimeType || "image/png" }));
    };
  } else {
    // Replace it so we always hand back our deterministic PNG bytes
    // regardless of the (empty) canvas state JSDOM keeps.
    window.HTMLCanvasElement.prototype.toBlob = function (cb, mimeType) {
      cb(new window.Blob([new Uint8Array(PNG_BYTES)], { type: mimeType || "image/png" }));
    };
  }
  // Stub the canvas 2d context so getContext("2d").drawImage doesn't blow up.
  window.HTMLCanvasElement.prototype.getContext = function () {
    return { drawImage() {} };
  };
  // Stub Image so loading dimensions resolves synchronously to known values.
  // The helper uses an Image() to decode the blob before drawing it to the
  // canvas — we want the tests to know the resulting width/height.
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
      // Resolve on a microtask so callers can attach onload first.
      Promise.resolve().then(() => { if (this.onload) this.onload(); });
    }
    get src() { return this._src; }
  }
  window.Image = FakeImage;
  // URL.createObjectURL is needed to feed the FakeImage from a blob.
  window.URL.createObjectURL = () => "blob:fake";
  window.URL.revokeObjectURL = () => {};
  window.eval(moduleSrc);
  return window;
}

function buildClipboardEvent(window, parts) {
  // parts is an array of {kind, type, file?, text?} mirroring the shape
  // of DataTransferItem entries surfaced through ClipboardEvent.
  const items = parts.map((p) => ({
    kind: p.kind,
    type: p.type,
    getAsFile() { return p.file || null; },
    getAsString(cb) { if (cb) cb(p.text || ""); },
  }));
  const event = new window.Event("paste", { bubbles: true, cancelable: true });
  // JSDOM doesn't ship a ClipboardEvent; bolt the items list on by hand.
  Object.defineProperty(event, "clipboardData", {
    value: {
      items,
      getData() { return parts.filter(p => p.kind === "string").map(p => p.text || "").join("") || ""; },
    },
  });
  return event;
}

function makeFile(window, bytes, name, type) {
  return new window.File([new Uint8Array(bytes)], name, { type });
}

function pass(cond, msg) { if (!cond) failures.push("FAIL: " + msg); }
const failures = [];

async function waitMicrotasks(n = 4) {
  for (let i = 0; i < n; i++) await new Promise((r) => setTimeout(r, 1));
}

(async () => {
  // ---------- Assertion 1: paste with one image blob queues one item ----------
  {
    const w = buildDom();
    const ta = w.document.getElementById("ta");
    const container = w.document.getElementById("attachments");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerImageHandlers(ta, pending);
    w.SerfComposerAttachments.renderAttachmentChips(container, pending);

    const file = makeFile(w, PNG_BYTES, "shot.png", "image/png");
    const ev = buildClipboardEvent(w, [{ kind: "file", type: "image/png", file }]);
    ta.dispatchEvent(ev);
    await waitMicrotasks(20);

    pass(pending.items.length === 1, "expected 1 queued item after PNG paste, got " + pending.items.length);
    const item = pending.items[0];
    pass(item && item.type === "image", "expected type=image, got " + (item && item.type));
    pass(item && item.mediaType === "image/png", "expected mediaType=image/png, got " + (item && item.mediaType));
    pass(item && item.data instanceof w.ArrayBuffer,
      "expected data to be ArrayBuffer, got " + (item && Object.prototype.toString.call(item.data)));
    pass(item && typeof item.name === "string" && item.name.startsWith("paste-") && item.name.endsWith(".png"),
      "expected name like paste-<ts>.png, got " + (item && item.name));
    pass(item && typeof item.width === "number" && item.width > 0, "expected width > 0, got " + (item && item.width));
    pass(item && typeof item.height === "number" && item.height > 0, "expected height > 0, got " + (item && item.height));
  }

  // ---------- Assertion 2: text-only paste leaves pendingAttachments alone ----------
  {
    const w = buildDom();
    const ta = w.document.getElementById("ta");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerImageHandlers(ta, pending);

    const ev = buildClipboardEvent(w, [{ kind: "string", type: "text/plain", text: "hello world" }]);
    ta.dispatchEvent(ev);
    await waitMicrotasks();

    pass(pending.items.length === 0, "expected no queued items for text-only paste, got " + pending.items.length);
    pass(!ev.defaultPrevented, "expected text paste to not preventDefault (let browser insert text)");
  }

  // ---------- Assertion 3: image + text paste extracts the image only ----------
  {
    const w = buildDom();
    const ta = w.document.getElementById("ta");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerImageHandlers(ta, pending);

    const file = makeFile(w, PNG_BYTES, "mix.png", "image/png");
    const ev = buildClipboardEvent(w, [
      { kind: "string", type: "text/plain", text: "see this:" },
      { kind: "file", type: "image/png", file },
    ]);
    ta.dispatchEvent(ev);
    await waitMicrotasks(20);

    pass(pending.items.length === 1, "expected 1 image queued from mixed paste, got " + pending.items.length);
    // The text portion is left to the browser's default insertion path — we
    // verify the helper did NOT preventDefault on the whole event, so the
    // browser will insert "see this:" normally. (preventDefault would block
    // both the image and the text.)
    pass(!ev.defaultPrevented, "expected mixed paste to not block default text insertion");
  }

  // ---------- Assertion 4: chip rendered with filename + dims ----------
  {
    const w = buildDom();
    const ta = w.document.getElementById("ta");
    const container = w.document.getElementById("attachments");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerImageHandlers(ta, pending);
    w.SerfComposerAttachments.renderAttachmentChips(container, pending);

    const file = makeFile(w, PNG_BYTES, "shot.png", "image/png");
    const ev = buildClipboardEvent(w, [{ kind: "file", type: "image/png", file }]);
    ta.dispatchEvent(ev);
    await waitMicrotasks(20);

    const chips = container.querySelectorAll("[data-attachment]");
    pass(chips.length === 1, "expected 1 [data-attachment] chip, got " + chips.length);
    const txt = chips.length ? chips[0].textContent : "";
    pass(txt.includes(pending.items[0].name), "expected chip to include filename, got: " + txt);
    pass(/\b\d+\s*[×x]\s*\d+\b/.test(txt), "expected chip to include WxH dimensions, got: " + txt);
  }

  // ---------- Assertion 5: clicking × removes the chip + the item ----------
  {
    const w = buildDom();
    const ta = w.document.getElementById("ta");
    const container = w.document.getElementById("attachments");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerImageHandlers(ta, pending);
    w.SerfComposerAttachments.renderAttachmentChips(container, pending);

    const file = makeFile(w, PNG_BYTES, "shot.png", "image/png");
    const ev = buildClipboardEvent(w, [{ kind: "file", type: "image/png", file }]);
    ta.dispatchEvent(ev);
    await waitMicrotasks(20);

    pass(pending.items.length === 1, "precondition: 1 item queued");
    const removeBtn = container.querySelector("[data-attachment-remove]");
    pass(removeBtn !== null, "expected remove button rendered");
    if (removeBtn) {
      removeBtn.click();
      pass(pending.items.length === 0, "expected items emptied after remove, got " + pending.items.length);
      pass(container.querySelectorAll("[data-attachment]").length === 0,
        "expected no chips after remove, got " + container.querySelectorAll("[data-attachment]").length);
    }
  }

  // ---------- Assertion 6: JPEG paste re-encodes to image/png ----------
  {
    const w = buildDom();
    const ta = w.document.getElementById("ta");
    const pending = { items: [] };
    w.SerfComposerAttachments.attachComposerImageHandlers(ta, pending);

    const file = makeFile(w, JPEG_BYTES, "phone.jpg", "image/jpeg");
    const ev = buildClipboardEvent(w, [{ kind: "file", type: "image/jpeg", file }]);
    ta.dispatchEvent(ev);
    await waitMicrotasks(20);

    pass(pending.items.length === 1, "expected 1 queued item after JPEG paste, got " + pending.items.length);
    if (pending.items[0]) {
      pass(pending.items[0].mediaType === "image/png",
        "expected JPEG to be re-encoded to image/png, got " + pending.items[0].mediaType);
    }
  }

  if (failures.length === 0) {
    console.log("PASS: all assertions");
    process.exit(0);
  } else {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
})();
