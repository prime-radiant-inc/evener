import type { NavigationInvalidationTarget } from "../../protocol/types.gen";
import {
  isProjectResource,
  keyID,
  resourceKey,
  type NavigationRequest,
  type NavigationResponse,
  type ResourceKey,
  type ResourceListener,
  type ResourceState,
} from "./types";

type Entry = {
  state: ResourceState;
  request?: NavigationRequest;
  controller?: AbortController;
  promise?: Promise<ResourceState>;
  rerun: boolean;
  token: number;
};
const error = (message: string) => new Error(`navigation protocol: ${message}`);

export class NavigationRevalidator {
  private generation: string;
  private readonly entries = new Map<string, Entry>();
  private readonly listeners = new Set<ResourceListener>();
  private token = 0;
  private disposed = false;

  constructor(generationID = "") {
    this.generation = generationID;
  }
  get generationID(): string {
    return this.generation;
  }
  subscribe(listener: ResourceListener): () => void {
    if (this.disposed) return () => {};
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }
  unsubscribe(listener: ResourceListener): void {
    this.listeners.delete(listener);
  }
  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    for (const e of this.entries.values()) e.controller?.abort();
    this.entries.clear();
    this.listeners.clear();
  }
  get<T = unknown>(key: ResourceKey): ResourceState<T> | undefined {
    return this.entries.get(keyID(key))?.state as ResourceState<T> | undefined;
  }
  states(): ReadonlyMap<string, ResourceState> {
    return new Map([...this.entries].map(([id, e]) => [id, e.state]));
  }

  invalidate(target: NavigationInvalidationTarget): void {
    if (this.disposed) return;
    if (target.kind === "all_loaded_projects") {
      for (const e of this.entries.values()) if (isProjectResource(e.state.key)) this.raise(e, undefined);
      return;
    }
    const mapped = resourceKey(target);
    if (!mapped) return;
    for (const e of this.entries.values()) if (matches(e.state.key, mapped)) this.raise(e, target.revision);
  }

  force(keys: Iterable<ResourceKey>): void {
    if (this.disposed) return;
    for (const key of keys) {
      const e = this.entries.get(keyID(key));
      if (e) this.raise(e, undefined, true);
    }
  }

  resetGeneration(generationID: string): void {
    if (this.disposed || generationID === this.generation) return;
    this.generation = generationID;
    for (const e of this.entries.values()) {
      e.controller?.abort();
      e.rerun = Boolean(e.promise);
      e.state = snapshot({
        ...e.state,
        generationID,
        loadedRevision: null,
        targetRevision: null,
        etag: null,
        stale: true,
        loading: Boolean(e.promise),
        error: null,
      });
      this.emit(e.state);
      if (!e.promise && e.request) void this.start(e);
    }
  }

  load<T = unknown>(key: ResourceKey, request: NavigationRequest<T>): Promise<ResourceState<T>> {
    if (this.disposed) return Promise.reject(error("revalidator disposed"));
    let e = this.entries.get(keyID(key));
    if (!e) {
      e = {
        state: snapshot({
          key,
          data: null,
          loadedRevision: null,
          targetRevision: null,
          etag: null,
          loading: false,
          stale: true,
          error: null,
          generationID: this.generation,
        }),
        rerun: false,
        token: 0,
      };
      this.entries.set(keyID(key), e);
    }
    e.request = request as NavigationRequest;
    if (e.promise) return e.promise as Promise<ResourceState<T>>;
    if (!e.state.stale && e.state.data !== null) return Promise.resolve(e.state as ResourceState<T>);
    return this.start(e) as Promise<ResourceState<T>>;
  }

  private raise(e: Entry, revision?: number, forced = false): void {
    const target = forced ? null : Math.max(e.state.targetRevision ?? -1, revision ?? -1);
    if (!forced && target === e.state.targetRevision && e.state.stale) return;
    e.token = forced ? ++this.token : e.token;
    e.state = snapshot({ ...e.state, targetRevision: target, stale: true, error: null });
    this.emit(e.state);
    if (e.promise) e.rerun = true;
    else if (e.request) void this.start(e);
  }

  private start(e: Entry): Promise<ResourceState> {
    if (!e.request || this.disposed) return Promise.resolve(e.state);
    const controller = new AbortController();
    e.controller = controller;
    const generation = this.generation;
    const target = e.state.targetRevision;
    const request = e.request;
    e.state = snapshot({ ...e.state, loading: true, error: null });
    this.emit(e.state);
    const p = request(controller.signal, e.state.etag)
      .then((response) => {
        if (generation !== this.generation) return e.state;
        try {
          validate(response, generation, e.state.etag);
        } catch (cause) {
          return this.fail(e, cause);
        }
        if (response.revision < (e.state.loadedRevision ?? -1) || response.revision < (e.state.targetRevision ?? -1)) {
          e.rerun = true;
          return this.fail(e, error("late or below-target response"));
        }
        const data = response.status === 304 ? e.state.data : response.data;
        e.state = snapshot({
          ...e.state,
          data: data ?? null,
          loadedRevision: response.revision,
          targetRevision: response.revision,
          etag: response.etag,
          stale: false,
          loading: false,
          error: null,
        });
        this.emit(e.state);
        return e.state;
      })
      .catch((cause) => (controller.signal.aborted ? e.state : this.fail(e, cause)))
      .then((state) => {
        if (e.promise === p) {
          e.promise = undefined;
          e.controller = undefined;
          if (e.rerun) {
            e.rerun = false;
            void this.start(e);
          }
        }
        return state;
      });
    e.promise = p;
    return p;
  }
  private fail(e: Entry, cause: unknown): ResourceState {
    e.state = snapshot({ ...e.state, loading: false, stale: true, error: cause });
    this.emit(e.state);
    return e.state;
  }
  private emit(state: ResourceState): void {
    if (!this.disposed) for (const listener of this.listeners) listener(state);
  }
}

function snapshot(state: ResourceState): ResourceState {
  return Object.freeze({ ...state });
}
function matches(a: ResourceKey, b: ResourceKey): boolean {
  if (a.kind !== b.kind) return false;
  if (a.kind === "project" && b.kind === "project") return a.projectKey === b.projectKey;
  if (a.kind === "project_page" && b.kind === "project") return a.projectKey === b.projectKey;
  return (
    JSON.stringify(a) === JSON.stringify(b) ||
    (a.kind === b.kind &&
      (a.kind === "section" || a.kind === "catalog" || a.kind === "pin_catalog" || a.kind === "pin_section"))
  );
}
function validate(response: NavigationResponse, generation: string, oldETag: string | null): void {
  if (!response || (response.status !== 200 && response.status !== 304)) throw error("status must be 200 or 304");
  if (response.generationID !== generation) throw error("generation mismatch");
  if (!Number.isSafeInteger(response.revision) || response.revision < 0) throw error("revision mismatch");
  if (!response.etag) throw error("missing ETag");
  if (response.status === 304 && (response.data !== undefined || oldETag === null || response.etag !== oldETag))
    throw error("contradictory 304");
  if (response.status === 200 && response.data === undefined) throw error("200 requires body");
}
