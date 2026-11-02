// ConversationStore (Zustand) tests with a fake ConversationService.
// Covers generation safety, notification routing, draft preservation,
// conflict restore, and no auto-retry.

import type {
	AnyNotification,
	EvenerThread,
	HistoryUpdatedParams,
	InputItem,
	MutationReceipt,
	Thread,
	ThreadCapabilities,
	ThreadItem,
	ThreadItemPosition,
	ThreadStatus,
	ThreadStatusChangedParams,
	ThreadTurnsListResponse,
	Turn,
	WarningParams,
} from "@evener/appwire-client";
import { hydrateThread, WireError } from "@evener/appwire-client";
import type { MobileConversation, MobileTimelineItem } from "../../projectedRows";
import { projectConversation } from "../../projectedRows";
import type { ConversationReadProjection, LiveConversationService } from "../services/conversation";
import { createConversationStore } from "./conversation";
export const ALL_TRUE_CAPS: ThreadCapabilities = {
	send: true,
	steer: true,
	interrupt: true,
	compact: true,
	clear: true,
	forkFromTurn: true,
	shutdown: true,
	changeModel: true,
	changeVisionModel: true,
	sharedNotes: false,
	queue: true,
	goal: true,
	rename: true,
	// The hub pages the thread from a before position, so the cap may trim.
	pageBefore: true,
};

export const refusal = () =>
	new WireError("action unavailable", -32000, {
		evenerErrorInfo: "actionUnavailable",
	}) as Error;

export class UnsupportedRowShapeError extends Error {}

export const V6_HISTORY_BASELINE = {
	bootGeneration: "1",
	epoch: 1,
	snapshot: { incarnation: "inc-1", length: 0 },
} as const;

export function makeConversation(over: Partial<MobileConversation> = {}, versioned = false): MobileConversation {
	// Hydrated through the package from a minimal wire Thread, so the fixture
	// tracks hydrateThread's defaults instead of restating every model field.
	const thread: Thread = {
		id: "thread-1",
		sessionId: "session-1",
		preview: "hello",
		ephemeral: false,
		modelProvider: "anthropic",
		createdAt: 0,
		updatedAt: 0,
		// The wire's vocabulary. Idle is the shape the hub publishes when it
		// advertises Send (Send folds !active at the source); the steering
		// submissions name a running turn themselves.
		status: { type: "idle" },
		cwd: "",
		cliVersion: "",
		source: "",
		turns: [],
		evener: {
			ref: "ref-1",
			capabilities: ALL_TRUE_CAPS,
			queue: { revision: 0, depth: 0, preview: [] },
		},
	};
	const base = projectConversation(
		hydrateThread(versioned ? { thread, ...V6_HISTORY_BASELINE } : { thread }, "ref-1", 0),
	);
	const { items, turns, ...rest } = over;
	if (turns !== undefined) {
		// A turns override is the model itself: the rows project from it, so
		// the fixture stays consistent through every re-projection. Versioned:
		// a live history/updated frame merges against model.history.turns, not
		// model.turns directly (withDisplay derives the latter from the
		// former) — an override that set only model.turns would merge the next
		// frame against the ORIGINAL (turns-less) history, silently discarding
		// the fixture's turns the moment any live frame arrived. Seeding
		// history.turns to match keeps the two in sync from the start.
		const withHistory = base.history === undefined ? base : { ...base, history: { ...base.history, turns } };
		return { ...projectConversation({ ...withHistory, turns }), ...rest };
	}
	if (items === undefined) {
		return {
			...base,
			...rest,
		};
	}
	try {
		// D23d: every publish re-projects the rows from the model, so rows a
		// fixture scripts over the conversation must be model-backed — build
		// the wire turns that project to them.
		const model = hydrateThread(
			versioned
				? {
						thread: { ...thread, turns: wirePageFromRows(items).data },
						...V6_HISTORY_BASELINE,
					}
				: { thread: { ...thread, turns: wirePageFromRows(items).data } },
			"ref-1",
			0,
		);
		return { ...projectConversation(model), ...rest };
	} catch (err) {
		// RoboRev round 2 (panel Low): catch only the genuinely unsupported row
		// kinds — anything else (a malformed supported fixture, a hydration or
		// projection failure) rethrows so an unintended conversion break fails
		// the test instead of degrading it to the literal rows.
		if (!(err instanceof UnsupportedRowShapeError)) throw err;
		// Rows no wire shape expresses (a notice's family, a bare failure row)
		// stay literal: their tests read the opened rows without re-projecting,
		// and open()'s display bound applies to whatever rows it is handed.
		return { ...base, items, ...rest };
	}
}

export function makeReceipt(over: Partial<MutationReceipt> = {}): MutationReceipt {
	return {
		clientMutationId: "cmid-1",
		disposition: "applied",
		threadId: "thread-1",
		turnId: "turn-1",
		projectionState: "pending",
		...over,
	};
}

export function textInput(text: string): InputItem[] {
	return [{ type: "text", text }];
}

export function wireTurn(id: string, inputTokens: number, outputTokens: number): Turn {
	return { id, itemsView: "fragment", status: "completed", usage: { inputTokens, outputTokens } };
}

export function wireTurnFragment(id: string, items: NonNullable<Turn["items"]>, usage?: Turn["usage"]): Turn {
	return {
		id,
		itemsView: "fragment",
		status: "completed",
		...(usage === undefined ? {} : { usage }),
		items,
	};
}

export function turnsPage(data: Turn[], nextCursor?: string): ThreadTurnsListResponse {
	return { data, nextCursor };
}

export function historyUpdated(params: Partial<HistoryUpdatedParams>): AnyNotification {
	return {
		method: "history/updated",
		params: {
			threadId: "thread-1",
			ref: "ref-1",
			bootGeneration: "1",
			epoch: 1,
			snapshot: { incarnation: "inc-1", length: 1 },
			...params,
		},
	} as AnyNotification;
}

export function statusChanged(status: ThreadStatus, over: Partial<ThreadStatusChangedParams> = {}): AnyNotification {
	return {
		method: "thread/status/changed",
		params: { threadId: "thread-1", ref: "ref-1", status, ...over },
	} as AnyNotification;
}

export function resyncFrame(threadId = "thread-1", ref = "ref-1"): AnyNotification {
	return { method: "evener/thread/resync", params: { threadId, ref } } as AnyNotification;
}

export function warningFrame(params: Partial<WarningParams>): AnyNotification {
	return {
		method: "warning",
		params: { threadId: "thread-1", ref: "ref-1", ...params },
	} as AnyNotification;
}

export function wirePageFromRows(rows: MobileConversation["items"]): ThreadTurnsListResponse {
	// An attachments row names its source by row id (the "<source>:attachments"
	// spelling the projector emits) or by sourceTranscriptKey. Its entries
	// convert back to the wire field the source's kind reads — input images
	// (inline data: URLs only) for user rows, output images (url-addressed)
	// for tool rows. Anything richer names a wire shape this helper refuses, so
	// the test scripts the wire page itself.
	const attachmentsBySource = new Map<string, Array<{ src: string; name?: string }>>();
	const consumedSources = new Set<string>();
	for (const row of rows) {
		if (row.kind !== "attachments") continue;
		const sourceKey = row.sourceTranscriptKey ?? row.id.replace(/:attachments$/, "");
		const existing = attachmentsBySource.get(sourceKey);
		attachmentsBySource.set(sourceKey, [
			...(existing ?? []),
			...row.items.map((attachment) => ({
				src: attachment.src,
				...(attachment.name !== undefined ? { name: attachment.name } : {}),
			})),
		]);
	}
	const data: Turn[] = [];
	const unsupportedKinds: string[] = [];
	for (const row of rows) {
		if (row.kind === "attachments") continue;
		const identity = {
			...(row.transcriptKey !== undefined ? { transcriptKey: row.transcriptKey } : {}),
			...(row.position !== undefined ? { position: row.position } : {}),
		};
		const attachments = attachmentsBySource.get(row.transcriptKey ?? row.id);
		if (attachments !== undefined) {
			consumedSources.add(row.transcriptKey ?? row.id);
		}
		if (row.kind === "activity" && row.state === "failed" && row.detail.error === undefined) {
			throw new Error("wirePageFromRows: a failed activity row needs detail.error to convert");
		}
		if (row.kind === "user") {
			const images: NonNullable<ThreadItem["images"]> = [];
			for (const attachment of attachments ?? []) {
				const match = /^data:([^;,]+);base64,(.*)$/.exec(attachment.src);
				if (match === null) {
					throw new Error(
						`wirePageFromRows: script the wire page for a non-inline input attachment (${attachment.src})`,
					);
				}
				images.push({
					type: "image",
					mediaType: match[1] ?? "image/png",
					data: match[2] ?? "",
					...(attachment.name !== undefined ? { name: attachment.name } : {}),
				});
			}
			data.push({
				id: `page-${row.id}`,
				itemsView: "fragment",
				status: "completed",
				items: [
					{
						id: row.id,
						type: "userMessage",
						text: row.text,
						status: "completed",
						...(images.length > 0 ? { images } : {}),
						...identity,
					} as ThreadItem,
				],
			});
			continue;
		}
		if (row.kind === "assistant") {
			const images: NonNullable<ThreadItem["images"]> = [];
			for (const attachment of attachments ?? []) {
				const match = /^data:([^;,]+);base64,(.*)$/.exec(attachment.src);
				if (match === null) {
					throw new Error(
						`wirePageFromRows: script the wire page for a non-inline input attachment (${attachment.src})`,
					);
				}
				images.push({
					type: "image",
					mediaType: match[1] ?? "image/png",
					data: match[2] ?? "",
					...(attachment.name !== undefined ? { name: attachment.name } : {}),
				});
			}
			data.push({
				id: `page-${row.id}`,
				itemsView: "fragment",
				status: "completed",
				items: [
					{
						id: row.id,
						type: "agentMessage",
						text: row.markdown,
						status: row.streaming ? "inProgress" : "completed",
						...(images.length > 0 ? { images } : {}),
						...identity,
					} as ThreadItem,
				],
			});
			continue;
		}
		if (row.kind === "activity" && row.members !== undefined) {
			// A clustered activity row is, on the wire, one turn's consecutive tool
			// items — the projection clusters what the wire served beside. The
			// row's own key and position name its FIRST member (the row id is that
			// member's); later members carry only what they carry themselves.
			data.push({
				id: `page-${row.id}`,
				itemsView: "fragment",
				status: "completed",
				items: row.members.map(
					(member, index) =>
						({
							id: member.id,
							type: "commandExecution",
							toolName: member.label,
							status: member.state === "running" ? "inProgress" : "completed",
							...(member.detail.output !== undefined ? { output: member.detail.output } : {}),
							...(member.detail.description !== undefined ? { description: member.detail.description } : {}),
							...(member.detail.error !== undefined ? { error: member.detail.error } : {}),
							...(member.detail.arguments !== undefined ? { argumentsJson: member.detail.arguments } : {}),
							...(member.detail.callId !== undefined ? { callId: member.detail.callId } : {}),
							...(member.detail.exitCode !== undefined ? { exitCode: member.detail.exitCode } : {}),
							...(attachments !== undefined && index === 0
								? {
										outputImages: attachments.map((attachment) => ({
											url: attachment.src,
											...(attachment.name !== undefined ? { name: attachment.name } : {}),
										})),
									}
								: {}),
							...(member.transcriptKey !== undefined || (index === 0 && row.transcriptKey !== undefined)
								? { transcriptKey: member.transcriptKey ?? row.transcriptKey }
								: {}),
							...(member.position !== undefined || row.position !== undefined
								? { position: member.position ?? row.position }
								: {}),
						}) as ThreadItem,
				),
			});
			continue;
		}
		if (row.kind === "activity" && row.members === undefined) {
			data.push({
				id: `page-${row.id}`,
				itemsView: "fragment",
				status: "completed",
				items: [
					{
						id: row.id,
						type: "commandExecution",
						toolName: row.label,
						status: row.state === "running" ? "inProgress" : "completed",
						...(row.detail.output !== undefined ? { output: row.detail.output } : {}),
						...(row.detail.description !== undefined ? { description: row.detail.description } : {}),
						...(row.detail.error !== undefined ? { error: row.detail.error } : {}),
						...(row.detail.arguments !== undefined ? { argumentsJson: row.detail.arguments } : {}),
						...(row.detail.callId !== undefined ? { callId: row.detail.callId } : {}),
						...(row.detail.exitCode !== undefined ? { exitCode: row.detail.exitCode } : {}),
						...(attachments !== undefined
							? {
									outputImages: attachments.map((attachment) => ({
										url: attachment.src,
										...(attachment.name !== undefined ? { name: attachment.name } : {}),
									})),
								}
							: {}),
						...identity,
					} as ThreadItem,
				],
			});
			continue;
		}
		// RoboRev round 5 (panel Low): defer the unsupported-row signal
		// until every row has validated — a malformed SUPPORTED row after an
		// unsupported one must fail loudly, not ride the literal fallback the
		// refusal would trigger.
		unsupportedKinds.push(row.kind);
	}
	for (const source of attachmentsBySource.keys()) {
		if (!consumedSources.has(source)) {
			throw new Error(`wirePageFromRows: an attachments row names no source row in the same fixture (${source})`);
		}
	}
	if (unsupportedKinds.length > 0) {
		throw new UnsupportedRowShapeError(
			`wirePageFromRows: script the wire page for a ${unsupportedKinds[0]} row fixture`,
		);
	}
	return { data, nextCursor: undefined };
}

export class FakeConversationService implements LiveConversationService {
	ref: string | null = null;
	openConv: MobileConversation = makeConversation();
	olderCursor: string | null = null;
	// RoboRev round 4 (panel M3): the two scripting seams are MUTUALLY
	// EXCLUSIVE — the wire page the store's paging seams read, or the legacy
	// row fixture the fake converts into one. A script that set both used to
	// discard the rows silently, so the type keeps the pair disjoint and
	// loadOlder throws on the dynamically-built scripts the type cannot see.
	olderItems:
		| {
				turnsPage: ThreadTurnsListResponse;
				items?: undefined;
				nextCursor?: string;
				hasEarlierItems?: boolean;
				hasLaterItems?: boolean;
		  }
		| {
				turnsPage?: undefined;
				items: MobileConversation["items"];
				nextCursor?: string;
				hasEarlierItems?: boolean;
				hasLaterItems?: boolean;
		  } = {
		turnsPage: { data: [], nextCursor: undefined },
	};
	receipt: MutationReceipt = makeReceipt();
	sendShouldReject: Error | null = null;
	sendCallCount = 0;
	queueCallCount = 0;
	interruptCallCount = 0;
	cancelQueuedResult: {
		removedText: string;
		removedImages?: number;
		receipt: MutationReceipt;
	} = {
		removedText: "text",
		receipt: makeReceipt(),
	};
	notificationHandler: ((n: AnyNotification) => void) | null = null;
	closed = false;
	// readProjection support
	readProjectionResult: {
		conversation: MobileConversation;
		olderCursor: string | null;
	} | null = null;
	readProjectionBlock: Promise<ConversationReadProjection> | null = null;
	readProjectionCalls: { ref: string }[] = [];

	// Like the real service, the conversation returned is hydrated under the
	// ref that was opened: the store routes notifications by the model's ref.
	private hydratedUnder(ref: string, conversation: MobileConversation): MobileConversation {
		return { ...conversation, ref };
	}
	async open(ref: string, _cursor?: string): Promise<MobileConversation> {
		this.ref = ref;
		return this.hydratedUnder(ref, this.openConv);
	}
	async readProjection(ref: string): Promise<ConversationReadProjection> {
		this.readProjectionCalls.push({ ref });
		if (this.readProjectionBlock) return this.readProjectionBlock;
		if (this.readProjectionResult) {
			// The real service's hydrateThread stamps ThreadModel.olderCursor from
			// the SAME response field this wrapper's own olderCursor carries
			// (threadFields, reducer.ts) — mirror that here, or a scripted fixture
			// whose conversation was built independently of this wrapper's cursor
			// (the common case: makeConversation never sees it) hands
			// applyReadModel a `fresh.olderCursor` the real service could never
			// produce.
			return {
				...this.readProjectionResult,
				conversation: {
					...this.hydratedUnder(ref, this.readProjectionResult.conversation),
					olderCursor: this.readProjectionResult.olderCursor ?? undefined,
				},
			};
		}
		return {
			conversation: {
				...this.hydratedUnder(ref, this.openConv),
				olderCursor: this.olderCursor ?? undefined,
			},
			olderCursor: this.olderCursor,
		};
	}
	// The requests loadOlder made: its cursor and before.
	olderRequests: { cursor: string | null; before?: ThreadItemPosition }[] = [];
	async loadOlder(
		cursor: string | null,
		before?: ThreadItemPosition,
	): Promise<{
		turnsPage: ThreadTurnsListResponse;
		nextCursor?: string;
		hasEarlierItems?: boolean;
		hasLaterItems?: boolean;
	}> {
		this.olderRequests.push({ cursor, ...(before !== undefined ? { before } : {}) });
		// Support hanging for stale-safety tests: if olderItems is a Promise,
		// await it so it resolves when the test wants.
		if (this.olderItems instanceof Promise) {
			return this.olderItems;
		}
		const { items, turnsPage, ...rest } = this.olderItems;
		if (items !== undefined && turnsPage !== undefined) {
			// RoboRev round 4 (panel M3): the seams are mutually exclusive —
			// silently discarding one of them is the same green-by-degradation
			// class the fixture conversion stopped swallowing.
			throw new Error("FakeConversationService.loadOlder: script either items or turnsPage, not both");
		}
		if (turnsPage !== undefined) return { ...rest, turnsPage };
		if (items === undefined) {
			throw new Error("FakeConversationService: no page scripted");
		}
		// The legacy row-fixture seam: one wire page synthesized from the
		// scripted rows, each row an item of its own turn (a failure row names
		// its turn under the failure: prefix, an attachments row re-attaches to
		// the source row before it), so the store's real merge and projection
		// produce the rows the fixture described.
		return { ...rest, turnsPage: wirePageFromRows(items) };
	}
	subscribeNotifications(handler: (n: AnyNotification) => void): () => void {
		this.notificationHandler = handler;
		return () => {
			this.notificationHandler = null;
		};
	}
	async send(_input: InputItem[]): Promise<MutationReceipt> {
		this.sendCallCount += 1;
		if (this.sendShouldReject) throw this.sendShouldReject;
		return this.receipt;
	}
	async queue(_input: InputItem[]): Promise<MutationReceipt> {
		this.queueCallCount += 1;
		return this.receipt;
	}
	async interrupt(): Promise<MutationReceipt> {
		this.interruptCallCount += 1;
		return this.receipt;
	}
	async compact(): Promise<void> {}
	async shutdown(): Promise<void> {}
	async changeModel(_p: string, _m: string): Promise<void> {}
	async setReasoningEffort(_e: string): Promise<void> {}
	async setVisionModel(_v: string): Promise<void> {}
	async rename(_n: string): Promise<void> {}
	async cancelQueued(
		_i: number,
		_e: string,
	): Promise<{
		removedText: string;
		removedImages?: number;
		receipt: MutationReceipt;
	}> {
		return this.cancelQueuedResult;
	}
	close(): void {
		this.closed = true;
		this.notificationHandler = null;
	}
}

export async function openedStore(
	conversation: MobileConversation = makeConversation(),
	ref = "ref-1",
): Promise<{
	service: FakeConversationService;
	store: ReturnType<typeof createConversationStore>;
}> {
	const service = new FakeConversationService();
	service.openConv = conversation;
	const store = createConversationStore();
	await store.getState().open(service, ref);
	return { service, store };
}

export function readServing(conversation: MobileConversation, olderCursor: string | null): ConversationReadProjection {
	return { conversation, olderCursor };
}

export async function loadFillerPage(
	store: ReturnType<typeof createConversationStore>,
	service: FakeConversationService,
): Promise<void> {
	store.setState({ olderCursor: "cursor-1" });
	service.olderItems = {
		turnsPage: turnsPage([wireTurn("t0", 500, 20)], undefined),
		nextCursor: undefined,
	};
	await store.getState().loadOlder(service);
}

export function makeControlledRead(service: FakeConversationService): {
	started: (target?: number) => Promise<void>;
	ready: (target?: number) => Promise<void>;
	release: () => boolean;
	completed: (target?: number) => Promise<void>;
	getStartedCount: () => number;
	getDoneCount: () => number;
} {
	let startedCount = 0;
	let readyCount = 0;
	let doneCount = 0;
	const releaseQueue: Array<() => void> = [];
	// Level-triggered waiters: each carries a target count. Resolved when
	// the counter reaches or exceeds the target. If the target is already
	// met when started()/completed() is called, resolve immediately.
	const startedWaiters: Array<{ target: number; resolve: () => void }> = [];
	const readyWaiters: Array<{ target: number; resolve: () => void }> = [];
	const doneWaiters: Array<{ target: number; resolve: () => void }> = [];
	const orig = service.readProjection.bind(service);
	service.readProjection = async (ref: string) => {
		startedCount += 1;
		// Resolve all started waiters whose target has been reached.
		for (let i = startedWaiters.length - 1; i >= 0; i--) {
			const w = startedWaiters[i];
			if (w !== undefined && startedCount >= w.target) {
				w.resolve();
				startedWaiters.splice(i, 1);
			}
		}
		const result = await orig(ref);
		await new Promise<void>((resolve) => {
			releaseQueue.push(resolve);
			readyCount += 1;
			for (let i = readyWaiters.length - 1; i >= 0; i--) {
				const w = readyWaiters[i];
				if (w !== undefined && readyCount >= w.target) {
					w.resolve();
					readyWaiters.splice(i, 1);
				}
			}
		});
		doneCount += 1;
		// Resolve all done waiters whose target has been reached.
		for (let i = doneWaiters.length - 1; i >= 0; i--) {
			const w = doneWaiters[i];
			if (w !== undefined && doneCount >= w.target) {
				w.resolve();
				doneWaiters.splice(i, 1);
			}
		}
		return result;
	};
	return {
		started: (target = 1) => {
			if (startedCount >= target) return Promise.resolve();
			return new Promise<void>((resolve) => {
				startedWaiters.push({ target, resolve });
			});
		},
		ready: (target = 1) => {
			if (readyCount >= target) return Promise.resolve();
			return new Promise<void>((resolve) => {
				readyWaiters.push({ target, resolve });
			});
		},
		release: () => {
			const r = releaseQueue.shift();
			if (r !== undefined) {
				r();
				return true;
			}
			return false;
		},
		completed: (target = 1) => {
			if (doneCount >= target) return Promise.resolve();
			return new Promise<void>((resolve) => {
				doneWaiters.push({ target, resolve });
			});
		},
		getStartedCount: () => startedCount,
		getDoneCount: () => doneCount,
	};
}

export async function yieldMicrotask(): Promise<void> {
	await Promise.resolve();
}

export function makeThread(over: Partial<Thread> = {}): Thread {
	return {
		id: "thread-1",
		sessionId: "session-1",
		preview: "hello",
		ephemeral: false,
		modelProvider: "anthropic",
		createdAt: 0,
		updatedAt: 0,
		status: { type: "ready" },
		cwd: "",
		cliVersion: "",
		source: "",
		evener: {
			ref: "ref-1",
			capabilities: { ...ALL_TRUE_CAPS },
			queue: { revision: 0 },
		},
		...over,
	};
}

export function makeTurn(over: Partial<Turn> = {}): Turn {
	return {
		id: "t1",
		items: [],
		itemsView: "default",
		status: "completed",
		...over,
	};
}

export function askUserItem(id: string, argsJson: string, status = "completed"): ThreadItem {
	return {
		type: "commandExecution",
		id,
		toolName: "ask_user",
		status,
		argumentsJson: argsJson,
	};
}

export function userMessageItem(id: string, text: string): ThreadItem {
	return { type: "userMessage", id, text };
}

export function agentMessageItem(id: string, text: string | undefined, status = "completed"): ThreadItem {
	return { type: "agentMessage", id, status, ...(text === undefined ? {} : { text }) };
}

export function reasoningItem(id: string, text: string, status = "completed"): ThreadItem {
	return { type: "reasoning", id, text, status };
}

export function commandExecItem(id: string, toolName: string, status = "completed"): ThreadItem {
	return { type: "commandExecution", id, toolName, status };
}

export const VALID_ASK_ARGS =
	'{"questions":[{"header":"Choose","question":"Pick one","options":[{"label":"A","detail":"da"},{"label":"B","detail":"db"}],"multi_select":false}]}';

export function makeReadProjectionResult(
	thread: Thread,
	capabilities: ThreadCapabilities = ALL_TRUE_CAPS,
	versioned = false,
): {
	conversation: MobileConversation;
	olderCursor: string | null;
} {
	return {
		conversation: projectConversation(
			hydrateThread(versioned ? { thread, ...V6_HISTORY_BASELINE } : { thread }, thread.evener.ref, 0),
		),
		olderCursor: null,
	};
}

export async function openProjectedThreadWith(
	thread: Thread,
	versioned = false,
): Promise<{
	store: ReturnType<typeof createConversationStore>;
	service: FakeConversationService;
}> {
	const service = new FakeConversationService();
	service.readProjectionResult = makeReadProjectionResult(thread, ALL_TRUE_CAPS, versioned);
	const store = createConversationStore();
	await store.getState().openProjected(service, "ref-1");
	return { store, service };
}

export async function openProjectedThread(thread: Thread, versioned = false) {
	const { store } = await openProjectedThreadWith(thread, versioned);
	return store;
}

export function runningTurnThread(items: ThreadItem[] = [], over: Partial<Thread> = {}): Thread {
	return makeThread({
		turns: [makeTurn({ id: "t1", status: "inProgress", items })],
		evener: evenerWith({ activeTurnId: "t1" }),
		...over,
	});
}

export async function openRunningTurn(
	items: ThreadItem[] = [],
	over: Partial<Thread> = {},
	versioned = false,
): Promise<{
	store: ReturnType<typeof createConversationStore>;
	service: FakeConversationService;
	thread: Thread;
}> {
	const thread = runningTurnThread(items, over);
	const service = new FakeConversationService();
	service.readProjectionResult = makeReadProjectionResult(thread, ALL_TRUE_CAPS, versioned);
	const store = createConversationStore();
	await store.getState().openProjected(service, "ref-1");
	return { store, service, thread };
}

export function rows(store: ReturnType<typeof createConversationStore>): MobileTimelineItem[] {
	return store.getState().conversation?.items ?? [];
}

export function rowById(store: ReturnType<typeof createConversationStore>, id: string): MobileTimelineItem | undefined {
	return rows(store).find((item) => item.id === id);
}

export const target = { threadId: "thread-1", ref: "ref-1" };

export const evenerWith = (over: Partial<EvenerThread>): Thread["evener"] => ({
	ref: "ref-1",
	capabilities: ALL_TRUE_CAPS,
	queue: { revision: 0 },
	...over,
});
