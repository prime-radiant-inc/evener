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

test("touch screens pan but never zoom this app", () => {
  // The app is app-like on every touch device (Jesse 2026-11-15): no
  // double-tap zoom-to-fit, no pinch zoom - matching the native app, which
  // has no zoom on any display. pan-x pan-y keeps scrolling and spends both
  // zoom gestures; the (any-pointer: coarse) query reaches every touch
  // screen at any width, including the iPhone Duo's 951pt inner display in
  // landscape and touch laptops. Desktop zoom is untouched by construction:
  // touch-action governs touch input only, and a window with no touch screen
  // never matches the coarse query. Component drag gestures (sheet, rail
  // resize, mermaid pan) declare touch-action: none on their own elements;
  // the intersection keeps those exactly as they are.
  const coarse = /@media \(any-pointer: coarse\) \{([\s\S]*?)\n\}/.exec(GLOBAL_CSS);
  expect(coarse, "global.css must have an (any-pointer: coarse) media block").not.toBeNull();
  const bodyRule = /\bbody \{([^}]*)\}/.exec(coarse![1]!);
  expect(bodyRule, "the coarse block must style body").not.toBeNull();
  expect(bodyRule![1]).toContain("touch-action: pan-x pan-y");
  // The rule lives only under the coarse gate: a top-level declaration is
  // redundant (touch input implies a coarse pointer), and a phone-gated one
  // would miss wide touch screens.
  const topLevelBody = /\nbody \{([^}]*)\}/.exec(GLOBAL_CSS);
  expect(topLevelBody, "global.css must have a top-level body rule").not.toBeNull();
  expect(topLevelBody![1]).not.toContain("touch-action");
  const mobile = GLOBAL_CSS.match(new RegExp(`@media ${escapeRegExp(MOBILE_QUERY)} \\{([\\s\\S]*?)\\n\\}`));
  expect(mobile).not.toBeNull();
  expect(mobile![1]).not.toContain("touch-action");
});
