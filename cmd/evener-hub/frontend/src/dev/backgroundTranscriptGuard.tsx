// Dev-only budget installation and read-only evidence for backgroundtranscriptguard.
// The real app imports its unchanged ordinary stores after the budget boundary is set.
// No ref selects a fixture route, and no separate transcript database is created.
import { installBackgroundTranscriptOptionsForTests } from "../stores/backgroundTranscriptOptions";
import { SESSION_CACHE_DATABASE_NAME, SESSION_CACHE_DATABASE_VERSION } from "../stores/sessionCacheSchema";

const entryProduction = new URLSearchParams(window.location.search).get("entry-production") === "true";
// Entry reads need 23,810 payload bytes plus 27,708 competing bytes. Reserve a
// second full envelope, rounded up to a KiB, without changing sustained pressure.
installBackgroundTranscriptOptionsForTests({
  residentBytes: 64 * 1024,
  pendingBytes: entryProduction ? 101 * 1024 : 32 * 1024,
});
(window as Window & { backgroundTranscriptBooted?: boolean }).backgroundTranscriptBooted = true;

// This harness document is already loaded. Put the ordinary app on its home
// route without navigating away from the dev entry.
window.history.replaceState(null, "", "/");

try {
  await import("../main");
  const threads = await import("../stores/threads");
  const connection = await import("../stores/connection");
  const workspace = await import("../shell/workspace");
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
        const route = threads.productionEntryTranscriptForTests(ref);
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
        const route = threads.productionEntryTranscriptForTests(ref);
        const store = threads.threadsStore.getState().threads.get(ref);
        const items = (store?.turns ?? []).flatMap((turn) => turn.items ?? []);
        return {
          present: route.repository !== undefined,
          historyKey: route.historyKey ?? null,
          storeModelPresent: store !== undefined,
          localOpening: threads.isEntryTranscriptLocalOpening(ref),
          storeItemCount: items.length,
          storeNonEmptyItems: items.filter((item) => (item.text ?? "") !== "" || (item.output ?? "") !== "").length,
          lastLocalOpenStatus: route.lastLocalOpenStatus ?? null,
        };
      },
      // Read-only recovery state: why a local open does or does not report
      // "current" for the entry route, measured rather than inferred.
      entryRecovery: (ref: string) => {
        const route = threads.productionEntryTranscriptForTests(ref);
        const repository = route.repository;
        if (!repository) return { repositoryPresent: false };
        return { repositoryPresent: true, ...repository.getRecoveryStateForTesting() };
      },
    }),
  });
  // Dev-only two-tab fencing surface. It exercises the REAL cache clear/delete
  // paths (threads.clearCachedSessions / markCacheSessionsDeleted), the real
  // BroadcastChannel propagation and the real IndexedDB across two real tabs.
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
      settle: (ref: string) => threads.settleEntryTranscriptForTests(ref),
      clear: () => threads.clearCachedSessions(),
      delete: (ref: string) => {
        threads.markCacheSessionsDeleted([ref]);
      },
    }),
  });
} catch (error) {
  (window as Window & { __backgroundGuardBootError?: string }).__backgroundGuardBootError = String(
    (error as { stack?: string })?.stack ?? error,
  );
  throw error;
}
