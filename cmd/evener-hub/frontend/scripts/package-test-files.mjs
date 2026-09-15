// package-test-files.mjs — fail the frontend gate if the AppWire package's
// test files stop being collected.
//
// The package lives at appwire-client/typescript/, outside this app's Vitest
// root, so it runs only because vite.config.ts's `test.include` names it
// explicitly. Drop or mistype that entry and every one of those files silently
// leaves the run: `make test-web` still reports a green suite, just a smaller
// one, and nothing else in the tree pins the suite's file count. This compares
// what Vitest collects under the package against what is on disk there, and
// then runs one of them for real: collection alone would not notice the
// package's `vitest` / `react` / `@testing-library/react` imports failing to
// resolve from outside the Vitest root, which is a transform-time error a
// collected-but-never-executed file hides.
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, realpathSync, rmSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import ts from "typescript";
import { isLoadedAtRuntime, moduleSpecifierSites, parseSource, walkImportGraph } from "../../../../scripts/sdk/module-specifiers.mjs";
import { resolveSourceFile } from "../../../../scripts/sdk/resolve-source.mjs";
import { CONSUMER_TREES, isTestFile, sourceFiles } from "../../../../scripts/sdk/source-files.mjs";

const frontend = fileURLToPath(new URL("../", import.meta.url));
const packageDir = path.resolve(frontend, "../../../appwire-client/typescript");

// Shared with the package's own value-import derivation, which needs the same
// answer: see scripts/sdk/source-files.mjs.

// Vitest resolves this one itself; it is the only bare specifier a package
// file may name without an alias.
const SELF_RESOLVING = new Set(["vitest"]);

// Every source file under the package, test or not - the no-app-import sweep
// has to see the whole tree, not only its tests. The shared walker, sorted so
// the disk listing compares stably against Vitest's.
export function sourceFilesOnDisk(dir) {
  return sourceFiles(dir).sort();
}

// A package module that reads from disk has to resolve the path against its own
// location, because the package is consumed from wherever the consumer happens
// to run and the process's working directory is not its own. The hazard is one
// shape: a filesystem call whose LEADING argument is a relative path literal,
// which is what anchors it to the working directory -- `join("..", …)`,
// `readFileSync("../x")`. Found on the AST rather than by line, so a call whose
// argument is wrapped onto the next line is caught too. A call anchored to a
// base identifier (`resolve(packageDir, "..")`, `new URL("../x",
// import.meta.url)`) resolves against that base, not the cwd, and an ordinary
// `from "../x"` import names no such call; neither matches. testing/
// hubWireFixtures.ts once read `join("..", "testdata", …)` against the
// frontend's CWD; it now loads the fixture through a `?raw` import, which is
// the other way to satisfy this.
const FS_PATH_FUNCTIONS = new Set([
  "join",
  "resolve",
  "readFileSync",
  "readFile",
  "readdirSync",
  "readdir",
  "existsSync",
  "createReadStream",
  "statSync",
  "stat",
  "openSync",
  "open",
]);
const cwdRelativeLiteral = (text) => /^\.\.?(?:[/\\]|$)/.test(text) || /(?:^|[/\\])testdata(?:[/\\]|$)/.test(text);

// Whether the file loads node's fs by any form -- an import, a require, a
// dynamic import -- read off the AST so all three count, where matching text
// once let the other forms through. `path.join` for a non-fs purpose in a file
// that never touches fs is not this rule's business.
const importsFs = (source) =>
  moduleSpecifierSites(ts, source).some((site) => /^(?:node:)?fs(?:\/promises)?$/.test(site.text));

export function describeCwdRelativeReads(files, read, dir) {
  const offenders = [];
  for (const file of files) {
    const source = parseSource(ts, file, read(file));
    if (!importsFs(source)) continue;
    const lines = source.text.split("\n");
    const seen = new Set();
    const visit = (node) => {
      if (ts.isCallExpression(node)) {
        const callee = ts.isPropertyAccessExpression(node.expression)
          ? node.expression.name.text
          : ts.isIdentifier(node.expression)
            ? node.expression.text
            : null;
        const first = node.arguments[0];
        if (callee && FS_PATH_FUNCTIONS.has(callee) && first && ts.isStringLiteralLike(first) && cwdRelativeLiteral(first.text)) {
          const line = source.getLineAndCharacterOfPosition(node.getStart(source)).line;
          if (!seen.has(line)) {
            seen.add(line);
            offenders.push(`${path.relative(dir, file)}:${line + 1}: ${lines[line].trim()}`);
          }
        }
      }
      ts.forEachChild(node, visit);
    };
    visit(source);
  }
  if (offenders.length === 0) return "";
  return [
    "the AppWire package must not read a path resolved against the working directory:",
    ...offenders.map((line) => `  ${line}`),
    "Resolve it with fileURLToPath(new URL(..., import.meta.url)), or move the file into the app.",
  ].join("\n");
}

// The `@evener/appwire-client/testing/` subpath is in-repo test support: it is
// absent from the tarball, so a production module importing it would name
// something no installed consumer can resolve, and would pull test fakes into
// the shipped bundle if it could. Only test files and the dev-support files
// that build the guard harnesses and previews may import it -- the set AGENTS.md
// names. Judged by path so a production consumer is caught wherever it sits.
const TESTING_SUBPATH = "@evener/appwire-client/testing/";
// `relativePath` is the repo-relative path, so a checkout that happens to sit
// under a directory called dev/ does not turn every file into dev-support --
// the segment that counts is `src/dev/`, and __tests__ is matched as a path
// segment, not anywhere in an absolute prefix.
export function mayImportTesting(relativePath) {
  const base = path.basename(relativePath);
  const segments = relativePath.split(/[/\\]/);
  return (
    isTestFile(base) ||
    segments.includes("__tests__") ||
    segments.slice(0, -1).some((segment, index) => segment === "dev" && segments[index - 1] === "src") ||
    /TestUtils\.[cm]?[jt]sx?$/.test(base)
  );
}

export function describeTestingImportsOutsideTests(files, read, dir) {
  const offenders = [];
  for (const file of files) {
    if (mayImportTesting(path.relative(dir, file))) continue;
    const text = read(file);
    if (!text.includes(TESTING_SUBPATH)) continue;
    for (const site of moduleSpecifierSites(ts, parseSource(ts, file, text))) {
      if (site.text.startsWith(TESTING_SUBPATH)) offenders.push(`${path.relative(dir, file)}: imports ${site.text}`);
    }
  }
  if (offenders.length === 0) return "";
  return [
    "the AppWire testing/ subpath is in-repo test support; only test and dev-support files may import it:",
    ...offenders.map((line) => `  ${line}`),
    "Move the use into a *.test.*, __tests__/, src/dev/ or *TestUtils.* file, or import from the package root.",
  ].join("\n");
}

// The package is a standalone library: nothing in it may reach back into the
// app -- nor into the mobile trees. Two test files reached into the app, and
// moving them out is only half the fix; this is the half that keeps it fixed.
// Read through the AST so every form counts -- a from-import, a side-effect
// import, a require, a dynamic import -- and a comment naming a path does not,
// being no specifier at all. A relative specifier is the offender when it
// resolves into one of the consumer trees (the web src and the two mobile
// trees); an import into shared repo tooling (scripts/sdk) is what the
// package's own scripts legitimately do, and a bare specifier names a
// dependency the alias check covers.
export function describeAppImports(files, read, dir) {
  const repoRoot = path.resolve(dir, "..", "..");
  const consumerTrees = CONSUMER_TREES.map((tree) => path.join(repoRoot, tree));
  const intoConsumerTree = (resolved) =>
    consumerTrees.some((tree) => resolved === tree || resolved.startsWith(tree + path.sep));
  const offenders = [];
  for (const file of files) {
    for (const site of moduleSpecifierSites(ts, parseSource(ts, file, read(file)))) {
      if (!site.text.startsWith(".")) continue;
      if (intoConsumerTree(path.resolve(path.dirname(file), site.text))) {
        offenders.push(`${path.relative(dir, file)}: imports ${site.text}`);
      }
    }
  }
  if (offenders.length === 0) return "";
  return [
    "the AppWire package must not import from the app or the mobile trees:",
    ...offenders.map((line) => `  ${line}`),
    "Move the file into the package, or restate the type it needs locally.",
  ].join("\n");
}

// vite.config.ts's `resolve.alias`, read off its AST rather than matched: this
// is the list the package's bare imports are held against, and a regex that
// missed an entry would fail the gate over an alias that is there.
//
// Each key maps to whether that alias can serve a SUBPATH, which is decided by
// its target: a directory can, a file cannot. `@evener/appwire-client` points
// at index.ts, so it answers `@evener/appwire-client` and nothing below it --
// Vite would build index.ts/testing/fakeClient, which is not a path. The
// target is classified from the last string literal of its expression, since
// the expressions are path.join calls this file cannot evaluate.
const TARGET_IS_A_FILE = /\.[cm]?[jt]sx?$|\.json$/;

export function aliasesFrom(configText) {
  const source = parseSource(ts, "vite.config.ts", configText);
  const aliases = new Map();
  const lastLiteral = (node) => {
    let found = null;
    const walk = (child) => {
      if (ts.isStringLiteralLike(child)) found = child.text;
      ts.forEachChild(child, walk);
    };
    walk(node);
    return found;
  };
  const visit = (node) => {
    if (
      ts.isPropertyAssignment(node) &&
      !ts.isComputedPropertyName(node.name) &&
      node.name.text === "alias" &&
      ts.isObjectLiteralExpression(node.initializer)
    ) {
      for (const property of node.initializer.properties) {
        if (!ts.isPropertyAssignment(property)) continue;
        const name = property.name;
        if (!ts.isIdentifier(name) && !ts.isStringLiteral(name)) continue;
        const target = lastLiteral(property.initializer);
        aliases.set(name.text, { servesSubpaths: target !== null && !TARGET_IS_A_FILE.test(target) });
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return aliases;
}

// Whether `key` can stand in for a specifier BELOW it, decided by its target:
// a directory alias can, a file alias (an entry point) cannot. aliasesFrom
// records that per key, so callers -- the gate and its tests alike -- pass a
// Map of it.
function servesSubpaths(key, aliases) {
  return aliases.get(key).servesSubpaths;
}

// Every module this file LOADS, by any form: a vi.mock or a require reaches a
// dependency exactly as an import does, and an alias it needs is needed just
// as much. An erased one reaches nothing -- `import type X from "pkg"` is gone
// before Vite resolves anything -- so counting it would demand an alias for a
// dependency no run has.
function loadedSpecifiersIn(file, text) {
  return moduleSpecifierSites(ts, parseSource(ts, file, text))
    .filter(isLoadedAtRuntime)
    .map((site) => site.text);
}

// The package's own files are TypeScript, plus the .mjs tooling its tests
// reach. Extension probing and the directory/index.ts rule are the rewriter's,
// shared rather than restated: an `existsSync` that says yes to a directory
// returns the directory, and the caller reads it as a file.
export const resolvePackageImport = (from, specifier) =>
  resolveSourceFile(from, specifier, [".ts", ".tsx", ".mjs", ".js"]);

// Every bare specifier reachable from the package's test files, mapped to the
// package-relative files that name it. The walk stops at the first bare
// specifier: what is inside node_modules is not this gate's business, and the
// package's own scripts that Vitest never loads (qualify-package.mjs and its
// `ws`) are not reachable from a test, so they are not held to this rule.
export function reachableBareImports(testFiles, read, resolveRelative, dir) {
  const bare = new Map();
  walkImportGraph(
    testFiles,
    (file) => loadedSpecifiersIn(file, read(file)),
    (file, specifier) => {
      if (specifier.startsWith("node:")) return null;
      // A relative import is followed; a bare one is the leaf this gate
      // records -- what is inside node_modules is not its business.
      if (specifier.startsWith(".")) return resolveRelative(file, specifier) || null;
      if (!bare.has(specifier)) bare.set(specifier, []);
      bare.get(specifier).push(path.relative(dir, file));
      return null;
    },
  );
  return bare;
}

// The alias Vite would use for a specifier: the FIRST key in config order that
// matches, which is how Rollup's alias plugin resolves -- not the longest one.
// The difference only shows when a general key precedes a specific one, which
// is what describeAliasOrder refuses.
export function firstAliasMatch(specifier, aliases) {
  for (const key of aliases.keys()) {
    if (specifier === key) return key;
    // A prefix match answers only if that alias serves subpaths at all.
    if (specifier.startsWith(`${key}/`) && servesSubpaths(key, aliases)) return key;
  }
  return null;
}

// First-match means order carries meaning: a key that is a path prefix of a
// later one swallows it, and `@evener/appwire-client` placed before
// `@evener/appwire-client/testing` would answer for every testing specifier
// with index.ts. vite.config.ts says as much in a comment; this is the same
// rule, enforced.
export function describeAliasOrder(aliases) {
  const keys = [...aliases.keys()];
  const offenders = [];
  for (let i = 0; i < keys.length; i++) {
    for (let j = i + 1; j < keys.length; j++) {
      if (keys[j].startsWith(`${keys[i]}/`)) offenders.push(`${keys[i]} precedes ${keys[j]}, which it swallows`);
    }
  }
  if (offenders.length === 0) return "";
  return [
    "vite.config.ts's resolve.alias is ordered so a general key answers before a specific one:",
    ...offenders.map((line) => `  ${line}`),
    "Vite takes the FIRST matching alias, so the specific entries have to come first.",
  ].join("\n");
}

// A bare specifier with no alias resolves from the importer, which is inside
// the package. That works on any machine where `npm ci --prefix
// appwire-client/typescript` has run for make test-api-package, and fails in
// CI's web job, which installs only this app - a green local gate over an
// import CI cannot resolve. This is the check that stops the two diverging.
export function describeUnaliasedImports(bare, aliases) {
  const offenders = [];
  for (const [specifier, files] of [...bare].sort()) {
    if (SELF_RESOLVING.has(specifier)) continue;
    if (firstAliasMatch(specifier, aliases) !== null) continue;
    offenders.push(`${specifier}, imported by ${files.join(", ")}`);
  }
  if (offenders.length === 0) return "";
  return [
    "the AppWire package's test graph names bare specifiers that vite.config.ts does not alias:",
    ...offenders.map((line) => `  ${line}`),
    "Vite resolves those from the package directory, which has node_modules only where",
    "`npm ci --prefix appwire-client/typescript` has run - not in CI's web job. Add an alias",
    "in cmd/evener-hub/frontend/vite.config.ts pointing at this app's copy.",
  ].join("\n");
}

export function testFilesOnDisk(dir) {
  return sourceFiles(dir, { keep: isTestFile }).sort();
}

// Vitest prints one collected file per line, relative to its root.
export function collectedUnder(listOutput, root, dir) {
  return listOutput
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line) => path.resolve(root, line))
    .filter((file) => file.startsWith(`${dir}${path.sep}`))
    .sort();
}

// The proof file is the first package test that imports `vitest` - executing
// one is what turns "collected" into "its bare imports really do resolve".
export function pickProofFile(files, read) {
  return files.find((file) => /(^|\n)\s*import\b[^;]*?from\s+["']vitest["']/.test(read(file))) ?? "";
}

// The JSON reporter's own summary, checked rather than trusted: a run that
// collected the file and executed nothing reports numTotalTests 0 and would
// otherwise read as a pass.
export function describeProofRun(report, file, dir) {
  // Compare on the real path relative to the package, not on the absolute
  // string: the reporter and this process can reach the same file by different
  // prefixes (a symlinked worktree, /var against /private/var on macOS), and an
  // inequality there would read as "never executed". realpathSync throws for a
  // path that does not exist, which for this comparison is the same answer as
  // "a different file", so fall back to the unresolved form.
  // A path that does not exist has no real path, and for this comparison that
  // is the same answer as "a different file". Only that case falls back: any
  // other error is a bug here and has to be loud, because a silent fallback
  // turns a broken comparison into a wrong verdict.
  const real = (name) => {
    try {
      return realpathSync(name);
    } catch (err) {
      if (err?.code !== "ENOENT" && err?.code !== "ENOTDIR") throw err;
      return path.resolve(name);
    }
  };
  const key = (name) => path.relative(real(dir), real(name));
  const ran = report.testResults?.some((result) => key(result.name) === key(file)) ?? false;
  if (!ran)
    return `${path.basename(file)} was not executed - the JSON report names ${(report.testResults ?? []).length} other file(s)`;
  if ((report.numTotalTests ?? 0) === 0) return `${path.basename(file)} ran no tests`;
  if ((report.numFailedTests ?? 0) > 0) return `${path.basename(file)} had ${report.numFailedTests} failing test(s)`;
  return "";
}

// Returns "" when the two agree, and otherwise the whole story: an empty disk
// listing is a failure too, because a check that measures nothing passes.
export function describeDifference(onDisk, collected, dir) {
  if (onDisk.length === 0) {
    return `no test files on disk under ${dir} - this check is measuring nothing`;
  }
  const missing = onDisk.filter((file) => !collected.includes(file));
  const extra = collected.filter((file) => !onDisk.includes(file));
  if (missing.length === 0 && extra.length === 0) return "";
  const lines = [`Vitest collects ${collected.length} of the package's ${onDisk.length} test files.`];
  for (const file of missing) lines.push(`  not collected: ${path.relative(dir, file)}`);
  for (const file of extra) lines.push(`  collected but not on disk: ${path.relative(dir, file)}`);
  lines.push("Fix vite.config.ts's test.include - it is the only thing that reaches outside the Vitest root.");
  return lines.join("\n");
}

// pathToFileURL, not a hand-built `file://` string: import.meta.url is
// percent-encoded, so a checkout path containing a space compares unequal and
// the whole check would skip in silence.
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  // The LOCAL binary, never `npx`: with node_modules missing or stale, npx
  // falls back to the npm cache or the registry and a default gate must make
  // no network request. scripts/web/web-preflight.sh guards the same hazard
  // by asking ./node_modules/.bin/tsc for its version rather than a bare or
  // npx `tsc`, which resolves to the unrelated tsc@2.0.4 package.
  const vitestBin = path.join(frontend, "node_modules", ".bin", "vitest");
  if (!existsSync(vitestBin)) {
    console.error(`${vitestBin} is missing - run npm ci in cmd/evener-hub/frontend`);
    process.exit(1);
  }
  const listed = spawnSync(vitestBin, ["list", "--filesOnly"], { cwd: frontend, encoding: "utf8" });
  if (listed.status !== 0) {
    console.error(`vitest list failed (exit ${listed.status}):\n${listed.stderr ?? ""}`);
    process.exit(1);
  }
  // One read per file across every check, and one walk of the package tree:
  // the test files are the source files that are tests, not a second sweep.
  const fileCache = new Map();
  const readFile = (file) => {
    let text = fileCache.get(file);
    if (text === undefined) {
      text = readFileSync(file, "utf8");
      fileCache.set(file, text);
    }
    return text;
  };
  const viteAliases = aliasesFrom(readFileSync(path.join(frontend, "vite.config.ts"), "utf8"));
  const packageSources = sourceFilesOnDisk(packageDir);
  const onDisk = packageSources.filter((file) => isTestFile(path.basename(file)));
  // The consumer trees, for the testing/-subpath rule: a production module in
  // the app or the mobile trees must not import in-repo test support.
  const repoRoot = path.resolve(frontend, "..", "..", "..");
  const consumerSources = CONSUMER_TREES.flatMap((tree) => sourceFiles(path.join(repoRoot, tree)));
  for (const problem of [
    describeAppImports(packageSources, readFile, packageDir),
    describeCwdRelativeReads(packageSources, readFile, packageDir),
    describeAliasOrder(viteAliases),
    describeUnaliasedImports(reachableBareImports(onDisk, readFile, resolvePackageImport, packageDir), viteAliases),
    describeTestingImportsOutsideTests(consumerSources, readFile, repoRoot),
  ]) {
    if (problem) {
      console.error(problem);
      process.exit(1);
    }
  }

  const problem = describeDifference(onDisk, collectedUnder(listed.stdout, frontend, packageDir), packageDir);
  if (problem) {
    console.error(problem);
    process.exit(1);
  }

  const proof = pickProofFile(onDisk, readFile);
  if (!proof) {
    console.error("no package test file imports vitest - this check is measuring nothing");
    process.exit(1);
  }
  const reportFile = path.join(mkdtempSync(path.join(os.tmpdir(), "appwire-proof-")), "report.json");
  const ran = spawnSync(vitestBin, ["run", "--reporter=json", `--outputFile=${reportFile}`, proof], {
    cwd: frontend,
    encoding: "utf8",
  });
  if (ran.status !== 0 || !existsSync(reportFile)) {
    console.error(
      `running ${path.relative(packageDir, proof)} failed (exit ${ran.status}):\n${ran.stdout ?? ""}${ran.stderr ?? ""}`,
    );
    process.exit(1);
  }
  const report = JSON.parse(readFileSync(reportFile, "utf8"));
  rmSync(path.dirname(reportFile), { recursive: true, force: true });
  const trouble = describeProofRun(report, proof, packageDir);
  if (trouble) {
    console.error(trouble);
    process.exit(1);
  }
  console.log(
    `the AppWire package imports nothing from the app and reads nothing through the working directory; vitest collects all ${onDisk.length} of its test files, and ${path.relative(packageDir, proof)} executes and passes`,
  );
}
