export function countUtf8Bytes(text: string): number {
  const CHUNK_CODE_UNITS = 4_096;
  const chunk = new Uint8Array(3 * CHUNK_CODE_UNITS);
  const encoder = new TextEncoder();
  let total = 0;
  let offset = 0;
  while (offset < text.length) {
    let end = Math.min(offset + CHUNK_CODE_UNITS, text.length);
    const boundary = text.charCodeAt(end - 1);
    if (end < text.length && boundary >= 0xd800 && boundary <= 0xdbff) end -= 1;
    const { read, written } = encoder.encodeInto(text.slice(offset, end), chunk);
    total += written;
    offset += read;
  }
  return total;
}

export function encodedPayloadBytes(value: unknown): number {
  try {
    const encoded = JSON.stringify(value);
    return encoded === undefined ? Number.POSITIVE_INFINITY : countUtf8Bytes(encoded);
  } catch {
    return Number.POSITIVE_INFINITY;
  }
}

export class TranscriptPayloadBudget {
  #used = 0;

  constructor(readonly limit: number) {
    if (!Number.isSafeInteger(limit) || limit < 0) throw new RangeError("budget limit must be non-negative");
  }

  get used(): number {
    return this.#used;
  }

  reserve(bytes: number): boolean {
    if (!Number.isSafeInteger(bytes) || bytes < 0 || this.#used + bytes > this.limit) return false;
    this.#used += bytes;
    return true;
  }

  release(bytes: number): void {
    if (!Number.isSafeInteger(bytes) || bytes < 0 || bytes > this.#used) {
      throw new RangeError("invalid budget release");
    }
    this.#used -= bytes;
  }
}
