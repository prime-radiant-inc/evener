import type { AnyNotification, PluginPreviewResponse } from "@evener/appwire-client";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { createNewSessionStore } from "../newSession";
import { render } from "../renderNative.testkit";
import { pluginChoice, useSheetPlugins } from "./sheetPlugins";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

/** Past usePluginPreview's 250ms debounce. */
const debounce = () =>
	act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 300));
	});

const candidate = (name: string, selected = true) => ({
	name,
	source: "installed" as const,
	marketplace: "superpowers-marketplace",
	selected,
	skillCount: 1,
	agentCount: 0,
	commandCount: 0,
	hookCount: 0,
	mcpCount: 0,
});
const preview: PluginPreviewResponse = { plugins: [candidate("superpowers"), candidate("go", false)] };

function mount(ready: boolean) {
	const calls: { method: string; params: unknown }[] = [];
	let notify: (notification: AnyNotification) => void = () => {};
	const client = {
		request: async (method: string, params: unknown) => {
			calls.push({ method, params });
			return preview;
		},
		onNotification: (handler: (notification: AnyNotification) => void) => {
			notify = handler;
			return () => {};
		},
	};
	const store = createNewSessionStore("hub-1");
	store.setState({ source: "paradise-park", cwd: "/Users/jesse/git/evener" });
	let state: ReturnType<typeof useSheetPlugins> | null = null;
	let isReady = ready;
	function Probe() {
		state = useSheetPlugins(store, client as never, isReady);
		return null;
	}
	const tree = render(<Probe />);
	const setReady = (next: boolean) =>
		act(() => {
			isReady = next;
			tree.update(<Probe />);
		});
	return { calls, store, state: () => state, setReady, notify: (n: AnyNotification) => notify(n) };
}

it("previews nothing while the sheet isn't ready, then previews on the chosen host", async () => {
	const sheet = mount(false);
	await debounce();
	expect(sheet.calls).toEqual([]);
	sheet.setReady(true);
	await debounce();
	expect(sheet.calls).toEqual([
		{
			method: "evener/host/request",
			params: { host: "paradise-park", method: "evener/plugin/preview", params: { cwd: "/Users/jesse/git/evener" } },
		},
	]);
	expect(sheet.state()).toEqual({ status: "ready", response: preview });
});

it("previews again when the hub's plugins or launch settings change", async () => {
	const sheet = mount(true);
	await debounce();
	await act(async () => sheet.notify({ method: "evener/plugin/updated", params: {} } as never));
	await debounce();
	await act(async () => sheet.notify({ method: "evener/launch/updated", params: {} } as never));
	await debounce();
	expect(sheet.calls).toHaveLength(3);
});

it("reads the selection against the preview: names on, total, and blocking issues", () => {
	expect(pluginChoice({}, { status: "ready", response: preview })).toMatchObject({
		on: ["superpowers"],
		total: 2,
		issues: [],
	});
	expect(pluginChoice({ enabledPlugins: ["gone"] }, { status: "ready", response: preview })).toMatchObject({
		on: [],
		issues: [{ name: "gone", reason: "not present in current preview" }],
	});
	expect(pluginChoice({}, { status: "loading" })).toMatchObject({ response: null, on: [], total: 0, issues: [] });
});

/** A sheet whose hub answers each place's preview when the test says. */
function mountHeld() {
	const pending: { params: unknown; resolve(value: PluginPreviewResponse): void }[] = [];
	const client = {
		request: (_method: string, params: unknown) =>
			new Promise<PluginPreviewResponse>((resolve) => pending.push({ params, resolve })),
		onNotification: () => () => {},
	};
	const store = createNewSessionStore("hub-1");
	store.setState({ source: "local", cwd: "/home/jesse/git/evener", launchOverrides: { enabledPlugins: ["gone"] } });
	let state: ReturnType<typeof useSheetPlugins> | null = null;
	function Probe() {
		state = useSheetPlugins(store, client as never, true);
		return null;
	}
	render(<Probe />);
	const choice = () => pluginChoice(store.getState().launchOverrides, state as never);
	return { store, pending, choice, state: () => state };
}

for (const [what, move] of [
	["host", { source: "paradise-park" }],
	["project", { cwd: "/home/jesse/git/docs" }],
] as const) {
	it(`never shows the last ${what}'s plugins or problems while the new one's preview loads`, async () => {
		const sheet = mountHeld();
		await debounce();
		await act(async () => sheet.pending[0]?.resolve(preview));
		expect(sheet.choice()).toMatchObject({ total: 2, issues: [{ name: "gone" }] });
		act(() => sheet.store.setState(move));
		expect(sheet.state()).toEqual({ status: "loading" });
		expect(sheet.choice()).toMatchObject({ response: null, on: [], total: 0, issues: [] });
		await debounce();
		await act(async () => sheet.pending[1]?.resolve({ plugins: [candidate("gone")] }));
		expect(sheet.choice()).toMatchObject({ total: 1, on: ["gone"], issues: [] });
	});
}
