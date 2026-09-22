// The transcript display hub-defaults store shared by the web and native
// settings surfaces. It keeps the hub's per-layout confirmed defaults behind a
// framework-free store and fences every request by the active ready generation.
//
// Two write paths share the PATCH: patchHubDefault is the live editor's direct
// write (an optimistic preview per layout while the request is out, dropped
// when it settles); saveDraft is the offline editor's checkpointed write (the
// intent is persisted through the draft port BEFORE the request leaves, and a
// lost reply leaves it `writeUncertain` until an authoritative read settles
// it). The `drafts` preview map and the `draft` checkpoint are distinct paths.

import { assertDraftDiscardable, discardCheckpointedDraft, persistCheckpointedDraft } from "./checkpointedDraftEditor";
import type { AppwireClient } from "./client";
import { canonicalJson, createDraftRepository, type DraftPort, UnreadableDraftError } from "./draftCheckpointPort";
import { errorText, WireError, wireRejectionPayload } from "./errors";
import { createFrameworkFreeStore, type FrameworkFreeStore } from "./frameworkFreeStore";
import { createReadyGenerationFence, type ReadyGenerationFence } from "./readyGenerationFence";
import {
  createSettingsHubGeneration,
  retireSettingsHubPayload,
  settleUnsettleableWrite,
} from "./settingsHubGeneration";
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

export type TranscriptDisplayClient = Pick<AppwireClient, "request" | "onNotification">;

export type TranscriptDisplaySupport = "unknown" | "supported" | "unsupported";

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

export type HubDefaultsByLayout = Partial<Record<ViewportClass, HubTranscriptDisplayDefault>>;

export interface TranscriptDisplayChange {
  layout: ViewportClass;
  revision: number;
  config: TranscriptDisplayConfigV1;
}

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
 * method may throw; the store maps a throw to `storageUnavailable`. Field for
 * field the shared checkpointed-draft port, over this store's own checkpoint
 * shape. */
export type TranscriptDraftStorage = DraftPort<TranscriptDraftCheckpoint>;

/** The offline editor's proposal: one layout's configuration and the
 * confirmed revision it was composed against. `generation` is the ready
 * generation it was last confirmed valid under - null for a draft restored
 * before any generation has begun (nothing to compare yet) until the first
 * authoritative payload stamps it. Hub revision numbering restarts on a hub
 * replacement, so a draft composed against generation N's revision 3 must not
 * read as current just because generation N+1 also reports revision 3. */
export interface TranscriptDraft {
  layout: ViewportClass;
  revision: number;
  config: TranscriptDisplayConfigV1;
  generation: number | null;
}

export interface TranscriptDisplayStoreFields {
  hubSupport: TranscriptDisplaySupport;
  hubLoading: boolean;
  hubError: string | null;
  hubErrors: Partial<Record<ViewportClass, string>>;
  /** Confirmed defaults keep presenting across a transient disconnect. */
  hub: HubDefaultsByLayout;
  /** Optimistic direct-write previews, retained only across transient disconnects. */
  drafts: Partial<Record<ViewportClass, TranscriptDisplayConfigV1>>;
  /** True only after the current ready generation's read has completed. */
  loaded: boolean;
  /** A checkpointed write is in flight; both write paths gate on it. */
  saving: boolean;
  /** A checkpointed write left without a confirmed outcome; edits stay
   * blocked until an authoritative read settles it. */
  writeUncertain: boolean;
  /** The offline editor's proposal. Restored from the draft port at creation;
   * distinct from the direct write's `drafts` preview map. */
  draft: TranscriptDraft | null;
  /** The draft port threw; edits stay blocked until refreshHubDefaults can
   * restore the checkpoint again. */
  storageUnavailable: boolean;
  /** The port answered but what it held could not be read. The RECORD is the
   * problem, not the port: the section still loads, and discarding is allowed
   * and is what clears it. */
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
  refreshHubDefaults(): Promise<void>;
  patchHubDefault(layout: ViewportClass, config: TranscriptDisplayConfigV1): Promise<HubTranscriptDisplayDefault>;
  applyHubChange(change: TranscriptDisplayChange): void;
  /** Replaces the draft with `config` for `layout`, checkpointed through the
   * draft port before anything leaves. Throws while the store is not editable
   * (see assertEditable). */
  editDraft(layout: ViewportClass, config: TranscriptDisplayConfigV1): void;
  /** PATCHes `config` (default: the draft, else the layout's confirmed value)
   * with the checkpoint's base revision as expectedRevision, checkpointing the
   * intent before the request leaves. Rejects on a stale draft (rebaseDraft
   * first), while a save is in flight, and on any failed or unconfirmed
   * write. Without a draft, `layout` names the layer being saved. */
  saveDraft(layout?: ViewportClass, config?: TranscriptDisplayConfigV1): Promise<HubTranscriptDisplayDefault>;
  /** Drops the draft and its checkpoint, readable or not. */
  discardDraft(): void;
  /** Moves the draft's base onto the confirmed revision the user reviewed;
   * throws when the hub has moved again since. */
  rebaseDraft(reviewedRevision: number): void;
}

export type TranscriptDisplayStoreState = TranscriptDisplayStoreFields & TranscriptDisplayStoreActions;

export interface TranscriptDisplayStore extends FrameworkFreeStore<TranscriptDisplayStoreState> {
  setSupport(support: TranscriptDisplaySupport): void;
  beginReadyGeneration(): void;
  endReadyGeneration(): void;
  detachHub(): void;
  reset(): void;
  dispose(): void;
}

export interface TranscriptDisplayStoreDeps {
  client: TranscriptDisplayClient;
  /** The draft port. Without one the draft editor keeps its state in memory
   * and the proposal does not survive the instance. */
  drafts?: TranscriptDraftStorage;
}

function initialState(): TranscriptDisplayStoreFields {
  return {
    hubSupport: "unknown",
    hubLoading: false,
    hubError: null,
    hubErrors: {},
    hub: {},
    drafts: {},
    loaded: false,
    saving: false,
    writeUncertain: false,
    draft: null,
    storageUnavailable: false,
    draftUnreadable: false,
    draftConflict: false,
    draftError: null,
  };
}

const MALFORMED_DEFAULTS_MESSAGE = "Hub returned malformed transcript display defaults";
const UNAVAILABLE_MESSAGE = "Hub transcript display settings are unavailable.";
const MALFORMED_PATCH_MESSAGE = "Hub returned malformed transcript display PATCH response";
/** The draft port's fixed failure copy: a raw storage error may carry a local
 * path, so it never reaches state verbatim. */
const DRAFT_SAVE_FAILED_MESSAGE = "Could not save the transcript draft locally.";
const DRAFT_DISCARD_FAILED_MESSAGE = "Could not discard the transcript draft locally.";
const DRAFT_RESTORE_FAILED_MESSAGE = "Could not restore the saved transcript draft. Check current settings to retry.";
const DRAFT_CLEANUP_FAILED_MESSAGE =
  "The hub confirmed this save, but the local draft could not be updated. Check current settings to retry.";
const DRAFT_REVIEW_BEFORE_SAVE_MESSAGE = "Review the current transcript settings before saving your changes.";
const DRAFT_REVIEW_AGAIN_MESSAGE = "Transcript display settings changed again. Review the current values.";
const SAVE_CANCELLED_MESSAGE = "Transcript preference save was cancelled.";

class InvalidPatchResponseError extends Error {}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isRevision(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

interface HubDefaultCalculation {
  accepted: boolean;
  contradictsPreview: boolean;
}

interface HubDefaultInputs {
  readonly hub: TranscriptDisplayStoreState["hub"];
  readonly drafts: TranscriptDisplayStoreState["drafts"];
  readonly previewBases: ReadonlyMap<ViewportClass, { generation: number; revision: number; fingerprint: string }>;
  readonly fence: { readonly awaitingFirstPayload: boolean; readonly generation: number };
}

function calculateHubDefault(
  layout: ViewportClass,
  incoming: HubTranscriptDisplayDefault,
  inputs: HubDefaultInputs,
): HubDefaultCalculation {
  const previous = inputs.hub[layout];
  const previewBase = inputs.previewBases.get(layout);
  const accepted = inputs.fence.awaitingFirstPayload || previous === undefined || incoming.revision > previous.revision;
  const contradictsPreview =
    accepted &&
    previewBase !== undefined &&
    inputs.drafts[layout] !== undefined &&
    (previewBase.generation !== inputs.fence.generation || incoming.revision > previewBase.revision) &&
    configFingerprint(incoming.config) !== previewBase.fingerprint;
  return { accepted, contradictsPreview };
}

function fromWirePatchResponse(value: unknown, layout: ViewportClass): HubTranscriptDisplayDefault | undefined {
  if (!isRecord(value) || value.layout !== layout) return undefined;
  return fromWireDefault(value);
}

// The conflict and post-apply payloads (appwire/transcript_display.go's
// TranscriptDisplayConflictData and TranscriptDisplayPostApplyData) name the
// layout their canonical value belongs to; a payload naming another layout
// is not this write's answer. The discriminator is the payload's info
// string, never the code - siblings share the code (errors.ts).
function canonicalErrorPayload(
  error: unknown,
  info: string,
  field: string,
  layout: ViewportClass,
): HubTranscriptDisplayDefault | undefined {
  if (!(error instanceof WireError) || !isRecord(error.data) || error.data.layout !== layout) return undefined;
  return wireRejectionPayload(error, info, field, fromWireDefault);
}

function conflictCurrent(error: unknown, layout: ViewportClass): HubTranscriptDisplayDefault | undefined {
  return canonicalErrorPayload(error, "conflict", "current", layout);
}

function postApplyDefault(error: unknown, layout: ViewportClass): HubTranscriptDisplayDefault | undefined {
  return canonicalErrorPayload(error, "transcriptDisplayPostApply", "applied", layout);
}

function decodePatchReply(
  result: unknown,
  layout: ViewportClass,
  confirmed: HubTranscriptDisplayDefault,
  requestedFingerprint: string,
): HubTranscriptDisplayDefault {
  const canonical = fromWirePatchResponse(result, layout);
  const isThisWrite =
    canonical !== undefined &&
    configFingerprint(canonical.config) === requestedFingerprint &&
    (canonical.revision === confirmed.revision + 1 ||
      (canonical.revision === confirmed.revision && requestedFingerprint === configFingerprint(confirmed.config)));
  if (canonical === undefined || !isThisWrite) throw new InvalidPatchResponseError(MALFORMED_PATCH_MESSAGE);
  return canonical;
}

export function fromWireChange(value: unknown): TranscriptDisplayChange | undefined {
  if (!isRecord(value) || !isViewportClass(value.layout) || !isRevision(value.revision)) return undefined;
  const config = fromWireConfig(value.config);
  return config === undefined ? undefined : { layout: value.layout, revision: value.revision, config };
}

/** The draft port a store without one runs on: the proposal is held in this
 * closure - in memory, per instance, never touching a real backing store.
 * Retention keeps the draft machinery coherent within the instance: a reset
 * re-reads what the editor wrote instead of wiping the draft and any
 * unresolved write's uncertainty. Only this one repository instance ever
 * touches the value, so its compare-and-swaps can never actually lose a
 * race - they still compare against what the closure holds, the same
 * contract a byte-aware port runs, so a refusal continues to read as
 * "someone else replaced it" only when something really did. */
function memoryDraftStorage(): TranscriptDraftStorage {
  let stored: unknown = null;
  return {
    createId: () => "memory",
    load: () => stored,
    save(checkpoint) {
      stored = checkpoint;
    },
    insertIfAbsent(checkpoint) {
      if (stored !== null) return false;
      stored = checkpoint;
      return true;
    },
    removeIf(identity) {
      if (stored === null || canonicalJson(identity) !== canonicalJson(stored)) return false;
      stored = null;
      return true;
    },
    replaceIf(expected, next) {
      if (stored === null || canonicalJson(expected) !== canonicalJson(stored)) return false;
      stored = next;
      return true;
    },
  };
}

function invalidDraft(): never {
  throw new UnreadableDraftError("Invalid transcript display draft.");
}

/** The strict checkpoint check the draft editor runs on a restored
 * checkpoint - including a layout, the one field a stored record without one
 * (a shape the native host wrote before layouts were recorded) can never be
 * guessed at. Throws on anything else. */
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
 * has confirmed a different one. A generation change is a hub replacement
 * (reconnect to a different hub, or the same hub restarted): the hub's own
 * revision numbering may restart too, so a draft carrying a real (non-null)
 * generation that no longer matches is stale regardless of what the new
 * generation's revision says. */
function staleDraft(draft: TranscriptDraft | null, hub: HubDefaultsByLayout, currentGeneration: number): boolean {
  if (draft === null) return false;
  if (draft.generation !== null && draft.generation !== currentGeneration) return true;
  const confirmed = hub[draft.layout];
  return confirmed !== undefined && confirmed.revision !== draft.revision;
}

/** Whether an authoritative read of `hub` confirms the draft: the layout's
 * confirmed revision is the draft's own base revision, so the read proves the
 * draft composed against current state. A read that lands nothing new has
 * still earned stamping the live generation onto a null-generation draft when
 * this holds - without it, a draft an adoption restored mid-generation could
 * sit through unchanged reads unstamped, and the next generation's first
 * payload would stamp the NEW generation onto a coincidentally equal
 * revision. */
function confirmsDraft(draft: TranscriptDraft | null, hub: HubDefaultsByLayout): boolean {
  if (draft === null) return false;
  const confirmed = hub[draft.layout];
  return confirmed !== undefined && confirmed.revision === draft.revision;
}

export function createTranscriptDisplayStore(deps: TranscriptDisplayStoreDeps): TranscriptDisplayStore {
  const { client } = deps;
  const draftRepository = createDraftRepository(deps.drafts ?? memoryDraftStorage(), draftCheckpoint);
  const fence: ReadyGenerationFence = createReadyGenerationFence(isSupported);
  let missedChangeNotification = false;
  let successfulHubReads = 0;
  const patchTokens = new Map<ViewportClass, number>();
  const previewBases = new Map<ViewportClass, { generation: number; revision: number; fingerprint: string }>();
  const strandedPreviews = new Set<ViewportClass>();
  let reconciliationRefresh: { generation: number; promise: Promise<void> } | undefined;

  const store = createFrameworkFreeStore<TranscriptDisplayStoreState>(() => ({
    ...initialState(),
    // The draft restore is part of the initial state so a host that builds
    // the store synchronously sees the persisted proposal on its first read,
    // before any refresh - and so the repository classifies whatever the
    // port holds (the identity a later discard or settle acts on) before
    // any caller can reach them.
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

  function isSupported(): boolean {
    return getState().hubSupport === "supported";
  }

  /** The fields a restored checkpoint (or its absence, or a failed restore)
   * sets. `confirmed` is passed in because the creation-time restore runs
   * before there is any state to read; `generation` is only supplied by the
   * identity-aware recovery path (recoverDraftPort) - an ordinary restore always
   * takes null, never the current generation: a replacement record knows
   * nothing about this store's generations, and stamping one would launder
   * an unprovable claim into the staleness check. The first authoritative
   * payload to land under a live generation stamps it (see stampedDraft). */
  function restoreDraft(
    confirmed: { loaded: boolean; hub: HubDefaultsByLayout },
    generation: number | null = null,
    checkpointOverride?: TranscriptDraftCheckpoint | null,
  ): Partial<TranscriptDisplayStoreFields> {
    try {
      const checkpoint = checkpointOverride === undefined ? draftRepository.load() : checkpointOverride;
      const draft =
        checkpoint === null
          ? null
          : { layout: checkpoint.layout, revision: checkpoint.baseRevision, config: checkpoint.config, generation };
      return {
        draft,
        writeUncertain: checkpoint?.writeUncertain ?? false,
        storageUnavailable: false,
        draftUnreadable: false,
        draftError: null,
        draftConflict: confirmed.loaded && staleDraft(draft, confirmed.hub, fence.generation),
      };
    } catch (error) {
      // An UnreadableDraftError names the RECORD as the problem, not the
      // port: whatever draft/writeUncertain/draftConflict described before
      // this call described a record that no longer exists to describe, so
      // they clear too - otherwise a write left uncertain by an earlier
      // attempt stays that way forever, and assertDraftDiscardable's
      // unconditional writeUncertain check refuses the one recovery (discard)
      // an unreadable record is supposed to allow. A genuine port failure
      // (the read itself failed, not what it read) says nothing about whether
      // the in-memory state is still accurate, so it is left alone - and
      // draftUnreadable is omitted rather than reset to false, which would
      // silently hide the one recovery an earlier unreadable classification
      // still allows.
      const unreadable = error instanceof UnreadableDraftError;
      return {
        storageUnavailable: true,
        draftError: DRAFT_RESTORE_FAILED_MESSAGE,
        ...(unreadable ? { draftUnreadable: true, draft: null, writeUncertain: false, draftConflict: false } : {}),
      };
    }
  }

  /** The generation to stamp a freshly composed or reconciled draft with -
   * null before any ready generation has begun (nothing to compare a later
   * one against yet). */
  function currentGeneration(): number | null {
    return fence.generation >= 0 ? fence.generation : null;
  }

  /** A draft restored before any ready generation existed keeps generation
   * null until the first authoritative payload to land under a live
   * generation stamps it - in the same publication that judges its
   * staleness, and before that judgment runs. Left null forever, the
   * generation check would never run for it, and a later hub replacement
   * reporting the identical revision by coincidence would read it as
   * current. Guarded on a LIVE generation: stamping without one would lock
   * in a generation identity nothing ever confirmed. */
  function stampedDraft(draft: TranscriptDraft | null): TranscriptDraft | null {
    return draft !== null && draft.generation === null && fence.generation >= 0
      ? { ...draft, generation: fence.generation }
      : draft;
  }

  function retirePayload(extra: Partial<TranscriptDisplayStoreFields> = {}): void {
    reconciliationRefresh = undefined;
    for (const layout of LAYOUTS) patchTokens.set(layout, fence.claimWrite());
    retireSettingsHubPayload(fence, getState, setState, extra);
  }

  function layoutError(layout: ViewportClass, message: string | undefined): Partial<TranscriptDisplayStoreFields> {
    // No error means the key is absent, never present with an undefined
    // value: both accepted-apply paths clear the same way, and key-presence
    // readers never see a cleared layout.
    const hubErrors = { ...getState().hubErrors };
    if (message === undefined) delete hubErrors[layout];
    else hubErrors[layout] = message;
    return { hubErrors };
  }

  // A preview's base and its draft clear together, whether the caller
  // clears against live state or against the snapshot an atomic publication
  // is about to build on.
  function dropPreview(layout: ViewportClass, drafts: TranscriptDisplayStoreState["drafts"]): void {
    previewBases.delete(layout);
    delete drafts[layout];
  }

  function clearPreview(layout: ViewportClass): Partial<TranscriptDisplayStoreFields> {
    const drafts = { ...getState().drafts };
    dropPreview(layout, drafts);
    return { drafts };
  }

  // A settled write clears its preview and its error slots together.
  function writeSettled(layout: ViewportClass): Partial<TranscriptDisplayStoreFields> {
    return { ...clearPreview(layout), hubError: null, ...layoutError(layout, undefined) };
  }

  function clearPreviews(): Partial<TranscriptDisplayStoreFields> {
    previewBases.clear();
    strandedPreviews.clear();
    return { drafts: {} };
  }

  /** A confirmed default is monotonic within a generation. The first
   * authoritative payload of a new generation may restart revision numbering. */
  function applyHubDefault(
    layout: ViewportClass,
    value: HubTranscriptDisplayDefault,
    extra:
      | Partial<TranscriptDisplayStoreFields>
      | ((hub: HubDefaultsByLayout) => Partial<TranscriptDisplayStoreFields>) = {},
  ): boolean {
    const state = getState();
    const calculation = calculateHubDefault(layout, value, {
      hub: state.hub,
      drafts: state.drafts,
      previewBases,
      fence,
    });
    // A callback extra runs BEFORE the publication, handed the hub that
    // publication is about to carry (the unchanged one on a rejected
    // payload), so a caller can settle storage or judge an adoption against
    // the final state before the unblocked state reaches subscribers.
    const settledExtra = (hub: HubDefaultsByLayout): Partial<TranscriptDisplayStoreFields> =>
      typeof extra === "function" ? extra(hub) : extra;
    if (!calculation.accepted) {
      const settled = settledExtra(state.hub);
      if (Object.keys(settled).length > 0) setState(settled);
      return false;
    }
    const draft = stampedDraft(state.draft);
    const hub = { ...state.hub, [layout]: value };
    // An accepted canonical payload supersedes whatever stale write error
    // the layout carried; rejected payloads keep theirs. Caller-provided
    // extras still win the merge below.
    setState({
      hub,
      ...(calculation.contradictsPreview ? clearPreview(layout) : {}),
      draft,
      draftConflict: staleDraft(draft, hub, fence.generation),
      ...layoutError(layout, undefined),
      ...settledExtra(hub),
    });
    return true;
  }

  function applyHubDefaults(
    defaults: Readonly<Record<ViewportClass, HubTranscriptDisplayDefault>>,
    settle?: (hub: HubDefaultsByLayout) => Partial<TranscriptDisplayStoreFields>,
  ): void {
    const state = getState();
    const hub = { ...state.hub };
    const drafts = { ...state.drafts };
    const hubErrors = { ...state.hubErrors };
    let anyAccepted = false;
    for (const layout of LAYOUTS) {
      const calculation = calculateHubDefault(layout, defaults[layout], {
        hub,
        drafts,
        previewBases,
        fence,
      });
      if (!calculation.accepted) continue;
      anyAccepted = true;
      hub[layout] = defaults[layout];
      delete hubErrors[layout];
      if (calculation.contradictsPreview) dropPreview(layout, drafts);
    }
    if (fence.awaitingFirstPayload) fence.firstPayloadApplied();
    // A preview the support flap stranded settled before this read: the
    // write's continuation is dead, and an unchanged revision proves the
    // write never landed, so the read ends that preview rather than
    // leaving it for the host.
    for (const layout of strandedPreviews) dropPreview(layout, drafts);
    strandedPreviews.clear();
    // The stamp runs for a read that landed at least one payload (a payload
    // rejected by the stale guard confirmed nothing new) or one that
    // CONFIRMS the draft - see confirmsDraft. `settle`
    // (the uncertain-write settlement below) is spread LAST so an adoption
    // it returns overrides this publish's own draft fields - the restore
    // already judged the replacement against this same final hub.
    const draft = anyAccepted || confirmsDraft(state.draft, hub) ? stampedDraft(state.draft) : state.draft;
    const settled = settle?.(hub) ?? {};
    setState({
      hub,
      drafts,
      hubErrors,
      draft,
      draftConflict: staleDraft(draft, hub, fence.generation),
      loaded: true,
      hubLoading: false,
      ...settled,
    });
  }

  const { beginReadyGeneration: beginReadyGenerationCore, endReadyGeneration } = createSettingsHubGeneration({
    fence,
    wireNotifications: (generation) => {
      missedChangeNotification = false;
      return client.onNotification((notification) => {
        if (!fence.isCurrent(generation)) return;
        onNotification(notification, generation);
      });
    },
    retirePayload,
  });

  function beginReadyGeneration(): void {
    const replacingGeneration = fence.generation >= 0;
    beginReadyGenerationCore();
    if (replacingGeneration) retirePayload();
  }

  function detachHub(): void {
    retirePayload({ hub: {}, hubError: null, hubErrors: {}, ...clearPreviews() });
  }

  function setSupport(support: TranscriptDisplaySupport): void {
    const state = getState();
    if (support === "supported") {
      if (state.hubSupport === support) return;
      if (state.hubSupport === "unsupported" && fence.generation >= 0) beginReadyGeneration();
      setState({ hubSupport: support });
      if (fence.generation >= 0) void refreshFor(fence.generation);
      return;
    }
    if (support === "unsupported") {
      if (state.hubSupport === support) return;
      retirePayload({ hubSupport: support, hubError: null, hubErrors: {}, hub: {}, ...clearPreviews() });
      return;
    }
    if (state.hubSupport !== support || state.hubLoading || state.hubError !== null) {
      setState({ hubSupport: support, hubLoading: false, hubError: null });
    }
  }

  function changeArrivedBeforeConfirmation(): boolean {
    if (fence.generation < 0 || getState().loaded) return false;
    missedChangeNotification = true;
    return true;
  }

  function externalPayloadLanded(applied: boolean): void {
    if (applied && getState().hubError !== null) setState({ hubError: null });
  }

  /** What an authoritative read settles for the draft editor: an uncertain
   * write's outcome is now whatever the hub confirmed, so the checkpoint is
   * re-marked and edits unblock. A read that started before the write left,
   * or landed while one is in flight, says nothing about that write and
   * settles nothing. `savingAtReadStart` covers the case the token check
   * misses: a write already in flight when this read began can fail (clearing
   * `saving`, leaving writeUncertain) before this read's reply lands, with no
   * newer write ever claiming a token - by reply time `saving` reads false
   * and the token still matches, but a read whose own snapshot predates that
   * write's outcome must not settle it. `draftAtReadStart` covers the case
   * neither check sees: an adoption that replaced the draft mid-read (a
   * failed edit adopting another window's replacement) claims no token and
   * holds no `saving`, yet its uncertainty left for the hub after this
   * read's snapshot - the read says nothing about THAT write's outcome
   * either, and only a later read that postdates the adoption may settle
   * it. */
  function settledWrite(
    writeSerialAtStart: number,
    savingAtReadStart: boolean,
    draftAtReadStart: TranscriptDraft | null,
    finalHub: HubDefaultsByLayout,
  ): Partial<TranscriptDisplayStoreFields> {
    const { draft, writeUncertain, saving } = getState();
    if (writeSerialAtStart !== fence.writeToken || saving || savingAtReadStart) return {};
    if (draft !== null && writeUncertain) {
      if (draft !== draftAtReadStart) return {};
      let replaced: boolean;
      try {
        // A fresh id: settledWrite has no checkpoint reference to reuse one
        // from (only the in-memory draft, which carries no id).
        replaced = draftRepository.replaceClassified({
          id: draftRepository.createId(),
          layout: draft.layout,
          baseRevision: draft.revision,
          config: draft.config,
          writeUncertain: false,
        });
      } catch {
        return { storageUnavailable: true, draftError: DRAFT_SAVE_FAILED_MESSAGE };
      }
      if (!replaced) {
        // The checkpoint this write was settling is gone, replaced by another
        // window's edit while the outcome was unknown: adopt whatever is
        // actually on disk now, judged against the FINAL hub state this
        // publication is about to carry, never overwrite it with the stale
        // now-settled checkpoint.
        return restoreDraft({ loaded: true, hub: finalHub });
      }
    }
    return { writeUncertain: false };
  }

  /** Publishes the end of a save whose reply can never be settled by anything
   * else, with draftConflict riding the shared helper's `extra` - the same
   * posture every other unknown-outcome settle takes: the proposal needs
   * review, not just a retry. */
  function settleLostHubWrite(generation: number, layout: ViewportClass, token: number): void {
    settleUnsettleableWrite(fence, generation, patchTokens.get(layout) === token, getState, setState, {
      draftConflict: true,
    });
  }

  function onNotification(notification: AnyNotification, generation: number): void {
    if (notification.method !== "evener/settings/transcriptDisplay/changed") return;
    if (!isSupported()) return;
    if (changeArrivedBeforeConfirmation()) return;
    const change = fromWireChange(notification.params);
    if (change === undefined) {
      void refreshFor(generation);
      return;
    }
    externalPayloadLanded(applyHubDefault(change.layout, { revision: change.revision, config: change.config }));
  }

  function applyHubChange(change: TranscriptDisplayChange): void {
    if (!isViewportClass(change.layout) || !isRevision(change.revision)) return;
    if (!fence.liveHub(fence.generation)) return;
    if (changeArrivedBeforeConfirmation()) return;
    let config: TranscriptDisplayConfigV1;
    try {
      config = normalizeConfig(change.config);
    } catch {
      return;
    }
    externalPayloadLanded(applyHubDefault(change.layout, { revision: change.revision, config }));
  }

  /** A draft port that failed gets one more restore attempt; reports whether
   * the hub read may proceed. The read waits only on a port that could not
   * be READ FROM, so an edit cannot compose against a confirmed payload with
   * the draft unknown. An unreadable RECORD is not that: the draft is simply
   * absent, and holding the section's defaults hostage to it would lock a
   * user out of settings they never edited. A plain port failure is that,
   * even when an earlier unreadable classification survives it (discard
   * must keep naming the record it classified): the record may since have
   * been replaced by anything, another window's uncertain write included,
   * so only a load that succeeds - or one that freshly classifies whatever
   * is stored as unreadable - reopens the store. */
  function recoverDraftPort(): boolean {
    if (!getState().storageUnavailable) return true;
    try {
      const { checkpoint, sameIdentity } = draftRepository.reload();
      // The re-read launders no stale generation: only the same classified
      // checkpoint identity carries the in-memory stamp forward, and a
      // replacement with equal fields is still a new record taking the
      // ordinary null-generation restore path.
      setState(restoreDraft(getState(), sameIdentity ? (getState().draft?.generation ?? null) : null, checkpoint));
    } catch (error) {
      const unreadable = error instanceof UnreadableDraftError;
      setState({
        storageUnavailable: true,
        draftError: DRAFT_RESTORE_FAILED_MESSAGE,
        ...(unreadable ? { draftUnreadable: true, draft: null, writeUncertain: false, draftConflict: false } : {}),
      });
      return unreadable;
    }
    return !getState().storageUnavailable || getState().draftUnreadable;
  }

  async function refreshFor(generation: number): Promise<void> {
    if (!fence.liveHub(generation)) return;
    // Automatic refreshes - support arriving, a malformed change
    // notification - wait on the same recovery gate an explicit one does:
    // landing `loaded` with the checkpoint, possibly an uncertain saved
    // write, still unread on the port would open the direct-write path with
    // the draft unknown. recoverDraftPort no-ops when an explicit
    // refreshHubDefaults already reloaded.
    if (!recoverDraftPort()) return;
    const serial = fence.claimRead();
    const writeSerialAtStart = fence.writeToken;
    const savingAtReadStart = getState().saving;
    const draftAtReadStart = getState().draft;
    const stillMine = () => fence.readStillMine(generation, serial);
    setState({ hubLoading: true, hubError: null });
    if (!stillMine()) return;
    try {
      const result = await client.request("evener/settings/transcriptDisplay/get", {});
      if (!stillMine()) return;
      const defaults = fromWireDefaults(result);
      if (defaults === undefined) throw new Error(MALFORMED_DEFAULTS_MESSAGE);
      applyHubDefaults(defaults, (hub) => settledWrite(writeSerialAtStart, savingAtReadStart, draftAtReadStart, hub));
      if (!stillMine()) return;
      successfulHubReads += 1;
      if (missedChangeNotification) {
        missedChangeNotification = false;
        void refreshFor(generation);
      }
    } catch (error) {
      if (stillMine()) setState({ hubError: errorText(error), hubLoading: false });
    }
  }

  async function refreshHubDefaults(): Promise<void> {
    // One more restore attempt per explicit refresh, even before any ready
    // generation exists to carry the hub read.
    if (!recoverDraftPort()) return;
    if (fence.generation < 0) return;
    await refreshFor(fence.generation);
  }

  async function reconcileHubDefaults(): Promise<void> {
    const generation = fence.generation;
    if (reconciliationRefresh?.generation === generation) return reconciliationRefresh.promise;
    const refresh = refreshHubDefaults();
    const reconciliation = { generation, promise: refresh };
    reconciliationRefresh = reconciliation;
    try {
      await refresh;
    } finally {
      if (reconciliationRefresh === reconciliation) reconciliationRefresh = undefined;
    }
  }

  async function patchHubDefault(
    layout: ViewportClass,
    input: TranscriptDisplayConfigV1,
  ): Promise<HubTranscriptDisplayDefault> {
    const generation = fence.generation;
    let state = getState();
    // A direct write composes against the same confirmed hub an edit does,
    // so it waits on the same draft-port recovery: a checkpoint this store
    // cannot read may be another window's uncertain write, and sending past
    // it would change settings that unresolved outcome is holding still.
    // The flags alone cannot say whether the port fails NOW - an earlier
    // unreadable-record classification survives a plain port failure for
    // discard to still name, so draftUnreadable alone is not permission -
    // so a marked-unavailable store gets one recovery attempt here. The
    // state re-reads after: a recovery can restore a draft this write must
    // respect.
    if (state.storageUnavailable && !recoverDraftPort()) {
      setState(layoutError(layout, UNAVAILABLE_MESSAGE));
      throw new Error(UNAVAILABLE_MESSAGE);
    }
    state = getState();
    if (!fence.liveHub(generation) || !state.loaded || state.hubLoading || state.saving || state.writeUncertain) {
      setState(layoutError(layout, UNAVAILABLE_MESSAGE));
      throw new Error(UNAVAILABLE_MESSAGE);
    }
    const config = normalizeConfig(input);
    const requestedFingerprint = configFingerprint(config);
    const confirmed = state.hub[layout] ?? shippedDefault(layout);
    const token = fence.claimWrite();
    patchTokens.set(layout, token);
    const stillMine = () => fence.liveHub(generation) && patchTokens.get(layout) === token;
    const retained = () => getState().hub[layout] ?? confirmed;
    // A support flap (generation unchanged, hub not live) can kill a
    // continuation that still owns the layout's write token without
    // retiring the payload, so the preview's write settled unseen: mark it
    // for the next authoritative read, which clears it even at an unchanged
    // revision - that read proves the write never landed. A token loss
    // strands nothing (a newer write owns the preview now), and a
    // generation change's retirement leaves the preview to the
    // stranded-preview rules.
    const strandedByFlap = () =>
      fence.isCurrent(generation) && !fence.liveHub(generation) && patchTokens.get(layout) === token;
    // A newer write on this layout owns the preview from here on, so a
    // support flap stranding an older write must not reach this one.
    strandedPreviews.delete(layout);
    previewBases.set(layout, { generation, revision: confirmed.revision, fingerprint: requestedFingerprint });
    setState({ drafts: { ...state.drafts, [layout]: config }, ...layoutError(layout, undefined) });
    // A synchronous subscriber can drop hub support while that publication
    // runs, killing the continuation before the request is even sent.
    if (!stillMine()) {
      if (strandedByFlap()) strandedPreviews.add(layout);
      return retained();
    }
    try {
      const result = await client.request("evener/settings/transcriptDisplay/patch", {
        layout,
        expectedRevision: confirmed.revision,
        config: toWireConfig(config),
      });
      if (!stillMine()) return retained();
      const canonical = decodePatchReply(result, layout, confirmed, requestedFingerprint);
      const current = getState().hub[layout] ?? confirmed;
      if (canonical.revision < current.revision) {
        setState(writeSettled(layout));
        return current;
      }
      applyHubDefault(layout, canonical, writeSettled(layout));
      return canonical;
    } catch (error) {
      if (!stillMine()) {
        if (strandedByFlap()) strandedPreviews.add(layout);
        return retained();
      }
      const applied = postApplyDefault(error, layout);
      if (applied !== undefined) {
        applyHubDefault(layout, applied, writeSettled(layout));
        return getState().hub[layout] ?? applied;
      }
      if (error instanceof WireError && error.evenerErrorInfo === "internal" && stillMine()) {
        for (let attempt = 0; attempt < 2; attempt++) {
          const readsBefore = successfulHubReads;
          await reconcileHubDefaults();
          if (!stillMine()) {
            if (strandedByFlap()) strandedPreviews.add(layout);
            return retained();
          }
          const reconciled = getState().hub[layout];
          if (
            successfulHubReads > readsBefore &&
            reconciled !== undefined &&
            reconciled.revision > confirmed.revision &&
            configFingerprint(reconciled.config) === requestedFingerprint
          ) {
            setState(writeSettled(layout));
            return reconciled;
          }
        }
      }
      const canonical = conflictCurrent(error, layout);
      if (canonical !== undefined) {
        applyHubDefault(layout, canonical);
        if (!stillMine()) return getState().hub[layout] ?? canonical;
      }
      const message = errorText(error);
      setState({
        ...clearPreview(layout),
        hubError: message,
        ...layoutError(layout, message),
      });
      throw error;
    }
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

  function confirmedFor(hub: HubDefaultsByLayout, layout: ViewportClass): HubTranscriptDisplayDefault {
    return hub[layout] ?? shippedDefault(layout);
  }

  /** Persists a freshly composed checkpoint - see persistCheckpointedDraft:
   * the mint-id/save/catch shape and the save-refusal adoption are shared,
   * with this store's own restoreDraft and fixed message texts. */
  function persistDraft(input: Omit<TranscriptDraftCheckpoint, "id">): TranscriptDraftCheckpoint {
    return persistCheckpointedDraft(
      draftRepository,
      input,
      getState,
      setState,
      restoreDraft,
      DRAFT_SAVE_FAILED_MESSAGE,
      DRAFT_REVIEW_AGAIN_MESSAGE,
    );
  }

  function editDraft(layout: ViewportClass, input: TranscriptDisplayConfigV1): void {
    const hub = assertEditable();
    const config = normalizeConfig(input);
    const existing = getState().draft;
    // One draft field can propose either layout: an edit continuing the
    // existing draft keeps the revision it was composed against, and an edit
    // switching layouts composes against that layout's confirmed revision.
    const revision = existing?.layout === layout ? existing.revision : confirmedFor(hub, layout).revision;
    persistDraft({ layout, baseRevision: revision, config, writeUncertain: false });
    // The generation follows the same rule as the revision: an edit
    // continuing the same layout's draft keeps BOTH, because re-stamping the
    // current generation here would launder a draft that went stale across a
    // reconnect - a hub replacement whose numbering reuses the same revision
    // would let an ordinary edit clear draftConflict and save settings
    // composed against the old hub without the review the generation guard
    // exists to force. Only a fresh draft or an explicit rebase composes
    // against the current generation.
    const generation = existing?.layout === layout ? existing.generation : currentGeneration();
    const draft: TranscriptDraft = { layout, revision, config, generation };
    setState({ draft, draftConflict: staleDraft(draft, hub, fence.generation), draftError: null });
  }

  async function saveDraft(
    layoutInput?: ViewportClass,
    input?: TranscriptDisplayConfigV1,
  ): Promise<HubTranscriptDisplayDefault> {
    const hub = assertEditable();
    if (getState().draftConflict) throw new Error(DRAFT_REVIEW_BEFORE_SAVE_MESSAGE);
    const existing = getState().draft;
    const layout = layoutInput ?? existing?.layout ?? "mobile";
    const confirmed = confirmedFor(hub, layout);
    const config = normalizeConfig(input ?? (existing?.layout === layout ? existing.config : confirmed.config));
    const requestedFingerprint = configFingerprint(config);
    const revision = existing?.layout === layout ? existing.revision : confirmed.revision;
    // The durable intent must exist before the request can leave the device.
    const checkpoint = persistDraft({ layout, baseRevision: revision, config, writeUncertain: true });
    const generation = fence.generation;
    // Both write paths share the layout's token: the checkpointed save
    // supersedes an earlier direct write's reply on this layout, and any later
    // write supersedes this one's - the direct write's gate refuses to start
    // while `saving` holds, so only the checkpointed path can take over a
    // live direct write's layer.
    const token = fence.claimWrite();
    patchTokens.set(layout, token);
    const stillMine = () => fence.liveHub(generation) && patchTokens.get(layout) === token;
    // A newer write on this layout owns the preview from here on, so a
    // support flap stranding an older write must not reach this one.
    strandedPreviews.delete(layout);
    // The takeover also clears the layout's direct-write preview: the token
    // claim just superseded that write, so its reply will be discarded by
    // the token check and nothing else would ever clear the preview - not
    // the discarded settlement, and not an unchanged refresh, which
    // contradicts nothing.
    setState({
      saving: true,
      draft: { layout, revision, config, generation: currentGeneration() },
      draftError: null,
      ...clearPreview(layout),
    });
    let result: unknown;
    try {
      // The saving publish above may have disposed the store or retired the
      // payload (a host tearing down on the transition): the checkpoint stays
      // for the next instance to restore, and nothing leaves.
      if (!stillMine()) throw new Error(SAVE_CANCELLED_MESSAGE);
      result = await client.request("evener/settings/transcriptDisplay/patch", {
        layout,
        expectedRevision: revision,
        config: toWireConfig(config),
      });
    } catch (error) {
      if (!stillMine()) {
        settleLostHubWrite(generation, layout, token);
        throw error;
      }
      const canonical = conflictCurrent(error, layout);
      if (canonical !== undefined) {
        // A revision conflict is not a lost reply: the hub REFUSED this write
        // and said what the current value is, so the outcome is known. The
        // canonical lands and the proposal stays for review against it. The
        // checkpoint settles inside the settle callback, which runs BEFORE
        // the publication: after it, a synchronous subscriber may already
        // have written a newer draft, and the repository's identity tracks
        // every write, so a re-mark settling later would reach past this
        // write's own record and overwrite that newer edit on disk. A
        // refused compare-and-swap adopts the replacement instead, judged
        // against the final hub the callback is handed.
        applyHubDefault(layout, canonical, (hub): Partial<TranscriptDisplayStoreFields> => {
          const extra: Partial<TranscriptDisplayStoreFields> = {
            saving: false,
            writeUncertain: false,
            draftConflict: true,
          };
          try {
            if (!draftRepository.replaceClassified({ ...checkpoint, writeUncertain: false }))
              return { ...extra, ...restoreDraft({ loaded: true, hub }) };
          } catch {
            return { ...extra, storageUnavailable: true, draftError: DRAFT_CLEANUP_FAILED_MESSAGE };
          }
          return extra;
        });
        throw error;
      }
      const applied = postApplyDefault(error, layout);
      if (applied !== undefined) {
        // The hub applied this write before a follow-up durable step failed
        // and said what it applied: the outcome is KNOWN, not uncertain. The
        // direct write reconciles the same response the same way; leaving
        // writeUncertain here would block edits and even discard on an
        // outcome the hub already reported.
        return settleConfirmedSave(applied, checkpoint, layout);
      }
      // No reply: the write's outcome is unknown, and that fact is the state
      // (writeUncertain) rather than a message. The checkpoint already says so.
      setState({ saving: false, draftConflict: true, writeUncertain: true });
      throw error;
    }
    // The reply is back. One ordered sequence, nothing ahead of the fence:
    // (1) FENCE - a reply that is no longer ours is discarded without
    //     interpretation; a retirement already published writeUncertain from
    //     `saving`, and the checkpoint on the port is the durable record.
    // (2) DECODE - the shared decodePatchReply, so this path cannot accept a
    //     reply the direct write would refuse. A malformed reply is
    //     hub-sourced: hubError, the outcome stays unknown, the checkpoint
    //     stays.
    // (3) APPLY, settling the in-flight flags on every path. A newer external
    //     revision that landed meanwhile keeps the proposal for review instead
    //     of reporting it applied - `>`, not "differs": an equal revision is
    //     this write's own broadcast arriving ahead of its reply.
    // (4) STORAGE LAST - released on a confirmed write, re-marked settled
    //     when the proposal stays for review; a cleanup failure keeps the
    //     draft in view with the port marked unavailable, and never turns a
    //     confirmed write back into an unknown outcome.
    if (!stillMine()) {
      settleLostHubWrite(generation, layout, token);
      return getState().hub[layout] ?? confirmed;
    }
    let value: HubTranscriptDisplayDefault;
    try {
      value = decodePatchReply(result, layout, confirmed, requestedFingerprint);
    } catch (error) {
      setState({ saving: false, draftConflict: true, writeUncertain: true, hubError: MALFORMED_PATCH_MESSAGE });
      throw error;
    }
    return settleConfirmedSave(value, checkpoint, layout);
  }

  /** The confirmed-save settlement the reply and post-apply paths share:
   * land the value (or keep the proposal for review when a newer external
   * revision beat it), settle the in-flight flags, and clean the checkpoint
   * up. The cleanup runs inside the settle callback - before the
   * publication that unblocks edits and while `saving` still holds every
   * subscriber away from the port - because the repository's identity
   * tracks every write: a re-mark or removal settling after that
   * publication would reach past this write's own record once a
   * subscriber's edit re-classified it. A refused compare-and-swap adopts
   * the replacement instead, judged against the final hub the callback is
   * handed; a cleanup failure keeps the proposal in view with the port
   * marked unavailable, and never turns a confirmed write back into an
   * unknown outcome. The layout's direct-write preview was cleared at this
   * write's takeover, so nothing preview-shaped survives to this
   * settlement. */
  function settleConfirmedSave(
    value: HubTranscriptDisplayDefault,
    checkpoint: TranscriptDraftCheckpoint,
    layout: ViewportClass,
  ): HubTranscriptDisplayDefault {
    const newerExternal = (getState().hub[layout]?.revision ?? -1) > value.revision;
    const settled: Partial<TranscriptDisplayStoreFields> = {
      saving: false,
      writeUncertain: false,
      draftConflict: newerExternal,
    };
    const settleStorage = (hub: HubDefaultsByLayout): Partial<TranscriptDisplayStoreFields> => {
      let settledStorage: Partial<TranscriptDisplayStoreFields>;
      try {
        if (newerExternal) {
          // The proposal stays for review; its checkpoint is re-marked
          // settled unless another writer replaced it, in which case the
          // replacement is adopted rather than overwritten.
          settledStorage = draftRepository.replaceClassified({ ...checkpoint, writeUncertain: false })
            ? settled
            : { ...settled, ...restoreDraft({ loaded: true, hub }) };
        } else if (draftRepository.removeIf(checkpoint)) {
          // The write landed and settled its own checkpoint: the draft it
          // described is gone with it.
          settledStorage = { ...settled, draft: null };
        } else {
          // The checkpoint this write settled is gone, replaced by another
          // writer while the PATCH was out: the replacement survives on disk
          // (removeIf's own compare refused to touch it) and must not be
          // hidden behind a "no draft" report.
          settledStorage = { ...settled, ...restoreDraft({ loaded: true, hub }) };
        }
      } catch {
        // A cleanup failure keeps the proposal in view with the port marked
        // unavailable.
        settledStorage = { ...settled, storageUnavailable: true, draftError: DRAFT_CLEANUP_FAILED_MESSAGE };
      }
      return settledStorage;
    };
    if (newerExternal) setState(settleStorage(getState().hub));
    else applyHubDefault(layout, value, settleStorage);
    return value;
  }

  /** Throwing a proposal away composes nothing and sends nothing, so it needs
   * no confirmed hub state - only that the proposal is not mid-flight and the
   * port is usable (an unreadable record is the one storage failure discarding
   * can FIX, so it is not a reason to refuse either). Gating it on the
   * editor's full contract would strand a restored draft on a store whose hub
   * read failed. */
  function discardDraft(): void {
    assertDraftDiscardable(fence, getState, UNAVAILABLE_MESSAGE);
    discardCheckpointedDraft(draftRepository, getState, setState, restoreDraft, DRAFT_DISCARD_FAILED_MESSAGE);
  }

  function rebaseDraft(reviewedRevision: number): void {
    const hub = assertEditable();
    const { draft, hubLoading } = getState();
    if (draft === null || hubLoading || confirmedFor(hub, draft.layout).revision !== reviewedRevision)
      throw new Error(DRAFT_REVIEW_AGAIN_MESSAGE);
    persistDraft({ layout: draft.layout, baseRevision: reviewedRevision, config: draft.config, writeUncertain: false });
    setState({
      draft: { ...draft, revision: reviewedRevision, generation: currentGeneration() },
      draftConflict: false,
      draftError: null,
    });
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
      // The persisted draft outlives the hub lifecycle: reset re-reads it
      // instead of wiping it - an identity-aware reload, so a record this
      // store had in view keeps the generation stamp it was composed under
      // while a replacement (which knows nothing about this store's
      // generations) restores with none, exactly as an adoption would. The
      // restore merges onto the PRIOR draft state, the same merge
      // recoverDraftPort runs, so a plain port failure's omitted fields
      // keep their pre-reset values - in particular the unreadable
      // classification that lets discard keep naming the record it holds;
      // spreading initialState() here would have set them to their cleared
      // values first. `saving` is not the restore's to clear, and a reset
      // always ends the editor's in-flight bookkeeping.
      const {
        draft: _draft,
        saving: _saving,
        writeUncertain: _writeUncertain,
        storageUnavailable: _storageUnavailable,
        draftUnreadable: _draftUnreadable,
        draftConflict: _draftConflict,
        draftError: _draftError,
        ...lifecycle
      } = initialState();
      try {
        const { checkpoint, sameIdentity } = draftRepository.reload();
        setState({
          ...lifecycle,
          saving: false,
          ...restoreDraft(
            { loaded: false, hub: {} },
            sameIdentity ? (getState().draft?.generation ?? null) : null,
            checkpoint,
          ),
        });
      } catch (error) {
        const unreadable = error instanceof UnreadableDraftError;
        setState({
          ...lifecycle,
          saving: false,
          storageUnavailable: true,
          draftError: DRAFT_RESTORE_FAILED_MESSAGE,
          ...(unreadable ? { draftUnreadable: true, draft: null, writeUncertain: false, draftConflict: false } : {}),
        });
      }
    },
    dispose() {
      if (fence.disposed) return;
      endReadyGeneration();
      fence.dispose();
    },
  };
}
