// Screen-level tests for the marketplace removal outcomes the browser cannot
// hold on its own: the PluginsPage-owned no-repeat guard fences a
// marketplace the hub says it already removed across browser remounts, the
// cleanup warning survives the browser's own revision fence and tab switches,
// a clean applied removal retires an obsolete cleanup warning, and a late
// outcome from a replaced client changes nothing on the new one. The fence
// covers only the window between an applied outcome and the first trusted
// read after it - presence retires it too (the fallback ruling), so a
// re-registration the wire cannot tell from a stale row can never stay
// fenced forever. The reconnect-recovery suite main added beside it
// (#1915 round-1 gap: a transport flap keeps the same client, so the
// screen's own mount effect never re-runs) shares this file's harness
// and mocks.
import type { ComponentProps } from "react";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { type AnyNotification, WireError, type PluginEntry } from "@evener/appwire-client";
import { HubWriteBusyError } from "@evener/appwire-client/state/extensions";
import { deferRequest, FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { ConversationClientLike } from "../conversation/services/conversation";
import { AddMarketplace, MarketplaceDetail } from "../MarketplaceBrowser";
import { timeAgo } from "../session/format";
import { PluginsPage } from "./PluginsPage";
import { PluginsStack } from "./pluginsStackTestUtils";
import {
	alertRequests,
	pressable,
	render,
	renderedText,
	screenConnection,
	testIDSuffixes,
	offersButton,
} from "../renderNative.testkit";
import { Button, Group, GroupFooter, Row } from "../sheet/Grouped";
import { SearchField } from "../sheet/SearchField";
import { marketplace, marketplaceRow, mountAdd, plugin, pluginsClient, problems } from "./pluginsPageTestUtils";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
}));

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

vi.mock("react-native-safe-area-context", () => ({
	SafeAreaView: "SafeAreaView",
}));

vi.mock("../ConnectionProvider", () => ({
	useConnection: () => harness.connection,
}));

// The page's focus: each registered effect runs once, as on first showing,
// and refocus() runs them again, as coming back to the page does.
const focus = vi.hoisted(() => ({ effects: new Set<() => void>() }));

vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => undefined | (() => void)) =>
			useEffect(() => {
				focus.effects.add(effect);
				effect();
				return () => {
					focus.effects.delete(effect);
				};
			}, [effect]),
	};
});

function refocus() {
	for (const effect of [...focus.effects]) effect();
}

function readyConnection(client: ConversationClientLike) {
	return screenConnection(client, "ready");
}

beforeEach(() => {
	alertRequests.length = 0;
});

it("holds Add marketplace open, and says Adding, while the add is in flight", async () => {
	let finish = () => {};
	const onAdd = vi.fn(() => new Promise<void>((resolve) => (finish = resolve)));
	const onClose = vi.fn();
	const tree = render(
		<AddMarketplace
			client={pluginsClient([]).client}
			connectionState="ready"
			hubName="Work hub"
			ready
			canUseConnection={() => true}
			onClose={onClose}
			onAdd={onAdd}
		/>,
	);
	const source = tree.root.findByProps({ accessibilityLabel: "Marketplace source" });
	act(() => source.props.onChangeText("https://example.test/plugins.git"));
	await act(async () => {
		tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Add" }).props.onPress();
	});
	const adding = tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Adding…" });
	expect(adding.props.accessibilityState).toEqual({ disabled: true, busy: true });
	const cancel = tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Cancel" });
	expect(cancel.props.disabled).toBe(true);
	act(() => tree.root.findByType("Modal" as never).props.onRequestClose());
	expect(onClose).not.toHaveBeenCalled();
	alertRequests.length = 0;
	await act(async () => finish());
	// The add landed: it closes without asking, though a source was typed.
	expect(onClose).toHaveBeenCalledOnce();
	expect(alertRequests).toHaveLength(0);
});

it("heads Add marketplace with the shared sheet header: its title and Cancel, and no second title in the body", () => {
	const onClose = vi.fn();
	const tree = render(
		<AddMarketplace
			client={pluginsClient([]).client}
			connectionState="ready"
			hubName="Work hub"
			ready
			canUseConnection={() => true}
			onClose={onClose}
			onAdd={async () => {}}
		/>,
	);
	// The sheet's title comes first; the form's section labels are headers too.
	const title = tree.root.findAllByProps({ accessibilityRole: "header" })[0];
	expect(title?.props.children).toBeTruthy();
	// The hub it adds to rides under the title.
	expect(renderedText(tree)).toContain("Work hub");
	act(() => tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Cancel" }).props.onPress());
	expect(onClose).toHaveBeenCalledOnce();
	// Only the header says it.
	const repeats = tree.root.findAll(
		(node) => String(node.type) === "Text" && node !== title && node.props.children === title?.props.children,
	);
	expect(repeats).toHaveLength(0);
});

it("keeps Add marketplace open when readiness is lost during submit", async () => {
	const hub = pluginsClient([]);
	const onAdd = vi.fn(async () => {});
	const onClose = vi.fn();
	let readinessChecks = 0;
	const tree = render(
		<AddMarketplace
			client={hub.client}
			connectionState="ready"
			hubName="Work hub"
			ready
			canUseConnection={() => readinessChecks++ === 0}
			onClose={onClose}
			onAdd={onAdd}
		/>,
	);
	const source = tree.root.findByProps({ accessibilityLabel: "Marketplace source" });
	act(() => source.props.onChangeText("https://example.test/plugins.git"));

	// The press passed whenReady's own recheck; the gate rechecks the same
	// predicate once more, after readiness was lost between the two. Nothing
	// ran, so the modal keeps the draft rather than closing as if it had.
	await act(async () => {
		tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Add" }).props.onPress();
	});

	expect(onAdd).not.toHaveBeenCalled();
	expect(onClose).not.toHaveBeenCalled();
	expect(tree.root.findByProps({ accessibilityLabel: "Marketplace source" }).props.value).toBe(
		"https://example.test/plugins.git",
	);
});

it("never renders the previous hub's retained client once the route names a hub whose profile is not active", async () => {
	// The panel-review gap: the route is re-keyed to hub B while the
	// connection still reports hub A - ready, with hub A's client. The
	// mismatch window's early return hides that data, but the retention
	// hooks must not record hub A's readiness under the route's hub either:
	// when the profile then moves to hub B mid-flap, hub B must meet a wall
	// for a hub it has never been ready for - not a banner over hub A's
	// retained client and rows.
	const hubA = pluginsClient([plugin]);
	harness.connection = {
		activeProfile: { id: "hub-a", name: "A hub" },
		client: hubA.client,
		state: "ready",
		fatal: false,
		downSince: null,
		lastLiveAt: null,
	};
	const props = {
		route: { params: { hubId: "hub-b" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	expect(testIDSuffixes(tree, "plugins-hub-not-selected")).toHaveLength(1);

	harness.connection = {
		activeProfile: { id: "hub-b", name: "B hub" },
		client: null,
		state: "reconnecting",
		fatal: false,
		downSince: null,
		lastLiveAt: null,
	};
	await act(async () => {
		tree.update(<PluginsStack {...props} />);
	});
	const rekeyed = renderedText(tree);
	expect(rekeyed).toContain("B hub");
	expect(testIDSuffixes(tree, "first-load-")).toEqual(["connecting"]);
	expect(rekeyed).not.toContain("demo-plugin");
});

// ---------------------------------------------------------------------------
// The page itself (spec 12's Plugins; rulings 7 and 9): three segments, the
// installed plugins grouped by marketplace with an "On by default" switch per
// row, a detail sheet per plugin, and nothing that asks you to reconnect or
// pull to refresh.

function entry(name: string, overrides: Partial<PluginEntry> = {}): PluginEntry {
	return { ...plugin, plugin: name, installPath: `/plugins/${name}`, ...overrides };
}

/** A hub whose installed list is `plugins` and whose only marketplace is
 * acme, with a two-plugin catalog. */
function pageHub(plugins: PluginEntry[]) {
	const hub = new FakeClient("ready");
	hub.on("evener/plugin/list", () => ({ plugins }));
	hub.on("evener/marketplace/list", () => ({ marketplaces: [marketplace] }));
	hub.on("evener/marketplace/browse", () => ({
		name: "acme",
		description: "Acme's tools",
		plugins: [{ name: "tool", description: "A tool" }],
	}));
	return hub;
}

async function mountPage(hub: FakeClient, params: Record<string, unknown> = {}) {
	harness.connection = readyConnection(hub as unknown as ConversationClientLike);
	const navigation = { setParams: vi.fn(), navigate: vi.fn() };
	const props = {
		route: { params: { hubId: "hub-1", ...params } },
		navigation,
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	return { tree, navigation, props };
}

async function choose(tree: ReturnType<typeof render>, segment: string) {
	await act(async () => {
		tree.root.findByProps({ accessibilityRole: "radio", accessibilityLabel: segment }).props.onPress();
	});
	await act(async () => {});
}

/** An installed plugin's row, which opens its detail. */
function pluginRow(tree: ReturnType<typeof render>, name: string) {
	const rows = tree.root.findAll(
		(node) =>
			node.props.accessibilityRole === "button" &&
			typeof node.props.accessibilityLabel === "string" &&
			node.props.accessibilityLabel.startsWith(`${name}, `),
	);
	if (rows.length !== 1) throw new Error(`expected one ${name} row, found ${rows.length}`);
	return rows[0];
}

async function openDetail(tree: ReturnType<typeof render>, name: string) {
	await act(async () => {
		pluginRow(tree, name).props.onPress();
	});
	await act(async () => {});
	return tree.root.findByType("Modal" as never);
}

it("asks the hub for updates once the installed list loads, and offers Upgrade only where it found one", async () => {
	const hub = pageHub([entry("stale"), entry("current")]);
	const releaseList = deferRequest<{ plugins: PluginEntry[] }>(hub, "evener/plugin/list");
	hub.on("evener/plugin/checkUpdates", () => {
		// The hub now holds the answer, so the list read that follows carries it.
		hub.on("evener/plugin/list", () => ({ plugins: [entry("stale", { updateAvailable: true }), entry("current")] }));
		return { plugins: [] };
	});
	const { tree } = await mountPage(hub);
	expect(hub.calls.some((call) => call.method === "evener/plugin/checkUpdates")).toBe(false);
	await act(async () => releaseList({ plugins: [entry("stale"), entry("current")] }));
	await act(async () => {});
	expect(hub.calls.filter((call) => call.method === "evener/plugin/checkUpdates")).toHaveLength(1);
	expect(pluginRow(tree, "stale").props.accessibilityLabel).toBe("stale, core, 1.0.0 · Update available");
	expect(pluginRow(tree, "current").props.accessibilityLabel).toBe("current, core, 1.0.0");

	expect((await openDetail(tree, "stale")).findAllByProps({ label: "Upgrade" }).length).toBeGreaterThan(0);
	await act(async () => {
		tree.root
			.findByType("Modal" as never)
			.findByProps({ accessibilityLabel: "Done" })
			.props.onPress();
	});
	expect((await openDetail(tree, "current")).findAllByProps({ label: "Upgrade" })).toHaveLength(0);
});

it("asks for no updates when the page closes before its installed list lands", async () => {
	const hub = pageHub([entry("stale")]);
	const releaseList = deferRequest<{ plugins: PluginEntry[] }>(hub, "evener/plugin/list");
	hub.on("evener/plugin/checkUpdates", () => ({ plugins: [] }));
	const { tree } = await mountPage(hub);
	await act(async () => tree.unmount());
	await act(async () => releaseList({ plugins: [entry("stale")] }));
	await act(async () => {});
	expect(hub.calls.some((call) => call.method === "evener/plugin/checkUpdates")).toBe(false);
});

/** Flaps the page's connection to reconnecting and back to ready. */
async function flap(tree: ReturnType<typeof render>, props: ComponentProps<typeof PluginsPage>) {
	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => tree.update(<PluginsStack {...props} />));
	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => tree.update(<PluginsStack {...props} />));
	await act(async () => {});
}

const checks = (hub: FakeClient) => hub.calls.filter((call) => call.method === "evener/plugin/checkUpdates").length;

it("asks for updates again after a reconnect when the check never reached the hub's answer", async () => {
	const hub = pageHub([entry("stale")]);
	hub.on("evener/plugin/checkUpdates", () => {
		throw new Error("connection closed");
	});
	const { tree, props } = await mountPage(hub);
	await act(async () => {});
	expect(checks(hub)).toBe(1);

	hub.on("evener/plugin/checkUpdates", () => ({ plugins: [] }));
	await flap(tree, props);
	expect(checks(hub)).toBe(2);
});

it("asks again when a check fails only after the connection has already come back", async () => {
	const hub = pageHub([entry("stale")]);
	let failCheck!: (err: Error) => void;
	hub.on(
		"evener/plugin/checkUpdates",
		() =>
			new Promise((_resolve, reject) => {
				failCheck = reject;
			}),
	);
	const { tree, props } = await mountPage(hub);
	await act(async () => {});
	expect(checks(hub)).toBe(1);

	// The connection drops and returns while the check is still pending; only
	// then does the check fail without an answer.
	await flap(tree, props);
	hub.on("evener/plugin/checkUpdates", () => ({ plugins: [] }));
	await act(async () => failCheck(new Error("connection closed")));
	await act(async () => {});
	expect(checks(hub)).toBe(2);
});

it("waits for the connection to be ready before asking again", async () => {
	const hub = pageHub([entry("stale")]);
	hub.on("evener/plugin/checkUpdates", () => {
		throw new Error("request timed out");
	});
	const { tree, props } = await mountPage(hub);
	await act(async () => {});
	expect(checks(hub)).toBe(1);

	hub.on("evener/plugin/checkUpdates", () => ({ plugins: [] }));
	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => tree.update(<PluginsStack {...props} />));
	expect(checks(hub)).toBe(1);
	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => tree.update(<PluginsStack {...props} />));
	await act(async () => {});
	expect(checks(hub)).toBe(2);
});

it("asks a hub that refused the check, or answered it, only once across reconnects and list failures", async () => {
	const hub = pageHub([entry("stale")]);
	hub.on("evener/plugin/checkUpdates", () => {
		throw new WireError("method not found", -32601);
	});
	const { tree, props } = await mountPage(hub);
	await act(async () => {});
	expect(checks(hub)).toBe(1);
	await flap(tree, props);
	expect(checks(hub)).toBe(1);

	// The list fails on one reconnect and recovers on the next.
	hub.on("evener/plugin/list", () => {
		throw new Error("hub busy");
	});
	await flap(tree, props);
	hub.on("evener/plugin/list", () => ({ plugins: [entry("stale")] }));
	await flap(tree, props);
	expect(checks(hub)).toBe(1);
});

it("asks for updates once a failed installed list recovers, not while it is failing", async () => {
	const hub = pageHub([entry("stale")]);
	hub.on("evener/plugin/list", () => {
		throw new Error("hub busy");
	});
	hub.on("evener/plugin/checkUpdates", () => ({ plugins: [] }));
	const { tree, props } = await mountPage(hub);
	expect(hub.calls.some((call) => call.method === "evener/plugin/checkUpdates")).toBe(false);

	// The same client flaps and comes back; the store re-reads its list.
	hub.on("evener/plugin/list", () => ({ plugins: [entry("stale")] }));
	await flap(tree, props);
	expect(checks(hub)).toBe(1);
});

it("says a broken plugin's row has an update when the hub found one", async () => {
	const { tree } = await mountPage(pageHub([entry("cracked", { broken: true, updateAvailable: true })]));
	expect(pluginRow(tree, "cracked").props.accessibilityLabel).toBe("cracked, core, Broken · Update available");
});

it("offers no Upgrade on a hub without the update check, and tells a broken plugin only to be removed", async () => {
	const { tree } = await mountPage(pageHub([entry("cracked", { broken: true })]));
	const detail = await openDetail(tree, "cracked");
	expect(detail.findAllByProps({ label: "Upgrade" })).toHaveLength(0);
	expect(testIDSuffixes(tree, "plugin-broken-")).toEqual(["remove-only"]);
});

it("shows Installed, Marketplaces and Browse, with Installed first", async () => {
	const { tree } = await mountPage(pageHub([entry("demo-plugin")]));
	const segments = tree.root.findAllByProps({ accessibilityRole: "radio" });
	expect(segments.map((node) => node.props.accessibilityLabel)).toEqual(["Installed", "Marketplaces", "Browse"]);
	expect(segments.map((node) => node.props.accessibilityState.checked)).toEqual([true, false, false]);

	await choose(tree, "Marketplaces");
	expect(marketplaceRow(tree, "acme").props.accessibilityLabel).toBe("acme, github: acme/plugins");
	expect(tree.root.findAllByProps({ accessibilityLabel: "Add marketplace" }).length).toBeGreaterThan(0);

	await choose(tree, "Browse");
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
	});
	await act(async () => {});
	expect(renderedText(tree)).toContain("A tool");
	expect(tree.root.findAllByProps({ accessibilityLabel: "Install tool from acme" }).length).toBeGreaterThan(0);
});

it("groups installed plugins by marketplace, with each one's version and state", async () => {
	const { tree } = await mountPage(
		pageHub([
			entry("demo-plugin"),
			entry("auto", { autoUpgrade: true }),
			entry("cracked", { broken: true }),
			entry("elsewhere", { marketplace: "acme" }),
		]),
	);
	const labels = tree.root
		.findAll((node) => node.props.accessibilityRole === "header")
		.map((node) => node.props.children);
	expect(labels).toEqual(["core", "acme"]);
	expect(pluginRow(tree, "demo-plugin").props.accessibilityLabel).toBe("demo-plugin, core, 1.0.0");
	expect(pluginRow(tree, "auto").props.accessibilityLabel).toBe("auto, core, 1.0.0 · Upgrades automatically");
	expect(pluginRow(tree, "cracked").props.accessibilityLabel).toBe("cracked, core, Broken");
});

it("turns a plugin on and off by default from its row's switch", async () => {
	const hub = pageHub([entry("demo-plugin")]);
	hub.on("evener/plugin/disable", () => ({ plugins: [entry("demo-plugin", { enabled: false })] }));
	hub.on("evener/plugin/enable", () => ({ plugins: [entry("demo-plugin")] }));
	const { tree } = await mountPage(hub);
	const toggle = () => tree.root.findByProps({ accessibilityLabel: "demo-plugin on by default" });
	expect(toggle().props.value).toBe(true);
	await act(async () => {
		toggle().props.onValueChange(false);
	});
	await act(async () => {});
	expect(hub.calls.at(-1)).toMatchObject({
		method: "evener/plugin/disable",
		params: { plugin: "demo-plugin", marketplace: "core" },
	});
	expect(toggle().props.value).toBe(false);
	await act(async () => {
		toggle().props.onValueChange(true);
	});
	await act(async () => {});
	expect(hub.calls.at(-1)).toMatchObject({
		method: "evener/plugin/enable",
		params: { plugin: "demo-plugin", marketplace: "core" },
	});
	expect(toggle().props.value).toBe(true);
});

it("says Already up to date when an upgrade changes neither version nor commit", async () => {
	const hub = pageHub([entry("demo-plugin", { gitCommitSha: "abc", updateAvailable: true })]);
	hub.on("evener/plugin/upgrade", () => ({ plugins: [entry("demo-plugin", { gitCommitSha: "abc" })] }));
	const { tree } = await mountPage(hub);
	const detail = await openDetail(tree, "demo-plugin");
	await act(async () => {
		detail.findByProps({ accessibilityLabel: "Upgrade" }).props.onPress();
	});
	await act(async () => {});
	expect(testIDSuffixes(tree, "plugin-notice-")).toEqual(["current"]);
});

it("shows no upgrade result for a plugin the answering list no longer carries", async () => {
	// The detail shows only a listed plugin, so an upgrade whose answer drops
	// it closes the detail; "Already up to date" is never said for it.
	const hub = pageHub([entry("demo-plugin", { gitCommitSha: "abc", updateAvailable: true })]);
	hub.on("evener/plugin/upgrade", () => ({ plugins: [] }));
	const { tree } = await mountPage(hub);
	const detail = await openDetail(tree, "demo-plugin");
	await act(async () => {
		detail.findByProps({ accessibilityLabel: "Upgrade" }).props.onPress();
	});
	await act(async () => {});
	expect(
		tree.root.findAll((node) => String(node.type) === "HoldingModal" || String(node.type) === "Modal"),
	).toHaveLength(0);
	expect(testIDSuffixes(tree, "plugin-notice-")).toEqual([]);
});

it("filters installed plugins by plugin or marketplace", async () => {
	const hub = pageHub([
		entry("demo-plugin", { marketplace: "core" }),
		entry("linter", { marketplace: "core" }),
		entry("tool", { marketplace: "acme" }),
	]);
	const { tree } = await mountPage(hub);
	const field = () =>
		tree.root.find(
			(node) => String(node.type) === "TextInput" && node.props.accessibilityLabel === "Filter installed plugins",
		);
	act(() => field().props.onChangeText("lint"));
	expect(renderedText(tree)).toContain("linter");
	expect(renderedText(tree)).not.toContain("demo-plugin");
	act(() => field().props.onChangeText("ACME"));
	expect(renderedText(tree)).toContain("tool");
	expect(renderedText(tree)).not.toContain("linter");
	act(() => field().props.onChangeText("nothing-like-it"));
	expect(testIDSuffixes(tree, "plugins-empty-")).toEqual(["filtered"]);
});

it("keeps the filter field while a filter is set, even once the list empties", async () => {
	let plugins = [entry("demo-plugin")];
	const hub = pageHub(plugins);
	hub.on("evener/plugin/list", () => ({ plugins }));
	const { tree } = await mountPage(hub);
	const fields = () =>
		tree.root.findAll(
			(node) => String(node.type) === "TextInput" && node.props.accessibilityLabel === "Filter installed plugins",
		);
	act(() => fields()[0]?.props.onChangeText("demo"));
	plugins = [];
	await act(async () => {
		hub.emitNotification({ method: "evener/plugin/updated", params: {} } as AnyNotification);
	});
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 300));
	});
	expect(fields()).toHaveLength(1);
});

it("reads the installed list again on coming back to the page after a read failed, with nothing to press", async () => {
	const hub = pageHub([entry("demo-plugin")]);
	let reads = 0;
	hub.on("evener/plugin/list", () => {
		reads += 1;
		if (reads === 1) throw new Error("hub busy");
		return { plugins: [entry("demo-plugin")] };
	});
	const { tree } = await mountPage(hub);
	await act(async () => {});
	expect(reads).toBe(1);
	expect(offersButton(tree, /\bRetry\b/)).toBe(false);
	await act(async () => {
		refocus();
	});
	await act(async () => {});
	expect(reads).toBe(2);
	expect(renderedText(tree)).toContain("demo-plugin");
});

it("says Upgraded to the new version when the upgrade's list carries one", async () => {
	const hub = pageHub([entry("demo-plugin", { gitCommitSha: "abc", updateAvailable: true })]);
	hub.on("evener/plugin/upgrade", () => ({
		plugins: [entry("demo-plugin", { version: "1.2.0", gitCommitSha: "def" })],
	}));
	const { tree } = await mountPage(hub);
	const detail = await openDetail(tree, "demo-plugin");
	await act(async () => {
		detail.findByProps({ accessibilityLabel: "Upgrade" }).props.onPress();
	});
	await act(async () => {});
	expect(testIDSuffixes(tree, "plugin-notice-")).toEqual(["upgraded"]);
	expect(renderedText(tree)).toContain("1.2.0");
});

it("holds the detail's switches, Upgrade and Remove, and says a broken plugin is broken", async () => {
	const hub = pageHub([entry("cracked", { broken: true, updateAvailable: true })]);
	hub.on("evener/plugin/setAutoUpgrade", () => ({ plugins: [entry("cracked", { broken: true, autoUpgrade: true })] }));
	const { tree } = await mountPage(hub);
	const detail = await openDetail(tree, "cracked");
	expect(testIDSuffixes(tree, "plugin-broken-")).toEqual(["upgradable"]);
	expect(detail.findByProps({ accessibilityLabel: "On by default" }).props.value).toBe(true);
	await act(async () => {
		detail.findByProps({ accessibilityLabel: "Upgrade automatically" }).props.onValueChange(true);
	});
	await act(async () => {});
	expect(hub.calls.at(-1)).toMatchObject({
		method: "evener/plugin/setAutoUpgrade",
		params: { plugin: "cracked", marketplace: "core", autoUpgrade: true },
	});
	await act(async () => {
		detail.findByProps({ accessibilityLabel: "Remove plugin" }).props.onPress();
	});
	expect(alertRequests.at(-1)?.buttons?.map((button) => button.style)).toEqual(["cancel", "destructive"]);
	expect(alertRequests.at(-1)?.message).toContain("cracked");
	await act(async () => {
		detail.findByProps({ accessibilityLabel: "Done" }).props.onPress();
	});
	expect(tree.root.findAllByType("Modal" as never)).toHaveLength(0);
});

it("heads a plugin's detail with the shared sheet header: its name, and Done on the right", async () => {
	const { tree } = await mountPage(pageHub([entry("cracked")]));
	const detail = await openDetail(tree, "cracked");
	// The sheet's title comes first; the page's section labels are headers too.
	expect(detail.findAllByProps({ accessibilityRole: "header" })[0]?.props.children).toBe("cracked");
	expect(detail.findAllByProps({ accessibilityRole: "button", accessibilityLabel: "Cancel" })).toHaveLength(0);
	await act(async () => {
		detail.findByProps({ accessibilityRole: "button", accessibilityLabel: "Done" }).props.onPress();
	});
	expect(tree.root.findAllByType("Modal" as never)).toHaveLength(0);
});

// The page clears a link's focus once it acts, so the same plugin named again
// by a later link opens again after its detail was closed.
it("opens a plugin again when a later link names it again", async () => {
	const hub = pageHub([entry("demo-plugin"), entry("cracked", { broken: true })]);
	const target = { plugin: "cracked", marketplace: "core" };
	const { tree, props } = await mountPage(hub, { focus: target });
	const detail = tree.root.findByType("Modal" as never);
	await act(async () => {
		detail.findByProps({ accessibilityRole: "button", accessibilityLabel: "Done" }).props.onPress();
	});
	expect(tree.root.findAllByType("Modal" as never)).toHaveLength(0);
	const withFocus = (focus: typeof target | undefined) => (
		<PluginsStack {...props} route={{ ...props.route, params: { ...props.route.params, focus } }} />
	);
	await act(async () => tree.update(withFocus(undefined)));
	await act(async () => tree.update(withFocus({ ...target })));
	await act(async () => {});
	expect(
		tree.root.findByType("Modal" as never).findAllByProps({ accessibilityLabel: "Remove plugin" }).length,
	).toBeGreaterThan(0);
});

it("opens the plugin a notice named once, then clears the focus", async () => {
	const hub = pageHub([entry("demo-plugin"), entry("cracked", { broken: true, updateAvailable: true })]);
	const { tree, navigation } = await mountPage(hub, { focus: { plugin: "cracked", marketplace: "core" } });
	const detail = tree.root.findByType("Modal" as never);
	expect(detail.findAllByProps({ accessibilityLabel: "Remove plugin" }).length).toBeGreaterThan(0);
	expect(testIDSuffixes(tree, "plugin-broken-")).toEqual(["upgradable"]);
	expect(navigation.setParams).toHaveBeenCalledWith({ focus: undefined });
});

it("leaves an open plugin's late result behind when a link opens another plugin", async () => {
	const hub = pageHub([entry("demo-plugin", { updateAvailable: true }), entry("other", { updateAvailable: true })]);
	let fail: (reason: Error) => void = () => {};
	hub.on(
		"evener/plugin/upgrade",
		() =>
			new Promise((_resolve, reject) => {
				fail = reject;
			}),
	);
	const { tree, props } = await mountPage(hub);
	const detail = await openDetail(tree, "demo-plugin");
	await act(async () => {
		detail.findByProps({ accessibilityLabel: "Upgrade" }).props.onPress();
	});
	const other = { plugin: "other", marketplace: entry("other").marketplace };
	await act(async () => {
		tree.update(
			<PluginsStack {...props} route={{ ...props.route, params: { ...props.route.params, focus: other } }} />,
		);
	});
	await act(async () => fail(new Error("upstream 502")));
	await act(async () => {});
	expect(
		tree.root.findByType("Modal" as never).findAllByProps({ accessibilityLabel: "Upgrade" }).length,
	).toBeGreaterThan(0);
	expect(renderedText(tree)).toContain("other");
	expect(problems(tree)).not.toContain("write-failed");
});

it("updates a marketplace's source and removes it from its detail in Marketplaces", async () => {
	const hub = pageHub([]);
	hub.on("evener/marketplace/refresh", () => ({ marketplaces: [marketplace] }));
	const { tree } = await mountPage(hub);
	await choose(tree, "Marketplaces");
	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	// The detail is the marketplace's, not its catalog: Browse reads that.
	expect(hub.calls.some((call) => call.method === "evener/marketplace/browse")).toBe(false);
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Update source" }).props.onPress();
	});
	await act(async () => {});
	expect(hub.calls.at(-1)).toMatchObject({ method: "evener/marketplace/refresh", params: { name: "acme" } });
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
	});
	expect(alertRequests.at(-1)?.buttons?.map((button) => button.style)).toEqual(["cancel", "destructive"]);
	expect(alertRequests.at(-1)?.message).toContain("acme");
});

it("installs a catalog's plugin from Browse", async () => {
	const hub = pageHub([]);
	hub.on("evener/plugin/install", () => ({ plugins: [entry("tool", { marketplace: "acme" })] }));
	const { tree } = await mountPage(hub);
	await choose(tree, "Browse");
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
	});
	await act(async () => {});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Install tool from acme" }).props.onPress();
	});
	await act(async () => {});
	expect(hub.calls.at(-1)).toMatchObject({
		method: "evener/plugin/install",
		params: { plugin: "tool", marketplace: "acme" },
	});
	expect(tree.root.findAllByProps({ accessibilityLabel: "Open tool from acme" }).length).toBeGreaterThan(0);
});

it("pushes a marketplace's page from its list, so the edge swipe returns there (audit M8)", async () => {
	const hub = pageHub([]);
	harness.connection = readyConnection(hub as unknown as ConversationClientLike);
	const navigation = { setParams: vi.fn(), navigate: vi.fn(), push: vi.fn() };
	const props = { route: { params: { hubId: "hub-1" } }, navigation } as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsPage {...props} />);
	await act(async () => {});
	await choose(tree, "Browse");
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
	});
	expect(navigation.push).toHaveBeenCalledWith("Marketplace", { hubId: "hub-1", name: "acme", segment: "browse" });
	await choose(tree, "Marketplaces");
	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	expect(navigation.push).toHaveBeenLastCalledWith("Marketplace", {
		hubId: "hub-1",
		name: "acme",
		segment: "marketplaces",
	});
	// The list stays whole: nothing drills in place.
	expect(tree.root.findAllByType(MarketplaceDetail)).toHaveLength(0);
});

it("opens an installed plugin from its marketplace's page on the Plugins page, with its detail up", async () => {
	const hub = pageHub([entry("tool", { marketplace: "acme" })]);
	const { tree } = await mountPage(hub);
	await choose(tree, "Browse");
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
	});
	await act(async () => {});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Open tool from acme" }).props.onPress();
	});
	await act(async () => {});
	expect(tree.root.findAllByProps({ accessibilityLabel: "Back to Plugins" })).toHaveLength(0);
	expect(tree.root.findByType("Modal" as never).props.visible).not.toBe(false);
	expect(
		tree.root.findByType("Modal" as never).findAllByProps({ accessibilityLabel: "Remove plugin" }).length,
	).toBeGreaterThan(0);
});

it("filters a marketplace's catalog with the shared search field, as Installed does", async () => {
	const hub = pageHub([]);
	hub.on("evener/marketplace/browse", () => ({
		name: "acme",
		plugins: [
			{ name: "tool", description: "A tool" },
			{ name: "gadget", description: "A gadget" },
		],
	}));
	const { tree } = await mountPage(hub);
	await choose(tree, "Browse");
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
	});
	await act(async () => {});
	const field = tree.root.findByType(SearchField);
	await act(async () => field.props.onChangeText("gad"));
	expect(tree.root.findAllByProps({ accessibilityLabel: "Install gadget from acme" }).length).toBeGreaterThan(0);
	expect(tree.root.findAllByProps({ accessibilityLabel: "Install tool from acme" })).toHaveLength(0);
});

it("shows a failed list read as a line with nothing to press", async () => {
	const hub = new FakeClient("ready");
	hub.on("evener/plugin/list", () => {
		throw new Error("list unavailable");
	});
	const { tree } = await mountPage(hub);
	expect(problems(tree)).toEqual(["list-failed"]);
	expect(offersButton(tree, /\bRetry\b/)).toBe(false);
});

it("never asks to reconnect or offers pull-to-refresh, in any segment", async () => {
	const hub = pageHub([entry("demo-plugin")]);
	const { tree } = await mountPage(hub);
	for (const segment of ["Installed", "Marketplaces", "Browse"]) {
		await choose(tree, segment);
		if (segment !== "Installed") {
			await act(async () => {
				(segment === "Browse"
					? tree.root.findByProps({ accessibilityLabel: "Browse acme" })
					: marketplaceRow(tree, "acme")
				).props.onPress();
			});
			await act(async () => {});
		}
		expect(offersButton(tree, /\bReconnect\b/)).toBe(false);
		expect(tree.root.findAll((node) => node.props.onRefresh !== undefined)).toHaveLength(0);
	}
	await choose(tree, "Installed");
	await openDetail(tree, "demo-plugin");
	expect(offersButton(tree, /\bReconnect\b/)).toBe(false);
});

it("waits for the installed list in the same padded space every Hub page waits in", async () => {
	const hub = pageHub([]);
	hub.on("evener/plugin/list", () => new Promise(() => {}));
	const { tree } = await mountPage(hub);
	const spinner = tree.root.findByProps({ accessibilityLabel: "Loading installed plugins" });
	expect(spinner.props.style).toEqual({ padding: 32 });
});

it("shows the busy copy when the shared gate refuses the add", async () => {
	const tree = render(
		<AddMarketplace
			client={pluginsClient([]).client}
			connectionState="ready"
			hubName="Work hub"
			ready
			canUseConnection={() => true}
			onClose={vi.fn()}
			onAdd={() => Promise.reject(new HubWriteBusyError())}
		/>,
	);
	act(() => tree.root.findByProps({ accessibilityLabel: "Marketplace source" }).props.onChangeText("acme/plugins"));
	await act(async () => {
		tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Add" }).props.onPress();
	});
	// A refusal is not a failure: the modal stays open on the busy copy.
	expect(testIDSuffixes(tree, "form-error-")).toEqual(["write-busy"]);
});

it("closes an untouched Add marketplace at once, by Cancel or a swipe", () => {
	alertRequests.length = 0;
	const { tree, onClose } = mountAdd();
	act(() => tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Cancel" }).props.onPress());
	act(() => tree.root.findByType("Modal" as never).props.onRequestClose());
	expect(onClose).toHaveBeenCalledTimes(2);
	expect(alertRequests).toHaveLength(0);
});

it("asks before Cancel or a swipe throws away a typed source (spec 6)", () => {
	alertRequests.length = 0;
	const { tree, onClose } = mountAdd();
	act(() => tree.root.findByProps({ accessibilityLabel: "Marketplace source" }).props.onChangeText("acme/plugins"));
	act(() => tree.root.findByType("Modal" as never).props.onRequestClose());
	expect(onClose).not.toHaveBeenCalled();
	expect(alertRequests).toHaveLength(1);
	act(() =>
		alertRequests
			.at(-1)
			?.buttons?.find((button) => button.text === "Keep editing")
			?.onPress?.(),
	);
	expect(onClose).not.toHaveBeenCalled();
	act(() => tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Cancel" }).props.onPress());
	act(() =>
		alertRequests
			.at(-1)
			?.buttons?.find((button) => button.text === "Discard")
			?.onPress?.(),
	);
	expect(onClose).toHaveBeenCalledOnce();
});

it("says when a plugin was installed and last updated, and leaves out a time the hub doesn't know (audit M9)", async () => {
	const nowSeconds = Math.floor(Date.now() / 1000);
	const hub = pageHub([
		entry("demo-plugin", { installedAt: nowSeconds - 3 * 86400, lastUpdated: nowSeconds - 5 * 3600 }),
		entry("fresh", { installedAt: 0, lastUpdated: 0 }),
	]);
	const { tree } = await mountPage(hub);
	const detail = await openDetail(tree, "demo-plugin");
	expect(detail.findAllByProps({ accessibilityLabel: "Installed, 3d ago" }).length).toBeGreaterThan(0);
	expect(detail.findAllByProps({ accessibilityLabel: "Updated, 5h ago" }).length).toBeGreaterThan(0);
	await act(async () => {
		tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Done" }).props.onPress();
	});
	const fresh = await openDetail(tree, "fresh");
	expect(fresh.findAll((node) => node.props.label === "Installed" || node.props.label === "Updated")).toHaveLength(0);
});

it("describes an installed plugin from its marketplace's catalog, as the web does (audit M9)", async () => {
	const hub = pageHub([entry("tool", { marketplace: "acme" })]);
	const { tree } = await mountPage(hub);
	await openDetail(tree, "tool");
	await act(async () => {});
	expect(renderedText(tree)).toContain("A tool");
	expect(hub.calls.filter((call) => call.method === "evener/marketplace/browse")).toHaveLength(1);
});

it("says a broken plugin is broken under the actions that fix it, not floating above them (audit M9)", async () => {
	const hub = pageHub([entry("cracked", { broken: true, updateAvailable: true })]);
	const { tree } = await mountPage(hub);
	const detail = await openDetail(tree, "cracked");
	const nodes = detail.findAll(() => true);
	const actions = nodes.findIndex(
		(node) => node.type === Group && node.findAllByProps({ label: "Upgrade" }).length > 0,
	);
	const warning = nodes.findIndex(
		(node) => node.type === GroupFooter && node.props.children === "This plugin is broken. Upgrade it or remove it.",
	);
	expect(actions).toBeGreaterThan(-1);
	expect(warning).toBeGreaterThan(actions);
});

it("says a time from a clock ahead of this phone's was just now, not a count of 0 (audit M9)", async () => {
	const nowSeconds = Math.floor(Date.now() / 1000);
	const hub = pageHub([entry("demo-plugin", { installedAt: nowSeconds + 3600, lastUpdated: nowSeconds + 3600 })]);
	const { tree } = await mountPage(hub);
	const detail = await openDetail(tree, "demo-plugin");
	expect(detail.findAllByProps({ accessibilityLabel: "Installed, just now" }).length).toBeGreaterThan(0);
	expect(detail.findAllByProps({ accessibilityLabel: "Updated, just now" }).length).toBeGreaterThan(0);
});

it("ages a plugin's time with the same wording as any other age in the app", async () => {
	// The clock stands still, so the time is exactly thirty seconds old when
	// the detail reads it.
	vi.useFakeTimers({ toFake: ["Date"] });
	try {
		const thirtySecondsAgo = (Date.now() - 30_000) / 1000;
		const hub = pageHub([entry("demo-plugin", { installedAt: thirtySecondsAgo, lastUpdated: thirtySecondsAgo })]);
		const { tree } = await mountPage(hub);
		const detail = await openDetail(tree, "demo-plugin");
		expect(detail.findAllByProps({ accessibilityLabel: `Updated, ${timeAgo(30_000)}` }).length).toBeGreaterThan(0);
	} finally {
		vi.useRealTimers();
	}
});

it.each([
	[
		"the catalog read fails",
		() => {
			throw new Error("catalog unavailable");
		},
	],
	[
		"the catalog doesn't list the plugin",
		() => ({ name: "acme", plugins: [{ name: "other", description: "Another" }] }),
	],
])("leaves out About when %s, and shows the rest", async (_name, browse) => {
	const hub = pageHub([entry("tool", { marketplace: "acme", updateAvailable: true })]);
	hub.on("evener/marketplace/browse", browse);
	const { tree } = await mountPage(hub);
	const detail = await openDetail(tree, "tool");
	await act(async () => {});
	expect(detail.findAll((node) => node.props.label === "About")).toHaveLength(0);
	expect(detail.findAllByProps({ label: "Version" }).length).toBeGreaterThan(0);
	expect(detail.findAllByProps({ label: "Upgrade" }).length).toBeGreaterThan(0);
});

it("points an empty plugin list at Browse when the hub has marketplaces, and at adding one when it has none (audit L6)", async () => {
	const installed = async (hub: FakeClient) => {
		const { tree } = await mountPage(hub);
		// Before the hub's marketplaces are read, it points at nothing.
		expect(testIDSuffixes(tree, "plugins-empty-")).toEqual(["unknown"]);
		await choose(tree, "Marketplaces");
		await choose(tree, "Installed");
		return testIDSuffixes(tree, "plugins-empty-");
	};
	expect(await installed(pageHub([]))).toEqual(["has-marketplaces"]);
	const hub = pageHub([]);
	hub.on("evener/marketplace/list", () => ({ marketplaces: [] }));
	expect(await installed(hub)).toEqual(["no-marketplaces"]);
});

it("points an empty marketplace list at the action its own segment has (audit L6)", async () => {
	const hub = pageHub([]);
	hub.on("evener/marketplace/list", () => ({ marketplaces: [] }));
	const { tree } = await mountPage(hub);
	await choose(tree, "Marketplaces");
	expect(testIDSuffixes(tree, "marketplaces-empty-")).toEqual(["marketplaces"]);
	expect(tree.root.findAllByProps({ accessibilityLabel: "Add marketplace" }).length).toBeGreaterThan(0);
	// Browse has no Add row: it points at the segment that does.
	await choose(tree, "Browse");
	expect(testIDSuffixes(tree, "marketplaces-empty-")).toEqual(["browse"]);
	expect(tree.root.findAllByProps({ accessibilityLabel: "Add marketplace" })).toHaveLength(0);
});

it("offers Install and Open as a catalog row's own mini button, as the prototype does, not the whole row (audit M13)", async () => {
	const hub = pageHub([entry("tool", { marketplace: "acme" })]);
	hub.on("evener/marketplace/browse", () => ({
		name: "acme",
		plugins: [
			{ name: "tool", description: "A tool" },
			{ name: "gadget", description: "A gadget" },
		],
	}));
	const { tree } = await mountPage(hub);
	await choose(tree, "Browse");
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
	});
	await act(async () => {});
	const minis = tree.root.findAll((node) => node.type === Button && node.props.mini === true);
	expect(minis.map((node) => [node.props.label, node.props.accessibilityLabel])).toEqual([
		["Open", "Open tool from acme"],
		["Install", "Install gadget from acme"],
	]);
	// The row itself is one plain reading beside its button: the button is the
	// row's only target.
	const row = tree.root.find((node) => node.type === Row && node.props.label === "gadget");
	const elements = row.findAll(
		(node) => typeof node.type === "string" && (node.props.accessible === true || String(node.type) === "Pressable"),
	);
	expect(elements.map((node) => [String(node.type), node.props.accessibilityLabel])).toEqual([
		["View", "gadget, A gadget"],
		["Pressable", "Install gadget from acme"],
	]);
	hub.on("evener/plugin/install", () => ({
		plugins: [entry("tool", { marketplace: "acme" }), entry("gadget", { marketplace: "acme" })],
	}));
	await act(async () => {
		pressable(tree, "Install gadget from acme")?.props.onPress();
	});
	await act(async () => {});
	expect(hub.calls.at(-1)).toMatchObject({
		method: "evener/plugin/install",
		params: { plugin: "gadget", marketplace: "acme" },
	});
});
