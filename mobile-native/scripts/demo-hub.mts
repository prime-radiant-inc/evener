// Explicitly launched network fixture for native UI checks; no Evener or LLM runs.
import { once } from "node:events";
import { createServer } from "node:http";
import { readFileSync } from "node:fs";
import { createInterface } from "node:readline";
import { pathToFileURL } from "node:url";
import { WebSocket, WebSocketServer } from "ws";
import { WireError } from "@evener/appwire-client";
import type {
	InitializeResponse,
	InputItem,
	ModelListResponse,
	MutationReceipt,
	NavigationInvalidatedPayload,
	NotesHumanSetParams,
	Thread,
	ThreadItem,
	Turn,
	TurnStartParams,
	UrlsRemoveParams,
} from "@evener/appwire-client";
import {
	ASKING_SESSION_ID,
	createDemoFleet,
	type DemoFleetOptions,
	type DemoStep,
	fleetSessionRef,
	ROW_STEPS,
	fleetSessions,
} from "../src/dev/demoFleet.js";
import { createDemoDocuments, SETTLE_RACE_PLAN, SETTLE_RACE_PLAN_REVISED } from "../src/dev/demoSubagents.js";
import {
	askWorkingSessionQuestion,
	createDemoSessions,
	endTurn,
	queuePreview,
	refreshCapabilities,
	removeLink,
	resolveEscalation,
	restFleetSession,
	setHumanNote,
	stageFleetState,
	startFleetTurn,
} from "../src/dev/demoSessions.js";
import { latestPage, pageBefore, withOlderHistory } from "../src/dev/demoOlderHistory.js";
import { createDemoSetup, DEMO_MODEL_NAMES, DEMO_MODEL_PROVIDERS, demoUpdateCheck } from "../src/dev/demoSetup.js";

// The playground's one scripted model; with EVENER_DEMO_FLEET, demoSetup.ts
// answers model/list instead.
const PLAYGROUND_MODEL_LIST = {
	data: [
		{
			provider: "demonstration",
			model: "scripted",
			displayName: "Scripted reply",
			reasoningEffortLevels: [],
		},
	],
} satisfies ModelListResponse;

// The fleet sessions' shared-notes and approval methods, served only with
// EVENER_DEMO_FLEET.
const FLEET_SESSION_METHODS = {
	"notes/human/set": (thread: Thread, params: NotesHumanSetParams) =>
		setHumanNote(thread, params, Date.now()),
	"urls/remove": (thread: Thread, params: UrlsRemoveParams) =>
		removeLink(thread, params, Date.now()),
	"evener/sandbox/escalation/resolve": resolveEscalation,
} as const;
type FleetSessionMethod = keyof typeof FLEET_SESSION_METHODS;

// The phase 6 screenshots' staging (see the startup block below for the
// environment variables that set these).
export interface DemoHubModes {
	// With the fleet on, one command per line: a DemoStep's name, or "burst"
	// (EVENER_DEMO_COMMANDS=1 wires this to stdin).
	commands?: NodeJS.ReadableStream;
	// turn/start and turn/queue answer as a daemon that can't record a
	// mutation's outcome does, so a send shows "Couldn't confirm this was
	// sent".
	unconfirmed?: boolean;
	// The handshake's protocolVersion, to show the phone a version mismatch.
	protocolVersion?: string;
	// thread/start answers this many seconds late, so New session can be
	// swiped away before its session opens (the Session started banner).
	startDelaySeconds?: number;
	// How often "grow" lands a step (default two seconds).
	growEveryMs?: number;
}

const DEMO_STEPS: readonly DemoStep[] = [
	"question",
	"failure",
	"approval",
	"finish",
	"host-offline",
	"host-online",
];
// "grow": the working session gains GROW_STEPS finished steps, one at a
// time, so the transcript's scrolling can be watched as rows land.
const GROW_STEPS = 15;
const COMMANDS = [...DEMO_STEPS, "burst", "grow"].join(", ");
// Three alerts within a second, for the coalesced banner.
const BURST: readonly DemoStep[] = ["question", "failure", "approval"];

export async function createDemoHub(
	port = 9196,
	initialMarkdown?: string,
	fleetOptions?: DemoFleetOptions,
	modes: DemoHubModes = {},
) {
	// One instant the fleet's rows and its sessions' threads both measure
	// "ago" from, so a row and its thread agree on when it last changed.
	const startedAt = fleetOptions?.now ?? Date.now();
	const demoFleet = fleetOptions
		? createDemoFleet({
				...fleetOptions,
				now: startedAt,
				modelProviders: DEMO_MODEL_PROVIDERS,
				modelNames: DEMO_MODEL_NAMES,
			})
		: null;
	// With the fleet on, New session and the Hub read the prototype's hosts,
	// providers, plugins, models and folders (demoSetup.ts).
	const demoSetup = demoFleet ? createDemoSetup(demoFleet, fleetOptions) : null;
	// With the demo fleet, the Reader's /doc/file reaches the same port as
	// /rpc, as on a real hub: one HTTP server carries both. The plan lives in
	// Get PR 2138 Test Clean's folder, where its transcript's link names it.
	const planSession = fleetSessions().find((session) => session.slug === "s-pr2138");
	const documents =
		demoFleet && planSession
			? createDemoDocuments(
					[
						{
							sessionRef: planSession.ref,
							path: "docs/superpowers/plans/2026-09-25-settle-race.md",
							text: fleetOptions?.planRevised ? SETTLE_RACE_PLAN_REVISED : SETTLE_RACE_PLAN,
						},
					],
					planSession.workingDir,
				)
			: null;
	const http = createServer((request, response) => {
		const url = URL.parse(request.url ?? "/", "http://demo");
		if (!url || !documents || request.method !== "GET" || url.pathname !== "/doc/file") {
			response.writeHead(404).end();
			return;
		}
		const answer = documents.answerDocFile(url);
		response.writeHead(answer.status, { "Content-Type": "text/plain; charset=utf-8" }).end(answer.body);
	});
	const server = new WebSocketServer({ server: http, path: "/rpc" });
	http.listen(port, "0.0.0.0");
	await once(http, "listening");
	const address = http.address();
	if (typeof address === "string" || !address)
		throw new Error("Missing demo address");
	const subscribers = new Map<WebSocket, Set<string>>();
	// thread/start replies held for startDelaySeconds, cleared on close.
	const heldReplies = new Set<ReturnType<typeof setTimeout>>();
	const thread: Thread = {
		id: "demo-thread",
		sessionId: "demo-session",
		name: "Mobile playground",
		preview: "Scripted demonstration only",
		ephemeral: true,
		modelProvider: "demonstration",
		createdAt: 1,
		updatedAt: 1,
		status: { type: "idle" },
		cwd: "/demonstration",
		cliVersion: "demo",
		source: "demo",
		turns:
			initialMarkdown === undefined
				? []
				: [
						{
							id: "demo-reference-turn",
							status: "completed",
							itemsView: "full",
							items: [
								{
									id: "demo-reference-message",
									type: "agentMessage",
									status: "completed",
									text: initialMarkdown,
								},
							],
						},
					],
		evener: {
			ref: "demo:playground",
			instanceId: "demo-instance",
			queue: { revision: 0 },
			capabilities: {
				send: true,
				steer: false,
				interrupt: false,
				compact: false,
				clear: false,
				forkFromTurn: false,
				shutdown: false,
				changeModel: false,
				changeVisionModel: false,
				sharedNotes: false,
				queue: true,
				goal: false,
				rename: false,
			},
		},
	};
	const threads = new Map([[thread.evener.ref, thread]]);
	// Each fleet session's own thread, on the ref its Board row names, so
	// opening a row reads a real conversation. An empty fleet has none.
	const fleetThreads =
		fleetOptions && !fleetOptions.empty
			? createDemoSessions({
					now: startedAt,
					long: fleetOptions.long,
					toolFamilies: fleetOptions.toolFamilies,
				})
			: [];
	for (const fleetThread of fleetThreads)
		threads.set(fleetThread.evener.ref, fleetThread);
	const olderHistoryThread = fleetOptions?.olderHistory
		? threads.get(fleetSessionRef("s-pr2138"))
		: undefined;
	if (olderHistoryThread) withOlderHistory(olderHistoryThread);
	const fleetRefs = new Set(fleetThreads.map((value) => value.evener.ref));
	let sessionNumber = 0;
	const handshake: InitializeResponse = {
		serverInfo: { name: "Native UI demonstration", version: "1" },
		protocolVersion: modes.protocolVersion ?? "evener-appwire-v6",
		sourceId: "demo",
		features: {
			threadList: true,
			threadTurnsList: true,
			turnStart: true,
			turnSteer: true,
			threadClear: false,
			threadShutdown: false,
			forkFromTurn: false,
			tasks: false,
			transcriptList: false,
			modelList: true,
			directoryComplete: false,
			auth: demoFleet !== null,
			// The fleet's setup keeps the hub's transcript display defaults.
			transcriptDisplaySettings: demoFleet !== null,
		},
	};
	let turnNumber = 0;
	// The Board-row changes a fleet session's turns made while answering one
	// message: setTurnRunning appends them, and the handler broadcasts every one
	// after it answers, the way it broadcasts an archive's invalidation.
	const turnNavigations: NavigationInvalidatedPayload[] = [];
	// Tells every socket connected at that moment that navigation changed,
	// as a real hub broadcasts navigation changes to every navigation client.
	function broadcastNavigation(payload: NavigationInvalidatedPayload) {
		broadcast("evener/navigation/invalidated", payload);
	}
	// Tells every client the hub's notices changed, carrying the whole new
	// list, as cmd/evener-hub/app_notices.go broadcasts evener/notices/changed.
	function broadcastNotices() {
		if (demoSetup) broadcast("evener/notices/changed", demoSetup.answer("evener/notices/list", {}));
	}
	function broadcast(method: string, params: unknown) {
		const notification = JSON.stringify({ jsonrpc: "2.0", method, params });
		for (const socket of server.clients) {
			if (socket.readyState !== WebSocket.OPEN) continue;
			// One socket that fails mid-send must not skip the rest of the
			// broadcast, as a real hub's per-client fan-out does.
			try {
				socket.send(notification);
			} catch {
				// The socket is gone; its close handler removes it.
			}
		}
	}
	// Makes the fleet's working row ask its question. Returned for tests to
	// fire on demand.
	function askQuestion() {
		broadcastNavigation(requireFleet().step("question"));
		const asking = threads.get(fleetSessionRef(ASKING_SESSION_ID));
		// Asked once: a second ask finds the session already waiting.
		if (asking?.status.type === "active") {
			askWorkingSessionQuestion(asking, Date.now());
			resync(asking);
		}
	}
	// Plays one scripted event for every connected client.
	function play(step: DemoStep) {
		if (step === "question") {
			askQuestion();
			return;
		}
		broadcastNavigation(requireFleet().step(step));
		if (step === "host-offline" || step === "host-online") {
			broadcastNotices();
			return;
		}
		// The session's own thread follows its row, as askQuestion's does.
		const [id, state] = ROW_STEPS[step];
		const thread = threads.get(fleetSessionRef(id));
		if (thread?.status.type !== "active") return;
		stageFleetState(thread, state as "failed" | "approval" | "yourmove", Date.now());
		resync(thread);
	}
	// "grow": one finished shell step lands in s-pr2138's running turn at a
	// time, each announced with a resync, as the hub announces a new round.
	const growTimers = new Set<ReturnType<typeof setInterval>>();
	let grown = 0;
	function grow() {
		const thread = threads.get(fleetSessionRef("s-pr2138"));
		const turn = thread?.turns?.find((candidate) => candidate.id === thread.evener.activeTurnId);
		if (!thread || !turn) throw new Error("s-pr2138 has no running turn to grow");
		let landed = 0;
		const timer = setInterval(() => {
			grown += 1;
			landed += 1;
			const now = Date.now();
			const step = {
				id: `demo-grow-${grown}`,
				type: "commandExecution",
				toolName: "shell",
				callId: `demo-grow-call-${grown}`,
				description: `Ran check ${grown}`,
				argumentsJson: JSON.stringify({ command: `go test ./agent/grow${grown}/...` }),
				status: "completed",
				startedAt: now - 3000,
				completedAt: now,
				output: "ok",
			} satisfies ThreadItem;
			turn.items = [...(turn.items ?? []), step];
			if (landed >= GROW_STEPS) {
				clearInterval(timer);
				growTimers.delete(timer);
			}
			// A step that can't be announced says so; the timer runs on.
			try {
				resync(thread);
			} catch (error) {
				console.error("grow: resync failed:", error);
			}
		}, modes.growEveryMs ?? 2000);
		growTimers.add(timer);
	}
	// The command input: a step's name plays it, "burst" plays three at once.
	const commandLines =
		demoFleet && modes.commands
			? createInterface({ input: modes.commands, terminal: false })
			: undefined;
	commandLines?.on("line", (line) => {
		const command = line.trim();
		// A step that fails says so and leaves the hub running.
		try {
			if (command === "burst") for (const step of BURST) play(step);
			else if (command === "grow") grow();
			else if ((DEMO_STEPS as readonly string[]).includes(command))
				play(command as DemoStep);
			else if (command !== "")
				console.info(`Unknown command ${command}. Commands: ${COMMANDS}`);
		} catch (error) {
			console.error(`Command ${command} failed:`, error);
		}
	});
	// EVENER_DEMO_FLEET_ASK_AFTER: counted from the hub's start, not from any
	// one client's connection.
	const askTimer =
		demoFleet && fleetOptions?.askAfterSeconds !== undefined
			? setTimeout(askQuestion, fleetOptions.askAfterSeconds * 1000)
			: undefined;
	function resync(thread: Thread) {
		for (const [socket, refs] of subscribers)
			if (refs.has(thread.evener.ref) && socket.readyState === WebSocket.OPEN)
				socket.send(
					JSON.stringify({
						jsonrpc: "2.0",
						method: "evener/thread/resync",
						params: { threadId: thread.id, ref: thread.evener.ref },
					}),
				);
	}
	// Starts the scripted turn: your message, stamped with its mutation id as
	// the real projector stamps it (appwire_projection.go, EventUserInput), so
	// a client recognizes its own send reflected back rather than treating it
	// as a message from elsewhere, then the scripted reply.
	function startScriptedTurn(
		thread: Thread,
		text: string,
		clientMutationId: string,
	): Turn {
		turnNumber += 1;
		const turn = {
			id: `demo-turn-${turnNumber}`,
			itemsView: "full",
			status: "inProgress",
			startedAt: Date.now(),
			items: [
				{
					id: `demo-user-${turnNumber}`,
					type: "userMessage",
					text,
					clientMutationId,
					status: "completed",
				},
				{
					id: `demo-assistant-${turnNumber}`,
					type: "agentMessage",
					text: "Demonstration reply: your message reached this scripted test server. Tap Stop to end this demonstration turn.",
					status: "completed",
				},
			],
		} satisfies Turn;
		thread.turns?.push(turn);
		// Your message answers any question the session was waiting on, as
		// the daemon clears its pending ask on user input.
		delete thread.evener.askPending;
		delete thread.evener.pendingQuestion;
		setTurnRunning(thread, turn);
		return turn;
	}
	// Stop: the turn ends interrupted (demoSessions.ts's endTurn).
	function stopTurn(thread: Thread, turn: Turn) {
		endTurn(thread, turn, "interrupted", Date.now());
		setTurnRunning(thread, undefined);
	}
	// Moves the thread to `turn` running, or with no turn, to resting. A
	// fleet session follows demoSessions.ts's rules; the playground offers
	// Stop and steering only while its scripted turn runs.
	function setTurnRunning(thread: Thread, turn: Turn | undefined) {
		if (fleetRefs.has(thread.evener.ref)) {
			if (turn) startFleetTurn(thread, turn, Date.now());
			else restFleetSession(thread, "idle", Date.now());
			// The Board follows the turn: a running turn reads Working, a
			// stopped one Idle, so a Stop leaves the row where its thread is.
			const invalidated = requireFleet().setSessionState(thread.evener.ref, turn ? "working" : "idle");
			if (invalidated) turnNavigations.push(invalidated);
			return;
		}
		const running = turn !== undefined;
		thread.status = { type: running ? "active" : "idle" };
		thread.evener.capabilities.send = !running;
		thread.evener.capabilities.interrupt = running;
		thread.evener.capabilities.steer = running;
		thread.evener.activeTurnId = turn?.id;
		thread.updatedAt += 1;
	}
	function requireFleet() {
		if (!demoFleet)
			throw new Error("Method not implemented by demonstration server");
		return demoFleet;
	}
	server.on("connection", (socket) => {
		socket.on("close", () => subscribers.delete(socket));
		socket.on("message", (raw) => {
			let id: unknown = null;
			try {
				const request = JSON.parse(raw.toString());
				id = request.id;
				if (id === undefined) return;
				const params = request.params ?? {};
				let result: unknown;
				let changed: Thread | null = null;
				let navigationChange: NavigationInvalidatedPayload | null = null;
				const selected = threads.get(params.ref);
				// A daemon that can't record a send's outcome
				// (appwire/errors.go's MutationUnknown).
				if (
					modes.unconfirmed &&
					(request.method === "turn/start" ||
						request.method === "turn/queue")
				)
					throw new WireError(
						"The send's outcome couldn't be recorded",
						-32603,
						{
							evenerErrorInfo: "mutationOutcomeUnknown",
							clientMutationId: params.clientMutationId,
							mutationOutcome: "unknown",
							retryDisposition: "blocked",
							cause: "persistenceUnavailable",
						},
					);
				if (demoSetup?.handles(request.method))
					result = demoSetup.answer(request.method, params);
				else
					switch (request.method) {
					case "initialize":
						result = demoFleet
							? { ...handshake, navigation: demoFleet.navigationCapability() }
							: handshake;
						break;
					case "ping":
						result = {};
						break;
					case "evener/update/check":
						result = demoUpdateCheck();
						break;
					case "evener/projects/recent":
						result = { data: ["/demonstration"] };
						break;
					case "evener/harnesses/list":
						result = {
							data: [{ id: "demonstration", label: "Demonstration (no LLM)" }],
						};
						break;
					case "model/list":
						result = PLAYGROUND_MODEL_LIST;
						break;
					case "thread/list":
						result = {
							data: [...threads.values()].map((value) => ({
								...value,
								turns: undefined,
							})),
						};
						break;
					case "thread/start": {
						if (!params.cwd?.trim())
							throw new Error("A project directory is required");
						sessionNumber += 1;
						const created: Thread = structuredClone(thread);
						created.id = `demo-thread-created-${sessionNumber}`;
						created.sessionId = `demo-session-created-${sessionNumber}`;
						// Another host's session is named by that host, as a real
						// hub qualifies a remote ref (appwire/refs.go).
						const source = params.source || "demo";
						created.evener.ref = `${source}:created-${sessionNumber}`;
						created.source = source;
						created.evener.instanceId = `demo-instance-created-${sessionNumber}`;
						created.cwd = params.cwd;
						created.modelProvider = params.modelProvider ?? "demonstration";
						created.name = `Demonstration ${sessionNumber}`;
						created.turns = [];
						created.status = { type: "idle" };
						created.evener.capabilities.send = true;
						created.evener.capabilities.interrupt = false;
						delete created.evener.activeTurnId;
						const openingText = inputText(params.input);
						const turn: Turn = {
							id: `demo-opening-${sessionNumber}`,
							status: openingText ? "inProgress" : "completed",
							itemsView: "full",
							items: [],
						};
						if (openingText) {
							turn.items = [
								{
									id: `demo-opening-user-${sessionNumber}`,
									type: "userMessage",
									text: openingText,
									status: "completed",
								},
								{
									id: `demo-opening-assistant-${sessionNumber}`,
									type: "agentMessage",
									text: "Demonstration session created. This is a scripted reply; no model is running.",
									status: "completed",
								},
							];
							created.turns.push(turn);
							created.status = { type: "active" };
							created.evener.capabilities.send = false;
							created.evener.capabilities.interrupt = true;
							created.evener.capabilities.steer = true;
							created.evener.activeTurnId = turn.id;
						}
						threads.set(created.evener.ref, created);
						result = { thread: created, turn };
						break;
					}
					case "thread/read":
						if (!selected) throw new Error("Unknown demonstration session");
						if (params.subscribe) {
							const refs = subscribers.get(socket) ?? new Set<string>();
							refs.add(selected.evener.ref);
							subscribers.set(socket, refs);
						}
						// EVENER_DEMO_FLEET_OLDER: the latest page of items, and a cursor
						// to the pages before it.
						if (
							selected === olderHistoryThread &&
							params.includeTurns &&
							typeof params.itemLimit === "number"
						) {
							const page = latestPage(selected.turns ?? [], params.itemLimit);
							result = {
								thread: { ...selected, turns: page.turns },
								...(page.olderCursor ? { olderCursor: page.olderCursor } : {}),
							};
							break;
						}
						result = {
							thread: {
								...selected,
								turns: params.includeTurns ? selected.turns : undefined,
							},
						};
						break;
					case "thread/unsubscribe":
						subscribers.get(socket)?.delete(params.ref);
						result = {};
						break;
					case "thread/turns/list": {
						const page =
							selected && selected === olderHistoryThread
								? pageBefore(selected.turns ?? [], params.cursor, params.itemLimit ?? 40)
								: null;
						result = page
							? { data: page.turns, ...(page.olderCursor ? { nextCursor: page.olderCursor } : {}) }
							: { data: [] };
						break;
					}
					case "turn/queue":
					case "turn/steer":
					case "turn/cancelQueued":
					case "turn/promoteQueuedAsSteer":
					case "turn/drainAsSteer": {
						if (
							!selected ||
							params.expectedInstanceId !== selected.evener.instanceId ||
							!params.clientMutationId
						)
							throw new WireError("Session identity changed", -32013, {
								evenerErrorInfo: "conflict",
							});
						// A message queued for a session at rest (idle, or waiting
						// on you) runs the queue's head as its own turn, as the
						// daemon's ProcessPendingUserInput does; queueing again also
						// releases a Stop's hold (session_client_mutation_queue.go).
						// With nothing queued, the head is this message.
						const resting =
							request.method === "turn/queue" &&
							(selected.status.type === "idle" ||
								selected.status.type === "awaiting");
						if (resting && (selected.evener.queue.depth ?? 0) === 0) {
							const turn = startScriptedTurn(
								selected,
								inputText(params.input),
								params.clientMutationId,
							);
							result = {
								receipt: {
									clientMutationId: params.clientMutationId,
									disposition: "applied",
									threadId: selected.id,
									instanceId: selected.evener.instanceId,
									turnId: turn.id,
									projectionState: "pending",
								} satisfies MutationReceipt,
							};
							changed = selected;
							break;
						}
						const queue = selected.evener.queue;
						const ids = queue.ids ?? [];
						const texts = queue.texts ?? [];
						// The phone clears a queued message's ghost once its mutation id
						// shows here (pendingEntries.ts's reflectedMutationIds), so the
						// ids stay in step with the entries, as the daemon keeps them.
						const mutationIds = queue.clientMutationIds ?? [];
						const method = request.method;
						let removedTexts: string[] = [];
						let consumedIds: string[] = [];
						let entryIds: string[] | undefined;
						if (method === "turn/queue") {
							const id = `demo-queue-${params.clientMutationId}`;
							ids.push(id);
							texts.push(inputText(params.input));
							mutationIds.push(params.clientMutationId);
							entryIds = [id];
						} else if (method === "turn/steer") {
							removedTexts = [inputText(params.input)];
						} else {
							if (method === "turn/drainAsSteer") {
								if (
									params.expectedQueueRevision !== queue.revision ||
									ids.length === 0
								)
									throw new WireError("Queue revision changed", -32013, {
										evenerErrorInfo: "conflict",
									});
								removedTexts = texts.splice(0);
								entryIds = ids.splice(0);
								consumedIds = mutationIds.splice(0);
							} else {
								if (
									params.index < 0 ||
									!params.expectedEntryId ||
									ids[params.index] !== params.expectedEntryId
								)
									throw new WireError("Queue entry changed", -32013, {
										evenerErrorInfo: "conflict",
									});
								removedTexts = texts.splice(params.index, 1);
								entryIds = ids.splice(params.index, 1);
								mutationIds.splice(params.index, 1);
							}
						}
						// At rest with a message parked ahead of this one, the
						// parked one runs now and this one waits behind it.
						let released: { text: string; mutationId: string } | undefined;
						if (resting && ids.length > 1) {
							ids.shift();
							released = {
								text: texts.shift() ?? "",
								mutationId: mutationIds.shift() ?? "",
							};
						}
						if (method !== "turn/steer") {
							queue.revision += 1;
							queue.ids = ids;
							queue.texts = texts;
							queue.clientMutationIds = mutationIds;
							queue.preview = queuePreview(texts);
							queue.depth = ids.length;
						}
						const steering =
							method === "turn/steer" ||
							method === "turn/promoteQueuedAsSteer" ||
							method === "turn/drainAsSteer";
						const running = selected.turns?.find(
							(turn) => turn.id === selected.evener.activeTurnId,
						);
						if (steering && running && selected.status.type === "active")
							// While a turn runs, the steer lands in it as the daemon
							// records one (apptranscript.go, TurnSteering), carrying the
							// mutation's id so the phone sees it arrive.
							running.items?.push({
								id: `demo-steering-${params.clientMutationId}`,
								type: "steering",
								source: "user",
								text: removedTexts.join("\n"),
								clientMutationId: params.clientMutationId,
								status: "completed",
							});
						else if (steering)
							// A resting session wakes with the message: a steer, or a
							// held message sent (Stop parked the queue), starts a turn
							// with it, as the daemon's wakeForPendingSteering does.
							startScriptedTurn(
								selected,
								removedTexts.join("\n"),
								params.clientMutationId,
							);
						else if (released)
							startScriptedTurn(selected, released.text, released.mutationId);
						const receipt: MutationReceipt = {
							clientMutationId: params.clientMutationId,
							disposition: "applied",
							threadId: selected.id,
							instanceId: selected.evener.instanceId,
							projectionState:
								method === "turn/cancelQueued" ? "removed" : "pending",
							...(entryIds ? { queueEntryIds: entryIds } : {}),
							...(method === "turn/drainAsSteer"
								? { consumedClientMutationIds: consumedIds }
								: {}),
							...(method === "turn/queue" || method === "turn/cancelQueued"
								? {}
								: { turnId: selected.evener.activeTurnId }),
						};
						result =
							method === "turn/cancelQueued"
								? { removedText: removedTexts[0], receipt }
								: { receipt };
						changed = selected;
						break;
					}

					case "turn/start":
					case "turn/interrupt": {
						if (!selected) throw new Error("Unknown demonstration session");
						const thread = selected;
						const mutation = params as TurnStartParams;
						if (
							mutation.ref !== thread.evener.ref ||
							mutation.expectedInstanceId !== thread.evener.instanceId
						)
							throw new Error("Demonstration session identity mismatch");
						if (!mutation.clientMutationId)
							throw new Error("Missing mutation identity");
						const starting = request.method === "turn/start";
						let turn = thread.turns?.at(-1);
						if (starting) {
							if (thread.status.type === "active")
								throw new Error("Stop the demonstration before another send");
							turn = startScriptedTurn(
								thread,
								inputText(mutation.input),
								mutation.clientMutationId,
							);
						} else {
							if (!turn || thread.status.type !== "active")
								throw new Error("No active demonstration turn");
							stopTurn(thread, turn);
						}
						const receipt: MutationReceipt = {
							clientMutationId: mutation.clientMutationId,
							disposition: "applied",
							threadId: thread.id,
							instanceId: thread.evener.instanceId,
							turnId: turn.id,
							projectionState: starting ? "pending" : "reflected",
						};
						result = starting ? { turn, receipt } : { receipt };
						changed = thread;
						break;
					}
					case "notes/human/set":
					case "urls/remove":
					case "evener/sandbox/escalation/resolve":
						requireFleet();
						if (!selected) throw new Error("Unknown demonstration session");
						result = FLEET_SESSION_METHODS[request.method as FleetSessionMethod](
							selected,
							params,
						);
						changed = selected;
						break;
					case "evener/navigation/read":
						result = requireFleet().answerNavigationRead(params);
						break;
					case "evener/thread/activity/read":
						result = requireFleet().answerActivityRead(params);
						break;
					case "evener/thread/delegates/list":
						result = requireFleet().answerDelegatesList(params);
						break;
					case "evener/thread/jobs/list":
						result = requireFleet().answerSessionJobsList(params);
						break;
					case "evener/thread/watches/list":
						result = requireFleet().answerWatchesList(params);
						break;
					case "evener/jobs/list":
						result = requireFleet().answerJobsList(params);
						break;
					case "evener/jobs/output":
						result = requireFleet().answerJobsOutput(params);
						break;
					case "evener/search":
						result = requireFleet().answerSearch(params);
						break;
					case "evener/auth/list":
						result = requireFleet().answerAuthList();
						break;
					case "evener/plugin/list":
						result = requireFleet().answerPluginList();
						break;
					case "evener/archive/set": {
						const { response, invalidated } = requireFleet().archive(params);
						result = response;
						navigationChange = invalidated;
						break;
					}
					default:
						throw new Error("Method not implemented by demonstration server");
				}
				// What a fleet session offers follows every change to it.
				if (changed && fleetRefs.has(changed.evener.ref))
					refreshCapabilities(changed);
				const reply = JSON.stringify({ jsonrpc: "2.0", id, result });
				if (request.method === "thread/start" && modes.startDelaySeconds) {
					// Held, as a slow hub would; a client that leaves meanwhile takes
					// the held reply with it.
					const dropHeld = () => {
						clearTimeout(held);
						heldReplies.delete(held);
					};
					const held = setTimeout(() => {
						heldReplies.delete(held);
						socket.off("close", dropHeld);
						socket.send(reply);
					}, modes.startDelaySeconds * 1000);
					heldReplies.add(held);
					socket.once("close", dropHeld);
				} else socket.send(reply);
				if (changed) resync(changed);
				if (navigationChange) broadcastNavigation(navigationChange);
			} catch (error) {
				socket.send(
					JSON.stringify({
						jsonrpc: "2.0",
						id,
						error: {
							code: error instanceof WireError ? error.code : -32602,
							data: error instanceof WireError ? error.data : undefined,
							message:
								error instanceof Error ? error.message : "Invalid request",
						},
					}),
				);
			} finally {
				for (const invalidated of turnNavigations) broadcastNavigation(invalidated);
				turnNavigations.length = 0;
			}
		});
	});
	return {
		origin: `http://127.0.0.1:${address.port}`,
		askQuestion,
		close: () =>
			new Promise<void>((resolve, reject) => {
				clearTimeout(askTimer);
				for (const held of heldReplies) clearTimeout(held);
				for (const timer of growTimers) clearInterval(timer);
				commandLines?.close();
				for (const socket of server.clients) socket.terminate();
				server.close();
				http.close((error) => (error ? reject(error) : resolve()));
			}),
	};
}

// A mutation's input as the text a transcript item carries: its text parts,
// one per line.
function inputText(input: InputItem[] | undefined): string {
	return (input ?? []).map((item) => item.text ?? "").join("\n");
}

// EVENER_DEMO_FLEET_ASK_AFTER and EVENER_DEMO_START_DELAY are numbers of
// seconds; anything else is a typo worth stopping on rather than a demo that
// silently never changes.
function secondsFrom(name: string): number | undefined {
	const value = process.env[name];
	if (value === undefined || value === "") return undefined;
	const seconds = Number(value);
	if (!Number.isFinite(seconds) || seconds < 0)
		throw new Error(
			`${name} must be a number of seconds, got ${JSON.stringify(value)}`,
		);
	return seconds;
}

const USAGE = `Usage: npx tsx scripts/demo-hub.mts [--help]

A scripted hub for native UI checks: no Evener daemon, no LLM. Add it in the
app as a hub at http://127.0.0.1:<port> with no token. Configured by
environment variables:

  EVENER_DEMO_PORT=<port>            listen here (default 9196)
  EVENER_DEMO_MARKDOWN=<file>        the playground's reply text
  EVENER_DEMO_FLEET=1                serve the redesign's fleet of sessions
  EVENER_DEMO_FLEET_OFFLINE_HOST=1   with the fleet: paradise-park offline
  EVENER_DEMO_FLEET_EMPTY=1          with the fleet: nothing live
  EVENER_DEMO_FLEET_ASK_AFTER=<s>    with the fleet: s-gateway asks after s seconds
  EVENER_DEMO_FLEET_PLAN_REVISED=1   with the fleet: serve the plan's revision
  EVENER_DEMO_FLEET_OLDER=1          with the fleet: fifteen older turns ahead of
                                     Get PR 2138's, paged by item as a v6 hub does
  EVENER_DEMO_LONG=1                 with the fleet: long questions, approvals
                                     and messages, many steps and notifications,
                                     so screenshots exercise real-sized content
  EVENER_DEMO_FLEET_TOOLS=1          with the fleet: add Show Every Tool Family,
                                     one step of every tool family, replayed
                                     from the recorded wire corpora
  EVENER_DEMO_COMMANDS=1             with the fleet: read commands from stdin
                                     (${COMMANDS})
  EVENER_DEMO_UNCONFIRMED=1          answer sends as a daemon that can't confirm them
  EVENER_DEMO_PROTOCOL=<version>     the handshake's protocol version
  EVENER_DEMO_START_DELAY=<s>        thread/start answers s seconds late
`;

if (
	process.argv[1] &&
	import.meta.url === pathToFileURL(process.argv[1]).href
) {
	if (process.argv.includes("--help") || process.argv.includes("-h")) {
		// Exit only once the help has drained: into a pipe, stdout is
		// asynchronous, and exiting at once can cut it short.
		await new Promise((resolve) => process.stdout.write(USAGE, resolve));
		process.exit(0);
	}
	const hub = await createDemoHub(
		Number(process.env.EVENER_DEMO_PORT ?? 9196),
		process.env.EVENER_DEMO_MARKDOWN
			? readFileSync(process.env.EVENER_DEMO_MARKDOWN, "utf8")
			: undefined,
		process.env.EVENER_DEMO_FLEET === "1"
			? {
					offlineHost: process.env.EVENER_DEMO_FLEET_OFFLINE_HOST === "1",
					empty: process.env.EVENER_DEMO_FLEET_EMPTY === "1",
					askAfterSeconds: secondsFrom("EVENER_DEMO_FLEET_ASK_AFTER"),
					planRevised: process.env.EVENER_DEMO_FLEET_PLAN_REVISED === "1",
					olderHistory: process.env.EVENER_DEMO_FLEET_OLDER === "1",
					long: process.env.EVENER_DEMO_LONG === "1",
					toolFamilies: process.env.EVENER_DEMO_FLEET_TOOLS === "1",
				}
			: undefined,
		{
			// Opt-in: a hub started in the background from a shell would
			// stop (SIGTTIN) the moment it read the terminal.
			commands:
				process.env.EVENER_DEMO_COMMANDS === "1" ? process.stdin : undefined,
			unconfirmed: process.env.EVENER_DEMO_UNCONFIRMED === "1",
			protocolVersion: process.env.EVENER_DEMO_PROTOCOL || undefined,
			startDelaySeconds: secondsFrom("EVENER_DEMO_START_DELAY"),
		},
	);
	console.info(
		`Scripted native UI demonstration: ${hub.origin} (no token, no LLM).`,
	);
	if (
		process.env.EVENER_DEMO_FLEET === "1" &&
		process.env.EVENER_DEMO_COMMANDS === "1"
	)
		console.info(`Type a command to stage an alert: ${COMMANDS}.`);
	for (const signal of ["SIGINT", "SIGTERM"] as const)
		process.once(signal, () => {
			void hub.close();
		});
}
