import assert from "node:assert/strict";
import { lstat, open, unlink } from "node:fs/promises";
import { isAbsolute } from "node:path";

// Reserve private output before connecting; never remove a replaced pathname.
export async function withPrivateOutput(outputPath, operation) {
  assert.ok(outputPath === undefined || isAbsolute(outputPath), "Use an absolute private output path.");
  let output;
  let reservation;
  let ownsOutput = false;
  try {
    if (outputPath !== undefined) {
      output = await open(outputPath, "wx", 0o600);
      reservation = await output.stat();
      ownsOutput = true;
    }
    const result = await operation(output);
    ownsOutput = false;
    return result;
  } finally {
    await output?.close();
    if (ownsOutput && outputPath !== undefined) {
      const current = await lstat(outputPath).catch(() => undefined);
      if (current && reservation && current.dev === reservation.dev && current.ino === reservation.ino)
        await unlink(outputPath).catch(() => {});
    }
  }
}
