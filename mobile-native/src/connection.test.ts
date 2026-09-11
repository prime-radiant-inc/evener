import { describe, expect, it } from "vitest";
import type { WebSocketLike } from "../../cmd/evener-hub/frontend/src/protocol/transport";
import {
	connectionTarget,
	createHubClient,
	HubProfiles,
	parsePairingURL,
} from "./connection";
import { connectionFailure } from "./connectionRecovery";

describe("hub connections", () => {
	it("parses escaped pairing tokens and rejects unsafe links without exposing input", () => {
		expect(
			parsePairingURL("https://hub.example:9443/auth/a%26b%23c%25ile"),
		).toEqual({
			origin: "https://hub.example:9443",
			token: "a&b#c%ile",
		});
		for (const input of [
			"https://user:pass@hub.example/auth/token",
			"ftp://hub.example/auth/token",
			"https://hub.example/auth/token?",
			"https://hub.example/auth/token#",
			"https://hub.example/auth/token/extra",
			"https://hub.example/a/../auth/token",
			"https://hub.example/auth/%ZZ",
			"https://hub.example/auth/.",
			"https://hub.example/auth/..",
			"https://hub.example/auth/%2e%2e",
			"http://hub\\@evil/auth/token",
			"http://hub\\evil/auth/token",
			"https://hub.example/auth/to\nken",
		]) {
			expect(() => parsePairingURL(input)).toThrow("Invalid pairing URL.");
		}
	});
	it("normalizes origins and rejects credentials and URL tokens", () => {
		expect(connectionTarget(" https://example.com:443/ ")).toBe(
			"wss://example.com/rpc",
		);
		expect(connectionTarget("http://127.0.0.1:9180")).toBe(
			"ws://127.0.0.1:9180/rpc",
		);
		for (const url of [
			"https://me:secret@hub.test",
			"https://hub.test/?token=secret",
			"https://hub.test/auth",
			"file:///tmp/hub",
			"https://hub.test/#secret",
		])
			expect(() => connectionTarget(url)).toThrow();
	});
	it("persists independent named hubs without exposing tokens in the roster", async () => {
		const data = new Map<string, string>();
		const profiles = new HubProfiles({
			getItemAsync: async (k) => data.get(k) ?? null,
			setItemAsync: async (k, v) => {
				data.set(k, v);
			},
			deleteItemAsync: async (k) => {
				data.delete(k);
			},
		});
		const a = await profiles.save({
			id: "a",
			name: "Local",
			origin: "http://localhost:9180",
			token: "first",
		});
		await profiles.save({
			id: "b",
			name: "Remote",
			origin: "https://hub.test",
			token: "second",
		});
		expect(await profiles.list()).toEqual([
			a,
			{ id: "b", name: "Remote", origin: "https://hub.test" },
		]);
		expect(await profiles.token("a")).toBe("first");
		await profiles.remove("a");
		expect(await profiles.token("a")).toBe("");
		expect((await profiles.list()).map((p) => p.id)).toEqual(["b"]);
		expect(await profiles.token("b")).toBe("second");
	});
	it("degrades to an empty roster for a corrupt index and reports a corrupt record", async () => {
		const data = new Map<string, string>();
		const profiles = new HubProfiles({
			getItemAsync: async (k) => data.get(k) ?? null,
			setItemAsync: async (k, v) => {
				data.set(k, v);
			},
			deleteItemAsync: async (k) => {
				data.delete(k);
			},
		});
		// A corrupt index is unreadable, not fatal: the roster is empty.
		data.set("evener.hubs", "{not json");
		expect(await profiles.list()).toEqual([]);
		// A corrupt record surfaces the domain error, never a raw SyntaxError.
		data.set("evener.hubs", JSON.stringify(["a"]));
		data.set("evener.hub.a", "{not json");
		await expect(profiles.token("a")).rejects.toThrow(
			"Saved hub could not be read.",
		);
		// A record that parses to null is just as unreadable.
		data.set("evener.hub.a", "null");
		await expect(profiles.token("a")).rejects.toThrow(
			"Saved hub could not be read.",
		);
		// list() still degrades around the unreadable record.
		expect(await profiles.list()).toEqual([]);
	});
	it("retains both hubs when secure writes overlap", async () => {
		const data = new Map<string, string>();
		const profiles = new HubProfiles({
			getItemAsync: async (k) => data.get(k) ?? null,
			setItemAsync: async (k, v) => {
				data.set(k, v);
			},
			deleteItemAsync: async (k) => {
				data.delete(k);
			},
		});
		await Promise.all(
			["a", "b"].map((id) =>
				profiles.save({ id, name: id, origin: "https://hub.test", token: id }),
			),
		);
		expect((await profiles.list()).map((p) => p.id)).toEqual(["a", "b"]);
	});
	it("skips one corrupt secure record while preserving valid siblings and their secrets", async () => {
		const data = new Map<string, string>([
			["evener.hubs", JSON.stringify(["bad", "good-a", "good-b"])],
			[
				"evener.hub.bad",
				JSON.stringify({
					id: "bad",
					name: "Broken",
					origin: "https://bad.test",
				}),
			],
			[
				"evener.hub.good-a",
				JSON.stringify({
					id: "good-a",
					name: "Good A",
					origin: "https://a.test",
					token: "secret-a",
				}),
			],
			[
				"evener.hub.good-b",
				JSON.stringify({
					id: "good-b",
					name: "Good B",
					origin: "https://b.test",
					token: "secret-b",
				}),
			],
		]);
		const profiles = new HubProfiles({
			getItemAsync: async (key) => data.get(key) ?? null,
			setItemAsync: async (key, value) => {
				data.set(key, value);
			},
			deleteItemAsync: async (key) => {
				data.delete(key);
			},
		});

		expect(await profiles.list()).toEqual([
			{ id: "good-a", name: "Good A", origin: "https://a.test" },
			{ id: "good-b", name: "Good B", origin: "https://b.test" },
		]);
		expect(await profiles.token("good-a")).toBe("secret-a");
		expect(await profiles.token("good-b")).toBe("secret-b");
		expect(data.has("evener.hub.good-a")).toBe(true);
		expect(data.has("evener.hub.good-b")).toBe(true);
	});

	it("uses the shared handshake and an authorization header, never URL credentials", async () => {
		let target = "";
		let headers: Record<string, string> = {};
		const socket: WebSocketLike = {
			onopen: null,
			onmessage: null,
			onerror: null,
			onclose: null,
			send(data) {
				const frame = JSON.parse(data);
				if (frame.method === "initialize")
					queueMicrotask(() =>
						socket.onmessage?.({
							data: JSON.stringify({
								jsonrpc: "2.0",
								id: frame.id,
								result: {
									serverInfo: { name: "test", version: "1" },
									protocolVersion: "evener-appwire-v5",
									sourceId: "local",
									features: Object.fromEntries(
										[
											"threadList",
											"threadTurnsList",
											"turnStart",
											"turnSteer",
											"threadClear",
											"threadShutdown",
											"forkFromTurn",
											"tasks",
											"transcriptList",
											"modelList",
											"directoryComplete",
											"auth",
										].map((k) => [k, true]),
									),
								},
							}),
						}),
					);
			},
			close() {
				socket.onclose?.({ code: 1000 });
			},
		};
		const client = createHubClient(
			"https://hub.test",
			"secret",
			(url, options) => {
				target = url;
				headers = options.headers;
				queueMicrotask(() => socket.onopen?.());
				return socket;
			},
		);
		try {
			await client.connect();
			expect(client.state).toBe("ready");
			expect(target).toBe("wss://hub.test/rpc");
			expect(headers).toEqual({ Authorization: "Bearer secret" });
		} finally {
			client.close();
		}
		expect(client.state).toBe("closed");
	});
	it("classifies a shared-client protocol refusal for recovery guidance", async () => {
		const socket: WebSocketLike = {
			onopen: null,
			onmessage: null,
			onerror: null,
			onclose: null,
			send(data) {
				const frame = JSON.parse(data);
				if (frame.method === "initialize")
					queueMicrotask(() =>
						socket.onmessage?.({
							data: JSON.stringify({
								jsonrpc: "2.0",
								id: frame.id,
								error: { code: -32600, message: "invalid request" },
							}),
						}),
					);
			},
			close() {
				socket.onclose?.({ code: 1000 });
			},
		};
		const client = createHubClient("https://hub.test", "", (_url, _options) => {
			queueMicrotask(() => socket.onopen?.());
			return socket;
		});
		await expect(client.connect()).rejects.toThrow();
		expect(client.terminalReason).toBe("protocol");
		expect(connectionFailure(client.terminalReason).kind).toBe("protocol");
		expect(connectionFailure(null).kind).toBe("transport");
	});
});

it("edits a saved hub without changing its identity or implicitly replacing credentials", async () => {
	const data = new Map<string, string>();
	const profiles = new HubProfiles({
		getItemAsync: async (k) => data.get(k) ?? null,
		setItemAsync: async (k, value) => {
			data.set(k, value);
		},
		deleteItemAsync: async (k) => {
			data.delete(k);
		},
	});
	await profiles.save({
		id: "a",
		name: "Before",
		origin: "https://a.test",
		token: "first",
	});
	await profiles.save({
		id: "b",
		name: "Other",
		origin: "https://b.test",
		token: "second",
	});
	expect(await profiles.update("a", { name: " After " })).toEqual({
		id: "a",
		name: "After",
		origin: "https://a.test",
	});
	expect(await profiles.token("a")).toBe("first");
	await profiles.update("a", { name: "After", token: "replacement" });
	expect(await profiles.token("a")).toBe("replacement");
	await profiles.update("a", { name: "After", token: "" });
	expect(await profiles.token("a")).toBe("");
	expect(await profiles.token("b")).toBe("second");
	await profiles.remove("a");
	await expect(profiles.update("a", { name: "Gone" })).rejects.toThrow();
	expect((await profiles.list()).map((p) => p.id)).toEqual(["b"]);
});
