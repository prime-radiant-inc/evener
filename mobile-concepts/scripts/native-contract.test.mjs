import assert from "node:assert/strict";
import {
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import {
  CANONICAL_CSP,
  CONCEPT_DISPLAY_NAME,
  CONCEPT_IDENTIFIER,
  classifyIosSignature,
  inspectAndroidApk,
  validateAndroidMetadata,
  validateIosMetadata,
  validateSourceContract,
} from "./native-contract.mjs";

const fixtureRoot = new URL("./fixtures/native/", import.meta.url);
const loadFixture = (name) =>
  JSON.parse(readFileSync(new URL(name, fixtureRoot), "utf8"));
const goodIos = loadFixture("good-ios-metadata.json");
const wrongIos = loadFixture("wrong-ios-metadata.json");
const goodAndroid = loadFixture("good-android-metadata.json");
const wrongAndroid = loadFixture("wrong-android-metadata.json");
const codes = (violations) => violations.map((item) => item.code);

function clone(value) {
  return structuredClone(value);
}

function writeSourceFixture(root) {
  mkdirSync(path.join(root, "src-tauri", "gen", "apple"), { recursive: true });
  mkdirSync(
    path.join(root, "src-tauri", "gen", "apple", "build", "Fixture.app"),
    { recursive: true },
  );
  mkdirSync(path.join(root, "dist"), { recursive: true });
  writeFileSync(
    path.join(root, "src-tauri", "tauri.conf.json"),
    `${JSON.stringify({
      productName: CONCEPT_DISPLAY_NAME,
      identifier: CONCEPT_IDENTIFIER,
      build: { frontendDist: "../dist" },
      app: { security: { csp: CANONICAL_CSP } },
      bundle: { icon: ["icons/icon.png"] },
    })}\n`,
  );
  const html = `<!doctype html><meta http-equiv="Content-Security-Policy" content="${CANONICAL_CSP}"><div id="root"></div>`;
  writeFileSync(path.join(root, "index.html"), html);
  writeFileSync(path.join(root, "dist", "index.html"), html);
  writeFileSync(
    path.join(root, "src-tauri", "gen", "apple", "Info.plist"),
    "<plist><dict></dict></plist>\n",
  );
  writeFileSync(
    path.join(
      root,
      "src-tauri",
      "gen",
      "apple",
      "build",
      "Fixture.app",
      "Info.plist",
    ),
    "<plist><dict><key>NSCameraUsageDescription</key><string>built artifact only</string></dict></plist>\n",
  );
}

test("normalized iOS fixtures enforce exact identity and forbidden usage metadata", () => {
  assert.deepEqual(validateIosMetadata(goodIos), []);
  assert.deepEqual(codes(validateIosMetadata(wrongIos)), [
    "ios-identifier",
    "ios-display-name",
    "ios-forbidden-usage-description",
  ]);
});

test("iOS rejects every forbidden privacy, network, notification, and background declaration", () => {
  const forbiddenKeys = [
    "NSMicrophoneUsageDescription",
    "NSSpeechRecognitionUsageDescription",
    "NSCameraUsageDescription",
    "NSPhotoLibraryUsageDescription",
    "NSPhotoLibraryAddUsageDescription",
    "NSLocalNetworkUsageDescription",
    "NSBonjourServices",
    "NSLocationWhenInUseUsageDescription",
    "NSLocationAlwaysAndWhenInUseUsageDescription",
    "NSUserNotificationUsageDescription",
    "UIBackgroundModes",
  ];
  for (const key of forbiddenKeys) {
    const metadata = clone(goodIos);
    metadata.info[key] = key === "UIBackgroundModes" ? ["audio"] : "declared";
    assert.deepEqual(
      codes(validateIosMetadata(metadata)),
      ["ios-forbidden-usage-description"],
      key,
    );
  }
});

test("signed iOS metadata allows only bundle-scoped development signing metadata", () => {
  for (const entitlement of [
    "aps-environment",
    "com.apple.security.application-groups",
    "com.apple.developer.associated-domains",
    "com.apple.developer.icloud-container-identifiers",
    "com.apple.developer.networking.networkextension",
    "com.apple.developer.healthkit",
  ]) {
    const metadata = clone(goodIos);
    metadata.entitlements[entitlement] = ["unexpected"];
    assert.deepEqual(
      codes(validateIosMetadata(metadata)),
      ["ios-forbidden-entitlement"],
      entitlement,
    );
  }

  const sharedGroup = clone(goodIos);
  sharedGroup.entitlements["keychain-access-groups"] = [
    "TEAM123456.com.primeradiant.evener.concepts",
    "TEAM123456.com.primeradiant.evener",
  ];
  assert.deepEqual(codes(validateIosMetadata(sharedGroup)), [
    "ios-keychain-access-groups",
  ]);

  const productionGroup = clone(goodIos);
  productionGroup.entitlements["keychain-access-groups"] = [
    "TEAM123456.com.primeradiant.evener",
  ];
  assert.deepEqual(codes(validateIosMetadata(productionGroup)), [
    "ios-keychain-access-groups",
  ]);
});

test("signed iOS binds application identifier, team, keychain group, and provisioning prefix", () => {
  const application = clone(goodIos);
  application.entitlements["application-identifier"] =
    "OTHERTEAM.com.primeradiant.evener.concepts";
  assert.deepEqual(codes(validateIosMetadata(application)), [
    "ios-application-identifier",
  ]);

  const team = clone(goodIos);
  team.entitlements["com.apple.developer.team-identifier"] = "OTHERTEAM";
  assert.deepEqual(codes(validateIosMetadata(team)), ["ios-team-identifier"]);

  const provisioning = clone(goodIos);
  provisioning.provisioning.Entitlements["aps-environment"] = "development";
  assert.deepEqual(codes(validateIosMetadata(provisioning)), [
    "ios-forbidden-entitlement",
  ]);
});

test("get-task-allow is accepted only for a debugging export", () => {
  const release = clone(goodIos);
  release.exportMethod = "app-store";
  assert.deepEqual(codes(validateIosMetadata(release)), [
    "ios-debug-entitlement",
    "ios-debug-entitlement",
  ]);
});

test("linker ad-hoc simulator signatures are reported as unsigned", () => {
  assert.equal(
    classifyIosSignature({
      code: 0,
      details:
        "flags=0x20002(adhoc,linker-signed)\nSignature=adhoc\nTeamIdentifier=not set",
    }),
    "unsigned",
  );
  assert.equal(
    classifyIosSignature({
      code: 0,
      details:
        "Authority=Apple Development: Developer\nTeamIdentifier=TEAM123456",
    }),
    "signed",
  );
});

test("unsigned simulator metadata has neither provisioning nor entitlements", () => {
  const unsigned = {
    identifier: CONCEPT_IDENTIFIER,
    displayName: CONCEPT_DISPLAY_NAME,
    info: {},
    signatureStatus: "unsigned",
    exportMethod: null,
    entitlements: {},
    provisioning: null,
  };
  assert.deepEqual(validateIosMetadata(unsigned), []);

  const provisioned = clone(unsigned);
  provisioned.provisioning = { Entitlements: {} };
  assert.deepEqual(codes(validateIosMetadata(provisioned)), [
    "ios-provisioning",
  ]);

  const entitled = clone(unsigned);
  entitled.entitlements["application-identifier"] =
    "TEAM123456.com.primeradiant.evener.concepts";
  assert.deepEqual(codes(validateIosMetadata(entitled)), [
    "ios-unsigned-entitlement",
  ]);
});

test("Android fixtures enforce identity, label, debug suffix, and permission classes", () => {
  assert.deepEqual(validateAndroidMetadata(goodAndroid), []);
  assert.deepEqual(codes(validateAndroidMetadata(wrongAndroid)), [
    "android-identifier",
    "android-display-name",
    "android-debug-suffix",
    "android-forbidden-permission",
    "android-unknown-permission",
  ]);
});

test("Android permits only reviewed required-template permissions", () => {
  const noPermissions = clone(goodAndroid);
  noPermissions.permissions = [];
  assert.deepEqual(validateAndroidMetadata(noPermissions), []);

  const forbidden = clone(goodAndroid);
  forbidden.permissions = [
    "android.permission.RECORD_AUDIO",
    "android.permission.ACCESS_FINE_LOCATION",
    "android.permission.POST_NOTIFICATIONS",
  ];
  assert.deepEqual(codes(validateAndroidMetadata(forbidden)), [
    "android-forbidden-permission",
    "android-forbidden-permission",
    "android-forbidden-permission",
  ]);
});

test("source contract requires canonical Tauri and bundled HTML CSP plus concept identity", () => {
  const root = mkdtempSync(path.join(os.tmpdir(), "native-source-contract-"));
  try {
    writeSourceFixture(root);
    assert.deepEqual(validateSourceContract(root), []);

    const configPath = path.join(root, "src-tauri", "tauri.conf.json");
    const config = JSON.parse(readFileSync(configPath, "utf8"));
    config.identifier = "com.primeradiant.evener";
    config.productName = "Evener";
    writeFileSync(configPath, JSON.stringify(config));
    assert.deepEqual(codes(validateSourceContract(root)).slice(0, 2), [
      "source-identifier",
      "source-display-name",
    ]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("source contract compares CSP canonically and rejects native forbidden declarations", () => {
  const root = mkdtempSync(path.join(os.tmpdir(), "native-source-contract-"));
  try {
    writeSourceFixture(root);
    const reordered = CANONICAL_CSP.split(";").reverse().join(";");
    writeFileSync(
      path.join(root, "dist", "index.html"),
      `<meta content="${reordered}" http-equiv="Content-Security-Policy">`,
    );
    assert.deepEqual(validateSourceContract(root), []);

    writeFileSync(
      path.join(root, "src-tauri", "gen", "apple", "Info.plist"),
      "<plist><dict><key>NSCameraUsageDescription</key><string>camera</string></dict></plist>",
    );
    assert.ok(
      codes(validateSourceContract(root)).includes(
        "source-forbidden-native-declaration",
      ),
    );

    writeFileSync(
      path.join(root, "dist", "index.html"),
      '<meta http-equiv="Content-Security-Policy" content="default-src \'self\'; connect-src https:">',
    );
    assert.ok(
      codes(validateSourceContract(root)).includes("source-bundled-csp"),
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("source contract rejects optional native entitlement keys", () => {
  const root = mkdtempSync(path.join(os.tmpdir(), "native-source-contract-"));
  try {
    writeSourceFixture(root);
    writeFileSync(
      path.join(root, "src-tauri", "gen", "apple", "Concepts.entitlements"),
      "<plist><dict><key>aps-environment</key><string>development</string><key>NSBonjourServices</key><array/></dict></plist>",
    );
    assert.ok(
      codes(validateSourceContract(root)).includes(
        "source-forbidden-native-declaration",
      ),
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("Android artifact mode treats analyzer launch failure as a hard error", async () => {
  await assert.rejects(
    inspectAndroidApk("fixture.apk", {
      apkanalyzer: path.join(
        os.tmpdir(),
        "missing-apkanalyzer-for-contract-test",
      ),
    }),
    /apkanalyzer/i,
  );
});
