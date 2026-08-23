/**
 * Service bundles for screen tests and the browser fixture mode.
 *
 * Production wires the real `nativeProfiles` service and a real `NativeBridge`.
 * Tests and the fixture inject a `FakeProfileService` and `FakeNativeBridge` so
 * no Tauri plugin, Keychain, or Hub is required. No credential, raw URL, or
 * token is ever held in JS state.
 */
import type { NativeBridge } from "../native/client";
import { FakeNativeBridge } from "../native/fake";
import type {
  ProfileRedacted,
  ProfileService,
} from "../services/nativeProfiles";
import { FakeProfileService } from "../test/fakeProfileService";
import type { ShellServiceBundle } from "./root-types";

export interface OnboardingServiceBundle {
  readonly profile: ProfileService;
  readonly native: NativeBridge;
  /** Recorded haptic kinds (for tests asserting success haptic). */
  readonly hapticCalls: string[];
  readonly contentSize: () => string;
}

export interface CreateOnboardingServicesOptions {
  readonly existing?: readonly ProfileRedacted[];
  readonly failPreview?: boolean;
  readonly failConfirm?: boolean;
}

/**
 * Build a service bundle for the onboarding screen. `failPreview`/`failConfirm`
 * inject one-shot failures so tests prove error redaction without a real Hub.
 */
export function createOnboardingServices(
  opts: CreateOnboardingServicesOptions = {},
): OnboardingServiceBundle {
  const profile = new FakeProfileService({
    profiles: opts.existing ?? [],
    activeProfileId:
      opts.existing && opts.existing.length > 0
        ? (opts.existing[0]?.id ?? null)
        : null,
  });
  if (opts.failPreview) profile.failOnce("previewPaste");
  if (opts.failConfirm) profile.failOnce("confirmPairing");

  const hapticCalls: string[] = [];
  const nativeTransport = new FakeNativeBridge();
  const native: NativeBridge = {
    version: nativeTransport.version,
    async secureGet(id) {
      return { present: nativeTransport.hasProfile(id) };
    },
    async secureSet(id, cap) {
      void cap;
      nativeTransport.seedProfile(id, "redacted");
      return { stored: true };
    },
    async secureDelete(_id) {
      return { deleted: true };
    },
    async scanAndPreviewPairing() {
      return {
        previewId: "scan-preview",
        origin: "https://hub.example.com:8443",
      };
    },
    async clipboardPaste() {
      return "";
    },
    async requestPermission(kind) {
      return { kind, granted: true };
    },
    async speechStart() {},
    async speechStop() {},
    async synthesisSpeak() {},
    async synthesisStop() {},
    async hapticPerform(kind) {
      hapticCalls.push(kind);
    },
    async getContentSize() {
      return "large";
    },
    onLifecycle() {
      return () => {};
    },
  };

  return {
    profile,
    native,
    hapticCalls,
    contentSize: () => "large",
  };
}

export interface CreateShellServicesOptions {
  readonly profiles?: readonly ProfileRedacted[];
  readonly activeProfileId?: string | null;
}

/**
 * Build a service bundle for the root shell (Sessions/New/Settings + switcher).
 */
export function createShellServices(
  opts: CreateShellServicesOptions = {},
): ShellServiceBundle {
  const profile = new FakeProfileService({
    profiles: opts.profiles ?? [],
    activeProfileId: opts.activeProfileId ?? null,
  });

  const hapticCalls: string[] = [];
  const nativeTransport = new FakeNativeBridge();
  const native: NativeBridge = {
    version: nativeTransport.version,
    async secureGet(id) {
      return { present: nativeTransport.hasProfile(id) };
    },
    async secureSet(id, cap) {
      void cap;
      nativeTransport.seedProfile(id, "redacted");
      return { stored: true };
    },
    async secureDelete(_id) {
      return { deleted: true };
    },
    async scanAndPreviewPairing() {
      return {
        previewId: "scan-preview",
        origin: "https://hub.example.com:8443",
      };
    },
    async clipboardPaste() {
      return "";
    },
    async requestPermission(kind) {
      return { kind, granted: true };
    },
    async speechStart() {},
    async speechStop() {},
    async synthesisSpeak() {},
    async synthesisStop() {},
    async hapticPerform(kind) {
      hapticCalls.push(kind);
    },
    async getContentSize() {
      return "large";
    },
    onLifecycle() {
      return () => {};
    },
  };

  return {
    profile,
    native,
    hapticCalls,
    contentSize: () => "large",
  };
}
