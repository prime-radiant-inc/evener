// One relative-specifier resolver for the repo's node-side tooling.
//
// Shared because the interesting part is a rule that is easy to get wrong and
// fails loudly one caller at a time: only a FILE resolves, so a bare
// `./helpers` naming a directory falls through to the `index` candidate rather
// than resolving to the directory and being read as a file (EISDIR). The
// extension list is a parameter because the callers probe different ones --
// the SDK rewriter only ever resolves TypeScript, the package-test gate also
// reaches .mjs tooling -- and widening either one to match the other would
// change what it resolves.
import { statSync } from "node:fs";
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
    // One stat, not an existsSync guarding it: statSync throws for a path that
    // is not there, which is the same answer as "not a file" here.
    try {
      if (statSync(candidate).isFile()) return candidate;
    } catch {}
  }
  return null;
}
