// Screen-level tests for the recovery the reconnect banners promise: a
// screen that survives a connection flap behind a banner must also catch up
// on what changed while it was away. The wall this screen used to show
// remounted the plugins store on every recovery, so the catch-up read came
// free with the remount; keeping the store mounted hands that duty to the
// store's own reconnect recovery (storeLifecycle.ts), which only runs when a
// host drives connectionChanged - the way useCredentialStore drives the
// credential store (credentialStore.ts). Mirrors ProvidersScreen.test.tsx's
// mocking: every native edge the screen reaches is mocked here and nowhere
// else, and the hub is the SDK's FakeClient.
import type { ComponentProps } from "react";
import {
	act,
	type ReactTestInstance,
} from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type {
	ConnectionState,
	MarketplaceEntry,
	PluginEntry,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import {
	createMarketplacesStore,
	createPluginsStore,
} from "@evener/appwire-client/state/extensions";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { MarketplaceBrowser } from "./MarketplaceBrowser";
import { PluginsScreen } from "./PluginsScreen";
import { createPluginMutationGate } from "./pluginMutationGate";
import {
	nativeModuleMock,
	render,
	renderedText,
	screenConnection as connection,
} from "./renderNative.testkit";

// What useConnection answers with. vi.hoisted because vi.mock's factory is
// hoisted above every module import and may not close over a module-level let.
const harness = vi.hoisted(() => ({ connection: {} as Record<string, unknown> }));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	Switch: "Switch",
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("./ConnectionProvider", () => ({ useConnection: () => harness.connection }));

const props = {
	route: { params: { hubId: "hub-1" } },
} as unknown as ComponentProps<typeof PluginsScreen>;

const ACME: MarketplaceEntry = {
	name: "acme",
	source: { kind: "github", repo: "acme/plugins" },
	lastUpdated: 1,
};

// The residue guard PluginsScreen wires around the browser in production, in
// the minimal shape a reconnect test needs: nothing is fenced, nothing is
// recorded, and every authoritative read reports to nobody. These tests
// exercise the connection's own recovery, never a marketplace removal.
const NO_APPLIED_REMOVALS: ReadonlySet<string> = new Set();

function plugin(name: string): PluginEntry {
	return {
		plugin: name,
		marketplace: "acme",
		version: "1.0.0",
		enabled: true,
		autoUpgrade: false,
		broken: false,
		installPath: "/plugins",
		installedAt: 1,
		lastUpdated: 1,
	};
}

/** Every string under a node - the modal-scoped counterpart of renderedText.
 * The host mock renders modal content regardless of its visible prop, so
 * the modal a flap-banner test scopes to is found by what it contains. */
function subtreeText(node: ReactTestInstance): string {
	const chunks: string[] = [];
	const visit = (value: ReactTestInstance | ReactTestInstance[] | string) => {
		if (typeof value === "string") {
			chunks.push(value);
			return;
		}
		if (Array.isArray(value)) {
			for (const entry of value) visit(entry);
			return;
		}
		for (const child of value.children) visit(child);
	};
	visit(node);
	return chunks.join(" ");
}

function modalContaining(
	tree: ReturnType<typeof render>,
	needle: string,
): ReactTestInstance {
	const modals = tree.root
		.findAll((node) => (node.type as unknown as string) === "Modal")
		.filter((modal) => subtreeText(modal).includes(needle));
	if (modals.length !== 1)
		throw new Error(`expected one modal containing "${needle}"`);
	return modals[0];
}

it("reads the plugin list again once a flap the screen survived is ready again", async () => {
	const hub = new FakeClient("ready");
	let reads = 0;
	hub.on("evener/plugin/list", () => {
		reads += 1;
		return {
			plugins:
				reads === 1
					? [plugin("kept")]
					: [plugin("kept"), plugin("added-while-away")],
		};
	});
	harness.connection = connection(hub, "ready");
	const tree = render(<PluginsScreen {...props} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("kept");
	expect(reads).toBe(1);

	harness.connection = connection(hub, "reconnecting");
	await act(async () => {
		tree.update(<PluginsScreen {...props} />);
	});
	harness.connection = connection(hub, "ready");
	await act(async () => {
		tree.update(<PluginsScreen {...props} />);
	});
	await act(async () => {});
	await act(async () => {});
	// The hub broadcasts a change only to clients connected when it happens, so
	// everything that moved while this one was away arrives as nothing at all;
	// the recovery read is what catches the screen up on it.
	expect(reads).toBe(2);
	expect(renderedText(tree)).toContain("added-while-away");
});

it("shows the connection status and reconnect inside an open plugin detail modal", async () => {
	const hub = new FakeClient("ready");
	hub.on("evener/plugin/list", () => ({ plugins: [plugin("kept")] }));
	harness.connection = connection(hub, "ready");
	const tree = render(<PluginsScreen {...props} />);
	await act(async () => {});
	const row = tree.root.find(
		(node) =>
			typeof node.props.accessibilityLabel === "string" &&
			node.props.accessibilityLabel.startsWith("kept"),
	);
	act(() => {
		row.props.onPress();
	});
	await act(async () => {});

	// The connection drops with the detail modal open: the native modal
	// covers the screen's banner, so the status and the manual reconnect
	// live inside it, with the modal's own content intact.
	harness.connection = connection(hub, "reconnecting");
	await act(async () => {
		tree.update(<PluginsScreen {...props} />);
	});
	const modal = modalContaining(tree, "Installation details");
	expect(subtreeText(modal)).toContain("reconnecting");
	expect(
		modal.findAll(
			(node) => node.props.accessibilityLabel === "Reconnect",
		).length,
	).toBeGreaterThan(0);
	expect(subtreeText(modal)).toContain("Installation details");
});

it("keeps the last action's notice when a disconnected press never runs the action", async () => {
	const hub = new FakeClient("ready");
	hub.on("evener/plugin/list", () => ({ plugins: [plugin("kept")] }));
	let upgrades = 0;
	hub.on("evener/plugin/upgrade", () => {
		upgrades += 1;
		return { plugins: [plugin("kept")] };
	});
	harness.connection = connection(hub, "ready");
	const tree = render(<PluginsScreen {...props} />);
	await act(async () => {});
	const row = tree.root.find(
		(node) =>
			typeof node.props.accessibilityLabel === "string" &&
			node.props.accessibilityLabel.startsWith("kept"),
	);
	act(() => {
		row.props.onPress();
	});
	await act(async () => {});

	// A successful upgrade leaves its notice in the open modal.
	await act(async () => {
		modalContaining(tree, "Installation details")
			.findByProps({ accessibilityLabel: "Upgrade" })
			.props.onPress();
	});
	await act(async () => {});
	expect(upgrades).toBe(1);
	expect(
		subtreeText(modalContaining(tree, "Installation details")),
	).toContain("Checked for upgrades.");

	// The connection drops with the modal open: a press now is a no-op the
	// gate refuses on readiness, and it must not retire the notice the last
	// real outcome left - the status inside the modal already says why
	// nothing ran.
	harness.connection = connection(hub, "reconnecting");
	await act(async () => {
		tree.update(<PluginsScreen {...props} />);
	});
	await act(async () => {
		modalContaining(tree, "Installation details")
			.findByProps({ accessibilityLabel: "Upgrade" })
			.props.onPress();
	});
	await act(async () => {});
	expect(upgrades).toBe(1);
	expect(
		subtreeText(modalContaining(tree, "Installation details")),
	).toContain("Checked for upgrades.");
});

it("walls a fatal close with the reason its retry cannot clear yet", async () => {
	const hub = new FakeClient("ready");
	hub.on("evener/plugin/list", () => ({ plugins: [plugin("kept")] }));
	harness.connection = connection(hub, "ready");
	const tree = render(<PluginsScreen {...props} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("kept");

	// The connection closes fatally - a protocol mismatch. The wall that
	// replaces the screen must say WHY: without the compatibility copy the
	// connection carries, the wall's own reconnect reads as ineffective -
	// nothing says why pressing it changes nothing. With it, the action is
	// the copy's own last word ("...then reconnect"), the way back once the
	// app and hub are updated together.
	harness.connection = {
		...connection(hub, "closed"),
		fatal: true,
		error:
			"This app and hub need compatible versions. Update them together, then reconnect.",
	};
	await act(async () => {
		tree.update(<PluginsScreen {...props} />);
	});
	const walled = renderedText(tree);
	expect(walled).toContain("Connect to Work hub to manage plugins.");
	expect(walled).toContain(
		"This app and hub need compatible versions. Update them together, then reconnect.",
	);
	expect(
		tree.root.findAllByProps({ accessibilityLabel: "Reconnect" }),
	).toHaveLength(1);
});

it("shows the connection status and reconnect inside the add-marketplace modal", async () => {
	const hub = new FakeClient("ready");
	hub.on("evener/marketplace/list", () => ({ marketplaces: [ACME] }));
	hub.on("evener/marketplace/browse", () => ({ name: "acme", plugins: [] }));
	const client = hub as unknown as ConversationClientLike;
	// The screen's half of the store wiring, in the minimal shape this
	// browser-level test needs: one store held for the whole render, and a
	// capture ref no add in this test ever fills.
	const marketplaces = createMarketplacesStore(client);
	const lastAddMarketplaces: {
		current: readonly MarketplaceEntry[] | null;
	} = { current: null };
	// ConnectionStatus inside the modal reads the connection itself, so the
	// harness must say what the browser's connectionState prop says - this
	// test does not inherit the state a sibling test leaves behind.
	harness.connection = connection(hub, "ready");
	const browser = (state: ConnectionState) => (
		<MarketplaceBrowser
			client={client}
			connectionState={state}
			hubName="Work hub"
			installed={createPluginsStore(client)}
			marketplaces={marketplaces}
			lastAddMarketplaces={lastAddMarketplaces}
			gate={createPluginMutationGate()}
			ready={state === "ready"}
			canUseConnection={() => state === "ready"}
			onOpenPlugin={() => {}}
			appliedRemovalNames={NO_APPLIED_REMOVALS}
			onAppliedRemoval={() => true}
			onAuthoritativeMarketplaces={() => {}}
			onMarketplaceAdded={() => {}}
			onRemovedMarketplace={() => {}}
		/>
	);
	const tree = render(browser("ready"));
	await act(async () => {});
	const add = tree.root.find(
		(node) => node.props.accessibilityLabel === "Add marketplace",
	);
	act(() => {
		add.props.onPress();
	});
	await act(async () => {});

	harness.connection = connection(hub, "reconnecting");
	await act(async () => {
		tree.update(browser("reconnecting"));
	});
	const modal = modalContaining(tree, "Git URL");
	expect(subtreeText(modal)).toContain("reconnecting");
	expect(
		modal.findAll(
			(node) => node.props.accessibilityLabel === "Reconnect",
		).length,
	).toBeGreaterThan(0);
	expect(subtreeText(modal)).toContain("Git URL");
});

it("treats a route re-keyed to another hub as a fresh screen", async () => {
	const fakeA = new FakeClient("ready");
	fakeA.on("evener/plugin/list", () => ({ plugins: [plugin("kept")] }));
	harness.connection = connection(fakeA, "ready");
	const forHub = (hubId: string) =>
		({ route: { params: { hubId } } }) as unknown as ComponentProps<
			typeof PluginsScreen
		>;
	const tree = render(<PluginsScreen {...forHub("hub-1")} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("kept");

	// The mounted instance is re-keyed to another hub while that hub's
	// connection is still opening. The retained client belongs to hub-1:
	// hub-2's screen must not render hub-1's list through it, and hub-1's
	// client must not hear another request.
	const fakeB = new FakeClient("connecting");
	fakeB.on("evener/plugin/list", () => ({ plugins: [plugin("from-b")] }));
	harness.connection = {
		activeProfile: { id: "hub-2", name: "Two hub" },
		client: null,
		state: "connecting",
		fatal: false,
		retry: () => {},
	};
	await act(async () => {
		tree.update(<PluginsScreen {...forHub("hub-2")} />);
	});
	const rekeyed = renderedText(tree);
	expect(rekeyed).not.toContain("kept");
	expect(rekeyed).toContain("to manage plugins.");
	expect(
		fakeA.calls.filter((call) => call.method === "evener/plugin/list")
			.length,
	).toBe(1);

	// The new hub is a fresh mount: its own list renders once its
	// connection is ready.
	fakeB.state = "ready";
	harness.connection = {
		activeProfile: { id: "hub-2", name: "Two hub" },
		client: fakeB,
		state: "ready",
		fatal: false,
		retry: () => {},
	};
	await act(async () => {
		tree.update(<PluginsScreen {...forHub("hub-2")} />);
	});
	await act(async () => {});
	expect(renderedText(tree)).toContain("from-b");
});

it("recovers a replacement client's failed first read when it becomes ready", async () => {
	const first = new FakeClient("ready");
	first.on("evener/plugin/list", () => ({ plugins: [plugin("kept")] }));
	harness.connection = connection(first, "ready");
	const tree = render(<PluginsScreen {...props} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("kept");

	// A manual retry opens a fresh client, and the screen sees it before it is
	// ready: the not-yet-ready replacement never displaces the previous client
	// (useRenderClient adopts it only once ready), so the mounted list keeps
	// the previous connection's rows under the banner - no doomed first read,
	// no error copy - and the replacement's own first read lands only once
	// the connection is ready.
	const second = new FakeClient("connecting");
	second.on("evener/plugin/list", () => ({
		plugins: [plugin("kept"), plugin("added-on-retry")],
	}));
	harness.connection = connection(second, "connecting");
	await act(async () => {
		tree.update(<PluginsScreen {...props} />);
	});
	expect(renderedText(tree)).toContain("kept");
	expect(renderedText(tree)).not.toContain("Could not load installed plugins");

	second.state = "ready";
	harness.connection = connection(second, "ready");
	await act(async () => {
		tree.update(<PluginsScreen {...props} />);
	});
	await act(async () => {});
	await act(async () => {});
	expect(renderedText(tree)).toContain("added-on-retry");
	expect(renderedText(tree)).not.toContain("Could not load installed plugins");
});

it("re-reads the marketplaces the browse panel shows when the connection is ready again", async () => {
	const hub = new FakeClient("ready");
	let reads = 0;
	hub.on("evener/marketplace/list", () => {
		reads += 1;
		return {
			marketplaces:
				reads === 1
					? [ACME]
					: [
							ACME,
							{
								name: "added-while-away",
								source: { kind: "github", repo: "late/plugins" },
								lastUpdated: 1,
							},
						],
		};
	});
	// The store's connection wiring is the screen's now, so the recovery this
	// pins is driven the way the app drives it: through the mounted screen.
	harness.connection = connection(hub, "ready");
	const tree = render(<PluginsScreen {...props} />);
	await act(async () => {});
	// Switch to the browse tab, which mounts MarketplaceBrowser.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Browse" }).props.onPress();
	});
	await act(async () => {});
	expect(reads).toBe(1);

	harness.connection = connection(hub, "reconnecting");
	await act(async () => {
		tree.update(<PluginsScreen {...props} />);
	});
	harness.connection = connection(hub, "ready");
	await act(async () => {
		tree.update(<PluginsScreen {...props} />);
	});
	await act(async () => {});
	await act(async () => {});
	// The marketplaces store is driven through the same recovery: the list it
	// already read is read again, and a change made while it was away lands.
	expect(reads).toBe(2);
	expect(renderedText(tree)).toContain("added-while-away");
});
