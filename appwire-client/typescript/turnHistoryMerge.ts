// Tool-call fold and turn/page history merge: pairs call and result items,
// coalesces turn fragments, and places older pages against retained turns.
// Depends on `transcriptItems`; knows nothing of versioned history.

import type { ItemModel, ThreadModel, TurnModel } from "./model";
import {
  copyItemTextPresence,
  freshSuppliedNullishFallbackFields,
  imageSessionRouteForSession,
  isToolCallId,
  isToolResultId,
  itemIsIdentityOnly,
  itemNullishMergedFields,
  itemTextPresence,
  markItemTextOmitted,
  wireToTurnModel,
} from "./transcriptItems";
import type { ThreadTurnsListResponse } from "./types.gen";

// Reload projects a tool CALL and its RESULT as two items sharing a callId, in
// separate wire turns (apptranscript.TurnsFromFile mints one turn per transcript
// entry). Collapse them the way the live path already produces a single item:
// the call supplies id + argumentsJSON + startedAt, the result supplies output +
// error + exitCode + completedAt + settled status. A turn emptied by the merge is
// dropped unless it was already empty or carries canonical turn metadata.
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
  // the rewritten call. Retention-only metadata: the
  // window bound needs it — a folded call is the only payload behind its
  // result's row once the row set keeps the result but not the call —
  // but it must stay OUT of the provenance membership, whose consumers
  // (the strip's real-source test, duplicate-reconciliation freshness)
  // treat a surviving candidate as a source the call never merged from.
  toolResultFolds: WeakMap<ItemModel, readonly ItemModel[]>;
  // The content the fold kept, written where the fold decided to keep it:
  // one record per merged item — the
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
// other (strict value equality would let an explicit empty text and an
// omitted text share the supply, because both hydrate to "" and the marker
// is invisible to Object.entries). The identity anchors
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

export function mergeToolCallsByCallId(
  turns: TurnModel[],
  context?: ToolItemMergeContext,
  view?: ToolFoldView,
): TurnModel[] {
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
      authorship: newer.authorship ?? older.authorship,
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
// The precedence itself is FIELD-scoped. A
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
    // can identity-match an accumulated candidate the original did not:
    // keep folding the merged result against the accumulated candidates
    // until no match remains. Each step consumes one candidate, so the loop
    // always ends.
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
      // The provenance records in LIST order, not freshness order: a side's
      // leaves must read, in the tool fold's reversed candidate walk, in the
      // same precedence the per-field resolution used — the later duplicate
      // that won a field is found first.
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
// actually covers. Text counts as supplied only when a
// fresh leaf PROVIDED it (the omitted marker means the wire carried
// none). Every other field counts by the merge's OWN rule for it: the nullish-fallback fields only when the leaf
// carries a value (null falls through to the older side), the rank-merged
// status only when a fresh leaf's status is the value the merge kept
// (a lower-ranked fresh status loses to the older alias's, and the
// inherited value must not claim precedence), and the
// spread-merged fields by property PRESENCE — a fresh leaf's own
// undefined clears the field, and the
// clearing property must claim precedence or a later stale duplicate's
// value survives the reconciliation. An identity-only leaf supplies
// nothing at all — not even its identity (a remembered skeleton must not
// claim per-field precedence over a restored item's fields) — and an item the context never saw speaks only for itself.
function freshSuppliedFields(context: ToolItemMergeContext, item: ItemModel): ReadonlySet<string> {
  const supplied = new Set<string>();
  // The rank rule: a fresh leaf's status only reaches the merge when it
  // wins the rank chain — an inProgress fragment under a failed alias
  // leaves the alias's failure in place — so the merged status counts as
  // fresh-supplied only when a fresh leaf's own status is what the merge
  // kept.
  let statusSupplied = false;
  const record = (leaf: ItemModel): void => {
    if (itemIsIdentityOnly(leaf)) return;
    for (const [key, value] of Object.entries(leaf)) {
      // Text follows the presence marker, not the property — see below.
      if (key === "text") continue;
      if (freshSuppliedNullishFallbackFields.has(key)) {
        // The merge inherits the older side's value when the leaf's is
        // null or undefined, so neither counts as supplied.
        if (value !== null && value !== undefined) supplied.add(key);
        continue;
      }
      if (key === "status") {
        if (value !== undefined && value === item.status) statusSupplied = true;
        continue;
      }
      // Every other field is spread-merged by property presence: the
      // leaf's own undefined CLEARS the field, and the clearing property
      // counts as supplied.
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

// Per-field reconciliation of a duplicate pair:
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

// Two turns carry conflicting keyed identities when they hold items of the
// same id under two different supplied transcript keys. itemIdentityMatches
// already refuses that pair directly, but a keyless alias (an item with the
// same id and no key) matches BOTH by id, and the group merge below would
// otherwise let it bridge them into one turn and combine their payloads. A
// keyless alias is unambiguous only while the turns it touches agree on a
// transcript key; two such turns are distinct recorded turns and stay apart.
function turnsCarryConflictingKeys(left: TurnModel, right: TurnModel): boolean {
  return left.items.some((leftItem) =>
    right.items.some(
      (rightItem) =>
        leftItem.id === rightItem.id &&
        Boolean(leftItem.transcriptKey) &&
        Boolean(rightItem.transcriptKey) &&
        leftItem.transcriptKey !== rightItem.transcriptKey,
    ),
  );
}

function groupsCarryConflictingKeys(left: TurnFragmentGroup, right: TurnFragmentGroup): boolean {
  return left.fragments.some((leftFragment) =>
    right.fragments.some((rightFragment) => turnsCarryConflictingKeys(leftFragment.turn, rightFragment.turn)),
  );
}

function groupConflictsWithFragment(group: TurnFragmentGroup, fragment: TurnFragment): boolean {
  return group.fragments.some((existing) => turnsCarryConflictingKeys(existing.turn, fragment.turn));
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
    // The target is the first matching group the fragment can join without a
    // same-id conflicting transcript key. It is not always matching[0]: a
    // fragment that conflicts with an earlier group may still fold into a
    // later compatible one — refusing outright would leave a shared keyless
    // item standing twice, the duplication coalescing exists to remove.
    const target = matching.find((group) => !groupConflictsWithFragment(group, fragment));
    if (target === undefined) {
      groups.push({ fragments: [fragment], firstOrder: order });
      return;
    }
    target.fragments.push(fragment);
    // A fragment that matches several groups joins them only when they agree
    // on their keyed identities. A keyless alias matching two turns whose
    // supplied keys differ would otherwise bridge distinct recorded turns into
    // one; the conflicting groups stay separate instead. Each candidate is
    // checked against the group as it grows, so two candidates that conflict
    // with each other but not with the target are not both absorbed. A group
    // the fragment cannot join (it conflicts) is skipped, never force-added.
    const absorbed: TurnFragmentGroup[] = [];
    for (const group of matching) {
      if (group === target) continue;
      if (groupsCarryConflictingKeys(target, group)) continue;
      target.fragments.push(...group.fragments);
      absorbed.push(group);
    }
    for (const group of absorbed.reverse()) {
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
  // happened, never re-derived here: the supplier sets
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
  // mobile store's alias-consumption gate: the store consumes this
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

  // The store's alias-consumption gate asks the package's own
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
