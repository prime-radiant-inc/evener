// Entry-transcript routes: the per-ref route that owns a thread's repository,
// reader and local-opening state, and the opening, recovery and retirement
// paths that drive it. This module never imports threads.ts; threads.ts
// installs the host below once its store exists.

import type { AppwireClientLike, ThreadItemPosition, ThreadModel } from "@evener/appwire-client";
import {
  comparePositions,
  encodeTranscriptItem,
  olderHistoryExhausted,
  pruneBackgroundHistoryBodies,
  threadModelFromCache,
} from "@evener/appwire-client";
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
import { withDeadline } from "./withDeadline";

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
  /** Parks until the next tracked hydration for `ref` settles either way,
   * excepting the one registration whose settlement the caller owns. */
  nextTrackedHydrationSettlement(ref: string, except?: unknown): { settled: Promise<void>; cancel: () => void };
  /** Registers a completion in trackedHydrationCompletions and settles the
   * waiters parked on it when it settles. */
  registerHydrationCompletion(ref: string, completion: Promise<void>): void;
  /** Wakes every parked opening waiter for `ref`: a quiet certification just
   * established the display over an older published opening, whose parked
   * wait can no longer count on the settlement it armed - the registration it
   * observed was displaced by a pending refresh that has not settled. */
  notifyOpeningEstablishedOverSuperseded(ref: string): void;
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
  /**
   * One bounded older-range read against the source — the same read a user's
   * scroll issues — so a pane whose restored older rows no current-baseline
   * statement validated can reconcile them against the source automatically.
   * "accepted" means the page was accepted and its range reconciled;
   * "discarded" means a competing read moved the baseline while the page was
   * in the air — a retryable rejection the caller may retry within its own
   * bounded budget once the competing read settles; "unissued" means no page
   * could serve the range (no sendable cursor, a failed request, or a page
   * that merged nothing against an unchanged baseline — genuine exhaustion).
   */
  reconcileRecordedRange(ref: string): Promise<"accepted" | "discarded" | "unissued">;
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
    localOpenDeadlineMissed: route?.localOpenDeadlineMissed,
    unresolvedOpenMiss: route?.unresolvedOpenMiss,
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
  // read answered (openEntryTranscript's backstop branch): the recorded window
  // was never served nor refused, so a held pane's display can still be
  // body-suppressed with no demand cursor. Distinguishes that strand from the
  // fast recovery-needed answers a repository without a usable record returns.
  localOpenDeadlineMissed?: boolean;

  // True while some raced local opening missed its bound and no later open
  // has established a usable view since: the pane's recorded display is
  // still owed its convergence. Unlike localOpenDeadlineMissed — per-open,
  // cleared when the next open begins, and read by the recovery chain to
  // pace its own attempts — this fact survives the acquisition's follow-up
  // opens: the promotion or certification open that follows a miss can
  // refuse fast (a transient storage error answers a fast recovery-needed
  // through openLocal's catch), and its own start has already reset the
  // per-open marker, so without this fact the pane would strand
  // body-suppressed with nothing scheduled — the CI strand where bodies
  // stayed [] after an awaited ensureThread.
  unresolvedOpenMiss?: boolean;
  /** @internal A replacement repository adopted the outgoing one's pending
   * recorded-range recovery; the next successful publication owes the chain's
   * one-shot successor arm (see the replacement seam in threads.ts). */
  pendingReplacementRecovery?: boolean;
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

// The ordinary open's storage-read backstop. The open's primary bound is
// deterministic — the next tracked hydration for the ref settling, or an
// ownership change — and this long clock exists only for the wedge where
// nothing else can fire: a remount whose storage read never answers and no
// hydration is in flight. It must stay far above any real IndexedDB latency
// so a slow-but-answering cache is never mistaken for a wedged one.
let sessionCacheOpenBackstopMs = 10000;

/** @internal Lets tests shorten the open backstop without waiting real seconds. */
export function setSessionCacheOpenBackstopForTests(ms: number): void {
  sessionCacheOpenBackstopMs = ms;
}

// The open-race sentinel for "a tracked hydration settled first": the source
// owns the display's convergence from that settlement, so the open ends
// superseded, not missed.
const OPEN_SUPERSEDED_BY_SOURCE = { supersededBySource: true } as const;

/** True while the entry fixture's reader is still restoring `ref` from disk. */
export function isEntryTranscriptLocalOpening(ref: string): boolean {
  return store().getState().localOpenings.has(ref);
}

// The newest opening that ever began for a ref, kept after it settles: a
// late restore result must not act once any newer opening exists, even after
// that newer opening finished.
const entryTranscriptLatestOpening = new Map<string, number>();

// A quiet opening's ownership token: present while the open runs, invisible
// to the consumer gate. The certification open a claim runs over a display
// its own publication already completed, and whose older frontier is still
// outstanding, has no pruned publication for the gate to protect, while
// consumers hold live paging demand the gate would strand exactly between
// the claim and the open's settle — so it does not hold them, while still recording its
// verdict like any open, including arming the recovery chain on a miss.
const entryTranscriptQuietOpenings = new Map<string, number>();

// Mark the opening pending BEFORE its first (pruned) publication, and publish
// the change through the store so subscribed consumers re-render. Returns the
// token the matching settle must present. A quiet opening registers its token
// without the store publication: consumers never wait on it, and ownership
// (the registries' tokens) behaves exactly as for a published one.
export function beginEntryTranscriptLocalOpening(ref: string, quiet = false): number {
  entryTranscriptOpeningSequence += 1;
  const token = entryTranscriptOpeningSequence;
  entryTranscriptLatestOpening.set(ref, token);
  if (quiet) {
    entryTranscriptQuietOpenings.set(ref, token);
    return token;
  }
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
  if (entryTranscriptQuietOpenings.get(ref) === token) entryTranscriptQuietOpenings.delete(ref);
  store().setState((state) => {
    if (state.localOpenings.get(ref) !== token) return {};
    const localOpenings = new Map(state.localOpenings);
    localOpenings.delete(ref);
    return { localOpenings };
  });
}

// Exclusive ownership between quiet and published openings: a quiet
// certification that establishes the display retires the older published
// token's consumer hold - consumers wait on that token, and the display it
// was held for now stands established. A newer published opening is never
// touched (the monotonic token comparison), and a route with no older
// published opening keeps nothing to retire. Returns whether a superseded
// hold was retired.
function retireSupersededPublishedOpening(ref: string, token: number): boolean {
  const superseded = store().getState().localOpenings.get(ref);
  if (superseded === undefined || superseded >= token) return false;
  store().setState((state) => {
    if (state.localOpenings.get(ref) !== superseded) return {};
    const localOpenings = new Map(state.localOpenings);
    localOpenings.delete(ref);
    return { localOpenings };
  });
  return true;
}

// Drop any hold for a ref whose fixture route is gone (disposal): publish the
// change so a consumer showing the port resumes ordinarily rather than waiting
// for a frame that will never arrive.
export function clearEntryTranscriptLocalOpening(ref: string): void {
  entryTranscriptQuietOpenings.delete(ref);
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
  // A saved view pages from its own oldest row while the live cursor is kept
  // for recovery, so progress must key off that boundary, not the cursor.
  if (
    model.servingFacts?.servedFrom === "saved-index" &&
    model.servingFacts.fallbackReason !== undefined &&
    route?.repository !== undefined
  ) {
    if (route.repository.savedViewExhausted()) return null;
    const before = route.repository.oldestPosition();
    if (before === undefined) return null;
    return JSON.stringify([
      "saved",
      route.repository.getBaselineIdentity(),
      before.entry,
      before.item,
      before.sub ?? 0,
    ]);
  }
  const local = route?.repository?.localOlderPosition();
  // This is a scheduling identity only. The wire path reads model.olderCursor,
  // never this key. Tag both kinds so an opaque server cursor cannot collide.
  if (local)
    return JSON.stringify(["local", route?.repository?.getBaselineIdentity(), local.entry, local.item, local.sub ?? 0]);
  if (model.olderCursor) return JSON.stringify(["server", model.olderCursor]);
  // A cursorless frontier is only exhausted when a source read or page named
  // it "none" (#5585). Until then the demand must reach the authoritative read
  // that establishes it: returning null here made HistoryPaging skip
  // loadOlderTurns and settle the demand as "no more history" (#5586).
  const authority = route?.repository?.getBaselineIdentity();
  return authority !== undefined && !olderHistoryExhausted(model)
    ? JSON.stringify(["frontier-unknown", authority])
    : null;
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

/** The repository side of the owed-range verdict: a windowed baseline's
 * outstanding recorded identities, or undefined when the repository owes
 * nothing below its baseline. */
export interface EntryRecordedRangeSource {
  owedRecorded(): { unknown: true } | { identities: ReadonlyMap<string, ThreadItemPosition> } | undefined;
  holdsWindowedBaseline(): boolean;
}

// The opening verdict's owed-range term, on its own: every identity the
// durable record still owes below a plain windowed baseline must be presented
// with a real body before the range counts as satisfied. The term is
// deliberately independent of the open's own status and cache proof — the
// verdict sites below run it for every status — because a window certified
// complete while recorded-range debt is outstanding would clear the route's
// outstanding miss and strand the pane on the wire window with no recovery
// ever scheduled. The owed range is measured in the record's own item
// identities, never in the snapshot's length: that number is a byte boundary,
// so an item count demanded from it would require 200 rows of a record that
// only has 80 and call a degraded one-blip window complete whenever bytes
// stayed small. A capture the record could not answer is unknown debt: treat
// it as owed rather than accept a view that might still be missing its
// recorded range. Only a windowed baseline can owe a recorded range: an
// authoritative or cursorless baseline speaks for everything below it, and a
// repository that never windowed cannot have the debt the arming note stands
// for, so repositories without the windowed shape stay satisfied.
export function holdsSatisfiedRecordedRange(
  model: ThreadModel | undefined,
  repository?: EntryRecordedRangeSource,
): boolean {
  const owed = repository?.holdsWindowedBaseline() ? repository.owedRecorded() : undefined;
  if (owed === undefined) return true;
  if ("unknown" in owed) return false;
  const displayed = new Map(
    model?.turns.flatMap((turn) => turn.items.map((item) => [item.transcriptKey ?? item.id, item] as const)),
  );
  // Bound the demand to the promotion window: the newest 200 DISTINCT
  // identities of what the model presents together with what the record
  // still owes. An identity the display presents that the capture also names
  // is one demand row, not two — double-counting it skews the boundary high
  // and silently drops the oldest owed rows out of the demand. Identities
  // below that boundary are older demand, not debt — the same line
  // holdsResidentForegroundRange draws for its own interval.
  const positionsByKey = new Map<string, ThreadItemPosition>();
  for (const item of displayed.values()) {
    if (item.position) positionsByKey.set(item.transcriptKey ?? item.id, item.position);
  }
  for (const [key, position] of owed.identities) positionsByKey.set(key, position);
  const floor = [...positionsByKey.values()].sort(comparePositions).slice(-200)[0];
  for (const [key, position] of owed.identities) {
    if (floor && comparePositions(position, floor) < 0) continue;
    const restored = displayed.get(key);
    if (restored === undefined || encodeTranscriptItem(restored).identityOnly) return false;
  }
  return true;
}

// The usable verdict every opening records for its route. The owed-range term
// above runs for every status: a certify-only open may be quick and its cache
// proof may say current, but the timing envelope of a certification must not
// also shortcut its completeness duty, so no cache status can certify an
// opening usable over outstanding recorded-range debt. The window term stays
// gated on recovery-needed: a healthy current or stale answer keeps a usable
// partial display (a previously loaded range can exceed the initial
// promotion), and only an answer that itself reports recovery must prove the
// whole requested window.
export function entryOpeningUsableVerdict(
  result: { status: "current" | "stale" | "recovery-needed"; retainedRangeComplete?: boolean; model?: ThreadModel },
  repository?: EntryRecordedRangeSource,
): boolean {
  return (
    result.retainedRangeComplete !== false &&
    holdsSatisfiedRecordedRange(result.model, repository) &&
    (result.status !== "recovery-needed" || holdsCompleteForegroundWindow(result.model, repository))
  );
}

// The complete-requested-window standard a local open tolerates without a
// cache certificate: every foreground item the model expects is present with
// at least its version and a real body — and the owed range above is
// satisfied. The daemon answers thread/read with the latest item window (the
// client requests 40 items) plus a server cursor, so an accepted
// fresh-baseline read leaves the model holding only that wire window, and the
// recorded cache — not the wire — is the display's source for the older
// bodies of the 200-row promotion. Without these clauses every disarm point
// below would treat a one-blip open's degraded wire window as converged and
// never restore the recorded rows.
export function holdsCompleteForegroundWindow(
  model: ThreadModel | undefined,
  repository?: EntryRecordedRangeSource,
): boolean {
  if (!holdsSatisfiedRecordedRange(model, repository)) return false;
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

// The source-superseded ACCEPTANCE verdict: a foreign hydration that settles
// while this open still owns the route hands the pane to the source lifecycle
// only when the settling read actually converged the pane — a bodied window
// over the resident range, or an empty transcript that expects nothing. The
// armed recorded-range debt is consulted here too, whatever the settling
// read recovered: a bodied window over an owed range is a source-owned
// display that still owes its convergence, so the open keeps its recovery
// handoff (the miss path below) instead of accepting the window as final.
export function supersededSourceAcceptance(
  repository: {
    holdsBodiedForegroundWindow(): boolean;
    holdsResidentForegroundRange(): boolean;
    expectsEmptyForegroundWindow(): boolean;
  } & EntryRecordedRangeSource,
  model: ThreadModel | undefined,
): boolean {
  return (
    repository.holdsBodiedForegroundWindow() &&
    (repository.holdsResidentForegroundRange() || repository.expectsEmptyForegroundWindow()) &&
    holdsSatisfiedRecordedRange(model, repository)
  );
}

// A waiter handle from the host's settlement wait: `settled` resolves when the
// next tracked hydration for the ref settles either way; `cancel` unparks a
// waiter whose open ended without the settlement firing.
type HydrationSettlementWait = {
  settled: Promise<void>;
  cancel: () => void;
};

export async function openEntryTranscript(
  ref: string,
  recovery = false,
  // The registration of the publication this open certifies, passed by a
  // caller that just awaited that publication (the claim's post-hydration
  // certification). The open's settlement wait excludes exactly that
  // registration — the claim's own hydration settling would otherwise
  // certify this re-open before its local promotion ran — while every later,
  // foreign settlement still supersedes. The identity is the awaited
  // hydration's own registration, never the map's current entry: a
  // replacement registered mid-publication displaces the claim's
  // registration there, and its settlement is foreign to this open.
  exceptRegistration?: Promise<void>,
  // Run the open without the consumer gate: the caller's own publication
  // already completed the display AND its older frontier is still outstanding
  // (the claim's post-hydration certification over a complete window with
  // live paging demand), so no pruned publication needs the gate's
  // protection and the consumers holding that demand must not be held while
  // the local read runs.
  quiet = false,
): Promise<boolean> {
  const fixture = entryTranscriptRoute(ref);
  if (!fixture || fixture.ref !== ref || !fixture.repository) return true;
  const repository = fixture.repository;
  const openingToken = beginEntryTranscriptLocalOpening(ref, quiet);
  fixture.localOpenDeadlineMissed = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let settlement: HydrationSettlementWait | undefined;
  try {
    // A fenced baseline retires the attached reader's captured opening state:
    // its openLocal answers the stale refusal without reading storage, so a
    // recovery re-open reads through a fresh reader. Attach the replacement
    // before disposing the fenced one — a sole-reader disposal synchronously
    // reclaims the recorded bodies behind a display that may still hold a
    // usable live tail — mirroring the backstop's held-pane swap ordering.
    if (recovery && fixture.reader) {
      const fenced = fixture.reader;
      fixture.reader = heldCountsAtStore(ref) ? fixture.repository.attachReader() : undefined;
      fenced.dispose();
    }
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
    // open already begun - quiet or published) must not overwrite what a
    // newer open established.
    const openingOwned = () =>
      entryTranscriptRoute(ref) === fixture &&
      fixture.repository === repository &&
      entryTranscriptLatestOpening.get(ref) === openingToken &&
      (store().getState().localOpenings.get(ref) === openingToken ||
        entryTranscriptQuietOpenings.get(ref) === openingToken);
    const established = fixture.lastLocalOpenUsable && repository.holdsEstablishedView();
    let stalled = false;
    // An open that certifies a just-awaited publication runs the local read
    // in its certify-only shape: the promotion of recorded ranges below the
    // window belongs to the recovery and promotion opens, so the
    // certification keeps the timing envelope the publication's own
    // deadlines (its release waits, its pinned certification opens) assume.
    const localRead = reader.openLocal(200, { certifyOnly: exceptRegistration !== undefined });
    const restore = established ? localRead : undefined;
    // Only the ordinary open arms the storage backstop: an established
    // restore races the stalled-open window alone, and one clearable timer
    // handle must not leak a second, un-cleared clock behind it.
    const openBound = established
      ? undefined
      : new Promise<undefined>((resolve) => {
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
              // The backstop is the clock of last resort: the primary bound
              // is the source-settlement competitor above, and this timer
              // exists for the wedge where nothing else can fire — storage
              // that never answers and no hydration in flight. A recovery
              // attempt keeps the longer stalled-open window on purpose: a
              // read that answers slower than the window but successfully
              // must be allowed to finish, or a consistently slow cache
              // strands the pane behind a race it can only keep losing.
            },
            recovery ? sessionCacheStalledOpenMs : sessionCacheOpenBackstopMs,
          );
        });
    settlement =
      established || recovery ? undefined : routeHost().nextTrackedHydrationSettlement(ref, exceptRegistration);
    let result = established
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
          localRead,
          // The open's primary deterministic bound, ordinary opens only: a
          // recovery attempt is the source machinery itself and must not
          // supersede its own re-open, and a foreign heal settling while a
          // recovery re-open waits converges through the recovery's own
          // compose registration rather than by superseding the re-open.
          // When the next tracked hydration for this ref settles — published
          // or failed, owned by the hydration lifecycle either way — the
          // local wait is over: the source owns the display from there.
          ...(settlement ? [settlement.settled.then(() => OPEN_SUPERSEDED_BY_SOURCE)] : []),
          openBound,
        ]);
    while (result && "supersededBySource" in result) {
      // A tracked hydration settled while this open's local read was still
      // pending. When the repository still holds the foreground range, the
      // source lifecycle owns the display's convergence: nothing is
      // stranded, no recovery strand is armed, and the reader stays
      // attached — a held pane keeps its reader so the repository cannot
      // reclaim the bodies behind the healed display. The still-running
      // local read publishes late under the route's existing publication
      // fences. A repository that moved past the pane's demand — its range
      // reclaimed behind a reconciliation boundary — can serve neither the
      // display nor a late publish, and the settling read itself recovered
      // nothing, so no hydration or failure retry owns what is missing.
      // The resident range alone is not enough: a live update can publish a
      // tail item over reclaimed identities and establish a resident range
      // that certifies only that tail, while the bodies the pane came to
      // restore stay missing — so the expected window must hold real bodies
      // alongside the range.
      // A re-open the caller's own machinery published the model for — the
      // claim's post-hydration certification — is source-owned only with the
      // same body-completeness proof every other superseded open shows: the
      // claim resolves for any non-null model, including a body-suppressed
      // shell a rejected response left behind, so a settling confirmation
      // that recovered nothing must not certify the still-missing window.
      // The verdict belongs to an open that still owns the route: a hydration
      // that changed incarnation, thread identity or durable authority has
      // already replaced and disposed this repository, and the replacement's
      // own lifecycle owns the pane from there. Judging the disposed
      // repository would report a miss that fences the healthy replacement,
      // so a superseded open hands the pane to whatever replaced it.
      if (!openingOwned()) {
        fixture.lastLocalOpenStatus = "superseded-by-source";
        return true;
      }
      // An accepted empty source window is established too: an empty
      // transcript never creates the foreground start the resident-range
      // check requires, but it expects nothing, so it is missing nothing -
      // the same empty-expectation semantics the window check carries. The
      // non-empty verdict is unchanged: a window that expects items still
      // needs the resident range AND the bodied window together.
      if (supersededSourceAcceptance(repository, store().getState().threads.get(ref))) {
        fixture.lastLocalOpenStatus = "superseded-by-source";
        return true;
      }
      // The settling registration may itself be composed with a recovery span
      // that is still pending: the hydration settled, the chain composed
      // behind it has not, and this wake is early - a verdict here would judge
      // an unfinished recovery's display. An open that still owns the route
      // re-arms the wait for the next settlement and keeps racing the
      // still-pending local read under the same backstop instead of falling
      // through: the recovery-needed strand belongs to the chain still
      // running, and the next wake (or the recovery's own span settling)
      // re-runs this verdict with the display as it actually stands.
      if (openingRecoveryCompletion(ref) !== undefined && openingOwned()) {
        settlement?.cancel();
        settlement = routeHost().nextTrackedHydrationSettlement(ref, exceptRegistration);
        result = await Promise.race([localRead, settlement.settled.then(() => OPEN_SUPERSEDED_BY_SOURCE), openBound]);
        continue;
      }
      // A settlement that recovered nothing — a failed read, or a successful
      // response the repository rejected behind a newer recorded update —
      // owns no convergence, and a success verdict here would strand what
      // the retired deadline used to hand to the paced recovery. Report the
      // miss verdict instead: the stranding marker and the false return arm
      // the same one-owner chain the deadline miss arms, while the
      // still-running local read keeps publishing under the existing
      // late-result fences.
      fixture.lastLocalOpenStatus = "recovery-needed";
      fixture.localOpenDeadlineMissed = true;
      fixture.unresolvedOpenMiss = true;
      // The stranding miss's convergence is owed: the recorded range below a
      // plain windowed baseline stays debt in the repository until a restore
      // presents it or the source speaks for it, surviving this open's
      // refusal and any follow-up open of the same acquisition. The note is
      // synchronous — no durable pull runs inside this open — and the demand
      // stays armed until a fill presents the range or the source speaks for
      // it.
      repository.noteOwedRecordedRange();
      if (openingOwned()) fixture.lastLocalOpenUsable = false;
      return false;
    }
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
            const usable = entryOpeningUsableVerdict(settled, repository);
            if (usable) {
              fixture.lastLocalOpenUsable = true;
              fixture.unresolvedOpenMiss = false;
              // The view may have restored recorded bodies the current
              // baseline never validated: reconcile that range against the
              // source before it is treated as converged.
              scheduleEntryRecordedReconciliation(ref);
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
            // A re-open that misses its own window leaves the route's
            // stranding markers set; only the paced chain clears them, the
            // same handoff the claim's certification open makes below.
            if (!(await openEntryTranscript(ref))) scheduleEntryOpeningRecovery(ref);
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
        // The deadline-missed open's convergence is owed the same way: the
        // repository holds the debt past the per-open marker the paced
        // chain reads.
        repository.noteOwedRecordedRange();
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
    const usable = entryOpeningUsableVerdict(result, repository);
    if (openingOwned()) fixture.lastLocalOpenUsable = usable;
    // A quiet certification that establishes the display takes exclusive
    // ownership of the route's opening: the older published opening it
    // superseded keeps the consumer gate held until its own read answers or
    // the backstop fires - and the settlement it parked on can go silent when
    // a pending refresh displaced the registration it observed. Retire that
    // superseded hold so paging and placement resume on the established
    // display, and wake the parked open so it ends through the superseded
    // path and clears its backstop timer instead of lingering toward it.
    if (quiet && usable && retireSupersededPublishedOpening(ref, openingToken)) {
      routeHost().notifyOpeningEstablishedOverSuperseded(ref);
    }
    if (usable && openingOwned()) fixture.unresolvedOpenMiss = false;
    if (usable) {
      // The view may have restored recorded bodies the current baseline never
      // validated: reconcile that range against the source before it is
      // treated as converged.
      scheduleEntryRecordedReconciliation(ref);
    }
    return usable;
  } finally {
    settlement?.cancel();
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
  const opening = withDeadline(
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
    SESSION_CACHE_LOOKUP_DEADLINE_MS,
    () => {
      active = false;
      if (installed && route.repository === installed && route.cachedUnverified) {
        retireEntryTranscriptRoute(route);
        clearEntryTranscriptLocalOpening(ref);
      }
    },
  ).finally(() => {
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
  // The per-open marker paces the chain's own attempts; the route-level
  // unresolvedOpenMiss keeps the chain armed across the follow-up opens that
  // reset the per-open marker — a pane whose promotion or certification open
  // refused fast after a miss is still owed its convergence, and no later
  // event will schedule it.
  if (route.localOpenDeadlineMissed !== true && route.unresolvedOpenMiss !== true) return;
  // The chain exists to promote recorded bodies into a display that is still
  // body-suppressed; a display another path (main's source recovery, a newer
  // open) already healed needs no re-open churn against storage that may
  // never answer.
  if (holdsCompleteForegroundWindow(store().getState().threads.get(ref), route.repository)) return;
  launchEntryOpeningRecovery(ref, route);
}

// The stale-cursor fallback's replacement inherits the recorded-range demand
// the outgoing repository still owed (see the replacement seam in threads.ts).
// The ordinary arming above refuses it — a usable open already cleared the
// miss flags, and the chain's own terminal states must not re-arm against
// storage on every later publication — so the replacement's successor arms
// here instead, once per replacement, under the chain's remaining guards: the
// dedup, the live foreground holder and the complete-window check keep this a
// no-op for a replacement whose display needs nothing.
export function scheduleReplacementEntryRecovery(ref: string): void {
  const route = entryTranscriptRoute(ref);
  if (!route || route.ref !== ref) return;
  if (pendingOpeningRecoveries.get(ref) === route) return;
  if (foregroundHolds(ref) <= 0) return;
  if (holdsCompleteForegroundWindow(store().getState().threads.get(ref), route.repository)) return;
  launchEntryOpeningRecovery(ref, route);
}

function launchEntryOpeningRecovery(ref: string, route: EntryTranscriptRoute): void {
  pendingOpeningRecoveries.set(ref, route);
  const recovery = (async () => {
    let stalledAttempts = 0;
    // One source handoff per chain: the handoff is the chain's last resort,
    // and a promotion re-open that misses its stalled window re-enters the
    // paced attempts below instead of ending the chain. The cap keeps
    // storage that never answers from looping the handoff.
    let sourceHandedOff = false;
    for (;;) {
      const opened = await openEntryTranscript(ref, true);
      if (opened) {
        return;
      }
      if (entryTranscriptRoute(ref) !== route) {
        return;
      }
      if (foregroundHolds(ref) <= 0) {
        return;
      }
      // Another path healed the display mid-chain only while the repository
      // still backs that display: a pending attempt whose timeout disposed the
      // shared reader lets the repository reclaim its bodies without
      // publishing, so the store's snapshot alone can be a stale complete view
      // the next publication would prune. Require the resident range too, and
      // a reclaimed repository re-opens instead of ending recovery on it.
      if (
        holdsCompleteForegroundWindow(store().getState().threads.get(ref), route.repository) &&
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
      // A structured history failure is the one refusal that must not take
      // that handoff: the read's own handling already recorded the single
      // visible diagnostic (readThreadHydration's applyHistoryReadFailure)
      // and schedules no retry - the next read comes from the next event, a
      // resync, a reconnect or the user, never from us. The local attempts
      // above remain this chain's contribution: storage that heals can still
      // restore the display. But while the diagnostic stands, the automatic
      // source handoff would be exactly the retry loop that contract
      // forbids, so the chain stops here and the pane keeps its diagnostic.
      // A transport failure never sets it, and a later successful read
      // discards the failed history wholesale, so the gate cannot outlive
      // the state it describes.
      if (store().getState().threads.get(ref)?.history?.failed !== undefined) return;
      if (sourceHandedOff) return;
      sourceHandedOff = true;
      route.repository?.beginFreshBaseline();
      publishEntryTranscriptModel(ref);
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
        return;
      }
      if (entryTranscriptRoute(ref) !== route) return;
      if (foregroundHolds(ref) <= 0) return;
      // Mirror the raced recovery's full sequence: the read that replaced
      // the view is followed by the local re-open that promotes its
      // restored bodies into the display — the loop's next attempt. A source
      // response that already healed the display needs no further cache
      // read to converge. The re-open is an attempt like any other, bounded
      // by the stalled-open window: a read that outlasts it — healthy but
      // slow — re-enters the paced attempts below rather than ending the
      // chain, because the miss sets the route's markers and no caller
      // follows the chain's own re-open to re-arm them.
      if (holdsCompleteForegroundWindow(store().getState().threads.get(ref), route.repository)) return;
      // The handoff changed the baseline its re-open certifies, so the
      // chain gets one fresh paced budget behind it.
      stalledAttempts = 0;
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
  routeHost().registerHydrationCompletion(ref, recovery);
  void recovery.then(() => {
    if (pendingOpeningRecoveries.get(ref) === route) pendingOpeningRecoveries.delete(ref);
    if (openingRecoveryCompletions.get(ref) === recovery) openingRecoveryCompletions.delete(ref);
  });
}

// An open that restored recorded bodies no current-baseline statement
// validated (see `unreconciledRecorded`) must not leave that range
// converged-on-cached-guesses: an older item that changed while the client
// was away would keep displaying its pre-gap cached version until the user
// happened to page that deep. The route reconciles the range against the
// source automatically — the same bounded older page a user's scroll issues —
// while the restored cached bodies keep displaying through the
// reconciliation. One chain per repository instance, stopping at the first
// page that makes no progress: the pending slot is owned by the REPOSITORY,
// never the route, because a stale-cursor replacement changes the repository
// under the same route — the replacement must be free to start its own
// successor chain while the old chain exits on the mismatch below, and the
// old chain's finally frees only a slot that still names its own repository.
// The ordinary owner-scoped paths and the user's own paging own the rest.
const pendingRecordedReconciliations = new Map<string, TranscriptRepository>();

export function scheduleEntryRecordedReconciliation(ref: string): void {
  const route = entryTranscriptRoute(ref);
  if (!route || route.ref !== ref) return;
  const repository = route.repository;
  if (!repository || repository.unreconciledRecorded().size === 0) return;
  if (pendingRecordedReconciliations.get(ref) === repository) return;
  if (foregroundHolds(ref) <= 0) return;
  pendingRecordedReconciliations.set(ref, repository);
  void (async () => {
    try {
      // The promotion window bounds the owed range and each page is one
      // bounded read, so the loop cannot run away; it ends when the source
      // has spoken for every identity the restore displayed or when a page
      // reconciles nothing (offline, exhausted, superseded).
      for (let pages = 0; pages < 8; pages += 1) {
        if (entryTranscriptRoute(ref) !== route) return;
        if (foregroundHolds(ref) <= 0) return;
        const owed = repository.unreconciledRecorded();
        if (owed.size === 0) return;
        const paged = await routeHost().reconcileRecordedRange(ref);
        if (entryTranscriptRoute(ref) !== route || route.repository !== repository) return;
        // A page a competing read discarded mid-flight — an overlay/end
        // reconciliation read, a refresh, anything that moved the baseline
        // while the page was in the air — is a retryable rejection, not
        // exhaustion: the next iteration re-captures the settled baseline's
        // own cursor and retries within this same bounded budget. A page
        // that could not be issued, or an accepted page that reconciles
        // nothing, ends the chain (offline, exhausted, superseded).
        if (paged === "unissued") return;
        if (paged === "discarded") continue;
        if (repository.unreconciledRecorded().size === owed.size) return;
      }
    } catch {
      // A failed reconciliation keeps the displayed cached bodies; the
      // ordinary retry paths and the user's own paging own the rest.
    } finally {
      if (pendingRecordedReconciliations.get(ref) === repository) pendingRecordedReconciliations.delete(ref);
    }
  })();
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
  try {
    return await withDeadline(
      Promise.all([fixture.cache.getClearEpoch(), fixture.cache.getHistoryGeneration(historyKey)]).then(
        ([clearEpoch, historyGeneration]) => ({ clearEpoch, historyGeneration }),
      ),
      SESSION_CACHE_LOOKUP_DEADLINE_MS,
    );
  } catch {
    return undefined;
  }
}
