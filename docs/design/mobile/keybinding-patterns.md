# Native shortcut patterns

The iOS engine in the Expo 57 Release build rejects the `v` RegExp flag used by
tinykeys 4. Pattern shortcuts such as `(F6|F7)` consequently produced a parse
warning and fell back to defaults. Ordinary shortcuts were unaffected.

The native installation patches tinykeys to compile its anchored `iv` pattern
through pinned regexpu-core 6.4.0, using `unicodeSetsFlag: "transform"` and the
compiler's returned flags. The parser still reads UnicodeSets syntax. There is
no retry with a different grammar, global RegExp replacement, or web dependency
patch. Compiled matchers remain real RegExp objects, including when the shared
validator clones them. Saved rules and displayed patterns retain authored text.

This is configuration of the hub's web shortcuts, not native keyboard dispatch.

## Compiler corrections

The regexpu patch also fixes two negative-property paths revealed by behavior
tests:

- A standalone `\P{Lowercase_Letter}` must be materialized when lowering `iv`;
  retaining that escape under `iu` changes the order of complement and folding.
- A UnicodeSets complement must remove the property's *folded* members from
  the folded universe. Removing the original members incorrectly includes
  Cherokee letters, whose canonical case is uppercase.

The differential tests exercise the actual native parser, shared validation and
preview, repeated matching, and cloned matchers. They cover set intersection,
subtraction, string sets, property-of-string emoji, positive and negative
properties, Cherokee, astral case pairs, invalid grammar, and duplicate-rule
selection. These bounded cases are not a complete ECMAScript conformance suite.

## Browser-engine discrepancy

Node 26.5.0 / V8 14.6.202.34-node.24 returns false for Kelvin sign U+212A and
long s U+017F in `/[\p{ASCII}&&\p{Letter}]/iv`. The compiler returns true.
The test explicitly uses the specification result for those two cases; it does
not claim an exact V8 differential pass.

[ECMAScript 2026](https://tc39.es/ecma262/2026/multipage/text-processing.html#sec-runtime-semantics-compiletocharset)
folds the property operands before intersection, retaining `k` and `s`.
[CharacterSetMatcher](https://tc39.es/ecma262/2026/multipage/text-processing.html#sec-runtime-semantics-charactersetmatcher)
then canonicalizes both the input and set members, so both characters match.
The inspected [V8 parser](https://github.com/v8/v8/blob/main/src/regexp/regexp-parser.cc)
special-cases ASCII with a literal range and bypasses the property case-folding
path. Browser-engine differences remain a qualification limitation; the native
compiler is not intended to reproduce that fast-path behavior.

## Metro dependency resolution

regexpu normally dynamically requires Unicode property modules. Metro requires
literal module paths, so the patch contains a lazy loader map generated from
the pinned regenerate-unicode-properties 10.2.2 manifest. Factories defer
property initialization until requested. To regenerate that map deliberately
when updating these pinned dependencies, run this from `mobile-native` after
installing them, then regenerate the regexpu patch with `npx patch-package
regexpu-core`:

```js
const fs = require("node:fs");
const manifest = require("regenerate-unicode-properties");
const entries = [];
for (const [property, values] of manifest) {
  for (const value of values) {
    const key = `${property}/${value}`;
    entries.push(`[${JSON.stringify(key)}, () => require(${JSON.stringify(`regenerate-unicode-properties/${key}.js`)})],`);
  }
}
fs.writeFileSync("node_modules/regexpu-core/unicode-properties.js",
  "// Static lazy imports let Metro bundle the pinned Unicode property manifest.\n" +
  "module.exports = new Map([\n" + entries.join("\n") + "\n]);\n");
```

`npm ci` applies both native dependency patches with `--error-on-fail`.
Vitest resolves shared headless imports to the native tinykeys installation,
matching Metro; the differential oracle explicitly imports the unpatched web
installation. Bundle cost and simulator observations belong in the shortcut
acceptance report, separate from these source-level decisions.
