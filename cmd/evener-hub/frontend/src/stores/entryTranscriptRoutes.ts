// Entry-transcript routes: the per-ref route that owns a thread's repository,
// reader and local-opening state, and the opening, recovery and retirement
// paths that drive it. This module never imports threads.ts; threads.ts
// installs the host below once its store exists.

import type { AppwireClientLike, ThreadItemPosition, ThreadModel } from "@evener/appwire-client";
import { encodeTranscriptItem, pruneBackgroundHistoryBodies, threadModelFromCache } from "@evener/appwire-client";
import { type CacheLifetime, observedCacheEpoch } from "./cacheLifetime";
import {
  entryHistoryKey,
  SESSION_CACHE_DATABASE_NAME,
  SESSION_CACHE_LOOKUP_DEADLINE_MS,
  type TranscriptResolvedIdentity,
} from "./sessionCacheSchema";
import { SessionEntryCache } from "./sessionEntryCache";
import {
  backgroundHolds,
  canonicalThreadOwnerActive,
  clearFinalReleaseOwner,
  foregroundHolds,
  heldCountsAtStore,
  isFinalReleaseOwner,
  nextBackgroundLeaseSequence,
  setFinalReleaseOwner,
} from "./threadOwners";
import type { TranscriptReader, TranscriptRepository, TranscriptRetirementOutcome } from "./transcriptRepository";
import { getReadyEpoch } from "./wiredClient";

/** The slice of the thread store this module reads and writes. */
export interface EntryRouteStoreState {
  threads: Map<string, ThreadModel>;
  localOpenings: Map<string, number>;
  deletedRefs: Set<string>;
  clearInFlight: unknown;
  cacheLifetimes: Map<string, CacheLifetime>;
}

export interface EntryRouteStore {
  getState(): EntryRouteStoreState;
  setState(
    update: (state: EntryRouteStoreState) => Partial<Pick<EntryRouteStoreState, "localOpenings" | "cacheLifetimes">>,
  ): void;
}

/** What the routes need from the thread store that stays in threads.ts. */
export interface EntryRouteHost {
  store: EntryRouteStore;
  trackedHydrationCompletions: Map<string, Promise<void>>;
  putThreadModel(ref: string, model: ThreadModel): void;
  dropUnpinnedModel(ref: string, flush?: boolean): void;
  syncThreadSubscription(ref: string): unknown;
  requireReadyClient(): Promise<AppwireClientLike>;
  /** A tracked foreground refresh of `ref` on the given connection generation. */
  refreshForeground(
    client: AppwireClientLike,
    epoch: number,
    ref: string,
    options: { reportFailure: boolean; registerCompletion?: boolean },
  ): Promise<unknown>;
  onCacheEpochObserved(observed: number): void;
  /** The ensure generation a cached opening must still match. */
  ensureGeneration(ref: string): number | undefined;
  createRepository(
    fixture: EntryTranscriptRoute,
    binding: {
      threadId: string;
      historyKey: string;
      identity: TranscriptResolvedIdentity;
      clearEpoch: number;
      historyGeneration: number;
      seedModel?: ThreadModel;
    },
  ): TranscriptRepository;
}

let installedHost: EntryRouteHost | undefined;

export function installEntryRouteHost(host: EntryRouteHost): void {
  installedHost = host;
}

function routeHost(): EntryRouteHost {
  if (!installedHost) throw new Error("entry route host is not installed");
  return installedHost;
}

function store(): EntryRouteStore {
  return routeHost().store;
}

const olderPageGenerations = new Map<string, number>();

export function olderPageGeneration(ref: string): number {
  return olderPageGenerations.get(ref) ?? 0;
}

/** Supersedes every older-page request captured at generation `from`. */
export function advanceOlderPageGeneration(ref: string, from: number): void {
  olderPageGenerations.set(ref, from + 1);
}

export function entryRouteRetirementCount(ref: string): number {
  return entryRouteRetirements.get(ref) ?? 0;
}

export function openingRecoveryCompletion(ref: string): Promise<void> | undefined {
  return openingRecoveryCompletions.get(ref);
}

/** The route for `ref` if one exists; unlike `entryTranscriptRoute`, never creates it. */
export function existingEntryRoute(ref: string): EntryTranscriptRoute | undefined {
  return productionEntryRoutes.get(ref);
}

/** Drops `route` from the table when it is still the one registered for its ref. */
export function forgetEntryRoute(route: EntryTranscriptRoute): void {
  if (productionEntryRoutes.get(route.ref) === route) productionEntryRoutes.delete(route.ref);
}

/** Retires every route, closes the entry cache and forgets all route state. */
export function resetEntryTranscriptRoutesForTests(): void {
  for (const route of productionEntryRoutes.values()) retireEntryTranscriptRoute(route);
  productionEntryRoutes.clear();
  productionEntryCache?.close();
  productionEntryCache = undefined;
  entryRouteRetirements.clear();
  olderPageGenerations.clear();
  openingRecoveryCompletions.clear();
}

// A per-ref counter of retirement events (an entry-route delete or same-tab
// Clear) that invalidate a hydration's claim to adopt newly observed durable
// cache authorities. `acceptEntryTranscriptHydration` awaits durable reads
// after its response published; a hydration that began before a retirement
// must not be relabelled fresh by authorities read after it, so it captures
// this counter at `beginThreadHydration` and refuses when it moved.
const entryRouteRetirements = new Map<string, number>();
export function noteEntryRouteRetirement(ref: string): void {
  entryRouteRetirements.set(ref, (entryRouteRetirements.get(ref) ?? 0) + 1);
}

// The entry cache is the tab's only transcript cache (6d removed the legacy
// adapter). Lazy so the ordinary browser captures its actual storage
// environment on first use.
let productionEntryCache: SessionEntryCache | undefined;
export function currentEntryCache(): SessionEntryCache {
  productionEntryCache ??= new SessionEntryCache({ databaseName: SESSION_CACHE_DATABASE_NAME });
  return productionEntryCache;
}

/** @internal Replace only the ordinary route's storage/clock boundary before acquisition. */
export function setProductionEntryCacheForTests(cache: SessionEntryCache): void {
  productionEntryCache = cache;
}

/** @internal Observe an existing ordinary route, without creating or selecting it. */
export function productionEntryTranscriptForTests(ref: string) {
  const route = productionEntryRoutes.get(ref);
  return {
    ref,
    historyKey: route?.historyKey,
    model: route?.repository?.getSnapshot(),
    baseline: route?.repository?.getBaselineIdentity(),
    olderPageGeneration: olderPageGeneration(ref),
    readerRetention: route?.reader?.getRetentionSnapshotForTesting(),
    repository: route?.repository,
    reader: route?.reader,
    lastLocalOpenStatus: route?.lastLocalOpenStatus,
    lastLocalOpenUsable: route?.lastLocalOpenUsable,
  };
}

export interface EntryTranscriptRoute {
  ref: string;
  cache: SessionEntryCache;
  clientId: string;
  hostId: string;
  repository?: TranscriptRepository;
  reader?: TranscriptReader;
  unsubscribeRepository?: () => void;
  retirement?: Promise<TranscriptRetirementOutcome>;
  retirementEpoch?: number;
  lifetime: number;
  historyKey?: string;
  // True while an in-flight same-tab Clear has suppressed this route's
  // publication and persistence claims. An aborted Clear clears it, re-enabling
  // the same held route without a release/reacquire.
  clearInert?: boolean;
  // One scalar per owned route, never a replay buffer of suppressed frames.
  clearRefreshGeneration?: number;
  // The durable authorities the installed repository captured. A later read
  // that observes a newer Clear epoch or deletion generation retires it, so a
  // fresh explicit foreground acquisition begins caching under the current
  // authorities instead of carrying stale ones.
  clearEpoch?: number;
  historyGeneration?: number;
  identity?: TranscriptResolvedIdentity;
  cachedUnverified?: boolean;
  cachedOpening?: Promise<void>;
  // Local reader continuation, separate from the repository's opaque server cursor.
  localBefore?: ThreadItemPosition;
  // The status the last local open (`openEntryTranscript`) reported for this
  // route. Read-only test evidence for why a pane open did or did not need a
  // source read; never consulted by product logic.
  lastLocalOpenStatus?: string;
  // True while the view the last accepted local open (`openEntryTranscript`)
  // established is still the one to serve: cleared when that open is torn
  // down. A remount of the same route may serve this view without racing a
  // disk recertification against the lookup deadline.
  lastLocalOpenUsable?: boolean;
  // True when the most recent open's bounded lookup expired before the local
  // read answered (openEntryTranscript's deadline branch): the recorded window
  // was never served nor refused, so a held pane's display can still be
  // body-suppressed with no demand cursor. Distinguishes that strand from the
  // fast recovery-needed answers a repository without a usable record returns.
  localOpenDeadlineMissed?: boolean;
  // True while some raced local opening missed its bounded lookup deadline and
  // no later open has established a usable view since: the pane's recorded
  // display is still owed its convergence. Unlike localOpenDeadlineMissed —
  // per-open, cleared when the next open begins, and read by the recovery
  // chain to pace its own attempts — this fact survives the acquisition's
  // follow-up opens. The deadline-missed open's per-open marker is otherwise
  // erased by the post-hydration promotion open that refuses while the
  // hydration's durable phase is still settling (its read answers after the
  // window's authority moved, so it discards a complete window and returns a
  // body-suppressed model), and the stranding recovery would never arm.
  unresolvedOpenMiss?: boolean;
}

const productionEntryRoutes = new Map<string, EntryTranscriptRoute>();

export function entryTranscriptRoute(ref: string): EntryTranscriptRoute | undefined {
  let route = productionEntryRoutes.get(ref);
  if (!route) {
    route = {
      ref,
      cache: currentEntryCache(),
      clientId: location.origin,
      hostId: "local",
      lifetime: 1,
    };
    productionEntryRoutes.set(ref, route);
  }
  return route;
}

export function entryTranscriptRoutes(): EntryTranscriptRoute[] {
  return [...productionEntryRoutes.values()];
}

export function isEntryTranscriptBackgroundOnly(ref: string): boolean {
  return backgroundHolds(ref) > 0 && !heldCountsAtStore(ref);
}

// Opening tokens, monotonic across the tab. The pane paints the
// background-evicted recorded snapshot before its reader restores bodies from
// disk, and react-virtual measures those empty rows (at zero) as it paints
// them, so geometry cannot tell that first paint from a settled transcript
// that legitimately holds an empty turn. The store owns that opening fact:
// callers hold paging while a ref's opening is pending, then let ordinary
// geometry decide. Tokens make a newer opening supersede an older one, so an
// old opening settling cannot clear a newer hold.
let entryTranscriptOpeningSequence = 0;

// The unraced restore of an established view is bounded only by a genuine
// storage stall, not by load: CI already showed load pushing the 250ms lookup
// deadline around, which is exactly the race this restore must not lose.
let sessionCacheStalledOpenMs = 5000;

/** @internal Lets tests shorten the stall bound without waiting real seconds. */
export function setSessionCacheStalledOpenForTests(ms: number): void {
  sessionCacheStalledOpenMs = ms;
}

/** True while the entry fixture's reader is still restoring `ref` from disk. */
export function isEntryTranscriptLocalOpening(ref: string): boolean {
  return store().getState().localOpenings.has(ref);
}

// The newest opening that ever began for a ref, kept after it settles: a
// late restore result must not act once any newer opening exists, even after
// that newer opening finished.
const entryTranscriptLatestOpening = new Map<string, number>();

// Mark the opening pending BEFORE its first (pruned) publication, and publish
// the change through the store so subscribed consumers re-render. Returns the
// token the matching settle must present.
export function beginEntryTranscriptLocalOpening(ref: string): number {
  entryTranscriptOpeningSequence += 1;
  const token = entryTranscriptOpeningSequence;
  entryTranscriptLatestOpening.set(ref, token);
  store().setState((state) => {
    const localOpenings = new Map(state.localOpenings);
    localOpenings.set(ref, token);
    return { localOpenings };
  });
  return token;
}

// Settle only the opening this token owns: a stale opening (a newer one took
// the ref) is a no-op, so it can never clear the newer hold or, by
// publishing, strand a port. Called on success, failure and no-model alike.
export function settleEntryTranscriptLocalOpening(ref: string, token: number): void {
  store().setState((state) => {
    if (state.localOpenings.get(ref) !== token) return {};
    const localOpenings = new Map(state.localOpenings);
    localOpenings.delete(ref);
    return { localOpenings };
  });
}

// Drop any hold for a ref whose fixture route is gone (disposal): publish the
// change so a consumer showing the port resumes ordinarily rather than waiting
// for a frame that will never arrive.
export function clearEntryTranscriptLocalOpening(ref: string): void {
  store().setState((state) => {
    if (!state.localOpenings.has(ref)) return {};
    const localOpenings = new Map(state.localOpenings);
    localOpenings.delete(ref);
    return { localOpenings };
  });
}

/** Scheduling identity for older demand, not an opaque wire cursor. */
export function olderTranscriptPageKey(ref: string): string | null | undefined {
  const model = store().getState().threads.get(ref);
  if (!model) return undefined;
  const route = entryTranscriptRoute(ref);
  const local = route?.repository?.localOlderPosition();
  // This is a scheduling identity only. The wire path reads model.olderCursor,
  // never this key. Tag both kinds so an opaque server cursor cannot collide.
  if (local)
    return JSON.stringify(["local", route?.repository?.getBaselineIdentity(), local.entry, local.item, local.sub ?? 0]);
  return model.olderCursor ? JSON.stringify(["server", model.olderCursor]) : null;
}

// Publish the repository's own model into the real store. Called while the
// fixture's reader is attached: the repository is the route's single owner of
// recorded ingestion, so its accepted updates (not a parallel legacy reduce)
// are what the renderer follows. The store model stays pruned only while the
// ref is genuinely background-only; an attached reader means a real holder.
export function publishEntryTranscriptModel(ref: string): void {
  const fixture = entryTranscriptRoute(ref);
  if (!fixture || fixture.ref !== ref || fixture.clearInert) return;
  const snapshot = fixture?.repository?.getSnapshot();
  if (!snapshot) return;
  const displayed = fixture.localBefore
    ? { ...snapshot, olderCursor: snapshot.olderCursor ?? `entry-local:${JSON.stringify(fixture.localBefore)}` }
    : snapshot;
  routeHost().putThreadModel(
    ref,
    isEntryTranscriptBackgroundOnly(ref) ? pruneBackgroundHistoryBodies(displayed) : displayed,
  );
}

// The complete-requested-window standard a local open tolerates without a
// cache certificate: every foreground item the model expects is present with
// at least its version and a real body.
function holdsCompleteForegroundWindow(model: ThreadModel | undefined): boolean {
  const displayed = new Map(
    model?.turns.flatMap((turn) => turn.items.map((item) => [item.transcriptKey ?? item.id, item] as const)),
  );
  // Legacy reads keep their recorded bodies in turns, without versioned
  // history. Do not mistake that nonempty accepted window for an empty one.
  const requested = (model?.history?.turns ?? model?.turns)?.flatMap((turn) => turn.items).slice(-200) ?? [];
  return (
    requested.length > 0 &&
    requested.every((item) => {
      const restored = displayed.get(item.transcriptKey ?? item.id);
      return (
        restored !== undefined &&
        (restored.version ?? 0) >= (item.version ?? 0) &&
        !encodeTranscriptItem(restored).identityOnly
      );
    })
  );
}

export async function openEntryTranscript(ref: string, recovery = false): Promise<boolean> {
  const fixture = entryTranscriptRoute(ref);
  if (!fixture || fixture.ref !== ref || !fixture.repository) return true;
  const repository = fixture.repository;
  const openingToken = beginEntryTranscriptLocalOpening(ref);
  fixture.localOpenDeadlineMissed = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    fixture.reader ??= fixture.repository.attachReader();
    // Ongoing repository publication: every accepted recorded update republishes
    // the repository model, so continued UI changes are repository-owned rather
    // than supplied by the parallel legacy reducer. The explicit openLocal result
    // below is one such publication; this is the subscription that outlives it.
    fixture.unsubscribeRepository ??= fixture.repository.subscribe(() => publishEntryTranscriptModel(ref));
    publishEntryTranscriptModel(ref);
    const reader = fixture.reader;
    // A remount over a route whose last accepted open still holds an
    // un-invalidated view trusts the local restore instead of racing it
    // against the lookup deadline: the repository kept that view's identities
    // and the restore republishes its bodies from the durable rows. Under
    // load the race can only lose — the expiry disposes this reader, the
    // background reclaim then drops the resident bodies, and the source
    // recovery replaces the loaded range with a latest window (#4939). The
    // unraced restore keeps the ordinary opening hold and result handling,
    // and only a genuinely stalled storage read settles the opening early.
    // Only the opening that still owns the route may record its verdict: a
    // superseded one (its pane released, its repository replaced, or a newer
    // open already begun) must not overwrite what a newer open established.
    const openingOwned = () =>
      entryTranscriptRoute(ref) === fixture &&
      fixture.repository === repository &&
      store().getState().localOpenings.get(ref) === openingToken;
    const established = fixture.lastLocalOpenUsable && repository.holdsEstablishedView();
    let stalled = false;
    const restore = established ? reader.openLocal(200) : undefined;
    const result = established
      ? await Promise.race([
          restore,
          new Promise<undefined>((resolve) => {
            timer = setTimeout(() => {
              stalled = true;
              resolve(undefined);
            }, sessionCacheStalledOpenMs);
          }),
        ])
      : await Promise.race([
          reader.openLocal(200),
          new Promise<undefined>((resolve) => {
            timer = setTimeout(
              () => {
                // A held pane keeps a reader through the swap: the repository
                // reclaims the bodies behind a displayed window the moment it
                // has none, and the next publication would then replace that
                // display with the body-suppressed model.
                if (fixture.reader === reader) {
                  fixture.reader = heldCountsAtStore(ref) ? repository.attachReader() : undefined;
                }
                reader.dispose();
                resolve(undefined);
                // A recovery attempt outlasts the lookup deadline on purpose:
                // a read that answers slower than the deadline but successfully
                // must be allowed to finish, or a consistently slow cache
                // strands the pane behind a race it can only keep losing.
              },
              recovery ? sessionCacheStalledOpenMs : SESSION_CACHE_LOOKUP_DEADLINE_MS,
            );
          }),
        ]);
    if (!result) {
      // A stalled restore keeps the established view standing only while that
      // view still holds its required bodies: a foreground release already
      // disposed the reader and reclaimed them, so an empty display must not
      // wait on storage that may never answer. The repository's own snapshot
      // is the truth here, not the possibly-pruned display.
      if (stalled && restore && repository.holdsResidentForegroundRange()) {
        // A stalled restore leaves the established view standing: the opening
        // settles so paging and placement resume, and the still-running
        // restore publishes through the repository subscription whenever
        // storage answers. Its late result still records the truth, but only
        // while this opening remains its owner: the route's current reader,
        // the newest opening generation and a live foreground pane all fence
        // a result that outlived a newer open, a release or a replacement.
        const stillOwnsLateResult = () =>
          entryTranscriptRoute(ref) === fixture &&
          fixture.repository === repository &&
          fixture.reader === reader &&
          entryTranscriptLatestOpening.get(ref) === openingToken &&
          foregroundHolds(ref) > 0 &&
          !store().getState().localOpenings.has(ref);
        void restore
          .then(async (settled) => {
            if (!stillOwnsLateResult()) return;
            fixture.lastLocalOpenStatus = settled.status;
            const usable =
              settled.retainedRangeComplete !== false &&
              (settled.status !== "recovery-needed" || holdsCompleteForegroundWindow(settled.model));
            if (usable) {
              fixture.lastLocalOpenUsable = true;
              fixture.unresolvedOpenMiss = false;
              return;
            }
            fixture.lastLocalOpenUsable = false;
            repository.beginFreshBaseline();
            publishEntryTranscriptModel(ref);
            const client = await routeHost().requireReadyClient();
            const epoch = getReadyEpoch();
            if (!stillOwnsLateResult()) return;
            await routeHost().refreshForeground(client, epoch, ref, { reportFailure: false });
            // Mirror the raced recovery's full sequence: the read that
            // replaced the view is followed by the local re-open that
            // promotes its restored bodies into the display.
            if (!stillOwnsLateResult()) return;
            await openEntryTranscript(ref);
          })
          .catch(() => {
            // A failed recovery read keeps the ordinary owner-scoped retry.
          });
        return true;
      }
      fixture.lastLocalOpenStatus = "recovery-needed";
      // Only the bounded-lookup deadline miss is the stranding strand a
      // tracked re-open can heal; an established restore that stalled past
      // its own settled-opening window hands to source recovery instead
      // (the #4954 stalled-open contract), and must not arm this chain.
      if (!stalled) {
        fixture.localOpenDeadlineMissed = true;
        fixture.unresolvedOpenMiss = true;
      }
      if (openingOwned()) fixture.lastLocalOpenUsable = false;
      return false;
    }
    fixture.lastLocalOpenStatus = result.status;
    if (
      result.model &&
      entryTranscriptRoute(ref) === fixture &&
      fixture.repository === repository &&
      !fixture.clearInert &&
      foregroundHolds(ref) > 0 &&
      !store().getState().deletedRefs.has(ref)
    ) {
      fixture.localBefore = result.before;
      publishEntryTranscriptModel(ref);
    }
    // A previously loaded range can exceed the initial 200-body promotion: keep
    // its usable partial display, but do not certify it via a smaller tail.
    // Cache proof can also be unavailable while the live model already carries
    // the complete requested window. Recover missing bodies, not merely a
    // missing certificate, which would replace usable paged history with a
    // latest read.
    const usable =
      result.retainedRangeComplete !== false &&
      (result.status !== "recovery-needed" || holdsCompleteForegroundWindow(result.model));
    if (openingOwned()) {
      fixture.lastLocalOpenUsable = usable;
      if (usable) fixture.unresolvedOpenMiss = false;
    }
    return usable;
  } finally {
    if (timer !== undefined) clearTimeout(timer);
    settleEntryTranscriptLocalOpening(ref, openingToken);
  }
}

export async function openCachedEntryTranscript(ref: string, generation: number): Promise<void> {
  const route = entryTranscriptRoute(ref);
  if (!route || productionEntryRoutes.get(ref) !== route || route.repository) return;
  if (route.cachedOpening) return route.cachedOpening;
  const retirement = entryRouteRetirements.get(ref) ?? 0;
  let active = true;
  let installed: TranscriptRepository | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const current = () => {
    const state = store().getState();
    return (
      active &&
      productionEntryRoutes.get(ref) === route &&
      foregroundHolds(ref) > 0 &&
      routeHost().ensureGeneration(ref) === generation &&
      (entryRouteRetirements.get(ref) ?? 0) === retirement &&
      !state.deletedRefs.has(ref) &&
      !route.clearInert &&
      state.clearInFlight === undefined &&
      !state.cacheLifetimes.get(ref)?.suppressed
    );
  };
  const opening = Promise.race([
    (async () => {
      const metadata = await route.cache.getSessionForRequestedRef(route.clientId, ref);
      if (!current() || !metadata?.resolvedIdentity || route.repository || store().getState().threads.has(ref)) return;
      const authorities = await readEntryRouteAuthorities(route, metadata.historyKey);
      if (
        !current() ||
        !authorities ||
        authorities.historyGeneration !== (metadata.generation ?? 0) ||
        route.repository ||
        store().getState().threads.has(ref)
      )
        return;
      const identity = metadata.resolvedIdentity;
      // Earlier v3 entries have identity but no display header. Their missing
      // wire instance stays explicitly unknown, not guessed from an opaque ref.
      // The first source success replaces this untrusted binding in full.
      const header = metadata.cachedHeader ?? {
        ref,
        threadId: "",
        name: "",
        modelProvider: "",
        model: "",
        imageSessionId: identity.sessionId,
        savedAt: metadata.savedAt ?? 0,
        history: {
          bootGeneration: "",
          epoch: 0,
          incarnation: identity.incarnation,
          length: 0,
          appliedGeneration: 0,
          issuedGeneration: 0,
        },
      };
      if (entryHistoryKey(identity) !== metadata.historyKey || header.history.incarnation !== identity.incarnation)
        return;
      // A first durable observation belongs to this unpublished acquisition.
      if (observedCacheEpoch() === undefined) {
        store().setState((state) => ({
          cacheLifetimes: new Map(state.cacheLifetimes).set(ref, {
            ...state.cacheLifetimes.get(ref),
            leaseEpoch: authorities.clearEpoch,
          }),
        }));
      }
      routeHost().onCacheEpochObserved(authorities.clearEpoch);
      if (!current()) return;
      route.identity = identity;
      route.hostId = identity.hostId;
      route.historyKey = metadata.historyKey;
      route.clearEpoch = authorities.clearEpoch;
      route.historyGeneration = authorities.historyGeneration;
      route.cachedUnverified = true;
      route.lifetime += 1;
      installed = routeHost().createRepository(route, {
        threadId: header.threadId,
        historyKey: metadata.historyKey,
        identity,
        ...authorities,
        seedModel: threadModelFromCache(
          { ...header, ref, history: { ...header.history, turns: [] } },
          route.cache.now(),
        ),
      });
      route.repository = installed;
      route.reader = installed.attachReader();
      route.unsubscribeRepository = installed.subscribe(() => {
        if (current() && route.repository === installed) publishEntryTranscriptModel(ref);
      });
      const token = beginEntryTranscriptLocalOpening(ref);
      try {
        const cachedResult = await route.reader.openLocal(200);
        route.lastLocalOpenStatus = cachedResult.status;
      } finally {
        settleEntryTranscriptLocalOpening(ref, token);
      }
    })().catch(() => {
      // A storage failure is a miss. The ordinary source opening still runs.
    }),
    new Promise<void>((resolve) => {
      timer = setTimeout(() => {
        active = false;
        if (installed && route.repository === installed && route.cachedUnverified) {
          // The deadline expired while a cached repository was being opened:
          // the pane's recorded window was never served nor refused, so its
          // convergence is owed exactly as a raced open's deadline miss owes
          // it — the source hydration continues the acquisition, and its own
          // promotion open may refuse while the hydration's durable phase is
          // still settling (discarding a complete window behind a moved
          // authority), which must not leave the chain disarmed.
          route.unresolvedOpenMiss = true;
          retireEntryTranscriptRoute(route);
          clearEntryTranscriptLocalOpening(ref);
        }
        resolve();
      }, SESSION_CACHE_LOOKUP_DEADLINE_MS);
    }),
  ]).finally(() => {
    if (timer !== undefined) clearTimeout(timer);
    if (route.cachedOpening === opening) route.cachedOpening = undefined;
  });
  route.cachedOpening = opening;
  await opening;
}

// A foreground acquisition whose bounded local opening missed its lookup
// deadline returns with the repository's recorded display standing in for the
// pane: body-suppressed and without a local demand cursor, because only a
// successful open promotes recorded bodies into the display — a fresh server
// read republishes the recorded projection and cannot restore them. One miss
// self-heals inside ensureThread (the authoritative read publishes, and the
// success arm's second open promotes the recorded window). Two misses in a
// row — the sustained-slow-cache case the 250ms lookup deadline makes
// possible — used to leave the pane that way permanently: nothing re-opened
// the record, so the thread store's documented bounded source recovery (S02:
// "the accepted baseline restores display, older paging and live updates")
// never ran. A tracked re-open closes the gap, and it must RE-ARM: its own
// open reads the same slow cache and can miss the same deadline again, so a
// fixed number of attempts just strands the pane one attempt later (the
// whole-suite third-miss failure this re-arm closes). Each attempt is bounded
// by the stalled-open window rather than the lookup deadline (a read that
// answers slowly but successfully must still finish), and only an attempt's
// completion decides the next one — an event-completion chain, not a timer
// loop — so it stops at the
// first successful open, when the foreground holder releases, or when the
// route retires, or once the storage chain has had its paced retry: a fast
// recovery-needed refusal, or a second missed stalled window, hands to
// source recovery once instead of re-arming against storage that may never
// answer. One tracked chain covers every attempt, deduplicated per live
// route, and is registered in trackedHydrationCompletions so a caller
// awaiting the acquisition's completion (including
// settleEntryTranscriptForTests) also awaits the display's convergence.
// The arming fact is the route's unresolvedOpenMiss, not the per-open
// localOpenDeadlineMissed: a miss can be left unresolved by a LATER open of
// the same acquisition. The deadline-missed open (a remount over a pruned
// background model, or the cached opening) resets the per-open marker when
// the post-hydration promotion open begins, and that open can refuse fast —
// its read answers only after the window's authority moved, so it discards a
// complete certified window — leaving the marker false and the pane
// body-suppressed with nothing scheduled (the whole-suite CI strand where
// bodies stayed [] after an awaited ensureThread). The owed fact survives
// that refusal and clears only when an open establishes a usable view.
// The route a pending recovery belongs to, keyed by ref: a recovery opened for
// a retired route must never suppress a fresh acquisition's recovery, and a
// released-then-reacquired pane (same ref, new route) still gets its own.
const pendingOpeningRecoveries = new Map<string, EntryTranscriptRoute>();

// The recovery chain's whole-span registration per ref, so a concurrent
// hydration can compose with it instead of displacing it. Tracked separately
// from trackedHydrationCompletions because a plain hydration's registration
// may be superseded by a newer attempt, while the chain's span may not.
const openingRecoveryCompletions = new Map<string, Promise<void>>();

export function scheduleEntryOpeningRecovery(ref: string): void {
  const route = entryTranscriptRoute(ref);
  if (!route || route.ref !== ref) return;
  if (pendingOpeningRecoveries.get(ref) === route) return;
  if (foregroundHolds(ref) <= 0) return; // no live foreground holder to recover for
  // No raced opening's deadline miss is still owed convergence: the last open
  // either answered storage (it refused or served on the record's merits) or
  // a usable view was established, and another path owns the rest.
  if (route.unresolvedOpenMiss !== true) return;
  // The chain exists to promote recorded bodies into a display that is still
  // body-suppressed; a display another path (main's source recovery, a newer
  // open) already healed needs no re-open churn against storage that may
  // never answer.
  if (holdsCompleteForegroundWindow(store().getState().threads.get(ref))) return;
  pendingOpeningRecoveries.set(ref, route);
  const recovery = (async () => {
    let stalledAttempts = 0;
    for (;;) {
      const opened = await openEntryTranscript(ref, true);
      if (opened) return;
      if (entryTranscriptRoute(ref) !== route) return; // a retired route's recovery must not outlive it
      if (foregroundHolds(ref) <= 0) return; // holder released: nothing left to recover for
      // Another path healed the display mid-chain only while the repository
      // still backs that display: a pending attempt whose timeout disposed the
      // shared reader lets the repository reclaim its bodies without
      // publishing, so the store's snapshot alone can be a stale complete view
      // the next publication would prune. Require the resident range too, and
      // a reclaimed repository re-opens instead of ending recovery on it.
      if (
        holdsCompleteForegroundWindow(store().getState().threads.get(ref)) &&
        route.repository?.holdsResidentForegroundRange()
      )
        return;
      if (route.localOpenDeadlineMissed === true && stalledAttempts < 1) {
        // The attempt missed the deadline again under sustained slowness; the
        // pane is still stranded and held, so give the storage chain one
        // more paced attempt before turning to the source.
        stalledAttempts += 1;
        continue;
      }
      // The attempt refused fast instead of missing the deadline — storage
      // answered a recovery-needed outright, and a transient repository error
      // takes openLocal's catch, which resolves without arming another
      // attempt — or it missed its second stalled window and the cache is not
      // answering at all. A pane that is still suppressed must not strand
      // behind a marker no later open will set, or behind storage that may
      // never answer: hand to source recovery once, the same sequence a
      // stalled restore hands to (#4954), and let the ordinary owner-scoped
      // paths own the rest.
      route.repository?.beginFreshBaseline();
      publishEntryTranscriptModel(ref);
      let sourceReadFailed = false;
      try {
        const client = await routeHost().requireReadyClient();
        const epoch = getReadyEpoch();
        if (entryTranscriptRoute(ref) !== route) return;
        if (foregroundHolds(ref) <= 0) return;
        // The chain's own registration spans this read and the re-open after
        // it; the tracked read joins under it rather than displacing it, so
        // a consumer that starts waiting mid-read still captures a promise
        // covering the entire recovery.
        await routeHost().refreshForeground(client, epoch, ref, { reportFailure: true, registerCompletion: false });
      } catch {
        // A failed source read keeps the ordinary owner-scoped retry — the
        // owned hydration lifecycle schedules it — and the re-open below
        // promotes the view a successful read restored, so a failed one has
        // nothing to promote.
        sourceReadFailed = true;
      }
      if (sourceReadFailed) {
        return;
      }
      if (entryTranscriptRoute(ref) !== route) return;
      if (foregroundHolds(ref) <= 0) return;
      // Mirror the raced recovery's full sequence: the read that replaced
      // the view is followed by the local re-open that promotes its
      // restored bodies into the display. A source response that already
      // healed the display needs no further cache read to converge.
      if (!holdsCompleteForegroundWindow(store().getState().threads.get(ref))) {
        await openEntryTranscript(ref, true);
      }
      return;
    }
  })().then(
    () => undefined,
    () => undefined,
  );
  // The chain's registration is the recovery's whole span; it is also
  // recorded in openingRecoveryCompletions so a concurrent refresh can compose
  // with it instead of displacing it. A superseded hydration's registration
  // may still be displaced by a newer hydration — the newer attempt owns the
  // ref's convergence — but the recovery chain's span is not a hydration and
  // must survive every one.
  openingRecoveryCompletions.set(ref, recovery);
  routeHost().trackedHydrationCompletions.set(ref, recovery);
  void recovery.then(() => {
    if (pendingOpeningRecoveries.get(ref) === route) pendingOpeningRecoveries.delete(ref);
    if (openingRecoveryCompletions.get(ref) === recovery) openingRecoveryCompletions.delete(ref);
    if (routeHost().trackedHydrationCompletions.get(ref) === recovery)
      routeHost().trackedHydrationCompletions.delete(ref);
  });
}

export function releaseEntryTranscriptReader(ref: string, releaseDisplay = false): void {
  if (heldCountsAtStore(ref)) return;
  const fixture = entryTranscriptRoute(ref);
  if (!fixture || fixture.ref !== ref) return;
  clearEntryTranscriptLocalOpening(ref);
  fixture.reader?.dispose();
  fixture.reader = undefined;
  const model = store().getState().threads.get(ref);
  if (releaseDisplay && model && isEntryTranscriptBackgroundOnly(ref)) {
    routeHost().putThreadModel(ref, pruneBackgroundHistoryBodies(model));
  }
}

export function retireEntryTranscriptIfUnowned(ref: string): Promise<TranscriptRetirementOutcome> | undefined {
  const fixture = entryTranscriptRoute(ref);
  const repository = fixture?.ref === ref ? fixture.repository : undefined;
  if (!fixture || !repository || canonicalThreadOwnerActive(ref)) return undefined;
  if (fixture.retirement) return fixture.retirement;
  const generation = nextBackgroundLeaseSequence(ref);
  setFinalReleaseOwner(ref, generation);
  routeHost().syncThreadSubscription(ref);
  fixture.retirementEpoch = getReadyEpoch();
  const retirement: Promise<TranscriptRetirementOutcome> = repository.retire().then((outcome) => {
    if (fixture.retirement !== retirement || fixture.repository !== repository || canonicalThreadOwnerActive(ref))
      return { kind: "cancelled" };
    fixture.unsubscribeRepository?.();
    fixture.unsubscribeRepository = undefined;
    fixture.reader?.dispose();
    fixture.reader = undefined;
    repository.dispose();
    fixture.repository = undefined;
    fixture.retirement = undefined;
    fixture.retirementEpoch = undefined;
    if (isFinalReleaseOwner(ref, generation)) clearFinalReleaseOwner(ref);
    routeHost().dropUnpinnedModel(ref, false);
    routeHost().syncThreadSubscription(ref);
    return outcome;
  });
  fixture.retirement = retirement;
  return retirement;
}

// Dispose the entry route's reader, publication subscription and repository
// together. `openEntryTranscript` uses `??=`, so leaving a disposed
// reader or a subscription bound to the old repository installed would let a
// later open reuse them.
export function retireEntryTranscriptRoute(fixture: EntryTranscriptRoute): void {
  fixture.reader?.dispose();
  fixture.reader = undefined;
  fixture.localBefore = undefined;
  fixture.lastLocalOpenUsable = undefined;
  fixture.unsubscribeRepository?.();
  fixture.unsubscribeRepository = undefined;
  fixture.repository?.dispose();
  fixture.repository = undefined;
}

// Read the durable Clear epoch and deletion generation under the existing
// lookup deadline, with a storage-failure fallback: a held or failed durable
// read must not delay or reject an otherwise successful source hydration.
export async function readEntryRouteAuthorities(
  fixture: EntryTranscriptRoute,
  historyKey: string,
): Promise<{ clearEpoch: number; historyGeneration: number } | undefined> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      Promise.all([fixture.cache.getClearEpoch(), fixture.cache.getHistoryGeneration(historyKey)]).then(
        ([clearEpoch, historyGeneration]) => ({ clearEpoch, historyGeneration }),
      ),
      new Promise<undefined>((resolve) => {
        timer = setTimeout(() => resolve(undefined), SESSION_CACHE_LOOKUP_DEADLINE_MS);
      }),
    ]);
  } catch {
    return undefined;
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
}
