import { afterEach, beforeEach, expect, test } from "vitest";
import { arrayBufferToBase64, reencodeToPng } from "./encodePng";

// jsdom has no real canvas 2d backend (no `canvas` npm package installed in
// this project), so getContext/toBlob and image decoding are stubbed here
// exactly the way this exact problem was already solved by the legacy
// composer's own jstest harness (cmd/serf-hub/jstest/test-paste-image.js,
// test-drag-drop-image.js, test-composer-image-markers.js all stub the
// identical seam) - reusing that proven technique rather than inventing a
// dependency-injection seam of our own, so the REAL production
// reencodeToPng (not a fake standing in for it) is what every test below
// actually exercises.
let originalGetContext: typeof HTMLCanvasElement.prototype.getContext;
let originalToBlob: typeof HTMLCanvasElement.prototype.toBlob;
let originalImage: typeof Image;
let originalCreateObjectURL: typeof URL.createObjectURL;
let originalRevokeObjectURL: typeof URL.revokeObjectURL;

beforeEach(() => {
  originalGetContext = HTMLCanvasElement.prototype.getContext;
  originalToBlob = HTMLCanvasElement.prototype.toBlob;
  originalImage = globalThis.Image;
  originalCreateObjectURL = URL.createObjectURL;
  originalRevokeObjectURL = URL.revokeObjectURL;
  URL.createObjectURL = () => "blob:fake";
  URL.revokeObjectURL = () => {};
});

afterEach(() => {
  HTMLCanvasElement.prototype.getContext = originalGetContext;
  HTMLCanvasElement.prototype.toBlob = originalToBlob;
  globalThis.Image = originalImage;
  URL.createObjectURL = originalCreateObjectURL;
  URL.revokeObjectURL = originalRevokeObjectURL;
});

// installDecodeStub makes `new Image()` resolve (via onload, on a
// microtask - real image decode is always async) to fixed dimensions, and
// makes canvas.toBlob hand back deterministic output bytes - so a test can
// assert exactly what reencodeToPng resolves with.
function installDecodeStub(outputBytes: Uint8Array, decodedWidth: number, decodedHeight: number): void {
  HTMLCanvasElement.prototype.getContext = (() => ({ drawImage() {} })) as unknown as typeof originalGetContext;
  HTMLCanvasElement.prototype.toBlob = function (this: HTMLCanvasElement, callback: BlobCallback, type?: string): void {
    callback(new Blob([new Uint8Array(outputBytes)], { type: type || "image/png" }));
  };

  class FakeImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    width = decodedWidth;
    height = decodedHeight;
    private _src = "";
    set src(value: string) {
      this._src = value;
      Promise.resolve().then(() => this.onload?.());
    }
    get src(): string {
      return this._src;
    }
  }
  // @ts-expect-error stubbing the global Image constructor for this test file only
  globalThis.Image = FakeImage;
}

function installDecodeErrorStub(): void {
  HTMLCanvasElement.prototype.getContext = (() => ({ drawImage() {} })) as unknown as typeof originalGetContext;
  class FailingImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    private _src = "";
    set src(value: string) {
      this._src = value;
      Promise.resolve().then(() => this.onerror?.());
    }
    get src(): string {
      return this._src;
    }
  }
  // @ts-expect-error stubbing the global Image constructor for this test file only
  globalThis.Image = FailingImage;
}

function installToBlobNullStub(decodedWidth: number, decodedHeight: number): void {
  HTMLCanvasElement.prototype.getContext = (() => ({ drawImage() {} })) as unknown as typeof originalGetContext;
  HTMLCanvasElement.prototype.toBlob = function (this: HTMLCanvasElement, callback: BlobCallback): void {
    callback(null);
  };
  class FakeImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    width = decodedWidth;
    height = decodedHeight;
    private _src = "";
    set src(value: string) {
      this._src = value;
      Promise.resolve().then(() => this.onload?.());
    }
    get src(): string {
      return this._src;
    }
  }
  // @ts-expect-error stubbing the global Image constructor for this test file only
  globalThis.Image = FakeImage;
}

// --- arrayBufferToBase64 -------------------------------------------------
// Fixture shared with test-submit-attachments.js (the legacy suite's own
// PNG signature bytes + known base64), so a correct implementation here is
// cross-checked against a value that suite already asserts independently.

test("encodes the PNG-signature fixture to the exact known base64", () => {
  const bytes = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  expect(arrayBufferToBase64(bytes.buffer)).toBe("iVBORw0KGgo=");
});

test("encodes an empty buffer to an empty string", () => {
  expect(arrayBufferToBase64(new ArrayBuffer(0))).toBe("");
});

test("round-trips a large buffer (exercises the chunked encode loop) losslessly", () => {
  const size = 0x8000 * 3 + 17; // spans multiple CHUNK_SIZE-sized chunks plus a remainder
  const bytes = new Uint8Array(size);
  for (let i = 0; i < size; i++) bytes[i] = i % 256;
  const encoded = arrayBufferToBase64(bytes.buffer);
  const decoded = atob(encoded);
  const roundTripped = new Uint8Array(decoded.length);
  for (let i = 0; i < decoded.length; i++) roundTripped[i] = decoded.charCodeAt(i);
  expect(Array.from(roundTripped)).toEqual(Array.from(bytes));
});

// --- reencodeToPng ---------------------------------------------------------

test("resolves with base64 data and the decoded image's dimensions", async () => {
  const outputBytes = new Uint8Array([1, 2, 3, 4]);
  installDecodeStub(outputBytes, 8, 4);
  const result = await reencodeToPng(new Blob([new Uint8Array([0xff])], { type: "image/jpeg" }));
  expect(result.width).toBe(8);
  expect(result.height).toBe(4);
  expect(result.data).toBe(arrayBufferToBase64(outputBytes.buffer));
});

test("re-encodes regardless of input mime type - output is always what the canvas produced", async () => {
  const outputBytes = new Uint8Array([0x89, 0x50, 0x4e, 0x47]);
  installDecodeStub(outputBytes, 2, 2);
  const result = await reencodeToPng(new Blob([new Uint8Array([1, 2, 3])], { type: "image/png" }));
  expect(result.data).toBe(arrayBufferToBase64(outputBytes.buffer));
});

test("rejects when the image fails to decode", async () => {
  installDecodeErrorStub();
  await expect(reencodeToPng(new Blob([new Uint8Array([1])], { type: "image/png" }))).rejects.toThrow();
});

test("rejects when canvas.toBlob hands back null", async () => {
  installToBlobNullStub(4, 4);
  await expect(reencodeToPng(new Blob([new Uint8Array([1])], { type: "image/png" }))).rejects.toThrow();
});

test("revokes the object URL after a successful decode (no blob: URL leak)", async () => {
  installDecodeStub(new Uint8Array([1]), 1, 1);
  let revoked = false;
  URL.revokeObjectURL = () => {
    revoked = true;
  };
  await reencodeToPng(new Blob([new Uint8Array([1])], { type: "image/png" }));
  expect(revoked).toBe(true);
});
