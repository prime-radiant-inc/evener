import { describe, expect, test } from "vitest";
import { prefixClassSelectors, resolveComposes, unwrapGlobal } from "./resolve-composes.mjs";

// Removes every @media block (brace-counted, so a block's nested rules go with
// its gate): what remains is the CSS the browser applies at EVERY width.
function withoutMediaBlocks(cssText) {
  let out = cssText;
  for (;;) {
    const open = out.indexOf("@media");
    if (open === -1) return out;
    const brace = out.indexOf("{", open);
    let depth = 0;
    let end = -1;
    for (let i = brace; i < out.length; i++) {
      if (out[i] === "{") depth++;
      else if (out[i] === "}") {
        depth--;
        if (depth === 0) {
          end = i;
          break;
        }
      }
    }
    if (end === -1) throw new Error("unbalanced @media braces in test input");
    out = out.slice(0, open) + out.slice(end + 1);
  }
}

describe("unwrapGlobal", () => {
  test("unwraps simple element and class inners verbatim", () => {
    expect(unwrapGlobal(".a > :global(span) { x: y; }")).toBe(".a > span { x: y; }");
    expect(unwrapGlobal(".a :global(button) { x: y; }")).toBe(".a button { x: y; }");
    expect(unwrapGlobal(".a :global(.foo) { x: y; }")).toBe(".a .foo { x: y; }");
  });

  test("leaves CSS without :global untouched", () => {
    expect(unwrapGlobal(".a { x: y; }")).toBe(".a { x: y; }");
  });

  test("throws on nested parens instead of passing them through silently", () => {
    // The regex cannot cross the inner parens, so without the leftover
    // check the :global( text would survive into the harness stylesheet,
    // where the browser drops the whole selector and the guard passes
    // without testing the rule.
    expect(() => unwrapGlobal(".a :global(:not(.x)) { x: y; }")).toThrow(/:global/);
  });
});

describe("prefixClassSelectors", () => {
  test("prefixes every class selector in a rule list, including compound and gated selectors", () => {
    const out = prefixClassSelectors(
      ".summary, details[open] > .summary:hover { color: red; }\n@media (max-width: 899px) { .body { margin-top: 4px; } }",
      "d-",
    );
    expect(out).toContain(".d-summary, details[open] > .d-summary:hover");
    expect(out).toMatch(/@media \(max-width: 899px\) \{\s*\.d-body\s*\{/);
    expect(out).toContain("margin-top: 4px");
  });

  test("leaves :global() inners and declaration values alone", () => {
    const out = prefixClassSelectors('.a > :global(.kept) { content: ".body"; }', "d-");
    expect(out).toContain(".d-a > :global(.kept)");
    expect(out).toContain('content: ".body"');
  });

  test("prefixes same-file composes targets and leaves cross-file ones", () => {
    const out = prefixClassSelectors('.a { composes: b, c; }\n.d { composes: e from "./x.module.css"; }', "d-");
    expect(out).toContain("composes: d-b, d-c;");
    expect(out).toContain('composes: e from "./x.module.css";');
  });
});

describe("resolveComposes", () => {
  test("resolves a same-name composes import without a cycle, consumer declarations kept", () => {
    // popover.module.css's .panel composes floatingSurface.module.css's
    // .panel: the consumer extends the same-named class from another
    // module. The consumer is not a target of itself.
    const out = resolveComposes([
      ".panel { background: blue; }",
      '.panel { composes: panel from "./floatingSurface.module.css"; color: red; }',
    ]);
    expect(out).toMatch(/\.panel\s*\{[^}]*background:\s*blue[^}]*color:\s*red/);
    expect(out).not.toContain("composes");
  });

  test("resolves a composes chain transitively, the way the build merges class lists", () => {
    // .sidebarMobile composes .sidebar, which itself composes .docked from
    // another module: postcss-modules merges the whole class list, so the
    // mobile sidebar paints the docked ground. A single pass leaves the
    // second hop as a literal composes line the browser drops.
    const out = resolveComposes([
      ".docked { background: blue; position: relative; }",
      '.sidebar { composes: docked from "./surface.module.css"; color: red; }\n.sidebarMobile { composes: sidebar; }',
    ]);
    expect(out).toMatch(/\.sidebarMobile\s*\{[^}]*background:\s*blue/);
    expect(out).toMatch(/\.sidebarMobile\s*\{[^}]*color:\s*red/);
    expect(out).not.toContain("composes");
  });

  test("unwraps :global while still resolving composes", () => {
    const out = resolveComposes([
      ".base { color: red; }",
      '.a { composes: base from "./b.module.css"; }\n.b > :global(span) { x: y; }',
    ]);
    expect(out).toContain(".b > span");
    expect(out).not.toContain(":global(");
  });

  test("splices an ungated composed body in place of the composes line", () => {
    const out = resolveComposes([".base { color: red; }", '.a { composes: base from "./b.module.css"; }']);
    expect(out).toMatch(/\.a\s*\{[^}]*color:\s*red/);
  });

  test("splices a zero-specificity :where(.base) rule and leaves the wrapper in place", () => {
    // `:where(.panel)` keeps the class a composes line names; the wrapper
    // only lowers its specificity. The harness element must still receive
    // the declarations, and the wrapper itself stays as the sheet wrote it.
    const out = resolveComposes([":where(.base) { color: red; }", '.a { composes: base from "./b.module.css"; }']);
    expect(out).toContain(":where(.base)");
    expect(out).toMatch(/\.a\s*\{[^}]*color:\s*red/);
  });

  test("keeps a gated consumer's own gate when it composes an ungated body", () => {
    // Composing INTO a rule that already sits inside a media block splices in
    // place, so the composed declarations stay inside the consumer's gate.
    const out = resolveComposes([
      ".base { color: red; }",
      '@media (min-width: 700px) {\n  .a {\n    composes: base from "./b.module.css";\n  }\n}',
    ]);
    expect(withoutMediaBlocks(out)).not.toMatch(/\.a\s*\{[^}]*color:\s*red/);
    expect(out).toMatch(/@media \(min-width: 700px\)\s*\{[\s\S]*?\.a\s*\{[^}]*color:\s*red/);
  });

  test("re-targets a media-gated composed body onto the consumer instead of splicing it ungated", () => {
    // The build step composes by MERGING CLASSNAMES: an element composing
    // .gutterPull keeps that class, and the @media gate around .gutterPull's
    // own rule keeps applying to it in the built app. Splicing the gated BODY
    // into the ungated consumer drops the gate - which would pull a rail icon
    // out of the pane at phone widths in a layoutguard harness. The resolved
    // form must re-state the gate around the consumer's own selector instead.
    const out = resolveComposes([
      "@media (min-width: 700px) {\n  .gutterPull {\n    margin-left: calc(-1 * var(--speaker-gutter));\n  }\n}",
      '.a {\n  composes: gutterPull from "./seat.module.css";\n  color: blue;\n}',
    ]);
    expect(out).toMatch(
      /@media \(min-width: 700px\)\s*\{[\s\S]*?\.a\s*\{[^}]*margin-left:\s*calc\(-1 \* var\(--speaker-gutter\)\)/,
    );
    expect(withoutMediaBlocks(out)).not.toMatch(/\.a\s*\{[^}]*margin-left:\s*calc\(-1/);
  });

  test("refuses a comma-list composes line, mirroring the build's from-binding", () => {
    // postcss-modules binds the `from` clause to the LAST comma entry only:
    // `composes: seat, gutterPull from "x"` imports gutterPull from x and
    // looks seat up as a LOCAL class - the vite build fails with
    // 'referenced class name "seat" in composes not found'. A resolver that
    // read the list as one source would measure geometry the app can never
    // ship, so it refuses the form loudly instead. Sources compose one class
    // per composes line (the form the build accepts; biome's duplicate-
    // property rule demands a suppression comment on the second line).
    expect(() =>
      resolveComposes([
        ".seat { color: red; }",
        "@media (min-width: 700px) {\n  .gutterPull {\n    margin-left: -34px;\n  }\n}",
        '.a {\n  composes: seat, gutterPull from "./railseat.module.css";\n  display: block;\n}',
      ]),
    ).toThrow(/from.*last comma entry|comma/);
  });

  test("refuses a gated consumer composing a gated body rather than mis-nesting the gates", () => {
    // Re-targeting inside the consumer's own media block would AND the two
    // gates; in the built app the composed class's gate is independent of the
    // consumer's. No current source needs the combination, so refuse loudly
    // instead of guessing.
    expect(() =>
      resolveComposes([
        "@media (min-width: 700px) {\n  .gutterPull {\n    margin-left: -1px;\n  }\n}",
        '@media (prefers-reduced-motion: no-preference) {\n  .a {\n    composes: gutterPull from "./seat.module.css";\n  }\n}',
      ]),
    ).toThrow(/gated/);
  });

  test("comment prose mentioning @media cannot poison the gate scan", () => {
    // holdhints.module.css's comment says "The @media rule is..." directly
    // before its real media block, and welcome.module.css mentions
    // "@media min-height" in prose (roborev). A raw-text position scan pairs
    // the comment's mention with the next `{` - swallowing the real opener
    // into a gate string of comment prose, which re-targets garbage or
    // throws - so comments are stripped before the scan runs.
    const out = resolveComposes([
      "/* The @media rule is the first-paint source of truth (the house pattern). */\n@media (prefers-reduced-motion: no-preference) {\n  .spin {\n    animation: x 1s;\n  }\n}",
      '.a {\n  composes: spin from "./x.module.css";\n  color: red;\n}',
    ]);
    expect(out).toMatch(/@media \(prefers-reduced-motion: no-preference\)\s*\{[\s\S]*?\.a\s*\{[^}]*animation:\s*x 1s/);
    expect(out).not.toContain("first-paint");
  });

  test("names the missing source when a composed rule is not among the cssFiles", () => {
    expect(() => resolveComposes(['.a { composes: missing from "./x.module.css"; }'])).toThrow(/cssFiles/);
  });

  test("composing a class that only styles pseudo-elements or descendants adds nothing to the consumer's box", () => {
    const out = resolveComposes([
      '.host { color: red; composes: tail from "./m.module.css"; }',
      ".tail > div > :last-child::after { content: ''; }",
    ]);
    expect(out).toContain("color: red");
    expect(out).not.toContain("composes");
  });
});
