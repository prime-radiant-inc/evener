// Inventories TypeScript tests that use human-readable copy as their oracle,
// the TypeScript half of docs/developing-evener/testing.md's "Copy Is Not a
// Test Oracle" tooling (the Go half, and the `evener dev copy-oracle`
// command that runs both, live in internal/devtool/copyonly).
//
// copyonlyAudit.test.ts audits the tracked TypeScript test files one slice per
// case (one openRepo snapshot) and fails on any target in a slice missing from
// allowlist.tsv. A target is a test whose name says its subject is wording (it
// shows, renders, labels, says, ...) and whose every check is a copy claim. A
// check is an expect(...) chain or a getBy*/findBy*
// query. It is a copy claim when it compares output with a copy literal and
// with nothing else: a count, an attribute or state read, a spy assertion or a
// role query makes it a behavior check. A copy literal also appears in
// production TypeScript source, is not shaped like an identifier, code or
// markup, and is not text the test itself supplied: seeing a test's own
// fixture come back out is a real behavioral check.
//
// inventoryRepo also lists every copy hit (a by-text query or text matcher
// against production copy, sorted into ts-assert, ts-locator and ts-constant),
// the fixture literals per package, and the copy-only tests whose names say
// they test behavior. Run directly, this module prints that inventory as
// JSON for `evener dev copy-oracle`:
//
//   node cmd/evener-hub/frontend/src/dev/copyonly/copyonly.ts --root REPO [--path PREFIX]
//
// Tests are read with the TypeScript compiler, so comments, strings, regex
// literals, JSX text, multi-line and typed declarations can't blur a test's
// boundaries or fake a check. The module runs under Node's type stripping, so
// it uses only erasable TypeScript syntax and no relative imports.
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { argv, stdout } from "node:process";
import * as ts from "typescript";

/** A test that exists only to check wording. */
export interface Target {
  /** Slash-separated, relative to the scanned root. */
  file: string;
  test: string;
  line: number;
}

/** One assertion whose oracle is production copy. */
export interface Hit {
  file: string;
  line: number;
  category: "ts-assert" | "ts-locator" | "ts-constant";
  literal: string;
}

/** A test whose every check is a copy claim; its name says it tests wording (copy) or behavior. */
export interface CopyOnlyTest extends Target {
  purpose: "copy" | "behavior";
}

/** The inventory of a set of tests; printed for internal/devtool/copyonly's Report. */
export interface Inventory {
  hits: Hit[];
  /** Per package (a test file's directory): literals found only in tests. */
  fixtures: Record<string, number>;
  copyOnly: CopyOnlyTest[];
}

/**
 * The TypeScript trees: tests in them are scanned, and their non-test sources
 * are the production corpus. mobile/src is the shared tree mobile-native
 * typechecks and tests.
 */
const TS_TREES = ["cmd/evener-hub/frontend/src/", "mobile-native/", "mobile/src/", "appwire-client/typescript/"];
const TS_SOURCE = /\.(ts|tsx|mts)$/;
const TEST_FILE = /\.(test|spec)\./;
/** Test support: never production copy. */
const TEST_SUPPORT = /(\/__tests__\/|TestUtils\.|\/testing\/|\/src\/dev\/)/;

/** The TypeScript files git tracks under root, in git's order. */
function trackedFiles(root: string): string[] {
  return execFileSync("git", ["-C", root, "ls-files", "-z"], { encoding: "utf8", maxBuffer: 1 << 28 })
    .split("\0")
    .filter(
      (f) =>
        f !== "" &&
        !f.includes("node_modules/") &&
        TS_TREES.some((tree) => f.startsWith(tree)) &&
        TS_SOURCE.test(f) &&
        !f.endsWith(".d.ts"),
    );
}

/** A tracked file's text, or null when it is tracked but deleted in the working tree. */
function readTracked(root: string, f: string): string | null {
  try {
    return readFileSync(join(root, f), "utf8");
  } catch (err) {
    if ((err as { code?: string }).code === "ENOENT") return null; // tracked but deleted
    throw err;
  }
}

/** The corpus a test's literals are looked up in: every tracked non-test, non-support source, joined. */
function productionCorpus(root: string, files: readonly string[]): string {
  return files
    .filter((f) => !TEST_FILE.test(f) && !TEST_SUPPORT.test(f))
    .map((f) => readTracked(root, f))
    .join("\n");
}

/** One checkout's TypeScript inventory, read once so slices of one audit share it. */
export interface RepoScan {
  /** The tracked TypeScript test files, in git's order. */
  testFiles: string[];
  /** Every target among the given tracked test files, sorted by file and line. */
  scan(files: readonly string[]): Target[];
}

/**
 * Reads root's tracked TypeScript files and production corpus once, and returns
 * a snapshot of that checkout: scan() answers from the text read here, so a
 * change to the working tree after openRepo returns is not seen, and the next
 * audit needs a fresh snapshot. A caller that audits the checkout in slices
 * takes one snapshot and scans each slice with it, sharing the reads instead of
 * re-reading per slice (issue #4608).
 */
export function openRepo(root: string): RepoScan {
  const files = trackedFiles(root);
  const scanner = new Scanner(productionCorpus(root, files));
  const testFiles = files.filter((f) => TEST_FILE.test(f));
  const contents = new Map<string, string>();
  for (const f of testFiles) {
    const text = readTracked(root, f);
    if (text !== null) contents.set(f, text);
  }
  return {
    testFiles,
    scan(files: readonly string[]): Target[] {
      const targets: Target[] = [];
      for (const f of files) {
        const text = contents.get(f);
        if (text === undefined) continue;
        targets.push(...scanner.scanFile(f, text));
      }
      targets.sort((a, b) => (a.file === b.file ? a.line - b.line : a.file < b.file ? -1 : 1));
      return targets;
    },
  };
}

/**
 * Every target among the TypeScript tests git tracks under root, sorted by
 * file and line.
 */
export function scanRepo(root: string): Target[] {
  const repo = openRepo(root);
  return repo.scan(repo.testFiles);
}

/**
 * The inventory of the TypeScript tests git tracks under root whose path
 * starts with prefix, copy-only tests sorted by file and line. Tracked files
 * are read as they are in the working tree; untracked files are left out, so
 * a scratch file can't change the answer. Hits and fixture counts are left
 * out unless withHits is set: they look up every literal in every test, which
 * the audit doesn't need.
 */
export function inventoryRepo(root: string, prefix: string, withHits = true): Inventory {
  const files = trackedFiles(root);
  const scanner = new Scanner(productionCorpus(root, files));
  const inventory: Inventory = { hits: [], fixtures: {}, copyOnly: [] };
  for (const f of files.filter((f) => TEST_FILE.test(f) && f.startsWith(prefix))) {
    const text = readTracked(root, f);
    if (text === null) continue;
    const found = scanner.inventoryFile(f, text, withHits);
    inventory.hits.push(...found.hits);
    inventory.copyOnly.push(...found.copyOnly);
    if (found.fixtures > 0) inventory.fixtures[dirname(f)] = (inventory.fixtures[dirname(f)] ?? 0) + found.fixtures;
  }
  inventory.copyOnly.sort((a, b) => (a.file === b.file ? a.line - b.line : a.file < b.file ? -1 : 1));
  return inventory;
}

/** A test name that says its subject is wording. */
const COPY_PURPOSE_NAME =
  /show|display|render|label|title|text|copy|message|word|say|mention|hint|placeholder|tooltip|caption|usage|help|heading|banner|describ|phrase|string/i;
/** An identifier named like copy (*_MESSAGE, *Label, ...). */
const COPY_IDENT =
  /(MESSAGE|LABEL|COPY|TEXT|TITLE|HINT|ERROR|PROMPT|USAGE|PLACEHOLDER|Message|Label|Copy|Text|Title|Hint|Error|Prompt|Usage|Placeholder)S?$/;

const TEST_MODIFIERS = new Set(["only", "skip", "concurrent"]);
const CHECK_QUERY = /^(get|find)(All)?By\w+$/;
const TEXT_QUERY = /^(get|find|query)(All)?By(Text|Title)$/;
const ROLE_QUERY = /^(get|find|query)(All)?ByRole$/;
const TEXT_MATCHERS = new Set(["toContain", "toBe", "toEqual", "toMatch", "toHaveTextContent", "toStrictEqual"]);
const NUMERIC_MATCHERS = new Set(["toBe", "toEqual", "toStrictEqual"]);
/** Calls that act on the element a query found: a by-text query inside one only locates a control. */
const CLICK_CALLS = new Set(["press", "click", "act"]);
const CLICK_RECEIVERS = new Set(["fireEvent", "user", "userEvent"]);
/** Reads of structure or state: a check through one of these is about the element, not its words. */
const BEHAVIOR_PROPERTIES = new Set(["length", "tagName", "dataset", "checked", "disabled", "className", "classList"]);
const BEHAVIOR_CALLS = new Set(["getAttribute", "hasAttribute", "toHaveAttribute", "toHaveLength"]);
const BEHAVIOR_CALL_PREFIXES = ["toHaveBeenCalled", "toBeGreaterThan", "toBeLessThan"];
const COMPARISONS = new Set([
  ts.SyntaxKind.EqualsEqualsToken,
  ts.SyntaxKind.EqualsEqualsEqualsToken,
  ts.SyntaxKind.ExclamationEqualsToken,
  ts.SyntaxKind.ExclamationEqualsEqualsToken,
  ts.SyntaxKind.LessThanToken,
  ts.SyntaxKind.LessThanEqualsToken,
  ts.SyntaxKind.GreaterThanToken,
  ts.SyntaxKind.GreaterThanEqualsToken,
]);

/** A test(...) call: its name literal and the call node. */
interface TestDeclaration {
  name: ts.StringLiteral | ts.NoSubstitutionTemplateLiteral;
  call: ts.CallExpression;
}

/** One test file's share of an Inventory; fixtures counts its fixture literals. */
interface FileInventory {
  hits: Hit[];
  fixtures: number;
  copyOnly: CopyOnlyTest[];
}

/** Looks literals up in the production corpus; a miss scans all of it, so answers are memoized. */
export class Scanner {
  private readonly prod: string;
  private readonly memo = new Map<string, boolean>();

  constructor(prod: string) {
    this.prod = prod;
  }

  /** The file's targets: its copy-only tests whose names say they test wording. */
  scanFile(file: string, text: string): Target[] {
    return this.inventoryFile(file, text, false)
      .copyOnly.filter((t) => t.purpose === "copy")
      .map(({ file, test, line }) => ({ file, test, line }));
  }

  /** The file's copy-only tests, and its hits and fixture count when withHits is set. */
  inventoryFile(file: string, text: string, withHits = true): FileInventory {
    const kind = file.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS;
    const sf = ts.createSourceFile(file, text, ts.ScriptTarget.Latest, true, kind);
    const imported = copyImports(sf);
    const found: FileInventory = withHits ? this.copyHits(file, sf, imported) : { hits: [], fixtures: 0, copyOnly: [] };
    const visit = (node: ts.Node): void => {
      const decl = testDeclaration(node);
      if (decl === null) {
        ts.forEachChild(node, visit);
        return;
      }
      if (this.isCopyOnly(decl, sf, imported)) {
        const test = rawText(decl.name, sf);
        found.copyOnly.push({
          file,
          test,
          line: lineOf(decl.call, sf),
          purpose: COPY_PURPOSE_NAME.test(test) ? "copy" : "behavior",
        });
      }
    };
    visit(sf);
    return found;
  }

  /**
   * Every by-text query and text matcher in the file whose argument is
   * production copy or an imported copy constant, and how many literals were
   * fixture data the tests supplied.
   */
  private copyHits(file: string, sf: ts.SourceFile, imported: Set<string>): FileInventory {
    const found: FileInventory = { hits: [], fixtures: 0, copyOnly: [] };
    walk(sf, (node) => {
      const check = textCheck(node);
      if (check === null) return;
      const { call, arg, isQuery } = check;
      const line = lineOf(ts.isPropertyAccessExpression(call.expression) ? call.expression.name : call.expression, sf);
      if (ts.isIdentifier(arg)) {
        if (imported.has(arg.text)) found.hits.push({ file, line, category: "ts-constant", literal: arg.text });
        return;
      }
      const probe = literalProbe(arg, sf);
      if (probe === null || contractShaped(probe)) return;
      if (!this.inProd(probe)) {
        found.fixtures++;
        return;
      }
      const category = isQuery && insideClick(call) ? "ts-locator" : "ts-assert";
      found.hits.push({ file, line, category, literal: probe });
    });
    return found;
  }

  private isCopyOnly(decl: TestDeclaration, sf: ts.SourceFile, imported: Set<string>): boolean {
    const checks = findChecks(decl.call);
    if (checks.length === 0 || checks.some(checksBehavior)) return false;
    const supplied = suppliedText(decl, checks, sf);
    return checks.every((check) => this.isCopyClaim(check, supplied, sf, imported));
  }

  /**
   * A check (already known not to check behavior) that claims only that some
   * copy appears: none of its literals is text the test supplied, and it
   * compares output with production copy.
   */
  private isCopyClaim(check: ts.Node, supplied: string, sf: ts.SourceFile, imported: Set<string>): boolean {
    const isSupplied = (raw: string) => runeLength(raw) >= 3 && supplied.includes(raw);
    if (stringLiterals(check).some((lit) => isSupplied(rawText(lit, sf)))) return false;
    return copyProbes(check, sf, imported).some(
      (probe) => probe === IMPORTED_COPY || (!contractShaped(probe) && this.inProd(probe)),
    );
  }

  private inProd(lit: string): boolean {
    let found = this.memo.get(lit);
    if (found === undefined) {
      found = this.prod.includes(lit);
      this.memo.set(lit, found);
    }
    return found;
  }
}

function lineOf(node: ts.Node, sf: ts.SourceFile): number {
  return sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1;
}

/**
 * A text check: a by-text or by-title query, or a text matcher, with the
 * argument it compares. The audit's copy claims and the inventory's hits both
 * read checks through this, so they agree on what one is.
 */
function textCheck(node: ts.Node): { call: ts.CallExpression; arg: ts.Expression; isQuery: boolean } | null {
  if (!ts.isCallExpression(node)) return null;
  const name = calleeName(node) ?? "";
  const isQuery = TEXT_QUERY.test(name);
  const isMatcher = ts.isPropertyAccessExpression(node.expression) && TEXT_MATCHERS.has(name);
  const arg = node.arguments[0];
  if (arg === undefined || !(isQuery || isMatcher)) return null;
  return { call: node, arg, isQuery };
}

/** The text a query or matcher argument stands for: a string's raw text, a regex's literal core. */
function literalProbe(arg: ts.Expression, sf: ts.SourceFile): string | null {
  if (ts.isStringLiteral(arg) || ts.isNoSubstitutionTemplateLiteral(arg)) return rawText(arg, sf).trim();
  if (ts.isRegularExpressionLiteral(arg)) return regexCore(arg.text.slice(1, arg.text.lastIndexOf("/"))).trim();
  return null;
}

/** The node is an argument, however deep, of a click: fireEvent.x(...), user.x(...), press/click/act(...). */
function insideClick(node: ts.Node): boolean {
  for (let p = node.parent; p !== undefined && !ts.isBlock(p) && !ts.isSourceFile(p); p = p.parent) {
    if (!ts.isCallExpression(p)) continue;
    const callee = p.expression;
    if (CLICK_CALLS.has(calleeName(p) ?? "")) return true;
    if (
      ts.isPropertyAccessExpression(callee) &&
      ts.isIdentifier(callee.expression) &&
      CLICK_RECEIVERS.has(callee.expression.text)
    ) {
      return true;
    }
  }
  return false;
}

/**
 * The test(...) call node is a test declaration: test or it, with any of
 * .only/.skip/.concurrent, called directly or through .each(table) (typed or
 * not, or with a tagged-template table), with a literal name.
 */
function testDeclaration(node: ts.Node): TestDeclaration | null {
  if (!ts.isCallExpression(node)) return null;
  const callee = node.expression;
  const isEachTable =
    (ts.isCallExpression(callee) && isEach(callee.expression)) ||
    (ts.isTaggedTemplateExpression(callee) && isEach(callee.tag));
  if (!isEachTable && !isTestFunction(callee)) return null;
  const name = node.arguments[0];
  if (name === undefined || !(ts.isStringLiteral(name) || ts.isNoSubstitutionTemplateLiteral(name))) return null;
  return { name, call: node };
}

function isEach(e: ts.Expression): boolean {
  return ts.isPropertyAccessExpression(e) && e.name.text === "each" && isTestFunction(e.expression);
}

function isTestFunction(e: ts.Expression): boolean {
  if (ts.isIdentifier(e)) return e.text === "test" || e.text === "it";
  return ts.isPropertyAccessExpression(e) && TEST_MODIFIERS.has(e.name.text) && isTestFunction(e.expression);
}

/** The name a call is made through: `f(...)` or `x.f(...)`. */
function calleeName(call: ts.CallExpression): string | null {
  const e = call.expression;
  if (ts.isIdentifier(e)) return e.text;
  if (ts.isPropertyAccessExpression(e)) return e.name.text;
  return null;
}

/**
 * The checks inside a test: each expect(...), expect.soft(...) or
 * expect.poll(...) chain, and each getBy or findBy query outside one (a query
 * throws when nothing matches, so on its own it is a claim). A check nested
 * inside another belongs to the outer one.
 */
function findChecks(call: ts.CallExpression): ts.Node[] {
  const roots: ts.Node[] = [];
  for (const arg of call.arguments.slice(1)) {
    walk(arg, (node) => {
      if (!ts.isCallExpression(node)) return;
      const name = calleeName(node);
      if (startsAssertion(node) || (name !== null && CHECK_QUERY.test(name))) {
        roots.push(chainTop(node));
      }
    });
  }
  return roots.filter(
    (check, i) =>
      roots.indexOf(check) === i &&
      !roots.some((outer) => outer !== check && outer.pos <= check.pos && check.end <= outer.end),
  );
}

/** Starts an assertion chain on its own: `expect(x)`, `expect.soft(x)`, `expect.poll(fn)`. */
const EXPECT_CHAIN_VARIANTS = new Set(["soft", "poll"]);

function startsAssertion(call: ts.CallExpression): boolean {
  const e = call.expression;
  if (ts.isIdentifier(e)) return e.text === "expect";
  return (
    ts.isPropertyAccessExpression(e) &&
    ts.isIdentifier(e.expression) &&
    e.expression.text === "expect" &&
    EXPECT_CHAIN_VARIANTS.has(e.name.text)
  );
}

/**
 * The whole member/call chain a call starts: `expect(x).not.toBe(y)` for
 * `expect(x)`, looking through the parentheses, `as`, `satisfies` and `!`
 * wrappers that leave the chain unchanged: `(expect(x) as T).toBe(y)`.
 */
function chainTop(node: ts.Node): ts.Node {
  let top = node;
  for (;;) {
    const p = top.parent;
    const continues =
      ((ts.isPropertyAccessExpression(p) || ts.isElementAccessExpression(p) || ts.isCallExpression(p)) &&
        p.expression === top) ||
      ts.isNonNullExpression(p) ||
      ts.isParenthesizedExpression(p) ||
      ts.isAsExpression(p) ||
      ts.isSatisfiesExpression(p) ||
      ts.isTypeAssertionExpression(p);
    if (!continues) return top;
    top = p;
  }
}

/** A check that reads a count, structure or state, asserts on a spy, or finds by role. */
function checksBehavior(check: ts.Node): boolean {
  let found = false;
  walk(check, (node) => {
    if (ts.isPropertyAccessExpression(node) && BEHAVIOR_PROPERTIES.has(node.name.text)) found = true;
    if (ts.isCallExpression(node)) {
      const name = calleeName(node) ?? "";
      found ||=
        BEHAVIOR_CALLS.has(name) ||
        BEHAVIOR_CALL_PREFIXES.some((prefix) => name.startsWith(prefix)) ||
        ROLE_QUERY.test(name) ||
        (NUMERIC_MATCHERS.has(name) && node.arguments[0] !== undefined && isNumber(node.arguments[0]));
    }
    if (ts.isBinaryExpression(node) && COMPARISONS.has(node.operatorToken.kind)) {
      found ||= isNumber(node.left) || isNumber(node.right);
    }
    return !found;
  });
  return found;
}

function isNumber(e: ts.Expression): boolean {
  if (ts.isPrefixUnaryExpression(e) && e.operator === ts.SyntaxKind.MinusToken) return isNumber(e.operand);
  return ts.isNumericLiteral(e);
}

/** Stands for a copy constant imported from code under test. */
const IMPORTED_COPY = Symbol("imported copy");

/**
 * What the check compares with copy: the first argument of a by-text query or
 * a text matcher, as literal text (a regex's literal core), or IMPORTED_COPY
 * for an imported copy constant.
 */
function copyProbes(check: ts.Node, sf: ts.SourceFile, imported: Set<string>): (string | typeof IMPORTED_COPY)[] {
  const probes: (string | typeof IMPORTED_COPY)[] = [];
  walk(check, (node) => {
    const arg = textCheck(node)?.arg;
    if (arg === undefined) return;
    const probe = literalProbe(arg, sf);
    if (probe !== null) probes.push(probe);
    else if (ts.isIdentifier(arg) && imported.has(arg.text)) probes.push(IMPORTED_COPY);
  });
  return probes;
}

/**
 * The text the test supplies: every literal in the declaration outside its
 * checks, .each table rows included, minus the test's name.
 */
function suppliedText(decl: TestDeclaration, checks: ts.Node[], sf: ts.SourceFile): string {
  const parts: string[] = [];
  const skip = new Set<ts.Node>([decl.name, ...checks]);
  walk(decl.call, (node) => {
    if (skip.has(node)) return false;
    // Raw source text, escapes as written, the form check literals are
    // compared in.
    if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) {
      parts.push(rawText(node, sf));
    } else if (ts.isTemplateHead(node) || ts.isTemplateMiddle(node) || ts.isTemplateTail(node)) {
      parts.push(node.rawText ?? node.text);
    } else if (ts.isJsxText(node)) {
      parts.push(node.text);
    }
  });
  return parts.join("\n");
}

function stringLiterals(root: ts.Node): (ts.StringLiteral | ts.NoSubstitutionTemplateLiteral)[] {
  const found: (ts.StringLiteral | ts.NoSubstitutionTemplateLiteral)[] = [];
  walk(root, (node) => {
    if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) found.push(node);
  });
  return found;
}

/** Visits root and its descendants depth-first; returning false from visit skips a node's children. */
function walk(root: ts.Node, visit: (node: ts.Node) => boolean | undefined): void {
  const step = (node: ts.Node): void => {
    if (visit(node) !== false) ts.forEachChild(node, step);
  };
  step(root);
}

function runeLength(s: string): number {
  return [...s].length;
}

/** A string literal's source text between its quotes, escapes as written. */
function rawText(lit: ts.StringLiteral | ts.NoSubstitutionTemplateLiteral, sf: ts.SourceFile): string {
  return lit.getText(sf).slice(1, -1);
}

/** The identifiers the file imports from code under test (not test support) whose names say they are copy. */
function copyImports(sf: ts.SourceFile): Set<string> {
  const names = new Set<string>();
  for (const stmt of sf.statements) {
    if (!ts.isImportDeclaration(stmt) || !ts.isStringLiteral(stmt.moduleSpecifier)) continue;
    const src = stmt.moduleSpecifier.text;
    if (src.includes("testing") || src.startsWith("vitest") || src.includes("@testing-library")) continue;
    const bindings = stmt.importClause?.namedBindings;
    if (bindings === undefined || !ts.isNamedImports(bindings)) continue;
    for (const el of bindings.elements) {
      if (COPY_IDENT.test(el.name.text)) names.add(el.name.text);
    }
  }
  return names;
}

/** An identifier, path, flag or other machine-contract value. */
const CONTRACT_TOKEN = /^[-\p{L}\p{N}_.:/@#=%+]*$/u;
/** A capitalized word ("Welcome", "Retry"): a label, not an identifier. */
const LABEL_WORD = /^[A-Z][a-z]+[.!?…]?$/u;
/** Shaped like code, shell commands, markup or wire syntax: a format contract even with spaces. */
const CODE_LIKE =
  /[{};=<>"`$\\]|^(?:cat|wc|uname|id|mkdir|rm|ls|sh|bash|git|go|npm|npx|test|printf|echo|chmod|mv|cp|tar|export|func|type|package|import|const|var|set|exec|cd|env|sudo|kill|pgrep|ps|tmux|ssh|curl|make)\b/;

/** A literal that is a machine contract (or too short or letterless to be wording), not copy. */
export function contractShaped(lit: string): boolean {
  if (runeLength(lit) < 3 || !/[A-Za-z]/.test(lit) || CODE_LIKE.test(lit)) return true;
  return CONTRACT_TOKEN.test(lit) && !LABEL_WORD.test(lit);
}

/**
 * The longest run of literal text in a regex, so it can be looked up as copy.
 * An escaped character is literal text; a class escape (`\d`) or an
 * unescaped metacharacter ends the run. Ties go to the first run.
 */
export function regexCore(rx: string): string {
  const runs: string[] = [];
  let run = "";
  const chars = [...rx];
  for (let i = 0; i < chars.length; i++) {
    const ch = chars[i] ?? "";
    if (ch === "\\") {
      i++;
      const next = chars[i];
      if (next === undefined) continue;
      if ("dDsSwWbB".includes(next)) {
        runs.push(run);
        run = "";
      } else {
        run += next;
      }
    } else if ("^$.*+?()[]{}|".includes(ch)) {
      runs.push(run);
      run = "";
    } else {
      run += ch;
    }
  }
  runs.push(run);
  let best = runs[0] ?? "";
  for (const r of runs) {
    if (runeLength(r) > runeLength(best)) best = r;
  }
  return best;
}

if (import.meta.main) {
  const args = argv.slice(2);
  const flag = (name: string): string | undefined => {
    const i = args.indexOf(name);
    return i >= 0 ? args[i + 1] : undefined;
  };
  const root = flag("--root");
  if (root === undefined) throw new Error("usage: node copyonly.ts --root REPO [--path PREFIX]");
  // The wire shape internal/devtool/copyonly's Report decodes.
  const { hits, fixtures, copyOnly } = inventoryRepo(root, flag("--path") ?? "");
  stdout.write(JSON.stringify({ hits, fixtures, copy_only: copyOnly }));
}
