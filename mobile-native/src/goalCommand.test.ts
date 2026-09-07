import { DatabaseSync } from "node:sqlite";
import { expect, it } from "vitest";
import type {
	ModelListResponse,
	Thread,
	ThreadClearResponse,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import {
	type ConversationClientLike,
	createConversationService,
} from "../../mobile/src/services/conversation";
import { createActivityStore } from "../../mobile/src/state/activity";
import { createConversationStore } from "../../mobile/src/state/conversation";
import { submitComposerCommand } from "./composerCommand";
import { DraftDocument } from "./draftDocument";
import { type DraftDatabase, DraftRepository } from "./draftRepository";
import { goalObjective, submitGoalCommand } from "./goalCommand";

const commandContext = {
	isCurrent: () => true,
	reasoning: () => null,
	turn: () => null,
	local: async () => {},
	openAside: (_ref: string, _title: string) => {},
	cleared: (_response: ThreadClearResponse) => {},
};

function boundary() {
	const thread: Thread = {
		id: "thread",
		sessionId: "session",
		preview: "",
		ephemeral: false,
		modelProvider: "scripted",
		createdAt: 1,
		updatedAt: 1,
		status: { type: "idle" },
		cwd: "/test",
		cliVersion: "test",
		source: "local",
		turns: [],
		evener: {
			ref: "local:test",
			instanceId: "instance",
			queue: { revision: 0 },
			capabilities: {
				send: true,
				steer: false,
				interrupt: false,
				compact: true,
				clear: false,
				forkFromTurn: false,
				shutdown: true,
				changeModel: true,
				changeVisionModel: false,
				queue: false,
				goal: true,
				rename: true,
			},
		},
	};
	const objectives: string[] = [];
	const io = {
		models: async (): Promise<ModelListResponse> => ({
			data: [
				{
					provider: "scripted",
					model: "alternate",
					displayName: "Alternate Model",
				},
			],
		}),
		lifecycle: async (_method: string, _params: unknown) => ({}),
		read: async () => ({ thread: structuredClone(thread) }),
		set: async (objective: string) => {
			objectives.push(objective);
			return { started: false };
		},
	};
	const service = createConversationService({
		request: async (method, params) => {
			if (method === "thread/read") return io.read();
			if (
				[
					"turn/steer",
					"turn/queue",
					"turn/drainAsSteer",
					"turn/interrupt",
				].includes(method)
			)
				return io.lifecycle(method, params);
			if (method === "model/list") return io.models();
			if (method === "thread/fork" || method === "thread/clear")
				return io.lifecycle(method, params);
			if (
				method === "thread/model/set" ||
				method === "thread/reasoning-effort/set"
			)
				return io.lifecycle(method, params);
			if (method === "goal/set")
				return io.set((params as { objective: string }).objective);
			if (method === "thread/compact/start" || method === "thread/shutdown")
				return io.lifecycle(method, params);
			throw new Error(`Unexpected ${method}`);
		},
		onNotification: () => () => {},
	} as ConversationClientLike);
	return { thread, objectives, io, service };
}

it.each([
	["/goal", ""],
	["/goal  objective\nnext line ", "objective\nnext line"],
	["/goals objective", null],
	[" /goal objective", null],
	["/goal\nobjective", null],
	["/plugin:goal objective", null],
])("parses the web goal command grammar: %s", (input, expected) => {
	expect(goalObjective(input)).toBe(expected);
	expect(goalObjective(input, 1)).toBeNull();
});

it.each([
	["/compact", "thread/compact/start"],
	["/shutdown", "thread/shutdown"],
])(
	"checkpoints %s and preserves a newer draft through acknowledgement loss",
	async (command, method) => {
		const db = new DatabaseSync(":memory:");
		const repository = new DraftRepository({
			execSync: (sql) => db.exec(sql),
			runSync: (sql, ...params) => db.prepare(sql).run(...params),
			getFirstSync: <T>(sql: string, ...params: string[]) =>
				(db.prepare(sql).get(...params) as T | undefined) ?? null,
		});
		const destination = { hubId: "hub", sessionRef: "local:test" };
		const document = new DraftDocument(() => repository, destination);
		const { io, service } = boundary();
		try {
			await service.open(destination.sessionRef);
			let received = 0;
			io.lifecycle = async (actual, params) => {
				received++;
				expect(actual).toBe(method);
				expect(params).toEqual({ ref: destination.sessionRef });
				expect(repository.read(destination).unconfirmed).toBe(command);
				return {};
			};
			document.edit(command);
			expect(
				await submitComposerCommand(document, service, commandContext),
			).toBe(command.slice(1));
			expect(received).toBe(1);
			expect(repository.read(destination)).toMatchObject({
				draft: "",
				unconfirmed: null,
			});
			document.edit(command);
			io.lifecycle = async () => {
				document.edit("newer draft");
				throw new Error("Lost acknowledgement");
			};
			await expect(
				submitComposerCommand(document, service, commandContext),
			).rejects.toThrow();
			expect(repository.read(destination)).toMatchObject({
				draft: "newer draft",
				unconfirmed: command,
			});
			document.edit(command);
			expect(
				await submitComposerCommand(document, service, commandContext),
			).toBeNull();
			expect(repository.read(destination).unconfirmed).toBe(command);
		} finally {
			service.close();
			db.close();
		}
	},
);

it("preserves newer live work state across an older hydration and isolates session notifications", async () => {
	const { thread, io, service } = boundary();
	thread.evener.goal = {
		objective: "initial",
		status: "active",
		iterations: 1,
	};
	const store = createConversationStore();
	thread.evener.tasks = { total: 2, done: 0 };
	const activity = createActivityStore().getState();
	try {
		await store.getState().openProjected(service, activity, "local:test");
		expect(store.getState().conversation?.goal?.objective).toBe("initial");
		expect(store.getState().conversation?.tasks).toEqual({ total: 2, done: 0 });
		let finishRead!: () => void;
		let entered!: () => void;
		const enteredRead = new Promise<void>((resolve) => {
			entered = resolve;
		});
		io.read = () =>
			new Promise((resolve) => {
				finishRead = () => resolve({ thread: structuredClone(thread) });
				entered();
			});
		const refresh = store.getState().rehydrate(service, activity);
		await enteredRead;
		const updated = { objective: "remote", status: "achieved", iterations: 4 };
		store.getState().applyNotification({
			method: "evener/goal/updated",
			params: { threadId: "other", ref: "other:test", goal: updated },
		});
		expect(store.getState().conversation?.goal?.objective).toBe("initial");
		store.getState().applyNotification({
			method: "evener/goal/updated",
			params: { threadId: "thread", ref: "local:test", goal: updated },
		});
		store.getState().applyNotification({
			method: "evener/task/updated",
			params: {
				threadId: "thread",
				ref: "local:test",
				total: 3,
				done: 1,
				current: { id: 2, description: "task-sentinel" },
			},
		});
		finishRead();
		await refresh;
		expect(store.getState().conversation?.goal).toEqual(updated);
		expect(store.getState().conversation?.tasks).toMatchObject({
			total: 3,
			done: 1,
			current: { id: 2 },
		});
		store.getState().applyNotification({
			method: "evener/goal/updated",
			params: { threadId: "thread", ref: "local:test", goal: null },
		});
		expect(store.getState().conversation?.goal).toBeNull();
	} finally {
		store.getState().close();
		service.close();
	}
});

it("checkpoints goal changes, retains uncertain delivery, and clears without consuming an ordinary draft", async () => {
	const db = new DatabaseSync(":memory:");
	const adapter: DraftDatabase = {
		execSync: (sql) => db.exec(sql),
		runSync: (sql, ...params) => db.prepare(sql).run(...params),
		getFirstSync: <T>(sql: string, ...params: string[]) =>
			(db.prepare(sql).get(...params) as T | undefined) ?? null,
	};
	const repository = new DraftRepository(adapter);
	const destination = { hubId: "hub", sessionRef: "local:test" };
	const { io, objectives, service, thread } = boundary();
	const document = new DraftDocument(() => repository, destination);
	try {
		await service.open("local:test");
		document.edit("/goal objective");
		await submitGoalCommand(document, service);
		expect(objectives).toEqual(["objective"]);
		expect(repository.read(destination).unconfirmed).toBeNull();
		document.edit("ordinary draft");
		await submitGoalCommand(document, service, true);
		expect(objectives).toEqual(["objective", ""]);
		expect(repository.read(destination).draft).toBe("ordinary draft");
		thread.evener.capabilities.goal = false;
		await service.open("local:test");
		await expect(service.setGoal("blocked")).rejects.toThrow();
		expect(objectives).toHaveLength(2);
		thread.evener.capabilities.goal = true;
		await service.open("local:test");
		io.set = async () => {
			expect(repository.read(destination).unconfirmed).toBe("/goal uncertain");
			document.edit("newer draft");
			throw new Error("Lost acknowledgement");
		};
		document.edit("/goal uncertain");
		await expect(submitGoalCommand(document, service)).rejects.toThrow();
		const reopened = new DraftDocument(() => repository, destination);
		expect(reopened.getSnapshot().record).toMatchObject({
			draft: "newer draft",
			unconfirmed: "/goal uncertain",
		});
		await submitGoalCommand(reopened, service, true);
		expect(reopened.getSnapshot().record.unconfirmed).toBe("/goal uncertain");
	} finally {
		service.close();
		db.close();
	}
});

it.each([false, true])(
	"keeps malformed goal acknowledgment recoverable (clear: %s)",
	async (clear) => {
		const { db, document } = commandDraft();
		const { io, service } = boundary();
		let received = 0;
		try {
			await service.open("local:test");
			const draft = clear ? "ordinary draft" : "/goal objective";
			document.edit(draft);
			io.set = async () => {
				received++;
				return {} as never;
			};
			await expect(
				submitGoalCommand(document, service, clear),
			).rejects.toThrow();
			expect(document.getSnapshot().record).toMatchObject({
				draft: clear ? draft : "",
				unconfirmed: clear ? "/goal" : draft,
			});
			if (!clear) document.edit(draft);
			await submitGoalCommand(document, service, clear);
			expect(received).toBe(1);
		} finally {
			service.close();
			db.close();
		}
	},
);

function commandDraft() {
	const db = new DatabaseSync(":memory:");
	const repository = new DraftRepository({
		execSync: (sql) => db.exec(sql),
		runSync: (sql, ...params) => db.prepare(sql).run(...params),
		getFirstSync: <T>(sql: string, ...params: string[]) =>
			(db.prepare(sql).get(...params) as T | undefined) ?? null,
	});
	const destination = { hubId: "hub", sessionRef: "local:test" };
	return { db, document: new DraftDocument(() => repository, destination) };
}

it.each([
	[
		"/model SCRIPTED/ALTERNATE",
		"thread/model/set",
		{ modelProvider: "scripted", model: "alternate" },
	],
	[
		"/model alternate model",
		"thread/model/set",
		{ modelProvider: "scripted", model: "alternate" },
	],
	[
		"/reasoning-effort HIGH",
		"thread/reasoning-effort/set",
		{ reasoningEffort: "high" },
	],
	[
		"/reasoning-effort (default)",
		"thread/reasoning-effort/set",
		{ reasoningEffort: "" },
	],
	["/reasoning-effort", "thread/reasoning-effort/set", { reasoningEffort: "" }],
	[
		"/reasoning-effort none (off)",
		"thread/reasoning-effort/set",
		{ reasoningEffort: "none" },
	],
])(
	"resolves the web enum argument for %s before checkpointing",
	async (text, method, expected) => {
		const { db, document } = commandDraft();
		const { io, service } = boundary();
		let received = 0;
		try {
			await service.open("local:test");
			document.edit(text);
			io.lifecycle = async (actual, params) => {
				received++;
				expect(actual).toBe(method);
				expect(params).toEqual({ ref: "local:test", ...expected });
				expect(document.getSnapshot().record.unconfirmed).toBe(text);
				return {};
			};
			await submitComposerCommand(document, service, {
				...commandContext,
				reasoning: () => ({
					supportsReasoning: true,
					reasoningEffortLevels: ["none", "high"],
				}),
			});
			expect(received).toBe(1);
			expect(document.getSnapshot().record).toMatchObject({
				draft: "",
				unconfirmed: null,
			});
		} finally {
			service.close();
			db.close();
		}
	},
);

it.each([
	"/model missing",
	"/model",
	"/reasoning-effort high",
	"/reasoning-effort",
])("keeps invalid enum input editable: %s", async (text) => {
	const { db, document } = commandDraft();
	const { service } = boundary();
	try {
		await service.open("local:test");
		document.edit(text);
		await expect(
			submitComposerCommand(document, service, {
				...commandContext,
				reasoning: () => ({
					supportsReasoning: true,
					reasoningEffortLevels: [],
				}),
			}),
		).rejects.toThrow();
		expect(document.getSnapshot().record).toMatchObject({
			draft: text,
			unconfirmed: null,
		});
	} finally {
		service.close();
		db.close();
	}
});

it.each(["draft", "binding"])(
	"discards a model lookup when its %s changes",
	async (change) => {
		const { db, document } = commandDraft();
		const { io, service } = boundary();
		let current = true;
		try {
			await service.open("local:test");
			document.edit("/model scripted/alternate");
			io.models = async () => {
				if (change === "draft") document.edit("newer draft");
				else current = false;
				return { data: [{ provider: "scripted", model: "alternate" }] };
			};
			io.lifecycle = async () => {
				throw new Error("Stale mutation reached transport");
			};
			expect(
				await submitComposerCommand(document, service, {
					...commandContext,
					isCurrent: () => current,
				}),
			).toBeNull();
			expect(document.getSnapshot().record).toMatchObject({
				draft: change === "draft" ? "newer draft" : "/model scripted/alternate",
				unconfirmed: null,
			});
		} finally {
			service.close();
			db.close();
		}
	},
);

it.each(["advertised", "turn"])(
	"preserves the %s active turn across stale hydration and older completion",
	async (source) => {
		const { thread, io, service } = boundary();
		if (source === "advertised") thread.evener.activeTurnId = "first";
		else
			thread.turns = [
				{ id: "first", status: "inProgress", itemsView: "full", items: [] },
			];
		const store = createConversationStore();
		const activity = createActivityStore().getState();
		try {
			await store.getState().openProjected(service, activity, "local:test");
			expect(store.getState().conversation?.activeTurnId).toBe("first");
			let finish!: () => void;
			let entered!: () => void;
			const reading = new Promise<void>((resolve) => {
				entered = resolve;
			});
			io.read = () =>
				new Promise((resolve) => {
					finish = () => resolve({ thread: structuredClone(thread) });
					entered();
				});
			const refresh = store.getState().rehydrate(service, activity);
			await reading;
			store.getState().applyNotification({
				method: "turn/started",
				params: {
					ref: "local:test",
					threadId: "thread",
					turn: {
						id: "second",
						status: "inProgress",
						itemsView: "full",
						items: [],
					},
				},
			});
			store.getState().applyNotification({
				method: "turn/completed",
				params: {
					ref: "local:test",
					threadId: "thread",
					turnId: "first",
					turn: {
						id: "first",
						status: "completed",
						itemsView: "full",
						items: [],
					},
				},
			});
			expect(store.getState().conversation?.activeTurnId).toBe("second");
			finish();
			await refresh;
			expect(store.getState().conversation?.activeTurnId).toBe("second");
			store.getState().applyNotification({
				method: "turn/completed",
				params: {
					ref: "local:test",
					threadId: "thread",
					turnId: "second",
					turn: {
						id: "second",
						status: "completed",
						itemsView: "full",
						items: [],
					},
				},
			});
			expect(store.getState().conversation?.activeTurnId).toBeUndefined();
		} finally {
			store.getState().close();
			service.close();
		}
	},
);

it.each([
	["/steer sentinel", "turn/steer", [{ type: "text", text: "sentinel" }]],
	["/queue sentinel", "turn/queue", [{ type: "text", text: "sentinel" }]],
	["/drain-as-steer", "turn/drainAsSteer", []],
	["/interrupt", "turn/interrupt", undefined],
])(
	"routes %s with acknowledged delivery and queue guards",
	async (text, method, input) => {
		const { db, document } = commandDraft();
		const { io, service, thread } = boundary();
		thread.evener.capabilities.steer = true;
		thread.evener.capabilities.queue = true;
		thread.evener.capabilities.interrupt = true;
		let received = 0;
		try {
			await service.open("local:test");
			io.lifecycle = async (actual, raw) => {
				const params = raw as { clientMutationId: string };
				received++;
				expect(actual).toBe(method);
				expect(raw).toEqual({
					ref: "local:test",
					expectedInstanceId: "instance",
					clientMutationId: expect.any(String),
					...(input ? { input } : {}),
					...(method === "turn/drainAsSteer"
						? { expectedQueueRevision: 7 }
						: {}),
				});
				expect(document.getSnapshot().record.unconfirmed).toBe(text);
				return {
					receipt: {
						clientMutationId: params.clientMutationId,
						instanceId: "instance",
						threadId: "thread",
						disposition: "applied",
						projectionState:
							method === "turn/interrupt" ? "reflected" : "pending",
						...(method === "turn/queue"
							? { queueEntryIds: ["entry"] }
							: { turnId: "turn" }),
					},
				};
			};
			document.edit(text);
			await submitComposerCommand(document, service, {
				...commandContext,
				turn: () =>
					method === "turn/interrupt"
						? null
						: { activeTurnId: "turn", queue: { revision: 7 } },
			});
			expect(received).toBe(1);
			expect(document.getSnapshot().record).toMatchObject({
				draft: "",
				unconfirmed: null,
			});
		} finally {
			service.close();
			db.close();
		}
	},
);

it.each(["/steer sentinel", "/queue sentinel", "/drain-as-steer"])(
	"retains %s without a live turn instead of sending chat",
	async (text) => {
		const { db, document } = commandDraft();
		const { service, thread } = boundary();
		thread.evener.capabilities.steer = true;
		thread.evener.capabilities.queue = true;
		try {
			await service.open("local:test");
			document.edit(text);
			await expect(
				submitComposerCommand(document, service, commandContext),
			).rejects.toThrow();
			expect(document.getSnapshot().record).toMatchObject({
				draft: text,
				unconfirmed: null,
			});
		} finally {
			service.close();
			db.close();
		}
	},
);

it("does not resurrect a turn that completes during a read before its start was observed", async () => {
	const { thread, io, service } = boundary();
	const store = createConversationStore();
	const activity = createActivityStore().getState();
	try {
		await store.getState().openProjected(service, activity, "local:test");
		thread.evener.activeTurnId = "missed-start";
		io.read = async () => {
			const stale = structuredClone(thread);
			store.getState().applyNotification({
				method: "turn/completed",
				params: {
					ref: "local:test",
					threadId: "thread",
					turnId: "missed-start",
					turn: {
						id: "missed-start",
						status: "completed",
						itemsView: "full",
						items: [],
					},
				},
			});
			return { thread: stale };
		};
		await store.getState().rehydrate(service, activity);
		expect(store.getState().conversation?.activeTurnId).toBeUndefined();
	} finally {
		store.getState().close();
		service.close();
	}
});

it.each(["tasks", "status", "copy-id", "project"] as const)(
	"runs /%s locally without a delivery checkpoint",
	async (id) => {
		const { db, document } = commandDraft();
		const { service } = boundary();
		try {
			for (const outcome of ["success", "newer", "failure"] as const) {
				const text = `/${id}`;
				document.edit(text);
				const operation = submitComposerCommand(document, service, {
					...commandContext,
					local: async (actual) => {
						expect(actual).toBe(id);
						expect(document.getSnapshot().record).toMatchObject({
							draft: text,
							unconfirmed: null,
						});
						if (outcome === "failure")
							throw new Error("Platform operation failed");
						if (outcome === "newer") document.edit("newer draft");
					},
				});
				if (outcome === "failure") await expect(operation).rejects.toThrow();
				else expect(await operation).toBe(id);
				expect(document.getSnapshot().record).toMatchObject({
					draft:
						outcome === "failure"
							? text
							: outcome === "newer"
								? "newer draft"
								: "",
					unconfirmed: null,
				});
			}
		} finally {
			service.close();
			db.close();
		}
	},
);

it.each(["current", "obsolete", "failure"])(
	"acknowledges an aside before navigation: %s",
	async (outcome) => {
		const { db, document } = commandDraft();
		const { io, thread, service } = boundary();
		thread.evener.capabilities.forkFromTurn = true;
		let current = true;
		const opened: string[] = [];
		try {
			await service.open("local:test");
			document.edit("/aside");
			io.lifecycle = async (method, params) => {
				expect(method).toBe("thread/fork");
				expect(params).toEqual({
					ref: "local:test",
					sourceTurnId: "",
					aside: true,
				});
				expect(document.getSnapshot().record.unconfirmed).toBe("/aside");
				document.edit("newer parent draft");
				if (outcome === "failure") throw new Error("Lost fork acknowledgement");
				if (outcome === "obsolete") current = false;
				return {
					thread: {
						id: "child",
						sessionId: "child",
						evener: { ref: "local:child" },
					},
				};
			};
			const operation = submitComposerCommand(document, service, {
				...commandContext,
				isCurrent: () => current,
				openAside: (ref) => {
					expect(document.getSnapshot().record.unconfirmed).toBeNull();
					opened.push(ref);
				},
			});
			if (outcome === "failure") await expect(operation).rejects.toThrow();
			else expect(await operation).toBe("aside");
			expect(opened).toEqual(outcome === "current" ? ["local:child"] : []);
			expect(document.getSnapshot().record).toMatchObject({
				draft: "newer parent draft",
				unconfirmed: outcome === "failure" ? "/aside" : null,
			});
		} finally {
			service.close();
			db.close();
		}
	},
);

it("blocks unavailable aside creation before the wire and rejects a parent returned as its own fork", async () => {
	const { io, thread, service } = boundary();
	let requests = 0;
	io.lifecycle = async () => {
		requests++;
		return { thread };
	};
	try {
		await service.open("local:test");
		await expect(service.forkAside()).rejects.toThrow();
		expect(requests).toBe(0);
		thread.evener.capabilities.forkFromTurn = true;
		await service.open("local:test");
		await expect(service.forkAside()).rejects.toThrow();
		expect(requests).toBe(1);
	} finally {
		service.close();
	}
});

it("forks from the selected transcript entry with input deferred and leaves the parent open", async () => {
	const { io, thread, service } = boundary();
	thread.evener.capabilities.forkFromTurn = true;
	const calls: unknown[] = [];
	io.lifecycle = async (method, params) => {
		calls.push({ method, params });
		return {
			thread: { evener: { ref: "local:child" } },
			originalInput: "selected input",
		};
	};
	try {
		await service.open("local:test");
		await expect(service.forkFromTurn(5)).resolves.toMatchObject({
			thread: { evener: { ref: "local:child" } },
			originalInput: "selected input",
		});
		expect(calls).toEqual([
			{
				method: "thread/fork",
				params: { ref: "local:test", sourceTurnId: "5", deferInput: true },
			},
		]);
		await service.forkFromTurn(7);
		expect(calls[1]).toMatchObject({
			params: { ref: "local:test", sourceTurnId: "7" },
		});
	} finally {
		service.close();
	}
});

it("refuses unavailable or invalid ordinary forks before transport", async () => {
	const { io, thread, service } = boundary();
	let calls = 0;
	io.lifecycle = async () => {
		calls++;
		return {};
	};
	try {
		await service.open("local:test");
		await expect(service.forkFromTurn(5)).rejects.toThrow();
		thread.evener.capabilities.forkFromTurn = true;
		await service.open("local:test");
		for (const index of [
			0,
			-1,
			1.5,
			Number.NaN,
			Infinity,
			Number.MAX_SAFE_INTEGER + 1,
		])
			await expect(service.forkFromTurn(index)).rejects.toThrow();
		expect(calls).toBe(0);
	} finally {
		service.close();
	}
});

it.each(["", "local:test"])(
	"rejects a fork without a new child identity: %s",
	async (ref) => {
		const { io, thread, service } = boundary();
		thread.evener.capabilities.forkFromTurn = true;
		let calls = 0;
		io.lifecycle = async () => {
			calls++;
			return { thread: { evener: { ref } } };
		};
		try {
			await service.open("local:test");
			await expect(service.forkFromTurn(5)).rejects.toThrow();
			expect(calls).toBe(1);
		} finally {
			service.close();
		}
	},
);

it("installs a clear replacement without allowing an older read to restore its transcript", async () => {
	const { io, thread, service } = boundary();
	thread.evener.capabilities.clear = true;
	thread.evener.goal = {
		objective: "old goal",
		status: "active",
		iterations: 1,
	};
	thread.turns = [
		{
			id: "old-turn",
			status: "completed",
			itemsView: "full",
			items: [{ id: "old-item", type: "agentMessage", text: "old transcript" }],
		},
	];
	const store = createConversationStore();
	const sink = createActivityStore().getState();
	let releaseClear!: () => void;
	let releaseRead!: () => void;
	let clearEntered!: () => void;
	let readEntered!: () => void;
	const clearing = new Promise<void>((resolve) => {
		clearEntered = resolve;
	});
	const reading = new Promise<void>((resolve) => {
		readEntered = resolve;
	});
	try {
		await store.getState().openProjected(service, sink, "local:test");
		expect(store.getState().conversation?.items.length).toBeGreaterThan(0);
		const replacement = structuredClone(thread);
		replacement.id = "replacement-thread";
		replacement.evener.instanceId = "replacement-instance";
		delete replacement.evener.goal;
		replacement.turns = [];
		io.lifecycle = (method, raw) =>
			new Promise((resolve) => {
				const params = raw as { clientMutationId: string };
				expect(method).toBe("thread/clear");
				expect(raw).toEqual({
					ref: "local:test",
					expectedInstanceId: "instance",
					clientMutationId: expect.any(String),
				});
				releaseClear = () =>
					resolve({
						ref: "local:test",
						thread: replacement,
						receipt: {
							clientMutationId: params.clientMutationId,
							disposition: "applied",
							threadId: "replacement-thread",
							instanceId: "replacement-instance",
							projectionState: "reflected",
						},
					});
				clearEntered();
			});
		const clear = service.clear();
		await clearing;
		io.read = () =>
			new Promise((resolve) => {
				releaseRead = () => resolve({ thread: structuredClone(thread) });
				readEntered();
			});
		const oldRead = store.getState().rehydrate(service, sink);
		await reading;
		releaseClear();
		const response = await clear;
		await store
			.getState()
			.openProjected(service, sink, "local:test", service.adoptClear(response));
		releaseRead();
		await oldRead;
		expect(store.getState().conversation).toMatchObject({
			id: "replacement-thread",
			instanceId: "replacement-instance",
			items: [],
			goal: null,
		});
		io.lifecycle = async (method, params) => {
			expect(method).toBe("thread/clear");
			expect(params).toMatchObject({
				expectedInstanceId: "replacement-instance",
			});
			throw new Error("stop at transport");
		};
		await expect(service.clear()).rejects.toThrow("stop at transport");
		service.close();
		expect(() => service.adoptClear(response)).toThrow();
	} finally {
		store.getState().close();
		service.close();
	}
});

it.each(["accepted", "uncertain", "wrong receipt", "wrong ref"])(
	"checkpoints clear and validates its replacement: %s",
	async (outcome) => {
		const { db, document } = commandDraft();
		const { io, thread, service } = boundary();
		thread.evener.capabilities.clear = true;
		let applied = 0;
		try {
			await service.open("local:test");
			document.edit("/clear");
			io.lifecycle = async (method, raw) => {
				const params = raw as { clientMutationId: string };
				expect(method).toBe("thread/clear");
				expect(document.getSnapshot().record.unconfirmed).toBe("/clear");
				document.edit("newer draft");
				if (outcome === "uncertain") throw new Error("Lost acknowledgement");
				const replacement = structuredClone(thread);
				replacement.id = "new-thread";
				replacement.evener.instanceId = "new-instance";
				return {
					ref: outcome === "wrong ref" ? "local:other" : "local:test",
					thread: replacement,
					receipt: {
						clientMutationId:
							outcome === "wrong receipt" ? "wrong" : params.clientMutationId,
						disposition: "applied",
						threadId: "new-thread",
						instanceId: "new-instance",
						projectionState: "reflected",
					},
				};
			};
			const operation = submitComposerCommand(document, service, {
				...commandContext,
				cleared: (response) => {
					expect(response.thread.evener.instanceId).toBe("new-instance");
					expect(document.getSnapshot().record.unconfirmed).toBeNull();
					applied++;
				},
			});
			if (outcome === "accepted") expect(await operation).toBe("clear");
			else await expect(operation).rejects.toThrow();
			expect(applied).toBe(outcome === "accepted" ? 1 : 0);
			expect(document.getSnapshot().record).toMatchObject({
				draft: "newer draft",
				unconfirmed: outcome === "accepted" ? null : "/clear",
			});
		} finally {
			service.close();
			db.close();
		}
	},
);
