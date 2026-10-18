import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { renderHook, settle } from "../renderNative.testkit";
import { useShareIntake } from "./useShareIntake";
import type { SharedPayload } from "./sharePayload";
import { HOUR, share, sharePort } from "./shareTestkit";

const harness = vi.hoisted(() => ({
	urls: [] as ((event: { url: string }) => void)[],
	appState: [] as ((state: string) => void)[],
	initialURL: null as string | null,
}));

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	Linking: {
		getInitialURL: async () => harness.initialURL,
		addEventListener: (_event: string, listener: (event: { url: string }) => void) => {
			harness.urls.push(listener);
			return {
				remove: () => {
					harness.urls = harness.urls.filter((entry) => entry !== listener);
				},
			};
		},
	},
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

// The default port resolves the app group through expo-file-system, which
// vitest cannot load; these tests hand in their own port.
vi.mock("expo-file-system", () => ({ Paths: { appleSharedContainers: {} } }));

beforeEach(() => {
	harness.urls = [];
	harness.appState = [];
	harness.initialURL = null;
});

it("offers a share on a cold launch with no URL and no hub", async () => {
	const offered: SharedPayload[] = [];
	renderHook(() => useShareIntake({ port: sharePort([share("one", HOUR)]), onShare: (entry) => offered.push(entry) }));
	await settle();
	expect(offered.map((entry) => entry.id)).toEqual(["one"]);
});

it("offers a share when the extension's URL opens the app", async () => {
	const offered: SharedPayload[] = [];
	const subject = sharePort([share("one", HOUR)]);
	harness.initialURL = "evener-native://share";
	renderHook(() => useShareIntake({ port: subject, onShare: (entry) => offered.push(entry) }));
	await settle();
	expect(offered.map((entry) => entry.id)).toEqual(["one"]);
});

it("offers a share that arrived while the app was in front when it comes forward", async () => {
	const offered: SharedPayload[] = [];
	let later = sharePort([]);
	const subject = {
		list: () => later.list(),
		read: (name: string) => later.read(name),
		remove: (name: string) => later.remove(name),
	};
	renderHook(() => useShareIntake({ port: subject, onShare: (entry) => offered.push(entry) }));
	await settle();
	expect(offered).toEqual([]);
	// The extension writes the share while Evener is in the background.
	later = sharePort([share("one", HOUR)]);
	await act(async () => {
		for (const listener of harness.appState) listener("active");
	});
	await settle();
	expect(offered.map((entry) => entry.id)).toEqual(["one"]);
});

it("ignores a URL that is not a share, and still finds a pending share on launch", async () => {
	const offered: SharedPayload[] = [];
	harness.initialURL = "evener-native://elsewhere";
	renderHook(() => useShareIntake({ port: sharePort([share("one", HOUR)]), onShare: (entry) => offered.push(entry) }));
	await settle();
	expect(offered.map((entry) => entry.id)).toEqual(["one"]);
});

it("does not offer the same share twice while its sheet is up", async () => {
	const offered: SharedPayload[] = [];
	renderHook(() => useShareIntake({ port: sharePort([share("one", HOUR)]), onShare: (entry) => offered.push(entry) }));
	await act(async () => {
		for (const listener of harness.urls) listener({ url: "evener-native://share" });
	});
	await act(async () => {
		for (const listener of harness.appState) listener("active");
	});
	await settle();
	expect(offered.map((entry) => entry.id)).toEqual(["one"]);
});

it("stops listening when the app unmounts", async () => {
	const offered: SharedPayload[] = [];
	const tree = renderHook(() =>
		useShareIntake({ port: sharePort([share("one", HOUR)]), onShare: (entry) => offered.push(entry) }),
	);
	tree.unmount();
	await act(async () => {
		for (const listener of harness.appState) listener("active");
	});
	await settle();
	expect(offered).toEqual([]);
});

it("offers a pending share once, whatever the hub does", async () => {
	const offered: SharedPayload[] = [];
	const connection = { ready: false };
	const tree = renderHook(() =>
		useShareIntake({
			port: sharePort([share("one", HOUR)]),
			onShare: (entry) => offered.push(entry),
			ready: connection.ready,
		}),
	);
	await settle();
	expect(offered.map((entry) => entry.id)).toEqual(["one"]);
	connection.ready = true;
	tree.rerender();
	await settle();
	expect(offered.map((entry) => entry.id)).toEqual(["one"]);
	tree.rerender();
	await settle();
	expect(offered.map((entry) => entry.id)).toEqual(["one"]);
});

afterEach(() => {
	harness.urls = [];
	harness.appState = [];
});
