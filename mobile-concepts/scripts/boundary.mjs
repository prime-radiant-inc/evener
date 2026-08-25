import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

/** @typedef {{ code: string, file: string, detail: string }} BoundaryViolation */

export const CANONICAL_CSP = "default-src 'self'; connect-src 'none'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; font-src 'self' data:; media-src 'self'; object-src 'none'; frame-src 'none'; base-uri 'none'; form-action 'none'";

export const NETWORK_API_PATTERNS = Object.freeze([
  { name: "fetch", pattern: /\bfetch\s*\(/ },
  { name: "XMLHttpRequest", pattern: /\bXMLHttpRequest\b/ },
  { name: "WebSocket", pattern: /\bWebSocket\b/ },
  { name: "EventSource", pattern: /\bEventSource\b/ },
  { name: "navigator.sendBeacon", pattern: /\bnavigator\s*\.\s*sendBeacon\s*\(/ },
]);

export const BROWSER_CAPABILITY_PATTERNS = Object.freeze([
  { code: "media-capture", name: "media capture", pattern: /\b(?:navigator\s*\.\s*mediaDevices|getUserMedia)\b/ },
  { code: "speech-recognition", name: "speech recognition", pattern: /\b(?:SpeechRecognition|webkitSpeechRecognition)\b/ },
  { code: "speech-synthesis", name: "speech synthesis", pattern: /\b(?:speechSynthesis|SpeechSynthesisUtterance)\b/ },
  { code: "audio-context", name: "audio context", pattern: /\b(?:AudioContext|webkitAudioContext)\b/ },
  { code: "file-access", name: "file picker", pattern: /\b(?:showOpenFilePicker|showSaveFilePicker|showDirectoryPicker)\s*\(/ },
  { code: "file-access", name: "file input or capture", pattern: /<input\b[^>]*\b(?:type\s*=\s*["']?file\b|capture(?:\s*=|\s|>))/i },
  { code: "geolocation", name: "geolocation", pattern: /\b(?:navigator\s*\.\s*)?geolocation\b/ },
  { code: "notification-push", name: "notifications or push", pattern: /\b(?:Notification|PushManager|showNotification|serviceWorker)\b/ },
  { code: "haptics", name: "vibration", pattern: /\bnavigator\s*\.\s*vibrate\s*\(/ },
]);

const SOURCE_EXTENSIONS = new Set([".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx"]);
const ASSET_EXTENSIONS = new Set([".css", ".html", ".htm", ".json", ".svg", ".webmanifest", ".xml"]);
const CODE_PRIORITY = new Map([
  ["malformed-config", -1],
  ["product-name", 0],
  ["identifier", 1],
  ["offline-csp", 2],
]);

export function violation(code, file, detail) {
  return { code, file, detail };
}

function sorted(violations) {
  return violations.sort((left, right) =>
    left.file.localeCompare(right.file) ||
    (CODE_PRIORITY.get(left.code) ?? 100) - (CODE_PRIORITY.get(right.code) ?? 100) ||
    left.code.localeCompare(right.code) ||
    left.detail.localeCompare(right.detail),
  );
}

function addMatch(violations, code, file, detail, pattern, text) {
  if (pattern.test(text)) violations.push(violation(code, file, detail));
}

function importSpecifiers(text) {
  const specifiers = [];
  const pattern = /(?:\b(?:import|export)\s+(?:[^;]*?\s+from\s+)?|\brequire\s*\()\s*["']([^"']+)["']/g;
  for (const match of text.matchAll(pattern)) specifiers.push(match[1]);
  return specifiers;
}

function isProductionMobileSpecifier(file, specifier) {
  const normalized = specifier.replaceAll("\\", "/");
  if (/^(?:@mobile|mobile|\/mobile)(?:\/|$)/.test(normalized)) return true;
  let base = path.posix.join("mobile-concepts", path.posix.dirname(file));
  let target = normalized;
  if (/^(?:@|~)\//.test(normalized)) {
    base = "mobile-concepts/src";
    target = normalized.slice(2);
  } else if (!normalized.startsWith(".")) {
    return false;
  }
  const resolved = path.posix.normalize(path.posix.join(base, target));
  return resolved === "mobile" || resolved.startsWith("mobile/");
}

function isExecutable(file) {
  return SOURCE_EXTENSIONS.has(path.extname(file).toLowerCase());
}

function isAsset(file) {
  return ASSET_EXTENSIONS.has(path.extname(file).toLowerCase());
}

/** Scan one executable source or textual asset. */
export function scanText(file, text) {
  const violations = [];
  const executable = isExecutable(file);
  const asset = isAsset(file);
  const html = /\.html?$/i.test(file);
  let usesNetworkApi = false;

  if (executable || html) {
    for (const item of NETWORK_API_PATTERNS) {
      if (item.pattern.test(text)) {
        usesNetworkApi = true;
        violations.push(violation("network-api", file, `${item.name} is forbidden in the offline concept lab`));
      }
    }
    for (const item of BROWSER_CAPABILITY_PATTERNS) {
      addMatch(violations, item.code, file, `${item.name} capability is forbidden`, item.pattern, text);
    }
  }

  if (executable) {
    for (const specifier of importSpecifiers(text)) {
      if (isProductionMobileSpecifier(file, specifier)) {
        violations.push(violation("production-mobile-import", file, `import resolves into production mobile/: ${specifier}`));
      }
      if (specifier.startsWith("@tauri-apps/plugin-")) {
        violations.push(violation("production-plugin", file, `Tauri plugin is forbidden: ${specifier}`));
      }
    }
    addMatch(violations, "native-invoke", file, "Tauri invoke is forbidden", /(?:["']@tauri-apps\/api\/core["']|\binvoke\s*\()/, text);
    const usesRemoteImport = /\bimport\s*\(\s*["'](?:https?:)?\/\//i.test(text);
    if (usesRemoteImport) violations.push(violation("remote-import", file, "dynamic remote import is forbidden"));
    if (!usesNetworkApi && !usesRemoteImport) {
      addMatch(violations, "remote-url", file, "HTTP(S) runtime literal is forbidden", /["'`]https?:\/\//i, text);
    }
    addMatch(violations, "credential-fixture", file, "credential-shaped fixture key is forbidden", /(?:["']?(?:token|authorizationUrl|apiKey|secret)["']?\s*:|\b(?:token|authorizationUrl|apiKey|secret)\s*=)/i, text);
  }

  if (/\.(?:json|webmanifest)$/i.test(file)) {
    addMatch(violations, "credential-fixture", file, "credential-shaped fixture key is forbidden", /["'](?:token|authorizationUrl|apiKey|secret)["']\s*:/i, text);
  }

  if (executable || asset) {
    addMatch(violations, "remote-asset", file, "remote HTML, CSS, or SVG asset is forbidden", /(?:\b(?:src|href|xlink:href|poster)\s*=\s*["']\s*(?:https?:)?\/\/|\burl\(\s*["']?\s*(?:https?:)?\/\/)/i, text);
    if (/\.(?:json|webmanifest)$/i.test(file)) {
      addMatch(violations, "remote-asset", file, "remote asset metadata is forbidden", /["'](?:https?:)?\/\//i, text);
    }
  }

  return sorted(violations);
}

function excludedSource(relative) {
  const normalized = relative.replaceAll("\\", "/");
  const base = path.posix.basename(normalized);
  return /\.test\.[^.]+$/.test(base) || normalized === "src/test/setup.ts" || normalized.startsWith("src/test/");
}

function walk(directory) {
  const files = [];
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const target = path.join(directory, entry.name);
    if (entry.isDirectory()) files.push(...walk(target));
    else if (entry.isFile()) files.push(target);
  }
  return files;
}

function scanCapability(file, text) {
  const violations = [];
  let config;
  try {
    config = JSON.parse(text);
  } catch {
    return [violation("malformed-capability", file, "capability file is not valid JSON")];
  }
  const permissions = Array.isArray(config.permissions) ? config.permissions : [];
  for (const permission of permissions) {
    if (permission !== "core:default") {
      violations.push(violation("product-permission", file, `product permission is forbidden: ${String(permission)}`));
    }
  }
  return violations;
}

function scanRust(file, text) {
  const violations = [];
  const handlers = /generate_handler!\s*\[([^\]]*)\]/gs.exec(text);
  if (handlers?.[1].trim()) violations.push(violation("invoke-handler", file, "native invoke handlers are forbidden"));
  addMatch(violations, "invoke-handler", file, "native invoke handlers are forbidden", /#\s*\[\s*tauri::command\b|\.invoke_handler\s*\(/, text);
  addMatch(violations, "production-plugin", file, "Tauri plugin initialization is forbidden", /\btauri_plugin_[A-Za-z0-9_]+\b/, text);
  return violations;
}

/** Scan the concept package rooted at root. */
export function scanSource(root) {
  const violations = [];
  const candidates = [];
  for (const relativeRoot of ["src", "src-tauri/src", "src-tauri/capabilities"]) {
    const absolute = path.join(root, relativeRoot);
    try {
      candidates.push(...walk(absolute));
    } catch (error) {
      if (error?.code !== "ENOENT") throw error;
    }
  }
  const index = path.join(root, "index.html");
  try {
    readFileSync(index);
    candidates.push(index);
  } catch (error) {
    if (error?.code !== "ENOENT") throw error;
  }

  for (const absolute of candidates) {
    const relative = path.relative(root, absolute).replaceAll("\\", "/");
    if (excludedSource(relative)) continue;
    const extension = path.extname(relative).toLowerCase();
    if (!SOURCE_EXTENSIONS.has(extension) && !ASSET_EXTENSIONS.has(extension) && extension !== ".rs" && extension !== ".json") continue;
    const text = readFileSync(absolute, "utf8");
    if (relative.startsWith("src-tauri/capabilities/") && extension === ".json") {
      violations.push(...scanCapability(relative, text));
    } else if (extension === ".rs") {
      violations.push(...scanRust(relative, text));
    } else {
      violations.push(...scanText(relative, text));
    }
  }
  return sorted(violations);
}

function canonicalCsp(value) {
  if (typeof value !== "string") return null;
  const directives = new Map();
  for (const raw of value.split(";")) {
    const parts = raw.trim().split(/\s+/).filter(Boolean);
    if (parts.length === 0) continue;
    if (directives.has(parts[0])) return null;
    directives.set(parts[0], [...parts.slice(1)].sort().join(" "));
  }
  return [...directives.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([name, sources]) => `${name} ${sources}`.trim()).join(";");
}

function hasRemoteUrl(value) {
  if (typeof value === "string") return /(?:https?:)?\/\//i.test(value);
  if (Array.isArray(value)) return value.some(hasRemoteUrl);
  if (value && typeof value === "object") return Object.values(value).some(hasRemoteUrl);
  return false;
}

function findKey(value, wanted) {
  if (!value || typeof value !== "object") return false;
  for (const [key, child] of Object.entries(value)) {
    if (wanted.test(key)) return true;
    if (findKey(child, wanted)) return true;
  }
  return false;
}

/** Validate parsed tauri.conf.json and, when supplied, the separately parsed HTML CSP. */
export function validateTauriConfig(config, htmlCsp) {
  const violations = [];
  const validObject = config !== null && typeof config === "object" && !Array.isArray(config);
  if (!validObject) violations.push(violation("malformed-config", "src-tauri/tauri.conf.json", "configuration must be an object"));
  const value = validObject ? config : {};
  if (value.productName !== "Evener Concepts") violations.push(violation("product-name", "src-tauri/tauri.conf.json", "productName must be Evener Concepts"));
  if (value.identifier !== "com.primeradiant.evener.concepts") violations.push(violation("identifier", "src-tauri/tauri.conf.json", "identifier must be com.primeradiant.evener.concepts"));
  const tauriCsp = value.app?.security?.csp;
  if (canonicalCsp(tauriCsp) !== canonicalCsp(CANONICAL_CSP)) {
    violations.push(violation("offline-csp", "src-tauri/tauri.conf.json", "Tauri CSP must equal the canonical offline CSP"));
  }
  if (htmlCsp !== undefined && canonicalCsp(htmlCsp) !== canonicalCsp(tauriCsp)) {
    violations.push(violation("html-csp-mismatch", "index.html", "HTML and Tauri CSP directives must be canonically equal"));
  }
  const frontendDist = value.build?.frontendDist;
  if (frontendDist !== undefined && (typeof frontendDist !== "string" || !frontendDist || /^(?:[a-z]+:|\/\/)/i.test(frontendDist))) {
    violations.push(violation("frontend-dist", "src-tauri/tauri.conf.json", "frontendDist must be a local path"));
  }
  if (value.app?.security && Object.hasOwn(value.app.security, "assetProtocol")) {
    violations.push(violation("asset-protocol", "src-tauri/tauri.conf.json", "asset protocol configuration is forbidden"));
  }
  if (hasRemoteUrl(value)) violations.push(violation("remote-config-url", "src-tauri/tauri.conf.json", "remote configuration URL is forbidden"));
  if (findKey(value, /^updater$/i)) violations.push(violation("updater", "src-tauri/tauri.conf.json", "updater configuration is forbidden"));
  if (value.plugins && typeof value.plugins === "object" && Object.keys(value.plugins).length > 0) {
    violations.push(violation("production-plugin", "src-tauri/tauri.conf.json", "Tauri product plugins are forbidden"));
  }
  if (findKey(value.app, /^permissions$/i)) violations.push(violation("product-permission", "src-tauri/tauri.conf.json", "product permissions are forbidden"));
  if (findKey(value, /^entitlements?$/i)) violations.push(violation("forbidden-entitlement", "src-tauri/tauri.conf.json", "native entitlements are forbidden"));
  return sorted(violations);
}

/** Validate Cargo.toml without accepting any runtime/build dependency except Tauri. */
export function validateRustManifest(manifestText) {
  const file = "src-tauri/Cargo.toml";
  if (typeof manifestText !== "string") return [violation("malformed-manifest", file, "manifest must be text")];
  const violations = [];
  let section = "";
  let sawTable = false;
  for (const [index, raw] of manifestText.split(/\r?\n/).entries()) {
    const line = raw.replace(/\s+#.*$/, "").trim();
    if (!line || line.startsWith("#")) continue;
    const table = /^\[{1,2}([^\]]+)\]{1,2}$/.exec(line);
    if (table) {
      section = table[1].trim();
      sawTable = true;
      continue;
    }
    const assignment = /^([A-Za-z0-9_-]+)\s*=\s*(.+)$/.exec(line);
    if (!assignment) return [violation("malformed-manifest", file, `invalid TOML syntax on line ${index + 1}`)];
    if (section === "dependencies" && assignment[1] !== "tauri") {
      violations.push(violation("rust-dependency", file, `runtime dependency is forbidden: ${assignment[1]}`));
    } else if (section === "build-dependencies" && assignment[1] !== "tauri-build") {
      violations.push(violation("rust-dependency", file, `build dependency is forbidden: ${assignment[1]}`));
    } else if (/^(?:dependencies|build-dependencies)\./.test(section)) {
      violations.push(violation("rust-dependency", file, `dependency subtable is forbidden: ${section}`));
    }
  }
  if (!sawTable) return [violation("malformed-manifest", file, "manifest has no TOML tables")];
  return sorted(violations);
}

export function extractHtmlCsp(html) {
  const meta = /<meta\b(?=[^>]*\bhttp-equiv\s*=\s*["']Content-Security-Policy["'])[^>]*>/i.exec(html)?.[0];
  if (!meta) return null;
  const content = /\bcontent\s*=\s*(?:"([^"]*)"|'([^']*)')/i.exec(meta);
  return content?.[1] ?? content?.[2] ?? null;
}

function runCli(root) {
  const violations = scanSource(root);
  let config;
  try {
    config = JSON.parse(readFileSync(path.join(root, "src-tauri/tauri.conf.json"), "utf8"));
  } catch (error) {
    violations.push(violation("malformed-config", "src-tauri/tauri.conf.json", error.message));
  }
  let htmlCsp = null;
  try {
    htmlCsp = extractHtmlCsp(readFileSync(path.join(root, "index.html"), "utf8"));
  } catch (error) {
    violations.push(violation("malformed-html", "index.html", error.message));
  }
  violations.push(...validateTauriConfig(config, htmlCsp));
  try {
    violations.push(...validateRustManifest(readFileSync(path.join(root, "src-tauri/Cargo.toml"), "utf8")));
  } catch (error) {
    violations.push(violation("malformed-manifest", "src-tauri/Cargo.toml", error.message));
  }
  const result = sorted(violations);
  for (const item of result) console.error(`${item.file} [${item.code}] ${item.detail}`);
  if (result.length) process.exitCode = 1;
}

const currentFile = fileURLToPath(import.meta.url);
if (process.argv[1] && path.resolve(process.argv[1]) === currentFile) {
  runCli(path.resolve(path.dirname(currentFile), ".."));
}
