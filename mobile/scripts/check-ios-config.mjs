import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const REQUIRED_USAGE_KEYS = [
  "NSCameraUsageDescription",
  "NSMicrophoneUsageDescription",
  "NSSpeechRecognitionUsageDescription",
  "NSLocalNetworkUsageDescription",
];

export function parseDeploymentTargets(projectSource) {
  return Array.from(
    projectSource.matchAll(/IPHONEOS_DEPLOYMENT_TARGET = ([^;]+);/g),
    (match) => match[1].trim().replace(/^"|"$/g, ""),
  );
}

export function defaultBuiltPlistPath(mobileRoot) {
  return path.join(
    mobileRoot,
    "src-tauri",
    "gen",
    "apple",
    "build",
    "arm64-sim",
    "Evener.app",
    "Info.plist",
  );
}

export function validateIosConfiguration({ deploymentTargets, metadata }) {
  if (deploymentTargets.length !== 2) {
    throw new Error(
      `generated project must have exactly two iOS deployment targets; found ${deploymentTargets.length}`,
    );
  }
  for (const target of deploymentTargets) {
    if (target !== "17.0") {
      throw new Error(
        `every iOS deployment target must be 17.0; found ${target}`,
      );
    }
  }

  if (metadata.MinimumOSVersion !== "17.0") {
    throw new Error(
      `built app minimum OS version must be 17.0; found ${String(metadata.MinimumOSVersion)}`,
    );
  }

  for (const key of REQUIRED_USAGE_KEYS) {
    const value = metadata[key];
    if (typeof value !== "string" || value.trim() === "") {
      throw new Error(`${key} must be a nonempty string`);
    }
  }

  const transportSecurity = metadata.NSAppTransportSecurity;
  if (
    transportSecurity !== null &&
    typeof transportSecurity === "object" &&
    transportSecurity.NSAllowsArbitraryLoads === true
  ) {
    throw new Error("arbitrary HTTP loads must remain disabled");
  }
}

function readBuiltPlist(plistPath) {
  const json = execFileSync(
    "plutil",
    ["-convert", "json", "-o", "-", "--", plistPath],
    { encoding: "utf8" },
  );
  return JSON.parse(json);
}

function main() {
  const scriptDirectory = path.dirname(fileURLToPath(import.meta.url));
  const mobileRoot = path.resolve(scriptDirectory, "..");
  const plistPath = path.resolve(
    process.argv[2] ?? defaultBuiltPlistPath(mobileRoot),
  );
  const projectPath = path.resolve(
    process.argv[3] ??
      path.join(
        mobileRoot,
        "src-tauri",
        "gen",
        "apple",
        "app.xcodeproj",
        "project.pbxproj",
      ),
  );

  validateIosConfiguration({
    deploymentTargets: parseDeploymentTargets(
      readFileSync(projectPath, "utf8"),
    ),
    metadata: readBuiltPlist(plistPath),
  });
  process.stdout.write(`iOS configuration valid: ${plistPath}\n`);
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  main();
}
