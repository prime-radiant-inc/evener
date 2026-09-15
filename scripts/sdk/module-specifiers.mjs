// One reader for "where does this file name a module, and how".
//
// Five tools in this repo answer that question -- the SDK import rewriter, the
// value-import derivation behind resolve-check.mjs, the package-test gate's
// graph walk, the native scripts' resolution check, and (in its own way) the
// grep gate. Each grew its own AST sweep, and each missed a different form: a
// no-substitution template literal, a require(), a vi.mock(), a specifier on
// the line after the call that opens it. A form nobody reads is a form that
// slips past every one of them in silence, so they read from here instead.
//
// `ts` is a parameter rather than an import: these callers resolve TypeScript
// from four different places (the frontend's node_modules, the package's own,
// mobile-native's, and the frontend's again by Vite alias), and a bare import
// here would pick the wrong one or none.

// The call names that take a module specifier as their first argument and are
// not `import`/`require`: vitest's and jest's mocking family.
const MOCK_CALLS = new Set(["mock", "doMock", "unmock", "importActual", "importMock"]);

// The ScriptKind the parser needs for each extension. It decides two things
// that matter here: whether `<X>` opens JSX or a type assertion, and whether
// TypeScript-only syntax is allowed at all. Guessing TS for everything but
// .tsx dropped the specifiers nested in any .jsx file's JSX -- `<View>` read
// as a type assertion, and the dynamic import in the markup went with the
// derailed subtree. .tsx is TSX (JSX plus TypeScript); .jsx and the .js React
// Native writes are JSX (JSX plus plain JavaScript, no type assertions to
// collide with); .mjs/.cjs are plain JS; the rest is TypeScript.
function scriptKindOf(ts, file) {
  const extension = file.slice(file.lastIndexOf("."));
  switch (extension) {
    case ".tsx":
      return ts.ScriptKind.TSX;
    case ".jsx":
    case ".js":
      return ts.ScriptKind.JSX;
    case ".mjs":
    case ".cjs":
      return ts.ScriptKind.JS;
    default:
      return ts.ScriptKind.TS;
  }
}

export function parseSource(ts, file, text) {
  return ts.createSourceFile(file, text, ts.ScriptTarget.Latest, true, scriptKindOf(ts, file));
}

function namedBindings(ts, elements) {
  return elements.map((element) => ({
    imported: (element.propertyName ?? element.name).text,
    local: element.name.text,
    typeOnly: Boolean(element.isTypeOnly),
  }));
}

// Every site in `source` that names a module, in source order. Each carries:
//
//   node      the string-literal-like node holding the specifier, for edits
//   text      the specifier itself
//   kind      import-named | import-namespace | import-default | import-side-effect
//             | export-from | export-namespace-from | export-star-from
//             | dynamic-import | require
//             | require-equals | mock-call
//   typeOnly  the statement was `import type` / `export type`, so it is erased
//   bindings  {imported, local, typeOnly} per named member; empty otherwise
//
// A default import that also has named members is reported as import-default,
// with the default itself first in `bindings` under the name `default`: it is
// the part nothing here can map, and a caller that refuses the kind never
// reaches the rest.
export function moduleSpecifierSites(ts, source) {
  const sites = [];
  const literal = (node) => (node && ts.isStringLiteralLike(node) ? node : null);
  const visit = (node) => {
    if (ts.isImportDeclaration(node)) {
      const specifier = literal(node.moduleSpecifier);
      if (specifier) {
        const clause = node.importClause;
        let kind = "import-side-effect";
        let bindings = [];
        if (clause) {
          if (clause.namedBindings && ts.isNamespaceImport(clause.namedBindings)) kind = "import-namespace";
          else if (clause.namedBindings && ts.isNamedImports(clause.namedBindings)) {
            kind = "import-named";
            bindings = namedBindings(ts, clause.namedBindings.elements);
          }
          if (clause.name) {
            kind = "import-default";
            // The default is a binding like any other, recorded rather than
            // implied by the kind: `import def, { type T } from "x"` binds a
            // runtime value, and a rule reading only the named members would
            // call the whole statement erased.
            bindings = [
              { imported: "default", local: clause.name.text, typeOnly: Boolean(clause.isTypeOnly) },
              ...bindings,
            ];
          }
        }
        sites.push({ node: specifier, text: specifier.text, kind, typeOnly: Boolean(clause?.isTypeOnly), bindings });
      }
    } else if (ts.isExportDeclaration(node)) {
      const specifier = literal(node.moduleSpecifier);
      if (specifier) {
        const clause = node.exportClause;
        // Three shapes, and they answer different questions: a named list
        // names what it takes, `export * as ns` names one binding and needs
        // the module to exist, and a bare `export *` names nothing at all.
        let kind = "export-star-from";
        let bindings = [];
        if (clause && ts.isNamedExports(clause)) {
          kind = "export-from";
          bindings = namedBindings(ts, clause.elements);
        } else if (clause && ts.isNamespaceExport(clause)) {
          kind = "export-namespace-from";
        }
        sites.push({ node: specifier, text: specifier.text, kind, typeOnly: Boolean(node.isTypeOnly), bindings });
      }
    } else if (ts.isImportEqualsDeclaration(node) && ts.isExternalModuleReference(node.moduleReference)) {
      // `import X = require("./x")`: TypeScript's own CommonJS import, binding
      // the whole module under one name.
      const specifier = literal(node.moduleReference.expression);
      if (specifier) {
        sites.push({
          node: specifier,
          text: specifier.text,
          kind: "require-equals",
          typeOnly: Boolean(node.isTypeOnly),
          bindings: [],
        });
      }
    } else if (ts.isImportTypeNode(node) && ts.isLiteralTypeNode(node.argument)) {
      // `import("./x").Name` in a type position: erased, but it still names a
      // module, and a rewrite has to reach it.
      const specifier = literal(node.argument.literal);
      if (specifier) {
        let qualifier = node.qualifier;
        while (qualifier && ts.isQualifiedName(qualifier)) qualifier = qualifier.left;
        sites.push({
          node: specifier,
          text: specifier.text,
          kind: qualifier ? "import-named" : "import-namespace",
          typeOnly: true,
          bindings: qualifier ? [{ imported: qualifier.text, local: qualifier.text, typeOnly: true }] : [],
        });
      }
    } else if (ts.isCallExpression(node)) {
      const specifier = literal(node.arguments[0]);
      if (specifier) {
        let kind = null;
        if (node.expression.kind === ts.SyntaxKind.ImportKeyword) kind = "dynamic-import";
        else if (ts.isIdentifier(node.expression) && node.expression.text === "require") kind = "require";
        else if (ts.isPropertyAccessExpression(node.expression) && MOCK_CALLS.has(node.expression.name.text)) {
          kind = "mock-call";
        }
        if (kind) sites.push({ node: specifier, text: specifier.text, kind, typeOnly: false, bindings: [] });
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return sites;
}

// Whether a site is loaded when the program runs, as opposed to erased by the
// compiler. A star re-export is loaded like any other: it names no binding, so
// a caller asking "which values must the package provide" cannot use it, but a
// caller walking a module graph has to follow it or it stops one hop short of
// whatever the chain reaches.
//
// `import { type Foo } from "x"` is erased just as surely as `import type
// { Foo }`, and only the binding shapes say so: the statement carries no
// type-only flag. A site that names NO binding -- a star re-export, a
// side-effect import, a bare require -- is loaded whatever it does with what
// it finds, so the emptiness has to be checked before the every().
export function isLoadedAtRuntime(site) {
  if (site.typeOnly) return false;
  if (site.bindings.length === 0) return true;
  return site.bindings.some((binding) => !binding.typeOnly);
}

// Whether a site brings in named bindings, as opposed to taking the module
// whole. A namespace import, a side-effect import, a bare re-export, a
// require, a dynamic import and a mock call all name no members to map onto
// the package name -- only a named import/re-export or a default import does.
const KINDS_WITH_BINDINGS = new Set(["import-named", "import-default", "export-from"]);

export function namesBindings(site) {
  return KINDS_WITH_BINDINGS.has(site.kind);
}

// A depth-first walk of a module graph from `entries`, each file visited once.
// `specifiersOf(file)` yields the specifiers a file loads; `follow(file,
// specifier)` returns the next file to descend into, or null to stop -- a bare
// specifier, a node_modules boundary or an import the caller records and does
// not follow. The two graph gates (the native scripts' resolution check and
// the package's test-file sweep) differ only in those two callbacks. Returns
// the set of files reached.
export function walkImportGraph(entries, specifiersOf, follow) {
  const walked = new Set();
  const visit = (file) => {
    if (walked.has(file)) return;
    walked.add(file);
    for (const specifier of specifiersOf(file)) {
      const next = follow(file, specifier);
      if (next) visit(next);
    }
  };
  for (const entry of entries) visit(entry);
  return walked;
}
