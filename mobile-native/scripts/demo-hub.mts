// Explicitly launched network fixture for native UI checks; no Evener or LLM runs.
import { once } from "node:events";
import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { WebSocket, WebSocketServer } from "ws";
import type {
	InitializeResponse,
	MutationReceipt,
	Thread,
	Turn,
	TurnStartParams,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";

export async function createDemoHub(port = 9196, initialMarkdown?: string) {
	const server = new WebSocketServer({ host: "127.0.0.1", port, path: "/rpc" });
	await once(server, "listening");
	const address = server.address();
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
				queue: false,
				goal: false,
				rename: false,
			},
		},
	};
	const threads = new Map([[thread.evener.ref, thread]]);
	let sessionNumber = 0;
	const handshake: InitializeResponse = {
		serverInfo: { name: "Native UI demonstration", version: "1" },
		protocolVersion: "evener-appwire-v3",
		sourceId: "demo",
		features: {
			threadList: true,
			threadTurnsList: true,
			turnStart: true,
			turnSteer: false,
			threadClear: false,
			threadShutdown: false,
			forkFromTurn: false,
			tasks: false,
			transcriptList: false,
			modelList: true,
			directoryComplete: false,
			auth: false,
		},
	};
	let turnNumber = 0;
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
				const selected = threads.get(params.ref);
				switch (request.method) {
					case "initialize":
						result = handshake;
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
						result = {
							data: [
								{
									provider: "demonstration",
									model: "scripted",
									displayName: "Scripted reply",
									reasoningEffortLevels: [],
								},
							],
						};
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
						created.evener.ref = `demo:created-${sessionNumber}`;
						created.evener.instanceId = `demo-instance-created-${sessionNumber}`;
						created.cwd = params.cwd;
						created.modelProvider = params.modelProvider ?? "demonstration";
						created.name = `Demonstration ${sessionNumber}`;
						created.turns = [];
						created.status = { type: "idle" };
						created.evener.capabilities.send = true;
						created.evener.capabilities.interrupt = false;
						delete created.evener.activeTurnId;
						const inputText = (params.input ?? [])
							.map((item: { text?: string }) => item.text ?? "")
							.join("\n");
						const turn: Turn = {
							id: `demo-opening-${sessionNumber}`,
							status: inputText ? "inProgress" : "completed",
							itemsView: "full",
							items: [],
						};
						if (inputText) {
							turn.items = [
								{
									id: `demo-opening-user-${sessionNumber}`,
									type: "userMessage",
									text: inputText,
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
							turnNumber += 1;
							turn = {
								id: `demo-turn-${turnNumber}`,
								itemsView: "full",
								status: "inProgress",
								items: [
									{
										id: `demo-user-${turnNumber}`,
										type: "userMessage",
										text: (mutation.input ?? [])
											.map((item) => item.text ?? "")
											.join("\n"),
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
						} else {
							if (!turn || thread.status.type !== "active")
								throw new Error("No active demonstration turn");
							turn.status = "interrupted";
						}
						if (!turn) throw new Error("Missing demonstration turn");
						thread.status = { type: starting ? "active" : "idle" };
						thread.evener.capabilities.send = !starting;
						thread.evener.capabilities.interrupt = starting;
						thread.evener.activeTurnId = starting ? turn.id : undefined;
						thread.updatedAt += 1;
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
					default:
						throw new Error("Method not implemented by demonstration server");
				}
				socket.send(JSON.stringify({ jsonrpc: "2.0", id, result }));
				if (changed) resync(changed);
			} catch (error) {
				socket.send(
					JSON.stringify({
						jsonrpc: "2.0",
						id,
						error: {
							code: -32602,
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
		close: () =>
			new Promise<void>((resolve, reject) => {
				for (const socket of server.clients) socket.terminate();
				server.close((error) => (error ? reject(error) : resolve()));
			}),
	};
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
	);
	console.info(
		`Scripted native UI demonstration: ${hub.origin} (no token, no LLM).`,
	);
	for (const signal of ["SIGINT", "SIGTERM"] as const)
		process.once(signal, () => {
			void hub.close();
		});
}
