import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { parse as parseJavaScript } from "@babel/parser";
import traverseJavaScript from "@babel/traverse";
import { parse as parseHtml } from "parse5";
import postcss from "postcss";
import { SaxesParser } from "saxes";
import { parse as parseToml } from "smol-toml";

/** @typedef {{ code: string, file: string, detail: string }} BoundaryViolation */

export const CANONICAL_CSP =
  "default-src 'self'; connect-src 'none'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; font-src 'self' data:; media-src 'self'; object-src 'none'; frame-src 'none'; base-uri 'none'; form-action 'none'";

export const NETWORK_API_PATTERNS = Object.freeze([
  { name: "fetch", kind: "call" },
  { name: "XMLHttpRequest", kind: "construct" },
  { name: "WebSocket", kind: "construct" },
  { name: "EventSource", kind: "construct" },
  { name: "navigator.sendBeacon", kind: "call" },
]);

export const BROWSER_CAPABILITY_PATTERNS = Object.freeze([
  { code: "media-capture", name: "media capture" },
  { code: "speech-recognition", name: "speech recognition" },
  { code: "speech-synthesis", name: "speech synthesis" },
  { code: "audio-context", name: "audio context" },
  { code: "file-access", name: "file picker or input" },
  { code: "geolocation", name: "geolocation" },
  { code: "notification-push", name: "notifications or push" },
  { code: "haptics", name: "vibration" },
]);

const SOURCE_EXTENSIONS = new Set([
  ".js",
  ".jsx",
  ".mjs",
  ".cjs",
  ".ts",
  ".tsx",
]);
const ASSET_EXTENSIONS = new Set([
  ".css",
  ".html",
  ".htm",
  ".json",
  ".svg",
  ".webmanifest",
  ".xml",
]);
const CODE_PRIORITY = new Map([
  ["malformed-config", -1],
  ["product-name", 0],
  ["identifier", 1],
  ["offline-csp", 2],
  ["frontend-dist", 3],
]);
const REMOTE_URL = /(?:https?:)?\/\//i;
const CREDENTIAL_KEYS = new Set([
  "token",
  "authorizationurl",
  "apikey",
  "secret",
]);

export function violation(code, file, detail) {
  return { code, file, detail };
}

function compareCodePoints(left, right) {
  return left < right ? -1 : left > right ? 1 : 0;
}

function sorted(violations) {
  return violations.sort(
    (left, right) =>
      compareCodePoints(left.file, right.file) ||
      (CODE_PRIORITY.get(left.code) ?? 100) -
        (CODE_PRIORITY.get(right.code) ?? 100) ||
      compareCodePoints(left.code, right.code) ||
      compareCodePoints(left.detail, right.detail),
  );
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

function walkAst(node, visitor, parent = null) {
  if (!node || typeof node !== "object") return;
  if (typeof node.type === "string") visitor(node, parent);
  for (const [key, child] of Object.entries(node)) {
    if (
      key === "loc" ||
      key === "start" ||
      key === "end" ||
      key === "comments" ||
      key === "tokens"
    )
      continue;
    if (Array.isArray(child)) {
      for (const item of child) walkAst(item, visitor, node);
    } else if (child && typeof child === "object") {
      walkAst(child, visitor, node);
    }
  }
}

function literalText(node) {
  if (node?.type === "StringLiteral") return node.value;
  if (node?.type === "TemplateLiteral") {
    let value = node.quasis[0]?.value?.cooked ?? "";
    for (const [index, expression] of node.expressions.entries()) {
      const part = literalText(expression);
      if (part === null) return null;
      value += part + (node.quasis[index + 1]?.value?.cooked ?? "");
    }
    return value;
  }
  if (node?.type === "BinaryExpression" && node.operator === "+") {
    const left = literalText(node.left);
    const right = literalText(node.right);
    return left === null || right === null ? null : left + right;
  }
  return null;
}

function memberPath(node) {
  if (!node) return null;
  if (node.type === "Identifier") return node.name;
  if (node.type === "ThisExpression") return "this";
  if (
    node.type !== "MemberExpression" &&
    node.type !== "OptionalMemberExpression"
  )
    return null;
  const object = memberPath(node.object);
  const property = node.computed
    ? literalText(node.property)
    : node.property?.name;
  return object && property ? `${object}.${property}` : null;
}

function propertyName(node) {
  if (!node) return null;
  if (node.computed) return literalText(node.key);
  return node.key?.name ?? node.key?.value ?? null;
}

function browserGlobalName(value) {
  return value?.replace(/^(?:window|globalThis|self)\./, "") ?? value;
}

function jsxAttributeText(node) {
  if (!node) return "";
  if (node.type === "JSXExpressionContainer")
    return literalText(node.expression);
  return literalText(node);
}

function pathRoot(value) {
  return value?.split(".")[0] ?? null;
}

function oneLine(message) {
  return String(message).replace(/\s+/g, " ").trim();
}

function parseApplicationSource(file, text) {
  try {
    return parseJavaScript(text, {
      sourceType: "unambiguous",
      errorRecovery: false,
      plugins: ["typescript", "jsx", "decorators-legacy", "importAttributes"],
    });
  } catch (error) {
    return violation(
      "malformed-source",
      file,
      `source parser rejected input: ${error.message}`,
    );
  }
}

function scanJavaScript(file, text) {
  const ast = parseApplicationSource(file, text);
  if (ast.code) return [ast];

  const violations = [];
  const tauriInvokeBindings = new Set();
  const tauriNamespaces = new Set();
  const found = new Map();
  const record = (code, detail) => {
    if (!found.has(code)) found.set(code, violation(code, file, detail));
  };

  walkAst(ast, (node) => {
    if (node.type !== "ImportDeclaration") return;
    const specifier = literalText(node.source);
    if (!specifier) return;
    if (isProductionMobileSpecifier(file, specifier)) {
      record(
        "production-mobile-import",
        `import resolves into production mobile/: ${specifier}`,
      );
    }
    if (specifier.startsWith("@tauri-apps/plugin-")) {
      record("production-plugin", `Tauri plugin is forbidden: ${specifier}`);
    }
    if (specifier === "@tauri-apps/api/core") {
      for (const binding of node.specifiers) {
        if (
          binding.type === "ImportSpecifier" &&
          (binding.imported?.name ?? binding.imported?.value) === "invoke"
        ) {
          tauriInvokeBindings.add(binding.local.name);
        } else if (binding.type === "ImportNamespaceSpecifier") {
          tauriNamespaces.add(binding.local.name);
        }
      }
    }
  });

  const scanExecutableNode = (nodePath) => {
    const { node } = nodePath;
    const isUnboundBrowserReference = (referencePath) => {
      const reference = memberPath(referencePath?.node);
      const root = pathRoot(reference);
      return (
        Boolean(root) &&
        referencePath.isReferenced() &&
        !referencePath.scope.getBinding(root)
      );
    };
    if (
      [
        "ImportDeclaration",
        "ExportNamedDeclaration",
        "ExportAllDeclaration",
      ].includes(node.type)
    ) {
      const specifier = literalText(node.source);
      if (specifier && isProductionMobileSpecifier(file, specifier)) {
        record(
          "production-mobile-import",
          `import resolves into production mobile/: ${specifier}`,
        );
      }
      return;
    }
    if (
      node.type === "ImportExpression" ||
      (node.type === "CallExpression" && node.callee?.type === "Import")
    ) {
      const specifier = literalText(node.source ?? node.arguments?.[0]);
      if (specifier === null) {
        record(
          "dynamic-import",
          "dynamic import target must reduce to a static safe local string",
        );
        return;
      }
      if (specifier && isProductionMobileSpecifier(file, specifier)) {
        record(
          "production-mobile-import",
          `dynamic import resolves into production mobile/: ${specifier}`,
        );
      }
      if (specifier && REMOTE_URL.test(specifier))
        record("remote-import", "dynamic remote import is forbidden");
      if (
        /^[A-Za-z][A-Za-z0-9+.-]*:/.test(specifier) &&
        !REMOTE_URL.test(specifier)
      )
        record(
          "dynamic-import",
          "dynamic import protocol is not a safe local target",
        );
      if (specifier === "@tauri-apps/api/core")
        record("native-invoke", "dynamic Tauri core import is forbidden");
      if (specifier.startsWith("@tauri-apps/plugin-"))
        record(
          "production-plugin",
          `dynamic Tauri plugin is forbidden: ${specifier}`,
        );
      return;
    }

    if (
      node.type === "CallExpression" ||
      node.type === "OptionalCallExpression"
    ) {
      const calleePath = nodePath.get("callee");
      const callee = memberPath(calleePath.node);
      const globalCallee = browserGlobalName(callee);
      const networkRule = NETWORK_API_PATTERNS.find(
        (item) =>
          item.kind === "call" &&
          item.name === globalCallee &&
          isUnboundBrowserReference(calleePath),
      );
      if (networkRule) {
        record(
          "network-api",
          `${networkRule.name} is forbidden in the offline concept lab`,
        );
      }
      if (
        tauriInvokeBindings.has(callee) ||
        [...tauriNamespaces].some((name) => callee === `${name}.invoke`)
      ) {
        record("native-invoke", "Tauri invoke is forbidden");
      }
      if (
        [
          "showOpenFilePicker",
          "showSaveFilePicker",
          "showDirectoryPicker",
        ].includes(globalCallee) &&
        isUnboundBrowserReference(calleePath)
      ) {
        record("file-access", "file picker capability is forbidden");
      }
      if (
        isUnboundBrowserReference(calleePath) &&
        (globalCallee === "getUserMedia" ||
          globalCallee?.startsWith("navigator.mediaDevices"))
      ) {
        record("media-capture", "media capture capability is forbidden");
      }
      if (
        globalCallee?.startsWith("speechSynthesis") &&
        isUnboundBrowserReference(calleePath)
      )
        record("speech-synthesis", "speech synthesis capability is forbidden");
      if (
        globalCallee?.startsWith("navigator.geolocation") &&
        isUnboundBrowserReference(calleePath)
      )
        record("geolocation", "geolocation capability is forbidden");
      if (
        isUnboundBrowserReference(calleePath) &&
        (globalCallee?.includes("showNotification") ||
          globalCallee?.startsWith("navigator.serviceWorker"))
      ) {
        record(
          "notification-push",
          "notifications or push capability is forbidden",
        );
      }
      if (
        globalCallee === "navigator.vibrate" &&
        isUnboundBrowserReference(calleePath)
      )
        record("haptics", "vibration capability is forbidden");
    }

    if (node.type === "NewExpression") {
      const constructorPath = nodePath.get("callee");
      const constructorName = memberPath(constructorPath.node);
      const globalConstructor = browserGlobalName(constructorName);
      const networkRule = NETWORK_API_PATTERNS.find(
        (item) =>
          item.kind === "construct" &&
          item.name === globalConstructor &&
          isUnboundBrowserReference(constructorPath),
      );
      if (networkRule) {
        record(
          "network-api",
          `${networkRule.name} is forbidden in the offline concept lab`,
        );
      }
      if (
        ["SpeechRecognition", "webkitSpeechRecognition"].includes(
          globalConstructor,
        ) &&
        isUnboundBrowserReference(constructorPath)
      ) {
        record(
          "speech-recognition",
          "speech recognition capability is forbidden",
        );
      }
      if (
        globalConstructor === "SpeechSynthesisUtterance" &&
        isUnboundBrowserReference(constructorPath)
      )
        record("speech-synthesis", "speech synthesis capability is forbidden");
      if (
        ["AudioContext", "webkitAudioContext"].includes(globalConstructor) &&
        isUnboundBrowserReference(constructorPath)
      )
        record("audio-context", "audio context capability is forbidden");
      if (
        globalConstructor === "Notification" &&
        isUnboundBrowserReference(constructorPath)
      )
        record(
          "notification-push",
          "notifications or push capability is forbidden",
        );
    }

    if (
      node.type === "VariableDeclarator" &&
      node.id?.type === "ObjectPattern"
    ) {
      const sourcePath = nodePath.get("init");
      const source = browserGlobalName(memberPath(sourcePath.node));
      if (!isUnboundBrowserReference(sourcePath)) return;
      for (const property of node.id.properties) {
        const key = String(propertyName(property) ?? "");
        if (
          source === "navigator" &&
          ["mediaDevices", "getUserMedia"].includes(key)
        )
          record("media-capture", "media capture capability is forbidden");
        if (
          (source === "navigator" && key === "sendBeacon") ||
          (["window", "globalThis", "self"].includes(source) &&
            ["fetch", "XMLHttpRequest", "WebSocket", "EventSource"].includes(
              key,
            ))
        )
          record(
            "network-api",
            `${key} is forbidden in the offline concept lab`,
          );
        if (source === "navigator" && key === "geolocation")
          record("geolocation", "geolocation capability is forbidden");
        if (
          (source === "navigator" && key === "serviceWorker") ||
          (["window", "globalThis", "self"].includes(source) &&
            ["Notification", "PushManager"].includes(key))
        )
          record(
            "notification-push",
            "notifications or push capability is forbidden",
          );
        if (source === "navigator" && key === "vibrate")
          record("haptics", "vibration capability is forbidden");
        if (
          ["window", "globalThis", "self"].includes(source) &&
          ["speechSynthesis", "SpeechSynthesisUtterance"].includes(key)
        )
          record(
            "speech-synthesis",
            "speech synthesis capability is forbidden",
          );
        if (
          ["window", "globalThis", "self"].includes(source) &&
          ["SpeechRecognition", "webkitSpeechRecognition"].includes(key)
        )
          record(
            "speech-recognition",
            "speech recognition capability is forbidden",
          );
        if (
          ["window", "globalThis", "self"].includes(source) &&
          ["AudioContext", "webkitAudioContext"].includes(key)
        )
          record("audio-context", "audio context capability is forbidden");
      }
    }

    const rawExpressionPath = memberPath(node);
    const expressionPath = browserGlobalName(rawExpressionPath);
    const isUnshadowedBrowserReference =
      nodePath.isReferenced() && isUnboundBrowserReference(nodePath);
    if (
      isUnshadowedBrowserReference &&
      NETWORK_API_PATTERNS.some((item) => item.name === expressionPath)
    )
      record(
        "network-api",
        `${expressionPath} is forbidden in the offline concept lab`,
      );
    if (
      isUnshadowedBrowserReference &&
      expressionPath?.startsWith("navigator.mediaDevices")
    )
      record("media-capture", "media capture capability is forbidden");
    if (
      isUnshadowedBrowserReference &&
      expressionPath?.startsWith("navigator.geolocation")
    )
      record("geolocation", "geolocation capability is forbidden");
    if (
      isUnshadowedBrowserReference &&
      ["speechSynthesis", "SpeechSynthesisUtterance"].includes(expressionPath)
    )
      record("speech-synthesis", "speech synthesis capability is forbidden");
    if (
      isUnshadowedBrowserReference &&
      ["SpeechRecognition", "webkitSpeechRecognition"].includes(expressionPath)
    )
      record(
        "speech-recognition",
        "speech recognition capability is forbidden",
      );
    if (
      isUnshadowedBrowserReference &&
      ["AudioContext", "webkitAudioContext"].includes(expressionPath)
    )
      record("audio-context", "audio context capability is forbidden");
    if (
      isUnshadowedBrowserReference &&
      [
        "showOpenFilePicker",
        "showSaveFilePicker",
        "showDirectoryPicker",
      ].includes(expressionPath)
    )
      record("file-access", "file picker capability is forbidden");
    if (
      isUnshadowedBrowserReference &&
      (expressionPath === "Notification" ||
        expressionPath?.startsWith("PushManager") ||
        expressionPath?.startsWith("navigator.serviceWorker"))
    )
      record(
        "notification-push",
        "notifications or push capability is forbidden",
      );
    if (isUnshadowedBrowserReference && expressionPath === "navigator.vibrate")
      record("haptics", "vibration capability is forbidden");

    if (
      ["ObjectProperty", "ObjectMethod", "ClassProperty"].includes(node.type)
    ) {
      const key = String(propertyName(node) ?? "").toLowerCase();
      if (CREDENTIAL_KEYS.has(key))
        record(
          "credential-fixture",
          `credential-shaped fixture key is forbidden: ${key}`,
        );
    }
    if (
      node.type === "JSXOpeningElement" &&
      node.name?.type === "JSXIdentifier" &&
      node.name.name.toLowerCase() === "input"
    ) {
      const attributes = new Map(
        node.attributes
          .filter(
            (attribute) =>
              attribute.type === "JSXAttribute" &&
              attribute.name?.type === "JSXIdentifier",
          )
          .map((attribute) => [
            attribute.name.name.toLowerCase(),
            jsxAttributeText(attribute.value),
          ]),
      );
      if (
        attributes.get("type")?.toLowerCase() === "file" ||
        attributes.has("capture")
      ) {
        record("file-access", "file input or capture capability is forbidden");
      }
    }
    if (
      node.type === "VariableDeclarator" &&
      node.id?.type === "Identifier" &&
      CREDENTIAL_KEYS.has(node.id.name.toLowerCase())
    ) {
      record(
        "credential-fixture",
        `credential-shaped fixture key is forbidden: ${node.id.name}`,
      );
    }
    if (["StringLiteral", "TemplateLiteral"].includes(node.type)) {
      const value = literalText(node);
      if (
        value &&
        REMOTE_URL.test(value) &&
        !found.has("network-api") &&
        !found.has("remote-import")
      ) {
        record("remote-url", "HTTP(S) runtime literal is forbidden");
      }
    }
  };
  traverseJavaScript(ast, { enter: scanExecutableNode });

  violations.push(...found.values());
  return sorted(violations);
}

function stripRustNonCode(text) {
  let output = "";
  let index = 0;
  let blockDepth = 0;
  while (index < text.length) {
    if (blockDepth > 0) {
      if (text.startsWith("/*", index)) {
        blockDepth += 1;
        index += 2;
        output += "  ";
        continue;
      }
      if (text.startsWith("*/", index)) {
        blockDepth -= 1;
        index += 2;
        output += "  ";
        continue;
      }
      output += text[index] === "\n" ? "\n" : " ";
      index += 1;
      continue;
    }
    if (text.startsWith("//", index)) {
      const end = text.indexOf("\n", index);
      if (end === -1) return output.padEnd(text.length, " ");
      output += `${" ".repeat(end - index)}\n`;
      index = end + 1;
      continue;
    }
    if (text.startsWith("/*", index)) {
      blockDepth = 1;
      output += "  ";
      index += 2;
      continue;
    }
    const raw = /^(?:br|r)(#+)?"/.exec(text.slice(index));
    if (raw) {
      const hashes = raw[1] ?? "";
      const close = `"${hashes}`;
      const end = text.indexOf(close, index + raw[0].length);
      const length =
        end === -1 ? text.length - index : end + close.length - index;
      output += text.slice(index, index + length).replace(/[^\n]/g, " ");
      index += length;
      continue;
    }
    if (text[index] === '"') {
      let end = index + 1;
      while (end < text.length) {
        if (text[end] === "\\") end += 2;
        else if (text[end++] === '"') break;
      }
      output += text.slice(index, end).replace(/[^\n]/g, " ");
      index = end;
      continue;
    }
    output += text[index++];
  }
  return output;
}

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function scanRust(file, text) {
  const code = stripRustNonCode(text);
  const violations = [];
  const commandAliases = new Set();
  const handlerAliases = new Set();
  const tauriAliases = new Set(["tauri"]);
  const bracedUseTrees = [
    ...code.matchAll(
      /\buse\s+(?:::)?\s*([A-Za-z_][A-Za-z0-9_]*)::\{([^}]*)\}\s*;/g,
    ),
  ];
  const crateAliasEdges = [
    ...code.matchAll(
      /\buse\s+(?:::)?\s*([A-Za-z_][A-Za-z0-9_]*)\s+as\s+([A-Za-z_][A-Za-z0-9_]*)\s*;/g,
    ),
    ...code.matchAll(
      /\bextern\s+crate\s+([A-Za-z_][A-Za-z0-9_]*)\s+as\s+([A-Za-z_][A-Za-z0-9_]*)\s*;/g,
    ),
  ].map((match) => ({ source: match[1], local: match[2] }));
  for (const match of bracedUseTrees) {
    for (const item of match[2].split(",").map((part) => part.trim())) {
      const selfImport = /^self(?:\s+as\s+([A-Za-z_][A-Za-z0-9_]*))?$/.exec(
        item,
      );
      if (selfImport)
        crateAliasEdges.push({
          source: match[1],
          local: selfImport[1] ?? match[1],
        });
    }
  }
  let foundCrateAlias = true;
  while (foundCrateAlias) {
    foundCrateAlias = false;
    for (const edge of crateAliasEdges) {
      if (tauriAliases.has(edge.source) && !tauriAliases.has(edge.local)) {
        tauriAliases.add(edge.local);
        foundCrateAlias = true;
      }
    }
  }
  for (const match of code.matchAll(
    /\buse\s+(?:::)?\s*([A-Za-z_][A-Za-z0-9_]*)::(?:\{([^}]*)\}|(command|generate_handler)(?:\s+as\s+([A-Za-z_][A-Za-z0-9_]*))?)\s*;/g,
  )) {
    if (!tauriAliases.has(match[1])) continue;
    const imports = match[2]
      ? match[2].split(",").map((item) => item.trim())
      : [`${match[3]}${match[4] ? ` as ${match[4]}` : ""}`];
    for (const item of imports) {
      const imported =
        /^(command|generate_handler)(?:\s+as\s+([A-Za-z_][A-Za-z0-9_]*))?$/.exec(
          item,
        );
      if (!imported) continue;
      const local = imported[2] ?? imported[1];
      (imported[1] === "command" ? commandAliases : handlerAliases).add(local);
    }
  }

  const recordHandler = (detail) =>
    violations.push(violation("invoke-handler", file, detail));
  for (const tauriAlias of tauriAliases) {
    const pattern = new RegExp(
      `#\\s*\\[\\s*(?<![A-Za-z0-9_])${escapeRegExp(tauriAlias)}::command\\b`,
      "g",
    );
    for (const _match of code.matchAll(pattern))
      recordHandler(
        `native Tauri command attribute is forbidden: ${tauriAlias}`,
      );
  }
  for (const alias of commandAliases) {
    const pattern = new RegExp(`#\\s*\\[\\s*${escapeRegExp(alias)}\\b`, "g");
    for (const _match of code.matchAll(pattern))
      recordHandler(`imported Tauri command attribute is forbidden: ${alias}`);
  }

  const scanHandlerMacros = (pattern, name) => {
    for (const match of code.matchAll(pattern)) {
      if (match[1].trim())
        recordHandler(`native invoke handler macro is forbidden: ${name}`);
    }
  };
  for (const tauriAlias of tauriAliases)
    scanHandlerMacros(
      new RegExp(
        `(?<![A-Za-z0-9_])${escapeRegExp(tauriAlias)}::generate_handler!\\s*\\[([^\\]]*)\\]`,
        "gs",
      ),
      `${tauriAlias}::generate_handler`,
    );
  for (const alias of handlerAliases) {
    scanHandlerMacros(
      new RegExp(
        `(?<![:A-Za-z0-9_])${escapeRegExp(alias)}!\\s*\\[([^\\]]*)\\]`,
        "gs",
      ),
      alias,
    );
  }
  for (const _match of code.matchAll(/\.invoke_handler\s*\(/g))
    recordHandler("native invoke_handler builder call is forbidden");
  if (/\btauri_plugin_[A-Za-z0-9_]+\b/.test(code)) {
    violations.push(
      violation(
        "production-plugin",
        file,
        "Tauri plugin initialization is forbidden",
      ),
    );
  }
  return sorted(violations);
}

function remoteAsset(file, detail) {
  return violation("remote-asset", file, detail);
}

function scanCss(file, text) {
  const violations = [];
  let root;
  try {
    root = postcss.parse(text, { from: file });
  } catch (error) {
    return [
      violation(
        "malformed-asset",
        file,
        `CSS parser rejected input: ${error.message}`,
      ),
    ];
  }
  root.walkAtRules((rule) => {
    if (rule.name.toLowerCase() === "import" && REMOTE_URL.test(rule.params)) {
      violations.push(remoteAsset(file, "remote CSS @import is forbidden"));
    }
  });
  root.walkDecls((declaration) => {
    if (REMOTE_URL.test(declaration.value))
      violations.push(
        remoteAsset(file, `remote CSS value is forbidden: ${declaration.prop}`),
      );
  });
  return sorted(violations);
}

function traverseHtml(node, visitor) {
  visitor(node);
  for (const child of node.childNodes ?? []) traverseHtml(child, visitor);
  if (node.content) traverseHtml(node.content, visitor);
}

function scanEmbeddedScript(file, text) {
  return scanJavaScript(file, text).map((item) =>
    item.code === "remote-url" || item.code === "remote-import"
      ? remoteAsset(file, "remote inline markup runtime URL is forbidden")
      : item,
  );
}

function scanHtmlMarkup(file, text) {
  const violations = [];
  const parseErrors = [];
  const document = parseHtml(text, {
    onParseError: (error) => parseErrors.push(error),
  });
  for (const error of parseErrors.filter(
    (item) => item.code !== "missing-doctype",
  )) {
    violations.push(
      violation(
        "malformed-asset",
        file,
        `HTML parser rejected input: ${error.code}`,
      ),
    );
  }
  traverseHtml(document, (node) => {
    for (const attribute of node.attrs ?? []) {
      if (attribute.name === "xmlns" || attribute.name.startsWith("xmlns:"))
        continue;
      if (REMOTE_URL.test(attribute.value))
        violations.push(
          remoteAsset(
            file,
            `remote markup attribute is forbidden: ${attribute.name}`,
          ),
        );
    }
    if (node.nodeName === "input") {
      const attrs = Object.fromEntries(
        (node.attrs ?? []).map((item) => [item.name, item.value]),
      );
      if (
        attrs.type?.toLowerCase() === "file" ||
        Object.hasOwn(attrs, "capture")
      ) {
        violations.push(
          violation(
            "file-access",
            file,
            "file input or capture capability is forbidden",
          ),
        );
      }
    }
    if (node.nodeName === "style") {
      violations.push(
        ...scanCss(
          file,
          (node.childNodes ?? []).map((item) => item.value ?? "").join(""),
        ),
      );
    }
    if (node.nodeName === "script") {
      const type = (node.attrs ?? []).find(
        (item) => item.name === "type",
      )?.value;
      if (
        !type ||
        /^(?:module|text\/javascript|application\/javascript)$/i.test(type)
      ) {
        violations.push(
          ...scanEmbeddedScript(
            file,
            (node.childNodes ?? []).map((child) => child.value ?? "").join(""),
          ),
        );
      }
    }
  });
  return sorted(violations);
}

function scanXmlMarkup(file, text) {
  const violations = [];
  const parseErrors = [];
  const frames = [];
  const parser = new SaxesParser({ xmlns: true });
  parser.on("error", (error) => parseErrors.push(oneLine(error.message)));
  parser.on("processinginstruction", (instruction) => {
    if (REMOTE_URL.test(instruction.body))
      violations.push(
        remoteAsset(
          file,
          `remote XML processing instruction is forbidden: ${instruction.target}`,
        ),
      );
  });
  parser.on("doctype", (doctype) => {
    if (REMOTE_URL.test(doctype))
      violations.push(remoteAsset(file, "remote XML doctype is forbidden"));
  });
  parser.on("opentag", (node) => {
    for (const attribute of Object.values(node.attributes)) {
      if (attribute.name === "xmlns" || attribute.prefix === "xmlns") continue;
      if (REMOTE_URL.test(attribute.value))
        violations.push(
          remoteAsset(
            file,
            `remote XML/SVG attribute is forbidden: ${attribute.name}`,
          ),
        );
    }
    frames.push({ name: node.local.toLowerCase(), text: "" });
  });
  const appendText = (value) => {
    if (frames.length) frames.at(-1).text += value;
  };
  parser.on("text", appendText);
  parser.on("cdata", appendText);
  parser.on("closetag", () => {
    const frame = frames.pop();
    if (!frame) return;
    if (frame.name === "style") violations.push(...scanCss(file, frame.text));
    if (frame.name === "script")
      violations.push(...scanEmbeddedScript(file, frame.text));
  });
  try {
    parser.write(text).close();
  } catch (error) {
    parseErrors.push(oneLine(error.message));
  }
  for (const detail of new Set(parseErrors))
    violations.push(
      violation(
        "malformed-asset",
        file,
        `XML parser rejected input: ${detail}`,
      ),
    );
  return sorted(violations);
}

function scanMetadata(file, text) {
  let value;
  try {
    value = JSON.parse(text);
  } catch (error) {
    return [
      violation(
        "malformed-asset",
        file,
        `asset metadata is not valid JSON: ${error.message}`,
      ),
    ];
  }
  const violations = [];
  const inspect = (child, key = "") => {
    if (typeof child === "string" && REMOTE_URL.test(child))
      violations.push(
        remoteAsset(file, `remote asset metadata is forbidden: ${key}`),
      );
    if (CREDENTIAL_KEYS.has(key.toLowerCase()))
      violations.push(
        violation(
          "credential-fixture",
          file,
          `credential-shaped fixture key is forbidden: ${key}`,
        ),
      );
    if (Array.isArray(child)) {
      child.forEach((item) => {
        inspect(item, key);
      });
    } else if (child && typeof child === "object") {
      Object.entries(child).forEach(([name, item]) => {
        inspect(item, name);
      });
    }
  };
  inspect(value);
  return sorted(violations);
}

/** Scan one executable source or production textual asset. */
export function scanText(file, text) {
  const extension = path.extname(file).toLowerCase();
  if (SOURCE_EXTENSIONS.has(extension)) return scanJavaScript(file, text);
  if (extension === ".css") return scanCss(file, text);
  if ([".html", ".htm"].includes(extension)) return scanHtmlMarkup(file, text);
  if ([".svg", ".xml"].includes(extension)) return scanXmlMarkup(file, text);
  if ([".json", ".webmanifest"].includes(extension))
    return scanMetadata(file, text);
  return [];
}

function excludedSource(relative) {
  const normalized = relative.replaceAll("\\", "/");
  const base = path.posix.basename(normalized);
  return (
    /\.test\.[^.]+$/.test(base) ||
    normalized === "src/test/setup.ts" ||
    normalized.startsWith("src/test/")
  );
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
  let config;
  try {
    config = JSON.parse(text);
  } catch (error) {
    return [
      violation(
        "malformed-capability",
        file,
        `capability file is not valid JSON: ${error.message}`,
      ),
    ];
  }
  if (!config || typeof config !== "object" || Array.isArray(config)) {
    return [
      violation(
        "malformed-capability",
        file,
        "capability must be a JSON object",
      ),
    ];
  }
  const violations = [];
  const allowedKeys = new Set([
    "$schema",
    "identifier",
    "description",
    "windows",
    "permissions",
  ]);
  for (const key of Object.keys(config)) {
    if (!allowedKeys.has(key))
      violations.push(
        violation(
          "product-permission",
          file,
          `capability field is forbidden: ${key}`,
        ),
      );
  }
  if (
    config.identifier !== "default" ||
    typeof config.description !== "string" ||
    !config.description
  ) {
    violations.push(
      violation(
        "malformed-capability",
        file,
        "capability requires identifier default and a nonempty description",
      ),
    );
  }
  if (
    Object.hasOwn(config, "$schema") &&
    (typeof config.$schema !== "string" ||
      !config.$schema ||
      REMOTE_URL.test(config.$schema))
  ) {
    violations.push(
      violation(
        "malformed-capability",
        file,
        "capability $schema must be a nonempty local string",
      ),
    );
  }
  if (
    !Array.isArray(config.windows) ||
    config.windows.length !== 1 ||
    config.windows[0] !== "main"
  ) {
    violations.push(
      violation(
        "product-permission",
        file,
        'capability windows must be exactly ["main"]',
      ),
    );
  }
  if (
    !Array.isArray(config.permissions) ||
    config.permissions.length !== 1 ||
    config.permissions[0] !== "core:default"
  ) {
    violations.push(
      violation(
        "product-permission",
        file,
        'capability permissions must be exactly ["core:default"]',
      ),
    );
  }
  const inspect = (value, key = "", depth = 0) => {
    if (
      /^remote$/i.test(key) ||
      (typeof value === "string" && REMOTE_URL.test(value))
    ) {
      violations.push(
        violation(
          "remote-capability",
          file,
          `remote capability surface is forbidden: ${key}`,
        ),
      );
    }
    if (/commands?/i.test(key))
      violations.push(
        violation(
          "command-capability",
          file,
          `command capability field is forbidden: ${key}`,
        ),
      );
    if (depth > 0 && /products?|plugins?/i.test(key))
      violations.push(
        violation(
          "product-permission",
          file,
          `product capability field is forbidden: ${key}`,
        ),
      );
    if (Array.isArray(value)) {
      value.forEach((item) => {
        inspect(item, key, depth + 1);
      });
    } else if (value && typeof value === "object") {
      Object.entries(value).forEach(([name, child]) => {
        inspect(child, name, depth + 1);
      });
    }
  };
  inspect(config);
  return sorted(violations);
}

/** Scan the concept package rooted at root. */
export function scanSource(root) {
  const violations = [];
  const candidates = [];
  for (const relativeRoot of [
    "src",
    "public",
    "src-tauri/src",
    "src-tauri/capabilities",
  ]) {
    try {
      candidates.push(...walk(path.join(root, relativeRoot)));
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
    if (
      !SOURCE_EXTENSIONS.has(extension) &&
      !ASSET_EXTENSIONS.has(extension) &&
      extension !== ".rs"
    )
      continue;
    const text = readFileSync(absolute, "utf8");
    if (relative.startsWith("src-tauri/capabilities/") && extension === ".json")
      violations.push(...scanCapability(relative, text));
    else if (extension === ".rs") violations.push(...scanRust(relative, text));
    else violations.push(...scanText(relative, text));
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
    directives.set(
      parts[0],
      [...parts.slice(1)].sort(compareCodePoints).join(" "),
    );
  }
  return [...directives.entries()]
    .sort(([a], [b]) => compareCodePoints(a, b))
    .map(([name, sources]) => `${name} ${sources}`.trim())
    .join(";");
}

function hasRemoteUrl(value) {
  if (typeof value === "string") return REMOTE_URL.test(value);
  if (Array.isArray(value)) return value.some(hasRemoteUrl);
  if (value && typeof value === "object")
    return Object.values(value).some(hasRemoteUrl);
  return false;
}

function findKey(value, wanted) {
  if (!value || typeof value !== "object") return false;
  for (const [key, child] of Object.entries(value)) {
    if (wanted.test(key) || findKey(child, wanted)) return true;
  }
  return false;
}

function localFrontendDist(value) {
  if (
    typeof value !== "string" ||
    !value.trim() ||
    path.posix.isAbsolute(value) ||
    path.win32.isAbsolute(value)
  )
    return false;
  const normalized = value.replaceAll("\\", "/");
  if (/^[a-z]+:/i.test(normalized)) return false;
  const packageRoot = "/concept-package";
  const resolved = path.posix.resolve(packageRoot, "src-tauri", normalized);
  return resolved.startsWith(`${packageRoot}/`) && resolved !== packageRoot;
}

/** Validate parsed tauri.conf.json and, when supplied, the separately parsed HTML CSP. */
export function validateTauriConfig(config, htmlCsp) {
  const violations = [];
  const validObject =
    config !== null && typeof config === "object" && !Array.isArray(config);
  if (!validObject)
    violations.push(
      violation(
        "malformed-config",
        "src-tauri/tauri.conf.json",
        "configuration must be an object",
      ),
    );
  const value = validObject ? config : {};
  if (value.productName !== "Evener Concepts")
    violations.push(
      violation(
        "product-name",
        "src-tauri/tauri.conf.json",
        "productName must be Evener Concepts",
      ),
    );
  if (value.identifier !== "com.primeradiant.evener.concepts")
    violations.push(
      violation(
        "identifier",
        "src-tauri/tauri.conf.json",
        "identifier must be com.primeradiant.evener.concepts",
      ),
    );
  const tauriCsp = value.app?.security?.csp;
  if (canonicalCsp(tauriCsp) !== canonicalCsp(CANONICAL_CSP))
    violations.push(
      violation(
        "offline-csp",
        "src-tauri/tauri.conf.json",
        "Tauri CSP must equal the canonical offline CSP",
      ),
    );
  if (htmlCsp !== undefined && canonicalCsp(htmlCsp) !== canonicalCsp(tauriCsp))
    violations.push(
      violation(
        "html-csp-mismatch",
        "index.html",
        "HTML and Tauri CSP directives must be canonically equal",
      ),
    );
  if (!localFrontendDist(value.build?.frontendDist))
    violations.push(
      violation(
        "frontend-dist",
        "src-tauri/tauri.conf.json",
        "frontendDist must resolve inside the concept package",
      ),
    );
  if (value.app?.security && Object.hasOwn(value.app.security, "assetProtocol"))
    violations.push(
      violation(
        "asset-protocol",
        "src-tauri/tauri.conf.json",
        "asset protocol configuration is forbidden",
      ),
    );
  if (hasRemoteUrl(value))
    violations.push(
      violation(
        "remote-config-url",
        "src-tauri/tauri.conf.json",
        "remote configuration URL is forbidden",
      ),
    );
  if (findKey(value, /^updater$/i))
    violations.push(
      violation(
        "updater",
        "src-tauri/tauri.conf.json",
        "updater configuration is forbidden",
      ),
    );
  if (
    value.plugins &&
    typeof value.plugins === "object" &&
    Object.keys(value.plugins).length > 0
  )
    violations.push(
      violation(
        "production-plugin",
        "src-tauri/tauri.conf.json",
        "Tauri product plugins are forbidden",
      ),
    );
  if (findKey(value.app, /^permissions$/i))
    violations.push(
      violation(
        "product-permission",
        "src-tauri/tauri.conf.json",
        "product permissions are forbidden",
      ),
    );
  if (findKey(value, /^entitlements?$/i))
    violations.push(
      violation(
        "forbidden-entitlement",
        "src-tauri/tauri.conf.json",
        "native entitlements are forbidden",
      ),
    );
  return sorted(violations);
}

function localRustDependencyPath(value) {
  if (
    typeof value !== "string" ||
    !value.trim() ||
    path.posix.isAbsolute(value) ||
    path.win32.isAbsolute(value) ||
    REMOTE_URL.test(value)
  )
    return false;
  const packageRoot = "/concept-package";
  const resolved = path.posix.resolve(
    packageRoot,
    "src-tauri",
    value.replaceAll("\\", "/"),
  );
  return resolved.startsWith(`${packageRoot}/`);
}

function validateAllowedDependency(
  table,
  dependency,
  declaration,
  file,
  violations,
) {
  const malformed = (detail) =>
    violations.push(violation("malformed-manifest", file, detail));
  const forbiddenSource = (detail) =>
    violations.push(violation("rust-dependency-source", file, detail));
  if (typeof declaration === "string") {
    if (!declaration.trim())
      malformed(`${table}.${dependency} version is empty`);
    return;
  }
  if (
    !declaration ||
    typeof declaration !== "object" ||
    Array.isArray(declaration)
  ) {
    malformed(`${table}.${dependency} must be a string or dependency table`);
    return;
  }
  const allowedKeys = new Set([
    "version",
    "features",
    "default-features",
    "optional",
    "package",
    "path",
  ]);
  for (const key of Object.keys(declaration)) {
    if (!allowedKeys.has(key))
      forbiddenSource(
        `${table}.${dependency} declaration key is forbidden: ${key}`,
      );
  }
  if (
    Object.hasOwn(declaration, "package") &&
    declaration.package !== dependency
  )
    violations.push(
      violation(
        "rust-dependency",
        file,
        `${table}.${dependency} may not rename package ${String(declaration.package)}`,
      ),
    );
  for (const key of ["git", "registry", "workspace"]) {
    if (Object.hasOwn(declaration, key))
      forbiddenSource(`${table}.${dependency} source is forbidden: ${key}`);
  }
  if (
    Object.hasOwn(declaration, "path") &&
    !localRustDependencyPath(declaration.path)
  )
    forbiddenSource(
      `${table}.${dependency} path must remain in the concept package`,
    );
  if (
    !Object.hasOwn(declaration, "version") &&
    !Object.hasOwn(declaration, "path")
  )
    malformed(
      `${table}.${dependency} requires a reviewed version or local path`,
    );
  if (
    Object.hasOwn(declaration, "version") &&
    (typeof declaration.version !== "string" || !declaration.version.trim())
  )
    malformed(`${table}.${dependency}.version must be a nonempty string`);
  if (
    Object.hasOwn(declaration, "features") &&
    (!Array.isArray(declaration.features) ||
      declaration.features.some((feature) => typeof feature !== "string"))
  )
    malformed(`${table}.${dependency}.features must be a string array`);
  for (const key of ["default-features", "optional"]) {
    if (
      Object.hasOwn(declaration, key) &&
      typeof declaration[key] !== "boolean"
    )
      malformed(`${table}.${dependency}.${key} must be boolean`);
  }
}

function inspectDependencyTables(value, file, violations, ancestry = []) {
  if (!value || typeof value !== "object" || Array.isArray(value)) return;
  for (const [key, child] of Object.entries(value)) {
    const lower = key.toLowerCase();
    if (["patch", "replace"].includes(lower)) {
      violations.push(
        violation(
          "rust-dependency-source",
          file,
          `Cargo ${[...ancestry, key].join(".")} overrides are forbidden`,
        ),
      );
      continue;
    }
    if (
      ["dependencies", "build-dependencies", "dev-dependencies"].includes(lower)
    ) {
      if (!child || typeof child !== "object" || Array.isArray(child)) {
        violations.push(
          violation(
            "malformed-manifest",
            file,
            `${[...ancestry, key].join(".")} must be a dependency table`,
          ),
        );
        continue;
      }
      const allowed =
        lower === "dependencies"
          ? new Set(["tauri"])
          : lower === "build-dependencies"
            ? new Set(["tauri-build"])
            : new Set();
      for (const [dependency, declaration] of Object.entries(child)) {
        if (!allowed.has(dependency))
          violations.push(
            violation(
              "rust-dependency",
              file,
              `${lower} dependency is forbidden: ${dependency}`,
            ),
          );
        if (allowed.has(dependency))
          validateAllowedDependency(
            lower,
            dependency,
            declaration,
            file,
            violations,
          );
      }
    } else {
      inspectDependencyTables(child, file, violations, [...ancestry, key]);
    }
  }
}

/** Validate Cargo.toml with a fail-closed TOML parser and recursive dependency inspection. */
export function validateRustManifest(manifestText) {
  const file = "src-tauri/Cargo.toml";
  if (typeof manifestText !== "string")
    return [violation("malformed-manifest", file, "manifest must be text")];
  let manifest;
  try {
    manifest = parseToml(manifestText);
  } catch (error) {
    return [
      violation(
        "malformed-manifest",
        file,
        `TOML parser rejected manifest: ${oneLine(error.message)}`,
      ),
    ];
  }
  if (!manifest || typeof manifest !== "object" || Array.isArray(manifest))
    return [
      violation(
        "malformed-manifest",
        file,
        "manifest must parse to a TOML table",
      ),
    ];
  const violations = [];
  inspectDependencyTables(manifest, file, violations);
  return sorted(violations);
}

/** Parse HTML and require exactly one complete CSP meta element. */
export function extractHtmlCsp(html) {
  const parseErrors = [];
  const document = parseHtml(html, {
    onParseError: (error) => parseErrors.push(error),
  });
  const materialErrors = parseErrors.filter(
    (error) => error.code !== "missing-doctype",
  );
  if (materialErrors.length)
    throw new Error(
      `HTML parser rejected input: ${materialErrors.map((error) => error.code).join(", ")}`,
    );
  const matches = [];
  traverseHtml(document, (node) => {
    if (node.nodeName !== "meta") return;
    const attrs = Object.fromEntries(
      (node.attrs ?? []).map((item) => [item.name.toLowerCase(), item.value]),
    );
    if (attrs["http-equiv"]?.toLowerCase() === "content-security-policy")
      matches.push(attrs);
  });
  if (matches.length !== 1)
    throw new Error(
      `HTML must contain exactly one Content-Security-Policy meta; found ${matches.length}`,
    );
  if (typeof matches[0].content !== "string" || !matches[0].content.trim())
    throw new Error(
      "Content-Security-Policy meta requires a nonempty content attribute",
    );
  return matches[0].content;
}

export function runCli(root) {
  const violations = scanSource(root);
  let config;
  try {
    config = JSON.parse(
      readFileSync(path.join(root, "src-tauri/tauri.conf.json"), "utf8"),
    );
  } catch (error) {
    violations.push(
      violation("malformed-config", "src-tauri/tauri.conf.json", error.message),
    );
  }
  let htmlCsp;
  try {
    htmlCsp = extractHtmlCsp(
      readFileSync(path.join(root, "index.html"), "utf8"),
    );
  } catch (error) {
    violations.push(violation("malformed-html", "index.html", error.message));
  }
  violations.push(...validateTauriConfig(config, htmlCsp));
  try {
    violations.push(
      ...validateRustManifest(
        readFileSync(path.join(root, "src-tauri/Cargo.toml"), "utf8"),
      ),
    );
  } catch (error) {
    violations.push(
      violation("malformed-manifest", "src-tauri/Cargo.toml", error.message),
    );
  }
  const result = sorted(violations);
  for (const item of result)
    console.error(`${item.file} [${item.code}] ${oneLine(item.detail)}`);
  return result.length === 0 ? 0 : 1;
}

const currentFile = fileURLToPath(import.meta.url);
if (process.argv[1] && path.resolve(process.argv[1]) === currentFile) {
  const root = process.argv[2]
    ? path.resolve(process.argv[2])
    : path.resolve(path.dirname(currentFile), "..");
  process.exitCode = runCli(root);
}
