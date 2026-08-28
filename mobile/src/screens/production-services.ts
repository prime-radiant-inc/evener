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
import type {
  AnyNotification,
  InitializeResponse,
  MethodName,
  MethodTypes,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
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

export interface ProfileClientTransport extends ConversationClientLike {
  connect(): Promise<InitializeResponse>;
  close(): void;
  onStateChange(handler: (state: string) => void): () => void;
  onHandshakeResult(handler: (result: InitializeResponse) => void): () => void;
}

export interface ProfileAppwireClient extends ProfileClientTransport {
  setActive(active: boolean): void;
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
) => ProfileClientTransport;

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
  const client = new LeaseAwareProfileClient(createClient(profile));
  return {
    client,
    rosterService: createRosterService(client),
    rosterStore: createRosterStore(),
    newSessionService: createNewSessionService(client),
    conversationService: createConversationService(client),
  };
}

class LeaseAwareProfileClient implements ProfileAppwireClient {
  private active = true;
  private disposed = false;
  private readonly pendingSettlements = new Set<CoordinatedRequest>();

  constructor(private readonly transport: ProfileClientTransport) {}

  setActive(active: boolean): void {
    if (this.disposed) return;
    this.active = active;
    if (active) {
      const pending = [...this.pendingSettlements];
      for (const settlement of pending) settlement.deliver();
    }
  }

  request<M extends MethodName>(
    method: M,
    params: MethodTypes[M]["params"],
    opts?: { timeoutMs?: number },
  ): Promise<MethodTypes[M]["result"]> {
    if (!this.active || this.disposed) {
      return Promise.reject(new Error("profile scope is inactive"));
    }
    const { request, promise } =
      this.createCoordinatedRequest<MethodTypes[M]["result"]>();
    this.pendingSettlements.add(request);
    let raw: Promise<MethodTypes[M]["result"]>;
    try {
      raw = this.transport.request(method, params, opts);
    } catch (cause) {
      request.settle(() => promise.reject(cause));
      return promise.value;
    }
    void raw.then(
      (value) => request.settle(() => promise.resolve(value)),
      (cause: unknown) => request.settle(() => promise.reject(cause)),
    );
    return promise.value;
  }

  onNotification(handler: (notification: AnyNotification) => void): () => void {
    return this.transport.onNotification((notification) => {
      if (this.active) handler(notification);
    });
  }

  onHandshakeResult(handler: (result: InitializeResponse) => void): () => void {
    return this.transport.onHandshakeResult((result) => {
      if (this.active && !this.disposed) handler(result);
    });
  }

  connect(): Promise<InitializeResponse> {
    return this.transport.connect();
  }

  close(): void {
    if (this.disposed) return;
    this.active = false;
    this.disposed = true;
    const pending = [...this.pendingSettlements];
    for (const settlement of pending) settlement.suppress();
    this.transport.close();
  }

  onStateChange(handler: (state: string) => void): () => void {
    return this.transport.onStateChange((state) => {
      if (this.active) handler(state);
    });
  }

  private createCoordinatedRequest<T>(): {
    readonly request: CoordinatedRequest;
    readonly promise: {
      readonly value: Promise<T>;
      readonly resolve: (value: T) => void;
      readonly reject: (cause?: unknown) => void;
    };
  } {
    let resolvePromise: (value: T) => void = () => {};
    let rejectPromise: (cause?: unknown) => void = () => {};
    const value = new Promise<T>((resolve, reject) => {
      resolvePromise = resolve;
      rejectPromise = reject;
    });
    let completed = false;
    let delivery: () => void = () => {};
    const complete = (completion: () => void): void => {
      if (completed) return;
      completed = true;
      this.pendingSettlements.delete(request);
      completion();
    };
    const request: CoordinatedRequest = {
      settle: (nextDelivery) => {
        delivery = () => complete(nextDelivery);
        this.settleRequest(request);
      },
      deliver: () => delivery(),
      suppress: () =>
        complete(() => rejectPromise(new Error("profile scope is inactive"))),
    };
    return {
      request,
      promise: { value, resolve: resolvePromise, reject: rejectPromise },
    };
  }

  private settleRequest(request: CoordinatedRequest): void {
    if (this.disposed) {
      request.suppress();
      return;
    }
    if (this.active) {
      request.deliver();
    }
  }
}

interface CoordinatedRequest {
  readonly settle: (delivery: () => void) => void;
  readonly deliver: () => void;
  readonly suppress: () => void;
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

export function createBrowserConceptStorage(): ConceptStorage {
  return {
    read: () => {
      const storage = localStorageOrNull();
      if (storage === null) return null;
      try {
        return storage.getItem(CONCEPT_STORAGE_KEY);
      } catch {
        return null;
      }
    },
    write: (conceptId) => {
      const storage = localStorageOrNull();
      if (storage === null) return;
      try {
        storage.setItem(CONCEPT_STORAGE_KEY, conceptId);
      } catch {
        // Selected-concept persistence is best-effort; in-memory UI still moves.
      }
    },
    remove: () => {
      const storage = localStorageOrNull();
      if (storage === null) return;
      try {
        storage.removeItem(CONCEPT_STORAGE_KEY);
      } catch {
        // Removal is best-effort for the same unavailable-storage boundary.
      }
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
