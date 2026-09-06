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
  const combined = cssTexts.map(unwrapGlobal).join("\n\n");

  const bodies = new Map();
  for (const m of combined.matchAll(RULE_RE)) {
    bodies.set(m[1], m[2]);
  }

  return combined.replace(RULE_RE, (_whole, selector, body) => {
    const resolvedBody = body.replace(COMPOSES_RE, (_composesDecl, names) => {
      return names
        .split(",")
        .map((n) => n.trim())
        .map((n) => {
          const target = bodies.get(`.${n}`);
          if (target === undefined) {
            throw new Error(`composes references .${n}, but no such rule was found in the provided CSS sources`);
          }
          return target;
        })
        .join("\n");
    });
    return `${selector} {${resolvedBody}}`;
  });
}
