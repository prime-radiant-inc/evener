// Pure reducer folding AppWire wire shapes into the UI-ready ThreadModel.
// hydrateThread REPLACES the model wholesale (snapshot recovery, e.g. on
// (re)subscribe); applyNotification folds one live notification at a time.
// Every function here is pure: given the same inputs, produces the same
// (possibly reference-equal, for no-op cases) output.

import { compareBootGeneration } from "./bootGeneration";
import { appendChunk, pendingTextJoined } from "./chunkview";
import {
  type HistoryState,
  type ItemImage,
  type ItemModel,
  SYSTEM_PRELUDE_TURN_ID,
  type ThreadModel,
  type TurnModel,
} from "./model";
import type {
  AnyNotification,
  EvenerDelegateInfo,
  HistoryUpdatedParams,
  InputItem,
  OutputImage,
  OverlayDeltaParams,
  OverlayItem,
  SandboxEscalationRequested,
  Thread,
  ThreadItem,
  ThreadItemPosition,
  ThreadReadResponse,
  ThreadResyncParams,
  ThreadTurnsListResponse,
  Turn,
  WarningParams,
} from "./types.gen";

function cloneStableDelegate(delegate: EvenerDelegateInfo): EvenerDelegateInfo {
  // JSON objects are open at runtime. Explicitly discard the delegate_send
  // result's call-scoped wait field if an older/mixed producer accidentally
  // places it beside a stable snapshot; every durable stable field remains.
  const { waitIgnoredReason: _callScoped, ...stable } = delegate as EvenerDelegateInfo & {
    waitIgnoredReason?: unknown;
  };
  return {
    ...stable,
    warnings: stable.warnings ? [...stable.warnings] : undefined,
    diagnostics: stable.diagnostics ? [...stable.diagnostics] : undefined,
    usage: stable.usage ? { ...stable.usage } : undefined,
    worktree: stable.worktree ? { ...stable.worktree } : undefined,
  };
}

function laterActivity(current: string | undefined, incoming: string | undefined): string | undefined {
  if (!incoming) return current;
  if (!current) return incoming;
  const currentMillis = Date.parse(current);
  const incomingMillis = Date.parse(incoming);
  if (Number.isNaN(incomingMillis)) return current;
  if (Number.isNaN(currentMillis) || incomingMillis > currentMillis) return incoming;
  return current;
}

function mergeStableDelegate(current: EvenerDelegateInfo, incoming: EvenerDelegateInfo): EvenerDelegateInfo {
  const activity = laterActivity(current.latestActivityAt, incoming.latestActivityAt);
  const state = incoming.projectionRevision > current.projectionRevision ? cloneStableDelegate(incoming) : current;
  if (activity === state.latestActivityAt) return state;
  return { ...state, latestActivityAt: activity };
}

function upsertStableDelegate(
  delegates: readonly EvenerDelegateInfo[],
  incoming: EvenerDelegateInfo,
): EvenerDelegateInfo[] {
  const index = delegates.findIndex((delegate) => delegate.delegateId === incoming.delegateId);
  if (index === -1) return [...delegates, cloneStableDelegate(incoming)];
  const current = delegates[index];
  if (!current) return [...delegates, cloneStableDelegate(incoming)];
  const merged = mergeStableDelegate(current, incoming);
  if (merged === current) return delegates as EvenerDelegateInfo[];
  return delegates.map((delegate, delegateIndex) => (delegateIndex === index ? merged : delegate));
}

function epochMsToISO(ms: number | undefined): string | undefined {
  // Go's zero value leaks through the wire as 0: a non-positive (or NaN)
  // anchor means "absent", never the 1970 epoch - downstream duration math
  // must never clock against it. statusFormat.ts rejects bad anchors too,
  // as defense-in-depth for its own callers.
  return ms === undefined || Number.isNaN(ms) || ms <= 0 ? undefined : new Date(ms).toISOString();
}

// joinedReasoningParagraphs turns ItemModel.reasoningSummaries (string[][] —
// per-summaryIndex chunk lists) into one string per summaryIndex, dropping
// any paragraph that joins to nothing or to whitespace alone: a summary the
// model opened but nothing ever streamed into is not a blank paragraph on
// screen. Every per-summary join goes through pendingTextJoined, so a live
// chunk view answers from its brand-cached text in O(1) rather than an
// element-by-element Proxy walk on every render. THE reading of that field
// for the web's think block (cmd/evener-hub/frontend/src/panes/session/
// transcript/messages/ThinkBlock.tsx) — check each host's own reasoning
// row before assuming it reads this too; not every consumer does.
export function joinedReasoningParagraphs(summaries: string[][] | undefined): string[] {
  if (!summaries) return [];
  return summaries.map((chunks) => pendingTextJoined(chunks)).filter((text) => text.trim() !== "");
}

// Thread.createdAt/updatedAt are the wire's only Unix-SECONDS stamps
// (cmd/evener-hub's hubcore.UnixSeconds for a past session,
// appsource/local_daemon.go's entry.StartedAt.Unix() for a live one), unlike
// every millisecond stamp epochMsToISO handles. Same absent-means-absent rule.
function epochSecondsToISO(seconds: number | undefined): string | undefined {
  return epochMsToISO(seconds === undefined || Number.isNaN(seconds) ? seconds : seconds * 1000);
}

// ItemModel.images/outputImages carry a resolved src alongside the wire's own
// name/path/(source) fields (ItemImage, model.ts) rather than collapsing to
// just src. src keeps preferring url — the field the legacy web client
// (cmd/evener-hub/assets/renderer.js: imagesForUserItem, renderToolOutputImages)
// treats as the <img src> — then the inline data-URI bytes, then a
// sha-addressed /s/{route}/images/{sha} route rebuilt from metadata["sha"]
// when the serving session is known (the wire Thread.sessionId, mirroring
// stampThreadImageURLs' sessionID-over-ID preference in output_images.go;
// imageSessionRoute undefined keeps that branch dark), then path or name,
// exactly as before; name/path/source ride alongside src unresolved so a
// renderer can caption the image instead of losing everything but whichever
// field happened to win that fallback (kata byq2). Bytes beat the
// synthesized route because they render unconditionally while the route 404s
// whenever the session is absent from the hub's Past index
// (handleSessionImage, image_serve.go) — so the route only ever fires for
// sha-only replay descriptors that carry no bytes at all.
// Output images: see appwire.MergeOutputImages; input images: see appwire.MergeInputImages.
function imagesToItemImagesForSession(
  images: InputItem[] | undefined,
  imageSessionRoute: string | undefined,
): ItemImage[] | undefined {
  // An empty list says nothing about this item's input images, the same rule the
  // hub applies on its own merges (`len(incoming.Images) == 0` keeps the
  // existing list: server/appwire_turns.go:884-886,
  // internal/apptranscript/logical_turn.go:309) and the same reading the wire's
  // `omitempty` implies. Real frames carry it — a steering notification with
  // `images: []` (fixtures/tool-and-jobs.jsonl:4) — and treating it as a removal
  // erases an older page's images through mergePageItem. outputImagesToItemImages
  // below reads the opposite way, because the wire itself means the opposite:
  // OutputImages is omitzero, so nil (never had any) sends no key, while a
  // non-nil empty list is the hub's only way to say "these are gone" — an
  // explicit removal (appwire/output_images.go's nil/non-nil-empty/non-empty
  // rule). Input images carry no removal signal at all (they're what the user
  // sent), which is why they read every empty list the same as absent.
  if (!images || images.length === 0) return undefined;
  // A composer-attached image reaches the wire as inline bytes (mediaType +
  // data, no url/path — appwire_projection.go's projectUserInputImages), so
  // when no server route is named, the bytes themselves are the src. Falling
  // through to the bare name gave the browser a relative URL that 404s, and
  // ImageGallery drops an unloadable src — no thumbnail at all (kata w53n).
  return images.map((img) => ({
    src: img.url ?? inlineImageSrc(img) ?? metadataShaImageSrc(img, imageSessionRoute) ?? img.path ?? img.name ?? "",
    name: img.name,
    path: img.path,
  }));
}

// A replayed input image carries no bytes at all: projectReplayInputImage
// (app_threadread.go) strips Data and records the content sha in
// metadata["sha"], and stampInputImageURLs (output_images.go) fills in the
// sha-addressed /s/{session}/images/{sha} route the hub serves those bytes on
// (handleSessionImage). A live or paged payload that reaches the client
// without that stamp (older-producer frames, page fits the read path didn't
// re-stamp) still names fetchable bytes by sha, so the short route is the src
// when — and only when — no inline bytes are present: the browser fetches and
// caches by URL instead of holding a ~33%-inflated base64 copy in the model
// heap and the DOM, but payload bytes render unconditionally while the route
// 404s for any session absent from the hub's Past index, so bytes stay first.
// Strict lowercase-hex only (imageShaRegexp): a non-sha metadata value is
// never URL-shaped, so it falls through to path/name rather than producing a
// src the hub would 400 on. imageSessionRoute is undefined wherever the wire
// named no serving session (absent/blank sessionId — the same gap
// stampThreadImageURLs patches with the thread id); without it there is
// nothing fetchable to prefer and the data-URI fallback stands.
function metadataShaImageSrc(img: InputItem, imageSessionRoute?: string): string | undefined {
  const sha = img.metadata?.sha;
  if (sha === undefined || sha === "" || imageSessionRoute === undefined || imageSessionRoute === "") {
    return undefined;
  }
  if (!/^[0-9a-f]{64}$/.test(sha)) return undefined;
  return `/s/${imageSessionRoute}/images/${sha}`;
}

function inlineImageSrc(img: InputItem): string | undefined {
  if (img.data === undefined || img.data === "" || img.mediaType === undefined || img.mediaType === "") {
    return undefined;
  }
  return `data:${img.mediaType};base64,${img.data}`;
}

// Output images: see appwire.MergeOutputImages; input images: see appwire.MergeInputImages.
function outputImagesToItemImages(images: OutputImage[] | undefined): ItemImage[] | undefined {
  if (!images) return undefined;
  return images.map((img) => ({
    src: img.url ?? img.path ?? img.name ?? img.source,
    name: img.name,
    path: img.path,
    source: img.source,
  }));
}

// Maps a wire ThreadItem to a settled ItemModel (no pendingText — that only
// exists for an item currently streaming). A reasoning item that already
// carries flattened text (e.g. replayed from a persisted transcript on
// hydrate) is seeded as a single chunk so display-time joining still works;
// when a later settle carries no text of its own, the live in-flight chunks
// accumulated via item/reasoning/summaryTextDelta are preserved by the
// item/completed and turn/completed handlers (mergeReasoning). A settle that
// DOES carry text re-seeds through here and wins — see mergeReasoning.
const ITEM_TEXT_PRESENCE = Symbol("itemTextPresence");
type ItemTextPresence = "omitted" | "provided";
type InternalItemModel = ItemModel & { [ITEM_TEXT_PRESENCE]?: ItemTextPresence };

function setItemTextPresence(item: ItemModel, presence: ItemTextPresence): ItemModel {
  Object.defineProperty(item, ITEM_TEXT_PRESENCE, { value: presence, enumerable: false, configurable: true });
  return item;
}

// Exported (through index.ts) for the mobile store's rehydrate prep: its
// clone sites spread items to patch fields, and the presence marker below is
// non-enumerable, so every clone must re-apply it or a stripped sparse item
// reads as an authoritative empty settle and blocks the page that later
// brings its real text (RoboRev round 9).
export function copyItemTextPresence(source: ItemModel, target: ItemModel): ItemModel {
  const presence = (source as InternalItemModel)[ITEM_TEXT_PRESENCE];
  return presence === undefined ? target : setItemTextPresence(target, presence);
}

// Exported (through index.ts) alongside copyItemTextPresence for the
// mobile store's rehydrate prep, which must tell an authoritative snapshot
// text from one the read omitted.
export function itemTextPresence(item: ItemModel): ItemTextPresence {
  return (item as InternalItemModel)[ITEM_TEXT_PRESENCE] ?? "provided";
}

// The public handle on the omission marker: mark a hand-built ItemModel as
// carrying no text of its own — the same semantics wireItemToModel gives a
// wire item whose text field was omitted (an omitted text hydrates to ""
// with this marker, never to undefined). Merges then treat the item exactly
// like a sparse wire fragment: it never wins a textSource selection against
// a side that actually provided text, and it never blocks one — a later
// merge with a text-bearing side adopts that side's text instead of holding
// the marked item's empty settle as authoritative. The mobile store's
// compact-turn skeletons (identity-only stand-ins for shed payloads) are
// the caller: their empty settle must stay adoptable by a later page that
// brings the item's real text, while still satisfying ItemModel's
// required-string invariant.
export function markItemTextOmitted(item: ItemModel): ItemModel {
  return setItemTextPresence(item, "omitted");
}

const ITEM_IDENTITY_ONLY = Symbol("itemIdentityOnly");

// The public handle on the identity-only marker: mark a hand-built ItemModel
// as carrying nothing but its identity, ordering and fold-classification
// fields — no text, no payload, nothing a merge keeps from it. The mobile
// store's compact-turn skeletons are the caller: a skeleton stands in for a
// shed payload, so merges must never read one as supplying content — most
// importantly, a fold that only drew a skeleton must not count as fresh
// payload participation (the duplicate reconciliation's precedence reads
// exactly that). A marked item keeps its other semantics unchanged: merges
// treat its text per the omitted-text marker, and identity matching ignores
// the marker entirely.
export function markItemIdentityOnly(item: ItemModel): ItemModel {
  Object.defineProperty(item, ITEM_IDENTITY_ONLY, { value: true, enumerable: false, configurable: true });
  return item;
}

// Whether an item carries nothing a merge keeps beyond identity, ordering
// and fold-classification. A marked item is identity-only by declaration —
// the marker exists precisely because shape alone cannot prove intent. An
// unmarked item is identity-only only when it says so structurally: its
// text is omitted (never a textSource winner) and every field it carries
// is one the merge would NOT keep from it — each by its own rule (review
// rounds 16 and 23): the nullish-fallback fields fall through on undefined
// AND null, the rank-merged status on undefined, and the spread-merged
// fields by property presence, where an own undefined is an explicit CLEAR
// the merge keeps — content, not absence. Hydration never creates those
// clears (it sets a field only when the wire carried one) while it always
// creates the nullish-fallback fields undefined-valued — which is why key
// presence alone cannot decide either way. A real item with omitted text is
// NOT identity-only: tool items routinely omit text while carrying their
// current output, arguments, images, and status.
const itemIdentityOnlyFields = new Set(["id", "turnId", "type", "text", "transcriptKey", "position", "callId"]);
function itemIsIdentityOnly(item: ItemModel): boolean {
  if ((item as ItemModel & { [ITEM_IDENTITY_ONLY]?: boolean })[ITEM_IDENTITY_ONLY] === true) return true;
  if (itemTextPresence(item) !== "omitted") return false;
  return Object.keys(item).every((field) => {
    if (itemIdentityOnlyFields.has(field)) return true;
    const value = (item as unknown as Record<string, unknown>)[field];
    if (freshSuppliedNullishFallbackFields.has(field)) return value === undefined || value === null;
    if (field === "status") return value === undefined;
    // Spread-merged by property presence: an own undefined is an explicit
    // clear, which the merge keeps — content.
    return false;
  });
}

// imageSessionRoute threads through wireItemToModel/wireToTurnModel from the
// callers that can name the serving session — hydrateThread's wire
// thread.sessionId (falling back to the thread id, mirroring
// stampThreadImageURLs in output_images.go), item pages and live
// notifications' model.imageSessionId, carried on the model from hydrate —
// so a sha-bearing input image that arrived WITHOUT its stamped url still
// folds to the short /s/{route}/images/{sha} src when it carries no inline
// bytes — otherwise the bytes stay the src. Undefined on the paths that
// cannot name it — the sha branch then stays dark and every image resolves
// exactly as before.
function wireItemToModel(item: ThreadItem, imageSessionRoute?: string): ItemModel {
  const model: ItemModel & { clientMutationId?: string } = {
    id: item.id,
    turnId: item.turnId ?? "",
    type: item.type,
    text: item.text ?? "",
    toolName: item.toolName,
    callId: item.callId,
    argumentsJSON: item.argumentsJson,
    description: item.description,
    eventKind: item.eventKind,
    raw: item.raw,
    output: item.output,
    error: item.error,
    prevalOnly: item.prevalOnly,
    exitCode: item.exitCode,
    images: imagesToItemImagesForSession(item.images, imageSessionRoute),
    outputImages: outputImagesToItemImages(item.outputImages),
    status: item.status,
    source: item.source,
    steeringKind: item.steeringKind,
    startedAt: epochMsToISO(item.startedAt),
    completedAt: epochMsToISO(item.completedAt),
  };
  setItemTextPresence(model, item.text === undefined ? "omitted" : "provided");
  if (item.transcriptKey !== undefined) model.transcriptKey = item.transcriptKey;
  if (item.position !== undefined) model.position = { ...item.position };
  // Set only when the wire carried one (like clientMutationId below, and
  // unlike the always-copied fields above): an absent transcriptEntryIndex
  // means the item has no persisted transcript position at all, which a fork
  // affordance must be able to tell apart from a real index.
  if (item.transcriptEntryIndex !== undefined) model.transcriptEntryIndex = item.transcriptEntryIndex;
  if (item.clientMutationId) model.clientMutationId = item.clientMutationId;
  // Versioned-history fields, set only when the wire carried them (a v6 read,
  // history/updated, or an overlay item), so pre-v6 items keep their shape.
  if (item.version) model.version = item.version;
  if (item.roundId) model.roundId = item.roundId;
  if (item.completedAtEntry) model.completedAtEntry = item.completedAtEntry;
  // `item.text !== undefined` (not truthiness): an explicitly provided empty
  // text is authoritative for a reasoning row exactly as it is for assistant
  // text (mergeCompletedText), so it seeds an authoritative EMPTY summary
  // ([[""]], which display-time joining drops to no paragraph) instead of
  // leaving reasoningSummaries unset — unset means "the settle said nothing"
  // to mergeReasoning, which would keep stale chunks on screen.
  if (item.type === "reasoning" && item.text !== undefined) {
    model.reasoningSummaries = [[item.text]];
  }
  return model;
}

// The model "keeps chunks" only when the settle carries no text of its own.
// An item/completed (or a "full" turn/completed item) that brings its own
// text is authoritative for a reasoning row exactly as it is for assistant
// text (mergeCompletedText): wireItemToModel has already seeded
// reasoningSummaries from that text (including an explicit empty text, as the
// authoritative-empty [[""]]), and that complete flattened reasoning replaces
// whatever the model accumulated, so a settle can correct a row the item's
// earlier seed or live deltas got wrong. An omitted text — the wire never
// sends an empty Text (appwire/types.go's `text,omitempty`), and the live
// settle carries none for reasoning — has nothing to say, so the chunks
// accumulated from item/reasoning/summaryTextDelta survive (only ever joined
// for display, by the consumer).
//
// The seed, not itemTextPresence(settled), is the signal deliberately:
// mergeCompletedText runs before this helper in every chain and copies the
// EXISTING item's presence onto its result when the settle omitted text, so
// by the time this runs a previously-text-bearing item's omitted settle still
// reads "provided" — presence here would discard the very chunks an omission
// must preserve.
function mergeReasoning(settled: ItemModel, existing: ItemModel | undefined): ItemModel {
  if (settled.reasoningSummaries) return settled;
  if (existing?.reasoningSummaries) {
    return copyItemTextPresence(settled, { ...settled, reasoningSummaries: existing.reasoningSummaries });
  }
  return settled;
}

// Omission is not a replacement: finalize the text already observed locally.
// Explicit wire text, including empty text, remains authoritative.
function mergeCompletedText(settled: ItemModel, existing: ItemModel | undefined): ItemModel {
  if (!existing || itemTextPresence(settled) === "provided") return settled;
  const pending = existing.pendingText;
  const merged = copyItemTextPresence(existing, {
    ...settled,
    text: existing.text + (pending === undefined ? "" : pendingTextJoined(pending)),
  });
  return pending === undefined ? merged : setItemTextPresence(merged, "provided");
}

// Output images: see appwire.MergeOutputImages; input images: see appwire.MergeInputImages.
function mergeItemImages(settled: ItemModel, existing: ItemModel | undefined): ItemModel {
  if (!existing) return settled;
  const images = settled.images ?? existing.images;
  const outputImages = settled.outputImages ?? existing.outputImages;
  if (images === settled.images && outputImages === settled.outputImages) return settled;
  // The text-presence marker is non-enumerable, so a spread drops it: carry it
  // the way every other merge in this chain does.
  return copyItemTextPresence(settled, {
    ...settled,
    ...(images === undefined ? {} : { images }),
    ...(outputImages === undefined ? {} : { outputImages }),
  });
}

// item/completed's settled wire item never carries observedStartedAt/
// observedCompletedAt — those are model-only client observations (see
// ItemModel's doc comment in model.ts), never present on a wire ThreadItem,
// so wireItemToModel never sets them and a fresh `settled` object has
// already lost whatever appendReasoningDelta stamped. Carries the existing
// item's observedStartedAt forward, and — if observation began but never
// got a completion stamp — stamps observedCompletedAt from `now` (purity:
// only ever from the now argument, never a clock read).
function mergeObservedTiming(settled: ItemModel, existing: ItemModel | undefined, now: number): ItemModel {
  if (existing?.observedStartedAt === undefined) return settled;
  return copyItemTextPresence(settled, {
    ...settled,
    observedStartedAt: existing.observedStartedAt,
    observedCompletedAt: existing.observedCompletedAt ?? epochMsToISO(now),
  });
}

// The live tool-settle site drops ArgumentsJSON: EventToolCallEnd
// (internal/appprojector/appwire_projection.go:414-442) resolves it into
// argsJSON at :424-427 but uses that only to derive Description, never
// attaching it to the emitted ThreadItem — so the settled wire item's
// argumentsJson is empty even though the streamed item/started item (:373)
// had it. Historical items don't lose it
// (internal/apptranscript/apptranscript.go:284,312), so this is a
// live-settle-only gap the model corrects: keep the existing item's
// argumentsJSON when the settled payload didn't bring its own. A settled
// payload that DOES carry argumentsJson wins — wire truth over memory.
function mergeArguments(settled: ItemModel, existing: ItemModel | undefined): ItemModel {
  if (settled.argumentsJSON !== undefined) return settled;
  if (existing?.argumentsJSON === undefined) return settled;
  return copyItemTextPresence(settled, { ...settled, argumentsJSON: existing.argumentsJSON });
}

// Folds a PRESERVED item (one carried over from before settlement, not
// replaced by wire-authoritative data — see the "turn/completed" case) into
// its settled shape. The live wire's settle stamp carries no items at all,
// so there is no authoritative text to adopt the way item/completed would;
// any pendingText chunks still sitting on the item are joined into text
// exactly as item/completed would eventually have finalized them (mirrors
// item/agentMessage/delta's own chunk accumulation). An item still marked
// inProgress inside a settled turn is stale — a turn cannot complete with
// one of its own items unfinished (e.g. an interrupt or session-end cut a
// stream short before its own item/completed arrived) — so its status is
// promoted to completed. reasoningSummaries pass through untouched (they are
// already the model's own accumulated chunks, not wire data to merge). An
// item still under reasoning-timing observation (observedStartedAt set, no
// observedCompletedAt yet) gets observedCompletedAt stamped from `now` — the
// turn ending is the honest end of observation (see ItemModel's doc comment
// in model.ts).
function settleItem(item: ItemModel, now: number): ItemModel {
  const pending = item.pendingText;
  const stale = item.status === "inProgress";
  const needsObservedCompletion = item.observedStartedAt !== undefined && item.observedCompletedAt === undefined;
  if (pending === undefined && !stale && !needsObservedCompletion) return item;
  const settled = copyItemTextPresence(item, {
    ...item,
    text: pending === undefined ? item.text : item.text + pendingTextJoined(pending),
    pendingText: undefined,
    status: stale ? "completed" : item.status,
    observedCompletedAt: needsObservedCompletion ? epochMsToISO(now) : item.observedCompletedAt,
  });
  return pending === undefined ? settled : setItemTextPresence(settled, "provided");
}

// The turn-level (non-items) fields wireToTurnModel maps — split out so the
// "turn/completed" bare-stamp path (which has real turn fields but no items
// worth trusting) can reuse the exact same field mapping without also
// pulling in wireToTurnModel's item conversion.
function wireToTurnScalars(turn: Turn): Omit<TurnModel, "items"> {
  return {
    id: turn.id,
    status: turn.status,
    startedAt: epochMsToISO(turn.startedAt),
    completedAt: epochMsToISO(turn.completedAt),
    durationMs: turn.durationMs,
    usage: turn.usage,
    cost: turn.cost,
    error: turn.error,
    ...(turn.version ? { version: turn.version } : {}),
  };
}

function wireToTurnModel(turn: Turn, imageSessionRoute?: string): TurnModel {
  return {
    ...wireToTurnScalars(turn),
    items: (turn.items ?? []).map((item) => wireItemToModel(item, imageSessionRoute)),
  };
}

// evener.activeTurnId is the primary signal; a turn already marked inProgress
// in the snapshot is the fallback for daemons/sources that don't populate it
// (mirrors activeTurnIDFromThread in cmd/evener-hub/assets/appwire.js).
function activeTurnIdFromThread(thread: Thread): string | undefined {
  if (thread.evener.activeTurnId) return thread.evener.activeTurnId;
  return thread.turns?.find((t) => t.status === "inProgress")?.id;
}

const isToolResultId = (id: string) => id.startsWith("item_tool_result_");
const isToolCallId = (id: string) => id.startsWith("item_tool_") && !isToolResultId(id);

// The same wire id conventions, public for callers that must classify items
// exactly as the callId fold does (it removes item_tool_result_* results and
// rewrites item_tool_* calls). The mobile store's skeleton strip needs the
// distinction: a rewritten CALL host can carry content the removed result was
// the only other holder of.
export const isToolResultItemId: (id: string) => boolean = isToolResultId;
export const isToolCallItemId: (id: string) => boolean = isToolCallId;

// Reload projects a tool CALL and its RESULT as two items sharing a callId, in
// separate wire turns (apptranscript.TurnsFromFile mints one turn per transcript
// entry). Collapse them the way the live path already produces a single item:
// the call supplies id + argumentsJSON + startedAt, the result supplies output +
// error + exitCode + completedAt + settled status. A turn emptied by the merge is
// dropped unless it was already empty or carries canonical turn metadata. (zrzr)
// Item payloads lose page ownership during retained placement. Keep the
// original source values beside the folded turns so inherited fields do not
// acquire the freshness of the item that carried them. The merge tree records
// membership at the identity-match edge; reconstructing it from final IDs
// would lose aliases and would make hydration quadratic.
type ToolItemSource = "fresh" | "older";
type ToolItemSourceMembership =
  | { item: ItemModel }
  | { left: ToolItemSourceMembership; right: ToolItemSourceMembership };
type ToolItemProvenance = Partial<Record<ToolItemSource, ToolItemSourceMembership>>;
// The recorded answer to "which inputs' content does this item carry":
// per non-tool field, every input that could have supplied the value the
// edge kept (an indistinguishable pair records BOTH — a side is necessary
// to the field only when it holds every supplier); per tool-result field,
// the candidate the rewrite's selection scanned; and the identity anchors
// — every input the item's existence folds from, content or not. A side
// holding all of the anchors owns the item outright.
type ItemContribution = {
  nonTool: ReadonlyMap<string, ReadonlySet<ItemModel>>;
  tools: Partial<Record<ToolResultField, ReadonlySet<ItemModel>>>;
  identity: ReadonlySet<ItemModel>;
};
type ToolItemMergeContext = {
  provenance: WeakMap<ItemModel, ToolItemProvenance>;
  // The results the tool fold absorbed onto each rewritten call, keyed by
  // the rewritten call (review round 16). Retention-only metadata: the
  // window bound needs it — a folded call is the only payload behind its
  // result's row once the row set keeps the result but not the call —
  // but it must stay OUT of the provenance membership, whose consumers
  // (the strip's real-source test, duplicate-reconciliation freshness)
  // treat a surviving candidate as a source the call never merged from.
  toolResultFolds: WeakMap<ItemModel, readonly ItemModel[]>;
  // The content the fold kept, written where the fold decided to keep it
  // (RoboRev round 5, panel Medium): one record per merged item — the
  // supplier sets per surviving field, and the identity anchors. The
  // identity edges record it beside the membership they already write;
  // the tool rewrite records the winners its own scan selected, dropping
  // the base's suppliers for the fields its candidates overrode. The
  // contribution question reads this record; nothing re-derives the
  // fold's decisions after the fact.
  contributions: WeakMap<ItemModel, ItemContribution>;
};
type ToolCandidates = { calls: ItemModel[]; results: ItemModel[] };

function createToolItemMergeContext(fresh: readonly TurnModel[], older: readonly TurnModel[]): ToolItemMergeContext {
  const context: ToolItemMergeContext = {
    provenance: new WeakMap(),
    toolResultFolds: new WeakMap(),
    contributions: new WeakMap(),
  };
  const add = (source: ToolItemSource, turns: readonly TurnModel[]): void => {
    for (const turn of turns) {
      for (const item of turn.items) {
        const existing = context.provenance.get(item);
        if (existing?.[source] !== undefined) continue;
        context.provenance.set(item, { ...existing, [source]: { item } });
      }
    }
  };
  add("fresh", fresh);
  add("older", older);
  return context;
}

function combineToolItemMembership(
  left: ToolItemSourceMembership | undefined,
  right: ToolItemSourceMembership | undefined,
): ToolItemSourceMembership | undefined {
  if (left === undefined) return right;
  if (right === undefined) return left;
  return { left, right };
}

function recordMergedToolItem(
  context: ToolItemMergeContext,
  merged: ItemModel,
  older: ItemModel,
  newer: ItemModel,
): void {
  const olderProvenance = context.provenance.get(older);
  const newerProvenance = context.provenance.get(newer);
  if (olderProvenance === undefined && newerProvenance === undefined) return;
  context.provenance.set(merged, {
    fresh: combineToolItemMembership(olderProvenance?.fresh, newerProvenance?.fresh),
    older: combineToolItemMembership(olderProvenance?.older, newerProvenance?.older),
  });
  recordItemContribution(context, merged, older, newer);
}

// The fold's keep-decisions for one identity edge, written as they
// happen. Every field the merged item carries was one input's value: the
// input whose value survived records as the field's supplier, and when
// BOTH inputs carried the same value the pair records together — either
// alone could have supplied it, so a side is necessary to the field only
// when it holds them both (the duplicate's answer: a page copy of a
// retained item shares every field with the retained side and keeps
// nothing). Two identical same-side inputs keep their side's supply —
// the fold of a reissued fragment's duplicate copies must not erase the
// page's own content. Text counts through its presence marker: a side
// whose text the merge dropped contributed no text, and a side that
// restored what the other omitted did. Every other field counts by the
// merge's own rule for it — the nullish-fallback fields only by carrying a
// value (null falls through to the other side), status by the rank chain's
// kept value, and the spread-merged fields by property presence — so a
// supplier set never names a side the merge would have read through to the
// other (#2213 final-verdict Medium: strict value equality let an explicit
// empty text and an omitted text share the supply, because both hydrate to
// "" and the marker is invisible to Object.entries). The identity anchors
// record every input regardless — an item whose anchors all sit on one side
// exists only because that side does. Chained edges resolve through the
// input's own record: an untouched original speaks for itself, a folded one
// for the suppliers it kept.
function recordItemContribution(
  context: ToolItemMergeContext,
  merged: ItemModel,
  older: ItemModel,
  newer: ItemModel,
): void {
  const recordOf = (item: ItemModel): ItemContribution | undefined => context.contributions.get(item);
  const suppliersOf = (item: ItemModel, field: string): ReadonlySet<ItemModel> | undefined => {
    const record = recordOf(item);
    if (record === undefined) return new Set<ItemModel>([item]);
    return toolResultFields.includes(field as ToolResultField)
      ? record.tools[field as ToolResultField]
      : record.nonTool.get(field);
  };
  const identity = new Set<ItemModel>();
  for (const input of [older, newer]) {
    for (const anchor of recordOf(input)?.identity ?? new Set<ItemModel>([input])) {
      identity.add(anchor);
    }
  }
  const attribute = (
    field: string,
    value: unknown,
    fromOlder: boolean,
    fromNewer: boolean,
  ): ReadonlySet<ItemModel> | undefined => {
    if (value === undefined) return undefined;
    if (fromOlder && fromNewer) {
      return new Set<ItemModel>([...(suppliersOf(older, field) ?? []), ...(suppliersOf(newer, field) ?? [])]);
    }
    if (fromOlder) return suppliersOf(older, field);
    if (fromNewer) return suppliersOf(newer, field);
    return undefined;
  };
  // Whether one input could have supplied the value the edge kept, read by
  // the merge's OWN rule for the field: text through its presence marker (an
  // omitted text never supplied text, whatever its placeholder string
  // reads), the nullish-fallback fields only by carrying a value, status by
  // the rank chain's kept value (the merge keeps one input's status, so
  // strict equality is its rule), and every other field by property
  // presence — the spread keeps the newer side's own property, so a side
  // that does not own the property never supplied the value it happens to
  // read as.
  const suppliedBy = (item: ItemModel, field: string, value: unknown): boolean => {
    if (field === "text") return itemTextPresence(item) === "provided" && item.text === value;
    if (field === "status") return item.status === value;
    const itemValue = (item as unknown as Record<string, unknown>)[field];
    if (freshSuppliedNullishFallbackFields.has(field)) {
      return itemValue !== null && itemValue !== undefined && itemValue === value;
    }
    return Object.hasOwn(item, field) && itemValue === value;
  };
  const nonTool = new Map<string, ReadonlySet<ItemModel>>();
  for (const [key, value] of Object.entries(merged)) {
    if (toolResultFields.includes(key as ToolResultField)) continue;
    const set = attribute(key, value, suppliedBy(older, key, value), suppliedBy(newer, key, value));
    if (set !== undefined) nonTool.set(key, set);
  }
  const tools: Partial<Record<ToolResultField, ReadonlySet<ItemModel>>> = {};
  for (const field of toolResultFields) {
    const value = merged[field];
    const set = attribute(field, value, suppliedBy(older, field, value), suppliedBy(newer, field, value));
    if (set !== undefined) tools[field] = set;
  }
  context.contributions.set(merged, { nonTool, tools, identity });
}

function appendToolItemCandidates(
  membership: ToolItemSourceMembership | undefined,
  candidates: ToolCandidates,
  callId: string | undefined,
): void {
  if (membership === undefined || callId === undefined) return;
  const stack: ToolItemSourceMembership[] = [membership];
  while (stack.length > 0) {
    const current = stack.pop();
    if (current === undefined) continue;
    if ("item" in current) {
      if (isToolResultId(current.item.id)) candidates.results.push(current.item);
      else if (isToolCallId(current.item.id)) candidates.calls.push(current.item);
      continue;
    }
    stack.push(current.right, current.left);
  }
}

const toolResultFields = [
  "output",
  "error",
  "prevalOnly",
  "exitCode",
  "completedAt",
  "status",
  "outputImages",
  "raw",
] as const;
type ToolResultField = (typeof toolResultFields)[number];

function collectToolCandidates(
  normalizedTurns: TurnModel[],
  context: ToolItemMergeContext,
  source: ToolItemSource,
): Map<string, ToolCandidates> {
  const candidates = new Map<string, ToolCandidates>();
  for (const turn of normalizedTurns) {
    for (const item of turn.items) {
      if (!item.callId) continue;
      const provenance = context.provenance.get(item);
      const entry = candidates.get(item.callId) ?? { calls: [], results: [] };
      appendToolItemCandidates(provenance?.[source], entry, item.callId);
      if (entry.calls.length > 0 || entry.results.length > 0) candidates.set(item.callId, entry);
    }
  }
  return candidates;
}

function collectDirectToolCandidates(turns: TurnModel[]): Map<string, ToolCandidates> {
  const candidates = new Map<string, ToolCandidates>();
  for (const turn of turns) {
    for (const item of turn.items) {
      if (!item.callId) continue;
      const entry = candidates.get(item.callId) ?? { calls: [], results: [] };
      if (isToolResultId(item.id)) entry.results.push(item);
      else if (isToolCallId(item.id)) entry.calls.push(item);
      candidates.set(item.callId, entry);
    }
  }
  return candidates;
}

// The fold's own view of a turn list, factored out of mergeToolCallsByCallId
// so the coverage walk can ask exactly what the fold will do instead of
// re-deriving it: which call ids exist, the candidates each call id draws
// fields from (per source, through the merge provenance), and which call ids
// have results to fold in.
type ToolFoldView = {
  callIds: Set<string>;
  fresh: Map<string, ToolCandidates>;
  older: Map<string, ToolCandidates>;
  resultCallIds: Set<string>;
  // The fold rewrites calls and drops emptied turns (unless they were already
  // empty or carry canonical turn metadata) only when some call id has a
  // result to fold in; otherwise it returns the turns unchanged.
  noOp: boolean;
};

function toolFoldView(turns: TurnModel[], context?: ToolItemMergeContext): ToolFoldView {
  const callIds = new Set<string>();
  for (const turn of turns) {
    for (const item of turn.items) {
      if (item.callId && isToolCallId(item.id)) callIds.add(item.callId);
    }
  }
  const fresh = context ? collectToolCandidates(turns, context, "fresh") : collectDirectToolCandidates(turns);
  const older = context ? collectToolCandidates(turns, context, "older") : new Map<string, ToolCandidates>();
  const resultCallIds = new Set(
    [...fresh, ...older].flatMap(([callId, candidates]) => (candidates.results.length > 0 ? [callId] : [])),
  );
  return { callIds, fresh, older, resultCallIds, noOp: resultCallIds.size === 0 };
}

function preferredToolField<K extends ToolResultField>(
  item: ItemModel,
  field: K,
  winners: Partial<Record<ToolResultField, ItemModel>>,
  ...sources: readonly (readonly ItemModel[])[]
): ItemModel[K] | undefined {
  for (const candidates of sources) {
    for (let index = candidates.length - 1; index >= 0; index -= 1) {
      const candidate = candidates[index];
      const value = candidate?.[field];
      if (value !== undefined) {
        // The selection's own keep-decision, written where it happens:
        // this candidate is the source the rewritten call's field carries.
        winners[field] = candidate;
        return value;
      }
    }
  }
  return item[field];
}

// The tool-result fold rewrites a surviving call as a NEW object, so any
// identity-fold membership recorded on the pre-rewrite object stops
// answering for the call the turns now carry — a caller following merged
// items through the provenance (the mobile store's retained-turn window
// reads exactly that) would lose the identities the call folded from, and
// a display row naming one of them would stop keeping the rewritten call's
// turn in the window. Only the rewritten call's OWN membership transfers:
// the callId candidates drew fields by call-precedence, not by an identity
// match, so recording them as fold sources would make a surviving candidate
// read as a source the call never merged from.
function recordToolFoldRewrite(context: ToolItemMergeContext, rewritten: ItemModel, source: ItemModel): void {
  const provenance = context.provenance.get(source);
  if (provenance !== undefined) context.provenance.set(rewritten, provenance);
}

function mergeToolCallsByCallId(turns: TurnModel[], context?: ToolItemMergeContext, view?: ToolFoldView): TurnModel[] {
  const fold = view ?? toolFoldView(turns, context);
  if (fold.noOp) return turns;

  const merged: TurnModel[] = [];
  for (const turn of turns) {
    const items: ItemModel[] = [];
    for (const item of turn.items) {
      if (item.callId && isToolResultId(item.id) && fold.callIds.has(item.callId)) continue; // folded into its call
      if (item.callId && isToolCallId(item.id) && fold.resultCallIds.has(item.callId)) {
        const fresh = fold.fresh.get(item.callId) ?? { calls: [], results: [] };
        const older = fold.older.get(item.callId) ?? { calls: [], results: [] };
        if (fresh.results.length > 0 || older.results.length > 0) {
          const fieldWinners: Partial<Record<ToolResultField, ItemModel>> = {};
          const field = <K extends ToolResultField>(name: K) =>
            preferredToolField(item, name, fieldWinners, fresh.results, fresh.calls, older.results, older.calls);
          const rewritten = copyItemTextPresence(item, {
            ...item,
            output: field("output"),
            error: field("error"),
            prevalOnly: field("prevalOnly"),
            exitCode: field("exitCode"),
            completedAt: field("completedAt"),
            status: field("status"),
            outputImages: field("outputImages"),
            raw: field("raw"),
          });
          if (context) {
            recordToolFoldRewrite(context, rewritten, item);
            const absorbed = [...fresh.results, ...older.results];
            if (absorbed.length > 0) context.toolResultFolds.set(rewritten, absorbed);
            // The rewrite's own keep-decisions: each tool field's supplier
            // is the candidate the selection scanned — even when its value
            // equals the base's, the selection names the source, and the
            // retained side covering a value the base first carried must
            // not leave the page's earlier supply standing. A field no
            // candidate supplied keeps the base's own supplier set (the
            // base itself when it is an untouched original carrying the
            // value). The base's non-tool content survives the rewrite
            // verbatim, so its per-field suppliers and identity anchors
            // stand as they were recorded.
            const baseRecord = context.contributions.get(item);
            const tools: Partial<Record<ToolResultField, ReadonlySet<ItemModel>>> = {};
            for (const name of toolResultFields) {
              const winner = fieldWinners[name];
              if (winner !== undefined) {
                tools[name] = new Set<ItemModel>([winner]);
                continue;
              }
              tools[name] =
                baseRecord?.tools[name] ??
                (baseRecord === undefined && item[name] !== undefined ? new Set<ItemModel>([item]) : undefined);
            }
            const nonTool = new Map<string, ReadonlySet<ItemModel>>();
            if (baseRecord !== undefined) {
              for (const [key, suppliers] of baseRecord.nonTool) {
                nonTool.set(key, suppliers);
              }
            } else {
              for (const [key, value] of Object.entries(item)) {
                if (value !== undefined && !toolResultFields.includes(key as ToolResultField)) {
                  nonTool.set(key, new Set<ItemModel>([item]));
                }
              }
            }
            context.contributions.set(rewritten, {
              nonTool,
              tools,
              identity: baseRecord?.identity ?? new Set<ItemModel>([item]),
            });
          }
          items.push(rewritten);
          continue;
        }
      }
      items.push(item);
    }
    if (items.length > 0 || turn.items.length === 0 || turnCoverageFields.some((field) => turn[field] !== undefined)) {
      merged.push({ ...turn, items });
    }
  }
  return merged;
}

const statusRank: Record<string, number> = {
  inProgress: 0,
  completed: 1,
  failed: 1,
};

function mergePageItem(older: ItemModel, newer: ItemModel): ItemModel {
  const textSource = itemTextPresence(newer) === "omitted" && itemTextPresence(older) === "provided" ? older : newer;
  return copyItemTextPresence(
    textSource,
    mergeItemIdentityMetadata(older, {
      ...older,
      ...newer,
      text: textSource.text,
      toolName: newer.toolName ?? older.toolName,
      callId: newer.callId ?? older.callId,
      argumentsJSON: newer.argumentsJSON ?? older.argumentsJSON,
      description: newer.description ?? older.description,
      eventKind: newer.eventKind ?? older.eventKind,
      steeringKind: newer.steeringKind ?? older.steeringKind,
      raw: newer.raw ?? older.raw,
      output: newer.output ?? older.output,
      error: newer.error ?? older.error,
      prevalOnly: newer.prevalOnly ?? older.prevalOnly,
      exitCode: newer.exitCode ?? older.exitCode,
      images: newer.images ?? older.images,
      outputImages: newer.outputImages ?? older.outputImages,
      source: newer.source ?? older.source,
      reasoningSummaries: newer.reasoningSummaries ?? older.reasoningSummaries,
      startedAt: newer.startedAt ?? older.startedAt,
      completedAt: newer.completedAt ?? older.completedAt,
      observedStartedAt: newer.observedStartedAt ?? older.observedStartedAt,
      observedCompletedAt: newer.observedCompletedAt ?? older.observedCompletedAt,
      status:
        newer.status === undefined || (statusRank[newer.status] ?? 0) < (statusRank[older.status ?? ""] ?? 0)
          ? older.status
          : newer.status,
    }),
  );
}

function mergeItemIdentityMetadata(existing: ItemModel, incoming: ItemModel): ItemModel {
  const transcriptKey = incoming.transcriptKey || existing.transcriptKey;
  return copyItemTextPresence(incoming, {
    ...incoming,
    ...(transcriptKey ? { transcriptKey } : {}),
    ...(incoming.position !== undefined || existing.position !== undefined
      ? { position: incoming.position ?? existing.position }
      : {}),
  });
}

// Structural, not ItemModel-only: a wire ThreadItem carries the same
// id/transcriptKey shape, so a caller matching a live wire item against
// folded ItemModels (the mobile store's findFoldedItem) can call this
// directly instead of re-implementing the rule.
export function itemIdentityMatches(
  left: { id: string; transcriptKey?: string },
  right: { id: string; transcriptKey?: string },
): boolean {
  if (left.transcriptKey && right.transcriptKey) {
    return left.transcriptKey === right.transcriptKey;
  }
  return left.id === right.id;
}

function orderedItems(items: ItemModel[]): ItemModel[] {
  return items
    .map((item, index) => ({ item, index }))
    .sort((a, b) => {
      if (a.item.position && b.item.position) {
        return (
          a.item.position.entry - b.item.position.entry ||
          a.item.position.item - b.item.position.item ||
          a.index - b.index
        );
      }
      if (a.item.position) return -1;
      if (b.item.position) return 1;
      return a.index - b.index;
    })
    .map(({ item }) => item);
}

function mergePageItems(older: ItemModel[], newer: ItemModel[], context?: ToolItemMergeContext): ItemModel[] {
  const merged = orderedItems(older);
  for (const current of orderedItems(newer)) {
    const index = merged.findIndex((item) => itemIdentityMatches(item, current));
    if (index === -1) {
      merged.push(current);
    } else {
      const existing = merged[index];
      if (existing) {
        const mergedItem = mergePageItem(existing, current);
        if (context) recordMergedToolItem(context, mergedItem, existing, current);
        merged[index] = mergedItem;
      }
    }
  }
  return orderedItems(reconcileItemDuplicates(orderedItems(merged), context));
}

// One identity, one item. The newer-iteration folds each newer item into the
// first older item it matches, but an alias chain can leave two items of the
// SAME identity in the result: a turn holding a real item under one alias
// and a remembered alias skeleton under another folds the newer side into
// the skeleton while the real item stays beside the fold (the mobile
// store's retained-turn bound injects exactly such alias skeletons). The
// reconciliation is the iteration's own rule applied to its own result: an
// item that identity-matches an earlier item folds into it.
//
// Which side of the fold is "newer" is decided by SOURCE, never by list
// order: an item whose membership includes newer-side inputs keeps its
// fields over a pure older-side sibling no matter where the two sort —
// display order says nothing about freshness, and the untouched sibling is
// exactly the case the iteration leaves behind (the fresh fold happened
// elsewhere). Items of the same participation — both untouched older-side
// wire fragments, or two folds that each drew newer content — keep the
// list's own order as the tiebreak, the same later-wins the iteration
// itself applies. The fold's fragment membership is recorded on the merge
// provenance, so a caller tracking participation still sees every input.
// Participation means PAYLOAD participation: an identity-only input (a
// remembered skeleton, a sparse wire fragment) supplies no field the fold
// keeps, so it never makes an item fresh — an unpositioned skeleton folding
// an older reissue must not tie with the positioned restored item beside it
// and let display order hand the stale text the win.
// Review rounds 17 and 19: the precedence itself is FIELD-scoped. A
// fresh input supplying one payload field (a status-only fragment)
// makes the merged item fresh, but its freshness covers only the fields
// that fresh input supplied: fields the item inherited from older-side
// leaves are older content, and their conflicts with the other
// duplicate's own values resolve in list order — the same later-wins a
// participation tie applies — including when BOTH duplicates carry
// fresh participation, each supplying its own fields
// (reconcileDuplicatesByFields).
function reconcileItemDuplicates(items: ItemModel[], context?: ToolItemMergeContext): ItemModel[] {
  const reconciled: ItemModel[] = [];
  for (const item of items) {
    // A fold can GROW its item's identity — a keyless older alias takes
    // the transcript key of the reissue it folded — so the merged result
    // can identity-match an accumulated candidate the original did not
    // (review round 20): keep folding the merged result against the
    // accumulated candidates until no match remains. Each step consumes
    // one candidate, so the loop always ends.
    let current = item;
    for (;;) {
      const index = reconciled.findIndex((candidate) => itemIdentityMatches(candidate, current));
      if (index === -1) {
        reconciled.push(current);
        break;
      }
      const existing = reconciled[index];
      if (existing === undefined) {
        reconciled.push(current);
        break;
      }
      const existingCarriesFresh = freshParticipates(context, existing);
      const itemCarriesFresh = freshParticipates(context, current);
      const existingIsNewer = existingCarriesFresh && !itemCarriesFresh;
      const olderItem = existingIsNewer ? current : existing;
      const newerItem = existingIsNewer ? existing : current;
      const mergedItem =
        context === undefined
          ? mergePageItem(olderItem, newerItem)
          : reconcileDuplicatesByFields(existing, current, context);
      // The provenance records in LIST order, not freshness order (review
      // round 21): a side's leaves must read, in the tool fold's reversed
      // candidate walk, in the same precedence the per-field resolution
      // used — the later duplicate that won a field is found first.
      // Freshness order here would leave an older call alias ahead of the
      // duplicate that won its output, and a result fragment omitting the
      // field would fold the stale alias's value back in. The fresh/older
      // side each leaf lands on is the leaf's own, unchanged — only the
      // walk order within a side moves.
      if (context) recordMergedToolItem(context, mergedItem, existing, current);
      reconciled.splice(index, 1);
      current = mergedItem;
    }
  }
  return reconciled;
}

// The fields an item's FRESH inputs supplied — the fields its freshness
// actually covers (review round 17). Text counts as supplied only when a
// fresh leaf PROVIDED it (the omitted marker means the wire carried
// none). Every other field counts by the merge's OWN rule for it (review
// rounds 20 and 22): the nullish-fallback fields only when the leaf
// carries a value (null falls through to the older side), the rank-merged
// status only when a fresh leaf's status is the value the merge kept
// (review round 24: a lower-ranked fresh status loses to the older
// alias's, and the inherited value must not claim precedence), and the
// spread-merged fields by property PRESENCE — a fresh leaf's own
// undefined clears the field, and the
// clearing property must claim precedence or a later stale duplicate's
// value survives the reconciliation. An identity-only leaf supplies
// nothing at all — not even its identity (review round 19: a remembered
// skeleton must not claim per-field precedence over a restored item's
// fields) — and an item the context never saw speaks only for itself.
function freshSuppliedFields(context: ToolItemMergeContext, item: ItemModel): ReadonlySet<string> {
  const supplied = new Set<string>();
  // The rank rule: a fresh leaf's status only reaches the merge when it
  // wins the rank chain — an inProgress fragment under a failed alias
  // leaves the alias's failure in place — so the merged status counts as
  // fresh-supplied only when a fresh leaf's own status is what the merge
  // kept (review round 24).
  let statusSupplied = false;
  const record = (leaf: ItemModel): void => {
    if (itemIsIdentityOnly(leaf)) return;
    for (const [key, value] of Object.entries(leaf)) {
      // Text follows the presence marker, not the property — see below.
      if (key === "text") continue;
      if (freshSuppliedNullishFallbackFields.has(key)) {
        // The merge inherits the older side's value when the leaf's is
        // null or undefined, so neither counts as supplied (review
        // round 20).
        if (value !== null && value !== undefined) supplied.add(key);
        continue;
      }
      if (key === "status") {
        if (value !== undefined && value === item.status) statusSupplied = true;
        continue;
      }
      // Every other field is spread-merged by property presence: the
      // leaf's own undefined CLEARS the field, and the clearing property
      // counts as supplied (review round 22).
      supplied.add(key);
    }
    if (itemTextPresence(leaf) === "provided") supplied.add("text");
  };
  const provenance = context.provenance.get(item);
  if (provenance === undefined) {
    record(item);
    if (statusSupplied) supplied.add("status");
    return supplied;
  }
  for (const leaf of membershipLeaves(provenance.fresh)) record(leaf);
  if (statusSupplied) supplied.add("status");
  return supplied;
}

// One duplicate's fresh-supplied fields, masked back onto its item: the
// overlay a per-field reconciliation layers over the plain merge. Text
// masks to the omitted marker when the fresh side did not supply it, so
// the merge's presence rule falls through to the value underneath.
function freshSuppliedOverlay(item: ItemModel, freshSupplied: ReadonlySet<string>): ItemModel {
  const overlay = copyItemTextPresence(item, { ...item });
  for (const key of Object.keys(overlay)) {
    if (key === "text") continue;
    if (!freshSupplied.has(key)) delete (overlay as unknown as Record<string, unknown>)[key];
  }
  if (!freshSupplied.has("text")) return markItemTextOmitted({ ...overlay, text: "" });
  return overlay;
}

// Per-field reconciliation of a duplicate pair (review rounds 17-19):
// each side's fresh inputs supply fields, and a field supplied fresh by
// one side wins over a value the other side merely inherited from older
// inputs — whichever duplicate is earlier in the list, and whether or
// not the other side also carries fresh participation. Fields both
// sides supplied fresh — or neither did — resolve as the plain later-wins
// tiebreak does. Composed through mergePageItem's own rules (nullish
// fallback, status rank, text presence) rather than a key-by-key patch:
// the plain merge first, then each side's fresh-supplied overlay, the
// earlier side's first so a both-fresh conflict lands on the later
// duplicate exactly as the tiebreak decides it.
function reconcileDuplicatesByFields(existing: ItemModel, item: ItemModel, context: ToolItemMergeContext): ItemModel {
  const earlier = freshSuppliedFields(context, existing);
  const later = freshSuppliedFields(context, item);
  if (earlier.size === 0 && later.size === 0) return mergePageItem(existing, item);
  let merged = mergePageItem(existing, item);
  if (earlier.size > 0) merged = mergePageItem(merged, freshSuppliedOverlay(existing, earlier));
  if (later.size > 0) merged = mergePageItem(merged, freshSuppliedOverlay(item, later));
  return merged;
}

// Whether an item's merge membership includes newer-side ("fresh") inputs
// that could supply payload — identity-only participants do not count: the
// retained-turn bound's remembered skeletons, and the wire's own sparse
// identity-only fragments, supply no field the fold can keep, so folding
// one in must not make an item read as fresh. Omitted TEXT alone never
// disqualifies a real item: tool items routinely omit text while carrying
// their current output, arguments, images, and status — payload is what
// matters, and itemIsIdentityOnly is the exact test. The context records an
// entry for every input item at creation, so an untouched item speaks for
// its own side; a folded item speaks for whatever its folds combined. An
// item the context never saw — a callId-fold rewrite, or a merge without a
// context at all — reads as older-side, leaving list order to decide exactly
// as it did before source precedence existed.
function freshParticipates(context: ToolItemMergeContext | undefined, item: ItemModel): boolean {
  if (context === undefined) return false;
  const provenance = context.provenance.get(item);
  return provenance !== undefined && membershipLeaves(provenance.fresh).some((leaf) => !itemIsIdentityOnly(leaf));
}

function turnsShareItemIdentity(left: TurnModel, right: TurnModel): boolean {
  return left.items.some((leftItem) => right.items.some((rightItem) => itemIdentityMatches(leftItem, rightItem)));
}

function turnsMatch(left: TurnModel, right: TurnModel): boolean {
  return left.id === right.id || turnsShareItemIdentity(left, right);
}

function mergePageTurn(older: TurnModel, newer: TurnModel, context?: ToolItemMergeContext): TurnModel {
  return {
    ...older,
    ...newer,
    startedAt: newer.startedAt ?? older.startedAt,
    completedAt: newer.completedAt ?? older.completedAt,
    durationMs: newer.durationMs ?? older.durationMs,
    usage: newer.usage ?? older.usage,
    cost: newer.cost ?? older.cost,
    error: newer.error ?? older.error,
    status:
      newer.status === undefined || (statusRank[newer.status] ?? 0) < (statusRank[older.status ?? ""] ?? 0)
        ? older.status
        : newer.status,
    items: mergePageItems(older.items, newer.items, context),
  };
}

// The escaped /s/{route} fragment the hub serves sha-addressed image bytes
// under. The hub's own stamp (sessionImageURL, output_images.go) escapes the
// wire Thread.sessionId with url.PathEscape — never the stable workspace ref
// (handleSessionImage looks the id up in Past.Find, where a ref like
// local:stable finds nothing, and a stable ref can name a different session
// than the one that served the bytes). encodeURIComponent is the matching
// client-side escape. An empty id names no fetchable route, so the sha branch
// stays dark for it. A session id never carries a slash (identifier's
// base62 UUIDv7), and any foreign ref form the page route cannot serve keeps
// the branch dark too.
export function imageSessionRouteForSession(sessionId: string): string | undefined {
  if (sessionId === "" || sessionId.includes("/")) return undefined;
  return encodeURIComponent(sessionId);
}

// hydrateThread builds a thread's whole model from one read. A v6 read (one
// that names its snapshot) builds versioned history and the overlay; a pre-v6
// read keeps the unversioned turns it always had.
export function hydrateThread(resp: ThreadReadResponse, ref: string, now: number): ThreadModel {
  const fields = threadFields(resp, ref, now);
  const imageSessionRoute = imageSessionRouteForSession(fields.imageSessionId ?? fields.threadId);
  if (!resp.snapshot) {
    return {
      ...fields,
      turns: mergeToolCallsByCallId((resp.thread.turns ?? []).map((turn) => wireToTurnModel(turn, imageSessionRoute))),
    };
  }
  const fresh = splitWireTurns(resp.thread.turns ?? [], imageSessionRoute);
  const generation = resp.requestGeneration ?? 0;
  const history: HistoryState = {
    ...readIdentity(resp),
    appliedGeneration: generation,
    issuedGeneration: generation,
    deferredPages: [],
    turns: mergeHistory([], fresh),
  };
  return withDisplay({ ...fields, turns: [], ...runningTurn(resp.thread) }, history, overlayRecord(resp.overlay));
}

// Every field a read sets except the transcript itself (turns, history,
// overlay and the running turn).
function threadFields(resp: ThreadReadResponse, ref: string, now: number): Omit<ThreadModel, "turns"> {
  const thread = resp.thread;
  // The snapshot's own stamped urls already win per-image (url-first
  // precedence); the route only matters for sha-bearing images that arrived
  // WITHOUT a stamp — replayed input images from a read path that didn't
  // re-stamp, or older-producer frames.
  // stampThreadImageURLs (output_images.go) prefers the wire session id and
  // falls back to the thread id, trimming both (strings.TrimSpace); the
  // client-side rebuild matches it exactly — a whitespace-padded session id
  // must not win the fallback and escape to a /s/%20.../images route the hub
  // would 404 on while the trimmed thread id would have served.
  const imageSessionId = thread.sessionId.trim() || thread.id.trim();
  return {
    ref,
    threadId: thread.id,
    ...(thread.evener.parentRef === undefined ? {} : { parentRef: thread.evener.parentRef }),
    imageSessionId,
    ...(thread.evener.instanceId === undefined ? {} : { instanceId: thread.evener.instanceId }),
    name: thread.name ?? "",
    status: thread.status,
    resumeRequired: thread.evener.resumeRequired ?? false,
    modelProvider: thread.modelProvider,
    // Thread has no separate "model id" field on the wire snapshot — only
    // ModelProvider, which appwire/types.go documents as overloaded to
    // "stay[ing] the model field" for this exact reason. thread/model/changed
    // (below) is what later splits provider and model id apart properly.
    model: thread.modelProvider,
    reasoningEffort: thread.evener.reasoningEffort,
    visionModel: thread.evener.visionModel ?? "",
    askPending: thread.evener.askPending ?? false,
    // Go wire-nullable-array rule: omitempty absent means empty, not missing.
    pendingEscalations: thread.evener.pendingEscalations ?? [],
    activeTurnId: activeTurnIdFromThread(thread),
    queue: thread.evener.queue,
    pendingMutations: thread.evener.pendingMutations ?? [],
    // An absent aggregate means the daemon could not authoritatively read
    // tasks; preserve a present zero so an empty task list stays distinct.
    tasks: thread.evener.tasks ?? null,
    ...(thread.evener.diagnostics
      ? { diagnostics: { plugins: thread.evener.diagnostics.plugins?.map((plugin) => ({ name: plugin.name })) } }
      : {}),
    delegates: (thread.evener.diagnostics?.delegates ?? []).map(cloneStableDelegate),
    skills: (thread.evener.diagnostics?.skills ?? []).map((skill) => ({ ...skill })),
    turnSlots: thread.evener.diagnostics?.turnSlots ? { ...thread.evener.diagnostics.turnSlots } : null,
    jobsUpdatedAt: null,
    jobsTreeRevision: null,
    olderCursor: resp.olderCursor,
    lastFrameAt: now,
    capabilities: thread.evener.capabilities,
    capabilitySource: "read",
    goal: thread.evener.goal ?? null,
    humanNote: thread.evener.humanNote ?? "",
    agentNote: thread.evener.agentNote ?? "",
    // Go wire-nullable-array rule: omitempty absent means empty, not missing.
    sessionUrls: thread.evener.sessionUrls ?? [],
    contextUsed: thread.evener.contextUsed ?? 0,
    contextWindow: thread.evener.contextWindow ?? 0,
    contextPressure: thread.evener.contextPressure ?? 0,
    usage: thread.evener.usage ?? null,
    cost: thread.evener.cost ?? null,
    // Passed straight through, undefined and all: absent is "nobody counted"
    // and must not become a 0 that reads as "nothing failed".
    failedToolCalls: thread.evener.failedToolCalls,
    workMillis: thread.evener.workMillis ?? 0,
    activeTurnStartedAt: epochMsToISO(thread.evener.activeTurnStartedAt),
    reasoningEffortLevels: thread.evener.reasoningEffortLevels ?? [],
    supportsReasoning: thread.evener.supportsReasoning ?? false,
    cwd: thread.cwd,
    gitBranch: thread.gitInfo?.branch,
    projectPath: thread.projectPath,
    createdAt: epochSecondsToISO(thread.createdAt),
    updatedAt: epochSecondsToISO(thread.updatedAt),
  };
}

export function collectAuthoritativeMutationIds(resp: ThreadReadResponse): Set<string> {
  const identities = new Set<string>();
  for (const pending of resp.thread.evener.pendingMutations ?? []) identities.add(pending.clientMutationId);
  for (const clientMutationId of resp.thread.evener.queue.clientMutationIds ?? []) identities.add(clientMutationId);
  for (const turn of resp.thread.turns ?? []) {
    for (const item of turn.items ?? []) {
      if (item.clientMutationId) identities.add(item.clientMutationId);
    }
  }
  return identities;
}

export function prependOlderTurns(model: ThreadModel, resp: ThreadTurnsListResponse): ThreadModel {
  return mergeOlderItemPage(model, resp);
}

type TurnFragment = {
  turn: TurnModel;
  source: "older" | "fresh";
  index: number;
  order: number;
};

type TurnFragmentGroup = {
  fragments: TurnFragment[];
  firstOrder: number;
};

type CoalescedTurn = {
  turn: TurnModel;
  olderIndexes: number[];
  freshIndexes: number[];
};

function coalesceTurnFragments(
  older: TurnModel[],
  fresh: TurnModel[],
  context?: ToolItemMergeContext,
): CoalescedTurn[] {
  const groups: TurnFragmentGroup[] = [];
  const add = (turn: TurnModel, source: TurnFragment["source"], index: number, order: number): void => {
    const fragment = { turn, source, index, order } satisfies TurnFragment;
    const matching = groups.filter((group) =>
      group.fragments.some((existing) => turnsMatch(existing.turn, fragment.turn)),
    );
    const target = matching[0];
    if (target === undefined) {
      groups.push({ fragments: [fragment], firstOrder: order });
      return;
    }
    target.fragments.push(fragment);
    for (const group of matching.slice(1)) target.fragments.push(...group.fragments);
    for (const group of matching.slice(1).reverse()) {
      const index = groups.indexOf(group);
      if (index !== -1) groups.splice(index, 1);
    }
    target.fragments.sort((left, right) => left.order - right.order);
    target.firstOrder = target.fragments[0]?.order ?? target.firstOrder;
  };

  older.forEach((turn, index) => {
    add(turn, "older", index, index);
  });
  fresh.forEach((turn, index) => {
    add(turn, "fresh", index, older.length + index);
  });

  return groups
    .sort((left, right) => left.firstOrder - right.firstOrder)
    .flatMap((group) => {
      const olderFragments = group.fragments.filter((fragment) => fragment.source === "older");
      const freshFragments = group.fragments.filter((fragment) => fragment.source === "fresh");
      let turn: TurnModel;
      if (olderFragments.length === 0) {
        const firstFresh = freshFragments[0]?.turn;
        if (firstFresh === undefined) return [];
        turn = freshFragments
          .slice(1)
          .reduce((current, fragment) => mergePageTurn(current, fragment.turn, context), firstFresh);
      } else {
        const firstOlder = olderFragments[0]?.turn;
        if (firstOlder === undefined) return [];
        const mergedOlder = olderFragments
          .slice(1)
          .reduce((current, fragment) => mergePageTurn(current, fragment.turn, context), firstOlder);
        turn = freshFragments.reduce(
          (current, fragment) => mergePageTurn(current, fragment.turn, context),
          mergedOlder,
        );
      }
      return [
        {
          turn,
          olderIndexes: olderFragments.map((fragment) => fragment.index),
          freshIndexes: freshFragments.map((fragment) => fragment.index),
        },
      ];
    });
}

function firstTurnPosition(turn: TurnModel): NonNullable<ItemModel["position"]> | undefined {
  return turn.items.reduce<NonNullable<ItemModel["position"]> | undefined>((first, item) => {
    if (item.position === undefined) return first;
    if (first === undefined) return item.position;
    return item.position.entry < first.entry || (item.position.entry === first.entry && item.position.item < first.item)
      ? item.position
      : first;
  }, undefined);
}

function compareTurnPositions(left: TurnModel, right: TurnModel): number | undefined {
  const leftPosition = firstTurnPosition(left);
  const rightPosition = firstTurnPosition(right);
  if (leftPosition === undefined || rightPosition === undefined) return undefined;
  return leftPosition.entry - rightPosition.entry || leftPosition.item - rightPosition.item;
}

function nextPositionedTurn(turns: CoalescedTurn[], start: number): CoalescedTurn | undefined {
  return turns.slice(start).find((turn) => firstTurnPosition(turn.turn) !== undefined);
}

function weaveTurnGap(
  fresh: CoalescedTurn[],
  older: CoalescedTurn[],
  preferOlderWithoutPositions: boolean,
): CoalescedTurn[] {
  const result: CoalescedTurn[] = [];
  let olderIndex = 0;
  for (const [freshIndex, freshTurn] of fresh.entries()) {
    const freshComparisonTurn =
      firstTurnPosition(freshTurn.turn) === undefined ? nextPositionedTurn(fresh, freshIndex) : freshTurn;
    while (olderIndex < older.length) {
      const olderTurn = older[olderIndex];
      if (olderTurn === undefined) break;
      const comparisonTurn =
        firstTurnPosition(olderTurn.turn) === undefined ? nextPositionedTurn(older, olderIndex) : olderTurn;
      const comparison =
        comparisonTurn === undefined || freshComparisonTurn === undefined
          ? undefined
          : compareTurnPositions(comparisonTurn.turn, freshComparisonTurn.turn);
      if (comparison !== undefined ? comparison < 0 : preferOlderWithoutPositions) {
        result.push(olderTurn);
        olderIndex += 1;
        continue;
      }
      break;
    }
    result.push(freshTurn);
  }
  result.push(...older.slice(olderIndex));
  return result;
}

function placeCoalescedTurns(groups: CoalescedTurn[], olderCount: number): TurnModel[] {
  const fresh = groups
    .filter((group) => group.freshIndexes.length > 0)
    .sort((left, right) => (left.freshIndexes[0] ?? 0) - (right.freshIndexes[0] ?? 0));
  const retained = groups
    .filter((group) => group.freshIndexes.length === 0)
    .sort((left, right) => (left.olderIndexes[0] ?? 0) - (right.olderIndexes[0] ?? 0));
  const oldAnchorFreshIndexes = new Array<number>(olderCount).fill(-1);
  for (const [freshIndex, group] of fresh.entries()) {
    for (const olderIndex of group.olderIndexes) {
      oldAnchorFreshIndexes[olderIndex] = freshIndex;
    }
  }

  const retainedByFreshGap = new Map<number, CoalescedTurn[]>();
  for (const group of retained) {
    const olderIndex = group.olderIndexes[0];
    if (olderIndex === undefined) continue;
    // Coalesced fresh groups can consume noncontiguous older anchors. Keep the
    // retained gaps moving forward by the greatest fresh rank seen so far,
    // then choose the earliest later rank that remains compatible with it.
    const previousAnchor = oldAnchorFreshIndexes
      .slice(0, olderIndex)
      .reduce((greatest, freshIndex) => Math.max(greatest, freshIndex), -1);
    const nextCompatibleAnchor =
      previousAnchor === -1
        ? undefined
        : oldAnchorFreshIndexes
            .slice(olderIndex + 1)
            .reduce<number | undefined>(
              (earliest, freshIndex) =>
                freshIndex > previousAnchor && (earliest === undefined || freshIndex < earliest)
                  ? freshIndex
                  : earliest,
              undefined,
            );
    const gap = previousAnchor === -1 ? 0 : (nextCompatibleAnchor ?? fresh.length);
    const run = retainedByFreshGap.get(gap) ?? [];
    run.push(group);
    retainedByFreshGap.set(gap, run);
  }

  const anchors = fresh.flatMap((group, index) => (group.olderIndexes.length > 0 ? [index] : []));
  const boundaries = [-1, ...anchors, fresh.length];
  const result: CoalescedTurn[] = [];
  for (let boundaryIndex = 0; boundaryIndex < boundaries.length - 1; boundaryIndex += 1) {
    const previousAnchor = boundaries[boundaryIndex];
    const nextAnchor = boundaries[boundaryIndex + 1];
    if (previousAnchor === undefined || nextAnchor === undefined) continue;
    const gapRun = retainedByFreshGap.get(previousAnchor === -1 ? 0 : nextAnchor) ?? [];
    result.push(...weaveTurnGap(fresh.slice(previousAnchor + 1, nextAnchor), gapRun, previousAnchor === -1));
    if (nextAnchor < fresh.length) {
      const anchorTurn = fresh[nextAnchor];
      if (anchorTurn !== undefined) result.push(anchorTurn);
    }
  }
  return result.map((group) => group.turn);
}

export interface TurnHistoryMergeResult {
  // When olderCoverage is false, this can still include older local
  // observations; when it is true, it includes persisted transcript fields.
  turns: TurnModel[];
  // Coverage is persisted transcript evidence. Local observations can still
  // be folded into turns while this remains false.
  olderCoverage: boolean;
  // A turn id alone is not overlap evidence; a non-warning item identity is.
  transcriptOverlap: boolean;
}

// The merge's own fragment membership, turn-level and item-level, so a
// caller can follow content to where the merge actually put it:
// - olderTurnFolds / newerTurnFolds map each returned turn id to the ids of
//   that side's input turns that coalesced into it. Coalescing is
//   transitive — turnsMatch chains through shared item identities, and
//   mergePageItems folds item aliases into a final identity neither
//   original carried — so an input turn's content can land in an output
//   turn whose items match none of its identities, and only the LAST
//   fragment of a group survives under its own id (a page fragment can
//   bridge two turns of the same side, so neither of the two survives
//   under its own). Membership, not final-identity matching, is the
//   authoritative answer to "which returned turn carries this input turn's
//   content" (the mobile store's compact-turn ownership transfer reads it
//   exactly that way, older side at rehydrate and newer side at loadOlder).
//   A fold's output id can be absent from turns — the fold drops a group
//   turn it emptied of removable results, and a merge whose older side
//   contributed nothing returns the newer side unchanged — so a caller
//   must treat a fold whose output turn is missing as having no carrier.
// - itemFoldSources names, for every item the merge BUILT through an
//   identity-match fold, the original input items it combined (itself for
//   items no fold touched). A merged item can settle on an identity one of
//   its inputs never carried, so participation — which inputs folded into
//   an item — is the authoritative test of what it descends from, not the
//   final identity (the strip pass of the mobile store's retained-turn
//   bound reads it exactly that way). The tool-result fold records no
//   membership: it rewrites calls in place from candidates by callId, which
//   is a different mechanism with its own participation rule — but the
//   results it absorbs onto a rewritten call are exposed separately
//   (toolResultFoldSources), because a retention consumer must treat the
//   call as backing its absorbed results' rows without any other consumer
//   starting to read call-precedence candidates as fold sources.
export interface TurnHistoryFoldDetail extends TurnHistoryMergeResult {
  olderTurnFolds: ReadonlyMap<string, readonly string[]>;
  newerTurnFolds: ReadonlyMap<string, readonly string[]>;
  itemFoldSources: (item: ItemModel) => readonly ItemModel[];
  toolResultFoldSources: (item: ItemModel) => readonly ItemModel[];
  // Whether the inputs `side` names contributed content the item carries
  // — read from the keep-decisions the fold edges recorded where they
  // happened, never re-derived here (RoboRev round 5): the supplier sets
  // per surviving field (a side counts for a field only when it holds
  // every supplier — removing it loses the field) and the identity
  // anchors (a side holding them all owns the item outright), the
  // rewrite's winners included. An item no edge folded vouches for
  // itself.
  itemSideContributes: (item: ItemModel, side: (input: ItemModel) => boolean) => boolean;
  // The merge's own coverage verdict for one older-input item: whether its
  // persisted content adds coverage the fresh side lacks, judged through
  // this merge's live view — the same host, contributor-chain, rank, and
  // fold-survival rules the merge's own coverage walk runs per turn, never
  // a re-derivation after the fact. Warnings never claim (the walk's own
  // rule); an item this merge never saw as an older input claims (true),
  // the safe direction for a caller gating on the answer. Published for the
  // mobile store's alias-consumption gate (#2152): the store consumes this
  // answer instead of re-deriving the field rules store-side.
  olderItemAddsCoverage: (item: ItemModel) => boolean;
}

const turnCoverageFields = ["startedAt", "completedAt", "durationMs", "usage", "cost", "error"] as const;
const itemIdentityFields = new Set(["id", "turnId", "transcriptKey", "clientMutationId"]);
const itemNonCoverageFields = new Set([
  ...itemIdentityFields,
  "pendingText",
  "reasoningSummaries",
  "warning",
  "observedStartedAt",
  "observedCompletedAt",
]);

// The item fields the page merge preserves through `??` — mergePageItem's
// per-field list, plus position, which mergeItemIdentityMetadata keeps the
// same way: a null or undefined on the fresh side falls through to the older
// side's value, so a fresh nullish cannot hide older data the merged result
// keeps, and an older nullish carries no data of its own. Every other field
// the coverage walk reaches is spread-merged ({ ...older, ...newer }; the
// fresh side's own property wins, even when its value is undefined), where
// the property's presence — read below — is the absence marker.
const itemNullishMergedFields = new Set([
  "position",
  "toolName",
  "callId",
  "argumentsJSON",
  "description",
  "eventKind",
  "steeringKind",
  "raw",
  "output",
  "error",
  "prevalOnly",
  "exitCode",
  "images",
  "outputImages",
  "source",
  "startedAt",
  "completedAt",
]);

// The fields whose null OR undefined falls through to the older side in
// the item merge — mergePageItem's nullish-fallback list above, plus the
// fields that list leaves to their own helpers (reasoning summaries and
// the observed timing fields) — so a fresh leaf supplies one only by
// carrying a value (freshSuppliedFields reads exactly this).
const freshSuppliedNullishFallbackFields = new Set([
  ...itemNullishMergedFields,
  "reasoningSummaries",
  "observedStartedAt",
  "observedCompletedAt",
]);

// What "absent" means for coverage follows each field's merge rule: every
// turnCoverageFields member merges with `??` in mergePageTurn (nullishMerged
// is true there for that reason), so null counts as absent for them exactly
// as it does for the item fields above; spread-merged fields keep undefined as
// their only absence marker.
function absentForCoverage(value: unknown, nullishMerged: boolean): boolean {
  return value === undefined || (nullishMerged && value === null);
}

function sameModelFields(left: object, right: object): boolean {
  const keys = new Set([...Object.keys(left), ...Object.keys(right)]);
  return [...keys].every((key) => {
    const leftValue = (left as Record<string, unknown>)[key];
    const rightValue = (right as Record<string, unknown>)[key];
    return leftValue === rightValue || (leftValue === undefined && rightValue === undefined);
  });
}

function olderItemContributes(older: ItemModel, newer: ItemModel): boolean {
  const merged = mergePageItem(older, newer);
  return !sameModelFields(merged, newer) || itemTextPresence(merged) !== itemTextPresence(newer);
}

// The fold (mergeToolCallsByCallId) removes an older tool RESULT from the
// merged items entirely, folding its toolResultFields into the surviving call
// by source precedence; every other field it carried goes with it. A result
// whose every foldable field the fresh side already supplies is therefore
// fully superseded — the merged output retains nothing from it, so it must
// not claim persisted coverage. The fold removes a result only when a CALL
// item for its callId survives in the placed turns — identity coalescing can
// merge an older call into a fresh result, leaving no call behind, and the
// result then survives as its own item. A result with no fresh counterpart
// for its callId, with or without that call, keeps its fields on its own item
// or on the surviving older call. Both still claim. The call ids and fresh
// candidates both come from the fold's own view of the placed turns, so the
// supersession asks exactly what the fold will do.
function fullySupersededToolResult(older: ItemModel, view: ToolFoldView) {
  if (older.callId === undefined || !isToolResultId(older.id)) return false;
  if (!view.callIds.has(older.callId)) return false;
  const fresh = view.fresh.get(older.callId);
  if (fresh === undefined) return false;
  for (const field of toolResultFields) {
    const value = (older as unknown as Record<string, unknown>)[field];
    if (value === undefined) continue;
    const freshSupplies = [...fresh.results, ...fresh.calls].some(
      (item) => (item as unknown as Record<string, unknown>)[field] !== undefined,
    );
    if (!freshSupplies) return false;
  }
  return true;
}

// Coverage reports persisted transcript history the returned turns actually
// keep, and the fold runs after the claims do. Each gated claim therefore
// proves its older data survives the fold, through the fold's own view of
// the placed turns:
// - Turn fields only ever land on the merged group turn, never on another
//   turn, and the fold drops a turn it has emptied of items unless it was
//   already empty or carries canonical turn metadata — a claim riding a
//   dropped turn reports history the merge throws away.
// - The fold removes a tool RESULT whose call survives elsewhere, carrying
//   only its toolResultFields onto the surviving call by fresh-first
//   precedence. A claimed fallback field outside toolResultFields dies with
//   the removed result; a toolResultField survives only while no fresh
//   candidate for that call id supplies a value of its own.
function turnSurvivesFold(turn: TurnModel, view: ToolFoldView): boolean {
  return (
    view.noOp ||
    turn.items.length === 0 ||
    turnCoverageFields.some((field) => turn[field] !== undefined) ||
    turn.items.some((item) => !isFoldedResult(item, view))
  );
}

function isFoldedResult(item: ItemModel, view: ToolFoldView): boolean {
  return !view.noOp && item.callId !== undefined && isToolResultId(item.id) && view.callIds.has(item.callId);
}

function foldRewritesCall(item: ItemModel, view: ToolFoldView): boolean {
  return !view.noOp && item.callId !== undefined && isToolCallId(item.id) && view.resultCallIds.has(item.callId);
}

function isToolResultField(field: string): boolean {
  return (toolResultFields as readonly string[]).includes(field);
}

function freshSuppliesToolField(callId: string, field: string, view: ToolFoldView): boolean {
  const candidates = view.fresh.get(callId);
  if (candidates === undefined) return false;
  return [...candidates.results, ...candidates.calls].some(
    (item) => (item as unknown as Record<string, unknown>)[field] !== undefined,
  );
}

// Walks a merge-provenance membership tree to the original items at its
// leaves — the merge records membership at every identity-match edge, so the
// leaves are exactly the inputs a merged item combined.
function membershipLeaves(membership: ToolItemSourceMembership | undefined, leaves: ItemModel[] = []): ItemModel[] {
  if (membership === undefined) return leaves;
  if ("item" in membership) {
    leaves.push(membership.item);
    return leaves;
  }
  membershipLeaves(membership.left, leaves);
  membershipLeaves(membership.right, leaves);
  return leaves;
}

function membershipHasLeaf(membership: ToolItemSourceMembership | undefined, leaf: ItemModel): boolean {
  if (membership === undefined) return false;
  if ("item" in membership) return membership.item === leaf;
  return membershipHasLeaf(membership.left, leaf) || membershipHasLeaf(membership.right, leaf);
}

// Status merges by rank (mergePageItem and mergePageTurn), not by
// later-wins: a later contributor's status only takes over when it is
// defined and not LOWER-ranked than what came before, so an older completed
// status outlives a later inProgress one. The chain walks the later
// contributors in merge order — once any of them takes over, the older
// value is gone for good.
function statusOutlives(base: string | undefined, later: readonly { status?: string }[]): boolean {
  const status = base;
  for (const contributor of later) {
    const next = contributor.status;
    if (next === undefined || (statusRank[next] ?? 0) < (statusRank[status ?? ""] ?? 0)) continue;
    return false;
  }
  return true;
}

// The merged item in the group turn hosting the older item's data. Identity
// alone stops at the first hop: coalescing can chain an older item through a
// fresh alias into a second fresh item that no longer matches the original
// identity (older --id--> fresh1 --transcriptKey--> fresh2), so the merge
// provenance is the authoritative membership test; identity is the fallback
// where no context recorded one.
function matchedItemHost(older: ItemModel, groupTurn: TurnModel, context?: ToolItemMergeContext): ItemModel {
  const hosting = groupTurn.items.find(
    (item) => item !== older && membershipHasLeaf(context?.provenance.get(item)?.older, older),
  );
  if (hosting !== undefined) return hosting;
  return groupTurn.items.find((item) => item === older || itemIdentityMatches(item, older)) ?? older;
}

// Every fresh item that merged into the host — the direct identity matches
// plus whatever an alias chain pulled in after them. The merged field value
// is the last fresh contributor's that supplies it, so the claim checks every
// contributor, not just the items matching the original older identity.
function freshContributorItems(
  host: ItemModel,
  matches: TurnModel[],
  older: ItemModel,
  context?: ToolItemMergeContext,
): ItemModel[] {
  const viaProvenance = context ? membershipLeaves(context.provenance.get(host)?.fresh) : [];
  if (viaProvenance.length > 0) return viaProvenance;
  return matches.flatMap((turn) => turn.items.filter((item) => itemIdentityMatches(item, older)));
}

// Every item the merge combined into the host, in merge order: a group turn
// folds its older fragments before its fresh ones, and every provenance edge
// keeps the earlier side on the left, so the leaves read in the order the
// merges applied. A contributor can only discard a field for everything
// merged before it, so a claim survives coalescing exactly while every LATER
// contributor leaves the field alone — a fresh item supplying it, or a later
// older fragment owning the property with an explicit undefined, both erase
// the value the earlier item claimed.
function contributorChain(
  host: ItemModel,
  older: ItemModel,
  matches: ItemModel[],
  context?: ToolItemMergeContext,
): ItemModel[] {
  const olderLeaves = context ? membershipLeaves(context.provenance.get(host)?.older) : [];
  const freshLeaves = context ? membershipLeaves(context.provenance.get(host)?.fresh) : [];
  if (olderLeaves.length === 0 && freshLeaves.length === 0) return [older, ...matches];
  return [...olderLeaves, ...freshLeaves];
}

// Whether a claimed field on a matched item outlives the fold. The host keeps
// every field when the fold neither removes it nor rewrites it as a call; a
// rewritten call keeps non-toolResultFields through the spread and takes
// toolResultFields from the candidates (fresh first); a removed result keeps
// only toolResultFields, and again only while the fresh side supplies none.
function matchedFieldSurvivesFold(field: string, host: ItemModel, view: ToolFoldView): boolean {
  if (host.callId === undefined) return true;
  if (isFoldedResult(host, view)) {
    return isToolResultField(field) && !freshSuppliesToolField(host.callId, field, view);
  }
  if (!isToolResultField(field)) return true;
  return !foldRewritesCall(host, view) || !freshSuppliesToolField(host.callId, field, view);
}

function olderItemAddsCoverage(
  older: ItemModel,
  matches: ItemModel[],
  groupTurn: TurnModel,
  view: ToolFoldView,
  context?: ToolItemMergeContext,
): boolean {
  if (matches.length === 0) {
    // An item with no fresh match may still have coalesced with another older
    // fragment's item, and the fold judges the merged host, not the original:
    // an older call folded into a shared-transcriptKey result is removable
    // exactly like that result is, fields and all.
    const host = matchedItemHost(older, groupTurn, context);
    return !fullySupersededToolResult(host, view);
  }
  const host = matchedItemHost(older, groupTurn, context);
  const chain = contributorChain(host, older, matches, context);
  const selfIndex = chain.indexOf(older);
  const later = selfIndex === -1 ? chain : chain.slice(selfIndex + 1);
  if (itemTextPresence(older) === "provided" && later.every((item) => itemTextPresence(item) === "omitted")) {
    // The fold carries no text onto a surviving call: the older text counts
    // only while the merged item hosting it survives. A discarded result can
    // still contribute surviving fields, so keep walking instead of returning.
    if (matchedFieldSurvivesFold("text", host, view)) return true;
  }
  return Object.keys(older).some((field) => {
    if (itemNonCoverageFields.has(field)) return false;
    const nullishMerged = itemNullishMergedFields.has(field);
    // Status merges by rank, so its claim follows the rank chain above rather
    // than treating any defined later status as superseding. Every other field
    // the walk reaches outside the ?? list merges by spread ({ ...older, ...newer }:
    // the later side's own property wins, even when its value is undefined),
    // so a later contributor that OWNS the property blocks the claim however
    // undefined its value reads — the spread discards the older value, and
    // coverage must not report history the merge throws away.
    const presenceMerged = !nullishMerged && field !== "status";
    const olderValue = (older as unknown as Record<string, unknown>)[field];
    if (absentForCoverage(olderValue, nullishMerged)) return false;
    const freshLacks =
      field === "status"
        ? statusOutlives(older.status, later)
        : later.every((item) => {
            if (presenceMerged) return !Object.hasOwn(item, field);
            return absentForCoverage((item as unknown as Record<string, unknown>)[field], nullishMerged);
          });
    return freshLacks && matchedFieldSurvivesFold(field, host, view);
  });
}

function olderTurnAddsCoverage(
  older: TurnModel,
  matches: TurnModel[],
  laterTurns: TurnModel[],
  groupTurn: TurnModel,
  view: ToolFoldView,
  context?: ToolItemMergeContext,
): boolean {
  if (
    // The canonical-fields check stays ahead of the unmatched early return:
    // an unmatched turn carrying persisted fields still claims even when its
    // items cannot (naming-trap regression: a warning-only turn with usage).
    turnCoverageFields.some(
      (field) =>
        !absentForCoverage(older[field], true) && matches.every((turn) => absentForCoverage(turn[field], true)),
    )
  ) {
    // The claimed turn fields ride the merged group turn, which the fold
    // drops once it has folded every item away unless it was already empty
    // or carries canonical turn metadata — usage on a turn the fold drops
    // never reaches the returned history. The turn's items can still
    // contribute data the fold carries onto a surviving call, so keep
    // checking instead of returning.
    if (turnSurvivesFold(groupTurn, view)) return true;
  }
  if (matches.length > 0 && statusOutlives(older.status, laterTurns)) {
    // Turn status merges by rank (mergePageTurn) exactly as item status does:
    // a matched older turn's completed outlives fresh inProgress fragments and
    // the group turn keeps that persisted state. Unmatched retained turns
    // keep their status with the turn itself and claim through their items or
    // canonical fields instead. The status rides the group turn, so it is
    // gated on the same fold survival — and keeps walking when the gate fails.
    if (turnSurvivesFold(groupTurn, view)) return true;
  }
  // The fold is global across turns: an older result in a turn that matches
  // nothing can still be superseded by a fresh call living in another turn,
  // so the unmatched-turn claim runs the same per-item check instead of
  // taking every non-warning item on faith.
  if (matches.length === 0) {
    return (
      (older.items.length === 0 && turnSurvivesFold(groupTurn, view)) ||
      older.items.some((item) => item.type !== "warning" && olderItemAddsCoverage(item, [], groupTurn, view, context))
    );
  }
  return older.items.some((olderItem) => {
    return olderItemCoverageClaim(olderItem, groupTurn, matches, view, context);
  });
}

// The per-item claim both the per-turn walk above and the merge-bound
// reader (mergeTurnHistoryWithContext's olderItemAddsCoverageOf) run: the
// item's merged host and its fresh contributor chain, judged through the
// merge's own live view. Warnings never claim — the walk's own rule,
// owned here so every caller inherits it.
function olderItemCoverageClaim(
  item: ItemModel,
  groupTurn: TurnModel,
  freshTurns: TurnModel[],
  view: ToolFoldView,
  context?: ToolItemMergeContext,
): boolean {
  if (item.type === "warning") return false;
  const host = matchedItemHost(item, groupTurn, context);
  const matchingItems = freshContributorItems(host, freshTurns, item, context);
  return olderItemAddsCoverage(item, matchingItems, groupTurn, view, context);
}

function foldTurnFragments(turns: TurnModel[], context?: ToolItemMergeContext): TurnModel | undefined {
  const first = turns[0];
  return first === undefined
    ? undefined
    : turns.slice(1).reduce((current, turn) => mergePageTurn(current, turn, context), first);
}

function olderTurnContributes(merged: TurnModel, fresh: TurnModel): boolean {
  for (const field of ["id", "status", ...turnCoverageFields] as const) {
    if (merged[field] !== fresh[field]) return true;
  }
  return merged.items.some((item) => {
    const freshItem = fresh.items.find((candidate) => itemIdentityMatches(candidate, item));
    return freshItem === undefined || olderItemContributes(item, freshItem);
  });
}

function mergeTurnHistoryWithContext(
  older: TurnModel[],
  newer: TurnModel[],
  context?: ToolItemMergeContext,
): TurnHistoryFoldDetail {
  const groups = coalesceTurnFragments(older, newer, context);
  const olderTurnFolds = new Map<string, readonly string[]>();
  const newerTurnFolds = new Map<string, readonly string[]>();
  for (const group of groups) {
    if (group.olderIndexes.length > 0) {
      olderTurnFolds.set(
        group.turn.id,
        group.olderIndexes.flatMap((index): string[] => {
          const id = older[index]?.id;
          return id === undefined ? [] : [id];
        }),
      );
    }
    if (group.freshIndexes.length > 0) {
      newerTurnFolds.set(
        group.turn.id,
        group.freshIndexes.flatMap((index): string[] => {
          const id = newer[index]?.id;
          return id === undefined ? [] : [id];
        }),
      );
    }
  }
  let olderContributed = false;
  let olderCoverage = false;
  let transcriptOverlap = false;
  // Coverage must only report older history the fold keeps, so the claims
  // below read the fold's own view of the turns they will return.
  const placed = placeCoalescedTurns(groups, older.length);
  const view = toolFoldView(placed, context);
  // The per-item inputs of the coverage verdicts below, so the fold detail
  // can answer the same question for one item on demand: the group turn and
  // the group's fresh turns are the walk's own arguments, and the chain
  // (host, fresh contributors) is exactly what the per-turn walk computes
  // for each item it reaches. Item objects flow into the groups by
  // reference, so a caller holding an older input item asks about the very
  // object this index names.
  const olderItemCoverageInputs = new WeakMap<ItemModel, { groupTurn: TurnModel; freshTurns: TurnModel[] }>();
  for (const group of groups) {
    const freshTurns = group.freshIndexes.flatMap((index) => (newer[index] === undefined ? [] : [newer[index]]));
    for (const [position, olderIndex] of group.olderIndexes.entries()) {
      const turn = older[olderIndex];
      if (turn === undefined) continue;
      for (const item of turn.items) {
        olderItemCoverageInputs.set(item, { groupTurn: group.turn, freshTurns });
      }
      if (
        turn.items.some(
          (item) =>
            item.type !== "warning" &&
            freshTurns.some((candidate) => candidate.items.some((next) => itemIdentityMatches(item, next))),
        )
      ) {
        transcriptOverlap = true;
      }
      // A group turn folds its older fragments before its fresh ones, so the
      // turns merged after this one are the later older fragments followed by
      // every fresh fragment — the rank chain a turn-status claim walks.
      const laterTurns = [
        ...group.olderIndexes.slice(position + 1).flatMap((index): TurnModel[] => {
          const laterTurn = older[index];
          return laterTurn === undefined ? [] : [laterTurn];
        }),
        ...freshTurns,
      ];
      if (olderTurnAddsCoverage(turn, freshTurns, laterTurns, group.turn, view, context)) olderCoverage = true;
    }

    if (group.olderIndexes.length === 0) continue;
    if (group.freshIndexes.length === 0) {
      olderContributed = true;
      continue;
    }
    const fresh = foldTurnFragments(freshTurns, context);
    if (fresh === undefined || olderTurnContributes(group.turn, fresh)) olderContributed = true;
  }

  // The store's alias-consumption gate (#2152) asks the package's own
  // answer for one retained item: whether its persisted content adds
  // coverage the fresh side lacks, judged through THIS merge's live view —
  // the same host, contributor-chain, and fold-survival rules the walk
  // above runs per turn, through the very function it calls — never a
  // re-derivation after the fact. Warnings never claim (the claim's own
  // rule), and an item this merge never saw as an older input claims —
  // the safe direction for a caller gating on the answer.
  const olderItemAddsCoverageOf = (item: ItemModel): boolean => {
    const input = olderItemCoverageInputs.get(item);
    if (input === undefined) return true;
    return olderItemCoverageClaim(item, input.groupTurn, input.freshTurns, view, context);
  };

  return {
    turns: olderContributed ? mergeToolCallsByCallId(placed, context, view) : newer,
    olderCoverage,
    transcriptOverlap,
    olderTurnFolds,
    newerTurnFolds,
    itemFoldSources: itemFoldSourcesOf(context),
    toolResultFoldSources: toolResultFoldSourcesOf(context),
    itemSideContributes: itemSideContributesOf(context),
    olderItemAddsCoverage: olderItemAddsCoverageOf,
  };
}

export function mergeTurnHistory(older: TurnModel[], newer: TurnModel[]): TurnHistoryMergeResult {
  return mergeTurnHistoryWithContext(older, newer, createToolItemMergeContext(newer, older));
}

// The same merge carrying its turn-level fragment membership, for callers
// that must follow an older turn's content to the output turn that holds it
// even through alias chains that leave the merged items matching none of the
// input's identities.
export function mergeTurnHistoryWithFolds(older: TurnModel[], newer: TurnModel[]): TurnHistoryFoldDetail {
  return mergeTurnHistoryWithContext(older, newer, createToolItemMergeContext(newer, older));
}

// The item-level view of the merge membership: the original input items a
// merged item combined, itself for untouched items. Without a context no
// fold recorded membership, so every item vouches only for itself.
function itemFoldSourcesOf(context?: ToolItemMergeContext): (item: ItemModel) => readonly ItemModel[] {
  return context === undefined
    ? (item) => [item]
    : (item) => {
        const provenance = context.provenance.get(item);
        if (provenance === undefined) return [item];
        return [...membershipLeaves(provenance.older), ...membershipLeaves(provenance.fresh)];
      };
}

// The results the tool fold absorbed onto a rewritten call — empty for
// every other item. Deliberately separate from itemFoldSources (see the
// folds' doc above): only retention consumers read it.
function toolResultFoldSourcesOf(context?: ToolItemMergeContext): (item: ItemModel) => readonly ItemModel[] {
  return context === undefined ? () => [] : (item) => context.toolResultFolds.get(item) ?? [];
}

// The fold's answer to whether the inputs `side` names contributed content
// the merged item carries: the keep-decisions the fold edges recorded —
// the supplier sets per surviving field and the identity anchors, the
// rewrite's winners included. A side holding every identity anchor owns
// the item outright — the fold would not hold it at all without them —
// and otherwise a field counts for the side only when the side is
// necessary to it: every input that could have supplied the kept value
// sits on the side, so removing the side loses the field. An item no edge
// folded vouches for itself; nothing here re-derives the fold's decisions
// after the fact.
function itemSideContributesOf(
  context?: ToolItemMergeContext,
): (item: ItemModel, side: (input: ItemModel) => boolean) => boolean {
  return (item, side) => {
    const record = context?.contributions.get(item);
    if (record === undefined) return side(item);
    if (record.identity.size > 0 && [...record.identity].every(side)) return true;
    const carried = (suppliers: ReadonlySet<ItemModel> | undefined): boolean =>
      suppliers !== undefined && suppliers.size > 0 && [...suppliers].every(side);
    for (const suppliers of record.nonTool.values()) {
      if (carried(suppliers)) return true;
    }
    for (const suppliers of Object.values(record.tools)) {
      if (carried(suppliers)) return true;
    }
    return false;
  };
}

// The older-page merge plus its own fragment membership, for callers that
// must follow content through it (the mobile store's retained-turn bound:
// the strip pass reads itemFoldSources, the compact-turn ownership transfer
// reads newerTurnFolds — the retained side is the merge's "newer" input
// here, and a page fragment can bridge two retained turns so only the
// group's last fragment keeps its id). olderTurns are the hydrated page
// inputs the membership refers to, so a caller can classify leaves by
// reference against its own real-source set.
export interface OlderItemPageMerge {
  model: ThreadModel;
  folds: TurnHistoryFoldDetail;
  olderTurns: readonly TurnModel[];
}

export function mergeOlderItemPageWithFolds(model: ThreadModel, resp: ThreadTurnsListResponse): OlderItemPageMerge {
  // The page response carries no ref of its own (ThreadTurnsListResponse is
  // bare turns); the model it merges into already knows the serving session,
  // carried from hydrate on model.imageSessionId. A legacy model hydrated
  // before that field existed re-derives it from its own thread id.
  const imageSessionRoute = imageSessionRouteForSession(model.imageSessionId ?? model.threadId);
  const olderTurns = (resp.data ?? []).map((turn) => wireToTurnModel(turn, imageSessionRoute));
  const context = createToolItemMergeContext(model.turns, olderTurns);
  const merged = mergeTurnHistoryWithContext(olderTurns, model.turns, context);
  // The public merge keeps a no-op fresh array by reference. Pagination has
  // historically normalized fresh fragment chains whenever a page arrives,
  // so retain that adapter behavior without changing the public no-op result.
  const turns =
    merged.turns === model.turns
      ? mergeToolCallsByCallId(
          placeCoalescedTurns(coalesceTurnFragments(olderTurns, model.turns, context), olderTurns.length),
          context,
        )
      : merged.turns;

  return {
    model: {
      ...model,
      turns,
      olderCursor: resp.nextCursor,
    },
    // The no-op branch re-coalesces the same inputs under the same context,
    // so the membership the first pass recorded still names the inputs the
    // returned items fold from.
    folds: { ...merged, turns },
    olderTurns,
  };
}

// mergeOlderItemPage folds one thread/turns/list backfill page into the model.
// A v6 page into a model holding versioned history merges by version under
// the snapshot rules below; anything else takes the unversioned merge.
export function mergeOlderItemPage(model: ThreadModel, resp: ThreadTurnsListResponse): ThreadModel {
  if (model.history && resp.snapshot) return mergeVersionedPage(model, model.history, resp, true);
  return mergeOlderItemPageWithFolds(model, resp).model;
}

// ---------------------------------------------------------------------------
// Versioned history and the live overlay (evener-appwire-v6; spec
// docs/superpowers/specs/2026-09-25-transcript-read-model-design.md: "Recorded
// length", "Reads", "Live history notifications", "The live overlay").
//
// model.history.turns holds the recorded turns; model.overlay holds the live
// overlay; model.turns is derived from both (withDisplay). Recorded items
// merge by version and are identified by transcriptKey; a merge never removes
// one. Only a replacement does: another boot generation, a newer epoch or
// incarnation, a read after invalidation, or an authoritative daemonless read
// for the position range it returned.
// ---------------------------------------------------------------------------

const EMPTY_HISTORY: HistoryState = {
  bootGeneration: "",
  epoch: 0,
  length: 0,
  appliedGeneration: 0,
  issuedGeneration: 0,
  deferredPages: [],
  turns: [],
};

type ReadIdentity = Pick<HistoryState, "bootGeneration" | "epoch" | "incarnation" | "length">;

function readIdentity(resp: {
  bootGeneration?: string;
  epoch?: number;
  snapshot?: { incarnation: string; length: number };
}): ReadIdentity {
  return {
    bootGeneration: resp.bootGeneration ?? "",
    epoch: resp.epoch ?? 0,
    incarnation: resp.snapshot?.incarnation,
    length: resp.snapshot?.length ?? 0,
  };
}

// The running turn a read names. Only evener.activeTurnId counts: a turn the
// snapshot merely records as inProgress (a crash left it open) is not running.
function runningTurn(thread: Thread): Pick<ThreadModel, "runningTurnId"> {
  return thread.evener.activeTurnId ? { runningTurnId: thread.evener.activeTurnId } : {};
}

function overlayRecord(items: readonly OverlayItem[] | undefined): Record<string, OverlayItem> {
  const record: Record<string, OverlayItem> = {};
  for (const item of items ?? []) record[item.key] = item;
  return record;
}

function itemIdentity(item: ItemModel): string {
  return item.transcriptKey ?? item.id;
}

// Orders by (entry, item, sub). An item with no position sorts after every
// positioned one.
function comparePositions(left: ThreadItemPosition | undefined, right: ThreadItemPosition | undefined): number {
  if (!left || !right) return left ? -1 : right ? 1 : 0;
  return left.entry - right.entry || left.item - right.item || (left.sub ?? 0) - (right.sub ?? 0);
}

// Where a turn sits: its first item's position, or, for a turn that holds no
// item yet, the entry its version names.
function turnPosition(turn: TurnModel): ThreadItemPosition | undefined {
  return turn.items[0]?.position ?? (turn.version === undefined ? undefined : { entry: turn.version, item: 0 });
}

// The number of items that sort before position: the index a new item at
// position is inserted at, and one past the last item preceding it.
function itemsBefore(items: readonly ItemModel[], position: ThreadItemPosition | undefined): number {
  let low = 0;
  let high = items.length;
  while (low < high) {
    const mid = (low + high) >>> 1;
    if (comparePositions(items[mid]?.position, position) < 0) low = mid + 1;
    else high = mid;
  }
  return low;
}

// The higher version wins; a held item or turn without a version (an older
// read's) always yields.
function supersedes(held: number | undefined, incoming: number | undefined): boolean {
  return held === undefined || (incoming !== undefined && incoming > held);
}

interface HistoryFragment {
  turns: TurnModel[]; // turn scalars, items empty
  items: ItemModel[];
}

// Flattens wire turns into turn scalars and the items they carry. An item
// without its own turnId belongs to the turn that carried it.
function splitWireTurns(turns: readonly Turn[], imageSessionRoute: string | undefined): HistoryFragment {
  const fragment: HistoryFragment = { turns: [], items: [] };
  for (const turn of turns) {
    fragment.turns.push({ ...wireToTurnScalars(turn), items: [] });
    for (const wire of turn.items ?? []) {
      const item = wireItemToModel(wire, imageSessionRoute);
      if (item.turnId === "") item.turnId = turn.id;
      fragment.items.push(item);
    }
  }
  return fragment;
}

function wireFragment(
  turns: readonly Turn[] | undefined,
  items: readonly ThreadItem[] | undefined,
  imageSessionRoute: string | undefined,
): HistoryFragment {
  return {
    turns: (turns ?? []).map((turn) => ({ ...wireToTurnScalars(turn), items: [] })),
    items: (items ?? []).map((item) => wireItemToModel(item, imageSessionRoute)),
  };
}

// Merges a fragment into recorded turns by version. A turn or item history
// does not hold is added, unless heldOnly (a daemonless read's `changes`,
// which refresh held items and never add pages the client did not load).
// Returns turns itself when nothing changed, and keeps every untouched turn
// by reference.
function mergeHistory(turns: TurnModel[], fragment: HistoryFragment, heldOnly = false): TurnModel[] {
  const byId = new Map(turns.map((turn) => [turn.id, turn]));
  const ownedItems = new Set<string>();
  let changed = false;
  for (const incoming of fragment.turns) {
    const held = byId.get(incoming.id);
    if (held ? !supersedes(held.version, incoming.version) : heldOnly) continue;
    byId.set(incoming.id, { ...incoming, items: held?.items ?? [] });
    changed = true;
  }
  for (const incoming of fragment.items) {
    const held = byId.get(incoming.turnId);
    if (!held && heldOnly) continue;
    const turn = held ?? { id: incoming.turnId, status: "inProgress", items: [] };
    const identity = itemIdentity(incoming);
    const index = turn.items.findIndex((item) => itemIdentity(item) === identity);
    if (index === -1 ? heldOnly : !supersedes(turn.items[index]?.version, incoming.version)) continue;
    // Copy a turn's items once per merge, then edit the copy in place.
    const writable = ownedItems.has(turn.id) ? turn : { ...turn, items: [...turn.items] };
    ownedItems.add(turn.id);
    if (index === -1) writable.items.splice(itemsBefore(writable.items, incoming.position), 0, incoming);
    else writable.items[index] = incoming;
    byId.set(turn.id, writable);
    changed = true;
  }
  if (!changed) return turns;
  return [...byId.values()].sort((left, right) => comparePositions(turnPosition(left), turnPosition(right)));
}

function fragmentRange(items: readonly ItemModel[]): [ThreadItemPosition, ThreadItemPosition] | undefined {
  let first: ThreadItemPosition | undefined;
  let last: ThreadItemPosition | undefined;
  for (const item of items) {
    if (!item.position) continue;
    if (!first || comparePositions(item.position, first) < 0) first = item.position;
    if (!last || comparePositions(item.position, last) > 0) last = item.position;
  }
  return first && last ? [first, last] : undefined;
}

// An authoritative read replaces what it returned: held items in [from, to]
// (to undefined: through the end of history) are dropped before the read's
// own items merge in, and a turn left with no items goes with them.
function dropItemsInRange(turns: TurnModel[], from: ThreadItemPosition, to?: ThreadItemPosition): TurnModel[] {
  const inRange = (position: ThreadItemPosition | undefined) =>
    position !== undefined &&
    comparePositions(position, from) >= 0 &&
    (to === undefined || comparePositions(position, to) <= 0);
  let changed = false;
  const kept: TurnModel[] = [];
  for (const turn of turns) {
    const items = turn.items.filter((item) => !inRange(item.position));
    if (items.length === turn.items.length) {
      kept.push(turn);
      continue;
    }
    changed = true;
    if (items.length > 0) kept.push({ ...turn, items });
  }
  return changed ? kept : turns;
}

// Drops the overlay state history now covers: a stream once history holds an
// item of its round, a preview once history holds the agentMessage of its
// communicate call, and a tool's execution state once its history item carries
// the completing TOOL_RESULTS entry. Returns overlay itself when nothing is
// covered.
function pruneCoveredOverlay(
  turns: readonly TurnModel[],
  overlay: Record<string, OverlayItem>,
): Record<string, OverlayItem> {
  if (Object.keys(overlay).length === 0) return overlay;
  const rounds = new Set<string>();
  const communicated = new Set<string>();
  const completed = new Set<string>();
  for (const turn of turns) {
    for (const item of turn.items) {
      if (item.roundId) rounds.add(item.roundId);
      if (item.type === "agentMessage" && item.callId) communicated.add(item.callId);
      if (item.completedAtEntry && item.transcriptKey) completed.add(item.transcriptKey);
    }
  }
  const covered = (item: OverlayItem): boolean => {
    switch (item.kind) {
      case "stream":
        return item.roundId !== undefined && rounds.has(item.roundId);
      case "preview":
        return item.callId !== undefined && communicated.has(item.callId);
      case "tool":
        return item.historyKey !== undefined && completed.has(item.historyKey);
      default:
        return false;
    }
  };
  return filterOverlay(overlay, covered);
}

// Drops the overlay items drop names; returns overlay itself when none go.
function filterOverlay(
  overlay: Record<string, OverlayItem>,
  drop: (item: OverlayItem) => boolean,
): Record<string, OverlayItem> {
  const entries = Object.entries(overlay);
  const kept = entries.filter(([, item]) => !drop(item));
  return kept.length === entries.length ? overlay : Object.fromEntries(kept);
}

// A display item the overlay contributes on its own: a stream, preview,
// notice, or a tool whose history item is not held.
function overlayDisplayItem(overlayItem: OverlayItem, imageSessionRoute: string | undefined): ItemModel {
  const item = wireItemToModel(overlayItem.item, imageSessionRoute);
  item.overlayKey = overlayItem.key;
  if (item.turnId === "" && overlayItem.turnId) item.turnId = overlayItem.turnId;
  if (overlayItem.anchor) item.position = { ...overlayItem.anchor };
  return item;
}

// A tool's execution state laid over its in-progress history item: running
// output, status and held images. The item keeps its id and version.
function layOverHistoryItem(item: ItemModel, overlayItem: OverlayItem): ItemModel {
  return copyItemTextPresence(item, {
    ...item,
    output: overlayItem.item.output ?? item.output,
    status: overlayItem.item.status ?? item.status,
    outputImages: outputImagesToItemImages(overlayItem.item.outputImages) ?? item.outputImages,
    overlayKey: overlayItem.key,
  });
}

// What produced a display turn that carries overlay items: its recorded turn
// (undefined for a placeholder) and the overlay items laid into it. A display
// turn whose inputs are unchanged is reused, so a delta on one turn leaves
// every other turn's reference alone. A turn with no overlay item is its
// recorded turn itself and needs no entry.
interface DisplayTurnSource {
  recorded: TurnModel | undefined;
  overlay: readonly OverlayItem[];
}

const displayTurnSources = new WeakMap<TurnModel, DisplayTurnSource>();

function sameOverlayItems(left: readonly OverlayItem[], right: readonly OverlayItem[]): boolean {
  return left.length === right.length && left.every((item, index) => item === right[index]);
}

// The turn holding the recorded item a tool's execution state lays over.
function toolHistoryTurn(overlayItem: OverlayItem, turns: readonly TurnModel[]): TurnModel | undefined {
  const holds = (turn: TurnModel) => turn.items.some((item) => item.transcriptKey === overlayItem.historyKey);
  const hinted = turns.find((turn) => turn.id === overlayItem.turnId);
  return hinted && holds(hinted) ? hinted : turns.find(holds);
}

// The turn holding the recorded item that precedes a notice's anchor across
// every turn (turns can interleave), or undefined when nothing precedes it.
function noticeHistoryTurn(anchor: ThreadItemPosition, turns: readonly TurnModel[]): TurnModel | undefined {
  let found: TurnModel | undefined;
  let preceding: ThreadItemPosition | undefined;
  for (const turn of turns) {
    const position = turn.items[itemsBefore(turn.items, anchor) - 1]?.position;
    if (position && (!preceding || comparePositions(position, preceding) > 0)) {
      found = turn;
      preceding = position;
    }
  }
  return found;
}

// The display turn that shows an overlay item.
function overlayTurnId(overlayItem: OverlayItem, turns: readonly TurnModel[]): string {
  const fallback = overlayItem.turnId || overlayItem.item.turnId || "";
  if (overlayItem.kind === "tool" && overlayItem.historyKey) {
    return toolHistoryTurn(overlayItem, turns)?.id ?? fallback;
  }
  if (overlayItem.kind === "notice" && overlayItem.anchor) {
    return (noticeHistoryTurn(overlayItem.anchor, turns) ?? turns[0])?.id ?? (fallback || SYSTEM_PRELUDE_TURN_ID);
  }
  return fallback;
}

function buildDisplayTurn(id: string, source: DisplayTurnSource, imageSessionRoute: string | undefined): TurnModel {
  const laid = new Map<string, OverlayItem>();
  const trailing: ItemModel[] = [];
  let items = source.recorded?.items ?? [];
  let noticeItems: ItemModel[] | undefined;
  for (const overlayItem of source.overlay) {
    if (overlayItem.kind === "notice") {
      noticeItems ??= [...items];
      const notice = overlayDisplayItem(overlayItem, imageSessionRoute);
      noticeItems.splice(itemsBefore(noticeItems, notice.position), 0, notice);
    } else if (
      overlayItem.kind === "tool" &&
      overlayItem.historyKey &&
      source.recorded?.items.some((item) => item.transcriptKey === overlayItem.historyKey)
    ) {
      laid.set(overlayItem.historyKey, overlayItem);
    } else {
      trailing.push(overlayDisplayItem(overlayItem, imageSessionRoute));
    }
  }
  items = noticeItems ?? items;
  if (laid.size > 0) {
    items = items.map((item) => {
      const overlayItem = item.transcriptKey === undefined ? undefined : laid.get(item.transcriptKey);
      return overlayItem && !item.completedAtEntry ? layOverHistoryItem(item, overlayItem) : item;
    });
  }
  const turn: TurnModel = {
    ...(source.recorded ?? { id, status: "inProgress" }),
    items: trailing.length > 0 ? [...items, ...trailing] : items,
  };
  displayTurnSources.set(turn, source);
  return turn;
}

// The display turns for recorded turns and an overlay: recorded turns in
// order, then a placeholder turn for each turn the overlay shows that history
// does not hold yet. previous is the model's current display, reused where a
// turn's inputs did not change.
function displayTurns(
  recorded: TurnModel[],
  overlay: Record<string, OverlayItem>,
  previous: readonly TurnModel[],
  imageSessionRoute: string | undefined,
): TurnModel[] {
  const byTurn = new Map<string, OverlayItem[]>();
  for (const overlayItem of Object.values(overlay)) {
    const id = overlayTurnId(overlayItem, recorded);
    const list = byTurn.get(id);
    if (list) list.push(overlayItem);
    else byTurn.set(id, [overlayItem]);
  }
  if (byTurn.size === 0) return recorded;
  const previousById = new Map(previous.map((turn) => [turn.id, turn]));
  const display = (id: string, recordedTurn: TurnModel | undefined): TurnModel => {
    const overlayItems = byTurn.get(id);
    if (!overlayItems && recordedTurn) return recordedTurn;
    const source: DisplayTurnSource = { recorded: recordedTurn, overlay: overlayItems ?? [] };
    const prior = previousById.get(id);
    const priorSource = prior && displayTurnSources.get(prior);
    if (
      prior &&
      priorSource &&
      priorSource.recorded === recordedTurn &&
      sameOverlayItems(priorSource.overlay, source.overlay)
    ) {
      return prior;
    }
    return buildDisplayTurn(id, source, imageSessionRoute);
  };
  const turns = recorded.map((turn) => display(turn.id, turn));
  const held = new Set(recorded.map((turn) => turn.id));
  for (const id of byTurn.keys()) {
    if (!held.has(id)) turns.push(display(id, undefined));
  }
  return turns;
}

function modelImageSessionRoute(model: ThreadModel): string | undefined {
  return imageSessionRouteForSession(model.imageSessionId ?? model.threadId);
}

// Sets a model's history and overlay and derives its display turns. The
// overlay first loses whatever the history now covers.
function withDisplay<M extends ThreadModel>(
  model: M,
  history: HistoryState,
  overlay: Record<string, OverlayItem>,
): M & { history: HistoryState } {
  const shown = pruneCoveredOverlay(history.turns, overlay);
  return {
    ...model,
    history,
    overlay: shown,
    turns: displayTurns(history.turns, shown, model.turns, modelImageSessionRoute(model)),
  };
}

// Marks a thread invalid: nothing merges until a latest-window response to a
// generation issued after this point replaces its whole history.
function invalidated(history: HistoryState, pendingIncarnation?: string): HistoryState {
  return {
    ...history,
    invalidatedAtGeneration: history.invalidatedAtGeneration ?? history.issuedGeneration,
    ...(pendingIncarnation === undefined ? {} : { pendingIncarnation }),
  };
}

function historyIsLive(history: HistoryState | undefined): history is HistoryState {
  return history !== undefined && history.invalidatedAtGeneration === undefined;
}

/**
 * Records a new latest-window thread/read for the thread and returns the
 * request generation it carries (ThreadReadParams.requestGeneration). The
 * counter is per thread and never resets. A store keeps one latest-window read
 * per thread in flight; applyReadResponse drops any response to a generation
 * older than the newest issued, or issued before the thread was invalidated.
 * Backfill pages (thread/turns/list) carry no generation.
 */
export function issueLatestWindowRead<M extends ThreadModel>(
  model: M,
): { model: ThreadModel & ModelExtras<M>; requestGeneration: number } {
  const history = model.history ?? EMPTY_HISTORY;
  const requestGeneration = history.issuedGeneration + 1;
  return {
    model: publicModel<M>({ ...model, history: { ...history, issuedGeneration: requestGeneration } }),
    requestGeneration,
  };
}

/**
 * Records that a history read failed with ErrorTranscriptHistoryFailed
 * (errors.ts's isTranscriptHistoryFailedError). The held history stays as it
 * is and `history.failed` carries the one diagnostic to show; nothing merges
 * or replaces until a latest-window read succeeds, while the overlay keeps
 * updating. The error's generation and epoch are never adopted. The failed
 * read ends the thread's invalid state, so a store does not re-issue it in a
 * loop; the next read comes from the next event (a resync, a reconnect, the
 * user).
 */
export function applyHistoryReadFailure<M extends ThreadModel>(
  model: M,
  diagnostic: string,
): ThreadModel & ModelExtras<M> {
  const { invalidatedAtGeneration: _ended, ...history } = model.history ?? EMPTY_HISTORY;
  return publicModel<M>({ ...model, history: { ...history, failed: diagnostic } });
}

type ReadDisposition = "discard" | "replace" | "merge";

// What a latest-window response does to held history. Request generations
// decide whether it applies at all; then the generation token, the epoch and
// the incarnation decide between replacing the whole history and merging.
function readDisposition(held: HistoryState, resp: ThreadReadResponse): ReadDisposition {
  const generation = resp.requestGeneration ?? 0;
  if (generation < held.issuedGeneration) return "discard";
  if (held.invalidatedAtGeneration !== undefined) {
    return generation > held.invalidatedAtGeneration ? "replace" : "discard";
  }
  const identity = readIdentity(resp);
  const action = compareBootGeneration(held.bootGeneration, identity.bootGeneration);
  if (action !== "apply") return action === "ignore" ? "discard" : "replace";
  if (identity.epoch < held.epoch) return "discard";
  if (identity.epoch > held.epoch || identity.incarnation !== held.incarnation) return "replace";
  return identity.length < held.length ? "discard" : "merge";
}

/**
 * Applies a latest-window thread/read response (one issued through
 * issueLatestWindowRead) to a model holding versioned history:
 * - a response to an older request generation than the newest issued, or to one
 *   issued before the thread was invalidated, is discarded;
 * - a lower boot generation, an older epoch, or the same incarnation with a
 *   shorter length is discarded;
 * - an invalid thread, another generation token, a newer epoch or another
 *   incarnation replaces the whole history, then applies any pages deferred
 *   for that incarnation;
 * - otherwise it merges by version; an authoritative (daemonless) response
 *   first drops every held item from its first position on; `changes` (held
 *   items and turns outside the window whose version grew) merge into held
 *   history only.
 * The thread's own fields, the overlay (resp.overlay) and the running turn are
 * always the response's, and a successful read ends a failed-history state.
 * A discarded response returns model itself.
 */
export function applyReadResponse<M extends ThreadModel>(
  model: M,
  resp: ThreadReadResponse,
  now: number,
): ThreadModel & ModelExtras<M> {
  const held = model.history ?? EMPTY_HISTORY;
  const disposition = readDisposition(held, resp);
  if (disposition === "discard") return publicModel<M>(model);
  const fields = threadFields(resp, model.ref, now);
  const imageSessionRoute = imageSessionRouteForSession(fields.imageSessionId ?? fields.threadId);
  const fresh = splitWireTurns(resp.thread.turns ?? [], imageSessionRoute);
  let turns: TurnModel[];
  let olderCursor = resp.olderCursor;
  if (disposition === "replace") {
    turns = mergeHistory([], fresh);
  } else {
    const window = fragmentRange(fresh.items);
    turns = resp.authoritative ? dropItemsInRange(held.turns, window?.[0] ?? { entry: 0, item: 0 }) : held.turns;
    // Older pages the client holds keep their own cursor.
    const oldest = turns[0]?.items[0]?.position;
    if (window && oldest && comparePositions(oldest, window[0]) < 0) olderCursor = model.olderCursor;
    turns = mergeHistory(turns, fresh);
    if (resp.changes) {
      turns = mergeHistory(turns, wireFragment(resp.changes.turns, resp.changes.items, imageSessionRoute), true);
    }
  }
  const generation = resp.requestGeneration ?? 0;
  const history: HistoryState = {
    ...readIdentity(resp),
    appliedGeneration: generation,
    issuedGeneration: Math.max(held.issuedGeneration, generation),
    deferredPages: [],
    turns,
  };
  const { runningTurnId: _previous, ...base } = model;
  let next = withDisplay(
    { ...base, ...fields, ...runningTurn(resp.thread), olderCursor },
    history,
    overlayRecord(resp.overlay),
  );
  for (const deferred of held.deferredPages) {
    if (deferred.snapshot?.incarnation === history.incarnation) {
      next = mergeVersionedPage(next, next.history, deferred, false);
    }
  }
  return publicModel<M>(next);
}

type PageDisposition = "discard" | "defer" | "invalidate" | "newIncarnation" | "merge";

// What a backfill page does to held history. Pages carry no request
// generation: they accumulate within their snapshot in any order, and never
// replace anything.
function pageDisposition(held: HistoryState, resp: ThreadTurnsListResponse): PageDisposition {
  if (held.failed !== undefined) return "discard";
  const identity = readIdentity(resp);
  const action = compareBootGeneration(held.bootGeneration, identity.bootGeneration);
  if (action === "ignore") return "discard";
  if (held.pendingIncarnation !== undefined && identity.incarnation === held.pendingIncarnation) return "defer";
  if (held.invalidatedAtGeneration !== undefined) return "discard";
  if (action === "replace" || identity.epoch > held.epoch) return "invalidate";
  if (identity.epoch < held.epoch) return "discard";
  if (identity.incarnation !== held.incarnation) return "newIncarnation";
  return identity.length < held.length ? "discard" : "merge";
}

function mergeVersionedPage<M extends ThreadModel>(
  model: M,
  held: HistoryState,
  resp: ThreadTurnsListResponse,
  takeCursor: boolean,
): M {
  switch (pageDisposition(held, resp)) {
    case "discard":
      return model;
    case "defer":
      return { ...model, history: { ...held, deferredPages: [...held.deferredPages, resp] } };
    case "invalidate":
      return { ...model, history: invalidated(held) };
    case "newIncarnation":
      return {
        ...model,
        history: { ...invalidated(held, resp.snapshot?.incarnation), deferredPages: [resp] },
      };
    case "merge": {
      const fresh = splitWireTurns(resp.data ?? [], modelImageSessionRoute(model));
      const range = resp.authoritative ? fragmentRange(fresh.items) : undefined;
      const turns = mergeHistory(range ? dropItemsInRange(held.turns, range[0], range[1]) : held.turns, fresh);
      const next = takeCursor ? { ...model, olderCursor: resp.nextCursor } : model;
      return withDisplay(next, { ...held, turns }, model.overlay ?? {});
    }
  }
}

// history/updated: the full current form of every recorded item and turn an
// entry changed, merged by version under the one generation state machine.
function applyHistoryUpdated<M extends ThreadModel>(model: M, params: HistoryUpdatedParams, now: number): M {
  const held = model.history ?? EMPTY_HISTORY;
  const live = { ...model, lastFrameAt: now };
  if (held.failed !== undefined || held.invalidatedAtGeneration !== undefined) return live;
  const action = compareBootGeneration(held.bootGeneration, params.bootGeneration);
  if (action === "ignore" || (action === "apply" && params.epoch < held.epoch)) return live;
  if (action === "replace" || params.epoch > held.epoch) return { ...live, history: invalidated(held) };
  if (params.snapshot.incarnation !== held.incarnation) {
    return { ...live, history: invalidated(held, params.snapshot.incarnation) };
  }
  const turns = mergeHistory(held.turns, wireFragment(params.turns, params.items, modelImageSessionRoute(model)));
  if (turns === held.turns) return live;
  return withDisplay(live, { ...held, turns }, model.overlay ?? {});
}

// evener/thread/resync: the server bumped the thread's epoch (or a hub pushed
// a resync of its own, which names none). A newer epoch or another generation
// invalidates the thread; the store re-reads its latest window.
function applyResync<M extends ThreadModel>(model: M, params: ThreadResyncParams, now: number): M {
  const held = model.history;
  if (!held) return model;
  const live = { ...model, lastFrameAt: now };
  const action = params.bootGeneration ? compareBootGeneration(held.bootGeneration, params.bootGeneration) : "apply";
  if (action === "ignore") return live;
  if (action === "replace" || params.epoch === undefined || params.epoch > held.epoch) {
    return { ...live, history: invalidated(held) };
  }
  return live;
}

// overlay/delta appends to one stream, preview or tool item. Only the display
// turn showing it is rebuilt. A delta for an item the overlay no longer holds
// (covered, reset or ended) is ignored.
function applyOverlayDelta<M extends ThreadModel>(model: M, params: OverlayDeltaParams, now: number): M {
  const held = model.overlay?.[params.key];
  const live = { ...model, lastFrameAt: now };
  if (!held || !historyIsLive(model.history)) return live;
  const item =
    params.field === "output"
      ? { ...held.item, output: (held.item.output ?? "") + params.delta }
      : { ...held.item, text: (held.item.text ?? "") + params.delta };
  const next: OverlayItem = { ...held, item };
  const overlay = { ...model.overlay, [params.key]: next };
  const index = model.turns.findIndex((turn) => displayTurnSources.get(turn)?.overlay.includes(held));
  const turn = model.turns[index];
  const source = turn && displayTurnSources.get(turn);
  // Every overlay item a display shows is in its turn's source; one that is not
  // shown yet (no turn found) takes the full derivation.
  if (!turn || !source) return withDisplay(live, model.history, overlay);
  const rebuilt = buildDisplayTurn(
    turn.id,
    { ...source, overlay: source.overlay.map((overlayItem) => (overlayItem === held ? next : overlayItem)) },
    modelImageSessionRoute(model),
  );
  return { ...live, overlay, turns: model.turns.map((shown, at) => (at === index ? rebuilt : shown)) };
}

// overlay/upserted, overlay/reset and overlay/end: replace the overlay and
// re-derive the display. Nothing applies while the thread is invalid.
function applyOverlayChange<M extends ThreadModel>(
  model: M,
  now: number,
  change: (overlay: Record<string, OverlayItem>) => Record<string, OverlayItem>,
): M {
  const live = { ...model, lastFrameAt: now };
  if (!historyIsLive(model.history)) return live;
  const current = model.overlay ?? {};
  const overlay = change(current);
  if (overlay === current) return live;
  return withDisplay(live, model.history, overlay);
}

// Removes one pending escalation by id, returning the same reference when the
// id is absent (the no-op-same-reference idiom used throughout this file). Two
// callers reuse it: the threads store's resolveEscalation action, after this
// client's own successful evener/sandbox/escalation/resolve call, and the
// evener/sandbox/escalation/resolved notification case in applyNotification,
// which fires when another client's resolve — or a turn-interrupt or session
// close — retires the card. So a client merely watching the session drops its
// now-stale copy live off the broadcast instead of waiting for its next
// snapshot.
// The fields a caller adds on top of ThreadModel, and ONLY those. ThreadModel's
// own fields are deliberately taken from ThreadModel, not from M: the reducer
// owns and rewrites them (clearing modelRetry, restamping lastFrameAt,
// replacing status), so a caller that intersects one to a narrower type gets
// ThreadModel's type back rather than a contract the fold is about to break.
//
// The conditional distributes over a union M: a plain Omit collapses a union to
// its members' COMMON keys and drops each member's own extras, so a caller
// whose model is a union would lose the field it distinguishes the members by.
type ModelExtras<M extends ThreadModel> = M extends unknown ? Omit<M, keyof ThreadModel> : never;

// Re-attaches ModelExtras at the exported boundary. The fold works on the
// concrete model M — every case spreads it, so the runtime value already
// carries the caller's extras — but TS cannot prove a bare generic M assignable
// to the conditional type above, so the public entry points convert here.
function publicModel<M extends ThreadModel>(value: unknown): ThreadModel & ModelExtras<M> {
  return value as ThreadModel & ModelExtras<M>;
}

// The fold's own form of resolvePendingEscalation: returns the concrete model,
// which is what applyNotificationToThread composes with. The exported wrapper
// below presents the distributive type.
function resolvePendingEscalationModel<M extends ThreadModel>(model: M, escalationId: string): M {
  if (!model.pendingEscalations.some((e) => e.escalationId === escalationId)) return model;
  return { ...model, pendingEscalations: model.pendingEscalations.filter((e) => e.escalationId !== escalationId) };
}

export function resolvePendingEscalation<M extends ThreadModel>(
  model: M,
  escalationId: string,
): ThreadModel & ModelExtras<M> {
  return publicModel<M>(resolvePendingEscalationModel(model, escalationId));
}

// notificationRoutingKey extracts the identity a frame routes by, from the
// frame's own params alone: ref before threadId, else nothing. It is the ONE
// source of truth for that precedence — notificationTargetsThread answers
// per-model matches from it, and the threads store's routing index keys off
// it directly — so the two layers cannot drift apart.
export type NotificationRoutingKey = { ref: string } | { threadId: string };

export function notificationRoutingKey(n: AnyNotification): NotificationRoutingKey | null {
  const params = n.params as { ref?: unknown; threadId?: unknown };
  if (typeof params.ref === "string") return { ref: params.ref };
  if (typeof params.threadId === "string") return { threadId: params.threadId };
  return null;
}

export function notificationTargetsThread(n: AnyNotification, model: ThreadModel): boolean {
  const key = notificationRoutingKey(n);
  if (!key) return false;
  if ("ref" in key) return key.ref === model.ref;
  return key.threadId === model.threadId;
}

// Replaces the turn identified by turnId with `fn(turn)`; turns not matching
// pass through unchanged (same reference). The whole ARRAY is also unchanged
// (same reference) when turnId names no turn in `turns` at all — every
// caller that needs to tell "the frame changed something" from "the frame
// named a turn outside this window" (a mobile store's gap detection, e.g.)
// reads that from array identity, and Array.prototype.map allocates a new
// array unconditionally, even when every mapped element came back identical.
function mapTurn(turns: TurnModel[], turnId: string, fn: (turn: TurnModel) => TurnModel): TurnModel[] {
  let changed = false;
  const mapped = turns.map((t) => {
    if (t.id !== turnId) return t;
    const next = fn(t);
    if (next !== t) changed = true;
    return next;
  });
  return changed ? mapped : turns;
}

// Replaces the item identified by itemId with `fn(item)`; items not matching
// pass through unchanged (same reference).
function mapItem(items: ItemModel[], itemId: string, fn: (item: ItemModel) => ItemModel): ItemModel[] {
  return items.map((it) => (it.id === itemId ? fn(it) : it));
}

function mapItemByIdentity(items: ItemModel[], incoming: ItemModel, fn: (item: ItemModel) => ItemModel): ItemModel[] {
  return items.map((it) => (itemIdentityMatches(it, incoming) ? fn(it) : it));
}

// Finds which turn currently holds itemId, preferring the notification's own
// turnId hint, then the model's active turn, then a full scan (defensive —
// in practice the hint and activeTurnId always agree, since only one turn is
// ever in flight at a time).
function findItemTurnId(
  model: ThreadModel,
  turnIdHint: string | undefined,
  identity: string | ItemModel,
): string | undefined {
  const turnHasItem = (turn: TurnModel) =>
    turn.items.some((it) => (typeof identity === "string" ? it.id === identity : itemIdentityMatches(it, identity)));
  if (turnIdHint) {
    const turn = model.turns.find((t) => t.id === turnIdHint);
    if (turn && turnHasItem(turn)) return turnIdHint;
  }
  if (model.activeTurnId) {
    const turn = model.turns.find((t) => t.id === model.activeTurnId);
    if (turn && turnHasItem(turn)) return model.activeTurnId;
  }
  return model.turns.find(turnHasItem)?.id;
}

// Resolves which turn a brand-new item belongs to (turnId hint, then the
// item's own turnId, then the model's active turn), verifying that turn
// actually exists in the model.
function resolveInsertTurnId(
  model: ThreadModel,
  turnIdHint: string | undefined,
  itemTurnId: string | undefined,
): string | undefined {
  const candidate = turnIdHint ?? itemTurnId ?? model.activeTurnId;
  return candidate !== undefined && model.turns.some((t) => t.id === candidate) ? candidate : undefined;
}

// Replaces the FIRST turn matching turnId with `settled`, reporting loudly
// when more than one row shares the id. model.turns is presented everywhere
// else as if ids are unique — a duplicate should never happen (see the
// "turn/started" case's comment) — but replacing EVERY entry sharing turnId
// would overwrite an unrelated turn's content with this settle's, silently,
// the exact corruption this reducer must not produce.
function settleFirstMatchingTurn(turns: TurnModel[], turnId: string, settled: TurnModel): TurnModel[] {
  const duplicateCount = turns.reduce((count, t) => (t.id === turnId ? count + 1 : count), 0);
  if (duplicateCount > 1) {
    console.error(
      `applyNotification: turn/completed turnId ${turnId} matches ${duplicateCount} turns in model.turns — settling only the first match (turn-id-uniqueness invariant violated)`,
    );
  }
  // Same array-identity contract as mapTurn above: unchanged (same reference)
  // when turnId names no turn in `turns` at all.
  let settledFirstMatch = false;
  const mapped = turns.map((t) => {
    if (t.id !== turnId || settledFirstMatch) return t;
    settledFirstMatch = true;
    return settled;
  });
  return settledFirstMatch ? mapped : turns;
}

// Folds a settle stamp's own items into a turn's item list BY ID: an item
// already there is merged exactly the way item/completed merges its settled
// payload (the three helpers read/write disjoint fields off the same `old`),
// a new one is appended. Appending — not replacing, which is what the active
// turn's "full" branch does — is what the announcement path needs: the daemon
// sends one turn/completed per announcement, all naming the same synthetic
// turn, so replacing would leave a startup burst showing only its last line
// where the snapshot shows every one (server/appwire_turns.go's upsertItem).
function upsertTurnItems(
  items: ItemModel[],
  incoming: ThreadItem[],
  now: number,
  imageSessionRoute?: string,
): ItemModel[] {
  let next = items;
  for (const wire of incoming) {
    const settled = wireItemToModel(wire, imageSessionRoute);
    const index = next.findIndex((it) => itemIdentityMatches(it, settled));
    if (index === -1) {
      next = [...next, settled];
      continue;
    }
    const old = next[index];
    if (!old) continue;
    const updated = mergeObservedTiming(
      mergeArguments(mergeReasoning(mergePageItem(old, settled), old), old),
      old,
      now,
    );
    next = next.map((item, itemIndex) => (itemIndex === index ? updated : item));
  }
  return next;
}

// Merges a completion stamp onto the turn it names, from an existing turn or
// from nothing when the model has never seen that turn. Only the fields the
// stamp actually carries are taken, mirroring the hub's own snapshot
// reduction (server/appwire_turns.go's NotifyTurnCompleted case): an
// announcement stamp carries id/status/items and nothing else, so a turn's
// already-known startedAt, usage and cost survive it.
function mergeTurnCompletionStamp(
  existing: TurnModel | undefined,
  turnId: string,
  stamp: Turn,
  now: number,
  imageSessionRoute?: string,
): TurnModel {
  const base: TurnModel = existing ?? { id: turnId, status: "", items: [] };
  return {
    ...base,
    id: turnId,
    status: stamp.status || base.status,
    completedAt: epochMsToISO(stamp.completedAt) ?? base.completedAt,
    durationMs: stamp.durationMs ?? base.durationMs,
    error: stamp.error,
    items: upsertTurnItems(base.items, stamp.items ?? [], now, imageSessionRoute),
  };
}

// The prelude is the one turn whose id fixes its POSITION: it holds content
// from before the session's first real turn by definition, so it belongs at
// the front however late its first frame arrives — nothing orders a session's
// first turn-starting request behind its startup announcements. Every other
// turn is placed where it arrived, which for a between-turns announcement gap
// is after the real turn it followed. Same rule, same reasons, as the hub's
// snapshot reduction (server/appwire_turns.go's ensureTurn).
function placeNewTurn(turns: TurnModel[], turn: TurnModel): TurnModel[] {
  return turn.id === SYSTEM_PRELUDE_TURN_ID ? [turn, ...turns] : [...turns, turn];
}

// Folds a turn/completed that names a turn OTHER than the model's active one.
// That is the daemon's no-active-turn announcement path
// (internal/appprojector/appwire_projection.go's systemAnnouncementItem): a
// session's startup burst, or a burst between two real turns, arrives as one
// turn/completed per announcement, each carrying a single item with itemsView
// "full", all sharing one synthetic turn id — SYSTEM_PRELUDE_TURN_ID before
// the first real turn, a minted "turn_N" gap id after one (kata 9ekv).
//
// activeTurnId and activeTurnStartedAt are deliberately left alone. This
// settle is not the active turn's, and a real turn can be streaming while an
// announcement's frames land, so clearing them here would stop the work clock
// and lose the item-routing anchor for a turn that is still in flight. The
// snapshot reduction clears its own active turn only on an id match, for the
// same reason.
function foldNonActiveTurnCompleted<M extends ThreadModel>(model: M, turnId: string, stamp: Turn, now: number): M {
  const existing = model.turns.find((t) => t.id === turnId);
  const settled = mergeTurnCompletionStamp(
    existing,
    turnId,
    stamp,
    now,
    imageSessionRouteForSession(model.imageSessionId ?? model.threadId),
  );
  return {
    ...model,
    turns: existing ? settleFirstMatchingTurn(model.turns, turnId, settled) : placeNewTurn(model.turns, settled),
    lastFrameAt: now,
  };
}

// Accumulates one reasoning delta chunk and, first delta only, stamps
// observedStartedAt as a client observation of when reasoning began (the
// wire carries no reasoning timestamps at all — see ItemModel's doc comment
// in model.ts). `now` is the reducer's own now parameter, never a clock
// read (purity).
function appendReasoningDelta(item: ItemModel, summaryIndex: number, delta: string, now: number): ItemModel {
  // O(1) per delta (was: summaries.slice() + [...chunks, delta], both
  // O(current-length) copies per delta — the same quadratic the
  // agentMessage case had). The outer array is a plain string[][] (its
  // entries come from wireItemToModel/hydrate too, not just this append),
  // so it copies per call — but that copy is O(#summaries), a small bound
  // set by the wire's summary indices, not by stream length. The chunk
  // list per summary is the unbounded one, and that one gets the O(1)
  // shared-backing append.
  const summaries = item.reasoningSummaries ? item.reasoningSummaries.slice() : [];
  while (summaries.length <= summaryIndex) summaries.push([]);
  const chunks = summaries[summaryIndex] ?? [];
  summaries[summaryIndex] = appendChunk(chunks, delta);
  return copyItemTextPresence(item, {
    ...item,
    reasoningSummaries: summaries,
    observedStartedAt: item.observedStartedAt ?? epochMsToISO(now),
  });
}

// Appends `incoming` to pendingEscalations, or — if an entry with the same
// escalationId is already present — replaces it in place rather than
// growing the list. Dedup exists because hydration's surface-on-entry
// snapshot (thread.evener.pendingEscalations) and this live notification can
// legitimately race and both deliver the same card; last write wins.
function upsertPendingEscalation(
  escalations: SandboxEscalationRequested[],
  incoming: SandboxEscalationRequested,
): SandboxEscalationRequested[] {
  const idx = escalations.findIndex((e) => e.escalationId === incoming.escalationId);
  if (idx === -1) return [...escalations, incoming];
  return escalations.map((e, i) => (i === idx ? incoming : e));
}

// item/completed kinds that count as the model actually producing something
// (design doc Component 1): assistant message, reasoning, tool call. A
// systemMessage or userMessage item completing (a steer "are you stuck?"
// completing its own systemMessage item, say) arrives mid-grind and must not
// be mistaken for the retry's wait being over.
const MODEL_OUTPUT_ITEM_TYPES = new Set(["agentMessage", "reasoning", "commandExecution"]);

// Restates appwire.WarningParams.EffectiveMessage shape for shape: the
// generated types (WarningParams, `warning` typed `unknown`) are
// declarations only, no runtime logic is generated alongside them, so the
// rule cannot be derived from the types and has to be written out again
// here. `message` wins when non-blank; otherwise `warning` counts when it is
// itself a non-blank string, or an object (and not an array) whose own
// `message` is a non-blank string. Every other shape carries no message.
// Returned strings are bounded from their first non-whitespace content so the
// fold does not scan and bound the selected message a second time.
function warningMessage(params: WarningParams): string {
  const message = boundedWarningText(params.message);
  if (message !== undefined) return message;
  const warning = params.warning;
  const warningText = boundedWarningText(warning);
  if (warningText !== undefined) return warningText;
  if (typeof warning === "object" && warning !== null && !Array.isArray(warning)) {
    const nested = (warning as { message?: unknown }).message;
    const nestedMessage = boundedWarningText(nested);
    if (nestedMessage !== undefined) return nestedMessage;
  }
  return "";
}

// True when value is a non-blank string — the same "is this actually content"
// reading warningMessage above and WarningItem.tsx's renderer both take for
// title/hint, so the raw-frame fallback below and the structured fields it
// would otherwise duplicate never disagree about which one has something to
// show. A type predicate so a caller narrows `unknown` in one step instead of
// repeating the typeof/trim check to get the same narrowing. Built on
// boundedWarningText below, which answers the same "is there content" scan
// as part of also bounding the value — so a caller that needs both (every
// foldWarningParams field) pays for one walk, not two.
export function hasWarningText(value: unknown): value is string {
  return boundedWarningText(value) !== undefined;
}

// A frame with no message anywhere is surfaced as the frame itself
// (appwire/warning.go's DecodeWarningParams: "a malformed warning is visible
// instead of silent" — cmd/evener-tui/hub_notifications_test.go pins the same
// contract server-side). Bounded because params is unknown on the wire and
// can carry anything; this is package-level code feeding both hosts, and
// neither host's own display bound can be assumed to run before something
// else reads item.text.
export const RAW_WARNING_FRAME_MAX_CHARS = 2000;
const RAW_WARNING_FRAME_MAX_ARRAY_ITEMS = 50;
const RAW_WARNING_FRAME_MAX_OBJECT_KEYS = 50;
const RAW_WARNING_FRAME_MAX_DEPTH = 6;
// Total object/array entries the prune will walk across the WHOLE frame,
// regardless of how the size is spread across depth and breadth. The
// per-level array/key caps alone leave a gap: many small objects, each
// individually within the array/key/depth bounds, can still sum to a huge
// tree for JSON.stringify to walk.
const RAW_WARNING_FRAME_MAX_NODES = 500;

// Prunes a value to a small bound before it ever reaches JSON.stringify:
// every string truncated to RAW_WARNING_FRAME_MAX_CHARS code points,
// every array/object to its first 50 items/keys, nesting cut off at 6
// levels, and the whole walk cut off after RAW_WARNING_FRAME_MAX_NODES
// entries regardless of shape. Without this, JSON.stringify(params) itself
// walks the WHOLE frame — up to the transport's 128 MiB limit — before
// rawWarningFrame gets a chance to slice anything; bounding the input, not
// just the output, is what keeps that walk small regardless of how large
// or how shaped the wire frame actually is.
// Truncates a string to maxCodePoints code points, safely (a UTF-16 slice
// can otherwise cut a surrogate pair in half). The one primitive every
// string bound in this file goes through — prunedForStringify's per-field
// and per-key truncation, and boundedCodePoints below — so a wire string
// too big to reach ItemModel unbounded is always cut on a code-point
// boundary, never mid-pair. A fast path for the common (short) case:
// UTF-16 length is always >= code-point count, so no huge value means no
// work.
// `start` lets a caller bound a WINDOW rather than always the leading
// prefix: the string from `start` onward is what's kept, sliced in one
// already-bounded copy (at most maxCodePoints * 2 UTF-16 units), never the
// whole `start`-to-end remainder — boundedWarningText below relies on this to
// stay bounded even when `start` is itself deep into a multi-megabyte
// string.
function boundedPrefix(s: string, maxCodePoints: number, start = 0): string {
  const remaining = s.length - start;
  if (remaining <= maxCodePoints) return start === 0 ? s : s.slice(start);
  // Bound the allocation before expanding to code points: a UTF-16 window
  // twice the code-point limit always contains at least that many code
  // points (every code point is at most two UTF-16 units), so slicing the
  // string first — a cheap view, no per-character array — never drops real
  // content.
  let bounded = s.slice(start, start + maxCodePoints * 2);
  // A UTF-16 slice can end mid-surrogate-pair, leaving a lone high surrogate
  // as the last unit of `bounded`. Array.from would treat that lone unit as
  // its own broken "character" rather than dropping it; strip it before
  // expanding so the final bounded frame never ends on one.
  const lastUnit = bounded.charCodeAt(bounded.length - 1);
  if (lastUnit >= 0xd800 && lastUnit <= 0xdbff) bounded = bounded.slice(0, -1);
  // Array.from splits a string into code points, not UTF-16 units, so a
  // surrogate pair (an emoji, or anything outside the BMP) straddling the
  // bound is kept or dropped whole - a plain String#slice(0, N) can instead
  // cut the pair in half, leaving a lone, unpaired surrogate at the tail.
  return Array.from(bounded).slice(0, maxCodePoints).join("");
}

function prunedForStringify(value: unknown, depth: number, budget: { remaining: number }): unknown {
  if (budget.remaining <= 0) return typeof value === "string" ? "" : "…";
  budget.remaining -= 1;
  if (typeof value === "string") {
    const bounded = boundedPrefix(value, RAW_WARNING_FRAME_MAX_CHARS);
    return bounded === value ? value : `${bounded}…`;
  }
  if (depth >= RAW_WARNING_FRAME_MAX_DEPTH) {
    return typeof value === "object" && value !== null ? "…" : value;
  }
  if (Array.isArray(value)) {
    return value.slice(0, RAW_WARNING_FRAME_MAX_ARRAY_ITEMS).map((item) => prunedForStringify(item, depth + 1, budget));
  }
  if (typeof value === "object" && value !== null) {
    // A wire key literally named "__proto__" is a real, own, enumerable
    // property on the parsed object (JSON.parse never invokes a setter) -
    // assigning into a plain `{}` here would invoke Object.prototype's
    // __proto__ setter instead of creating an own property, silently
    // dropping that field from the pruned result. Object.create(null) has
    // no such setter, so every assignment below is a genuine own property.
    const pruned: Record<string, unknown> = Object.create(null);
    // for...in still needs one full enumeration of value's own keys (so
    // does Object.keys/Object.entries) — that step is O(keys), the same
    // order as the JSON.parse that produced this object in the first
    // place, so it adds no NEW asymptotic cost on top of what parsing the
    // wire frame already paid. What for...in avoids is allocating a
    // [key, value] PAIR per key and READING more values than survive the
    // cap: Object.entries reads and copies every value up front, while this
    // loop counts and breaks, reading (and copying) at most
    // RAW_WARNING_FRAME_MAX_OBJECT_KEYS + 1 property values regardless of
    // how many keys the object has.
    let taken = 0;
    for (const key in value) {
      if (!Object.hasOwn(value, key)) continue;
      if (taken >= RAW_WARNING_FRAME_MAX_OBJECT_KEYS || budget.remaining <= 0) break;
      taken++;
      // The key-count cap above bounds how many properties survive, but
      // says nothing about how long any one property NAME is — an
      // oversized key would otherwise ride through verbatim, the same
      // vector the value-length bound above closes for string values.
      const boundedKeyPrefix = boundedPrefix(key, RAW_WARNING_FRAME_MAX_CHARS);
      let boundedKey = boundedKeyPrefix === key ? key : `${boundedKeyPrefix}…`;
      // Two distinct keys can share their first RAW_WARNING_FRAME_MAX_CHARS
      // code points and truncate to the identical boundedKey - assigning
      // straight into `pruned` would then have the second key's value
      // silently overwrite the first's. Suffix a collision with a counter
      // until it lands on a key `pruned` doesn't already own, so both
      // survive (as two visibly-truncated keys) instead of one vanishing.
      for (let collision = 2; Object.hasOwn(pruned, boundedKey); collision++) {
        boundedKey = `${boundedKeyPrefix}…#${collision}`;
      }
      pruned[boundedKey] = prunedForStringify((value as Record<string, unknown>)[key], depth + 1, budget);
    }
    return pruned;
  }
  return value;
}

// Applied to every string a warning frame can put into the model — message,
// title, hint, source, and the raw fallback — so a multi-megabyte value
// anywhere in the frame can never reach ItemModel unbounded, not just via
// the message-less fallback path.
function boundedCodePoints(s: string): string {
  return boundedPrefix(s, RAW_WARNING_FRAME_MAX_CHARS);
}

// boundedCodePoints alone always keeps the LEADING RAW_WARNING_FRAME_MAX_CHARS
// code points — a message, title, hint, or source with more than that many
// leading blank code points followed by real content would then be stored as
// nothing but the blank prefix, rendering as nothing to every consumer.
// boundedWarningText answers "is there content" and bounds it starting from
// that content in the same walk: /\S/.exec finds the first non-whitespace
// index without copying anything, then boundedPrefix takes its own single,
// already-bounded slice starting there, so the window kept always contains
// the actual content instead of the padding in front of it. undefined when
// value isn't a non-blank string at all — hasWarningText and the fold are both
// built on this one walk, instead of each asking "is there content" and
// "bound it" as two separate scans. The fast
// path only skips leading padding when truncation is actually needed
// (matching boundedPrefix's own fast path): a short value already within the
// bound is returned unchanged, leading whitespace included, since nothing
// about it needs to be bounded away from at all.
function boundedWarningText(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const match = /\S/.exec(value);
  if (match === null) return undefined;
  if (value.length <= RAW_WARNING_FRAME_MAX_CHARS) return value;
  return boundedPrefix(value, RAW_WARNING_FRAME_MAX_CHARS, match.index);
}

function rawWarningFrame(params: WarningParams): string {
  const json = JSON.stringify(prunedForStringify(params, 0, { remaining: RAW_WARNING_FRAME_MAX_NODES }));
  // prunedForStringify above already keeps `json` itself small; this bound
  // is defense-in-depth for the code-point expansion specifically.
  return boundedCodePoints(json);
}

// The one validated shape every warning row — live with a turn, live
// without one, and (via the item this produces) a canonical reread — reads
// title/hint/source from. params is unknown on the wire (WarningParams'
// `warning` field, and title/hint despite their declared string type), so
// this is the single place that turns it into string-or-absent fields; every
// consumer reads the result, never params directly.
export interface WarningFold {
  text: string;
  title?: string;
  hint?: string;
  source?: string;
}

export function foldWarningParams(params: WarningParams): WarningFold {
  const text = warningMessage(params);
  const title = boundedWarningText(params.title);
  const hint = boundedWarningText(params.hint);
  const source = boundedWarningText(params.source);
  const foldedText = text || (title !== undefined || hint !== undefined ? "" : rawWarningFrame(params));
  return {
    // warningMessage and rawWarningFrame already bound the selected text;
    // keeping that result avoids rescanning it during the fold.
    text: foldedText,
    // Blank is absent too, not just "not a string" — hasWarningText's own
    // reading, which every consumer must apply anyway. Normalizing it here
    // means a future reader is never one missed hasWarningText call away
    // from rendering blank content. Bounded for the same reason as text:
    // an oversized title/hint/source reaching ItemModel.warning verbatim is
    // the same class of vector rawWarningFrame closes for the fallback.
    // These values are reused for the title/hint presence check above, so
    // each field is scanned and bounded once.
    title,
    hint,
    source,
  };
}

// Joins whichever WarningFold parts a caller has (title/text/hint, in
// whatever order it passes them) into one display string, filtering out
// blanks - the one composition rule every surface that renders a fold as a
// single string shares, so mobile's canonical projector and its live row
// (both pass text and hint, keeping title as their own field) never drift
// into two different join implementations.
export function joinWarningParts(parts: readonly (string | undefined)[]): string {
  return parts.filter(hasWarningText).join(" — ");
}

// Folds one live wire notification into model. Most notifications carry
// ref/threadId and are matched via notificationTargetsThread — routing those
// to the right ThreadModel is the caller's job (or not: a mismatch is a safe
// no-op here) either way. turn/completed splits on whether it names the
// model's active turn: the active turn's own settle ends the turn, while any
// other turn's is the no-active-turn announcement path and folds through
// foldNonActiveTurnCompleted instead.
//
// Generic over the model: every case builds its result by spreading `model`
// and overriding only the fields it owns, so a caller's extra fields (native's
// MobileConversation = ThreadModel & { items }, say) survive the fold at
// runtime AND in the return type — the caller needs no cast and no re-spread.
// The return is ThreadModel & ModelExtras<M>, so this holds for extra fields
// while the fields the fold rewrites keep ThreadModel's types.
export function applyNotification<M extends ThreadModel>(
  model: M,
  n: AnyNotification,
  now: number,
): ThreadModel & ModelExtras<M> {
  const next = applyNotificationToThread(model, n, now);
  if (!next.modelRetry || !notificationTargetsThread(n, model)) return publicModel<M>(next);
  // A pending retry is sticky (design doc Component 1): it survives deltas
  // and other mid-grind item completions, clearing only on a turn boundary or
  // the completion of the model's own output item — otherwise a provider
  // grinding through retries looks like the indicator vanished for no reason.
  const turnBoundary = n.method === "turn/completed" || n.method === "turn/started";
  const modelOutputCompleted =
    (n.method === "item/completed" && MODEL_OUTPUT_ITEM_TYPES.has(n.params.item.type)) ||
    (n.method === "history/updated" && (n.params.items ?? []).some((item) => MODEL_OUTPUT_ITEM_TYPES.has(item.type)));
  if (!turnBoundary && !modelOutputCompleted) return publicModel<M>(next);
  const cleared = { ...next };
  delete cleared.modelRetry;
  return publicModel<M>(cleared);
}

function applyNotificationToThread<M extends ThreadModel>(model: M, n: AnyNotification, now: number): M {
  switch (n.method) {
    case "turn/started": {
      if (!notificationTargetsThread(n, model)) return model;
      const { turn } = n.params;
      // The serving session is the model's own hydrate-carried imageSessionId
      // (the wire thread.sessionId the image bytes belong to) — never
      // params.ref or model.ref, which can be a stable workspace alias for a
      // different session than the one serving /s/{id}/images/{sha}.
      const imageSessionRoute = imageSessionRouteForSession(model.imageSessionId ?? model.threadId);
      // turns is presented everywhere else (mapTurn, findItemTurnId) as if
      // ids are unique. A duplicate here should never happen — the two known
      // ways it could (eptj, bz2z) are both fixed server-side — but blindly
      // appending would grow a second row sharing an id, silently setting up
      // turn/completed's same-id-replaces-both hazard below. Report loudly
      // (a reducer is a bad place to throw) and replace the existing row in
      // place instead of duplicating it.
      const existingIndex = model.turns.findIndex((t) => t.id === turn.id);
      if (existingIndex !== -1) {
        console.error(
          `applyNotification: turn/started turnId ${turn.id} already exists in model.turns — replacing it in place instead of appending a duplicate row (turn-id-uniqueness invariant violated)`,
        );
        return {
          ...model,
          turns: model.turns.map((t, i) => (i === existingIndex ? wireToTurnModel(turn, imageSessionRoute) : t)),
          activeTurnId: turn.id,
          lastFrameAt: now,
        };
      }
      return {
        ...model,
        turns: [...model.turns, wireToTurnModel(turn, imageSessionRoute)],
        activeTurnId: turn.id,
        lastFrameAt: now,
      };
    }

    case "turn/completed": {
      const params = n.params;
      const turnId = params.turn.id;
      if (!notificationTargetsThread(n, model)) return model;
      if (model.activeTurnId !== turnId) {
        // The status is authoritative and the transcript's id can be absent
        // while the session is active (a hydrate cut between turns, or the gap
        // after turn/completed at an inline boundary). A failed completion
        // arriving then is still the session's own failure, but its status
        // frame follows: the agent's failure exit (agent/session_lifecycle.go
        // endInputAtTurnFailure, kata hen0) emits EventSessionEnd with Reason
        // "turn_failed", announced as thread/status/changed(idle) with the
        // capabilities inline, and that frame owns the transition (the
        // work-clock anchor goes with it — the invariant thread/status/changed
        // keeps below: no live anchor at rest). A failed completion for a turn
        // another turn has since superseded (the id names a different turn) is
        // bookkeeping about the past and leaves the status too.
        return foldNonActiveTurnCompleted(model, turnId, params.turn, now);
      }
      const oldTurn = model.turns.find((t) => t.id === turnId);
      const stamp = params.turn;
      let settledTurn: TurnModel;
      if (stamp.itemsView === "full") {
        settledTurn = wireToTurnModel(stamp, imageSessionRouteForSession(model.imageSessionId ?? model.threadId));
        // Same helper composition as item/completed's existing-item branch
        // below (mergeCompletedText/mergeItemImages/mergeReasoning/
        // mergeArguments/mergeObservedTiming read/write disjoint fields off the
        // same `old` reference, so composition order is free) — this branch has
        // its own settled items rather than item/completed's single one, so it
        // maps instead of a single mapItem call. "Full" replaces the item set,
        // not every field: an image list a payload omits is kept off `old`,
        // exactly as item/completed keeps it.
        settledTurn.items = settledTurn.items.map((item) => {
          const old = oldTurn?.items.find((o) => itemIdentityMatches(o, item));
          const identitySettled = old ? mergeItemIdentityMetadata(old, item) : item;
          return mergeObservedTiming(
            mergeArguments(mergeReasoning(mergeItemImages(mergeCompletedText(identitySettled, old), old), old), old),
            old,
            now,
          );
        });
      } else {
        // The live wire's settle stamp never carries items — every live
        // settle site (EventUserInput, EventGoalContinuation, EventError,
        // EventSessionEnd in internal/appprojector/appwire_projection.go)
        // emits a bare Turn{ID,Status[,Error]} with Items nil, ItemsView "".
        // itemsView !== "full" means "this payload has nothing to say about
        // items," not "the turn has no items" — keep whatever the model
        // already accumulated via item/started + deltas + item/completed,
        // folding any item still mid-stream through settleItem (a just-
        // settled turn cannot legitimately still have a pending item).
        settledTurn = {
          ...wireToTurnScalars(stamp),
          items: (oldTurn?.items ?? []).map((item) => settleItem(item, now)),
        };
      }
      return {
        ...model,
        turns: settleFirstMatchingTurn(model.turns, turnId, settledTurn),
        activeTurnId: undefined,
        // The active turn just ended: its start anchor is now stale (there is
        // no live push to refresh it), so clear it in lockstep with activeTurnId
        // to stop the work-clock ticking against a completed turn.
        activeTurnStartedAt: undefined,
        // The status is thread/status/changed's, not this frame's: a completed
        // turn is followed by one (idle at session end, active when the next
        // turn runs inline), and so is a failed one — the agent's failure exit
        // (agent/session_lifecycle.go endInputAtTurnFailure, kata hen0) emits
        // EventSessionEnd with Reason "turn_failed", announced as
        // thread/status/changed(idle), and that frame owns the transition.
        status: model.status,
        lastFrameAt: now,
      };
    }

    case "item/started": {
      if (!notificationTargetsThread(n, model)) return model;
      const { turnId, item } = n.params;
      const targetTurnId = resolveInsertTurnId(model, turnId, item.turnId);
      if (!targetTurnId) return { ...model, lastFrameAt: now };
      return {
        ...model,
        turns: mapTurn(model.turns, targetTurnId, (turn) => ({
          ...turn,
          items: upsertTurnItems(
            turn.items,
            [item],
            now,
            imageSessionRouteForSession(model.imageSessionId ?? model.threadId),
          ),
        })),
        lastFrameAt: now,
      };
    }

    case "item/completed": {
      if (!notificationTargetsThread(n, model)) return model;
      const { turnId, item } = n.params;
      const incoming = wireItemToModel(item, imageSessionRouteForSession(model.imageSessionId ?? model.threadId));
      // A live watcher on a long turn sees nothing move on thread/status/
      // changed until the turn ends, however many tool calls fail inside it
      // (kata 895d) — item/completed is the finer-grained carrier, stamped
      // by the server only on the item whose completion actually moved the
      // count. Applied exactly like thread/status/changed's: absent means
      // "no change", never "nobody counted".
      const failedToolCalls = n.params.failedToolCalls ?? model.failedToolCalls;
      const existingTurnId = findItemTurnId(model, turnId, incoming);
      if (existingTurnId) {
        return {
          ...model,
          turns: mapTurn(model.turns, existingTurnId, (turn) => ({
            ...turn,
            items: mapItemByIdentity(turn.items, incoming, (old) =>
              mergeObservedTiming(
                mergeArguments(
                  mergeReasoning(
                    mergeItemImages(mergeCompletedText(mergeItemIdentityMetadata(old, incoming), old), old),
                    old,
                  ),
                  old,
                ),
                old,
                now,
              ),
            ),
          })),
          failedToolCalls,
          lastFrameAt: now,
        };
      }
      // Some item types (userMessage, systemMessage) go straight to
      // item/completed with no preceding item/started — see
      // internal/appprojector/appwire_projection.go (EventUserInput,
      // EventGoalContinuation): a new turn opens via turn/started with an
      // empty turn, then item/completed alone carries the item. Insert
      // rather than drop it.
      const insertTurnId = resolveInsertTurnId(model, turnId, item.turnId);
      if (!insertTurnId) return { ...model, failedToolCalls, lastFrameAt: now };
      return {
        ...model,
        turns: mapTurn(model.turns, insertTurnId, (turn) => ({
          ...turn,
          items: [...turn.items, incoming],
        })),
        failedToolCalls,
        lastFrameAt: now,
      };
    }

    case "item/agentMessage/delta": {
      if (!notificationTargetsThread(n, model)) return model;
      const params = n.params;
      const targetTurnId = findItemTurnId(model, params.turnId, params.itemId);
      if (!targetTurnId) return { ...model, lastFrameAt: now };
      return {
        ...model,
        turns: mapTurn(model.turns, targetTurnId, (turn) => ({
          ...turn,
          items: mapItem(turn.items, params.itemId, (item) =>
            copyItemTextPresence(item, {
              ...item,
              // O(1) — see appendChunk's doc comment.
              pendingText: appendChunk(item.pendingText, params.delta),
            }),
          ),
        })),
        lastFrameAt: now,
      };
    }

    case "item/agentMessage/reset": {
      if (!notificationTargetsThread(n, model)) return model;
      const params = n.params;
      const targetTurnId = findItemTurnId(model, params.turnId, params.itemId);
      if (!targetTurnId) return { ...model, lastFrameAt: now };
      return {
        ...model,
        turns: mapTurn(model.turns, targetTurnId, (turn) => ({
          ...turn,
          items: turn.items.filter((it) => it.id !== params.itemId),
        })),
        lastFrameAt: now,
      };
    }

    case "item/reasoning/summaryTextDelta": {
      if (!notificationTargetsThread(n, model)) return model;
      const params = n.params;
      const targetTurnId = findItemTurnId(model, params.turnId, params.itemId);
      if (!targetTurnId) return { ...model, lastFrameAt: now };
      return {
        ...model,
        turns: mapTurn(model.turns, targetTurnId, (turn) => ({
          ...turn,
          items: mapItem(turn.items, params.itemId, (item) =>
            appendReasoningDelta(item, params.summaryIndex, params.delta, now),
          ),
        })),
        lastFrameAt: now,
      };
    }

    case "item/toolOutput/delta": {
      if (!notificationTargetsThread(n, model)) return model;
      const params = n.params;
      const targetTurnId = findItemTurnId(model, params.turnId, params.itemId);
      if (!targetTurnId) return { ...model, lastFrameAt: now };
      return {
        ...model,
        turns: mapTurn(model.turns, targetTurnId, (turn) => ({
          ...turn,
          items: mapItem(turn.items, params.itemId, (item) =>
            copyItemTextPresence(item, {
              ...item,
              output: (item.output ?? "") + params.delta,
            }),
          ),
        })),
        lastFrameAt: now,
      };
    }

    case "history/updated": {
      if (!notificationTargetsThread(n, model)) return model;
      return applyHistoryUpdated(model, n.params, now);
    }

    case "evener/thread/resync": {
      if (!notificationTargetsThread(n, model)) return model;
      return applyResync(model, n.params, now);
    }

    case "overlay/upserted": {
      if (!notificationTargetsThread(n, model)) return model;
      const item = n.params.item;
      return applyOverlayChange(model, now, (overlay) => ({ ...overlay, [item.key]: item }));
    }

    case "overlay/delta": {
      if (!notificationTargetsThread(n, model)) return model;
      return applyOverlayDelta(model, n.params, now);
    }

    case "overlay/reset": {
      if (!notificationTargetsThread(n, model)) return model;
      const streamId = n.params.streamId;
      return applyOverlayChange(model, now, (overlay) => filterOverlay(overlay, (item) => item.streamId === streamId));
    }

    case "overlay/end": {
      if (!notificationTargetsThread(n, model)) return model;
      const roundId = n.params.roundId;
      return applyOverlayChange(model, now, (overlay) =>
        filterOverlay(overlay, (item) => item.kind !== "notice" && item.roundId === roundId),
      );
    }

    case "thread/queueChanged": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, queue: n.params.queue, lastFrameAt: now };
    }

    case "thread/status/changed": {
      if (!notificationTargetsThread(n, model)) return model;
      const status = n.params.status;
      // The running turn is the frame's activeTurnId, and none when it names
      // none: an idle frame ends the running turn.
      const { runningTurnId: _previous, ...rest } = model;
      return {
        ...(rest as M),
        ...(n.params.activeTurnId ? { runningTurnId: n.params.activeTurnId } : {}),
        status,
        // The work-clock anchor (activeTurnStartedAt) has no live push to
        // refresh it, so a cold-hydrated live anchor would keep clocking
        // now-minus-anchor forever once the turn ends (StatusRow.tsx feeds it to
        // totalWorkMillis unconditionally). Drop it on any non-active
        // transition so the model never carries a live anchor while at rest.
        activeTurnStartedAt: status.type === "active" ? model.activeTurnStartedAt : undefined,
        // The failure count is otherwise snapshot-only, so a client that
        // attached while the session was clean would keep saying nothing
        // however many failures followed - the watcher the count exists for.
        // A status change is a turn boundary, the only moment it can have
        // moved. Absent here means "no update" (an old daemon omits it), never
        // "nobody counted": clearing it would blank a figure the hydrate
        // legitimately gave us. Absence at HYDRATE is where unknown lives.
        failedToolCalls: n.params.failedToolCalls ?? model.failedToolCalls,
        // askPending is snapshot-authoritative and this is the wire refreshing
        // it, not the reducer deriving it: the hub stamps the flag on the frame
        // that goes with every clear of the pending set (a resolving user turn,
        // an interrupt), so a client stops showing "question waiting" without a
        // reread. Same absent-means-no-update rule as the count above; the ask
        // dock's own in-tool signal is still separate and still not this.
        askPending: n.params.askPending ?? model.askPending,
        // Capabilities are snapshot-only too, and two of them (send, queue)
        // are defined BY this very transition: the hub gates send on "no turn
        // in flight" and queue on "a turn in flight"
        // (server/appwire_runtime.go's appCapabilities; steer is harness
        // support alone). A set cut before the
        // turn therefore describes the wrong session by the time the composer
        // reads it back, which is how a running session came to show no Steer,
        // no Stop and a dead Send until the page was reloaded (kata 06t8).
        // Same absent-means-no-update rule as the count above: a source that
        // omits the capability sends none, and clearing on absence would strip
        // the session of every action it advertised.
        capabilities: n.params.capabilities ?? model.capabilities,
        capabilitySource: n.params.capabilities ? "statusFrame" : model.capabilitySource,
        lastFrameAt: now,
      };
    }

    case "thread/model/changed": {
      if (!notificationTargetsThread(n, model)) return model;
      // ThreadModelChangedParams (appwire/types.go:867-874) carries
      // reasoningEffortLevels/supportsReasoning alongside modelProvider/
      // model, describing the NEW model's full reasoning profile - not a
      // partial patch, so an omitted/empty ladder on the new payload
      // replaces (not preserves) whatever the old model's picker showed.
      return {
        ...model,
        modelProvider: n.params.modelProvider,
        model: n.params.model,
        reasoningEffortLevels: n.params.reasoningEffortLevels ?? [],
        supportsReasoning: n.params.supportsReasoning ?? false,
        lastFrameAt: now,
      };
    }

    case "thread/reasoning-effort/changed": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, reasoningEffort: n.params.reasoningEffort, lastFrameAt: now };
    }

    case "evener/goal/updated": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, goal: n.params.goal ?? null, lastFrameAt: now };
    }

    case "evener/notes/updated": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, humanNote: n.params.humanNote ?? "", agentNote: n.params.agentNote ?? "", lastFrameAt: now };
    }

    case "evener/urls/updated": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, sessionUrls: n.params.urls ?? [], lastFrameAt: now };
    }

    case "thread/vision-model/changed": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, visionModel: n.params.visionModel, lastFrameAt: now };
    }

    case "evener/task/updated": {
      if (!notificationTargetsThread(n, model)) return model;
      return {
        ...model,
        tasks: {
          total: n.params.total,
          done: n.params.done,
          ...(n.params.cancelled === undefined ? {} : { cancelled: n.params.cancelled }),
          ...(n.params.remaining === undefined
            ? n.params.cancelled === undefined
              ? {}
              : { remaining: Math.max(0, n.params.total - n.params.done - n.params.cancelled) }
            : { remaining: n.params.remaining }),
          ...(n.params.current ? { current: n.params.current } : {}),
        },
        lastFrameAt: now,
      };
    }

    case "evener/thread/name/changed": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, name: n.params.name, lastFrameAt: now };
    }

    // PendingEscalations is THREAD-level human-client state, never a
    // transcript item (appwire/types.go's ThreadEvener.PendingEscalations doc
    // comment: "a HUMAN-CLIENT field only ... never part of the model's
    // transcript or any model-visible projection"). The catalog entry
    // (appwire/protocol.go:185) declares this notification with its real
    // generated payload type (SandboxEscalationRequested), used verbatim
    // here rather than a local "(inline)" interface. See
    // upsertPendingEscalation for the dedup rationale.
    case "evener/sandbox/escalation/requested": {
      if (!notificationTargetsThread(n, model)) return model;
      return {
        ...model,
        pendingEscalations: upsertPendingEscalation(model.pendingEscalations, n.params),
        lastFrameAt: now,
      };
    }

    // The resolved twin of requested (wire-honesty spec Part B): a previously-
    // raised escalation left the pending set — resolved, turn-interrupted, or
    // cleared by session close. The payload carries no outcome (see
    // SandboxEscalationResolved's Go doc); this client clears its own copy by
    // id via the same helper the local resolve action reuses. resolvePending-
    // Escalation is a same-reference no-op for an id we never held, but a
    // targeted live frame still stamps lastFrameAt like every other case here.
    case "evener/sandbox/escalation/resolved": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...resolvePendingEscalationModel(model, n.params.escalationId), lastFrameAt: now };
    }

    // The model holds no job LIST at this layer, so the lifecycle pair leaves
    // its liveness signal (lastFrameAt) and a timestamp the jobs panel watches
    // to know its list went stale — the whole reason the panel need not poll.
    // Both ends bump it: starting and finishing both change what
    // evener/jobs/list returns. Live steering (below) is handled separately:
    // unlike jobs, it becomes a transcript item.
    case "evener/job/started":
    case "evener/job/finished": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, jobsUpdatedAt: now, lastFrameAt: now };
    }

    case "evener/delegate/updated": {
      if (!notificationTargetsThread(n, model)) return model;
      const delegates = upsertStableDelegate(model.delegates ?? [], n.params.delegate);
      if (delegates === model.delegates) return { ...model, jobsUpdatedAt: now, lastFrameAt: now };
      return { ...model, delegates, jobsUpdatedAt: now, lastFrameAt: now };
    }

    case "evener/jobs/treeUpdated": {
      if (!notificationTargetsThread(n, model)) return model;
      if (model.jobsTreeRevision !== null && n.params.revision <= model.jobsTreeRevision) return model;
      return { ...model, jobsTreeRevision: n.params.revision, jobsUpdatedAt: now };
    }

    case "warning": {
      if (!notificationTargetsThread(n, model)) return model;
      const activeTurnId = model.activeTurnId;
      // No active turn: there is nowhere wire-true to put it, and — unlike
      // evener/steering/injected's race window — warnings are not transcript-
      // persisted at all (internal/apptranscript has no warning-item
      // conversion), so the next snapshot would not carry it either. Drop
      // it client-side; only the liveness signal survives.
      if (!activeTurnId) return { ...model, lastFrameAt: now };
      const params = n.params;
      const folded = foldWarningParams(params);
      return {
        ...model,
        turns: mapTurn(model.turns, activeTurnId, (turn) => {
          // Same collision-proofing as evener/steering/injected: count what's
          // already there rather than a global counter, so multiple
          // warnings in one turn land with distinct, order-preserving ids.
          const warningCount = turn.items.filter((it) => it.type === "warning").length;
          const item: ItemModel = {
            id: `item_warning_live_${activeTurnId}_${warningCount}`,
            turnId: activeTurnId,
            type: "warning",
            text: folded.text,
            status: "completed",
            warning: { source: folded.source, title: folded.title, hint: folded.hint },
          };
          return { ...turn, items: [...turn.items, item] };
        }),
        lastFrameAt: now,
      };
    }

    case "evener/thread/modelRetry": {
      if (!notificationTargetsThread(n, model)) return model;
      const params = n.params;
      // No lastFrameAt restamp on purpose — see ThreadModel.modelRetry. The
      // quiet/stall clock is measuring a real silence; this explains it rather
      // than resetting it.
      return {
        ...model,
        modelRetry: {
          attempt: params.attempt,
          maxAttempts: params.maxAttempts,
          delayMs: params.delayMs,
          ...(params.errorClass ? { errorClass: params.errorClass } : {}),
          ...(params.statusCode ? { statusCode: params.statusCode } : {}),
          ...(params.turnId ? { turnId: params.turnId } : {}),
          ...(params.model ? { model: params.model } : {}),
          groupElapsedMs: params.groupElapsedMs,
          attemptCap: params.attemptCap,
          receivedAt: now,
        },
      };
    }

    case "evener/steering/injected": {
      if (!notificationTargetsThread(n, model)) return model;
      const activeTurnId = model.activeTurnId;
      // The server only injects steering into an in-flight turn; if the
      // model has none (e.g. this arrived just after the turn's own settle),
      // there is nowhere wire-true to put it — a race recovered by the next
      // snapshot, not a turn to fabricate client-side.
      if (!activeTurnId) return { ...model, lastFrameAt: now };
      const params = n.params;
      return {
        ...model,
        turns: mapTurn(model.turns, activeTurnId, (turn) => {
          // id must be unique across multiple steers landing in the same
          // turn; count what's already there rather than a global counter,
          // mirroring the historical reload shape's per-turn indexing
          // (internal/apptranscript/apptranscript.go:211-229, item_steering_<n>).
          const steeringCount = turn.items.filter((it) => it.type === "steering").length;
          const item: ItemModel & { clientMutationId?: string } = {
            id: `item_steering_live_${activeTurnId}_${steeringCount}`,
            turnId: activeTurnId,
            type: "steering",
            ...(params.startedAt !== undefined ? { startedAt: epochMsToISO(params.startedAt) } : {}),
            text: params.text ?? "",
            images: imagesToItemImagesForSession(
              params.images,
              imageSessionRouteForSession(model.imageSessionId ?? model.threadId),
            ),
            status: "completed",
            source: params.source,
            steeringKind: params.kind,
          };
          if (params.clientMutationId) item.clientMutationId = params.clientMutationId;
          return { ...turn, items: [...turn.items, item] };
        }),
        lastFrameAt: now,
      };
    }

    default:
      return model;
  }
}
