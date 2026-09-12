import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { sessionActionError } from "../protocol/errors";
import type { ThreadModel } from "../protocol/model";
import type { MutationOutboxRecord, MutationRecord } from "./mutationOutbox";
import { readMutationPersistence, retryBlockedMutation, subscribeMutationPersistence, threadsStore } from "./threads";

export interface HumanNoteDraft {
  text: string;
  generation: number;
  dirty: boolean;
  saved: boolean;
  error: string | null;
  submitted?: { generation: number; id: string; text: string; state: "submitting" | "blockedUnknown" | "rejected" };
  timer?: ReturnType<typeof setTimeout>;
  release?: () => void;
  // flush runs the pending delayed save immediately; teardown uses it so a
  // page that goes away inside the debounce window is not silently dropped.
  flush?: () => void;
  focusOwners: ReadonlySet<symbol>;
}

const drafts = createStore<{ records: Map<string, HumanNoteDraft> }>(() => ({ records: new Map() }));
let unsubscribePersistence: (() => void) | undefined;
let persistenceRead = 0;
function observePersistence(): void {
  unsubscribePersistence ??= subscribeMutationPersistence(refreshPersistence);
}
function refreshPersistence(): void {
  const read = ++persistenceRead;
  void readMutationPersistence()
    .then(({ outbox, recovery }) => {
      if (read !== persistenceRead) return;
      const latest = new Map<string, MutationOutboxRecord>();
      for (const record of [...outbox, ...recovery]) {
        if (record.method !== "notes/human/set") continue;
        if (record.intentSequence > (latest.get(record.targetRef)?.intentSequence ?? -1))
          latest.set(record.targetRef, record);
      }
      for (const record of latest.values()) {
        let draft = get(record.targetRef);
        if (
          draft &&
          !draft.dirty &&
          !draft.submitted &&
          draft.generation === 0 &&
          typeof record.payload.note === "string"
        ) {
          draft = {
            ...draft,
            generation: 1,
            text: record.payload.note,
            dirty: true,
            submitted: { generation: 1, id: record.clientMutationId, text: record.payload.note, state: record.state },
          };
          put(record.targetRef, draft);
        }
        if (draft?.submitted?.id !== record.clientMutationId || draft.generation !== draft.submitted.generation)
          continue;
        const refused = recovery.find((item) => item.clientMutationId === record.clientMutationId);
        const state = refused ? "rejected" : record.state;
        const error = refused
          ? (refused.recoveryReason ?? "Note could not be saved")
          : state === "blockedUnknown"
            ? "Note save is blocked pending session recovery"
            : null;
        put(record.targetRef, { ...draft, submitted: { ...draft.submitted, state }, error, saved: false });
      }
    })
    .catch(() => {
      /* Existing outbox retains the request when storage is unavailable. */
    });
}
function put(ref: string, draft: HumanNoteDraft): void {
  drafts.setState(({ records }) => ({ records: new Map(records).set(ref, draft) }));
}
function get(ref: string): HumanNoteDraft | undefined {
  return drafts.getState().records.get(ref);
}
function cancel(ref: string): void {
  const draft = get(ref);
  if (draft?.timer === undefined) return;
  clearTimeout(draft.timer);
  put(ref, { ...draft, timer: undefined, release: undefined, flush: undefined });
  draft.release?.();
}
export function syncHumanNote(ref: string, note: string): void {
  observePersistence();
  const draft = get(ref);
  if (!draft) {
    put(ref, { text: note, generation: 0, dirty: false, saved: false, error: null, focusOwners: new Set() });
    refreshPersistence();
  } else if (!draft.dirty && !draft.submitted && draft.text !== note) {
    put(ref, { ...draft, text: note, saved: false });
  }
}
export function editHumanNote(ref: string, text: string): void {
  const draft = get(ref);
  if (!draft || draft.text === text) return;
  put(ref, { ...draft, text, generation: draft.generation + 1, dirty: true, saved: false, error: null });
}
export function focusHumanNote(ref: string, owner: symbol): void {
  cancel(ref);
  const draft = get(ref);
  if (draft) put(ref, { ...draft, focusOwners: new Set(draft.focusOwners).add(owner) });
}
export function unmountHumanNote(ref: string, owner: symbol): void {
  const draft = get(ref);
  if (!draft) return;
  const focusOwners = new Set(draft.focusOwners);
  focusOwners.delete(owner);
  put(ref, { ...draft, focusOwners });
}
export function blurHumanNote(ref: string, owner: symbol): void {
  unmountHumanNote(ref, owner);
  const draft = get(ref);
  if (!draft?.dirty || draft.focusOwners.size || draft.timer !== undefined) return;
  if (draft.submitted?.generation === draft.generation && draft.submitted.state === "submitting") return;
  const state = threadsStore.getState();
  const model = state.threads.get(ref) ?? state.watchedThreads.get(ref);
  const expectedInstanceId = model?.instanceId ?? model?.threadId ?? "";
  const retained = state.ensureThread(ref).then(
    () => null,
    (error: unknown) => ({ error }),
  );
  const release = () => state.releaseThread(ref);
  // The debounce coalesces one typing session's blurs into a single write; it
  // must never mean "no write at all", so the same save is reachable from
  // teardown (flushPendingHumanNoteSaves).
  const save = async () => {
    const current = get(ref);
    if (!current) return;
    // Whoever reaches the save first retires the debounce timer: a teardown
    // flush must not leave the live handle to fire again inside the window and
    // enqueue the same note a second time.
    if (current.timer !== undefined) clearTimeout(current.timer);
    put(ref, { ...current, timer: undefined, release: undefined, flush: undefined });
    try {
      const failure = await retained;
      if (failure) throw failure.error;
      const active = get(ref);
      if (!active?.dirty || active.focusOwners.size || active.generation !== current.generation) return;
      if (current.submitted?.generation === current.generation && current.submitted.state === "blockedUnknown") {
        await retryBlockedMutation(current.submitted.id);
        return;
      }
      await threadsStore.getState().setHumanNote(ref, current.text, expectedInstanceId, (record) => {
        const latest = get(ref);
        if (latest)
          put(ref, {
            ...latest,
            submitted: {
              generation: current.generation,
              id: record.clientMutationId,
              text: current.text,
              state: "submitting",
            },
            error: null,
          });
      });
    } catch (error) {
      const latest = get(ref);
      if (latest && latest.generation === current.generation)
        put(ref, { ...latest, error: sessionActionError("Couldn't save note", error) });
    } finally {
      release();
    }
  };
  const timer = setTimeout(() => void save(), 10_000);
  installTeardownFlush();
  put(ref, { ...draft, timer, release, flush: save });
}

let teardownFlushInstalled = false;

// A pending blur save must be FLUSHED, not dropped — the same rule DockHost
// documents for its layout debounce. Closing or reloading the page inside the
// 10s window would otherwise discard the user's last edit. pagehide is the
// reliable signal (it also fires for bfcache navigations); beforeunload is
// ignored by many browsers and adds nothing here. The save itself is async, so
// a real teardown makes it best-effort: what must not be dropped is the
// durable enqueue it starts.
function installTeardownFlush(): void {
  if (teardownFlushInstalled || typeof window === "undefined") return;
  teardownFlushInstalled = true;
  window.addEventListener("pagehide", flushPendingHumanNoteSaves);
}

function flushPendingHumanNoteSaves(): void {
  for (const draft of drafts.getState().records.values()) draft.flush?.();
}
export function acknowledgeHumanNote(record: MutationRecord, note: string): void {
  const draft = get(record.targetRef);
  if (!draft || draft.submitted?.id !== record.clientMutationId) return;
  if (draft.generation !== draft.submitted.generation) {
    put(record.targetRef, { ...draft, submitted: undefined });
    return;
  }
  put(record.targetRef, { ...draft, text: note, dirty: false, saved: true, error: null, submitted: undefined });
}
export function useHumanNoteDraft(ref: string): HumanNoteDraft | undefined {
  return useStore(drafts, (state) => state.records.get(ref));
}
export function resetHumanNoteDrafts(): void {
  unsubscribePersistence?.();
  unsubscribePersistence = undefined;
  persistenceRead += 1;
  for (const draft of drafts.getState().records.values()) if (draft.timer !== undefined) clearTimeout(draft.timer);
  drafts.setState({ records: new Map() });
}
export function canWriteHumanNote(model: ThreadModel | undefined): boolean {
  return (
    !!model?.capabilities.sharedNotes &&
    !["ended", "closed", "notLoaded", "restartRequired"].includes(model.status.type)
  );
}
