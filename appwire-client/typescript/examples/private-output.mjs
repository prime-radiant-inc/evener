import assert from "node:assert/strict";
import { open } from "node:fs/promises";
import { isAbsolute } from "node:path";

// Reserve private output before connecting; never remove a replaced pathname.
export async function withPrivateOutput(outputPath, operation) {
  assert.ok(outputPath === undefined || isAbsolute(outputPath), "Use an absolute private output path.");
  let output;
  try {
    if (outputPath !== undefined) {
      output = await open(outputPath, "wx", 0o600);
    }
    const result = await operation(output);
    return result;
  } finally {
    await output?.close();
  }
}
