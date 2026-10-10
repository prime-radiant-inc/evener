// Dev-only budget installation and read-only evidence for backgroundtranscriptguard.
// The real app imports its unchanged ordinary stores after the budget boundary is set.
// No ref selects a fixture route, and no separate transcript database is created.
import { installBackgroundTranscriptOptionsForTests } from "../stores/backgroundTranscriptOptions";
import { SESSION_CACHE_DATABASE_NAME, SESSION_CACHE_DATABASE_VERSION } from "../stores/sessionCacheSchema";

installBackgroundTranscriptOptionsForTests({ residentBytes: 64 * 1024, pendingBytes: 32 * 1024 });
(window as Window & { backgroundTranscriptBooted?: boolean }).backgroundTranscriptBooted = true;

// This harness document is already loaded. Put the ordinary app on its home
// route without navigating away from the dev entry.
window.history.replaceState(null, "", "/");

try {
  await import("../main");
  const threads = await import("../stores/threads");
  const connection = await import("../stores/connection");
  Object.defineProperty(window, "backgroundTranscriptEvidence", {
    value: Object.freeze({
      snapshot: threads.backgroundTranscriptSnapshotForTests,
      settled: threads.settleBackgroundTranscriptsForTests,
      entrySettled: (ref: string) => threads.settleEntryTranscriptForTests(ref),
      model: (ref: string) => threads.threadsStore.getState().threads.get(ref),
      entryCacheDatabase: SESSION_CACHE_DATABASE_NAME,
      entryCacheVersion: SESSION_CACHE_DATABASE_VERSION,
      connectionState: () => connection.connectionStore.getState().state,
    }),
  });
} catch (error) {
  (window as Window & { __backgroundGuardBootError?: string }).__backgroundGuardBootError = String(
    (error as { stack?: string })?.stack ?? error,
  );
  throw error;
}
