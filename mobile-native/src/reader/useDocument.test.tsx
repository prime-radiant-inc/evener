import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { renderHook } from "../renderNative.testkit";
import { useDocument } from "./useDocument";

const harness = vi.hoisted(() => ({
	state: "ready" as string,
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
		profiles: [{ id: "studio", name: "Studio", origin: "https://hub.test" }],
		state: harness.state,
	}),
}));

type Answer = { status: number; body: string } | "offline";
let answers: Answer[] = [];
let fetchSpy: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
	harness.state = "ready";
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
	vi.restoreAllMocks();
});

async function settle() {
	await act(async () => {
		for (let turn = 0; turn < 10; turn += 1) await Promise.resolve();
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
}

function mount() {
	return renderHook(() => useDocument("studio", "local:fix", "docs/plan.md"));
}

it("reads /doc/file with the hub's bearer token and returns the document", async () => {
	answers = [{ status: 200, body: "# Settle the race\n\nFirst." }];
	const hook = mount();
	expect(hook.result.current.document).toBeNull();
	await settle();
	expect(fetchSpy).toHaveBeenCalledWith(
		"https://hub.test/doc/file?format=raw&session=local%3Afix&path=docs%2Fplan.md",
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
