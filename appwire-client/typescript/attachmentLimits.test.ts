// @vitest-environment node
import { expect, test } from "vitest";
import {
  admissionRejection,
  fitImageDimensions,
  IMAGE_JPEG_QUALITY,
  MAX_ATTACHMENT_BYTES,
  MAX_ATTACHMENTS,
  MAX_IMAGE_LONG_EDGE,
  MAX_SOURCE_BYTES,
  MAX_SOURCE_PIXELS,
  rejectionReason,
  sizeRejection,
} from "./attachmentLimits";

// parity-m5-composer.md §G: max 8 attachments, max 8 MiB per file, the
// 8-count cap cumulative across the whole composer session (paste + drag +
// picker share one running total). rejectionReason takes the count already
// reserved so far as an explicit input rather than reading global state,
// so it stays a pure function the ingestion loop can call once per file.

test("accepts a normal in-limits image", () => {
  expect(rejectionReason({ type: "image/png", size: 1024, name: "shot.png" }, 0)).toBeUndefined();
});

test("rejects a non-image MIME type with the bare filename", () => {
  expect(rejectionReason({ type: "text/plain", size: 10, name: "notes.txt" }, 0)).toBe("notes.txt");
});

test("rejects a non-image file with an empty name as 'unknown'", () => {
  expect(rejectionReason({ type: "text/plain", size: 10, name: "" }, 0)).toBe("unknown");
});

test("rejects the file once the 8-image cap is already reserved", () => {
  expect(rejectionReason({ type: "image/png", size: 1024, name: "ninth.png" }, MAX_ATTACHMENTS)).toBe(
    "ninth.png (maximum 8 images)",
  );
});

test("accepts the 8th image (reservedCount 7, zero-indexed) - the cap rejects only the 9th onward", () => {
  expect(rejectionReason({ type: "image/png", size: 1024, name: "eighth.png" }, MAX_ATTACHMENTS - 1)).toBeUndefined();
});

test("rejects an oversized image with the maximum 8 MB message", () => {
  expect(rejectionReason({ type: "image/png", size: MAX_ATTACHMENT_BYTES + 1, name: "big.png" }, 0)).toBe(
    "big.png (maximum 8 MB)",
  );
});

test("accepts a file exactly at the 8 MB boundary", () => {
  expect(rejectionReason({ type: "image/png", size: MAX_ATTACHMENT_BYTES, name: "exact.png" }, 0)).toBeUndefined();
});

test("count cap is checked before the size cap", () => {
  // A file that is BOTH over the count cap AND oversized reports the count
  // rejection, not the size one.
  expect(rejectionReason({ type: "image/png", size: MAX_ATTACHMENT_BYTES + 1, name: "x.png" }, MAX_ATTACHMENTS)).toBe(
    "x.png (maximum 8 images)",
  );
});

// The two checks behind rejectionReason, for a caller that makes them at
// different moments: the phone admits a picked image by type and count, and
// measures its size only after scaling it (#3166).
test("admits an image by type and count alone, whatever its size", () => {
  expect(admissionRejection({ type: "image/png" }, 0)).toBeUndefined();
  expect(admissionRejection({ type: "text/plain" }, 0)).toBe("notImage");
  expect(admissionRejection({ type: "image/png" }, MAX_ATTACHMENTS)).toBe("tooMany");
  expect(admissionRejection({ type: "text/plain" }, MAX_ATTACHMENTS)).toBe("notImage");
});

test("measures size alone, up to and including the limit", () => {
  expect(sizeRejection(MAX_ATTACHMENT_BYTES)).toBeUndefined();
  expect(sizeRejection(MAX_ATTACHMENT_BYTES + 1)).toBe("tooLarge");
});

// Composers that decode an image before sending it bound the decode with the
// source's own bytes and pixels, then scale the result and encode it. These
// shared values keep the phone and the web composer on the same bounds.
test("pins the shared source and encoding limits", () => {
  expect(MAX_SOURCE_BYTES).toBe(25 * 1024 * 1024);
  expect(MAX_SOURCE_PIXELS).toBe(8064 * 6048);
  expect(MAX_IMAGE_LONG_EDGE).toBe(2048);
  expect(IMAGE_JPEG_QUALITY).toBe(0.85);
});

test("fits a landscape image's long edge to the shared cap", () => {
  expect(fitImageDimensions(4032, 3024)).toEqual({ width: 2048, height: 1536 });
});

test("fits a portrait image's long edge to the shared cap", () => {
  expect(fitImageDimensions(3024, 4032)).toEqual({ width: 1536, height: 2048 });
});

test("fits a square image to the shared cap on both edges", () => {
  expect(fitImageDimensions(4096, 4096)).toEqual({ width: 2048, height: 2048 });
});

test("leaves an image that already fits its dimensions", () => {
  expect(fitImageDimensions(800, 600)).toEqual({ width: 800, height: 600 });
  expect(fitImageDimensions(2048, 1536)).toEqual({ width: 2048, height: 1536 });
});

test("clamps a zero-sized image to one pixel on both edges", () => {
  expect(fitImageDimensions(0, 0)).toEqual({ width: 1, height: 1 });
});

test("clamps a non-positive edge to one pixel and keeps the other edge", () => {
  expect(fitImageDimensions(-5, 100)).toEqual({ width: 1, height: 100 });
});

test("rounds fractional dimensions to whole pixels", () => {
  expect(fitImageDimensions(800.4, 600.6)).toEqual({ width: 800, height: 601 });
});

test("never scales an edge below one pixel", () => {
  expect(fitImageDimensions(10000, 1)).toEqual({ width: 2048, height: 1 });
  expect(fitImageDimensions(1, 10000)).toEqual({ width: 1, height: 2048 });
});
