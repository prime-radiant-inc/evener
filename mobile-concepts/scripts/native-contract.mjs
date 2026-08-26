import { spawn } from "node:child_process";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { readdir, readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

import {
  CANONICAL_CSP as BOUNDARY_CANONICAL_CSP,
  extractHtmlCsp,
} from "./boundary.mjs";

export const CONCEPT_IDENTIFIER = "com.primeradiant.evener.concepts";
export const CONCEPT_DISPLAY_NAME = "Evener Concepts";
export const PRODUCTION_IDENTIFIER = "com.primeradiant.evener";
export const CANONICAL_CSP = BOUNDARY_CANONICAL_CSP;
export const APKANALYZER =
  "/opt/homebrew/share/android-commandlinetools/cmdline-tools/latest/bin/apkanalyzer";

const IOS_ALLOWED_ENTITLEMENTS = new Set([
  "application-identifier",
  "com.apple.developer.team-identifier",
  "get-task-allow",
  "keychain-access-groups",
]);
const IOS_FORBIDDEN_INFO_KEYS = new Set([
  "NSMicrophoneUsageDescription",
  "NSSpeechRecognitionUsageDescription",
  "NSCameraUsageDescription",
  "NSPhotoLibraryUsageDescription",
  "NSPhotoLibraryAddUsageDescription",
  "NSLocalNetworkUsageDescription",
  "NSBonjourServices",
  "NSLocationUsageDescription",
  "NSLocationWhenInUseUsageDescription",
  "NSLocationAlwaysUsageDescription",
  "NSLocationAlwaysAndWhenInUseUsageDescription",
  "NSUserNotificationUsageDescription",
  "UIBackgroundModes",
]);
const ANDROID_REQUIRED_TEMPLATE_PERMISSIONS = new Set([
  "android.permission.INTERNET",
]);
const ANDROID_FORBIDDEN_PERMISSIONS = new Set([
  "android.permission.ACCESS_BACKGROUND_LOCATION",
  "android.permission.ACCESS_COARSE_LOCATION",
  "android.permission.ACCESS_FINE_LOCATION",
  "android.permission.ACCESS_MEDIA_LOCATION",
  "android.permission.BLUETOOTH_ADVERTISE",
  "android.permission.BLUETOOTH_CONNECT",
  "android.permission.BLUETOOTH_SCAN",
  "android.permission.CAMERA",
  "android.permission.MANAGE_EXTERNAL_STORAGE",
  "android.permission.NEARBY_WIFI_DEVICES",
  "android.permission.POST_NOTIFICATIONS",
  "android.permission.READ_EXTERNAL_STORAGE",
  "android.permission.READ_MEDIA_AUDIO",
  "android.permission.READ_MEDIA_IMAGES",
  "android.permission.READ_MEDIA_VIDEO",
  "android.permission.RECORD_AUDIO",
  "android.permission.WRITE_EXTERNAL_STORAGE",
]);
const CODE_PRIORITY = new Map([
  ["ios-identifier", 0],
  ["ios-display-name", 1],
  ["ios-signature-status", 2],
  ["ios-forbidden-usage-description", 3],
  ["ios-application-identifier", 4],
  ["ios-team-identifier", 5],
  ["ios-keychain-access-groups", 6],
  ["ios-debug-entitlement", 7],
  ["ios-forbidden-entitlement", 8],
  ["ios-provisioning", 9],
  ["ios-unsigned-entitlement", 10],
  ["ios-entitlements-source", 11],
  ["android-identifier", 20],
  ["android-display-name", 21],
  ["android-debug-suffix", 22],
  ["android-forbidden-permission", 23],
  ["android-unknown-permission", 24],
  ["source-identifier", 30],
  ["source-display-name", 31],
  ["source-tauri-csp", 32],
  ["source-html-csp", 33],
  ["source-bundled-csp", 34],
  ["source-forbidden-native-declaration", 35],
  ["source-malformed", 36],
]);

/** @typedef {{ code: string, detail: string }} NativeViolation */

/**
 * @typedef {object} NativeArtifactReport
 * @property {"ios" | "android"} platform
 * @property {string} artifact
 * @property {string} identifier
 * @property {string} displayName
 * @property {string[]} permissions
 * @property {"signed" | "unsigned"} signatureStatus
 * @property {Object<string, unknown>} entitlements
 * @property {Object<string, unknown> | null} provisioning
 * @property {{ code: string, detail: string }[]} violations
 */

function violation(code, detail) {
  return { code, detail };
}

function compareText(left, right) {
  return left < right ? -1 : left > right ? 1 : 0;
}

function sorted(violations) {
  return violations.sort(
    (left, right) =>
      (CODE_PRIORITY.get(left.code) ?? 100) -
        (CODE_PRIORITY.get(right.code) ?? 100) ||
      compareText(left.code, right.code) ||
      compareText(left.detail, right.detail),
  );
}

function object(value) {
  return value && typeof value === "object" && !Array.isArray(value)
    ? value
    : {};
}

function stringArray(value) {
  return Array.isArray(value) && value.every((item) => typeof item === "string")
    ? value
    : null;
}

function provisioningPrefix(provisioning, entitlements) {
  const prefixes = stringArray(provisioning?.ApplicationIdentifierPrefix);
  if (prefixes?.length === 1 && prefixes[0])
    return prefixes[0].replace(/\.$/, "");
  const team = entitlements["com.apple.developer.team-identifier"];
  return typeof team === "string" && team ? team : null;
}

function validateEntitlementSet(
  entitlements,
  { prefix, exportMethod, provisioning = false },
) {
  const violations = [];
  const values = object(entitlements);
  for (const key of Object.keys(values).sort()) {
    if (!IOS_ALLOWED_ENTITLEMENTS.has(key)) {
      violations.push(
        violation(
          "ios-forbidden-entitlement",
          `${provisioning ? "provisioning" : "effective"} entitlement is forbidden: ${key}`,
        ),
      );
    }
  }
  if (!prefix) {
    violations.push(
      violation(
        "ios-team-identifier",
        "signed metadata has no single team prefix",
      ),
    );
    return violations;
  }
  const expectedApplication = `${prefix}.${CONCEPT_IDENTIFIER}`;
  if (values["application-identifier"] !== expectedApplication) {
    violations.push(
      violation(
        "ios-application-identifier",
        `${provisioning ? "provisioning" : "effective"} application-identifier must be ${expectedApplication}`,
      ),
    );
  }
  if (values["com.apple.developer.team-identifier"] !== prefix) {
    violations.push(
      violation(
        "ios-team-identifier",
        `${provisioning ? "provisioning" : "effective"} team identifier must equal ${prefix}`,
      ),
    );
  }
  const groups = stringArray(values["keychain-access-groups"]);
  if (groups?.length !== 1 || groups[0] !== expectedApplication) {
    violations.push(
      violation(
        "ios-keychain-access-groups",
        `${provisioning ? "provisioning" : "effective"} Keychain groups must contain only ${expectedApplication}`,
      ),
    );
  }
  if (values["get-task-allow"] === true && exportMethod !== "debugging") {
    violations.push(
      violation(
        "ios-debug-entitlement",
        `${provisioning ? "provisioning" : "effective"} get-task-allow requires a debugging export`,
      ),
    );
  }
  if (
    Object.hasOwn(values, "get-task-allow") &&
    typeof values["get-task-allow"] !== "boolean"
  ) {
    violations.push(
      violation(
        "ios-debug-entitlement",
        `${provisioning ? "provisioning" : "effective"} get-task-allow must be boolean`,
      ),
    );
  }
  return violations;
}

/** Validate normalized iOS plist, signature, entitlement, and provisioning data. */
export function validateIosMetadata(metadata) {
  const value = object(metadata);
  const violations = [];
  if (value.identifier !== CONCEPT_IDENTIFIER) {
    violations.push(
      violation(
        "ios-identifier",
        `bundle identifier must be ${CONCEPT_IDENTIFIER}`,
      ),
    );
  }
  if (value.displayName !== CONCEPT_DISPLAY_NAME) {
    violations.push(
      violation(
        "ios-display-name",
        `display name must be ${CONCEPT_DISPLAY_NAME}`,
      ),
    );
  }
  const info = object(value.info);
  for (const key of Object.keys(info).sort()) {
    if (IOS_FORBIDDEN_INFO_KEYS.has(key)) {
      violations.push(
        violation(
          "ios-forbidden-usage-description",
          `iOS declaration is forbidden: ${key}`,
        ),
      );
    }
  }
  if (
    value.signatureStatus !== "signed" &&
    value.signatureStatus !== "unsigned"
  ) {
    violations.push(
      violation("ios-signature-status", "signature status must be explicit"),
    );
  } else if (value.signatureStatus === "unsigned") {
    if (value.provisioning !== null && value.provisioning !== undefined) {
      violations.push(
        violation(
          "ios-provisioning",
          "unsigned simulator app must not contain embedded provisioning",
        ),
      );
    }
    if (Object.keys(object(value.entitlements)).length > 0) {
      violations.push(
        violation(
          "ios-unsigned-entitlement",
          "unsigned simulator target entitlement plist must be empty",
        ),
      );
    }
  } else {
    const entitlements = object(value.entitlements);
    const provisioning = object(value.provisioning);
    const prefix = provisioningPrefix(provisioning, entitlements);
    violations.push(
      ...validateEntitlementSet(entitlements, {
        prefix,
        exportMethod: value.exportMethod,
      }),
    );
    const provisionedEntitlements = object(provisioning.Entitlements);
    if (Object.keys(provisionedEntitlements).length > 0) {
      violations.push(
        ...validateEntitlementSet(provisionedEntitlements, {
          prefix,
          exportMethod: value.exportMethod,
          provisioning: true,
        }),
      );
    }
    const provisionedTeams = stringArray(provisioning.TeamIdentifier);
    if (
      provisionedTeams &&
      (provisionedTeams.length !== 1 || provisionedTeams[0] !== prefix)
    ) {
      violations.push(
        violation(
          "ios-team-identifier",
          "provisioning TeamIdentifier must equal the application prefix",
        ),
      );
    }
  }
  return sorted(violations);
}

/** Validate normalized APK identity, label, and permission metadata. */
export function validateAndroidMetadata(metadata) {
  const value = object(metadata);
  const violations = [];
  if (value.identifier !== CONCEPT_IDENTIFIER) {
    violations.push(
      violation(
        "android-identifier",
        `application ID must be ${CONCEPT_IDENTIFIER}`,
      ),
    );
  }
  if (value.displayName !== CONCEPT_DISPLAY_NAME) {
    violations.push(
      violation(
        "android-display-name",
        `application label must be ${CONCEPT_DISPLAY_NAME}`,
      ),
    );
  }
  if (/\.(?:debug|dev|staging|test)$/i.test(String(value.identifier ?? ""))) {
    violations.push(
      violation(
        "android-debug-suffix",
        "debug application-ID suffix is forbidden",
      ),
    );
  }
  const permissions = Array.isArray(value.permissions) ? value.permissions : [];
  for (const permission of [...new Set(permissions)].sort()) {
    if (ANDROID_REQUIRED_TEMPLATE_PERMISSIONS.has(permission)) continue;
    if (ANDROID_FORBIDDEN_PERMISSIONS.has(permission)) {
      violations.push(
        violation(
          "android-forbidden-permission",
          `Android permission is forbidden: ${permission}`,
        ),
      );
    } else {
      violations.push(
        violation(
          "android-unknown-permission",
          `Android permission is not reviewed: ${permission}`,
        ),
      );
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
    const directive = parts.shift().toLowerCase();
    if (directives.has(directive)) return null;
    directives.set(directive, parts.sort().join(" "));
  }
  return [...directives.entries()]
    .sort(([left], [right]) => compareText(left, right))
    .map(([directive, sources]) => `${directive} ${sources}`.trim())
    .join(";");
}

function readHtmlCsp(file, code, violations) {
  try {
    return extractHtmlCsp(readFileSync(file, "utf8"));
  } catch (error) {
    violations.push(
      violation(code, `${path.basename(file)}: ${error.message}`),
    );
    return null;
  }
}

const NATIVE_TEXT_EXTENSIONS = new Set([
  ".entitlements",
  ".gradle",
  ".json",
  ".kts",
  ".pbxproj",
  ".plist",
  ".xml",
  ".yml",
  ".yaml",
]);
const FORBIDDEN_NATIVE_TEXT = [
  /NS(?:MicrophoneUsageDescription|SpeechRecognitionUsageDescription|CameraUsageDescription|PhotoLibrary(?:Add)?UsageDescription|LocalNetworkUsageDescription|BonjourServices|Location[^<\s]*UsageDescription|UserNotificationUsageDescription)/,
  /UIBackgroundModes/,
  /(?:aps-environment|com\.apple\.security\.application-groups|com\.apple\.developer\.(?:associated-domains|icloud|networking\.networkextension|usernotifications))/,
  /android\.permission\.(?:RECORD_AUDIO|CAMERA|ACCESS_(?:FINE|COARSE|BACKGROUND)_LOCATION|POST_NOTIFICATIONS|READ_MEDIA_(?:AUDIO|IMAGES|VIDEO)|NEARBY_WIFI_DEVICES)/,
  /applicationIdSuffix/,
];

function walkNativeText(root, relative = "") {
  const directory = path.join(root, relative);
  if (!statSync(directory, { throwIfNoEntry: false })?.isDirectory()) return [];
  const files = [];
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    if (
      entry.isDirectory() &&
      [".gradle", "build", "DerivedData", "target"].includes(entry.name)
    ) {
      continue;
    }
    const child = path.join(relative, entry.name);
    if (entry.isDirectory()) files.push(...walkNativeText(root, child));
    else if (
      entry.isFile() &&
      NATIVE_TEXT_EXTENSIONS.has(path.extname(entry.name))
    ) {
      files.push(child);
    }
  }
  return files;
}

/** Validate source configuration without claiming built native metadata. */
export function validateSourceContract(root) {
  const resolved = path.resolve(root);
  const violations = [];
  let config = {};
  try {
    config = JSON.parse(
      readFileSync(path.join(resolved, "src-tauri", "tauri.conf.json"), "utf8"),
    );
  } catch (error) {
    violations.push(
      violation("source-malformed", `Tauri config: ${error.message}`),
    );
  }
  if (config.identifier !== CONCEPT_IDENTIFIER) {
    violations.push(
      violation(
        "source-identifier",
        `identifier must be ${CONCEPT_IDENTIFIER}`,
      ),
    );
  }
  if (config.productName !== CONCEPT_DISPLAY_NAME) {
    violations.push(
      violation(
        "source-display-name",
        `productName must be ${CONCEPT_DISPLAY_NAME}`,
      ),
    );
  }
  const tauriCsp = config.app?.security?.csp;
  if (canonicalCsp(tauriCsp) !== canonicalCsp(CANONICAL_CSP)) {
    violations.push(
      violation(
        "source-tauri-csp",
        "Tauri CSP must equal the canonical offline CSP",
      ),
    );
  }
  const htmlCsp = readHtmlCsp(
    path.join(resolved, "index.html"),
    "source-html-csp",
    violations,
  );
  if (htmlCsp && canonicalCsp(htmlCsp) !== canonicalCsp(tauriCsp)) {
    violations.push(
      violation(
        "source-html-csp",
        "HTML and Tauri CSP must be canonically equal",
      ),
    );
  }
  const bundledHtml = path.join(resolved, "dist", "index.html");
  if (statSync(bundledHtml, { throwIfNoEntry: false })?.isFile()) {
    const bundledCsp = readHtmlCsp(
      bundledHtml,
      "source-bundled-csp",
      violations,
    );
    if (bundledCsp && canonicalCsp(bundledCsp) !== canonicalCsp(tauriCsp)) {
      violations.push(
        violation(
          "source-bundled-csp",
          "bundled HTML and Tauri CSP must be canonically equal",
        ),
      );
    }
  }
  const nativeRoot = path.join(resolved, "src-tauri", "gen");
  for (const relative of walkNativeText(nativeRoot)) {
    const text = readFileSync(path.join(nativeRoot, relative), "utf8");
    const productionIdentity = new RegExp(
      `${PRODUCTION_IDENTIFIER.replaceAll(".", "\\.")}(?!\\.concepts(?:\\b|$))`,
    );
    if (
      productionIdentity.test(text) ||
      FORBIDDEN_NATIVE_TEXT.some((pattern) => pattern.test(text))
    ) {
      violations.push(
        violation(
          "source-forbidden-native-declaration",
          `forbidden native declaration in src-tauri/gen/${relative.replaceAll(path.sep, "/")}`,
        ),
      );
    }
  }
  return sorted(violations);
}

function runProcess(command, args, options = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd: options.cwd,
      stdio: [options.input === undefined ? "ignore" : "pipe", "pipe", "pipe"],
    });
    const stdout = [];
    const stderr = [];
    child.stdout.on("data", (chunk) => stdout.push(chunk));
    child.stderr.on("data", (chunk) => stderr.push(chunk));
    child.once("error", (error) => {
      error.message = `${command} launch failed: ${error.message}`;
      reject(error);
    });
    child.once("close", (code) =>
      resolve({
        code: code ?? 1,
        stdout: Buffer.concat(stdout),
        stderr: Buffer.concat(stderr),
      }),
    );
    if (options.input !== undefined) child.stdin.end(options.input);
  });
}

function toolText(result, stream = "stdout") {
  return result[stream].toString("utf8").trim();
}

async function requiredTool(run, command, args, options = {}) {
  const result = await run(command, args, options);
  if (result.code !== 0) {
    const detail = toolText(result, "stderr") || toolText(result);
    throw new Error(
      `${path.basename(command)} failed (${result.code}): ${detail}`,
    );
  }
  return result;
}

async function parsePlistFile(run, file) {
  const result = await requiredTool(run, "plutil", [
    "-convert",
    "json",
    "-o",
    "-",
    file,
  ]);
  return JSON.parse(toolText(result));
}

async function parsePlistData(run, data) {
  const result = await requiredTool(
    run,
    "plutil",
    ["-convert", "json", "-o", "-", "-"],
    { input: data },
  );
  return JSON.parse(toolText(result));
}

async function locateUnsignedEntitlements(appPath, run) {
  let current = path.resolve(appPath);
  for (let depth = 0; depth < 10; depth += 1) {
    const entries = await readdir(current, { withFileTypes: true }).catch(
      () => [],
    );
    const projects = entries.filter(
      (entry) => entry.isDirectory() && entry.name.endsWith(".xcodeproj"),
    );
    for (const project of projects) {
      const projectFile = path.join(current, project.name, "project.pbxproj");
      const text = await readFile(projectFile, "utf8").catch(() => null);
      if (!text) continue;
      const settings = [
        ...text.matchAll(/CODE_SIGN_ENTITLEMENTS\s*=\s*"?([^";]+)"?;/g),
      ].map((match) => match[1]);
      const unique = [...new Set(settings)];
      if (unique.length !== 1) {
        return { entitlements: {}, inspected: false };
      }
      const entitlementPath = path.resolve(current, unique[0]);
      return {
        entitlements: await parsePlistFile(run, entitlementPath),
        inspected: true,
      };
    }
    const parent = path.dirname(current);
    if (parent === current) break;
    current = parent;
  }
  return { entitlements: {}, inspected: false };
}

/** Classify simulator linker/ad-hoc output separately from signed exports. */
export function classifyIosSignature({ code, details }) {
  if (code === 0) {
    return /\bSignature=adhoc\b|\([^\n)]*\badhoc\b[^\n)]*\)/i.test(details)
      ? "unsigned"
      : "signed";
  }
  return /not signed|code object is not signed/i.test(details)
    ? "unsigned"
    : null;
}

/** Inspect a real iOS .app using plist, signature, and provisioning tools. */
export async function inspectIosApp(appPath, tools = {}) {
  const run = tools.run ?? runProcess;
  const artifact = path.resolve(appPath);
  const info = await parsePlistFile(run, path.join(artifact, "Info.plist"));
  const signature = await run("codesign", ["-dv", "--verbose=4", artifact]);
  const signatureText = `${toolText(signature)}\n${toolText(signature, "stderr")}`;
  const signatureStatus = classifyIosSignature({
    code: signature.code,
    details: signatureText,
  });
  let entitlements = {};
  let provisioning = null;
  let entitlementsInspected = true;
  if (signatureStatus === "signed") {
    const entitlementResult = await requiredTool(run, "codesign", [
      "-d",
      "--entitlements",
      ":-",
      artifact,
    ]);
    const entitlementData =
      entitlementResult.stdout.length > 0
        ? entitlementResult.stdout
        : entitlementResult.stderr;
    entitlements = await parsePlistData(run, entitlementData);
    const profile = path.join(artifact, "embedded.mobileprovision");
    if (statSync(profile, { throwIfNoEntry: false })?.isFile()) {
      const decoded = await requiredTool(run, "security", [
        "cms",
        "-D",
        "-i",
        profile,
      ]);
      provisioning = await parsePlistData(run, decoded.stdout);
    }
  } else if (signatureStatus === "unsigned") {
    if (
      statSync(path.join(artifact, "embedded.mobileprovision"), {
        throwIfNoEntry: false,
      })
    ) {
      provisioning = { unexpectedEmbeddedProfile: true };
    }
    const inspected = await locateUnsignedEntitlements(artifact, run);
    entitlements = inspected.entitlements;
    entitlementsInspected = inspected.inspected;
  } else {
    throw new Error(
      `codesign failed (${signature.code}): ${signatureText.trim()}`,
    );
  }
  const exportMethod =
    entitlements["get-task-allow"] === true ||
    provisioning?.Entitlements?.["get-task-allow"] === true
      ? "debugging"
      : null;
  const metadata = {
    identifier: info.CFBundleIdentifier,
    displayName: info.CFBundleDisplayName ?? info.CFBundleName,
    info,
    signatureStatus,
    exportMethod,
    entitlements,
    provisioning,
  };
  const violations = validateIosMetadata(metadata);
  if (signatureStatus === "unsigned" && !entitlementsInspected) {
    violations.push(
      violation(
        "ios-entitlements-source",
        "generated target CODE_SIGN_ENTITLEMENTS could not be inspected",
      ),
    );
  }
  return {
    platform: "ios",
    artifact,
    identifier: String(metadata.identifier ?? ""),
    displayName: String(metadata.displayName ?? ""),
    permissions: Object.keys(info)
      .filter((key) => IOS_FORBIDDEN_INFO_KEYS.has(key))
      .sort(),
    signatureStatus,
    entitlements,
    provisioning,
    violations: sorted(violations),
  };
}

function parseManifestLabel(manifest) {
  const application = manifest.match(/<application\b[^>]*>/i)?.[0] ?? "";
  return (
    application.match(/(?:android:)?label\s*=\s*["']([^"']+)["']/i)?.[1] ?? ""
  );
}

function normalizePermissions(output) {
  const permissions = [];
  for (const line of output.split(/\r?\n/)) {
    const match = line.match(/(?:name=['"])?([A-Za-z][A-Za-z0-9_.]+)(?:['"])?/);
    if (match?.[1].includes(".")) permissions.push(match[1]);
  }
  return [...new Set(permissions)].sort();
}

async function resolveAndroidLabel(
  run,
  analyzer,
  apkPath,
  identifier,
  manifest,
) {
  const label = parseManifestLabel(manifest);
  const resource = label.match(/^@(?:[^:]+:)?string\/(.+)$/);
  if (!resource) return label;
  const args = [
    "resources",
    "value",
    "--config",
    "default",
    "--name",
    resource[1],
    "--type",
    "string",
    "--package",
    identifier,
    apkPath,
  ];
  return toolText(await requiredTool(run, analyzer, args));
}

async function apkSignatureStatus(apkPath) {
  const bytes = await readFile(apkPath);
  const magic = Buffer.from("APK Sig Block 42", "ascii");
  if (bytes.indexOf(magic) !== -1) return "signed";
  const centralText = bytes.toString("latin1");
  return /META-INF\/[A-Z0-9_-]+\.(?:RSA|DSA|EC)\b/i.test(centralText)
    ? "signed"
    : "unsigned";
}

/** Inspect a real APK with apkanalyzer; analyzer launch/failure is fatal. */
export async function inspectAndroidApk(apkPath, tools = {}) {
  const run = tools.run ?? runProcess;
  const analyzer = tools.apkanalyzer ?? APKANALYZER;
  const artifact = path.resolve(apkPath);
  const applicationId = toolText(
    await requiredTool(run, analyzer, ["manifest", "application-id", artifact]),
  );
  const permissionOutput = toolText(
    await requiredTool(run, analyzer, ["manifest", "permissions", artifact]),
  );
  const manifest = toolText(
    await requiredTool(run, analyzer, ["manifest", "print", artifact]),
  );
  const displayName = await resolveAndroidLabel(
    run,
    analyzer,
    artifact,
    applicationId,
    manifest,
  );
  const permissions = normalizePermissions(permissionOutput);
  const signatureStatus = await apkSignatureStatus(artifact);
  const metadata = {
    identifier: applicationId,
    displayName,
    permissions,
    signatureStatus,
  };
  return {
    platform: "android",
    artifact,
    identifier: applicationId,
    displayName,
    permissions,
    signatureStatus,
    entitlements: {},
    provisioning: null,
    violations: validateAndroidMetadata(metadata),
  };
}

function parseCli(argv) {
  if (argv.length === 1 && argv[0] === "--source") return { mode: "source" };
  if (argv.length === 2 && argv[0] === "--ios-app") {
    return { mode: "ios", artifact: argv[1] };
  }
  if (argv.length === 2 && argv[0] === "--android-apk") {
    return { mode: "android", artifact: argv[1] };
  }
  throw new Error(
    "usage: native-contract.mjs --source | --ios-app PATH | --android-apk PATH",
  );
}

async function runCli(argv) {
  const options = parseCli(argv);
  let report;
  if (options.mode === "source") {
    const root = process.cwd();
    report = { mode: "source", root, violations: validateSourceContract(root) };
  } else if (options.mode === "ios") {
    report = await inspectIosApp(options.artifact);
  } else {
    report = await inspectAndroidApk(options.artifact);
  }
  console.log(JSON.stringify(report, null, 2));
  if (report.violations.length > 0) process.exitCode = 1;
}

const isMain =
  process.argv[1] &&
  path.resolve(process.argv[1]) ===
    path.resolve(fileURLToPath(import.meta.url));
if (isMain) {
  runCli(process.argv.slice(2)).catch((error) => {
    console.error(error.message);
    process.exitCode = 1;
  });
}
