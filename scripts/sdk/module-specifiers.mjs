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

export function parseSource(ts, file, text) {
  return ts.createSourceFile(
    file,
    text,
    ts.ScriptTarget.Latest,
    true,
    file.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
  );
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
//             | export-from | export-star-from | dynamic-import | require
//             | require-equals | mock-call
//   typeOnly  the statement was `import type` / `export type`, so it is erased
//   bindings  {imported, local, typeOnly} per named member; empty otherwise
//
// A default import that also has named members is reported as import-default
// with those members in `bindings`: the default is the part nothing here can
// map, and a caller that refuses it never reaches the rest.
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
          if (clause.name) kind = "import-default";
        }
        sites.push({ node: specifier, text: specifier.text, kind, typeOnly: Boolean(clause?.isTypeOnly), bindings });
      }
    } else if (ts.isExportDeclaration(node)) {
      const specifier = literal(node.moduleSpecifier);
      if (specifier) {
        const named = node.exportClause && ts.isNamedExports(node.exportClause);
        sites.push({
          node: specifier,
          text: specifier.text,
          kind: named ? "export-from" : "export-star-from",
          typeOnly: Boolean(node.isTypeOnly),
          bindings: named ? namedBindings(ts, node.exportClause.elements) : [],
        });
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
export function isLoadedAtRuntime(site) {
  return !site.typeOnly;
}
