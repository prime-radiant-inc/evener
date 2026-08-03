// @vitest-environment node

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const STYLES_DIR = dirname(fileURLToPath(import.meta.url));
const GLOBAL_CSS = readFileSync(join(STYLES_DIR, "global.css"), "utf8");

function removeMobileMediaBlock(source: string): string {
  const start = source.indexOf("@media (max-width: 899px) {");
  expect(start).toBeGreaterThanOrEqual(0);

  let depth = 0;
  for (let index = start; index < source.length; index += 1) {
    const char = source[index]!;
    if (char === "{") {
      depth += 1;
    } else if (char === "}") {
      depth -= 1;
      if (depth === 0) {
        return source.slice(0, start) + source.slice(index + 1);
      }
    }
  }

  throw new Error("mobile media block was not closed");
}

test("mobile: html suppresses root overscroll while panes retain scroll ownership", () => {
  const mobile = GLOBAL_CSS.match(/@media \(max-width: 899px\) \{([\s\S]*?)\n\}/);
  expect(mobile).not.toBeNull();

  const htmlRule = mobile![1]!.match(/html \{([^}]*)\}/);
  expect(htmlRule).not.toBeNull();
  expect(htmlRule![1]).toContain("overscroll-behavior: none");

  const documentLock = mobile![1]!.match(/html,\s*\n\s*body \{([^}]*)\}/);
  expect(documentLock).not.toBeNull();
  expect(documentLock![1]).toContain("overflow: hidden");
});

test("desktop: root overscroll containment is not declared outside the mobile query", () => {
  const withoutMobileBlock = removeMobileMediaBlock(GLOBAL_CSS);
  expect(withoutMobileBlock).not.toContain("overscroll-behavior");
});
