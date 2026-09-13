import { describe, expect, it } from "vitest";
import type {
	AnyNotification,
	KeybindingsOverrides,
	KeybindingsRule,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import type {
	KeybindingDraftCheckpoint,
	KeybindingDraftStorage,
} from "./keybindingDraftRepository";
import { NativePreferences } from "./nativePreferences";

const getMethod = "evener/settings/keybindings/get";
const patchMethod = "evener/settings/keybindings/patch";
const rules: KeybindingsRule[] = [
	{ action: "composer.focus", chord: "Meta+P" },
];

function fixture() {
	let stored: unknown = null;
	let id = 0;
	let failSave = false;
	const storage: KeybindingDraftStorage = {
		createId: () => String(++id),
		load: () => structuredClone(stored),
		save: (value) => {
			if (failSave) throw new Error("disk unavailable");
			stored = structuredClone(value);
		},
		remove: () => {
			stored = null;
		},
		removeIf: (value) => {
			if (JSON.stringify(value) === JSON.stringify(stored)) stored = null;
		},
	};
	const requests: { method: string; params: unknown }[] = [];
	const listeners = new Set<(value: AnyNotification) => void>();
	const handlers = new Map<string, (params: unknown) => unknown>([
		[getMethod, () => ({ version: 1, revision: 3, rules: [] })],
		[patchMethod, () => ({ version: 1, revision: 4, rules })],
	]);
	const client = {
		request: async (method: string, params: unknown) => {
			requests.push({ method, params });
			return handlers.get(method)?.(params);
		},
		onNotification: (listener: (value: AnyNotification) => void) => {
			listeners.add(listener);
			return () => {
				listeners.delete(listener);
			};
		},
	} as ConversationClientLike;
	return {
		storage,
		requests,
		handlers,
		failSave: () => {
			failSave = true;
		},
		allowSave: () => {
			failSave = false;
		},
		corrupt: () => {
			stored = { invalid: true };
		},
		create: () =>
			new NativePreferences(
				client,
				{ keybindingsSettings: true, transcriptDisplaySettings: false },
				undefined,
				storage,
			),
		emit: (params: KeybindingsOverrides) => {
			for (const listener of listeners)
				listener({ method: "evener/settings/keybindings/changed", params });
		},
		patches: () => requests.filter(({ method }) => method === patchMethod),
	};
}

describe("keybinding draft recovery", () => {
	it("does not use a read started before a failed write to resolve its outcome", async () => {
		const f = fixture();
		const model = f.create();
		await model.refresh();
		const pending = Promise.withResolvers<unknown>();
		f.handlers.set(getMethod, () => pending.promise);
		const read = model.refresh();
		f.handlers.set(patchMethod, () => {
			throw new Error("lost reply");
		});
		await expect(model.saveKeybindings(rules)).rejects.toThrow();
		pending.resolve({ version: 1, revision: 3, rules: [] });
		await read;
		expect(model.getSnapshot().keybindings).toMatchObject({
			loading: false,
			writeUncertain: true,
			draft: { rules },
		});
		expect(f.storage.load()).toMatchObject({ writeUncertain: true });
	});
	it("does not dispatch after a saving notification disposes the model", async () => {
		const f = fixture();
		const model = f.create();
		await model.refresh();
		model.subscribe(() => {
			if (model.getSnapshot().keybindings.saving) model.dispose();
		});
		await expect(model.saveKeybindings(rules)).rejects.toThrow();
		expect(f.patches()).toEqual([]);
		expect(f.storage.load()).toMatchObject({ rules, writeUncertain: true });
	});
	it("persists intent before dispatch and restores the proposal without replay", async () => {
		const f = fixture();
		const pending = Promise.withResolvers<unknown>();
		f.handlers.set(patchMethod, () => {
			expect(f.storage.load()).toMatchObject({
				baseRevision: 3,
				rules,
				writeUncertain: true,
			});
			return pending.promise;
		});
		const model = f.create();
		await model.refresh();
		await model.editKeybindings(rules);
		const save = model.saveKeybindings();
		pending.reject(new Error("lost reply"));
		await expect(save).rejects.toThrow();
		model.dispose();
		const restored = f.create();
		expect(restored.getSnapshot().keybindings.writeUncertain).toBe(true);
		await restored.refresh();
		expect(restored.getSnapshot().keybindings).toMatchObject({
			draft: { revision: 3, rules },
			writeUncertain: false,
		});
		expect(f.storage.load()).toMatchObject({ rules, writeUncertain: false });
		expect(f.patches()).toEqual([
			{
				method: patchMethod,
				params: { expectedRevision: 3, config: { version: 1, rules } },
			},
		]);
	});

	it("does not dispatch if writing the durable intent fails", async () => {
		const f = fixture();
		const model = f.create();
		await model.refresh();
		await model.editKeybindings(rules);
		f.failSave();
		await expect(model.saveKeybindings()).rejects.toThrow();
		expect(f.patches()).toEqual([]);
		expect(model.getSnapshot().keybindings.storageUnavailable).toBe(true);
		f.allowSave();
		await model.refresh();
		expect(model.getSnapshot().keybindings).toMatchObject({
			storageUnavailable: false,
			draft: { rules },
			saving: false,
		});
	});

	it("preserves the proposal through notifications and requires the reviewed revision", async () => {
		const f = fixture();
		const model = f.create();
		await model.refresh();
		await model.editKeybindings(rules);
		f.emit({ version: 1, revision: 4, rules: [] });
		expect(model.getSnapshot().keybindings).toMatchObject({
			draft: { revision: 3, rules },
			conflict: true,
		});
		await expect(model.saveKeybindings()).rejects.toThrow();
		await expect(model.rebaseKeybindingsDraft(3)).rejects.toThrow();
		expect(f.patches()).toEqual([]);
		await model.rebaseKeybindingsDraft(4);
		f.handlers.set(patchMethod, () => ({ version: 1, revision: 5, rules }));
		await model.saveKeybindings();
		expect(f.patches()[0]?.params).toEqual({
			expectedRevision: 4,
			config: { version: 1, rules },
		});
		expect(f.storage.load()).toBeNull();
	});

	it("cannot erase a replacement checkpoint with a disposed instance's late acknowledgement", async () => {
		const f = fixture();
		const pending = Promise.withResolvers<unknown>();
		f.handlers.set(patchMethod, () => pending.promise);
		const model = f.create();
		await model.refresh();
		await model.editKeybindings(rules);
		const save = model.saveKeybindings();
		model.dispose();
		const replacement: KeybindingDraftCheckpoint = {
			id: "replacement",
			baseRevision: 4,
			rules: [],
			writeUncertain: false,
		};
		f.storage.save(replacement);
		pending.resolve({ version: 1, revision: 4, rules });
		await save;
		expect(f.storage.load()).toEqual(replacement);
	});

	it("retains unknown intent across read failures and fallback defaults", async () => {
		const f = fixture();
		const model = f.create();
		await model.refresh();
		await model.editKeybindings(rules);
		f.handlers.set(patchMethod, () => {
			throw new Error("post-rename durability failure");
		});
		await expect(model.saveKeybindings()).rejects.toThrow();
		f.handlers.set(getMethod, () => {
			throw new Error("offline");
		});
		await model.refresh();
		expect(model.getSnapshot().keybindings.writeUncertain).toBe(true);
		f.handlers.set(getMethod, () => ({
			version: 1,
			revision: 0,
			rules: [],
			loadError: "corrupt file",
		}));
		await model.refresh();
		expect(model.getSnapshot().keybindings).toMatchObject({
			draft: { rules },
			writeUncertain: true,
		});
		await expect(model.discardKeybindingsDraft()).rejects.toThrow();
		expect(f.patches()).toHaveLength(1);
	});

	it("blocks changes when the saved draft is malformed", async () => {
		const f = fixture();
		f.corrupt();
		const model = f.create();
		await model.refresh();
		expect(model.getSnapshot().keybindings.storageUnavailable).toBe(true);
		await expect(model.saveKeybindings(rules)).rejects.toThrow();
		expect(f.patches()).toEqual([]);
	});
});
