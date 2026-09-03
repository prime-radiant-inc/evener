import type { NavigationInvalidatedPayload, NavigationInvalidationTarget } from "../../protocol/types.gen";
import {
  isProjectResource,
  keyID,
  NavigationBaseInvalidError,
  type NavigationRequest,
  type NavigationResponse,
  type ResourceKey,
  type ResourceListener,
  type ResourceState,
  targetBase,
} from "./types";

interface Entry {
  state: ResourceState;
  request?: NavigationRequest;
  controller?: AbortController;
  promise?: Promise<ResourceState>;
  rerun: boolean;
  recoveringBase: boolean;
  epoch: number;
}
interface TargetWaiter {
  targets: NavigationInvalidationTarget[];
  generationID: string;
  resolve: () => void;
  reject: (reason?: unknown) => void;
}
export interface NavigationInvalidationWaiter {
  promise: Promise<NavigationInvalidatedPayload>;
  cancel(): void;
}
const protocolError = (message: string) => new Error(`navigation protocol: ${message}`);
const clone = <T>(value: T): T => {
  if (value === null || typeof value !== "object") return value;
  if (typeof structuredClone === "function") return structuredClone(value);
  return JSON.parse(JSON.stringify(value)) as T;
};
const deepFreeze = <T>(value: T): T => {
  if (value !== null && typeof value === "object" && !Object.isFrozen(value)) {
    for (const child of Object.values(value as Record<string, unknown>)) deepFreeze(child);
    Object.freeze(value);
  }
  return value;
};
const freezeValue = <T>(value: T): T => {
  if (value === null || Object.isFrozen(value)) return value;
  return deepFreeze(clone(value));
};
const frozen = (state: ResourceState): ResourceState =>
  Object.freeze({
    ...state,
    key: freezeValue(state.key),
    data: freezeValue(state.data),
  });

export class NavigationRevalidator {
  private generationIDValue: string;
  private epoch = 0;
  private forceSequence = 0;
  private lastSequence = 0;
  private disposed = false;
  private readonly entries = new Map<string, Entry>();
  private readonly listeners = new Set<ResourceListener>();
  private readonly targetWaiters = new Set<TargetWaiter>();
  private readonly invalidationWaiters = new Set<{
    predicate: (payload: NavigationInvalidatedPayload) => boolean;
    resolve: (payload: NavigationInvalidatedPayload) => void;
    reject: (reason?: unknown) => void;
  }>();
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
    for (const waiter of this.targetWaiters) waiter.reject(protocolError("revalidator disposed"));
    for (const waiter of this.invalidationWaiters) waiter.reject(protocolError("revalidator disposed"));
    this.targetWaiters.clear();
    this.invalidationWaiters.clear();
  }
  get<T = unknown>(key: ResourceKey): ResourceState<T> | undefined {
    return this.entries.get(keyID(key))?.state as ResourceState<T> | undefined;
  }
  states(): ReadonlyMap<string, ResourceState> {
    return new Map([...this.entries].map(([id, e]) => [id, e.state]));
  }
  // A key is loaded once a caller has registered a request callback. This
  // deliberately includes in-flight and failed entries so sequence gaps and
  // generation resets can retry them; unseen/collapsed keys never enter here.
  loadedKeys(): ResourceKey[] {
    return [...this.entries.values()].map((entry) => entry.state.key);
  }
  waitForTargets(targets: NavigationInvalidationTarget[], generationID = this.generationIDValue): Promise<void> {
    if (generationID !== this.generationIDValue) return Promise.reject(protocolError("generation mismatch"));
    if (this.targetsSatisfied(targets, generationID)) return Promise.resolve();
    return new Promise<void>((resolve, reject) => {
      this.targetWaiters.add({ targets, generationID, resolve, reject });
      this.resolveTargetWaiters();
    });
  }
  waitForInvalidation(
    predicate: (payload: NavigationInvalidatedPayload) => boolean = () => true,
  ): NavigationInvalidationWaiter {
    if (this.disposed) return { promise: Promise.reject(protocolError("revalidator disposed")), cancel: () => {} };
    let waiter: {
      predicate: (payload: NavigationInvalidatedPayload) => boolean;
      resolve: (payload: NavigationInvalidatedPayload) => void;
      reject: (reason?: unknown) => void;
    };
    const promise = new Promise<NavigationInvalidatedPayload>((resolve, reject) => {
      waiter = { predicate, resolve, reject };
      this.invalidationWaiters.add(waiter);
    });
    return {
      promise,
      cancel: () => {
        if (!waiter) return;
        this.invalidationWaiters.delete(waiter);
        waiter.reject(protocolError("invalidation waiter cancelled"));
      },
    };
  }
  notifyInvalidation(payload: NavigationInvalidatedPayload): void {
    for (const waiter of [...this.invalidationWaiters]) {
      if (!waiter.predicate(payload)) continue;
      waiter.resolve(payload);
      this.invalidationWaiters.delete(waiter);
    }
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
  forceLocations(): void {
    this.force(this.loadedKeys().filter((key) => key.kind === "location"));
  }
  resetGeneration(generationID: string): void {
    // The promise returned by an earlier load belongs to that old request.
    // Retained callbacks are started immediately for the new epoch; their
    // result is exposed through state/listeners rather than that old promise.
    if (this.disposed || generationID === this.generationIDValue) return;
    for (const waiter of this.targetWaiters) waiter.reject(protocolError("generation mismatch"));
    this.targetWaiters.clear();
    this.generationIDValue = generationID;
    this.epoch++;
    this.lastSequence = 0;
    for (const e of this.entries.values()) {
      e.controller?.abort();
      e.promise = undefined;
      e.controller = undefined;
      e.epoch = this.epoch;
      e.rerun = false;
      e.recoveringBase = false;
      e.state = frozen({
        ...e.state,
        generationID,
        loadedRevision: null,
        targetRevision: null,
        etag: null,
        version: undefined,
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
    const e = this.entry(key);
    e.request = request as NavigationRequest;
    if (e.promise) return e.promise as Promise<ResourceState<T>>;
    if (!e.state.stale && e.state.data !== null) return Promise.resolve(e.state as ResourceState<T>);
    return this.start(e) as Promise<ResourceState<T>>;
  }
  track<T = unknown>(key: ResourceKey, request: NavigationRequest<T>): void {
    if (this.disposed) return;
    this.entry(key).request = request as NavigationRequest;
  }
  private entry(key: ResourceKey): Entry {
    const id = keyID(key);
    const existing = this.entries.get(id);
    if (existing) return existing;
    const entry = {
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
      recoveringBase: false,
      epoch: this.epoch,
    };
    this.entries.set(id, entry);
    return entry;
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
    const requestedForce = e.state.forceToken;
    const controller = new AbortController();
    e.controller = controller;
    e.state = frozen({ ...e.state, loading: true, error: null });
    this.emit(e.state);
    let run!: Promise<ResourceState>;
    run = e
      .request(controller.signal, e.state.etag, e.state.version)
      .then((response) => {
        if (this.disposed || epoch !== this.epoch || generation !== this.generationIDValue || e.epoch !== epoch)
          return e.state;
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
        e.rerun = false;
        e.recoveringBase = false;
        e.state = frozen({
          ...e.state,
          data: response.status === 304 ? e.state.data : (response.data ?? null),
          loadedRevision: response.revision,
          targetRevision: response.revision,
          etag: response.etag,
          version: { generationId: response.generationID, revision: response.revision, etag: response.etag },
          normalized: response.status === 304 ? e.state.normalized : response.normalized,
          stale: false,
          loading: false,
          error: null,
        });
        this.emit(e.state);
        return e.state;
      })
      .catch((cause) =>
        controller.signal.aborted
          ? e.state
          : cause instanceof NavigationBaseInvalidError
            ? this.recoverInvalidBase(e, cause)
            : this.fail(e, cause),
      )
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
  private recoverInvalidBase(e: Entry, cause: NavigationBaseInvalidError): ResourceState {
    if (e.recoveringBase) return this.fail(e, cause);
    e.recoveringBase = true;
    e.rerun = true;
    e.state = frozen({
      ...e.state,
      version: undefined,
      etag: null,
      forceToken: ++this.forceSequence,
      loading: false,
      stale: true,
      error: null,
    });
    this.emit(e.state);
    return e.state;
  }
  private emit(state: ResourceState): void {
    if (!this.disposed)
      for (const listener of this.listeners) {
        try {
          listener(state);
        } catch {
          /* observers cannot affect request lifecycle */
        }
      }
    this.resolveTargetWaiters();
  }
  private targetsSatisfied(targets: NavigationInvalidationTarget[], generationID: string): boolean {
    if (generationID !== this.generationIDValue) return false;
    return targets.every((target) => {
      return [...this.entries.values()].every((entry) => {
        if (!matchesTarget(entry.state.key, target)) return true;
        const revision = target.revision;
        return (
          !entry.state.loading &&
          !entry.state.stale &&
          !entry.state.error &&
          entry.state.loadedRevision !== null &&
          (revision === undefined || entry.state.loadedRevision >= revision)
        );
      });
    });
  }
  private resolveTargetWaiters(): void {
    for (const waiter of [...this.targetWaiters]) {
      if (waiter.generationID !== this.generationIDValue) {
        waiter.reject(protocolError("generation mismatch"));
        this.targetWaiters.delete(waiter);
      } else if (this.targetFailed(waiter.targets)) {
        waiter.reject(protocolError("target convergence failed"));
        this.targetWaiters.delete(waiter);
      } else if (this.targetsSatisfied(waiter.targets, waiter.generationID)) {
        waiter.resolve();
        this.targetWaiters.delete(waiter);
      }
    }
  }
  private targetFailed(targets: NavigationInvalidationTarget[]): boolean {
    return targets.some((target) =>
      [...this.entries.values()].some(
        (entry) => matchesTarget(entry.state.key, target) && entry.state.error !== null && !entry.rerun,
      ),
    );
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
function matchesTarget(key: ResourceKey, target: NavigationInvalidationTarget): boolean {
  if (target.kind === "all_loaded_projects") return isProjectResource(key);
  const base = targetBase(target);
  return base ? matchesBase(key, base) : false;
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
  }
  const gap = revalidator.acceptSequence(payload.sequence);
  if (gap) revalidator.force(revalidator.loadedKeys());
  for (const target of payload.targets) revalidator.invalidate(target);
  if (!gap) revalidator.forceLocations();
}
