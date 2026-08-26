import type { NavigationInvalidationTarget, NavigationInvalidatedPayload } from "../../protocol/types.gen";
import {
  isProjectResource,
  keyID,
  targetBase,
  type NavigationRequest,
  type NavigationResponse,
  type ResourceKey,
  type ResourceListener,
  type ResourceState,
} from "./types";

interface Entry {
  state: ResourceState;
  request?: NavigationRequest;
  controller?: AbortController;
  promise?: Promise<ResourceState>;
  rerun: boolean;
  epoch: number;
}
const protocolError = (message: string) => new Error(`navigation protocol: ${message}`);
const frozen = (state: ResourceState): ResourceState => Object.freeze({ ...state });

export class NavigationRevalidator {
  private generationIDValue: string;
  private epoch = 0;
  private forceSequence = 0;
  private lastSequence = 0;
  private disposed = false;
  private readonly entries = new Map<string, Entry>();
  private readonly listeners = new Set<ResourceListener>();
  constructor(generationID = "") {
    this.generationIDValue = generationID;
  }
  get generationID(): string {
    return this.generationIDValue;
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
  loadedKeys(): ResourceKey[] {
    return [...this.entries.values()].map((entry) => entry.state.key);
  }
  acceptSequence(sequence: number): boolean {
    const gap = sequence > this.lastSequence + 1;
    this.lastSequence = Math.max(this.lastSequence, sequence);
    return gap;
  }

  invalidate(target: NavigationInvalidationTarget): void {
    if (this.disposed) return;
    if (target.kind === "all_loaded_projects") {
      for (const e of this.entries.values()) if (isProjectResource(e.state.key)) this.raise(e, undefined, false);
      return;
    }
    const base = targetBase(target);
    if (!base) return;
    for (const e of this.entries.values()) if (matchesBase(e.state.key, base)) this.raise(e, target.revision, false);
  }
  force(keys: Iterable<ResourceKey>): void {
    if (this.disposed) return;
    for (const key of keys) {
      const e = this.entries.get(keyID(key));
      if (e) this.raise(e, undefined, true);
    }
  }
  resetGeneration(generationID: string): void {
    if (this.disposed || generationID === this.generationIDValue) return;
    this.generationIDValue = generationID;
    this.epoch++;
    this.lastSequence = 0;
    for (const e of this.entries.values()) {
      e.controller?.abort();
      e.promise = undefined;
      e.controller = undefined;
      e.epoch = this.epoch;
      e.rerun = false;
      e.state = frozen({
        ...e.state,
        generationID,
        loadedRevision: null,
        targetRevision: null,
        etag: null,
        stale: true,
        loading: false,
        error: null,
      });
      this.emit(e.state);
      if (e.request) void this.start(e);
    }
  }
  load<T = unknown>(key: ResourceKey, request: NavigationRequest<T>): Promise<ResourceState<T>> {
    if (this.disposed) return Promise.reject(protocolError("revalidator disposed"));
    const id = keyID(key);
    let e = this.entries.get(id);
    if (!e) {
      e = {
        state: frozen({
          key,
          data: null,
          loadedRevision: null,
          targetRevision: null,
          forceToken: 0,
          etag: null,
          loading: false,
          stale: true,
          error: null,
          generationID: this.generationIDValue,
        }),
        rerun: false,
        epoch: this.epoch,
      };
      this.entries.set(id, e);
    }
    e.request = request as NavigationRequest;
    if (e.promise) return e.promise as Promise<ResourceState<T>>;
    if (!e.state.stale && e.state.data !== null) return Promise.resolve(e.state as ResourceState<T>);
    return this.start(e) as Promise<ResourceState<T>>;
  }
  private raise(e: Entry, revision: number | undefined, forced: boolean): void {
    const nextRevision = forced ? e.state.targetRevision : Math.max(e.state.targetRevision ?? -1, revision ?? -1);
    if (!forced && nextRevision === e.state.targetRevision && e.state.stale) return;
    const forceToken = forced ? ++this.forceSequence : e.state.forceToken;
    e.state = frozen({ ...e.state, targetRevision: nextRevision, forceToken, stale: true, error: null });
    this.emit(e.state);
    if (e.promise) e.rerun = true;
    else if (e.request) void this.start(e);
  }
  private start(e: Entry): Promise<ResourceState> {
    if (!e.request || this.disposed) return Promise.resolve(e.state);
    const epoch = this.epoch;
    e.epoch = epoch;
    const generation = this.generationIDValue;
    const requestedRevision = e.state.targetRevision;
    const requestedForce = e.state.forceToken;
    const controller = new AbortController();
    e.controller = controller;
    e.state = frozen({ ...e.state, loading: true, error: null });
    this.emit(e.state);
    let run!: Promise<ResourceState>;
    run = e
      .request(controller.signal, e.state.etag)
      .then((response) => {
        if (epoch !== this.epoch || generation !== this.generationIDValue || e.epoch !== epoch) return e.state;
        try {
          validate(response, generation, e.state.etag);
        } catch (cause) {
          return this.fail(e, cause);
        }
        if (
          response.revision < (e.state.loadedRevision ?? -1) ||
          response.revision < (e.state.targetRevision ?? -1) ||
          requestedForce !== e.state.forceToken
        ) {
          e.rerun = true;
          return this.fail(e, protocolError("late, below-target, or superseded response"));
        }
        e.state = frozen({
          ...e.state,
          data: response.status === 304 ? e.state.data : (response.data ?? null),
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
        if (e.promise === run && e.epoch === epoch) {
          e.promise = undefined;
          e.controller = undefined;
          if (e.rerun) {
            e.rerun = false;
            void this.start(e);
          }
        }
        return state;
      });
    e.promise = run;
    return run;
  }
  private fail(e: Entry, cause: unknown): ResourceState {
    e.state = frozen({ ...e.state, loading: false, stale: true, error: cause });
    this.emit(e.state);
    return e.state;
  }
  private emit(state: ResourceState): void {
    if (!this.disposed) for (const listener of this.listeners) listener(state);
  }
}
function matchesBase(key: ResourceKey, base: Partial<ResourceKey>): boolean {
  if (key.kind !== base.kind) {
    if (!(base.kind === "project" && key.kind === "project_page")) return false;
  }
  if (base.kind === "section" && key.kind === "section") return key.section === base.section;
  if (base.kind === "catalog" && key.kind === "catalog") return key.catalog === base.catalog;
  if (base.kind === "pin_section" && key.kind === "pin_section") return key.sectionId === base.sectionId;
  if (base.kind === "project" && (key.kind === "project" || key.kind === "project_page"))
    return key.projectKey === base.projectKey;
  return key.kind === base.kind;
}
function validate(response: NavigationResponse, generation: string, cachedETag: string | null): void {
  if (!response || (response.status !== 200 && response.status !== 304))
    throw protocolError("status must be exact 200 or 304");
  if (response.generationID !== generation) throw protocolError("generation mismatch");
  if (!Number.isSafeInteger(response.revision) || response.revision < 0) throw protocolError("revision mismatch");
  if (typeof response.etag !== "string" || response.etag.length === 0) throw protocolError("ETag mismatch");
  if (response.status === 200 && response.data === undefined) throw protocolError("200 requires body");
  if (response.status === 304 && (response.data !== undefined || cachedETag === null || response.etag !== cachedETag))
    throw protocolError("304 cache/body contradiction");
}
export function applyNavigationInvalidation(
  revalidator: NavigationRevalidator,
  payload: NavigationInvalidatedPayload,
): void {
  if (payload.generationId !== revalidator.generationID) {
    revalidator.resetGeneration(payload.generationId);
    return;
  }
  if (revalidator.acceptSequence(payload.sequence)) revalidator.force(revalidator.loadedKeys());
  for (const target of payload.targets) revalidator.invalidate(target);
}
