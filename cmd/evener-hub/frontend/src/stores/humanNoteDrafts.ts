import type { ThreadModel } from "@evener/appwire-client";
import { sessionActionError } from "@evener/appwire-client";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { MutationOutboxRecord, MutationRecord, MutationRecoveryRecord } from "./mutationOutbox";
import { registerPanelStoreEvictor, schedulePanelStoreEviction } from "./panelStoreEviction";
import { readMutationPersistence, retryBlockedMutation, subscribeMutationPersistence, threadsStore } from "./threads";

export interface HumanNoteDraft {
  text: string;
  generation: number;
  dirty: boolean;
  saved: boolean;
  error: string | null;
  submitted?: {
    generation: number;
    id: string;
    text: string;
    state: "submitting" | "blockedUnknown" | "canceled" | "rejected";
  };
  timer?: ReturnType<typeof setTimeout>;
  release?: () => void;
  // flush runs the pending delayed save immediately; teardown uses it so a
  // page that goes away inside the debounce window is not silently dropped.
  flush?: () => void;
  focusOwners: ReadonlySet<symbol>;
}

const drafts = createStore<{ records: Map<string, HumanNoteDraft> }>(() => ({ records: new Map() }));
// The retained draft's status when the blocked row it was waiting on is no
// longer in this tab's own records at all: another connection settled or
// removed it without a canonical acknowledgement arriving here, so nothing is
// left to settle this save and the note really is unsaved. It replaces the
// "blocked pending session recovery" status, which claimed the write was only
// waiting - a wait that will now never end. The draft keeps its text, stays
// dirty, and keeps the submitted identity an acknowledgement may still arrive
// under; absence is not an acknowledgement and is never treated as one.
const SETTLED_ELSEWHERE_STATUS =
  "Note is still unsaved: the blocked save it was waiting on is no longer pending in this tab";
// The state and status text a retained row maps to on the draft's submitted
// record: a row in the recovery store is a refusal (its reason when the daemon
// gave one), a blocked row is still waiting on session recovery, any other
// state is taken as-is. refreshPersistence applies this on a persistence
// notification; the blur-save retry path applies the same mapping to the row
// its post-retry read observes.
function submittedStatusFor(
  record: MutationOutboxRecord,
  refused: MutationRecoveryRecord | undefined,
): { state: "submitting" | "blockedUnknown" | "canceled" | "rejected"; error: string | null } {
  const state = refused ? "rejected" : record.state;
  const error = refused
    ? (refused.recoveryReason ?? "Note could not be saved")
    : state === "blockedUnknown"
      ? "Note save is blocked pending session recovery"
      : state === "canceled"
        ? "Note save was canceled by Stop"
        : null;
  return { state, error };
}
let unsubscribePersistence: (() => void) | undefined;
let persistenceRead = 0;
function observePersistence(): void {
  unsubscribePersistence ??= subscribeMutationPersistence(refreshPersistence);
}
function refreshPersistence(): void {
  const read = ++persistenceRead;
  void readMutationPersistence()
    .then(({ outbox, optimistic, recovery }) => {
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
        const { state, error } = submittedStatusFor(record, refused);
        put(record.targetRef, { ...draft, submitted: { ...draft.submitted, state }, error, saved: false });
      }
      // The optimistic store holds accepted-but-unreflected rows: another
      // connection's "pending" receipt moved this tab's blocked note out of
      // the outbox (settleReceipt's accepted copy, the retention notes carry
      // since e7a2098d5), so a draft whose submitted identity now lives only
      // there is waiting on its canonical reflection - not on session
      // recovery. Reading only the outbox and recovery stores left the draft
      // showing the stale blocked status for a save that had already been
      // accepted. Map the accepted row to the draft's pending state and clear
      // that stale error, the same mapping the post-retry lookup's accepted
      // branch applies: the draft keeps its submitted identity so the
      // canonical note state that arrives next still acknowledges it.
      for (const accepted of optimistic) {
        if (accepted.method !== "notes/human/set") continue;
        // A row the outbox or recovery store still holds reports itself
        // through the loop above; the optimistic copy is authoritative only
        // for a row neither other store holds anymore.
        if (
          outbox.some((record) => record.clientMutationId === accepted.clientMutationId) ||
          recovery.some((record) => record.clientMutationId === accepted.clientMutationId)
        )
          continue;
        const draft = get(accepted.targetRef);
        if (draft?.submitted?.id !== accepted.clientMutationId || draft.generation !== draft.submitted.generation)
          continue;
        // Idempotent: the accepted copy persists until reconcileIdentities
        // settles it, while refreshes fire on every notification and mount.
        if (draft.submitted.state === "submitting" && draft.error === null) continue;
        put(accepted.targetRef, {
          ...draft,
          submitted: { ...draft.submitted, state: "submitting" },
          error: null,
          saved: false,
        });
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
// Closing a focused panel without a blur keeps its draft and invents no save
// (docs/web-ui/shared-notes.md: closing without leaving the field does not
// save; the draft is retained for the same browser session). Only an actual
// blur schedules the debounce.
export function blurHumanNote(ref: string, owner: symbol): void {
  unmountHumanNote(ref, owner);
  const draft = get(ref);
  if (!draft?.dirty || draft.focusOwners.size || draft.timer !== undefined) return;
  if (draft.submitted?.generation === draft.generation && draft.submitted.state === "submitting") return;
  const state = threadsStore.getState();
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
    if (!current) {
      release();
      return;
    }
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
      // The live draft may no longer be in the state this save was armed for:
      // another connection can accept the blocked save while the debounce (or
      // this very await) was open, and the persistence refresh maps that
      // accepted row to the draft's pending state without touching the armed
      // timer. A same-generation draft that is already submitting has its
      // write durable and waiting on canonical reflection - the blur-time
      // guard above refuses to arm for exactly this state, and the fire-time
      // save must recheck it live the same way. Skipping the recheck sent the
      // save past the blocked-retry branch to a duplicate notes/human/set
      // whose onEnqueue replaced the submitted identity the original save's
      // acknowledgement arrives under.
      if (active.submitted?.generation === active.generation && active.submitted.state === "submitting") return;
      if (current.submitted?.generation === current.generation && current.submitted.state === "blockedUnknown") {
        const blockedId = current.submitted.id;
        await retryBlockedMutation(blockedId, "backgroundNote");
        // The retry's boolean is not the settlement authority: `true` only
        // means the row no longer reads blocked to THIS tab's final lookup,
        // which also covers another connection settling or restoring the row
        // mid-retry on the shared outbox - a change this tab is never notified
        // about. `false` is every way the retry can decline: this tab fenced
        // by a Stop, a closed write gate, a stalled reconciliation, or the row
        // simply no longer being blocked here. Either way the question left is
        // what this ref's own records say now, asked of them directly
        // (readMutationPersistence), never a queue-wide freshen, and never a
        // resave.
        const { outbox, optimistic, recovery } = await readMutationPersistence(ref);
        const refused = recovery.find((record) => record.clientMutationId === blockedId);
        const record = refused ?? outbox.find((record) => record.clientMutationId === blockedId);
        // The optimistic store holds accepted-but-unreflected rows: another
        // dispatcher's receipt (projectionState "pending") moved the row there
        // while its canonical note state is still on the way.
        const accepted = optimistic.find((record) => record.clientMutationId === blockedId);
        const latest = get(ref);
        // The same generation AND the same submitted identity: a newer edit, or
        // an acknowledgement that landed while the read ran, owns the status now.
        const submitted = latest?.submitted;
        if (!latest || latest.generation !== current.generation || !submitted || submitted.id !== blockedId) return;
        if (!record) {
          if (accepted) {
            // Accepted but not yet reflected: the note is pending, not
            // settled-elsewhere. Keeping the submitted identity lets the
            // acknowledgement that arrives with the canonical note state
            // settle this same save.
            put(ref, { ...latest, submitted: { ...submitted, state: "submitting" }, error: null, saved: false });
            return;
          }
          // Absence from every store is not a canonical acknowledgement, so
          // the dirty note stays exactly where it is and only its status
          // changes.
          put(ref, { ...latest, error: SETTLED_ELSEWHERE_STATUS });
          return;
        }
        // Still blocked here: nothing settled, and the draft's blocked status
        // is already the truth.
        if (!refused && record.state === "blockedUnknown") return;
        // Present in another state: the draft takes the row's actual state
        // rather than the retry's boolean - the same mapping refreshPersistence
        // applies on a persistence notification.
        const { state, error } = submittedStatusFor(record, refused);
        put(ref, {
          ...latest,
          submitted: { ...submitted, state },
          error,
          saved: false,
        });
        return;
      }
      // The daemon's ExpectedInstanceID check is the authority on session
      // staleness; this path asserts the client's freshest knowledge of the
      // instance. Capturing at blur time would refuse a hydration or rotation
      // the client has already observed (threads.ts's client pre-check reads
      // this same store), while a genuine race still reaches the daemon fence.
      const live = threadsStore.getState();
      const model = live.threads.get(ref) ?? live.watchedThreads.get(ref);
      const expectedInstanceId = model?.instanceId ?? model?.threadId ?? "";
      await live.setHumanNote(ref, current.text, expectedInstanceId, (record) => {
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
// ignored by many browsers and adds nothing here. The save itself is async and
// has to await the session's thread handle before it can enqueue durably, so a
// hard teardown is best-effort: the flush starts the save without waiting for
// the debounce, and a page that survives (a bfcache navigation) gets it; a
// page torn down first can still end before the enqueue commits. That residual
// window is inherent to an async store, not something the flush can close.
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
  const next =
    draft.generation !== draft.submitted.generation
      ? { ...draft, submitted: undefined }
      : { ...draft, text: note, dirty: false, saved: true, error: null, submitted: undefined };
  put(record.targetRef, next);
  // Clearing the pending state can make the record reclaimable while its
  // pane is already gone - the workspace-change sweep that preserved it
  // will not come again, so the transition itself must schedule the sweep,
  // or the acknowledged draft lingers in the map forever.
  schedulePanelStoreEviction();
}
export function useHumanNoteDraft(ref: string): HumanNoteDraft | undefined {
  return useStore(drafts, (state) => state.records.get(ref));
}
export function resetHumanNoteDrafts(): void {
  unsubscribePersistence?.();
  unsubscribePersistence = undefined;
  persistenceRead += 1;
  for (const draft of drafts.getState().records.values()) {
    if (draft.timer === undefined) continue;
    clearTimeout(draft.timer);
    draft.release?.();
  }
  drafts.setState({ records: new Map() });
}
// Shared notes stay readable while a session is under the hub's recovery fence
// (resumeRequired), so the SharedNotes capability and status alone admit edits
// the hub refuses until an explicit resume. The fence closes the write gate
// here; an edit made before the fence arrived stays in the draft store and an
// already-queued mutation stays in the durable outbox, both for retry after
// resume. Read rendering (canReadSharedNotes) is deliberately unaffected.
export function canWriteHumanNote(model: ThreadModel | undefined): boolean {
  return (
    !!model?.capabilities.sharedNotes &&
    model.resumeRequired !== true &&
    !["ended", "closed", "notLoaded", "restartRequired"].includes(model.status.type)
  );
}

// The always-mounted panel sync creates one record per notes-capable session
// pane, so without an evictor the store grows without bound over a
// long-lived hub. A record with nothing pending is recreatable from the
// model's note on the next sync, making it reclaimable once no pane holds its
// ref. Dirty and submitted records are the retry-after-resume contract (an
// edit made before a fence or close stays queued for retry) and deliberately
// outlive the pane.
function hasNothingPending(draft: HumanNoteDraft): boolean {
  return (
    !draft.dirty &&
    draft.submitted === undefined &&
    draft.focusOwners.size === 0 &&
    draft.timer === undefined &&
    draft.flush === undefined
  );
}

registerPanelStoreEvictor({
  refs: () => drafts.getState().records.keys(),
  evict: (ref: string) => {
    const draft = get(ref);
    if (!draft || !hasNothingPending(draft)) return;
    const next = new Map(drafts.getState().records);
    next.delete(ref);
    drafts.setState({ records: next });
  },
});
