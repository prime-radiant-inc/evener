/**
 * LifecycleCoordinator — orchestrates background/foreground transitions for the
 * mobile conversation experience.
 *
 * One active profile AppWire connection feeds strict thread reduction. When
 * the app backgrounds (`tauri://suspended`), the coordinator closes the
 * active-profile AppWire socket, cancels in-flight HTTP/uploads, revokes object
 * URLs, deletes temp attachment handles, and ends voice placeholder state —
 * while preserving profile/session-keyed process-memory drafts. It also
 * rejects stale events from inactive profiles by incrementing the connection
 * generation.
 *
 * When the app foregrounds (`tauri://resumed`), the coordinator probes
 * reachability, reconnects the AppWire socket, refreshes capabilities, and
 * rehydrates the selected profile's conversation before enabling mutation.
 *
 * The coordinator operates on injected store/service handles so tests can
 * substitute fakes. It never imports Tauri, HTTP, or AppWire directly — the
 * stores and services own transport. The coordinator is the single place where
 * lifecycle ordering is enforced.
 *
 * Contract invariants (verified by mobile-lifecycle.test.mjs):
 * 1. Backgrounding closes the active-profile AppWire socket.
 * 2. Backgrounding cancels HTTP/uploads.
 * 3. Backgrounding revokes object URLs.
 * 4. Backgrounding deletes temp attachment handles.
 * 5. Backgrounding ends voice placeholder state.
 * 6. Backgrounding preserves profile/session-keyed process-memory drafts.
 * 7. Backgrounding rejects stale events from inactive profiles.
 * 8. Foregrounding probes/reconnects/refreshes/rehydrates before enabling mutation.
 */

// --- injected handle shapes (structural — compatible with real stores) ------

/**
 * The AppWire socket handle the coordinator closes on background.
 */
export interface AppWireSocketHandle {
  /** Close the socket. Idempotent — safe to call on an already-closed socket. */
  close(): void;
  /** True if the socket is open. */
  readonly isOpen: boolean;
}

/**
 * The HTTP/upload handle the coordinator cancels on background.
 */
export interface HttpCancelHandle {
  /** Abort all in-flight HTTP requests and uploads. */
  abortAll(): void;
  /** True if there are in-flight requests. */
  readonly hasInFlight: boolean;
}

/**
 * Object URL manager — tracks created object URLs for revocation on background.
 */
export interface ObjectUrlHandle {
  /** Revoke all tracked object URLs. */
  revokeAll(): void;
  /** Register an object URL for later revocation. */
  track(url: string): void;
  /** True if any object URLs are tracked. */
  readonly hasTracked: boolean;
}

/**
 * Temp attachment handle manager — deletes temp native handles on background.
 */
export interface TempAttachmentHandle {
  /** Delete all temp attachment handles. */
  deleteAll(): void;
  /** Register a temp handle for later deletion. */
  track(handle: string): void;
  /** True if any temp handles are tracked. */
  readonly hasTracked: boolean;
}

/**
 * Voice placeholder handle — ends voice state on background.
 */
export interface VoicePlaceholderHandle {
  /** End any active voice placeholder state. */
  endPlaceholder(): void;
  /** True if voice placeholder is active. */
  readonly isActive: boolean;
}

/**
 * Draft store — preserves profile/session-keyed process-memory drafts across
 * background/foreground. Drafts are keyed by profileId + sessionId so switching
 * profiles does not lose work. The coordinator never clears drafts on
 * background — only on explicit send or profile switch.
 */
export interface DraftHandle {
  /** Read the current draft text for a profile/session key. */
  getDraft(profileId: string, sessionId: string): string;
  /** Set the draft text for a profile/session key. */
  setDraft(profileId: string, sessionId: string, text: string): void;
  /** Clear the draft for a profile/session key (on send, not background). */
  clearDraft(profileId: string, sessionId: string): void;
  /** List all draft keys (for verification). */
  readonly draftKeys: readonly string[];
}

/**
 * Conversation store handle — the coordinator closes the conversation on
 * background and rehydrates on foreground. The generation counter rejects
 * stale frames.
 */
export interface ConversationLifecycleHandle {
  /** Close the active conversation (keeps draft, increments generation). */
  close(): void;
  /** Reset the conversation store to idle. */
  reset(): void;
  /** Rehydrate a conversation from a ref after foregrounding. */
  rehydrate(ref: string): Promise<void>;
  /** The current conversation ref (null if closed). */
  readonly ref: string | null;
  /**
   * The last conversation ref before backgrounding — used to rehydrate on
   * foreground. Stays non-null even after close() so the coordinator knows
   * which conversation to restore.
   */
  readonly lastRef: string | null;
  /** The current conversation generation. */
  readonly generation: number;
  /** True if mutation is enabled (false during rehydration). */
  readonly mutationEnabled: boolean;
  /** Enable mutation after rehydration completes. */
  enableMutation(): void;
}

/**
 * Connection store handle — the coordinator manages profile generation and
 * reachability across background/foreground.
 */
export interface ConnectionLifecycleHandle {
  /** The active profile ID. */
  readonly activeProfileId: string | null;
  /** The connection generation (incremented on background to reject stale events). */
  readonly generation: number;
  /** Increment the generation, rejecting stale events from the prior profile. */
  incrementGeneration(): void;
  /** Probe reachability and reconnect for the active profile. */
  probeAndReconnect(): Promise<void>;
  /** Refresh capabilities and profile health. */
  refresh(): Promise<void>;
  /** Clear server-scoped state (roster/conversation/activity). */
  clearServerScopedState(): void;
}

/**
 * Attachment store handle — clears temp attachments on background.
 */
export interface AttachmentLifecycleHandle {
  /** Clear all pending attachments. */
  clear(): void;
  /** True if there are pending attachments. */
  readonly hasAttachments: boolean;
}

// --- the coordinator ---------------------------------------------------------

export interface LifecycleCoordinatorDeps {
  readonly socket: AppWireSocketHandle;
  readonly http: HttpCancelHandle;
  readonly objectUrls: ObjectUrlHandle;
  readonly tempAttachments: TempAttachmentHandle;
  readonly voice: VoicePlaceholderHandle;
  readonly drafts: DraftHandle;
  readonly conversation: ConversationLifecycleHandle;
  readonly connection: ConnectionLifecycleHandle;
  readonly attachments: AttachmentLifecycleHandle;
}

export interface LifecycleCoordinator {
  /** Handle a background transition (`tauri://suspended`). */
  onBackground(): void;
  /** Handle a foreground transition (`tauri://resumed`). */
  onForeground(): Promise<void>;
  /** Reject a stale event from an inactive profile. Returns true if rejected. */
  rejectStaleEvent(profileId: string, generation: number): boolean;
}

/**
 * Create a LifecycleCoordinator that enforces the background/foreground contract.
 *
 * Background ordering:
 *  1. Increment connection generation (reject stale events before closing).
 *  2. Close the AppWire socket.
 *  3. Abort all in-flight HTTP/uploads.
 *  4. Revoke all object URLs.
 *  5. Delete all temp attachment handles.
 *  6. End voice placeholder state.
 *  7. Clear pending attachments.
 *  8. Close the conversation (but preserve drafts).
 *
 * Foreground ordering:
 *  1. Probe reachability and reconnect.
 *  2. Refresh capabilities and profile health.
 *  3. Rehydrate the conversation from the last ref.
 *  4. Enable mutation only after rehydration completes.
 */
export function createLifecycleCoordinator(
  deps: LifecycleCoordinatorDeps,
): LifecycleCoordinator {
  return {
    onBackground() {
      // 1. Increment generation first so any in-flight event from the active
      //    profile is rejected before we start closing resources.
      deps.connection.incrementGeneration();

      // 2. Close the AppWire socket.
      deps.socket.close();

      // 3. Abort all in-flight HTTP requests and uploads.
      deps.http.abortAll();

      // 4. Revoke all tracked object URLs.
      deps.objectUrls.revokeAll();

      // 5. Delete all temp attachment handles.
      deps.tempAttachments.deleteAll();

      // 6. End voice placeholder state.
      deps.voice.endPlaceholder();

      // 7. Clear pending attachments (temp handles already deleted).
      deps.attachments.clear();

      // 8. Close the conversation — but do NOT clear drafts. Drafts are
      //    profile/session-keyed in process memory and survive background.
      deps.conversation.close();
    },

    async onForeground() {
      // 1. Probe reachability and reconnect the AppWire socket.
      await deps.connection.probeAndReconnect();

      // 2. Refresh capabilities and profile health.
      await deps.connection.refresh();

      // 3. Rehydrate the conversation from the last ref (if any). The lastRef
      //    survives close() on background so we know which conversation to
      //    restore.
      const ref = deps.conversation.lastRef;
      if (ref !== null) {
        await deps.conversation.rehydrate(ref);
      }

      // 4. Enable mutation only after rehydration completes.
      deps.conversation.enableMutation();
    },

    rejectStaleEvent(profileId: string, generation: number): boolean {
      // Reject if the event's profile doesn't match the active profile, or
      // if the event's generation is older than the current connection generation.
      const activeId = deps.connection.activeProfileId;
      if (activeId !== null && profileId !== activeId) {
        return true;
      }
      return generation < deps.connection.generation;
    },
  };
}
