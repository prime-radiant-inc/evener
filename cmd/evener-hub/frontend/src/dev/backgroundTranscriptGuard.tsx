// Dev-only budget installation and read-only evidence for backgroundtranscriptguard.
// The real app imports its unchanged ordinary stores after the budget boundary is set.
// No ref selects a fixture route, and no separate transcript database is created.
import type { TranscriptDisplayConfigV1, ViewportClass } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { installBackgroundTranscriptOptionsForTests } from "../stores/backgroundTranscriptOptions";
import { SESSION_CACHE_DATABASE_NAME, SESSION_CACHE_DATABASE_VERSION } from "../stores/sessionCacheSchema";

const query = new URLSearchParams(window.location.search);
const entryProduction = query.get("entry-production") === "true";
const displayTransport = query.get("display-transport");
const displayDocument = displayTransport === "broadcast" || displayTransport === "storage";
const displayStorageBlocked = displayTransport === "broadcast";
const displayStorage: {
  key: string;
  old_value: string | null;
  new_value: string | null;
  url: string;
  trusted: boolean;
  local_storage: boolean;
  blocked: boolean;
}[] = [];
if (displayDocument) {
  // Attach before importing the app and its production storage listener. Only
  // the native-channel documents suppress the fallback, so storage cannot
  // conceal a broken BroadcastChannel receive path.
  window.addEventListener(
    "storage",
    (event) => {
      if (
        event.key !== "evener.prefs.transcriptDisplay.desktop" &&
        event.key !== "evener.prefs.transcriptDisplay.mobile"
      )
        return;
      displayStorage.push({
        key: event.key,
        old_value: event.oldValue,
        new_value: event.newValue,
        url: event.url,
        trusted: event.isTrusted,
        local_storage: event.storageArea === window.localStorage,
        blocked: displayStorageBlocked,
      });
      if (displayStorageBlocked) event.stopImmediatePropagation();
    },
    true,
  );
}
// Entry reads need 23,810 payload bytes plus 27,708 competing bytes. Reserve a
// second full envelope, rounded up to a KiB, without changing sustained pressure.
installBackgroundTranscriptOptionsForTests({
  residentBytes: 64 * 1024,
  pendingBytes: entryProduction ? 101 * 1024 : 32 * 1024,
});
(window as Window & { backgroundTranscriptBooted?: boolean }).backgroundTranscriptBooted = true;

// This harness document is already loaded. Put the ordinary app on its home
// route without navigating away from the dev entry.
// Display documents retain their distinct source URLs for native storage proof.
window.history.replaceState(null, "", displayDocument ? `/?${query}` : "/");

try {
  await import("../main");
  const threads = await import("../stores/threads");
  const routes = await import("../stores/entryTranscriptRoutes");
  const connection = await import("../stores/connection");
  const workspace = await import("../shell/workspace");
  if (displayDocument) {
    const display = await import("../stores/transcriptDisplay");
    const { LOCAL_KEYS } = await import("../stores/transcriptDisplay/localStore");
    // Idempotent, and deliberately BEFORE the independent native observer.
    // Its receipt is a delivery barrier after the production receiver's task.
    display.initTranscriptDisplay();
    const publications: Partial<Record<ViewportClass, TranscriptDisplayConfigV1>>[] = [];
    display.transcriptDisplayStore.subscribe((state, previous) => {
      if (state.local !== previous.local) publications.push(structuredClone(state.local));
    });
    const messages: unknown[] = [];
    const observer =
      typeof BroadcastChannel === "undefined" ? null : new BroadcastChannel(display.TRANSCRIPT_DISPLAY_CHANNEL);
    observer?.addEventListener("message", (event: MessageEvent<unknown>) => messages.push(event.data));
    Object.defineProperty(window, "backgroundDisplayGuard", {
      value: Object.freeze({
        setLocal: (layout: ViewportClass, config: TranscriptDisplayConfigV1) =>
          display.transcriptDisplayStore.getState().setLocal(layout, config),
        clearLocal: (layout: ViewportClass) => display.transcriptDisplayStore.getState().clearLocal(layout),
        resetObservations: () => {
          publications.length = 0;
          messages.length = 0;
          displayStorage.length = 0;
        },
        snapshot: () => {
          const state = display.transcriptDisplayStore.getState();
          return structuredClone({
            local: state.local,
            effective: { desktop: state.effective("desktop"), mobile: state.effective("mobile") },
            publications,
            messages,
            storage: displayStorage,
            persisted: {
              desktop: localStorage.getItem(LOCAL_KEYS.desktop),
              mobile: localStorage.getItem(LOCAL_KEYS.mobile),
            },
            broadcast_channel: typeof BroadcastChannel,
            storage_blocked: displayStorageBlocked,
            origin: location.origin,
            href: location.href,
          });
        },
      }),
    });
  }
  const paneIdentities = new WeakMap<object, number>();
  let nextPaneIdentity = 0;
  Object.defineProperty(window, "backgroundTranscriptEvidence", {
    value: Object.freeze({
      panes: (ref: string) =>
        workspace.workspaceStore
          .getState()
          .panes.filter((pane) => pane.type === "session" && (pane.params as { ref?: string }).ref === ref)
          .map((pane) => {
            let identity = paneIdentities.get(pane);
            if (identity === undefined) {
              identity = ++nextPaneIdentity;
              paneIdentities.set(pane, identity);
            }
            return { id: pane.id, identity };
          }),
      snapshot: threads.backgroundTranscriptSnapshotForTests,
      settled: threads.settleBackgroundTranscriptsForTests,
      entrySettled: (ref: string) => threads.settleEntryTranscriptForTests(ref),
      model: (ref: string) => threads.threadsStore.getState().threads.get(ref),
      entryCacheDatabase: SESSION_CACHE_DATABASE_NAME,
      entryCacheVersion: SESSION_CACHE_DATABASE_VERSION,
      connectionState: () => connection.connectionStore.getState().state,
      // The ordinary entry route's own accounting, read through the same store
      // binding the app uses. The shared write admission covers every entry
      // repository writing through the real cache, so its retained bytes are
      // the bounded shared budget the sustained workload is charged against.
      entryAdmission: (ref: string) => {
        const route = routes.productionEntryTranscriptForTests(ref);
        const repository = route.repository;
        if (!repository) return null;
        const admission = repository.getAdmissionSnapshotForTesting();
        return {
          retainedBytes: admission.retainedBytes,
          retainedIdentities: admission.retainedIdentities,
          exemptBytes: admission.exemptBytes,
          backgroundInFlight: admission.backgroundInFlight,
        };
      },
      entryRoute: (ref: string) => {
        const route = routes.productionEntryTranscriptForTests(ref);
        const store = threads.threadsStore.getState().threads.get(ref);
        const items = (store?.turns ?? []).flatMap((turn) => turn.items ?? []);
        return {
          present: route.repository !== undefined,
          historyKey: route.historyKey ?? null,
          storeModelPresent: store !== undefined,
          localOpening: routes.isEntryTranscriptLocalOpening(ref),
          storeItemCount: items.length,
          storeNonEmptyItems: items.filter((item) => (item.text ?? "") !== "" || (item.output ?? "") !== "").length,
          lastLocalOpenStatus: route.lastLocalOpenStatus ?? null,
        };
      },
      // Read-only recovery state: why a local open does or does not report
      // "current" for the entry route, measured rather than inferred.
      entryRecovery: (ref: string) => {
        const route = routes.productionEntryTranscriptForTests(ref);
        const repository = route.repository;
        if (!repository) return { repositoryPresent: false };
        return { repositoryPresent: true, ...repository.getRecoveryStateForTesting() };
      },
    }),
  });
  // Dev-only two-tab fencing surface. Deletion uses the production action after
  // the real daemon exits, so a sibling's fresh read meets a server-side fence,
  // not merely a cache broadcast while the source session is still readable.
  // It is a guard entry, never a product route: no ref selects a fixture and no
  // separate store is created.
  Object.defineProperty(window, "backgroundTranscriptGuard", {
    value: Object.freeze({
      fenceState: (ref: string) => {
        const state = threads.threadsStore.getState();
        return {
          deletedRefs: [...state.deletedRefs].sort(),
          lifetime: state.cacheLifetimes.get(ref) ?? null,
          clearInFlight: state.clearInFlight ?? null,
        };
      },
      open: (ref: string) => threads.threadsStore.getState().ensureThread(ref),
      sourceReadOutcome: async (ref: string) => {
        const client = connection.connectionStore.getState().client;
        if (!client) throw new Error("No connected client for the deletion proof");
        try {
          await client.request("thread/read", { ref, includeTurns: true, subscribe: false });
          return "readable";
        } catch (error) {
          if (!(error instanceof WireError)) throw error;
          return "rejected";
        }
      },
      settle: (ref: string) => threads.settleEntryTranscriptForTests(ref),
      clear: () => threads.clearCachedSessions(),
      delete: async (key: string, workingDir: string) => {
        const { deleteProject } = await import("../shell/rail/actions");
        return deleteProject(key, workingDir);
      },
    }),
  });
} catch (error) {
  (window as Window & { __backgroundGuardBootError?: string }).__backgroundGuardBootError = String(
    (error as { stack?: string })?.stack ?? error,
  );
  throw error;
}
