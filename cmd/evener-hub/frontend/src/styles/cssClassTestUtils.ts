// Static CSS class parsing for the stylesheet contract tests
// (requireclass-contract.test.ts, layoutguard-class-collisions.test.ts),
// which read *.module.css straight off disk rather than through vite.
//
// A class is "defined" only when it appears in SELECTOR position, never
// inside a declaration's value - so a declaration value with a literal dot
// followed by a letter (e.g. `url("./sprite.svg")`) is never mistaken for a
// class. Comments are stripped first, so a class-shaped mention inside a
// comment doesn't count either.
const COMMENT_RE = /\/\*[\s\S]*?\*\//g;

// A selector or at-rule prelude is the text between the start of the file
// or a `{`/`}`, and the next `{`. The boundary MUST be a lookbehind rather
// than a consumed leading character: that lets two preludes share the same
// single brace as their boundary, which is exactly the shape of
// `@media (...) { .foo { ... } }` - the text right after @media's own `{`
// starts a second, nested prelude, " .foo ". A version that consumes the
// boundary character as part of the match silently drops every rule nested
// one level inside an at-rule.
const PRELUDE_RE = /(?<=^|[{}])([^{}]*)\{/g;

// Class tokens within a prelude: identifier-shaped only, the same shape as
// a JS property name - every class in this codebase's CSS modules is
// camelCase with no hyphens. Requiring a letter/underscore immediately after
// the dot excludes decimals in media conditions (`min-width: 43.75em`).
const CLASS_TOKEN_RE = /\.([A-Za-z_][A-Za-z0-9_]*)/g;

export function definedClassNames(cssText: string): Set<string> {
  const stripped = cssText.replace(COMMENT_RE, " ");
  const names = new Set<string>();
  for (const [, prelude = ""] of stripped.matchAll(PRELUDE_RE)) {
    for (const [, name] of prelude.matchAll(CLASS_TOKEN_RE)) if (name !== undefined) names.add(name);
  }
  return names;
}
