// What counts as a test file, and the source-file extensions, skipped
// directories and app trees every sweep shares. The lists live in
// source-files.env, a plain key="word word" file, because the grep gate is a
// bash script that `source`s the same file: one source is what keeps a sweep
// in one language from drifting from a sweep in the other. The rewriter, the
// value-import derivation and the package-test gate all import from here.
import { readdirSync, readFileSync } from "node:fs";
import { extname, join } from "node:path";
import { fileURLToPath } from "node:url";

// The same shape as Vitest's default test glob. Two tools needed the answer
// and had two -- one for ".test.", one Vitest's glob -- so a .spec. file was a
// test to one and source to the other; the predicate is shared instead.
const TEST_FILE = /\.(test|spec)\.[cm]?[jt]sx?$/;

export function isTestFile(name) {
  return TEST_FILE.test(name);
}

function envList(env, key) {
  const match = env.match(new RegExp(`^${key}="([^"]*)"`, "m"));
  if (!match) throw new Error(`source-files.env has no ${key}`);
  return match[1].split(/\s+/).filter(Boolean);
}

const env = readFileSync(fileURLToPath(new URL("./source-files.env", import.meta.url)), "utf8");

// The dot is the JS spelling; the env carries the bare word the shell globs want.
export const SOURCE_EXTENSIONS = envList(env, "extensions").map((extension) => `.${extension}`);
export const SKIPPED_DIRS = new Set(envList(env, "skip_dirs"));
export const CONSUMER_TREES = envList(env, "consumer_trees");

// Every source file under `dir`, recursively. `skipDir(name, full)` prunes a
// directory and `keep(name)` filters the source-extension files; the defaults
// skip SKIPPED_DIRS and keep every source file. A directory that is not there
// yields nothing, so a sweep over a tree a fixture omits is simply empty. Order
// is unspecified: a caller that compares listings sorts. The one walker behind
// the rewriter, the value-import derivation and the package-test gate.
export function sourceFiles(dir, { skipDir = (name) => SKIPPED_DIRS.has(name), keep = () => true } = {}) {
  let entries;
  try {
    entries = readdirSync(dir, { withFileTypes: true });
  } catch {
    return [];
  }
  const found = [];
  for (const entry of entries) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) {
      if (!skipDir(entry.name, full)) found.push(...sourceFiles(full, { skipDir, keep }));
    } else if (entry.isFile() && SOURCE_EXTENSIONS.includes(extname(entry.name)) && keep(entry.name)) {
      found.push(full);
    }
  }
  return found;
}
