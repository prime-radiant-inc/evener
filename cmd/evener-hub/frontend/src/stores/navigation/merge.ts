import type { NavigationDelta, NavigationReadBase, NavigationSnapshot } from "../../protocol/types.gen";
import {
  type NavigationGraph,
  type NavigationGraphContainer,
  type NavigationGraphEntity,
  type NormalizedResource,
  normalizedGraphFromSnapshot,
  validateGraphForResource,
} from "./codec";
import { cloneAndDeepFreezeJSON, equalJSON } from "./immutable";
import { NavigationBaseInvalidError } from "./types";

type Keyed = { readonly key: string };

type KeyedUpdate<T extends Keyed> = {
  readonly incoming: Iterable<T>;
  readonly removed: readonly string[];
  readonly complete: boolean;
  readonly equal: (existing: T, incoming: T) => boolean;
  readonly prepare: (incoming: T) => T;
};

function reconcileKeyedMap<T extends Keyed>(
  previous: ReadonlyMap<string, T>,
  update: KeyedUpdate<T>,
): ReadonlyMap<string, T> {
  let staged: Map<string, T> | undefined;
  const incomingKeys = update.complete ? new Set<string>() : undefined;
  const mutable = (): Map<string, T> => {
    staged ??= new Map(previous);
    return staged;
  };

  for (const incoming of update.incoming) {
    incomingKeys?.add(incoming.key);
    const existing = previous.get(incoming.key);
    const chosen = existing && update.equal(existing, incoming) ? existing : update.prepare(incoming);
    if (chosen !== (staged ?? previous).get(incoming.key)) mutable().set(incoming.key, chosen);
  }

  if (incomingKeys) {
    for (const key of previous.keys()) {
      if (!incomingKeys.has(key)) mutable().delete(key);
    }
  } else {
    for (const key of update.removed) {
      if ((staged ?? previous).has(key)) mutable().delete(key);
    }
  }
  return staged ?? previous;
}

const sameEntity = (existing: NavigationGraphEntity, incoming: NavigationGraphEntity): boolean =>
  existing.kind === incoming.kind && equalJSON(existing.value, incoming.value);

const sameContainer = (existing: NavigationGraphContainer, incoming: NavigationGraphContainer): boolean =>
  equalJSON(existing.owner, incoming.owner) && equalJSON(existing.children, incoming.children);

const freezeEntity = (value: NavigationGraphEntity): NavigationGraphEntity => cloneAndDeepFreezeJSON(value);
const freezeContainer = (value: NavigationGraphContainer): NavigationGraphContainer => cloneAndDeepFreezeJSON(value);

type GraphUpdate = {
  readonly metadata: unknown;
  readonly metadataSupplied: boolean;
  readonly entities: Iterable<NavigationGraphEntity>;
  readonly removedEntityKeys: readonly string[];
  readonly containers: Iterable<NavigationGraphContainer>;
  readonly removedContainerKeys: readonly string[];
  readonly complete: boolean;
  readonly prepared: boolean;
};

function reconcileGraph(previous: NavigationGraph | undefined, update: GraphUpdate): NavigationGraph {
  const previousEntities = previous?.entities ?? new Map<string, NavigationGraphEntity>();
  const previousContainers = previous?.containers ?? new Map<string, NavigationGraphContainer>();
  const prepareEntity = update.prepared ? (value: NavigationGraphEntity) => value : freezeEntity;
  const prepareContainer = update.prepared ? (value: NavigationGraphContainer) => value : freezeContainer;
  const entities = reconcileKeyedMap(previousEntities, {
    incoming: update.entities,
    removed: update.removedEntityKeys,
    complete: update.complete,
    equal: sameEntity,
    prepare: prepareEntity,
  });
  const containers = reconcileKeyedMap(previousContainers, {
    incoming: update.containers,
    removed: update.removedContainerKeys,
    complete: update.complete,
    equal: sameContainer,
    prepare: prepareContainer,
  });

  let metadata = previous?.metadata;
  if (!previous || update.metadataSupplied) {
    metadata =
      previous && equalJSON(previous.metadata, update.metadata)
        ? previous.metadata
        : update.prepared
          ? (update.metadata as Readonly<Record<string, unknown>>)
          : cloneAndDeepFreezeJSON((update.metadata as Record<string, unknown>) ?? {});
  }
  if (!metadata) metadata = Object.freeze({});
  if (
    previous &&
    metadata === previous.metadata &&
    entities === previous.entities &&
    containers === previous.containers
  )
    return previous;
  return Object.freeze({ metadata, entities, containers });
}

export function normalizeSnapshot(snapshot: NavigationSnapshot): NavigationGraph {
  return normalizedGraphFromSnapshot(snapshot);
}

export function reconcileSnapshot(
  previous: NormalizedResource | null | undefined,
  incoming: NormalizedResource,
): NormalizedResource {
  const graph = reconcileGraph(previous?.graph, {
    metadata: incoming.graph.metadata,
    metadataSupplied: true,
    entities: incoming.graph.entities.values(),
    removedEntityKeys: [],
    containers: incoming.graph.containers.values(),
    removedContainerKeys: [],
    complete: true,
    prepared: true,
  });
  validateGraphForResource(incoming.key, incoming.version, graph);
  return Object.freeze({
    ...incoming,
    graph,
    version: cloneAndDeepFreezeJSON(incoming.version),
  });
}

export function applyDelta(
  previous: NormalizedResource,
  delta: NavigationDelta,
  version: NavigationReadBase,
): NormalizedResource {
  try {
    const graph = reconcileGraph(previous.graph, {
      metadata: delta.metadata,
      metadataSupplied: delta.metadata !== undefined,
      entities: delta.upsertedEntities,
      removedEntityKeys: delta.removedEntityKeys,
      containers: delta.upsertedContainers,
      removedContainerKeys: delta.removedContainerKeys,
      complete: false,
      prepared: false,
    });
    validateGraphForResource(previous.key, version, graph);
    return Object.freeze({
      ...previous,
      graph,
      version: cloneAndDeepFreezeJSON(version),
    });
  } catch {
    throw new NavigationBaseInvalidError();
  }
}
