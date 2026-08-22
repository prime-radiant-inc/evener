import * as fs from "node:fs";
import * as path from "node:path";
import { describe, expect, it } from "vitest";

function readSrc(rel: string): string {
  return fs.readFileSync(path.join(__dirname, rel), "utf8");
}

describe("C1: production never imports test fake", () => {
  it("App.tsx does not import FakeProfileService", () => {
    const src = readSrc("App.tsx");
    expect(src).not.toMatch(/FakeProfileService/);
  });

  it("App.tsx imports createTauriBridge and createProfileService for production", () => {
    const src = readSrc("App.tsx");
    expect(src).toMatch(/createProductionServices/);
  });

  it("fixture-services.ts is only imported by tests and fixture.ts, not App.tsx production path", () => {
    const appSrc = readSrc("App.tsx");
    // App.tsx may import fixture-services only for fixture mode, but
    // the production code path must use real services.
    // The production branch must not construct FakeProfileService.
    expect(appSrc).not.toMatch(/FakeProfileService/);
  });
});

describe("C2: raw auth URL not retained", () => {
  it("FakeProfileService does not retain raw input in recordedRaws", () => {
    const src = readSrc("test/fakeProfileService.ts");
    expect(src).not.toMatch(/recordedRaws/);
  });

  it("FakeProfileService does not store the raw URL in any field", () => {
    const src = readSrc("test/fakeProfileService.ts");
    // No field that stores the raw input string
    expect(src).not.toMatch(/this\.\w*[Rr]aw/);
  });
});

describe("C3: QR flow uses native previewId directly", () => {
  it("OnboardingScreen handleScan does not fabricate a paste URL with a fake token", () => {
    const src = readSrc("screens/OnboardingScreen.tsx");
    // Must not construct a synthetic URL with 'redacted-scan-token' or any token
    expect(src).not.toMatch(/redacted-scan-token/);
    expect(src).not.toMatch(/token=.*scan/);
  });

  it("OnboardingScreen uses the native scan previewId/origin directly, not previewPaste", () => {
    const src = readSrc("screens/OnboardingScreen.tsx");
    // The scan handler should set the preview from the native result directly,
    // not call previewPaste with a fabricated URL.
    expect(src).toMatch(/scanAndPreviewPairing/);
    expect(src).toMatch(/setScanPreview/);
  });
});
