// Image decoding and encoding for composer attachments: decode once with the
// browser's EXIF-aware decoder, refuse a pixel bomb before any canvas exists,
// keep a fitting PNG as PNG (the canvas round-trip strips EXIF/ICC/text
// metadata), and compose everything else onto opaque white at a bounded long
// edge as JPEG. Every result is measured against the shared attachment byte
// limit before it may be sent.
import {
  fitImageDimensions,
  IMAGE_JPEG_QUALITY,
  MAX_ATTACHMENT_BYTES,
  MAX_IMAGE_LONG_EDGE,
  MAX_SOURCE_PIXELS,
  sizeRejection,
} from "@evener/appwire-client";

export type EncodedImageMediaType = "image/png" | "image/jpeg";

export interface EncodedImage {
  data: string; // base64 bytes, no "data:" prefix
  mediaType: EncodedImageMediaType;
  width: number;
  height: number;
}

export type ImageEncodeErrorCode = "decodeFailed" | "tooManyPixels" | "tooLarge";

export class ImageEncodeError extends Error {
  readonly code: ImageEncodeErrorCode;

  constructor(code: ImageEncodeErrorCode, message: string) {
    super(message);
    this.name = "ImageEncodeError";
    this.code = code;
  }
}

// A standard browser-safe (no Node Buffer) ArrayBuffer -> base64 conversion:
// chunked through String.fromCharCode so a large image does not blow the call
// stack by spreading every byte in one call.
const CHUNK_SIZE = 0x8000;

export function arrayBufferToBase64(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf);
  let binary = "";
  for (let i = 0; i < bytes.length; i += CHUNK_SIZE) {
    binary += String.fromCharCode(...bytes.subarray(i, i + CHUNK_SIZE));
  }
  return btoa(binary);
}

interface DecodedImage {
  source: CanvasImageSource;
  width: number;
  height: number;
  close(): void;
}

// decodeImage prefers createImageBitmap with the EXIF orientation applied and
// keeps the decoded dimensions it reports. SVG has no bitmap decode, and an
// older browser can reject the imageOrientation option outright, so both fall
// back to an <img>, whose object URL is revoked on every exit path.
async function decodeImage(blob: Blob): Promise<DecodedImage> {
  if (typeof createImageBitmap === "function" && blob.type !== "image/svg+xml") {
    try {
      const bitmap = await createImageBitmap(blob, { imageOrientation: "from-image" });
      return {
        source: bitmap,
        width: bitmap.width,
        height: bitmap.height,
        close: () => bitmap.close(),
      };
    } catch {
      // Fall through to the element decoder.
    }
  }
  const url = URL.createObjectURL(blob);
  const image = new Image();
  try {
    await new Promise<void>((resolve, reject) => {
      image.onload = () => resolve();
      image.onerror = () => reject(new ImageEncodeError("decodeFailed", "image decode failed"));
      image.src = url;
    });
  } finally {
    URL.revokeObjectURL(url);
  }
  return {
    source: image,
    // The width fallback keeps test stubs that only set element dimensions working.
    width: image.naturalWidth || image.width,
    height: image.naturalHeight || image.height,
    close: () => {},
  };
}

function canvasToBlob(canvas: HTMLCanvasElement, type: string, quality?: number): Promise<Blob> {
  return new Promise((resolve, reject) => {
    canvas.toBlob(
      (out) => {
        if (!out) {
          reject(new ImageEncodeError("decodeFailed", "canvas.toBlob returned null"));
          return;
        }
        resolve(out);
      },
      type,
      quality,
    );
  });
}

async function toEncodedImage(
  out: Blob,
  mediaType: EncodedImageMediaType,
  width: number,
  height: number,
): Promise<EncodedImage> {
  return {
    data: arrayBufferToBase64(await out.arrayBuffer()),
    mediaType,
    width,
    height,
  };
}

export async function encodeImage(blob: Blob): Promise<EncodedImage> {
  const decoded = await decodeImage(blob);
  let canvas: HTMLCanvasElement | null = null;
  try {
    const { width, height } = decoded;
    // fitImageDimensions assumes a positive finite size, so a decoder that
    // reports anything else is rejected here rather than producing a NaN canvas.
    if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) {
      throw new ImageEncodeError("decodeFailed", "image decode failed");
    }
    // A full-frame camera photo is the decode ceiling: refuse anything larger
    // before a canvas allocates its backing store.
    if (width * height > MAX_SOURCE_PIXELS) {
      throw new ImageEncodeError("tooManyPixels", "image exceeds 48 MP limit");
    }

    const target = fitImageDimensions(width, height);
    const keepPng =
      blob.type === "image/png" && Math.max(width, height) <= MAX_IMAGE_LONG_EDGE && blob.size <= MAX_ATTACHMENT_BYTES;

    canvas = document.createElement("canvas");
    canvas.width = target.width;
    canvas.height = target.height;
    const ctx = canvas.getContext("2d");
    if (!ctx) {
      throw new ImageEncodeError("decodeFailed", "2d context unavailable");
    }
    if (target.width < width || target.height < height) {
      ctx.imageSmoothingEnabled = true;
      ctx.imageSmoothingQuality = "high";
    }

    if (keepPng) {
      // No white fill: a PNG keeps its alpha through the round-trip that strips
      // its metadata.
      ctx.drawImage(decoded.source, 0, 0, target.width, target.height);
      const png = await canvasToBlob(canvas, "image/png");
      // The browser can inflate a fitting PNG past the send limit; fall through
      // to the JPEG branch on this same canvas instead of refusing it.
      if (sizeRejection(png.size) === undefined) {
        return await toEncodedImage(png, "image/png", target.width, target.height);
      }
    }

    // Opaque white keeps transparent inputs from turning black in JPEG.
    ctx.fillStyle = "#ffffff";
    ctx.fillRect(0, 0, target.width, target.height);
    ctx.drawImage(decoded.source, 0, 0, target.width, target.height);
    const jpeg = await canvasToBlob(canvas, "image/jpeg", IMAGE_JPEG_QUALITY);
    if (sizeRejection(jpeg.size) === "tooLarge") {
      throw new ImageEncodeError("tooLarge", "encoded image exceeds 8 MB limit");
    }
    return await toEncodedImage(jpeg, "image/jpeg", target.width, target.height);
  } finally {
    decoded.close();
    // iOS Safari keeps a canvas backing store until the element is collected,
    // so the image data is released as soon as the encode settles.
    if (canvas) {
      canvas.width = 0;
      canvas.height = 0;
    }
  }
}
