import { DatabaseSync } from "node:sqlite";
import { expect, it } from "vitest";
import type { Thread } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import {
	type ConversationClientLike,
	createConversationService,
} from "../../mobile/src/services/conversation";
import { DraftLibrary } from "./draftLibrary";
import { DraftRepository } from "./draftRepository";
import { ForkActions } from "./forkActions";
import { forkCheckpoints } from "./forkCheckpointRepository";
import { LocationRepository, restoredStack } from "./location";

function savedLocations() {
	let value: string | null = null;
	let fail = false;
	const storage = {
		getItemSync: () => value,
		setItemSync: (_key: string, next: string) => {
			if (fail) throw Error("storage unavailable");
			value = next;
		},
		removeItemSync: () => {
			value = null;
		},
	};
	return {
		storage,
		repository: new LocationRepository(storage),
		set fail(next: boolean) {
			fail = next;
		},
	};
}

const target = { instanceId: "instance", entryIndex: 7, preview: "selected" };
async function boundary() {
	const db = new DatabaseSync(":memory:");
	let failDraft = false,
		failJournal = false,
		current = true,
		instance = "instance",
		id = 0;
	const values = new Map<string, unknown>();
	const journal = forkCheckpoints("hub", "local:parent", {
		createId: () => String(++id),
		get: (key) => values.get(key),
		set: (key, value) => {
			if (failJournal) throw Error("storage failure");
			values.set(key, value);
		},
		deleteIf: (key, expected) => {
			if (JSON.stringify(values.get(key)) !== JSON.stringify(expected))
				return false;
			return values.delete(key);
		},
	});
	const repository = new DraftRepository({
		execSync: (sql) => db.exec(sql),
		runSync: (sql, ...params) => {
			if (failDraft) throw Error("draft storage failure");
			return db.prepare(sql).run(...params);
		},
		getFirstSync: <T>(sql: string, ...params: string[]) =>
			(db.prepare(sql).get(...params) as T | undefined) ?? null,
	});
	const drafts = new DraftLibrary(() => repository);
	const parent: Thread = {
		id: "parent",
		sessionId: "parent",
		preview: "",
		ephemeral: false,
		modelProvider: "scripted",
		createdAt: 1,
		updatedAt: 1,
		status: { type: "idle" },
		cwd: "/owned",
		cliVersion: "test",
		source: "local",
		turns: [],
		evener: {
			ref: "local:parent",
			instanceId: "instance",
			queue: { revision: 0 },
			capabilities: {
				send: true,
				steer: false,
				interrupt: false,
				compact: false,
				clear: false,
				forkFromTurn: true,
				shutdown: false,
				changeModel: false,
				changeVisionModel: false,
				queue: false,
				goal: false,
				rename: false,
			},
		},
	};
	let requests = 0;
	const result = {
		thread: {
			...parent,
			id: "child",
			sessionId: "child",
			evener: { ...parent.evener, ref: "local:child" },
		},
		originalInput: "original input",
	};
	const io = { fork: async () => result };
	const service = createConversationService({
		request: async (method, params) => {
			if (method === "thread/read") return { thread: parent };
			if (method === "thread/fork") {
				requests++;
				expect(params).toEqual({
					ref: "local:parent",
					sourceTurnId: "7",
					deferInput: true,
				});
				expect(journal.load()).toMatchObject({ target });
				return io.fork();
			}
			throw Error(`Unexpected ${method}`);
		},
		onNotification: () => () => {},
	} as ConversationClientLike);
	await service.open("local:parent");
	const actions = () =>
		new ForkActions(
			journal,
			service,
			drafts,
			"hub",
			() => current,
			() => instance,
		);
	return {
		journal,
		drafts,
		io,
		result,
		actions,
		get requests() {
			return requests;
		},
		set failDraft(value: boolean) {
			failDraft = value;
		},
		set failJournal(value: boolean) {
			failJournal = value;
		},
		set current(value: boolean) {
			current = value;
		},
		set instance(value: string) {
			instance = value;
		},
		close: () => {
			service.close();
			db.close();
		},
	};
}

it("persists a deferred fork and its composer before allowing navigation", async () => {
	const b = await boundary();
	try {
		const actions = b.actions(),
			child = await actions.create(target);
		expect(child?.ref).toBe("local:child");
		expect(b.journal.load()).toMatchObject({
			child: { ref: "local:child", input: "original input" },
			draftPrepared: true,
		});
		expect(
			b.drafts.open({ hubId: "hub", sessionRef: "local:child" }).getSnapshot()
				.record.draft,
		).toBe("original input");
		expect(b.requests).toBe(1);
		const location = savedLocations();
		expect(
			actions.finish(location.repository, (destination) => {
				expect(
					new LocationRepository(location.storage).read(["hub"])?.conversation
						?.ref,
				).toBe(destination.ref);
				expect(b.journal.load()?.child?.ref).toBe(destination.ref);
			}),
		).toBe(true);
		expect(b.journal.load()).toBeNull();
	} finally {
		b.close();
	}
});

it("retains an acknowledged child through destination storage and navigation failures without replay", async () => {
	const b = await boundary();
	try {
		const actions = b.actions();
		await actions.create(target);
		const location = savedLocations();
		location.repository.save({
			hubId: "hub",
			conversation: { ref: "local:parent", title: "Parent" },
			fork: target,
		});
		location.fail = true;
		let navigations = 0;
		expect(
			actions.finish(location.repository, () => {
				navigations++;
			}),
		).toBe(false);
		expect(navigations).toBe(0);
		expect(
			restoredStack(
				new LocationRepository(location.storage).read(["hub"]),
			).routes.at(-1)?.name,
		).toBe("Fork");
		expect(b.journal.load()?.child?.ref).toBe("local:child");
		location.fail = false;
		expect(
			actions.finish(location.repository, () => {
				navigations++;
				throw Error("navigation unavailable");
			}),
		).toBe(false);
		expect(navigations).toBe(1);
		expect(b.journal.load()?.child?.ref).toBe("local:child");
		expect(
			restoredStack(
				new LocationRepository(location.storage).read(["hub"]),
			).routes.at(-1),
		).toMatchObject({ name: "Conversation", params: { ref: "local:child" } });
		actions.dispose();
		const restored = b.actions();
		expect(restored.openChild()?.ref).toBe("local:child");
		expect(
			restored.finish(location.repository, () => {
				navigations++;
			}),
		).toBe(true);
		expect(navigations).toBe(2);
		expect(b.journal.load()).toBeNull();
		expect(b.requests).toBe(1);
	} finally {
		b.close();
	}
});

it("restores an unknown fork without replay and requires an explicit discard before another request", async () => {
	const b = await boundary();
	try {
		b.io.fork = async () => {
			throw Error("lost acknowledgement");
		};
		const first = b.actions();
		expect(await first.create(target)).toBeNull();
		first.dispose();
		const restored = b.actions(),
			checkpoint = b.journal.load();
		expect(checkpoint).toMatchObject({ target });
		expect(restored.openChild()).toBeNull();
		expect(await restored.create(target)).toBeNull();
		expect(b.requests).toBe(1);
		if (!checkpoint) throw Error("missing checkpoint");
		expect(restored.discardUnknown(checkpoint)).toBe(true);
		b.io.fork = async () => b.result;
		expect(await restored.create(target)).toMatchObject({ ref: "local:child" });
		expect(b.requests).toBe(2);
	} finally {
		b.close();
	}
});

it("keeps late acknowledged child identity after the source screen is abandoned", async () => {
	const b = await boundary();
	try {
		let resolve: (value: typeof b.result) => void = () => {};
		b.io.fork = () =>
			new Promise((done) => {
				resolve = done;
			});
		const first = b.actions(),
			request = first.create(target);
		b.current = false;
		first.dispose();
		resolve(b.result);
		expect(await request).toBeNull();
		expect(b.journal.load()).toMatchObject({ child: { ref: "local:child" } });
		b.current = true;
		expect(b.actions().openChild()?.ref).toBe("local:child");
		expect(b.requests).toBe(1);
	} finally {
		b.close();
	}
});

it("retries an acknowledgement storage failure without repeating the fork", async () => {
	const b = await boundary();
	try {
		b.io.fork = async () => {
			b.failJournal = true;
			return b.result;
		};
		const actions = b.actions();
		expect(await actions.create(target)).toBeNull();
		expect(actions.getSnapshot().storageUnavailable).toBe(true);
		expect(b.journal.load()?.child).toBeUndefined();
		b.failJournal = false;
		actions.retryStorage();
		expect(actions.openChild()?.ref).toBe("local:child");
		expect(b.requests).toBe(1);
	} finally {
		b.close();
	}
});

it("retains the acknowledged child until its draft is saved and preserves later edits", async () => {
	const b = await boundary();
	try {
		b.failDraft = true;
		const actions = b.actions();
		expect(await actions.create(target)).toBeNull();
		expect(b.journal.load()).toMatchObject({ child: { ref: "local:child" } });
		expect(b.journal.load()?.draftPrepared).toBeUndefined();
		b.failDraft = false;
		expect(actions.openChild()?.ref).toBe("local:child");
		const draft = b.drafts.open({ hubId: "hub", sessionRef: "local:child" });
		draft.edit("");
		actions.dispose();
		expect(b.actions().openChild()?.ref).toBe("local:child");
		expect(draft.getSnapshot().record.draft).toBe("");
		expect(b.requests).toBe(1);
	} finally {
		b.close();
	}
});

it("does not overwrite an existing child's draft or send from a replaced source instance", async () => {
	const b = await boundary();
	try {
		const draft = b.drafts.open({ hubId: "hub", sessionRef: "local:child" });
		draft.edit("newer child draft");
		b.instance = "replacement";
		const actions = b.actions();
		expect(await actions.create(target)).toBeNull();
		expect(b.requests).toBe(0);
		b.instance = target.instanceId;
		expect(await actions.create(target)).toMatchObject({ ref: "local:child" });
		expect(draft.getSnapshot().record.draft).toBe("newer child draft");
	} finally {
		b.close();
	}
});
