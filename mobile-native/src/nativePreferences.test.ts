import { describe, expect, it, vi } from "vitest";
import type {
	AnyNotification,
	KeybindingsOverrides,
	TranscriptDisplayDefaults,
	TranscriptDisplayStore,
	TranscriptDraftCheckpoint,
} from "@evener/appwire-client";
import { toWireConfig, WireError } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { NativePreferences } from "./nativePreferences";

const features = { keybindingsSettings: true, transcriptDisplaySettings: true };
const keybindings: KeybindingsOverrides = {
	version: 1,
	revision: 3,
	rules: [{ action: "composer.focus", chord: "Meta+K" }],
};
const config = {
	version: 1 as const,
	content: { kind: "preset" as const, level: "chat" as const },
	advanced: {
		roundTimings: false,
		tokenCounts: false,
		estimatedCost: false,
		systemEvents: false,
		promptEvents: false,
		hookExits: "none" as const,
	},
};
const transcript: TranscriptDisplayDefaults = {
	desktop: { revision: 1, config: toWireConfig(config) },
	mobile: { revision: 4, config: toWireConfig(config) },
};

function fakeClient() {
	const listeners = new Set<(notification: AnyNotification) => void>();
	const requests: Array<{ method: string; params: unknown }> = [];
	const handlers = new Map<string, (params: unknown) => unknown>();
	const client = {
		request: vi.fn(async (method: string, params: unknown) => {
			requests.push({ method, params });
			return handlers.get(method)?.(params);
		}),
		onNotification: (listener: (notification: AnyNotification) => void) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		emit(notification: AnyNotification) {
			for (const listener of listeners) listener(notification);
		},
		handlers,
		requests,
	} as unknown as ConversationClientLike & {
		emit: (notification: AnyNotification) => void;
		handlers: typeof handlers;
		requests: typeof requests;
	};
	return client;
}

describe("NativePreferences", () => {
	it("gates unsupported domains without issuing reads or writes", async () => {
		const client = fakeClient();
		const model = new NativePreferences(client, {
			keybindingsSettings: false,
			transcriptDisplaySettings: false,
		});
		await model.refresh();
		expect(client.requests).toEqual([]);
		expect(model.getSnapshot().keybindings.support).toBe("unsupported");
		expect(model.getSnapshot().transcriptMobile.support).toBe("unsupported");
	});

	it("loads confirmed domains and applies matching changed notifications", async () => {
		const client = fakeClient();
		client.handlers.set("evener/settings/keybindings/get", () => keybindings);
		client.handlers.set(
			"evener/settings/transcriptDisplay/get",
			() => transcript,
		);
		const model = new NativePreferences(client, features);
		await model.refresh();
		expect(model.getSnapshot().keybindings.confirmed).toEqual(keybindings);
		expect(model.getSnapshot().transcriptMobile.confirmed?.revision).toBe(4);
		client.emit({
			method: "evener/settings/keybindings/changed",
			params: { ...keybindings, revision: 5, rules: [] },
		});
		expect(model.getSnapshot().keybindings.confirmed?.revision).toBe(5);
	});

	it("decodes the nested custom content used on the wire", async () => {
		const client = fakeClient();
		client.handlers.set("evener/settings/keybindings/get", () => keybindings);
		client.handlers.set("evener/settings/transcriptDisplay/get", () => ({
			...transcript,
			mobile: {
				revision: 7,
				config: {
					...toWireConfig(config),
					content: {
						kind: "custom",
						custom: {
							toolIntent: true,
							toolCalls: false,
							reasoning: true,
							expandByDefault: false,
						},
					},
				},
			},
		}));
		const model = new NativePreferences(client, features);
		await model.refresh();
		expect(
			model.getSnapshot().transcriptMobile.confirmed?.config.content,
		).toEqual({
			kind: "custom",
			toolIntent: true,
			toolCalls: false,
			reasoning: true,
			expandByDefault: false,
		});
	});

	it("uses the confirmed revision and retains the draft after an uncertain write", async () => {
		const client = fakeClient();
		client.handlers.set("evener/settings/keybindings/get", () => keybindings);
		client.handlers.set(
			"evener/settings/transcriptDisplay/get",
			() => transcript,
		);
		client.handlers.set("evener/settings/keybindings/patch", () => {
			throw new Error("connection lost after dispatch");
		});
		const model = new NativePreferences(client, features);
		await model.refresh();
		const rules = [{ action: "composer.focus", chord: "Meta+P" }];
		await expect(model.saveKeybindings(rules)).rejects.toThrow();
		expect(client.requests.at(-1)).toEqual({
			method: "evener/settings/keybindings/patch",
			params: { expectedRevision: 3, config: { version: 1, rules } },
		});
		expect(model.getSnapshot().keybindings.draft?.rules).toEqual(rules);
		expect(model.getSnapshot().keybindings.writeUncertain).toBe(true);
		await model.refresh();
		expect(model.getSnapshot().keybindings.writeUncertain).toBe(false);
		expect(
			client.requests.filter(
				(request) => request.method === "evener/settings/keybindings/patch",
			),
		).toHaveLength(1);
	});

	it("projects the keybindings draft without the store's internal generation stamp", async () => {
		const client = fakeClient();
		client.handlers.set("evener/settings/keybindings/get", () => keybindings);
		client.handlers.set(
			"evener/settings/transcriptDisplay/get",
			() => transcript,
		);
		const model = new NativePreferences(client, features);
		await model.refresh();
		const rules = [{ action: "composer.focus", chord: "Meta+P" }];
		await model.editKeybindings(rules);
		const draft = model.getSnapshot().keybindings.draft;
		expect(draft).toMatchObject({ version: 1, revision: 3, rules });
		// The store's own KeybindingsDraft carries a `generation` stamp
		// (staleness bookkeeping - see readyGenerationFence); the projected
		// PreferenceState<ConfirmedKeybindings> type has no such field, and
		// the projection must not leak it through structurally.
		expect(draft).not.toHaveProperty("generation");
	});

	it("ignores stale reads and notifications after disposal", async () => {
		const client = fakeClient();
		let resolve!: (value: KeybindingsOverrides) => void;
		client.handlers.set(
			"evener/settings/keybindings/get",
			() =>
				new Promise<KeybindingsOverrides>((done) => {
					resolve = done;
				}),
		);
		const model = new NativePreferences(client, features);
		const read = model.refresh();
		model.dispose();
		client.emit({
			method: "evener/settings/keybindings/changed",
			params: { ...keybindings, revision: 9, rules: [] },
		});
		resolve(keybindings);
		await read;
		expect(model.getSnapshot().keybindings.confirmed).toBeNull();
	});

	it("preserves the draft when a newer notification arrives during a pending save", async () => {
		const client = fakeClient();
		client.handlers.set("evener/settings/keybindings/get", () => keybindings);
		let resolve!: (value: KeybindingsOverrides) => void;
		client.handlers.set(
			"evener/settings/keybindings/patch",
			() =>
				new Promise<KeybindingsOverrides>((done) => {
					resolve = done;
				}),
		);
		const model = new NativePreferences(client, features);
		await model.refresh();
		const rules = [{ action: "composer.focus", chord: "Meta+P" }];
		const save = model.saveKeybindings(rules);
		await Promise.resolve();
		client.emit({
			method: "evener/settings/keybindings/changed",
			params: { ...keybindings, revision: 4, rules: [] },
		});
		expect(model.getSnapshot().keybindings.draft?.rules).toEqual(rules);
		expect(model.getSnapshot().keybindings.confirmed?.revision).toBe(4);
		resolve({ ...keybindings, revision: 5, rules });
		await save;
		expect(model.getSnapshot().keybindings).toMatchObject({
			saving: false,
			writeUncertain: false,
			draft: null,
			confirmed: { revision: 5, rules },
		});
	});
});

function deferred<T>() {
	let resolve: (value: T) => void = () => {
		throw new Error("not initialized");
	};
	const promise = new Promise<T>((done) => {
		resolve = done;
	});
	return { promise, resolve };
}
function setupDomain(domain: "keybindings" | "transcript") {
	const client = fakeClient();
	client.handlers.set("evener/settings/keybindings/get", () => keybindings);
	client.handlers.set(
		"evener/settings/transcriptDisplay/get",
		() => transcript,
	);
	const model = new NativePreferences(client, features);
	const isKeys = domain === "keybindings";
	const get = isKeys
		? "evener/settings/keybindings/get"
		: "evener/settings/transcriptDisplay/get";
	const patch = isKeys
		? "evener/settings/keybindings/patch"
		: "evener/settings/transcriptDisplay/patch";
	const old = isKeys ? keybindings : transcript;
	const result = isKeys
		? { ...keybindings, revision: 4, rules: [] }
		: { layout: "mobile" as const, revision: 5, config: toWireConfig(config) };
	const snapshot = () =>
		isKeys
			? model.getSnapshot().keybindings
			: model.getSnapshot().transcriptMobile;
	const save = () =>
		isKeys ? model.saveKeybindings([]) : model.saveTranscript(config);
	client.handlers.set(patch, () => result);
	return { client, model, get, patch, old, result, snapshot, save };
}
for (const domain of ["keybindings", "transcript"] as const) {
	it(`${domain}: an older GET cannot replace an acknowledged write`, async () => {
		const f = setupDomain(domain);
		await f.model.refresh();
		const held = deferred<unknown>();
		f.client.handlers.set(f.get, () => held.promise);
		const read = f.model.refresh();
		await f.save();
		held.resolve(f.old);
		await read;
		expect(f.snapshot().confirmed?.revision).toBe(f.result.revision);
		expect(f.snapshot().loading).toBe(false);
	});
	it(`${domain}: refresh during a pending write preserves its draft and settles the reply`, async () => {
		const f = setupDomain(domain);
		await f.model.refresh();
		const held = deferred<unknown>();
		f.client.handlers.set(f.patch, () => held.promise);
		const save = f.save();
		await f.model.refresh();
		expect(f.snapshot().saving).toBe(true);
		expect(f.snapshot().draft).not.toBeNull();
		held.resolve(f.result);
		await save;
		expect(f.snapshot().saving).toBe(false);
		expect(f.snapshot().confirmed?.revision).toBe(f.result.revision);
	});
	it(`${domain}: refuses duplicate, uncertain and disposed writes`, async () => {
		const f = setupDomain(domain);
		await f.model.refresh();
		const held = deferred<unknown>();
		f.client.handlers.set(f.patch, () => held.promise);
		const first = f.save();
		await expect(f.save()).rejects.toThrow();
		held.resolve(f.result);
		await first;
		f.client.handlers.set(f.patch, () => {
			throw new Error("lost reply");
		});
		await expect(f.save()).rejects.toThrow();
		await expect(f.save()).rejects.toThrow();
		expect(f.client.requests.filter((x) => x.method === f.patch)).toHaveLength(
			2,
		);
		f.model.dispose();
		await expect(f.save()).rejects.toThrow();
		expect(f.client.requests.filter((x) => x.method === f.patch)).toHaveLength(
			2,
		);
	});
	it(`${domain}: late hub A events cannot alter hub B`, async () => {
		const a = setupDomain(domain);
		const b = setupDomain(domain);
		const held = deferred<unknown>();
		a.client.handlers.set(a.get, () => held.promise);
		const read = a.model.refresh();
		a.model.dispose();
		await b.model.refresh();
		const before = b.model.getSnapshot();
		a.client.emit({
			method: "evener/settings/keybindings/changed",
			params: { ...keybindings, revision: 99 },
		});
		held.resolve(a.old);
		await read;
		expect(b.model.getSnapshot()).toBe(before);
		expect(b.client.requests.filter((x) => x.method === b.patch)).toHaveLength(
			0,
		);
	});
}

it("does not overwrite the fallback rules when hub settings failed to load", async () => {
	const client = fakeClient();
	client.handlers.set("evener/settings/keybindings/get", () => ({
		...keybindings,
		loadError: "unreadable settings",
	}));
	const model = new NativePreferences(client, {
		keybindingsSettings: true,
		transcriptDisplaySettings: false,
	});
	await model.refresh();
	await expect(model.saveKeybindings([])).rejects.toThrow();
	expect(model.getSnapshot().keybindings).toMatchObject({
		draftError: null,
		hubError: "unreadable settings",
		loadError: "unreadable settings",
		error:
			"The hub could not load its saved shortcuts. Repair the hub settings file before editing.",
	});
	expect(client.requests.map((x) => x.method)).toEqual([
		"evener/settings/keybindings/get",
	]);
	expect(model.getSnapshot().keybindings.confirmed?.rules).toEqual(
		keybindings.rules,
	);
});

it("preserves the hub diagnostic when draft recovery clears its own error", async () => {
	const client = fakeClient();
	client.handlers.set("evener/settings/keybindings/get", () => {
		throw new Error("hub request failed");
	});
	const backend = fakeDraftBackend();
	backend.store.set("evener.native.keybinding-draft.hub", "{not json");
	const model = new NativePreferences(
		client,
		{ keybindingsSettings: true, transcriptDisplaySettings: false },
		undefined,
		nativeKeybindingDrafts("hub", backend),
	);
	await model.refresh();
	expect(model.getSnapshot().keybindings).toMatchObject({
		draftError: expect.any(String),
		hubError: "hub request failed",
		loadError: null,
		error: expect.any(String),
	});

	await model.discardKeybindingsDraft();
	expect(model.getSnapshot().keybindings).toMatchObject({
		draft: null,
		draftError: null,
		hubError: "hub request failed",
		loadError: null,
		error: "The hub request could not be confirmed.",
	});
	model.dispose();
});

import { fakeDraftBackend } from "./draftBackend.testkit";
import {
	nativeKeybindingDrafts,
	type NativePreferenceDraftBackend,
	nativeTranscriptDrafts,
} from "./nativePreferenceDrafts";

function draftStorage() {
	const backend = fakeDraftBackend();
	return { backend, storage: nativeTranscriptDrafts("hub", backend) };
}
function persistedPreferences(storage = draftStorage().storage) {
	const client = fakeClient();
	client.handlers.set(
		"evener/settings/transcriptDisplay/get",
		() => transcript,
	);
	const model = new NativePreferences(
		client,
		{ keybindingsSettings: false, transcriptDisplaySettings: true },
		storage,
	);
	return { client, model, storage };
}
const transcriptPatch = "evener/settings/transcriptDisplay/patch";
const proposedConfig = {
	...config,
	content: { kind: "preset" as const, level: "full" as const },
};

it("restores a draft synchronously and preserves its base across read and conflict review", async () => {
	const f = persistedPreferences();
	await f.model.refresh();
	await f.model.editTranscript(proposedConfig);
	f.model.dispose();
	const next = persistedPreferences(f.storage);
	expect(next.model.getSnapshot().transcriptMobile.draft?.config).toEqual(
		proposedConfig,
	);
	next.client.handlers.set("evener/settings/transcriptDisplay/get", () => ({
		...transcript,
		mobile: { revision: 6, config: toWireConfig(config) },
	}));
	await next.model.refresh();
	expect(next.model.getSnapshot().transcriptMobile).toMatchObject({
		conflict: true,
		draft: { revision: 4 },
	});
	await expect(next.model.rebaseTranscriptDraft(5)).rejects.toThrow();
	await next.model.rebaseTranscriptDraft(6);
	expect(next.model.getSnapshot().transcriptMobile).toMatchObject({
		conflict: false,
		draft: { revision: 6 },
	});
	expect(next.client.requests.some((r) => r.method === transcriptPatch)).toBe(
		false,
	);
});

it("checkpoints before dispatch and refuses edits, discard, or duplicate save during a write", async () => {
	const f = persistedPreferences();
	await f.model.refresh();
	const ack = deferred<unknown>();
	f.client.handlers.set(transcriptPatch, () => {
		expect(f.storage.load()).toMatchObject({
			writeUncertain: true,
			config: proposedConfig,
			baseRevision: 4,
		});
		return ack.promise;
	});
	const save = f.model.saveTranscript(proposedConfig);
	await expect(f.model.editTranscript(config)).rejects.toThrow();
	await expect(f.model.discardTranscriptDraft()).rejects.toThrow();
	await expect(f.model.saveTranscript()).rejects.toThrow();
	ack.resolve({ layout: "mobile", revision: 5, config: toWireConfig(proposedConfig) });
	await save;
	expect(f.storage.load()).toBeNull();
});

it("accepts its own notification before the acknowledgement", async () => {
	const f = persistedPreferences();
	await f.model.refresh();
	const ack = deferred<unknown>();
	f.client.handlers.set(transcriptPatch, () => ack.promise);
	const save = f.model.saveTranscript(proposedConfig);
	f.client.emit({
		method: "evener/settings/transcriptDisplay/changed",
		params: {
			layout: "mobile",
			revision: 5,
			config: toWireConfig(proposedConfig),
		},
	});
	ack.resolve({ layout: "mobile", revision: 5, config: toWireConfig(proposedConfig) });
	await save;
	expect(f.model.getSnapshot().transcriptMobile).toMatchObject({
		conflict: false,
		writeUncertain: false,
		draft: null,
		confirmed: { revision: 5 },
	});
	expect(f.storage.load()).toBeNull();
});

it("retains a proposal if a genuinely newer external revision beats its acknowledgement", async () => {
	const f = persistedPreferences();
	await f.model.refresh();
	const ack = deferred<unknown>();
	f.client.handlers.set(transcriptPatch, () => ack.promise);
	const save = f.model.saveTranscript(proposedConfig);
	f.client.emit({
		method: "evener/settings/transcriptDisplay/changed",
		params: { layout: "mobile", revision: 6, config: toWireConfig(config) },
	});
	ack.resolve({ layout: "mobile", revision: 5, config: toWireConfig(proposedConfig) });
	await save;
	expect(f.model.getSnapshot().transcriptMobile).toMatchObject({
		conflict: true,
		writeUncertain: false,
		draft: { config: proposedConfig, revision: 4 },
		confirmed: { revision: 6, config },
	});
});

it("late acknowledgement cannot erase a new same-config pending checkpoint", async () => {
	const f = persistedPreferences();
	await f.model.refresh();
	const oldAck = deferred<unknown>();
	f.client.handlers.set(transcriptPatch, () => oldAck.promise);
	const oldSave = f.model.saveTranscript(proposedConfig);
	const oldCheckpoint = f.storage.load();
	f.model.dispose();
	const next = persistedPreferences(f.storage);
	await next.model.refresh();
	const nextAck = deferred<unknown>();
	next.client.handlers.set(transcriptPatch, () => nextAck.promise);
	const nextSave = next.model.saveTranscript();
	expect(f.storage.load()).not.toEqual(oldCheckpoint);
	oldAck.resolve({ layout: "mobile", revision: 5, config: toWireConfig(proposedConfig) });
	await oldSave;
	expect(f.storage.load()).not.toBeNull();
	nextAck.resolve({ layout: "mobile", revision: 5, config: toWireConfig(proposedConfig) });
	await nextSave;
});

it("storage failure prevents dispatch and exposes a fixed safe error", async () => {
	const source = draftStorage().storage;
	const f = persistedPreferences({
		...source,
		// The shared repository persists through the port's compare-and-swap
		// set (insertIfAbsent on the first write, replaceIf once a record is
		// classified), so a local write failure surfaces there.
		insertIfAbsent: () => {
			throw new Error("secret local path");
		},
		replaceIf: () => {
			throw new Error("secret local path");
		},
	});
	await f.model.refresh();
	await expect(f.model.saveTranscript(proposedConfig)).rejects.toThrow(
		"Could not save",
	);
	expect(f.client.requests.some((r) => r.method === transcriptPatch)).toBe(
		false,
	);
	expect(f.model.getSnapshot().transcriptMobile.error).not.toContain("secret");
});

it("an uncertain write cannot be discarded or replayed until an authoritative read", async () => {
	const f = persistedPreferences();
	await f.model.refresh();
	f.client.handlers.set(transcriptPatch, () => {
		throw new Error("token secret");
	});
	await expect(f.model.saveTranscript(proposedConfig)).rejects.toThrow();
	await expect(f.model.discardTranscriptDraft()).rejects.toThrow();
	await expect(f.model.editTranscript(config)).rejects.toThrow();
	expect(f.model.getSnapshot().transcriptMobile.error).not.toContain("secret");
	await f.model.refresh();
	expect(f.model.getSnapshot().transcriptMobile.writeUncertain).toBe(false);
	expect(f.storage.load()).toMatchObject({ writeUncertain: false });
	await f.model.discardTranscriptDraft();
	expect(f.storage.load()).toBeNull();
});

it("resolves a post-apply PATCH failure by adopting the applied value, without blocking further edits", async () => {
	// The hub already published the new revision before a follow-up durable
	// step failed (hubcore.TranscriptDisplayPostApplyError): the write
	// applied, so saveTranscript must reconcile from it rather than treat
	// the write as rejected - mirrors keybindingsStore.ts's handling of
	// KeybindingsPostRenameError for the sibling store.
	const f = persistedPreferences();
	await f.model.refresh();
	const applied = { ...config, content: { kind: "preset" as const, level: "full" as const } };
	f.client.handlers.set(transcriptPatch, () => {
		throw new WireError(
			"transcript display applied then a follow-up step failed",
			-32603,
			{
				evenerErrorInfo: "transcriptDisplayPostApply",
				layout: "mobile",
				applied: { revision: 5, config: toWireConfig(applied) },
			},
		);
	});
	await f.model.saveTranscript(proposedConfig);
	expect(f.model.getSnapshot().transcriptMobile).toMatchObject({
		conflict: false,
		writeUncertain: false,
		draft: null,
		confirmed: { revision: 5, config: applied },
	});
	expect(f.storage.load()).toBeNull();
	// A blocked write would reject further edits (as the writeUncertain
	// test above does); this one applied, so editing is not blocked.
	await f.model.editTranscript(config);
});

it("rejects a post-apply error when the layout does not match", async () => {
	// The postApplyPatch reply is only valid when layout matches the
	// receiving client's own layout. A desktop error received by the mobile
	// store must be treated as an unconfirmed write, retaining the draft.
	const f = persistedPreferences();
	await f.model.refresh();
	f.client.handlers.set(transcriptPatch, () => {
		throw new WireError(
			"transcript display applied then a follow-up step failed",
			-32603,
			{
				evenerErrorInfo: "transcriptDisplayPostApply",
				layout: "desktop",
				applied: { revision: 5, config: toWireConfig(proposedConfig) },
			},
		);
	});
	await expect(f.model.saveTranscript(proposedConfig)).rejects.toThrow();
	expect(f.model.getSnapshot().transcriptMobile).toMatchObject({
		conflict: true,
		writeUncertain: true,
		draft: { config: proposedConfig },
		error: "The hub request could not be confirmed.",
	});
	expect(f.storage.load()).toMatchObject({
		writeUncertain: true,
		config: proposedConfig,
	});
});

it("a corrupt local draft blocks writes until it can be restored", async () => {
	const original = draftStorage().storage;
	let healthy = false;
	const f = persistedPreferences({
		...original,
		load: () => {
			if (!healthy) throw new Error("private storage detail");
			return original.load();
		},
	});
	await f.model.refresh();
	expect(f.model.getSnapshot().transcriptMobile.storageUnavailable).toBe(true);
	await expect(f.model.editTranscript(config)).rejects.toThrow();
	await expect(f.model.saveTranscript(config)).rejects.toThrow();
	expect(f.client.requests).toHaveLength(0);
	healthy = true;
	await f.model.refresh();
	await f.model.editTranscript(config);
	expect(f.model.getSnapshot().transcriptMobile.storageUnavailable).toBe(false);
});

it("a cleanup failure does not turn a confirmed save into an unknown server outcome", async () => {
	const source = draftStorage().storage;
	const f = persistedPreferences({
		...source,
		removeIf: () => {
			throw new Error("secret cleanup");
		},
	});
	await f.model.refresh();
	f.client.handlers.set(transcriptPatch, () => ({
		layout: "mobile",
		revision: 5,
		config: toWireConfig(proposedConfig),
	}));
	await f.model.saveTranscript(proposedConfig);
	expect(f.model.getSnapshot().transcriptMobile).toMatchObject({
		confirmed: { revision: 5 },
		writeUncertain: false,
		storageUnavailable: true,
		saving: false,
	});
	expect(f.model.getSnapshot().transcriptMobile.error).not.toContain("secret");
	await expect(f.model.saveTranscript()).rejects.toThrow();
});

it("a change notification cannot hide a local recovery error", async () => {
	const storage = draftStorage().storage;
	const f = persistedPreferences({
		...storage,
		load: () => {
			throw new Error("storage unavailable");
		},
	});
	const error = f.model.getSnapshot().transcriptMobile.error;
	f.client.emit({
		method: "evener/settings/transcriptDisplay/changed",
		params: { layout: "mobile", revision: 9, config: toWireConfig(config) },
	});
	expect(f.model.getSnapshot().transcriptMobile.error).toBe(error);
	expect(f.model.getSnapshot().transcriptMobile.storageUnavailable).toBe(true);
});

describe("transcriptMobile projection (A10)", () => {
	it("projects transcriptMobile through the shared transcript display store", async () => {
		const f = persistedPreferences();
		await f.model.refresh();
		await f.model.editTranscript(proposedConfig);
		// The shared store's own checkpoint carries its `layout` field; the
		// deleted hand-rolled draft repository never wrote one. Its presence
		// is the projection's own signature.
		expect(f.storage.load()).toMatchObject({
			layout: "mobile",
			baseRevision: 4,
			config: proposedConfig,
			writeUncertain: false,
		});
		expect(f.model.getSnapshot().transcriptMobile).toMatchObject({
			draft: { revision: 4, config: proposedConfig },
			draftUnreadable: false,
		});
		expect(
			f.client.requests.filter((request) => request.method === transcriptPatch),
		).toHaveLength(0);
	});

	it("surfaces an unreadable transcript draft record and clears it on discard", async () => {
		const backend = fakeDraftBackend();
		backend.store.set("evener.native.transcript-draft.hub", "{not json");
		const client = fakeClient();
		client.handlers.set("evener/settings/transcriptDisplay/get", () => transcript);
		const model = new NativePreferences(
			client,
			{ keybindingsSettings: false, transcriptDisplaySettings: true },
			nativeTranscriptDrafts("hub", backend),
		);
		await model.refresh();
		expect(model.getSnapshot().transcriptMobile.draftUnreadable).toBe(true);
		await model.discardTranscriptDraft();
		expect(backend.store.has("evener.native.transcript-draft.hub")).toBe(false);
		expect(model.getSnapshot().transcriptMobile.draftUnreadable).toBe(false);
	});

	it("migrates a legacy transcript draft checkpoint (no layout) instead of stranding it", async () => {
		// The previous native implementation stored this hub's mobile-only
		// checkpoint without a layout. The shared store's decoder requires one,
		// so the port adopts the known mobile layout by compare-and-swap rather
		// than classifying an upgradeable draft as unreadable.
		const backend = fakeDraftBackend();
		backend.store.set("evener.native.transcript-draft.hub", {
			id: "old",
			baseRevision: 4,
			config,
			writeUncertain: false,
		});
		const client = fakeClient();
		client.handlers.set("evener/settings/transcriptDisplay/get", () => transcript);
		const model = new NativePreferences(
			client,
			{ keybindingsSettings: false, transcriptDisplaySettings: true },
			nativeTranscriptDrafts("hub", backend),
		);
		await model.refresh();
		expect(model.getSnapshot().transcriptMobile.draftUnreadable).toBe(false);
		expect(model.getSnapshot().transcriptMobile.draft).toMatchObject({
			revision: 4,
			config,
		});
		expect(backend.store.get("evener.native.transcript-draft.hub")).toMatchObject({
			layout: "mobile",
		});
	});

	it("keeps a readable legacy checkpoint visible when the migration write fails", async () => {
		// The adoption write is best-effort: a failed write (quota, denied
		// storage) must not make load() throw, which the shared store would map
		// to storageUnavailable with no draft - hiding the readable legacy
		// checkpoint the read just decoded.
		const backend = fakeDraftBackend();
		backend.store.set("evener.native.transcript-draft.hub", {
			id: "old",
			baseRevision: 4,
			config,
			writeUncertain: false,
		});
		const client = fakeClient();
		client.handlers.set("evener/settings/transcriptDisplay/get", () => transcript);
		const model = new NativePreferences(
			client,
			{ keybindingsSettings: false, transcriptDisplaySettings: true },
			nativeTranscriptDrafts("hub", {
				...backend,
				replaceIf: () => {
					throw new Error("quota exceeded");
				},
			}),
		);
		await model.refresh();
		expect(model.getSnapshot().transcriptMobile).toMatchObject({
			draftUnreadable: false,
			storageUnavailable: false,
			draft: { revision: 4, config },
		});
	});

	it("can discard a legacy checkpoint whose migration write failed", async () => {
		// The migrated checkpoint must still name the bytes on disk, or the
		// shared repository's compare-and-swap refuses and discard silently
		// does nothing.
		const backend = fakeDraftBackend();
		backend.store.set("evener.native.transcript-draft.hub", {
			id: "old",
			baseRevision: 4,
			config,
			writeUncertain: false,
		});
		const client = fakeClient();
		client.handlers.set("evener/settings/transcriptDisplay/get", () => transcript);
		const model = new NativePreferences(
			client,
			{ keybindingsSettings: false, transcriptDisplaySettings: true },
			nativeTranscriptDrafts("hub", {
				...backend,
				replaceIf: () => {
					throw new Error("quota exceeded");
				},
			}),
		);
		await model.refresh();
		await model.discardTranscriptDraft();
		expect(backend.store.has("evener.native.transcript-draft.hub")).toBe(false);
		expect(model.getSnapshot().transcriptMobile.draft).toBeNull();
	});

	it("can save a legacy draft after one migration-write failure", async () => {
		const backend = fakeDraftBackend();
		backend.store.set("evener.native.transcript-draft.hub", {
			id: "old",
			baseRevision: 4,
			config,
			writeUncertain: false,
		});
		let writes = 0;
		const client = fakeClient();
		client.handlers.set("evener/settings/transcriptDisplay/get", () => transcript);
		client.handlers.set(transcriptPatch, () => ({
			layout: "mobile",
			revision: 5,
			config: toWireConfig(proposedConfig),
		}));
		// The shared fake's own replaceIf is narrowed to the keybinding
		// checkpoint, but this port stores the transcript checkpoint: view the
		// fake through the shape-agnostic base backend it also implements, so
		// the delegation is typed for the record this port actually writes.
		const storeBackend: NativePreferenceDraftBackend = backend;
		const model = new NativePreferences(
			client,
			{ keybindingsSettings: false, transcriptDisplaySettings: true },
			nativeTranscriptDrafts("hub", {
				...backend,
				replaceIf: (key, expected, next) => {
					writes += 1;
					if (writes === 1) throw new Error("quota exceeded");
					return storeBackend.replaceIf(
						key,
						expected,
						next as TranscriptDraftCheckpoint,
					);
				},
			}),
		);
		await model.refresh();
		await model.saveTranscript(proposedConfig);
		expect(model.getSnapshot().transcriptMobile.confirmed?.revision).toBe(5);
		expect(backend.store.has("evener.native.transcript-draft.hub")).toBe(false);
	});

	it("generation-aware staleness: a draft composed under generation N does not read current when a replacement hub reuses revision N", async () => {
		const backend = fakeDraftBackend();
		const client = fakeClient();
		client.handlers.set("evener/settings/transcriptDisplay/get", () => transcript);
		const model = new NativePreferences(
			client,
			{ keybindingsSettings: false, transcriptDisplaySettings: true },
			nativeTranscriptDrafts("hub", backend),
		);
		await model.refresh();
		await model.editTranscript(proposedConfig);
		expect(model.getSnapshot().transcriptMobile.conflict).toBe(false);
		// A native model owns exactly one ready generation (see the constructor
		// contract), so a replacement hub drives the store instance this model
		// projects to a new generation rather than a fresh store. Its revision
		// numbering may restart, so mobile revision 4 is not the 4 the draft was
		// composed against: the projected conflict field must surface that.
		const store = (
			model as unknown as { transcripts: TranscriptDisplayStore }
		).transcripts;
		store.beginReadyGeneration();
		await model.refresh();
		expect(model.getSnapshot().transcriptMobile.conflict).toBe(true);
	});
});
