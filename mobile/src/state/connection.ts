/**
 * Connection store — multi-profile state, reachability, switching, and
 * atomic add/edit/re-pair/remove.
 *
 * Wraps a {@link ProfileService} (real `nativeProfiles` in production, fake in
 * tests/fixture). Never stores a credential, raw URL, or token in state: the
 * service holds secrets in native memory and returns only redacted
 * `{id,name,origin}` summaries and opaque preview IDs. Errors are mapped to
 * redacted codes — the raw backend message (which may carry a secret) is
 * never retained.
 *
 * Switching clears server-scoped placeholder state and increments the
 * connection generation so late frames from the old server are rejected.
 */

import { create } from "zustand";
import type {
  HealthSnapshot,
  ProfilePreview,
  ProfileRedacted,
  ProfileService,
  SelectResult,
} from "../services/nativeProfiles";
import { isProfileServiceError } from "../services/nativeProfiles";

export type ConnectionStatus = "initial" | "loading" | "ready" | "error";
export type Reachability = "reachable" | "reconnecting" | "unreachable";

/** A preview holds only the opaque ID + redacted origin. Never a token/query. */
export interface ConnectionPreview {
  readonly previewId: string;
  readonly origin: string;
  readonly isPrivateNetwork: boolean;
}

/** Input for paste preview — the raw URL is transient and never persisted. */
export interface PreviewInput {
  readonly raw: string;
}

export interface ConnectionState {
  readonly profiles: readonly ProfileRedacted[];
  readonly activeProfileId: string | null;
  readonly generation: number;
  readonly status: ConnectionStatus;
  readonly reachability: Readonly<Record<string, Reachability>>;
  readonly preview: ConnectionPreview | null;
  readonly previewError: string | null;
  /** Server-scoped placeholder state (roster/conversation); cleared on switch. */
  readonly __serverScopedState: unknown | null;
  /** Test seam: seed server-scoped state to prove switch clears it. */
  __seedServerScopedState(value: unknown): void;

  refresh(): Promise<void>;
  /** Accept a raw URL string or `{raw}`; the raw value is never persisted. */
  previewPaste(input: string | PreviewInput): Promise<void>;
  previewRepair(input: {
    readonly profileId: string;
    readonly raw: string;
  }): Promise<void>;
  cancelPreview(): Promise<void>;
  confirmPairing(
    previewId: string,
    name: string,
    allowDuplicateOrigin: boolean,
  ): Promise<ProfileRedacted>;
  rePair(
    profileId: string,
    previewId: string,
    name: string,
    allowDuplicateOrigin: boolean,
  ): Promise<ProfileRedacted>;
  rename(profileId: string, newName: string): Promise<ProfileRedacted>;
  remove(profileId: string): Promise<void>;
  switchTo(profileId: string): Promise<void>;
  setReachability(profileId: string, state: Reachability): void;
}

function toRaw(input: string | PreviewInput): string {
  return typeof input === "string" ? input : input.raw;
}

export function createConnectionStore(service: ProfileService) {
  return create<ConnectionState>((set, get) => ({
    profiles: [],
    activeProfileId: null,
    generation: 0,
    status: "initial",
    reachability: {},
    preview: null,
    previewError: null,
    __serverScopedState: null,
    __seedServerScopedState: (value) => set({ __serverScopedState: value }),

    async refresh() {
      set({ status: "loading" });
      try {
        const health: HealthSnapshot = await service.health();
        set({
          profiles: health.profiles,
          activeProfileId: health.activeProfileId,
          generation: health.generation,
          status: "ready",
        });
      } catch {
        set({ status: "error" });
      }
    },

    async previewPaste(input: string | PreviewInput) {
      set({ previewError: null });
      try {
        const result: ProfilePreview = await service.previewPaste({
          raw: toRaw(input),
        });
        set({
          preview: {
            previewId: result.previewId,
            origin: result.origin,
            isPrivateNetwork: isPrivateNetwork(result.origin),
          },
        });
      } catch (cause) {
        set({ preview: null, previewError: redactError(cause) });
      }
    },

    async previewRepair(input) {
      set({ previewError: null });
      try {
        const result: ProfilePreview = await service.previewRepair({
          profileId: input.profileId,
          raw: input.raw,
        });
        set({
          preview: {
            previewId: result.previewId,
            origin: result.origin,
            isPrivateNetwork: isPrivateNetwork(result.origin),
          },
        });
      } catch (cause) {
        set({ preview: null, previewError: redactError(cause) });
      }
    },

    async cancelPreview() {
      const preview = get().preview;
      if (preview === null) return;
      try {
        await service.cancelPreview({ previewId: preview.previewId });
      } catch {
        // best-effort; preview is transient
      }
      set({ preview: null });
    },

    async confirmPairing(previewId, name, allowDuplicateOrigin) {
      // Load the current profile list so name/origin validation runs against
      // the authoritative service state, not a stale snapshot.
      const profiles = await loadProfiles(service, get, set);
      assertNameUnique(profiles, name);
      assertOriginConsent(profiles, get().preview, allowDuplicateOrigin);
      try {
        const profile = await service.confirmPairing({
          previewId,
          name,
          allowDuplicateOrigin,
        });
        await syncFromService(service, set);
        return profile;
      } catch (cause) {
        throw redactedThrow(cause);
      }
    },

    async rePair(profileId, previewId, name, allowDuplicateOrigin) {
      const profiles = await loadProfiles(service, get, set);
      assertNameUniqueForRename(profiles, profileId, name);
      try {
        const profile = await service.confirmPairing({
          previewId,
          name,
          allowDuplicateOrigin,
        });
        await syncFromService(service, set);
        return profile;
      } catch (cause) {
        throw redactedThrow(cause);
      }
    },

    async rename(profileId, newName) {
      const profiles = await loadProfiles(service, get, set);
      assertNameUniqueForRename(profiles, profileId, newName);
      try {
        const profile = await service.rename({ profileId, newName });
        await syncFromService(service, set);
        return profile;
      } catch (cause) {
        throw redactedThrow(cause);
      }
    },

    async remove(profileId) {
      // Load current state so a failure leaves the prior list intact.
      await loadProfiles(service, get, set);
      try {
        const result: SelectResult = await service.remove({ profileId });
        set({
          activeProfileId: result.profileId,
          generation: result.generation,
        });
        await syncFromService(service, set);
      } catch (cause) {
        throw redactedThrow(cause);
      }
    },

    async switchTo(profileId) {
      await loadProfiles(service, get, set);
      const priorActive = get().activeProfileId;
      try {
        const result: SelectResult = await service.select({ profileId });
        set({
          activeProfileId: result.profileId,
          generation: result.generation,
          // Clear server-scoped placeholder state (roster/conversation).
          __serverScopedState: null,
        });
      } catch (cause) {
        // Restore prior active on failure.
        set({ activeProfileId: priorActive });
        throw redactedThrow(cause);
      }
    },

    setReachability: (profileId, state) =>
      set((s) => ({
        reachability: { ...s.reachability, [profileId]: state },
      })),
  }));
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

async function loadProfiles(
  service: ProfileService,
  get: () => ConnectionState,
  set: (partial: Partial<ConnectionState>) => void,
): Promise<readonly ProfileRedacted[]> {
  // If we already have a non-stale list, return it; otherwise fetch health.
  try {
    const health = await service.health();
    const next: {
      profiles?: readonly ProfileRedacted[];
      activeProfileId?: string | null;
      generation?: number;
      status?: ConnectionStatus;
    } = {
      profiles: health.profiles,
      activeProfileId: health.activeProfileId,
      generation: health.generation,
    };
    // Preserve loading/error status unless we were initial.
    if (get().status === "initial") {
      next.status = "ready";
    }
    set(next as Partial<ConnectionState>);
    return health.profiles;
  } catch {
    return get().profiles;
  }
}

async function syncFromService(
  service: ProfileService,
  set: (partial: Partial<ConnectionState>) => void,
): Promise<void> {
  try {
    const health = await service.health();
    set({
      profiles: health.profiles,
      activeProfileId: health.activeProfileId,
      generation: health.generation,
      status: "ready",
    });
  } catch {
    // Keep the prior list on health-read failure.
  }
}

function isPrivateNetwork(origin: string): boolean {
  return origin.startsWith("http://");
}

/**
 * Map any error to a redacted, secret-free message. The raw cause is dropped
 * deliberately: it may carry a token, query, or path.
 */
function redactError(cause: unknown): string {
  if (isProfileServiceError(cause)) {
    return cause.message;
  }
  return "pairing preview failed";
}

function redactedThrow(cause: unknown): never {
  if (isProfileServiceError(cause)) {
    throw cause;
  }
  throw new Error("profile command failed");
}

function normalizeName(name: string): string {
  return name.trim().toLowerCase();
}

function assertNameUnique(
  profiles: readonly ProfileRedacted[],
  name: string,
): void {
  if (name.trim() === "") {
    throw new Error("server name is required");
  }
  const target = normalizeName(name);
  for (const p of profiles) {
    if (normalizeName(p.name) === target) {
      throw new Error("server name must be unique");
    }
  }
}

function assertNameUniqueForRename(
  profiles: readonly ProfileRedacted[],
  profileId: string,
  name: string,
): void {
  if (name.trim() === "") {
    throw new Error("server name is required");
  }
  const target = normalizeName(name);
  for (const p of profiles) {
    if (p.id !== profileId && normalizeName(p.name) === target) {
      throw new Error("server name must be unique");
    }
  }
}

function assertOriginConsent(
  profiles: readonly ProfileRedacted[],
  preview: ConnectionPreview | null,
  allowDuplicateOrigin: boolean,
): void {
  if (preview === null) return;
  const origin = preview.origin;
  for (const p of profiles) {
    if (p.origin === origin && !allowDuplicateOrigin) {
      throw new Error(
        "a server with this origin already exists — confirm to add a second credential",
      );
    }
  }
}
