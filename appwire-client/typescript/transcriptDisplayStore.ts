// The transcript display hub-defaults store both apps' display settings run
// on: one hub's per-layout transcript display defaults
// (evener/settings/transcriptDisplay/{get,patch} and the `changed`
// broadcast), with a direct write for a host that edits live and a
// checkpointed draft editor for a host that edits offline.
// createTranscriptDisplayStore is a factory - each app builds the one
// instance it wires to its connection and its view layer, tests build their
// own - returning the framework-free triple plus the connection-lifecycle
// methods the host drives. Pure logic - no DOM, no React, no storage of its
// own: the client and the draft storage are ports.
//
// This store holds the HUB layer only. A host that layers a local override
// on top (the web's per-browser localStorage value and its viewport class)
// keeps that beside the store and resolves the effective configuration
// itself; this store never sees it.
//
// Two write paths share the PATCH: patchHubDefault is the live editor's
// direct write (a preview draft per layout while the request is out,
// dropped when it settles, the canonical response applied); saveDraft is the
// offline editor's checkpointed write (the intent is persisted through the
// draft port BEFORE the request leaves, and a lost reply leaves it
// `writeUncertain` until an authoritative read). Which one a host uses is the
// host's product decision; the hub state they confirm is one.
//
// Every post-await site fences through one predicate of the shared ready
// generation fence (readyGenerationFence.ts) and every reset site retires the
// payload through one helper (retirePayload), so a reply that arrives after
// the generation ended, support dropped or the hub was replaced lands nothing.

import type { AppwireClient } from "./client";
import { createDraftRepository, UnreadableDraftError } from "./draftCheckpointPort";
import { errorText, WireError } from "./errors";
import { createFrameworkFreeStore, type FrameworkFreeStore } from "./frameworkFreeStore";
import { createReadyGenerationFence, lostHub } from "./readyGenerationFence";
import {
  configFingerprint,
  fromWireConfig,
  fromWireDefault,
  fromWireDefaults,
  type HubTranscriptDisplayDefault,
  normalizeConfig,
  shippedDefault,
  type TranscriptDisplayConfigV1,
  toWireConfig,
  type ViewportClass,
} from "./transcriptDisplayConfig";
import type { AnyNotification, FeatureSet } from "./types.gen";

/** The two members of the client this store calls; AppwireClientLike satisfies it. */
export type TranscriptDisplayClient = Pick<AppwireClient, "request" | "onNotification">;

export type TranscriptDisplaySupport = "unknown" | "supported" | "unsupported";

/** Whether the connected hub advertises transcript display settings: unknown
 * until the handshake's feature set is in hand. */
export function transcriptDisplaySupport(
  features: Pick<FeatureSet, "transcriptDisplaySettings"> | undefined,
): TranscriptDisplaySupport {
  if (features === undefined) return "unknown";
  return features.transcriptDisplaySettings === true ? "supported" : "unsupported";
}

export const LAYOUTS: readonly ViewportClass[] = ["desktop", "mobile"];

export function isViewportClass(value: unknown): value is ViewportClass {
  return value === "desktop" || value === "mobile";
}

/** The hub's confirmed default per layout; a layout the hub has not answered
 * for yet is absent. */
export type HubDefaultsByLayout = Partial<Record<ViewportClass, HubTranscriptDisplayDefault>>;

/** A draft persisted through the draft port: the configuration the user
 * proposed for one layout, the hub revision it was composed against, and
 * whether a PATCH carrying it left without a confirmed outcome. */
export interface TranscriptDraftCheckpoint {
  id: string;
  layout: ViewportClass;
  baseRevision: number;
  config: TranscriptDisplayConfigV1;
  writeUncertain: boolean;
}

/** The storage port the checkpointed draft editor writes through. Every
 * method may throw; the store maps a throw to `storageUnavailable`. */
export interface TranscriptDraftStorage {
  createId(): string;
  load(): unknown;
  save(checkpoint: TranscriptDraftCheckpoint): void;
  /** Removes the stored checkpoint only if it is still this one. */
  removeIf(checkpoint: TranscriptDraftCheckpoint): void;
}

/** The draft port a store without one runs on: the proposal lives in the
 * store's state only and does not survive the instance. */
function memoryDraftStorage(): TranscriptDraftStorage {
  return { createId: () => "memory", load: () => null, save() {}, removeIf() {} };
}

/** The offline editor's proposal: one layout's configuration and the
 * confirmed revision it was composed against. */
export interface TranscriptDraft {
  layout: ViewportClass;
  revision: number;
  config: TranscriptDisplayConfigV1;
}

/** One `changed` broadcast, as the host may also feed it in by hand. */
export interface TranscriptDisplayChange {
  layout: ViewportClass;
  revision: number;
  config: TranscriptDisplayConfigV1;
}

export interface TranscriptDisplayStoreFields {
  hubSupport: TranscriptDisplaySupport;
  hubLoading: boolean;
  /** The last hub-sourced failure (a failed read, a malformed reply); the
   * per-layout copy of a write's failure rides `hubErrors`. */
  hubError: string | null;
  /** A write's failure on the layout it wrote; cleared by the layout's next
   * write leaving. */
  hubErrors: Partial<Record<ViewportClass, string>>;
  /** The hub's confirmed defaults. They keep presenting across a transient
   * disconnect (the effective configuration must not flap); `loaded` says
   * whether they are confirmed for the CURRENT ready generation. */
  hub: HubDefaultsByLayout;
  /** True only while `hub` was confirmed by the hub for the current ready
   * generation. Set on every applied payload; cleared when the generation
   * ends, support drops, the hub is replaced or the store resets. Writes gate
   * on it: a PATCH composed against a previous hub's revision would overwrite
   * the new hub's default on a revision collision. */
  loaded: boolean;
  /** The direct write's preview per layout while its request is out. */
  drafts: Partial<Record<ViewportClass, TranscriptDisplayConfigV1>>;
  /** The offline editor's proposal. Restored from the draft port at creation. */
  draft: TranscriptDraft | null;
  /** A checkpointed write is in flight. */
  saving: boolean;
  /** A checkpointed write left without a confirmed outcome; edits stay
   * blocked until an authoritative read lands. */
  writeUncertain: boolean;
  /** The draft port threw; edits stay blocked until refreshHubDefaults can
   * restore the checkpoint again. */
  storageUnavailable: boolean;
  /** The port answered but what it held could not be read. The RECORD is the
   * problem, not the port: the section still loads, and discarding is allowed
   * and is what clears it. A host must offer that discard, or the section is
   * locked with no way out. */
  draftUnreadable: boolean;
  /** The draft's base revision is not the confirmed revision of its layout
   * (the hub moved under it, or a write's outcome is unknown): saveDraft
   * refuses until rebaseDraft reviews the current value. */
  draftConflict: boolean;
  /** The draft port's own failures (save, discard, restore, cleanup);
   * hub-sourced failures ride hubError and an unconfirmed write is the
   * `writeUncertain` fact itself. */
  draftError: string | null;
}

export interface TranscriptDisplayStoreActions {
  /** Refreshes both layouts from the hub under the current ready generation;
   * a no-op without one, while unsupported, or while the draft port is
   * unavailable. */
  refreshHubDefaults(): Promise<void>;
  /** The live editor's write: previews `config` on `layout` while the PATCH
   * is out, applies the canonical response, clears the preview. A lost
   * revision race adopts the canonical current the hub reports and rejects;
   * a later write on the same layout supersedes an earlier one's reply. */
  patchHubDefault(layout: ViewportClass, config: TranscriptDisplayConfigV1): Promise<HubTranscriptDisplayDefault>;
  /** Applies one hub change by hand (a host relaying a broadcast it received
   * on its own channel): a malformed or stale change is ignored. */
  applyHubChange(change: TranscriptDisplayChange): void;
  /** Replaces the draft with `config` for `layout`, checkpointed through the
   * draft port. Throws while the store is not editable (see assertEditable). */
  editDraft(layout: ViewportClass, config: TranscriptDisplayConfigV1): void;
  /** PATCHes `config` (default: the draft, else the layout's confirmed value)
   * with the draft's base revision as expectedRevision, checkpointing the
   * intent before the request leaves. Rejects on a stale draft (rebaseDraft
   * first), while a save is in flight, and on any failed or unconfirmed
   * write. Without a draft, `layout` names the layer being saved. */
  saveDraft(layout?: ViewportClass, config?: TranscriptDisplayConfigV1): Promise<HubTranscriptDisplayDefault>;
  /** Drops the draft and its checkpoint. */
  discardDraft(): void;
  /** Moves the draft's base onto the confirmed revision the user reviewed;
   * throws when the hub has moved again since. */
  rebaseDraft(reviewedRevision: number): void;
}

export type TranscriptDisplayStoreState = TranscriptDisplayStoreFields & TranscriptDisplayStoreActions;

export interface TranscriptDisplayStore extends FrameworkFreeStore<TranscriptDisplayStoreState> {
  /** Publishes the connection's support for transcript display settings.
   * Resolving to unsupported retires the payload; flapping back to supported
   * while a ready generation is active begins a new one so no pre-flap
   * in-flight work lands, and loads. */
  setSupport(support: TranscriptDisplaySupport): void;
  /** The client is ready: subscribe to its notifications and fence every
   * later refresh and write to this generation. The host refreshes next. */
  beginReadyGeneration(): void;
  /** The ready generation ended (disconnect, client replacement): drop the
   * subscription, fence in-flight work out, and mark the confirmed defaults
   * as no longer current. They keep presenting - a transient disconnect must
   * not flap the effective configuration. */
  endReadyGeneration(): void;
  /** The client was replaced by one for a possibly different hub: the
   * confirmed defaults are the previous hub's, so they stop presenting and
   * the payload state resets. */
  detachHub(): void;
  /** Ends the generation and returns the state to its initial values; the
   * host re-publishes support afterwards. */
  reset(): void;
  /** Ends the generation for good: every later refresh and write is refused
   * and nothing in flight lands. */
  dispose(): void;
}

export interface TranscriptDisplayStoreDeps {
  client: TranscriptDisplayClient;
  /** The draft port. Without one the draft editor keeps its state in memory. */
  drafts?: TranscriptDraftStorage;
}

function initialState(): TranscriptDisplayStoreFields {
  return {
    hubSupport: "unknown",
    hubLoading: false,
    hubError: null,
    hubErrors: {},
    hub: {},
    loaded: false,
    drafts: {},
    draft: null,
    saving: false,
    writeUncertain: false,
    storageUnavailable: false,
    draftUnreadable: false,
    draftConflict: false,
    draftError: null,
  };
}

const UNAVAILABLE_MESSAGE = "Hub transcript display settings are unavailable.";
const MALFORMED_DEFAULTS_MESSAGE = "Hub returned malformed transcript display defaults";
const MALFORMED_PATCH_MESSAGE = "Hub returned malformed transcript display PATCH response";
/** The draft port's fixed failure copy: a raw storage error may carry a local
 * path, so it never reaches state verbatim. */
const DRAFT_SAVE_FAILED_MESSAGE = "Could not save the transcript draft locally.";
const DRAFT_DISCARD_FAILED_MESSAGE = "Could not discard the transcript draft locally.";
const DRAFT_RESTORE_FAILED_MESSAGE = "Could not restore the saved transcript draft. Check current settings to retry.";
const DRAFT_CLEANUP_FAILED_MESSAGE =
  "The hub confirmed this save, but the local draft could not be updated. Check current settings to retry.";

/** A distinguishable class for the response-shape rejection: the direct write
 * reports it on both error fields, where a transport failure reports on the
 * layout alone. */
export class InvalidPatchResponseError extends Error {}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isRevision(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

/** Structural check for one `changed` broadcast: the trust-boundary re-check
 * so a malformed payload degrades to a re-read instead of a crash. Every
 * field must be present and well-formed; an empty or partial record is not a
 * change. */
export function fromWireChange(value: unknown): TranscriptDisplayChange | undefined {
  if (!isRecord(value) || !isViewportClass(value.layout) || !isRevision(value.revision)) return undefined;
  const config = fromWireConfig(value.config);
  return config === undefined ? undefined : { layout: value.layout, revision: value.revision, config };
}

/** The PATCH response shape: exactly {layout, revision, config} for the
 * layout written, or undefined. Semantic checks against the request belong to
 * the caller. */
function fromWirePatchResponse(value: unknown, layout: ViewportClass): HubTranscriptDisplayDefault | undefined {
  if (!isRecord(value)) return undefined;
  const keys = Object.keys(value);
  if (keys.length !== 3 || !("layout" in value) || !("revision" in value) || !("config" in value)) return undefined;
  if (value.layout !== layout || !isRevision(value.revision)) return undefined;
  const config = fromWireConfig(value.config);
  return config === undefined ? undefined : { revision: value.revision, config };
}

/** The canonical current the hub reports with a lost revision race, or
 * undefined for any other rejection. */
function conflictCurrent(error: unknown, layout: ViewportClass): HubTranscriptDisplayDefault | undefined {
  if (!(error instanceof WireError) || error.code !== -32013 || !isRecord(error.data)) return undefined;
  if (error.data.evenerErrorInfo !== "conflict" || error.data.layout !== layout) return undefined;
  return fromWireDefault(error.data.current);
}

/** A PATCH reply decoded as THIS write's own outcome: the requested
 * configuration at the confirmed revision (a no-op write) or one past it,
 * never a jump the request cannot account for. Both write paths decode here,
 * so neither can accept a reply the other would refuse. What a path does when
 * the STORE has moved past the reply meanwhile is the path's own rule, not
 * the reply's: the direct write refuses it, the checkpointed editor keeps its
 * proposal for review. */
function decodePatchReply(
  result: unknown,
  layout: ViewportClass,
  confirmed: HubTranscriptDisplayDefault,
  requested: TranscriptDisplayConfigV1,
): HubTranscriptDisplayDefault {
  const canonical = fromWirePatchResponse(result, layout);
  const requestedFingerprint = configFingerprint(requested);
  const isThisWrite =
    canonical !== undefined &&
    configFingerprint(canonical.config) === requestedFingerprint &&
    (canonical.revision === confirmed.revision + 1 ||
      // A no-op write: the hub kept the revision because the requested
      // configuration is the confirmed one.
      (canonical.revision === confirmed.revision && requestedFingerprint === configFingerprint(confirmed.config)));
  if (canonical === undefined || !isThisWrite) throw new InvalidPatchResponseError(MALFORMED_PATCH_MESSAGE);
  return canonical;
}

// discardStoredDraft is re-exported here (not just from draftCheckpointPort
// directly) so index.ts's existing `discardStoredDraft as
// discardStoredTranscriptDraft` import keeps working unchanged.
export { discardStoredDraft } from "./draftCheckpointPort";

function invalidDraft(): never {
  throw new UnreadableDraftError("Invalid transcript preference draft.");
}

/** The strict checkpoint check the draft editor runs on a restored
 * checkpoint. Throws on anything else. */
function draftCheckpoint(value: unknown): TranscriptDraftCheckpoint {
  if (!isRecord(value)) invalidDraft();
  if (
    typeof value.id !== "string" ||
    !value.id.length ||
    !isRevision(value.baseRevision) ||
    typeof value.writeUncertain !== "boolean" ||
    !isViewportClass(value.layout)
  )
    invalidDraft();
  let config: TranscriptDisplayConfigV1;
  try {
    config = normalizeConfig(value.config as TranscriptDisplayConfigV1);
  } catch {
    invalidDraft();
  }
  return {
    id: value.id,
    layout: value.layout,
    baseRevision: value.baseRevision,
    config,
    writeUncertain: value.writeUncertain,
  };
}

/** A draft composed against one confirmed revision is stale once its layout
 * has confirmed a different one. */
function staleDraft(draft: TranscriptDraft | null, hub: HubDefaultsByLayout): boolean {
  if (draft === null) return false;
  const confirmed = hub[draft.layout];
  return confirmed !== undefined && confirmed.revision !== draft.revision;
}

export function createTranscriptDisplayStore(deps: TranscriptDisplayStoreDeps): TranscriptDisplayStore {
  const { client } = deps;
  const drafts = createDraftRepository(deps.drafts ?? memoryDraftStorage(), draftCheckpoint);

  // Ready-generation wiring: every refresh and write captures the generation
  // it started under and fences its reply on the shared fence.
  const fence = createReadyGenerationFence(isSupported);
  let unwireNotification: (() => void) | null = null;
  /** Set when a changed-notification is dropped because the current
   * generation has no confirmed state yet: the refresh that lands the state
   * may carry a response PREDATING the dropped change, so a successful
   * refresh with the flag set fires ONE follow-up fetch. Per generation. */
  let missedChangeNotification = false;
  /** The direct write's token per layout: a later write on the same layout
   * supersedes an earlier one's reply. Bumped for every layout by retirement. */
  const patchTokens = new Map<ViewportClass, number>();
  /** What each direct write's preview was composed against: the hub generation
   * and the confirmed revision. A preview is a GUESS at what its layer will
   * hold. A confirmed payload that CONTRADICTS the guess has settled the layer
   * at something else, so the guess must not keep sitting on top of it - true
   * however the write's own reply ended up, including never landing. A payload
   * that MATCHES it is left alone: the host still has an unacknowledged write to
   * report, and the preview is what it reports about.
   *
   * What decides is the CONFIGURATION, and the revision only says whether the
   * number is comparable at all. Within the generation the guess was made in,
   * numbering is the same hub's, so only a higher revision has decided anything
   * and an equal one is the same decision re-stated. ACROSS a generation
   * boundary the number is not comparable - a reconnect may reach a restarted
   * hub numbering from its own 1, or the same hub at the same number - so the
   * configuration alone decides: a first authoritative payload that contradicts
   * the guess clears it whether it numbers lower, equal or higher. Not part of
   * `drafts`, which is the host's published shape. */
  const previewBases = new Map<ViewportClass, { generation: number; revision: number }>();
  const store = createFrameworkFreeStore<TranscriptDisplayStoreState>(() => ({
    ...initialState(),
    // The draft restore is part of the initial state so a host that builds
    // the store synchronously (native, per connection) sees the persisted
    // proposal on its first read, before any refresh.
    ...restoreDraft({ loaded: false, hub: {} }),
    refreshHubDefaults,
    patchHubDefault,
    applyHubChange,
    editDraft,
    saveDraft,
    discardDraft,
    rebaseDraft,
  }));
  const { getState, setState } = store;

  function restoreDraft(confirmed: {
    loaded: boolean;
    hub: HubDefaultsByLayout;
  }): Partial<TranscriptDisplayStoreFields> {
    try {
      const checkpoint = drafts.load();
      const draft = checkpoint
        ? { layout: checkpoint.layout, revision: checkpoint.baseRevision, config: checkpoint.config }
        : null;
      return {
        draft,
        writeUncertain: checkpoint?.writeUncertain ?? false,
        storageUnavailable: false,
        draftUnreadable: false,
        draftError: null,
        draftConflict: confirmed.loaded && staleDraft(draft, confirmed.hub),
      };
    } catch (error) {
      return {
        storageUnavailable: true,
        draftUnreadable: error instanceof UnreadableDraftError,
        draftError: DRAFT_RESTORE_FAILED_MESSAGE,
      };
    }
  }

  function isSupported(): boolean {
    return getState().hubSupport === "supported";
  }

  /** Takes the layout for this write. A layer carries ONE hub value, so the
   * later write on it owns the outcome and every earlier reply for that layer
   * is superseded - whichever path either write came from. Payload retirement
   * takes every layout the same way. */
  function claimLayoutWrite(layout: ViewportClass): number {
    const token = fence.claimWrite();
    patchTokens.set(layout, token);
    return token;
  }

  /** A write's reply is its own to land: live hub, and no later write on its
   * layout or payload retirement has superseded it. Both write paths fence
   * here. */
  function writeStillMine(generation: number, layout: ViewportClass, token: number): boolean {
    return fence.liveHub(generation) && patchTokens.get(layout) === token;
  }

  /** WHY a reply is not ours, because the two answers call for opposite things.
   * SUPERSEDED: a later write on this layer, or a payload retirement, has taken
   * over - whoever took over owns `saving` now, and this reply must touch
   * nothing. LOST-HUB: this write's own claim is intact and only support went
   * away (the unknown window keeps the state and the in-flight work), so
   * nothing else will ever settle this write and the editor must not be left
   * mid-write. */
  function whyFenced(generation: number, layout: ViewportClass, token: number): "superseded" | "lost-hub" {
    return lostHub(fence, generation, patchTokens.get(layout) === token) ? "lost-hub" : "superseded";
  }

  /** Publishes the end of a write whose reply can never be settled by anything
   * else. A superseded reply publishes nothing: its successor owns the flags. */
  function settleUnsettleableWrite(why: "superseded" | "lost-hub"): void {
    if (why === "lost-hub" && getState().saving) setState({ saving: false, writeUncertain: true });
  }

  /** The confirmed payload can no longer be acted on (the generation ended,
   * support dropped, the hub was replaced): it stops presenting as current,
   * every reply still in flight is superseded so it lands nothing, and the
   * in-flight flags end here. A checkpointed write caught mid-flight has an
   * UNKNOWN outcome - exactly what writeUncertain means, and what its
   * checkpoint already says on disk; the next authoritative read settles it.
   * A direct write's preview stays up for a retirement that keeps the HUB
   * (a transient disconnect, same hub): its outcome is as unknown as the
   * checkpointed write's, and the host shows it until a confirmed value
   * replaces it. The sites that discard the hub identity instead - detachHub
   * and a support drop - clear it with the rest, because a preview describes
   * a write sent to a hub this store has left and says nothing about the next
   * one's value. `extra` is the site's own addition in the same publish. */
  function retirePayload(extra: Partial<TranscriptDisplayStoreFields> = {}): void {
    fence.supersede();
    for (const layout of LAYOUTS) patchTokens.set(layout, fence.writeToken);
    const state = getState();
    setState({
      loaded: false,
      saving: false,
      hubLoading: false,
      writeUncertain: state.writeUncertain || state.saving,
      ...extra,
    });
  }

  /** A valid external payload that is not stale has moved the hub PAST
   * whatever the store-wide error described, so the retry notice it left goes
   * with it. Per-layout write errors are the write's own and stay: they say
   * which layer a user's change did not reach. Keyed on applyHubDefault's
   * return rather than ridden in as `extra`, which publishes for an IGNORED
   * payload too - a stale broadcast moves nothing and so clears nothing. */
  function externalPayloadLanded(applied: boolean): void {
    if (applied && getState().hubError !== null) setState({ hubError: null });
  }

  /** Applies one layout's confirmed default (a GET's layer, a `changed`
   * broadcast, a PATCH response, a conflict's canonical current). While the
   * current generation has confirmed state, a revision at or below the held
   * one is stale and ignored; a new generation's first payload is
   * authoritative at ANY revision, because revision numbering is the hub's
   * and a reconnect can be a hub restart. That exception belongs to the
   * authoritative READ and to its whole payload: only `refreshFor` passes
   * `authoritative`, once for every layer it applies, so the first layer's
   * apply does not fence the rest. A broadcast, a relayed change and a
   * write's reply are never a generation's first payload and never confirm
   * it - `loaded` rides the read's own publish. `extra` lands in the same
   * publish. Returns false for an ignored payload. */
  function applyHubDefault(
    layout: ViewportClass,
    value: HubTranscriptDisplayDefault,
    extra: Partial<TranscriptDisplayStoreFields> = {},
    authoritative = false,
  ): boolean {
    const state = getState();
    const previous = state.hub[layout];
    if (!authoritative && previous !== undefined && value.revision <= previous.revision) {
      if (Object.keys(extra).length > 0) setState(extra);
      return false;
    }
    const hub = { ...state.hub, [layout]: value };
    const previewBase = previewBases.get(layout);
    const preview = state.drafts[layout];
    const contradictsPreview =
      previewBase !== undefined &&
      preview !== undefined &&
      configFingerprint(value.config) !== configFingerprint(preview) &&
      (previewBase.generation !== fence.generation || value.revision > previewBase.revision);
    setState({
      hub,
      ...(contradictsPreview ? clearPreview(layout) : {}),
      draftConflict: staleDraft(state.draft, hub),
      ...extra,
    });
    return true;
  }

  /** Drops one layout's direct-write preview and its basis: the value that
   * replaced it (this write's own reply, a newer external payload, or the
   * checkpointed editor's confirmed save) must not sit under a guess that no
   * longer describes what is out. */
  function clearPreview(layout: ViewportClass): Partial<TranscriptDisplayStoreFields> {
    previewBases.delete(layout);
    const drafts = { ...getState().drafts };
    delete drafts[layout];
    return { drafts };
  }

  /** Drops every layout's preview and its basis: every layer's confirmed
   * value is gone (a support drop, detaching the hub), so no guess composed
   * against any of them can still describe what is out. */
  function clearPreviews(): Partial<TranscriptDisplayStoreFields> {
    previewBases.clear();
    return { drafts: {} };
  }

  function endReadyGeneration(): void {
    fence.end();
    unwireNotification?.();
    unwireNotification = null;
    retirePayload();
  }

  function beginReadyGeneration(): void {
    const generation = fence.begin();
    if (generation < 0) return;
    missedChangeNotification = false;
    unwireNotification?.();
    unwireNotification = client.onNotification((notification) => {
      if (!fence.isCurrent(generation)) return;
      onNotification(notification, generation);
    });
  }

  function setSupport(support: TranscriptDisplaySupport): void {
    const state = getState();
    if (support === "supported") {
      // A flap BACK from unsupported: the pre-flap generation's in-flight
      // work must not survive into the refreshed state, so the transition
      // begins a NEW ready generation (the epoch bump fences every token
      // captured pre-flap). The refresh runs AFTER the bump, under the new
      // epoch. Only the TRANSITION bumps: a same-value re-notification and
      // the unknown window (a transient disconnect keeps its state and its
      // in-flight work) leave the generation intact.
      if (state.hubSupport === support) return;
      if (state.hubSupport === "unsupported" && fence.generation >= 0) beginReadyGeneration();
      setState({ hubSupport: support });
      // The transition INTO supported with a ready generation active is the
      // load trigger - from unknown (the handshake's features resolving after
      // the client was ready) as much as from unsupported.
      if (fence.generation >= 0) void refreshFor(fence.generation);
      return;
    }
    if (support === "unsupported") {
      // The feature set is KNOWN and does not advertise transcript display
      // settings: the shipped defaults are in effect. The payload state goes
      // with it - a retained revision could be HIGHER than a returning hub's
      // (a restored backup, a reset state file) and would eat its refresh.
      // Only the TRANSITION retires, as only the transition into supported
      // refreshes: the host re-resolves support on every connection publish,
      // and while unsupported every confirm path is gated, so a repeat has
      // nothing left to retire and would publish a fresh `hub` identity per
      // tick.
      if (state.hubSupport === support) return;
      retirePayload({ hubSupport: support, hubError: null, hubErrors: {}, hub: {}, ...clearPreviews() });
      return;
    }
    // The unknown window is the transient-disconnect case: the payload keeps
    // presenting until endReadyGeneration retires it, so only the connection
    // facts publish, and only when one changed.
    if (state.hubSupport !== support || state.hubLoading || state.hubError !== null)
      setState({ hubSupport: support, hubLoading: false, hubError: null });
  }

  function onNotification(notification: AnyNotification, generation: number): void {
    if (notification.method !== "evener/settings/transcriptDisplay/changed") return;
    // A late notification landing during an unsupported window carries a hub
    // state the section says is not in effect.
    if (!isSupported()) return;
    if (changeArrivedBeforeConfirmation()) return;
    const change = fromWireChange(notification.params);
    if (change === undefined) {
      // The hub said something changed and this store could not read what:
      // the listing is uncertain, so the truth is read rather than the
      // broadcast dropped.
      void refreshFor(generation);
      return;
    }
    externalPayloadLanded(applyHubDefault(change.layout, { revision: change.revision, config: change.config }));
  }

  /** A change arriving before the CURRENT generation's refresh has confirmed
   * state carries pre-refresh cargo: its revision predates what the in-flight
   * read will confirm, and applying it would let the stale guard eat the
   * read's authoritative payload. It is dropped; the read fetches the truth,
   * and fires ONE follow-up so a change the read's response predates is not
   * lost. Both delivery paths - this store's own subscription and a change
   * the host relays - ask here, because the cargo is the same either way.
   * A notification only ever fires under a live generation, so this is the
   * relayed path's answer that differs: with no generation there is no read
   * to predate. */
  function changeArrivedBeforeConfirmation(): boolean {
    // Only a LIVE generation has a read to be predated by. With none - a host
    // seeding this store from its own cache before it connects - there is
    // nothing in flight to lose the change to, and nothing to follow up.
    if (fence.generation < 0 || getState().loaded) return false;
    missedChangeNotification = true;
    return true;
  }

  /** A change the HOST relayed rather than the store's own subscription (the
   * web's one client feeds several stores). Like a notification it is not
   * this generation's confirmation: it merges into `hub` under the ordinary
   * stale guard and leaves the authoritative read to confirm. Unlike a
   * notification - which only a supported hub ever sends - a relayed change
   * is cargo some OTHER window already accepted before this store's own
   * generation existed, so `changeArrivedBeforeConfirmation`'s
   * no-live-generation exception (a host seeding this store from its cache
   * before it connects) would otherwise let it through while this section
   * has decided the hub does not carry the setting at all. Guarded on
   * `unsupported` specifically, not `isSupported()`, so it still merges
   * during the unknown window - the tests exercise a relay landing there,
   * before a connection's features are known. */
  function applyHubChange(change: TranscriptDisplayChange): void {
    if (!isViewportClass(change.layout) || !isRevision(change.revision)) return;
    if (getState().hubSupport === "unsupported") return;
    if (changeArrivedBeforeConfirmation()) return;
    let config: TranscriptDisplayConfigV1;
    try {
      config = normalizeConfig(change.config);
    } catch {
      // A malformed change cannot be a confirmed hub record.
      return;
    }
    externalPayloadLanded(applyHubDefault(change.layout, { revision: change.revision, config }));
  }

  /** What an authoritative read settles for the draft editor: an uncertain
   * write's outcome is now whatever the hub confirmed, so the checkpoint is
   * re-marked and edits unblock. A read that started before the write left,
   * or landed while one is in flight, says nothing about that write. */
  function settledWrite(writeSerialAtStart: number): Partial<TranscriptDisplayStoreFields> {
    const { draft, writeUncertain, saving } = getState();
    if (writeSerialAtStart !== fence.writeToken || saving) return {};
    if (draft !== null && writeUncertain) {
      try {
        persistDraft({
          layout: draft.layout,
          baseRevision: draft.revision,
          config: draft.config,
          writeUncertain: false,
        });
      } catch {
        return { draftError: DRAFT_SAVE_FAILED_MESSAGE };
      }
    }
    return { writeUncertain: false };
  }

  async function refreshFor(generation: number): Promise<void> {
    if (!fence.liveHub(generation)) return;
    const serial = fence.claimRead();
    const writeSerialAtStart = fence.writeToken;
    const stillMine = () => fence.readStillMine(generation, serial);
    setState({ hubLoading: true, hubError: null });
    try {
      const result = await client.request("evener/settings/transcriptDisplay/get", {});
      if (!stillMine()) return;
      const defaults = fromWireDefaults(result);
      if (defaults === undefined) throw new Error(MALFORMED_DEFAULTS_MESSAGE);
      // One transition per layout, so a host announcing effective changes
      // sees each layer move on its own. Both layers are one payload from
      // one hub, so whether this generation's confirmed state exists yet is
      // read ONCE: the desktop apply flips `loaded`, and without the pin the
      // mobile layer would then be fenced by the previous hub's revision.
      const authoritative = fence.awaitingFirstPayload;
      applyHubDefault("desktop", defaults.desktop, {}, authoritative);
      applyHubDefault(
        "mobile",
        defaults.mobile,
        { loaded: true, hubLoading: false, ...settledWrite(writeSerialAtStart) },
        authoritative,
      );
      fence.firstPayloadApplied();
      if (missedChangeNotification) {
        // A changed-notification was dropped while this generation had no
        // confirmed state and THIS get's response may predate it. One
        // follow-up fetch converges; the flag is cleared FIRST so it cannot
        // loop.
        missedChangeNotification = false;
        void refreshFor(generation);
      }
    } catch (error) {
      if (stillMine()) setState({ hubError: errorText(error), hubLoading: false });
    }
  }

  async function refreshHubDefaults(): Promise<void> {
    // A draft port that failed gets one more restore attempt per refresh. The
    // hub read waits only on a port that could not be READ FROM, so an edit
    // cannot compose against a confirmed payload with the draft unknown. An
    // unreadable RECORD is not that: the draft is simply absent, and holding
    // the section's defaults hostage to it would lock a user out of settings
    // they never edited.
    if (getState().storageUnavailable) {
      setState(restoreDraft(getState()));
      const restored = getState();
      if (restored.storageUnavailable && !restored.draftUnreadable) return;
    }
    if (fence.generation < 0) return;
    await refreshFor(fence.generation);
  }

  function detachHub(): void {
    // The confirmed defaults belong to the PREVIOUS hub. Until this client's
    // refresh lands they must not present as current or as a base for a
    // write. hubSupport is connection-sourced, not hub state, so it is left
    // to setSupport.
    retirePayload({ hub: {}, hubError: null, hubErrors: {}, ...clearPreviews() });
  }

  function layoutError(layout: ViewportClass, message: string | undefined): Partial<TranscriptDisplayStoreFields> {
    return { hubErrors: { ...getState().hubErrors, [layout]: message } };
  }

  async function patchHubDefault(
    layout: ViewportClass,
    input: TranscriptDisplayConfigV1,
  ): Promise<HubTranscriptDisplayDefault> {
    const state = getState();
    const generation = fence.generation;
    // The checkpointed editor's sibling gate. Both paths claim the same
    // layer token, so a direct write starting while a checkpointed one is
    // in flight - or while its outcome is still unknown - would take the
    // layer, fence that write's own reply out and leave the editor saving
    // with nothing left to settle it.
    if (!fence.liveHub(generation) || !state.loaded || state.hubLoading || state.saving || state.writeUncertain) {
      setState(layoutError(layout, UNAVAILABLE_MESSAGE));
      throw new Error(UNAVAILABLE_MESSAGE);
    }
    const config = normalizeConfig(input);
    const confirmed = confirmedFor(state.hub, layout);
    const token = claimLayoutWrite(layout);
    const stillMine = () => writeStillMine(generation, layout, token);
    previewBases.set(layout, { generation, revision: confirmed.revision });
    setState({ drafts: { ...state.drafts, [layout]: config }, ...layoutError(layout, undefined) });
    try {
      const result = await client.request("evener/settings/transcriptDisplay/patch", {
        layout,
        expectedRevision: confirmed.revision,
        config: toWireConfig(config),
      });
      if (!stillMine()) return getState().hub[layout] ?? confirmed;
      const canonical = decodePatchReply(result, layout, confirmed, config);
      // The direct write's host presents the live value, so a reply the store
      // has already moved past cannot be shown as this write's outcome.
      if (canonical.revision < (getState().hub[layout] ?? confirmed).revision) {
        // The layer moved on while this write was out. A malformed reply keeps
        // its preview because the hub MAY have applied the write and the
        // pre-write value would read as a refusal; here there is a confirmed,
        // newer value already on screen, so the preview would sit on top of it
        // claiming a change the hub has overtaken.
        setState(clearPreview(layout));
        throw new InvalidPatchResponseError(MALFORMED_PATCH_MESSAGE);
      }
      applyHubDefault(layout, canonical, {
        ...clearPreview(layout),
        hubError: null,
        ...layoutError(layout, undefined),
      });
      return canonical;
    } catch (error) {
      if (!stillMine()) {
        // A conflict's canonical current is a confirmed payload like any
        // other, so it lands only while this write is still its own. Past the
        // fence it belongs to a hub this store has left: applying it would
        // repopulate a retired payload and mark it confirmed, and the next
        // hub's refresh - a restart numbering from its own 1 - would then be
        // eaten by the stale guard.
        return getState().hub[layout] ?? confirmed;
      }
      const canonical = conflictCurrent(error, layout);
      if (canonical !== undefined) applyHubDefault(layout, canonical);
      // A malformed success keeps the preview: the hub may well have applied
      // the write, and dropping the preview would show the pre-write value
      // as if the write were refused. A refused or lost write drops it.
      const message = errorText(error);
      setState({
        // A malformed success keeps the preview (see above); the overtaken path
        // has already cleared its own.
        ...(error instanceof InvalidPatchResponseError ? {} : clearPreview(layout)),
        hubError: message,
        ...layoutError(layout, message),
      });
      throw error;
    }
  }

  /** The states in which touching the local proposal at all is unsafe: a
   * write is in flight or its outcome is unknown, the port cannot be reached,
   * or the store is finished. Nothing here is about the HUB - composing
   * against confirmed state is the editor's extra requirement, not this one. */
  function assertDiscardable(): void {
    const state = getState();
    // An unreadable record is the one storage failure discarding can FIX, so
    // it is not a reason to refuse: throwing the record away is exactly what
    // the user is asking for.
    if (fence.disposed || state.saving || state.writeUncertain || (state.storageUnavailable && !state.draftUnreadable))
      throw new Error(UNAVAILABLE_MESSAGE);
  }

  /** The draft editor's gate: a confirmed, supported, idle hub state with a
   * usable draft port. Returns the confirmed defaults the edit composes
   * against. An in-flight READ does not close it, unlike the direct write's
   * gate: a checkpointed write is durable before it leaves and its fence
   * discards the older read's reply, so waiting would only lose the intent. */
  function assertEditable(): HubDefaultsByLayout {
    const state = getState();
    if (
      fence.disposed ||
      state.saving ||
      state.storageUnavailable ||
      state.writeUncertain ||
      state.hubSupport !== "supported" ||
      !state.loaded
    )
      throw new Error(UNAVAILABLE_MESSAGE);
    return state.hub;
  }

  function persistDraft(input: Omit<TranscriptDraftCheckpoint, "id">): TranscriptDraftCheckpoint {
    try {
      const checkpoint = { ...input, id: drafts.createId() };
      drafts.save(checkpoint);
      return checkpoint;
    } catch {
      // The port's own failure, in the port's fixed words: a raw storage error
      // may carry a local path, so the host shows this and never the cause.
      setState({ storageUnavailable: true, draftError: DRAFT_SAVE_FAILED_MESSAGE });
      throw new Error(DRAFT_SAVE_FAILED_MESSAGE);
    }
  }

  function confirmedFor(hub: HubDefaultsByLayout, layout: ViewportClass): HubTranscriptDisplayDefault {
    return hub[layout] ?? shippedDefault(layout);
  }

  function editDraft(layout: ViewportClass, input: TranscriptDisplayConfigV1): void {
    const hub = assertEditable();
    const config = normalizeConfig(input);
    const existing = getState().draft;
    const revision = existing?.layout === layout ? existing.revision : confirmedFor(hub, layout).revision;
    persistDraft({ layout, baseRevision: revision, config, writeUncertain: false });
    const draft = { layout, revision, config };
    setState({ draft, draftConflict: staleDraft(draft, hub), draftError: null });
  }

  async function saveDraft(
    layoutInput?: ViewportClass,
    input?: TranscriptDisplayConfigV1,
  ): Promise<HubTranscriptDisplayDefault> {
    const hub = assertEditable();
    const existing = getState().draft;
    if (getState().draftConflict) throw new Error("Review the current transcript settings before saving your changes.");
    const layout = layoutInput ?? existing?.layout ?? "mobile";
    const confirmed = confirmedFor(hub, layout);
    const config = normalizeConfig(input ?? (existing?.layout === layout ? existing.config : confirmed.config));
    const revision = existing?.layout === layout ? existing.revision : confirmed.revision;
    // The durable intent must exist before the request can leave the device.
    const checkpoint = persistDraft({ layout, baseRevision: revision, config, writeUncertain: true });
    const token = claimLayoutWrite(layout);
    const generation = fence.generation;
    const stillMine = () => writeStillMine(generation, layout, token);
    setState({ saving: true, draft: { layout, revision, config }, draftError: null });
    let result: unknown;
    try {
      // The saving publish above may have disposed the store or retired the
      // payload (a host tearing down on the transition): the checkpoint stays
      // for the next instance to restore, and nothing leaves.
      if (!stillMine()) throw new Error("Transcript preference save was cancelled.");
      result = await client.request("evener/settings/transcriptDisplay/patch", {
        layout,
        expectedRevision: revision,
        config: toWireConfig(config),
      });
    } catch (error) {
      if (!stillMine()) {
        settleUnsettleableWrite(whyFenced(generation, layout, token));
        throw error;
      }
      const canonical = conflictCurrent(error, layout);
      if (canonical !== undefined) {
        // A revision conflict is not a lost reply: the hub REFUSED this write
        // and said what the current value is, so the outcome is known. The
        // canonical lands and the proposal stays for review against it - the
        // same rule the direct write follows, and the checkpoint is re-marked
        // settled rather than left claiming an unknown outcome.
        applyHubDefault(layout, canonical, { saving: false, writeUncertain: false, draftConflict: true });
        try {
          persistDraft({ ...checkpoint, writeUncertain: false });
        } catch {
          setState({ storageUnavailable: true, draftError: DRAFT_CLEANUP_FAILED_MESSAGE });
        }
        throw error;
      }
      // No reply: the write's outcome is unknown, and that fact is the state
      // (writeUncertain) rather than a message. The checkpoint already says so.
      setState({ saving: false, draftConflict: true, writeUncertain: true });
      throw error;
    }
    // The reply is back. One ordered sequence, nothing ahead of the fence:
    // (1) FENCE - a reply that is no longer ours is discarded without
    //     interpretation; retirePayload already published writeUncertain and
    //     the checkpoint on the port is the durable record of it.
    // (2) DECODE - the shared decodePatchReply, so this path cannot accept a
    //     reply the direct write would refuse. A malformed reply is
    //     hub-sourced: hubError, the outcome stays unknown, the checkpoint
    //     stays.
    // (3) APPLY, settling the in-flight flags on every path. A newer external
    //     revision that landed meanwhile keeps the proposal for review instead
    //     of reporting it applied - `>`, not "differs": an equal revision is
    //     this write's own broadcast arriving ahead of its reply.
    // (4) STORAGE LAST - released on a confirmed write, re-marked settled when
    //     the proposal stays for review; a cleanup failure keeps the draft in
    //     view with the port marked unavailable, and never turns a confirmed
    //     write back into an unknown outcome.
    if (!stillMine()) {
      settleUnsettleableWrite(whyFenced(generation, layout, token));
      return getState().hub[layout] ?? confirmed;
    }
    let value: HubTranscriptDisplayDefault;
    try {
      value = decodePatchReply(result, layout, confirmed, config);
    } catch (error) {
      setState({ saving: false, draftConflict: true, writeUncertain: true, hubError: MALFORMED_PATCH_MESSAGE });
      throw error;
    }
    const newerExternal = (getState().hub[layout]?.revision ?? -1) > value.revision;
    const settled: Partial<TranscriptDisplayStoreFields> = {
      saving: false,
      writeUncertain: false,
      draftConflict: newerExternal,
    };
    if (newerExternal) setState(settled);
    else applyHubDefault(layout, value, settled);
    let storageError: string | null = null;
    try {
      if (newerExternal) persistDraft({ ...checkpoint, writeUncertain: false });
      else drafts.removeIf(checkpoint);
    } catch {
      storageError = DRAFT_CLEANUP_FAILED_MESSAGE;
    }
    // This write took the layer, so any direct write's preview on it belongs to
    // a superseded write and must not outlive the value that replaced it -
    // whether this write's own value landed or a newer external one did.
    setState({
      draft: newerExternal || storageError !== null ? getState().draft : null,
      storageUnavailable: storageError !== null,
      draftError: storageError,
      ...clearPreview(layout),
    });
    return value;
  }

  /** Throwing a proposal away composes nothing and sends nothing, so it needs
   * no confirmed hub state - only that the proposal is not mid-flight and the
   * port is usable. Gating it on the editor's full contract would strand a
   * restored draft on a store whose hub read failed. */
  function discardDraft(): void {
    assertDiscardable();
    if (getState().draftUnreadable) {
      try {
        drafts.discardUnreadable();
      } catch {
        setState({ storageUnavailable: true });
        throw new Error(DRAFT_DISCARD_FAILED_MESSAGE);
      }
      // The removal above may have refused (the record it named is gone,
      // replaced by something else): re-read what is actually there now
      // rather than assume success, so a newer readable checkpoint surfaces
      // instead of staying reported as the same unreadable record.
      setState(restoreDraft(getState()));
      return;
    }
    try {
      const checkpoint = drafts.load();
      if (checkpoint) drafts.removeIf(checkpoint);
    } catch {
      setState({ storageUnavailable: true });
      throw new Error(DRAFT_DISCARD_FAILED_MESSAGE);
    }
    setState({
      draft: null,
      draftConflict: false,
      draftError: null,
      storageUnavailable: false,
      draftUnreadable: false,
    });
  }

  function rebaseDraft(reviewedRevision: number): void {
    const hub = assertEditable();
    const { draft, hubLoading } = getState();
    if (draft === null || hubLoading || confirmedFor(hub, draft.layout).revision !== reviewedRevision)
      throw new Error("Transcript settings changed again. Review the current values.");
    persistDraft({ layout: draft.layout, baseRevision: reviewedRevision, config: draft.config, writeUncertain: false });
    setState({ draft: { ...draft, revision: reviewedRevision }, draftConflict: false, draftError: null });
  }

  return {
    ...store,
    setSupport,
    beginReadyGeneration,
    endReadyGeneration,
    detachHub,
    reset() {
      endReadyGeneration();
      missedChangeNotification = false;
      previewBases.clear();
      setState(initialState());
    },
    dispose() {
      if (fence.disposed) return;
      endReadyGeneration();
      fence.dispose();
    },
  };
}
