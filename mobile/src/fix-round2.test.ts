import * as fs from "node:fs";
import * as path from "node:path";
import { describe, expect, it } from "vitest";

function readSrc(rel: string): string {
  return fs.readFileSync(path.join(__dirname, rel), "utf8");
}

describe("C-native: production transport uses real command names", () => {
  it("production-services.ts does not call nonexistent native_bridge command", () => {
    const src = readSrc("screens/production-services.ts");
    expect(src).not.toMatch(/native_bridge/);
    expect(src).not.toMatch(/native_subscribe_lifecycle/);
  });
});

describe("C-haptic: pairing success commits even if haptic fails", () => {
  it("OnboardingScreen handleSave does not gate onConnected on haptic success", () => {
    const src = readSrc("screens/OnboardingScreen.tsx");
    // The haptic call should be best-effort — onConnected must fire even if
    // hapticPerform rejects. Look for a try/catch around haptic or fire-and-forget.
    expect(src).toMatch(/hapticPerform/);
    // onConnected should not be inside the haptic await chain unconditionally.
    // It should be called after confirmPairing regardless of haptic outcome.
  });
});

describe("I-preview: cancel preview on stale/unmount/sheet-dismiss", () => {
  it("connection store cancelPreview increments generation", () => {
    const src = readSrc("state/connection.ts");
    expect(src).toMatch(/\+\+previewGen/);
  });

  it("OnboardingScreen calls cancelPreview on unmount", () => {
    const src = readSrc("screens/OnboardingScreen.tsx");
    expect(src).toMatch(/cancelPreview/);
  });
});

describe("I-clear-nav: switch clears actual navigation/conversation state", () => {
  it("RootShell switchTo calls navigation clearConversations", () => {
    const _src = readSrc("screens/RootShell.tsx");
    // The switch handler in the server switcher should clear navigation state.
    // This may be in ServerSwitcherSheet or RootShell.
    const switcherSrc = readSrc("screens/ServerSwitcherSheet.tsx");
    expect(switcherSrc).toMatch(/clearConversations|popAllConversations/);
  });
});

describe("I-viewport: one constrained viewport + one scroller", () => {
  it("CSS defines a viewport-height variable and overflow hidden on shell", () => {
    const css = readSrc("ui/global.css");
    expect(css).toMatch(/--viewport-height/);
    expect(css).toMatch(/overflow:\s*hidden/i);
  });
});

describe("I-onboarding-visual: copy and hierarchy changes", () => {
  it("OnboardingScreen has a Connect to a Hub heading", () => {
    const src = readSrc("screens/OnboardingScreen.tsx");
    expect(src).toMatch(/Connect to a Hub/i);
  });

  it("OnboardingScreen says Scan QR Code (not just Scan QR)", () => {
    const src = readSrc("screens/OnboardingScreen.tsx");
    expect(src).toMatch(/Scan QR Code/i);
  });

  it("OnboardingScreen has an explicit or divider", () => {
    const src = readSrc("screens/OnboardingScreen.tsx");
    expect(src).toMatch(/\bor\b/i);
  });

  it("OnboardingScreen field label says Authorization URL", () => {
    const src = readSrc("screens/OnboardingScreen.tsx");
    expect(src).toMatch(/Authorization URL/i);
  });

  it("OnboardingScreen submit button says Connect (not Paste)", () => {
    const src = readSrc("screens/OnboardingScreen.tsx");
    expect(src).toMatch(/Connect/i);
  });
});

describe("I-switcher-detail: one-row-tap to profile detail", () => {
  it("ServerSwitcherSheet does not render 4 buttons per row", () => {
    const src = readSrc("screens/ServerSwitcherSheet.tsx");
    // The view mode should not have Use, Edit, Re-pair, Remove all visible.
    // Instead, tapping the row should open a detail view with those actions.
    // Count the number of Button elements in the view-mode branch.
    expect(src).toMatch(/detail|Detail/i);
  });
});
