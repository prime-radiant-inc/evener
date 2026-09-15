import type { ActivityTree } from "./activityData";
import { type ActivityDelegateRow, type ActivityJobRow, indexActivityEntities } from "./activityRows";
import type { ItemModel, TurnModel } from "./model";
import type { EvenerDelegateInfo } from "./types.gen";
import { foldWatchSummaries, type WatchSummary } from "./watchRows";

export interface OpenTarget {
  ref: string;
  parentRef: string;
}

interface EntityViewState {
  id: string;
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
    id: row.job.jobId,
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
    id: row.delegate.delegateId,
    row,
    open: { ref: row.transcriptRef, parentRef: row.parentRef },
    stale,
    ended,
  };
}

function liveDelegateEntity(
  stable: EvenerDelegateInfo,
  sessionRef: string,
  stale: boolean,
  ended: boolean,
): DelegateEntityView {
  return {
    kind: "delegate",
    id: stable.delegateId,
    stable,
    open: { ref: stable.transcriptRef, parentRef: sessionRef },
    stale,
    ended,
  };
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

  for (const stable of sources.delegates ?? []) {
    const existing = entities.get(stable.delegateId);
    if (
      existing?.kind === "delegate" &&
      existing.row !== undefined &&
      existing.row.delegate.projectionRevision !== undefined &&
      existing.row.delegate.projectionRevision > stable.projectionRevision
    ) {
      continue;
    }
    entities.set(stable.delegateId, liveDelegateEntity(stable, sources.sessionRef, sources.stale, sources.ended));
  }

  for (const [id, watch] of foldWatchSummaries(watchItems(sources.turns))) {
    entities.set(id, {
      kind: "watch",
      id,
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
  return JSON.stringify(
    watchItems(turns).map((item) => [
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
