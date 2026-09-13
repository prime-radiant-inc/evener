import { describe, expect, it } from "vitest";
import type {
	AnyNotification,
	InitializeResponse,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import {
	bindNativePreferences,
	type PreferencesClient,
} from "./bindNativePreferences";
import { nativeTranscriptDrafts } from "./nativePreferenceDrafts";
import type { NativePreferences } from "./nativePreferences";

function deferred<T>() {
	let resolve!: (value: T) => void;
	const promise = new Promise<T>((done) => {
		resolve = done;
	});
	return { promise, resolve };
}
function fixture() {
	const connection = deferred<InitializeResponse>();
	const readyListeners = new Set<(value: InitializeResponse) => void>();
	const notifications = new Set<(value: AnyNotification) => void>();
	const models: NativePreferences[] = [];
	const client = {
		connect: () => connection.promise,
		onReady: (cb: (value: InitializeResponse) => void) => {
			readyListeners.add(cb);
			return () => {
				readyListeners.delete(cb);
			};
		},
		onNotification: (cb: (value: AnyNotification) => void) => {
			notifications.add(cb);
			return () => {
				notifications.delete(cb);
			};
		},
		request: async () => {
			throw new Error("Unsupported domains must not request");
		},
	} as unknown as PreferencesClient;
	const storage = nativeTranscriptDrafts("hub", {
		get: () => null,
		set: () => {},
		delete: () => {},
		deleteIf: () => {},
		createId: () => "test",
	});
	const hello = {
		features: { keybindingsSettings: false, transcriptDisplaySettings: false },
	} as InitializeResponse;
	const dispose = bindNativePreferences(client, storage, (model) => {
		models.push(model);
	});
	return {
		connection,
		models,
		notifications,
		hello,
		dispose,
		ready: (value = hello) => {
			for (const cb of readyListeners) cb(value);
		},
	};
}
describe("native preferences connection binding", () => {
	it("binds an already-ready client and disposes notification ownership", async () => {
		const f = fixture();
		f.connection.resolve(f.hello);
		await f.connection.promise;
		expect(f.models).toHaveLength(1);
		expect(f.notifications.size).toBe(1);
		f.dispose();
		expect(f.notifications.size).toBe(0);
	});
	it("does not double-bind the initial ready event and shared connect reply", async () => {
		const f = fixture();
		f.ready();
		f.connection.resolve(f.hello);
		await f.connection.promise;
		expect(f.models).toHaveLength(1);
		f.ready({ ...f.hello });
		expect(f.models).toHaveLength(2);
		expect(f.notifications.size).toBe(1);
		await expect(f.models[0].editTranscript({} as never)).rejects.toThrow();
		f.dispose();
	});
	it("ignores a late old-hub handshake after disposal", async () => {
		const old = fixture();
		old.dispose();
		const next = fixture();
		next.ready();
		old.connection.resolve(old.hello);
		await old.connection.promise;
		expect(old.models).toHaveLength(0);
		expect(next.models).toHaveLength(1);
		next.dispose();
	});
});
