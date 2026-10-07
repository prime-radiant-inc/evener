import { type ActivityDelegate, type ActivityTree, activityNodeID } from "./activityData";
import { type ActivityDelegateRow, type ActivityJobRow, indexActivityEntities, watchRowID } from "./activityRows";
import type { ItemModel, TurnModel } from "./model";
import type { EvenerDelegateInfo } from "./types.gen";
import { compareTranscriptPosition, foldWatchSummaries, type WatchSummary } from "./watchRows";

export interface OpenTarget {
  ref: string;
  parentRef: string;
}

interface EntityViewState {
  id: string;
  logicalId: string;
  ownerRef: string;
  stale: boolean;
  ended: boolean;
}

export interface JobEntityView extends EntityViewState {
  kind: "job";
  row: ActivityJobRow;
  open: OpenTarget;
}

interface DelegateEntityViewBase extends EntityViewState {
  kind: "delegate";
  name?: string;
  open: OpenTarget;
}

export type DelegateEntityView =
  | (DelegateEntityViewBase & {
      row: ActivityDelegateRow;
      stable?: never;
    })
  | (DelegateEntityViewBase & {
      row?: never;
      stable: EvenerDelegateInfo;
    });

export interface WatchEntityView extends EntityViewState {
  kind: "watch";
  watch: WatchSummary;
  lastKnown: true;
  stale: true;
  ended: false;
}

export type EntityView = JobEntityView | DelegateEntityView | WatchEntityView;

interface EntityViewSources {
  sessionRef: string;
  tree?: ActivityTree;
  delegates?: EvenerDelegateInfo[];
  turns: TurnModel[];
  stale: boolean;
  ended: boolean;
}

function jobEntity(row: ActivityJobRow, stale: boolean, ended: boolean): JobEntityView {
  return {
    kind: "job",
    id: row.id,
    logicalId: row.job.jobId,
    ownerRef: row.job.ownerRef,
    row,
    open: {
      ref: row.transcriptRef ?? `job:${row.job.jobId}`,
      parentRef: row.parentRef,
    },
    stale,
    ended,
  };
}

function treeDelegateEntity(row: ActivityDelegateRow, stale: boolean, ended: boolean): DelegateEntityView {
  return {
    kind: "delegate",
    name: row.delegate.name,
    id: row.id,
    logicalId: row.delegate.delegateId,
    ownerRef: row.parentRef,
    row,
    open: { ref: row.transcriptRef, parentRef: row.parentRef },
    stale,
    ended,
  };
}

function liveDelegateEntity(
  stable: EvenerDelegateInfo,
  ownerRef: string,
  stale: boolean,
  ended: boolean,
  name?: string,
): DelegateEntityView {
  return {
    kind: "delegate",
    name,
    id: activityNodeID({ kind: "delegate", delegateId: stable.delegateId, childRef: stable.transcriptRef }),
    logicalId: stable.delegateId,
    ownerRef,
    stable,
    open: { ref: stable.transcriptRef, parentRef: ownerRef },
    stale,
    ended,
  };
}

function provenDelegateGeneration(generation: number | undefined): number {
  return typeof generation === "number" && Number.isSafeInteger(generation) && generation > 0 ? generation : 0;
}

function preferActivityDelegate(row: ActivityDelegate, stable: EvenerDelegateInfo): boolean {
  const rowGeneration = provenDelegateGeneration(row.runGeneration);
  const stableGeneration = provenDelegateGeneration(stable.runGeneration);
  if (rowGeneration !== stableGeneration) return rowGeneration > stableGeneration;
  // A settled run cannot reopen without advancing its proven generation.
  if (rowGeneration > 0 && (row.terminal === true) !== (stable.terminal === true)) return row.terminal === true;
  // Unknown generations carry no settlement ordering; comparable revisions
  // retain their existing authority without assigning time-based identity.
  return row.projectionRevision !== undefined && row.projectionRevision > stable.projectionRevision;
}

export function buildEntityView(sources: EntityViewSources): Map<string, EntityView> {
  const entities = new Map<string, EntityView>();
  const activityEntities = sources.tree ? indexActivityEntities(sources.tree) : undefined;

  for (const [id, row] of activityEntities ?? []) {
    entities.set(
      id,
      row.kind === "job"
        ? jobEntity(row, sources.stale, sources.ended)
        : treeDelegateEntity(row, sources.stale, sources.ended),
    );
  }

  // A thread's list holds its whole subtree. A nested row belongs to its
  // parent delegate's session. A row whose parent the list does not hold
  // belongs to the viewed session: on a subagent's thread, that parent is the
  // subagent's own delegate.
  const childRefs = new Map((sources.delegates ?? []).map((stable) => [stable.delegateId, stable.transcriptRef]));
  for (const stable of sources.delegates ?? []) {
    const ownerRef = (stable.parentDelegateId && childRefs.get(stable.parentDelegateId)) || sources.sessionRef;
    const id = activityNodeID({ kind: "delegate", delegateId: stable.delegateId, childRef: stable.transcriptRef });
    const existing = entities.get(id);
    const sameIdentity =
      existing?.kind === "delegate" &&
      existing.ownerRef === ownerRef &&
      existing.logicalId === stable.delegateId &&
      existing.open.ref === stable.transcriptRef;
    if (sameIdentity && existing.row !== undefined && preferActivityDelegate(existing.row.delegate, stable)) continue;
    // Status overlays retain an immutable name only for the same owned delegate.
    const name = sameIdentity ? existing.name : undefined;
    entities.set(id, liveDelegateEntity(stable, ownerRef, sources.stale, sources.ended, name));
  }

  for (const [id, watch] of foldWatchSummaries(watchItems(sources.turns))) {
    const qualified = watchRowID(sources.sessionRef, id);
    entities.set(qualified, {
      kind: "watch",
      id: qualified,
      logicalId: id,
      ownerRef: sources.sessionRef,
      watch,
      lastKnown: true,
      stale: true,
      ended: false,
    });
  }

  return entities;
}

export function entityOpenTarget(view: EntityView): OpenTarget | undefined {
  return view.kind === "watch" ? undefined : view.open;
}

/** Resolve a transcript's logical ID within its owning session. Ambiguous
 * loaded evidence cannot select an arbitrary resource or cross a host boundary.
 * A delegate also resolves from any session above it: a transcript can name a
 * delegate its own subagents started, and delegate IDs are unique in a tree. */
export function findEntityView(
  entities: ReadonlyMap<string, EntityView>,
  kind: EntityView["kind"],
  logicalId: string,
  ownerRef: string,
): EntityView | undefined {
  let found: EntityView | undefined;
  for (const entity of entities.values()) {
    if (entity.kind !== kind || entity.logicalId !== logicalId) continue;
    if (entity.ownerRef !== ownerRef && !(kind === "delegate" && delegateOwnedBelow(entities, entity, ownerRef)))
      continue;
    if (found) return undefined;
    found = entity;
  }
  return found;
}

// delegateOwnedBelow reports whether entity's owner session descends from
// ownerRef through the loaded delegates' child sessions.
function delegateOwnedBelow(entities: ReadonlyMap<string, EntityView>, entity: EntityView, ownerRef: string): boolean {
  const seen = new Set<string>();
  let current = entity.ownerRef;
  while (current !== ownerRef) {
    if (seen.has(current)) return false;
    seen.add(current);
    let parent: EntityView | undefined;
    for (const candidate of entities.values()) {
      if (candidate.kind === "delegate" && candidate.open.ref === current) {
        parent = candidate;
        break;
      }
    }
    if (!parent) return false;
    current = parent.ownerRef;
  }
  return true;
}

export function watchItems(turns: TurnModel[]): ItemModel[] {
  const items: ItemModel[] = [];
  for (const turn of turns) {
    for (const item of turn.items) {
      if (item.toolName === "job_watch") items.push(item);
    }
  }
  return items;
}

export function watchFoldKey(turns: TurnModel[]): string {
  // Canonical order, not caller order: the fold orders these snapshots by
  // transcript position, so the key must too — otherwise a page arriving out of
  // order rebuilds the view for identical content.
  return JSON.stringify(
    [...watchItems(turns)]
      .sort(compareTranscriptPosition)
      .map((item) => [
        item.position?.entry ?? null,
        item.position?.item ?? null,
        item.argumentsJSON ?? null,
        item.output ?? null,
        item.error ?? null,
        item.status ?? null,
        item.raw === undefined ? null : JSON.stringify(item.raw),
      ]),
  );
}
