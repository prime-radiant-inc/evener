// Explicitly launched network fixture for native UI checks; no Evener or LLM runs.
import { once } from "node:events";
import { createServer } from "node:http";
import { readFileSync } from "node:fs";
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
	Turn,
	TurnStartParams,
	UrlsRemoveParams,
} from "@evener/appwire-client";
import {
	ASKING_SESSION_ID,
	createDemoFleet,
	type DemoFleetOptions,
	fleetSessionRef,
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
	startFleetTurn,
} from "../src/dev/demoSessions.js";
import { createDemoSetup } from "../src/dev/demoSetup.js";

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

export async function createDemoHub(
	port = 9196,
	initialMarkdown?: string,
	fleetOptions?: DemoFleetOptions,
) {
	// One instant the fleet's rows and its sessions' threads both measure
	// "ago" from, so a row and its thread agree on when it last changed.
	const startedAt = fleetOptions?.now ?? Date.now();
	const demoFleet = fleetOptions
		? createDemoFleet({ ...fleetOptions, now: startedAt })
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
			? createDemoSessions({ now: startedAt })
			: [];
	for (const fleetThread of fleetThreads)
		threads.set(fleetThread.evener.ref, fleetThread);
	const fleetRefs = new Set(fleetThreads.map((value) => value.evener.ref));
	let sessionNumber = 0;
	const handshake: InitializeResponse = {
		serverInfo: { name: "Native UI demonstration", version: "1" },
		protocolVersion: "evener-appwire-v6",
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
		},
	};
	let turnNumber = 0;
	// Tells every socket connected at that moment that navigation changed,
	// as a real hub broadcasts navigation changes to every navigation client.
	function broadcastNavigation(payload: NavigationInvalidatedPayload) {
		const notification = JSON.stringify({
			jsonrpc: "2.0",
			method: "evener/navigation/invalidated",
			params: payload,
		});
		for (const socket of server.clients)
			if (socket.readyState === WebSocket.OPEN) socket.send(notification);
	}
	// Makes the fleet's working row ask its question. Returned for tests to
	// fire on demand.
	function askQuestion() {
		broadcastNavigation(requireFleet().askQuestion());
		const asking = threads.get(fleetSessionRef(ASKING_SESSION_ID));
		// Asked once: a second ask finds the session already waiting.
		if (asking?.status.type === "active") {
			askWorkingSessionQuestion(asking, Date.now());
			resync(asking);
		}
	}
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
						created.evener.ref = `${params.source || "demo"}:created-${sessionNumber}`;
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
					case "thread/turns/list":
						result = { data: [] };
						break;
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
					case "evener/jobs/list":
						result = requireFleet().answerJobsList(params);
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
				socket.send(JSON.stringify({ jsonrpc: "2.0", id, result }));
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
			}
		});
	});
	return {
		origin: `http://127.0.0.1:${address.port}`,
		askQuestion,
		close: () =>
			new Promise<void>((resolve, reject) => {
				clearTimeout(askTimer);
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

// EVENER_DEMO_FLEET_ASK_AFTER is a number of seconds; anything else is a
// typo worth stopping on rather than a demo that silently never changes.
function askAfterSeconds(value: string | undefined): number | undefined {
	if (value === undefined || value === "") return undefined;
	const seconds = Number(value);
	if (!Number.isFinite(seconds) || seconds < 0)
		throw new Error(
			`EVENER_DEMO_FLEET_ASK_AFTER must be a number of seconds, got ${JSON.stringify(value)}`,
		);
	return seconds;
}

if (
	process.argv[1] &&
	import.meta.url === pathToFileURL(process.argv[1]).href
) {
	const hub = await createDemoHub(
		Number(process.env.EVENER_DEMO_PORT ?? 9196),
		process.env.EVENER_DEMO_MARKDOWN
			? readFileSync(process.env.EVENER_DEMO_MARKDOWN, "utf8")
			: undefined,
		process.env.EVENER_DEMO_FLEET === "1"
			? {
					offlineHost: process.env.EVENER_DEMO_FLEET_OFFLINE_HOST === "1",
					empty: process.env.EVENER_DEMO_FLEET_EMPTY === "1",
					askAfterSeconds: askAfterSeconds(process.env.EVENER_DEMO_FLEET_ASK_AFTER),
					planRevised: process.env.EVENER_DEMO_FLEET_PLAN_REVISED === "1",
				}
			: undefined,
	);
	console.info(
		`Scripted native UI demonstration: ${hub.origin} (no token, no LLM).`,
	);
	for (const signal of ["SIGINT", "SIGTERM"] as const)
		process.once(signal, () => {
			void hub.close();
		});
}
