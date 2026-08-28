/**
 * Production service factory — wires real Tauri-backed services.
 *
 * The real {@link TauriBridge} delegates to `@tauri-apps/api` (imported from
 * exactly one seam: `services/tauri.ts`). The real {@link ProfileService}
 * wraps the Tauri bridge. The real {@link NativeBridge} wraps the production
 * transport adapter ({@link createTauriNativeTransport}) that invokes actual
 * registered plugin commands via namespaced routes.
 *
 * This module is imported only by the production App path — never by tests or
 * fixture mode. No credential, raw URL, or token is ever held in JS state.
 */

import { rpcURLFromLocation } from "../../../cmd/evener-hub/frontend/src/protocol/transport";
import type { ConceptStorage } from "../live-concepts/live-ui-store";
import type { NativeBridge } from "../native/client";
import { createNativeBridge } from "../native/client";
import { createAppwireClient } from "../services/appwireSocket";
import {
  type ConversationClientLike,
  createConversationService,
  type LiveConversationService,
} from "../services/conversation";
import type {
  ProfileRedacted,
  ProfileService,
} from "../services/nativeProfiles";
import { createProfileService } from "../services/nativeProfiles";
import {
  createNewSessionService,
  type NewSessionService,
} from "../services/newSession";
import { createRosterService, type RosterService } from "../services/roster";
import { createTauriBridge } from "../services/tauri";
import { createRosterStore } from "../state/roster";
import { createTauriNativeTransport } from "./production-transport";

const CONCEPT_STORAGE_KEY = "evener.live-concept";

export interface ProfileAppwireClient extends ConversationClientLike {
  connect(): Promise<unknown>;
  close(): void;
  onStateChange(handler: (state: string) => void): () => void;
}

export interface ProfileScopedServices {
  readonly client: ProfileAppwireClient;
  readonly rosterService: RosterService;
  readonly rosterStore: ReturnType<typeof createRosterStore>;
  readonly newSessionService: NewSessionService;
  readonly conversationService: LiveConversationService;
}

export type ProfileClientFactory = (
  profile: ProfileRedacted,
) => ProfileAppwireClient;

/**
 * Build all server-scoped services around one AppWire client. The client owns
 * the native socket lifecycle; the wrappers only translate protocol calls.
 * Keeping this graph together prevents a screen from silently falling back to
 * its header-only fixture path in production.
 */
export function createProfileScopedServices(
  profile: ProfileRedacted,
  createClient: ProfileClientFactory,
): ProfileScopedServices {
  const client = createClient(profile);
  return {
    client,
    rosterService: createRosterService(client),
    rosterStore: createRosterStore(),
    newSessionService: createNewSessionService(client),
    conversationService: createConversationService(client),
  };
}

export interface ProductionServices {
  readonly profile: ProfileService;
  readonly native: NativeBridge;
  readonly conceptStorage: ConceptStorage;
  readonly createProfileScopedServices: (
    profile: ProfileRedacted,
  ) => ProfileScopedServices;
}

/**
 * Build the production service bundle backed by real Tauri IPC. This is the
 * only path that imports `createTauriBridge` and `createProfileService` for
 * live use — fixture mode injects fakes instead.
 */
export function createProductionServices(): ProductionServices {
  const bridge = createTauriBridge();
  const profile = createProfileService(bridge);
  const native = createNativeBridge(createTauriNativeTransport(bridge));
  const createClient: ProfileClientFactory = (profileSummary) => {
    const origin = new URL(profileSummary.origin);
    return createAppwireClient({
      bridge,
      profileId: profileSummary.id,
      url: rpcURLFromLocation(origin),
    });
  };
  return {
    profile,
    native,
    conceptStorage: createBrowserConceptStorage(),
    createProfileScopedServices: (profileSummary) =>
      createProfileScopedServices(profileSummary, createClient),
  };
}

function createBrowserConceptStorage(): ConceptStorage {
  return {
    read: () => localStorageOrNull()?.getItem(CONCEPT_STORAGE_KEY) ?? null,
    write: (conceptId) => {
      localStorageOrNull()?.setItem(CONCEPT_STORAGE_KEY, conceptId);
    },
    remove: () => {
      localStorageOrNull()?.removeItem(CONCEPT_STORAGE_KEY);
    },
  };
}

function localStorageOrNull(): Storage | null {
  if (typeof window === "undefined") return null;
  try {
    return window.localStorage ?? null;
  } catch {
    return null;
  }
}
