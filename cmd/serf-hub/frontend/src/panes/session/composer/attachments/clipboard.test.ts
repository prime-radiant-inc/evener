import { expect, test } from "vitest";
import { imageFilesFromClipboard } from "./clipboard";

function makeItem(kind: string, type: string, file: File | null): DataTransferItem {
  return { kind, type, getAsFile: () => file } as unknown as DataTransferItem;
}

test("returns an empty array for null clipboardData", () => {
  expect(imageFilesFromClipboard(null)).toEqual([]);
});

test("extracts a single pasted image file", () => {
  const file = new File(["x"], "shot.png", { type: "image/png" });
  const clipboardData = { items: [makeItem("file", "image/png", file)] } as unknown as DataTransfer;
  expect(imageFilesFromClipboard(clipboardData)).toEqual([file]);
});

test("a text-only paste yields no files", () => {
  const clipboardData = { items: [makeItem("string", "text/plain", null)] } as unknown as DataTransfer;
  expect(imageFilesFromClipboard(clipboardData)).toEqual([]);
});

test("a mixed image + text paste extracts only the image", () => {
  const file = new File(["x"], "mix.png", { type: "image/png" });
  const clipboardData = {
    items: [makeItem("string", "text/plain", null), makeItem("file", "image/png", file)],
  } as unknown as DataTransfer;
  expect(imageFilesFromClipboard(clipboardData)).toEqual([file]);
});

test("a non-image file item (e.g. a pasted document) is not treated as an image attachment", () => {
  const file = new File(["x"], "doc.pdf", { type: "application/pdf" });
  const clipboardData = { items: [makeItem("file", "application/pdf", file)] } as unknown as DataTransfer;
  expect(imageFilesFromClipboard(clipboardData)).toEqual([]);
});

test("multiple pasted images are all extracted, in order", () => {
  const a = new File(["x"], "a.png", { type: "image/png" });
  const b = new File(["x"], "b.png", { type: "image/png" });
  const clipboardData = {
    items: [makeItem("file", "image/png", a), makeItem("file", "image/png", b)],
  } as unknown as DataTransfer;
  expect(imageFilesFromClipboard(clipboardData)).toEqual([a, b]);
});

test("a file-kind item whose getAsFile() returns null is skipped without throwing", () => {
  const clipboardData = { items: [makeItem("file", "image/png", null)] } as unknown as DataTransfer;
  expect(imageFilesFromClipboard(clipboardData)).toEqual([]);
});
