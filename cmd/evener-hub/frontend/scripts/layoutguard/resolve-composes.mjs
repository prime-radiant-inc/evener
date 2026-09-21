// Minimal CSS-Modules `composes:` resolver, plus `:global()` unwrapping.
//
// Vite/postcss-modules resolves `composes: X from "./other.module.css"` at
// BUILD time by merging classnames. A layoutguard case loads .module.css
// files directly as plain <link rel="stylesheet"> tags with NO build step,
// so `composes` (not a real CSS property) is silently dropped by the browser
// and the composed rules never apply. For widgets/internal/fieldTrigger
// .module.css (composed into the path-field and model-field triggers) that
// silently drops white-space:nowrap - which is exactly the property kata
// p6g8's bug and fix depend on. A harness that skips this resolution step
// tests nothing and will not react to the mutation it exists to catch.
//
// The same build-step gap applies to `:global(...)`: in production it means
// "don't hash this part" (used to pierce into a child component's own
// classes, e.g. spawn.module.css constraining ModelSwitchTrigger's nested
// spans). A browser parsing raw CSS treats `:global()` as an unknown
// pseudo-class and drops the WHOLE selector, so the rule silently never
// applies in the harness. Unwrapping `:global(<inner>)` to `<inner>`
// mirrors the build exactly for the simple element/class inners this
// codebase uses.
//
// This does one level of inlining: for every simple `.class { ... }` rule
// across ALL provided sources, if its body contains
// `composes: a[, b] from "...";`, splice in the (already-defined) target
// rule's own declarations in place of the composes line. Good enough for the
// flat one-file-deep composes chains this codebase actually uses (verified:
// fieldTrigger.module.css itself composes nothing, so there is no chain to
// walk recursively).
//
// The one refinement correctness demands: a composes target whose rule sits
// INSIDE a media block must not have its body spliced ungated. The build step
// composes by MERGING CLASSNAMES (postcss-modules): the consuming element
// keeps the composed class, and the gate stays wrapped around the target's
// own rule, applying to the element through that class. Splicing the gated
// body into the ungated consumer drops the gate in the harness - for the
// rail seat's gutter pull that would drag a glyph out of the pane at phone
// widths, the exact escape the rail cases exist to catch. So a gated
// target's declarations are re-stated AFTER the consumer's rule, inside the
// same gate, on the consumer's own selector (the harness markup names only
// the consumer class). One combination is refused instead of resolved: a
// gated consumer composing a gated target, whose correct form would have to
// keep the two gates independent rather than AND-ed; nothing in this
// codebase needs it, and guessing would silently measure the wrong geometry.

// A plain single-class selector immediately followed by its rule body - the
// only shape `composes` is allowed to target. Deliberately does not match
// compound selectors (.trigger:hover) or grouped ones (.a, .b): requiring
// the class name to be followed immediately by optional horizontal
// whitespace then `{` rules those out.
const RULE_RE = /(\.[A-Za-z0-9_-]+)[ \t]*\{([^}]*)\}/g;
const COMPOSES_RE = /composes:\s*([^;]+?)\s+from\s+["'][^"']*["'];?/g;

// `:global(<inner>)` unwrapping: the build strips the wrapper and keeps the
// inner selector verbatim. The pattern below only matches paren-free inners;
// anything nested (e.g. `:global(:not(.x))`) matches NOTHING, so the leftover
// check after the replace throws rather than letting the browser silently
// drop the selector and the guard pass without testing the rule.
const GLOBAL_RE = /:global\(\s*([^()]+?)\s*\)/g;
const LEFTOVER_GLOBAL_RE = /:global\(/;

export function unwrapGlobal(cssText) {
  const unwrapped = cssText.replace(GLOBAL_RE, (_whole, inner) => {
    if (!/^[a-zA-Z.*#:[\]]/.test(inner)) {
      throw new Error(`unwrapGlobal: unsupported :global() inner ${JSON.stringify(inner)} - extend the pattern`);
    }
    return inner;
  });
  if (LEFTOVER_GLOBAL_RE.test(unwrapped)) {
    throw new Error("unwrapGlobal: unmatched :global( remains (nested parens?) - extend the pattern");
  }
  return unwrapped;
}

export function resolveComposes(cssTexts) {
  // Comments are stripped FIRST, before any position scan: an "@media" or
  // ":global(" mentioned in comment prose would otherwise pair with the next
  // `{` (MEDIA_OPEN_RE below) or trip unwrapGlobal's leftover check, turning
  // comment text into a malformed gate or a spurious throw - real files in
  // this tree mention both (holdhints.module.css, welcome.module.css). The
  // resolved sheet loses its comments, which the browser ignores anyway.
  const combined = cssTexts
    .map((css) => css.replace(/\/\*[\s\S]*?\*\//g, ""))
    .map(unwrapGlobal)
    .join("\n\n");

  // Every @media block, brace-counted so nested rules cannot end one early:
  // the map from a rule's position to the gate it sits under.
  const mediaBlocks = [];
  const MEDIA_OPEN_RE = /@media[^{]*\{/g;
  let m = MEDIA_OPEN_RE.exec(combined);
  while (m !== null) {
    const open = m.index + m[0].length - 1;
    let depth = 0;
    let end = -1;
    for (let i = open; i < combined.length; i++) {
      if (combined[i] === "{") depth++;
      else if (combined[i] === "}") {
        depth--;
        if (depth === 0) {
          end = i;
          break;
        }
      }
    }
    if (end === -1) {
      throw new Error("resolveComposes: unterminated @media block");
    }
    mediaBlocks.push({ gate: m[0].replace(/\{\s*$/, "").trim(), start: m.index, end });
    MEDIA_OPEN_RE.lastIndex = end + 1;
    m = MEDIA_OPEN_RE.exec(combined);
  }

  const gateAt = (index) => {
    for (const block of mediaBlocks) {
      if (index > block.start && index < block.end) return block.gate;
    }
    return null;
  };

  // Every simple rule keyed by class name, KEEPING source order and every
  // definition: a class may be defined both ungated and inside a block, and
  // composing it must carry both (the same element carries both rules in the
  // built app).
  const bodies = new Map();
  for (const m of combined.matchAll(RULE_RE)) {
    const entries = bodies.get(m[1]) ?? [];
    entries.push({ body: m[2], gate: gateAt(m.index) });
    bodies.set(m[1], entries);
  }

  return combined.replace(RULE_RE, (_whole, selector, body, offset) => {
    const consumerGate = gateAt(offset);
    // Gated composed bodies for this consumer, grouped by gate so one
    // re-stated block carries all of that gate's declarations.
    const gatedByGate = new Map();
    const resolvedBody = body.replace(COMPOSES_RE, (_composesDecl, names) => {
      // One class per composes line. A comma list would not mean what it
      // looks like it means: the build (postcss-modules) binds `from` to the
      // last comma entry only, so every earlier name resolves as a LOCAL
      // class there - the vite build fails on the form rather than resolving
      // it, and this resolver refuses it for the same reason instead of
      // resolving a list the app can never ship.
      if (names.includes(",")) {
        throw new Error(
          `composes: "${names}" - the build binds \`from\` to the last comma entry only; compose one class per composes line instead`,
        );
      }
      const name = names.trim();
      const entries = bodies.get(`.${name}`);
      if (entries === undefined || entries.length === 0) {
        throw new Error(
          `composes references .${name}, but no such rule was found in the provided CSS sources - add the module that defines it to the case's cssFiles`,
        );
      }
      const inlined = [];
      for (const entry of entries) {
        if (entry.gate === null) {
          inlined.push(entry.body);
        } else if (consumerGate === null) {
          const decls = gatedByGate.get(entry.gate) ?? [];
          decls.push(entry.body.trim());
          gatedByGate.set(entry.gate, decls);
        } else {
          throw new Error(
            `composes in a rule gated by "${consumerGate}" references the media-gated .${name}: the composed class's gate must stay independent of the consumer's - extend resolveComposes instead`,
          );
        }
      }
      return inlined.join("\n");
    });

    let out = `${selector} {${resolvedBody}}`;
    for (const [gate, decls] of gatedByGate) {
      out += `\n\n${gate} {\n  ${selector} {\n${decls.map((d) => `    ${d}`).join("\n")}\n  }\n}`;
    }
    return out;
  });
}
