// A real IndexedDB request-fault seam for the legacy draft transfer: once a
// real earlier draft write AND a real earlier transfer-marker write have
// genuinely succeeded in the same transaction, one extra real duplicate-key
// add of the captured first draft row is enqueued, whose ConstraintError
// aborts the whole transfer transaction. transferSupportedDrafts reports the
// attempt as "pending" - the durable-write fault that the legacy-draft
// migration gate must order against - without synthesizing any request,
// event or transaction.
//
// Use it to give an adapter under test a legacy-draft transfer that stays
// pending: seed a legacy draft key into the storage the adapter's repository
// reads (globalThis.localStorage for adapters that construct their own
// repository, the storage option otherwise), then pass `factory` wherever the
// real IDBFactory would go.
import type { IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { COMPOSER_DRAFTS_STORE, DRAFT_TRANSFERS_STORE } from "../localCompositionSchema";

// Dependency fault injection over the REAL injected IDBFactory, through public
// IndexedDB APIs only. Every normal production request is forwarded unchanged.
// State, the first-row entry snapshot, the observed successes and the single
// injection are tracked PER ACTUAL TRANSACTION across repeated objectStore
// retrievals. Once a real earlier draft write AND a real earlier transfer-marker
// write have genuinely succeeded IN THAT TRANSACTION, the second of those
// success callbacks enqueues one extra real duplicate-key add of the captured
// first draft row. The store's own primary key raises a genuine asynchronous
// ConstraintError, so the transaction aborts for real. No request, event,
// rejection or transaction is synthesized and no explicit abort is used. The
// injector does not depend on add vs put, a particular store wrapper, a
// particular draft/marker write order, or any later production write: a
// producer that enqueues all its writes before any success callback is handled
// the same as one that awaits each request before issuing the next.
interface TransactionWriteState {
  draftStore?: IDBObjectStore;
  firstRow?: unknown;
  captured: boolean;
  draftSucceeded: boolean;
  markerSucceeded: boolean;
  injected: boolean;
}
export function faultAfterEarlierDraftAndMarker(real: IDBFactory): {
  factory: IDBFactory;
  draftSucceeded: () => boolean;
  markerSucceeded: () => boolean;
  requestErrorName: () => string | undefined;
  events: () => string[];
} {
  let sawDraftSuccess = false;
  let sawMarkerSuccess = false;
  let injectedError: DOMException | undefined;
  const events: string[] = [];
  const databases = new WeakMap<object, IDBDatabase>();
  const transactionStates = new WeakMap<object, TransactionWriteState>();
  // A plain snapshot at write entry, so a producer that mutates its own object
  // after issuing the real request cannot change what is re-added.
  const snapshotRow = (value: unknown): unknown => JSON.parse(JSON.stringify(value)) as unknown;

  const stateFor = (transaction: object): TransactionWriteState => {
    let state = transactionStates.get(transaction);
    if (!state) {
      state = { captured: false, draftSucceeded: false, markerSucceeded: false, injected: false };
      transactionStates.set(transaction, state);
    }
    return state;
  };
  // Enqueue the real duplicate-key add only once both earlier successes have
  // genuinely happened IN THIS transaction, so no later production write is
  // required. This is a PURE extra request the code under test is not awaiting;
  // its own error listener observes the ConstraintError. The production
  // transaction-completion promise is created before the body is awaited and its
  // handler is attached only after the awaited request rejects, so this seam can
  // expose a genuine production unhandled-rejection window (recorded as a RED for
  // the next source phase, not hidden here).
  const injectOnceBothSucceeded = (state: TransactionWriteState): void => {
    if (!state.draftSucceeded || !state.markerSucceeded || state.injected) return;
    if (!state.captured || !state.draftStore) return;
    state.injected = true;
    const duplicate = state.draftStore.add(snapshotRow(state.firstRow) as never);
    duplicate.addEventListener("success", () => {
      events.push("unexpected-injected-success");
    });
    duplicate.addEventListener("error", () => {
      injectedError = duplicate.error ?? undefined;
      events.push("error");
    });
  };
  const wrapWriteStore = (transaction: object, store: IDBObjectStore, kind: "draft" | "marker"): IDBObjectStore =>
    new Proxy(store, {
      get(target, prop) {
        if (prop === "add" || prop === "put") {
          return (value: unknown, explicitKey?: IDBValidKey) => {
            const state = stateFor(transaction);
            if (kind === "draft") {
              if (!state.captured) {
                state.firstRow = snapshotRow(value);
                state.captured = true;
              }
              state.draftStore = target;
            }
            const request =
              prop === "add"
                ? target.add(value as never, explicitKey)
                : target.put(value as never, explicitKey as never);
            request.addEventListener("success", () => {
              if (kind === "draft") {
                state.draftSucceeded = true;
                sawDraftSuccess = true;
                events.push("draft");
              } else {
                state.markerSucceeded = true;
                sawMarkerSuccess = true;
                events.push("marker");
              }
              injectOnceBothSucceeded(state);
            });
            return request;
          };
        }
        const value = Reflect.get(target, prop, target) as unknown;
        return typeof value === "function" ? (value as (...args: unknown[]) => unknown).bind(target) : value;
      },
    }) as IDBObjectStore;

  const wrapTransaction = (transaction: IDBTransaction): IDBTransaction =>
    new Proxy(transaction, {
      get(target, prop) {
        if (prop === "objectStore") {
          return (name: string) => {
            const store = target.objectStore(name);
            if (name === COMPOSER_DRAFTS_STORE) return wrapWriteStore(target, store, "draft");
            if (name === DRAFT_TRANSFERS_STORE) return wrapWriteStore(target, store, "marker");
            return store;
          };
        }
        const value = Reflect.get(target, prop, target) as unknown;
        return typeof value === "function" ? (value as (...args: unknown[]) => unknown).bind(target) : value;
      },
    }) as IDBTransaction;

  const wrapDatabase = (database: IDBDatabase): IDBDatabase => {
    const existing = databases.get(database);
    if (existing) return existing;
    const proxy = new Proxy(database, {
      get(target, prop) {
        if (prop === "transaction") {
          return (storeNames: string | string[], mode?: IDBTransactionMode) =>
            wrapTransaction(target.transaction(storeNames, mode));
        }
        const value = Reflect.get(target, prop, target) as unknown;
        return typeof value === "function" ? (value as (...args: unknown[]) => unknown).bind(target) : value;
      },
    }) as IDBDatabase;
    databases.set(database, proxy);
    return proxy;
  };

  const factory = new Proxy(real, {
    get(target, prop) {
      if (prop === "open") {
        return (name: string, version?: number) => {
          const request = target.open(name, version);
          return new Proxy(request, {
            get(requestTarget, requestProp) {
              if (requestProp === "result") return wrapDatabase(requestTarget.result);
              const value = Reflect.get(requestTarget, requestProp, requestTarget) as unknown;
              return typeof value === "function"
                ? (value as (...args: unknown[]) => unknown).bind(requestTarget)
                : value;
            },
          }) as IDBOpenDBRequest;
        };
      }
      const value = Reflect.get(target, prop, target) as unknown;
      return typeof value === "function" ? (value as (...args: unknown[]) => unknown).bind(target) : value;
    },
  }) as IDBFactory;

  return {
    factory,
    draftSucceeded: () => sawDraftSuccess,
    markerSucceeded: () => sawMarkerSuccess,
    requestErrorName: () => injectedError?.name,
    events: () => [...events],
  };
}
