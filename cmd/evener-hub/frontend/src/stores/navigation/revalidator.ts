import type { NavigationInvalidationTarget } from "../../protocol/types.gen";
import {
  isProjectResource,
  projectKeyFromResource,
  resourceKey,
  type NavigationRequest,
  type NavigationResponse,
  type ResourceKey,
  type ResourceListener,
  type ResourceState,
} from "./types";

interface Entry<T> {
  state: ResourceState<T>;
  request?: NavigationRequest<T>;
  controller?: AbortController;
  promise?: Promise<ResourceState<T>>;
  rerun: boolean;
  forceToken: number;
}

const generationOf = (response: NavigationResponse): string | undefined =>
  response.generationID ?? response.generation_id;

/** Coordinates conditional navigation reads without allowing old reads to win. */
export class NavigationRevalidator {
  private generationIDValue: string;
  private readonly entries = new Map<ResourceKey, Entry<unknown>>();
  private readonly listeners = new Set<ResourceListener>();
  private sequence = 0;

  constructor(generationID = "") {
    this.generationIDValue = generationID;
  }

  get generationID(): string {
    return this.generationIDValue;
  }

  subscribe(listener: ResourceListener): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  get<T = unknown>(key: ResourceKey): ResourceState<T> | undefined {
    return this.entries.get(key)?.state as ResourceState<T> | undefined;
  }

  states(): ReadonlyMap<ResourceKey, ResourceState> {
    return new Map([...this.entries].map(([key, entry]) => [key, entry.state]));
  }

  invalidate(target: NavigationInvalidationTarget): void {
    if (target.kind === "all_loaded_projects") {
      for (const [key, entry] of this.entries) {
        if (isProjectResource(key)) this.raise(key, entry, undefined, true);
      }
      return;
    }
    const key = resourceKey(target);
    if (!key) return; // Unknown/incomplete advertised targets fail closed.
    const entry = this.entries.get(key);
    if (!entry) return; // Invalidations do not create an unrequested resource.
    this.raise(key, entry, target.revision, false);
  }

  force(keys: Iterable<ResourceKey>): void {
    for (const key of keys) {
      const entry = this.entry(key);
      this.raise(key, entry, undefined, true);
    }
  }

  resetGeneration(generationID: string): void {
    if (generationID === this.generationIDValue) return;
    this.generationIDValue = generationID;
    this.sequence = 0;
    for (const entry of this.entries.values()) {
      entry.controller?.abort();
      entry.controller = undefined;
      entry.promise = undefined;
      entry.request = undefined;
      entry.rerun = false;
      entry.state = {
        ...entry.state,
        generationID,
        loadedRevision: -1,
        targetRevision: 0,
        data: undefined,
        error: undefined,
        loading: false,
      };
      this.emit(entry.state.key, entry.state);
    }
  }

  load<T>(key: ResourceKey, request: NavigationRequest<T>): Promise<ResourceState<T>> {
    const entry = this.entry<T>(key);
    entry.request = request;
    if (entry.promise) return entry.promise as Promise<ResourceState<T>>;
    if (entry.state.loadedRevision >= entry.state.targetRevision && entry.state.data !== undefined) {
      return Promise.resolve(entry.state as ResourceState<T>);
    }
    return this.start(entry as Entry<T>) as Promise<ResourceState<T>>;
  }

  private entry<T>(key: ResourceKey): Entry<T> {
    let found = this.entries.get(key) as Entry<T> | undefined;
    if (!found) {
      found = {
        state: {
          key,
          loadedRevision: -1,
          targetRevision: 0,
          loading: false,
          generationID: this.generationIDValue,
        },
        rerun: false,
        forceToken: 0,
      };
      this.entries.set(key, found as Entry<unknown>);
    }
    return found;
  }

  private raise(key: ResourceKey, entry: Entry<unknown>, revision: number | undefined, force: boolean): void {
    const next = force
      ? Math.max(entry.state.targetRevision + 1, ++this.sequence)
      : (revision ?? entry.state.targetRevision);
    if (!force && next <= entry.state.targetRevision) return;
    entry.state = { ...entry.state, targetRevision: next, error: undefined };
    this.emit(key, entry.state);
    if (entry.promise) {
      entry.rerun = true;
      entry.controller?.abort();
    }
  }

  private start<T>(entry: Entry<T>): Promise<ResourceState<T>> {
    const request = entry.request as NavigationRequest<T> | undefined;
    if (!request) return Promise.resolve(entry.state);
    const controller = new AbortController();
    entry.controller = controller;
    entry.state = { ...entry.state, loading: true, error: undefined };
    this.emit(entry.state.key, entry.state);
    const requestedGeneration = this.generationIDValue;
    const requestedTarget = entry.state.targetRevision;
    const promise = request(controller.signal, entry.state.etag)
      .then((response) => this.finish(entry, response, requestedGeneration, requestedTarget))
      .catch((error) => {
        if (entry.state.generationID === requestedGeneration && !controller.signal.aborted) {
          entry.state = { ...entry.state, loading: false, error };
          this.emit(entry.state.key, entry.state);
        }
        return entry.state;
      })
      .then((state) => {
        if (entry.controller === controller) {
          entry.controller = undefined;
          entry.promise = undefined;
          if (entry.rerun) {
            entry.rerun = false;
            void this.start(entry);
          }
        }
        return state;
      });
    entry.promise = promise;
    return promise;
  }

  private finish<T>(
    entry: Entry<T>,
    response: NavigationResponse<T>,
    generation: string,
    target: number,
  ): ResourceState<T> {
    const responseGeneration = generationOf(response);
    if (
      generation !== this.generationIDValue ||
      (responseGeneration !== undefined && responseGeneration !== generation)
    ) {
      return entry.state;
    }
    const revision = response.revision ?? target;
    if (revision < entry.state.loadedRevision || target < entry.state.targetRevision) {
      entry.state = { ...entry.state, loading: false };
      this.emit(entry.state.key, entry.state);
      return entry.state;
    }
    const data = response.data ?? response.value;
    entry.state = {
      ...entry.state,
      loading: false,
      loadedRevision: response.status === 304 ? Math.max(entry.state.loadedRevision, revision) : revision,
      targetRevision: Math.max(entry.state.targetRevision, revision),
      etag: response.etag ?? entry.state.etag,
      ...(response.status === 304 || data === undefined ? {} : { data }),
      error: undefined,
      generationID: generation,
    };
    this.emit(entry.state.key, entry.state);
    return entry.state;
  }

  private emit(key: ResourceKey, state: ResourceState): void {
    for (const listener of this.listeners) listener(key, state);
  }
}

export { projectKeyFromResource };
