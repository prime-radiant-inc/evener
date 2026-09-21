// Shared CSS block extraction for the off-disk style-contract tests
// (hovercard.style.test.ts, entityref.style.test.ts). These tests read
// *.module.css straight from disk because vitest leaves CSS Modules
// unprocessed (test.css defaults to false), so no rendered-DOM assert can
// ever see the declarations; the extraction helpers they share live here for
// the same reason node-fs-shim.d.ts does - test support inside src/styles.

// Brace-depth extraction, not a CSS parser dependency: the modules these
// tests read never nest braces inside a rule (the same shape
// token-contract.test.ts's own extractBlock relies on). The column-0 anchor
// keeps an indented selector nested inside a later @media from being picked up
// by mistake.
export function topRuleBlock(css: string, selector: string): string {
  const match = new RegExp(`(^|\\n)${selector}\\s*\\{`).exec(css);
  if (!match) throw new Error(`cssBlock: no top-level rule for ${selector}`);
  const open = match.index + match[0].length - 1;
  let depth = 0;
  for (let i = open; i < css.length; i++) {
    if (css[i] === "{") depth += 1;
    else if (css[i] === "}") {
      depth -= 1;
      if (depth === 0) return css.slice(open + 1, i);
    }
  }
  throw new Error(`cssBlock: unterminated rule for ${selector}`);
}

// The block one media condition owns, with the same brace-depth walk.
export function mediaBlock(css: string, condition: string): string {
  const match = new RegExp(`@media \\(${condition}\\)\\s*\\{`).exec(css);
  if (!match) throw new Error(`cssBlock: no @media (${condition}) block`);
  const open = match.index + match[0].length - 1;
  let depth = 0;
  for (let i = open; i < css.length; i++) {
    if (css[i] === "{") depth += 1;
    else if (css[i] === "}") {
      depth -= 1;
      if (depth === 0) return css.slice(open + 1, i);
    }
  }
  throw new Error(`cssBlock: unterminated @media (${condition}) block`);
}
