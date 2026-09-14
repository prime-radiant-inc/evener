// One relative-specifier resolver for the repo's node-side tooling.
//
// Shared because the interesting part is a rule that is easy to get wrong and
// fails loudly one caller at a time: `existsSync` is true for a directory, so
// a bare `./helpers` resolves to the directory itself, the `index` candidate
// below never runs, and the caller reads a directory as a file (EISDIR). The
// extension list is a parameter because the callers probe different ones --
// the SDK rewriter only ever resolves TypeScript, the package-test gate also
// reaches .mjs tooling -- and widening either one to match the other would
// change what it resolves.
import { existsSync, statSync } from "node:fs";
import path from "node:path";

// Returns the file `specifier` names when read from `fromFile`, or null.
// Relative specifiers only; a bare one belongs to Node's own resolver.
export function resolveSourceFile(fromFile, specifier, extensions) {
  const base = path.resolve(path.dirname(fromFile), specifier);
  const candidates = [
    base,
    ...extensions.map((extension) => `${base}${extension}`),
    // The directory fallback probes the same extensions: a caller that asks
    // about .mjs means it for ./dir/index.mjs too.
    ...extensions.map((extension) => path.join(base, `index${extension}`)),
  ];
  for (const candidate of candidates) {
    if (existsSync(candidate) && statSync(candidate).isFile()) return candidate;
  }
  return null;
}
