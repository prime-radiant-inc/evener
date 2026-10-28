// Wire-item hydration: image mapping, text-presence and identity-only marks,
// stable-delegate helpers, and the wire-to-model item conversions. A leaf over
// `model`, `stableDelegate` and `types.gen`.

import { pendingTextJoined } from "./chunkview";
import type { ItemImage, ItemModel, ThreadModel, TurnModel } from "./model";
import { joinDelegateState, revisionOf } from "./stableDelegate";
import type {
  EvenerDelegateInfo,
  InputItem,
  OutputImage,
  Thread,
  ThreadItem,
  ThreadReadResponse,
  Turn,
} from "./types.gen";

export function cloneStableDelegate(delegate: EvenerDelegateInfo): EvenerDelegateInfo {
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

function mergeStableDelegate(current: EvenerDelegateInfo, incoming: EvenerDelegateInfo): EvenerDelegateInfo {
  const incomingWins = revisionOf(incoming.projectionRevision) > revisionOf(current.projectionRevision);
  const joined = joinDelegateState(incoming, current, incomingWins);
  return incomingWins ? cloneStableDelegate(joined) : joined;
}

export function upsertStableDelegate(
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

export function epochMsToISO(ms: number | undefined): string | undefined {
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
export function epochSecondsToISO(seconds: number | undefined): string | undefined {
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
export function imagesToItemImagesForSession(
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
export function outputImagesToItemImages(images: OutputImage[] | undefined): ItemImage[] | undefined {
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

// The item fields the page merge preserves through `??` — mergePageItem's
// per-field list, plus position, which mergeItemIdentityMetadata keeps the
// same way: a null or undefined on the fresh side falls through to the older
// side's value, so a fresh nullish cannot hide older data the merged result
// keeps, and an older nullish carries no data of its own. Every other field
// the coverage walk reaches is spread-merged ({ ...older, ...newer }; the
// fresh side's own property wins, even when its value is undefined), where
// the property's presence — read below — is the absence marker.
export const itemNullishMergedFields = new Set([
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
export const freshSuppliedNullishFallbackFields = new Set([
  ...itemNullishMergedFields,
  "reasoningSummaries",
  "observedStartedAt",
  "observedCompletedAt",
]);

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
export function itemIsIdentityOnly(item: ItemModel): boolean {
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
export function wireItemToModel(item: ThreadItem, imageSessionRoute?: string): ItemModel {
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
  // text, so it seeds an authoritative EMPTY summary ([[""]], which
  // display-time joining drops to no paragraph) instead of leaving
  // reasoningSummaries unset.
  if (item.type === "reasoning" && item.text !== undefined) {
    model.reasoningSummaries = [[item.text]];
  }
  return model;
}

export function wireToTurnScalars(turn: Turn): Omit<TurnModel, "items"> {
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

export function wireToTurnModel(turn: Turn, imageSessionRoute?: string): TurnModel {
  return {
    ...wireToTurnScalars(turn),
    items: (turn.items ?? []).map((item) => wireItemToModel(item, imageSessionRoute)),
  };
}

// evener.activeTurnId is the primary signal; a turn already marked inProgress
// in the snapshot is the fallback for daemons/sources that don't populate it
// (mirrors activeTurnIDFromThread in cmd/serf-hub/assets/appwire.js).
export function activeTurnIdFromThread(thread: Thread): string | undefined {
  if (thread.evener.activeTurnId) return thread.evener.activeTurnId;
  return thread.turns?.find((t) => t.status === "inProgress")?.id;
}

export const isToolResultId = (id: string) => id.startsWith("item_tool_result_");
export const isToolCallId = (id: string) => id.startsWith("item_tool_") && !isToolResultId(id);

// The same wire id conventions, public for callers that must classify items
// exactly as the callId fold does (it removes item_tool_result_* results and
// rewrites item_tool_* calls). The mobile store's skeleton strip needs the
// distinction: a rewritten CALL host can carry content the removed result was
// the only other holder of.
export const isToolResultItemId: (id: string) => boolean = isToolResultId;
export const isToolCallItemId: (id: string) => boolean = isToolCallId;

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

// stampThreadImageURLs (output_images.go) prefers the wire session id and
// falls back to the thread id, trimming both (strings.TrimSpace); the
// client-side rebuild matches it exactly — a whitespace-padded session id
// must not win the fallback and escape to a /s/%20.../images route the hub
// would 404 on while the trimmed thread id would have served.
export function wireImageSessionId(thread: Thread): string {
  return thread.sessionId.trim() || thread.id.trim();
}

// Every field a read sets except the transcript itself (turns, history,
// overlay and the running turn).
export function threadFields(resp: ThreadReadResponse, ref: string, now: number): Omit<ThreadModel, "turns"> {
  const thread = resp.thread;
  // The snapshot's own stamped urls already win per-image (url-first
  // precedence); the route only matters for sha-bearing images that arrived
  // WITHOUT a stamp — replayed input images from a read path that didn't
  // re-stamp, or older-producer frames.
  const imageSessionId = wireImageSessionId(thread);
  const fields: Omit<ThreadModel, "turns"> = {
    ref,
    threadId: thread.id,
    ...(thread.evener.parentRef === undefined ? {} : { parentRef: thread.evener.parentRef }),
    imageSessionId,
    ...(thread.evener.instanceId === undefined ? {} : { instanceId: thread.evener.instanceId }),
    name: thread.name ?? "",
    status: thread.status,
    resumeRequired: thread.evener.resumeRequired ?? false,
    resumeOnlyFoldable: thread.evener.resumeOnlyFoldable ?? false,
    // A read's fields are merged over the existing model, so omitting the key
    // when the snapshot has no root would keep a previous value and leave link
    // actions pointing at a directory this snapshot no longer advertises.
    // Present-and-undefined clears it; the key is then dropped so a model
    // without a root keeps the shape it had before this field existed.
    artifactsDir: thread.evener.artifactsDir || undefined,
    modelProvider: thread.modelProvider,
    // Thread has no separate "model id" field on the wire snapshot — only
    // ModelProvider, which appwire/types_thread.go documents as overloaded to
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
      ? {
          diagnostics: {
            commands: thread.evener.diagnostics.commands?.map((command) => ({ ...command })),
            plugins: thread.evener.diagnostics.plugins?.map((plugin) => ({ name: plugin.name })),
          },
        }
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
    contextAssumed: thread.evener.contextAssumed ?? false,
    usage: thread.evener.usage ?? null,
    cost: thread.evener.cost ?? null,
    // Passed straight through, undefined and all: absent is "nobody counted"
    // and must not become a 0 that reads as "nothing failed".
    failedToolCalls: thread.evener.failedToolCalls,
    ...(thread.evener.access ? { access: { ...thread.evener.access } } : {}),
    workMillis: thread.evener.workMillis ?? 0,
    activeTurnStartedAt: epochMsToISO(thread.evener.activeTurnStartedAt),
    lastTurnEndedAt: epochMsToISO(thread.evener.lastTurnEndedAt),
    reasoningEffortLevels: thread.evener.reasoningEffortLevels ?? [],
    supportsReasoning: thread.evener.supportsReasoning ?? false,
    cwd: thread.cwd,
    gitBranch: thread.gitInfo?.branch,
    projectPath: thread.projectPath,
    createdAt: epochSecondsToISO(thread.createdAt),
    updatedAt: epochSecondsToISO(thread.updatedAt),
  };
  // A read's fields are merged over the existing model, so omitting the key
  // when the snapshot has no root would keep a previous value and leave link
  // actions pointing at a directory this snapshot no longer advertises.
  // Present-and-undefined clears it; the key is then dropped so a model
  // without a root keeps the shape it had before this field existed.
  if (fields.artifactsDir === undefined) delete fields.artifactsDir;
  return fields;
}
