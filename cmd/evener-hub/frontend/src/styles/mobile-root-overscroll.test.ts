// @vitest-environment node

import { expect, test } from "vitest";
import { MOBILE_QUERY } from "../shell/useIsMobile";
import { blockBody, mediaBlock, readModuleCss } from "./cssBlock";

// readModuleCss also strips comments, so a declaration quoted in a doc
// comment can never satisfy - or fail - an assertion about a rule.
const GLOBAL_CSS = readModuleCss(import.meta.url, "global.css");

// The document lock has two halves with different reach (issue #6471).
// The GESTURE half - pull-to-refresh, page bounce, root scroll chaining -
// belongs to every touching surface, so it carries the coarse arm. The LAYOUT
// half - html/body overflow: hidden - stays phone-only, because only the
// phone's fixed shell gives the document nothing to scroll. Both predicates
// are pinned here because widening either one silently changes which surfaces
// the app treats as gripping the document.
const GESTURE_LOCK_QUERY = `${MOBILE_QUERY}, (any-pointer: coarse)`;

/** The body of the first `@media <prelude> {` block whose prelude is exactly
 * `prelude` - the combined query has no `(any-pointer: coarse)` wrapper for
 * cssBlock's mediaBlock() to key on. */
function blockFor(source: string, prelude: string): string {
  const open = source.indexOf("{", source.indexOf(`@media ${prelude} {`));
  expect(open, `global.css must have an @media ${prelude} block`).toBeGreaterThanOrEqual(0);
  return blockBody(source, open);
}

function removeMediaBlock(source: string, mediaQuery: string): string {
  const start = source.indexOf(`@media ${mediaQuery} {`);
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

  throw new Error("media block was not closed");
}

test("the document gesture lock reaches phones and every touch screen, and nothing else", () => {
  const gesture = blockFor(GLOBAL_CSS, GESTURE_LOCK_QUERY);
  expect(gesture).toMatch(/\bhtml \{[^}]*overscroll-behavior: none;/);
  // That block alone may declare the property: with it removed, no other
  // rule - top-level, the phone gate, or the coarse touch-action gate - owns
  // overscroll containment.
  expect(removeMediaBlock(GLOBAL_CSS, GESTURE_LOCK_QUERY)).not.toContain("overscroll-behavior");
});

test("the phone layout keeps the document overflow lock", () => {
  const phone = mediaBlock(GLOBAL_CSS, "max-width: 899px");
  const documentLock = phone.match(/html,\s*\n\s*body \{([^}]*)\}/);
  expect(documentLock).not.toBeNull();
  expect(documentLock![1]).toContain("overflow: hidden");
});

test("the document overflow lock stays phone-only, so a wide touch screen keeps document scrolling", () => {
  // Issue #6471's other half: the gesture lock reaches a wide touch screen,
  // but the layout lock must not. overflow: hidden is only safe behind the
  // phone's fixed shell; a desktop layout at a touch width still scrolls its
  // document, so widening this would break it.
  const gesture = blockFor(GLOBAL_CSS, GESTURE_LOCK_QUERY);
  expect(gesture).not.toContain("overflow");
  const withoutPhoneBlock = removeMediaBlock(GLOBAL_CSS, MOBILE_QUERY);
  expect(withoutPhoneBlock).not.toMatch(/(?:^|\n)\s*(?:html|body)\s*\{[^}]*overflow:\s*hidden/);
});

test("touch screens pan but never zoom this app", () => {
  // The 2026-11-15 decision (Jesse): every touch screen pans and never zooms
  // the app, matching the native app, which has no zoom on any display.
  // global.css's coarse-gate comment owns the full rationale.
  const coarse = mediaBlock(GLOBAL_CSS, "any-pointer: coarse");
  expect(coarse).toMatch(/\bbody \{[^}]*touch-action: pan-x pan-y;/);
  // That block alone may declare touch-action: with it removed, no other
  // rule - top-level, the phone gate, or any later gate - owns the property.
  expect(removeMediaBlock(GLOBAL_CSS, "(any-pointer: coarse)")).not.toContain("touch-action");
});
