// Shared CSS block extraction for the off-disk style-contract tests
// (hovercard.style.test.ts, entityref.style.test.ts, and token-contract.test.
// ts's block scans). These tests read *.module.css straight from disk because
// vitest leaves CSS Modules unprocessed (test.css defaults to false), so no
// rendered-DOM assert can ever see the declarations; the extraction helpers
// they share live here for the same reason node-fs-shim.d.ts does - test
// support inside src/styles.

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

/** One .module.css (or other CSS) file, read straight off disk next to the
 * calling test file. `moduleUrl` is always `import.meta.url` from the caller;
 * passing it in (rather than importing.meta from here) keeps the resolution
 * relative to whoever owns the file being read. */
export function readModuleCss(moduleUrl: string, fileName: string): string {
  return readFileSync(join(dirname(fileURLToPath(moduleUrl)), fileName), "utf8");
}

/** The text of one balanced-brace block, given the index of its opening `{`.
 * The one walk every caller shares: find the start yourself (each caller has
 * its own anchoring rule), then let this count depth to the matching `}`. */
export function blockBody(css: string, openBraceIndex: number): string {
  let depth = 0;
  for (let i = openBraceIndex; i < css.length; i++) {
    if (css[i] === "{") depth += 1;
    else if (css[i] === "}") {
      depth -= 1;
      if (depth === 0) return css.slice(openBraceIndex + 1, i);
    }
  }
  throw new Error("cssBlock: unterminated block");
}

function escapeRegExp(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/** The block of the first top-level rule for `selector`, matched literally:
 * callers pass the plain CSS selector text (`.status[data-state="failed"]`),
 * never a hand-escaped regex. The column-0 anchor keeps an indented selector
 * nested inside a later @media from being picked up by mistake. */
export function topRuleBlock(css: string, selector: string): string {
  const match = new RegExp(`(^|\\n)${escapeRegExp(selector)}\\s*\\{`).exec(css);
  if (!match) throw new Error(`cssBlock: no top-level rule for ${selector}`);
  return blockBody(css, match.index + match[0].length - 1);
}

/** The block one media condition owns: the nested rules between its braces. */
export function mediaBlock(css: string, condition: string): string {
  const match = new RegExp(`@media \\(${escapeRegExp(condition)}\\)\\s*\\{`).exec(css);
  if (!match) throw new Error(`cssBlock: no @media (${condition}) block`);
  return blockBody(css, match.index + match[0].length - 1);
}
