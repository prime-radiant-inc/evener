// ConversationService wraps an AppwireClient to provide a typed, capability-gated
// API for opening, reading, paging, and mutating a single session thread. It is
// the protocol/native service layer between the Zustand ConversationStore and
// the wire: the store calls the service; the service calls the client.
//
// The service does NOT create or own the socket — it wraps an existing
// AppwireClient (or any structurally compatible client, e.g. FakeClient in
// tests). It hydrates the wire Thread through the package (hydrateThread),
// projects the display rows via projectConversation, subscribes to
// notifications, and enforces ThreadCapabilities before every mutation: a
// false capability blocks the request entirely, never reaching the wire. A
// server-reported actionUnavailable triggers a non-subscribing capability
// re-read so the store's cached capabilities stay fresh.
//
// The service guards its current identity and capabilities with open epochs;
// the store owns profile/connection/conversation generations and view publication.

import type {
	AnyNotification,
	AppwireClient,
	AppwireClientLike,
	ThreadSubscriptionLease,
	InputItem,
	MethodName,
	MethodTypes,
	ModelControlAuthority,
	ModelControlInput,
	ModelControlRequestFence,
	ModelListParams,
	ModelListResponse,
	MutationReceipt,
	SnapshotIdentity,
	Thread,
	ThreadCapabilities,
	ThreadClearResponse,
	ThreadForkResponse,
	ThreadItemPosition,
	ThreadReadResponse,
	ThreadModel,
	ThreadTurnsListResponse,
	TranscriptDisplayConfigV1,
	TurnCancelQueuedResponse,
} from "@evener/appwire-client";
import {
	acquireThreadSubscription,
	applyModelControlNotification,
	applyNotification,
	hydrateThread,
	isStaleCursorError,
	modelControlAuthority,
	modelControlRecoveryInput,
	modelPickCanSend as sharedModelPickCanSend,
	reduceModelControl,
	sameModelControlAuthority,
	SHUT_DOWN_STATUSES,
	validateThreadModelSetResponse,
	WireError,
} from "@evener/appwire-client";
import type { MobileConversation } from "../../projectedRows";
import { projectConversation } from "../../projectedRows";

// The bounded read page limit, centralized so every caller uses the same
// constant. The retained item cap lives with the shared projection helpers
// (the row module's RETAINED_ITEM_CAP, mobile-native/src/projectedRows.ts); nothing here needs its own
// copy.
export const READ_ITEM_LIMIT = 40;

/** Whether a model pick on `session` can reach the wire. The rule is the
 * shared client's (modelPickCanSend in @evener/appwire-client), so the sheet
 * and the browser picker cannot disagree about which sessions offer one. */
export function modelPickCanSend(session: MobileConversation): boolean {
	return sharedModelPickCanSend(session);
}

// The statuses a resume can wake, so a model pick on them has a path to
// authority (resumeThenAuthorize). The shared shut-down set only:
// restartRequired names a live daemon whose read carries no changeModel and
// whose thread/resume the hub refuses.
function isColdSessionStatus(statusType: string): boolean {
	return SHUT_DOWN_STATUSES.has(statusType);
}

// FORK_ENTRY_SOURCE_ITEM_KEY_TURN_ID fills the turn-id component of the item
// key forkFromTurn builds from transcriptEntryIndex. Mobile does not yet carry
// each transcript item's own key (MobileTimelineItem.transcriptKey) through
// to this call site, only its 1-based entry index; the hub's item-key parser
// (transcriptindex.ParseItemKey) recovers the divergence position from the
// key's ordinal alone and never reads this component, so a fixed placeholder
// is sufficient until that plumbing lands.
const FORK_ENTRY_SOURCE_ITEM_KEY_TURN_ID = "mobile-fork-entry";

// forkEntrySourceItemKey spells transcriptindex.ItemKey's format:
// apptranscript-item-v2:<turnID>:<entry ordinal>:<part>. transcriptEntryIndex
// is already 1-based (wire ThreadItem.transcriptEntryIndex, i.e. ordinal + 1),
// so the ordinal is transcriptEntryIndex - 1; part is 0 (the divergence names
// a transcript position, not a specific content part).
function forkEntrySourceItemKey(transcriptEntryIndex: number): string {
	return `apptranscript-item-v2:${FORK_ENTRY_SOURCE_ITEM_KEY_TURN_ID}:${transcriptEntryIndex - 1}:0`;
}

// The narrow client surface the service depends on. Structurally compatible
// with AppwireClient and FakeClient, so tests inject a FakeClient without
// pulling the real class's reconnect/heartbeat machinery.
export interface ConversationClientLike extends Pick<AppwireClientLike, "state" | "onReady" | "onStateChange"> {
	request<M extends MethodName>(
		method: M,
		params: MethodTypes[M]["params"],
		opts?: { timeoutMs?: number },
	): Promise<MethodTypes[M]["result"]>;
	onNotification(cb: (n: AnyNotification) => void): () => void;
	forceStop?(ref: string): Promise<void>;
	resumeThread?(ref: string): Promise<MethodTypes["thread/resume"]["result"]>;
}

// A function that generates a clientMutationId. Default uses crypto.randomUUID
// when available, falling back to a counter; tests inject a deterministic one.
export type IdFactory = () => string;

export type ConversationModelControlRecoveryInput = Extract<
	ModelControlInput,
	{ kind: "authorityStale" | "postDispatchFailure" | "snapshot" }
>;

export type ConversationModelControlRecoveryHandler = (
	threadRef: string,
	expectedThreadId: string,
	input: ConversationModelControlRecoveryInput,
) => void;

// The conversation's instanceId is the service's own fence value, not
// hydrateThread's pass-through of the wire field: a daemon-backed read always
// carries evener.instanceId (appsource/local_daemon.go stamps it with a
// thread-id fallback), but the hub's past-session read (app_threadread.go)
// stamps none, and the fork and queue affordances key on the field. One rule,
// applied here where the fence is computed, so the displayed conversation and
// the mutations it offers can never disagree about the instance.

export interface ConversationServiceOptions<ReadLease = unknown> {
	readonly idFactory?: IdFactory;
	// The clock hydrateThread stamps the model with; tests inject a fixed one.
	readonly now?: () => number;
	// The transcript display config the projection runs at (D24-5's content
	// dimension, routed through the seam): read at projection time, so a read
	// that starts under one level and lands after the user changed it projects
	// at the level the store will republish anyway. Null/undefined means the
	// show-everything default — a hub that does not support the settings, or a
	// host that has not supplied the resolver.
	readonly resolveDisplayConfig?: () => TranscriptDisplayConfigV1 | null;
	// Native wires these to its durable mutation host: onReadStart leases the
	// target just before the raw authoritative read, and onReadComplete hands
	// the same raw response back so the runtime can settle its dispatch gate
	// from exactly the snapshot that produces the conversation projection. A
	// host that has no runtime passes neither and nothing changes.
	readonly onReadStart?: (threadRef: string, expectedThreadId?: string) => ReadLease | undefined;
	readonly onReadComplete?: (lease: ReadLease | undefined, response: ThreadReadResponse) => void | Promise<unknown>;
}

export interface ConversationReadProjection {
	conversation: MobileConversation;
	// The store applies out-of-window changes to the older pages it holds.
	changes?: ThreadReadResponse["changes"];
	olderCursor: string | null;
	hasEarlierItems?: boolean;
	hasLaterItems?: boolean;
	// True only on the stale-snapshot recovery path: the server refused the
	// held window as predating its bounded update log, and the fresh window
	// below replaces the held history instead of merging with it. A merge
	// would keep the older pages' stale content — an item that changed while
	// the client was disconnected — and advance the snapshot length past the
	// skipped update with no way back.
	replacedHeldHistory?: boolean;
	modelControlRecoveryRequired?: true;
}

// The canonical service interface — preserved for screen test mocks that only
// need the basic open/send/queue/interrupt/close surface.
export interface ConversationService {
	open(ref: string, cursor?: string): Promise<MobileConversation>;
	// The page above cursor, or above before (the oldest row kept after a trim):
	// with both, before rebases the cursor; with before alone the hub mints one.
	loadOlder(
		cursor: string | null,
		before?: ThreadItemPosition,
	): Promise<{
		// The page's own wire turns - the store
		// folds these into conversation.turns via the package's own
		// identity-aware merge (a turn can be split into fragments across the
		// page boundary; an id-only filter drops or double-counts the split),
		// so sessionTokens's turn-summed fallback covers what's actually loaded,
		// not just the first page, and projects the display rows from that
		// merged model (D23d: the model owns the older page and its cursor).
		turnsPage: ThreadTurnsListResponse;
		nextCursor?: string;
		hasEarlierItems?: boolean;
		hasLaterItems?: boolean;
	}>;
	subscribeNotifications(
		handler: (n: AnyNotification) => void,
		modelControlRecoveryHandler?: ConversationModelControlRecoveryHandler,
	): () => void;
	send(input: InputItem[]): Promise<MutationReceipt>;
	queue(input: InputItem[]): Promise<MutationReceipt>;
	interrupt(): Promise<MutationReceipt>;
	compact(): Promise<void>;
	shutdown(): Promise<void>;
	changeModel(modelProvider: string, model: string): Promise<void>;
	setVisionModel(visionModel: string): Promise<void>;
	setReasoningEffort(effort: string): Promise<void>;
	rename(name: string): Promise<void>;
	cancelQueued(index: number, expectedEntryId: string, expectedInstanceId: string): Promise<TurnCancelQueuedResponse>;
	close(): void;
}

export interface ConversationRecoveryActions {
	forceStop(): Promise<void>;
	resume(): Promise<void>;
}

// Required live behavior interface for the live read/projection path. This
// must be implemented by any service that supports the live conversation
// features (readProjection, openProjected, rehydrate, coalescer, mutation
// state). It extends the canonical ConversationService with the live-only
// methods that must not be optional-fallback to the old open() path.
export interface LiveConversationService extends ConversationService {
	readProjection(ref: string, heldSnapshot?: SnapshotIdentity): Promise<ConversationReadProjection>;
}

export interface ConversationModelCatalog {
	models(): Promise<ModelListResponse>;
}

export interface ConversationGoalActions {
	setGoal(objective: string): Promise<void>;
}

export interface ConversationForkActions {
	forkAside(): Promise<ThreadForkResponse>;
}

export interface ConversationTurnForkActions {
	forkFromTurn(transcriptEntryIndex: number): Promise<ThreadForkResponse>;
}

export interface ConversationClearActions {
	clear(): Promise<ThreadClearResponse>;
	adoptClear(response: ThreadClearResponse): ConversationReadProjection;
}

let defaultIdCounter = 0;
function defaultIdFactory(): string {
	if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
		return crypto.randomUUID();
	}
	defaultIdCounter += 1;
	return `cmid-${Date.now()}-${defaultIdCounter}`;
}

// The 12 legacy capability fields that must be present and boolean in every
// ThreadCapabilities. Extra keys from future protocol versions are allowed but
// never retained in the extracted copy; sharedNotes is one of those extras (see
// the absent-means-unsupported handling below).
const REQUIRED_CAPABILITY_FIELDS = [
	"send",
	"steer",
	"interrupt",
	"compact",
	"clear",
	"forkFromTurn",
	"shutdown",
	"changeModel",
	"changeVisionModel",
	"queue",
	"goal",
	"rename",
] as const;

// Extract and runtime-validate capabilities into a complete plain local
// ThreadCapabilities copy. All 12 required fields must be present and
// boolean; extra keys are allowed but not retained. Null, non-object,
// wrong-type, or throwing-getter inputs throw before any state write,
// leaving the ref+capabilities pair null/fail-closed. The returned copy
// never retains the response object or its getters.
function extractCapabilities(raw: unknown): ThreadCapabilities {
	if (raw === null || typeof raw !== "object") {
		throw new Error("ConversationService: capabilities is not an object");
	}
	const obj = raw as Record<string, unknown>;
	const caps: ThreadCapabilities = {
		send: false,
		steer: false,
		interrupt: false,
		compact: false,
		clear: false,
		forkFromTurn: false,
		shutdown: false,
		changeModel: false,
		changeVisionModel: false,
		sharedNotes: false,
		queue: false,
		goal: false,
		rename: false,
	};
	for (const field of REQUIRED_CAPABILITY_FIELDS) {
		const value = obj[field];
		if (typeof value !== "boolean") {
			throw new Error(`ConversationService: capability "${field}" is not a boolean`);
		}
		caps[field] = value;
	}
	// sharedNotes is newer than the protocol version this client still speaks, so
	// a peer built before it omits the field entirely: absent means "not
	// supported" and must not reject the whole payload, which would fail every
	// capability-gated action for the session. A present value is still validated.
	const sharedNotes = obj.sharedNotes;
	if (sharedNotes !== undefined) {
		if (typeof sharedNotes !== "boolean") {
			throw new Error(`ConversationService: capability "sharedNotes" is not a boolean`);
		}
		caps.sharedNotes = sharedNotes;
	}
	// The hub stamps pageBefore on reads and on the status frames it relays; a
	// hub that predates it omits it, which leaves it absent (no paging before).
	const pageBefore = obj.pageBefore;
	if (pageBefore !== undefined) {
		if (typeof pageBefore !== "boolean") {
			throw new Error(`ConversationService: capability "pageBefore" is not a boolean`);
		}
		caps.pageBefore = pageBefore;
	}
	// voiceArm is the hub's per-session answer, stamped on reads and on the status
	// frames it relays. A hub that predates it omits the field; dropping a pushed
	// value would replace a read's true with the default and hide voice.
	const voiceArm = obj.voiceArm;
	if (voiceArm !== undefined) {
		if (typeof voiceArm !== "boolean") {
			throw new Error(`ConversationService: capability "voiceArm" is not a boolean`);
		}
		caps.voiceArm = voiceArm;
	}
	const modelControlVersion = obj.modelControlVersion;
	if (modelControlVersion !== undefined) {
		if (
			typeof modelControlVersion !== "number" ||
			!Number.isSafeInteger(modelControlVersion) ||
			modelControlVersion < 1
		) {
			throw new Error('ConversationService: capability "modelControlVersion" is invalid');
		}
		caps.modelControlVersion = modelControlVersion;
	}
	return caps;
}

type MutationKind = "send" | "queue" | "cancel" | "interrupt" | "clear";
export const CANONICAL_MUTATION_DISPOSITIONS = ["applied", "replayed"] as const;
export type CanonicalMutationDisposition = (typeof CANONICAL_MUTATION_DISPOSITIONS)[number];

const CANONICAL_MUTATION_PROJECTION: Readonly<Record<MutationKind, "pending" | "reflected" | "removed">> = {
	send: "pending",
	queue: "pending",
	cancel: "removed",
	interrupt: "reflected",
	clear: "reflected",
};

function requireObject(raw: unknown, label: string): Record<string, unknown> {
	if (raw === null || typeof raw !== "object" || Array.isArray(raw)) {
		throw new Error(`ConversationService: ${label} is not an object`);
	}
	return raw as Record<string, unknown>;
}

function exactObject(raw: unknown, keys: readonly string[], label: string): Record<string, unknown> {
	const object = requireObject(raw, label);
	const actual = Object.keys(object).sort();
	const expected = [...keys].sort();
	if (actual.length !== expected.length || actual.some((key, index) => key !== expected[index])) {
		throw new Error(`ConversationService: ${label} has unexpected keys`);
	}
	return object;
}

function nonemptyString(raw: unknown, label: string): string {
	if (typeof raw !== "string" || raw.trim() === "") {
		throw new Error(`ConversationService: ${label} is empty or invalid`);
	}
	return raw;
}

// Every receipt carries the correlation fields below, and each mutation kind
// adds the keys its own receipt requires. A key from KNOWN_RECEIPT_KEYS that a
// kind does not expect is a malformed receipt and is rejected; a key outside
// that vocabulary is an additive field no shipped build has seen, and is
// ignored. Jesse's 2026-09-17 ruling keeps additive AppWire changes on the
// current ProtocolVersion, so an older peer must read a new optional key as no
// information -- rejecting it would turn every additive wire field into a hard
// failure for a phone build already in testers' hands (issue #1759). The
// vocabulary is derived from the per-kind tables below so the two cannot drift.
// The result envelope around a receipt stays exact via exactObject.
const RECEIPT_CORRELATION_KEYS = ["clientMutationId", "disposition", "threadId", "projectionState"] as const;

type RequiredReceiptKey = (typeof RECEIPT_CORRELATION_KEYS)[number] | "turnId" | "queueEntryIds";

const REQUIRED_RECEIPT_KEYS_BY_KIND: Readonly<Record<MutationKind, readonly RequiredReceiptKey[]>> = {
	send: [...RECEIPT_CORRELATION_KEYS, "turnId"],
	interrupt: [...RECEIPT_CORRELATION_KEYS, "turnId"],
	queue: [...RECEIPT_CORRELATION_KEYS, "queueEntryIds"],
	cancel: [...RECEIPT_CORRELATION_KEYS, "queueEntryIds"],
	clear: [...RECEIPT_CORRELATION_KEYS],
};

// A receipt key the daemon includes only when it has something to report: any
// kind may name the instance the receipt is bound to.
const OPTIONAL_RECEIPT_KEYS_ANY_KIND = ["instanceId"] as const;

const KNOWN_RECEIPT_KEYS: ReadonlySet<string> = new Set([
	...Object.values(REQUIRED_RECEIPT_KEYS_BY_KIND).flat(),
	...OPTIONAL_RECEIPT_KEYS_ANY_KIND,
]);

function decodedReceipt(raw: unknown, expectedKeys: readonly string[], label: string): Record<string, unknown> {
	const receipt = requireObject(raw, label);
	const expected = new Set(expectedKeys);
	for (const key of Object.keys(receipt)) {
		if (!expected.has(key) && KNOWN_RECEIPT_KEYS.has(key)) {
			throw new Error(`ConversationService: ${label} has unexpected keys`);
		}
	}
	for (const key of expected) {
		if (!Object.hasOwn(receipt, key)) {
			throw new Error(`ConversationService: ${label} is missing ${key}`);
		}
	}
	return receipt;
}

function decodeMutationResult(
	kind: MutationKind,
	raw: unknown,
	clientMutationId: string,
	expectedInstanceId: string,
): MutationReceipt {
	const resultKeys =
		kind === "clear"
			? ["receipt", "thread", "ref"]
			: kind === "send"
				? ["receipt", "turn"]
				: kind === "cancel"
					? [
							"receipt",
							"removedText",
							...(raw !== null && typeof raw === "object" && "removedImages" in raw ? ["removedImages"] : []),
						]
					: ["receipt"];
	const result = exactObject(raw, resultKeys, `${kind} result`);
	if (kind === "cancel") {
		if (
			typeof result.removedText !== "string" ||
			("removedImages" in result &&
				(!Number.isSafeInteger(result.removedImages) || (result.removedImages as number) < 0))
		) {
			throw new Error("ConversationService: invalid cancellation echo");
		}
	}
	if (kind === "send") {
		const turn = result.turn;
		if (turn === null || typeof turn !== "object" || Array.isArray(turn)) {
			throw new Error("ConversationService: send result turn is not an object");
		}
		nonemptyString((turn as Record<string, unknown>).id, "send turn id");
	}

	const receiptObject =
		result.receipt !== null && typeof result.receipt === "object" ? (result.receipt as Record<string, unknown>) : null;
	const kindReceiptKeys = REQUIRED_RECEIPT_KEYS_BY_KIND[kind];
	const requiredReceiptKeys: string[] = [...kindReceiptKeys];
	for (const key of OPTIONAL_RECEIPT_KEYS_ANY_KIND) {
		if (receiptObject !== null && Object.hasOwn(receiptObject, key)) {
			requiredReceiptKeys.push(key);
		}
	}
	const receipt = decodedReceipt(result.receipt, requiredReceiptKeys, `${kind} receipt`);
	if (receipt.clientMutationId !== clientMutationId) {
		throw new Error(`ConversationService: ${kind} receipt correlation mismatch`);
	}
	if (!CANONICAL_MUTATION_DISPOSITIONS.includes(receipt.disposition as CanonicalMutationDisposition)) {
		throw new Error(`ConversationService: ${kind} receipt was not applied`);
	}
	const disposition = receipt.disposition as CanonicalMutationDisposition;
	if (receipt.projectionState !== CANONICAL_MUTATION_PROJECTION[kind]) {
		throw new Error(`ConversationService: ${kind} projection state is invalid`);
	}
	const decoded: MutationReceipt = {
		clientMutationId,
		disposition,
		threadId: nonemptyString(receipt.threadId, `${kind} thread id`),
		projectionState: nonemptyString(receipt.projectionState, `${kind} projection state`),
	};
	if (Object.hasOwn(receipt, "instanceId")) {
		if (receipt.instanceId !== expectedInstanceId) {
			throw new Error(`ConversationService: ${kind} receipt instance mismatch`);
		}
		decoded.instanceId = expectedInstanceId;
	}
	// decodedReceipt has checked the kind's keys are present and no other
	// known key is, so the kind's table says which of these the receipt holds.
	if (kindReceiptKeys.includes("turnId")) {
		decoded.turnId = nonemptyString(receipt.turnId, `${kind} turn id`);
	}
	if (kindReceiptKeys.includes("queueEntryIds")) {
		const ids = receipt.queueEntryIds;
		if (!Array.isArray(ids) || ids.length === 0 || ids.some((id) => typeof id !== "string" || id.trim() === "")) {
			throw new Error("ConversationService: queue entry ids are empty or invalid");
		}
		decoded.queueEntryIds = [...ids];
	}
	return decoded;
}

function validateCancelQueued(
	result: unknown,
	clientMutationId: string,
	expectedInstanceId: string,
	expectedThreadId: string,
	expectedEntryId: string,
): void {
	const receipt = decodeMutationResult("cancel", result, clientMutationId, expectedInstanceId);
	const ids = receipt.queueEntryIds;
	if (
		receipt.threadId !== expectedThreadId ||
		receipt.instanceId !== expectedInstanceId ||
		ids?.length !== 1 ||
		ids[0] !== expectedEntryId
	) {
		throw new Error("ConversationService: queue action receipt identity mismatch");
	}
}

export function createConversationService<ReadLease = unknown>(
	client: ConversationClientLike | AppwireClient,
	options: ConversationServiceOptions<ReadLease> = {},
): LiveConversationService &
	ConversationModelCatalog &
	ConversationGoalActions &
	ConversationForkActions &
	ConversationTurnForkActions &
	ConversationClearActions &
	ConversationRecoveryActions {
	const idFactory: IdFactory = options.idFactory ?? defaultIdFactory;
	const now = options.now ?? Date.now;
	// The display config this read's projection runs at, read at projection
	// time (see the option's own comment). A null/undefined answer keeps the
	// show-everything default.
	const displayConfig = (): TranscriptDisplayConfigV1 | undefined => options.resolveDisplayConfig?.() ?? undefined;

	// Current thread identity and capabilities, set by open() / readProjection().
	// Mutations check these before reaching the wire; a re-read on
	// actionUnavailable refreshes them. ref+capabilities form one fail-closed
	// lifecycle pair: a new open/readProjection clears BOTH ref=null and
	// capabilities=null BEFORE awaiting so the service is fail-closed while the
	// read is in flight (a mutation cannot send against a prior thread's gates
	// or the pending thread's not-yet-validated ref). The pair is installed
	// together only on the current epoch's success; a failed read leaves both
	// null, so requireRef-only operations (setReasoningEffort, cancelQueued,
	// loadOlder) also fail before any wire call.
	let ref: string | null = null;
	let subscription: { ref: string; lease: ThreadSubscriptionLease } | null = null;
	const subscribedRead = (threadRef: string) => {
		if (subscription?.ref !== threadRef) {
			subscription?.lease.release();
			subscription = { ref: threadRef, lease: acquireThreadSubscription(client, threadRef) };
		}
		return subscription.lease;
	};
	let modelScope: ModelListParams | null = null;
	// Retain the instance shown by the full read; a capability-only refresh
	// must not redirect a draft to a replacement session.
	let instanceId: string | null = null;
	let threadId: string | null = null;
	let opening: {
		ref: string;
		threadId: string | null;
		pushed: { threadId: string; caps: ThreadCapabilities } | null;
		modelControl: Array<{
			notification: Extract<AnyNotification, { method: "thread/model/controlChanged" }>;
			receivedAt: number;
		}>;
	} | null = null;
	let capabilities: ThreadCapabilities | null = null;
	let acceptedModel: ThreadModel | null = null;
	let notificationHandler: ((notification: AnyNotification) => void) | null = null;
	let modelControlRecoveryHandler: ConversationModelControlRecoveryHandler | null = null;
	let notificationUnsub: (() => void) | null = null;
	type PendingProjection = {
		ref: string;
		instanceId: string | null;
		// Paging waits on the whole projected publish, not the raw RPC: the
		// response alone is not the state a page's cursor must be seated against.
		// The publish spans the raw response, the projection work and commit, and
		// the host's read fence, so paging that waited on the raw read could seat a
		// cursor against a projection the publish had not installed yet.
		publish: Promise<ConversationReadProjection>;
	};
	let pendingProjection: PendingProjection | null = null;

	// Monotonic service/open epoch. Every open/readProjection/close increments
	// it; an in-flight read captures its epoch and only installs ref+caps if
	// its epoch is still current when it resolves. This makes stale
	// completions (an older open resolving after a newer open, or a refresh
	// resolving after close/reopen) no-ops against the live pair.
	let openEpoch = 0;
	type ModelControlReadOutcome = { kind: "ready" } | { kind: "failed"; error: unknown };
	type ModelControlReadBinding = {
		ref: string;
		epoch: number;
		terminal: Promise<ModelControlReadOutcome>;
		settle(outcome: ModelControlReadOutcome): void;
		settled: boolean;
	};
	let modelControlReadBinding: ModelControlReadBinding | null = null;

	function settleModelControlRead(binding: ModelControlReadBinding | null, outcome: ModelControlReadOutcome): void {
		if (binding === null || binding.settled) return;
		binding.settled = true;
		binding.settle(outcome);
	}

	function retireModelControlRead(error: unknown): void {
		settleModelControlRead(modelControlReadBinding, { kind: "failed", error });
		modelControlReadBinding = null;
	}

	function installModelControlRead(threadRef: string, epoch: number): void {
		let settle: (outcome: ModelControlReadOutcome) => void = () => {};
		const terminal = new Promise<ModelControlReadOutcome>((resolve) => {
			settle = resolve;
		});
		modelControlReadBinding = { ref: threadRef, epoch, terminal, settle, settled: false };
	}

	function settleModelControlReadFromModel(epoch: number, model: ThreadModel): void {
		const binding = modelControlReadBinding;
		if (binding === null || binding.epoch !== epoch) return;
		settleModelControlRead(
			binding,
			model.modelControlState.kind === "ready"
				? { kind: "ready" }
				: { kind: "failed", error: new Error("ConversationService: read did not establish model-control authority") },
		);
	}

	function failModelControlRead(epoch: number, error: unknown): void {
		if (modelControlReadBinding?.epoch === epoch)
			settleModelControlRead(modelControlReadBinding, { kind: "failed", error });
	}

	function requireQueueInstance(expected: string): string {
		const threadRef = requireRef();
		if (nonemptyString(expected, "observed thread instance id") !== instanceId)
			throw new Error("The session instance changed. Try again.");
		return threadRef;
	}

	function beginOpen(threadRef: string): number {
		retireModelControlRead(new Error("ConversationService: model-control read was replaced"));
		opening = {
			ref: threadRef,
			threadId: ref === threadRef ? threadId : null,
			pushed: null,
			modelControl: [],
		};
		// Starting a new open invalidates the prior epoch and clears BOTH ref
		// and capabilities before the await, so the service is truly fail-closed
		// while the read is in flight: no mutation or requireRef-only operation
		// (setReasoningEffort, cancelQueued, loadOlder) can reach the wire until
		// the pair is installed together on success. The requested threadRef is
		// captured only in the epoch; it is NOT written to ref until success.
		openEpoch += 1;
		const epoch = openEpoch;
		installModelControlRead(threadRef, epoch);
		ref = null;
		modelScope = null;
		instanceId = null;
		threadId = null;
		capabilities = null;
		acceptedModel = null;
		return epoch;
	}

	function readWithPushedCapabilities(thread: Thread, epoch: number): Thread {
		const readCapabilities = extractCapabilities(thread.evener.capabilities);
		const pushed = opening?.pushed;
		if (openEpoch !== epoch || pushed?.threadId !== thread.id) return thread;
		const capabilities = { ...pushed.caps };
		if (readCapabilities.modelControlVersion === 1) capabilities.modelControlVersion = 1;
		else delete capabilities.modelControlVersion;
		return {
			...thread,
			evener: { ...thread.evener, capabilities },
		};
	}

	function sameAuthority(left: ModelControlAuthority, right: ModelControlAuthority): boolean {
		return sameModelControlAuthority(left, right);
	}

	function replayOpeningModelControl(
		model: ThreadModel,
		responseThreadId: string,
		epoch: number,
	): { model: ThreadModel; replacementReadRequired: boolean } {
		if (openEpoch !== epoch || opening === null) return { model, replacementReadRequired: false };
		const authority = modelControlAuthority(model);
		if (!authority) return { model, replacementReadRequired: false };
		let next = model;
		let replacementReadRequired = false;
		for (const buffered of opening.modelControl) {
			const params = buffered.notification.params;
			// A malformed frame must not discard the read it was buffered
			// alongside: no snapshot means no authority to compare, so it cannot
			// apply and the transcript this read produced is still valid.
			if (params.modelControl === null || params.modelControl === undefined) continue;
			if (
				params.threadId === responseThreadId &&
				params.ref === opening.ref &&
				sameAuthority(authority, params.modelControl.authority)
			) {
				const result = applyModelControlNotification(next, buffered.notification, buffered.receivedAt);
				next = result.model;
				replacementReadRequired ||= result.replacementReadRequired;
			}
		}
		return { model: next, replacementReadRequired };
	}

	function requireRef(): string {
		if (ref === null) throw new Error("ConversationService: no thread open");
		return ref;
	}

	function requireCap(cap: keyof ThreadCapabilities, action: string): void {
		if (capabilities === null || !capabilities[cap]) {
			throw new Error(`Action "${action}" is not available for this thread`);
		}
	}

	// Hand the same raw authoritative read back to the host's fence. A fence
	// failure is non-fatal to the conversation read: the read itself succeeded,
	// and a fence that cannot settle leaves the runtime's durable dispatch gate
	// blocked (the fail-safe direction) until a later read reconciles it. It must
	// never turn a good projection into a conversation error.
	async function reconcileRead(lease: ReadLease | undefined, response: ThreadReadResponse): Promise<void> {
		if (options.onReadComplete === undefined) return;
		try {
			await options.onReadComplete(lease, response);
		} catch (error) {
			console.error("ConversationService: read fence failed", error);
		}
	}

	async function waitForModelControl(): Promise<{
		threadRef: string;
		expectedThreadId: string;
		authority: ModelControlAuthority;
		epoch: number;
	}> {
		if (acceptedModel?.modelControlState.kind !== "ready") {
			const binding = modelControlReadBinding;
			if (binding === null || binding.epoch !== openEpoch) {
				throw new Error("ConversationService: model control is not established by a current read");
			}
			const outcome = await binding.terminal;
			if (modelControlReadBinding !== binding || binding.epoch !== openEpoch) {
				throw new Error("ConversationService: model-control read was superseded");
			}
			if (outcome.kind === "failed") throw outcome.error;
		}
		const threadRef = requireRef();
		requireCap("changeModel", "changeModel");
		const authority = acceptedModel === null ? undefined : modelControlAuthority(acceptedModel);
		if (!authority) throw new Error("ConversationService: model control is not established");
		return {
			threadRef,
			expectedThreadId: nonemptyString(threadId, "thread id"),
			authority,
			epoch: openEpoch,
		};
	}

	function modelControlFenceCurrent(
		threadRef: string,
		expectedThreadId: string,
		fence: ModelControlRequestFence,
	): boolean {
		const currentAuthority = acceptedModel === null ? undefined : modelControlAuthority(acceptedModel);
		return (
			openEpoch === fence.clientGeneration &&
			openEpoch === fence.readGeneration &&
			ref === threadRef &&
			threadId === expectedThreadId &&
			currentAuthority !== undefined &&
			sameAuthority(fence.authority, currentAuthority)
		);
	}

	// A superseded disposition is the daemon saying another pick won, so this
	// caller did not get the model it asked for and its operation fails whether
	// or not this request still owns publication. appwire's CodeConflict
	// (-32013): another client's newer pick won, so it is a conflict rather
	// than a failed operation.
	function modelSetSupersededError(
		clientMutationId: string,
		response: MethodTypes["thread/model/set"]["result"],
	): WireError {
		return new WireError("The model was already changed in another client.", -32013, {
			clientMutationId,
			disposition: response.disposition,
		});
	}

	let startReplacementRead: ((threadRef: string) => void) | null = null;

	// A cold session cannot carry a model change on the authority a previous
	// read reported: there is no daemon behind it, the hub advertises
	// changeModel so it can resume behind the mutation, and the first read
	// that can report the resumed daemon's authority is the one after the
	// resume. A held ready state over a cold status is the same case: a status
	// frame that turns a session cold leaves modelControlState untouched, so
	// its snapshot names a boot generation the resumed daemon replaced.
	function coldSession(): boolean {
		return acceptedModel !== null && isColdSessionStatus(acceptedModel.status.type);
	}

	// Resume, then take exactly one authoritative read: the same subscribed
	// read open() takes, so the resumed session's identity, capabilities and
	// model control install together and settle the read waitForModelControl
	// awaits. A read that establishes no supported authority leaves
	// changeModel to fail there, before any dispatch.
	//
	// The resume can hand back a different identity, and the read must address
	// the one it returned: open() on the ref the daemon just retired would
	// fence the mutation on the departed process's authority. The resumed
	// thread's ref is authoritative; only an empty one falls back to the ref
	// this service already holds.
	async function resumeThenAuthorize(): Promise<void> {
		requireCap("changeModel", "changeModel");
		const threadRef = requireRef();
		if (typeof client.resumeThread !== "function") {
			throw new Error("ConversationService: resume is unavailable");
		}
		const resumed = await client.resumeThread(threadRef);
		const resumedRef = resumed.thread.evener.ref;
		await service.open(resumedRef === "" ? threadRef : resumedRef);
	}

	function requestModelControlRecovery(
		threadRef: string,
		expectedThreadId: string,
		input: ConversationModelControlRecoveryInput,
	): void {
		if (modelControlRecoveryHandler !== null) {
			modelControlRecoveryHandler(threadRef, expectedThreadId, input);
			return;
		}
		startReplacementRead?.(threadRef);
	}

	function recoverModelControl(
		threadRef: string,
		expectedThreadId: string,
		fence: ModelControlRequestFence,
		input: Extract<ModelControlInput, { kind: "authorityStale" | "postDispatchFailure" }>,
	): void {
		if (!modelControlFenceCurrent(threadRef, expectedThreadId, fence) || acceptedModel === null) return;
		const reduced = reduceModelControl(acceptedModel, input);
		acceptedModel = reduced.model;
		if (reduced.replacementReadRequired) requestModelControlRecovery(threadRef, expectedThreadId, input);
	}

	const service: LiveConversationService &
		ConversationModelCatalog &
		ConversationGoalActions &
		ConversationForkActions &
		ConversationTurnForkActions &
		ConversationClearActions &
		ConversationRecoveryActions = {
		async open(threadRef, _cursor) {
			pendingProjection = null;
			// The compatibility cursor is intentionally ignored: open() must send
			// exactly the canonical unbounded subscribed open request. Bounded
			// live projection lives exclusively in readProjection; cursor paging
			// lives exclusively in thread/turns/list. beginOpen clears BOTH ref
			// and capabilities before the await so the service is fail-closed
			// during the read; the pair is installed together only on success.
			const expectedThreadId = threadId ?? undefined;
			const epoch = beginOpen(threadRef);
			const readLease = options.onReadStart?.(threadRef, expectedThreadId);
			try {
				const response: ThreadReadResponse = await subscribedRead(threadRef).read({ includeTurns: true });
				// Compute ALL response-derived projection work BEFORE committing the
				// pair — a throw here leaves ref+capabilities null/fail-closed. Only
				// commit the pair after projection succeeds and the epoch is still
				// current; a stale successful result returns without committing.
				const thread = readWithPushedCapabilities(response.thread, epoch);
				const readInstanceId = nonemptyString(
					response.thread.evener.instanceId ?? response.thread.id,
					"thread instance id",
				);
				const replayedModelControl = replayOpeningModelControl(
					{
						...hydrateThread({ ...response, thread }, threadRef, now()),
						instanceId: readInstanceId,
					},
					response.thread.id,
					epoch,
				);
				const hydrated = replayedModelControl.model;
				const conversation = projectConversation(hydrated, undefined, displayConfig());
				const caps = extractCapabilities(thread.evener.capabilities);
				const readModelScope = { harness: thread.source, cwd: thread.cwd };
				if (openEpoch === epoch) {
					modelScope = readModelScope;
					instanceId = readInstanceId;
					threadId = response.thread.id;
					ref = threadRef;
					capabilities = caps;
					acceptedModel = conversation;
					opening = null;
					settleModelControlReadFromModel(epoch, conversation);
				}
				// Reconcile the host's durable dispatch gate only for a read whose
				// projection actually installed: a failed projection or a stale epoch
				// leaves the gate blocked.
				if (openEpoch === epoch) await reconcileRead(readLease, response);
				return conversation;
			} catch (error) {
				failModelControlRead(epoch, error);
				throw error;
			}
		},

		async readProjection(threadRef, heldSnapshot) {
			const pagingInstance =
				ref === threadRef ? instanceId : pendingProjection?.ref === threadRef ? pendingProjection.instanceId : null;
			const expectedThreadId = threadId ?? undefined;
			const epoch = beginOpen(threadRef);
			const readLease = options.onReadStart?.(threadRef, expectedThreadId);
			const readOnce = (snapshot?: SnapshotIdentity) =>
				subscribedRead(threadRef).read({
					includeTurns: true,
					itemsView: "fragment",
					itemLimit: READ_ITEM_LIMIT,
					...(snapshot ? { heldSnapshot: snapshot } : {}),
				});
			// The publish is the whole projected read: the raw response, the
			// projection work and pair commit, and then the host's read fence. Paging
			// waits on it (see PendingProjection), so a page can never be seated
			// against a projection the publish has not installed.
			const publish = (async (): Promise<ConversationReadProjection> => {
				let response: ThreadReadResponse;
				let replacedHeldHistory = false;
				try {
					response = await readOnce(heldSnapshot);
				} catch (error) {
					// A held snapshot that predates the server's bounded update log
					// is refused with transcriptItemCursorStale. The refusal
					// indicts the held window, not the thread: without a
					// fallback, every refresh and reconnect re-derives the same
					// stale window from the retained history and fails the same
					// way, so the conversation cannot recover. Retry exactly once
					// with no held snapshot: the fresh window replaces the held
					// history through this same publish, and the draft lives in
					// the store, untouched here.
					if (heldSnapshot === undefined || !isStaleCursorError(error)) throw error;
					response = await readOnce(undefined);
					replacedHeldHistory = true;
				}
				// Compute ALL response-derived projection work BEFORE committing the
				// pair — a throw in hydration or projectConversation (or a
				// malformed response) leaves ref+capabilities null/fail-closed. Only
				// commit the pair after all projection succeeds and the epoch is still
				// current; a stale successful result returns without committing.
				const thread = readWithPushedCapabilities(response.thread, epoch);
				const readInstanceId = nonemptyString(
					response.thread.evener.instanceId ?? response.thread.id,
					"thread instance id",
				);
				const replayedModelControl = replayOpeningModelControl(
					{
						...hydrateThread({ ...response, thread }, threadRef, now()),
						instanceId: readInstanceId,
					},
					response.thread.id,
					epoch,
				);
				const hydrated = replayedModelControl.model;
				const conversation = projectConversation(hydrated, undefined, displayConfig());
				const olderCursor = response.olderCursor ?? null;
				const caps = extractCapabilities(thread.evener.capabilities);
				const readModelScope = { harness: thread.source, cwd: thread.cwd };
				if (openEpoch === epoch) {
					modelScope = readModelScope;
					instanceId = readInstanceId;
					threadId = response.thread.id;
					ref = threadRef;
					capabilities = caps;
					acceptedModel = conversation;
					opening = null;
					settleModelControlReadFromModel(epoch, conversation);
				}
				// Reconcile the host's durable dispatch gate only after the projection
				// installed, so a failed projection or a stale epoch leaves it blocked.
				if (openEpoch === epoch) await reconcileRead(readLease, response);
				return {
					conversation,
					changes: response.changes,
					olderCursor,
					...(replacedHeldHistory ? { replacedHeldHistory: true } : {}),
					...(replayedModelControl.replacementReadRequired ? { modelControlRecoveryRequired: true } : {}),
					hasEarlierItems: thread.turns?.some((turn) => turn.hasEarlierItems === true) ?? false,
					hasLaterItems: thread.turns?.some((turn) => turn.hasLaterItems === true) ?? false,
				};
			})().catch((error) => {
				failModelControlRead(epoch, error);
				throw error;
			});
			pendingProjection = { ref: threadRef, instanceId: pagingInstance, publish };
			return publish;
		},

		async loadOlder(cursor, before) {
			const pending = pendingProjection;
			if (ref === null && pending !== null && pending.instanceId !== null) {
				// A same-session refresh closes mutation gates while validating its
				// snapshot. Paging waits for validation while retaining its cursor.
				const expected = pending;
				let projection: PendingProjection = pending;
				for (;;) {
					// Wait for the projected publish, not the raw RPC: the host's read
					// fence and the pair commit both sit inside it, so a page is seated
					// only after the state it pages against is installed. A rejection
					// here is the read's own failure, surfaced unchanged.
					await projection.publish;
					if (pendingProjection === projection) break;
					const next: PendingProjection | null = pendingProjection;
					if (next === null || next.ref !== expected.ref || next.instanceId !== expected.instanceId) {
						throw new Error("ConversationService: thread changed while paging");
					}
					projection = next;
				}
				if (ref !== expected.ref || instanceId !== expected.instanceId) {
					throw new Error("ConversationService: thread changed while paging");
				}
			}
			const threadRef = requireRef();
			let response: ThreadTurnsListResponse;
			try {
				response = await client.request("thread/turns/list", {
					ref: threadRef,
					...(cursor !== null ? { cursor } : {}),
					...(before !== undefined ? { before } : {}),
					itemsView: "fragment",
					itemLimit: READ_ITEM_LIMIT,
				});
			} catch (error) {
				if (!isStaleCursorError(error)) throw error;
				// The store owns the single authoritative refresh so the refreshed
				// cursor and visible projection are published together.
				throw error;
			}
			return {
				turnsPage: response,
				nextCursor: response.nextCursor,
				hasEarlierItems: response.data.some((turn) => turn.hasEarlierItems === true),
				hasLaterItems: response.data.some((turn) => turn.hasLaterItems === true),
			};
		},

		subscribeNotifications(handler, recoveryHandler) {
			if (notificationUnsub !== null) {
				notificationUnsub();
			}
			notificationHandler = handler;
			modelControlRecoveryHandler = recoveryHandler ?? null;
			const unsubscribe = client.onNotification((notification) => {
				// Fold a committed notification into the model the mutation gates
				// read. Both branches below fold through this one path.
				const foldCommitted = (candidate: { ref: string; threadId: string }): void => {
					if (acceptedModel === null || ref === null) return;
					if (candidate.ref !== ref || candidate.threadId !== threadId) return;
					acceptedModel = applyNotification(acceptedModel, notification, now());
				};
				if (notification.method === "thread/model/controlChanged") {
					const params = notification.params;
					const committed = ref !== null && params.ref === ref && params.threadId === threadId;
					const pending =
						opening !== null &&
						params.ref === opening.ref &&
						(opening.threadId === null || params.threadId === opening.threadId);
					if (pending && opening !== null) {
						opening.modelControl.push({ notification, receivedAt: now() });
						return;
					}
					if (committed) foldCommitted(params);
				}
				if (notification.method === "thread/status/changed") {
					const params = notification.params;
					const committed = ref !== null && params.ref === ref && params.threadId === threadId;
					const pending =
						opening !== null &&
						params.ref === opening.ref &&
						(opening.threadId === null || params.threadId === opening.threadId);
					if ((committed || pending) && params.capabilities !== undefined) {
						// Preserve pushes during hydration without permitting mutations until its read succeeds.
						try {
							const next = extractCapabilities(params.capabilities);
							delete next.modelControlVersion;
							if (committed) {
								if (capabilities?.modelControlVersion === 1) next.modelControlVersion = 1;
								capabilities = next;
							}
							if (pending && opening) opening.pushed = { threadId: params.threadId, caps: next };
						} catch {
							return;
						}
					}
					// Fold the frame into the model the mutation gates read, so a
					// session that goes cold is visible to changeModel without a
					// read. The shared reducer deliberately leaves
					// modelControlState alone on a status transition (the snapshot
					// names the authority the departing daemon reported), which is
					// why coldSession() checks the status rather than the state.
					if (committed) foldCommitted(params);
				}
				handler(notification);
			});
			notificationUnsub = unsubscribe;
			return () => {
				if (notificationUnsub === unsubscribe) {
					unsubscribe();
					notificationUnsub = null;
					notificationHandler = null;
					modelControlRecoveryHandler = null;
				}
			};
		},

		async send(input) {
			requireCap("send", "send");
			const threadRef = requireRef();
			const expectedInstanceId = nonemptyString(instanceId, "thread instance id");
			const clientMutationId = idFactory();
			const result = await client.request("turn/start", {
				ref: threadRef,
				clientMutationId,
				expectedInstanceId,
				input,
			});
			return decodeMutationResult("send", result, clientMutationId, expectedInstanceId);
		},

		async queue(input) {
			requireCap("queue", "queue");
			const threadRef = requireRef();
			const expectedInstanceId = nonemptyString(instanceId, "thread instance id");
			const clientMutationId = idFactory();
			const result = await client.request("turn/queue", {
				ref: threadRef,
				clientMutationId,
				expectedInstanceId,
				input,
			});
			return decodeMutationResult("queue", result, clientMutationId, expectedInstanceId);
		},

		async interrupt() {
			requireCap("interrupt", "interrupt");
			const threadRef = requireRef();
			const expectedInstanceId = nonemptyString(instanceId, "thread instance id");
			const clientMutationId = idFactory();
			const result = await client.request("turn/interrupt", {
				ref: threadRef,
				clientMutationId,
				expectedInstanceId,
			});
			return decodeMutationResult("interrupt", result, clientMutationId, expectedInstanceId);
		},

		async compact() {
			requireCap("compact", "compact");
			const threadRef = requireRef();
			await client.request("thread/compact/start", { ref: threadRef });
		},

		async clear() {
			requireCap("clear", "clear");
			const targetRef = requireRef();
			const expectedInstanceId = nonemptyString(instanceId, "thread instance id");
			const clientMutationId = idFactory();
			const response = await client.request("thread/clear", {
				ref: targetRef,
				expectedInstanceId,
				clientMutationId,
			});
			const replacementInstance = nonemptyString(response.thread?.evener?.instanceId, "replacement instance id");
			const receipt = decodeMutationResult("clear", response, clientMutationId, replacementInstance);
			if (
				response.ref !== targetRef ||
				response.thread.evener.ref !== targetRef ||
				receipt.threadId !== response.thread.id ||
				receipt.instanceId !== replacementInstance
			)
				throw new Error("The clear response does not match the replacement session.");
			return response;
		},

		adoptClear(response) {
			if ((ref ?? opening?.ref) !== response.ref)
				throw new Error("The conversation changed before the clear could be displayed.");
			const thread = response.thread;
			beginOpen(response.ref);
			const replacementInstance = nonemptyString(thread.evener.instanceId, "replacement instance id");
			const hydrated = {
				...hydrateThread({ thread }, response.ref, now()),
				instanceId: replacementInstance,
			};
			const retired = reduceModelControl(hydrated, { kind: "connectionRetired" }).model;
			const conversation = projectConversation(retired, undefined, displayConfig());
			const caps = extractCapabilities(thread.evener.capabilities);
			modelScope = { harness: thread.source, cwd: thread.cwd };
			instanceId = replacementInstance;
			threadId = thread.id;
			ref = response.ref;
			capabilities = caps;
			acceptedModel = conversation;
			opening = null;
			settleModelControlRead(modelControlReadBinding, {
				kind: "failed",
				error: new Error("ConversationService: clear requires a replacement model-control read"),
			});
			return { conversation, olderCursor: null };
		},

		async forkFromTurn(transcriptEntryIndex) {
			if (!Number.isSafeInteger(transcriptEntryIndex) || transcriptEntryIndex <= 0)
				throw new Error("The selected message has no persisted fork position.");
			requireCap("forkFromTurn", "forkFromTurn");
			const parentRef = requireRef();
			const response = await client.request("thread/fork", {
				ref: parentRef,
				sourceItemKey: forkEntrySourceItemKey(transcriptEntryIndex),
				deferInput: true,
			});
			const childRef = nonemptyString(response.thread?.evener?.ref, "fork session reference");
			if (childRef === parentRef) throw new Error("The fork response did not identify a new session.");
			return response;
		},

		async forkAside() {
			requireCap("forkFromTurn", "forkAside");
			const parentRef = requireRef();
			const response = await client.request("thread/fork", {
				ref: parentRef,
				aside: true,
			});
			const childRef = nonemptyString(response.thread?.evener?.ref, "aside session reference");
			if (childRef === parentRef) throw new Error("The aside response did not identify a new session.");
			return response;
		},

		async shutdown() {
			requireCap("shutdown", "shutdown");
			const threadRef = requireRef();
			await client.request("thread/shutdown", { ref: threadRef });
		},

		async forceStop() {
			const threadRef = requireRef();
			if (typeof client.forceStop !== "function") {
				throw new Error("ConversationService: force stop is unavailable");
			}
			await client.forceStop(threadRef);
		},

		async resume() {
			const threadRef = requireRef();
			if (typeof client.resumeThread !== "function") {
				throw new Error("ConversationService: resume is unavailable");
			}
			await client.resumeThread(threadRef);
		},

		async models() {
			requireRef();
			if (!modelScope) throw new Error("No model catalog scope is available.");
			const epoch = openEpoch;
			const catalog = await client.request("model/list", { ...modelScope });
			if (epoch !== openEpoch) throw new Error("The conversation changed while loading models.");
			return catalog;
		},

		async changeModel(modelProvider, model) {
			// A pick on a cold session resumes behind the change, exactly as the
			// hub's pastThreadCapabilities advertise. The one read after the
			// resume establishes the authority the resumed daemon will accept;
			// without it the pick would carry a retired boot generation.
			if (coldSession()) await resumeThenAuthorize();
			const { threadRef, expectedThreadId, authority, epoch } = await waitForModelControl();
			const fence: ModelControlRequestFence = {
				authority,
				clientGeneration: epoch,
				readGeneration: epoch,
			};
			if (!modelControlFenceCurrent(threadRef, expectedThreadId, fence)) {
				throw new Error("ConversationService: model-control authority was superseded before dispatch");
			}
			const clientMutationId = idFactory();
			let rawResponse: unknown;
			try {
				rawResponse = await client.request("thread/model/set", {
					ref: threadRef,
					modelProvider,
					model,
					clientMutationId,
					expectedInstanceId: authority.instanceId,
					expectedBootGeneration: authority.bootGeneration,
				});
			} catch (error) {
				const recovery = modelControlRecoveryInput(error);
				if (recovery) recoverModelControl(threadRef, expectedThreadId, fence, recovery);
				throw error;
			}
			let response: MethodTypes["thread/model/set"]["result"];
			try {
				response = validateThreadModelSetResponse(rawResponse, clientMutationId);
			} catch (error) {
				recoverModelControl(threadRef, expectedThreadId, fence, { kind: "postDispatchFailure" });
				throw error;
			}
			// The fence decides who may publish the snapshot, not what this
			// caller is told. Resolving here let a superseded pick look like a
			// success whenever a reread had advanced the generation while the
			// mutation was in flight, so the sheet confirmed the losing model.
			if (!modelControlFenceCurrent(threadRef, expectedThreadId, fence)) {
				if (response.disposition === "superseded") throw modelSetSupersededError(clientMutationId, response);
				return;
			}
			const snapshot = response.modelControl;
			if (!snapshot || !sameAuthority(fence.authority, snapshot.authority)) {
				recoverModelControl(threadRef, expectedThreadId, fence, { kind: "postDispatchFailure" });
				validateThreadModelSetResponse(undefined, clientMutationId);
				return;
			}
			if (acceptedModel === null) return;
			const reduced = reduceModelControl(acceptedModel, {
				kind: "snapshot",
				source: "response",
				snapshot,
			});
			acceptedModel = reduced.model;
			if (reduced.replacementReadRequired)
				requestModelControlRecovery(threadRef, expectedThreadId, {
					kind: "snapshot",
					source: "response",
					snapshot,
				});
			notificationHandler?.({
				method: "thread/model/controlChanged",
				params: {
					threadId: expectedThreadId,
					ref: threadRef,
					modelControl: snapshot,
				},
			});
			// The daemon answered with the pick that won, not this one: it
			// reconciled the canonical snapshot above (the client converges on the
			// winner's model), but this mutation did not change the model, so the
			// caller must not confirm it as if it had. The disposition is the
			// wire's own answer (appwire.ModelControlDispositionSuperseded), never
			// a guess from the snapshot.
			if (response.disposition === "superseded") {
				throw modelSetSupersededError(clientMutationId, response);
			}
		},

		async setVisionModel(visionModel) {
			requireCap("changeVisionModel", "setVisionModel");
			const threadRef = requireRef();
			await client.request("thread/vision-model/set", {
				ref: threadRef,
				visionModel,
			});
		},

		async setReasoningEffort(effort) {
			const threadRef = requireRef();
			await client.request("thread/reasoning-effort/set", {
				ref: threadRef,
				reasoningEffort: effort,
			});
		},

		async setGoal(objective) {
			requireCap("goal", "setGoal");
			const threadRef = requireRef();
			const result = await client.request("goal/set", { ref: threadRef, objective });
			const response = exactObject(result, ["started"], "goal result");
			if (typeof response.started !== "boolean") {
				throw new Error("ConversationService: invalid goal acknowledgment");
			}
		},

		async rename(name) {
			requireCap("rename", "rename");
			const threadRef = requireRef();
			await client.request("evener/thread/name/set", { ref: threadRef, name });
		},

		async cancelQueued(index, expectedEntryId, expectedInstanceId) {
			const threadRef = requireQueueInstance(expectedInstanceId);
			const expectedThreadId = nonemptyString(threadId, "thread id");
			const clientMutationId = idFactory();
			const result = await client.request("turn/cancelQueued", {
				ref: threadRef,
				index,
				clientMutationId,
				expectedEntryId,
				expectedInstanceId,
			});
			validateCancelQueued(result, clientMutationId, expectedInstanceId, expectedThreadId, expectedEntryId);
			return result;
		},

		close() {
			subscription?.lease.release();
			subscription = null;
			pendingProjection = null;
			if (notificationUnsub !== null) {
				notificationUnsub();
				notificationUnsub = null;
			}
			// Increment the epoch and clear the pair so a refresh that was in
			// flight before close cannot republish into the closed service.
			retireModelControlRead(new Error("ConversationService: model-control authority was closed"));
			openEpoch += 1;
			opening = null;
			ref = null;
			modelScope = null;
			instanceId = null;
			threadId = null;
			capabilities = null;
			acceptedModel = null;
			notificationHandler = null;
			modelControlRecoveryHandler = null;
		},
	};
	startReplacementRead = (threadRef) => {
		void service.readProjection(threadRef).catch(() => {});
	};
	return service;
}
