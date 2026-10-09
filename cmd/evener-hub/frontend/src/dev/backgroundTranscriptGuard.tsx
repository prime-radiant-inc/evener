// Dev-only entry for backgroundtranscriptguard. It is loaded ONLY by the
// guard's nested HTML (scripts/backgroundtranscriptguard/index.html), which is
// not part of the production build inputs or the served public assets.
//
// The one installation the browser guard may not do from test code is the
// budget install: Task 5's tiny budgets must be in effect BEFORE the real app
// imports its stores, or the run would exercise the production budgets instead
// of the eviction path under test. So the installer runs first, here, and the
// real app (../main) is imported dynamically after it. The evidence global is
// read-only: it exposes the store's own snapshot and settle functions, never a
// product endpoint or a mutable control surface.
import { installBackgroundTranscriptOptionsForTests } from "../stores/backgroundTranscriptOptions";
import type { SessionEntryCache } from "../stores/sessionEntryCache";
import type { installEntryTranscriptFixtureForTests } from "../stores/threads";

// The budget install is the one thing that must happen before the real app
// imports its stores, so it runs first, synchronously. The synchronous boot
// marker is set immediately after it: the driver's boot check must not wait on
// the dynamic import below, whose top-level await leaves the document live but
// bootless until the whole app module graph has loaded.
installBackgroundTranscriptOptionsForTests({ residentBytes: 64 * 1024, pendingBytes: 32 * 1024 });
(window as Window & { backgroundTranscriptBooted?: boolean }).backgroundTranscriptBooted = true;

const entryRef = new URLSearchParams(window.location.search).get("entryRef");
let entryCache: SessionEntryCache | undefined;
let entryFixture: ReturnType<typeof installEntryTranscriptFixtureForTests> | undefined;
if (entryRef) {
  const [{ SessionEntryCache }, threads] = await Promise.all([
    import("../stores/sessionEntryCache"),
    import("../stores/threads"),
  ]);
  entryCache = new SessionEntryCache({ databaseName: "background-transcript-entry-slice" });
  entryFixture = threads.installEntryTranscriptFixtureForTests({
    ref: entryRef,
    cache: entryCache,
    clientId: "entry-slice-browser-client",
    hostId: "entry-slice-browser-host",
  });
}

// The nested harness is served from /scripts/backgroundtranscriptguard/, a path
// the app's router does not resolve; AppShell would render NotFound instead of
// the rail. The document is already the harness, so resetting the address to
// the app's home lets the real shell (rail included) mount without navigating.
window.history.replaceState(null, "", "/");

try {
  await import("../main");

  const threads = await import("../stores/threads");
  // Same module graph as the app (the app imports this same resolved module),
  // so this is the app's own connection state, not a second instance.
  const connection = await import("../stores/connection");

  Object.defineProperty(window, "backgroundTranscriptEvidence", {
    value: Object.freeze({
      snapshot: threads.backgroundTranscriptSnapshotForTests,
      settled: threads.settleBackgroundTranscriptsForTests,
      entrySettled: () => entryFixture?.settled() ?? Promise.resolve(),
      // Read-only view of the SAME threads binding the app uses. The driver
      // reads literal target content through this, never through a second
      // `import("/src/stores/threads")` that would resolve another instance.
      model: (ref: string) => threads.threadsStore.getState().threads.get(ref),
      entryFixture: () => entryFixture?.inspect() ?? null,
      entryCacheDatabase: entryRef ? "background-transcript-entry-slice" : null,
      // Read-only view of the app's own connection state for the disconnection
      // observation; never a mutable control surface.
      connectionState: () => connection.connectionStore.getState().state,
    }),
  });
} catch (error) {
  // The guard's driver reads this to report WHY the harness never booted: a
  // top-level rejection otherwise surfaces as only "the global is missing".
  (window as Window & { __backgroundGuardBootError?: string }).__backgroundGuardBootError = String(
    (error as { stack?: string })?.stack ?? error,
  );
  throw error;
}

window.addEventListener("pagehide", () => {
  entryFixture?.dispose();
  entryCache?.close();
});
