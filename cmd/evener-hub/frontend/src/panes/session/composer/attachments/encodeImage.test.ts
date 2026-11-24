import { MAX_ATTACHMENT_BYTES, MAX_SOURCE_PIXELS } from "@evener/appwire-client";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { arrayBufferToBase64, encodeImage, ImageEncodeError } from "./encodeImage";

// jsdom has no canvas 2d backend, no image decoder and no createImageBitmap, so
// every browser primitive this encoder touches is stubbed at that boundary -
// the same technique encodePng.test.ts and the legacy jstest harness use. The
// real encodeImage below the boundary is what each test exercises.

type CreateImageBitmap = typeof globalThis.createImageBitmap;
type ImageConstructor = typeof globalThis.Image;

interface FakeCanvasContext {
  imageSmoothingEnabled: boolean;
  imageSmoothingQuality: ImageSmoothingQuality;
  fillStyle: string | CanvasGradient | CanvasPattern;
  drawImageCalls: unknown[][];
  fillRectCalls: { x: number; y: number; width: number; height: number; fillStyle: string }[];
  drawImage(...args: unknown[]): void;
  fillRect(x: number, y: number, width: number, height: number): void;
}

interface ToBlobCall {
  type: string | undefined;
  quality: number | undefined;
}

interface CanvasProbe {
  canvases: HTMLCanvasElement[];
  contexts: FakeCanvasContext[];
  toBlobCalls: ToBlobCall[];
}

interface BitmapProbe {
  bitmap: ImageBitmap;
  close: ReturnType<typeof vi.fn>;
  createImageBitmap: ReturnType<typeof vi.fn>;
}

interface FakeImageElement {
  onload: (() => void) | null;
  onerror: (() => void) | null;
  naturalWidth: number;
  naturalHeight: number;
  width: number;
  height: number;
  src: string;
}

let originalGetContext: typeof HTMLCanvasElement.prototype.getContext;
let originalToBlob: typeof HTMLCanvasElement.prototype.toBlob;
let originalImage: ImageConstructor;
let originalCreateImageBitmap: CreateImageBitmap;
let originalCreateObjectURL: typeof URL.createObjectURL;
let originalRevokeObjectURL: typeof URL.revokeObjectURL;
let createObjectURLMock: ReturnType<typeof vi.fn>;
let revokeObjectURLMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  originalGetContext = HTMLCanvasElement.prototype.getContext;
  originalToBlob = HTMLCanvasElement.prototype.toBlob;
  originalImage = globalThis.Image;
  originalCreateImageBitmap = globalThis.createImageBitmap;
  originalCreateObjectURL = URL.createObjectURL;
  originalRevokeObjectURL = URL.revokeObjectURL;
  globalThis.createImageBitmap = undefined as unknown as CreateImageBitmap;
  createObjectURLMock = vi.fn(() => "blob:test-url");
  revokeObjectURLMock = vi.fn(() => {});
  URL.createObjectURL = createObjectURLMock as unknown as typeof URL.createObjectURL;
  URL.revokeObjectURL = revokeObjectURLMock as unknown as typeof URL.revokeObjectURL;
});

afterEach(() => {
  HTMLCanvasElement.prototype.getContext = originalGetContext;
  HTMLCanvasElement.prototype.toBlob = originalToBlob;
  globalThis.Image = originalImage;
  globalThis.createImageBitmap = originalCreateImageBitmap;
  URL.createObjectURL = originalCreateObjectURL;
  URL.revokeObjectURL = originalRevokeObjectURL;
  vi.restoreAllMocks();
});

function installBitmap(width: number, height: number): BitmapProbe {
  const close = vi.fn();
  const bitmap = { width, height, close } as unknown as ImageBitmap;
  const createImageBitmap = vi.fn(async () => bitmap);
  globalThis.createImageBitmap = createImageBitmap as unknown as CreateImageBitmap;
  return { bitmap, close, createImageBitmap };
}

function installRejectingBitmap(): ReturnType<typeof vi.fn> {
  const createImageBitmap = vi.fn(async () => {
    throw new Error("createImageBitmap unavailable");
  });
  globalThis.createImageBitmap = createImageBitmap as unknown as CreateImageBitmap;
  return createImageBitmap;
}

function installImageDecode(width: number, height: number, fail = false): FakeImageElement[] {
  const images: FakeImageElement[] = [];
  class FakeImage implements FakeImageElement {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    naturalWidth = width;
    naturalHeight = height;
    width = width;
    height = height;
    private _src = "";
    constructor() {
      images.push(this);
    }
    set src(value: string) {
      this._src = value;
      Promise.resolve().then(() => (fail ? this.onerror?.() : this.onload?.()));
    }
    get src(): string {
      return this._src;
    }
  }
  globalThis.Image = FakeImage as unknown as ImageConstructor;
  return images;
}

function installCanvas(
  options: { contextAvailable?: boolean; toBlob?: (call: ToBlobCall) => Blob | null } = {},
): CanvasProbe {
  const probe: CanvasProbe = { canvases: [], contexts: [], toBlobCalls: [] };
  HTMLCanvasElement.prototype.getContext = function (this: HTMLCanvasElement, contextId: string) {
    probe.canvases.push(this);
    if (contextId !== "2d" || options.contextAvailable === false) return null;
    const context: FakeCanvasContext = {
      imageSmoothingEnabled: false,
      imageSmoothingQuality: "low",
      fillStyle: "#000000",
      drawImageCalls: [],
      fillRectCalls: [],
      drawImage(...args: unknown[]) {
        context.drawImageCalls.push(args);
      },
      fillRect(x: number, y: number, width: number, height: number) {
        context.fillRectCalls.push({ x, y, width, height, fillStyle: String(context.fillStyle) });
      },
    };
    probe.contexts.push(context);
    return context as unknown as CanvasRenderingContext2D;
  } as typeof HTMLCanvasElement.prototype.getContext;
  HTMLCanvasElement.prototype.toBlob = function (
    this: HTMLCanvasElement,
    callback: BlobCallback,
    type?: string,
    quality?: number,
  ): void {
    const call = { type, quality };
    probe.toBlobCalls.push(call);
    callback(options.toBlob ? options.toBlob(call) : null);
  } as typeof HTMLCanvasElement.prototype.toBlob;
  return probe;
}

async function captureEncodeError(blob: Blob): Promise<ImageEncodeError> {
  try {
    await encodeImage(blob);
  } catch (error) {
    if (error instanceof ImageEncodeError) return error;
    throw error;
  }
  throw new Error("encodeImage resolved; expected an ImageEncodeError");
}

function firstContext(probe: CanvasProbe): FakeCanvasContext {
  const context = probe.contexts[0];
  if (!context) throw new Error("expected encodeImage to acquire a 2d context");
  return context;
}

function firstCanvas(probe: CanvasProbe): HTMLCanvasElement {
  const canvas = probe.canvases[0];
  if (!canvas) throw new Error("expected encodeImage to allocate a canvas");
  return canvas;
}

function firstImage(images: FakeImageElement[]): FakeImageElement {
  const image = images[0];
  if (!image) throw new Error("expected encodeImage to construct an HTMLImageElement");
  return image;
}

function firstDrawImageCall(context: FakeCanvasContext): unknown[] {
  const call = context.drawImageCalls[0];
  if (!call) throw new Error("expected encodeImage to draw the decoded image");
  return call;
}

function blobOf(type: string, bytes: number[] = [1]): Blob {
  return new Blob([new Uint8Array(bytes)], { type });
}

function canvasCreations(createElement: { mock: { calls: readonly unknown[][] } }): number {
  return createElement.mock.calls.filter((call) => call[0] === "canvas").length;
}

// --- arrayBufferToBase64 ---------------------------------------------------

describe("arrayBufferToBase64", () => {
  test("encodes the PNG-signature fixture to the exact known base64", () => {
    const bytes = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
    expect(arrayBufferToBase64(bytes.buffer)).toBe("iVBORw0KGgo=");
  });

  test("encodes an empty buffer to an empty string", () => {
    expect(arrayBufferToBase64(new ArrayBuffer(0))).toBe("");
  });

  test("round-trips a large buffer losslessly across chunk boundaries", () => {
    const size = 0x8000 * 3 + 17;
    const bytes = new Uint8Array(size);
    for (let i = 0; i < size; i++) bytes[i] = i % 256;
    const decoded = atob(arrayBufferToBase64(bytes.buffer));
    const roundTripped = new Uint8Array(decoded.length);
    for (let i = 0; i < decoded.length; i++) roundTripped[i] = decoded.charCodeAt(i);
    expect(Array.from(roundTripped)).toEqual(Array.from(bytes));
  });
});

// --- encodeImage: decoding -------------------------------------------------

describe("encodeImage decoding", () => {
  test("decodes through createImageBitmap with the from-image EXIF orientation and closes the bitmap", async () => {
    const blob = blobOf("image/png");
    const { close, createImageBitmap } = installBitmap(800, 600);
    const probe = installCanvas({ toBlob: () => blobOf("image/png", [1, 2, 3, 4]) });

    const result = await encodeImage(blob);

    expect(createImageBitmap).toHaveBeenCalledWith(blob, { imageOrientation: "from-image" });
    expect(close).toHaveBeenCalledTimes(1);
    expect(result.mediaType).toBe("image/png");
    expect(probe.toBlobCalls).toHaveLength(1);
  });

  test("keeps a fitting PNG as image/png with its exact canvas bytes and no white fill", async () => {
    const blob = blobOf("image/png", [1, 2, 3]);
    const { bitmap } = installBitmap(800, 600);
    const probe = installCanvas({ toBlob: () => blobOf("image/png", [1, 2, 3, 4]) });

    const result = await encodeImage(blob);

    expect(result).toEqual({ data: "AQIDBA==", mediaType: "image/png", width: 800, height: 600, byteLength: 4 });
    expect(probe.toBlobCalls).toEqual([{ type: "image/png", quality: undefined }]);
    const context = firstContext(probe);
    expect(context.drawImageCalls).toEqual([[bitmap, 0, 0, 800, 600]]);
    expect(context.fillRectCalls).toEqual([]);
    const canvas = firstCanvas(probe);
    expect(canvas.width).toBe(0);
    expect(canvas.height).toBe(0);
  });

  test("falls back to HTMLImageElement when createImageBitmap rejects and revokes its object URL", async () => {
    const blob = blobOf("image/png", [1]);
    const createImageBitmap = installRejectingBitmap();
    const images = installImageDecode(800, 600);
    const probe = installCanvas({ toBlob: () => blobOf("image/png", [1, 2, 3]) });

    const result = await encodeImage(blob);

    expect(createImageBitmap).toHaveBeenCalledWith(blob, { imageOrientation: "from-image" });
    expect(images).toHaveLength(1);
    expect(createObjectURLMock).toHaveBeenCalledWith(blob);
    expect(revokeObjectURLMock).toHaveBeenCalledWith("blob:test-url");
    expect(firstContext(probe).drawImageCalls).toEqual([[firstImage(images), 0, 0, 800, 600]]);
    expect(result.mediaType).toBe("image/png");
  });

  test("decodes image/svg+xml through HTMLImageElement without asking createImageBitmap", async () => {
    const blob = blobOf("image/svg+xml", [1]);
    const { createImageBitmap } = installBitmap(800, 600);
    const images = installImageDecode(300, 200);
    const probe = installCanvas({ toBlob: () => blobOf("image/jpeg", [1, 2, 3]) });

    const result = await encodeImage(blob);

    expect(createImageBitmap).not.toHaveBeenCalled();
    expect(images).toHaveLength(1);
    expect(createObjectURLMock).toHaveBeenCalledWith(blob);
    expect(revokeObjectURLMock).toHaveBeenCalledWith("blob:test-url");
    expect(firstContext(probe).drawImageCalls).toEqual([[firstImage(images), 0, 0, 300, 200]]);
    expect(result).toEqual({ data: "AQID", mediaType: "image/jpeg", width: 300, height: 200, byteLength: 3 });
  });

  test("rejects with decodeFailed and revokes the object URL when HTMLImageElement cannot decode", async () => {
    installImageDecode(0, 0, true);

    const error = await captureEncodeError(blobOf("image/svg+xml"));

    expect(error.code).toBe("decodeFailed");
    expect(revokeObjectURLMock).toHaveBeenCalledWith("blob:test-url");
  });
});

// --- encodeImage: pre-canvas gates -----------------------------------------

describe("encodeImage pre-canvas gates", () => {
  test("rejects an image with zero decoded dimensions before allocating a canvas", async () => {
    const { close } = installBitmap(0, 0);
    const createElement = vi.spyOn(document, "createElement");

    const error = await captureEncodeError(blobOf("image/jpeg"));

    expect(error.code).toBe("decodeFailed");
    expect(close).toHaveBeenCalledTimes(1);
    expect(canvasCreations(createElement)).toBe(0);
  });

  test("rejects an image with non-finite decoded dimensions", async () => {
    const { close } = installBitmap(Number.NaN, 100);

    const error = await captureEncodeError(blobOf("image/jpeg"));

    expect(error.code).toBe("decodeFailed");
    expect(close).toHaveBeenCalledTimes(1);
  });

  test("rejects an image above 48 MP with tooManyPixels, closing the bitmap before any canvas", async () => {
    const width = 8064;
    const height = 6049;
    expect(width * height).toBeGreaterThan(MAX_SOURCE_PIXELS);
    const { close } = installBitmap(width, height);
    const createElement = vi.spyOn(document, "createElement");

    const error = await captureEncodeError(blobOf("image/jpeg"));

    expect(error.code).toBe("tooManyPixels");
    expect(close).toHaveBeenCalledTimes(1);
    expect(canvasCreations(createElement)).toBe(0);
  });
});

// --- encodeImage: format selection -----------------------------------------

describe("encodeImage format selection", () => {
  test("falls back to a white-composited JPEG at 0.85 when the canvas PNG exceeds 8 MB", async () => {
    const blob = blobOf("image/png", [1]);
    const { bitmap } = installBitmap(800, 600);
    const oversizedPng = new Blob([new Uint8Array(MAX_ATTACHMENT_BYTES + 1)], { type: "image/png" });
    const jpeg = blobOf("image/jpeg", [9, 8, 7]);
    const probe = installCanvas({ toBlob: ({ type }) => (type === "image/png" ? oversizedPng : jpeg) });

    const result = await encodeImage(blob);

    expect(probe.toBlobCalls).toEqual([
      { type: "image/png", quality: undefined },
      { type: "image/jpeg", quality: 0.85 },
    ]);
    const context = firstContext(probe);
    expect(context.fillRectCalls).toEqual([{ x: 0, y: 0, width: 800, height: 600, fillStyle: "#ffffff" }]);
    expect(context.drawImageCalls).toEqual([
      [bitmap, 0, 0, 800, 600],
      [bitmap, 0, 0, 800, 600],
    ]);
    expect(result).toEqual({ data: "CQgH", mediaType: "image/jpeg", width: 800, height: 600, byteLength: 3 });
  });

  test("encodes a raw PNG over 8 MB as JPEG even when it fits the long edge", async () => {
    const blob = new Blob([new Uint8Array(MAX_ATTACHMENT_BYTES + 1)], { type: "image/png" });
    installBitmap(800, 600);
    const probe = installCanvas({ toBlob: () => blobOf("image/jpeg", [1, 2, 3]) });

    const result = await encodeImage(blob);

    expect(probe.toBlobCalls).toEqual([{ type: "image/jpeg", quality: 0.85 }]);
    expect(firstContext(probe).fillRectCalls).toHaveLength(1);
    expect(result).toEqual({ data: "AQID", mediaType: "image/jpeg", width: 800, height: 600, byteLength: 3 });
  });

  test.each(["image/jpeg", "image/webp", "image/svg+xml", "image/png"])(
    "downscales %s to the 2048 px long edge on a white canvas as JPEG 0.85",
    async (type) => {
      installBitmap(4032, 3024);
      installImageDecode(4032, 3024);
      const probe = installCanvas({ toBlob: () => blobOf("image/jpeg", [9, 8, 7]) });

      const result = await encodeImage(blobOf(type, [1]));

      expect(result).toEqual({ data: "CQgH", mediaType: "image/jpeg", width: 2048, height: 1536, byteLength: 3 });
      const context = firstContext(probe);
      expect(context.fillRectCalls).toEqual([{ x: 0, y: 0, width: 2048, height: 1536, fillStyle: "#ffffff" }]);
      expect(context.imageSmoothingEnabled).toBe(true);
      expect(context.imageSmoothingQuality).toBe("high");
      expect(context.drawImageCalls).toHaveLength(1);
      expect(firstDrawImageCall(context).slice(1)).toEqual([0, 0, 2048, 1536]);
      expect(probe.toBlobCalls).toEqual([{ type: "image/jpeg", quality: 0.85 }]);
      const canvas = firstCanvas(probe);
      expect(canvas.width).toBe(0);
      expect(canvas.height).toBe(0);
    },
  );
});

// --- encodeImage: encode failures ------------------------------------------

describe("encodeImage encode failures", () => {
  test("rejects with tooLarge when the encoded JPEG exceeds 8 MB, zeroing the canvas and closing the bitmap", async () => {
    const { close } = installBitmap(800, 600);
    const probe = installCanvas({
      toBlob: () => new Blob([new Uint8Array(MAX_ATTACHMENT_BYTES + 1)], { type: "image/jpeg" }),
    });

    const error = await captureEncodeError(blobOf("image/jpeg"));

    expect(error.code).toBe("tooLarge");
    expect(close).toHaveBeenCalledTimes(1);
    const canvas = firstCanvas(probe);
    expect(canvas.width).toBe(0);
    expect(canvas.height).toBe(0);
  });

  test("rejects with decodeFailed when the 2d context is unavailable, zeroing the canvas", async () => {
    installBitmap(800, 600);
    const probe = installCanvas({ contextAvailable: false });

    const error = await captureEncodeError(blobOf("image/jpeg"));

    expect(error.code).toBe("decodeFailed");
    const canvas = firstCanvas(probe);
    expect(canvas.width).toBe(0);
    expect(canvas.height).toBe(0);
  });

  test("rejects with decodeFailed when canvas.toBlob hands back null, zeroing the canvas", async () => {
    installBitmap(800, 600);
    const probe = installCanvas({ toBlob: () => null });

    const error = await captureEncodeError(blobOf("image/png"));

    expect(error.code).toBe("decodeFailed");
    const canvas = firstCanvas(probe);
    expect(canvas.width).toBe(0);
    expect(canvas.height).toBe(0);
  });
});
