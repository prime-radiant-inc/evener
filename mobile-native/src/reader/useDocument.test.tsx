import { once } from "node:events";
import { createServer, request as httpRequest, type Server } from "node:http";
import { setTimeout as wait } from "node:timers/promises";

import { StrictMode, useEffect } from "react";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { render, renderHook, settle, unmountMountedTrees } from "../renderNative.testkit";
import { useDocument } from "./useDocument";

const harness = vi.hoisted(() => ({
	state: "ready" as string,
	activeProfileId: "studio",
	origin: "https://hub.test",
	appState: [] as ((state: string) => void)[],
}));

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	AppState: {
		currentState: "active",
		addEventListener: (_event: string, listener: (state: string) => void) => {
			harness.appState.push(listener);
			return {
				remove: () => {
					harness.appState = harness.appState.filter((entry) => entry !== listener);
				},
			};
		},
	},
}));
vi.mock("expo-secure-store", () => ({
	getItemAsync: vi.fn(async (key: string) =>
		key === "evener.hub.studio"
			? JSON.stringify({ id: "studio", name: "Studio", origin: "https://hub.test", token: "secret" })
			: null,
	),
	setItemAsync: vi.fn(async () => {}),
	deleteItemAsync: vi.fn(async () => {}),
}));
vi.mock("../ConnectionProvider", () => ({
	useConnection: () => ({
		profiles: [
			{ id: "other", name: "Other", origin: "https://unrelated.test" },
			{ id: "studio", name: "Studio", origin: harness.origin },
		],
		activeProfile: { id: harness.activeProfileId },
		state: harness.state,
	}),
}));

type Answer = { status: number; body: string } | "offline";
let answers: Answer[] = [];
let fetchSpy: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
	harness.state = "ready";
	harness.origin = "https://hub.test";
	harness.activeProfileId = "studio";
	harness.appState = [];
	answers = [];
	fetchSpy = vi.spyOn(globalThis, "fetch").mockImplementation(async () => {
		const answer = answers.shift() ?? { status: 200, body: "# Plan" };
		if (answer === "offline") throw new TypeError("Network request failed");
		return new Response(answer.body, {
			status: answer.status,
			headers: { "Content-Type": "text/plain; charset=utf-8" },
		});
	});
});
afterEach(() => {
	unmountMountedTrees();
	vi.restoreAllMocks();
	vi.useRealTimers();
});

const reference = {
	path: "docs/plan.md",
	cwd: "/work/a",
	readTarget: "/work/a/docs/plan.md",
	provenance: "relative" as const,
};
function mount() {
	return renderHook(() => useDocument("studio", "local:fix", reference, true));
}

it("reads useful bytes and Reload after actual StrictMode effect replay", async () => {
	vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
	answers = [
		{ status: 200, body: "# Useful replay\n\nInitial bytes." },
		{ status: 200, body: "# Useful Reload\n\nReloaded bytes." },
	];
	let value!: ReturnType<typeof useDocument>;
	const effects: string[] = [];
	function Probe() {
		value = useDocument("studio", "local:fix", reference, true);
		useEffect(() => {
			effects.push("setup");
			return () => {
				effects.push("cleanup");
			};
		}, []);
		return null;
	}
	const tree = render(
		<StrictMode>
			<Probe />
		</StrictMode>,
	);
	try {
		expect(effects).toEqual(["setup", "cleanup", "setup"]);
		await until(() =>
			expect(value.document).toMatchObject({
				kind: "markdown",
				title: "Useful replay",
				text: "# Useful replay\n\nInitial bytes.",
			}),
		);
		expect(fetchSpy).toHaveBeenCalledOnce();
		act(() => value.reload());
		await until(() =>
			expect(value.document).toMatchObject({ title: "Useful Reload", text: "# Useful Reload\n\nReloaded bytes." }),
		);
		expect(fetchSpy).toHaveBeenCalledTimes(2);
		expect(harness.appState).toHaveLength(1);
	} finally {
		act(() => tree.unmount());
	}
	expect(effects).toEqual(["setup", "cleanup", "setup", "cleanup"]);
	expect(harness.appState).toEqual([]);
	expect(vi.getTimerCount()).toBe(0);
});

it("coalesces pending StrictMode Reload and retires late HTTP completion on cleanup", async () => {
	vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
	const releases: ((response: Response) => void)[] = [];
	fetchSpy.mockImplementation(
		() =>
			new Promise<Response>((resolve) => {
				releases.push(resolve);
			}),
	);
	let value!: ReturnType<typeof useDocument>;
	const seen: unknown[] = [];
	function Probe() {
		value = useDocument("studio", "local:fix", reference, true);
		seen.push(value.document);
		return null;
	}
	const tree = render(
		<StrictMode>
			<Probe />
		</StrictMode>,
	);
	try {
		await until(() => expect(releases).toHaveLength(1));
		act(() => {
			value.reload();
			value.reload();
		});
		expect(fetchSpy).toHaveBeenCalledOnce();
		await act(async () => releases[0]?.(new Response("# Obsolete pending bytes")));
		await until(() => expect(releases).toHaveLength(2));
		expect(seen.every((document) => document === null)).toBe(true);
		act(() => tree.unmount());
		const renders = seen.length;
		await act(async () => {
			releases[1]?.(new Response("offline", { status: 503 }));
			await vi.advanceTimersByTimeAsync(60000);
		});
		expect(seen).toHaveLength(renders);
		expect(fetchSpy).toHaveBeenCalledTimes(2);
		expect(harness.appState).toEqual([]);
		expect(vi.getTimerCount()).toBe(0);
	} finally {
		act(() => tree.unmount());
	}
});

it("suppresses old bytes in the first render of a different target", async () => {
	let target = reference;
	const seen: unknown[] = [];
	const hook = renderHook(() => {
		const value = useDocument("studio", "local:fix", target, true);
		seen.push(value.document);
		return value;
	});
	await settle();
	expect(hook.result.current.document).toMatchObject({ title: "Plan" });
	fetchSpy.mockImplementationOnce(() => new Promise<Response>(() => {}));
	seen.length = 0;
	target = { path: "docs/other.md", cwd: "/work/b", readTarget: "/work/b/docs/other.md", provenance: "relative" };
	hook.rerender();
	expect(seen[0]).toBeNull();
	hook.unmount();
});

it("does not read on another profile's ready signal", async () => {
	harness.activeProfileId = "other";
	harness.state = "reconnecting";
	const hook = mount();
	await settle();
	const before = fetchSpy.mock.calls.length;
	harness.state = "ready";
	hook.rerender();
	await settle();
	expect(fetchSpy).toHaveBeenCalledTimes(before);
	hook.unmount();
});

it("coalesces Reload while the actual HTTP read is still pending", async () => {
	let release!: (response: Response) => void;
	fetchSpy.mockImplementationOnce(
		() =>
			new Promise<Response>((resolve) => {
				release = resolve;
			}),
	);
	const hook = mount();
	await settle();
	act(() => {
		hook.result.current.reload();
		hook.result.current.reload();
	});
	await settle();
	expect(fetchSpy).toHaveBeenCalledTimes(1);
	release(new Response("# Retired"));
	await settle();
	expect(fetchSpy).toHaveBeenCalledTimes(2);
	expect(hook.result.current.document).toMatchObject({ title: "Plan" });
	hook.unmount();
});

it("reads /doc/file with the hub's bearer token and returns the document", async () => {
	answers = [{ status: 200, body: "# Settle the race\n\nFirst." }];
	const hook = mount();
	expect(hook.result.current.document).toBeNull();
	await settle();
	expect(fetchSpy).toHaveBeenCalledWith(
		"https://hub.test/doc/file?format=raw&session=local%3Afix&path=%2Fwork%2Fa%2Fdocs%2Fplan.md",
		{ headers: { Authorization: "Bearer secret" } },
	);
	expect(hook.result.current.document).toMatchObject({ kind: "markdown", title: "Settle the race" });
	hook.unmount();
});

it("keeps the shown document until a reload's read lands", async () => {
	answers = [{ status: 200, body: "# One" }];
	const hook = mount();
	await settle();
	let release!: () => void;
	fetchSpy.mockImplementationOnce(
		() =>
			new Promise<Response>((resolve) => {
				release = () => resolve(new Response("# Two", { headers: { "Content-Type": "text/plain" } }));
			}),
	);
	act(() => hook.result.current.reload());
	await settle();
	expect(hook.result.current.document).toMatchObject({ title: "One" });
	release();
	await settle();
	expect(hook.result.current.document).toMatchObject({ title: "Two" });
	hook.unmount();
});

it("keeps the old document when a re-read fails, and replaces it when the file is gone", async () => {
	answers = [{ status: 200, body: "# One" }, "offline", { status: 404, body: "not found" }];
	const hook = mount();
	await settle();
	act(() => hook.result.current.reload());
	await settle();
	expect(hook.result.current.document).toMatchObject({ kind: "markdown", title: "One" });
	expect(hook.result.current.notice).toBe("plan.md couldn't be loaded right now.");
	act(() => hook.result.current.reload());
	await settle();
	expect(hook.result.current.document).toMatchObject({ kind: "missing" });
	hook.unmount();
});

it("shows a failed first read, since there is nothing older to keep", async () => {
	answers = ["offline"];
	const hook = mount();
	await settle();
	expect(hook.result.current.document).toMatchObject({ kind: "failed" });
	hook.unmount();
});

it("reads again when the connection comes back and when the app returns to the front", async () => {
	const hook = mount();
	await settle();
	expect(fetchSpy).toHaveBeenCalledTimes(1);
	harness.state = "reconnecting";
	hook.rerender();
	await settle();
	expect(fetchSpy).toHaveBeenCalledTimes(1);
	harness.state = "ready";
	hook.rerender();
	await settle();
	expect(fetchSpy).toHaveBeenCalledTimes(2);
	act(() => {
		for (const listener of harness.appState) listener("background");
	});
	await settle();
	expect(fetchSpy).toHaveBeenCalledTimes(2);
	act(() => {
		for (const listener of harness.appState) listener("active");
	});
	await settle();
	expect(fetchSpy).toHaveBeenCalledTimes(3);
	hook.unmount();
	expect(harness.appState).toEqual([]);
});

// Real I/O settles by an observable result, not a fixed number of microtasks.
async function until(check: () => void) {
	const deadline = performance.now() + 3000;
	for (;;) {
		try {
			await act(async () => {
				await wait(1);
			});
			check();
			return;
		} catch (error) {
			if (performance.now() > deadline) throw error;
		}
	}
}
async function listen(server: Server) {
	server.listen(0, "127.0.0.1");
	await once(server, "listening");
	const address = server.address();
	if (!address || typeof address === "string") throw new Error("expected TCP fixture");
	return `http://127.0.0.1:${address.port}`;
}
async function close(server: Server) {
	server.closeAllConnections();
	await new Promise<void>((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())));
}

it("recovers real owning HTTP 503 bytes at 1, 2, 4, 8, 15, 15 seconds while the controller stays ready", async () => {
	fetchSpy.mockRestore();
	const requests: { path: string | null; session: string | null; auth: string | undefined }[] = [];
	const server = createServer((request, response) => {
		const url = new URL(request.url ?? "", "http://fixture.test");
		requests.push({
			path: url.searchParams.get("path"),
			session: url.searchParams.get("session"),
			auth: request.headers.authorization,
		});
		response.writeHead(requests.length < 7 ? 503 : 200, { "Content-Type": "text/plain; charset=utf-8" });
		response.end(requests.length < 7 ? "owning attachment is offline" : "# Useful recovery\n\nActual bytes.");
	});
	harness.origin = await listen(server);
	// Only the transport boundary adapts real HTTP bytes, so the fake clock
	// controls demand rather than Node fetch's unrelated Undici timers.
	vi.spyOn(globalThis, "fetch").mockImplementation(
		(input, init) =>
			new Promise<Response>((resolve, reject) => {
				const request = httpRequest(String(input), { headers: init?.headers as Record<string, string> }, (response) => {
					const chunks: Buffer[] = [];
					response.on("data", (chunk: Buffer) => chunks.push(chunk));
					response.on("end", () =>
						resolve(
							new Response(new Uint8Array(Buffer.concat(chunks)), {
								status: response.statusCode,
								headers: response.headers as Record<string, string>,
							}),
						),
					);
				});
				request.on("error", reject);
				request.end();
			}),
	);
	vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
	const scheduled = vi.spyOn(globalThis, "setTimeout");
	const hook = mount();
	try {
		await until(() => expect(hook.result.current.document?.kind).toBe("failed"));
		for (const [index, delay] of [1000, 2000, 4000, 8000, 15000, 15000].entries()) {
			await until(() =>
				expect(
					scheduled.mock.calls.filter((call) => [1000, 2000, 4000, 8000, 15000].includes(Number(call[1]))),
				).toHaveLength(index + 1),
			);
			const previous = hook.result.current.document;
			await act(async () => {
				await vi.advanceTimersByTimeAsync(delay - 1);
			});
			expect(requests).toHaveLength(index + 1);
			await act(async () => {
				await vi.advanceTimersByTimeAsync(1);
			});
			await until(() => expect(requests).toHaveLength(index + 2));
			await until(() => {
				expect(hook.result.current.document).not.toBe(previous);
				expect(hook.result.current.document?.kind).toBe(index === 5 ? "markdown" : "failed");
			});
		}
		expect(hook.result.current.document).toMatchObject({ kind: "markdown", title: "Useful recovery" });
		expect(harness.state).toBe("ready");
		expect(
			requests.every(
				(request) =>
					request.path === "/work/a/docs/plan.md" &&
					request.session === "local:fix" &&
					request.auth === "Bearer secret",
			),
		).toBe(true);
		hook.unmount();
		await act(async () => {
			await vi.advanceTimersByTimeAsync(60000);
		});
		expect(requests).toHaveLength(7);
		expect(harness.appState).toEqual([]);
	} finally {
		hook.unmount();
		vi.useRealTimers();
		await close(server);
	}
});

it.each(["hidden", "background", "other-profile", "unmounted"])("cancels paced retries when %s", async (reason) => {
	answers = Array.from({ length: 10 }, () => ({ status: 503, body: "offline" }));
	vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
	let inFront = true;
	const hook = renderHook(() => useDocument("studio", "local:fix", reference, inFront));
	await until(() => expect(hook.result.current.document?.kind).toBe("failed"));
	expect(vi.getTimerCount()).toBe(1);
	if (reason === "unmounted") hook.unmount();
	else if (reason === "background")
		act(() => {
			for (const listener of [...harness.appState]) listener("background");
		});
	else {
		if (reason === "hidden") inFront = false;
		else harness.activeProfileId = "other";
		hook.rerender();
	}
	expect(vi.getTimerCount()).toBe(0);
	await act(async () => {
		await vi.advanceTimersByTimeAsync(60000);
	});
	expect(fetchSpy).toHaveBeenCalledOnce();
	hook.unmount();
	expect(harness.appState).toEqual([]);
});

it("retires deferred old target and origin reads, and never retains healthy bytes across identities", async () => {
	let target = reference;
	const seen: unknown[] = [];
	const hook = renderHook(() => {
		const value = useDocument("studio", "local:fix", target, true);
		seen.push(value.document);
		return value;
	});
	await until(() => expect(hook.result.current.document).toMatchObject({ title: "Plan" }));
	let release!: (value: Response) => void;
	fetchSpy.mockImplementationOnce(
		() =>
			new Promise<Response>((resolve) => {
				release = resolve;
			}),
	);
	act(() => hook.result.current.reload());
	await until(() => expect(fetchSpy).toHaveBeenCalledTimes(2));
	target = { path: "docs/plan.md", cwd: "/work/b", readTarget: "/work/b/docs/plan.md", provenance: "relative" };
	seen.length = 0;
	hook.rerender();
	expect(seen[0]).toBeNull();
	answers = [{ status: 503, body: "B offline" }];
	release(new Response("# Obsolete A"));
	await until(() => expect(hook.result.current.document?.kind).toBe("failed"));
	expect(fetchSpy.mock.calls[2]?.[0]).toContain("path=%2Fwork%2Fb%2Fdocs%2Fplan.md");
	act(() => hook.result.current.reload());
	await until(() => expect(hook.result.current.document).toMatchObject({ title: "Plan" }));
	fetchSpy.mockImplementationOnce(
		() =>
			new Promise<Response>((resolve) => {
				release = resolve;
			}),
	);
	const calls = fetchSpy.mock.calls.length;
	act(() => hook.result.current.reload());
	await until(() => expect(fetchSpy).toHaveBeenCalledTimes(calls + 1));
	harness.origin = "https://replacement.test";
	answers = [{ status: 503, body: "replacement offline" }];
	seen.length = 0;
	hook.rerender();
	expect(seen[0]).toBeNull();
	release(new Response("# Retired old origin"));
	await until(() => expect(hook.result.current.document?.kind).toBe("failed"));
	expect(fetchSpy.mock.calls.at(-1)).toEqual([
		"https://replacement.test/doc/file?format=raw&session=local%3Afix&path=%2Fwork%2Fb%2Fdocs%2Fplan.md",
		{ headers: { Authorization: "Bearer secret" } },
	]);
	hook.unmount();
});
