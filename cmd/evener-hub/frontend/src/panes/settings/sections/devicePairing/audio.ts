import { crc32, MAX_TRANSFER_BYTES } from "@evener/appwire-client/pairing";

export const SAMPLE_RATE = 44100;
const SYMBOL_SAMPLES = 441;
const FRAGMENT_BYTES = 48;
const SYNC = [0, 1, 3, 2, 1, 0, 2, 3];
const FREQUENCIES = [1500, 2100, 2700, 3300];

function codeword(byte: number): number {
  let code = 0,
    bit = 0,
    parity = 0;
  for (let i = 1; i <= 12; i++) {
    if ([1, 2, 4, 8].includes(i)) continue;
    if ((byte >> bit++) & 1) {
      code |= 1 << (i - 1);
      parity ^= i;
    }
  }
  for (const p of [1, 2, 4, 8]) if (parity & p) code |= 1 << (p - 1);
  return code;
}

export function transferFrames(bytes: Uint8Array): Uint8Array[] {
  if (!bytes.length || bytes.length > MAX_TRANSFER_BYTES) throw new Error("Invalid setup transfer size.");
  const id = crc32(bytes);
  return Array.from({ length: Math.ceil(bytes.length / FRAGMENT_BYTES) }, (_, index) => {
    const frame = new Uint8Array(59);
    frame[0] = index;
    frame[1] = bytes.length & 255;
    frame[2] = bytes.length >> 8;
    new DataView(frame.buffer).setUint32(3, id, true);
    frame.set(bytes.subarray(index * FRAGMENT_BYTES, (index + 1) * FRAGMENT_BYTES), 7);
    new DataView(frame.buffer).setUint32(55, crc32(frame.subarray(0, 55)), true);
    return frame;
  });
}

export function encodeFrames(frames: Uint8Array[]): Float32Array {
  // Each fragment resynchronizes independently, bounding sample-clock drift.
  const frameSamples = (32 + SYNC.length + 59 * 6 + 12) * SYMBOL_SAMPLES;
  const output = new Float32Array(frames.length * frameSamples + SAMPLE_RATE / 5);
  const tones = FREQUENCIES.map((frequency) =>
    Float32Array.from({ length: SYMBOL_SAMPLES }, (_, i) => {
      const envelope = Math.min(1, i / 8, (SYMBOL_SAMPLES - 1 - i) / 8);
      return 0.65 * envelope * Math.sin((2 * Math.PI * frequency * i) / SAMPLE_RATE);
    }),
  );
  let at = SAMPLE_RATE / 10;
  const tone = (value: number) => {
    const samples = tones[value];
    if (samples) output.set(samples, at);
    at += SYMBOL_SAMPLES;
  };
  for (const frame of frames) {
    for (let i = 0; i < 16; i++) tone(0);
    for (let i = 0; i < 16; i++) tone(3);
    for (const value of SYNC) tone(value);
    for (const byte of frame) {
      const code = codeword(byte);
      for (let i = 0; i < 6; i++) tone((code >> (i * 2)) & 3);
    }
    at += 12 * SYMBOL_SAMPLES;
  }
  return output;
}

export function encodeAudio(bytes: Uint8Array): Float32Array {
  return encodeFrames(transferFrames(bytes));
}
