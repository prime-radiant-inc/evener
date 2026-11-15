import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, realpathSync, rmSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { expect, test } from "vitest";
import { createConsumerDir } from "./consumer-fixture.mjs";

test("the consumer keeps its own npm prefix under an ancestor node_modules", () => {
  const ancestor = mkdtempSync(path.join(os.tmpdir(), "evener-appwire-ancestor-"));
  try {
    mkdirSync(path.join(ancestor, "node_modules"), { recursive: true });

    const consumerDir = createConsumerDir(ancestor);
    try {
      const prefix = execFileSync("npm", ["prefix"], { cwd: consumerDir, encoding: "utf8" }).trim();
      expect(realpathSync(prefix)).toBe(realpathSync(consumerDir));
    } finally {
      rmSync(consumerDir, { recursive: true, force: true });
    }
  } finally {
    rmSync(ancestor, { recursive: true, force: true });
  }
});
