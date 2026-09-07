import { describe, expect, it, vi } from "vitest";
import type {
	AnyNotification,
	KeybindingsOverrides,
	TranscriptDisplayDefaults,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import { toWireConfig } from "../../cmd/evener-hub/frontend/src/transcriptDisplay/config";
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
		: { revision: 5, config: toWireConfig(config) };
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
	expect(client.requests.map((x) => x.method)).toEqual([
		"evener/settings/keybindings/get",
	]);
	expect(model.getSnapshot().keybindings.confirmed?.rules).toEqual(
		keybindings.rules,
	);
});
