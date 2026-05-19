// test-submit-attachments: covers kata v80q — web submit flow base64-encodes
// image attachments and threads them through all four POST entry points:
//   1. POST /api/spawn          (spawn.js submit handler)
//   2. POST /s/<id>/send        (renderer.js composer submit)
//   3. POST /s/<id>/queue       (renderer.js queue while processing)
//   4. POST /s/<id>/drain-as-steer (renderer.js steer-with-attachments)
//
// Each handler should accept the JSON shape:
//   { items: [
//       { type: "text", text: "..." },
//       { type: "image", mediaType: "image/png", data: "<base64>", name }
//     ] }
//
// We verify base64 encoding (no ArrayBuffer-shaped binary in the JSON body),
// multi-attachment, empty attachments, and round-trip naming.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const APPWIRE_PATH = path.resolve(__dirname, "../assets/appwire.js");
const appwireSrc = fs.readFileSync(APPWIRE_PATH, "utf8");

const failures = [];
function pass(cond, msg) { if (!cond) failures.push("FAIL: " + msg); }

// PNG signature bytes — 8 bytes is enough to assert base64 encoding without
// asserting an actual image decode. Used as the test fixture across all
// four entry points.
const PNG_BYTES = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
const PNG_B64 = "iVBORw0KGgo=";  // canonical base64 of the 8 PNG signature bytes
const PNG2_BYTES = new Uint8Array([0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46]);
const PNG2_B64 = "/9j/4AAQSkY=";

// arrayBuf returns a fresh ArrayBuffer view of the given Uint8Array. Each
// fixture starts as a Uint8Array (deterministic bytes) and we hand the
// composer-attachments-style ArrayBuffer to the wire layer.
function arrayBuf(u8) {
  const buf = new ArrayBuffer(u8.length);
  new Uint8Array(buf).set(u8);
  return buf;
}

// ---------- 1. appwire.startTurn accepts attachments + base64-encodes ----------
async function testAppwireStartTurnEncodesAttachments() {
  const dom = new JSDOM("<!DOCTYPE html><html><body></body></html>", {
    url: "http://127.0.0.1:9180/",
    runScripts: "outside-only",
    pretendToBeVisual: true,
  });
  const { window } = dom;
  // Stub WebSocket so request() can hand the JSON to our spy via send().
  let sentJSON = null;
  class StubWS {
    constructor() {
      this.readyState = 1; // OPEN
      this.listeners = {};
      StubWS.last = this;
      // Drive the initialize handshake to completion so subsequent
      // method calls don't queue forever waiting on a response. We resolve
      // it by injecting a JSON-RPC reply after a microtask.
      setTimeout(() => {
        if (this.listeners.open) this.listeners.open[0]();
      }, 0);
    }
    addEventListener(name, fn) {
      (this.listeners[name] = this.listeners[name] || []).push(fn);
    }
    send(body) {
      // The very first send is the "initialize" handshake — reply with a
      // synthetic result so the pending promise resolves and startTurn can
      // be invoked next. Subsequent sends capture the JSON body for the
      // assertion below.
      const msg = JSON.parse(body);
      if (msg.method === "initialize") {
        setTimeout(() => {
          if (this.listeners.message) this.listeners.message[0]({ data: JSON.stringify({ id: msg.id, result: {} }) });
        }, 0);
        return;
      }
      sentJSON = msg;
      // Resolve immediately so callers can continue.
      setTimeout(() => {
        if (this.listeners.message) this.listeners.message[0]({ data: JSON.stringify({ id: msg.id, result: {} }) });
      }, 0);
    }
    close() {}
  }
  window.WebSocket = StubWS;
  // Constants like OPEN.
  window.WebSocket.OPEN = 1;

  window.eval(appwireSrc);

  // Drive a startTurn with one PNG attachment.
  const attachments = [{
    type: "image",
    mediaType: "image/png",
    data: arrayBuf(PNG_BYTES),
    name: "paste-1.png",
  }];
  await window.SerfAppwire.startTurn("local:01TEST", "describe this", attachments);
  pass(sentJSON !== null, "startTurn should have sent a JSON-RPC frame");
  if (!sentJSON) return;
  pass(sentJSON.method === "turn/start",
    "expected method=turn/start, got " + sentJSON.method);
  pass(sentJSON.params && Array.isArray(sentJSON.params.items),
    "expected params.items to be an array, got " + JSON.stringify(sentJSON.params));
  const items = (sentJSON.params && sentJSON.params.items) || [];
  // We expect the text portion to land as a text item at items[0] (or as
  // params.prompt — daemon accepts either). For the v80q contract we want
  // items[N] to include the image entry with base64-encoded data.
  const imageEntries = items.filter((it) => it && it.type === "image");
  pass(imageEntries.length === 1,
    "expected exactly 1 image entry in items, got " + JSON.stringify(items));
  if (imageEntries.length === 1) {
    const img = imageEntries[0];
    pass(img.mediaType === "image/png", "expected mediaType=image/png, got " + img.mediaType);
    pass(typeof img.data === "string",
      "expected data to be a base64 string (not ArrayBuffer/object), got " + (typeof img.data));
    pass(img.data === PNG_B64,
      "expected data='" + PNG_B64 + "', got " + JSON.stringify(img.data));
    pass(img.name === "paste-1.png",
      "expected name=paste-1.png, got " + JSON.stringify(img.name));
  }
}

// ---------- 2. multi-attachment ----------
async function testMultiAttachmentEntries() {
  const dom = new JSDOM("<!DOCTYPE html><html><body></body></html>", {
    url: "http://127.0.0.1:9180/",
    runScripts: "outside-only",
    pretendToBeVisual: true,
  });
  const { window } = dom;
  let sentJSON = null;
  class StubWS {
    constructor() {
      this.readyState = 1;
      this.listeners = {};
      setTimeout(() => { if (this.listeners.open) this.listeners.open[0](); }, 0);
    }
    addEventListener(name, fn) { (this.listeners[name] = this.listeners[name] || []).push(fn); }
    send(body) {
      const msg = JSON.parse(body);
      if (msg.method === "initialize") {
        setTimeout(() => { if (this.listeners.message) this.listeners.message[0]({ data: JSON.stringify({ id: msg.id, result: { features: { turnDrainAsSteerInput: true } } }) }); }, 0);
        return;
      }
      sentJSON = msg;
      setTimeout(() => { if (this.listeners.message) this.listeners.message[0]({ data: JSON.stringify({ id: msg.id, result: {} }) }); }, 0);
    }
    close() {}
  }
  window.WebSocket = StubWS;
  window.WebSocket.OPEN = 1;
  window.eval(appwireSrc);

  const attachments = [
    { type: "image", mediaType: "image/png", data: arrayBuf(PNG_BYTES), name: "a.png" },
    { type: "image", mediaType: "image/png", data: arrayBuf(PNG2_BYTES), name: "b.png" },
  ];
  await window.SerfAppwire.startTurn("local:01TEST", "two pics", attachments);
  pass(sentJSON !== null, "multi: startTurn should have sent JSON-RPC");
  const items = (sentJSON && sentJSON.params && sentJSON.params.items) || [];
  const imgs = items.filter((it) => it && it.type === "image");
  pass(imgs.length === 2, "multi: expected 2 image entries, got " + imgs.length);
  if (imgs.length === 2) {
    pass(imgs[0].data === PNG_B64, "multi: first image base64 mismatch, got " + imgs[0].data);
    pass(imgs[1].data === PNG2_B64, "multi: second image base64 mismatch, got " + imgs[1].data);
  }
}

// ---------- 3. empty attachments leaves items text-only or absent ----------
async function testEmptyAttachmentsSendsNoImageItems() {
  const dom = new JSDOM("<!DOCTYPE html><html><body></body></html>", {
    url: "http://127.0.0.1:9180/",
    runScripts: "outside-only",
    pretendToBeVisual: true,
  });
  const { window } = dom;
  let sentJSON = null;
  class StubWS {
    constructor() {
      this.readyState = 1;
      this.listeners = {};
      setTimeout(() => { if (this.listeners.open) this.listeners.open[0](); }, 0);
    }
    addEventListener(name, fn) { (this.listeners[name] = this.listeners[name] || []).push(fn); }
    send(body) {
      const msg = JSON.parse(body);
      if (msg.method === "initialize") {
        setTimeout(() => { if (this.listeners.message) this.listeners.message[0]({ data: JSON.stringify({ id: msg.id, result: { features: { turnDrainAsSteerInput: true } } }) }); }, 0);
        return;
      }
      sentJSON = msg;
      setTimeout(() => { if (this.listeners.message) this.listeners.message[0]({ data: JSON.stringify({ id: msg.id, result: {} }) }); }, 0);
    }
    close() {}
  }
  window.WebSocket = StubWS;
  window.WebSocket.OPEN = 1;
  window.eval(appwireSrc);

  await window.SerfAppwire.startTurn("local:01TEST", "just text", []);
  pass(sentJSON !== null, "empty: startTurn should have sent JSON-RPC");
  const items = (sentJSON && sentJSON.params && sentJSON.params.items) || [];
  const imgs = items.filter((it) => it && it.type === "image");
  pass(imgs.length === 0, "empty: expected 0 image entries, got " + imgs.length);
}

// ---------- 4. queueTurn accepts attachments ----------
async function testQueueTurnEncodesAttachments() {
  const dom = new JSDOM("<!DOCTYPE html><html><body></body></html>", {
    url: "http://127.0.0.1:9180/",
    runScripts: "outside-only",
    pretendToBeVisual: true,
  });
  const { window } = dom;
  let sentJSON = null;
  class StubWS {
    constructor() {
      this.readyState = 1;
      this.listeners = {};
      setTimeout(() => { if (this.listeners.open) this.listeners.open[0](); }, 0);
    }
    addEventListener(name, fn) { (this.listeners[name] = this.listeners[name] || []).push(fn); }
    send(body) {
      const msg = JSON.parse(body);
      if (msg.method === "initialize") {
        setTimeout(() => { if (this.listeners.message) this.listeners.message[0]({ data: JSON.stringify({ id: msg.id, result: { features: { turnDrainAsSteerInput: true } } }) }); }, 0);
        return;
      }
      sentJSON = msg;
      setTimeout(() => { if (this.listeners.message) this.listeners.message[0]({ data: JSON.stringify({ id: msg.id, result: {} }) }); }, 0);
    }
    close() {}
  }
  window.WebSocket = StubWS;
  window.WebSocket.OPEN = 1;
  window.eval(appwireSrc);

  const attachments = [{ type: "image", mediaType: "image/png", data: arrayBuf(PNG_BYTES), name: "q.png" }];
  await window.SerfAppwire.queueTurn("local:01TEST", "queue me", attachments);
  pass(sentJSON !== null, "queue: should have sent JSON-RPC");
  pass(sentJSON && sentJSON.method === "turn/queue",
    "queue: expected method=turn/queue, got " + (sentJSON && sentJSON.method));
  const items = (sentJSON && sentJSON.params && sentJSON.params.items) || [];
  const imgs = items.filter((it) => it && it.type === "image");
  pass(imgs.length === 1, "queue: expected 1 image entry, got " + imgs.length);
  if (imgs.length === 1) {
    pass(imgs[0].data === PNG_B64, "queue: image base64 mismatch, got " + imgs[0].data);
  }
}

// ---------- 5. drainAsSteer accepts attachments ----------
// drainAsSteer with attachments sends one atomic turn/drainAsSteer request.
async function testDrainAsSteerWithAttachmentsQueuesThenDrains() {
  const dom = new JSDOM("<!DOCTYPE html><html><body></body></html>", {
    url: "http://127.0.0.1:9180/",
    runScripts: "outside-only",
    pretendToBeVisual: true,
  });
  const { window } = dom;
  const sentJSONs = [];
  class StubWS {
    constructor() {
      this.readyState = 1;
      this.listeners = {};
      setTimeout(() => { if (this.listeners.open) this.listeners.open[0](); }, 0);
    }
    addEventListener(name, fn) { (this.listeners[name] = this.listeners[name] || []).push(fn); }
    send(body) {
      const msg = JSON.parse(body);
      if (msg.method === "initialize") {
        setTimeout(() => { if (this.listeners.message) this.listeners.message[0]({ data: JSON.stringify({ id: msg.id, result: { features: { turnDrainAsSteerInput: true } } }) }); }, 0);
        return;
      }
      sentJSONs.push(msg);
      setTimeout(() => { if (this.listeners.message) this.listeners.message[0]({ data: JSON.stringify({ id: msg.id, result: {} }) }); }, 0);
    }
    close() {}
  }
  window.WebSocket = StubWS;
  window.WebSocket.OPEN = 1;
  window.eval(appwireSrc);

  const attachments = [{ type: "image", mediaType: "image/png", data: arrayBuf(PNG_BYTES), name: "d.png" }];
  await window.SerfAppwire.drainAsSteer("local:01TEST", "drain text", attachments);
  const methods = sentJSONs.map((m) => m.method);
  pass(methods.length === 1 && methods[0] === "turn/drainAsSteer",
    "drain: expected one turn/drainAsSteer call, got " + JSON.stringify(methods));
  const drainCall = sentJSONs.find((m) => m.method === "turn/drainAsSteer");
  if (drainCall) {
    pass(drainCall.params && drainCall.params.text === "drain text",
      "drain: params should carry text, got " + JSON.stringify(drainCall.params));
    const items = (drainCall.params && drainCall.params.items) || [];
    const imgs = items.filter((it) => it && it.type === "image");
    pass(imgs.length === 1 && imgs[0].data === PNG_B64,
      "drain: params items should carry the base64 image, got " + JSON.stringify(items));
  }
}

(async () => {
  await testAppwireStartTurnEncodesAttachments();
  await testMultiAttachmentEntries();
  await testEmptyAttachmentsSendsNoImageItems();
  await testQueueTurnEncodesAttachments();
  await testDrainAsSteerWithAttachmentsQueuesThenDrains();

  if (failures.length === 0) {
    console.log("PASS: web submit flow base64-encodes attachments across all entry points");
    process.exit(0);
  } else {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
})();
