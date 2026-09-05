import { describe, expect, it } from "vitest";
import { connectionTarget, HubProfiles, createHubClient } from "./connection";
import type { WebSocketLike } from "../../cmd/evener-hub/frontend/src/protocol/transport";

describe("hub connections", () => {
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
									protocolVersion: "evener-appwire-v3",
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
});
