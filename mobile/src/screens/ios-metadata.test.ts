import { existsSync, readFileSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

const appRoot = path.join(__dirname, "..", "..", "src-tauri");

function plistStrings(source: string): Map<string, string> {
  const document = new DOMParser().parseFromString(source, "application/xml");
  expect(document.querySelector("parsererror")).toBeNull();

  const values = new Map<string, string>();
  for (const key of document.querySelectorAll("plist > dict > key")) {
    const value = key.nextElementSibling;
    if (value?.tagName === "string") {
      values.set(key.textContent ?? "", value.textContent ?? "");
    }
  }
  return values;
}

describe("iOS application metadata", () => {
  it("declares every required privacy usage description in source and generated plists", () => {
    const plistPaths = [
      path.join(appRoot, "Info.ios.plist"),
      path.join(appRoot, "gen", "apple", "app_iOS", "Info.plist"),
    ];

    for (const plistPath of plistPaths) {
      expect(existsSync(plistPath), `${plistPath} must exist`).toBe(true);
      const values = plistStrings(readFileSync(plistPath, "utf8"));
      for (const key of [
        "NSCameraUsageDescription",
        "NSMicrophoneUsageDescription",
        "NSSpeechRecognitionUsageDescription",
        "NSLocalNetworkUsageDescription",
      ]) {
        const value = values.get(key);
        expect(value, `${plistPath}: ${key} must exist`).toBeDefined();
        expect(
          value?.trim().length,
          `${plistPath}: ${key} must be nonempty`,
        ).toBeGreaterThan(0);
      }
    }
  });

  it("keeps every generated iOS target at the iOS 17 floor", () => {
    const project = readFileSync(
      path.join(appRoot, "gen", "apple", "app.xcodeproj", "project.pbxproj"),
      "utf8",
    );
    const targets = Array.from(
      project.matchAll(/IPHONEOS_DEPLOYMENT_TARGET = ([^;]+);/g),
      (match) => match[1],
    );

    expect(targets).toEqual(["17.0", "17.0"]);
  });
});
