// @vitest-environment node

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";
import { MOBILE_QUERY } from "../shell/useIsMobile";
import { escapeRegExp } from "./cssBlock";

const STYLES_DIR = dirname(fileURLToPath(import.meta.url));
const GLOBAL_CSS = readFileSync(join(STYLES_DIR, "global.css"), "utf8");

function removeMobileMediaBlock(source: string): string {
  const start = source.indexOf(`@media ${MOBILE_QUERY} {`);
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
  const mobile = GLOBAL_CSS.match(new RegExp(`@media ${escapeRegExp(MOBILE_QUERY)} \\{([\\s\\S]*?)\\n\\}`));
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

test("the root spends touch-action: manipulation so a double-tap never zooms", () => {
  // Safari's double-tap zoom-to-fit makes a tap-heavy app feel like it zooms
  // in and out. manipulation removes only that: panning and pinch zoom stay
  // (the viewport meta never locks zoom - WCAG 1.4.4, viewport-pin.test.ts).
  // Declared on the top-level body rule, ungated, because a touch screen is
  // not bounded by the phone query: the iPhone Duo's 951pt inner display in
  // landscape renders the desktop layout and double-taps there zoom too.
  // Component drag gestures (sheet, rail resize, mermaid pan) declare
  // touch-action: none on their own elements; the intersection with the root
  // keeps those exactly as they are.
  const bodyRule = /\nbody \{([^}]*)\}/.exec(GLOBAL_CSS);
  expect(bodyRule, "global.css must have a top-level body rule").not.toBeNull();
  expect(bodyRule![1]).toContain("touch-action: manipulation");
  // It must not migrate into the phone gate: the rule is for every touch
  // pointer, not only phone widths.
  const mobile = GLOBAL_CSS.match(new RegExp(`@media ${escapeRegExp(MOBILE_QUERY)} \\{([\\s\\S]*?)\\n\\}`));
  expect(mobile).not.toBeNull();
  expect(mobile![1]).not.toContain("touch-action");
});
