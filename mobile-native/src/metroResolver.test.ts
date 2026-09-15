// metro.config.js's resolveRequest is the only thing that resolves
// @evener/appwire-client for the native bundle: Metro reads neither the
// tsconfig paths nor the package's exports map. Nothing in CI bundles the app
// (#1244), so until that gate lands this is what holds the mapping.
import { existsSync } from "node:fs";
import { createRequire } from "node:module";
import { describe, expect, it } from "vitest";

// The config is CommonJS and calls expo's getDefaultConfig at load, so it is
// required rather than imported. createRequire, not the bare `require` Vitest
// happens to provide: its runner evaluates this file with the CommonJS trio
// (require, module, __filename) in scope, which works and says nothing about
// what the source means -- the file is ESM, and this is how ESM spells it.
const config = createRequire(import.meta.url)("../metro.config.js");

const FELL_THROUGH = Symbol("fell through to Metro's own resolver");

// Metro hands the resolver a context carrying the default resolution; this
// stub records that the config declined to answer rather than answering.
function resolve(name: string) {
  const context = {
    originModulePath: new URL("../index.ts", import.meta.url).pathname,
    resolveRequest: () => FELL_THROUGH,
  };
  return config.resolver.resolveRequest(context, name, "ios");
}

describe("metro.config.js resolves the AppWire package by name", () => {
  for (const [specifier, expectedSuffix] of [
    ["@evener/appwire-client", "appwire-client/typescript/index.ts"],
    ["@evener/appwire-client/docContent", "appwire-client/typescript/docContent.ts"],
    ["@evener/appwire-client/testing/fakeClient", "appwire-client/typescript/testing/fakeClient.ts"],
  ] as const) {
    it(`maps ${specifier} to a file that exists`, () => {
      const resolved = resolve(specifier);
      expect(resolved).toMatchObject({ type: "sourceFile" });
      expect(resolved.filePath.endsWith(expectedSuffix)).toBe(true);
      expect(existsSync(resolved.filePath)).toBe(true);
    });
  }

  it("resolves a .tsx subpath, not only .ts", () => {
    // The package ships only .ts today; a .tsx module (a widget, say) must
    // resolve too, since the resolver probes the shared extension list. Stub
    // the existence check rather than writing into the package tree, so an
    // interrupted run leaves nothing untracked. metro.config.js captured the
    // same fs singleton this require returns, so the stub reaches its probe.
    const fs = createRequire(import.meta.url)("node:fs");
    const realExistsSync = fs.existsSync;
    fs.existsSync = (candidate: string) => candidate.endsWith("__probe.tsx") || realExistsSync(candidate);
    try {
      const resolved = resolve("@evener/appwire-client/__probe");
      expect(resolved).toMatchObject({ type: "sourceFile" });
      expect(resolved.filePath.endsWith("__probe.tsx")).toBe(true);
    } finally {
      fs.existsSync = realExistsSync;
    }
  });

  it("declines a subpath the package does not have instead of naming a missing file", () => {
    expect(resolve("@evener/appwire-client/nope")).toBe(FELL_THROUGH);
  });

  it("leaves an unrelated specifier to Metro", () => {
    expect(resolve("react-native")).toBe(FELL_THROUGH);
  });
});
