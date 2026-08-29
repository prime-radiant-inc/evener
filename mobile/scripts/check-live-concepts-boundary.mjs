// check-live-concepts-boundary.mjs — deterministic isolation checker for the
// mobile/src/live-concepts tree. Live-concept renderers must never depend on
// the concept-lab's fixture/scenario/prototype/synthetic infrastructure,
// must not open transports directly, and must not ship unscoped CSS.
//
// Exports checkLiveConceptBoundary(root): Promise<Violation[]> where root is
// the mobile root (the directory containing src/live-concepts).
// Walks .ts/.tsx/.css without following symlinks, resolves every relative
// import against its containing file, and returns sorted {code, file, detail}
// records. The CLI checks src/live-concepts, prints one line per violation,
// and exits 1 when nonempty.

import { readdir, readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const CODE_EXTENSIONS = new Set([".ts", ".tsx", ".css"]);
const CONCEPT_IDS = ["stillwater", "constellation", "field-notes"];
const PLATFORM_PRESENTATION = "src/ui/platformPresentation.ts";

// Forbidden symbol patterns that indicate concept-lab fixture/scenario/
// prototype/synthetic/lab infrastructure. Word boundaries prevent matching
// substrings of longer identifiers.
const FORBIDDEN_SYMBOLS = [
  { pattern: /\bPrototypeState\b/, detail: "PrototypeState type" },
  { pattern: /\bScenarioId\b/, detail: "ScenarioId type" },
  { pattern: /\.fixture\b/, detail: "fixture property access" },
  { pattern: /\bcanonicalFixture\b/, detail: "canonicalFixture reference" },
  { pattern: /\bsourceFixture\b/, detail: "sourceFixture reference" },
  { pattern: /\bsyntheticTurn\b/, detail: "syntheticTurn control" },
  { pattern: /\bonOpenLabControls\b/, detail: "onOpenLabControls lab hook" },
  { pattern: /\bonOpenLab\b/, detail: "onOpenLab lab hook" },
];

// Direct transport construction that live-concepts must not use; they go
// through the NativeBridge instead.
const FORBIDDEN_TRANSPORTS = [
  { pattern: /\bnew\s+WebSocket\s*\(/, detail: "new WebSocket() transport" },
  {
    pattern: /\bnew\s+EventSource\s*\(/,
    detail: "new EventSource() transport",
  },
  {
    pattern: /\bnew\s+XMLHttpRequest\s*\(/,
    detail: "new XMLHttpRequest() transport",
  },
];

// Bare/aliased specifiers that pull in transport runtimes.
const FORBIDDEN_TRANSPORT_PREFIXES = ["@tauri-apps/"];

// Bare/aliased specifier path segments naming concept-lab
// fixture/scenario/runtime modules.
const FORBIDDEN_BARE_SEGMENTS = new Set([
  "fixtures",
  "scenario",
  "scenarios",
  "runtime",
]);

/**
 * @typedef {Object} Violation
 * @property {string} code
 * @property {string} file
 * @property {string} detail
 */

async function walkCodeFiles(dir, files = []) {
  let entries;
  try {
    entries = await readdir(dir, { withFileTypes: true });
  } catch (err) {
    if (err.code === "ENOENT") return files;
    throw err;
  }
  for (const entry of entries) {
    // Never follow symlinks.
    if (entry.isSymbolicLink()) continue;
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      await walkCodeFiles(fullPath, files);
    } else if (entry.isFile()) {
      const ext = path.extname(entry.name);
      if (CODE_EXTENSIONS.has(ext)) {
        files.push(fullPath);
      }
    }
  }
  return files;
}

function extractImportSpecifiers(source) {
  const specifiers = new Set();
  // `from "..."` — covers `import { x } from "y"` and `export { x } from "y"`.
  const fromRe = /\bfrom\s+['"]([^'"]+)['"]/g;
  for (const m of source.matchAll(fromRe)) {
    specifiers.add(m[1]);
  }
  // Side-effect `import "..."`.
  const sideEffectRe = /\bimport\s+['"]([^'"]+)['"]/g;
  for (const m of source.matchAll(sideEffectRe)) {
    specifiers.add(m[1]);
  }
  // Dynamic `import("...")`.
  const dynamicRe = /\bimport\s*\(\s*['"]([^'"]+)['"]\s*\)/g;
  for (const m of source.matchAll(dynamicRe)) {
    specifiers.add(m[1]);
  }
  return specifiers;
}

// Remove comments without treating comment markers inside quoted literals as
// comments. Import/selector parsing runs on this representation so a disabled
// example cannot trigger a rule while import strings remain available.
function stripComments(source) {
  let result = "";
  let i = 0;
  let quote = null;
  while (i < source.length) {
    const ch = source[i];
    if (quote !== null) {
      result += ch;
      if (ch === "\\") {
        if (i + 1 < source.length) result += source[i + 1];
        i += 2;
        continue;
      }
      if (ch === quote) quote = null;
      i++;
      continue;
    }
    if (ch === '"' || ch === "'" || ch === "`") {
      quote = ch;
      result += ch;
      i++;
      continue;
    }
    if (ch === "/" && source[i + 1] === "/") {
      while (i < source.length && source[i] !== "\n") i++;
      continue;
    }
    if (ch === "/" && source[i + 1] === "*") {
      i += 2;
      while (
        i < source.length &&
        !(source[i] === "*" && source[i + 1] === "/")
      ) {
        if (source[i] === "\n") result += "\n";
        i++;
      }
      i += 2;
      continue;
    }
    result += ch;
    i++;
  }
  return result;
}

function isRelative(specifier) {
  return specifier.startsWith("./") || specifier.startsWith("../");
}

function pathContainsSegment(filePath, segment) {
  return path.normalize(filePath).split(path.sep).includes(segment);
}

function checkImports(specifiers, fileDir, fileRel, violations) {
  for (const specifier of specifiers) {
    if (isRelative(specifier)) {
      const resolved = path.resolve(fileDir, specifier);
      if (pathContainsSegment(resolved, "mobile-concepts")) {
        violations.push({
          code: "forbidden-import",
          file: fileRel,
          detail: `relative import resolves under mobile-concepts: ${specifier}`,
        });
      }
    } else {
      // Bare or aliased specifier.
      if (FORBIDDEN_TRANSPORT_PREFIXES.some((p) => specifier.startsWith(p))) {
        violations.push({
          code: "forbidden-transport",
          file: fileRel,
          detail: `import from transport module: ${specifier}`,
        });
      }
      const segments = specifier.split("/");
      if (segments.some((seg) => FORBIDDEN_BARE_SEGMENTS.has(seg))) {
        violations.push({
          code: "forbidden-import",
          file: fileRel,
          detail: `bare/aliased specifier names fixture/scenario/runtime module: ${specifier}`,
        });
      }
    }
  }
}

function checkSymbols(source, fileRel, violations) {
  for (const { pattern, detail } of FORBIDDEN_SYMBOLS) {
    if (pattern.test(source)) {
      violations.push({ code: "forbidden-symbol", file: fileRel, detail });
    }
  }
}

function checkTransports(source, fileRel, violations) {
  for (const { pattern, detail } of FORBIDDEN_TRANSPORTS) {
    if (pattern.test(source)) {
      violations.push({ code: "forbidden-transport", file: fileRel, detail });
    }
  }
}

function checkConversationSkin(source, specifiers, fileRel, violations) {
  if (path.basename(fileRel) !== "ConversationSkin.tsx") return;
  const rules = [
    {
      matches: specifiers.has("@tanstack/react-virtual"),
      detail: "ConversationSkin must not import @tanstack/react-virtual",
    },
    {
      matches: /\bMobileTimelineItem\b/.test(source),
      detail: "ConversationSkin must not reference MobileTimelineItem",
    },
    {
      matches: [...specifiers].some((specifier) =>
        /(?:^|\/)(?:state|services)\/conversation(?:$|\/)/.test(specifier),
      ),
      detail:
        "ConversationSkin must not import a conversation store or service",
    },
    {
      matches: /\bitems\s*\.\s*map\s*\(/.test(source),
      detail: "ConversationSkin must not map conversation items",
    },
    {
      matches:
        /<(?:button|input|textarea|select)\b/i.test(source) ||
        /<a\b[^>]*\bhref\s*=/i.test(source),
      detail: "ConversationSkin must not render interactive controls",
    },
    {
      matches:
        /\bon(?:Click|Change|Submit|Send|Steer|Queue|Interrupt|Open|Close|Back|Voice|Work)\w*\s*(?:\??\s*:|\()/.test(
          source,
        ),
      detail: "ConversationSkin must not declare control callback props",
    },
    {
      matches: /data-live-concept-scroller/.test(source),
      detail: "ConversationSkin must not declare data-live-concept-scroller",
    },
  ];
  for (const rule of rules) {
    if (!rule.matches) continue;
    violations.push({
      code: "conversation-skin-boundary",
      file: fileRel,
      detail: rule.detail,
    });
  }
}

function checkConversationImportDirection(
  source,
  specifiers,
  fileRel,
  violations,
) {
  if (!fileRel.startsWith("src/live-concepts/conversation/")) return;
  if (/\.(?:test|spec)\.[cm]?[jt]sx?$/.test(fileRel)) return;
  const rules = [
    {
      matches: [...specifiers].some(
        (specifier) =>
          specifier === "../contract" || specifier === "../contract.ts",
      ),
      detail: "conversation primitive must not import ../contract",
    },
    {
      matches: /\bLiveConceptState\b/.test(source),
      detail: "conversation primitive must not reference LiveConceptState",
    },
    {
      matches: /\bLiveConceptIntent\b/.test(source),
      detail: "conversation primitive must not reference LiveConceptIntent",
    },
  ];
  for (const rule of rules) {
    if (!rule.matches) continue;
    violations.push({
      code: "conversation-import-direction",
      file: fileRel,
      detail: rule.detail,
    });
  }
}

function checkParentConversationImports(specifiers, fileRel, violations) {
  if (fileRel !== "src/live-concepts/contract.ts") return;
  for (const required of [
    "./conversation/contract",
    "./conversation/primitives",
  ]) {
    if (specifiers.has(required)) continue;
    violations.push({
      code: "conversation-import-direction",
      file: fileRel,
      detail: `parent contract must import ${required}`,
    });
  }
}

function checkRequiredConversationSkin(source, fileRel, violations) {
  for (const concept of CONCEPT_IDS) {
    if (fileRel !== `src/live-concepts/${concept}/index.ts`) continue;
    if (/\bconversationSkin\s*:/.test(source)) return;
    violations.push({
      code: "missing-conversation-skin",
      file: fileRel,
      detail: "live concept module must provide conversationSkin",
    });
    return;
  }
}

function isScopedSelector(selectorList) {
  // .concept-* scoped selectors (existing broad acceptance).
  if (selectorList.includes(".concept-")) return true;
  // Production-owned shared roots: every comma-separated part must start with
  // the same root class (or its BEM element/modifier) and must not contain
  // global element selectors.
  for (const root of [
    ".live-concept-switcher",
    ".live-conversation-frame",
    ".live-conversation-evidence",
  ]) {
    if (!selectorList.includes(root)) continue;
    const parts = selectorList.split(",");
    for (const part of parts) {
      const trimmed = part.trim();
      if (!trimmed.startsWith(root)) {
        return false;
      }
      const suffix = trimmed.slice(root.length);
      if (suffix !== "" && !/^(?:__|--|[\s:[.#>+~])/.test(suffix)) {
        return false;
      }
      if (/\b(body|html)\b/.test(trimmed) || trimmed.includes("*")) {
        return false;
      }
    }
    return true;
  }
  return false;
}

function findUnscopedCssSelectors(css) {
  const noComments = css.replace(/\/\*[\s\S]*?\*\//g, "");
  const unscoped = [];
  let depth = 0;
  let current = "";
  for (let i = 0; i < noComments.length; i++) {
    const ch = noComments[i];
    if (ch === "{") {
      if (depth === 0) {
        const selector = current.trim();
        // Skip at-rules (@media, @keyframes, @font-face, …).
        if (
          selector &&
          !selector.startsWith("@") &&
          !isScopedSelector(selector)
        ) {
          unscoped.push(selector);
        }
      }
      depth++;
      current = "";
    } else if (ch === "}") {
      depth--;
      current = "";
    } else {
      current += ch;
    }
  }
  return unscoped;
}

function checkCss(source, fileRel, violations) {
  for (const selector of findUnscopedCssSelectors(source)) {
    violations.push({
      code: "unscoped-css",
      file: fileRel,
      detail: `unscoped selector: ${selector}`,
    });
  }
  if (
    !/^src\/live-concepts\/(?:stillwater|constellation|field-notes)\//.test(
      fileRel,
    )
  ) {
    return;
  }
  const css = stripComments(source);
  const emitted = new Set();
  const rulePattern = /([^{}]+)\{([^{}]*)\}/g;
  for (const match of css.matchAll(rulePattern)) {
    const selector = match[1].trim();
    const declarations = match[2];
    const conversationOnly =
      /conversation-skin/.test(selector) ||
      /data-surface\s*=\s*["']?conversation/.test(selector);
    if (!conversationOnly) continue;
    if (
      /composer/i.test(selector) &&
      /\bposition\s*:\s*(?:fixed|sticky)\b/i.test(declarations)
    ) {
      emitted.add(
        "conversation composer must not use fixed/sticky positioning",
      );
    }
    if (/\boverflow-y\s*:/i.test(declarations)) {
      emitted.add("active conversation must not own overflow-y");
    }
    if (/data-live-concept-scroller/.test(selector)) {
      emitted.add(
        "conversation skin must not declare data-live-concept-scroller",
      );
    }
  }
  for (const detail of emitted) {
    violations.push({
      code: "conversation-css-boundary",
      file: fileRel,
      detail,
    });
  }
}

function checkViewportOwnership(source, fileRel, violations) {
  if (/\.(?:test|spec)\.[cm]?[jt]sx?$/.test(fileRel)) return;
  if (/--visual-viewport-height/.test(source)) {
    violations.push({
      code: "forbidden-viewport-token",
      file: fileRel,
      detail: "--visual-viewport-height is forbidden",
    });
  }
  if (fileRel === PLATFORM_PRESENTATION) return;
  if (
    /\bvisualViewport\b/.test(source) &&
    /\baddEventListener\s*\(/.test(source)
  ) {
    violations.push({
      code: "viewport-ownership",
      file: fileRel,
      detail:
        "only src/ui/platformPresentation.ts may listen to visualViewport",
    });
  }
  for (const token of ["--viewport-height", "--keyboard-inset"]) {
    if (!source.includes(token) || !/\bsetProperty\s*\(/.test(source)) continue;
    violations.push({
      code: "viewport-ownership",
      file: fileRel,
      detail: `only src/ui/platformPresentation.ts may write ${token}`,
    });
  }
}

// Strip string and template literals so symbol/transport checks don't match
// identifiers that only appear inside quoted strings (e.g. negative type
// assertions like .not.toHaveProperty("sourceFixture")). Import extraction
// runs against the raw source separately.
function stripLiterals(source) {
  let result = "";
  let i = 0;
  while (i < source.length) {
    const ch = source[i];
    if (ch === '"' || ch === "'" || ch === "`") {
      const quote = ch;
      result += quote;
      i++;
      while (i < source.length) {
        if (source[i] === "\\") {
          i += 2;
          continue;
        }
        if (source[i] === quote) {
          i++;
          break;
        }
        i++;
      }
      result += quote;
      continue;
    }
    if (ch === "/" && source[i + 1] === "/") {
      while (i < source.length && source[i] !== "\n") i++;
      continue;
    }
    if (ch === "/" && source[i + 1] === "*") {
      i += 2;
      while (i < source.length && !(source[i] === "*" && source[i + 1] === "/"))
        i++;
      i += 2;
      continue;
    }
    result += ch;
    i++;
  }
  return result;
}

/**
 * @param {string} root — mobile root (directory containing src/live-concepts)
 * @returns {Promise<Violation[]>} sorted violation records
 */
export async function checkLiveConceptBoundary(root) {
  const liveConceptsDir = path.join(root, "src", "live-concepts");
  const files = await walkCodeFiles(liveConceptsDir);
  const sourceFiles = await walkCodeFiles(path.join(root, "src"));
  /** @type {Violation[]} */
  const violations = [];
  for (const file of sourceFiles) {
    const fileRel = path.relative(root, file).split(path.sep).join("/");
    const source = stripComments(await readFile(file, "utf8"));
    checkViewportOwnership(source, fileRel, violations);
  }
  for (const file of files) {
    const ext = path.extname(file);
    const fileRel = path.relative(root, file).split(path.sep).join("/");
    const source = stripComments(await readFile(file, "utf8"));
    if (ext === ".css") {
      checkCss(source, fileRel, violations);
    } else {
      const specifiers = extractImportSpecifiers(source);
      checkImports(specifiers, path.dirname(file), fileRel, violations);
      const code = stripLiterals(source);
      checkSymbols(code, fileRel, violations);
      checkTransports(code, fileRel, violations);
      checkConversationSkin(code, specifiers, fileRel, violations);
      checkConversationImportDirection(code, specifiers, fileRel, violations);
      checkParentConversationImports(specifiers, fileRel, violations);
      checkRequiredConversationSkin(code, fileRel, violations);
    }
  }
  violations.sort((a, b) => {
    if (a.file !== b.file) return a.file < b.file ? -1 : 1;
    if (a.code !== b.code) return a.code < b.code ? -1 : 1;
    if (a.detail !== b.detail) return a.detail < b.detail ? -1 : 1;
    return 0;
  });
  return violations;
}

async function main() {
  const scriptDir = path.dirname(fileURLToPath(import.meta.url));
  const mobileRoot = path.resolve(scriptDir, "..");
  const violations = await checkLiveConceptBoundary(mobileRoot);
  for (const v of violations) {
    console.log(`${v.code}: ${v.file}: ${v.detail}`);
  }
  if (violations.length > 0) {
    process.exitCode = 1;
  }
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  await main();
}
