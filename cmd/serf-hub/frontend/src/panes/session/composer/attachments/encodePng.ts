// Image re-encoding + base64 conversion (parity-m5-composer.md §G, contracts
// §Attachments): every accepted image - including already-PNG input - is
// re-encoded to PNG via an offscreen <canvas> round-trip, which strips
// color profiles/EXIF. Matches the TUI's "always re-encode pasted clipboard
// image data" rule (ported from composer-attachments.js's reencodeToPng).
//
// Unlike the legacy composer (which keeps ArrayBuffer at this layer and
// defers base64 to the submit/fetch boundary, specifically to dodge a 33%
// memory blow-up during composition), this returns base64 directly:
// stores/threads.ts's InputAttachment already requires base64 (`data:
// string`), so producing it here removes a whole second conversion step at
// submit time for a one-composer-at-a-time, <=8-image-cap workload where
// that blow-up is not a real concern (worst case ~64MB raw -> ~85MB
// base64, fine for a single browser tab).
export interface EncodedPng {
  data: string; // base64-encoded PNG bytes, no "data:image/png;base64," prefix
  width: number;
  height: number;
}

// arrayBufferToBase64 is a standard browser-safe (no Node Buffer) ArrayBuffer
// -> base64 conversion: chunked through String.fromCharCode to avoid a
// call-stack blowup from spreading a huge byte array in one call.
const CHUNK_SIZE = 0x8000;

export function arrayBufferToBase64(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf);
  let binary = "";
  for (let i = 0; i < bytes.length; i += CHUNK_SIZE) {
    binary += String.fromCharCode(...bytes.subarray(i, i + CHUNK_SIZE));
  }
  return btoa(binary);
}

// reencodeToPng decodes `blob` into an <img>, draws it onto a canvas sized
// to its natural dimensions, and re-encodes that canvas to PNG - the same
// pipeline for every input type (a JPEG gets converted; a PNG still gets
// round-tripped, which is what strips its color profile/EXIF). Resolves
// with the re-encoded bytes (base64) and the decoded width/height (for chip
// display - neither the wire's InputItem nor stores/threads.ts's
// InputAttachment carries dimensions, so this is UI-layer-only metadata the
// caller keeps separately from what it hands to the store).
export function reencodeToPng(blob: Blob): Promise<EncodedPng> {
  return new Promise((resolve, reject) => {
    const url = URL.createObjectURL(blob);
    const img = new Image();
    img.onload = () => {
      const canvas = document.createElement("canvas");
      canvas.width = img.width || 1;
      canvas.height = img.height || 1;
      const ctx = canvas.getContext("2d");
      if (ctx) ctx.drawImage(img, 0, 0);
      canvas.toBlob((out) => {
        URL.revokeObjectURL(url);
        if (!out) {
          reject(new Error("canvas.toBlob returned null while re-encoding to PNG"));
          return;
        }
        out
          .arrayBuffer()
          .then((buf) => {
            resolve({ data: arrayBufferToBase64(buf), width: canvas.width, height: canvas.height });
          })
          .catch(reject);
      }, "image/png");
    };
    img.onerror = () => {
      URL.revokeObjectURL(url);
      reject(new Error("image decode failed"));
    };
    img.src = url;
  });
}
