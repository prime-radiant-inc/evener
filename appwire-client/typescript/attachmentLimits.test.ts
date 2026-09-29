// @vitest-environment node
import { expect, test } from "vitest";
import {
  admissionRejection,
  MAX_ATTACHMENT_BYTES,
  MAX_ATTACHMENTS,
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
