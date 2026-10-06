// ConversationStore — the active session's Zustand state. Wraps a
// ConversationService (which wraps an AppwireClient) and owns the conversation
// projection, draft, paging cursor, and mutation lifecycle.
//
// Generation safety (CRITICAL):
// - conversationGeneration increments on every open, close, and reset; late
//   frames from an older generation cannot overwrite a newer conversation's
//   state.
// - applyNotification checks threadId/ref against the current conversation and
//   silently drops mismatches.
// - Profile switching calls reset() before opening a new conversation.
// - The store never auto-retries a user mutation. On conflict, it restores the
//   draft (only if no new text was typed) and sets error.
//
// The store holds the package's ThreadModel (never the raw wire Thread) with
// the phone's display rows projected from it: every notification folds through
// reducer.applyNotification and the rows are re-projected from the model it
// returns, so a frame and a snapshot produce rows through the one projector.
// A re-read via thread/read after evener/thread/resync is the authoritative
// refresh path (triggered by the store-owned drain scheduler, not timers).

import type {
	AnyNotification,
	InputItem,
	ItemModel,
	MutationReceipt,
	ThreadItemPosition,
	ThreadModel,
	TranscriptDisplayConfigV1,
	TurnModel,
	WarningParams,
} from "@evener/appwire-client";
import {
	applyNotification,
	applyReadModel,
	configFingerprint,
	copyItemTextPresence,
	deriveSendQueueAvailability,
	foldWarningParams,
	isStaleCursorError,
	itemIdentityMatches,
	itemTextPresence,
	joinWarningParts,
	mergeOlderItemPage,
	notificationTargetsThread,
	sessionControls,
	WireError,
} from "@evener/appwire-client";
import type {
	MutationAttachmentRef,
	MutationPersistenceSnapshot,
	PendingTurnEntry,
} from "@evener/appwire-client/state/mutation";
import { ownPendingSend, reconcilePendingEntries } from "@evener/appwire-client/state/mutation";
import { create } from "zustand";
import type {
	ActivityMember,
	BoundText,
	MobileConversation,
	MobileTimelineItem,
} from "../../../mobile-native/src/projectedRows";
import {
	activityIdentity,
	attachmentSourceId,
	attachmentSourceIdentity,
	capItems,
	failureRowIdentity,
	MAX_ITEM_BYTES,
	ownTimelineIdentities,
	projectConversation,
	projectTimeline,
	truncateItem as sharedTruncateItem,
	timelineIdentities,
	timelineIdentity,
	truncateText,
} from "../../../mobile-native/src/projectedRows";

// Re-exported where they have always been imported from: the bounds are the row
// shape's, and the row module (mobile-native/src/projectedRows.ts, the D24-6
// re-home) owns that shape.
export {
	MAX_ITEM_BYTES,
	RETAINED_ITEM_CAP,
	TRUNCATION_MARKER,
	truncateText,
} from "../../../mobile-native/src/projectedRows";

import type { ActivityView } from "../services/activity";
import type {
	ConversationReadProjection,
	ConversationService,
	LiveConversationService,
} from "../services/conversation";
import type { ActivityIdentity, NotificationOutcome } from "./activity";
import type { ConversationMutationPendingPort, ConversationMutationSubmitter } from "./conversationMutation";

export type ConversationStatus = "idle" | "opening" | "open" | "error" | "closed";

// The wire's nil/non-nil-empty/non-empty rule (reducer.ts's own
// ITEM_OMISSION_TOLERANT_FIELDS, mirrored here for the mobile-only merges
// that fall outside the shared reducer's per-turn mergeHistory): an omitted
// field means "this entry said nothing new here", not "cleared". Read by
// both the rehydrate reconciliation below and reconcileCrossTurnReissues.
const SNAPSHOT_AUTHORITY_FIELDS = [
	"images",
	"outputImages",
	"output",
	"error",
	"raw",
	"prevalOnly",
	"exitCode",
	"argumentsJSON",
	"description",
	"toolName",
	"callId",
	"eventKind",
	"steeringKind",
	"source",
] as const;

// The known thread-scalar notifications: reducer.ts's own case for each just
// restamps one scalar field (and lastFrameAt), never `turns` — read straight
// off applyNotificationToThread's switch, minus history/updated, the
// overlay/* cases (which do touch turns), evener/thread/resync (its own
// clause) and warning (its own drop rule). The gap rule below reads this as
// a denylist: anything NOT in this set — including a forward-compat method
// this client has no case for at all — is transcript-shaped, and a frame
// that leaves `turns` unchanged by reference is a gap.
const THREAD_SCALAR_ONLY_METHODS = new Set<string>([
	"thread/queueChanged",
	"thread/status/changed",
	"thread/model/changed",
	"thread/reasoning-effort/changed",
	"evener/goal/updated",
	"evener/notes/updated",
	"evener/urls/updated",
	"thread/vision-model/changed",
	"evener/task/updated",
	"evener/thread/name/changed",
	"evener/sandbox/escalation/requested",
	"evener/sandbox/escalation/resolved",
	"evener/job/started",
	"evener/job/finished",
	"evener/delegate/updated",
	"evener/jobs/treeUpdated",
	"evener/thread/modelRetry",
]);

// Mutation lifecycle state for send/steer/queue/interrupt. The store tracks
// the kind, pending/failed status, the exact draft snapshot at submission,
// the draft revision at submission (so type-then-delete after clear is
// detected as an edit), and the conversation generation that initiated it.
// On failure, the failed state PERSISTS until a subsequent mutation or open
// clears it. The mutationId is a monotonically increasing private counter
// (F4) so out-of-order completion cannot change a newer mutation, error, or
// draft.
export interface ConversationMutationState {
	kind: "send" | "steer" | "queue" | "interrupt";
	status: "pending" | "failed";
	draftSnapshot: string | null;
	/** @internal — draft revision captured at submit; used to detect post-clear edits. */
	draftRevisionAtSubmit: number;
	generation: number;
	/** @internal — monotonic token for stale-check; not for external consumption. */
	mutationId: number;
}

/** Display-safe local acknowledgement of a completed production mutation. A
 * durable admission has no wire receipt to carry (the runtime settles at the
 * enqueue boundary, before the daemon answers), so the receipt is optional and
 * present only on the direct service path. */
export interface AcceptedConversationMutation {
	readonly kind: "send" | "steer" | "queue" | "interrupt";
	readonly receipt?: MutationReceipt;
}

// The production wiring's durable-submission port, supplied by the host (the
// native screen passes its NativeMutationRuntime). When present, every
// conversation mutation is admitted durably through it instead of calling the
// service's transport directly; the store then reports the admission, and the
// runtime owns dispatch and the recovery row a rejection produces.
export interface ConversationStoreOptions {
	readonly mutationHubId?: string;
	readonly mutationSubmitter?: ConversationMutationSubmitter;
	// The display config the projection opens at. The screen owns the config's
	// source (the hub's transcriptDisplay settings); the store holds the value
	// the projection runs at, and a later change reaches it through
	// setDisplayConfig — the store is not recreated per config (its identity is
	// the conversation binding).
	readonly displayConfig?: TranscriptDisplayConfigV1 | null;
}

export type LoadOlderResult =
	| { readonly status: "loaded"; readonly itemKeys: readonly string[] }
	| { readonly status: "failed"; readonly error: unknown }
	| { readonly status: "ignored" };

interface DrainScheduler {
	request(key: string, effect: () => Promise<void>): Promise<void>;
}

// A structural activity sink accepted by openProjected/rehydrate. Uses the
// approved LiveActivityState contract from state/activity.ts directly via an
// exact structural Pick so argument order cannot drift from the real store.
// The applyLiveNotification return value drives the shared drain scheduler.
export interface LiveActivitySink {
	setLiveView(view: ActivityView, identity: ActivityIdentity): boolean;
	applyLiveNotification(n: AnyNotification, identity: ActivityIdentity): NotificationOutcome;
	reset(): void;
}

// I1: Binding snapshot captured at request time. Every request through the
// drain scheduler captures the current bindingEpoch + ref + generation +
// service + sink object identities. The effect verifies ALL fields are still
// current BEFORE any read, suppressing stale work at the boundary — a request
// queued for conversation A can never call serviceA with refB after the store
// switched to B. This is an internal exact tuple; it is never exported.
/** @internal — binding snapshot for scheduler stale-work suppression. */
interface RequestBinding {
	readonly epoch: number;
	readonly ref: string;
	readonly generation: number;
	readonly service: LiveConversationService;
	// C1: sink is null for plain open() (no projected sink), non-null for
	// openProjected. Both paths validate exact service identity.
	readonly sink: LiveActivitySink | null;
}

// Production-owned drain scheduler with per-key completion promises. Each
// request returns a promise that resolves when that key's actual effect
// completes/skips/errors — even while unrelated rereads remain or hang. Same
// logical key coalesces (overwrites effect, merges waiter lists); distinct keys
// are preserved so heterogeneous outcomes both run. One effect at a time;
// recursively drains all pending. Catches effect errors without unhandled
// rejections and remains usable.
//
// SCHEDULER CONTRACT — effects must NOT await a scheduler request:
// Effects passed to scheduler.request(key, effect) are executed by the drain
// loop. An effect MUST NOT call scheduler.request() and await its result,
// because that would create a circular dependency: the drain loop awaits the
// effect, which awaits a new scheduler request, which cannot start until the
// current drain loop reaches idle — a deadlock. The reread effect
// (requestRehydrate) calls storeGet().rehydrate() directly (not through the
// scheduler). handleMutationError only requests a reread (never awaits one).
// This invariant is confirmed by the production call graph:
//   requestRehydrate effect -> rehydrate() -> readProjection() [no scheduler]
//   handleMutationError -> requestRehydrate() [fire-and-forget, action body]
function createDrainScheduler(): DrainScheduler {
	let scheduled = false;
	let busy = false;
	let inFlight: Promise<void> | null = null;
	// Per-key pending queue. Each entry holds the coalesced effect AND a list of
	// waiters that resolve when this key's effect completes/skips/errors.
	interface PendingEntry {
		effect: () => Promise<void>;
		waiters: Array<() => void>;
	}
	const pending: Map<string, PendingEntry> = new Map();
	// Waiters for the currently in-flight effect (not yet in pending Map).
	let currentWaiters: Array<() => void> = [];

	function runOne(effect: () => Promise<void>): Promise<void> {
		return Promise.resolve()
			.then(effect)
			.catch(() => {
				// Effect errors are the effect's responsibility. Swallow to prevent
				// unhandled rejection; remain usable.
			})
			.then(() => {
				// Resolve this key's waiters — the effect completed/skipped/errored.
				const waiters = currentWaiters;
				currentWaiters = [];
				for (const w of waiters) w();
				if (pending.size > 0) {
					// Drain the next pending effect in insertion order. One at a time.
					const entry = pending.entries().next().value;
					if (entry === undefined) {
						inFlight = null;
						busy = false;
						return undefined;
					}
					const nextKey = entry[0] as string;
					const nextEntry = entry[1] as PendingEntry;
					pending.delete(nextKey);
					currentWaiters = nextEntry.waiters;
					inFlight = runOne(nextEntry.effect);
					return inFlight;
				}
				inFlight = null;
				busy = false;
				return undefined;
			});
	}

	function request(key: string, effect: () => Promise<void>): Promise<void> {
		// Per-key completion promise. Resolves when this key's actual effect
		// completes/skips/errors — even while unrelated rereads remain or hang.
		const waiters: Array<() => void> = [];
		const completion = new Promise<void>((resolve) => {
			waiters.push(resolve);
		});

		if (busy) {
			// An effect is in flight — coalesce same key (merge waiters, overwrite
			// effect); distinct keys are preserved for trailing drain.
			const existing = pending.get(key);
			if (existing !== undefined) {
				existing.effect = effect;
				for (const w of waiters) existing.waiters.push(w);
			} else {
				pending.set(key, { effect, waiters });
			}
			return completion;
		}
		if (scheduled) {
			// A microtask is pending but hasn't started — coalesce same key only.
			const existing = pending.get(key);
			if (existing !== undefined) {
				existing.effect = effect;
				for (const w of waiters) existing.waiters.push(w);
			} else {
				pending.set(key, { effect, waiters });
			}
			return completion;
		}
		// First request in a quiescent drain — defer to a microtask so a
		// synchronous burst coalesces into one effect.
		scheduled = true;
		pending.set(key, { effect, waiters });
		inFlight = Promise.resolve().then(() => {
			scheduled = false;
			busy = true;
			const entry = pending.entries().next().value;
			if (entry === undefined) {
				inFlight = null;
				busy = false;
				return;
			}
			const keyToUse = entry[0] as string;
			const entryValue = entry[1] as PendingEntry;
			pending.delete(keyToUse);
			currentWaiters = entryValue.waiters;
			return runOne(entryValue.effect);
		});
		return completion;
	}

	return {
		request,
	};
}

// F4: No legacy createActivitySink adapter — the activity store's own
// LiveActivityState (Coordinate B) implements LiveActivitySink directly.
// Do not fabricate "applied" — use the real applyLiveNotification outcome.

export interface ConversationState {
	readonly ref: string | null;
	readonly profileId: string | null;
	readonly connectionGeneration: number;
	readonly conversationGeneration: number;

	readonly conversation: MobileConversation | null;
	// The display config the projection runs at (D24-5's content dimension,
	// routed through this seam): null means the show-everything default. The
	// rows are a projection of the model AT THIS CONFIG — which items exist at
	// all is the shared projector's decision here — so a level change
	// re-projects the conversation through the same display boundary
	// (capAndTruncate) every other rebuild passes through.
	readonly displayConfig: TranscriptDisplayConfigV1 | null;
	readonly olderCursor: string | null;
	// The 500-row cap trimmed rows from the top of the timeline, so older
	// history sits above it whether or not olderCursor is set: loadOlder pages
	// the trimmed rows back by naming the oldest row kept (thread/turns/list's
	// before), rebasing olderCursor when there is one.
	readonly trimmedAbove: boolean;
	readonly hasEarlierItems: boolean;
	readonly hasLaterItems: boolean;
	readonly loadingOlder: boolean;
	readonly status: ConversationStatus;
	readonly error: string | null;

	readonly draft: string;
	readonly pendingSend: string | null;
	readonly pendingMutation?: ConversationMutationState | null;
	// Slice 7a: the durable pending rows a host's bound seam projects. Null until
	// a seam is bound (LiveConversationState.bindPendingMutations); never a
	// per-process memory fact, so it survives a cold reopen.
	readonly pendingMutations?: readonly PendingTurnEntry[] | null;
	readonly lastAcceptedMutation?: AcceptedConversationMutation | null;

	// The non-projected compatibility surface, kept for screen test mocks; no
	// production screen calls it (they open through openProjected /
	// resumeProjected). It binds no activity sink, so it does not self-recover:
	// a refused mutation surfaces its error and issues no reread.
	open(service: ConversationService, ref: string): Promise<void>;
	loadOlder(service: ConversationService): Promise<LoadOlderResult>;
	setDraft(text: string): void;
	send(service: ConversationService, input: InputItem[]): Promise<void>;
	steer(service: ConversationService, input: InputItem[], expectedQueueRevision?: number): Promise<void>;
	queue(service: ConversationService, input: InputItem[]): Promise<void>;
	interrupt(service: ConversationService): Promise<void>;
	close(): void;
	applyNotification(n: AnyNotification): void;
	reset(): void;
	// Sets the display config the projection runs at. A value-equal config is a
	// no-op (the level is keyed by configFingerprint, so a fresh-but-equal
	// object from the provider's per-publish resolve does not re-project); a
	// changed one re-projects a live conversation at the new level, once,
	// inside the store's display boundary.
	setDisplayConfig(config: TranscriptDisplayConfigV1 | null): void;
	// Whether the reader follows the live end. The 500-row cap trims the top
	// of the timeline only while they do: trimming above a reader moves what
	// they read and drops rows they may scroll back to. Rows that grew past
	// the cap while they read above are trimmed when they return.
	setFollowingLiveEnd(following: boolean): void;
}

// Required live state interface (F3): the production store always implements
// these live-only methods. They are NOT optional-fallback to old open().
// Base ConversationState is preserved for screen test mocks that only need
// the basic open/send/steer/queue/interrupt/close surface.
// F2: setCoalescer is removed from the public interface — openProjected
// creates and binds the coalescer internally.
export interface LiveConversationState extends ConversationState {
	// Slice 7a: binds this conversation's durable pending-row seam (the host's
	// scoped read + storage subscription + client-ownership rule) and follows it
	// for the binding's lifetime, projecting the durable outbox/optimistic
	// records as `pendingMutations`. Returns the unbind that stops following and
	// clears the projection; a later storage change cannot resurrect it.
	bindPendingMutations(port: ConversationMutationPendingPort): () => void;
	// Slice 7b: binds the seam UNLESS one is already following this conversation.
	// The store retires the seam on any thread open (openProjected runs from the
	// resume effect, /clear's cleared callback and the refresh paths), so a host
	// re-establishes it after each (re)open; this makes that idempotent, so a
	// suspend/rehydrate generation bump that did not retire the seam leaves the
	// live subscription untouched. Returns null when a seam is already bound.
	bindPendingMutationsIfUnbound(port: ConversationMutationPendingPort): (() => void) | null;
	// Records a mutation this client sent straight to the hub, outside the
	// durable outbox (a queue promote or drain), as its own: the hub's pending
	// row for it then shows as this client's, as a steer in flight does. Its
	// time is when the hub confirmed it. Unlike a durable read's provenance it
	// needs no teardown fence: the store is per conversation and ids are unique
	// per submission, so a late call after close writes an id nothing reports.
	rememberSubmittedHere(clientMutationId: string): void;
	openProjected(
		service: LiveConversationService,
		activitySink: LiveActivitySink,
		ref: string,
		replacement?: ConversationReadProjection,
	): Promise<void>;
	suspendProjected(): void;
	resumeProjected(service: LiveConversationService, activitySink: LiveActivitySink, ref: string): Promise<void>;
	rehydrate(service: LiveConversationService, activitySink: LiveActivitySink): Promise<void>;
	// Task3: store-owned external error publication seam. The dispatcher passes
	// a generic sanitized message plus the exact ref and conversationGeneration
	// it captured when deciding to publish. The store writes the error ONLY when
	// the current ref and conversationGeneration exactly match the expected
	// values; any mismatch (profile/thread switch, reset/open transition) makes
	// zero set calls, leaves the full state object reference unchanged, and
	// fires zero subscriber notifications. The write goes through the store's
	// wrapped set so errorOwnerRev advances, which lets a subsequent rehydrate
	// preserve the newer external error instead of clearing it. No
	// service/display/raw IDs are accepted beyond the ref already privately held
	// by the store; the dispatcher is responsible for passing only generic
	// sanitized messages.
	publishExternalError(message: string, expectedRef: string | null, expectedGeneration: number): void;
}

// Check if an error is a WireError carrying the actionUnavailable
// evenerErrorInfo. F11: uses actual WireError identity (instanceof), not a
// structural property check, to match the canonical error discrimination
// pattern used by isHubLaunchError.
function isActionUnavailableError(err: unknown): boolean {
	return err instanceof WireError && err.evenerErrorInfo === "actionUnavailable";
}

// A mutation's precondition, re-evaluated at the mutation boundary rather than
// trusted from the render that offered the control: the session's controls
// (the SDK's sessionControls over the wire's status, the harness's
// capabilities and the queue depth). A status that flipped between the render
// and the submit refuses the mutation here, with the control's own reason.
// Runs in the store so the fake service (which gates on nothing) still
// respects what the hub would refuse.
function requireControl(
	conv: MobileConversation,
	control: "stop" | "steer" | "drain" | "queue" | "send",
	action: string,
): void {
	const controls = sessionControls(conv.status.type, conv.capabilities, conv.queue?.depth ?? 0);
	if (!controls[control]) {
		throw new Error(controls.reason[control] ?? `Action "${action}" is not available for this thread`);
	}
}

// The queue's precondition: a running turn, or this client's own send that
// the session has not reflected yet (deriveSendQueueAvailability's tier 6).
// The durable outbox accepts a send before the hub answers, so a second
// message composed in that window waits behind the first; as a turn/start it
// would be refused as a turn already running.
function requireQueue(conv: MobileConversation, pending: readonly PendingTurnEntry[] | null | undefined): void {
	const availability = deriveSendQueueAvailability({
		statusType: conv.status.type,
		capabilities: conv.capabilities,
		hasPendingSend: ownPendingSend(pending),
	});
	// Every state that refuses a queue here also refuses it in sessionControls,
	// so requireControl throws with that control's own reason.
	if (!availability.canQueue) requireControl(conv, "queue", "queue");
}

// Exported so this module's own test file and any other reader can bound a
// row exactly as the display pipeline does. The bound defaults to the plain
// byte bound; the store's own publishes pass the caching callback instead, so
// a settled row costs one encode for its life rather than one per publish.
export function truncateItem(
	item: MobileTimelineItem,
	bound: BoundText = (text) => truncateText(text, MAX_ITEM_BYTES),
): MobileTimelineItem {
	return sharedTruncateItem(item, bound);
}

/** The row the next older page ends above after a trim: the oldest row kept
 * that names its transcript position. Rows ahead of it with none (a live
 * overlay row, a notice) come back in the page, where they fold into the
 * rows already held. */
export function trimBoundary(
	state: Pick<ConversationState, "trimmedAbove" | "conversation">,
): ThreadItemPosition | undefined {
	return state.trimmedAbove ? state.conversation?.items.find((row) => row.position !== undefined)?.position : undefined;
}

/** The next older page as one key, for telling page attempts apart, or null
 * when nothing older is known: the cursor, the trim boundary, or both. */
export function olderPageKey(
	state: Pick<ConversationState, "olderCursor" | "trimmedAbove" | "conversation">,
): string | null {
	const before = trimBoundary(state);
	if (before === undefined && state.olderCursor === null) return null;
	// JSON keeps the hub's opaque cursor from running into the boundary.
	return JSON.stringify([state.olderCursor, before?.entry, before?.item]);
}

export function createConversationStore(options: ConversationStoreOptions = {}) {
	if (options.mutationSubmitter !== undefined && (options.mutationHubId === undefined || options.mutationHubId === ""))
		throw new Error("ConversationStore: mutationHubId is required with mutationSubmitter");
	const mutationHubId = options.mutationHubId ?? "";
	const mutationSubmitter = options.mutationSubmitter;

	// The durable path for one conversation mutation: the intent is admitted
	// through the host's runtime at the enqueue boundary and returns no wire
	// receipt, so the caller settles on admission rather than on a daemon answer.
	// The target ref and instance fence come from the operation binding the
	// caller already captured and rechecks after the await.
	function submitMutation(
		kind: "send" | "steer" | "queue" | "interrupt",
		opBinding: RequestBinding,
		conversation: MobileConversation,
		input: InputItem[],
		expectedQueueRevision?: number,
	): Promise<MutationReceipt | undefined> {
		return mutationSubmitter!.submit({
			kind,
			hubId: mutationHubId,
			targetRef: opBinding.ref,
			threadId: conversation.threadId,
			instanceId: conversation.instanceId ?? conversation.threadId,
			input,
			expectedQueueRevision,
		});
	}

	// One mutation's transport: admitted durably through the submitter when the
	// host wired one, otherwise the service's own method. Keeping the choice here
	// means each action declares only its kind, binding and input.
	function dispatchMutation(
		service: ConversationService,
		kind: "send" | "steer" | "queue" | "interrupt",
		opBinding: RequestBinding,
		conversation: MobileConversation,
		input: InputItem[],
		expectedQueueRevision?: number,
	): Promise<MutationReceipt | undefined> {
		if (mutationSubmitter !== undefined)
			return submitMutation(kind, opBinding, conversation, input, expectedQueueRevision);
		switch (kind) {
			case "send":
				return service.send(input);
			case "steer":
				return service.steer(input, expectedQueueRevision);
			case "queue":
				return service.queue(input);
			case "interrupt":
				return service.interrupt();
		}
	}
	let conversationGen = 0;
	let mutationIdCounter = 0;
	// Draft revision: a monotonically increasing counter incremented on every
	// setDraft call. When a mutation clears the draft on submit, it captures the
	// current revision. On failure, the snapshot is restored ONLY if the revision
	// has not changed since the clear — type-then-delete (which produces "" but
	// increments the revision) counts as an edit and prevents restore.
	let draftRevision = 0;
	// loadOlder operation token — incremented on each loadOlder call so a stale
	// loadOlder (from an older operation in the same generation) cannot overwrite
	// a newer loadOlder's loadingOlder, error, or items.
	let loadOlderToken = 0;
	// R1: Monotonic mutation-owner revision — increments on every
	// pendingMutation transition (set, clear, even ABA same value). Rehydrate
	// captures this at entry; if it changed during the await, the rehydrate
	// is stale with respect to the mutation owner and must not publish its
	// projection. Instead, one bounded trailing reread is scheduled.
	let mutationOwnerRev = 0;
	// R1: Monotonic error-owner revision — increments on every error
	// transition (set, clear, even ABA same value). Rehydrate captures this at
	// entry; if it changed during the await, the rehydrate is stale with
	// respect to the error owner and must not clear or overwrite error.
	let errorOwnerRev = 0;
	// The reread's snapshot is authoritative over every thread-level write that
	// preceded its response. AppWire orders a thread/read response at the
	// snapshot cut and delivers frames in producer order on the one socket
	// (docs/superpowers/plans/2026-07-28-appwire-retry-safe-mutations-and-atomic-
	// rejoin.md:19, 372-373), so a frame this store applied while the read was
	// in flight is already folded into the snapshot that arrives, and a frame
	// that arrives after the response lands on top of it through the normal
	// path. The service's readProjection and rehydrate now also await the native
	// host's read fence behind the response, but that fence resolves within
	// microtasks (the native mutation adapter is synchronous), so a socket frame
	// (a macrotask) still cannot interleave before the commit; there is no window
	// to buffer for. If that fence ever crossed a macrotask boundary, frames
	// arriving in the window would need buffering. The web's
	// applyHydrationResponseCut drops its buffer at the same point for the same
	// reason.
	//
	// loadOlder and rehydrate fold page/reread content into `turns` through
	// applyReadModel/mergeOlderItemPage/reconcileCrossTurnReissues, which read
	// and write model.history.turns — the versioned record every OTHER live
	// frame (history/updated, overlay/*, via applyNotification -> the
	// reducer's withDisplay) rebuilds `turns` FROM. Left unsynced, a page turn
	// or a rehydrate's retained history lives only in `turns`, and the next
	// such frame silently drops it (withDisplay derives `turns` from
	// model.history.turns, not from the caller's prior `turns`). Both callers
	// must therefore commit the SAME final turns array into both fields; this
	// is the one place that happens.
	//
	// A model whose `.history` was never established (no read has gone
	// through applyReadModel/mergeOlderItemPage's own versioned branch yet —
	// in production this is momentary, since every hydrate already stamps one
	// via hydrateThread's own snapshot branch; the mobile test suite's legacy
	// non-versioned fixtures can hold one indefinitely) mints one now, seeded
	// with these turns: applyReadModel otherwise reads an eternally-empty
	// held.turns forever and silently discards whatever this merge just
	// produced the very next time it runs. The seeded bootGeneration stays ""
	// — never a real one's spelling — so a model that only ever went through
	// this seeding, and never a real read, still reads as NOT-really-versioned
	// to the one other place that cares (reducer.ts's "warning" case: it folds
	// a warning into the active turn's items on a legacy model, and shows it
	// as an overlay notice, not yet implemented, on a real v6 one — see its
	// own comment).
	function withSyncedHistoryTurns<M extends MobileConversation>(model: M, turns: readonly TurnModel[]): M {
		const turnsArray = [...turns];
		if (model.history === undefined) {
			return {
				...model,
				history: {
					bootGeneration: "",
					epoch: 0,
					length: 0,
					appliedGeneration: 0,
					issuedGeneration: 0,
					deferredPages: [],
					turns: turnsArray,
				},
			};
		}
		return { ...model, history: { ...model.history, turns: turnsArray } };
	}

	// A turn a history/updated frame marks itemsView "full" carries its
	// complete current item list in this frame's own `items` (turnId-matched) —
	// never accumulated across frames (Go's Turn.ItemsView, appwire/types.go).
	// The shared reducer's mergeHistory deliberately never removes an item (an
	// invariant reducer.history.test.ts pins for every OTHER caller), so a full
	// turn's withdrawal of an item it omits is this store's own product
	// decision (RoboRev round 18-20), applied after the reducer's own merge —
	// to `turns` AND model.history.turns together (withSyncedHistoryTurns),
	// or the next frame's withDisplay resurrects the withdrawn row from
	// history.turns.
	function withdrawOmittedFullTurns(turns: readonly TurnModel[], n: AnyNotification): readonly TurnModel[] {
		if (n.method !== "history/updated") return turns;
		const fullTurnIds = (n.params.turns ?? []).filter((turn) => turn.itemsView === "full").map((turn) => turn.id);
		if (fullTurnIds.length === 0) return turns;
		const suppliedByTurn = new Map<string, Set<string>>();
		for (const id of fullTurnIds) suppliedByTurn.set(id, new Set());
		for (const item of n.params.items ?? []) {
			if (item.turnId === undefined) continue;
			suppliedByTurn.get(item.turnId)?.add(item.transcriptKey ?? item.id);
		}
		let changed = false;
		const result = turns.map((turn) => {
			const supplied = suppliedByTurn.get(turn.id);
			if (supplied === undefined) return turn;
			const kept = turn.items.filter((item) => supplied.has(item.transcriptKey ?? item.id));
			if (kept.length === turn.items.length) return turn;
			changed = true;
			return { ...turn, items: kept };
		});
		return changed ? result : turns;
	}

	// The package reducer over the conversation. The display rows are projected
	// from the model it returns (applyNotification below), so the rows a frame
	// produces and the rows a snapshot produces come from the one projector.
	function applyThreadNotification(conversation: MobileConversation, n: AnyNotification): MobileConversation {
		const next = applyNotification(conversation, n, Date.now());
		// Only a frame the reducer actually folded into `turns` can withdraw or
		// reconcile anything: an invalidated/discarded/ignored frame (the
		// reducer's own generation/epoch/incarnation rules, reducer.ts's
		// classifySignal) returns `turns` unchanged by reference, and a
		// full-turn descriptor riding such a frame describes a merge that never
		// happened.
		if (next.turns === conversation.turns) return next;
		// mergeHistory matches an item only within its OWN wire turn id — never
		// across turns the way a page/rehydrate merge's turnsMatch does. A live
		// history/updated frame that reissues a page-owned (or otherwise
		// retained) item under a NEW turn id therefore leaves two copies
		// standing side by side instead of one, until reconcileCrossTurnReissues
		// folds them; only history/updated can introduce this (every other
		// method is scalar-only or overlay-only, per the reducer's own
		// dispatch), so other methods skip the scan.
		const reconciled =
			n.method === "history/updated" ? reconcileCrossTurnReissues(conversation.turns, next.turns) : next.turns;
		const turns = withdrawOmittedFullTurns(reconciled, n);
		return turns === next.turns ? next : withSyncedHistoryTurns({ ...next, turns: [...turns] }, turns);
	}

	// See applyThreadNotification's own comment: an identity `previous` held
	// under one turn that this frame just reissued under ANOTHER turn stays
	// positioned at its OLD turn, beside its other neighbors there, not
	// wherever the live frame's own (often freshly-opened) turn happens to
	// sort — absorbing the reissue's fields (SNAPSHOT_AUTHORITY_FIELDS, the
	// same omission-tolerant fallback rehydrate's own post-merge stripping
	// pass reads) there, and the copy the reducer's own per-turn merge just
	// landed at the new turn drops. loadOlder and rehydrate call this same
	// function on their own merge output for the identical reason (this
	// file's own doc comments on those callers). An identity untouched by
	// this frame, or one already reconciled in place (both locations agree),
	// is left alone.
	function reconcileCrossTurnReissues(
		previous: readonly TurnModel[],
		turns: readonly TurnModel[],
	): readonly TurnModel[] {
		const heldTurnByIdentity = new Map<string, string>();
		for (const turn of previous) {
			for (const item of turn.items) {
				heldTurnByIdentity.set(item.transcriptKey ?? item.id, turn.id);
			}
		}
		if (heldTurnByIdentity.size === 0) return turns;
		const locationsByIdentity = new Map<string, string[]>();
		const itemByLocation = new Map<string, ItemModel>();
		for (const turn of turns) {
			for (const item of turn.items) {
				const identity = item.transcriptKey ?? item.id;
				const locations = locationsByIdentity.get(identity);
				if (locations) locations.push(turn.id);
				else locationsByIdentity.set(identity, [turn.id]);
				itemByLocation.set(`${turn.id}\u0000${identity}`, item);
			}
		}
		const reissues = new Map<string, { oldTurn: string; newTurn: string }>();
		for (const [identity, locations] of locationsByIdentity) {
			if (locations.length < 2) continue;
			const oldTurn = heldTurnByIdentity.get(identity);
			if (oldTurn === undefined || !locations.includes(oldTurn)) continue;
			const newTurn = locations.find((turnId) => turnId !== oldTurn);
			if (newTurn !== undefined) reissues.set(identity, { oldTurn, newTurn });
		}
		if (reissues.size === 0) return turns;
		let changed = false;
		const result = turns.map((turn) => {
			let turnChanged = false;
			let items = turn.items.filter((item) => {
				const reissue = reissues.get(item.transcriptKey ?? item.id);
				const drop = reissue !== undefined && reissue.newTurn === turn.id;
				if (drop) turnChanged = true;
				return !drop;
			});
			items = items.map((item) => {
				const identity = item.transcriptKey ?? item.id;
				const reissue = reissues.get(identity);
				if (reissue === undefined || reissue.oldTurn !== turn.id) return item;
				const fresh = itemByLocation.get(`${reissue.newTurn}\u0000${identity}`);
				if (fresh === undefined) return item;
				const merged = applyCrossTurnFallback(fresh, item);
				turnChanged = true;
				return merged;
			});
			if (!turnChanged) return turn;
			changed = true;
			return { ...turn, items };
		});
		return changed ? result : turns;
	}

	// A reissue is authoritative for every field it explicitly carries; an
	// omitted omission-tolerant field, or an omitted text field, falls back to
	// the superseded item's value instead of clearing it (the same rule
	// reducer.ts's private mergeReplacedItem applies within one turn — see
	// that function's own comment for why).
	function applyCrossTurnFallback(fresh: ItemModel, held: ItemModel): ItemModel {
		let merged = fresh;
		for (const field of SNAPSHOT_AUTHORITY_FIELDS) {
			const heldValue = (held as unknown as Record<string, unknown>)[field];
			if (heldValue === undefined || (fresh as unknown as Record<string, unknown>)[field] !== undefined) continue;
			if (merged === fresh) merged = { ...fresh };
			(merged as unknown as Record<string, unknown>)[field] = heldValue;
		}
		if (itemTextPresence(fresh) === "omitted" && itemTextPresence(held) === "provided") {
			merged = copyItemTextPresence(
				fresh,
				merged === fresh ? { ...fresh, text: held.text } : { ...merged, text: held.text },
			);
		}
		return merged;
	}
	// The phone's two display bounds, as one pass over the projected rows: the
	// newest RETAINED_ITEM_CAP rows, each text-bearing field cut to
	// MAX_ITEM_BYTES with the marker. Every path that publishes a conversation
	// runs it, so the bound is a property of what is displayed rather than of
	// the sequence of frames that produced it. The model behind the rows keeps
	// its full text (#1535 asks whether the package should bound that).
	//
	// The rows are rebuilt from the model on every frame, but the model hands
	// back the SAME string reference for text no frame touched, so each bound
	// string is cached under the source string it came from and reused until
	// that reference changes: a transcript of settled rows is not re-encoded
	// per delta. The cache is rebuilt from the rows of each publish (the
	// previous one is read through, then dropped), so it holds only what is on
	// screen and needs no invalidation of its own. The one item a delta is
	// streaming into does re-encode once per frame, because its text really is
	// new each time — #1535 is where that stops growing.
	let boundedText = new Map<string, string>();
	// The cache belongs to the conversation whose rows it bound: every string
	// in it is held by that conversation's model or its rows, so once the
	// conversation is dropped the cache is the only thing retaining them. Every
	// transition that drops the conversation releases it. A suspend does not:
	// the rows stay on screen for the resume, which republishes the same text.
	function releaseBoundedTextCache(): void {
		boundedText = new Map();
	}
	// Whether a folded model can change a row. projectConversation reads two of
	// the model's own fields: `turns` — every turn, item, status and error a row
	// is made of hangs off it — and `askPending`, the wire fact liveAskQuestions
	// (deriveAskQuestions.ts) gates its whole item scan on since #1731 (piece A)
	// round 4: a status frame that flips askPending with no item of its own can
	// turn an already-projected tool row into a question row, or take one away,
	// with turns untouched by reference. Every OTHER field a frame moves (the
	// status word itself, the name, the queue, the jobs tree, the goal, and
	// lastFrameAt, which moves on EVERY frame) changes no row.
	function changesRows(previous: MobileConversation, applied: ThreadModel): boolean {
		return applied.turns !== previous.turns || applied.askPending !== previous.askPending;
	}

	// RoboRev round 34: turn-independent warnings — a warning that arrives
	// with no active turn — are real server diagnostics the wire drops: the
	// reducer has nowhere transcript-true to fold one (its "warning" case
	// returns only the liveness restamp when no turn is active), and the
	// transcript never persists warnings, so no read can recover what the
	// frame carried either. Main's row applier displayed them as live-owned
	// rows; this store's rows are a projection of the model, so a row the
	// model cannot hold lives here, as transient display state outside it.
	// Every timeline rebuild passes through capAndTruncate, the display
	// boundary, which seats each notice at the position it arrived at
	// (round 35: re-appended at the tail, a notice sank below rows that
	// arrived later and the retained window could never evict it), and
	// the conversation transitions that clear the page history clear
	// these with it, so a notice never crosses threads.
	const transientWarnings: {
		row: MobileTimelineItem;
		anchor: string | null;
	}[] = [];
	let liveNoticeSerial = 0;
	// The row an idle warning displays as: the same attention row the
	// canonical projection builds for a model warning item projectedRows.ts's
	// warningItem — kind "failure", title its own field, message and hint
	// joined as detail), under the live serial identity main's applier
	// used, since there is no model item to share an identity with.
	function idleWarningRow(params: WarningParams): MobileTimelineItem {
		const folded = foldWarningParams(params);
		return {
			kind: "failure",
			id: `warning:${(liveNoticeSerial += 1)}`,
			title: folded.title ?? "Warning",
			detail: joinWarningParts([folded.text, folded.hint]),
		};
	}

	// The position a notice arrived at: the identity of the last MODEL row
	// on screen when the notice landed, or null when the timeline held
	// nothing model-backed (the notice predates every row the model has
	// produced since). Seated notices do not qualify (round 36: the second
	// of two back-to-back notices anchored to the first, but the seating
	// walk matches anchors against model rows only, so that notice never
	// seated and the prune deleted it) — consecutive notices stack at the
	// same arrival position in arrival order instead, and leave it
	// together. An attachment row's own identity is GENERATED from its
	// source's wire id, which a reissue (the same source under a new wire
	// id, transcript key standing) re-keys — an anchor to it matched
	// nothing after the reissue and the next rebuild pruned the notice
	// (RoboRev panel round 2) — so attachment rows anchor through their
	// STABLE source identity (attachmentSourceIdentity), which the reissue
	// keeps. The seating walk resolves that anchor on the source row and
	// seats the notice after the attachments that follow it (never between
	// the pair — see capAndTruncate), so the notice keeps the position it
	// arrived at, after the attachments row that was the nearest row then.
	// RoboRev review round 1: a notice arriving over a CLUSTER anchors to
	// the LAST member's identity, never the row's own top-level one — the
	// top-level identity belongs to the FIRST member, and the R38 split
	// resolves it there, lifting the notice above the run's later members
	// the very moment it arrives. Every member of the displayed run was on
	// screen when the notice landed, so the whole run stays above it; only
	// members that join the run LATER split below it.
	function arrivalAnchorIdentity(item: MobileTimelineItem): string {
		const source = attachmentSourceIdentity(item);
		if (source !== null) return source;
		if (item.kind === "activity" && item.members) {
			const last = item.members[item.members.length - 1];
			if (last !== undefined) return activityIdentity(last);
		}
		return timelineIdentity(item);
	}

	function arrivalAnchor(items: MobileTimelineItem[], noticeIdentities: ReadonlySet<string>): string | null {
		for (let index = items.length - 1; index >= 0; index -= 1) {
			const item = items[index];
			const identity = arrivalAnchorIdentity(item);
			if (!noticeIdentities.has(identity)) return identity;
		}
		return null;
	}

	// Whether the reader follows the live end (setFollowingLiveEnd).
	let followingLiveEnd = true;
	// Whether the hub last said it pages this thread from a before position
	// (ThreadCapabilities.pageBefore), on a read or on a status frame it
	// relayed; a frame straight from a daemon names none. A hub that doesn't say
	// so (an older hub, or a thread on another host until #3176) keeps every
	// row, since trimmed rows couldn't come back: a memory trade-off that lasts
	// until it does.
	let pagesBefore = false;
	/** Takes the hub's pageBefore when it says one as a boolean, `otherwise`
	 * when it doesn't. */
	function adoptPageBefore(said: unknown, otherwise: boolean): void {
		pagesBefore = typeof said === "boolean" ? said : otherwise;
	}
	function adoptRead(conversation: MobileConversation): void {
		adoptPageBefore(conversation.capabilities?.pageBefore, false);
	}

	// The rows the timeline keeps: the newest RETAINED_ITEM_CAP while the
	// reader follows the live end of a thread the hub pages from a before
	// position, every row otherwise.
	function retainedRows(rows: MobileTimelineItem[]): MobileTimelineItem[] {
		return followingLiveEnd && pagesBefore ? capItems(rows) : rows;
	}

	// The display boundary every publish runs: the cap (retainedRows) and the
	// text bound. trimmed says whether the cap dropped rows from the top, for the
	// caller's publish to record (trimmedState).
	function capAndTruncate(conversation: MobileConversation): {
		conversation: MobileConversation;
		trimmed: boolean;
	} {
		const previous = boundedText;
		const next = new Map<string, string>();
		const bound: BoundText = (text) => {
			const cached = next.get(text) ?? previous.get(text);
			const bounded = cached ?? truncateText(text, MAX_ITEM_BYTES);
			next.set(text, bounded);
			// A published row carries its BOUNDED text, so the next publish binds
			// that string, not the one the model handed over. Bounding a bounded
			// string is a no-op (truncateText never returns more than the limit), so
			// record it as its own answer and the row costs one encode for its life
			// rather than one more on every publish after the first.
			if (bounded !== text) next.set(bounded, bounded);
			return bounded;
		};
		const seated = seatTransientWarnings(conversation.items);
		const capped = retainedRows(seated);
		const items = capped.map((item) => truncateItem(item, bound));
		const retainedIdentities = new Set(items.map(timelineIdentity));
		for (let index = transientWarnings.length - 1; index >= 0; index -= 1) {
			if (!retainedIdentities.has(timelineIdentity(transientWarnings[index].row))) {
				transientWarnings.splice(index, 1);
			}
		}
		boundedText = next;
		return { conversation: { ...conversation, items }, trimmed: capped.length < seated.length };
	}

	// The transient notices rejoin the timeline here, each seated at
	// the position it arrived at (after its anchor's row) rather than
	// at the tail: a notice is evidence of a moment, so it stays above
	// the rows that arrived later, and the cap that slides past its
	// position evicts it with it — the notice enters the window as a
	// row like any other. A rebuild whose input rows already carry one
	// — loadOlder merges the current items — must not duplicate it (the
	// carried filter); a notice whose anchor left the model rows (a
	// withdrawal, a reread window that starts past it) has no position
	// to sit at and retires with the prune; and the prune back to what
	// the cap kept means an evicted notice stays evicted.
	// RoboRev review round 4: the seating is extracted so loadOlder can
	// read the same window the publish will show — a notice consumes a
	// cap slot, so the window the retained-turn bound trims against must
	// count it.
	// R38: the row a sub-run of a split cluster re-projects as — the same
	// shape clusterActivityRun builds: a run of one is the member's own
	// row, a longer run is that row with the run's aggregate state and the
	// members carried for the renderer that expands them.
	function activityRunRow(run: ActivityMember[]): Extract<MobileTimelineItem, { kind: "activity" }> | null {
		const first = run[0];
		if (first === undefined) return null;
		const row: Extract<MobileTimelineItem, { kind: "activity" }> = {
			kind: "activity",
			id: first.id,
			label: first.label,
			family: first.family,
			state: first.state,
			detail: first.detail,
			...(first.summaryOnly ? { summaryOnly: first.summaryOnly } : {}),
			...(first.transcriptKey ? { transcriptKey: first.transcriptKey } : {}),
			...(first.position ? { position: first.position } : {}),
			...(first.turnId ? { turnId: first.turnId } : {}),
		};
		if (run.length === 1) return row;
		return {
			...row,
			state: run.some((member) => member.state === "running") ? "running" : "completed",
			members: [...run],
		};
	}

	// R38: a cluster that absorbed the row a notice anchored to AND grew
	// past it — later same-family members arrived after the notice — splits
	// at the anchored member, so the notice seats at the position it
	// arrived at, above the members that arrived later. An anchor on the
	// LAST member needs no split (the whole-row seat already sits below
	// it), so null keeps that seat. The caller only offers a cluster with
	// no attachment run behind it: a notice anchored through an
	// attachment's source identity arrived after those attachments, and a
	// split would lift it above them.
	function splitClusterAtAnchors(
		item: Extract<MobileTimelineItem, { kind: "activity" }>,
		bucketsByIdentity: ReadonlyMap<string, MobileTimelineItem[]>,
	): MobileTimelineItem[] | null {
		const members = item.members;
		if (members === undefined) return null;
		// Only a member with members AFTER it splits the cluster: an anchor on
		// the last member seats after the whole row as before. The common
		// anchored publish answers that without allocating the walk.
		let splitsBeforeLastMember = false;
		for (let index = 0; index < members.length - 1; index += 1) {
			const member = members[index];
			if (member === undefined) continue;
			if (bucketsByIdentity.get(activityIdentity(member)) !== undefined) {
				splitsBeforeLastMember = true;
				break;
			}
		}
		if (!splitsBeforeLastMember) return null;
		const out: MobileTimelineItem[] = [];
		let run: ActivityMember[] = [];
		for (const member of members) {
			run.push(member);
			const bucket = bucketsByIdentity.get(activityIdentity(member));
			if (bucket === undefined) continue;
			const row = activityRunRow(run);
			if (row !== null) out.push(row);
			out.push(...bucket);
			run = [];
		}
		const tail = activityRunRow(run);
		if (tail !== null) out.push(tail);
		return out;
	}

	function seatTransientWarnings(items: MobileTimelineItem[]): MobileTimelineItem[] {
		const carriedIdentities = new Set(items.map(timelineIdentity));
		const noticesByAnchor = new Map<string, MobileTimelineItem[]>();
		const noticesBeforeEverything: MobileTimelineItem[] = [];
		for (const notice of transientWarnings) {
			if (carriedIdentities.has(timelineIdentity(notice.row))) continue;
			if (notice.anchor === null) {
				noticesBeforeEverything.push(notice.row);
				continue;
			}
			const bucket = noticesByAnchor.get(notice.anchor);
			if (bucket === undefined) {
				noticesByAnchor.set(notice.anchor, [notice.row]);
			} else {
				bucket.push(notice.row);
			}
		}
		const seated: MobileTimelineItem[] = [...noticesBeforeEverything];
		for (let index = 0; index < items.length; index += 1) {
			const item = items[index];
			seated.push(item);
			// RoboRev round 37: anchors resolve through the identities the row
			// OWNS, not just its own top-level one — pagination can seat an older
			// tool directly beside the one a notice anchored to, and the next
			// projection then clusters the two under the older's identity: the
			// anchored row stays visible as a member while its identity stops
			// matching, which used to retire the notice with the prune. The
			// size guard keeps the common no-notice publish at one check per
			// row.
			if (noticesByAnchor.size === 0) continue;
			const identities = ownTimelineIdentities(item);
			const bucketsByIdentity = new Map<string, MobileTimelineItem[]>();
			for (const identity of identities) {
				const bucket = noticesByAnchor.get(identity);
				if (bucket !== undefined) bucketsByIdentity.set(identity, bucket);
			}
			if (bucketsByIdentity.size === 0) continue;
			// A notice anchored through an attachment row's source identity
			// seats after the attachments that follow the anchored row, never
			// between them: capItems' cap cut drops a leading attachment
			// whose source fell off the cut and only scans a LEADING RUN of
			// attachments, so a notice seated inside that run would become
			// the first retained row at the cut and the orphans behind it
			// would survive — their lingering source identities then make
			// loadOlder's F10 admission rule refuse genuine older page copies
			// of those sources, forever. The run spans every attachment the
			// anchored row OWNS the source of — a clustered activity carries
			// the attachments of all its members, so a notice anchored to one
			// member seats after them all. This is also the position the
			// notice arrived at: the attachments row was the nearest row when
			// it landed (RoboRev panel round 2).
			let attachmentsEnd = index;
			while (attachmentsEnd + 1 < items.length) {
				const next = items[attachmentsEnd + 1];
				const source = attachmentSourceIdentity(next);
				if (source === null || !identities.has(source)) break;
				attachmentsEnd += 1;
			}
			// R38: the cluster grew past the anchored member — split it there
			// so the notice keeps its arrival position above the members that
			// arrived later. Only when no attachment run follows: that notice
			// arrived after the attachments, and the whole-row seat below them
			// is its arrival position.
			if (attachmentsEnd === index && item.kind === "activity") {
				const split = splitClusterAtAnchors(item, bucketsByIdentity);
				if (split !== null) {
					seated.pop();
					seated.push(...split);
					continue;
				}
			}
			for (let attach = index + 1; attach <= attachmentsEnd; attach += 1) {
				seated.push(items[attach]);
			}
			index = attachmentsEnd;
			for (const bucket of bucketsByIdentity.values()) {
				seated.push(...bucket);
			}
		}
		return seated;
	}

	// A level change hides rows without withdrawing them from the model, and
	// the prune in capAndTruncate retires a notice only when its own row left
	// the window. But the seating walk consumes a notice only at a DISPLAYED
	// anchor row, so a notice whose anchor the new level hides never seats
	// and the prune mistakes it for withdrawn. Re-anchor each such notice to
	// the nearest row the new level still displays at or above its arrival
	// position — the closest visible seat, not a retirement — and to null
	// (before everything) when nothing at or above it survives. The anchor
	// only moves up: on switch-back to the richer level the notice sits at
	// its re-anchored position, below rows that arrived after it, the
	// disclosed cost of keeping it visible through the level change.
	function reanchorTransientWarnings(oldItems: MobileTimelineItem[], newItems: MobileTimelineItem[]): void {
		if (transientWarnings.length === 0) return;
		const displayedIdentities = new Set<string>();
		for (const row of newItems) {
			for (const identity of ownTimelineIdentities(row)) {
				displayedIdentities.add(identity);
			}
		}
		for (const notice of transientWarnings) {
			const anchor = notice.anchor;
			if (anchor === null || displayedIdentities.has(anchor)) continue;
			// The anchor's arrival position in the rows leaving the screen.
			let arrival = -1;
			for (let index = 0; index < oldItems.length; index += 1) {
				const row = oldItems[index];
				if (row === undefined) continue;
				for (const identity of ownTimelineIdentities(row)) {
					if (identity === anchor) {
						arrival = index;
						break;
					}
				}
				if (arrival >= 0) break;
			}
			// The nearest displayed row at or above it, re-anchored through
			// one of that row's own displayed identities so the next seating
			// walk resolves the anchor as written.
			let reanchor: string | null = null;
			for (let above = arrival; above >= 0; above -= 1) {
				const candidate = oldItems[above];
				if (candidate === undefined) continue;
				for (const identity of ownTimelineIdentities(candidate)) {
					if (displayedIdentities.has(identity)) {
						reanchor = identity;
						break;
					}
				}
				if (reanchor !== null) break;
			}
			notice.anchor = reanchor;
		}
	}

	// C1+I1: Deferred trailing-reread request. When a rehydrate detects the
	// mutation owner changed during its await, it stores a deferred trailing
	// request with the EXACT binding snapshot captured at schedule time (not
	// recaptured inside the effect). The request is drained exactly once after
	// the mutation settles (pendingMutation cleared or set to "failed"). If the
	// binding changed (switch to B), the stored binding is stale and the request
	// is dropped. Additional mutation revisions create at most one later need.
	interface TrailingReread {
		binding: RequestBinding;
		mutationRev: number;
	}
	let trailingReread: TrailingReread | null = null;

	// C1+I1: Store a deferred trailing reread with the exact binding captured
	// at schedule time. Does NOT schedule through the scheduler yet — the
	// request is drained when the mutation settles. If a request is already
	// pending, it is overwritten (at most one later need per mutation revision).
	function scheduleTrailingReread(): void {
		const binding = captureBinding();
		if (binding === null) return;
		trailingReread = { binding, mutationRev: mutationOwnerRev };
		// The mutation may have already settled (e.g., the send completed before
		// the rehydrate was released). Try to drain immediately — if the mutation
		// is still pending, drainTrailingReread will defer.
		drainTrailingReread();
	}

	// C1+I1: Drain the deferred trailing reread after a mutation settles. The
	// mutation must be terminal (pendingMutation is null or "failed"). The
	// stored binding is validated — if stale (switch to B), the request is
	// dropped. Exactly one reread is scheduled through the store-owned scheduler.
	// The effect validates the exact captured binding (NOT recapturing current),
	// AND rechecks the captured/current monotonic mutation revision and true
	// terminal status INSIDE the scheduler effect immediately before any read.
	// If a newer mutation is pending after enqueue, do zero read; atomically
	// restore one binding-owned deferred request for that mutation revision and
	// let its settle hook drain exactly once. Keep separate queued/deferred
	// identity so no duplicate effects/third reread.
	function drainTrailingReread(): void {
		if (trailingReread === null) return;
		// Only drain if the mutation has settled (no pending mutation, or a
		// failed terminal mutation). A still-pending mutation keeps the request
		// deferred.
		const currentMutation = storeGet?.().pendingMutation;
		if (currentMutation !== null && currentMutation !== undefined && currentMutation.status === "pending") {
			return;
		}
		const { binding } = trailingReread;
		// Capture the mutation revision at enqueue time — the effect will compare
		// this against the current revision to detect a newer pending mutation
		// that started after enqueue.
		const enqueueMutationRev = mutationOwnerRev;
		trailingReread = null;
		// C1: Validate the EXACT captured binding — if stale (switch to B),
		// drop the request. Never recapture the current binding here.
		if (!isBindingCurrent(binding)) return;
		// Schedule one trailing reread through the store-owned scheduler. The
		// effect validates the exact captured binding again (double-check after
		// the scheduler microtask), rechecks mutation revision and terminal status,
		// and calls rehydrate with the captured service and sink — never the
		// closure's. If a newer mutation is pending, the effect re-defers instead
		// of reading.
		scheduler.request(binding.ref, async () => {
			// C1: Re-validate the exact captured binding after the microtask.
			if (!isBindingCurrent(binding)) return;
			// Residual 1: Recheck mutation revision and true terminal status INSIDE
			// the effect immediately before any read. If the mutation revision
			// advanced since enqueue AND a mutation is currently pending, a newer
			// mutation started after enqueue — do zero read. Atomically restore one
			// binding-owned deferred request for the newer mutation revision and let
			// its settle hook drain exactly once.
			if (mutationOwnerRev !== enqueueMutationRev) {
				const mut = storeGet?.().pendingMutation;
				if (mut !== null && mut !== undefined && mut.status === "pending") {
					// Newer mutation is pending — re-defer for this revision. The
					// binding stays the same (already validated). The settle hook
					// (called when M2 settles) will drainTrailingReread exactly once.
					trailingReread = { binding, mutationRev: mutationOwnerRev };
					return; // zero read
				}
			}
			// captureBinding only returns non-null when boundSink is non-null, so
			// binding.sink is always non-null here.
			if (binding.sink === null) return;
			await storeGet?.().rehydrate(binding.service, binding.sink);
		});
	}

	// The store owns ONE drain scheduler for its entire lifetime. Lifecycle,
	// activity rehydrate, structural notification gaps, and mutation capability
	// recovery all request through it rather than owning timers/coalescers.
	const scheduler = createDrainScheduler();
	let activitySink: LiveActivitySink | null = null;
	// The service+sink bound by the last openProjected/rehydrate, used by
	// requestRehydrate so applyNotification can request through the scheduler
	// without holding its own service reference.
	let boundService: LiveConversationService | null = null;
	let boundSink: LiveActivitySink | null = null;
	let suspendedService: LiveConversationService | null = null;
	let acceptedRehydrate: { generation: number; sink: LiveActivitySink } | null = null;
	// I1: Binding epoch — incremented on every openProjected/open/close/reset so
	// a request queued for an older binding (serviceA+refA) can never run after
	// the store switched to a newer binding (serviceB+refB). Every request
	// captures epoch+ref+generation+service+sink; the effect verifies all still
	// current before any read.
	let bindingEpoch = 0;
	// Late-bound store getter — assigned inside create() so requestRehydrate
	// (called from applyNotification) can access get().rehydrate.
	let storeGet: (() => LiveConversationState) | null = null;

	// I1: Capture the current binding snapshot at request time. The effect
	// verifies ALL fields (epoch+ref+generation+service+sink object identities)
	// are still current BEFORE any read — stale work is suppressed at the
	// boundary. Object identity comparison means a rebind with different service
	// or sink objects (even if same ref) produces a different binding and the
	// old effect is suppressed.
	function captureBinding(): RequestBinding | null {
		if (boundService === null || boundSink === null) return null;
		const state = storeGet?.();
		if (state === undefined || state.ref === null || state.status !== "open") return null;
		return {
			epoch: bindingEpoch,
			ref: state.ref,
			generation: state.conversationGeneration,
			service: boundService,
			sink: boundSink,
		};
	}

	// C1: Capture a service-specific operation binding for loadOlder and
	// mutations. The supplied service MUST equal boundService (wrong service
	// after B bound => zero request/state). sink is boundSink (null for plain
	// open, non-null for openProjected). epoch/ref/gen are captured from
	// current state. Returns null if no service is bound or ref is null.
	function captureOperationBinding(service: ConversationService): RequestBinding | null {
		if (boundService === null) return null;
		// C1: The supplied service must be the exact bound service object.
		// A wrong service (serviceA called after B is bound) is rejected at
		// the boundary before any request.
		if ((service as LiveConversationService) !== boundService) return null;
		const state = storeGet?.();
		if (state === undefined || state.ref === null || state.status !== "open") return null;
		return {
			epoch: bindingEpoch,
			ref: state.ref,
			generation: state.conversationGeneration,
			service: boundService,
			sink: boundSink,
		};
	}

	// Verify a captured binding is still current. This applies to scheduled
	// rereads, paging, and mutations. If the epoch, ref,
	// generation, or service/sink object identity changed, suppress the stale
	// request before it can publish into the new binding.
	function isBindingCurrent(binding: RequestBinding): boolean {
		const state = storeGet?.();
		if (state === undefined) return false;
		return (
			binding.epoch === bindingEpoch &&
			binding.ref === state.ref &&
			binding.generation === state.conversationGeneration &&
			boundService === binding.service &&
			boundSink === binding.sink
		);
	}

	// Request one authoritative reread through the store-owned drain scheduler.
	// I1: captures the binding at request time; the effect verifies it is still
	// current before calling rehydrate with the expected service+sink.
	function requestRehydrate(ref: string): void {
		const binding = captureBinding();
		const service = boundService;
		const sink = boundSink;
		if (binding === null || service === null || sink === null) return;
		scheduler.request(ref, async () => {
			// I1: Suppress stale work BEFORE any read — if the binding changed,
			// do not call rehydrate (serviceA must never be called with refB).
			if (!isBindingCurrent(binding)) return;
			await storeGet?.().rehydrate(service, sink);
		});
	}

	// Returns the current draft revision — passed to handleMutationError so it
	// can detect post-clear edits (type-then-delete) without exposing the
	// revision in public state.
	function getDraftRevision(): number {
		return draftRevision;
	}
	// I1: Returns the current error-owner revision — passed to
	// handleMutationError so it can compare against the captured
	// entryErrorRev without exposing the revision in public state.
	function getErrorOwnerRev(): number {
		return errorOwnerRev;
	}
	// F9: Rehydrate operation token — incremented on each rehydrate call so
	// a stale rehydrate (from an older operation) cannot overwrite a newer
	// rehydrate's state within the same generation.
	let rehydrateToken = 0;

	// Review round 3 (Medium): a level-carrying publish (the frame publish,
	// setDisplayConfig) widens the window to level-independent rows — see
	// retentionWindowItems — so the display level never decides retention.
	//
	// The keep-window for a level-carrying publish. The display level decides
	// what RENDERS, never what payload leaves the model: a window taken from
	// the level's own rows would shed every turn the level hides — rows that
	// are hidden, not withdrawn — and the switch back to a richer level could
	// not rebuild what the re-projection reads, because the payloads would be
	// gone from the model. The retention truth is the show-everything cap, the
	// pre-slice rule: only the cap window decides what leaves. The union with
	// the publish's own rows keeps what the level's deeper cap reach retains —
	// a coarse level renders fewer rows per turn, so the same row budget
	// reaches farther down the timeline. At the null config the level IS
	// show-everything, so the publish's own rows are the window.
	function retentionWindowItems(
		model: ThreadModel,
		levelItems: MobileTimelineItem[],
		config: TranscriptDisplayConfigV1 | null,
	): MobileTimelineItem[] {
		if (config === null) return levelItems;
		return levelItems.concat(retainedRows(projectConversation(model).items));
	}

	// #1919 follow-up: bound retained page-turn data. The keep-window is the
	// retained display set itself — the final capped rows at the publish site
	// (loadOlder's pageMerged, rehydrate's rehydrateSeated). A turn whose items
	// intersect it keeps full payloads: those are exactly the turns a fresh
	// reread's window can merge against, so trimming them would change
	// mergeHistory's version-supersession behavior. A turn outside the window
	// can no longer display anything or supply anything the window needs, so
	// only its identity + usage metadata survive — its items trim to `[]`, and
	// a later page/rehydrate that re-serves the turn's content merges it back
	// in on mergeHistory's own "identity not found, splice it in" path (Task
	// 17 (v6 read path): no page-ownership bookkeeping is needed to make that
	// safe, since a real v6 turn id never gets reassigned the way the pre-v6
	// merge's fragments could).
	function boundRetainedTurns(
		turns: TurnModel[],
		retainedItems: MobileTimelineItem[],
		activeTurnId?: string,
	): TurnModel[] {
		// RoboRev round 29: the row side carries bare ids too, not just each
		// row's key-first identity. A KEYLESS backing item matches a keyed row
		// by bare id under the package's own rule (itemIdentityMatches falls to
		// the id when one side carries no key), so a keyed row that contributed
		// only its transcript key left that item outside the window and the
		// bound shed the payload behind a row still on screen. Clustered
		// members and attachment sources follow the same rule, and a bare-id
		// hit against a row the item conflicts with on transcript key only
		// over-keeps — the same safe direction the item-side check below takes.
		const retainedIdentities = new Set<string>();
		for (const row of retainedItems) {
			for (const identity of timelineIdentities(row)) retainedIdentities.add(identity);
			retainedIdentities.add(row.id);
			if (row.kind === "activity" && row.members) {
				for (const member of row.members) retainedIdentities.add(member.id);
			}
			const source = attachmentSourceId(row);
			if (source !== null) retainedIdentities.add(source);
		}
		let trimmed = false;
		const bounded = turns.map((turn) => {
			if (turn.items.length === 0) return turn;
			// Review round 26: the active turn's payloads are the live working
			// set, not retained history. The dual-write row appliers lag the
			// model half — a steering append has no row applier yet — so the
			// bound would trim live items before any display row exists to back
			// them, and every caller runs it, not just publishModel: an
			// in-flight page or rehydrate can land mid-stream. Skipping before
			// the bookkeeping also keeps the live turn out of the compact sets.
			// The exemption ends when the turn settles: a completion clears
			// activeTurnId, and the next bound pass compacts it like any
			// settled turn.
			if (activeTurnId !== undefined && turn.id === activeTurnId) return turn;
			// Item identity is the package's own rule (itemIdentityMatches:
			// transcriptKey when both sides carry one, else id) approximated from
			// above: a retained row keeps the turn that could supply it alive
			// whether it matches by transcript key or by bare id — the same rule
			// mergeTurnHistory matches fragments by, clustered members included.
			// Both sides read from above now that the row side carries bare ids:
			// an item can hit a bare id whose row it conflicts with on transcript
			// key, but a false hit only over-keeps — the safe direction for merge
			// parity (round 29).
			const inWindow = turn.items.some(
				(item) => retainedIdentities.has(item.transcriptKey ?? item.id) || retainedIdentities.has(item.id),
			);
			if (inWindow) return turn;
			trimmed = true;
			return { ...turn, items: [] };
		});
		return trimmed ? bounded : turns;
	}

	// Slice 7a: the durable pending-row seam. While a host binds one, the store
	// follows this conversation target's durable outbox/optimistic records and
	// projects them as pending rows through the shared reconciliation (#2140's
	// shape) — so an in-flight mutation survives a cold reopen (its record is the
	// runtime's, not the store's memory), a rejected or reflected one drops out
	// (no zombie pendings, no double rows), and a settled one clears exactly when
	// its record leaves storage. The reconciliation always reads the live model:
	// every durable read re-runs it, and so does every conversation write.
	let pendingPort: ConversationMutationPendingPort | null = null;
	let pendingUnsubscribe: (() => void) | null = null;
	let pendingGeneration = 0;
	// Advanced only by forgetPendingProvenance (close/reset). A read's provenance
	// write is fenced on THIS, not on pendingGeneration: a read in flight across a
	// same-target rebind or a thread open still observed a durable record of this
	// client's own, and ids are unique per submission so it cannot contaminate
	// another target. Only a teardown fences it out.
	let pendingProvenanceGeneration = 0;
	let pendingSnapshot: {
		outbox: MutationPersistenceSnapshot<MutationAttachmentRef>["outbox"];
		optimistic: MutationPersistenceSnapshot<MutationAttachmentRef>["optimistic"];
	} | null = null;
	// Every durable record this client submitted, id -> createdAt, carried past
	// the record's own settle so the reconciliation's provenance rule can still
	// answer after the durable read reports the id out of storage.
	// Growth is bounded by the store's own lifetime: a store is per conversation,
	// client mutation ids are unique per submission, and close/reset forget the
	// map. The package store this mirrors deliberately never prunes it, because
	// pruning an id the daemon still reports would flip an own in-flight send to
	// `fromThisClient: false` and misroute tier-6 send/queue availability.
	const pendingSubmittedHere = new Map<string, number>();

	function reconcilePendingMutations(
		model: MobileConversation | null | undefined = storeGet?.().conversation,
	): readonly PendingTurnEntry[] {
		const port = pendingPort;
		const snapshot = pendingSnapshot;
		if (port === null || snapshot === null || !model) return [];
		return reconcilePendingEntries(
			port.targetRef,
			[...snapshot.outbox, ...snapshot.optimistic],
			model,
			pendingSubmittedHere,
			(record) => port.isOwnMutationRecord(record),
		);
	}

	// Whether two reconciliations describe the same rows. The reconciliation
	// always allocates a fresh array, so publishing it on every conversation
	// write would re-render every subscriber on each streaming delta even when
	// nothing about the rows changed; comparing the fields keeps the reference
	// stable until a row actually appears, changes state, or drops out.
	function samePendingRows(
		left: readonly PendingTurnEntry[] | null | undefined,
		right: readonly PendingTurnEntry[],
	): boolean {
		if (left === undefined || left === null || left.length !== right.length) {
			return false;
		}
		for (let index = 0; index < right.length; index += 1) {
			const a = left[index];
			const b = right[index];
			if (
				a.id !== b.id ||
				a.method !== b.method ||
				a.state !== b.state ||
				a.source !== b.source ||
				a.text !== b.text ||
				a.imageCount !== b.imageCount ||
				a.createdAt !== b.createdAt ||
				a.fromThisClient !== b.fromThisClient ||
				a.skillNames.length !== b.skillNames.length ||
				a.skillNames.some((name, skillIndex) => name !== b.skillNames[skillIndex])
			) {
				return false;
			}
		}
		return true;
	}

	// Stops following the bound seam and forgets its snapshot. Never sets state:
	// the callers that clear the published projection do so through their own
	// set (close/reset null it, the returned unbind publishes the clear).
	// Provenance is deliberately NOT forgotten here: the id -> createdAt map is
	// monotonic knowledge the package store treats as never pruned, so a
	// same-target rebind keeps it and a still-authoritative entry stays honest.
	function detachPendingRows(): void {
		++pendingGeneration;
		pendingUnsubscribe?.();
		pendingUnsubscribe = null;
		pendingPort = null;
		pendingSnapshot = null;
	}

	// Forgets the carried provenance. Only a true teardown (close/reset) calls
	// this: a rebind keeps it (same target), and a thread open keeps it too - the
	// map is keyed by client mutation id, unique per submission, and it is the
	// only carrier once a record settles out of storage while the daemon still
	// reports the id.
	function forgetPendingProvenance(): void {
		++pendingProvenanceGeneration;
		pendingSubmittedHere.clear();
	}

	return create<LiveConversationState>((rawSet, get) => {
		// R1: Wrap set so any write to pendingMutation or error increments the
		// corresponding monotonic revision counter — even ABA (same value). This
		// is the single chokepoint for ownership transitions; all set() calls
		// inside the store go through this wrapper.
		const set = (partial: Partial<ConversationState>) => {
			if ("pendingMutation" in partial) mutationOwnerRev += 1;
			if ("error" in partial) errorOwnerRev += 1;
			// A conversation write is the one model change the reconciliation has to
			// see: re-project the durable rows against the model being committed, so a
			// mutation the model now reflects drops out with no storage round-trip.
			// The reconcile runs BEFORE the write and rides it in ONE rawSet, so no
			// subscriber ever observes the intermediate state (model reflects the
			// mutation, pendingMutations still holds its stale row). A partial that
			// publishes its own pendingMutations (open/close/reset/unbind) owns that
			// field and is written as-is.
			if (
				"conversation" in partial &&
				!("pendingMutations" in partial) &&
				pendingPort !== null &&
				pendingSnapshot !== null
			) {
				const next = reconcilePendingMutations(partial.conversation);
				if (!samePendingRows(get().pendingMutations, next)) {
					rawSet({ ...partial, pendingMutations: next });
					return;
				}
			}
			rawSet(partial);
		};
		storeGet = get;
		// Re-projects the durable rows against the current model and publishes
		// them only when they changed, so subscribers keep a stable reference.
		const republishPendingRows = () => {
			const next = reconcilePendingMutations();
			if (!samePendingRows(get().pendingMutations, next)) set({ pendingMutations: next });
		};
		// What a publish records when its cap trimmed rows from the top. An older
		// page in flight asked from above rows the trim just dropped: merged, it
		// would leave those rows a hole nothing pages back, so it is dropped and
		// the next page names the oldest row kept.
		const trimmedState = (trimmed: boolean): Partial<ConversationState> => {
			if (!trimmed) return {};
			if (!get().loadingOlder) return { trimmedAbove: true };
			loadOlderToken += 1;
			return { trimmedAbove: true, loadingOlder: false };
		};
		return {
			ref: null,
			profileId: null,
			connectionGeneration: 0,
			conversationGeneration: 0,

			conversation: null,
			displayConfig: options.displayConfig ?? null,
			olderCursor: null,
			trimmedAbove: false,
			hasEarlierItems: false,
			hasLaterItems: false,
			loadingOlder: false,
			status: "idle",
			error: null,

			draft: "",
			pendingSend: null,
			pendingMutation: null,
			pendingMutations: null,
			lastAcceptedMutation: null,

			setDisplayConfig(config) {
				const current = get();
				// The level is keyed by the config's VALUE: the provider resolves a
				// fresh object per publish, and an equal config re-derives nothing.
				if (
					(current.displayConfig ?? null) === (config ?? null) ||
					(current.displayConfig !== null &&
						config !== null &&
						configFingerprint(current.displayConfig) === configFingerprint(config))
				) {
					return;
				}
				// A live conversation re-projects at the new level through the same
				// display boundary every other rebuild passes through — once, inside
				// capAndTruncate (seating, cap, truncation) — so the level change is
				// on screen with no frame and no screen-level re-projection.
				const conversation = current.conversation;
				if (conversation === null) {
					set({ displayConfig: config });
					return;
				}
				const projected = projectConversation(conversation, undefined, config ?? undefined);
				// The rows the level change hides are hidden, not withdrawn:
				// seat each notice whose anchor they include BEFORE the display
				// boundary runs, or the seating walk drops it and the prune
				// retires it as if the model had withdrawn the anchor.
				reanchorTransientWarnings(conversation.items, projected.items);
				const { conversation: bounded, trimmed } = capAndTruncate(projected);
				// The retained-turn bound every other publish runs, against the
				// level-independent window (retentionWindowItems): the level
				// change hides rows without shedding their payloads. The active
				// turn stays exempt inside the helper.
				set({
					displayConfig: config,
					conversation: {
						...bounded,
						turns: boundRetainedTurns(
							bounded.turns,
							retentionWindowItems(conversation, bounded.items, config),
							bounded.activeTurnId,
						),
					},
					...trimmedState(trimmed),
				});
			},

			setFollowingLiveEnd(following) {
				if (followingLiveEnd === following) return;
				followingLiveEnd = following;
				const current = get();
				const conversation = current.conversation;
				// Rows that grew past the cap while the reader was above are
				// trimmed as they return, with the payloads behind them.
				if (!following || conversation === null) return;
				const { conversation: bounded, trimmed } = capAndTruncate(conversation);
				if (!trimmed) return;
				set({
					conversation: {
						...bounded,
						turns: boundRetainedTurns(
							bounded.turns,
							retentionWindowItems(conversation, bounded.items, current.displayConfig),
							bounded.activeTurnId,
						),
					},
					...trimmedState(trimmed),
				});
			},

			async open(service, ref) {
				suspendedService = null;
				// Any thread open retires the bound seam: the port is scoped by the
				// durable target key (hub + ref) and the store cannot see the hub, so a
				// same-ref open through a different hub must not inherit the prior
				// target's rows. The host rebinds for the conversation it opens. The
				// carried provenance is NOT forgotten: it is keyed by client mutation id
				// (unique per submission), it is the only carrier once a record settles
				// out of storage, and the package store it mirrors never prunes it.
				detachPendingRows();
				// Increment conversation generation so late frames from a previous
				// conversation are rejected.
				const gen = ++conversationGen;
				// I1: plain open CANNOT retain projected bindings — clear them and
				// increment the binding epoch so any queued projected rehydrate/cap
				// refresh is suppressed at the boundary.
				bindingEpoch += 1;
				boundService = null;
				boundSink = null;
				// Fix round 1 I2: invalidate page ownership on conversation transition.
				loadOlderToken += 1;
				// C1+I1: clear any stale deferred trailing-reread request.
				trailingReread = null;
				transientWarnings.length = 0;
				releaseBoundedTextCache();
				set({
					status: "opening",
					ref,
					error: null,
					conversation: null,
					olderCursor: null,
					trimmedAbove: false,
					hasEarlierItems: false,
					hasLaterItems: false,
					loadingOlder: false,
					draft: "",
					pendingSend: null,
					pendingMutation: null,
					pendingMutations: null,
					lastAcceptedMutation: null,
					conversationGeneration: gen,
				});
				try {
					const conv = await service.open(ref);
					// Reject if a newer conversation generation was opened during the await.
					if (gen !== conversationGen) return;
					adoptRead(conv);
					const { conversation: bounded, trimmed } = capAndTruncate(conv);
					set({
						conversation: bounded,
						...trimmedState(trimmed),
						status: "open",
						olderCursor: null,
					});
					// C1: Track the plain-open service as boundService with a null
					// sink so loadOlder/mutations validate exact service
					// identity via captureOperationBinding.
					boundService = service as LiveConversationService;
					// Subscribe to notifications for this thread.
					service.subscribeNotifications((n) => {
						if (gen !== conversationGen) return;
						get().applyNotification(n);
					});
				} catch (err) {
					if (gen !== conversationGen) return;
					set({
						status: "error",
						error: err instanceof Error ? err.message : String(err),
					});
				}
			},

			async openProjected(service, sink, ref, replacement) {
				suspendedService = null;
				// Same rule as open(): any thread open retires the prior seam and its
				// rows (the durable target key is hub + ref, and the store cannot see
				// the hub), and the host rebinds for the conversation it opens. The
				// carried provenance survives, keyed by client mutation id.
				detachPendingRows();
				const gen = ++conversationGen;
				// I1: increment the binding epoch and bind service+sink so queued
				// requests from an older binding are suppressed at the boundary.
				bindingEpoch += 1;
				activitySink = sink;
				boundService = service;
				boundSink = sink;
				// Fix round 1 I2: invalidate page ownership on conversation transition.
				loadOlderToken += 1;
				// C1+I1: clear any stale deferred trailing-reread request.
				trailingReread = null;
				transientWarnings.length = 0;
				releaseBoundedTextCache();
				// Reset thread-scoped state (draft, pending mutation) — presentation state
				// now lives outside the store (in live-ui-store).
				// F4: reset the activity sink on thread change.
				sink.reset();
				set({
					status: "opening",
					ref,
					error: null,
					conversation: null,
					olderCursor: null,
					trimmedAbove: false,
					loadingOlder: false,
					draft: "",
					pendingSend: null,
					pendingMutation: null,
					pendingMutations: null,
					lastAcceptedMutation: null,
					conversationGeneration: gen,
				});
				let active = true;
				let unsubscribe: (() => void) | null = null;
				let liveHandler: ((notification: AnyNotification) => void) | null = null;
				try {
					// Subscribe before the read so no frame after its response is missed;
					// a frame before the response is already in the snapshot (the
					// response-cut note by applyThreadNotification), so no handler yet.
					unsubscribe = service.subscribeNotifications((notification) => {
						if (!active || gen !== conversationGen) return;
						liveHandler?.(notification);
					});
					const { conversation, activity, olderCursor, hasEarlierItems, hasLaterItems } =
						replacement ?? (await service.readProjection(ref));
					if (gen !== conversationGen) return;
					const identity: ActivityIdentity = {
						threadId: conversation.threadId,
						ref,
						generation: gen,
					};
					// Strict activity sink: call setLiveView BEFORE committing the paired
					// conversation projection. If it returns false (stale/invalid identity),
					// do not commit the conversation projection — the two stores stay
					// atomically consistent under the same exact identity tuple.
					const accepted = sink.setLiveView(activity, identity);
					if (!accepted) return;
					adoptRead(conversation);
					const { conversation: bounded, trimmed } = capAndTruncate(conversation);
					set({
						conversation: bounded,
						...trimmedState(trimmed),
						status: "open",
						olderCursor,
						hasEarlierItems: hasEarlierItems ?? false,
						hasLaterItems: hasLaterItems ?? false,
					});
					liveHandler = (n) => {
						if (gen !== conversationGen) return;
						// Route notifications to BOTH stores — conversation and activity.
						// Propagate every returned outcome; rehydrate requests the one shared
						// drain scheduler. Notification-first, identity-second.
						const outcome = sink.applyLiveNotification(n, identity);
						if (outcome === "rehydrate") {
							requestRehydrate(ref);
						}
						// Also process the conversation store's own notification handler.
						// Only call the conversation's applyNotification if the activity
						// sink did not say "ignored" (ignored means the activity store
						// rejected it on identity grounds — but conversation still needs
						// its own processing for conversation-specific notifications).
						get().applyNotification(n);
					};
				} catch (err) {
					if (gen !== conversationGen) return;
					set({
						status: "error",
						error: err instanceof Error ? err.message : String(err),
					});
				} finally {
					if (!liveHandler) {
						active = false;
						// The service's cleanup targets its current subscription. Never
						// let an old open unsubscribe a newer conversation's listener.
						if (gen === conversationGen) unsubscribe?.();
					}
				}
			},

			suspendProjected() {
				const state = get();
				if (state.ref === null) return;
				suspendedService = boundService;
				conversationGen += 1;
				bindingEpoch += 1;
				loadOlderToken += 1;
				trailingReread = null;
				boundService = null;
				boundSink = null;
				set({
					status: "opening",
					error: null,
					pendingMutation: null,
					pendingSend: null,
					loadingOlder: false,
					conversationGeneration: conversationGen,
				});
			},

			async resumeProjected(service, sink, ref) {
				const state = get();
				if (state.ref !== ref || state.conversation === null || suspendedService !== service) {
					await get().openProjected(service, sink, ref);
					return;
				}
				const gen = ++conversationGen;
				bindingEpoch += 1;
				loadOlderToken += 1;
				trailingReread = null;
				activitySink = sink;
				boundService = service;
				boundSink = sink;
				set({
					status: "opening",
					error: null,
					pendingMutation: null,
					pendingSend: null,
					loadingOlder: false,
					conversationGeneration: gen,
				});

				let active = true;
				// As in openProjected: no handler until the snapshot has committed (a
				// frame before the response is already in it — the response-cut note
				// by applyThreadNotification).
				let liveHandler: ((notification: AnyNotification) => void) | null = null;
				const unsubscribe = service.subscribeNotifications((notification) => {
					if (!active || gen !== conversationGen) return;
					liveHandler?.(notification);
				});
				try {
					await get().rehydrate(service, sink);
					if (!active || gen !== conversationGen) return;
					const current = get();
					if (
						current.error !== null ||
						acceptedRehydrate === null ||
						acceptedRehydrate.generation !== gen ||
						acceptedRehydrate.sink !== sink
					) {
						boundService = null;
						boundSink = null;
						suspendedService = service;
						set({
							status: "error",
							error: current.error ?? "Could not load the session. Try again.",
						});
						return;
					}
					suspendedService = null;
					set({ status: "open" });
					liveHandler = (notification) => {
						const conversation = get().conversation;
						if (conversation) {
							const outcome = sink.applyLiveNotification(notification, {
								threadId: conversation.threadId,
								ref,
								generation: gen,
							});
							if (outcome === "rehydrate") requestRehydrate(ref);
						}
						get().applyNotification(notification);
					};
				} finally {
					if (gen !== conversationGen || get().status === "error") {
						active = false;
						if (gen === conversationGen) unsubscribe();
					}
				}
			},

			async rehydrate(service, sink) {
				// Rehydrate uses readProjection to refresh the conversation without
				// calling destructive open(). Preserves draft.
				// F3: rehydrate triggers the same shared reread.
				// F9: Operation token prevents stale rehydrate from overwriting
				// newer state within the same generation.
				// I1: rehydrate accepts the expected binding (ref+gen from entry state)
				// rather than resnapshotting current state mid-flight. It can never
				// call serviceA with refB — the scheduler effect verifies the binding
				// epoch before calling this, and rehydrate itself re-checks.
				// I1: Any rehydrate(service, sink) with different objects than the
				// current binding increments bindingEpoch BEFORE assignment, so queued
				// effects captured with the old service/sink are suppressed.
				// R1: capture monotonic mutation-owner revision and error-owner revision
				// (not mutationId/null or error string) at entry. After the await, if
				// either changed, the rehydrate is stale with respect to that owner.
				// If mutation owner changed, do not publish predating projection;
				// arrange one bounded trailing authoritative reread after the mutation
				// settles via the scheduler (no loop, no reentrant await).
				// R2: if page owner changed, safely merge/preserve newer page-owned
				// items/cursor while committing the reread conversation+activity.
				const state = get();
				if (state.ref === null) return;
				acceptedRehydrate = null;
				const ref = state.ref;
				const gen = state.conversationGeneration;
				// I1: If the service or sink objects differ from the current binding,
				// increment bindingEpoch BEFORE assignment so queued effects captured
				// with the old service/sink are suppressed.
				// I5: The rebind transition itself settles any old pending/loading
				// ownership so A's late completion makes ZERO state changes — no
				// permanent UI state stuck. This is the new binding transition, not
				// A's completion. A pending mutation or loadingOlder from the old
				// binding is invalidated by incrementing its operation tokens.
				if (service !== boundService || sink !== boundSink) {
					bindingEpoch += 1;
					// I5: Invalidate old page/loading ownership so a held loadOlder
					// completion from A cannot write loadingOlder/items/cursor.
					loadOlderToken += 1;
					// I5: Settle any held pending mutation from the old binding so a
					// held mutation completion from A cannot write pending/error/draft.
					// Only clear if the current pending mutation belongs to the old
					// binding (it always does — rehydrate is same-generation, and a
					// new binding means the old one's mutation is stale).
					const heldMutation = get().pendingMutation;
					if (heldMutation !== null && heldMutation !== undefined && heldMutation.status === "pending") {
						// Settle the old pending mutation — it belongs to A's binding.
						// Do NOT write error (a newer error owner may own it). Just
						// clear the pending state so it's not permanently stuck.
						set({ pendingMutation: null, pendingSend: null });
					}
					// I5: Settle held loadingOlder from the old binding.
					if (get().loadingOlder) {
						set({ loadingOlder: false });
					}
				}
				// I1: Capture the binding epoch at entry — if it changed during the
				// await (open/close/reset/openProjected/openProjected/rehydrate), this
				// rehydrate is stale.
				const entryEpoch = bindingEpoch;
				const token = ++rehydrateToken;
				// R1: Capture monotonic ownership revisions at entry (not string/id).
				const entryLoadOlderToken = loadOlderToken;
				const entryMutationRev = mutationOwnerRev;
				const entryErrorRev = errorOwnerRev;
				// D18 B3 round 8: the store's own paging cursor at entry. A racing
				// loadOlder that succeeds with no retained rows still advances this
				// value; a failed one moves it not at all — the difference the
				// cursor merge below reads to keep a successful page's advancement
				// without letting a failed page pin a stale pre-race cursor.
				const entryOlderCursor = get().olderCursor;
				activitySink = sink;
				boundService = service;
				boundSink = sink;
				try {
					const { conversation, activity, hasEarlierItems, hasLaterItems } = await service.readProjection(ref);
					// I1: Suppress stale work — if the binding epoch changed, the store
					// switched to a different service/sink/ref. Do not commit.
					if (entryEpoch !== bindingEpoch) return;
					// Guard: a newer generation may have opened during the await.
					if (get().conversationGeneration !== gen) {
						return;
					}
					// F9: Stale rehydrate — a newer rehydrate started in the same gen.
					if (token !== rehydrateToken) {
						return;
					}
					const currentSnapshot = get();
					const mutationOwnerChanged = entryMutationRev !== mutationOwnerRev;
					// R1: If mutation owner changed, do not publish predating projection.
					// Arrange one bounded trailing authoritative reread via the scheduler
					// (no loop, no reentrant await). The trailing reread publishes the
					// fresh projection once the mutation has settled.
					if (mutationOwnerChanged) {
						// C1+I1: Store a deferred trailing reread with the exact binding
						// captured at schedule time. Do NOT schedule through the scheduler
						// yet — the request is drained when the mutation settles. If a
						// request is already pending, it is overwritten (at most one per
						// mutation revision).
						if (trailingReread === null) {
							scheduleTrailingReread();
						}
						// Preserve current draft only — do not publish projection.
						set({ draft: currentSnapshot.draft });
						return;
					}
					// The snapshot is authoritative for every row it carries (the
					// response-cut note by applyThreadNotification): a frame this store
					// folded while the read was in flight is already in it. The one
					// thing the snapshot cannot know about is older history this client
					// paged in — and D23d keeps that in the model itself, so the merge
					// below carries it and the rows re-project from the merged model.
					const currentConvForMerge = currentSnapshot.conversation;
					const sameInstance =
						currentConvForMerge !== null && currentConvForMerge.instanceId === conversation.instanceId;
					const replacesInstance = currentConvForMerge !== null && !sameInstance;
					// The user's display config, read after the read's await: the rows
					// below project at it (which rows exist at all is the projector's
					// decision at that config), and every projection this publish makes
					// — the seated window, the committed rows — shares the one value
					// so they cannot disagree.
					const projectConfig = get().displayConfig ?? undefined;
					// Task 17 (v6 read path): fold this read into whatever versioned
					// history the held model already carries through the package's
					// own applyReadModel (applyReadResponse for a caller whose service
					// layer hydrates the wire response before the merge boundary
					// reaches this package — see its own doc comment). Its disposition
					// (replace/merge/discard) already reads the read's own boot
					// generation/epoch/incarnation/length, so a DIFFERENT backing
					// instance (replacesInstance) naturally replaces on its own;
					// merging is only meaningful against the SAME instance's held
					// history. mergeHistory never removes an item, so the currently
					// running turn's content this read's own bounded window may omit
					// survives without a separate live-turn carve-out (the old
					// withLiveActiveTurn).
					const appliedModel =
						sameInstance && currentConvForMerge !== null
							? applyReadModel(currentConvForMerge, conversation)
							: conversation;
					// applyReadModel returns the held model BY REFERENCE, completely
					// unchanged, when the read's own wire identity disposition is
					// "discard" (an older epoch, a stale same-incarnation length, or
					// an ignorable boot generation — the read raced a resync or a
					// second rehydrate and lost). Bail out exactly like the store's
					// own staleness guards above (entryEpoch/gen/token): committing
					// nothing here is the correct outcome, and continuing would treat
					// this discarded read's own turns as fresh, authoritative content
					// for the strip pass below, clearing real held payloads (an
					// image, a tool's output) a NEWER frame or read already settled.
					// The read itself still SUCCEEDED (the wire answered; the model
					// just declined to apply it) — mark it accepted before returning,
					// or resumeProjected reads the untouched acceptedRehydrate=null
					// from entry as a failure and tears down a healthy subscription
					// over a read that merely lost an identity race. The activity
					// sink is a strict identity gate (applyLiveNotification drops
					// anything whose identity, generation included, does not match
					// what setLiveView last installed) — resumeProjected bumps the
					// generation before this call and binds its live handler to it,
					// so a discard that skips setLiveView leaves the sink pinned to
					// the PRE-suspend generation and silently drops every live frame
					// from here on, freezing the activity/jobs/usage panel. Install
					// the current generation's identity exactly as the accepted path
					// does, keeping the held conversation model.
					if (sameInstance && currentConvForMerge !== null && appliedModel === currentConvForMerge) {
						const discardAccepted = sink.setLiveView(activity, {
							threadId: conversation.threadId,
							ref,
							generation: gen,
						});
						if (discardAccepted) acceptedRehydrate = { generation: gen, sink };
						return;
					}
					// A fresh read can reissue a paged/retained item under a DIFFERENT
					// turn id than before (the versioned merge matches by turn id
					// alone and never renames an item across turns) — fold that the
					// same way loadOlder and the live-frame path
					// (applyThreadNotification) do.
					let mergedTurns: TurnModel[] =
						sameInstance && currentConvForMerge !== null
							? [...reconcileCrossTurnReissues(currentConvForMerge.turns, appliedModel.turns)]
							: [...appliedModel.turns];
					// mergeReplacedItem (reducer.ts) is deliberately omission-tolerant
					// per field (ITEM_OMISSION_TOLERANT_FIELDS/SNAPSHOT_AUTHORITY_FIELDS
					// — an incremental history/updated delta naturally omits a field it
					// did not change, and that must fall back to the held value, not
					// clear it). A full thread/read snapshot is not incremental: it
					// describes the item's CURRENT complete state, so a field it omits
					// for an item it otherwise covers is genuinely absent now — a
					// payload a live frame added while this same read was in flight
					// (an image, a tool's output) must not survive past a clean
					// covering snapshot that says nothing about it. This strips
					// exactly that: only for an item this read's OWN turns actually
					// name (an item outside its window is untouched, page or not —
					// mergeHistory never even saw it to begin with).
					if (sameInstance && currentConvForMerge !== null) {
						const freshItemByIdentity = new Map<string, ItemModel>();
						for (const turn of conversation.turns) {
							for (const item of turn.items) {
								freshItemByIdentity.set(item.transcriptKey ?? item.id, item);
								freshItemByIdentity.set(item.id, item);
							}
						}
						mergedTurns = mergedTurns.map((turn) => {
							let turnChanged = false;
							const items = turn.items.map((item) => {
								const fresh =
									freshItemByIdentity.get(item.transcriptKey ?? item.id) ?? freshItemByIdentity.get(item.id);
								if (fresh === undefined || !itemIdentityMatches(item, fresh)) return item;
								let stripped: ItemModel | undefined;
								for (const field of SNAPSHOT_AUTHORITY_FIELDS) {
									const heldValue = (item as unknown as Record<string, unknown>)[field];
									const freshValue = (fresh as unknown as Record<string, unknown>)[field];
									if (heldValue === undefined || freshValue !== undefined) continue;
									stripped ??= copyItemTextPresence(item, { ...item });
									(stripped as unknown as Record<string, unknown>)[field] = Array.isArray(heldValue) ? [] : undefined;
								}
								if (stripped === undefined) return item;
								turnChanged = true;
								return stripped;
							});
							if (!turnChanged) return turn;
							return { ...turn, items };
						});
					}
					const merged = withSyncedHistoryTurns({ ...appliedModel, turns: mergedTurns }, mergedTurns);
					// The wire-truth cursor is applyReadModel's own: it already keeps
					// the held older cursor when the retained history overlaps this
					// read's window, and takes the fresh read's own cursor otherwise
					// (the same rule mergeOlderItemPage's cursor carries).
					let mergedCursor = merged.olderCursor ?? null;
					// D18 B3 round 8: a racing loadOlder that retained no display rows
					// (every row deduped, or cap-evicted) still advanced the store's
					// own paging cursor, and the fresh reread's window cursor knows
					// nothing about pages this client already consumed — keep the
					// advancement, or the next loadOlder re-requests that page (or
					// resurrects paging at a cursor exhausted history had stopped).
					// A FAILED racing page also bumps the page token but moves the
					// cursor not at all, so the entry comparison
					// — not the token, and not row ownership — is what separates the
					// two: the fresh read's own signal still wins unless the store's
					// own cursor actually moved during the await.
					const pageCursorAdvanced = sameInstance && currentSnapshot.olderCursor !== entryOlderCursor;
					if (pageCursorAdvanced) mergedCursor = currentSnapshot.olderCursor;
					const identity: ActivityIdentity = {
						threadId: conversation.threadId,
						ref,
						generation: gen,
					};
					// Strict activity sink: call setLiveView BEFORE committing the paired
					// conversation projection. If it returns false, do not commit.
					const accepted = sink.setLiveView(activity, identity);
					if (!accepted) return;
					acceptedRehydrate = { generation: gen, sink };
					adoptRead(conversation);
					// R1: Success preserves any newer error owner. Only clear error if
					// the error-owner revision hasn't changed AND no failed mutation
					// owns the error. A failed mutation's error persists until a
					// subsequent mutation or open clears it.
					const currentState = get();
					const errorUnchanged = entryErrorRev === errorOwnerRev;
					const mutationOwnsError = currentState.pendingMutation?.status === "failed";
					// #1919 follow-up: bound the merged result AFTER the merge, so
					// turns inside the keep-window keep everything the held history
					// supplied, and only out-of-window payloads trim. The final
					// retained rows are the window; the pass settles page turn
					// ownership with the same bound.
					// RoboRev round 2 (panel Medium 1): the window must be the one the
					// publish below will actually show — capAndTruncate seats the
					// transient warnings before the cap, and a seated notice consumes
					// a cap slot, so an unseated bound would retain page payloads for
					// rows the seated cap just evicted.
					// RoboRev local round 1 (Medium): the publish shows exactly this
					// seated window, so hoist it — a notice the window kept survives
					// the commit even when the bound sheds the turn its anchor row
					// came from.
					let seatedRehydrateItems: MobileTimelineItem[] | null = null;
					if (sameInstance) {
						const rehydrateModel = { ...merged, turns: mergedTurns };
						const rehydrateSeated = seatTransientWarnings(projectTimeline(rehydrateModel, undefined, projectConfig));
						// RoboRev panel: the keep-window is level-independent at this
						// merge too — a coarse rehydrate must not shed the payloads of
						// the turns the level hides (retentionWindowItems unions the
						// show-everything cap; the null config keeps the seated window
						// alone).
						mergedTurns = boundRetainedTurns(
							mergedTurns,
							retentionWindowItems(rehydrateModel, retainedRows(rehydrateSeated), projectConfig ?? null),
							conversation.activeTurnId,
						);
						seatedRehydrateItems = rehydrateSeated;
					}
					if (replacesInstance) {
						transientWarnings.length = 0;
					}
					// The snapshot's thread-level fields are authoritative (see the
					// response-cut note by applyThreadNotification); the rows are a
					// projection of the merged model — the snapshot's own turns plus
					// the page history the model still carries, one projector for a
					// frame and for a snapshot.
					// The projection runs at the user's display config, read after the
					// read's await: which rows exist at all is the projector's decision
					// at that config, and the level is read at merge time so a change
					// that landed mid-read projects here (setDisplayConfig's own
					// re-projection already republished the old level; this publish
					// supersedes it with the fresh read).
					// RoboRev local round 1 (Medium): on the history-preserving path
					// the committed rows are the SAME seated projection the
					// retained-turn bound trimmed against — the projector's input is
					// the pre-trim merged model, exactly the rows loadOlder commits —
					// so a notice the window kept stays seated even when the bound
					// sheds its anchor row. Re-projecting here instead would drop the
					// shed rows, leave the notice no anchor to seat at, and the
					// carried-filter prune would retire a warning the cap kept.
					const { conversation: committedConversation, trimmed: rehydrateTrimmed } = capAndTruncate(
						withSyncedHistoryTurns(
							seatedRehydrateItems === null
								? projectConversation(
										{
											...merged,
											turns: mergedTurns,
											olderCursor: merged.olderCursor,
										},
										undefined,
										projectConfig,
									)
								: {
										...merged,
										turns: mergedTurns,
										olderCursor: merged.olderCursor,
										items: seatedRehydrateItems,
									},
							mergedTurns,
						),
					);
					const commitBase = {
						conversation: committedConversation,
						// A read of another instance replaces the window, and with it
						// any trim above it; the same instance keeps its trim.
						...(replacesInstance ? { trimmedAbove: rehydrateTrimmed } : rehydrateTrimmed ? { trimmedAbove: true } : {}),
						olderCursor: mergedCursor,
						hasEarlierItems: hasEarlierItems ?? currentSnapshot.hasEarlierItems,
						hasLaterItems: hasLaterItems ?? currentSnapshot.hasLaterItems,
						draft: currentState.draft,
					};
					if (errorUnchanged && !mutationOwnsError && currentState.error !== null) {
						// I1: Only include error: null when the current error is actually
						// non-null. Clearing an already-null error would spuriously
						// increment errorOwnerRev (ABA-null), blocking a pending
						// mutation from writing its error after a rehydrate clear-to-null.
						set({ ...commitBase, error: null });
					} else {
						// A mutation or page operation owns the error, or error is already
						// null — preserve it (do not spuriously increment errorOwnerRev).
						set(commitBase);
					}
				} catch (err) {
					// R1: failure may set error only if error/page/mutation owners all
					// remain unchanged. Check binding epoch, generation, token, page
					// owner, mutation-owner revision, and error-owner revision.
					const currentSnapshot = get();
					if (
						entryEpoch === bindingEpoch &&
						currentSnapshot.conversationGeneration === gen &&
						token === rehydrateToken &&
						entryLoadOlderToken === loadOlderToken &&
						entryMutationRev === mutationOwnerRev &&
						entryErrorRev === errorOwnerRev
					) {
						set({
							error: err instanceof Error ? err.message : String(err),
						});
					}
				}
			},

			async loadOlder(service) {
				const state = get();
				if (state.loadingOlder || state.conversation === null) {
					return { status: "ignored" };
				}
				// Older history is above: past the cursor, or trimmed from the top by
				// the cap, when the page names the oldest row kept (thread/turns/list's
				// before), rebasing the cursor when there is one.
				// A hub that has stopped paging from a before position since the trim
				// can't bring those rows back, so nothing above them is asked for.
				const cursor = state.olderCursor;
				const before = trimBoundary(state);
				if (before !== undefined && !pagesBefore) return { status: "ignored" };
				if (cursor === null && before === undefined) return { status: "ignored" };
				const gen = state.conversationGeneration;
				// C1: Capture a service-specific operation binding. If the supplied
				// service is wrong (A after B bound), binding is null — zero request.
				const opBinding = captureOperationBinding(service);
				if (opBinding === null) return { status: "ignored" };
				// Task 2A-Ops-2: Operation token for loadOlder — a stale success/failure
				// from an older operation must make no state change at all after a
				// newer conversation or newer page operation owns those fields.
				const olderToken = ++loadOlderToken;
				// I1: Capture the error-owner revision BEFORE the call. A current page
				// failure always settles its own loadingOlder, but writes error only if
				// its captured error owner is unchanged; a newer mutation/page error
				// survives.
				const entryErrorRev = errorOwnerRev;
				set({ loadingOlder: true });
				try {
					const result = await service.loadOlder(cursor, before);
					// C1: Recheck the exact operation binding after the await. If the
					// binding changed (rebind to B), A's completion makes ZERO state
					// changes — no items/cursor/loading.
					if (!isBindingCurrent(opBinding)) return { status: "ignored" };
					// Fix round 1 I2: generation/identity-stale — perform ZERO set calls
					// (including loadingOlder). The newer conversation owns all fields.
					if (get().conversationGeneration !== gen) {
						return { status: "ignored" };
					}
					// Task 2A-Ops-2: Stale loadOlder — a newer page operation owns the
					// loadingOlder/error fields. Make no state change at all.
					if (olderToken !== loadOlderToken) return { status: "ignored" };
					const currentConv = get().conversation;
					if (currentConv !== null) {
						// Task 17 (v6 read path): the page merges into the model through
						// the package's own versioned mergeOlderItemPage, keyed by turn id
						// and per-turn item identity (transcriptKey ?? id) — it never
						// removes an item and never renames one across turns, so none of
						// the old fold-provenance bookkeeping (page ownership through
						// folds, compacted-turn skeleton injection, failure-claim
						// migration) has anything left to do: a turn's scalar fields
						// (including its error) are simply whatever the highest-version
						// copy says, and a trimmed (compacted) turn's later reappearance
						// under the SAME turn id merges back in on its own. The one thing
						// the versioned merge does NOT do is cross-turn dedup: a page can
						// re-serve an identity the window already holds under a
						// DIFFERENT turn id (the pagination boundary can split a turn
						// differently than a prior page did), which reconcileCrossTurnReissues
						// — written for the live-frame path — folds the same way here.
						// D23d: the page merges into the model through the package's own
						// versioned merge and the rows re-project from the merged model.
						// The service hands over the page's wire turns alone
						// (projectOlderTurns and its fake Thread are deleted), so the
						// merge's identity rules ARE the page dedupe — an item the
						// window already holds folds by transcript key or bare id, a
						// cluster split at the pagination boundary keeps its un-held
						// members (the projector clusters what survives), and a page
						// fragment supplements the fields the live side omits (the
						// web's rule, decision 2 — the row filter and the model-side
						// strip pass that mirrored it are gone with the rows). The I3
						// question-row filter is gone the same way: rows re-project
						// through liveAskQuestions, which gates on askPending and
						// excludes every ask before the newest resolution item, so a
						// page's settled ask renders as the tool row it is.
						// D18 B3 round 3/6: conversation.turns/olderCursor (the
						// ThreadModel fields sessionTokens reads) must stay in sync with
						// items/the store's own olderCursor, or a session with no
						// cumulative usage keeps summing only the first page after older
						// turns load. thread/turns/list is itself item-paginated, so an
						// older page can carry a fragment of a turn already in the
						// window; folding through the package's own mergeOlderItemPage
						// (turnsMatch/mergePageTurn) reconciles that by identity instead
						// of an id-only filter, which would drop the fragment or
						// double-count it under a different id.
						// Record page ownership by turn ID separately from the page item
						// identities — a turn survives here even
						// when every one of its display rows is deduped away or evicted.
						// Every identity the page's own raw wire turns carry — the
						// page's ownership set (D23d), built directly off the wire
						// response rather than merge fold provenance: the versioned
						// merge never renames an item's identity across turns, so a raw
						// page item's identity is exactly the identity it merges under.
						const beforeIdentities = new Set<string>();
						for (const turn of currentConv.turns) {
							for (const item of turn.items) {
								beforeIdentities.add(item.transcriptKey ?? item.id);
								beforeIdentities.add(item.id);
							}
						}
						const rawPageIdentities = new Set<string>();
						for (const turn of result.turnsPage.data) {
							for (const item of turn.items ?? []) {
								rawPageIdentities.add(item.transcriptKey ?? item.id);
								rawPageIdentities.add(item.id);
							}
						}
						// A failed turn projects its own row keyed failure:<turnId>
						// (projectedRows.ts), never an item identity — count it too, or
						// a page whose only contribution is a failure row reports none.
						// A real v6 turn id is
						// stable (mergeHistory never renames one), so the raw page
						// turn's own id is exactly the merged output's id.
						for (const turn of result.turnsPage.data) {
							if (turn.error !== undefined) rawPageIdentities.add(failureRowIdentity(turn.id));
						}
						// itemKeys is the reader's no-progress signal (nonempty clears
						// its retry guard): an identity already held before this page
						// merged in contributed nothing new, whatever field the page's
						// own (possibly higher-version) copy carried — a page that only
						// re-serves retained content, even under a different turn id
						// (reconcileCrossTurnReissues below folds that away), must
						// report empty.
						const pageKeys = new Set<string>();
						for (const id of rawPageIdentities) {
							if (!beforeIdentities.has(id)) pageKeys.add(id);
						}
						const versionedMerge = mergeOlderItemPage(currentConv, result.turnsPage);
						if (versionedMerge === currentConv) {
							// A real discard (pageDisposition's "discard" branch, or a
							// stale-model race) returns the model UNCHANGED — the exact
							// object passed in. This is strictly narrower than comparing
							// `.turns` by reference: mergeHistory and withDisplay's
							// displayTurns also return the held turns array by reference
							// on a genuine "merge" disposition whose page turns out to
							// carry nothing newer than what is already held, so `.turns`
							// equality alone cannot tell a no-op merge apart from a
							// discard — see the no-op-merge test below.
							set({ loadingOlder: false });
							return { status: "ignored" };
						}
						const heldHistory = currentConv.history;
						const mergedHistory = versionedMerge.history;
						if (mergedHistory?.invalidatedAtGeneration !== undefined) {
							// "defer" (the page waits on an already-invalid thread's
							// pending incarnation) and "invalidate" (this page just
							// announced the invalidation itself) both leave the merged
							// history invalid — pageDisposition never reaches "merge"
							// while held is already invalid, and "invalidate" always
							// marks the fresh history invalid via `invalidated()` — so
							// this test alone separates them from a real merge. Neither
							// advances the cursor: a deferred page resolves itself once
							// the held incarnation's own latest-window read lands (per
							// applyReadModel/applyReadResponse's own deferredPages
							// replay); an invalidated one means this page raced a resync
							// mid-flight and only a fresh read can recover.
							set({ loadingOlder: false });
							// A newly-invalidated thread needs a rehydrate; re-arming an
							// ALREADY-invalid one (a second, newer signal arriving before
							// the recovering read lands) needs one too, since the earlier
							// request was issued against the now-superseded `awaited`
							// identity. invalidatedAtGeneration itself cannot tell the two
							// apart from "still invalid, nothing new" here: this caller's
							// issuedGeneration never advances (mobile never issues
							// issueLatestWindowRead), so invalidatedAtGeneration can only
							// ever move from undefined to 0 — comparing it before/after
							// misses a re-invalidation (the awaited signal moving from one
							// real epoch/incarnation to a newer one) entirely. Compare the
							// signal that actually changes instead.
							const newlyInvalidated =
								heldHistory?.invalidatedAtGeneration === undefined ||
								heldHistory.awaited?.bootGeneration !== mergedHistory.awaited?.bootGeneration ||
								heldHistory.awaited?.epoch !== mergedHistory.awaited?.epoch ||
								heldHistory.pendingIncarnation !== mergedHistory.pendingIncarnation;
							if (newlyInvalidated) {
								requestRehydrate(currentConv.ref);
							}
							return { status: "ignored" };
						}
						// A genuine merge — advance the cursor and take the normal
						// "loaded" path below even when the page turned out to carry
						// nothing newer than what is already held (mergeHistory then
						// returns `turns` unchanged by reference; itemKeys ends up
						// empty, which is the reader's own honest no-progress signal).
						// A page can re-serve an identity the window already holds under
						// a DIFFERENT turn id than before (the pagination boundary can
						// split a turn differently across pages) — the versioned merge
						// matches turns by id alone and never removes an item, so this
						// is the one dedup it does not do on its own; fold it exactly as
						// the live-frame path (applyThreadNotification) does.
						const strippedPageTurns: TurnModel[] = [
							...reconcileCrossTurnReissues(currentConv.turns, versionedMerge.turns),
						];
						// The rows re-project from the merged model. mergedInput is the
						// pre-cap projection.
						// RoboRev review round 4: the transient warnings seat into it
						// here, with the same seating the publish performs — a notice
						// consumes a cap slot, so the window the retained-turn bound
						// trims against must count it. capAndTruncate's carried filter makes the
						// re-seat below a no-op.
						const mergedModel = withSyncedHistoryTurns({ ...currentConv, turns: strippedPageTurns }, strippedPageTurns);
						// The page merge projects at the user's display config, exactly
						// as the rehydrate and frame paths do: one projector, one level.
						const mergedInput = seatTransientWarnings(
							projectTimeline(mergedModel, undefined, get().displayConfig ?? undefined),
						);
						// Paging never ends on a trim: a page that lands while the
						// reader follows the live end is trimmed like any publish, and
						// the next page names the oldest row kept (trimmedAbove).
						const nextCursor = result.nextCursor ?? null;
						const pageMerged = retainedRows(mergedInput);
						const { conversation: pageConversation, trimmed: pageTrimmed } = capAndTruncate({
							...mergedModel,
							items: mergedInput,
						});
						const merged = pageConversation.items;
						// #1919 follow-up: bound the retained turn payloads against the
						// final retained rows (pageMerged), after the merge — a page
						// turn whose payloads left the keep-window trims to identity +
						// usage (boundRetainedTurns's own comment).
						// RoboRev panel: the keep-window is level-independent at this
						// merge too (retentionWindowItems unions the show-everything
						// cap; the null config keeps pageMerged alone) — a coarse page
						// load must not shed the payloads of the turns the level hides.
						const boundedTurns = boundRetainedTurns(
							strippedPageTurns,
							retentionWindowItems(mergedModel, pageMerged, get().displayConfig),
							currentConv.activeTurnId,
						);
						set({
							// conversation.olderCursor is the wire truth (result.nextCursor).
							conversation: withSyncedHistoryTurns(
								{
									...pageConversation,
									turns: boundedTurns,
									olderCursor: result.nextCursor,
								},
								boundedTurns,
							),
							olderCursor: nextCursor,
							trimmedAbove: pageTrimmed,
							hasEarlierItems: result.hasEarlierItems ?? get().hasEarlierItems,
							hasLaterItems: result.hasLaterItems ?? get().hasLaterItems,
							loadingOlder: false,
						});
						// The page's own contributions the retained window still holds:
						// what this loadOlder brought that the cap did not discard.
						const retainedIds = new Set<string>();
						for (const row of merged) {
							for (const id of timelineIdentities(row)) retainedIds.add(id);
						}
						return {
							status: "loaded",
							itemKeys: [...pageKeys].filter((id) => retainedIds.has(id)),
						};
					}
					return { status: "ignored" };
				} catch (err) {
					// A stale v4 item cursor invalidates the visible transcript
					// incarnation. Rehydrate before surfacing the failure so the next
					// user retry starts from the refreshed bounded state and cursor.
					if (isStaleCursorError(err) && opBinding.sink !== null && isBindingCurrent(opBinding)) {
						// Rehydrate only the operation's still-current binding. A stale
						// page from service A must not use service B's sink after a
						// rebind, even if both conversations share a ref.
						await get().rehydrate(opBinding.service, opBinding.sink);
					}
					// C1: Recheck the exact operation binding after the await. If the
					// binding changed (rebind to B), A's completion makes ZERO state
					// changes — no loadingOlder/error.
					if (!isBindingCurrent(opBinding)) return { status: "ignored" };
					// I1: A current page failure always settles its own loadingOlder,
					// but writes error only if its captured error owner is unchanged;
					// a newer mutation/page error/clear/ABA survives.
					if (get().conversationGeneration === gen && olderToken === loadOlderToken) {
						// I1: always settle loadingOlder (this page owns it), but only
						// write error if the error-owner revision hasn't changed. A
						// newer clear-to-null or ABA-null owns error — revision equality
						// ONLY, no || currentError === null shortcut.
						if (entryErrorRev === errorOwnerRev) {
							set({
								loadingOlder: false,
								error: err instanceof Error ? err.message : String(err),
							});
						} else {
							// A newer error owner published (even to null) — preserve it,
							// only settle loadingOlder.
							set({ loadingOlder: false });
						}
						return { status: "failed", error: err };
					}
					return { status: "ignored" };
				}
			},

			setDraft(text) {
				draftRevision += 1;
				set({ draft: text });
			},

			bindPendingMutations(port) {
				// One conversation follows one target: release any prior binding first.
				detachPendingRows();
				// Replacing the binding retires the prior target's published rows at
				// once: they belong to the seam being replaced, and a replacement whose
				// own first read never lands must not leave them standing.
				set({ pendingMutations: null });
				const generation = pendingGeneration;
				pendingPort = port;
				// Within one binding, only a read newer than the last PUBLISHED one may
				// publish: a slow earlier read cannot resurrect a row a newer successful
				// read already dropped, and a FAILED read (sync throw or rejection)
				// never advances the fence, so an earlier in-flight read still publishes
				// its newer state rather than being suppressed by a read that produced
				// nothing.
				let readRequest = 0;
				let lastPublishedRequest = 0;

				const read = () => {
					const request = ++readRequest;
					const provenanceGeneration = pendingProvenanceGeneration;
					let pending: ReturnType<ConversationMutationPendingPort["read"]>;
					try {
						pending = port.read();
					} catch {
						// A synchronous refusal is the read's failure: keep the last
						// durable projection, exactly as an async rejection does.
						return;
					}
					void pending.then(
						(snapshot) => {
							let recordedProvenance = false;
							// The provenance scan is fenced only on a forget (close/reset):
							// a read in flight across a same-target rebind or a thread open,
							// or one a newer read superseded, still observed durable records
							// of this client's own, and the package store it mirrors records
							// submitted-here before applying its own fences. Ids are unique
							// per submission, so this cannot contaminate another target.
							if (provenanceGeneration === pendingProvenanceGeneration) {
								for (const record of [...snapshot.outbox, ...snapshot.optimistic]) {
									// Target-scoped exactly as the reconciliation is: a foreign
									// target's row must not claim this client's provenance.
									if (record.targetRef === port.targetRef && port.isOwnMutationRecord(record)) {
										if (!pendingSubmittedHere.has(record.clientMutationId)) {
											recordedProvenance = true;
										}
										pendingSubmittedHere.set(record.clientMutationId, record.createdAt);
									}
								}
							}
							// Newly recorded provenance reaches the already-published
							// projection at once, through whatever binding is CURRENT (which
							// may differ from this read's: a same-target rebind). The
							// reconciliation reads the current snapshot, never this fenced
							// read's, so a newer read's removals cannot be resurrected.
							if (
								recordedProvenance &&
								pendingPort !== null &&
								pendingSnapshot !== null &&
								pendingPort.targetRef === port.targetRef
							) {
								republishPendingRows();
							}
							// A retired binding publishes nothing of its own snapshot.
							if (generation !== pendingGeneration) return;
							// Only a read newer than the last published one publishes.
							if (request <= lastPublishedRequest) return;
							lastPublishedRequest = request;
							pendingSnapshot = snapshot;
							// The same stability check the conversation-write path uses: a
							// storage read that re-serves identical rows must not churn
							// subscribers with a fresh array.
							republishPendingRows();
						},
						() => {
							// A failed read leaves the last durable projection standing: the
							// shared projection fence's own rule, so a storage hiccup never
							// blanks rows the user is watching. A later read retries.
						},
					);
				};

				pendingUnsubscribe = port.subscribe(read);
				read();
				return () => {
					if (generation !== pendingGeneration) return;
					detachPendingRows();
					set({ pendingMutations: null });
				};
			},

			rememberSubmittedHere(clientMutationId) {
				pendingSubmittedHere.set(clientMutationId, Date.now());
				// Until the first durable read lands there is no projection to update.
				if (pendingPort === null || pendingSnapshot === null) return;
				republishPendingRows();
			},

			bindPendingMutationsIfUnbound(port) {
				// Already following a seam: leave it alone, so a host that re-runs its
				// rebind after a generation bump the store did not retire (suspend,
				// rehydrate) does not churn a live subscription.
				if (pendingPort !== null) return null;
				return get().bindPendingMutations(port);
			},

			async send(service, input) {
				const state = get();
				if (state.conversation === null) return;
				requireControl(state.conversation, "send", "send");
				// C1: Capture a service-specific operation binding. If the supplied
				// service is wrong (A after B bound), zero request/state change.
				const opBinding = captureOperationBinding(service);
				if (opBinding === null) return;
				const draftText = state.draft;
				const gen = state.conversationGeneration;
				const mutationId = ++mutationIdCounter;
				const revisionAtSubmit = draftRevision;
				const mutation: ConversationMutationState = {
					kind: "send",
					status: "pending",
					draftSnapshot: draftText,
					draftRevisionAtSubmit: revisionAtSubmit,
					generation: gen,
					mutationId,
				};
				// Clearing the draft via set() (not setDraft) does NOT increment
				// draftRevision — so any subsequent setDraft call (including
				// type-then-delete) increments the revision and is detected as an edit.
				// Fix round 1 I3: atomically clear prior error with new pending mutation.
				set({
					draft: "",
					pendingSend: "pending",
					pendingMutation: mutation,
					lastAcceptedMutation: null,
					error: null,
				});
				// I1: Capture the error-owner revision AFTER installing the pending
				// mutation + error-clear (the set wrapper incremented errorOwnerRev
				// for the error: null transition). A newer error owner that writes
				// during the await will increment errorOwnerRev past this captured
				// value, so this mutation's success/failure cannot clear or overwrite
				// it.
				const entryErrorRev = errorOwnerRev;
				try {
					const receipt = await dispatchMutation(service, "send", opBinding, state.conversation, input);
					// C1: Recheck the exact operation binding after the await.
					if (!isBindingCurrent(opBinding)) return;
					// F4: Check mutationId — out-of-order completion cannot clear a
					// newer mutation's state.
					if (get().pendingMutation?.mutationId === mutationId) {
						// I1: Clear pending fields unconditionally (this mutation owns
						// them), but clear error only if the error-owner revision is
						// unchanged — revision equality ONLY, no || error === null
						// shortcut. A newer clear-to-null or ABA-null owns error.
						if (entryErrorRev === errorOwnerRev) {
							set({
								pendingSend: null,
								pendingMutation: null,
								lastAcceptedMutation: { kind: "send", receipt },
								error: null,
							});
						} else {
							set({
								pendingSend: null,
								pendingMutation: null,
								lastAcceptedMutation: { kind: "send", receipt },
							});
						}
						// I1: mutation settled (success) — drain deferred trailing reread.
						drainTrailingReread();
					}
				} catch (err) {
					// C1: Recheck the exact operation binding after the await.
					if (!isBindingCurrent(opBinding)) return;
					handleMutationError(
						err,
						state.ref,
						mutationId,
						mutation,
						draftText,
						revisionAtSubmit,
						entryErrorRev,
						getDraftRevision,
						getErrorOwnerRev,
						set,
						get,
						requestRehydrate,
					);
					// I1: mutation settled (failed terminal) — drain deferred trailing
					// reread after handleMutationError sets the failed state.
					drainTrailingReread();
				}
			},

			async steer(service, input, expectedQueueRevision) {
				const state = get();
				if (state.conversation === null) return;
				requireControl(state.conversation, "steer", "steer");
				// C1: Capture a service-specific operation binding.
				const opBinding = captureOperationBinding(service);
				if (opBinding === null) return;
				const draftText = state.draft;
				const gen = state.conversationGeneration;
				const mutationId = ++mutationIdCounter;
				const revisionAtSubmit = draftRevision;
				const mutation: ConversationMutationState = {
					kind: "steer",
					status: "pending",
					draftSnapshot: draftText,
					draftRevisionAtSubmit: revisionAtSubmit,
					generation: gen,
					mutationId,
				};
				// Steer/queue clear the draft on submit like send.
				// F10: any new mutation clears legacy pendingSend.
				// Fix round 1 I3: atomically clear prior error with new pending mutation.
				set({
					draft: "",
					pendingSend: null,
					pendingMutation: mutation,
					lastAcceptedMutation: null,
					error: null,
				});
				// I1: Capture error-owner revision AFTER installing pending+error-clear.
				const entryErrorRev = errorOwnerRev;
				try {
					const receipt = await dispatchMutation(
						service,
						"steer",
						opBinding,
						state.conversation,
						input,
						expectedQueueRevision,
					);
					// C1: Recheck the exact operation binding after the await.
					if (!isBindingCurrent(opBinding)) return;
					if (get().pendingMutation?.mutationId === mutationId) {
						// I1: clear error only if error-owner revision is unchanged —
						// revision equality ONLY.
						if (entryErrorRev === errorOwnerRev) {
							set({
								pendingMutation: null,
								lastAcceptedMutation: { kind: "steer", receipt },
								error: null,
							});
						} else {
							set({
								pendingMutation: null,
								lastAcceptedMutation: { kind: "steer", receipt },
							});
						}
						// I1: mutation settled (success) — drain deferred trailing reread.
						drainTrailingReread();
					}
				} catch (err) {
					// C1: Recheck the exact operation binding after the await.
					if (!isBindingCurrent(opBinding)) return;
					handleMutationError(
						err,
						state.ref,
						mutationId,
						mutation,
						draftText,
						revisionAtSubmit,
						entryErrorRev,
						getDraftRevision,
						getErrorOwnerRev,
						set,
						get,
						requestRehydrate,
					);
					// I1: mutation settled (failed terminal) — drain deferred trailing
					// reread after handleMutationError sets the failed state.
					drainTrailingReread();
				}
			},

			async queue(service, input) {
				const state = get();
				if (state.conversation === null) return;
				requireQueue(state.conversation, state.pendingMutations);
				// C1: Capture a service-specific operation binding.
				const opBinding = captureOperationBinding(service);
				if (opBinding === null) return;
				const draftText = state.draft;
				const gen = state.conversationGeneration;
				const mutationId = ++mutationIdCounter;
				const revisionAtSubmit = draftRevision;
				const mutation: ConversationMutationState = {
					kind: "queue",
					status: "pending",
					draftSnapshot: draftText,
					draftRevisionAtSubmit: revisionAtSubmit,
					generation: gen,
					mutationId,
				};
				// F10: any new mutation clears legacy pendingSend.
				// Fix round 1 I3: atomically clear prior error with new pending mutation.
				set({
					draft: "",
					pendingSend: null,
					pendingMutation: mutation,
					lastAcceptedMutation: null,
					error: null,
				});
				// I1: Capture error-owner revision AFTER installing pending+error-clear.
				const entryErrorRev = errorOwnerRev;
				try {
					const receipt = await dispatchMutation(service, "queue", opBinding, state.conversation, input);
					// C1: Recheck the exact operation binding after the await.
					if (!isBindingCurrent(opBinding)) return;
					if (get().pendingMutation?.mutationId === mutationId) {
						// I1: clear error only if error-owner revision is unchanged —
						// revision equality ONLY.
						if (entryErrorRev === errorOwnerRev) {
							set({
								pendingMutation: null,
								lastAcceptedMutation: { kind: "queue", receipt },
								error: null,
							});
						} else {
							set({
								pendingMutation: null,
								lastAcceptedMutation: { kind: "queue", receipt },
							});
						}
						// I1: mutation settled (success) — drain deferred trailing reread.
						drainTrailingReread();
					}
				} catch (err) {
					// C1: Recheck the exact operation binding after the await.
					if (!isBindingCurrent(opBinding)) return;
					handleMutationError(
						err,
						state.ref,
						mutationId,
						mutation,
						draftText,
						revisionAtSubmit,
						entryErrorRev,
						getDraftRevision,
						getErrorOwnerRev,
						set,
						get,
						requestRehydrate,
					);
					// I1: mutation settled (failed terminal) — drain deferred trailing
					// reread after handleMutationError sets the failed state.
					drainTrailingReread();
				}
			},

			async interrupt(service) {
				const state = get();
				if (state.conversation === null) return;
				requireControl(state.conversation, "stop", "interrupt");
				// C1: Capture a service-specific operation binding.
				const opBinding = captureOperationBinding(service);
				if (opBinding === null) return;
				const gen = state.conversationGeneration;
				const mutationId = ++mutationIdCounter;
				const revisionAtSubmit = draftRevision;
				const mutation: ConversationMutationState = {
					kind: "interrupt",
					status: "pending",
					// Interrupt does NOT snapshot the draft — it should remain as-is.
					draftSnapshot: null,
					draftRevisionAtSubmit: revisionAtSubmit,
					generation: gen,
					mutationId,
				};
				// Interrupt does NOT clear the draft.
				// F10: any new mutation clears legacy pendingSend.
				// Fix round 1 I3: atomically clear prior error with new pending mutation.
				set({
					pendingSend: null,
					pendingMutation: mutation,
					lastAcceptedMutation: null,
					error: null,
				});
				// I1: Capture error-owner revision AFTER installing pending+error-clear.
				const entryErrorRev = errorOwnerRev;
				try {
					const receipt = await dispatchMutation(service, "interrupt", opBinding, state.conversation, []);
					// C1: Recheck the exact operation binding after the await.
					if (!isBindingCurrent(opBinding)) return;
					if (get().pendingMutation?.mutationId === mutationId) {
						// I1: clear error only if error-owner revision is unchanged —
						// revision equality ONLY.
						if (entryErrorRev === errorOwnerRev) {
							set({
								pendingMutation: null,
								lastAcceptedMutation: {
									kind: "interrupt",
									receipt,
								},
								error: null,
							});
						} else {
							set({
								pendingMutation: null,
								lastAcceptedMutation: {
									kind: "interrupt",
									receipt,
								},
							});
						}
						// I1: mutation settled (success) — drain deferred trailing reread.
						drainTrailingReread();
					}
				} catch (err) {
					// C1: Recheck the exact operation binding after the await.
					if (!isBindingCurrent(opBinding)) return;
					handleMutationError(
						err,
						state.ref,
						mutationId,
						mutation,
						null,
						revisionAtSubmit,
						entryErrorRev,
						getDraftRevision,
						getErrorOwnerRev,
						set,
						get,
						requestRehydrate,
					);
					// I1: mutation settled (failed terminal) — drain deferred trailing
					// reread after handleMutationError sets the failed state.
					drainTrailingReread();
				}
			},

			close() {
				suspendedService = null;
				// Increment generation so late frames from the closed conversation
				// cannot repopulate the store.
				++conversationGen;
				// I1: increment the binding epoch and clear bindings so queued
				// requests from the closed conversation are suppressed.
				bindingEpoch += 1;
				// Fix round 1 I2: invalidate page ownership on conversation transition.
				loadOlderToken += 1;
				// C1+I1: clear any stale deferred trailing-reread request.
				trailingReread = null;
				transientWarnings.length = 0;
				releaseBoundedTextCache();
				detachPendingRows();
				forgetPendingProvenance();
				// F4: reset the activity sink on close.
				if (activitySink !== null) {
					activitySink.reset();
					activitySink = null;
				}
				boundService = null;
				boundSink = null;
				set({
					status: "closed",
					conversation: null,
					ref: null,
					draft: "",
					pendingSend: null,
					pendingMutation: null,
					pendingMutations: null,
					lastAcceptedMutation: null,
					olderCursor: null,
					trimmedAbove: false,
					loadingOlder: false,
					conversationGeneration: conversationGen,
				});
			},

			applyNotification(n) {
				const state = get();
				if (state.conversation === null) return;

				// The package reducer's routing: a frame names its thread by ref
				// when it carries one, else by threadId; a frame naming neither is
				// not about this thread. Silently drop everything else.
				if (!notificationTargetsThread(n, state.conversation)) return;
				// A status frame the hub relays names pageBefore for its thread; one
				// straight from a daemon names none and leaves the last answer.
				if (n.method === "thread/status/changed" && n.params.threadId === state.conversation.threadId)
					adoptPageBefore(n.params.capabilities?.pageBefore, pagesBefore);

				// Every frame about this thread folds into the package reducer's
				// model, and the display rows are a projection of that model: one
				// rule for a frame and for a snapshot, so there is nothing for a row
				// applier to disagree with. The two display bounds run after the
				// projection.
				let applied = applyThreadNotification(state.conversation, n);
				// activeTurnId is otherwise snapshot-only (reducer.ts's
				// activeTurnIdFromThread runs once, at hydrate); history/updated's
				// own turn-scalar merge (mergeHistory) only adds/updates items and
				// turn fields by version and never moves this field. The mobile
				// store is this field's one live writer: a frame's own turn opening
				// (inProgress) becomes the active turn, and a frame settling the
				// CURRENTLY active turn (any status the wire uses to close one)
				// clears it. A frame naming some OTHER, already-settled turn changes
				// nothing (the "not on a superseded turn" rule).
				if (n.method === "history/updated") {
					let activeTurnId = applied.activeTurnId;
					for (const turn of n.params.turns ?? []) {
						if (turn.status === "inProgress") activeTurnId = turn.id;
						else if (turn.id === activeTurnId) activeTurnId = undefined;
					}
					if (activeTurnId !== applied.activeTurnId) {
						applied = { ...applied, activeTurnId };
					}
				}
				// RoboRev round 34: a warning with no active turn is the one frame
				// the wire drops entirely (and never persists, so no read can carry
				// it back), yet it is a real diagnostic — the projector emits
				// EventWarning unconditionally, and a prompt-render failure on a
				// model change lands exactly here, while idle. The store keeps it
				// itself: the notice goes to the transient surface capAndTruncate
				// reseats at its arrival position on every rebuild, and this
				// frame takes the full
				// publish path below so the row reaches the screen at once. A
				// warning WITH an active turn needs none of this — the reducer
				// folds it into the turn's items and the projection renders it.
				// RoboRev round 37: an active turn the LOADED WINDOW does not hold
				// is the same drop — the wire can name one (a resumed old turn
				// while the window holds only newer history), and the reducer's
				// fold then finds no turn to attach the warning to (mapTurn hands
				// back the same turns), while the gap reread the frame requests
				// can never carry the warning back either, the wire not
				// persisting warnings. The notice therefore goes to the transient
				// surface whenever the reducer could not place the warning, not
				// only when idle.
				const unplacedWarningNotice =
					n.method === "warning" &&
					(!applied.activeTurnId || !applied.turns.some((turn) => turn.id === applied.activeTurnId))
						? idleWarningRow(n.params)
						: null;
				if (unplacedWarningNotice !== null) {
					transientWarnings.push({
						row: unplacedWarningNotice,
						anchor: arrivalAnchor(
							state.conversation.items,
							new Set(transientWarnings.map((notice) => timelineIdentity(notice.row))),
						),
					});
				}
				if (applied !== state.conversation) {
					if (changesRows(state.conversation, applied) || unplacedWarningNotice !== null) {
						// The frame's rows project at the user's display config — the
						// same level every other publish projects at.
						const projected = projectConversation(applied, undefined, get().displayConfig ?? undefined);
						const { conversation: bounded, trimmed } = capAndTruncate(projected);
						// #1919 follow-up: a row-changing frame can repopulate a
						// compacted turn's entire payload (a completion's full view)
						// with no applier pass left to bound it — the keep-window is
						// the level-independent retention set (retentionWindowItems:
						// the capped level rows plus the show-everything cap), so a
						// coarse level never sheds a turn it merely hides; the active
						// turn is exempted inside the helper (its payloads are the
						// live working set).
						const conversation: MobileConversation = {
							...bounded,
							turns: boundRetainedTurns(
								bounded.turns,
								retentionWindowItems(applied, bounded.items, get().displayConfig),
								bounded.activeTurnId,
							),
						};
						set({ conversation, ...trimmedState(trimmed) });
					} else {
						// The model advanced — the frame is the authority on whatever it
						// carried, and lastFrameAt moved — but no row changed, so the rows
						// this conversation already published stand, by reference.
						set({ conversation: { ...applied, items: state.conversation.items } });
					}
				}
				if (state.ref === null) return;
				// evener/thread/resync is the authoritative refresh path (the store
				// never re-reads on its own). Any frame about the transcript that the
				// reducer could not place — an unknown method, one naming an item or
				// turn this model does not hold, a warning or a steer whose active
				// turn lies outside the loaded window — leaves `turns` untouched by
				// reference (every applied fold rebuilds it through mapTurn, which
				// hands back the same array when nothing matched), while the model
				// itself is still a new object because the frame is evidence of
				// liveness. That is the gap case: the transcript this client holds is
				// missing what the frame was about, so it asks for the canonical read
				// at once rather than showing an incomplete transcript until the next
				// resync. Thread-level frames (a status, a name, the queue) never
				// touch turns and are not gaps, so they are named out.
				//
				// history/updated is the read model's one transcript frame now (its
				// predecessors — the per-item/per-turn lifecycle notifications, and
				// evener/steering/injected, whose "steering" item history/updated now
				// carries directly — are gone). The package's applyHistoryUpdated
				// hands back `turns` by reference whenever it merged nothing (a stale
				// replay under the one generation state machine) or invalidated the
				// thread (a newer epoch or incarnation than held — the read model's
				// own gap, "a history update whose epoch is newer than held"), so
				// the identity check below already recognizes both without a
				// separate epoch comparison here.
				// The gap rule is a DENYLIST, not an allowlist: per the comment
				// above, ANY frame the reducer could not place — including a
				// forward-compat method this client has no case for at all — is a
				// gap when it leaves `turns` untouched by reference, and only the
				// known thread-scalar frames (never touch turns; reducer.ts's own
				// cases for each just restamp a scalar field and lastFrameAt) are
				// named out. evener/thread/resync is handled by the clause's other
				// arm above and warning by droppedByTheWiresOwnRule below, so
				// neither needs to be named here.
				const touchesTranscript = !THREAD_SCALAR_ONLY_METHODS.has(n.method);
				// One exception, by the wire's own rule rather than by this window's
				// contents: a warning that lands with no active turn is dropped in the
				// reducer because warnings are never transcript-persisted (its
				// "warning" case cites internal/apptranscript having no warning-item
				// conversion), so the canonical read cannot carry that warning either.
				// Nothing is missing from the transcript and there is nothing to
				// fetch — the store displays the dropped warning from its own
				// transient surface instead (transientWarnings, above).
				const droppedByTheWiresOwnRule = n.method === "warning" && !state.conversation.activeTurnId;
				if (
					n.method === "evener/thread/resync" ||
					(touchesTranscript && !droppedByTheWiresOwnRule && applied.turns === state.conversation.turns)
				) {
					requestRehydrate(state.ref);
				}
				// The transient-warning settle finding (RoboRev, Sep-18, on this
				// piece's pre-restack branch): a warning folds into the active
				// turn's items and a bare turn/completed settles the turn without
				// touching them, so the projected warning row lingered with nothing
				// in flight to clear it. The wire never persists warnings, so the
				// canonical read is the one honest way to drop what the settle kept:
				// a settled turn that still holds warning items requests it.
				// turn/completed's read-model replacement, history/updated, carries
				// the settling turn's scalars (id/status) in its own `turns` field —
				// it never removes items itself (mergeHistory only adds/updates by
				// identity), so a warning added before this thread's first
				// history/updated (the narrow window in which the reducer's
				// "warning" case still inserts a live item — see its `model.history`
				// gate) would otherwise never get cleared once its turn settles.
				if (n.method === "history/updated") {
					for (const turn of n.params.turns ?? []) {
						if (turn.status === "inProgress") continue;
						const settledTurn = applied.turns.find((t) => t.id === turn.id);
						if (settledTurn?.items.some((item) => item.type === "warning")) {
							requestRehydrate(state.ref);
							break;
						}
					}
				}
			},

			reset() {
				suspendedService = null;
				// Invalidate the current conversation generation so late frames are
				// rejected, then return to idle.
				++conversationGen;
				// I1: increment the binding epoch and clear bindings so queued
				// requests from the prior conversation are suppressed.
				bindingEpoch += 1;
				// Fix round 1 I2: invalidate page ownership on conversation transition.
				loadOlderToken += 1;
				// C1+I1: clear any stale deferred trailing-reread request.
				trailingReread = null;
				transientWarnings.length = 0;
				releaseBoundedTextCache();
				detachPendingRows();
				forgetPendingProvenance();
				// F4: reset the activity sink on thread change.
				if (activitySink !== null) {
					activitySink.reset();
					activitySink = null;
				}
				boundService = null;
				boundSink = null;
				set({
					ref: null,
					profileId: null,
					conversation: null,
					olderCursor: null,
					trimmedAbove: false,
					loadingOlder: false,
					status: "idle",
					error: null,
					draft: "",
					pendingSend: null,
					pendingMutation: null,
					pendingMutations: null,
					lastAcceptedMutation: null,
					conversationGeneration: conversationGen,
				});
			},

			// Task3: store-owned external error publication seam. Writes the generic
			// sanitized message as the conversation error ONLY when the current ref
			// and conversationGeneration exactly match the expected values the
			// dispatcher captured. Any mismatch — a profile/thread switch, a reset,
			// or an open transition — makes zero set calls: the full state object
			// reference is unchanged and no subscriber fires. The write goes through
			// the wrapped set so errorOwnerRev advances; a subsequent rehydrate that
			// captured the older revision must then preserve this newer external
			// error rather than clearing it. expectedRef null matches a null current
			// ref (e.g. an error published against the idle store) but never matches
			// an active conversation's non-null ref.
			publishExternalError(message, expectedRef, expectedGeneration) {
				const state = get();
				if (state.ref !== expectedRef || state.conversationGeneration !== expectedGeneration) {
					return;
				}
				set({ error: message });
			},
		};
	});
}

// Shared mutation error handler. On failure, the failed mutation state
// PERSISTS (not cleared to null). The draft is only restored if the user has
// not edited since the mutation cleared the draft — detected via
// draftRevision, so type-then-delete (which produces "" but increments the
// revision) counts as an edit and prevents restore. F4/F10: uses mutationId
// (not generation alone) so out-of-order failure cannot overwrite a newer
// mutation's error.
function handleMutationError(
	err: unknown,
	ref: string | null,
	mutationId: number,
	mutation: ConversationMutationState,
	draftSnapshot: string | null,
	revisionAtSubmit: number,
	entryErrorRev: number,
	getDraftRevision: () => number,
	getErrorOwnerRev: () => number,
	set: (partial: Partial<ConversationState>) => void,
	get: () => ConversationState,
	requestRehydrate: (ref: string) => void,
): void {
	// F10: Check active mutation first. If a newer mutation has already
	// started, this error is stale — bail out.
	if (get().pendingMutation?.mutationId !== mutationId) return;

	// The hub refused because its capabilities moved on without a status frame
	// this client saw. The web's rule (decision 2): surface the typed error and
	// let the model converge through the reducer — one coalesced reread of the
	// authoritative snapshot — rather than a bespoke capability read and write.
	if (isActionUnavailableError(err) && ref !== null) requestRehydrate(ref);

	// F10: Re-check mutationId — out-of-order failure cannot change a newer
	// mutation, error, or draft.
	if (get().pendingMutation?.mutationId === mutationId) {
		// The failed mutation state PERSISTS — do NOT clear pendingMutation.
		// Task 2A-Ops-4: Draft revision prevents type-delete restore — only
		// restore if the draft revision has NOT changed since the mutation
		// cleared the draft. Type-then-delete produces "" but increments the
		// revision, so it counts as an edit and prevents restore.
		// I1: A current mutation failure may install failed pending/draft
		// restoration, but must NOT overwrite a newer error owner. Only write
		// error if the captured error-owner revision is unchanged — revision
		// equality ONLY, no || currentError === null shortcut. Revision
		// equality preserves ANY newer error owner including a clear-to-null
		// (which increments errorOwnerRev but writes null) and an ABA same
		// text/null re-set. A stale rehydrate that tries to CLEAR a newer
		// mutation's error is handled in the rehydrate success path via
		// entryErrorRev comparison. Here, the mutation failure owns the error
		// write only when no newer owner (null or non-null) advanced the
		// revision during the await.
		const shouldRestore = draftSnapshot !== null && getDraftRevision() === revisionAtSubmit;
		const newError = err instanceof Error ? err.message : String(err);
		if (entryErrorRev === getErrorOwnerRev()) {
			set({
				pendingMutation: { ...mutation, status: "failed" },
				pendingSend: null,
				...(shouldRestore ? { draft: draftSnapshot } : {}),
				error: newError,
			});
		} else {
			// A newer error owner published (non-null error, null clear, or ABA
			// same text/null re-set) — preserve it. Only install the failed
			// pending/draft restoration.
			set({
				pendingMutation: { ...mutation, status: "failed" },
				pendingSend: null,
				...(shouldRestore ? { draft: draftSnapshot } : {}),
			});
		}
	}
}
