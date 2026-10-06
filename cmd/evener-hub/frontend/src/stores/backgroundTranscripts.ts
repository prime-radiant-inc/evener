// The web-lifetime background transcript owner: keeps every Live session's
// transcript current before its pane is opened, without expanding the rail.
//
// It walks the complete Live navigation roster through the navigation store's
// background page lease, acquires one background thread lease per eligible
// session ref, and releases a lease only when a coherent, complete sweep proves
// the ref left Live. It owns no renderer state and never enqueues, resumes or
// dispatches a mutation: discovery only reads navigation pages and thread
// snapshots.
import type { NavigationSectionResource, NavigationSessionSummary } from "@evener/appwire-client";
import {
  type BackgroundLivePages,
  keyID,
  type NavigationStore,
  nextNavigationOffset,
  type ResourceKey,
  type ResourceState,
} from "@evener/appwire-client/state/navigation";
import { acquireBackgroundThread, type BackgroundThreadLease } from "./threads";

/** Every Live session ref in a navigation page, walking nested children. A
 * row qualifies only when it is a real session (`kind === "session"`) and
 * currently Live; delegates, archived and ended rows are excluded. Refs stay
 * opaque and client-qualified, so the same id on two hosts yields two refs. */
export function eligibleLiveRefs(rows: readonly NavigationSessionSummary[]): Set<string> {
  const refs = new Set<string>();
  const visit = (row: NavigationSessionSummary): void => {
    if (row.kind === "session" && row.live) refs.add(row.ref);
    for (const child of row.children) visit(child);
  };
  for (const row of rows) visit(row);
  return refs;
}

export interface BackgroundTranscriptsOwner {
  /** Stops accepting navigation changes, then releases every claim. Does not
   * close the shared client; safe to call more than once. */
  dispose(): Promise<void>;
  /** Joins the current sweep and its acquisition/release work, not future
   * retry timers. */
  settled(): Promise<void>;
}

/** A runaway guard for a server that never reports a terminal page; a
 * non-advancing page already ends a sweep, so this is only a safety net. */
const MAX_SWEEP_PAGES = 10_000;

// Bounded, paced retry for an incomplete discovery sweep. The navigation
// revalidator schedules nothing itself, so the owner must retry a failed page
// without spinning on its own publications.
const RETRY_BASE_MS = 250;
const RETRY_MAX_MS = 5_000;
const RETRY_MAX_ATTEMPTS = 6;

export function startBackgroundTranscripts(navigation: NavigationStore): BackgroundTranscriptsOwner {
  let accepting = true;
  let disposed = false;
  let disposePromise: Promise<void> | undefined;
  // The lease covering the current/last coherent sweep's page demand. A
  // coherent sweep replaces it and retires any trailing page it no longer
  // needs; an incomplete or superseded sweep leaves the previous lease in
  // place.
  let pageLease: BackgroundLivePages | null = null;
  const threadLeases = new Map<string, BackgroundThreadLease>();
  const acquisitionWork = new Set<Promise<void>>();
  const releaseWork = new Set<Promise<void>>();
  let sweepPromise: Promise<void> | null = null;
  let sweeping = false;
  let pending = false;
  let scheduledSoon = false;
  let lastInput = "";
  let retryTimer: ReturnType<typeof setTimeout> | null = null;
  let retryAttempt = 0;

  // The external navigation input a sweep acts on, plus the identity of every
  // settled Live page. The settled-page part is what lets a successful
  // same-invalidation recovery (a fresh page published by any owner) restart
  // discovery, while failed/loading pages never appear there and so can never
  // reschedule the owner into a publication loop.
  function inputKey(): string {
    const state = navigation.getState();
    const settled: string[] = [];
    for (const resource of state.resources.values()) {
      if (resource.key.kind !== "section" || resource.key.section !== "live") continue;
      if (resource.stale || resource.loading || resource.error !== null || resource.data === null) continue;
      const sessions = (resource.data as { sessions?: readonly unknown[] } | null)?.sessions?.length ?? 0;
      settled.push(
        `${resource.key.offset}/${resource.key.limit}:${resource.generationID}:${resource.loadedRevision}:${sessions}`,
      );
    }
    settled.sort();
    return `${state.mode}:${state.clientGenerationID}:${state.lastSequence}|${settled.join(",")}`;
  }

  const unsubscribe = navigation.subscribe(() => {
    if (!accepting || disposed) return;
    const input = inputKey();
    if (input === lastInput) return;
    lastInput = input;
    clearRetry();
    scheduleSoon();
  });

  function track(work: Promise<void>, set: Set<Promise<void>>): void {
    set.add(work);
    void work.finally(() => set.delete(work));
  }

  // Defer past the store notification's own synchronous handler: an
  // evener/navigation/invalidated notification publishes lastSequence before
  // it marks the revalidator's resources stale, so a sweep started
  // synchronously would read the pre-invalidation cached pages. Deferring one
  // microtask lets the revalidator's invalidation land first.
  function scheduleSoon(): void {
    if (!accepting || disposed) return;
    pending = true;
    if (scheduledSoon) return;
    scheduledSoon = true;
    queueMicrotask(() => {
      scheduledSoon = false;
      startDrain();
    });
  }

  function startDrain(): void {
    if (!accepting || disposed || sweeping) return;
    sweeping = true;
    sweepPromise = drain().finally(() => {
      sweeping = false;
      sweepPromise = null;
      // A pending input set after the loop's last check (but before sweeping
      // cleared) must still get its sweep.
      if (pending && accepting && !disposed) scheduleSoon();
    });
  }

  async function drain(): Promise<void> {
    while (accepting && !disposed && pending) {
      pending = false;
      await runSweep();
    }
  }

  function clearRetry(): void {
    if (retryTimer !== null) {
      clearTimeout(retryTimer);
      retryTimer = null;
    }
    retryAttempt = 0;
  }

  function armRetry(): void {
    if (!accepting || disposed || retryTimer !== null || retryAttempt >= RETRY_MAX_ATTEMPTS) return;
    const delay = Math.min(RETRY_MAX_MS, RETRY_BASE_MS * 2 ** retryAttempt);
    retryAttempt += 1;
    retryTimer = setTimeout(() => {
      retryTimer = null;
      scheduleSoon();
    }, delay);
  }

  function acquireDiscovered(discovered: ReadonlySet<string>): void {
    for (const ref of discovered) {
      if (disposed || threadLeases.has(ref)) continue;
      const lease = acquireBackgroundThread(ref);
      threadLeases.set(ref, lease);
      // Acquire every ref before awaiting any ready: residency, not discovery
      // order, owns hydration concurrency.
      track(
        lease.ready.then(
          () => undefined,
          () => undefined,
        ),
        acquisitionWork,
      );
    }
  }

  function releaseAbsent(discovered: ReadonlySet<string>): void {
    for (const [ref, lease] of threadLeases) {
      if (discovered.has(ref)) continue;
      threadLeases.delete(ref);
      track(
        lease.release().then(
          () => undefined,
          () => undefined,
        ),
        releaseWork,
      );
    }
  }

  function adoptPageLease(lease: BackgroundLivePages): void {
    const previous = pageLease;
    pageLease = lease;
    // Drop the retired lease only after the new one has registered every page
    // it still needs, so a page both sweeps use keeps its demand.
    previous?.release();
  }

  async function runSweep(): Promise<void> {
    const initial = navigation.getState();
    if (initial.mode !== "v3") return;
    const generation = initial.clientGenerationID;
    // The invalidation identity this sweep is authorized to act on. A newer
    // same-generation invalidation during the sweep revokes its authority to
    // remove refs or adopt its page set.
    const capturedSequence = initial.lastSequence;
    const lease = navigation.acquireBackgroundLivePages();
    const discovered = new Set<string>();
    const loadedPages: Array<{ key: ResourceKey; loadedRevision: number | null }> = [];
    let revision: number | null = null;
    let complete = false;

    try {
      let offset = 0;
      for (let pageIndex = 0; pageIndex < MAX_SWEEP_PAGES; pageIndex += 1) {
        let page: ResourceState<NavigationSectionResource>;
        try {
          page = await lease.load(offset);
        } catch {
          break;
        }
        if (disposed || !accepting) return;
        if (page.error !== null || page.data === null || page.loading || page.stale) break;
        if (page.generationID !== generation || page.data.generation_id !== generation) break;
        if (revision === null) revision = page.loadedRevision;
        else if (page.loadedRevision !== revision) break;
        if (navigation.getState().clientGenerationID !== generation) break;

        loadedPages.push({ key: page.key, loadedRevision: page.loadedRevision });
        for (const ref of eligibleLiveRefs(page.data.sessions)) discovered.add(ref);

        const rows = page.data.sessions.length;
        if (page.data.remaining === 0) {
          // A truncated page cannot prove the roster is complete, even when it
          // claims no remaining rows: the codec accepts that combination.
          if (page.data.truncated) break;
          complete = true;
          break;
        }
        // A page that returns no new top-level rows cannot advance the offset;
        // the roster cannot be proven complete.
        if (rows === 0) break;
        offset = nextNavigationOffset(offset, rows);
      }
    } finally {
      // A sweep interrupted by disposal must not leak its lease.
      if (disposed) lease.release();
    }

    if (disposed || !accepting) return;

    // Fresh-page additions may be acquired even from an incomplete sweep.
    acquireDiscovered(discovered);

    // Fence removal and adoption against the sweep's captured invalidation
    // identity AND the current freshness of every page it read. A superseded or
    // mixed-revision sweep must retain existing owners while its replacement
    // settles.
    const now = navigation.getState();
    const sameAuthority =
      now.mode === "v3" &&
      now.clientGenerationID === generation &&
      now.lastSequence === capturedSequence &&
      loadedPages.length > 0 &&
      loadedPages.every(({ key, loadedRevision }) => {
        const resource = now.resources.get(keyID(key));
        return (
          resource !== undefined &&
          !resource.stale &&
          !resource.loading &&
          resource.error === null &&
          resource.generationID === generation &&
          resource.loadedRevision === loadedRevision
        );
      });

    if (complete && sameAuthority) {
      clearRetry();
      releaseAbsent(discovered);
      adoptPageLease(lease);
    } else {
      // Incomplete or superseded: retain previous owners and their page demand.
      if (pageLease === null) pageLease = lease;
      else lease.release();
      // Pace a bounded retry for genuinely incomplete discovery. A superseded
      // complete sweep already has a fresh sweep scheduled by its invalidation.
      if (!complete) armRetry();
    }
  }

  function dispose(): Promise<void> {
    if (disposePromise) return disposePromise;
    accepting = false;
    disposed = true;
    unsubscribe();
    clearRetry();
    pageLease?.release();
    pageLease = null;
    const work: Promise<void>[] = [];
    for (const lease of threadLeases.values()) {
      work.push(
        lease.release().then(
          () => undefined,
          () => undefined,
        ),
      );
    }
    threadLeases.clear();
    disposePromise = Promise.all(work).then(() => undefined);
    return disposePromise;
  }

  async function settled(): Promise<void> {
    for (;;) {
      if (scheduledSoon) {
        await Promise.resolve();
        continue;
      }
      if (sweepPromise) await sweepPromise;
      const work = [...acquisitionWork, ...releaseWork];
      if (work.length > 0) await Promise.all(work);
      if (!scheduledSoon && !sweeping && !sweepPromise && acquisitionWork.size === 0 && releaseWork.size === 0) return;
    }
  }

  // Begin the first sweep without waiting for a navigation change: the app
  // may already hold a settled Live roster by the time the owner starts.
  lastInput = inputKey();
  scheduleSoon();

  return { dispose, settled };
}
