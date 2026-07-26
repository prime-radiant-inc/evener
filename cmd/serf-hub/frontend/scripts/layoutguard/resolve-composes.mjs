// Minimal CSS-Modules `composes:` resolver.
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

export function resolveComposes(cssTexts) {
  const combined = cssTexts.join("\n\n");

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
