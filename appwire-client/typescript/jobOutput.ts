import { WireError } from "./errors";
import type { JobOutputPage } from "./types.gen";

const MAX_PAGE_BYTES = 65536;
const BASE64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

export interface DecodedJobOutputPage extends JobOutputPage {
  bytes: Uint8Array;
}

function isByteOffset(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

// Allocate only the validated byte count, never the untrusted encoded length.
function encodeUTF8(data: string, count: number): Uint8Array | null {
  if (data.length > MAX_PAGE_BYTES) return null;
  const bytes = new Uint8Array(count);
  let offset = 0;
  for (const scalar of data) {
    const point = scalar.codePointAt(0);
    if (point === undefined || (point >= 0xd800 && point <= 0xdfff)) return null;
    const width = point < 0x80 ? 1 : point < 0x800 ? 2 : point < 0x10000 ? 3 : 4;
    if (width > count - offset) return null;
    if (width === 1) {
      bytes[offset++] = point;
    } else {
      bytes[offset++] = (width === 2 ? 0xc0 : width === 3 ? 0xe0 : 0xf0) | (point >> (6 * (width - 1)));
      for (let shift = 6 * (width - 2); shift >= 0; shift -= 6) bytes[offset++] = 0x80 | ((point >> shift) & 0x3f);
    }
  }
  return offset === count ? bytes : null;
}

function decodeBase64(data: string, count: number): Uint8Array | null {
  if (data.length !== 4 * Math.ceil(count / 3)) return null;
  const bytes = new Uint8Array(count);
  for (let i = 0, offset = 0; i < data.length; i += 4) {
    const a = BASE64.indexOf(data.charAt(i));
    const b = BASE64.indexOf(data.charAt(i + 1));
    const remaining = Math.min(3, count - offset);
    const c = remaining > 1 ? BASE64.indexOf(data.charAt(i + 2)) : 0;
    const d = remaining > 2 ? BASE64.indexOf(data.charAt(i + 3)) : 0;
    if (a < 0 || b < 0 || c < 0 || d < 0) return null;
    if (remaining === 1 && (data.slice(i + 2, i + 4) !== "==" || (b & 15) !== 0)) return null;
    if (remaining === 2 && (data.charAt(i + 3) !== "=" || (c & 3) !== 0)) return null;
    bytes[offset++] = (a << 2) | (b >> 4);
    if (remaining > 1) bytes[offset++] = (b << 4) | (c >> 2);
    if (remaining > 2) bytes[offset++] = (c << 6) | d;
  }
  return bytes;
}

// Validate wire bounds and bytes together before any consumer admits a page.
export function parseJobOutputPage(data: unknown): DecodedJobOutputPage | null {
  if (typeof data !== "object" || data === null || Array.isArray(data)) return null;
  const raw = data as Record<string, unknown>;
  const { offsetBytes, bytesReturned, totalBytes, retainedStartBytes, encoding } = raw;
  if (
    !isByteOffset(offsetBytes) ||
    !isByteOffset(bytesReturned) ||
    !isByteOffset(totalBytes) ||
    !isByteOffset(retainedStartBytes)
  )
    return null;
  if (
    retainedStartBytes > offsetBytes ||
    offsetBytes > totalBytes ||
    bytesReturned > totalBytes - offsetBytes ||
    bytesReturned > MAX_PAGE_BYTES
  )
    return null;
  if (typeof raw.data !== "string") return null;
  if (encoding !== "utf8" && encoding !== "base64") return null;
  const bytes = encoding === "utf8" ? encodeUTF8(raw.data, bytesReturned) : decodeBase64(raw.data, bytesReturned);
  if (!bytes) return null;
  return { offsetBytes, bytesReturned, totalBytes, retainedStartBytes, encoding, data: raw.data, bytes };
}

// Replacement characters retain the source spans they consumed. A later
// contiguous page can therefore repair an incomplete scalar without losing bytes.
export function forEachJobOutputScalar(
  bytes: Uint8Array,
  emit: (value: string, start: number, end: number) => void,
): void {
  let offset = 0;
  while (offset < bytes.length) {
    const start = offset;
    const lead = bytes[offset++];
    if (lead === undefined) break;
    if (lead < 0x80) {
      emit(String.fromCharCode(lead), start, offset);
      continue;
    }
    const width =
      lead >= 0xc2 && lead <= 0xdf ? 2 : lead >= 0xe0 && lead <= 0xef ? 3 : lead >= 0xf0 && lead <= 0xf4 ? 4 : 0;
    let point = lead & (width === 2 ? 0x1f : width === 3 ? 0x0f : 0x07);
    for (let index = 1; index < width; index++) {
      const next = bytes[offset];
      if (next === undefined || next < 0x80 || next > 0xbf) break;
      if (
        index === 1 &&
        ((lead === 0xe0 && next < 0xa0) ||
          (lead === 0xed && next > 0x9f) ||
          (lead === 0xf0 && next < 0x90) ||
          (lead === 0xf4 && next > 0x8f))
      )
        break;
      point = (point << 6) | (next & 0x3f);
      offset++;
    }
    emit(width > 0 && offset - start === width ? String.fromCodePoint(point) : "\ufffd", start, offset);
  }
}

export function decodeJobOutputText(bytes: Uint8Array): string {
  const scalars: string[] = [];
  forEachJobOutputScalar(bytes, (value) => scalars.push(value));
  return scalars.join("");
}

export function jobOutputPrunedBounds(error: unknown): { retainedStartBytes: number; totalBytes: number } | null {
  if (!(error instanceof WireError) || error.code !== -32014 || error.evenerErrorInfo !== "jobOutputPruned")
    return null;
  if (typeof error.data !== "object" || error.data === null || Array.isArray(error.data)) return null;
  const { retainedStartBytes, totalBytes } = error.data as Record<string, unknown>;
  if (!isByteOffset(retainedStartBytes) || !isByteOffset(totalBytes) || retainedStartBytes > totalBytes) return null;
  return { retainedStartBytes, totalBytes };
}
